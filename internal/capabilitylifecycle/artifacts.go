package capabilitylifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/HJSunDev/ownward/internal/composition"
	"github.com/HJSunDev/ownward/internal/contract"
)

const (
	CandidateArtifactSchema   = "ownward.stateless-candidate-artifact/v1"
	CandidateInspectionSchema = "ownward.stateless-candidate-inspection/v1"
	IntegrationReportSchema   = "ownward.stateless-candidate-integration/v1"
	ObservationReportSchema   = "ownward.stateless-candidate-observation-report/v1"
	PlanEnvelopeSchema        = "ownward.stateless-capability-plan-envelope/v1"
	LifecycleStatusSchema     = "ownward.stateless-capability-status/v1"
)

type CandidateArtifact struct {
	Schema  string             `json:"schema"`
	Role    string             `json:"role"`
	Content []CandidateContent `json:"content"`
}

type CandidateInspection struct {
	Schema                  string                   `json:"schema"`
	Identity                string                   `json:"identity"`
	CandidateArtifactSHA256 string                   `json:"candidate_artifact_sha256"`
	Role                    string                   `json:"role"`
	StateImpact             string                   `json:"state_impact"`
	BaselineComposition     string                   `json:"baseline_composition"`
	CandidateComponent      string                   `json:"candidate_component"`
	TargetComposition       string                   `json:"target_composition"`
	Contracts               []contract.Reference     `json:"contracts"`
	DirectDependencies      []composition.Dependency `json:"direct_dependencies"`
}

type IntegrationReport struct {
	Schema                    string `json:"schema"`
	Inspection                string `json:"inspection"`
	Passed                    bool   `json:"passed"`
	ContractCompatible        bool   `json:"contract_compatible"`
	IntegrationBaselinePassed bool   `json:"integration_baseline_passed"`
	EvidenceSHA256            string `json:"evidence_sha256"`
}

type ObservationReport struct {
	Schema            string `json:"schema"`
	Plan              string `json:"plan"`
	ActiveComposition string `json:"active_composition"`
	Passed            bool   `json:"passed"`
	EvidenceSHA256    string `json:"evidence_sha256"`
}

type planEnvelope struct {
	Schema string `json:"schema"`
	Plan   Plan   `json:"plan"`
	SHA256 string `json:"sha256"`
}

type LifecycleStatus struct {
	Schema                 string `json:"schema"`
	Plan                   string `json:"plan"`
	Phase                  string `json:"phase"`
	JournalRevision        uint64 `json:"journal_revision"`
	ActiveComposition      string `json:"active_composition"`
	ActiveKernelGeneration string `json:"active_kernel_generation"`
	ControlRevision        uint64 `json:"control_revision"`
	Consistent             bool   `json:"consistent"`
	NextAction             string `json:"next_action"`
}

// InspectCandidate reconstructs the candidate and target identities from a
// sealed baseline and a minimal candidate artifact. It never writes a plan or
// reads product state.
func InspectCandidate(repository, baselinePath, candidatePath string) (CandidateInspection, error) {
	baseline, err := composition.Load(baselinePath)
	if err != nil {
		return CandidateInspection{}, err
	}
	if _, err := composition.VerifySealed(baseline); err != nil {
		return CandidateInspection{}, fmt.Errorf("封存基线无效: %w", err)
	}
	artifact, err := loadStrict[CandidateArtifact](candidatePath)
	if err != nil {
		return CandidateInspection{}, err
	}
	if artifact.Schema != CandidateArtifactSchema || strings.TrimSpace(artifact.Role) == "" || len(artifact.Content) == 0 {
		return CandidateInspection{}, errors.New("候选制品描述无效")
	}
	artifactDigest, err := digestJSON(artifact)
	if err != nil {
		return CandidateInspection{}, err
	}
	replacement, err := sealReplacement(repository, baseline, artifact.Role, artifact.Content)
	if err != nil {
		return CandidateInspection{}, err
	}
	target, err := replaceComponent(baseline, replacement)
	if err != nil {
		return CandidateInspection{}, err
	}
	inspection := CandidateInspection{
		Schema: CandidateInspectionSchema, CandidateArtifactSHA256: artifactDigest,
		Role: artifact.Role, StateImpact: StateImpact(artifact.Role), BaselineComposition: baseline.Identity,
		CandidateComponent: replacement.Identity, TargetComposition: target.Identity,
		Contracts:          append([]contract.Reference(nil), replacement.Contracts...),
		DirectDependencies: append([]composition.Dependency(nil), replacement.Dependencies...),
	}
	inspection.Identity, err = candidateInspectionDigest(inspection)
	return inspection, err
}

