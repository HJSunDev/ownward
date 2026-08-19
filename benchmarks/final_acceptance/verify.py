#!/usr/bin/env python3
from __future__ import annotations

import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
from pathlib import PurePosixPath
import re
import subprocess
import sys
import tarfile
import tempfile
from typing import Any


REPORT_SCHEMA = "ownward.final-acceptance-report/v1"
OFFICIAL_REVISION = "2cc8c540bdb87fe6761629b585e727e1c4704520"
LONGMEM_CODEX_MODEL = "gpt-5.4-mini"
LONGMEM_CODEX_REASONING_EFFORT = "xhigh"
LONGMEM_CODEX_CLI_VERSION = "codex-cli 0.117.0"
LONGMEM_ADAPTER_FILES = (
    "run.py",
    "ownward_memory.py",
    "ownward_trajectory.py",
    "memory_config.active.json",
    "memory_config.direct.json",
)
AGENT_INITIAL_CONTENT = "Ownward acceptance Project Borealis requires one action before migration: verify backups."
AGENT_FINAL_CONTENT = "Ownward acceptance Project Borealis requires two actions before migration: verify backups; test restoration."
AGENT_EXCLUDED_CONTENT = "Scratch note: the acceptance controller is currently considering retry 2; this belongs only to this session."
AGENT_APPLIED_ACTIONS = ["verify backups", "test restoration"]
AGENT_MODEL = "gpt-5.4"
AGENT_REASONING_EFFORT = "low"
AGENT_MUTATION_PROMPT = f"""Use only the connected Ownward tools and do not use shell or file tools. First obtain Ownward's collaboration rules. Evaluate each statement below only under those rules.

Statement A: {AGENT_INITIAL_CONTENT}
Statement B: {AGENT_EXCLUDED_CONTENT}

Search for Project Borealis before making any mutation. For each statement that belongs in Ownward under the retrieved rules, persist it through Ownward. Search for and read Statement A after creation. Then treat the following as the corrected full content of Statement A, update the same stable information item using its observed revision, and search for and read it again:

{AGENT_FINAL_CONTENT}"""


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


def _run_checked(command: list[str], *, cwd: Path, environment: dict[str, str] | None = None) -> None:
    completed = subprocess.run(
        command,
        cwd=cwd,
        env=environment,
        check=False,
        capture_output=True,
        text=True,
        encoding="utf-8",
        timeout=1800,
    )
    _require(
        completed.returncode == 0,
        f"engineering verification failed: {' '.join(command)}\n{completed.stdout[-2000:]}\n{completed.stderr[-2000:]}",
    )
    if command[:2] == ["gofmt", "-l"]:
        _require(not completed.stdout.strip(), f"Go files are not formatted:\n{completed.stdout}")


def _verify_repository(repository: Path, candidate: str) -> list[str]:
    completed = subprocess.run(
        ["git", "rev-parse", "HEAD"], cwd=repository, check=False, capture_output=True, text=True, encoding="utf-8"
    )
    _require(completed.returncode == 0 and completed.stdout.strip() == candidate, "repository HEAD differs from the final candidate")
    completed = subprocess.run(
        ["git", "status", "--porcelain"], cwd=repository, check=False, capture_output=True, text=True, encoding="utf-8"
    )
    _require(completed.returncode == 0 and not completed.stdout.strip(), "repository contains uncommitted final-task changes")

    commands = [
        ["go", "mod", "verify"],
        ["gofmt", "-l", "."],
        ["go", "vet", "./..."],
        ["go", "test", "./..."],
        ["go", "build", "./..."],
        [sys.executable, "-m", "unittest", "discover", "-s", "benchmarks/longmemeval_v2", "-p", "test_*.py"],
        [sys.executable, "-m", "py_compile", "benchmarks/final_acceptance/official_validate.py"],
        [sys.executable, "-m", "py_compile", "benchmarks/resource_frontier/tam_benchmark.py"],
    ]
    for directory in ("agent_integration", "resource_frontier", "final_acceptance"):
        environment = dict(os.environ)
        environment["PYTHONPATH"] = str(repository / "benchmarks" / directory)
        _run_checked(
            [sys.executable, "-m", "unittest", "discover", "-s", f"benchmarks/{directory}", "-p", "test_*.py"],
            cwd=repository,
            environment=environment,
        )
    for command in commands:
        _run_checked(command, cwd=repository)
    with tempfile.TemporaryDirectory(prefix="ownward-cross-build-") as root:
        for target_os, target_arch in (("linux", "amd64"), ("darwin", "arm64")):
            environment = dict(os.environ)
            environment.update({"GOOS": target_os, "GOARCH": target_arch, "CGO_ENABLED": "0"})
            _run_checked(
                [
                    "go",
                    "build",
                    "-trimpath",
                    "-ldflags",
                    f"-s -w -X main.version={candidate}",
                    "-o",
                    str(Path(root) / f"ownward-{target_os}-{target_arch}"),
                    "./cmd/ownward",
                ],
                cwd=repository,
                environment=environment,
            )
    return ["go-mod-verify", "gofmt", "go-vet", "go-test", "go-build", "python-tests", "linux-amd64-build", "darwin-arm64-build"]


