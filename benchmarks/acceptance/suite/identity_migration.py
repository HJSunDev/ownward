from __future__ import annotations

import copy
import json
from pathlib import Path
from typing import Any

import binding
import evidence_identity
import lifecycle


class IdentityMigrationError(ValueError):
    pass


def migrate(
    repository: Path,
    state_path: Path,
    binding_dir: Path,
    frozen_path: Path,
    *,
    write: bool,
) -> dict[str, Any]:
    repository = repository.resolve()
    state_path = state_path.resolve()
    binding_dir = binding_dir.resolve()
    frozen = _load_json(frozen_path.resolve())
    state_bytes = state_path.read_bytes()
    state = _decode_object(state_bytes, "Acceptance state")
    if state.get("schema") == evidence_identity.STATE_SCHEMA:
        if "reporting" in state.get("binding", {}):
            _recover_binding_pointer(binding_dir, state["binding"], write=write)
        refreshed = _refresh_v3_lifecycle(repository, state, binding_dir, frozen)
        if refreshed != state:
            lifecycle._validate_state(_load_json(repository / "benchmarks" / "acceptance" / "suite" / "contract.json"), refreshed)
            if not write:
                return _result(refreshed, changed=True, written=False)
            active_dir = _generation_dir_for_binding(binding_dir, state["binding"])
            legacy = _legacy_binding_from_v5_like(refreshed["binding"])
            manifests = _manifests_from_directory(active_dir, legacy)
            raw_sources = {filename: active_dir / filename for filename in manifests}
            generation = binding._stage_generation(binding_dir, refreshed["binding"], manifests, raw_sources=raw_sources)
            lifecycle.save_state(state_path, refreshed)
            binding._publish_generation(binding_dir, generation)
            _validate_completed(repository, state_path, binding_dir, frozen, refreshed)
            return _result(refreshed, changed=True, written=True)
        _validate_completed(repository, state_path, binding_dir, frozen, state)
        return _result(state, changed=False, written=False)
    if state.get("schema") == "ownward.acceptance-state/v2":
        return _correct_v2(repository, state_path, binding_dir, frozen, state, write=write)

    _verify_legacy_state(repository, state, state_bytes, binding_dir, frozen)
    v1_spec = frozen["candidates"]["v1"]
    v0_spec = frozen["candidates"]["v0"]
    active_dir = binding._active_generation_dir(binding_dir)
    active_legacy = binding.load_json(active_dir / "binding.json")
    active_manifests = _manifests_from_directory(active_dir, active_legacy)
    v1_components = evidence_identity.build_candidate_components(
        repository, v1_spec["candidate"], v1_spec["binary_sha256"], v1_spec["release_bundle"]["manifest_sha256"],
    )
    lifecycle_components = evidence_identity.lifecycle_identities(repository)
    next_binding = evidence_identity.build_binding(
        active_legacy, v1_components, active_manifests, lifecycle_components,
        evidence_identity.reporting_identities(repository),
    )
    next_state = _map_state(
        repository, state, state_bytes, next_binding, v0_spec,
        lifecycle_components, frozen, binding_dir,
    )
    lifecycle._validate_state(_load_json(repository / "benchmarks" / "acceptance" / "suite" / "contract.json"), next_state)

    if not write:
        return _result(next_state, changed=True, written=False)

    raw_sources = {filename: active_dir / filename for filename in active_manifests}
    generation = binding._stage_generation(
        binding_dir, next_binding, active_manifests, raw_sources=raw_sources,
    )
    # The only mutable formal state is replaced only after every source and
    # target identity has validated. A crash after this point is recovered by
    # the idempotent v2 branch, which publishes the already sealed generation.
    lifecycle.save_state(state_path, next_state)
    binding._publish_generation(binding_dir, generation)
    _validate_completed(repository, state_path, binding_dir, frozen, next_state)
    return _result(next_state, changed=True, written=True)


