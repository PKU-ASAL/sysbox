import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import child_partition


class PartitionRecovery(unittest.TestCase):
    def test_proxy_restored_when_observation_fails(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            calls = []
            observations = 0

            def execute(_root, _manifest, role, command):
                nonlocal observations
                calls.append((role, command))
                if role == "execution":
                    observations += 1
                    if observations > 1:
                        raise RuntimeError("observation unavailable")
                    return json.dumps({"result": {"Status": "running"}})
                return ""

            with mock.patch("sys.argv", ["child_partition.py", "--root", directory, "--topology", "p9-test"]), \
                 mock.patch.object(child_partition.lab, "load", return_value=(root, {"id": "lab"})), \
                 mock.patch.object(child_partition, "execute", side_effect=execute):
                with self.assertRaisesRegex(RuntimeError, "observation unavailable"):
                    child_partition.main()
            self.assertIn(("control", "sudo systemctl start haproxy"), calls)
            evidence = json.loads((root / "p9-test-partition.json").read_text())
            self.assertTrue(evidence["proxy_restored"])
            self.assertNotIn("after", evidence)

    def test_nonrunning_child_never_triggers_fault(self):
        with tempfile.TemporaryDirectory() as directory:
            with mock.patch("sys.argv", ["child_partition.py", "--root", directory, "--topology", "p9-test"]), \
                 mock.patch.object(child_partition.lab, "load", return_value=(Path(directory), {"id": "lab"})), \
                 mock.patch.object(child_partition, "execute", return_value=json.dumps({"result": {"Status": "exited"}})) as execute:
                with self.assertRaisesRegex(RuntimeError, "observed running"):
                    child_partition.main()
                self.assertEqual(execute.call_count, 1)
                self.assertEqual(execute.call_args.args[2], "execution")


if __name__ == "__main__":
    unittest.main()
