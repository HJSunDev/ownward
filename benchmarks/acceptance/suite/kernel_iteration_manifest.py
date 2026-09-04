from __future__ import annotations

import json
from pathlib import Path
from typing import Any


SCHEMA = "ownward.kernel-iteration-manifest/v1"
CURRENT_SCHEMA = "ownward.kernel-iteration-current/v1"


def load(suite_root: Path, path: Path | None = None) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    if path is None:
        current = _read(suite_root / "iteration" / "current.json")
        _require(current.get("schema") == CURRENT_SCHEMA, "current kernel-iteration manifest pointer is invalid")
        path = suite_root / str(current.get("manifest", ""))
    path = path.resolve()
    _require(path.is_relative_to(suite_root / "iteration") and path.is_file(), "kernel-iteration manifest escapes the iteration root")
    value = _read(path)
    _require(value.get("schema") == SCHEMA, "kernel-iteration manifest schema is invalid")
    _require(isinstance(value.get("major_version"), str) and value["major_version"], "kernel-iteration major version is missing")
    paths = value.get("paths")
    required = {"validation_contract", "blind_budget", "comparison_contract"}
    optional = {"measurement_rejudgment"}
    _require(
        isinstance(paths, dict)
        and required <= set(paths)
        and set(paths) <= required | optional,
        "kernel-iteration manifest paths are incomplete",
    )
    resolved: dict[str, str] = {}
    for name, relative in paths.items():
        target = (suite_root / str(relative)).resolve()
        _require(target.is_relative_to(suite_root / "iteration"), f"kernel-iteration manifest path is invalid: {name}")
        resolved[name] = str(target)
    return {**value, "manifest_path": str(path), "resolved_paths": resolved}


def path(suite_root: Path, name: str, manifest: dict[str, Any] | None = None) -> Path:
    selected = manifest or load(suite_root)
    value = selected.get("resolved_paths", {}).get(name)
    _require(isinstance(value, str), f"kernel-iteration manifest has no path: {name}")
    result = Path(value)
    _require(result.is_file(), f"kernel-iteration manifest path does not exist: {name}")
    return result


def _read(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    _require(isinstance(value, dict), f"JSON must be an object: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise ValueError(message)