def _correct_v2(
    repository: Path,
    state_path: Path,
    binding_dir: Path,
    frozen: dict[str, Any],
    state: dict[str, Any],
    *,
    write: bool,
) -> dict[str, Any]:
    """Converge the first v2 dependency graph without touching any report."""
    migration = state.get("identity_migration")
    _require(isinstance(migration, dict) and migration.get("schema") == "ownward.acceptance-identity-migration/v1", "v2 状态缺少可审计的原始 v1 映射")
    payload = {name: value for name, value in migration.items() if name != "identity"}
    _require(migration.get("identity") == evidence_identity.canonical_sha256(payload), "v2 身份迁移收据漂移")
    source = migration.get("source")
    _require(isinstance(source, dict), "v2 身份迁移缺少原始 v1 来源")
    _require(source.get("state_file_sha256") == _direct_file_sha(frozen, "acceptance-unique-state"), "v2 身份迁移不是冻结起点")
    _verify_preserved_results(state)

    active_dir = _generation_dir_for_binding(binding_dir, state["binding"])
    old_binding = _load_json(active_dir / "binding.json")
    _require(old_binding == state.get("binding") and old_binding.get("schema") == "ownward.acceptance-binding/v5", "v2 活动绑定不是待纠正的 v5")
    old_lifecycle = old_binding.get("lifecycle", {}).get("evidence", {}).get("identity")
    _require(evidence_identity.is_sha256(str(old_lifecycle)), "v5 生命周期身份无效")
    for scope, value in old_binding.get("scopes", {}).items():
        _require(value.get("direct_dependencies", {}).get("acceptance-lifecycle") == old_lifecycle, f"{scope} 不是已知的生命周期过度绑定结构")

    v1_spec = frozen["candidates"]["v1"]
    v0_spec = frozen["candidates"]["v0"]
    legacy = _legacy_binding_from_v5_like(old_binding)
    manifests = _manifests_from_directory(active_dir, legacy)
    components = evidence_identity.build_candidate_components(
        repository, v1_spec["candidate"], v1_spec["binary_sha256"], v1_spec["release_bundle"]["manifest_sha256"],
    )
    lifecycle_components = evidence_identity.lifecycle_identities(repository)
    corrected_binding = evidence_identity.build_binding(
        legacy, components, manifests, lifecycle_components,
        evidence_identity.reporting_identities(repository),
    )
    corrected = _map_corrected_state(
        repository, state, corrected_binding, v0_spec, lifecycle_components, frozen, source,
    )
    contract = _load_json(repository / "benchmarks" / "acceptance" / "suite" / "contract.json")
    lifecycle._validate_state(contract, corrected)
    if not write:
        return _result(corrected, changed=True, written=False)

    raw_sources = {filename: active_dir / filename for filename in manifests}
    generation = binding._stage_generation(binding_dir, corrected_binding, manifests, raw_sources=raw_sources)
    lifecycle.save_state(state_path, corrected)
    binding._publish_generation(binding_dir, generation)
    _validate_completed(repository, state_path, binding_dir, frozen, corrected)
    return _result(corrected, changed=True, written=True)


def _refresh_v3_lifecycle(
    repository: Path, state: dict[str, Any], binding_dir: Path, frozen: dict[str, Any],
) -> dict[str, Any]:
    current = evidence_identity.lifecycle_identities(repository)
    reporting = evidence_identity.reporting_identities(repository)
    active_dir = _raw_active_generation_dir(binding_dir)
    legacy = _legacy_binding_from_v5_like(state["binding"])
    manifests = _manifests_from_directory(active_dir, legacy)
    corrected_binding = evidence_identity.build_binding(
        legacy, state["binding"]["components"], manifests, current, reporting,
    )
    if state.get("binding") == corrected_binding:
        return state
    migration = state.get("identity_migration")
    _require(isinstance(migration, dict) and migration.get("schema") == evidence_identity.MIGRATION_SCHEMA, "v3 身份迁移收据缺失")
    source = migration.get("source")
    _require(isinstance(source, dict), "v3 身份迁移缺少原始来源")
    return _map_corrected_state(
        repository, state, corrected_binding, frozen["candidates"]["v0"], current, frozen, source,
    )


