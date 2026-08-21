#!/usr/bin/env python3
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
from typing import Any


OFFICIAL_REVISION = "2cc8c540bdb87fe6761629b585e727e1c4704520"
ACTIVE_CODEX_MODEL = "gpt-5.4-mini"
ACTIVE_CODEX_REASONING_EFFORT = "xhigh"
ADAPTER_FILES = (
    "run.py",
    "ownward_memory.py",
    "ownward_trajectory.py",
    "memory_config.active.json",
    "memory_config.direct.json",
)


def _parse_wrapper_args() -> tuple[argparse.Namespace, list[str]]:
    parser = argparse.ArgumentParser(add_help=False)
    parser.add_argument("--official-repo", required=True)
    parser.add_argument("--ownward-binary", required=True)
    parser.add_argument("--codex-binary", required=True)
    parser.add_argument("--codex-auth-file", required=True)
    parser.add_argument("--runtime-dir", required=True)
    parser.add_argument("--candidate", required=True)
    parser.add_argument("--environment-sha256", required=True)
    parser.add_argument("--input-manifest-sha256", required=True)
    parser.add_argument("--tool-sha256", required=True)
    return parser.parse_known_args()


def _sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def _valid_sha256(value: str) -> bool:
    return len(value) == 64 and all(character in "0123456789abcdef" for character in value)


def _argument_value(arguments: list[str], name: str) -> str:
    try:
        return arguments[arguments.index(name) + 1]
    except (ValueError, IndexError) as error:
        raise RuntimeError(f"official harness argument {name} is required") from error


def _verify_candidate(binary: Path, candidate: str) -> str:
    completed = subprocess.run(
        [str(binary), "version"], check=False, capture_output=True, text=True, encoding="utf-8"
    )
    if completed.returncode != 0:
        raise RuntimeError(f"could not read Ownward binary version: {completed.stderr.strip()}")
    version = completed.stdout.strip()
    if not candidate.strip() or version != candidate.strip():
        raise RuntimeError(f"Ownward binary version {version!r} does not match candidate {candidate.strip()!r}")
    return _sha256(binary)


def _verify_codex(binary: Path) -> tuple[str, str]:
    completed = subprocess.run(
        [*_command_prefix(binary), "--version"], check=False, capture_output=True, text=True, encoding="utf-8"
    )
    version = completed.stdout.strip()
    if completed.returncode != 0 or not version:
        raise RuntimeError(f"could not execute Codex: {completed.stderr.strip()!r}")
    return version, _sha256(binary)


def _command_prefix(binary: Path) -> list[str]:
    if binary.suffix.lower() == ".ps1":
        shell = shutil.which("pwsh") or shutil.which("powershell")
        if shell is None:
            raise RuntimeError("PowerShell is required to run the configured Codex entry")
        return [shell, "-NoProfile", "-File", str(binary)]
    if binary.suffix.lower() in {".cmd", ".bat"}:
        raise RuntimeError("command wrappers are not accepted as the Codex evidence entry")
    return [str(binary)]


def _record_ownward_evidence(
    harness_args: list[str], *, candidate: str, binary_sha256: str, adapter_root: Path,
    environment_sha256: str, input_manifest_sha256: str, tool_sha256: str,
    codex_version: str = "", codex_sha256: str = "",
) -> None:
    output_dir = Path(_argument_value(harness_args, "--output-dir")).resolve()
    run_args_path = output_dir / "run_args.json"
    if not run_args_path.exists():
        raise RuntimeError(f"official harness did not produce {run_args_path}")
    config_path = Path(_argument_value(harness_args, "--memory-config-path")).resolve()
    config = json.loads(config_path.read_text(encoding="utf-8"))
    params = config.get("memory_params") if isinstance(config, dict) else None
    if not isinstance(params, dict) or params.get("query_mode") not in {"direct", "codex"}:
        raise RuntimeError("Ownward memory config does not declare a valid query_mode")
    if params["query_mode"] == "codex" and (
        params.get("codex_model") != ACTIVE_CODEX_MODEL
        or params.get("codex_reasoning_effort") != ACTIVE_CODEX_REASONING_EFFORT
        or not codex_version
        or len(codex_sha256) != 64
    ):
        raise RuntimeError("Ownward active retrieval must use the fixed official Codex version, model, and reasoning effort")
    payload = json.loads(run_args_path.read_text(encoding="utf-8"))
    payload["ownward_evidence"] = {
        "candidate": candidate.strip(),
        "release_binary_sha256": binary_sha256,
        "environment_sha256": environment_sha256,
        "input_manifest_sha256": input_manifest_sha256,
        "tool_sha256": tool_sha256,
        "query_mode": params["query_mode"],
        "codex_model": params.get("codex_model", ""),
        "codex_reasoning_effort": params.get("codex_reasoning_effort", ""),
        "codex_cli_version": codex_version,
        "codex_binary_sha256": codex_sha256,
        "official_revision": OFFICIAL_REVISION,
        "adapter_sha256": {
            name: _sha256(adapter_root / name)
            for name in ADAPTER_FILES
        },
    }
    run_args_path.write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


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
    completed = subprocess.run(
        ["git", "status", "--porcelain"],
        cwd=repo,
        check=False,
        capture_output=True,
        text=True,
        encoding="utf-8",
    )
    if completed.returncode != 0 or completed.stdout.strip():
        raise RuntimeError("LongMemEval-V2 official checkout is not clean at the pinned revision")


def main() -> None:
    wrapper, harness_args = _parse_wrapper_args()
    for name in ("environment_sha256", "input_manifest_sha256", "tool_sha256"):
        if not _valid_sha256(getattr(wrapper, name)):
            raise RuntimeError(f"{name} must be a lowercase SHA-256 digest")
    official_repo = Path(wrapper.official_repo).resolve()
    _verify_official_revision(official_repo)
    binary = Path(wrapper.ownward_binary).resolve()
    binary_sha256 = _verify_candidate(binary, wrapper.candidate)
    os.environ["OWNWARD_BENCHMARK_BINARY"] = str(binary)
    runtime_dir = Path(wrapper.runtime_dir).resolve()
    if not runtime_dir.is_dir():
        raise RuntimeError(f"accepted Ownward runtime directory does not exist: {runtime_dir}")
    os.environ["OWNWARD_BENCHMARK_RUNTIME_DIR"] = str(runtime_dir)
    codex_version = ""
    codex_sha256 = ""
    codex_binary = Path(wrapper.codex_binary).resolve()
    codex_version, codex_sha256 = _verify_codex(codex_binary)
    os.environ["OWNWARD_BENCHMARK_CODEX_BINARY"] = str(codex_binary)
    codex_auth_file = Path(wrapper.codex_auth_file).resolve()
    if not codex_auth_file.is_file():
        raise RuntimeError(f"Codex auth file does not exist: {codex_auth_file}")
    os.environ["OWNWARD_BENCHMARK_CODEX_AUTH_FILE"] = str(codex_auth_file)
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
    _record_ownward_evidence(
        harness_args,
        candidate=wrapper.candidate,
        binary_sha256=binary_sha256,
        environment_sha256=wrapper.environment_sha256,
        input_manifest_sha256=wrapper.input_manifest_sha256,
        tool_sha256=wrapper.tool_sha256,
        adapter_root=Path(__file__).resolve().parent,
        codex_version=codex_version,
        codex_sha256=codex_sha256,
    )


if __name__ == "__main__":
    main()
