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
            with self.assertRaises(process_control.ProcessTimeout):
                process_control.run(
                    [sys.executable, "-c", "import time; time.sleep(5)"],
                    cwd=Path(directory),
                    timeout=0.05,
                )
        self.assertLess(time.perf_counter() - started, 2)


if __name__ == "__main__":
    unittest.main()
