from __future__ import annotations

import os
from pathlib import Path
import signal
import subprocess
import threading
from typing import Mapping


class ProcessTimeout(TimeoutError):
    def __init__(self, message: str, stdout: str = "", stderr: str = "") -> None:
        super().__init__(message)
        self.stdout = stdout
        self.stderr = stderr


def run(
    command: list[str],
    *,
    cwd: Path,
    timeout: float,
    input_text: str | None = None,
    env: Mapping[str, str] | None = None,
    stdout_path: Path | None = None,
    stderr_path: Path | None = None,
) -> subprocess.CompletedProcess[str]:
    if timeout <= 0:
        raise ProcessTimeout("process wall-clock budget is exhausted")
    process = subprocess.Popen(
        command,
        cwd=cwd,
        stdin=subprocess.PIPE if input_text is not None else subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        encoding="utf-8",
        errors="replace",
        env=env,
        creationflags=subprocess.CREATE_NEW_PROCESS_GROUP if os.name == "nt" else 0,
        start_new_session=os.name != "nt",
    )
    stdout: list[str] = []
    stderr: list[str] = []
    threads = []
    stream_errors: list[BaseException] = []

    def drain(source: object, target: list[str], output_path: Path | None) -> None:
        output = None
        try:
            if output_path is not None:
                output_path.parent.mkdir(parents=True, exist_ok=True)
                output = output_path.open("w", encoding="utf-8", newline="")
            for chunk in iter(source.readline, ""):  # type: ignore[attr-defined]
                target.append(chunk)
                if output is not None:
                    output.write(chunk)
                    output.flush()
        except BaseException as error:
            stream_errors.append(error)
        finally:
            try:
                if output is not None:
                    output.flush()
                    os.fsync(output.fileno())
                    output.close()
                source.close()  # type: ignore[attr-defined]
            except BaseException as error:
                stream_errors.append(error)

    assert process.stdout is not None and process.stderr is not None
    for source, target, output_path in (
        (process.stdout, stdout, stdout_path),
        (process.stderr, stderr, stderr_path),
    ):
        thread = threading.Thread(target=drain, args=(source, target, output_path), daemon=True)
        thread.start()
        threads.append(thread)
    if process.stdin is not None:
        try:
            process.stdin.write(input_text or "")
            process.stdin.flush()
        finally:
            process.stdin.close()
    timed_out = False
    try:
        process.wait(timeout=timeout)
    except subprocess.TimeoutExpired:
        timed_out = True
        _terminate_tree(process)
        process.wait()
    for thread in threads:
        thread.join(timeout=5)
        if thread.is_alive():
            stream_errors.append(RuntimeError("process output stream did not close after process exit"))
    stdout_text, stderr_text = "".join(stdout), "".join(stderr)
    if timed_out:
        detail = f"; output capture failed: {stream_errors[0]}" if stream_errors else ""
        raise ProcessTimeout(
            f"process exceeded {timeout:.0f} seconds{detail}",
            stdout_text,
            stderr_text,
        )
    if stream_errors:
        raise RuntimeError(f"process output capture failed: {stream_errors[0]}")
    return subprocess.CompletedProcess(command, process.returncode, stdout_text, stderr_text)


def _terminate_tree(process: subprocess.Popen[str]) -> None:
    if process.poll() is not None:
        return
    if os.name == "nt":
        subprocess.run(
            ["taskkill", "/PID", str(process.pid), "/T", "/F"],
            check=False,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        if process.poll() is None:
            process.kill()
        return
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
