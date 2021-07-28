package migrator

import (
	"fmt"
	liberr "github.com/konveyor/controller/pkg/error"
	migapi "github.com/konveyor/mig-controller/pkg/apis/migration/v1alpha1"
	"github.com/konveyor/mig-controller/pkg/compat"
	"github.com/sirupsen/logrus"
	velerov1api "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
	"os"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
	"time"
)

var PollInterval = time.Millisecond * 500

// Phases
const (
	Started                        = "Started"
	PreExportHooks                 = "PreExportHooks"
	ExportSrcManifests             = "ExportSrcManifests"
	PostExportHooks                = "PostExportHooks"
	ChangePVReclaimPolicy          = "ChangePVReclaimPolicy"
	QuiesceApplications            = "QuiesceApplications"
	EnsureQuiesced                 = "EnsureQuiesced"
	RegisterFCD                    = "RegisterFCD"
	StaticallyProvisionDestPV      = "StaticallyProvisionDestPV"
	EnsurePVCBond                  = "EnsurePVCBond"
	PreImportHooks                 = "PreImportHooks"
	ImportManifestsToDest          = "ImportManifestsToDest"
	PostImportHooks                = "PostImportHooks"
	Verification                   = "Verification"
	MigrationFailed                = "MigrationFailed"
	DeleteMigratedNSResources      = "DeleteMigratedNSResources"
	EnsurePVsUnmounted             = "EnsurePVsUnmounted"
	DeleteMigratedPVs              = "DeleteMigratedPVs"
	DeleteMigratedClusterResources = "DeleteMigratedClusterResources"
	UnQuiesceSrcApplications       = "UnQuiesceSrcApplications"
	ResetPVReclaimPolicy           = "ResetPVReclaimPolicy"
	Canceling                      = "Canceling"
	Canceled                       = "Canceled"
	Rollback                       = "Rollback"
	Completed                      = "Completed"
)

var PhaseDescriptions = map[string]string{
	Started:                        "Migration started.",
	PreExportHooks:                 "Run hooks before export resources",
	ExportSrcManifests:             "Export resources from source cluster",
	PostExportHooks:                "Run hooks after export resources",
	ChangePVReclaimPolicy:          "Change target PV reclaim policy to retain in source cluster",
	QuiesceApplications:            "Quiescing (Scaling to 0 replicas): Deployments, DeploymentConfigs, StatefulSets, ReplicaSets, DaemonSets, CronJobs and Jobs.",
	EnsureQuiesced:                 "Waiting for Quiesce (Scaling to 0 replicas) to finish for Deployments, DeploymentConfigs, StatefulSets, ReplicaSets, DaemonSets, CronJobs and Jobs.",
	RegisterFCD:                    "Register target PVs as FCD",
	StaticallyProvisionDestPV:      "Statically Provision PVs in destination cluster",
	EnsurePVCBond:                  "Ensure provisioned PVC Bond with PV",
	PreImportHooks:                 "Run hooks before import resources",
	ImportManifestsToDest:          "Import resources to destination cluster",
	PostImportHooks:                "Run hooks after import resources",
	Verification:                   "Verify applications up and running",
	MigrationFailed:                "Migration failed",
	DeleteMigratedNSResources:      "Delete migrated namespace scoped resources",
	EnsurePVsUnmounted:             "Ensure migrated PVCs are unmounted",
	DeleteMigratedPVs:              "Delete migrated PVCs and PVs",
	DeleteMigratedClusterResources: "Delete migrated cluster scoped resources",
	UnQuiesceSrcApplications:       "UnQuiescing (Scaling to N replicas) source cluster Deployments, DeploymentConfigs, StatefulSets, ReplicaSets, DaemonSets, CronJobs and Jobs.",
	ResetPVReclaimPolicy:           "Reset PV reclaim policy to original value in source cluster",
	Canceling:                      "Canceling migration",
	Canceled:                       "Canceled migration",
	Rollback:                       "Rollback migration",
	Completed:                      "Migration is completed",
}

// Flags
const (
	Quiesce         = 0x001 // Only when QuiescePods (true).
	HasPVs          = 0x002 // Only when PVs migrated.
	HasVerify       = 0x004 // Only when the plan has enabled verification
	SourceOpenshift = 0x008 // True when the source cluster is a Openshift cluster
)

