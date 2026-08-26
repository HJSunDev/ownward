from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import sys
import tempfile
from datetime import datetime, timezone
from typing import Any
import urllib.request


SCHEMA = "ownward.longmemeval-s-environment/v1"
OFFICIAL_REPOSITORY = "https://github.com/xiaowu0162/LongMemEval.git"
OFFICIAL_REVISION = "9e0b455f4ef0e2ab8f2e582289761153549043fc"
DATA_REPOSITORY = "https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned"
DATA_REVISION = "98d7416c24c778c2fee6e6f3006e7a073259d48f"
DATA_FILE = "longmemeval_s_cleaned.json"
DATA_URL = f"{DATA_REPOSITORY}/resolve/{DATA_REVISION}/{DATA_FILE}?download=true"
EXPECTED_QUESTIONS = 500
ASSET_VERSION = "v1"
PROJECT_CONSTRAINTS = Path(__file__).resolve().with_name("constraints.txt")
REQUIRED_QUESTION_FIELDS = {
    "question_id",
    "question_type",
    "question",
    "answer",
    "haystack_dates",
    "haystack_session_ids",
    "haystack_sessions",
}


class EnvironmentError(RuntimeError):
    pass


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise EnvironmentError(message)


def _run(arguments: list[str], cwd: Path | None = None) -> subprocess.CompletedProcess[str]:
    completed = subprocess.run(
        arguments,
        cwd=cwd,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        check=False,
    )
    if completed.returncode != 0:
        detail = (completed.stderr or completed.stdout).strip()[-3000:]
        raise EnvironmentError(f"command failed ({completed.returncode}): {' '.join(arguments)}\n{detail}")
    return completed


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _write_json(path: Path, value: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + ".partial")
    temporary.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    temporary.replace(path)


def _layout(root: Path) -> dict[str, Path]:
    return {
        "root": root,
        "source": root / "assets" / ASSET_VERSION / "source",
        "data": root / "assets" / ASSET_VERSION / "data" / DATA_FILE,
        "python": root / "runtime" / ASSET_VERSION / "python",
        "lock": root / "runtime" / ASSET_VERSION / "requirements.lock",
        "constraints": root / "runtime" / ASSET_VERSION / "constraints.txt",
        "manifest": root / "manifests" / f"{ASSET_VERSION}.json",
        "runs": root / "runs",
        "staging": root / ".install",
    }


def _validate_root(root: Path) -> Path:
    resolved = root.resolve()
    _require(resolved.is_absolute(), "environment root must be absolute")
    _require(resolved.drive.upper() == "E:", "LongMemEval-S persistent environment must be on E drive")
    lowered = str(resolved).lower()
    _require("\\temp\\" not in lowered and "\\tmp\\" not in lowered, "persistent environment cannot use a temporary directory")
    _require("appdata" not in lowered and "codex" not in lowered, "persistent environment cannot use an application or Codex directory")
    return resolved


def _validate_layout(paths: dict[str, Path]) -> None:
    root = paths["root"]
    for name in ("source", "data", "python", "lock", "constraints", "manifest", "runs"):
        _require(paths[name].is_relative_to(root), f"{name} escapes environment root")
    fixed = [paths["source"], paths["data"], paths["python"], paths["lock"], paths["constraints"], paths["manifest"]]
    _require(all(not paths["runs"].is_relative_to(item) and not item.is_relative_to(paths["runs"]) for item in fixed), "run output overlaps immutable environment")


def _validate_dataset(path: Path) -> int:
    value = json.loads(path.read_text(encoding="utf-8"))
    _require(isinstance(value, list), "LongMemEval-S dataset must be a JSON array")
    _require(len(value) == EXPECTED_QUESTIONS, f"LongMemEval-S must contain {EXPECTED_QUESTIONS} questions")
    identifiers: set[str] = set()
    for index, item in enumerate(value):
        _require(isinstance(item, dict), f"dataset entry {index} must be an object")
        missing = REQUIRED_QUESTION_FIELDS - set(item)
        _require(not missing, f"dataset entry {index} is missing fields: {sorted(missing)}")
        identifier = item["question_id"]
        _require(isinstance(identifier, str) and identifier, f"dataset entry {index} has no question_id")
        _require(identifier not in identifiers, f"duplicate question_id: {identifier}")
        identifiers.add(identifier)
    return len(value)


