from __future__ import annotations

import json
import math
import platform
from pathlib import Path
import statistics
import time
from typing import Any, Iterable

import kernel_iteration_evidence as evidence
import kernel_iteration_validation as validation


CONTRACT = "iteration/v2/stage4-semantic-model-screening-contract.json"
RESULT_SCHEMA = "ownward.kernel-iteration-stage4-semantic-model-screening-result/v1"


def run(repository: Path, model_root: Path, output: Path, formal_state: Path) -> dict[str, Any]:
    repository, model_root = repository.resolve(), model_root.resolve()
    output, formal_state = output.resolve(), formal_state.resolve()
    _require(output.is_relative_to(repository / ".tmp"), "语义表示筛选只能写入非正式 .tmp 边界")
    _require(not output.exists(), "语义表示筛选结果已存在；禁止覆盖或选择性重跑")
    contract_path = Path(__file__).resolve().parent / CONTRACT
    contract = _read_json(contract_path)
    _validate_contract(repository, contract)
    state_before = evidence.file_sha256(formal_state)
    _require(state_before == contract["formal_state_sha256"], "筛选前正式 state 漂移")

    candidates: list[dict[str, Any]] = []
    passing: list[str] = []
    for key in contract["order"]:
        spec = next(item for item in contract["candidates"] if item["key"] == key)
        result = _screen_candidate(repository, model_root / key, spec, contract)
        candidates.append(result)
        if result["status"] == "first-layer-pass":
            passing.append(key)

    state_after = evidence.file_sha256(formal_state)
    _require(state_after == state_before, "语义表示筛选改写了正式 state")
    _require(len(passing) <= 1, "筛选合同要求只有一个胜者才能进入独立向量世代")
    content: dict[str, Any] = {
        "schema": RESULT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "contract_sha256": evidence.file_sha256(contract_path),
        "controller_sha256": evidence.file_sha256(Path(__file__).resolve()),
        "machine": {"platform": platform.platform()},
        "candidates": candidates,
        "first_layer_winners": passing,
        "v2_vector_generation_created": False,
        "retrieval_latency_status": "open",
        "next_validation": (
            "build-one-isolated-v2-vector-generation-and-run-affected-end-to-end-protections"
            if passing
            else contract["next_if_all_rejected"]
        ),
        "formal_state_sha256_before": state_before,
        "formal_state_sha256_after": state_after,
    }
    value = {**content, "identity": evidence.canonical_sha256(content)}
    evidence.atomic_json(output, value)
    return {**value, "path": str(output)}


def _screen_candidate(
    repository: Path,
    model_dir: Path,
    spec: dict[str, Any],
    contract: dict[str, Any],
) -> dict[str, Any]:
    receipt = _read_json(model_dir / "artifact-receipt.json")
    _validate_receipt(model_dir, spec, receipt)
    embedder = _OnnxEmbedder(model_dir, spec, contract["runtime"])
    queries = _read_jsonl(repository / contract["inputs"]["formal_dataset"] / "queries.jsonl")
    by_id = {item["id"]: item for item in queries}
    latency_texts = [str(by_id[item]["text"]) for item in contract["runtime"]["latency_query_ids"]]
    for index in range(int(contract["runtime"]["warmup_queries"])):
        embedder.encode([latency_texts[index % len(latency_texts)]], "query")
    samples: list[float] = []
    count = int(contract["runtime"]["measured_queries"])
    for index in range(count):
        started = time.perf_counter_ns()
        embedder.encode([latency_texts[index % len(latency_texts)]], "query")
        samples.append((time.perf_counter_ns() - started) / 1_000_000)
    latency = _distribution(samples)
    latency_pass = (
        latency["mean"] <= contract["gates"]["isolated_query_mean_ms_maximum"]
        and latency["p95"] <= contract["gates"]["isolated_query_p95_ms_maximum"]
    )
    common: dict[str, Any] = {
        "key": spec["key"],
        "repository": spec["repository"],
        "revision": spec["revision"],
        "license": spec["license"],
        "artifact_identity": receipt["identity"],
        "runtime": embedder.runtime_identity,
        "vector_space": {
            "dimension": spec["dimension"],
            "max_length": spec["max_length"],
            "query_prefix": spec["query_prefix"],
            "document_prefix": spec["document_prefix"],
            "pooling": spec["pooling"],
            "normalization": spec["normalization"],
            "truncation": spec["truncation"],
        },
        "isolated_query_ms": latency,
        "isolated_query_gate_passed": latency_pass,
    }
    if not latency_pass:
        return {
            **common,
            "status": "rejected-isolated-latency",
            "quality_executed": False,
            "early_stop_honored": True,
        }

    formal = _evaluate_dataset(
        embedder, repository / contract["inputs"]["formal_dataset"], supplement=False,
    )
    supplement = _evaluate_dataset(
        embedder, repository / contract["inputs"]["supplement_dataset"], supplement=True,
    )
    reference = _read_json(repository / contract["reference"]["quality_result"])["metrics"]
    supplement_reference = _read_json(repository / contract["reference"]["quality_supplement"])["models"]["embeddinggemma_300m"]["overall"]
    quality_checks = _quality_checks(formal, supplement, reference, supplement_reference, contract["gates"])
    quality_pass = all(item["passed"] for item in quality_checks)
    return {
        **common,
        "quality_executed": True,
        "formal_quality": formal,
        "supplement_quality": supplement,
        "quality_checks": quality_checks,
        "quality_gate_passed": quality_pass,
        "status": "first-layer-pass" if quality_pass else "rejected-breadth-quality",
        "early_stop_honored": True,
    }