// Migration steps
const (
	StepPrepareMigration = "Prepare Migration"
	StepPrepareRollback  = "Prepare Rollback"
	StepExportResource   = "Export Resources"
	StepQuiesce          = "Quiesce Applications"
	StepMigratePV        = "Migrate PVs"
	StepImportResource   = "Import Resources"
	StepCleanupMigration = "Cleanup Migration"
	StepCleanupFail      = "Cleanup Fail"
	StepCleanupRollback  = "Cleanup Rollback"
	StepCleanupHelpers   = "CleanupHelpers"
	StepCleanupMigrated  = "Cleanup Migrated"
	StepUnquiesce        = "Unquiesce Applications"
)

// Phase defines phase in the migration
type Phase struct {
	// A phase name.
	Name string
	// High level Step this phase belongs to
	Step string
	// Step included when ALL flags evaluate true.
	all uint16
	// Step included when ANY flag evaluates true.
	any uint16
}

// Itinerary defines itinerary
type Itinerary struct {
	Name   string
	Phases []Phase
}

// GetStepForPhase returns which high level step current phase belongs to
func (r *Itinerary) GetStepForPhase(phaseName string) string {
	for _, phase := range r.Phases {
		if phaseName == phase.Name {
			return phase.Step
		}
	}
	return ""
}

// Get a progress report.
// Returns: phase, n, total.
func (r Itinerary) progressReport(phaseName string) (string, int, int) {
	n := 0
	total := len(r.Phases)
	for i, phase := range r.Phases {
		if phase.Name == phaseName {
			n = i + 1
			break
		}
	}

	return phaseName, n, total
}

var MoveItinerary = Itinerary{
	Name: "StatefulMovePV",
	Phases: []Phase{
		{Name: Started, Step: StepPrepareMigration},
		{Name: PreExportHooks, Step: StepExportResource},
		{Name: ExportSrcManifests, Step: StepExportResource},
		{Name: PostExportHooks, Step: StepExportResource},
		{Name: QuiesceApplications, Step: StepQuiesce},
		{Name: EnsureQuiesced, Step: StepQuiesce},
		{Name: ChangePVReclaimPolicy, Step: StepMigratePV},
		{Name: RegisterFCD, Step: StepMigratePV},
		{Name: StaticallyProvisionDestPV, Step: StepMigratePV},
		{Name: EnsurePVCBond, Step: StepMigratePV},
		{Name: PreImportHooks, Step: StepImportResource},
		{Name: ImportManifestsToDest, Step: StepImportResource},
		{Name: PostImportHooks, Step: StepImportResource},
		{Name: Verification, Step: StepCleanupMigration, all: HasVerify},
		{Name: Completed, Step: StepCleanupMigration},
	},
}

var FailedItinerary = Itinerary{
	Name: "Failed",
	Phases: []Phase{
		{Name: MigrationFailed, Step: StepCleanupHelpers},
		{Name: Completed, Step: StepCleanupFail},
	},
}

var RollbackItinerary = Itinerary{
	Name: "Rollback",
	Phases: []Phase{
		{Name: Rollback, Step: StepPrepareRollback},
		{Name: DeleteMigratedNSResources, Step: StepCleanupMigrated},
		{Name: EnsurePVsUnmounted, Step: StepCleanupMigrated},
		{Name: DeleteMigratedPVs, Step: StepCleanupMigrated},
		{Name: DeleteMigratedClusterResources, Step: StepCleanupMigrated},
		{Name: UnQuiesceSrcApplications, Step: StepUnquiesce},
		{Name: ResetPVReclaimPolicy, Step: StepUnquiesce},
		{Name: Completed, Step: StepCleanupRollback},
	},
}

// A Task that provides the complete migration workflow.
// Log - A controller's logger.
// Client - A controller's (local) client.
// Owner - A MigMigration resource.
// PlanResources - A PlanRefResources.
// Annotations - Map of annotations to applied to the backup & restore
// Phase - The Task phase.
// Requeue - The requeueAfter duration. 0 indicates no requeue.
// Itinerary - The phase itinerary.
// Errors - Migration errors.
// Failed - Task phase has failed.
type Task struct {
	Log           *logrus.Logger
	PluginDir     string
	CacheDir      string
	BackupFile    *os.File
	Backup        *velerov1api.Backup
	Client        k8sclient.Client
	SrcClient     compat.Client
	DestClient    compat.Client
	Owner         *migapi.MigMigration
	PlanResources *migapi.PlanResources
	Annotations   map[string]string
	Phase         string
	Itinerary     Itinerary
	Errors        []string
	Step          string
}

