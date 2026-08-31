#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
from pathlib import Path

import kernel_iteration_evaluator_reliability as reliability


def main() -> None:
    parser = argparse.ArgumentParser(description="Ownward Stage 6 官方评测器非候选资格入口")
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--execution-config", type=Path, required=True)
    parser.add_argument("--formal-state", type=Path, required=True)
    parser.add_argument("--resume", action="store_true")
    args = parser.parse_args()
    result = reliability.run(
        Path(__file__).resolve().parent,
        args.output,
        args.execution_config,
        args.formal_state,
        resume=args.resume,
    )
    print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    main()
