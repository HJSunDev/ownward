from __future__ import annotations

from dataclasses import dataclass
import json
import os
from pathlib import Path
import queue
import secrets
import signal
import subprocess
import threading
import time
from typing import Any
from urllib import request


class MCPError(RuntimeError):
    pass


class StreamableHTTPClient:
    def __init__(self, endpoint: str, timeout_seconds: float, bearer_token: str = "") -> None:
        self.endpoint = endpoint
        self.timeout_seconds = timeout_seconds
        self.bearer_token = bearer_token
        self._opener = request.build_opener(request.ProxyHandler({}))
        self._session_id = ""
        self._next_id = 1
        initialized = self._request(
            "initialize",
            {
                "protocolVersion": "2025-06-18",
                "capabilities": {},
                "clientInfo": {"name": "ownward-acceptance-suite", "version": "1"},
            },
        )
        if not isinstance(initialized, dict) or not isinstance(initialized.get("serverInfo"), dict):
            raise MCPError("Ownward MCP initialization returned invalid metadata")
        self._notification("notifications/initialized", {})

    def _headers(self) -> dict[str, str]:
        headers = {
            "Accept": "application/json, text/event-stream",
            "Content-Type": "application/json",
        }
        if self._session_id:
            headers["Mcp-Session-Id"] = self._session_id
        if self.bearer_token:
            headers["Authorization"] = "Bearer " + self.bearer_token
        return headers

    def _post(self, payload: dict[str, Any], *, expect_result: bool) -> dict[str, Any] | None:
        encoded = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
        message = request.Request(self.endpoint, data=encoded, headers=self._headers(), method="POST")
        with self._opener.open(message, timeout=self.timeout_seconds) as response:
            session = response.headers.get("Mcp-Session-Id", "").strip()
            if session:
                self._session_id = session
            content = response.read()
        if not expect_result:
            return None
        if not content:
            raise MCPError("Ownward MCP returned an empty response")
        try:
            decoded = json.loads(content)
        except json.JSONDecodeError as error:
            raise MCPError("Ownward MCP returned invalid JSON") from error
        if not isinstance(decoded, dict):
            raise MCPError("Ownward MCP response is not an object")
        if decoded.get("error") is not None:
            raise MCPError(f"Ownward MCP error: {decoded['error']}")
        result = decoded.get("result")
        if not isinstance(result, dict):
            raise MCPError("Ownward MCP response has no result")
        return result

    def _request(self, method: str, params: dict[str, Any]) -> dict[str, Any]:
        message_id = self._next_id
        self._next_id += 1
        result = self._post(
            {"jsonrpc": "2.0", "id": message_id, "method": method, "params": params},
            expect_result=True,
        )
        assert result is not None
        return result

    def _notification(self, method: str, params: dict[str, Any]) -> None:
        self._post({"jsonrpc": "2.0", "method": method, "params": params}, expect_result=False)

    def call_tool(self, name: str, arguments: dict[str, Any]) -> Any:
        result = self._request("tools/call", {"name": name, "arguments": arguments})
        if result.get("isError") is True:
            raise MCPError(f"Ownward tool {name} failed: {result}")
        structured = result.get("structuredContent")
        if structured is None:
            structured = result.get("structured_content")
        if structured is not None:
            return structured
        for item in result.get("content", []):
            if not isinstance(item, dict) or item.get("type") != "text":
                continue
            try:
                return json.loads(str(item.get("text", "")))
            except json.JSONDecodeError:
                continue
        raise MCPError(f"Ownward tool {name} returned no structured result")

    def close(self) -> None:
        if not self._session_id:
            return
        message = request.Request(self.endpoint, headers=self._headers(), method="DELETE")
        try:
            with self._opener.open(message, timeout=min(self.timeout_seconds, 5)):
                pass
        except Exception:
            pass
        self._session_id = ""


