#!/usr/bin/env python3
from __future__ import annotations

import argparse
from collections import Counter, defaultdict
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
from datetime import datetime, timezone
import hashlib
import json
import math
import os
from pathlib import Path
import queue
import secrets
import shutil
import subprocess
import sys
import tempfile
import threading
import time
from typing import Any


HERE = Path(__file__).resolve().parent
LEGACY_MODEL_ENVIRONMENT = (
    "OPENAI_API_KEY",
    "OWNWARD_MODEL_BASE_URL",
    "OWNWARD_MODEL_API_KEY",
    "OWNWARD_CHAT_MODEL",
    "OWNWARD_EMBEDDING_MODEL",
    "OWNWARD_EMBEDDING_DIMENSIONS",
)
sys.path.insert(0, str(HERE))
sys.path.insert(0, str(HERE.parents[1]))
from common import (  # noqa: E402
    ABLATION_REPORT_SCHEMA,
    DYNAMIC_REPORT_SCHEMA,
    agent_prompt,
    canonical_relation,
    expression_prompt,
    generation_prompt,
    load_json,
    merge_valid_dataset,
    require,
    sha256,
    validation_prompt,
    validate_hidden_world,
    validate_protocol,
    wilson_lower,
    write_json,
)
from schemas import answers_schema, expression_schema, hidden_world_schema, validation_schema  # noqa: E402
from support.ownward_mcp import OwnwardRuntime  # noqa: E402


@dataclass(frozen=True)
class AgentToolCall:
    name: str
    arguments: dict[str, Any]
    result: Any
    error: str


@dataclass(frozen=True)
class AgentTrace:
    session_id: str
    calls: tuple[AgentToolCall, ...]
    bypassed: bool
    bypass_operations: tuple[str, ...]


def _command_prefix(binary: Path) -> list[str]:
    if binary.suffix.lower() == ".ps1":
        shell = shutil.which("pwsh") or shutil.which("powershell")
        require(shell is not None, "PowerShell is required to run Codex")
        return [shell, "-NoProfile", "-File", str(binary)]
    return [str(binary)]


def _json_fragment(value: object) -> Any:
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


def _arguments(value: object) -> dict[str, Any]:
    if isinstance(value, dict):
        return value
    if isinstance(value, str):
        parsed = json.loads(value)
        require(isinstance(parsed, dict), "tool arguments must be an object")
        return parsed
    raise RuntimeError("tool arguments must be an object")


def _tool_name(value: object) -> str:
    name = str(value or "").strip()
    return name.rsplit("__", 1)[-1] if "__" in name else name


def parse_agent_trace(text: str) -> AgentTrace:
    session_id = ""
    calls: list[AgentToolCall] = []
    bypass_operations: list[str] = []
    for line in text.splitlines():
        if not line.strip():
            continue
        try:
            event = json.loads(line)
        except json.JSONDecodeError as error:
            raise RuntimeError("Codex agent event stream contains non-JSON output") from error
        if event.get("type") == "thread.started":
            session_id = str(event.get("thread_id", "")).strip()
            continue
        if event.get("type") != "item.completed":
            continue
        item = event.get("item")
        if not isinstance(item, dict):
            continue
        item_type = str(item.get("type", ""))
        if item_type in {"agent_message", "reasoning"}:
            continue
        if item_type != "mcp_tool_call":
            bypass_operations.append(item_type)
            continue
        name = _tool_name(item.get("tool"))
        if item.get("server") != "ownward" or not name.startswith("ownward_"):
            bypass_operations.append(f"{item_type}:{item.get('server')}:{name}")
            continue
        result = item.get("result")
        if isinstance(result, dict) and result.get("isError") is True:
            error = str(item.get("error") or "Ownward tool returned an error")
        else:
            error = str(item.get("error") or "")
        if isinstance(result, dict) and "structured_content" in result:
            result = result["structured_content"]
        if item.get("status") != "completed":
            error = error or f"tool status {item.get('status')!r}"
        calls.append(AgentToolCall(name, _arguments(item.get("arguments", {})), _json_fragment(result), error))
    require(bool(session_id), "Codex event stream has no session identity")
    return AgentTrace(session_id, tuple(calls), bool(bypass_operations), tuple(bypass_operations))


def _validate_generation_trace(path: Path) -> None:
    session_id = ""
    for line in path.read_text(encoding="utf-8").splitlines():
        if not line.strip():
            continue
        try:
            event = json.loads(line)
        except json.JSONDecodeError as error:
            raise RuntimeError("dataset generation trace contains non-JSON output") from error
        if event.get("type") == "thread.started":
            session_id = str(event.get("thread_id", "")).strip()
            continue
        if event.get("type") != "item.completed":
            continue
        item = event.get("item")
        require(isinstance(item, dict), "dataset generation trace contains an invalid item")
        require(item.get("type") in {"agent_message", "reasoning"}, "dataset generation used a tool or external operation")
    require(bool(session_id), "dataset generation trace has no session identity")


def _observed_tool_evidence(calls: tuple[AgentToolCall, ...]) -> dict[str, list[str]]:
    evidence: dict[str, list[str]] = defaultdict(list)

    def strings(value: Any) -> list[str]:
        if isinstance(value, dict):
            return [text for item in value.values() for text in strings(item)]
        if isinstance(value, list):
            return [text for item in value for text in strings(item)]
        if isinstance(value, str):
            parsed = _json_fragment(value)
            if not isinstance(parsed, str):
                return [value, *strings(parsed)]
            return [value]
        return []

    def visit(value: Any) -> None:
        if isinstance(value, dict):
            information_id = value.get("id")
            if isinstance(information_id, str) and information_id:
                evidence[information_id].extend(strings(value))
            for item in value.values():
                visit(item)
            return
        if isinstance(value, list):
            for item in value:
                visit(item)
            return
        if isinstance(value, str):
            parsed = _json_fragment(value)
            if not isinstance(parsed, str):
                visit(parsed)

    for call in calls:
        if call.error == "":
            visit(call.result)
    return evidence


def _isolated_codex_environment(auth_file: Path, codex_home: Path, base: dict[str, str]) -> dict[str, str]:
    codex_home.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(auth_file, codex_home / "auth.json")
    environment = dict(base)
    environment["CODEX_HOME"] = str(codex_home)
    environment.pop("OPENAI_API_KEY", None)
    return environment


def _codex_command(
    args: argparse.Namespace,
    *,
    model: str,
    reasoning_effort: str,
    work_dir: Path,
    schema_path: Path,
    output_path: Path,
    endpoint: str | None = None,
) -> list[str]:
    command = _command_prefix(args.codex_binary) + [
        "exec",
        "--ephemeral",
        "--json",
        "--color",
        "never",
        "--skip-git-repo-check",
        "-C",
        str(work_dir),
        "--sandbox",
        "read-only",
        "-m",
        model,
        "-c",
        f"model_reasoning_effort={json.dumps(reasoning_effort)}",
    ]
    for feature in (
        "apps",
        "image_generation",
        "include_apply_patch_tool",
        "js_repl",
        "memories",
        "memory_tool",
        "multi_agent",
        "personality",
        "plugins",
        "request_permissions_tool",
        "search_tool",
        "shell_snapshot",
        "shell_tool",
        "tool_search",
        "tool_suggest",
    ):
        command.extend(["-c", f"features.{feature}=false"])
    command.extend(["-c", 'web_search="disabled"', "--output-schema", str(schema_path), "-o", str(output_path)])
    if endpoint is not None:
        command.extend(
            [
                "-c",
                f"mcp_servers.ownward.url={json.dumps(endpoint)}",
                "-c",
                'mcp_servers.ownward.bearer_token_env_var="OWNWARD_MCP_BEARER_TOKEN"',
            ]
        )
    return command


def _run_codex_json(
    args: argparse.Namespace,
    *,
    model: str,
    reasoning_effort: str,
    prompt: str,
    schema: dict[str, Any],
    output_path: Path,
    events_path: Path,
    environment: dict[str, str],
    endpoint: str | None = None,
    maximum_seconds: float | None = None,
) -> dict[str, Any]:
    work_dir = events_path.with_suffix("")
    require(not work_dir.exists() or not any(work_dir.iterdir()), f"Codex work directory is not blank: {work_dir}")
    work_dir.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="ownward-dynamic-codex-") as temporary:
        root = Path(temporary)
        schema_path = root / "output.schema.json"
        schema_path.write_text(json.dumps(schema, ensure_ascii=False), encoding="utf-8")
        command = _codex_command(
            args,
            model=model,
            reasoning_effort=reasoning_effort,
            work_dir=work_dir,
            schema_path=schema_path,
            output_path=output_path,
            endpoint=endpoint,
        )
        command.append(prompt)
        isolated = _isolated_codex_environment(args.codex_auth_file, root / "codex-home", environment)
        returncode, stderr = _run_codex_with_inactivity_watchdog(
            command,
            environment=isolated,
            events_path=events_path,
            inactivity_seconds=float(args.protocol["execution"]["codex_inactivity_seconds"]),
            maximum_seconds=maximum_seconds,
        )
    require(returncode == 0, f"Codex run failed: {stderr[-3000:]}")
    require(output_path.is_file(), "Codex run produced no structured output")
    return load_json(output_path)


