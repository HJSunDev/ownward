#!/usr/bin/env python3
from __future__ import annotations

import argparse
from datetime import datetime, timezone
import hashlib
import importlib.util
import json
import math
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


_DYNAMIC_COMMON_PATH = Path(__file__).resolve().parents[1] / "acceptance" / "dynamic" / "common.py"
_DYNAMIC_COMMON_SPEC = importlib.util.spec_from_file_location("ownward_dynamic_common", _DYNAMIC_COMMON_PATH)
_require_dynamic_common = _DYNAMIC_COMMON_SPEC is not None and _DYNAMIC_COMMON_SPEC.loader is not None
if not _require_dynamic_common:
    raise RuntimeError("dynamic acceptance common validator is missing")
_dynamic_common = importlib.util.module_from_spec(_DYNAMIC_COMMON_SPEC)
_DYNAMIC_COMMON_SPEC.loader.exec_module(_dynamic_common)


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

    completed = subprocess.run(
        ["git", "ls-files", "-z", "--", "*.go"], cwd=repository, check=False, capture_output=True
    )
    _require(completed.returncode == 0, "could not enumerate tracked Go files")
    tracked_go_files = [value.decode("utf-8") for value in completed.stdout.split(b"\x00") if value]
    _require(bool(tracked_go_files), "repository has no tracked Go files")

    commands = [
        ["go", "mod", "verify"],
        ["gofmt", "-l", *tracked_go_files],
        ["go", "vet", "./..."],
        ["go", "test", "./..."],
        ["go", "build", "./..."],
        [sys.executable, "-m", "unittest", "discover", "-s", "benchmarks/longmemeval_v2", "-p", "test_*.py"],
        [sys.executable, "-m", "py_compile", "benchmarks/final_acceptance/official_validate.py"],
        [sys.executable, "-m", "py_compile", "benchmarks/resource_frontier/tam_benchmark.py"],
    ]
    for directory in ("agent_integration", "resource_frontier", "final_acceptance", "acceptance/dynamic"):
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


def _validate_dynamic_evidence(report: dict[str, Any], task_classes: set[str]) -> dict[str, Path]:
    evidence = report.get("evidence")
    _require(isinstance(evidence, dict) and evidence, "dynamic report has no raw evidence")
    paths: dict[str, Path] = {}
    for name, descriptor in evidence.items():
        _require(isinstance(descriptor, dict), f"dynamic evidence {name} is invalid")
        path = Path(str(descriptor.get("path", ""))).resolve()
        _require(path.is_file(), f"dynamic evidence {name} is missing")
        _require(descriptor.get("sha256") == _sha256(path), f"dynamic evidence {name} changed")
        paths[str(name)] = path
    required = {
        "random",
        "hidden",
        "hidden_events",
        "hidden_run",
        "expression",
        "expression_events",
        "expression_run",
        "validation",
        "validation_events",
        "validation_run",
        "dataset",
        "dataset_run",
        "full_mapping",
        "baseline_mapping",
        "asset_integrity",
        "organization",
        "codex_binary",
    }
    for condition in ("full", "baseline"):
        for task_class in task_classes:
            required.update(
                {
                    f"{condition}_{task_class}_answers",
                    f"{condition}_{task_class}_events",
                    f"{condition}_{task_class}_run",
                }
            )
    _require(required <= set(paths), "dynamic evidence is incomplete")
    return paths


def _dynamic_scenario_key(scenario_id: str, node_id: str) -> str:
    return f"{scenario_id}/{node_id}"


def _dynamic_agent_prompt(questions: list[dict[str, str]]) -> str:
    return _dynamic_common.agent_prompt(questions)


def _dynamic_json_fragment(value: object) -> Any:
    if not isinstance(value, str):
        return value
    text = value.strip()
    starts = [position for position in (text.find("{"), text.find("[")) if position >= 0]
    if starts:
        text = text[min(starts) :]
    try:
        result, _ = json.JSONDecoder().raw_decode(text)
        return result
    except json.JSONDecodeError:
        return value


def _validate_dynamic_generation_trace(path: Path) -> None:
    session_id = ""
    for line in path.read_text(encoding="utf-8").splitlines():
        if not line.strip():
            continue
        try:
            event = json.loads(line)
        except json.JSONDecodeError as error:
            raise RuntimeError(f"dynamic generation trace contains non-JSON output: {path}") from error
        if event.get("type") == "thread.started":
            session_id = str(event.get("thread_id", "")).strip()
            continue
        if event.get("type") != "item.completed":
            continue
        item = event.get("item")
        _require(isinstance(item, dict), f"dynamic generation trace contains an invalid item: {path}")
        _require(
            item.get("type") in {"agent_message", "reasoning"},
            f"dynamic generation trace used a tool or external operation: {path}",
        )
    _require(bool(session_id), f"dynamic generation trace has no session identity: {path}")


