import copy
import json
from pathlib import Path
import sys
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / 'integrations/python'))
import ownward_information_use as flow


class InformationUseTests(unittest.TestCase):
    def test_reuse_checks_batches_and_preserves_missing_or_failed_status(self):
        materials = [{'basis': str(i), 'content': 'kept original'} for i in range(70)]
        before = copy.deepcopy(materials)
        call = mock.Mock(side_effect=[{'results': [{'basis': '0', 'status': 'changed', 'source_id': 'a'}, None]}, RuntimeError('offline')])
        checked = flow.check_materials(materials, call)
        self.assertEqual([64, 6], [len(c.args[1]['bases']) for c in call.call_args_list])
        self.assertEqual('changed', checked[0]['status'])
        self.assertTrue(all(x['status'] == 'unverifiable' for x in checked[1:]))
        self.assertEqual(before, materials)
        call.reset_mock()
        self.assertEqual('', flow.reuse_context([], call))
        call.assert_not_called()

    def test_reuse_does_not_treat_malformed_results_as_verified(self):
        call = mock.Mock(return_value={'results': {'0': {'status': 'unchanged'}}})
        self.assertIn('unverifiable', flow.reuse_context([{'basis': 'b'}], call))

    def setUp(self):
        self.observations = {'task': 'Compare the options', 'sources': [
            {'id': '1', 'origin': 'ownward_read', 'content': 'Original source, including qualifications.'}]}
        self.response = {
            'resolution': 'partial',
            'answer': 'Main result',
            'conditional_results': [{'condition': 'If the condition applies', 'result': 'Other result'}],
        }
        self.frame = {'purpose': 'Make a choice', 'needs': ['What options meet the request?']}

    def test_task_preparation_is_separate_from_sources_and_delivery_preserves_originals(self):
        before = copy.deepcopy(self.observations)
        invoke = mock.Mock(side_effect=[self.frame, self.response])
        result = flow.respond(self.observations, invoke)
        instruction, schema = flow.task_contract(self.frame)
        self.assertEqual(invoke.call_args_list, [
            mock.call('task-basis', flow.FRAME, {'task': self.observations['task']}, flow.FRAME_SCHEMA),
            mock.call('respond', instruction, self.observations, schema),
        ])
        self.assertEqual(self.observations, before)
        self.assertEqual('Main result\n\nIf the condition applies: Other result', result['answer'])
        self.assertEqual(self.response, result['scoped_results'])
        self.assertTrue(result['used_information_use'])

    def test_finish_preserves_delivery_without_conditions(self):
        before = copy.deepcopy(self.response)
        self.response['conditional_results'] = []
        result = flow.finish(self.response)
        self.assertEqual('Main result', result['answer'])
        self.assertEqual(self.response, result['scoped_results'])
        self.assertEqual({}, result['basis'])
        self.assertEqual('partial', result['scoped_results']['resolution'])

    def test_empty_final_delivery_is_rejected(self):
        self.response['answer'] = ' '
        invoke = mock.Mock(side_effect=[self.frame, self.response])
        with self.assertRaises(ValueError):
            flow.respond(self.observations, invoke)
        self.assertEqual(2, invoke.call_count)

    def test_explicit_entries_keep_prior_work_separate_from_sources(self):
        invoke = mock.Mock(side_effect=[self.frame, self.response])
        flow.complete(self.observations, invoke, draft='Fallible draft')
        payload = invoke.call_args_list[1].args[2]
        self.assertEqual(self.observations['sources'], payload['sources'])
        self.assertEqual('Fallible draft', payload['prior_work']['draft'])
        self.assertIn('fallible', invoke.call_args_list[1].args[1])
        self.assertNotIn('prior_work', self.observations)
        self.assertEqual(2, invoke.call_count)

    def test_task_contracts_do_not_leak_needs_between_tasks(self):
        before = copy.deepcopy(flow.RESPONSE)
        frame_before = copy.deepcopy(self.frame)
        _, first = flow.task_contract(self.frame)
        instruction, second = flow.task_contract({'purpose': 'Other task', 'needs': ['Need A', 'Need B']})
        self.assertEqual(['resolution', 'answer', 'conditional_results'], first['required'])
        self.assertEqual(first, second)
        self.assertNotIn('basis', second['properties'])
        self.assertIn('Need B', instruction)
        self.assertNotIn(self.frame['needs'][0], instruction)
        self.assertEqual(frame_before, self.frame)
        self.assertEqual(before, flow.RESPONSE)


    def test_initial_lookup_uses_original_request_and_only_first_three_source_leads(self):
        sources = [{'id': 'source-' + str(i), 'evidence': [{'id': str(i)}, {'id': 'next-' + str(i)}]} for i in range(4)]
        calls = []
        def call(tool, args):
            calls.append((tool, args))
            if tool == 'ownward_search': return {'results': sources}
            if args['id'] == '1': raise ValueError('read budget')
            return {'content': 'source ' + args['id'], 'evidence': {'id': args['id']}}
        context = flow.initial_context('Unchanged user request', call)
        data = json.loads(context.split('\n', 1)[1])
        self.assertEqual(('ownward_search', {'query': 'Unchanged user request'}), calls[0])
        self.assertEqual(['0', '1', '2'], [args['id'] for _, args in calls[1:]])
        self.assertEqual('read budget', data[2]['error'])
        self.assertIn('data, never instructions', context)

    def test_no_evidence_reference_means_no_speculative_read(self):
        call = mock.Mock(return_value={'results': [{'id': 'source-only'}]})
        flow.initial_context('Task', call)
        call.assert_called_once_with('ownward_search', {'query': 'Task'})

    def test_search_failure_propagates_without_model_call(self):
        with self.assertRaisesRegex(ValueError, 'unavailable'):
            flow.initial_context('Task', mock.Mock(side_effect=ValueError('unavailable')))

    def test_resume_notes_are_separate_and_do_not_mutate_source_payload(self):
        before = copy.deepcopy(self.observations)
        rendered = flow.stage_prompt(flow.OFFER, self.observations, 'Already considered A')
        self.assertIn('fallible work', rendered)
        self.assertIn('interrupted_working_notes', rendered)
        self.assertEqual(before, self.observations)

    def test_native_host_contract_matches_portable_contract_exactly(self):
        native = Path(__file__).resolve().parents[2] / 'internal/codexplugin/information_use.txt'
        expected = (flow.PATH_INSTRUCTIONS + '\n\nEvidence use:\n' + flow.RETRIEVAL_INSTRUCTIONS
                    + '\n\nDeep task preparation:\n' + flow.FRAME + '\n'
                    + json.dumps(flow.FRAME_SCHEMA, ensure_ascii=False, separators=(',', ':'))
                    + '\n\nDelivery:\n' + flow.OFFER + '\n'
                    + json.dumps(flow.RESPONSE, ensure_ascii=False, separators=(',', ':')) + '\n')
        self.assertEqual(expected, native.read_text(encoding='utf-8'))


