from __future__ import annotations

from contextlib import contextmanager
from dataclasses import dataclass
import hashlib
import json
from pathlib import Path
import subprocess
import sys
from typing import Any, Iterator


BENCHMARK_ROOT = Path(__file__).resolve().parent
SUPPORT_ROOT = BENCHMARK_ROOT.parent / "support"
SUITE_ROOT = BENCHMARK_ROOT.parent / "acceptance" / "suite"
PRODUCT_ADAPTER_ROOT = SUITE_ROOT / "adapters" / "product"
for dependency_root in (SUPPORT_ROOT, PRODUCT_ADAPTER_ROOT):
    if str(dependency_root) not in sys.path:
        sys.path.insert(0, str(dependency_root))

from external_intelligence import (  # noqa: E402
    ExternalIntelligenceError,
    ExternalIntelligenceTimeout,
    RuntimeIdentity,
    load_runtime_selection,
)
import codex_session  # noqa: E402
from codex_app_server import (  # noqa: E402
    AppServerError,
    AppServerTimeout,
    CodexAppServer,
    CodexAppServerPool,
    isolated_runtime_root,
    remove_runtime_root,
)


SELECTION_PATH = SUPPORT_ROOT / "external-intelligence-runtime.json"
_CURRENT_SELECTION = load_runtime_selection(SELECTION_PATH)
CURRENT_DRIVER = _CURRENT_SELECTION["driver"]
CURRENT_PROVIDER = _CURRENT_SELECTION["provider"]
CURRENT_TRANSPORT = _CURRENT_SELECTION["transport"]
CURRENT_WORKER_ISOLATION = _CURRENT_SELECTION["worker_isolation"]

if (
    CURRENT_DRIVER != "codex-app-server/v1"
    or CURRENT_PROVIDER != "openai-codex"
    or CURRENT_TRANSPORT != "persistent-independent-worker-pool/v1"
    or CURRENT_WORKER_ISOLATION != "one-active-turn-per-worker"
):
    raise ExternalIntelligenceError("runtime selection is incompatible with the Codex App Server adapter")


@dataclass(frozen=True)
class CurrentRuntimeConfiguration:
    """Current provider configuration after the adapter consumes legacy field names."""

    binary: Path
    credential_file: Path


def configuration_from_execution(section: dict[str, Any]) -> CurrentRuntimeConfiguration:
    """Translate the current execution document at the provider boundary."""
    binary = section.get("codex_binary")
    credential_file = section.get("codex_auth_file")
    if not isinstance(binary, str) or not binary.strip():
        raise ExternalIntelligenceError("current external-intelligence executable locator is missing")
    if not isinstance(credential_file, str) or not credential_file.strip():
        raise ExternalIntelligenceError("current external-intelligence credential locator is missing")
    return CurrentRuntimeConfiguration(Path(binary).resolve(), Path(credential_file).resolve())


def role_profile_from_execution(section: dict[str, Any]) -> dict[str, dict[str, str]]:
    """Translate current provider role settings without exposing their field names to orchestration."""
    mapping = {
        "semantic": ("codex_semantic_model", "codex_semantic_reasoning_effort"),
        "reader": ("codex_reader_model", "codex_reader_reasoning_effort"),
        "judge": ("codex_judge_model", "codex_judge_reasoning_effort"),
    }
    result: dict[str, dict[str, str]] = {}
    for role, (model_field, effort_field) in mapping.items():
        model = section.get(model_field)
        effort = section.get(effort_field)
        if not isinstance(model, str) or not model.strip() or not isinstance(effort, str) or not effort.strip():
            raise ExternalIntelligenceError(f"current external-intelligence {role} role configuration is missing")
        result[role] = {"model": model, "reasoning_effort": effort}
    return result


def validate_configuration(configuration: CurrentRuntimeConfiguration) -> None:
    if not configuration.binary.is_file() or not configuration.credential_file.is_file():
        raise ExternalIntelligenceError("external-intelligence runtime artifacts are incomplete")