def _validate_bound_report(
    report: dict[str, Any], *, schema: str, candidate: str, binary_sha256: str, label: str
) -> None:
    _require(report.get("schema") == schema, f"{label} report schema is not {schema}")
    _require(report.get("passed") is True, f"{label} report did not pass")
    _require(report.get("candidate") == candidate, f"{label} report belongs to another candidate")
    _require(report.get("release_binary_version") == candidate, f"{label} report has an unbound binary version")
    _require(report.get("release_binary_sha256") == binary_sha256, f"{label} report belongs to another binary")


def _require_checks(report: dict[str, Any], expected: set[str], label: str) -> None:
    checks = report.get("checks")
    _require(isinstance(checks, list), f"{label} report has no checks")
    names = {
        str(check.get("name"))
        for check in checks
        if isinstance(check, dict) and check.get("passed") is True
    }
    _require(names == expected and len(checks) == len(expected), f"{label} report checks are incomplete or changed")


def _validate_resource_frontier(
    report_path: Path,
    report: dict[str, Any],
    *,
    comparator_path: Path,
    performance_path: Path,
    candidate: str,
    binary_sha256: str,
) -> None:
    _validate_bound_report(
        report,
        schema="ownward.resource-frontier-report/v1",
        candidate=candidate,
        binary_sha256=binary_sha256,
        label="resource frontier",
    )
    _require(report.get("performance_report_sha256") == _sha256(performance_path), "resource frontier used another Ownward performance report")
    _require(report.get("comparator_report_sha256") == _sha256(comparator_path), "resource frontier used another comparator report")
    _require(
        report.get("comparator") == {"name": "total-agent-memory", "version": "12.4.0"},
        "resource frontier used another comparator",
    )
    _require_checks(
        report,
        {
            "程序运行闭包",
            "空载常驻内存",
            "十万条 384 维常驻内存",
            "空闲 CPU",
            "十万条 384 维存储占用",
            "持久写入 P95",
            "基础可检索 P95",
            "语义检索内核 P95",
        },
        "resource frontier",
    )
    comparator = _load(comparator_path)
    _require(
        comparator.get("schema") == "ownward.resource-comparator-report/v1"
        and comparator.get("passed") is True
        and comparator.get("comparator") == "total-agent-memory"
        and comparator.get("version") == "12.4.0",
        "resource comparator report is invalid",
    )
    _require(
        comparator.get("scale") == 100000
        and comparator.get("dimensions") == 384
        and comparator.get("counts")
        == {"information": 100000, "embeddings": 100000, "relations": 99999, "fts": 100000},
        "resource comparator did not use the complete 100k/384d fixture",
    )
    _require(report_path.is_file(), "resource frontier report is missing")


