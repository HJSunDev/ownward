from __future__ import annotations

from concurrent.futures import Future, ThreadPoolExecutor
from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path
import threading
import time
from typing import Any, Callable, Protocol


CONTRACT_SCHEMA = "ownward.external-intelligence/v1"
REQUEST_SCHEMA = "ownward.external-intelligence-request/v1"
ATTEMPT_SCHEMA = "ownward.external-intelligence-attempt/v1"
CHECKPOINT_SCHEMA = "ownward.external-intelligence-checkpoint/v1"
SELECTION_SCHEMA = "ownward.external-intelligence-selection/v1"


class ExternalIntelligenceError(RuntimeError):
    pass


class ExternalIntelligenceTimeout(ExternalIntelligenceError):
    pass


class ExternalIntelligenceTransport(Protocol):
    """Provider-neutral structured-turn transport used by evaluation workflows."""

    @property
    def identity(self) -> dict[str, Any]:
        ...

    def invoke(
        self,
        *,
        prompt: str,
        schema: dict[str, Any],
        model: str,
        effort: str,
        work_dir: Any,
        timeout_seconds: float,
        dynamic_tools: list[dict[str, Any]] | None = None,
        tool_handler: Callable[[str, Any], Any] | None = None,
        base_instructions: str | None = None,
    ) -> tuple[dict[str, Any], dict[str, int], dict[str, Any]]:
        ...

    def diagnostics(self) -> dict[str, Any]:
        ...


@dataclass(frozen=True)
class InvocationLifecycle:
    """Optional request-local hooks for tool-using external-intelligence turns."""

    retrieval_mode: str
    tool_manifest_identity: str | None = None
    dynamic_tools: list[dict[str, Any]] | None = None
    tool_handler: Callable[[str, Any], Any] | None = None
    base_instructions: str | None = None
    reset_attempt: Callable[[], None] | None = None
    restore: Callable[[Any], None] | None = None
    validate: Callable[[], None] | None = None
    report: Callable[[], Any] | None = None


@dataclass(frozen=True)
class RuntimeIdentity:
    driver: str
    provider: str
    transport: str
    selection_sha256: str
    artifact_sha256: str
    credential_locator_sha256: str
    max_active: int
    worker_processes: int

    def value(self) -> dict[str, Any]:
        result = {
            "schema": "ownward.external-intelligence-runtime-identity/v1",
            "contract": CONTRACT_SCHEMA,
            "driver": self.driver,
            "provider": self.provider,
            "transport": self.transport,
            "selection_sha256": self.selection_sha256,
            "artifact_sha256": self.artifact_sha256,
            "credential_locator_sha256": self.credential_locator_sha256,
            "credential_content_read": False,
            "max_active": self.max_active,
            "worker_processes": self.worker_processes,
        }
        validate_runtime_identity(result)
        return result


