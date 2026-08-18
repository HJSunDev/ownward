#!/usr/bin/env python3
from __future__ import annotations

import argparse
import os
from pathlib import Path
import subprocess
import sys
from typing import Any


OFFICIAL_REVISION = "2cc8c540bdb87fe6761629b585e727e1c4704520"


def _parse_wrapper_args() -> tuple[argparse.Namespace, list[str]]:
    parser = argparse.ArgumentParser(add_help=False)
    parser.add_argument("--official-repo", required=True)
    parser.add_argument("--ownward-binary", required=True)
    parser.add_argument("--codex-binary")
    return parser.parse_known_args()


def _verify_official_revision(repo: Path) -> None:
    completed = subprocess.run(
        ["git", "rev-parse", "HEAD"],
        cwd=repo,
        check=False,
        capture_output=True,
        text=True,
        encoding="utf-8",
    )
    if completed.returncode != 0 or completed.stdout.strip() != OFFICIAL_REVISION:
        actual = completed.stdout.strip() or completed.stderr.strip() or "unknown"
        raise RuntimeError(f"LongMemEval-V2 must be checked out at {OFFICIAL_REVISION}; found {actual}")


def main() -> None:
    wrapper, harness_args = _parse_wrapper_args()
    official_repo = Path(wrapper.official_repo).resolve()
    _verify_official_revision(official_repo)
    os.environ["OWNWARD_BENCHMARK_BINARY"] = str(Path(wrapper.ownward_binary).resolve())
    if wrapper.codex_binary:
        os.environ["OWNWARD_BENCHMARK_CODEX_BINARY"] = str(Path(wrapper.codex_binary).resolve())
    sys.path.insert(0, str(official_repo))

    from evaluation import harness
    import ownward_memory  # noqa: F401

    original_inject = harness.inject_runtime_memory_params

    def inject_runtime_memory_params(
        memory_config: dict[str, Any],
        *,
        workspace_dir: Path,
        trajectories_path: str,
        reader_temperature: float | None = None,
        reader_top_p: float | None = None,
        query_trace_dir: Path | None = None,
    ) -> dict[str, Any]:
        runtime = original_inject(
            memory_config,
            workspace_dir=workspace_dir,
            trajectories_path=trajectories_path,
            reader_temperature=reader_temperature,
            reader_top_p=reader_top_p,
            query_trace_dir=query_trace_dir,
        )
        if runtime["memory_type"] == "ownward":
            runtime["memory_params"]["workspace_dir"] = str(workspace_dir.resolve())
            runtime["memory_params"]["trajectories_root_dir"] = str(Path(trajectories_path).resolve().parent)
            if query_trace_dir is not None:
                runtime["memory_params"]["query_trace_dir"] = str(query_trace_dir.resolve())
        return runtime

    harness.inject_runtime_memory_params = inject_runtime_memory_params
    harness.NONSHARED_PARALLEL_MEMORY_TYPES.add("ownward")
    sys.argv = [sys.argv[0], *harness_args]
    harness.main()


if __name__ == "__main__":
    main()
