from __future__ import annotations

import hashlib
import json
import math
import os
from pathlib import Path
import statistics
import struct
import subprocess
import tempfile
import time
from typing import Any

import kernel_iteration_evidence as evidence
import kernel_iteration_stage4_vector_runtime as vector_runtime
import kernel_iteration_validation as validation


CONTRACT_SCHEMA = "ownward.kernel-iteration-stage4-runtime-implementation-contract/v1"
RESULT_SCHEMA = "ownward.kernel-iteration-stage4-runtime-implementation-result/v1"
QUERIES = vector_runtime.QUERIES


def probe(
    suite_root: Path,
    reference_bundle: Path,
    implementation_root: Path,
    archive_path: Path,
    implementation: str,
    output_path: Path,
    formal_state: Path,
) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    repository = suite_root.parents[2]
    reference_bundle = reference_bundle.resolve()
    implementation_root = implementation_root.resolve()
    archive_path = archive_path.resolve()
    output_path = output_path.resolve()
    formal_state = formal_state.resolve()
    _require(output_path.is_relative_to(repository / ".tmp"), "运行实现探针只能写入非正式 .tmp 边界")
    _require(not output_path.exists(), "运行实现探针结果已存在；禁止选择性覆盖")

    contract_path = suite_root / "iteration" / "v2" / "stage4-retrieval-latency-runtime-implementation-contract.json"
    contract = _load_json(contract_path)
    _require(contract.get("schema") == CONTRACT_SCHEMA, "运行实现探针合同 schema 无效")
    _require(contract.get("frozen_before_measurement") is True, "运行实现探针合同未在结果前冻结")
    implementation_contract = contract.get("implementations", {}).get(implementation)
    _require(isinstance(implementation_contract, dict), "运行实现没有冻结合同")
    _require("static_rejection" not in implementation_contract, "静态淘汰实现不得启动探针")
    _require(
        evidence.file_sha256(archive_path) == implementation_contract.get("official_archive_sha256"),
        "运行实现官方制品摘要漂移",
    )

    reference_manifest_path = reference_bundle / "manifest.json"
    reference_manifest = _load_json(reference_manifest_path)
    reference = contract["reference"]
    _require(evidence.file_sha256(reference_manifest_path) == reference["embedding_manifest_sha256"], "参考向量清单漂移")
    model = reference_bundle / str(reference_manifest["model"]["path"])
    reference_runtime = reference_bundle / str(reference_manifest["runtime"]["entry"])
    _require(evidence.file_sha256(model) == reference["model_sha256"], "参考模型漂移")
    _require(evidence.canonical_sha256(list(QUERIES)) == reference["queries_sha256"], "隔离查询集合漂移")

    runtime = implementation_root / "llama-server.exe"
    _require(runtime.is_file(), "运行实现缺少 llama-server.exe")
    state_before = evidence.file_sha256(formal_state)
    _require(state_before == contract["formal_state_sha256"], "运行实现探针前正式 state 漂移")

    measurement = contract["measurement"]
    with vector_runtime._Server(reference_runtime, model, int(measurement["threads"]), 1) as server:
        reference_vectors = {query: server.vector(query) for query in QUERIES}

    command_tail = [
        "--device", str(implementation_contract["device"]),
        "--n-gpu-layers", str(implementation_contract["gpu_layers"]),
    ]
    with _ImplementationServer(
        runtime,
        model,
        int(measurement["threads"]),
        int(measurement["parallel"]),
        command_tail,
    ) as server:
        for index in range(int(measurement["warmups"])):
            server.vector(QUERIES[index % len(QUERIES)])
        measured: list[float] = []
        candidate_vectors: dict[str, list[float]] = {}
        for index in range(int(measurement["repetitions"])):
            query = QUERIES[index % len(QUERIES)]
            started = time.perf_counter()
            candidate_vectors[query] = server.vector(query)
            measured.append((time.perf_counter() - started) * 1000)
        peak_working_set_bytes = server.peak_working_set_bytes
    runtime_log = server.runtime_log

    maximum_drift = max(
        abs(actual - expected)
        for query in QUERIES
        for actual, expected in zip(candidate_vectors[query], reference_vectors[query])
    )
    summary = _summary(measured)
    gates = contract["gates"]
    mean_passed = float(summary["mean_ms"]) < float(gates["retrieval_mean_ms_maximum_exclusive"])
    p95_passed = float(summary["p95_ms"]) <= float(gates["exact_query_p95_ms_maximum"])
    drift_passed = maximum_drift <= float(gates["maximum_vector_component_drift"])
    offload_proven = "offloaded" in runtime_log.lower() and "vulkan0" in runtime_log.lower()
    state_after = evidence.file_sha256(formal_state)
    _require(state_after == state_before, "运行实现探针改写了正式 state")

    content = {
        "schema": RESULT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "contract_sha256": evidence.file_sha256(contract_path),
        "implementation": implementation,
        "official_archive_sha256": evidence.file_sha256(archive_path),
        "runtime_sha256": evidence.file_sha256(runtime),
        "reference_runtime_sha256": evidence.file_sha256(reference_runtime),
        "model_sha256": evidence.file_sha256(model),
        "space_identity": reference["space_id"],
        "queries_sha256": reference["queries_sha256"],
        "measurement": measurement,
        "summary": summary,
        "maximum_vector_component_drift": maximum_drift,
        "vector_sha256": {query: _vector_sha256(candidate_vectors[query]) for query in QUERIES},
        "reference_vector_sha256": {query: _vector_sha256(reference_vectors[query]) for query in QUERIES},
        "peak_working_set_bytes": peak_working_set_bytes,
        "runtime_log_sha256": hashlib.sha256(runtime_log.encode("utf-8")).hexdigest(),
        "offload_proven": offload_proven,
        "gates": {
            "mean_passed": mean_passed,
            "p95_passed": p95_passed,
            "drift_passed": drift_passed,
            "offload_proven": offload_proven,
        },
        "passed": mean_passed and p95_passed and drift_passed and offload_proven,
        "formal_state_sha256_before": state_before,
        "formal_state_sha256_after": state_after,
    }
    value = {**content, "identity": evidence.canonical_sha256(content)}
    evidence.atomic_json(output_path, value)
    return {**value, "path": str(output_path)}