def _recover_binding_pointer(binding_dir: Path, expected: dict[str, Any], *, write: bool) -> None:
    try:
        active = binding.load_active_binding(binding_dir)
    except (OSError, ValueError):
        active = None
    if active == expected:
        return
    expected_identity = evidence_identity.canonical_sha256(expected)
    matched: Path | None = None
    for directory in sorted((binding_dir / "generations").glob("*")):
        path = directory / "binding.json"
        if not path.is_file():
            continue
        try:
            candidate = binding.load_json(path)
        except (OSError, ValueError):
            continue
        if candidate == expected and evidence_identity.canonical_sha256(candidate) == expected_identity:
            matched = directory
            break
    _require(matched is not None, "身份迁移 state 已提交，但找不到对应的封存 binding 世代")
    _require(write, "身份迁移 state 已提交但活动 binding 指针尚未发布；需使用 --write 幂等恢复")
    binding._publish_generation(binding_dir, matched.name)


def _generation_dir_for_binding(binding_dir: Path, expected: dict[str, Any]) -> Path:
    active = _raw_active_generation_dir(binding_dir)
    path = active / "binding.json"
    if path.is_file() and _load_json(path) == expected:
        return active
    for directory in sorted((binding_dir / "generations").glob("*")):
        path = directory / "binding.json"
        if path.is_file() and _load_json(path) == expected:
            return directory
    raise IdentityMigrationError("找不到与唯一 state 一致的封存 binding 世代")


def _map_state(
    repository: Path,
    state: dict[str, Any],
    source_bytes: bytes,
    active_binding: dict[str, Any],
    v0_spec: dict[str, Any],
    lifecycle_components: dict[str, dict[str, Any]],
    frozen: dict[str, Any],
    active_binding_dir: Path,
) -> dict[str, Any]:
    result = copy.deepcopy(state)
    result["schema"] = evidence_identity.STATE_SCHEMA
    result["binding"] = copy.deepcopy(active_binding)
    for mode, checkpoint in result["checkpoints"].items():
        dependencies = _mode_dependencies(active_binding, mode)
        checkpoint["evidence_identity"] = evidence_identity.evidence_identity(
            mode, checkpoint["report_sha256"], dependencies,
        )

    v0_components = evidence_identity.build_candidate_components(
        repository, v0_spec["candidate"], v0_spec["binary_sha256"], v0_spec["release_bundle"]["manifest_sha256"],
    )
    v0_binding_dir = (repository / v0_spec["acceptance_workspace"] / "binding").resolve()
    legacy_history_hashes: list[str] = []
    for baseline in result.get("baseline_history", []):
        legacy_history_hashes.append(evidence_identity.canonical_sha256(baseline))
        _require(baseline.get("candidate") == v0_spec["candidate"], "基线历史包含非 V0 候选")
        legacy = _legacy_binding_from_baseline(baseline)
        manifests = _find_manifests(v0_binding_dir, legacy)
        mapped = evidence_identity.build_binding(
            legacy, v0_components, manifests, lifecycle_components,
            evidence_identity.reporting_identities(repository),
        )
        dependencies = {
            "core": evidence_identity.scope_dependencies(mapped, "core"),
            "frontier": evidence_identity.scope_dependencies(mapped, "frontier"),
            "qualification": evidence_identity.scope_dependencies(mapped, "product"),
        }
        baseline.update(evidence_identity.baseline_identity_fields(
            mapped["product"], dependencies, _baseline_report_hashes(baseline),
        ))

    source_acceptance = frozen["acceptance"]
    migration = {
        "schema": evidence_identity.MIGRATION_SCHEMA,
        "source": {
            "state_file_sha256": evidence_identity.file_sha256_bytes(source_bytes),
            "binding_sha256": source_acceptance["binding_sha256"],
            "checkpoints_sha256": source_acceptance["checkpoints_sha256"],
            "baseline_history_record_sha256": legacy_history_hashes,
            "active_pointer_sha256": evidence_identity.file_sha256(active_binding_dir / "active.json"),
        },
        "candidates": {
            "v0": {
                "audit_source_git": v0_spec["candidate"],
                "product": v0_components["product"]["identity"],
                "kernel": v0_components["kernel"]["identity"],
                "kernel_generation": v0_components["kernel-generation"]["identity"],
            },
            "v1": {
                "audit_source_git": frozen["candidates"]["v1"]["candidate"],
                "product": active_binding["product"],
                "kernel": active_binding["components"]["kernel"]["identity"],
                "kernel_generation": active_binding["components"]["kernel-generation"]["identity"],
            },
        },
        "lifecycle_artifacts": {
            name: {"identity": value["identity"], "plan_schema": value["plan_schema"], "mapped_instances": []}
            for name, value in lifecycle_components.items()
        },
        "reporting_artifacts": {
            name: {"identity": value["identity"], "schema": value["schema"]}
            for name, value in evidence_identity.reporting_identities(repository).items()
        },
        "policy": {
            "reports_rewritten": False,
            "evidence_rewritten": False,
            "parallel_state_created": False,
            "git_is_active_identity": False,
            "report_dependencies_exclude_lifecycle_maintenance": True,
            "baseline_contract_shared_with_future_promotion": True,
        },
    }
    migration["identity"] = evidence_identity.canonical_sha256(migration)
    result["identity_migration"] = migration
    return result


