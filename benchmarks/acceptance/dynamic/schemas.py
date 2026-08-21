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


def hidden_content_schema(structure: dict[str, Any]) -> dict[str, Any]:
    scenario_properties: dict[str, Any] = {}
    scenario_ids: list[str] = []
    for scenario in structure["scenarios"]:
        scenario_id = str(scenario["id"])
        scenario_ids.append(scenario_id)
        node_ids = [str(value["id"]) for value in scenario["nodes"]]
        update_ids = [str(value) for value in scenario["update_node_ids"]]
        scenario_properties[scenario_id] = {
            "type": "object",
            "additionalProperties": False,
            "required": ["nodes", "updates", "question"],
            "properties": {
                "nodes": {
                    "type": "object",
                    "additionalProperties": False,
                    "required": node_ids,
                    "properties": {node_id: _string_array() for node_id in node_ids},
                },
                "updates": {
                    "type": "object",
                    "additionalProperties": False,
                    "required": update_ids,
                    "properties": {node_id: _string_array() for node_id in update_ids},
                },
                "question": {"type": "string"},
            },
        }
    return {
        "type": "object",
        "additionalProperties": False,
        "required": ["scenarios"],
        "properties": {
            "scenarios": {
                "type": "object",
                "additionalProperties": False,
                "required": scenario_ids,
                "properties": scenario_properties,
            }
        },
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
