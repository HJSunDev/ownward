#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import kernel_iteration_evidence
import kernel_iteration_validation


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Ownward 通用非正式内核迭代证据入口")
    parser.add_argument("--output", type=Path, required=True)
    selection = parser.add_mutually_exclusive_group()
    selection.add_argument("--subject", choices=["v0", "current-product"])
    selection.add_argument("--subject-manifest", type=Path)
    selection.add_argument("--runtime-state", type=Path)
    selection.add_argument("--blind-calibration-config", type=Path)
    selection.add_argument("--blind-plan-identity")
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
    parser.add_argument("--bind-blind-current-dependencies", action="store_true")
    parser.add_argument("--contract", type=Path)
    parser.add_argument("--resume", action="store_true")
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    if args.compare_left is not None or args.compare_right is not None:
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
