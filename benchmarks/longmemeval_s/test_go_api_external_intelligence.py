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

    def test_complete_json_fence_preserves_delivery_without_model_rewrite(self):
        value = {'answer': 'Established fact\n```literal content```'}
        for opening, newline in [('```json', '\n'), ('```', '\r\n')]:
            with self.subTest(opening=opening):
                encoded = opening + newline + json.dumps(value) + newline + '```'
                with mock.patch.object(self.client, '_post', return_value=answer(encoded)) as post:
                    actual, usage, _ = self.invoke()
                self.assertEqual(value, actual)
                self.assertEqual(0, usage['format_corrections'])
                post.assert_called_once()

    def test_json_fence_does_not_bypass_schema_or_extract_partial_output(self):
        invalid = [
            '```json\n{"answer": 7}\n```',
            '```json\n{"answer": "ok", "extra": true}\n```',
            '```json\n{"answer": "a"}\n{"answer": "b"}\n```',
            'commentary\n```json\n{"answer": "ok"}\n```',
            '```json\n{"answer": "ok"}\n```\ncommentary',
            '```json\nnot json\n```',
        ]
        for encoded in invalid:
            with self.subTest(encoded=encoded):
                replacement = '{"/answer":"repaired"}' if encoded == invalid[0] else '{"answer":"repaired"}'
                with mock.patch.object(self.client, '_post', side_effect=[answer(encoded), answer(replacement)]) as post:
                    actual, usage, _ = self.invoke()
                self.assertEqual({'answer': 'repaired'}, actual)
                self.assertEqual(1, usage['format_corrections'])
                self.assertEqual(2, post.call_count)

    def test_field_repair_finds_all_errors_and_preserves_valid_content(self):
        schema = {'type': 'object', 'required': ['rows'], 'additionalProperties': False,
                  'properties': {'rows': {'type': 'array', 'items': {
                      'type': 'object', 'additionalProperties': False, 'required': ['text', 'context', 'kind'],
                      'properties': {'text': {'type': 'string'},
                                     'context': {'type': 'array', 'items': {'type': 'integer'}},
                                     'kind': {'type': 'string', 'maxLength': 12}}}}}}
        original = {'rows': [{'text': 'Keep all original evidence.', 'context': [8], 'kind': 'fact'},
                             {'text': 'Keep this evidence too.', 'context': '11', 'kind': 'overlong category name'}]}
        corrections = {'/rows/1/context': [11], '/rows/1/kind': 'event'}
        with mock.patch.object(self.client, '_post', side_effect=[answer(json.dumps(original)), answer(json.dumps(corrections))]) as post:
            actual, usage, _ = self.client.invoke(prompt='organize', schema=schema, model=subject.MODEL,
                effort='medium', work_dir=self.root / 'repair', timeout_seconds=5)
        self.assertEqual(original['rows'][0], actual['rows'][0])
        self.assertEqual(original['rows'][1]['text'], actual['rows'][1]['text'])
        self.assertEqual([11], actual['rows'][1]['context'])
        self.assertEqual('event', actual['rows'][1]['kind'])
        self.assertEqual('11', original['rows'][1]['context'])
        system = post.call_args.args[0]['system'][0]['text']
        self.assertIn('/rows/1/context', system)
        self.assertIn('/rows/1/kind', system)
        self.assertNotIn('/rows/0', system)
        self.assertEqual(1, usage['format_corrections'])
        self.assertEqual(2, post.call_count)

    def test_field_repair_rejects_changes_outside_invalid_fields(self):
        schema = {'type': 'object', 'additionalProperties': False, 'required': ['answer', 'count'],
                  'properties': {'answer': {'type': 'string'}, 'count': {'type': 'integer'}}}
        replies = [answer('{"answer":"original evidence","count":"two"}'),
                   answer('{"/answer":"changed evidence","/count":2}')]
        with mock.patch.object(self.client, '_post', side_effect=replies) as post:
            with self.assertRaisesRegex(subject.ExternalIntelligenceError, 'after one correction'):
                self.client.invoke(prompt='test', schema=schema, model=subject.MODEL,
                    effort='medium', work_dir=self.root / 'invalid-repair', timeout_seconds=5)
        self.assertEqual(2, post.call_count)

    def test_field_repair_keeps_full_schema_validation_and_handles_escaped_keys(self):
        schema = {'type': 'object', 'required': ['a/b~c'], 'additionalProperties': False,
                  'properties': {'a/b~c': {'type': 'integer', 'minimum': 0}}}
        for replacement, succeeds in [({'/a~1b~0c': 2}, True), ({'/a~1b~0c': -1}, False)]:
            with self.subTest(replacement=replacement), mock.patch.object(self.client, '_post', side_effect=[
                    answer('{"a/b~c":"two"}'), answer(json.dumps(replacement))]):
                invoke = lambda: self.client.invoke(prompt='test', schema=schema, model=subject.MODEL,
                    effort='medium', work_dir=self.root / 'escaped', timeout_seconds=5)
                if succeeds:
                    self.assertEqual({'a/b~c': 2}, invoke()[0])
                else:
                    with self.assertRaisesRegex(subject.ExternalIntelligenceError, 'after one correction'):
                        invoke()

    def test_truncated_batch_repairs_only_missing_record(self):
        schema = {'type': 'object', 'additionalProperties': False, 'required': ['rows'],
                  'properties': {'rows': {'type': 'array', 'minItems': 3, 'maxItems': 3,
                                         'items': {'type': 'string'}}}}
        first = '{"rows":["first untouched","second untouched",'
        with mock.patch.object(self.client, '_post', side_effect=[answer(first), answer('{"/rows/2":"third"}')]) as post:
            value, _, _ = self.client.invoke(prompt='test', schema=schema, model=subject.MODEL,
                effort='medium', work_dir=self.root / 'truncated', timeout_seconds=5)
        self.assertEqual({'rows': ['first untouched', 'second untouched', 'third']}, value)
        self.assertIn('/rows/2', post.call_args.args[0]['system'][0]['text'])
        self.assertNotIn('/rows/0', post.call_args.args[0]['system'][0]['text'])
        self.assertEqual(2, post.call_count)
        self.assertIsNone(subject._partial_array_output('{"rows":[', schema))
        self.assertIsNone(subject._partial_array_output('{"other":["one",', schema))
        self.assertIsNone(subject._partial_array_output('{"rows":["one","two","three",', schema))

    def test_interrupted_complete_batch_requires_an_unambiguous_complete_envelope(self):
        schema = {'type': 'object', 'additionalProperties': False, 'required': ['rows'],
                  'properties': {'rows': {'type': 'array', 'minItems': 2, 'maxItems': 2,
                                         'items': {'type': 'string'}}}}
        full = {'rows': ['first unchanged', 'second unchanged']}
        self.assertEqual(full, subject._partial_array_output(json.dumps(full), schema, allow_complete=True))
        self.assertIsNone(subject._partial_array_output(json.dumps(full), schema))
        for text in ('{"rows":["first","second",',
                     '{"rows":["first","second","extra"]}', '{"rows":["first","second"],"extra":true}',
                     '{"rows":["first","second"]} trailing',
                     '{"rows":["first","second"],"rows":["different","second"]}'):
            with self.subTest(text=text):
                self.assertIsNone(subject._partial_array_output(text, schema, allow_complete=True))
        invalid = {'rows': ['first unchanged', 3]}
        retained = subject._partial_array_output(json.dumps(invalid), schema, allow_complete=True)
        self.assertEqual(invalid, retained)
        with self.assertRaises(subject.ExternalIntelligenceError):
            subject._validate_schema(retained, schema)
        single = {**schema, 'properties': {'rows': {**schema['properties']['rows'], 'minItems': 1, 'maxItems': 1}}}
        self.assertEqual({'rows': ['only source']}, subject._partial_array_output(
            '{"rows":["only source"]}', single, allow_complete=True))

    def test_every_batch_truncation_retains_exactly_the_closed_source_records(self):
        for count in (1, 3):
            rows = [{'index': i, 'text': 'Original 原文, quotes " and \\ escapes; ]} and \u0085\u2028\u2029 are content.',
                     'nested': [{'value': i + 12}, [True, None]]} for i in range(count)]
            schema = {'type': 'object', 'additionalProperties': False, 'required': ['analyses'],
                      'properties': {'analyses': {'type': 'array', 'minItems': count, 'maxItems': count,
                                                 'items': {'type': 'object'}}}}
            for indent in (None, 2):
                text, ends = '{"analyses":[\n', []
                for index, row in enumerate(rows):
                    text += (',\n' if index else '') + json.dumps(row, ensure_ascii=False, indent=indent)
                    ends.append(len(text))
                text += '\n]}'
                for opening in ('', '```json\n', '```\r\n', '```JSON\r'):
                    wrapped = opening + text + ('\n```' if opening else '')
                    for cut in range(len(wrapped) + 1):
                        with self.subTest(count=count, indent=indent, opening=opening, cut=cut):
                            expected = sum(end + len(opening) <= cut for end in ends)
                            kept = subject._partial_array_output(wrapped[:cut], schema, allow_complete=True)
                            if expected:
                                self.assertEqual({'analyses': rows[:expected] + [None] * (count - expected)}, kept)
                            else:
                                self.assertIsNone(kept)
                for suffix in ('', '}', ']}'):
                    self.assertEqual({'analyses': rows}, subject._partial_array_output(
                        text + suffix, schema, allow_complete=True))
                self.assertEqual({'analyses': rows}, subject._partial_array_output(
                    '```json\n' + text + '\n```', schema, allow_complete=True))

    def test_short_batches_do_not_hide_conflicting_or_unrequested_envelope_fields(self):
        schema = {'type': 'object', 'additionalProperties': False, 'required': ['rows'],
                  'properties': {'rows': {'type': 'array', 'minItems': 2, 'maxItems': 2,
                                         'items': {'type': 'string'}}}}
        for text in ('{"rows":["keep"],"extra":true}',
                     '{"rows":["keep"],"rows":["different","second"]}',
                     '{"rows":["keep"]} trailing'):
            with self.subTest(text=text):
                self.assertIsNone(subject._partial_array_output(text, schema, allow_complete=True))

    def test_cut_markdown_wrappers_do_not_change_content_or_accept_other_wrappers(self):
        value = {'rows': ['literal ```json\n``` and escaped " quote']}
        schema = {'type': 'object', 'additionalProperties': False, 'required': ['rows'],
                  'properties': {'rows': {'type': 'array', 'minItems': 1, 'maxItems': 1,
                                         'items': {'type': 'string'}}}}
        text = json.dumps(value)
        for ending in ('', '\n', '\n`', '\n``', '\n```'):
            self.assertEqual(value, subject._partial_array_output(
                '```json\n' + text + ending, schema, allow_complete=True))
        for encoded in ('prose\n' + text, '```python\n' + text, '```j\u017fon\n' + text, '```json\n' + text + '\n``` prose',
                        '```json\n' + text + '\n```\n{}', '```json\n' + text + '\ntrailing'):
            with self.subTest(encoded=encoded):
                self.assertIsNone(subject._partial_array_output(encoded, schema, allow_complete=True))
        # Normal successful responses still require the complete Markdown fence.
        self.assertEqual('```json\n' + text, subject._json_response_text('```json\n' + text))

    def test_markdown_removes_only_wrapper_bytes(self):
        content = 'Unicode \u0085\u2028\u2029; escapes \r\n\t\b\f\v\x1c\x1d\x1e\\"; literal ```json'
        for newline in ('\n', '\r\n', '\r'):
            for indent in (None, 2):
                # JSON whitespace and string values must both remain unchanged.
                body = json.dumps({'key\u2028': [content]}, ensure_ascii=False, indent=indent).replace('\n', newline)
                for opening in ('```json', '```', '```JSON'):
                    for suffix in ('', newline, newline + '`', newline + '``', newline + '```'):
                        encoded = opening + newline + body + suffix
                        with self.subTest(newline=newline, indent=indent, opening=opening, suffix=suffix):
                            self.assertEqual(body, subject._json_response_text(encoded, allow_incomplete=True))
                            if suffix == newline + '```':
                                self.assertEqual(body, subject._json_response_text(encoded))
                self.assertEqual(body, subject._json_response_text(body))

    def test_unicode_body_survives_normal_interrupted_and_correction_delivery(self):
        content = 'before\u0085\u2028\u2029after; literal ``` and escaped\nnewline'
        full = {'rows': ['first', content]}
        schema = {'type': 'object', 'additionalProperties': False, 'required': ['rows'],
                  'properties': {'rows': {'type': 'array', 'minItems': 2, 'maxItems': 2,
                                         'items': {'type': 'string'}}}}
        for newline in ('\n', '\r\n', '\r'):
            for mode in ('normal', 'interrupted', 'correction'):
                encoded = json.dumps({'/rows/1': content} if mode == 'correction' else full, ensure_ascii=False)
                text = '```json' + newline + encoded + newline + ('```' if mode == 'normal' else '``')
                error = subject.ExternalIntelligenceTimeout('interrupted')
                error.partial_message = answer(text)
                replies = ([answer(text)] if mode == 'normal' else
                           [answer(json.dumps({'rows': ['first', 1]})), error] if mode == 'correction' else [error])
                with self.subTest(mode=mode, newline=newline), mock.patch.object(self.client, '_post', side_effect=replies) as post:
                    params = dict(prompt='test', schema=schema, model=subject.MODEL, effort='medium',
                                  work_dir=self.root / newline.encode().hex() / mode, timeout_seconds=5)
                    if mode == 'normal':
                        value, _, _ = self.client.invoke(**params)
                    else:
                        with self.assertRaises(subject.ExternalIntelligenceError) as caught:
                            self.client.invoke(**params)
                        value = caught.exception.partial_output
                    self.assertEqual(full, value)
                    self.assertEqual(2 if mode == 'correction' else 1, post.call_count)

    def test_cut_fenced_corrections_preserve_only_closed_fields(self):
        original = {'rows': [None, None]}
        fields = [(['rows', i], {'type': 'string'}) for i in range(2)]
        schema = {'type': 'object', 'additionalProperties': False,
                  'properties': {subject._field_pointer(path): rule for path, rule in fields}}
        text = '{"/rows/0":"literal ```", "/rows/1":"second"}'
        ends = [text.index(', '), len(text) - 1]
        for opening in ('', '```json\n', '```\n'):
            wrapped = opening + text + ('\n```' if opening else '')
            for cut in range(len(wrapped) + 1):
                expected = sum(end + len(opening) <= cut for end in ends)
                kept = subject._retained_field_corrections(wrapped[:cut], original, fields, schema)
                with self.subTest(opening=opening, cut=cut):
                    self.assertEqual({'rows': ['literal ```', 'second'][:expected] + [None] * (2 - expected)}
                                     if expected else None, kept)
        self.assertEqual({'rows': [None, None]}, original)

    def test_interrupted_field_repair_retains_only_valid_requested_corrections(self):
        schema = {'type': 'object', 'additionalProperties': False, 'required': ['rows'],
                  'properties': {'rows': {'type': 'array', 'minItems': 3, 'maxItems': 3,
                                         'items': {'type': 'string'}}}}
        original = {'rows': ['unchanged first', 2, 3]}
        cases = [({'/rows/1': 'fixed second', '/rows/2': 'fixed third'},
                  {'rows': ['unchanged first', 'fixed second', 'fixed third']}),
                 ({'/rows/1': 'fixed second', '/rows/2': 3},
                  {'rows': ['unchanged first', 'fixed second', 3]}),
                 ({'/rows/1': 'fixed second'}, {'rows': ['unchanged first', 'fixed second', 3]}),
                 ({'/rows/0': 'unrequested change', '/rows/1': 'fixed second'}, original)]
        for number, (corrections, expected) in enumerate(cases):
            error = subject.ExternalIntelligenceTimeout('field correction stream interrupted')
            error.partial_message = {'model': subject.MODEL, 'content': [{'type': 'text', 'text': json.dumps(corrections)}]}
            with self.subTest(corrections=corrections), mock.patch.object(self.client, '_post',
                    side_effect=[answer(json.dumps(original)), error]) as post:
                with self.assertRaises(subject.ExternalIntelligenceError) as caught:
                    self.client.invoke(prompt='test', schema=schema, model=subject.MODEL, effort='medium',
                        work_dir=self.root / 'field-interruption' / str(number), timeout_seconds=5)
            self.assertEqual(expected, caught.exception.partial_output)
            self.assertEqual(2, post.call_count)
            self.assertEqual({'rows': ['unchanged first', 2, 3]}, original)

    def test_cut_field_corrections_never_complete_truncated_numbers_or_strings(self):
        original = {'rows': ['unchanged', None, None]}
        fields = [(['rows', 1], {'type': 'string'}), (['rows', 2], {'type': 'integer'})]
        schema = {'type': 'object', 'additionalProperties': False, 'required': ['/rows/1', '/rows/2'],
                  'properties': {subject._field_pointer(path): rule for path, rule in fields}}
        cases = [('{"/rows/1":"fixed",', {'rows': ['unchanged', 'fixed', None]}),
                 ('{"/rows/1":"fixed","/rows/2":12', {'rows': ['unchanged', 'fixed', None]}),
                 ('{"/rows/1":"fixed","/rows/2":12e', {'rows': ['unchanged', 'fixed', None]}),
                 ('{"/rows/1":"unfinished', None),
                 ('{"/rows/1":"fixed","/rows/2":12}', {'rows': ['unchanged', 'fixed', 12]}),
                 ('{"/rows/1":"fixed","/rows/0":"unrequested"', None),
                 ('{"/rows/1":"fixed","/rows/1":"different"', None),
                 ('{"/rows/1":"fixed","/rows/1":"different","/rows/2":12}', None),
                 ('{"/rows/1":"fixed"} trailing', None)]
        for text, expected in cases:
            with self.subTest(text=text):
                self.assertEqual(expected, subject._retained_field_corrections(text, original, fields, schema))
        self.assertEqual({'rows': ['unchanged', None, None]}, original)

    def test_complete_interrupted_batch_is_retained_without_hiding_transport_failure(self):
        schema = {'type': 'object', 'additionalProperties': False, 'required': ['rows'],
                  'properties': {'rows': {'type': 'array', 'minItems': 2, 'maxItems': 2,
                                         'items': {'type': 'string'}}}}
        full = {'rows': ['first unchanged', 'second unchanged']}
        for model, unexpected_tool in ((model, tool) for model in (subject.MODEL, 'wrong-model') for tool in (False, True)):
            error = subject.ExternalIntelligenceError('stream ended without message_stop')
            error.partial_message = {'model': model, 'content': [{'type': 'text', 'text': json.dumps(full)}]}
            if unexpected_tool:
                error.partial_message['content'].append({'type': 'tool_use', 'id': 'unexecuted', 'name': 'read', 'input': {}})
            scope = self.root / 'complete-interruption' / model / str(unexpected_tool)
            with self.subTest(model=model, unexpected_tool=unexpected_tool), mock.patch.object(self.client, '_post', side_effect=error) as post:
                with self.assertRaises(subject.ExternalIntelligenceError) as caught:
                    self.client.invoke(prompt='test', schema=schema, model=subject.MODEL,
                        effort='medium', work_dir=scope, timeout_seconds=5)
            self.assertEqual(1, post.call_count)
            if model == subject.MODEL and not unexpected_tool:
                self.assertEqual(full, caught.exception.partial_output)
                self.assertEqual(1, caught.exception.partial_usage['api_requests'])
                audit = json.loads((scope / 'interrupted-source-prefix.json').read_text())
                self.assertFalse(audit['output_usage_complete'])
            else:
                self.assertFalse(hasattr(caught.exception, 'partial_output'))

    def test_failed_correction_retains_valid_records_but_never_completes_missing_one(self):
        schema = {'type': 'object', 'required': ['rows'], 'additionalProperties': False,
                  'properties': {'rows': {'type': 'array', 'minItems': 2, 'maxItems': 2,
                                         'items': {'type': 'string'}}}}
        with mock.patch.object(self.client, '_post', side_effect=[answer('{"rows":["keep",'), answer('{"/rows/1":5}')]):
            with self.assertRaises(subject.ExternalIntelligenceError) as caught:
                self.client.invoke(prompt='test', schema=schema, model=subject.MODEL,
                    effort='medium', work_dir=self.root / 'partial-failure', timeout_seconds=5)
        self.assertEqual({'rows': ['keep', None]}, caught.exception.partial_output)
        with self.assertRaises(subject.ExternalIntelligenceError):
            subject._validate_schema(caught.exception.partial_output, schema)

    def test_correction_timeout_retains_closed_records_without_accepting_the_batch(self):
        schema = {'type': 'object', 'required': ['rows'], 'additionalProperties': False,
                  'properties': {'rows': {'type': 'array', 'minItems': 2, 'maxItems': 2,
                                         'items': {'type': 'string'}}}}
        with mock.patch.object(self.client, '_post', side_effect=[answer('{"rows":["keep",'),
                subject.ExternalIntelligenceTimeout('correction timed out')]):
            with self.assertRaises(subject.ExternalIntelligenceTimeout) as caught:
                self.client.invoke(prompt='test', schema=schema, model=subject.MODEL,
                    effort='medium', work_dir=self.root / 'partial-timeout', timeout_seconds=5)
        self.assertEqual({'rows': ['keep', None]}, caught.exception.partial_output)
        with self.assertRaises(subject.ExternalIntelligenceError):
            subject._validate_schema(caught.exception.partial_output, schema)

    def test_single_integer_collection_is_wrapped_without_changing_members_or_bounds(self):
        selector = {'anyOf': [{'type': 'integer', 'minimum': 0},
                    {'type': 'array', 'minItems': 2, 'maxItems': 2, 'items': {'type': 'integer'}}]}
        schema = {'type': 'object', 'required': ['context'], 'additionalProperties': False,
                  'properties': {'context': {'type': 'array', 'items': selector, 'maxItems': 5}}}
        with mock.patch.object(self.client, '_post', return_value=answer('{"context":3}')) as post:
            value, _, _ = self.client.invoke(prompt='test', schema=schema, model=subject.MODEL,
                effort='medium', work_dir=self.root / 'singleton', timeout_seconds=5)
        self.assertEqual({'context': [3]}, value)
        self.assertEqual(1, post.call_count)
        original = {'context': [[3, 7], 9]}
        self.assertEqual(original, subject._normalize_integer_collections(original, schema))
        for bad in (-1, None, True, '3'):
            self.assertEqual({'context': bad}, subject._normalize_integer_collections({'context': bad}, schema))
        pair = {'type': 'array', 'minItems': 2, 'maxItems': 2, 'items': {'type': 'integer'}}
        self.assertEqual(3, subject._normalize_integer_collections(3, pair))
        self.assertEqual(3, subject._normalize_integer_collections([3], selector))
        self.assertEqual([3, 7], subject._normalize_integer_collections([3, 7], selector))
        self.assertEqual({'context': [3]}, subject._normalize_integer_collections({'context': [3]}, schema))
        self.assertEqual([-1], subject._normalize_integer_collections([-1], selector))

    def test_wrapped_exact_corrections_are_accepted_without_changing_other_fields(self):
        schema = {'type': 'object', 'required': ['kind', 'fact'], 'additionalProperties': False,
                  'properties': {'kind': {'type': 'string', 'maxLength': 8}, 'fact': {'type': 'string'}}}
        original = {'kind': 'a label that is too long', 'fact': 'unchanged original information'}
        with mock.patch.object(self.client, '_post', side_effect=[answer(json.dumps(original)),
                answer('{"analyses":[{"/kind":"event"}]}')]) as post:
            value, _, _ = self.client.invoke(prompt='test', schema=schema, model=subject.MODEL,
                effort='medium', work_dir=self.root / 'wrapped-corrections', timeout_seconds=5)
        self.assertEqual({'kind': 'event', 'fact': original['fact']}, value)
        self.assertEqual(2, post.call_count)
        repair = {'type': 'object', 'required': ['/kind'], 'additionalProperties': False,
                  'properties': {'/kind': {'type': 'string'}}}
        ambiguous = {'one': {'/kind': 'event'}, 'two': {'/kind': 'fact'}}
        self.assertIsNone(subject._unwrap_field_corrections(ambiguous, repair))

    def test_surplus_closing_delimiter_is_recovered_but_extra_content_is_not(self):
        with mock.patch.object(self.client, '_post', return_value=answer('{"answer":"kept"}}')) as post:
            value, _, _ = self.invoke()
        self.assertEqual({'answer': 'kept'}, value)
        self.assertEqual(1, post.call_count)
        self.assertIsNone(subject._closed_json_prefix('{"answer":"one"}{"answer":"two"}'))
        self.assertIsNone(subject._closed_json_prefix('{"answer":"one"} commentary'))

    def test_initial_context_is_a_separate_block_without_schema_or_rule_changes(self):
        with mock.patch.object(self.client, '_post', return_value=answer('{"answer":"ok"}')) as post:
            self.invoke(initial_context='Original source', base_instructions='product rules')
        body = post.call_args.args[0]
        self.assertEqual('test', body['messages'][0]['content'][0]['text'])
        self.assertEqual('Original source', body['messages'][0]['content'][1]['text'])
        self.assertNotIn('Original source', body['system'][0]['text'])

    def test_typed_tool_arrays_are_decoded_without_rewriting_strings(self):
        tools = [{'name':'read', 'description':'read', 'inputSchema': {'type':'object', 'properties': {
            'ids': {'type':'array', 'items':{'type':'string'}}, 'query': {'type':'string'}}}}]
        reply = answer('', content=[{'type':'tool_use','id':'t','name':'read',
                                     'input':{'ids':'["a","b"]','query':'["literal"]'}}])
        handler = mock.Mock(return_value={})
        with mock.patch.object(self.client, '_post', side_effect=[reply, answer('{"answer":"ok"}')]):
            self.invoke(dynamic_tools=tools, tool_handler=handler)
        handler.assert_called_once_with('read', {'ids':['a','b'], 'query':'["literal"]'})

    def test_completed_text_with_unknown_empty_tool_is_repaired_without_retrieval(self):
        reply = answer('', content=[{'type':'text','text':'completed work'},
                                    {'type':'tool_use','id':'t','name':'unknown','input':{}}])
        handler = mock.Mock()
        with mock.patch.object(self.client, '_post', side_effect=[reply, answer('{"answer":"done"}')]) as post:
            _, usage, _ = self.invoke(dynamic_tools=TOOLS, tool_handler=handler)
        handler.assert_not_called()
        self.assertEqual(1, usage['format_corrections'])
        self.assertNotIn('tools', post.call_args_list[1].args[0])
        self.assertIn('completed work', str(post.call_args_list[1].args[0]['messages']))

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

    def test_single_range_wrapper_is_normalized_without_changing_evidence(self):
        selector = {"anyOf": [{"type": "integer", "minimum": 0},
                    {"type": "array", "minItems": 2, "maxItems": 2,
                     "items": {"type": "integer", "minimum": 0}}]}
        schema = {"type": "object", "required": ["selector", "meaning"],
                  "additionalProperties": False,
                  "properties": {"selector": selector, "meaning": {"type": "string"}}}
        value = {"selector": [[7, 9]], "meaning": "Keep this condition unchanged."}
        with mock.patch.object(self.client, "_post", return_value=answer(json.dumps(value))) as post:
            actual, usage, _ = self.client.invoke(prompt="test", schema=schema, model=subject.MODEL,
                effort="medium", work_dir=self.root / "range", timeout_seconds=5)
        self.assertEqual({**value, "selector": [7, 9]}, actual)
        self.assertEqual(0, usage["format_corrections"])
        post.assert_called_once()
        for invalid in ([[7, 9], [11, 12]], [[7, True]], [[-1, 9]], [[7, 9, 11]]):
            original = {**value, "selector": invalid}
            self.assertEqual(original, subject._normalize_integer_collections(original, schema))

    def test_object_alternative_normalization_requires_one_valid_meaning(self):
        def branch(key, schema):
            return {"type": "object", "required": ["a", "b"],
                    "properties": {key: schema}}
        ambiguous = {"anyOf": [branch("a", {"type": "array", "items": {"type": "integer"}}),
                               branch("b", {"type": "array", "items": {"type": "integer"}})]}
        original = {"a": 7, "b": 9}
        self.assertEqual(original, subject._normalize_integer_collections(original, ambiguous))
        fixed = {"anyOf": [{"type": "object", "required": ["asset_id", "selector"],
                  "additionalProperties": False, "properties": {
                    "asset_id": {"enum": ["self"]}, "selector": {"anyOf": [
                      {"type": "integer"}, {"type": "array", "minItems": 2, "maxItems": 2,
                                            "items": {"type": "integer"}}]}}}]}
        self.assertEqual({"asset_id": "self", "selector": [7, 9]},
                         subject._normalize_integer_collections({"asset_id": "self", "selector": [[7, 9]]}, fixed))
        original = {"asset_id": "other", "selector": [[7, 9]]}
        self.assertEqual(original, subject._normalize_integer_collections(original, fixed))

    def test_interrupted_source_prefix_remains_partial_and_checks_model(self):
        schema = {"type": "object", "required": ["analyses"], "properties": {
            "analyses": {"type": "array", "minItems": 2, "maxItems": 2,
                         "items": {"type": "object", "required": ["index", "text"],
                                   "properties": {"index": {"type": "integer"}, "text": {"type": "string"}}}}}}
        prefix = '{"analyses":[{"index":0,"text":"Keep this complete source."},'
        for model in (subject.MODEL, "wrong-model"):
            failure = subject.ExternalIntelligenceTimeout("interrupted")
            failure.partial_message = {"model": model, "content": [{"type": "text", "text": prefix}],
                                       "usage": {"input_tokens": 10}}
            with mock.patch.object(self.client, "_post", side_effect=failure) as post:
                with self.assertRaises(subject.ExternalIntelligenceTimeout) as raised:
                    self.client.invoke(prompt="test", schema=schema, model=subject.MODEL, effort="medium",
                                       work_dir=self.root / model, timeout_seconds=5)
            post.assert_called_once()
            if model == subject.MODEL:
                self.assertEqual({"index": 0, "text": "Keep this complete source."},
                                 raised.exception.partial_output["analyses"][0])
                audit = json.loads((self.root / model / "interrupted-source-prefix.json").read_text())
                self.assertFalse(audit["output_usage_complete"])
                self.assertIsNone(audit["unreported_output_tokens"])
            else:
                self.assertFalse(hasattr(raised.exception, "partial_output"))

    def test_transport_interruptions_preserve_only_delivered_source_prefixes(self):
        schema = {"type": "object", "required": ["analyses"], "properties": {
            "analyses": {"type": "array", "minItems": 2, "maxItems": 2,
                         "items": {"type": "object", "required": ["index", "text"],
                                   "properties": {"index": {"type": "integer"}, "text": {"type": "string"},
                                                  "selector": {"anyOf": [{"type": "integer"},
                                                      {"type": "array", "minItems": 2, "maxItems": 2,
                                                       "items": {"type": "integer"}}]}}}}}}
        good = {"index": 0, "text": "Keep this complete source.", "selector": [3, 5]}
        prefix = '{"analyses":[' + json.dumps({**good, "selector": [[3, 5]]}) + ',{"index":1,"text":"unfinished'
        modes = ("timeout", "eof", "reset", "incomplete_read", "cut_event", "cut_utf8",
                 "malformed_event", "service_error", "service_error_no_newline")
        for mode in modes:
            for model in (subject.MODEL, "wrong-model"):
                with self.subTest(interruption=mode, model=model):
                    events = [
                        {"type": "message_start", "message": {"model": model, "usage": {"input_tokens": 10}}},
                        {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}},
                        {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": prefix}},
                    ]
                    if mode.startswith("service_error"):
                        events.append({"type": "error", "error": {"type": "api_error", "message": "rejected"}})
                    wire = b"".join(b"data: " + json.dumps(event).encode() + b"\n\n" for event in events)
                    if mode == "malformed_event":
                        wire += b"data: {not JSON}\n\n"
                    elif mode == "cut_event":
                        wire += b'data: {"type":"message_delta","usage":{"output_tokens":'
                    elif mode == "cut_utf8":
                        wire += b'data: {"text":"' + '中'.encode()[:2]
                    elif mode == "service_error_no_newline":
                        wire = wire.rstrip(b'\n')

                    class Response(io.BytesIO):
                        status = 200
                        def readline(self, *args):
                            line = super().readline(*args)
                            if not line:
                                if mode == "timeout":
                                    raise TimeoutError("interrupted")
                                if mode == "reset":
                                    raise ConnectionResetError("interrupted")
                                if mode == "incomplete_read":
                                    raise subject.http.client.IncompleteRead(b"unparsed bytes")
                            return line

                    connection = mock.Mock(sock=None)
                    connection.getresponse.return_value = Response(wire)
                    with mock.patch.object(subject.http.client, "HTTPSConnection", return_value=connection):
                        with self.assertRaises(subject.ExternalIntelligenceError) as caught:
                            self.client.invoke(prompt="test", schema=schema, model=subject.MODEL, effort="medium",
                                work_dir=self.root / mode / model, timeout_seconds=5)
                    connection.request.assert_called_once()
                    connection.close.assert_called_once()
                    self.assertEqual(0, self.client._active)
                    partial = getattr(caught.exception, "partial_output", None)
                    if mode in ("timeout", "eof", "reset", "incomplete_read", "cut_event", "cut_utf8") and model == subject.MODEL:
                        self.assertEqual({"analyses": [good, None]}, partial)
                        with self.assertRaises(subject.ExternalIntelligenceError):
                            subject._validate_schema(partial, schema)
                        audit = json.loads((self.root / mode / model / "interrupted-source-prefix.json").read_text())
                        self.assertFalse(audit["output_usage_complete"])
                        self.assertIsNone(audit["unreported_output_tokens"])
                    else:
                        self.assertIsNone(partial)

    def test_every_wire_cut_preserves_only_fully_decoded_text_events(self):
        rows = [{'index': i, 'text': '中文 ``` and "quoted"'} for i in range(2)]
        schema = {'type': 'object', 'required': ['analyses'], 'properties': {
            'analyses': {'type': 'array', 'minItems': 2, 'maxItems': 2, 'items': {'type': 'object'}}}}
        prefix = '{"analyses":[' + json.dumps(rows[0], ensure_ascii=False) + ','
        events = [
            {'type': 'message_start', 'message': {'model': subject.MODEL}},
            {'type': 'content_block_start', 'index': 0, 'content_block': {'type': 'text', 'text': prefix}},
        ]
        wire = b''.join(b'data: ' + json.dumps(e, ensure_ascii=False).encode() + b'\n\n' for e in events)
        tail_event = {'type': 'content_block_delta', 'index': 0,
                      'delta': {'type': 'text_delta', 'text': json.dumps(rows[1], ensure_ascii=False) + ']}'}}
        tail = b'data: ' + json.dumps(tail_event, ensure_ascii=False).encode() + b'\n\n'
        for cut in range(len(tail) + 1):
            response = io.BytesIO(wire + tail[:cut])
            response.status = 200
            connection = mock.Mock(sock=None)
            connection.getresponse.return_value = response
            with self.subTest(cut=cut), mock.patch.object(subject.http.client, 'HTTPSConnection', return_value=connection):
                with self.assertRaises(subject.ExternalIntelligenceError) as caught:
                    self.client._post({}, 'fixture', time.monotonic() + 5, self.root / 'wire-cut.json')
            received = ''.join(block.get('text', '') for block in caught.exception.partial_message['content'])
            expected = rows if cut >= len(tail) - 2 else [rows[0], None]
            self.assertEqual({'analyses': expected}, subject._partial_array_output(received, schema, allow_complete=True))
            connection.request.assert_called_once()
            connection.close.assert_called_once()

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
            request = {"messages": [{"role": "user", "content": "保留 空格\n原文"}], "stream": True}
            value = self.client._post(request, "session", time.monotonic()+5, self.root / "response.json")
            self.assertEqual("token-plan.cn-beijing.maas.aliyuncs.com", https.call_args.args[0])
            method, endpoint, wire, headers = connection.request.call_args.args
            self.assertEqual(("POST", "/apps/anthropic/v1/messages"), (method, endpoint))
            self.assertEqual(request, json.loads(wire))
            self.assertEqual(json.dumps(request, ensure_ascii=False, separators=(",", ":")).encode("utf-8"), wire)
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
