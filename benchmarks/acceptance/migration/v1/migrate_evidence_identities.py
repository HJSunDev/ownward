#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path


HERE = Path(__file__).resolve().parent
DEFAULT_REPOSITORY = HERE.parents[3]
SUITE_ROOT = DEFAULT_REPOSITORY / "benchmarks" / "acceptance" / "suite"
sys.path.insert(0, str(SUITE_ROOT))

import binding  # noqa: E402
import evidence_identity  # noqa: E402
import identity_migration  # noqa: E402
import lifecycle  # noqa: E402


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="把唯一 Acceptance state 无损映射到直接依赖身份图")
    parser.add_argument("--repository", type=Path, default=DEFAULT_REPOSITORY)
    parser.add_argument("--state", type=Path, default=DEFAULT_REPOSITORY / ".tmp" / "first-kernel-baseline-v1" / "acceptance" / "state.json")
    parser.add_argument("--binding", type=Path, default=DEFAULT_REPOSITORY / ".tmp" / "first-kernel-baseline-v1" / "acceptance-3e712f2" / "binding")
    parser.add_argument("--frozen", type=Path, default=HERE / "frozen-baseline.json")
    parser.add_argument("--write", action="store_true", help="原子替换唯一 state 并发布已封存的 v6 binding")
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    result = identity_migration.migrate(
        args.repository, args.state, args.binding, args.frozen, write=args.write,
    )
    print(json.dumps(result, ensure_ascii=False, sort_keys=True, separators=(",", ":")))


if __name__ == "__main__":
    try:
        main()
    except (
        OSError, ValueError, json.JSONDecodeError,
        binding.BindingError, evidence_identity.EvidenceIdentityError,
        identity_migration.IdentityMigrationError, lifecycle.LifecycleError,
    ) as error:
        print(f"evidence-identity-migration: {error}", file=sys.stderr)
        raise SystemExit(2)
