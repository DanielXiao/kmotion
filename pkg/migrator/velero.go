package migrator

import (
	"fmt"
	"github.com/danielxiao/kmotion/pkg/client"
	"github.com/sirupsen/logrus"
	velerov1api "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
	"github.com/vmware-tanzu/velero/pkg/backup"
	"github.com/vmware-tanzu/velero/pkg/builder"
	veleroclient "github.com/vmware-tanzu/velero/pkg/client"
	velerodiscovery "github.com/vmware-tanzu/velero/pkg/discovery"
	"github.com/vmware-tanzu/velero/pkg/plugin/clientmgmt"
	"github.com/vmware-tanzu/velero/pkg/podexec"
	"github.com/vmware-tanzu/velero/pkg/restore"
	"io"
	"k8s.io/client-go/rest"
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

func runBackup(logger *logrus.Logger, name string, restConfig *rest.Config, plugins string, backupFile io.Writer, namespaces []string) (*velerov1api.Backup, error) {
	f:= client.NewFactory(name, restConfig)
	kubeClient, err := f.KubeClient()
	if err != nil {
		return nil, err
	}

	veleroClient, err := f.Client()
	if err != nil {
		return nil, err
	}

	dynamicClient, err := f.DynamicClient()
	if err != nil {
		return nil, err
	}

	kubeClientConfig, err := f.ClientConfig()
	if err != nil {
		return nil, err
	}

	//Register internal and custom plugins during process start
	pluginRegistry := clientmgmt.NewRegistry(plugins, logger, logger.Level)
	if err := pluginRegistry.DiscoverPlugins(); err != nil {
		return nil, err
	}

	//Get backup plugin instances
	pluginManager := clientmgmt.NewManager(logger, logger.Level, pluginRegistry)
	defer pluginManager.CleanupClients()
	actions, err := pluginManager.GetBackupItemActions()
	if err != nil {
		return nil, err
	}

	//Initialize discovery helper
	discoveryHelper, err := velerodiscovery.NewHelper(veleroClient.Discovery(), logger)
	if err != nil {
		return nil, err
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
		return nil, err
	}

	//Run the backup
	backupParams := builder.ForBackup("default", name).IncludedNamespaces(namespaces...).ExcludedResources("persistentvolumeclaims", "persistentvolumes").DefaultVolumesToRestic(false).Result()
	backupReq := backup.Request{
		Backup: backupParams,
	}
	if err = k8sBackupper.Backup(logger, &backupReq, backupFile, actions, pluginManager); err != nil {
		return nil, err
	} else {
		logger.Infof("Ran backup against %s successfully", restConfig.Host)
	}
	return backupParams, nil
}

func runRestore(logger *logrus.Logger, name string, restConfig *rest.Config, plugins string, backupFile io.Reader, backup *velerov1api.Backup) (*velerov1api.Restore, error) {
	f := client.NewFactory(name, restConfig)
	kubeClient, err := f.KubeClient()
	if err != nil {
		return nil, err
	}

	veleroClient, err := f.Client()
	if err != nil {
		return nil, err
	}

	dynamicClient, err := f.DynamicClient()
	if err != nil {
		return nil, err
	}

	kubeClientConfig, err := f.ClientConfig()
	if err != nil {
		return nil, err
	}

	//Register internal and custom plugins during process start
	pluginRegistry := clientmgmt.NewRegistry(plugins, logger, logger.Level)
	if err := pluginRegistry.DiscoverPlugins(); err != nil {
		return nil, err
	}

	//Get restore plugin instances
	pluginManager := clientmgmt.NewManager(logger, logger.Level, pluginRegistry)
	defer pluginManager.CleanupClients()
	actions, err := pluginManager.GetRestoreItemActions()
	if err != nil {
		return nil, err
	}

	//Initialize discovery helper
	discoveryHelper, err := velerodiscovery.NewHelper(veleroClient.Discovery(), logger)
	if err != nil {
		return nil, err
	}

	//Intitialize KubernetesRestorer
	k8sRestorer, err := restore.NewKubernetesRestorer(
		veleroClient.VeleroV1(),
		discoveryHelper,
		veleroclient.NewDynamicFactory(dynamicClient),
		defaultRestorePriorities,
		kubeClient.CoreV1().Namespaces(),
		nil,
		240 * time.Minute,
		10 * time.Minute,
		logger,
		podexec.NewPodCommandExecutor(kubeClientConfig, kubeClient.CoreV1().RESTClient()),
		kubeClient.CoreV1().RESTClient(),
	)
	if err != nil {
		return nil, err
	}

	//Run the restore
	restoreParams := builder.ForRestore("default", name).ExcludedResources(nonRestorableResources...).Backup(backup.Name).RestorePVs(false).Result()
	restoreReq := restore.Request{
		Log:              logger,
		Restore:          restoreParams,
		Backup:           backup,
		BackupReader:     backupFile,
	}
	restoreWarnings, restoreErrors := k8sRestorer.Restore(restoreReq, actions, nil, pluginManager)
	if len(restoreWarnings.Namespaces) > 0 || len(restoreWarnings.Velero) > 0 || len(restoreWarnings.Cluster) > 0  {
		logger.Warning(restoreWarnings)
	}
	//TODO reformat the error
	if len(restoreErrors.Namespaces) > 0 || len(restoreErrors.Velero) > 0 || len(restoreErrors.Cluster) > 0  {
		return restoreParams, fmt.Errorf("%s", restoreErrors)
	} else {
		logger.Infof("Ran restore against %s successfully", restConfig.Host)
	}
	return restoreParams, nil
}
