package capabilitylifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/authoritysubstrate"
	"github.com/HJSunDev/ownward/internal/composition"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
	compositionv1 "github.com/HJSunDev/ownward/manifests/compositions/v1"
)

func TestAccessCandidateLifecycleIsRecoverableAndPreservesAcceptedAssets(t *testing.T) {
	current := currentComposition(t)
	root := t.TempDir()
	dataDir := filepath.Join(t.TempDir(), "data")
	initial := initialControl(t, current)
	authority, err := authoritysubstrate.Open(dataDir, initial)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 28, 1, 2, 3, 0, time.UTC)
	asset := domain.Information{Schema: domain.AssetSchema, ID: "accepted", Revision: 1, CreatedAt: now, UpdatedAt: now, Kind: domain.KindKnowledge, Content: "accepted before stateless replacement"}
	if _, err := authority.Assets().CreateAsset(asset); err != nil {
		t.Fatal(err)
	}
	derivedMarker := filepath.Join(dataDir, "state", "unchanged.marker")
	writeFile(t, derivedMarker, "derived-state-must-not-change")
	journalDir := filepath.Join(t.TempDir(), "lifecycle")
	journal, err := OpenFileJournal(journalDir)
	if err != nil {
		t.Fatal(err)
	}
	controlBefore, err := os.ReadFile(filepath.Join(dataDir, "authority", "control.json"))
	if err != nil {
		t.Fatal(err)
	}
	plan := accessPlan(t, current, root, "candidate/access-v2.go", "package candidate\n// access v2\n")
	controlAfterPrepare, err := os.ReadFile(filepath.Join(dataDir, "authority", "control.json"))
	preparedAsset, preparedExists := authority.Assets().ReadCurrent(asset.ID)
	preparedMarker, markerErr := os.ReadFile(derivedMarker)
	if err != nil || markerErr != nil || string(controlAfterPrepare) != string(controlBefore) || !preparedExists ||
		!reflect.DeepEqual(preparedAsset, asset) || string(preparedMarker) != "derived-state-must-not-change" {
		t.Fatalf("candidate preparation changed current state: control=%v asset=%#v marker=%q errors=%v/%v", string(controlAfterPrepare) == string(controlBefore), preparedAsset, preparedMarker, err, markerErr)
	}

	observing, err := ActivateForNextStart(authority.Control(), journal, plan)
	if err != nil {
		t.Fatal(err)
	}
	if observing.Revision != 2 || observing.ActiveComposition != plan.Target.Identity ||
		observing.ActiveKernelGeneration != initial.ActiveKernelGeneration {
		t.Fatalf("stateless activation did not create one recoverable active decision: %#v", observing)
	}
	journalRecord, exists, err := journal.Read()
	if err != nil || !exists || journalRecord.Phase != PhaseObserving || journalRecord.Plan != plan.Identity {
		t.Fatalf("stateless activation did not preserve a recoverable observation record: %#v %v", journalRecord, err)
	}
	repeated, err := ActivateForNextStart(authority.Control(), journal, plan)
	if err != nil || !reflect.DeepEqual(repeated, observing) {
		t.Fatalf("repeated activation was not idempotent: %#v %v", repeated, err)
	}
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}

	// A cold start reconstructs the same plan from sealed artifacts and resumes
	// the pending observation without creating another control state.
	reopened, err := authoritysubstrate.Open(dataDir, initialControl(t, plan.Target))
	if err != nil {
		t.Fatal(err)
	}
	updated := asset
	updated.Revision = 2
	updated.UpdatedAt = now.Add(time.Minute)
	updated.Content = "accepted while candidate observation is open"
	if _, err := reopened.Assets().UpdateAsset(updated, 1); err != nil {
		t.Fatal(err)
	}
	failedObservation := observation(plan, false, "failed observation")
	recoveredJournal, err := OpenFileJournal(journalDir)
	if err != nil {
		t.Fatal(err)
	}
	rolledBack, err := CompleteObservation(reopened.Control(), recoveredJournal, plan, failedObservation)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.Revision != 3 || rolledBack.ActiveComposition != current.Identity {
		t.Fatalf("failed observation did not rollback exactly once: %#v", rolledBack)
	}
	repeatedRollback, err := CompleteObservation(reopened.Control(), recoveredJournal, plan, failedObservation)
	if err != nil || !reflect.DeepEqual(repeatedRollback, rolledBack) {
		t.Fatalf("repeated rollback was not idempotent: %#v %v", repeatedRollback, err)
	}
	actual, exists := reopened.Assets().ReadCurrent(asset.ID)
	if !exists || !reflect.DeepEqual(actual, updated) {
		t.Fatalf("rollback lost an accepted update: %#v", actual)
	}
	marker, err := os.ReadFile(derivedMarker)
	if err != nil || string(marker) != "derived-state-must-not-change" {
		t.Fatalf("stateless lifecycle changed derived state: %q %v", marker, err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	baseline, err := authoritysubstrate.Open(dataDir, initial)
	if err != nil {
		t.Fatal(err)
	}
	defer baseline.Close()
	acceptedPlan := accessPlan(t, current, root, "candidate/access-v3.go", "package candidate\n// access v3\n")
	if _, err := ActivateForNextStart(baseline.Control(), recoveredJournal, acceptedPlan); err != nil {
		t.Fatal(err)
	}
	passedObservation := observation(acceptedPlan, true, "passing observation")
	accepted, err := CompleteObservation(baseline.Control(), recoveredJournal, acceptedPlan, passedObservation)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.ActiveComposition != acceptedPlan.Target.Identity {
		t.Fatalf("passing observation did not finalize candidate: %#v", accepted)
	}
	repeatedAccept, err := CompleteObservation(baseline.Control(), recoveredJournal, acceptedPlan, passedObservation)
	if err != nil || !reflect.DeepEqual(repeatedAccept, accepted) {
		t.Fatalf("repeated observation completion was not idempotent: %#v %v", repeatedAccept, err)
	}
}

func TestStatelessPreparationRejectsPollutionAndStatefulCapabilities(t *testing.T) {
	current := currentComposition(t)
	root := t.TempDir()
	plan := accessPlan(t, current, root, "candidate/access.go", "package candidate\n// access candidate\n")
	dataDir := filepath.Join(t.TempDir(), "data")
	authority, err := authoritysubstrate.Open(dataDir, initialControl(t, current))
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	controlPath := filepath.Join(dataDir, "authority", "control.json")
	before, err := os.ReadFile(controlPath)
	if err != nil {
		t.Fatal(err)
	}

	failed := plan.Validation
	failed.Passed = false
	if _, err := PrepareStateless(current, plan.Replacement, failed); err == nil {
		t.Fatal("failed integration baseline was accepted")
	}
	incompatible := plan.Replacement
	incompatible.Dependencies[0].Identity = strings.Repeat("f", 64)
	incompatible.Identity, _ = composition.ComponentIdentity(incompatible)
	if _, err := PrepareStateless(current, incompatible, plan.Validation); err == nil {
		t.Fatal("incompatible direct dependency was accepted")
	}
	for _, role := range []string{"semantic", "vector"} {
		component := componentByRole(t, current, role)
		component.Content = append([]composition.Content(nil), component.Content...)
		component.Content[0].SHA256 = strings.Repeat("e", 64)
		component.Identity, _ = composition.ComponentIdentity(component)
		if _, err := PrepareStateless(current, component, Validation{}); err == nil || !strings.Contains(err.Error(), "状态失效") {
			t.Fatalf("%s candidate entered stateless lifecycle: %v", role, err)
		}
	}
	after, err := os.ReadFile(controlPath)
	if err != nil || string(after) != string(before) || authority.Control().ReadControl().Revision != 1 {
		t.Fatalf("failed candidate polluted current control: %v", err)
	}

	// Files and audit metadata outside the access component are not direct
	// dependencies and cannot change or block its identity.
	writeFile(t, filepath.Join(root, "unrelated", "notes.txt"), "changed")
	current.Audit["migration_source_git"] = strings.Repeat("9", 40)
	resealed, err := sealReplacement(root, current, "access", candidateContent(plan.Replacement.Content))
	if err != nil || resealed.Identity != plan.Replacement.Identity {
		t.Fatalf("unrelated change altered access identity: %s %s %v", resealed.Identity, plan.Replacement.Identity, err)
	}
	writeFile(t, filepath.Join(root, "candidate", "assembly.go"), "package candidate\n// unrelated assembly candidate\n")
	replacementAssembly, err := sealReplacement(root, current, "assembly", []CandidateContent{{Name: "candidate-assembly", Path: "candidate/assembly.go"}})
	if err != nil {
		t.Fatal(err)
	}
	withUnrelatedReplacement, err := replaceComponent(current, replacementAssembly)
	if err != nil {
		t.Fatal(err)
	}
	resealed, err = sealReplacement(root, withUnrelatedReplacement, "access", candidateContent(plan.Replacement.Content))
	if err != nil || resealed.Identity != plan.Replacement.Identity {
		t.Fatalf("unrelated component replacement altered or blocked access candidate: %s %s %v", resealed.Identity, plan.Replacement.Identity, err)
	}
}

func TestConcurrentStatelessDecisionsProduceOneActiveTruth(t *testing.T) {
	current := currentComposition(t)
	root := t.TempDir()
	left := accessPlan(t, current, root, "candidate/left.go", "package candidate\n// left\n")
	right := accessPlan(t, current, root, "candidate/right.go", "package candidate\n// right\n")
	authority, err := authoritysubstrate.Open(filepath.Join(t.TempDir(), "data"), initialControl(t, current))
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	journal, err := OpenFileJournal(filepath.Join(t.TempDir(), "lifecycle"))
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, plan := range []Plan{left, right} {
		plan := plan
		go func() {
			ready.Done()
			<-start
			_, err := ActivateForNextStart(authority.Control(), journal, plan)
			results <- err
		}()
	}
	ready.Wait()
	close(start)
	successes := 0
	for index := 0; index < 2; index++ {
		if <-results == nil {
			successes++
		}
	}
	state := authority.Control().ReadControl()
	if successes != 1 || (state.ActiveComposition != left.Target.Identity && state.ActiveComposition != right.Target.Identity) {
		t.Fatalf("concurrent decisions did not preserve one active truth: successes=%d state=%#v", successes, state)
	}
}

func TestObservationMustBindTheExactPlanAndReport(t *testing.T) {
	current := currentComposition(t)
	plan := accessPlan(t, current, t.TempDir(), "candidate/access.go", "package candidate\n// access candidate\n")
	authority, err := authoritysubstrate.Open(filepath.Join(t.TempDir(), "data"), initialControl(t, current))
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	journal, err := OpenFileJournal(filepath.Join(t.TempDir(), "lifecycle"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ActivateForNextStart(authority.Control(), journal, plan); err != nil {
		t.Fatal(err)
	}
	invalid := observation(plan, true, "passing observation")
	invalid.Plan = strings.Repeat("f", 64)
	if _, err := CompleteObservation(authority.Control(), journal, plan, invalid); err == nil {
		t.Fatal("observation for another plan finalized authority control")
	}
	state := authority.Control().ReadControl()
	if state.ActiveComposition != plan.Target.Identity || state.Revision != 2 {
		t.Fatalf("invalid observation changed authority control: %#v", state)
	}
}

func TestStatelessLifecycleReconcilesInterruptedControlBoundaries(t *testing.T) {
	current := currentComposition(t)
	plan := accessPlan(t, current, t.TempDir(), "candidate/access.go", "package candidate\n// access candidate\n")
	dataDir := filepath.Join(t.TempDir(), "data")
	authority, err := authoritysubstrate.Open(dataDir, initialControl(t, current))
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	journalDir := filepath.Join(t.TempDir(), "lifecycle")
	journal, err := OpenFileJournal(journalDir)
	if err != nil {
		t.Fatal(err)
	}
	prepared := recordForPlan(plan, PhasePrepared, "")
	prepared.Revision = 1
	if _, err := journal.Append(0, prepared); err != nil {
		t.Fatal(err)
	}
	before := authority.Control().ReadControl()
	if _, err := authority.Control().CompareAndSwapControl(before.Revision, contract.ControlState{
		Schema: contract.ControlStateSchema, Revision: before.Revision + 1,
		ActiveComposition: plan.Target.Identity, ActiveKernelGeneration: before.ActiveKernelGeneration,
	}); err != nil {
		t.Fatal(err)
	}

	// This is the state after a crash between the authority CAS and publishing
	// the observing checkpoint. A new journal handle must reconcile it without
	// another authority mutation.
	reopenedJournal, err := OpenFileJournal(journalDir)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := ActivateForNextStart(authority.Control(), reopenedJournal, plan)
	if err != nil || recovered.Revision != 2 || recovered.ActiveComposition != plan.Target.Identity {
		t.Fatalf("activation boundary was not recovered: %#v %v", recovered, err)
	}
	record, exists, err := reopenedJournal.Read()
	if err != nil || !exists || record.Phase != PhaseObserving || record.Revision != 2 {
		t.Fatalf("observing checkpoint was not recovered: %#v %v", record, err)
	}

	// Simulate a crash after the rollback CAS but before its terminal journal
	// record. Repeating the same evidence must finish only the missing append.
	observing := authority.Control().ReadControl()
	if _, err := authority.Control().CompareAndSwapControl(observing.Revision, contract.ControlState{
		Schema: contract.ControlStateSchema, Revision: observing.Revision + 1,
		ActiveComposition: plan.Baseline.Identity, ActiveKernelGeneration: observing.ActiveKernelGeneration,
	}); err != nil {
		t.Fatal(err)
	}
	failed := observation(plan, false, "failed observation")
	rolledBack, err := CompleteObservation(authority.Control(), reopenedJournal, plan, failed)
	if err != nil || rolledBack.ActiveComposition != plan.Baseline.Identity || rolledBack.Revision != 3 {
		t.Fatalf("rollback boundary was not recovered: %#v %v", rolledBack, err)
	}
	record, exists, err = reopenedJournal.Read()
	if err != nil || !exists || record.Phase != PhaseRolledBack || record.ObservationSHA256 != failed.ReportSHA256 {
		t.Fatalf("rollback checkpoint was not finalized: %#v %v", record, err)
	}
}

func TestLifecycleJournalRejectsTamperedHistory(t *testing.T) {
	current := currentComposition(t)
	plan := accessPlan(t, current, t.TempDir(), "candidate/access.go", "package candidate\n// access candidate\n")
	dir := filepath.Join(t.TempDir(), "lifecycle")
	journal, err := OpenFileJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	prepared := recordForPlan(plan, PhasePrepared, "")
	prepared.Revision = 1
	if _, err := journal.Append(0, prepared); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, recordName(1))
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	encoded[len(encoded)/2] ^= 1
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := journal.Read(); err == nil {
		t.Fatal("tampered lifecycle history was accepted")
	}
}

func accessPlan(t *testing.T, current composition.Manifest, root, relative, content string) Plan {
	t.Helper()
	writeFile(t, filepath.Join(root, filepath.FromSlash(relative)), content)
	replacement, err := sealReplacement(root, current, "access", []CandidateContent{{Name: "candidate-access", Path: filepath.ToSlash(relative)}})
	if err != nil {
		t.Fatal(err)
	}
	target, err := replaceComponent(current, replacement)
	if err != nil {
		t.Fatal(err)
	}
	validation := Validation{
		Schema: ValidationSchema, CandidateComponent: replacement.Identity,
		BaselineComposition: current.Identity, TargetComposition: target.Identity,
		StateImpact: ImpactNone, IntegrationSHA256: digest("integration:" + replacement.Identity), Passed: true,
	}
	plan, err := PrepareStateless(current, replacement, validation)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func currentComposition(t *testing.T) composition.Manifest {
	t.Helper()
	manifest, err := composition.Parse(compositionv1.CurrentCollaborative())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := composition.VerifySealed(manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func initialControl(t *testing.T, manifest composition.Manifest) contract.ControlState {
	t.Helper()
	return contract.ControlState{
		Schema: contract.ControlStateSchema, Revision: 1,
		ActiveComposition: manifest.Identity, ActiveKernelGeneration: componentByRole(t, manifest, "kernel").Identity,
	}
}

func componentByRole(t *testing.T, manifest composition.Manifest, role string) composition.Component {
	t.Helper()
	for _, component := range manifest.Components {
		if component.Role == role {
			return component
		}
	}
	t.Fatalf("missing role %s", role)
	return composition.Component{}
}

func writeFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func candidateContent(content []composition.Content) []CandidateContent {
	result := make([]CandidateContent, len(content))
	for index, item := range content {
		result[index] = CandidateContent{Name: item.Name, Path: item.Path}
	}
	return result
}

func observation(plan Plan, passed bool, report string) Observation {
	return Observation{
		Schema: ObservationSchema, Plan: plan.Identity, ActiveComposition: plan.Target.Identity,
		ReportSHA256: digest(report), Passed: passed,
	}
}
