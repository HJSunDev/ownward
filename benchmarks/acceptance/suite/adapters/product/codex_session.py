from __future__ import annotations

from dataclasses import dataclass
import json
import os
from pathlib import Path
import shutil
from typing import Any


@dataclass(frozen=True)
class ToolCall:
    name: str
    arguments: dict[str, Any]
    result: Any
    error: bool


@dataclass(frozen=True)
class SessionTrace:
    session_id: str
    calls: list[ToolCall]
    bypassed: bool
    bypass_operations: tuple[str, ...]


def command_prefix(binary: Path) -> list[str]:
    if binary.suffix.lower() == ".ps1":
        shell = shutil.which("pwsh") or shutil.which("powershell")
        _require(shell is not None, "PowerShell is required to run Codex")
        return [shell, "-NoProfile", "-File", str(binary)]
    _require(binary.suffix.lower() not in {".cmd", ".bat"}, "command wrappers are not accepted as evidence entries")
    return [str(binary)]


def isolated_environment(auth_file: Path, codex_home: Path) -> dict[str, str]:
    codex_home.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(auth_file, codex_home / "auth.json")
    environment = os.environ.copy()
    environment["CODEX_HOME"] = str(codex_home)
    environment.pop("OPENAI_API_KEY", None)
    no_proxy = []
    for name in ("NO_PROXY", "no_proxy"):
        for item in environment.get(name, "").split(","):
            item = item.strip()
            if item and item not in no_proxy:
                no_proxy.append(item)
    for item in ("127.0.0.1", "localhost", "::1"):
        if item not in no_proxy:
            no_proxy.append(item)
    environment["NO_PROXY"] = environment["no_proxy"] = ",".join(no_proxy)
    return environment


def load_exec_events(text: str) -> SessionTrace:
    session_id = ""
    calls: list[ToolCall] = []
    bypassed: list[str] = []
    for line in text.splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if event.get("type") == "thread.started":
            session_id = str(event.get("thread_id", "")).strip()
            continue
        if event.get("type") != "item.completed" or not isinstance(event.get("item"), dict):
            continue
        item = event["item"]
        item_type = item.get("type")
        if item_type in {"agent_message", "reasoning", "todo_list"}:
            continue
        name = _tool_name(item.get("tool"))
        if item_type != "mcp_tool_call" or item.get("server") != "ownward" or not name.startswith("ownward_"):
            bypassed.append(f"{item_type}:{item.get('server')}:{name}")
            continue
        result = item.get("result")
        if isinstance(result, dict) and "structured_content" in result:
            result = result["structured_content"]
        calls.append(ToolCall(
            name=name,
            arguments=_arguments(item.get("arguments", {})),
            result=_json_fragment(result),
            error=item.get("status") != "completed" or item.get("error") is not None,
        ))
    _require(bool(session_id), "Codex event stream does not contain a thread id")
    return SessionTrace(session_id, calls, bool(bypassed), tuple(bypassed))


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
        text = text[min(starts):]
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
    return name.rsplit("__", 1)[-1] if "__" in name else name


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise RuntimeError(message)
