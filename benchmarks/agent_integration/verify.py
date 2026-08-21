#!/usr/bin/env python3
from __future__ import annotations

import argparse
from dataclasses import dataclass
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
from typing import Any


REPORT_SCHEMA = "ownward.agent-integration-report/v1"
EXPECTED_INITIAL_CONTENT = "Ownward acceptance Project Borealis requires one action before migration: verify backups."
EXPECTED_FINAL_CONTENT = "Ownward acceptance Project Borealis requires two actions before migration: verify backups; test restoration."
EXCLUDED_TRANSIENT_CONTENT = "Scratch note: the acceptance controller is currently considering retry 2; this belongs only to this session."
EXPECTED_APPLIED_ACTIONS = ["verify backups", "test restoration"]
SEARCH_QUERY = "Ownward acceptance Project Borealis"
EXPECTED_CODEX_MODEL = "gpt-5.4"
EXPECTED_CODEX_REASONING_EFFORT = "low"
MUTATION_PROMPT = f"""Use only the connected Ownward tools and do not use shell or file tools. First obtain Ownward's collaboration rules. Evaluate each statement below only under those rules, and complete any Ownward maintenance required by those rules and by returned organization states.

Statement A: {EXPECTED_INITIAL_CONTENT}
Statement B: {EXCLUDED_TRANSIENT_CONTENT}

Search for Project Borealis before making any mutation. For each statement that belongs in Ownward under the retrieved rules, persist it through Ownward. Search for and read Statement A after creation. Then treat the following as the corrected full content of Statement A, update the same stable information item using its observed revision, and search for and read it again:

{EXPECTED_FINAL_CONTENT}"""


@dataclass(frozen=True)
class ToolCall:
    name: str
    arguments: dict[str, Any]
    result: Any


@dataclass(frozen=True)
class SessionTrace:
    session_id: str
    agent: str
    calls: list[ToolCall]
    bypassed: bool
    user_text: str = ""
    model: str = ""
    reasoning_effort: str = ""
    tool_call_count: int = 0
    bypass_operations: tuple[str, ...] = ()


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise RuntimeError(message)


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def write_evidence_trace(path: Path, trace: SessionTrace) -> None:
    """只保留可复核的 Ownward 工具事实，不复制会话提示和私有上下文。"""
    lines = [
        json.dumps(
            {
                "type": "session",
                "session_id": trace.session_id,
                "agent": trace.agent,
                "model": trace.model,
                "reasoning_effort": trace.reasoning_effort,
                "prompt_sha256": hashlib.sha256(trace.user_text.strip().encode("utf-8")).hexdigest()
                if trace.user_text
                else "",
                "tool_call_count": trace.tool_call_count or len(trace.calls),
                "bypassed": trace.bypassed,
            },
            ensure_ascii=False,
            separators=(",", ":"),
        )
    ]
    lines.extend(
        json.dumps(
            {
                "type": "ownward_tool_call",
                "name": call.name,
                "arguments": call.arguments,
                "result": call.result,
            },
            ensure_ascii=False,
            separators=(",", ":"),
        )
        for call in trace.calls
    )
    path.write_text("\n".join(lines) + "\n", encoding="utf-8")


def _json_fragment(value: object) -> Any:
    if not isinstance(value, str):
        return value
    text = value.strip()
    for marker in ("\nFinal output:\n", "\nOutput:\n"):
        if marker in text:
            text = text.rsplit(marker, 1)[1].strip()
            break
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
        _require(isinstance(parsed, dict), "tool arguments must be an object")
        return parsed
    raise RuntimeError("tool arguments must be an object")


def _tool_name(value: object) -> str:
    name = str(value or "").strip()
    if "__" in name:
        name = name.rsplit("__", 1)[-1]
    return name


