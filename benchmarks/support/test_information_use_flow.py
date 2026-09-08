import sys
import json
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / 'integrations' / 'python'))
import ownward_information_use as flow


class InformationUseFlowTests(unittest.TestCase):
    def test_stage_recovery_preserves_original_input_and_marks_notes_as_fallible(self):
        payload = {'task': 'Use the records.', 'sources': ['original']}
        plain = flow.stage_prompt('instruction', payload)
        self.assertEqual('instruction\n\n' + json.dumps(payload, ensure_ascii=False), plain)
        resumed = flow.stage_prompt('instruction', payload, 'unfinished work')
        self.assertEqual({'task': 'Use the records.', 'sources': ['original']}, payload)
        self.assertEqual('instruction\n\n' + flow.RESUME + '\n\n' + json.dumps(
            {**payload, 'interrupted_working_notes': 'unfinished work'}, ensure_ascii=False), resumed)

    def setUp(self):
        self.observations = {'task': 'Plan the requested work.',
                             'sources': [{'id': 's1', 'origin': 'original', 'content': 'A record.'}]}

    def run_flow(self, selected, edits=None, existing=None, addition="", accepted=True, dependency_edits=None):
        calls = {}
        def invoke(stage, instruction, payload, schema):
            self.assertEqual(payload['task'], self.observations['task'])
            if stage == 'route-dependency-check':
                self.assertNotIn('sources', payload)
                self.assertIn('existing_reasoning', payload)
                calls[stage] = payload
                return {'basis': 'Working decision', 'check': dependency_edits is not None}
            self.assertEqual(payload['sources'], self.observations['sources'])
            calls[stage] = payload
            if stage == 'check-dependencies':
                return {'unsupported_bridge': '', 'result_without_bridge': '',
                        'edits': dependency_edits or []}
            if stage == "admit-addition":
                self.assertNotIn("completed_work", payload)
                self.assertNotIn("considered_alternatives", payload)
                return {"accepted": accepted, "basis": "Independent source check"}
            if stage == "supplement":
                return {"addition": addition}
            if stage == 'understand':
                return {'finding': 'A fallible reading.'}
            if stage == 'draft':
                return {'answer': 'Initial plan'}
            if stage == 'alternative':
                return {'answer': 'Alternative plan'}
            candidates = payload['candidates']
            choice = next(c['id'] for c in candidates if c['content'] == selected)
            return {'selected_id': choice, 'basis': 'External assessment', 'edits': edits or []}
        result = flow.complete(self.observations, invoke, draft=existing)
        return result, calls

    def test_external_choice_can_retain_or_replace_the_draft(self):
        for selected in ('Initial plan', 'Alternative plan'):
            with self.subTest(selected=selected):
                result, calls = self.run_flow(selected)
                self.assertEqual(result['answer'], selected)
                self.assertEqual(set(calls), {'understand', 'draft', 'alternative', 'compare', 'supplement', 'route-dependency-check'})
                self.assertEqual(calls['alternative']['existing_proposal'], 'Initial plan')
                self.assertEqual(calls['alternative']['fallible_reading'], 'A fallible reading.')
                self.assertEqual(set(calls['compare']), {'task', 'sources', 'candidates'})
                self.assertEqual([set(c) for c in calls['compare']['candidates']],
                                 [{'id', 'content'}, {'id', 'content'}])

    def test_existing_work_is_reconsidered_without_generating_a_new_draft(self):
        result, calls = self.run_flow('Alternative plan', existing='Prior work')
        self.assertNotIn('draft', calls)
        self.assertEqual(calls['alternative']['existing_proposal'], 'Prior work')
        self.assertEqual(result['draft'], 'Prior work')

    def test_only_external_exact_edits_change_the_selected_result(self):
        edits = [{'before': 'Alternative', 'after': 'Revised', 'basis': 'External assessment'}]
        result, calls = self.run_flow('Alternative plan', edits)
        self.assertEqual(result['answer'], 'Revised plan')
        self.assertEqual(len(calls), 6)

    def test_dependency_review_receives_accepted_addition_and_preserves_other_work(self):
        edits = [{'before': 'Unconfirmed outcome', 'after': 'Conditional outcome', 'basis': 'Missing link'}]
        result, calls = self.run_flow('Alternative plan', addition='Unconfirmed outcome', dependency_edits=edits)
        original = 'Alternative plan\n\nUnconfirmed outcome'
        self.assertEqual(calls['check-dependencies'], {**self.observations, 'completed_work': original})
        self.assertEqual(result['work_before_dependency_check'], original)
        self.assertEqual(result['answer'], 'Alternative plan\n\nConditional outcome')
        self.assertEqual(result['dependency_edits'], edits)
        self.assertEqual(list(calls)[-1], 'check-dependencies')

    def test_dependency_routing_cannot_silently_skip_or_reinterpret_a_choice(self):
        for route in ({}, {'check': 'false'}, {'check': 0}):
            with self.subTest(route=route), self.assertRaises(ValueError):
                flow._finish_dependencies(self.observations, {'answer': 'Work'}, lambda *args: route)

    def test_declined_dependency_check_preserves_result_without_reading_or_modifying_it(self):
        work = {'answer': 'A qualified result', 'decision': {'basis': 'Already considered'}}
        calls = []
        def invoke(stage, instruction, payload, schema):
            calls.append(stage)
            self.assertEqual(payload, {'task': self.observations['task'], 'completed_work': work['answer'],
                                     'existing_reasoning': {'decision': work['decision']}})
            return {'basis': 'Already qualified', 'check': False}
        result = flow._finish_dependencies(self.observations, work, invoke)
        self.assertEqual(result['answer'], 'A qualified result')
        self.assertEqual(calls, ['route-dependency-check'])
        self.assertNotIn('dependency_review', result)

    def test_dependency_anchor_repair_has_a_separate_checkpoint_from_comparison_repair(self):
        calls = []
        def invoke(stage, instruction, payload, schema):
            calls.append(stage)
            if stage == 'check-dependencies':
                return {'unsupported_bridge': 'Missing condition', 'result_without_bridge': 'Conditional',
                        'edits': [{'before': 'the result', 'after': 'conditional result', 'basis': 'Sources'}]}
            self.assertEqual(stage, 'repair-dependency-edit-anchors')
            return {'anchors': [{'first': 0, 'last': 0}]}
        result = flow.check_dependencies(self.observations, 'Result remains usable.', invoke)
        self.assertEqual(result['answer'], 'conditional result remains usable.')
        self.assertEqual(calls, ['check-dependencies', 'repair-dependency-edit-anchors'])
        self.assertEqual(result['dependency_edits'][0]['basis'], 'Sources')

    def test_dependency_review_failure_does_not_silently_release_unchecked_work(self):
        with self.assertRaisesRegex(RuntimeError, 'Unavailable'):
            flow.check_dependencies(self.observations, 'Prior work',
                                    lambda *args: (_ for _ in ()).throw(RuntimeError('Unavailable')))

    def test_supplement_preserves_the_completed_result_verbatim(self):
        result, calls = self.run_flow('Alternative plan', addition='A supported condition.')
        self.assertEqual(calls['supplement']['completed_work'], 'Alternative plan')
        self.assertEqual(result['base_answer'], 'Alternative plan')
        self.assertEqual(result['answer'], 'Alternative plan\n\nA supported condition.')
        self.assertEqual(result['addition'], 'A supported condition.')

    def test_rejected_addition_never_changes_completed_work(self):
        result, calls = self.run_flow('Alternative plan', addition='Unsupported result.', accepted=False)
        self.assertEqual(result['answer'], result['base_answer'])
        self.assertEqual(result['addition'], '')
        self.assertFalse(result['addition_review']['accepted'])
        self.assertEqual(calls['admit-addition']['proposed_result'], 'Unsupported result.')

    def test_empty_supplement_does_not_change_the_completed_result(self):
        result, _ = self.run_flow('Alternative plan')
        self.assertEqual(result['answer'], result['base_answer'])
        self.assertEqual(result['addition'], '')

    def test_ambiguous_overlapping_or_missing_edits_fail(self):
        for text, edits in [
            ('same same', [{'before': 'same', 'after': 'new'}]),
            ('aaa', [{'before': 'aa', 'after': 'new'}]),
            ('a', [{'before': '', 'after': 'new'}]),
            ('abc', [{'before': 'x', 'after': 'new'}]),
            ('abc', [{'before': 'ab', 'after': 'x'}, {'before': 'bc', 'after': 'y'}]),
        ]:
            with self.subTest(text=text, edits=edits), self.assertRaises(ValueError):
                flow.apply_edits(text, edits)

    def test_edits_refer_to_original_spans_even_when_offsets_change(self):
        self.assertEqual(flow.apply_edits('first + last', [
            {'before': 'last', 'after': 'ending'},
            {'before': 'first', 'after': 'a longer beginning'}]),
            'a longer beginning + ending')

    def test_valid_edits_need_no_location_repair(self):
        edits = [{'before': 'old', 'after': 'new', 'basis': 'Evidence'}]
        answer, applied = flow.apply_reviewed_edits(
            'An old result', edits, lambda *args: self.fail('Valid edits need no extra invocation'))
        self.assertEqual(answer, 'An new result')
        self.assertEqual(applied, edits)

    def test_location_repair_preserves_the_external_decision_and_original_record(self):
        edits = [{'before': 'pairing by order', 'after': 'chronological pairing', 'basis': 'Evidence'}]
        calls = []
        def invoke(stage, instruction, payload, schema):
            calls.append(stage)
            self.assertEqual(payload['work'], 'Use pairing them by order.')
            self.assertEqual(payload['requested_edits'], edits)
            self.assertEqual(payload['tokens'][1], {'id': 1, 'text': 'pairing'})
            return {'anchors': [{'first': 1, 'last': 4}]}
        answer, applied = flow.apply_reviewed_edits('Use pairing them by order.', edits, invoke)
        self.assertEqual(answer, 'Use chronological pairing.')
        self.assertEqual(applied, [{**edits[0], 'before': 'pairing them by order'}])
        self.assertEqual(edits[0]['before'], 'pairing by order')
        self.assertEqual(calls, ['repair-edit-anchors'])

    def test_invalid_location_repair_never_silently_drops_or_guesses_an_edit(self):
        for text, anchors in [('abc', [{'first': -1, 'last': -1}]),
                              ('same same', [{'first': 0, 'last': 0}]),
                              ('abc', [{'first': 0, 'last': 1}]), ('abc', []),
                              ('abc', [{'first': 0, 'last': 0}] * 2),
                              ('abc', [{'first': True, 'last': 0}])]:
            calls = []
            def invoke(*args):
                calls.append(args[0])
                return {'anchors': anchors}
            with self.subTest(text=text, anchors=anchors), self.assertRaises(ValueError):
                flow.apply_reviewed_edits(text, [{'before': 'wrong', 'after': 'new'}], invoke)
            self.assertEqual(calls, ['repair-edit-anchors'])

    def test_location_repair_cannot_create_overlapping_edits(self):
        with self.assertRaisesRegex(ValueError, 'overlap'):
            flow.apply_reviewed_edits('a b c', [{'before': 'x', 'after': '1'},
                                               {'before': 'y', 'after': '2'}],
                                     lambda *args: {'anchors': [{'first': 0, 'last': 1},
                                                               {'first': 1, 'last': 2}]})

    def test_invalid_choice_never_falls_back_to_a_silent_answer(self):
        def invoke(stage, instruction, payload, schema):
            if stage == 'alternative':
                return {'answer': 'Other'}
            return {'selected_id': '-1', 'basis': 'Invalid', 'edits': []}
        with self.assertRaises(ValueError):
            flow.reconsider(self.observations, 'Initial', 'Reading', invoke)

    def test_callback_failure_is_visible_and_does_not_trigger_semantic_retries(self):
        calls = []
        def invoke(stage, instruction, payload, schema):
            calls.append(stage)
            raise RuntimeError('Provider failed')
        with self.assertRaisesRegex(RuntimeError, 'Provider failed'):
            flow.complete(self.observations, invoke, draft='Existing')
        self.assertEqual(calls, ['understand'])

    def test_standard_entry_offers_capability_without_forcing_it(self):
        calls = []
        def invoke(stage, instruction, payload, schema):
            calls.append(stage)
            if stage == 'route':
                self.assertEqual(payload, {**self.observations, 'proposed_work': 'Direct result'})
                return {'basis': 'Directly stated.', 'mode': 'direct'}
            self.assertEqual(payload, self.observations)
            self.assertIn(flow.OFFER, instruction)
            self.assertEqual(schema, flow.RESPONSE)
            return {'answer': 'Direct result', 'use_information_use': False}
        result = flow.respond(self.observations, invoke)
        self.assertEqual(calls, ['respond', 'route'])
        self.assertEqual(result['answer'], 'Direct result')
        self.assertFalse(result['used_information_use'])

    def test_independent_routing_sees_originals_and_fallible_work_before_starting(self):
        calls = []
        def invoke(stage, instruction, payload, schema):
            calls.append(stage)
            if stage == 'route':
                self.assertEqual(payload, {**self.observations, 'proposed_work': 'Unchanged draft'})
                return {'basis': 'Interpretive choice.', 'mode': 'collaborate'}
            self.assertEqual(stage, 'understand')
            self.assertEqual(payload, self.observations)
            raise RuntimeError('Observed component entry')
        with self.assertRaisesRegex(RuntimeError, 'Observed component entry'):
            flow.finish(self.observations, {'answer': 'Unchanged draft', 'use_information_use': False}, invoke)
        self.assertEqual(calls, ['route', 'understand'])

    def test_invalid_routing_never_silently_bypasses_collaboration(self):
        for decision in ({}, {'mode': 'unknown'}):
            with self.subTest(decision=decision), self.assertRaises(ValueError):
                flow.finish(self.observations, {'answer': 'Work', 'use_information_use': False},
                            lambda *args: decision)

    def test_agent_can_choose_collaboration_and_reuse_its_work(self):
        calls = []
        def invoke(stage, instruction, payload, schema):
            calls.append(stage)
            if stage == 'respond':
                return {'answer': 'Original work', 'use_information_use': True}
            if stage == 'understand':
                return {'finding': 'Reading'}
            if stage == 'alternative':
                self.assertEqual(payload['existing_proposal'], 'Original work')
                return {'answer': 'Better work'}
            if stage == 'compare':
                selected = next(c['id'] for c in payload['candidates'] if c['content'] == 'Better work')
                return {'selected_id': selected, 'basis': 'Sources', 'edits': []}
            if stage == 'supplement':
                return {'addition': ''}
            if stage == 'check-dependencies':
                return {'unsupported_bridge': '', 'result_without_bridge': '', 'edits': []}
            if stage == 'route-dependency-check':
                return {'basis': 'No gap', 'check': False}
            self.fail('Unexpected stage: ' + stage)
        result = flow.respond(self.observations, invoke)
        self.assertEqual(result['answer'], 'Better work')
        self.assertTrue(result['used_information_use'])
        self.assertEqual(calls, ['respond', 'understand', 'alternative', 'compare', 'supplement', 'route-dependency-check'])

    def test_missing_or_nonboolean_choice_is_not_silently_treated_as_direct(self):
        for response in [{'answer': 'Work'}, {'answer': 'Work', 'use_information_use': 'false'}]:
            with self.assertRaises(ValueError):
                flow.finish(self.observations, response, lambda *args: self.fail('Must not call model'))


if __name__ == '__main__':
    unittest.main()
