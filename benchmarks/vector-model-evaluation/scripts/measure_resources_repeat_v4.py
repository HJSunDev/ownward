from __future__ import annotations

import hashlib
import json
import statistics
import time
from datetime import datetime, timezone
from pathlib import Path

import psutil

from measure_resources_v2 import (
    CONFIG,
    MODELS,
    ROOT,
    cpu_seconds,
    percentile,
    representative_queries,
    request,
    rss_bytes,
    runtime_bytes,
    start_worker,
    stop_worker,
    tree_bytes,
)


OUTPUT = ROOT / "results" / "resources-v4-repeat"
FREEZE = ROOT / "state" / "resource-repeat-v4-freeze.json"
SETTLING_SECONDS = 10


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(8 * 1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def evaluate(
    model_key: str,
    directory: str,
    config: dict[str, object],
    shared_runtime: dict[str, int],
    queries: list[str],
) -> dict[str, object]:
    model_dir = ROOT / "models" / directory
    affinity = [int(value) for value in config["execution"]["logical_processor_affinity"]]
    gates = config["resource_gates"]
    model_bytes = tree_bytes(model_dir, exclude_cache=True)
    install_bytes = model_bytes + shared_runtime["delivery_runtime"]
    dimension = int(config["models"][model_key]["deliverable"]["dimension"])

    cold_wall: list[float] = []
    cold_reported: list[float] = []
    cold_peak: list[int] = []

    def cold_start() -> None:
        process, _, sampler, ready, wall_ms = start_worker(
            model_key, model_dir, affinity
        )
        try:
            cold_wall.append(wall_ms)
            cold_reported.append(float(ready["load_ms"]))
            cold_peak.append(sampler.peak)
        finally:
            stop_worker(process, sampler)

    for _ in range(int(config["execution"]["cold_process_starts"])):
        cold_start()
    cold_limit = float(gates["cold_start_ms_max"])
    if abs(percentile(cold_wall, 95) - cold_limit) / cold_limit < 0.10:
        cold_start()
        cold_start()

    process, ps_process, sampler, _, _ = start_worker(
        model_key, model_dir, affinity
    )
    try:
        request(process, "warmup", queries[0])
        sampler.reset()
        latencies: list[float] = []
        reported: list[float] = []
        for round_index in range(
            int(config["execution"]["warm_measured_rounds"])
        ):
            for query_index, text in enumerate(queries):
                response, wall_ms = request(
                    process, f"{round_index}-{query_index}", text
                )
                latencies.append(wall_ms)
                reported.append(float(response["elapsed_ms"]))

        time.sleep(SETTLING_SECONDS)
        idle_started = time.perf_counter()
        cpu_started = cpu_seconds(ps_process)
        time.sleep(int(config["execution"]["idle_seconds"]))
        idle_seconds = time.perf_counter() - idle_started
        cpu_delta = max(0.0, cpu_seconds(ps_process) - cpu_started)
        logical_processors = int(psutil.cpu_count(logical=True) or 1)
        idle_cpu = cpu_delta / idle_seconds / logical_processors * 100
        rss_at_idle_end = rss_bytes(ps_process)
        peak_rss = max(sampler.peak, rss_at_idle_end)
    finally:
        stop_worker(process, sampler)

    cold_p95 = percentile(cold_wall, 95)
    warm_p95 = percentile(latencies, 95)
    result: dict[str, object] = {
        "model_key": model_key,
        "model_directory": directory,
        "model_bytes": model_bytes,
        "delivery_runtime_bytes": shared_runtime["delivery_runtime"],
        "install_bytes": install_bytes,
        "dimension": dimension,
        "float32_vector_bytes": dimension * 4,
        "cold_starts": {
            "wall_ms": cold_wall,
            "reported_load_ms": cold_reported,
            "peak_rss_bytes": cold_peak,
            "p95_wall_ms": cold_p95,
        },
        "warm": {
            "query_count": len(latencies),
            "wall_ms": {
                "p50": percentile(latencies, 50),
                "p95": warm_p95,
                "p99": percentile(latencies, 99),
                "mean": statistics.fmean(latencies),
                "max": max(latencies),
            },
            "worker_ms": {
                "p50": percentile(reported, 50),
                "p95": percentile(reported, 95),
                "p99": percentile(reported, 99),
            },
            "settling_seconds": SETTLING_SECONDS,
            "idle_seconds": idle_seconds,
            "idle_cpu_percent_total_logical_capacity": idle_cpu,
            "peak_process_tree_rss_bytes_through_idle": peak_rss,
            "rss_bytes_at_idle_end": rss_at_idle_end,
        },
        "gates": {
            "install": {
                "pass": install_bytes
                <= int(gates["model_plus_minimum_runtime_bytes_max"]),
                "value": install_bytes,
                "limit": int(gates["model_plus_minimum_runtime_bytes_max"]),
            },
            "cold_start": {
                "pass": cold_p95 <= cold_limit,
                "statistic": "p95",
                "sample_count": len(cold_wall),
                "value_ms": cold_p95,
                "limit_ms": cold_limit,
            },
            "warm_query_p95": {
                "pass": warm_p95 <= float(gates["warm_query_p95_ms_max"]),
                "value_ms": warm_p95,
                "limit_ms": float(gates["warm_query_p95_ms_max"]),
            },
            "peak_rss": {
                "pass": peak_rss
                <= int(gates["warm_peak_process_tree_rss_bytes_max"]),
                "value": peak_rss,
                "limit": int(gates["warm_peak_process_tree_rss_bytes_max"]),
            },
            "idle_cpu": {
                "pass": idle_cpu <= float(gates["idle_cpu_percent_max"]),
                "value_percent": idle_cpu,
                "limit_percent": float(gates["idle_cpu_percent_max"]),
            },
        },
    }
    result["admitted"] = all(
        bool(value["pass"]) for value in result["gates"].values()
    )
    return result


def main() -> None:
    OUTPUT.mkdir(parents=True, exist_ok=False)
    config = json.loads(CONFIG.read_text(encoding="utf-8"))
    freeze = {
        "status": "frozen_before_repeat_results",
        "created_at": datetime.now(timezone.utc).isoformat(),
        "script_sha256": sha256(Path(__file__)),
        "config_sha256": sha256(CONFIG),
        "delivery_runtime_validation_sha256": sha256(
            ROOT / "state" / "delivery-runtime-v3-validation.json"
        ),
        "models": [key for key, _ in MODELS],
        "settling_seconds": SETTLING_SECONDS,
        "idle_seconds": int(config["execution"]["idle_seconds"]),
        "output": str(OUTPUT),
    }
    FREEZE.write_text(
        json.dumps(freeze, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
        newline="\n",
    )

    affinity = [int(value) for value in config["execution"]["logical_processor_affinity"]]
    psutil.Process().cpu_affinity(affinity)
    shared_runtime = runtime_bytes()
    queries = representative_queries()
    results = []
    for model_key, directory in MODELS:
        result = evaluate(model_key, directory, config, shared_runtime, queries)
        results.append(result)
        (OUTPUT / f"{model_key}.json").write_text(
            json.dumps(result, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
            encoding="utf-8",
            newline="\n",
        )
        print(json.dumps(result, ensure_ascii=False, sort_keys=True), flush=True)

    summary = {
        "freeze": freeze,
        "results": results,
    }
    (OUTPUT / "summary.json").write_text(
        json.dumps(summary, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
        newline="\n",
    )


if __name__ == "__main__":
    main()
