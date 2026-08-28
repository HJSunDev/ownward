from __future__ import annotations

import hashlib
import json
import subprocess
from pathlib import Path
from typing import Any


class EvidenceIdentityError(ValueError):
    pass


BINDING_SCHEMA = "ownward.acceptance-binding/v6"
DEPENDENCY_SCHEMA = "ownward.acceptance-direct-dependencies/v1"
EVIDENCE_SCHEMA = "ownward.acceptance-evidence-identity/v1"
MIGRATION_SCHEMA = "ownward.acceptance-identity-migration/v2"
STATE_SCHEMA = "ownward.acceptance-state/v3"
BASELINE_IDENTITY_SCHEMA = "ownward.acceptance-baseline-identity/v1"

COMPONENT_ROLES = {
    "access",
    "authority-substrate",
    "binary",
    "composition",
    "kernel",
    "kernel-generation",
    "product",
    "product-rules",
    "release",
    "semantic",
    "vector",
    "vector-space",
}

SCOPE_COMPONENTS = {
    # The frontier observer executes frozen semantic/vector fixtures.  Its own
    # artifact plus the kernel effect are the product-side facts it observes;
    # production model and vector-space identities are not consumed.
    "frontier": ("kernel",),
    "core": ("authority-substrate", "kernel", "semantic", "vector", "vector-space", "binary"),
    "product": (
        "product", "authority-substrate", "kernel", "semantic", "vector", "vector-space",
        "access", "composition", "binary", "release",
    ),
    "community": (
        "product", "authority-substrate", "kernel", "semantic", "vector", "vector-space",
        "access", "composition", "binary", "release",
    ),
}

# These files only own binding/checkpoint invalidation. Changing them does not
# change the command, model, scoring, observer, or raw facts that produced a
# report. Their own identity is recorded by the migration receipt instead.
EVIDENCE_LIFECYCLE_ONLY = {
    "benchmarks/acceptance/suite/binding.py",
    "benchmarks/acceptance/suite/evidence_identity.py",
    "benchmarks/acceptance/suite/lifecycle.py",
    "benchmarks/acceptance/suite/lifecycle_error.py",
    "benchmarks/acceptance/suite/state_relationships.py",
}

# The retired facade remains an audit-only classification for sealed historical
# tool manifests. It is not an active source, dependency, or runtime fallback.
LEGACY_EVIDENCE_LIFECYCLE_ONLY = {
    "benchmarks/acceptance/suite/relationships.py",
}


def canonical_sha256(value: Any) -> str:
    encoded = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def file_sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def file_sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def tool_identity(manifest: dict[str, Any]) -> str:
    """Return the report-producing tool identity, excluding Git and lifecycle-only code."""
    _require(isinstance(manifest, dict), "验收工具清单必须是对象")
    scope = manifest.get("scope")
    files = manifest.get("files")
    _require(isinstance(scope, str) and scope, "验收工具清单缺少 scope")
    _require(isinstance(files, list) and files, "验收工具清单缺少文件")
    selected: list[dict[str, str]] = []
    seen: set[str] = set()
    for item in files:
        _require(isinstance(item, dict) and set(item) == {"path", "sha256"}, "验收工具文件身份无效")
        path = str(item["path"])
        digest = str(item["sha256"])
        _require(path not in seen and is_sha256(digest), "验收工具文件重复或摘要无效")
        seen.add(path)
        if path not in EVIDENCE_LIFECYCLE_ONLY and path not in LEGACY_EVIDENCE_LIFECYCLE_ONLY:
            selected.append({"path": path, "sha256": digest})
    _require(selected, "验收工具执行身份不能为空")
    projection: dict[str, Any] = {
        "schema": "ownward.acceptance-tool-identity/v1",
        "scope": scope,
        "files": sorted(selected, key=lambda item: item["path"]),
    }
    return canonical_sha256(projection)


