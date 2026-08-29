from __future__ import annotations

import base64
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import subprocess
import tarfile
import tempfile
from typing import Any

import kernel_iteration_evidence as evidence
import kernel_iteration_candidate_latency as latency_candidate
import kernel_iteration_validation as validation


RECEIPT_SCHEMA = "ownward.kernel-iteration-stage4-shared-vector-runtime/v1"
RUNTIME_CONFIGURATION = {"threads": 6, "threads_batch": 6, "parallel": 2}
PREVIOUS_SOURCE_COMMIT = "ea793ff75cfe77daec554f54fe0417375c0c93c9"


def prepare(
    suite_root: Path,
    output_root: Path,
    previous_candidate_root: Path,
    final_candidate_root: Path,
    *,
    resume: bool = False,
) -> dict[str, Any]:
    suite_root, output_root = suite_root.resolve(), output_root.resolve()
    repository = suite_root.parents[2]
    _require(output_root.is_relative_to(repository / ".tmp"), "共享向量运行时制品只能写入仓库非正式 .tmp 边界")
    previous_candidate_root = previous_candidate_root.resolve()
    final_candidate_root = final_candidate_root.resolve()
    receipt_path = output_root / "runtime-binaries.json"
    binary_paths = {name: output_root / f"ownward-{name}.exe" for name in ("v0", "previous-v2", "candidate")}
    embedding_path = output_root / "embedding"
    generated_runtime_path = output_root / "managed-runtime.go.overlay"
    if receipt_path.is_file():
        _require(resume and all(path.is_file() for path in binary_paths.values()) and embedding_path.is_dir() and generated_runtime_path.is_file(), "共享向量运行时制品现场不完整")
        value = _load_json(receipt_path)
        _validate_receipt(value, repository, previous_candidate_root, final_candidate_root, binary_paths, embedding_path, generated_runtime_path)
        return {**value, "path": str(receipt_path), "reused": True}
    _require(not receipt_path.exists() and not any(path.exists() for path in binary_paths.values()), "共享向量运行时制品现场已存在；禁止覆盖")
    output_root.mkdir(parents=True, exist_ok=True)

    comparison = _load_json(suite_root / "iteration" / "v2" / "comparison-contract.json")
    v0 = comparison["subjects"]["v0"]
    v0_commit = str(v0["audit_source_git"])
    previous_receipt = _validate_previous_candidate(previous_candidate_root)
    final_receipt = _validate_final_candidate(final_candidate_root)
    runtime_base_source = repository / "internal" / "embedding" / "llama.go"
    runtime_transform = repository / "manifests" / "kernel-candidates" / "v2" / "retrieval-latency" / "managed-runtime-transform.json"
    latency_candidate._render_transformed_source(repository, runtime_transform, generated_runtime_path)
    runtime_source_sha = evidence.file_sha256(generated_runtime_path)

    with tempfile.TemporaryDirectory(prefix="shared-runtime-build-", dir=output_root) as temporary:
        temporary_root = Path(temporary)
        v0_source = _extract_commit(repository, v0_commit, temporary_root / "v0")
        shutil.copy2(generated_runtime_path, v0_source / "internal" / "embedding" / "llama.go")
        _build(
            v0_source,
            binary_paths["v0"],
            f"-X main.version={v0['kernel_generation_identity']}",
        )

        previous_source = _extract_commit(repository, PREVIOUS_SOURCE_COMMIT, temporary_root / "previous")
        shutil.copy2(generated_runtime_path, previous_source / "internal" / "embedding" / "llama.go")
        previous_overlay = _remap_overlay(repository, previous_source, previous_candidate_root / "go-overlay.json", temporary_root / "previous-overlay.json")
        composition = base64.b64encode((previous_candidate_root / "composition.json").read_bytes()).decode("ascii")
        _build(
            previous_source,
            binary_paths["previous-v2"],
            (
                f"-X main.version={previous_receipt['kernel_generation_identity']} "
                f"-X github.com/HJSunDev/ownward/manifests/compositions/v1.sealedCompositionBase64={composition}"
            ),
            overlay=previous_overlay,
        )

    shutil.copy2(final_candidate_root / "ownward.exe", binary_paths["candidate"])
    shutil.copytree(final_candidate_root / "embedding", embedding_path, copy_function=_link_or_copy)
    content = {
        "schema": RECEIPT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "runtime_configuration": dict(RUNTIME_CONFIGURATION),
        "runtime_source_sha256": runtime_source_sha,
        "runtime_base_source_sha256": evidence.file_sha256(runtime_base_source),
        "runtime_transform_sha256": evidence.file_sha256(runtime_transform),
        "builder_sha256": evidence.file_sha256(Path(__file__).resolve()),
        "sources": {
            "v0": {"git_audit": v0_commit, "kernel_generation_identity": v0["kernel_generation_identity"]},
            "previous-v2": {
                "git_audit": PREVIOUS_SOURCE_COMMIT,
                "subject_identity": previous_receipt["subject_identity"],
                "kernel_generation_identity": previous_receipt["kernel_generation_identity"],
                "kernel_effect_identity": previous_receipt["kernel_effect_identity"],
                "candidate_receipt_sha256": evidence.file_sha256(previous_candidate_root / "candidate-receipt.json"),
                "composition_sha256": evidence.file_sha256(previous_candidate_root / "composition.json"),
                "source_overlay_sha256": evidence.file_sha256(previous_candidate_root / "go-overlay.json"),
            },
            "candidate": {
                "subject_identity": final_receipt["subject_identity"],
                "kernel_generation_identity": final_receipt["kernel_generation_identity"],
                "kernel_effect_identity": final_receipt["kernel_effect_identity"],
                "candidate_receipt_sha256": evidence.file_sha256(final_candidate_root / "candidate-receipt.json"),
                "composition_sha256": evidence.file_sha256(final_candidate_root / "composition.json"),
                "source_overlay_sha256": evidence.file_sha256(final_candidate_root / "go-overlay.json"),
            },
        },
        "binary_sha256": {name: evidence.file_sha256(path) for name, path in binary_paths.items()},
        "binary_paths": {name: str(path) for name, path in binary_paths.items()},
        "embedding_path": str(embedding_path),
        "embedding_manifest_sha256": evidence.file_sha256(embedding_path / "manifest.json"),
        "same_model_space_and_exact_query": True,
    }
    value = {**content, "identity": evidence.canonical_sha256(content)}
    evidence.atomic_json(receipt_path, value)
    return {**value, "path": str(receipt_path), "reused": False}


