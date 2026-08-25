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
        self.assertNotIn("evidence_layers", frontier["contract"])
        self.assertEqual("ownward-core-baseline/v1", core["contract"]["layer"]["version"])
        self.assertTrue(all(len(item["sha256"]) == 64 for item in frontier["files"] + core["files"]))

    def test_tool_manifest_binds_executors_but_not_tests(self) -> None:
        manifests = {scope: binding._tool_manifest(self.root, scope) for scope in ("community", "frontier", "core", "product")}
        community = {item["path"] for item in manifests["community"]["files"]}
        frontier = {item["path"] for item in manifests["frontier"]["files"]}
        core = {item["path"] for item in manifests["core"]["files"]}
        product = {item["path"] for item in manifests["product"]["files"]}
        self.assertTrue(all(manifest["schema"] == "ownward.acceptance-tool-manifest/v4" for manifest in manifests.values()))
        self.assertTrue(all(len(manifest["repository_commit"]) == 40 for manifest in manifests.values()))
        self.assertIn("cmd/ownward-frontier/main.go", frontier)
        self.assertIn("benchmarks/acceptance/suite/execution.py", community & frontier & product)
        self.assertIn("benchmarks/acceptance/suite/adapters/product/verify.py", product)
        self.assertIn("benchmarks/longmemeval_s/run.py", community)
        self.assertIn("benchmarks/longmemeval_s/protocol.json", community)
        self.assertIn("benchmarks/acceptance/suite/adapters/product/codex_session.py", community)
        self.assertFalse(any("longmemeval_v2" in path for path in community))
        self.assertIn("benchmarks/support/ownward_mcp.py", core)
        self.assertNotIn("cmd/ownward-frontier/main.go", community)
        self.assertNotIn("benchmarks/acceptance/suite/community.py", frontier | core)
        self.assertNotIn("benchmarks/acceptance/suite/execution_product.py", frontier | core)
        self.assertFalse(any(Path(path).name.startswith("test_") for path in community | frontier | core | product))

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
        product_paths = {item["path"] for item in first["files"]}
        self.assertTrue({
            "benchmarks/acceptance/suite/materials/product/v2/dataset.json",
            "benchmarks/acceptance/suite/materials/product/v2/qualification.json",
            "benchmarks/acceptance/suite/materials/product/v2/review.json",
        } <= product_paths)
        self.assertFalse(any("/materials/product/v1/" in f"/{path}" for path in product_paths))
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
            config = {"candidate": {"binary": str(root / "ownward.exe"), "embedding_bundle_dir": str(runtime)}, "product": {"codex_binary": str(codex)}}
            with patch.object(binding.subprocess, "run", return_value=completed):
                environment = binding._environment_manifest(config, "product")
                self.assertEqual(2, len(environment["embedding"]["runtime_files"]))
                model.write_bytes(b"changed")
                with self.assertRaisesRegex(binding.BindingError, "摘要不一致"):
                    binding._environment_manifest(config, "product")

    def test_internal_config_does_not_require_community_inputs(self) -> None:
        config = {
            "schema": "ownward.acceptance-execution/v3",
            "repository": "repo", "workspace": "workspace", "binding_dir": "binding",
            "enabled_scopes": ["core"],
            "candidate": {"binary": "binary", "embedding_bundle_dir": "embedding"},
        }
        binding.validate_config(config)

    def test_deferred_community_scope_is_required_only_when_used(self) -> None:
        value = {
            "schema": "ownward.acceptance-binding/v4", "suite_version": "1.0.0",
            "candidate": "a" * 40,
            "scopes": {
                name: {"environment_sha256": "c" * 64, "input_manifest_sha256": "d" * 64, "tool_sha256": "e" * 64, "artifact_sha256": "f" * 64}
                for name in ("frontier", "core", "product")
            },
        }
        binding.validate_binding(value)
        self.assertEqual("a" * 40, binding.for_mode(value, "qualification")["candidate"])
        with self.assertRaisesRegex(binding.BindingError, "尚未绑定 community"):
            binding.for_mode(value, "longmemeval")

    def test_binding_rejects_unbound_fields_and_invalid_candidate(self) -> None:
        value = {
            "schema": "ownward.acceptance-binding/v4", "suite_version": "1.0.0",
            "candidate": "a" * 40,
            "scopes": {
                name: {"environment_sha256": "c" * 64, "input_manifest_sha256": "d" * 64, "tool_sha256": "e" * 64, "artifact_sha256": "f" * 64}
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
            auth = root / "auth.json"
            for path in (binary, codex, frontier, auth):
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
                "schema": "ownward.acceptance-execution/v3",
                "repository": str(self.root.parents[2]), "workspace": str(root / "workspace"),
                "binding_dir": str(output),
                "enabled_scopes": ["frontier", "core", "product"],
                "candidate": {"binary": str(binary), "embedding_bundle_dir": str(runtime)},
                "frontier": {"tool": str(frontier), "targeted_stages": []},
                "product": {
                    "package": str(package), "production_storage_report": str(production), "codex_binary": str(codex),
                    "codex_auth_file": str(auth),
                    "codex_model": binding.ACTIVE_CODEX_MODEL,
                    "codex_reasoning_effort": binding.ACTIVE_CODEX_REASONING_EFFORT,
                },
            }
            candidate = "a" * 40
            git_result = lambda _repository, *arguments: candidate if arguments == ("rev-parse", "HEAD") else ""
            version = SimpleNamespace(returncode=0, stdout=candidate + "\n")
            environment = lambda _config, scope: {"schema": "fixture", "scope": scope}
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

    def test_frontier_binding_does_not_require_candidate_or_model(self) -> None:
        temporary_root = self.root.parents[2] / ".tmp"
        temporary_root.mkdir(exist_ok=True)
        with tempfile.TemporaryDirectory(dir=temporary_root) as directory:
            root = Path(directory)
            observer = root / "frontier.exe"
            observer.write_bytes(b"observer")
            output = root / "binding"
            config = {
                "schema": "ownward.acceptance-execution/v3",
                "repository": str(self.root.parents[2]), "workspace": str(root / "workspace"), "binding_dir": str(output),
                "enabled_scopes": ["frontier"], "frontier": {"tool": str(observer), "targeted_stages": ["lexical"]},
            }
            candidate = "a" * 40
            git_result = lambda _repository, *arguments: candidate if arguments == ("rev-parse", "HEAD") else ""
            with patch.object(binding, "_git", side_effect=git_result), patch.object(binding, "_verify_go_binary"):
                result = binding.create(self.root, self._write_config(root, config), output)
            self.assertEqual({"frontier"}, set(result["scopes"]))
            self.assertNotIn("binary_sha256", binding.for_mode(result, "frontier"))

    @staticmethod
    def _write_config(root: Path, config: dict) -> Path:
        path = root / "execution.json"
        path.write_text(json.dumps(config), encoding="utf-8")
        return path

    def test_execution_config_rejects_unknown_targeted_stage(self) -> None:
        config = {
            "schema": "ownward.acceptance-execution/v3",
            "repository": "repo", "workspace": "workspace", "binding_dir": "binding",
            "enabled_scopes": ["frontier", "core", "product", "community"],
            "candidate": {"binary": "binary", "embedding_bundle_dir": "embedding"},
            "frontier": {"tool": "tool", "targeted_stages": ["unknown"]},
            "product": {name: name for name in ("package", "production_storage_report", "codex_binary", "codex_auth_file", "codex_model", "codex_reasoning_effort")},
            "community": {name: name for name in (
                "environment_manifest", "protocol", "output_dir", "codex_binary", "codex_auth_file",
                "codex_semantic_model", "codex_semantic_reasoning_effort", "codex_reader_model",
                "codex_reader_reasoning_effort", "judge_api_key_env",
            )},
        }
        with self.assertRaisesRegex(binding.BindingError, "未知阶段"):
            binding.validate_config(config)

    def test_public_example_freezes_official_longmem_protocol(self) -> None:
        config = binding.load_json(self.root / "execution.example.json")
        community = config["community"]
        self.assertEqual("E:\\Ownward\\acceptance\\longmemeval-s\\manifests\\v1.json", community["environment_manifest"])
        self.assertEqual("gpt-5.4-mini", community["codex_semantic_model"])
        self.assertEqual("gpt-5.4", community["codex_reader_model"])
        self.assertEqual("OPENAI_API_KEY", community["judge_api_key_env"])
        protocol = binding.load_json(self.root.parents[1] / "longmemeval_s" / "protocol.json")
        self.assertEqual(binding.LONGMEMEVAL_S_CODE_REVISION, protocol["official"]["code_revision"])
        self.assertEqual(binding.LONGMEMEVAL_S_DATA_SHA256, protocol["official"]["data_sha256"])

    def test_community_config_binds_codex_capabilities_without_reading_judge_credentials(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            codex = root / "codex.exe"
            auth = root / "auth.json"
            codex.write_bytes(b"codex")
            auth.write_text("{}\n", encoding="utf-8")
            manifest = root / "manifest.json"
            manifest.write_text(json.dumps({
                "schema": binding.LONGMEMEVAL_S_ENVIRONMENT_SCHEMA,
                "official": {"code_revision": binding.LONGMEMEVAL_S_CODE_REVISION, "data_revision": binding.LONGMEMEVAL_S_DATA_REVISION},
                "integrity": {"data_sha256": binding.LONGMEMEVAL_S_DATA_SHA256},
            }), encoding="utf-8")
            protocol = self.root.parents[1] / "longmemeval_s" / "protocol.json"
            community = {
                "environment_manifest": str(manifest), "protocol": str(protocol), "output_dir": str(root / "runs"),
                "codex_binary": str(codex), "codex_auth_file": str(auth),
                "codex_semantic_model": "gpt-5.4-mini", "codex_semantic_reasoning_effort": "low",
                "codex_reader_model": "gpt-5.4", "codex_reader_reasoning_effort": "medium",
                "judge_api_key_env": "OPENAI_API_KEY",
            }
            binding._validate_community_config(community)
            community["codex_reader_model"] = "different"
            with self.assertRaisesRegex(binding.BindingError, "Reader"):
                binding._validate_community_config(community)

    def test_scope_rebind_preserves_candidate_and_other_scope_generations(self) -> None:
        temporary_root = self.root.parents[2] / ".tmp"
        temporary_root.mkdir(exist_ok=True)
        with tempfile.TemporaryDirectory(dir=temporary_root) as directory:
            root = Path(directory)
            output = root / "binding"
            binary = root / "ownward.exe"
            binary.write_bytes(b"candidate")
            embedding = root / "embedding"
            embedding.mkdir()
            candidate = "a" * 40
            manifests = {}
            scopes = {}
            for scope in ("frontier", "core", "product"):
                values = {kind: {"scope": scope, "kind": kind, "version": 1} for kind in ("environment", "inputs", "tools")}
                manifests.update({f"{scope}-{kind}.json": value for kind, value in values.items()})
                scopes[scope] = {
                    "environment_sha256": binding._serialized_json_sha256(values["environment"]),
                    "input_manifest_sha256": binding._serialized_json_sha256(values["inputs"]),
                    "tool_sha256": binding._serialized_json_sha256(values["tools"]),
                    "artifact_sha256": "f" * 64,
                }
            previous = {"schema": "ownward.acceptance-binding/v4", "suite_version": "1.0.0", "candidate": candidate, "scopes": scopes}
            binding._activate_generation(output, previous, manifests)
            old_active = binding._active_generation_dir(output)
            old_core = (old_active / "core-tools.json").read_bytes()
            config = {
                "schema": "ownward.acceptance-execution/v3", "repository": str(self.root.parents[2]),
                "workspace": str(root / "workspace"), "binding_dir": str(output),
                "enabled_scopes": ["frontier", "core", "product"],
                "candidate": {"binary": str(binary), "embedding_bundle_dir": str(embedding)},
                "frontier": {"tool": str(binary), "targeted_stages": []},
                "product": {"package": str(root), "production_storage_report": str(binary), "codex_binary": str(binary), "codex_auth_file": str(binary), "codex_model": binding.ACTIVE_CODEX_MODEL, "codex_reasoning_effort": binding.ACTIVE_CODEX_REASONING_EFFORT},
            }
            config_path = self._write_config(root, config)
            replacement = {"scope": "product", "kind": "tools", "version": 2}
            with (
                patch.object(binding, "_git", return_value=""),
                patch.object(binding, "_verify_go_binary"),
                patch.object(binding.subprocess, "run", return_value=SimpleNamespace(returncode=0, stdout=candidate + "\n")),
                patch.object(binding, "_environment_manifest", return_value=manifests["product-environment.json"]),
                patch.object(binding, "_input_manifest", return_value=manifests["product-inputs.json"]),
                patch.object(binding, "_tool_manifest", return_value=replacement),
                patch.object(binding, "_artifact_sha256", return_value="f" * 64),
            ):
                current = binding.rebind_scope(self.root, config_path, output, "product")
            active = binding._active_generation_dir(output)
            self.assertEqual(candidate, current["candidate"])
            self.assertEqual(previous["scopes"]["core"], current["scopes"]["core"])
            self.assertEqual(old_core, (active / "core-tools.json").read_bytes())
            self.assertNotEqual(old_active, active)
            self.assertEqual(replacement, json.loads((active / "product-tools.json").read_text(encoding="utf-8")))

    def test_scope_rebind_preserves_a_legacy_root_binding_as_an_immutable_generation(self) -> None:
        temporary_root = self.root.parents[2] / ".tmp"
        temporary_root.mkdir(exist_ok=True)
        with tempfile.TemporaryDirectory(dir=temporary_root) as directory:
            root = Path(directory)
            output = root / "binding"
            output.mkdir()
            binary = root / "ownward.exe"
            binary.write_bytes(b"candidate")
            embedding = root / "embedding"
            embedding.mkdir()
            candidate = "a" * 40
            manifests = {}
            scopes = {}
            for scope in ("frontier", "core", "product"):
                values = {kind: {"scope": scope, "kind": kind, "version": 1} for kind in ("environment", "inputs", "tools")}
                manifests.update({f"{scope}-{kind}.json": value for kind, value in values.items()})
                scopes[scope] = {
                    "environment_sha256": "0" * 64,
                    "input_manifest_sha256": "0" * 64,
                    "tool_sha256": "0" * 64,
                    "artifact_sha256": "f" * 64,
                }
            for filename, value in manifests.items():
                encoded = (json.dumps(value, ensure_ascii=False, indent=2) + "\n").replace("\n", "\r\n").encode("utf-8")
                (output / filename).write_bytes(encoded)
                scope, kind = filename.removesuffix(".json").split("-", 1)
                field = {"environment": "environment_sha256", "inputs": "input_manifest_sha256", "tools": "tool_sha256"}[kind]
                scopes[scope][field] = binding.sha256(output / filename)
            previous = {"schema": "ownward.acceptance-binding/v4", "suite_version": "1.0.0", "candidate": candidate, "scopes": scopes}
            legacy_binding = (json.dumps(previous, ensure_ascii=False, indent=2) + "\n").replace("\n", "\r\n").encode("utf-8")
            (output / "binding.json").write_bytes(legacy_binding)
            config = {
                "schema": "ownward.acceptance-execution/v3", "repository": str(self.root.parents[2]),
                "workspace": str(root / "workspace"), "binding_dir": str(output),
                "enabled_scopes": ["frontier", "core", "product"],
                "candidate": {"binary": str(binary), "embedding_bundle_dir": str(embedding)},
                "frontier": {"tool": str(binary), "targeted_stages": []},
                "product": {"package": str(root), "production_storage_report": str(binary), "codex_binary": str(binary), "codex_auth_file": str(binary), "codex_model": binding.ACTIVE_CODEX_MODEL, "codex_reasoning_effort": binding.ACTIVE_CODEX_REASONING_EFFORT},
            }
            config_path = self._write_config(root, config)
            replacement = {"scope": "product", "kind": "tools", "version": 2}
            old_product_sha = previous["scopes"]["product"]["tool_sha256"]
            with (
                patch.object(binding, "_git", return_value=""),
                patch.object(binding, "_verify_go_binary"),
                patch.object(binding.subprocess, "run", return_value=SimpleNamespace(returncode=0, stdout=candidate + "\n")),
                patch.object(binding, "_environment_manifest", return_value=manifests["product-environment.json"]),
                patch.object(binding, "_input_manifest", return_value=manifests["product-inputs.json"]),
                patch.object(binding, "_tool_manifest", return_value=replacement),
                patch.object(binding, "_artifact_sha256", return_value="f" * 64),
            ):
                binding.rebind_scope(self.root, config_path, output, "product")
            generations = list((output / "generations").iterdir())
            self.assertEqual(2, len(generations))
            legacy_tools = [path for path in output.rglob("product-tools.json") if binding.sha256(path) == old_product_sha]
            self.assertEqual(1, len(legacy_tools))
            self.assertIn(b"\r\n", legacy_tools[0].read_bytes())
            self.assertEqual(manifests["product-tools.json"], json.loads(legacy_tools[0].read_text(encoding="utf-8")))
            self.assertEqual(replacement, json.loads((binding._active_generation_dir(output) / "product-tools.json").read_text(encoding="utf-8")))

    def test_invalid_generation_is_not_published(self) -> None:
        temporary_root = self.root.parents[2] / ".tmp"
        temporary_root.mkdir(exist_ok=True)
        with tempfile.TemporaryDirectory(dir=temporary_root) as directory:
            output = Path(directory) / "binding"
            manifests = {
                "frontier-environment.json": {"kind": "environment"},
                "frontier-inputs.json": {"kind": "inputs"},
                "frontier-tools.json": {"kind": "tools"},
            }
            scope = {
                "environment_sha256": "f" * 64,
                "input_manifest_sha256": "f" * 64,
                "tool_sha256": "f" * 64,
                "artifact_sha256": "f" * 64,
            }
            value = {"schema": "ownward.acceptance-binding/v4", "suite_version": "1.0.0", "candidate": "a" * 40, "scopes": {"frontier": scope}}
            with self.assertRaisesRegex(binding.BindingError, "摘要不一致"):
                binding._activate_generation(output, value, manifests)
            self.assertEqual([], list((output / "generations").iterdir()))

    def test_active_generation_rejects_a_tampered_manifest(self) -> None:
        temporary_root = self.root.parents[2] / ".tmp"
        temporary_root.mkdir(exist_ok=True)
        with tempfile.TemporaryDirectory(dir=temporary_root) as directory:
            output = Path(directory) / "binding"
            manifests = {
                "frontier-environment.json": {"kind": "environment"},
                "frontier-inputs.json": {"kind": "inputs"},
                "frontier-tools.json": {"kind": "tools"},
            }
            scope = {
                "environment_sha256": binding._serialized_json_sha256(manifests["frontier-environment.json"]),
                "input_manifest_sha256": binding._serialized_json_sha256(manifests["frontier-inputs.json"]),
                "tool_sha256": binding._serialized_json_sha256(manifests["frontier-tools.json"]),
                "artifact_sha256": "f" * 64,
            }
            value = {"schema": "ownward.acceptance-binding/v4", "suite_version": "1.0.0", "candidate": "a" * 40, "scopes": {"frontier": scope}}
            binding._activate_generation(output, value, manifests)
            active = json.loads((output / "active.json").read_text(encoding="utf-8"))
            generation = output / "generations" / active["generation"]
            (generation / "frontier-tools.json").write_text('{"tampered":true}\n', encoding="utf-8")
            with self.assertRaisesRegex(binding.BindingError, "摘要不一致"):
                binding.load_active_binding(output)


if __name__ == "__main__":
    unittest.main()
