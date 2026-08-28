from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import sys
import time
from typing import Any

import evidence_identity
import report_relationships as relationships


class BindingError(ValueError):
    pass


TARGET_STAGES = set(relationships.TARGET_STAGES)
SCOPE_NAMES = set(relationships.SCOPE_CONFIG)
MODE_SCOPES = relationships.MODE_SCOPES
ACTIVE_CODEX_MODEL = "gpt-5.4-mini"
ACTIVE_CODEX_REASONING_EFFORT = "xhigh"
LONGMEMEVAL_S_ENVIRONMENT_SCHEMA = "ownward.longmemeval-s-environment/v1"
LONGMEMEVAL_S_CODE_REVISION = "9e0b455f4ef0e2ab8f2e582289761153549043fc"
LONGMEMEVAL_S_DATA_REVISION = "98d7416c24c778c2fee6e6f3006e7a073259d48f"
LONGMEMEVAL_S_DATA_SHA256 = "d6f21ea9d60a0d56f34a05b609c79c88a451d2ae03597821ea3d5a9678c3a442"


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def load_json(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    _require(isinstance(value, dict), f"JSON 文档必须是对象: {path}")
    return value


def create(suite_root: Path, config_path: Path, output_dir: Path) -> dict[str, Any]:
    config = load_json(config_path.resolve())
    validate_config(config)
    scopes_to_bind = relationships.enabled_scopes(config)
    repository = Path(config["repository"]).resolve()
    workspace = Path(config["workspace"]).resolve()
    output_dir = output_dir.resolve()
    _require(Path(config["binding_dir"]).resolve() == output_dir, "输出目录必须与执行配置 binding_dir 一致")
    _require_isolated_path(workspace, "验收工作区")
    _require_isolated_path(output_dir, "绑定清单")
    _require_clean_tool_repository(suite_root)
    candidate = _git(repository, "rev-parse", "HEAD")
    _require(not _git(repository, "status", "--porcelain"), "候选仓库不是干净、冻结的提交")
    if "frontier" in scopes_to_bind:
        observer = Path(_mapping(config, "frontier")["tool"]).resolve()
        _require(observer.is_file(), f"内核观察器不存在: {observer}")
        _verify_go_binary(observer, candidate)
    if set(scopes_to_bind) & {"core", "product", "community"}:
        binary, embedding = _candidate_paths(config)
        _require(binary.is_file(), f"候选二进制不存在: {binary}")
        _require(embedding.is_dir(), "本地向量能力包不存在")
        _require(embedding == (binary.parent / "embedding").resolve(), "向量能力包必须是候选二进制相邻的正式产品制品")
        version = subprocess.run([str(binary), "version"], cwd=repository, capture_output=True, text=True, encoding="utf-8", timeout=30, check=False)
        _require(version.returncode == 0 and version.stdout.strip() == candidate, "候选二进制没有绑定候选提交")
        _verify_go_binary(binary, candidate)
    if "product" in scopes_to_bind:
        product = _mapping(config, "product")
        package = Path(product["package"]).resolve()
        production = Path(product["production_storage_report"]).resolve()
        codex = Path(product["codex_binary"]).resolve()
        auth = Path(product["codex_auth_file"]).resolve()
        _require(package.is_dir() and (package / "manifest.json").is_file(), "候选发布包或清单不存在")
        _require(production.is_file(), "生产规模存储证据不存在")
        _require(codex.is_file(), "Codex 不存在")
        _require(auth.is_file(), "Codex 认证文件不存在")
    if "community" in scopes_to_bind:
        _validate_community_workspace(config, workspace)
    output_dir.mkdir(parents=True, exist_ok=True)
    hash_workspace = output_dir / ".binding-hash"
    if hash_workspace.exists():
        shutil.rmtree(hash_workspace)
    scopes: dict[str, dict[str, str]] = {}
    manifest_values: dict[str, dict[str, Any]] = {}
    for scope in scopes_to_bind:
        manifests = {
            "environment": _environment_manifest(config, scope),
            "inputs": _input_manifest(suite_root, config, scope),
            "tools": _tool_manifest(suite_root, scope),
        }
        manifest_values.update({f"{scope}-{name}.json": value for name, value in manifests.items()})
        paths = {name: output_dir / ".binding-hash" / f"{scope}-{name}.json" for name in manifests}
        for name, value in manifests.items():
            _write_json(paths[name], value)
        scopes[scope] = {
            "environment_sha256": sha256(paths["environment"]),
            "input_manifest_sha256": sha256(paths["inputs"]),
            "tool_sha256": sha256(paths["tools"]),
            "artifact_sha256": _artifact_sha256(config, scope),
        }
    legacy = {"schema": "ownward.acceptance-binding/v4", "suite_version": "1.0.0", "candidate": candidate, "scopes": scopes}
    components = evidence_identity.build_candidate_components(
        repository, candidate, _candidate_binary_sha(config, scopes_to_bind), _release_manifest_sha(config),
    )
    result = evidence_identity.build_binding(
        legacy, components, manifest_values, evidence_identity.lifecycle_identities(repository),
        evidence_identity.reporting_identities(repository),
    )
    validate_binding(result)
    shutil.rmtree(output_dir / ".binding-hash")
    _activate_generation(output_dir, result, manifest_values)
    return result


def rebind_scope(suite_root: Path, config_path: Path, output_dir: Path, scope: str) -> dict[str, Any]:
    """Rebind one acceptance-tool scope without changing the frozen product candidate."""
    _require(scope in SCOPE_NAMES, f"未知绑定范围: {scope}")
    config = load_json(config_path.resolve())
    validate_config(config)
    _require(scope in relationships.enabled_scopes(config), f"当前配置未启用 {scope}")
    output_dir = output_dir.resolve()
    _require(Path(config["binding_dir"]).resolve() == output_dir, "输出目录必须与执行配置 binding_dir 一致")
    _require_clean_tool_repository(suite_root)
    active_dir = _active_generation_dir(output_dir)
    previous = load_json(active_dir / "binding.json")
    validate_binding(previous)
    repository = Path(config["repository"]).resolve()
    _require(not _git(repository, "status", "--porcelain"), "验收工具仓库不是干净、冻结的提交")
    candidate = evidence_identity.source_git(previous)
    _git(repository, "cat-file", "-e", candidate + "^{commit}")
    if scope == "frontier":
        _verify_go_binary(Path(_mapping(config, "frontier")["tool"]).resolve(), candidate)
    else:
        binary, embedding = _candidate_paths(config)
        _require(binary.is_file() and embedding.is_dir(), "冻结候选制品不完整")
        version = subprocess.run([str(binary), "version"], cwd=repository, capture_output=True, text=True, encoding="utf-8", timeout=30, check=False)
        _require(version.returncode == 0 and version.stdout.strip() == candidate, "候选二进制没有绑定既有冻结提交")
        _verify_go_binary(binary, candidate)

    manifest_values: dict[str, dict[str, Any]] = {}
    scopes = {name: dict(value) for name, value in previous["scopes"].items()}
    for name in previous["scopes"]:
        for kind in ("environment", "inputs", "tools"):
            filename = f"{name}-{kind}.json"
            manifest_values[filename] = load_json(active_dir / filename)
    if active_dir == output_dir:
        # 旧版根目录清单可能使用平台换行，摘要绑定的是原始字节；必须逐字节固化，
        # 不能重新序列化。后续资源复核仍需按旧工具摘要找到真实测量依赖。
        _preserve_legacy_generation(output_dir, previous)
        active_dir = _active_generation_dir(output_dir)
    replacement = {
        "environment": _environment_manifest(config, scope),
        "inputs": _input_manifest(suite_root, config, scope),
        "tools": _tool_manifest(suite_root, scope),
    }
    manifest_values.update({f"{scope}-{name}.json": value for name, value in replacement.items()})
    hashes = {name: _serialized_json_sha256(value) for name, value in replacement.items()}
    artifact_sha256 = _artifact_sha256(config, scope)
    if previous.get("schema") == evidence_identity.BINDING_SCHEMA:
        current_lifecycle = evidence_identity.lifecycle_identities(repository)
        current_reporting = evidence_identity.reporting_identities(repository)
        for name, value in scopes.items():
            direct = dict(value["direct_dependencies"])
            direct["report-reception"] = current_reporting["reception"]["identity"]
            direct["relationship-execution"] = current_reporting["relationships"]["identity"]
            value["direct_dependencies"] = dict(sorted(direct.items()))
            value["identity"] = evidence_identity.dependency_identity(name, direct)
        prior = scopes[scope]
        direct_dependencies = dict(prior["direct_dependencies"])
        direct_dependencies["environment"] = hashes["environment"]
        direct_dependencies["input"] = hashes["inputs"]
        direct_dependencies["acceptance-tool"] = evidence_identity.tool_identity(replacement["tools"])
        if scope == "frontier":
            direct_dependencies["observer"] = artifact_sha256
        else:
            _require(artifact_sha256 == previous["components"]["binary"]["identity"], "scope rebind 不得改变冻结候选二进制")
        report_tool = prior["report_binding"]["tool_sha256"]
        if direct_dependencies["acceptance-tool"] != prior["direct_dependencies"]["acceptance-tool"]:
            report_tool = hashes["tools"]
        scopes[scope] = {
            "identity": evidence_identity.dependency_identity(scope, direct_dependencies),
            "direct_dependencies": dict(sorted(direct_dependencies.items())),
            "report_binding": {
                "environment_sha256": hashes["environment"],
                "input_manifest_sha256": hashes["inputs"],
                "tool_sha256": report_tool,
                "artifact_sha256": artifact_sha256,
            },
        }
        result = {
            "schema": previous["schema"], "suite_version": previous["suite_version"],
            "product": previous["product"], "components": previous["components"],
            "lifecycle": current_lifecycle,
            "reporting": current_reporting,
            "scopes": scopes, "audit": previous["audit"],
        }
    else:
        scopes[scope] = {
            "environment_sha256": hashes["environment"],
            "input_manifest_sha256": hashes["inputs"],
            "tool_sha256": hashes["tools"],
            "artifact_sha256": artifact_sha256,
        }
        result = {"schema": "ownward.acceptance-binding/v4", "suite_version": previous["suite_version"], "candidate": candidate, "scopes": scopes}
    validate_binding(result)
    preserved_sources = {
        filename: active_dir / filename
        for filename in manifest_values
        if not filename.startswith(f"{scope}-")
    }
    _activate_generation(output_dir, result, manifest_values, raw_sources=preserved_sources)
    return result


def load_active_binding(path: Path) -> dict[str, Any]:
    path = path.resolve()
    binding_dir = path if path.is_dir() else path.parent
    active = _active_generation_dir(binding_dir)
    return load_json(active / "binding.json")


def _active_generation_dir(binding_dir: Path) -> Path:
    active_path = binding_dir / "active.json"
    if not active_path.is_file():
        _validate_generation(binding_dir)
        return binding_dir
    active = load_json(active_path)
    _require(active.get("schema") == "ownward.acceptance-binding-active/v1", "活动绑定指针无效")
    generation = str(active.get("generation", ""))
    _require(generation and Path(generation).name == generation, "活动绑定代次无效")
    root = (binding_dir / "generations" / generation).resolve()
    _require(root.is_relative_to(binding_dir.resolve()) and root.is_dir(), "活动绑定代次不存在")
    _require(sha256(root / "binding.json") == active.get("binding_sha256"), "活动绑定指针摘要不一致")
    _validate_generation(root)
    return root


def _activate_generation(
    output_dir: Path,
    binding: dict[str, Any],
    manifests: dict[str, dict[str, Any]],
    *,
    raw_sources: dict[str, Path] | None = None,
) -> None:
    generation = _stage_generation(output_dir, binding, manifests, raw_sources=raw_sources)
    _publish_generation(output_dir, generation)


def _stage_generation(
    output_dir: Path,
    binding: dict[str, Any],
    manifests: dict[str, dict[str, Any]],
    *,
    raw_sources: dict[str, Path] | None = None,
) -> str:
    """Persist an immutable generation without changing the active pointer."""
    generation = _canonical_sha256({"binding": binding, "manifests": manifests})[:24]
    generations = output_dir / "generations"
    generations.mkdir(parents=True, exist_ok=True)
    destination = generations / generation
    if not destination.exists():
        temporary = generations / f".tmp-{os.getpid()}-{time.time_ns()}"
        try:
            temporary.mkdir()
            _write_json(temporary / "binding.json", binding)
            for filename, value in manifests.items():
                source = (raw_sources or {}).get(filename)
                if source is None:
                    _write_json(temporary / filename, value)
                else:
                    _copy_exact(source, temporary / filename)
            _validate_generation(temporary, expected_binding=binding, expected_manifests=manifests)
            temporary.replace(destination)
        finally:
            if temporary.exists():
                shutil.rmtree(temporary)
    _validate_generation(destination, expected_binding=binding, expected_manifests=manifests)
    return generation


def _publish_generation(output_dir: Path, generation: str) -> None:
    """Atomically select one already validated immutable generation."""
    destination = output_dir / "generations" / generation
    _validate_generation(destination)
    binding = load_json(destination / "binding.json")
    active = {"schema": "ownward.acceptance-binding-active/v1", "generation": generation, "binding_sha256": sha256(destination / "binding.json")}
    _write_json(output_dir / "active.json", active)
    # Compatibility mirrors are not authoritative; readers resolve active.json first.
    _write_json(output_dir / "binding.json", binding)
    for scope in binding["scopes"]:
        for kind in ("environment", "inputs", "tools"):
            filename = f"{scope}-{kind}.json"
            _copy_exact(destination / filename, output_dir / filename)


def _preserve_legacy_generation(output_dir: Path, binding: dict[str, Any]) -> None:
    filenames = ["binding.json"] + [
        f"{scope}-{kind}.json"
        for scope in binding["scopes"]
        for kind in ("environment", "inputs", "tools")
    ]
    hashes = {filename: sha256(output_dir / filename) for filename in filenames}
    generation = _canonical_sha256({"schema": "ownward.acceptance-legacy-binding-files/v1", "files": hashes})[:24]
    generations = output_dir / "generations"
    generations.mkdir(parents=True, exist_ok=True)
    destination = generations / generation
    if not destination.exists():
        temporary = generations / f".tmp-{os.getpid()}-{time.time_ns()}"
        try:
            temporary.mkdir()
            for filename in filenames:
                _copy_exact(output_dir / filename, temporary / filename)
            _validate_generation(temporary, expected_binding=binding)
            temporary.replace(destination)
        finally:
            if temporary.exists():
                shutil.rmtree(temporary)
    _validate_generation(destination, expected_binding=binding)
    active = {"schema": "ownward.acceptance-binding-active/v1", "generation": generation, "binding_sha256": sha256(destination / "binding.json")}
    _write_json(output_dir / "active.json", active)


def _copy_exact(source: Path, target: Path) -> None:
    with target.open("wb") as stream:
        stream.write(source.read_bytes())
        stream.flush()
        os.fsync(stream.fileno())


def _validate_generation(
    directory: Path,
    *,
    expected_binding: dict[str, Any] | None = None,
    expected_manifests: dict[str, dict[str, Any]] | None = None,
) -> None:
    binding = load_json(directory / "binding.json")
    validate_binding(binding)
    if expected_binding is not None:
        _require(binding == expected_binding, "不可变绑定代次的候选身份不一致")
    for scope, identity in binding["scopes"].items():
        report_identity = identity["report_binding"] if binding.get("schema") == evidence_identity.BINDING_SCHEMA else identity
        for kind, field in (
            ("environment", "environment_sha256"),
            ("inputs", "input_manifest_sha256"),
            ("tools", "tool_sha256"),
        ):
            filename = f"{scope}-{kind}.json"
            path = directory / filename
            _require(path.is_file(), f"不可变绑定代次的 {filename} 缺失")
            if binding.get("schema") == evidence_identity.BINDING_SCHEMA and kind == "tools":
                _require(
                    evidence_identity.tool_manifest_identity_valid(load_json(path), identity["direct_dependencies"]["acceptance-tool"]),
                    f"不可变绑定代次的 {filename} 执行身份不一致",
                )
            else:
                _require(sha256(path) == report_identity[field], f"不可变绑定代次的 {filename} 摘要不一致")
            if expected_manifests is not None:
                _require(load_json(path) == expected_manifests[filename], f"不可变绑定代次的 {filename} 内容不一致")


def validate_config(config: dict[str, Any]) -> None:
    _require(config.get("schema") == "ownward.acceptance-execution/v3", "执行配置 schema 无效")
    for name in ("repository", "workspace", "binding_dir"):
        _require(isinstance(config.get(name), str) and config[name].strip(), f"执行配置缺少 {name}")
    enabled = relationships.enabled_scopes(config)
    if "frontier" in enabled:
        frontier = _mapping(config, "frontier")
        _require(isinstance(frontier.get("tool"), str) and frontier["tool"].strip(), "执行配置缺少 frontier.tool")
        stages = frontier.get("targeted_stages", [])
        _require(isinstance(stages, list) and all(isinstance(item, str) for item in stages), "frontier.targeted_stages 必须是字符串数组")
        _require(len(stages) == len(set(stages)) and set(stages) <= TARGET_STAGES, "frontier.targeted_stages 包含重复或未知阶段")
    if set(enabled) & {"core", "product", "community"}:
        candidate = _mapping(config, "candidate")
        for name in ("binary", "embedding_bundle_dir"):
            _require(isinstance(candidate.get(name), str) and candidate[name].strip(), f"执行配置缺少 candidate.{name}")
    if "product" in enabled:
        product = _mapping(config, "product")
        for name in ("package", "production_storage_report", "codex_binary", "codex_auth_file", "codex_model", "codex_reasoning_effort"):
            _require(isinstance(product.get(name), str) and product[name].strip(), f"执行配置缺少 product.{name}")
        _require(product["codex_model"] == ACTIVE_CODEX_MODEL, "专项集必须使用固定外部智能体模型")
        _require(product["codex_reasoning_effort"] == ACTIVE_CODEX_REASONING_EFFORT, "专项集必须使用固定外部智能体推理强度")
    if "community" in enabled:
        _validate_community_config(_mapping(config, "community"))


def validate_binding(value: dict[str, Any]) -> None:
    if isinstance(value, dict) and value.get("schema") == evidence_identity.BINDING_SCHEMA:
        try:
            evidence_identity.validate_binding(value)
        except evidence_identity.EvidenceIdentityError as error:
            raise BindingError(str(error)) from error
        return
    _require(isinstance(value, dict), "候选绑定必须是对象")
    _require(set(value) == {"schema", "suite_version", "candidate", "scopes"}, "候选绑定顶层字段无效")
    _require(value.get("schema") == "ownward.acceptance-binding/v4", "候选绑定 schema 无效")
    _require(value.get("suite_version") == "1.0.0", "候选绑定体系版本无效")
    candidate = value.get("candidate")
    _require(isinstance(candidate, str) and len(candidate) == 40 and all(ch in "0123456789abcdef" for ch in candidate), "候选提交身份无效")
    scopes = value.get("scopes")
    _require(isinstance(scopes, dict) and scopes and set(scopes) <= SCOPE_NAMES, "候选绑定范围无效")
    for name, scope in scopes.items():
        _require(isinstance(scope, dict), f"{name} 绑定范围无效")
        _require(set(scope) == {"environment_sha256", "input_manifest_sha256", "tool_sha256", "artifact_sha256"}, f"{name} 绑定字段无效")
        _require(all(_is_sha256(item) for item in scope.values()), f"{name} 绑定摘要无效")
    binary_scopes = set(scopes) & {"core", "product", "community"}
    _require(len({scopes[name]["artifact_sha256"] for name in binary_scopes}) <= 1, "固定内核、专项与社区没有绑定同一候选二进制")


def scope_for_mode(mode: str) -> str:
    try:
        return relationships.scope_for_mode(mode)
    except relationships.RelationshipError as error:
        raise BindingError(str(error)) from error


def for_scope(value: dict[str, Any], scope: str) -> dict[str, str]:
    validate_binding(value)
    if value.get("schema") == evidence_identity.BINDING_SCHEMA:
        try:
            return evidence_identity.report_binding(value, scope)
        except evidence_identity.EvidenceIdentityError as error:
            raise BindingError(str(error)) from error
    _require(scope in value["scopes"], f"候选尚未绑定 {scope} 验收材料")
    active = value["scopes"][scope]
    result = {"suite_version": value["suite_version"], "candidate": value["candidate"], "environment_sha256": active["environment_sha256"], "input_manifest_sha256": active["input_manifest_sha256"], "tool_sha256": active["tool_sha256"]}
    if scope == "frontier":
        result["observer_sha256"] = active["artifact_sha256"]
    else:
        result["binary_sha256"] = active["artifact_sha256"]
    return result


def for_mode(value: dict[str, Any], mode: str) -> dict[str, str]:
    return aggregate(value) if mode == "summarize" else for_scope(value, scope_for_mode(mode))


def aggregate(value: dict[str, Any]) -> dict[str, str]:
    validate_binding(value)
    selected = {name: for_scope(value, name) for name in ("core", "product", "community")}
    binaries = {item["binary_sha256"] for item in selected.values()}
    _require(len(binaries) == 1, "正式三层没有绑定同一候选二进制")
    tool_inputs = {name: item["tool_sha256"] for name, item in selected.items()}
    if value.get("schema") == evidence_identity.BINDING_SCHEMA:
        tool_inputs["summary-generation"] = evidence_identity.reporting_identity(value, "summary")
    return {"suite_version": value["suite_version"], "candidate": evidence_identity.source_git(value), "binary_sha256": binaries.pop(), "environment_sha256": _canonical_sha256({name: item["environment_sha256"] for name, item in selected.items()}), "input_manifest_sha256": _canonical_sha256({name: item["input_manifest_sha256"] for name, item in selected.items()}), "tool_sha256": _canonical_sha256(tool_inputs)}


def verify_current(suite_root: Path, binding_dir: Path, config: dict[str, Any], expected: dict[str, Any], mode: str) -> None:
    validate_config(config)
    validate_binding(expected)
    scope = scope_for_mode(mode)
    _require(scope in relationships.enabled_scopes(config), f"当前配置未启用 {scope}")
    active = for_scope(expected, scope)
    binding_dir = binding_dir.resolve()
    active_dir = _active_generation_dir(binding_dir)
    binding_path = active_dir / "binding.json"
    _require(binding_path.is_file() and load_json(binding_path) == expected, "绑定文件与状态不一致")
    manifests = {name: active_dir / f"{scope}-{name}.json" for name in ("environment", "inputs", "tools")}
    current = {"environment": _environment_manifest(config, scope), "inputs": _input_manifest(suite_root, config, scope), "tools": _tool_manifest(suite_root, scope)}
    for name, field in (("environment", "environment_sha256"), ("inputs", "input_manifest_sha256"), ("tools", "tool_sha256")):
        _require(manifests[name].is_file(), f"{scope} {name} 清单缺失")
        if expected.get("schema") == evidence_identity.BINDING_SCHEMA and name == "tools":
            current_identity = evidence_identity.tool_identity(current[name])
            _require(current_identity == expected["scopes"][scope]["direct_dependencies"]["acceptance-tool"], f"{scope} 执行/评分工具已经变化")
            _require(evidence_identity.tool_identity(load_json(manifests[name])) == current_identity, f"{scope} 封存工具执行身份不一致")
            current_reporting = evidence_identity.reporting_identities(suite_root.parents[2])
            _require(expected["reporting"]["reception"] == current_reporting["reception"], "报告接收语义已经变化")
            _require(expected["reporting"]["relationships"] == current_reporting["relationships"], "报告执行关系语义已经变化")
        else:
            _require(sha256(manifests[name]) == active[field], f"{scope} {name} 清单发生变化")
            _require(load_json(manifests[name]) == current[name], f"{scope} {name} 直接事实已经变化")
    expected_artifact = active["observer_sha256"] if scope == "frontier" else active["binary_sha256"]
    _require(_artifact_sha256(config, scope) == expected_artifact, f"{scope} 候选执行制品已经变化")


def _input_manifest(suite_root: Path, config: dict[str, Any], scope: str) -> dict[str, Any]:
    _require(scope in SCOPE_NAMES, f"未知绑定范围: {scope}")
    paths = [_resolve_repository_input(suite_root, value) for value in relationships.SCOPE_MATERIALS[scope]]
    result: dict[str, Any] = {"schema": "ownward.acceptance-input-manifest/v3", "scope": scope, "files": _files(suite_root.parents[2], paths), "contract": _contract_projection(suite_root, scope), "adapter": _adapter_projection(suite_root, scope)}
    if scope == "product":
        product = _mapping(config, "product")
        package = Path(product["package"]).resolve()
        production = Path(product["production_storage_report"]).resolve()
        result["external_files"] = [{"id": "production_storage_report", "name": production.name, "sha256": sha256(production)}]
        result["external_trees"] = {"product_package": _directory_files(package)}
        result["protocol"] = {"codex_model": product["codex_model"], "codex_reasoning_effort": product["codex_reasoning_effort"]}
    elif scope == "community":
        community = _mapping(config, "community")
        environment_path = Path(community["environment_manifest"]).resolve()
        environment = load_json(environment_path)
        layout = _mapping(environment, "layout")
        data = Path(layout["data"]).resolve()
        protocol_path = Path(community["protocol"]).resolve()
        result["external_files"] = [
            {"id": "longmemeval_s.environment", "name": environment_path.name, "sha256": sha256(environment_path)},
            {"id": "longmemeval_s.data", "name": data.name, "sha256": sha256(data)},
            {"id": "longmemeval_s.protocol", "name": protocol_path.name, "sha256": sha256(protocol_path)},
        ]
        result["protocol"] = {
            "official_code_revision": LONGMEMEVAL_S_CODE_REVISION,
            "official_data_revision": LONGMEMEVAL_S_DATA_REVISION,
            "official_data_sha256": LONGMEMEVAL_S_DATA_SHA256,
            "codex_semantic_model": community["codex_semantic_model"],
            "codex_semantic_reasoning_effort": community["codex_semantic_reasoning_effort"],
            "codex_reader_model": community["codex_reader_model"],
            "codex_reader_reasoning_effort": community["codex_reader_reasoning_effort"],
            "codex_judge_model": community["codex_judge_model"],
            "codex_judge_reasoning_effort": community["codex_judge_reasoning_effort"],
        }
    return result


def _contract_projection(suite_root: Path, scope: str) -> dict[str, Any]:
    contract = load_json(suite_root / "contract.json")
    if scope == "frontier":
        return {"suite_version": contract["suite_version"], "optimization_loop": contract["optimization_loop"], "report": contract["reports"]["frontier"]}
    layer = {"core": "core", "product": "product", "community": "community"}[scope]
    return {"suite_version": contract["suite_version"], "layer": contract["evidence_layers"][layer], "report": contract["reports"][layer]}


def _adapter_projection(suite_root: Path, scope: str) -> dict[str, Any]:
    adapters = load_json(suite_root / "adapters.json")
    if scope == "frontier":
        return {"suite_version": adapters["suite_version"], "definition": adapters["optimization_loop"]}
    layer = {"core": "core", "product": "product", "community": "community"}[scope]
    return {"suite_version": adapters["suite_version"], "definition": adapters["layers"][layer], "product_interface": adapters["product_interface"]}


def _tool_manifest(suite_root: Path, scope: str) -> dict[str, Any]:
    _require(scope in SCOPE_NAMES, f"未知绑定范围: {scope}")
    repository = suite_root.parents[2]
    shared = [suite_root / name for name in ("run.py", "relationships.py", "binding.py", "execution.py", "execution_support.py", "lifecycle.py", "evidence.py", "contract.py", "materials.py", "process_control.py")]
    responsibility_path = suite_root / "adapters" / "product" / "tool-responsibilities.json"
    scoped = {
        "frontier": [suite_root / "execution_frontier.py", suite_root / "frontier.py", repository / "cmd" / "ownward-frontier" / "main.go"],
        "core": [suite_root / "execution_core.py", suite_root / "adapters" / "core" / "verify.py", repository / "benchmarks" / "support" / "ownward_mcp.py"],
        "product": [suite_root / "execution_core.py", suite_root / "execution_product.py", suite_root / "product.py", suite_root / "product_scoring.py", suite_root / "resource_environment.py", responsibility_path, *sorted(path for path in (suite_root / "adapters" / "product").glob("*.py") if not path.name.startswith("test_")), *sorted(path for path in (suite_root / "adapters" / "product_resource").glob("*.py") if not path.name.startswith("test_")), repository / "benchmarks" / "support" / "ownward_mcp.py"],
        "community": [suite_root / "execution_community.py", suite_root / "community.py", suite_root / "process_control.py", suite_root / "adapters" / "product" / "codex_session.py", suite_root / "adapters" / "product" / "codex_transport.py", repository / "benchmarks" / "longmemeval_s" / "run.py", repository / "benchmarks" / "longmemeval_s" / "codex_app_server.py", repository / "benchmarks" / "longmemeval_s" / "environment.py", repository / "benchmarks" / "longmemeval_s" / "protocol.json", repository / "benchmarks" / "longmemeval_s" / "constraints.txt", repository / "benchmarks" / "support" / "ownward_mcp.py"],
    }[scope]
    files = _files(repository, shared + scoped)
    if scope == "product":
        return _product_tool_manifest(repository, responsibility_path, files)
    return {
        "schema": "ownward.acceptance-tool-manifest/v4",
        "scope": scope,
        "repository_commit": _git(repository, "rev-parse", "HEAD"),
        "files": files,
    }


def _product_tool_manifest(
    repository: Path, responsibility_path: Path, files: list[dict[str, str]],
) -> dict[str, Any]:
    declaration = load_json(responsibility_path)
    _require(declaration.get("schema") == "ownward.product-tool-responsibilities/v1", "product 工具职责清单 schema 无效")
    responsibility_relative = responsibility_path.relative_to(repository).as_posix()
    available = {item["path"]: item for item in files if item["path"] != responsibility_relative}
    raw_paths = declaration.get("raw_execution")
    derivation_paths = declaration.get("derivation")
    _require(
        isinstance(raw_paths, list) and isinstance(derivation_paths, list)
        and all(isinstance(value, str) and value for value in raw_paths + derivation_paths),
        "product 工具职责清单内容无效",
    )
    raw_set, derivation_set = set(raw_paths), set(derivation_paths)
    _require(len(raw_set) == len(raw_paths) and len(derivation_set) == len(derivation_paths), "product 工具职责清单包含重复路径")
    _require(not raw_set & derivation_set, "product 工具职责发生重叠")
    _require(raw_set | derivation_set == set(available), "product 工具职责没有完整覆盖活动文件")
    responsibilities = {
        "raw_execution": {
            "schema": "ownward.product-raw-execution-identity/v1",
            "files": [available[path] for path in sorted(raw_set)],
        },
        "derivation": {
            "schema": "ownward.product-derivation-identity/v1",
            "files": [available[path] for path in sorted(derivation_set)],
        },
    }
    for value in responsibilities.values():
        value["sha256"] = _canonical_sha256({"schema": value["schema"], "files": value["files"]})
    migrations = declaration.get("legacy_derivation_replay")
    _require(isinstance(migrations, list) and migrations, "product 旧证据迁移证明缺失")
    migration_ids: set[str] = set()
    for migration in migrations:
        _require(isinstance(migration, dict), "product 旧证据迁移证明无效")
        migration_id = str(migration.get("migration_id", ""))
        _require(migration_id and migration_id not in migration_ids, "product 旧证据迁移身份无效")
        migration_ids.add(migration_id)
        _require(_is_sha256(str(migration.get("source_tool_sha256", ""))), "product 旧工具身份无效")
        _require(_is_sha256(str(migration.get("source_files_sha256", ""))), "product 旧工具文件身份无效")
        _require(_is_sha256(str(migration.get("source_parser_sha256", ""))), "product 旧解析器身份无效")
        _require(_is_sha256(str(migration.get("target_raw_execution_sha256", ""))), "product 迁移目标原始执行身份无效")
        _require(_is_sha256(str(migration.get("target_derivation_sha256", ""))), "product 迁移目标派生身份无效")
    active_migrations = [
        migration for migration in migrations
        if migration["target_raw_execution_sha256"] == responsibilities["raw_execution"]["sha256"]
        and migration["target_derivation_sha256"] == responsibilities["derivation"]["sha256"]
    ]
    return {
        "schema": "ownward.acceptance-tool-manifest/v5",
        "scope": "product",
        "repository_commit": _git(repository, "rev-parse", "HEAD"),
        "files": files,
        "responsibility_manifest": {
            "path": responsibility_relative,
            "sha256": sha256(responsibility_path),
        },
        "responsibilities": responsibilities,
        "legacy_derivation_replay": active_migrations,
    }


def _require_clean_tool_repository(suite_root: Path) -> None:
    repository = suite_root.parents[2].resolve()
    _require(not _git(repository, "status", "--porcelain"), "验收工具仓库不是干净、可复现的提交")


def _environment_manifest(config: dict[str, Any], scope: str) -> dict[str, Any]:
    _require(scope in SCOPE_NAMES, f"未知绑定范围: {scope}")
    result: dict[str, Any] = {"schema": "ownward.acceptance-environment/v3", "scope": scope, "platform": {"system": platform.system(), "release": platform.release(), "machine": platform.machine(), "processor": _processor_name(), "cpu_count": os.cpu_count(), "physical_memory_bytes": _physical_memory_bytes()}}
    if scope != "frontier" or Path(_mapping(config, "frontier")["tool"]).suffix.lower() == ".py":
        result["python"] = {"version": platform.python_version(), "executable_sha256": sha256(Path(sys.executable).resolve())}
    if relationships.SCOPE_CONFIG[scope]["embedding"]:
        _, bundle = _candidate_paths(config)
        result["embedding"] = _embedding_identity(bundle)
    if relationships.SCOPE_CONFIG[scope]["codex"]:
        section = _mapping(config, "product" if scope == "product" else "community")
        codex = Path(section["codex_binary"]).resolve()
        completed = subprocess.run([*_executable_command(codex), "--version"], capture_output=True, text=True, encoding="utf-8", timeout=30, check=False)
        _require(completed.returncode == 0 and completed.stdout.strip(), "无法读取外部智能体版本")
        result["codex"] = {"version": completed.stdout.strip(), "entry_sha256": sha256(codex)}
    if scope == "community":
        community = _mapping(config, "community")
        manifest_path = Path(community["environment_manifest"]).resolve()
        manifest = load_json(manifest_path)
        python_root = Path(_mapping(manifest, "layout")["python"]).resolve()
        python = python_root / ("Scripts/python.exe" if os.name == "nt" else "bin/python")
        completed = subprocess.run([str(python), "--version"], capture_output=True, text=True, encoding="utf-8", timeout=30, check=False)
        _require(completed.returncode == 0 and completed.stdout.strip(), "无法读取 LongMemEval-S 固定 Python")
        result["longmemeval_s"] = {"manifest_sha256": sha256(manifest_path), "python_version": completed.stdout.strip(), "python_sha256": sha256(python)}
    return result


def _embedding_identity(bundle: Path) -> dict[str, Any]:
    runtime_manifest = bundle / "manifest.json"
    _require(runtime_manifest.is_file(), "本地向量能力包清单不存在")
    manifest = load_json(runtime_manifest)
    _require(manifest.get("schema") == "ownward.embedding-bundle/v3", "本地向量能力包 schema 无效")
    model, runtime = manifest.get("model"), manifest.get("runtime")
    _require(isinstance(model, dict) and isinstance(runtime, dict) and isinstance(runtime.get("files"), dict), "本地向量运行时清单不完整")
    declared = {str(model.get("path", "")): str(model.get("sha256", "")), **{str(path): str(digest) for path, digest in runtime["files"].items()}}
    files: list[dict[str, str]] = []
    for relative, expected in sorted(declared.items()):
        path = Path(relative)
        _require(relative and not path.is_absolute() and ".." not in path.parts and _is_sha256(expected), "本地向量运行时文件声明无效")
        artifact = (bundle / path).resolve()
        _require(artifact.is_relative_to(bundle) and artifact.is_file(), f"本地向量能力制品不存在: {relative}")
        actual = sha256(artifact)
        _require(actual == expected, f"本地向量运行时制品摘要不一致: {relative}")
        files.append({"path": path.as_posix(), "sha256": actual})
    return {"runtime_manifest_sha256": sha256(runtime_manifest), "runtime_files": files, "capability": manifest.get("capability")}


def _artifact_sha256(config: dict[str, Any], scope: str) -> str:
    path = Path(_mapping(config, "frontier")["tool"]).resolve() if scope == "frontier" else _candidate_paths(config)[0]
    _require(path.is_file(), f"{scope} 候选执行制品不存在")
    return sha256(path)


def _candidate_binary_sha(config: dict[str, Any], scopes: list[str]) -> str:
    if set(scopes) & {"core", "product", "community"}:
        return sha256(_candidate_paths(config)[0])
    frontier = Path(_mapping(config, "frontier")["tool"]).resolve()
    return sha256(frontier)


def _release_manifest_sha(config: dict[str, Any]) -> str:
    product = config.get("product")
    if isinstance(product, dict) and isinstance(product.get("package"), str):
        manifest = Path(product["package"]).resolve() / "manifest.json"
        if manifest.is_file():
            return sha256(manifest)
    candidate = config.get("candidate")
    if isinstance(candidate, dict) and isinstance(candidate.get("binary"), str):
        sibling = Path(candidate["binary"]).resolve().parent / "ownward-windows-amd64" / "manifest.json"
        if sibling.is_file():
            return sha256(sibling)
    # Frontier-only bindings have no release package dependency. The explicit
    # absence is still a deterministic technical identity, not a Git identity.
    return _canonical_sha256({"schema": "ownward.release-artifact-absence/v1"})


def _candidate_paths(config: dict[str, Any]) -> tuple[Path, Path]:
    candidate = _mapping(config, "candidate")
    return Path(candidate["binary"]).resolve(), Path(candidate["embedding_bundle_dir"]).resolve()


def _validate_community_config(community: dict[str, Any]) -> None:
    _require("judge_api_key_env" not in community, "LongMemEval-S community 不得要求额外 API Key")
    for name in (
        "environment_manifest", "protocol", "output_dir", "codex_binary", "codex_auth_file",
        "codex_semantic_model", "codex_semantic_reasoning_effort", "codex_reader_model",
        "codex_reader_reasoning_effort", "codex_judge_model", "codex_judge_reasoning_effort",
    ):
        _require(isinstance(community.get(name), str) and community[name].strip(), f"执行配置缺少 community.{name}")
    manifest_path = Path(community["environment_manifest"]).resolve()
    protocol_path = Path(community["protocol"]).resolve()
    _require(manifest_path.is_file() and protocol_path.is_file(), "LongMemEval-S 固定环境或协议不存在")
    manifest = load_json(manifest_path)
    _require(manifest.get("schema") == LONGMEMEVAL_S_ENVIRONMENT_SCHEMA, "LongMemEval-S 环境清单无效")
    _require(manifest.get("official", {}).get("code_revision") == LONGMEMEVAL_S_CODE_REVISION, "LongMemEval-S 官方代码版本漂移")
    _require(manifest.get("official", {}).get("data_revision") == LONGMEMEVAL_S_DATA_REVISION, "LongMemEval-S 官方数据版本漂移")
    _require(manifest.get("integrity", {}).get("data_sha256") == LONGMEMEVAL_S_DATA_SHA256, "LongMemEval-S 官方数据摘要漂移")
    protocol = load_json(protocol_path)
    _require(protocol.get("schema") == "ownward.longmemeval-s-protocol/v1", "LongMemEval-S 固定协议无效")
    _require(protocol.get("official", {}).get("data_sha256") == LONGMEMEVAL_S_DATA_SHA256, "LongMemEval-S 协议与数据身份不一致")
    _require(Path(community["codex_binary"]).resolve().is_file(), "LongMemEval-S Codex 执行程序不存在")
    _require(Path(community["codex_auth_file"]).resolve().is_file(), "LongMemEval-S Codex 认证文件不存在")
    _require(community["codex_semantic_model"] == protocol["memory"]["semantic_model"], "LongMemEval-S Codex 语义模型偏离固定口径")
    _require(community["codex_semantic_reasoning_effort"] == protocol["memory"]["semantic_reasoning_effort"], "LongMemEval-S Codex 语义推理强度偏离固定口径")
    _require(community["codex_reader_model"] == protocol["reader"]["model"], "LongMemEval-S Codex Reader 模型偏离固定口径")
    _require(community["codex_reader_reasoning_effort"] == protocol["reader"]["reasoning_effort"], "LongMemEval-S Codex Reader 推理强度偏离固定口径")
    _require(community["codex_judge_model"] == protocol["judge"]["model"], "LongMemEval-S Codex 裁判模型偏离固定口径")
    _require(community["codex_judge_reasoning_effort"] == protocol["judge"]["reasoning_effort"], "LongMemEval-S Codex 裁判推理强度偏离固定口径")


def _validate_community_workspace(config: dict[str, Any], workspace: Path) -> None:
    community = _mapping(config, "community")
    manifest = load_json(Path(community["environment_manifest"]).resolve())
    runs = Path(_mapping(manifest, "layout")["runs"]).resolve()
    output = Path(community["output_dir"]).resolve()
    _require(output.is_relative_to(runs) and output != runs, "LongMemEval-S 运行输出必须位于持久环境 runs 内")
    evidence = (workspace / "evidence" / "community").resolve()
    _require(evidence.is_relative_to(workspace) and evidence != workspace, "LongMemEval-S Suite 证据必须位于验收工作区内")


def _executable_command(path: Path) -> list[str]:
    if path.suffix.lower() == ".ps1":
        executable = shutil.which("pwsh") or shutil.which("powershell")
        _require(executable is not None, "运行外部智能体 PowerShell 入口需要 PowerShell")
        return [executable, "-NoProfile", "-File", str(path)]
    _require(path.suffix.lower() not in {".cmd", ".bat"}, "不接受不可审计的命令包装器")
    return [str(path)]


def _require_isolated_path(path: Path, label: str) -> None:
    if os.name == "nt":
        _require(bool(path.drive) and path.drive.lower() != Path.home().drive.lower(), f"{label}不得位于系统盘")
    else:
        _require(path.is_absolute(), f"{label}必须使用绝对路径")


def _processor_name() -> str:
    value = platform.processor().strip()
    if os.name == "nt":
        try:
            import winreg
            with winreg.OpenKey(winreg.HKEY_LOCAL_MACHINE, r"HARDWARE\DESCRIPTION\System\CentralProcessor\0") as key:
                value = str(winreg.QueryValueEx(key, "ProcessorNameString")[0]).strip()
        except OSError:
            pass
    return value or platform.machine()


def _physical_memory_bytes() -> int:
    if os.name == "nt":
        import ctypes
        class MemoryStatus(ctypes.Structure):
            _fields_ = [("length", ctypes.c_ulong), ("memory_load", ctypes.c_ulong), ("total_physical", ctypes.c_ulonglong), ("available_physical", ctypes.c_ulonglong), ("total_page_file", ctypes.c_ulonglong), ("available_page_file", ctypes.c_ulonglong), ("total_virtual", ctypes.c_ulonglong), ("available_virtual", ctypes.c_ulonglong), ("available_extended_virtual", ctypes.c_ulonglong)]
        status = MemoryStatus()
        status.length = ctypes.sizeof(status)
        _require(bool(ctypes.windll.kernel32.GlobalMemoryStatusEx(ctypes.byref(status))), "无法读取物理内存身份")
        return int(status.total_physical)
    return int(os.sysconf("SC_PAGE_SIZE") * os.sysconf("SC_PHYS_PAGES"))


def _files(repository: Path, paths: list[Path]) -> list[dict[str, str]]:
    result = []
    for path in sorted({item.resolve() for item in paths}):
        _require(path.is_file(), f"绑定工具或材料不存在: {path}")
        try:
            relative = path.relative_to(repository.resolve()).as_posix()
        except ValueError:
            relative = path.name
        result.append({"path": relative, "sha256": sha256(path)})
    return result


def _directory_files(root: Path) -> list[dict[str, Any]]:
    _require(root.is_dir(), f"目录不存在: {root}")
    return [{"path": path.relative_to(root).as_posix(), "size": path.stat().st_size, "sha256": sha256(path)} for path in sorted(item for item in root.rglob("*") if item.is_file())]


def _resolve_repository_input(suite_root: Path, value: str) -> Path:
    repository = suite_root.parents[2].resolve()
    path = (repository / value).resolve()
    _require(path.is_relative_to(repository) and path.is_file(), f"固定输入路径无效: {value}")
    return path


def _git(repository: Path, *arguments: str) -> str:
    completed = subprocess.run(["git", "-C", str(repository), *arguments], capture_output=True, text=True, encoding="utf-8", timeout=30, check=False)
    _require(completed.returncode == 0, f"Git 命令失败: {' '.join(arguments)}")
    return completed.stdout.strip()


def _argument_value(arguments: list[str], name: str) -> str:
    _require(name in arguments, f"参数缺少 {name}")
    index = arguments.index(name)
    _require(index + 1 < len(arguments), f"参数 {name} 缺少值")
    return arguments[index + 1]


def _canonical_sha256(value: Any) -> str:
    encoded = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def _serialized_json_sha256(value: Any) -> str:
    encoded = (json.dumps(value, ensure_ascii=False, indent=2) + "\n").encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def _is_sha256(value: Any) -> bool:
    return isinstance(value, str) and len(value) == 64 and all(character in "0123456789abcdef" for character in value)


def _verify_go_binary(binary: Path, candidate: str) -> None:
    completed = subprocess.run(["go", "version", "-m", str(binary)], capture_output=True, text=True, encoding="utf-8", timeout=30, check=False)
    _require(completed.returncode == 0, f"无法读取 Go 构建身份: {binary}")
    fields = {}
    for line in completed.stdout.splitlines():
        parts = line.strip().split("\t")
        if len(parts) >= 2 and parts[0] == "build" and "=" in parts[1]:
            name, value = parts[1].split("=", 1)
            fields[name] = value
    _require(fields.get("vcs.revision") == candidate, "Go 构建身份没有绑定候选提交")
    _require(fields.get("vcs.modified") == "false", "Go 构建来自脏工作区")


def _write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.{os.getpid()}.{time.time_ns()}.tmp")
    with temporary.open("w", encoding="utf-8", newline="\n") as stream:
        stream.write(json.dumps(value, ensure_ascii=False, indent=2) + "\n")
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(temporary, path)


def _mapping(value: dict[str, Any], name: str) -> dict[str, Any]:
    nested = value.get(name)
    _require(isinstance(nested, dict), f"执行配置缺少 {name}")
    return nested


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise BindingError(message)
