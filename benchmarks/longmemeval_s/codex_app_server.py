from __future__ import annotations

import json
import os
from pathlib import Path
import queue
import shutil
import subprocess
import tempfile
import threading
import time
from typing import Any, Callable


class AppServerError(RuntimeError):
    pass


class AppServerTimeout(AppServerError):
    pass


class CodexAppServer:
    """One isolated Codex App Server process; every model request gets a fresh thread."""

    def __init__(self, binary: Path, auth_file: Path, runtime_root: Path, command_prefix: list[str], environment: dict[str, str]) -> None:
        self.binary = binary.resolve()
        self.auth_file = auth_file.resolve()
        self.runtime_root = runtime_root.resolve()
        self.command_prefix = list(command_prefix)
        self.environment = dict(environment)
        self.process: subprocess.Popen[str] | None = None
        self._request_id = 0
        self._write_lock = threading.Lock()
        self._state_lock = threading.Lock()
        self._responses: dict[int, queue.Queue[dict[str, Any]]] = {}
        self._turn_events: dict[str, queue.Queue[dict[str, Any]]] = {}
        self._completed_turns: dict[str, dict[str, Any]] = {}
        self._usage: dict[str, dict[str, Any]] = {}
        self._active_turns: dict[str, str] = {}
        self._fatal_error: str | None = None
        self._stderr_chunks: list[str] = []
        self.instance_id = ""
        self.started_at = 0.0

    @staticmethod
    def command(command_prefix: list[str]) -> list[str]:
        command = list(command_prefix) + [
            "app-server", "--listen", "stdio://", "-c", "project_doc_max_bytes=0",
            "-c", 'web_search="disabled"',
        ]
        for feature in (
            "apply_patch_freeform", "apps", "image_generation", "js_repl", "memories", "multi_agent",
            "personality", "plugins", "request_permissions_tool", "search_tool", "shell_snapshot",
            "shell_tool", "tool_search", "tool_suggest",
        ):
            command.extend(["-c", f"features.{feature}=false"])
        return command

    @staticmethod
    def direct_command_prefix(binary: Path, fallback: list[str]) -> list[str]:
        """Resolve the packaged native Codex binary so worker shutdown owns the real process."""
        binary = binary.resolve()
        if binary.suffix.lower() != ".ps1":
            return list(fallback)
        package_root = binary.parent / "node_modules" / "@openai" / "codex" / "node_modules" / "@openai"
        candidates = sorted(package_root.glob("codex-*/vendor/*/bin/codex.exe")) if package_root.is_dir() else []
        if len(candidates) != 1:
            raise AppServerError("Codex PowerShell entry does not resolve to exactly one native executable")
        return [str(candidates[0].resolve())]

    def __enter__(self) -> "CodexAppServer":
        self.runtime_root.mkdir(parents=True, exist_ok=True)
        self.started_at = time.perf_counter()
        self.instance_id = self.runtime_root.name
        self.process = subprocess.Popen(
            self.command(self.command_prefix), cwd=self.runtime_root, env=self.environment,
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            text=True, encoding="utf-8", errors="replace", bufsize=1,
        )
        threading.Thread(target=self._read_stdout, name="codex-app-server-stdout", daemon=True).start()
        threading.Thread(target=self._read_stderr, name="codex-app-server-stderr", daemon=True).start()
        self.request(
            "initialize",
            {"clientInfo": {"name": "ownward-longmemeval-s", "version": "1"}, "capabilities": {"experimentalApi": True}},
            timeout_seconds=30,
        )
        self.notify("initialized", {})
        return self

    def _read_stdout(self) -> None:
        assert self.process is not None and self.process.stdout is not None
        try:
            for line in self.process.stdout:
                try:
                    event = json.loads(line)
                except json.JSONDecodeError:
                    continue
                if not isinstance(event, dict):
                    continue
                if "id" in event and ("result" in event or "error" in event):
                    with self._state_lock:
                        target = self._responses.get(event["id"])
                    if target is not None:
                        target.put(event)
                    continue
                method = event.get("method")
                params = event.get("params") if isinstance(event.get("params"), dict) else {}
                if method == "thread/tokenUsage/updated":
                    turn_id = str(params.get("turnId", ""))
                    token_usage = params.get("tokenUsage") if isinstance(params.get("tokenUsage"), dict) else {}
                    with self._state_lock:
                        self._usage[turn_id] = token_usage.get("last", {}) if isinstance(token_usage.get("last"), dict) else {}
                elif method == "turn/completed":
                    turn = params.get("turn") if isinstance(params.get("turn"), dict) else {}
                    turn_id = str(turn.get("id", ""))
                    with self._state_lock:
                        target = self._turn_events.get(turn_id)
                        if target is None:
                            self._completed_turns[turn_id] = params
                    if target is not None:
                        target.put(params)
        finally:
            self._fail_waiters("Codex App Server output stream closed")

    def _read_stderr(self) -> None:
        assert self.process is not None and self.process.stderr is not None
        for line in self.process.stderr:
            with self._state_lock:
                self._stderr_chunks.append(line)
                if len(self._stderr_chunks) > 200:
                    del self._stderr_chunks[:100]

    def _fail_waiters(self, message: str) -> None:
        with self._state_lock:
            self._fatal_error = message
            responses = list(self._responses.values())
            turns = list(self._turn_events.values())
        failure = {"error": {"message": message}}
        for target in responses + turns:
            target.put(failure)

    def request(self, method: str, params: dict[str, Any], *, timeout_seconds: float = 30) -> dict[str, Any]:
        if self.process is None or self.process.poll() is not None:
            raise AppServerError(self._fatal_error or "Codex App Server is not running")
        target: queue.Queue[dict[str, Any]] = queue.Queue()
        with self._write_lock:
            self._request_id += 1
            request_id = self._request_id
            with self._state_lock:
                self._responses[request_id] = target
            assert self.process.stdin is not None
            self.process.stdin.write(json.dumps({"method": method, "id": request_id, "params": params}, ensure_ascii=False) + "\n")
            self.process.stdin.flush()
        try:
            response = target.get(timeout=timeout_seconds)
        except queue.Empty as error:
            raise AppServerTimeout(f"Codex App Server request timed out: {method}") from error
        finally:
            with self._state_lock:
                self._responses.pop(request_id, None)
        if "error" in response:
            detail = response["error"]
            message = detail.get("message", str(detail)) if isinstance(detail, dict) else str(detail)
            raise AppServerError(f"Codex App Server request failed: {method}: {message}")
        result = response.get("result")
        if not isinstance(result, dict):
            raise AppServerError(f"Codex App Server returned no result: {method}")
        return result

    def notify(self, method: str, params: dict[str, Any]) -> None:
        if self.process is None or self.process.poll() is not None:
            raise AppServerError(self._fatal_error or "Codex App Server is not running")
        with self._write_lock:
            assert self.process.stdin is not None
            self.process.stdin.write(json.dumps({"method": method, "params": params}, ensure_ascii=False) + "\n")
            self.process.stdin.flush()

    def invoke(
        self, *, prompt: str, schema: dict[str, Any], model: str, effort: str,
        work_dir: Path, timeout_seconds: float,
    ) -> tuple[dict[str, Any], dict[str, int], dict[str, Any]]:
        deadline = time.perf_counter() + timeout_seconds

        def remaining() -> float:
            value = deadline - time.perf_counter()
            if value <= 0:
                raise AppServerTimeout(f"Codex turn timed out after {timeout_seconds:g} seconds")
            return value

        thread_result = self.request("thread/start", {
            "model": model,
            "cwd": str(work_dir.resolve()),
            "approvalPolicy": "never",
            "sandbox": "read-only",
            "ephemeral": True,
            "dynamicTools": [],
            "environments": [],
            "baseInstructions": "Return only the requested structured JSON. Do not use tools.",
        }, timeout_seconds=min(30, remaining()))
        thread = thread_result.get("thread") if isinstance(thread_result.get("thread"), dict) else {}
        thread_id = str(thread.get("id", ""))
        if not thread_id:
            raise AppServerError("Codex App Server created no thread")
        try:
            turn_result = self.request("turn/start", {
                "threadId": thread_id,
                "input": [{"type": "text", "text": prompt}],
                "model": model,
                "effort": effort,
                "approvalPolicy": "never",
                "sandboxPolicy": {"type": "readOnly", "networkAccess": False},
                "outputSchema": schema,
            }, timeout_seconds=remaining())
        except AppServerTimeout as error:
            interrupted = self._interrupt_latest_turn(thread_id)
            raise AppServerTimeout(f"{error}; orphan_turn_interrupted={str(interrupted).lower()}") from error
        turn = turn_result.get("turn") if isinstance(turn_result.get("turn"), dict) else {}
        turn_id = str(turn.get("id", ""))
        if not turn_id:
            raise AppServerError("Codex App Server created no turn")
        target: queue.Queue[dict[str, Any]] = queue.Queue()
        with self._state_lock:
            self._active_turns[turn_id] = thread_id
            completed = self._completed_turns.pop(turn_id, None)
            if completed is None:
                self._turn_events[turn_id] = target
        try:
            if completed is None:
                try:
                    completed = target.get(timeout=remaining())
                except queue.Empty as error:
                    try:
                        self.request("turn/interrupt", {"threadId": thread_id, "turnId": turn_id}, timeout_seconds=10)
                    finally:
                        try:
                            target.get(timeout=10)
                        except queue.Empty:
                            pass
                    raise AppServerTimeout(f"Codex turn timed out after {timeout_seconds:g} seconds") from error
            if "error" in completed:
                raise AppServerError(str(completed["error"]))
            final_turn = completed.get("turn") if isinstance(completed.get("turn"), dict) else {}
            status = final_turn.get("status")
            if status != "completed":
                error = final_turn.get("error") if isinstance(final_turn.get("error"), dict) else {}
                raise AppServerError(f"Codex turn ended as {status}: {error.get('message', '')}")
            items = final_turn.get("items") if isinstance(final_turn.get("items"), list) else []
            forbidden = [item.get("type") for item in items if isinstance(item, dict) and item.get("type") not in {"userMessage", "agentMessage", "reasoning", "plan"}]
            if forbidden:
                raise AppServerError(f"Codex capability attempted to use a tool: {forbidden[0]}")
            messages = [str(item.get("text")) for item in items if isinstance(item, dict) and item.get("type") == "agentMessage"]
            if not messages:
                raise AppServerError("Codex turn produced no agent message")
            try:
                value = json.loads(messages[-1])
            except json.JSONDecodeError as error:
                raise AppServerError("Codex structured output is not JSON") from error
            if not isinstance(value, dict):
                raise AppServerError("Codex structured output is not an object")
            with self._state_lock:
                raw_usage = dict(self._usage.get(turn_id, {}))
            usage = {
                "input_tokens": int(raw_usage.get("inputTokens", 0)),
                "cached_input_tokens": int(raw_usage.get("cachedInputTokens", 0)),
                "output_tokens": int(raw_usage.get("outputTokens", 0)),
                "reasoning_output_tokens": int(raw_usage.get("reasoningOutputTokens", 0)),
            }
            metadata = {
                "transport": "codex-app-server-stdio",
                "server_instance": self.instance_id,
                "thread_id": thread_id,
                "turn_id": turn_id,
                "thread_ephemeral": True,
                "sandbox": "read-only",
                "status": status,
            }
            return value, usage, metadata
        finally:
            with self._state_lock:
                self._active_turns.pop(turn_id, None)
                self._turn_events.pop(turn_id, None)
                self._completed_turns.pop(turn_id, None)
                self._usage.pop(turn_id, None)

    def _interrupt_latest_turn(self, thread_id: str) -> bool:
        """Recover a turn id when turn/start itself timed out, then stop that exact turn."""
        try:
            result = self.request("thread/read", {"threadId": thread_id, "includeTurns": True}, timeout_seconds=10)
            thread = result.get("thread") if isinstance(result.get("thread"), dict) else {}
            turns = thread.get("turns") if isinstance(thread.get("turns"), list) else []
            latest = turns[-1] if turns and isinstance(turns[-1], dict) else {}
            turn_id = str(latest.get("id", ""))
            if not turn_id:
                return False
            self.request("turn/interrupt", {"threadId": thread_id, "turnId": turn_id}, timeout_seconds=10)
            with self._state_lock:
                self._active_turns.pop(turn_id, None)
                self._turn_events.pop(turn_id, None)
                self._completed_turns.pop(turn_id, None)
                self._usage.pop(turn_id, None)
            return True
        except AppServerError:
            return False

    def diagnostics(self) -> dict[str, Any]:
        with self._state_lock:
            stderr = "".join(self._stderr_chunks)
            active = len(self._active_turns)
        lowered = stderr.lower()
        return {
            "transport": "codex-app-server-stdio",
            "server_processes": 1,
            "active_turns": active,
            "rate_limit_observed": any(marker in lowered for marker in ("rate limit", "rate_limit", "status 429", "http 429")),
            "uptime_seconds": max(0.0, time.perf_counter() - self.started_at),
        }

    def __exit__(self, *_args: object) -> None:
        process = self.process
        if process is not None and process.poll() is None:
            with self._state_lock:
                active = list(self._active_turns.items())
            for turn_id, thread_id in active:
                try:
                    self.request("turn/interrupt", {"threadId": thread_id, "turnId": turn_id}, timeout_seconds=2)
                except AppServerError:
                    pass
            if os.name == "nt":
                subprocess.run(
                    ["taskkill", "/PID", str(process.pid), "/T", "/F"],
                    capture_output=True,
                    text=True,
                    encoding="utf-8",
                    errors="replace",
                    timeout=5,
                    check=False,
                )
            if process.poll() is None:
                process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
        if process is not None:
            for stream in (process.stdin, process.stdout, process.stderr):
                if stream is not None:
                    try:
                        stream.close()
                    except OSError:
                        pass
        self.process = None
        remove_runtime_root(self.runtime_root)


