from __future__ import annotations

from datetime import datetime, timezone
import hashlib
import json
import math
import os
from pathlib import Path
import shutil
import time
from typing import Any

import process_control


OFFICIAL_REVISION = "9e0b455f4ef0e2ab8f2e582289761153549043fc"
OFFICIAL_DATA_SHA256 = "d6f21ea9d60a0d56f34a05b609c79c88a451d2ae03597821ea3d5a9678c3a442"


class CommunityExecutionError(ValueError):
    pass


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise CommunityExecutionError(message)


def _load(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    _require(isinstance(value, dict), f"JSON document is not an object: {path}")
    return value


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _remaining_wall_seconds(run_dir: Path, maximum: float) -> float:
    _require(math.isfinite(maximum) and maximum > 0, "LongMemEval-S wall-clock budget is invalid")
    clock_path = run_dir / "wall-clock.json"
    if not clock_path.is_file():
        return maximum
    try:
        clock = _load(clock_path)
    except (OSError, json.JSONDecodeError) as error:
        raise CommunityExecutionError("LongMemEval-S wall-clock checkpoint is invalid") from error
    _require(clock.get("schema") == "ownward.longmemeval-s-wall-clock/v1", "LongMemEval-S wall-clock checkpoint is invalid")
    elapsed_value = clock.get("elapsed_seconds")
    _require(isinstance(elapsed_value, (int, float)) and not isinstance(elapsed_value, bool), "LongMemEval-S wall-clock checkpoint is invalid")
    elapsed = float(elapsed_value)
    _require(math.isfinite(elapsed) and elapsed >= 0, "LongMemEval-S wall-clock checkpoint is invalid")
    remaining = maximum - elapsed
    _require(remaining > 0, "LongMemEval-S frozen wall-clock budget is exhausted")
    return remaining


def _write_wall_seconds(run_dir: Path, elapsed: float) -> None:
    clock_path = run_dir / "wall-clock.json"
    temporary = run_dir / ".wall-clock.suite.tmp"
    temporary.write_text(json.dumps({
        "schema": "ownward.longmemeval-s-wall-clock/v1",
        "elapsed_seconds": elapsed,
    }, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    temporary.replace(clock_path)


def _run_with_wall_budget(command: list[str], *, cwd: Path, run_dir: Path, maximum: float) -> Any:
    remaining = _remaining_wall_seconds(run_dir, maximum)
    consumed = maximum - remaining
    started = time.monotonic()
    exhausted = False
    try:
        return process_control.run(command, cwd=cwd, timeout=remaining)
    except process_control.ProcessTimeout as error:
        exhausted = True
        raise CommunityExecutionError("LongMemEval-S exceeded its frozen wall-clock budget and was stopped") from error
    finally:
        elapsed = maximum if exhausted else min(maximum, consumed + max(0.0, time.monotonic() - started))
        _write_wall_seconds(run_dir, elapsed)


def _persistent_layout(manifest_path: Path) -> tuple[dict[str, Any], Path, Path, Path]:
    manifest = _load(manifest_path)
    _require(manifest.get("schema") == "ownward.longmemeval-s-environment/v1", "LongMemEval-S environment manifest is invalid")
    _require(manifest.get("official", {}).get("code_revision") == OFFICIAL_REVISION, "LongMemEval-S source revision changed")
    _require(manifest.get("integrity", {}).get("data_sha256") == OFFICIAL_DATA_SHA256, "LongMemEval-S data changed")
    layout = manifest.get("layout")
    _require(isinstance(layout, dict), "LongMemEval-S environment layout is missing")
    python_root = Path(layout["python"]).resolve()
    python = python_root / ("Scripts/python.exe" if os.name == "nt" else "bin/python")
    data = Path(layout["data"]).resolve()
    runs = Path(layout["runs"]).resolve()
    _require(python.is_file() and data.is_file() and runs.is_dir(), "LongMemEval-S persistent environment is incomplete")
    return manifest, python, data, runs


def _run_complete(path: Path, binding: dict[str, str]) -> dict[str, Any] | None:
    identity_path = path / "identity.json"
    report_path = path / "report.json"
    package_path = path / "submission.zip"
    evaluation_path = path / "official-evaluation.jsonl"
    checkpoint_path = path / "checkpoint-manifest.json"
    diagnostics_path = path / "diagnostics.jsonl"
    diagnostic_summary_path = path / "diagnostic-summary.json"
    if not all(item.is_file() for item in (
        identity_path, report_path, package_path, evaluation_path, checkpoint_path,
        diagnostics_path, diagnostic_summary_path,
    )):
        return None
    try:
        identity, report = _load(identity_path), _load(report_path)
    except (OSError, json.JSONDecodeError, CommunityExecutionError):
        return None
    expected = {
        "candidate": binding["candidate"], "binary_sha256": binding["binary_sha256"],
        "environment_sha256": binding["environment_sha256"], "input_manifest_sha256": binding["input_manifest_sha256"],
        "tool_sha256": binding["tool_sha256"], "formal": True,
        "profile": "Ownward LongMemEval-S Production Profile",
    }
    if any(identity.get(name) != value for name, value in expected.items()):
        return None
    if any(report.get(name) != value for name, value in expected.items() if name != "formal") or report.get("formal") is not True:
        return None
    if (
        report.get("profile") != "Ownward LongMemEval-S Production Profile"
        or report.get("questions") != 500
        or report.get("submission_sha256") != _sha256(package_path)
        or report.get("execution", {}).get("complete") is not True
        or report.get("execution", {}).get("protocol_valid") is not True
        or report.get("execution", {}).get("evidence_complete") is not True
        or report.get("quality", {}).get("assessment_status") != "not_determined"
        or report.get("passed") is not False
    ):
        return None
    return report


def artifact_paths(config: dict[str, Any]) -> list[Path]:
    workspace = Path(config["_workspace"]).resolve()
    evidence = workspace / "evidence" / "community"
    _require(evidence.is_relative_to(workspace) and evidence != workspace, "community evidence must stay inside the acceptance workspace")
    return [evidence]


def execute(
    suite_root: Path,
    contract: dict[str, Any],
    binding: dict[str, str],
    config: dict[str, Any],
    *,
    resume: bool,
) -> dict[str, Any]:
    started_at = datetime.now(timezone.utc).isoformat()
    manifest_path = Path(config["environment_manifest"]).resolve()
    _, python, dataset, runs = _persistent_layout(manifest_path)
    protocol = Path(config["protocol"]).resolve()
    adapter = (suite_root.parents[1] / "longmemeval_s" / "run.py").resolve()
    binary = Path(config["binary"]).resolve()
    embedding = Path(config["embedding_bundle_dir"]).resolve()
    _require(all(path.is_file() for path in (protocol, adapter, binary)), "community execution input is missing")
    _require(embedding.is_dir(), "community embedding bundle is missing")
    _require(_sha256(binary) == binding["binary_sha256"], "community candidate binary changed")
    run_dir = Path(config["output_dir"]).resolve()
    _require(run_dir.is_relative_to(runs) and run_dir != runs, "community run must stay under the persistent runs root")
    existing = _run_complete(run_dir, binding)
    if existing is None:
        if run_dir.exists() and any(run_dir.iterdir()):
            _require(resume, "community run is incomplete; use --resume")
        run_dir.mkdir(parents=True, exist_ok=True)
        command = [
            str(python), str(adapter), "run", "--environment-manifest", str(manifest_path), "--protocol", str(protocol),
            "--dataset", str(dataset), "--output-dir", str(run_dir), "--ownward-binary", str(binary),
            "--embedding-bundle-dir", str(embedding),
            "--codex-binary", str(config["codex_binary"]), "--codex-auth-file", str(config["codex_auth_file"]),
            "--candidate", binding["candidate"], "--environment-sha256", binding["environment_sha256"],
            "--input-manifest-sha256", binding["input_manifest_sha256"], "--tool-sha256", binding["tool_sha256"],
        ]
        representation = config.get("semantic_representation")
        if representation is not None:
            _require(isinstance(representation, dict) and isinstance(representation.get("manifest"), str), "community semantic representation declaration is invalid")
            command.extend(["--semantic-representation-manifest", str(Path(representation["manifest"]).resolve())])
        if resume:
            command.append("--resume")
        maximum = float(contract["evidence_layers"]["community"]["expected_wall_seconds"]["max"])
        completed = _run_with_wall_budget(command, cwd=suite_root.parents[2], run_dir=run_dir, maximum=maximum)
        _require(completed.returncode == 0, f"LongMemEval-S adapter failed: {completed.stderr[-3000:]}")
        existing = _run_complete(run_dir, binding)
    else:
        _require(resume, "community run already exists; use --resume")
    _require(existing is not None, "LongMemEval-S did not produce a complete run")

    workspace = Path(config["_workspace"]).resolve()
    evidence = workspace / "evidence" / "community"
    evidence.mkdir(parents=True, exist_ok=True)
    for name in (
        "identity.json", "report.json", "hypotheses.jsonl", "official-evaluation.jsonl",
        "diagnostics.jsonl", "diagnostic-summary.json", "checkpoint-manifest.json", "submission.zip",
    ):
        source = run_dir / name
        _require(source.is_file(), f"LongMemEval-S evidence is missing: {name}")
        temporary = evidence / f".{name}.tmp"
        shutil.copyfile(source, temporary)
        temporary.replace(evidence / name)
    adapter_report = existing
    definition = contract["evidence_layers"]["community"]
    wall_seconds = float(adapter_report["cost"]["wall_seconds"])
    within_budget = wall_seconds <= float(definition["expected_wall_seconds"]["max"])
    profile_complete = adapter_report.get("profile") == definition["profile"]
    diagnostics = adapter_report.get("diagnostics")
    diagnostics_complete = isinstance(diagnostics, dict) and diagnostics.get("questions") == definition["questions"]
    adapter_execution = adapter_report.get("execution")
    execution_complete = (
        isinstance(adapter_execution, dict)
        and adapter_execution.get("complete") is True
        and adapter_execution.get("protocol_valid") is True
        and adapter_execution.get("evidence_complete") is True
        and adapter_execution.get("within_wall_boundary") is True
    )
    quality_assessment = definition["quality_assessment"]
    report = {
        "schema": definition["output_schema"], "suite_version": contract["suite_version"],
        "official_version": definition["version"], "profile": definition["profile"],
        "candidate": binding["candidate"], "binary_sha256": binding["binary_sha256"],
        "environment": {"sha256": binding["environment_sha256"]}, "inputs": {"sha256": binding["input_manifest_sha256"]},
        "capabilities": adapter_report["capabilities"],
        "benchmark": {"name": definition["benchmark"], "questions": adapter_report["questions"], "question_types": sorted(adapter_report["categories"]), "complete": adapter_report["questions"] == definition["questions"]},
        "quality": {
            "accuracy": adapter_report["accuracy"],
            "categories": adapter_report["categories"],
            "comparison_policy": definition["comparison_policy"],
            "hard_accuracy_threshold": None,
            "score_complete": True,
            "assessment_status": quality_assessment["status"],
            "assessment_basis": quality_assessment["basis"],
            "first_version_condition_satisfied": quality_assessment["first_version_condition_satisfied"],
            "passed": None,
        },
        "execution": {
            "complete": execution_complete,
            "protocol_valid": bool(isinstance(adapter_execution, dict) and adapter_execution.get("protocol_valid") is True),
            "evidence_complete": bool(isinstance(adapter_execution, dict) and adapter_execution.get("evidence_complete") is True),
            "passed": bool(execution_complete and profile_complete and diagnostics_complete and within_budget),
        },
        "retrieval": adapter_report["retrieval"],
        "cost": {**adapter_report["cost"], "max_wall_seconds": definition["expected_wall_seconds"]["max"], "within_budget": within_budget},
        "diagnostics": {
            **(diagnostics if isinstance(diagnostics, dict) else {}),
            "records_sha256": _sha256(run_dir / "diagnostics.jsonl"),
            "summary_sha256": _sha256(run_dir / "diagnostic-summary.json"),
            "complete": diagnostics_complete,
        },
        "submission": {
            "package_sha256": _sha256(run_dir / "submission.zip"),
            "official_evaluation_sha256": _sha256(run_dir / "official-evaluation.jsonl"),
            "hypotheses_sha256": _sha256(run_dir / "hypotheses.jsonl"),
            "diagnostics_sha256": _sha256(run_dir / "diagnostics.jsonl"),
            "diagnostic_summary_sha256": _sha256(run_dir / "diagnostic-summary.json"),
            "checkpoint_manifest_sha256": _sha256(run_dir / "checkpoint-manifest.json"),
        },
        "completion": {"status": "not_satisfied", "reason": "community-quality-not-determined"},
        "passed": False,
        "started_at": started_at, "finished_at": datetime.now(timezone.utc).isoformat(),
    }
    return report
