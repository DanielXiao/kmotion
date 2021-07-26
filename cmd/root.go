package cmd

import (
	"context"
	"fmt"
	"github.com/danielxiao/kmotion/pkg/client"
	"github.com/danielxiao/kmotion/pkg/migrator"
	"github.com/konveyor/mig-controller/pkg/apis"
	migapi "github.com/konveyor/mig-controller/pkg/apis/migration/v1alpha1"
	"github.com/spf13/cobra"
	"github.com/vmware-tanzu/velero/pkg/cmd/server/plugin"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"os"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
	"strings"
)

var (
	rootCmd = &cobra.Command{
		Use:   "kmotion",
		Short: "Migrate workloads from one Kubernetes cluster to another",
		Long:  "Migrate workloads from one Kubernetes cluster to another",
	}
	kubeClient     k8sclient.Client
	planResources  *migapi.PlanResources
	namespacedName types.NamespacedName
)

func init() {
	var err error
	kubeClient, err = getKubeClient(os.Getenv("KUBECONFIG"))
	checkError(err)
	names := strings.Split(os.Getenv("MIGRATION_NAME"), string(types.Separator))
	if len(names) != 2 {
		fmt.Fprintln(os.Stderr, fmt.Errorf("MIGRATION_NAME is not set correctly"))
		os.Exit(1)
	}
	namespacedName = types.NamespacedName{Name: names[1], Namespace: names[0]}
	migration := &migapi.MigMigration{}
	err = kubeClient.Get(context.TODO(), namespacedName, migration)
	checkError(err)
	plan, err := migration.GetPlan(kubeClient)
	checkError(err)
	planResources, err = plan.GetRefResources(kubeClient)
	checkError(err)
	var cluster *migapi.MigCluster
	if migration.Status.Phase == migrator.ExportSrcManifests {
		cluster = planResources.SrcMigCluster
	} else if migration.Status.Phase == migrator.ImportManifestsToDest {
		cluster = planResources.DestMigCluster
	}
	if len(os.Args) > 1 && os.Args[1] == "run-plugins" {
		if cluster != nil {
			restConfig, err := cluster.BuildRestConfig(kubeClient)
			checkError(err)
			rootCmd.AddCommand(plugin.NewCommand(client.NewFactory(string(migration.UID), restConfig)))
		} else {
			fmt.Fprintln(os.Stderr, fmt.Errorf("can not execute run-plugins during phase %s", migration.Status.Phase))
			os.Exit(1)
		}
	}
}

func Execute() {
	err := rootCmd.Execute()
	checkError(err)
}

func checkError(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func getKubeClient(kubeconfigPath string) (k8sclient.Client, error) {
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, err
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := apis.AddToScheme(scheme); err != nil {
		return nil, err
	}
	return k8sclient.New(config, k8sclient.Options{Scheme: scheme})
}
