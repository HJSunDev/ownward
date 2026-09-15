"""Freeze a stratified selection and prepare reusable, pre-answer memory states."""
from __future__ import annotations

import argparse
from collections import Counter
from concurrent.futures import FIRST_COMPLETED, ThreadPoolExecutor, wait
import hashlib
import inspect
import json
from pathlib import Path
import subprocess
import sys
import time

import run as product


SEED = "ownward-longmemeval-s-stratified-100-v1-20260914"
QUESTION_WORKERS = 6
EXTERNAL_SLOTS = 8


def failure_scope(error):
    # Only known source-output failures are safe to isolate from other questions.
    if isinstance(error, product.AdapterError) and str(error).startswith((
        "semantic submission batch contains failures:",
        "semantic source repair remains incomplete:",
        "organization location repair remains incomplete:",
    )):
        return "question"
    return "run"


def prepare_pending(pending, one, rows, progress, *, workers=QUESTION_WORKERS):
    consecutive_failures = 0
    stop_reason = None
    with ThreadPoolExecutor(max_workers=workers) as pool:
        iterator = iter(pending)
        active = {}

        def fill():
            while not stop_reason and len(active) < workers:
                item = next(iterator, None)
                if item is None:
                    break
                active[pool.submit(one, item)] = item

        fill()
        while active:
            done, _ = wait(active, return_when=FIRST_COMPLETED)
            for future in done:
                active.pop(future)
                row = future.result()
                rows.append(row)
                if row["prepared"]:
                    consecutive_failures = 0
                else:
                    consecutive_failures += 1
                    if row.get("failure_scope") != "question":
                        stop_reason = "shared_or_unclassified_failure"
                    elif consecutive_failures >= 3 and stop_reason is None:
                        stop_reason = "consecutive_question_failures"
            fill()
            progress({"prepared": sum(r["prepared"] for r in rows),
                      "failed": sum(not r["prepared"] for r in rows),
                      "active": [r["ordinal"] for r in active.values()],
                      "stop_reason": stop_reason,
                      "rows": sorted(rows, key=lambda r: r["ordinal"])})
    return stop_reason


def freeze(path, value):
    if path.exists():
        product.require(product.load_json(path) == value, f"immutable material changed: {path}")
    else:
        product.write_json(path, value)


def freeze_preparation_protocol(path, protocol):
    # Reader and Judge settings are not dependencies of prepared memory.
    if path.exists():
        product.require(product.load_json(path).get("memory") == protocol["memory"],
                        f"immutable preparation protocol changed: {path}")
    else:
        freeze(path, {"memory": protocol["memory"]})


