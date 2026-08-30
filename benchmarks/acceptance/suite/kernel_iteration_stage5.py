from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import subprocess
from typing import Any

import evidence_identity


class Stage5FreezeError(ValueError):
    pass


CONTRACT = Path("benchmarks/acceptance/suite/iteration/v2/stage5-internal-validation-contract.json")


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _load(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    _require(isinstance(value, dict), f"JSON 必须是对象: {path}")
    return value


def _identity(value: dict[str, Any]) -> str:
    unsigned = {name: item for name, item in value.items() if name != "identity"}
    return evidence_identity.canonical_sha256(unsigned)


def _go_build(path: Path) -> dict[str, str]:
    completed = subprocess.run(
        ["go", "version", "-m", str(path.resolve())], capture_output=True, text=True,
        encoding="utf-8", timeout=30, check=False,
    )
    _require(completed.returncode == 0, f"无法读取 Go 构建身份: {path}")
    fields: dict[str, str] = {}
    for line in completed.stdout.splitlines():
        parts = line.strip().split("\t")
        if len(parts) >= 2 and parts[0] == "build" and "=" in parts[1]:
            name, value = parts[1].split("=", 1)
            fields[name] = value
    return fields


def freeze(
    suite_root: Path,
    output: Path,
    source_candidate_root: Path,
    rebuilt_candidate_root: Path,
    package: Path,
    production_storage_report: Path,
    frontier_tool: Path,
    formal_state: Path,
) -> dict[str, Any]:
    repository = suite_root.parents[2].resolve()
    contract_path = repository / CONTRACT
    contract = _load(contract_path)
    _require(contract.get("schema") == "ownward.kernel-iteration-stage5-contract/v1", "Stage 5 合同 schema 无效")
    _require(contract.get("identity") == _identity(contract), "Stage 5 合同身份漂移")

    source_candidate_root = source_candidate_root.resolve()
    rebuilt_candidate_root = rebuilt_candidate_root.resolve()
    source_receipt = _load(source_candidate_root / "candidate-receipt.json")
    source_subject = _load(source_candidate_root / "subject.json")
    rebuilt_receipt = _load(rebuilt_candidate_root / "candidate-receipt.json")
    rebuilt_subject = _load(rebuilt_candidate_root / "subject.json")
    candidate = contract["candidate"]
    _require(source_subject.get("identity") == candidate["source_subject_identity"], "Stage 4 subject 身份错绑")
    for name in contract["rebuild_equivalence"]["must_match"]:
        _require(source_receipt.get(name) == rebuilt_receipt.get(name), f"干净重建改变了候选直接事实: {name}")
    _require(rebuilt_receipt.get("kernel_generation_identity") == candidate["kernel_generation_identity"], "干净重建内核世代漂移")
    _require(rebuilt_receipt.get("kernel_effect_identity") == candidate["kernel_effect_identity"], "干净重建内核效果漂移")
    _require(rebuilt_receipt.get("composition_identity") == candidate["composition_identity"], "干净重建组合漂移")
    _require(rebuilt_subject.get("direct_dependencies") == source_subject.get("direct_dependencies"), "干净重建直接依赖漂移")

    component_path = (repository / candidate["component_manifest"]).resolve()
    component_manifest = _load(component_path)
    _require(component_manifest.get("identity") == candidate["component_manifest_identity"], "Stage 5 组件清单错绑")
    _require(component_manifest.get("identity") == _identity(component_manifest), "Stage 5 组件清单身份漂移")
    _require(component_manifest.get("source_subject_identity") == source_subject["identity"], "组件清单未绑定 Stage 4 subject")
    _require(component_manifest.get("runtime_identity") == rebuilt_receipt["kernel_generation_identity"], "组件清单运行身份漂移")
    _require(component_manifest.get("kernel_effect_identity") == rebuilt_receipt["kernel_effect_identity"], "组件清单内核效果漂移")
    _require(component_manifest.get("sealed_composition_identity") == rebuilt_receipt["composition_identity"], "组件清单组合身份漂移")
    _require(component_manifest.get("components") == {
        "access": "bb88b75bd0c9c1999f3eec7f3ff1b42e03960e44baf528bc2d7fbeb8de45736c",
        **rebuilt_subject["direct_dependencies"],
    }, "组件清单直接依赖与封存候选不一致")

    for item in contract["stage4_evidence"]:
        path = (repository / item["path"]).resolve()
        _require(path.is_file() and _sha256(path) == item["sha256"], f"Stage 4 证据缺失或改变: {item['path']}")
        _require(_load(path).get("identity") == item["identity"], f"Stage 4 证据身份错绑: {item['path']}")
    final = _load((repository / contract["stage4_evidence"][1]["path"]).resolve())
    semantic_gate = _load((repository / contract["stage4_evidence"][-1]["path"]).resolve())["semantic_input_tokens"]
    quality = final["quality_and_closed_dimensions"]
    gates = contract["eligibility"]
    _require(quality["development_accuracy"] == gates["development_accuracy"], "开发质量资格不成立")
    _require(quality["regression_accuracy"] == gates["regression_accuracy"], "固定回归资格不成立")
    _require(semantic_gate["candidate_component_tokens"] <= gates["maximum_semantic_component_tokens"], "语义组件成本资格不成立")
    _require(quality["ownward_data_ratio_to_v0"] <= gates["maximum_ownward_data_ratio_to_v0"], "产品数据成本资格不成立")
    _require(quality["retrieval_p95_ms"] <= gates["maximum_consumer_p95_ms"], "完整消费者时延资格不成立")
    _require(final["candidate_controlled_gate"]["candidate_plus_error_seconds"] <= gates["maximum_controlled_wall_seconds"], "候选可控墙钟资格不成立")
    _require(final["resume"]["same_identity_is_byte_exact_and_zero_execution"] is True, "同身份恢复资格不成立")

    binary = rebuilt_candidate_root / "ownward.exe"
    source_build = _go_build(source_candidate_root / "ownward.exe")
    clean_build = _go_build(binary)
    _require(source_build.get("vcs.modified") == "true", "Stage 4 dirty 构建事实未被机械确认")
    _require(clean_build.get("vcs.revision") == contract["audit_source_git"] and clean_build.get("vcs.modified") == "false", "正式候选不是合同绑定的干净源码构建")
    _require(_sha256(binary) == rebuilt_receipt["binary_sha256"], "正式候选二进制与重建收据错绑")
    version = subprocess.run([str(binary), "version"], capture_output=True, text=True, encoding="utf-8", timeout=30, check=False)
    _require(version.returncode == 0 and version.stdout.strip() == candidate["kernel_generation_identity"], "正式候选运行身份错绑")

    package = package.resolve()
    release = _load(package / "manifest.json")
    _require(release.get("candidate") == candidate["kernel_generation_identity"], "发布包候选身份错绑")
    _require(release.get("files", {}).get("bin/ownward.exe") == rebuilt_receipt["binary_sha256"], "发布包二进制错绑")
    production_storage_report = production_storage_report.resolve()
    production = _load(production_storage_report)
    _require(production.get("candidate") == candidate["kernel_generation_identity"], "生产规模证据候选错绑")
    _require(production.get("release_binary_sha256") == rebuilt_receipt["binary_sha256"], "生产规模证据二进制错绑")
    frontier_build = _go_build(frontier_tool.resolve())
    _require(frontier_build.get("vcs.revision") == contract["audit_source_git"] and frontier_build.get("vcs.modified") == "false", "前沿观察器不是同一干净源码构建")

    formal_state = formal_state.resolve()
    formal_state_sha256 = _sha256(formal_state)
    result = {
        "schema": "ownward.kernel-iteration-stage5-freeze/v1",
        "formal": False,
        "contract_identity": contract["identity"],
        "source_subject_identity": source_subject["identity"],
        "rebuilt_packaging_subject_identity": rebuilt_subject["identity"],
        "runtime_identity": candidate["kernel_generation_identity"],
        "kernel_effect_identity": candidate["kernel_effect_identity"],
        "composition_identity": candidate["composition_identity"],
        "component_manifest_identity": candidate["component_manifest_identity"],
        "audit_source_git": contract["audit_source_git"],
        "binary_sha256": rebuilt_receipt["binary_sha256"],
        "frontier_sha256": _sha256(frontier_tool.resolve()),
        "release_manifest_sha256": _sha256(package / "manifest.json"),
        "production_storage_sha256": _sha256(production_storage_report),
        "formal_state_before_sha256": formal_state_sha256,
        "stage4_evidence": [{"identity": item["identity"], "sha256": item["sha256"]} for item in contract["stage4_evidence"]],
        "eligibility_passed": True,
        "packaging_drift": {
            "stage4_binary_was_dirty": True,
            "capability_generation_unchanged": True,
            "clean_binary_subject_is_packaging_provenance_only": True,
        },
    }
    result["identity"] = evidence_identity.canonical_sha256(result)
    output = output.resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    temporary = output.with_suffix(output.suffix + ".tmp")
    temporary.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    temporary.replace(output)
    _require(_sha256(formal_state) == formal_state_sha256, "冻结过程改变了正式 state")
    return result


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise Stage5FreezeError(message)


def _parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Freeze the V2 Stage 5 internal-validation candidate")
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--source-candidate-root", type=Path, required=True)
    parser.add_argument("--rebuilt-candidate-root", type=Path, required=True)
    parser.add_argument("--candidate-package", type=Path, required=True)
    parser.add_argument("--production-storage-report", type=Path, required=True)
    parser.add_argument("--frontier-tool", type=Path, required=True)
    parser.add_argument("--formal-state", type=Path, required=True)
    return parser.parse_args()


def main() -> None:
    args = _parse_args()
    print(json.dumps(freeze(
        Path(__file__).resolve().parent,
        args.output,
        args.source_candidate_root,
        args.rebuilt_candidate_root,
        args.candidate_package,
        args.production_storage_report,
        args.frontier_tool,
        args.formal_state,
    ), ensure_ascii=False))


if __name__ == "__main__":
    main()
