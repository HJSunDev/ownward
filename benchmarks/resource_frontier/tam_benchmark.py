#!/usr/bin/env python3
from __future__ import annotations

import argparse
from datetime import datetime, timezone
import gc
import hashlib
import importlib.metadata
import json
import math
import os
from pathlib import Path
import platform
import subprocess
import sys
import tempfile
import time
import tomllib
from typing import Any, Iterable


REPORT_SCHEMA = "ownward.resource-comparator-report/v1"
COMPARATOR_NAME = "total-agent-memory"
COMPARATOR_VERSION = "12.4.0"
SOURCE_TAG = "v12.4.0"
SOURCE_REVISION = "224323c9f522b5329515c14694c967ac6099b5a8"
RUNTIME_PINS = {"mcp": "1.29.0"}
SCALE = 100_000
DIMENSIONS = 384
ITERATIONS = 100
PROFILE = {
    "MEMORY_MODE": "fast",
    "MEMORY_ALLOW_OLLAMA_IN_HOT_PATH": "false",
    "MEMORY_ENRICHMENT_ENABLED": "false",
    "MEMORY_ASYNC_ENRICHMENT": "false",
    "MEMORY_RERANK_ENABLED": "false",
    "MEMORY_EMBED_PROVIDER": "openai",
    "OPENAI_API_KEY": "",
    "COHERE_API_KEY": "",
    "USE_OLLAMA_EMBED": "false",
    "USE_BINARY_SEARCH": "true",
}


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise RuntimeError(message)


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _tree_sha256(root: Path, paths: list[Path]) -> str:
    digest = hashlib.sha256()
    files = (
        item
        for base in paths
        for item in base.rglob("*")
        if item.is_file() and "__pycache__" not in item.parts and item.suffix not in {".pyc", ".pyo"}
    )
    for path in sorted(files, key=lambda item: item.relative_to(root).as_posix()):
        relative = path.relative_to(root).as_posix().encode("utf-8")
        digest.update(len(relative).to_bytes(4, "big"))
        digest.update(relative)
        with path.open("rb") as stream:
            for chunk in iter(lambda: stream.read(1024 * 1024), b""):
                digest.update(chunk)
    return digest.hexdigest()


def _source_size(paths: list[Path]) -> int:
    return sum(
        item.stat().st_size
        for base in paths
        for item in base.rglob("*")
        if item.is_file() and "__pycache__" not in item.parts and item.suffix not in {".pyc", ".pyo"}
    )


def _git(root: Path, *arguments: str) -> str:
    completed = subprocess.run(
        ["git", "-C", str(root), *arguments], check=False, capture_output=True, text=True, encoding="utf-8", timeout=30
    )
    _require(completed.returncode == 0, f"could not verify comparator source: {completed.stderr.strip()}")
    return completed.stdout.strip()


def _directory_size(root: Path) -> int:
    return sum(path.stat().st_size for path in root.rglob("*") if path.is_file())


def _python_runtime_size() -> tuple[int, list[str]]:
    prefix = Path(sys.prefix).resolve()
    base = Path(sys.base_prefix).resolve()
    if prefix == base:
        return _directory_size(prefix), [str(prefix)]

    size = _directory_size(prefix)
    roots = [str(prefix)]
    for path in base.iterdir():
        if path.name.lower() in {"include", "libs", "scripts", "share", "site-packages"}:
            continue
        if path.name.lower() == "lib":
            roots.append(str(path))
            for child in path.iterdir():
                if child.name.lower() == "site-packages":
                    continue
                size += _directory_size(child) if child.is_dir() else child.stat().st_size
            continue
        roots.append(str(path))
        size += _directory_size(path) if path.is_dir() else path.stat().st_size
    return size, roots


def _percentile(values: Iterable[float], quantile: float) -> float:
    ordered = sorted(values)
    _require(bool(ordered), "cannot calculate a percentile without samples")
    position = max(0, math.ceil(len(ordered) * quantile) - 1)
    return ordered[position]


def _distribution(values: list[float]) -> dict[str, Any]:
    return {
        "samples": len(values),
        "p50_ms": _percentile(values, 0.50),
        "p95_ms": _percentile(values, 0.95),
        "p99_ms": _percentile(values, 0.99),
        "max_ms": max(values),
    }


