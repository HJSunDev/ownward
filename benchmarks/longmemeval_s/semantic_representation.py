from __future__ import annotations

from dataclasses import dataclass
from functools import lru_cache
import copy
import hashlib
import json
from pathlib import Path
from typing import Any


MANIFEST_SCHEMA = "ownward.kernel-iteration-semantic-representation/v1"
DEFAULT_REPRESENTATION = "ownward.semantic-deduplicated-body-table/v1"
COMPACT_REPRESENTATION = "ownward.semantic-indexed-body-context-table/v2"
LEGACY_GROUNDED_REPRESENTATION = "ownward.semantic-source-spans/v1"
GROUNDED_REPRESENTATION = "ownward.semantic-source-spans/v2"


@lru_cache(maxsize=1)
def organization_contract() -> dict[str, Any]:
    return json.loads((Path(__file__).resolve().parents[2] / "internal/semantics/organization_contract.json").read_text(encoding="utf-8"))


def organization_requested(work: list[dict[str, Any]]) -> bool:
    return bool(work) and all(item.get("organization_schema") == "ownward.organization/v1" for item in work)


def input_references(work: list[dict[str, Any]]) -> list[dict[str, Any]]:
    refs = {}
    for item in work:
        for source in [item["asset"], *item.get("candidates", [])]:
            snapshot = (source.get("organization") or {}).get("snapshot", "")
            key = (source["id"], source["revision"], snapshot)
            refs[key] = {"id": key[0], "revision": key[1], **({"organization_snapshot": snapshot} if snapshot else {})}
    return [refs[key] for key in sorted(refs)]


def organization_schema(work: list[dict[str, Any]], schema: dict[str, Any], representation: str = "") -> dict[str, Any]:
    if not organization_requested(work):
        return schema
    schema = copy.deepcopy(schema)
    analysis = schema["properties"]["analyses"]["items"]
    graph = copy.deepcopy(organization_contract()["output_schema"])
    if representation in {GROUNDED_REPRESENTATION, LEGACY_GROUNDED_REPRESENTATION}:
        def compact_locator(node):
            if isinstance(node, dict):
                if node.get("type") == "object" and "asset_id" in node.get("properties", {}):
                    # One endpoint has one locator. Repeating a unit locator
                    # as a passage range creates two independently generated
                    # addresses which can disagree without adding evidence.
                    refs = [b["body_ref"] for b in default_semantic_input(work)["bodies"]]
                    properties = {k:compact_locator(v) for k,v in node["properties"].items()}
                    properties["asset_id"] = {"enum": ["self", *refs]}
                    unit = {"type":"object", "additionalProperties":False,
                            "required":list(dict.fromkeys([*node.get("required",[]),"unit_id"])),
                            "properties":{k:v for k,v in properties.items() if k!="selector"}}
                    inventories = {c["id"] for item in work for c in item.get("candidates",[])
                                   if (c.get("organization") or {}).get("units")}
                    unit["properties"]["asset_id"] = {"enum":["self", *[
                        b["body_ref"] for b in default_semantic_input(work)["bodies"] if b["id"] in inventories]]}
                    if "mention_id" in node.get("required",[]):
                        return unit
                    raw = {"type":"object", "additionalProperties":False,
                           "required":["asset_id","selector"],
                           "properties":{k:properties[k] for k in ["asset_id","selector"]}}
                    return {"anyOf":[raw,unit]}
                if node.get("type") == "object" and {"source", "target"}.issubset(node.get("properties", {})):
                    # A relative owner stays stable when a rejected subset is
                    # retried. Work ordinals and source-table ordinals differ.
                    node = copy.deepcopy(node)
                    node["anyOf"] = [{"properties": {side: {"properties": {"asset_id": {"enum": ["self"]}}}}} for side in ["source", "target"]]
                if node.get("type") == "object" and "exact" in node.get("properties", {}):
                    # All supplied sources are numbered. Keep a single locator
                    # language rather than asking the model to copy quotations
                    # while also selecting source-relative passage indices.
                    return {"anyOf": [{"type":"integer", "minimum":0}, {"type":"array", "minItems":2, "maxItems":2, "items":{"type":"integer", "minimum":0}}]}
                return {k: compact_locator(v) for k,v in node.items()}
            return [compact_locator(v) for v in node] if isinstance(node,list) else node
        graph = compact_locator(graph)
        # A numbered source always has a range, including the complete source;
        # avoid the ambiguous combination of several units with null locators.
        graph["properties"]["units"]["items"]["properties"]["selector"] = {"anyOf":[{"type":"integer","minimum":0},{"type":"array","minItems":2,"maxItems":2,"items":{"type":"integer","minimum":0}}]}
        mention = graph["properties"]["units"]["items"]["properties"]["mentions"]["items"]
        mention["required"].append("selector")
        mention["properties"]["selector"] = copy.deepcopy(graph["properties"]["units"]["items"]["properties"]["selector"])
    analysis["properties"]["organization"] = graph
    analysis["required"].append("organization")
    return schema