def _validate_product_baseline(report: dict[str, Any], baseline_path: Path, thresholds_path: Path) -> None:
    descriptor = _load(baseline_path)
    _require(descriptor.get("schema") == "ownward.acceptance-baseline/v5", "product baseline is not the frozen v5 definition")
    _require(report.get("baseline") == descriptor.get("schema"), "product report used another baseline schema")
    _require(report.get("baseline_sha256") == _sha256(baseline_path), "product report used another baseline descriptor")
    base = baseline_path.parent
    referenced = {
        "thresholds": descriptor.get("thresholds"),
        "information": descriptor.get("information"),
        "kind_gold": descriptor.get("kind_gold"),
        "relation_gold": descriptor.get("relation_gold"),
        "queries": descriptor.get("queries"),
        "updates": descriptor.get("updates"),
    }
    _require(all(isinstance(value, str) and value for value in referenced.values()), "product baseline references are incomplete")
    paths = {name: (base / str(value)).resolve() for name, value in referenced.items()}
    _require(paths["thresholds"] == thresholds_path, "final thresholds differ from the product baseline")
    actual = report.get("data_sha256")
    _require(isinstance(actual, dict), "product report has no baseline data digests")
    _require(actual == {name: _sha256(path) for name, path in paths.items()}, "product report used changed baseline data")


def _adapter_digests(adapter_dir: Path) -> dict[str, str]:
    return {
        name: _sha256(adapter_dir / name)
        for name in LONGMEM_ADAPTER_FILES
    }


def _load_agent_trace(path: Path) -> tuple[dict[str, Any], list[dict[str, Any]]]:
    events = [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line.strip()]
    _require(bool(events) and isinstance(events[0], dict) and events[0].get("type") == "session", f"invalid agent evidence trace: {path}")
    calls = events[1:]
    _require(
        all(
            isinstance(call, dict)
            and call.get("type") == "ownward_tool_call"
            and str(call.get("name", "")).startswith("ownward_")
            for call in calls
        ),
        f"agent evidence trace contains a bypass event: {path}",
    )
    return events[0], calls


def _agent_information(call: dict[str, Any]) -> dict[str, Any]:
    result = call.get("result")
    _require(isinstance(result, dict), "agent evidence tool result is invalid")
    if call.get("name") in {"ownward_create", "ownward_update"}:
        result = result.get("result")
        _require(isinstance(result, dict), "agent mutation evidence is incomplete")
    information = result.get("information")
    _require(isinstance(information, dict), "agent evidence has no information")
    return information