def _python_executable(venv: Path) -> Path:
    return venv / ("Scripts/python.exe" if os.name == "nt" else "bin/python")


def _download(url: str, destination: Path) -> None:
    destination.parent.mkdir(parents=True, exist_ok=True)
    request = urllib.request.Request(url, headers={"User-Agent": "Ownward-LongMemEval-S-Installer/1"})
    with urllib.request.urlopen(request, timeout=120) as response, destination.open("wb") as stream:
        downloaded = 0
        while True:
            chunk = response.read(1024 * 1024)
            if not chunk:
                break
            stream.write(chunk)
            downloaded += len(chunk)
            if downloaded % (32 * 1024 * 1024) < len(chunk):
                print(f"downloaded_mib={downloaded // (1024 * 1024)}", flush=True)


def _make_read_only(path: Path) -> None:
    for item in path.rglob("*"):
        if item.is_file() and ".git" not in item.parts:
            item.chmod(stat.S_IREAD)


def _manifest(paths: dict[str, Path], python_version: str, lock_sha256: str) -> dict[str, Any]:
    source = paths["source"]
    data = paths["data"]
    return {
        "schema": SCHEMA,
        "installed_utc": datetime.now(timezone.utc).isoformat(),
        "root": str(paths["root"]),
        "official": {
            "code_repository": OFFICIAL_REPOSITORY,
            "code_revision": OFFICIAL_REVISION,
            "code_tree": _run(["git", "rev-parse", "HEAD^{tree}"], source).stdout.strip(),
            "data_repository": DATA_REPOSITORY,
            "data_revision": DATA_REVISION,
            "data_file": DATA_FILE,
            "data_url": DATA_URL,
        },
        "layout": {
            "source": str(source),
            "data": str(data),
            "python": str(paths["python"]),
            "requirements_lock": str(paths["lock"]),
            "constraints": str(paths["constraints"]),
            "runs": str(paths["runs"]),
        },
        "integrity": {
            "data_sha256": _sha256(data),
            "data_bytes": data.stat().st_size,
            "question_count": EXPECTED_QUESTIONS,
            "requirements_lite_sha256": _sha256(source / "requirements-lite.txt"),
            "requirements_lock_sha256": lock_sha256,
            "constraints_sha256": _sha256(paths["constraints"]),
            "evaluate_qa_sha256": _sha256(source / "src" / "evaluation" / "evaluate_qa.py"),
            "print_qa_metrics_sha256": _sha256(source / "src" / "evaluation" / "print_qa_metrics.py"),
        },
        "runtime": {"python_version": python_version, "dependency_profile": "requirements-lite.txt"},
    }


