//go:build ownward_migration

package capabilitylifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/HJSunDev/ownward/internal/composition"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
)

const (
	AuthorityValidationSchema  = "ownward.authority-persistence-validation/v1"
	AuthorityPlanSchema        = "ownward.authority-persistence-plan/v1"
	AuthorityPlanEnvelope      = "ownward.authority-persistence-plan-envelope/v1"
	AuthorityObservationSchema = "ownward.authority-persistence-observation/v1"
)

type AuthorityValidation struct {
	Schema                  string `json:"schema"`
	CandidateComponent      string `json:"candidate_component"`
	BaselineComposition     string `json:"baseline_composition"`
	TargetComposition       string `json:"target_composition"`
	StateImpact             string `json:"state_impact"`
	CandidateFormat         string `json:"candidate_format"`
	IntegrationSHA256       string `json:"integration_sha256"`
	AssetSemanticsPassed    bool   `json:"asset_semantics_passed"`
	BackupRestorePassed     bool   `json:"backup_restore_passed"`
	ExclusiveWriterPassed   bool   `json:"exclusive_writer_passed"`
	IntegrationBaselinePass bool   `json:"integration_baseline_passed"`
}

type AuthorityPlan struct {
	Schema          string                `json:"schema"`
	Identity        string                `json:"identity"`
	Role            string                `json:"role"`
	CandidateFormat string                `json:"candidate_format"`
	Baseline        composition.Manifest  `json:"baseline"`
	Replacement     composition.Component `json:"replacement"`
	Target          composition.Manifest  `json:"target"`
	Validation      AuthorityValidation   `json:"validation"`
}

type AuthorityPersistenceSnapshot struct {
	Schema      string                  `json:"schema"`
	Identity    string                  `json:"identity"`
	AssetCount  int                     `json:"asset_count"`
	AssetSHA256 string                  `json:"asset_sha256"`
	Versions    []contract.AssetVersion `json:"versions"`
	Control     contract.ControlState   `json:"control"`
}

type AuthorityObservation struct {
	Schema            string `json:"schema"`
	Plan              string `json:"plan"`
	ActiveComposition string `json:"active_composition"`
	ReportSHA256      string `json:"report_sha256"`
	Passed            bool   `json:"passed"`
}

type AuthorityCandidate interface {
	contract.AssetAuthority
	Seed([]domain.Information) error
	ApplyChanges([]domain.Information) error
	ChangesSince([]contract.AssetVersion) ([]domain.Information, error)
	BackupAuthority(string, contract.ControlState) error
}

type authorityPlanEnvelope struct {
	Schema string        `json:"schema"`
	Plan   AuthorityPlan `json:"plan"`
	SHA256 string        `json:"sha256"`
}

func InspectAuthorityTarget(current composition.Manifest, replacement composition.Component) (composition.Manifest, error) {
	if StateImpact(replacement.Role) != ImpactAuthority {
		return composition.Manifest{}, errors.New("组件不属于权威状态生命周期")
	}
	return replaceComponent(current, replacement)
}

func PrepareAuthority(current composition.Manifest, replacement composition.Component, validation AuthorityValidation) (AuthorityPlan, error) {
	if StateImpact(replacement.Role) != ImpactAuthority {
		return AuthorityPlan{}, errors.New("候选不属于权威状态生命周期")
	}
	target, err := replaceComponent(current, replacement)
	if err != nil {
		return AuthorityPlan{}, err
	}
	plan := AuthorityPlan{Schema: AuthorityPlanSchema, Role: replacement.Role, CandidateFormat: validation.CandidateFormat, Baseline: current, Replacement: replacement, Target: target, Validation: validation}
	plan.Identity, err = authorityPlanDigest(plan)
	if err != nil {
		return AuthorityPlan{}, err
	}
	if err := validateAuthorityPlan(plan); err != nil {
		return AuthorityPlan{}, err
	}
	return plan, nil
}

