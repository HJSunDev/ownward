from __future__ import annotations

import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from types import SimpleNamespace
from unittest import mock

import preflight


class PreflightTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.suite_root = Path(__file__).resolve().parent
        cls.repository = cls.suite_root.parents[2]
        (cls.repository / ".tmp").mkdir(exist_ok=True)

    def test_isolated_preflight_verifies_dependencies_and_removes_probe(self) -> None:
        with tempfile.TemporaryDirectory(dir=self.repository / ".tmp") as directory:
            root = Path(directory)
            binary = root / "ownward.exe"
            codex = root / "codex.exe"
            auth = root / "auth.json"
            for path, content in ((binary, b"binary"), (codex, b"codex"), (auth, b"{}")):
                path.write_bytes(content)
            runtime = root / "runtime"
            runtime.mkdir()
            model = runtime / "model.gguf"
            library = runtime / "runtime.dll"
            model.write_bytes(b"model")
            library.write_bytes(b"runtime")
            manifest = {
                "capability": "embeddinggemma-q8",
                "model": {"path": model.name, "sha256": self._sha(model)},
                "runtime": {"files": {library.name: self._sha(library)}},
            }
            (runtime / "manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
            isolation = root / "new-isolation"
            revision = "2cc8c540bdb87fe6761629b585e727e1c4704520"
            runs = [
                SimpleNamespace(returncode=0, stdout="codex-cli 1.0\n", stderr=""),
                SimpleNamespace(returncode=0, stdout=f"{revision}\tHEAD\n", stderr=""),
            ]
            with (
                mock.patch.object(preflight.subprocess, "run", side_effect=runs),
                mock.patch.object(preflight.shutil, "which", return_value="git"),
                mock.patch.object(preflight.shutil, "disk_usage", return_value=SimpleNamespace(free=21 * 1024**3)),
            ):
                report = preflight.run(
                    self.suite_root, self.repository, binary, runtime, codex, auth, isolation
                )
            self.assertTrue(report["passed"])
            self.assertFalse(report["formal_evidence"])
            self.assertFalse(isolation.exists())

    def test_preflight_rejects_changed_runtime_artifact_before_execution(self) -> None:
        with tempfile.TemporaryDirectory(dir=self.repository / ".tmp") as directory:
            root = Path(directory)
            binary = root / "ownward.exe"
            codex = root / "codex.exe"
            auth = root / "auth.json"
            runtime = root / "runtime"
            runtime.mkdir()
            for path in (binary, codex, auth, runtime / "model.gguf", runtime / "runtime.dll"):
                path.write_bytes(b"fixture")
            manifest = {
                "capability": "embeddinggemma-q8",
                "model": {"path": "model.gguf", "sha256": "0" * 64},
                "runtime": {"files": {"runtime.dll": self._sha(runtime / "runtime.dll")}},
            }
            (runtime / "manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
            with mock.patch.object(
                preflight.subprocess,
                "run",
                return_value=SimpleNamespace(returncode=0, stdout="codex-cli 1.0\n", stderr=""),
            ):
                with self.assertRaisesRegex(preflight.PreflightError, "摘要不一致"):
                    preflight.run(
                        self.suite_root, self.repository, binary, runtime, codex, auth, root / "isolation"
                    )

    @staticmethod
    def _sha(path: Path) -> str:
        return hashlib.sha256(path.read_bytes()).hexdigest()


if __name__ == "__main__":
    unittest.main()
