from __future__ import annotations

from pathlib import Path
import sys
from typing import Any

from evidence import validate_layer_report
from execution_support import load_json, require, run


def execute_core(
    suite_root: Path,
    contract: dict[str, Any],
    state: dict[str, Any],
    config: dict[str, Any],
    workspace: Path,
    resume: bool,
) -> dict[str, Any]:
    binary = product_binary(config, state)
    evidence = workspace / "evidence" / "core"
    adapter_report = evidence / "adapter-report.json"
    if adapter_report.is_file():
        require(resume, "固定内核证据已完成；使用 --resume 封装报告")
        report = load_json(adapter_report)
        validate_layer_report(contract, "core", report, expected_binding=state["binding"])
        return report
    if evidence.exists():
        require(resume, "固定内核证据未完成；使用 --resume 仅重做该层")
        from execution_support import safe_remove
        safe_remove(evidence, workspace / "evidence")
    adapter = suite_root / "adapters" / "core" / "verify.py"
    command = [
        sys.executable, str(adapter), "--repository", str(suite_root.parents[2]), "--binary", str(binary),
        "--candidate", state["binding"]["candidate"], "--evidence-dir", str(evidence), "--output", str(adapter_report),
        "--suite-version", contract["suite_version"], "--environment-sha256", state["binding"]["environment_sha256"],
        "--input-manifest-sha256", state["binding"]["input_manifest_sha256"],
    ]
    run(command, cwd=suite_root.parents[2], timeout=float(contract["evidence_layers"]["core"]["max_wall_seconds"]))
    report = load_json(adapter_report)
    validate_layer_report(contract, "core", report, expected_binding=state["binding"])
    return report


def product_binary(config: dict[str, Any], state: dict[str, Any]) -> Path:
    binary = Path(config["candidate"]["binary"]).resolve()
    require(binary.is_file(), "候选二进制不存在")
    from lifecycle import file_sha256
    require(file_sha256(binary) == state["binding"]["binary_sha256"], "候选二进制摘要变化")
    return binary
