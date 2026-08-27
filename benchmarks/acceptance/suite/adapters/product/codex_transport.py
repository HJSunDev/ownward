from __future__ import annotations

import os
from pathlib import Path
import shutil


def command_prefix(binary: Path) -> list[str]:
    if binary.suffix.lower() == ".ps1":
        shell = shutil.which("pwsh") or shutil.which("powershell")
        _require(shell is not None, "PowerShell is required to run Codex")
        return [shell, "-NoProfile", "-File", str(binary)]
    _require(binary.suffix.lower() not in {".cmd", ".bat"}, "command wrappers are not accepted as evidence entries")
    return [str(binary)]


def isolated_environment(auth_file: Path, codex_home: Path) -> dict[str, str]:
    codex_home.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(auth_file, codex_home / "auth.json")
    environment = os.environ.copy()
    environment["CODEX_HOME"] = str(codex_home)
    environment.pop("OPENAI_API_KEY", None)
    no_proxy = []
    for name in ("NO_PROXY", "no_proxy"):
        for item in environment.get(name, "").split(","):
            item = item.strip()
            if item and item not in no_proxy:
                no_proxy.append(item)
    for item in ("127.0.0.1", "localhost", "::1"):
        if item not in no_proxy:
            no_proxy.append(item)
    environment["NO_PROXY"] = environment["no_proxy"] = ",".join(no_proxy)
    return environment


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise RuntimeError(message)