def tool_manifest_identity_valid(manifest: dict[str, Any], expected: str) -> bool:
    return is_sha256(expected) and tool_identity(manifest) == expected


def build_candidate_components(
    repository: Path,
    candidate: str,
    binary_sha256: str,
    release_sha256: str,
    *,
    catalog_path: Path | None = None,
    composition_path: Path | None = None,
) -> dict[str, dict[str, Any]]:
    """Reconstruct content identities; Git is used only to obtain historical bytes."""
    repository = repository.resolve()
    catalog_path = catalog_path or repository / "manifests" / "kernel-generations" / "v1" / "catalog.json"
    composition_path = composition_path or repository / "manifests" / "compositions" / "v1" / "current-collaborative.json"
    catalog = _load_json(catalog_path)
    composition = _load_json(composition_path)
    _require(catalog.get("schema") == "ownward.kernel-generation-catalog/v1", "内核世代目录 schema 无效")
    _require(composition.get("schema") == "ownward.composition/v1", "组合清单 schema 无效")
    generation = next(
        (value for value in catalog.get("generations", []) if value.get("audit", {}).get("source_git") == candidate),
        None,
    )
    _require(isinstance(generation, dict), f"候选没有封存的内核世代: {candidate}")
    kernel = generation.get("kernel")
    dependencies = generation.get("dependencies")
    _require(isinstance(kernel, dict) and isinstance(dependencies, list), "内核世代目录内容无效")
    by_role = {value.get("role"): value for value in dependencies if isinstance(value, dict)}
    for role in ("authority-substrate", "product-rules", "semantic", "vector"):
        _require(isinstance(by_role.get(role), dict) and is_sha256(str(by_role[role].get("identity", ""))), f"内核世代缺少 {role}")

    kernel_effect = _kernel_effect_identity(kernel, generation.get("facets"), by_role)
    access_template = next((value for value in composition.get("components", []) if value.get("role") == "access"), None)
    _require(isinstance(access_template, dict), "组合清单缺少接入组件")
    access = _historical_access_component(repository, candidate, access_template, kernel_effect, by_role["product-rules"]["identity"])
    vector_space = canonical_sha256({
        "schema": "ownward.vector-space-identity/v1",
        "capability": by_role["vector"].get("config", {}).get("capability"),
        "space": by_role["vector"].get("config", {}).get("space"),
        "dimensions": by_role["vector"].get("config", {}).get("dimensions"),
        "vector": by_role["vector"]["identity"],
    })
    component_ids = {
        "authority-substrate": by_role["authority-substrate"]["identity"],
        "kernel": kernel_effect,
        "kernel-generation": kernel["identity"],
        "product-rules": by_role["product-rules"]["identity"],
        "semantic": by_role["semantic"]["identity"],
        "vector": by_role["vector"]["identity"],
        "vector-space": vector_space,
        "access": access["identity"],
        "binary": binary_sha256,
        "release": release_sha256,
    }
    _require(all(is_sha256(str(value)) for value in component_ids.values()), "候选组件身份不完整")
    composition_identity = canonical_sha256({
        "schema": "ownward.evidence-composition-identity/v1",
        "components": {name: component_ids[name] for name in sorted(component_ids) if name not in {"binary", "release"}},
    })
    product_identity = canonical_sha256({
        "schema": "ownward.product-capability-identity/v1",
        "composition": composition_identity,
        "binary": binary_sha256,
        "release": release_sha256,
    })
    component_ids["composition"] = composition_identity
    component_ids["product"] = product_identity
    graph = {
        "authority-substrate": _node(component_ids["authority-substrate"]),
        "product-rules": _node(component_ids["product-rules"]),
        "semantic": _node(component_ids["semantic"]),
        "vector": _node(component_ids["vector"]),
        "vector-space": _node(component_ids["vector-space"], {"vector": component_ids["vector"]}),
        "kernel": _node(component_ids["kernel"], {
            "product-rules": component_ids["product-rules"],
        }),
        "kernel-generation": _node(component_ids["kernel-generation"], {
            "authority-substrate": component_ids["authority-substrate"],
            "kernel": component_ids["kernel"],
            "product-rules": component_ids["product-rules"],
            "semantic": component_ids["semantic"],
            "vector": component_ids["vector"],
        }),
        "access": _node(component_ids["access"], {
            "kernel": component_ids["kernel"], "product-rules": component_ids["product-rules"],
        }),
        "binary": _node(component_ids["binary"]),
        "release": _node(component_ids["release"], {"binary": component_ids["binary"]}),
        "composition": _node(component_ids["composition"], {
            name: component_ids[name]
            for name in ("access", "authority-substrate", "kernel-generation", "product-rules", "semantic", "vector", "vector-space")
        }),
        "product": _node(component_ids["product"], {
            "composition": component_ids["composition"], "binary": component_ids["binary"], "release": component_ids["release"],
        }),
    }
    validate_component_graph(graph)
    return graph


