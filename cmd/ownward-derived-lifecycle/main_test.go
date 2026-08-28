//go:build ownward_migration

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/authoritysubstrate"
	"github.com/HJSunDev/ownward/internal/capabilitylifecycle"
	"github.com/HJSunDev/ownward/internal/composition"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/derivedcandidate"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/embedding"
	compositionv1 "github.com/HJSunDev/ownward/manifests/compositions/v1"
)

func TestFormalDerivedLifecycleEntryRunsInIndependentProcesses(t *testing.T) {
	fixture := newCommandFixture(t)
	runCommandProcess(t, "target", "prepare", fixture.args("--generation", "gen-command-candidate")...)

	// A product update accepted after the baseline must be caught up without a
	// rebuild. A further un-caught-up version makes the final barrier fail open.
	fixture.updateAuthority(t, fixture.plan.Baseline, 2, false)
	runCommandProcess(t, "target", "catch-up", fixture.args()...)
	fixture.updateAuthority(t, fixture.plan.Baseline, 3, false)
	runCommandProcessFails(t, "target", "promote", fixture.args()...)
	assertActive(t, fixture, fixture.plan.Baseline, "gen-baseline")
	runCommandProcess(t, "target", "catch-up", fixture.args()...)
	runCommandProcess(t, "target", "promote", fixture.args()...)
	assertActive(t, fixture, fixture.plan.Target, "gen-command-candidate")

	// Normal product work may append an incremental tail during observation.
	// The final command reseals the full tail and rebinds the active pointer
	// before accepting the observation.
	fixture.updateAuthority(t, fixture.plan.Target, 4, true)
	observation := capabilitylifecycle.DerivedObservation{
		Schema: capabilitylifecycle.DerivedObservationSchema, Plan: fixture.plan.Identity,
		ActiveComposition: fixture.plan.Target.Identity, ReportSHA256: commandDigest("pass-observation"), Passed: true,
	}
	writeJSON(t, fixture.observation, observation)
	runCommandProcess(t, "target", "observe", fixture.args("--observation", fixture.observation)...)
	runCommandProcess(t, "target", "status", fixture.args()...)
	before := directoryBytes(t, fixture.journal)
	runCommandProcess(t, "target", "observe", fixture.args("--observation", fixture.observation)...)
	after := directoryBytes(t, fixture.journal)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("repeated accepted observation changed durable checkpoints")
	}
	record := fixture.record(t)
	if record.Phase != capabilitylifecycle.DerivedPhaseAccepted || record.CandidateSnapshot.Assets[0].Revision != 4 ||
		record.Candidate.SealedBytes != record.Candidate.LogBytes {
		t.Fatalf("accepted lifecycle did not bind the complete tail: %#v", record)
	}
}

