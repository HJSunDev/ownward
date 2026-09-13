from __future__ import annotations

import copy
from contextlib import ExitStack
import json
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
import queue
import subprocess
import tempfile
import time
import unittest
from unittest import mock

import codex_app_server as concrete_transport
import run as adapter


class FakeToolClient:
    def __init__(self) -> None:
        self.instructions = "来自当前 Ownward 的协作规则。"
        self.contents: dict[str, str] = {}
        self.evidence: dict[str, tuple[str, str]] = {}
        self.operations: list[tuple[str, list[str]]] = []

    def call_tool(self, name: str, arguments: dict):
        if name == "ownward_create_batch":
            values = []
            for item in arguments["items"]:
                identifier = f"info-{len(self.contents) + 1}"
                self.contents[identifier] = item["content"]
                values.append({"result": {"information": {"id": identifier}}})
            return {"results": values}
        if name == "ownward_search":
            return {"results": [{"id": identifier, "score": 1.0, "signals": ["lexical"]} for identifier in self.contents]}
        if name == "ownward_evidence_search":
            identifier = arguments["source_id"]
            content = self.contents[identifier]
            if len(content) <= 384:
                return {"evidence": []}
            chunks = [content[start:start + 384] for start in range(0, len(content), 384)]
            query_terms = {value.lower() for value in arguments["query"].replace("?", " ").split() if value}
            ranked = sorted(
                enumerate(chunks),
                key=lambda item: (-sum(term in item[1].lower() for term in query_terms), item[0]),
            )[:arguments["limit"]]
            values = []
            for index, chunk in ranked:
                evidence_id = f"evidence:{identifier}:{index}"
                self.evidence[evidence_id] = (identifier, chunk)
                values.append({"id": evidence_id, "source_id": identifier, "content_runes": len(chunk)})
            return {"evidence": values}
        if name == "ownward_evidence_read":
            identifier, content = self.evidence[arguments["id"]]
            return {"evidence": {"id": arguments["id"], "source_id": identifier, "content": content}}
        if name == "ownward_read":
            identifier = arguments["id"]
            return {"information": {"id": identifier, "content": self.contents[identifier]}}
        if name == "ownward_semantic_work":
            self.operations.append((name, list(arguments["asset_ids"])))
            return {"work": [
                {"id": f"work-{identifier}", "asset": {"id": identifier, "revision": 1, "content": self.contents[identifier]}, "candidates": []}
                for identifier in arguments["asset_ids"]
            ]}
        if name == "ownward_semantic_submit_batch":
            self.operations.append((name, [item["asset_id"] for item in arguments["submissions"]]))
            return {"results": [{"result": {"organization": {"status": "ready"}}} for _ in arguments["submissions"]]}
        raise AssertionError(name)

    def list_tools(self):
        return [
            {
                "name": name,
                "description": name,
                "inputSchema": {"type": "object", "additionalProperties": True},
            }
            for name in adapter.ACTIVE_RETRIEVAL_TOOLS
        ]


class FakeRuntime:
    starts = 0
    last_client: FakeToolClient | None = None

    def __init__(self, *_args, **_kwargs) -> None:
        self.client = FakeToolClient()
        FakeRuntime.last_client = self.client
        self.binding = object()

    def __enter__(self):
        FakeRuntime.starts += 1
        return self

    def __exit__(self, *_args) -> None:
        return None


class FakeCodex:
    def semantics(self, work: list[dict], _settings: dict, _stage: Path):
        return ([{"work_id": item["id"], "summary": item["asset"]["content"], "topics": [], "cues": []} for item in work], {
            "input_tokens": 10, "output_tokens": 2, "calls": 1, "attempts": 1, "retries": 0,
            "rate_limit_events": 0, "interrupted_attempts": 0, "wall_seconds": 0.01,
        })

    def semantic_request(self, work: list[dict], settings: dict):
        real = object.__new__(adapter.ExternalIntelligenceCapability)
        return adapter.ExternalIntelligenceCapability.semantic_request(real, work, settings)

    def answer(self, prompt: str, _settings: dict, _stage: Path):
        if "Kyoto" in prompt:
            return "Kyoto", {"input_tokens": 10, "output_tokens": 1}
        return "unknown", {
            "input_tokens": 10, "output_tokens": 1, "calls": 1, "attempts": 1, "retries": 0,
            "rate_limit_events": 0, "interrupted_attempts": 0, "wall_seconds": 0.01,
        }

    def active_answer(self, question: dict, client: FakeToolClient, _reader: dict, retrieval: dict, _stage: Path):
        search = client.call_tool("ownward_search", {"query": question["question"], "limit": retrieval["search_limit_per_call"]})
        source = search["results"][0]["id"]
        read = client.call_tool("ownward_read", {"id": source})["information"]
        answer = "Kyoto" if "Kyoto" in read["content"] else "unknown"
        return answer, {
            "input_tokens": 10, "output_tokens": 1, "calls": 1, "attempts": 1, "retries": 0,
            "rate_limit_events": 0, "interrupted_attempts": 0, "wall_seconds": 0.01,
        }, {
            "mode": "external-agent-progressive/v1", "tool_manifest_identity": "fixture",
            "search_ms": 0.1, "evidence_search_ms": 0.0, "read_ms": 0.1, "total_ms": 0.2,
            "returned": [{"id": source}], "read_ids": [source], "evidence_read_ids": [],
            "read_paths": [{"source_id": source, "mode": "full", "evidence_ids": []}],
            "context_chars": len(read["content"]),
            "limits": {"tool_calls": retrieval["max_tool_calls"], "read_units": retrieval["read_limit"], "context_chars": retrieval["context_max_chars"]},
            "selection_policy": "external-agent-progressive/v1",
            "selection_steps": [
                {"tool": "ownward_search", "success": True, "elapsed_ms": 0.1},
                {"tool": "ownward_read", "success": True, "elapsed_ms": 0.1},
            ],
        }

    def judge(self, prompt: str, _settings: dict, _stage: Path):
        return "Kyoto" in prompt, "yes", {
            "input_tokens": 12, "output_tokens": 1, "calls": 1, "attempts": 1, "retries": 0,
            "rate_limit_events": 0, "interrupted_attempts": 0, "wall_seconds": 0.01,
        }


class DelayedCodex(FakeCodex):
    def semantics(self, work: list[dict], settings: dict, stage: Path):
        first = int(str(work[0]["asset"]["id"]).split("-")[-1])
        time.sleep({1: 0.06, 21: 0.03, 41: 0.0}[first])
        return super().semantics(work, settings, stage)


class FakeTransport:
    def __init__(self, outputs: list[dict] | None = None, error: Exception | None = None) -> None:
        self.outputs = list(outputs or [{"items": ["ok"]}])
        self.error = error
        self.calls = 0

    @property
    def identity(self):
        return {
            "schema": "ownward.external-intelligence-runtime-identity/v2",
            "contract": "ownward.external-intelligence/v1",
            "driver": "fixture/v1",
            "provider": "fixture",
            "transport": "in-process/v1",
            "selection_sha256": "c" * 64,
            "artifact_sha256": "a" * 64,
            "implementation_sha256": "d" * 64,
            "credential_locator_sha256": "b" * 64,
            "credential_content_read": False,
            "max_active": 1,
            "worker_processes": 1,
        }

    def invoke(self, **_kwargs):
        self.calls += 1
        if self.error is not None:
            raise self.error
        value = self.outputs.pop(0)
        return value, {"input_tokens": 1, "cached_input_tokens": 0, "output_tokens": 1, "reasoning_output_tokens": 0}, {
            "transport": "in-process/v1", "server_instance": "fixture", "thread_id": f"thread-{self.calls}",
            "turn_id": f"turn-{self.calls}", "thread_ephemeral": True, "sandbox": "read-only", "status": "completed",
        }

    def diagnostics(self):
        return {"rate_limit_observed": False}


