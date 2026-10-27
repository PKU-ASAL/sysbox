import tempfile
import unittest
from pathlib import Path
import host_fencing

class FenceProofTests(unittest.TestCase):
    def test_proof_requires_verified_process_exit_and_new_boot(self):
        value = dict(lab_id='isolated', role='execution', request_id='fence-1', old_boot='old', new_boot='new', qemu_exited=True, control_boot_unchanged=True, provider_scope='local-only', operation_result='unknown')
        host_fencing.validate_proof(value)
        for field, bad in [('new_boot', 'old'), ('qemu_exited', False), ('control_boot_unchanged', False), ('provider_scope','remote'), ('operation_result','succeeded')]:
            with self.subTest(field=field):
                altered = dict(value, **{field:bad})
                with self.assertRaises(ValueError): host_fencing.validate_proof(altered)

    def test_existing_lab_requires_matching_explicit_scope(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            with self.assertRaises((ValueError, FileNotFoundError)):
                host_fencing.check_scope(root, {'id':'historical'})

if __name__ == '__main__': unittest.main()

class FenceSafetyTests(unittest.TestCase):
    def test_pidfd_preflight_precedes_any_guest_mutation(self):
        from types import SimpleNamespace
        from unittest import mock
        with mock.patch.object(host_fencing, 'os', SimpleNamespace()), mock.patch.object(host_fencing.two_vms, 'load') as load:
            with self.assertRaisesRegex(RuntimeError, 'pidfd-capable'):
                host_fencing.fence('/unused')
            load.assert_not_called()

    @unittest.skipUnless(hasattr(host_fencing.os, 'pidfd_open'), 'system Python pidfd required')
    def test_foreign_process_rejected_before_monitor_command(self):
        from unittest import mock
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            execution = root / 'execution'
            execution.mkdir()
            for name in ('disk.qcow2','seed.iso'):
                (execution / name).write_bytes(b'test')
            (execution / 'pid').write_text(str(host_fencing.os.getpid()))
            with mock.patch.object(host_fencing.two_vms, 'qmp') as monitor:
                with self.assertRaisesRegex(ValueError, 'identity mismatch'):
                    host_fencing.capture_vm(root, {'id':'isolated'})
                monitor.assert_not_called()

class FenceAdmissionTests(unittest.TestCase):
    def test_request_matches_lab_and_original_kernel_before_actuation(self):
        from unittest import mock
        import json
        import base64
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'admission.json'
            key = Path(tmp) / 'key'
            key.write_bytes(b'a' * 64)
            value = {'request': {'lab_id': 'isolated', 'grant': {'target_boot_id': 'old'}, 'controller_key': base64.b64encode(b'a' * 32).decode()}}
            path.write_text(json.dumps(value))
            with mock.patch.object(host_fencing.subprocess, 'run') as signer:
                signer.return_value.stdout = 'a' * 64 + '\n'
                record, digest = host_fencing.check_admission(path, {'id': 'isolated'}, 'old', '/signer', key)
                self.assertEqual(digest, 'a' * 64)
                for lab, boot in [('historical', 'old'), ('isolated', 'new')]:
                    with self.assertRaises(ValueError):
                        host_fencing.check_admission(path, {'id': lab}, boot, '/signer', key)
                key.write_bytes(b'b' * 64)
                with self.assertRaises(ValueError):
                    host_fencing.check_admission(path, {'id': 'isolated'}, 'old', '/signer', key)
