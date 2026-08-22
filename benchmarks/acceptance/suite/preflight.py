from __future__ import annotations

import shutil
import subprocess
from pathlib import Path
from typing import Any

import binding
import relationships


class PreflightError(ValueError):
    pass


def run(suite_root: Path, config: dict[str, Any], isolation_dir: Path) -> dict[str, Any]:
    try:
        binding.validate_config(config)
        scopes = relationships.enabled_scopes(config)
    except (binding.BindingError, relationships.RelationshipError) as error:
        raise PreflightError(str(error)) from error
    isolation_dir = isolation_dir.resolve()
    _require(isolation_dir.drive.upper() != "C:", "验收隔离目录不得位于系统盘")
    _require(not isolation_dir.exists(), "验收隔离目录必须为空白且尚未存在")
    isolation_dir.mkdir(parents=True)
    probe = isolation_dir / ".write-probe"
    probe.write_text("ok", encoding="utf-8")
    probe.unlink()
    isolation_dir.rmdir()
    free_bytes = shutil.disk_usage(isolation_dir.parent).free

    checks: dict[str, Any] = {}
    if "frontier" in scopes:
        observer = Path(config["frontier"]["tool"]).resolve()
        _require(observer.is_file(), "内核观察器不存在")
        checks["frontier"] = {"observer_sha256": binding.sha256(observer)}
    if set(scopes) & {"core", "product", "community"}:
        binary = Path(config["candidate"]["binary"]).resolve()
        bundle = Path(config["candidate"]["embedding_bundle_dir"]).resolve()
        _require(binary.is_file(), "候选二进制不存在")
        _require(bundle.is_dir(), "本地模型能力目录不存在")
        try:
            embedding = binding._embedding_identity(bundle)
        except binding.BindingError as error:
            raise PreflightError(str(error)) from error
        checks["candidate"] = {
            "binary_sha256": binding.sha256(binary),
            "embedding_capability": embedding.get("capability"),
            "embedding_files": len(embedding["runtime_files"]),
        }
    if "product" in scopes:
        product = config["product"]
        codex = Path(product["codex_binary"]).resolve()
        auth = Path(product["codex_auth_file"]).resolve()
        package = Path(product["package"]).resolve()
        production = Path(product["production_storage_report"]).resolve()
        _require(codex.is_file(), "外部智能体执行程序不存在")
        _require(auth.is_file(), "外部智能体认证文件不存在")
        _require(package.is_dir() and (package / "manifest.json").is_file(), "候选发布包或清单不存在")
        _require(production.is_file(), "生产规模存储证据不存在")
        completed = subprocess.run([*binding._executable_command(codex), "--version"], capture_output=True, text=True, encoding="utf-8", timeout=30, check=False)
        _require(completed.returncode == 0 and completed.stdout.strip(), "外部智能体执行程序不可运行")
        checks["product"] = {"codex_binary_sha256": binding.sha256(codex), "codex_version": completed.stdout.strip(), "codex_auth_available": True}
    if "community" in scopes:
        community = config["community"]
        codex = Path(community["codex_binary"]).resolve()
        auth = Path(community["codex_auth_file"]).resolve()
        _require(codex.is_file(), "社区验收外部智能体执行程序不存在")
        _require(auth.is_file(), "社区验收外部智能体认证文件不存在")
        completed = subprocess.run([*binding._executable_command(codex), "--version"], capture_output=True, text=True, encoding="utf-8", timeout=30, check=False)
        _require(completed.returncode == 0 and completed.stdout.strip(), "社区验收外部智能体执行程序不可运行")
        checks["community"] = _community_preflight(suite_root)
        checks["community"]["codex_binary_sha256"] = binding.sha256(codex)
        checks["community"]["codex_version"] = completed.stdout.strip()
        checks["community"]["codex_auth_available"] = True
        _require(free_bytes >= 20 * 1024**3, "社区验收隔离目录可用磁盘空间不足 20 GiB")

    report: dict[str, Any] = {
        "schema": "ownward.acceptance-preflight/v2",
        "formal_evidence": False,
        "passed": True,
        "enabled_scopes": list(scopes),
        "checks": checks,
        "isolation_root": str(isolation_dir.parent),
        "free_bytes": free_bytes,
    }
    if "community" in scopes:
        report["cost_bound"] = {"only_multi_hour_step": "LongMemEval-V2", "expected_wall_seconds": [14400, 28800]}
    return report


def _community_preflight(suite_root: Path) -> dict[str, Any]:
    adapters = binding.load_json(suite_root / "adapters.json")
    community = adapters["layers"]["community"]
    adapter = (suite_root / community["adapter"]).resolve()
    _require(adapter.is_file(), "LongMemEval-V2 适配器不存在")
    revision = community["official_revision"]
    source = adapter.read_text(encoding="utf-8")
    _require(f'OFFICIAL_REVISION = "{revision}"' in source, "LongMemEval-V2 校验路径未绑定固定版本")
    git = shutil.which("git")
    _require(git is not None, "缺少取得官方数据所需的 Git")
    remote = subprocess.run([git, "ls-remote", community["official_repository"]], capture_output=True, text=True, timeout=30, check=False)
    _require(remote.returncode == 0, "LongMemEval-V2 官方仓库不可取得")
    _require(revision in remote.stdout, "LongMemEval-V2 固定版本无法从官方仓库取得")
    return {"official_revision": revision, "official_repository_available": True}


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise PreflightError(message)
