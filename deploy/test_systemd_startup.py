"""Check service-manager recovery policy without launching any host service."""
import configparser
from pathlib import Path
import unittest

class StartupPolicyTests(unittest.TestCase):
    def test_dependencies_can_recover_after_startup_delay(self):
        for service in ('sysbox-host', 'sysbox-agent'):
            with self.subTest(service=service):
                unit=configparser.ConfigParser(interpolation=None,strict=False)
                unit.read(Path(__file__).parent/'systemd'/f'{service}.service')
                self.assertEqual(unit['Service']['Restart'],'on-failure')
                self.assertEqual(unit['Service'].get('RestartSec'),'5s')
                self.assertEqual(unit['Unit'].get('StartLimitIntervalSec'),'0')
                if service=='sysbox-agent':
                    self.assertIn('sysbox-host.service',unit['Unit'].get('Wants',''))
                    self.assertNotIn('sysbox-host.service',unit['Unit'].get('Requires',''))
                    self.assertIn('sysbox-host.service',unit['Unit']['After'])

if __name__ == '__main__':
    unittest.main()
