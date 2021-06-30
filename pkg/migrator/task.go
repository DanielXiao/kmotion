package migrator

import (
	migapi "github.com/danielxiao/mig-controller/pkg/apis/migration/v1alpha1"
	"github.com/danielxiao/mig-controller/pkg/compat"
	liberr "github.com/konveyor/controller/pkg/error"
	"github.com/sirupsen/logrus"
	velerov1api "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
	"os"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
	"time"
)

var PollInterval = time.Duration(time.Millisecond * 500)
var NoWait = time.Duration(0)

// Phases
const (
	Started                   = "Started"
	PreBackupHooks            = "PreBackupHooks"
	BackupSrcManifests        = "BackupSrcManifests"
	PostBackupHooks           = "PostBackupHooks"
	ChangePVReclaimPolicy     = "ChangePVReclaimPolicy"
	QuiesceApplications       = "QuiesceApplications"
	EnsureQuiesced            = "EnsureQuiesced"
	RegisterFCD               = "RegisterFCD"
	StaticallyProvisionDestPV = "StaticallyProvisionDestPV"
	PreRestoreHooks           = "PreRestoreHooks"
	RestoreDestManifests      = "RestoreDestManifests"
	PostRestoreHooks          = "PostRestoreHooks"
	Verification              = "Verification"
	MigrationFailed           = "MigrationFailed"
	Canceling                 = "Canceling"
	Canceled                  = "Canceled"
	Rollback                  = "Rollback"
	Completed                 = "Completed"
)

var PhaseDescriptions = map[string]string{
	Started:                   "Migration started.",
	PreBackupHooks:            "Run hooks before backup",
	BackupSrcManifests:        "Backup resources from source cluster",
	PostBackupHooks:           "Run hooks after backup",
	ChangePVReclaimPolicy:     "Change target PV reclaim policy to retain",
	QuiesceApplications:       "Quiesce target applications in source cluster",
	EnsureQuiesced:            "Ensure applications quiesced",
	RegisterFCD:               "Register target PVs as FCD",
	StaticallyProvisionDestPV: "Statically Provision PVs in destination cluster",
	PreRestoreHooks:           "Run hooks before restore",
	RestoreDestManifests:      "Restore resources to destination cluster",
	PostRestoreHooks:          "Run hooks after restore",
	Verification:              "Verify applications up and running",
	MigrationFailed:           "Migration failed",
	Canceling:                 "Canceling migration",
	Canceled:                  "Canceled migration",
	Rollback:                  "Rollback migration",
	Completed:                 "Migration is completed",
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
	StepPrepare          = "Prepare"
	StepBackup           = "Backup"
	StepQuiesce          = "Quiesce"
	StepMigratePV        = "Migrate PV"
	StepStageBackup      = "StageBackup"
	StepStageRestore     = "StageRestore"
	StepRestore          = "Restore"
	StepCleanup          = "Cleanup"
	StepCleanupHelpers   = "CleanupHelpers"
	StepCleanupMigrated  = "CleanupMigrated"
	StepCleanupUnquiesce = "CleanupUnquiesce"
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
		{Name: Started, Step: StepPrepare},
		{Name: PreBackupHooks, Step: StepBackup},
		{Name: BackupSrcManifests, Step: StepBackup},
		{Name: PostBackupHooks, Step: StepBackup},
		{Name: QuiesceApplications, Step: StepQuiesce},
		{Name: EnsureQuiesced, Step: StepQuiesce},
		{Name: ChangePVReclaimPolicy, Step: StepMigratePV},
		{Name: RegisterFCD, Step: StepMigratePV},
		{Name: StaticallyProvisionDestPV, Step: StepMigratePV},
		{Name: PreRestoreHooks, Step: StepRestore},
		{Name: RestoreDestManifests, Step: StepRestore},
		{Name: PostRestoreHooks, Step: StepRestore},
		{Name: Verification, Step: StepCleanup, all: HasVerify},
		{Name: Completed, Step: StepCleanup},
	},
}

