from __future__ import annotations

import base64
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
from typing import Any

import kernel_iteration_candidate as first_candidate
import kernel_iteration_candidate_multisource as multisource_candidate
import kernel_iteration_candidate_system_budget as system_budget
import kernel_iteration_evidence as evidence
import kernel_iteration_stage4_resource_cost as resource_cost
import kernel_iteration_validation as validation


CANDIDATE_RECEIPT_SCHEMA = "ownward.kernel-iteration-v2-candidate/v6"
CANDIDATE_POLICY = "sealed-current-revision-raw-to-ready-formal-release-with-exact-query-vector-lazy-evidence-quiescent-compaction-and-compact-semantic-transport/v5"
STORAGE_POLICY = "quiescent-lossless-derived-compaction/v1"
SEMANTIC_REPRESENTATION = "ownward.semantic-indexed-body-context-table/v2"
REPRESENTATION_LIFECYCLE = "sealed-current-revision-raw-to-ready-formal-release/v2"


def prepare(
    suite_root: Path,
    output_root: Path,
    execution_config_path: Path,
    *,
    resume: bool = False,
) -> dict[str, Any]:
    repository = suite_root.resolve().parents[2]
    output_root = output_root.resolve()
    _require(output_root.is_relative_to(repository / ".tmp"), "V2 资源成本候选只能写入仓库非正式 .tmp 边界")
    runtime = validation.validate_execution_config(suite_root, execution_config_path.resolve())
    receipt_path = output_root / "candidate-receipt.json"
    subject_path = output_root / "subject.json"
    candidate_config_path = output_root / "execution.json"
    binary_path = output_root / ("ownward.exe" if os.name == "nt" else "ownward")
    embedding_path = output_root / "embedding"
    composition_path = output_root / "composition.json"
    overlay_path = output_root / "go-overlay.json"
    generated_service_path = output_root / "core-service.go.overlay"
    generated_collaboration_path = output_root / "core-collaboration.go.overlay"
    generated_generation_path = output_root / "core-generation.go.overlay"
    semantic_manifest_path = output_root / "semantic-representation.json"
    paths = (
        receipt_path, subject_path, candidate_config_path, binary_path, composition_path,
        overlay_path, generated_service_path, generated_collaboration_path, generated_generation_path, semantic_manifest_path,
    )
    if any(path.exists() for path in (*paths, embedding_path)):
        _require(resume and all(path.is_file() for path in paths) and embedding_path.is_dir(), "V2 资源成本候选现场不完整；禁止覆盖或宽松复用")
        receipt = _load_json(receipt_path)
        subject = evidence.validate_v2_subject(evidence.load_contract(suite_root), _load_json(subject_path))
        _require(receipt.get("schema") == CANDIDATE_RECEIPT_SCHEMA and receipt.get("subject_identity") == subject["identity"], "V2 资源成本候选收据漂移")
        checks = {
            "builder_sha256": evidence.file_sha256(Path(__file__).resolve()),
            "binary_sha256": evidence.file_sha256(binary_path),
            "composition_sha256": evidence.file_sha256(composition_path),
            "overlay_sha256": evidence.file_sha256(overlay_path),
            "generated_service_sha256": evidence.file_sha256(generated_service_path),
            "generated_collaboration_sha256": evidence.file_sha256(generated_collaboration_path),
            "generated_generation_sha256": evidence.file_sha256(generated_generation_path),
            "semantic_manifest_sha256": evidence.file_sha256(semantic_manifest_path),
            "semantic_runtime_sha256": evidence.file_sha256(repository / "benchmarks/longmemeval_s/semantic_representation.py"),
            "semantic_executor_sha256": evidence.file_sha256(repository / "benchmarks/longmemeval_s/run.py"),
            "embedding_manifest_sha256": evidence.file_sha256(embedding_path / "manifest.json"),
            "service_source_transform_sha256": evidence.file_sha256(repository / "manifests/kernel-candidates/v2/retrieval-latency/service-transform.json"),
            "collaboration_source_transform_sha256": evidence.file_sha256(repository / "manifests/kernel-candidates/v2/resource-cost/collaboration-transform.json"),
            "representation_service_transform_sha256": evidence.file_sha256(repository / "manifests/kernel-candidates/v2/resource-cost/representation-service-transform.json"),
            "representation_collaboration_transform_sha256": evidence.file_sha256(repository / "manifests/kernel-candidates/v2/resource-cost/representation-collaboration-transform.json"),
            "representation_generation_transform_sha256": evidence.file_sha256(repository / "manifests/kernel-candidates/v2/resource-cost/representation-generation-transform.json"),
            "representation_lifecycle_source_sha256": evidence.file_sha256(repository / "internal/kernelv2candidate/representation.go"),
        }
        for field, actual in checks.items():
            _require(receipt.get(field) == actual, f"V2 资源成本候选制品漂移: {field}")
        return {**receipt, "reused": True, "subject_manifest": str(subject_path), "execution_config": str(candidate_config_path)}

    output_root.mkdir(parents=True, exist_ok=True)
    service_transform_relative = "manifests/kernel-candidates/v2/retrieval-latency/service-transform.json"
    collaboration_transform_relative = "manifests/kernel-candidates/v2/resource-cost/collaboration-transform.json"
    representation_service_transform_relative = "manifests/kernel-candidates/v2/resource-cost/representation-service-transform.json"
    representation_collaboration_transform_relative = "manifests/kernel-candidates/v2/resource-cost/representation-collaboration-transform.json"
    representation_generation_transform_relative = "manifests/kernel-candidates/v2/resource-cost/representation-generation-transform.json"
    semantic_manifest_relative = "manifests/kernel-candidates/v2/resource-cost/semantic-representation.json"
    semantic_runtime_relative = "benchmarks/longmemeval_s/semantic_representation.py"
    semantic_executor_relative = "benchmarks/longmemeval_s/run.py"
    with tempfile.TemporaryDirectory(dir=output_root) as staging:
        stage = Path(staging)
        stage_service = stage / "core-service.go"
        stage_collaboration = stage / "core-collaboration.go"
        system_budget.latency_candidate._render_transformed_source(
            repository, repository / service_transform_relative, stage_service,
        )
        _render_transformed_input(stage_service, repository / representation_service_transform_relative, generated_service_path)
        system_budget.latency_candidate._render_transformed_source(
            repository, repository / collaboration_transform_relative, stage_collaboration,
        )
        _render_transformed_input(stage_collaboration, repository / representation_collaboration_transform_relative, generated_collaboration_path)
    system_budget.latency_candidate._render_transformed_source(
        repository, repository / representation_generation_transform_relative, generated_generation_path,
    )

    current_path = repository / "manifests" / "compositions" / "v1" / "current-collaborative.json"
    template = _load_json(current_path)
    template["name"] = "v2-resource-cost-candidate"
    template.pop("audit", None)
    kernel = next((item for item in template.get("components", []) if item.get("role") == "kernel"), None)
    _require(isinstance(kernel, dict), "当前组合缺少内核组件")
    kernel["config"] = {
        **_mapping(kernel, "config"),
        "evidence_selection": first_candidate.CANDIDATE_POLICY,
        "evidence_source_scheduling": multisource_candidate.CANDIDATE_POLICY,
        "semantic_query_strategy": system_budget.CANDIDATE_POLICY,
        "derived_storage_lifecycle": STORAGE_POLICY,
        "representation_lifecycle": REPRESENTATION_LIFECYCLE,
    }
    content = kernel.get("content")
    _require(isinstance(content, list), "当前内核组件内容无效")
    evidence_overlay_relative = "manifests/kernel-candidates/v2/fragment-complete/core-evidence.go.overlay"
    composition_embed_relative = "manifests/kernel-candidates/v2/fragment-complete/composition-embed.go.overlay"
    core_evidence = next((item for item in content if item.get("name") == "core-evidence"), None)
    _require(isinstance(core_evidence, dict), "当前内核缺少证据选择实现")
    core_evidence["path"] = evidence_overlay_relative
    content.extend([
        {"name": "v2-evidence-continuity", "path": "internal/kernelv2candidate/evidence.go", "sha256": ""},
        {"name": "v2-source-scheduling", "path": "internal/kernelv2candidate/planner.go", "sha256": ""},
        {"name": "v2-latency-service-transform", "path": service_transform_relative, "sha256": ""},
        {"name": "v2-resource-cost-collaboration-transform", "path": collaboration_transform_relative, "sha256": ""},
        {"name": "v2-representation-lifecycle", "path": "internal/kernelv2candidate/representation.go", "sha256": ""},
        {"name": "v2-representation-service-transform", "path": representation_service_transform_relative, "sha256": ""},
        {"name": "v2-representation-collaboration-transform", "path": representation_collaboration_transform_relative, "sha256": ""},
        {"name": "v2-representation-generation-transform", "path": representation_generation_transform_relative, "sha256": ""},
    ])
    assembly = next((item for item in template.get("components", []) if item.get("role") == "assembly"), None)
    _require(isinstance(assembly, dict), "当前组合缺少装配组件")
    assembly_content = assembly.get("content")
    _require(isinstance(assembly_content, list), "当前装配组件内容无效")
    composition_embed = next((item for item in assembly_content if item.get("name") == "release-composition-embed"), None)
    _require(isinstance(composition_embed, dict), "当前装配组件缺少发布组合封存实现")
    composition_embed["path"] = composition_embed_relative
    vector = next((item for item in template.get("components", []) if item.get("role") == "vector"), None)
    _require(isinstance(vector, dict), "当前组合缺少向量组件")
    _require(all(item.get("name") != "v2-managed-runtime-transform" for item in vector.get("content", [])), "资源成本候选不得恢复 6/2 runtime overlay")
    semantic = next((item for item in template.get("components", []) if item.get("role") == "semantic"), None)
    _require(isinstance(semantic, dict) and isinstance(semantic.get("content"), list), "当前组合缺少语义组件")
    semantic_manifest = _load_json(repository / semantic_manifest_relative)
    semantic["config"] = {
        **_mapping(semantic, "config"),
        "input_representation": semantic_manifest["representation"],
        "input_representation_manifest_identity": semantic_manifest["identity"],
    }
    semantic["content"].append({"name": "v2-semantic-input-representation", "path": semantic_manifest_relative, "sha256": ""})

    template_path = output_root / "composition-template.json"
    evidence.atomic_json(template_path, template)
    sealed = subprocess.run(
        ["go", "run", "./cmd/ownward-composition", "seal", "--repository", str(repository), "--manifest", str(template_path), "--output", str(composition_path)],
        cwd=repository, capture_output=True, text=True, encoding="utf-8", timeout=180, check=False,
    )
    _require(sealed.returncode == 0 and composition_path.is_file(), f"封存 V2 资源成本候选组合失败: {sealed.stderr.strip()}")
    composition = _load_json(composition_path)
    sealed_kernel = next((item for item in composition.get("components", []) if item.get("role") == "kernel"), None)
    _require(isinstance(sealed_kernel, dict), "V2 资源成本候选缺少封存内核")
    dependencies = {item["role"]: item["identity"] for item in sealed_kernel.get("dependencies", [])}
    _require(set(dependencies) == {"authority-substrate", "product-rules", "semantic", "vector"}, "V2 资源成本候选内核直接依赖不完整")
    generation_identity = evidence.canonical_sha256({
        "schema": "ownward.kernel-iteration-generation/v1",
        "kernel_effect_identity": sealed_kernel["identity"],
        "composition_identity": composition["identity"],
        "evidence_selection": first_candidate.CANDIDATE_POLICY,
        "evidence_source_scheduling": multisource_candidate.CANDIDATE_POLICY,
        "semantic_query_strategy": system_budget.CANDIDATE_POLICY,
        "derived_storage_lifecycle": STORAGE_POLICY,
        "representation_lifecycle": REPRESENTATION_LIFECYCLE,
        "embedding_runtime_configuration": system_budget.SYSTEM_RUNTIME,
        "direct_dependencies": dict(sorted(dependencies.items())),
    })
    evidence.atomic_json(overlay_path, {"Replace": {
        str((repository / "internal" / "core" / "evidence.go").resolve()): str((repository / evidence_overlay_relative).resolve()),
        str((repository / "internal" / "core" / "service.go").resolve()): str(generated_service_path),
        str((repository / "internal" / "core" / "collaboration.go").resolve()): str(generated_collaboration_path),
        str((repository / "internal" / "core" / "generation.go").resolve()): str(generated_generation_path),
        str((repository / "manifests" / "compositions" / "v1" / "embed.go").resolve()): str((repository / composition_embed_relative).resolve()),
    }})
    # Windows CreateProcess has a short command-line ceiling. The sealed JSON
    # object is whitespace-insensitive, so embed its compact form while keeping
    # the audited pretty-printed composition file and identity unchanged.
    sealed_composition = base64.b64encode(
        json.dumps(composition, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    ).decode("ascii")
    ldflags = f"-X main.version={generation_identity} -X github.com/HJSunDev/ownward/manifests/compositions/v1.sealedCompositionBase64={sealed_composition}"
    build = subprocess.run(
        ["go", "build", "-trimpath", "-overlay", str(overlay_path), "-ldflags", ldflags, "-o", str(binary_path), "./cmd/ownward"],
        cwd=repository, capture_output=True, text=True, encoding="utf-8", timeout=300, check=False,
    )
    _require(build.returncode == 0 and binary_path.is_file(), f"构建 V2 资源成本候选失败: {build.stderr.strip()}")
    shutil.copytree(runtime["embedding"], embedding_path, copy_function=shutil.copy2)
    shutil.copy2(repository / semantic_manifest_relative, semantic_manifest_path)
    binary_sha256 = evidence.file_sha256(binary_path)
    subject_content = {
        "schema": evidence.SUBJECT_SCHEMA,
        "role": "v2-candidate",
        "kernel_generation_identity": generation_identity,
        "kernel_effect_identity": sealed_kernel["identity"],
        "direct_dependencies": dict(sorted(dependencies.items())),
        "artifacts": {
            "binary": binary_sha256,
            "composition": composition["identity"],
            "composition-file": evidence.file_sha256(composition_path),
            "generated-service": evidence.file_sha256(generated_service_path),
            "generated-collaboration": evidence.file_sha256(generated_collaboration_path),
            "generated-generation": evidence.file_sha256(generated_generation_path),
            "service-source-transform": evidence.file_sha256(repository / service_transform_relative),
            "collaboration-source-transform": evidence.file_sha256(repository / collaboration_transform_relative),
            "representation-service-transform": evidence.file_sha256(repository / representation_service_transform_relative),
            "representation-collaboration-transform": evidence.file_sha256(repository / representation_collaboration_transform_relative),
            "representation-generation-transform": evidence.file_sha256(repository / representation_generation_transform_relative),
            "representation-lifecycle": evidence.file_sha256(repository / "internal/kernelv2candidate/representation.go"),
            "embedding-runtime-source": evidence.file_sha256(repository / "internal" / "embedding" / "llama.go"),
            "semantic-representation-manifest": evidence.file_sha256(repository / semantic_manifest_relative),
            "semantic-representation-runtime": evidence.file_sha256(repository / semantic_runtime_relative),
            "semantic-executor": evidence.file_sha256(repository / semantic_executor_relative),
            "semantic-representation-feasibility-source": evidence.file_sha256(repository / "benchmarks/acceptance/suite/kernel_iteration_stage4_resource_cost_compact_feasibility.py"),
        },
    }
    subject_identity = evidence.canonical_sha256(subject_content)
    evidence.atomic_json(subject_path, {
        **subject_content,
        "name": "v2-resource-cost-candidate",
        "identity": subject_identity,
        "audit": {"source": "exact-workspace-content", "git_is_identity": False},
    })
    candidate_config = json.loads(json.dumps(runtime["value"]))
    candidate_config["candidate"]["binary"] = str(binary_path)
    candidate_config["candidate"]["embedding_bundle_dir"] = str(embedding_path)
    candidate_config["candidate"]["commit"] = generation_identity
    candidate_config["candidate"].pop("semantic_representation_manifest", None)
    sealed_semantic = next((item for item in composition.get("components", []) if item.get("role") == "semantic"), None)
    _require(isinstance(sealed_semantic, dict), "V2 资源成本候选缺少封存语义组件")
    candidate_config["candidate"]["semantic_representation"] = {
        "manifest": str(semantic_manifest_path),
        "identity": semantic_manifest["identity"],
        "composition_identity": composition["identity"],
        "semantic_component_identity": sealed_semantic["identity"],
    }
    evidence.atomic_json(candidate_config_path, candidate_config)
    receipt_content = {
        "schema": CANDIDATE_RECEIPT_SCHEMA,
        "subject_identity": subject_identity,
        "kernel_generation_identity": generation_identity,
        "kernel_effect_identity": sealed_kernel["identity"],
        "composition_identity": composition["identity"],
        "composition_sha256": evidence.file_sha256(composition_path),
        "overlay_sha256": evidence.file_sha256(overlay_path),
        "generated_service_sha256": evidence.file_sha256(generated_service_path),
        "generated_collaboration_sha256": evidence.file_sha256(generated_collaboration_path),
        "generated_generation_sha256": evidence.file_sha256(generated_generation_path),
        "semantic_manifest_sha256": evidence.file_sha256(semantic_manifest_path),
        "semantic_representation": SEMANTIC_REPRESENTATION,
        "semantic_runtime_sha256": evidence.file_sha256(repository / semantic_runtime_relative),
        "semantic_executor_sha256": evidence.file_sha256(repository / semantic_executor_relative),
        "service_source_transform_sha256": evidence.file_sha256(repository / service_transform_relative),
        "collaboration_source_transform_sha256": evidence.file_sha256(repository / collaboration_transform_relative),
        "representation_service_transform_sha256": evidence.file_sha256(repository / representation_service_transform_relative),
        "representation_collaboration_transform_sha256": evidence.file_sha256(repository / representation_collaboration_transform_relative),
        "representation_generation_transform_sha256": evidence.file_sha256(repository / representation_generation_transform_relative),
        "representation_lifecycle_source_sha256": evidence.file_sha256(repository / "internal/kernelv2candidate/representation.go"),
        "embedding_runtime_configuration": dict(system_budget.SYSTEM_RUNTIME),
        "embedding_manifest_sha256": evidence.file_sha256(embedding_path / "manifest.json"),
        "binary_sha256": binary_sha256,
        "builder_sha256": evidence.file_sha256(Path(__file__).resolve()),
        "storage_policy": STORAGE_POLICY,
        "representation_lifecycle": REPRESENTATION_LIFECYCLE,
        "formal": False,
        "formal_state_written": False,
    }
    receipt = {**receipt_content, "identity": evidence.canonical_sha256(receipt_content)}
    evidence.atomic_json(receipt_path, receipt)
    template_path.unlink()
    return {**receipt, "reused": False, "subject_manifest": str(subject_path), "execution_config": str(candidate_config_path)}


def _render_transformed_input(source_path: Path, transform_path: Path, output_path: Path) -> None:
    transform = _load_json(transform_path)
    _require(transform.get("schema") == system_budget.latency_candidate.TRANSFORM_SCHEMA, "V2 资源成本候选源码转换合同无效")
    value = source_path.read_text(encoding="utf-8")
    replacements = transform.get("replacements")
    _require(isinstance(replacements, list) and replacements, "V2 资源成本候选源码转换步骤为空")
    for replacement in replacements:
        _require(isinstance(replacement, dict), "V2 资源成本候选源码转换步骤无效")
        before, after, count = replacement.get("before"), replacement.get("after"), replacement.get("count")
        _require(isinstance(before, str) and isinstance(after, str) and isinstance(count, int) and count > 0, "V2 资源成本候选源码转换参数无效")
        _require(value.count(before) == count, f"V2 资源成本候选源码转换来源漂移: {replacement.get('name', '')}")
        value = value.replace(before, after)
    output_path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile("w", encoding="utf-8", newline="\n", dir=output_path.parent, delete=False) as handle:
        handle.write(value)
        temporary = Path(handle.name)
    os.replace(temporary, output_path)


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取 V2 资源成本候选制品 {path}: {error}") from error
    _require(isinstance(value, dict), f"V2 资源成本候选制品不是对象: {path}")
    return value


def _mapping(value: dict[str, Any], name: str) -> dict[str, Any]:
    result = value.get(name)
    _require(isinstance(result, dict), f"V2 资源成本候选缺少对象: {name}")
    return result


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