def _dynamic_trace(path: Path) -> tuple[str, list[str], dict[str, list[str]]]:
    session_id = ""
    calls: list[str] = []
    observed_evidence: dict[str, list[str]] = {}

    def strings(value: Any) -> list[str]:
        if isinstance(value, dict):
            return [text for item in value.values() for text in strings(item)]
        if isinstance(value, list):
            return [text for item in value for text in strings(item)]
        if isinstance(value, str):
            parsed = _dynamic_json_fragment(value)
            if not isinstance(parsed, str):
                return [value, *strings(parsed)]
            return [value]
        return []

    def observe(value: Any) -> None:
        if isinstance(value, dict):
            information_id = value.get("id")
            if isinstance(information_id, str) and information_id:
                observed_evidence.setdefault(information_id, []).extend(strings(value))
            for item in value.values():
                observe(item)
            return
        if isinstance(value, list):
            for item in value:
                observe(item)
            return
        if isinstance(value, str):
            parsed = _dynamic_json_fragment(value)
            if not isinstance(parsed, str):
                observe(parsed)

    for line in path.read_text(encoding="utf-8").splitlines():
        if not line.strip():
            continue
        try:
            event = json.loads(line)
        except json.JSONDecodeError as error:
            raise RuntimeError(f"dynamic agent trace contains non-JSON output: {path}") from error
        if event.get("type") == "thread.started":
            session_id = str(event.get("thread_id", "")).strip()
            continue
        if event.get("type") != "item.completed":
            continue
        item = event.get("item")
        _require(isinstance(item, dict), f"dynamic agent trace contains an invalid item: {path}")
        item_type = str(item.get("type", ""))
        if item_type in {"agent_message", "reasoning"}:
            continue
        name = str(item.get("tool", "")).rsplit("__", 1)[-1]
        _require(
            item_type == "mcp_tool_call"
            and item.get("server") == "ownward"
            and name.startswith("ownward_"),
            f"dynamic agent trace contains a bypass operation: {path}",
        )
        calls.append(name)
        result = item.get("result")
        successful = item.get("status") == "completed" and not item.get("error")
        if isinstance(result, dict) and result.get("isError") is True:
            successful = False
        if successful:
            if isinstance(result, dict) and "structured_content" in result:
                result = result["structured_content"]
            observe(_dynamic_json_fragment(result))
    _require(bool(session_id), f"dynamic agent trace has no session identity: {path}")
    return session_id, calls, observed_evidence


def _validate_dynamic_mapping(
    dataset: dict[str, Any], mapping: dict[str, Any], *, condition: str, disable_relations: bool
) -> None:
    _require(
        mapping.get("schema") == "ownward.dynamic-ingestion/v1"
        and mapping.get("condition") == condition
        and mapping.get("disable_relations") is disable_relations,
        f"dynamic {condition} mapping is invalid",
    )
    _require(mapping.get("data_directory") == "full-data", f"dynamic {condition} used another data state")
    expected_keys: set[str] = set()
    operation_count = 0
    for scenario in dataset["valid_scenarios"]:
        scenario_id = str(scenario["truth"]["id"])
        for item in scenario["expression"]["information"]:
            expected_keys.add(_dynamic_scenario_key(scenario_id, str(item["node_id"])))
            operation_count += 1
        operation_count += len(scenario["expression"]["updates"])
    stable_ids = mapping.get("stable_ids")
    revisions = mapping.get("revisions")
    _require(isinstance(stable_ids, dict) and set(stable_ids) == expected_keys, f"dynamic {condition} identities are incomplete")
    _require(len(set(stable_ids.values())) == len(stable_ids), f"dynamic {condition} identities were merged")
    _require(isinstance(revisions, dict) and set(revisions) == expected_keys, f"dynamic {condition} revisions are incomplete")
    _require(
        mapping.get("operation_count") == operation_count
        and mapping.get("logical_semantic_model_calls") == operation_count * 2,
        f"dynamic {condition} operation accounting changed",
    )
    for name in ("organization_seconds", "organization_seconds_max"):
        value = float(mapping.get(name, -1))
        _require(math.isfinite(value) and value >= 0, f"dynamic {condition} timing is invalid")