def install(root: Path, bootstrap_python: Path) -> dict[str, Any]:
    root = _validate_root(root)
    paths = _layout(root)
    _validate_layout(paths)
    if paths["manifest"].is_file():
        return check(root, smoke=True)
    occupied = [paths[name] for name in ("source", "data", "python", "lock", "constraints") if paths[name].exists()]
    _require(not occupied, f"partial environment exists without manifest: {occupied}")
    _require(not paths["staging"].exists(), f"installation staging already exists: {paths['staging']}")
    _require(bootstrap_python.is_file(), f"bootstrap Python does not exist: {bootstrap_python}")

    stage = paths["staging"]
    stage_source = stage / "source"
    stage_data = stage / "data" / DATA_FILE
    stage_python = stage / "python"
    stage_lock = stage / "requirements.lock"
    stage_constraints = stage / "constraints.txt"
    stage.mkdir(parents=True)
    try:
        print("install_stage=official-source", flush=True)
        _run(["git", "clone", "--no-checkout", OFFICIAL_REPOSITORY, str(stage_source)])
        _run(["git", "checkout", "--detach", OFFICIAL_REVISION], stage_source)
        _require(_run(["git", "rev-parse", "HEAD"], stage_source).stdout.strip() == OFFICIAL_REVISION, "official source revision mismatch")
        _require(not _run(["git", "status", "--porcelain"], stage_source).stdout.strip(), "official source checkout is dirty")

        print("install_stage=official-data", flush=True)
        _download(DATA_URL, stage_data)
        _validate_dataset(stage_data)

        print("install_stage=python-runtime", flush=True)
        _require(PROJECT_CONSTRAINTS.is_file(), f"project constraints do not exist: {PROJECT_CONSTRAINTS}")
        shutil.copy2(PROJECT_CONSTRAINTS, stage_constraints)
        _run([str(bootstrap_python), "-m", "venv", "--copies", str(stage_python)])
        python = _python_executable(stage_python)
        _run([str(python), "-m", "pip", "install", "--disable-pip-version-check", "--no-input", "--no-cache-dir", "-c", str(stage_constraints), "-r", str(stage_source / "requirements-lite.txt")])
        freeze = _run([str(python), "-m", "pip", "freeze", "--all"]).stdout
        stage_lock.write_text(freeze, encoding="utf-8")

        paths["source"].parent.mkdir(parents=True, exist_ok=True)
        paths["data"].parent.mkdir(parents=True, exist_ok=True)
        paths["python"].parent.mkdir(parents=True, exist_ok=True)
        stage_source.replace(paths["source"])
        stage_data.replace(paths["data"])
        stage_python.replace(paths["python"])
        stage_lock.replace(paths["lock"])
        stage_constraints.replace(paths["constraints"])
        paths["runs"].mkdir(parents=True, exist_ok=True)
        python_version = _run([str(_python_executable(paths["python"])), "--version"]).stdout.strip()
        value = _manifest(paths, python_version, _sha256(paths["lock"]))
        _write_json(paths["manifest"], value)
        _make_read_only(paths["source"])
        paths["data"].chmod(stat.S_IREAD)
        paths["lock"].chmod(stat.S_IREAD)
        paths["constraints"].chmod(stat.S_IREAD)
        paths["manifest"].chmod(stat.S_IREAD)
        print("install_stage=offline-verification", flush=True)
    finally:
        if stage.exists():
            shutil.rmtree(stage)
    return check(root, smoke=True)


