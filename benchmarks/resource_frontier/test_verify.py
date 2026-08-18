from __future__ import annotations

import copy
import hashlib
import json
from pathlib import Path
import unittest

import verify


def comparator() -> dict:
    packages = [{"name": name, "version": version} for name, version in verify.RUNTIME_PINS.items()]
    return {
        "schema": verify.COMPARATOR_SCHEMA,
        "passed": True,
        "comparator": verify.COMPARATOR,
        "version": verify.COMPARATOR_VERSION,
        "source": "https://github.com/vbcherepanov/total-agent-memory",
        "source_tag": verify.SOURCE_TAG,
        "source_revision": verify.SOURCE_REVISION,
        "profile": verify.PROFILE,
        "runtime_pins": verify.RUNTIME_PINS,
        "scale": 100_000,
        "dimensions": 384,
        "model_excluded_from_kernel_measurement": True,
        "python_utf8_mode": True,
        "fixture_construction_excluded_from_timed_operations": True,
        "counts": {"information": 100_000, "embeddings": 100_000, "relations": 99_999, "fts": 100_000},
        "semantic_target_id": 50_001,
        "semantic_top_id": 50_001,
        "python_executable_sha256": "a" * 64,
        "runtime_packages_sha256": hashlib.sha256(
            json.dumps(packages, sort_keys=True, separators=(",", ":")).encode("utf-8")
        ).hexdigest(),
        "runtime_packages": packages,
        "runtime_closure_roots": ["isolated-environment", "base-runtime"],
        "metadata_sha256": "d" * 64,
        "source_tree_sha256": "e" * 64,
        "workload_sha256": verify._sha256(Path(verify.__file__).with_name("tam_benchmark.py")),
        "source_sha256": {name: "c" * 64 for name in ("server.py", "config.py", "paths.py")},
        "runtime_footprint_mib": 1000,
        "storage_mib_at_scale": 250,
        "idle_rss_mib": 500,
        "rss_mib_at_scale": 900,
        "idle_cpu_percent": 0.1,
        "latency": {
            name: {"p95_ms": value}
            for name, value in {"durable_write": 20, "basic_searchable": 25, "semantic_kernel": 100, "fts": 5}.items()
        },
    }


class ResourceFrontierVerifierTests(unittest.TestCase):
    def test_accepts_complete_comparator_evidence(self) -> None:
        verify._validate_comparator(comparator())

    def test_rejects_incomplete_scale_fixture(self) -> None:
        value = copy.deepcopy(comparator())
        value["counts"]["embeddings"] -= 1
        with self.assertRaisesRegex(RuntimeError, "fixture counts"):
            verify._validate_comparator(value)

    def test_rejects_unbound_source(self) -> None:
        value = copy.deepcopy(comparator())
        value["source_sha256"].pop("server.py")
        with self.assertRaisesRegex(RuntimeError, "source is unbound"):
            verify._validate_comparator(value)


if __name__ == "__main__":
    unittest.main()
