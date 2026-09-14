"""In-process Messages client with independent services and optional task-local routing."""
from __future__ import annotations

from contextlib import contextmanager
from dataclasses import dataclass, field
import copy
import hashlib
import http.client
import json
from pathlib import Path
import re
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


def _invalid_fields(value, schema, path=()):
    """Locate disjoint invalid fields without changing their values or constraints."""
    try:
        _validate_schema(value, schema)
        return []
    except ExternalIntelligenceError:
        pass
    if any(key in schema for key in ("anyOf", "oneOf", "allOf")):
        return [(path, schema)]
    shallow = {**schema, "properties": {key: {} for key in schema.get("properties", {})}, "items": {}}
    try:
        _validate_schema(value, shallow)
    except ExternalIntelligenceError:
        return [(path, schema)]
    if isinstance(value, dict):
        children = [(key, child, schema["properties"][key]) for key, child in value.items()
                    if key in schema.get("properties", {})]
    elif isinstance(value, list) and isinstance(schema.get("items"), dict):
        children = [(index, child, schema["items"]) for index, child in enumerate(value)]
    else:
        return [(path, schema)]
    return [error for key, child, rule in children for error in _invalid_fields(child, rule, (*path, key))]


def _field_pointer(path):
    return "/" + "/".join(str(key).replace("~", "~0").replace("/", "~1") for key in path)


def _normalize_integer_collections(value, schema):
    """Unwrap a single position/range only when the complete schema admits it."""
    if isinstance(value, list) and len(value) == 1 and (
            type(value[0]) is int or (isinstance(value[0], list) and len(value[0]) == 2
                                    and all(type(x) is int for x in value[0]))) and any(k in schema for k in ("anyOf", "oneOf")):
        try:
            _validate_schema(value, schema)
        except ExternalIntelligenceError:
            try:
                _validate_schema(value[0], schema)
                return value[0]
            except ExternalIntelligenceError:
                pass
    if schema.get("type") == "array" and type(value) is int:
        try:
            _validate_schema([value], schema)
            return [value]
        except ExternalIntelligenceError:
            return value
    if isinstance(value, dict) and schema.get("type") == "object":
        properties = schema.get("properties", {})
        value = {key: _normalize_integer_collections(child, properties.get(key, {}))
                 for key, child in value.items()}
    elif isinstance(value, list) and schema.get("type") == "array":
        value = [_normalize_integer_collections(child, schema.get("items", {})) for child in value]
    if isinstance(value, dict) and any(k in schema for k in ("anyOf", "oneOf")):
        try:
            _validate_schema(value, schema)
            return value
        except ExternalIntelligenceError:
            pass
        choices = {}
        for branch in schema.get("anyOf", schema.get("oneOf", [])):
            if branch.get("type") != "object":
                continue
            candidate = _normalize_integer_collections(value, branch)
            try:
                _validate_schema(candidate, schema)
                choices[json.dumps(candidate, sort_keys=True)] = candidate
            except ExternalIntelligenceError:
                pass
        if len(choices) == 1:
            return next(iter(choices.values()))
    return value


