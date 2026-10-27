import copy
import json
import unittest

import live_failover


class FailoverTests(unittest.TestCase):
    def test_success_requires_primary_still_down_and_restores_it(self):
        before = {"result": {"Status": "running"}, "provider_node_id": "a"*64,
                  "handle": {"ID": "task"}, "run_id": "run"}
        after = dict(before, reported=True, stop_receipt={"receipt": "proof"},
                     result={"ExitCodeKnown": True, "ExitCode": 0, "RemoteTermination": "confirmed"})
        calls, saved = [], []
        records = iter([before, after])
        def execute(role, command):
            calls.append(command)
            if "find " in command:
                return json.dumps(next(records))
            if "cgroup.events" in command:
                return "populated 1\n"
            if "python3 -c" in command:
                return json.dumps({"status": "done", "host_completion": {"receipt": "proof"}})
            return ""
        result = live_failover.inject(execute, "p9-test", lambda value: saved.append(copy.deepcopy(value)))
        self.assertTrue(result["primary_still_stopped_at_completion"])
        self.assertTrue(result["primary_restored"])
        self.assertTrue(any(cmd.startswith("! systemctl") for cmd in calls))

    def test_primary_restored_on_cancelled_child(self):
        before = {"result": {"Status": "running"}, "provider_node_id": "a"*64,
                  "handle": {"ID": "task"}, "run_id": "run"}
        calls, saved = [], []
        count = 0
        def execute(role, command):
            nonlocal count
            calls.append(command)
            if "find " in command:
                count += 1
                return json.dumps(before if count == 1 else dict(before, cancel_requested=True))
            if "cgroup.events" in command:
                return "populated 1\n"
            return ""
        with self.assertRaisesRegex(RuntimeError, "cancelled"):
            live_failover.inject(execute, "p9-test", lambda value: saved.append(copy.deepcopy(value)))
        self.assertTrue(saved[-1]["primary_restored"])
        self.assertTrue(any("start sysbox-api1" in cmd for cmd in calls))

    def test_nonrunning_child_prevents_fault(self):
        calls = []
        def execute(role, command):
            calls.append(command)
            return json.dumps({"result": {"Status": "exited"}})
        with self.assertRaisesRegex(RuntimeError, "must be running"):
            live_failover.inject(execute, "p9-test", lambda value: None)
        self.assertEqual(len(calls), 1)
