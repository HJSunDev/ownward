from __future__ import annotations

import json
import os
from pathlib import Path
from typing import Any

import kernel_iteration_evidence as evidence
import kernel_iteration_validation as validation
import kernel_iteration_longmemeval as longmemeval


MATERIALS_SCHEMA = "ownward.kernel-iteration-stage4-retrieval-latency-materials/v1"
RECEIPT_SCHEMA = "ownward.kernel-iteration-stage4-retrieval-latency-preparation/v2"


def load_materials(suite_root: Path) -> dict[str, Any]:
    path = suite_root.resolve() / "iteration" / "v2" / "stage4-retrieval-latency-materials.json"
    value = _load_json(path)
    _require(value.get("schema") == MATERIALS_SCHEMA, "检索时延材料 schema 无效")
    _require(value.get("contains_formal_questions_answers_gold_content_outputs_or_case_ids") is False, "检索时延材料接触正式内容")
    _require(value.get("performance_only") is True and value.get("quality_material_overlap") is False, "检索时延材料职责没有隔离")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), "检索时延材料身份漂移")
    generation = _mapping(value, "generation")
    _require(generation.get("content_format") == "headline-newline-repeated-filler/v1", "检索时延内容生成规则漂移")
    _require(generation.get("sources_per_case") == 8, "检索时延来源规模漂移")
    cases = value.get("cases")
    _require(isinstance(cases, list) and len(cases) == 4, "检索时延材料必须包含四个独立案例")
    identifiers: set[str] = set()
    for item in cases:
        _require(isinstance(item, dict), "检索时延案例无效")
        case_id = str(item.get("case_id", ""))
        _require(case_id and case_id not in identifiers, "检索时延案例身份重复")
        identifiers.add(case_id)
        repeats, headlines = item.get("repeat"), item.get("headlines")
        _require(
            isinstance(repeats, list) and isinstance(headlines, list)
            and len(repeats) == len(headlines) == 8
            and all(isinstance(repeat, int) and repeat > 0 for repeat in repeats),
            f"检索时延案例来源无效: {case_id}",
        )
        _require(all(isinstance(value, str) and value.strip() for value in headlines), f"检索时延案例标题无效: {case_id}")
        for field in ("query", "shared_phrase", "filler"):
            _require(isinstance(item.get(field), str) and item[field].strip(), f"检索时延案例缺少 {field}: {case_id}")
    return value


def expanded_sources(materials: dict[str, Any], case: dict[str, Any]) -> list[dict[str, str]]:
    generation = _mapping(materials, "generation")
    result: list[dict[str, str]] = []
    for index, (headline, repeat) in enumerate(zip(case["headlines"], case["repeat"]), start=1):
        source_id = f"{case['case_id']}-s{index:02d}"
        content = f"{case['shared_phrase']}. {headline}\n" + str(case["filler"]) * int(repeat)
        result.append({"source_id": source_id, "headline": str(headline), "content": content, "actor": str(generation["source_actor"])})
    return result


