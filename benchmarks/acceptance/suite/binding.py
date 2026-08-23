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

import relationships


class BindingError(ValueError):
    pass


TARGET_STAGES = set(relationships.TARGET_STAGES)
SCOPE_NAMES = set(relationships.SCOPE_CONFIG)
MODE_SCOPES = relationships.MODE_SCOPES
ACTIVE_CODEX_MODEL = "gpt-5.4-mini"
ACTIVE_CODEX_REASONING_EFFORT = "xhigh"
OFFICIAL_LONGMEM_ARGUMENTS = {
    "--model": "Qwen/Qwen3.5-9B",
    "--temperature": "0.6",
    "--top-p": "0.95",
    "--top-k": "20",
    "--max-completion-tokens": "20000",
    "--memory-context-max-tokens": "200000",
    "--reader-max-concurrent-requests": "16",
    "--evaluator-model": "gpt-5.2",
    "--evaluator-reasoning-effort": "medium",
    "--evaluator-max-completion-tokens": "4096",
}


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
        community = _mapping(config, "community")
        _require(Path(community["codex_binary"]).resolve().is_file(), "社区验收 Codex 不存在")
        _require(Path(community["codex_auth_file"]).resolve().is_file(), "社区验收 Codex 认证文件不存在")
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
    result = {"schema": "ownward.acceptance-binding/v4", "suite_version": "1.0.0", "candidate": candidate, "scopes": scopes}
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
    candidate = previous["candidate"]
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
    replacement = {
        "environment": _environment_manifest(config, scope),
        "inputs": _input_manifest(suite_root, config, scope),
        "tools": _tool_manifest(suite_root, scope),
    }
    manifest_values.update({f"{scope}-{name}.json": value for name, value in replacement.items()})
    hashes = {name: _serialized_json_sha256(value) for name, value in replacement.items()}
    scopes[scope] = {
        "environment_sha256": hashes["environment"],
        "input_manifest_sha256": hashes["inputs"],
        "tool_sha256": hashes["tools"],
        "artifact_sha256": _artifact_sha256(config, scope),
    }
    result = {"schema": "ownward.acceptance-binding/v4", "suite_version": previous["suite_version"], "candidate": candidate, "scopes": scopes}
    validate_binding(result)
    _activate_generation(output_dir, result, manifest_values)
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


