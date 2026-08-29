from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor
import hashlib
import http.client
import json
import math
import os
from pathlib import Path
import socket
import statistics
import struct
import subprocess
import threading
import time
from typing import Any
from urllib import request

import psutil

import kernel_iteration_evidence as evidence
import kernel_iteration_validation as validation


RESULT_SCHEMA = "ownward.kernel-iteration-stage4-vector-runtime-calibration/v1"
CONFIGURATIONS = ((2, 1), (4, 1), (6, 1), (2, 2), (4, 2), (6, 2))
QUERIES = (
    "Which current maintenance note supersedes the earlier storage instruction?",
    "查找说明最终交付位置和有效时间范围的当前记录。",
    "Locate the independently verified procedure for resolving a conflicting update.",
    "检索同时包含责任人、截止日期与适用条件的权威说明。",
)


def calibrate(bundle_root: Path, output_path: Path) -> dict[str, Any]:
    bundle_root, output_path = bundle_root.resolve(), output_path.resolve()
    manifest_path = bundle_root / "manifest.json"
    manifest = _load_json(manifest_path)
    runtime = bundle_root / str(manifest["runtime"]["entry"])
    model = bundle_root / str(manifest["model"]["path"])
    _require(runtime.is_file() and model.is_file(), "向量校准制品不完整")
    _require(not output_path.exists(), "向量校准结果已存在；禁止选择性覆盖")

    reference: dict[str, list[float]] | None = None
    results: list[dict[str, Any]] = []
    for threads, parallel in CONFIGURATIONS:
        with _Server(runtime, model, threads, parallel) as server:
            server.vector(QUERIES[0])
            vectors = {query: server.vector(query) for query in QUERIES}
            if reference is None:
                reference = vectors
            maximum_drift = max(
                abs(actual - expected)
                for query in QUERIES
                for actual, expected in zip(vectors[query], reference[query])
            )
            sequential = _measure(lambda index: server.vector(QUERIES[index % len(QUERIES)]), 12, workers=1)
            concurrent = _measure(lambda index: server.vector(QUERIES[index % len(QUERIES)]), 16, workers=2)
            results.append({
                "threads": threads,
                "parallel": parallel,
                "startup_ms": server.startup_ms,
                "single": _summary(sequential),
                "concurrent_two": _summary(concurrent),
                "throughput_queries_per_second": 16000.0 / sum(concurrent),
                "peak_working_set_bytes": server.peak_working_set_bytes,
                "maximum_vector_component_drift": maximum_drift,
                "vector_sha256": {query: _vector_sha256(vector) for query, vector in vectors.items()},
            })

    stable = [item for item in results if float(item["maximum_vector_component_drift"]) <= 1e-6]
    _require(len(stable) == len(results), "运行配置导致向量结果漂移")
    selected = min(stable, key=lambda item: (
        float(item["concurrent_two"]["p95_ms"]),
        -float(item["throughput_queries_per_second"]),
        int(item["threads"]),
        int(item["parallel"]),
    ))
    content = {
        "schema": RESULT_SCHEMA,
        "formal": False,
        "model_or_prompt_changed": False,
        "vector_space_changed": False,
        "model_sha256": evidence.file_sha256(model),
        "runtime_sha256": evidence.file_sha256(runtime),
        "manifest_sha256": evidence.file_sha256(manifest_path),
        "controller_sha256": evidence.file_sha256(Path(__file__).resolve()),
        "machine": {"logical_processors": os.cpu_count()},
        "representative_scale": {"authoritative_assets_per_question": 50, "formal_observed_range": [38, 62]},
        "queries_sha256": evidence.canonical_sha256(list(QUERIES)),
        "configurations": results,
        "selected": {"threads": selected["threads"], "parallel": selected["parallel"]},
    }
    value = {**content, "identity": evidence.canonical_sha256(content)}
    evidence.atomic_json(output_path, value)
    return value