def _run_codex_with_inactivity_watchdog(
    command: list[str],
    *,
    environment: dict[str, str],
    events_path: Path,
    inactivity_seconds: float,
    maximum_seconds: float | None,
) -> tuple[int, str]:
    require(inactivity_seconds > 0, "Codex inactivity protection must be positive")
    process = subprocess.Popen(
        command,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        encoding="utf-8",
        bufsize=1,
        env=environment,
    )
    require(process.stdout is not None and process.stderr is not None, "Codex pipes are unavailable")
    messages: queue.Queue[tuple[str, str | None]] = queue.Queue()

    def pump(label: str, stream: Any) -> None:
        try:
            for line in stream:
                messages.put((label, line))
        finally:
            messages.put((label, None))

    threads = [
        threading.Thread(target=pump, args=("stdout", process.stdout), daemon=True),
        threading.Thread(target=pump, args=("stderr", process.stderr), daemon=True),
    ]
    for thread in threads:
        thread.start()
    closed: set[str] = set()
    stderr_parts: list[str] = []
    started = time.monotonic()
    last_activity = time.monotonic()
    events_path.parent.mkdir(parents=True, exist_ok=True)
    try:
        with events_path.open("w", encoding="utf-8") as events:
            while process.poll() is None or len(closed) < 2:
                if maximum_seconds is not None and time.monotonic() - started > maximum_seconds:
                    process.terminate()
                    try:
                        process.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait()
                    raise RuntimeError(f"Codex exceeded the product-derived execution limit of {maximum_seconds:g} seconds")
                remaining = inactivity_seconds - (time.monotonic() - last_activity)
                if remaining <= 0:
                    process.terminate()
                    try:
                        process.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait()
                    raise RuntimeError(f"Codex produced no process activity for {inactivity_seconds:g} seconds")
                try:
                    label, line = messages.get(timeout=min(1.0, remaining))
                except queue.Empty:
                    continue
                if line is None:
                    closed.add(label)
                    continue
                last_activity = time.monotonic()
                if label == "stdout":
                    events.write(line)
                    events.flush()
                else:
                    stderr_parts.append(line)
                    if len(stderr_parts) > 200:
                        del stderr_parts[:-200]
    finally:
        for stream in (process.stdout, process.stderr):
            stream.close()
        for thread in threads:
            thread.join(timeout=1)
        if process.poll() is None:
            process.kill()
            process.wait()
    return int(process.returncode), "".join(stderr_parts)


def _clear_incomplete_codex_stage(output_path: Path, events_path: Path, run_path: Path) -> None:
    require(not run_path.exists(), f"sealed run metadata exists without complete evidence: {run_path}")
    require(output_path.parent == events_path.parent == run_path.parent, "incomplete Codex evidence escaped its stage directory")
    for path in (output_path, events_path):
        if path.exists():
            path.unlink()
    work_dir = events_path.with_suffix("")
    if work_dir.exists():
        require(work_dir.is_dir() and work_dir.parent == events_path.parent, "invalid incomplete Codex work directory")
        shutil.rmtree(work_dir)


def _generation_prompt(protocol: dict[str, Any], random_seed: str) -> str:
    return generation_prompt(protocol, random_seed)


def _expression_prompt(hidden: dict[str, Any]) -> str:
    return expression_prompt(hidden)


def _validation_prompt(hidden: dict[str, Any], expression: dict[str, Any]) -> str:
    return validation_prompt(hidden, expression)


def _dataset_stage(
    args: argparse.Namespace,
    *,
    name: str,
    model: dict[str, Any],
    prompt: str,
    schema: dict[str, Any],
    output_path: Path,
    events_path: Path,
    run_path: Path,
    environment: dict[str, str],
) -> dict[str, Any]:
    binding = {
        "candidate": args.candidate,
        "protocol_sha256": sha256(args.protocol_path),
        "codex_binary_sha256": sha256(args.codex_binary),
        "model": model["model"],
        "reasoning_effort": model["reasoning_effort"],
        "prompt_sha256": hashlib.sha256(prompt.encode("utf-8")).hexdigest(),
    }
    existing = [output_path.exists(), events_path.exists(), run_path.exists()]
    if any(existing):
        require(args.resume, f"{name} already exists; use --resume")
        if not all(existing):
            _clear_incomplete_codex_stage(output_path, events_path, run_path)
        else:
            value = load_json(output_path)
            run = load_json(run_path)
            require(run.get("schema") == "ownward.dynamic-dataset-stage/v2", f"{name} run schema changed")
            require(run.get("binding") == binding, f"{name} run binding changed")
            require(run.get("output_sha256") == sha256(output_path), f"{name} output changed")
            require(run.get("events_sha256") == sha256(events_path), f"{name} events changed")
            require(float(run.get("elapsed_seconds", 0)) > 0, f"{name} elapsed time is invalid")
            _validate_generation_trace(events_path)
            return value
    begin = time.perf_counter()
    value = _run_codex_json(
        args,
        model=str(model["model"]),
        reasoning_effort=str(model["reasoning_effort"]),
        prompt=prompt,
        schema=schema,
        output_path=output_path,
        events_path=events_path,
        environment=environment,
        maximum_seconds=float(args.protocol["execution"]["dataset_stage_seconds_max"]),
    )
    elapsed = time.perf_counter() - begin
    _validate_generation_trace(events_path)
    write_json(
        run_path,
        {
            "schema": "ownward.dynamic-dataset-stage/v2",
            "binding": binding,
            "output_sha256": sha256(output_path),
            "events_sha256": sha256(events_path),
            "elapsed_seconds": elapsed,
        },
    )
    return value


def _prepare_dataset(args: argparse.Namespace, protocol: dict[str, Any], environment: dict[str, str]) -> tuple[dict[str, Any], dict[str, Path]]:
    paths = {
        "random": args.evidence_dir / "random-source.json",
        "hidden": args.evidence_dir / "hidden-world.json",
        "hidden_events": args.evidence_dir / "hidden-world.events.jsonl",
        "hidden_run": args.evidence_dir / "hidden-world.run.json",
        "expression": args.evidence_dir / "natural-language.json",
        "expression_events": args.evidence_dir / "natural-language.events.jsonl",
        "expression_run": args.evidence_dir / "natural-language.run.json",
        "validation": args.evidence_dir / "independent-validation.json",
        "validation_events": args.evidence_dir / "independent-validation.events.jsonl",
        "validation_run": args.evidence_dir / "independent-validation.run.json",
        "dataset": args.evidence_dir / "valid-dataset.json",
        "dataset_run": args.evidence_dir / "dataset-run.json",
    }
    models = protocol["models"]
    if paths["random"].exists():
        require(args.resume, "random source already exists; use --resume instead of regenerating")
        random_source = load_json(paths["random"])
    else:
        random_source = {"method": "secrets.token_hex(32)", "seed": secrets.token_hex(32)}
        write_json(paths["random"], random_source)
    hidden = _dataset_stage(
        args,
        name="hidden world",
        model=models["generator"],
        prompt=_generation_prompt(protocol, str(random_source["seed"])),
        schema=hidden_world_schema(),
        output_path=paths["hidden"],
        events_path=paths["hidden_events"],
        run_path=paths["hidden_run"],
        environment=environment,
    )
    validate_hidden_world(hidden, protocol)
    expression = _dataset_stage(
        args,
        name="natural-language expression",
        model=models["generator"],
        prompt=_expression_prompt(hidden),
        schema=expression_schema(),
        output_path=paths["expression"],
        events_path=paths["expression_events"],
        run_path=paths["expression_run"],
        environment=environment,
    )
    validation = _dataset_stage(
        args,
        name="independent validation",
        model=models["validator"],
        prompt=_validation_prompt(hidden, expression),
        schema=validation_schema(),
        output_path=paths["validation"],
        events_path=paths["validation_events"],
        run_path=paths["validation_run"],
        environment=environment,
    )
    dataset = merge_valid_dataset(hidden, expression, validation, protocol)
    if paths["dataset"].exists():
        require(args.resume and load_json(paths["dataset"]) == dataset, "frozen valid dataset changed")
    else:
        write_json(paths["dataset"], dataset)
    dataset_run = {
        "schema": "ownward.dynamic-dataset-run/v1",
        "candidate": args.candidate,
        "protocol_sha256": sha256(args.protocol_path),
        "codex_cli_version": protocol["runtime"]["codex_cli_version"],
        "codex_binary_sha256": sha256(args.codex_binary),
        "generator": protocol["models"]["generator"],
        "validator": protocol["models"]["validator"],
        "random_source_sha256": sha256(paths["random"]),
        "hidden_truth_sha256": sha256(paths["hidden"]),
        "expression_sha256": sha256(paths["expression"]),
        "validation_sha256": sha256(paths["validation"]),
        "dataset_sha256": sha256(paths["dataset"]),
    }
    if paths["dataset_run"].exists():
        require(args.resume and load_json(paths["dataset_run"]) == dataset_run, "dynamic dataset run binding changed")
    else:
        write_json(paths["dataset_run"], dataset_run)
    return dataset, paths


