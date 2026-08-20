from __future__ import annotations

import argparse
import hashlib
import json
import math
import os
import platform
import time
from collections import defaultdict
from pathlib import Path
from typing import Iterable, Sequence

import numpy as np
import psutil

from onnx_embedder import OnnxTextEmbedder


def _parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", type=Path, required=True)
    parser.add_argument("--model-key", required=True)
    parser.add_argument("--variant", choices=("reference", "deliverable"), required=True)
    parser.add_argument("--model-dir", type=Path, required=True)
    parser.add_argument("--dataset", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--track", choices=("formal", "reference_validation"), required=True)
    return parser.parse_args()


def _read_jsonl(path: Path) -> list[dict[str, object]]:
    with path.open("r", encoding="utf-8") as handle:
        return [json.loads(line) for line in handle if line.strip()]


def _write_json(path: Path, value: object) -> None:
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(
        json.dumps(value, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
        newline="\n",
    )
    temporary.replace(path)


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(8 * 1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _set_affinity(config: dict[str, object]) -> list[int]:
    requested = [int(item) for item in config["execution"]["logical_processor_affinity"]]
    available = psutil.Process().cpu_affinity()
    selected = [item for item in requested if item in available]
    if len(selected) != len(requested):
        raise RuntimeError(f"无法应用冻结的处理器亲和性：requested={requested} available={available}")
    psutil.Process().cpu_affinity(selected)
    return selected


def _select_track(
    corpus: list[dict[str, object]],
    queries: list[dict[str, object]],
    qrels: list[dict[str, object]],
    track: str,
) -> tuple[list[dict[str, object]], list[dict[str, object]], list[dict[str, object]]]:
    if track == "formal":
        return corpus, queries, qrels
    selected: list[dict[str, object]] = []
    by_category: dict[str, int] = defaultdict(int)
    for query in sorted(queries, key=lambda item: str(item["id"])):
        category = str(query["category"])
        if by_category[category] < 125:
            selected.append(query)
            by_category[category] += 1
    if len(selected) != 1000 or set(by_category.values()) != {125}:
        raise RuntimeError(f"参考验证查询抽样异常：{dict(by_category)}")
    selected_query_ids = {str(item["id"]) for item in selected}
    selected_world_ids = {"w-" + item.removeprefix("q-") for item in selected_query_ids}
    selected_corpus = [item for item in corpus if str(item["world_id"]) in selected_world_ids]
    selected_qrels = [item for item in qrels if str(item["query_id"]) in selected_query_ids]
    if len(selected_corpus) != 20000 or len(selected_qrels) != 1000:
        raise RuntimeError("参考验证语料抽样不完整")
    return selected_corpus, selected, selected_qrels


def _distribution(values: Sequence[int | float]) -> dict[str, float | int]:
    array = np.asarray(values)
    return {
        "count": int(array.size),
        "min": float(array.min()),
        "p50": float(np.percentile(array, 50)),
        "p95": float(np.percentile(array, 95)),
        "p99": float(np.percentile(array, 99)),
        "max": float(array.max()),
        "mean": float(array.mean()),
    }


def _token_distribution(
    embedder: OnnxTextEmbedder,
    texts: Sequence[str],
    prompt_type: str,
) -> dict[str, object]:
    lengths: list[int] = []
    for start in range(0, len(texts), 256):
        lengths.extend(embedder.token_lengths(texts[start : start + 256], prompt_type, truncated=False))
    return {
        **_distribution(lengths),
        "truncated_count": sum(item > embedder.max_length for item in lengths),
        "max_length": embedder.max_length,
    }


def _embed_corpus(
    embedder: OnnxTextEmbedder,
    texts: Sequence[str],
    output: Path,
) -> tuple[np.memmap, dict[str, object]]:
    data_path = output / "corpus.f32"
    state_path = output / "corpus-progress.json"
    count = len(texts)
    expected_bytes = count * embedder.dimension * 4
    if state_path.is_file():
        state = json.loads(state_path.read_text(encoding="utf-8"))
        complete = int(state["complete"])
        if int(state["count"]) != count or int(state["dimension"]) != embedder.dimension:
            raise RuntimeError("语料向量检查点与当前运行不一致")
        if not data_path.is_file() or data_path.stat().st_size != expected_bytes:
            raise RuntimeError("语料向量检查点文件无效")
        vectors = np.memmap(data_path, mode="r+", dtype=np.float32, shape=(count, embedder.dimension))
    else:
        complete = 0
        vectors = np.memmap(data_path, mode="w+", dtype=np.float32, shape=(count, embedder.dimension))
        _write_json(state_path, {"complete": 0, "count": count, "dimension": embedder.dimension})
    started = time.perf_counter()
    if complete < count:
        for batch in embedder.encode_iter(texts[complete:], "document"):
            vectors[complete : complete + len(batch)] = batch
            complete += len(batch)
            if complete % (embedder.batch_size * 50) == 0 or complete == count:
                vectors.flush()
                _write_json(
                    state_path,
                    {"complete": complete, "count": count, "dimension": embedder.dimension},
                )
    elapsed = time.perf_counter() - started
    vectors.flush()
    return vectors, {
        "count": count,
        "dimension": embedder.dimension,
        "bytes": expected_bytes,
        "seconds_this_run": elapsed,
        "complete": complete,
    }


def _embed_queries(
    embedder: OnnxTextEmbedder,
    texts: Sequence[str],
    output: Path,
) -> tuple[np.ndarray, dict[str, object]]:
    path = output / "queries.npy"
    if path.is_file():
        vectors = np.load(path)
        if vectors.shape != (len(texts), embedder.dimension):
            raise RuntimeError("查询向量检查点与当前运行不一致")
        return vectors, {"count": len(texts), "dimension": embedder.dimension, "seconds_this_run": 0.0}
    started = time.perf_counter()
    vectors = embedder.encode(texts, "query")
    elapsed = time.perf_counter() - started
    np.save(path, vectors, allow_pickle=False)
    return vectors, {"count": len(texts), "dimension": embedder.dimension, "seconds_this_run": elapsed}


def _load_valid_rankings(path: Path) -> list[dict[str, object]]:
    if not path.is_file():
        return []
    valid = []
    invalid_tail = False
    with path.open("r", encoding="utf-8") as handle:
        for line in handle:
            try:
                valid.append(json.loads(line))
            except json.JSONDecodeError:
                invalid_tail = True
                break
    if invalid_tail:
        with path.open("w", encoding="utf-8", newline="\n") as handle:
            for item in valid:
                handle.write(json.dumps(item, ensure_ascii=False, sort_keys=True) + "\n")
    return valid


def _rank(
    corpus: np.ndarray,
    queries: np.ndarray,
    document_ids: Sequence[str],
    query_ids: Sequence[str],
    output: Path,
) -> tuple[list[dict[str, object]], float]:
    path = output / "rankings.jsonl"
    rankings = _load_valid_rankings(path)
    complete = len(rankings)
    if complete and [str(item["query_id"]) for item in rankings] != list(query_ids[:complete]):
        raise RuntimeError("排名检查点与查询顺序不一致")
    started = time.perf_counter()
    with path.open("a", encoding="utf-8", newline="\n") as handle:
        for start in range(complete, len(query_ids), 32):
            query_batch = np.ascontiguousarray(queries[start : start + 32])
            scores = query_batch @ corpus.T
            top_indices = np.argpartition(scores, -10, axis=1)[:, -10:]
            top_scores = np.take_along_axis(scores, top_indices, axis=1)
            order = np.argsort(-top_scores, axis=1)
            top_indices = np.take_along_axis(top_indices, order, axis=1)
            top_scores = np.take_along_axis(top_scores, order, axis=1)
            for offset in range(len(query_batch)):
                record = {
                    "query_id": query_ids[start + offset],
                    "document_ids": [document_ids[int(index)] for index in top_indices[offset]],
                    "scores": [float(value) for value in top_scores[offset]],
                }
                rankings.append(record)
                handle.write(json.dumps(record, ensure_ascii=False, sort_keys=True) + "\n")
            handle.flush()
    return rankings, time.perf_counter() - started


def _metrics(
    rankings: Sequence[dict[str, object]],
    queries: Sequence[dict[str, object]],
    qrels: Sequence[dict[str, object]],
) -> tuple[dict[str, object], list[dict[str, object]]]:
    relevant = {str(item["query_id"]): str(item["document_id"]) for item in qrels}
    query_meta = {str(item["id"]): item for item in queries}
    per_query: list[dict[str, object]] = []
    for ranking in rankings:
        query_id = str(ranking["query_id"])
        ids = [str(item) for item in ranking["document_ids"]]
        target = relevant[query_id]
        rank = ids.index(target) + 1 if target in ids else None
        per_query.append(
            {
                "query_id": query_id,
                "category": query_meta[query_id]["category"],
                "scope": query_meta[query_id]["scope"],
                "relevant_document_id": target,
                "rank": rank,
                "recall_at_10": 1.0 if rank is not None else 0.0,
                "ndcg_at_10": 1.0 / math.log2(rank + 1) if rank is not None else 0.0,
            }
        )

    def aggregate(items: Iterable[dict[str, object]]) -> dict[str, object]:
        selected = list(items)
        return {
            "count": len(selected),
            "recall_at_10": float(np.mean([item["recall_at_10"] for item in selected])),
            "ndcg_at_10": float(np.mean([item["ndcg_at_10"] for item in selected])),
        }

    summary = {
        "overall": aggregate(per_query),
        "by_category": {
            category: aggregate(item for item in per_query if item["category"] == category)
            for category in sorted({str(item["category"]) for item in per_query})
        },
        "by_scope": {
            scope: aggregate(item for item in per_query if item["scope"] == scope)
            for scope in sorted({str(item["scope"]) for item in per_query})
        },
    }
    return summary, per_query


def main() -> None:
    args = _parse_args()
    config = json.loads(args.config.read_text(encoding="utf-8"))
    manifest_hash = _sha256(args.dataset / "manifest.json")
    if manifest_hash != config["ownward_track"]["dataset_manifest_sha256"]:
        raise RuntimeError(
            f"数据清单与冻结配置不一致：{manifest_hash} != {config['ownward_track']['dataset_manifest_sha256']}"
        )
    affinity = _set_affinity(config)
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    corpus_all = _read_jsonl(args.dataset / "corpus.jsonl")
    queries_all = _read_jsonl(args.dataset / "queries.jsonl")
    qrels_all = _read_jsonl(args.dataset / "qrels.jsonl")
    corpus, queries, qrels = _select_track(corpus_all, queries_all, qrels_all, args.track)
    batch_size = int(
        config["execution"]["reference_batch_size"]
        if args.variant == "reference"
        else config["execution"]["quality_batch_size"]
    )
    embedder = OnnxTextEmbedder(
        args.config,
        args.model_key,
        args.variant,
        args.model_dir,
        batch_size,
    )
    corpus_texts = [str(item["text"]) for item in corpus]
    query_texts = [str(item["text"]) for item in queries]
    corpus_vectors, corpus_timing = _embed_corpus(embedder, corpus_texts, output)
    query_vectors, query_timing = _embed_queries(embedder, query_texts, output)
    rankings, ranking_seconds = _rank(
        corpus_vectors,
        query_vectors,
        [str(item["id"]) for item in corpus],
        [str(item["id"]) for item in queries],
        output,
    )
    metrics, per_query = _metrics(rankings, queries, qrels)
    with (output / "per-query.jsonl").open("w", encoding="utf-8", newline="\n") as handle:
        for item in per_query:
            handle.write(json.dumps(item, ensure_ascii=False, sort_keys=True) + "\n")

    variant_config = config["models"][args.model_key][args.variant]
    artifact_names = [str(variant_config["weight"])]
    if variant_config.get("external_data"):
        artifact_names.append(str(variant_config["external_data"]))
    model_artifacts = []
    for name in artifact_names:
        artifact = args.model_dir / name
        model_artifacts.append({"path": name, "bytes": artifact.stat().st_size, "sha256": _sha256(artifact)})
    summary = {
        "freeze_id": config["freeze_id"],
        "model_key": args.model_key,
        "variant": args.variant,
        "track": args.track,
        "model_artifacts": model_artifacts,
        "dataset_manifest_sha256": config["ownward_track"]["dataset_manifest_sha256"],
        "frozen_config_sha256": _sha256(args.config),
        "runtime": {
            "python": platform.python_version(),
            "platform": platform.platform(),
            "pid": os.getpid(),
            "affinity": affinity,
            "batch_size": batch_size,
        },
        "token_lengths": {
            "corpus": _token_distribution(embedder, corpus_texts, "document"),
            "queries": _token_distribution(embedder, query_texts, "query"),
        },
        "timing": {
            "corpus": corpus_timing,
            "queries": query_timing,
            "ranking_seconds_this_run": ranking_seconds,
        },
        "metrics": metrics,
    }
    _write_json(output / "summary.json", summary)
    print(json.dumps(summary, ensure_ascii=False, indent=2, sort_keys=True))


if __name__ == "__main__":
    main()
