import copy
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import database_outage


class DatabaseOutage(unittest.TestCase):
    def test_post_restoration_validation_failure_is_persisted(self):
        def inject(topology, execute, save, fault):
            save({"topology": topology, "network_restored": True})
            raise RuntimeError("stop receipt mismatch")

        with tempfile.TemporaryDirectory() as root:
            with mock.patch("sys.argv", ["database_outage.py", "--root", root, "--topology", "p9-test", "--fault", "private-network"]), \
                    mock.patch.object(database_outage.lab, "load", return_value=(Path(root), {"id": "lab"})), \
                    mock.patch.object(database_outage.lab, "save") as save, \
                    mock.patch.object(database_outage, "inject", side_effect=inject):
                with self.assertRaisesRegex(RuntimeError, "stop receipt mismatch"):
                    database_outage.main()
                self.assertEqual(save.call_args.args[1], {"lab_id": "lab", "topology": "p9-test",
                                                          "network_restored": True, "acceptance_error": "stop receipt mismatch"})

    def test_stop_report_is_bound_to_original_task(self):
        handle = {"ID": "task", "Digest": "digest", "BootID": "boot", "Epoch": 3,
                  "TopologyUID": "owner", "ResourceUID": "resource", "Generation": 1}
        grant = {"scope": {"topology_uid": "owner", "resource_uid": "resource", "generation": 1},
                 "target_boot_id": "boot", "agent_id": "agent", "session_id": "session", "action_id": "task",
                 "command_id": "command", "attempt": 1, "epoch": 3, "parent_epoch": 2,
                 "operation": "guest_task", "digest": "digest", "expires_at": "earlier", "state": "active"}
        before = {"handle": handle, "run_id": "run", "provider_node_id": "container", "grant": {"grant": grant}}
        after = copy.deepcopy(before)
        after.update(reported=True, result={"RemoteTermination": "confirmed"}, stop_receipt={
            "grant": {**grant, "expires_at": "later"}, "task_id": "task", "task_digest": "digest",
            "guest_boot_id": "boot", "quiescence": "confirmed", "signature": "signed"})
        database_outage.verify_stop_report(before, after)
        for field in ("epoch", "scope", "digest", "session_id", "command_id", "parent_epoch"):
            changed = copy.deepcopy(after)
            changed["stop_receipt"]["grant"][field] = "wrong"
            with self.subTest(field=field), self.assertRaisesRegex(RuntimeError, "stop receipt"):
                database_outage.verify_stop_report(before, changed)
        changed = copy.deepcopy(after)
        changed["provider_node_id"] = "replacement"
        with self.assertRaisesRegex(RuntimeError, "identity"):
            database_outage.verify_stop_report(before, changed)

    def test_partition_rejects_reachable_service(self):
        execute = mock.Mock(return_value='{"19870": false, "5432": true}')
        with self.assertRaisesRegex(RuntimeError, "connectivity"):
            database_outage.require_connectivity(execute, False)

    def test_partition_requires_both_service_probes(self):
        execute = mock.Mock(return_value='{"19870": false}')
        with self.assertRaisesRegex(RuntimeError, "connectivity"):
            database_outage.require_connectivity(execute, False)

    def test_partition_accepts_both_unreachable_services(self):
        execute = mock.Mock(return_value='{"19870": false, "5432": false}')
        self.assertEqual(database_outage.require_connectivity(execute, False), {"19870": False, "5432": False})

    @mock.patch("database_outage.time.sleep")
    def test_network_restoration_waits_for_connectivity(self, sleep):
        evidence = {}
        calls = []

        def execute(role, command):
            calls.append(command)
            if command == "ping -c 1 -W 1 10.77.0.10":
                if calls.count(command) == 1:
                    raise RuntimeError("link not ready")
            return ""

        database_outage.during_outage(execute, lambda: None, evidence, lambda _: None, "private-network")
        self.assertEqual(calls.count("sudo ip link set dev lan0 up"), 1)
        self.assertEqual(calls.count("ping -c 1 -W 1 10.77.0.10"), 2)
        self.assertTrue(evidence["network_restored"])
        sleep.assert_called_once_with(1)

    def test_probe_and_restore_errors_are_both_persisted(self):
        evidence, saved = {}, []

        def execute(role, command):
            if "lan0 up" in command:
                raise RuntimeError("restore failed")

        with self.assertRaisesRegex(RuntimeError, "probe failed"):
            database_outage.during_outage(execute, mock.Mock(side_effect=RuntimeError("probe failed")), evidence,
                                         lambda value: saved.append(dict(value)), "private-network")
        self.assertEqual(saved[-1]["fault_error"], "probe failed")
        self.assertEqual(saved[-1]["restoration_error"], "restore failed")
        self.assertNotIn("network_restored", evidence)

    @mock.patch("database_outage.time.sleep")
    def test_restoration_timeout_is_bounded_and_saved(self, sleep):
        evidence, saved = {}, []

        def execute(role, command):
            if command.startswith("ping "):
                raise RuntimeError("still unreachable")

        with self.assertRaisesRegex(RuntimeError, "still unreachable"):
            database_outage.during_outage(execute, lambda: None, evidence,
                                         lambda value: saved.append(dict(value)), "private-network")
        self.assertEqual(len(saved[-1]["network_readiness_errors"]), 10)
        self.assertEqual(saved[-1]["restoration_error"], "still unreachable")
        self.assertEqual(sleep.call_count, 9)
        self.assertNotIn("network_restored", evidence)

    def test_private_network_restored_when_probe_fails(self):
        execute = mock.Mock()
        evidence = {}
        with self.assertRaisesRegex(RuntimeError, "probe failed"):
            database_outage.during_outage(execute, mock.Mock(side_effect=RuntimeError("probe failed")), evidence, lambda _: None, "private-network")
        execute.assert_any_call("execution", "sudo ip link set dev lan0 down")
        execute.assert_any_call("execution", "sudo ip link set dev lan0 up")
        execute.assert_any_call("execution", "ping -c 1 -W 1 10.77.0.10")
        self.assertTrue(evidence["network_restored"])
        self.assertFalse(any("postgresql" in c.args[1] for c in execute.call_args_list))

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
