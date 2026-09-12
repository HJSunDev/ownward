"""In-process Messages client with independent services and optional task-local routing."""
from __future__ import annotations

from contextlib import contextmanager
from dataclasses import dataclass, field
import copy
import hashlib
import http.client
import json
from pathlib import Path
import socket
import sys
import threading
import time
import traceback
import uuid
from typing import Any, Iterator
from urllib.parse import urlsplit

SUPPORT_ROOT = Path(__file__).resolve().parents[1] / "support"
if str(SUPPORT_ROOT) not in sys.path:
    sys.path.insert(0, str(SUPPORT_ROOT))

from external_intelligence import (
    ExternalIntelligenceError, ExternalIntelligenceTimeout,
    _write_json as _atomic_json, validate_structured_output as _validate_schema,
)
from service_routing import ServiceError, ServiceRoute

# Keep the existing driver identifier so callers still select the third client.
DRIVER = "opencode-go-api/v1"
PROVIDER = "aliyun-bailian"
TransportError = ExternalIntelligenceError
TransportTimeout = ExternalIntelligenceTimeout
IN_PROCESS = True
HOST = "token-plan.cn-beijing.maas.aliyuncs.com"
ENDPOINT = "/apps/anthropic/v1/messages"
MODEL = "qwen3.8-flash"
KEY_FIELD = "BAILIAN_API_KEY"


@dataclass
class MessageService:
    host: str
    endpoint: str
    key: str = field(repr=False)
    session_header: str | None = None


def _load_services(path: Path) -> tuple[dict[str, MessageService], ServiceRoute]:
    try:
        configuration = json.loads(path.read_text(encoding="utf-8-sig"))
        if configuration.get("schema") != "ownward.messages-services/v1":
            key = configuration[KEY_FIELD]
            if not isinstance(key, str) or not key.strip():
                raise ValueError()
            return {PROVIDER: MessageService(HOST, ENDPOINT, key.strip())}, ServiceRoute(PROVIDER)
        primary = configuration["primary"]
        fallback = configuration.get("fallback") or {}
        target = fallback.get("service")
        codes = fallback.get("on_error_codes", [])
        if (not isinstance(primary, str) or not primary or
                (target is not None and (not isinstance(target, str) or target == primary)) or
                not isinstance(codes, list) or any(not isinstance(code, str) or not code for code in codes) or
                (target is not None and not codes)):
            raise ValueError()
        services = {}
        for name in dict.fromkeys([primary] + ([target] if target else [])):
            spec = configuration["services"][name]
            url = urlsplit(spec["url"])
            if url.scheme != "https" or not url.hostname or url.username or url.password or url.query or url.fragment:
                raise ValueError()
            key_path = spec["key_path"]
            if not isinstance(key_path, list) or not key_path or any(not isinstance(part, str) for part in key_path):
                raise ValueError()
            key_file = (path.parent / spec["credential_file"]).resolve()
            key = json.loads(key_file.read_text(encoding="utf-8-sig"))
            for part in key_path:
                key = key[part]
            if not isinstance(key, str) or not key.strip():
                raise ValueError()
            header = spec.get("session_header")
            if header is not None and header != "x-opencode-session":
                raise ValueError()
            services[name] = MessageService(url.netloc, url.path, key.strip(), header)
        return services, ServiceRoute(primary, target, tuple(codes))
    except (OSError, ValueError, KeyError, TypeError, AttributeError):
        raise ExternalIntelligenceError(
            f"Invalid Messages service configuration or local credential; check {KEY_FIELD} / configured key_path"
        ) from None


def identity_files() -> tuple[Path, ...]:
    return (Path(__file__), SUPPORT_ROOT / "external_intelligence.py", SUPPORT_ROOT / "service_routing.py")


def implementation_sha256() -> str:
    return hashlib.sha256(b"".join(p.read_bytes() for p in identity_files())).hexdigest()


def artifact_sha256(binary: Path) -> str:
    return hashlib.sha256(binary.read_bytes()).hexdigest()


