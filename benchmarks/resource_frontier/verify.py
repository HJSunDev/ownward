#!/usr/bin/env python3
from __future__ import annotations

import argparse
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import re
import subprocess
from typing import Any


REPORT_SCHEMA = "ownward.resource-frontier-report/v1"
COMPARATOR_SCHEMA = "ownward.resource-comparator-report/v1"
COMPARATOR = "total-agent-memory"
COMPARATOR_VERSION = "12.4.0"
SOURCE_TAG = "v12.4.0"
SOURCE_REVISION = "224323c9f522b5329515c14694c967ac6099b5a8"
RUNTIME_PINS = {"mcp": "1.29.0"}
PROFILE = {
    "MEMORY_MODE": "fast",
    "MEMORY_ALLOW_OLLAMA_IN_HOT_PATH": "false",
    "MEMORY_ENRICHMENT_ENABLED": "false",
    "MEMORY_ASYNC_ENRICHMENT": "false",
    "MEMORY_RERANK_ENABLED": "false",
    "MEMORY_EMBED_PROVIDER": "openai",
    "OPENAI_API_KEY": "",
    "COHERE_API_KEY": "",
    "USE_OLLAMA_EMBED": "false",
    "USE_BINARY_SEARCH": "true",
}


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise RuntimeError(message)


