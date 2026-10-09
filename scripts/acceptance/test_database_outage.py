import unittest
from unittest import mock

import database_outage


class DatabaseOutage(unittest.TestCase):
    def test_already_cancelling_guest_is_not_live_precondition(self):
        observed = {"record": {"status": "running", "cancel_requested": True}, "cgroup_events": "populated 1\n"}
        self.assertFalse(database_outage.running(observed))
        observed["record"]["cancel_requested"] = False
        self.assertTrue(database_outage.running(observed))

    def test_natural_exit_is_not_fault_cancellation(self):
        observed = {"record": {"status": "exited", "termination_confirmed": True, "cancel_requested": False}, "cgroup_events": "populated 0\n"}
        self.assertFalse(database_outage.cancelled(observed))
        observed["record"].update(status="cancelled", cancel_requested=True)
        self.assertTrue(database_outage.cancelled(observed))

    def test_replaced_task_cgroup_is_rejected(self):
        before = {"cgroup_inode": 1, "record": {"pid": 5, "start_ticks": 6, "supervisor_pid": 7, "supervisor_start_ticks": 8}}
        after = {**before, "cgroup_inode": 2}
        with self.assertRaisesRegex(RuntimeError, "incarnation"):
            database_outage.same_guest(before, after)

    def test_missing_live_child_never_stops_database(self):
        execute = mock.Mock(return_value="")
        with self.assertRaisesRegex(RuntimeError, "exactly one"):
            database_outage.inject("p9-test", execute, lambda _: None)
        self.assertFalse(any("systemctl stop" in call.args[1] for call in execute.call_args_list))

    def test_database_restored_when_outage_probe_fails(self):
        execute = mock.Mock()
        evidence = {}
        probe = mock.Mock(side_effect=RuntimeError("guest probe failed"))
        with self.assertRaisesRegex(RuntimeError, "guest probe failed"):
            database_outage.during_outage(execute, probe, evidence, lambda _: None)
        execute.assert_any_call("control", "sudo systemctl start postgresql@16-main")
        self.assertTrue(evidence["database_restored"])


if __name__ == "__main__":
    unittest.main()