var FailedItinerary = Itinerary{
	Name: "Failed",
	Phases: []Phase{
		{Name: MigrationFailed, Step: StepCleanupHelpers},
		{Name: Completed, Step: StepCleanup},
	},
}

// A Task that provides the complete migration workflow.
// Logger - A controller's logger.
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
	Logger        *logrus.Logger
	PluginDir     string
	CacheDir      string
	BackupFile    *os.File
	Backup        *velerov1api.Backup
	Client        k8sclient.Client
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
		//} else if t.rollback() {
		//	t.Itinerary = RollbackItinerary
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
		return err
	}
	t.Logger.Infof("[RUN] phase %s", t.Phase)
	defer t.updatePipeline()

	id := string(t.Owner.UID)
	// Run the current phase.
	switch t.Phase {
	case Started:
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
				t.Logger.Info("Quiesce in source cluster is incomplete. " +
					"Pods are not yet terminated, waiting.")
				time.Sleep(PollInterval)
			}
		}
	case BackupSrcManifests:
		restConfig, err := t.PlanResources.SrcMigCluster.BuildRestConfig(t.Client)
		if err != nil {
			return liberr.Wrap(err)
		}
		if t.BackupFile, err = createManifestFile(t.Logger, t.CacheDir, id); err != nil {
			return liberr.Wrap(err)
		}
		if t.Backup, err = runBackup(t.Logger, id, restConfig, t.PluginDir, t.BackupFile, t.PlanResources.MigPlan.Spec.Namespaces); err != nil {
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
		err := t.staticallyProvisionDestPV()
		if err != nil {
			return liberr.Wrap(err)
		}
		return t.next()
	case RestoreDestManifests:
		restConfig, err := t.PlanResources.DestMigCluster.BuildRestConfig(t.Client)
		if err != nil {
			return liberr.Wrap(err)
		}
		if _, err = t.BackupFile.Seek(0, 0); err != nil {
			return liberr.Wrap(err)
		}
		if _, err = runRestore(t.Logger, id, restConfig, t.PluginDir, t.BackupFile, t.Backup); err != nil {
			return liberr.Wrap(err)
		}
		if err = cleanManifestFile(t.Logger, t.BackupFile); err != nil {
			return liberr.Wrap(err)
		}
		return t.next()

	case Completed:
	default:
		if err := t.next(); err != nil {
			return liberr.Wrap(err)
		}
	}

	if t.Phase == Completed {
		t.Logger.Info("[COMPLETED]")
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
	t.Step = StepCleanup
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

// Get whether the migration has failed
func (t *Task) failed() bool {
	return t.Owner.HasErrors() || t.Owner.Status.HasCondition(migapi.Failed)
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
		t.Logger.Infof("Phase %s completed phaseElapsed %s", t.Phase, elapsed)
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
		t.Step = StepCleanup
		return nil
	}
	for n := current + 1; n < len(t.Itinerary.Phases); n++ {
		next := t.Itinerary.Phases[n]
		flag, err := t.allFlags(next)
		if err != nil {
			return liberr.Wrap(err)
		}
		if !flag {
			t.Logger.Info("Skipped phase due to flag evaluation.",
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
	t.Step = StepCleanup
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
	return t.PlanResources.SrcMigCluster.GetClient(t.Client)
}

// Get a client for the destination cluster.
func (t *Task) getDestinationClient() (compat.Client, error) {
	return t.PlanResources.DestMigCluster.GetClient(t.Client)
}

// Get the migration source namespaces without mapping.
func (t *Task) sourceNamespaces() []string {
	return t.PlanResources.MigPlan.GetSourceNamespaces()
}

// Get the migration source namespaces without mapping.
func (t *Task) destinationNamespaces() []string {
	return t.PlanResources.MigPlan.GetDestinationNamespaces()
}
