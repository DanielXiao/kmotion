package migrator

import (
	"context"
	"fmt"
	migapi "github.com/danielxiao/mig-controller/pkg/apis/migration/v1alpha1"
	"github.com/danielxiao/mig-controller/pkg/controller/migplan"
	"github.com/google/uuid"
	liberr "github.com/konveyor/controller/pkg/error"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vslm"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"regexp"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ReclaimPolicyAnnotation = "migrator.run.tanzu.vmware.com/PreReclaimPolicy"
	FCDIDAnnotation = "migrator.run.tanzu.vmware.com/FCDID"
	ProvisionedByAnnotation = "pv.kubernetes.io/provisioned-by"
	CSISecretName           = "vsphere-config-secret"
	CSISecretNameSpace      = "kube-system"
	CSIConfFile             = "csi-vsphere.conf"
	VSphereInTreeDriverName = migplan.VSphereInTreeDriverName
	VSphereCSIDriverName    = migplan.VSphereCSIDriverName
)

// getPVs Get the persistent volumes included in the plan which are included in the
// backup/restore process
// This function will only return PVs that are being copied
// and any PVs selected for move.
func (t *Task) getPVs() migapi.PersistentVolumes {
	volumes := []migapi.PV{}
	for _, pv := range t.PlanResources.MigPlan.Spec.PersistentVolumes.List {
		if pv.Selection.Action == migapi.PvSkipAction {
			continue
		}
		volumes = append(volumes, pv)
	}
	pvList := t.PlanResources.MigPlan.Spec.PersistentVolumes.DeepCopy()
	pvList.List = volumes
	return *pvList
}

// changePVReclaimPolicy set reclaim policy of target PVs to retain
func (t *Task) changePVReclaimPolicy() error {
	pvs := t.getPVs()
	client, err := t.getSourceClient()
	if err != nil {
		return liberr.Wrap(err)
	}
	for _, pv := range pvs.List {
		pvResource := &corev1.PersistentVolume{}
		err = client.Get(context.TODO(), k8sclient.ObjectKey{Name: pv.Name}, pvResource)
		if err != nil {
			return liberr.Wrap(err)
		}
		if pvResource.Annotations == nil {
			pvResource.Annotations = make(map[string]string)
		}
		reclaimPolicy := string(pvResource.Spec.PersistentVolumeReclaimPolicy)
		t.Log.Infof("Change PV %s reclaim policy from %s to %s", pv.Name, reclaimPolicy, corev1.PersistentVolumeReclaimRetain)
		pvResource.Annotations[migapi.PvActionAnnotation] = pv.Selection.Action
		pvResource.Annotations[migapi.PvStorageClassAnnotation] = pv.Selection.StorageClass
		pvResource.Annotations[ReclaimPolicyAnnotation] = reclaimPolicy
		pvResource.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimRetain
		err = client.Update(context.TODO(), pvResource)
		if err != nil {
			return liberr.Wrap(err)
		}
	}
	return nil
}

