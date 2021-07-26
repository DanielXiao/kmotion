package migrator

import (
	"fmt"
	"github.com/danielxiao/kmotion/pkg/client"
	"github.com/vmware-tanzu/velero/pkg/backup"
	"github.com/vmware-tanzu/velero/pkg/builder"
	veleroclient "github.com/vmware-tanzu/velero/pkg/client"
	velerodiscovery "github.com/vmware-tanzu/velero/pkg/discovery"
	"github.com/vmware-tanzu/velero/pkg/plugin/clientmgmt"
	"github.com/vmware-tanzu/velero/pkg/podexec"
	"github.com/vmware-tanzu/velero/pkg/restore"
	"time"
)

var nonRestorableResources = []string{
	"nodes",
	"events",
	"events.events.k8s.io",
}

var defaultRestorePriorities = []string{
	"customresourcedefinitions",
	"namespaces",
	"storageclasses",
	"volumesnapshotclass.snapshot.storage.k8s.io",
	"volumesnapshotcontents.snapshot.storage.k8s.io",
	"volumesnapshots.snapshot.storage.k8s.io",
	"persistentvolumes",
	"persistentvolumeclaims",
	"secrets",
	"configmaps",
	"serviceaccounts",
	"limitranges",
	"pods",
	"replicasets.apps",
	"clusters.cluster.x-k8s.io",
	"clusterresourcesets.addons.cluster.x-k8s.io",
}

func (t *Task) runBackup() error {
	restConfig, err := t.PlanResources.SrcMigCluster.BuildRestConfig(t.Client)
	if err != nil {
		return err
	}

	f := client.NewFactory(t.UID(), restConfig)
	kubeClient, err := f.KubeClient()
	if err != nil {
		return err
	}

	veleroClient, err := f.Client()
	if err != nil {
		return err
	}

	dynamicClient, err := f.DynamicClient()
	if err != nil {
		return err
	}

	kubeClientConfig, err := f.ClientConfig()
	if err != nil {
		return err
	}

	//Register internal and custom plugins during process start
	pluginRegistry := clientmgmt.NewRegistry(t.PluginDir, t.Log, t.Log.Level)
	if err := pluginRegistry.DiscoverPlugins(); err != nil {
		return err
	}

	//Get backup plugin instances
	pluginManager := clientmgmt.NewManager(t.Log, t.Log.Level, pluginRegistry)
	defer pluginManager.CleanupClients()
	actions, err := pluginManager.GetBackupItemActions()
	if err != nil {
		return err
	}

	//Initialize discovery helper
	discoveryHelper, err := velerodiscovery.NewHelper(veleroClient.Discovery(), t.Log)
	if err != nil {
		return err
	}

	//Intitialize kubernetesBackupper
	k8sBackupper, err := backup.NewKubernetesBackupper(
		veleroClient.VeleroV1(),
		discoveryHelper,
		veleroclient.NewDynamicFactory(dynamicClient),
		podexec.NewPodCommandExecutor(kubeClientConfig, kubeClient.CoreV1().RESTClient()),
		nil,
		0,
		false,
	)
	if err != nil {
		return err
	}

	//Run the backup
	backupParams := builder.ForBackup("default", t.planUID()).IncludedNamespaces(t.sourceNamespaces()...).ExcludedResources("persistentvolumeclaims", "persistentvolumes").DefaultVolumesToRestic(false).Result()
	backupReq := backup.Request{
		Backup: backupParams,
	}
	t.Log.Infof("Run backup against %s", restConfig.Host)
	if err = k8sBackupper.Backup(t.Log, &backupReq, t.BackupFile, actions, pluginManager); err != nil {
		return err
	}
	t.Backup = backupParams
	return nil
}

func (t *Task) runRestore() error {
	restConfig, err := t.PlanResources.DestMigCluster.BuildRestConfig(t.Client)
	if err != nil {
		return err
	}
	f := client.NewFactory(t.UID(), restConfig)
	kubeClient, err := f.KubeClient()
	if err != nil {
		return err
	}

	veleroClient, err := f.Client()
	if err != nil {
		return err
	}

	dynamicClient, err := f.DynamicClient()
	if err != nil {
		return err
	}

	kubeClientConfig, err := f.ClientConfig()
	if err != nil {
		return err
	}

	//Register internal and custom plugins during process start
	pluginRegistry := clientmgmt.NewRegistry(t.PluginDir, t.Log, t.Log.Level)
	if err := pluginRegistry.DiscoverPlugins(); err != nil {
		return err
	}

	//Get restore plugin instances
	pluginManager := clientmgmt.NewManager(t.Log, t.Log.Level, pluginRegistry)
	defer pluginManager.CleanupClients()
	actions, err := pluginManager.GetRestoreItemActions()
	if err != nil {
		return err
	}

	//Initialize discovery helper
	discoveryHelper, err := velerodiscovery.NewHelper(veleroClient.Discovery(), t.Log)
	if err != nil {
		return err
	}

	//Intitialize KubernetesRestorer
	k8sRestorer, err := restore.NewKubernetesRestorer(
		veleroClient.VeleroV1(),
		discoveryHelper,
		veleroclient.NewDynamicFactory(dynamicClient),
		defaultRestorePriorities,
		kubeClient.CoreV1().Namespaces(),
		nil,
		240*time.Minute,
		10*time.Minute,
		t.Log,
		podexec.NewPodCommandExecutor(kubeClientConfig, kubeClient.CoreV1().RESTClient()),
		kubeClient.CoreV1().RESTClient(),
	)
	if err != nil {
		return err
	}

	//Run the restore
	restoreParams := builder.ForRestore("default", t.planUID()).ExcludedResources(nonRestorableResources...).Backup(t.Backup.Name).RestorePVs(false).Result()
	restoreReq := restore.Request{
		Log:          t.Log,
		Restore:      restoreParams,
		Backup:       t.Backup,
		BackupReader: t.BackupFile,
	}
	t.Log.Infof("Run restore against %s", restConfig.Host)
	restoreWarnings, restoreErrors := k8sRestorer.Restore(restoreReq, actions, nil, pluginManager)
	if len(restoreWarnings.Namespaces) > 0 || len(restoreWarnings.Velero) > 0 || len(restoreWarnings.Cluster) > 0 {
		t.Log.Warning(restoreWarnings)
	}
	//TODO reformat the error
	if len(restoreErrors.Namespaces) > 0 || len(restoreErrors.Velero) > 0 || len(restoreErrors.Cluster) > 0 {
		return fmt.Errorf("%s", restoreErrors)
	}
	return nil
}
