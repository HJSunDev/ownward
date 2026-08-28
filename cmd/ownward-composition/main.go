package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/HJSunDev/ownward/internal/authoritysubstrate"
	"github.com/HJSunDev/ownward/internal/capabilitylifecycle"
	"github.com/HJSunDev/ownward/internal/composition"
	"github.com/HJSunDev/ownward/internal/contract"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ownward-composition:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	command := "verify"
	if len(args) > 0 && isCommand(args[0]) {
		command = args[0]
		args = args[1:]
	}
	switch command {
	case "verify", "seal":
		return runComposition(command, args)
	case "lifecycle-inspect":
		return runLifecycleInspect(args)
	case "lifecycle-prepare":
		return runLifecyclePrepare(args)
	case "lifecycle-status":
		return runLifecycleStatus(args)
	case "lifecycle-activate":
		return runLifecycleActivate(args)
	case "lifecycle-complete":
		return runLifecycleComplete(args)
	default:
		return fmt.Errorf("未知命令: %s", command)
	}
}

func isCommand(value string) bool {
	switch value {
	case "verify", "seal", "lifecycle-inspect", "lifecycle-prepare", "lifecycle-status", "lifecycle-activate", "lifecycle-complete":
		return true
	default:
		return false
	}
}

func runComposition(command string, args []string) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	repository := flags.String("repository", ".", "仓库根目录")
	manifestPath := flags.String("manifest", filepath.FromSlash("manifests/compositions/v1/current-collaborative.json"), "组合清单")
	outputPath := flags.String("output", "", "seal 输出文件；为空时写标准输出")
	if err := flags.Parse(args); err != nil {
		return err
	}
	manifest, err := composition.Load(*manifestPath)
	if err != nil {
		return err
	}
	switch command {
	case "seal":
		sealed, err := composition.Seal(*repository, manifest)
		if err != nil {
			return err
		}
		if *outputPath != "" {
			output, err := os.Create(*outputPath)
			if err != nil {
				return err
			}
			if err := composition.WriteJSON(output, sealed); err != nil {
				_ = output.Close()
				return err
			}
			return output.Close()
		}
		return composition.WriteJSON(os.Stdout, sealed)
	case "verify":
		result, err := composition.Verify(*repository, manifest)
		if err != nil {
			return err
		}
		return composition.WriteJSON(os.Stdout, result)
	}
	return nil
}

func runLifecycleInspect(args []string) error {
	flags := flag.NewFlagSet("lifecycle-inspect", flag.ContinueOnError)
	repository := flags.String("repository", ".", "候选内容仓库根目录")
	manifest := flags.String("manifest", "", "封存基线组合清单")
	candidate := flags.String("candidate", "", "候选制品描述")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *manifest == "" || *candidate == "" {
		return fmt.Errorf("lifecycle-inspect 需要 --manifest 与 --candidate")
	}
	inspection, err := capabilitylifecycle.InspectCandidate(*repository, *manifest, *candidate)
	if err != nil {
		return err
	}
	return composition.WriteJSON(os.Stdout, inspection)
}

func runLifecyclePrepare(args []string) error {
	flags := flag.NewFlagSet("lifecycle-prepare", flag.ContinueOnError)
	repository := flags.String("repository", ".", "候选内容仓库根目录")
	manifest := flags.String("manifest", "", "封存基线组合清单")
	candidate := flags.String("candidate", "", "候选制品描述")
	integration := flags.String("integration", "", "绑定候选检查身份的集成报告")
	output := flags.String("output", "", "不可变候选计划输出路径")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *manifest == "" || *candidate == "" || *integration == "" || *output == "" {
		return fmt.Errorf("lifecycle-prepare 需要 --manifest、--candidate、--integration 与 --output")
	}
	plan, err := capabilitylifecycle.PreparePlan(*repository, *manifest, *candidate, *integration, *output)
	if err != nil {
		return err
	}
	return composition.WriteJSON(os.Stdout, map[string]any{
		"schema": "ownward.stateless-capability-prepare-result/v1", "passed": true,
		"plan": plan.Identity, "candidate_component": plan.Replacement.Identity,
		"baseline_composition": plan.Baseline.Identity, "target_composition": plan.Target.Identity,
		"output": filepath.Clean(*output),
	})
}