class _OnnxEmbedder:
    def __init__(self, model_dir: Path, spec: dict[str, Any], runtime: dict[str, Any]) -> None:
        try:
            import numpy as np
            import onnxruntime as ort
            import tokenizers
            import transformers
            from transformers import AutoTokenizer
        except ImportError as error:
            raise validation.KernelIterationValidationError(f"缺少冻结筛选运行依赖: {error}") from error
        self.np, self.ort = np, ort
        self.spec = spec
        self.tokenizer = AutoTokenizer.from_pretrained(str(model_dir), local_files_only=True)
        options = ort.SessionOptions()
        options.intra_op_num_threads = int(runtime["intra_op_threads"])
        options.inter_op_num_threads = int(runtime["inter_op_threads"])
        options.execution_mode = ort.ExecutionMode.ORT_SEQUENTIAL
        options.graph_optimization_level = ort.GraphOptimizationLevel.ORT_ENABLE_ALL
        weight = model_dir / str(spec["weight"])
        self.session = ort.InferenceSession(str(weight), providers=["CPUExecutionProvider"], sess_options=options)
        self.inputs = {item.name for item in self.session.get_inputs()}
        outputs = {item.name for item in self.session.get_outputs()}
        _require("last_hidden_state" in outputs, f"{spec['key']} 缺少 last_hidden_state")
        self.runtime_identity = {
            "onnxruntime_version": ort.__version__,
            "numpy_version": np.__version__,
            "transformers_version": transformers.__version__,
            "tokenizers_version": tokenizers.__version__,
            "providers": self.session.get_providers(),
            "intra_op_threads": options.intra_op_num_threads,
            "inter_op_threads": options.inter_op_num_threads,
            "execution_mode": "sequential",
            "graph_optimization": "all",
        }

    def encode(self, texts: list[str], kind: str, batch_size: int = 32):
        np = self.np
        prefix = self.spec[f"{kind}_prefix"]
        chunks = []
        for start in range(0, len(texts), batch_size):
            formatted = [prefix + item for item in texts[start : start + batch_size]]
            tokens = self.tokenizer(
                formatted, padding=True, truncation=True, max_length=int(self.spec["max_length"]), return_tensors="np",
            )
            feeds = {name: np.asarray(tokens[name], dtype=np.int64) for name in self.inputs if name in tokens}
            if "token_type_ids" in self.inputs and "token_type_ids" not in feeds:
                feeds["token_type_ids"] = np.zeros_like(feeds["input_ids"], dtype=np.int64)
            hidden = np.asarray(self.session.run(["last_hidden_state"], feeds)[0], dtype=np.float32)
            mask = np.asarray(tokens["attention_mask"], dtype=np.float32)[..., None]
            pooled = (hidden * mask).sum(axis=1) / np.maximum(mask.sum(axis=1), 1e-9)
            norms = np.linalg.norm(pooled, axis=1, keepdims=True)
            _require(np.isfinite(pooled).all() and np.all(norms > 0), f"{self.spec['key']} 产生无效向量")
            chunks.append(np.ascontiguousarray(pooled / norms, dtype=np.float32))
        return np.concatenate(chunks, axis=0)


def _evaluate_dataset(embedder: _OnnxEmbedder, dataset: Path, supplement: bool) -> dict[str, Any]:
    np = embedder.np
    corpus = _read_jsonl(dataset / "corpus.jsonl")
    queries = _read_jsonl(dataset / "queries.jsonl")
    qrels = _read_jsonl(dataset / "qrels.jsonl")
    documents = embedder.encode([str(item["text"]) for item in corpus], "document")
    query_vectors = embedder.encode([str(item["text"]) for item in queries], "query")
    scores = query_vectors @ documents.T
    document_ids = [str(item["id"]) for item in corpus]
    relevant = {str(item["query_id"]): str(item["document_id"]) for item in qrels}
    rows: list[dict[str, Any]] = []
    for index, query in enumerate(queries):
        order = np.argsort(-scores[index], kind="stable")[:10]
        ranked = [document_ids[int(item)] for item in order]
        target = relevant[str(query["id"])]
        rank = ranked.index(target) + 1 if target in ranked else None
        rows.append({"category": str(query["category"]), "rank": rank})

    def aggregate(items: Iterable[dict[str, Any]]) -> dict[str, Any]:
        selected = list(items)
        ranks = [item["rank"] for item in selected]
        if supplement:
            return {
                "count": len(selected),
                "top1_accuracy": sum(rank == 1 for rank in ranks) / len(selected),
                "mrr_at_10": sum(0.0 if rank is None else 1.0 / rank for rank in ranks) / len(selected),
                "recall_at_5": sum(rank is not None and rank <= 5 for rank in ranks) / len(selected),
                "recall_at_10": sum(rank is not None for rank in ranks) / len(selected),
            }
        return {
            "count": len(selected),
            "recall_at_10": sum(rank is not None for rank in ranks) / len(selected),
            "ndcg_at_10": sum(0.0 if rank is None else 1.0 / math.log2(rank + 1) for rank in ranks) / len(selected),
        }

    return {
        "overall": aggregate(rows),
        "by_category": {
            category: aggregate(item for item in rows if item["category"] == category)
            for category in sorted({item["category"] for item in rows})
        },
    }


