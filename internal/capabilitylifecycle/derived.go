//go:build ownward_migration

package capabilitylifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/HJSunDev/ownward/internal/composition"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
)

const (
	DerivedValidationSchema   = "ownward.derived-capability-validation/v1"
	DerivedPlanSchema         = "ownward.derived-capability-plan/v1"
	DerivedPlanEnvelopeSchema = "ownward.derived-capability-plan-envelope/v1"
	AuthoritySnapshotSchema   = "ownward.authority-version-snapshot/v1"
	DerivedObservationSchema  = "ownward.derived-capability-observation/v1"
	derivedPreparationSchema  = "ownward.derived-capability-preparation/v1"
	derivedCatchUpSchema      = "ownward.derived-capability-catch-up/v1"
)

type derivedPlanEnvelope struct {
	Schema string      `json:"schema"`
	Plan   DerivedPlan `json:"plan"`
	SHA256 string      `json:"sha256"`
}

type DerivedRuntimeIdentity struct {
	Composition      string `json:"composition"`
	Kernel           string `json:"kernel"`
	Semantic         string `json:"semantic"`
	Vector           string `json:"vector"`
	VectorCapability string `json:"vector_capability"`
	VectorSpace      string `json:"vector_space"`
	VectorDimensions int    `json:"vector_dimensions"`
}

type DerivedValidation struct {
	Schema              string `json:"schema"`
	CandidateComponent  string `json:"candidate_component"`
	BaselineComposition string `json:"baseline_composition"`
	TargetComposition   string `json:"target_composition"`
	StateImpact         string `json:"state_impact"`
	IntegrationSHA256   string `json:"integration_sha256"`
	Passed              bool   `json:"passed"`
}

type DerivedPlan struct {
	Schema      string                 `json:"schema"`
	Identity    string                 `json:"identity"`
	Role        string                 `json:"role"`
	Baseline    composition.Manifest   `json:"baseline"`
	Replacement composition.Component  `json:"replacement"`
	Target      composition.Manifest   `json:"target"`
	BaselineRun DerivedRuntimeIdentity `json:"baseline_runtime"`
	TargetRun   DerivedRuntimeIdentity `json:"target_runtime"`
	Validation  DerivedValidation      `json:"validation"`
}

type AuthoritySnapshot struct {
	Schema   string                  `json:"schema"`
	Identity string                  `json:"identity"`
	Assets   []contract.AssetVersion `json:"assets"`
}

type DerivedObservation struct {
	Schema            string `json:"schema"`
	Plan              string `json:"plan"`
	ActiveComposition string `json:"active_composition"`
	ReportSHA256      string `json:"report_sha256"`
	Passed            bool   `json:"passed"`
}

type derivedPreparation struct {
	Schema     string                 `json:"schema"`
	Plan       string                 `json:"plan"`
	Generation string                 `json:"generation"`
	Runtime    DerivedRuntimeIdentity `json:"runtime"`
	Snapshot   AuthoritySnapshot      `json:"snapshot"`
}

type derivedCatchUp struct {
	Schema       string            `json:"schema"`
	Plan         string            `json:"plan"`
	Generation   string            `json:"generation"`
	FromRevision uint64            `json:"from_revision"`
	Snapshot     AuthoritySnapshot `json:"snapshot"`
}

func LoadDerivedValidation(path string) (DerivedValidation, error) {
	validation, err := loadStrict[DerivedValidation](path)
	if err != nil {
		return DerivedValidation{}, err
	}
	if validation.Schema != DerivedValidationSchema || !validation.Passed || validation.StateImpact != ImpactDerived ||
		!isSHA256(validation.CandidateComponent) || !isSHA256(validation.BaselineComposition) ||
		!isSHA256(validation.TargetComposition) || !isSHA256(validation.IntegrationSHA256) {
		return DerivedValidation{}, errors.New("派生候选集成验证无效")
	}
	return validation, nil
}

// InspectDerivedTarget deterministically reconstructs the target composition
// and runtime identity without touching product state.
func InspectDerivedTarget(current composition.Manifest, replacement composition.Component) (composition.Manifest, DerivedRuntimeIdentity, error) {
	if StateImpact(replacement.Role) != ImpactDerived {
		return composition.Manifest{}, DerivedRuntimeIdentity{}, errors.New("组件不属于派生状态生命周期")
	}
	target, err := replaceComponent(current, replacement)
	if err != nil {
		return composition.Manifest{}, DerivedRuntimeIdentity{}, err
	}
	runtime, err := runtimeIdentity(target)
	return target, runtime, err
}

// InspectDerivedRuntime returns the exact runtime identity sealed into one
// composition. Candidate binaries use this instead of accepting a caller-
// supplied claim about the kernel, semantic, or vector implementation.
func InspectDerivedRuntime(manifest composition.Manifest) (DerivedRuntimeIdentity, error) {
	return runtimeIdentity(manifest)
}

// DerivedBuilder is implemented by an exact candidate (or retained baseline)
// binary. The lifecycle supplies only authority snapshot bytes and an isolated
// generation; it never supplies the active derived state.
type DerivedBuilder interface {
	RuntimeIdentity() DerivedRuntimeIdentity
	Build(context.Context, string, string, []domain.Information) (*derived.Store, error)
	CatchUp(context.Context, *derived.Store, []domain.Information, contract.ChangeScope) error
}

func PrepareDerived(current composition.Manifest, replacement composition.Component, validation DerivedValidation) (DerivedPlan, error) {
	if StateImpact(replacement.Role) != ImpactDerived {
		return DerivedPlan{}, fmt.Errorf("组件 %s 不属于派生状态生命周期", replacement.Role)
	}
	target, targetRun, err := InspectDerivedTarget(current, replacement)
	if err != nil {
		return DerivedPlan{}, err
	}
	baselineRun, err := runtimeIdentity(current)
	if err != nil {
		return DerivedPlan{}, err
	}
	plan := DerivedPlan{
		Schema: DerivedPlanSchema, Role: replacement.Role, Baseline: current,
		Replacement: replacement, Target: target, BaselineRun: baselineRun,
		TargetRun: targetRun, Validation: validation,
	}
	plan.Identity, err = derivedPlanDigest(plan)
	if err != nil {
		return DerivedPlan{}, err
	}
	if err := validateDerivedPlan(plan); err != nil {
		return DerivedPlan{}, err
	}
	return plan, nil
}

