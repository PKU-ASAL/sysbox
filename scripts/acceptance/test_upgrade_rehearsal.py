import unittest
import upgrade_rehearsal

class UpgradeRehearsalTests(unittest.TestCase):
    def test_archive_scope_is_fixed_and_no_delete_command(self):
        commands=upgrade_rehearsal.archive_commands('/var/tmp/sysbox-upgrade-abc')
        joined='\n'.join(commands)
        self.assertIn('pg_dump',joined)
        self.assertIn('sysbox-api1',joined)
        self.assertNotIn('DROP',joined)
        self.assertNotIn('rm ',joined)
        for path in ('/','/var/lib/sysbox','/var/tmp/sysbox-upgrade-../other'):
            with self.assertRaises(ValueError):upgrade_rehearsal.archive_commands(path)

    def test_restoration_attempts_both_hosts_and_saves_failure(self):
        calls=[];evidence={};saved=[]
        def execute(role,command):
            calls.append((role,command))
            if role=='control':raise RuntimeError('control unavailable')
            return ''
        with self.assertRaises(RuntimeError):
            upgrade_rehearsal.restore_services(execute,evidence,lambda value:saved.append(dict(value)))
        self.assertTrue(any(role=='execution' for role,_ in calls))
        self.assertFalse(saved[-1]['services_restored'])
        self.assertIn('control',saved[-1]['restoration_errors'])
