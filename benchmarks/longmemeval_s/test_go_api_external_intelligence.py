from concurrent.futures import ThreadPoolExecutor
import io
import json
from pathlib import Path
import tempfile
import threading
import time
import unittest
from unittest import mock

import go_api_external_intelligence as subject
import external_intelligence_runtime as runtime


SCHEMA = {"type": "object", "additionalProperties": False, "required": ["answer"],
          "properties": {"answer": {"type": "string"}}}
TOOLS = [{"name": "read", "description": "Read the task's fact", "inputSchema": {"type": "object"}}]


def answer(text, **extra):
    return {"model": subject.MODEL, "content": [{"type": "text", "text": text}],
            "stop_reason": "end_turn", "usage": {"input_tokens": 4, "output_tokens": 2}, **extra}


class GoAPIClientTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.auth = self.root / "auth.json"
        self.auth.write_text(json.dumps({"BAILIAN_API_KEY": "test-key"}))
        self.client = subject.GoAPIClient(self.auth, 8, {})

    def invoke(self, **changes):
        return self.client.invoke(**dict(prompt="test", schema=SCHEMA, model=subject.MODEL,
                                        effort="medium", work_dir=self.root / "work", timeout_seconds=5,
                                        **changes))

    def test_tool_loop_preserves_context_and_accounts_usage(self):
        requests = []
        def post(body, session, deadline, path):
            requests.append(body)
            if len(requests) == 1:
                return answer("", content=[{"type": "thinking", "thinking": "check", "signature": "sig"},
                                           {"type": "tool_use", "id": "t1", "name": "read", "input": {}}], stop_reason="tool_use")
            self.assertEqual("sig", body["messages"][1]["content"][0]["signature"])
            self.assertIn("private fact", body["messages"][2]["content"][0]["content"])
            return answer('{"answer":"private fact"}')
        with mock.patch.object(self.client, "_post", side_effect=post):
            value, usage, _ = self.invoke(dynamic_tools=TOOLS, tool_handler=lambda *_: "private fact", base_instructions="product rules")
        self.assertEqual({"answer": "private fact"}, value)
        self.assertEqual(8, usage["input_tokens"])
        self.assertEqual("medium", requests[0]["output_config"]["effort"])
        self.assertTrue(requests[0]["system"][0]["text"].startswith("product rules"))
        self.assertNotIn("test-key", "".join(p.read_text() for p in (self.root / "work").glob("*.json")))

    def test_blank_or_old_credentials_fail_before_any_network_request(self):
        for content in ('{"BAILIAN_API_KEY":""}', '{"BAILIAN_API_KEY":"  "}',
                        '{"opencode-go":{"type":"api","key":"old-key"}}', 'invalid json'):
            self.auth.write_text(content)
            with mock.patch.object(subject.http.client, "HTTPSConnection") as connection:
                with self.assertRaisesRegex(subject.ExternalIntelligenceError, "BAILIAN_API_KEY"):
                    subject.GoAPIClient(self.auth, 1, {})
                connection.assert_not_called()

    def test_windows_edited_credential_accepts_bom_and_surrounding_whitespace(self):
        self.auth.write_text('{"BAILIAN_API_KEY":" test-key \\n"}', encoding="utf-8-sig")
        client = subject.GoAPIClient(self.auth, 1, {})
        self.assertEqual("test-key", client._key)

    def test_single_format_correction_disables_tools_and_keeps_session(self):
        seen = []
        def post(body, session, deadline, path):
            seen.append((body, session))
            return answer("not json" if len(seen) == 1 else '{"answer":"ok"}')
        with mock.patch.object(self.client, "_post", side_effect=post):
            _, usage, _ = self.invoke(dynamic_tools=TOOLS, tool_handler=lambda *_: {})
        self.assertEqual(1, usage["format_corrections"])
        self.assertEqual(seen[0][1], seen[1][1])
        self.assertNotIn("tools", seen[1][0])
        with mock.patch.object(self.client, "_post", return_value=answer("not json")) as post:
            with self.assertRaisesRegex(subject.ExternalIntelligenceError, "after one correction"):
                self.invoke()
            self.assertEqual(2, post.call_count)

    def test_unavailable_tool_never_runs(self):
        reply = answer("", content=[{"type": "tool_use", "id": "t1", "name": "delete", "input": {}}])
        handler = mock.Mock()
        with mock.patch.object(self.client, "_post", return_value=reply):
            with self.assertRaisesRegex(subject.ExternalIntelligenceError, "unavailable tool"):
                self.invoke(dynamic_tools=TOOLS, tool_handler=handler)
        handler.assert_not_called()

    def test_eight_concurrent_contexts_are_isolated_without_processes(self):
        barrier = threading.Barrier(8)
        sessions = set()
        def post(body, session, deadline, path):
            sessions.add(session)
            barrier.wait(timeout=4)
            return answer(json.dumps({"answer": body["messages"][0]["content"][0]["text"]}))
        def work(i):
            return self.client.invoke(prompt=str(i), schema=SCHEMA, model=subject.MODEL, effort="xhigh",
                                      work_dir=self.root / str(i), timeout_seconds=5)[0]
        with mock.patch.object(self.client, "_post", side_effect=post), ThreadPoolExecutor(max_workers=8) as pool:
            values = list(pool.map(work, range(8)))
        self.assertEqual([{"answer": str(i)} for i in range(8)], values)
        self.assertEqual(8, len(sessions))
        self.assertEqual(8, self.client.diagnostics()["max_active"])
        self.assertEqual(0, self.client.diagnostics()["process_starts"])

    def test_stream_reassembles_tools_and_preserves_partial_failure_trace(self):
        events = [
            {"type": "message_start", "message": {"model": subject.MODEL, "usage": {"input_tokens": 10}}},
            {"type": "content_block_start", "index": 0, "content_block": {"type": "tool_use", "id": "t", "name": "read", "input": {}}},
            {"type": "content_block_delta", "index": 0, "delta": {"type": "input_json_delta", "partial_json": '{"id":'}},
            {"type": "content_block_delta", "index": 0, "delta": {"type": "input_json_delta", "partial_json": '"one"}'}},
            {"type": "message_delta", "delta": {"stop_reason": "tool_use"}, "usage": {"output_tokens": 5}},
            {"type": "message_stop"},
        ]
        connection = mock.Mock(sock=None)
        def response(data):
            stream = io.BytesIO(b"".join(b"data: " + json.dumps(x).encode() + b"\n\n" for x in data))
            stream.status = 200
            return stream
        with mock.patch.object(subject.http.client, "HTTPSConnection", return_value=connection) as https:
            connection.getresponse.return_value = response(events)
            value = self.client._post({}, "session", time.monotonic()+5, self.root / "response.json")
            self.assertEqual("token-plan.cn-beijing.maas.aliyuncs.com", https.call_args.args[0])
            method, endpoint, _, headers = connection.request.call_args.args
            self.assertEqual(("POST", "/apps/anthropic/v1/messages"), (method, endpoint))
            self.assertEqual("test-key", headers["x-api-key"])
            self.assertNotIn("x-opencode-session", headers)
            self.assertEqual({"id": "one"}, value["content"][0]["input"])
            self.assertEqual(5, value["usage"]["output_tokens"])
            connection.getresponse.return_value = response(events[:-1])
            with self.assertRaisesRegex(subject.ExternalIntelligenceError, "before message_stop"):
                self.client._post({}, "session", time.monotonic()+5, self.root / "partial.json")
        self.assertTrue((self.root / "partial.events.jsonl").is_file())
        self.assertFalse((self.root / "partial.json").exists())

    def test_http_failure_and_deadline_release_capacity(self):
        connection = mock.Mock(sock=None)
        connection.getresponse.return_value.status = 429
        with mock.patch.object(subject.http.client, "HTTPSConnection", return_value=connection):
            with self.assertRaisesRegex(subject.ExternalIntelligenceError, "HTTP 429"):
                self.invoke()
        self.assertTrue(self.client.diagnostics()["rate_limit_observed"])
        self.assertEqual(0, self.client.diagnostics()["active_turns"])
        with self.assertRaises(subject.ExternalIntelligenceTimeout):
            self.client._remaining(time.monotonic()-1)

    def test_timeout_exposes_only_unfinished_thinking_and_keeps_trace(self):
        for failure in (subject.socket.timeout(), subject.ExternalIntelligenceTimeout('deadline')):
            with self.subTest(failure=type(failure).__name__):
                events = [
                    {'type': 'content_block_start', 'index': 0,
                     'content_block': {'type': 'thinking', 'thinking': '', 'signature': 'private-signature'}},
                    {'type': 'content_block_delta', 'index': 0,
                     'delta': {'type': 'thinking_delta', 'thinking': 'useful test-key progress'}},
                ]
                response = mock.Mock(status=200)
                response.readline.side_effect = [
                    b'data: '+json.dumps(event).encode()+b'\n' for event in events] + [failure]
                connection = mock.Mock(sock=None)
                connection.getresponse.return_value = response
                with mock.patch.object(subject.http.client, 'HTTPSConnection', return_value=connection):
                    with self.assertRaises(subject.ExternalIntelligenceTimeout) as raised:
                        self.invoke()
                self.assertEqual('useful [redacted] progress', raised.exception.working_notes)
                self.assertTrue((self.root/'work/response-001.events.jsonl').exists())
                self.assertFalse((self.root/'work/response-001.json').exists())
                self.assertEqual(0, self.client.diagnostics()['active_turns'])

    def test_sse_http_error_records_service_reason_without_key(self):
        response = io.BytesIO(b'event:error\ndata:{"code":"InvalidParameter","message":"inspection rejected test-key"}\n\n')
        response.status = 400
        connection = mock.Mock(sock=None)
        connection.getresponse.return_value = response
        with mock.patch.object(subject.http.client, "HTTPSConnection", return_value=connection):
            with self.assertRaisesRegex(subject.ExternalIntelligenceError, "HTTP 400"):
                self.invoke()
        saved = (self.root / "work/response-001.error.json").read_text()
        self.assertIn("InvalidParameter", saved)
        self.assertNotIn("test-key", saved)

    def test_local_transport_failure_preserves_operation_trace(self):
        connection = mock.Mock(sock=None)
        connection.request.side_effect = FileNotFoundError(2, "missing", "fixture-path")
        with mock.patch.object(subject.http.client, "HTTPSConnection", return_value=connection):
            with self.assertRaisesRegex(subject.ExternalIntelligenceError, "FileNotFoundError"):
                self.invoke()
        saved = json.loads((self.root / "work/response-001.transport-error.json").read_text())
        self.assertEqual("fixture-path", saved["filename"])
        self.assertTrue(saved["trace"])

    def test_handshake_retry_keeps_reader_context_without_resending_a_model_request(self):
        def stream(content, stop):
            events = [
                {"type": "message_start", "message": {"model": subject.MODEL}},
                {"type": "content_block_start", "index": 0, "content_block": content},
                {"type": "message_delta", "delta": {"stop_reason": stop}},
                {"type": "message_stop"},
            ]
            value = io.BytesIO(b"".join(b"data: " + json.dumps(event).encode() + b"\n\n" for event in events))
            value.status = 200
            return value
        first, broken, last = (mock.Mock(sock=None) for _ in range(3))
        first.getresponse.return_value = stream({"type": "tool_use", "id": "t", "name": "read", "input": {}}, "tool_use")
        broken.connect.side_effect = FileNotFoundError(2, "handshake failed")
        last.getresponse.return_value = stream({"type": "text", "text": '{"answer":"ok"}'}, "end_turn")
        handler = mock.Mock(return_value="fact")
        with mock.patch.object(subject.http.client, "HTTPSConnection", side_effect=[first, broken, last]), mock.patch.object(subject.time, "sleep"):
            value, _, metadata = self.invoke(dynamic_tools=TOOLS, tool_handler=handler)
        self.assertEqual({"answer": "ok"}, value)
        self.assertEqual(2, metadata["api_requests"])
        handler.assert_called_once()
        broken.request.assert_not_called()
        final_body = json.loads(last.request.call_args.args[2])
        self.assertEqual("tool_result", final_body["messages"][-1]["content"][0]["type"])
        self.assertTrue((self.root / "work/response-002.connection-retries.json").exists())

    def test_handshake_retry_is_bounded_and_releases_capacity(self):
        connections = [mock.Mock(sock=None) for _ in range(3)]
        for connection in connections:
            connection.connect.side_effect = FileNotFoundError(2, "handshake failed")
        with mock.patch.object(subject.http.client, "HTTPSConnection", side_effect=connections), mock.patch.object(subject.time, "sleep"):
            with self.assertRaisesRegex(subject.ExternalIntelligenceError, "FileNotFoundError"):
                self.invoke()
        for connection in connections:
            connection.request.assert_not_called()
        self.assertEqual(0, self.client.diagnostics()["active_turns"])

    def test_runtime_uses_same_roles_and_one_host_process(self):
        identity = runtime.current_runtime_identity(driver=subject.DRIVER, binary=Path(subject.__file__),
                                                   credential_file=self.auth, max_active=8, worker_processes=8)
        self.assertEqual(1, identity["worker_processes"])
        self.assertEqual(8, identity["max_active"])
        self.assertEqual("aliyun-bailian", identity["provider"])
        self.assertEqual("opencode-go", runtime.selected_implementation("opencode-server/v1")["provider"])
        self.assertEqual("medium", runtime.selected_role_profile(subject.DRIVER)["semantic"]["reasoning_effort"])
        self.assertEqual("xhigh", runtime.selected_role_profile("opencode-server/v1")["semantic"]["reasoning_effort"])


class ServiceRoutingTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / "bailian.json").write_text('{"BAILIAN_API_KEY":"bailian-secret"}')
        (self.root / "go.json").write_text('{"opencode-go":{"type":"api","key":"go-secret"}}')
        self.config = {
            "schema": "ownward.messages-services/v1", "primary": "bailian",
            "fallback": {"service": "go", "on_error_codes": ["data_inspection_failed"]},
            "services": {
                "bailian": {"url": "https://token-plan.cn-beijing.maas.aliyuncs.com/apps/anthropic/v1/messages",
                            "credential_file": "bailian.json", "key_path": ["BAILIAN_API_KEY"]},
                "go": {"url": "https://opencode.ai/zen/go/v1/messages", "credential_file": "go.json",
                       "key_path": ["opencode-go", "key"], "session_header": "x-opencode-session"},
            },
        }
        self.path = self.root / "services.json"
        self.client = self.make_client()

    def make_client(self):
        self.path.write_text(json.dumps(self.config))
        return subject.GoAPIClient(self.path, 8, {"unchanged": True})

    def invoke(self, client, label, **extra):
        return client.invoke(prompt=label, schema=SCHEMA, model=subject.MODEL, effort="medium",
                             work_dir=self.root / label, timeout_seconds=5, **extra)

    @staticmethod
    def stream(content, stop="end_turn"):
        events = [{"type": "message_start", "message": {"model": subject.MODEL}},
                  {"type": "content_block_start", "index": 0, "content_block": content},
                  {"type": "message_delta", "delta": {"stop_reason": stop}}, {"type": "message_stop"}]
        stream = io.BytesIO(b"".join(b"data: " + json.dumps(event).encode() + b"\n\n" for event in events))
        stream.status = 200
        return stream

    def test_same_question_stays_on_fallback_next_question_restarts_primary(self):
        routes = []
        def post(body, session, deadline, path, *, service="bailian"):
            routes.append((body["messages"][0]["content"][0]["text"], service))
            if len(routes) == 1:
                raise subject.ServiceError("rejected", "data_inspection_failed")
            return answer('{"answer":"ok"}')
        wrapped = runtime._StableTransport(self.client, subject)
        first = wrapped.new_scope()
        with mock.patch.object(self.client, "_post", side_effect=post):
            self.invoke(first, "q1-semantic")
            self.invoke(first, "q1-reader")
            self.invoke(first, "q1-judge")
            self.invoke(wrapped.new_scope(), "q2-semantic")
        self.assertEqual([("q1-semantic", "bailian"), ("q1-semantic", "go"),
                          ("q1-reader", "go"), ("q1-judge", "go"), ("q2-semantic", "bailian")], routes)
        self.assertEqual(first.identity, wrapped.identity)

    def test_switch_after_tool_result_keeps_context_deadline_and_does_not_repeat_tools(self):
        requests = []
        def post(body, session, deadline, path, *, service="bailian"):
            requests.append((body, session, deadline, service))
            if len(requests) == 1:
                return answer("", content=[{"type": "tool_use", "id": "t1", "name": "read", "input": {}}], stop_reason="tool_use")
            if service == "bailian":
                raise subject.ServiceError("rejected", "data_inspection_failed")
            return answer('{"answer":"fact"}')
        handler = mock.Mock(return_value="fact")
        with mock.patch.object(self.client, "_post", side_effect=post):
            value, usage, metadata = self.invoke(self.client.new_scope(), "tools", dynamic_tools=TOOLS,
                                                 tool_handler=handler, base_instructions="Ownward rules")
        handler.assert_called_once()
        self.assertEqual(requests[1][:3], requests[2][:3])
        self.assertEqual("fact", value["answer"])
        self.assertEqual(3, metadata["api_requests"])
        self.assertEqual(8, usage["input_tokens"])
        self.assertTrue(requests[2][0]["system"][0]["text"].startswith("Ownward rules"))
        self.assertTrue((self.root / "tools/response-002.route.json").is_file())

    def test_concurrent_questions_and_parallel_units_do_not_leak_routes(self):
        barrier = threading.Barrier(8)
        requests = []
        lock = threading.Lock()
        def post(body, session, deadline, path, *, service="bailian"):
            prompt = body["messages"][0]["content"][0]["text"]
            with lock:
                requests.append((prompt, service))
            if prompt.startswith("start") and service == "bailian":
                barrier.wait(timeout=4)
                if prompt in {"start-0", "start-1"}:
                    raise subject.ServiceError("rejected", "data_inspection_failed")
            return answer('{"answer":"ok"}')
        scopes = [self.client.new_scope() for _ in range(7)]
        # Two simultaneous rejected units belong to the same question.
        jobs = [(scopes[0], "start-0"), (scopes[0], "start-1")] + [(scopes[i], f"start-{i+1}") for i in range(1, 7)]
        with mock.patch.object(self.client, "_post", side_effect=post), ThreadPoolExecutor(max_workers=8) as pool:
            list(pool.map(lambda job: self.invoke(*job), jobs))
            for i, scope in enumerate(scopes):
                self.invoke(scope, f"after-{i}")
        self.assertEqual([("after-0", "go")] + [(f"after-{i}", "bailian") for i in range(1, 7)],
                         [item for item in requests if item[0].startswith("after")])
        self.assertEqual(0, self.client.diagnostics()["active_turns"])
        self.assertEqual(0, self.client.diagnostics()["server_processes"])

    def test_only_configured_error_codes_switch_and_fallback_failure_is_bounded(self):
        for code in ("", "rate_limit_error", "invalid_api_key", "server_error"):
            with self.subTest(code=code), mock.patch.object(self.client, "_post", side_effect=subject.ServiceError("failed", code)) as post:
                with self.assertRaises(subject.ExternalIntelligenceError):
                    self.invoke(self.client.new_scope(), f"error-{code}")
                self.assertEqual(1, post.call_count)
        with mock.patch.object(self.client, "_post", side_effect=subject.ServiceError("rejected", "data_inspection_failed")) as post:
            with self.assertRaises(subject.ExternalIntelligenceError):
                self.invoke(self.client.new_scope(), "both-rejected")
            self.assertEqual(2, post.call_count)

    def test_either_service_works_alone_without_loading_unused_credentials(self):
        for name, unused in (("bailian", "go"), ("go", "bailian")):
            with self.subTest(name=name):
                self.config["primary"] = name
                self.config.pop("fallback", None)
                saved = self.config["services"][unused]["credential_file"]
                self.config["services"][unused]["credential_file"] = "missing.json"
                client = self.make_client()
                with mock.patch.object(client, "_post", return_value=answer('{"answer":"ok"}')) as post:
                    self.assertEqual("ok", self.invoke(client, f"alone-{name}")[0]["answer"])
                self.assertEqual(1, post.call_count)
                self.config["services"][unused]["credential_file"] = saved

    def test_http_and_sse_rejections_use_independent_credentials_and_keep_error_trace(self):
        for streaming in (False, True):
            with self.subTest(streaming=streaming):
                error = {"code": "data_inspection_failed", "message": "rejected bailian-secret"}
                if streaming:
                    rejected = io.BytesIO(b'data: ' + json.dumps({"type": "error", "error": error}).encode() + b'\n\n')
                    rejected.status = 200
                else:
                    rejected = io.BytesIO(json.dumps({"error": error}).encode())
                    rejected.status = 400
                primary, fallback = mock.Mock(sock=None), mock.Mock(sock=None)
                primary.getresponse.return_value = rejected
                fallback.getresponse.return_value = self.stream({"type": "text", "text": '{"answer":"ok"}'})
                label = f"http-{streaming}"
                with mock.patch.object(subject.http.client, "HTTPSConnection", side_effect=[primary, fallback]) as https:
                    self.assertEqual("ok", self.invoke(self.client.new_scope(), label)[0]["answer"])
                self.assertEqual(["token-plan.cn-beijing.maas.aliyuncs.com", "opencode.ai"], [call.args[0] for call in https.call_args_list])
                self.assertEqual("/zen/go/v1/messages", fallback.request.call_args.args[1])
                self.assertEqual("bailian-secret", primary.request.call_args.args[3]["x-api-key"])
                self.assertEqual("go-secret", fallback.request.call_args.args[3]["x-api-key"])
                self.assertIn("x-opencode-session", fallback.request.call_args.args[3])
                self.assertNotIn("x-opencode-session", primary.request.call_args.args[3])
                artifacts = "".join(path.read_text() for path in (self.root / label).iterdir())
                self.assertNotIn("bailian-secret", artifacts)
                self.assertNotIn("go-secret", artifacts)
                self.assertIn("data_inspection_failed", artifacts)

    def test_direct_invocations_start_primary_without_an_explicit_task_scope(self):
        calls = []
        def post(body, session, deadline, path, *, service="bailian"):
            calls.append(service)
            if service == "bailian":
                raise subject.ServiceError("rejected", "data_inspection_failed")
            return answer('{"answer":"ok"}')
        with mock.patch.object(self.client, "_post", side_effect=post):
            self.invoke(self.client, "direct-1")
            self.invoke(self.client, "direct-2")
        self.assertEqual(["bailian", "go", "bailian", "go"], calls)

    def test_gateway_wrapped_structured_rejection_switches_without_matching_prose(self):
        for prefix, streaming in (("", False), ("data: ", False), ("", True), ("data: ", True)):
            inner = {"error": {"code": "data_inspection_failed", "message": "rejected"}}
            wrapped = {"code": "InvalidParameter", "message": prefix + json.dumps(inner)}
            rejected = io.BytesIO((b'data: {"type":"ping"}\n\ndata: ' + json.dumps(wrapped).encode() + b'\n\n')
                                  if streaming else json.dumps({"error": wrapped}).encode())
            rejected.status = 200 if streaming else 400
            primary, fallback = mock.Mock(sock=None), mock.Mock(sock=None)
            primary.getresponse.return_value = rejected
            fallback.getresponse.return_value = self.stream({"type": "text", "text": '{"answer":"ok"}'})
            with mock.patch.object(subject.http.client, "HTTPSConnection", side_effect=[primary, fallback]) as https:
                self.assertEqual("ok", self.invoke(self.client.new_scope(), "wrapped-" + str(len(prefix)) + str(streaming))[0]["answer"])
            self.assertEqual(["token-plan.cn-beijing.maas.aliyuncs.com", "opencode.ai"],
                             [call.args[0] for call in https.call_args_list])
        self.assertEqual("InvalidParameter", subject._error_code(
            {"code": "InvalidParameter", "message": "This is not data_inspection_failed"}))


if __name__ == "__main__":
    unittest.main()
