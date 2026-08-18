from __future__ import annotations

import json
from pathlib import Path
import tempfile
import unittest

import run


class RunWrapperTests(unittest.TestCase):
    def test_records_candidate_and_adapter_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as root_value:
            root = Path(root_value)
            output = root / "output"
            output.mkdir()
            (output / "run_args.json").write_text("{}\n", encoding="utf-8")
            config = root / "memory.json"
            config.write_text(
                json.dumps(
                    {
                        "memory_type": "ownward",
                        "memory_params": {
                            "query_mode": "codex",
                            "codex_model": run.ACTIVE_CODEX_MODEL,
                            "codex_reasoning_effort": run.ACTIVE_CODEX_REASONING_EFFORT,
                        },
                    }
                ),
                encoding="utf-8",
            )
            adapter = root / "adapter"
            adapter.mkdir()
            for name in run.ADAPTER_FILES:
                (adapter / name).write_text(name, encoding="utf-8")
            run._record_ownward_evidence(
                ["--output-dir", str(output), "--memory-config-path", str(config)],
                candidate="abc",
                binary_sha256="def",
                adapter_root=adapter,
                codex_version=run.ACTIVE_CODEX_CLI_VERSION,
                codex_sha256="f" * 64,
            )
            evidence = json.loads((output / "run_args.json").read_text(encoding="utf-8"))["ownward_evidence"]
        self.assertEqual(evidence["candidate"], "abc")
        self.assertEqual(evidence["query_mode"], "codex")
        self.assertEqual(evidence["official_revision"], run.OFFICIAL_REVISION)
        self.assertEqual(evidence["codex_model"], run.ACTIVE_CODEX_MODEL)
        self.assertEqual(evidence["codex_cli_version"], run.ACTIVE_CODEX_CLI_VERSION)
        self.assertEqual(set(evidence["adapter_sha256"]), set(run.ADAPTER_FILES))