def build_binding_from_legacy_migration(
    legacy: dict[str, Any],
    components: dict[str, dict[str, Any]],
    manifests: dict[str, dict[str, Any]],
    lifecycle: dict[str, dict[str, Any]],
    reporting: dict[str, dict[str, Any]],
) -> dict[str, Any]:
    """Map one sealed v4 report binding during the one-time identity migration."""
    _validate_legacy_binding(legacy)
    return build_current_binding(
        candidate=legacy["candidate"], suite_version=legacy["suite_version"], scopes=legacy["scopes"],
        components=components, manifests=manifests, lifecycle=lifecycle, reporting=reporting,
        audit={"source_git": legacy["candidate"]},
    )


def build_current_binding(
    *,
    candidate: str,
    suite_version: str,
    scopes: dict[str, dict[str, str]],
    components: dict[str, dict[str, Any]],
    manifests: dict[str, dict[str, Any]],
    lifecycle: dict[str, dict[str, Any]],
    reporting: dict[str, dict[str, Any]],
    audit: dict[str, str],
) -> dict[str, Any]:
    """Build the sole active v6 binding without accepting a legacy binding as input."""
    _require(_is_git_commit(candidate), "候选审计来源无效")
    _require(suite_version == "1.0.0", "候选绑定体系版本无效")
    _require(isinstance(scopes, dict) and scopes and set(scopes) <= set(SCOPE_COMPONENTS), "候选绑定范围无效")
    validate_component_graph(components)
    _validate_lifecycle(lifecycle)
    _validate_reporting(reporting)
    result_scopes: dict[str, dict[str, Any]] = {}
    for scope, old in scopes.items():
        _require(
            isinstance(old, dict)
            and set(old) == {"environment_sha256", "input_manifest_sha256", "tool_sha256", "artifact_sha256"}
            and all(is_sha256(str(value)) for value in old.values()),
            f"{scope} 报告绑定摘要无效",
        )
        dependencies = {name: components[name]["identity"] for name in SCOPE_COMPONENTS[scope]}
        dependencies.update({
            "environment": old["environment_sha256"],
            "input": old["input_manifest_sha256"],
            "acceptance-tool": tool_identity(manifests[f"{scope}-tools.json"]),
            "report-reception": reporting["reception"]["identity"],
            "relationship-execution": reporting["relationships"]["identity"],
        })
        if scope == "frontier":
            dependencies["observer"] = old["artifact_sha256"]
        else:
            _require(old["artifact_sha256"] == components["binary"]["identity"], f"{scope} 二进制与产品组件错绑")
        scope_identity = dependency_identity(scope, dependencies)
        result_scopes[scope] = {
            "identity": scope_identity,
            "direct_dependencies": dict(sorted(dependencies.items())),
            "report_binding": {
                "environment_sha256": old["environment_sha256"],
                "input_manifest_sha256": old["input_manifest_sha256"],
                "tool_sha256": old["tool_sha256"],
                "artifact_sha256": old["artifact_sha256"],
            },
        }
    result = {
        "schema": BINDING_SCHEMA,
        "suite_version": suite_version,
        "product": components["product"]["identity"],
        "components": components,
        "lifecycle": lifecycle,
        "reporting": reporting,
        "scopes": result_scopes,
        "audit": dict(audit),
    }
    validate_binding(result)
    return result


