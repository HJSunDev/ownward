"""Prepared inputs remain reusable only while their preparation contract matches."""
import copy
import inspect
import tempfile
import threading
import unittest
from pathlib import Path
from unittest import mock

import prepare_materials as subject


class PreparationDispatchTests(unittest.TestCase):
    def test_source_failure_releases_slot_without_waiting_for_other_tasks(self):
        pending = [{"ordinal": n} for n in range(1, 6)]
        next_started = threading.Event()
        calls = []
        active = 0
        maximum = 0
        lock = threading.Lock()

        def one(item):
            nonlocal active, maximum
            number = item["ordinal"]
            with lock:
                calls.append(number)
                active += 1
                maximum = max(active, maximum)
            try:
                if number == 2:
                    self.assertTrue(next_started.wait(2), 'isolated failure drained the pool')
                if number == 3:
                    next_started.set()
                return {**item, "prepared": number != 1,
                        **({"failure_scope": "question"} if number == 1 else {})}
            finally:
                with lock:
                    active -= 1

        rows = [{"ordinal": 0, "prepared": True, "reused": True}]
        progress = []
        reason = subject.prepare_pending(pending, one, rows, progress.append, workers=2)
        self.assertIsNone(reason)
        self.assertEqual(sorted(calls), list(range(1, 6)))
        self.assertLessEqual(maximum, 2)
        self.assertEqual(progress[-1]['prepared'], 5)
        self.assertEqual(progress[-1]['failed'], 1)
        self.assertEqual(progress[-1]['active'], [])
        self.assertFalse(next(r for r in rows if r['ordinal'] == 1)['prepared'])
        self.assertTrue(rows[0]['reused'])

    def test_shared_or_unknown_failure_stops_new_dispatch(self):
        for scope in (None, 'run'):
            calls = []
            def one(item):
                calls.append(item['ordinal'])
                return {**item, 'prepared': False, 'failure_scope': scope}
            reason = subject.prepare_pending([{'ordinal': 1}, {'ordinal': 2}], one, [],
                                             lambda value: None, workers=1)
            self.assertEqual(reason, 'shared_or_unclassified_failure')
            self.assertEqual(calls, [1])

    def test_multiple_isolated_failures_do_not_block_other_questions(self):
        calls = []; rows = []
        def one(item):
            calls.append(item['ordinal'])
            return {**item, 'prepared': item['ordinal'] >= 3, 'failure_scope': 'question'}
        reason = subject.prepare_pending([{'ordinal': n} for n in range(6)], one, rows,
                                         lambda value: None, workers=1)
        self.assertIsNone(reason)
        self.assertEqual(calls, list(range(6)))
        self.assertEqual(len(rows), 6)
        self.assertEqual(sum(row['prepared'] for row in rows), 3)

    def test_only_known_output_failures_are_isolated(self):
        for message in ('semantic submission batch contains failures: source error',
                        'semantic source repair remains incomplete: source error',
                        'organization location repair remains incomplete: source error'):
            self.assertEqual(subject.failure_scope(subject.product.AdapterError(message)), 'question')
        for error in (OSError('disk full'), AssertionError('identity changed'),
                      subject.product.AdapterError('prepared material changed'),
                      subject.product.ExternalIntelligenceError('HTTP 401 Unauthorized'),
                      subject.product.ExternalIntelligenceError('HTTP 429 Too Many Requests'),
                      subject.product.ExternalIntelligenceError('request timed out')):
            self.assertEqual(subject.failure_scope(error), 'run')

    def test_empty_pending_does_not_invoke_a_worker(self):
        worker = mock.Mock()
        self.assertIsNone(subject.prepare_pending([], worker, [], lambda value: None))
        worker.assert_not_called()