def _quality_checks(
    formal: dict[str, Any],
    supplement: dict[str, Any],
    reference: dict[str, Any],
    supplement_reference: dict[str, Any],
    gates: dict[str, Any],
) -> list[dict[str, Any]]:
    checks = [
        _check("formal-overall-recall", formal["overall"]["recall_at_10"], gates["formal_overall_recall_at_10_minimum"]),
        _check("formal-overall-ndcg", formal["overall"]["ndcg_at_10"], reference["overall"]["ndcg_at_10"] - gates["formal_overall_ndcg_reference_tolerance"]),
        _check("supplement-recall-at-5", supplement["overall"]["recall_at_5"], gates["supplement_recall_at_5_minimum"]),
        _check("supplement-mrr-at-10", supplement["overall"]["mrr_at_10"], supplement_reference["mrr_at_10"] - gates["supplement_mrr_at_10_reference_tolerance"]),
        _check("supplement-top1", supplement["overall"]["top1_accuracy"], supplement_reference["top1_accuracy"] - gates["supplement_top1_reference_tolerance"]),
    ]
    for category, metrics in formal["by_category"].items():
        checks.append(_check(f"formal-{category}-recall", metrics["recall_at_10"], gates["formal_each_category_recall_at_10_minimum"]))
        checks.append(_check(f"formal-{category}-ndcg", metrics["ndcg_at_10"], reference["by_category"][category]["ndcg_at_10"] - gates["formal_each_category_ndcg_reference_tolerance"]))
    return checks


def _check(name: str, value: float, minimum: float) -> dict[str, Any]:
    return {"name": name, "value": value, "minimum": minimum, "passed": value >= minimum}


def _validate_contract(repository: Path, contract: dict[str, Any]) -> None:
    _require(contract["schema"] == "ownward.kernel-iteration-stage4-semantic-model-screening-contract/v1", "筛选合同 schema 错误")
    _require(contract["frozen_before_candidate_results"] and not contract["candidate_results_seen"], "筛选合同未在结果前冻结")
    _require(len(contract["candidates"]) == 2 and len(set(contract["order"])) == 2, "筛选候选必须恰为两个且顺序唯一")
    _require(contract["gates"]["isolated_query_mean_ms_maximum"] < contract["gates"]["full_retrieval_mean_ms_maximum"], "隔离 mean 未保留完整检索余量")
    _require(contract["gates"]["isolated_query_p95_ms_maximum"] < contract["gates"]["full_retrieval_p95_ms_maximum"], "隔离 p95 未保留完整检索余量")
    inputs = contract["inputs"]
    for label in ("formal", "supplement"):
        root = repository / inputs[f"{label}_dataset"]
        for filename in ("manifest", "queries", "corpus", "qrels"):
            suffix = ".json" if filename == "manifest" else ".jsonl"
            actual = evidence.file_sha256(root / f"{filename}{suffix}")
            _require(actual == inputs[f"{label}_{filename}_sha256"], f"{label} {filename} 冻结输入漂移")


def _validate_receipt(model_dir: Path, spec: dict[str, Any], receipt: dict[str, Any]) -> None:
    _require(receipt["repository"] == spec["repository"] and receipt["revision"] == spec["revision"], "模型仓库或修订错绑")
    _require(receipt["license"] == spec["license"], "模型许可错绑")
    files = receipt["files"]
    _require(str(spec["weight"]) in files, "模型收据缺少权重")
    for name, expected in files.items():
        _require(evidence.file_sha256(model_dir / name) == expected, f"模型制品摘要漂移: {name}")
    content = {key: value for key, value in receipt.items() if key != "identity"}
    _require(receipt["identity"] == evidence.canonical_sha256(content), "模型制品收据身份漂移")


def _distribution(values: list[float]) -> dict[str, Any]:
    ordered = sorted(values)
    index = max(0, math.ceil(0.95 * len(ordered)) - 1)
    return {
        "samples": len(values),
        "mean": statistics.fmean(values),
        "p95": ordered[index],
        "max": ordered[-1],
    }


def _read_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取 JSON {path}: {error}") from error
    _require(isinstance(value, dict), f"JSON 不是对象: {path}")
    return value


def _read_jsonl(path: Path) -> list[dict[str, Any]]:
    try:
        return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line.strip()]
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取 JSONL {path}: {error}") from error


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