def lifecycle_identities(repository: Path) -> dict[str, dict[str, Any]]:
    repository = repository.resolve()
    definitions = {
        "evidence": (
            "ownward.acceptance-evidence-lifecycle/v1",
            [
                "benchmarks/acceptance/suite/binding.py",
                "benchmarks/acceptance/suite/evidence_identity.py",
                "benchmarks/acceptance/suite/lifecycle.py",
                "benchmarks/acceptance/suite/state_relationships.py",
                "benchmarks/acceptance/suite/identity_migration.py",
            ],
        ),
        "stateless": (
            "ownward.stateless-capability-plan/v1",
            [
                "internal/capabilitylifecycle/artifacts.go",
                "internal/capabilitylifecycle/journal.go",
                "internal/capabilitylifecycle/stateless.go",
                "cmd/ownward-composition/main.go",
            ],
        ),
        "derived": (
            "ownward.derived-capability-plan/v1",
            [
                "internal/capabilitylifecycle/derived.go",
                "internal/capabilitylifecycle/derived_journal.go",
                "cmd/ownward-derived-lifecycle/main.go",
            ],
        ),
        "authority": (
            "ownward.authority-persistence-plan/v1",
            [
                "internal/capabilitylifecycle/authority.go",
                "internal/capabilitylifecycle/authority_journal.go",
                "cmd/ownward-authority-lifecycle/main.go",
            ],
        ),
    }
    result: dict[str, dict[str, Any]] = {}
    for name, (schema, files) in definitions.items():
        content = []
        for relative in files:
            path = (repository / relative).resolve()
            _require(path.is_relative_to(repository) and path.is_file(), f"生命周期制品内容缺失: {relative}")
            content.append({"path": relative, "sha256": file_sha256(path)})
        identity = canonical_sha256({"schema": "ownward.lifecycle-artifact-identity/v1", "kind": name, "plan_schema": schema, "content": content})
        result[name] = {"identity": identity, "plan_schema": schema, "content": content}
    return result


def reporting_identities(repository: Path) -> dict[str, dict[str, Any]]:
    repository = repository.resolve()
    definitions = {
        "reception": ("ownward.acceptance-report-reception/v1", "benchmarks/acceptance/suite/report_semantics.py"),
        "relationships": ("ownward.acceptance-report-relationships/v1", "benchmarks/acceptance/suite/report_relationships.py"),
        "summary": ("ownward.acceptance-summary-generation/v1", "benchmarks/acceptance/suite/summary_reporting.py"),
    }
    result: dict[str, dict[str, Any]] = {}
    for name, (schema, relative) in definitions.items():
        path = (repository / relative).resolve()
        _require(path.is_relative_to(repository) and path.is_file(), f"报告语义制品内容缺失: {relative}")
        content = {"path": relative, "sha256": file_sha256(path)}
        identity = canonical_sha256({"schema": schema, "content": content})
        result[name] = {"identity": identity, "schema": schema, "content": content}
    return result


