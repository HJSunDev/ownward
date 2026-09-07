import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / 'integrations' / 'python'))
import ownward_information_use as flow


class InformationUseFlowTests(unittest.TestCase):
    def setUp(self):
        self.observations = {'task': 'Plan the requested work.',
                             'sources': [{'id': 's1', 'origin': 'original', 'content': 'A record.'}]}

    def run_flow(self, selected, edits=None, existing=None, addition="", accepted=True):
        calls = {}
        def invoke(stage, instruction, payload, schema):
            self.assertEqual(payload['task'], self.observations['task'])
            self.assertEqual(payload['sources'], self.observations['sources'])
            calls[stage] = payload
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
                self.assertEqual(set(calls), {'understand', 'draft', 'alternative', 'compare', 'supplement'})
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
        self.assertEqual(len(calls), 5)

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


if __name__ == '__main__':
    unittest.main()
