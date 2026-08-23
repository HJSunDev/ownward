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
    with temporary.open("w", encoding="utf-8", newline="\n") as stream:
        stream.write(json.dumps(value, ensure_ascii=False, indent=2) + "\n")
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(temporary, path)


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
                stdout_path=events,
                stderr_path=stage / "stderr.txt",
            )
        except process_control.ProcessTimeout as error:
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


def _semantic_prompt(asset_id: str, args: argparse.Namespace) -> str:
    return f"""Act only as Ownward's external semantic capability. Use only the connected Ownward tools.

Call `ownward_semantic_work` once with exactly this one asset ID:
{json.dumps([asset_id], ensure_ascii=False)}

Analyze only the returned asset and candidate contexts. Do not infer from a query, expected answer, test truth, or outside knowledge. Immediately submit exactly one result through `ownward_semantic_submit` using schema `ownward.semantic-submission/v1`, capability id `codex`, capability version `{args.codex_model}`, and execution `ownward-product-dataset-v1`. Correct and retry a rejected submission at most twice.

Use this relation contract exactly:
{json.dumps(RELATION_CONTRACT, ensure_ascii=False, separators=(',', ':'))}

Relations must target candidates supplied for the same work item and must cite explicit evidence. Prefer the single most precise relation; topical similarity alone is not a relation. If the content is understandable but no reliable relation exists, submit complete with no relation. Submit uncertain only when the asset's basic meaning cannot be understood reliably.

Return processed=1 and the number submitted as uncertain."""


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


def _tree_manifest(root: Path) -> list[dict[str, Any]]:
    if not root.exists():
        return []
    return [
        {"path": path.relative_to(root).as_posix(), "size": path.stat().st_size, "sha256": sha256(path)}
        for path in sorted(item for item in root.rglob("*") if item.is_file() and not item.is_symlink())
    ]


def _tree_sha256(root: Path) -> str:
    return json_sha256(_tree_manifest(root))


def _evidence_valid(scenario_root: Path, evidence: dict[str, str]) -> bool:
    try:
        for relative, digest in evidence.items():
            path = scenario_root / relative
            require(path.is_file() and not path.is_symlink() and sha256(path) == digest, f"scenario evidence changed: {relative}")
    except RuntimeError:
        return False
    return True


def _progress_evidence_complete(progress: dict[str, Any]) -> bool:
    completed = progress.get("completed_units")
    evidence = progress.get("evidence")
    if not isinstance(completed, dict) or not isinstance(evidence, dict):
        return False
    groups: dict[str, set[str]] = {}
    for relative in evidence:
        path = Path(relative)
        parent = path.parent.as_posix()
        if not (parent.startswith("semantic-initial/") or parent.startswith("semantic-update/")) or "/attempt-" not in parent:
            return False
        groups.setdefault(parent, set()).add(path.name)
    return len(groups) == len(completed) and all(
        names == {"output.json", "events.jsonl", "stderr.txt"}
        for names in groups.values()
    )


def _scenario_evidence_files(has_updates: bool) -> tuple[str, ...]:
    """Legacy v1 checkpoint paths, retained only to validate existing sealed evidence."""
    semantic = ["semantic-initial/output.json", "semantic-initial/events.jsonl"]
    if has_updates:
        semantic.extend(["semantic-update/output.json", "semantic-update/events.jsonl"])
    return (*semantic, "query/output.json", "query/events.jsonl")


def _scenario_evidence(scenario_root: Path, has_updates: bool) -> dict[str, str]:
    result: dict[str, str] = {}
    for relative in _scenario_evidence_files(has_updates):
        path = scenario_root / relative
        require(path.is_file() and not path.is_symlink(), f"scenario evidence is missing: {relative}")
        result[relative] = sha256(path)
    return result