def validate_binding(value: dict[str, Any]) -> None:
    _require(isinstance(value, dict), "候选绑定必须是对象")
    _require(set(value) == {"schema", "suite_version", "product", "components", "lifecycle", "reporting", "scopes", "audit"}, "候选绑定顶层字段无效")
    _require(value.get("schema") == BINDING_SCHEMA and value.get("suite_version") == "1.0.0", "候选绑定 schema 或体系版本无效")
    components = value.get("components")
    _require(isinstance(components, dict) and set(components) == COMPONENT_ROLES, "候选组件身份集合无效")
    validate_component_graph(components)
    _require(value.get("product") == components["product"]["identity"], "候选产品身份错绑")
    _validate_lifecycle(value.get("lifecycle"))
    _validate_reporting(value.get("reporting"))
    audit = value.get("audit")
    _require(isinstance(audit, dict) and set(audit) == {"source_git"}, "候选审计来源无效")
    _require(_is_git_commit(audit["source_git"]), "候选审计来源身份无效")
    scopes = value.get("scopes")
    _require(isinstance(scopes, dict) and scopes and set(scopes) <= set(SCOPE_COMPONENTS), "候选绑定范围无效")
    for name, scope in scopes.items():
        _require(isinstance(scope, dict) and set(scope) == {"identity", "direct_dependencies", "report_binding"}, f"{name} 绑定字段无效")
        dependencies = scope["direct_dependencies"]
        report_binding = scope["report_binding"]
        _require(isinstance(dependencies, dict) and all(isinstance(key, str) and is_sha256(str(item)) for key, item in dependencies.items()), f"{name} 直接依赖无效")
        for component in SCOPE_COMPONENTS[name]:
            _require(dependencies.get(component) == components[component]["identity"], f"{name} 直接依赖错绑: {component}")
        _require(dependencies.get("report-reception") == value["reporting"]["reception"]["identity"], f"{name} 报告接收身份错绑")
        _require(dependencies.get("relationship-execution") == value["reporting"]["relationships"]["identity"], f"{name} 报告关系身份错绑")
        _require(scope["identity"] == dependency_identity(name, dependencies), f"{name} 依赖图身份漂移")
        _require(isinstance(report_binding, dict) and set(report_binding) == {"environment_sha256", "input_manifest_sha256", "tool_sha256", "artifact_sha256"}, f"{name} 报告兼容绑定无效")
        _require(all(is_sha256(str(item)) for item in report_binding.values()), f"{name} 报告兼容摘要无效")
        _require(dependencies.get("environment") == report_binding["environment_sha256"], f"{name} 环境身份错绑")
        _require(dependencies.get("input") == report_binding["input_manifest_sha256"], f"{name} 输入身份错绑")
        if name == "frontier":
            _require(dependencies.get("observer") == report_binding["artifact_sha256"], "观察者身份错绑")
        else:
            _require(report_binding["artifact_sha256"] == components["binary"]["identity"], f"{name} 二进制身份错绑")


def validate_component_graph(components: dict[str, dict[str, Any]]) -> None:
    _require(isinstance(components, dict), "组件身份图必须是对象")
    for role, node in components.items():
        _require(isinstance(role, str) and isinstance(node, dict) and set(node) == {"identity", "direct_dependencies"}, f"组件身份无效: {role}")
        _require(is_sha256(str(node["identity"])), f"组件缺少身份: {role}")
        dependencies = node["direct_dependencies"]
        _require(isinstance(dependencies, dict), f"组件直接依赖无效: {role}")
        for dependency, identity in dependencies.items():
            _require(dependency in components and dependency != role, f"组件依赖缺失或自环: {role}->{dependency}")
            _require(identity == components[dependency]["identity"], f"组件直接依赖错绑: {role}->{dependency}")
    visiting: set[str] = set()
    visited: set[str] = set()

    def visit(role: str) -> None:
        _require(role not in visiting, f"组件依赖存在循环: {role}")
        if role in visited:
            return
        visiting.add(role)
        for dependency in components[role]["direct_dependencies"]:
            visit(dependency)
        visiting.remove(role)
        visited.add(role)

    for role in components:
        visit(role)


def dependency_identity(scope: str, dependencies: dict[str, str]) -> str:
    return canonical_sha256({"schema": DEPENDENCY_SCHEMA, "scope": scope, "direct_dependencies": dict(sorted(dependencies.items()))})


