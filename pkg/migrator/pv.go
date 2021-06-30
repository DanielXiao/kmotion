package migrator

import (
	"context"
	"fmt"
	migapi "github.com/danielxiao/mig-controller/pkg/apis/migration/v1alpha1"
	"github.com/danielxiao/mig-controller/pkg/controller/migplan"
	liberr "github.com/konveyor/controller/pkg/error"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vslm"
	corev1 "k8s.io/api/core/v1"
	"regexp"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ReclaimPolicyAnnotation = "migrator.run.tanzu.vmware.com/PreReclaimPolicy"
	FCDIDAnnotation = "migrator.run.tanzu.vmware.com/FCDID"
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
		t.Logger.Infof("Change PV %s reclaim policy from %s to %s", pv.Name, reclaimPolicy, corev1.PersistentVolumeReclaimRetain)
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
		t.Logger.Infof("Destination cluster does not have storage class %s, skip registering FCD", VSphereCSIDriverName)
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
	t.Logger.Debugf("%s:\n %s", CSIConfFile, data)
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
				t.Logger.Debugf("PV: %s, volumePath: %s", pv.Name, volumePath)
				vStorageObject, err := globalObjectManager.RegisterDisk(context.TODO(), volumePath, "")
				if err != nil {
					return liberr.Wrap(err)
				} else {
					id := vStorageObject.Config.Id.Id
					t.Logger.Infof("Registered %s as FCD %s", pv.Name, id)
					pvResource.Annotations[FCDIDAnnotation] = id
					err = srcClient.Update(context.TODO(), pvResource)
					if err != nil {
						return liberr.Wrap(err)
					}
				}
			} else {
				t.Logger.Infof("PV %s is already registed as FCD %s, skipping", pv.Name, fcdID)
			}
		} else {
			t.Logger.Infof("Source PV provisioner %s, Destination PV provisioner %s, no need to register FCD", srcProvisioner, destProvisioner)
		}
	}
	return nil
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