def _ownward_environment(args: argparse.Namespace, *, disable_relations: bool) -> dict[str, str]:
    environment = os.environ.copy()
    prohibited = tuple(str(value) for value in args.protocol["product_runtime"]["prohibited_environment"])
    require(set(prohibited) == set(LEGACY_MODEL_ENVIRONMENT), "release-default environment isolation changed")
    for name in prohibited:
        environment.pop(name, None)
    environment["OWNWARD_DISABLE_RELATIONS"] = "true" if disable_relations else "false"
    environment["NO_PROXY"] = "127.0.0.1,localhost"
    environment["no_proxy"] = "127.0.0.1,localhost"
    return environment


def _tree_sha256(root: Path) -> str:
    require(root.is_dir(), f"state directory is missing: {root}")
    digest = hashlib.sha256()
    for path in sorted((value for value in root.rglob("*") if value.is_file()), key=lambda value: value.relative_to(root).as_posix()):
        relative = path.relative_to(root).as_posix().encode("utf-8")
        digest.update(len(relative).to_bytes(8, "little"))
        digest.update(relative)
        digest.update(bytes.fromhex(sha256(path)))
    return digest.hexdigest()


def _run_binary(
    args: argparse.Namespace,
    command: list[str],
    environment: dict[str, str],
    *,
    timeout: float,
) -> dict[str, Any]:
    try:
        completed = subprocess.run(
            [str(args.binary), command[0], "--runtime-dir", str(args.runtime_dir), *command[1:]],
            check=False,
            capture_output=True,
            text=True,
            encoding="utf-8",
            timeout=timeout,
            env=environment,
        )
    except subprocess.TimeoutExpired as error:
        raise RuntimeError(f"Ownward command made no valid completion within {timeout:g} seconds: {' '.join(command[:2])}") from error
    require(completed.returncode == 0, f"Ownward command failed: {' '.join(command[:2])}: {completed.stderr[-2000:]}")
    value = json.loads(completed.stdout)
    require(isinstance(value, dict) or isinstance(value, list), "Ownward returned invalid JSON")
    return value


def _percentile(values: list[float], quantile: float) -> float:
    require(values and 0 < quantile <= 1, "percentile input is invalid")
    ordered = sorted(values)
    return ordered[max(0, math.ceil(len(ordered) * quantile) - 1)]


def _scenario_key(scenario_id: str, node_id: str) -> str:
    return f"{scenario_id}/{node_id}"


def _semantic_completion_schema() -> dict[str, Any]:
    return {
        "type": "object",
        "additionalProperties": False,
        "required": ["processed", "uncertain"],
        "properties": {
            "processed": {"type": "integer", "minimum": 0},
            "uncertain": {"type": "integer", "minimum": 0},
        },
    }


def _semantic_prompt(asset_ids: list[str], capability: dict[str, Any]) -> str:
    return f"""Act only as Ownward's external semantic capability. Use only the connected Ownward tools.

Call `ownward_semantic_work` once with exactly these asset IDs:
{json.dumps(asset_ids, ensure_ascii=False)}

Analyze only the returned assets and candidate contexts. Do not infer anything from a user task, query, test expectation, hidden truth, or outside knowledge. For every returned work item, submit exactly one result through `ownward_semantic_submit_batch` using schema `ownward.semantic-submission/v1`, capability id `codex`, capability version `{capability['model']}`, and execution `dynamic-unseen-organization`. A complete analysis may contain a concise summary, retrieval cues, topics, inferred contexts, and relations supported by explicit evidence in the work. Relations may use only same_as, broader_than, narrower_than, part_of, has_part, supports, contradicts, derived_from, applies_in, or related_to, and must target a supplied candidate with confidence at least 0.75. Use incoming direction when the candidate is the semantic source; otherwise use outgoing. Do not create a relation merely because two items share vocabulary. If the work does not support a reliable judgment, submit uncertain with a concise reason instead of guessing.

Check every per-item submission result. Return only the number processed and the number submitted as uncertain."""


def _run_semantic_partition(
    args: argparse.Namespace,
    *,
    scenario_id: str,
    asset_ids: list[str],
    endpoint: str,
    environment: dict[str, str],
) -> tuple[float, dict[str, Path]]:
    require(0 < len(asset_ids) <= 20, "semantic partition must contain one to twenty current assets")
    capability = args.protocol["models"]["external_agent"]
    prompt = _semantic_prompt(asset_ids, capability)
    safe_id = hashlib.sha256(scenario_id.encode("utf-8")).hexdigest()[:20]
    output_path = args.evidence_dir / f"semantic-{safe_id}.json"
    events_path = args.evidence_dir / f"semantic-{safe_id}.events.jsonl"
    run_path = args.evidence_dir / f"semantic-{safe_id}-run.json"
    binding = {
        "candidate": args.candidate,
        "release_binary_sha256": sha256(args.binary),
        "protocol_sha256": sha256(args.protocol_path),
        "codex_binary_sha256": sha256(args.codex_binary),
        "scenario_id": scenario_id,
        "asset_ids": asset_ids,
        "model": capability["model"],
        "reasoning_effort": capability["reasoning_effort"],
        "prompt_sha256": hashlib.sha256(prompt.encode("utf-8")).hexdigest(),
    }
    existing = [output_path.exists(), events_path.exists(), run_path.exists()]
    if any(existing):
        require(args.resume, f"semantic partition {scenario_id} exists; use --resume")
        if not all(existing):
            _clear_incomplete_codex_stage(output_path, events_path, run_path)
        else:
            run = load_json(run_path)
            require(run.get("schema") == "ownward.dynamic-semantic-run/v1", "semantic run schema changed")
            require(run.get("binding") == binding, "semantic run binding changed")
            require(run.get("output_sha256") == sha256(output_path), "semantic output changed")
            require(run.get("events_sha256") == sha256(events_path), "semantic events changed")
            return float(run["elapsed_seconds"]), {
                f"semantic_{safe_id}_output": output_path,
                f"semantic_{safe_id}_events": events_path,
                f"semantic_{safe_id}_run": run_path,
            }
    begin = time.perf_counter()
    result = _run_codex_json(
        args,
        model=str(capability["model"]),
        reasoning_effort=str(capability["reasoning_effort"]),
        prompt=prompt,
        schema=_semantic_completion_schema(),
        output_path=output_path,
        events_path=events_path,
        environment=environment,
        endpoint=endpoint,
        maximum_seconds=float(args.protocol["execution"]["organization_operation_stall_seconds"]),
    )
    elapsed = time.perf_counter() - begin
    trace = parse_agent_trace(events_path.read_text(encoding="utf-8"))
    require(not trace.bypassed, f"semantic capability used bypass tools: {trace.bypass_operations}")
    require(trace.calls and all(call.name in {"ownward_semantic_work", "ownward_semantic_submit_batch"} for call in trace.calls), "semantic capability used a non-semantic Ownward tool")
    require(all(not call.error for call in trace.calls), "semantic capability encountered a tool error")
    require(sum(call.name == "ownward_semantic_work" for call in trace.calls) == 1, "semantic capability changed its assigned work partition")
    require(sum(call.name == "ownward_semantic_submit_batch" for call in trace.calls) == 1, "semantic capability did not submit one complete batch")
    require(int(result.get("processed", -1)) == len(asset_ids), "semantic capability did not process every assigned asset")
    write_json(
        run_path,
        {
            "schema": "ownward.dynamic-semantic-run/v1",
            "binding": binding,
            "elapsed_seconds": elapsed,
            "output_sha256": sha256(output_path),
            "events_sha256": sha256(events_path),
        },
    )
    return elapsed, {
        f"semantic_{safe_id}_output": output_path,
        f"semantic_{safe_id}_events": events_path,
        f"semantic_{safe_id}_run": run_path,
    }