def _packages() -> list[dict[str, str]]:
    values = [
        {"name": distribution.metadata["Name"].lower(), "version": distribution.version}
        for distribution in importlib.metadata.distributions()
        if distribution.metadata.get("Name")
    ]
    return sorted(values, key=lambda value: (value["name"], value["version"]))


def _process_metrics() -> tuple[int, float]:
    import psutil

    process = psutil.Process()
    rss = process.memory_info().rss
    cpu = process.cpu_times()
    started = time.perf_counter()
    time.sleep(5)
    elapsed = time.perf_counter() - started
    after = process.cpu_times()
    cpu_percent = ((after.user + after.system) - (cpu.user + cpu.system)) / elapsed / max(1, os.cpu_count() or 1) * 100
    return rss, cpu_percent


def _fixed_vectors(batch_start: int, count: int, dimensions: int):
    import numpy as np

    indices = np.arange(batch_start, batch_start + count, dtype=np.float32)[:, None]
    axes = np.arange(dimensions, dtype=np.float32)[None, :]
    vectors = np.sin(indices * np.float32(0.017) + axes * np.float32(0.031))
    vectors += np.cos(indices * np.float32(0.013) - axes * np.float32(0.019))
    norms = np.linalg.norm(vectors, axis=1, keepdims=True)
    return (vectors / norms).astype(np.float32)


def _initialize_store(server: Any, root: Path) -> Any:
    server.MEMORY_DIR = root
    server.HAS_FASTEMBED = False
    server.HAS_ST = False
    store = server.Store()
    _require(store._embed_mode == "none", "comparator unexpectedly loaded an embedding model")
    return store


def _close_store(store: Any) -> None:
    cache = getattr(store, "v9_cache", None)
    if cache is not None:
        cache.close()
    store.db.close()


def _measure_live_writes(server: Any, root: Path, iterations: int) -> tuple[dict[str, Any], dict[str, Any]]:
    store = _initialize_store(server, root)
    try:
        write_values: list[float] = []
        searchable_values: list[float] = []
        for index in range(iterations):
            marker = f"ownwardlive{index:06d}"
            started = time.perf_counter()
            record_id, *_ = store.save_knowledge(
                "ownward-resource-frontier",
                f"Persistent comparator information {marker}",
                "fact",
                project="ownward-resource-frontier",
                skip_dedup=True,
                skip_quality=True,
                coref=False,
            )
            saved = time.perf_counter()
            rows = store.db.execute(
                "SELECT rowid FROM knowledge_fts WHERE knowledge_fts MATCH ? LIMIT 1", (marker,)
            ).fetchall()
            finished = time.perf_counter()
            _require(record_id is not None and rows and int(rows[0][0]) == int(record_id), "comparator write was not searchable")
            write_values.append((saved - started) * 1000)
            searchable_values.append((finished - started) * 1000)
        store.db.execute("PRAGMA wal_checkpoint(TRUNCATE)")
    finally:
        _close_store(store)
    return _distribution(write_values), _distribution(searchable_values)