def _activate_generation(output_dir: Path, binding: dict[str, Any], manifests: dict[str, dict[str, Any]]) -> None:
    generation = _canonical_sha256({"binding": binding, "manifests": manifests})[:24]
    generations = output_dir / "generations"
    generations.mkdir(parents=True, exist_ok=True)
    destination = generations / generation
    if not destination.exists():
        temporary = generations / f".tmp-{os.getpid()}-{time.time_ns()}"
        temporary.mkdir()
        _write_json(temporary / "binding.json", binding)
        for filename, value in manifests.items():
            _write_json(temporary / filename, value)
        _require(load_json(temporary / "binding.json") == binding, "新绑定代次自检失败")
        temporary.replace(destination)
    _validate_generation(destination, expected_binding=binding, expected_manifests=manifests)
    active = {"schema": "ownward.acceptance-binding-active/v1", "generation": generation, "binding_sha256": sha256(destination / "binding.json")}
    _write_json(output_dir / "active.json", active)
    # Compatibility mirrors are not authoritative; readers resolve active.json first.
    _write_json(output_dir / "binding.json", binding)
    for filename, value in manifests.items():
        _write_json(output_dir / filename, value)


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
        for kind, field in (
            ("environment", "environment_sha256"),
            ("inputs", "input_manifest_sha256"),
            ("tools", "tool_sha256"),
        ):
            filename = f"{scope}-{kind}.json"
            path = directory / filename
            _require(path.is_file() and sha256(path) == identity[field], f"不可变绑定代次的 {filename} 缺失或摘要不一致")
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
    return {"suite_version": value["suite_version"], "candidate": value["candidate"], "binary_sha256": binaries.pop(), "environment_sha256": _canonical_sha256({name: item["environment_sha256"] for name, item in selected.items()}), "input_manifest_sha256": _canonical_sha256({name: item["input_manifest_sha256"] for name, item in selected.items()}), "tool_sha256": _canonical_sha256({name: item["tool_sha256"] for name, item in selected.items()})}


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
        _require(manifests[name].is_file() and sha256(manifests[name]) == active[field], f"{scope} {name} 清单发生变化")
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
        external: list[dict[str, str]] = []
        protocol: dict[str, Any] = {"official_revision": "2cc8c540bdb87fe6761629b585e727e1c4704520", "domains": {}}
        for domain in ("web", "enterprise"):
            arguments = list(community[f"{domain}_arguments"])
            normalized: list[Any] = []
            for index in range(0, len(arguments), 2):
                name, value = arguments[index:index + 2]
                if name in {"--questions-path", "--haystack-path", "--trajectories-path", "--memory-config-path"}:
                    path = Path(value).resolve()
                    digest = sha256(path)
                    external.append({"id": f"{domain}.{name[2:-5]}", "name": path.name, "sha256": digest})
                    normalized.extend([name, {"path": path.name, "sha256": digest}])
                elif name != "--output-dir":
                    normalized.extend([name, value])
            protocol["domains"][domain] = normalized
        result["external_files"] = sorted(external, key=lambda item: item["id"])
        result["protocol"] = protocol
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
    scoped = {
        "frontier": [suite_root / "execution_frontier.py", suite_root / "frontier.py", repository / "cmd" / "ownward-frontier" / "main.go"],
        "core": [suite_root / "execution_core.py", suite_root / "adapters" / "core" / "verify.py", repository / "benchmarks" / "support" / "ownward_mcp.py"],
        "product": [suite_root / "execution_core.py", suite_root / "execution_product.py", suite_root / "product.py", suite_root / "resource_environment.py", *sorted(path for path in (suite_root / "adapters" / "product").glob("*.py") if not path.name.startswith("test_")), *sorted(path for path in (suite_root / "adapters" / "product_resource").glob("*.py") if not path.name.startswith("test_")), repository / "benchmarks" / "support" / "ownward_mcp.py"],
        "community": [suite_root / "execution_community.py", suite_root / "community.py", repository / "benchmarks" / "longmemeval_v2" / "run.py", repository / "benchmarks" / "longmemeval_v2" / "ownward_memory.py", repository / "benchmarks" / "longmemeval_v2" / "ownward_trajectory.py", repository / "benchmarks" / "longmemeval_v2" / "memory_config.active.json", repository / "benchmarks" / "longmemeval_v2" / "memory_config.direct.json", repository / "benchmarks" / "longmemeval_v2" / "SYSTEM_DESCRIPTION.md", repository / "benchmarks" / "support" / "ownward_mcp.py"],
    }[scope]
    return {
        "schema": "ownward.acceptance-tool-manifest/v4",
        "scope": scope,
        "repository_commit": _git(repository, "rev-parse", "HEAD"),
        "files": _files(repository, shared + scoped),
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


def _candidate_paths(config: dict[str, Any]) -> tuple[Path, Path]:
    candidate = _mapping(config, "candidate")
    return Path(candidate["binary"]).resolve(), Path(candidate["embedding_bundle_dir"]).resolve()


def _validate_community_config(community: dict[str, Any]) -> None:
    for name in ("official_repo", "codex_binary", "codex_auth_file", "submission_root", "submission_name"):
        _require(isinstance(community.get(name), str) and community[name].strip(), f"执行配置缺少 community.{name}")
    for domain in ("web", "enterprise"):
        arguments = community.get(f"{domain}_arguments")
        _require(isinstance(arguments, list) and all(isinstance(item, str) for item in arguments) and len(arguments) % 2 == 0, f"{domain} 参数必须由名称和值组成")
        for required in ("--domain", "--questions-path", "--haystack-path", "--trajectories-path", "--memory-config-path", "--output-dir", "--base-url", "--evaluator-api-key-env", "--prompt-build-max-workers"):
            _argument_value(arguments, required)
        _require(_argument_value(arguments, "--domain") == domain, f"{domain} 参数选择了另一领域")
        for name, expected in OFFICIAL_LONGMEM_ARGUMENTS.items():
            _require(_argument_value(arguments, name) == expected, f"{domain} 参数 {name} 偏离固定官方口径")
        try:
            workers = int(_argument_value(arguments, "--prompt-build-max-workers"))
        except ValueError as error:
            raise BindingError(f"{domain} 并发数无效") from error
        _require(workers > 0, f"{domain} 并发数必须为正数")


def _validate_community_workspace(config: dict[str, Any], workspace: Path) -> None:
    community = _mapping(config, "community")
    for domain in ("web", "enterprise"):
        output = Path(_argument_value(list(community[f"{domain}_arguments"]), "--output-dir")).resolve()
        _require(output.is_relative_to(workspace) and output != workspace, f"{domain} 输出必须位于验收工作区内")
    submission = Path(community["submission_root"]).resolve()
    _require(submission.is_relative_to(workspace) and submission != workspace, "LongMemEval-V2 submission 必须位于验收工作区内")


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
