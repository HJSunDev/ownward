from __future__ import annotations

from dataclasses import dataclass
import hashlib
import json
import threading
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
    protocol_operations: tuple[str, ...]


class DynamicToolSession:
    """Provider-neutral Ownward tool surface and replayable call trace."""

    def __init__(self, client: Any, enabled_tools: tuple[str, ...]) -> None:
        self.client = client
        self.enabled_tools = enabled_tools
        manifest = self._list_tools(client)
        by_name = {str(item.get("name", "")): item for item in manifest}
        missing = [name for name in enabled_tools if name not in by_name]
        _require(not missing, f"Ownward tools are missing: {missing}")
        self.dynamic_tools = [self._dynamic_spec(by_name[name]) for name in enabled_tools]
        self.tool_manifest_identity = _canonical_sha256(self.dynamic_tools)
        self._lock = threading.Lock()
        self._calls: list[ToolCall] = []

    @staticmethod
    def _list_tools(client: Any) -> list[dict[str, Any]]:
        direct = getattr(client, "list_tools", None)
        if callable(direct):
            return direct()
        request = getattr(client, "_request", None)
        _require(callable(request), "Ownward MCP client cannot enumerate tools")
        result: list[dict[str, Any]] = []
        cursor = ""
        while True:
            page = request("tools/list", {"cursor": cursor} if cursor else {})
            tools = page.get("tools") if isinstance(page, dict) else None
            _require(isinstance(tools, list) and all(isinstance(item, dict) for item in tools), "Ownward tool manifest is invalid")
            result.extend(tools)
            cursor = str(page.get("nextCursor", page.get("next_cursor", "")) or "")
            if not cursor:
                return result

    @staticmethod
    def _dynamic_spec(tool: dict[str, Any]) -> dict[str, Any]:
        schema = tool.get("inputSchema", tool.get("input_schema"))
        _require(isinstance(schema, dict), f"Ownward tool has no input schema: {tool.get('name')}")
        return {
            "type": "function", "name": str(tool["name"]),
            "description": str(tool.get("description", "")), "inputSchema": schema,
            "deferLoading": False,
        }

    def reset(self) -> None:
        with self._lock:
            self._calls = []

    def call(self, name: str, raw_arguments: Any) -> Any:
        arguments = raw_arguments if isinstance(raw_arguments, dict) else {}
        _require(name in self.enabled_tools, f"external intelligence used a disabled Ownward tool: {name}")
        try:
            result = self.client.call_tool(name, arguments)
        except Exception:
            with self._lock:
                self._calls.append(ToolCall(name, arguments, None, True))
            raise
        with self._lock:
            self._calls.append(ToolCall(name, arguments, result, False))
        return result

    def report(self) -> dict[str, Any]:
        return {
            "schema": "ownward.product-external-intelligence-trace/v1",
            "tool_manifest_identity": self.tool_manifest_identity,
            "calls": [
                {"name": call.name, "arguments": call.arguments, "result": call.result, "error": call.error}
                for call in self._calls
            ],
        }

    def restore(self, value: Any) -> None:
        _require(isinstance(value, dict) and value.get("schema") == "ownward.product-external-intelligence-trace/v1", "external-intelligence tool trace is invalid")
        calls = value.get("calls")
        _require(isinstance(calls, list), "external-intelligence tool trace is incomplete")
        self._calls = [
            ToolCall(str(item["name"]), dict(item["arguments"]), item.get("result"), bool(item.get("error")))
            for item in calls if isinstance(item, dict)
        ]

    def trace(self, session_id: str) -> SessionTrace:
        return SessionTrace(session_id, list(self._calls), False, (), ())

    def events(self, session_id: str) -> str:
        records: list[dict[str, Any]] = [{"type": "thread.started", "thread_id": session_id}]
        records.extend({
            "type": "item.completed",
            "item": {
                "type": "mcp_tool_call", "server": "ownward", "tool": call.name,
                "arguments": call.arguments,
                "result": {"structured_content": call.result},
                "status": "failed" if call.error else "completed",
                "error": "tool call failed" if call.error else None,
            },
        } for call in self._calls)
        return "".join(json.dumps(item, ensure_ascii=False) + "\n" for item in records)


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
        operation = _resource_discovery_protocol_operation(item, name)
        if operation is not None:
            protocol_operations.append(operation)
            continue
        if item_type != "mcp_tool_call" or item.get("server") != "ownward" or not name.startswith("ownward_"):
            bypassed.append(f"{item_type}:{item.get('server')}:{name}")
            continue
        result = item.get("result")
        if isinstance(result, dict) and "structured_content" in result:
            result = result["structured_content"]
        calls.append(ToolCall(
            name=name, arguments=_arguments(item.get("arguments", {})), result=_json_fragment(result),
            error=item.get("status") != "completed" or item.get("error") is not None,
        ))
    _require(bool(session_id), "external-intelligence event stream does not contain a session id")
    return SessionTrace(session_id, calls, bool(bypassed), tuple(bypassed), tuple(protocol_operations))


def _resource_discovery_protocol_operation(item: dict[str, Any], name: str) -> str | None:
    empty_field = {"list_mcp_resources": "resources", "list_mcp_resource_templates": "resourceTemplates"}.get(name)
    if item.get("type") != "mcp_tool_call" or empty_field is None:
        return None
    arguments = item.get("arguments")
    if not isinstance(arguments, dict) or arguments.get("cursor") not in {None, ""}:
        return None
    if item.get("status") != "completed" or item.get("error") is not None:
        return f"{name}:failed" if item.get("result") is None else None
    result = item.get("result")
    content = result.get("content") if isinstance(result, dict) else None
    if not isinstance(content, list) or len(content) != 1 or not isinstance(content[0], dict):
        return None
    payload = _json_fragment(content[0].get("text"))
    return f"{name}:empty" if isinstance(payload, dict) and payload.get(empty_field) == [] else None


def _json_fragment(value: object) -> Any:
    if not isinstance(value, str):
        return value
    text = value.strip()
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


def _canonical_sha256(value: Any) -> str:
    data = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(data).hexdigest()


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise RuntimeError(message)
