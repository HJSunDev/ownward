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
import kernel_iteration_candidate_latency
import kernel_iteration_candidate_system_budget
import kernel_iteration_validation
import kernel_iteration_stage4
import kernel_iteration_stage4_multisource
import kernel_iteration_stage4_performance
import kernel_iteration_stage4_protection_performance
import kernel_iteration_stage4_latency_data
import kernel_iteration_stage4_latency_performance
import kernel_iteration_stage4_latency_contention
import kernel_iteration_stage4_latency_candidate
import kernel_iteration_stage4_latency_candidate_data
import kernel_iteration_stage4_latency_finalize
import kernel_iteration_stage4_vector_runtime
import kernel_iteration_stage4_vector_runtime_followup
import kernel_iteration_stage4_latency_shared_runtime
import kernel_iteration_stage4_latency_real_scale
import kernel_iteration_stage4_runtime_implementation_probe
import kernel_iteration_stage4_runtime_implementation_probe_batch2
import kernel_iteration_stage4_runtime_implementation_assessment
import kernel_iteration_stage4_semantic_model_screening
import kernel_iteration_stage4_hierarchical_feasibility
import kernel_iteration_stage4_latency_comparability
import kernel_iteration_stage4_system_budget
import kernel_iteration_stage4_system_budget_finalize


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
    selection.add_argument("--prepare-v2-latency-candidate", action="store_true")
    selection.add_argument("--prepare-v2-system-budget-candidate", action="store_true")
    parser.add_argument("--stage4-finalize", action="store_true")
    parser.add_argument("--stage4-multisource-diagnose", action="store_true")
    parser.add_argument("--stage4-multisource-performance", action="store_true")
    parser.add_argument("--stage4-protection-performance", action="store_true")
    parser.add_argument("--stage4-multisource-finalize", action="store_true")
    parser.add_argument("--stage4-latency-prepare", action="store_true")
    parser.add_argument("--stage4-latency-diagnose", action="store_true")
    parser.add_argument("--stage4-latency-contention", action="store_true")
    parser.add_argument("--stage4-latency-candidate-performance", action="store_true")
    parser.add_argument("--stage4-latency-candidate-prepare", action="store_true")
    parser.add_argument("--stage4-latency-finalize", action="store_true")
    parser.add_argument("--stage4-vector-runtime-calibrate", action="store_true")
    parser.add_argument("--stage4-vector-runtime-followup", action="store_true")
    parser.add_argument("--stage4-latency-shared-runtime-prepare", action="store_true")
    parser.add_argument("--stage4-latency-real-scale", action="store_true")
    parser.add_argument("--stage4-runtime-implementation-probe", action="store_true")
    parser.add_argument("--stage4-runtime-implementation-assess", action="store_true")
    parser.add_argument("--stage4-semantic-model-screen", action="store_true")
    parser.add_argument("--stage4-hierarchical-feasibility", action="store_true")
    parser.add_argument("--stage4-latency-comparability-audit", action="store_true")
    parser.add_argument("--stage4-latency-system-budget", action="store_true")
    parser.add_argument("--stage4-latency-system-budget-finalize", action="store_true")
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
    parser.add_argument("--persistent-root", type=Path)
    parser.add_argument("--baseline-binary", type=Path)
    parser.add_argument("--baseline-embedding", type=Path)
    parser.add_argument("--runtime-implementation-root", type=Path)
    parser.add_argument("--runtime-archive", type=Path)
    parser.add_argument("--runtime-implementation")
    parser.add_argument("--runtime-probe-batch", choices=["initial", "batch2"], default="initial")
    parser.add_argument("--runtime-probe-root", type=Path)
    parser.add_argument("--semantic-model-root", type=Path)
    parser.add_argument("--preparation-receipt", type=Path)
    parser.add_argument("--candidate-preparation-receipt", type=Path)
    parser.add_argument("--superseded-performance-result", type=Path)
    parser.add_argument("--paired-diagnosis", type=Path)
    parser.add_argument("--contention-diagnosis", type=Path)
    parser.add_argument("--performance-result", type=Path)
    parser.add_argument("--multisource-quality-result", type=Path)
    parser.add_argument("--semantic-protection-result", type=Path)
    parser.add_argument("--previous-semantic-protection-result", type=Path)
    parser.add_argument("--fail-closed-result", type=Path)
    parser.add_argument("--previous-fail-closed-result", type=Path)
    parser.add_argument("--previous-candidate-root", type=Path)
    parser.add_argument("--final-candidate-root", type=Path)
    parser.add_argument("--generalization-result", type=Path)
    parser.add_argument("--v0-formal-run", type=Path)
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
    if args.stage4_latency_system_budget_finalize:
        required = (
            args.subject_manifest, args.execution_config, args.performance_result,
            args.development_result, args.multisource_result, args.regression_result, args.formal_state,
        )
        if any(path is None for path in required):
            raise SystemExit("系统线程预算终态必须提供候选、性能、开发、多来源、回归与正式 state")
        result = kernel_iteration_stage4_system_budget_finalize.finalize(
            HERE, args.output, args.subject_manifest, args.execution_config,
            args.performance_result, args.development_result, args.multisource_result,
            args.regression_result, args.formal_state, resume=args.resume,
        )
    elif args.stage4_latency_system_budget:
        required = (args.final_candidate_root, args.execution_config, args.preparation_receipt, args.persistent_root, args.formal_state)
        if any(path is None for path in required):
            raise SystemExit("系统线程预算测量必须提供候选根、执行配置、prepared data 收据与正式 state")
        result = kernel_iteration_stage4_system_budget.run(
            HERE, args.output, args.final_candidate_root, args.execution_config,
            args.preparation_receipt, args.persistent_root, args.formal_state, resume=args.resume,
        )
    elif args.stage4_latency_comparability_audit:
        if args.formal_state is None or args.v0_formal_run is None or args.performance_result is None:
            raise SystemExit("检索时延同尺审计必须提供正式 state、V0 聚合运行目录与既有真实规模结果")
        result = kernel_iteration_stage4_latency_comparability.run(
            HERE.parents[2], args.output, args.formal_state, args.v0_formal_run, args.performance_result,
        )
    elif args.stage4_hierarchical_feasibility:
        if args.formal_state is None:
            raise SystemExit("分层检索可行性判定必须提供 --formal-state")
        result = kernel_iteration_stage4_hierarchical_feasibility.run(
            HERE.parents[2], args.output, args.formal_state,
        )
    elif args.stage4_semantic_model_screen:
        if args.semantic_model_root is None or args.formal_state is None:
            raise SystemExit("语义表示筛选必须提供 --semantic-model-root 与 --formal-state")
        result = kernel_iteration_stage4_semantic_model_screening.run(
            HERE.parents[2], args.semantic_model_root, args.output, args.formal_state,
        )
    elif args.stage4_runtime_implementation_assess:
        if args.runtime_probe_root is None or args.formal_state is None:
            raise SystemExit("运行实现评估必须提供 --runtime-probe-root 与 --formal-state")
        result = kernel_iteration_stage4_runtime_implementation_assessment.assess(
            HERE.parents[2], args.runtime_probe_root, args.output, args.formal_state,
        )
    elif args.stage4_runtime_implementation_probe:
        required = (
            args.baseline_embedding, args.runtime_implementation_root,
            args.runtime_archive, args.runtime_implementation, args.formal_state,
        )
        if any(value is None for value in required):
            raise SystemExit("运行实现探针必须提供参考 embedding、实现目录、官方制品、实现名与正式 state")
        controller = (
            kernel_iteration_stage4_runtime_implementation_probe.probe
            if args.runtime_probe_batch == "initial"
            else kernel_iteration_stage4_runtime_implementation_probe_batch2.probe
        )
        result = controller(
            HERE, args.baseline_embedding, args.runtime_implementation_root,
            args.runtime_archive, args.runtime_implementation, args.output, args.formal_state,
        )
    elif args.stage4_vector_runtime_followup:
        if args.baseline_embedding is None or args.formal_state is None:
            raise SystemExit("向量后续校准必须提供 --baseline-embedding 与 --formal-state")
        result = kernel_iteration_stage4_vector_runtime_followup.run(
            HERE, args.baseline_embedding, args.output, args.formal_state,
        )
    elif args.stage4_latency_real_scale:
        required = (args.execution_config, args.preparation_receipt, args.persistent_root, args.formal_state)
        if any(path is None for path in required):
            raise SystemExit("真实规模检索必须提供执行配置、共享运行时收据、持久隔离根与正式 state")
        result = kernel_iteration_stage4_latency_real_scale.run(
            HERE, args.output, args.execution_config, args.preparation_receipt,
            args.persistent_root, args.formal_state, resume=args.resume,
        )
    elif args.stage4_latency_shared_runtime_prepare:
        if args.previous_candidate_root is None or args.final_candidate_root is None:
            raise SystemExit("共享向量运行时制品必须提供前序与最终候选根目录")
        result = kernel_iteration_stage4_latency_shared_runtime.prepare(
            HERE, args.output, args.previous_candidate_root, args.final_candidate_root, resume=args.resume,
        )
    elif args.stage4_vector_runtime_calibrate:
        if args.baseline_embedding is None:
            raise SystemExit("向量运行时校准必须提供 --baseline-embedding")
        result = kernel_iteration_stage4_vector_runtime.calibrate(args.baseline_embedding, args.output)
    elif args.stage4_latency_finalize:
        required = (
            args.subject_manifest, args.execution_config, args.performance_result,
            args.semantic_protection_result, args.previous_semantic_protection_result,
            args.fail_closed_result, args.previous_fail_closed_result,
            args.generalization_result,
            args.multisource_quality_result, args.development_result, args.regression_result,
            args.formal_state,
        )
        if any(path is None for path in required):
            raise SystemExit("检索时延终态必须提供候选、性能、三份质量结果与正式 state")
        result = kernel_iteration_stage4_latency_finalize.finalize(
            HERE, args.output, args.subject_manifest, args.execution_config,
            args.performance_result, args.semantic_protection_result,
            args.previous_semantic_protection_result, args.fail_closed_result,
            args.previous_fail_closed_result, args.generalization_result,
            args.multisource_quality_result,
            args.development_result, args.regression_result, args.formal_state,
            resume=args.resume,
        )
    elif args.stage4_latency_candidate_prepare:
        required = (
            args.subject_manifest, args.execution_config, args.preparation_receipt,
            args.persistent_root, args.formal_state,
        )
        if any(path is None for path in required):
            raise SystemExit("检索时延候选准备必须提供候选、基线 prepared data、持久隔离根与正式 state")
        result = kernel_iteration_stage4_latency_candidate_data.prepare(
            HERE, args.output, args.subject_manifest, args.execution_config,
            args.preparation_receipt, args.persistent_root, args.formal_state,
            resume=args.resume,
        )
    elif args.stage4_latency_candidate_performance:
        required = (
            args.subject_manifest, args.execution_config,
            args.baseline_subject_manifest, args.baseline_execution_config,
            args.baseline_binary, args.baseline_embedding,
            args.preparation_receipt, args.candidate_preparation_receipt,
            args.superseded_performance_result, args.formal_state,
        )
        if any(path is None for path in required):
            raise SystemExit("检索时延候选性能必须提供三代 subject/运行身份、共同 prepared data、旧诊断与正式 state")
        result = kernel_iteration_stage4_latency_candidate.run(
            HERE, args.output, args.subject_manifest, args.execution_config,
            args.baseline_subject_manifest, args.baseline_execution_config,
            args.baseline_binary, args.baseline_embedding,
            args.preparation_receipt, args.candidate_preparation_receipt,
            args.superseded_performance_result, args.formal_state,
        )
    elif args.stage4_latency_contention:
        required = (
            args.execution_config, args.baseline_binary, args.baseline_embedding,
            args.preparation_receipt, args.paired_diagnosis, args.formal_state,
        )
        if any(path is None for path in required):
            raise SystemExit("检索时延串行竞争诊断必须提供两代运行身份、prepared data、并发证据与正式 state")
        result = kernel_iteration_stage4_latency_contention.run(
            HERE, args.output, args.execution_config, args.baseline_binary,
            args.baseline_embedding, args.preparation_receipt, args.paired_diagnosis,
            args.formal_state,
        )
    elif args.stage4_latency_diagnose:
        required = (
            args.subject_manifest, args.execution_config, args.baseline_binary,
            args.baseline_embedding, args.preparation_receipt, args.formal_state,
        )
        if any(path is None for path in required):
            raise SystemExit("检索时延诊断必须提供当前 V2、V0 二进制/向量、prepared-data 收据与正式 state")
        result = kernel_iteration_stage4_latency_performance.run(
            HERE, args.output, args.subject_manifest, args.execution_config,
            args.baseline_binary, args.baseline_embedding, args.preparation_receipt,
            args.formal_state,
        )
    elif args.stage4_latency_prepare:
        required = (args.execution_config, args.baseline_binary, args.baseline_embedding, args.persistent_root, args.formal_state)
        if any(path is None for path in required):
            raise SystemExit("检索时延准备必须提供当前配置、V0 二进制/向量、持久隔离根与正式 state 只读基线")
        result = kernel_iteration_stage4_latency_data.prepare(
            HERE, args.output, args.execution_config, args.baseline_binary, args.baseline_embedding, args.persistent_root,
            args.formal_state, resume=args.resume,
        )
    elif args.stage4_multisource_finalize:
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
    elif args.prepare_v2_system_budget_candidate:
        if args.execution_config is None:
            raise SystemExit("V2 系统线程预算候选准备必须提供当前非正式 --execution-config")
        result = kernel_iteration_candidate_system_budget.prepare(
            HERE, args.output, args.execution_config, resume=args.resume,
        )
    elif args.prepare_v2_latency_candidate:
        if args.execution_config is None:
            raise SystemExit("V2 检索时延候选准备必须提供当前非正式 --execution-config")
        result = kernel_iteration_candidate_latency.prepare(
            HERE, args.output, args.execution_config, resume=args.resume,
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