def _build(source: Path, output: Path, ldflags: str, *, overlay: Path | None = None) -> None:
    runtime_flags = (
        f" -X github.com/HJSunDev/ownward/internal/embedding.managedRuntimeThreads={RUNTIME_CONFIGURATION['threads']}"
        f" -X github.com/HJSunDev/ownward/internal/embedding.managedRuntimeParallel={RUNTIME_CONFIGURATION['parallel']}"
    )
    command = ["go", "build", "-trimpath"]
    if overlay is not None:
        command.extend(["-overlay", str(overlay)])
    command.extend(["-ldflags", ldflags + runtime_flags, "-o", str(output), "./cmd/ownward"])
    result = subprocess.run(command, cwd=source, capture_output=True, text=True, encoding="utf-8", timeout=300, check=False)
    _require(result.returncode == 0 and output.is_file(), f"构建共享向量运行时比较制品失败: {result.stderr.strip()}")


def _extract_commit(repository: Path, commit: str, destination: Path) -> Path:
    archive = destination.with_suffix(".tar")
    result = subprocess.run(
        ["git", "archive", "--format=tar", "-o", str(archive), commit],
        cwd=repository, capture_output=True, text=True, encoding="utf-8", timeout=120, check=False,
    )
    _require(result.returncode == 0 and archive.is_file(), f"读取历史审计源码失败: {commit}: {result.stderr.strip()}")
    destination.mkdir(parents=True)
    with tarfile.open(archive, "r:") as bundle:
        for member in bundle.getmembers():
            path = PurePosixPath(member.name)
            _require(not path.is_absolute() and ".." not in path.parts and not member.issym() and not member.islnk(), "历史源码归档包含越界路径")
        bundle.extractall(destination)
    archive.unlink()
    return destination


