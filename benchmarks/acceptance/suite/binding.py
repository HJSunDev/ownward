from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import sys
from typing import Any


class BindingError(ValueError):
    pass


TARGET_STAGES = {
    "identity", "relations", "merge_split", "incremental_consistency", "organization", "indexing",
    "lexical", "vector", "graph", "context", "fusion",
}

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
ACTIVE_CODEX_MODEL = "gpt-5.4-mini"
ACTIVE_CODEX_REASONING_EFFORT = "xhigh"
SCOPE_NAMES = {"frontier", "core", "product", "community"}
MODE_SCOPES = {
    "targeted": "frontier",
    "frontier": "frontier",
    "core": "core",
    "qualification": "product",
    "full": "product",
    "longmemeval": "community",
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


def create(
    suite_root: Path,
    config_path: Path,
    output_dir: Path,
) -> dict[str, Any]:
    config_path = config_path.resolve()
    config = load_json(config_path)
    validate_config(config)
    product = _mapping(config, "product")
    frontier = _mapping(config, "frontier")
    repository = Path(config["repository"])
    binary = Path(product["binary"])
    runtime_dir = Path(product["runtime_dir"])
    package = Path(product["package"])
    production_report = Path(product["production_storage_report"])
    codex_binary = Path(product["codex_binary"])
    frontier_binary = Path(frontier["tool"])
    repository = repository.resolve()
    binary = binary.resolve()
    runtime_dir = runtime_dir.resolve()
    package = package.resolve()
    production_report = production_report.resolve()
    codex_binary = codex_binary.resolve()
    frontier_binary = frontier_binary.resolve()
    output_dir = output_dir.resolve()
    _require(Path(config["binding_dir"]).resolve() == output_dir, "输出目录必须与执行配置 binding_dir 一致")
    workspace = Path(config["workspace"]).resolve()
    _require(workspace.drive.lower() != Path.home().drive.lower(), "验收工作区不得位于系统盘")
    community = config.get("community")
    if community is not None:
        _require(isinstance(community, dict), "执行配置 community 必须是对象")
        _require(Path(community["binary"]).resolve() == binary and Path(community["runtime_dir"]).resolve() == runtime_dir, "专项集与社区基准必须使用同一候选与运行时")
        _require(Path(community["codex_binary"]).resolve() == codex_binary, "专项集与社区基准必须使用同一外部智能体程序")
        for domain in ("web", "enterprise"):
            run_output = Path(_argument_value(list(community[f"{domain}_arguments"]), "--output-dir")).resolve()
            _require(run_output.is_relative_to(workspace) and run_output != workspace, f"{domain} 输出必须位于验收工作区内")
        submission_root = Path(community["submission_root"]).resolve()
        _require(submission_root.is_relative_to(workspace) and submission_root != workspace, "LongMemEval-V2 submission 必须位于验收工作区内")
    _require(output_dir.drive and output_dir.drive.lower() != Path.home().drive.lower(), "绑定清单不得写入系统盘")
    for path, label in ((binary, "候选二进制"), (codex_binary, "Codex"), (frontier_binary, "内核观察器")):
        _require(path.is_file(), f"{label}不存在: {path}")
    _require(runtime_dir.is_dir(), "本地向量运行时不存在")
    _require(package.is_dir() and (package / "manifest.json").is_file(), "候选发布包或清单不存在")
    _require(production_report.is_file(), "生产规模存储证据不存在")
    candidate = _git(repository, "rev-parse", "HEAD")
    _require(not _git(repository, "status", "--porcelain"), "候选仓库不是干净、冻结的提交")
    version = subprocess.run([str(binary), "version"], cwd=repository, capture_output=True, text=True, encoding="utf-8", timeout=30, check=False)
    _require(version.returncode == 0 and version.stdout.strip() == candidate, "候选二进制没有绑定候选提交")
    _verify_go_binary(binary, candidate)
    _verify_go_binary(frontier_binary, candidate)

    output_dir.mkdir(parents=True, exist_ok=True)
    scopes: dict[str, dict[str, str]] = {}
    enabled_scopes = ["frontier", "core", "product"] + (["community"] if community is not None else [])
    for scope in enabled_scopes:
        manifests = {
            "environment": _environment_manifest(runtime_dir, codex_binary, frontier_binary, scope),
            "inputs": _input_manifest(suite_root, config, scope),
            "tools": _tool_manifest(suite_root, scope),
        }
        paths = {name: output_dir / f"{scope}-{name}.json" for name in manifests}
        for name, value in manifests.items():
            _write_json(paths[name], value)
        scopes[scope] = {
            "environment_sha256": sha256(paths["environment"]),
            "input_manifest_sha256": sha256(paths["inputs"]),
            "tool_sha256": sha256(paths["tools"]),
        }
    binding = {
        "schema": "ownward.acceptance-binding/v2",
        "suite_version": "1.0.0",
        "candidate": candidate,
        "binary_sha256": sha256(binary),
        "scopes": scopes,
    }
    validate_binding(binding)
    _write_json(output_dir / "binding.json", binding)
    return binding


def validate_config(config: dict[str, Any]) -> None:
    _require(config.get("schema") == "ownward.acceptance-execution/v1", "执行配置 schema 无效")
    for name in ("repository", "workspace", "binding_dir"):
        _require(isinstance(config.get(name), str) and config[name].strip(), f"执行配置缺少 {name}")
    frontier = _mapping(config, "frontier")
    _require(isinstance(frontier.get("tool"), str) and frontier["tool"].strip(), "执行配置缺少 frontier.tool")
    stages = frontier.get("targeted_stages")
    _require(isinstance(stages, list) and all(isinstance(item, str) for item in stages), "frontier.targeted_stages 必须是字符串数组")
    _require(len(stages) == len(set(stages)) and set(stages) <= TARGET_STAGES, "frontier.targeted_stages 包含重复或未知阶段")
    product = _mapping(config, "product")
    for name in (
        "binary", "runtime_dir", "package", "production_storage_report", "codex_binary", "codex_auth_file",
        "codex_model", "codex_reasoning_effort",
    ):
        _require(isinstance(product.get(name), str) and product[name].strip(), f"执行配置缺少 product.{name}")
    _require(product["codex_model"] == ACTIVE_CODEX_MODEL, "专项集必须使用固定外部智能体模型")
    _require(product["codex_reasoning_effort"] == ACTIVE_CODEX_REASONING_EFFORT, "专项集必须使用固定外部智能体推理强度")
    community = config.get("community")
    if community is None:
        return
    _require(isinstance(community, dict), "执行配置 community 必须是对象")
    for name in (
        "official_repo", "binary", "runtime_dir", "codex_binary", "codex_auth_file", "submission_root", "submission_name",
    ):
        _require(isinstance(community.get(name), str) and community[name].strip(), f"执行配置缺少 community.{name}")
    for domain in ("web", "enterprise"):
        arguments = community.get(f"{domain}_arguments")
        _require(isinstance(arguments, list) and all(isinstance(item, str) for item in arguments), f"{domain} 参数必须是字符串数组")
        _require(len(arguments) % 2 == 0, f"{domain} 参数必须由名称和值组成")
        for required in (
            "--domain", "--questions-path", "--haystack-path", "--trajectories-path", "--memory-config-path", "--output-dir",
            "--base-url", "--evaluator-api-key-env", "--prompt-build-max-workers",
        ):
            _argument_value(arguments, required)
        _require(_argument_value(arguments, "--domain") == domain, f"{domain} 参数选择了另一领域")
        for name, expected in OFFICIAL_LONGMEM_ARGUMENTS.items():
            _require(_argument_value(arguments, name) == expected, f"{domain} 参数 {name} 偏离固定官方口径")
        _require(bool(_argument_value(arguments, "--base-url").strip()), f"{domain} 缺少固定 Reader 服务地址")
        _require(bool(_argument_value(arguments, "--evaluator-api-key-env").strip()), f"{domain} 缺少官方裁判凭证变量名")
        try:
            workers = int(_argument_value(arguments, "--prompt-build-max-workers"))
        except ValueError as error:
            raise BindingError(f"{domain} 并发数无效") from error
        _require(workers > 0, f"{domain} 并发数必须为正数")


def validate_binding(value: dict[str, Any]) -> None:
    _require(isinstance(value, dict), "候选绑定必须是对象")
    _require(
        set(value) == {"schema", "suite_version", "candidate", "binary_sha256", "scopes"},
        "候选绑定顶层字段无效",
    )
    _require(value.get("schema") == "ownward.acceptance-binding/v2", "候选绑定 schema 无效")
    _require(value.get("suite_version") == "1.0.0", "候选绑定体系版本无效")
    candidate = value.get("candidate")
    _require(
        isinstance(candidate, str)
        and len(candidate) == 40
        and all(character in "0123456789abcdef" for character in candidate),
        "候选提交身份无效",
    )
    _require(_is_sha256(value.get("binary_sha256")), "候选二进制摘要无效")
    scopes = value.get("scopes")
    _require(isinstance(scopes, dict), "候选绑定缺少分层范围")
    _require({"frontier", "core", "product"}.issubset(scopes) and set(scopes) <= SCOPE_NAMES, "候选绑定范围无效")
    for name, scope in scopes.items():
        _require(isinstance(scope, dict), f"{name} 绑定范围无效")
        _require(set(scope) == {"environment_sha256", "input_manifest_sha256", "tool_sha256"}, f"{name} 绑定字段无效")
        _require(all(_is_sha256(value) for value in scope.values()), f"{name} 绑定摘要无效")


def scope_for_mode(mode: str) -> str:
    scope = MODE_SCOPES.get(mode)
    _require(scope is not None, f"模式 {mode} 没有独立绑定范围")
    return scope


def for_scope(value: dict[str, Any], scope: str) -> dict[str, str]:
    validate_binding(value)
    scopes = value["scopes"]
    _require(scope in scopes, f"候选尚未绑定 {scope} 验收材料")
    return {
        "suite_version": value["suite_version"],
        "candidate": value["candidate"],
        "binary_sha256": value["binary_sha256"],
        **scopes[scope],
    }


def for_mode(value: dict[str, Any], mode: str) -> dict[str, str]:
    if mode == "summarize":
        return aggregate(value)
    return for_scope(value, scope_for_mode(mode))


def aggregate(value: dict[str, Any]) -> dict[str, str]:
    validate_binding(value)
    selected = {name: for_scope(value, name) for name in ("core", "product", "community")}
    return {
        "suite_version": value["suite_version"],
        "candidate": value["candidate"],
        "binary_sha256": value["binary_sha256"],
        "environment_sha256": _canonical_sha256({name: item["environment_sha256"] for name, item in selected.items()}),
        "input_manifest_sha256": _canonical_sha256({name: item["input_manifest_sha256"] for name, item in selected.items()}),
        "tool_sha256": _canonical_sha256({name: item["tool_sha256"] for name, item in selected.items()}),
    }


def verify_current(
    suite_root: Path,
    binding_dir: Path,
    config: dict[str, Any],
    expected: dict[str, Any],
    mode: str,
) -> None:
    validate_binding(expected)
    scope = scope_for_mode(mode)
    active = for_scope(expected, scope)
    binding_dir = binding_dir.resolve()
    manifests = {name: binding_dir / f"{scope}-{name}.json" for name in ("environment", "inputs", "tools")}
    binding_path = binding_dir / "binding.json"
    _require(binding_path.is_file() and load_json(binding_path) == expected, "绑定文件与状态不一致")
    for name, field in (("environment", "environment_sha256"), ("inputs", "input_manifest_sha256"), ("tools", "tool_sha256")):
        _require(manifests[name].is_file() and sha256(manifests[name]) == active[field], f"{scope} {name} 清单发生变化")
    _require(load_json(manifests["inputs"]) == _input_manifest(suite_root, config, scope), f"{scope} 验收输入或执行口径已经变化")
    _require(load_json(manifests["tools"]) == _tool_manifest(suite_root, scope), f"{scope} 验收工具源码已经变化")
    product = _mapping(config, "product")
    frontier = _mapping(config, "frontier")
    current_environment = _environment_manifest(
        Path(product["runtime_dir"]).resolve(),
        Path(product["codex_binary"]).resolve(),
        Path(frontier["tool"]).resolve(),
        scope,
    )
    _require(load_json(manifests["environment"]) == current_environment, f"{scope} 验收环境或执行文件已经变化")
    _require(sha256(Path(product["binary"]).resolve()) == active["binary_sha256"], "候选二进制已经变化")


def _input_manifest(suite_root: Path, config: dict[str, Any], scope: str) -> dict[str, Any]:
    _require(scope in SCOPE_NAMES, f"未知绑定范围: {scope}")
    materials = load_json(suite_root / "materials" / "manifest.json")
    paths = [
        suite_root / "contract.json",
        suite_root / "adapters.json",
    ]
    selected_materials = {
        "frontier": ("/core/", "/frontier/"),
        "core": ("/core/",),
        "product": ("/product/",),
        "community": (),
    }[scope]
    paths.extend(
        _resolve_material(suite_root, item["path"])
        for item in materials["files"]
        if any(marker in str(item["path"]).replace("\\", "/") for marker in selected_materials)
    )
    if scope == "product":
        paths.append(suite_root / "adapters" / "product_resource" / "thresholds.json")
    result: dict[str, Any] = {
        "schema": "ownward.acceptance-input-manifest/v2",
        "scope": scope,
        "files": _files(suite_root.parents[2], paths),
    }
    if scope == "product":
        product = _mapping(config, "product")
        package = Path(product["package"]).resolve()
        production = Path(product["production_storage_report"]).resolve()
        result["external_files"] = [{
            "id": "production_storage_report",
            "name": production.name,
            "sha256": sha256(production),
        }]
        result["external_trees"] = {"product_package": _directory_files(package)}
        result["protocol"] = {
            "codex_model": product["codex_model"],
            "codex_reasoning_effort": product["codex_reasoning_effort"],
        }
    elif scope == "community":
        community = _mapping(config, "community")
        external: list[dict[str, str]] = []
        protocol: dict[str, Any] = {"official_revision": "2cc8c540bdb87fe6761629b585e727e1c4704520", "domains": {}}
        for domain in ("web", "enterprise"):
            arguments = list(community[f"{domain}_arguments"])
            normalized: list[Any] = []
            index = 0
            while index < len(arguments):
                name = arguments[index]
                _require(index + 1 < len(arguments), f"{domain} 参数不完整: {name}")
                value = arguments[index + 1]
                if name in {"--questions-path", "--haystack-path", "--trajectories-path", "--memory-config-path"}:
                    path = Path(value).resolve()
                    external.append({"id": f"{domain}.{name[2:-5]}", "name": path.name, "sha256": sha256(path)})
                    normalized.extend([name, {"path": path.name, "sha256": sha256(path)}])
                elif name != "--output-dir":
                    normalized.extend([name, value])
                index += 2
            protocol["domains"][domain] = normalized
        result["external_files"] = sorted(external, key=lambda item: item["id"])
        result["protocol"] = protocol
    return result


def _tool_manifest(suite_root: Path, scope: str) -> dict[str, Any]:
    _require(scope in SCOPE_NAMES, f"未知绑定范围: {scope}")
    repository = suite_root.parents[2]
    shared = [
        suite_root / name for name in (
            "run.py", "binding.py", "execution.py", "lifecycle.py", "evidence.py",
            "contract.py", "materials.py", "process_control.py",
        )
    ]
    scoped = {
        "frontier": [suite_root / "frontier.py", repository / "cmd" / "ownward-frontier" / "main.go"],
        "core": [suite_root / "adapters" / "core" / "verify.py"],
        "product": [
            suite_root / "product.py",
            *sorted(path for path in (suite_root / "adapters" / "product").glob("*.py") if not path.name.startswith("test_")),
            *sorted(path for path in (suite_root / "adapters" / "product_resource").glob("*.py") if not path.name.startswith("test_")),
            repository / "benchmarks" / "support" / "ownward_mcp.py",
        ],
        "community": [
            suite_root / "community.py",
            repository / "benchmarks" / "longmemeval_v2" / "run.py",
            repository / "benchmarks" / "longmemeval_v2" / "ownward_memory.py",
            repository / "benchmarks" / "longmemeval_v2" / "ownward_trajectory.py",
            repository / "benchmarks" / "longmemeval_v2" / "memory_config.active.json",
            repository / "benchmarks" / "longmemeval_v2" / "memory_config.direct.json",
            repository / "benchmarks" / "longmemeval_v2" / "SYSTEM_DESCRIPTION.md",
            repository / "benchmarks" / "support" / "ownward_mcp.py",
        ],
    }[scope]
    return {"schema": "ownward.acceptance-tool-manifest/v2", "scope": scope, "files": _files(repository, shared + scoped)}


def _environment_manifest(runtime_dir: Path, codex_binary: Path, frontier_binary: Path, scope: str) -> dict[str, Any]:
    _require(scope in SCOPE_NAMES, f"未知绑定范围: {scope}")
    runtime_manifest = runtime_dir / "manifest.json"
    _require(runtime_manifest.is_file(), "本地向量运行时清单不存在")
    manifest = load_json(runtime_manifest)
    model = manifest.get("model")
    runtime = manifest.get("runtime")
    _require(isinstance(model, dict) and isinstance(runtime, dict), "本地向量运行时清单不完整")
    declared = {str(model.get("path", "")): str(model.get("sha256", ""))}
    runtime_files = runtime.get("files")
    _require(isinstance(runtime_files, dict), "本地向量运行时文件清单无效")
    declared.update({str(path): str(digest) for path, digest in runtime_files.items()})
    verified_files: list[dict[str, str]] = []
    for relative, expected in sorted(declared.items()):
        path = Path(relative)
        _require(relative and not path.is_absolute() and ".." not in path.parts and len(expected) == 64, "本地向量运行时文件声明无效")
        artifact = (runtime_dir / path).resolve()
        _require(artifact.is_relative_to(runtime_dir) and artifact.is_file(), f"本地向量运行时制品不存在: {relative}")
        actual = sha256(artifact)
        _require(actual == expected, f"本地向量运行时制品摘要不一致: {relative}")
        verified_files.append({"path": path.as_posix(), "sha256": actual})
    result: dict[str, Any] = {
        "schema": "ownward.acceptance-environment/v2",
        "scope": scope,
        "platform": {
            "system": platform.system(),
            "release": platform.release(),
            "machine": platform.machine(),
            "processor": _processor_name(),
            "cpu_count": os.cpu_count(),
            "physical_memory_bytes": _physical_memory_bytes(),
        },
        "python": {"version": platform.python_version(), "executable_sha256": sha256(Path(sys.executable).resolve())},
        "runtime_manifest_sha256": sha256(runtime_manifest),
        "runtime_files": verified_files,
    }
    if scope in {"product", "community"}:
        codex_version = subprocess.run(
            [*_executable_command(codex_binary), "--version"],
            capture_output=True, text=True, encoding="utf-8", timeout=30, check=False,
        )
        _require(codex_version.returncode == 0 and codex_version.stdout.strip(), "无法读取外部智能体版本")
        result["codex"] = {"version": codex_version.stdout.strip(), "entry_sha256": sha256(codex_binary)}
    if scope == "frontier":
        result["frontier_observer"] = {"entry_sha256": sha256(frontier_binary)}
    return result


def _executable_command(path: Path) -> list[str]:
    if path.suffix.lower() == ".ps1":
        executable = shutil.which("pwsh") or shutil.which("powershell")
        _require(executable is not None, "运行外部智能体 PowerShell 入口需要 PowerShell")
        return [executable, "-NoProfile", "-File", str(path)]
    _require(path.suffix.lower() not in {".cmd", ".bat"}, "不接受不可审计的命令包装器")
    return [str(path)]


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
            _fields_ = [
                ("length", ctypes.c_ulong), ("memory_load", ctypes.c_ulong),
                ("total_physical", ctypes.c_ulonglong), ("available_physical", ctypes.c_ulonglong),
                ("total_page_file", ctypes.c_ulonglong), ("available_page_file", ctypes.c_ulonglong),
                ("total_virtual", ctypes.c_ulonglong), ("available_virtual", ctypes.c_ulonglong),
                ("available_extended_virtual", ctypes.c_ulonglong),
            ]

        status = MemoryStatus()
        status.length = ctypes.sizeof(status)
        _require(bool(ctypes.windll.kernel32.GlobalMemoryStatusEx(ctypes.byref(status))), "无法读取物理内存身份")
        return int(status.total_physical)
    page_size = os.sysconf("SC_PAGE_SIZE")
    pages = os.sysconf("SC_PHYS_PAGES")
    return int(page_size * pages)


def _files(repository: Path, paths: list[Path]) -> list[dict[str, str]]:
    unique = sorted({path.resolve() for path in paths})
    result = []
    for path in unique:
        _require(path.is_file(), f"清单文件不存在: {path}")
        try:
            relative = path.relative_to(repository.resolve()).as_posix()
        except ValueError as error:
            raise BindingError(f"清单文件不在仓库中: {path}") from error
        result.append({"path": relative, "sha256": sha256(path)})
    return result


def _directory_files(root: Path) -> list[dict[str, Any]]:
    _require(root.is_dir(), f"外部目录不存在: {root}")
    result: list[dict[str, Any]] = []
    for path in sorted(root.rglob("*")):
        _require(not path.is_symlink(), f"外部目录不得包含符号链接: {path}")
        if path.is_file():
            result.append({
                "path": path.relative_to(root).as_posix(),
                "bytes": path.stat().st_size,
                "sha256": sha256(path),
            })
    _require(result, f"外部目录为空: {root}")
    return result


def _resolve_material(suite_root: Path, value: str) -> Path:
    path = Path(value)
    return path if path.is_absolute() else suite_root.parents[2] / path


def _git(repository: Path, *arguments: str) -> str:
    completed = subprocess.run(["git", *arguments], cwd=repository, capture_output=True, text=True, encoding="utf-8", timeout=30, check=False)
    _require(completed.returncode == 0, f"Git 命令失败: {' '.join(arguments)}")
    return completed.stdout.strip()


def _argument_value(arguments: list[str], name: str) -> str:
    try:
        return arguments[arguments.index(name) + 1]
    except (ValueError, IndexError) as error:
        raise BindingError(f"执行配置缺少 {name}") from error


def _canonical_sha256(value: Any) -> str:
    encoded = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def _is_sha256(value: Any) -> bool:
    return isinstance(value, str) and len(value) == 64 and all(character in "0123456789abcdef" for character in value)


def _verify_go_binary(binary: Path, candidate: str) -> None:
    completed = subprocess.run(
        ["go", "version", "-m", str(binary)],
        capture_output=True,
        text=True,
        encoding="utf-8",
        timeout=30,
        check=False,
    )
    _require(completed.returncode == 0, "无法读取候选二进制的 Go 构建身份")
    settings: dict[str, str] = {}
    for line in completed.stdout.splitlines():
        fields = line.strip().split("\t")
        if len(fields) == 2 and fields[0] == "build" and "=" in fields[1]:
            key, value = fields[1].split("=", 1)
            settings[key] = value
    _require(settings.get("vcs.revision") == candidate, "候选二进制的源码版本与候选提交不一致")
    _require(settings.get("vcs.modified") == "false", "候选二进制由脏工作区构建")


def _write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    temporary.replace(path)


def _mapping(value: dict[str, Any], name: str) -> dict[str, Any]:
    nested = value.get(name)
    _require(isinstance(nested, dict), f"执行配置缺少 {name}")
    return nested


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise BindingError(message)
