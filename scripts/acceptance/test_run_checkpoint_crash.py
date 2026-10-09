import unittest
import json
from unittest import mock

import run_checkpoint_crash as crash
import recover_failed_cleanup as cleanup


class CheckpointCrash(unittest.TestCase):
    def test_crash_records_restart_and_termination(self):
        before = {"state": "intent", "run_checkpoint": {"version": 1, "children": [{"handle": {"ID": "task"}}]}}
        after = {"state": "complete", "reported": True, "result": {"status": "failed", "error": "run controller stopped after resource execution; child termination confirmed"}}
        running = {"handle": {"ID": "task"}, "result": {"Status": "running"}}
        stopped = {"handle": {"ID": "task"}, "stop_receipt": {}, "reported": True}
        stopped["stop_receipt"] = {"quiescence": "confirmed"}
        execute = mock.Mock(side_effect=[json.dumps(before), json.dumps(running), "123", "", json.dumps(after), json.dumps(stopped), "456"])
        evidence = crash.inject("p9-test", execute, lambda _: None)
        self.assertEqual(evidence["new_pid"], 456)

    def test_missing_checkpoint_never_kills_supervisor(self):
        execute = mock.Mock(return_value="")
        with self.assertRaisesRegex(RuntimeError, "exactly one"):
            crash.inject("p9-test", execute, lambda _: None)
        self.assertFalse(any("systemctl kill" in call.args[0] for call in execute.call_args_list))

    def test_cleanup_accepts_only_confirmed_failures(self):
        for reason in ("run child execution failed after confirmed termination",
                       "run controller stopped after resource execution; child termination confirmed"):
            self.assertTrue(cleanup.confirmed_failure({"status": "failed", "error": reason}))
            self.assertFalse(cleanup.confirmed_failure({"status": "running", "error": reason}))
        self.assertFalse(cleanup.confirmed_failure({"status": "failed", "error": "run lease expired"}))


if __name__ == "__main__":
    unittest.main()
