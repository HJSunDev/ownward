//go:build ownward_migration

package capabilitylifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/composition"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
)

func TestDerivedLifecycleBuildCatchUpPromoteObserveAndRetire(t *testing.T) {
	for _, role := range []string{"semantic", "vector", "kernel"} {
		t.Run(role, func(t *testing.T) {
			plan := derivedPlanForRole(t, role)
			authority := newMemoryAuthority(asset("alpha", 1), asset("beta", 1))
			root := filepath.Join(t.TempDir(), "state")
			seedActiveGeneration(t, authority, root, plan.BaselineRun.VectorSpace)
			control := &memoryControl{state: controlFor(plan.Baseline)}
			journal, err := OpenDerivedJournal(filepath.Join(t.TempDir(), "journal"))
			if err != nil {
				t.Fatal(err)
			}
			builder := &recordBuilder{runtime: plan.TargetRun}
			record, err := PrepareDerivedGeneration(context.Background(), authority, root, plan, builder, journal, "gen-candidate-"+role)
			if err != nil || record.Phase != DerivedPhaseReady || builder.builds != 1 {
				t.Fatalf("candidate baseline failed: %#v builds=%d err=%v", record, builder.builds, err)
			}
			authority.set(asset("alpha", 2))
			record, err = CatchUpDerivedGeneration(context.Background(), authority, root, plan, builder, journal)
			if err != nil || builder.caught != 1 || record.CandidateSnapshot.Assets[0].Revision != 2 {
				t.Fatalf("first incremental catch-up failed: %#v caught=%d err=%v", record, builder.caught, err)
			}
			authority.set(asset("beta", 2))
			record, err = CatchUpDerivedGeneration(context.Background(), authority, root, plan, builder, journal)
			if err != nil || builder.caught != 2 {
				t.Fatalf("second incremental catch-up failed: caught=%d err=%v", builder.caught, err)
			}
			record, err = PromoteDerived(authority, control, root, plan, journal)
			if err != nil || record.Phase != DerivedPhaseObserving {
				t.Fatalf("promotion failed: %#v err=%v", record, err)
			}
			if _, err := derived.OpenGeneration(root, record.Baseline.Generation); err != nil {
				t.Fatalf("rollback generation was not retained: %v", err)
			}
			authority.set(asset("alpha", 3))
			if _, err := CatchUpDerivedGeneration(context.Background(), authority, root, plan, builder, journal); err != nil {
				t.Fatal(err)
			}
			observation := DerivedObservation{Schema: DerivedObservationSchema, Plan: plan.Identity, ActiveComposition: plan.Target.Identity, ReportSHA256: digest("pass:" + role), Passed: true}
			record, err = CompleteDerivedObservation(context.Background(), authority, control, root, plan, nil, builder, journal, observation)
			if err != nil || record.Phase != DerivedPhaseAccepted {
				t.Fatalf("observation acceptance failed: %#v err=%v", record, err)
			}
			if _, err := derived.OpenGeneration(root, record.Baseline.Generation); err == nil {
				t.Fatal("obsolete rollback generation was not retired after acceptance")
			}
		})
	}
}

func TestDerivedPlanIsDurableAndRejectsRuntimeOrDependencyDrift(t *testing.T) {
	plan := derivedPlanForRole(t, "kernel")
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := WriteDerivedPlan(path, plan); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadDerivedPlan(path)
	if err != nil || loaded.Identity != plan.Identity {
		t.Fatalf("sealed derived plan did not round-trip: %#v %v", loaded, err)
	}
	wrongRuntime := plan
	wrongRuntime.TargetRun.VectorSpace = "wrong-space"
	if err := WriteDerivedPlan(filepath.Join(t.TempDir(), "wrong.json"), wrongRuntime); err == nil {
		t.Fatal("runtime identity drift was accepted")
	}
	wrongDependency := plan.Replacement
	wrongDependency.Dependencies = append([]composition.Dependency(nil), wrongDependency.Dependencies...)
	wrongDependency.Dependencies[0].Identity = digest("wrong-direct-dependency")
	wrongDependency.Identity, _ = composition.ComponentIdentity(wrongDependency)
	validation := plan.Validation
	validation.CandidateComponent = wrongDependency.Identity
	if _, err := PrepareDerived(plan.Baseline, wrongDependency, validation); err == nil {
		t.Fatal("direct dependency mismatch was accepted")
	}
	encoded, _ := os.ReadFile(path)
	encoded[len(encoded)/2] ^= 1
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDerivedPlan(path); err == nil {
		t.Fatal("tampered derived plan was accepted")
	}
}

