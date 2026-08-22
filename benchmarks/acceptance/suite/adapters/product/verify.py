#!/usr/bin/env python3
from __future__ import annotations

import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
from typing import Any

HERE = Path(__file__).resolve().parent
SUITE = HERE.parents[1]
REPOSITORY = SUITE.parents[2]


def _load_module(name: str, path: Path) -> Any:
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load adapter: {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


def require(condition: bool, message: str) -> None:
    if not condition:
        raise RuntimeError(message)


codex_session = _load_module("ownward_suite_codex_session", HERE / "codex_session.py")
resource = _load_module("ownward_suite_resource", SUITE / "adapters" / "product_resource" / "verify.py")
support = _load_module("ownward_suite_mcp_support", REPOSITORY / "benchmarks" / "support" / "ownward_mcp.py")
process_control = _load_module("ownward_suite_process_control", SUITE / "process_control.py")


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def json_sha256(value: Any) -> str:
    encoded = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def load_json(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    require(isinstance(value, dict), f"JSON document is not an object: {path}")
    return value


def write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    temporary.replace(path)


def _safe_reset(path: Path, parent: Path) -> None:
    if not path.exists():
        return
    resolved = path.resolve()
    require(resolved.parent == parent.resolve(), f"refusing to clear unexpected path: {resolved}")
    shutil.rmtree(resolved)


def _cleanup_temporary(path: Path) -> None:
    last_error: OSError | None = None
    for _ in range(20):
        try:
            shutil.rmtree(path)
            return
        except FileNotFoundError:
            return
        except OSError as error:
            last_error = error
            time.sleep(0.1)
    assert last_error is not None
    raise last_error


def _codex_command(
    args: argparse.Namespace,
    *,
    work_dir: Path,
    schema_path: Path,
    output_path: Path,
    endpoint: str,
) -> list[str]:
    command = codex_session.command_prefix(args.codex_binary) + [
        "exec", "--ephemeral", "--json", "--color", "never", "--skip-git-repo-check",
        "-C", str(work_dir), "--sandbox", "read-only", "-m", args.codex_model,
        "-c", f"model_reasoning_effort={json.dumps(args.codex_reasoning_effort)}",
        "-c", "project_doc_max_bytes=0",
    ]
    for feature in (
        "apply_patch_freeform", "apps", "image_generation", "js_repl", "memories", "multi_agent",
        "personality", "plugins", "request_permissions_tool", "search_tool", "shell_snapshot",
        "shell_tool", "tool_search", "tool_suggest",
    ):
        command.extend(["-c", f"features.{feature}=false"])
    command.extend([
        "-c", 'web_search="disabled"',
        "-c", f"mcp_servers.ownward.url={json.dumps(endpoint)}",
        "-c", 'mcp_servers.ownward.bearer_token_env_var="OWNWARD_MCP_BEARER_TOKEN"',
        "-c", 'mcp_servers.ownward.tools.ownward_semantic_submit.approval_mode="approve"',
        "-c", 'mcp_servers.ownward.tools.ownward_semantic_submit_batch.approval_mode="approve"',
        "--output-schema", str(schema_path), "-o", str(output_path), "-",
    ])
    return command


def _run_codex(
    args: argparse.Namespace,
    *,
    stage: Path,
    prompt: str,
    schema: dict[str, Any],
    endpoint: str,
    bearer_token: str,
    timeout_seconds: float,
) -> tuple[dict[str, Any], Any, float]:
    stage.mkdir(parents=True, exist_ok=True)
    output = stage / "output.json"
    events = stage / "events.jsonl"
    work = stage / "work"
    require(not output.exists() and not events.exists() and not work.exists(), f"Codex stage is not blank: {stage}")
    work.mkdir()
    temporary_root = Path(tempfile.mkdtemp(prefix="codex-", dir=stage))
    failed = False
    try:
        schema_path = temporary_root / "schema.json"
        schema_path.write_text(json.dumps(schema, ensure_ascii=False), encoding="utf-8")
        environment = codex_session.isolated_environment(args.codex_auth_file, temporary_root / "codex-home")
        environment["OWNWARD_MCP_BEARER_TOKEN"] = bearer_token
        started = time.perf_counter()
        try:
            completed = process_control.run(
                _codex_command(args, work_dir=work, schema_path=schema_path, output_path=output, endpoint=endpoint),
                cwd=work,
                input_text=prompt,
                timeout=timeout_seconds,
                env=environment,
            )
        except process_control.ProcessTimeout as error:
            events.write_text(error.stdout, encoding="utf-8")
            (stage / "stderr.txt").write_text(error.stderr, encoding="utf-8")
            detail = error.stderr[-1000:].strip()
            message = "Codex stage exceeded its wall-clock budget and its process tree was stopped"
            raise RuntimeError(f"{message}: {detail}" if detail else message) from error
        elapsed = time.perf_counter() - started
    except Exception:
        failed = True
        raise
    finally:
        try:
            _cleanup_temporary(temporary_root)
        except OSError:
            if not failed:
                raise
    events.write_text(completed.stdout, encoding="utf-8")
    require(completed.returncode == 0, f"Codex stage failed: {completed.stderr[-2000:]}")
    require(output.is_file(), "Codex stage produced no structured output")
    return load_json(output), codex_session.load_exec_events(completed.stdout), elapsed


RELATION_CONTRACT = {
    "direction": "Every relation is stated as source_id TYPE target_id.",
    "types": {
        "same_as": "The source and target express the same underlying information.",
        "broader_than": "The source is a broader category or concept than the target.",
        "narrower_than": "The source is a narrower category or concept than the target.",
        "part_of": "The source is a component of the target mechanism, structure, process, system, or topic.",
        "has_part": "The source contains the target as a component.",
        "supports": "The source provides evidence, a mechanism, a condition, a method, or a solution for the target.",
        "contradicts": "The source and target make claims that cannot both hold in the stated context.",
        "derived_from": "The source conclusion, choice, or practice is derived from the target basis.",
        "applies_in": "The source is applicable in the context represented by the target.",
        "related_to": "The source and target have a direct semantic relation for which no other type is accurate.",
    },
}


def _semantic_prompt(asset_ids: list[str], args: argparse.Namespace) -> str:
    return f"""Act only as Ownward's external semantic capability. Use only the connected Ownward tools.

Call `ownward_semantic_work` once with exactly these asset IDs:
{json.dumps(asset_ids, ensure_ascii=False)}

Analyze only the returned assets and candidate contexts. Do not infer from a query, expected answer, test truth, or outside knowledge. Submit exactly one result for every work item through `ownward_semantic_submit_batch` using schema `ownward.semantic-submission/v1`, capability id `codex`, capability version `{args.codex_model}`, and execution `ownward-product-dataset-v1`. Correct and retry only rejected items, at most twice.

Use this relation contract exactly:
{json.dumps(RELATION_CONTRACT, ensure_ascii=False, separators=(',', ':'))}

Relations must target candidates supplied for the same work item and must cite explicit evidence. Prefer the single most precise relation; topical similarity alone is not a relation. If the content is understandable but no reliable relation exists, submit complete with no relation. Submit uncertain only when the asset's basic meaning cannot be understood reliably.

Return only the number processed and the number submitted as uncertain."""


def _query_prompt(question: str) -> str:
    return f"""Use only the connected Ownward read tools. Do not use shell, files, web, prior knowledge, or mutation tools.

Answer this question by actively searching Ownward, following useful relations when needed, and reading every item used as evidence:
{question}

Return `information_ids` containing only the stable IDs that jointly support the answer, and `answer_facts` containing the exact complete fact sentences from those items. Do not include irrelevant or merely related facts. Use no more than eight Ownward tool calls."""


SEMANTIC_SCHEMA = {
    "type": "object", "additionalProperties": False, "required": ["processed", "uncertain"],
    "properties": {"processed": {"type": "integer", "minimum": 0}, "uncertain": {"type": "integer", "minimum": 0}},
}
ANSWER_SCHEMA = {
    "type": "object", "additionalProperties": False, "required": ["information_ids", "answer_facts"],
    "properties": {
        "information_ids": {"type": "array", "items": {"type": "string"}, "uniqueItems": True},
        "answer_facts": {"type": "array", "items": {"type": "string"}, "uniqueItems": True},
    },
}


def _strings(value: Any) -> list[str]:
    if isinstance(value, dict):
        return [text for item in value.values() for text in _strings(item)]
    if isinstance(value, list):
        return [text for item in value for text in _strings(item)]
    return [value] if isinstance(value, str) else []


def _observed(trace: Any, call_names: set[str] | None = None) -> dict[str, list[str]]:
    evidence: dict[str, list[str]] = {}

    def visit(value: Any) -> None:
        if isinstance(value, dict):
            identifier = value.get("id")
            if isinstance(identifier, str) and identifier:
                evidence.setdefault(identifier, []).extend(_strings(value))
            for nested in value.values():
                visit(nested)
        elif isinstance(value, list):
            for nested in value:
                visit(nested)

    for call in trace.calls:
        if not call.error and (call_names is None or call.name in call_names):
            visit(call.result)
    return evidence


def _resource_values(report: dict[str, Any], candidate: str, binary_sha256: str) -> tuple[float, bool, float]:
    require(report.get("schema") == "ownward.delivery-resource-report/v1", "resource report schema is invalid")
    require(report.get("candidate") == candidate and report.get("release_binary_sha256") == binary_sha256, "resource report belongs to another candidate")
    checks = {item.get("name"): item for item in report.get("checks", []) if isinstance(item, dict)}
    working = checks.get("working-resources", {})
    throughput = checks.get("embedding-throughput", {})
    peak = float(working.get("actual_peak_rss_mib", -1))
    query_limit = float(throughput.get("query_maximum_ms", -1))
    require(peak >= 0 and query_limit > 0, "resource report lacks working-set or query budget evidence")
    return peak, report.get("passed") is True, query_limit


def _scenario_binding(args: argparse.Namespace, task: dict[str, Any], binding: dict[str, Any], resource_sha: str) -> dict[str, Any]:
    return {
        "suite_version": binding["suite_version"], "candidate": binding["candidate"],
        "binary_sha256": binding["binary_sha256"], "environment_sha256": binding["environment_sha256"],
        "input_manifest_sha256": binding["input_manifest_sha256"], "tool_sha256": binding["tool_sha256"],
        "task_sha256": json_sha256(task), "resource_report_sha256": resource_sha,
        "codex_binary_sha256": sha256(args.codex_binary), "codex_model": args.codex_model,
        "codex_reasoning_effort": args.codex_reasoning_effort,
    }


def _scenario_evidence_files(has_updates: bool) -> tuple[str, ...]:
    semantic = ["semantic-initial/output.json", "semantic-initial/events.jsonl"]
    if has_updates:
        semantic.extend(["semantic-update/output.json", "semantic-update/events.jsonl"])
    return (*semantic, "query/output.json", "query/events.jsonl")


def _scenario_evidence(scenario_root: Path, has_updates: bool) -> dict[str, str]:
    evidence: dict[str, str] = {}
    for relative in _scenario_evidence_files(has_updates):
        path = scenario_root / relative
        require(path.is_file() and not path.is_symlink(), f"scenario evidence is missing: {relative}")
        evidence[relative] = sha256(path)
    return evidence


def _sealed_scenario_valid(
    sealed: dict[str, Any], scenario_root: Path, binding: dict[str, Any], has_updates: bool
) -> bool:
    try:
        require(sealed.get("schema") == "ownward.product-scenario-checkpoint/v1", "scenario checkpoint schema is invalid")
        require(sealed.get("binding") == binding and isinstance(sealed.get("result"), dict), "scenario checkpoint binding is invalid")
        require(sealed.get("evidence") == _scenario_evidence(scenario_root, has_updates), "scenario raw evidence changed")
    except RuntimeError:
        return False
    return True


def _complete_semantics(
    args: argparse.Namespace,
    runtime: Any,
    scenario_root: Path,
    stage_name: str,
    asset_ids: list[str],
    deadline: float,
) -> float:
    remaining = deadline - time.monotonic()
    require(remaining > 0, "product execution exceeded its total budget")
    semantic, semantic_trace, elapsed = _run_codex(
        args,
        stage=scenario_root / stage_name,
        prompt=_semantic_prompt(asset_ids, args),
        schema=SEMANTIC_SCHEMA,
        endpoint=runtime.binding.endpoint,
        bearer_token=runtime.binding.bearer_token,
        timeout_seconds=min(args.stage_timeout, remaining),
    )
    require(int(semantic.get("processed", -1)) == len(asset_ids), "semantic capability did not process every asset")
    require(not semantic_trace.bypassed and semantic_trace.calls, "semantic capability bypassed Ownward")
    require(
        all(
            call.name in {"ownward_semantic_work", "ownward_semantic_submit", "ownward_semantic_submit_batch"}
            and not call.error
            for call in semantic_trace.calls
        ),
        "semantic capability used an invalid path",
    )
    for stable_id in asset_ids:
        status = runtime.client.call_tool("ownward_status", {"id": stable_id})
        require(status.get("organization", {}).get("status") in {"ready", "uncertain"}, "semantic work did not reach a terminal state")
    return elapsed


def _run_scenario(
    args: argparse.Namespace,
    task: dict[str, Any],
    binding: dict[str, Any],
    peak_mib: float,
    resource_passed: bool,
    query_limit_ms: float,
    resource_sha: str,
    deadline: float,
) -> dict[str, Any]:
    scenario_root = args.evidence_dir / str(task["scenario_id"])
    result_path = scenario_root / "result.json"
    expected_binding = _scenario_binding(args, task, binding, resource_sha)
    if result_path.is_file():
        sealed = load_json(result_path)
        if _sealed_scenario_valid(sealed, scenario_root, expected_binding, bool(task["updates"])):
            return dict(sealed["result"])
        require(args.resume, f"scenario {task['scenario_id']} evidence is stale; use --resume to replace it")
        _safe_reset(scenario_root, args.evidence_dir)
    if scenario_root.exists():
        require(args.resume, f"scenario {task['scenario_id']} is incomplete; use --resume")
        _safe_reset(scenario_root, args.evidence_dir)
    scenario_root.mkdir(parents=True)
    scenario_started = time.perf_counter()
    data_dir = scenario_root / "data"
    environment = os.environ.copy()
    stable_by_node: dict[str, str] = {}
    revisions: dict[str, int] = {}
    binary = args.binary
    with support.OwnwardRuntime(binary, data_dir, environment) as runtime:
        require(runtime.client is not None and runtime.binding is not None and runtime.process is not None, "Ownward runtime did not start")
        with resource.TreeSampler(runtime.process.pid) as sampler:
            items = task["information"]
            created = runtime.client.call_tool("ownward_create_batch", {"items": [
                {"content": item["content"], "source": {"actor": "acceptance-suite", "ref": item["node_id"]}}
                for item in items
            ]})
            created_results = created.get("results") if isinstance(created, dict) else None
            require(isinstance(created_results, list) and len(created_results) == len(items), "create batch is incomplete")
            for item, value in zip(items, created_results):
                mutation = value.get("result") if isinstance(value, dict) else None
                information = mutation.get("information") if isinstance(mutation, dict) else None
                require(isinstance(information, dict), "create batch item failed")
                stable_by_node[str(item["node_id"])] = str(information["id"])
                revisions[str(item["node_id"])] = int(information["revision"])
            semantic_seconds = _complete_semantics(
                args, runtime, scenario_root, "semantic-initial", list(stable_by_node.values()), deadline
            )
            updated_ids: list[str] = []
            for update in task["updates"]:
                node_id = str(update["node_id"])
                changed = runtime.client.call_tool("ownward_update", {
                    "id": stable_by_node[node_id], "expected_revision": revisions[node_id], "content": update["content"],
                })
                mutation = changed.get("result") if isinstance(changed, dict) else None
                information = mutation.get("information") if isinstance(mutation, dict) else None
                require(isinstance(information, dict) and information.get("id") == stable_by_node[node_id], "update changed stable identity")
                revisions[node_id] = int(information["revision"])
                updated_ids.append(stable_by_node[node_id])
            if updated_ids:
                semantic_seconds += _complete_semantics(
                    args, runtime, scenario_root, "semantic-update", updated_ids, deadline
                )
            query_started = time.perf_counter()
            direct = runtime.client.call_tool("ownward_search", {"query": task["query"]["question"], "limit": 10})
            direct_ms = (time.perf_counter() - query_started) * 1000
            remaining = deadline - time.monotonic()
            require(remaining > 0, "product execution exceeded its total budget")
            answer, query_trace, agent_query_seconds = _run_codex(
                args, stage=scenario_root / "query", prompt=_query_prompt(task["query"]["question"]),
                schema=ANSWER_SCHEMA, endpoint=runtime.binding.endpoint, bearer_token=runtime.binding.bearer_token,
                timeout_seconds=min(args.stage_timeout, remaining),
            )
        require(not query_trace.bypassed and query_trace.calls, "query agent bypassed Ownward")
        allowed_calls = {"ownward_rules", "ownward_search", "ownward_read", "ownward_navigate", "ownward_status"}
        require(len(query_trace.calls) <= 8 and all(call.name in allowed_calls and not call.error for call in query_trace.calls), "query agent exceeded or violated its public read-only path")
        observed = _observed(query_trace)
        returned_stable = [str(value) for value in answer.get("information_ids", [])]
        facts = [str(value) for value in answer.get("answer_facts", [])]
        grounded = set(returned_stable) <= set(observed) and all(
            any(fact in text for stable_id in returned_stable for text in observed.get(stable_id, [])) for fact in facts
        )
        reverse = {stable: node for node, stable in stable_by_node.items()}
        require(set(returned_stable) <= set(reverse), "query agent returned evidence outside the frozen scenario")
        direct_values = direct.get("results") if isinstance(direct, dict) else None
        require(isinstance(direct_values, list), "direct search returned invalid results")
        direct_ids = [reverse[str(item["id"])] for item in direct_values if str(item.get("id", "")) in reverse]
        returned_ids = [reverse[value] for value in returned_stable]
        navigation_ids = [
            reverse[value]
            for value in _observed(query_trace, {"ownward_navigate"})
            if value in reverse
        ]
        sampled_peak = max((int(sample.get("rss_bytes", 0)) for sample in sampler.samples), default=0) / (1024 * 1024)
    end_to_end_ms = (time.perf_counter() - scenario_started) * 1000
    result = {
        "scenario_id": task["scenario_id"], "direct_ids": list(dict.fromkeys(direct_ids)),
        "returned_ids": list(dict.fromkeys(returned_ids)), "answer_facts": list(dict.fromkeys(facts)),
        "navigation_ids": list(dict.fromkeys(navigation_ids)),
        "grounded": grounded, "latency_ms": direct_ms,
        "semantic_ms": semantic_seconds * 1000,
        "agent_query_ms": agent_query_seconds * 1000,
        "end_to_end_ms": end_to_end_ms,
        "peak_mib": max(peak_mib, sampled_peak),
        "used_navigation": any(call.name == "ownward_navigate" and not call.error for call in query_trace.calls),
        "within_latency_budget": direct_ms <= query_limit_ms, "within_resource_budget": resource_passed,
    }
    _safe_reset(data_dir, scenario_root)
    _safe_reset(scenario_root / "semantic-initial" / "work", scenario_root / "semantic-initial")
    if task["updates"]:
        _safe_reset(scenario_root / "semantic-update" / "work", scenario_root / "semantic-update")
    _safe_reset(scenario_root / "query" / "work", scenario_root / "query")
    write_json(result_path, {
        "schema": "ownward.product-scenario-checkpoint/v1",
        "binding": expected_binding,
        "evidence": _scenario_evidence(scenario_root, bool(task["updates"])),
        "result": result,
    })
    return result


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--codex-binary", type=Path, required=True)
    parser.add_argument("--codex-auth-file", type=Path, required=True)
    parser.add_argument("--codex-model", default="gpt-5.4-mini")
    parser.add_argument("--codex-reasoning-effort", default="xhigh")
    parser.add_argument("--tasks", type=Path, required=True)
    parser.add_argument("--binding", type=Path, required=True)
    parser.add_argument("--resource-report", type=Path, required=True)
    parser.add_argument("--evidence-dir", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--resume", action="store_true")
    parser.add_argument("--stage-timeout", type=float, default=240)
    parser.add_argument("--max-wall-seconds", type=float, required=True)
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    for name in ("binary", "codex_binary", "codex_auth_file", "tasks", "binding", "resource_report", "evidence_dir", "output"):
        setattr(args, name, getattr(args, name).resolve())
    for path, label in ((args.binary, "binary"), (args.codex_binary, "Codex"), (args.codex_auth_file, "Codex auth")):
        require(path.is_file(), f"{label} file does not exist: {path}")
    args.evidence_dir.mkdir(parents=True, exist_ok=True)
    tasks = load_json(args.tasks)
    binding = load_json(args.binding)
    require(tasks.get("schema") == "ownward.product-tasks/v1", "product tasks schema is invalid")
    require(binding.get("candidate") and sha256(args.binary) == binding.get("binary_sha256"), "candidate binary binding is invalid")
    version = subprocess.run([str(args.binary), "version"], check=True, capture_output=True, text=True, encoding="utf-8", timeout=30).stdout.strip()
    require(version == binding["candidate"], "candidate binary version changed")
    resource_report = load_json(args.resource_report)
    peak_mib, resource_passed, query_limit_ms = _resource_values(resource_report, binding["candidate"], binding["binary_sha256"])
    resource_sha = sha256(args.resource_report)
    deadline = time.monotonic() + args.max_wall_seconds
    results = [
        _run_scenario(args, task, binding, peak_mib, resource_passed, query_limit_ms, resource_sha, deadline)
        for task in tasks["tasks"]
    ]
    envelope = {
        "schema": "ownward.product-results/v1", "dataset_version": tasks["dataset_version"],
        "mode": tasks["mode"], "results": results,
        "evidence": {"resource_report": {"path": str(args.resource_report), "sha256": resource_sha}},
    }
    write_json(args.output, envelope)
    print(json.dumps({"completed": len(results), "output": str(args.output)}, ensure_ascii=False))


if __name__ == "__main__":
    main()
