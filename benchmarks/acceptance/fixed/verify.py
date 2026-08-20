#!/usr/bin/env python3
from __future__ import annotations

import argparse
from collections import defaultdict
import hashlib
import json
import math
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
from typing import Any

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parents[1]))
from support.ownward_mcp import OwnwardRuntime  # noqa: E402

RELATION_TYPES = {
    "same_as", "broader_than", "narrower_than", "part_of", "has_part",
    "supports", "contradicts", "derived_from", "applies_in", "related_to",
}


def require(condition: bool, message: str) -> None:
    if not condition:
        raise RuntimeError(message)


def load_json(path: Path) -> Any:
    return json.loads(path.read_text(encoding="utf-8"))


def load_jsonl(path: Path) -> list[dict[str, Any]]:
    result: list[dict[str, Any]] = []
    for line in path.read_text(encoding="utf-8").splitlines():
        if line.strip():
            value = json.loads(line)
            require(isinstance(value, dict), f"JSONL row is not an object: {path}")
            result.append(value)
    return result


def write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    temporary.replace(path)


def sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def resolve(base: Path, value: str) -> Path:
    path = Path(value)
    return path.resolve() if path.is_absolute() else (base / path).resolve()


def percentile(values: list[float], quantile: float) -> float:
    require(values, "percentile input is empty")
    ordered = sorted(values)
    return ordered[max(0, math.ceil(len(ordered) * quantile) - 1)]


def ratio(numerator: int, denominator: int) -> float:
    return numerator / denominator if denominator else 0.0


def average(values: list[float]) -> float:
    return sum(values) / len(values) if values else 0.0


def reciprocal_rank(returned: list[str], expected: set[str], limit: int) -> float:
    for index, value in enumerate(returned[:limit]):
        if value in expected:
            return 1 / (index + 1)
    return 0.0


def ndcg(returned: list[str], expected: set[str], limit: int) -> float:
    dcg = sum(1 / math.log2(index + 2) for index, value in enumerate(returned[:limit]) if value in expected)
    ideal = sum(1 / math.log2(index + 2) for index in range(min(len(expected), limit)))
    return dcg / ideal if ideal else 1.0


def recall(returned: list[str], expected: set[str], limit: int) -> float:
    return ratio(len(set(returned[:limit]) & expected), len(expected))


def canonical_relation(source: str, relation_type: str, target: str) -> tuple[str, str, str]:
    if relation_type == "related_to" and source > target:
        source, target = target, source
    return source, relation_type, target


def command_prefix(binary: Path) -> list[str]:
    require(binary.suffix.lower() not in {".cmd", ".bat", ".ps1", ".js"}, "formal evidence requires native executables")
    return [str(binary)]


def isolated_codex_environment(auth_file: Path, root: Path, base: dict[str, str], token: str) -> dict[str, str]:
    root.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(auth_file, root / "auth.json")
    environment = dict(base)
    environment["CODEX_HOME"] = str(root)
    environment["OWNWARD_MCP_BEARER_TOKEN"] = token
    environment["NO_PROXY"] = "127.0.0.1,localhost"
    environment["no_proxy"] = "127.0.0.1,localhost"
    environment.pop("OPENAI_API_KEY", None)
    return environment


def semantic_prompt(asset_ids: list[str], model: str) -> str:
    return (
        "Act only as Ownward's external semantic capability. Use only the connected Ownward semantic tools. "
        "Call ownward_semantic_work once with exactly the asset IDs below, analyze only the returned assets and candidate contexts, "
        "then call ownward_semantic_submit_batch once with one submission per work item. "
        f"Use schema ownward.semantic-submission/v1, capability id codex, capability version {model}, and execution fixed-regression. "
        "Preserve source meaning and cite evidence present in the work. Do not use benchmark queries, expected answers, relation gold, "
        "outside knowledge, or temporary task intent. Relations may use only "
        + ", ".join(sorted(RELATION_TYPES))
        + ". Submit uncertain rather than guessing.\n\nAsset IDs:\n"
        + json.dumps(asset_ids, ensure_ascii=False)
    )