def _sealed_scenario_valid(
    sealed: dict[str, Any], scenario_root: Path, binding: dict[str, Any], has_updates: bool
) -> bool:
    try:
        schema = sealed.get("schema")
        require(schema in {"ownward.product-scenario-checkpoint/v1", "ownward.product-scenario-checkpoint/v2"}, "scenario checkpoint schema is invalid")
        require(sealed.get("binding") == binding and isinstance(sealed.get("result"), dict), "scenario checkpoint binding is invalid")
        evidence = sealed.get("evidence")
        require(isinstance(evidence, dict), "scenario raw evidence is invalid")
        if schema == "ownward.product-scenario-checkpoint/v1":
            require(evidence == _scenario_evidence(scenario_root, has_updates), "legacy scenario raw evidence is incomplete or changed")
        else:
            progress_path = scenario_root / "progress.json"
            require(progress_path.is_file() and sha256(progress_path) == sealed.get("progress_sha256"), "scenario progress checkpoint changed")
            progress = load_json(progress_path)
            progress_evidence = progress.get("evidence")
            require(isinstance(progress_evidence, dict) and _progress_evidence_complete(progress), "scenario progress evidence is incomplete")
            require(all(evidence.get(path) == digest for path, digest in progress_evidence.items()), "scenario checkpoint omits committed semantic evidence")
            query_evidence = {path: digest for path, digest in evidence.items() if path not in progress_evidence}
            query_parents = {Path(path).parent.as_posix() for path in query_evidence}
            query_names = {Path(path).name for path in query_evidence}
            require(
                len(query_parents) == 1
                and next(iter(query_parents)).startswith("query/attempt-")
                and query_names == {"output.json", "events.jsonl", "stderr.txt"},
                "scenario checkpoint lacks one complete query evidence attempt",
            )
        require(_evidence_valid(scenario_root, evidence), "scenario raw evidence changed")
    except RuntimeError:
        return False
    return True


def _archive_incomplete(scenario_root: Path, evidence_root: Path, reason: str) -> None:
    audit = evidence_root / "_audit"
    audit.mkdir(parents=True, exist_ok=True)
    destination = audit / f"{scenario_root.name}-{time.time_ns()}"
    shutil.move(str(scenario_root), destination)
    write_json(destination / "archive.json", {"schema": "ownward.product-scenario-archive/v1", "reason": reason})


def _rollback_paths(scenario_root: Path) -> tuple[Path, Path]:
    root = scenario_root / "rollback"
    return root, root / "manifest.json"


def _begin_rollback(scenario_root: Path, progress: dict[str, Any], unit: str) -> float:
    started = time.perf_counter()
    data_dir = scenario_root / "data"
    rollback_root, marker = _rollback_paths(scenario_root)
    require(not rollback_root.exists(), "a previous scenario mutation still has an unresolved rollback point")
    rollback_root.mkdir(parents=True)
    before = _tree_manifest(data_dir)
    shutil.copytree(data_dir, rollback_root / "data")
    after = _tree_manifest(data_dir)
    copied = _tree_manifest(rollback_root / "data")
    require(before == after == copied, "scenario data changed while creating its rollback point")
    write_json(marker, {
        "schema": "ownward.product-scenario-rollback/v1",
        "unit": unit,
        "progress_sha256": json_sha256(progress),
        "data_tree_sha256": json_sha256(before),
    })
    return time.perf_counter() - started


def _recover_rollback(scenario_root: Path, progress: dict[str, Any]) -> None:
    data_dir = scenario_root / "data"
    rollback_root, marker = _rollback_paths(scenario_root)
    if not rollback_root.exists():
        return
    if not marker.is_file():
        _safe_reset(rollback_root, scenario_root)
        return
    rollback = load_json(marker)
    current_digest = _tree_sha256(data_dir)
    evidence = progress.get("evidence", {})
    if (
        current_digest == progress.get("data_tree_sha256")
        and isinstance(evidence, dict)
        and _evidence_valid(scenario_root, evidence)
    ):
        _safe_reset(rollback_root, scenario_root)
        return
    require(rollback.get("progress_sha256") == json_sha256(progress), "rollback point does not bind the current progress checkpoint")
    require(_tree_sha256(rollback_root / "data") == rollback.get("data_tree_sha256"), "rollback data is damaged")
    _safe_reset(data_dir, scenario_root)
    shutil.copytree(rollback_root / "data", data_dir)
    require(_tree_sha256(data_dir) == progress.get("data_tree_sha256"), "restored scenario data does not match the checkpoint")
    _safe_reset(rollback_root, scenario_root)