def _seed_scale(store: Any, scale: int, dimensions: int) -> float:
    import numpy as np

    started = time.perf_counter()
    now = "2026-08-18T00:00:00Z"
    batch_size = 500
    for batch_start in range(0, scale, batch_size):
        count = min(batch_size, scale - batch_start)
        vectors = _fixed_vectors(batch_start, count, dimensions)
        knowledge = []
        embeddings = []
        relations = []
        for offset in range(count):
            index = batch_start + offset
            record_id = index + 1
            marker = f"marker{index:06d}"
            knowledge.append(
                (
                    record_id,
                    "ownward-resource-frontier",
                    "fact",
                    f"Long-lived comparator information {index}; topic bucket{index % 100}; {marker}.",
                    "",
                    "ownward-resource-frontier",
                    "[]",
                    "active",
                    1.0,
                    "benchmark-fixture",
                    now,
                    now,
                )
            )
            vector = vectors[offset]
            embeddings.append(
                (
                    record_id,
                    np.packbits(vector > 0).tobytes(),
                    vector.tobytes(),
                    "ownward-fixed-384",
                    dimensions,
                    now,
                    "fixture",
                    "text",
                    "text",
                    "en",
                )
            )
            if record_id > 1:
                relations.append((record_id, record_id - 1, "related_to", now))
        store.db.executemany(
            """
            INSERT INTO knowledge (
                id, session_id, type, content, context, project, tags, status,
                confidence, source, created_at, last_confirmed
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            knowledge,
        )
        store.db.executemany(
            """
            INSERT INTO embeddings (
                knowledge_id, binary_vector, float32_vector, embed_model, embed_dim,
                created_at, embedding_provider, embedding_space, content_type, language
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            embeddings,
        )
        store.db.executemany(
            "INSERT INTO relations (from_id, to_id, type, created_at) VALUES (?, ?, ?, ?)", relations
        )
        store.db.commit()
    store.db.execute("PRAGMA wal_checkpoint(TRUNCATE)")
    return time.perf_counter() - started


def _measure_scaled_search(store: Any, scale: int, iterations: int) -> tuple[dict[str, Any], dict[str, Any], int]:
    import numpy as np

    target_id = scale // 2 + 1
    row = store.db.execute("SELECT float32_vector FROM embeddings WHERE knowledge_id=?", (target_id,)).fetchone()
    _require(row is not None, "comparator target vector is missing")
    query = np.frombuffer(row[0], dtype=np.float32).tolist()
    semantic_values: list[float] = []
    semantic_top = 0
    for _ in range(iterations):
        started = time.perf_counter()
        results = store._binary_search(query, n_candidates=50, n_results=10, embedding_spaces=["text"])
        semantic_values.append((time.perf_counter() - started) * 1000)
        _require(results, "comparator semantic search returned no results")
        semantic_top = int(results[0][0])
        _require(semantic_top == target_id, "comparator semantic search did not recover the fixed target")

    marker = f"marker{target_id - 1:06d}"
    fts_values: list[float] = []
    for _ in range(iterations):
        started = time.perf_counter()
        rows = store.db.execute(
            "SELECT rowid FROM knowledge_fts WHERE knowledge_fts MATCH ? LIMIT 10", (marker,)
        ).fetchall()
        fts_values.append((time.perf_counter() - started) * 1000)
        _require(rows and int(rows[0][0]) == target_id, "comparator FTS did not recover the fixed target")
    return _distribution(semantic_values), _distribution(fts_values), semantic_top


def _parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--source-root", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--scale", type=int, default=SCALE)
    parser.add_argument("--dimensions", type=int, default=DIMENSIONS)
    parser.add_argument("--iterations", type=int, default=ITERATIONS)
    return parser.parse_args()


