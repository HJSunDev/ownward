from __future__ import annotations

import hashlib
import json
import math
import os
import statistics
import subprocess
import threading
import time
import urllib.request
from pathlib import Path
from typing import Iterable, Sequence

import numpy as np
import psutil


ROOT = Path(r"E:\Dev\ownward\.tmp\vector-model-evaluation")
BASE = ROOT / "llama-q8"
SERVER = BASE / "runtime" / "llama-server.exe"
MODEL = BASE / "embeddinggemma-300m-qat-Q8_0.gguf"
RESULTS = ROOT / "results" / "embeddinggemma-runtime-optimization" / "llama-q8"
STATE = ROOT / "state" / "embeddinggemma-runtime-optimization"
ONNX_RESULTS = ROOT / "results" / "embeddinggemma-runtime-optimization" / "onnx-formal.json"
PORT = 18089
AFFINITY = [0, 2, 4, 6]
DIMENSION = 512
SETTLING_SECONDS = 10
IDLE_SECONDS = 60


def write_json(path: Path, value: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(
        json.dumps(value, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
        newline="\n",
    )
    temporary.replace(path)


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(8 * 1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def percentile(values: Sequence[float], value: float) -> float:
    return float(np.percentile(np.asarray(values, dtype=np.float64), value))


def process_tree(root: psutil.Process) -> list[psutil.Process]:
    try:
        return [root, *root.children(recursive=True)]
    except (psutil.NoSuchProcess, psutil.AccessDenied):
        return []


def rss_bytes(root: psutil.Process) -> int:
    total = 0
    for process in process_tree(root):
        try:
            total += process.memory_info().rss
        except (psutil.NoSuchProcess, psutil.AccessDenied):
            pass
    return total


def cpu_seconds(root: psutil.Process) -> float:
    total = 0.0
    for process in process_tree(root):
        try:
            times = process.cpu_times()
            total += times.user + times.system
        except (psutil.NoSuchProcess, psutil.AccessDenied):
            pass
    return total


class MemorySampler:
    def __init__(self, process: psutil.Process) -> None:
        self.process = process
        self.peak = 0
        self.running = True
        self.thread = threading.Thread(target=self._run, daemon=True)
        self.thread.start()

    def _run(self) -> None:
        while self.running:
            self.peak = max(self.peak, rss_bytes(self.process))
            time.sleep(0.02)

    def reset(self) -> None:
        self.peak = rss_bytes(self.process)

    def stop(self) -> None:
        self.running = False
        self.thread.join(timeout=2)
        self.peak = max(self.peak, rss_bytes(self.process))


class LlamaServer:
    def __init__(self, threads: int, label: str) -> None:
        self.threads = threads
        self.label = label
        self.process: subprocess.Popen[bytes] | None = None
        self.ps_process: psutil.Process | None = None
        self.sampler: MemorySampler | None = None
        self.started_ms = 0.0

    def __enter__(self) -> "LlamaServer":
        stdout = (RESULTS / f"{self.label}.stdout.log").open("wb")
        stderr = (RESULTS / f"{self.label}.stderr.log").open("wb")
        command = [
            str(SERVER),
            "-m",
            str(MODEL),
            "--embeddings",
            "--pooling",
            "mean",
            "--embd-normalize",
            "2",
            "--host",
            "127.0.0.1",
            "--port",
            str(PORT),
            "--threads",
            str(self.threads),
            "--threads-batch",
            str(self.threads),
            "--parallel",
            "1",
            "--ctx-size",
            "512",
            "--batch-size",
            "512",
            "--ubatch-size",
            "512",
            "--no-warmup",
            "--no-webui",
            "--log-disable",
        ]
        started = time.perf_counter()
        self.process = subprocess.Popen(
            command,
            cwd=SERVER.parent,
            stdout=stdout,
            stderr=stderr,
            creationflags=subprocess.CREATE_NO_WINDOW,
        )
        self.ps_process = psutil.Process(self.process.pid)
        available = self.ps_process.cpu_affinity()
        selected = [item for item in AFFINITY if item in available]
        if selected:
            self.ps_process.cpu_affinity(selected)
        self.sampler = MemorySampler(self.ps_process)
        deadline = time.perf_counter() + 30
        while time.perf_counter() < deadline:
            if self.process.poll() is not None:
                raise RuntimeError(f"llama-server 启动失败：exit={self.process.returncode}")
            try:
                response = self.request_json("GET", "/health", None, timeout=1)
                if response.get("status") == "ok":
                    self.started_ms = (time.perf_counter() - started) * 1000
                    return self
            except Exception:
                pass
            time.sleep(0.05)
        raise RuntimeError("llama-server 30 秒内未就绪")

    def __exit__(self, exc_type: object, exc: object, traceback: object) -> None:
        if self.sampler is not None:
            self.sampler.stop()
        if self.process is not None:
            self.process.terminate()
            try:
                self.process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=10)

    @staticmethod
    def request_json(method: str, path: str, body: object | None, timeout: float) -> dict[str, object]:
        data = None if body is None else json.dumps(body, ensure_ascii=False).encode("utf-8")
        request = urllib.request.Request(
            f"http://127.0.0.1:{PORT}{path}",
            data=data,
            method=method,
            headers={"Content-Type": "application/json"},
        )
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        with opener.open(request, timeout=timeout) as response:
            return json.loads(response.read())

    def encode(self, texts: Sequence[str], kind: str, batch_size: int = 8) -> np.ndarray:
        prefix = "task: search result | query: " if kind == "query" else "title: none | text: "
        output: list[list[float]] = []
        for start in range(0, len(texts), batch_size):
            inputs = [prefix + text for text in texts[start : start + batch_size]]
            response = self.request_json(
                "POST",
                "/v1/embeddings",
                {"input": inputs, "model": "embeddinggemma"},
                timeout=180,
            )
            records = sorted(response["data"], key=lambda item: int(item["index"]))
            output.extend(record["embedding"] for record in records)
        vectors = np.asarray(output, dtype=np.float32)[:, :DIMENSION]
        norms = np.linalg.norm(vectors, axis=1, keepdims=True)
        if np.any(norms == 0):
            raise RuntimeError("llama.cpp 返回零向量")
        return np.ascontiguousarray(vectors / norms)


def read_jsonl(path: Path) -> list[dict[str, object]]:
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line]


def rank_and_measure(
    corpus_vectors: np.ndarray,
    query_vectors: np.ndarray,
    corpus: Sequence[dict[str, object]],
    queries: Sequence[dict[str, object]],
    qrels: Sequence[dict[str, object]],
) -> tuple[list[dict[str, object]], list[dict[str, object]], dict[str, object]]:
    relevant = {str(item["query_id"]): str(item["document_id"]) for item in qrels}
    query_meta = {str(item["id"]): item for item in queries}
    document_ids = [str(item["id"]) for item in corpus]
    rankings: list[dict[str, object]] = []
    per_query: list[dict[str, object]] = []
    for start in range(0, len(queries), 32):
        scores = query_vectors[start : start + 32] @ corpus_vectors.T
        top_indices = np.argpartition(scores, -10, axis=1)[:, -10:]
        top_scores = np.take_along_axis(scores, top_indices, axis=1)
        order = np.argsort(-top_scores, axis=1)
        top_indices = np.take_along_axis(top_indices, order, axis=1)
        top_scores = np.take_along_axis(top_scores, order, axis=1)
        for offset in range(len(top_indices)):
            query = queries[start + offset]
            query_id = str(query["id"])
            ids = [document_ids[int(index)] for index in top_indices[offset]]
            target = relevant[query_id]
            rank = ids.index(target) + 1 if target in ids else None
            rankings.append(
                {
                    "query_id": query_id,
                    "document_ids": ids,
                    "scores": [float(value) for value in top_scores[offset]],
                }
            )
            per_query.append(
                {
                    "query_id": query_id,
                    "category": query["category"],
                    "scope": query["scope"],
                    "relevant_document_id": target,
                    "rank": rank,
                    "recall_at_10": 1.0 if rank is not None else 0.0,
                    "ndcg_at_10": 1.0 / math.log2(rank + 1) if rank is not None else 0.0,
                }
            )

    def aggregate(items: Iterable[dict[str, object]]) -> dict[str, object]:
        selected = list(items)
        return {
            "count": len(selected),
            "recall_at_10": float(np.mean([item["recall_at_10"] for item in selected])),
            "ndcg_at_10": float(np.mean([item["ndcg_at_10"] for item in selected])),
            "top1_accuracy": float(np.mean([item["rank"] == 1 for item in selected])),
            "mrr_at_10": float(
                np.mean([1.0 / int(item["rank"]) if item["rank"] is not None else 0.0 for item in selected])
            ),
            "recall_at_5": float(np.mean([item["rank"] is not None and int(item["rank"]) <= 5 for item in selected])),
        }

    metrics = {
        "overall": aggregate(per_query),
        "by_category": {
            category: aggregate(item for item in per_query if item["category"] == category)
            for category in sorted({str(item["category"]) for item in per_query})
        },
        "by_scope": {
            scope: aggregate(item for item in per_query if item["scope"] == scope)
            for scope in sorted({str(item["scope"]) for item in per_query})
        },
    }
    return rankings, per_query, metrics


def run_quality(server: LlamaServer, dataset: Path, label: str) -> dict[str, object]:
    output = RESULTS / label
    output.mkdir(parents=True, exist_ok=True)
    corpus = read_jsonl(dataset / "corpus.jsonl")
    queries = read_jsonl(dataset / "queries.jsonl")
    qrels = read_jsonl(dataset / "qrels.jsonl")
    started = time.perf_counter()
    corpus_vectors = server.encode([str(item["text"]) for item in corpus], "document")
    corpus_seconds = time.perf_counter() - started
    started = time.perf_counter()
    query_vectors = server.encode([str(item["text"]) for item in queries], "query")
    query_seconds = time.perf_counter() - started
    rankings, per_query, metrics = rank_and_measure(corpus_vectors, query_vectors, corpus, queries, qrels)
    with (output / "rankings.jsonl").open("w", encoding="utf-8", newline="\n") as handle:
        for item in rankings:
            handle.write(json.dumps(item, ensure_ascii=False, sort_keys=True) + "\n")
    with (output / "per-query.jsonl").open("w", encoding="utf-8", newline="\n") as handle:
        for item in per_query:
            handle.write(json.dumps(item, ensure_ascii=False, sort_keys=True) + "\n")
    summary = {
        "model": "ggml-org/embeddinggemma-300m-qat-q8_0-GGUF",
        "runtime": "llama.cpp b10488",
        "model_sha256": sha256(MODEL),
        "dataset_manifest_sha256": sha256(dataset / "manifest.json"),
        "dimension": DIMENSION,
        "timing": {"corpus_seconds": corpus_seconds, "query_seconds": query_seconds},
        "metrics": metrics,
    }
    write_json(output / "summary.json", summary)
    return summary


def representative_queries() -> list[str]:
    templates = [
        "查找与离线个人信息检索相关的可靠记录，要求区分原始事实、后续结论和已经弃用的方案。",
        "Find the note that explains why the earlier approach was rejected and which replacement was finally adopted.",
        "我记得曾经总结过一个跨项目可复用的方法，请找出步骤、适用条件和失败边界。",
        "Locate the experience where similar wording referred to different contexts and return the actually relevant one.",
        "检索关于长期信息资产与可替换内核关系的说明，不要返回只有关键词相似但结论相反的内容。",
    ]
    return [f"{templates[index % len(templates)]} 样本 {index:03d}" for index in range(100)]


def screen_threads() -> dict[str, object]:
    results: list[dict[str, object]] = []
    queries = representative_queries()[:20]
    for threads in (2, 4):
        with LlamaServer(threads, f"screen-t{threads}") as server:
            server.encode([queries[0]], "query", batch_size=1)
            assert server.sampler is not None
            server.sampler.reset()
            latencies = []
            for text in queries:
                started = time.perf_counter()
                server.encode([text], "query", batch_size=1)
                latencies.append((time.perf_counter() - started) * 1000)
            results.append(
                {
                    "threads": threads,
                    "startup_ms": server.started_ms,
                    "p95_ms": percentile(latencies, 95),
                    "mean_ms": statistics.fmean(latencies),
                    "peak_rss_bytes": server.sampler.peak,
                }
            )
    selected = min(results, key=lambda item: (float(item["p95_ms"]), int(item["peak_rss_bytes"])))
    summary = {"results": results, "selected_threads": int(selected["threads"])}
    write_json(RESULTS / "thread-screening.json", summary)
    return summary


def measure_cycle(threads: int, cycle: int) -> dict[str, object]:
    cold = []
    cold_peak = []
    for index in range(3):
        with LlamaServer(threads, f"cycle-{cycle}-cold-{index + 1}") as server:
            cold.append(server.started_ms)
            assert server.sampler is not None
            cold_peak.append(server.sampler.peak)

    queries = representative_queries()
    with LlamaServer(threads, f"cycle-{cycle}-warm") as server:
        server.encode([queries[0]], "query", batch_size=1)
        assert server.sampler is not None and server.ps_process is not None
        server.sampler.reset()
        latencies = []
        for _ in range(2):
            for text in queries:
                started = time.perf_counter()
                server.encode([text], "query", batch_size=1)
                latencies.append((time.perf_counter() - started) * 1000)
        time.sleep(SETTLING_SECONDS)
        idle_started = time.perf_counter()
        cpu_started = cpu_seconds(server.ps_process)
        time.sleep(IDLE_SECONDS)
        idle_duration = time.perf_counter() - idle_started
        idle_cpu = max(0.0, cpu_seconds(server.ps_process) - cpu_started)
        idle_cpu_percent = idle_cpu / idle_duration / int(psutil.cpu_count(logical=True) or 1) * 100
        idle_rss = rss_bytes(server.ps_process)
        peak_rss = max(server.sampler.peak, idle_rss)
    return {
        "cycle": cycle,
        "threads": threads,
        "cold": {"samples_ms": cold, "p95_ms": percentile(cold, 95), "peak_rss_bytes": cold_peak},
        "warm": {
            "query_count": len(latencies),
            "p50_ms": percentile(latencies, 50),
            "p95_ms": percentile(latencies, 95),
            "p99_ms": percentile(latencies, 99),
            "mean_ms": statistics.fmean(latencies),
            "peak_rss_bytes_through_idle": peak_rss,
            "rss_bytes_at_idle_end": idle_rss,
            "idle_cpu_percent_total_logical_capacity": idle_cpu_percent,
        },
    }


def main() -> None:
    RESULTS.mkdir(parents=True, exist_ok=True)
    if not SERVER.is_file() or not MODEL.is_file():
        raise RuntimeError("llama.cpp 运行时或 Q8 GGUF 不存在")
    screening = screen_threads()
    threads = int(screening["selected_threads"])
    resources = [measure_cycle(threads, cycle) for cycle in (1, 2)]
    write_json(
        RESULTS / "resources.json",
        {
            "model_bytes": MODEL.stat().st_size,
            "runtime_bytes": sum(path.stat().st_size for path in SERVER.parent.rglob("*") if path.is_file()),
            "model_sha256": sha256(MODEL),
            "runtime_version": "llama.cpp b10488 (9d77fa172)",
            "screening": screening,
            "cycles": resources,
        },
    )
    with LlamaServer(threads, "quality") as server:
        formal = run_quality(server, ROOT / "data" / "ownward-v2", "quality-formal")
        supplement = run_quality(server, ROOT / "data" / "ownward-quality-supplement-v1", "quality-supplement")
    result = {
        "status": "complete",
        "selected_threads": threads,
        "resources": resources,
        "quality_formal": formal["metrics"],
        "quality_supplement": supplement["metrics"],
        "onnx_baseline": json.loads(ONNX_RESULTS.read_text(encoding="utf-8")),
    }
    write_json(RESULTS / "summary.json", result)
    write_json(
        STATE / "llama-q8-complete.json",
        {
            "status": "complete",
            "result": str(RESULTS / "summary.json"),
            "model_sha256": sha256(MODEL),
            "runtime_build": 10488,
        },
    )
    print(json.dumps(result, ensure_ascii=False, indent=2, sort_keys=True))


if __name__ == "__main__":
    main()