func (t *Task) init() error {
	if t.failed() {
		t.Itinerary = FailedItinerary
		//} else if t.canceled() {
		//	t.Itinerary = CancelItinerary
	} else if t.rollback() {
		t.Itinerary = RollbackItinerary
		//} else if t.stage() {
		//	t.Itinerary = StageItinerary
	} else {
		// TODO Choose itinerary according to PV migrate method: move, copy or stateless
		t.Itinerary = MoveItinerary
	}
	if t.Owner.Status.Itinerary != t.Itinerary.Name {
		t.Phase = t.Itinerary.Phases[0].Name
	}
	t.Step = t.Itinerary.GetStepForPhase(t.Phase)

	return t.initPipeline(t.Owner.Status.Itinerary)
}

// Run the Task.
// Each call will:
//   1. Run the current phase.
//   2. Update the phase to the next phase.
//   3. Set the Requeue (as appropriate).
//   4. Return.
func (t *Task) Run() error {
	if err := t.init(); err != nil {
		return liberr.Wrap(err)
	}
	t.Log.Infof("[START] Phase %s", t.Phase)
	defer t.updatePipeline()

	// Run the current phase.
	switch t.Phase {
	case Started, Rollback:
		return t.next()
	case QuiesceApplications:
		err := t.quiesceApplications()
		if err != nil {
			return liberr.Wrap(err)
		}
		return t.next()
	case EnsureQuiesced:
		for {
			quiesced, err := t.ensureQuiescedPodsTerminated()
			if err != nil {
				return liberr.Wrap(err)
			}
			if quiesced {
				return t.next()
			} else {
				// TODO add timeout here
				t.Log.Info("Quiesce in source cluster is incomplete. " +
					"Pods are not yet terminated, waiting.")
				time.Sleep(PollInterval)
			}
		}
	case ExportSrcManifests:
		if err := t.createManifestFile(); err != nil {
			return liberr.Wrap(err)
		}
		if err := t.runBackup(); err != nil {
			return liberr.Wrap(err)
		}
		return t.next()
	case ChangePVReclaimPolicy:
		err := t.changePVReclaimPolicy()
		if err != nil {
			return liberr.Wrap(err)
		}
		return t.next()
	case RegisterFCD:
		err := t.registerFCD()
		if err != nil {
			return liberr.Wrap(err)
		}
		return t.next()
	case StaticallyProvisionDestPV:
		err := t.createDestNamespaces()
		if err != nil {
			return liberr.Wrap(err)
		}
		err = t.staticallyProvisionDestPV()
		if err != nil {
			return liberr.Wrap(err)
		}
		return t.next()
	case EnsurePVCBond:
		for {
			bound, err := t.ensurePVCBond()
			if err != nil {
				return liberr.Wrap(err)
			}
			if bound {
				return t.next()
			} else {
				// TODO add timeout here
				t.Log.Info("PVC and PV are still binding")
				time.Sleep(PollInterval)
			}
		}
	case ImportManifestsToDest:
		if _, err := t.BackupFile.Seek(0, 0); err != nil {
			return liberr.Wrap(err)
		}
		if err := t.runRestore(); err != nil {
			return liberr.Wrap(err)
		}
		if err := t.cleanManifestFile(); err != nil {
			return liberr.Wrap(err)
		}
		return t.next()
	case DeleteMigratedNSResources:
		if err := t.deleteMigratedNamespaceScopedResources(); err != nil {
			return liberr.Wrap(err)
		}
		return t.next()
	case EnsurePVsUnmounted:
		for {
			unmounted, err := t.ensureMigratedPVsUnmounted()
			if err != nil {
				return liberr.Wrap(err)
			}
			if unmounted {
				return t.next()
			} else {
				// TODO add timeout here
				t.Log.Info("PVs are still mounted")
				time.Sleep(PollInterval)
			}
		}
	case DeleteMigratedPVs:
		if err := t.deleteMigratedPVs(); err != nil {
			return liberr.Wrap(err)
		}
		return t.next()
	case DeleteMigratedClusterResources:
		if err := t.deleteMigratedClusterScopedResources(); err != nil {
			return liberr.Wrap(err)
		}
		return t.next()
	case UnQuiesceSrcApplications:
		err := t.unQuiesceApplications(t.SrcClient, t.sourceNamespaces(), t.PlanResources.SrcMigCluster.Spec.Vendor)
		if err != nil {
			return liberr.Wrap(err)
		}
		return t.next()
	case ResetPVReclaimPolicy:
		if err := t.resetPVReclaimPolicy(); err != nil {
			return liberr.Wrap(err)
		}
		return t.next()
	case Completed:
	default:
		return t.next()
	}

	if t.Phase == Completed {
		t.Log.Info("[COMPLETED]")
	}

	return nil
}

