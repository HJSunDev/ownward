from __future__ import annotations

from dataclasses import dataclass
import hashlib
import json
from pathlib import Path
from typing import Any


MANIFEST_SCHEMA = "ownward.kernel-iteration-semantic-representation/v1"
DEFAULT_REPRESENTATION = "ownward.semantic-deduplicated-body-table/v1"
COMPACT_REPRESENTATION = "ownward.semantic-indexed-body-context-table/v2"


class SemanticRepresentationError(RuntimeError):
    pass


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise SemanticRepresentationError(message)


def canonical_sha256(value: Any) -> str:
    encoded = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def default_instruction() -> str:
    return (
        "Act only as Ownward's external semantic capability. Analyze every supplied semantic work item exactly once. "
        "The items came from Ownward's public semantic_work path; the host will validate and submit your result through "
        "the public semantic_submit path. No query, expected answer, answer-session label, question type, or evaluator "
        "signal is available. Preserve meaning, use only explicit content and candidate evidence, and do not invent "
        "relationships. Bodies are listed once and work items reference them by stable body_ref, id, and revision; "
        "candidate metadata contains every similarity, context, and relation field exposed by semantic_work. Return one "
        "analysis per work_id in the supplied order. Use one short sentence per summary, at most 4 short topics, and at "
        "most 4 cues only for durable answer-bearing facts, entities, preferences, events or decisions. Do not turn "
        "source IDs, conversation dates or acknowledgements into cues.\n\nSemantic input:\n"
    )


def compact_instruction() -> str:
    return (
        "Act only as Ownward's external semantic capability. Analyze every supplied semantic work item exactly once. "
        "The items came from Ownward's public semantic_work path; the host will validate and submit your result through "
        "the public semantic_submit path. No query, expected answer, answer-session label, question type, or evaluator "
        "signal is available. Preserve meaning, use only explicit content and candidate evidence, and do not invent "
        "relationships. The fields object names every array position; body and context rows are listed once, and work "
        "assets and candidates reference them by zero-based index while body rows retain stable id and revision. Candidate "
        "metadata preserves every similarity and relation field exposed by semantic_work. Return one analysis per work_id "
        "in supplied order. Use one short sentence per summary, at most 4 short topics, and at most 4 cues only for durable "
        "answer-bearing facts, entities, preferences, events or decisions. Do not turn source IDs, conversation dates or "
        "acknowledgements into cues.\n\nSemantic input:\n"
    )


def default_semantic_input(work: list[dict[str, Any]]) -> dict[str, Any]:
    bodies: list[dict[str, Any]] = []
    body_refs: dict[tuple[str, int, str], str] = {}

    def body_reference(value: dict[str, Any]) -> str:
        content = value.get("content")
        identifier = value.get("id")
        revision = value.get("revision")
        _require(isinstance(content, str) and isinstance(identifier, str) and isinstance(revision, int), "semantic body identity is invalid")
        digest = hashlib.sha256(content.encode("utf-8")).hexdigest()
        key = (identifier, revision, digest)
        if key not in body_refs:
            reference = f"body-{len(bodies):05d}-{digest[:16]}"
            body_refs[key] = reference
            bodies.append({"body_ref": reference, "id": identifier, "revision": revision, "content": content})
        return body_refs[key]

    items = []
    for item in work:
        asset = item.get("asset") if isinstance(item, dict) else None
        _require(isinstance(asset, dict), "semantic work asset is invalid")
        candidates = []
        for candidate in item.get("candidates", [])[:2]:
            if not isinstance(candidate, dict):
                continue
            metadata = {key: value for key, value in candidate.items() if key not in {"content", "id", "revision"}}
            candidates.append({
                "body_ref": body_reference(candidate),
                "id": candidate["id"],
                "revision": candidate["revision"],
                **metadata,
            })
        items.append({
            "work_id": item["id"],
            "asset": {
                "body_ref": body_reference(asset),
                "id": asset["id"],
                "revision": asset["revision"],
                "explicit_contexts": asset.get("contexts", []),
            },
            "candidates": candidates,
        })
    return {"representation": DEFAULT_REPRESENTATION, "bodies": bodies, "work": items}


