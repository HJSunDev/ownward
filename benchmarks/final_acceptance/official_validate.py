#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
from pathlib import Path
import sys


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--official-repo", type=Path, required=True)
    parser.add_argument("--overview", type=Path, required=True)
    args = parser.parse_args()

    official_repo = args.official_repo.resolve()
    sys.path.insert(0, str(official_repo / "leaderboard"))
    sys.path.insert(0, str(official_repo))
    from build_submission_step_2_build_package import (  # type: ignore[import-not-found]
        build_submission_overview,
        validate_operating_points,
    )

    overview_path = args.overview.resolve()
    overview = json.loads(overview_path.read_text(encoding="utf-8"))
    if not isinstance(overview, dict):
        raise RuntimeError("submission overview must contain an object")
    submission_name = str(overview.get("submission_name", "")).strip()
    points = overview.get("operating_points")
    if not submission_name or not isinstance(points, list) or not points:
        raise RuntimeError("submission overview is incomplete")
    names = [str(point.get("name", "")).strip() for point in points if isinstance(point, dict)]
    if len(names) != len(points) or any(not name or Path(name).name != name for name in names):
        raise RuntimeError("submission operating points are invalid")
    point_dirs = [overview_path.parent / "operating_points" / name for name in names]
    validated = validate_operating_points(submission_name, point_dirs)
    rebuilt = build_submission_overview(
        submission_name,
        str(overview.get("system_description_file", "")),
        str(overview.get("code_file", "")),
        str(overview.get("archive_name", "")),
        validated,
    )
    rebuilt["generated_at_utc"] = overview.get("generated_at_utc")
    if rebuilt != overview:
        raise RuntimeError("submission overview differs from the official recomputation")
    print(json.dumps({"method": rebuilt["method"], "tier": rebuilt["tier"], "operating_points": len(validated)}))


if __name__ == "__main__":
    main()
