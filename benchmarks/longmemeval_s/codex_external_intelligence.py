from __future__ import annotations

from contextlib import contextmanager
import hashlib
from pathlib import Path
import subprocess
import sys
from typing import Any, Iterator


BENCHMARK_ROOT = Path(__file__).resolve().parent
SUITE_ROOT = BENCHMARK_ROOT.parent / "acceptance" / "suite"
PRODUCT_ADAPTER_ROOT = SUITE_ROOT / "adapters" / "product"
if str(PRODUCT_ADAPTER_ROOT) not in sys.path:
    sys.path.insert(0, str(PRODUCT_ADAPTER_ROOT))

import codex_session  # noqa: E402
from codex_app_server import (  # noqa: E402
    AppServerError,
    AppServerTimeout,
    CodexAppServer,
    CodexAppServerPool,
    isolated_runtime_root,
)


DRIVER = "codex-app-server/v1"


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
    return (
        Path(__file__), BENCHMARK_ROOT / "codex_app_server.py", PRODUCT_ADAPTER_ROOT / "codex_session.py",
        PRODUCT_ADAPTER_ROOT / "codex_transport.py",
    )


def artifact_sha256(binary: Path) -> str:
    return _sha256(binary.resolve())


def validate(binary: Path, credential_file: Path) -> None:
    if not binary.resolve().is_file() or not credential_file.resolve().is_file():
        raise AppServerError("Codex runtime artifacts are incomplete")


def probe(binary: Path, credential_file: Path) -> dict[str, str]:
    validate(binary, credential_file)
    completed = subprocess.run(
        [*CodexAppServer.direct_command_prefix(binary.resolve(), codex_session.command_prefix(binary.resolve())), "--version"],
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        timeout=30,
        check=False,
    )
    if completed.returncode != 0 or not completed.stdout.strip():
        raise AppServerError("Codex runtime cannot start")
    return {"version": completed.stdout.strip(), "artifact_sha256": _sha256(binary.resolve())}


class CodexTransport:
    def __init__(self, pool: CodexAppServerPool, identity: dict[str, Any], provider: str) -> None:
        self._pool = pool
        self._identity = identity
        self._provider = provider

    @property
    def identity(self) -> dict[str, Any]:
        return dict(self._identity)

    def invoke(self, **request: Any) -> tuple[dict[str, Any], dict[str, int], dict[str, Any]]:
        value, usage, metadata = self._pool.invoke(**request)
        return value, usage, {
            **metadata,
            "external_intelligence_driver": DRIVER,
            "external_intelligence_provider": self._provider,
        }

    def diagnostics(self) -> dict[str, Any]:
        return {
            **self._pool.diagnostics(),
            "external_intelligence_driver": DRIVER,
            "external_intelligence_provider": self._provider,
        }


@contextmanager
def open_runtime(
    *, binary: Path, credential_file: Path, max_active: int, runtime_parent: Path,
    identity: dict[str, Any], provider: str, models: tuple[str, ...], reasoning_efforts: tuple[str, ...],
) -> Iterator[CodexTransport]:
    del models, reasoning_efforts  # Codex validates caller-frozen model and effort at its own protocol boundary.
    command_prefix = CodexAppServer.direct_command_prefix(binary.resolve(), codex_session.command_prefix(binary.resolve()))

    def factory(_worker_index: int, _generation: int) -> CodexAppServer:
        runtime_root = isolated_runtime_root(runtime_parent)
        environment = codex_session.isolated_environment(credential_file.resolve(), runtime_root / "codex-home")
        return CodexAppServer(binary.resolve(), credential_file.resolve(), runtime_root, command_prefix, environment)

    with CodexAppServerPool(max_active, factory) as pool:
        yield CodexTransport(pool, identity, provider)


__all__ = [
    "AppServerError", "AppServerTimeout", "CodexTransport", "DRIVER", "artifact_sha256", "identity_files", "implementation_sha256", "open_runtime",
    "probe", "validate",
]
