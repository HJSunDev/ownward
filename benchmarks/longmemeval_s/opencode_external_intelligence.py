from __future__ import annotations

from contextlib import contextmanager
import base64
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import queue
import re
import secrets
import shutil
import socket
import subprocess
import sys
import threading
import time
from typing import Any, Callable, Iterator
from urllib import error, parse, request


DRIVER = "opencode-server/v1"
BENCHMARK_ROOT = Path(__file__).resolve().parent
BRIDGE_PATH = BENCHMARK_ROOT / "opencode_mcp_bridge.py"


class OpenCodeError(RuntimeError):
    pass


class OpenCodeTimeout(OpenCodeError):
    pass


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def implementation_sha256() -> str:
    digest = hashlib.sha256()
    for path in identity_files():
        digest.update(path.read_bytes())
    return digest.hexdigest()


def identity_files() -> tuple[Path, ...]:
    return (Path(__file__), BRIDGE_PATH)


def artifact_sha256(binary: Path) -> str:
    return _sha256(resolve_native_binary(binary))


def resolve_native_binary(binary: Path) -> Path:
    binary = binary.resolve()
    if binary.suffix.lower() not in {".ps1", ".cmd"}:
        return binary
    candidate = binary.parent / "node_modules" / "opencode-ai" / "bin" / ("opencode.exe" if os.name == "nt" else "opencode")
    if not candidate.is_file():
        raise OpenCodeError("OpenCode launcher does not resolve to its native executable")
    return candidate.resolve()


def validate(binary: Path, credential_file: Path) -> None:
    native = resolve_native_binary(binary)
    if not native.is_file() or not credential_file.resolve().is_file() or not BRIDGE_PATH.is_file():
        raise OpenCodeError("OpenCode runtime artifacts are incomplete")


def probe(binary: Path, credential_file: Path) -> dict[str, str]:
    validate(binary, credential_file)
    native = resolve_native_binary(binary)
    completed = subprocess.run(
        [str(native), "--version"], capture_output=True, text=True, encoding="utf-8", errors="replace",
        timeout=30, check=False,
    )
    if completed.returncode != 0 or not completed.stdout.strip():
        raise OpenCodeError("OpenCode runtime cannot start")
    return {"version": completed.stdout.strip(), "artifact_sha256": _sha256(native)}


def _atomic_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.{os.getpid()}.{time.time_ns()}.tmp")
    temporary.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    temporary.replace(path)


def _available_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
        listener.bind(("127.0.0.1", 0))
        return int(listener.getsockname()[1])