def _clear_partial_ingestion(data_dir: Path, progress_path: Path, evidence_dir: Path) -> None:
    resolved_evidence = evidence_dir.resolve()
    if data_dir.exists():
        resolved_data = data_dir.resolve()
        require(resolved_data.parent == resolved_evidence, "refusing to clear an unexpected data directory")
        shutil.rmtree(resolved_data)
    for path in evidence_dir.glob("semantic-*"):
        resolved = path.resolve()
        require(resolved.parent == resolved_evidence, "semantic evidence escaped the acceptance directory")
        if resolved.is_dir():
            shutil.rmtree(resolved)
        else:
            resolved.unlink()
    if progress_path.exists():
        require(progress_path.resolve().parent == resolved_evidence, "ingestion progress escaped the acceptance directory")
        progress_path.unlink()


def _ingest_condition(
    args: argparse.Namespace, dataset: dict[str, Any], *, condition: str, disable_relations: bool
) -> tuple[dict[str, Any], dict[str, str]]:
    mapping_path = args.evidence_dir / f"{condition}-mapping.json"
    environment = _ownward_environment(args, disable_relations=disable_relations)
    binding = {
        "candidate": args.candidate,
        "release_binary_sha256": sha256(args.binary),
        "protocol_sha256": sha256(args.protocol_path),
        "dataset_sha256": sha256(args.evidence_dir / "valid-dataset.json"),
        "product_runtime": args.protocol["product_runtime"],
    }
    if disable_relations:
        full_mapping_path = args.evidence_dir / "full-mapping.json"
        require(condition == "baseline" and full_mapping_path.is_file(), "baseline requires the completed full ingestion")
        full_mapping = load_json(full_mapping_path)
        full_data = args.evidence_dir / str(full_mapping["data_directory"])
        baseline_data = args.evidence_dir / "baseline-data"
        if baseline_data.exists():
            require(args.resume and baseline_data.is_dir(), "baseline state already exists; use --resume")
        else:
            shutil.copytree(full_data, baseline_data)
        source_tree = _tree_sha256(full_data)
        baseline_tree = _tree_sha256(baseline_data)
        require(source_tree == baseline_tree, "baseline state is not an exact copy of the frozen full state")
        mapping = {
            **full_mapping,
            "condition": "baseline",
            "disable_relations": True,
            "data_directory": "baseline-data",
            "source_mapping_sha256": sha256(full_mapping_path),
            "source_state_tree_sha256": source_tree,
            "baseline_state_tree_sha256": baseline_tree,
        }
        if mapping_path.exists():
            require(args.resume and load_json(mapping_path) == mapping, "baseline mapping changed")
        else:
            write_json(mapping_path, mapping)
        return mapping, environment
    require(condition == "full", "full ingestion must keep relation organization enabled")
    data_dir = args.evidence_dir / "full-data"
    progress_path = args.evidence_dir / "full-ingestion-progress.json"
    if mapping_path.exists():
        require(args.resume, f"{condition} mapping exists; use --resume")
        mapping = load_json(mapping_path)
        require(mapping.get("binding") == binding, f"{condition} ingestion binding changed")
        require(progress_path.is_file(), "sealed ingestion mapping has no progress evidence")
        require(mapping.get("ingestion_progress_sha256") == sha256(progress_path), "sealed ingestion progress changed")
        stable_ids = {str(key): str(value) for key, value in mapping["stable_ids"].items()}
        inspection_stall = float(args.protocol["execution"]["inspection_operation_stall_seconds"])
        for scenario in dataset["valid_scenarios"]:
            scenario_id = str(scenario["truth"]["id"])
            updates = {str(item["node_id"]): str(item["content"]) for item in scenario["expression"]["updates"]}
            for item in scenario["expression"]["information"]:
                node_id = str(item["node_id"])
                expected_content = updates.get(node_id, str(item["content"]))
                read = _run_binary(
                    args,
                    ["read", "--data-dir", str(data_dir), "--id", stable_ids[_scenario_key(scenario_id, node_id)]],
                    environment,
                    timeout=inspection_stall,
                )
                require(read.get("content") == expected_content, f"{condition} resumed asset differs from the frozen dataset")
        return mapping, environment
    progress: dict[str, Any] | None = None
    if data_dir.exists() or progress_path.exists():
        require(args.resume, f"{condition} ingestion already started; use --resume")
        if data_dir.is_dir() and progress_path.is_file():
            candidate_progress = load_json(progress_path)
            require(candidate_progress.get("schema") == "ownward.dynamic-ingestion-progress/v1", "ingestion progress schema changed")
            require(candidate_progress.get("binding") == binding, "ingestion progress binding changed")
            if not candidate_progress.get("inflight_scenario"):
                progress = candidate_progress
        if progress is None:
            _clear_partial_ingestion(data_dir, progress_path, args.evidence_dir)
    if progress is None:
        data_dir.mkdir(parents=True)
        progress = {
            "schema": "ownward.dynamic-ingestion-progress/v1",
            "binding": binding,
            "completed_scenarios": [],
            "inflight_scenario": "",
            "stable_ids": {},
            "revisions": {},
            "durations": [],
            "operation_count": 0,
            "semantic_evidence": {},
        }
        write_json(progress_path, progress)
    stable_ids = {str(key): str(value) for key, value in progress["stable_ids"].items()}
    revisions = {str(key): int(value) for key, value in progress["revisions"].items()}
    durations = [float(value) for value in progress["durations"]]
    operation_count = int(progress["operation_count"])
    completed_scenarios = {str(value) for value in progress["completed_scenarios"]}
    execution = args.protocol["execution"]
    operation_stall = float(execution["organization_operation_stall_seconds"])
    accepted_p95 = float(execution["organization_p95_seconds_max"])
    total_operations = sum(
        len(scenario["expression"]["information"]) + len(scenario["expression"]["updates"])
        for scenario in dataset["valid_scenarios"]
    )
    allowed_slow_operations = max(0, int(total_operations * 0.05))
    semantic_evidence = {str(key): str(value) for key, value in progress["semantic_evidence"].items()}
    with OwnwardRuntime(
        args.binary,
        data_dir,
        args.runtime_dir,
        environment,
        startup_seconds=operation_stall,
        operation_seconds=operation_stall,
    ) as runtime:
        require(runtime.client is not None and runtime.binding is not None, "Ownward shared runtime did not start")
        for scenario in dataset["valid_scenarios"]:
            scenario_id = str(scenario["truth"]["id"])
            if scenario_id in completed_scenarios:
                for item in scenario["expression"]["information"]:
                    key = _scenario_key(scenario_id, str(item["node_id"]))
                    status = runtime.client.call_tool("ownward_status", {"id": stable_ids[key]})
                    organization = status.get("organization") if isinstance(status, dict) else None
                    require(isinstance(organization, dict) and organization.get("status") == "ready", "resumed semantic organization is not ready")
                continue
            progress["inflight_scenario"] = scenario_id
            write_json(progress_path, progress)
            information = list(scenario["expression"]["information"])
            begin = time.perf_counter()
            created_batch = runtime.client.call_tool(
                "ownward_create_batch",
                {"items": [{"content": str(item["content"]), "source": {"actor": "dynamic-unseen"}} for item in information]},
            )
            batch_results = created_batch.get("results") if isinstance(created_batch, dict) else None
            require(isinstance(batch_results, list) and len(batch_results) == len(information), "create batch result is incomplete")
            scenario_ids: list[str] = []
            for item, batch_result in zip(information, batch_results, strict=True):
                require(isinstance(batch_result, dict) and not batch_result.get("error"), f"create batch item failed: {batch_result}")
                mutation = batch_result.get("result")
                result = mutation.get("information") if isinstance(mutation, dict) else None
                organization = mutation.get("organization") if isinstance(mutation, dict) else None
                require(isinstance(result, dict) and isinstance(organization, dict), "create batch item is incomplete")
                require(organization.get("status") == "pending", "new asset did not expose pending semantic work")
                key = _scenario_key(scenario_id, str(item["node_id"]))
                require(key not in stable_ids, "dynamic node was created twice")
                stable_ids[key] = str(result["id"])
                revisions[key] = int(result["revision"])
                scenario_ids.append(str(result["id"]))
                operation_count += 1
            for item in scenario["expression"]["updates"]:
                key = _scenario_key(scenario_id, str(item["node_id"]))
                updated = runtime.client.call_tool(
                    "ownward_update",
                    {
                        "id": stable_ids[key],
                        "expected_revision": revisions[key],
                        "content": str(item["content"]),
                    },
                )
                mutation = updated.get("result") if isinstance(updated, dict) else None
                result = mutation.get("information") if isinstance(mutation, dict) else None
                organization = mutation.get("organization") if isinstance(mutation, dict) else None
                require(isinstance(result, dict) and isinstance(organization, dict), "update result is incomplete")
                require(result.get("id") == stable_ids[key] and int(result.get("revision", 0)) == revisions[key] + 1, "update changed stable identity")
                require(organization.get("status") == "pending", "updated asset did not expose pending semantic work")
                revisions[key] = int(result["revision"])
                operation_count += 1
            semantic_seconds, evidence = _run_semantic_partition(
                args,
                scenario_id=scenario_id,
                asset_ids=scenario_ids,
                endpoint=runtime.binding.endpoint,
                environment={**environment, "OWNWARD_MCP_BEARER_TOKEN": runtime.binding.bearer_token},
            )
            for name, path in evidence.items():
                semantic_evidence[name] = str(path.relative_to(args.evidence_dir))
            for information_id in scenario_ids:
                status = runtime.client.call_tool("ownward_status", {"id": information_id})
                organization = status.get("organization") if isinstance(status, dict) else None
                require(isinstance(organization, dict) and organization.get("status") == "ready", f"semantic organization did not become ready: {organization}")
            scenario_seconds = time.perf_counter() - begin
            durations.extend([scenario_seconds] * (len(information) + len(scenario["expression"]["updates"])))
            require(semantic_seconds <= operation_stall, "semantic organization exceeded the frozen operation boundary")
            require(
                sum(value > accepted_p95 for value in durations) <= allowed_slow_operations,
                "organization can no longer satisfy the frozen P95; stop before consuming the remaining batch",
            )
            completed_scenarios.add(scenario_id)
            progress = {
                "schema": "ownward.dynamic-ingestion-progress/v1",
                "binding": binding,
                "completed_scenarios": sorted(completed_scenarios),
                "inflight_scenario": "",
                "stable_ids": stable_ids,
                "revisions": revisions,
                "durations": durations,
                "operation_count": operation_count,
                "semantic_evidence": semantic_evidence,
            }
            write_json(progress_path, progress)
    expected_scenarios = {str(value["truth"]["id"]) for value in dataset["valid_scenarios"]}
    require(completed_scenarios == expected_scenarios, "full ingestion did not complete every frozen scenario")
    mapping = {
        "schema": "ownward.dynamic-ingestion/v2",
        "condition": condition,
        "disable_relations": disable_relations,
        "data_directory": "full-data",
        "binding": binding,
        "stable_ids": stable_ids,
        "revisions": revisions,
        "operation_count": operation_count,
        "organization_seconds": sum(durations),
        "organization_seconds_max": max(durations, default=0),
        "organization_seconds_p95": _percentile(durations, 0.95),
        "semantic_evidence": semantic_evidence,
        "ingestion_progress_sha256": sha256(progress_path),
    }
    write_json(mapping_path, mapping)
    return mapping, environment


