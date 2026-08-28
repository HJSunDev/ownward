// Package capabilitylifecycle contains the migration lifecycle that runs only
// at an explicit offline authority boundary. It is not part of the normal
// request path and does not load, proxy or hot-swap implementations.
package capabilitylifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/HJSunDev/ownward/internal/composition"
	"github.com/HJSunDev/ownward/internal/contract"
)

const (
	ValidationSchema  = "ownward.stateless-capability-validation/v1"
	PlanSchema        = "ownward.stateless-capability-plan/v1"
	ObservationSchema = "ownward.stateless-capability-observation/v1"

	ImpactNone      = "none"
	ImpactDerived   = "derived-state"
	ImpactAuthority = "authority-state"
)

// Validation binds a candidate's integration evidence to the exact baseline
// and resulting composition. The report remains in the validation system;
// only its immutable digest participates here.
type Validation struct {
	Schema              string `json:"schema"`
	CandidateComponent  string `json:"candidate_component"`
	BaselineComposition string `json:"baseline_composition"`
	TargetComposition   string `json:"target_composition"`
	StateImpact         string `json:"state_impact"`
	IntegrationSHA256   string `json:"integration_sha256"`
	Passed              bool   `json:"passed"`
}

// Plan is a deterministic, reconstructable stateless replacement decision.
// Baseline and target are sealed manifests; Prepare and every mutating action
// revalidate them, so callers cannot promote a forged or drifted plan.
type Plan struct {
	Schema      string                `json:"schema"`
	Identity    string                `json:"identity"`
	Role        string                `json:"role"`
	Baseline    composition.Manifest  `json:"baseline"`
	Replacement composition.Component `json:"replacement"`
	Target      composition.Manifest  `json:"target"`
	Validation  Validation            `json:"validation"`
}

// Observation is the immutable post-selection result used to close an open
// transition. A bare boolean cannot finalize authority control: the decision
// must bind the exact plan, active composition and external observation report.
type Observation struct {
	Schema            string `json:"schema"`
	Plan              string `json:"plan"`
	ActiveComposition string `json:"active_composition"`
	ReportSHA256      string `json:"report_sha256"`
	Passed            bool   `json:"passed"`
}

// StateImpact classifies replacement by the state that would become invalid,
// not by whether the implementation itself happens to persist data.
func StateImpact(role string) string {
	switch strings.TrimSpace(role) {
	case "access":
		return ImpactNone
	case "authority-substrate":
		return ImpactAuthority
	case "semantic", "vector", "kernel", "product-rules":
		return ImpactDerived
	default:
		return "unsupported"
	}
}