def load_exec_events(text: str) -> SessionTrace:
    session_id = ""
    calls: list[ToolCall] = []
    bypassed = False
    bypass_operations: list[str] = []
    tool_call_count = 0
    for line in text.splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if event.get("type") == "thread.started":
            session_id = str(event.get("thread_id", "")).strip()
            continue
        if event.get("type") != "item.completed":
            continue
        item = event.get("item")
        if not isinstance(item, dict):
            continue
        item_type = item.get("type")
        if item_type in {"agent_message", "reasoning", "todo_list"}:
            continue
        tool_call_count += 1
        if item_type != "mcp_tool_call":
            bypassed = True
            bypass_operations.append(str(item_type))
            continue
        name = _tool_name(item.get("tool"))
        if item.get("server") != "ownward" or not name.startswith("ownward_"):
            bypassed = True
            bypass_operations.append(f"{item_type}:{item.get('server')}:{name}")
            continue
        _require(
            item.get("status") == "completed" and item.get("error") is None,
            f"{name} did not complete: status={item.get('status')!r}, error={item.get('error')!r}, "
            f"arguments={item.get('arguments')!r}, result={item.get('result')!r}",
        )
        result = item.get("result")
        if isinstance(result, dict) and "structured_content" in result:
            result = result["structured_content"]
        calls.append(ToolCall(name, _arguments(item.get("arguments", {})), _json_fragment(result)))
    _require(bool(session_id), "Codex event stream does not contain a thread id")
    return SessionTrace(
        session_id,
        "Codex/openai",
        calls,
        bypassed,
        tool_call_count=tool_call_count,
        bypass_operations=tuple(bypass_operations),
    )


def _result_information(call: ToolCall) -> dict[str, Any]:
    result = call.result
    _require(isinstance(result, dict), f"{call.name} returned a non-object result")
    if call.name in {"ownward_create", "ownward_update"}:
        result = result.get("result")
        _require(isinstance(result, dict), f"{call.name} result is incomplete")
    information = result.get("information")
    _require(isinstance(information, dict), f"{call.name} did not return information")
    return information