def validate_default_input(work: list[dict[str, Any]], value: dict[str, Any]) -> dict[str, Any]:
    _require(value.get("representation") == DEFAULT_REPRESENTATION, "semantic input representation changed")
    bodies = value.get("bodies")
    items = value.get("work")
    _require(isinstance(bodies, list) and isinstance(items, list) and len(items) == len(work), "semantic input is incomplete")
    by_ref = {item.get("body_ref"): item for item in bodies if isinstance(item, dict)}
    _require(len(by_ref) == len(bodies) and None not in by_ref, "semantic bodies are duplicated")
    reconstructed = []
    for source, encoded in zip(work, items):
        _require(isinstance(encoded, dict) and encoded.get("work_id") == source.get("id"), "semantic work identity changed")
        asset = source.get("asset") if isinstance(source.get("asset"), dict) else {}
        encoded_asset = encoded.get("asset") if isinstance(encoded.get("asset"), dict) else {}
        body = by_ref.get(encoded_asset.get("body_ref"))
        _require(
            isinstance(body, dict)
            and body.get("id") == asset.get("id")
            and body.get("revision") == asset.get("revision")
            and body.get("content") == asset.get("content")
            and encoded_asset.get("explicit_contexts") == asset.get("contexts", []),
            "semantic asset content or context changed",
        )
        encoded_candidates = encoded.get("candidates") if isinstance(encoded.get("candidates"), list) else []
        source_candidates = [item for item in source.get("candidates", [])[:2] if isinstance(item, dict)]
        _require(len(encoded_candidates) == len(source_candidates), "semantic candidate count changed")
        for source_candidate, encoded_candidate in zip(source_candidates, encoded_candidates):
            candidate_body = by_ref.get(encoded_candidate.get("body_ref")) if isinstance(encoded_candidate, dict) else None
            _require(
                isinstance(candidate_body, dict)
                and candidate_body.get("id") == source_candidate.get("id")
                and candidate_body.get("revision") == source_candidate.get("revision")
                and candidate_body.get("content") == source_candidate.get("content"),
                "semantic candidate content changed",
            )
            metadata = {key: item for key, item in source_candidate.items() if key not in {"content", "id", "revision"}}
            _require(
                {key: item for key, item in encoded_candidate.items() if key not in {"body_ref", "id", "revision"}} == metadata,
                "semantic candidate metadata or relations changed",
            )
        reconstructed.append(str(source["id"]))
    return {
        "equivalent": True,
        "work_ids": reconstructed,
        "body_count": len(bodies),
        "body_identity_sha256": canonical_sha256([
            {"body_ref": item["body_ref"], "id": item["id"], "revision": item["revision"], "content_sha256": hashlib.sha256(item["content"].encode("utf-8")).hexdigest()}
            for item in bodies
        ]),
    }


def compact_semantic_input(original: dict[str, Any]) -> dict[str, Any]:
    _require(original.get("representation") == DEFAULT_REPRESENTATION, "compact semantic source representation changed")
    bodies = original.get("bodies")
    works = original.get("work")
    _require(isinstance(bodies, list) and isinstance(works, list), "compact semantic source is incomplete")
    body_index = {item["body_ref"]: index for index, item in enumerate(bodies)}
    _require(len(body_index) == len(bodies), "compact semantic body_ref is duplicated")
    contexts: list[list[dict[str, Any]]] = []
    context_index: dict[str, int] = {}

    def context_ref(value: Any) -> int:
        _require(isinstance(value, list), "compact semantic context is invalid")
        key = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
        if key not in context_index:
            context_index[key] = len(contexts)
            contexts.append(value)
        return context_index[key]

    compact_work = []
    for item in works:
        asset = item.get("asset")
        candidates = item.get("candidates")
        _require(isinstance(asset, dict) and isinstance(candidates, list), "compact semantic work is invalid")
        compact_candidates = []
        for candidate in candidates:
            _require(isinstance(candidate, dict), "compact semantic candidate is invalid")
            metadata = {key: value for key, value in candidate.items() if key not in {"body_ref", "id", "revision", "explicit_contexts"}}
            compact_candidates.append([body_index[candidate["body_ref"]], context_ref(candidate.get("explicit_contexts", [])), metadata])
        compact_work.append([item["work_id"], body_index[asset["body_ref"]], context_ref(asset.get("explicit_contexts", [])), compact_candidates])
    value = {
        "representation": COMPACT_REPRESENTATION,
        "fields": {
            "body": ["id", "revision", "content"],
            "work": ["work_id", "asset_body", "asset_context", "candidates"],
            "candidate": ["body", "context", "metadata"],
        },
        "contexts": contexts,
        "bodies": [[item["id"], item["revision"], item["content"]] for item in bodies],
        "work": compact_work,
    }
    validate_compact_equivalence(original, value)
    return value


