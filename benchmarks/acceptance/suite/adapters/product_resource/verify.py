#!/usr/bin/env python3
from __future__ import annotations

import argparse
import ctypes
from ctypes import wintypes
from datetime import datetime, timezone
import hashlib
import json
import math
import os
from pathlib import Path
import re
import statistics
import subprocess
import sys
import threading
import time
from typing import Any


REPOSITORY = Path(__file__).resolve().parents[5]
if str(REPOSITORY) not in sys.path:
    sys.path.insert(0, str(REPOSITORY))
SUITE = Path(__file__).resolve().parents[2]
if str(SUITE) not in sys.path:
    sys.path.insert(0, str(SUITE))

from benchmarks.support.ownward_mcp import OwnwardRuntime  # noqa: E402
import resource_environment  # noqa: E402


REPORT_SCHEMA = "ownward.delivery-resource-report/v1"
THRESHOLDS_SCHEMA = "ownward.delivery-resource-thresholds/v1"
RELEASE_SCHEMA = "ownward.release-bundle/v2"
EMBEDDING_SCHEMA = "ownward.embedding-bundle/v3"


def require(condition: bool, message: str) -> None:
    if not condition:
        raise RuntimeError(message)


def load(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    require(isinstance(value, dict), f"JSON document must contain an object: {path}")
    return value


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def canonical_json_sha256(path: Path) -> str:
    value = json.loads(path.read_text(encoding="utf-8"))
    encoded = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    temporary.replace(path)


def percentile(values: list[float], quantile: float) -> float:
    require(values, "cannot calculate a percentile without samples")
    ordered = sorted(values)
    rank = max(0, math.ceil(quantile * len(ordered)) - 1)
    return ordered[rank]


def file_manifest(root: Path, *, exclude: set[str] | None = None) -> dict[str, dict[str, Any]]:
    excluded = exclude or set()
    result: dict[str, dict[str, Any]] = {}
    for path in sorted(root.rglob("*")):
        if path.is_symlink():
            raise RuntimeError(f"release package contains a symbolic link: {path}")
        if not path.is_file():
            continue
        relative = path.relative_to(root).as_posix()
        if relative in excluded:
            continue
        result[relative] = {"bytes": path.stat().st_size, "sha256": sha256(path)}
    return result


def validate_release(package: Path, candidate: str) -> tuple[Path, dict[str, Any], dict[str, dict[str, Any]]]:
    release = load(package / "manifest.json")
    require(release.get("schema") == RELEASE_SCHEMA and release.get("candidate") == candidate, "release manifest belongs to another candidate")
    files = release.get("files")
    require(isinstance(files, dict), "release manifest has no file inventory")
    actual = file_manifest(package, exclude={"manifest.json"})
    require(set(files) == set(actual), "release file inventory is incomplete")
    for relative, digest in files.items():
        require(isinstance(digest, str) and actual[relative]["sha256"] == digest, f"release file changed: {relative}")
    required = {"bin/ownward.exe", "bin/embedding/manifest.json", "LICENSE", "README.md"}
    require(required <= set(actual), "release package is missing a required product file")
    binary = package / "bin" / "ownward.exe"
    completed = subprocess.run([str(binary), "version"], capture_output=True, text=True, encoding="utf-8", timeout=30)
    require(completed.returncode == 0 and completed.stdout.strip() == candidate, "release binary version differs from candidate")
    embedding = load(package / "bin" / "embedding" / "manifest.json")
    require(embedding.get("schema") == EMBEDDING_SCHEMA, "release embedding manifest has an unexpected schema")
    require(release.get("embedding_space") == embedding.get("space", {}).get("id"), "release vector space binding changed")
    require(release.get("embedding_legal_materials") == embedding.get("legal", {}).get("legal_materials_id"), "release legal-material binding changed")
    require(embedding.get("model", {}).get("sha256") == "6fa0c02a9c302be6f977521d399b4de3a46310a4f2621ee0063747881b673f67", "release contains another embedding model")
    require(embedding.get("runtime", {}).get("source_archive_sha256") == "6c938f6d79aac96cb90fda673aade20cff9b1b6c1e97de04f4d5d60bca107082", "release contains another embedding runtime")
    embedding_root = package / "bin" / "embedding"
    bound_files = {
        str(embedding["model"]["path"]): str(embedding["model"]["sha256"]),
        **{str(path): str(digest) for path, digest in embedding["runtime"]["files"].items()},
        **{str(path): str(digest) for path, digest in embedding["legal"]["files"].items()},
    }
    for relative, expected in bound_files.items():
        path = (embedding_root / Path(relative)).resolve()
        require(path.is_relative_to(embedding_root.resolve()) and path.is_file(), f"embedding artifact is missing: {relative}")
        require(sha256(path) == expected, f"embedding artifact changed: {relative}")
    legal_binding = {
        "model_sha256": str(embedding["model"]["sha256"]),
        "files": dict(sorted(embedding["legal"]["files"].items())),
    }
    encoded = json.dumps(legal_binding, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    expected_legal_materials = "legal_" + hashlib.sha256(encoded).hexdigest()[:32]
    require(embedding["legal"]["legal_materials_id"] == expected_legal_materials, "embedding legal-material identity is invalid")
    return binary, embedding, actual


if os.name == "nt":
    TH32CS_SNAPPROCESS = 0x00000002
    PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
    PROCESS_VM_READ = 0x0010
    INVALID_HANDLE_VALUE = ctypes.c_void_p(-1).value

    class PROCESSENTRY32W(ctypes.Structure):
        _fields_ = [
            ("dwSize", wintypes.DWORD), ("cntUsage", wintypes.DWORD), ("th32ProcessID", wintypes.DWORD),
            ("th32DefaultHeapID", ctypes.POINTER(ctypes.c_ulong)), ("th32ModuleID", wintypes.DWORD),
            ("cntThreads", wintypes.DWORD), ("th32ParentProcessID", wintypes.DWORD),
            ("pcPriClassBase", wintypes.LONG), ("dwFlags", wintypes.DWORD), ("szExeFile", wintypes.WCHAR * 260),
        ]

    class PROCESS_MEMORY_COUNTERS_EX(ctypes.Structure):
        _fields_ = [
            ("cb", wintypes.DWORD), ("PageFaultCount", wintypes.DWORD),
            ("PeakWorkingSetSize", ctypes.c_size_t), ("WorkingSetSize", ctypes.c_size_t),
            ("QuotaPeakPagedPoolUsage", ctypes.c_size_t), ("QuotaPagedPoolUsage", ctypes.c_size_t),
            ("QuotaPeakNonPagedPoolUsage", ctypes.c_size_t), ("QuotaNonPagedPoolUsage", ctypes.c_size_t),
            ("PagefileUsage", ctypes.c_size_t), ("PeakPagefileUsage", ctypes.c_size_t),
            ("PrivateUsage", ctypes.c_size_t),
        ]

    kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    psapi = ctypes.WinDLL("psapi", use_last_error=True)
    kernel32.CreateToolhelp32Snapshot.argtypes = [wintypes.DWORD, wintypes.DWORD]
    kernel32.CreateToolhelp32Snapshot.restype = wintypes.HANDLE
    kernel32.Process32FirstW.argtypes = [wintypes.HANDLE, ctypes.POINTER(PROCESSENTRY32W)]
    kernel32.Process32FirstW.restype = wintypes.BOOL
    kernel32.Process32NextW.argtypes = [wintypes.HANDLE, ctypes.POINTER(PROCESSENTRY32W)]
    kernel32.Process32NextW.restype = wintypes.BOOL
    kernel32.OpenProcess.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
    kernel32.OpenProcess.restype = wintypes.HANDLE
    kernel32.GetProcessTimes.argtypes = [wintypes.HANDLE, ctypes.POINTER(wintypes.FILETIME), ctypes.POINTER(wintypes.FILETIME), ctypes.POINTER(wintypes.FILETIME), ctypes.POINTER(wintypes.FILETIME)]
    kernel32.GetProcessTimes.restype = wintypes.BOOL
    kernel32.CloseHandle.argtypes = [wintypes.HANDLE]
    kernel32.CloseHandle.restype = wintypes.BOOL
    psapi.GetProcessMemoryInfo.argtypes = [wintypes.HANDLE, ctypes.POINTER(PROCESS_MEMORY_COUNTERS_EX), wintypes.DWORD]
    psapi.GetProcessMemoryInfo.restype = wintypes.BOOL


def _filetime(value: wintypes.FILETIME) -> int:
    return (value.dwHighDateTime << 32) | value.dwLowDateTime


def _process_snapshot() -> dict[int, tuple[int, str]]:
    require(os.name == "nt", "complete delivery resources currently require the pinned Windows runtime")
    handle = kernel32.CreateToolhelp32Snapshot(TH32CS_SNAPPROCESS, 0)
    require(handle != INVALID_HANDLE_VALUE, "could not enumerate the Windows process tree")
    result: dict[int, tuple[int, str]] = {}
    try:
        entry = PROCESSENTRY32W()
        entry.dwSize = ctypes.sizeof(PROCESSENTRY32W)
        if kernel32.Process32FirstW(handle, ctypes.byref(entry)):
            while True:
                result[int(entry.th32ProcessID)] = (int(entry.th32ParentProcessID), str(entry.szExeFile))
                if not kernel32.Process32NextW(handle, ctypes.byref(entry)):
                    break
    finally:
        kernel32.CloseHandle(handle)
    return result


def _descendants(root_pid: int, snapshot: dict[int, tuple[int, str]]) -> set[int]:
    selected = {root_pid}
    changed = True
    while changed:
        changed = False
        for pid, (parent, _) in snapshot.items():
            if parent in selected and pid not in selected:
                selected.add(pid)
                changed = True
    return selected


def _process_metrics(pid: int) -> tuple[int, int] | None:
    handle = kernel32.OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION | PROCESS_VM_READ, False, pid)
    if not handle:
        return None
    try:
        memory = PROCESS_MEMORY_COUNTERS_EX()
        memory.cb = ctypes.sizeof(PROCESS_MEMORY_COUNTERS_EX)
        if not psapi.GetProcessMemoryInfo(handle, ctypes.byref(memory), memory.cb):
            return None
        creation, exit_time, kernel, user = wintypes.FILETIME(), wintypes.FILETIME(), wintypes.FILETIME(), wintypes.FILETIME()
        if not kernel32.GetProcessTimes(handle, ctypes.byref(creation), ctypes.byref(exit_time), ctypes.byref(kernel), ctypes.byref(user)):
            return None
        return int(memory.WorkingSetSize), _filetime(kernel) + _filetime(user)
    finally:
        kernel32.CloseHandle(handle)


def sample_tree(root_pid: int) -> dict[str, Any]:
    snapshot = _process_snapshot()
    pids = _descendants(root_pid, snapshot)
    processes = []
    total_rss = 0
    total_cpu = 0
    for pid in sorted(pids):
        metrics = _process_metrics(pid)
        if metrics is None:
            continue
        rss, cpu = metrics
        total_rss += rss
        total_cpu += cpu
        processes.append({"pid": pid, "parent_pid": snapshot.get(pid, (0, ""))[0], "name": snapshot.get(pid, (0, ""))[1], "rss_bytes": rss})
    require(any(item["pid"] == root_pid for item in processes), "Ownward process disappeared during resource sampling")
    return {"captured_at": time.time(), "rss_bytes": total_rss, "cpu_100ns": total_cpu, "processes": processes}


class TreeSampler:
    def __init__(self, root_pid: int) -> None:
        self.root_pid = root_pid
        self.samples: list[dict[str, Any]] = []
        self._stop = threading.Event()
        self._thread = threading.Thread(target=self._run, daemon=True)

    def __enter__(self) -> "TreeSampler":
        self._thread.start()
        return self

    def __exit__(self, *_: Any) -> None:
        self._stop.set()
        self._thread.join(timeout=2)

    def _run(self) -> None:
        while not self._stop.is_set():
            try:
                self.samples.append(sample_tree(self.root_pid))
            except RuntimeError:
                pass
            self._stop.wait(0.05)


def tree_cpu_percent(before: dict[str, Any], after: dict[str, Any]) -> float:
    elapsed = float(after["captured_at"]) - float(before["captured_at"])
    require(elapsed > 0, "invalid process sampling interval")
    used = max(0, int(after["cpu_100ns"]) - int(before["cpu_100ns"])) / 10_000_000
    return used / elapsed / max(1, os.cpu_count() or 1) * 100


def call(client: Any, name: str, arguments: dict[str, Any]) -> Any:
    started = time.perf_counter()
    result = client.call_tool(name, arguments)
    return result, (time.perf_counter() - started) * 1000


def run_cold_samples(binary: Path, workspace: Path, environment: dict[str, str], count: int) -> tuple[list[dict[str, float]], list[dict[str, Any]]]:
    durations: list[dict[str, float]] = []
    samples: list[dict[str, Any]] = []
    for index in range(count):
        data_dir = workspace / f"cold-{index + 1}"
        started = time.perf_counter()
        runtime = OwnwardRuntime(binary, data_dir, environment, startup_seconds=15, operation_seconds=15)
        runtime.__enter__()
        try:
            require(runtime.client is not None and runtime.process is not None, "Ownward runtime did not start")
            server_ms = (time.perf_counter() - started) * 1000
            with TreeSampler(runtime.process.pid) as sampler:
                _, query_ms = call(runtime.client, "ownward_search", {"query": "如何恢复我此前记录的旅行准备经验？", "limit": 10})
                total_ms = (time.perf_counter() - started) * 1000
                time.sleep(0.1)
            durations.append({"server_start_ms": server_ms, "first_query_ms": query_ms, "total_ms": total_ms})
            samples.extend(sampler.samples)
        finally:
            runtime.close()
    return durations, samples


def run_warm_workload(binary: Path, workspace: Path, environment: dict[str, str], query_count: int, document_count: int, idle_seconds: int) -> tuple[dict[str, Any], list[dict[str, Any]]]:
    runtime = OwnwardRuntime(binary, workspace / "workload", environment, startup_seconds=15, operation_seconds=30)
    runtime.__enter__()
    try:
        require(runtime.client is not None and runtime.process is not None, "Ownward runtime did not start")
        _, _ = call(runtime.client, "ownward_search", {"query": "预热本地向量能力", "limit": 10})
        query_latencies: list[float] = []
        documents = [
            {"content": f"长期信息样本 {index + 1}：用户在不同阶段总结的可复用方法与适用场景。", "source": {"actor": "delivery-resource", "ref": f"doc-{index + 1}"}}
            for index in range(document_count)
        ]
        with TreeSampler(runtime.process.pid) as working_sampler:
            created, document_ms = call(runtime.client, "ownward_create_batch", {"items": documents})
            require(isinstance(created, dict) and len(created.get("results", [])) == document_count, "document embedding workload did not persist every item")
            for index in range(query_count):
                _, latency = call(runtime.client, "ownward_search", {"query": f"第 {index + 1} 次查找可复用方法和适用场景", "limit": 10})
                query_latencies.append(latency)
        # Working allocations may finish reclamation immediately after the last
        # query. Stable idle starts only after this fixed settling interval.
        time.sleep(2)
        before = sample_tree(runtime.process.pid)
        time.sleep(idle_seconds)
        after = sample_tree(runtime.process.pid)
        all_samples = working_sampler.samples + [before, after]
        process_names = {str(item["name"]).lower() for sample in all_samples for item in sample["processes"]}
        result = {
            "query_samples_ms": query_latencies,
            "query_p95_ms": percentile(query_latencies, 0.95),
            "document_batch_ms": document_ms,
            "document_ms_per_item": document_ms / document_count,
            "working_peak_rss_bytes": max(sample["rss_bytes"] for sample in working_sampler.samples),
            "idle_rss_bytes": after["rss_bytes"],
            "idle_cpu_percent": tree_cpu_percent(before, after),
            "observed_process_names": sorted(process_names),
            "observed_model_runtime": any(name == "llama-server.exe" for name in process_names),
        }
        return result, all_samples
    finally:
        runtime.close()


def check(name: str, passed: bool, **values: Any) -> dict[str, Any]:
    return {"name": name, "passed": bool(passed), **values}


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--package", type=Path, required=True)
    parser.add_argument("--candidate", required=True)
    parser.add_argument("--production-storage-report", type=Path, required=True)
    parser.add_argument("--thresholds", type=Path, default=Path(__file__).with_name("thresholds.json"))
    parser.add_argument("--workspace", type=Path, required=True)
    parser.add_argument("--evidence-dir", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    require(os.name == "nt", "first-version complete delivery resource acceptance requires Windows")
    candidate = args.candidate.strip()
    require(re.fullmatch(r"[0-9a-f]{40}", candidate) is not None, "candidate must be a full lowercase Git commit hash")
    package = args.package.resolve()
    thresholds_path = args.thresholds.resolve()
    performance_path = args.production_storage_report.resolve()
    workspace = args.workspace.resolve()
    evidence_dir = args.evidence_dir.resolve()
    require(package.is_dir() and thresholds_path.is_file() and performance_path.is_file(), "required delivery resource input is missing")
    require(not workspace.exists() or not any(workspace.iterdir()), "delivery resource workspace must be empty")
    workspace.mkdir(parents=True, exist_ok=True)
    evidence_dir.mkdir(parents=True, exist_ok=True)

    thresholds = load(thresholds_path)
    require(thresholds.get("schema") == THRESHOLDS_SCHEMA, "delivery resource thresholds have an unexpected schema")
    for relative, digest_key in (("embedding_resource_report", "embedding_resource_report_sha256"), ("product_thresholds", "product_thresholds_sha256")):
        basis_path = REPOSITORY / str(thresholds["basis"][relative])
        require(
            basis_path.is_file() and canonical_json_sha256(basis_path) == thresholds["basis"][digest_key],
            f"delivery threshold basis changed: {relative}",
        )
    limits = thresholds["limits"]
    workload = thresholds["workload"]

    binary, embedding, package_files = validate_release(package, candidate)
    binary_digest = sha256(binary)
    installed_bytes = sum(int(item["bytes"]) for item in package_files.values()) + (package / "manifest.json").stat().st_size
    performance = load(performance_path)
    require(
        performance.get("schema") == "ownward.production-storage-report/v1"
        and performance.get("passed") is True
        and performance.get("candidate") == candidate
        and performance.get("release_binary_version") == candidate
        and performance.get("release_binary_sha256") == binary_digest,
        "production performance report belongs to another candidate or binary",
    )
    require(performance.get("scale") == workload["production_scale"] and performance.get("dimensions") == workload["production_dimensions"], "production storage workload does not use the frozen scale and vector dimensions")

    environment = dict(os.environ)
    cold_ms, cold_samples = run_cold_samples(binary, workspace, environment, int(workload["cold_start_samples"]))
    warm, warm_samples = run_warm_workload(
        binary, workspace, environment,
        int(workload["warm_query_samples"]), int(workload["document_samples"]), int(workload["idle_observation_seconds"]),
    )

    package_evidence = {
        "schema": "ownward.release-package-evidence/v2",
        "candidate": candidate,
        "package": str(package),
        "files": package_files,
        "installed_bytes": installed_bytes,
        "release_manifest_sha256": sha256(package / "manifest.json"),
        "embedding_space": embedding["space"]["id"],
        "embedding_legal_materials": embedding["legal"]["legal_materials_id"],
        "runtime_confirmation_files": sorted(str(path.relative_to(workspace)) for path in workspace.rglob("embedding-terms-acceptance.json")),
    }
    process_evidence = {
        "schema": "ownward.process-tree-evidence/v1",
        "candidate": candidate,
        "cold_samples": cold_samples,
        "warm_samples": warm_samples,
    }
    workload_evidence = {
        "schema": "ownward.delivery-workload-evidence/v1",
        "candidate": candidate,
        "cold_start_samples": cold_ms,
        "cold_start_p95_ms": percentile([item["total_ms"] for item in cold_ms], 0.95),
        "warm": warm,
        "production_storage_report": str(performance_path),
        "production_storage_report_sha256": sha256(performance_path),
        "production_scale": performance["scale"],
        "production_dimensions": performance["dimensions"],
        "production_storage_mib": performance["storage_mib_at_scale"],
        "derived_storage_ratio": performance["derived_storage_over_raw_plus_vectors_ratio"],
    }
    package_evidence_path = evidence_dir / "package-manifest.json"
    process_evidence_path = evidence_dir / "process-samples.json"
    workload_evidence_path = evidence_dir / "workload-results.json"
    write_json(package_evidence_path, package_evidence)
    write_json(process_evidence_path, process_evidence)
    write_json(workload_evidence_path, workload_evidence)

    require(not package_evidence["runtime_confirmation_files"], "blank product state created a model-terms confirmation file")

    mib = 1024 * 1024
    checks = [
        check("release-package-completeness", True, file_count=len(package_files), embedding_space=embedding["space"]["id"]),
        check("installed-footprint", installed_bytes / mib <= limits["installed_footprint_mib_max"], actual_mib=installed_bytes / mib, maximum_mib=limits["installed_footprint_mib_max"]),
        check("complete-process-tree", warm["observed_model_runtime"] is True, process_names=warm["observed_process_names"]),
        check("cold-start", workload_evidence["cold_start_p95_ms"] <= limits["cold_start_p95_ms_max"], actual_p95_ms=workload_evidence["cold_start_p95_ms"], maximum_p95_ms=limits["cold_start_p95_ms_max"]),
        check("idle-resources", warm["idle_rss_bytes"] / mib <= limits["idle_process_tree_rss_mib_max"] and warm["idle_cpu_percent"] <= limits["idle_cpu_percent_max"], rss_mib=warm["idle_rss_bytes"] / mib, rss_maximum_mib=limits["idle_process_tree_rss_mib_max"], cpu_percent=warm["idle_cpu_percent"], cpu_maximum_percent=limits["idle_cpu_percent_max"]),
        check("working-resources", warm["working_peak_rss_bytes"] / mib <= limits["working_process_tree_rss_mib_max"], actual_peak_rss_mib=warm["working_peak_rss_bytes"] / mib, maximum_peak_rss_mib=limits["working_process_tree_rss_mib_max"]),
        check("embedding-throughput", warm["query_p95_ms"] <= limits["warm_query_p95_ms_max"] and warm["document_ms_per_item"] <= limits["document_embedding_ms_per_item_max"], query_p95_ms=warm["query_p95_ms"], query_maximum_ms=limits["warm_query_p95_ms_max"], document_ms_per_item=warm["document_ms_per_item"], document_maximum_ms_per_item=limits["document_embedding_ms_per_item_max"]),
        check("production-scale-storage", performance["derived_storage_over_raw_plus_vectors_ratio"] <= limits["derived_storage_over_raw_plus_vectors_ratio_max"], scale=performance["scale"], dimensions=performance["dimensions"], storage_mib=performance["storage_mib_at_scale"], derived_ratio=performance["derived_storage_over_raw_plus_vectors_ratio"], maximum_derived_ratio=limits["derived_storage_over_raw_plus_vectors_ratio_max"]),
    ]
    report = {
        "schema": REPORT_SCHEMA,
        "candidate": candidate,
        "release_binary_version": candidate,
        "release_binary_sha256": binary_digest,
        "measured_at": datetime.now(timezone.utc).isoformat(),
        "environment": resource_environment.machine_identity(),
        "thresholds_sha256": sha256(thresholds_path),
        "production_storage_report_sha256": sha256(performance_path),
        "evidence": {
            "package_manifest": {"path": str(package_evidence_path), "sha256": sha256(package_evidence_path)},
            "process_samples": {"path": str(process_evidence_path), "sha256": sha256(process_evidence_path)},
            "workload_results": {"path": str(workload_evidence_path), "sha256": sha256(workload_evidence_path)},
        },
        "checks": checks,
        "passed": all(item["passed"] for item in checks),
    }
    write_json(args.output.resolve(), report)
    print(json.dumps(report, ensure_ascii=False, indent=2))
    if not report["passed"]:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
