#!/usr/bin/env python3
"""Stage-3 non-formal LongMemEval adapter with an explicit V0 public-path fallback."""
from __future__ import annotations

import importlib.util
from pathlib import Path
import sys
import time
from typing import Any


REPOSITORY = Path(__file__).resolve().parents[3]
LONG_ROOT = REPOSITORY / "benchmarks" / "longmemeval_s"
if str(LONG_ROOT) not in sys.path:
    sys.path.insert(0, str(LONG_ROOT))
SPEC = importlib.util.spec_from_file_location("ownward_stage3_longmemeval", LONG_ROOT / "run.py")
if SPEC is None or SPEC.loader is None:
    raise RuntimeError("cannot load the frozen LongMemEval-S adapter")
adapter = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = adapter
SPEC.loader.exec_module(adapter)

_public_retrieve = adapter.retrieve
_official_prompt = adapter.official_prompt


def official_prompt_with_explicit_unanswerable(
    evaluator: Path,
    question: dict[str, Any],
    hypothesis: str,
) -> str:
    """Render synthetic unanswerable questions through the official abstention contract."""
    if question.get("question_type") != "unanswerable":
        return _official_prompt(evaluator, question, hypothesis)
    spec = importlib.util.spec_from_file_location("longmemeval_official_evaluate_qa", evaluator)
    adapter.require(spec is not None and spec.loader is not None, "official evaluator cannot be imported")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    prompt = module.get_anscheck_prompt(
        question["question_type"],
        question["question"],
        question["answer"],
        hypothesis,
        abstention=True,
    )
    adapter.require(isinstance(prompt, str) and prompt, "official evaluator returned no prompt")
    return prompt


def retrieve_with_v0_compatibility(runtime: Any, question: str, protocol: dict[str, Any]) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    """Use the current public evidence path, or V0's original public full-read path when absent."""
    try:
        return _public_retrieve(runtime, question, protocol)
    except adapter.MCPError as error:
        message = str(error)
        if "unknown tool" not in message or "ownward_evidence_search" not in message:
            raise
    adapter.require(runtime.client is not None, "Ownward client is unavailable")
    settings = protocol["retrieval"]
    started = time.monotonic()
    search = runtime.client.call_tool("ownward_search", {"query": question, "limit": settings["search_limit"]})
    search_ms = (time.monotonic() - started) * 1000
    results = search.get("results") if isinstance(search, dict) else None
    adapter.require(isinstance(results, list), "Ownward search returned no result list")
    observed = [
        {"id": result["id"], "score": result.get("score"), "signals": result.get("signals", [])}
        for result in results
        if isinstance(result, dict) and isinstance(result.get("id"), str)
    ]
    evidence: list[dict[str, Any]] = []
    read_ids: list[str] = []
    read_paths: list[dict[str, Any]] = []
    selection_steps: list[dict[str, Any]] = []
    read_ms = 0.0
    used_chars = 0
    for source_rank, result in enumerate(observed):
        if len(evidence) >= int(settings["read_limit"]):
            break
        before = time.monotonic()
        try:
            read = runtime.client.call_tool("ownward_read", {"id": result["id"]})
        except adapter.MCPError:
            read_ms += (time.monotonic() - before) * 1000
            selection_steps.append({"source_id": result["id"], "source_rank": source_rank, "mode": "full", "depth": 0, "selected": False, "reason": "unreadable"})
            continue
        read_ms += (time.monotonic() - before) * 1000
        information = read.get("information") if isinstance(read, dict) else None
        if not isinstance(information, dict) or not isinstance(information.get("content"), str):
            selection_steps.append({"source_id": result["id"], "source_rank": source_rank, "mode": "full", "depth": 0, "selected": False, "reason": "unreadable"})
            continue
        content = information["content"]
        step = {"source_id": result["id"], "source_rank": source_rank, "mode": "full", "depth": 0, "content_runes": len(content)}
        if used_chars + len(content) > int(settings["context_max_chars"]):
            selection_steps.append({**step, "selected": False, "reason": "context_budget"})
            if evidence:
                break
            continue
        evidence.append({"id": information["id"], "content": content})
        read_ids.append(result["id"])
        read_paths.append({"source_id": result["id"], "mode": "full", "evidence_ids": []})
        selection_steps.append({**step, "selected": True})
        used_chars += len(content)
    adapter.require(evidence, "Ownward retrieval produced no readable evidence")
    return evidence, {
        "search_ms": search_ms, "evidence_search_ms": 0.0, "read_ms": read_ms,
        "total_ms": search_ms + read_ms, "returned": observed, "read_ids": read_ids,
        "evidence_read_ids": [], "read_paths": read_paths, "context_chars": used_chars,
        "limits": {
            "read_units": int(settings["read_limit"]),
            "context_chars": int(settings["context_max_chars"]),
            "evidence_depth_per_source": int(settings["evidence_search_limit_per_source"]),
        },
        "selection_policy": "v0-ranked-full-read/v1", "selection_steps": selection_steps,
    }


adapter.retrieve = retrieve_with_v0_compatibility
adapter.official_prompt = official_prompt_with_explicit_unanswerable


if __name__ == "__main__":
    raise SystemExit(adapter.main())
