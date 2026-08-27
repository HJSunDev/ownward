from __future__ import annotations

import copy
import hashlib
import json
from pathlib import Path
import shutil
from types import SimpleNamespace
from typing import Any

from adapters.product import verify


def rebind_replayable_evidence(
    *,
    binding_dir: Path,
    workspace: Path,
    tasks: dict[str, Any],
    binding: dict[str, Any],
    resource_sha256: str,
    codex_binary: Path,
    codex_model: str,
    codex_reasoning_effort: str,
    include_preflight: bool,
) -> list[dict[str, Any]]:
    """Re-derive checkpoints from immutable traces after parser-only tool changes.

    The formal product scope still changes identity and is rescored. This function
    only preserves expensive scenario execution whose candidate, inputs, model,
    task, resource evidence, and raw execution implementation are unchanged.
    """
    scenario_tasks = tasks.get("tasks")
    _require(isinstance(scenario_tasks, list), "product replay requires frozen scenario tasks")
    args = SimpleNamespace(
        evidence_dir=workspace / "evidence" / "product" / "scenarios",
        codex_binary=codex_binary.resolve(),
        codex_model=codex_model,
        codex_reasoning_effort=codex_reasoning_effort,
    )
    receipts = _rebind_scenarios(
        args, scenario_tasks, binding, resource_sha256, binding_dir.resolve(),
    )
    if include_preflight:
        receipts.extend(_rebind_preflight(
            args,
            tasks,
            binding,
            resource_sha256,
            binding_dir.resolve(),
            workspace / "evidence" / "product-preflight",
        ))
    return receipts


def _rebind_scenarios(
    args: SimpleNamespace,
    tasks: list[dict[str, Any]],
    binding: dict[str, Any],
    resource_sha256: str,
    binding_dir: Path,
) -> list[dict[str, Any]]:
    receipts: list[dict[str, Any]] = []
    for task in tasks:
        scenario_root = args.evidence_dir / str(task["scenario_id"])
        if not scenario_root.is_dir() or not (scenario_root / "progress.json").is_file():
            continue
        current = verify._scenario_binding(args, task, binding, resource_sha256)
        pending = scenario_root / "derivation-replay-pending.json"
        if pending.is_file():
            record = verify.load_json(pending)
            _require(record.get("current_binding") == current, "pending derivation replay belongs to another binding")
            active = verify.load_json(scenario_root / "progress.json")
            if active.get("binding") == current:
                receipts.append(_finish_pending_replay(scenario_root, task, record, binding_dir))
                continue
            _restore_pending_source(scenario_root, record)
        progress = verify.load_json(scenario_root / "progress.json")
        previous = progress.get("binding")
        if previous == current:
            continue
        if not _bindings_replay_compatible(previous, current, binding_dir):
            continue
        receipts.append(_replay_scenario(scenario_root, task, previous, current, binding_dir))
    return receipts