def _validate_agent_traces(report_path: Path, report: dict[str, Any]) -> dict[str, Path]:
    result: dict[str, Path] = {}
    traces: dict[str, tuple[dict[str, Any], list[dict[str, Any]]]] = {}
    for label, suffix in (("mutation_session", ".mutation.jsonl"), ("independent_session", ".independent.jsonl")):
        trace_path = report_path.with_suffix(suffix)
        _require(trace_path.is_file(), f"agent evidence trace is missing: {trace_path}")
        session = report.get(label)
        _require(isinstance(session, dict), f"agent report has no {label}")
        _require(session.get("trace_sha256") == _sha256(trace_path), f"agent evidence trace changed: {trace_path}")
        traces[label] = _load_agent_trace(trace_path)
        _require(traces[label][0].get("session_id") == session.get("id"), f"agent evidence session identity changed: {trace_path}")
        _require(traces[label][0].get("model") == AGENT_MODEL, f"agent evidence used another model: {trace_path}")
        _require(traces[label][0].get("reasoning_effort") == AGENT_REASONING_EFFORT, f"agent evidence used another reasoning effort: {trace_path}")
        _require(traces[label][0].get("bypassed") is False, f"agent evidence used a bypass tool: {trace_path}")
        _require(traces[label][0].get("tool_call_count") == len(traces[label][1]), f"agent evidence omitted tool calls: {trace_path}")
        result[label] = trace_path
    mutation_session, mutation_calls = traces["mutation_session"]
    independent_session, independent_calls = traces["independent_session"]
    _require(mutation_session.get("session_id") != independent_session.get("session_id"), "agent evidence does not use independent sessions")
    _require(
        mutation_session.get("prompt_sha256") == hashlib.sha256(AGENT_MUTATION_PROMPT.encode("utf-8")).hexdigest(),
        "agent mutation evidence did not use the fixed prompt",
    )
    _require(report.get("model") == AGENT_MODEL and report.get("reasoning_effort") == AGENT_REASONING_EFFORT, "agent report used another fixed agent configuration")
    mutation_names = [str(call.get("name")) for call in mutation_calls]
    _require(
        mutation_names.count("ownward_rules") >= 1
        and mutation_names.count("ownward_create") == 1
        and mutation_names.count("ownward_update") == 1,
        "agent mutation evidence is missing required lifecycle operations",
    )
    rules_position = mutation_names.index("ownward_rules")
    create_position = mutation_names.index("ownward_create")
    update_position = mutation_names.index("ownward_update")
    search_positions = [position for position, name in enumerate(mutation_names) if name == "ownward_search"]
    read_positions = [position for position, name in enumerate(mutation_names) if name == "ownward_read"]
    _require(
        search_positions
        and rules_position < search_positions[0] < create_position < update_position
        and any(create_position < position < update_position for position in search_positions)
        and any(create_position < position < update_position for position in read_positions)
        and any(position > update_position for position in search_positions)
        and any(position > update_position for position in read_positions),
        "agent mutation evidence does not contain the required lifecycle",
    )
    rules_result = next(call for call in mutation_calls if call.get("name") == "ownward_rules").get("result")
    _require(
        isinstance(rules_result, dict)
        and "长期复用" in str(rules_result.get("rules", ""))
        and "临时工作状态" in str(rules_result.get("rules", "")),
        "agent evidence does not contain the Ownward asset-boundary rules",
    )
    created = _agent_information(next(call for call in mutation_calls if call.get("name") == "ownward_create"))
    updated = _agent_information(next(call for call in mutation_calls if call.get("name") == "ownward_update"))
    _require(created.get("id") == updated.get("id") and created.get("revision") == 1 and updated.get("revision") == 2, "agent mutation evidence does not preserve stable identity")
    _require(created.get("content") == AGENT_INITIAL_CONTENT and updated.get("content") == AGENT_FINAL_CONTENT, "agent evidence does not use the fixed growth scenario")
    _require(
        all(AGENT_EXCLUDED_CONTENT not in str(call.get("arguments", {}).get("content", "")) for call in mutation_calls),
        "agent evidence persisted the fixed transient state",
    )
    searches = [call.get("result") for call in mutation_calls if call.get("name") == "ownward_search"]
    _require(isinstance(searches[0], dict) and searches[0].get("results") == [], "growth scenario was not empty before persistence")
    _require(
        all(
            isinstance(search, dict)
            and isinstance(search.get("results"), list)
            and any(isinstance(item, dict) and item.get("id") == updated.get("id") for item in search["results"])
            for search in searches[1:]
        ),
        "growth scenario was not retrievable after persistence",
    )
    independent_names = [str(call.get("name")) for call in independent_calls]
    _require("ownward_search" in independent_names and "ownward_read" in independent_names, "independent agent evidence does not search and read")
    independent_read = _agent_information([call for call in independent_calls if call.get("name") == "ownward_read"][-1])
    _require(independent_read == updated, "independent agent evidence differs from the mutation result")
    independent_result = report.get("independent_result")
    _require(
        independent_result
        == {
            "stable_id": updated.get("id"),
            "revision": updated.get("revision"),
            "content": updated.get("content"),
            "required_actions": AGENT_APPLIED_ACTIONS,
        },
        "agent report does not preserve the independent session's applied result",
    )
    information = report.get("information")
    _require(isinstance(information, dict), "agent report has no information evidence")
    _require(
        information.get("id") == updated.get("id")
        and information.get("revision") == updated.get("revision")
        and information.get("content_sha256") == hashlib.sha256(str(updated.get("content", "")).encode("utf-8")).hexdigest(),
        "agent report information differs from its tool evidence",
    )
    _require(
        information.get("excluded_content_sha256") == hashlib.sha256(AGENT_EXCLUDED_CONTENT.encode("utf-8")).hexdigest(),
        "agent report does not bind the fixed transient state",
    )
    _require(
        information.get("applied_actions") == independent_result["required_actions"],
        "independent agent did not apply the persisted lesson",
    )
    asset_path = report_path.with_suffix(".assets.jsonl")
    _require(asset_path.is_file(), f"agent asset evidence is missing: {asset_path}")
    _require(report.get("asset_log_sha256") == _sha256(asset_path), "agent asset evidence changed")
    asset_text = asset_path.read_text(encoding="utf-8")
    _require(AGENT_EXCLUDED_CONTENT not in asset_text, "agent asset evidence contains transient state")
    asset_events = [json.loads(line) for line in asset_text.splitlines() if line.strip()]
    _require(len(asset_events) == 2, "agent asset evidence contains an unexpected mutation count")
    _require([event.get("operation") for event in asset_events] == ["create", "update"], "agent asset evidence does not contain the fixed lifecycle")
    asset_values = [event.get("value") for event in asset_events]
    _require(
        all(isinstance(value, dict) and value.get("id") == updated.get("id") for value in asset_values)
        and asset_values[0].get("content") == AGENT_INITIAL_CONTENT
        and asset_values[1].get("content") == AGENT_FINAL_CONTENT,
        "agent asset evidence differs from the fixed lifecycle",
    )
    result["asset_log"] = asset_path
    return result