def _map_corrected_state(
    repository: Path,
    state: dict[str, Any],
    active_binding: dict[str, Any],
    v0_spec: dict[str, Any],
    lifecycle_components: dict[str, dict[str, Any]],
    frozen: dict[str, Any],
    source: dict[str, Any],
) -> dict[str, Any]:
    result = copy.deepcopy(state)
    result["schema"] = evidence_identity.STATE_SCHEMA
    result["binding"] = copy.deepcopy(active_binding)
    for mode, checkpoint in result["checkpoints"].items():
        checkpoint["evidence_identity"] = evidence_identity.evidence_identity(
            mode, checkpoint["report_sha256"], _mode_dependencies(active_binding, mode),
        )

    v0_components = evidence_identity.build_candidate_components(
        repository, v0_spec["candidate"], v0_spec["binary_sha256"], v0_spec["release_bundle"]["manifest_sha256"],
    )
    v0_binding_dir = (repository / v0_spec["acceptance_workspace"] / "binding").resolve()
    for baseline in result.get("baseline_history", []):
        _require(baseline.get("candidate") == v0_spec["candidate"], "基线历史包含非 V0 候选")
        legacy = _legacy_binding_from_baseline(baseline)
        manifests = _find_manifests(v0_binding_dir, legacy)
        mapped = evidence_identity.build_binding(
            legacy, v0_components, manifests, lifecycle_components,
            evidence_identity.reporting_identities(repository),
        )
        dependencies = {
            "core": evidence_identity.scope_dependencies(mapped, "core"),
            "frontier": evidence_identity.scope_dependencies(mapped, "frontier"),
            "qualification": evidence_identity.scope_dependencies(mapped, "product"),
        }
        baseline.update(evidence_identity.baseline_identity_fields(
            mapped["product"], dependencies, _baseline_report_hashes(baseline),
        ))
    if result.get("baseline") is not None:
        active = result["baseline"]
        dependencies = {
            "core": evidence_identity.scope_dependencies(active_binding, "core"),
            "frontier": evidence_identity.scope_dependencies(active_binding, "frontier"),
            "qualification": evidence_identity.scope_dependencies(active_binding, "product"),
        }
        active.update(evidence_identity.baseline_identity_fields(
            active_binding["product"], dependencies, _baseline_report_hashes(active),
        ))

    migration = {
        "schema": evidence_identity.MIGRATION_SCHEMA,
        "source": copy.deepcopy(source),
        "candidates": {
            "v0": {
                "audit_source_git": v0_spec["candidate"],
                "product": v0_components["product"]["identity"],
                "kernel": v0_components["kernel"]["identity"],
                "kernel_generation": v0_components["kernel-generation"]["identity"],
            },
            "v1": {
                "audit_source_git": frozen["candidates"]["v1"]["candidate"],
                "product": active_binding["product"],
                "kernel": active_binding["components"]["kernel"]["identity"],
                "kernel_generation": active_binding["components"]["kernel-generation"]["identity"],
            },
        },
        "lifecycle_artifacts": {
            name: {"identity": value["identity"], "plan_schema": value["plan_schema"], "mapped_instances": []}
            for name, value in lifecycle_components.items()
        },
        "reporting_artifacts": {
            name: {"identity": value["identity"], "schema": value["schema"]}
            for name, value in evidence_identity.reporting_identities(repository).items()
        },
        "policy": {
            "reports_rewritten": False,
            "evidence_rewritten": False,
            "parallel_state_created": False,
            "git_is_active_identity": False,
            "report_dependencies_exclude_lifecycle_maintenance": True,
            "baseline_contract_shared_with_future_promotion": True,
        },
    }
    migration["identity"] = evidence_identity.canonical_sha256(migration)
    result["identity_migration"] = migration
    return result