func TestFormalPlanIsBoundToEmbeddedCandidateRatherThanCallerIdentity(t *testing.T) {
	current, err := composition.Parse(compositionv1.CurrentCollaborative())
	if err != nil {
		t.Fatal(err)
	}
	replacement := commandComponent(t, current, "kernel")
	previous := replacement
	previous.Content = append([]composition.Content(nil), previous.Content...)
	previous.Content[0].SHA256 = commandDigest("formal-plan-baseline")
	previous.Identity, err = composition.ComponentIdentity(previous)
	if err != nil {
		t.Fatal(err)
	}
	baseline, _, err := capabilitylifecycle.InspectDerivedTarget(current, previous)
	if err != nil {
		t.Fatal(err)
	}
	target, _, err := capabilitylifecycle.InspectDerivedTarget(baseline, replacement)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	baselinePath := filepath.Join(root, "baseline.json")
	validationPath := filepath.Join(root, "validation.json")
	planPath := filepath.Join(root, "plan.json")
	file, err := os.Create(baselinePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := composition.WriteJSON(file, baseline); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	writeJSON(t, validationPath, capabilitylifecycle.DerivedValidation{
		Schema: capabilitylifecycle.DerivedValidationSchema, CandidateComponent: replacement.Identity,
		BaselineComposition: baseline.Identity, TargetComposition: target.Identity,
		StateImpact: capabilitylifecycle.ImpactDerived, IntegrationSHA256: commandDigest("formal-plan-integration"), Passed: true,
	})
	if err := run(context.Background(), []string{"plan", "--baseline", baselinePath, "--role", "kernel", "--validation", validationPath, "--output", planPath}, commandResources{}); err != nil {
		t.Fatal(err)
	}
	plan, err := capabilitylifecycle.LoadDerivedPlan(planPath)
	if err != nil || plan.Target.Identity != current.Identity || plan.Replacement.Identity != replacement.Identity {
		t.Fatalf("formal plan did not bind the embedded candidate: %#v err=%v", plan, err)
	}
	wrong := current
	wrong.Identity = commandDigest("caller-claimed-target")
	writeJSON(t, validationPath, capabilitylifecycle.DerivedValidation{
		Schema: capabilitylifecycle.DerivedValidationSchema, CandidateComponent: replacement.Identity,
		BaselineComposition: baseline.Identity, TargetComposition: wrong.Identity,
		StateImpact: capabilitylifecycle.ImpactDerived, IntegrationSHA256: commandDigest("wrong-integration"), Passed: true,
	})
	if err := run(context.Background(), []string{"plan", "--baseline", baselinePath, "--role", "kernel", "--validation", validationPath, "--output", filepath.Join(root, "wrong-plan.json")}, commandResources{}); err == nil {
		t.Fatal("caller-claimed target identity was accepted")
	}
}

func TestFormalDerivedLifecycleEntryCatchesRollbackGenerationUpAndRecovers(t *testing.T) {
	fixture := newCommandFixture(t)
	runCommandProcess(t, "target", "prepare", fixture.args("--generation", "gen-command-rollback")...)
	runCommandProcess(t, "target", "promote", fixture.args()...)
	fixture.updateAuthority(t, fixture.plan.Target, 2, true)
	observation := capabilitylifecycle.DerivedObservation{
		Schema: capabilitylifecycle.DerivedObservationSchema, Plan: fixture.plan.Identity,
		ActiveComposition: fixture.plan.Target.Identity, ReportSHA256: commandDigest("failed-observation"), Passed: false,
	}
	writeJSON(t, fixture.observation, observation)
	runCommandProcess(t, "baseline", "observe", fixture.args("--observation", fixture.observation)...)
	assertActive(t, fixture, fixture.plan.Baseline, "gen-baseline")
	record := fixture.record(t)
	if record.Phase != capabilitylifecycle.DerivedPhaseRolledBack || record.BaselineSnapshot.Assets[0].Revision != 2 ||
		record.Baseline.SealedBytes != record.Baseline.LogBytes {
		t.Fatalf("rollback did not seal the latest authority version: %#v", record)
	}
	// A second process recovers the already committed terminal decision exactly.
	before := directoryBytes(t, fixture.journal)
	runCommandProcess(t, "baseline", "observe", fixture.args("--observation", fixture.observation)...)
	if !reflect.DeepEqual(before, directoryBytes(t, fixture.journal)) {
		t.Fatal("repeated rollback changed durable checkpoints")
	}
}

// TestDerivedLifecycleCommandProcess is re-executed as a child so the tests
// exercise the same argument parsing and durable filesystem boundary as the
// formal binary. The runtime mode represents which sealed candidate binary is
// executing; it is not accepted as a command-line identity claim.
func TestDerivedLifecycleCommandProcess(t *testing.T) {
	if os.Getenv("OWNWARD_DERIVED_COMMAND_HELPER") != "1" {
		return
	}
	separator := -1
	for index, value := range os.Args {
		if value == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		t.Fatal("missing helper command")
	}
	plan, err := capabilitylifecycle.LoadDerivedPlan(os.Getenv("OWNWARD_DERIVED_PLAN"))
	if err != nil {
		t.Fatal(err)
	}
	mode := os.Getenv("OWNWARD_DERIVED_RUNTIME")
	runtime := plan.TargetRun
	resources := commandResources{
		openVector: func(string) (contract.VectorCapability, error) { return commandVector{runtime: runtime}, nil },
		newBuilder: func(vector contract.VectorCapability) capabilitylifecycle.DerivedBuilder {
			if mode == "baseline" {
				return &commandBuilder{runtime: plan.BaselineRun}
			}
			return &derivedcandidate.Collaborative{Vector: vector}
		},
	}
	if mode == "baseline" {
		runtime = plan.BaselineRun
	}
	if err := run(context.Background(), os.Args[separator+1:], resources); err != nil {
		t.Fatal(err)
	}
}

type commandFixture struct {
	root, data, planPath, journal, vector, observation string
	plan                                               capabilitylifecycle.DerivedPlan
}

func newCommandFixture(t *testing.T) commandFixture {
	t.Helper()
	current, err := composition.Parse(compositionv1.CurrentCollaborative())
	if err != nil {
		t.Fatal(err)
	}
	replacement := commandComponent(t, current, "kernel")
	previous := replacement
	previous.Content = append([]composition.Content(nil), previous.Content...)
	previous.Content[0].SHA256 = commandDigest("previous-kernel")
	previous.Identity, err = composition.ComponentIdentity(previous)
	if err != nil {
		t.Fatal(err)
	}
	baseline, _, err := capabilitylifecycle.InspectDerivedTarget(current, previous)
	if err != nil {
		t.Fatal(err)
	}
	target, _, err := capabilitylifecycle.InspectDerivedTarget(baseline, replacement)
	if err != nil || target.Identity != current.Identity {
		t.Fatalf("target is not the current embedded collaborative candidate: %v", err)
	}
	validation := capabilitylifecycle.DerivedValidation{
		Schema: capabilitylifecycle.DerivedValidationSchema, CandidateComponent: replacement.Identity,
		BaselineComposition: baseline.Identity, TargetComposition: target.Identity,
		StateImpact: capabilitylifecycle.ImpactDerived, IntegrationSHA256: commandDigest("integration"), Passed: true,
	}
	plan, err := capabilitylifecycle.PrepareDerived(baseline, replacement, validation)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	fixture := commandFixture{root: root, data: filepath.Join(root, "data"), planPath: filepath.Join(root, "plan.json"), journal: filepath.Join(root, "journal"), vector: filepath.Join(root, "vector"), observation: filepath.Join(root, "observation.json"), plan: plan}
	if err := capabilitylifecycle.WriteDerivedPlan(fixture.planPath, plan); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fixture.vector, 0o700); err != nil {
		t.Fatal(err)
	}
	substrate, err := authoritysubstrate.Open(fixture.data, controlForCommand(plan.Baseline))
	if err != nil {
		t.Fatal(err)
	}
	value := commandAsset(1)
	if _, err := substrate.Assets().CreateAsset(value); err != nil {
		t.Fatal(err)
	}
	seedCommandGeneration(t, substrate.Assets(), filepath.Join(fixture.data, "state"), plan.BaselineRun.VectorSpace)
	if err := substrate.Close(); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture commandFixture) args(extra ...string) []string {
	result := []string{"--plan", fixture.planPath, "--journal", fixture.journal, "--data-dir", fixture.data, "--vector-bundle", fixture.vector}
	return append(result, extra...)
}