// PreparePlan is the only artifact-to-Plan path. It reconstructs the inspected
// identities and binds an external integration report; callers cannot provide
// contracts, direct dependencies, Validation or a prebuilt Plan.
func PreparePlan(repository, baselinePath, candidatePath, integrationPath, outputPath string) (Plan, error) {
	inspection, err := InspectCandidate(repository, baselinePath, candidatePath)
	if err != nil {
		return Plan{}, err
	}
	report, err := loadStrict[IntegrationReport](integrationPath)
	if err != nil {
		return Plan{}, err
	}
	if report.Schema != IntegrationReportSchema || report.Inspection != inspection.Identity || !report.Passed ||
		!report.ContractCompatible || !report.IntegrationBaselinePassed || !isSHA256(report.EvidenceSHA256) {
		return Plan{}, errors.New("候选集成报告未绑定当前检查结果或未通过")
	}
	reportDigest, err := digestJSON(report)
	if err != nil {
		return Plan{}, err
	}
	baseline, err := composition.Load(baselinePath)
	if err != nil {
		return Plan{}, err
	}
	artifact, err := loadStrict[CandidateArtifact](candidatePath)
	if err != nil {
		return Plan{}, err
	}
	replacement, err := sealReplacement(repository, baseline, artifact.Role, artifact.Content)
	if err != nil {
		return Plan{}, err
	}
	validation := Validation{
		Schema: ValidationSchema, CandidateComponent: inspection.CandidateComponent,
		BaselineComposition: inspection.BaselineComposition, TargetComposition: inspection.TargetComposition,
		StateImpact: inspection.StateImpact, IntegrationSHA256: reportDigest, Passed: true,
	}
	plan, err := PrepareStateless(baseline, replacement, validation)
	if err != nil {
		return Plan{}, err
	}
	if plan.Identity == "" || plan.Target.Identity != inspection.TargetComposition {
		return Plan{}, errors.New("候选计划与检查结果不一致")
	}
	if err := WritePlan(outputPath, plan); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func WritePlan(path string, plan Plan) error {
	if err := validatePlan(plan); err != nil {
		return err
	}
	payload, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	envelope := planEnvelope{Schema: PlanEnvelopeSchema, Plan: plan, SHA256: hex.EncodeToString(digest[:])}
	encoded, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if existing, readErr := LoadPlan(path); readErr == nil {
		if existing.Identity == plan.Identity {
			return nil
		}
		return errors.New("计划输出已存在且身份不同")
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	return publishNewFile(path, encoded, 0o600)
}

func LoadPlan(path string) (Plan, error) {
	envelope, err := loadStrict[planEnvelope](path)
	if err != nil {
		return Plan{}, err
	}
	if envelope.Schema != PlanEnvelopeSchema || !isSHA256(envelope.SHA256) {
		return Plan{}, errors.New("候选计划封装无效")
	}
	payload, err := json.Marshal(envelope.Plan)
	if err != nil {
		return Plan{}, err
	}
	digest := sha256.Sum256(payload)
	if envelope.SHA256 != hex.EncodeToString(digest[:]) {
		return Plan{}, errors.New("候选计划完整性校验失败")
	}
	if err := validatePlan(envelope.Plan); err != nil {
		return Plan{}, err
	}
	return envelope.Plan, nil
}

func LoadObservation(path string, plan Plan) (Observation, error) {
	report, err := loadStrict[ObservationReport](path)
	if err != nil {
		return Observation{}, err
	}
	if report.Schema != ObservationReportSchema || report.Plan != plan.Identity ||
		report.ActiveComposition != plan.Target.Identity || !isSHA256(report.EvidenceSHA256) {
		return Observation{}, errors.New("观察报告未绑定当前候选计划")
	}
	reportDigest, err := digestJSON(report)
	if err != nil {
		return Observation{}, err
	}
	return Observation{
		Schema: ObservationSchema, Plan: plan.Identity, ActiveComposition: plan.Target.Identity,
		ReportSHA256: reportDigest, Passed: report.Passed,
	}, nil
}

func InspectStatus(state contract.ControlState, journal Journal, plan Plan) (LifecycleStatus, error) {
	if err := validatePlan(plan); err != nil {
		return LifecycleStatus{}, err
	}
	record, exists, err := journal.Read()
	if err != nil {
		return LifecycleStatus{}, err
	}
	status := LifecycleStatus{
		Schema: LifecycleStatusSchema, Plan: plan.Identity,
		ActiveComposition: state.ActiveComposition, ActiveKernelGeneration: state.ActiveKernelGeneration,
		ControlRevision: state.Revision,
	}
	if !exists {
		status.Phase = "not-started"
		status.Consistent = state.ActiveComposition == plan.Baseline.Identity
		status.NextAction = "activate"
		return status, nil
	}
	status.Phase = record.Phase
	status.JournalRevision = record.Revision
	if record.Plan != plan.Identity {
		if record.Phase == PhaseAccepted || record.Phase == PhaseRolledBack {
			status.Phase = "not-started"
			status.Consistent = state.ActiveComposition == plan.Baseline.Identity
			status.NextAction = "activate"
			return status, nil
		}
		status.NextAction = "resolve-other-plan"
		return status, nil
	}
	switch record.Phase {
	case PhasePrepared:
		status.Consistent = state.ActiveComposition == plan.Baseline.Identity || state.ActiveComposition == plan.Target.Identity
		status.NextAction = "activate"
	case PhaseObserving:
		status.Consistent = state.ActiveComposition == plan.Target.Identity
		status.NextAction = "complete-observation"
	case PhaseAccepted:
		status.Consistent = state.ActiveComposition == plan.Target.Identity
		status.NextAction = "complete"
	case PhaseRolledBack:
		status.Consistent = state.ActiveComposition == plan.Baseline.Identity
		status.NextAction = "complete"
	}
	return status, nil
}

func candidateInspectionDigest(inspection CandidateInspection) (string, error) {
	inspection.Identity = ""
	return digestJSON(inspection)
}

func loadStrict[T any](path string) (T, error) {
	var result T
	encoded, err := os.ReadFile(path)
	if err != nil {
		return result, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		return result, err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return result, errors.New("JSON 只能包含一个对象")
	} else if !errors.Is(err, io.EOF) {
		return result, err
	}
	return result, nil
}

func publishNewFile(path string, encoded []byte, mode os.FileMode) error {
	if !filepath.IsAbs(path) {
		return errors.New("输出路径必须是绝对路径")
	}
	parent := filepath.Dir(filepath.Clean(path))
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(parent, ".artifact-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Link(temporaryPath, path); err != nil {
		return fmt.Errorf("发布不可变制品: %w", err)
	}
	return nil
}