func PrepareAuthorityPlan(repository, baselinePath, candidatePath, integrationPath, candidateFormat, outputPath string) (AuthorityPlan, error) {
	inspection, err := InspectCandidate(repository, baselinePath, candidatePath)
	if err != nil {
		return AuthorityPlan{}, err
	}
	if inspection.Role != "authority-substrate" || inspection.StateImpact != ImpactAuthority {
		return AuthorityPlan{}, errors.New("候选制品不是权威持久化实现")
	}
	report, err := loadStrict[AuthorityValidation](integrationPath)
	if err != nil {
		return AuthorityPlan{}, err
	}
	baseline, err := composition.Load(baselinePath)
	if err != nil {
		return AuthorityPlan{}, err
	}
	artifact, err := loadStrict[CandidateArtifact](candidatePath)
	if err != nil {
		return AuthorityPlan{}, err
	}
	replacement, err := sealReplacement(repository, baseline, artifact.Role, artifact.Content)
	if err != nil {
		return AuthorityPlan{}, err
	}
	if report.Schema != AuthorityValidationSchema || report.CandidateComponent != inspection.CandidateComponent ||
		report.BaselineComposition != inspection.BaselineComposition || report.TargetComposition != inspection.TargetComposition ||
		report.StateImpact != ImpactAuthority || report.CandidateFormat != candidateFormat || !isSHA256(report.IntegrationSHA256) ||
		!report.AssetSemanticsPassed || !report.BackupRestorePassed || !report.ExclusiveWriterPassed || !report.IntegrationBaselinePass {
		return AuthorityPlan{}, errors.New("权威持久化集成报告未绑定或未通过")
	}
	plan, err := PrepareAuthority(baseline, replacement, report)
	if err != nil {
		return AuthorityPlan{}, err
	}
	if err := WriteAuthorityPlan(outputPath, plan); err != nil {
		return AuthorityPlan{}, err
	}
	return plan, nil
}

