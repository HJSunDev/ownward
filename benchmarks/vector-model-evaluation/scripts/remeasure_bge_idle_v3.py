from __future__ import annotations

import hashlib
import json
import time
from pathlib import Path

import psutil

from measure_resources_v2 import (
    CONFIG,
    MODELS,
    ROOT,
    cpu_seconds,
    representative_queries,
    request,
    rss_bytes,
    start_worker,
    stop_worker,
)


FREEZE = ROOT / "state" / "bge-idle-remeasurement-v3-freeze.json"
OUTPUT = ROOT / "results" / "resources-v3" / "bge_m3-idle-remeasurement.json"


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(8 * 1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def main() -> None:
    freeze = json.loads(FREEZE.read_text(encoding="utf-8"))
    if sha256(Path(__file__)) != freeze["script_sha256"]:
        raise RuntimeError("BGE 空闲 CPU 重测脚本与冻结记录不一致")
    base_path = ROOT / "results" / "resources-v3" / "summary.json"
    if sha256(base_path) != freeze["base_resource_summary_sha256"]:
        raise RuntimeError("基础资源结果与冻结记录不一致")
    base = json.loads(base_path.read_text(encoding="utf-8"))
    bge = next(value for value in base["results"] if value["model_key"] == "bge_m3")
    if not all(
        bool(bge["gates"][name]["pass"])
        for name in ("install", "cold_start", "warm_query_p95", "peak_rss")
    ):
        raise RuntimeError("BGE 除空闲 CPU 外仍有失败项，不应执行定向重测")

    config = json.loads(CONFIG.read_text(encoding="utf-8"))
    affinity = [int(value) for value in config["execution"]["logical_processor_affinity"]]
    psutil.Process().cpu_affinity(affinity)
    queries = representative_queries()
    directory = next(directory for key, directory in MODELS if key == "bge_m3")
    process, ps_process, sampler, _, _ = start_worker(
        "bge_m3", ROOT / "models" / directory, affinity
    )
    try:
        request(process, "warmup", queries[0])
        for round_index in range(int(config["execution"]["warm_measured_rounds"])):
            for query_index, text in enumerate(queries):
                request(process, f"{round_index}-{query_index}", text)
        time.sleep(int(freeze["settling_seconds"]))
        started = time.perf_counter()
        cpu_started = cpu_seconds(ps_process)
        time.sleep(int(freeze["measured_seconds"]))
        measured_seconds = time.perf_counter() - started
        cpu_delta = max(0.0, cpu_seconds(ps_process) - cpu_started)
        logical_processors = int(psutil.cpu_count(logical=True) or 1)
        idle_percent = cpu_delta / measured_seconds / logical_processors * 100
        resident_bytes = rss_bytes(ps_process)
    finally:
        stop_worker(process, sampler)

    limit = float(config["resource_gates"]["idle_cpu_percent_max"])
    result = {
        "model_key": "bge_m3",
        "base_resource_summary_sha256": freeze["base_resource_summary_sha256"],
        "settling_seconds": freeze["settling_seconds"],
        "measured_seconds": measured_seconds,
        "logical_processors": logical_processors,
        "cpu_seconds": cpu_delta,
        "idle_cpu_percent_total_logical_capacity": idle_percent,
        "resident_bytes_after_measurement": resident_bytes,
        "gate": {"pass": idle_percent <= limit, "value_percent": idle_percent, "limit_percent": limit},
    }
    OUTPUT.write_text(
        json.dumps(result, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
        newline="\n",
    )
    print(json.dumps(result, ensure_ascii=False, indent=2, sort_keys=True))


if __name__ == "__main__":
    main()
