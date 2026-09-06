import hashlib
from pathlib import Path
import subprocess
import tempfile
import unittest

import frozen_inputs


class FrozenInputsTests(unittest.TestCase):
    def test_history_is_exact_and_never_rewrites_or_executes_current_source(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "runner.py"
            original = b"raise RuntimeError('must never execute archived code')\n"
            source.write_bytes(original)
            def git(*args):
                return subprocess.run(["git", *args], cwd=root, check=True, capture_output=True)
            git("init")
            git("add", "runner.py")
            git("-c", "user.name=Test", "-c", "user.email=test@example.invalid",
                "commit", "-m", "fixture")
            digest = hashlib.sha256(original).hexdigest()
            for current in (b"new gate implementation\n", b"new final-test implementation\n"):
                source.write_bytes(current)
                self.assertEqual(original.decode(), frozen_inputs.read_text(root, "runner.py", digest))
                self.assertEqual(current, source.read_bytes())
            with self.assertRaisesRegex(ValueError, "unavailable or digest mismatch"):
                frozen_inputs.read_text(root, "runner.py", "f" * 64)
            with self.assertRaisesRegex(ValueError, "Invalid frozen input"):
                frozen_inputs.read_text(root, "../runner.py", digest)

    def test_current_frozen_input_needs_no_repository_and_checks_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "receipt.json"
            source.write_bytes(b'{"identity":"original"}\r\n')
            digest = hashlib.sha256(source.read_bytes().replace(b"\r\n", b"\n")).hexdigest()
            item = {"path": "receipt.json", "sha256": digest, "identity": "original"}
            frozen_inputs.verify_files(root, [item])
            with self.assertRaisesRegex(ValueError, "identity mismatch"):
                frozen_inputs.verify_files(root, [{**item, "identity": "changed"}])


if __name__ == "__main__":
    unittest.main()