def probe(configuration: CurrentRuntimeConfiguration) -> dict[str, str]:
    """Run the current provider's lightweight availability probe inside its adapter."""
    validate_configuration(configuration)
    completed = subprocess.run(
        [*CodexAppServer.direct_command_prefix(
            configuration.binary,
            codex_session.command_prefix(configuration.binary),
        ), "--version"],
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        timeout=30,
        check=False,
    )
    if completed.returncode != 0 or not completed.stdout.strip():
        raise ExternalIntelligenceError("current external-intelligence runtime cannot start")
    return {"version": completed.stdout.strip(), "artifact_sha256": _sha256(configuration.binary)}


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _locator_identity(path: Path) -> str:
    encoded = json.dumps(str(path.resolve()), ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


class CodexAppServerAdapter:
    """The current provider adapter; evaluation orchestration never imports Codex transport types."""

    def __init__(self, pool: CodexAppServerPool, identity: dict[str, Any]) -> None:
        self._pool = pool
        self._identity = identity

    @property
    def identity(self) -> dict[str, Any]:
        return dict(self._identity)

    def invoke(self, **request: Any) -> tuple[dict[str, Any], dict[str, int], dict[str, Any]]:
        try:
            value, usage, metadata = self._pool.invoke(**request)
        except AppServerTimeout as error:
            raise ExternalIntelligenceTimeout(str(error)) from error
        except AppServerError as error:
            raise ExternalIntelligenceError(str(error)) from error
        return value, usage, {
            **metadata,
            "external_intelligence_driver": CURRENT_DRIVER,
            "external_intelligence_provider": CURRENT_PROVIDER,
        }

    def diagnostics(self) -> dict[str, Any]:
        return {
            **self._pool.diagnostics(),
            "external_intelligence_driver": CURRENT_DRIVER,
            "external_intelligence_provider": CURRENT_PROVIDER,
        }


def current_runtime_identity(
    *,
    driver: str,
    binary: Path,
    credential_file: Path,
    max_active: int,
    worker_processes: int,
) -> dict[str, Any]:
    if driver != CURRENT_DRIVER:
        raise ExternalIntelligenceError(f"unsupported external-intelligence driver: {driver}")
    binary = binary.resolve()
    credential_file = credential_file.resolve()
    validate_configuration(CurrentRuntimeConfiguration(binary, credential_file))
    if max_active < 1 or worker_processes != max_active:
        raise ExternalIntelligenceError("current external-intelligence driver requires one isolated worker per active turn")
    return RuntimeIdentity(
        driver=driver,
        provider=CURRENT_PROVIDER,
        transport=CURRENT_TRANSPORT,
        selection_sha256=_CURRENT_SELECTION["selection_sha256"],
        artifact_sha256=_sha256(binary),
        credential_locator_sha256=_locator_identity(credential_file),
        max_active=max_active,
        worker_processes=worker_processes,
    ).value()


@contextmanager
def open_external_intelligence_runtime(
    *,
    driver: str,
    binary: Path,
    credential_file: Path,
    max_active: int,
    worker_processes: int,
    runtime_parent: Path,
) -> Iterator[CodexAppServerAdapter]:
    identity = current_runtime_identity(
        driver=driver,
        binary=binary,
        credential_file=credential_file,
        max_active=max_active,
        worker_processes=worker_processes,
    )
    command_prefix = CodexAppServer.direct_command_prefix(binary.resolve(), codex_session.command_prefix(binary.resolve()))

    def factory(_worker_index: int, _generation: int) -> CodexAppServer:
        runtime_root = isolated_runtime_root(runtime_parent)
        environment = codex_session.isolated_environment(credential_file.resolve(), runtime_root / "codex-home")
        return CodexAppServer(binary.resolve(), credential_file.resolve(), runtime_root, command_prefix, environment)

    try:
        with CodexAppServerPool(max_active, factory) as pool:
            yield CodexAppServerAdapter(pool, identity)
    except AppServerTimeout as error:
        raise ExternalIntelligenceTimeout(str(error)) from error
    except AppServerError as error:
        raise ExternalIntelligenceError(str(error)) from error


def clean_stale_runtime_roots(output_dir: Path, *, runtime_dir_name: str = ".external-intelligence-runtime") -> list[str]:
    parent = (output_dir / runtime_dir_name).resolve()
    if not parent.exists():
        return []
    if not parent.is_dir() or parent.parent != output_dir.resolve():
        raise ExternalIntelligenceError("external-intelligence runtime root escapes the evaluation output")
    cleaned: list[str] = []
    for child in parent.iterdir():
        if not child.is_dir() or not child.name.startswith("codex-app-server-") or child.resolve().parent != parent:
            raise ExternalIntelligenceError(f"unexpected object in external-intelligence runtime root: {child.name}")
        cleaned.append(child.name)
        remove_runtime_root(child)
    parent.rmdir()
    return cleaned
