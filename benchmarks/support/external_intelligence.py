from __future__ import annotations

from concurrent.futures import Future, ThreadPoolExecutor
from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path
import re
import threading
import time
from typing import Any, Callable, Protocol


CONTRACT_SCHEMA = "ownward.external-intelligence/v1"
REQUEST_SCHEMA = "ownward.external-intelligence-request/v1"
ATTEMPT_SCHEMA = "ownward.external-intelligence-attempt/v1"
CHECKPOINT_SCHEMA = "ownward.external-intelligence-checkpoint/v1"
SELECTION_SCHEMA = "ownward.external-intelligence-selection/v2"


class ExternalIntelligenceError(RuntimeError):
    pass


class ExternalIntelligenceTimeout(ExternalIntelligenceError):
    pass


def validate_structured_output(value: Any, schema: dict[str, Any], path: str = "$") -> None:
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
        raise ExternalIntelligenceError(f"structured output violates schema at {path}: expected {expected}")
    for name in ("allOf",):
        clauses = schema.get(name)
        if isinstance(clauses, list):
            for clause in clauses:
                if isinstance(clause, dict):
                    validate_structured_output(value, clause, path)
    for name, exact in (("anyOf", False), ("oneOf", True)):
        clauses = schema.get(name)
        if isinstance(clauses, list):
            matches_count = 0
            for clause in clauses:
                try:
                    if isinstance(clause, dict):
                        validate_structured_output(value, clause, path)
                    else:
                        continue
                except ExternalIntelligenceError:
                    continue
                matches_count += 1
            if matches_count == 0 or (exact and matches_count != 1):
                raise ExternalIntelligenceError(f"structured output violates {name} at {path}")
    if "enum" in schema and value not in schema["enum"]:
        raise ExternalIntelligenceError(f"structured output violates enum at {path}")
    if "const" in schema and value != schema["const"]:
        raise ExternalIntelligenceError(f"structured output violates const at {path}")
    if isinstance(value, dict):
        properties = schema.get("properties") if isinstance(schema.get("properties"), dict) else {}
        missing = [name for name in schema.get("required", []) if name not in value]
        if missing:
            raise ExternalIntelligenceError(f"structured output is missing {path}.{missing[0]}")
        if schema.get("additionalProperties") is False:
            extras = sorted(set(value) - set(properties))
            if extras:
                raise ExternalIntelligenceError(f"structured output has an extra field at {path}.{extras[0]}")
        for name, child in value.items():
            if name in properties and isinstance(properties[name], dict):
                validate_structured_output(child, properties[name], f"{path}.{name}")
    if isinstance(value, list):
        if isinstance(schema.get("minItems"), int) and len(value) < schema["minItems"]:
            raise ExternalIntelligenceError(f"structured output has too few items at {path}")
        if isinstance(schema.get("maxItems"), int) and len(value) > schema["maxItems"]:
            raise ExternalIntelligenceError(f"structured output has too many items at {path}")
        child_schema = schema.get("items")
        if isinstance(child_schema, dict):
            for index, child in enumerate(value):
                validate_structured_output(child, child_schema, f"{path}[{index}]")
        if schema.get("uniqueItems") is True:
            serialized = [json.dumps(item, ensure_ascii=False, sort_keys=True, separators=(",", ":")) for item in value]
            if len(serialized) != len(set(serialized)):
                raise ExternalIntelligenceError(f"structured output has duplicate items at {path}")
    if isinstance(value, str):
        if isinstance(schema.get("minLength"), int) and len(value) < schema["minLength"]:
            raise ExternalIntelligenceError(f"structured output string is too short at {path}")
        if isinstance(schema.get("maxLength"), int) and len(value) > schema["maxLength"]:
            raise ExternalIntelligenceError(f"structured output string is too long at {path}")
        pattern = schema.get("pattern")
        if isinstance(pattern, str) and re.search(pattern, value) is None:
            raise ExternalIntelligenceError(f"structured output violates pattern at {path}")
    if isinstance(value, (int, float)) and not isinstance(value, bool):
        if isinstance(schema.get("minimum"), (int, float)) and value < schema["minimum"]:
            raise ExternalIntelligenceError(f"structured output is below minimum at {path}")
        if isinstance(schema.get("maximum"), (int, float)) and value > schema["maximum"]:
            raise ExternalIntelligenceError(f"structured output is above maximum at {path}")


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
        initial_context: str | None = None,
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
    prepare_context: Callable[[Callable[[str, Any], Any]], str] | None = None
    restore: Callable[[Any], None] | None = None
    validate: Callable[[], None] | None = None
    report: Callable[[], Any] | None = None
    resume_prompt: Callable[[str], str] | None = None