class SemanticRepresentationError(RuntimeError):
    pass


def decode_organization(work: list[dict[str, Any]], item: dict[str, Any], value: dict[str, Any], representation: str = "") -> dict[str, Any]:
    # Resolve only exact aliases in the lossless presentation, not guessed IDs.
    result = copy.deepcopy(value)
    bodies = default_semantic_input(work)["bodies"]
    ids = {body["body_ref"]: body["id"] for body in bodies}
    allowed = {body["id"] for body in bodies}
    raw = {body["id"]: body["content"] for body in bodies}
    indexed = set(raw)
    def locate(selector, identifier, scoped=False):
        if type(selector) is int or isinstance(selector,list):
            _require(representation in {GROUNDED_REPRESENTATION, LEGACY_GROUNDED_REPRESENTATION} and identifier in indexed,
                     "passage locator requires a supplied numbered source")
            spans = source_passages(raw[identifier], representation)
            begin,end = (selector,selector) if type(selector) is int else tuple(selector)
            _require(type(begin) is int and type(end) is int and 0 <= begin <= end < len(spans),
                     f"organization range {selector!r} is outside source {identifier}; its passage indices are 0..{len(spans)-1}")
            before = ''.join(spans[:begin]); exact = ''.join(spans[begin:end+1]); after = ''.join(spans[end+1:])
            width = 32
            while True:
                prefix = before[-width:]; suffix = after[:width]
                if raw[identifier].count(prefix+exact+suffix)==1:
                    return {"exact":exact,"prefix":prefix,"suffix":suffix}
                width *= 2
        if selector is not None:
            _require(isinstance(selector,dict) and isinstance(selector.get("exact"),str),"invalid organization selector")
            exact=selector["exact"]; full=selector.get("prefix","")+exact+selector.get("suffix","")
            matches=raw[identifier].count(full)
            _require(bool(exact) and (matches==1 or (scoped and not selector.get("prefix") and not selector.get("suffix") and matches>0)),
                     f"organization quote in {identifier} must match its supplied source: {exact[:100]!r}")
        return selector
    own=item["asset"]["id"]
    ids["self"] = own
    for unit in result.get("units",[]):
        unit["selector"] = locate(unit.get("selector"),own)
        if unit.get("context"):unit["context"]=[locate(v,own) for v in unit["context"]]
        for mention in unit.get("mentions",[]):
            if mention.get("selector") is not None:mention["selector"]=locate(mention["selector"],own,scoped=True)
    _require(all(u.get("id") for u in result.get("units", [])), "each own organization unit needs a nonempty local id")
    for link in result.get("links", []):
        for endpoint in [link["source"], link["target"], *link.get("conditions", [])]:
            source_id=endpoint["asset_id"]
            if type(source_id) is int:
                _require(representation in {GROUNDED_REPRESENTATION, LEGACY_GROUNDED_REPRESENTATION} and 0<=source_id<len(work), "organization source index is outside supplied work")
                source_id=work[source_id]["asset"]["id"]
            endpoint["asset_id"] = ids.get(source_id, source_id)
            _require(endpoint["asset_id"] in allowed, "organization endpoint is outside this work's supplied sources")
            if endpoint.get("selector") is not None:endpoint["selector"]=locate(endpoint["selector"],endpoint["asset_id"])
        _require(item["asset"]["id"] in {link["source"]["asset_id"], link["target"]["asset_id"]},
                 f"a relation in work {item['id']} must involve its own target asset {item['asset']['id']}")
        if link["type"] == "same_object":
            _require(link["source"].get("mention_id") and link["target"].get("mention_id"),
                     "same_object requires two actual mention IDs; otherwise omit the identity link")
    return result


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