def _verify_legacy_state(
    repository: Path,
    state: dict[str, Any],
    state_bytes: bytes,
    binding_dir: Path,
    frozen: dict[str, Any],
) -> None:
    acceptance = frozen.get("acceptance")
    _require(isinstance(acceptance, dict), "冻结基线缺少 Acceptance 投影")
    _require(state.get("schema") == "ownward.acceptance-state/v1", "源状态不是唯一可迁移的 v1")
    binding.validate_binding(state.get("binding"))
    _require(state["binding"].get("schema") == "ownward.acceptance-binding/v4", "源状态已经不是旧提交绑定")
    _require(evidence_identity.file_sha256_bytes(state_bytes) == _direct_file_sha(frozen, "acceptance-unique-state"), "源 state 字节不等于冻结起点")
    _require(evidence_identity.canonical_sha256(state["binding"]) == acceptance["binding_sha256"], "源绑定不等于冻结起点")
    _require(evidence_identity.canonical_sha256(state["checkpoints"]) == acceptance["checkpoints_sha256"], "源检查点不等于冻结起点")
    _require(
        [evidence_identity.canonical_sha256(value) for value in state.get("baseline_history", [])] == acceptance["baseline_history_record_sha256"],
        "源基线历史不等于冻结起点",
    )
    active = binding.load_active_binding(binding_dir)
    _require(active == state["binding"], "源活动绑定与唯一 state 不一致")
    for mode, expected in acceptance["checkpoints"].items():
        checkpoint = state["checkpoints"].get(mode)
        _require(isinstance(checkpoint, dict) and checkpoint.get("report_sha256") == expected, f"源检查点变化: {mode}")
        path = Path(str(checkpoint.get("report_path", "")))
        _require(path.is_file() and evidence_identity.file_sha256(path) == expected, f"源报告变化: {mode}")
    _require(state.get("baseline") is None, "V1 未晋升状态已变化")
    _require(state["binding"]["candidate"] == frozen["candidates"]["v1"]["candidate"], "源活动候选不是冻结 V1")