class LongMemEvalSAdapterTests(unittest.TestCase):
    def setUp(self) -> None:
        self.protocol = adapter.load_json(Path(adapter.__file__).with_name("protocol.json"))

    def test_public_source_references_hide_dataset_labels_without_changing_turns(self):
        source = 'answer_family_1'
        turns = [{'role': 'user', 'content': 'Keep the blue original label, not the later green suggestion.'},
                 {'role': 'assistant', 'content': 'The earlier wording remains authoritative.'}]
        content = adapter.session_content(source, '2024-05-16', turns)
        self.assertNotIn(source, content)
        self.assertIn('2024-05-16', content)
        for turn in turns:
            self.assertIn(turn['content'], content)
        reference = adapter.session_reference(source)
        self.assertIn(reference, content)
        self.assertEqual(reference, adapter.session_reference(source))
        self.assertNotEqual(reference, adapter.session_reference('answer_family_2'))

    def test_selected_provider_resume_preserves_results_and_detects_logic_changes(self):
        with tempfile.TemporaryDirectory() as directory, ExitStack() as stack:
            root = Path(directory)
            protocol_path = root / "protocol.json"
            adapter.write_json(protocol_path, self.protocol)
            binary, dataset, evaluator = (root / name for name in ("binary", "dataset", "evaluator"))
            for path in (binary, dataset, evaluator):
                path.write_text("fixture")
            output = root / "run"
            questions = [{"question_id": name} for name in ("old", "pending", "unstarted")]
            live_identity = {"provider": "old", "driver": "same/v1", "contract": "contract", "credential_content_read": False}
            logic = ["v1"]
            calls = []
            def dependencies(**kwargs):
                runtime = kwargs.get("external_intelligence_runtime_identity") or {}
                return {name: runtime.get("provider", "neutral") + logic[0]
                        for name in ("semantic", "retrieval", "reader", "judge", "diagnostic")}
            def process(question, output_dir, *_args):
                calls.append(question["question_id"])
                result = {"question_id": question["question_id"], "complete": True}
                adapter.write_json(output_dir / "questions" / question["question_id"] / "result.json", result)
                return result
            for name, value in {
                "validate_environment": {"value": {"layout": {"runs": str(root)}}, "evaluator": evaluator},
                "validate_protocol": None, "validate_dataset": questions,
            }.items():
                stack.enter_context(mock.patch.object(adapter, name, return_value=value))
            stack.enter_context(mock.patch.object(adapter, "current_runtime_identity", side_effect=lambda **_: dict(live_identity)))
            stack.enter_context(mock.patch.object(adapter, "stage_dependency_identities", side_effect=dependencies))
            stack.enter_context(mock.patch.object(adapter, "process_question", side_effect=process))
            stack.enter_context(mock.patch.object(adapter, "open_external_intelligence_runtime"))
            kwargs = dict(environment_manifest=root / "environment.json", protocol_path=protocol_path,
                          dataset_path=dataset, output_dir=output, binary=binary, embedding=root,
                          external_intelligence_driver="opencode-go-api/v1", external_intelligence_binary=binary,
                          external_intelligence_credential_file=root / "unused.json", candidate="fixture",
                          environment_sha256="fixture", input_manifest_sha256="fixture", tool_sha256="fixture", formal=True)
            adapter.execute(**kwargs, resume=False, question_ids=["old"])
            legacy = adapter.load_json(output / "identity.json")
            legacy.pop("stage_logic_dependencies")
            legacy.pop("sha256")
            adapter.write_json(output / "identity.json", {**legacy, "sha256": adapter.canonical_sha256(legacy)})
            original = (output / "questions/old/result.json").read_bytes()
            live_identity["provider"] = "new"
            report = adapter.execute(**kwargs, resume=True, question_ids=["pending"], resume_compatible_transport=True)
            self.assertTrue(report["complete"])
            self.assertFalse(report["full_report_published"])
            self.assertEqual(["old", "pending"], calls)
            self.assertEqual(original, (output / "questions/old/result.json").read_bytes())
            self.assertFalse((output / "questions/unstarted").exists())
            self.assertFalse((output / "report.json").exists())
            # A later full resume accepts the established namespace without reimporting old work.
            with mock.patch.object(adapter, "complete_report", return_value={"cached": True}):
                self.assertEqual({"cached": True}, adapter.execute(**kwargs, resume=True))
            identity_before = (output / "identity.json").read_bytes()
            logic[0] = "v2"
            with self.assertRaisesRegex(adapter.AdapterError, "execution logic changed"):
                adapter.execute(**kwargs, resume=True, question_ids=["pending"], resume_compatible_transport=True)
            self.assertEqual(identity_before, (output / "identity.json").read_bytes())

    def test_selected_questions_rejects_unknown_and_duplicate_ids(self):
        questions = [{"question_id": "a"}, {"question_id": "b"}]
        self.assertEqual([questions[1]], adapter.selected_questions(questions, ["b"]))
        for invalid in ([], ["a", "a"], ["missing"]):
            with self.assertRaises(adapter.AdapterError):
                adapter.selected_questions(questions, invalid)

    def test_failure_batch_stop_drains_inflight_and_keeps_service_errors_separate(self):
        with tempfile.TemporaryDirectory() as directory, ExitStack() as stack:
            root = Path(directory)
            protocol = copy.deepcopy(self.protocol)
            protocol['execution']['max_workers'] = 2
            adapter.write_json(root/'protocol.json', protocol)
            for name in ('binary', 'dataset', 'evaluator'):
                (root/name).write_text('fixture')
            questions = [{'question_id': str(i)} for i in range(20)]
            started = []
            def process(question, output, *_args):
                identifier = question['question_id']
                started.append(identifier)
                if identifier == '0':
                    raise RuntimeError('temporary service failure')
                time.sleep(.01)
                result = {'question_id': identifier, 'complete': True, 'autoeval_label': {'label': False}}
                adapter.write_json(output/'questions'/identifier/'result.json', result)
                return result
            for name, value in {
                'validate_environment': {'value': {'layout': {'runs': str(root)}}, 'evaluator': root/'evaluator'},
                'validate_protocol': None, 'validate_dataset': questions,
                'current_runtime_identity': {'provider': 'fixture'},
                'stage_dependency_identities': {name: 'fixture' for name in ('semantic','retrieval','reader','judge','diagnostic')},
            }.items():
                stack.enter_context(mock.patch.object(adapter, name, return_value=value))
            stack.enter_context(mock.patch.object(adapter, 'process_question', side_effect=process))
            stack.enter_context(mock.patch.object(adapter, 'open_external_intelligence_runtime'))
            result = adapter.execute(environment_manifest=root/'environment',protocol_path=root/'protocol.json',
                dataset_path=root/'dataset',output_dir=root/'run',binary=root/'binary',embedding=root,
                external_intelligence_driver='opencode-go-api/v1',external_intelligence_binary=root/'binary',
                external_intelligence_credential_file=root/'unused',candidate='fixture',environment_sha256='fixture',
                input_manifest_sha256='fixture',tool_sha256='fixture',formal=True,resume=False,stop_after_failures=2)
            self.assertEqual('failure_batch_ready', result['reason'])
            self.assertIn('0', result['execution_failures'])
            self.assertNotIn('0', result['wrong_question_ids'])
            self.assertGreaterEqual(len(result['wrong_question_ids']), 2)
            self.assertLessEqual(len(result['wrong_question_ids']), 3)
            self.assertEqual(len(started)-1, result['completed'])
            self.assertLess(len(started), len(questions))
            self.assertFalse((root/'run/report.json').exists())
            for identifier in started:
                if identifier != '0':
                    self.assertTrue((root/'run/questions'/identifier/'result.json').is_file())

    def test_transport_recovery_archives_only_incomplete_attempts(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            failed = root / "questions/pending/reader/codex/attempt-001/error.txt"
            failed.parent.mkdir(parents=True)
            failed.write_text("HTTP 429")
            complete = root / "questions/pending/judge/codex/complete.json"
            adapter.write_json(complete, {"completed": True})
            done = root / "questions/done/result.json"
            adapter.write_json(done, {"complete": True})
            request_only = root / "questions/interrupted/reader/codex/request.json"
            adapter.write_json(request_only, {"provider": "old"})
            adapter.archive_incomplete_transport_attempts(root, ["pending", "done", "interrupted"], "old")
            self.assertFalse(failed.exists())
            self.assertEqual("HTTP 429", (root / "_audit/old/transport-resume/questions/pending/reader/codex/attempt-001/error.txt").read_text())
            self.assertTrue(complete.is_file())
            self.assertTrue(done.is_file())
            self.assertFalse(request_only.exists())
            self.assertTrue((root / "_audit/old/transport-resume/questions/interrupted/reader/codex/request.json").is_file())

    def test_question_failure_preserves_other_work_without_publishing_partial_score(self) -> None:
        for formal in (False, True):
            with self.subTest(formal=formal), tempfile.TemporaryDirectory() as directory, ExitStack() as stack:
                root = Path(directory)
                protocol = copy.deepcopy(self.protocol)
                protocol["execution"]["max_workers"] = 1
                protocol_path = root / "protocol.json"
                adapter.write_json(protocol_path, protocol)
                binary, dataset, evaluator = (root / name for name in ("binary", "dataset", "evaluator"))
                for path in (binary, dataset, evaluator):
                    path.write_text("fixture", encoding="utf-8")
                output = root / "run"
                questions = [{"question_id": name, "haystack_session_ids": []} for name in ("failed", "good")]
                completed = []

                def process(question, output_dir, *_args):
                    if question["question_id"] == "failed":
                        raise adapter.AdapterError("Go API HTTP 500")
                    value = {"question_id": "good", "complete": True}
                    adapter.write_json(output_dir / "questions/good/result.json", value)
                    completed.append("good")
                    return value

                replacements = {
                    "validate_environment": {"value": {"layout": {"runs": str(root)}}, "evaluator": evaluator},
                    "validate_protocol": None,
                    "validate_dataset": questions,
                    "current_runtime_identity": {"provider": "fixture"},
                    "stage_dependency_identities": {"semantic": "fixture"},
                }
                for name, value in replacements.items():
                    stack.enter_context(mock.patch.object(adapter, name, return_value=value))
                stack.enter_context(mock.patch.object(adapter, "process_question", side_effect=process))
                stack.enter_context(mock.patch.object(adapter, "open_external_intelligence_runtime"))
                with self.assertRaisesRegex(adapter.AdapterError, "question execution failures"):
                    adapter.execute(
                        environment_manifest=root / "environment.json", protocol_path=protocol_path,
                        dataset_path=dataset, output_dir=output, binary=binary, embedding=root,
                        external_intelligence_driver="opencode-go-api/v1", external_intelligence_binary=binary,
                        external_intelligence_credential_file=root / "unused-auth.json", candidate="fixture",
                        environment_sha256="fixture", input_manifest_sha256="fixture", tool_sha256="fixture",
                        formal=formal, resume=False,
                    )
                self.assertEqual(["good"], completed)
                self.assertTrue((output / "questions/good/result.json").is_file())
                self.assertTrue((output / "questions/failed/failure.json").is_file())
                self.assertFalse((output / "report.json").exists())

    def test_source_owned_semantics_decodes_before_submission_and_retries_bad_reference(self) -> None:
        contract = adapter.semantic_representation.load_contract(
            Path(adapter.__file__).resolve().parents[2] / "manifests/kernel-candidates/v2/source-ownership/semantic-representation.json")
        work = [{"id": "work-a", "asset": {"id": "asset-a", "revision": 3,
                 "content": "User: Choose violet.\nAssistant: Amber is not approved."}, "candidates": []}]
        good = {"analyses": [{"index": 0, "summary": 0, "topics": ["choice"],
                             "cues": [{"passage": 1, "kind": "constraint"}]}]}
        bad = copy.deepcopy(good)
        bad["analyses"][0]["summary"] = 99
        transport = FakeTransport([bad, good])
        capability = adapter.ExternalIntelligenceCapability(transport, contract)
        settings = {**self.protocol["memory"], "semantic_attempts": 2}
        with tempfile.TemporaryDirectory() as directory:
            decoded, _ = capability.semantics(work, settings, Path(directory))
        self.assertEqual(transport.calls, 2)
        self.assertEqual(decoded, [{"work_id": "work-a", "summary": "User: Choose violet.",
                                   "topics": ["choice"], "cues": [{"text": "Assistant: Amber is not approved.", "kind": "constraint"}]}])

    def test_organization_source_repair_preserves_valid_work_and_actual_inputs(self):
        contract = adapter.semantic_representation.SemanticInputContract(adapter.semantic_representation.GROUNDED_REPRESENTATION, "test", None)
        work = [{"id": f"w{i}", "organization_schema": "ownward.organization/v1",
                 "asset": {"id": f"a{i}", "revision": 1, "content": text}, "candidates": []}
                for i, text in enumerate(["Keep violet.\nOnly indoors.", "Use Beacon."])]
        def row(index, passage):
            return {"index": index, "summary": passage, "topics": [], "cues": [],
                    "organization": {"schema": "ownward.organization/v1", "units": [], "links": []}}
        transport = FakeTransport([{"analyses": [row(0, 0), row(1, 99)]}, {"analyses": [row(0, 0)]}])
        capability = adapter.ExternalIntelligenceCapability(transport, contract)
        with tempfile.TemporaryDirectory() as directory:
            decoded, usage = capability.semantics(work, {**self.protocol["memory"], "semantic_attempts": 2}, Path(directory))
            repeated, _ = capability.semantics(work, {**self.protocol["memory"], "semantic_attempts": 2}, Path(directory))
        self.assertEqual(transport.calls, 2)
        self.assertEqual(decoded, repeated)
        self.assertEqual([x["work_id"] for x in decoded], ["w0", "w1"])
        self.assertEqual({x["id"] for x in decoded[0]["input_assets"]}, {"a0", "a1"})
        self.assertEqual({x["id"] for x in decoded[1]["input_assets"]}, {"a1"})
        self.assertEqual(usage["retries"], 1)

    def test_protocol_freezes_official_identity_models_and_cost_inventory(self) -> None:
        adapter.validate_protocol(self.protocol)
        self.assertEqual(adapter.OFFICIAL_DATA_SHA256, self.protocol["official"]["data_sha256"])
        self.assertEqual("gpt-5.6-luna", self.protocol["memory"]["semantic_model"])
        self.assertEqual("gpt-5.6-luna", self.protocol["reader"]["model"])
        self.assertEqual("xhigh", self.protocol["reader"]["reasoning_effort"])
        self.assertEqual("gpt-5.6-terra", self.protocol["judge"]["model"])
        self.assertEqual("codex", self.protocol["memory"]["capability_source"])
        self.assertEqual("codex", self.protocol["reader"]["capability_source"])
        self.assertEqual("codex", self.protocol["judge"]["capability_source"])
        self.assertEqual(adapter.PRODUCTION_PROFILE, self.protocol["acceptance"]["profile"])
        self.assertNotIn("minimum_accuracy", self.protocol["acceptance"])
        self.assertEqual(23867, self.protocol["execution"]["total_sessions"])
        self.assertEqual(1498, self.protocol["execution"]["semantic_batches"])
        self.assertEqual(1498, self.protocol["execution"]["semantic_work_requests"])
        selection = adapter.load_json(adapter.SUPPORT_ROOT / "external-intelligence-runtime.json")
        self.assertEqual("ownward.external-intelligence/v1", selection["contract"])
        self.assertEqual("opencode-go-api/v1", selection["default_driver"])
        self.assertEqual(
            {"codex-app-server/v1", "opencode-server/v1", "opencode-go-api/v1"},
            {item["driver"] for item in selection["implementations"]},
        )
        self.assertEqual(8, self.protocol["execution"]["codex_max_active"])
        self.assertEqual(20, self.protocol["memory"]["semantic_analysis_max_works"])
        self.assertEqual("ownward.semantic-deduplicated-body-table/v1", self.protocol["memory"]["semantic_input_representation"])
        self.assertEqual(1050000, self.protocol["memory"]["semantic_context_window_tokens"])
        self.assertEqual(850000, self.protocol["memory"]["semantic_analysis_input_token_upper_bound"])
        self.assertEqual(20400, self.protocol["execution"]["full_wall_seconds"])
        self.assertEqual("not_determined", self.protocol["acceptance"]["quality_assessment_status"])
        self.assertEqual("external-agent-progressive/v1", self.protocol["retrieval"]["mode"])
        self.assertTrue(self.protocol["reader"]["requires_tools"])
        self.assertNotIn("evidence_selection_policy", self.protocol["retrieval"])
        self.assertEqual(list(adapter.ACTIVE_RETRIEVAL_TOOLS), self.protocol["retrieval"]["allowed_tools"])

    def test_explicit_external_intelligence_roles_create_an_effective_copy(self) -> None:
        before = json.loads(json.dumps(self.protocol))
        roles = {
            role: {"model": "qwen3.8-flash", "reasoning_effort": effort}
            for role, effort in (("semantic", "xhigh"), ("reader", "xhigh"), ("judge", "medium"))
        }
        effective = adapter.apply_external_intelligence_roles(self.protocol, roles)
        self.assertEqual(before, self.protocol)
        self.assertEqual("qwen3.8-flash", effective["memory"]["semantic_model"])
        self.assertEqual("xhigh", effective["reader"]["reasoning_effort"])
        self.assertEqual("xhigh", effective["judge"]["reasoning_effort"])
        self.assertEqual("external-intelligence", effective["memory"]["capability_source"])
        self.assertEqual("external-intelligence", effective["reader"]["capability_source"])
        self.assertEqual("external-intelligence", effective["judge"]["capability_source"])
        self.assertNotIn("selection_profile_identity", effective["reader"])
        with self.assertRaisesRegex(adapter.AdapterError, "incomplete"):
            adapter.apply_external_intelligence_roles(self.protocol, {"reader": roles["reader"]})

    def test_stage6_and_formal_share_the_same_xhigh_reader_identity(self) -> None:
        adapter.validate_protocol(self.protocol, formal=False)
        adapter.validate_protocol(self.protocol, formal=True)
        medium = json.loads(json.dumps(self.protocol))
        medium["reader"]["reasoning_effort"] = "medium"
        with self.assertRaisesRegex(adapter.AdapterError, "Reader identity changed"):
            adapter.validate_protocol(medium, formal=False)
        with self.assertRaisesRegex(adapter.AdapterError, "Reader identity changed"):
            adapter.validate_protocol(medium, formal=True)

    def test_product_protocol_fails_closed_without_active_agent_tools(self) -> None:
        passive = json.loads(json.dumps(self.protocol))
        passive["retrieval"].pop("mode")
        with self.assertRaisesRegex(adapter.AdapterError, "retrieval protocol is invalid"):
            adapter.validate_protocol(passive)
        host_selected = json.loads(json.dumps(self.protocol))
        host_selected["retrieval"]["evidence_selection_policy"] = "rank-depth-diagonal-budget-fit/v1"
        with self.assertRaisesRegex(adapter.AdapterError, "host-side evidence selection"):
            adapter.validate_protocol(host_selected)
        no_tools = json.loads(json.dumps(self.protocol))
        no_tools["reader"]["requires_tools"] = False
        with self.assertRaisesRegex(adapter.AdapterError, "Reader identity changed"):
            adapter.validate_protocol(no_tools)

    def test_active_reader_requires_product_rules_before_invoking_the_model(self) -> None:
        client = FakeToolClient()
        client.instructions = ""
        transport = FakeTransport()
        with tempfile.TemporaryDirectory() as directory:
            with mock.patch.object(client, "call_tool", return_value={"rules": ""}) as call:
                with self.assertRaisesRegex(adapter.MCPError, "collaboration rules"):
                    adapter.ExternalIntelligenceCapability(transport).active_answer(
                        {"question": "Which detail is current?"}, client,
                        self.protocol["reader"], self.protocol["retrieval"], Path(directory),
                    )
                call.assert_called_once_with("ownward_rules", {})
        self.assertEqual(0, transport.calls)

    def test_nonformal_comparison_accepts_only_the_common_active_tools(self) -> None:
        comparison = json.loads(json.dumps(self.protocol))
        comparison["retrieval"]["allowed_tools"] = list(adapter.CORE_ACTIVE_RETRIEVAL_TOOLS)
        adapter.validate_protocol(comparison, formal=False)
        with self.assertRaisesRegex(adapter.AdapterError, "retrieval protocol is invalid"):
            adapter.validate_protocol(comparison, formal=True)

    def test_active_retrieval_agent_chooses_observed_tools_and_reads_evidence(self) -> None:
        client = FakeToolClient()
        client.contents["info-1"] = "The selected city is Kyoto."
        session = adapter.ActiveRetrievalSession(client, self.protocol["retrieval"])
        search = session.call("ownward_search", {"query": "selected city", "limit": 1})
        source_id = search["results"][0]["id"]
        session.call("ownward_read", {"id": source_id})
        session.validate()
        report = session.report()
        self.assertEqual("external-agent-progressive/v1", report["mode"])
        self.assertEqual(["info-1"], report["read_ids"])
        self.assertEqual(2, len(report["selection_steps"]))
        self.assertEqual({"query": "selected city", "limit": 1}, report["selection_steps"][0]["arguments"])
        self.assertEqual(search, report["selection_steps"][0]["result"])

    def test_active_retrieval_rejects_unobserved_ids(self) -> None:
        client = FakeToolClient()
        session = adapter.ActiveRetrievalSession(client, self.protocol["retrieval"])
        with self.assertRaisesRegex(adapter.AdapterError, "not observed"):
            session.call("ownward_read", {"id": "invented"})
        self.assertEqual(1, len(session.report()["selection_steps"]))
        self.assertFalse(session.report()["selection_steps"][0]["success"])
        self.assertEqual({"id": "invented"}, session.report()["selection_steps"][0]["arguments"])
        self.assertIsNone(session.report()["selection_steps"][0]["result"])

    def test_reader_tool_limits_match_execution_without_mutating_product_schema(self) -> None:
        client = FakeToolClient()
        client.contents['info-1'] = 'A remembered event. ' * 50
        manifest = client.list_tools()
        evidence_tool = next(t for t in manifest if t['name'] == 'ownward_evidence_search')
        evidence_tool['inputSchema']['properties'] = {
            'limit': {'type': 'integer', 'minimum': 1, 'maximum': 8, 'default': 8}}
        original = json.loads(json.dumps(manifest))
        with mock.patch.object(client, 'list_tools', return_value=manifest):
            session = adapter.ActiveRetrievalSession(client, self.protocol['retrieval'])
        exposed = next(t for t in session.dynamic_tools if t['name'] == 'ownward_evidence_search')
        limit = exposed['inputSchema']['properties']['limit']
        self.assertEqual(3, limit['maximum'])
        self.assertLessEqual(limit['default'], limit['maximum'])
        self.assertEqual(original, manifest)
        session.call('ownward_search', {'query': 'event', 'limit': 1})
        session.call('ownward_evidence_search', {'source_id': 'info-1', 'query': 'event', 'limit': limit['maximum']})
        with self.assertRaises(adapter.AdapterError):
            session.call('ownward_evidence_search', {'source_id': 'info-1', 'query': 'event', 'limit': limit['maximum'] + 1})

    def test_search_evidence_can_be_read_directly_with_normal_read_budget(self) -> None:
        client = FakeToolClient()
        client.evidence['ref-a'] = ('info-1', 'The archive opens at noon.')
        call = client.call_tool
        def serve(name, arguments):
            if name == 'ownward_search':
                return {'results': [{'id': 'info-1', 'evidence': [
                    {'id': 'ref-a', 'source_id': 'info-1'},
                    {'id': 'ref-foreign', 'source_id': 'info-2'}]}]}
            return call(name, arguments)
        client.call_tool = serve
        session = adapter.ActiveRetrievalSession(client, self.protocol['retrieval'])
        session.call('ownward_search', {'query': 'archive opening'})
        session.call('ownward_evidence_read', {'id': 'ref-a'})
        session.validate()
        self.assertEqual(len('The archive opens at noon.'), session.report()['context_chars'])
        with self.assertRaisesRegex(adapter.AdapterError, 'not observed'):
            session.call('ownward_evidence_read', {'id': 'ref-foreign'})

    def test_source_backed_search_excerpt_needs_no_redundant_read(self) -> None:
        for preview, source_id, accepted in [('The archive opens at noon.', 'info-1', True),
                                              ('', 'info-1', False),
                                              ('A foreign claim.', 'info-2', False)]:
            with self.subTest(preview=preview, source_id=source_id):
                client = FakeToolClient()
                response = {'results': [{'id': 'info-1', 'summary': 'Unverified summary alone is not evidence.',
                    'evidence': [{'id': 'ref-a', 'source_id': source_id, 'preview': preview}]}]}
                with mock.patch.object(client, 'call_tool', return_value=response):
                    session = adapter.ActiveRetrievalSession(client, self.protocol['retrieval'])
                    session.call('ownward_search', {'query': 'archive opening'})
                if accepted:
                    session.validate()
                    self.assertEqual(['info-1'], session.report()['preview_source_ids'])
                    self.assertEqual(len(preview), session.report()['preview_chars'])
                    self.assertEqual(0, session.report()['context_chars'])
                    self.assertEqual([], session.report()['read_ids'])
                else:
                    with self.assertRaisesRegex(adapter.AdapterError, 'without reading'):
                        session.validate()

    def test_active_reader_delivers_after_preparation_and_retrieval(self) -> None:
        requests = []
        def invoke(**request):
            requests.append(request)
            if request['schema'] == adapter.information_use_flow.FRAME_SCHEMA:
                self.assertNotIn('active_retrieval', request)
                self.assertNotIn('SECRET GOLD', request['prompt'])
                return {'purpose': 'Find the city', 'needs': ['Which city is selected?']}, {'calls': 1}
            active = request['active_retrieval']
            context = request['prepare_context'](active.call)
            self.assertIn('ownward_search', context)
            active.call('ownward_read', {'id': 'info-1'})
            self.assertIn(adapter.information_use_flow.OFFER, request['prompt'])
            self.assertNotIn('SECRET GOLD', request['prompt'])
            self.assertEqual(request['model'], self.protocol['reader']['model'])
            self.assertEqual(request['effort'], self.protocol['reader']['reasoning_effort'])
            return {'intended_outcome': 'Find the city',
                    'basis': {'1': {'supported': 'City is Kyoto', 'unresolved': ''}},
                    'answer': 'Kyoto', 'conditional_results': []}, {'calls': 1}
        with tempfile.TemporaryDirectory() as directory:
            client = FakeToolClient()
            client.contents['info-1'] = 'The selected city is Kyoto.'
            capability = adapter.ExternalIntelligenceCapability(FakeTransport())
            with mock.patch.object(capability, '_invoke', side_effect=invoke):
                answer, usage, report = capability.active_answer(
                    {'question': 'Which city?', 'question_date': 'today', 'answer': 'SECRET GOLD',
                     'haystack_sessions': ['UNREAD HAYSTACK']},
                    client, self.protocol['reader'], self.protocol['retrieval'], Path(directory))
            self.assertEqual(answer, 'Kyoto')
            self.assertEqual(usage['calls'], 2)
            self.assertEqual(len(requests), 2)
            self.assertTrue(report['information_use']['used'])
            self.assertEqual(len(report['selection_steps']), 2)
            self.assertTrue((Path(directory) / 'information-use-result.json').is_file())


    def test_active_capability_exposes_tools_and_checkpoints_the_agent_trace(self) -> None:
        class ActiveTransport(FakeTransport):
            def invoke(self, **request):
                self.calls += 1
                self.test_case.assertEqual(self.expected_instructions, request["base_instructions"])
                if request['schema'] == adapter.information_use_flow.FRAME_SCHEMA:
                    self.test_case.assertNotIn('dynamic_tools', request)
                    return {'purpose': 'Find the city', 'needs': ['Which city is selected?']}, {'input_tokens': 1, 'output_tokens': 1}, {'transport': 'fixture'}
                self.test_case.assertIn("initial_context", request)
                self.test_case.assertIn("ownward_search", request["initial_context"])
                names = [item["name"] for item in request["dynamic_tools"]]
                self.test_case.assertEqual(list(adapter.ACTIVE_RETRIEVAL_TOOLS), names)
                search = request["tool_handler"]("ownward_search", {"query": "selected city", "limit": 1})
                request["tool_handler"]("ownward_read", {"id": search["results"][0]["id"]})
                return {"answer": "Kyoto", "intended_outcome": "Find the city",
                        "basis": {"1": {"supported": "Kyoto", "unresolved": ""}}, "conditional_results": []}, {
                    "input_tokens": 1, "cached_input_tokens": 0, "output_tokens": 1, "reasoning_output_tokens": 0,
                }, {
                    "transport": "codex-app-server-stdio", "server_instance": "fixture",
                    "thread_id": "thread", "turn_id": "turn", "thread_ephemeral": True,
                    "sandbox": "read-only", "status": "completed",
                }

        with tempfile.TemporaryDirectory() as directory:
            client = FakeToolClient()
            client.contents["info-1"] = "The selected city is Kyoto."
            transport = ActiveTransport()
            transport.test_case = self
            transport.expected_instructions = client.instructions
            answer, _usage, report = adapter.ExternalIntelligenceCapability(transport).active_answer(
                {"question": "Which city?", "question_date": "today"},
                client,
                self.protocol["reader"],
                self.protocol["retrieval"],
                Path(directory),
            )
            self.assertEqual("Kyoto", answer)
            self.assertEqual("external-agent-progressive/v1", report["mode"])
            checkpoint = adapter.load_json(Path(directory) / "complete.json")
            self.assertEqual("external-agent-progressive/v1", checkpoint["active_retrieval"]["mode"])
            request = adapter.load_json(Path(directory) / "request.json")
            self.assertEqual(client.instructions, request["base_instructions"])
            self.assertEqual(3, len(report["selection_steps"]))

            client.instructions = "更新后的服务端规则。"
            with self.assertRaisesRegex(adapter.AdapterError, "request identity changed"):
                adapter.ExternalIntelligenceCapability(transport).active_answer(
                    {"question": "Which city?", "question_date": "today"}, client,
                    self.protocol["reader"], self.protocol["retrieval"], Path(directory),
                )
            self.assertEqual(2, transport.calls)

    def test_app_server_returns_dynamic_tool_results_on_the_protocol_channel(self) -> None:
        server = concrete_transport.CodexAppServer(Path("codex.exe"), Path("auth.json"), Path("runtime"), ["codex"], {})
        server._active_tool_handler = lambda name, arguments: {"tool": name, "arguments": arguments}
        with mock.patch.object(server, "_write_message") as write:
            server._handle_tool_call(17, {"tool": "ownward_search", "arguments": {"query": "q"}})
        write.assert_called_once_with({
            "id": 17,
            "result": {
                "success": True,
                "contentItems": [{
                    "type": "inputText",
                    "text": '{"tool":"ownward_search","arguments":{"query":"q"}}',
                }],
            },
        })

    def test_official_answer_labels_are_validated_but_never_enter_memory_content(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "fixture.json"
            path.write_text(json.dumps([{
                "question_id": "q", "question_type": "single-session-user", "question": "q?", "answer": "a",
                "haystack_dates": ["date"], "haystack_session_ids": ["s"],
                "haystack_sessions": [[{"role": "user", "content": "memory", "has_answer": True}]],
            }]), encoding="utf-8")
            dataset = adapter.validate_dataset(path, formal=False)
            content = adapter.session_content("s", "date", dataset[0]["haystack_sessions"][0])
            product_question = adapter._product_question({**dataset[0], "answer_session_ids": ["s"]})
            self.assertNotIn("has_answer", content)
            self.assertNotIn("True", content)
            self.assertNotIn("answer", product_question)
            self.assertNotIn("answer_session_ids", product_question)

    def test_question_lifecycle_uses_public_paths_and_reuses_complete_checkpoint(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "ownward.exe"
            codex = root / "codex.exe"
            auth = root / "auth.json"
            embedding = root / "embedding"
            for path in (binary, codex, auth):
                path.write_bytes(b"fixture")
            embedding.mkdir()
            evaluator = root / "evaluate_qa.py"
            evaluator.write_text("def get_anscheck_prompt(task, question, answer, response, abstention=False):\n    return f'{question} {answer} {response}'\n", encoding="utf-8")
            question = {
                "question_id": "fixture", "question_type": "single-session-user",
                "question": "Which city?", "answer": "Kyoto city", "answer_session_ids": ["session-1"], "question_date": "today",
                "haystack_dates": ["yesterday"], "haystack_session_ids": ["session-1"],
                "haystack_sessions": [[{"role": "user", "content": "I chose Kyoto."}]],
            }
            FakeRuntime.starts = 0
            with mock.patch.object(adapter, "OwnwardRuntime", FakeRuntime), \
                    mock.patch.object(FakeToolClient, "call_tool", autospec=True, side_effect=FakeToolClient.call_tool) as calls, \
                    adapter.ExternalIntelligenceScheduler(8) as scheduler:
                result = adapter.process_question(
                    question, root / "run", "identity", binary, embedding,
                    self.protocol, evaluator, lambda: FakeCodex(), scheduler,
                )
                resumed = adapter.process_question(
                    question, root / "run", "identity", binary, embedding,
                    self.protocol, evaluator, lambda: FakeCodex(), scheduler,
                )
            self.assertTrue(result["complete"])
            self.assertTrue(result["autoeval_label"]["label"])
            self.assertEqual(["info-1"], result["retrieval"]["read_ids"])
            self.assertEqual(result, resumed)
            self.assertEqual(1, FakeRuntime.starts)
            checkpoint = adapter.load_json(root / "run" / "questions" / "fixture" / "checkpoint.json")
            self.assertEqual(1, checkpoint["organized_batches"])
            created = next(call.args[2]['items'][0] for call in calls.call_args_list if call.args[1] == 'ownward_create_batch')
            self.assertEqual(adapter.session_reference('session-1'), created['source']['ref'])
            self.assertEqual('session-1', checkpoint['asset_sources'][0]['session_id'])
            reader_input = adapter.load_json(root / "run" / "questions" / "fixture" / "reader" / "input.json")
            judge_input = adapter.load_json(root / "run" / "questions" / "fixture" / "judge" / "input.json")
            diagnostic = adapter.load_json(root / "run" / "questions" / "fixture" / "diagnostic.json")
            self.assertNotIn("Kyoto city", reader_input["prompt"])
            self.assertIn("Kyoto city", judge_input["official_prompt"])
            self.assertTrue(diagnostic["post_answer_only"] if "post_answer_only" in diagnostic else diagnostic["diagnostic_only"])
            self.assertEqual(["info-1"], diagnostic["evidence_coverage"]["read_expected"])

    def test_two_stage_retrieval_delivers_late_long_source_within_original_budget(self) -> None:
        runtime = FakeRuntime()
        assert runtime.client is not None
        first = "Cobalt archive draft notes. " + ("unrelated observatory log. " * 520)
        second = ("routine harbor ledger. " * 260) + "The cobalt archive final code is VIOLET-731. " + ("routine harbor ledger. " * 260)
        runtime.client.contents = {"info-1": first, "info-2": second}

        old_read_ids = []
        old_chars = 0
        for identifier, content in runtime.client.contents.items():
            if old_read_ids and old_chars + len(content) > self.protocol["retrieval"]["context_max_chars"]:
                break
            old_read_ids.append(identifier)
            old_chars += len(content)
        self.assertEqual(["info-1"], old_read_ids)

        evidence, trace = adapter.passive_retrieve(runtime, "What is the cobalt archive final code?", self.protocol)
        self.assertEqual(["info-1", "info-2"], trace["read_ids"])
        self.assertTrue(all(item["mode"] == "evidence" for item in trace["read_paths"]))
        self.assertLessEqual(trace["context_chars"], self.protocol["retrieval"]["context_max_chars"])
        self.assertLessEqual(len(evidence), self.protocol["retrieval"]["read_limit"])
        self.assertEqual(self.protocol["retrieval"]["read_limit"], trace["limits"]["read_units"])
        self.assertEqual(self.protocol["retrieval"]["context_max_chars"], trace["limits"]["context_chars"])
        self.assertEqual(self.protocol["retrieval"]["evidence_search_limit_per_source"], trace["limits"]["evidence_depth_per_source"])
        self.assertTrue(any("VIOLET-731" in item["content"] for item in evidence))
        self.assertTrue(trace["evidence_read_ids"])

    def test_adjacent_source_text_is_charged_and_invalid_ranges_are_rejected(self) -> None:
        value = {"source_id": "source", "start_rune": 5, "end_rune": 9,
                 "content": "原文片段", "context_before": "🙂前", "context_after": "后",
                 "context_start_rune": 3, "context_end_rune": 10}
        source, text = adapter.ActiveRetrievalSession._read_content(
            "ownward_evidence_read", {"evidence": value})
        self.assertEqual("source", source)
        self.assertEqual(7, len(text))
        self.assertEqual(("🙂前", "后"), adapter.ActiveRetrievalSession._adjacent_content(value))
        for change in ({"context_start_rune": 2}, {"context_end_rune": 11},
                       {"context_before": None}, {"context_start_rune": True}):
            with self.subTest(change=change), self.assertRaises(adapter.AdapterError):
                adapter.ActiveRetrievalSession._adjacent_content({**value, **change})

    def test_passive_retrieval_delivers_and_charges_adjacent_source_text(self) -> None:
        class ContextClient(FakeToolClient):
            def call_tool(self, name: str, arguments: dict):
                value = super().call_tool(name, arguments)
                if name == "ownward_evidence_read":
                    evidence = value["evidence"]
                    start = evidence.get("start_rune", 0)
                    end = start + len(evidence["content"])
                    evidence.update(start_rune=start, end_rune=end, context_before="",
                                    context_after=" SOURCE-CONTEXT", context_start_rune=start,
                                    context_end_rune=end + len(" SOURCE-CONTEXT"))
                return value
        runtime = FakeRuntime()
        runtime.client = ContextClient()
        runtime.client.contents = {"info-1": "signal alpha. " * 40}
        evidence, trace = adapter.passive_retrieve(runtime, "signal", self.protocol)
        self.assertTrue(evidence)
        self.assertTrue(all(item["content"].endswith(" SOURCE-CONTEXT") for item in evidence))
        self.assertEqual(sum(len(item["content"]) for item in evidence), trace["context_chars"])

    def test_two_stage_retrieval_preserves_revision_bound_source_prelude(self) -> None:
        class PreludeClient(FakeToolClient):
            def call_tool(self, name: str, arguments: dict):
                value = super().call_tool(name, arguments)
                if name == "ownward_evidence_search":
                    for reference in value["evidence"]:
                        reference.update({"start_rune": 256, "source_revision": 1})
                if name == "ownward_evidence_read":
                    evidence = value["evidence"]
                    prelude = "Conversation date: 2031-02-15\n\nSource session: independent"
                    evidence.update({
                        "start_rune": 256,
                        "source_revision": 1,
                        "source_prelude_start_rune": 0,
                        "source_prelude_end_rune": len(prelude),
                        "source_prelude": prelude,
                    })
                return value

        class Runtime:
            def __init__(self) -> None:
                self.client = PreludeClient()

        runtime = Runtime()
        runtime.client.contents = {"info-1": "neutral detail. " * 40 + "the selected marker is IVORY-17"}
        evidence, trace = adapter.passive_retrieve(runtime, "What is the selected marker?", self.protocol)
        self.assertTrue(evidence[0]["content"].startswith("Conversation date: 2031-02-15"))
        self.assertEqual(sum(len(item["content"]) for item in evidence), trace["context_chars"])
        selected = next(step for step in trace["selection_steps"] if step["selected"])
        self.assertGreater(selected["source_prelude_runes"], 0)
        self.assertEqual(len(evidence[0]["content"]), selected["delivered_runes"])

    def test_two_stage_retrieval_rejects_unbound_source_prelude(self) -> None:
        class PreludeClient(FakeToolClient):
            def __init__(self, failure: str) -> None:
                super().__init__()
                self.failure = failure

            def call_tool(self, name: str, arguments: dict):
                value = super().call_tool(name, arguments)
                if name == "ownward_evidence_search":
                    for reference in value["evidence"]:
                        reference.update({"start_rune": 256, "source_revision": 1})
                if name == "ownward_evidence_read":
                    evidence = value["evidence"]
                    prelude = "Conversation date: 2031-02-15"
                    evidence.update({
                        "start_rune": 256,
                        "source_revision": 2 if self.failure == "revision" else 1,
                        "source_prelude_start_rune": 1 if self.failure == "origin" else 0,
                        "source_prelude_end_rune": 300 if self.failure == "overlap" else len(prelude),
                        "source_prelude": prelude,
                    })
                return value

        class Runtime:
            def __init__(self, failure: str) -> None:
                self.client = PreludeClient(failure)
                self.client.contents = {"info-1": "neutral detail. " * 40 + "the selected marker is IVORY-17"}

        for failure in ("revision", "origin", "overlap"):
            with self.subTest(failure=failure):
                with self.assertRaisesRegex(adapter.AdapterError, "non-source-bound prelude"):
                    adapter.passive_retrieve(Runtime(failure), "What is the selected marker?", self.protocol)

    def test_truncated_candidate_evidence_uses_one_revision_bound_budget_fit_complete_source(self) -> None:
        source = "header " + ("neutral record. " * 80) + "FACT-A FACT-B FACT-C FACT-D"

        class CompleteSourceClient:
            def __init__(self) -> None:
                self.read_arguments: list[dict] = []

            def call_tool(self, name: str, arguments: dict):
                if name == "ownward_search":
                    return {"results": [{"id": "info-1", "score": 1.0, "signals": ["lexical"]}]}
                if name == "ownward_evidence_search":
                    return {
                        "evidence": [
                            {
                                "id": f"evidence-{index}", "source_id": "info-1", "source_revision": 7,
                                "start_rune": index * 100, "end_rune": index * 100 + 80, "content_runes": 80,
                            }
                            for index in range(3)
                        ],
                        "truncated": True,
                        "source_runes": len(source),
                    }
                if name == "ownward_evidence_read":
                    self.read_arguments.append(dict(arguments))
                    return {"evidence": {
                        "id": arguments["id"], "source_id": "info-1", "source_revision": 7,
                        "start_rune": 0, "end_rune": 80, "content": source[:80],
                        "source_complete_start_rune": 0,
                        "source_complete_end_rune": len(source),
                        "source_complete": source,
                    }}
                raise AssertionError(name)

        client = CompleteSourceClient()
        runtime = type("Runtime", (), {"client": client})()
        evidence, trace = adapter.passive_retrieve(runtime, "List FACT-A through FACT-D", self.protocol)

        self.assertEqual(1, len(evidence))
        self.assertEqual(source, evidence[0]["content"])
        self.assertEqual("complete-source", trace["read_paths"][0]["mode"])
        self.assertEqual(1, len(client.read_arguments))
        self.assertEqual(self.protocol["retrieval"]["context_max_chars"], client.read_arguments[0]["source_context_limit"])
        selected = [step for step in trace["selection_steps"] if step["selected"]]
        self.assertEqual([("info-1", 0, "complete-source")], [
            (step["source_id"], step["depth"], step["mode"]) for step in selected
        ])

    def test_truncated_source_that_does_not_fit_keeps_original_fragment_path(self) -> None:
        class OversizedClient:
            def __init__(self) -> None:
                self.read_arguments: list[dict] = []

            def call_tool(self, name: str, arguments: dict):
                if name == "ownward_search":
                    return {"results": [{"id": "info-1", "score": 1.0, "signals": ["lexical"]}]}
                if name == "ownward_evidence_search":
                    return {
                        "evidence": [
                            {
                                "id": f"evidence-{index}", "source_id": "info-1", "source_revision": 3,
                                "start_rune": index * 80, "end_rune": index * 80 + 80, "content_runes": 80,
                            }
                            for index in range(3)
                        ],
                        "truncated": True,
                        "source_runes": 30000,
                    }
                if name == "ownward_evidence_read":
                    self.read_arguments.append(dict(arguments))
                    return {"evidence": {
                        "id": arguments["id"], "source_id": "info-1", "source_revision": 3,
                        "start_rune": 0, "end_rune": 80, "content": "x" * 80,
                    }}
                raise AssertionError(name)

        client = OversizedClient()
        runtime = type("Runtime", (), {"client": client})()
        evidence, trace = adapter.passive_retrieve(runtime, "x", self.protocol)

        self.assertEqual(3, len(evidence))
        self.assertTrue(all("source_context_limit" not in value for value in client.read_arguments))
        self.assertTrue(all(path["mode"] == "evidence" for path in trace["read_paths"]))

    def test_budget_loading_uses_rank_depth_diagonal_order(self) -> None:
        runtime = FakeRuntime()
        assert runtime.client is not None
        runtime.client.contents = {
            "info-1": "cobalt routing distractor alpha. " * 80,
            "info-2": "cobalt routing distractor beta. " * 80,
            "info-3": "cobalt routing distractor gamma. " * 80,
            "info-4": "The cobalt routing marker is SOLAR-904. " + ("cobalt routing target context. " * 80),
        }

        evidence, trace = adapter.passive_retrieve(runtime, "What is the cobalt routing marker?", self.protocol)

        self.assertEqual("rank-depth-diagonal-budget-fit/v1", trace["selection_policy"])
        self.assertEqual(["info-1", "info-2", "info-3", "info-4"], trace["read_ids"][:4])
        selected = [step for step in trace["selection_steps"] if step["selected"]]
        self.assertEqual(
            [("info-1", 0), ("info-2", 0), ("info-1", 1), ("info-3", 0),
             ("info-2", 1), ("info-1", 2), ("info-4", 0), ("info-3", 1)],
            [(step["source_id"], step["depth"]) for step in selected],
        )
        self.assertTrue(any(item["id"] == "info-4" and "SOLAR-904" in item["content"] for item in evidence))
        self.assertLessEqual(len(evidence), self.protocol["retrieval"]["read_limit"])

    def test_non_fitting_candidate_does_not_block_later_budget_fit_evidence(self) -> None:
        runtime = FakeRuntime()
        assert runtime.client is not None
        runtime.client.contents = {
            "info-1": "signal alpha. " * 40,
            "info-2": "signal beta. " * 40,
            "info-3": "signal compact result.",
        }
        protocol = json.loads(json.dumps(self.protocol))
        protocol["retrieval"]["context_max_chars"] = 500
        protocol["retrieval"]["read_limit"] = 3
        protocol["retrieval"]["evidence_search_limit_per_source"] = 1

        evidence, trace = adapter.passive_retrieve(runtime, "signal result", protocol)

        self.assertEqual(["info-1", "info-3"], trace["read_ids"])
        self.assertTrue(any(step.get("source_id") == "info-2" and step.get("reason") == "context_budget" for step in trace["selection_steps"]))
        self.assertTrue(any(item["id"] == "info-3" for item in evidence))
        self.assertLessEqual(trace["context_chars"], 500)

    def test_unreadable_evidence_does_not_block_later_candidates(self) -> None:
        class UnreadableClient(FakeToolClient):
            def call_tool(self, name: str, arguments: dict):
                if name == "ownward_evidence_read" and ":info-1:" in arguments["id"]:
                    return {"evidence": None}
                return super().call_tool(name, arguments)

        class Runtime:
            def __init__(self) -> None:
                self.client = UnreadableClient()

        runtime = Runtime()
        runtime.client.contents = {
            "info-1": "signal unreadable. " * 80,
            "info-2": "signal readable beta. " * 80,
            "info-3": "signal compact result.",
        }
        protocol = json.loads(json.dumps(self.protocol))
        protocol["retrieval"]["read_limit"] = 3

        evidence, trace = adapter.passive_retrieve(runtime, "signal result", protocol)

        self.assertFalse(any(item["id"] == "info-1" for item in evidence))
        self.assertTrue(any(item["id"] == "info-2" for item in evidence))
        self.assertTrue(any(item["id"] == "info-3" for item in evidence))
        self.assertTrue(any(
            step.get("source_id") == "info-1" and step.get("reason") == "unreadable"
            for step in trace["selection_steps"]
        ))
        self.assertEqual(3, len(evidence))

    def test_selection_respects_read_limit_at_maximum_returned_sources(self) -> None:
        runtime = FakeRuntime()
        assert runtime.client is not None
        runtime.client.contents = {
            f"info-{index + 1}": f"orchid relay source {index + 1}. " * 80
            for index in range(self.protocol["retrieval"]["search_limit_per_call"])
        }

        evidence, trace = adapter.passive_retrieve(runtime, "orchid relay", self.protocol)

        self.assertEqual(self.protocol["retrieval"]["search_limit_per_call"], len(trace["returned"]))
        self.assertEqual(self.protocol["retrieval"]["read_limit"], len(evidence))
        self.assertEqual(4, len(trace["read_ids"]))
        selected = [step for step in trace["selection_steps"] if step["selected"]]
        self.assertEqual([0, 1, 2], sorted(step["depth"] for step in selected if step["source_id"] == "info-1"))
        self.assertEqual([0, 1, 2, 3], sorted({step["source_rank"] for step in selected}))
        self.assertEqual(sorted(step["priority_layer"] for step in selected), [step["priority_layer"] for step in selected])
        self.assertLessEqual(trace["context_chars"], self.protocol["retrieval"]["context_max_chars"])

    def test_selection_preserves_three_necessary_passages_from_top_ranked_source(self) -> None:
        def passage(fact: str) -> str:
            value = fact + " " + ("neutral itinerary context. " * 40)
            return value[:384].ljust(384, "~")

        runtime = FakeRuntime()
        assert runtime.client is not None
        runtime.client.contents = {
            "info-1": "".join((
                passage("Cobalt itinerary departure fact DAWN-31."),
                passage("Cobalt itinerary transfer fact NOON-52."),
                passage("Cobalt itinerary arrival fact DUSK-74."),
            )),
            **{
                f"info-{index}": f"cobalt itinerary distractor source {index}. " * 80
                for index in range(2, self.protocol["retrieval"]["search_limit_per_call"] + 1)
            },
        }

        evidence, trace = adapter.passive_retrieve(runtime, "Which cobalt itinerary facts are required?", self.protocol)

        delivered = "\n".join(item["content"] for item in evidence)
        self.assertTrue(all(marker in delivered for marker in ("DAWN-31", "NOON-52", "DUSK-74")))
        selected = [step for step in trace["selection_steps"] if step["selected"]]
        self.assertEqual(
            [("info-1", 0), ("info-2", 0), ("info-1", 1), ("info-3", 0),
             ("info-2", 1), ("info-1", 2), ("info-4", 0), ("info-3", 1)],
            [(step["source_id"], step["depth"]) for step in selected],
        )

    def test_diagnostics_separate_pipeline_failures_and_capability_types(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            trace = root / "semantic" / "batch"
            trace.mkdir(parents=True)
            adapter.write_json(trace / "submission.json", {"submitted": True})
            artifacts = {}
            for name in ("checkpoint", "semantic-plan", "retrieval", "reader-input", "reader-output", "answer", "judge-input", "judge-output"):
                path = root / f"{name}.json"
                adapter.write_json(path, {"name": name})
                artifacts[name] = path
            base = {
                "question_id": "q", "question_type": "multi-session", "answer_session_ids": ["session-1"],
            }
            common = {
                "identity": "identity", "answer": "answer", "correct": False, "assets": ["info-1"],
                "asset_sources": [{"session_id": "session-1", "asset_id": "info-1"}],
                "semantic_trace_root": root / "semantic", "reader_input": artifacts["reader-input"],
                "reader_output": artifacts["reader-output"], "judge_input": artifacts["judge-input"],
                "judge_output": artifacts["judge-output"], "phase_seconds": {}, "usage": {},
                "checkpoint_path": artifacts["checkpoint"], "semantic_plan_path": artifacts["semantic-plan"],
                "retrieval_path": artifacts["retrieval"], "answer_path": artifacts["answer"],
            }
            cases = (
                ([], {"returned": [], "read_ids": []}, "target_evidence_not_search_returned"),
                (["info-1"], {"returned": [], "read_ids": []}, "target_evidence_not_search_returned"),
                (["info-1"], {"returned": [{"id": "info-1"}], "read_ids": []}, "target_evidence_not_read"),
                (["info-1"], {"returned": [{"id": "info-1"}], "read_ids": ["info-1"]}, "evidence_read_answer_incorrect"),
            )
            for organized, retrieval, expected in cases:
                record = adapter._diagnostic_record(base, organized_asset_ids=organized, retrieval=retrieval, **common)
                self.assertEqual(expected, record["first_observed_gap"])
                self.assertEqual("not_determined", record["causal_interpretation"]["status"])
                self.assertEqual("cross_session", record["capability"])
            for question_type, capability in (
                ("knowledge-update", "knowledge_update"),
                ("temporal-reasoning", "temporal_reasoning"),
                ("single-session-assistant", "assistant_memory"),
            ):
                record = adapter._diagnostic_record(
                    {**base, "question_type": question_type}, organized_asset_ids=["info-1"],
                    retrieval={"returned": [{"id": "info-1"}], "read_ids": ["info-1"]}, **common,
                )
                self.assertEqual(capability, record["capability"])
            abstention = adapter._diagnostic_record(
                {"question_id": "q_abs", "question_type": "single-session-user", "answer_session_ids": []},
                organized_asset_ids=["info-1"], retrieval={"returned": [], "read_ids": []}, **common,
            )
            self.assertEqual("abstention_response_incorrect", abstention["first_observed_gap"])
            self.assertEqual("abstention", abstention["capability"])

    def test_direct_question_probe_separates_kernel_recall_from_reader_search_choice(self) -> None:
        client = FakeToolClient()
        client.contents = {"info-1": "target", "info-2": "distractor"}
        probe = adapter._direct_question_retrieval_probe(client, "target?", ["info-1"], 2)
        self.assertTrue(probe["applicable"])
        self.assertTrue(probe["all_expected_returned"])
        self.assertEqual([1], probe["expected_return_ranks"])

    def test_failure_record_distinguishes_source_and_semantic_submission_without_gold(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            question = {
                "question_id": "q", "question_type": "multi-session", "question": "q?",
                "haystack_session_ids": ["s1", "s2"], "haystack_dates": ["d1", "d2"],
                "haystack_sessions": [[], []], "answer": "secret", "answer_session_ids": ["s2"],
            }
            adapter.record_question_failure(output, question, RuntimeError("stopped"))
            failure = adapter.load_json(output / "questions" / "q" / "failure.json")
            self.assertEqual("source_session_not_created", failure["first_observed_gap"])
            self.assertFalse(failure["diagnostic_gold_used"])
            self.assertNotIn("answer_session_ids", json.dumps(failure))
            checkpoint = output / "questions" / "q" / "checkpoint.json"
            adapter.write_json(checkpoint, {"asset_sources": [{"session_id": "s1"}, {"session_id": "s2"}], "organized_batches": 0})
            adapter.write_json(output / "questions" / "q" / "semantic-plan.json", {"batches": [{"batch_id": "b"}]})
            adapter.record_question_failure(output, question, RuntimeError("stopped"))
            failure = adapter.load_json(output / "questions" / "q" / "failure.json")
            self.assertEqual("semantic_work_not_fully_submitted", failure["first_observed_gap"])
            adapter.write_json(output / "questions/q/reader/input.json", {"question": "q?"})
            partial = output / "questions/q/reader/codex/attempt-001/active-retrieval.json"
            adapter.write_json(partial, {"selection_steps": [{"tool": "ownward_search", "success": True}]})
            adapter.record_question_failure(output, question, RuntimeError("Reader timed out"))
            failure = adapter.load_json(output / "questions/q/failure.json")
            self.assertEqual("reader_execution_failed", failure["first_observed_gap"])
            self.assertEqual(adapter.sha256(partial), failure["artifacts"]["reader_attempt-001_retrieval"]["sha256"])

    def test_semantic_host_uses_public_work_and_submit_paths_with_codex_output(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            runtime = FakeRuntime()
            runtime.client.contents["info-1"] = "I chose Kyoto."
            capability = FakeCodex()
            frozen = adapter.freeze_semantic_batch(runtime, ["info-1"], Path(directory), "question", 0)
            units = adapter.semantic_analysis_units(frozen, self.protocol["memory"], capability)
            results = {unit["unit_index"]: adapter.analyze_semantic_unit(unit, Path(directory), self.protocol["memory"], capability) for unit in units}
            analysis = adapter.combine_semantic_batch(frozen, units, results, Path(directory), self.protocol["memory"])
            trace_value = adapter.submit_semantic_batch(runtime, frozen, analysis, Path(directory))
            trace = next(Path(directory).rglob("submission.json"))
            submitted = adapter.load_json(trace)["submissions"][0]
            self.assertEqual("codex", submitted["capability"]["id"])
            self.assertEqual("work-info-1", submitted["work_id"])
            self.assertEqual(10, trace_value["usage"]["input_tokens"])

    def test_analyzed_unsubmitted_batch_reuses_exact_frozen_result(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            runtime = FakeRuntime()
            runtime.client.contents["info-1"] = "I chose Kyoto."
            capability = mock.Mock(wraps=FakeCodex())
            frozen = adapter.freeze_semantic_batch(runtime, ["info-1"], root, "question", 0)
            units = adapter.semantic_analysis_units(frozen, self.protocol["memory"], capability)
            first_results = {unit["unit_index"]: adapter.analyze_semantic_unit(unit, root, self.protocol["memory"], capability) for unit in units}
            first = adapter.combine_semantic_batch(frozen, units, first_results, root, self.protocol["memory"])
            restored = adapter.freeze_semantic_batch(runtime, ["info-1"], root, "question", 0)
            restored_units = adapter.semantic_analysis_units(restored, self.protocol["memory"], capability)
            second_results = {unit["unit_index"]: adapter.analyze_semantic_unit(unit, root, self.protocol["memory"], capability) for unit in restored_units}
            second = adapter.combine_semantic_batch(restored, restored_units, second_results, root, self.protocol["memory"])
            self.assertEqual(first, second)
            self.assertEqual(1, capability.semantics.call_count)
            self.assertEqual(1, len([item for item in runtime.client.operations if item[0] == "ownward_semantic_work"]))
            self.assertFalse(any(item[0] == "ownward_semantic_submit_batch" for item in runtime.client.operations))

    def test_semantic_submission_repairs_only_rejected_items_and_resumes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            runtime = FakeRuntime()
            runtime.client.contents.update({"info-1": "Keep violet.", "info-2": "Beacon needs shelter."})
            capability = FakeCodex()
            frozen = adapter.freeze_semantic_batch(runtime, list(runtime.client.contents), root, "question", 0)
            units = adapter.semantic_analysis_units(frozen, self.protocol["memory"], capability)
            results = {u["unit_index"]: adapter.analyze_semantic_unit(u, root, self.protocol["memory"], capability) for u in units}
            initial = adapter.combine_semantic_batch(frozen, units, results, root, self.protocol["memory"])
            submitted = []
            def submit(name, arguments):
                self.assertEqual(name, "ownward_semantic_submit_batch")
                submitted.append(copy.deepcopy(arguments["submissions"]))
                return {"results": ([{"error": "unknown local unit"}] if len(submitted) == 2 else [{}])}
            runtime.client.call_tool = submit
            repaired = mock.Mock()
            repaired.semantics.return_value = ([{"work_id": "work-info-2", "summary": "Beacon needs shelter.",
                                                "topics": [], "cues": [], "input_assets": []}], {"calls": 1, "input_tokens": 7})
            result = adapter.submit_semantic_batch(runtime, frozen, initial, root, repaired, self.protocol["memory"])
            self.assertEqual([len(s) for s in submitted], [1, 1, 1])
            self.assertEqual(submitted[0][0], result["submissions"][0])
            self.assertEqual(submitted[1][0]["work_id"], "work-info-2")
            self.assertEqual([w["id"] for w in repaired.semantics.call_args.args[0]], ["work-info-2"])
            self.assertEqual(result["usage"]["input_tokens"], initial["usage"]["input_tokens"] + 7)
            self.assertEqual(result, adapter.submit_semantic_batch(runtime, frozen, initial, root, repaired, self.protocol["memory"]))
            self.assertEqual(repaired.semantics.call_count, 1)
            self.assertEqual(len(submitted), 3)

    def test_semantic_publication_resumes_after_transport_failure_without_repeating_completed_sources(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            runtime = FakeRuntime()
            runtime.client.contents.update({"info-1": "Keep violet.", "info-2": "Beacon needs shelter."})
            capability = FakeCodex()
            frozen = adapter.freeze_semantic_batch(runtime, list(runtime.client.contents), root, "question", 0)
            units = adapter.semantic_analysis_units(frozen, self.protocol["memory"], capability)
            values = {u["unit_index"]: adapter.analyze_semantic_unit(u, root, self.protocol["memory"], capability) for u in units}
            initial = adapter.combine_semantic_batch(frozen, units, values, root, self.protocol["memory"])
            calls = []
            def submit(name, arguments):
                ids = [item['asset_id'] for item in arguments['submissions']]
                calls.extend(ids)
                if len(calls) == 2:
                    raise adapter.MCPError('transport interrupted')
                return {'results': [{}]}
            runtime.client.call_tool = submit
            with self.assertRaises(adapter.MCPError):
                adapter.submit_semantic_batch(runtime, frozen, initial, root)
            restored = adapter.submit_semantic_batch(runtime, frozen, initial, root)
            self.assertEqual(calls, ['info-1', 'info-2', 'info-2'])
            self.assertEqual(restored['submissions'], initial['submissions'])

    def test_official_abstention_uses_declared_type_as_well_as_dataset_id(self):
        with tempfile.TemporaryDirectory() as directory:
            evaluator = Path(directory) / 'evaluate_qa.py'
            evaluator.write_text('def get_anscheck_prompt(task, question, answer, response, abstention=False):\n    return "abstention" if abstention else "ordinary"\n', encoding='utf-8')
            base = {'question_id': 'case-6', 'question_type': 'unanswerable', 'question': 'Where?', 'answer': 'Unknown'}
            self.assertEqual(adapter.official_prompt(evaluator, base, 'Not recorded'), 'abstention')
            self.assertEqual(adapter.official_prompt(evaluator, {**base, 'question_id': 'x_abs', 'question_type': 'multi-session'}, 'Not recorded'), 'abstention')
            self.assertEqual(adapter.official_prompt(evaluator, {**base, 'question_type': 'multi-session'}, 'Here'), 'ordinary')

    def test_analysis_units_preserve_every_work_item_and_request_contract(self) -> None:
        capability = FakeCodex()
        work = [
            {"id": f"work-{index}", "asset": {"id": f"info-{index}", "revision": 1, "content": "x" * 2000}, "candidates": []}
            for index in range(3)
        ]
        frozen = {
            "question_identity": "question", "batch_index": 0, "batch_id": "batch",
            "asset_ids": [item["asset"]["id"] for item in work], "work": work, "work_sha256": adapter.canonical_sha256(work),
        }
        settings = dict(self.protocol["memory"])
        one_prompt, _, _ = capability.semantic_request(work[:1], settings)
        two_prompt, _, _ = capability.semantic_request(work[:2], settings)
        settings["semantic_analysis_input_token_upper_bound"] = (len(one_prompt.encode("utf-8")) + len(two_prompt.encode("utf-8"))) // 2
        settings["semantic_analysis_output_token_upper_bound"] = 120000
        settings["semantic_analysis_max_works"] = 60
        units = adapter.semantic_analysis_units(frozen, settings, capability)
        self.assertEqual(3, len(units))
        self.assertEqual([item["id"] for item in work], [work_id for unit in units for work_id in unit["work_ids"]])
        self.assertTrue(all(unit["input_utf8_bytes"] <= settings["semantic_analysis_input_token_upper_bound"] for unit in units))
        self.assertEqual(len({unit["identity"] for unit in units}), len(units))

    def test_deduplicated_semantic_input_is_lossless_and_transmits_each_body_once(self) -> None:
        shared = {
            "id": "info-shared", "revision": 2, "content": "shared body",
            "explicit_contexts": [{"key": "r", "value": "v"}],
            "relations": [{"source_id": "info-1", "target_id": "info-shared", "kind": "related"}],
        }
        work = [
            {"id": "work-1", "asset": {"id": "info-1", "revision": 1, "content": "asset one", "contexts": []}, "candidates": [shared]},
            {"id": "work-2", "asset": {"id": "info-shared", "revision": 2, "content": "shared body", "contexts": []}, "candidates": []},
        ]
        encoded = adapter.ExternalIntelligenceCapability.semantic_input(work)
        proof = adapter.ExternalIntelligenceCapability.validate_semantic_input(work, encoded)
        serialized = json.dumps(encoded, ensure_ascii=False)
        self.assertTrue(proof["equivalent"])
        self.assertEqual(2, proof["body_count"])
        self.assertEqual(1, serialized.count("shared body"))
        self.assertEqual(["work-1", "work-2"], proof["work_ids"])
        renamed = json.loads(json.dumps(work))
        replacements = {"work-1": "new-work-a", "work-2": "new-work-b", "info-1": "new-info-a", "info-shared": "new-info-b"}

        def replace(value):
            if isinstance(value, str):
                return replacements.get(value, value)
            if isinstance(value, list):
                return [replace(item) for item in value]
            if isinstance(value, dict):
                return {key: replace(item) for key, item in value.items()}
            return value

        renamed = replace(renamed)
        self.assertEqual(
            adapter.ExternalIntelligenceCapability.semantic_fact_equivalence_sha256(work),
            adapter.ExternalIntelligenceCapability.semantic_fact_equivalence_sha256(renamed),
        )

    def test_global_codex_scheduler_never_exceeds_its_bound(self) -> None:
        active = 0
        maximum = 0

        def work() -> None:
            nonlocal active, maximum
            active += 1
            maximum = max(maximum, active)
            time.sleep(0.02)
            active -= 1

        with adapter.ExternalIntelligenceScheduler(8) as scheduler:
            futures = [scheduler.submit(work) for _ in range(24)]
            for future in futures:
                future.result()
            snapshot = scheduler.snapshot()
        self.assertEqual(8, maximum)
        self.assertEqual({"limit": 8, "max_active": 8, "submitted": 24}, snapshot)

    def test_run_rebind_preserves_semantics_and_invalidates_only_changed_downstream_stages(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            question = root / "questions" / "q"
            semantic = question / "semantic-traces" / "batch" / "analysis.json"
            checkpoint = question / "checkpoint.json"
            retrieval = question / "retrieval.json"
            reader = question / "reader" / "output.json"
            judge = question / "judge" / "output.json"
            for path in (semantic, checkpoint, retrieval, reader, judge, question / "result.json"):
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("{}\n", encoding="utf-8")
            previous = {
                "sha256": "old", "stage_dependencies": {
                    "semantic": "s", "retrieval": "r1", "reader": "a1", "judge": "j1", "diagnostic": "d1",
                },
            }
            current = {
                "sha256": "new", "stage_dependencies": {
                    "semantic": "s", "retrieval": "r2", "reader": "a2", "judge": "j2", "diagnostic": "d2",
                },
            }
            adapter.write_json(root / "identity.json", previous)
            adapter.rebind_run_identity(root, previous, current)
            self.assertTrue(semantic.is_file())
            self.assertTrue(checkpoint.is_file())
            self.assertFalse(retrieval.exists())
            self.assertFalse(reader.exists())
            self.assertFalse(judge.exists())
            self.assertTrue((root / "_audit" / "old" / "questions" / "q" / "retrieval.json").is_file())
            self.assertEqual(current, adapter.load_json(root / "identity.json"))

    def test_run_only_change_reuses_complete_result_with_original_timings(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            question = {"question_id": "q", "question": "What changed?"}
            stages = dict.fromkeys(("semantic", "retrieval", "reader", "judge", "diagnostic"), "stage")
            previous = {"sha256": "old", "stage_dependencies": stages}
            current = {"sha256": "new", "stage_dependencies": stages}
            result = {"identity": adapter._question_identity(question, "old"), "complete": True,
                      "wall_seconds": 123.4, "hypothesis": "Original answer"}
            checkpoint = {"identity": result["identity"], "stage_identities": {
                name: adapter._question_identity(question, value) for name, value in stages.items()}}
            adapter.write_json(root / "identity.json", previous)
            adapter.write_json(root / "questions/q/result.json", result)
            adapter.write_json(root / "questions/q/checkpoint.json", checkpoint)
            adapter.rebind_run_identity(root, previous, current)
            with mock.patch.object(adapter, "OwnwardRuntime", side_effect=AssertionError("must reuse result")):
                resumed = adapter.process_question(
                    question, root, "new", root, root, self.protocol, root,
                    mock.Mock(side_effect=AssertionError("must not call model")), mock.Mock(), stages)
            self.assertEqual({**result, "identity": adapter._question_identity(question, "new")}, resumed)
            self.assertEqual(result, adapter.load_json(root / "_audit/old/questions/q/result.json"))

    def test_interrupted_app_server_runtime_is_cleaned_without_reading_credentials(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            stale = root / ".external-intelligence-runtime" / "codex-app-server-stale"
            stale.mkdir(parents=True)
            (stale / "auth.json").write_text("secret fixture", encoding="utf-8")
            cleaned = adapter.clean_stale_external_intelligence_runtime_roots(root)
            self.assertEqual(["codex-app-server-stale"], cleaned)
            self.assertFalse((root / ".external-intelligence-runtime").exists())
            audit = (root / "_audit" / "transport-cleanup.jsonl").read_text(encoding="utf-8")
            self.assertNotIn("secret fixture", audit)
            self.assertIn('"credential_content_read":false', audit)

    def test_three_natural_batches_analyze_concurrently_but_submit_in_original_order(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "ownward.exe"
            embedding = root / "embedding"
            binary.write_bytes(b"fixture")
            embedding.mkdir()
            evaluator = root / "evaluate_qa.py"
            evaluator.write_text("def get_anscheck_prompt(task, question, answer, response, abstention=False):\n    return response\n", encoding="utf-8")
            sessions = [[{"role": "user", "content": f"Memory {index}."}] for index in range(45)]
            sessions[0] = [{"role": "user", "content": "I chose Kyoto."}]
            question = {
                "question_id": "three-batch", "question_type": "multi-session", "question": "Which city?", "answer": "Kyoto",
                "question_date": "today", "haystack_dates": ["yesterday"] * 45,
                "haystack_session_ids": [f"session-{index}" for index in range(45)], "haystack_sessions": sessions,
            }
            with mock.patch.object(adapter, "OwnwardRuntime", FakeRuntime), adapter.ExternalIntelligenceScheduler(8) as scheduler:
                result = adapter.process_question(
                    question, root / "run", "identity", binary, embedding, self.protocol, evaluator,
                    lambda: DelayedCodex(), scheduler,
                )
            operations = FakeRuntime.last_client.operations
            self.assertEqual(["ownward_semantic_work"] * 3, [name for name, _ in operations[:3]])
            self.assertEqual(["ownward_semantic_submit_batch"] * 45, [name for name, _ in operations[3:]])
            self.assertEqual(3, result["semantic_execution"]["analysis_units"])
            self.assertEqual([[0], [1], [2]], sorted(item["batch_indexes"] for item in result["semantic_execution"]["analysis_completion_order"]))
            self.assertEqual([0, 1, 2], result["semantic_execution"]["submission_order"])
            self.assertTrue(result["semantic_execution"]["serial_concurrent_equivalent"])

    def test_dry_plan_uses_real_public_work_without_model_or_submission_and_resumes_exactly(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "ownward.exe"
            embedding = root / "embedding"
            binary.write_bytes(b"fixture")
            embedding.mkdir()
            question = {
                "question_id": "dry", "question_type": "multi-session", "question": "unused",
                "haystack_dates": ["date"] * 45,
                "haystack_session_ids": [f"session-{index}" for index in range(45)],
                "haystack_sessions": [[{"role": "user", "content": f"Memory {index}."}] for index in range(45)],
            }
            with mock.patch.object(adapter, "OwnwardRuntime", FakeRuntime):
                result = adapter.dry_plan_question(question, root / "plan", "identity", binary, embedding, self.protocol)
            self.assertEqual(3, result["semantic_work_batches"])
            self.assertEqual(3, result["analysis_calls"])
            self.assertTrue(result["all_work_preserved"])
            self.assertFalse(result["model_invoked"])
            self.assertFalse(any(name == "ownward_semantic_submit_batch" for name, _ in FakeRuntime.last_client.operations))
            self.assertFalse((root / "plan" / "questions" / "dry" / "ownward-data").exists())
            with mock.patch.object(adapter, "OwnwardRuntime", side_effect=AssertionError("must not restart")):
                resumed = adapter.dry_plan_question(question, root / "plan", "identity", binary, embedding, self.protocol)
            self.assertEqual(result, resumed)

    def test_codex_failure_is_bounded_by_frozen_attempt_count(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            transport = FakeTransport(error=adapter.ExternalIntelligenceError("timeout"))
            capability = adapter.ExternalIntelligenceCapability(transport)
            with self.assertRaisesRegex(adapter.AdapterError, "after 3 bounded attempts"):
                capability._invoke(
                    role="test", prompt="prompt", schema={"type": "object"}, stage=root / "stage",
                    model="model", effort="low", timeout_seconds=1, attempts=3,
                )
            self.assertEqual(3, transport.calls)
            self.assertEqual(3, len(list((root / "stage").glob("attempt-*"))))

    def test_explicit_recovery_reopens_only_exhausted_transport_failures(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            stage = Path(directory) / "stage"
            with self.assertRaisesRegex(adapter.AdapterError, "after 2 bounded attempts"):
                adapter.ExternalIntelligenceCapability(FakeTransport(error=ConnectionResetError("reset")))._invoke(
                    role="test", prompt="prompt", schema={"type": "object"}, stage=stage,
                    model="model", effort="low", timeout_seconds=1, attempts=2,
                )
            value, usage = adapter.ExternalIntelligenceCapability(FakeTransport())._invoke(
                role="test", prompt="prompt", schema={"type": "object"}, stage=stage,
                model="model", effort="low", timeout_seconds=1, attempts=2,
            )
            self.assertEqual({"items": ["ok"]}, value)
            self.assertEqual(1, usage["attempts"])
            archived = list((stage / "_audit").glob("retryable-runtime-cycle-*/attempt-*"))
            self.assertEqual(2, len(archived))

    def test_codex_output_validation_retries_inside_the_frozen_bound(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            transport = FakeTransport(outputs=[{"items": []}, {"items": ["ok"]}])
            value, usage = adapter.ExternalIntelligenceCapability(transport)._invoke(
                role="test", prompt="prompt", schema={"type": "object"}, stage=root / "stage",
                model="model", effort="low", timeout_seconds=1, attempts=3,
                validate=lambda result: adapter.require(result["items"] == ["ok"], "incomplete output"),
            )
            self.assertEqual({"items": ["ok"]}, value)
            self.assertEqual(2, usage["attempts"])
            self.assertEqual(1, usage["retries"])

    def test_cached_codex_output_is_revalidated_and_archived_before_bounded_retry(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            stage = root / "stage"
            first_transport = FakeTransport(outputs=[{"items": ["stale"]}])
            first, _usage = adapter.ExternalIntelligenceCapability(first_transport)._invoke(
                role="test", prompt="prompt", schema={"type": "object"}, stage=stage,
                model="model", effort="low", timeout_seconds=1, attempts=3,
            )
            self.assertEqual({"items": ["stale"]}, first)

            transport = FakeTransport(outputs=[{"items": ["ok"]}])
            value, usage = adapter.ExternalIntelligenceCapability(transport)._invoke(
                role="test", prompt="prompt", schema={"type": "object"}, stage=stage,
                model="model", effort="low", timeout_seconds=1, attempts=3,
                validate=lambda result: adapter.require(result["items"] == ["ok"], "cached output is invalid"),
            )
            self.assertEqual({"items": ["ok"]}, value)
            self.assertEqual(2, usage["attempts"])
            self.assertEqual(1, transport.calls)
            self.assertEqual(1, len(list((stage / "_audit").glob("invalid-complete-*.json"))))

    def test_invalid_cached_codex_output_cannot_reset_the_frozen_attempt_bound(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            stage = Path(directory) / "stage"
            adapter.ExternalIntelligenceCapability(FakeTransport(outputs=[{"items": ["stale"]}]))._invoke(
                role="test", prompt="prompt", schema={"type": "object"}, stage=stage,
                model="model", effort="low", timeout_seconds=1, attempts=1,
            )
            transport = FakeTransport(outputs=[{"items": ["must-not-run"]}])
            with self.assertRaisesRegex(adapter.AdapterError, "after 1 bounded attempts"):
                adapter.ExternalIntelligenceCapability(transport)._invoke(
                    role="test", prompt="prompt", schema={"type": "object"}, stage=stage,
                    model="model", effort="low", timeout_seconds=1, attempts=1,
                    validate=lambda result: adapter.require(result["items"] == ["ok"], "cached output is invalid"),
                )
            self.assertEqual(0, transport.calls)
            self.assertEqual(1, len(list((stage / "_audit").glob("invalid-complete-*.json"))))

    def test_cached_codex_revalidation_never_masks_request_identity_drift(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            stage = Path(directory) / "stage"
            adapter.ExternalIntelligenceCapability(FakeTransport(outputs=[{"items": ["stale"]}]))._invoke(
                role="test", prompt="prompt", schema={"type": "object"}, stage=stage,
                model="model", effort="low", timeout_seconds=1, attempts=3,
            )
            transport = FakeTransport(outputs=[{"items": ["ok"]}])
            with self.assertRaisesRegex(adapter.AdapterError, "request identity changed"):
                adapter.ExternalIntelligenceCapability(transport)._invoke(
                    role="test", prompt="different-prompt", schema={"type": "object"}, stage=stage,
                    model="model", effort="low", timeout_seconds=1, attempts=3,
                    validate=lambda _result: None,
                )
            self.assertEqual(0, transport.calls)
            self.assertTrue((stage / "complete.json").is_file())
            self.assertFalse((stage / "_audit").exists())

    def test_codex_runtime_cleanup_retries_a_transient_windows_file_lock(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "codex-app-server-fixture"
            root.mkdir()
            (root / "goals.sqlite").write_bytes(b"fixture")
            real_rmtree = adapter.shutil.rmtree
            calls = 0

            def transient(path: Path) -> None:
                nonlocal calls
                calls += 1
                if calls == 1:
                    raise PermissionError(32, "fixture lock")
                real_rmtree(path)

            with mock.patch("codex_app_server.shutil.rmtree", side_effect=transient), mock.patch("codex_app_server.time.sleep"):
                concrete_transport.remove_runtime_root(root, timeout_seconds=1)
            self.assertEqual(2, calls)
            self.assertFalse(root.exists())

    def test_codex_runtime_cleanup_fails_open_after_its_bound(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "codex-app-server-fixture"
            root.mkdir()
            with mock.patch("codex_app_server.shutil.rmtree", side_effect=PermissionError(32, "fixture lock")):
                with self.assertRaisesRegex(concrete_transport.AppServerError, "cleanup did not quiesce"):
                    concrete_transport.remove_runtime_root(root, timeout_seconds=0)
            self.assertTrue(root.exists())

    def test_codex_worker_shutdown_targets_its_exact_windows_process_tree(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "codex-app-server-fixture"
            root.mkdir()
            server = concrete_transport.CodexAppServer(Path(directory) / "codex.exe", Path(directory) / "auth.json", root, ["codex"], {})
            process = mock.Mock(pid=43210, stdin=None, stdout=None, stderr=None)
            process.poll.side_effect = [None, 0]
            server.process = process
            with mock.patch("codex_app_server.os.name", "nt"), mock.patch("codex_app_server.subprocess.run") as taskkill:
                server.__exit__()
            taskkill.assert_called_once_with(
                ["taskkill", "/PID", "43210", "/T", "/F"],
                capture_output=True, text=True, encoding="utf-8", errors="replace", timeout=5, check=False,
            )
            process.terminate.assert_not_called()
            process.wait.assert_called_once_with(timeout=5)
            self.assertFalse(root.exists())

    def test_retrieval_treats_explicit_null_evidence_as_no_passage_and_reads_full_source(self) -> None:
        client = FakeToolClient()
        client.contents["info-1"] = "A short source that must be read in full."
        original = client.call_tool

        def call_tool(name: str, arguments: dict):
            if name == "ownward_evidence_search":
                return {"evidence": None}
            return original(name, arguments)

        client.call_tool = call_tool  # type: ignore[method-assign]
        runtime = mock.Mock(client=client)
        evidence, retrieval = adapter.passive_retrieve(runtime, "short source", self.protocol)
        self.assertEqual("A short source that must be read in full.", evidence[0]["content"])
        self.assertEqual("full", retrieval["read_paths"][0]["mode"])
        self.assertEqual([], retrieval["evidence_read_ids"])

    def test_app_server_timeout_interrupts_the_exact_fresh_turn(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            work = root / "work"
            work.mkdir()
            server = concrete_transport.CodexAppServer(root / "codex.exe", root / "auth.json", root / "runtime", ["codex"], {})
            responses = [
                {"thread": {"id": "thread-1"}},
                {"turn": {"id": "turn-1"}},
                {},
            ]
            target = mock.Mock()
            target.get.side_effect = [queue.Empty(), queue.Empty()]
            with mock.patch.object(server, "request", side_effect=responses) as request, mock.patch("codex_app_server.queue.Queue", return_value=target):
                with self.assertRaisesRegex(concrete_transport.AppServerTimeout, "timed out"):
                    server.invoke(
                        prompt="prompt", schema={"type": "object"}, model="model", effort="low",
                        work_dir=work, timeout_seconds=1, initial_context="Original evidence",
                    )
            turn_input = request.call_args_list[1].args[1]["input"]
            self.assertEqual([{"type":"text","text":"prompt"},{"type":"text","text":"Original evidence"}], turn_input)
            self.assertEqual(
                mock.call("turn/interrupt", {"threadId": "thread-1", "turnId": "turn-1"}, timeout_seconds=10),
                request.call_args_list[-1],
            )

    def test_app_server_recovers_and_interrupts_a_turn_start_timeout(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            work = root / "work"
            work.mkdir()
            server = concrete_transport.CodexAppServer(root / "codex.exe", root / "auth.json", root / "runtime", ["codex"], {})
            responses = [
                {"thread": {"id": "thread-1"}},
                concrete_transport.AppServerTimeout("turn/start timeout"),
                {"thread": {"turns": [{"id": "turn-1", "status": "inProgress"}]}},
                {},
            ]
            with mock.patch.object(server, "request", side_effect=responses) as request:
                with self.assertRaisesRegex(concrete_transport.AppServerTimeout, "orphan_turn_interrupted=true"):
                    server.invoke(
                        prompt="prompt", schema={"type": "object"}, model="model", effort="low",
                        work_dir=work, timeout_seconds=1,
                    )
            self.assertEqual(
                mock.call("turn/interrupt", {"threadId": "thread-1", "turnId": "turn-1"}, timeout_seconds=10),
                request.call_args_list[-1],
            )

    def test_app_server_pool_allows_only_one_active_turn_per_worker(self) -> None:
        class Worker:
            def __init__(self, index: int, generation: int) -> None:
                self.index = index
                self.generation = generation
                self.active = 0
                self.maximum = 0

            def __enter__(self):
                return self

            def __exit__(self, *_args):
                return None

            def invoke(self, **_request):
                self.active += 1
                self.maximum = max(self.maximum, self.active)
                try:
                    time.sleep(0.02)
                    return {"ok": True}, {}, {"transport": "codex-app-server-stdio"}
                finally:
                    self.active -= 1

            def diagnostics(self):
                return {"rate_limit_observed": False}

        workers: list[Worker] = []

        def factory(index: int, generation: int):
            worker = Worker(index, generation)
            workers.append(worker)
            return worker

        with concrete_transport.CodexAppServerPool(2, factory) as pool:
            with ThreadPoolExecutor(max_workers=4) as executor:
                values = list(executor.map(lambda _: pool.invoke(), range(4)))
            diagnostics = pool.diagnostics()
        self.assertEqual(4, len(values))
        self.assertTrue(all(worker.maximum == 1 for worker in workers))
        self.assertEqual(2, diagnostics["max_active"])
        self.assertEqual(1, diagnostics["per_worker_max_active"])

    def test_app_server_pool_restarts_only_the_failed_worker(self) -> None:
        created: list[tuple[int, int]] = []

        class Worker:
            def __init__(self, index: int, generation: int) -> None:
                self.index = index
                self.generation = generation

            def __enter__(self):
                return self

            def __exit__(self, *_args):
                return None

            def invoke(self, **_request):
                if self.generation == 0:
                    raise concrete_transport.AppServerError("worker failed")
                return {"ok": True}, {}, {"transport": "codex-app-server-stdio"}

            def diagnostics(self):
                return {"rate_limit_observed": False}

        def factory(index: int, generation: int):
            created.append((index, generation))
            return Worker(index, generation)

        with concrete_transport.CodexAppServerPool(1, factory) as pool:
            with self.assertRaises(concrete_transport.AppServerError):
                pool.invoke()
            value, _, metadata = pool.invoke()
            diagnostics = pool.diagnostics()
        self.assertEqual({"ok": True}, value)
        self.assertEqual(1, metadata["pool_worker_generation"])
        self.assertEqual([(0, 0), (0, 1)], created)
        self.assertEqual(1, diagnostics["worker_restarts"])

    def test_app_server_pool_closes_every_worker_before_raising_cleanup_failure(self) -> None:
        closed: list[int] = []

        class Worker:
            def __init__(self, index: int) -> None:
                self.index = index

            def diagnostics(self):
                return {"rate_limit_observed": False}

            def __exit__(self, *_args):
                closed.append(self.index)
                if self.index == 0:
                    raise concrete_transport.AppServerError("cleanup failed")

        pool = concrete_transport.CodexAppServerPool(2, lambda index, _generation: Worker(index))
        pool._workers = {0: Worker(0), 1: Worker(1)}
        with self.assertRaisesRegex(concrete_transport.AppServerError, "cleanup failed"):
            pool.__exit__()
        self.assertEqual([0, 1], closed)
        self.assertEqual({}, pool._workers)

    def test_submission_package_is_deterministic(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            first = root / "a.json"
            second = root / "b.json"
            first.write_text("a\n", encoding="utf-8")
            second.write_text("b\n", encoding="utf-8")
            package = adapter.deterministic_package(root / "submission.zip", [second, first], root)
            digest = adapter.sha256(package)
            adapter.deterministic_package(package, [first, second], root)
            self.assertEqual(digest, adapter.sha256(package))

    def test_checkpoint_manifest_excludes_mutable_product_data_and_binds_agent_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            evidence = root / "questions" / "q" / "reader" / "codex" / "complete.json"
            product = root / "questions" / "q" / "ownward-data" / "store.db"
            evidence.parent.mkdir(parents=True)
            product.parent.mkdir(parents=True)
            evidence.write_text("{}\n", encoding="utf-8")
            product.write_bytes(b"data")
            for name in ("hypotheses.jsonl", "official-evaluation.jsonl", "diagnostics.jsonl", "diagnostic-summary.json"):
                (root / name).write_text("{}\n", encoding="utf-8")
            manifest = adapter.load_json(adapter.checkpoint_manifest(root, "identity"))
            paths = {item["path"] for item in manifest["files"]}
            self.assertIn("questions/q/reader/codex/complete.json", paths)
            self.assertFalse(any("ownward-data" in path for path in paths))

    def test_codex_is_the_only_semantic_reader_and_judge_capability(self) -> None:
        source = Path(adapter.__file__).read_text(encoding="utf-8")
        self.assertNotIn('client.answer(_answer_prompt', source)
        self.assertIn('capability.active_answer,', source)
        self.assertNotIn('evidence, retrieval = retrieve(', source)
        self.assertIn('capability.judge,', source)
        self.assertIn('external_intelligence_scheduler.submit(', source)
        self.assertIn('"capability": {"id": settings.get("capability_id", "codex")', source)
        self.assertNotIn('"/responses"', source)
        self.assertNotIn("OfficialJudgeClient", source)
        self.assertNotIn("OPENAI_API_KEY", source)

    def test_codex_capability_uses_one_app_server_without_ephemeral_cli_processes(self) -> None:
        command = concrete_transport.CodexAppServer.command(["codex"])
        serialized = " ".join(command)
        self.assertIn("app-server", command)
        self.assertNotIn("exec", command)
        self.assertNotIn("--ephemeral", command)
        self.assertNotIn("mcp_servers", serialized)
        self.assertNotIn("OPENAI_API_KEY", serialized)

    def test_app_server_resolves_the_native_codex_process_behind_a_powershell_entry(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            entry = root / "codex.ps1"
            native = root / "node_modules" / "@openai" / "codex" / "node_modules" / "@openai" / "codex-win32-x64" / "vendor" / "target" / "bin" / "codex.exe"
            entry.write_text("wrapper", encoding="utf-8")
            native.parent.mkdir(parents=True)
            native.write_bytes(b"native")
            self.assertEqual([str(native.resolve())], concrete_transport.CodexAppServer.direct_command_prefix(entry, ["pwsh", str(entry)]))


if __name__ == "__main__":
    unittest.main()