def _next_attempt(stage_root: Path) -> Path:
    stage_root.mkdir(parents=True, exist_ok=True)
    attempts = [item for item in stage_root.iterdir() if item.is_dir() and item.name.startswith("attempt-")]
    return stage_root / f"attempt-{len(attempts) + 1:03d}"


def _relative_evidence(scenario_root: Path, stage: Path) -> dict[str, str]:
    evidence: dict[str, str] = {}
    for name in ("output.json", "events.jsonl", "stderr.txt"):
        path = stage / name
        if path.is_file():
            evidence[path.relative_to(scenario_root).as_posix()] = sha256(path)
    return evidence


def _semantic_trace_identity(trace: Any, asset_id: str, revision: int) -> dict[str, Any]:
    work_calls = [call for call in trace.calls if call.name == "ownward_semantic_work" and not call.error]
    submit_calls = [call for call in trace.calls if call.name == "ownward_semantic_submit" and not call.error]
    require(len(work_calls) == 1 and submit_calls, "semantic attempt lacks a single work call and a successful submit")
    work_items = work_calls[0].result.get("work") if isinstance(work_calls[0].result, dict) else None
    require(isinstance(work_items, list) and len(work_items) == 1, "semantic work did not return exactly one item")
    work = work_items[0]
    require(isinstance(work, dict) and isinstance(work.get("asset"), dict), "semantic work evidence is invalid")
    submission = submit_calls[-1].arguments.get("submission")
    require(isinstance(submission, dict), "semantic submit evidence is invalid")
    require(
        work["asset"].get("id") == asset_id
        and int(work["asset"].get("revision", -1)) == revision
        and submission.get("work_id") == work.get("id")
        and submission.get("asset_id") == asset_id
        and int(submission.get("asset_revision", -1)) == revision,
        "semantic work and submission do not bind the requested asset revision",
    )
    return {"work_id": str(work["id"]), "submission_sha256": json_sha256(submission)}


def _complete_semantic_unit(
    args: argparse.Namespace,
    runtime: Any,
    scenario_root: Path,
    stage_root: Path,
    asset_id: str,
    revision: int,
    deadline: float,
) -> tuple[float, dict[str, Any], dict[str, str]]:
    remaining = deadline - time.monotonic()
    require(remaining > 0, "product execution exceeded its total budget")
    stage = _next_attempt(stage_root)
    semantic, semantic_trace, elapsed = _run_codex(
        args,
        stage=stage,
        prompt=_semantic_prompt(asset_id, args),
        schema=SEMANTIC_SCHEMA,
        endpoint=runtime.binding.endpoint,
        bearer_token=runtime.binding.bearer_token,
        timeout_seconds=min(args.stage_timeout, remaining),
    )
    require(int(semantic.get("processed", -1)) == 1, "semantic capability did not process exactly one asset")
    require(not semantic_trace.bypassed and semantic_trace.calls, "semantic capability bypassed Ownward")
    require(
        all(
            call.name in {"ownward_semantic_work", "ownward_semantic_submit"}
            and not call.error
            for call in semantic_trace.calls
        ),
        "semantic capability used an invalid path",
    )
    identity = _semantic_trace_identity(semantic_trace, asset_id, revision)
    status = runtime.client.call_tool("ownward_status", {"id": asset_id})
    terminal = status.get("organization", {}).get("status")
    require(terminal in {"ready", "uncertain"}, "semantic work did not reach a terminal state")
    identity["terminal_status"] = terminal
    return elapsed, identity, _relative_evidence(scenario_root, stage)


def _commit_progress(
    scenario_root: Path,
    progress: dict[str, Any],
    unit: str,
    entry: dict[str, Any],
    evidence: dict[str, str],
) -> None:
    progress["completed_units"][unit] = entry
    progress["evidence"].update(evidence)
    progress["data_tree_sha256"] = _tree_sha256(scenario_root / "data")
    write_json(scenario_root / "progress.json", progress)
    rollback_root, _ = _rollback_paths(scenario_root)
    _safe_reset(rollback_root, scenario_root)


