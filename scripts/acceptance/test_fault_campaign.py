import tempfile
import unittest
from pathlib import Path
from unittest import mock
import fault_campaign

class FaultCampaignTests(unittest.TestCase):
    def test_historical_or_unscoped_lab_refused_before_effects(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            with mock.patch.object(fault_campaign.lab,'load',return_value=(root,{'id':'test'})), mock.patch.object(fault_campaign.lab,'ssh') as ssh:
                with self.assertRaises((ValueError,FileNotFoundError)):
                    fault_campaign.run(root,'failover')
                ssh.assert_not_called()

    def test_waits_for_exact_live_task_and_checkpoint(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            execute=mock.Mock(return_value='')
            with self.assertRaisesRegex(RuntimeError,'live child'):
                fault_campaign.wait_live(execute,'p9-test',timeout=0)
            execute.assert_not_called()
            with self.assertRaises(ValueError):fault_campaign.wait_live(execute,'../old',timeout=0)

    def test_owner_absence_query_cannot_accept_names_or_unknown(self):
        execute=mock.Mock(return_value='')
        fault_campaign.verify_absence(execute,'owner-123')
        self.assertEqual(execute.call_count,2)
        execute.return_value='remaining'
        with self.assertRaises(RuntimeError):fault_campaign.verify_absence(execute,'owner-123')
        with self.assertRaises(ValueError):fault_campaign.verify_absence(execute,'')

    def test_preflight_refuses_residue_before_new_workload(self):
        execute=mock.Mock(return_value='leftover-container')
        with self.assertRaisesRegex(RuntimeError,'prior managed'):
            fault_campaign.require_clean_execution(execute)
        self.assertEqual(execute.call_count,1)

    def test_missing_container_does_not_hide_unknown_host_intent(self):
        execute=mock.Mock(side_effect=['','','{"outcomes":{"old":{"state":"intent"}}}'])
        with self.assertRaisesRegex(RuntimeError,'unresolved host'):
            fault_campaign.require_clean_execution(execute)

    def test_running_builds_uses_service_executable_not_installed_file(self):
        calls=[]
        def execute(role,command):
            calls.append(command)
            return '42' if 'MainPID' in command else 'a'*64+' /proc/42/exe'
        result=fault_campaign.running_builds(execute)
        self.assertEqual(result,{'control':'a'*64,'execution':'a'*64})
        self.assertEqual(sum('/proc/42/exe' in command for command in calls),4)
        self.assertFalse(any('/usr/local/bin/sysbox' in command for command in calls))

    def test_failed_round_keeps_bounded_service_diagnostics(self):
        calls=[]
        def execute(role,command):
            calls.append((role,command))
            if role=='control':raise RuntimeError('API unreachable')
            return 'host diagnostic'
        result=fault_campaign.failure_diagnostics(execute)
        self.assertIn('control_error',result)
        self.assertEqual(result['execution'],'host diagnostic')
        self.assertEqual(len(calls),2)
        self.assertTrue(all('journalctl' in cmd and '-n 80' in cmd for _,cmd in calls))