// Fail the task.
func (t *Task) Fail(nextPhase string, reasons []string) {
	t.addErrors(reasons)
	t.Owner.AddErrors(t.Errors)
	t.Owner.Status.SetCondition(migapi.Condition{
		Type:     migapi.Failed,
		Status:   migapi.True,
		Reason:   t.Phase,
		Category: migapi.Advisory,
		Message:  "The migration has failed.  See: Errors.",
		Durable:  true,
	})
	t.failCurrentStep()
	t.Phase = nextPhase
	//t.Step = StepCleanupHelpers
}

// Marks current step failed
func (t *Task) failCurrentStep() {
	currentStep := t.Owner.Status.FindStep(t.Step)
	if currentStep != nil {
		currentStep.Failed = true
	}
}

// Add errors.
func (t *Task) addErrors(errors []string) {
	for _, e := range errors {
		t.Errors = append(t.Errors, e)
	}
}

// Migration UID.
func (t *Task) UID() string {
	return string(t.Owner.UID)
}

func (t *Task) planUID() string {
	return string(t.PlanResources.MigPlan.UID)
}

// Get whether the migration has failed
func (t *Task) failed() bool {
	return t.Owner.HasErrors() || t.Owner.Status.HasCondition(migapi.Failed)
}

// Get whether the migration is rollback.
func (t *Task) rollback() bool {
	return t.Owner.Spec.Rollback
}

func (t *Task) initPipeline(prevItinerary string) error {
	if t.Itinerary.Name != prevItinerary {
		for _, phase := range t.Itinerary.Phases {
			currentStep := t.Owner.Status.FindStep(phase.Step)
			if currentStep != nil {
				continue
			}
			allFlags, err := t.allFlags(phase)
			if err != nil {
				return liberr.Wrap(err)
			}
			if !allFlags {
				continue
			}
			anyFlags, err := t.anyFlags(phase)
			if err != nil {
				return liberr.Wrap(err)
			}
			if !anyFlags {
				continue
			}
			t.Owner.Status.AddStep(&migapi.Step{
				Name:    phase.Step,
				Message: "Not started",
			})
		}
	}
	currentStep := t.Owner.Status.FindStep(t.Step)
	if currentStep != nil {
		currentStep.MarkStarted()
		currentStep.Phase = t.Phase
		if desc, found := PhaseDescriptions[t.Phase]; found {
			currentStep.Message = desc
		} else {
			currentStep.Message = ""
		}
	}
	return nil
}

func (t *Task) updatePipeline() {
	currentStep := t.Owner.Status.FindStep(t.Step)
	for _, step := range t.Owner.Status.Pipeline {
		if currentStep != step && step.MarkedStarted() {
			step.MarkCompleted()
		}
	}
	// mark steps skipped
	for _, step := range t.Owner.Status.Pipeline {
		if step == currentStep {
			break
		} else if !step.MarkedStarted() {
			step.Skipped = true
		}
	}
	if currentStep != nil {
		currentStep.MarkStarted()
		currentStep.Phase = t.Phase
		if desc, found := PhaseDescriptions[t.Phase]; found {
			currentStep.Message = desc
		} else {
			currentStep.Message = ""
		}
		if t.Phase == Completed {
			currentStep.MarkCompleted()
		}
	}
	t.Owner.Status.ReflectPipeline()
}