def evidence_identity(kind: str, report_sha256: str, dependencies: dict[str, str]) -> dict[str, Any]:
    _require(is_sha256(report_sha256), "证据报告摘要无效")
    value = {
        "schema": EVIDENCE_SCHEMA,
        "kind": kind,
        "report_sha256": report_sha256,
        "direct_dependencies": dict(sorted(dependencies.items())),
    }
    value["identity"] = canonical_sha256(value)
    return value


def validate_evidence_identity(value: dict[str, Any], *, kind: str, report_sha256: str, dependencies: dict[str, str]) -> None:
    expected = evidence_identity(kind, report_sha256, dependencies)
    _require(value == expected, f"{kind} 证据直接依赖身份不一致")


def baseline_identity_fields(
    product: str,
    dependencies: dict[str, dict[str, str]],
    report_sha256s: dict[str, str],
) -> dict[str, Any]:
    """Build the identity-bearing portion shared by migration and promotion."""
    modes = ("core", "frontier", "qualification")
    _require(is_sha256(product), "基线产品身份无效")
    _require(set(dependencies) == set(modes), "基线直接依赖范围无效")
    _require(set(report_sha256s) == set(modes), "基线报告摘要范围无效")
    normalized: dict[str, dict[str, str]] = {}
    identities: dict[str, dict[str, Any]] = {}
    for mode in modes:
        direct = dependencies[mode]
        _require(
            isinstance(direct, dict)
            and direct
            and all(isinstance(name, str) and is_sha256(str(identity)) for name, identity in direct.items()),
            f"{mode} 基线直接依赖无效",
        )
        normalized[mode] = dict(sorted(direct.items()))
        identities[mode] = evidence_identity(mode, report_sha256s[mode], normalized[mode])
    identity = canonical_sha256({
        "schema": BASELINE_IDENTITY_SCHEMA,
        "product": product,
        "evidence": {mode: identities[mode]["identity"] for mode in modes},
    })
    return {
        "product_identity": product,
        "direct_dependencies": normalized,
        "evidence_identities": identities,
        "identity": identity,
    }


def validate_baseline_identity(value: dict[str, Any], *, binding: dict[str, Any] | None = None) -> None:
    """Validate a sealed baseline without rewriting or normalizing it."""
    _require(isinstance(value, dict), "基线记录必须是对象")
    report_fields = {
        "core": "core_report_sha256",
        "frontier": "frontier_report_sha256",
        "qualification": "qualification_report_sha256",
    }
    report_sha256s = {mode: str(value.get(field, "")) for mode, field in report_fields.items()}
    expected = baseline_identity_fields(
        str(value.get("product_identity", "")),
        value.get("direct_dependencies"),
        report_sha256s,
    )
    for field, expected_value in expected.items():
        _require(value.get(field) == expected_value, f"基线 {field} 身份漂移")
    reports = value.get("reports")
    _require(isinstance(reports, dict) and set(report_fields) <= set(reports), "基线报告摘要缺失")
    candidate = value.get("candidate")
    _require(_is_git_commit(candidate), "基线候选审计来源无效")
    for mode in report_fields:
        sealed = reports[mode]
        _require(
            isinstance(sealed, dict)
            and set(sealed) == {"canonical_sha256", "value"}
            and isinstance(sealed["value"], dict)
            and sealed["canonical_sha256"] == canonical_sha256(sealed["value"]),
            f"{mode} 基线报告摘要漂移",
        )
        _require(sealed["value"].get("candidate") == candidate, f"{mode} 基线报告候选错绑")
    legacy = value.get("bindings")
    _require(isinstance(legacy, dict) and {"core", "frontier", "product"} <= set(legacy), "基线报告绑定缺失")
    for mode, scope in (("core", "core"), ("frontier", "frontier"), ("qualification", "product")):
        report = legacy[scope]
        direct = expected["direct_dependencies"][mode]
        _require(
            isinstance(report, dict)
            and report.get("candidate") == candidate
            and direct.get("environment") == report.get("environment_sha256")
            and direct.get("input") == report.get("input_manifest_sha256"),
            f"{mode} 基线报告绑定与直接依赖不一致",
        )
        artifact_name = "observer" if mode == "frontier" else "binary"
        artifact_field = "observer_sha256" if mode == "frontier" else "binary_sha256"
        _require(direct.get(artifact_name) == report.get(artifact_field), f"{mode} 基线执行制品错绑")
    _require(
        value.get("binary_sha256") == legacy["core"].get("binary_sha256") == legacy["product"].get("binary_sha256"),
        "基线候选二进制错绑",
    )
    if binding is not None:
        validate_binding(binding)
        _require(candidate == source_git(binding), "活动基线候选审计来源不是当前绑定")
        _require(value.get("product_identity") == product_identity(binding), "活动基线产品身份不是当前绑定")
        for mode, scope in (("core", "core"), ("frontier", "frontier"), ("qualification", "product")):
            _require(
                value["direct_dependencies"][mode] == scope_dependencies(binding, scope),
                f"活动基线 {mode} 直接依赖不是当前绑定",
            )