def _run_scenario(
    args: argparse.Namespace,
    task: dict[str, Any],
    binding: dict[str, Any],
    peak_mib: float,
    resource_passed: bool,
    query_limit_ms: float,
    resource_sha: str,
    deadline: float,
    *,
    cleanup_data: bool = True,
) -> dict[str, Any]:
    scenario_root = args.evidence_dir / str(task["scenario_id"])
    result_path = scenario_root / "result.json"
    progress_path = scenario_root / "progress.json"
    expected_binding = _scenario_binding(args, task, binding, resource_sha)
    if result_path.is_file():
        sealed = load_json(result_path)
        if _sealed_scenario_valid(sealed, scenario_root, expected_binding, bool(task["updates"])):
            _safe_reset(scenario_root / "data", scenario_root)
            _safe_reset(scenario_root / "rollback", scenario_root)
            return dict(sealed["result"])
        require(args.resume, f"scenario {task['scenario_id']} evidence is stale; use --resume to archive it")
        _archive_incomplete(scenario_root, args.evidence_dir, "sealed scenario binding or evidence changed")

    progress: dict[str, Any] | None = None
    if scenario_root.exists():
        require(args.resume, f"scenario {task['scenario_id']} is incomplete; use --resume")
        if not progress_path.is_file():
            _archive_incomplete(scenario_root, args.evidence_dir, "legacy incomplete scenario has no transactional checkpoint")
        else:
            candidate_progress = load_json(progress_path)
            if candidate_progress.get("schema") != "ownward.product-scenario-progress/v1" or candidate_progress.get("binding") != expected_binding:
                _archive_incomplete(scenario_root, args.evidence_dir, "scenario progress belongs to another binding")
            else:
                progress = candidate_progress
                _recover_rollback(scenario_root, progress)
                require(_tree_sha256(scenario_root / "data") == progress.get("data_tree_sha256"), "scenario data changed after its last valid checkpoint")
                require(_progress_evidence_complete(progress), "scenario checkpoint omits a completed semantic attempt")
                require(_evidence_valid(scenario_root, progress.get("evidence", {})), "scenario checkpoint evidence changed")
    scenario_root.mkdir(parents=True, exist_ok=True)
    scenario_started = time.perf_counter()
    data_dir = scenario_root / "data"
    environment = os.environ.copy()
    binary = args.binary
    with support.OwnwardRuntime(binary, data_dir, environment) as runtime:
        require(runtime.client is not None and runtime.binding is not None and runtime.process is not None, "Ownward runtime did not start")
        with resource.TreeSampler(runtime.process.pid) as sampler:
            items = task["information"]
            if progress is None:
                created = runtime.client.call_tool("ownward_create_batch", {"items": [
                    {"content": item["content"], "source": {"actor": "acceptance-suite", "ref": item["node_id"]}}
                    for item in items
                ]})
                created_results = created.get("results") if isinstance(created, dict) else None
                require(isinstance(created_results, list) and len(created_results) == len(items), "create batch is incomplete")
                stable_by_node: dict[str, str] = {}
                revisions: dict[str, int] = {}
                for item, value in zip(items, created_results):
                    mutation = value.get("result") if isinstance(value, dict) else None
                    information = mutation.get("information") if isinstance(mutation, dict) else None
                    require(isinstance(information, dict), "create batch item failed")
                    stable_by_node[str(item["node_id"])] = str(information["id"])
                    revisions[str(item["node_id"])] = int(information["revision"])
                progress = {
                    "schema": "ownward.product-scenario-progress/v1",
                    "binding": expected_binding,
                    "stable_by_node": stable_by_node,
                    "revisions": revisions,
                    "completed_units": {},
                    "evidence": {},
                    "data_tree_sha256": _tree_sha256(data_dir),
                }
                write_json(progress_path, progress)
            stable_by_node = {str(key): str(value) for key, value in progress["stable_by_node"].items()}
            revisions = {str(key): int(value) for key, value in progress["revisions"].items()}
            completed_units = progress["completed_units"]

            for item in items:
                node_id = str(item["node_id"])
                unit = f"initial:{node_id}"
                if unit in completed_units:
                    continue
                rollback_seconds = _begin_rollback(scenario_root, progress, unit)
                elapsed, identity, evidence = _complete_semantic_unit(
                    args, runtime, scenario_root,
                    scenario_root / "semantic-initial" / json_sha256(unit)[:16],
                    stable_by_node[node_id], revisions[node_id], deadline,
                )
                _commit_progress(scenario_root, progress, unit, {
                    "asset_id": stable_by_node[node_id], "revision": revisions[node_id],
                    "elapsed_seconds": elapsed, "rollback_seconds": rollback_seconds, **identity,
                }, evidence)

            for index, update in enumerate(task["updates"]):
                node_id = str(update["node_id"])
                unit = f"update:{index}:{node_id}"
                if unit in completed_units:
                    continue
                rollback_seconds = _begin_rollback(scenario_root, progress, unit)
                changed = runtime.client.call_tool("ownward_update", {
                    "id": stable_by_node[node_id], "expected_revision": revisions[node_id], "content": update["content"],
                })
                mutation = changed.get("result") if isinstance(changed, dict) else None
                information = mutation.get("information") if isinstance(mutation, dict) else None
                require(isinstance(information, dict) and information.get("id") == stable_by_node[node_id], "update changed stable identity")
                revisions[node_id] = int(information["revision"])
                progress["revisions"][node_id] = revisions[node_id]
                elapsed, identity, evidence = _complete_semantic_unit(
                    args, runtime, scenario_root,
                    scenario_root / "semantic-update" / json_sha256(unit)[:16],
                    stable_by_node[node_id], revisions[node_id], deadline,
                )
                _commit_progress(scenario_root, progress, unit, {
                    "asset_id": stable_by_node[node_id], "revision": revisions[node_id],
                    "elapsed_seconds": elapsed, "rollback_seconds": rollback_seconds, **identity,
                }, evidence)

            semantic_seconds = sum(float(value.get("elapsed_seconds", 0)) for value in completed_units.values())
            query_started = time.perf_counter()
            direct = runtime.client.call_tool("ownward_search", {"query": task["query"]["question"], "limit": 10})
            direct_ms = (time.perf_counter() - query_started) * 1000
            remaining = deadline - time.monotonic()
            require(remaining > 0, "product execution exceeded its total budget")
            query_stage = _next_attempt(scenario_root / "query")
            answer, query_trace, agent_query_seconds = _run_codex(
                args, stage=query_stage, prompt=_query_prompt(task["query"]["question"]),
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
        "rollback_ms": sum(float(value.get("rollback_seconds", 0)) for value in completed_units.values()) * 1000,
        "agent_query_ms": agent_query_seconds * 1000,
        "end_to_end_ms": end_to_end_ms,
        "peak_mib": max(peak_mib, sampled_peak),
        "used_navigation": any(call.name == "ownward_navigate" and not call.error for call in query_trace.calls),
        "within_latency_budget": direct_ms <= query_limit_ms, "within_resource_budget": resource_passed,
    }
    query_evidence = _relative_evidence(scenario_root, query_stage)
    evidence = dict(progress["evidence"])
    evidence.update(query_evidence)
    write_json(result_path, {
        "schema": "ownward.product-scenario-checkpoint/v2",
        "binding": expected_binding,
        "progress_sha256": sha256(progress_path),
        "evidence": evidence,
        "result": result,
    })
    if cleanup_data:
        _safe_reset(data_dir, scenario_root)
        _safe_reset(scenario_root / "rollback", scenario_root)
    for work in scenario_root.rglob("work"):
        if work.is_dir():
            _safe_reset(work, work.parent)
    return result