def validate(binary: Path, credential_file: Path) -> None:
    if binary.resolve() != Path(__file__).resolve() or not credential_file.is_file():
        raise ExternalIntelligenceError("Bailian API client artifact or credential locator is invalid")


def probe(binary: Path, credential_file: Path) -> dict[str, str]:
    validate(binary, credential_file)
    return {"version": DRIVER, "artifact_sha256": artifact_sha256(binary)}


def _error_code(error: dict[str, Any]) -> str:
    # Some compatible gateways wrap a structured upstream error in message.
    # Read only explicit error objects; a mention of a code in prose is not a cause.
    for _ in range(3):
        message = error.get("message")
        if not isinstance(message, str):
            break
        nested = None
        for raw in [message] + [line[5:] for line in message.splitlines() if line.startswith("data:")]:
            try:
                value = json.loads(raw)
            except ValueError:
                continue
            if isinstance(value, dict) and isinstance(value.get("error"), dict):
                nested = value["error"]
                break
        if nested is None:
            break
        error = nested
    return str(error.get("code", error.get("type", "")))


def normalize_tool_arguments(value, schema):
    """Restore explicitly declared container types without changing string values."""
    kinds = schema.get("type", [])
    if isinstance(kinds, str):
        kinds = [kinds]
    if isinstance(value, str) and "string" not in kinds and any(k in kinds for k in ("object", "array")):
        try:
            decoded = json.loads(value)
        except (ValueError, TypeError):
            return value
        if (isinstance(decoded, dict) and "object" in kinds) or (isinstance(decoded, list) and "array" in kinds):
            value = decoded
    if isinstance(value, dict) and "object" in kinds:
        properties = schema.get("properties", {})
        return {key: normalize_tool_arguments(item, properties.get(key, {})) for key, item in value.items()}
    if isinstance(value, list) and "array" in kinds:
        return [normalize_tool_arguments(item, schema.get("items", {})) for item in value]
    return value