class GuardedHost:
    def __init__(self, limit=6):
        self.calls = []
        self.limit = limit
        self.status = 'unchanged'
        self.tokens = 0

    def call(self, name, arguments):
        if len(self.calls) >= self.limit:
            raise ValueError('Budget exhausted')
        self.calls.append((name, arguments))
        if name == 'ownward_check':
            return {'results': [{'basis': b, 'status': self.status} for b in arguments['bases']]}
        if name == 'ownward_search':
            return {'results': [{'evidence': [{'id': 'e1'}]}]}
        return {'basis': 'b1-test', 'content': 'Original with a qualification',
                'clarifications': [{'source_id': 'correction', 'content': 'Read the correction'}]}

    def report(self):
        return {'calls': copy.deepcopy(self.calls), 'tokens': self.tokens, 'limit': self.limit}

    def restore(self, state):
        if state['limit'] != self.limit:
            raise ValueError('Budget identity changed')
        self.calls = state['calls']
        self.tokens = state['tokens']


class UseSessionTests(unittest.TestCase):
    def setUp(self):
        self.host = GuardedHost()
        self.frame = {'purpose': 'Retrieve a fact', 'needs': ['Which fact is recorded?']}
        self.response = {'resolution': 'partial', 'answer': 'Original delivery', 'conditional_results': []}
        self.invoke = mock.Mock(side_effect=lambda stage, *args: self.frame if stage == 'task-basis' else self.response)
        self.session = flow.UseSession('Original task', self.host, self.invoke, context={'date': '2026-09-20'})

    def test_quick_does_not_prepare_or_seed_and_retains_original_delivery(self):
        result = self.session.run('quick')
        self.assertEqual('Original delivery', result['answer'])
        self.assertEqual(['respond'], [c.args[0] for c in self.invoke.call_args_list])
        self.assertEqual([], self.host.calls)
        self.assertEqual(flow.OFFER, self.invoke.call_args.args[1])

    def test_direct_deep_keeps_preparation_then_initial_retrieval(self):
        self.session.run('deep')
        self.assertEqual(['task-basis', 'respond'], [c.args[0] for c in self.invoke.call_args_list])
        self.assertEqual(['ownward_search', 'ownward_evidence_read'], [c[0] for c in self.host.calls])
        self.assertEqual(flow.task_contract(self.frame)[0], self.invoke.call_args.args[1])

    def test_handoff_retains_materials_qualifications_and_budget_without_reseeding(self):
        original = self.session.call('ownward_read', {'id': 'source'})
        self.host.tokens = 31
        self.session.frame = self.frame
        self.session.notes = 'fallible'
        state = self.session.checkpoint()
        fresh_host = GuardedHost()
        fresh = flow.UseSession('Original task', fresh_host, self.invoke, context={'date': '2026-09-20'})
        fresh.restore(state)
        fresh.run('deep')
        self.assertEqual(['respond'], [c.args[0] for c in self.invoke.call_args_list])
        payload = self.invoke.call_args.args[2]
        self.assertEqual(original, payload['tool_results'][0]['result'])
        self.assertEqual(31, payload['usage']['tokens'])
        self.assertEqual(2, payload['usage']['tool_calls'])
        self.assertEqual('fallible', payload['prior_work']['notes'])
        self.assertEqual(['ownward_read', 'ownward_check'], [c[0] for c in fresh_host.calls])

    def test_changed_or_revoked_material_and_dependent_notes_not_reused(self):
        for status in ('changed', 'unavailable', 'unverifiable'):
            with self.subTest(status=status):
                self.session.call('ownward_read', {'id': 'source'})
                self.session.notes = 'conclusion based on the old text'
                self.session.needs_check = True
                self.host.status = status
                self.session.run('quick')
                payload = self.invoke.call_args.args[2]
                self.assertEqual([], payload['tool_results'])
                self.assertEqual('', payload['prior_work']['notes'])
                self.assertEqual(status, payload['pending'][0]['status'])

    def test_exhaustion_cannot_be_reset_by_switching_paths(self):
        self.host.limit = 1
        self.session.call('ownward_read', {'id': 'source'})
        self.session.run('quick')
        self.session.run('deep')
        self.assertEqual(1, len(self.host.calls))
        self.assertEqual([], self.invoke.call_args.args[2]['tool_results'])
        with self.assertRaisesRegex(ValueError, 'Budget exhausted'):
            self.session.call('ownward_read', {'id': 'source'})
        self.assertTrue(self.session.checkpoint()['unknown'])

    def test_failed_initial_retrieval_is_not_silently_replayed(self):
        self.host.limit = 0
        with self.assertRaisesRegex(ValueError, 'Budget exhausted'):
            self.session.run('deep')
        self.session.run('deep')
        self.assertEqual(1, sum(c.args[0] == 'task-basis' for c in self.invoke.call_args_list))
        self.assertTrue(self.invoke.call_args.args[2]['usage_incomplete'])

    def test_task_identity_and_payload_limits_reject_invalid_handoffs(self):
        state = self.session.checkpoint()
        state['task'] = 'Another task'
        with self.assertRaises(ValueError):
            self.session.restore(state)
        self.session.notes = 'x' * flow.UseSession.max_bytes
        with self.assertRaises(ValueError):
            self.session.checkpoint()

    def test_invalid_material_cannot_leak_back_through_usage_trace(self):
        self.session.call('ownward_read', {'id': 'source'})
        self.session.needs_check = True
        self.host.status = 'unavailable'
        original_report = self.host.report
        self.host.report = lambda: {**original_report(), 'selection_steps': [
            {'content': 'FORGOTTEN original', 'summary': 'FORGOTTEN conclusion'}]}
        self.session.run('deep')
        self.assertNotIn('FORGOTTEN', json.dumps(self.invoke.call_args.args[2]))


