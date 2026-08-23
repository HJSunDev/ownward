import sys
import tempfile
import time
import unittest
from pathlib import Path
import os

import process_control


class ProcessControlTests(unittest.TestCase):
    def test_forwards_input_and_environment(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            environment = os.environ.copy()
            environment["OWNWARD_PROCESS_CONTROL_TEST"] = "bound"
            completed = process_control.run(
                [sys.executable, "-c", "import os,sys; print(sys.stdin.read()+os.environ['OWNWARD_PROCESS_CONTROL_TEST'])"],
                cwd=Path(directory),
                timeout=2,
                input_text="input-",
                env=environment,
            )
        self.assertEqual(0, completed.returncode)
        self.assertEqual("input-bound", completed.stdout.strip())

    def test_timeout_stops_process_without_waiting_for_natural_exit(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            started = time.perf_counter()
            with self.assertRaises(process_control.ProcessTimeout) as raised:
                process_control.run(
                    [sys.executable, "-c", "import sys,time; print('started', flush=True); time.sleep(5)"],
                    cwd=Path(directory),
                    timeout=0.05,
                )
        self.assertLess(time.perf_counter() - started, 2)
        self.assertIn("started", raised.exception.stdout)

    def test_invalid_utf8_output_does_not_break_process_collection(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            completed = process_control.run(
                [sys.executable, "-c", "import sys; sys.stderr.buffer.write(b'bad-\\xff-output')"],
                cwd=Path(directory),
                timeout=2,
            )
        self.assertEqual(0, completed.returncode)
        self.assertIn("bad-\ufffd-output", completed.stderr)

    def test_streams_durable_output_before_timeout(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            stdout_path = root / "stdout.log"
            stderr_path = root / "stderr.log"
            with self.assertRaises(process_control.ProcessTimeout):
                process_control.run(
                    [sys.executable, "-c", "import sys,time; print('durable-out', flush=True); print('durable-err', file=sys.stderr, flush=True); time.sleep(5)"],
                    cwd=root,
                    timeout=0.1,
                    stdout_path=stdout_path,
                    stderr_path=stderr_path,
                )
            self.assertIn("durable-out", stdout_path.read_text(encoding="utf-8"))
            self.assertIn("durable-err", stderr_path.read_text(encoding="utf-8"))


if __name__ == "__main__":
    unittest.main()
