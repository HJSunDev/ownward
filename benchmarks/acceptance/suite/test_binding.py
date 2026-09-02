import unittest
import copy
import hashlib
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
from unittest.mock import patch

import binding
import evidence_identity


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
        self.assertTrue(all(manifests[scope]["schema"] == "ownward.acceptance-tool-manifest/v4" for scope in ("community", "frontier", "core")))
        self.assertEqual("ownward.acceptance-tool-manifest/v5", manifests["product"]["schema"])
        self.assertTrue(all("repository_commit" not in manifest for manifest in manifests.values()))
        self.assertIn("cmd/ownward-frontier/main.go", frontier)
        self.assertIn("benchmarks/acceptance/suite/execution.py", community & frontier & product)
        self.assertIn("benchmarks/acceptance/suite/adapters/product/verify.py", product)
        self.assertIn("benchmarks/acceptance/suite/adapters/product/codex_transport.py", product)
        self.assertIn("benchmarks/acceptance/suite/product_scoring.py", product)
        self.assertIn("benchmarks/longmemeval_s/run.py", community)
        self.assertIn("benchmarks/longmemeval_s/external_intelligence_runtime.py", community)
        self.assertIn("benchmarks/longmemeval_s/opencode_external_intelligence.py", community)
        self.assertIn("benchmarks/longmemeval_s/opencode_mcp_bridge.py", community)
        self.assertIn("benchmarks/support/external_intelligence.py", community)
        self.assertNotIn("benchmarks/support/external-intelligence-runtime.json", community)
        self.assertEqual("opencode-server/v1", manifests["community"]["external_intelligence_selection"]["driver"])
        self.assertIn("benchmarks/longmemeval_s/protocol.json", community)
        self.assertNotIn("benchmarks/acceptance/suite/adapters/product/codex_session.py", community)
        self.assertFalse(any("longmemeval_v2" in path for path in community))
        self.assertIn("benchmarks/support/ownward_mcp.py", core)
        self.assertNotIn("cmd/ownward-frontier/main.go", community)
        self.assertNotIn("benchmarks/acceptance/suite/community.py", frontier | core)
        self.assertNotIn("benchmarks/acceptance/suite/execution_product.py", frontier | core)
        self.assertFalse(any(Path(path).name.startswith("test_") for path in community | frontier | core | product))
        responsibilities = manifests["product"]["responsibilities"]
        raw = {item["path"] for item in responsibilities["raw_execution"]["files"]}
        derivation = {item["path"] for item in responsibilities["derivation"]["files"]}
        self.assertIn("benchmarks/acceptance/suite/execution_product.py", raw)
        self.assertIn("benchmarks/acceptance/suite/adapters/product/verify.py", raw)
        self.assertIn("benchmarks/acceptance/suite/adapters/product/codex_transport.py", raw)
        self.assertIn("benchmarks/acceptance/suite/adapters/product/codex_session.py", derivation)
        self.assertIn("benchmarks/acceptance/suite/product_scoring.py", derivation)
        self.assertNotIn("benchmarks/acceptance/suite/adapters/product/replay.py", derivation)

    def test_community_tool_identity_binds_only_the_selected_provider_adapter(self) -> None:
        config = {"community": {"external_intelligence": {
            "driver": "opencode-server/v1", "binary": "opencode.cmd", "credential_file": "auth.json",
        }}}
        manifest = binding._tool_manifest(self.root, "community", config)
        paths = {item["path"] for item in manifest["files"]}
        self.assertEqual("opencode-server/v1", manifest["external_intelligence_selection"]["driver"])
        self.assertIn("benchmarks/longmemeval_s/opencode_external_intelligence.py", paths)
        self.assertIn("benchmarks/longmemeval_s/opencode_mcp_bridge.py", paths)
        self.assertNotIn("benchmarks/longmemeval_s/codex_app_server.py", paths)
        self.assertNotIn("benchmarks/acceptance/suite/adapters/product/codex_session.py", paths)

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

    def test_legacy_binding_is_not_an_active_binding(self) -> None:
        value = {
            "schema": "ownward.acceptance-binding/v4", "suite_version": "1.0.0",
            "candidate": "a" * 40,
            "scopes": {
                name: {"environment_sha256": "c" * 64, "input_manifest_sha256": "d" * 64, "tool_sha256": "e" * 64, "artifact_sha256": "f" * 64}
                for name in ("frontier", "core", "product")
            },
        }
        with self.assertRaisesRegex(binding.BindingError, "顶层字段"):
            binding.validate_binding(value)

        manifests = {
            "frontier-tools.json": self._tool_fixture("frontier", 1),
        }
        current = self._current_binding("a" * 40, {"frontier": value["scopes"]["frontier"]}, manifests)
        current["schema"] = "ownward.acceptance-binding/v5"
        with self.assertRaisesRegex(binding.BindingError, "schema"):
            binding.validate_binding(current)

    def test_v6_binding_root_without_active_pointer_is_not_an_active_generation(self) -> None:
        temporary_root = self.root.parents[2] / ".tmp"
        temporary_root.mkdir(exist_ok=True)
        with tempfile.TemporaryDirectory(dir=temporary_root) as directory:
            output = Path(directory) / "binding"
            output.mkdir()
            manifests = {
                "frontier-environment.json": {"kind": "environment"},
                "frontier-inputs.json": {"kind": "inputs"},
                "frontier-tools.json": self._tool_fixture("frontier", 1),
            }
            scope = {
                "environment_sha256": binding._serialized_json_sha256(manifests["frontier-environment.json"]),
                "input_manifest_sha256": binding._serialized_json_sha256(manifests["frontier-inputs.json"]),
                "tool_sha256": binding._serialized_json_sha256(manifests["frontier-tools.json"]),
                "artifact_sha256": "f" * 64,
            }
            value = self._current_binding("a" * 40, {"frontier": scope}, manifests)
            (output / "binding.json").write_text(json.dumps(value), encoding="utf-8")
            for name, manifest in manifests.items():
                (output / name).write_text(json.dumps(manifest), encoding="utf-8")
            with self.assertRaisesRegex(binding.BindingError, "活动绑定指针缺失"):
                binding.load_active_binding(output)

    def test_binding_rejects_unbound_fields_and_invalid_candidate(self) -> None:
        manifests = {
            "frontier-environment.json": {"kind": "environment"},
            "frontier-inputs.json": {"kind": "inputs"},
            "frontier-tools.json": self._tool_fixture("frontier", 1),
        }
        scope = {
            "environment_sha256": binding._serialized_json_sha256(manifests["frontier-environment.json"]),
            "input_manifest_sha256": binding._serialized_json_sha256(manifests["frontier-inputs.json"]),
            "tool_sha256": binding._serialized_json_sha256(manifests["frontier-tools.json"]),
            "artifact_sha256": "f" * 64,
        }
        value = self._current_binding("a" * 40, {"frontier": scope}, manifests)
        unexpected = dict(value, ignored="not-bound")
        with self.assertRaisesRegex(binding.BindingError, "顶层字段"):
            binding.validate_binding(unexpected)
        invalid_candidate = copy.deepcopy(value)
        invalid_candidate["audit"]["source_git"] = "z" * 40
        with self.assertRaisesRegex(binding.BindingError, "审计来源"):
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
            candidate = "3e712f22f0529b4eef81b8826f8bb201bf9f6bf8"
            version = SimpleNamespace(returncode=0, stdout=candidate + "\n")
            environment = lambda _config, scope: {"schema": "fixture", "scope": scope}
            with (
                patch.object(binding, "_go_binary_revision", return_value=candidate),
                patch.object(binding, "_verify_go_binary"),
                patch.object(binding.subprocess, "run", return_value=version),
                patch.object(binding, "_environment_manifest", side_effect=environment),
            ):
                result = binding.create(self.root, self._write_config(root, config), output)
                self.assertEqual({"frontier", "core", "product"}, set(result["scopes"]))
                self.assertFalse(any(path.name.startswith("community-") for path in output.iterdir()))
                with patch.object(
                    binding.evidence_identity,
                    "lifecycle_identities",
                    side_effect=AssertionError("report verification must not consult lifecycle maintenance identity"),
                ):
                    binding.verify_current(self.root, output, config, result, "core")
                lifecycle_only = copy.deepcopy(result["lifecycle"])
                lifecycle_only["evidence"]["identity"] = "9" * 64
                with patch.object(binding.evidence_identity, "lifecycle_identities", return_value=lifecycle_only):
                    rebound = binding.rebind_scope(
                        self.root, self._write_config(root, config), output, "product",
                    )
                self.assertEqual(
                    {name: value["identity"] for name, value in result["scopes"].items()},
                    {name: value["identity"] for name, value in rebound["scopes"].items()},
                )
                self.assertEqual("9" * 64, rebound["lifecycle"]["evidence"]["identity"])

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
            candidate = "3e712f22f0529b4eef81b8826f8bb201bf9f6bf8"
            with patch.object(binding, "_go_binary_revision", return_value=candidate), patch.object(binding, "_verify_go_binary"):
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
                "codex_reader_reasoning_effort", "codex_judge_model", "codex_judge_reasoning_effort",
            )},
        }
        with self.assertRaisesRegex(binding.BindingError, "未知阶段"):
            binding.validate_config(config)

    def test_public_example_freezes_official_longmem_protocol(self) -> None:
        config = binding.load_json(self.root / "execution.example.json")
        community = config["community"]
        self.assertEqual("E:\\Ownward\\acceptance\\longmemeval-s\\manifests\\v1.json", community["environment_manifest"])
        self.assertNotIn("driver", community["external_intelligence"])
        self.assertIn("opencode", community["external_intelligence"]["binary"])
        roles = binding.external_intelligence_runtime.role_profile_from_execution(community)
        self.assertEqual("qwen3.8-flash", roles["semantic"]["model"])
        self.assertEqual("xhigh", roles["reader"]["reasoning_effort"])
        self.assertEqual("medium", roles["judge"]["reasoning_effort"])
        self.assertNotIn("judge_api_key_env", community)
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
                "codex_semantic_model": "gpt-5.6-luna", "codex_semantic_reasoning_effort": "low",
                "codex_reader_model": "gpt-5.6-luna", "codex_reader_reasoning_effort": "xhigh",
                "codex_judge_model": "gpt-5.6-terra", "codex_judge_reasoning_effort": "medium",
            }
            binding._validate_community_config(community)
            community["judge_api_key_env"] = "OPENAI_API_KEY"
            with self.assertRaisesRegex(binding.BindingError, "API Key"):
                binding._validate_community_config(community)
            community.pop("judge_api_key_env")
            community["codex_reader_reasoning_effort"] = "medium"
            with self.assertRaisesRegex(binding.BindingError, "Reader"):
                binding._validate_community_config(community)
            community["codex_reader_reasoning_effort"] = "xhigh"
            community["codex_reader_model"] = "different"
            with self.assertRaisesRegex(binding.BindingError, "Reader"):
                binding._validate_community_config(community)

    def test_community_config_accepts_one_complete_explicit_provider_profile(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            launcher = root / "opencode.cmd"
            native = root / "node_modules" / "opencode-ai" / "bin" / "opencode.exe"
            native.parent.mkdir(parents=True)
            launcher.write_bytes(b"launcher")
            native.write_bytes(b"runtime")
            credential = root / "auth.json"
            credential.write_bytes(b"credential")
            manifest = root / "manifest.json"
            manifest.write_text(json.dumps({
                "schema": binding.LONGMEMEVAL_S_ENVIRONMENT_SCHEMA,
                "official": {"code_revision": binding.LONGMEMEVAL_S_CODE_REVISION, "data_revision": binding.LONGMEMEVAL_S_DATA_REVISION},
                "integrity": {"data_sha256": binding.LONGMEMEVAL_S_DATA_SHA256},
            }), encoding="utf-8")
            roles = {
                role: {"model": "qwen3.8-flash", "reasoning_effort": "xhigh"}
                for role in binding.external_intelligence_runtime.EXPLICIT_ROLE_KEYS
            }
            roles["judge"]["reasoning_effort"] = "medium"
            community = {
                "environment_manifest": str(manifest),
                "protocol": str(self.root.parents[1] / "longmemeval_s" / "protocol.json"),
                "output_dir": str(root / "runs"),
                "external_intelligence": {
                    "driver": "opencode-server/v1", "binary": str(launcher),
                    "credential_file": str(credential), "roles": roles,
                },
            }
            binding._validate_community_config(community)
            community["external_intelligence"]["roles"].pop("generator")
            with self.assertRaisesRegex(binding.BindingError, "role profile"):
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
                values = {kind: {"scope": scope, "kind": kind, "version": 1} for kind in ("environment", "inputs")}
                values["tools"] = self._tool_fixture(scope, 1)
                manifests.update({f"{scope}-{kind}.json": value for kind, value in values.items()})
                scopes[scope] = {
                    "environment_sha256": binding._serialized_json_sha256(values["environment"]),
                    "input_manifest_sha256": binding._serialized_json_sha256(values["inputs"]),
                    "tool_sha256": binding._serialized_json_sha256(values["tools"]),
                    "artifact_sha256": "f" * 64,
                }
            previous = self._current_binding(candidate, scopes, manifests)
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
            replacement = self._tool_fixture("product", 2)
            with (
                patch.object(binding, "_verify_go_binary"),
                patch.object(binding.subprocess, "run", return_value=SimpleNamespace(returncode=0, stdout=candidate + "\n")),
                patch.object(binding, "_environment_manifest", return_value=manifests["product-environment.json"]),
                patch.object(binding, "_input_manifest", return_value=manifests["product-inputs.json"]),
                patch.object(binding, "_tool_manifest", return_value=replacement),
                patch.object(binding, "_artifact_sha256", return_value="f" * 64),
            ):
                current = binding.rebind_scope(self.root, config_path, output, "product")
            active = binding._active_generation_dir(output)
            self.assertEqual(candidate, evidence_identity.source_git(current))
            self.assertEqual(previous["scopes"]["core"]["identity"], current["scopes"]["core"]["identity"])
            self.assertEqual(old_core, (active / "core-tools.json").read_bytes())
            self.assertNotEqual(old_active, active)
            self.assertEqual(replacement, json.loads((active / "product-tools.json").read_text(encoding="utf-8")))

    def test_scope_rebind_rejects_a_legacy_root_binding_without_the_one_time_migrator(self) -> None:
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
            with self.assertRaisesRegex(binding.BindingError, "活动绑定指针缺失"):
                binding.load_active_binding(output)
            self.assertFalse((output / "active.json").exists())

    def test_invalid_generation_is_not_published(self) -> None:
        temporary_root = self.root.parents[2] / ".tmp"
        temporary_root.mkdir(exist_ok=True)
        with tempfile.TemporaryDirectory(dir=temporary_root) as directory:
            output = Path(directory) / "binding"
            manifests = {
                "frontier-environment.json": {"kind": "environment"},
                "frontier-inputs.json": {"kind": "inputs"},
                "frontier-tools.json": self._tool_fixture("frontier", 1),
            }
            scope = {
                "environment_sha256": "f" * 64,
                "input_manifest_sha256": "f" * 64,
                "tool_sha256": "f" * 64,
                "artifact_sha256": "f" * 64,
            }
            value = self._current_binding("a" * 40, {"frontier": scope}, manifests)
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
                "frontier-tools.json": self._tool_fixture("frontier", 1),
            }
            scope = {
                "environment_sha256": binding._serialized_json_sha256(manifests["frontier-environment.json"]),
                "input_manifest_sha256": binding._serialized_json_sha256(manifests["frontier-inputs.json"]),
                "tool_sha256": binding._serialized_json_sha256(manifests["frontier-tools.json"]),
                "artifact_sha256": "f" * 64,
            }
            value = self._current_binding("a" * 40, {"frontier": scope}, manifests)
            binding._activate_generation(output, value, manifests)
            active = json.loads((output / "active.json").read_text(encoding="utf-8"))
            generation = output / "generations" / active["generation"]
            (generation / "frontier-tools.json").write_text('{"tampered":true}\n', encoding="utf-8")
            with self.assertRaises(binding.BindingError):
                binding.load_active_binding(output)

    @staticmethod
    def _tool_fixture(scope: str, version: int) -> dict:
        return {
            "schema": "fixture-tool", "scope": scope,
            "files": [{"path": f"fixture/{scope}.py", "sha256": str(version) * 64}],
        }

    def _current_binding(self, candidate: str, scopes: dict, manifests: dict) -> dict:
        components = {
            role: {"identity": ("f" * 64 if role == "binary" else evidence_identity.canonical_sha256({"role": role})), "direct_dependencies": {}}
            for role in evidence_identity.COMPONENT_ROLES
        }
        return evidence_identity.build_current_binding(
            candidate=candidate, suite_version="1.0.0", scopes=scopes,
            components=components, manifests=manifests,
            lifecycle=evidence_identity.lifecycle_identities(self.root.parents[2]),
            reporting=evidence_identity.reporting_identities(self.root.parents[2]),
            audit={"source_git": candidate},
        )


if __name__ == "__main__":
    unittest.main()