def grounded_instruction(representation: str = GROUNDED_REPRESENTATION) -> str:
    if representation == LEGACY_GROUNDED_REPRESENTATION:
        return (
            "Prepare source-owned retrieval metadata for every work item, in order. Return its explicit index, "
            "one summary passage index, up to 4 short topics, and up to 4 nonredundant cues {passage,kind}. "
            "Passage indices are the numbered keys in that item's target.passages, never another source. These passages "
            "concatenate to the complete source; select answer-bearing statements, preferences, events or decisions, "
            "preserving speaker, negation, conditions and changes. The host copies selected original passages; do not "
            "rewrite them. No cues for question-only or acknowledgement sources. Do not index source IDs or dates "
            "as facts. Related sources are reference context only. No query, answer or evaluation label is supplied."
            "\n\nSemantic input:\n"
        )
    return (
        "Prepare source-owned retrieval metadata for every work item, in order. Return its explicit index, "
        "one summary passage index, up to 4 short topics, and up to 8 nonredundant cues {passage,kind}. "
        "Passage indices are the numbered keys in that item's target.passages, never another source. These passages "
        "concatenate to the complete source; select answer-bearing statements, preferences, events or decisions, "
        "preserving speaker, negation, conditions and changes. The host copies selected original passages; do not "
        "rewrite them. Cover distinct stated facts across the source, including facts embedded in questions; "
        "do not spend cues on advice requests, acknowledgements or repeated topic mentions alone. Do not index source IDs or dates "
        "as facts. Related sources are reference context only. No query, answer or evaluation label is supplied."
        "\n\nSemantic input:\n"
    )


def source_passages(content: str, representation: str = GROUNDED_REPRESENTATION) -> list[str]:
    # Stable, lossless source slices; offsets avoid repeatedly copying the
    # unconsumed tail of a long source while planning several possible batches.
    passages = []
    legacy = representation == LEGACY_GROUNDED_REPRESENTATION
    offset, length = 0, len(content)
    while offset < length:
        end = min(200 if legacy else 384, length-offset)
        window = content[offset:offset+end]
        boundaries = [i+1 for i,char in enumerate(window)
                      if char == "\n" or (legacy and char in ".!?。！？" and
                         (offset+i+1 == length or content[offset+i+1].isspace()))]
        boundaries = [i for i in boundaries if window[:i].strip()]
        if boundaries:
            end = boundaries[0]
        elif offset+end < length:
            space = window.rfind(" ")
            if space > 0:
                end = space+1
        part = content[offset:offset+end]
        if not part.strip() and passages:
            passages[-1] += part
        else:
            passages.append(part)
        offset += end
    return passages or [""]


