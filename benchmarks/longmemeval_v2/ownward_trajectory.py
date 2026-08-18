from __future__ import annotations

from typing import Any


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise RuntimeError(message)


def _goal_text(value: object) -> str:
    if isinstance(value, str) and value.strip():
        return value.strip()
    if isinstance(value, list):
        parts = [part.strip() for part in value if isinstance(part, str) and part.strip()]
        if parts:
            return " ".join(parts)
    return "<goal not found>"


def normalize_trajectory(trajectory: dict[str, object]) -> dict[str, Any]:
    """把 LongMemEval-V2 的两种公开轨迹格式归一为纯文本状态。"""
    trajectory_id = trajectory.get("id")
    _require(isinstance(trajectory_id, str) and bool(trajectory_id.strip()), "trajectory id must be non-empty")

    raw_states = trajectory.get("states")
    public_format = isinstance(raw_states, list) and bool(raw_states)
    if not public_format:
        raw_states = trajectory.get("content")
    _require(isinstance(raw_states, list) and bool(raw_states), f"trajectory {trajectory_id} has no states")

    metadata = trajectory.get("metadata")
    goal_value = (
        trajectory.get("goal")
        if public_format
        else metadata.get("original_goal") if isinstance(metadata, dict) else None
    )
    goal = _goal_text(goal_value)
    outcome_value = trajectory.get("outcome")
    _require(outcome_value is None or isinstance(outcome_value, str), f"trajectory {trajectory_id} has invalid outcome")

    states: list[dict[str, Any]] = []
    actions: list[str] = []
    for index, raw_state in enumerate(raw_states):
        _require(isinstance(raw_state, dict), f"trajectory {trajectory_id} state {index} must be an object")
        url = raw_state.get("url")
        action = raw_state.get("action")
        thoughts = raw_state.get("thought", raw_state.get("thoughts"))
        if public_format:
            text = raw_state.get("accessibility_tree", raw_state.get("text"))
        else:
            observation = raw_state.get("observation")
            _require(isinstance(observation, dict), f"trajectory {trajectory_id} state {index} has no observation")
            text = observation.get("text")
        _require(isinstance(url, str) and bool(url.strip()), f"trajectory {trajectory_id} state {index} has no url")
        _require(
            action is None or isinstance(action, str),
            f"trajectory {trajectory_id} state {index} has invalid action",
        )
        _require(
            thoughts is None or isinstance(thoughts, str),
            f"trajectory {trajectory_id} state {index} has invalid thoughts",
        )
        _require(isinstance(text, str), f"trajectory {trajectory_id} state {index} has invalid text")
        if isinstance(action, str) and action.strip():
            actions.append(action.strip())
        step = raw_state.get("step", raw_state.get("state_index", index))
        states.append(
            {
                "state_index": index,
                "step": step if isinstance(step, int) and not isinstance(step, bool) else index,
                "url": url.strip(),
                "action": action.strip() if isinstance(action, str) and action.strip() else None,
                "thoughts": thoughts.strip() if isinstance(thoughts, str) and thoughts.strip() else None,
                "text": text.strip(),
            }
        )

    start_url = trajectory.get("start_url") if public_format else states[0]["url"]
    _require(isinstance(start_url, str) and bool(start_url.strip()), f"trajectory {trajectory_id} has no start url")
    return {
        "id": trajectory_id.strip(),
        "goal": goal,
        "outcome": outcome_value.strip() if isinstance(outcome_value, str) and outcome_value.strip() else None,
        "start_url": start_url.strip(),
        "actions": actions,
        "states": states,
    }


def _split_document(header: str, body: str, max_chars: int) -> list[str]:
    _require(max_chars >= 1000, "max_chunk_chars must be at least 1000")
    prefix = header.rstrip() + "\n"
    available = max_chars - len(prefix)
    _require(available >= 200, "document header leaves no room for evidence")
    text = body.strip()
    if not text:
        return [prefix.rstrip()]

    chunks: list[str] = []
    offset = 0
    while offset < len(text):
        end = min(len(text), offset + available)
        if end < len(text):
            boundary = text.rfind("\n", offset, end)
            if boundary <= offset:
                boundary = text.rfind(" ", offset, end)
            if boundary > offset:
                end = boundary
        chunk = text[offset:end].strip()
        if chunk:
            chunks.append(prefix + chunk)
        offset = end
        while offset < len(text) and text[offset].isspace():
            offset += 1
    return chunks


def trajectory_documents(trajectory: dict[str, object], max_chunk_chars: int) -> list[str]:
    """生成不含答案、题型或人工关系标签的 Ownward 信息资产。"""
    normalized = normalize_trajectory(trajectory)
    trajectory_id = normalized["id"]
    common = (
        f"LongMemEval-V2 trajectory: {trajectory_id}\n"
        f"Goal: {normalized['goal']}\n"
        f"Outcome: {normalized['outcome'] or '<not recorded>'}"
    )
    actions = "\n".join(f"{index + 1}. {action}" for index, action in enumerate(normalized["actions"]))
    overview = _split_document(
        common + f"\nRecord: trajectory overview\nStart URL: {normalized['start_url']}",
        "Actions:\n" + (actions or "<none recorded>"),
        max_chunk_chars,
    )

    documents = list(overview)
    for state in normalized["states"]:
        state_header = (
            common
            + f"\nRecord: state {state['state_index']} of {len(normalized['states']) - 1}"
            + f"\nStep: {state['step']}"
            + f"\nURL: {state['url']}"
            + f"\nAction: {state['action'] or '<none>'}"
            + f"\nThoughts: {state['thoughts'] or '<none>'}"
            + "\nObserved evidence:"
        )
        documents.extend(_split_document(state_header, state["text"], max_chunk_chars))
    return documents