class _ImplementationServer(vector_runtime._Server):
    def __init__(self, runtime: Path, model: Path, threads: int, parallel: int, command_tail: list[str]) -> None:
        super().__init__(runtime, model, threads, parallel)
        self.command_tail = command_tail
        self.runtime_log = ""
        self._log_path: Path | None = None
        self._log_handle: Any = None

    def __enter__(self) -> "_ImplementationServer":
        command = [
            str(self.runtime), "-m", str(self.model), "--embeddings", "--pooling", "mean", "--embd-normalize", "2",
            "--host", "127.0.0.1", "--port", str(self.port), "--threads", str(self.threads),
            "--threads-batch", str(self.threads), "--parallel", str(self.parallel), "--ctx-size", "512",
            "--batch-size", "512", "--ubatch-size", "512", "--no-warmup", "--no-webui",
            *self.command_tail,
        ]
        handle, name = tempfile.mkstemp(prefix="ownward-runtime-probe-", suffix=".log")
        os.close(handle)
        self._log_path = Path(name)
        self._log_handle = self._log_path.open("wb")
        started = time.perf_counter()
        self.process = subprocess.Popen(
            command,
            cwd=self.runtime.parent,
            stdout=self._log_handle,
            stderr=subprocess.STDOUT,
            creationflags=subprocess.CREATE_NO_WINDOW if os.name == "nt" else 0,
        )
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            if self.process.poll() is not None:
                self._close_log()
                raise validation.KernelIterationValidationError("运行实现提前退出: " + self.runtime_log[-2000:])
            try:
                if vector_runtime._json_request(self.port, "GET", "/health", None).get("status") == "ok":
                    self.startup_ms = (time.perf_counter() - started) * 1000
                    return self
            except OSError:
                pass
            time.sleep(0.05)
        super().__exit__()
        self._close_log()
        raise validation.KernelIterationValidationError("运行实现启动超时: " + self.runtime_log[-2000:])

    def __exit__(self, *args: object) -> None:
        super().__exit__(*args)
        self._close_log()

    def _close_log(self) -> None:
        if self._log_handle is not None:
            self._log_handle.close()
            self._log_handle = None
        if self._log_path is not None and self._log_path.exists():
            self.runtime_log = self._log_path.read_text(encoding="utf-8", errors="replace")
            self._log_path.unlink()


def _summary(values: list[float]) -> dict[str, float | int]:
    ordered = sorted(values)
    return {
        "samples": len(values),
        "mean_ms": statistics.fmean(values),
        "p95_ms": ordered[min(len(ordered) - 1, math.ceil(len(ordered) * 0.95) - 1)],
        "max_ms": ordered[-1],
    }


def _vector_sha256(vector: list[float]) -> str:
    digest = hashlib.sha256()
    for item in vector:
        digest.update(struct.pack("<f", item))
    return digest.hexdigest()


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取运行实现输入 {path}: {error}") from error
    _require(isinstance(value, dict), f"运行实现输入不是对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