def _validate_schema(value: Any, schema: dict[str, Any], path: str = "$") -> None:
    expected = schema.get("type")
    matches = {
        "object": isinstance(value, dict),
        "array": isinstance(value, list),
        "string": isinstance(value, str),
        "integer": isinstance(value, int) and not isinstance(value, bool),
        "number": isinstance(value, (int, float)) and not isinstance(value, bool),
        "boolean": isinstance(value, bool),
        "null": value is None,
    }
    expected_types = [expected] if isinstance(expected, str) else expected if isinstance(expected, list) else []
    if expected_types and not any(matches.get(item, False) for item in expected_types):
        raise OpenCodeError(f"OpenCode structured output violates schema at {path}: expected {expected}")
    for name in ("allOf",):
        clauses = schema.get(name)
        if isinstance(clauses, list):
            for clause in clauses:
                if isinstance(clause, dict):
                    _validate_schema(value, clause, path)
    for name, exact in (("anyOf", False), ("oneOf", True)):
        clauses = schema.get(name)
        if isinstance(clauses, list):
            matches_count = 0
            for clause in clauses:
                try:
                    if isinstance(clause, dict):
                        _validate_schema(value, clause, path)
                    else:
                        continue
                except OpenCodeError:
                    continue
                matches_count += 1
            if matches_count == 0 or (exact and matches_count != 1):
                raise OpenCodeError(f"OpenCode structured output violates {name} at {path}")
    if "enum" in schema and value not in schema["enum"]:
        raise OpenCodeError(f"OpenCode structured output violates enum at {path}")
    if "const" in schema and value != schema["const"]:
        raise OpenCodeError(f"OpenCode structured output violates const at {path}")
    if isinstance(value, dict):
        properties = schema.get("properties") if isinstance(schema.get("properties"), dict) else {}
        missing = [name for name in schema.get("required", []) if name not in value]
        if missing:
            raise OpenCodeError(f"OpenCode structured output is missing {path}.{missing[0]}")
        if schema.get("additionalProperties") is False:
            extras = sorted(set(value) - set(properties))
            if extras:
                raise OpenCodeError(f"OpenCode structured output has an extra field at {path}.{extras[0]}")
        for name, child in value.items():
            if name in properties and isinstance(properties[name], dict):
                _validate_schema(child, properties[name], f"{path}.{name}")
    if isinstance(value, list):
        if isinstance(schema.get("minItems"), int) and len(value) < schema["minItems"]:
            raise OpenCodeError(f"OpenCode structured output has too few items at {path}")
        if isinstance(schema.get("maxItems"), int) and len(value) > schema["maxItems"]:
            raise OpenCodeError(f"OpenCode structured output has too many items at {path}")
        child_schema = schema.get("items")
        if isinstance(child_schema, dict):
            for index, child in enumerate(value):
                _validate_schema(child, child_schema, f"{path}[{index}]")
        if schema.get("uniqueItems") is True:
            serialized = [json.dumps(item, ensure_ascii=False, sort_keys=True, separators=(",", ":")) for item in value]
            if len(serialized) != len(set(serialized)):
                raise OpenCodeError(f"OpenCode structured output has duplicate items at {path}")
    if isinstance(value, str):
        if isinstance(schema.get("minLength"), int) and len(value) < schema["minLength"]:
            raise OpenCodeError(f"OpenCode structured output string is too short at {path}")
        if isinstance(schema.get("maxLength"), int) and len(value) > schema["maxLength"]:
            raise OpenCodeError(f"OpenCode structured output string is too long at {path}")
        pattern = schema.get("pattern")
        if isinstance(pattern, str) and re.search(pattern, value) is None:
            raise OpenCodeError(f"OpenCode structured output violates pattern at {path}")
    if isinstance(value, (int, float)) and not isinstance(value, bool):
        if isinstance(schema.get("minimum"), (int, float)) and value < schema["minimum"]:
            raise OpenCodeError(f"OpenCode structured output is below minimum at {path}")
        if isinstance(schema.get("maximum"), (int, float)) and value > schema["maximum"]:
            raise OpenCodeError(f"OpenCode structured output is above maximum at {path}")


class _CallbackServer:
    def __init__(self, token: str) -> None:
        self.token = token
        self._handler: Callable[[str, Any], Any] | None = None
        self._lock = threading.Lock()
        owner = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self) -> None:  # noqa: N802
                if self.path != "/call" or self.headers.get("Authorization") != f"Bearer {owner.token}":
                    self.send_error(403)
                    return
                try:
                    size = int(self.headers.get("Content-Length", "0"))
                    if size < 1 or size > 4 * 1024 * 1024:
                        raise ValueError("private tool request size is invalid")
                    value = json.loads(self.rfile.read(size).decode("utf-8"))
                    with owner._lock:
                        callback = owner._handler
                    if callback is None:
                        raise RuntimeError("no external-intelligence turn owns the tool bridge")
                    result = {"ok": True, "value": callback(str(value.get("name", "")), value.get("arguments")), "error": ""}
                except Exception as caught:
                    result = {"ok": False, "value": None, "error": f"Tool failed: {caught}"}
                body = json.dumps(result, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, _format: str, *_args: object) -> None:
                return

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, name="opencode-private-tool-callback", daemon=True)
        self.thread.start()

    @property
    def url(self) -> str:
        return f"http://127.0.0.1:{self.server.server_port}/call"

    def bind(self, handler: Callable[[str, Any], Any] | None) -> None:
        with self._lock:
            if handler is not None and self._handler is not None:
                raise OpenCodeError("OpenCode worker already owns an active tool turn")
            self._handler = handler

    def close(self) -> None:
        self.bind(None)
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)


