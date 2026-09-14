"""Exercise the public flow, executor and transport together without paid calls."""
import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import run as product
import go_api_external_intelligence as provider
from test_go_api_external_intelligence import answer


class Records:
    instructions = 'Use only original authorized evidence.'

    def __init__(self):
        self.calls = []
        self.denied = set()

    def list_tools(self):
        return [{'name': name, 'inputSchema': {'type': 'object', 'properties': {}}}
                for name in ['ownward_search', 'ownward_evidence_read']]

    def call_tool(self, name, args):
        self.calls.append((name, args))
        if name == 'ownward_search':
            return {'results': [{'id': 'source-' + str(i), 'evidence':
                    [{'id': 'evidence-' + str(i), 'source_id': 'source-' + str(i)}]}
                    for i in range(8)]}
        ref = args['id']
        if ref in self.denied:
            raise ValueError('Source is not accessible')
        i = ref.split('-')[-1]
        return {'evidence': {'source_id': 'source-' + i, 'id': ref,
                'content': '事实' + i, 'start_rune': 10, 'end_rune': 13}}


def settings(**overrides):
    return dict({'allowed_tools': ['ownward_search', 'ownward_evidence_read'],
                 'max_tool_calls': 12, 'read_limit': 8, 'context_max_chars': 24000,
                 'search_limit_per_call': 24, 'navigate_limit_per_call': 24,
                 'evidence_search_limit_per_source': 3}, **overrides)


class InformationUseAdoptionTests(unittest.TestCase):
    def prepare(self, **limits):
        self.records = Records()
        self.host = product.ActiveRetrievalSession(self.records, settings(**limits))
        self.session = product.information_use_flow.EvidenceToolSession(self.host, self.host.settings['read_limit'])
        found = self.session.call('ownward_search', {'query': 'facts'})
        self.refs = [r['evidence'][0]['id'] for r in found['results']]

    def test_batch_preserves_originals_and_each_native_budget_charge(self):
        self.prepare()
        result = self.session.call(product.information_use_flow.READ_MANY, {'ids': self.refs[:3]})
        self.assertEqual(['事实0', '事实1', '事实2'], [r['result']['evidence']['content'] for r in result['results']])
        self.assertEqual(self.refs[:3], [r['result']['evidence']['id'] for r in result['results']])
        self.assertEqual(9, self.host._read_chars)
        self.assertEqual(4, len(self.host._calls))
        self.session.validate()

    def test_partial_failure_preserves_siblings_and_blocks_unobserved_read(self):
        self.prepare()
        self.records.denied.add('evidence-1')
        result = self.session.call(product.information_use_flow.READ_MANY,
            {'ids': [self.refs[0], 'unknown', self.refs[1], self.refs[2]]})
        self.assertEqual([True, False, False, True], ['result' in r for r in result['results']])
        self.assertEqual(6, self.host._read_chars)
        self.assertNotIn(('ownward_evidence_read', {'id': 'unknown'}), self.records.calls)

    def test_batch_cannot_expand_call_read_or_character_budget(self):
        for limits, succeeds in [({'max_tool_calls': 3}, 2), ({'read_limit': 2}, 2),
                                 ({'context_max_chars': 5}, 1)]:
            with self.subTest(limits=limits):
                self.prepare(**limits)
                ids = self.refs[:min(3, self.host.settings['read_limit'])]
                result = self.session.call(product.information_use_flow.READ_MANY, {'ids': ids})
                self.assertEqual(succeeds, sum('result' in r for r in result['results']))
                self.assertEqual(3 * succeeds, self.host._read_chars)
                if 'max_tool_calls' in limits:
                    self.assertEqual([], self.session.dynamic_tools)
                if 'read_limit' in limits:
                    self.assertEqual(['ownward_search'], [t['name'] for t in self.session.dynamic_tools])

    def test_reset_preserves_handles_but_requires_new_observation(self):
        self.prepare()
        ref = self.refs[0]
        self.session.reset_attempt()
        result = self.session.call(product.information_use_flow.READ_MANY, {'ids': [ref]})
        self.assertIn('not observed', result['results'][0]['error'])
        current = self.session.call('ownward_search', {'query': 'facts'})
        self.assertEqual(ref, current['results'][0]['evidence'][0]['id'])

    def test_formal_entry_batches_and_resumes_without_replaying_initial_reads(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            auth = root / 'fake-auth.json'
            auth.write_text('{"BAILIAN_API_KEY":"test-key"}', encoding='utf-8')
            client = provider.GoAPIClient(auth, 8, {})
            client.identity = {
                'schema': 'ownward.external-intelligence-runtime-identity/v2',
                'contract': 'ownward.external-intelligence/v1', 'driver': 'test', 'provider': 'test',
                'transport': 'in-process-go-api', 'credential_content_read': False,
                'max_active': 8, 'worker_processes': 1,
                **{key: '0' * 64 for key in ['selection_sha256', 'artifact_sha256',
                    'implementation_sha256', 'credential_locator_sha256']}}
            records, bodies, sessions = Records(), [], []

            def post(body, session, *args, **kwargs):
                bodies.append(copy.deepcopy(body))
                sessions.append(session)
                n = len(bodies)
                if n == 1:
                    return answer('{"purpose":"resolve facts","needs":["facts"]}')
                if n == 2:
                    self.assertIn('Worked examples of using records', str(body['messages']))
                    self.assertIn(product.information_use_flow.READ_MANY, [t['name'] for t in body['tools']])
                    return answer('', content=[{'type': 'tool_use', 'id': 'batch',
                        'name': product.information_use_flow.READ_MANY,
                        'input': {'ids': ['ref8', 'ref10', 'ref12', 'ref14', 'ref16']}}], stop_reason='tool_use')
                if n == 3:
                    error = provider.ExternalIntelligenceError('TLS failed before POST')
                    error.request_not_sent = True
                    raise error
                if n == 4:
                    return answer('', content=[{'type': 'tool_use', 'id': 'search' + str(i),
                        'name': 'ownward_search', 'input': {'query': 'facts'}} for i in range(3)], stop_reason='tool_use')
                return answer('{"resolution":"partial","answer":"original result","conditional_results":[{"condition":"If applicable","result":"conditional result"}]}')

            capability = product.ExternalIntelligenceCapability(client)
            with mock.patch.object(client, '_post', side_effect=post):
                result = capability.active_answer({'question': 'facts'}, records,
                    {'model': provider.MODEL, 'reasoning_effort': 'xhigh',
                     'timeout_seconds': 5, 'attempts': 3}, settings(), root / 'task')
            self.assertEqual('original result\n\nIf applicable: conditional result', result[0])
            self.assertEqual(12, len(records.calls))
            self.assertEqual(8, sum(name == 'ownward_evidence_read' for name, _ in records.calls))
            self.assertEqual(12, len(result[2]['selection_steps']))
            self.assertEqual(bodies[2], bodies[3])
            self.assertEqual(sessions[2], sessions[3])
            self.assertEqual(['ownward_search'], [t['name'] for t in bodies[3]['tools']])
            self.assertEqual({'type': 'none'}, bodies[-1]['tool_choice'])
            self.assertEqual(1, len(list((root / 'task').glob('attempt-*/initial-context.json'))))
            self.assertEqual(2, len(list((root / 'task').glob('attempt-*'))))
            saved = json.loads((root / 'task/information-use-result.json').read_text(encoding='utf-8'))
            self.assertEqual('partial', saved['scoped_results']['resolution'])
            self.assertEqual({}, saved['basis'])


if __name__ == '__main__':
    unittest.main()
