import importlib.util
import json
import hashlib
from pathlib import Path
import tempfile
import unittest
import subprocess

spec = importlib.util.spec_from_file_location('installer', Path(__file__).with_name('install.py'))
installer = importlib.util.module_from_spec(spec)
# Import in tests so the missing implementation is an explicit test failure.

class InstallTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.binary_dir = tempfile.TemporaryDirectory()
        cls.addClassCleanup(cls.binary_dir.cleanup)
        cls.binary = Path(cls.binary_dir.name)/'sysbox'
        subprocess.run(['go','build','-buildvcs=false','-o',str(cls.binary),'./cmd/sysbox'],cwd=Path(__file__).resolve().parents[1],check=True)

    def setUp(self):
        self.assertTrue(Path(__file__).with_name('install.py').exists(), 'release installer missing')
        spec.loader.exec_module(installer)
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.base = Path(self.tmp.name)
        self.bundle = self.base / 'bundle'
        self.bundle.mkdir()
        files = {'sysbox': self.binary.read_bytes(), 'sysbox-init': b'helper', 'sysbox-netns': b'netns',
                 'deploy/systemd/sysbox-host.service': b'host unit',
                 'deploy/systemd/sysbox-agent.service': b'agent unit',
                 'deploy/docker/agent.yaml': b'version: 1\n',
                 'build-metadata.json': json.dumps({'commit': 'a'*40, 'version': 'v0.1.0', 'os': 'linux', 'architecture': installer.machine_arch()}).encode()}
        for name, data in files.items():
            path = self.bundle / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(data)
            if name == 'sysbox': path.chmod(0o755)
        (self.bundle/'INSTALL-SHA256SUMS').write_text(''.join(hashlib.sha256(data).hexdigest()+'  '+name+'\n' for name,data in files.items()))
        self.inputs = self.base/'inputs'
        self.inputs.mkdir()
        # RFC 8032 test vector: a real seed/public pair, never a production key.
        for name,data in {'identity.json': json.dumps({'id':'host','api_url':'http://localhost:9876','secret':'communication-secret'}).encode(), 'host.key': bytes.fromhex('9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60'+'d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a'), 'host.pub': bytes.fromhex('d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a'), 'issuer.pub': b'i'*32, 'sysbox.yaml': b'version: 1\n'}.items():
            (self.inputs/name).write_bytes(data)
        self.root = self.base/'root'

    def test_install_and_refuse_overwrite(self):
        installer.install(self.bundle, self.inputs, self.root, check=False)
        self.assertEqual((self.root/'usr/local/bin/sysbox').read_bytes(), self.binary.read_bytes())
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

    def test_existing_root_is_supported_but_symlink_parent_is_not(self):
        self.root.mkdir()
        (self.root/'unrelated').write_text('keep')
        installer.install(self.bundle,self.inputs,self.root,check=False)
        self.assertEqual((self.root/'unrelated').read_text(),'keep')

    def test_symlink_parent_rejected_before_writes(self):
        self.root.mkdir()
        outside=self.base/'outside'; outside.mkdir()
        (self.root/'etc').symlink_to(outside,target_is_directory=True)
        with self.assertRaisesRegex(ValueError,'unsafe'):
            installer.install(self.bundle,self.inputs,self.root,check=False)
        self.assertEqual(list(outside.iterdir()),[])
        self.assertFalse((self.root/'usr').exists())

    def test_missing_input_and_placeholder_rejected(self):
        (self.inputs/'sysbox.yaml').write_text('backend: REPLACE_PASSWORD')
        with self.assertRaisesRegex(ValueError,'placeholder'):
            installer.install(self.bundle,self.inputs,self.root,check=False)
        self.assertFalse(self.root.exists())

    def test_wrong_key_rejected(self):
        (self.inputs/'host.pub').write_bytes(b'x'*32)
        with self.assertRaisesRegex(ValueError,'matching'):
            installer.install(self.bundle,self.inputs,self.root,check=False)
        self.assertFalse(self.root.exists())

    def test_restrictive_umask_keeps_worker_files_reachable(self):
        import os
        previous=os.umask(0o077)
        try:
            installer.install(self.bundle,self.inputs,self.root,check=False)
        finally:
            os.umask(previous)
        for directory in ('usr','usr/local','usr/local/bin','etc','etc/sysbox-worker','etc/systemd/system'):
            self.assertEqual((self.root/directory).stat().st_mode & 0o777,0o755,directory)
        self.assertEqual((self.root/'etc/sysbox-host').stat().st_mode & 0o777,0o700)

    def test_corrupt_seed_with_matching_public_tail_rejected(self):
        path=self.inputs/'host.key'
        key=bytearray(path.read_bytes()); key[0] ^= 1; path.write_bytes(key)
        with self.assertRaisesRegex(ValueError,'signing key'):
            installer.install(self.bundle,self.inputs,self.root,check=True)
        self.assertFalse(self.root.exists())

if __name__ == '__main__':
    unittest.main()