func lifecycleInputs(command string, args []string, needsObservation bool) (*capabilitylifecycle.Plan, capabilitylifecycle.Journal, string, string, error) {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	planPath := flags.String("plan", "", "封存候选计划")
	journalPath := flags.String("journal", "", "追加式候选生命周期检查点目录")
	dataDir := flags.String("data-dir", "", "离线权威基座数据目录")
	observationPath := flags.String("observation", "", "绑定计划的观察报告，仅 complete 使用")
	if err := flags.Parse(args); err != nil {
		return nil, nil, "", "", err
	}
	if *planPath == "" || *journalPath == "" || *dataDir == "" || (needsObservation && *observationPath == "") {
		return nil, nil, "", "", fmt.Errorf("%s 需要 --plan、--journal、--data-dir%s", command, map[bool]string{true: " 与 --observation", false: ""}[needsObservation])
	}
	plan, err := capabilitylifecycle.LoadPlan(*planPath)
	if err != nil {
		return nil, nil, "", "", err
	}
	journal, err := capabilitylifecycle.OpenFileJournal(*journalPath)
	if err != nil {
		return nil, nil, "", "", err
	}
	return &plan, journal, filepath.Clean(*dataDir), filepath.Clean(*observationPath), nil
}

func runLifecycleStatus(args []string) error {
	plan, journal, dataDir, _, err := lifecycleInputs("lifecycle-status", args, false)
	if err != nil {
		return err
	}
	authority, err := openLifecycleAuthority(dataDir, *plan)
	if err != nil {
		return err
	}
	defer authority.Close()
	state := authority.Control().ReadControl()
	status, err := capabilitylifecycle.InspectStatus(state, journal, *plan)
	if err != nil {
		return err
	}
	return composition.WriteJSON(os.Stdout, status)
}

func runLifecycleActivate(args []string) error {
	plan, journal, dataDir, _, err := lifecycleInputs("lifecycle-activate", args, false)
	if err != nil {
		return err
	}
	authority, err := openLifecycleAuthority(dataDir, *plan)
	if err != nil {
		return err
	}
	defer authority.Close()
	state, err := capabilitylifecycle.ActivateForNextStart(authority.Control(), journal, *plan)
	if err != nil {
		return err
	}
	status, err := capabilitylifecycle.InspectStatus(state, journal, *plan)
	if err != nil {
		return err
	}
	return composition.WriteJSON(os.Stdout, status)
}

func runLifecycleComplete(args []string) error {
	plan, journal, dataDir, observationPath, err := lifecycleInputs("lifecycle-complete", args, true)
	if err != nil {
		return err
	}
	observation, err := capabilitylifecycle.LoadObservation(observationPath, *plan)
	if err != nil {
		return err
	}
	authority, err := openLifecycleAuthority(dataDir, *plan)
	if err != nil {
		return err
	}
	defer authority.Close()
	state, err := capabilitylifecycle.CompleteObservation(authority.Control(), journal, *plan, observation)
	if err != nil {
		return err
	}
	status, err := capabilitylifecycle.InspectStatus(state, journal, *plan)
	if err != nil {
		return err
	}
	return composition.WriteJSON(os.Stdout, status)
}

// openLifecycleAuthority is the only control-state entry used by lifecycle
// commands. Existence is checked before Open so an offline command can never
// initialize an authority decision from a candidate plan. The private control
// envelope remains interpreted solely by authoritysubstrate.Open, which also
// acquires the product's asset lock before returning the control port.
func openLifecycleAuthority(dataDir string, plan capabilitylifecycle.Plan) (*authoritysubstrate.Substrate, error) {
	if !filepath.IsAbs(dataDir) {
		return nil, errors.New("权威基座数据目录必须是绝对路径")
	}
	dataDir = filepath.Clean(dataDir)
	controlPath := filepath.Join(dataDir, "authority", "control.json")
	info, err := os.Stat(controlPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("权威控制状态尚未建立，生命周期命令禁止初始化")
		}
		return nil, fmt.Errorf("检查既有权威控制状态: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("权威控制状态不是普通文件")
	}
	baselineKernel, err := lifecycleKernelIdentity(plan.Baseline)
	if err != nil {
		return nil, err
	}
	targetKernel, err := lifecycleKernelIdentity(plan.Target)
	if err != nil {
		return nil, err
	}
	if targetKernel != baselineKernel {
		return nil, errors.New("无状态候选改变了活动内核身份")
	}
	states := []contract.ControlState{
		{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: plan.Baseline.Identity, ActiveKernelGeneration: baselineKernel},
		{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: plan.Target.Identity, ActiveKernelGeneration: targetKernel},
	}
	var failures []error
	for _, state := range states {
		authority, openErr := authoritysubstrate.Open(dataDir, state)
		if openErr == nil {
			return authority, nil
		}
		failures = append(failures, openErr)
	}
	return nil, fmt.Errorf("无法在离线安全边界打开既有权威状态: %w", errors.Join(failures...))
}

func lifecycleKernelIdentity(manifest composition.Manifest) (string, error) {
	for _, component := range manifest.Components {
		if component.Role == "kernel" && component.Identity != "" {
			return component.Identity, nil
		}
	}
	return "", errors.New("候选组合缺少内核身份")
}