def _package_manifest(root: Path) -> dict[str, dict[str, Any]]:
    result: dict[str, dict[str, Any]] = {}
    for path in sorted(root.rglob("*"), key=lambda value: value.as_posix()):
        _require(not path.is_symlink(), f"LongMemEval package contains a symlink: {path}")
        _require(path.is_dir() or path.is_file(), f"LongMemEval package contains an unsupported entry: {path}")
        if path.is_file():
            result[path.relative_to(root).as_posix()] = {"size": path.stat().st_size, "sha256": _sha256(path)}
    return result


def _manifest_sha256(manifest: dict[str, dict[str, Any]]) -> str:
    encoded = json.dumps(manifest, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def _verify_package_archive(package_root: Path, archive_path: Path) -> str:
    expected = _package_manifest(package_root)
    actual: dict[str, dict[str, Any]] = {}
    with tarfile.open(archive_path, "r:gz") as archive:
        for member in archive.getmembers():
            name = PurePosixPath(member.name)
            _require(not name.is_absolute() and ".." not in name.parts, f"unsafe LongMemEval archive entry: {member.name}")
            if member.isdir():
                continue
            _require(member.isfile(), f"unsupported LongMemEval archive entry: {member.name}")
            _require(name.parts and name.parts[0] == package_root.name and len(name.parts) > 1, f"unexpected LongMemEval archive root: {member.name}")
            relative = PurePosixPath(*name.parts[1:]).as_posix()
            _require(relative not in actual, f"duplicate LongMemEval archive entry: {relative}")
            stream = archive.extractfile(member)
            _require(stream is not None, f"cannot read LongMemEval archive entry: {relative}")
            digest = hashlib.sha256()
            size = 0
            for chunk in iter(lambda: stream.read(1024 * 1024), b""):
                size += len(chunk)
                digest.update(chunk)
            actual[relative] = {"size": size, "sha256": digest.hexdigest()}
    _require(actual == expected, "LongMemEval archive does not exactly match the validated package directory")
    return _manifest_sha256(expected)


def _validate_official_package(
    overview_path: Path,
    overview: dict[str, Any],
    *,
    official_repo: Path,
    official_python: Path,
    adapter_dir: Path,
) -> dict[str, Any]:
    completed = subprocess.run(
        ["git", "rev-parse", "HEAD"],
        cwd=official_repo,
        check=False,
        capture_output=True,
        text=True,
        encoding="utf-8",
    )
    _require(completed.returncode == 0 and completed.stdout.strip() == OFFICIAL_REVISION, "LongMemEval official repository revision changed")
    completed = subprocess.run(
        ["git", "status", "--porcelain", "--untracked-files=no"],
        cwd=official_repo,
        check=False,
        capture_output=True,
        text=True,
        encoding="utf-8",
    )
    _require(completed.returncode == 0 and not completed.stdout.strip(), "LongMemEval official tracked files differ from the pinned revision")
    submission_name = str(overview.get("submission_name", "")).strip()
    _require(submission_name and overview_path.parent.name == submission_name, "LongMemEval submission directory and name differ")
    points = overview.get("operating_points")
    _require(isinstance(points, list) and points, "LongMemEval submission has no operating point")
    _require(all(isinstance(point, dict) and str(point.get("name", "")).strip() for point in points), "LongMemEval operating point metadata is invalid")

    system_description = str(overview.get("system_description_file", "")).strip()
    code_file = str(overview.get("code_file", "")).strip()
    archive_name = str(overview.get("archive_name", "")).strip()
    for value, label in ((system_description, "system description"), (code_file, "code file"), (archive_name, "archive")):
        _require(value and Path(value).name == value, f"LongMemEval {label} name is invalid")
    package_root = overview_path.parent
    _require((package_root / system_description).is_file(), "LongMemEval system description is missing")
    packaged_code = package_root / code_file
    _require(packaged_code.is_file(), "LongMemEval code artifact is missing")
    _require(code_file == "ownward_memory.py" and _sha256(packaged_code) == _sha256(adapter_dir / "ownward_memory.py"), "LongMemEval package does not contain the accepted Ownward adapter")

    helper = Path(__file__).resolve().with_name("official_validate.py")
    _require(official_python.is_file(), f"LongMemEval environment Python does not exist: {official_python}")
    completed = subprocess.run(
        [
            str(official_python),
            str(helper),
            "--official-repo",
            str(official_repo),
            "--overview",
            str(overview_path),
        ],
        check=False,
        capture_output=True,
        text=True,
        encoding="utf-8",
        timeout=1800,
    )
    _require(completed.returncode == 0, f"official LongMemEval package validation failed: {completed.stderr[-2000:]}")

    archive_path = package_root.parent / archive_name
    _require(archive_path.is_file(), "LongMemEval final archive is missing")
    tree_sha256 = _verify_package_archive(package_root, archive_path)
    return {"archive_path": archive_path, "archive_sha256": _sha256(archive_path), "package_tree_sha256": tree_sha256}


def _validate_longmemeval(
    overview_path: Path,
    *,
    candidate: str,
    binary_sha256: str,
    accuracy_min: float,
    latency_max: float,
    adapter_dir: Path,
    official_repo: Path,
    official_python: Path,
) -> tuple[dict[str, float], dict[str, Any]]:
    overview = _load(overview_path)
    _require(str(overview.get("method", "")).lower() == "ownward", "LongMemEval submission method is not Ownward")
    _require(overview.get("tier") == "small", "LongMemEval submission does not use the Small tier")
    lafs = overview.get("lafs")
    _require(isinstance(lafs, dict) and float(lafs.get("lafs_gain", -1)) >= 0, "official LAFS gain is negative or missing")
    points = overview.get("operating_points")
    _require(isinstance(points, list) and points, "LongMemEval submission has no operating point")
    active = [point for point in points if isinstance(point, dict) and "active" in str(point.get("name", "")).lower()]
    _require(active, "LongMemEval submission has no external-agent active operating point")
    qualifying = [
        point
        for point in active
        if float(point.get("overall_full_set", -1)) >= accuracy_min
        and float(point.get("memory_query_avg_seconds", float("inf"))) <= latency_max
    ]
    _require(qualifying, "external-agent active retrieval did not reach the frozen quality-latency frontier")

    expected_adapter = _adapter_digests(adapter_dir)
    root = overview_path.parent
    codex_binary_digests: set[str] = set()
    for point in active:
        metric_path = point.get("metric_overview_file")
        _require(isinstance(metric_path, str) and metric_path, "LongMemEval operating point has no metric path")
        metric_file = (root / metric_path).resolve()
        _require(root == metric_file or root in metric_file.parents, "LongMemEval metric path escapes the submission package")
        operating_point = metric_file.parent
        for domain in ("web", "enterprise"):
            run_args = _load(operating_point / domain / "run_args.json")
            evidence = run_args.get("ownward_evidence")
            _require(isinstance(evidence, dict), f"{domain} run has no Ownward candidate evidence")
            _require(evidence.get("candidate") == candidate, f"{domain} run belongs to another candidate")
            _require(evidence.get("release_binary_sha256") == binary_sha256, f"{domain} run belongs to another binary")
            _require(evidence.get("query_mode") == "codex", f"{domain} active run did not use external Codex retrieval")
            _require(evidence.get("codex_model") == LONGMEM_CODEX_MODEL, f"{domain} active run used another Codex model")
            _require(
                evidence.get("codex_reasoning_effort") == LONGMEM_CODEX_REASONING_EFFORT,
                f"{domain} active run used another Codex reasoning effort",
            )
            _require(evidence.get("codex_cli_version") == LONGMEM_CODEX_CLI_VERSION, f"{domain} active run used another Codex CLI version")
            codex_digest = str(evidence.get("codex_binary_sha256", ""))
            _require(re.fullmatch(r"[0-9a-f]{64}", codex_digest) is not None, f"{domain} run has no Codex binary digest")
            codex_binary_digests.add(codex_digest)
            _require(evidence.get("official_revision") == OFFICIAL_REVISION, f"{domain} run used another official revision")
            _require(evidence.get("adapter_sha256") == expected_adapter, f"{domain} run used another adapter revision")
    _require(len(codex_binary_digests) == 1, "LongMemEval active runs used different Codex binaries")
    package = _validate_official_package(
        overview_path,
        overview,
        official_repo=official_repo,
        official_python=official_python,
        adapter_dir=adapter_dir,
    )
    best = max(qualifying, key=lambda point: float(point["overall_full_set"]))
    return {
        "accuracy": float(best["overall_full_set"]),
        "memory_query_avg_seconds": float(best["memory_query_avg_seconds"]),
        "lafs_gain": float(lafs["lafs_gain"]),
    }, package


def _parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--repository", type=Path, default=Path("."))
    parser.add_argument("--candidate", required=True)
    parser.add_argument("--baseline", type=Path, required=True)
    parser.add_argument("--thresholds", type=Path, required=True)
    parser.add_argument("--product-report", type=Path, required=True)
    parser.add_argument("--performance-report", type=Path, required=True)
    parser.add_argument("--resource-report", type=Path, required=True)
    parser.add_argument("--resource-comparator-report", type=Path, required=True)
    parser.add_argument("--agent-report", type=Path, required=True)
    parser.add_argument("--longmemeval-overview", type=Path, required=True)
    parser.add_argument("--official-repo", type=Path, required=True)
    parser.add_argument("--official-python", type=Path, required=True)
    parser.add_argument("--adapter-dir", type=Path, default=Path("benchmarks/longmemeval_v2"))
    parser.add_argument("--output", type=Path, required=True)
    return parser.parse_args()


def main() -> None:
    args = _parse_args()
    paths = [
        args.binary,
        args.baseline,
        args.thresholds,
        args.product_report,
        args.performance_report,
        args.resource_report,
        args.resource_comparator_report,
        args.agent_report,
        args.longmemeval_overview,
    ]
    for path in paths:
        _require(path.resolve().is_file(), f"required artifact does not exist: {path}")
    candidate = args.candidate.strip()
    _require(re.fullmatch(r"[0-9a-f]{40}", candidate) is not None, "candidate must be a full lowercase Git commit hash")
    binary = args.binary.resolve()
    binary_sha256 = _sha256(binary)
    _require(_binary_version(binary) == candidate, "release binary version differs from the final candidate")

    thresholds_path = args.thresholds.resolve()
    thresholds = _load(thresholds_path)
    frontier = thresholds.get("public_frontier", {}).get("longmemeval_v2_small", {})
    accuracy_min = float(frontier.get("accuracy_min", 0))
    latency_max = float(frontier.get("average_memory_query_seconds_max", 0))
    _require(accuracy_min > 0 and latency_max > 0, "frozen public-frontier thresholds are incomplete")

    product = _load(args.product_report.resolve())
    performance = _load(args.performance_report.resolve())
    resource = _load(args.resource_report.resolve())
    agent = _load(args.agent_report.resolve())
    _validate_bound_report(product, schema="ownward.acceptance-report/v2", candidate=candidate, binary_sha256=binary_sha256, label="product")
    _validate_bound_report(performance, schema="ownward.performance-report/v4", candidate=candidate, binary_sha256=binary_sha256, label="performance")
    _validate_bound_report(agent, schema="ownward.agent-integration-report/v1", candidate=candidate, binary_sha256=binary_sha256, label="agent")
    _require_checks(
        product,
        {
            "信息沉淀与明确语义保持",
            "自主信息类型判断",
            "自主语义关系组织",
            "明确对象检索",
            "语义意图检索",
            "关系约束检索",
            "场景适用性检索",
            "资产备份、空白恢复与派生重建",
        },
        "product",
    )
    _require_checks(
        performance,
        {
            "明确对象检索 P95",
            "语义意图检索 P95",
            "关系导航 P95",
            "场景适用性检索 P95",
            "并发八路语义检索 P95",
            "持久写入 P95",
            "基础可检索 P95",
            "发布二进制体积",
            "空载常驻内存",
            "十万条 384 维常驻内存",
            "空闲 CPU",
            "派生状态存储比",
        },
        "performance",
    )
    _require_checks(
        agent,
        {
            "rules-create-search-read-update",
            "stable-identity-and-revision",
            "independent-session-search-and-read",
            "growth-closure",
            "short-term-state-excluded",
            "no-bypass-tool",
            "final-binary-authority",
        },
        "agent",
    )
    threshold_sha256 = _sha256(thresholds_path)
    baseline_path = args.baseline.resolve()
    _validate_product_baseline(product, baseline_path, thresholds_path)
    _require(str(product.get("provider", "")).startswith("openai-compatible:"), "product report did not use the required semantic provider")
    _require(performance.get("thresholds_sha256") == threshold_sha256, "performance report used other thresholds")
    _require(
        performance.get("comparable") is True
        and performance.get("scale") == 100000
        and performance.get("dimensions") == 384,
        "performance report is not a comparable 100k/384d run",
    )
    _validate_resource_frontier(
        args.resource_report.resolve(),
        resource,
        comparator_path=args.resource_comparator_report.resolve(),
        performance_path=args.performance_report.resolve(),
        candidate=candidate,
        binary_sha256=binary_sha256,
    )
    agent_traces = _validate_agent_traces(args.agent_report.resolve(), agent)

    public_metrics, package = _validate_longmemeval(
        args.longmemeval_overview.resolve(),
        candidate=candidate,
        binary_sha256=binary_sha256,
        accuracy_min=accuracy_min,
        latency_max=latency_max,
        adapter_dir=args.adapter_dir.resolve(),
        official_repo=args.official_repo.resolve(),
        official_python=args.official_python.resolve(),
    )
    engineering_checks = _verify_repository(args.repository.resolve(), candidate)
    artifacts = {
        "release_binary": binary,
        "baseline": baseline_path,
        "thresholds": thresholds_path,
        "product_report": args.product_report.resolve(),
        "performance_report": args.performance_report.resolve(),
        "resource_frontier_report": args.resource_report.resolve(),
        "resource_comparator_report": args.resource_comparator_report.resolve(),
        "agent_report": args.agent_report.resolve(),
        "agent_mutation_trace": agent_traces["mutation_session"],
        "agent_independent_trace": agent_traces["independent_session"],
        "agent_asset_log": agent_traces["asset_log"],
        "longmemeval_overview": args.longmemeval_overview.resolve(),
        "longmemeval_archive": package["archive_path"],
    }
    report = {
        "schema": REPORT_SCHEMA,
        "candidate": candidate,
        "release_binary_version": candidate,
        "release_binary_sha256": binary_sha256,
        "verified_at": datetime.now(timezone.utc).isoformat(),
        "artifacts": {name: {"path": str(path), "sha256": _sha256(path)} for name, path in artifacts.items()},
        "public_frontier": {
            **public_metrics,
            "accuracy_min": accuracy_min,
            "memory_query_avg_seconds_max": latency_max,
            "package_tree_sha256": package["package_tree_sha256"],
        },
        "engineering_checks": engineering_checks,
        "checks": [
            {"name": "product-and-recovery", "passed": True},
            {"name": "external-agent-and-growth", "passed": True},
            {"name": "public-quality-latency-frontier", "passed": True},
            {"name": "release-resource-envelope", "passed": True},
            {"name": "public-resource-frontier", "passed": True},
            {"name": "single-candidate-binding", "passed": True},
        ],
        "passed": True,
    }
    output = args.output.resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    encoded = json.dumps(report, ensure_ascii=False, indent=2) + "\n"
    temporary = output.with_suffix(output.suffix + ".tmp")
    temporary.write_text(encoded, encoding="utf-8")
    temporary.replace(output)
    print(encoded, end="")


if __name__ == "__main__":
    main()