def semantic_trace_calls(text: str) -> list[tuple[str, bool]]:
    calls: list[tuple[str, bool]] = []
    for line in text.splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError as error:
            raise RuntimeError("Codex emitted non-JSON trace output") from error
        item = event.get("item") if isinstance(event, dict) and event.get("type") == "item.completed" else None
        if not isinstance(item, dict):
            continue
        require(item.get("type") not in {"command_execution", "file_change", "web_search"}, "semantic agent used a bypass tool")
        if item.get("type") != "mcp_tool_call":
            continue
        require(item.get("server") == "ownward", "semantic agent called a non-Ownward MCP server")
        calls.append((str(item.get("tool", "")), item.get("status") == "completed" and item.get("error") is None))
    return calls


def run_semantic_agent(
    args: argparse.Namespace,
    runtime: OwnwardRuntime,
    asset_ids: list[str],
    evidence_name: str,
    environment: dict[str, str],
) -> dict[str, Path]:
    require(runtime.binding is not None, "Ownward runtime has no binding")
    output = args.evidence_dir / f"semantic-{evidence_name}.txt"
    events = args.evidence_dir / f"semantic-{evidence_name}.jsonl"
    stderr = args.evidence_dir / f"semantic-{evidence_name}.stderr.txt"
    prompt = semantic_prompt(asset_ids, args.codex_model)
    command = command_prefix(args.codex_binary) + [
        "exec", "--ephemeral", "--json", "--color", "never", "--skip-git-repo-check",
        "-C", str(args.evidence_dir), "--sandbox", "read-only", "-m", args.codex_model,
        "-c", f"model_reasoning_effort={json.dumps(args.codex_reasoning_effort)}",
        "-c", f"mcp_servers.ownward.url={json.dumps(runtime.binding.endpoint)}",
        "-c", 'mcp_servers.ownward.bearer_token_env_var="OWNWARD_MCP_BEARER_TOKEN"',
        "-c", "features.apps=false", "-c", "features.multi_agent=false", "-c", "features.plugins=false",
        "-c", "features.shell_tool=false", "-c", 'web_search="disabled"', "-o", str(output), prompt,
    ]
    with tempfile.TemporaryDirectory(prefix="ownward-fixed-codex-") as temporary:
        completed = subprocess.run(
            command, check=False, capture_output=True, text=True, encoding="utf-8",
            timeout=args.codex_timeout_seconds,
            env=isolated_codex_environment(args.codex_auth_file, Path(temporary) / "codex-home", environment, runtime.binding.bearer_token),
        )
    events.write_text(completed.stdout, encoding="utf-8")
    stderr.write_text(completed.stderr, encoding="utf-8")
    require(completed.returncode == 0, f"Codex semantic organization failed: {completed.stderr[-2000:]}")
    calls = semantic_trace_calls(completed.stdout)
    require(calls == [("ownward_semantic_work", True), ("ownward_semantic_submit_batch", True)], "semantic agent changed the bounded collaboration path")
    return {f"semantic_{evidence_name}_output": output, f"semantic_{evidence_name}_events": events, f"semantic_{evidence_name}_stderr": stderr}


def load_fixtures(baseline_path: Path) -> dict[str, Any]:
    descriptor = load_json(baseline_path)
    require(isinstance(descriptor, dict), "baseline descriptor is invalid")
    base = baseline_path.parent
    return {
        "descriptor": descriptor,
        "thresholds": load_json(resolve(base, str(descriptor["thresholds"]))),
        "information": load_jsonl(resolve(base, str(descriptor["information"]))),
        "relations": load_jsonl(resolve(base, str(descriptor["relation_gold"]))),
        "queries": load_jsonl(resolve(base, str(descriptor["queries"]))),
        "updates": load_jsonl(resolve(base, str(descriptor["updates"]))) if descriptor.get("updates") else [],
    }


