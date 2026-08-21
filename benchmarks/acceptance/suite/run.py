#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import frontier
import binding
import execution
import lifecycle
import preflight
from contract import load_contract
from evidence import attach_artifacts, validate_suite_inputs


for stream in (sys.stdout, sys.stderr):
    reconfigure = getattr(stream, "reconfigure", None)
    if callable(reconfigure):
        reconfigure(encoding="utf-8")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Ownward Acceptance Suite v1 唯一执行入口")
    parser.add_argument("mode", choices=["check", "plan", "self-check", "preflight", "bind", "init", "rebind", "execute", "invalidate", "promote", "summarize"])
    parser.add_argument("--state", type=Path)
    parser.add_argument("--binding", type=Path)
    parser.add_argument("--impact", action="append", default=[])
    parser.add_argument("--checkpoint-mode")
    parser.add_argument("--output", type=Path)
    parser.add_argument("--repository", type=Path, default=Path("."))
    parser.add_argument("--binary", type=Path)
    parser.add_argument("--embedding-bundle-dir", type=Path)
    parser.add_argument("--codex-binary", type=Path)
    parser.add_argument("--codex-auth-file", type=Path)
    parser.add_argument("--isolation-dir", type=Path)
    parser.add_argument("--config", type=Path)
    parser.add_argument("--resume", action="store_true")
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    contract = load_contract(HERE / "contract.json")
    if args.mode == "check":
        validate_suite_inputs(HERE)
        print(json.dumps({"passed": True, "suite_version": contract["suite_version"]}, ensure_ascii=False))
        return
    if args.mode == "plan":
        print(json.dumps({
            "modes": lifecycle.plan_for_impacts(args.impact),
            "targeted_stages": lifecycle.stages_for_impacts(args.impact),
        }, ensure_ascii=False))
        return
    if args.mode == "self-check":
        report = frontier.run_self_check(HERE, args.output)
        print(json.dumps(report, ensure_ascii=False))
        return
    if args.mode == "preflight":
        if None in (args.binary, args.embedding_bundle_dir, args.codex_binary, args.codex_auth_file, args.isolation_dir):
            raise lifecycle.LifecycleError("preflight 需要 --binary、--embedding-bundle-dir、--codex-binary、--codex-auth-file 与 --isolation-dir")
        report = preflight.run(
            HERE, args.repository, args.binary, args.embedding_bundle_dir, args.codex_binary, args.codex_auth_file, args.isolation_dir
        )
        print(json.dumps(report, ensure_ascii=False))
        return
    if args.mode == "bind":
        if args.config is None or args.output is None:
            raise lifecycle.LifecycleError("bind 需要 --config 与 --output")
        result = binding.create(HERE, args.config, args.output)
        print(json.dumps(result, ensure_ascii=False))
        return
    if args.state is None:
        raise lifecycle.LifecycleError("该模式需要 --state")
    if args.mode == "init":
        if args.binding is None:
            raise lifecycle.LifecycleError("init 需要 --binding")
        binding_data = json.loads(args.binding.read_text(encoding="utf-8"))
        lifecycle.save_state(args.state, lifecycle.new_state(contract, binding_data))
        print(json.dumps({"initialized": True, "state": str(args.state)}, ensure_ascii=False))
        return
    state = lifecycle.load_state(args.state)
    if args.mode == "execute":
        if args.config is None or args.checkpoint_mode is None:
            raise lifecycle.LifecycleError("execute 需要 --config 与 --checkpoint-mode")
        result = execution.execute(HERE, contract, args.state, args.checkpoint_mode, execution.load_config(args.config), resume=args.resume)
        print(json.dumps(result, ensure_ascii=False))
        return
    if args.mode == "rebind":
        if args.binding is None:
            raise lifecycle.LifecycleError("rebind 需要 --binding")
        binding_data = json.loads(args.binding.read_text(encoding="utf-8"))
        removed = lifecycle.rebind(contract, state, binding_data)
        lifecycle.save_state(args.state, state)
        print(json.dumps({"removed": removed, "baseline_preserved": state.get("baseline") is not None}, ensure_ascii=False))
        return
    if args.mode == "invalidate":
        removed = lifecycle.invalidate(contract, state, str(args.checkpoint_mode))
        lifecycle.save_state(args.state, state)
        print(json.dumps({"removed": removed}, ensure_ascii=False))
        return
    if args.mode == "promote":
        lifecycle.promote_baseline(contract, state)
        lifecycle.save_state(args.state, state)
        print(json.dumps({"baseline": state["baseline"]}, ensure_ascii=False))
        return
    if args.output is None:
        raise lifecycle.LifecycleError("summarize 需要 --output")
    reusable = lifecycle.reusable_report(contract, state, "summarize")
    if reusable is not None:
        if not args.resume:
            raise lifecycle.LifecycleError("summarize 已有有效检查点；使用 --resume 复用")
        print(json.dumps(json.loads(reusable.read_text(encoding="utf-8")), ensure_ascii=False))
        return
    if args.output.exists():
        if not args.resume:
            raise lifecycle.LifecycleError("汇总报告已存在；使用 --resume 恢复")
        try:
            recovered = json.loads(args.output.read_text(encoding="utf-8"))
            lifecycle.record(
                contract, state, "summarize", recovered, lifecycle.file_sha256(args.output), 0,
                str(args.output.resolve()),
            )
        except (OSError, ValueError, json.JSONDecodeError):
            args.output.unlink(missing_ok=True)
        else:
            lifecycle.save_state(args.state, state)
            print(json.dumps(recovered, ensure_ascii=False))
            return
    report = lifecycle.summarize(contract, state)
    layer_reports = [Path(state["checkpoints"][name]["report_path"]).resolve() for name in ("core", "full", "longmemeval")]
    report_directories = {path.parent for path in layer_reports}
    if len(report_directories) != 1 or args.output.resolve().parent not in report_directories:
        raise lifecycle.LifecycleError("汇总报告必须与三层报告位于同一验收工作区的 reports 目录")
    attach_artifacts(
        report,
        args.output,
        layer_reports,
    )
    args.output.parent.mkdir(parents=True, exist_ok=True)
    temporary = args.output.with_suffix(args.output.suffix + ".tmp")
    temporary.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    temporary.replace(args.output)
    lifecycle.record(contract, state, "summarize", report, lifecycle.file_sha256(args.output), 0, str(args.output.resolve()))
    lifecycle.save_state(args.state, state)
    print(json.dumps(report, ensure_ascii=False))


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, json.JSONDecodeError) as error:
        print(f"acceptance-suite: {error}", file=sys.stderr)
        raise SystemExit(2)