func TestDerivedFailedBuildAndWrongCapabilityIdentityDoNotPolluteActiveState(t *testing.T) {
	plan := derivedPlanForRole(t, "semantic")
	authority := newMemoryAuthority(asset("alpha", 1))
	root := filepath.Join(t.TempDir(), "state")
	seedActiveGeneration(t, authority, root, plan.BaselineRun.VectorSpace)
	activeBefore, _ := derived.ActiveGeneration(root)
	journal, _ := OpenDerivedJournal(filepath.Join(t.TempDir(), "journal"))
	failed := &recordBuilder{runtime: plan.TargetRun, fail: true}
	if _, err := PrepareDerivedGeneration(context.Background(), authority, root, plan, failed, journal, "gen-failed"); err == nil {
		t.Fatal("failed candidate build was accepted")
	}
	activeAfter, _ := derived.ActiveGeneration(root)
	if activeAfter != activeBefore {
		t.Fatal("failed candidate changed the active generation")
	}
	if _, exists, err := journal.Read(); err != nil || exists {
		t.Fatalf("failed candidate published a lifecycle checkpoint: exists=%v err=%v", exists, err)
	}
	wrong := &recordBuilder{runtime: plan.TargetRun}
	wrong.runtime.Semantic = digest("wrong-semantic-capability")
	if _, err := PrepareDerivedGeneration(context.Background(), authority, root, plan, wrong, journal, "gen-wrong-runtime"); err == nil {
		t.Fatal("wrong semantic capability identity was accepted")
	}
	activeAfter, _ = derived.ActiveGeneration(root)
	if activeAfter != activeBefore {
		t.Fatal("wrong capability identity changed active state")
	}
}

func TestDerivedPreparationRecoversSealedBuildWithoutRepeatingCandidateWork(t *testing.T) {
	plan := derivedPlanForRole(t, "kernel")
	authority := newMemoryAuthority(asset("alpha", 1), asset("beta", 1))
	root := filepath.Join(t.TempDir(), "state")
	seedActiveGeneration(t, authority, root, plan.BaselineRun.VectorSpace)
	snapshot, values, err := CaptureAuthoritySnapshot(authority)
	if err != nil {
		t.Fatal(err)
	}
	generation := "gen-interrupted-baseline"
	preparation := derivedPreparation{Schema: derivedPreparationSchema, Plan: plan.Identity, Generation: generation, Runtime: plan.TargetRun, Snapshot: snapshot}
	if err := sealDerivedPreparation(root, preparation); err != nil {
		t.Fatal(err)
	}
	builder := &recordBuilder{runtime: plan.TargetRun}
	store, err := builder.Build(context.Background(), root, generation, values)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SealGeneration(derived.GenerationMetadata{AssetCount: len(values), AssetSnapshot: snapshot.Identity, EmbeddingSpace: plan.TargetRun.VectorSpace}); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	journal, _ := OpenDerivedJournal(filepath.Join(t.TempDir(), "journal"))
	record, err := PrepareDerivedGeneration(context.Background(), authority, root, plan, builder, journal, generation)
	if err != nil || record.Phase != DerivedPhaseReady || builder.builds != 1 {
		t.Fatalf("sealed candidate work was not recovered exactly: %#v builds=%d err=%v", record, builder.builds, err)
	}
	checkpoint, _ := derived.CandidateCheckpointPath(root, generation)
	if _, err := os.Stat(checkpoint); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("temporary candidate preparation marker remained after durable journal commit")
	}
}

