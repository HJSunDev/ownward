from __future__ import annotations

import json
import statistics
import subprocess
import time
from pathlib import Path

import numpy as np
import psutil

from measure_resources_v2 import (
    CONFIG,
    DATASET,
    MemorySampler,
    NODE,
    ROOT,
    percentile,
    read_jsonl,
    read_line,
    rss_bytes,
    stop_worker,
)


BUNDLE = ROOT / "delivery-runtime-v3"
CURRENT_WORKER = BUNDLE / "vector-worker.mjs"
DIRECT_WORKER = ROOT / "scripts" / "vector-worker-ort.mjs"
MODEL_DIR = ROOT / "models" / "embeddinggemma-int8"
OUTPUT = ROOT / "results" / "embeddinggemma-runtime-optimization"
STATE = ROOT / "state" / "embeddinggemma-runtime-optimization"

VARIANTS = (
    ("direct-t4-arena-pattern", 4, True, True),
    ("direct-t2-arena-pattern", 2, True, True),
    ("direct-t4-noarena-nopattern", 4, False, False),
    ("direct-t2-noarena-nopattern", 2, False, False),
)


def representative_queries() -> list[str]:
    rows = sorted(read_jsonl(DATASET / "queries.jsonl"), key=lambda row: str(row["id"]))
    categories: dict[str, list[str]] = {}
    for row in rows:
        categories.setdefault(str(row["category"]), []).append(str(row["text"]))
    selected = []
    for category in sorted(categories):
        selected.extend(categories[category][:3])
    return selected[:20]


def start(worker: Path, session_config: Path | None = None):
    command = [str(NODE), str(worker), str(CONFIG), "embeddinggemma_300m", str(MODEL_DIR)]
    if session_config is not None:
        command.append(str(session_config))
    started = time.perf_counter()
    process = subprocess.Popen(
        command,
        cwd=BUNDLE,
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        encoding="utf-8",
        creationflags=subprocess.CREATE_NO_WINDOW,
    )
    ps_process = psutil.Process(process.pid)
    sampler = MemorySampler(ps_process)
    sampler.start()
    ready = read_line(process, 30)
    if ready.get("type") != "ready":
        raise RuntimeError(f"工作进程未就绪：{ready}")
    return process, ps_process, sampler, ready, (time.perf_counter() - started) * 1000


def request(process, request_id: str, text: str, *, return_vector: bool = False):
    if process.stdin is None:
        raise RuntimeError("工作进程没有标准输入")
    started = time.perf_counter_ns()
    process.stdin.write(
        json.dumps(
            {
                "id": request_id,
                "text": text,
                "prompt_type": "query",
                "return_vector": return_vector,
            },
            ensure_ascii=False,
        )
        + "\n"
    )
    process.stdin.flush()
    response = read_line(process, 30)
    return response, (time.perf_counter_ns() - started) / 1_000_000


def vector_consistency(session_config: Path, queries: list[str]) -> dict[str, float]:
    current, _, current_sampler, _, _ = start(CURRENT_WORKER)
    direct, _, direct_sampler, _, _ = start(DIRECT_WORKER, session_config)
    cosines = []
    max_abs = []
    try:
        for index, text in enumerate(queries[:8]):
            left, _ = request(current, f"current-{index}", text, return_vector=True)
            right, _ = request(direct, f"direct-{index}", text, return_vector=True)
            a = np.asarray(left["vector"], dtype=np.float64)
            b = np.asarray(right["vector"], dtype=np.float64)
            cosines.append(float(np.dot(a, b) / np.linalg.norm(a) / np.linalg.norm(b)))
            max_abs.append(float(np.max(np.abs(a - b))))
    finally:
        stop_worker(current, current_sampler)
        stop_worker(direct, direct_sampler)
    return {
        "minimum_cosine": min(cosines),
        "maximum_absolute_difference": max(max_abs),
    }


def screen(name: str, session_config: Path, queries: list[str]) -> dict[str, object]:
    process, ps_process, sampler, ready, cold_ms = start(DIRECT_WORKER, session_config)
    try:
        request(process, "warmup", queries[0])
        sampler.reset()
        latencies = []
        worker = []
        for index, text in enumerate(queries):
            response, wall_ms = request(process, str(index), text)
            latencies.append(wall_ms)
            worker.append(float(response["elapsed_ms"]))
        time.sleep(2)
        peak = max(sampler.peak, rss_bytes(ps_process))
    finally:
        stop_worker(process, sampler)
    return {
        "variant": name,
        "session": json.loads(session_config.read_text(encoding="utf-8")),
        "cold_ms": cold_ms,
        "query_count": len(latencies),
        "warm_wall_ms": {
            "p50": percentile(latencies, 50),
            "p95": percentile(latencies, 95),
            "mean": statistics.fmean(latencies),
        },
        "worker_p95_ms": percentile(worker, 95),
        "peak_rss_bytes": peak,
    }


def main() -> None:
    OUTPUT.mkdir(parents=True, exist_ok=True)
    STATE.mkdir(parents=True, exist_ok=True)
    affinity = [0, 2, 4, 6]
    psutil.Process().cpu_affinity(affinity)
    queries = representative_queries()
    results = []
    config_paths = {}
    for name, threads, arena, pattern in VARIANTS:
        config = {
            "name": name,
            "intra_op_threads": threads,
            "enable_cpu_mem_arena": arena,
            "enable_mem_pattern": pattern,
        }
        path = STATE / f"{name}.json"
        path.write_text(json.dumps(config, indent=2) + "\n", encoding="utf-8", newline="\n")
        config_paths[name] = path
    consistency = vector_consistency(config_paths[VARIANTS[0][0]], queries)
    if consistency["minimum_cosine"] < 0.999999:
        raise RuntimeError(f"直接 ONNX 输出与当前配置不一致：{consistency}")
    for name, _, _, _ in VARIANTS:
        result = screen(name, config_paths[name], queries)
        results.append(result)
        print(json.dumps(result, ensure_ascii=False), flush=True)
    summary = {"vector_consistency": consistency, "results": results}
    (OUTPUT / "screening.json").write_text(
        json.dumps(summary, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
        newline="\n",
    )
    print(json.dumps(summary, ensure_ascii=False, indent=2), flush=True)


if __name__ == "__main__":
    main()
