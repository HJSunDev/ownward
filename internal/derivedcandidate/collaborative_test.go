//go:build ownward_migration

package derivedcandidate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/authorityport"
	"github.com/HJSunDev/ownward/internal/capabilitylifecycle"
	"github.com/HJSunDev/ownward/internal/composition"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/embedding"
	compositionv1 "github.com/HJSunDev/ownward/manifests/compositions/v1"
)

func TestCurrentCollaborativeBoundaryRunsCompleteDerivedLifecycle(t *testing.T) {
	manifest, err := composition.Parse(compositionv1.CurrentCollaborative())
	if err != nil {
		t.Fatal(err)
	}
	replacement := component(t, manifest, "kernel")
	previous := replacement
	previous.Content = append([]composition.Content(nil), previous.Content...)
	previous.Content[0].SHA256 = hash("previous-kernel-content")
	previous.Identity, err = composition.ComponentIdentity(previous)
	if err != nil {
		t.Fatal(err)
	}
	baseline, _, err := capabilitylifecycle.InspectDerivedTarget(manifest, previous)
	if err != nil {
		t.Fatal(err)
	}
	target, _, err := capabilitylifecycle.InspectDerivedTarget(baseline, replacement)
	if err != nil {
		t.Fatal(err)
	}
	if target.Identity != manifest.Identity {
		t.Fatal("current candidate binary did not reconstruct its embedded composition")
	}
	validation := capabilitylifecycle.DerivedValidation{
		Schema: capabilitylifecycle.DerivedValidationSchema, CandidateComponent: replacement.Identity,
		BaselineComposition: baseline.Identity, TargetComposition: target.Identity,
		StateImpact: capabilitylifecycle.ImpactDerived, IntegrationSHA256: hash("integration"), Passed: true,
	}
	plan, err := capabilitylifecycle.PrepareDerived(baseline, replacement, validation)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	assetStore, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	authority, err := authorityport.Bind(assetStore)
	if err != nil {
		t.Fatal(err)
	}
	value := domain.Information{Schema: domain.AssetSchema, ID: "asset", Revision: 1, CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(1, 0), Kind: domain.KindKnowledge, Content: "isolated lifecycle fact"}
	if _, err := authority.CreateAsset(value); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(root, "state")
	seedGeneration(t, authority, stateRoot, plan.BaselineRun.VectorSpace)
	journal, _ := capabilitylifecycle.OpenDerivedJournal(filepath.Join(root, "lifecycle"))
	control := &testControl{state: controlFor(baseline)}
	targetBuilder := &Collaborative{Vector: lifecycleVector{runtime: plan.TargetRun}}
	if targetBuilder.RuntimeIdentity() != plan.TargetRun {
		t.Fatal("candidate builder identity is not derived from the embedded release composition")
	}
	record, err := capabilitylifecycle.PrepareDerivedGeneration(context.Background(), authority, stateRoot, plan, targetBuilder, journal, "gen-current-collaborative")
	if err != nil || record.Phase != capabilitylifecycle.DerivedPhaseReady {
		t.Fatalf("current collaborative candidate did not build: %#v %v", record, err)
	}
	value.Revision = 2
	value.UpdatedAt = time.Unix(2, 0)
	value.Content = "isolated lifecycle fact updated"
	if _, err := authority.UpdateAsset(value, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := capabilitylifecycle.CatchUpDerivedGeneration(context.Background(), authority, stateRoot, plan, targetBuilder, journal); err != nil {
		t.Fatal(err)
	}
	if _, err := capabilitylifecycle.PromoteDerived(authority, control, stateRoot, plan, journal); err != nil {
		t.Fatal(err)
	}
	observation := capabilitylifecycle.DerivedObservation{Schema: capabilitylifecycle.DerivedObservationSchema, Plan: plan.Identity, ActiveComposition: plan.Target.Identity, ReportSHA256: hash("passing-observation"), Passed: true}
	record, err = capabilitylifecycle.CompleteDerivedObservation(context.Background(), authority, control, stateRoot, plan, nil, targetBuilder, journal, observation)
	if err != nil || record.Phase != capabilitylifecycle.DerivedPhaseAccepted {
		t.Fatalf("current collaborative lifecycle did not close: %#v %v", record, err)
	}
	active, err := derived.ActiveGeneration(stateRoot)
	if err != nil || active.Generation != record.Candidate.Generation {
		t.Fatalf("candidate did not become the sole active generation: %#v %v", active, err)
	}
	_ = assetStore.Close()
}

func TestCurrentCollaborativeBuilderBindsEachDerivedCandidateRoleToEmbeddedRelease(t *testing.T) {
	manifest, err := composition.Parse(compositionv1.CurrentCollaborative())
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"semantic", "vector", "kernel"} {
		t.Run(role, func(t *testing.T) {
			replacement := component(t, manifest, role)
			previous := replacement
			previous.Content = append([]composition.Content(nil), previous.Content...)
			previous.Content[0].SHA256 = hash("previous-" + role)
			previous.Identity, err = composition.ComponentIdentity(previous)
			if err != nil {
				t.Fatal(err)
			}
			baseline, _, inspectErr := capabilitylifecycle.InspectDerivedTarget(manifest, previous)
			if inspectErr != nil {
				t.Fatal(inspectErr)
			}
			target, _, inspectErr := capabilitylifecycle.InspectDerivedTarget(baseline, replacement)
			if inspectErr != nil || target.Identity != manifest.Identity {
				t.Fatalf("%s candidate did not resolve to embedded release: %v", role, inspectErr)
			}
			validation := capabilitylifecycle.DerivedValidation{
				Schema: capabilitylifecycle.DerivedValidationSchema, CandidateComponent: replacement.Identity,
				BaselineComposition: baseline.Identity, TargetComposition: target.Identity,
				StateImpact: capabilitylifecycle.ImpactDerived, IntegrationSHA256: hash("identity-" + role), Passed: true,
			}
			plan, prepareErr := capabilitylifecycle.PrepareDerived(baseline, replacement, validation)
			if prepareErr != nil {
				t.Fatal(prepareErr)
			}
			builder := &Collaborative{}
			if builder.RuntimeIdentity() != plan.TargetRun {
				t.Fatalf("%s candidate accepted a runtime identity not sealed into its binary", role)
			}
		})
	}
}