def _replay_scenario(
    scenario_root: Path,
    task: dict[str, Any],
    previous: dict[str, Any],
    current: dict[str, Any],
    binding_dir: Path,
) -> dict[str, Any]:
    progress_path = scenario_root / "progress.json"
    agent_path = scenario_root / "agent-result.json"
    result_path = scenario_root / "result.json"
    progress = verify.load_json(progress_path)
    _validate_progress(progress, scenario_root, previous, sealed=result_path.is_file())
    agent = verify.load_json(agent_path) if agent_path.is_file() else None
    sealed = verify.load_json(result_path) if result_path.is_file() else None
    if agent is not None:
        _require(
            verify._agent_checkpoint_valid(agent, scenario_root, previous),
            "previous-binding agent checkpoint is invalid",
        )
    if sealed is not None:
        _require(
            verify._sealed_scenario_valid(sealed, scenario_root, previous, bool(task.get("updates"))),
            "previous-binding sealed checkpoint is invalid",
        )

    compatibility = _compatibility_receipt(previous, current, binding_dir)
    audit = scenario_root / "derivation-audit" / f"{previous['tool_sha256'][:12]}-{current['tool_sha256'][:12]}"
    source_hashes = _archive_derivations(scenario_root, audit)

    replayed_progress = _replay_progress(progress, scenario_root, current)
    replayed_agent = _replay_agent(agent, progress, replayed_progress, scenario_root, current) if agent is not None else None
    replayed_measurement: dict[str, Any] | None = None
    replayed_measurement_path: Path | None = None
    if sealed is not None and replayed_agent is not None:
        measurement_path = scenario_root / str(sealed["direct_evidence_path"])
        measurement = verify.load_json(measurement_path)
        _require(
            not measurement.get("prior_readiness_failures"),
            "sealed scenario with bound readiness failures requires a fresh direct measurement",
        )
        replayed_measurement = copy.deepcopy(measurement)
        replayed_measurement["binding"] = current
        replayed_measurement["progress_sha256"] = verify.json_sha256(replayed_progress)
        replayed_measurement["agent_checkpoint_sha256"] = _written_json_sha256(replayed_agent)
        replayed_measurement_path = (
            scenario_root / "direct" / "replays" / current["tool_sha256"][:16] / "measurement.json"
        )

    pending = scenario_root / "derivation-replay-pending.json"
    verify.write_json(pending, {
        "schema": "ownward.product-derivation-replay-pending/v1",
        "previous_binding": previous,
        "current_binding": current,
        "audit": audit.relative_to(scenario_root).as_posix(),
    })
    if replayed_agent is not None:
        verify.write_json(agent_path, replayed_agent)
    if replayed_measurement is not None and replayed_measurement_path is not None:
        verify.write_json(replayed_measurement_path, replayed_measurement)
        replayed_result = verify._merge_direct_result(replayed_agent["result"], replayed_measurement)
        verify.write_json(result_path, {
            "schema": "ownward.product-scenario-checkpoint/v3",
            "binding": current,
            "agent_checkpoint_sha256": verify.sha256(agent_path),
            "direct_evidence_path": replayed_measurement_path.relative_to(scenario_root).as_posix(),
            "direct_evidence_sha256": verify.sha256(replayed_measurement_path),
            "result": replayed_result,
        })
    _rebind_rollback_marker(scenario_root, replayed_progress)
    verify.write_json(progress_path, replayed_progress)

    _require(
        verify.load_json(progress_path).get("binding") == current,
        "replayed progress did not bind the current derivation",
    )
    if replayed_agent is not None:
        _require(
            verify._agent_checkpoint_valid(verify.load_json(agent_path), scenario_root, current),
            "replayed agent checkpoint is invalid",
        )
    if replayed_measurement is not None:
        _require(
            verify._sealed_scenario_valid(
                verify.load_json(result_path), scenario_root, current, bool(task.get("updates")),
            ),
            "replayed sealed checkpoint is invalid",
        )

    receipt = {
        "schema": "ownward.product-derivation-replay/v1",
        "scenario_id": str(task["scenario_id"]),
        "previous_binding": previous,
        "current_binding": current,
        "compatibility": compatibility,
        "source_derivations": source_hashes,
        "raw_evidence": dict(agent["evidence"] if agent is not None else progress["evidence"]),
        "replayed": {
            "progress_sha256": verify.sha256(progress_path),
            "agent_checkpoint_sha256": verify.sha256(agent_path) if agent_path.is_file() else None,
            "sealed_checkpoint_sha256": verify.sha256(result_path) if result_path.is_file() else None,
        },
    }
    receipt_path = scenario_root / "derivation-replay" / f"{current['tool_sha256']}.json"
    verify.write_json(receipt_path, receipt)
    pending.unlink()
    return {"scenario_id": str(task["scenario_id"]), "receipt": str(receipt_path)}


def _validate_progress(
    progress: dict[str, Any], scenario_root: Path, binding: dict[str, Any], *, sealed: bool,
) -> None:
    _require(progress.get("schema") == "ownward.product-scenario-progress/v1", "scenario progress schema is invalid")
    _require(progress.get("binding") == binding, "scenario progress binding is invalid")
    _require(verify._progress_evidence_complete(progress), "scenario progress evidence is incomplete")
    _require(verify._evidence_valid(scenario_root, progress["evidence"]), "scenario progress evidence changed")
    data = scenario_root / "data"
    _require(
        (sealed and not data.exists()) or (data.is_dir() and verify._tree_sha256(data) == progress.get("data_tree_sha256")),
        "scenario data does not match its progress checkpoint",
    )


