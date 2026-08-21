#!/usr/bin/env python3
from __future__ import annotations

import argparse
import copy
from datetime import datetime, timezone
import os
from pathlib import Path
import sys
import time


HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from common import (  # noqa: E402
    dataset_implementation_sha256,
    load_json,
    require,
    sha256,
    validate_protocol,
    write_json,
)
import verify  # noqa: E402


REPORT_SCHEMA = "ownward.dynamic-data-preflight/v1"


def preflight_protocol(protocol: dict) -> dict:
    value = copy.deepcopy(protocol)
    generation = value["generation"]
    class_count = len(generation["task_classes"])
    batch_size = int(generation["validation_scenarios_per_batch"])
    generation["generated_scenarios"] = class_count * batch_size
    generation["minimum_valid_scenarios"] = class_count
    generation["minimum_scenarios_per_task_class"] = 1
    value["statistics"]["basis"] = (
        "Preflight executes one formal-sized validation batch per frozen task class "
        "and requires at least one independently valid scenario from every class."
    )
    validate_protocol(value)
    return value


def _parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--protocol", dest="protocol_path", type=Path, default=HERE / "protocol.json")
    parser.add_argument("--evidence-dir", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--codex-binary", type=Path, required=True)
    parser.add_argument("--codex-auth-file", type=Path, required=True)
    parser.add_argument("--resume", action="store_true")
    return parser.parse_args()


def main() -> None:
    args = _parse_args()
    args.protocol_path = args.protocol_path.resolve()
    args.evidence_dir = args.evidence_dir.resolve()
    args.output = args.output.resolve()
    args.codex_binary = args.codex_binary.resolve()
    args.codex_auth_file = args.codex_auth_file.resolve()
    for path, label in (
        (args.protocol_path, "dynamic protocol"),
        (args.codex_binary, "Codex binary"),
        (args.codex_auth_file, "Codex auth file"),
    ):
        require(path.is_file(), f"{label} does not exist: {path}")
    require(args.codex_binary.suffix.lower() not in {".ps1", ".cmd", ".bat", ".js"}, "preflight must bind the native Codex executable")

    formal_protocol_path = args.protocol_path
    formal_protocol = load_json(formal_protocol_path)
    validate_protocol(formal_protocol)
    require(
        verify._binary_text(args.codex_binary, "--version") == formal_protocol["runtime"]["codex_cli_version"],
        "Codex CLI version differs from the frozen protocol",
    )
    if args.output.exists():
        require(args.resume, "preflight report already exists; use --resume")
        report = load_json(args.output)
        require(report.get("schema") == REPORT_SCHEMA and report.get("passed") is True, "preflight report is invalid")
        require(report.get("formal_protocol_sha256") == sha256(formal_protocol_path), "preflight protocol binding changed")
        require(report.get("dataset_implementation_sha256") == dataset_implementation_sha256(), "preflight implementation binding changed")
        require(report.get("codex_binary_sha256") == sha256(args.codex_binary), "preflight Codex binding changed")
        return
    if args.evidence_dir.exists():
        require(args.resume, "preflight evidence directory already exists; use --resume")
    else:
        args.evidence_dir.mkdir(parents=True)

    derived = preflight_protocol(formal_protocol)
    derived_path = args.evidence_dir / "preflight-protocol.json"
    if derived_path.exists():
        require(args.resume and load_json(derived_path) == derived, "preflight protocol changed")
    else:
        write_json(derived_path, derived)
    args.protocol_path = derived_path
    args.protocol = derived
    args.candidate = f"preflight-{sha256(formal_protocol_path)}"

    started = time.perf_counter()
    dataset, paths = verify._prepare_dataset(args, derived, os.environ.copy())
    elapsed = time.perf_counter() - started
    counts = {task_class: 0 for task_class in formal_protocol["generation"]["task_classes"]}
    for scenario in dataset["valid_scenarios"]:
        counts[str(scenario["truth"]["task_class"])] += 1
    passed = all(value >= 1 for value in counts.values())
    require(passed, f"dynamic data preflight failed: {counts}")

    report = {
        "schema": REPORT_SCHEMA,
        "passed": True,
        "measured_at": datetime.now(timezone.utc).isoformat(),
        "formal_protocol_sha256": sha256(formal_protocol_path),
        "dataset_implementation_sha256": dataset_implementation_sha256(),
        "codex_binary_sha256": sha256(args.codex_binary),
        "codex_cli_version": formal_protocol["runtime"]["codex_cli_version"],
        "models": {
            "generator": formal_protocol["models"]["generator"],
            "validator": formal_protocol["models"]["validator"],
        },
        "valid_scenarios_by_task_class": counts,
        "rejected_scenarios": len(dataset["rejected_scenarios"]),
        "unvalidated_reserve_scenarios": (
            derived["generation"]["generated_scenarios"]
            - len(dataset["valid_scenarios"])
            - len(dataset["reserve_scenarios"])
            - len(dataset["rejected_scenarios"])
        ),
        "dataset_sha256": sha256(paths["dataset"]),
        "elapsed_seconds": elapsed,
    }
    write_json(args.output, report)


if __name__ == "__main__":
    main()
