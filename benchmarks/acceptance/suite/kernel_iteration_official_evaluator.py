from __future__ import annotations

import hashlib
import json
from pathlib import Path
import subprocess
import threading
from typing import Any


class OfficialEvaluatorError(RuntimeError):
    pass


WORKER = Path(__file__).with_name("kernel_iteration_official_evaluator_worker.py").resolve()


def _sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def cold_probe(python: Path, evaluator: Path, *, timeout_seconds: float = 30.0) -> dict[str, Any]:
    python = python.resolve()
    evaluator = evaluator.resolve()
    if not python.is_file() or not evaluator.is_file() or not WORKER.is_file():
        raise OfficialEvaluatorError("official evaluator runtime is incomplete")
    try:
        completed = subprocess.run(
            [str(python), str(WORKER), str(evaluator), "--probe"],
            capture_output=True,
            text=True,
            encoding="utf-8",
            timeout=timeout_seconds,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as error:
        raise OfficialEvaluatorError(f"official evaluator cold probe failed: {error}") from error
    if completed.returncode != 0:
        detail = completed.stderr.strip().splitlines()[-1] if completed.stderr.strip() else "no stderr"
        raise OfficialEvaluatorError(f"official evaluator cold probe exited {completed.returncode}: {detail}")
    try:
        value = json.loads(completed.stdout)
    except json.JSONDecodeError as error:
        raise OfficialEvaluatorError("official evaluator cold probe returned invalid JSON") from error
    if not isinstance(value, dict) or value.get("schema") != "ownward.kernel-iteration-official-evaluator-probe/v1":
        raise OfficialEvaluatorError("official evaluator cold probe schema mismatch")
    if value.get("evaluator_sha256") != _sha256(evaluator) or value.get("prompts_distinct") is not True:
        raise OfficialEvaluatorError("official evaluator cold probe identity or prompt controls failed")
    return {**value, "worker_sha256": _sha256(WORKER), "python_sha256": _sha256(python)}


class PromptRenderer:
    def __init__(self, python: Path, evaluator: Path) -> None:
        self.python = python.resolve()
        self.evaluator = evaluator.resolve()
        self._process: subprocess.Popen[str] | None = None
        self._lock = threading.Lock()
        self._counter = 0

    def __enter__(self) -> "PromptRenderer":
        cold_probe(self.python, self.evaluator)
        self._process = subprocess.Popen(
            [str(self.python), str(WORKER), str(self.evaluator)],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            encoding="utf-8",
            bufsize=1,
        )
        ready = self._read_response()
        if ready.get("schema") != "ownward.kernel-iteration-official-evaluator-ready/v1" or ready.get("ready") is not True:
            self.close()
            raise OfficialEvaluatorError("official evaluator worker did not become ready")
        return self

    def render(self, question: dict[str, Any], hypothesis: str) -> str:
        with self._lock:
            process = self._require_process()
            self._counter += 1
            request_id = f"prompt-{self._counter}"
            request = {"request_id": request_id, "question": question, "hypothesis": hypothesis}
            assert process.stdin is not None
            process.stdin.write(json.dumps(request, ensure_ascii=False) + "\n")
            process.stdin.flush()
            response = self._read_response()
            if response.get("request_id") != request_id or response.get("ok") is not True:
                raise OfficialEvaluatorError(
                    f"official evaluator render failed: {response.get('error_type', 'invalid-response')}: {response.get('message', '')}"
                )
            prompt = response.get("prompt")
            if not isinstance(prompt, str) or not prompt:
                raise OfficialEvaluatorError("official evaluator returned no prompt")
            return prompt

    def close(self) -> None:
        process = self._process
        self._process = None
        if process is None:
            return
        try:
            if process.poll() is None and process.stdin is not None:
                process.stdin.write(json.dumps({"action": "close", "request_id": "close"}) + "\n")
                process.stdin.flush()
                if process.stdout is not None:
                    process.stdout.readline()
                process.wait(timeout=5)
        except (OSError, subprocess.TimeoutExpired):
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
        finally:
            for stream in (process.stdin, process.stdout, process.stderr):
                if stream is not None:
                    stream.close()

    def __exit__(self, _type: Any, _value: Any, _traceback: Any) -> None:
        self.close()

    def _require_process(self) -> subprocess.Popen[str]:
        if self._process is None or self._process.poll() is not None:
            raise OfficialEvaluatorError("official evaluator worker is unavailable")
        return self._process

    def _read_response(self) -> dict[str, Any]:
        process = self._require_process()
        assert process.stdout is not None
        line = process.stdout.readline()
        if not line:
            detail = process.stderr.read().strip() if process.stderr is not None else ""
            raise OfficialEvaluatorError(f"official evaluator worker stopped unexpectedly: {detail[-500:]}")
        try:
            value = json.loads(line)
        except json.JSONDecodeError as error:
            raise OfficialEvaluatorError("official evaluator worker returned invalid JSON") from error
        if not isinstance(value, dict):
            raise OfficialEvaluatorError("official evaluator worker returned a non-object response")
        return value