def _remap_overlay(repository: Path, source: Path, original_path: Path, destination: Path) -> Path:
    original = _load_json(original_path)
    replacements = original.get("Replace")
    _require(isinstance(replacements, dict) and replacements, "前序 V2 overlay 无效")
    remapped: dict[str, str] = {}
    for original_source, replacement in replacements.items():
        source_path, replacement_path = Path(original_source).resolve(), Path(replacement).resolve()
        _require(source_path.is_relative_to(repository) and replacement_path.is_file(), "前序 V2 overlay 越界或缺失")
        remapped[str((source / source_path.relative_to(repository)).resolve())] = str(replacement_path)
    destination.write_text(json.dumps({"Replace": remapped}, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return destination


def _validate_previous_candidate(root: Path) -> dict[str, Any]:
    receipt = _load_json(root / "candidate-receipt.json")
    subject = _load_json(root / "subject.json")
    _require(receipt.get("subject_identity") == subject.get("identity") == "7b78a0ef7036f1c6c34665162262cefbc98d9c3819f736616a7aeea3e4e74fbd", "前序 V2 subject 错绑")
    for field, name in (("binary_sha256", "ownward.exe"), ("composition_sha256", "composition.json"), ("overlay_sha256", "go-overlay.json"), ("generated_service_sha256", "core-service.go.overlay")):
        _require(receipt.get(field) == evidence.file_sha256(root / name), f"前序 V2 制品漂移: {field}")
    return receipt


def _validate_final_candidate(root: Path) -> dict[str, Any]:
    receipt = _load_json(root / "candidate-receipt.json")
    subject = _load_json(root / "subject.json")
    _require(receipt.get("subject_identity") == subject.get("identity"), "最终候选 subject 错绑")
    _require(receipt.get("embedding_runtime_configuration") == RUNTIME_CONFIGURATION, "最终候选没有冻结 6/2 共享向量运行时")
    _require(receipt.get("binary_sha256") == evidence.file_sha256(root / "ownward.exe"), "最终候选二进制漂移")
    return receipt


def _validate_receipt(value: dict[str, Any], repository: Path, previous: Path, final: Path, binaries: dict[str, Path], embedding_path: Path, generated_runtime_path: Path) -> None:
    _require(value.get("schema") == RECEIPT_SCHEMA, "共享向量运行时收据 schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), "共享向量运行时收据身份漂移")
    _require(value.get("builder_sha256") == evidence.file_sha256(Path(__file__).resolve()), "共享向量运行时构建器漂移")
    runtime_base = repository / "internal" / "embedding" / "llama.go"
    runtime_transform = repository / "manifests" / "kernel-candidates" / "v2" / "retrieval-latency" / "managed-runtime-transform.json"
    _require(value.get("runtime_source_sha256") == evidence.file_sha256(generated_runtime_path), "共享向量运行时生成源码漂移")
    _require(value.get("runtime_base_source_sha256") == evidence.file_sha256(runtime_base), "共享向量运行时基础源码漂移")
    _require(value.get("runtime_transform_sha256") == evidence.file_sha256(runtime_transform), "共享向量运行时转换身份漂移")
    _validate_previous_candidate(previous)
    _validate_final_candidate(final)
    _require(value.get("binary_sha256") == {name: evidence.file_sha256(path) for name, path in binaries.items()}, "共享向量运行时比较二进制漂移")
    _require(value.get("embedding_path") == str(embedding_path) and value.get("embedding_manifest_sha256") == evidence.file_sha256(embedding_path / "manifest.json"), "共享向量运行时向量包漂移")


def _link_or_copy(source: str, destination: str) -> str:
    try:
        os.link(source, destination)
        return destination
    except OSError:
        return shutil.copy2(source, destination)


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取共享向量运行时制品 {path}: {error}") from error
    _require(isinstance(value, dict), f"共享向量运行时制品不是对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
