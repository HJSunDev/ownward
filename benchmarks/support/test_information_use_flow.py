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
            'intended_outcome': 'Make a choice',
            'basis': {'established': 'Original report', 'unresolved': 'Meaning of the condition'},
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
        self.assertEqual(before['basis'], result['basis'])

    def test_empty_delivery_is_rejected_without_retrying_for_quality(self):
        self.response['answer'] = ' '
        invoke = mock.Mock(side_effect=[self.frame, self.response])
        with self.assertRaises(ValueError):
            flow.respond(self.observations, invoke)
        self.assertEqual(2, invoke.call_count)

    def test_explicit_entries_keep_prior_work_separate_from_sources(self):
        invoke = mock.Mock(side_effect=[self.frame, self.response])
        flow.complete(self.observations, invoke, draft='Fallible draft')
        payload = invoke.call_args.args[2]
        self.assertEqual(self.observations['sources'], payload['sources'])
        self.assertEqual('Fallible draft', payload['prior_work']['draft'])
        self.assertIn('fallible', invoke.call_args.args[1])
        self.assertNotIn('prior_work', self.observations)
        self.assertEqual(2, invoke.call_count)

    def test_task_contracts_do_not_leak_needs_between_tasks(self):
        before = copy.deepcopy(flow.RESPONSE)
        _, first = flow.task_contract(self.frame)
        _, second = flow.task_contract({'purpose': 'Other task', 'needs': ['Need A', 'Need B']})
        self.assertEqual(['1'], first['properties']['basis']['required'])
        self.assertEqual(['1', '2'], second['properties']['basis']['required'])
        self.assertEqual('Need B', second['properties']['basis']['properties']['2']['description'])
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


if __name__ == '__main__':
    unittest.main()