class GoAPIClient:
    def __init__(self, credential_file: Path, max_active: int, identity: dict[str, Any]) -> None:
        self._services, self._route = _load_services(credential_file)
        self.identity = dict(identity)
        self._slots = threading.BoundedSemaphore(max_active)
        self._lock = threading.Lock()
        self._active = self._maximum = self._requests = 0
        self._rate_limited = False

    @property
    def _key(self) -> str:
        return self._services[self._route.primary].key

    def new_scope(self) -> ScopedClient:
        return ScopedClient(self, self._route.new_scope())

    @staticmethod
    def _remaining(deadline: float) -> float:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise ExternalIntelligenceTimeout("Bailian API turn exhausted its timeout")
        return remaining

    def _post(self, body: dict[str, Any], session: str, deadline: float, path: Path,
              *, service: str | None = None) -> dict[str, Any]:
        service = service or self._route.primary
        destination = self._services[service]
        blocks: dict[int, dict[str, Any]] = {}
        connection = http.client.HTTPSConnection(destination.host, timeout=self._remaining(deadline))
        try:
            connection_failures = []
            for attempt in range(3):
                try:
                    # No model request has been sent yet: retry the handshake in place.
                    connection.connect()
                    break
                except (OSError, http.client.HTTPException) as error:
                    connection.close()
                    connection_failures.append({"attempt": attempt + 1, "error_type": type(error).__name__,
                                                "errno": getattr(error, "errno", None)})
                    _atomic_json(path.with_suffix(".connection-retries.json"), {"attempts": connection_failures})
                    if attempt == 2:
                        raise
                    time.sleep(min(0.2 * (attempt + 1), self._remaining(deadline)))
                    connection = http.client.HTTPSConnection(destination.host, timeout=self._remaining(deadline))
            headers = {
                "Content-Type": "application/json", "Accept": "text/event-stream",
                "x-api-key": destination.key, "anthropic-version": "2023-06-01",
                "User-Agent": "Ownward-Lightweight-Agent/1.0",
            }
            if destination.session_header:
                headers[destination.session_header] = session
            connection.request("POST", destination.endpoint, json.dumps(body, ensure_ascii=False).encode("utf-8"), headers)
            response = connection.getresponse()
            if response.status != 200:
                with self._lock:
                    self._rate_limited |= response.status == 429
                details: dict[str, Any] = {"http_status": response.status}
                # Keep bounded service error fields, with authentication explicitly removed.
                raw = response.read(16384)
                if isinstance(raw, bytes):
                    candidates = [raw] + [line[5:] for line in raw.splitlines() if line.startswith(b"data:")]
                    for candidate in candidates:
                        try:
                            value = json.loads(candidate)
                        except (ValueError, UnicodeError):
                            continue
                        error = value.get("error", value) if isinstance(value, dict) else {}
                        if isinstance(error, dict):
                            details["error"] = {name: str(error[name]).replace(destination.key, "[redacted]")[:2000]
                                                for name in ("code", "type", "message") if name in error}
                            break
                _atomic_json(path.with_suffix(".error.json"), details)
                error = details.get("error", {})
                raise ServiceError(f"Messages API HTTP {response.status}", _error_code(error))
            message: dict[str, Any] = {}
            blocks: dict[int, dict[str, Any]] = {}
            arguments: dict[int, str] = {}
            stopped = False
            with path.with_suffix(".events.jsonl").open("w", encoding="utf-8") as log:
                while True:
                    remaining = self._remaining(deadline)
                    if connection.sock is not None:
                        connection.sock.settimeout(remaining)
                    line = response.readline()
                    if not line:
                        break
                    if not line.startswith(b"data:"):
                        continue
                    event = json.loads(line[5:].replace(destination.key.encode(), b"[redacted]"))
                    log.write(json.dumps(event, ensure_ascii=False) + "\n")
                    log.flush()
                    kind = event.get("type")
                    if (kind == "error" or isinstance(event.get("error"), dict) or
                            (kind is None and isinstance(event.get("code"), str) and isinstance(event.get("message"), str))):
                        error = event.get("error", event)
                        raise ServiceError("Messages API stream returned an error; see response events",
                                           _error_code(error))
                    if kind == "message_start":
                        message = event["message"]
                    elif kind == "content_block_start":
                        blocks[event["index"]] = event["content_block"]
                    elif kind == "content_block_delta":
                        index, delta = event["index"], event["delta"]
                        if delta["type"] == "input_json_delta":
                            arguments[index] = arguments.get(index, "") + delta["partial_json"]
                        else:
                            field = {"text_delta": "text", "thinking_delta": "thinking", "signature_delta": "signature"}.get(delta["type"])
                            if field:
                                blocks[index][field] = blocks[index].get(field, "") + delta[field]
                    elif kind == "message_delta":
                        message.update(event["delta"])
                        message.setdefault("usage", {}).update(event.get("usage", {}))
                    elif kind == "message_stop":
                        stopped = True
                        break
            if not stopped:
                raise ExternalIntelligenceError("Bailian API response stream ended before message_stop")
            for index, text in arguments.items():
                blocks[index]["input"] = json.loads(text)
            message["content"] = [blocks[index] for index in sorted(blocks)]
            _atomic_json(path, message)
            return message
        except (socket.timeout, TimeoutError, ExternalIntelligenceTimeout):
            error = ExternalIntelligenceTimeout("Bailian API request timed out")
            # Incomplete reasoning is recoverable work, never a completed answer.
            error.working_notes = "".join(blocks[index].get("thinking", "") for index in sorted(blocks))
            raise error from None
        except (OSError, http.client.HTTPException, ValueError) as error:
            details = {"error_type": type(error).__name__, "errno": getattr(error, "errno", None),
                       "filename": getattr(error, "filename", None),
                       "trace": [{"file": frame.filename, "line": frame.lineno, "function": frame.name}
                                 for frame in traceback.extract_tb(error.__traceback__)]}
            try:
                _atomic_json(path.with_suffix(".transport-error.json"), details)
            except OSError:
                pass
            raise ExternalIntelligenceError(f"Bailian API transport failed ({type(error).__name__})") from None
        finally:
            connection.close()

    def invoke(self, *, prompt: str, schema: dict[str, Any], model: str, effort: str,
               work_dir: Path, timeout_seconds: float, dynamic_tools: list[dict[str, Any]] | None = None,
               tool_handler: Any = None, base_instructions: str | None = None,
               _route: ServiceRoute | None = None, initial_context: str | None = None) -> tuple[dict, dict, dict]:
        if model.removeprefix(f"{PROVIDER}/").removeprefix("opencode-go/") != MODEL or effort not in {"medium", "xhigh"}:
            raise ExternalIntelligenceError("Bailian API model or reasoning effort is unsupported")
        if (dynamic_tools is None) != (tool_handler is None):
            raise ExternalIntelligenceError("Bailian API tools and handler must be supplied together")
        started = time.monotonic()
        deadline = started + timeout_seconds
        if not self._slots.acquire(timeout=self._remaining(deadline)):
            raise ExternalIntelligenceTimeout("Bailian API concurrency wait timed out")
        with self._lock:
            self._active += 1
            self._maximum = max(self._maximum, self._active)
        try:
            return self._turn(prompt, schema, model, effort, Path(work_dir), deadline,
                              dynamic_tools, tool_handler, base_instructions, _route or self._route.new_scope(), initial_context)
        finally:
            with self._lock:
                self._active -= 1
            self._slots.release()

    def _turn(self, prompt, schema, model, effort, work_dir, deadline, tools, handler, instructions, route, initial_context=None):
        work_dir.mkdir(parents=True, exist_ok=True)
        session = str(uuid.uuid4())
        allowed = {t["name"] for t in tools or []}
        declarations = [{"name": t["name"], "description": t["description"], "input_schema": t["inputSchema"]} for t in tools or []]
        messages = [{"role": "user", "content": [{"type": "text", "text": prompt}]}]
        if initial_context is not None:
            messages[0]["content"].append({"type": "text", "text": initial_context})
        system = (instructions or "Do not use tools.") + " Return only one strict JSON object matching this JSON Schema; do not use Markdown or commentary: " + json.dumps(schema, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
        usage = dict(input_tokens=0, cached_input_tokens=0, output_tokens=0, reasoning_output_tokens=0,
                     cache_write_tokens=0, format_corrections=0)
        step = 0
        requests = 0
        while True:
            self._remaining(deadline)
            step += 1
            outgoing = copy.deepcopy(messages)
            # Keep the stable request prefix cacheable across the tool loop.
            outgoing[-1]["content"][-1]["cache_control"] = {"type": "ephemeral"}
            body = {"model": MODEL, "max_tokens": 128000, "stream": True,
                    "output_config": {"effort": effort},
                    "system": [{"type": "text", "text": system, "cache_control": {"type": "ephemeral"}}],
                    "messages": outgoing}
            if declarations:
                body["tools"] = declarations
            _atomic_json(work_dir / f"request-{step:03d}.json", body)
            service = route.current
            response_path = work_dir / f"response-{step:03d}.json"
            while True:
                self._remaining(deadline)
                with self._lock:
                    self._requests += 1
                requests += 1
                try:
                    options = {} if service == self._route.primary else {"service": service}
                    result = self._post(body, session, deadline, response_path, **options)
                    break
                except ServiceError as error:
                    fallback = route.after_error(service, error)
                    if fallback is None:
                        raise
                    _atomic_json(response_path.with_suffix(".route.json"), {
                        "from": service, "to": fallback, "error_code": error.code,
                    })
                    service = fallback
                    response_path = response_path.with_suffix(".fallback.json")
            if result.get("model") != MODEL:
                raise ExternalIntelligenceError("Bailian API response model changed")
            u = result.get("usage", {})
            for target, source in (("input_tokens", "input_tokens"), ("output_tokens", "output_tokens"),
                                   ("cached_input_tokens", "cache_read_input_tokens"), ("cache_write_tokens", "cache_creation_input_tokens")):
                usage[target] += int(u.get(source, 0))
            content = result.get("content", [])
            messages.append({"role": "assistant", "content": content})
            calls = [block for block in content if block["type"] == "tool_use"]
            if calls:
                if not declarations or any(call["name"] not in allowed for call in calls):
                    if (not usage["format_corrections"] and len(calls) == 1 and not calls[0].get("input")
                            and any(b.get("type") == "text" and b.get("text", "").strip() for b in content)):
                        usage["format_corrections"] = 1
                        declarations = []
                        messages.append({"role":"user","content":[{"type":"tool_result","tool_use_id":calls[0]["id"],"is_error":True,"content":"This tool is not available; no action was executed."}]})
                        messages.append({"role":"user","content":[{"type":"text","text":"Preserve your completed work and return it as the required JSON object. This is delivery-format repair; use the evidence already obtained and do not call tools."}]})
                        continue
                    raise ExternalIntelligenceError("Bailian API requested an unavailable tool")
                replies = []
                for call in calls:
                    self._remaining(deadline)
                    try:
                        definition = next(t["input_schema"] for t in declarations if t["name"] == call["name"])
                        arguments = normalize_tool_arguments(call["input"], definition)
                        if arguments != call["input"]:
                            _atomic_json(work_dir / f"argument-normalization-{step:03d}-{len(replies):02d}.json", {"tool": call["name"], "original": call["input"], "normalized": arguments})
                        value = handler(call["name"], arguments)
                        reply = {"type": "tool_result", "tool_use_id": call["id"], "content": json.dumps(value, ensure_ascii=False)}
                    except Exception as error:
                        reply = {"type": "tool_result", "tool_use_id": call["id"], "content": str(error), "is_error": True}
                    replies.append(reply)
                    _atomic_json(work_dir / f"tools-{step:03d}.json", replies)
                messages.append({"role": "user", "content": replies})
                continue
            text = "".join(block["text"] for block in content if block["type"] == "text")
            try:
                if result.get("stop_reason") != "end_turn":
                    raise ValueError("response did not finish normally")
                encoded = text.strip()
                lines = encoded.splitlines()
                if len(lines) >= 3 and lines[0].lower() in ("```", "```json") and lines[-1] == "```":
                    encoded = "\n".join(lines[1:-1])
                value = json.loads(encoded)
                _validate_schema(value, schema)
                if not isinstance(value, dict):
                    raise ValueError("expected an object")
            except (ValueError, RuntimeError) as error:
                if usage["format_corrections"]:
                    raise ExternalIntelligenceError("Bailian API structured output failed after one correction") from None
                usage["format_corrections"] = 1
                declarations = []
                messages.append({"role": "user", "content": [{"type": "text", "text":
                    f"Your previous result failed JSON Schema validation: {error}. Return the corrected JSON object only. Do not use tools."}]})
                continue
            return value, usage, {"transport": "in-process-go-api", "session_id": session,
                                  "api_requests": requests, "model": MODEL, "reasoning_effort": effort,
                                  "external_intelligence_driver": DRIVER, "external_intelligence_provider": PROVIDER}

    def diagnostics(self) -> dict[str, Any]:
        with self._lock:
            return {"transport": "in-process-go-api", "server_processes": 0, "process_starts": 0,
                    "active_turns": self._active, "max_active": self._maximum, "api_requests": self._requests,
                    "rate_limit_observed": self._rate_limited,
                    "external_intelligence_driver": DRIVER, "external_intelligence_provider": PROVIDER}


class ScopedClient:
    """Share capacity and connections' configuration, never the task's routing state."""
    def __init__(self, client: GoAPIClient, route: ServiceRoute) -> None:
        self._client = client
        self._route = route
        self.identity = client.identity

    def invoke(self, **request: Any) -> Any:
        return self._client.invoke(**request, _route=self._route)

    def new_scope(self) -> ScopedClient:
        return self._client.new_scope()


    def diagnostics(self) -> dict[str, Any]:
        return self._client.diagnostics()


@contextmanager
def open_runtime(*, binary: Path, credential_file: Path, max_active: int, runtime_parent: Path,
                 identity: dict[str, Any], provider: str, models: tuple, reasoning_efforts: tuple) -> Iterator[Any]:
    validate(binary, credential_file)
    client = GoAPIClient(credential_file, max_active, identity)
    try:
        yield client
    finally:
        for service in client._services.values():
            service.key = ""