def grounded_input(original: dict[str, Any], representation: str = GROUNDED_REPRESENTATION, *, index_references: bool = False) -> dict[str, Any]:
    bodies = {body["body_ref"]: body for body in original["bodies"]}
    targets = {item["asset"]["body_ref"] for item in original["work"]}
    return {
        "representation": representation,
        "work": [{
            "index": index,
            "work_id": item["work_id"],
            "target": {"source_ref": item["asset"]["body_ref"],
                       **{k: v for k, v in bodies[item["asset"]["body_ref"]].items() if k not in {"body_ref", "content"}},
                       "passages": {str(i): text for i, text in enumerate(source_passages(bodies[item["asset"]["body_ref"]]["content"], representation))},
                       "explicit_contexts": item["asset"]["explicit_contexts"]},
            "related_sources": [{"source_ref": c["body_ref"], **{k: v for k, v in c.items() if k != "body_ref"}}
                                for c in item["candidates"]],
        } for index, item in enumerate(original["work"])],
        "reference_sources": [{"source_ref": ref, **{k: v for k, v in body.items() if k not in ({"body_ref","content"} if index_references else {"body_ref"})},
                               **({"passages":{str(i):text for i,text in enumerate(source_passages(body["content"],representation))}} if index_references else {})}
                              for ref, body in bodies.items() if ref not in targets],
    }


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
        for candidate in (item.get("candidates", []) if item.get("organization_schema") else item.get("candidates", [])[:2]):
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
        source_candidates = [item for item in (source.get("candidates", []) if source.get("organization_schema") else source.get("candidates", [])[:2]) if isinstance(item, dict)]
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
    body_order: str = "discovery"

    def ordered_input(self, work: list[dict[str, Any]]) -> dict[str, Any]:
        value = default_semantic_input(work)
        if self.body_order == "targets-first":
            # Keep primary sources in work order; append reference-only bodies.
            # No content or candidate metadata is discarded, and all indices are
            # rebuilt from the resulting table rather than inferred by position.
            by_ref = {body["body_ref"]: body for body in value["bodies"]}
            refs = list(dict.fromkeys([item["asset"]["body_ref"] for item in value["work"]] + list(by_ref)))
            value["bodies"] = [by_ref[ref] for ref in refs]
        return value

    def instruction(self) -> str:
        if self.representation in {LEGACY_GROUNDED_REPRESENTATION, GROUNDED_REPRESENTATION}:
            return grounded_instruction(self.representation)
        return compact_instruction() if self.representation == COMPACT_REPRESENTATION else default_instruction()

    def encode(self, work: list[dict[str, Any]]) -> dict[str, Any]:
        original = self.ordered_input(work)
        if self.representation in {LEGACY_GROUNDED_REPRESENTATION, GROUNDED_REPRESENTATION}:
            return grounded_input(original, self.representation, index_references=organization_requested(work))
        return compact_semantic_input(original) if self.representation == COMPACT_REPRESENTATION else original

    def validate(self, work: list[dict[str, Any]], value: dict[str, Any]) -> dict[str, Any]:
        original = self.ordered_input(work)
        if self.representation in {LEGACY_GROUNDED_REPRESENTATION, GROUNDED_REPRESENTATION}:
            _require(value == grounded_input(original, self.representation, index_references=organization_requested(work)), "grounded source identity or content changed")
            return {**validate_default_input(work, original), "representation": self.representation}
        if self.representation == COMPACT_REPRESENTATION:
            validate_compact_equivalence(original, value)
            baseline = validate_default_input(work, original)
            return {**baseline, "representation": self.representation, "compact_identity_sha256": canonical_sha256(value)}
        return validate_default_input(work, value)

    def fact_identity(self, work: list[dict[str, Any]]) -> str:
        return fact_equivalence_sha256(work)

    def body_chars(self, value: dict[str, Any]) -> int:
        if self.representation in {LEGACY_GROUNDED_REPRESENTATION, GROUNDED_REPRESENTATION}:
            return sum(sum(map(len, item["target"]["passages"].values())) for item in value["work"]) + sum(sum(map(len,item["passages"].values())) if "passages" in item else len(item["content"]) for item in value["reference_sources"])
        if self.representation == COMPACT_REPRESENTATION:
            return sum(len(item[2]) for item in value["bodies"])
        return sum(len(item["content"]) for item in value["bodies"])

    def output_schema(self, work: list[dict[str, Any]], legacy: dict[str, Any]) -> dict[str, Any]:
        if self.representation not in {LEGACY_GROUNDED_REPRESENTATION, GROUNDED_REPRESENTATION}:
            return organization_schema(work, legacy)
        return organization_schema(work, {"type": "object", "additionalProperties": False, "required": ["analyses"], "properties": {
            "analyses": {"type": "array", "minItems": len(work), "maxItems": len(work), "items": {
                "type": "object", "additionalProperties": False, "required": ["index", "summary", "topics", "cues"],
                "properties": {
                    "index": {"type": "integer", "minimum": 0, "maximum": len(work)-1},
                    "summary": {"type": "integer", "minimum": 0},
                    "topics": {"type": "array", "maxItems": 4, "items": {"type": "string", "maxLength": 100}},
                    "cues": {"type": "array", "maxItems": 4 if self.representation == LEGACY_GROUNDED_REPRESENTATION else 8, "items": {"type": "object", "additionalProperties": False,
                        "required": ["passage", "kind"], "properties": {"passage": {"type": "integer", "minimum": 0},
                                                                       "kind": {"type": "string", "maxLength": 40}}}},
                },
            }},
        }}, self.representation)

    def decode_analyses(self, work: list[dict[str, Any]], value: dict[str, Any]) -> list[dict[str, Any]]:
        analyses = value.get("analyses")
        _require(isinstance(analyses, list) and len(analyses) == len(work), "semantic output omitted work items")
        if self.representation not in {LEGACY_GROUNDED_REPRESENTATION, GROUNDED_REPRESENTATION}:
            _require([item.get("work_id") for item in analyses if isinstance(item, dict)] == [item["id"] for item in work],
                     "semantic output reordered work items")
            return [{**analysis, **({"organization": decode_organization(work, item, analysis["organization"], self.representation)} if "organization" in analysis else {})}
                    for item, analysis in zip(work, analyses)]
        _require([item.get("index") for item in analyses if isinstance(item, dict)] == list(range(len(work))),
                 "semantic output reordered work items")
        return [self.decode_analysis(work, index, analysis) for index, analysis in enumerate(analyses)]

    def decode_analysis(self, work: list[dict[str, Any]], index: int, analysis: dict[str, Any]) -> dict[str, Any]:
        item = work[index]
        graph = {"organization": decode_organization(work, item, analysis["organization"], self.representation)} if "organization" in analysis else {}
        if self.representation not in {LEGACY_GROUNDED_REPRESENTATION, GROUNDED_REPRESENTATION}:
            _require(analysis.get("work_id") == item["id"], "semantic output reordered work items")
            return {**analysis, **graph}
        _require(analysis.get("index") == index, "semantic output reordered work items")
        passages = source_passages(item["asset"]["content"], self.representation)
        def passage(selected: Any) -> str:
            _require(type(selected) is int and 0 <= selected < len(passages),
                     f"semantic passage {selected!r} is outside work {item['id']}; valid indices are 0..{len(passages)-1}")
            return passages[selected].strip()
        summary = passage(analysis.get("summary"))
        cues = [{"text": passage(cue.get("passage")), "kind": cue["kind"]} for cue in analysis.get("cues", [])]
        _require(bool(summary) and all(cue["text"] for cue in cues), "semantic passage is empty")
        return {"work_id": item["id"], "summary": summary, "topics": analysis["topics"], "cues": cues, **graph}


