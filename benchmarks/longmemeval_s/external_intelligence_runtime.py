from __future__ import annotations

from contextlib import contextmanager
from dataclasses import dataclass
import hashlib
import json
from pathlib import Path
import sys
from typing import Any, Iterator


BENCHMARK_ROOT = Path(__file__).resolve().parent
SUPPORT_ROOT = BENCHMARK_ROOT.parent / "support"
if str(SUPPORT_ROOT) not in sys.path:
    sys.path.insert(0, str(SUPPORT_ROOT))

from external_intelligence import (  # noqa: E402
    ExternalIntelligenceError,
    ExternalIntelligenceTimeout,
    RuntimeIdentity,
    load_runtime_selection,
    select_runtime_implementation,
    select_runtime_role_profile,
)
import codex_external_intelligence  # noqa: E402
import opencode_external_intelligence  # noqa: E402
from codex_app_server import remove_runtime_root  # noqa: E402


SELECTION_PATH = SUPPORT_ROOT / "external-intelligence-runtime.json"
_CURRENT_SELECTION = load_runtime_selection(SELECTION_PATH)
CURRENT_DRIVER = _CURRENT_SELECTION["driver"]
CURRENT_PROVIDER = _CURRENT_SELECTION["provider"]
CURRENT_TRANSPORT = _CURRENT_SELECTION["transport"]
CURRENT_WORKER_ISOLATION = _CURRENT_SELECTION["worker_isolation"]

_ADAPTERS = {
    codex_external_intelligence.DRIVER: codex_external_intelligence,
    opencode_external_intelligence.DRIVER: opencode_external_intelligence,
}
EXPLICIT_ROLE_KEYS = ("generator", "quality_admission", "semantic", "reader", "judge")
LEGACY_CODEX_DRIVER = codex_external_intelligence.DRIVER


@dataclass(frozen=True)
class RuntimeConfiguration:
    """One explicit provider selection and its local, secret-bearing locators."""

    driver: str
    binary: Path
    credential_file: Path


CurrentRuntimeConfiguration = RuntimeConfiguration


def configuration_from_execution(section: dict[str, Any]) -> RuntimeConfiguration:
    """Translate either the provider-neutral block or the unchanged Codex fields."""
    declared = section.get("external_intelligence")
    if declared is None:
        # The legacy field names are an explicit Codex selection, not an alias
        # for whichever implementation is currently the catalog default.
        driver = LEGACY_CODEX_DRIVER
        binary = section.get("codex_binary")
        credential_file = section.get("codex_auth_file")
    else:
        if not isinstance(declared, dict):
            raise ExternalIntelligenceError("external-intelligence execution configuration is invalid")
        allowed = {"driver", "binary", "credential_file", "roles"}
        if not {"binary", "credential_file"}.issubset(declared) or set(declared) - allowed:
            raise ExternalIntelligenceError("external-intelligence execution configuration fields changed")
        driver = declared.get("driver", CURRENT_DRIVER)
        binary = declared.get("binary")
        credential_file = declared.get("credential_file")
    if not isinstance(driver, str) or not driver.strip():
        raise ExternalIntelligenceError("external-intelligence driver is missing")
    select_runtime_implementation(_CURRENT_SELECTION, driver)
    if not isinstance(binary, str) or not binary.strip():
        raise ExternalIntelligenceError("external-intelligence executable locator is missing")
    if not isinstance(credential_file, str) or not credential_file.strip():
        raise ExternalIntelligenceError("external-intelligence credential locator is missing")
    return RuntimeConfiguration(driver, Path(binary).resolve(), Path(credential_file).resolve())


