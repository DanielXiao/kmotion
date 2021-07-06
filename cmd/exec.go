package cmd

import (
	"github.com/danielxiao/kmotion/pkg/migrator"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/vmware-tanzu/velero/pkg/util/logging"
	"log"
	"os"
)

var pluginDir, cacheDir string

var execCmd = &cobra.Command{
	Use:     "exec",
	Short:   "Execute workload migration",
	Long:    "Execute workload migration",
	Example: "kmotion exec -p /Users/yifengx/empty -c /Users/yifengx/mig",
	RunE: func(cmd *cobra.Command, args []string) error {
		// go-plugin uses log.Println to log when it's waiting for all plugin processes to complete so we need to
		// set its output to stdout.
		log.SetOutput(os.Stdout)
		// Make sure we log to stdout so cloud log dashboards don't show this as an error.
		logrus.SetOutput(os.Stdout)
		// Velero's DefaultLogger logs to stdout, so all is good there.
		logLevel := logging.LogLevelFlag(logrus.DebugLevel).Parse()
		format := logging.NewFormatFlag().Parse()
		logger := logging.DefaultLogger(logLevel, format)
		return migrator.Migrate(logger, pluginDir, cacheDir, kubeClient, namespacedName, planResources)
	},
}

func init() {
	rootCmd.AddCommand(execCmd)
	execCmd.Flags().StringVarP(&pluginDir, "plugin-dir", "p", "", "Velero custom plugin directory path")
	execCmd.Flags().StringVarP(&cacheDir, "cache-dir", "c", "", "Cache directory path")
}
