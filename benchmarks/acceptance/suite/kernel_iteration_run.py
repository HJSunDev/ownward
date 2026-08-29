#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import kernel_iteration_evidence
import kernel_iteration_candidate
import kernel_iteration_candidate_multisource
import kernel_iteration_validation
import kernel_iteration_stage4
import kernel_iteration_stage4_multisource
import kernel_iteration_stage4_performance
import kernel_iteration_stage4_protection_performance


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Ownward 通用非正式内核迭代证据入口")
    parser.add_argument("--output", type=Path, required=True)
    selection = parser.add_mutually_exclusive_group()
    selection.add_argument("--subject", choices=["v0", "current-product"])
    selection.add_argument("--subject-manifest", type=Path)
    selection.add_argument("--runtime-state", type=Path)
    selection.add_argument("--blind-calibration-config", type=Path)
    selection.add_argument("--blind-plan-identity")
    selection.add_argument("--stage3-prepare", action="store_true")
    selection.add_argument("--stage3-finalize", action="store_true")
    selection.add_argument("--prepare-v2-candidate", action="store_true")
    selection.add_argument("--prepare-v2-multisource-candidate", action="store_true")
    parser.add_argument("--stage4-finalize", action="store_true")
    parser.add_argument("--stage4-multisource-diagnose", action="store_true")
    parser.add_argument("--stage4-multisource-performance", action="store_true")
    parser.add_argument("--stage4-protection-performance", action="store_true")
    parser.add_argument("--stage4-multisource-finalize", action="store_true")
    parser.add_argument("--evidence-type", default="identity-calibration")
    parser.add_argument("--input-manifest", type=Path)
    parser.add_argument("--execution-config", type=Path)
    parser.add_argument("--prepare-materials", type=Path)
    parser.add_argument("--write-input", type=Path)
    parser.add_argument("--formal-state", type=Path)
    parser.add_argument("--gate-seed")
    parser.add_argument("--compare-left", type=Path)
    parser.add_argument("--compare-right", type=Path)
    parser.add_argument("--candidate-result", type=Path)
    parser.add_argument("--baseline-subject-manifest", type=Path)
    parser.add_argument("--baseline-execution-config", type=Path)
    parser.add_argument("--baseline-run-root", type=Path)
    parser.add_argument("--baseline-result", type=Path)
    parser.add_argument("--candidate-run-root", type=Path)
    parser.add_argument("--multisource-result", type=Path)
    parser.add_argument("--multisource-performance-result", type=Path)
    parser.add_argument("--protection-performance-result", type=Path)
    parser.add_argument("--noncandidate-diagnostic", action="store_true")
    parser.add_argument("--stage3-plan-identity")
    parser.add_argument("--development-input", type=Path)
    parser.add_argument("--regression-input", type=Path)
    parser.add_argument("--current-development-result", type=Path)
    parser.add_argument("--v0-development-result", type=Path)
    parser.add_argument("--current-regression-result", type=Path)
    parser.add_argument("--v0-regression-result", type=Path)
    parser.add_argument("--development-result", type=Path)
    parser.add_argument("--regression-result", type=Path)
    parser.add_argument("--bind-blind-current-dependencies", action="store_true")
    parser.add_argument("--contract", type=Path)
    parser.add_argument("--resume", action="store_true")
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    if args.stage4_multisource_finalize:
        required = (
            args.subject_manifest, args.execution_config, args.multisource_result,
            args.development_result, args.regression_result, args.multisource_performance_result,
            args.protection_performance_result, args.formal_state,
        )
        if any(path is None for path in required):
            raise SystemExit("多来源终态必须提供候选、三份结果、两份性能复核与正式 state")
        result = kernel_iteration_stage4_multisource.finalize(
            HERE, args.output, args.subject_manifest, args.execution_config,
            args.multisource_result, args.development_result, args.regression_result,
            args.multisource_performance_result, args.protection_performance_result,
            args.formal_state, resume=args.resume,
        )
    elif args.stage4_protection_performance:
        required = (
            args.baseline_subject_manifest, args.baseline_execution_config, args.baseline_run_root,
            args.baseline_result, args.subject_manifest, args.execution_config, args.candidate_run_root,
            args.candidate_result, args.formal_state,
        )
        if any(path is None for path in required):
            raise SystemExit("保护性能复核必须提供基线/候选 subject、配置、运行根、结果与正式 state")
        result = kernel_iteration_stage4_protection_performance.run(
            HERE, args.output, args.baseline_subject_manifest, args.baseline_execution_config,
            args.baseline_run_root, args.baseline_result, args.subject_manifest, args.execution_config,
            args.candidate_run_root, args.candidate_result, args.formal_state,
        )
    elif args.stage4_multisource_performance:
        required = (
            args.baseline_subject_manifest, args.baseline_execution_config, args.baseline_run_root,
            args.baseline_result, args.subject_manifest, args.execution_config, args.candidate_run_root,
            args.candidate_result, args.formal_state,
        )
        if any(path is None for path in required):
            raise SystemExit("成对性能复核必须提供基线/候选 subject、配置、运行根、结果与正式 state")
        result = kernel_iteration_stage4_performance.run(
            HERE, args.output, args.baseline_subject_manifest, args.baseline_execution_config,
            args.baseline_run_root, args.baseline_result, args.subject_manifest, args.execution_config,
            args.candidate_run_root, args.candidate_result, args.formal_state,
        )
    elif args.stage4_multisource_diagnose:
        if args.subject_manifest is None or args.candidate_result is None or args.formal_state is None:
            raise SystemExit("多来源诊断必须提供首候选、执行结果与正式 state")
        result = kernel_iteration_stage4_multisource.diagnose(
            HERE, args.output, args.subject_manifest, args.candidate_result, args.formal_state, resume=args.resume,
        )
    elif args.stage4_finalize:
        required = (
            args.subject_manifest, args.execution_config, args.development_input, args.regression_input,
            args.development_result, args.regression_result, args.formal_state,
        )
        if any(path is None for path in required):
            raise SystemExit("阶段 4 收口必须提供候选、执行配置、开发/回归输入与结果、正式 state")
        result = kernel_iteration_stage4.finalize(
            HERE, args.output, args.subject_manifest, args.execution_config,
            args.development_input, args.regression_input, args.development_result,
            args.regression_result, args.formal_state, resume=args.resume,
        )
    elif args.stage3_prepare:
        if args.development_input is None or args.regression_input is None or args.formal_state is None:
            raise SystemExit("阶段 3 准备必须提供开发/回归输入与正式 state 只读基线")
        result = kernel_iteration_validation.prepare_stage3(
            HERE, args.output, args.development_input, args.regression_input, args.formal_state, resume=args.resume,
        )
    elif args.prepare_v2_multisource_candidate:
        if args.execution_config is None:
            raise SystemExit("V2 多来源候选准备必须提供当前非正式 --execution-config")
        result = kernel_iteration_candidate_multisource.prepare(
            HERE, args.output, args.execution_config, resume=args.resume,
        )
    elif args.prepare_v2_candidate:
        if args.execution_config is None:
            raise SystemExit("V2 候选准备必须提供当前非正式 --execution-config")
        result = kernel_iteration_candidate.prepare(
            HERE, args.output, args.execution_config, resume=args.resume,
        )
    elif args.stage3_finalize:
        paths = {
            "current-development": args.current_development_result,
            "v0-development": args.v0_development_result,
            "current-regression": args.current_regression_result,
            "v0-regression": args.v0_regression_result,
        }
        if args.stage3_plan_identity is None or args.formal_state is None or any(path is None for path in paths.values()):
            raise SystemExit("阶段 3 收口必须提供 plan identity、四份同尺结果与正式 state")
        result = kernel_iteration_validation.finalize_stage3(
            HERE, args.output, args.stage3_plan_identity, paths, args.formal_state, resume=args.resume,
        )
    elif args.compare_left is not None or args.compare_right is not None:
        if args.compare_left is None or args.compare_right is None:
            raise SystemExit("同尺比较必须同时提供 --compare-left 和 --compare-right")
        result = kernel_iteration_validation.compare_execution_results(args.compare_left, args.compare_right)
    elif args.prepare_materials is not None:
        if args.execution_config is None or args.write_input is None:
            raise SystemExit("准备执行输入必须提供 --execution-config 和 --write-input")
        result = kernel_iteration_validation.build_input_manifest(
            HERE,
            args.prepare_materials,
            args.execution_config,
            args.evidence_type,
            args.write_input,
        )
    elif args.blind_plan_identity is not None:
        if args.bind_blind_current_dependencies:
            if args.resume or args.execution_config is None or args.formal_state is None:
                raise SystemExit("封存五题当前依赖必须提供 --execution-config 和 --formal-state，且不得使用 --resume")
            result = kernel_iteration_validation.bind_blind_dependency_locator(
                HERE, args.output, args.blind_plan_identity, args.execution_config, args.formal_state,
            )
        else:
            if not args.resume:
                raise SystemExit("按 plan identity 恢复五题关卡必须提供 --resume")
            if args.gate_seed is not None or args.formal_state is not None or args.execution_config is not None:
                raise SystemExit("按 plan identity 恢复只读取已封存依赖定位，不得再次提供 seed、配置或正式 state")
            result = kernel_iteration_validation.resume_current_blind_by_plan_identity(
                HERE,
                args.output,
                args.blind_plan_identity,
            )
    elif args.blind_calibration_config is not None:
        if args.formal_state is None:
            raise SystemExit("五题校准必须提供 --formal-state 以证明正式状态只读")
        result = kernel_iteration_validation.calibrate_blind(
            HERE,
            args.output,
            args.blind_calibration_config,
            args.formal_state,
            seed=args.gate_seed,
            resume=args.resume,
        )
        locator = kernel_iteration_validation.bind_blind_dependency_locator(
            HERE, args.output, result["plan_identity"], args.blind_calibration_config, args.formal_state,
        )
        result = {**result, **locator}
    elif args.runtime_state is not None:
        if args.evidence_type != "identity-calibration" or args.input_manifest is not None:
            raise SystemExit("运行态校准不得同时指定候选证据类型或输入清单")
        result = kernel_iteration_evidence.calibrate_runtime(
            HERE,
            args.output,
            args.runtime_state,
            contract_path=args.contract,
            resume=args.resume,
        )
    else:
        if args.subject is None and args.subject_manifest is None:
            raise SystemExit("必须选择 --subject、--subject-manifest 或 --runtime-state")
        if args.execution_config is not None:
            if args.input_manifest is None:
                raise SystemExit("端到端执行必须提供 --input-manifest")
            if args.contract is not None:
                raise SystemExit("端到端执行只使用唯一版本化比较合同")
            result = kernel_iteration_validation.execute_prepared_evidence(
                HERE,
                args.output,
                args.execution_config,
                selector=args.subject,
                subject_manifest=args.subject_manifest,
                evidence_type=args.evidence_type,
                input_manifest=args.input_manifest,
                candidate_result_path=args.candidate_result,
                noncandidate_diagnostic=args.noncandidate_diagnostic,
                resume=args.resume,
            )
        else:
            result = kernel_iteration_evidence.run(
                HERE,
                args.output,
                selector=args.subject,
                subject_manifest=args.subject_manifest,
                evidence_type=args.evidence_type,
                input_manifest=args.input_manifest,
                contract_path=args.contract,
                resume=args.resume,
            )
    print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    main()
