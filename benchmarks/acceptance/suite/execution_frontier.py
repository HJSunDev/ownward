from __future__ import annotations

from pathlib import Path
import sys
from typing import Any

import frontier
import lifecycle
from contract import validate_report
from execution_support import load_json, require, run


def execute_frontier(
    suite_root: Path,
    contract: dict[str, Any],
    state: dict[str, Any],
    mode: str,
    config: dict[str, Any],
    workspace: Path,
    resume: bool,
) -> tuple[dict[str, Any], Path]:
    section = config["frontier"]
    tool = Path(section["tool"]).resolve()
    require(tool.is_file(), "内核前沿观察器不存在")
    require(lifecycle.file_sha256(tool) == state["binding"]["observer_sha256"], "内核观察器制品与候选绑定不一致")
    observation_path = workspace / "evidence" / "frontier" / f"{mode}-observation.json"
    observation_path.parent.mkdir(parents=True, exist_ok=True)
    actual_mode = "targeted" if mode == "targeted" else "full"
    expected_stages = set(section.get("targeted_stages", [])) if actual_mode == "targeted" else frontier.STAGES
    candidate = None
    if observation_path.exists():
        require(resume, "内核观察已存在；使用 --resume 复用")
        try:
            existing = load_json(observation_path)
            frontier.validate_observation(contract, existing)
            if (
                existing.get("candidate") == state["binding"]["candidate"]
                and existing.get("environment", {}).get("sha256") == state["binding"]["environment_sha256"]
                and existing.get("input_manifest_sha256") == state["binding"]["input_manifest_sha256"]
                and existing.get("tool_sha256") == state["binding"]["observer_sha256"]
                and set(existing.get("requested_stages", [])) == expected_stages
            ):
                candidate = existing
        except (ValueError, OSError):
            candidate = None
        if candidate is None:
            observation_path.unlink()
    dataset = suite_root / "materials" / "core" / "v1" / "dataset.json"
    command = ([sys.executable, str(tool)] if tool.suffix.lower() == ".py" else [str(tool)]) + [
        "--materials", str(dataset), "--candidate", state["binding"]["candidate"],
        "--mode", actual_mode, "--environment-sha256", state["binding"]["environment_sha256"],
        "--input-manifest-sha256", state["binding"]["input_manifest_sha256"],
        "--repository", str(suite_root.parents[2]), "--output", str(observation_path),
    ]
    if actual_mode == "targeted":
        stages = section.get("targeted_stages")
        require(isinstance(stages, list) and stages, "定向模式必须声明受影响阶段")
        command.extend(["--stages", ",".join(str(value) for value in stages)])
    if candidate is None:
        run(command, cwd=suite_root.parents[2], timeout=float(contract["optimization_loop"]["modes"][actual_mode]["max_wall_seconds"]))
        candidate = load_json(observation_path)
    frontier.validate_observation(contract, candidate)
    require(set(candidate.get("requested_stages", [])) == expected_stages, "内核观察报告没有绑定本次执行阶段")
    require(candidate.get("materials_sha256") == lifecycle.file_sha256(dataset), "内核观察报告没有绑定实际固定材料")
    require(candidate.get("tool_sha256") == state["binding"]["observer_sha256"], "内核观察报告没有绑定当前候选观察器")
    baseline_observation = None
    baseline = state.get("baseline")
    if isinstance(baseline, dict):
        observation = baseline.get("observations", {}).get("full")
        if isinstance(observation, dict):
            if isinstance(observation.get("value"), dict):
                require(lifecycle.canonical_sha256(observation["value"]) == observation.get("canonical_sha256"), "有效内核基线内联证据发生变化")
                baseline_observation = observation["value"]
            else:
                path = Path(str(observation.get("path", "")))
                require(path.is_file() and lifecycle.file_sha256(path) == observation.get("sha256"), "有效内核基线观察证据缺失或变化")
                baseline_observation = load_json(path)
    calibration = load_json(suite_root / "materials" / "frontier" / "v1" / "calibration.json")
    report = frontier.compare(contract, baseline_observation, candidate, calibration)
    validate_report(contract, "frontier", report)
    return report, observation_path
