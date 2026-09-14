"""Prepared inputs remain reusable only while their preparation contract matches."""
import copy
import inspect
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import prepare_materials as subject


class PreparationProtocolTests(unittest.TestCase):
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