def selection(questions, dataset_hash):
    counts = Counter(q["question_type"] for q in questions)
    allocation = {kind: count * 100 // len(questions) for kind, count in counts.items()}
    remainder = 100 - sum(allocation.values())
    for kind in sorted(counts, key=lambda k: (-(counts[k] * 100 % len(questions)), k))[:remainder]:
        allocation[kind] += 1
    chosen = set()
    for kind in sorted(counts):
        ids = [q["question_id"] for q in questions if q["question_type"] == kind]
        ids.sort(key=lambda qid: hashlib.sha256((SEED + "\n" + kind + "\n" + qid).encode()).digest())
        chosen.update(ids[:allocation[kind]])
    rows = [{"ordinal": i + 1, "question_id": q["question_id"], "category": q["question_type"]}
            for i, q in enumerate(questions) if q["question_id"] in chosen]
    assert len(rows) == len(chosen) == 100
    return {"schema": "ownward.stratified-selection/v1", "dataset_sha256": dataset_hash,
            "seed": SEED, "method": "largest-remainder; ties by category; SHA256 ranks within category",
            "population": dict(sorted(counts.items())), "allocation": dict(sorted(allocation.items())),
            "selection_inputs": ["question_id", "question_type"], "questions": rows}


def source_only(q):
    return {"question_id": q["question_id"],
            "haystack_dates": q["haystack_dates"], "haystack_session_ids": q["haystack_session_ids"],
            "haystack_sessions": [[{"role": t["role"], "content": t["content"]} for t in session]
                                  for session in q["haystack_sessions"]]}


def hash_tree(root):
    return {p.relative_to(root).as_posix(): product.sha256(p)
            for p in sorted(root.rglob("*")) if p.is_file()}


def check_preparation_dependencies(states, dependencies, *, rebuild_changed=False):
    identity = product.canonical_sha256(dependencies)
    if (states / identity / "dependencies.json").is_file() or rebuild_changed:
        return
    changes = []
    for path in sorted(states.glob("*/dependencies.json")):
        previous = product.load_json(path)
        changed = sorted(key for key in previous.keys() | dependencies.keys()
                         if previous.get(key) != dependencies.get(key))
        changes.append({"state": path.parent.name, "changed": changed})
    product.require(not changes,
        f"prepared dependencies changed: {changes}; review affected preparation layers before "
        "using --rebuild-changed. Existing materials have not been rewritten or regenerated.")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--environment-root", type=Path, required=True)
    parser.add_argument("--execution-config", type=Path, required=True)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--selection-only", action="store_true")
    parser.add_argument("--rebuild-changed", action="store_true",
                        help="prepare a new state after reviewing changed ingestion dependencies")
    args = parser.parse_args()
    repo = Path(__file__).resolve().parents[2]
    environment = args.environment_root.resolve()
    manifest = product.load_json(environment / "manifests/v1.json")
    dataset = Path(manifest["layout"]["data"])
    questions = product.validate_dataset(dataset, formal=True)
    dataset_hash = product.sha256(dataset)
    materials = environment / "prepared" / "longmemeval-s" / dataset_hash
    selected = selection(questions, dataset_hash)
    selected_path = materials / "selections" / "stratified-100-v1.json"
    freeze(selected_path, selected)
    print(json.dumps({"event": "selection_frozen", "allocation": selected["allocation"],
                      "manifest": str(selected_path)}, ensure_ascii=False), flush=True)
    by_id = {q["question_id"]: q for q in questions}
    for row in selected["questions"]:
        raw = source_only(by_id[row["question_id"]])
        freeze(materials / "sources" / raw["question_id"] / (product.canonical_sha256(raw) + ".json"), raw)
    if args.selection_only:
        return
    settings = product.load_json(args.execution_config)
    external = settings["community"]["external_intelligence"]
    protocol = product.apply_external_intelligence_roles(product.load_json(repo / "benchmarks/longmemeval_s/protocol.json"),
        {k: external["roles"][k] for k in ("semantic", "reader", "judge")})
    assert protocol["memory"]["semantic_model"] == "qwen3.8-flash"
    assert protocol["memory"]["semantic_reasoning_effort"] == "medium"
    contract_path = repo / "manifests/kernel-candidates/v2/source-ownership/semantic-representation.json"
    contract = product.semantic_representation.load_contract(contract_path)
    embedding = Path(settings["candidate"]["embedding_bundle_dir"])
    driver = repo / "benchmarks/longmemeval_s/go_api_external_intelligence.py"
    # The preparation identity excludes the sample, task question, Reader and Judge.
    dependencies = {
        "dataset_sha256": dataset_hash, "binary_sha256": product.sha256(args.binary),
        "embedding_files": hash_tree(embedding), "memory": protocol["memory"],
        "representation": product.load_json(contract_path),
        "organization_contract": product.semantic_representation.organization_contract(),
        "source_import": hashlib.sha256(inspect.getsource(source_only).encode()).hexdigest(),
        "preparation_implementation": product.semantic_implementation_identity(),
        "transport_driver": product.sha256(driver),
    }
    dependency_id = product.canonical_sha256(dependencies)
    check_preparation_dependencies(materials / "states", dependencies,
                                   rebuild_changed=args.rebuild_changed)
    state = materials / "states" / dependency_id
    freeze(state / "dependencies.json", dependencies)
    freeze_preparation_protocol(state / "protocol.json", protocol)
    receipt_root = materials / "preparation-runs" / time.strftime("%Y%m%d-%H%M%S")
    receipt_root.mkdir(parents=True)
    product.write_json(receipt_root / "identity.json", {
        "selection": selected_path.relative_to(materials).as_posix(), "dependency_id": dependency_id,
        "head": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=repo).decode().strip(),
        "runner_sha256": product.sha256(Path(__file__)), "formal_entry_sha256": product.sha256(Path(product.__file__)),
        "mode": "prepare_only", "question_workers": QUESTION_WORKERS, "external_slots": EXTERNAL_SLOTS,
        "reader_calls": 0, "judge_calls": 0,
    })
    pending = []
    rows = []
    for item in selected["questions"]:
        root = state / "questions" / item["question_id"]
        receipt = root / "prepared.json"
        if receipt.is_file():
            value = product.load_json(receipt)
            assert value["dependency_id"] == dependency_id
            for relative, expected in value["files"].items():
                product.require(product.sha256(root / relative) == expected, f"prepared material changed: {item['question_id']} {relative}")
            rows.append({"ordinal": item["ordinal"], "question_id": item["question_id"], "prepared": True, "reused": True})
        else:
            pending.append(item)
    print(json.dumps({"event": "preparation_started", "reused": len(rows), "pending": len(pending),
                      "state": str(state), "receipts": str(receipt_root)}), flush=True)
    started = time.monotonic()
    import os
    product.write_json(receipt_root / "process.json", {"pid": os.getpid(), "state": "running", "started": time.time()})
    with product.ExternalIntelligenceScheduler(EXTERNAL_SLOTS) as scheduler:
        with product.open_external_intelligence_runtime(driver=external["driver"], binary=driver,
                credential_file=Path(external["credential_file"]), max_active=EXTERNAL_SLOTS, worker_processes=EXTERNAL_SLOTS,
                runtime_parent=receipt_root / ".runtime") as transport:
            def one(item):
                qid = item["question_id"]
                product.write_json(receipt_root / (qid + ".json"), {"state": "running", **item, "started": time.time()})
                print(json.dumps({"event": "started", **item}), flush=True)
                raw = source_only(by_id[qid])
                raw_hash = product.canonical_sha256(raw)
                q = {**raw, "question": "", "question_type": "", "question_date": ""}
                begin = time.monotonic()
                try:
                    value = product.process_question(q, state, dependency_id, args.binary, embedding,
                        protocol, environment / "unused-evaluator", lambda: product.ExternalIntelligenceCapability(transport, contract),
                        scheduler, prepare_only=True, runtime_workers=min(QUESTION_WORKERS, len(pending)))
                    root = state / "questions" / qid
                    assert not any((root / name).exists() for name in ("reader", "judge", "answer.json", "result.json"))
                    checkpoint = product.load_json(root / "checkpoint.json")
                    assert len(checkpoint["assets"]) == len(raw["haystack_sessions"])
                    assert checkpoint["submission_order"] == list(range(value["semantic_batches"]))
                    files = hash_tree(root)
                    byte_count = sum((root / relative).stat().st_size for relative in files)
                    receipt = {**value, "dependency_id": dependency_id, "source_sha256": raw_hash,
                               "source_path": (materials / "sources" / qid / (raw_hash + ".json")).relative_to(materials).as_posix(),
                               "files": files, "bytes": byte_count, "pre_answer": True,
                               "wall_seconds": time.monotonic() - begin}
                    freeze(root / "prepared.json", receipt)
                    row = {**item, "prepared": True, "reused": False, "seconds": receipt["wall_seconds"],
                           "assets": value["asset_count"], "bytes": byte_count, "usage": value["usage"]}
                except Exception as error:
                    row = {**item, "prepared": False, "error_type": type(error).__name__, "error": str(error),
                           "failure_scope": failure_scope(error)}
                product.write_json(receipt_root / (qid + ".json"), row)
                print(json.dumps({"event": "finished", **row}, ensure_ascii=False), flush=True)
                return row
            stop_reason = prepare_pending(pending, one, rows,
                lambda value: product.write_json(receipt_root / "progress.json", value))
    failed = any(not row["prepared"] for row in rows)
    summary = {"prepared": sum(r["prepared"] for r in rows), "failed": sum(not r["prepared"] for r in rows),
               "selected": 100, "elapsed_seconds": time.monotonic() - started,
               "dependency_id": dependency_id, "reader_calls": 0, "judge_calls": 0, "stop_reason": stop_reason,
               "rows": sorted(rows, key=lambda r: r["ordinal"])}
    product.write_json(receipt_root / "summary.json", summary)
    product.write_json(receipt_root / "process.json", {"pid": os.getpid(), "state": "stopped" if failed else "finished", "finished": time.time()})
    print(json.dumps({"event": "stopped" if failed else "all_prepared", "prepared": summary["prepared"],
                      "failed": summary["failed"], "elapsed_seconds": summary["elapsed_seconds"]}), flush=True)
    if failed:
        sys.exit(1)


if __name__ == "__main__":
    main()