def _replay_progress(
    progress: dict[str, Any], scenario_root: Path, current: dict[str, Any],
) -> dict[str, Any]:
    result = copy.deepcopy(progress)
    result["binding"] = current
    for unit, old_entry in progress["completed_units"].items():
        entry = copy.deepcopy(old_entry)
        stage_name = "semantic-update" if str(unit).startswith("update:") else "semantic-initial"
        stage_root = scenario_root / stage_name / verify.json_sha256(str(unit))[:16]
        operations: list[str] = []
        accepted: dict[str, Any] | None = None
        for attempt in sorted(item for item in stage_root.glob("attempt-*") if item.is_dir()):
            events = attempt / "events.jsonl"
            _require(events.is_file(), "semantic replay lacks raw event evidence")
            trace = verify.codex_session.load_exec_events(events.read_text(encoding="utf-8"))
            operations.extend(str(value) for value in trace.protocol_operations)
            attempt_record = attempt / "attempt.json"
            if attempt_record.is_file():
                record = verify.load_json(attempt_record)
                _require(record.get("status") == "rejected", "semantic replay encountered a non-terminal attempt record")
                no_calls = not trace.bypassed and not trace.calls
                recoverable = verify._recoverable_semantic_rejection(trace)
                _require(no_calls or recoverable, "current parser no longer agrees with a rejected semantic attempt")
                _require(int(record.get("tool_calls", len(trace.calls))) == len(trace.calls), "semantic attempt call count changed")
                _require(
                    int(record.get("failed_tool_calls", sum(1 for call in trace.calls if call.error)))
                    == sum(1 for call in trace.calls if call.error),
                    "semantic attempt failure count changed",
                )
                continue
            _require(not trace.bypassed, "current parser found a semantic execution bypass")
            identity = verify._semantic_trace_identity(trace, str(entry["asset_id"]), int(entry["revision"]))
            _require(accepted is None, "semantic replay found more than one successful terminal submission")
            accepted = identity
        _require(accepted is not None, f"semantic replay found no successful submission for {unit}")
        _require(
            accepted["work_id"] == entry.get("work_id")
            and accepted["submission_sha256"] == entry.get("submission_sha256"),
            f"semantic replay identity changed for {unit}",
        )
        entry["protocol_operations"] = operations
        result["completed_units"][unit] = entry
    return result


def _replay_agent(
    agent: dict[str, Any],
    old_progress: dict[str, Any],
    new_progress: dict[str, Any],
    scenario_root: Path,
    current: dict[str, Any],
) -> dict[str, Any]:
    _require(agent.get("schema") == "ownward.product-scenario-agent-checkpoint/v2", "only bounded v2 query checkpoints can be replayed")
    reverse = {str(stable): str(node) for node, stable in old_progress["stable_by_node"].items()}
    traces: list[Any] = []
    accepted: tuple[Any, list[str], list[str], bool] | None = None
    for recorded in agent["query_attempts"]:
        stage = scenario_root / str(recorded["path"])
        trace = verify.codex_session.load_exec_events((stage / "events.jsonl").read_text(encoding="utf-8"))
        traces.append(trace)
        status = "accepted"
        reason: str | None = None
        try:
            verify._query_trace_metrics(trace)
            returned, facts = verify._validated_answer(verify.load_json(stage / "output.json"))
            grounded = verify._grounded_query_answer(trace, returned, facts)
            verify.query_require(grounded, "query agent answer was not grounded in successfully read evidence")
            verify.query_require(set(returned) <= set(reverse), "query agent returned evidence outside the frozen scenario")
        except verify.QueryAttemptRejected as error:
            status, reason = "rejected", str(error)
        _require(status == recorded.get("status"), "current parser changed a recorded query attempt decision")
        if status == "rejected":
            _require(reason == recorded.get("reason"), "current parser changed a query rejection reason")
            continue
        accepted = (trace, returned, facts, grounded)
        break
    _require(accepted is not None, "query replay found no accepted attempt")
    trace, returned, facts, grounded = accepted
    old_result = agent["result"]
    replayed_fields = {
        "returned_ids": list(dict.fromkeys(reverse[value] for value in returned)),
        "answer_facts": list(dict.fromkeys(facts)),
        "navigation_ids": list(dict.fromkeys(
            reverse[value]
            for value in verify._observed(trace, {"ownward_navigate"})
            if value in reverse
        )),
        "grounded": grounded,
        "used_navigation": any(call.name == "ownward_navigate" and not call.error for call in trace.calls),
        "agent_query_attempts": len(traces),
        "agent_tool_calls": sum(len(value.calls) for value in traces),
        "agent_successful_tool_calls": sum(1 for value in traces for call in value.calls if not call.error),
        "agent_failed_tool_calls": sum(1 for value in traces for call in value.calls if call.error),
    }
    for name, value in replayed_fields.items():
        _require(old_result.get(name) == value, f"current parser changed replayed query field: {name}")
    result = copy.deepcopy(agent)
    result["binding"] = current
    result["progress_sha256"] = _written_json_sha256(new_progress)
    return result