class CodexAppServerPool:
    """A bounded pool where every App Server owns at most one active turn."""

    def __init__(self, size: int, factory: Callable[[int, int], CodexAppServer]) -> None:
        if size < 1:
            raise ValueError("Codex App Server pool size must be positive")
        self.size = size
        self.factory = factory
        self._workers: dict[int, CodexAppServer] = {}
        self._generations = [0] * size
        self._available: queue.Queue[int] = queue.Queue()
        self._state_lock = threading.Lock()
        self._active = 0
        self._max_active = 0
        self._restarts = 0
        self._rate_limit_observed = False

    def __enter__(self) -> "CodexAppServerPool":
        try:
            for index in range(self.size):
                worker = self.factory(index, self._generations[index])
                self._workers[index] = worker.__enter__()
                self._available.put(index)
        except BaseException:
            self.__exit__()
            raise
        return self

    def _restart(self, index: int) -> None:
        previous = self._workers.pop(index, None)
        if previous is not None:
            self._rate_limit_observed = self._rate_limit_observed or bool(previous.diagnostics()["rate_limit_observed"])
            previous.__exit__()
        self._generations[index] += 1
        replacement = self.factory(index, self._generations[index])
        self._workers[index] = replacement.__enter__()
        with self._state_lock:
            self._restarts += 1

    def invoke(self, **request: Any) -> tuple[dict[str, Any], dict[str, int], dict[str, Any]]:
        index = self._available.get()
        with self._state_lock:
            self._active += 1
            self._max_active = max(self._max_active, self._active)
        try:
            worker = self._workers[index]
            try:
                value, usage, metadata = worker.invoke(**request)
            except AppServerError:
                self._restart(index)
                raise
            return value, usage, {
                **metadata,
                "worker_transport": metadata["transport"],
                "transport": "codex-app-server-pool-stdio",
                "pool_worker": index,
                "pool_worker_generation": self._generations[index],
            }
        finally:
            with self._state_lock:
                self._active -= 1
            self._available.put(index)

    def diagnostics(self) -> dict[str, Any]:
        with self._state_lock:
            active = self._active
            maximum = self._max_active
            restarts = self._restarts
        workers = list(self._workers.values())
        rate_limited = self._rate_limit_observed or any(bool(worker.diagnostics()["rate_limit_observed"]) for worker in workers)
        return {
            "transport": "codex-app-server-pool-stdio",
            "server_processes": len(workers),
            "process_starts": self.size + restarts,
            "pool_size": self.size,
            "active_turns": active,
            "max_active": maximum,
            "per_worker_max_active": 1,
            "worker_restarts": restarts,
            "rate_limit_observed": rate_limited,
        }

    def __exit__(self, *_args: object) -> None:
        failures: list[BaseException] = []
        for index, worker in list(self._workers.items()):
            try:
                try:
                    self._rate_limit_observed = self._rate_limit_observed or bool(worker.diagnostics()["rate_limit_observed"])
                finally:
                    worker.__exit__()
            except BaseException as error:
                failures.append(error)
            finally:
                self._workers.pop(index, None)
        if failures:
            raise failures[0]


def isolated_runtime_root(parent: Path) -> Path:
    parent.mkdir(parents=True, exist_ok=True)
    return Path(tempfile.mkdtemp(prefix="codex-app-server-", dir=parent))


def remove_runtime_root(path: Path, *, timeout_seconds: float = 30.0) -> None:
    """Remove one isolated worker root after Windows releases terminated-process handles."""
    deadline = time.perf_counter() + timeout_seconds
    last_error: OSError | None = None
    while path.exists():
        try:
            shutil.rmtree(path)
            return
        except OSError as error:
            last_error = error
            if time.perf_counter() >= deadline:
                raise AppServerError(f"Codex App Server runtime cleanup did not quiesce: {path.name}") from error
            time.sleep(0.05)
    if last_error is not None and path.exists():
        raise AppServerError(f"Codex App Server runtime cleanup failed: {path.name}") from last_error
