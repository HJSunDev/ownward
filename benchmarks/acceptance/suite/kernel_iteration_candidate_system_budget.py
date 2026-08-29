from __future__ import annotations

import base64
import json
import os
from pathlib import Path
import shutil
import subprocess
from typing import Any

import kernel_iteration_candidate as first_candidate
import kernel_iteration_candidate_latency as latency_candidate
import kernel_iteration_candidate_multisource as multisource_candidate
import kernel_iteration_evidence as evidence
import kernel_iteration_validation as validation


CANDIDATE_RECEIPT_SCHEMA = "ownward.kernel-iteration-v2-candidate/v3"
CANDIDATE_POLICY = "exact-query-vector-with-persistent-loopback-and-lazy-bounded-evidence/v1"
SYSTEM_RUNTIME = {"threads": 2, "threads_batch": 2, "parallel": 1}


def prepare(
    suite_root: Path,
    output_root: Path,
    execution_config_path: Path,
    *,
    resume: bool = False,
) -> dict[str, Any]:
    repository = suite_root.resolve().parents[2]
    output_root = output_root.resolve()
    _require(output_root.is_relative_to(repository / ".tmp"), "V2 系统线程预算候选只能写入仓库非正式 .tmp 边界")
    runtime = validation.validate_execution_config(suite_root, execution_config_path.resolve())
    receipt_path = output_root / "candidate-receipt.json"
    subject_path = output_root / "subject.json"
    candidate_config_path = output_root / "execution.json"
    binary_path = output_root / ("ownward.exe" if os.name == "nt" else "ownward")
    embedding_path = output_root / "embedding"
    composition_path = output_root / "composition.json"
    overlay_path = output_root / "go-overlay.json"
    generated_service_path = output_root / "core-service.go.overlay"
    paths = receipt_path, subject_path, candidate_config_path, binary_path, composition_path, overlay_path, generated_service_path
    if any(path.exists() for path in (*paths, embedding_path)):
        _require(resume and all(path.is_file() for path in paths) and embedding_path.is_dir(), "V2 系统线程预算候选现场不完整；禁止覆盖或宽松复用")
        receipt = _load_json(receipt_path)
        subject = evidence.validate_v2_subject(evidence.load_contract(suite_root), _load_json(subject_path))
        _require(receipt.get("schema") == CANDIDATE_RECEIPT_SCHEMA and receipt.get("subject_identity") == subject["identity"], "V2 系统线程预算候选收据漂移")
        checks = {
            "builder_sha256": evidence.file_sha256(Path(__file__).resolve()),
            "binary_sha256": evidence.file_sha256(binary_path),
            "composition_sha256": evidence.file_sha256(composition_path),
            "overlay_sha256": evidence.file_sha256(overlay_path),
            "generated_service_sha256": evidence.file_sha256(generated_service_path),
            "embedding_manifest_sha256": evidence.file_sha256(embedding_path / "manifest.json"),
            "embedding_runtime_source_sha256": evidence.file_sha256(repository / "internal" / "embedding" / "llama.go"),
        }
        for field, actual in checks.items():
            _require(receipt.get(field) == actual, f"V2 系统线程预算候选制品漂移: {field}")
        _require(receipt.get("embedding_runtime_configuration") == SYSTEM_RUNTIME, "V2 系统线程预算候选运行配置漂移")
        return {**receipt, "reused": True, "subject_manifest": str(subject_path), "execution_config": str(candidate_config_path)}

    output_root.mkdir(parents=True, exist_ok=True)
    transform_relative = "manifests/kernel-candidates/v2/retrieval-latency/service-transform.json"
    transform_path = repository / transform_relative
    latency_candidate._render_transformed_source(repository, transform_path, generated_service_path)

    current_path = repository / "manifests" / "compositions" / "v1" / "current-collaborative.json"
    template = _load_json(current_path)
    template["name"] = "v2-retrieval-latency-system-budget-candidate"
    template.pop("audit", None)
    kernel = next((item for item in template.get("components", []) if item.get("role") == "kernel"), None)
    _require(isinstance(kernel, dict), "当前组合缺少内核组件")
    kernel["config"] = {
        **_mapping(kernel, "config"),
        "evidence_selection": first_candidate.CANDIDATE_POLICY,
        "evidence_source_scheduling": multisource_candidate.CANDIDATE_POLICY,
        "semantic_query_strategy": CANDIDATE_POLICY,
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
        {"name": "v2-latency-service-transform", "path": transform_relative, "sha256": ""},
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
    _require(
        all(item.get("name") != "v2-managed-runtime-transform" for item in vector.get("content", [])),
        "系统线程预算候选不得携带 6/2 managed runtime overlay",
    )

    template_path = output_root / "composition-template.json"
    evidence.atomic_json(template_path, template)
    sealed = subprocess.run(
        ["go", "run", "./cmd/ownward-composition", "seal", "--repository", str(repository), "--manifest", str(template_path), "--output", str(composition_path)],
        cwd=repository, capture_output=True, text=True, encoding="utf-8", timeout=180, check=False,
    )
    _require(sealed.returncode == 0 and composition_path.is_file(), f"封存 V2 系统线程预算候选组合失败: {sealed.stderr.strip()}")
    composition = _load_json(composition_path)
    sealed_kernel = next((item for item in composition.get("components", []) if item.get("role") == "kernel"), None)
    _require(isinstance(sealed_kernel, dict), "V2 系统线程预算候选缺少封存内核")
    dependencies = {item["role"]: item["identity"] for item in sealed_kernel.get("dependencies", [])}
    _require(set(dependencies) == {"authority-substrate", "product-rules", "semantic", "vector"}, "V2 系统线程预算候选内核直接依赖不完整")
    generation_identity = evidence.canonical_sha256({
        "schema": "ownward.kernel-iteration-generation/v1",
        "kernel_effect_identity": sealed_kernel["identity"],
        "composition_identity": composition["identity"],
        "evidence_selection": first_candidate.CANDIDATE_POLICY,
        "evidence_source_scheduling": multisource_candidate.CANDIDATE_POLICY,
        "semantic_query_strategy": CANDIDATE_POLICY,
        "embedding_runtime_configuration": SYSTEM_RUNTIME,
        "direct_dependencies": dict(sorted(dependencies.items())),
    })
    evidence.atomic_json(overlay_path, {"Replace": {
        str((repository / "internal" / "core" / "evidence.go").resolve()): str((repository / evidence_overlay_relative).resolve()),
        str((repository / "internal" / "core" / "service.go").resolve()): str(generated_service_path),
        str((repository / "manifests" / "compositions" / "v1" / "embed.go").resolve()): str((repository / composition_embed_relative).resolve()),
    }})
    sealed_composition = base64.b64encode(composition_path.read_bytes()).decode("ascii")
    ldflags = (
        f"-X main.version={generation_identity} "
        f"-X github.com/HJSunDev/ownward/manifests/compositions/v1.sealedCompositionBase64={sealed_composition}"
    )
    build = subprocess.run(
        ["go", "build", "-trimpath", "-overlay", str(overlay_path), "-ldflags", ldflags, "-o", str(binary_path), "./cmd/ownward"],
        cwd=repository, capture_output=True, text=True, encoding="utf-8", timeout=300, check=False,
    )
    _require(build.returncode == 0 and binary_path.is_file(), f"构建 V2 系统线程预算候选失败: {build.stderr.strip()}")
    shutil.copytree(runtime["embedding"], embedding_path, copy_function=shutil.copy2)
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
            "source-transform": evidence.file_sha256(transform_path),
            "embedding-runtime-source": evidence.file_sha256(repository / "internal" / "embedding" / "llama.go"),
        },
    }
    subject_identity = evidence.canonical_sha256(subject_content)
    evidence.atomic_json(subject_path, {
        **subject_content,
        "name": "v2-retrieval-latency-system-budget-candidate",
        "identity": subject_identity,
        "audit": {"source": "exact-workspace-content", "git_is_identity": False},
    })
    candidate_config = json.loads(json.dumps(runtime["value"]))
    candidate_config["candidate"]["binary"] = str(binary_path)
    candidate_config["candidate"]["embedding_bundle_dir"] = str(embedding_path)
    candidate_config["candidate"]["commit"] = generation_identity
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
        "source_transform_sha256": evidence.file_sha256(transform_path),
        "embedding_runtime_source_sha256": evidence.file_sha256(repository / "internal" / "embedding" / "llama.go"),
        "embedding_runtime_configuration": dict(SYSTEM_RUNTIME),
        "embedding_manifest_sha256": evidence.file_sha256(embedding_path / "manifest.json"),
        "binary_sha256": binary_sha256,
        "builder_sha256": evidence.file_sha256(Path(__file__).resolve()),
        "evidence_selection": first_candidate.CANDIDATE_POLICY,
        "evidence_source_scheduling": multisource_candidate.CANDIDATE_POLICY,
        "semantic_query_strategy": CANDIDATE_POLICY,
        "formal": False,
        "formal_state_written": False,
    }
    receipt = {**receipt_content, "identity": evidence.canonical_sha256(receipt_content)}
    evidence.atomic_json(receipt_path, receipt)
    template_path.unlink()
    return {**receipt, "reused": False, "subject_manifest": str(subject_path), "execution_config": str(candidate_config_path)}


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取 V2 系统线程预算候选制品 {path}: {error}") from error
    _require(isinstance(value, dict), f"V2 系统线程预算候选制品不是对象: {path}")
    return value


def _mapping(value: dict[str, Any], name: str) -> dict[str, Any]:
    result = value.get(name)
    _require(isinstance(result, dict), f"V2 系统线程预算候选缺少对象: {name}")
    return result


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
