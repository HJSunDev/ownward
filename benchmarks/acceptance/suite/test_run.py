import argparse
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import run
import lifecycle
import binding as candidate_binding
from contract import load_contract


class UnifiedEntryTests(unittest.TestCase):
    def test_bind_dispatches_to_binding_module(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            arguments = argparse.Namespace(
                mode="bind", config=root / "execution.json", output=root / "binding",
                state=None, binding=None, impact=[], checkpoint_mode=None, repository=Path("."),
                binary=None, runtime_dir=None, codex_binary=None, codex_auth_file=None,
                isolation_dir=None, resume=False,
            )
            expected = {"candidate": "a" * 40}
            with (
                patch.object(run, "parse_args", return_value=arguments),
                patch.object(run.binding, "create", return_value=expected) as create,
                patch("sys.stdout", new_callable=io.StringIO),
            ):
                run.main()
        create.assert_called_once_with(run.HERE, arguments.config, arguments.output)

    def test_summarize_resume_reuses_sealed_report(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            report_path = root / "suite.json"
            report_path.write_text('{"sealed": true}\n', encoding="utf-8")
            contract = load_contract(run.HERE / "contract.json")
            binding = {
                "schema": "ownward.acceptance-binding/v2",
                "suite_version": "1.0.0", "candidate": "a" * 40,
                "binary_sha256": "b" * 64,
                "scopes": {
                    name: {
                        "environment_sha256": values[0] * 64,
                        "input_manifest_sha256": values[1] * 64,
                        "tool_sha256": values[2] * 64,
                    }
                    for name, values in {
                        "frontier": "cde", "core": "f01", "product": "234", "community": "567",
                    }.items()
                },
            }
            state = lifecycle.new_state(contract, binding)
            state["checkpoints"]["summarize"] = {
                "binding": candidate_binding.for_mode(binding, "summarize"), "report_path": str(report_path),
                "report_sha256": lifecycle.file_sha256(report_path), "passed": True,
            }
            state_path = root / "state.json"
            lifecycle.save_state(state_path, state)
            arguments = argparse.Namespace(
                mode="summarize", config=None, output=report_path, state=state_path,
                binding=None, impact=[], checkpoint_mode=None, repository=Path("."),
                binary=None, runtime_dir=None, codex_binary=None, codex_auth_file=None,
                isolation_dir=None, resume=True,
            )
            with (
                patch.object(run, "parse_args", return_value=arguments),
                patch.object(run.lifecycle, "reusable_report", return_value=report_path),
                patch("sys.stdout", new_callable=io.StringIO) as output,
            ):
                run.main()
        self.assertEqual({"sealed": True}, json.loads(output.getvalue()))


if __name__ == "__main__":
    unittest.main()