def validate_mutation_session(
    trace: SessionTrace,
    *,
    expected_initial_content: str | None = None,
    expected_final_content: str | None = None,
    excluded_transient_content: str | None = None,
    expected_prompt: str | None = None,
    expected_model: str | None = None,
    expected_reasoning_effort: str | None = None,
) -> dict[str, Any]:
    _require(not trace.bypassed, "mutation session used a non-Ownward tool")
    _require(trace.tool_call_count in {0, len(trace.calls)}, "mutation session contains unaccounted tool calls")
    if expected_prompt is not None:
        _require(trace.user_text.strip() == expected_prompt, "mutation session did not use the fixed acceptance prompt")
    if expected_model is not None:
        _require(trace.model == expected_model, "mutation session used another Codex model")
    if expected_reasoning_effort is not None:
        _require(trace.reasoning_effort == expected_reasoning_effort, "mutation session used another reasoning effort")
    names = [call.name for call in trace.calls]
    _require(
        names.count("ownward_rules") >= 1
        and names.count("ownward_create") == 1
        and names.count("ownward_update") == 1,
        "mutation session is missing required lifecycle operations",
    )
    rules_position = names.index("ownward_rules")
    create_position = names.index("ownward_create")
    update_position = names.index("ownward_update")
    search_positions = [position for position, name in enumerate(names) if name == "ownward_search"]
    read_positions = [position for position, name in enumerate(names) if name == "ownward_read"]
    semantic_work_positions = [position for position, name in enumerate(names) if name == "ownward_semantic_work"]
    semantic_submit_positions = [
        position
        for position, name in enumerate(names)
        if name in {"ownward_semantic_submit", "ownward_semantic_submit_batch"}
    ]
    _require(
        search_positions
        and rules_position < search_positions[0] < create_position < update_position
        and any(create_position < position < update_position for position in search_positions)
        and any(create_position < position < update_position for position in read_positions)
        and any(position > update_position for position in search_positions)
        and any(position > update_position for position in read_positions),
        f"mutation session did not complete the required Ownward lifecycle: {names}",
    )
    _require(
        len(semantic_work_positions) == 2
        and len(semantic_submit_positions) == 2
        and create_position < semantic_work_positions[0] < semantic_submit_positions[0] < update_position
        and update_position < semantic_work_positions[1] < semantic_submit_positions[1],
        f"the connected agent did not complete both semantic collaboration cycles: {names}",
    )

    rules = next(call for call in trace.calls if call.name == "ownward_rules")
    create_call = next(call for call in trace.calls if call.name == "ownward_create")
    update_call = next(call for call in trace.calls if call.name == "ownward_update")
    search_calls = [call for call in trace.calls if call.name == "ownward_search"]
    read_calls = [call for call in trace.calls if call.name == "ownward_read"]
    created = _result_information(create_call)
    updated = _result_information(update_call)
    reads = [_result_information(call) for call in read_calls]
    _require(len(reads) >= 2, "mutation session must read before and after the update")
    _require(isinstance(rules.result, dict), "mutation session did not obtain collaboration rules")
    rules_text = str(rules.result.get("rules", ""))
    _require("长期复用" in rules_text and "临时工作状态" in rules_text, "Ownward collaboration rules do not define the asset boundary")
    stable_id = str(created.get("id", ""))
    _require(bool(stable_id), "created information has no stable id")
    _require(created.get("revision") == 1, "created information must start at revision 1")
    for position in semantic_work_positions:
        _require(
            trace.calls[position].arguments.get("asset_ids") == [stable_id],
            "semantic work was not scoped to the mutated information",
        )
    for position in semantic_submit_positions:
        call = trace.calls[position]
        if call.name == "ownward_semantic_submit_batch":
            submissions = call.arguments.get("submissions")
            _require(isinstance(submissions, list) and len(submissions) == 1, "semantic submission batch is incomplete")
            submission = submissions[0]
        else:
            submission = call.arguments.get("submission")
        _require(
            isinstance(submission, dict)
            and submission.get("asset_id") == stable_id
            and submission.get("status") in {"complete", "uncertain"}
            and isinstance(submission.get("capability"), dict)
            and str(submission["capability"].get("id", "")).strip(),
            "semantic submission is not bound to the external capability and asset",
        )
        result = call.result
        if call.name == "ownward_semantic_submit_batch":
            _require(
                isinstance(result, dict)
                and isinstance(result.get("results"), list)
                and len(result["results"]) == 1
                and not result["results"][0].get("error"),
                "Ownward did not accept the external semantic result",
            )
        else:
            _require(
                isinstance(result, dict) and isinstance(result.get("organization"), dict),
                "Ownward did not accept the external semantic result",
            )
    _require(create_call.arguments.get("content") == created.get("content"), "create input and persisted content differ")
    _require(updated.get("id") == stable_id and updated.get("revision") == 2, "update did not preserve identity and advance revision")
    _require(update_call.arguments.get("id") == stable_id and update_call.arguments.get("expected_revision") == 1, "update did not use the stable id and observed revision")
    _require(update_call.arguments.get("content") == updated.get("content"), "update input and persisted content differ")
    _require(reads[0].get("id") == stable_id and reads[0].get("revision") == 1, "pre-update read is inconsistent")
    _require(reads[-1] == updated, "post-update read differs from the update result")
    _require(all(call.arguments.get("id") == stable_id for call in read_calls), "mutation session read a different information item")
    first_search = search_calls[0].result
    _require(
        isinstance(first_search, dict) and first_search.get("results") == [],
        "the fixed growth scenario was not empty before the lesson was persisted",
    )
    for call in search_calls[1:]:
        result = call.result
        _require(isinstance(result, dict) and isinstance(result.get("results"), list), "search returned an invalid result")
        _require(any(isinstance(item, dict) and item.get("id") == stable_id for item in result["results"]), "search did not return the mutated information")
    if expected_initial_content is not None:
        _require(created.get("content") == expected_initial_content, "created content differs from the fixed acceptance input")
    if expected_final_content is not None:
        _require(updated.get("content") == expected_final_content, "updated content differs from the fixed acceptance input")
    if excluded_transient_content is not None:
        _require(excluded_transient_content in trace.user_text, "mutation session was not given the fixed transient state")
        _require(
            all(excluded_transient_content not in str(call.arguments.get("content", "")) for call in trace.calls),
            "external agent persisted the transient session state",
        )
    _require(created.get("content") != updated.get("content"), "update did not change the information")
    return updated


def validate_read_session(trace: SessionTrace, expected: dict[str, Any]) -> None:
    _require(
        not trace.bypassed,
        f"independent read session used a bypass tool: {trace.bypass_operations}",
    )
    _require(trace.session_id, "independent read session has no identity")
    searches = [call for call in trace.calls if call.name == "ownward_search"]
    reads = [_result_information(call) for call in trace.calls if call.name == "ownward_read"]
    _require(searches and reads, "independent session must search and read through Ownward")
    _require(reads[-1] == expected, "independent session did not observe the final information")


