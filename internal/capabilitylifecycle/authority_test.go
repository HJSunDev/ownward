//go:build ownward_migration

package capabilitylifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/authoritycandidate"
	"github.com/HJSunDev/ownward/internal/authoritysubstrate"
	"github.com/HJSunDev/ownward/internal/composition"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
	compositionv1 "github.com/HJSunDev/ownward/manifests/compositions/v1"
)

func TestAuthorityPersistenceLifecycleCatchesUpSwitchesAndRollsBackWithoutTwoWriters(t *testing.T) {
	plan := authorityTestPlan(t)
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	initial := authorityInitialState(t, plan.Baseline)
	source, err := authoritysubstrate.Open(dataDir, initial)
	if err != nil {
		t.Fatal(err)
	}
	asset := authorityTestAsset(1, "baseline")
	if _, err := source.Assets().CreateAsset(asset); err != nil {
		t.Fatal(err)
	}
	candidate, err := authoritycandidate.Open(filepath.Join(root, "candidate"))
	if err != nil {
		t.Fatal(err)
	}
	defer candidate.Close()
	journal, _ := OpenAuthorityJournal(filepath.Join(root, "journal"))
	baseline, assets, err := CaptureAuthorityPersistence(source.Assets(), source.Control().ReadControl())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareAuthorityStore(plan, candidate, journal, baseline, assets); err != nil {
		t.Fatal(err)
	}

	asset = nextAuthorityAsset(asset, "first accepted update")
	if _, err := source.Assets().UpdateAsset(asset, 1); err != nil {
		t.Fatal(err)
	}
	latest, assets, _ := CaptureAuthorityPersistence(source.Assets(), source.Control().ReadControl())
	if _, err := CatchUpAuthorityStore(plan, candidate, journal, latest, assets); err != nil {
		t.Fatal(err)
	}

	// A write after the caller's first snapshot is mechanically detected inside
	// the final barrier; the stale snapshot cannot be promoted.
	stale, _, _ := CaptureAuthorityPersistence(source.Assets(), source.Control().ReadControl())
	asset = nextAuthorityAsset(asset, "write in the final gap")
	if _, err := source.Assets().UpdateAsset(asset, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := PromoteAuthorityStore(plan, source.Assets(), candidate, source.Control(), journal, stale); err == nil || !strings.Contains(err.Error(), "最终权威屏障") {
		t.Fatalf("stale final snapshot was promoted: %v", err)
	}
	latest, assets, _ = CaptureAuthorityPersistence(source.Assets(), source.Control().ReadControl())
	if _, err := CatchUpAuthorityStore(plan, candidate, journal, latest, assets); err != nil {
		t.Fatal(err)
	}
	record, err := PromoteAuthorityStore(plan, source.Assets(), candidate, source.Control(), journal, latest)
	if err != nil || record.Phase != AuthorityPhaseObserving || source.Control().ReadControl().ActiveComposition != plan.Target.Identity {
		t.Fatalf("authority promote failed: %#v %v", record, err)
	}

	baselineGuard := &ActiveAuthority{Assets: source.Assets(), Control: source.Control(), Composition: plan.Baseline.Identity}
	if _, err := baselineGuard.CreateAsset(authorityTestAsset(1, "forbidden stale source")); err == nil {
		t.Fatal("retained baseline still had write authority")
	}
	candidateGuard := &ActiveAuthority{Assets: candidate, Control: source.Control(), Composition: plan.Target.Identity}
	asset = nextAuthorityAsset(asset, "observation update")
	if _, err := candidateGuard.UpdateAsset(asset, 3); err != nil {
		t.Fatal(err)
	}

	activeSnapshot, _, _ := CaptureAuthorityPersistence(candidate, source.Control().ReadControl())
	failed := AuthorityObservation{Schema: AuthorityObservationSchema, Plan: plan.Identity, ActiveComposition: plan.Target.Identity, ReportSHA256: authorityDigest("failed-observation"), Passed: false}
	rollback := &legacyAuthorityCandidate{AssetAuthority: source.Assets()}
	history, err := candidate.ChangesSince(record.Baseline.Versions)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := ChangesFromAuthorityHistory(rollback.ListCurrent(), history)
	if err != nil || ApplyAuthorityChanges(rollback, changes) != nil {
		t.Fatalf("rollback source catch-up failed: %v", err)
	}
	record, err = CompleteAuthorityObservation(plan, candidate, rollback, source.Control(), journal, failed, activeSnapshot, "")
	if err != nil || record.Phase != AuthorityPhaseRolledBack || source.Control().ReadControl().ActiveComposition != plan.Baseline.Identity {
		t.Fatalf("authority rollback failed: %#v %v", record, err)
	}
	actual, ok := source.Assets().ReadCurrent(asset.ID)
	if !ok || actual.Revision != 4 || actual.Content != asset.Content {
		t.Fatalf("rollback source lost observation updates: %#v", actual)
	}
	if _, err := candidateGuard.UpdateAsset(nextAuthorityAsset(asset, "forbidden candidate write"), 4); err == nil {
		t.Fatal("rolled-back candidate retained write authority")
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorityPromotionBarrierRejectsUncaughtTailWithoutCandidateWrites(t *testing.T) {
	plan := authorityTestPlan(t)
	root := t.TempDir()
	source, err := authoritysubstrate.Open(filepath.Join(root, "data"), authorityInitialState(t, plan.Baseline))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := source.Assets().CreateAsset(authorityTestAsset(1, "baseline")); err != nil {
		t.Fatal(err)
	}
	store, err := authoritycandidate.Open(filepath.Join(root, "candidate"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	candidate := &trackedAuthorityCandidate{AuthorityCandidate: store}
	journal, _ := OpenAuthorityJournal(filepath.Join(root, "journal"))
	baseline, assets, _ := CaptureAuthorityPersistence(source.Assets(), source.Control().ReadControl())
	if _, err := PrepareAuthorityStore(plan, candidate, journal, baseline, assets); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 64; index++ {
		value := authorityTestAsset(1, strings.Repeat("tail", 1024))
		value.ID = fmt.Sprintf("tail-%03d", index)
		if _, err := source.Assets().CreateAsset(value); err != nil {
			t.Fatal(err)
		}
	}
	latest, latestAssets, _ := CaptureAuthorityPersistence(source.Assets(), source.Control().ReadControl())
	if _, err := PromoteAuthorityStore(plan, source.Assets(), candidate, source.Control(), journal, latest); err == nil {
		t.Fatal("promotion copied an uncaught tail inside the final barrier")
	}
	if candidate.applyCalls != 0 {
		t.Fatalf("promotion wrote candidate inside final barrier: %d calls", candidate.applyCalls)
	}
	if _, err := CatchUpAuthorityStore(plan, candidate, journal, latest, latestAssets); err != nil {
		t.Fatal(err)
	}
	candidate.applyCalls = 0
	record, err := PromoteAuthorityStore(plan, source.Assets(), candidate, source.Control(), journal, latest)
	if err != nil || record.Phase != AuthorityPhaseObserving {
		t.Fatalf("promotion rejected an externally caught-up candidate: %#v %v", record, err)
	}
	if candidate.applyCalls != 0 {
		t.Fatalf("successful promotion wrote candidate inside final barrier: %d calls", candidate.applyCalls)
	}
}

func TestAuthorityRollbackBarrierRejectsUncaughtTailWithoutHistoryReplay(t *testing.T) {
	plan := authorityTestPlan(t)
	root := t.TempDir()
	source, _ := authoritysubstrate.Open(filepath.Join(root, "data"), authorityInitialState(t, plan.Baseline))
	defer source.Close()
	asset := authorityTestAsset(1, "baseline")
	_, _ = source.Assets().CreateAsset(asset)
	store, _ := authoritycandidate.Open(filepath.Join(root, "candidate"))
	defer store.Close()
	active := &trackedAuthorityCandidate{AuthorityCandidate: store}
	journal, _ := OpenAuthorityJournal(filepath.Join(root, "journal"))
	snapshot, assets, _ := CaptureAuthorityPersistence(source.Assets(), source.Control().ReadControl())
	_, _ = PrepareAuthorityStore(plan, active, journal, snapshot, assets)
	_, _ = PromoteAuthorityStore(plan, source.Assets(), active, source.Control(), journal, snapshot)
	guard := &ActiveAuthority{Assets: active, Control: source.Control(), Composition: plan.Target.Identity}
	asset = nextAuthorityAsset(asset, "observation tail")
	if _, err := guard.UpdateAsset(asset, 1); err != nil {
		t.Fatal(err)
	}
	latest, _, _ := CaptureAuthorityPersistence(active, source.Control().ReadControl())
	rollback := &trackedAuthorityCandidate{AuthorityCandidate: &legacyAuthorityCandidate{AssetAuthority: source.Assets()}}
	observation := AuthorityObservation{Schema: AuthorityObservationSchema, Plan: plan.Identity, ActiveComposition: plan.Target.Identity, ReportSHA256: authorityDigest("failed-observation"), Passed: false}
	if _, err := CompleteAuthorityObservation(plan, active, rollback, source.Control(), journal, observation, latest, ""); err == nil {
		t.Fatal("rollback replayed an uncaught tail inside the final barrier")
	}
	if active.historyCalls != 0 || rollback.applyCalls != 0 {
		t.Fatalf("rollback barrier replayed history: history=%d apply=%d", active.historyCalls, rollback.applyCalls)
	}
	record, _, _ := journal.Read()
	history, err := active.ChangesSince(record.Baseline.Versions)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := ChangesFromAuthorityHistory(rollback.ListCurrent(), history)
	if err != nil || rollback.ApplyChanges(changes) != nil {
		t.Fatalf("external rollback catch-up failed: %v", err)
	}
	asset = nextAuthorityAsset(asset, "write after rollback catch-up")
	if _, err := guard.UpdateAsset(asset, 2); err != nil {
		t.Fatal(err)
	}
	latest, _, _ = CaptureAuthorityPersistence(active, source.Control().ReadControl())
	beforeHistory, beforeApply := active.historyCalls, rollback.applyCalls
	if _, err := CompleteAuthorityObservation(plan, active, rollback, source.Control(), journal, observation, latest, ""); err == nil {
		t.Fatal("rollback accepted a write after external catch-up")
	}
	if active.historyCalls != beforeHistory || rollback.applyCalls != beforeApply {
		t.Fatal("rollback final barrier replayed work after a new write")
	}
	history, err = active.ChangesSince(record.Baseline.Versions)
	if err != nil {
		t.Fatal(err)
	}
	changes, err = ChangesFromAuthorityHistory(rollback.ListCurrent(), history)
	if err != nil || rollback.ApplyChanges(changes) != nil {
		t.Fatalf("incremental rollback retry failed: %v", err)
	}
	beforeHistory, beforeApply = active.historyCalls, rollback.applyCalls
	record, err = CompleteAuthorityObservation(plan, active, rollback, source.Control(), journal, observation, latest, "")
	if err != nil || record.Phase != AuthorityPhaseRolledBack {
		t.Fatalf("externally caught-up rollback did not complete: %#v %v", record, err)
	}
	if active.historyCalls != beforeHistory || rollback.applyCalls != beforeApply {
		t.Fatal("successful rollback final barrier replayed history")
	}
}

func TestAuthorityPersistenceLifecycleAcceptsOnlyWithRecoverableBackup(t *testing.T) {
	plan := authorityTestPlan(t)
	root := t.TempDir()
	source, err := authoritysubstrate.Open(filepath.Join(root, "data"), authorityInitialState(t, plan.Baseline))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	asset := authorityTestAsset(1, "accepted")
	if _, err := source.Assets().CreateAsset(asset); err != nil {
		t.Fatal(err)
	}
	candidate, _ := authoritycandidate.Open(filepath.Join(root, "candidate"))
	defer candidate.Close()
	journal, _ := OpenAuthorityJournal(filepath.Join(root, "journal"))
	snapshot, assets, _ := CaptureAuthorityPersistence(source.Assets(), source.Control().ReadControl())
	if _, err := PrepareAuthorityStore(plan, candidate, journal, snapshot, assets); err != nil {
		t.Fatal(err)
	}
	if _, err := PromoteAuthorityStore(plan, source.Assets(), candidate, source.Control(), journal, snapshot); err != nil {
		t.Fatal(err)
	}
	active, _, _ := CaptureAuthorityPersistence(candidate, source.Control().ReadControl())
	observation := AuthorityObservation{Schema: AuthorityObservationSchema, Plan: plan.Identity, ActiveComposition: plan.Target.Identity, ReportSHA256: authorityDigest("passed-observation"), Passed: true}
	if _, err := CompleteAuthorityObservation(plan, candidate, nil, source.Control(), journal, observation, active, ""); err == nil {
		t.Fatal("candidate was accepted without a recoverable backup")
	}
	backup := filepath.Join(root, "accepted.ownward")
	if err := candidate.BackupAuthority(backup, source.Control().ReadControl()); err != nil {
		t.Fatal(err)
	}
	record, err := CompleteAuthorityObservation(plan, candidate, nil, source.Control(), journal, observation, active, authorityDigestFile(t, backup))
	if err != nil || record.Phase != AuthorityPhaseAccepted {
		t.Fatalf("accepted candidate did not close: %#v %v", record, err)
	}
	restoredControl, err := authoritycandidate.RestoreAuthority(backup, filepath.Join(root, "restored"))
	if err != nil || restoredControl != source.Control().ReadControl() {
		t.Fatalf("accepted backup was not recoverable: %#v %v", restoredControl, err)
	}
}

func TestAuthorityPromotionRecoversAfterControlCASBeforeJournal(t *testing.T) {
	plan := authorityTestPlan(t)
	root := t.TempDir()
	source, _ := authoritysubstrate.Open(filepath.Join(root, "data"), authorityInitialState(t, plan.Baseline))
	defer source.Close()
	asset := authorityTestAsset(1, "crash recovery")
	_, _ = source.Assets().CreateAsset(asset)
	candidate, _ := authoritycandidate.Open(filepath.Join(root, "candidate"))
	defer candidate.Close()
	baseJournal, _ := OpenAuthorityJournal(filepath.Join(root, "journal"))
	snapshot, assets, _ := CaptureAuthorityPersistence(source.Assets(), source.Control().ReadControl())
	_, _ = PrepareAuthorityStore(plan, candidate, baseJournal, snapshot, assets)
	failing := &failAuthorityJournal{AuthorityJournal: baseJournal, phase: AuthorityPhaseObserving}
	if _, err := PromoteAuthorityStore(plan, source.Assets(), candidate, source.Control(), failing, snapshot); err == nil {
		t.Fatal("injected post-CAS checkpoint failure was not observed")
	}
	current, _, err := baseJournal.Read()
	if err != nil || current.Phase != AuthorityPhaseSwitching || source.Control().ReadControl().ActiveComposition != plan.Target.Identity {
		t.Fatalf("post-CAS crash boundary not preserved: %#v %v", current, err)
	}
	active, _, _ := CaptureAuthorityPersistence(candidate, source.Control().ReadControl())
	recovered, err := ReconcileAuthoritySwitch(plan, source.Control(), baseJournal, active)
	if err != nil || recovered.Phase != AuthorityPhaseObserving {
		t.Fatalf("post-CAS recovery failed: %#v %v", recovered, err)
	}
}

func TestAuthorityRollbackRecoversAfterControlCASBeforeJournal(t *testing.T) {
	plan := authorityTestPlan(t)
	root := t.TempDir()
	source, _ := authoritysubstrate.Open(filepath.Join(root, "data"), authorityInitialState(t, plan.Baseline))
	defer source.Close()
	asset := authorityTestAsset(1, "rollback crash")
	_, _ = source.Assets().CreateAsset(asset)
	candidate, _ := authoritycandidate.Open(filepath.Join(root, "candidate"))
	defer candidate.Close()
	journal, _ := OpenAuthorityJournal(filepath.Join(root, "journal"))
	snapshot, assets, _ := CaptureAuthorityPersistence(source.Assets(), source.Control().ReadControl())
	_, _ = PrepareAuthorityStore(plan, candidate, journal, snapshot, assets)
	_, _ = PromoteAuthorityStore(plan, source.Assets(), candidate, source.Control(), journal, snapshot)
	active, _, _ := CaptureAuthorityPersistence(candidate, source.Control().ReadControl())
	observation := AuthorityObservation{Schema: AuthorityObservationSchema, Plan: plan.Identity, ActiveComposition: plan.Target.Identity, ReportSHA256: authorityDigest("rollback-crash"), Passed: false}
	failing := &failAuthorityJournal{AuthorityJournal: journal, phase: AuthorityPhaseRolledBack}
	if _, err := CompleteAuthorityObservation(plan, candidate, &legacyAuthorityCandidate{AssetAuthority: source.Assets()}, source.Control(), failing, observation, active, ""); err == nil {
		t.Fatal("injected post-rollback-CAS checkpoint failure was not observed")
	}
	record, _, _ := journal.Read()
	if record.Phase != AuthorityPhaseRollbackReady || source.Control().ReadControl().ActiveComposition != plan.Baseline.Identity {
		t.Fatalf("rollback crash boundary was not preserved: %#v", record)
	}
	baseline, _, _ := CaptureAuthorityPersistence(source.Assets(), source.Control().ReadControl())
	recovered, err := ReconcileAuthorityRollback(plan, source.Control(), journal, baseline)
	if err != nil || recovered.Phase != AuthorityPhaseRolledBack {
		t.Fatalf("rollback crash recovery failed: %#v %v", recovered, err)
	}
}

func TestAuthorityLifecycleRejectsCheckpointTamperAndStaleControl(t *testing.T) {
	plan := authorityTestPlan(t)
	root := t.TempDir()
	source, _ := authoritysubstrate.Open(filepath.Join(root, "data"), authorityInitialState(t, plan.Baseline))
	defer source.Close()
	asset := authorityTestAsset(1, "tamper")
	_, _ = source.Assets().CreateAsset(asset)
	candidate, _ := authoritycandidate.Open(filepath.Join(root, "candidate"))
	defer candidate.Close()
	journalDir := filepath.Join(root, "journal")
	journal, _ := OpenAuthorityJournal(journalDir)
	snapshot, assets, _ := CaptureAuthorityPersistence(source.Assets(), source.Control().ReadControl())
	_, _ = PrepareAuthorityStore(plan, candidate, journal, snapshot, assets)
	path := filepath.Join(journalDir, authorityRecordName(1))
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	encoded[len(encoded)/2] ^= 1
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := journal.Read(); err == nil {
		t.Fatal("tampered authority lifecycle checkpoint was accepted")
	}

	// A different durable decision is never overwritten by a stale plan.
	cleanJournal, _ := OpenAuthorityJournal(filepath.Join(root, "clean-journal"))
	_, _ = PrepareAuthorityStore(plan, candidate, cleanJournal, snapshot, assets)
	state := source.Control().ReadControl()
	other := state
	other.Revision++
	other.ActiveComposition = authorityDigest("other-composition")
	if _, err := source.Control().CompareAndSwapControl(state.Revision, other); err != nil {
		t.Fatal(err)
	}
	latest, _, _ := CaptureAuthorityPersistence(source.Assets(), other)
	if _, err := PromoteAuthorityStore(plan, source.Assets(), candidate, source.Control(), cleanJournal, latest); err == nil {
		t.Fatal("stale authority plan overwrote another durable decision")
	}
}

type legacyAuthorityCandidate struct{ contract.AssetAuthority }

func (adapter *legacyAuthorityCandidate) Seed(values []domain.Information) error {
	return ApplyAuthorityChanges(adapter.AssetAuthority, values)
}
func (adapter *legacyAuthorityCandidate) ApplyChanges(values []domain.Information) error {
	return ApplyAuthorityChanges(adapter.AssetAuthority, values)
}
func (adapter *legacyAuthorityCandidate) ChangesSince([]contract.AssetVersion) ([]domain.Information, error) {
	return nil, errors.New("legacy rollback adapter does not own active change history")
}
func (adapter *legacyAuthorityCandidate) BackupAuthority(string, contract.ControlState) error {
	return errors.New("legacy rollback adapter does not publish candidate backups")
}

type trackedAuthorityCandidate struct {
	AuthorityCandidate
	applyCalls   int
	historyCalls int
}

func (candidate *trackedAuthorityCandidate) ApplyChanges(values []domain.Information) error {
	candidate.applyCalls++
	return candidate.AuthorityCandidate.ApplyChanges(values)
}

func (candidate *trackedAuthorityCandidate) ChangesSince(versions []contract.AssetVersion) ([]domain.Information, error) {
	candidate.historyCalls++
	return candidate.AuthorityCandidate.ChangesSince(versions)
}

type failAuthorityJournal struct {
	AuthorityJournal
	phase string
	used  bool
}

func (journal *failAuthorityJournal) Append(expected uint64, next AuthorityRecord) (AuthorityRecord, error) {
	if next.Phase == journal.phase && !journal.used {
		journal.used = true
		return AuthorityRecord{}, errors.New("injected checkpoint crash")
	}
	return journal.AuthorityJournal.Append(expected, next)
}

func authorityTestPlan(t *testing.T) AuthorityPlan {
	t.Helper()
	current, err := composition.Parse(compositionv1.CurrentCollaborative())
	if err != nil {
		t.Fatal(err)
	}
	component, ok := lookupComponent(current, "authority-substrate")
	if !ok {
		t.Fatal("current composition lacks authority substrate")
	}
	replacement := component
	replacement.Content = append([]composition.Content(nil), component.Content...)
	replacement.Content[0].SHA256 = authorityDigest("candidate-authority-content")
	replacement.Identity, err = composition.ComponentIdentity(replacement)
	if err != nil {
		t.Fatal(err)
	}
	target, err := replaceComponent(current, replacement)
	if err != nil {
		t.Fatal(err)
	}
	validation := AuthorityValidation{
		Schema: AuthorityValidationSchema, CandidateComponent: replacement.Identity,
		BaselineComposition: current.Identity, TargetComposition: target.Identity,
		StateImpact: ImpactAuthority, CandidateFormat: authoritycandidate.Format,
		IntegrationSHA256: authorityDigest("authority-integration"), AssetSemanticsPassed: true,
		BackupRestorePassed: true, ExclusiveWriterPassed: true, IntegrationBaselinePass: true,
	}
	plan, err := PrepareAuthority(current, replacement, validation)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func authorityInitialState(t *testing.T, manifest composition.Manifest) contract.ControlState {
	t.Helper()
	for _, component := range manifest.Components {
		if component.Role == "kernel" {
			return contract.ControlState{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: manifest.Identity, ActiveKernelGeneration: component.Identity}
		}
	}
	t.Fatal("composition lacks kernel")
	return contract.ControlState{}
}

func authorityTestAsset(revision uint64, content string) domain.Information {
	now := time.Date(2026, 8, 28, 8, 0, 0, 0, time.UTC)
	return domain.Information{Schema: domain.AssetSchema, ID: "authority-lifecycle-asset", Revision: revision, CreatedAt: now, UpdatedAt: now, Kind: domain.KindKnowledge, Content: content}
}

func nextAuthorityAsset(value domain.Information, content string) domain.Information {
	value.Revision++
	value.UpdatedAt = value.UpdatedAt.Add(time.Minute)
	value.Content = content
	return value
}

func authorityDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func authorityDigestFile(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(hasher.Sum(nil))
}