def _recompute_dynamic_run(
    *,
    condition: str,
    task_class: str,
    dataset: dict[str, Any],
    mapping: dict[str, Any],
    evidence: dict[str, Path],
    protocol: dict[str, Any],
    expected_binding: dict[str, Any],
) -> dict[str, Any]:
    scenarios = [value for value in dataset["valid_scenarios"] if value["truth"]["task_class"] == task_class]
    questions = [
        {"query_id": str(value["truth"]["id"]), "question": str(value["expression"]["query"]["question"])}
        for value in scenarios
    ]
    prompt_sha256 = hashlib.sha256(_dynamic_agent_prompt(questions).encode("utf-8")).hexdigest()
    prefix = f"{condition}_{task_class}"
    answers_path = evidence[f"{prefix}_answers"]
    events_path = evidence[f"{prefix}_events"]
    run = _load(evidence[f"{prefix}_run"])
    agent = protocol["models"]["external_agent"]
    _require(
        run.get("schema") == "ownward.dynamic-agent-run/v1"
        and run.get("model") == agent["model"]
        and run.get("reasoning_effort") == agent["reasoning_effort"]
        and run.get("prompt_sha256") == prompt_sha256
        and run.get("answers_sha256") == _sha256(answers_path)
        and run.get("events_sha256") == _sha256(events_path),
        f"dynamic {condition} {task_class} run metadata changed",
    )
    _require(run.get("binding") == expected_binding, f"dynamic {condition} {task_class} run binding changed")
    elapsed = float(run.get("elapsed_seconds", 0))
    _require(math.isfinite(elapsed) and elapsed > 0, f"dynamic {condition} {task_class} elapsed time is invalid")
    answers = _load(answers_path).get("answers")
    _require(isinstance(answers, list), f"dynamic {condition} {task_class} answers are missing")
    by_id = {str(value.get("query_id", "")): value for value in answers if isinstance(value, dict)}
    _require(
        len(answers) == len(by_id) == len(scenarios)
        and set(by_id) == {str(value["truth"]["id"]) for value in scenarios},
        f"dynamic {condition} query set changed",
    )
    stable_ids = mapping["stable_ids"]
    session_id, calls, observed_evidence = _dynamic_trace(events_path)
    successes = 0
    for scenario in scenarios:
        truth = scenario["truth"]
        scenario_id = str(truth["id"])
        answer = by_id[scenario_id]
        expected_ids = {
            stable_ids[_dynamic_scenario_key(scenario_id, str(node_id))] for node_id in truth["query"]["expected_ids"]
        }
        forbidden_ids = {
            stable_ids[_dynamic_scenario_key(scenario_id, str(node_id))] for node_id in truth["query"]["forbidden_ids"]
        }
        actual_ids = {str(value) for value in answer.get("information_ids", [])}
        expected_facts = {str(value) for value in truth["query"]["answer_facts"]}
        actual_facts = {str(value) for value in answer.get("answer_facts", [])}
        grounded = actual_ids <= set(observed_evidence) and all(
            any(fact in text for information_id in actual_ids for text in observed_evidence[information_id])
            for fact in actual_facts
        )
        successes += int(
            actual_ids == expected_ids
            and not actual_ids & forbidden_ids
            and actual_facts == expected_facts
            and grounded
        )
    budget = int(protocol["budgets"]["agent_tool_calls_per_query"])
    _require(len(calls) <= len(scenarios) * budget, f"dynamic {condition} {task_class} exceeded its tool budget")
    total = len(scenarios)
    return {
        "session_id": session_id,
        "prompt_sha256": prompt_sha256,
        "successes": successes,
        "total": total,
        "success_rate": successes / total,
        "tool_calls": len(calls),
        "search_calls": calls.count("ownward_search"),
        "elapsed_seconds": elapsed,
    }


def _same_metric(actual: Any, expected: float, label: str) -> None:
    try:
        value = float(actual)
    except (TypeError, ValueError) as error:
        raise RuntimeError(f"{label} is not numeric") from error
    _require(math.isfinite(value) and abs(value - expected) <= 1e-9, f"{label} differs from raw dynamic evidence")


