from __future__ import annotations

from dataclasses import dataclass
import json
from typing import Any

try:
    from adapters.product.codex_transport import command_prefix, isolated_environment
except ModuleNotFoundError:  # standalone adapter import
    from codex_transport import command_prefix, isolated_environment


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
    protocol_operations: tuple[str, ...]


def load_exec_events(text: str) -> SessionTrace:
    session_id = ""
    calls: list[ToolCall] = []
    bypassed: list[str] = []
    protocol_operations: list[str] = []
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
        protocol_operation = _resource_discovery_protocol_operation(item, name)
        if protocol_operation is not None:
            protocol_operations.append(protocol_operation)
            continue
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
    return SessionTrace(session_id, calls, bool(bypassed), tuple(bypassed), tuple(protocol_operations))


def _resource_discovery_protocol_operation(item: dict[str, Any], name: str) -> str | None:
    if (
        item.get("type") != "mcp_tool_call"
        or name != "list_mcp_resources"
    ):
        return None
    arguments = item.get("arguments")
    if not isinstance(arguments, dict) or arguments.get("cursor") not in {None, ""}:
        return None
    if item.get("status") != "completed" or item.get("error") is not None:
        return "list_mcp_resources:failed" if item.get("result") is None else None
    result = item.get("result")
    content = result.get("content") if isinstance(result, dict) else None
    if not isinstance(content, list) or len(content) != 1 or not isinstance(content[0], dict):
        return None
    payload = _json_fragment(content[0].get("text"))
    if isinstance(payload, dict) and payload.get("resources") == []:
        return "list_mcp_resources:empty"
    return None


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