def _command_prefix(binary: Path) -> list[str]:
    if binary.suffix.lower() == ".ps1":
        shell = shutil.which("pwsh") or shutil.which("powershell")
        _require(shell is not None, "PowerShell is required to run Codex")
        return [shell, "-NoProfile", "-File", str(binary)]
    return [str(binary)]


def _codex_command(args: argparse.Namespace, agent_dir: Path, *, allow_mutation: bool = False) -> list[str]:
    command = _command_prefix(args.codex_binary) + [
        "exec",
        "--ephemeral",
        "--json",
        "--color",
        "never",
        "--skip-git-repo-check",
        "-C",
        str(agent_dir),
        "--sandbox",
        "workspace-write",
        "-m",
        args.codex_model,
        "-c",
        f"model_reasoning_effort={json.dumps(args.codex_reasoning_effort)}",
        "-c",
        f"mcp_servers.ownward.command={json.dumps(str(args.binary))}",
        "-c",
        f"mcp_servers.ownward.args={json.dumps(['mcp', '--data-dir', str(args.data_dir), '--runtime-dir', str(args.runtime_dir)])}",
        "-c",
        "features.apps=false",
        "-c",
        "features.multi_agent=false",
        "-c",
        "features.personality=false",
        "-c",
        "features.plugins=false",
        "-c",
        "features.shell_snapshot=false",
        "-c",
        "features.shell_tool=false",
        "-c",
        'web_search="disabled"',
    ]
    if args.codex_service_tier:
        command.extend(["-c", f"service_tier={json.dumps(args.codex_service_tier)}"])
    if allow_mutation:
        command.extend(
            [
                "-c",
                'mcp_servers.ownward.tools.ownward_create.approval_mode="approve"',
                "-c",
                'mcp_servers.ownward.tools.ownward_update.approval_mode="approve"',
                "-c",
                'mcp_servers.ownward.tools.ownward_semantic_submit_batch.approval_mode="approve"',
                "-c",
                'mcp_servers.ownward.tools.ownward_semantic_submit.approval_mode="approve"',
            ]
        )
    return command


def _isolated_codex_environment(auth_file: Path, codex_home: Path) -> dict[str, str]:
    codex_home.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(auth_file, codex_home / "auth.json")
    environment = os.environ.copy()
    environment["CODEX_HOME"] = str(codex_home)
    environment.pop("OPENAI_API_KEY", None)
    return environment


def _bound_trace(parsed: SessionTrace, args: argparse.Namespace, prompt: str = "") -> SessionTrace:
    return SessionTrace(
        parsed.session_id,
        parsed.agent,
        parsed.calls,
        parsed.bypassed,
        prompt,
        args.codex_model,
        args.codex_reasoning_effort,
        parsed.tool_call_count,
        parsed.bypass_operations,
    )


def run_mutation_session(args: argparse.Namespace) -> tuple[SessionTrace, dict[str, Any]]:
    agent_dir = args.data_dir.parent / "agent-mutation"
    _require(not agent_dir.exists() or not any(agent_dir.iterdir()), "mutation Codex work directory is not blank")
    agent_dir.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="ownward-agent-mutation-") as temporary:
        environment = _isolated_codex_environment(args.codex_auth_file, Path(temporary) / "codex-home")
        command = _codex_command(args, agent_dir, allow_mutation=True)
        command.append(MUTATION_PROMPT)
        completed = subprocess.run(
            command,
            check=False,
            capture_output=True,
            text=True,
            encoding="utf-8",
            timeout=args.timeout,
            env=environment,
        )
    _require(completed.returncode == 0, f"mutation Codex session failed: {completed.stderr[-2000:]}")
    trace = _bound_trace(load_exec_events(completed.stdout), args, MUTATION_PROMPT)
    expected = validate_mutation_session(
        trace,
        expected_initial_content=EXPECTED_INITIAL_CONTENT,
        expected_final_content=EXPECTED_FINAL_CONTENT,
        excluded_transient_content=EXCLUDED_TRANSIENT_CONTENT,
        expected_prompt=MUTATION_PROMPT,
        expected_model=EXPECTED_CODEX_MODEL,
        expected_reasoning_effort=EXPECTED_CODEX_REASONING_EFFORT,
    )
    return trace, expected