func TestDerivedCatchUpRecoversAfterLogAndManifestBoundariesWithoutRepeatingWork(t *testing.T) {
	for _, boundary := range []string{"after-log", "after-manifest"} {
		t.Run(boundary, func(t *testing.T) {
			plan := derivedPlanForRole(t, "kernel")
			authority := newMemoryAuthority(asset("alpha", 1))
			root := filepath.Join(t.TempDir(), "state")
			seedActiveGeneration(t, authority, root, plan.BaselineRun.VectorSpace)
			journal, _ := OpenDerivedJournal(filepath.Join(t.TempDir(), "journal"))
			builder := &recordBuilder{runtime: plan.TargetRun}
			record, err := PrepareDerivedGeneration(context.Background(), authority, root, plan, builder, journal, "gen-catch-up-crash-"+boundary)
			if err != nil {
				t.Fatal(err)
			}
			authority.set(asset("alpha", 2))
			snapshot, values, err := CaptureAuthoritySnapshot(authority)
			if err != nil {
				t.Fatal(err)
			}
			changes, err := changedVersions(record.CandidateSnapshot, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			marker := derivedCatchUp{Schema: derivedCatchUpSchema, Plan: plan.Identity, Generation: record.Candidate.Generation, FromRevision: record.Revision, Snapshot: snapshot}
			if _, err := sealDerivedCatchUp(root, marker); err != nil {
				t.Fatal(err)
			}
			store, err := derived.OpenGeneration(root, record.Candidate.Generation)
			if err != nil {
				t.Fatal(err)
			}
			if err := builder.CatchUp(context.Background(), store, values, changes); err != nil {
				t.Fatal(err)
			}
			if boundary == "after-manifest" {
				if _, err := store.ResealGeneration(derived.GenerationMetadata{AssetCount: 1, AssetSnapshot: snapshot.Identity, EmbeddingSpace: plan.TargetRun.VectorSpace}); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			caughtBefore := builder.caught
			recovered, err := CatchUpDerivedGeneration(context.Background(), authority, root, plan, builder, journal)
			if err != nil || builder.caught != caughtBefore || recovered.CandidateSnapshot.Identity != snapshot.Identity ||
				recovered.Candidate.SealedBytes != recovered.Candidate.LogBytes {
				t.Fatalf("%s recovery repeated work or lost durable identity: %#v caught=%d/%d err=%v", boundary, recovered, builder.caught, caughtBefore, err)
			}
		})
	}
}

func TestObservedGenerationRecoversAfterManifestAndPointerBoundaries(t *testing.T) {
	for _, boundary := range []string{"after-manifest", "after-pointer"} {
		t.Run(boundary, func(t *testing.T) {
			plan := derivedPlanForRole(t, "kernel")
			authority := newMemoryAuthority(asset("alpha", 1))
			root := filepath.Join(t.TempDir(), "state")
			seedActiveGeneration(t, authority, root, plan.BaselineRun.VectorSpace)
			control := &memoryControl{state: controlFor(plan.Baseline)}
			journal, _ := OpenDerivedJournal(filepath.Join(t.TempDir(), "journal"))
			builder := &recordBuilder{runtime: plan.TargetRun}
			record, err := PrepareDerivedGeneration(context.Background(), authority, root, plan, builder, journal, "gen-observe-crash-"+boundary)
			if err != nil {
				t.Fatal(err)
			}
			record, err = PromoteDerived(authority, control, root, plan, journal)
			if err != nil {
				t.Fatal(err)
			}
			authority.set(asset("alpha", 2))
			snapshot, values, err := CaptureAuthoritySnapshot(authority)
			if err != nil {
				t.Fatal(err)
			}
			store, err := derived.OpenGeneration(root, record.Candidate.Generation)
			if err != nil {
				t.Fatal(err)
			}
			if err := builder.CatchUp(context.Background(), store, values, scopeFor(values[0])); err != nil {
				t.Fatal(err)
			}
			state, err := store.ResealGeneration(derived.GenerationMetadata{AssetCount: 1, AssetSnapshot: snapshot.Identity, EmbeddingSpace: plan.TargetRun.VectorSpace})
			if err != nil {
				t.Fatal(err)
			}
			_ = store.Close()
			if boundary == "after-pointer" {
				if rebound, err := derived.RebindActiveGeneration(root, record.Candidate, state); err != nil || !rebound {
					t.Fatalf("seed pointer boundary: rebound=%v err=%v", rebound, err)
				}
			}
			recovered, err := SealObservedGenerationAtSnapshot(root, plan, journal, snapshot)
			if err != nil || recovered.Candidate != state || recovered.CandidateSnapshot.Identity != snapshot.Identity {
				t.Fatalf("%s observation recovery failed: %#v err=%v", boundary, recovered, err)
			}
		})
	}
}

func TestDerivedLifecycleCatchesRollbackGenerationUpBeforeSwitch(t *testing.T) {
	plan := derivedPlanForRole(t, "kernel")
	authority := newMemoryAuthority(asset("alpha", 1), asset("beta", 1))
	root := filepath.Join(t.TempDir(), "state")
	seedActiveGeneration(t, authority, root, plan.BaselineRun.VectorSpace)
	control := &memoryControl{state: controlFor(plan.Baseline)}
	journal, _ := OpenDerivedJournal(filepath.Join(t.TempDir(), "journal"))
	target := &recordBuilder{runtime: plan.TargetRun}
	baseline := &recordBuilder{runtime: plan.BaselineRun}
	record, err := PrepareDerivedGeneration(context.Background(), authority, root, plan, target, journal, "gen-rollback-target")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PromoteDerived(authority, control, root, plan, journal); err != nil {
		t.Fatal(err)
	}
	authority.set(asset("alpha", 2))
	if _, err := CatchUpDerivedGeneration(context.Background(), authority, root, plan, target, journal); err != nil {
		t.Fatal(err)
	}
	failed := DerivedObservation{Schema: DerivedObservationSchema, Plan: plan.Identity, ActiveComposition: plan.Target.Identity, ReportSHA256: digest("failed-observation"), Passed: false}
	record, err = CompleteDerivedObservation(context.Background(), authority, control, root, plan, baseline, target, journal, failed)
	if err != nil || record.Phase != DerivedPhaseRolledBack || baseline.caught != 1 {
		t.Fatalf("rollback did not catch the previous implementation up: %#v caught=%d err=%v", record, baseline.caught, err)
	}
	if err := verifyActive(control.ReadControl(), root, plan.Baseline.Identity, plan.BaselineRun.Kernel, record.Baseline.Generation); err != nil {
		t.Fatal(err)
	}
	if err := validateGenerationCoverage(root, record.Baseline.Generation, record.BaselineSnapshot, plan.BaselineRun.VectorSpace); err != nil {
		t.Fatal(err)
	}
	if _, err := derived.OpenGeneration(root, record.Candidate.Generation); err == nil {
		t.Fatal("failed candidate generation was retained after rollback closed")
	}
}

func TestRollbackCatchUpCanAdvanceAfterFinalBarrierConflict(t *testing.T) {
	plan := derivedPlanForRole(t, "kernel")
	authority := newMemoryAuthority(asset("alpha", 1))
	root := filepath.Join(t.TempDir(), "state")
	seedActiveGeneration(t, authority, root, plan.BaselineRun.VectorSpace)
	control := &memoryControl{state: controlFor(plan.Baseline)}
	journal, _ := OpenDerivedJournal(filepath.Join(t.TempDir(), "journal"))
	target := &recordBuilder{runtime: plan.TargetRun}
	baseline := &recordBuilder{runtime: plan.BaselineRun}
	if _, err := PrepareDerivedGeneration(context.Background(), authority, root, plan, target, journal, "gen-rollback-conflict"); err != nil {
		t.Fatal(err)
	}
	if _, err := PromoteDerived(authority, control, root, plan, journal); err != nil {
		t.Fatal(err)
	}
	failed := DerivedObservation{Schema: DerivedObservationSchema, Plan: plan.Identity, ActiveComposition: plan.Target.Identity, ReportSHA256: digest("rollback-conflict"), Passed: false}
	authority.set(asset("alpha", 2))
	first, values, _ := CaptureAuthoritySnapshot(authority)
	if _, err := CatchUpRollbackGenerationAtSnapshot(context.Background(), root, plan, baseline, journal, first, values, failed); err != nil {
		t.Fatal(err)
	}
	authority.set(asset("alpha", 3))
	latest, values, _ := CaptureAuthoritySnapshot(authority)
	if _, err := CompleteDerivedObservationAtSnapshot(latest, control, root, plan, journal, failed); err == nil {
		t.Fatal("rollback crossed a final authority snapshot gap")
	}
	record, err := CatchUpRollbackGenerationAtSnapshot(context.Background(), root, plan, baseline, journal, latest, values, failed)
	if err != nil || record.BaselineSnapshot.Identity != latest.Identity || baseline.caught != 2 {
		t.Fatalf("rollback did not incrementally catch up after conflict: %#v caught=%d err=%v", record, baseline.caught, err)
	}
	record, err = CompleteDerivedObservationAtSnapshot(latest, control, root, plan, journal, failed)
	if err != nil || record.Phase != DerivedPhaseRolledBack {
		t.Fatalf("rollback did not close at the latest write boundary: %#v err=%v", record, err)
	}
}

func TestDerivedLifecycleRejectsDriftTamperAndConcurrentDecision(t *testing.T) {
	plan := derivedPlanForRole(t, "vector")
	authority := newMemoryAuthority(asset("alpha", 1))
	root := filepath.Join(t.TempDir(), "state")
	seedActiveGeneration(t, authority, root, plan.BaselineRun.VectorSpace)
	control := &memoryControl{state: controlFor(plan.Baseline)}
	journalDir := filepath.Join(t.TempDir(), "journal")
	journal, _ := OpenDerivedJournal(journalDir)
	builder := &recordBuilder{runtime: plan.TargetRun}
	record, err := PrepareDerivedGeneration(context.Background(), authority, root, plan, builder, journal, "gen-tamper")
	if err != nil {
		t.Fatal(err)
	}
	authority.set(asset("alpha", 2))
	if _, err := PromoteDerived(authority, control, root, plan, journal); err == nil {
		t.Fatal("snapshot drift was promoted without catch-up")
	}
	if _, err := CatchUpDerivedGeneration(context.Background(), authority, root, plan, builder, journal); err != nil {
		t.Fatal(err)
	}
	current, _, err := journal.Read()
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "generations", record.Candidate.Generation, derived.LogFileName)
	file, err := os.OpenFile(logPath, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(current.Candidate.LogBytes-8, 0); err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write([]byte{0xff})
	_ = file.Close()
	if _, err := PromoteDerived(authority, control, root, plan, journal); err == nil {
		t.Fatal("tampered generation was promoted")
	}
	checkpoint := filepath.Join(journalDir, derivedRecordName(1))
	encoded, err := os.ReadFile(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	encoded[len(encoded)/2] ^= 1
	if err := os.WriteFile(checkpoint, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := journal.Read(); err == nil {
		t.Fatal("tampered append-only lifecycle history was accepted")
	}
}

func TestDerivedLifecycleRecoversBothSidesOfPromotionCAS(t *testing.T) {
	plan := derivedPlanForRole(t, "semantic")
	authority := newMemoryAuthority(asset("alpha", 1))
	root := filepath.Join(t.TempDir(), "state")
	seedActiveGeneration(t, authority, root, plan.BaselineRun.VectorSpace)
	control := &memoryControl{state: controlFor(plan.Baseline)}
	journal, _ := OpenDerivedJournal(filepath.Join(t.TempDir(), "journal"))
	builder := &recordBuilder{runtime: plan.TargetRun}
	record, err := PrepareDerivedGeneration(context.Background(), authority, root, plan, builder, journal, "gen-crash")
	if err != nil {
		t.Fatal(err)
	}
	record.Revision++
	record.Phase = DerivedPhaseSwitching
	if _, err := journal.Append(record.Revision-1, record); err != nil {
		t.Fatal(err)
	}
	if _, err := derived.SwitchGeneration(root, record.Baseline.Generation, record.Candidate.Generation); err != nil {
		t.Fatal(err)
	}
	// This is the durable state after a crash between the pointer and control CAS.
	recovered, err := PromoteDerived(authority, control, root, plan, journal)
	if err != nil || recovered.Phase != DerivedPhaseObserving || control.ReadControl().ActiveComposition != plan.Target.Identity {
		t.Fatalf("two-CAS promotion did not recover: %#v err=%v", recovered, err)
	}
	repeated, err := PromoteDerived(authority, control, root, plan, journal)
	if err != nil || repeated.Revision != recovered.Revision {
		t.Fatalf("repeated promotion was not idempotent: %#v err=%v", repeated, err)
	}
}

func TestDerivedLifecycleRecoversBothSidesOfRollbackCASAndKeepsDecisionImmutable(t *testing.T) {
	plan := derivedPlanForRole(t, "kernel")
	authority := newMemoryAuthority(asset("alpha", 1))
	root := filepath.Join(t.TempDir(), "state")
	seedActiveGeneration(t, authority, root, plan.BaselineRun.VectorSpace)
	control := &memoryControl{state: controlFor(plan.Baseline)}
	journal, _ := OpenDerivedJournal(filepath.Join(t.TempDir(), "journal"))
	target := &recordBuilder{runtime: plan.TargetRun}
	baseline := &recordBuilder{runtime: plan.BaselineRun}
	record, err := PrepareDerivedGeneration(context.Background(), authority, root, plan, target, journal, "gen-rollback-crash")
	if err != nil {
		t.Fatal(err)
	}
	record, err = PromoteDerived(authority, control, root, plan, journal)
	if err != nil {
		t.Fatal(err)
	}
	record.Revision++
	record.Phase = DerivedPhaseRollbackReady
	record.ObservationSHA256 = digest("rollback-crash-observation")
	record, err = journal.Append(record.Revision-1, record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := derived.SwitchGeneration(root, record.Candidate.Generation, record.Baseline.Generation); err != nil {
		t.Fatal(err)
	}
	failed := DerivedObservation{Schema: DerivedObservationSchema, Plan: plan.Identity, ActiveComposition: plan.Target.Identity, ReportSHA256: record.ObservationSHA256, Passed: false}
	recovered, err := CompleteDerivedObservation(context.Background(), authority, control, root, plan, baseline, target, journal, failed)
	if err != nil || recovered.Phase != DerivedPhaseRolledBack {
		t.Fatalf("two-CAS rollback did not recover: %#v err=%v", recovered, err)
	}
	passed := failed
	passed.Passed = true
	if _, err := CompleteDerivedObservation(context.Background(), authority, control, root, plan, baseline, target, journal, passed); err == nil {
		t.Fatal("durable rollback decision was changed to accepted")
	}
}

func TestDerivedJournalCASAllowsOnlyOneConcurrentDecision(t *testing.T) {
	plan := derivedPlanForRole(t, "semantic")
	authority := newMemoryAuthority(asset("alpha", 1))
	root := filepath.Join(t.TempDir(), "state")
	seedActiveGeneration(t, authority, root, plan.BaselineRun.VectorSpace)
	journalDir := filepath.Join(t.TempDir(), "journal")
	first, _ := OpenDerivedJournal(journalDir)
	builder := &recordBuilder{runtime: plan.TargetRun}
	record, err := PrepareDerivedGeneration(context.Background(), authority, root, plan, builder, first, "gen-concurrent")
	if err != nil {
		t.Fatal(err)
	}
	left, _ := OpenDerivedJournal(journalDir)
	right, _ := OpenDerivedJournal(journalDir)
	next := record
	next.Revision++
	next.Phase = DerivedPhaseSwitching
	results := make(chan error, 2)
	go func() { _, appendErr := left.Append(record.Revision, next); results <- appendErr }()
	go func() { _, appendErr := right.Append(record.Revision, next); results <- appendErr }()
	successes := 0
	for range 2 {
		if appendErr := <-results; appendErr == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent lifecycle decisions committed %d times", successes)
	}
}

type recordBuilder struct {
	runtime DerivedRuntimeIdentity
	builds  int
	caught  int
	fail    bool
}

func (builder *recordBuilder) RuntimeIdentity() DerivedRuntimeIdentity { return builder.runtime }

func (builder *recordBuilder) Build(_ context.Context, root, generation string, values []domain.Information) (*derived.Store, error) {
	if builder.fail {
		return nil, errors.New("candidate build failed")
	}
	builder.builds++
	store, err := derived.CreateGeneration(root, generation)
	if err != nil {
		return nil, err
	}
	records := make([]derived.Record, len(values))
	for index, value := range values {
		records[index] = derived.Record{AssetID: value.ID, AssetRevision: value.Revision, GeneratedAt: time.Unix(int64(value.Revision), 0), Provider: "candidate", Status: "ready", EmbeddingSpace: builder.runtime.VectorSpace, Embedding: []float32{float32(value.Revision)}}
	}
	if err := store.StageGeneration(records); err != nil {
		_ = store.Discard()
		return nil, err
	}
	return store, nil
}

func (builder *recordBuilder) CatchUp(_ context.Context, store *derived.Store, snapshot []domain.Information, scope contract.ChangeScope) error {
	if builder.fail {
		return errors.New("candidate catch-up failed")
	}
	values := make(map[string]domain.Information, len(snapshot))
	for _, value := range snapshot {
		values[value.ID] = value
	}
	for _, value := range scope.Assets {
		builder.caught++
		asset, exists := values[value.ID]
		if !exists || asset.Revision != value.Revision {
			return errors.New("candidate catch-up snapshot mismatch")
		}
		if err := store.Put(derived.Record{AssetID: asset.ID, AssetRevision: asset.Revision, GeneratedAt: time.Unix(int64(asset.Revision), 0), Provider: "candidate", Status: "ready", EmbeddingSpace: builder.runtime.VectorSpace, Embedding: []float32{float32(asset.Revision)}}); err != nil {
			return err
		}
	}
	return nil
}

type memoryAuthority struct {
	mu     sync.RWMutex
	assets map[string]domain.Information
}

func newMemoryAuthority(values ...domain.Information) *memoryAuthority {
	authority := &memoryAuthority{assets: make(map[string]domain.Information, len(values))}
	for _, value := range values {
		authority.assets[value.ID] = value
	}
	return authority
}

func (authority *memoryAuthority) set(value domain.Information) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.assets[value.ID] = value
}

func (authority *memoryAuthority) CreateAsset(value domain.Information) (contract.ChangeScope, error) {
	authority.set(value)
	return scopeFor(value), nil
}
func (authority *memoryAuthority) CreateAssets(values []domain.Information) (contract.ChangeScope, error) {
	result := contract.ChangeScope{Schema: contract.AssetChangeScopeSchema}
	for _, value := range values {
		authority.set(value)
		result.Assets = append(result.Assets, contract.AssetVersion{ID: value.ID, Revision: value.Revision})
	}
	return result, result.Validate()
}
func (authority *memoryAuthority) UpdateAsset(value domain.Information, expected uint64) (contract.ChangeScope, error) {
	current, exists := authority.ReadCurrent(value.ID)
	if !exists || current.Revision != expected {
		return contract.ChangeScope{}, errors.New("revision conflict")
	}
	authority.set(value)
	return scopeFor(value), nil
}
func (authority *memoryAuthority) ReadCurrent(id string) (domain.Information, bool) {
	authority.mu.RLock()
	defer authority.mu.RUnlock()
	value, exists := authority.assets[id]
	return value, exists
}
func (authority *memoryAuthority) ReadVersion(id string, revision uint64) (domain.Information, bool) {
	value, exists := authority.ReadCurrent(id)
	return value, exists && value.Revision == revision
}
func (authority *memoryAuthority) ListCurrent() []domain.Information {
	authority.mu.RLock()
	defer authority.mu.RUnlock()
	result := make([]domain.Information, 0, len(authority.assets))
	for _, value := range authority.assets {
		result = append(result, value)
	}
	return result
}
func (*memoryAuthority) Sync() error         { return nil }
func (*memoryAuthority) Compact() error      { return nil }
func (*memoryAuthority) Backup(string) error { return nil }

type memoryControl struct {
	mu    sync.Mutex
	state contract.ControlState
}

func (control *memoryControl) ReadControl() contract.ControlState {
	control.mu.Lock()
	defer control.mu.Unlock()
	return control.state
}
func (control *memoryControl) CompareAndSwapControl(expected uint64, next contract.ControlState) (contract.ControlState, error) {
	control.mu.Lock()
	defer control.mu.Unlock()
	if control.state.Revision != expected || next.Revision != expected+1 || next.Validate() != nil {
		return contract.ControlState{}, errors.New("control conflict")
	}
	control.state = next
	return next, nil
}

func derivedPlanForRole(t *testing.T, role string) DerivedPlan {
	t.Helper()
	current := currentComposition(t)
	replacement := componentByRole(t, current, role)
	replacement.Content = append([]composition.Content(nil), replacement.Content...)
	replacement.Content[0].SHA256 = digest("derived-candidate:" + role)
	if role == "vector" {
		replacement.Config = cloneConfig(replacement.Config)
		replacement.Config["capability"] = "candidate-vector"
		replacement.Config["space"] = "candidate-vector-space"
	}
	identity, err := composition.ComponentIdentity(replacement)
	if err != nil {
		t.Fatal(err)
	}
	replacement.Identity = identity
	target, err := replaceComponent(current, replacement)
	if err != nil {
		t.Fatal(err)
	}
	validation := DerivedValidation{Schema: DerivedValidationSchema, CandidateComponent: replacement.Identity, BaselineComposition: current.Identity, TargetComposition: target.Identity, StateImpact: ImpactDerived, IntegrationSHA256: digest("derived-integration:" + role), Passed: true}
	plan, err := PrepareDerived(current, replacement, validation)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func seedActiveGeneration(t *testing.T, authority contract.AssetAuthority, root, vectorSpace string) {
	t.Helper()
	current, err := derived.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, values, err := CaptureAuthoritySnapshot(authority)
	if err != nil {
		t.Fatal(err)
	}
	next, err := derived.CreateGeneration(root, "gen-baseline")
	if err != nil {
		t.Fatal(err)
	}
	records := make([]derived.Record, len(values))
	for index, value := range values {
		records[index] = derived.Record{AssetID: value.ID, AssetRevision: value.Revision, GeneratedAt: time.Unix(1, 0), Provider: "baseline", Status: "ready", EmbeddingSpace: vectorSpace, Embedding: []float32{1}}
	}
	if err := next.StageGeneration(records); err != nil {
		t.Fatal(err)
	}
	if err := current.CommitGeneration(next, derived.GenerationMetadata{AssetCount: len(values), AssetSnapshot: snapshot.Identity, EmbeddingSpace: vectorSpace}); err != nil {
		t.Fatal(err)
	}
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}
}

func asset(id string, revision uint64) domain.Information {
	return domain.Information{Schema: domain.AssetSchema, ID: id, Revision: revision, CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(int64(revision), 0), Kind: domain.KindKnowledge, Content: id}
}

func scopeFor(value domain.Information) contract.ChangeScope {
	return contract.ChangeScope{Schema: contract.AssetChangeScopeSchema, Assets: []contract.AssetVersion{{ID: value.ID, Revision: value.Revision}}}
}

func controlFor(manifest composition.Manifest) contract.ControlState {
	kernel := ""
	for _, component := range manifest.Components {
		if component.Role == "kernel" {
			kernel = component.Identity
		}
	}
	return contract.ControlState{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: manifest.Identity, ActiveKernelGeneration: kernel}
}

func sortedVersions(values []contract.AssetVersion) []contract.AssetVersion {
	result := append([]contract.AssetVersion(nil), values...)
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result
}