def product_environment(*, disable_relations: bool) -> dict[str, str]:
    environment = os.environ.copy()
    for name in (
        "OPENAI_API_KEY", "OWNWARD_MODEL_BASE_URL", "OWNWARD_MODEL_API_KEY", "OWNWARD_CHAT_MODEL",
        "OWNWARD_EMBEDDING_MODEL", "OWNWARD_EMBEDDING_DIMENSIONS",
    ):
        environment.pop(name, None)
    environment["OWNWARD_DISABLE_RELATIONS"] = "true" if disable_relations else "false"
    environment["NO_PROXY"] = "127.0.0.1,localhost"
    environment["no_proxy"] = "127.0.0.1,localhost"
    return environment


def create_and_organize(args: argparse.Namespace, fixtures: dict[str, Any]) -> tuple[dict[str, str], list[float], dict[str, Path]]:
    data_dir = args.evidence_dir / "full-data"
    require(not data_dir.exists(), "fixed-regression full-data already exists; use a new evidence directory")
    mapping: dict[str, str] = {}
    durations: list[float] = []
    evidence: dict[str, Path] = {}
    information = list(fixtures["information"])
    environment = product_environment(disable_relations=False)
    with OwnwardRuntime(args.binary, data_dir, args.runtime_dir, environment, operation_seconds=45) as runtime:
        require(runtime.client is not None, "Ownward runtime did not start")
        for batch_index, offset in enumerate(range(0, len(information), 20)):
            batch = information[offset : offset + 20]
            begin = time.perf_counter()
            response = runtime.client.call_tool(
                "ownward_create_batch",
                {"items": [{"content": str(item["content"]), "contexts": item.get("contexts", []), "source": {"actor": "fixed-regression", "ref": str(item["fixture_id"])}} for item in batch]},
            )
            results = response.get("results") if isinstance(response, dict) else None
            require(isinstance(results, list) and len(results) == len(batch), "fixed-regression create batch is incomplete")
            asset_ids: list[str] = []
            for item, result in zip(batch, results, strict=True):
                mutation = result.get("result") if isinstance(result, dict) and not result.get("error") else None
                value = mutation.get("information") if isinstance(mutation, dict) else None
                organization = mutation.get("organization") if isinstance(mutation, dict) else None
                require(isinstance(value, dict) and isinstance(organization, dict), "fixed-regression create failed")
                require(organization.get("status") == "pending", "created information did not expose semantic work")
                mapping[str(item["fixture_id"])] = str(value["id"])
                asset_ids.append(str(value["id"]))
            evidence.update(run_semantic_agent(args, runtime, asset_ids, f"create-{batch_index}", environment))
            for asset_id in asset_ids:
                status = runtime.client.call_tool("ownward_status", {"id": asset_id})
                organization = status.get("organization") if isinstance(status, dict) else None
                require(isinstance(organization, dict) and organization.get("status") == "ready", "created information did not become ready")
            elapsed = time.perf_counter() - begin
            durations.extend([elapsed] * len(batch))
        update_ids: list[str] = []
        update_started: dict[str, float] = {}
        for update in fixtures["updates"]:
            fixture_id = str(update["fixture_id"])
            asset_id = mapping[fixture_id]
            current = runtime.client.call_tool("ownward_read", {"id": asset_id})["information"]
            update_started[asset_id] = time.perf_counter()
            result = runtime.client.call_tool(
                "ownward_update",
                {"id": asset_id, "expected_revision": int(current["revision"]), "content": str(update["content"]), "contexts": update.get("contexts", [])},
            )["result"]
            require(result["information"]["id"] == asset_id and result["organization"]["status"] == "pending", "fixed update lost identity or semantic work")
            update_ids.append(asset_id)
        if update_ids:
            evidence.update(run_semantic_agent(args, runtime, update_ids, "updates", environment))
            for asset_id in update_ids:
                status = runtime.client.call_tool("ownward_status", {"id": asset_id})
                require(status["organization"]["status"] == "ready", "updated information did not become ready")
                durations.append(time.perf_counter() - update_started[asset_id])
    write_json(args.evidence_dir / "mapping.json", {"schema": "ownward.fixed-mapping/v1", "fixture_to_asset": mapping})
    return mapping, durations, evidence