func (t *Task) registerFCD() error {
	isDestVsphereCSI := false
	for _, sc := range t.PlanResources.MigPlan.Status.DestStorageClasses {
		if sc.Provisioner == VSphereCSIDriverName {
			isDestVsphereCSI = true
			break
		}
	}
	if !isDestVsphereCSI {
		t.Log.Infof("Destination cluster does not have storage class %s, skip registering FCD", VSphereCSIDriverName)
		return nil
	}

	destClient, err := t.getDestinationClient()
	if err != nil {
		return liberr.Wrap(err)
	}
	secret := &corev1.Secret{}
	err = destClient.Get(context.TODO(), k8sclient.ObjectKey{Name: CSISecretName, Namespace: CSISecretNameSpace}, secret)
	if err != nil {
		return liberr.Wrap(err)
	}
	data := string(secret.Data[CSIConfFile])
	vCenter, err := grepCSIConf(data, "VirtualCenter \"(.+)\"")
	if err != nil {
		return liberr.Wrap(err)
	}
	user, err := grepCSIConf(data, "user = \"(.+)\"")
	if err != nil {
		return liberr.Wrap(err)
	}
	password, err := grepCSIConf(data, "password = \"(.+)\"")
	if err != nil {
		return liberr.Wrap(err)
	}
	datacenter, err := grepCSIConf(data, "datacenters = \"(.+)\"")
	if err != nil {
		return liberr.Wrap(err)
	}

	globalObjectManager, err := getVStorageObjectManager(vCenter, user, password)
	if err != nil {
		return liberr.Wrap(err)
	}
	srcClient, err := t.getSourceClient()
	if err != nil {
		return liberr.Wrap(err)
	}
	pvs := t.getPVs()
	for _, pv := range pvs.List {
		if pv.NFS != nil {
			t.Log.Infof("PV %s is nfs type, skip registering FCD", pv.Name)
			continue
		}
		srcProvisioner := getProvisioner(pv.StorageClass, t.PlanResources.MigPlan.Status.SrcStorageClasses)
		destProvisioner := getProvisioner(pv.Selection.StorageClass, t.PlanResources.MigPlan.Status.DestStorageClasses)
		if srcProvisioner == VSphereInTreeDriverName && destProvisioner == VSphereCSIDriverName {
			pvResource := &corev1.PersistentVolume{}
			err = srcClient.Get(context.TODO(), k8sclient.ObjectKey{Name: pv.Name}, pvResource)
			if err != nil {
				return liberr.Wrap(err)
			}
			if pvResource.Annotations == nil {
				pvResource.Annotations = make(map[string]string)
			}
			if fcdID, ok := pvResource.Annotations[FCDIDAnnotation]; !ok {
				dsName, vmdkPath, err := grepPath(pvResource.Spec.VsphereVolume.VolumePath)
				if err != nil {
					return liberr.Wrap(err)
				}
				volumePath := fmt.Sprintf("https://%s/folder/%s?dcPath=%s&dsName=%s", vCenter, vmdkPath, datacenter, dsName)
				t.Log.Infof("Register PV as FCD: %s, volumePath: %s", pv.Name, volumePath)
				vStorageObject, err := globalObjectManager.RegisterDisk(context.TODO(), volumePath, "")
				if err != nil {
					return liberr.Wrap(err)
				} else {
					id := vStorageObject.Config.Id.Id
					t.Log.Infof("PV %s 's FCD %s", pv.Name, id)
					pvResource.Annotations[FCDIDAnnotation] = id
					err = srcClient.Update(context.TODO(), pvResource)
					if err != nil {
						return liberr.Wrap(err)
					}
				}
			} else {
				t.Log.Infof("PV %s is already registed as FCD %s, skipping", pv.Name, fcdID)
			}
		} else {
			t.Log.Infof("Source PV provisioner %s, Destination PV provisioner %s, skip registering FCD", srcProvisioner, destProvisioner)
		}
	}
	return nil
}

func (t *Task) ensurePVCBond() (bool, error) {
	destClient, err := t.getDestinationClient()
	if err != nil {
		return false, liberr.Wrap(err)
	}
	pvs := t.getPVs()
	for _, ns := range t.destinationNamespaces() {
		pvcResourceList := &corev1.PersistentVolumeClaimList{}
		err = destClient.List(context.TODO(), pvcResourceList, k8sclient.InNamespace(ns))
		if err != nil {
			return false, liberr.Wrap(err)
		}
		for _, pvcResource := range pvcResourceList.Items {
			if pvcResource.Status.Phase != corev1.ClaimBound {
				for _, pv := range pvs.List {
					if pv.PVC.Name == pvcResource.Name {
						t.Log.Infof("PVC %s is still in %s status", pvcResource.Name, pvcResource.Status.Phase)
						return false, nil
					}
				}
			} else {
				t.Log.Infof("PVC %s is in %s status", pvcResource.Name, corev1.ClaimBound)
			}
		}
	}
	return true, nil
}