func WriteAuthorityPlan(path string, plan AuthorityPlan) error {
	if err := validateAuthorityPlan(plan); err != nil {
		return err
	}
	payload, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	encoded, err := json.MarshalIndent(authorityPlanEnvelope{Schema: AuthorityPlanEnvelope, Plan: plan, SHA256: hex.EncodeToString(digest[:])}, "", "  ")
	if err != nil {
		return err
	}
	if existing, readErr := LoadAuthorityPlan(path); readErr == nil {
		if existing.Identity == plan.Identity {
			return nil
		}
		return errors.New("权威持久化计划输出已存在且身份不同")
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	return publishNewFile(path, append(encoded, '\n'), 0o600)
}

func LoadAuthorityPlan(path string) (AuthorityPlan, error) {
	envelope, err := loadStrict[authorityPlanEnvelope](path)
	if err != nil {
		return AuthorityPlan{}, err
	}
	payload, err := json.Marshal(envelope.Plan)
	if err != nil {
		return AuthorityPlan{}, err
	}
	digest := sha256.Sum256(payload)
	if envelope.Schema != AuthorityPlanEnvelope || envelope.SHA256 != hex.EncodeToString(digest[:]) || validateAuthorityPlan(envelope.Plan) != nil {
		return AuthorityPlan{}, errors.New("权威持久化计划封装无效")
	}
	return envelope.Plan, nil
}

func CaptureAuthorityPersistence(authority contract.AssetAuthority, control contract.ControlState) (AuthorityPersistenceSnapshot, []domain.Information, error) {
	if authority == nil || control.Validate() != nil {
		return AuthorityPersistenceSnapshot{}, nil, errors.New("权威资产或控制状态无效")
	}
	var assets []domain.Information
	if capturer, ok := authority.(interface {
		CaptureCurrentForMigration() []domain.Information
	}); ok {
		assets = capturer.CaptureCurrentForMigration()
	} else {
		assets = authority.ListCurrent()
	}
	return CaptureAuthorityPersistenceFromAssets(assets, control)
}

func CaptureAuthorityPersistenceFromAssets(assets []domain.Information, control contract.ControlState) (AuthorityPersistenceSnapshot, []domain.Information, error) {
	if control.Validate() != nil {
		return AuthorityPersistenceSnapshot{}, nil, errors.New("权威控制状态无效")
	}
	assets = append([]domain.Information(nil), assets...)
	sort.Slice(assets, func(i, j int) bool { return assets[i].ID < assets[j].ID })
	versions := make([]contract.AssetVersion, len(assets))
	for index, asset := range assets {
		if err := asset.Validate(); err != nil {
			return AuthorityPersistenceSnapshot{}, nil, err
		}
		versions[index] = contract.AssetVersion{ID: asset.ID, Revision: asset.Revision}
	}
	assetDigest, err := digestJSON(assets)
	if err != nil {
		return AuthorityPersistenceSnapshot{}, nil, err
	}
	snapshot := AuthorityPersistenceSnapshot{Schema: "ownward.authority-persistence-snapshot/v1", AssetCount: len(assets), AssetSHA256: assetDigest, Versions: versions, Control: control}
	snapshot.Identity, err = authoritySnapshotDigest(snapshot)
	return snapshot, assets, err
}

func PrepareAuthorityStore(plan AuthorityPlan, candidate AuthorityCandidate, journal AuthorityJournal, baseline AuthorityPersistenceSnapshot, assets []domain.Information) (AuthorityRecord, error) {
	if err := validateAuthorityPlan(plan); err != nil {
		return AuthorityRecord{}, err
	}
	if candidate == nil || journal == nil || validateAuthoritySnapshot(baseline) != nil || baseline.Control.ActiveComposition != plan.Baseline.Identity {
		return AuthorityRecord{}, errors.New("权威候选准备输入无效")
	}
	if err := candidate.Seed(assets); err != nil {
		return AuthorityRecord{}, err
	}
	candidateSnapshot, _, err := CaptureAuthorityPersistence(candidate, baseline.Control)
	if err != nil || !sameAuthorityAssets(baseline, candidateSnapshot) {
		return AuthorityRecord{}, errors.New("权威候选基线复制不完整")
	}
	current, exists, err := journal.Read()
	if err != nil {
		return AuthorityRecord{}, err
	}
	if exists && current.Plan == plan.Identity {
		if current.Phase == AuthorityPhaseReady && sameAuthorityAssets(current.Candidate, candidateSnapshot) {
			return current, nil
		}
		return AuthorityRecord{}, errors.New("权威候选已经进入其他阶段")
	}
	revision := uint64(0)
	if exists {
		revision = current.Revision
	}
	record := AuthorityRecord{Schema: AuthorityRecordSchema, Revision: revision + 1, Plan: plan.Identity, Phase: AuthorityPhaseReady, Baseline: baseline, Candidate: candidateSnapshot, CandidateFormat: plan.CandidateFormat}
	return journal.Append(revision, record)
}

func CatchUpAuthorityStore(plan AuthorityPlan, candidate AuthorityCandidate, journal AuthorityJournal, latest AuthorityPersistenceSnapshot, assets []domain.Information) (AuthorityRecord, error) {
	if err := validateAuthorityPlan(plan); err != nil {
		return AuthorityRecord{}, err
	}
	record, exists, err := journal.Read()
	if err != nil || !exists || record.Plan != plan.Identity || (record.Phase != AuthorityPhaseReady && record.Phase != AuthorityPhaseSwitching) {
		return AuthorityRecord{}, errors.New("权威候选没有可追平检查点")
	}
	if latest.Control.ActiveComposition != plan.Baseline.Identity || !snapshotAdvances(record.Candidate, latest) {
		return AuthorityRecord{}, errors.New("权威候选追平快照倒退、漂移或活动组合不符")
	}
	changes, err := changedAssets(candidate.ListCurrent(), assets)
	if err != nil {
		return AuthorityRecord{}, err
	}
	if err := candidate.ApplyChanges(changes); err != nil {
		return AuthorityRecord{}, err
	}
	candidateSnapshot, _, err := CaptureAuthorityPersistence(candidate, latest.Control)
	if err != nil || !sameAuthorityAssets(latest, candidateSnapshot) {
		return AuthorityRecord{}, errors.New("权威候选追平后仍不完整")
	}
	if sameAuthorityAssets(record.Candidate, candidateSnapshot) && record.Candidate.Control.Revision == candidateSnapshot.Control.Revision {
		return record, nil
	}
	record.Revision++
	record.Baseline = latest
	record.Candidate = candidateSnapshot
	return journal.Append(record.Revision-1, record)
}

// PromoteAuthorityStore is the bounded final barrier. The candidate must have
// been fully caught up and checkpointed before this call. The barrier only
// captures and compares the two complete snapshots, seals the switching
// checkpoint, and performs the unique control CAS; it never copies assets.
func PromoteAuthorityStore(plan AuthorityPlan, source contract.AssetAuthority, candidate AuthorityCandidate, control contract.ControlAuthority, journal AuthorityJournal, latest AuthorityPersistenceSnapshot) (AuthorityRecord, error) {
	if source == nil || candidate == nil || control == nil || journal == nil || validateAuthoritySnapshot(latest) != nil {
		return AuthorityRecord{}, errors.New("最终权威屏障输入无效")
	}
	record, exists, err := journal.Read()
	if err != nil || !exists || record.Plan != plan.Identity {
		return AuthorityRecord{}, errors.New("权威候选没有可晋升检查点")
	}
	state := control.ReadControl()
	if record.Phase == AuthorityPhaseObserving && state.ActiveComposition == plan.Target.Identity {
		return record, nil
	}
	if record.Phase != AuthorityPhaseReady && record.Phase != AuthorityPhaseSwitching {
		return AuthorityRecord{}, errors.New("权威候选阶段不能晋升")
	}
	if latest.Control != state || state.ActiveComposition != plan.Baseline.Identity {
		return AuthorityRecord{}, errors.New("最终权威屏障快照与控制状态不一致")
	}
	candidateSnapshot, _, err := CaptureAuthorityPersistence(candidate, latest.Control)
	if err != nil || record.Baseline.Identity != latest.Identity || record.Candidate.Identity != candidateSnapshot.Identity || !sameAuthorityAssets(latest, candidateSnapshot) {
		return AuthorityRecord{}, errors.New("权威候选尚未在最终屏障外追平并封存")
	}
	barrierSnapshot, _, err := CaptureAuthorityPersistence(source, control.ReadControl())
	if err != nil || barrierSnapshot.Identity != latest.Identity {
		return AuthorityRecord{}, errors.New("最终权威屏障内仍有新写入，必须再次追平")
	}
	if record.Phase == AuthorityPhaseReady {
		record.Revision++
		record.Phase = AuthorityPhaseSwitching
		record.Baseline = latest
		record.Candidate = candidateSnapshot
		record, err = journal.Append(record.Revision-1, record)
		if err != nil {
			return AuthorityRecord{}, err
		}
	}
	state = control.ReadControl()
	if state.ActiveComposition == plan.Baseline.Identity {
		next := state
		next.Revision++
		next.ActiveComposition = plan.Target.Identity
		next.ActiveKernelGeneration = authorityKernelIdentity(plan.Target)
		if _, err := control.CompareAndSwapControl(state.Revision, next); err != nil {
			return AuthorityRecord{}, err
		}
		state = next
	} else if state.ActiveComposition != plan.Target.Identity {
		return AuthorityRecord{}, errors.New("权威控制已被其他候选修改")
	}
	if record.Phase == AuthorityPhaseSwitching {
		record.Revision++
		record.Phase = AuthorityPhaseObserving
		record.Candidate.Control = state
		record.Candidate.Identity, _ = authoritySnapshotDigest(record.Candidate)
		return journal.Append(record.Revision-1, record)
	}
	return record, nil
}

// CompleteAuthorityObservation is the bounded observation barrier. A failed
// observation requires the rollback store to have been caught up before this
// call. The barrier compares complete snapshots and performs the control CAS;
// it never scans or replays change history.
func CompleteAuthorityObservation(plan AuthorityPlan, active, rollback AuthorityCandidate, control contract.ControlAuthority, journal AuthorityJournal, observation AuthorityObservation, latest AuthorityPersistenceSnapshot, recoveryBackup string) (AuthorityRecord, error) {
	if control == nil || active == nil || journal == nil || observation.Schema != AuthorityObservationSchema || observation.Plan != plan.Identity || observation.ActiveComposition != plan.Target.Identity || !isSHA256(observation.ReportSHA256) {
		return AuthorityRecord{}, errors.New("权威持久化观察输入无效")
	}
	record, exists, err := journal.Read()
	if err != nil || !exists || record.Plan != plan.Identity {
		return AuthorityRecord{}, errors.New("权威候选没有观察检查点")
	}
	state := control.ReadControl()
	if record.Phase == AuthorityPhaseAccepted || record.Phase == AuthorityPhaseRolledBack {
		return record, nil
	}
	if state != latest.Control || state.ActiveComposition != plan.Target.Identity || !sameAuthorityAssets(latest, mustSnapshot(active, state)) {
		return AuthorityRecord{}, errors.New("观察边界的活动权威快照不一致")
	}
	if observation.Passed {
		if !isSHA256(recoveryBackup) {
			return AuthorityRecord{}, errors.New("接受权威候选前缺少可恢复备份")
		}
		record.Revision++
		record.Phase = AuthorityPhaseAccepted
		record.Candidate = latest
		record.ObservationSHA256 = observation.ReportSHA256
		record.RecoveryBackup = recoveryBackup
		return journal.Append(record.Revision-1, record)
	}
	if rollback == nil {
		return AuthorityRecord{}, errors.New("观察失败缺少只读回退源")
	}
	rollbackSnapshot, _, err := CaptureAuthorityPersistence(rollback, state)
	if err != nil || !sameAuthorityAssets(latest, rollbackSnapshot) {
		return AuthorityRecord{}, errors.New("回退源尚未在最终屏障外追平观察期更新")
	}
	barrierSnapshot, _, err := CaptureAuthorityPersistence(active, control.ReadControl())
	if err != nil || barrierSnapshot.Identity != latest.Identity {
		return AuthorityRecord{}, errors.New("回退最终屏障内仍有新写入，必须再次追平")
	}
	if record.Phase == AuthorityPhaseObserving {
		record.Revision++
		record.Phase = AuthorityPhaseRollbackReady
		record.Baseline = rollbackSnapshot
		record.Candidate = latest
		record.ObservationSHA256 = observation.ReportSHA256
		record, err = journal.Append(record.Revision-1, record)
		if err != nil {
			return AuthorityRecord{}, err
		}
	}
	state = control.ReadControl()
	if state.ActiveComposition == plan.Target.Identity {
		next := state
		next.Revision++
		next.ActiveComposition = plan.Baseline.Identity
		next.ActiveKernelGeneration = authorityKernelIdentity(plan.Baseline)
		if _, err := control.CompareAndSwapControl(state.Revision, next); err != nil {
			return AuthorityRecord{}, err
		}
		state = next
	} else if state.ActiveComposition != plan.Baseline.Identity {
		return AuthorityRecord{}, errors.New("回退期间权威控制被其他候选修改")
	}
	record.Revision++
	record.Phase = AuthorityPhaseRolledBack
	record.Baseline.Control = state
	record.Baseline.Identity, _ = authoritySnapshotDigest(record.Baseline)
	return journal.Append(record.Revision-1, record)
}

// ReconcileAuthoritySwitch closes the crash window after the control CAS and
// before the observing checkpoint append. The selected candidate contents
// must still match the last sealed switching snapshot.
func ReconcileAuthoritySwitch(plan AuthorityPlan, control contract.ControlAuthority, journal AuthorityJournal, active AuthorityPersistenceSnapshot) (AuthorityRecord, error) {
	record, exists, err := journal.Read()
	if err != nil || !exists || record.Plan != plan.Identity || record.Phase != AuthorityPhaseSwitching {
		return AuthorityRecord{}, errors.New("没有可恢复的权威持久化切换")
	}
	state := control.ReadControl()
	if state.ActiveComposition != plan.Target.Identity || !sameAuthorityAssets(record.Candidate, active) || active.Control != state {
		return AuthorityRecord{}, errors.New("权威持久化切换后的活动状态不一致")
	}
	record.Revision++
	record.Phase = AuthorityPhaseObserving
	record.Candidate = active
	return journal.Append(record.Revision-1, record)
}

func ReconcileAuthorityRollback(plan AuthorityPlan, control contract.ControlAuthority, journal AuthorityJournal, active AuthorityPersistenceSnapshot) (AuthorityRecord, error) {
	record, exists, err := journal.Read()
	if err != nil || !exists || record.Plan != plan.Identity || record.Phase != AuthorityPhaseRollbackReady {
		return AuthorityRecord{}, errors.New("没有可恢复的权威持久化回退")
	}
	state := control.ReadControl()
	if state.ActiveComposition != plan.Baseline.Identity || !sameAuthorityAssets(record.Baseline, active) || active.Control != state {
		return AuthorityRecord{}, errors.New("权威持久化回退后的活动状态不一致")
	}
	record.Revision++
	record.Phase = AuthorityPhaseRolledBack
	record.Baseline = active
	return journal.Append(record.Revision-1, record)
}

func ValidateAuthorityStatus(plan AuthorityPlan, record AuthorityRecord, state contract.ControlState) error {
	if err := validateAuthorityPlan(plan); err != nil {
		return err
	}
	if record.validate() != nil || record.Plan != plan.Identity || state.Validate() != nil {
		return errors.New("权威持久化状态身份无效")
	}
	baseline := state.ActiveComposition == plan.Baseline.Identity
	target := state.ActiveComposition == plan.Target.Identity
	switch record.Phase {
	case AuthorityPhaseReady:
		if baseline {
			return nil
		}
	case AuthorityPhaseSwitching:
		if baseline || target {
			return nil
		}
	case AuthorityPhaseObserving, AuthorityPhaseRollbackReady, AuthorityPhaseAccepted:
		if target {
			return nil
		}
	case AuthorityPhaseRolledBack:
		if baseline {
			return nil
		}
	}
	return errors.New("权威持久化检查点与唯一活动控制不一致")
}

func ChangesForAuthorityCatchUp(current, latest []domain.Information) ([]domain.Information, error) {
	return changedAssets(current, latest)
}

func ChangesFromAuthorityHistory(current, history []domain.Information) ([]domain.Information, error) {
	versions := make(map[string]uint64, len(current))
	for _, value := range current {
		versions[value.ID] = value.Revision
	}
	changes := make([]domain.Information, 0)
	for _, value := range history {
		known := versions[value.ID]
		if value.Revision <= known {
			continue
		}
		if known == 0 && value.Revision != 1 || known != 0 && value.Revision != known+1 {
			return nil, errors.New("权威变化历史不连续")
		}
		changes = append(changes, value)
		versions[value.ID] = value.Revision
	}
	return changes, nil
}

// ApplyAuthorityChanges is the generic migration adapter for a retained
// legacy source. Public authority updates remain sequential and CAS-bound.
func ApplyAuthorityChanges(authority contract.AssetAuthority, changes []domain.Information) error {
	for _, value := range changes {
		current, exists := authority.ReadCurrent(value.ID)
		if !exists {
			if value.Revision != 1 {
				return errors.New("回退源缺少资产初始版本")
			}
			if _, err := authority.CreateAsset(value); err != nil {
				return err
			}
			continue
		}
		if value.Revision <= current.Revision {
			continue
		}
		if value.Revision != current.Revision+1 {
			return errors.New("回退源资产版本不连续，不能从当前契约跳跃恢复")
		}
		if _, err := authority.UpdateAsset(value, current.Revision); err != nil {
			return err
		}
	}
	return nil
}

func validateAuthorityPlan(plan AuthorityPlan) error {
	if plan.Schema != AuthorityPlanSchema || plan.Role != "authority-substrate" || StateImpact(plan.Role) != ImpactAuthority || strings.TrimSpace(plan.CandidateFormat) == "" ||
		plan.Replacement.Role != plan.Role || plan.Validation.Schema != AuthorityValidationSchema || plan.Validation.StateImpact != ImpactAuthority ||
		plan.Validation.CandidateComponent != plan.Replacement.Identity || plan.Validation.BaselineComposition != plan.Baseline.Identity || plan.Validation.TargetComposition != plan.Target.Identity ||
		plan.Validation.CandidateFormat != plan.CandidateFormat || !plan.Validation.AssetSemanticsPassed || !plan.Validation.BackupRestorePassed || !plan.Validation.ExclusiveWriterPassed || !plan.Validation.IntegrationBaselinePass || !isSHA256(plan.Validation.IntegrationSHA256) {
		return errors.New("权威持久化计划字段无效")
	}
	if _, err := composition.VerifySealed(plan.Baseline); err != nil {
		return err
	}
	target, err := replaceComponent(plan.Baseline, plan.Replacement)
	if err != nil || target.Identity != plan.Target.Identity {
		return errors.New("权威持久化目标组合身份无效")
	}
	expected, err := authorityPlanDigest(plan)
	if err != nil || expected != plan.Identity {
		return errors.New("权威持久化计划身份漂移")
	}
	return nil
}

func authorityPlanDigest(plan AuthorityPlan) (string, error) {
	plan.Identity = ""
	return digestJSON(plan)
}

func authoritySnapshotDigest(snapshot AuthorityPersistenceSnapshot) (string, error) {
	snapshot.Identity = ""
	return digestJSON(snapshot)
}

func validateAuthoritySnapshot(snapshot AuthorityPersistenceSnapshot) error {
	if snapshot.Schema != "ownward.authority-persistence-snapshot/v1" || !isSHA256(snapshot.Identity) || !isSHA256(snapshot.AssetSHA256) || snapshot.AssetCount != len(snapshot.Versions) || snapshot.Control.Validate() != nil {
		return errors.New("权威持久化快照无效")
	}
	for index, version := range snapshot.Versions {
		if strings.TrimSpace(version.ID) == "" || version.Revision == 0 || index > 0 && snapshot.Versions[index-1].ID >= version.ID {
			return errors.New("权威持久化快照版本集合无效")
		}
	}
	expected, err := authoritySnapshotDigest(snapshot)
	if err != nil || expected != snapshot.Identity {
		return errors.New("权威持久化快照身份漂移")
	}
	return nil
}

func snapshotAdvances(previous, next AuthorityPersistenceSnapshot) bool {
	if validateAuthoritySnapshot(previous) != nil || validateAuthoritySnapshot(next) != nil || next.Control.Revision < previous.Control.Revision {
		return false
	}
	previousVersions := make(map[string]uint64, len(previous.Versions))
	for _, version := range previous.Versions {
		previousVersions[version.ID] = version.Revision
	}
	for _, version := range next.Versions {
		if old, exists := previousVersions[version.ID]; exists && version.Revision < old {
			return false
		}
		delete(previousVersions, version.ID)
	}
	return len(previousVersions) == 0
}

func sameAuthorityAssets(left, right AuthorityPersistenceSnapshot) bool {
	return left.AssetCount == right.AssetCount && left.AssetSHA256 == right.AssetSHA256 && fmt.Sprint(left.Versions) == fmt.Sprint(right.Versions)
}

func changedAssets(current, latest []domain.Information) ([]domain.Information, error) {
	currentByID := make(map[string]domain.Information, len(current))
	for _, value := range current {
		currentByID[value.ID] = value
	}
	changes := make([]domain.Information, 0)
	for _, value := range latest {
		old, exists := currentByID[value.ID]
		if !exists || old.Revision < value.Revision {
			changes = append(changes, value)
		} else if old.Revision > value.Revision {
			return nil, errors.New("候选权威资产版本领先于活动权威")
		} else {
			oldJSON, _ := json.Marshal(old)
			valueJSON, _ := json.Marshal(value)
			if string(oldJSON) != string(valueJSON) {
				return nil, errors.New("同一权威资产版本内容漂移")
			}
		}
		delete(currentByID, value.ID)
	}
	if len(currentByID) != 0 {
		return nil, errors.New("活动权威缺少候选已有资产")
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].ID < changes[j].ID })
	return changes, nil
}

func mustSnapshot(authority contract.AssetAuthority, state contract.ControlState) AuthorityPersistenceSnapshot {
	snapshot, _, _ := CaptureAuthorityPersistence(authority, state)
	return snapshot
}

func authorityKernelIdentity(manifest composition.Manifest) string {
	for _, component := range manifest.Components {
		if component.Role == "kernel" {
			return component.Identity
		}
	}
	return ""
}