def _agent_prompt(questions: list[dict[str, str]]) -> str:
    return agent_prompt(questions)


def _run_agents(
    args: argparse.Namespace,
    dataset: dict[str, Any],
    mapping: dict[str, Any],
    environment: dict[str, str],
    protocol: dict[str, Any],
    endpoint: str,
    *,
    condition: str,
    task_classes: list[str] | None = None,
) -> tuple[dict[str, Any], dict[str, Path]]:
    agent_config = protocol["models"]["external_agent"]
    model = str(agent_config["model"])
    reasoning_effort = str(agent_config["reasoning_effort"])
    execution = protocol["execution"]
    budget = int(execution["agent_tool_calls_per_query"])
    seconds_per_question = float(execution["agent_seconds_per_question_max"])
    scenarios_by_class: dict[str, list[dict[str, Any]]] = defaultdict(list)
    for scenario in dataset["valid_scenarios"]:
        scenarios_by_class[str(scenario["truth"]["task_class"])].append(scenario)
    runs: dict[str, Any] = {}
    evidence: dict[str, Path] = {}
    selected_classes = task_classes or list(protocol["generation"]["task_classes"])
    require(set(selected_classes) <= set(protocol["generation"]["task_classes"]), "agent task class is outside the frozen protocol")
    for task_class in selected_classes:
        scenarios = scenarios_by_class[str(task_class)]
        class_seconds = len(scenarios) * seconds_per_question
        questions = [
            {"query_id": str(item["truth"]["id"]), "question": str(item["expression"]["query"]["question"])}
            for item in scenarios
        ]
        output_path = args.evidence_dir / f"{condition}-{task_class}-answers.json"
        events_path = args.evidence_dir / f"{condition}-{task_class}.events.jsonl"
        run_path = args.evidence_dir / f"{condition}-{task_class}-run.json"
        prompt = _agent_prompt(questions)
        prompt_sha256 = hashlib.sha256(prompt.encode("utf-8")).hexdigest()
        run_binding = {
            "candidate": args.candidate,
            "release_binary_sha256": sha256(args.binary),
            "protocol_sha256": sha256(args.protocol_path),
            "dataset_sha256": sha256(args.evidence_dir / "valid-dataset.json"),
            "mapping_sha256": sha256(args.evidence_dir / f"{condition}-mapping.json"),
            "codex_binary_sha256": sha256(args.codex_binary),
        }
        existing = [output_path.exists(), events_path.exists(), run_path.exists()]
        if any(existing):
            require(args.resume, f"{condition} {task_class} agent result exists; use --resume")
            if not all(existing):
                _clear_incomplete_codex_stage(output_path, events_path, run_path)
                existing = [False, False, False]
        if all(existing):
            answers = load_json(output_path)
            run_metadata = load_json(run_path)
            require(run_metadata.get("schema") == "ownward.dynamic-agent-run/v1", "agent run metadata schema changed")
            require(run_metadata.get("answers_sha256") == sha256(output_path), "resumed agent answers changed")
            require(run_metadata.get("events_sha256") == sha256(events_path), "resumed agent events changed")
            require(run_metadata.get("prompt_sha256") == prompt_sha256, "resumed agent prompt changed")
            require(run_metadata.get("model") == model, "resumed agent model changed")
            require(run_metadata.get("reasoning_effort") == reasoning_effort, "resumed agent reasoning effort changed")
            require(run_metadata.get("binding") == run_binding, "resumed agent run binding changed")
            elapsed = float(run_metadata.get("elapsed_seconds", 0))
            require(elapsed > 0, "resumed agent elapsed time is invalid")
            require(elapsed <= class_seconds, f"{condition} {task_class} exceeded the product-derived time boundary")
        else:
            begin = time.perf_counter()
            answers = _run_codex_json(
                args,
                model=model,
                reasoning_effort=reasoning_effort,
                prompt=prompt,
                schema=answers_schema(),
                output_path=output_path,
                events_path=events_path,
                environment=environment,
                endpoint=endpoint,
                maximum_seconds=class_seconds,
            )
            elapsed = time.perf_counter() - begin
            write_json(
                run_path,
                {
                    "schema": "ownward.dynamic-agent-run/v1",
                    "model": model,
                    "reasoning_effort": reasoning_effort,
                    "prompt_sha256": prompt_sha256,
                    "answers_sha256": sha256(output_path),
                    "events_sha256": sha256(events_path),
                    "elapsed_seconds": elapsed,
                    "binding": run_binding,
                },
            )
        trace = parse_agent_trace(events_path.read_text(encoding="utf-8"))
        require(not trace.bypassed, f"{condition} agent used bypass tools: {trace.bypass_operations}")
        require(
            trace.calls
            and all(
                call.name in {"ownward_rules", "ownward_search", "ownward_read", "ownward_navigate", "ownward_status"}
                for call in trace.calls
            ),
            f"{condition} agent used a mutation or non-retrieval Ownward tool",
        )
        require(all(not call.error for call in trace.calls), f"{condition} agent encountered an Ownward tool error")
        observed_evidence = _observed_tool_evidence(trace.calls)
        answer_values = answers.get("answers")
        require(isinstance(answer_values, list), "agent output has no answers")
        by_id = {str(value.get("query_id", "")): value for value in answer_values if isinstance(value, dict)}
        require(
            len(answer_values) == len(by_id) == len(scenarios)
            and set(by_id) == {str(item["truth"]["id"]) for item in scenarios},
            "agent changed the query set",
        )
        successes = 0
        details: list[dict[str, Any]] = []
        stable_ids = mapping["stable_ids"]
        for scenario in scenarios:
            truth = scenario["truth"]
            scenario_id = str(truth["id"])
            answer = by_id[scenario_id]
            expected_ids = {stable_ids[_scenario_key(scenario_id, str(node_id))] for node_id in truth["query"]["expected_ids"]}
            forbidden_ids = {stable_ids[_scenario_key(scenario_id, str(node_id))] for node_id in truth["query"]["forbidden_ids"]}
            actual_ids = {str(value) for value in answer.get("information_ids", [])}
            actual_facts = {str(value) for value in answer.get("answer_facts", [])}
            expected_facts = {str(value) for value in truth["query"]["answer_facts"]}
            grounded = actual_ids <= set(observed_evidence) and all(
                any(fact in text for information_id in actual_ids for text in observed_evidence[information_id])
                for fact in actual_facts
            )
            passed = actual_ids == expected_ids and not actual_ids & forbidden_ids and actual_facts == expected_facts and grounded
            successes += int(passed)
            details.append(
                {
                    "id": scenario_id,
                    "passed": passed,
                    "grounded_in_ownward_evidence": grounded,
                    "expected_count": len(expected_ids),
                    "returned_count": len(actual_ids),
                }
            )
        require(len(trace.calls) <= len(scenarios) * budget, f"{condition} {task_class} exceeded the tool-call budget")
        search_calls = sum(call.name == "ownward_search" for call in trace.calls)
        runs[str(task_class)] = {
            "session_id": trace.session_id,
            "prompt_sha256": prompt_sha256,
            "successes": successes,
            "total": len(scenarios),
            "success_rate": successes / len(scenarios),
            "tool_calls": len(trace.calls),
            "search_calls": search_calls,
            "elapsed_seconds": elapsed,
            "details": details,
        }
        evidence[f"{condition}_{task_class}_answers"] = output_path
        evidence[f"{condition}_{task_class}_events"] = events_path
        evidence[f"{condition}_{task_class}_run"] = run_path
    return runs, evidence