def verify_assets(runtime: OwnwardRuntime, fixtures: dict[str, Any], mapping: dict[str, str]) -> dict[str, Any]:
    require(runtime.client is not None, "Ownward runtime did not start")
    updates = {str(item["fixture_id"]): item for item in fixtures["updates"]}
    retained = 0
    stable_updates = 0
    for item in fixtures["information"]:
        fixture_id = str(item["fixture_id"])
        expected = updates.get(fixture_id, item)
        value = runtime.client.call_tool("ownward_read", {"id": mapping[fixture_id]})["information"]
        retained += int(value.get("content") == expected.get("content") and value.get("contexts", []) == expected.get("contexts", []))
        stable_updates += int(int(value.get("revision", 0)) == (2 if fixture_id in updates else 1))
    return {"semantic_retention": ratio(retained, len(fixtures["information"])), "stable_identity_and_revision": ratio(stable_updates, len(fixtures["information"]))}


def organization_metrics(runtime: OwnwardRuntime, fixtures: dict[str, Any], mapping: dict[str, str]) -> dict[str, Any]:
    require(runtime.client is not None, "Ownward runtime did not start")
    reverse = {value: key for key, value in mapping.items()}
    expected = {canonical_relation(str(item["source_id"]), str(item["type"]), str(item["target_id"])) for item in fixtures["relations"]}
    actual: set[tuple[str, str, str]] = set()
    for asset_id in mapping.values():
        graph = runtime.client.call_tool("ownward_navigate", {"start_ids": [asset_id], "depth": 1, "limit": 100})["result"]
        for edge in graph.get("edges", []):
            source = reverse.get(str(edge.get("source_id", "")))
            target = reverse.get(str(edge.get("target_id", "")))
            if source and target:
                actual.add(canonical_relation(source, str(edge.get("type", "")), target))
    true_positive = len(expected & actual)
    return {"expected": len(expected), "actual": len(actual), "true_positive": true_positive, "precision": ratio(true_positive, len(actual)), "recall": ratio(true_positive, len(expected))}


