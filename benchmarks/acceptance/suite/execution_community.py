from __future__ import annotations

from pathlib import Path
from typing import Any


def execute_community(
    suite_root: Path,
    contract: dict[str, Any],
    binding: dict[str, Any],
    config: dict[str, Any],
    workspace: Path,
    *,
    resume: bool,
) -> tuple[dict[str, Any], list[Path]]:
    import community
    from evidence import validate_layer_report

    section = dict(config["community"])
    section["binary"] = config["candidate"]["binary"]
    section["embedding_bundle_dir"] = config["candidate"]["embedding_bundle_dir"]
    section["_workspace"] = str(workspace)
    report = community.execute(suite_root, contract, binding, section, resume=resume)
    validate_layer_report(contract, "community", report, expected_binding=binding)
    return report, community.artifact_paths(section)