def _run_product_preflight(
    args: argparse.Namespace,
    tasks: dict[str, Any],
    binding: dict[str, Any],
    peak_mib: float,
    resource_passed: bool,
    query_limit_ms: float,
    resource_sha: str,
) -> dict[str, Any]:
    require(tasks.get("tasks"), "product preflight requires frozen tasks")
    source = tasks["tasks"][0]
    require(len(source.get("information", [])) >= 2, "product preflight needs two independent semantic items")
    selected = [source["information"][0], source["information"][-1]]
    task = {
        "scenario_id": "nonformal-preflight",
        "information": selected,
        "updates": [],
        "query": {"question": "Retrieve the two independently stored statements and cite both information items."},
    }
    preflight_root = args.evidence_dir / "_preflight"
    if preflight_root.exists():
        _safe_reset(preflight_root, args.evidence_dir)
    preflight_root.mkdir()
    original_root = args.evidence_dir
    args.evidence_dir = preflight_root
    started = time.perf_counter()
    try:
        result = _run_scenario(
            args, task, binding, peak_mib, resource_passed, query_limit_ms, resource_sha,
            time.monotonic() + min(420, args.max_wall_seconds), cleanup_data=False,
        )
        scenario_root = preflight_root / "nonformal-preflight"
        require(
            result["grounded"] and set(result["returned_ids"]) == {str(item["node_id"]) for item in selected},
            "product preflight query did not retrieve and ground both independent items",
        )
        progress = load_json(scenario_root / "progress.json")
        rollback_started = time.perf_counter()
        _begin_rollback(scenario_root, progress, "preflight-restore-cost")
        (scenario_root / "data" / ".preflight-fault").write_text("fault", encoding="utf-8")
        _recover_rollback(scenario_root, progress)
        restore_seconds = time.perf_counter() - rollback_started
        require(_tree_sha256(scenario_root / "data") == progress["data_tree_sha256"], "preflight rollback round trip changed scenario data")
    finally:
        args.evidence_dir = original_root
    semantic_units = sum(len(item["information"]) + len(item["updates"]) for item in tasks["tasks"])
    query_units = len(tasks["tasks"])
    per_semantic = result["semantic_ms"] / 1000 / len(selected)
    per_query = result["agent_query_ms"] / 1000
    per_rollback = result["rollback_ms"] / 1000 / len(selected)
    per_scenario_overhead = max(
        0.0,
        result["end_to_end_ms"] / 1000
        - result["semantic_ms"] / 1000
        - result["rollback_ms"] / 1000
        - per_query,
    )
    estimated = 1.25 * (
        semantic_units * per_semantic
        + query_units * per_query
        + query_units * per_scenario_overhead
        + semantic_units * per_rollback
    )
    report = {
        "schema": "ownward.product-execution-preflight/v1",
        "formal_evidence": False,
        "qualification_binding": binding,
        "binding": _scenario_binding(args, task, binding, resource_sha),
        "task_set_sha256": json_sha256(tasks),
        "resource_report_sha256": resource_sha,
        "observed": {
            "semantic_items": len(selected), "semantic_seconds": result["semantic_ms"] / 1000,
            "agent_query_seconds": per_query, "rollback_round_trip_seconds": restore_seconds,
            "wall_seconds": time.perf_counter() - started,
        },
        "projected": {"semantic_items": semantic_units, "queries": query_units, "wall_seconds": estimated},
        "maximum_wall_seconds": 1800,
        "required_margin_seconds": 300,
        "passed": estimated <= 1500,
    }
    _safe_reset(preflight_root, original_root)
    require(report["passed"], f"product qualification is projected to exceed its safe budget: {estimated:.1f}s")
    return report


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
    parser.add_argument("--preflight-only", action="store_true")
    parser.add_argument("--preflight-output", type=Path)
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
    if args.preflight_only:
        require(args.preflight_output is not None, "product preflight requires --preflight-output")
        report = _run_product_preflight(args, tasks, binding, peak_mib, resource_passed, query_limit_ms, resource_sha)
        write_json(args.preflight_output.resolve(), report)
        print(json.dumps(report, ensure_ascii=False))
        return
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