def load_contract(path: Path | None) -> SemanticInputContract:
    if path is None:
        identity = canonical_sha256({"representation": DEFAULT_REPRESENTATION, "instruction": default_instruction()})
        return SemanticInputContract(DEFAULT_REPRESENTATION, identity, None)
    resolved = path.resolve()
    value = json.loads(resolved.read_text(encoding="utf-8"))
    _require(isinstance(value, dict) and value.get("schema") == MANIFEST_SCHEMA, "semantic representation manifest schema is invalid")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == canonical_sha256(content), "semantic representation manifest identity changed")
    representation = value.get("representation")
    _require(representation in {COMPACT_REPRESENTATION, LEGACY_GROUNDED_REPRESENTATION, GROUNDED_REPRESENTATION}, "unknown semantic representation")
    instruction = compact_instruction() if representation == COMPACT_REPRESENTATION else grounded_instruction(representation)
    _require(value.get("instruction_identity") == canonical_sha256(instruction), "semantic representation instruction changed")
    _require(value.get("fact_equivalence") == "lossless-roundtrip-to-ownward.semantic-deduplicated-body-table/v1", "semantic representation equivalence contract changed")
    _require(value.get("selection") == "candidate-composition-declared", "semantic representation is not composition declared")
    _require(value.get("formal_requires_bound_candidate") is True, "semantic representation formal binding rule changed")
    body_order = value.get("body_order", "discovery")
    _require(body_order in {"discovery", "targets-first"}, "unknown semantic body order")
    return SemanticInputContract(str(representation), str(value["identity"]), str(resolved), str(body_order))
