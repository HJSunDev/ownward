from __future__ import annotations

from typing import Any


RELATION_TYPES = [
    "same_as",
    "broader_than",
    "narrower_than",
    "part_of",
    "has_part",
    "supports",
    "contradicts",
    "derived_from",
    "applies_in",
    "related_to",
]


def _string_array() -> dict[str, Any]:
    return {"type": "array", "items": {"type": "string"}}


def hidden_world_schema() -> dict[str, Any]:
    node = {
        "type": "object",
        "additionalProperties": False,
        "required": ["id", "facts"],
        "properties": {"id": {"type": "string"}, "facts": _string_array()},
    }
    relation = {
        "type": "object",
        "additionalProperties": False,
        "required": ["source_id", "type", "target_id"],
        "properties": {
            "source_id": {"type": "string"},
            "type": {"type": "string", "enum": RELATION_TYPES},
            "target_id": {"type": "string"},
        },
    }
    update = {
        "type": "object",
        "additionalProperties": False,
        "required": ["node_id", "replacement_facts"],
        "properties": {"node_id": {"type": "string"}, "replacement_facts": _string_array()},
    }
    query = {
        "type": "object",
        "additionalProperties": False,
        "required": ["intent", "expected_ids", "forbidden_ids", "answer_facts"],
        "properties": {
            "intent": {"type": "string"},
            "expected_ids": _string_array(),
            "forbidden_ids": _string_array(),
            "answer_facts": _string_array(),
        },
    }
    scenario = {
        "type": "object",
        "additionalProperties": False,
        "required": ["id", "task_class", "information_scope", "nodes", "relations", "updates", "query"],
        "properties": {
            "id": {"type": "string"},
            "task_class": {"type": "string"},
            "information_scope": _string_array(),
            "nodes": {"type": "array", "items": node},
            "relations": {"type": "array", "items": relation},
            "updates": {"type": "array", "items": update},
            "query": query,
        },
    }
    return {
        "type": "object",
        "additionalProperties": False,
        "required": ["scenarios"],
        "properties": {"scenarios": {"type": "array", "items": scenario}},
    }


def expression_schema() -> dict[str, Any]:
    information = {
        "type": "object",
        "additionalProperties": False,
        "required": ["node_id", "content"],
        "properties": {"node_id": {"type": "string"}, "content": {"type": "string"}},
    }
    scenario = {
        "type": "object",
        "additionalProperties": False,
        "required": ["id", "information", "updates", "query"],
        "properties": {
            "id": {"type": "string"},
            "information": {"type": "array", "items": information},
            "updates": {"type": "array", "items": information},
            "query": {
                "type": "object",
                "additionalProperties": False,
                "required": ["question"],
                "properties": {"question": {"type": "string"}},
            },
        },
    }
    return {
        "type": "object",
        "additionalProperties": False,
        "required": ["scenarios"],
        "properties": {"scenarios": {"type": "array", "items": scenario}},
    }


def validation_schema() -> dict[str, Any]:
    verdict = {
        "type": "object",
        "additionalProperties": False,
        "required": ["id", "valid", "issues"],
        "properties": {"id": {"type": "string"}, "valid": {"type": "boolean"}, "issues": _string_array()},
    }
    return {
        "type": "object",
        "additionalProperties": False,
        "required": ["scenarios"],
        "properties": {"scenarios": {"type": "array", "items": verdict}},
    }


def answers_schema() -> dict[str, Any]:
    answer = {
        "type": "object",
        "additionalProperties": False,
        "required": ["query_id", "answer_facts", "information_ids"],
        "properties": {
            "query_id": {"type": "string"},
            "answer_facts": _string_array(),
            "information_ids": _string_array(),
        },
    }
    return {
        "type": "object",
        "additionalProperties": False,
        "required": ["answers"],
        "properties": {"answers": {"type": "array", "items": answer}},
    }