type lifecycleVector struct {
	runtime capabilitylifecycle.DerivedRuntimeIdentity
}

func (value lifecycleVector) Name() string { return value.runtime.VectorCapability }
func (value lifecycleVector) Space() embedding.Space {
	return embedding.Space{ID: value.runtime.VectorSpace, Dimensions: value.runtime.VectorDimensions}
}
func (value lifecycleVector) EmbedDocuments(_ context.Context, values []string) ([][]float32, error) {
	result := make([][]float32, len(values))
	for index := range result {
		result[index] = make([]float32, value.runtime.VectorDimensions)
		result[index][0] = 1
	}
	return result, nil
}
func (value lifecycleVector) EmbedQuery(context.Context, string) ([]float32, error) {
	return make([]float32, value.runtime.VectorDimensions), nil
}
func (lifecycleVector) Close() error { return nil }

type testControl struct {
	mu    sync.Mutex
	state contract.ControlState
}

func (control *testControl) ReadControl() contract.ControlState {
	control.mu.Lock()
	defer control.mu.Unlock()
	return control.state
}
func (control *testControl) CompareAndSwapControl(expected uint64, next contract.ControlState) (contract.ControlState, error) {
	control.mu.Lock()
	defer control.mu.Unlock()
	if control.state.Revision != expected || next.Revision != expected+1 {
		return contract.ControlState{}, errors.New("control conflict")
	}
	control.state = next
	return next, nil
}

func seedGeneration(t *testing.T, authority contract.AssetAuthority, root, space string) {
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
		records[index] = derived.Record{AssetID: value.ID, AssetRevision: value.Revision, GeneratedAt: time.Unix(1, 0), Provider: "baseline", Status: "ready", EmbeddingSpace: space, Embedding: make([]float32, 512)}
	}
	if err := next.StageGeneration(records); err != nil {
		t.Fatal(err)
	}
	if err := current.CommitGeneration(next, derived.GenerationMetadata{AssetCount: len(values), AssetSnapshot: snapshot.Identity, EmbeddingSpace: space}); err != nil {
		t.Fatal(err)
	}
	_ = current.Close()
}

func component(t *testing.T, manifest composition.Manifest, role string) composition.Component {
	t.Helper()
	for _, value := range manifest.Components {
		if value.Role == role {
			return value
		}
	}
	t.Fatalf("missing component %s", role)
	return composition.Component{}
}

func controlFor(manifest composition.Manifest) contract.ControlState {
	kernel := ""
	for _, value := range manifest.Components {
		if value.Role == "kernel" {
			kernel = value.Identity
		}
	}
	return contract.ControlState{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: manifest.Identity, ActiveKernelGeneration: kernel}
}

func hash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
