#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import kernel_iteration_blind_suite as blind_suite
import kernel_iteration_evidence as evidence
import kernel_iteration_manifest
import kernel_iteration_validation as validation


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Ownward parameterized kernel-iteration lifecycle")
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--iteration-manifest", type=Path)
    action = parser.add_mutually_exclusive_group(required=True)
    action.add_argument("--subject", choices=["v0", "current-product"])
    action.add_argument("--subject-manifest", type=Path)
    action.add_argument("--runtime-state", type=Path)
    action.add_argument("--prepare-materials", type=Path)
    action.add_argument("--compare-left", type=Path)
    action.add_argument("--blind-suite-qualify-admission", action="store_true")
    action.add_argument("--blind-suite-prepare", action="store_true")
    action.add_argument("--blind-suite-plan-identity")
    action.add_argument("--blind-suite-evaluation-batch", type=Path)
    action.add_argument("--blind-suite-run-plan-identity")
    action.add_argument("--blind-suite-inspect", action="store_true")
    action.add_argument("--blind-suite-retire", action="store_true")
    action.add_argument("--blind-suite-rejudge", type=Path)
    parser.add_argument("--compare-right", type=Path)
    parser.add_argument("--evidence-type", default="identity-calibration")
    parser.add_argument("--input-manifest", type=Path)
    parser.add_argument("--execution-config", type=Path)
    parser.add_argument("--write-input", type=Path)
    parser.add_argument("--formal-state", type=Path)
    parser.add_argument("--candidate-result", type=Path)
    parser.add_argument("--noncandidate-diagnostic", action="store_true")
    parser.add_argument("--contract", type=Path)
    parser.add_argument("--blind-suite-vault", type=Path)
    parser.add_argument("--blind-suite-version")
    parser.add_argument("--blind-suite-identity")
    parser.add_argument("--blind-suite-level", type=int, choices=(5, 15, 25, 50))
    parser.add_argument("--blind-suite-previous-plan-identity")
    parser.add_argument("--blind-suite-measurement-rejudgment", type=Path)
    parser.add_argument("--gate-seed")
    parser.add_argument("--resume", action="store_true")
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    manifest = kernel_iteration_manifest.load(HERE, args.iteration_manifest)
    version = str(manifest["major_version"])
    if args.blind_suite_version is not None and args.blind_suite_version.lower() != version:
        raise SystemExit("blind-suite version does not match the selected iteration manifest")

    if args.runtime_state is not None:
        _forbid(args.input_manifest is not None or args.evidence_type != "identity-calibration", "runtime calibration accepts no evidence input")
        result = evidence.calibrate_runtime(HERE, args.output, args.runtime_state, contract_path=args.contract, resume=args.resume)
    elif args.compare_left is not None:
        _need(args.compare_right, "comparison requires --compare-right")
        result = validation.compare_execution_results(args.compare_left, args.compare_right)
    elif args.prepare_materials is not None:
        _need(args.execution_config, "material preparation requires --execution-config")
        _need(args.write_input, "material preparation requires --write-input")
        result = validation.build_input_manifest(
            HERE, args.prepare_materials, args.execution_config,
            args.evidence_type, args.write_input,
        )
    elif args.blind_suite_qualify_admission:
        _need(args.execution_config, "blind-suite admission qualification requires --execution-config")
        _need(args.formal_state, "blind-suite admission qualification requires --formal-state")
        result = blind_suite.qualify_admission(HERE, args.output, args.execution_config, args.formal_state)
    elif args.blind_suite_prepare:
        _need(args.blind_suite_vault, "blind-suite preparation requires --blind-suite-vault")
        _need(args.execution_config, "blind-suite preparation requires --execution-config")
        _need(args.formal_state, "blind-suite preparation requires --formal-state")
        result = blind_suite.prepare(
            HERE, args.output, args.blind_suite_vault, args.execution_config, args.formal_state,
            major_version=version, seed=args.gate_seed, resume=args.resume,
        )
    elif args.blind_suite_plan_identity is not None:
        _forbid(not args.resume, "sealed-suite recovery requires --resume")
        result = blind_suite.resume_by_plan_identity(HERE, args.output, args.blind_suite_plan_identity)
    elif args.blind_suite_evaluation_batch is not None:
        for value, message in (
            (args.blind_suite_vault, "partition execution requires --blind-suite-vault"),
            (args.blind_suite_identity, "partition execution requires --blind-suite-identity"),
            (args.blind_suite_level, "partition execution requires --blind-suite-level"),
            (args.formal_state, "partition execution requires --formal-state"),
        ):
            _need(value, message)
        result = blind_suite.run_partition(
            HERE, args.output, args.blind_suite_vault, args.blind_suite_evaluation_batch, args.formal_state,
            major_version=version, suite_identity=args.blind_suite_identity, level=args.blind_suite_level,
            previous_plan_identity=args.blind_suite_previous_plan_identity,
            measurement_rejudgment_path=args.blind_suite_measurement_rejudgment,
            resume=args.resume,
        )
    elif args.blind_suite_run_plan_identity is not None:
        _forbid(not args.resume, "partition recovery requires --resume")
        result = blind_suite.resume_partition_by_plan_identity(HERE, args.output, args.blind_suite_run_plan_identity)
    elif args.blind_suite_inspect:
        _need(args.blind_suite_vault, "blind-suite inspection requires --blind-suite-vault")
        result = blind_suite.inspect_suite(HERE, args.output, args.blind_suite_vault, major_version=version, suite_identity=args.blind_suite_identity)
    elif args.blind_suite_retire:
        _need(args.blind_suite_vault, "blind-suite retirement requires --blind-suite-vault")
        _need(args.blind_suite_identity, "blind-suite retirement requires --blind-suite-identity")
        result = blind_suite.retire(HERE, args.output, args.blind_suite_vault, major_version=version, suite_identity=args.blind_suite_identity)
    elif args.blind_suite_rejudge is not None:
        result = blind_suite.rejudge_partition(HERE, args.output, args.blind_suite_rejudge)
    else:
        if args.execution_config is not None:
            _need(args.input_manifest, "end-to-end execution requires --input-manifest")
            result = validation.execute_prepared_evidence(
                HERE, args.output, args.execution_config, selector=args.subject,
                subject_manifest=args.subject_manifest, evidence_type=args.evidence_type,
                input_manifest=args.input_manifest, candidate_result_path=args.candidate_result,
                noncandidate_diagnostic=args.noncandidate_diagnostic, resume=args.resume,
            )
        else:
            result = evidence.run(
                HERE, args.output, selector=args.subject, subject_manifest=args.subject_manifest,
                evidence_type=args.evidence_type, input_manifest=args.input_manifest,
                contract_path=args.contract, resume=args.resume,
            )
    print(json.dumps(result, ensure_ascii=False))


def _need(value: object, message: str) -> None:
    if value is None or value is False:
        raise SystemExit(message)


def _forbid(condition: bool, message: str) -> None:
    if condition:
        raise SystemExit(message)


if __name__ == "__main__":
    main()
