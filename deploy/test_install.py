import importlib.util
import json
import hashlib
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('installer', Path(__file__).with_name('install.py'))
installer = importlib.util.module_from_spec(spec)
# Import in tests so the missing implementation is an explicit test failure.

class InstallTests(unittest.TestCase):
    def setUp(self):
        self.assertTrue(Path(__file__).with_name('install.py').exists(), 'release installer missing')
        spec.loader.exec_module(installer)
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.base = Path(self.tmp.name)
        self.bundle = self.base / 'bundle'
        self.bundle.mkdir()
        files = {'sysbox': b'binary', 'sysbox-init': b'helper', 'sysbox-netns': b'netns',
                 'deploy/systemd/sysbox-host.service': b'host unit',
                 'deploy/systemd/sysbox-agent.service': b'agent unit',
                 'deploy/docker/agent.yaml': b'version: 1\n',
                 'build-metadata.json': json.dumps({'commit': 'a'*40, 'version': 'v0.1.0', 'os': 'linux', 'architecture': installer.machine_arch()}).encode()}
        for name, data in files.items():
            path = self.bundle / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(data)
        (self.bundle/'INSTALL-SHA256SUMS').write_text(''.join(hashlib.sha256(data).hexdigest()+'  '+name+'\n' for name,data in files.items()))
        self.inputs = self.base/'inputs'
        self.inputs.mkdir()
        for name,data in {'identity.json': json.dumps({'id':'host','api_url':'http://localhost:9876','secret':'communication-secret'}).encode(), 'host.key': bytes(range(64)), 'host.pub': bytes(range(32,64)), 'issuer.pub': b'i'*32, 'sysbox.yaml': b'version: 1\n'}.items():
            (self.inputs/name).write_bytes(data)
        self.root = self.base/'root'

    def test_install_and_refuse_overwrite(self):
        installer.install(self.bundle, self.inputs, self.root, check=False)
        self.assertEqual((self.root/'usr/local/bin/sysbox').read_bytes(), b'binary')
        self.assertEqual((self.root/'etc/sysbox-host/host.key').stat().st_mode & 0o777, 0o600)
        self.assertFalse((self.root/'etc/sysbox-worker/host.key').exists())
        with self.assertRaisesRegex(ValueError, 'existing'):
            installer.install(self.bundle, self.inputs, self.root, check=False)

    def test_tamper_and_missing_input_do_not_write(self):
        (self.bundle/'sysbox').write_bytes(b'tamper')
        with self.assertRaisesRegex(ValueError, 'checksum'):
            installer.install(self.bundle, self.inputs, self.root, check=False)
        self.assertFalse(self.root.exists())

    def test_check_does_not_write(self):
        installer.install(self.bundle, self.inputs, self.root, check=True)
        self.assertFalse(self.root.exists())

    def test_reject_admin_identity_and_symlink_destination(self):
        (self.inputs/'identity.json').write_text(json.dumps({'id':'host','api_url':'http://localhost','secret':'s','token':'admin'}))
        with self.assertRaisesRegex(ValueError, 'identity'):
            installer.install(self.bundle,self.inputs,self.root,check=False)
        self.assertFalse(self.root.exists())

    def test_missing_checksum_entry(self):
        path=self.bundle/'INSTALL-SHA256SUMS'
        path.write_text('\n'.join(line for line in path.read_text().splitlines() if not line.endswith('  sysbox'))+'\n')
        with self.assertRaisesRegex(ValueError, 'checksum'):
            installer.install(self.bundle,self.inputs,self.root,check=False)
        self.assertFalse(self.root.exists())

if __name__ == '__main__':
    unittest.main()
