from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
from typing import Any

import kernel_iteration_evidence as evidence
import kernel_iteration_validation as validation


RESULT_SCHEMA = "ownward.kernel-iteration-stage4-runtime-implementation-assessment/v1"


def assess(
    repository: Path,
    probe_root: Path,
    output_path: Path,
    formal_state: Path,
) -> dict[str, Any]:
    repository, probe_root = repository.resolve(), probe_root.resolve()
    output_path, formal_state = output_path.resolve(), formal_state.resolve()
    _require(output_path.is_relative_to(repository / ".tmp"), "运行实现评估只能写入非正式 .tmp 边界")
    _require(not output_path.exists(), "运行实现评估已存在；禁止选择性覆盖")
    state_before = evidence.file_sha256(formal_state)
    _require(state_before == "3c7826e0ef86a82ddfab676886e384e2674a23dc80f6df3612d4443be00ffcdc", "运行实现评估前正式 state 漂移")

    result_paths = {
        "cpu": repository / ".tmp/kernel-v2-major-iteration/stage4-retrieval-latency/vector-runtime-followup-v1.json",
        "vulkan": repository / ".tmp/kernel-v2-major-iteration/stage4-retrieval-latency/runtime-implementation-vulkan-b10488.json",
        "openvino": repository / ".tmp/kernel-v2-major-iteration/stage4-retrieval-latency/runtime-implementation-openvino-b10488.json",
    }
    results = {name: _load_json(path) for name, path in result_paths.items()}
    _require(results["cpu"].get("identity") == "94e543b7369cc3950bec9f18c0eda9b49b511475332e60e3899de2f7273342f9", "CPU 下界证据漂移")
    _require(results["vulkan"].get("identity") == "5f0eb06b366a605be3658e65cea6b24c222c07f8a4a7cad23847bdb4b15550a8", "Vulkan 下界证据漂移")
    _require(results["openvino"].get("identity") == "838ef5d583c4311b605d57068d8678231ea35cb17286b007b2244b4ed03faa22", "OpenVINO 下界证据漂移")

    sycl_archive = probe_root / "llama-b10488-bin-win-sycl-x64.zip"
    sycl_server = probe_root / "sycl-b10488/llama-server.exe"
    _require(evidence.file_sha256(sycl_archive) == "a243e54915d7582793dc4697169103bcbf33f475667893891862390c11c7ee2d", "SYCL 官方制品摘要漂移")
    sycl_devices = _command([str(sycl_server), "--list-devices"])
    _require("(none)" in sycl_devices.lower(), "SYCL 静态淘汰证据不成立")

    nvidia_smi = _command(["nvidia-smi"])
    _require("Driver Version: 442.62" in nvidia_smi and "CUDA Version: 10.2" in nvidia_smi, "CUDA 驱动事实漂移")
    vulkan_devices = _command([str(probe_root / "vulkan-b10488/llama-server.exe"), "--list-devices"])
    _require("Intel(R) UHD Graphics" in vulkan_devices and "MX350" not in vulkan_devices, "Vulkan 设备事实漂移")
    openvino_devices = _command([str(probe_root / "openvino-b10488/llama-server.exe"), "--list-devices"])
    _require("OPENVINO0" in openvino_devices, "OpenVINO 设备事实漂移")

    state_after = evidence.file_sha256(formal_state)
    _require(state_after == state_before, "运行实现评估改写了正式 state")
    content = {
        "schema": RESULT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "controller_sha256": evidence.file_sha256(Path(__file__).resolve()),
        "machine": {
            "platform": platform.platform(),
            "logical_processors": os.cpu_count(),
            "nvidia_smi_sha256": hashlib.sha256(nvidia_smi.encode("utf-8")).hexdigest(),
            "nvidia_driver": "442.62",
            "advertised_cuda": "10.2",
            "visible_graphics": ["Intel(R) UHD Graphics", "NVIDIA GeForce MX350 2GB"],
        },
        "source_release": {
            "repository": "https://github.com/ggml-org/llama.cpp",
            "tag": "b10488",
            "commit": "9d77fa17254e1dee4b9e92504c91611a60b1359f",
        },
        "fixed_model": {
            "sha256": "6fa0c02a9c302be6f977521d399b4de3a46310a4f2621ee0063747881b673f67",
            "space_id": "emb_79f072bf21c0c0f5226fa4fe6f1946a5",
            "material": "embeddinggemma-300m-qat-Q8_0.gguf",
        },
        "gates": {
            "mean_ms_maximum_exclusive": 41.201,
            "p95_ms_maximum": 62.4,
            "maximum_vector_component_drift": 0.0000001,
        },
        "implementations": [
            {
                "name": "llamacpp-b10488-cpu-x64",
                "result_identity": results["cpu"]["identity"],
                "mean_ms": results["cpu"]["isolated_selected_runtime_mean_ms"],
                "p95_ms": next(item["p95_ms"] for item in results["cpu"]["trials"] if item["name"] == "isolated-selected-6-2"),
                "status": "rejected-lower-bound",
            },
            {
                "name": "llamacpp-b10488-vulkan-x64",
                "official_archive_sha256": results["vulkan"]["official_archive_sha256"],
                "result_identity": results["vulkan"]["identity"],
                "mean_ms": results["vulkan"]["summary"]["mean_ms"],
                "p95_ms": results["vulkan"]["summary"]["p95_ms"],
                "maximum_vector_component_drift": results["vulkan"]["maximum_vector_component_drift"],
                "visible_devices_sha256": hashlib.sha256(vulkan_devices.encode("utf-8")).hexdigest(),
                "status": "rejected-latency-and-vector-drift",
            },
            {
                "name": "llamacpp-b10488-openvino-2026.3-x64",
                "official_archive_sha256": results["openvino"]["official_archive_sha256"],
                "result_identity": results["openvino"]["identity"],
                "failure": results["openvino"]["failure"],
                "peak_working_set_bytes": results["openvino"]["peak_working_set_bytes"],
                "visible_devices_sha256": hashlib.sha256(openvino_devices.encode("utf-8")).hexdigest(),
                "status": "rejected-query-failure",
            },
            {
                "name": "llamacpp-b10488-sycl-x64",
                "official_archive_sha256": evidence.file_sha256(sycl_archive),
                "visible_devices_sha256": hashlib.sha256(sycl_devices.encode("utf-8")).hexdigest(),
                "status": "rejected-no-device",
            },
            {
                "name": "llamacpp-b10488-cuda-12.4-or-13-x64",
                "status": "rejected-driver-runtime-incompatibility",
                "reason": "official-runtime-requires-newer-cuda-driver-than-machine-442.62-cuda-10.2",
            },
            {
                "name": "other-model-runtimes",
                "status": "ineligible-fixed-material-and-precision-contract",
                "reason": "installed-candidate-material-is-q8_0-gguf-and-no-other-local-runtime-loads-that-exact-material-with-the-frozen-1e-7-vector-contract",
            },
        ],
        "all_realistic_fixed_model_runtime_implementations_rejected": True,
        "retrieval_latency_status": "open",
        "next_validation": "freeze-a-more-efficient-semantic-representation-model-that-must-reprove-all-quality-protections-before-any-candidate-adoption",
        "formal_state_sha256_before": state_before,
        "formal_state_sha256_after": state_after,
    }
    value = {**content, "identity": evidence.canonical_sha256(content)}
    evidence.atomic_json(output_path, value)
    return {**value, "path": str(output_path)}


def _command(command: list[str]) -> str:
    completed = subprocess.run(command, check=True, capture_output=True, text=True, encoding="utf-8", errors="replace", timeout=30)
    return completed.stdout + completed.stderr


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取运行实现证据 {path}: {error}") from error
    _require(isinstance(value, dict), f"运行实现证据不是对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