def _archive_derivations(scenario_root: Path, audit: Path) -> dict[str, str]:
    audit.mkdir(parents=True, exist_ok=True)
    result: dict[str, str] = {}
    for relative in ("progress.json", "agent-result.json", "result.json", "rollback/manifest.json"):
        source = scenario_root / relative
        if not source.is_file():
            continue
        target = audit / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        if target.is_file():
            _require(target.read_bytes() == source.read_bytes(), f"archived derivation changed: {relative}")
        else:
            shutil.copyfile(source, target)
        result[relative] = verify.sha256(target)
    return result


def _finish_pending_replay(
    scenario_root: Path,
    task: dict[str, Any],
    pending: dict[str, Any],
    binding_dir: Path,
) -> dict[str, Any]:
    previous = pending.get("previous_binding")
    current = pending.get("current_binding")
    _require(isinstance(previous, dict) and isinstance(current, dict), "pending replay binding is invalid")
    audit = scenario_root / str(pending.get("audit", ""))
    _require(audit.is_dir(), "pending replay audit is missing")
    progress = verify.load_json(scenario_root / "progress.json")
    _validate_progress(progress, scenario_root, current, sealed=(scenario_root / "result.json").is_file())
    agent_path = scenario_root / "agent-result.json"
    result_path = scenario_root / "result.json"
    if agent_path.is_file():
        _require(verify._agent_checkpoint_valid(verify.load_json(agent_path), scenario_root, current), "pending replay agent checkpoint is invalid")
    if result_path.is_file():
        _require(
            verify._sealed_scenario_valid(verify.load_json(result_path), scenario_root, current, bool(task.get("updates"))),
            "pending replay sealed checkpoint is invalid",
        )
    source_progress = verify.load_json(audit / "progress.json")
    source_hashes = {
        path.relative_to(audit).as_posix(): verify.sha256(path)
        for path in sorted(audit.rglob("*.json"))
    }
    receipt = {
        "schema": "ownward.product-derivation-replay/v1",
        "scenario_id": str(task["scenario_id"]),
        "previous_binding": previous,
        "current_binding": current,
        "compatibility": _compatibility_receipt(previous, current, binding_dir),
        "source_derivations": source_hashes,
        "raw_evidence": dict(
            verify.load_json(audit / "agent-result.json")["evidence"]
            if (audit / "agent-result.json").is_file()
            else source_progress["evidence"]
        ),
        "replayed": {
            "progress_sha256": verify.sha256(scenario_root / "progress.json"),
            "agent_checkpoint_sha256": verify.sha256(agent_path) if agent_path.is_file() else None,
            "sealed_checkpoint_sha256": verify.sha256(result_path) if result_path.is_file() else None,
        },
    }
    receipt_path = scenario_root / "derivation-replay" / f"{current['tool_sha256']}.json"
    verify.write_json(receipt_path, receipt)
    (scenario_root / "derivation-replay-pending.json").unlink()
    return {"scenario_id": str(task["scenario_id"]), "receipt": str(receipt_path)}


def _restore_pending_source(scenario_root: Path, pending: dict[str, Any]) -> None:
    audit = scenario_root / str(pending.get("audit", ""))
    _require(audit.is_dir(), "pending replay audit is missing")
    for relative in ("progress.json", "agent-result.json", "result.json", "rollback/manifest.json"):
        source = audit / relative
        target = scenario_root / relative
        if source.is_file():
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source, target)
        elif target.is_file() and relative in {"agent-result.json", "result.json"}:
            target.unlink()
    (scenario_root / "derivation-replay-pending.json").unlink()


