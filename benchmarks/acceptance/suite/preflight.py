from __future__ import annotations

import hashlib
import json
import shutil
import subprocess
from pathlib import Path
from typing import Any

from materials import load_json


class PreflightError(ValueError):
    pass


def run(
    suite_root: Path,
    repository: Path,
    binary: Path,
    embedding_bundle_dir: Path,
    codex_binary: Path,
    codex_auth_file: Path,
    isolation_dir: Path,
) -> dict[str, Any]:
    repository = repository.resolve()
    binary = binary.resolve()
    embedding_bundle_dir = embedding_bundle_dir.resolve()
    codex_binary = codex_binary.resolve()
    codex_auth_file = codex_auth_file.resolve()
    isolation_dir = isolation_dir.resolve()
    _require(binary.is_file(), "候选二进制不存在")
    _require(codex_binary.is_file(), "外部智能体执行程序不存在")
    _require(codex_auth_file.is_file(), "外部智能体认证文件不存在")
    _require(embedding_bundle_dir.is_dir(), "本地模型能力目录不存在")
    codex_version = subprocess.run(
        [*_command_prefix(codex_binary), "--version"],
        capture_output=True, text=True, encoding="utf-8", timeout=30, check=False,
    )
    _require(codex_version.returncode == 0 and codex_version.stdout.strip(), "外部智能体执行程序不可运行")
    manifest = load_json(embedding_bundle_dir / "manifest.json")
    _require(manifest.get("schema") == "ownward.embedding-bundle/v3", "本地模型能力清单 schema 无效")
    files = {manifest["model"]["path"]: manifest["model"]["sha256"]}
    files.update(manifest["runtime"]["files"])
    for relative, expected in files.items():
        path = embedding_bundle_dir / relative
        _require(path.is_file(), f"本地模型制品不存在: {relative}")
        _require(_sha256(path) == expected, f"本地模型制品摘要不一致: {relative}")
    adapters = load_json(suite_root / "adapters.json")
    community = adapters["layers"]["community"]
    adapter = (suite_root / community["adapter"]).resolve()
    _require(adapter.is_file(), "LongMemEval-V2 适配器不存在")
    source = adapter.read_text(encoding="utf-8")
    revision = community["official_revision"]
    _require(f'OFFICIAL_REVISION = "{revision}"' in source, "LongMemEval-V2 校验路径未绑定固定版本")
    git = shutil.which("git")
    _require(git is not None, "缺少取得官方数据所需的 Git")
    remote = subprocess.run(
        [git, "ls-remote", community["official_repository"]],
        capture_output=True,
        text=True,
        timeout=30,
        check=False,
    )
    _require(remote.returncode == 0, "LongMemEval-V2 官方仓库不可取得")
    _require(revision in remote.stdout, "LongMemEval-V2 固定版本无法从官方仓库取得")
    _require(isolation_dir.drive.upper() != "C:", "验收隔离目录不得位于系统盘")
    _require(not isolation_dir.exists(), "验收隔离目录必须为空白且尚未存在")
    isolation_dir.mkdir(parents=True)
    probe = isolation_dir / ".write-probe"
    probe.write_text("ok", encoding="utf-8")
    probe.unlink()
    isolation_dir.rmdir()
    free_bytes = shutil.disk_usage(isolation_dir.parent).free
    _require(free_bytes >= 20 * 1024**3, "隔离目录可用磁盘空间不足 20 GiB")
    return {
        "schema": "ownward.acceptance-preflight/v1",
        "formal_evidence": False,
        "passed": True,
        "binary_sha256": _sha256(binary),
        "embedding_capability": manifest["capability"],
        "embedding_files": len(files),
        "codex_binary_sha256": _sha256(codex_binary),
        "codex_version": codex_version.stdout.strip(),
        "codex_auth_available": True,
        "official_revision": revision,
        "official_repository_available": True,
        "isolation_root": str(isolation_dir.parent),
        "free_bytes": free_bytes,
        "cost_bound": {"only_multi_hour_step": "LongMemEval-V2", "expected_wall_seconds": [14400, 28800]},
    }


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _command_prefix(path: Path) -> list[str]:
    if path.suffix.lower() == ".ps1":
        shell = shutil.which("pwsh") or shutil.which("powershell")
        _require(shell is not None, "运行 Codex PowerShell 入口需要 PowerShell")
        return [shell, "-NoProfile", "-File", str(path)]
    _require(path.suffix.lower() not in {".cmd", ".bat"}, "预检不接受不可审计的命令包装器")
    return [str(path)]


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise PreflightError(message)