func WriteDerivedPlan(path string, plan DerivedPlan) error {
	if err := validateDerivedPlan(plan); err != nil {
		return err
	}
	payload, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	encoded, err := json.MarshalIndent(derivedPlanEnvelope{Schema: DerivedPlanEnvelopeSchema, Plan: plan, SHA256: hex.EncodeToString(digest[:])}, "", "  ")
	if err != nil {
		return err
	}
	if existing, readErr := LoadDerivedPlan(path); readErr == nil {
		if existing.Identity == plan.Identity {
			return nil
		}
		return errors.New("派生计划输出已存在且身份不同")
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	return publishNewFile(path, append(encoded, '\n'), 0o600)
}

func LoadDerivedPlan(path string) (DerivedPlan, error) {
	envelope, err := loadStrict[derivedPlanEnvelope](path)
	if err != nil {
		return DerivedPlan{}, err
	}
	if envelope.Schema != DerivedPlanEnvelopeSchema || !isSHA256(envelope.SHA256) {
		return DerivedPlan{}, errors.New("派生候选计划封装无效")
	}
	payload, err := json.Marshal(envelope.Plan)
	if err != nil {
		return DerivedPlan{}, err
	}
	digest := sha256.Sum256(payload)
	if envelope.SHA256 != hex.EncodeToString(digest[:]) {
		return DerivedPlan{}, errors.New("派生候选计划完整性校验失败")
	}
	if err := validateDerivedPlan(envelope.Plan); err != nil {
		return DerivedPlan{}, err
	}
	return envelope.Plan, nil
}

// LoadDerivedObservation reads a frozen observation report without allowing
// the lifecycle command to invent or repair any binding fields.
func LoadDerivedObservation(path string, plan DerivedPlan) (DerivedObservation, error) {
	observation, err := loadStrict[DerivedObservation](path)
	if err != nil {
		return DerivedObservation{}, err
	}
	if observation.Schema != DerivedObservationSchema || observation.Plan != plan.Identity ||
		observation.ActiveComposition != plan.Target.Identity || !isSHA256(observation.ReportSHA256) {
		return DerivedObservation{}, errors.New("派生能力观察证据与候选计划不一致")
	}
	return observation, nil
}

func CaptureAuthoritySnapshot(authority contract.AssetAuthority) (AuthoritySnapshot, []domain.Information, error) {
	if authority == nil {
		return AuthoritySnapshot{}, nil, errors.New("资产权威不能为空")
	}
	values := authority.ListCurrent()
	sort.Slice(values, func(left, right int) bool { return values[left].ID < values[right].ID })
	snapshot := AuthoritySnapshot{Schema: AuthoritySnapshotSchema, Assets: make([]contract.AssetVersion, len(values))}
	for index, value := range values {
		if strings.TrimSpace(value.ID) == "" || value.Revision == 0 || (index > 0 && values[index-1].ID == value.ID) {
			return AuthoritySnapshot{}, nil, errors.New("权威资产版本快照无效")
		}
		snapshot.Assets[index] = contract.AssetVersion{ID: value.ID, Revision: value.Revision}
	}
	identity, err := snapshotDigest(snapshot)
	if err != nil {
		return AuthoritySnapshot{}, nil, err
	}
	snapshot.Identity = identity
	return snapshot, values, nil
}

func PrepareDerivedGeneration(ctx context.Context, authority contract.AssetAuthority, root string, plan DerivedPlan, builder DerivedBuilder, journal DerivedJournal, generation string) (DerivedRecord, error) {
	snapshot, assets, err := CaptureAuthoritySnapshot(authority)
	if err != nil {
		return DerivedRecord{}, err
	}
	return PrepareDerivedGenerationAtSnapshot(ctx, root, plan, builder, journal, generation, snapshot, assets)
}

// PrepareDerivedGenerationAtSnapshot performs the long candidate build from
// values captured while the authority lock was held. The formal command closes
// that lock before entering here, so current product writes are not blocked by
// vector or semantic work.
func PrepareDerivedGenerationAtSnapshot(ctx context.Context, root string, plan DerivedPlan, builder DerivedBuilder, journal DerivedJournal, generation string, snapshot AuthoritySnapshot, assets []domain.Information) (DerivedRecord, error) {
	if err := validateDerivedPlan(plan); err != nil {
		return DerivedRecord{}, err
	}
	if err := validateSnapshotValues(snapshot, assets); err != nil {
		return DerivedRecord{}, err
	}
	if builder == nil || journal == nil || builder.RuntimeIdentity() != plan.TargetRun {
		return DerivedRecord{}, errors.New("候选运行时身份与派生计划不一致")
	}
	if current, exists, err := journal.Read(); err != nil {
		return DerivedRecord{}, err
	} else if exists && current.Plan == plan.Identity && current.Phase != DerivedPhaseAccepted && current.Phase != DerivedPhaseRolledBack {
		return current, nil
	} else if exists && current.Phase != DerivedPhaseAccepted && current.Phase != DerivedPhaseRolledBack {
		return DerivedRecord{}, errors.New("已有其他派生候选正在迁移")
	}
	active, err := derived.ActiveGeneration(root)
	if err != nil {
		return DerivedRecord{}, err
	}
	if active.Generation == "legacy" {
		return DerivedRecord{}, errors.New("活动派生状态尚未形成可回退的封存世代")
	}
	if err := validateGenerationCoverage(root, active.Generation, snapshot, plan.BaselineRun.VectorSpace); err != nil {
		return DerivedRecord{}, fmt.Errorf("活动派生世代未覆盖构建基线: %w", err)
	}
	preparation := derivedPreparation{Schema: derivedPreparationSchema, Plan: plan.Identity, Generation: generation, Runtime: plan.TargetRun, Snapshot: snapshot}
	if err := sealDerivedPreparation(root, preparation); err != nil {
		return DerivedRecord{}, err
	}
	sealed, err := derived.CandidateGenerationSealed(root, generation)
	if err != nil {
		return DerivedRecord{}, err
	}
	var state derived.GenerationState
	if sealed {
		state, err = derived.InspectGeneration(root, generation)
		if err != nil || state.AssetSnapshot != snapshot.Identity || state.EmbeddingSpace != plan.TargetRun.VectorSpace {
			return DerivedRecord{}, errors.New("已封存候选世代与耐久准备检查点不一致")
		}
	} else {
		if err := derived.DiscardInactiveGeneration(root, generation); err != nil {
			return DerivedRecord{}, err
		}
		next, buildErr := builder.Build(ctx, root, generation, assets)
		if buildErr != nil {
			_ = derived.DiscardInactiveGeneration(root, generation)
			_ = removeDerivedPreparation(root, generation)
			return DerivedRecord{}, buildErr
		}
		if next == nil || next.Root() != root || next.Generation() != generation || next.Sealed() {
			if next != nil {
				_ = next.Discard()
			}
			_ = removeDerivedPreparation(root, generation)
			return DerivedRecord{}, errors.New("候选构建器返回了无效派生世代")
		}
		state, err = next.SealGeneration(derived.GenerationMetadata{
			AssetCount: len(snapshot.Assets), AssetSnapshot: snapshot.Identity,
			EmbeddingSpace: plan.TargetRun.VectorSpace,
		})
		if err != nil {
			_ = next.Discard()
			_ = removeDerivedPreparation(root, generation)
			return DerivedRecord{}, err
		}
		_ = next.Close()
	}
	if err := validateGenerationCoverage(root, generation, snapshot, plan.TargetRun.VectorSpace); err != nil {
		_ = derived.RetireGeneration(root, generation)
		_ = removeDerivedPreparation(root, generation)
		return DerivedRecord{}, err
	}
	previousRevision := uint64(0)
	if prior, exists, _ := journal.Read(); exists {
		previousRevision = prior.Revision
	}
	record := DerivedRecord{
		Schema: DerivedRecordSchema, Revision: previousRevision + 1, Plan: plan.Identity,
		Phase: DerivedPhaseReady, Baseline: active, Candidate: state,
		BaselineSnapshot: snapshot, CandidateSnapshot: snapshot,
	}
	record, err = journal.Append(previousRevision, record)
	if err == nil {
		_ = removeDerivedPreparation(root, generation)
	}
	return record, err
}

func sealDerivedPreparation(root string, expected derivedPreparation) error {
	path, err := derived.CandidateCheckpointPath(root, expected.Generation)
	if err != nil {
		return err
	}
	if encoded, readErr := os.ReadFile(path); readErr == nil {
		var actual derivedPreparation
		if json.Unmarshal(encoded, &actual) != nil || !reflect.DeepEqual(actual, expected) {
			return errors.New("派生候选准备检查点身份冲突")
		}
		return nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	encoded, err := json.MarshalIndent(expected, "", "  ")
	if err != nil {
		return err
	}
	return publishNewFile(path, append(encoded, '\n'), 0o600)
}

func removeDerivedPreparation(root, generation string) error {
	path, err := derived.CandidateCheckpointPath(root, generation)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// CatchUpDerivedGeneration applies exactly the authority versions that changed
// since the last checkpoint. Repeating it with no changes performs no model or
// generation work and returns the existing record.
func CatchUpDerivedGeneration(ctx context.Context, authority contract.AssetAuthority, root string, plan DerivedPlan, builder DerivedBuilder, journal DerivedJournal) (DerivedRecord, error) {
	snapshot, assets, err := CaptureAuthoritySnapshot(authority)
	if err != nil {
		return DerivedRecord{}, err
	}
	return CatchUpDerivedGenerationAtSnapshot(ctx, root, plan, builder, journal, snapshot, assets)
}

// CatchUpDerivedGenerationAtSnapshot performs model and derived work only
// against a frozen authority snapshot. The formal command captures it under
// the unique asset lock, releases that lock, and then enters this function.
func CatchUpDerivedGenerationAtSnapshot(ctx context.Context, root string, plan DerivedPlan, builder DerivedBuilder, journal DerivedJournal, snapshot AuthoritySnapshot, assets []domain.Information) (DerivedRecord, error) {
	if err := validateSnapshotValues(snapshot, assets); err != nil {
		return DerivedRecord{}, err
	}
	record, exists, err := journal.Read()
	if err != nil || !exists {
		return DerivedRecord{}, errors.New("派生候选尚未建立")
	}
	if record.Plan != plan.Identity || builder == nil || builder.RuntimeIdentity() != plan.TargetRun ||
		(record.Phase != DerivedPhaseReady && record.Phase != DerivedPhaseObserving) {
		return DerivedRecord{}, errors.New("派生候选不能在当前阶段追平")
	}
	changes, err := changedVersions(record.CandidateSnapshot, snapshot)
	if err != nil {
		return DerivedRecord{}, err
	}
	currentState, inspectErr := derived.InspectGeneration(root, record.Candidate.Generation)
	if len(changes.Assets) == 0 && inspectErr == nil && currentState == record.Candidate &&
		record.CandidateSnapshot.Identity == snapshot.Identity && validateGenerationCoverage(root, currentState.Generation, snapshot, plan.TargetRun.VectorSpace) == nil {
		return record, nil
	}
	marker := derivedCatchUp{Schema: derivedCatchUpSchema, Plan: plan.Identity, Generation: record.Candidate.Generation, FromRevision: record.Revision, Snapshot: snapshot}
	markerPath, err := sealDerivedCatchUp(root, marker)
	if err != nil {
		return DerivedRecord{}, err
	}
	store, err := derived.OpenGeneration(root, record.Candidate.Generation)
	if err != nil {
		return DerivedRecord{}, err
	}
	if validateGenerationRecords(store, snapshot, plan.TargetRun.VectorSpace) != nil {
		if err := builder.CatchUp(ctx, store, assets, changes); err != nil {
			_ = store.Close()
			return DerivedRecord{}, err
		}
	}
	if err := validateGenerationRecords(store, snapshot, plan.TargetRun.VectorSpace); err != nil {
		_ = store.Close()
		return DerivedRecord{}, err
	}
	state, err := store.ResealGeneration(derived.GenerationMetadata{AssetCount: len(snapshot.Assets), AssetSnapshot: snapshot.Identity, EmbeddingSpace: plan.TargetRun.VectorSpace})
	if closeErr := store.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		return DerivedRecord{}, err
	}
	if _, err := derived.RebindActiveGeneration(root, record.Candidate, state); err != nil {
		return DerivedRecord{}, err
	}
	if err := validateGenerationCoverage(root, state.Generation, snapshot, plan.TargetRun.VectorSpace); err != nil {
		return DerivedRecord{}, err
	}
	record.Revision++
	record.Candidate = state
	record.CandidateSnapshot = snapshot
	record, err = journal.Append(record.Revision-1, record)
	if err == nil {
		_ = os.Remove(markerPath)
	}
	return record, err
}

func sealDerivedCatchUp(root string, expected derivedCatchUp) (string, error) {
	base, err := derived.CandidateCheckpointPath(root, expected.Generation)
	if err != nil {
		return "", err
	}
	path := fmt.Sprintf("%s.catch-up-%020d-%s.json", strings.TrimSuffix(base, ".json"), expected.FromRevision, expected.Snapshot.Identity)
	if encoded, readErr := os.ReadFile(path); readErr == nil {
		var actual derivedCatchUp
		if json.Unmarshal(encoded, &actual) != nil || !reflect.DeepEqual(actual, expected) {
			return "", errors.New("派生追平检查点身份冲突")
		}
		return path, nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return "", readErr
	}
	encoded, err := json.MarshalIndent(expected, "", "  ")
	if err != nil {
		return "", err
	}
	if err := publishNewFile(path, append(encoded, '\n'), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func PromoteDerived(authority contract.AssetAuthority, control contract.ControlAuthority, root string, plan DerivedPlan, journal DerivedJournal) (DerivedRecord, error) {
	latest, _, err := CaptureAuthoritySnapshot(authority)
	if err != nil {
		return DerivedRecord{}, err
	}
	return PromoteDerivedAtSnapshot(latest, control, root, plan, journal)
}

// PromoteDerivedAtSnapshot performs only the short final identity checks and
// two CAS operations. The formal entry calls it while holding the authority's
// unique asset lock; any snapshot gap therefore fails before this function or
// is impossible until both active decisions are durably switched.
func PromoteDerivedAtSnapshot(latest AuthoritySnapshot, control contract.ControlAuthority, root string, plan DerivedPlan, journal DerivedJournal) (DerivedRecord, error) {
	if control == nil || journal == nil {
		return DerivedRecord{}, errors.New("权威控制和派生生命周期记录不能为空")
	}
	if err := validateSnapshot(latest); err != nil {
		return DerivedRecord{}, err
	}
	if err := validateDerivedPlan(plan); err != nil {
		return DerivedRecord{}, err
	}
	record, exists, err := journal.Read()
	if err != nil || !exists || record.Plan != plan.Identity {
		return DerivedRecord{}, errors.New("派生候选没有匹配的耐久检查点")
	}
	if record.Phase == DerivedPhaseReady {
		candidateState, inspectErr := derived.InspectGeneration(root, record.Candidate.Generation)
		baselineState, baselineErr := derived.InspectGeneration(root, record.Baseline.Generation)
		if inspectErr != nil || baselineErr != nil || latest.Identity != record.CandidateSnapshot.Identity ||
			candidateState != record.Candidate || baselineState != record.Baseline {
			return DerivedRecord{}, errors.New("派生候选、回退世代或权威快照在晋升前发生漂移")
		}
		if err := validateGenerationCoverage(root, record.Candidate.Generation, latest, plan.TargetRun.VectorSpace); err != nil {
			return DerivedRecord{}, err
		}
		current := control.ReadControl()
		active, activeErr := derived.ActiveGeneration(root)
		if activeErr != nil || current.ActiveComposition != plan.Baseline.Identity ||
			current.ActiveKernelGeneration != plan.BaselineRun.Kernel || active.Generation != record.Baseline.Generation {
			return DerivedRecord{}, errors.New("活动状态已离开派生候选基线")
		}
		record.Revision++
		record.Phase = DerivedPhaseSwitching
		record, err = journal.Append(record.Revision-1, record)
		if err != nil {
			return DerivedRecord{}, err
		}
	}
	if record.Phase == DerivedPhaseObserving {
		return record, nil
	}
	if record.Phase != DerivedPhaseSwitching {
		return DerivedRecord{}, errors.New("派生候选不处于可晋升阶段")
	}
	candidateState, candidateErr := derived.InspectGeneration(root, record.Candidate.Generation)
	baselineState, baselineErr := derived.InspectGeneration(root, record.Baseline.Generation)
	if candidateErr != nil || baselineErr != nil || candidateState != record.Candidate || baselineState != record.Baseline ||
		candidateState.RecoveredCorruption || baselineState.RecoveredCorruption {
		return DerivedRecord{}, errors.New("派生候选或回退世代在切换期间发生漂移")
	}
	if err := reconcileSwitch(control, root, plan.Baseline.Identity, plan.BaselineRun.Kernel, record.Baseline.Generation,
		plan.Target.Identity, plan.TargetRun.Kernel, record.Candidate.Generation); err != nil {
		return DerivedRecord{}, err
	}
	record.Revision++
	record.Phase = DerivedPhaseObserving
	return journal.Append(record.Revision-1, record)
}

func CompleteDerivedObservation(ctx context.Context, authority contract.AssetAuthority, control contract.ControlAuthority, root string, plan DerivedPlan, baselineBuilder, candidateBuilder DerivedBuilder, journal DerivedJournal, observation DerivedObservation) (DerivedRecord, error) {
	latest, assets, err := CaptureAuthoritySnapshot(authority)
	if err != nil {
		return DerivedRecord{}, err
	}
	if observation.Passed {
		if candidateBuilder == nil || candidateBuilder.RuntimeIdentity() != plan.TargetRun {
			return DerivedRecord{}, errors.New("候选运行时身份漂移")
		}
		if _, err := SealObservedGenerationAtSnapshot(root, plan, journal, latest); err != nil {
			return DerivedRecord{}, err
		}
	} else {
		if baselineBuilder == nil || baselineBuilder.RuntimeIdentity() != plan.BaselineRun {
			return DerivedRecord{}, errors.New("上一实现运行时身份漂移，不能安全回退")
		}
		if _, err := CatchUpRollbackGenerationAtSnapshot(ctx, root, plan, baselineBuilder, journal, latest, assets, observation); err != nil {
			return DerivedRecord{}, err
		}
	}
	return CompleteDerivedObservationAtSnapshot(latest, control, root, plan, journal, observation)
}

// SealObservedGenerationAtSnapshot binds an active generation's complete log
// tail to the observation snapshot. It performs no model work and is safe to
// run while the authority write lock closes the final version gap.
func SealObservedGenerationAtSnapshot(root string, plan DerivedPlan, journal DerivedJournal, snapshot AuthoritySnapshot) (DerivedRecord, error) {
	if err := validateSnapshot(snapshot); err != nil {
		return DerivedRecord{}, err
	}
	record, exists, err := journal.Read()
	if err != nil || !exists || record.Plan != plan.Identity {
		return DerivedRecord{}, errors.New("派生候选没有匹配的观察检查点")
	}
	if record.Phase == DerivedPhaseAccepted && record.CandidateSnapshot.Identity == snapshot.Identity {
		return record, nil
	}
	if record.Phase != DerivedPhaseObserving {
		return DerivedRecord{}, errors.New("派生候选不处于可封存观察阶段")
	}
	state, inspectErr := derived.InspectGeneration(root, record.Candidate.Generation)
	if inspectErr == nil && state == record.Candidate && record.CandidateSnapshot.Identity == snapshot.Identity &&
		validateGenerationCoverage(root, state.Generation, snapshot, plan.TargetRun.VectorSpace) == nil {
		return record, nil
	}
	marker := derivedCatchUp{Schema: derivedCatchUpSchema, Plan: plan.Identity, Generation: record.Candidate.Generation, FromRevision: record.Revision, Snapshot: snapshot}
	markerPath, err := sealDerivedCatchUp(root, marker)
	if err != nil {
		return DerivedRecord{}, err
	}
	store, err := derived.OpenGeneration(root, record.Candidate.Generation)
	if err != nil {
		return DerivedRecord{}, err
	}
	if err := validateGenerationRecords(store, snapshot, plan.TargetRun.VectorSpace); err != nil {
		_ = store.Close()
		return DerivedRecord{}, fmt.Errorf("观察期间活动派生世代未追平: %w", err)
	}
	state, err = store.ResealGeneration(derived.GenerationMetadata{AssetCount: len(snapshot.Assets), AssetSnapshot: snapshot.Identity, EmbeddingSpace: plan.TargetRun.VectorSpace})
	if closeErr := store.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		return DerivedRecord{}, err
	}
	if rebound, err := derived.RebindActiveGeneration(root, record.Candidate, state); err != nil {
		return DerivedRecord{}, err
	} else if !rebound {
		return DerivedRecord{}, errors.New("观察世代不是当前活动派生状态")
	}
	if err := validateGenerationCoverage(root, state.Generation, snapshot, plan.TargetRun.VectorSpace); err != nil {
		return DerivedRecord{}, err
	}
	record.Revision++
	record.Candidate = state
	record.CandidateSnapshot = snapshot
	record, err = journal.Append(record.Revision-1, record)
	if err == nil {
		_ = os.Remove(markerPath)
	}
	return record, err
}

// CatchUpRollbackGenerationAtSnapshot brings the retained baseline generation
// to one frozen snapshot without switching either active decision. The long
// model work therefore happens outside the final authority write barrier.
func CatchUpRollbackGenerationAtSnapshot(ctx context.Context, root string, plan DerivedPlan, baselineBuilder DerivedBuilder, journal DerivedJournal, snapshot AuthoritySnapshot, assets []domain.Information, observation DerivedObservation) (DerivedRecord, error) {
	if observation.Schema != DerivedObservationSchema || observation.Plan != plan.Identity ||
		observation.ActiveComposition != plan.Target.Identity || !isSHA256(observation.ReportSHA256) {
		return DerivedRecord{}, errors.New("派生能力观察证据与候选计划不一致")
	}
	if observation.Passed {
		return DerivedRecord{}, errors.New("通过观察不需要追平回退世代")
	}
	if err := validateSnapshotValues(snapshot, assets); err != nil {
		return DerivedRecord{}, err
	}
	if baselineBuilder == nil || baselineBuilder.RuntimeIdentity() != plan.BaselineRun {
		return DerivedRecord{}, errors.New("上一实现运行时身份漂移，不能安全回退")
	}
	record, exists, err := journal.Read()
	if err != nil || !exists || record.Plan != plan.Identity {
		return DerivedRecord{}, errors.New("派生候选没有匹配的观察检查点")
	}
	if record.Phase == DerivedPhaseRolledBack {
		if record.ObservationSHA256 == observation.ReportSHA256 {
			return record, nil
		}
		return DerivedRecord{}, errors.New("已回退观察与当前结论不一致")
	}
	if record.Phase == DerivedPhaseRollbackReady {
		if record.ObservationSHA256 != observation.ReportSHA256 {
			return DerivedRecord{}, errors.New("已封存回退决定与当前观察不一致")
		}
		if record.BaselineSnapshot.Identity == snapshot.Identity &&
			validateGenerationCoverage(root, record.Baseline.Generation, snapshot, plan.BaselineRun.VectorSpace) == nil {
			return record, nil
		}
	}
	if record.Phase != DerivedPhaseObserving && record.Phase != DerivedPhaseRollbackReady {
		return DerivedRecord{}, errors.New("派生候选不处于观察阶段")
	}
	changes, err := changedVersions(record.BaselineSnapshot, snapshot)
	if err != nil {
		return DerivedRecord{}, err
	}
	marker := derivedCatchUp{Schema: derivedCatchUpSchema, Plan: plan.Identity, Generation: record.Baseline.Generation, FromRevision: record.Revision, Snapshot: snapshot}
	markerPath, err := sealDerivedCatchUp(root, marker)
	if err != nil {
		return DerivedRecord{}, err
	}
	store, err := derived.OpenGeneration(root, record.Baseline.Generation)
	if err != nil {
		return DerivedRecord{}, err
	}
	if validateGenerationRecords(store, snapshot, plan.BaselineRun.VectorSpace) != nil {
		if err := baselineBuilder.CatchUp(ctx, store, assets, changes); err != nil {
			_ = store.Close()
			return DerivedRecord{}, err
		}
	}
	if err := validateGenerationRecords(store, snapshot, plan.BaselineRun.VectorSpace); err != nil {
		_ = store.Close()
		return DerivedRecord{}, fmt.Errorf("上一派生世代未追平，不能回退: %w", err)
	}
	state, err := store.ResealGeneration(derived.GenerationMetadata{AssetCount: len(snapshot.Assets), AssetSnapshot: snapshot.Identity, EmbeddingSpace: plan.BaselineRun.VectorSpace})
	if closeErr := store.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		return DerivedRecord{}, err
	}
	if err := validateGenerationCoverage(root, state.Generation, snapshot, plan.BaselineRun.VectorSpace); err != nil {
		return DerivedRecord{}, err
	}
	record.Revision++
	record.Phase = DerivedPhaseRollbackReady
	record.Baseline = state
	record.BaselineSnapshot = snapshot
	record.ObservationSHA256 = observation.ReportSHA256
	record, err = journal.Append(record.Revision-1, record)
	if err == nil {
		_ = os.Remove(markerPath)
	}
	return record, err
}

// CompleteDerivedObservationAtSnapshot performs only the final checks and
// active pointer/control switch while the authority's unique write lock is
// held by the formal entry. All long catch-up work is completed beforehand.
func CompleteDerivedObservationAtSnapshot(latest AuthoritySnapshot, control contract.ControlAuthority, root string, plan DerivedPlan, journal DerivedJournal, observation DerivedObservation) (DerivedRecord, error) {
	if observation.Schema != DerivedObservationSchema || observation.Plan != plan.Identity ||
		observation.ActiveComposition != plan.Target.Identity || !isSHA256(observation.ReportSHA256) {
		return DerivedRecord{}, errors.New("派生能力观察证据与候选计划不一致")
	}
	if err := validateSnapshot(latest); err != nil {
		return DerivedRecord{}, err
	}
	record, exists, err := journal.Read()
	if err != nil || !exists || record.Plan != plan.Identity {
		return DerivedRecord{}, errors.New("派生候选没有匹配的观察检查点")
	}
	if record.Phase == DerivedPhaseAccepted {
		if observation.Passed && record.ObservationSHA256 == observation.ReportSHA256 {
			if err := derived.RetireGeneration(root, record.Baseline.Generation); err != nil && !errors.Is(err, os.ErrNotExist) {
				return DerivedRecord{}, err
			}
			return record, nil
		}
		return DerivedRecord{}, errors.New("已接受观察与当前结论不一致")
	}
	if record.Phase == DerivedPhaseRolledBack {
		if !observation.Passed && record.ObservationSHA256 == observation.ReportSHA256 {
			if err := derived.RetireGeneration(root, record.Candidate.Generation); err != nil && !errors.Is(err, os.ErrNotExist) {
				return DerivedRecord{}, err
			}
			return record, nil
		}
		return DerivedRecord{}, errors.New("已回退观察与当前结论不一致")
	}
	if observation.Passed {
		if record.Phase != DerivedPhaseObserving || record.CandidateSnapshot.Identity != latest.Identity {
			return DerivedRecord{}, errors.New("观察通过时派生世代尚未封存到切换点")
		}
		if err := validateGenerationCoverage(root, record.Candidate.Generation, latest, plan.TargetRun.VectorSpace); err != nil {
			return DerivedRecord{}, err
		}
		state, inspectErr := derived.InspectGeneration(root, record.Candidate.Generation)
		if inspectErr != nil || state != record.Candidate || state.RecoveredCorruption {
			return DerivedRecord{}, errors.New("观察通过时活动派生世代完整性无效")
		}
		if err := verifyActive(control.ReadControl(), root, plan.Target.Identity, plan.TargetRun.Kernel, state.Generation); err != nil {
			return DerivedRecord{}, err
		}
		record.Revision++
		record.Phase = DerivedPhaseAccepted
		record.ObservationSHA256 = observation.ReportSHA256
		record, err = journal.Append(record.Revision-1, record)
		if err != nil {
			return DerivedRecord{}, err
		}
		if err := derived.RetireGeneration(root, record.Baseline.Generation); err != nil {
			return DerivedRecord{}, err
		}
		return record, nil
	}
	if record.Phase != DerivedPhaseRollbackReady || record.ObservationSHA256 != observation.ReportSHA256 ||
		record.BaselineSnapshot.Identity != latest.Identity {
		return DerivedRecord{}, errors.New("回退世代尚未封存到切换点")
	}
	if err := validateGenerationCoverage(root, record.Baseline.Generation, latest, plan.BaselineRun.VectorSpace); err != nil {
		return DerivedRecord{}, err
	}
	state, inspectErr := derived.InspectGeneration(root, record.Baseline.Generation)
	if inspectErr != nil || state != record.Baseline || state.RecoveredCorruption {
		return DerivedRecord{}, errors.New("回退世代在切换前发生漂移")
	}
	if err := reconcileSwitch(control, root, plan.Target.Identity, plan.TargetRun.Kernel, record.Candidate.Generation,
		plan.Baseline.Identity, plan.BaselineRun.Kernel, state.Generation); err != nil {
		return DerivedRecord{}, err
	}
	record.Revision++
	record.Phase = DerivedPhaseRolledBack
	record, err = journal.Append(record.Revision-1, record)
	if err != nil {
		return DerivedRecord{}, err
	}
	if err := derived.RetireGeneration(root, record.Candidate.Generation); err != nil {
		return DerivedRecord{}, err
	}
	return record, nil
}

func reconcileSwitch(control contract.ControlAuthority, root, fromComposition, fromKernel, fromGeneration, toComposition, toKernel, toGeneration string) error {
	state := control.ReadControl()
	active, err := derived.ActiveGeneration(root)
	if err != nil {
		return err
	}
	if active.Generation == fromGeneration {
		if state.ActiveComposition != fromComposition || state.ActiveKernelGeneration != fromKernel {
			return errors.New("派生指针与权威控制不在同一切换基线")
		}
		if _, err := derived.SwitchGeneration(root, fromGeneration, toGeneration); err != nil {
			return err
		}
		active.Generation = toGeneration
	}
	if active.Generation != toGeneration {
		return errors.New("派生指针与候选切换检查点不一致")
	}
	state = control.ReadControl()
	if state.ActiveComposition == fromComposition && state.ActiveKernelGeneration == fromKernel {
		next := contract.ControlState{Schema: contract.ControlStateSchema, Revision: state.Revision + 1, ActiveComposition: toComposition, ActiveKernelGeneration: toKernel}
		if _, err := control.CompareAndSwapControl(state.Revision, next); err != nil {
			latest := control.ReadControl()
			if latest.ActiveComposition == toComposition && latest.ActiveKernelGeneration == toKernel {
				return nil
			}
			_, _ = derived.SwitchGeneration(root, toGeneration, fromGeneration)
			return err
		}
		return nil
	}
	if state.ActiveComposition != toComposition || state.ActiveKernelGeneration != toKernel {
		_, _ = derived.SwitchGeneration(root, toGeneration, fromGeneration)
		return errors.New("权威控制与候选切换检查点不一致")
	}
	return nil
}

func verifyActive(state contract.ControlState, root, compositionID, kernelID, generation string) error {
	active, err := derived.ActiveGeneration(root)
	if err != nil {
		return err
	}
	if state.ActiveComposition != compositionID || state.ActiveKernelGeneration != kernelID || active.Generation != generation {
		return errors.New("活动组合、内核与派生世代不一致")
	}
	return nil
}

func validateGenerationCoverage(root, generation string, snapshot AuthoritySnapshot, vectorSpace string) error {
	if err := validateSnapshot(snapshot); err != nil {
		return err
	}
	state, err := derived.InspectGeneration(root, generation)
	if err != nil {
		return err
	}
	if state.AssetCount != len(snapshot.Assets) || state.RecordCount != len(snapshot.Assets) ||
		state.AssetSnapshot != snapshot.Identity || state.EmbeddingSpace != vectorSpace ||
		state.SealedBytes != state.LogBytes || state.RecoveredCorruption {
		return errors.New("派生世代耐久身份未完整绑定资产快照与日志")
	}
	store, err := derived.OpenGeneration(root, generation)
	if err != nil {
		return err
	}
	records, err := store.AllWithEmbeddings()
	closeErr := store.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return validateGenerationRecordValues(records, snapshot, vectorSpace)
}

func validateGenerationRecords(store *derived.Store, snapshot AuthoritySnapshot, vectorSpace string) error {
	if store == nil {
		return errors.New("派生世代不能为空")
	}
	records, err := store.AllWithEmbeddings()
	if err != nil {
		return err
	}
	if store.RecoveredCorruption() {
		return errors.New("派生世代曾从损坏尾部恢复，必须重建而不能晋升")
	}
	return validateGenerationRecordValues(records, snapshot, vectorSpace)
}

func validateGenerationRecordValues(records []derived.Record, snapshot AuthoritySnapshot, vectorSpace string) error {
	if len(records) != len(snapshot.Assets) {
		return fmt.Errorf("派生记录数量 %d 与资产数量 %d 不一致", len(records), len(snapshot.Assets))
	}
	versions := make(map[string]uint64, len(snapshot.Assets))
	for _, value := range snapshot.Assets {
		versions[value.ID] = value.Revision
	}
	for _, record := range records {
		if versions[record.AssetID] != record.AssetRevision {
			return fmt.Errorf("派生记录未覆盖资产版本: %s@%d", record.AssetID, record.AssetRevision)
		}
		if len(record.Embedding) > 0 && record.EmbeddingSpace != vectorSpace {
			return errors.New("派生记录向量空间与候选能力不一致")
		}
		delete(versions, record.AssetID)
	}
	if len(versions) != 0 {
		return errors.New("派生世代缺少资产记录")
	}
	return nil
}

func validateSnapshotValues(snapshot AuthoritySnapshot, values []domain.Information) error {
	if err := validateSnapshot(snapshot); err != nil {
		return err
	}
	actual := append([]domain.Information(nil), values...)
	sort.Slice(actual, func(left, right int) bool { return actual[left].ID < actual[right].ID })
	if len(actual) != len(snapshot.Assets) {
		return errors.New("权威资产正文与版本快照数量不一致")
	}
	for index, version := range snapshot.Assets {
		if actual[index].ID != version.ID || actual[index].Revision != version.Revision ||
			actual[index].ID == "" || actual[index].Revision == 0 || (index > 0 && actual[index-1].ID == actual[index].ID) {
			return errors.New("权威资产正文与版本快照身份不一致")
		}
	}
	return nil
}

func changedVersions(from, to AuthoritySnapshot) (contract.ChangeScope, error) {
	if err := validateSnapshot(from); err != nil {
		return contract.ChangeScope{}, err
	}
	if err := validateSnapshot(to); err != nil {
		return contract.ChangeScope{}, err
	}
	previous := make(map[string]uint64, len(from.Assets))
	for _, value := range from.Assets {
		previous[value.ID] = value.Revision
	}
	result := contract.ChangeScope{Schema: contract.AssetChangeScopeSchema}
	for _, value := range to.Assets {
		if old, exists := previous[value.ID]; exists && value.Revision < old {
			return contract.ChangeScope{}, errors.New("权威资产版本发生回退")
		} else if !exists || value.Revision != old {
			result.Assets = append(result.Assets, value)
		}
		delete(previous, value.ID)
	}
	if len(previous) != 0 {
		return contract.ChangeScope{}, errors.New("权威资产从版本快照中消失")
	}
	return result, nil
}

func validateSnapshot(snapshot AuthoritySnapshot) error {
	if snapshot.Schema != AuthoritySnapshotSchema || !isSHA256(snapshot.Identity) {
		return errors.New("权威资产版本快照格式无效")
	}
	expected, err := snapshotDigest(snapshot)
	if err != nil || expected != snapshot.Identity {
		return errors.New("权威资产版本快照身份漂移")
	}
	for index, value := range snapshot.Assets {
		if value.ID == "" || value.Revision == 0 || (index > 0 && snapshot.Assets[index-1].ID >= value.ID) {
			return errors.New("权威资产版本快照未规范化")
		}
	}
	return nil
}

func snapshotDigest(snapshot AuthoritySnapshot) (string, error) {
	payload := struct {
		Schema string                  `json:"schema"`
		Assets []contract.AssetVersion `json:"assets"`
	}{snapshot.Schema, snapshot.Assets}
	return digestJSON(payload)
}

func runtimeIdentity(manifest composition.Manifest) (DerivedRuntimeIdentity, error) {
	if _, err := composition.VerifySealed(manifest); err != nil {
		return DerivedRuntimeIdentity{}, err
	}
	result := DerivedRuntimeIdentity{Composition: manifest.Identity}
	var vectorConfig map[string]any
	for _, component := range manifest.Components {
		switch component.Role {
		case "kernel":
			result.Kernel = component.Identity
		case "semantic":
			result.Semantic = component.Identity
		case "vector":
			result.Vector = component.Identity
			vectorConfig = component.Config
		}
	}
	result.VectorCapability, _ = vectorConfig["capability"].(string)
	result.VectorSpace, _ = vectorConfig["space"].(string)
	switch value := vectorConfig["dimensions"].(type) {
	case json.Number:
		result.VectorDimensions, _ = stringsToInt(value.String())
	case float64:
		result.VectorDimensions = int(value)
	case int:
		result.VectorDimensions = value
	}
	if !isSHA256(result.Composition) || !isSHA256(result.Kernel) || !isSHA256(result.Semantic) || !isSHA256(result.Vector) ||
		strings.TrimSpace(result.VectorCapability) == "" || strings.TrimSpace(result.VectorSpace) == "" || result.VectorDimensions <= 0 {
		return DerivedRuntimeIdentity{}, errors.New("组合缺少完整派生运行时身份")
	}
	return result, nil
}

func stringsToInt(value string) (int, error) {
	var result int
	_, err := fmt.Sscan(value, &result)
	return result, err
}

func validateDerivedPlan(plan DerivedPlan) error {
	if plan.Schema != DerivedPlanSchema || plan.Role != plan.Replacement.Role || StateImpact(plan.Role) != ImpactDerived {
		return errors.New("派生候选计划边界无效")
	}
	if _, err := composition.VerifySealed(plan.Baseline); err != nil {
		return err
	}
	rebuilt, err := replaceComponent(plan.Baseline, plan.Replacement)
	if err != nil || rebuilt.Identity != plan.Target.Identity {
		return errors.New("派生候选目标组合身份漂移")
	}
	baselineRun, err := runtimeIdentity(plan.Baseline)
	if err != nil || baselineRun != plan.BaselineRun {
		return errors.New("派生候选基线运行时身份漂移")
	}
	targetRun, err := runtimeIdentity(plan.Target)
	if err != nil || targetRun != plan.TargetRun {
		return errors.New("派生候选目标运行时身份漂移")
	}
	validation := plan.Validation
	if validation.Schema != DerivedValidationSchema || !validation.Passed || validation.StateImpact != ImpactDerived ||
		validation.CandidateComponent != plan.Replacement.Identity || validation.BaselineComposition != plan.Baseline.Identity ||
		validation.TargetComposition != plan.Target.Identity || !isSHA256(validation.IntegrationSHA256) {
		return errors.New("派生候选契约、依赖或集成验证未成立")
	}
	expected, err := derivedPlanDigest(plan)
	if err != nil || expected != plan.Identity {
		return errors.New("派生候选计划身份漂移")
	}
	return nil
}

func derivedPlanDigest(plan DerivedPlan) (string, error) {
	payload := struct {
		Schema              string                 `json:"schema"`
		Role                string                 `json:"role"`
		BaselineComposition string                 `json:"baseline_composition"`
		CandidateComponent  string                 `json:"candidate_component"`
		TargetComposition   string                 `json:"target_composition"`
		BaselineRuntime     DerivedRuntimeIdentity `json:"baseline_runtime"`
		TargetRuntime       DerivedRuntimeIdentity `json:"target_runtime"`
		ValidationSHA256    string                 `json:"validation_sha256"`
	}{DerivedPlanSchema, plan.Role, plan.Baseline.Identity, plan.Replacement.Identity, plan.Target.Identity, plan.BaselineRun, plan.TargetRun, plan.Validation.IntegrationSHA256}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