@dataclass(frozen=True)
class RuntimeIdentity:
    driver: str
    provider: str
    transport: str
    selection_sha256: str
    artifact_sha256: str
    implementation_sha256: str
    credential_locator_sha256: str
    max_active: int
    worker_processes: int

    def value(self) -> dict[str, Any]:
        result = {
            "schema": "ownward.external-intelligence-runtime-identity/v2",
            "contract": CONTRACT_SCHEMA,
            "driver": self.driver,
            "provider": self.provider,
            "transport": self.transport,
            "selection_sha256": self.selection_sha256,
            "artifact_sha256": self.artifact_sha256,
            "implementation_sha256": self.implementation_sha256,
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
    required = {"schema", "contract", "default_driver", "implementations", "role_profiles"}
    if not isinstance(value, dict) or set(value) != required:
        raise ExternalIntelligenceError("external-intelligence runtime selection fields changed")
    if value["schema"] != SELECTION_SCHEMA or value["contract"] != CONTRACT_SCHEMA:
        raise ExternalIntelligenceError("external-intelligence runtime selection schema changed")
    default_driver = value["default_driver"]
    implementations = value["implementations"]
    if not isinstance(default_driver, str) or not default_driver.strip():
        raise ExternalIntelligenceError("external-intelligence default driver is missing")
    if not isinstance(implementations, list) or not implementations:
        raise ExternalIntelligenceError("external-intelligence implementations are missing")
    expected = {
        "driver", "provider", "transport", "worker_isolation", "models", "reasoning_efforts",
        "reasoning_fallback",
    }
    by_driver: dict[str, dict[str, Any]] = {}
    for item in implementations:
        if not isinstance(item, dict) or set(item) != expected:
            raise ExternalIntelligenceError("external-intelligence implementation fields changed")
        for name in ("driver", "provider", "transport", "worker_isolation", "reasoning_fallback"):
            if not isinstance(item[name], str) or not item[name].strip():
                raise ExternalIntelligenceError(f"external-intelligence implementation {name} is missing")
        for name in ("models", "reasoning_efforts"):
            entries = item[name]
            if not isinstance(entries, list) or any(not isinstance(entry, str) or not entry.strip() for entry in entries):
                raise ExternalIntelligenceError(f"external-intelligence implementation {name} is invalid")
            if len(entries) != len(set(entries)):
                raise ExternalIntelligenceError(f"external-intelligence implementation {name} contains duplicates")
        driver = item["driver"]
        if driver in by_driver:
            raise ExternalIntelligenceError(f"duplicate external-intelligence driver: {driver}")
        by_driver[driver] = dict(item)
    if default_driver not in by_driver:
        raise ExternalIntelligenceError("external-intelligence default driver is unknown")
    profiles = value["role_profiles"]
    if not isinstance(profiles, dict) or set(profiles) != set(by_driver):
        raise ExternalIntelligenceError("external-intelligence role profiles do not match implementations")
    role_names = {"generator", "quality_admission", "semantic", "reader", "judge"}
    for driver, profile in profiles.items():
        if not isinstance(profile, dict) or set(profile) != role_names:
            raise ExternalIntelligenceError(f"external-intelligence {driver} role profile is incomplete")
        implementation = by_driver[driver]
        allowed_models = set(implementation["models"])
        allowed_efforts = set(implementation["reasoning_efforts"])
        for role, settings in profile.items():
            if not isinstance(settings, dict) or set(settings) != {"model", "reasoning_effort"}:
                raise ExternalIntelligenceError(f"external-intelligence {driver} {role} profile is invalid")
            model, effort = settings["model"], settings["reasoning_effort"]
            if not isinstance(model, str) or not model.strip() or not isinstance(effort, str) or not effort.strip():
                raise ExternalIntelligenceError(f"external-intelligence {driver} {role} profile is missing")
            if allowed_models and model.removeprefix(f"{implementation['provider']}/") not in allowed_models:
                raise ExternalIntelligenceError(f"external-intelligence {driver} {role} model is unsupported")
            if allowed_efforts and effort not in allowed_efforts:
                raise ExternalIntelligenceError(f"external-intelligence {driver} {role} effort is unsupported")
    selected = by_driver[default_driver]
    return {
        **value,
        # Keep the historical flattened default view for read-only callers.
        **{name: selected[name] for name in ("driver", "provider", "transport", "worker_isolation")},
        "selection_sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
    }


def select_runtime_implementation(selection: dict[str, Any], driver: str | None = None) -> dict[str, Any]:
    """Resolve one sealed implementation; a running request can never switch it."""
    selected_driver = driver or selection.get("default_driver")
    for item in selection.get("implementations", []):
        if isinstance(item, dict) and item.get("driver") == selected_driver:
            return {
                **item,
                "selection_sha256": canonical_sha256({
                    "schema": selection.get("schema"),
                    "contract": selection.get("contract"),
                    "implementation": item,
                }),
            }
    raise ExternalIntelligenceError(f"unsupported external-intelligence driver: {selected_driver}")


def effective_reasoning_effort(role: str, effort: str) -> str:
    """All new judging uses xhigh, including callers with older saved profiles."""
    if role.replace("_", "-") in {"judge", "quality-admission", "quality-admission-qualification"}:
        return "xhigh"
    return effort


def select_runtime_role_profile(selection: dict[str, Any], driver: str | None = None) -> dict[str, dict[str, str]]:
    """Resolve the qualified role profile for one implementation without reading credentials."""
    selected_driver = driver or selection.get("default_driver")
    select_runtime_implementation(selection, selected_driver)
    profile = selection.get("role_profiles", {}).get(selected_driver)
    if not isinstance(profile, dict):
        raise ExternalIntelligenceError(f"external-intelligence role profile is missing: {selected_driver}")
    return {role: {**settings, "reasoning_effort": effective_reasoning_effort(role, settings["reasoning_effort"])}
            for role, settings in profile.items()}


def validate_runtime_identity(value: dict[str, Any]) -> None:
    required = {
        "schema", "contract", "driver", "provider", "transport", "selection_sha256", "artifact_sha256",
        "implementation_sha256",
        "credential_locator_sha256", "credential_content_read", "max_active", "worker_processes",
    }
    if set(value) != required:
        raise ExternalIntelligenceError("external-intelligence runtime identity fields changed")
    if value["schema"] != "ownward.external-intelligence-runtime-identity/v2" or value["contract"] != CONTRACT_SCHEMA:
        raise ExternalIntelligenceError("external-intelligence runtime identity schema changed")
    for name in ("driver", "provider", "transport"):
        if not isinstance(value[name], str) or not value[name].strip():
            raise ExternalIntelligenceError(f"external-intelligence runtime {name} is missing")
    for name in ("selection_sha256", "artifact_sha256", "implementation_sha256", "credential_locator_sha256"):
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
        **({"base_instructions": base_instructions} if base_instructions is not None else {}),
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
        effort = effective_reasoning_effort(role, effort)
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
        if attempt_directories and all(_retryable_failed_attempt(path) for path in attempt_directories):
            cycle = canonical_sha256([
                _load_json(path / "metadata.json")
                for path in attempt_directories
            ])
            audit = stage / "_audit"
            sequence = len(list(audit.glob("retryable-runtime-cycle-*"))) if audit.is_dir() else 0
            destination = audit / f"retryable-runtime-cycle-{sequence + 1:03d}-{cycle}"
            destination.mkdir(parents=True, exist_ok=False)
            for path in attempt_directories:
                path.replace(destination / path.name)
            attempt_directories = []
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
        working_notes: list[str] = []
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
                    "prompt": lifecycle.resume_prompt("\n\n".join(working_notes)) if working_notes else prompt,
                    "schema": schema,
                    "model": model,
                    "effort": effort,
                    "work_dir": work,
                    "timeout_seconds": timeout_seconds,
                }
                if lifecycle.base_instructions is not None:
                    invoke_arguments["base_instructions"] = lifecycle.base_instructions
                if lifecycle.dynamic_tools is not None:
                    handler = lifecycle.tool_handler
                    if handler is not None and lifecycle.report is not None:
                        def traced_call(
                            name: str, arguments: Any, *, callback=handler, report=lifecycle.report,
                            path=attempt / "active-retrieval.json", lock=threading.Lock(),
                        ) -> Any:
                            # Persist completed calls before the model continues. A timeout,
                            # retry or process stop must not erase the evidence already read.
                            with lock:
                                try:
                                    return callback(name, arguments)
                                finally:
                                    _write_json(path, report())

                        handler = traced_call
                    invoke_arguments.update({
                        "dynamic_tools": lifecycle.dynamic_tools,
                        "tool_handler": handler,
                        "base_instructions": lifecycle.base_instructions,
                    })
                if lifecycle.prepare_context is not None:
                    if lifecycle.dynamic_tools is None or handler is None:
                        raise ExternalIntelligenceError("initial retrieval requires tool access")
                    context = lifecycle.prepare_context(handler)
                    _write_json(attempt / "initial-context.json", {"text": context})
                    invoke_arguments["initial_context"] = context
                    remaining = timeout_seconds - (time.perf_counter() - started)
                    if remaining <= 0:
                        raise ExternalIntelligenceTimeout("initial retrieval exhausted the request timeout")
                    invoke_arguments["timeout_seconds"] = remaining
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
                notes = getattr(error, "working_notes", "")
                if (isinstance(error, ExternalIntelligenceTimeout) and isinstance(notes, str) and notes
                        and lifecycle.resume_prompt is not None and lifecycle.dynamic_tools is None):
                    working_notes.append(notes)
                    _write_json(attempt / "interrupted-work.json", {"working_notes": notes})
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


def _retryable_failed_attempt(path: Path) -> bool:
    metadata_path = path / "metadata.json"
    if not metadata_path.is_file():
        return False
    metadata = _load_json(metadata_path)
    if not isinstance(metadata, dict) or metadata.get("outcome") != "failed":
        return False
    error_type = str(metadata.get("error_type", ""))
    message = str(metadata.get("error_message", "")).lower()
    return error_type in {
        "ConnectionAbortedError", "ConnectionError", "ConnectionResetError",
        "ExternalIntelligenceTimeout", "OpenCodeTimeout", "TimeoutError",
    } or _is_rate_limit(message) or bool(re.search(
        r'(?:\bhttp\s+|"statuscode"\s*:\s*)(?:500|502|503|504)\b', message,
    )) or any(marker in message for marker in (
        "connection reset", "connection aborted", "connection refused",
        "timed out", "timeout", "winerror 10053", "winerror 10054", "winerror 10061",
    ))


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