class OpenCodeServer:
    """One isolated OpenCode server; each request uses a fresh, non-shared session."""

    def __init__(
        self, binary: Path, credential_file: Path, runtime_root: Path, *,
        provider: str, models: tuple[str, ...], reasoning_efforts: tuple[str, ...],
    ) -> None:
        if not provider or not models or not reasoning_efforts:
            raise OpenCodeError("OpenCode provider capability declaration is incomplete")
        self.binary = resolve_native_binary(binary)
        self.credential_file = credential_file.resolve()
        self.runtime_root = runtime_root.resolve()
        self.provider = provider
        self.models = frozenset(models)
        self.reasoning_efforts = frozenset(reasoning_efforts)
        self.process: subprocess.Popen[str] | None = None
        self.started_at = 0.0
        self.instance_id = self.runtime_root.name
        self._stderr: list[str] = []
        self._callback: _CallbackServer | None = None
        self._tool_identity: str | None = None
        self._tool_ids: dict[str, str] = {}
        self._request_lock = threading.Lock()
        self._rate_limit_observed = False
        self._username = "ownward"
        self._password = secrets.token_urlsafe(32)

    def _normalize_model(self, value: str) -> str:
        model = value.removeprefix(f"{self.provider}/")
        if model not in self.models:
            raise OpenCodeError(f"OpenCode model is not declared for provider {self.provider}: {value}")
        return model

    def __enter__(self) -> "OpenCodeServer":
        self.runtime_root.mkdir(parents=True, exist_ok=False)
        try:
            data_root = self.runtime_root / "data"
            auth = data_root / "opencode" / "auth.json"
            auth.parent.mkdir(parents=True)
            shutil.copyfile(self.credential_file, auth)
            config = self.runtime_root / "opencode.json"
            _atomic_json(config, {
                "$schema": "https://opencode.ai/config.json",
                "autoupdate": False,
                "share": "disabled",
                "snapshot": False,
                "permission": {"*": "deny"},
            })
            environment = dict(os.environ)
            environment.update({
                "XDG_DATA_HOME": str(data_root),
                "OPENCODE_CONFIG": str(config),
                "OPENCODE_CONFIG_DIR": str(self.runtime_root / "config"),
                "OPENCODE_SERVER_USERNAME": self._username,
                "OPENCODE_SERVER_PASSWORD": self._password,
                "NO_PROXY": "127.0.0.1,localhost",
            })
            port = _available_port()
            self._base_url = f"http://127.0.0.1:{port}"
            self.started_at = time.perf_counter()
            self.process = subprocess.Popen(
                [str(self.binary), "serve", "--pure", "--hostname", "127.0.0.1", "--port", str(port)],
                cwd=self.runtime_root, env=environment, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                text=True, encoding="utf-8", errors="replace",
            )
            threading.Thread(target=self._drain, args=(self.process.stdout, False), daemon=True).start()
            threading.Thread(target=self._drain, args=(self.process.stderr, True), daemon=True).start()
            deadline = time.perf_counter() + 30
            while True:
                if self.process.poll() is not None:
                    raise OpenCodeError("OpenCode server stopped during startup")
                try:
                    health = self._http("GET", "/global/health", timeout=2)
                    if isinstance(health, dict) and health.get("healthy") is True:
                        return self
                except (OpenCodeError, OpenCodeTimeout):
                    pass
                if time.perf_counter() >= deadline:
                    raise OpenCodeTimeout("OpenCode server startup timed out")
                time.sleep(0.05)
        except BaseException:
            self.__exit__()
            raise

    def _drain(self, stream: Any, capture: bool) -> None:
        if stream is None:
            return
        for line in stream:
            if capture:
                self._stderr.append(line)
                if len(self._stderr) > 200:
                    del self._stderr[:100]

    def _http(self, method: str, path: str, body: Any = None, *, timeout: float = 30) -> Any:
        payload = None if body is None else json.dumps(body, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
        token = base64.b64encode(f"{self._username}:{self._password}".encode("utf-8")).decode("ascii")
        message = request.Request(
            self._base_url + path, data=payload, method=method,
            headers={"Authorization": f"Basic {token}", "Content-Type": "application/json"},
        )
        try:
            with request.build_opener(request.ProxyHandler({})).open(message, timeout=timeout) as response:
                content = response.read()
        except error.HTTPError as caught:
            detail = caught.read().decode("utf-8", errors="replace")
            if caught.code == 429:
                self._rate_limit_observed = True
            raise OpenCodeError(f"OpenCode HTTP {caught.code}: {detail[:1000]}") from caught
        except TimeoutError as caught:
            raise OpenCodeTimeout(f"OpenCode request timed out after {timeout:g} seconds") from caught
        except error.URLError as caught:
            raise OpenCodeError(f"OpenCode request failed: {caught.reason}") from caught
        if not content:
            return None
        value = json.loads(content.decode("utf-8"))
        return value

    @staticmethod
    def _mcp_tools(dynamic_tools: list[dict[str, Any]]) -> list[dict[str, Any]]:
        result = []
        for item in dynamic_tools:
            if not isinstance(item, dict) or item.get("type") != "function" or not isinstance(item.get("inputSchema"), dict):
                raise OpenCodeError("OpenCode dynamic tool declaration is invalid")
            result.append({
                "name": str(item.get("name", "")),
                "description": str(item.get("description", "")),
                "inputSchema": item["inputSchema"],
            })
        if any(not item["name"] for item in result) or len({item["name"] for item in result}) != len(result):
            raise OpenCodeError("OpenCode dynamic tool names are invalid")
        return result

    def _ensure_tools(self, dynamic_tools: list[dict[str, Any]]) -> dict[str, str]:
        tools = self._mcp_tools(dynamic_tools)
        identity = hashlib.sha256(json.dumps(tools, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")).hexdigest()
        if self._tool_identity is not None:
            if identity != self._tool_identity:
                raise OpenCodeError("OpenCode worker tool manifest changed")
            return dict(self._tool_ids)
        token = secrets.token_urlsafe(32)
        callback = _CallbackServer(token)
        manifest = self.runtime_root / "ownward-tools.json"
        _atomic_json(manifest, {"schema": "ownward.opencode-mcp-tools/v1", "identity": identity, "tools": tools})
        config = {
            "type": "local",
            "command": [sys.executable, str(BRIDGE_PATH), "--manifest", str(manifest), "--callback", callback.url, "--token", token],
            "cwd": str(self.runtime_root),
            "enabled": True,
            "timeout": 30000,
        }
        try:
            status = self._http("POST", "/mcp", {"name": "ownward_active", "config": config})
            connected = status.get("ownward_active") if isinstance(status, dict) else None
            if isinstance(connected, dict) and connected.get("status") == "connected":
                # OpenCode names MCP tools deterministically as <server>_<declared-name>.
                # Its experimental inventory currently lists core tools only, so the
                # connected MCP status plus this public naming rule is the stable proof.
                by_name = {item["name"]: f"ownward_active_{item['name']}" for item in tools}
                self._callback = callback
                self._tool_identity = identity
                self._tool_ids = by_name
                return dict(by_name)
        except BaseException:
            callback.close()
            raise
        callback.close()
        raise OpenCodeError(
            "OpenCode did not expose the complete Ownward tool manifest: "
            + json.dumps({"status": status}, ensure_ascii=False, separators=(",", ":"))[:2000]
        )

    def invoke(
        self, *, prompt: str, schema: dict[str, Any], model: str, effort: str, work_dir: Path,
        timeout_seconds: float, dynamic_tools: list[dict[str, Any]] | None = None,
        tool_handler: Callable[[str, Any], Any] | None = None, base_instructions: str | None = None,
    ) -> tuple[dict[str, Any], dict[str, int], dict[str, Any]]:
        del work_dir  # The isolated server has no filesystem tools; work is checkpoint-owned by the caller.
        if (dynamic_tools is None) != (tool_handler is None):
            raise OpenCodeError("dynamic tools and their handler must be enabled together")
        model_id = self._normalize_model(model)
        if effort not in self.reasoning_efforts:
            raise OpenCodeError(f"OpenCode reasoning effort is not declared for provider {self.provider}: {effort}")
        with self._request_lock:
            tool_ids = self._ensure_tools(dynamic_tools) if dynamic_tools is not None else {}
            all_ids = self._http(
                "GET", "/experimental/tool/ids?directory=" + parse.quote(str(self.runtime_root)),
                timeout=min(30, timeout_seconds),
            )
            if not isinstance(all_ids, list):
                raise OpenCodeError("OpenCode tool inventory is invalid")
            tools = {str(identifier): False for identifier in all_ids}
            tools.update({identifier: True for identifier in tool_ids.values()})
            if self._callback is not None:
                self._callback.bind(tool_handler)
            session_id = ""
            try:
                permissions = [
                    {"permission": identifier, "pattern": "*", "action": "allow"}
                    for identifier in sorted(tool_ids.values())
                ]
                permissions.append({"permission": "*", "pattern": "*", "action": "deny"})
                session = self._http("POST", "/session", {
                    "title": "Ownward external-intelligence turn",
                    "permission": permissions,
                }, timeout=min(30, timeout_seconds))
                session_id = str(session.get("id", "")) if isinstance(session, dict) else ""
                if not session_id:
                    raise OpenCodeError("OpenCode created no session")
                schema_text = json.dumps(schema, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
                system = (
                    (base_instructions or "Do not use tools.")
                    + " Return only one strict JSON object matching this JSON Schema; do not use Markdown or commentary: "
                    + schema_text
                )
                result = self._http("POST", f"/session/{parse.quote(session_id)}/message", {
                    "model": {"providerID": self.provider, "modelID": model_id},
                    "variant": effort,
                    "tools": tools,
                    "system": system,
                    "parts": [{"type": "text", "text": prompt}],
                }, timeout=timeout_seconds)
                info = result.get("info") if isinstance(result, dict) and isinstance(result.get("info"), dict) else {}
                if isinstance(info.get("error"), dict):
                    detail = json.dumps(info["error"], ensure_ascii=False, separators=(",", ":"))
                    if "429" in detail or "rate limit" in detail.lower():
                        self._rate_limit_observed = True
                    raise OpenCodeError(f"OpenCode turn failed: {detail[:1000]}")
                if info.get("providerID") != self.provider or info.get("modelID") != model_id or info.get("variant") != effort:
                    raise OpenCodeError("OpenCode turn identity drifted")
                parts = result.get("parts") if isinstance(result, dict) and isinstance(result.get("parts"), list) else []
                texts = [str(part.get("text")) for part in parts if isinstance(part, dict) and part.get("type") == "text"]
                if not texts:
                    raise OpenCodeError("OpenCode turn produced no final text")
                try:
                    value = json.loads(texts[-1])
                except json.JSONDecodeError as caught:
                    raise OpenCodeError("OpenCode structured output is not strict JSON") from caught
                if not isinstance(value, dict):
                    raise OpenCodeError("OpenCode structured output is not an object")
                _validate_schema(value, schema)
                tokens = info.get("tokens") if isinstance(info.get("tokens"), dict) else {}
                cache = tokens.get("cache") if isinstance(tokens.get("cache"), dict) else {}
                usage = {
                    "input_tokens": int(tokens.get("input", 0)),
                    "cached_input_tokens": int(cache.get("read", 0)),
                    "output_tokens": int(tokens.get("output", 0)),
                    "reasoning_output_tokens": int(tokens.get("reasoning", 0)),
                }
                return value, usage, {
                    "transport": "opencode-server-http",
                    "server_instance": self.instance_id,
                    "session_id": session_id,
                    "session_ephemeral": True,
                    "thread_id": session_id,
                    "thread_ephemeral": True,
                    "sandbox": "read-only",
                    "status": str(info.get("finish", "")),
                    "dynamic_tools_enabled": dynamic_tools is not None,
                    "dynamic_tool_names": sorted(tool_ids),
                    "external_intelligence_driver": DRIVER,
                    "external_intelligence_provider": self.provider,
                }
            finally:
                if self._callback is not None:
                    self._callback.bind(None)
                if session_id:
                    try:
                        self._http("DELETE", f"/session/{parse.quote(session_id)}", timeout=5)
                    except OpenCodeError:
                        pass

    def diagnostics(self) -> dict[str, Any]:
        stderr = "".join(self._stderr).lower()
        return {
            "transport": "opencode-server-http",
            "server_processes": 1,
            "active_turns": 0,
            "rate_limit_observed": self._rate_limit_observed or any(
                marker in stderr for marker in ("rate limit", "rate_limit", "status 429", "http 429")
            ),
            "uptime_seconds": max(0.0, time.perf_counter() - self.started_at),
        }

    def __exit__(self, *_args: object) -> None:
        if self._callback is not None:
            self._callback.close()
            self._callback = None
        process = self.process
        if process is not None and process.poll() is None:
            if os.name == "nt":
                subprocess.run(
                    ["taskkill", "/PID", str(process.pid), "/T", "/F"], capture_output=True, text=True,
                    encoding="utf-8", errors="replace", timeout=10, check=False,
                )
            if process.poll() is None:
                process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
        if process is not None:
            for stream in (process.stdout, process.stderr):
                if stream is not None:
                    stream.close()
        self.process = None
        deadline = time.perf_counter() + 30
        while self.runtime_root.exists():
            try:
                shutil.rmtree(self.runtime_root)
            except OSError as caught:
                if time.perf_counter() >= deadline:
                    raise OpenCodeError("OpenCode runtime cleanup did not quiesce") from caught
                time.sleep(0.05)


class OpenCodePool:
    def __init__(self, size: int, factory: Callable[[int, int], OpenCodeServer], *, provider: str = "opencode") -> None:
        if size < 1:
            raise OpenCodeError("OpenCode pool size must be positive")
        self.size = size
        self.factory = factory
        self.provider = provider
        self._workers: dict[int, OpenCodeServer] = {}
        self._generations = [0] * size
        self._available: queue.Queue[int] = queue.Queue()
        self._lock = threading.Lock()
        self._active = 0
        self._maximum = 0
        self._restarts = 0
        self._rate_limit_observed = False

    def __enter__(self) -> "OpenCodePool":
        try:
            for index in range(self.size):
                self._workers[index] = self.factory(index, 0).__enter__()
                self._available.put(index)
        except BaseException:
            self.__exit__()
            raise
        return self

    def _restart(self, index: int) -> None:
        previous = self._workers.pop(index, None)
        if previous is not None:
            self._rate_limit_observed = self._rate_limit_observed or previous.diagnostics()["rate_limit_observed"]
            previous.__exit__()
        self._generations[index] += 1
        self._workers[index] = self.factory(index, self._generations[index]).__enter__()
        self._restarts += 1

    def invoke(self, **request_value: Any) -> tuple[dict[str, Any], dict[str, int], dict[str, Any]]:
        index = self._available.get()
        with self._lock:
            self._active += 1
            self._maximum = max(self._maximum, self._active)
        try:
            try:
                if index not in self._workers:
                    self._restart(index)
                value, usage, metadata = self._workers[index].invoke(**request_value)
            except OpenCodeError:
                self._restart(index)
                raise
            return value, usage, {
                **metadata,
                "worker_transport": metadata["transport"],
                "transport": "opencode-server-pool-http",
                "pool_worker": index,
                "pool_worker_generation": self._generations[index],
            }
        finally:
            with self._lock:
                self._active -= 1
            self._available.put(index)

    def diagnostics(self) -> dict[str, Any]:
        workers = list(self._workers.values())
        return {
            "transport": "opencode-server-pool-http",
            "server_processes": len(workers),
            "process_starts": self.size + self._restarts,
            "pool_size": self.size,
            "active_turns": self._active,
            "max_active": self._maximum,
            "per_worker_max_active": 1,
            "worker_restarts": self._restarts,
            "rate_limit_observed": self._rate_limit_observed or any(worker.diagnostics()["rate_limit_observed"] for worker in workers),
            "external_intelligence_driver": DRIVER,
            "external_intelligence_provider": self.provider,
        }

    def __exit__(self, *_args: object) -> None:
        failures = []
        for index, worker in list(self._workers.items()):
            try:
                worker.__exit__()
            except BaseException as caught:
                failures.append(caught)
            self._workers.pop(index, None)
        if failures:
            raise failures[0]


@contextmanager
def open_runtime(
    *, binary: Path, credential_file: Path, max_active: int, runtime_parent: Path,
    identity: dict[str, Any], provider: str, models: tuple[str, ...], reasoning_efforts: tuple[str, ...],
) -> Iterator[Any]:
    if not provider or not models or not reasoning_efforts:
        raise OpenCodeError("OpenCode provider capability declaration is incomplete")

    def factory(worker_index: int, generation: int) -> OpenCodeServer:
        root = runtime_parent / f"opencode-server-{worker_index:03d}-{generation:03d}-{secrets.token_hex(8)}"
        return OpenCodeServer(
            binary, credential_file, root, provider=provider, models=models, reasoning_efforts=reasoning_efforts,
        )

    with OpenCodePool(max_active, factory, provider=provider) as pool:
        class Transport:
            @property
            def identity(self) -> dict[str, Any]:
                return dict(identity)

            def invoke(self, **request_value: Any) -> tuple[dict[str, Any], dict[str, int], dict[str, Any]]:
                return pool.invoke(**request_value)

            def diagnostics(self) -> dict[str, Any]:
                return pool.diagnostics()

        yield Transport()


__all__ = [
    "DRIVER", "OpenCodeError", "OpenCodePool", "OpenCodeServer", "OpenCodeTimeout",
    "artifact_sha256", "identity_files", "implementation_sha256", "open_runtime", "probe", "resolve_native_binary", "validate",
]