def source_git(value: dict[str, Any]) -> str:
    validate_binding(value)
    return str(value["audit"]["source_git"])


def product_identity(value: dict[str, Any]) -> str:
    validate_binding(value)
    return str(value["product"])


def scope_dependencies(value: dict[str, Any], scope: str) -> dict[str, str]:
    validate_binding(value)
    _require(scope in value["scopes"], f"候选尚未绑定 {scope}")
    return dict(value["scopes"][scope]["direct_dependencies"])


def reporting_identity(value: dict[str, Any], kind: str) -> str:
    validate_binding(value)
    _require(kind in value["reporting"], f"未知报告语义身份: {kind}")
    return str(value["reporting"][kind]["identity"])


def report_binding(value: dict[str, Any], scope: str) -> dict[str, str]:
    validate_binding(value)
    _require(scope in value["scopes"], f"候选尚未绑定 {scope}")
    active = value["scopes"][scope]["report_binding"]
    result = {
        "suite_version": value["suite_version"],
        "candidate": source_git(value),
        "environment_sha256": active["environment_sha256"],
        "input_manifest_sha256": active["input_manifest_sha256"],
        "tool_sha256": active["tool_sha256"],
    }
    if scope == "frontier":
        result["observer_sha256"] = active["artifact_sha256"]
    else:
        result["binary_sha256"] = active["artifact_sha256"]
    return result


def _kernel_effect_identity(kernel: dict[str, Any], facets: Any, dependencies: dict[str, dict[str, Any]]) -> str:
    content = {item["name"]: item["sha256"] for item in kernel.get("content", []) if isinstance(item, dict)}
    config = kernel.get("config", {})
    _require(isinstance(facets, list) and facets, "内核世代缺少 facet")
    values = []
    for facet in facets:
        _require(isinstance(facet, dict) and isinstance(facet.get("name"), str), "内核 facet 无效")
        selected_content = {name: content[name] for name in sorted(facet.get("content", []))}
        selected_config = {name: config[name] for name in sorted(facet.get("config", []))}
        # Authority persistence is deliberately not part of kernel-effect evidence.
        # It remains a direct dependency of core/product invariants and of the full
        # kernel-generation lifecycle identity.
        selected_dependencies = {
            role: dependencies[role]["identity"]
            for role in sorted(facet.get("dependencies", []))
            if role not in {"authority-substrate", "semantic", "vector"}
        }
        values.append({
            "name": facet["name"],
            "content": selected_content,
            "config": selected_config,
            "direct_dependencies": selected_dependencies,
        })
    return canonical_sha256({"schema": "ownward.kernel-effect-identity/v1", "facets": values})


