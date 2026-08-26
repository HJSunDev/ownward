#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

from contract import load_contract


for stream in (sys.stdout, sys.stderr):
    reconfigure = getattr(stream, "reconfigure", None)
    if callable(reconfigure):
        reconfigure(encoding="utf-8")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Ownward Acceptance Suite v1 唯一执行入口")
    parser.add_argument("mode", choices=["check", "plan", "self-check", "kernel-iteration", "kernel-storage", "kernel-execution", "preflight", "bind", "init", "rebind", "execute", "invalidate", "promote", "summarize"])
    parser.add_argument("--state", type=Path)
    parser.add_argument("--binding", type=Path)
    parser.add_argument("--impact", action="append", default=[])
    parser.add_argument("--stage")
    parser.add_argument("--checkpoint-mode")
    parser.add_argument("--output", type=Path)
    parser.add_argument("--isolation-dir", type=Path)
    parser.add_argument("--config", type=Path)
    parser.add_argument("--resume", action="store_true")
    parser.add_argument("--scope")
    parser.add_argument("--formal-run", type=Path)
    parser.add_argument("--candidate")
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    if args.mode == "check":
        from evidence import validate_suite_inputs
        contract = load_contract(HERE / "contract.json")
        validate_suite_inputs(HERE)
        print(json.dumps({"passed": True, "suite_version": contract["suite_version"]}, ensure_ascii=False))
        return
    if args.mode == "plan":
        import lifecycle
        if args.stage:
            if args.impact:
                raise lifecycle.LifecycleError("plan 不得同时声明 --impact 与 --stage")
            print(json.dumps({"modes": lifecycle.plan_for_stage(args.stage), "stage": args.stage}, ensure_ascii=False))
        else:
            print(json.dumps({"modes": lifecycle.plan_for_impacts(args.impact), "targeted_stages": lifecycle.stages_for_impacts(args.impact)}, ensure_ascii=False))
        return
    if args.mode == "self-check":
        import frontier
        print(json.dumps(frontier.run_self_check(HERE, args.output), ensure_ascii=False))
        return
    if args.mode == "kernel-iteration":
        if args.formal_run is None or args.output is None or args.candidate is None:
            raise ValueError("kernel-iteration 需要 --formal-run、--output 与 --candidate")
        import kernel_iteration
        print(json.dumps(kernel_iteration.run(HERE, args.formal_run, args.output, args.candidate, args.resume), ensure_ascii=False))
        return
    if args.mode == "kernel-storage":
        if args.formal_run is None or args.output is None or args.candidate is None:
            raise ValueError("kernel-storage 需要 --formal-run、--output 与 --candidate")
        import kernel_iteration
        print(json.dumps(kernel_iteration.run_storage(HERE, args.formal_run, args.output, args.candidate, args.resume), ensure_ascii=False))
        return
    if args.mode == "kernel-execution":
        if args.formal_run is None or args.output is None or args.candidate is None:
            raise ValueError("kernel-execution 需要 --formal-run、--output 与 --candidate")
        import kernel_iteration
        print(json.dumps(kernel_iteration.run_execution(HERE, args.formal_run, args.output, args.candidate, args.resume), ensure_ascii=False))
        return
    if args.mode == "preflight":
        if args.config is None or args.isolation_dir is None:
            raise ValueError("preflight 需要 --config 与 --isolation-dir")
        import preflight
        import binding
        config = binding.load_json(args.config)
        print(json.dumps(preflight.run(HERE, config, args.isolation_dir), ensure_ascii=False))
        return
    if args.mode == "bind":
        if args.config is None or args.output is None:
            raise ValueError("bind 需要 --config 与 --output")
        import binding
        print(json.dumps(binding.create(HERE, args.config, args.output), ensure_ascii=False))
        return

    contract = load_contract(HERE / "contract.json")
    import lifecycle
    if args.state is None:
        raise lifecycle.LifecycleError("该模式需要 --state")
    if args.mode == "init":
        if args.binding is None:
            raise lifecycle.LifecycleError("init 需要 --binding")
        import binding
        value = binding.load_active_binding(args.binding)
        lifecycle.save_state(args.state, lifecycle.new_state(contract, value))
        print(json.dumps({"initialized": True, "state": str(args.state)}, ensure_ascii=False))
        return
    state = lifecycle.load_state(args.state)
    if args.mode == "execute":
        if args.config is None or args.checkpoint_mode is None:
            raise lifecycle.LifecycleError("execute 需要 --config 与 --checkpoint-mode")
        import execution
        result = execution.execute(HERE, contract, args.state, args.checkpoint_mode, execution.load_config(args.config), resume=args.resume)
        print(json.dumps(result, ensure_ascii=False))
        return
    if args.mode == "rebind":
        import binding
        if args.scope is not None:
            if args.config is None:
                raise lifecycle.LifecycleError("scope rebind 需要 --config")
            config = binding.load_json(args.config)
            value = binding.rebind_scope(HERE, args.config, Path(config["binding_dir"]), args.scope)
        else:
            if args.binding is None:
                raise lifecycle.LifecycleError("rebind 需要 --binding，scope rebind 需要 --scope 与 --config")
            value = binding.load_active_binding(args.binding)
        removed = lifecycle.rebind(contract, state, value)
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
            lifecycle.record(contract, state, "summarize", recovered, lifecycle.file_sha256(args.output), 0, str(args.output.resolve()))
        except (OSError, ValueError, json.JSONDecodeError):
            args.output.unlink(missing_ok=True)
        else:
            lifecycle.save_state(args.state, state)
            print(json.dumps(recovered, ensure_ascii=False))
            return
    report = lifecycle.summarize(contract, state)
    layer_reports = [Path(state["checkpoints"][name]["report_path"]).resolve() for name in ("core", "full", "longmemeval")]
    if len({path.parent for path in layer_reports}) != 1 or args.output.resolve().parent not in {path.parent for path in layer_reports}:
        raise lifecycle.LifecycleError("汇总报告必须与三层报告位于同一验收工作区的 reports 目录")
    from evidence import attach_artifacts
    attach_artifacts(report, args.output, layer_reports)
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