def _load_manifest(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    _require(isinstance(value, dict) and value.get("schema") == SCHEMA, "environment manifest schema mismatch")
    return value


def _smoke(paths: dict[str, Path]) -> None:
    python = _python_executable(paths["python"])
    client = _run([
        str(python),
        "-c",
        "from openai import OpenAI; OpenAI(api_key='fixture', base_url='http://127.0.0.1:1/v1'); print('client-ok')",
    ]).stdout
    _require("client-ok" in client, "official evaluator client cannot be constructed")
    evaluator = paths["source"] / "src" / "evaluation" / "evaluate_qa.py"
    usage = _run([str(python), str(evaluator)]).stdout
    _require("Usage:" in usage, "official evaluator entry did not start")

    smoke_root = Path(tempfile.mkdtemp(prefix=".smoke-", dir=paths["runs"]))
    try:
        reference = smoke_root / "reference.json"
        result = smoke_root / "result.jsonl"
        types = [
            "single-session-user",
            "single-session-preference",
            "single-session-assistant",
            "multi-session",
            "temporal-reasoning",
            "knowledge-update",
        ]
        references = [
            {"question_id": f"fixture-{index}{'_abs' if index == 0 else ''}", "question_type": kind}
            for index, kind in enumerate(types)
        ]
        # The pinned official metrics script asserts its historical wire-format model
        # sentinel before counting labels. This offline fixture exercises that unchanged
        # script; it does not select or invoke a model. Production execution records the
        # actual Codex judge identity separately and never uses this compatibility value.
        results = [
            {"question_id": item["question_id"], "autoeval_label": {"model": "gpt-4o-2024-08-06", "label": True}}
            for item in references
        ]
        reference.write_text(json.dumps(references), encoding="utf-8")
        result.write_text("".join(json.dumps(item) + "\n" for item in results), encoding="utf-8")
        metrics = paths["source"] / "src" / "evaluation" / "print_qa_metrics.py"
        output = _run([str(python), str(metrics), str(result), str(reference)]).stdout
        _require("Overall Accuracy: 1.0" in output, "official metric fixture did not complete")
    finally:
        shutil.rmtree(smoke_root)


def check(root: Path, smoke: bool = False) -> dict[str, Any]:
    root = _validate_root(root)
    paths = _layout(root)
    _validate_layout(paths)
    _require(paths["manifest"].is_file(), f"environment manifest does not exist: {paths['manifest']}")
    value = _load_manifest(paths["manifest"])
    _require(Path(value["root"]).resolve() == root, "environment manifest root mismatch")
    _require(value["official"] == {
        "code_repository": OFFICIAL_REPOSITORY,
        "code_revision": OFFICIAL_REVISION,
        "code_tree": value["official"].get("code_tree"),
        "data_repository": DATA_REPOSITORY,
        "data_revision": DATA_REVISION,
        "data_file": DATA_FILE,
        "data_url": DATA_URL,
    }, "official environment identity mismatch")
    _require(not paths["staging"].exists(), "installation staging residue exists")
    _require(paths["runs"].is_dir(), "writable run root does not exist")
    _require(_run(["git", "rev-parse", "HEAD"], paths["source"]).stdout.strip() == OFFICIAL_REVISION, "official source revision changed")
    _require(_run(["git", "rev-parse", "HEAD^{tree}"], paths["source"]).stdout.strip() == value["official"]["code_tree"], "official source tree changed")
    _require(not _run(["git", "status", "--porcelain"], paths["source"]).stdout.strip(), "official source checkout changed")
    _require(_run(["git", "config", "--get", "remote.origin.url"], paths["source"]).stdout.strip() == OFFICIAL_REPOSITORY, "official source origin changed")
    _require(_validate_dataset(paths["data"]) == value["integrity"]["question_count"], "question count changed")
    checks = {
        "data_sha256": paths["data"],
        "requirements_lite_sha256": paths["source"] / "requirements-lite.txt",
        "requirements_lock_sha256": paths["lock"],
        "constraints_sha256": paths["constraints"],
        "evaluate_qa_sha256": paths["source"] / "src" / "evaluation" / "evaluate_qa.py",
        "print_qa_metrics_sha256": paths["source"] / "src" / "evaluation" / "print_qa_metrics.py",
    }
    for name, path in checks.items():
        _require(path.is_file() and _sha256(path) == value["integrity"][name], f"integrity mismatch: {name}")
    _require(paths["data"].stat().st_size == value["integrity"]["data_bytes"], "dataset size changed")
    python = _python_executable(paths["python"])
    _require(python.is_file(), "pinned Python environment is missing")
    version = _run([str(python), "--version"]).stdout.strip()
    _require(version == value["runtime"]["python_version"], "Python runtime changed")
    freeze = _run([str(python), "-m", "pip", "freeze", "--all"]).stdout.replace("\r\n", "\n")
    expected_freeze = paths["lock"].read_text(encoding="utf-8").replace("\r\n", "\n")
    _require(freeze == expected_freeze, "installed Python dependencies changed")
    if smoke:
        _smoke(paths)
    return {
        "schema": SCHEMA,
        "status": "reused" if value else "invalid",
        "root": str(root),
        "manifest": str(paths["manifest"]),
        "code_revision": OFFICIAL_REVISION,
        "data_revision": DATA_REVISION,
        "data_sha256": value["integrity"]["data_sha256"],
        "question_count": value["integrity"]["question_count"],
        "python_version": version,
        "smoke": "passed" if smoke else "not-run",
    }


def main() -> int:
    parser = argparse.ArgumentParser(description="Install or verify the persistent LongMemEval-S environment")
    subparsers = parser.add_subparsers(dest="command", required=True)
    install_parser = subparsers.add_parser("install", help="explicitly install once; later calls only verify and reuse")
    install_parser.add_argument("--root", type=Path, required=True)
    install_parser.add_argument("--bootstrap-python", type=Path, required=True)
    check_parser = subparsers.add_parser("check", help="offline integrity check")
    check_parser.add_argument("--root", type=Path, required=True)
    check_parser.add_argument("--smoke", action="store_true")
    arguments = parser.parse_args()
    try:
        result = install(arguments.root, arguments.bootstrap_python) if arguments.command == "install" else check(arguments.root, arguments.smoke)
    except (EnvironmentError, OSError, ValueError, json.JSONDecodeError) as error:
        print(f"LongMemEval-S environment error: {error}", file=sys.stderr)
        return 1
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