// Advance the task to the next phase.
func (t *Task) next() error {
	// Write time taken to complete phase
	t.Owner.Status.StageCondition(migapi.Running)
	cond := t.Owner.Status.FindCondition(migapi.Running)
	if cond != nil {
		elapsed := time.Since(cond.LastTransitionTime.Time)
		t.Log.Infof("[END] Phase %s completed phaseElapsed %s", t.Phase, elapsed)
	}

	current := -1
	for i, phase := range t.Itinerary.Phases {
		if phase.Name != t.Phase {
			continue
		}
		current = i
		break
	}
	if current == -1 {
		t.Phase = Completed
		t.Step = StepCleanupMigration
		return nil
	}
	for n := current + 1; n < len(t.Itinerary.Phases); n++ {
		next := t.Itinerary.Phases[n]
		flag, err := t.allFlags(next)
		if err != nil {
			return liberr.Wrap(err)
		}
		if !flag {
			t.Log.Info("Skipped phase due to flag evaluation.",
				"skippedPhase", next.Name)
			continue
		}
		flag, err = t.anyFlags(next)
		if err != nil {
			return liberr.Wrap(err)
		}
		if !flag {
			continue
		}
		t.Phase = next.Name
		t.Step = next.Step
		return nil
	}
	t.Phase = Completed
	t.Step = StepCleanupMigration
	return nil
}

// Evaluate `all` flags.
func (t *Task) allFlags(phase Phase) (bool, error) {
	anyPVs, _ := t.hasPVs()
	if phase.all&HasPVs != 0 && !anyPVs {
		return false, nil
	}

	if phase.all&Quiesce != 0 && !t.quiesce() {
		return false, nil
	}
	if phase.all&HasVerify != 0 && !t.hasVerify() {
		return false, nil
	}
	if phase.all&SourceOpenshift != 0 && !t.isSourceOpenshift() {
		return false, nil
	}

	return true, nil
}

// Evaluate `any` flags.
func (t *Task) anyFlags(phase Phase) (bool, error) {
	anyPVs, _ := t.hasPVs()
	if phase.any&HasPVs != 0 && anyPVs {
		return true, nil
	}
	if phase.any&Quiesce != 0 && t.quiesce() {
		return true, nil
	}
	if phase.any&HasVerify != 0 && t.hasVerify() {
		return true, nil
	}
	return phase.any == uint16(0), nil
}

// Get whether the associated plan lists not skipped PVs.
// First return value is PVs overall, and second is limited to Move PVs
func (t *Task) hasPVs() (bool, bool) {
	var anyPVs bool
	for _, pv := range t.PlanResources.MigPlan.Spec.PersistentVolumes.List {
		if pv.Selection.Action == migapi.PvMoveAction {
			return true, true
		}
		if pv.Selection.Action != migapi.PvSkipAction {
			anyPVs = true
		}
	}
	return anyPVs, false
}

// Get whether to quiesce pods.
func (t *Task) quiesce() bool {
	return t.Owner.Spec.QuiescePods
}

// Get whether the verification is desired
func (t *Task) hasVerify() bool {
	return t.Owner.Spec.Verify
}

// Returns true if the source cluster is a SourceOpenshift cluster
func (t *Task) isSourceOpenshift() bool {
	return t.PlanResources.SrcMigCluster.Spec.Vendor == migapi.OpenShift
}

// Get a client for the source cluster.
func (t *Task) getSourceClient() (compat.Client, error) {
	if t.SrcClient == nil {
		return nil, fmt.Errorf("source client is not initialized")
	}
	return t.SrcClient, nil
}

// Get a client for the destination cluster.
func (t *Task) getDestinationClient() (compat.Client, error) {
	if t.DestClient == nil {
		return nil, fmt.Errorf("destination client is not initialized")
	}
	return t.DestClient, nil
}

// Get the migration source namespaces without mapping.
func (t *Task) sourceNamespaces() []string {
	return t.PlanResources.MigPlan.GetSourceNamespaces()
}

// Get the migration source namespaces without mapping.
func (t *Task) destinationNamespaces() []string {
	return t.PlanResources.MigPlan.GetDestinationNamespaces()
}
