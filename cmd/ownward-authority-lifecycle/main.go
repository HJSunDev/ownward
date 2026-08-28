//go:build ownward_migration

// Command ownward-authority-lifecycle is the offline, independently resumable
// entry for replacing the authority persistence implementation. It is absent
// from the normal product graph.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/HJSunDev/ownward/internal/authoritycandidate"
	"github.com/HJSunDev/ownward/internal/authoritysubstrate"
	"github.com/HJSunDev/ownward/internal/capabilitylifecycle"
	"github.com/HJSunDev/ownward/internal/composition"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
)

const resultSchema = "ownward.authority-persistence-command-result/v1"

type commandResult struct {
	Schema      string `json:"schema"`
	Command     string `json:"command"`
	Plan        string `json:"plan"`
	Phase       string `json:"phase"`
	Revision    uint64 `json:"revision"`
	Composition string `json:"active_composition"`
	Assets      int    `json:"asset_count"`
	Snapshot    string `json:"snapshot"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ownward-authority-lifecycle:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("需要 plan、prepare、catch-up、promote、status 或 observe 命令")
	}
	if args[0] == "plan" {
		return runPlan(args[1:])
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	planPath := flags.String("plan", "", "不可变权威持久化候选计划")
	journalPath := flags.String("journal", "", "追加式权威持久化检查点目录")
	dataDir := flags.String("data-dir", "", "既有权威基座数据目录")
	candidateDir := flags.String("candidate-dir", "", "隔离候选存储目录")
	backupPath := flags.String("backup", "", "接受候选前的可恢复权威备份")
	observationPath := flags.String("observation", "", "冻结观察报告")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	for label, path := range map[string]string{"plan": *planPath, "journal": *journalPath, "data-dir": *dataDir, "candidate-dir": *candidateDir} {
		if path == "" || !filepath.IsAbs(path) {
			return fmt.Errorf("%s 的 %s 必须是绝对路径", args[0], label)
		}
	}
	data := filepath.Clean(*dataDir)
	candidatePath := filepath.Clean(*candidateDir)
	journalDir := filepath.Clean(*journalPath)
	sealedPlan := filepath.Clean(*planPath)
	observationFile := filepath.Clean(*observationPath)
	backupFile := filepath.Clean(*backupPath)
	if args[0] == "observe" {
		if *observationPath == "" || !filepath.IsAbs(*observationPath) {
			return errors.New("observe 需要绝对 --observation")
		}
		if *backupPath != "" && !filepath.IsAbs(*backupPath) {
			return errors.New("observe 的 --backup 必须是绝对路径")
		}
	}
	if err := validateLifecyclePaths(args[0], sealedPlan, journalDir, data, candidatePath, observationFile, backupFile); err != nil {
		return err
	}
	plan, err := capabilitylifecycle.LoadAuthorityPlan(sealedPlan)
	if err != nil {
		return err
	}
	if plan.CandidateFormat != authoritycandidate.Format {
		return errors.New("计划候选格式与当前候选二进制不一致")
	}
	journal, err := capabilitylifecycle.OpenAuthorityJournal(journalDir)
	if err != nil {
		return err
	}
	switch args[0] {
	case "prepare", "catch-up":
		return runCopy(args[0], data, candidatePath, plan, journal)
	case "promote":
		return runPromote(data, candidatePath, plan, journal)
	case "status":
		return runStatus(data, candidatePath, plan, journal)
	case "observe":
		observation, err := loadObservation(observationFile, plan)
		if err != nil {
			return err
		}
		if observation.Passed && (*backupPath == "" || !filepath.IsAbs(*backupPath)) {
			return errors.New("接受候选需要绝对 --backup")
		}
		return runObserve(data, candidatePath, backupFile, plan, journal, observation)
	default:
		return fmt.Errorf("未知命令: %s", args[0])
	}
}

func validateLifecyclePaths(command, planPath, journalDir, dataDir, candidateDir, observationPath, backupPath string) error {
	state := map[string]string{"权威数据": dataDir, "候选存储": candidateDir, "生命周期检查点": journalDir}
	for leftName, left := range state {
		for rightName, right := range state {
			if leftName < rightName && !disjointPath(left, right) {
				return errors.New("权威数据、候选存储和生命周期检查点必须位于互不包含的独立目录")
			}
		}
	}
	artifacts := map[string]string{"计划": planPath}
	if command == "observe" {
		artifacts["观察报告"] = observationPath
		if backupPath != "." && strings.TrimSpace(backupPath) != "" {
			artifacts["备份"] = backupPath
		}
	}
	for artifactName, artifact := range artifacts {
		for stateName, root := range state {
			if !disjointPath(artifact, root) {
				return fmt.Errorf("迁移制品 %s 不得位于%s目录内或包含该目录", artifactName, stateName)
			}
		}
	}
	return nil
}

func disjointPath(left, right string) bool {
	relative, err := filepath.Rel(left, right)
	if err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return false
	}
	relative, err = filepath.Rel(right, left)
	return err != nil || relative != "." && (relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func runPlan(args []string) error {
	flags := flag.NewFlagSet("plan", flag.ContinueOnError)
	repository := flags.String("repository", "", "候选源码根")
	baseline := flags.String("baseline", "", "封存基线组合")
	candidate := flags.String("candidate", "", "候选制品内容清单")
	integration := flags.String("integration", "", "候选集成报告")
	output := flags.String("output", "", "不可变计划输出")
	if err := flags.Parse(args); err != nil {
		return err
	}
	for label, path := range map[string]string{"repository": *repository, "baseline": *baseline, "candidate": *candidate, "integration": *integration, "output": *output} {
		if path == "" {
			return fmt.Errorf("plan 缺少 --%s", label)
		}
	}
	plan, err := capabilitylifecycle.PrepareAuthorityPlan(*repository, *baseline, *candidate, *integration, authoritycandidate.Format, *output)
	if err != nil {
		return err
	}
	return composition.WriteJSON(os.Stdout, commandResult{Schema: resultSchema, Command: "plan", Plan: plan.Identity, Phase: "planned", Composition: plan.Target.Identity})
}

func runCopy(command, dataDir, candidateDir string, plan capabilitylifecycle.AuthorityPlan, journal capabilitylifecycle.AuthorityJournal) error {
	baseline, err := openBaseline(dataDir, plan)
	if err != nil {
		return err
	}
	state := baseline.Control().ReadControl()
	snapshot, assets, captureErr := capabilitylifecycle.CaptureAuthorityPersistence(baseline.Assets(), state)
	closeErr := baseline.Close()
	if captureErr != nil {
		return captureErr
	}
	if closeErr != nil {
		return closeErr
	}
	candidate, err := authoritycandidate.Open(candidateDir)
	if err != nil {
		return err
	}
	var record capabilitylifecycle.AuthorityRecord
	if command == "prepare" {
		record, err = capabilitylifecycle.PrepareAuthorityStore(plan, candidate, journal, snapshot, assets)
	} else {
		record, err = capabilitylifecycle.CatchUpAuthorityStore(plan, candidate, journal, snapshot, assets)
	}
	closeErr = candidate.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return writeResult(command, plan, record)
}

func runPromote(dataDir, candidateDir string, plan capabilitylifecycle.AuthorityPlan, journal capabilitylifecycle.AuthorityJournal) error {
	record, exists, err := journal.Read()
	if err != nil || !exists {
		return errors.New("权威候选没有准备检查点")
	}
	// Crash recovery after the control CAS no longer needs the baseline store.
	if record.Phase == capabilitylifecycle.AuthorityPhaseSwitching {
		candidate, openErr := authoritycandidate.Open(candidateDir)
		if openErr == nil {
			control, controlErr := openCandidateControl(dataDir, plan)
			if controlErr == nil && control.ReadControl().ActiveComposition == plan.Target.Identity {
				snapshot, _, captureErr := capabilitylifecycle.CaptureAuthorityPersistence(candidate, control.ReadControl())
				if captureErr == nil {
					record, err = capabilitylifecycle.ReconcileAuthoritySwitch(plan, control, journal, snapshot)
				}
			}
			_ = candidate.Close()
			if err == nil && record.Phase == capabilitylifecycle.AuthorityPhaseObserving {
				return writeResult("promote", plan, record)
			}
		}
	}
	baseline, err := openBaseline(dataDir, plan)
	if err != nil {
		return err
	}
	defer baseline.Close()
	candidate, err := authoritycandidate.Open(candidateDir)
	if err != nil {
		return err
	}
	defer candidate.Close()
	state := baseline.Control().ReadControl()
	snapshot, _, err := capabilitylifecycle.CaptureAuthorityPersistence(baseline.Assets(), state)
	if err != nil {
		return err
	}
	record, err = capabilitylifecycle.PromoteAuthorityStore(plan, baseline.Assets(), candidate, baseline.Control(), journal, snapshot)
	if err != nil {
		return err
	}
	return writeResult("promote", plan, record)
}

func runStatus(dataDir, candidateDir string, plan capabilitylifecycle.AuthorityPlan, journal capabilitylifecycle.AuthorityJournal) error {
	record, exists, err := journal.Read()
	if err != nil || !exists || record.Plan != plan.Identity {
		return errors.New("权威持久化候选没有匹配检查点")
	}
	if record.Phase == capabilitylifecycle.AuthorityPhaseSwitching {
		candidate, openErr := authoritycandidate.Open(candidateDir)
		if openErr == nil {
			control, controlErr := openCandidateControl(dataDir, plan)
			if controlErr == nil && control.ReadControl().ActiveComposition == plan.Target.Identity {
				active, _, captureErr := capabilitylifecycle.CaptureAuthorityPersistence(candidate, control.ReadControl())
				if captureErr == nil {
					record, err = capabilitylifecycle.ReconcileAuthoritySwitch(plan, control, journal, active)
				}
			}
			_ = candidate.Close()
		}
	}
	if record.Phase == capabilitylifecycle.AuthorityPhaseRollbackReady {
		baseline, openErr := openBaseline(dataDir, plan)
		if openErr == nil {
			active, _, captureErr := capabilitylifecycle.CaptureAuthorityPersistence(baseline.Assets(), baseline.Control().ReadControl())
			if captureErr == nil {
				record, err = capabilitylifecycle.ReconcileAuthorityRollback(plan, baseline.Control(), journal, active)
			}
			_ = baseline.Close()
		}
	}
	if err != nil {
		return err
	}
	state, closeFn, err := openSelectedState(dataDir, candidateDir, plan)
	if err != nil {
		return err
	}
	defer closeFn()
	if err := capabilitylifecycle.ValidateAuthorityStatus(plan, record, state); err != nil {
		return err
	}
	return writeResult("status", plan, record)
}

func runObserve(dataDir, candidateDir, backupPath string, plan capabilitylifecycle.AuthorityPlan, journal capabilitylifecycle.AuthorityJournal, observation capabilitylifecycle.AuthorityObservation) error {
	existing, exists, err := journal.Read()
	if err != nil {
		return err
	} else if exists && existing.Plan == plan.Identity && (existing.Phase == capabilitylifecycle.AuthorityPhaseAccepted || existing.Phase == capabilitylifecycle.AuthorityPhaseRolledBack) {
		return writeResult("observe", plan, existing)
	} else if exists && existing.Plan == plan.Identity && existing.Phase == capabilitylifecycle.AuthorityPhaseRollbackReady {
		baseline, openErr := openBaseline(dataDir, plan)
		if openErr == nil {
			active, _, captureErr := capabilitylifecycle.CaptureAuthorityPersistence(baseline.Assets(), baseline.Control().ReadControl())
			if captureErr == nil {
				existing, err = capabilitylifecycle.ReconcileAuthorityRollback(plan, baseline.Control(), journal, active)
			}
			_ = baseline.Close()
			if err == nil && existing.Phase == capabilitylifecycle.AuthorityPhaseRolledBack {
				return writeResult("observe", plan, existing)
			}
		}
	}
	if !exists || existing.Plan != plan.Identity || existing.Phase != capabilitylifecycle.AuthorityPhaseObserving {
		return errors.New("权威候选没有可观察的匹配检查点")
	}
	// Long rollback catch-up happens before the final candidate write barrier.
	if !observation.Passed {
		candidate, err := authoritycandidate.Open(candidateDir)
		if err != nil {
			return err
		}
		control, err := openCandidateControl(dataDir, plan)
		if err != nil {
			candidate.Close()
			return err
		}
		_, _, err = capabilitylifecycle.CaptureAuthorityPersistence(candidate, control.ReadControl())
		history, historyErr := candidate.ChangesSince(existing.Baseline.Versions)
		candidate.Close()
		if err != nil {
			return err
		}
		if historyErr != nil {
			return historyErr
		}
		rollback, closeRollback, err := authoritysubstrate.OpenInactiveBaselineForMigration(dataDir)
		if err != nil {
			return err
		}
		changes, err := capabilitylifecycle.ChangesFromAuthorityHistory(rollback.ListCurrent(), history)
		if err == nil {
			err = capabilitylifecycle.ApplyAuthorityChanges(rollback, changes)
		}
		closeErr := closeRollback()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	candidate, err := authoritycandidate.Open(candidateDir)
	if err != nil {
		return err
	}
	defer candidate.Close()
	control, err := openCandidateControl(dataDir, plan)
	if err != nil {
		return err
	}
	state := control.ReadControl()
	latest, _, err := capabilitylifecycle.CaptureAuthorityPersistence(candidate, state)
	if err != nil {
		return err
	}
	backupDigest := ""
	if observation.Passed {
		if _, statErr := os.Stat(backupPath); errors.Is(statErr, os.ErrNotExist) {
			if err := candidate.BackupAuthority(backupPath, state); err != nil {
				return err
			}
		} else if statErr != nil {
			return statErr
		} else if err := verifyCandidateBackup(backupPath, state, latest); err != nil {
			return err
		}
		backupDigest, err = fileDigest(backupPath)
		if err != nil {
			return err
		}
		record, err := capabilitylifecycle.CompleteAuthorityObservation(plan, candidate, nil, control, journal, observation, latest, backupDigest)
		if err != nil {
			return err
		}
		return writeResult("observe", plan, record)
	}
	rollback, closeRollback, err := authoritysubstrate.OpenInactiveBaselineForMigration(dataDir)
	if err != nil {
		return err
	}
	record, completeErr := capabilitylifecycle.CompleteAuthorityObservation(plan, candidate, &candidateAdapter{AssetAuthority: rollback}, control, journal, observation, latest, "")
	closeErr := closeRollback()
	if completeErr != nil {
		return completeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return writeResult("observe", plan, record)
}

func verifyCandidateBackup(path string, expectedControl contract.ControlState, expected capabilitylifecycle.AuthorityPersistenceSnapshot) error {
	parent := filepath.Dir(path)
	restored, err := os.MkdirTemp(parent, ".candidate-backup-check-*")
	if err != nil {
		return err
	}
	if err := os.Remove(restored); err != nil {
		return err
	}
	defer os.RemoveAll(restored)
	control, err := authoritycandidate.RestoreAuthority(path, restored)
	if err != nil || control != expectedControl {
		return errors.New("既有候选权威备份与当前控制状态不一致")
	}
	store, err := authoritycandidate.Open(restored)
	if err != nil {
		return err
	}
	snapshot, _, captureErr := capabilitylifecycle.CaptureAuthorityPersistence(store, control)
	closeErr := store.Close()
	if captureErr != nil {
		return captureErr
	}
	if closeErr != nil {
		return closeErr
	}
	if snapshot.Identity != expected.Identity {
		return errors.New("既有候选权威备份与当前资产快照不一致")
	}
	return nil
}

type candidateAdapter struct{ contract.AssetAuthority }

func (adapter *candidateAdapter) Seed(values []domain.Information) error {
	return capabilitylifecycle.ApplyAuthorityChanges(adapter.AssetAuthority, values)
}
func (adapter *candidateAdapter) ApplyChanges(values []domain.Information) error {
	return capabilitylifecycle.ApplyAuthorityChanges(adapter.AssetAuthority, values)
}
func (adapter *candidateAdapter) ChangesSince([]contract.AssetVersion) ([]domain.Information, error) {
	return nil, errors.New("read-only rollback adapter does not own active change history")
}
func (adapter *candidateAdapter) BackupAuthority(string, contract.ControlState) error {
	return errors.New("回退源备份不由候选适配器执行")
}

func openBaseline(dataDir string, plan capabilitylifecycle.AuthorityPlan) (*authoritysubstrate.Substrate, error) {
	kernel, err := kernelIdentity(plan.Baseline)
	if err != nil {
		return nil, err
	}
	return authoritysubstrate.Open(dataDir, contract.ControlState{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: plan.Baseline.Identity, ActiveKernelGeneration: kernel})
}

func openCandidateControl(dataDir string, plan capabilitylifecycle.AuthorityPlan) (contract.ControlAuthority, error) {
	baselineKernel, err := kernelIdentity(plan.Baseline)
	if err != nil {
		return nil, err
	}
	targetKernel, err := kernelIdentity(plan.Target)
	if err != nil {
		return nil, err
	}
	return authoritysubstrate.OpenExistingControlForMigration(dataDir,
		contract.ControlState{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: plan.Baseline.Identity, ActiveKernelGeneration: baselineKernel},
		contract.ControlState{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: plan.Target.Identity, ActiveKernelGeneration: targetKernel})
}

func openSelectedState(dataDir, candidateDir string, plan capabilitylifecycle.AuthorityPlan) (contract.ControlState, func() error, error) {
	baseline, err := openBaseline(dataDir, plan)
	if err == nil {
		return baseline.Control().ReadControl(), baseline.Close, nil
	}
	candidate, candidateErr := authoritycandidate.Open(candidateDir)
	if candidateErr != nil {
		return contract.ControlState{}, nil, errors.Join(err, candidateErr)
	}
	control, controlErr := openCandidateControl(dataDir, plan)
	if controlErr != nil {
		candidate.Close()
		return contract.ControlState{}, nil, controlErr
	}
	return control.ReadControl(), candidate.Close, nil
}

func kernelIdentity(manifest composition.Manifest) (string, error) {
	for _, component := range manifest.Components {
		if component.Role == "kernel" && component.Identity != "" {
			return component.Identity, nil
		}
	}
	return "", errors.New("组合缺少内核身份")
}

func loadObservation(path string, plan capabilitylifecycle.AuthorityPlan) (capabilitylifecycle.AuthorityObservation, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return capabilitylifecycle.AuthorityObservation{}, err
	}
	var observation capabilitylifecycle.AuthorityObservation
	if err := json.Unmarshal(encoded, &observation); err != nil {
		return capabilitylifecycle.AuthorityObservation{}, err
	}
	if observation.Schema != capabilitylifecycle.AuthorityObservationSchema || observation.Plan != plan.Identity || observation.ActiveComposition != plan.Target.Identity {
		return capabilitylifecycle.AuthorityObservation{}, errors.New("观察报告未绑定当前权威候选")
	}
	return observation, nil
}

func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func writeResult(command string, plan capabilitylifecycle.AuthorityPlan, record capabilitylifecycle.AuthorityRecord) error {
	snapshot := record.Baseline
	compositionID := plan.Baseline.Identity
	if record.Phase == capabilitylifecycle.AuthorityPhaseObserving || record.Phase == capabilitylifecycle.AuthorityPhaseAccepted || record.Phase == capabilitylifecycle.AuthorityPhaseRollbackReady {
		snapshot = record.Candidate
		compositionID = plan.Target.Identity
	}
	return composition.WriteJSON(os.Stdout, commandResult{Schema: resultSchema, Command: command, Plan: plan.Identity, Phase: record.Phase, Revision: record.Revision, Composition: compositionID, Assets: snapshot.AssetCount, Snapshot: snapshot.Identity})
}