def role_profile_from_execution(section: dict[str, Any]) -> dict[str, dict[str, str]]:
    """Read explicit roles, with existing Codex fields preserved as the default profile."""
    declared = section.get("external_intelligence")
    if isinstance(declared, dict):
        driver = declared.get("driver", CURRENT_DRIVER)
        roles = declared.get("roles")
        if roles is None:
            return select_runtime_role_profile(_CURRENT_SELECTION, driver)
        if not isinstance(roles, dict) or set(roles) != set(EXPLICIT_ROLE_KEYS):
            raise ExternalIntelligenceError("external-intelligence role profile is invalid")
        result: dict[str, dict[str, str]] = {}
        for role in EXPLICIT_ROLE_KEYS:
            value = roles.get(role)
            if not isinstance(value, dict) or set(value) != {"model", "reasoning_effort"}:
                raise ExternalIntelligenceError(f"external-intelligence {role} role configuration is invalid")
            model, effort = value.get("model"), value.get("reasoning_effort")
            if not isinstance(model, str) or not model.strip() or not isinstance(effort, str) or not effort.strip():
                raise ExternalIntelligenceError(f"external-intelligence {role} role configuration is missing")
            result[role] = {"model": model, "reasoning_effort": effort}
        implementation = selected_implementation(driver if isinstance(driver, str) else None)
        allowed_models = set(implementation["models"])
        allowed_efforts = set(implementation["reasoning_efforts"])
        if allowed_models and any(value["model"].removeprefix(f"{implementation['provider']}/") not in allowed_models for value in result.values()):
            raise ExternalIntelligenceError("external-intelligence role model is not supported by the selected driver")
        if allowed_efforts and any(value["reasoning_effort"] not in allowed_efforts for value in result.values()):
            raise ExternalIntelligenceError("external-intelligence reasoning effort is not supported by the selected driver")
        return result
    mapping = {
        "semantic": ("codex_semantic_model", "codex_semantic_reasoning_effort"),
        "reader": ("codex_reader_model", "codex_reader_reasoning_effort"),
        "judge": ("codex_judge_model", "codex_judge_reasoning_effort"),
    }
    legacy_role_fields = tuple(field for fields in mapping.values() for field in fields)
    present_role_fields = tuple(field for field in legacy_role_fields if field in section)
    if not present_role_fields:
        return select_runtime_role_profile(_CURRENT_SELECTION, LEGACY_CODEX_DRIVER)
    if len(present_role_fields) != len(legacy_role_fields):
        raise ExternalIntelligenceError("legacy Codex role profile is incomplete")
    result = {}
    for role, (model_field, effort_field) in mapping.items():
        model = section.get(model_field)
        effort = section.get(effort_field)
        if not isinstance(model, str) or not model.strip() or not isinstance(effort, str) or not effort.strip():
            raise ExternalIntelligenceError(f"external-intelligence {role} role configuration is missing")
        result[role] = {"model": model, "reasoning_effort": effort}
    return result


def selected_implementation(driver: str | None = None) -> dict[str, Any]:
    return select_runtime_implementation(_CURRENT_SELECTION, driver)


def selected_role_profile(driver: str | None = None) -> dict[str, dict[str, str]]:
    return select_runtime_role_profile(_CURRENT_SELECTION, driver)


def _adapter(driver: str) -> Any:
    select_runtime_implementation(_CURRENT_SELECTION, driver)
    adapter = _ADAPTERS.get(driver)
    if adapter is None:
        raise ExternalIntelligenceError(f"external-intelligence driver has no implementation: {driver}")
    return adapter


def implementation_files(driver: str) -> tuple[Path, ...]:
    return tuple(_adapter(driver).identity_files())


def validate_configuration(configuration: RuntimeConfiguration) -> None:
    try:
        _adapter(configuration.driver).validate(configuration.binary, configuration.credential_file)
    except (OSError, RuntimeError) as error:
        raise ExternalIntelligenceError(str(error)) from error


def probe(configuration: RuntimeConfiguration) -> dict[str, str]:
    try:
        return _adapter(configuration.driver).probe(configuration.binary, configuration.credential_file)
    except (OSError, RuntimeError) as error:
        raise ExternalIntelligenceError(str(error)) from error