def run_independent_session(args: argparse.Namespace, expected: dict[str, Any]) -> tuple[SessionTrace, dict[str, Any]]:
    agent_dir = args.data_dir.parent / "agent-independent"
    _require(not agent_dir.exists() or not any(agent_dir.iterdir()), "independent Codex work directory is not blank")
    agent_dir.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="ownward-agent-result-") as temporary:
        temporary_root = Path(temporary)
        result_path = temporary_root / "result.txt"
        schema_path = temporary_root / "result.schema.json"
        schema_path.write_text(
            json.dumps(
                {
                    "type": "object",
                    "additionalProperties": False,
                    "required": ["stable_id", "revision", "content", "required_actions"],
                    "properties": {
                        "stable_id": {"type": "string"},
                        "revision": {"type": "integer"},
                        "content": {"type": "string"},
                        "required_actions": {"type": "array", "items": {"type": "string"}},
                    },
                }
            ),
            encoding="utf-8",
        )
        command = _codex_command(args, agent_dir)
        command.extend(
            [
                "--output-schema",
                str(schema_path),
                "-o",
                str(result_path),
                (
                    "Use only the Ownward MCP tools. Do not use shell commands or read files directly. "
                    f"Project Borealis is about to migrate. Search for {SEARCH_QUERY!r}, read the matching information, "
                    "and use it to identify every action required before migration. Return the stable ID, revision, full content, "
                    "and required_actions using the exact action wording from the information. This is a new independent session."
                ),
            ]
        )
        environment = _isolated_codex_environment(args.codex_auth_file, temporary_root / "codex-home")
        completed = subprocess.run(
            command,
            check=False,
            capture_output=True,
            text=True,
            encoding="utf-8",
            timeout=args.timeout,
            env=environment,
        )
        _require(completed.returncode == 0, f"independent Codex session failed: {completed.stderr[-2000:]}")
        _require(result_path.exists(), "independent Codex session produced no final result")
        final_result = json.loads(result_path.read_text(encoding="utf-8"))
    parsed = load_exec_events(completed.stdout)
    trace = _bound_trace(parsed, args)
    validate_read_session(trace, expected)
    expected_result = {
        "stable_id": expected["id"],
        "revision": expected["revision"],
        "content": expected["content"],
        "required_actions": EXPECTED_APPLIED_ACTIONS,
    }
    _require(
        final_result == expected_result,
        "independent agent did not correctly apply the persisted lesson: "
        f"expected={expected_result!r}, actual={final_result!r}",
    )
    return trace, final_result


def _direct_read(binary: Path, data_dir: Path, stable_id: str) -> dict[str, Any]:
    completed = subprocess.run(
        [str(binary), "read", "--data-dir", str(data_dir), "--id", stable_id],
        check=False,
        capture_output=True,
        text=True,
        encoding="utf-8",
        timeout=60,
    )
    _require(completed.returncode == 0, f"final binary could not read acceptance data: {completed.stderr.strip()}")
    result = json.loads(completed.stdout)
    _require(isinstance(result, dict), "final binary returned invalid information")
    return result


def _binary_version(binary: Path) -> str:
    completed = subprocess.run(
        [str(binary), "version"], check=False, capture_output=True, text=True, encoding="utf-8", timeout=30
    )
    _require(completed.returncode == 0, f"could not read final binary version: {completed.stderr.strip()}")
    return completed.stdout.strip()


