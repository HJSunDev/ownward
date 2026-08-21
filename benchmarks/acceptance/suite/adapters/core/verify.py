#!/usr/bin/env python3
from __future__ import annotations

import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
from typing import Any
import zipfile

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parents[3]))
sys.path.insert(0, str(HERE.parents[1]))
from support.ownward_mcp import MCPError, OwnwardRuntime  # noqa: E402
import process_control  # noqa: E402

REPORT_SCHEMA = "ownward.core-baseline-report/v1"


def require(condition: bool, message: str) -> None:
    if not condition:
        raise RuntimeError(message)


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    temporary.replace(path)


def load_json(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    require(isinstance(value, dict), f"JSON document is not an object: {path}")
    return value


def run_json(command: list[str], *, expected_success: bool = True, timeout: float = 180) -> tuple[dict[str, Any] | None, subprocess.CompletedProcess[str]]:
    try:
        completed = process_control.run(command, cwd=Path.cwd(), timeout=timeout)
    except process_control.ProcessTimeout as error:
        raise RuntimeError("command exceeded its wall-clock budget and its process tree was stopped") from error
    if expected_success:
        require(completed.returncode == 0, f"command failed: {command!r}\n{completed.stderr[-2000:]}")
        value = json.loads(completed.stdout)
        require(isinstance(value, dict), f"command returned invalid JSON: {command!r}")
        return value, completed
    require(completed.returncode != 0, f"command unexpectedly succeeded: {command!r}")
    return None, completed


def tree_sha256(root: Path) -> str:
    digest = hashlib.sha256()
    for path in sorted(value for value in root.rglob("*") if value.is_file()):
        digest.update(path.relative_to(root).as_posix().encode("utf-8"))
        digest.update(b"\0")
        digest.update(sha256(path).encode("ascii"))
        digest.update(b"\n")
    return digest.hexdigest()


def generation_snapshot(state: Path) -> dict[str, Any]:
    pointer_path = state / "current.json"
    require(pointer_path.is_file(), "derived generation pointer is missing")
    pointer = load_json(pointer_path)
    generation = str(pointer.get("generation", ""))
    directory = state / "generations" / generation
    manifest_path = directory / "manifest.json"
    require(directory.is_dir() and manifest_path.is_file(), "active derived generation is incomplete")
    return {
        "pointer": pointer,
        "pointer_sha256": sha256(pointer_path),
        "manifest": load_json(manifest_path),
        "manifest_sha256": sha256(manifest_path),
        "state_tree_sha256": tree_sha256(state),
        "generation_directories": sorted(path.name for path in (state / "generations").iterdir() if path.is_dir()),
    }


def complete_submission(work: dict[str, Any], *, summary: str, execution: str, inferred_context: bool = False) -> dict[str, Any]:
    analysis: dict[str, Any] = {"summary": summary, "cues": [{"text": "Project Atlas", "kind": "project"}], "topics": ["migration"]}
    if inferred_context:
        analysis["inferred_contexts"] = [
            {"key": "project", "value": "atlas", "confidence": 0.95, "evidence": "Project Atlas"}
        ]
    asset = work["asset"]
    return {
        "schema": "ownward.semantic-submission/v1", "work_id": work["id"], "asset_id": asset["id"],
        "asset_revision": asset["revision"],
        "capability": {"id": "module-lifecycle-controller", "version": "1", "execution": execution},
        "status": "complete", "analysis": analysis,
    }


def uncertain_submission(work: dict[str, Any]) -> dict[str, Any]:
    asset = work["asset"]
    return {
        "schema": "ownward.semantic-submission/v1", "work_id": work["id"], "asset_id": asset["id"],
        "asset_revision": asset["revision"],
        "capability": {"id": "module-lifecycle-controller", "version": "1", "execution": "uncertainty"},
        "status": "uncertain", "uncertainty": "The asset explicitly says the deployment window is not determined.",
        "analysis": {"summary": "Deployment window remains uncertain", "cues": [], "topics": ["deployment"]},
    }


def tool(runtime: OwnwardRuntime, name: str, arguments: dict[str, Any]) -> Any:
    require(runtime.client is not None, "Ownward MCP client is unavailable")
    return runtime.client.call_tool(name, arguments)


def copy_alternate_space_release(binary: Path, target: Path) -> tuple[Path, str]:
    target.mkdir(parents=True)
    copied_binary = target / binary.name
    shutil.copy2(binary, copied_binary)
    source_bundle = binary.parent / "embedding"
    target_bundle = target / "embedding"
    manifest = load_json(source_bundle / "manifest.json")
    for source in source_bundle.rglob("*"):
        relative = source.relative_to(source_bundle)
        destination = target_bundle / relative
        if source.is_dir():
            destination.mkdir(parents=True, exist_ok=True)
            continue
        if relative.as_posix() == "manifest.json":
            continue
        destination.parent.mkdir(parents=True, exist_ok=True)
        try:
            os.link(source, destination)
        except OSError:
            shutil.copy2(source, destination)
    manifest["capability"] = str(manifest["capability"]) + "-lifecycle-alternate"
    space = dict(manifest["space"])
    space["id"] = ""
    binding = {"capability": manifest["capability"], "model": manifest["model"], "runtime": manifest["runtime"], "space": space}
    encoded = json.dumps(binding, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    alternate_space = "emb_" + hashlib.sha256(encoded).hexdigest()[:32]
    manifest["space"]["id"] = alternate_space
    write_json(target_bundle / "manifest.json", manifest)
    return copied_binary, alternate_space


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--repository", type=Path, default=Path("."))
    parser.add_argument("--candidate", required=True)
    parser.add_argument("--runtime-dir", type=Path, required=True)
    parser.add_argument("--evidence-dir", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--suite-version", required=True)
    parser.add_argument("--environment-sha256", required=True)
    parser.add_argument("--input-manifest-sha256", required=True)
    return parser.parse_args()


def main() -> None:
    started_at = datetime.now(timezone.utc).isoformat()
    args = parse_args()
    args.binary = args.binary.resolve()
    args.repository = args.repository.resolve()
    args.runtime_dir = args.runtime_dir.resolve()
    args.evidence_dir = args.evidence_dir.resolve()
    args.output = args.output.resolve()
    require(args.binary.is_file(), "release binary does not exist")
    require(args.runtime_dir.is_dir(), "accepted runtime directory does not exist")
    require(not args.evidence_dir.exists(), "module lifecycle evidence directory must be new")
    version = subprocess.run([str(args.binary), "version"], check=True, capture_output=True, text=True, encoding="utf-8", timeout=30).stdout.strip()
    require(version == args.candidate, "release binary version differs from the candidate")
    bundle_manifest = load_json(args.binary.parent / "embedding" / "manifest.json")
    args.evidence_dir.mkdir(parents=True)
    data = args.evidence_dir / "data"
    environment = dict(os.environ)
    contract: dict[str, Any] = {"schema": "ownward.module-lifecycle-evidence/v1"}

    with OwnwardRuntime(args.binary, data, args.runtime_dir, environment) as runtime:
        created = tool(runtime, "ownward_create_batch", {"items": [
            {
                "content": "Project Atlas migration requires verified backups.",
                "contexts": [{"key": "project", "value": "atlas"}],
                "source": {"actor": "module-lifecycle", "ref": "atlas"},
            },
            {"content": "Project Atlas deployment window remains uncertain.", "source": {"actor": "module-lifecycle", "ref": "window"}},
        ]})
        results = created.get("results")
        require(isinstance(results, list) and len(results) == 2, "batch creation did not return two results")
        asset_ids = [str(value["result"]["information"]["id"]) for value in results]
        works = tool(runtime, "ownward_semantic_work", {"asset_ids": asset_ids}).get("work")
        require(isinstance(works, list) and len(works) == 2, "semantic work was not created for both assets")
        by_id = {str(work["asset"]["id"]): work for work in works}
        complete = complete_submission(by_id[asset_ids[0]], summary="Atlas migration backup requirement", execution="initial", inferred_context=True)
        uncertain = uncertain_submission(by_id[asset_ids[1]])
        accepted = tool(runtime, "ownward_semantic_submit_batch", {"submissions": [complete, uncertain]})
        accepted_results = accepted.get("results")
        require(isinstance(accepted_results, list) and [value.get("organization", {}).get("status") for value in accepted_results] == ["ready", "uncertain"], "complete and uncertain semantic results were not preserved")
        conflicting = json.loads(json.dumps(complete))
        conflicting["analysis"]["summary"] = "Conflicting replacement"
        conflict_rejected = False
        try:
            tool(runtime, "ownward_semantic_submit", {"submission": conflicting})
        except MCPError:
            conflict_rejected = True
        require(conflict_rejected, "conflicting semantic result was accepted")

        updated = tool(runtime, "ownward_update", {"id": asset_ids[0], "expected_revision": 1, "content": "Project Atlas migration requires verified backups and a tested restoration."})
        require(updated["result"]["information"]["revision"] == 2, "asset revision did not advance")
        stale_rejected = False
        try:
            tool(runtime, "ownward_semantic_submit", {"submission": complete})
        except MCPError:
            stale_rejected = True
        require(stale_rejected, "semantic result from the old asset revision was accepted")
        pending = tool(runtime, "ownward_status", {"id": asset_ids[0]})
        require(pending["organization"]["status"] == "pending", "missing semantic capability was not exposed as pending")
        updated_work = tool(runtime, "ownward_semantic_work", {"asset_ids": [asset_ids[0]]})["work"][0]
        updated_submission = complete_submission(updated_work, summary="Atlas backup and restoration requirement", execution="reevaluation", inferred_context=True)
        reevaluated = tool(runtime, "ownward_semantic_submit", {"submission": updated_submission})
        require(reevaluated["organization"]["status"] == "ready", "semantic reevaluation did not recover organization")
        contract.update({"asset_ids": asset_ids, "initial_works": works, "accepted": accepted, "conflict_rejected": conflict_rejected, "stale_rejected": stale_rejected, "pending_without_semantics": pending, "updated_work": updated_work, "reevaluated": reevaluated})

    rebuild_command = [str(args.binary), "rebuild", "--data-dir", str(data), "--runtime-dir", str(args.runtime_dir)]
    first_rebuild, _ = run_json(rebuild_command, timeout=300)
    require(first_rebuild is not None and first_rebuild.get("ready") == 1 and first_rebuild.get("uncertain") == 1, "initial generation rebuild lost semantic state")
    before = generation_snapshot(data / "state")

    naked = args.evidence_dir / ".naked" / args.binary.name
    naked.parent.mkdir()
    shutil.copy2(args.binary, naked)
    _, failed = run_json([str(naked), "rebuild", "--data-dir", str(data), "--runtime-dir", str(args.runtime_dir)], expected_success=False, timeout=120)
    after_failure = generation_snapshot(data / "state")
    require(after_failure["pointer_sha256"] == before["pointer_sha256"], "failed rebuild changed the active generation")
    durable, _ = run_json([str(naked), "read", "--data-dir", str(data), "--runtime-dir", str(args.runtime_dir), "--id", asset_ids[0]])
    require(durable is not None and durable.get("revision") == 2, "embedding failure affected the authoritative asset")
    unavailable, _ = run_json([str(naked), "create", "--data-dir", str(data), "--runtime-dir", str(args.runtime_dir), "--content", "Project Atlas recovery review is scheduled for Friday."])
    require(unavailable is not None and unavailable.get("organization", {}).get("status") == "pending", "embedding outage did not preserve a pending asset")
    unavailable_id = str(unavailable["information"]["id"])
    shutil.rmtree(naked.parent)

    recovered_rebuild, _ = run_json(rebuild_command, timeout=300)
    require(recovered_rebuild is not None and recovered_rebuild.get("pending") == 1, "embedding recovery did not rebuild the pending asset")
    with OwnwardRuntime(args.binary, data, args.runtime_dir, environment) as runtime:
        recovered_work = tool(runtime, "ownward_semantic_work", {"asset_ids": [unavailable_id]})["work"][0]
        recovered_submission = complete_submission(recovered_work, summary="Friday recovery review", execution="embedding-recovery")
        recovered_semantics = tool(runtime, "ownward_semantic_submit", {"submission": recovered_submission})
        require(recovered_semantics["organization"]["status"] == "ready", "recovered embedding did not complete semantic organization")
        contract["embedding_recovery"] = {"work": recovered_work, "result": recovered_semantics}

    alternate_root = args.evidence_dir / ".alternate-release"
    alternate_binary, alternate_space = copy_alternate_space_release(args.binary, alternate_root)
    require(alternate_space != bundle_manifest["space"]["id"], "alternate vector space was not distinct")
    with OwnwardRuntime(alternate_binary, data, args.runtime_dir, environment) as runtime:
        isolated = tool(runtime, "ownward_status", {"id": asset_ids[0]})
        require(isolated["organization"]["status"] == "pending", "old vector space remained active under a changed capability generation")
        contract["space_isolation"] = {"alternate_space": alternate_space, "status": isolated}
    shutil.rmtree(alternate_root)

    final_rebuild, _ = run_json(rebuild_command, timeout=300)
    require(final_rebuild is not None and final_rebuild.get("ready") == 2 and final_rebuild.get("uncertain") == 1, "final capability recovery did not restore all organization states")
    after = generation_snapshot(data / "state")
    require(after["pointer_sha256"] != before["pointer_sha256"], "successful recovery did not switch generations")
    require(len(after["generation_directories"]) == 1, "old derived generations were not reclaimed")
    require(after["manifest"].get("embedding_space") == bundle_manifest["space"]["id"], "active generation is not bound to the release vector space")
    source_information: dict[str, dict[str, Any]] = {}
    with OwnwardRuntime(args.binary, data, args.runtime_dir, environment) as runtime:
        statuses = [tool(runtime, "ownward_status", {"id": value})["organization"]["status"] for value in [*asset_ids, unavailable_id]]
        require(statuses == ["ready", "uncertain", "ready"], "final module states differ after restart")
        search = tool(runtime, "ownward_search", {"query": "Which project needs backup and restoration verification?", "limit": 5})
        require(any("semantic" in item.get("signals", []) for item in search.get("results", [])), "recovered vector capability did not participate in retrieval")
        source_information = {
            value: tool(runtime, "ownward_read", {"id": value})["information"]
            for value in [*asset_ids, unavailable_id]
        }
        contract["final_statuses"] = statuses

    source_asset_files = {
        name: sha256(data / "assets" / name)
        for name in ("manifest.json", "information.jsonl")
    }
    backup_path = args.evidence_dir / "independent-backup.ownward"
    backup_result, _ = run_json(
        [str(args.binary), "backup", "--data-dir", str(data), "--runtime-dir", str(args.runtime_dir), "--output", str(backup_path)],
        timeout=120,
    )
    require(backup_result is not None and Path(str(backup_result.get("backup", ""))).resolve() == backup_path, "production backup did not return the independent archive")
    with zipfile.ZipFile(backup_path) as archive:
        backup_entries = sorted(archive.namelist())
        require(backup_entries == ["backup.json", "information.jsonl", "manifest.json"], "backup contains derived or runtime state")
        require(
            all(archive.read(name) == (data / "assets" / name).read_bytes() for name in ("manifest.json", "information.jsonl")),
            "backup asset bytes differ from the authoritative source",
        )

    restored_data = args.evidence_dir / "blank-restored-data"
    require(not restored_data.exists(), "restore destination was not blank")
    restore_result, _ = run_json(
        [str(args.binary), "restore", "--data-dir", str(restored_data), "--runtime-dir", str(args.runtime_dir), "--backup", str(backup_path)],
        timeout=300,
    )
    require(
        restore_result is not None
        and restore_result.get("organization", {}).get("pending") == 3,
        "blank restore did not expose assets awaiting current semantic reorganization",
    )
    restored_asset_files = {
        name: sha256(restored_data / "assets" / name)
        for name in ("manifest.json", "information.jsonl")
    }
    require(restored_asset_files == source_asset_files, "restored authoritative asset bytes differ from the backup source")
    restored_generation = generation_snapshot(restored_data / "state")
    require(restored_generation["manifest"].get("embedding_space") == bundle_manifest["space"]["id"], "restored generation belongs to another vector space")

    with OwnwardRuntime(args.binary, restored_data, args.runtime_dir, environment) as runtime:
        restored_information = {
            value: tool(runtime, "ownward_read", {"id": value})["information"]
            for value in [*asset_ids, unavailable_id]
        }
        require(restored_information == source_information, "restored information identity, content, revision, or explicit semantics changed")
        restore_initial_statuses = [tool(runtime, "ownward_status", {"id": value})["organization"]["status"] for value in [*asset_ids, unavailable_id]]
        require(restore_initial_statuses == ["pending", "pending", "pending"], "restored assets did not expose the required semantic work")
        restored_works = tool(runtime, "ownward_semantic_work", {"asset_ids": [*asset_ids, unavailable_id]})["work"]
        require(len(restored_works) == 3, "current semantic capability could not obtain every restored work item")
        restored_by_id = {str(work["asset"]["id"]): work for work in restored_works}
        restored_submissions = [
            complete_submission(restored_by_id[asset_ids[0]], summary="Atlas backup and restoration requirement", execution="blank-restore", inferred_context=True),
            uncertain_submission(restored_by_id[asset_ids[1]]),
            complete_submission(restored_by_id[unavailable_id], summary="Friday recovery review", execution="blank-restore"),
        ]
        restored_organization = tool(runtime, "ownward_semantic_submit_batch", {"submissions": restored_submissions})
        require(
            [value.get("organization", {}).get("status") for value in restored_organization.get("results", [])] == ["ready", "uncertain", "ready"],
            "current semantic capability did not rebuild restored organization state",
        )
        restored_statuses = [tool(runtime, "ownward_status", {"id": value})["organization"]["status"] for value in [*asset_ids, unavailable_id]]
        require(restored_statuses == ["ready", "uncertain", "ready"], "restored organization states differ after semantic reorganization")
        rules = tool(runtime, "ownward_rules", {})
        require(isinstance(rules, dict) and str(rules.get("rules", "")).strip(), "restored product did not expose collaboration rules")
        restored_search = tool(runtime, "ownward_search", {"query": "Project Atlas backup and restoration requirement", "limit": 5})
        require(any(item.get("id") == asset_ids[0] for item in restored_search.get("results", [])), "restored assets are not searchable")
        created_after_restore = tool(runtime, "ownward_create", {
            "content": "Project Atlas restored environment requires a quarterly recovery drill.",
            "source": {"actor": "module-lifecycle", "ref": "post-restore"},
        })
        created_id = str(created_after_restore["result"]["information"]["id"])
        created_work = tool(runtime, "ownward_semantic_work", {"asset_ids": [created_id]})["work"][0]
        created_submission = complete_submission(created_work, summary="Quarterly recovery drill", execution="post-restore-create", inferred_context=True)
        created_organized = tool(runtime, "ownward_semantic_submit", {"submission": created_submission})
        require(created_organized["organization"]["status"] == "ready", "restored environment could not organize a new asset")
        updated_after_restore = tool(runtime, "ownward_update", {
            "id": created_id,
            "expected_revision": 1,
            "content": "Project Atlas restored environment requires a monthly recovery drill.",
        })
        require(updated_after_restore["result"]["information"]["revision"] == 2, "restored environment could not update an asset")
        updated_work = tool(runtime, "ownward_semantic_work", {"asset_ids": [created_id]})["work"][0]
        updated_submission = complete_submission(updated_work, summary="Monthly recovery drill", execution="post-restore-update", inferred_context=True)
        updated_organized = tool(runtime, "ownward_semantic_submit", {"submission": updated_submission})
        require(updated_organized["organization"]["status"] == "ready", "restored environment could not reorganize an updated asset")
        updated_read = tool(runtime, "ownward_read", {"id": created_id})["information"]
        require(updated_read["revision"] == 2 and "monthly recovery drill" in updated_read["content"], "restored update was not durably readable")

    with OwnwardRuntime(args.binary, restored_data, args.runtime_dir, environment) as runtime:
        independent_read = tool(runtime, "ownward_read", {"id": created_id})["information"]
        independent_search = tool(runtime, "ownward_search", {"query": "monthly recovery drill", "limit": 5})
        require(
            independent_read["revision"] == 2 and any(item.get("id") == created_id for item in independent_search.get("results", [])),
            "an independent restored session could not use the post-restore mutation",
        )

    recovery_contract = {
        "schema": "ownward.recovery-evidence/v1",
        "source_asset_files": source_asset_files,
        "source_information": source_information,
        "backup_entries": backup_entries,
        "backup_sha256": sha256(backup_path),
        "restore_destination_was_blank": True,
        "restore_result": restore_result,
        "restored_asset_files_before_mutation": restored_asset_files,
        "restored_information_before_mutation": restored_information,
        "restore_initial_statuses": restore_initial_statuses,
        "restored_semantic_work_count": len(restored_works),
        "restored_organization": restored_organization,
        "restored_statuses": restored_statuses,
        "restored_generation": restored_generation,
        "rules_sha256": hashlib.sha256(str(rules["rules"]).encode("utf-8")).hexdigest(),
        "restored_search_ids": [item.get("id") for item in restored_search.get("results", [])],
        "post_restore_mutation": {
            "id": created_id,
            "revision": independent_read["revision"],
            "content_sha256": hashlib.sha256(independent_read["content"].encode("utf-8")).hexdigest(),
            "organization_status": updated_organized["organization"]["status"],
            "independent_search_ids": [item.get("id") for item in independent_search.get("results", [])],
        },
    }

    asset_evidence = args.evidence_dir / "asset-log.jsonl"
    shutil.copy2(data / "assets" / "information.jsonl", asset_evidence)
    before["failed_rebuild"] = {"returncode": failed.returncode, "stderr_sha256": hashlib.sha256(failed.stderr.encode("utf-8")).hexdigest()}
    before["after_failure_pointer_sha256"] = after_failure["pointer_sha256"]
    before["authoritative_asset_after_failure"] = {"id": durable["id"], "revision": durable["revision"], "content_sha256": hashlib.sha256(durable["content"].encode("utf-8")).hexdigest()}
    before_path = args.evidence_dir / "generation-before-failure.json"
    after_path = args.evidence_dir / "generation-after-recovery.json"
    contract_path = args.evidence_dir / "semantic-contract.json"
    recovery_path = args.evidence_dir / "recovery-contract.json"
    write_json(before_path, before)
    write_json(after_path, after)
    write_json(contract_path, contract)
    write_json(recovery_path, recovery_contract)
    shutil.rmtree(data)
    shutil.rmtree(restored_data)
    checks = [
        "derived-generation-atomic-switch", "derived-generation-failure-fallback", "old-generation-reclamation",
        "embedding-capability-generation-consistency", "embedding-space-isolation", "embedding-unavailable-safe-degradation",
        "embedding-recovery", "semantic-work-version-binding", "semantic-provenance-and-evidence", "semantic-uncertainty",
        "semantic-conflict-rejection", "semantic-stale-result-rejection", "semantic-unavailable-safe-degradation",
        "semantic-recovery-reevaluation", "asset-only-independent-backup", "blank-environment-restore",
        "asset-byte-equivalence", "assets-only-derived-rebuild", "post-restore-core-closure",
    ]
    invariant_names = (
        "asset_preservation", "asset_update", "backup_restore", "derived_rebuild",
        "stable_identity", "disposable_derived_state", "replaceable_model",
    )
    report = {
        "schema": REPORT_SCHEMA,
        "suite_version": args.suite_version,
        "candidate": args.candidate,
        "binary_sha256": sha256(args.binary),
        "environment": {"sha256": args.environment_sha256},
        "inputs": {"sha256": args.input_manifest_sha256},
        "invariants": {name: True for name in invariant_names},
        "passed": True,
        "started_at": started_at,
        "finished_at": datetime.now(timezone.utc).isoformat(),
        "details": {
            "release_binary_version": version,
            "embedding_space": bundle_manifest["space"]["id"],
            "evidence": {
            "asset_log": {"path": str(asset_evidence), "sha256": sha256(asset_evidence)},
            "generation_before_failure": {"path": str(before_path), "sha256": sha256(before_path)},
            "generation_after_recovery": {"path": str(after_path), "sha256": sha256(after_path)},
            "semantic_contract": {"path": str(contract_path), "sha256": sha256(contract_path)},
            "backup_archive": {"path": str(backup_path), "sha256": sha256(backup_path)},
            "recovery_contract": {"path": str(recovery_path), "sha256": sha256(recovery_path)},
            },
            "checks": [{"name": name, "passed": True} for name in checks],
        },
    }
    write_json(args.output, report)
    print(json.dumps(report, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