def _validate_completed(
    repository: Path,
    state_path: Path,
    binding_dir: Path,
    frozen: dict[str, Any],
    expected_state: dict[str, Any],
) -> None:
    state = _load_json(state_path)
    _require(state == expected_state, "身份迁移后的 state 内容漂移")
    migration = state.get("identity_migration")
    _require(isinstance(migration, dict) and migration.get("schema") == evidence_identity.MIGRATION_SCHEMA, "身份迁移审计缺失")
    payload = {name: value for name, value in migration.items() if name != "identity"}
    _require(migration.get("identity") == evidence_identity.canonical_sha256(payload), "身份迁移审计摘要漂移")
    _require(migration["source"]["state_file_sha256"] == _direct_file_sha(frozen, "acceptance-unique-state"), "身份迁移源 state 错绑")
    active = binding.load_active_binding(binding_dir)
    _require(active == state["binding"], "身份迁移后的活动 binding 与唯一 state 不一致")
    contract = _load_json(repository / "benchmarks" / "acceptance" / "suite" / "contract.json")
    lifecycle._validate_state(contract, state)
    for mode in state["checkpoints"]:
        _require(lifecycle.reusable_report(contract, state, mode) is not None, f"迁移后检查点不可复用: {mode}")


def _legacy_binding_from_baseline(baseline: dict[str, Any]) -> dict[str, Any]:
    scopes: dict[str, dict[str, str]] = {}
    for scope in ("core", "frontier", "product"):
        value = baseline["bindings"][scope]
        artifact = value.get("observer_sha256") if scope == "frontier" else value.get("binary_sha256")
        scopes[scope] = {
            "environment_sha256": value["environment_sha256"],
            "input_manifest_sha256": value["input_manifest_sha256"],
            "tool_sha256": value["tool_sha256"],
            "artifact_sha256": artifact,
        }
    legacy = {
        "schema": "ownward.acceptance-binding/v4",
        "suite_version": baseline["bindings"]["core"]["suite_version"],
        "candidate": baseline["candidate"],
        "scopes": scopes,
    }
    binding.validate_binding(legacy)
    return legacy


def _legacy_binding_from_v5_like(value: dict[str, Any]) -> dict[str, Any]:
    scopes: dict[str, dict[str, str]] = {}
    _require(value.get("schema") in {"ownward.acceptance-binding/v5", evidence_identity.BINDING_SCHEMA}, "绑定不是可映射的直接依赖版本")
    for scope, item in value.get("scopes", {}).items():
        report = item.get("report_binding") if isinstance(item, dict) else None
        _require(isinstance(report, dict), f"v5 {scope} 缺少报告绑定")
        scopes[scope] = {
            "environment_sha256": str(report.get("environment_sha256", "")),
            "input_manifest_sha256": str(report.get("input_manifest_sha256", "")),
            "tool_sha256": str(report.get("tool_sha256", "")),
            "artifact_sha256": str(report.get("artifact_sha256", "")),
        }
    legacy = {
        "schema": "ownward.acceptance-binding/v4",
        "suite_version": value.get("suite_version"),
        "candidate": value.get("audit", {}).get("source_git"),
        "scopes": scopes,
    }
    binding.validate_binding(legacy)
    return legacy


def _raw_active_generation_dir(binding_dir: Path) -> Path:
    active_path = binding_dir / "active.json"
    _require(active_path.is_file(), "v2 活动 binding 指针缺失")
    active = _load_json(active_path)
    generation = str(active.get("generation", ""))
    _require(active.get("schema") == "ownward.acceptance-binding-active/v1" and Path(generation).name == generation, "v2 活动 binding 指针无效")
    directory = (binding_dir / "generations" / generation).resolve()
    _require(directory.is_relative_to(binding_dir) and directory.is_dir(), "v2 活动 binding 世代缺失")
    path = directory / "binding.json"
    _require(path.is_file() and evidence_identity.file_sha256(path) == active.get("binding_sha256"), "v2 活动 binding 摘要漂移")
    return directory