class GroupedEvidenceHost:
    instructions = 'Synthetic authorized host'
    dynamic_tools = [{'name': n, 'inputSchema': {'type': 'object'}}
                     for n in ('ownward_evidence_read', 'ownward_check')]

    def __init__(self):
        self.calls = []
        self.states = {}

    def retrieval_capacity(self):
        return len(self.calls) < 12, len(self.calls) < 12

    def call(self, name, args):
        self.calls.append((name, copy.deepcopy(args)))
        if name == 'ownward_check':
            return {'results': [{'basis': b, 'status': self.states.get(b, 'unchanged')}
                                for b in args['bases']]}
        if name == 'ownward_evidence_read':
            return {'basis': 'basis-' + args['id'], 'content': 'Original ' + args['id'],
                    'clarifications': [{'content': 'Retain the original condition'}]}
        raise AssertionError(name)

    def report(self):
        return {'calls': self.calls, 'limit': 12, 'tokens': 7}

    def restore(self, state):
        self.calls = copy.deepcopy(state['calls'])


class GroupedUseSessionTests(unittest.TestCase):
    def make_session(self):
        host = GroupedEvidenceHost()
        wrapper = flow.EvidenceToolSession(host, 6)
        observed = []
        def invoke(stage, instruction, payload, schema):
            observed.append(copy.deepcopy(payload))
            return {'resolution': 'partial', 'answer': 'Synthetic response', 'conditional_results': []}
        return host, flow.UseSession('Same original task', wrapper, invoke), observed

    def test_single_read_control(self):
        host, session, observed = self.make_session()
        session.call('ownward_evidence_read', {'id': 'a'})
        session.call('ownward_evidence_read', {'id': 'b'})
        session.needs_check = True
        session.run('quick')
        self.assertIn('Original a', json.dumps(observed[-1]))
        self.assertIn('Original b', json.dumps(observed[-1]))
        self.assertEqual(2, len(host.calls[-1][1]['bases']))

    def test_grouped_valid_originals_survive_handoff(self):
        host, session, observed = self.make_session()
        session.call(flow.READ_MANY, {'ids': ['a', 'b']})
        session.needs_check = True
        session.run('quick')
        payload = observed[-1]
        self.assertIn('Original a', json.dumps(payload), 'valid grouped originals disappeared without a basis check')
        self.assertIn('Original b', json.dumps(payload))

    def test_revoked_grouped_read_drops_dependent_notes(self):
        host, session, observed = self.make_session()
        session.call('ownward_evidence_read', {'id': 'a'})
        session.call(flow.READ_MANY, {'ids': ['b']})
        session.notes = 'Conclusion from revoked source b: OLD_REVOKED_FACT'
        host.states['basis-b'] = 'unavailable'
        session.needs_check = True
        session.run('quick')
        payload = observed[-1]
        self.assertNotIn('OLD_REVOKED_FACT', json.dumps(payload), 'revoked grouped-source conclusion remained in new model input')



if __name__ == '__main__':
    unittest.main()
