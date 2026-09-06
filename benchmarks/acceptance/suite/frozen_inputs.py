"""Read immutable audit inputs without pinning the working runner to old code.

This module only reads text; it never imports or executes historical code.
Live execution still validates its own artifacts and protocol.
"""
from functools import lru_cache
import hashlib
import json
from pathlib import Path
import subprocess


def verify_files(repository: Path, items: list[dict]) -> None:
    for item in items:
        source = read_text(repository, item["path"], item["sha256"])
        if "identity" in item and json.loads(source).get("identity") != item["identity"]:
            raise ValueError(f"Frozen input identity mismatch: {item['path']}")


def _matches(data: bytes, digest: str) -> bool:
    return digest in (hashlib.sha256(data).hexdigest(),
                      hashlib.sha256(data.replace(b"\r\n", b"\n")).hexdigest())


def read_text(repository: Path, relative: str, digest: str) -> str:
    repository = repository.resolve()
    path = (repository / relative).resolve()
    if not path.is_relative_to(repository) or not relative or len(digest) != 64:
        raise ValueError("Invalid frozen input reference")
    if path.is_file():
        data = path.read_bytes()
        if _matches(data, digest):
            return data.decode("utf-8")
    return _historical_text(str(repository), path.relative_to(repository).as_posix(), digest)


@lru_cache(maxsize=128)
def _historical_text(repository: str, relative: str, digest: str) -> str:
    revisions = subprocess.run(
        ["git", "log", "--format=%H", "--", relative], cwd=repository,
        capture_output=True, text=True, check=True, timeout=30,
    ).stdout.splitlines()
    for revision in revisions:
        result = subprocess.run(
            ["git", "show", f"{revision}:{relative}"], cwd=repository,
            capture_output=True, timeout=30,
        )
        if result.returncode == 0 and _matches(result.stdout, digest):
            return result.stdout.decode("utf-8")
    raise ValueError(f"Frozen input unavailable or digest mismatch: {relative} ({digest})")