def prepare(
    suite_root: Path,
    output_root: Path,
    execution_config_path: Path,
    v0_binary: Path,
    v0_embedding: Path,
    persistent_root: Path,
    formal_state: Path,
    *,
    resume: bool = False,
) -> dict[str, Any]:
    suite_root, output_root = suite_root.resolve(), output_root.resolve()
    repository = suite_root.parents[2]
    _require(output_root.is_relative_to(repository / ".tmp"), "检索时延准备收据只能写入非正式 .tmp 边界")
    materials = load_materials(suite_root)
    runtime = validation.validate_execution_config(suite_root, execution_config_path.resolve())
    v0_binary, v0_embedding = v0_binary.resolve(), v0_embedding.resolve()
    _require(v0_binary.is_file(), "检索时延 V0 二进制不存在")
    _require((v0_embedding / "manifest.json").is_file(), "检索时延 V0 向量制品不存在")
    state_path = formal_state.resolve()
    state_before = evidence.file_sha256(state_path)
    persistent_root = persistent_root.resolve()
    _require("kernel-iteration" in persistent_root.parts and "retrieval-latency" in persistent_root.parts, "检索时延 prepared data 必须位于隔离非正式边界")
    receipt_path = output_root / "preparation.json"
    subject_roots = {name: persistent_root / name for name in ("v0", "current-v2")}
    if receipt_path.is_file():
        _require(resume, "检索时延 prepared data 已存在；禁止覆盖")
        receipt = _load_json(receipt_path)
        _validate_receipt(receipt, materials, runtime, v0_binary, v0_embedding, state_path, subject_roots)
        return {**receipt, "path": str(receipt_path), "reused": True}
    _require(not receipt_path.exists(), "检索时延准备收据不是普通文件")
    _require(not any(root.exists() for root in subject_roots.values()), "检索时延 prepared data 现场已存在但缺少完整收据")

    v0_runtime = {**runtime, "binary": v0_binary, "embedding": v0_embedding}
    for name, subject_runtime in (("current-v2", runtime), ("v0", v0_runtime)):
        for case in materials["cases"]:
            data_dir = subject_roots[name] / str(case["case_id"]) / "ownward-data"
            _prepare_case(subject_runtime, data_dir, materials, case)
    identities = {
        name: {str(case["case_id"]): _tree_sha256(root / str(case["case_id"]) / "ownward-data") for case in materials["cases"]}
        for name, root in subject_roots.items()
    }
    materialized_authority_identities = {
        name: {str(case["case_id"]): _materialized_authority_sha256(root / str(case["case_id"]) / "ownward-data") for case in materials["cases"]}
        for name, root in subject_roots.items()
    }
    _require(materialized_authority_identities["v0"] == materialized_authority_identities["current-v2"], "V0 与当前 V2 权威事实不同尺")
    state_after = evidence.file_sha256(state_path)
    _require(state_after == state_before, "检索时延 prepared data 准备改写了正式 state")
    content = {
        "schema": RECEIPT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "materials_identity": materials["identity"],
        "preparer_sha256": evidence.file_sha256(Path(__file__).resolve()),
        "binary_sha256": {
            "v0": evidence.file_sha256(v0_binary),
            "current-v2": evidence.file_sha256(runtime["binary"]),
        },
        "embedding_manifest_sha256": {
            "v0": evidence.file_sha256(v0_embedding / "manifest.json"),
            "current-v2": evidence.file_sha256(runtime["embedding"] / "manifest.json"),
        },
        "protocol_sha256": evidence.file_sha256(runtime["protocol"]),
        "subject_roots": {name: str(root) for name, root in subject_roots.items()},
        "prepared_data_sha256": identities,
        "materialized_authority_sha256": materialized_authority_identities,
        "formal_state_sha256_before": state_before,
        "formal_state_sha256_after": state_after,
        "codex_calls": 0,
        "answer_generation_calls": 0,
    }
    receipt = {**content, "identity": evidence.canonical_sha256(content)}
    evidence.atomic_json(receipt_path, receipt)
    return {**receipt, "path": str(receipt_path), "reused": False}


def _prepare_case(runtime: dict[str, Any], data_dir: Path, materials: dict[str, Any], case: dict[str, Any]) -> None:
    environment = os.environ.copy()
    environment["OWNWARD_EMBEDDING_BUNDLE_DIR"] = str(runtime["embedding"])
    data_dir.parent.mkdir(parents=True, exist_ok=True)
    sources = expanded_sources(materials, case)
    generation = _mapping(materials, "generation")
    with longmemeval.adapter.OwnwardRuntime(
        runtime["binary"], data_dir, environment, startup_seconds=60,
        # Preparation is outside the measured read-only query path.  V0 may
        # legitimately need longer than one query budget to embed an eight-
        # asset synthetic batch, so do not confuse preparation latency with
        # the frozen retrieval timeout used by the paired measurement.
        operation_seconds=max(180.0, float(runtime["protocol_value"]["retrieval"]["query_timeout_seconds"])),
    ) as service:
        created = service.client.call_tool("ownward_create_batch", {"items": [
            {
                "content": item["content"],
                "contexts": [dict(generation["context"])],
                "source": {"actor": item["actor"], "ref": item["source_id"]},
            }
            for item in sources
        ]})
        values = created.get("results") if isinstance(created, dict) else None
        _require(isinstance(values, list) and len(values) == len(sources), f"检索时延资产创建失败: {case['case_id']}")
        asset_ids: list[str] = []
        for value in values:
            mutation = value.get("result") if isinstance(value, dict) and not value.get("error") else None
            information = mutation.get("information") if isinstance(mutation, dict) else None
            _require(isinstance(information, dict) and isinstance(information.get("id"), str), f"检索时延资产创建结果无效: {case['case_id']}")
            asset_ids.append(str(information["id"]))
        _organize_assets(service, materials, case, asset_ids, sources)


