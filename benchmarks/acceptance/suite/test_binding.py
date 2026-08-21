import unittest
import hashlib
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
from unittest.mock import patch

import binding


class BindingManifestTests(unittest.TestCase):
    def setUp(self) -> None:
        self.root = Path(__file__).resolve().parent

    def test_input_manifests_bind_only_their_frozen_materials(self) -> None:
        frontier = binding._input_manifest(self.root, {}, "frontier")
        core = binding._input_manifest(self.root, {}, "core")
        frontier_paths = {item["path"] for item in frontier["files"]}
        core_paths = {item["path"] for item in core["files"]}
        self.assertIn("benchmarks/acceptance/suite/materials/core/v1/dataset.json", frontier_paths)
        self.assertIn("benchmarks/acceptance/suite/materials/frontier/v1/calibration.json", frontier_paths)
        self.assertIn("benchmarks/acceptance/suite/materials/core/v1/dataset.json", core_paths)
        self.assertNotIn("benchmarks/acceptance/suite/materials/product/v1/dataset.json", frontier_paths | core_paths)
        self.assertNotIn("benchmarks/acceptance/suite/materials/frontier/v1/calibration.json", core_paths)
        self.assertTrue(all(len(item["sha256"]) == 64 for item in frontier["files"] + core["files"]))

    def test_tool_manifest_binds_executors_but_not_tests(self) -> None:
        community = {item["path"] for item in binding._tool_manifest(self.root, "community")["files"]}
        frontier = {item["path"] for item in binding._tool_manifest(self.root, "frontier")["files"]}
        product = {item["path"] for item in binding._tool_manifest(self.root, "product")["files"]}
        self.assertIn("cmd/ownward-frontier/main.go", frontier)
        self.assertIn("benchmarks/acceptance/suite/execution.py", community & frontier & product)
        self.assertIn("benchmarks/acceptance/suite/adapters/product/verify.py", product)
        self.assertIn("benchmarks/longmemeval_v2/ownward_trajectory.py", community)
        self.assertIn("benchmarks/longmemeval_v2/memory_config.active.json", community)
        self.assertNotIn("cmd/ownward-frontier/main.go", community)
        self.assertFalse(any(Path(path).name.startswith("test_") for path in community | frontier | product))

    def test_targeted_scope_is_not_a_frozen_input(self) -> None:
        external = self.root / "contract.json"
        arguments = [
            "--questions-path", str(external), "--haystack-path", str(external),
            "--trajectories-path", str(external), "--memory-config-path", str(external),
            "--output-dir", str(self.root / "ignored"),
        ]
        config = {
            "frontier": {"targeted_stages": ["lexical"]},
            "product": {
                "production_storage_report": str(external),
                "package": str(self.root / "materials" / "core" / "v1"),
                "codex_model": "fixed-model", "codex_reasoning_effort": "fixed-effort",
            },
            "community": {"web_arguments": arguments, "enterprise_arguments": arguments},
        }
        first = binding._input_manifest(self.root, config, "product")
        config["frontier"]["targeted_stages"] = ["relations", "fusion"]
        self.assertEqual(first, binding._input_manifest(self.root, config, "product"))

    def test_input_manifest_binds_actual_release_tree(self) -> None:
        external = self.root / "contract.json"
        arguments = [
            "--questions-path", str(external), "--haystack-path", str(external),
            "--trajectories-path", str(external), "--memory-config-path", str(external),
            "--output-dir", str(self.root / "ignored"),
        ]
        with tempfile.TemporaryDirectory() as directory:
            package = Path(directory)
            (package / "manifest.json").write_text("{}\n", encoding="utf-8")
            artifact = package / "artifact.bin"
            artifact.write_bytes(b"first")
            config = {
                "product": {
                    "production_storage_report": str(external), "package": str(package),
                    "codex_model": "fixed-model", "codex_reasoning_effort": "fixed-effort",
                },
                "community": {"web_arguments": arguments, "enterprise_arguments": arguments},
            }
            first = binding._input_manifest(self.root, config, "product")
            artifact.write_bytes(b"second")
            second = binding._input_manifest(self.root, config, "product")
        self.assertNotEqual(first, second)

    def test_go_binary_must_bind_clean_candidate_source(self) -> None:
        completed = SimpleNamespace(
            returncode=0,
            stdout="\tbuild\tvcs.revision=" + "a" * 40 + "\n\tbuild\tvcs.modified=false\n",
        )
        with patch.object(binding.subprocess, "run", return_value=completed):
            binding._verify_go_binary(Path("candidate.exe"), "a" * 40)
        completed.stdout = completed.stdout.replace("vcs.modified=false", "vcs.modified=true")
        with patch.object(binding.subprocess, "run", return_value=completed):
            with self.assertRaisesRegex(binding.BindingError, "脏工作区"):
                binding._verify_go_binary(Path("candidate.exe"), "a" * 40)

    def test_environment_binding_rehashes_model_and_runtime_artifacts(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            runtime = root / "bundle"
            model = runtime / "model" / "model.gguf"
            executable = runtime / "runtime" / "server.exe"
            model.parent.mkdir(parents=True)
            executable.parent.mkdir(parents=True)
            model.write_bytes(b"model")
            executable.write_bytes(b"runtime")
            digest = lambda path: hashlib.sha256(path.read_bytes()).hexdigest()
            (runtime / "manifest.json").write_text(json.dumps({
                "schema": "ownward.embedding-bundle/v3",
                "model": {"path": "model/model.gguf", "sha256": digest(model)},
                "runtime": {"files": {"runtime/server.exe": digest(executable)}},
            }), encoding="utf-8")
            codex = root / "codex.exe"
            frontier = root / "frontier.exe"
            codex.write_bytes(b"codex")
            frontier.write_bytes(b"frontier")
            completed = SimpleNamespace(returncode=0, stdout="codex 1.0\n")
            with patch.object(binding.subprocess, "run", return_value=completed):
                environment = binding._environment_manifest(runtime.resolve(), codex, frontier, "product")
                self.assertEqual(2, len(environment["runtime_files"]))
                model.write_bytes(b"changed")
                with self.assertRaisesRegex(binding.BindingError, "摘要不一致"):
                    binding._environment_manifest(runtime.resolve(), codex, frontier, "product")

    def test_internal_config_does_not_require_community_inputs(self) -> None:
        config = {
            "schema": "ownward.acceptance-execution/v2",
            "repository": "repo", "workspace": "workspace", "binding_dir": "binding",
            "frontier": {"tool": "tool", "targeted_stages": []},
            "product": {name: name for name in (
                "binary", "embedding_bundle_dir", "package", "production_storage_report", "codex_binary", "codex_auth_file",
            )},
        }
        config["product"].update({
            "codex_model": binding.ACTIVE_CODEX_MODEL,
            "codex_reasoning_effort": binding.ACTIVE_CODEX_REASONING_EFFORT,
        })
        binding.validate_config(config)

    def test_deferred_community_scope_is_required_only_when_used(self) -> None:
        value = {
            "schema": "ownward.acceptance-binding/v3", "suite_version": "1.0.0",
            "candidate": "a" * 40, "binary_sha256": "b" * 64,
            "scopes": {
                name: {"environment_sha256": "c" * 64, "input_manifest_sha256": "d" * 64, "tool_sha256": "e" * 64}
                for name in ("frontier", "core", "product")
            },
        }
        binding.validate_binding(value)
        self.assertEqual("a" * 40, binding.for_mode(value, "qualification")["candidate"])
        with self.assertRaisesRegex(binding.BindingError, "尚未绑定 community"):
            binding.for_mode(value, "longmemeval")

    def test_binding_rejects_unbound_fields_and_invalid_candidate(self) -> None:
        value = {
            "schema": "ownward.acceptance-binding/v3", "suite_version": "1.0.0",
            "candidate": "a" * 40, "binary_sha256": "b" * 64,
            "scopes": {
                name: {"environment_sha256": "c" * 64, "input_manifest_sha256": "d" * 64, "tool_sha256": "e" * 64}
                for name in ("frontier", "core", "product")
            },
        }
        unexpected = dict(value, ignored="not-bound")
        with self.assertRaisesRegex(binding.BindingError, "顶层字段"):
            binding.validate_binding(unexpected)
        invalid_candidate = dict(value, candidate="z" * 40)
        with self.assertRaisesRegex(binding.BindingError, "提交身份"):
            binding.validate_binding(invalid_candidate)
        with self.assertRaisesRegex(binding.BindingError, "必须是对象"):
            binding.validate_binding([])

    def test_internal_binding_never_reads_or_creates_community_scope(self) -> None:
        temporary_root = self.root.parents[2] / ".tmp"
        temporary_root.mkdir(exist_ok=True)
        with tempfile.TemporaryDirectory(dir=temporary_root) as directory:
            root = Path(directory)
            binary = root / "ownward.exe"
            codex = root / "codex.exe"
            frontier = root / "frontier.exe"
            for path in (binary, codex, frontier):
                path.write_bytes(path.name.encode())
            runtime = root / "embedding"
            runtime.mkdir()
            package = root / "package"
            package.mkdir()
            (package / "manifest.json").write_text("{}\n", encoding="utf-8")
            production = root / "production.json"
            production.write_text("{}\n", encoding="utf-8")
            output = root / "binding"
            config = {
                "schema": "ownward.acceptance-execution/v2",
                "repository": str(self.root.parents[2]), "workspace": str(root / "workspace"),
                "binding_dir": str(output),
                "frontier": {"tool": str(frontier), "targeted_stages": []},
                "product": {
                    "binary": str(binary), "embedding_bundle_dir": str(runtime), "package": str(package),
                    "production_storage_report": str(production), "codex_binary": str(codex),
                    "codex_auth_file": str(root / "auth.json"),
                    "codex_model": binding.ACTIVE_CODEX_MODEL,
                    "codex_reasoning_effort": binding.ACTIVE_CODEX_REASONING_EFFORT,
                },
            }
            candidate = "a" * 40
            git_result = lambda _repository, *arguments: candidate if arguments == ("rev-parse", "HEAD") else ""
            version = SimpleNamespace(returncode=0, stdout=candidate + "\n")
            environment = lambda _runtime, _codex, _frontier, scope: {"schema": "fixture", "scope": scope}
            with (
                patch.object(binding, "_git", side_effect=git_result),
                patch.object(binding, "_verify_go_binary"),
                patch.object(binding.subprocess, "run", return_value=version),
                patch.object(binding, "_environment_manifest", side_effect=environment),
            ):
                result = binding.create(self.root, self._write_config(root, config), output)
                self.assertEqual({"frontier", "core", "product"}, set(result["scopes"]))
                self.assertFalse(any(path.name.startswith("community-") for path in output.iterdir()))
                binding.verify_current(self.root, output, config, result, "core")

    @staticmethod
    def _write_config(root: Path, config: dict) -> Path:
        path = root / "execution.json"
        path.write_text(json.dumps(config), encoding="utf-8")
        return path

    def test_execution_config_rejects_unknown_targeted_stage(self) -> None:
        config = {
            "schema": "ownward.acceptance-execution/v2",
            "repository": "repo", "workspace": "workspace", "binding_dir": "binding",
            "frontier": {"tool": "tool", "targeted_stages": ["unknown"]},
            "product": {name: name for name in (
                "binary", "embedding_bundle_dir", "package", "production_storage_report", "codex_binary", "codex_auth_file",
                "codex_model", "codex_reasoning_effort",
            )},
            "community": {name: name for name in (
                "official_repo", "binary", "embedding_bundle_dir", "codex_binary", "codex_auth_file", "submission_root", "submission_name",
            )},
        }
        arguments = [
            "--domain", "web", "--questions-path", "q", "--haystack-path", "h",
            "--trajectories-path", "t", "--memory-config-path", "m", "--output-dir", "o",
        ]
        config["community"]["web_arguments"] = arguments
        config["community"]["enterprise_arguments"] = [
            "enterprise" if item == "web" else item for item in arguments
        ]
        with self.assertRaisesRegex(binding.BindingError, "未知阶段"):
            binding.validate_config(config)

    def test_public_example_freezes_official_longmem_protocol(self) -> None:
        config = binding.load_json(self.root / "execution.example.json")
        binding.validate_config(config)
        for domain in ("web", "enterprise"):
            arguments = config["community"][f"{domain}_arguments"]
            for name, expected in binding.OFFICIAL_LONGMEM_ARGUMENTS.items():
                self.assertEqual(expected, binding._argument_value(arguments, name))


if __name__ == "__main__":
    unittest.main()