def main() -> None:
    args = _parse_args()
    _require(args.scale == SCALE and args.dimensions == DIMENSIONS, "formal comparator requires the fixed 100k/384d workload")
    _require(args.iterations >= 20, "formal comparator requires at least 20 timed samples")
    _require(sys.flags.utf8_mode == 1, "formal comparator requires Python UTF-8 mode")
    for name, value in PROFILE.items():
        os.environ[name] = value
    sys.dont_write_bytecode = True

    for name, version in RUNTIME_PINS.items():
        _require(importlib.metadata.version(name) == version, f"unexpected {name} compatibility version")
    source_root = args.source_root.resolve()
    pyproject = source_root / "pyproject.toml"
    runtime_paths = [source_root / name for name in ("src", "migrations", "total_agent_memory", "claude_total_memory")]
    required_sources = [source_root / "src" / name for name in ("server.py", "config.py", "paths.py")]
    _require(pyproject.is_file() and all(path.is_dir() for path in runtime_paths) and all(path.is_file() for path in required_sources), "comparator source checkout is incomplete")
    metadata = tomllib.loads(pyproject.read_text(encoding="utf-8"))
    _require(metadata.get("project", {}).get("version") == COMPARATOR_VERSION, "unexpected comparator source version")
    _require(_git(source_root, "rev-parse", "HEAD") == SOURCE_REVISION, "unexpected comparator source revision")
    _require(_git(source_root, "describe", "--tags", "--exact-match") == SOURCE_TAG, "comparator source is not the release tag")
    _require(not _git(source_root, "status", "--porcelain", "--untracked-files=no"), "comparator source checkout has tracked changes")
    sys.path.insert(0, str(source_root / "src"))

    with tempfile.TemporaryDirectory(prefix="ownward-resource-frontier-") as root_value:
        root = Path(root_value)
        os.environ["TAM_MEMORY_DIR"] = str(root / "loaded")
        import server  # type: ignore[import-not-found]  # noqa: PLC0415

        write, searchable = _measure_live_writes(server, root / "writes", args.iterations)
        store = _initialize_store(server, root / "loaded")
        try:
            gc.collect()
            idle_rss, _ = _process_metrics()
            seed_seconds = _seed_scale(store, args.scale, args.dimensions)
            counts = {
                "information": int(store.db.execute("SELECT COUNT(*) FROM knowledge WHERE status='active'").fetchone()[0]),
                "embeddings": int(store.db.execute("SELECT COUNT(*) FROM embeddings WHERE embed_dim=?", (args.dimensions,)).fetchone()[0]),
                "relations": int(store.db.execute("SELECT COUNT(*) FROM relations").fetchone()[0]),
                "fts": int(store.db.execute("SELECT COUNT(*) FROM knowledge_fts").fetchone()[0]),
            }
            _require(counts == {"information": args.scale, "embeddings": args.scale, "relations": args.scale - 1, "fts": args.scale}, "comparator fixture is incomplete")
            semantic, fts, semantic_top = _measure_scaled_search(store, args.scale, args.iterations)
            gc.collect()
            loaded_rss, idle_cpu = _process_metrics()
            storage_bytes = _directory_size(root / "loaded")
        finally:
            _close_store(store)

        packages = _packages()
        runtime_bytes, runtime_roots = _python_runtime_size()
        runtime_bytes += _source_size(runtime_paths)
        runtime_roots.extend(str(path) for path in runtime_paths)
        package_json = json.dumps(packages, sort_keys=True, separators=(",", ":")).encode("utf-8")
        report = {
            "schema": REPORT_SCHEMA,
            "comparator": COMPARATOR_NAME,
            "version": COMPARATOR_VERSION,
            "source": "https://github.com/vbcherepanov/total-agent-memory",
            "source_tag": SOURCE_TAG,
            "source_revision": SOURCE_REVISION,
            "measured_at": datetime.now(timezone.utc).isoformat(),
            "os": platform.platform(),
            "arch": platform.machine(),
            "cpus": os.cpu_count(),
            "python_version": platform.python_version(),
            "python_utf8_mode": True,
            "python_executable_sha256": _sha256(Path(sys.executable)),
            "runtime_packages_sha256": hashlib.sha256(package_json).hexdigest(),
            "runtime_packages": packages,
            "runtime_pins": RUNTIME_PINS,
            "runtime_footprint_mib": runtime_bytes / (1024 * 1024),
            "runtime_closure_roots": runtime_roots,
            "source_sha256": {path.name: _sha256(path) for path in required_sources},
            "source_tree_sha256": _tree_sha256(source_root, runtime_paths),
            "metadata_sha256": _sha256(pyproject),
            "workload_sha256": _sha256(Path(__file__).resolve()),
            "profile": PROFILE,
            "model_excluded_from_kernel_measurement": True,
            "fixture_construction_excluded_from_timed_operations": True,
            "scale": args.scale,
            "dimensions": args.dimensions,
            "counts": counts,
            "seed_seconds": seed_seconds,
            "storage_mib_at_scale": storage_bytes / (1024 * 1024),
            "idle_rss_mib": idle_rss / (1024 * 1024),
            "rss_mib_at_scale": loaded_rss / (1024 * 1024),
            "idle_cpu_percent": idle_cpu,
            "latency": {
                "durable_write": write,
                "basic_searchable": searchable,
                "semantic_kernel": semantic,
                "fts": fts,
            },
            "semantic_target_id": args.scale // 2 + 1,
            "semantic_top_id": semantic_top,
            "passed": True,
        }

    output = args.output.resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    encoded = json.dumps(report, ensure_ascii=False, indent=2) + "\n"
    temporary = output.with_suffix(output.suffix + ".tmp")
    temporary.write_text(encoded, encoding="utf-8")
    temporary.replace(output)
    print(encoded, end="")


if __name__ == "__main__":
    main()