def _run_agent_pairs(
    args: argparse.Namespace,
    dataset: dict[str, Any],
    full_mapping: dict[str, Any],
    full_environment: dict[str, str],
    baseline_mapping: dict[str, Any],
    baseline_environment: dict[str, str],
    protocol: dict[str, Any],
    full_endpoint: str,
    baseline_endpoint: str,
) -> tuple[dict[str, Any], dict[str, Path], dict[str, Any], dict[str, Path]]:
    require(int(protocol["execution"]["parallel_conditions"]) == 2, "dynamic condition parallelism changed")
    full_runs: dict[str, Any] = {}
    full_evidence: dict[str, Path] = {}
    baseline_runs: dict[str, Any] = {}
    baseline_evidence: dict[str, Path] = {}
    for task_class in protocol["generation"]["task_classes"]:
        with ThreadPoolExecutor(max_workers=2, thread_name_prefix=f"dynamic-{task_class}") as executor:
            full_future = executor.submit(
                _run_agents,
                args,
                dataset,
                full_mapping,
                full_environment,
                protocol,
                full_endpoint,
                condition="full",
                task_classes=[str(task_class)],
            )
            baseline_future = executor.submit(
                _run_agents,
                args,
                dataset,
                baseline_mapping,
                baseline_environment,
                protocol,
                baseline_endpoint,
                condition="baseline",
                task_classes=[str(task_class)],
            )
            class_full_runs, class_full_evidence = full_future.result()
            class_baseline_runs, class_baseline_evidence = baseline_future.result()
        full_runs.update(class_full_runs)
        full_evidence.update(class_full_evidence)
        baseline_runs.update(class_baseline_runs)
        baseline_evidence.update(class_baseline_evidence)
    return full_runs, full_evidence, baseline_runs, baseline_evidence


def _verify_assets(
    args: argparse.Namespace, dataset: dict[str, Any], mapping: dict[str, Any], environment: dict[str, str], *, condition: str
) -> tuple[dict[str, Any], list[dict[str, Any]]]:
    stable_ids = mapping["stable_ids"]
    inspection_stall = float(args.protocol["execution"]["inspection_operation_stall_seconds"])
    observed_ids: set[str] = set()
    evidence: list[dict[str, Any]] = []
    checked = 0
    for scenario in dataset["valid_scenarios"]:
        scenario_id = str(scenario["truth"]["id"])
        updates = {str(item["node_id"]): str(item["content"]) for item in scenario["expression"]["updates"]}
        for item in scenario["expression"]["information"]:
            node_id = str(item["node_id"])
            stable_id = stable_ids[_scenario_key(scenario_id, node_id)]
            read = _run_binary(
                args,
                ["read", "--data-dir", str(args.evidence_dir / str(mapping["data_directory"])), "--id", stable_id],
                environment,
                timeout=inspection_stall,
            )
            require(str(read.get("id")) == stable_id, "stable identity changed")
            require(str(read.get("content")) == updates.get(node_id, str(item["content"])), "asset content changed")
            require(stable_id not in observed_ids, "information was merged or split")
            observed_ids.add(stable_id)
            evidence.append(
                {
                    "key": _scenario_key(scenario_id, node_id),
                    "id": stable_id,
                    "revision": int(read.get("revision", 0)),
                    "content_sha256": hashlib.sha256(str(read.get("content", "")).encode("utf-8")).hexdigest(),
                }
            )
            checked += 1
    return {"checked": checked, "unique_ids": len(observed_ids), "passed": checked == len(observed_ids)}, evidence


def _verify_asset_pair(
    args: argparse.Namespace,
    dataset: dict[str, Any],
    full_mapping: dict[str, Any],
    full_environment: dict[str, str],
    baseline_mapping: dict[str, Any],
    baseline_environment: dict[str, str],
) -> tuple[tuple[dict[str, Any], list[dict[str, Any]]], tuple[dict[str, Any], list[dict[str, Any]]]]:
    require(int(args.protocol["execution"]["parallel_conditions"]) == 2, "dynamic condition parallelism changed")
    with ThreadPoolExecutor(max_workers=2, thread_name_prefix="dynamic-asset-integrity") as executor:
        full_future = executor.submit(
            _verify_assets,
            args,
            dataset,
            full_mapping,
            full_environment,
            condition="full",
        )
        baseline_future = executor.submit(
            _verify_assets,
            args,
            dataset,
            baseline_mapping,
            baseline_environment,
            condition="baseline",
        )
        return full_future.result(), baseline_future.result()


def _organization_metrics(
    args: argparse.Namespace, dataset: dict[str, Any], mapping: dict[str, Any], environment: dict[str, str]
) -> tuple[dict[str, Any], dict[str, Any]]:
    stable_ids = mapping["stable_ids"]
    inspection_stall = float(args.protocol["execution"]["inspection_operation_stall_seconds"])
    reverse = {value: key for key, value in stable_ids.items()}
    expected: set[tuple[str, str, str]] = set()
    for scenario in dataset["valid_scenarios"]:
        scenario_id = str(scenario["truth"]["id"])
        for relation in scenario["truth"]["relations"]:
            source = _scenario_key(scenario_id, str(relation["source_id"]))
            target = _scenario_key(scenario_id, str(relation["target_id"]))
            expected.add(canonical_relation(source, str(relation["type"]), target))
    actual: set[tuple[str, str, str]] = set()
    for stable_id in stable_ids.values():
        graph = _run_binary(
            args,
            ["navigate", "--data-dir", str(args.evidence_dir / "full-data"), "--id", stable_id, "--depth", "1", "--limit", "100"],
            environment,
            timeout=inspection_stall,
        )
        for edge in graph.get("edges", []):
            source = reverse.get(str(edge.get("source_id", "")))
            target = reverse.get(str(edge.get("target_id", "")))
            if source and target:
                actual.add(canonical_relation(source, str(edge.get("type", "")), target))
    true_positive = len(expected & actual)
    confidence = float(args.protocol["statistics"]["confidence_level"])
    precision = true_positive / len(actual) if actual else 0.0
    recall = true_positive / len(expected) if expected else 0.0
    metrics = {
        "expected": len(expected),
        "actual": len(actual),
        "true_positive": true_positive,
        "precision": precision,
        "recall": recall,
        "precision_wilson_lower": wilson_lower(true_positive, len(actual), confidence) if actual else 0.0,
        "recall_wilson_lower": wilson_lower(true_positive, len(expected), confidence) if expected else 0.0,
    }
    snapshot = {
        "schema": "ownward.dynamic-organization-evidence/v1",
        "expected": [list(value) for value in sorted(expected)],
        "actual": [list(value) for value in sorted(actual)],
    }
    return metrics, snapshot


