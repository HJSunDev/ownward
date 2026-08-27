#!/usr/bin/env python3
from __future__ import annotations

import argparse
import ast
import hashlib
import json
import re
import subprocess
import sys
from pathlib import Path
from typing import Any


HERE = Path(__file__).resolve().parent
DEFAULT_REPOSITORY = HERE.parents[3]
SUITE_ROOT = DEFAULT_REPOSITORY / "benchmarks" / "acceptance" / "suite"
sys.path.insert(0, str(SUITE_ROOT))

import binding  # noqa: E402
import lifecycle  # noqa: E402
from contract import load_contract  # noqa: E402
from evidence import validate_report_artifacts  # noqa: E402


class BaselineError(ValueError):
    pass


def canonical_sha256(value: Any) -> str:
    encoded = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def file_sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def load_json(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    _require(isinstance(value, dict), f"{path} 必须是 JSON 对象")
    return value


def confined_path(repository: Path, value: str) -> Path:
    relative = Path(value)
    _require(value.strip() == value and value != "" and not relative.is_absolute(), f"基线路径必须是仓库相对路径: {value!r}")
    resolved = (repository / relative).resolve()
    _require(resolved.is_relative_to(repository.resolve()), f"基线路径越出仓库: {value}")
    return resolved


def verify_manifest_identity(manifest: dict[str, Any]) -> None:
    expected_fields = {
        "schema", "source_snapshot", "direct_files", "active_product", "candidates",
        "retained_artifact_candidates", "acceptance", "responsibility_map", "manifest_sha256",
    }
    _require(set(manifest) == expected_fields, "冻结基线顶层字段无效")
    _require(manifest.get("schema") == "ownward.authority-substrate-migration-baseline/v1", "冻结基线 schema 无效")
    expected = manifest.get("manifest_sha256")
    content = {name: value for name, value in manifest.items() if name != "manifest_sha256"}
    _require(is_sha256(expected) and canonical_sha256(content) == expected, "冻结基线自身摘要无效")


def verify_direct_files(repository: Path, entries: list[dict[str, Any]]) -> None:
    _require(isinstance(entries, list) and entries, "冻结基线缺少直接文件")
    roles: set[str] = set()
    paths: set[str] = set()
    for entry in entries:
        _require(set(entry) == {"role", "path", "bytes", "sha256"}, "直接文件字段无效")
        role, value = entry["role"], entry["path"]
        _require(isinstance(role, str) and role and role not in roles, f"直接文件角色重复: {role!r}")
        _require(isinstance(value, str) and value not in paths, f"直接文件路径重复: {value!r}")
        roles.add(role)
        paths.add(value)
        path = confined_path(repository, value)
        _require(path.is_file(), f"直接依赖缺失: {role} ({value})")
        _require(path.stat().st_size == entry["bytes"], f"直接依赖大小变化: {role}")
        _require(file_sha256(path) == entry["sha256"], f"直接依赖摘要变化: {role}")


def verify_embedding_bundle(repository: Path, spec: dict[str, Any]) -> int:
    root = confined_path(repository, spec["root"])
    manifest_path = root / "manifest.json"
    _require(manifest_path.is_file(), f"向量能力清单缺失: {spec['root']}")
    _require(file_sha256(manifest_path) == spec["manifest_sha256"], f"向量能力清单变化: {spec['root']}")
    manifest = load_json(manifest_path)
    files: dict[str, str] = {}
    model = manifest.get("model")
    runtime = manifest.get("runtime")
    _require(isinstance(model, dict) and isinstance(runtime, dict), "向量能力清单缺少模型或运行时")
    files[str(model.get("path", ""))] = str(model.get("sha256", ""))
    runtime_files = runtime.get("files")
    _require(isinstance(runtime_files, dict) and runtime_files, "向量能力清单缺少运行时文件")
    files.update({str(name): str(digest) for name, digest in runtime_files.items()})
    legal = manifest.get("legal")
    if isinstance(legal, dict):
        legal_files = legal.get("files", {})
        _require(isinstance(legal_files, dict), "向量能力法务文件清单无效")
        files.update({str(name): str(digest) for name, digest in legal_files.items()})
    for relative, expected in files.items():
        _require(relative and is_sha256(expected), f"向量能力文件身份无效: {relative!r}")
        path = (root / relative).resolve()
        _require(path.is_relative_to(root.resolve()) and path.is_file(), f"向量能力文件缺失: {relative}")
        _require(file_sha256(path) == expected, f"向量能力文件摘要变化: {relative}")
    return len(files)


def verify_release_bundle(repository: Path, spec: dict[str, Any]) -> int:
    root = confined_path(repository, spec["root"])
    manifest_path = root / "manifest.json"
    _require(manifest_path.is_file(), f"发布包清单缺失: {spec['root']}")
    _require(file_sha256(manifest_path) == spec["manifest_sha256"], f"发布包清单变化: {spec['root']}")
    manifest = load_json(manifest_path)
    _require(manifest.get("schema") == "ownward.release-bundle/v2", "发布包 schema 无效")
    _require(manifest.get("candidate") == spec["candidate"], "发布包候选身份错绑")
    files = manifest.get("files")
    _require(isinstance(files, dict) and files, "发布包文件清单为空")
    for relative, expected in files.items():
        path = (root / str(relative)).resolve()
        _require(path.is_relative_to(root.resolve()) and path.is_file(), f"发布包文件缺失: {relative}")
        _require(is_sha256(expected) and file_sha256(path) == expected, f"发布包文件摘要变化: {relative}")
    return len(files)


def verify_active_product(repository: Path, spec: dict[str, Any]) -> int:
    config_path = confined_path(repository, spec["config_path"])
    server = load_toml_section(config_path, "mcp_servers.ownward")
    expected_binary = confined_path(repository, spec["binary_path"])
    expected_data = confined_path(repository, spec["data_dir"])
    _require(file_sha256(expected_binary) == spec["binary_sha256"], "活动产品二进制身份变化")
    _require(Path(str(server.get("command", ""))).resolve() == expected_binary, "活动产品配置指向另一二进制")
    _require(Path(str(server.get("cwd", ""))).resolve() == repository.resolve(), "活动产品工作目录变化")
    _require(server.get("enabled") is True and server.get("required") is True, "活动产品 MCP 不再启用或不再必需")
    args = server.get("args")
    _require(isinstance(args, list) and len(args) == 3 and args[:2] == ["mcp", "--data-dir"], "活动产品 MCP 参数变化")
    _require(Path(str(args[2])).resolve() == expected_data, "活动产品数据目录变化")
    completed = subprocess.run([str(expected_binary), "version"], capture_output=True, text=True, encoding="utf-8", timeout=30, check=False)
    _require(completed.returncode == 0 and completed.stdout.strip() == spec["version"], "活动产品版本行为变化")
    build = subprocess.run(["go", "version", "-m", str(expected_binary)], capture_output=True, text=True, encoding="utf-8", timeout=30, check=False)
    _require(build.returncode == 0, "无法读取活动产品构建来源")
    _require(f"vcs.revision={spec['source_revision']}" in build.stdout, "活动产品源码来源变化")
    _require(f"vcs.modified={str(spec['source_modified']).lower()}" in build.stdout, "活动产品工作树来源标记变化")
    return verify_embedding_bundle(repository, spec["embedding_bundle"])


def load_toml_section(path: Path, section: str) -> dict[str, Any]:
    """Parse the frozen scalar/list section without adding a Python package dependency."""
    active = ""
    result: dict[str, Any] = {}
    for raw_line in path.read_text(encoding="utf-8").splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("[") and line.endswith("]"):
            active = line[1:-1].strip()
            continue
        if active != section:
            continue
        _require("=" in line, f"TOML 配置行无效: {line}")
        key, encoded = (part.strip() for part in line.split("=", 1))
        if encoded in {"true", "false"}:
            value: Any = encoded == "true"
        elif encoded.isdecimal():
            value = int(encoded)
        elif encoded.startswith("'") and encoded.endswith("'"):
            value = encoded[1:-1]
        elif encoded.startswith('"') and encoded.endswith('"'):
            value = json.loads(encoded)
        elif encoded.startswith("[") and encoded.endswith("]"):
            matches = re.findall(r"'([^']*)'|\"((?:\\.|[^\"])*)\"", encoded[1:-1])
            value = [single if single != "" else json.loads(f'"{double}"') for single, double in matches]
        else:
            try:
                value = ast.literal_eval(encoded)
            except (SyntaxError, ValueError) as error:
                raise BaselineError(f"TOML 配置值无效: {key}") from error
        result[key] = value
    _require(result, f"TOML 缺少节: {section}")
    return result


def verify_candidate(repository: Path, spec: dict[str, Any]) -> tuple[int, int]:
    binary = confined_path(repository, spec["binary_path"])
    _require(file_sha256(binary) == spec["binary_sha256"], f"候选二进制身份变化: {spec['name']}")
    completed = subprocess.run([str(binary), "version"], capture_output=True, text=True, encoding="utf-8", timeout=30, check=False)
    _require(completed.returncode == 0 and completed.stdout.strip() == spec["candidate"], f"候选版本行为变化: {spec['name']}")
    release_files = verify_release_bundle(repository, spec["release_bundle"])
    embedding_files = verify_embedding_bundle(repository, spec["embedding_bundle"])
    workspace = confined_path(repository, spec["acceptance_workspace"])
    active_binding = binding.load_active_binding(workspace / "binding")
    _require(active_binding.get("candidate") == spec["candidate"], f"候选活动绑定错绑: {spec['name']}")
    for report_spec in spec["reports"].values():
        report_path = confined_path(repository, report_spec["path"])
        _require(file_sha256(report_path) == report_spec["sha256"], f"候选报告摘要变化: {report_path.name}")
        report = load_json(report_path)
        _require(report.get("candidate") == spec["candidate"], f"候选报告错绑: {report_path.name}")
        validate_report_artifacts(report_path, report)
    return release_files, embedding_files


def verify_state_projection(state: dict[str, Any], expected: dict[str, Any]) -> None:
    _require(state.get("schema") == "ownward.acceptance-state/v1", "唯一验收状态 schema 无效")
    _require(state.get("binding", {}).get("candidate") == expected["bound_candidate"], "唯一验收状态绑定另一候选")
    _require((state.get("baseline") is None) == expected["active_baseline_is_null"], "活动基线指针状态变化")
    _require(canonical_sha256(state.get("binding")) == expected["binding_sha256"], "活动候选绑定变化")
    _require(canonical_sha256(state.get("checkpoints")) == expected["checkpoints_sha256"], "有效检查点集合变化")
    _require(state.get("invalidated_reports") == expected["invalidated_reports"], "失效报告登记变化")
    history = state.get("baseline_history")
    _require(isinstance(history, list), "正式基线历史无效")
    actual_history = [canonical_sha256(value) for value in history]
    _require(actual_history == expected["baseline_history_record_sha256"], "正式基线历史变化")


def verify_acceptance(repository: Path, spec: dict[str, Any]) -> int:
    state_path = confined_path(repository, spec["state_path"])
    state = load_json(state_path)
    contract = load_contract(repository / "benchmarks" / "acceptance" / "suite" / "contract.json")
    lifecycle._validate_state(contract, state)
    verify_state_projection(state, spec)
    history_candidates = {value.get("candidate") for value in state["baseline_history"]}
    _require(history_candidates == {spec["formal_baseline_candidate"]}, "正式基线历史包含另一候选")
    checkpoint_candidates = {value.get("binding", {}).get("candidate") for value in state["checkpoints"].values()}
    _require(checkpoint_candidates == {spec["bound_candidate"]}, "有效检查点包含另一候选")
    binding_dir = confined_path(repository, spec["binding_dir"])
    active = binding.load_active_binding(binding_dir)
    _require(active == state["binding"], "活动绑定代次与唯一状态不一致")
    for mode, expected in spec["checkpoints"].items():
        checkpoint = state["checkpoints"].get(mode)
        _require(isinstance(checkpoint, dict), f"缺少有效检查点: {mode}")
        _require(checkpoint.get("passed") is True and checkpoint.get("report_sha256") == expected, f"检查点结果变化: {mode}")
        _require(lifecycle.reusable_report(contract, state, mode) is not None, f"检查点及原始证据不可复用: {mode}")
    return len(spec["checkpoints"])


def verify_retained_candidates(repository: Path, expected: list[dict[str, Any]]) -> None:
    root = repository / ".tmp" / "first-kernel-baseline-v1"
    actual: list[dict[str, Any]] = []
    for directory in sorted(path for path in root.glob("candidate-artifacts*") if path.is_dir()):
        manifest_path = directory / "ownward-windows-amd64" / "manifest.json"
        binary_path = directory / "ownward.exe"
        _require(manifest_path.is_file() and binary_path.is_file(), f"保留候选制品不完整: {directory.name}")
        manifest = load_json(manifest_path)
        actual.append({
            "directory": directory.name,
            "candidate": manifest.get("candidate"),
            "manifest_sha256": file_sha256(manifest_path),
            "binary_sha256": file_sha256(binary_path),
        })
    _require(actual == expected, "保留候选制品集合或身份变化")


def verify_responsibility_map(entries: list[dict[str, Any]]) -> None:
    required = {
        "long-term-assets", "authoritative-control-state", "active-kernel-control",
        "capability-derived-generation", "runtime-retrieval-state", "core-service-orchestration",
        "semantic-capability", "vector-capability", "explicit-assembly", "mcp-access",
        "shared-mcp-runtime", "collaboration-rules", "candidate-release-artifacts",
        "acceptance-binding", "acceptance-evidence-lifecycle",
    }
    fields = {
        "id", "current_owner", "current_paths", "state_class", "write_authority",
        "direct_dependencies", "target_owner", "current_issue", "implicit_semantics", "next_stage",
    }
    _require(isinstance(entries, list), "责任映射必须是数组")
    ids: list[str] = []
    for entry in entries:
        _require(set(entry) == fields, f"责任映射字段无效: {entry.get('id')}")
        ids.append(entry["id"])
        _require(isinstance(entry["current_paths"], list) and entry["current_paths"], f"责任映射缺少当前路径: {entry['id']}")
        _require(isinstance(entry["direct_dependencies"], list), f"责任映射直接依赖无效: {entry['id']}")
        _require(isinstance(entry["target_owner"], str) and entry["target_owner"] not in {"", "unassigned"}, f"责任映射没有唯一目标归属: {entry['id']}")
        _require(isinstance(entry["next_stage"], int) and 2 <= entry["next_stage"] <= 8, f"责任映射后续阶段无效: {entry['id']}")
    _require(len(ids) == len(set(ids)), "责任映射标识重复")
    _require(set(ids) == required, "责任映射覆盖范围不完整")


def verify(repository: Path, manifest_path: Path) -> dict[str, Any]:
    repository = repository.resolve()
    manifest = load_json(manifest_path.resolve())
    verify_manifest_identity(manifest)
    verify_direct_files(repository, manifest["direct_files"])
    _require(manifest["source_snapshot"]["git_commit"] == "a2c8c6deeacbcde03a09fccb22bbc79b7ea3ce58", "迁移源码起点变化")
    source = subprocess.run(
        ["git", "cat-file", "-e", f"{manifest['source_snapshot']['git_commit']}^{{commit}}"],
        cwd=repository, capture_output=True, text=True, encoding="utf-8", timeout=30, check=False,
    )
    _require(source.returncode == 0, "迁移源码起点提交不存在")
    active_embedding_files = verify_active_product(repository, manifest["active_product"])
    release_files = 0
    candidate_embedding_files = 0
    for name in ("v0", "v1"):
        release_count, embedding_count = verify_candidate(repository, manifest["candidates"][name])
        release_files += release_count
        candidate_embedding_files += embedding_count
    verify_retained_candidates(repository, manifest["retained_artifact_candidates"])
    checkpoint_count = verify_acceptance(repository, manifest["acceptance"])
    verify_responsibility_map(manifest["responsibility_map"])
    return {
        "schema": "ownward.authority-substrate-migration-baseline-check/v1",
        "passed": True,
        "manifest_sha256": manifest["manifest_sha256"],
        "source_snapshot": manifest["source_snapshot"]["git_commit"],
        "active_product_binary_sha256": manifest["active_product"]["binary_sha256"],
        "formal_baseline": manifest["candidates"]["v0"]["candidate"],
        "validated_unpromoted_candidate": manifest["candidates"]["v1"]["candidate"],
        "other_valid_candidates": [],
        "checked_direct_files": len(manifest["direct_files"]),
        "checked_embedding_files": active_embedding_files + candidate_embedding_files,
        "checked_release_files": release_files,
        "checked_acceptance_checkpoints": checkpoint_count,
        "checked_responsibilities": len(manifest["responsibility_map"]),
    }


def is_sha256(value: Any) -> bool:
    return isinstance(value, str) and len(value) == 64 and all(character in "0123456789abcdef" for character in value)


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise BaselineError(message)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="只读校验权威基座—能力世代迁移冻结起点")
    parser.add_argument("--repository", type=Path, default=DEFAULT_REPOSITORY)
    parser.add_argument("--manifest", type=Path, default=HERE / "frozen-baseline.json")
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    print(json.dumps(verify(args.repository, args.manifest), ensure_ascii=False, sort_keys=True, separators=(",", ":")))


if __name__ == "__main__":
    try:
        main()
    except (BaselineError, binding.BindingError, lifecycle.LifecycleError, OSError, ValueError, json.JSONDecodeError, subprocess.SubprocessError) as error:
        print(f"frozen-baseline: {error}", file=sys.stderr)
        raise SystemExit(2)