def _require_dynamic_outcomes(
    *,
    full_runs: dict[str, dict[str, Any]],
    baseline_runs: dict[str, dict[str, Any]],
    organization: dict[str, float],
    protocol: dict[str, Any],
) -> None:
    statistics = protocol["statistics"]
    confidence = float(statistics["confidence_level"])
    sessions: list[str] = []
    equivalent = True
    advantage = False
    for task_class in protocol["generation"]["task_classes"]:
        full = full_runs[str(task_class)]
        baseline = baseline_runs[str(task_class)]
        sessions.extend((str(full["session_id"]), str(baseline["session_id"])))
        lower = _dynamic_common.wilson_lower(int(full["successes"]), int(full["total"]), confidence)
        _require(
            lower >= float(statistics["dynamic_task_success_wilson_lower_min"]),
            f"dynamic {task_class} did not reach the frozen success threshold",
        )
        quality_gain = float(full["success_rate"]) - float(baseline["success_rate"])
        class_equivalent = quality_gain >= -float(statistics["ablation_equivalence_margin"])
        latency_reduction = 1 - float(full["elapsed_seconds"]) / float(baseline["elapsed_seconds"])
        full_cost = int(full["tool_calls"]) + int(full["search_calls"])
        baseline_cost = int(baseline["tool_calls"]) + int(baseline["search_calls"])
        cost_reduction = 1 - full_cost / baseline_cost if baseline_cost else 0.0
        class_advantage = quality_gain >= float(statistics["minimum_quality_gain"]) or (
            class_equivalent
            and (
                latency_reduction >= float(statistics["minimum_latency_or_cost_reduction"])
                or cost_reduction >= float(statistics["minimum_latency_or_cost_reduction"])
            )
        )
        equivalent = equivalent and class_equivalent
        advantage = advantage or class_advantage
    _require(all(sessions) and len(set(sessions)) == len(sessions), "dynamic agent sessions are not independent")
    _require(
        sum(float(value["elapsed_seconds"]) for value in full_runs.values())
        <= float(protocol["budgets"]["agent_seconds_per_condition"]),
        "dynamic full agent exceeded the frozen condition time budget",
    )
    _require(
        sum(float(value["elapsed_seconds"]) for value in baseline_runs.values())
        <= float(protocol["budgets"]["agent_seconds_per_condition"]),
        "dynamic baseline agent exceeded the frozen condition time budget",
    )
    _require(
        float(organization["precision_wilson_lower"])
        >= float(statistics["relation_precision_wilson_lower_min"])
        and float(organization["recall_wilson_lower"])
        >= float(statistics["relation_recall_wilson_lower_min"]),
        "dynamic semantic relations did not reach the frozen quality thresholds",
    )
    _require(equivalent, "organization ablation regressed beyond the frozen equivalence margin")
    _require(advantage, "organization ablation showed no frozen meaningful advantage")