def _unwrap_field_corrections(value, schema, *, original=None):
    """Accept unique requested pointers or those exact paths in a response tree."""
    matches = {}
    required = set(schema.get("required", []))
    def accept(candidate):
        candidate = _normalize_integer_collections(candidate, schema)
        try:
            _validate_schema(candidate, schema)
        except ExternalIntelligenceError:
            return
        fingerprint = json.dumps(candidate, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
        matches[fingerprint] = candidate

    def explicit_maps(node):
        if isinstance(node, dict):
            # Explicit pointers retain their absolute meaning under a wrapper.
            # Unrequested pointer edits are not silently accepted.
            pointer_keys = {key for key in node if key.startswith("/")}
            if required and pointer_keys == required:
                accept({key: node[key] for key in required})
            for child in node.values():
                explicit_maps(child)
        elif isinstance(node, list):
            for child in node:
                explicit_maps(child)
    explicit_maps(value)

    # A correction map may omit exactly the leading slash of every requested
    # JSON Pointer. Match the complete root key set; do not interpret prose,
    # unrequested edits, mixed spellings, or arbitrary nested property names.
    if (required and all(key.startswith("/") for key in required)
            and any("/" in key[1:] for key in required) and isinstance(value, dict)):
        bare_keys = {key[1:]: key for key in required}
        if set(value).intersection(bare_keys) and set(value).intersection(required):
            return None
        if len(bare_keys) == len(required) and set(value) == set(bare_keys):
            accept({pointer: value[bare] for bare, pointer in bare_keys.items()})

    # Ordinary property names are resolved only at the response root, never
    # by looking for an arbitrary same-named leaf in a nested object.
    if original is not None and isinstance(value, (dict, list)):
        projected = {}
        try:
            for pointer in required:
                if not pointer.startswith("/"):
                    raise ValueError("not a JSON Pointer")
                node, template = value, original
                for encoded in pointer[1:].split("/"):
                    token = encoded.replace("~1", "/").replace("~0", "~")
                    if isinstance(template, list):
                        index = int(token)
                        if not isinstance(node, list) or str(index) != token or index < 0:
                            raise ValueError("array index requires an array")
                        node, template = node[index], template[index]
                    elif isinstance(template, dict) and isinstance(node, dict):
                        node, template = node[token], template[token]
                    else:
                        raise ValueError("correction container changed")
                projected[pointer] = node
            if required:
                accept(projected)
        except (ValueError, TypeError, KeyError, IndexError):
            pass
    if len(matches) > 1:
        # A raw object may itself pass the patch schema despite conflicting
        # wrapped interpretations. An invalid sentinel prevents both delivery
        # and partial-output retention from accepting an arbitrary candidate.
        return None
    return next(iter(matches.values())) if matches else value


def _unique_json_fields(pairs):
    value = {}
    for key, child in pairs:
        if key in value:
            raise ValueError("duplicate JSON field")
        value[key] = child
    return value


def _partial_array_output(text, schema, *, allow_complete=False):
    """Retain batch records for per-source validation; missing records stay invalid."""
    properties = schema.get("properties", {})
    if schema.get("type") != "object" or len(properties) != 1:
        return None
    name, rule = next(iter(properties.items()))
    count = rule.get("minItems")
    if (rule.get("type") != "array" or type(count) is not int or count <= 0
            or count != rule.get("maxItems") or schema.get("required") != [name]):
        return None
    decoder = json.JSONDecoder(object_pairs_hook=_unique_json_fields)
    text = _json_response_text(text, allow_incomplete=allow_complete)
    remaining = text.lstrip()
    if not remaining.startswith("{"):
        return None
    remaining = remaining[1:].lstrip()
    try:
        key, end = decoder.raw_decode(remaining)
    except ValueError:
        return None
    remaining = remaining[end:].lstrip()
    if key != name or not remaining.startswith(":"):
        return None
    remaining = remaining[1:].lstrip()
    if not remaining.startswith("["):
        return None
    remaining, rows = remaining[1:].lstrip(), []
    while remaining and len(rows) < count:
        try:
            row, end = decoder.raw_decode(remaining)
        except ValueError:
            break
        rows.append(row)
        remaining = remaining[end:].lstrip()
        if len(rows) == count:
            break
        if not remaining.startswith(","):
            break
        remaining = remaining[1:].lstrip()
    if not rows:
        return None
    if len(rows) == count or remaining.startswith("]"):
        if len(rows) == count and not allow_complete:
            return None
        try:
            if remaining.strip() in ("", "]"):
                # Every retained record has already closed independently.
                # Only the outer array/object delimiters may be absent.
                value = {name: rows}
            else:
                try:
                    value = decoder.decode(text)
                except ValueError:
                    value = _closed_json_prefix(text, decoder=decoder)
            if not isinstance(value, dict) or value.get(name) != rows:
                return None
            # Validate the complete envelope without discarding good sources
            # because another source needs the existing per-source repair.
            _validate_schema(value, {**schema, "properties": {name: {**rule, "minItems": 0, "items": {}}}})
        except (ValueError, ExternalIntelligenceError):
            return None
    return {name: rows + [None] * (count - len(rows))}


def _json_response_text(text, *, allow_incomplete=False):
    encoded = text.strip()
    opening = re.match(r"```(?:json)?(?:\r\n|\r|\n)", encoded, re.IGNORECASE | re.ASCII)
    if opening is None:
        return encoded
    body = encoded[opening.end():]
    # Remove only boundary markers. Unicode separators within JSON strings
    # are content, not line endings; preserve all body characters verbatim.
    closing = re.search(r"(?:\r\n|\r|\n)(`{1,3})\Z", body)
    if closing is not None and (closing[1] == "```" or allow_incomplete):
        return body[:closing.start()]
    return body if allow_incomplete else encoded


def _retained_field_corrections(text, original, fields, schema):
    encoded = _json_response_text(text, allow_incomplete=True)
    decoder = json.JSONDecoder(object_pairs_hook=_unique_json_fields)
    try:
        value = decoder.decode(encoded)
    except ValueError:
        value = _closed_json_prefix(encoded, decoder=decoder)
    if value is None:
        # A cut correction map may still contain independently complete fields.
        # Numeric tokens need a delimiter: a received '12' could still be '123'.
        if not encoded.startswith("{"):
            return None
        remaining, value = encoded[1:].lstrip(), {}
        while remaining:
            try:
                key, end = decoder.raw_decode(remaining)
                if not isinstance(key, str) or key not in schema.get("properties", {}) or key in value:
                    return None
                remaining = remaining[end:].lstrip()
                if not remaining.startswith(":"):
                    break
                candidate, end = decoder.raw_decode(remaining[1:].lstrip())
                remaining = remaining[1:].lstrip()[end:].lstrip()
                if remaining and remaining[0] not in ",}":
                    break
                if not remaining and not isinstance(candidate, (dict, list, str)):
                    break
                value[key] = candidate
                if remaining.startswith(","):
                    remaining = remaining[1:].lstrip()
                elif remaining in ("", "}"):
                    break
                else:
                    return None
            except ValueError:
                break
        if not value:
            return None
    corrections = _unwrap_field_corrections(value, schema, original=original)
    if not isinstance(corrections, dict) or not set(corrections).issubset(schema.get("properties", {})):
        return None
    retained = copy.deepcopy(original)
    for path, rule in fields:
        pointer = _field_pointer(path)
        if pointer not in corrections:
            continue
        try:
            _validate_schema(corrections[pointer], rule)
        except ExternalIntelligenceError:
            continue
        parent = retained
        for key in path[:-1]:
            parent = parent[key]
        parent[path[-1]] = corrections[pointer]
    return retained


def _complete_json_containers(text):
    """Close only unfinished containers after a complete value; never add data."""
    encoded = text.strip()
    if not encoded.startswith("{") or encoded[-1:] in ("{", "[", ":", ","):
        return None
    stack, quoted, escaped = [], False, False
    for char in encoded:
        if quoted:
            if escaped:
                escaped = False
            elif char == "\\":
                escaped = True
            elif char == '"':
                quoted = False
        elif char == '"':
            quoted = True
        elif char in "{[":
            stack.append("}" if char == "{" else "]")
        elif char in "}]":
            if not stack or stack.pop() != char:
                return None
    if quoted or not stack:
        return None
    try:
        value = json.loads(encoded + "".join(reversed(stack)))
    except ValueError:
        return None
    return value if isinstance(value, dict) else None


def _closed_json_prefix(text, *, decoder=None):
    """Recover a complete object followed only by surplus closing delimiters."""
    try:
        value, end = (decoder or json.JSONDecoder()).raw_decode(text)
    except ValueError:
        return None
    trailing = text[end:].strip()
    if isinstance(value, dict) and trailing and set(trailing).issubset({'}', ']', ' ', '\r', '\n', '\t'}):
        return value
    return None


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


@dataclass
class _UnsentRequestContinuation:
    owner: Any
    route: Any
    identity: str
    state: dict[str, Any]
    consumed: bool = False


def _request_identity(prompt, schema, model, effort, instructions):
    return hashlib.sha256(json.dumps([prompt, schema, model, effort, instructions], ensure_ascii=False,
        sort_keys=True, separators=(",", ":")).encode("utf-8")).hexdigest()


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
        message: dict[str, Any] = {}
        request_started = False

        def interrupted(error):
            error.request_not_sent = not request_started
            error.partial_message = {**message, "content": [blocks[index] for index in sorted(blocks)]}
            return error

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
            request_started = True
            connection.request("POST", destination.endpoint, json.dumps(body, ensure_ascii=False, separators=(",", ":")).encode("utf-8"), headers)
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
                    try:
                        event = json.loads(line[5:].replace(destination.key.encode(), b"[redacted]"))
                    except ValueError:
                        if not line.endswith(b"\n"):
                            # readline returned an EOF fragment. Discard only
                            # this event, keeping earlier fully decoded events.
                            raise interrupted(ExternalIntelligenceError(
                                "Bailian API response stream ended inside an event")) from None
                        raise
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
                raise interrupted(ExternalIntelligenceError("Bailian API response stream ended before message_stop"))
            invalid_arguments = {}
            for index, text in arguments.items():
                try:
                    blocks[index]["input"] = json.loads(text)
                except json.JSONDecodeError:
                    # An unusable invocation is a model-format failure, not a
                    # broken transport. Keep raw bytes in the response record;
                    # an empty placeholder is never dispatched to a handler.
                    invalid_arguments[blocks[index]["id"]] = text
                    blocks[index]["input"] = {}
            message["content"] = [blocks[index] for index in sorted(blocks)]
            if invalid_arguments:
                message["invalid_tool_arguments"] = invalid_arguments
            _atomic_json(path, message)
            return message
        except (socket.timeout, TimeoutError, ExternalIntelligenceTimeout):
            error = interrupted(ExternalIntelligenceTimeout("Bailian API request timed out"))
            # Incomplete reasoning is recoverable work, never a completed answer.
            error.working_notes = "".join(blocks[index].get("thinking", "") for index in sorted(blocks))
            raise error from None
        except (OSError, http.client.HTTPException, ValueError) as error:
            details = {"request_started": request_started, "error_type": type(error).__name__, "errno": getattr(error, "errno", None),
                       "filename": getattr(error, "filename", None),
                       "trace": [{"file": frame.filename, "line": frame.lineno, "function": frame.name}
                                 for frame in traceback.extract_tb(error.__traceback__)]}
            try:
                _atomic_json(path.with_suffix(".transport-error.json"), details)
            except OSError:
                pass
            failure = ExternalIntelligenceError(f"Bailian API transport failed ({type(error).__name__})")
            failure.request_not_sent = not request_started
            if isinstance(error, (OSError, http.client.HTTPException)):
                interrupted(failure)
            raise failure from None
        finally:
            connection.close()

    def invoke(self, *, prompt: str, schema: dict[str, Any], model: str, effort: str,
               work_dir: Path, timeout_seconds: float, dynamic_tools: list[dict[str, Any]] | None = None,
               tool_handler: Any = None, base_instructions: str | None = None,
               _route: ServiceRoute | None = None, initial_context: str | None = None,
               _continuation: Any = None) -> tuple[dict, dict, dict]:
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
                              dynamic_tools, tool_handler, base_instructions, _route or (_continuation.route if isinstance(_continuation, _UnsentRequestContinuation) else self._route.new_scope()), initial_context, _continuation)
        except ExternalIntelligenceError as error:
            retained_path = Path(work_dir) / "retained-output.json"
            if not hasattr(error, "partial_output") and retained_path.is_file():
                retained = json.loads(retained_path.read_text(encoding="utf-8"))
                error.partial_output = retained["output"]
                error.partial_usage = retained["usage"]
            raise
        finally:
            with self._lock:
                self._active -= 1
            self._slots.release()

    def _turn(self, prompt, schema, model, effort, work_dir, deadline, tools, handler, instructions, route, initial_context=None, continuation=None):
        work_dir.mkdir(parents=True, exist_ok=True)
        session = str(uuid.uuid4())
        catalog = list(tools or [])
        allowed = {t["name"] for t in catalog}
        declarations = [{"name": t["name"], "description": t["description"], "input_schema": t["inputSchema"]} for t in tools or []]
        messages = [{"role": "user", "content": [{"type": "text", "text": prompt}]}]
        if initial_context is not None:
            messages[0]["content"].append({"type": "text", "text": initial_context})
        system = (instructions or "Do not use tools.") + " Return only one strict JSON object matching this JSON Schema; do not use Markdown or commentary: " + json.dumps(schema, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
        usage = dict(input_tokens=0, cached_input_tokens=0, output_tokens=0, reasoning_output_tokens=0,
                     cache_write_tokens=0, format_corrections=0)
        step = 0
        requests = 0
        repair_fields = []
        original_value = None
        response_schema = schema
        request_identity = _request_identity(prompt, schema, model, effort, instructions)
        if continuation is not None:
            if (not isinstance(continuation, _UnsentRequestContinuation) or continuation.owner is not self
                    or continuation.route is not route or continuation.identity != request_identity or continuation.consumed):
                raise ExternalIntelligenceError("unsent-request continuation identity changed")
            continuation.consumed = True
            saved = copy.deepcopy(continuation.state)
            session, catalog, allowed = saved["session"], saved["catalog"], set(saved["allowed"])
            declarations, messages, system = saved["declarations"], saved["messages"], saved["system"]
            usage, step, requests = saved["usage"], saved["step"] - 1, saved["requests"]
            repair_fields, original_value = saved["repair_fields"], saved["original_value"]
            response_schema = saved["response_schema"]
            _atomic_json(work_dir / "resumed-unsent-request.json", {
                "request_identity": request_identity, "session_id": session,
                "next_request_step": step + 1, "prior_api_attempts": requests,
                "same_source_observations_and_budget": True})
        while True:
            self._remaining(deadline)
            step += 1
            # The host may withdraw exhausted capabilities in the same list.
            # Only narrow declarations; delivery repair must not re-enable tools.
            available = {t["name"] for t in tools or []}
            declarations = [t for t in declarations if t["name"] in available]
            outgoing = copy.deepcopy(messages)
            if catalog and not declarations and not usage["format_corrections"]:
                outgoing.append({"role":"user","content":[{"type":"text","text":
                    "Host execution state: retrieval capacity is exhausted; no further tool call can execute. "
                    "This is the actual host state, not an estimate from the conversation. "
                    "Continue the original task using the observations already obtained and return the required result."}]})
            # Keep the stable request prefix cacheable across the tool loop.
            outgoing[-1]["content"][-1]["cache_control"] = {"type": "ephemeral"}
            body = {"model": MODEL, "max_tokens": 128000, "stream": True,
                    "output_config": {"effort": effort},
                    "system": [{"type": "text", "text": system, "cache_control": {"type": "ephemeral"}}],
                    "messages": outgoing}
            if declarations:
                body["tools"] = declarations
            elif catalog:
                body["tool_choice"] = {"type":"none"}
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
                except ExternalIntelligenceError as error:
                    partial = getattr(error, "partial_message", None)
                    if (not catalog and isinstance(partial, dict)
                            and partial.get("model") == MODEL
                            and not any(block.get("type") == "tool_use" for block in partial.get("content", []))):
                        partial_text = "".join(block.get("text", "") for block in partial.get("content", [])
                                               if block.get("type") == "text")
                        retained = (_retained_field_corrections(partial_text, original_value, repair_fields, response_schema)
                                    if repair_fields else _partial_array_output(partial_text, response_schema, allow_complete=True))
                        if retained is not None:
                            retained = _normalize_integer_collections(retained, schema)
                            known_usage = dict(usage)
                            for target, source in (("input_tokens", "input_tokens"), ("output_tokens", "output_tokens"),
                                    ("cached_input_tokens", "cache_read_input_tokens"), ("cache_write_tokens", "cache_creation_input_tokens")):
                                known_usage[target] += int(partial.get("usage", {}).get(source, 0))
                            error.partial_output = retained
                            error.partial_usage = {**known_usage, "api_requests": requests, "calls": 1}
                            _atomic_json(work_dir / "interrupted-source-prefix.json", {
                                "output": retained, "known_usage_lower_bound": error.partial_usage,
                                "output_usage_complete": False, "unreported_output_tokens": None,
                                "completion": "interrupted; only complete rows may enter existing per-source validation"})
                    if getattr(error, "request_not_sent", False) is True:
                        state = copy.deepcopy({"session": session, "catalog": catalog, "allowed": sorted(allowed),
                            "declarations": declarations, "messages": messages, "system": system, "usage": usage,
                            "step": step, "requests": requests, "repair_fields": repair_fields,
                            "original_value": original_value, "response_schema": response_schema})
                        error.request_continuation = _UnsentRequestContinuation(self, route, request_identity, state)
                        # Diagnostic snapshot only: disk data is never accepted as a live continuation.
                        _atomic_json(work_dir / "unsent-request-continuation.json", {
                            "request_identity": request_identity, "request_step": step,
                            "state": state, "requires_same_live_scope": True})
                    raise
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
                if catalog and not declarations and not usage["format_corrections"]:
                    usage["format_corrections"] = 1
                    messages.append({"role":"user","content":[{"type":"tool_result", "tool_use_id":call["id"],
                        "is_error":True,"content":"Host retrieval capacity is exhausted; no action was executed."} for call in calls]})
                    messages.append({"role":"user","content":[{"type":"text","text":
                        "Preserve your completed work and return the required JSON result from the evidence already obtained. "
                        "No tool can execute; this is the single delivery-format correction, not a new retrieval attempt."}]})
                    continue
                if not declarations or any(call["name"] not in allowed for call in calls):
                    if (not usage["format_corrections"] and len(calls) == 1 and not calls[0].get("input")
                            and any(b.get("type") == "text" and b.get("text", "").strip() for b in content)):
                        usage["format_corrections"] = 1
                        declarations = []
                        messages.append({"role":"user","content":[{"type":"tool_result","tool_use_id":calls[0]["id"],"is_error":True,"content":"This tool is not available; no action was executed."}]})
                        messages.append({"role":"user","content":[{"type":"text","text":"Preserve your completed work and return it as the required JSON object. This is delivery-format repair; use the evidence already obtained and do not call tools."}]})
                        continue
                    raise ExternalIntelligenceError("Bailian API requested an unavailable tool")
                invalid_arguments = result.get("invalid_tool_arguments", {})
                if invalid_arguments:
                    if usage["format_corrections"]:
                        raise ExternalIntelligenceError("Bailian API tool arguments failed after one correction")
                    usage["format_corrections"] = 1
                replies = []
                for call in calls:
                    self._remaining(deadline)
                    try:
                        if call["id"] in invalid_arguments:
                            raise ValueError("Tool arguments were not valid JSON; no action was executed for this call. "
                                "Preserve evidence already obtained and resubmit valid arguments if this action is still needed. "
                                "Malformed argument data: " + invalid_arguments[call["id"]])
                        definition = next(t["inputSchema"] for t in catalog if t["name"] == call["name"])
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
            value = None
            try:
                if result.get("stop_reason") != "end_turn":
                    raise ValueError("response did not finish normally")
                encoded = _json_response_text(text)
                try:
                    value = json.loads(encoded)
                except json.JSONDecodeError:
                    value = _closed_json_prefix(encoded)
                    if value is None:
                        value = _complete_json_containers(encoded)
                        if value is not None:
                            _atomic_json(work_dir / f"container-closure-{step:03d}.json", {
                                "rule": "append-missing-container-closers-only", "output": value})
                    if value is None:
                        value = _partial_array_output(encoded, response_schema)
                    if value is None:
                        raise
                    _atomic_json(work_dir / "partial-output.json", value)
                normalized = _normalize_integer_collections(value, response_schema)
                if repair_fields:
                    normalized = _unwrap_field_corrections(normalized, response_schema, original=original_value)
                if normalized != value:
                    _atomic_json(work_dir / f"normalized-output-{step:03d}.json", {
                        "original": value, "normalized": normalized})
                    value = normalized
                _validate_schema(value, response_schema)
                if repair_fields:
                    corrections = value
                    value = copy.deepcopy(original_value)
                    for path, _ in repair_fields:
                        parent = value
                        for key in path[:-1]:
                            parent = parent[key]
                        parent[path[-1]] = corrections[_field_pointer(path)]
                    _validate_schema(value, schema)
                    _atomic_json(work_dir / "format-repair.json", {"corrections": corrections})
                if not isinstance(value, dict):
                    raise ValueError("expected an object")
            except (ValueError, RuntimeError) as error:
                if usage["format_corrections"]:
                    retained = original_value if repair_fields else value
                    if (repair_fields and isinstance(value, dict)
                            and set(value).issubset(response_schema.get("properties", {}))):
                        retained = copy.deepcopy(original_value)
                        for path, rule in repair_fields:
                            pointer = _field_pointer(path)
                            if pointer not in value:
                                continue
                            try:
                                _validate_schema(value[pointer], rule)
                            except ExternalIntelligenceError:
                                continue
                            parent = retained
                            for key in path[:-1]:
                                parent = parent[key]
                            parent[path[-1]] = value[pointer]
                    failure = ExternalIntelligenceError("Bailian API structured output failed after one correction")
                    failure.partial_output = retained
                    failure.partial_usage = {**usage, "api_requests": requests}
                    raise failure from None
                usage["format_corrections"] = 1
                declarations = []
                if isinstance(value, dict):
                    repair_fields = _invalid_fields(value, schema)
                if repair_fields and all(path for path, _ in repair_fields):
                    original_value = value
                    _atomic_json(work_dir / "retained-output.json", {
                        "output": original_value, "usage": {**usage, "api_requests": requests}})
                    properties = {_field_pointer(path): rule for path, rule in repair_fields}
                    response_schema = {"type": "object", "additionalProperties": False,
                                       "required": list(properties), "properties": properties}
                    system = (instructions or "Do not use tools.") + " Return only the requested JSON field corrections matching this JSON Schema: " + json.dumps(response_schema, ensure_ascii=False, separators=(",", ":"))
                    messages.append({"role": "user", "content": [{"type": "text", "text":
                        "Correct only the invalid fields listed in the response schema. Each key is a JSON Pointer into your previous result. "
                        "Return the replacement value under that exact key. Preserve its supported meaning and references; "
                        "all other fields are retained unchanged. Do not use tools or repeat the whole result."}]})
                    continue
                repair_fields = []
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
