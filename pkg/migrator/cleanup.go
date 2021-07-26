package migrator

import (
	"context"
	migapi "github.com/konveyor/mig-controller/pkg/apis/migration/v1alpha1"
	"github.com/konveyor/mig-controller/pkg/gvk"
	velerov1 "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
	corev1 "k8s.io/api/core/v1"
	k8serror "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
	"strings"
)

// ensureMigratedPVsUnmounted Ensure migrated PVs unmounted by checking migrated Pods deleted
func (t *Task) ensureMigratedPVsUnmounted() (bool, error) {
	podList := &corev1.PodList{}
	matchingLabels := k8sclient.MatchingLabels(map[string]string{
		velerov1.RestoreNameLabel: t.planUID(),
	})
	err := t.DestClient.List(context.TODO(), podList, matchingLabels)
	if err != nil {
		return false, err
	}
	var pods []string
	if len(podList.Items) > 0 {
		for _, item := range podList.Items {
			if len(item.Spec.Volumes) > 0 {
				for _, volume := range item.Spec.Volumes {
					if volume.PersistentVolumeClaim != nil {
						pods = append(pods, item.Name)
					}
				}
			}
		}
	}
	if len(pods) > 0 {
		t.Log.Infof("Remaining Pods: %s mounting PVC", pods)
		return false, nil
	} else {
		return true, nil
	}
}

// deleteMigratedNamespaceScopedResources Delete migrated namespace-scoped resources on dest cluster
func (t *Task) deleteMigratedNamespaceScopedResources() error {
	t.Log.Info("Scanning all GVKs in all migrated namespaces for " +
		"MigPlan associated resources to delete.")
	client, GVRs, err := gvk.GetNamespacedGVRsForCluster(t.PlanResources.DestMigCluster, t.Client)
	if err != nil {
		return err
	}

	clientListOptions := k8sclient.ListOptions{}
	matchingLabels := k8sclient.MatchingLabels(map[string]string{
		velerov1.RestoreNameLabel: t.planUID(),
	})
	matchingLabels.ApplyToList(&clientListOptions)
	listOptions := clientListOptions.AsListOptions()
	for _, gvr := range filterNamespacedGVKs(&GVRs) {
		for _, ns := range t.destinationNamespaces() {
			t.Log.Infof("Scan resource %s in ns %s with label %s", gvr.String(), ns, listOptions.LabelSelector)
			list, err := client.Resource(gvr).Namespace(ns).List(context.TODO(), *listOptions)
			if err != nil {
				t.Log.Error(err)
				continue
			}
			for _, r := range list.Items {
				t.Log.Infof("Delete %s %s/%s", gvr.String(), ns, r.GetName())
				err = client.Resource(gvr).Namespace(ns).Delete(context.TODO(), r.GetName(), metav1.DeleteOptions{})
				if err != nil {
					// Will ignore the ones that were removed, or for some reason are not supported
					// Assuming that main resources will be removed, such as pods
					if k8serror.IsMethodNotSupported(err) || k8serror.IsNotFound(err) {
						t.Log.Warning(err)
						continue
					}
					t.Log.Errorf("Failed to delete %s %s: %s\n", gvr.String(), r.GetName(), err.Error())
					return err
				}
			}
		}
	}
	return nil
}

func filterNamespacedGVKs(origin *[]schema.GroupVersionResource) []schema.GroupVersionResource {
	var list, filter []schema.GroupVersionResource
	for _, gvr := range *origin {
		if (gvr.Resource == "pods" || gvr.Resource == "endpoints") && gvr.Group == "" {
			filter = append(filter, gvr)
			continue
		}
		if gvr.Group == "metrics.k8s.io" ||
			gvr.Resource == "events" ||
			strings.Contains(gvr.Group, "antrea.tanzu.vmware.com") {
			continue
		}
		list = append(list, gvr)
	}
	list = append(list, filter...)
	return list
}

func filterClusterGVKs(origin *[]schema.GroupVersionResource) []schema.GroupVersionResource {
	var list, filter []schema.GroupVersionResource
	for _, gvr := range *origin {
		if gvr.Resource == "nodes" ||
			gvr.Group == "node.k8s.io" ||
			gvr.Group == "flowcontrol.apiserver.k8s.io" ||
			gvr.Group == "storage.k8s.io" ||
			strings.Contains(gvr.Group, "antrea.tanzu.vmware.com") {
			continue
		}
		list = append(list, gvr)
	}
	list = append(list, filter...)
	return list
}

// deleteMigratedClusterScopedResources Delete migrated cluster-scoped resources on dest cluster
func (t *Task) deleteMigratedClusterScopedResources() error {
	t.Log.Info("Scanning all cluster scoped GVKs " +
		"MigPlan associated resources to delete.")
	client, GVRs, err := gvk.GetClusterScopedGVRsForCluster(t.PlanResources.DestMigCluster, t.Client)
	if err != nil {
		return err
	}

	clientListOptions := k8sclient.ListOptions{}
	matchingLabels := k8sclient.MatchingLabels(map[string]string{
		velerov1.RestoreNameLabel: t.planUID(),
	})
	matchingLabels.ApplyToList(&clientListOptions)
	listOptions := clientListOptions.AsListOptions()
	for _, gvr := range filterClusterGVKs(&GVRs) {
		t.Log.Infof("Scan resource %s with label %s", gvr.String(), listOptions.LabelSelector)
		list, err := client.Resource(gvr).List(context.TODO(), *listOptions)
		if err != nil {
			t.Log.Error(err)
			continue
		}
		for _, r := range list.Items {
			t.Log.Infof("Delete %s %s", gvr.String(), r.GetName())
			err = client.Resource(gvr).Delete(context.TODO(), r.GetName(), metav1.DeleteOptions{})
			if err != nil {
				// Will ignore the ones that were removed, or for some reason are not supported
				if k8serror.IsMethodNotSupported(err) || k8serror.IsNotFound(err) {
					t.Log.Warning(err)
					continue
				}
				t.Log.Errorf("Failed to delete %s %s: %s\n", gvr.String(), r.GetName(), err.Error())
				return err
			}
		}
	}

	namespaceList := &corev1.NamespaceList{}
	matchingPlanLabels := k8sclient.MatchingLabels(map[string]string{
		migapi.MigPlanLabel: t.planUID(),
	})
	err = t.DestClient.List(context.TODO(), namespaceList, matchingPlanLabels)
	if err != nil {
		return err
	}
	for _, ns := range namespaceList.Items {
		t.Log.Infof("Delete namespace %s", ns.Name)
		err = t.DestClient.Delete(context.TODO(), &ns)
		if err != nil {
			return err
		}
	}

	return nil
}