def _rebind_rollback_marker(scenario_root: Path, progress: dict[str, Any]) -> None:
    marker = scenario_root / "rollback" / "manifest.json"
    if not marker.is_file():
        return
    value = verify.load_json(marker)
    _require(value.get("schema") == "ownward.product-scenario-rollback/v1", "scenario rollback schema is invalid")
    value["progress_sha256"] = verify.json_sha256(progress)
    verify.write_json(marker, value)


def _rebind_preflight(
    args: SimpleNamespace,
    tasks: dict[str, Any],
    binding: dict[str, Any],
    resource_sha256: str,
    binding_dir: Path,
    root: Path,
) -> list[dict[str, Any]]:
    report_path = root / "report.json"
    if not report_path.is_file():
        return []
    preflight_tasks = verify._preflight_tasks(tasks)
    preflight_args = copy.copy(args)
    preflight_args.evidence_dir = root / "scenarios" / "_preflight"
    receipts = _rebind_scenarios(preflight_args, preflight_tasks, binding, resource_sha256, binding_dir)
    report = verify.load_json(report_path)
    previous = report.get("qualification_binding")
    if previous == binding:
        return receipts
    if not _bindings_replay_compatible(previous, binding, binding_dir):
        return receipts
    _require(report.get("passed") is True, "failed product preflight cannot be rebound")
    _require(report.get("task_set_sha256") == verify.json_sha256(tasks), "product preflight tasks changed")
    _require(report.get("resource_report_sha256") == resource_sha256, "product preflight resource evidence changed")

    full_query_path = Path(str(report.get("full_information_query", {}).get("path", ""))).resolve()
    _require(full_query_path.is_file() and verify.sha256(full_query_path) == report["full_information_query"].get("sha256"), "product preflight full-information evidence changed")
    full_query = verify.load_json(full_query_path)
    _require(full_query.get("passed") is True, "failed full-information preflight cannot be rebound")
    _require(full_query.get("identity", {}).get("binding") == previous, "full-information preflight binding changed")
    _archive_json(root / "_audit" / "derivation-replay" / "full-information", full_query_path)
    full_query["identity"]["binding"] = binding
    verify.write_json(full_query_path, full_query)

    expected_bindings = [verify._scenario_binding(preflight_args, task, binding, resource_sha256) for task in preflight_tasks]
    for task, expected in zip(preflight_tasks, expected_bindings):
        scenario = preflight_args.evidence_dir / str(task["scenario_id"])
        _require(
            verify._sealed_scenario_valid(verify.load_json(scenario / "result.json"), scenario, expected, False),
            "rebound product preflight scenario is invalid",
        )
    _archive_json(root / "_audit" / "derivation-replay" / "reports", report_path)
    report["qualification_binding"] = binding
    report["bindings"] = expected_bindings
    report["full_information_query"] = {"path": str(full_query_path), "sha256": verify.sha256(full_query_path)}
    verify.write_json(report_path, report)
    receipts.append({"preflight_report": str(report_path)})
    return receipts


def _archive_json(root: Path, source: Path) -> Path:
    root.mkdir(parents=True, exist_ok=True)
    target = root / f"{verify.sha256(source)}.json"
    if target.is_file():
        _require(target.read_bytes() == source.read_bytes(), "archived JSON derivation changed")
    else:
        shutil.copyfile(source, target)
    return target


def _bindings_replay_compatible(previous: Any, current: dict[str, Any], binding_dir: Path) -> bool:
    if not isinstance(previous, dict) or set(previous) != set(current):
        return False
    if previous.get("tool_sha256") == current.get("tool_sha256"):
        return False
    if any(previous.get(name) != value for name, value in current.items() if name != "tool_sha256"):
        return False
    try:
        _compatibility_receipt(previous, current, binding_dir)
    except RuntimeError:
        return False
    return True