def _organize_assets(service: Any, materials: dict[str, Any], case: dict[str, Any], asset_ids: list[str], sources: list[dict[str, str]]) -> None:
        frozen = service.client.call_tool("ownward_semantic_work", {"asset_ids": asset_ids})
        works = frozen.get("work") if isinstance(frozen, dict) else None
        _require(isinstance(works, list) and len(works) == len(asset_ids), f"检索时延语义工作不完整: {case['case_id']}")
        source_by_asset = {asset_id: source for asset_id, source in zip(asset_ids, sources)}
        submissions = []
        for work in works:
            asset = _mapping(work, "asset")
            source = source_by_asset.get(str(asset.get("id", "")))
            _require(source is not None, f"检索时延语义工作资产错绑: {case['case_id']}")
            submissions.append({
                "schema": "ownward.semantic-submission/v1",
                "work_id": work["id"],
                "asset_id": asset["id"],
                "asset_revision": asset["revision"],
                "capability": {"id": "codex", "version": "gpt-5.6-luna", "execution": "longmemeval-s"},
                "status": "complete",
                "analysis": {
                    "summary": source["headline"],
                    "topics": [str(case["shared_phrase"])[:120]],
                    "cues": [],
                    "inferred_contexts": [],
                    "relations": [],
                },
            })
        submitted = service.client.call_tool("ownward_semantic_submit_batch", {"submissions": submissions})
        results = submitted.get("results") if isinstance(submitted, dict) else None
        _require(
            isinstance(results, list) and len(results) == len(submissions)
            and all(isinstance(item, dict) and not item.get("error") and _mapping(item, "organization").get("status") == "ready" for item in results),
            f"检索时延派生表示准备失败: {case['case_id']}",
        )


def _validate_receipt(
    receipt: dict[str, Any],
    materials: dict[str, Any],
    runtime: dict[str, Any],
    v0_binary: Path,
    v0_embedding: Path,
    state_path: Path,
    subject_roots: dict[str, Path],
) -> None:
    _require(receipt.get("schema") == RECEIPT_SCHEMA, "检索时延准备收据 schema 无效")
    content = {key: item for key, item in receipt.items() if key != "identity"}
    _require(receipt.get("identity") == evidence.canonical_sha256(content), "检索时延准备收据身份漂移")
    _require(receipt.get("materials_identity") == materials["identity"], "检索时延准备材料漂移")
    _require(receipt.get("preparer_sha256") == evidence.file_sha256(Path(__file__).resolve()), "检索时延准备器漂移")
    _require(receipt.get("binary_sha256") == {
        "v0": evidence.file_sha256(v0_binary), "current-v2": evidence.file_sha256(runtime["binary"]),
    }, "检索时延准备二进制漂移")
    _require(receipt.get("embedding_manifest_sha256") == {
        "v0": evidence.file_sha256(v0_embedding / "manifest.json"),
        "current-v2": evidence.file_sha256(runtime["embedding"] / "manifest.json"),
    }, "检索时延准备向量清单漂移")
    _require(receipt.get("protocol_sha256") == evidence.file_sha256(runtime["protocol"]), "检索时延准备协议漂移")
    expected = {
        name: {str(case["case_id"]): _tree_sha256(root / str(case["case_id"]) / "ownward-data") for case in materials["cases"]}
        for name, root in subject_roots.items()
    }
    _require(receipt.get("prepared_data_sha256") == expected, "检索时延 prepared data 漂移")
    materialized = {
        name: {str(case["case_id"]): _materialized_authority_sha256(root / str(case["case_id"]) / "ownward-data") for case in materials["cases"]}
        for name, root in subject_roots.items()
    }
    _require(materialized["v0"] == materialized["current-v2"] == receipt.get("materialized_authority_sha256", {}).get("v0") == receipt.get("materialized_authority_sha256", {}).get("current-v2"), "检索时延权威事实身份漂移")
    current_state = evidence.file_sha256(state_path)
    _require(receipt.get("formal_state_sha256_before") == receipt.get("formal_state_sha256_after") == current_state, "检索时延准备后正式 state 漂移")


def _tree_sha256(root: Path) -> str:
    _require(root.is_dir(), f"检索时延数据目录不存在: {root}")
    manifest = []
    for path in sorted(root.rglob("*")):
        if path.is_file() and not path.is_symlink() and path.name != ".ownward.lock":
            manifest.append({"path": path.relative_to(root).as_posix(), "size": path.stat().st_size, "sha256": evidence.file_sha256(path)})
    return evidence.canonical_sha256(manifest)


def _materialized_authority_sha256(root: Path) -> str:
    path = root / "assets" / "information.jsonl"
    _require(path.is_file(), f"检索时延权威日志不存在: {path}")
    values = []
    for line in path.read_text(encoding="utf-8").splitlines():
        item = json.loads(line)
        value = item.get("value") if isinstance(item, dict) else None
        _require(isinstance(value, dict), "检索时延权威日志条目无效")
        source = value.get("source")
        _require(isinstance(source, dict), "检索时延权威事实缺少来源")
        values.append({
            "kind": value.get("kind"), "content": value.get("content"), "contexts": value.get("contexts"),
            "source": {"actor": source.get("actor"), "ref": source.get("ref")}, "revision": value.get("revision"),
        })
    return evidence.canonical_sha256(values)


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取检索时延制品 {path}: {error}") from error
    _require(isinstance(value, dict), f"检索时延制品不是对象: {path}")
    return value


def _mapping(value: dict[str, Any], name: str) -> dict[str, Any]:
    result = value.get(name)
    _require(isinstance(result, dict), f"检索时延制品缺少对象: {name}")
    return result


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
