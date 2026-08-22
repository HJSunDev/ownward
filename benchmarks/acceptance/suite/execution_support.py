from __future__ import annotations

import json
from pathlib import Path
import shutil
from typing import Any

import process_control


class ExecutionError(ValueError):
    pass


def load_json(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    require(isinstance(value, dict), f"JSON 文档必须是对象: {path}")
    return value


def run(command: list[str], *, cwd: Path, timeout: float) -> None:
    try:
        completed = process_control.run(command, cwd=cwd, timeout=timeout)
    except process_control.ProcessTimeout as error:
        raise ExecutionError(f"验收执行超过 {timeout:.0f} 秒上限，已停止") from error
    require(completed.returncode == 0, f"验收执行失败: {completed.stderr[-3000:]}")


def safe_remove(path: Path, parent: Path) -> None:
    if not path.exists():
        return
    resolved = path.resolve()
    require(resolved.parent == parent.resolve(), f"拒绝清理非预期路径: {resolved}")
    if resolved.is_dir():
        shutil.rmtree(resolved)
    else:
        resolved.unlink()


def mapping(value: dict[str, Any], name: str) -> dict[str, Any]:
    nested = value.get(name)
    require(isinstance(nested, dict), f"执行配置缺少 {name}")
    return nested


def require(condition: bool, message: str) -> None:
    if not condition:
        raise ExecutionError(message)
