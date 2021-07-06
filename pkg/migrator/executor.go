package migrator

import (
	"context"
	"fmt"
	migapi "github.com/konveyor/mig-controller/pkg/apis/migration/v1alpha1"
	"github.com/konveyor/mig-controller/pkg/controller/migmigration"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/types"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func Migrate(logger *logrus.Logger, pluginDir, cacheDir string, client k8sclient.Client, namespacedName types.NamespacedName, planResources *migapi.PlanResources) error {
	srcClient, err := planResources.SrcMigCluster.GetClient(client)
	if err != nil {
		return err
	}
	destClient, err := planResources.DestMigCluster.GetClient(client)
	if err != nil {
		return err
	}
	t := &Task{
		Log:           logger,
		PluginDir:     pluginDir,
		CacheDir:      cacheDir,
		Client:        client,
		PlanResources: planResources,
		SrcClient:     srcClient,
		DestClient:    destClient,
	}
	for {
		// Get Migration
		migration := &migapi.MigMigration{}
		err := client.Get(context.TODO(), namespacedName, migration)
		if err != nil {
			return err
		}
		t.Owner = migration
		migration.Status.BeginStagingConditions()
		// Run a phase
		err = t.Run()
		if err != nil {
			t.Fail(MigrationFailed, []string{err.Error()})
			// Update migration
			err = client.Update(context.TODO(), migration)
			if err != nil {
				return err
			}
			continue
		}

		// Update phase in status
		migration.Status.Phase = t.Phase
		migration.Status.Itinerary = t.Itinerary.Name

		// Completed
		if t.Phase == Completed {
			migration.Status.DeleteCondition(migapi.Running)
			failed := t.Owner.Status.FindCondition(migapi.Failed)
			warnings := t.Owner.Status.FindConditionByCategory(migapi.Warn)
			if failed == nil && len(warnings) == 0 {
				migration.Status.SetCondition(migapi.Condition{
					Type:     migapi.Succeeded,
					Status:   migapi.True,
					Reason:   t.Phase,
					Category: migapi.Advisory,
					Message:  "The migration has completed successfully.",
					Durable:  true,
				})
			}
			if failed == nil && len(warnings) > 0 {
				migration.Status.SetCondition(migapi.Condition{
					Type:     migmigration.SucceededWithWarnings,
					Status:   migapi.True,
					Reason:   t.Phase,
					Category: migmigration.Advisory,
					Message:  "The migration has completed with warnings, please look at `Warn` conditions.",
					Durable:  true,
				})
			}
			migration.Status.EndStagingConditions()
			// Update migration
			err = client.Update(context.TODO(), migration)
			if err != nil {
				return err
			}
			break
		}

		//Report running progress
		phase, n, total := t.Itinerary.progressReport(t.Phase)
		message := fmt.Sprintf("Step: %d/%d", n, total)
		migration.Status.SetCondition(migapi.Condition{
			Type:     migapi.Running,
			Status:   migapi.True,
			Reason:   phase,
			Category: migapi.Advisory,
			Message:  message,
		})
		migration.Status.EndStagingConditions()
		// Update migration
		err = client.Update(context.TODO(), migration)
		if err != nil {
			return err
		}
	}
	return nil
}
