#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import kernel_iteration_evidence


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Ownward 通用非正式内核迭代证据入口")
    parser.add_argument("--output", type=Path, required=True)
    selection = parser.add_mutually_exclusive_group()
    selection.add_argument("--subject", choices=["v0", "current-product"])
    selection.add_argument("--subject-manifest", type=Path)
    selection.add_argument("--runtime-state", type=Path)
    parser.add_argument("--evidence-type", default="identity-calibration")
    parser.add_argument("--input-manifest", type=Path)
    parser.add_argument("--contract", type=Path)
    parser.add_argument("--resume", action="store_true")
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    if args.runtime_state is not None:
        if args.evidence_type != "identity-calibration" or args.input_manifest is not None:
            raise SystemExit("运行态校准不得同时指定候选证据类型或输入清单")
        result = kernel_iteration_evidence.calibrate_runtime(
            HERE,
            args.output,
            args.runtime_state,
            contract_path=args.contract,
            resume=args.resume,
        )
    else:
        if args.subject is None and args.subject_manifest is None:
            raise SystemExit("必须选择 --subject、--subject-manifest 或 --runtime-state")
        result = kernel_iteration_evidence.run(
            HERE,
            args.output,
            selector=args.subject,
            subject_manifest=args.subject_manifest,
            evidence_type=args.evidence_type,
            input_manifest=args.input_manifest,
            contract_path=args.contract,
            resume=args.resume,
        )
    print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    main()