// PrepareStateless validates one independently sealed access replacement and
// constructs the exact target composition without consulting or mutating
// authority, derived or runtime state.
func PrepareStateless(current composition.Manifest, replacement composition.Component, validation Validation) (Plan, error) {
	if StateImpact(replacement.Role) != ImpactNone {
		return Plan{}, fmt.Errorf("组件 %s 会使现有状态失效，不能使用无状态生命周期", replacement.Role)
	}
	target, err := replaceComponent(current, replacement)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{
		Schema: PlanSchema, Role: replacement.Role, Baseline: current,
		Replacement: replacement, Target: target, Validation: validation,
	}
	plan.Identity, err = planDigest(plan)
	if err != nil {
		return Plan{}, err
	}
	if err := validatePlan(plan); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

// ActivateForNextStart performs the sole active-selection mutation. The
// caller must own the authority substrate at an offline safe boundary. The
// selected candidate becomes the only active composition; the previous
// identities remain solely as a rollback target while observation is open.
func ActivateForNextStart(control contract.ControlAuthority, journal Journal, plan Plan) (contract.ControlState, error) {
	if control == nil || journal == nil {
		return contract.ControlState{}, errors.New("权威控制和候选生命周期记录不能为空")
	}
	if err := validatePlan(plan); err != nil {
		return contract.ControlState{}, err
	}
	record, exists, err := journal.Read()
	if err != nil {
		return contract.ControlState{}, err
	}
	if exists && record.Plan == plan.Identity {
		current := control.ReadControl()
		switch record.Phase {
		case PhaseAccepted:
			if current.ActiveComposition == plan.Target.Identity {
				return current, nil
			}
			return contract.ControlState{}, errors.New("已接受候选与权威控制状态不一致")
		case PhaseRolledBack:
			if current.ActiveComposition == plan.Baseline.Identity {
				return current, nil
			}
			return contract.ControlState{}, errors.New("已回退候选与权威控制状态不一致")
		case PhaseObserving:
			if current.ActiveComposition == plan.Target.Identity {
				return current, nil
			}
			return contract.ControlState{}, errors.New("观察中的候选与权威控制状态不一致")
		}
	} else if exists && record.Phase != PhaseAccepted && record.Phase != PhaseRolledBack {
		return contract.ControlState{}, errors.New("已有其他能力切换正在观察")
	}
	baselineKernel, err := componentIdentity(plan.Baseline, "kernel")
	if err != nil {
		return contract.ControlState{}, err
	}
	targetKernel, err := componentIdentity(plan.Target, "kernel")
	if err != nil {
		return contract.ControlState{}, err
	}
	current := control.ReadControl()
	resumingPrepared := exists && record.Plan == plan.Identity && record.Phase == PhasePrepared && current.ActiveComposition == plan.Target.Identity
	if targetKernel != baselineKernel || current.ActiveKernelGeneration != baselineKernel ||
		(current.ActiveComposition != plan.Baseline.Identity && !resumingPrepared) {
		return contract.ControlState{}, errors.New("权威控制状态已离开候选验证基线")
	}
	if !exists || record.Plan != plan.Identity {
		previousRevision := record.Revision
		record = recordForPlan(plan, PhasePrepared, "")
		record.Revision = previousRevision + 1
		expected := record.Revision - 1
		record, err = journal.Append(expected, record)
		if err != nil {
			return contract.ControlState{}, err
		}
	}
	current = control.ReadControl()
	if current.ActiveComposition == plan.Baseline.Identity {
		next := contract.ControlState{
			Schema: contract.ControlStateSchema, Revision: current.Revision + 1,
			ActiveComposition: plan.Target.Identity, ActiveKernelGeneration: targetKernel,
		}
		current, err = control.CompareAndSwapControl(current.Revision, next)
		if err != nil {
			return contract.ControlState{}, err
		}
	} else if current.ActiveComposition != plan.Target.Identity || current.ActiveKernelGeneration != targetKernel {
		return contract.ControlState{}, errors.New("权威控制状态与候选切换记录不一致")
	}
	if record.Phase == PhasePrepared {
		nextRecord := recordForPlan(plan, PhaseObserving, "")
		nextRecord.Revision = record.Revision + 1
		if _, err := journal.Append(record.Revision, nextRecord); err != nil {
			return contract.ControlState{}, err
		}
	}
	return current, nil
}

// CompleteObservation closes the recoverable observation. A passing result
// retains the candidate; a failure atomically restores the previous selection.
// Repeating either completed decision is idempotent and does not add revisions.
func CompleteObservation(control contract.ControlAuthority, journal Journal, plan Plan, observation Observation) (contract.ControlState, error) {
	if control == nil || journal == nil {
		return contract.ControlState{}, errors.New("权威控制和候选生命周期记录不能为空")
	}
	if err := validatePlan(plan); err != nil {
		return contract.ControlState{}, err
	}
	if observation.Schema != ObservationSchema || observation.Plan != plan.Identity ||
		observation.ActiveComposition != plan.Target.Identity || !isSHA256(observation.ReportSHA256) {
		return contract.ControlState{}, errors.New("能力观察证据与候选计划不匹配")
	}
	record, exists, err := journal.Read()
	if err != nil {
		return contract.ControlState{}, err
	}
	if !exists || record.Plan != plan.Identity {
		return contract.ControlState{}, errors.New("候选生命周期没有匹配的观察记录")
	}
	current := control.ReadControl()
	if record.Phase == PhaseAccepted {
		if observation.Passed && record.ObservationSHA256 == observation.ReportSHA256 && current.ActiveComposition == plan.Target.Identity {
			return current, nil
		}
		return contract.ControlState{}, errors.New("已完成的候选观察结论不一致")
	}
	if record.Phase == PhaseRolledBack {
		if !observation.Passed && record.ObservationSHA256 == observation.ReportSHA256 && current.ActiveComposition == plan.Baseline.Identity {
			return current, nil
		}
		return contract.ControlState{}, errors.New("已完成的候选回退结论不一致")
	}
	if record.Phase == PhasePrepared {
		if current.ActiveComposition != plan.Target.Identity {
			return contract.ControlState{}, errors.New("候选尚未完成权威控制切换")
		}
		nextRecord := recordForPlan(plan, PhaseObserving, "")
		nextRecord.Revision = record.Revision + 1
		record, err = journal.Append(record.Revision, nextRecord)
		if err != nil {
			return contract.ControlState{}, err
		}
	}
	if record.Phase != PhaseObserving {
		return contract.ControlState{}, errors.New("候选不处于观察阶段")
	}
	if observation.Passed {
		if current.ActiveComposition != plan.Target.Identity {
			return contract.ControlState{}, errors.New("观察通过但候选不是活动组合")
		}
	} else if current.ActiveComposition == plan.Target.Identity {
		next := contract.ControlState{
			Schema: contract.ControlStateSchema, Revision: current.Revision + 1,
			ActiveComposition: plan.Baseline.Identity, ActiveKernelGeneration: current.ActiveKernelGeneration,
		}
		current, err = control.CompareAndSwapControl(current.Revision, next)
		if err != nil {
			return contract.ControlState{}, err
		}
	} else if current.ActiveComposition != plan.Baseline.Identity {
		return contract.ControlState{}, errors.New("观察回退时权威控制状态已漂移")
	}
	phase := PhaseRolledBack
	if observation.Passed {
		phase = PhaseAccepted
	}
	nextRecord := recordForPlan(plan, phase, observation.ReportSHA256)
	nextRecord.Revision = record.Revision + 1
	if _, err := journal.Append(record.Revision, nextRecord); err != nil {
		return contract.ControlState{}, err
	}
	return current, nil
}

func validatePlan(plan Plan) error {
	if plan.Schema != PlanSchema || plan.Role != "access" || plan.Replacement.Role != plan.Role || StateImpact(plan.Role) != ImpactNone {
		return errors.New("无状态候选计划边界无效")
	}
	if _, err := composition.VerifySealed(plan.Baseline); err != nil {
		return fmt.Errorf("候选基线组合无效: %w", err)
	}
	rebuilt, err := replaceComponent(plan.Baseline, plan.Replacement)
	if err != nil {
		return err
	}
	if _, err := composition.VerifySealed(plan.Target); err != nil {
		return fmt.Errorf("候选目标组合无效: %w", err)
	}
	if rebuilt.Identity != plan.Target.Identity {
		return errors.New("候选目标组合身份漂移")
	}
	if plan.Target.Identity == plan.Baseline.Identity {
		return errors.New("候选没有形成可观察的实现变化")
	}
	validation := plan.Validation
	if validation.Schema != ValidationSchema || !validation.Passed || validation.StateImpact != ImpactNone ||
		validation.CandidateComponent != plan.Replacement.Identity || validation.BaselineComposition != plan.Baseline.Identity ||
		validation.TargetComposition != plan.Target.Identity || !isSHA256(validation.IntegrationSHA256) {
		return errors.New("候选契约、集成基线或状态影响验证未成立")
	}
	expected, err := planDigest(plan)
	if err != nil || expected != plan.Identity {
		return errors.New("无状态候选计划身份漂移")
	}
	return nil
}

func recordForPlan(plan Plan, phase, observation string) JournalRecord {
	baselineKernel, _ := componentIdentity(plan.Baseline, "kernel")
	return JournalRecord{
		Schema: JournalRecordSchema, Plan: plan.Identity, Role: plan.Role,
		CandidateComponent: plan.Replacement.Identity, BaselineComposition: plan.Baseline.Identity,
		TargetComposition: plan.Target.Identity, BaselineKernelGeneration: baselineKernel,
		ValidationSHA256: plan.Validation.IntegrationSHA256, Phase: phase, ObservationSHA256: observation,
	}
}

func planDigest(plan Plan) (string, error) {
	payload := struct {
		Schema              string `json:"schema"`
		Role                string `json:"role"`
		BaselineComposition string `json:"baseline_composition"`
		CandidateComponent  string `json:"candidate_component"`
		TargetComposition   string `json:"target_composition"`
		ValidationSHA256    string `json:"validation_sha256"`
	}{PlanSchema, plan.Role, plan.Baseline.Identity, plan.Replacement.Identity, plan.Target.Identity, plan.Validation.IntegrationSHA256}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func componentIdentity(manifest composition.Manifest, role string) (string, error) {
	for _, component := range manifest.Components {
		if component.Role == role {
			return component.Identity, nil
		}
	}
	return "", fmt.Errorf("组合缺少组件角色: %s", role)
}

func isSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}