def validate_compact_equivalence(original: dict[str, Any], compact: dict[str, Any]) -> None:
    _require(compact.get("representation") == COMPACT_REPRESENTATION, "compact semantic representation changed")
    _require(compact.get("fields") == {
        "body": ["id", "revision", "content"],
        "work": ["work_id", "asset_body", "asset_context", "candidates"],
        "candidate": ["body", "context", "metadata"],
    }, "compact semantic fields changed")
    original_bodies = original["bodies"]
    bodies = compact.get("bodies")
    contexts = compact.get("contexts")
    works = compact.get("work")
    _require(isinstance(bodies, list) and isinstance(contexts, list) and isinstance(works, list), "compact semantic tables are invalid")
    _require(bodies == [[item["id"], item["revision"], item["content"]] for item in original_bodies], "compact semantic body, identity, or revision changed")
    reconstructed = []
    for row in works:
        _require(isinstance(row, list) and len(row) == 4, "compact semantic work row is invalid")
        work_id, asset_index, asset_context, candidate_rows = row
        source_body = original_bodies[int(asset_index)]
        candidates = []
        for candidate_row in candidate_rows:
            _require(isinstance(candidate_row, list) and len(candidate_row) == 3, "compact semantic candidate row is invalid")
            candidate_index, candidate_context, metadata = candidate_row
            candidate_body = original_bodies[int(candidate_index)]
            candidates.append({
                "body_ref": candidate_body["body_ref"], "id": candidate_body["id"], "revision": candidate_body["revision"],
                "explicit_contexts": contexts[int(candidate_context)], **metadata,
            })
        reconstructed.append({
            "work_id": work_id,
            "asset": {"body_ref": source_body["body_ref"], "id": source_body["id"], "revision": source_body["revision"], "explicit_contexts": contexts[int(asset_context)]},
            "candidates": candidates,
        })
    _require(reconstructed == original["work"], "compact semantic work, context, candidate, or relation cannot be losslessly reconstructed")


def fact_equivalence_sha256(work: list[dict[str, Any]]) -> str:
    value = default_semantic_input(work)
    bodies = value["bodies"]
    ref_map = {body["body_ref"]: f"fact-{index:05d}" for index, body in enumerate(bodies)}
    id_map = {body["id"]: ref_map[body["body_ref"]] for body in bodies}

    def normalize(item: Any) -> Any:
        if isinstance(item, str):
            return id_map.get(item, item)
        if isinstance(item, list):
            return [normalize(child) for child in item]
        if isinstance(item, dict):
            return {
                key: normalize(ref_map.get(child, child) if key == "body_ref" else child)
                for key, child in sorted(item.items()) if key not in {"id", "work_id"}
            }
        return item

    normalized = {
        "bodies": [
            {"body_ref": ref_map[body["body_ref"]], "revision": body["revision"], "content_sha256": hashlib.sha256(body["content"].encode("utf-8")).hexdigest()}
            for body in bodies
        ],
        "work": [normalize(item) for item in value["work"]],
    }
    return canonical_sha256(normalized)


@dataclass(frozen=True)
class SemanticInputContract:
    representation: str
    manifest_identity: str
    manifest_path: str | None

    def instruction(self) -> str:
        return compact_instruction() if self.representation == COMPACT_REPRESENTATION else default_instruction()

    def encode(self, work: list[dict[str, Any]]) -> dict[str, Any]:
        original = default_semantic_input(work)
        return compact_semantic_input(original) if self.representation == COMPACT_REPRESENTATION else original

    def validate(self, work: list[dict[str, Any]], value: dict[str, Any]) -> dict[str, Any]:
        original = default_semantic_input(work)
        if self.representation == COMPACT_REPRESENTATION:
            validate_compact_equivalence(original, value)
            baseline = validate_default_input(work, original)
            return {**baseline, "representation": self.representation, "compact_identity_sha256": canonical_sha256(value)}
        return validate_default_input(work, value)

    def fact_identity(self, work: list[dict[str, Any]]) -> str:
        return fact_equivalence_sha256(work)

    def body_chars(self, value: dict[str, Any]) -> int:
        if self.representation == COMPACT_REPRESENTATION:
            return sum(len(item[2]) for item in value["bodies"])
        return sum(len(item["content"]) for item in value["bodies"])


def load_contract(path: Path | None) -> SemanticInputContract:
    if path is None:
        identity = canonical_sha256({"representation": DEFAULT_REPRESENTATION, "instruction": default_instruction()})
        return SemanticInputContract(DEFAULT_REPRESENTATION, identity, None)
    resolved = path.resolve()
    value = json.loads(resolved.read_text(encoding="utf-8"))
    _require(isinstance(value, dict) and value.get("schema") == MANIFEST_SCHEMA, "semantic representation manifest schema is invalid")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == canonical_sha256(content), "semantic representation manifest identity changed")
    _require(value.get("representation") == COMPACT_REPRESENTATION, "unknown semantic representation")
    _require(value.get("instruction_identity") == canonical_sha256(compact_instruction()), "semantic representation instruction changed")
    _require(value.get("fact_equivalence") == "lossless-roundtrip-to-ownward.semantic-deduplicated-body-table/v1", "semantic representation equivalence contract changed")
    _require(value.get("selection") == "candidate-composition-declared", "semantic representation is not composition declared")
    _require(value.get("formal_requires_bound_candidate") is True, "semantic representation formal binding rule changed")
    return SemanticInputContract(COMPACT_REPRESENTATION, str(value["identity"]), str(resolved))