def canonical_sha256(value: Any) -> str:
    encoded = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def load_runtime_selection(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    required = {"schema", "contract", "driver", "provider", "transport", "worker_isolation"}
    if not isinstance(value, dict) or set(value) != required:
        raise ExternalIntelligenceError("external-intelligence runtime selection fields changed")
    if value["schema"] != SELECTION_SCHEMA or value["contract"] != CONTRACT_SCHEMA:
        raise ExternalIntelligenceError("external-intelligence runtime selection schema changed")
    for name in ("driver", "provider", "transport", "worker_isolation"):
        if not isinstance(value[name], str) or not value[name].strip():
            raise ExternalIntelligenceError(f"external-intelligence runtime selection {name} is missing")
    return {**value, "selection_sha256": hashlib.sha256(path.read_bytes()).hexdigest()}


def validate_runtime_identity(value: dict[str, Any]) -> None:
    required = {
        "schema", "contract", "driver", "provider", "transport", "selection_sha256", "artifact_sha256",
        "credential_locator_sha256", "credential_content_read", "max_active", "worker_processes",
    }
    if set(value) != required:
        raise ExternalIntelligenceError("external-intelligence runtime identity fields changed")
    if value["schema"] != "ownward.external-intelligence-runtime-identity/v1" or value["contract"] != CONTRACT_SCHEMA:
        raise ExternalIntelligenceError("external-intelligence runtime identity schema changed")
    for name in ("driver", "provider", "transport"):
        if not isinstance(value[name], str) or not value[name].strip():
            raise ExternalIntelligenceError(f"external-intelligence runtime {name} is missing")
    for name in ("selection_sha256", "artifact_sha256", "credential_locator_sha256"):
        item = value[name]
        if not isinstance(item, str) or len(item) != 64 or any(character not in "0123456789abcdef" for character in item):
            raise ExternalIntelligenceError(f"external-intelligence runtime {name} is invalid")
    if value["credential_content_read"] is not False:
        raise ExternalIntelligenceError("external-intelligence credentials must not enter evidence identity")
    if not isinstance(value["max_active"], int) or value["max_active"] < 1:
        raise ExternalIntelligenceError("external-intelligence max_active must be positive")
    if not isinstance(value["worker_processes"], int) or value["worker_processes"] < 1:
        raise ExternalIntelligenceError("external-intelligence worker_processes must be positive")


def request_identity(
    *,
    role: str,
    prompt: str,
    schema: dict[str, Any],
    model: str,
    effort: str,
    retrieval_mode: str,
    tool_manifest_identity: str | None,
    base_instructions: str | None,
    timeout_seconds: float,
    maximum_attempts: int,
    runtime_identity: dict[str, Any],
) -> tuple[str, dict[str, Any]]:
    validate_runtime_identity(runtime_identity)
    if not isinstance(role, str) or not role.strip():
        raise ExternalIntelligenceError("external-intelligence role is missing")
    if timeout_seconds <= 0 or maximum_attempts < 1:
        raise ExternalIntelligenceError("external-intelligence execution policy is invalid")
    identity_input = {
        "contract": CONTRACT_SCHEMA,
        "role": role,
        "prompt": prompt,
        "schema": schema,
        "model": model,
        "effort": effort,
        "retrieval_mode": retrieval_mode,
        "tool_manifest_identity": tool_manifest_identity,
        "base_instructions": base_instructions,
        "timeout_seconds": timeout_seconds,
        "maximum_attempts": maximum_attempts,
        "runtime_identity": runtime_identity,
    }
    identity = canonical_sha256(identity_input)
    return identity, {
        "schema": REQUEST_SCHEMA,
        "identity": identity,
        "role": role,
        "prompt_sha256": hashlib.sha256(prompt.encode("utf-8")).hexdigest(),
        "output_schema_sha256": canonical_sha256(schema),
        "model": model,
        "reasoning_effort": effort,
        "retrieval_mode": retrieval_mode,
        "tool_manifest_identity": tool_manifest_identity,
        "timeout_seconds": timeout_seconds,
        "maximum_attempts": maximum_attempts,
        "runtime_identity": runtime_identity,
    }


def _load_json(path: Path) -> Any:
    return json.loads(path.read_text(encoding="utf-8"))


def _write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.{os.getpid()}.{time.time_ns()}.tmp")
    temporary.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    temporary.replace(path)


def _file_sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _is_rate_limit(value: str) -> bool:
    lowered = value.lower()
    return any(marker in lowered for marker in ("rate limit", "rate_limit", "too many requests", "status 429", "http 429"))


class ExternalIntelligenceExecutor:
    """Provider-neutral turn execution, bounded retry, and atomic recovery."""

    def __init__(self, transport: ExternalIntelligenceTransport) -> None:
        self.transport = transport

    def invoke(
        self,
        *,
        role: str,
        prompt: str,
        schema: dict[str, Any],
        stage: Path,
        model: str,
        effort: str,
        timeout_seconds: float,
        attempts: int,
        validate: Callable[[dict[str, Any]], None] | None = None,
        lifecycle: InvocationLifecycle | None = None,
    ) -> tuple[dict[str, Any], dict[str, int]]:
        lifecycle = lifecycle or InvocationLifecycle(retrieval_mode="no-tools")
        identity, request_value = request_identity(
            role=role,
            prompt=prompt,
            schema=schema,
            model=model,
            effort=effort,
            retrieval_mode=lifecycle.retrieval_mode,
            tool_manifest_identity=lifecycle.tool_manifest_identity,
            base_instructions=lifecycle.base_instructions,
            timeout_seconds=timeout_seconds,
            maximum_attempts=attempts,
            runtime_identity=self.transport.identity,
        )
        request_path = stage / "request.json"
        if request_path.is_file():
            if _load_json(request_path) != request_value:
                raise ExternalIntelligenceError("external-intelligence request identity changed")
        else:
            _write_json(request_path, request_value)
        complete_path = stage / "complete.json"
        if complete_path.is_file():
            complete = _load_json(complete_path)
            if not isinstance(complete, dict) or complete.get("identity") != identity:
                raise ExternalIntelligenceError("external-intelligence checkpoint identity changed")
            try:
                if lifecycle.restore is not None:
                    lifecycle.restore(complete.get("active_retrieval"))
                if validate is not None:
                    validate(complete["output"])
                if lifecycle.validate is not None:
                    lifecycle.validate()
            except (ExternalIntelligenceError, ValueError):
                audit = stage / "_audit"
                audit.mkdir(parents=True, exist_ok=True)
                archived = audit / f"invalid-complete-{_file_sha256(complete_path)}.json"
                if archived.is_file():
                    if archived.read_bytes() != complete_path.read_bytes():
                        raise ExternalIntelligenceError("external-intelligence invalid checkpoint audit changed")
                    complete_path.unlink()
                else:
                    complete_path.replace(archived)
            else:
                return complete["output"], complete["usage"]
        stage.mkdir(parents=True, exist_ok=True)
        last_error = ""
        attempt_directories = sorted(path for path in stage.glob("attempt-*") if path.is_dir())
        existing_attempts = len(attempt_directories)
        prior_wall_seconds = 0.0
        prior_rate_limits = 0
        interrupted_attempts = 0
        for attempt in attempt_directories:
            metadata_path = attempt / "metadata.json"
            if not metadata_path.is_file():
                interrupted_attempts += 1
                continue
            metadata = _load_json(metadata_path)
            if not isinstance(metadata, dict):
                raise ExternalIntelligenceError("external-intelligence attempt metadata is invalid")
            prior_wall_seconds += float(metadata.get("wall_seconds", 0.0))
            prior_rate_limits += int(bool(metadata.get("rate_limited", False)))
        for number in range(existing_attempts + 1, attempts + 1):
            attempt = stage / f"attempt-{number:03d}"
            attempt.mkdir()
            work = attempt / "work"
            work.mkdir()
            attempt_started = time.perf_counter()
            try:
                if lifecycle.reset_attempt is not None:
                    lifecycle.reset_attempt()
                started = time.perf_counter()
                invoke_arguments: dict[str, Any] = {
                    "prompt": prompt,
                    "schema": schema,
                    "model": model,
                    "effort": effort,
                    "work_dir": work,
                    "timeout_seconds": timeout_seconds,
                }
                if lifecycle.dynamic_tools is not None:
                    invoke_arguments.update({
                        "dynamic_tools": lifecycle.dynamic_tools,
                        "tool_handler": lifecycle.tool_handler,
                        "base_instructions": lifecycle.base_instructions,
                    })
                value, usage, transport = self.transport.invoke(**invoke_arguments)
                elapsed = time.perf_counter() - started
                if not isinstance(value, dict):
                    raise ExternalIntelligenceError("external-intelligence output is not an object")
                if validate is not None:
                    validate(value)
                if lifecycle.validate is not None:
                    lifecycle.validate()
                rate_limited = bool(self.transport.diagnostics()["rate_limit_observed"])
                _write_json(attempt / "metadata.json", {
                    "schema": ATTEMPT_SCHEMA,
                    "attempt": number,
                    "outcome": "complete",
                    "wall_seconds": elapsed,
                    "rate_limited": rate_limited,
                    **transport,
                })
                usage.update({
                    "calls": 1,
                    "attempts": number,
                    "retries": number - 1,
                    "rate_limit_events": prior_rate_limits + int(rate_limited),
                    "interrupted_attempts": interrupted_attempts,
                    "wall_seconds": prior_wall_seconds + elapsed,
                })
                _write_json(complete_path, {
                    "schema": CHECKPOINT_SCHEMA,
                    "identity": identity,
                    "output": value,
                    "usage": usage,
                    "wall_seconds": usage["wall_seconds"],
                    "active_retrieval": lifecycle.report() if lifecycle.report is not None else None,
                })
                return value, usage
            except (ExternalIntelligenceError, OSError, ValueError) as error:
                last_error = str(error)
                elapsed = time.perf_counter() - attempt_started
                rate_limited = _is_rate_limit(last_error) or bool(self.transport.diagnostics()["rate_limit_observed"])
                _write_json(attempt / "metadata.json", {
                    "schema": ATTEMPT_SCHEMA,
                    "attempt": number,
                    "outcome": "failed",
                    "error_type": type(error).__name__,
                    "error_message": last_error[:1000],
                    "wall_seconds": elapsed,
                    "rate_limited": rate_limited,
                    "transport": self.transport.diagnostics().get("transport", "external-intelligence"),
                })
                prior_wall_seconds += elapsed
                prior_rate_limits += int(rate_limited)
        raise ExternalIntelligenceError(
            f"external-intelligence capability failed after {attempts} bounded attempts: {last_error}"
        )


class BoundedScheduler:
    """One provider-neutral concurrency bound for independent intelligence turns."""

    def __init__(self, max_active: int) -> None:
        if max_active < 1:
            raise ExternalIntelligenceError("external-intelligence concurrency limit must be positive")
        self.max_active = max_active
        self._pool = ThreadPoolExecutor(max_workers=max_active, thread_name_prefix="external-intelligence")
        self._lock = threading.Lock()
        self._active = 0
        self._maximum = 0
        self._submitted = 0

    def submit(self, callback: Callable[..., Any], *args: Any, **kwargs: Any) -> Future[Any]:
        with self._lock:
            self._submitted += 1

        def bounded() -> Any:
            with self._lock:
                self._active += 1
                self._maximum = max(self._maximum, self._active)
            try:
                return callback(*args, **kwargs)
            finally:
                with self._lock:
                    self._active -= 1

        return self._pool.submit(bounded)

    def snapshot(self) -> dict[str, int]:
        with self._lock:
            return {"limit": self.max_active, "max_active": self._maximum, "submitted": self._submitted}

    def __enter__(self) -> "BoundedScheduler":
        return self

    def __exit__(self, *_args: object) -> None:
        self._pool.shutdown(wait=True, cancel_futures=False)