def _load(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    _require(isinstance(value, dict), f"JSON document must contain an object: {path}")
    return value


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _binary_version(path: Path) -> str:
    completed = subprocess.run(
        [str(path), "version"], check=False, capture_output=True, text=True, encoding="utf-8", timeout=30
    )
    _require(completed.returncode == 0, f"could not read release binary version: {completed.stderr.strip()}")
    return completed.stdout.strip()


def _metric(document: dict[str, Any], name: str) -> float:
    value = document.get(name)
    _require(isinstance(value, (int, float)) and value >= 0, f"invalid metric: {name}")
    return float(value)


def _p95(document: dict[str, Any], name: str) -> float:
    latency = document.get("latency")
    _require(isinstance(latency, dict) and isinstance(latency.get(name), dict), f"missing latency: {name}")
    return _metric(latency[name], "p95_ms")


def _check(name: str, actual: float, maximum: float, unit: str) -> dict[str, Any]:
    return {"name": name, "actual": actual, "maximum": maximum, "unit": unit, "passed": actual <= maximum}


def _validate_comparator(report: dict[str, Any]) -> None:
    _require(report.get("schema") == COMPARATOR_SCHEMA and report.get("passed") is True, "comparator report did not pass")
    _require(report.get("comparator") == COMPARATOR and report.get("version") == COMPARATOR_VERSION, "unexpected comparator")
    _require(report.get("source") == "https://github.com/vbcherepanov/total-agent-memory", "unexpected comparator source")
    _require(report.get("source_tag") == SOURCE_TAG and report.get("source_revision") == SOURCE_REVISION, "unexpected comparator source revision")
    _require(report.get("profile") == PROFILE, "comparator profile changed")
    _require(report.get("runtime_pins") == RUNTIME_PINS, "comparator compatibility pins changed")
    _require(report.get("scale") == 100_000 and report.get("dimensions") == 384, "comparator workload is not 100k/384d")
    _require(report.get("model_excluded_from_kernel_measurement") is True, "comparator includes a local model in kernel resources")
    _require(report.get("python_utf8_mode") is True, "comparator did not use Python UTF-8 mode")
    _require(report.get("fixture_construction_excluded_from_timed_operations") is True, "comparator timed fixture construction")
    _require(
        report.get("counts") == {"information": 100_000, "embeddings": 100_000, "relations": 99_999, "fts": 100_000},
        "comparator fixture counts are incomplete",
    )
    _require(report.get("semantic_target_id") == report.get("semantic_top_id"), "comparator semantic smoke failed")
    _require(re.fullmatch(r"[0-9a-f]{64}", str(report.get("python_executable_sha256", ""))) is not None, "comparator Python is unbound")
    _require(re.fullmatch(r"[0-9a-f]{64}", str(report.get("runtime_packages_sha256", ""))) is not None, "comparator packages are unbound")
    packages = report.get("runtime_packages")
    _require(
        isinstance(packages, list)
        and all(any(item == {"name": name, "version": version} for item in packages) for name, version in RUNTIME_PINS.items()),
        "comparator package inventory is incomplete",
    )
    roots = report.get("runtime_closure_roots")
    _require(isinstance(roots, list) and len(roots) >= 2 and all(isinstance(root, str) and root for root in roots), "comparator runtime closure is incomplete")
    package_json = json.dumps(packages, sort_keys=True, separators=(",", ":")).encode("utf-8")
    _require(report.get("runtime_packages_sha256") == hashlib.sha256(package_json).hexdigest(), "comparator package inventory changed")
    _require(
        report.get("workload_sha256") == _sha256(Path(__file__).with_name("tam_benchmark.py")),
        "comparator used another workload",
    )
    _require(re.fullmatch(r"[0-9a-f]{64}", str(report.get("metadata_sha256", ""))) is not None, "comparator metadata is unbound")
    _require(re.fullmatch(r"[0-9a-f]{64}", str(report.get("source_tree_sha256", ""))) is not None, "comparator source tree is unbound")
    source = report.get("source_sha256")
    _require(
        isinstance(source, dict)
        and set(source) == {"server.py", "config.py", "paths.py"}
        and all(re.fullmatch(r"[0-9a-f]{64}", str(value)) is not None for value in source.values()),
        "comparator source is unbound",
    )
    for name in ("runtime_footprint_mib", "storage_mib_at_scale", "idle_rss_mib", "rss_mib_at_scale", "idle_cpu_percent"):
        _metric(report, name)
    for name in ("durable_write", "basic_searchable", "semantic_kernel", "fts"):
        _p95(report, name)


def _parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--candidate", required=True)
    parser.add_argument("--performance-report", type=Path, required=True)
    parser.add_argument("--comparator-report", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    return parser.parse_args()


def main() -> None:
    args = _parse_args()
    candidate = args.candidate.strip()
    _require(re.fullmatch(r"[0-9a-f]{40}", candidate) is not None, "candidate must be a full lowercase Git commit hash")
    binary = args.binary.resolve()
    performance_path = args.performance_report.resolve()
    comparator_path = args.comparator_report.resolve()
    _require(binary.is_file() and performance_path.is_file() and comparator_path.is_file(), "required resource artifact is missing")
    binary_sha256 = _sha256(binary)
    _require(_binary_version(binary) == candidate, "release binary version differs from the candidate")

    performance = _load(performance_path)
    _require(performance.get("schema") == "ownward.performance-report/v4" and performance.get("passed") is True, "Ownward performance report did not pass")
    _require(
        performance.get("candidate") == candidate
        and performance.get("release_binary_version") == candidate
        and performance.get("release_binary_sha256") == binary_sha256,
        "Ownward performance report belongs to another candidate or binary",
    )
    _require(performance.get("comparable") is True and performance.get("scale") == 100_000 and performance.get("dimensions") == 384, "Ownward performance workload is not 100k/384d")
    comparator = _load(comparator_path)
    _validate_comparator(comparator)

    checks = [
        _check("程序运行闭包", _metric(performance, "release_binary_mib"), _metric(comparator, "runtime_footprint_mib"), "MiB"),
        _check("空载常驻内存", _metric(performance, "idle_rss_mib"), _metric(comparator, "idle_rss_mib"), "MiB"),
        _check("十万条 384 维常驻内存", _metric(performance, "rss_mib"), _metric(comparator, "rss_mib_at_scale"), "MiB"),
        _check("空闲 CPU", _metric(performance, "idle_cpu_percent"), _metric(comparator, "idle_cpu_percent"), "%"),
        _check("十万条 384 维存储占用", _metric(performance, "storage_mib_at_scale"), _metric(comparator, "storage_mib_at_scale"), "MiB"),
        _check("持久写入 P95", _p95(performance, "durable_write"), _p95(comparator, "durable_write"), "ms"),
        _check("基础可检索 P95", _p95(performance, "basic_searchable"), _p95(comparator, "basic_searchable"), "ms"),
        _check("语义检索内核 P95", _p95(performance, "semantic_intent"), _p95(comparator, "semantic_kernel"), "ms"),
    ]
    passed = all(check["passed"] for check in checks)
    report = {
        "schema": REPORT_SCHEMA,
        "candidate": candidate,
        "release_binary_version": candidate,
        "release_binary_sha256": binary_sha256,
        "verified_at": datetime.now(timezone.utc).isoformat(),
        "performance_report_sha256": _sha256(performance_path),
        "comparator_report_sha256": _sha256(comparator_path),
        "comparator": {"name": COMPARATOR, "version": COMPARATOR_VERSION},
        "checks": checks,
        "passed": passed,
    }
    output = args.output.resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    encoded = json.dumps(report, ensure_ascii=False, indent=2) + "\n"
    temporary = output.with_suffix(output.suffix + ".tmp")
    temporary.write_text(encoded, encoding="utf-8")
    temporary.replace(output)
    print(encoded, end="")
    if not passed:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
