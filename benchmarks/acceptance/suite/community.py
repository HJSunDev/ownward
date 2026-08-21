from __future__ import annotations

from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import sys
import time
from typing import Any

import process_control


OFFICIAL_REVISION = "2cc8c540bdb87fe6761629b585e727e1c4704520"


class CommunityExecutionError(ValueError):
    pass


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def load_json(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    _require(isinstance(value, dict), f"JSON document is not an object: {path}")
    return value


def build_submission_code(memory_path: Path, trajectory_path: Path, output: Path) -> Path:
    memory = memory_path.read_text(encoding="utf-8")
    trajectory = trajectory_path.read_text(encoding="utf-8")
    marker = "from ownward_trajectory import trajectory_documents\n"
    _require(marker in memory, "Ownward LongMemEval adapter dependency marker is missing")
    trajectory = trajectory.replace("from __future__ import annotations\n\n", "", 1)
    trajectory = trajectory.replace("from typing import Any\n\n", "", 1)
    helper = "# Frozen trajectory normalization used by this submission.\n" + trajectory.strip() + "\n"
    combined = memory.replace(marker, helper + "\n", 1)
    compile(combined, str(output), "exec")
    output.parent.mkdir(parents=True, exist_ok=True)
    temporary = output.with_suffix(output.suffix + ".tmp")
    temporary.write_text(combined, encoding="utf-8")
    temporary.replace(output)
    return output


def _run(command: list[str], *, cwd: Path, timeout: float) -> None:
    _require(timeout > 0, "LongMemEval-V2 total wall-clock budget exhausted")
    try:
        completed = process_control.run(command, cwd=cwd, timeout=timeout)
    except process_control.ProcessTimeout as error:
        raise CommunityExecutionError("LongMemEval-V2 exceeded its total wall-clock budget and was stopped") from error
    _require(completed.returncode == 0, f"LongMemEval-V2 command failed: {completed.stderr[-3000:]}")


def _value(arguments: list[str], name: str) -> str:
    try:
        return arguments[arguments.index(name) + 1]
    except (ValueError, IndexError) as error:
        raise CommunityExecutionError(f"LongMemEval-V2 arguments require {name}") from error


def _verify_official(repo: Path) -> None:
    revision = subprocess.run(["git", "rev-parse", "HEAD"], cwd=repo, check=False, capture_output=True, text=True, encoding="utf-8")
    _require(revision.returncode == 0 and revision.stdout.strip() == OFFICIAL_REVISION, "LongMemEval-V2 official revision changed")
    status = subprocess.run(["git", "status", "--porcelain"], cwd=repo, check=False, capture_output=True, text=True, encoding="utf-8")
    _require(status.returncode == 0 and not status.stdout.strip(), "LongMemEval-V2 official checkout is not clean")


def _domain_complete(path: Path, domain: str, binding: dict[str, str]) -> dict[str, Any] | None:
    metrics_path = path / "aggregated_metrics.json"
    run_args_path = path / "run_args.json"
    if not metrics_path.is_file() or not run_args_path.is_file():
        return None
    try:
        run_args = load_json(run_args_path)
        metrics = load_json(metrics_path)
    except (OSError, json.JSONDecodeError, CommunityExecutionError):
        return None
    evidence = run_args.get("ownward_evidence")
    if not isinstance(evidence, dict):
        return None
    expected = {
        "candidate": binding["candidate"],
        "release_binary_sha256": binding["binary_sha256"],
        "environment_sha256": binding["environment_sha256"],
        "input_manifest_sha256": binding["input_manifest_sha256"],
        "tool_sha256": binding["tool_sha256"],
        "query_mode": "codex",
        "official_revision": OFFICIAL_REVISION,
    }
    if any(evidence.get(name) != value for name, value in expected.items()):
        return None
    return metrics


def _frontier_eligible(lafs: dict[str, Any], point: dict[str, Any]) -> bool:
    gain = float(lafs.get("lafs_gain", -1))
    if gain > 0:
        return True
    accuracy = float(point.get("lafs_accuracy_percentage_points", -1))
    latency = float(point.get("lafs_latency_seconds", -1))
    references = lafs.get("reference_frontier")
    if not isinstance(references, list):
        return False
    return any(
        isinstance(reference, dict)
        and float(reference.get("accuracy", -2)) == accuracy
        and float(reference.get("latency_seconds", -2)) == latency
        for reference in references
    )


def artifact_paths(config: dict[str, Any]) -> list[Path]:
    workspace = Path(config["_workspace"]).resolve()
    paths: list[Path] = []
    for domain in ("web", "enterprise"):
        arguments = config.get(f"{domain}_arguments")
        _require(isinstance(arguments, list), f"{domain} official arguments are invalid")
        path = Path(_value(arguments, "--output-dir")).resolve()
        _require(path.is_relative_to(workspace) and path != workspace, f"{domain} evidence must stay inside the acceptance workspace")
        paths.append(path)
    output_root = Path(config["submission_root"]).resolve()
    _require(output_root.is_relative_to(workspace) and output_root != workspace, "submission evidence must stay inside the acceptance workspace")
    submission_name = str(config.get("submission_name", "ownward-active-v1"))
    paths.extend([
        output_root / submission_name,
        output_root / f"{submission_name}.tar.gz",
        output_root / ".submission-source" / "ownward_memory.py",
    ])
    return paths


def execute(
    suite_root: Path,
    contract: dict[str, Any],
    binding: dict[str, str],
    config: dict[str, Any],
    *,
    resume: bool,
) -> dict[str, Any]:
    started_at = datetime.now(timezone.utc).isoformat()
    official_repo = Path(config["official_repo"]).resolve()
    _verify_official(official_repo)
    binary = Path(config["binary"]).resolve()
    codex_binary = Path(config["codex_binary"]).resolve()
    codex_auth_file = Path(config["codex_auth_file"]).resolve()
    embedding_bundle_dir = Path(config["embedding_bundle_dir"]).resolve()
    for path, label in ((binary, "Ownward binary"), (codex_binary, "Codex binary"), (codex_auth_file, "Codex auth")):
        _require(path.is_file(), f"{label} does not exist: {path}")
    _require(embedding_bundle_dir.is_dir(), "embedding bundle directory does not exist")
    _require(sha256(binary) == binding["binary_sha256"], "community binary binding changed")
    wrapper = (suite_root.parents[1] / "longmemeval_v2" / "run.py").resolve()
    runs: dict[str, tuple[Path, dict[str, Any]]] = {}
    maximum = float(contract["evidence_layers"]["community"]["expected_wall_seconds"]["max"])
    deadline = time.monotonic() + maximum
    remaining = lambda: deadline - time.monotonic()
    workspace = Path(config["_workspace"]).resolve()
    for domain in ("web", "enterprise"):
        arguments = config.get(f"{domain}_arguments")
        _require(isinstance(arguments, list) and all(isinstance(item, str) for item in arguments), f"{domain} official arguments are invalid")
        _require(_value(arguments, "--domain") == domain, f"{domain} arguments select another domain")
        run_dir = Path(_value(arguments, "--output-dir")).resolve()
        _require(run_dir.drive and run_dir.drive.lower() != Path.home().drive.lower(), f"{domain} run must stay off the system drive")
        _require(run_dir.is_relative_to(workspace) and run_dir != workspace, f"{domain} run must stay inside the acceptance workspace")
        existing = _domain_complete(run_dir, domain, binding)
        if existing is None:
            if run_dir.exists() and any(run_dir.iterdir()):
                _require(resume, f"{domain} run is incomplete; use --resume")
                _require(run_dir.is_relative_to(workspace) and run_dir != workspace, f"refusing to clear {domain} outside the acceptance workspace")
                shutil.rmtree(run_dir)
            command = [
                sys.executable, str(wrapper), "--official-repo", str(official_repo),
                "--ownward-binary", str(binary), "--codex-binary", str(codex_binary),
                "--codex-auth-file", str(codex_auth_file), "--embedding-bundle-dir", str(embedding_bundle_dir),
                "--candidate", binding["candidate"],
                "--environment-sha256", binding["environment_sha256"],
                "--input-manifest-sha256", binding["input_manifest_sha256"],
                "--tool-sha256", binding["tool_sha256"],
                *arguments,
            ]
            _run(command, cwd=suite_root.parents[2], timeout=remaining())
            existing = _domain_complete(run_dir, domain, binding)
        else:
            _require(resume, f"{domain} run already exists; use --resume")
        _require(existing is not None, f"{domain} run did not produce complete evidence")
        runs[domain] = (run_dir, existing)

    submission_name = str(config.get("submission_name", "ownward-active-v1"))
    output_root = Path(config["submission_root"]).resolve()
    _require(output_root.drive and output_root.drive.lower() != Path.home().drive.lower(), "official submission workspace must stay off the system drive")
    _require(output_root.is_relative_to(workspace) and output_root != workspace, "official submission workspace must stay inside the acceptance workspace")
    point_name = "active"
    step1 = official_repo / "leaderboard" / "build_submission_step_1_single_operating_point.py"
    _run([
        sys.executable, str(step1), str(runs["web"][0]), str(runs["enterprise"][0]),
        submission_name, point_name, "small", "--method", "ownward", "--output-root", str(output_root), "--force",
    ], cwd=official_repo, timeout=min(1800, remaining()))
    point = output_root / submission_name / "operating_points" / point_name
    system_description = (suite_root.parents[1] / "longmemeval_v2" / "SYSTEM_DESCRIPTION.md").resolve()
    adapter_root = (suite_root.parents[1] / "longmemeval_v2").resolve()
    code_file = build_submission_code(
        adapter_root / "ownward_memory.py",
        adapter_root / "ownward_trajectory.py",
        output_root / ".submission-source" / "ownward_memory.py",
    )
    step2 = official_repo / "leaderboard" / "build_submission_step_2_build_package.py"
    _run([
        sys.executable, str(step2), submission_name, str(system_description), str(code_file), str(point),
        "--output-root", str(output_root), "--force",
    ], cwd=official_repo, timeout=min(1800, remaining()))
    overview_path = output_root / submission_name / "submission_overview.json"
    overview = load_json(overview_path)
    _require(str(overview.get("method", "")).lower() == "ownward" and overview.get("tier") == "small", "official package identity is invalid")
    points = overview.get("operating_points")
    _require(isinstance(points, list) and len(points) == 1 and points[0].get("name") == point_name, "official package changed the operating point")
    point = points[0]
    lafs = overview.get("lafs")
    _require(isinstance(lafs, dict) and float(lafs.get("lafs_gain", -1)) >= 0, "official LAFS gain is negative or missing")
    frontier_eligible = _frontier_eligible(lafs, point)
    archive = output_root / f"{submission_name}.tar.gz"
    _require(archive.is_file(), "official submission archive is missing")
    domains = {
        name: {"passed": True, "metrics_sha256": sha256(path / "aggregated_metrics.json")}
        for name, (path, _) in runs.items()
    }
    report = {
        "schema": contract["evidence_layers"]["community"]["output_schema"],
        "suite_version": contract["suite_version"], "official_version": contract["evidence_layers"]["community"]["version"],
        "candidate": binding["candidate"], "binary_sha256": binding["binary_sha256"],
        "environment": {"sha256": binding["environment_sha256"]},
        "inputs": {"sha256": binding["input_manifest_sha256"]}, "domains": domains,
        "submission": {
            "package_sha256": sha256(archive), "lafs": float(lafs["lafs_gain"]),
            "accuracy": float(point["overall_full_set"]),
            "latency_seconds": float(point["memory_query_avg_seconds"]),
            "frontier_eligible": frontier_eligible,
            "reference_frontier": lafs["reference_frontier"],
            "overview_sha256": sha256(overview_path), "code_sha256": sha256(code_file),
        },
        "passed": frontier_eligible, "started_at": started_at, "finished_at": datetime.now(timezone.utc).isoformat(),
    }
    return report


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise CommunityExecutionError(message)
