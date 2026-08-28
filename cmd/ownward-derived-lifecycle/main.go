//go:build ownward_migration

// Command onward-derived-lifecycle is the offline, candidate-binary-owned
// entry for replacing any capability that invalidates rebuildable derived
// state. It is deliberately absent from the normal product build graph.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

const resultSchema = "ownward.derived-capability-command-result/v1"

type commandResources struct {
	openVector func(string) (contract.VectorCapability, error)
	newBuilder func(contract.VectorCapability) capabilitylifecycle.DerivedBuilder
}

type commandResult struct {
	Schema      string `json:"schema"`
	Command     string `json:"command"`
	Plan        string `json:"plan"`
	Phase       string `json:"phase"`
	Revision    uint64 `json:"revision"`
	Generation  string `json:"generation"`
	Composition string `json:"active_composition"`
	Snapshot    string `json:"authority_snapshot"`
}

func main() {
	resources := commandResources{openVector: func(path string) (contract.VectorCapability, error) {
		return embedding.OpenManaged(path)
	}, newBuilder: func(vector contract.VectorCapability) capabilitylifecycle.DerivedBuilder {
		return &derivedcandidate.Collaborative{Vector: vector}
	}}
	if err := run(context.Background(), os.Args[1:], resources); err != nil {
		fmt.Fprintln(os.Stderr, "ownward-derived-lifecycle:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, resources commandResources) error {
	if len(args) == 0 {
		return errors.New("需要 prepare、catch-up、promote、status 或 observe 命令")
	}
	command := args[0]
	if command == "plan" {
		return runPlan(args[1:])
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	planPath := flags.String("plan", "", "不可变派生候选计划")
	journalPath := flags.String("journal", "", "追加式派生生命周期检查点目录")
	dataDir := flags.String("data-dir", "", "既有权威基座数据目录")
	vectorBundle := flags.String("vector-bundle", "", "当前候选二进制绑定的向量能力包")
	generation := flags.String("generation", "", "新候选派生世代标识，仅 prepare 使用")
	observationPath := flags.String("observation", "", "冻结观察报告，仅 observe 使用")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *planPath == "" || *journalPath == "" || *dataDir == "" {
		return fmt.Errorf("%s 需要 --plan、--journal 与 --data-dir", command)
	}
	for label, path := range map[string]string{"plan": *planPath, "journal": *journalPath, "data-dir": *dataDir} {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("%s 必须是绝对路径", label)
		}
	}
	plan, err := capabilitylifecycle.LoadDerivedPlan(filepath.Clean(*planPath))
	if err != nil {
		return err
	}
	journal, err := capabilitylifecycle.OpenDerivedJournal(filepath.Clean(*journalPath))
	if err != nil {
		return err
	}
	data := filepath.Clean(*dataDir)
	root := filepath.Join(data, "state")

	switch command {
	case "status":
		record, exists, err := journal.Read()
		if err != nil || !exists || record.Plan != plan.Identity {
			return errors.New("派生候选没有匹配的耐久检查点")
		}
		substrate, err := openExistingAuthority(data, plan)
		if err != nil {
			return err
		}
		state := substrate.Control().ReadControl()
		closeErr := substrate.Close()
		if closeErr != nil {
			return closeErr
		}
		active, err := derived.ActiveGeneration(root)
		if err != nil {
			return err
		}
		if err := validateCommandStatus(plan, record, state, active.Generation); err != nil {
			return err
		}
		return writeResult(command, plan, record)
	case "prepare":
		if strings.TrimSpace(*generation) == "" {
			return errors.New("prepare 需要 --generation")
		}
		snapshot, assets, err := captureExistingAuthority(data, plan)
		if err != nil {
			return err
		}
		builder, closeVector, err := openCandidateBuilder(*vectorBundle, resources)
		if err != nil {
			return err
		}
		defer closeVector()
		record, err := capabilitylifecycle.PrepareDerivedGenerationAtSnapshot(ctx, root, plan, builder, journal, *generation, snapshot, assets)
		if err != nil {
			return err
		}
		return writeResult(command, plan, record)
	case "catch-up":
		snapshot, assets, err := captureExistingAuthority(data, plan)
		if err != nil {
			return err
		}
		builder, closeVector, err := openCandidateBuilder(*vectorBundle, resources)
		if err != nil {
			return err
		}
		defer closeVector()
		record, err := capabilitylifecycle.CatchUpDerivedGenerationAtSnapshot(ctx, root, plan, builder, journal, snapshot, assets)
		if err != nil {
			return err
		}
		return writeResult(command, plan, record)
	case "promote":
		substrate, err := openExistingAuthority(data, plan)
		if err != nil {
			return err
		}
		defer substrate.Close()
		snapshot, _, err := capabilitylifecycle.CaptureAuthoritySnapshot(substrate.Assets())
		if err != nil {
			return err
		}
		record, err := capabilitylifecycle.PromoteDerivedAtSnapshot(snapshot, substrate.Control(), root, plan, journal)
		if err != nil {
			return err
		}
		return writeResult(command, plan, record)
	case "observe":
		if *observationPath == "" || !filepath.IsAbs(*observationPath) {
			return errors.New("observe 需要绝对 --observation 路径")
		}
		observation, err := capabilitylifecycle.LoadDerivedObservation(filepath.Clean(*observationPath), plan)
		if err != nil {
			return err
		}
		if !observation.Passed {
			snapshot, assets, err := captureExistingAuthority(data, plan)
			if err != nil {
				return err
			}
			builder, closeVector, err := openCandidateBuilder(*vectorBundle, resources)
			if err != nil {
				return err
			}
			_, err = capabilitylifecycle.CatchUpRollbackGenerationAtSnapshot(ctx, root, plan, builder, journal, snapshot, assets, observation)
			closeVector()
			if err != nil {
				return err
			}
		}
		substrate, err := openExistingAuthority(data, plan)
		if err != nil {
			return err
		}
		defer substrate.Close()
		snapshot, _, err := capabilitylifecycle.CaptureAuthoritySnapshot(substrate.Assets())
		if err != nil {
			return err
		}
		if observation.Passed {
			if _, err := capabilitylifecycle.SealObservedGenerationAtSnapshot(root, plan, journal, snapshot); err != nil {
				return err
			}
		}
		record, err := capabilitylifecycle.CompleteDerivedObservationAtSnapshot(snapshot, substrate.Control(), root, plan, journal, observation)
		if err != nil {
			return err
		}
		return writeResult(command, plan, record)
	default:
		return fmt.Errorf("未知命令: %s", command)
	}
}

func validateCommandStatus(plan capabilitylifecycle.DerivedPlan, record capabilitylifecycle.DerivedRecord, state contract.ControlState, generation string) error {
	baseline := state.ActiveComposition == plan.Baseline.Identity && state.ActiveKernelGeneration == plan.BaselineRun.Kernel
	target := state.ActiveComposition == plan.Target.Identity && state.ActiveKernelGeneration == plan.TargetRun.Kernel
	switch record.Phase {
	case capabilitylifecycle.DerivedPhaseReady:
		if baseline && generation == record.Baseline.Generation {
			return nil
		}
	case capabilitylifecycle.DerivedPhaseSwitching:
		if baseline && (generation == record.Baseline.Generation || generation == record.Candidate.Generation) ||
			target && generation == record.Candidate.Generation {
			return nil
		}
	case capabilitylifecycle.DerivedPhaseObserving, capabilitylifecycle.DerivedPhaseRollbackReady, capabilitylifecycle.DerivedPhaseAccepted:
		if target && generation == record.Candidate.Generation {
			return nil
		}
	case capabilitylifecycle.DerivedPhaseRolledBack:
		if baseline && generation == record.Baseline.Generation {
			return nil
		}
	}
	return errors.New("耐久派生阶段与权威控制或活动世代不一致")
}

func runPlan(args []string) error {
	flags := flag.NewFlagSet("plan", flag.ContinueOnError)
	baselinePath := flags.String("baseline", "", "封存基线组合清单")
	role := flags.String("role", "", "待替换的派生组件角色")
	validationPath := flags.String("validation", "", "绑定集成验证报告")
	output := flags.String("output", "", "不可变派生候选计划输出")
	if err := flags.Parse(args); err != nil {
		return err
	}
	for label, path := range map[string]string{"baseline": *baselinePath, "validation": *validationPath, "output": *output} {
		if path == "" || !filepath.IsAbs(path) {
			return fmt.Errorf("plan 的 %s 必须是绝对路径", label)
		}
	}
	baseline, err := composition.Load(filepath.Clean(*baselinePath))
	if err != nil {
		return err
	}
	if _, err := composition.VerifySealed(baseline); err != nil {
		return err
	}
	embedded, err := composition.Parse(compositionv1.CurrentCollaborative())
	if err != nil {
		return err
	}
	var replacement composition.Component
	for _, component := range embedded.Components {
		if component.Role == *role {
			replacement = component
			break
		}
	}
	if replacement.Identity == "" || capabilitylifecycle.StateImpact(replacement.Role) != capabilitylifecycle.ImpactDerived {
		return errors.New("当前候选二进制没有封存指定派生组件")
	}
	target, _, err := capabilitylifecycle.InspectDerivedTarget(baseline, replacement)
	if err != nil {
		return err
	}
	if target.Identity != embedded.Identity {
		return errors.New("封存基线与当前候选二进制不能组成唯一目标组合")
	}
	validation, err := capabilitylifecycle.LoadDerivedValidation(filepath.Clean(*validationPath))
	if err != nil {
		return err
	}
	plan, err := capabilitylifecycle.PrepareDerived(baseline, replacement, validation)
	if err != nil {
		return err
	}
	if err := capabilitylifecycle.WriteDerivedPlan(filepath.Clean(*output), plan); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(commandResult{Schema: resultSchema, Command: "plan", Plan: plan.Identity, Phase: "planned", Composition: plan.Target.Identity})
}

func openCandidateBuilder(vectorBundle string, resources commandResources) (capabilitylifecycle.DerivedBuilder, func(), error) {
	if resources.openVector == nil || resources.newBuilder == nil || vectorBundle == "" || !filepath.IsAbs(vectorBundle) {
		return nil, nil, errors.New("候选命令需要绝对 --vector-bundle 及向量资源")
	}
	vector, err := resources.openVector(filepath.Clean(vectorBundle))
	if err != nil {
		return nil, nil, err
	}
	builder := resources.newBuilder(vector)
	if builder == nil {
		_ = vector.Close()
		return nil, nil, errors.New("候选二进制没有封存派生构建边界")
	}
	return builder, func() { _ = vector.Close() }, nil
}

func captureExistingAuthority(dataDir string, plan capabilitylifecycle.DerivedPlan) (capabilitylifecycle.AuthoritySnapshot, []domain.Information, error) {
	substrate, err := openExistingAuthority(dataDir, plan)
	if err != nil {
		return capabilitylifecycle.AuthoritySnapshot{}, nil, err
	}
	snapshot, assets, captureErr := capabilitylifecycle.CaptureAuthoritySnapshot(substrate.Assets())
	closeErr := substrate.Close()
	if captureErr != nil {
		return capabilitylifecycle.AuthoritySnapshot{}, nil, captureErr
	}
	return snapshot, assets, closeErr
}

func openExistingAuthority(dataDir string, plan capabilitylifecycle.DerivedPlan) (*authoritysubstrate.Substrate, error) {
	controlPath := filepath.Join(dataDir, "authority", "control.json")
	if info, err := os.Stat(controlPath); err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("权威控制状态尚未建立，派生生命周期禁止初始化")
	}
	states := []contract.ControlState{
		{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: plan.Baseline.Identity, ActiveKernelGeneration: plan.BaselineRun.Kernel},
		{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: plan.Target.Identity, ActiveKernelGeneration: plan.TargetRun.Kernel},
	}
	var failures []error
	for _, state := range states {
		substrate, err := authoritysubstrate.Open(dataDir, state)
		if err == nil {
			return substrate, nil
		}
		failures = append(failures, err)
	}
	return nil, fmt.Errorf("无法在离线安全边界打开既有权威状态: %w", errors.Join(failures...))
}

func writeResult(command string, plan capabilitylifecycle.DerivedPlan, record capabilitylifecycle.DerivedRecord) error {
	generation := record.Candidate.Generation
	compositionID := plan.Baseline.Identity
	snapshot := record.CandidateSnapshot.Identity
	if record.Phase == capabilitylifecycle.DerivedPhaseObserving || record.Phase == capabilitylifecycle.DerivedPhaseAccepted {
		compositionID = plan.Target.Identity
	}
	if record.Phase == capabilitylifecycle.DerivedPhaseRollbackReady || record.Phase == capabilitylifecycle.DerivedPhaseRolledBack {
		generation = record.Baseline.Generation
		snapshot = record.BaselineSnapshot.Identity
	}
	return json.NewEncoder(os.Stdout).Encode(commandResult{Schema: resultSchema, Command: command, Plan: plan.Identity, Phase: record.Phase, Revision: record.Revision, Generation: generation, Composition: compositionID, Snapshot: snapshot})
}