@dataclass(frozen=True)
class RuntimeBinding:
    endpoint: str
    bearer_token: str
    binary: str
    data_directory: str


class OwnwardRuntime:
    def __init__(
        self,
        binary: Path,
        data_directory: Path,
        environment: dict[str, str],
        *,
        startup_seconds: float = 45,
        operation_seconds: float = 45,
    ) -> None:
        self.binary = binary.resolve()
        self.data_directory = data_directory.resolve()
        self.environment = dict(environment)
        self.startup_seconds = startup_seconds
        self.operation_seconds = operation_seconds
        self.process: subprocess.Popen[str] | None = None
        self.client: StreamableHTTPClient | None = None
        self.binding: RuntimeBinding | None = None
        self._stderr: list[str] = []
        self._stderr_thread: threading.Thread | None = None
        self._bearer_token = secrets.token_urlsafe(32)

    def __enter__(self) -> OwnwardRuntime:
        flags = (
            getattr(subprocess, "CREATE_NO_WINDOW", 0) | subprocess.CREATE_NEW_PROCESS_GROUP
            if os.name == "nt" else 0
        )
        self.process = subprocess.Popen(
            [
                str(self.binary),
                "mcp-http",
                "--data-dir",
                str(self.data_directory),
                "--listen",
                "127.0.0.1:0",
                "--token",
                self._bearer_token,
            ],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            encoding="utf-8",
            env=self.environment,
            creationflags=flags,
            start_new_session=os.name != "nt",
        )
        assert self.process.stdout is not None and self.process.stderr is not None
        messages: queue.Queue[str | None] = queue.Queue()

        def read_stdout() -> None:
            assert self.process is not None and self.process.stdout is not None
            try:
                for line in self.process.stdout:
                    messages.put(line)
            finally:
                messages.put(None)

        def read_stderr() -> None:
            assert self.process is not None and self.process.stderr is not None
            for line in self.process.stderr:
                self._stderr.append(line)
                if len(self._stderr) > 200:
                    del self._stderr[:-200]

        threading.Thread(target=read_stdout, daemon=True).start()
        self._stderr_thread = threading.Thread(target=read_stderr, daemon=True)
        self._stderr_thread.start()
        deadline = time.monotonic() + self.startup_seconds
        output = ""
        endpoint = ""
        while time.monotonic() < deadline:
            if self.process.poll() is not None:
                raise MCPError(f"Ownward MCP exited during startup: {''.join(self._stderr)[-2000:]}")
            try:
                line = messages.get(timeout=min(0.25, max(0.01, deadline - time.monotonic())))
            except queue.Empty:
                continue
            if line is None:
                break
            output += line
            try:
                metadata = json.loads(output)
            except json.JSONDecodeError:
                continue
            if isinstance(metadata, dict):
                endpoint = str(metadata.get("endpoint", "")).strip()
            break
        if not endpoint.startswith("http://127.0.0.1:"):
            self.close()
            raise MCPError(f"Ownward MCP did not publish a loopback endpoint: {output[-2000:]}")
        self.client = StreamableHTTPClient(endpoint, self.operation_seconds, self._bearer_token)
        self.binding = RuntimeBinding(endpoint, self._bearer_token, str(self.binary), str(self.data_directory))
        return self

    def close(self) -> None:
        if self.client is not None:
            self.client.close()
            self.client = None
        process = self.process
        self.process = None
        if process is None:
            return
        if process.poll() is None:
            if os.name == "nt":
                subprocess.run(
                    ["taskkill", "/PID", str(process.pid), "/T", "/F"],
                    check=False,
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.DEVNULL,
                )
                if process.poll() is None:
                    process.kill()
            else:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=10)
        for stream in (process.stdout, process.stderr):
            if stream is not None:
                stream.close()
        if self._stderr_thread is not None:
            self._stderr_thread.join(timeout=1)
            self._stderr_thread = None

    def __exit__(self, _type: object, _value: object, _traceback: object) -> None:
        self.close()