def retrieval_metrics(full: OwnwardRuntime, baseline: OwnwardRuntime, fixtures: dict[str, Any], mapping: dict[str, str]) -> tuple[dict[str, Any], list[dict[str, Any]]]:
    require(full.client is not None and baseline.client is not None, "query runtimes did not start")
    reverse = {value: key for key, value in mapping.items()}
    by_type: dict[str, dict[str, Any]] = defaultdict(lambda: {"recalls": [], "ranks": [], "ndcgs": [], "passed": 0, "total": 0, "forbidden": 0, "forbidden_total": 0, "relation_gains": [], "relation_evidence_correct": 0, "relation_evidence_total": 0})
    evidence: list[dict[str, Any]] = []
    for query in fixtures["queries"]:
        arguments = {"query": str(query["query"]), "contexts": query.get("contexts", []), "limit": 10}
        begin = time.perf_counter()
        full_results = full.client.call_tool("ownward_search", arguments)["results"]
        latency = time.perf_counter() - begin
        baseline_results = baseline.client.call_tool("ownward_search", arguments)["results"]
        full_ids = [reverse[str(item["id"])] for item in full_results if str(item.get("id", "")) in reverse]
        baseline_ids = [reverse[str(item["id"])] for item in baseline_results if str(item.get("id", "")) in reverse]
        expected = {str(value) for value in query["expected_ids"]}
        forbidden = {str(value) for value in query.get("forbidden_ids", [])}
        kind = str(query["type"])
        values = by_type[kind]
        recall_value = recall(full_ids, expected, 5 if kind == "explicit_object" else 10)
        values["recalls"].append(recall_value)
        values["ranks"].append(reciprocal_rank(full_ids, expected, 10))
        values["ndcgs"].append(ndcg(full_ids, expected, 10))
        values["passed"] += int(recall_value == 1)
        values["total"] += 1
        values["forbidden"] += len(set(full_ids) & forbidden)
        values["forbidden_total"] += len(forbidden)
        if kind == "relation_constraint":
            relation_results = [item for item in full_results if "relation" in item.get("signals", [])]
            full_relation = sum(1 for item in relation_results if reverse.get(str(item.get("id", ""))) in expected)
            baseline_relation = sum(1 for item in baseline_results if "relation" in item.get("signals", []) and reverse.get(str(item.get("id", ""))) in expected)
            values["relation_gains"].append((full_relation - baseline_relation) / len(expected))
            values["relation_evidence_correct"] += full_relation
            values["relation_evidence_total"] += len(relation_results)
        evidence.append({"query_id": query["query_id"], "type": kind, "full_ids": full_ids, "baseline_ids": baseline_ids, "latency_seconds": latency})
    limits = fixtures["thresholds"]
    explicit, semantic = by_type["explicit_object"], by_type["semantic_intent"]
    relation, contextual = by_type["relation_constraint"], by_type["context_applicability"]
    metrics: dict[str, Any] = {
        "explicit_object": {"recall_at_5_min": min(explicit["recalls"]), "mrr_at_10_min": min(explicit["ranks"])},
        "semantic_intent": {"recall_at_10_min": min(semantic["recalls"]), "ndcg_at_10_min": min(semantic["ndcgs"])},
        "relation_constraint": {
            "recall": average(relation["recalls"]),
            "precision": ratio(relation["relation_evidence_correct"], relation["relation_evidence_total"]),
            "graph_evidence_gain": average(relation["relation_gains"]),
        },
        "context_applicability": {"accuracy": ratio(contextual["passed"], contextual["total"]), "incompatible_leakage": ratio(contextual["forbidden"], contextual["forbidden_total"])},
        "latency_p95_seconds": percentile([float(item["latency_seconds"]) for item in evidence], 0.95),
    }
    metrics["passed"] = (
        metrics["explicit_object"]["recall_at_5_min"] >= limits["retrieval"]["explicit_object"]["recall_at_5_min"]
        and metrics["explicit_object"]["mrr_at_10_min"] >= limits["retrieval"]["explicit_object"]["mrr_at_10_min"]
        and metrics["semantic_intent"]["recall_at_10_min"] >= limits["retrieval"]["semantic_intent"]["recall_at_10_min"]
        and metrics["semantic_intent"]["ndcg_at_10_min"] >= limits["retrieval"]["semantic_intent"]["ndcg_at_10_min"]
        and metrics["relation_constraint"]["recall"] >= limits["retrieval"]["relation_constraint"]["evidence_recall_min"]
        and metrics["relation_constraint"]["precision"] >= limits["retrieval"]["relation_constraint"]["evidence_precision_min"]
        and metrics["relation_constraint"]["graph_evidence_gain"] >= limits["organization"]["retrieval_relation_evidence_gain_over_no_graph_min"]
        and metrics["context_applicability"]["accuracy"] >= limits["retrieval"]["context_applicability"]["accuracy_min"]
        and metrics["context_applicability"]["incompatible_leakage"] <= limits["retrieval"]["context_applicability"]["incompatible_context_leakage_max"]
    )
    return metrics, evidence


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repository", type=Path, default=Path("."))
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--candidate", required=True)
    parser.add_argument("--baseline", type=Path, default=HERE.parent / "v5" / "baseline.json")
    parser.add_argument("--runtime-dir", type=Path, required=True)
    parser.add_argument("--codex-binary", type=Path, required=True)
    parser.add_argument("--codex-auth-file", type=Path, required=True)
    parser.add_argument("--codex-model", default="gpt-5.4-2026-03-05")
    parser.add_argument("--codex-reasoning-effort", default="low")
    parser.add_argument("--codex-timeout-seconds", type=float, default=300)
    parser.add_argument("--evidence-dir", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    for name in ("repository", "binary", "baseline", "runtime_dir", "codex_binary", "codex_auth_file", "evidence_dir", "output"):
        setattr(args, name, getattr(args, name).resolve())
    for path, label in ((args.binary, "release binary"), (args.baseline, "baseline"), (args.codex_binary, "Codex binary"), (args.codex_auth_file, "Codex auth file")):
        require(path.is_file(), f"{label} does not exist: {path}")
    require(args.runtime_dir.is_dir(), f"accepted runtime does not exist: {args.runtime_dir}")
    require(not args.evidence_dir.exists(), "fixed-regression evidence directory must be new")
    args.evidence_dir.mkdir(parents=True)
    head = subprocess.run(["git", "rev-parse", "HEAD"], cwd=args.repository, check=True, capture_output=True, text=True, encoding="utf-8").stdout.strip()
    status = subprocess.run(["git", "status", "--porcelain"], cwd=args.repository, check=True, capture_output=True, text=True, encoding="utf-8").stdout
    require(head == args.candidate and not status.strip(), "fixed regression requires the clean frozen candidate")
    version = subprocess.run([str(args.binary), "version"], check=True, capture_output=True, text=True, encoding="utf-8").stdout.strip()
    require(version == args.candidate, "release binary version differs from the candidate")
    fixtures = load_fixtures(args.baseline)
    mapping, durations, semantic_evidence = create_and_organize(args, fixtures)
    full_data = args.evidence_dir / "full-data"
    baseline_data = args.evidence_dir / "baseline-data"
    shutil.copytree(full_data, baseline_data)
    full_environment = product_environment(disable_relations=False)
    baseline_environment = product_environment(disable_relations=True)
    with OwnwardRuntime(args.binary, full_data, args.runtime_dir, full_environment, operation_seconds=45) as full, OwnwardRuntime(args.binary, baseline_data, args.runtime_dir, baseline_environment, operation_seconds=45) as baseline:
        assets = verify_assets(full, fixtures, mapping)
        organization = organization_metrics(full, fixtures, mapping)
        retrieval, query_evidence = retrieval_metrics(full, baseline, fixtures, mapping)
    limits = fixtures["thresholds"]
    organization["passed"] = organization["precision"] >= limits["organization"]["relation_precision_min"] and organization["recall"] >= limits["organization"]["relation_recall_min"]
    ingestion_p95 = percentile(durations, 0.95)
    checks = {
        "asset_fidelity": assets["semantic_retention"] >= limits["organization"]["explicit_semantic_retention_min"] and assets["stable_identity_and_revision"] == 1,
        "organization": organization["passed"],
        "organization_latency": ingestion_p95 <= limits["ingestion"]["organization_complete_p95_seconds_max"],
        "retrieval": retrieval["passed"],
    }
    query_path = args.evidence_dir / "queries.json"
    write_json(query_path, query_evidence)
    report = {
        "schema": "ownward.fixed-regression-report/v1", "candidate": args.candidate,
        "release_binary_version": args.candidate,
        "release_binary_sha256": sha256(args.binary), "baseline": fixtures["descriptor"]["schema"],
        "baseline_sha256": sha256(args.baseline),
        "data_sha256": {
            name: sha256(resolve(args.baseline.parent, str(fixtures["descriptor"][field])))
            for name, field in {
                "thresholds": "thresholds", "information": "information", "kind_gold": "kind_gold",
                "relation_gold": "relation_gold", "queries": "queries", "updates": "updates",
            }.items()
        },
        "semantic_capability": {"model": args.codex_model, "reasoning_effort": args.codex_reasoning_effort},
        "assets": assets, "organization": organization, "organization_completion_p95_seconds": ingestion_p95,
        "retrieval": retrieval, "checks": checks,
        "evidence": {
            **{name: {"path": str(path), "sha256": sha256(path)} for name, path in semantic_evidence.items()},
            "mapping": {"path": str(args.evidence_dir / "mapping.json"), "sha256": sha256(args.evidence_dir / "mapping.json")},
            "queries": {"path": str(query_path), "sha256": sha256(query_path)},
        },
        "passed": all(checks.values()),
    }
    write_json(args.output, report)
    print(json.dumps({"passed": report["passed"], "report": str(args.output)}, ensure_ascii=False))
    if not report["passed"]:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