def _validate_dynamic_reports(
    dynamic_path: Path,
    dynamic: dict[str, Any],
    ablation_path: Path,
    ablation: dict[str, Any],
    *,
    candidate: str,
    binary_sha256: str,
    repository: Path,
) -> dict[str, Path]:
    _validate_bound_report(
        dynamic,
        schema="ownward.dynamic-acceptance-report/v1",
        candidate=candidate,
        binary_sha256=binary_sha256,
        label="dynamic",
    )
    _validate_bound_report(
        ablation,
        schema="ownward.organization-ablation-report/v1",
        candidate=candidate,
        binary_sha256=binary_sha256,
        label="organization ablation",
    )
    task_classes = {"cross_time", "multi_hop", "context_applicability", "information_update"}
    _require_checks(
        dynamic,
        {
            "post-freeze-random-generation",
            "independent-expression-validation",
            "required-scope-and-task-coverage",
            "asset-identity-content-integrity",
            "semantic-relation-quality",
            "external-agent-no-bypass-and-budget",
            *(f"dynamic-{value}" for value in task_classes),
        },
        "dynamic",
    )
    _require_checks(
        ablation,
        {
            "same-candidate-binary-and-dataset",
            "only-relation-organization-disabled",
            "per-class-no-regression-beyond-equivalence",
            "meaningful-organization-advantage",
            "complete-quality-latency-and-cost-accounting",
        },
        "organization ablation",
    )
    protocol = repository / "benchmarks" / "acceptance" / "dynamic" / "protocol.json"
    _require(protocol.is_file(), "frozen dynamic protocol is missing")
    protocol_value = _load(protocol)
    _dynamic_common.validate_protocol(protocol_value)
    protocol_sha256 = _sha256(protocol)
    _require(dynamic.get("protocol_sha256") == protocol_sha256, "dynamic report used another protocol")
    _require(ablation.get("protocol_sha256") == protocol_sha256, "organization ablation used another protocol")
    _require(dynamic.get("statistics") == protocol_value.get("statistics"), "dynamic report changed the frozen statistics")
    _require(dynamic.get("budgets") == protocol_value.get("budgets"), "dynamic report changed the frozen budgets")
    _require(dynamic.get("generator") == protocol_value.get("models", {}).get("generator"), "dynamic report changed the generator")
    _require(dynamic.get("validator") == protocol_value.get("models", {}).get("validator"), "dynamic report changed the validator")
    _require(dynamic.get("external_agent") == protocol_value.get("models", {}).get("external_agent"), "dynamic report changed the external agent")
    _require(
        dynamic.get("codex_cli")
        == {
            "version": protocol_value.get("runtime", {}).get("codex_cli_version"),
            "sha256": dynamic.get("evidence", {}).get("codex_binary", {}).get("sha256"),
        },
        "dynamic report changed the frozen Codex CLI",
    )
    _require(set(protocol_value.get("generation", {}).get("task_classes", [])) == task_classes, "dynamic protocol task classes changed")
    _require(dynamic.get("dataset_sha256") == ablation.get("dataset_sha256"), "dynamic and ablation reports used different data")
    _require(ablation.get("dynamic_report_sha256") == _sha256(dynamic_path), "organization ablation is not bound to the dynamic report")
    _require(
        ablation.get("only_difference") == ["relation organization state", "relation signals", "relation navigation"]
        and ablation.get("full_disable_relations") is False
        and ablation.get("baseline_disable_relations") is True,
        "organization ablation changed more than relation organization",
    )
    generator = dynamic.get("generator")
    validator = dynamic.get("validator")
    _require(isinstance(generator, dict) and isinstance(validator, dict), "dynamic model roles are missing")
    _require(generator.get("model") != validator.get("model"), "dynamic generator and validator are not independent")
    tasks = dynamic.get("tasks")
    organization = dynamic.get("organization")
    statistics = dynamic.get("statistics")
    _require(isinstance(tasks, dict) and set(tasks) == task_classes, "dynamic task coverage changed")
    _require(isinstance(organization, dict) and isinstance(statistics, dict), "dynamic metrics are incomplete")
    for task_class, metrics in tasks.items():
        _require(isinstance(metrics, dict), f"dynamic task {task_class} is invalid")
        _require(
            float(metrics.get("wilson_lower", 0)) >= float(statistics.get("dynamic_task_success_wilson_lower_min", 1)),
            f"dynamic task {task_class} is below its frozen confidence threshold",
        )
    _require(
        float(organization.get("precision_wilson_lower", 0)) >= float(statistics.get("relation_precision_wilson_lower_min", 1))
        and float(organization.get("recall_wilson_lower", 0)) >= float(statistics.get("relation_recall_wilson_lower_min", 1)),
        "dynamic organization quality is below its frozen confidence threshold",
    )
    class_results = ablation.get("task_classes")
    _require(isinstance(class_results, dict) and set(class_results) == task_classes, "organization ablation task classes changed")
    _require(all(isinstance(value, dict) and value.get("equivalent") is True for value in class_results.values()), "an ablation task class regressed")
    _require(any(value.get("meaningful_advantage") is True for value in class_results.values()), "organization produced no meaningful advantage")
    expected_ablation_statistics = {
        "equivalence_margin": protocol_value.get("statistics", {}).get("ablation_equivalence_margin"),
        "minimum_quality_gain": protocol_value.get("statistics", {}).get("minimum_quality_gain"),
        "minimum_latency_or_cost_reduction": protocol_value.get("statistics", {}).get("minimum_latency_or_cost_reduction"),
    }
    _require(ablation.get("statistics") == expected_ablation_statistics, "organization ablation changed the frozen statistics")
    for label in ("full_total_cost", "baseline_total_cost"):
        cost = ablation.get(label)
        _require(
            isinstance(cost, dict)
            and {"ingestion_operations", "semantic_model_calls", "agent_tool_calls", "organization_seconds", "agent_seconds"} <= set(cost),
            f"{label} is incomplete",
        )
    evidence = _validate_dynamic_evidence(dynamic, task_classes)
    _require(
        evidence["codex_binary"].suffix.lower() not in {".ps1", ".cmd", ".bat", ".js"},
        "dynamic evidence binds a Codex launcher instead of the native executable",
    )
    random_source = _load(evidence["random"])
    _require(
        random_source.get("method") == "secrets.token_hex(32)"
        and re.fullmatch(r"[0-9a-f]{64}", str(random_source.get("seed", ""))) is not None,
        "dynamic random source is invalid",
    )
    hidden = _load(evidence["hidden"])
    expression = _load(evidence["expression"])
    validation = _load(evidence["validation"])
    dataset = _load(evidence["dataset"])
    _require(
        _dynamic_common.merge_valid_dataset(hidden, expression, validation, protocol_value) == dataset,
        "dynamic valid dataset cannot be reproduced from hidden truth and independent validation",
    )
    _require(
        dynamic.get("generated_scenarios") == protocol_value.get("generation", {}).get("generated_scenarios")
        and dynamic.get("valid_scenarios") == len(dataset.get("valid_scenarios", []))
        and dynamic.get("rejected_scenarios") == len(dataset.get("rejected_scenarios", [])),
        "dynamic scenario counts do not match the frozen evidence",
    )
    dataset_run = _load(evidence["dataset_run"])
    expected_dataset_run = {
        "schema": "ownward.dynamic-dataset-run/v1",
        "candidate": candidate,
        "protocol_sha256": protocol_sha256,
        "codex_cli_version": protocol_value["runtime"]["codex_cli_version"],
        "codex_binary_sha256": _sha256(evidence["codex_binary"]),
        "generator": protocol_value["models"]["generator"],
        "validator": protocol_value["models"]["validator"],
        "random_source_sha256": _sha256(evidence["random"]),
        "hidden_truth_sha256": _sha256(evidence["hidden"]),
        "expression_sha256": _sha256(evidence["expression"]),
        "validation_sha256": _sha256(evidence["validation"]),
        "dataset_sha256": _sha256(evidence["dataset"]),
    }
    _require(dataset_run == expected_dataset_run, "dynamic dataset run binding changed")
    stage_prompts = {
        "hidden": _dynamic_common.generation_prompt(protocol_value, str(random_source["seed"])),
        "expression": _dynamic_common.expression_prompt(hidden),
        "validation": _dynamic_common.validation_prompt(hidden, expression),
    }
    for prefix, role in (("hidden", "generator"), ("expression", "generator"), ("validation", "validator")):
        run = _load(evidence[f"{prefix}_run"])
        binding = run.get("binding")
        expected_model = protocol_value["models"][role]
        _require(
            run.get("schema") == "ownward.dynamic-dataset-stage/v1"
            and isinstance(binding, dict)
            and binding.get("candidate") == candidate
            and binding.get("protocol_sha256") == protocol_sha256
            and binding.get("codex_binary_sha256") == _sha256(evidence["codex_binary"])
            and binding.get("model") == expected_model["model"]
            and binding.get("reasoning_effort") == expected_model["reasoning_effort"]
            and binding.get("prompt_sha256") == hashlib.sha256(stage_prompts[prefix].encode("utf-8")).hexdigest()
            and run.get("output_sha256") == _sha256(evidence[prefix])
            and run.get("events_sha256") == _sha256(evidence[f"{prefix}_events"]),
            f"dynamic {prefix} stage binding changed",
        )
        _validate_dynamic_generation_trace(evidence[f"{prefix}_events"])
    full_mapping = _load(evidence["full_mapping"])
    baseline_mapping = _load(evidence["baseline_mapping"])
    _validate_dynamic_mapping(dataset, full_mapping, condition="full", disable_relations=False)
    _validate_dynamic_mapping(dataset, baseline_mapping, condition="baseline", disable_relations=True)
    _require(
        baseline_mapping.get("source_mapping_sha256") == _sha256(evidence["full_mapping"])
        and baseline_mapping.get("stable_ids") == full_mapping.get("stable_ids")
        and baseline_mapping.get("revisions") == full_mapping.get("revisions")
        and baseline_mapping.get("operation_count") == full_mapping.get("operation_count")
        and baseline_mapping.get("logical_semantic_model_calls") == full_mapping.get("logical_semantic_model_calls")
        and baseline_mapping.get("organization_seconds") == full_mapping.get("organization_seconds")
        and baseline_mapping.get("organization_seconds_max") == full_mapping.get("organization_seconds_max"),
        "organization ablation did not reuse the identical frozen non-relation state",
    )
    semantic_provider = dynamic.get("semantic_provider")
    _require(isinstance(semantic_provider, dict), "dynamic semantic provider binding is missing")
    _require(
        {
            "chat_model": semantic_provider.get("chat_model"),
            "embedding_model": semantic_provider.get("embedding_model"),
            "embedding_dimensions": semantic_provider.get("embedding_dimensions"),
        }
        == protocol_value.get("semantic_provider"),
        "dynamic semantic provider differs from the frozen protocol",
    )
    expected_ingestion_binding = {
        "candidate": candidate,
        "release_binary_sha256": binary_sha256,
        "protocol_sha256": protocol_sha256,
        "dataset_sha256": _sha256(evidence["dataset"]),
        "model_base_url_sha256": semantic_provider.get("base_url_sha256"),
        "chat_model": semantic_provider.get("chat_model"),
        "embedding_model": semantic_provider.get("embedding_model"),
        "embedding_dimensions": semantic_provider.get("embedding_dimensions"),
    }
    _require(
        full_mapping.get("binding") == expected_ingestion_binding
        and baseline_mapping.get("binding") == expected_ingestion_binding,
        "dynamic ingestion binding changed",
    )
    asset_snapshot = _load(evidence["asset_integrity"])
    _require(asset_snapshot.get("schema") == "ownward.dynamic-asset-integrity/v1", "dynamic asset evidence schema changed")
    expected_assets: dict[str, tuple[int, str]] = {}
    for scenario in dataset["valid_scenarios"]:
        scenario_id = str(scenario["truth"]["id"])
        updates = {str(value["node_id"]): str(value["content"]) for value in scenario["expression"]["updates"]}
        for value in scenario["expression"]["information"]:
            node_id = str(value["node_id"])
            content = updates.get(node_id, str(value["content"]))
            expected_assets[_dynamic_scenario_key(scenario_id, node_id)] = (
                2 if node_id in updates else 1,
                hashlib.sha256(content.encode("utf-8")).hexdigest(),
            )
    for condition, mapping in (("full", full_mapping), ("baseline", baseline_mapping)):
        values = asset_snapshot.get(condition)
        _require(isinstance(values, list), f"dynamic {condition} asset evidence is missing")
        by_key = {str(value.get("key", "")): value for value in values if isinstance(value, dict)}
        _require(set(by_key) == set(expected_assets) and len(values) == len(expected_assets), f"dynamic {condition} asset evidence is incomplete")
        for key, (revision, content_sha256) in expected_assets.items():
            value = by_key[key]
            _require(
                value.get("id") == mapping["stable_ids"][key]
                and value.get("revision") == revision
                and value.get("content_sha256") == content_sha256,
                f"dynamic {condition} asset integrity changed: {key}",
            )
        summary = dynamic.get("asset_integrity", {}).get(condition, {})
        _require(
            summary.get("checked") == len(expected_assets)
            and summary.get("unique_ids") == len(expected_assets)
            and summary.get("passed") is True,
            f"dynamic {condition} asset summary differs from raw evidence",
        )
    organization_snapshot = _load(evidence["organization"])
    _require(organization_snapshot.get("schema") == "ownward.dynamic-organization-evidence/v1", "dynamic organization evidence schema changed")
    expected_relations: set[tuple[str, str, str]] = set()
    for scenario in dataset["valid_scenarios"]:
        scenario_id = str(scenario["truth"]["id"])
        for relation in scenario["truth"]["relations"]:
            expected_relations.add(
                _dynamic_common.canonical_relation(
                    _dynamic_scenario_key(scenario_id, str(relation["source_id"])),
                    str(relation["type"]),
                    _dynamic_scenario_key(scenario_id, str(relation["target_id"])),
                )
            )
    snapshot_expected = {tuple(str(part) for part in value) for value in organization_snapshot.get("expected", [])}
    snapshot_actual = {tuple(str(part) for part in value) for value in organization_snapshot.get("actual", [])}
    _require(snapshot_expected == expected_relations, "dynamic organization truth differs from the frozen dataset")
    true_positive = len(snapshot_expected & snapshot_actual)
    confidence = float(protocol_value["statistics"]["confidence_level"])
    expected_organization = {
        "expected": len(snapshot_expected),
        "actual": len(snapshot_actual),
        "true_positive": true_positive,
        "precision": true_positive / len(snapshot_actual) if snapshot_actual else 0.0,
        "recall": true_positive / len(snapshot_expected) if snapshot_expected else 0.0,
        "precision_wilson_lower": _dynamic_common.wilson_lower(true_positive, len(snapshot_actual), confidence)
        if snapshot_actual
        else 0.0,
        "recall_wilson_lower": _dynamic_common.wilson_lower(true_positive, len(snapshot_expected), confidence)
        if snapshot_expected
        else 0.0,
    }
    for name, expected in expected_organization.items():
        _same_metric(dynamic["organization"].get(name), float(expected), f"dynamic organization {name}")
    full_runs: dict[str, dict[str, Any]] = {}
    baseline_runs: dict[str, dict[str, Any]] = {}
    for task_class in task_classes:
        full = _recompute_dynamic_run(
            condition="full",
            task_class=task_class,
            dataset=dataset,
            mapping=full_mapping,
            evidence=evidence,
            protocol=protocol_value,
            expected_binding={
                "candidate": candidate,
                "release_binary_sha256": binary_sha256,
                "protocol_sha256": protocol_sha256,
                "dataset_sha256": _sha256(evidence["dataset"]),
                "mapping_sha256": _sha256(evidence["full_mapping"]),
                "codex_binary_sha256": _sha256(evidence["codex_binary"]),
            },
        )
        baseline = _recompute_dynamic_run(
            condition="baseline",
            task_class=task_class,
            dataset=dataset,
            mapping=baseline_mapping,
            evidence=evidence,
            protocol=protocol_value,
            expected_binding={
                "candidate": candidate,
                "release_binary_sha256": binary_sha256,
                "protocol_sha256": protocol_sha256,
                "dataset_sha256": _sha256(evidence["dataset"]),
                "mapping_sha256": _sha256(evidence["baseline_mapping"]),
                "codex_binary_sha256": _sha256(evidence["codex_binary"]),
            },
        )
        full_runs[task_class] = full
        baseline_runs[task_class] = baseline
        reported = dynamic["tasks"][task_class]
        for name in ("successes", "total", "success_rate", "tool_calls", "search_calls", "elapsed_seconds"):
            _same_metric(reported.get(name), float(full[name]), f"dynamic {task_class} {name}")
        _require(
            reported.get("session_id") == full["session_id"] and reported.get("prompt_sha256") == full["prompt_sha256"],
            f"dynamic {task_class} agent identity differs from raw evidence",
        )
        expected_wilson = _dynamic_common.wilson_lower(full["successes"], full["total"], confidence)
        _same_metric(reported.get("wilson_lower"), expected_wilson, f"dynamic {task_class} Wilson bound")
        quality_gain = full["success_rate"] - baseline["success_rate"]
        latency_reduction = 1 - full["elapsed_seconds"] / baseline["elapsed_seconds"]
        full_operation_cost = full["tool_calls"] + full["search_calls"]
        baseline_operation_cost = baseline["tool_calls"] + baseline["search_calls"]
        cost_reduction = 1 - full_operation_cost / baseline_operation_cost if baseline_operation_cost else 0.0
        statistics = protocol_value["statistics"]
        equivalent = quality_gain >= -float(statistics["ablation_equivalence_margin"])
        meaningful_advantage = quality_gain >= float(statistics["minimum_quality_gain"]) or (
            equivalent
            and (
                latency_reduction >= float(statistics["minimum_latency_or_cost_reduction"])
                or cost_reduction >= float(statistics["minimum_latency_or_cost_reduction"])
            )
        )
        class_report = ablation["task_classes"][task_class]
        expected_class_metrics = {
            "full_success_rate": full["success_rate"],
            "baseline_success_rate": baseline["success_rate"],
            "quality_gain": quality_gain,
            "full_elapsed_seconds": full["elapsed_seconds"],
            "baseline_elapsed_seconds": baseline["elapsed_seconds"],
            "latency_reduction": latency_reduction,
            "full_tool_and_search_cost": full_operation_cost,
            "baseline_tool_and_search_cost": baseline_operation_cost,
            "cost_reduction": cost_reduction,
        }
        for name, expected in expected_class_metrics.items():
            _same_metric(class_report.get(name), float(expected), f"organization ablation {task_class} {name}")
        _require(
            class_report.get("equivalent") is equivalent
            and class_report.get("meaningful_advantage") is meaningful_advantage,
            f"organization ablation {task_class} conclusion differs from raw evidence",
        )
    expected_total_costs = {
        "full_total_cost": {
            "ingestion_operations": full_mapping["operation_count"],
            "semantic_model_calls": full_mapping["logical_semantic_model_calls"]
            + sum(value["search_calls"] for value in full_runs.values()),
            "agent_tool_calls": sum(value["tool_calls"] for value in full_runs.values()),
            "organization_seconds": full_mapping["organization_seconds"],
            "agent_seconds": sum(value["elapsed_seconds"] for value in full_runs.values()),
        },
        "baseline_total_cost": {
            "ingestion_operations": baseline_mapping["operation_count"],
            "semantic_model_calls": baseline_mapping["logical_semantic_model_calls"]
            + sum(value["search_calls"] for value in baseline_runs.values()),
            "agent_tool_calls": sum(value["tool_calls"] for value in baseline_runs.values()),
            "organization_seconds": baseline_mapping["organization_seconds"],
            "agent_seconds": sum(value["elapsed_seconds"] for value in baseline_runs.values()),
        },
    }
    for label, expected in expected_total_costs.items():
        for name, value in expected.items():
            _same_metric(ablation[label].get(name), float(value), f"{label} {name}")
    _require_dynamic_outcomes(
        full_runs=full_runs,
        baseline_runs=baseline_runs,
        organization=expected_organization,
        protocol=protocol_value,
    )
    random_source = _load(evidence["random"])
    _require(
        random_source.get("method") == "secrets.token_hex(32)"
        and re.fullmatch(r"[0-9a-f]{64}", str(random_source.get("seed", ""))) is not None,
        "dynamic random source is invalid",
    )
    _require(dynamic.get("random_source_sha256") == _sha256(evidence["random"]), "dynamic random source changed")
    _require(dynamic.get("hidden_truth_sha256") == _sha256(evidence["hidden"]), "dynamic hidden truth changed")
    _require(dynamic.get("dataset_sha256") == _sha256(evidence["dataset"]), "dynamic valid dataset changed")
    return evidence


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
    parser.add_argument("--dynamic-report", type=Path, required=True)
    parser.add_argument("--organization-ablation-report", type=Path, required=True)
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
        args.dynamic_report,
        args.organization_ablation_report,
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
    dynamic = _load(args.dynamic_report.resolve())
    organization_ablation = _load(args.organization_ablation_report.resolve())
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
    dynamic_evidence = _validate_dynamic_reports(
        args.dynamic_report.resolve(),
        dynamic,
        args.organization_ablation_report.resolve(),
        organization_ablation,
        candidate=candidate,
        binary_sha256=binary_sha256,
        repository=args.repository.resolve(),
    )

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
        "dynamic_report": args.dynamic_report.resolve(),
        "organization_ablation_report": args.organization_ablation_report.resolve(),
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
    artifacts.update({f"dynamic_{name}": path for name, path in dynamic_evidence.items()})
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
            {"name": "dynamic-unseen-generality", "passed": True},
            {"name": "organization-structure-advantage", "passed": True},
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
