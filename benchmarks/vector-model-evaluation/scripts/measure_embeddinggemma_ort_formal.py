from __future__ import annotations

import json
import statistics
import time
from pathlib import Path

import psutil

from measure_resources_v2 import (
    MemorySampler,
    ROOT,
    cpu_seconds,
    percentile,
    representative_queries,
    rss_bytes,
    stop_worker,
)
from screen_embeddinggemma_ort import (
    CURRENT_WORKER,
    DIRECT_WORKER,
    STATE,
    request,
    start,
)


OUTPUT = ROOT / "results" / "embeddinggemma-runtime-optimization" / "onnx-formal.json"
SESSION_CONFIG = STATE / "direct-t2-arena-pattern.json"
SETTLING_SECONDS = 10
IDLE_SECONDS = 60


def measure(label: str, worker: Path, session_config: Path | None) -> dict[str, object]:
    queries = representative_queries()
    cold = []
    cold_peak = []
    for _ in range(3):
        process, _, sampler, _, wall_ms = start(worker, session_config)
        try:
            cold.append(wall_ms)
            cold_peak.append(sampler.peak)
        finally:
            stop_worker(process, sampler)

    process, ps_process, sampler, _, _ = start(worker, session_config)
    try:
        request(process, "warmup", queries[0])
        sampler.reset()
        wall = []
        reported = []
        for round_index in range(2):
            for query_index, text in enumerate(queries):
                response, wall_ms = request(process, f"{round_index}-{query_index}", text)
                wall.append(wall_ms)
                reported.append(float(response["elapsed_ms"]))
        time.sleep(SETTLING_SECONDS)
        idle_started = time.perf_counter()
        cpu_started = cpu_seconds(ps_process)
        time.sleep(IDLE_SECONDS)
        idle_duration = time.perf_counter() - idle_started
        idle_cpu = max(0.0, cpu_seconds(ps_process) - cpu_started)
        idle_cpu_percent = idle_cpu / idle_duration / int(psutil.cpu_count(logical=True) or 1) * 100
        idle_rss = rss_bytes(ps_process)
        peak_rss = max(sampler.peak, idle_rss)
    finally:
        stop_worker(process, sampler)
    return {
        "label": label,
        "cold": {
            "samples_ms": cold,
            "p95_ms": percentile(cold, 95),
            "peak_rss_bytes": cold_peak,
        },
        "warm": {
            "query_count": len(wall),
            "wall_ms": {
                "p50": percentile(wall, 50),
                "p95": percentile(wall, 95),
                "p99": percentile(wall, 99),
                "mean": statistics.fmean(wall),
            },
            "worker_p95_ms": percentile(reported, 95),
            "peak_rss_bytes_through_idle": peak_rss,
            "rss_bytes_at_idle_end": idle_rss,
            "idle_cpu_percent_total_logical_capacity": idle_cpu_percent,
        },
    }


def main() -> None:
    psutil.Process().cpu_affinity([0, 2, 4, 6])
    runs = []
    order = (
        ("cycle-1-current", CURRENT_WORKER, None),
        ("cycle-1-direct-t2", DIRECT_WORKER, SESSION_CONFIG),
        ("cycle-2-direct-t2", DIRECT_WORKER, SESSION_CONFIG),
        ("cycle-2-current", CURRENT_WORKER, None),
    )
    for label, worker, session_config in order:
        result = measure(label, worker, session_config)
        runs.append(result)
        print(json.dumps(result, ensure_ascii=False), flush=True)

    current = [run for run in runs if run["label"].endswith("current")]
    candidate = [run for run in runs if run["label"].endswith("direct-t2")]
    current_latency = [float(run["warm"]["wall_ms"]["p95"]) for run in current]
    candidate_latency = [float(run["warm"]["wall_ms"]["p95"]) for run in candidate]
    current_memory = [int(run["warm"]["peak_rss_bytes_through_idle"]) for run in current]
    candidate_memory = [int(run["warm"]["peak_rss_bytes_through_idle"]) for run in candidate]
    comparison = {
        "latency_improves_beyond_observed_ranges": max(candidate_latency) < min(current_latency),
        "memory_improves_beyond_observed_ranges": max(candidate_memory) < min(current_memory),
        "current_p95_ms": current_latency,
        "candidate_p95_ms": candidate_latency,
        "current_peak_rss_bytes": current_memory,
        "candidate_peak_rss_bytes": candidate_memory,
    }
    comparison["route_succeeds"] = bool(
        comparison["latency_improves_beyond_observed_ranges"]
        and comparison["memory_improves_beyond_observed_ranges"]
    )
    summary = {"session_config": json.loads(SESSION_CONFIG.read_text()), "runs": runs, "comparison": comparison}
    OUTPUT.write_text(
        json.dumps(summary, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
        newline="\n",
    )
    print(json.dumps(summary, ensure_ascii=False, indent=2), flush=True)


if __name__ == "__main__":
    main()