func (t *Task) staticallyProvisionDestPV() error {
	srcClient, err := t.getSourceClient()
	if err != nil {
		return liberr.Wrap(err)
	}
	destClient, err := t.getDestinationClient()
	if err != nil {
		return liberr.Wrap(err)
	}
	pvs := t.getPVs()
	nsMap := t.PlanResources.MigPlan.GetNamespaceMapping()
	for _, pv := range pvs.List {
		if pv.Selection.Action != migapi.PvMoveAction {
			return fmt.Errorf("PV %s's action is %s, expect %s", pv.Name, pv.Selection.Action, migapi.PvMoveAction)
		}
		if pv.NFS != nil {
			// TODO high priority
			return fmt.Errorf("PV %s is nfs type, not implemented yet", pv.Name)
		}
		srcPVResource := &corev1.PersistentVolume{}
		err = srcClient.Get(context.TODO(), k8sclient.ObjectKey{Name: pv.Name}, srcPVResource)
		if err != nil {
			return liberr.Wrap(err)
		}
		srcProvisioner := getProvisioner(pv.StorageClass, t.PlanResources.MigPlan.Status.SrcStorageClasses)
		destProvisioner := getProvisioner(pv.Selection.StorageClass, t.PlanResources.MigPlan.Status.DestStorageClasses)
		destPVCNamespace := nsMap[pv.PVC.Namespace]
		destPVName := fmt.Sprintf("import-%s", uuid.New().String())
		var destPVResource *corev1.PersistentVolume
		switch destProvisioner {
		case VSphereCSIDriverName:
			switch srcProvisioner {
			case VSphereInTreeDriverName:
				if fcdID, ok := srcPVResource.Annotations[FCDIDAnnotation]; ok {
					destPVResource = buildVSphereCSIPVSpec(
						destPVName,
						pv.PVC.Name,
						destPVCNamespace,
						srcPVResource.Spec.VsphereVolume.FSType,
						fcdID,
						srcPVResource.Spec.Capacity,
						srcPVResource.Spec.AccessModes,
						srcPVResource.Labels)
				} else {
					return fmt.Errorf("can not find %s from PV %s annotation", FCDIDAnnotation, pv.Name)
				}
			case VSphereCSIDriverName:
				fcdID := srcPVResource.Spec.CSI.VolumeHandle
				destPVResource = buildVSphereCSIPVSpec(
					destPVName,
					pv.PVC.Name,
					destPVCNamespace,
					srcPVResource.Spec.CSI.FSType,
					fcdID,
					srcPVResource.Spec.Capacity,
					srcPVResource.Spec.AccessModes,
					srcPVResource.Labels)
			}
		case VSphereInTreeDriverName:
			switch srcProvisioner {
			case VSphereInTreeDriverName:
				destPVResource = buildVSphereCSPPVSpec(
					destPVName,
					pv.PVC.Name,
					destPVCNamespace,
					srcPVResource.Spec.VsphereVolume.FSType,
					srcPVResource.Spec.VsphereVolume.VolumePath,
					srcPVResource.Spec.Capacity,
					srcPVResource.Spec.AccessModes,
					srcPVResource.Labels)
			case VSphereCSIDriverName:
				//TODO low priority
				return fmt.Errorf("not implement yet from %s to %s", srcProvisioner, destProvisioner)
			}

		}
		if destPVResource == nil {
			return fmt.Errorf("not supported from %s to %s", srcProvisioner, destProvisioner)
		}
		t.Log.Infof("Privision PV %s in destination cluster with spec:\n %s", destPVResource.Name, destPVResource.String())
		err := destClient.Create(context.TODO(), destPVResource)
		if err != nil {
			return liberr.Wrap(err)
		}
		srcPVCResource := &corev1.PersistentVolumeClaim{}
		err = srcClient.Get(context.TODO(), k8sclient.ObjectKey{Namespace: pv.PVC.Namespace, Name: pv.PVC.Name}, srcPVCResource)
		if err != nil {
			return liberr.Wrap(err)
		}
		empty := ""
		destPVCResource := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      srcPVCResource.Name,
				Namespace: destPVCNamespace,
				Labels:    srcPVCResource.Labels,
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes:      srcPVCResource.Spec.AccessModes,
				Resources:        srcPVCResource.Spec.Resources,
				StorageClassName: &empty,
				VolumeName:       destPVName,
			},
		}
		t.Log.Infof("Privision PVC %s in destination cluster with spec:\n %s", destPVCResource.Name, destPVCResource.String())
		err = destClient.Create(context.TODO(), destPVCResource)
		if err != nil {
			return liberr.Wrap(err)
		}
	}
	return nil
}