def _historical_access_component(
    repository: Path,
    candidate: str,
    template: dict[str, Any],
    kernel_identity: str,
    rules_identity: str,
) -> dict[str, Any]:
    content = []
    for item in template.get("content", []):
        relative = str(item.get("path", "")).split("#", 1)[0]
        _require(relative, "接入组件内容路径无效")
        completed = subprocess.run(
            ["git", "show", f"{candidate}:{relative}"], cwd=repository,
            capture_output=True, timeout=30, check=False,
        )
        _require(completed.returncode == 0, f"候选接入内容缺失: {relative}")
        encoded = completed.stdout.encode("utf-8") if isinstance(completed.stdout, str) else completed.stdout
        content.append({"name": item["name"], "sha256": hashlib.sha256(encoded).hexdigest()})
    projection = {
        "schema": "ownward.component-identity/v1",
        "role": "access",
        "contracts": template.get("contracts", []),
        "content": sorted(content, key=lambda item: item["name"]),
        "config": template.get("config", {}),
        "dependencies": [
            {"role": "kernel", "identity": kernel_identity},
            {"role": "product-rules", "identity": rules_identity},
        ],
    }
    return {"identity": canonical_sha256(projection), "projection": projection}


def _node(identity: str, dependencies: dict[str, str] | None = None) -> dict[str, Any]:
    return {"identity": identity, "direct_dependencies": dict(sorted((dependencies or {}).items()))}


def _validate_lifecycle(value: Any) -> None:
    _require(
        isinstance(value, dict) and set(value) == {"evidence", "stateless", "derived", "authority"},
        "证据与三类候选生命周期制品身份无效",
    )
    for name, item in value.items():
        _require(isinstance(item, dict) and set(item) == {"identity", "plan_schema", "content"}, f"{name} 生命周期制品无效")
        _require(is_sha256(str(item["identity"])) and isinstance(item["content"], list) and item["content"], f"{name} 生命周期制品身份无效")


def _validate_reporting(value: Any) -> None:
    _require(isinstance(value, dict) and set(value) == {"reception", "relationships", "summary"}, "报告语义制品身份无效")
    for name, item in value.items():
        _require(isinstance(item, dict) and set(item) == {"identity", "schema", "content"}, f"{name} 报告语义制品无效")
        _require(is_sha256(str(item["identity"])) and isinstance(item["schema"], str), f"{name} 报告语义身份无效")
        content = item["content"]
        _require(isinstance(content, dict) and set(content) == {"path", "sha256"} and is_sha256(str(content["sha256"])), f"{name} 报告语义内容无效")
        _require(item["identity"] == canonical_sha256({"schema": item["schema"], "content": content}), f"{name} 报告语义身份漂移")


def _validate_legacy_binding(value: dict[str, Any]) -> None:
    _require(isinstance(value, dict) and set(value) == {"schema", "suite_version", "candidate", "scopes"}, "旧候选绑定字段无效")
    _require(value.get("schema") == "ownward.acceptance-binding/v4" and value.get("suite_version") == "1.0.0", "旧候选绑定 schema 无效")
    _require(_is_git_commit(value.get("candidate")), "旧候选来源提交无效")
    _require(isinstance(value.get("scopes"), dict) and value["scopes"], "旧候选绑定范围无效")
    for scope, item in value["scopes"].items():
        _require(scope in SCOPE_COMPONENTS and isinstance(item, dict), f"旧候选范围无效: {scope}")
        _require(set(item) == {"environment_sha256", "input_manifest_sha256", "tool_sha256", "artifact_sha256"} and all(is_sha256(str(digest)) for digest in item.values()), f"旧候选范围摘要无效: {scope}")


def _load_json(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    _require(isinstance(value, dict), f"JSON 文档必须是对象: {path}")
    return value


def is_sha256(value: Any) -> bool:
    return isinstance(value, str) and len(value) == 64 and all(character in "0123456789abcdef" for character in value)


def _is_git_commit(value: Any) -> bool:
    return isinstance(value, str) and len(value) == 40 and all(character in "0123456789abcdef" for character in value)


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise EvidenceIdentityError(message)