def _compatibility_receipt(
    previous: dict[str, Any], current: dict[str, Any], binding_dir: Path,
) -> dict[str, Any]:
    old_path, old_manifest = _tool_manifest(binding_dir, str(previous["tool_sha256"]))
    new_path, new_manifest = _tool_manifest(binding_dir, str(current["tool_sha256"]))
    _require(new_manifest.get("schema") == "ownward.acceptance-tool-manifest/v5", "current product tool manifest has no responsibility identity")
    current_raw = _responsibility(new_manifest, "raw_execution", "ownward.product-raw-execution-identity/v1")
    current_derivation = _responsibility(new_manifest, "derivation", "ownward.product-derivation-identity/v1")
    migration: dict[str, Any] | None = None
    if old_manifest.get("schema") == "ownward.acceptance-tool-manifest/v5":
        previous_raw = _responsibility(old_manifest, "raw_execution", "ownward.product-raw-execution-identity/v1")
        _require(previous_raw["sha256"] == current_raw["sha256"], "product raw execution identity changed")
        compatibility = {
            "kind": "responsibility-identity",
            "previous_raw_execution_sha256": previous_raw["sha256"],
            "current_raw_execution_sha256": current_raw["sha256"],
        }
    else:
        _require(old_manifest.get("schema") == "ownward.acceptance-tool-manifest/v4", "previous product tool manifest schema is unsupported")
        source_files_sha256 = verify.json_sha256(old_manifest.get("files"))
        source_files = {
            str(value.get("path")): str(value.get("sha256"))
            for value in old_manifest.get("files", [])
            if isinstance(value, dict)
        }
        source_parser_sha256 = source_files.get("benchmarks/acceptance/suite/adapters/product/codex_session.py")
        migrations = new_manifest.get("legacy_derivation_replay")
        _require(isinstance(migrations, list), "current product tool manifest has no legacy replay proof")
        migration = next((
            value for value in migrations
            if isinstance(value, dict)
            and value.get("source_tool_sha256") == previous["tool_sha256"]
            and value.get("source_files_sha256") == source_files_sha256
            and value.get("source_parser_sha256") == source_parser_sha256
            and value.get("target_raw_execution_sha256") == current_raw["sha256"]
            and value.get("target_derivation_sha256") == current_derivation["sha256"]
        ), None)
        _require(migration is not None, "legacy product tool identity has no exact replay proof")
        compatibility = {
            "kind": "one-time-legacy-proof",
            "source_tool_sha256": previous["tool_sha256"],
            "source_files_sha256": source_files_sha256,
            "current_raw_execution_sha256": current_raw["sha256"],
            "current_derivation_sha256": current_derivation["sha256"],
            "migration_id": migration.get("migration_id"),
            "proof": migration.get("proof"),
        }
    return {
        "previous_tool_manifest": {"path": str(old_path), "sha256": verify.sha256(old_path)},
        "current_tool_manifest": {"path": str(new_path), "sha256": verify.sha256(new_path)},
        "compatibility": compatibility,
    }


def _tool_manifest(binding_dir: Path, digest: str) -> tuple[Path, dict[str, Any]]:
    _require(len(digest) == 64, "product tool identity is invalid")
    matches = [
        path for path in binding_dir.rglob("product-tools.json")
        if path.is_file() and verify.sha256(path) == digest
    ]
    _require(matches, f"product tool manifest is unavailable: {digest}")
    manifests = [(path, verify.load_json(path)) for path in matches]
    canonical = verify.json_sha256(manifests[0][1])
    _require(all(verify.json_sha256(value) == canonical for _, value in manifests), "product tool identity has conflicting manifests")
    manifest = manifests[0][1]
    _require(
        manifest.get("schema") in {"ownward.acceptance-tool-manifest/v4", "ownward.acceptance-tool-manifest/v5"}
        and manifest.get("scope") == "product",
        "product tool manifest is invalid",
    )
    return manifests[0]


def _responsibility(manifest: dict[str, Any], name: str, schema: str) -> dict[str, Any]:
    responsibilities = manifest.get("responsibilities")
    _require(isinstance(responsibilities, dict), "product tool responsibility identity is missing")
    result = responsibilities.get(name)
    _require(
        isinstance(result, dict)
        and result.get("schema") == schema
        and isinstance(result.get("files"), list)
        and len(str(result.get("sha256", ""))) == 64
        and verify.json_sha256({"schema": schema, "files": result["files"]}) == result["sha256"],
        f"product tool {name} identity is invalid",
    )
    return result


def _written_json_sha256(value: Any) -> str:
    encoded = (json.dumps(value, ensure_ascii=False, indent=2) + "\n").encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise RuntimeError(message)