class _Server:
    def __init__(self, runtime: Path, model: Path, threads: int, parallel: int) -> None:
        self.runtime, self.model = runtime, model
        self.threads, self.parallel = threads, parallel
        self.port = _available_port()
        self.process: subprocess.Popen[bytes] | None = None
        self.startup_ms = 0.0
        self.peak_working_set_bytes = 0
        self._connections: list[http.client.HTTPConnection] = []
        self._connections_lock = threading.Lock()
        self._thread_connection = threading.local()

    def __enter__(self) -> "_Server":
        command = [
            str(self.runtime), "-m", str(self.model), "--embeddings", "--pooling", "mean", "--embd-normalize", "2",
            "--host", "127.0.0.1", "--port", str(self.port), "--threads", str(self.threads),
            "--threads-batch", str(self.threads), "--parallel", str(self.parallel), "--ctx-size", "512",
            "--batch-size", "512", "--ubatch-size", "512", "--no-warmup", "--no-webui", "--log-disable",
        ]
        started = time.perf_counter()
        self.process = subprocess.Popen(
            command, cwd=self.runtime.parent, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
            creationflags=subprocess.CREATE_NO_WINDOW if os.name == "nt" else 0,
        )
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            if self.process.poll() is not None:
                raise validation.KernelIterationValidationError("向量运行时提前退出")
            try:
                if _json_request(self.port, "GET", "/health", None).get("status") == "ok":
                    self.startup_ms = (time.perf_counter() - started) * 1000
                    return self
            except OSError:
                pass
            time.sleep(0.05)
        raise validation.KernelIterationValidationError("向量运行时启动超时")

    def __exit__(self, *_: object) -> None:
        if self.process is None:
            return
        self._sample_memory()
        with self._connections_lock:
            for connection in self._connections:
                connection.close()
            self._connections.clear()
        self.process.terminate()
        try:
            self.process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait(timeout=10)

    def vector(self, value: str) -> list[float]:
        payload = self._post({"input": ["task: search result | query: " + value], "model": "embeddinggemma"})
        data = payload.get("data")
        _require(isinstance(data, list) and len(data) == 1, "向量运行时返回数量无效")
        vector = data[0].get("embedding") if isinstance(data[0], dict) else None
        _require(isinstance(vector, list) and len(vector) >= 512, "向量运行时返回维度无效")
        result = [float(item) for item in vector[:512]]
        self._sample_memory()
        norm = math.sqrt(sum(item * item for item in result))
        _require(norm > 0, "向量运行时返回零向量")
        return [item / norm for item in result]

    def _post(self, body: dict[str, Any]) -> dict[str, Any]:
        connection = getattr(self._thread_connection, "value", None)
        if connection is None:
            connection = http.client.HTTPConnection("127.0.0.1", self.port, timeout=60)
            connection.connect()
            connection.sock.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
            self._thread_connection.value = connection
            with self._connections_lock:
                self._connections.append(connection)
        encoded = json.dumps(body, ensure_ascii=False).encode("utf-8")
        connection.request("POST", "/v1/embeddings", body=encoded, headers={"Content-Type": "application/json"})
        response = connection.getresponse()
        content = response.read()
        _require(200 <= response.status < 300, f"向量运行时返回 HTTP {response.status}")
        value = json.loads(content)
        _require(isinstance(value, dict), "向量运行时返回不是对象")
        return value

    def _sample_memory(self) -> None:
        if self.process is None:
            return
        try:
            memory = psutil.Process(self.process.pid).memory_info()
            current = int(getattr(memory, "peak_wset", memory.rss))
            self.peak_working_set_bytes = max(self.peak_working_set_bytes, current)
        except (psutil.NoSuchProcess, psutil.AccessDenied):
            pass


def _measure(operation: Any, count: int, *, workers: int) -> list[float]:
    def timed(index: int) -> float:
        started = time.perf_counter()
        operation(index)
        return (time.perf_counter() - started) * 1000
    if workers == 1:
        return [timed(index) for index in range(count)]
    with ThreadPoolExecutor(max_workers=workers) as pool:
        return list(pool.map(timed, range(count)))


def _summary(values: list[float]) -> dict[str, float]:
    ordered = sorted(values)
    return {
        "samples": len(values),
        "mean_ms": statistics.fmean(values),
        "p95_ms": ordered[min(len(ordered) - 1, math.ceil(len(ordered) * 0.95) - 1)],
        "max_ms": ordered[-1],
    }


def _json_request(port: int, method: str, path: str, body: object | None) -> dict[str, Any]:
    encoded = None if body is None else json.dumps(body, ensure_ascii=False).encode("utf-8")
    message = request.Request(
        f"http://127.0.0.1:{port}{path}", data=encoded, method=method,
        headers={"Content-Type": "application/json"},
    )
    with request.build_opener(request.ProxyHandler({})).open(message, timeout=60) as response:
        value = json.loads(response.read())
    _require(isinstance(value, dict), "向量运行时返回不是对象")
    return value


def _available_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
        listener.bind(("127.0.0.1", 0))
        return int(listener.getsockname()[1])


def _vector_sha256(vector: list[float]) -> str:
    digest = hashlib.sha256()
    for item in vector:
        digest.update(struct.pack("<f", item))
    return digest.hexdigest()


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取向量校准清单: {error}") from error
    _require(isinstance(value, dict), "向量校准清单不是对象")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