def _binary_text(binary: Path, *arguments: str) -> str:
    completed = subprocess.run(
        [*_command_prefix(binary), *arguments], check=False, capture_output=True, text=True, encoding="utf-8", timeout=60
    )
    require(completed.returncode == 0, f"could not execute release binary: {completed.stderr}")
    return completed.stdout.strip()


def _repository_candidate(repository: Path) -> tuple[str, str]:
    head = subprocess.run(["git", "rev-parse", "HEAD"], cwd=repository, check=True, capture_output=True, text=True, encoding="utf-8").stdout.strip()
    status = subprocess.run(["git", "status", "--porcelain"], cwd=repository, check=True, capture_output=True, text=True, encoding="utf-8").stdout
    return head, status


def _artifact_entries(paths: dict[str, Path]) -> dict[str, dict[str, str]]:
    return {name: {"path": str(path.resolve()), "sha256": sha256(path.resolve())} for name, path in paths.items() if path.is_file()}


def _seal_report(path: Path, value: dict[str, Any], *, resume: bool) -> dict[str, Any]:
    if not path.exists():
        write_json(path, value)
        return value
    require(resume, f"acceptance report already exists: {path}")
    existing = load_json(path)
    if "measured_at" in existing:
        value["measured_at"] = existing["measured_at"]
    require(existing == value, f"sealed acceptance report changed: {path}")
    return existing


def _build_reports(
    args: argparse.Namespace,
    protocol: dict[str, Any],
    dataset: dict[str, Any],
    dataset_paths: dict[str, Path],
    full_mapping: dict[str, Any],
    baseline_mapping: dict[str, Any],
    full_runs: dict[str, Any],
    baseline_runs: dict[str, Any],
    full_agent_evidence: dict[str, Path],
    baseline_agent_evidence: dict[str, Path],
    verification_evidence: dict[str, Path],
    asset_integrity: dict[str, Any],
    organization: dict[str, Any],
) -> tuple[dict[str, Any], dict[str, Any]]:
    statistics = protocol["statistics"]
    execution = protocol["execution"]
    organization = {
        **organization,
        "completion_p95_seconds": float(full_mapping["organization_seconds_p95"]),
    }
    confidence = float(statistics["confidence_level"])
    task_metrics: dict[str, Any] = {}
    task_checks: list[dict[str, Any]] = []
    for task_class in protocol["generation"]["task_classes"]:
        run = full_runs[str(task_class)]
        lower = wilson_lower(int(run["successes"]), int(run["total"]), confidence)
        passed = lower >= float(statistics["dynamic_task_success_wilson_lower_min"])
        task_metrics[str(task_class)] = {**run, "wilson_lower": lower}
        task_checks.append({"name": f"dynamic-{task_class}", "passed": passed})
    relation_passed = (
        organization["precision_wilson_lower"] >= float(statistics["relation_precision_wilson_lower_min"])
        and organization["recall_wilson_lower"] >= float(statistics["relation_recall_wilson_lower_min"])
    )
    agent_sessions = [
        str(runs[str(task_class)]["session_id"])
        for runs in (full_runs, baseline_runs)
        for task_class in protocol["generation"]["task_classes"]
    ]
    independent_agent_sessions = all(agent_sessions) and len(set(agent_sessions)) == len(agent_sessions)
    evidence = {**dataset_paths, **full_agent_evidence, **baseline_agent_evidence, **verification_evidence}
    evidence["codex_binary"] = args.codex_binary
    evidence["full_mapping"] = args.evidence_dir / "full-mapping.json"
    evidence["baseline_mapping"] = args.evidence_dir / "baseline-mapping.json"
    dynamic_checks = [
        {"name": "post-freeze-random-generation", "passed": True},
        {"name": "independent-expression-validation", "passed": True},
        {"name": "required-scope-and-task-coverage", "passed": True},
        {"name": "asset-identity-content-integrity", "passed": bool(asset_integrity["passed"])},
        {"name": "semantic-relation-quality", "passed": relation_passed},
        {
            "name": "organization-completion-p95",
            "passed": organization["completion_p95_seconds"] <= float(execution["organization_p95_seconds_max"]),
        },
        *task_checks,
        {"name": "external-agent-no-bypass-and-budget", "passed": independent_agent_sessions},
    ]
    dynamic = {
        "schema": DYNAMIC_REPORT_SCHEMA,
        "candidate": args.candidate,
        "release_binary_version": args.candidate,
        "release_binary_sha256": sha256(args.binary),
        "measured_at": datetime.now(timezone.utc).isoformat(),
        "protocol_sha256": sha256(args.protocol_path),
        "generator": protocol["models"]["generator"],
        "validator": protocol["models"]["validator"],
        "external_agent": protocol["models"]["external_agent"],
        "codex_cli": {
            "version": protocol["runtime"]["codex_cli_version"],
            "sha256": sha256(args.codex_binary),
        },
        "product_runtime": protocol["product_runtime"],
        "random_source_sha256": sha256(dataset_paths["random"]),
        "hidden_truth_sha256": sha256(dataset_paths["hidden"]),
        "dataset_sha256": sha256(dataset_paths["dataset"]),
        "generated_scenarios": int(protocol["generation"]["generated_scenarios"]),
        "valid_scenarios": len(dataset["valid_scenarios"]),
        "reserve_scenarios": len(dataset["reserve_scenarios"]),
        "rejected_scenarios": len(dataset["rejected_scenarios"]),
        "asset_integrity": asset_integrity,
        "organization": organization,
        "tasks": task_metrics,
        "statistics": statistics,
        "execution": protocol["execution"],
        "evidence": _artifact_entries(evidence),
        "checks": dynamic_checks,
        "passed": all(bool(item["passed"]) for item in dynamic_checks),
    }
    class_results: dict[str, Any] = {}
    equivalent = True
    advantage = False
    for task_class in protocol["generation"]["task_classes"]:
        full = full_runs[str(task_class)]
        baseline = baseline_runs[str(task_class)]
        require(full["prompt_sha256"] == baseline["prompt_sha256"], f"{task_class} prompts differ between conditions")
        quality_gain = float(full["success_rate"]) - float(baseline["success_rate"])
        latency_reduction = 1 - float(full["elapsed_seconds"]) / float(baseline["elapsed_seconds"]) if baseline["elapsed_seconds"] else 0.0
        full_cost = int(full["tool_calls"]) + int(full["search_calls"])
        baseline_cost = int(baseline["tool_calls"]) + int(baseline["search_calls"])
        cost_reduction = 1 - full_cost / baseline_cost if baseline_cost else 0.0
        class_equivalent = quality_gain >= -float(statistics["ablation_equivalence_margin"])
        class_advantage = quality_gain >= float(statistics["minimum_quality_gain"]) or (
            class_equivalent
            and (
                latency_reduction >= float(statistics["minimum_latency_or_cost_reduction"])
                or cost_reduction >= float(statistics["minimum_latency_or_cost_reduction"])
            )
        )
        equivalent = equivalent and class_equivalent
        advantage = advantage or class_advantage
        class_results[str(task_class)] = {
            "full_success_rate": full["success_rate"],
            "baseline_success_rate": baseline["success_rate"],
            "quality_gain": quality_gain,
            "full_elapsed_seconds": full["elapsed_seconds"],
            "baseline_elapsed_seconds": baseline["elapsed_seconds"],
            "latency_reduction": latency_reduction,
            "full_tool_and_search_cost": full_cost,
            "baseline_tool_and_search_cost": baseline_cost,
            "cost_reduction": cost_reduction,
            "equivalent": class_equivalent,
            "meaningful_advantage": class_advantage,
        }
    full_cost = {
        "ingestion_operations": full_mapping["operation_count"],
        "agent_tool_calls": sum(value["tool_calls"] for value in full_runs.values()),
        "organization_seconds": full_mapping["organization_seconds"],
        "agent_seconds": sum(value["elapsed_seconds"] for value in full_runs.values()),
    }
    baseline_cost = {
        "ingestion_operations": baseline_mapping["operation_count"],
        "agent_tool_calls": sum(value["tool_calls"] for value in baseline_runs.values()),
        "organization_seconds": baseline_mapping["organization_seconds"],
        "agent_seconds": sum(value["elapsed_seconds"] for value in baseline_runs.values()),
    }
    same_non_relation_configuration = (
        full_mapping["operation_count"] == baseline_mapping["operation_count"]
        and full_mapping["stable_ids"] == baseline_mapping["stable_ids"]
        and full_mapping["revisions"] == baseline_mapping["revisions"]
        and full_mapping["data_directory"] == "full-data"
        and baseline_mapping["data_directory"] == "baseline-data"
        and baseline_mapping.get("source_mapping_sha256") == sha256(args.evidence_dir / "full-mapping.json")
        and baseline_mapping.get("source_state_tree_sha256") == baseline_mapping.get("baseline_state_tree_sha256")
    )
    ablation_checks = [
        {"name": "same-candidate-binary-and-dataset", "passed": True},
        {"name": "only-relation-organization-disabled", "passed": same_non_relation_configuration},
        {"name": "per-class-no-regression-beyond-equivalence", "passed": equivalent},
        {"name": "meaningful-organization-advantage", "passed": advantage},
        {"name": "complete-quality-latency-and-cost-accounting", "passed": True},
    ]
    ablation = {
        "schema": ABLATION_REPORT_SCHEMA,
        "candidate": args.candidate,
        "release_binary_version": args.candidate,
        "release_binary_sha256": sha256(args.binary),
        "measured_at": datetime.now(timezone.utc).isoformat(),
        "protocol_sha256": sha256(args.protocol_path),
        "dataset_sha256": sha256(dataset_paths["dataset"]),
        "only_difference": ["relation organization state", "relation signals", "relation navigation"],
        "full_disable_relations": False,
        "baseline_disable_relations": True,
        "task_classes": class_results,
        "full_total_cost": full_cost,
        "baseline_total_cost": baseline_cost,
        "statistics": {
            "equivalence_margin": statistics["ablation_equivalence_margin"],
            "minimum_quality_gain": statistics["minimum_quality_gain"],
            "minimum_latency_or_cost_reduction": statistics["minimum_latency_or_cost_reduction"],
        },
        "checks": ablation_checks,
        "passed": all(bool(item["passed"]) for item in ablation_checks),
    }
    return dynamic, ablation