def _parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--data-dir", type=Path, required=True)
    parser.add_argument("--runtime-dir", type=Path, required=True)
    parser.add_argument("--codex-binary", type=Path, required=True)
    parser.add_argument("--codex-auth-file", type=Path, required=True)
    parser.add_argument("--codex-model", default=EXPECTED_CODEX_MODEL)
    parser.add_argument("--codex-reasoning-effort", default=EXPECTED_CODEX_REASONING_EFFORT)
    parser.add_argument("--codex-service-tier", default="")
    parser.add_argument("--candidate", required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--timeout", type=float, default=900)
    return parser.parse_args()


def main() -> None:
    args = _parse_args()
    args.binary = args.binary.resolve()
    args.data_dir = args.data_dir.resolve()
    args.runtime_dir = args.runtime_dir.resolve()
    args.codex_binary = args.codex_binary.resolve()
    args.codex_auth_file = args.codex_auth_file.resolve()
    for path, label in (
        (args.binary, "Ownward binary"),
        (args.codex_binary, "Codex binary"),
        (args.codex_auth_file, "Codex auth file"),
    ):
        _require(path.exists() and path.is_file(), f"{label} does not exist: {path}")
    _require(
        not args.data_dir.exists() or not any(args.data_dir.iterdir()),
        "agent acceptance data directory is not blank",
    )
    _require(args.runtime_dir.is_dir(), "accepted product runtime directory does not exist")
    args.data_dir.mkdir(parents=True, exist_ok=True)
    _require(args.codex_model == EXPECTED_CODEX_MODEL, "Codex acceptance model changed")
    _require(args.codex_reasoning_effort == EXPECTED_CODEX_REASONING_EFFORT, "Codex acceptance reasoning effort changed")

    mutation, expected = run_mutation_session(args)
    output = args.output.resolve()
    mutation_trace_path = output.with_suffix(".mutation.jsonl")
    independent_trace_path = output.with_suffix(".independent.jsonl")
    output.parent.mkdir(parents=True, exist_ok=True)
    independent, independent_result = run_independent_session(args, expected)
    _require(mutation.session_id != independent.session_id, "the read verification did not use an independent session")
    write_evidence_trace(mutation_trace_path, mutation)
    write_evidence_trace(independent_trace_path, independent)
    direct = _direct_read(args.binary, args.data_dir, str(expected["id"]))
    _require(direct == expected, "final binary and external agent observed different information")

    binary_version = _binary_version(args.binary)
    _require(binary_version == args.candidate.strip(), "final binary version differs from --candidate")

    asset_log = args.data_dir / "assets" / "information.jsonl"
    _require(asset_log.exists(), "authoritative asset log is missing")
    asset_text = asset_log.read_text(encoding="utf-8")
    _require(EXCLUDED_TRANSIENT_CONTENT not in asset_text, "transient session state entered the authoritative asset log")
    asset_events = [json.loads(line) for line in asset_text.splitlines() if line.strip()]
    _require(len(asset_events) == 2, "agent acceptance data directory was not blank or contains unexpected mutations")
    _require([event.get("operation") for event in asset_events] == ["create", "update"], "asset log does not contain the fixed lifecycle")
    _require(
        all(isinstance(event.get("value"), dict) and event["value"].get("id") == expected["id"] for event in asset_events),
        "asset log contains another information identity",
    )
    asset_trace_path = output.with_suffix(".assets.jsonl")
    asset_trace_path.write_text(asset_text, encoding="utf-8")
    report = {
        "schema": REPORT_SCHEMA,
        "candidate": args.candidate.strip(),
        "release_binary_version": binary_version,
        "measured_at": datetime.now(timezone.utc).isoformat(),
        "agent": mutation.agent,
        "model": EXPECTED_CODEX_MODEL,
        "reasoning_effort": EXPECTED_CODEX_REASONING_EFFORT,
        "release_binary_sha256": _sha256(args.binary),
        "asset_log_sha256": _sha256(asset_trace_path),
        "mutation_session": {"id": mutation.session_id, "trace_sha256": _sha256(mutation_trace_path)},
        "independent_session": {"id": independent.session_id, "trace_sha256": _sha256(independent_trace_path)},
        "independent_result": independent_result,
        "information": {
            "id": expected["id"],
            "revision": expected["revision"],
            "content_sha256": hashlib.sha256(str(expected["content"]).encode("utf-8")).hexdigest(),
            "excluded_content_sha256": hashlib.sha256(EXCLUDED_TRANSIENT_CONTENT.encode("utf-8")).hexdigest(),
            "applied_actions": independent_result["required_actions"],
        },
        "checks": [
            {"name": "rules-create-search-read-update", "passed": True},
            {"name": "external-semantic-collaboration", "passed": True},
            {"name": "stable-identity-and-revision", "passed": True},
            {"name": "independent-session-search-and-read", "passed": True},
            {"name": "growth-closure", "passed": True},
            {"name": "short-term-state-excluded", "passed": True},
            {"name": "no-bypass-tool", "passed": True},
            {"name": "final-binary-authority", "passed": True},
        ],
        "passed": True,
    }
    _require(bool(report["candidate"]), "--candidate must be non-empty")
    encoded = json.dumps(report, ensure_ascii=False, indent=2) + "\n"
    temporary = output.with_suffix(output.suffix + ".tmp")
    temporary.write_text(encoded, encoding="utf-8")
    temporary.replace(output)
    print(encoded, end="")


if __name__ == "__main__":
    main()