func (fixture commandFixture) updateAuthority(t *testing.T, active composition.Manifest, revision uint64, updateDerived bool) {
	t.Helper()
	substrate, err := authoritysubstrate.Open(fixture.data, controlForCommand(active))
	if err != nil {
		t.Fatal(err)
	}
	value := commandAsset(revision)
	if _, err := substrate.Assets().UpdateAsset(value, revision-1); err != nil {
		t.Fatal(err)
	}
	if updateDerived {
		store, err := derived.Open(filepath.Join(fixture.data, "state"))
		if err != nil {
			t.Fatal(err)
		}
		record := commandRecord(value, fixture.plan.TargetRun.VectorSpace)
		if err := store.Put(record); err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := substrate.Close(); err != nil {
		t.Fatal(err)
	}
}

func (fixture commandFixture) record(t *testing.T) capabilitylifecycle.DerivedRecord {
	t.Helper()
	journal, err := capabilitylifecycle.OpenDerivedJournal(fixture.journal)
	if err != nil {
		t.Fatal(err)
	}
	record, exists, err := journal.Read()
	if err != nil || !exists {
		t.Fatalf("read lifecycle record: exists=%v err=%v", exists, err)
	}
	return record
}

func runCommandProcess(t *testing.T, runtime, command string, args ...string) {
	t.Helper()
	if output, err := commandProcess(runtime, command, args...); err != nil {
		t.Fatalf("%s failed: %v\n%s", command, err, output)
	}
}

func runCommandProcessFails(t *testing.T, runtime, command string, args ...string) {
	t.Helper()
	if output, err := commandProcess(runtime, command, args...); err == nil {
		t.Fatalf("%s unexpectedly passed:\n%s", command, output)
	}
}

func commandProcess(runtime, command string, args ...string) (string, error) {
	arguments := []string{"-test.run=^TestDerivedLifecycleCommandProcess$", "--", command}
	arguments = append(arguments, args...)
	cmd := exec.Command(os.Args[0], arguments...)
	planPath := ""
	for index := range args {
		if args[index] == "--plan" && index+1 < len(args) {
			planPath = args[index+1]
		}
	}
	cmd.Env = append(os.Environ(), "OWNWARD_DERIVED_COMMAND_HELPER=1", "OWNWARD_DERIVED_RUNTIME="+runtime, "OWNWARD_DERIVED_PLAN="+planPath)
	encoded, err := cmd.CombinedOutput()
	return string(encoded), err
}

func assertActive(t *testing.T, fixture commandFixture, manifest composition.Manifest, generation string) {
	t.Helper()
	substrate, err := authoritysubstrate.Open(fixture.data, controlForCommand(manifest))
	if err != nil {
		t.Fatal(err)
	}
	active, err := derived.ActiveGeneration(filepath.Join(fixture.data, "state"))
	if err != nil || active.Generation != generation {
		t.Fatalf("active generation=%#v err=%v", active, err)
	}
	_ = substrate.Close()
}

type commandVector struct {
	runtime capabilitylifecycle.DerivedRuntimeIdentity
}

func (vector commandVector) Name() string { return vector.runtime.VectorCapability }
func (vector commandVector) Space() embedding.Space {
	return embedding.Space{ID: vector.runtime.VectorSpace, Dimensions: vector.runtime.VectorDimensions}
}
func (vector commandVector) EmbedDocuments(_ context.Context, values []string) ([][]float32, error) {
	result := make([][]float32, len(values))
	for index := range result {
		result[index] = make([]float32, vector.runtime.VectorDimensions)
		result[index][0] = 1
	}
	return result, nil
}
func (vector commandVector) EmbedQuery(context.Context, string) ([]float32, error) {
	return make([]float32, vector.runtime.VectorDimensions), nil
}
func (commandVector) Close() error { return nil }

type commandBuilder struct {
	runtime capabilitylifecycle.DerivedRuntimeIdentity
}

func (builder *commandBuilder) RuntimeIdentity() capabilitylifecycle.DerivedRuntimeIdentity {
	return builder.runtime
}
func (builder *commandBuilder) Build(_ context.Context, root, generation string, assets []domain.Information) (*derived.Store, error) {
	store, err := derived.CreateGeneration(root, generation)
	if err != nil {
		return nil, err
	}
	records := make([]derived.Record, len(assets))
	for index, value := range assets {
		records[index] = commandRecord(value, builder.runtime.VectorSpace)
	}
	if err := store.StageGeneration(records); err != nil {
		_ = store.Discard()
		return nil, err
	}
	return store, nil
}
func (builder *commandBuilder) CatchUp(_ context.Context, store *derived.Store, assets []domain.Information, scope contract.ChangeScope) error {
	values := make(map[string]domain.Information, len(assets))
	for _, value := range assets {
		values[value.ID] = value
	}
	for _, version := range scope.Assets {
		value, exists := values[version.ID]
		if !exists || value.Revision != version.Revision {
			return errors.New("snapshot mismatch")
		}
		if err := store.Put(commandRecord(value, builder.runtime.VectorSpace)); err != nil {
			return err
		}
	}
	return nil
}

func seedCommandGeneration(t *testing.T, authority contract.AssetAuthority, root, space string) {
	t.Helper()
	current, err := derived.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, values, err := capabilitylifecycle.CaptureAuthoritySnapshot(authority)
	if err != nil {
		t.Fatal(err)
	}
	next, err := derived.CreateGeneration(root, "gen-baseline")
	if err != nil {
		t.Fatal(err)
	}
	records := make([]derived.Record, len(values))
	for index, value := range values {
		records[index] = commandRecord(value, space)
	}
	if err := next.StageGeneration(records); err != nil {
		t.Fatal(err)
	}
	if err := current.CommitGeneration(next, derived.GenerationMetadata{AssetCount: len(values), AssetSnapshot: snapshot.Identity, EmbeddingSpace: space}); err != nil {
		t.Fatal(err)
	}
	_ = current.Close()
}

func commandRecord(value domain.Information, space string) derived.Record {
	return derived.Record{AssetID: value.ID, AssetRevision: value.Revision, GeneratedAt: time.Unix(int64(value.Revision), 0), Provider: "command-fixture", Status: "ready", EmbeddingSpace: space, Embedding: make([]float32, 512)}
}

func commandAsset(revision uint64) domain.Information {
	return domain.Information{Schema: domain.AssetSchema, ID: "asset", Revision: revision, CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(int64(revision), 0), Kind: domain.KindKnowledge, Content: "durable fact revision " + string(rune('0'+revision))}
}

func controlForCommand(manifest composition.Manifest) contract.ControlState {
	kernel := commandComponentIdentity(manifest, "kernel")
	return contract.ControlState{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: manifest.Identity, ActiveKernelGeneration: kernel}
}

func commandComponent(t *testing.T, manifest composition.Manifest, role string) composition.Component {
	t.Helper()
	for _, component := range manifest.Components {
		if component.Role == role {
			return component
		}
	}
	t.Fatalf("missing component %s", role)
	return composition.Component{}
}

func commandComponentIdentity(manifest composition.Manifest, role string) string {
	for _, component := range manifest.Components {
		if component.Role == role {
			return component.Identity
		}
	}
	return ""
}

func commandDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil || os.WriteFile(path, append(encoded, '\n'), 0o600) != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func directoryBytes(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Name() < entries[right].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		encoded, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		result[entry.Name()] = string(encoded)
	}
	return result
}