def _parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repository", type=Path, default=Path("."))
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--candidate", required=True)
    parser.add_argument("--protocol", dest="protocol_path", type=Path, default=HERE / "protocol.json")
    parser.add_argument("--evidence-dir", type=Path, required=True)
    parser.add_argument("--dynamic-output", type=Path, required=True)
    parser.add_argument("--ablation-output", type=Path, required=True)
    parser.add_argument("--codex-binary", type=Path, required=True)
    parser.add_argument("--codex-auth-file", type=Path, required=True)
    parser.add_argument("--runtime-dir", type=Path, required=True)
    parser.add_argument("--resume", action="store_true")
    return parser.parse_args()


def main() -> None:
    args = _parse_args()
    args.repository = args.repository.resolve()
    args.binary = args.binary.resolve()
    args.protocol_path = args.protocol_path.resolve()
    args.evidence_dir = args.evidence_dir.resolve()
    args.dynamic_output = args.dynamic_output.resolve()
    args.ablation_output = args.ablation_output.resolve()
    args.codex_binary = args.codex_binary.resolve()
    args.codex_auth_file = args.codex_auth_file.resolve()
    args.runtime_dir = args.runtime_dir.resolve()
    for path, label in (
        (args.binary, "release binary"),
        (args.protocol_path, "dynamic protocol"),
        (args.codex_binary, "Codex binary"),
        (args.codex_auth_file, "Codex auth file"),
    ):
        require(path.is_file(), f"{label} does not exist: {path}")
    require(args.runtime_dir.is_dir(), f"accepted product runtime does not exist: {args.runtime_dir}")
    require(args.codex_binary.suffix.lower() not in {".ps1", ".cmd", ".bat", ".js"}, "Codex evidence must bind the native executable, not a launcher script")
    require(len(args.candidate) == 40 and all(value in "0123456789abcdef" for value in args.candidate), "candidate must be a full lowercase Git hash")
    head, status = _repository_candidate(args.repository)
    require(head == args.candidate, "repository HEAD differs from the frozen candidate")
    require(not status.strip(), "repository must be clean before dynamic data generation")
    require(_binary_text(args.binary, "version") == args.candidate, "release binary version differs from the candidate")
    protocol = load_json(args.protocol_path)
    validate_protocol(protocol)
    args.protocol = protocol
    require(_binary_text(args.codex_binary, "--version") == protocol["runtime"]["codex_cli_version"], "Codex CLI version differs from the frozen protocol")
    if args.evidence_dir.exists():
        require(args.resume, "evidence directory already exists; use --resume")
    else:
        args.evidence_dir.mkdir(parents=True)
    generator_environment = os.environ.copy()
    dataset, dataset_paths = _prepare_dataset(args, protocol, generator_environment)
    full_mapping, full_environment = _ingest_condition(args, dataset, condition="full", disable_relations=False)
    baseline_mapping, baseline_environment = _ingest_condition(args, dataset, condition="baseline", disable_relations=True)
    (full_integrity, full_asset_evidence), (baseline_integrity, baseline_asset_evidence) = _verify_asset_pair(
        args,
        dataset,
        full_mapping,
        full_environment,
        baseline_mapping,
        baseline_environment,
    )
    asset_integrity = {
        "full": full_integrity,
        "baseline": baseline_integrity,
        "passed": full_integrity["passed"] and baseline_integrity["passed"],
    }
    asset_evidence_path = args.evidence_dir / "asset-integrity.json"
    write_json(
        asset_evidence_path,
        {
            "schema": "ownward.dynamic-asset-integrity/v1",
            "full": full_asset_evidence,
            "baseline": baseline_asset_evidence,
        },
    )
    organization, organization_evidence = _organization_metrics(args, dataset, full_mapping, full_environment)
    organization_evidence_path = args.evidence_dir / "organization-relations.json"
    write_json(organization_evidence_path, organization_evidence)
    operation_stall = float(protocol["execution"]["organization_operation_stall_seconds"])
    with OwnwardRuntime(
        args.binary,
        args.evidence_dir / str(full_mapping["data_directory"]),
        args.runtime_dir,
        full_environment,
        startup_seconds=operation_stall,
        operation_seconds=operation_stall,
    ) as full_runtime, OwnwardRuntime(
        args.binary,
        args.evidence_dir / str(baseline_mapping["data_directory"]),
        args.runtime_dir,
        baseline_environment,
        startup_seconds=operation_stall,
        operation_seconds=operation_stall,
    ) as baseline_runtime:
        require(full_runtime.binding is not None and baseline_runtime.binding is not None, "query runtimes did not start")
        full_runs, full_evidence, baseline_runs, baseline_evidence = _run_agent_pairs(
            args,
            dataset,
            full_mapping,
            {**full_environment, "OWNWARD_MCP_BEARER_TOKEN": full_runtime.binding.bearer_token},
            baseline_mapping,
            {**baseline_environment, "OWNWARD_MCP_BEARER_TOKEN": baseline_runtime.binding.bearer_token},
            protocol,
            full_runtime.binding.endpoint,
            baseline_runtime.binding.endpoint,
        )
    dynamic, ablation = _build_reports(
        args,
        protocol,
        dataset,
        dataset_paths,
        full_mapping,
        baseline_mapping,
        full_runs,
        baseline_runs,
        full_evidence,
        baseline_evidence,
        {"asset_integrity": asset_evidence_path, "organization": organization_evidence_path},
        asset_integrity,
        organization,
    )
    dynamic = _seal_report(args.dynamic_output, dynamic, resume=args.resume)
    ablation["dynamic_report_sha256"] = sha256(args.dynamic_output)
    ablation = _seal_report(args.ablation_output, ablation, resume=args.resume)
    print(json.dumps({"dynamic": dynamic["passed"], "organization_ablation": ablation["passed"]}, ensure_ascii=False))
    if not dynamic["passed"] or not ablation["passed"]:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