def _verify_preserved_results(state: dict[str, Any]) -> None:
    for mode, checkpoint in state.get("checkpoints", {}).items():
        _require(isinstance(checkpoint, dict), f"v2 {mode} 检查点无效")
        path = Path(str(checkpoint.get("report_path", "")))
        _require(path.is_file() and evidence_identity.file_sha256(path) == checkpoint.get("report_sha256"), f"v2 {mode} 报告漂移")
    migration = state["identity_migration"]
    legacy_checkpoints = copy.deepcopy(state.get("checkpoints", {}))
    for checkpoint in legacy_checkpoints.values():
        if isinstance(checkpoint, dict):
            checkpoint.pop("evidence_identity", None)
    _require(
        evidence_identity.canonical_sha256(legacy_checkpoints) == migration["source"].get("checkpoints_sha256"),
        "v2 检查点不再等于原始 v1 来源",
    )
    expected = migration["source"].get("baseline_history_record_sha256")
    actual = []
    for baseline in state.get("baseline_history", []):
        legacy = copy.deepcopy(baseline)
        for field in ("product_identity", "direct_dependencies", "evidence_identities", "identity"):
            legacy.pop(field, None)
        actual.append(evidence_identity.canonical_sha256(legacy))
    _require(actual == expected, "v2 基线历史不再等于原始 v1 来源")


def _baseline_report_hashes(value: dict[str, Any]) -> dict[str, str]:
    return {
        "core": str(value.get("core_report_sha256", "")),
        "frontier": str(value.get("frontier_report_sha256", "")),
        "qualification": str(value.get("qualification_report_sha256", "")),
    }


def _find_manifests(binding_dir: Path, legacy: dict[str, Any]) -> dict[str, dict[str, Any]]:
    candidates = [path for path in (binding_dir / "generations").glob("*") if path.is_dir()]
    if binding_dir.is_dir():
        candidates.append(binding_dir)
    result: dict[str, dict[str, Any]] = {}
    for scope, identities in legacy["scopes"].items():
        for kind, field in (("environment", "environment_sha256"), ("inputs", "input_manifest_sha256"), ("tools", "tool_sha256")):
            filename = f"{scope}-{kind}.json"
            matched = next((directory / filename for directory in candidates if (directory / filename).is_file() and evidence_identity.file_sha256(directory / filename) == identities[field]), None)
            _require(matched is not None, f"找不到基线历史的不可变清单: {filename}/{identities[field]}")
            result[filename] = _load_json(matched)
    return result


def _manifests_from_directory(directory: Path, legacy: dict[str, Any]) -> dict[str, dict[str, Any]]:
    return {
        f"{scope}-{kind}.json": _load_json(directory / f"{scope}-{kind}.json")
        for scope in legacy["scopes"] for kind in ("environment", "inputs", "tools")
    }


def _mode_dependencies(value: dict[str, Any], mode: str) -> dict[str, str]:
    return evidence_identity.scope_dependencies(value, binding.scope_for_mode(mode))


def _direct_file_sha(frozen: dict[str, Any], role: str) -> str:
    match = next((value for value in frozen.get("direct_files", []) if value.get("role") == role), None)
    _require(isinstance(match, dict) and evidence_identity.is_sha256(match.get("sha256")), f"冻结基线缺少 {role}")
    return str(match["sha256"])


def _result(state: dict[str, Any], *, changed: bool, written: bool) -> dict[str, Any]:
    migration = state.get("identity_migration", {})
    return {
        "schema": "ownward.acceptance-identity-migration-result/v1",
        "passed": True,
        "changed": changed,
        "written": written,
        "state_schema": state.get("schema"),
        "binding_schema": state.get("binding", {}).get("schema"),
        "product": state.get("binding", {}).get("product"),
        "checkpoints": sorted(state.get("checkpoints", {})),
        "baseline_history": len(state.get("baseline_history", [])),
        "migration": migration.get("identity"),
    }


def _load_json(path: Path) -> dict[str, Any]:
    return _decode_object(path.read_bytes(), str(path))


def _decode_object(encoded: bytes, label: str) -> dict[str, Any]:
    value = json.loads(encoded.decode("utf-8"))
    _require(isinstance(value, dict), f"{label} 必须是 JSON 对象")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise IdentityMigrationError(message)
