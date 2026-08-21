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

    def test_input_manifest_binds_all_frozen_materials(self) -> None:
        manifest = binding._input_manifest(self.root)
        paths = {item["path"] for item in manifest["files"]}
        self.assertIn("benchmarks/acceptance/suite/contract.json", paths)
        self.assertIn("benchmarks/acceptance/suite/materials/manifest.json", paths)
        self.assertIn("benchmarks/acceptance/suite/materials/core/v1/dataset.json", paths)
        self.assertIn("benchmarks/acceptance/suite/materials/product/v1/dataset.json", paths)
        self.assertTrue(all(len(item["sha256"]) == 64 for item in manifest["files"]))

    def test_tool_manifest_binds_executors_but_not_tests(self) -> None:
        manifest = binding._tool_manifest(self.root)
        paths = {item["path"] for item in manifest["files"]}
        self.assertIn("cmd/ownward-frontier/main.go", paths)
        self.assertIn("benchmarks/acceptance/suite/execution.py", paths)
        self.assertIn("benchmarks/acceptance/suite/adapters/product/verify.py", paths)
        self.assertIn("benchmarks/longmemeval_v2/ownward_trajectory.py", paths)
        self.assertIn("benchmarks/longmemeval_v2/memory_config.active.json", paths)
        self.assertFalse(any(Path(path).name.startswith("test_") for path in paths))

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
        first = binding._input_manifest(self.root, config)
        config["frontier"]["targeted_stages"] = ["relations", "fusion"]
        self.assertEqual(first, binding._input_manifest(self.root, config))

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
            first = binding._input_manifest(self.root, config)
            artifact.write_bytes(b"second")
            second = binding._input_manifest(self.root, config)
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
                "model": {"path": "model/model.gguf", "sha256": digest(model)},
                "runtime": {"files": {"runtime/server.exe": digest(executable)}},
            }), encoding="utf-8")
            codex = root / "codex.exe"
            frontier = root / "frontier.exe"
            codex.write_bytes(b"codex")
            frontier.write_bytes(b"frontier")
            completed = SimpleNamespace(returncode=0, stdout="codex 1.0\n")
            with patch.object(binding.subprocess, "run", return_value=completed):
                environment = binding._environment_manifest(runtime.resolve(), codex, frontier)
                self.assertEqual(2, len(environment["runtime_files"]))
                model.write_bytes(b"changed")
                with self.assertRaisesRegex(binding.BindingError, "摘要不一致"):
                    binding._environment_manifest(runtime.resolve(), codex, frontier)

    def test_execution_config_rejects_unknown_targeted_stage(self) -> None:
        config = {
            "schema": "ownward.acceptance-execution/v1",
            "repository": "repo", "workspace": "workspace", "binding_dir": "binding",
            "frontier": {"tool": "tool", "targeted_stages": ["unknown"]},
            "product": {name: name for name in (
                "binary", "runtime_dir", "package", "production_storage_report", "codex_binary", "codex_auth_file",
                "codex_model", "codex_reasoning_effort",
            )},
            "community": {name: name for name in (
                "official_repo", "binary", "runtime_dir", "codex_binary", "codex_auth_file", "submission_root", "submission_name",
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