func buildVSphereCSIPVSpec(pvName, pvcName, pvcNamespace, fsType, fcdID string, capacity corev1.ResourceList, accessMode []corev1.PersistentVolumeAccessMode, labels map[string]string) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:        pvName,
			Annotations: map[string]string{ProvisionedByAnnotation: VSphereCSIDriverName},
			Labels:      labels,
		},
		Spec: corev1.PersistentVolumeSpec{
			Capacity:                      capacity,
			AccessModes:                   accessMode,
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			ClaimRef: &corev1.ObjectReference{
				Name: pvcName,
				Namespace: pvcNamespace,
			},
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver: VSphereCSIDriverName,
					FSType: fsType,
					VolumeAttributes: map[string]string{"type": "vSphere CNS Block Volume"},
					VolumeHandle: fcdID,
				},
			},
		},
	}
}

func buildVSphereCSPPVSpec(pvName, pvcName, pvcNamespace, fsType, volumePath string, capacity corev1.ResourceList, accessMode []corev1.PersistentVolumeAccessMode, labels map[string]string) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:        pvName,
			Annotations: map[string]string{ProvisionedByAnnotation: VSphereInTreeDriverName},
			Labels:      labels,
		},
		Spec: corev1.PersistentVolumeSpec{
			Capacity:                      capacity,
			AccessModes:                   accessMode,
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			ClaimRef: &corev1.ObjectReference{
				Name: pvcName,
				Namespace: pvcNamespace,
			},
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				VsphereVolume: &corev1.VsphereVirtualDiskVolumeSource{
					FSType: fsType,
					VolumePath: volumePath,
				},
			},
		},
	}
}

func getVStorageObjectManager(vCenter, user, password string) (*vslm.GlobalObjectManager, error) {
	vcUrl := fmt.Sprintf("https://%s:%s@%s/sdk", user, password, vCenter)
	url, err := soap.ParseURL(vcUrl)
	if err != nil {
		return nil, liberr.Wrap(err)
	}
	c, err := govmomi.NewClient(context.TODO(), url, true)
	if err != nil {
		return nil, liberr.Wrap(err)
	}
	vslmClient, err := vslm.NewClient(context.TODO(), c.Client)
	if err != nil {
		return nil, liberr.Wrap(err)
	}
	return vslm.NewGlobalObjectManager(vslmClient), nil
}

func getProvisioner(name string, storageClasses []migapi.StorageClass) string {
	for _, sc := range storageClasses {
		if name == sc.Name {
			return sc.Provisioner
		}
	}
	return ""
}

func grepCSIConf(data, exp string) (string, error) {
	r, _ := regexp.Compile(exp)
	value := r.FindStringSubmatch(data)
	if value != nil {
		return value[1], nil
	} else {
		return "", fmt.Errorf("exp %s failed to match %s", exp, data)
	}
}

func grepPath(data string) (string, string, error) {
	r, _ := regexp.Compile("\\[(.*)\\] (.*)")
	value := r.FindStringSubmatch(data)
	if value != nil {
		return value[1], value[2], nil
	} else {
		return "", "", fmt.Errorf("failed to grep datastore and path from %s", data)
	}
}