def _locator_identity(path: Path) -> str:
    encoded = json.dumps(str(path.resolve()), ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def _implementation_identity(adapter: Any) -> str:
    digest = hashlib.sha256()
    digest.update(Path(__file__).read_bytes())
    digest.update((SUPPORT_ROOT / "external_intelligence.py").read_bytes())
    digest.update(bytes.fromhex(adapter.implementation_sha256()))
    return digest.hexdigest()


def current_runtime_identity(
    *, driver: str, binary: Path, credential_file: Path, max_active: int, worker_processes: int,
) -> dict[str, Any]:
    implementation = selected_implementation(driver)
    adapter = _adapter(driver)
    configuration = RuntimeConfiguration(driver, binary.resolve(), credential_file.resolve())
    validate_configuration(configuration)
    if max_active < 1 or worker_processes != max_active:
        raise ExternalIntelligenceError("external-intelligence driver requires one isolated worker per active turn")
    return RuntimeIdentity(
        driver=driver,
        provider=implementation["provider"],
        transport=implementation["transport"],
        selection_sha256=implementation["selection_sha256"],
        artifact_sha256=adapter.artifact_sha256(configuration.binary),
        implementation_sha256=_implementation_identity(adapter),
        credential_locator_sha256=_locator_identity(configuration.credential_file),
        max_active=max_active,
        worker_processes=worker_processes,
    ).value()


class _StableTransport:
    """Translate provider failures at the port, before the shared retry loop sees them."""

    def __init__(self, transport: Any) -> None:
        self._transport = transport

    @property
    def identity(self) -> dict[str, Any]:
        return self._transport.identity

    def invoke(self, **request: Any) -> Any:
        try:
            return self._transport.invoke(**request)
        except (codex_external_intelligence.AppServerTimeout, opencode_external_intelligence.OpenCodeTimeout) as error:
            raise ExternalIntelligenceTimeout(str(error)) from error
        except (codex_external_intelligence.AppServerError, opencode_external_intelligence.OpenCodeError) as error:
            raise ExternalIntelligenceError(str(error)) from error

    def diagnostics(self) -> dict[str, Any]:
        return self._transport.diagnostics()


@contextmanager
def open_external_intelligence_runtime(
    *, driver: str, binary: Path, credential_file: Path, max_active: int, worker_processes: int,
    runtime_parent: Path,
) -> Iterator[Any]:
    identity = current_runtime_identity(
        driver=driver, binary=binary, credential_file=credential_file,
        max_active=max_active, worker_processes=worker_processes,
    )
    implementation = selected_implementation(driver)
    adapter = _adapter(driver)
    try:
        with adapter.open_runtime(
            binary=binary.resolve(), credential_file=credential_file.resolve(), max_active=max_active,
            runtime_parent=runtime_parent.resolve(), identity=identity, provider=implementation["provider"],
            models=tuple(implementation["models"]), reasoning_efforts=tuple(implementation["reasoning_efforts"]),
        ) as transport:
            yield _StableTransport(transport)
    except (codex_external_intelligence.AppServerTimeout, opencode_external_intelligence.OpenCodeTimeout) as error:
        raise ExternalIntelligenceTimeout(str(error)) from error
    except (codex_external_intelligence.AppServerError, opencode_external_intelligence.OpenCodeError) as error:
        raise ExternalIntelligenceError(str(error)) from error


def clean_stale_runtime_roots(output_dir: Path, *, runtime_dir_name: str = ".external-intelligence-runtime") -> list[str]:
    parent = (output_dir / runtime_dir_name).resolve()
    if not parent.exists():
        return []
    if not parent.is_dir() or parent.parent != output_dir.resolve():
        raise ExternalIntelligenceError("external-intelligence runtime root escapes the evaluation output")
    cleaned: list[str] = []
    prefixes = ("codex-app-server-", "opencode-server-")
    for child in parent.iterdir():
        if not child.is_dir() or not child.name.startswith(prefixes) or child.resolve().parent != parent:
            raise ExternalIntelligenceError(f"unexpected object in external-intelligence runtime root: {child.name}")
        cleaned.append(child.name)
        remove_runtime_root(child)
    parent.rmdir()
    return cleaned