class PreparationProtocolTests(unittest.TestCase):
    def test_changed_identity_cannot_silently_regenerate_prepared_materials(self):
        with tempfile.TemporaryDirectory() as directory:
            states = Path(directory)
            old = {"binary_sha256": "old", "transport_driver": "old", "memory": {"model": "same"}}
            old_path = states / subject.product.canonical_sha256(old) / "dependencies.json"
            subject.freeze(old_path, old)
            original = old_path.read_bytes()
            for key in ("binary_sha256", "transport_driver", "memory"):
                changed = {**old, key: "new"}
                with self.assertRaisesRegex(subject.product.AdapterError, 'review affected preparation layers'):
                    subject.check_preparation_dependencies(states, changed)
                subject.check_preparation_dependencies(states, changed, rebuild_changed=True)
                self.assertEqual(old_path.read_bytes(), original)
            self.assertEqual(len(list(states.iterdir())), 1)
            subject.check_preparation_dependencies(states, old)

    def test_fresh_preparation_and_existing_matching_state_are_allowed(self):
        with tempfile.TemporaryDirectory() as directory:
            states = Path(directory)
            current = {"memory": "current"}
            subject.check_preparation_dependencies(states, current)
            subject.freeze(states / subject.product.canonical_sha256(current) / 'dependencies.json', current)
            subject.freeze(states / 'old' / 'dependencies.json', {"memory": "old"})
            subject.check_preparation_dependencies(states, current)

    def test_semantic_callees_invalidate_both_preparation_and_evaluation(self):
        product = subject.product
        capability = product.ExternalIntelligenceCapability
        protocol = product.load_json(Path(product.__file__).with_name("protocol.json"))
        arguments = dict(protocol=protocol, candidate="same", binary_sha256="same", environment_sha256="same",
                         input_manifest_sha256="same", dataset_sha256="same", formal=True, evaluator_sha256="same")
        before = product.semantic_implementation_identity()
        stages = product.stage_dependency_identities(**arguments)
        getsource = inspect.getsource
        callees = (capability.__init__, capability.semantic_contract.fget,
                   capability.semantic_prompt, capability.semantic_output_reservation,
                   capability.semantic_output_upper_bound, capability.semantic_instruction_text,
                   capability.encoded_semantic_input, capability.validate_encoded_semantic_input,
                   capability.semantic_fact_identity, capability._invoke, product._semantic_analysis_units,
                   product.validate_structured_output)
        for changed in callees:
            with self.subTest(callee=changed.__qualname__), mock.patch.object(inspect, "getsource",
                    side_effect=lambda function: getsource(function) + ("\n# implementation changed\n" if function is changed else "")):
                self.assertNotEqual(before, product.semantic_implementation_identity())
                updated = product.stage_dependency_identities(**arguments)
                self.assertTrue(all(updated[name] != stages[name] for name in stages))

    def test_answer_only_changes_do_not_invalidate_semantic_implementation(self):
        product = subject.product
        before = product.semantic_implementation_identity()
        getsource = inspect.getsource
        for changed in (product.ExternalIntelligenceCapability.active_answer,
                        product.ExternalIntelligenceCapability.judge):
            with self.subTest(callee=changed.__qualname__), mock.patch.object(inspect, "getsource",
                    side_effect=lambda function: getsource(function) + ("\n# answer behavior changed\n" if function is changed else "")):
                self.assertEqual(before, product.semantic_implementation_identity())

    def test_downstream_changes_reuse_legacy_protocol_without_rewriting_it(self):
        protocol = {"memory": {"semantic_model": "same"},
                    "reader": {"model": "reader"}, "judge": {"model": "judge"}}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "protocol.json"
            subject.freeze(path, protocol)
            original = path.read_bytes()
            changed = copy.deepcopy(protocol)
            changed["reader"]["model"] = "new reader"
            changed["judge"]["model"] = "new judge"
            subject.freeze_preparation_protocol(path, changed)
            self.assertEqual(original, path.read_bytes())
            changed["memory"]["semantic_model"] = "new semantic model"
            with self.assertRaises(subject.product.AdapterError):
                subject.freeze_preparation_protocol(path, changed)
            self.assertEqual(original, path.read_bytes())

    def test_new_preparation_protocol_freezes_only_memory(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "protocol.json"
            protocol = {"memory": {"semantic_model": "same"}, "reader": {"model": "first"}}
            subject.freeze_preparation_protocol(path, protocol)
            self.assertEqual({"memory": protocol["memory"]}, subject.product.load_json(path))
            protocol["reader"]["model"] = "second"
            subject.freeze_preparation_protocol(path, protocol)


if __name__ == "__main__":
    unittest.main()
