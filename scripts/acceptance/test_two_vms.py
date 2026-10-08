import json
import hashlib
import subprocess
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import two_vms


class LabTests(unittest.TestCase):
    def test_partial_start_keeps_host_keys_and_canonical_root(self):
        with tempfile.TemporaryDirectory() as directory:
            parent = Path(directory)
            alias = parent / "alias"
            alias.symlink_to(parent, target_is_directory=True)
            base = parent / "base"
            base.write_bytes(b"test-image")

            def process(*args, **kwargs):
                if args[0] == "git":
                    return subprocess.CompletedProcess(args, 0, stdout="test-commit\n")
                if args[0] == "ssh-keygen":
                    key = Path(args[-1])
                    key.write_text("test-private")
                    key.with_suffix(".pub").write_text("ssh-ed25519 test-public")
                if args[0] == "qemu-system-x86_64" and "execution" in args[2]:
                    raise RuntimeError("second start failed")
                return subprocess.CompletedProcess(args, 0)

            with mock.patch.object(two_vms, "BASE_SHA256", hashlib.sha256(b"test-image").hexdigest()), \
                 mock.patch.object(two_vms.os, "access", return_value=True), \
                 mock.patch.object(two_vms, "free_ports", return_value=[30001, 30002, 30003]), \
                 mock.patch.object(two_vms, "run", side_effect=process):
                with self.assertRaisesRegex(RuntimeError, "second start failed"):
                    two_vms.up(alias / "lab", base)
            root, _ = two_vms.load(parent / "lab")
            self.assertIn("[127.0.0.1]:30001", (root / "known_hosts").read_text())

    def manifest(self, root):
        data = {"format": 1, "root": str(root), "id": "test-lab"}
        two_vms.save(root / "lab.json", data)
        return data

    def test_rejects_changed_root_binding(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            data = self.manifest(root)
            data["root"] = "/foreign-lab"
            two_vms.save(root / "lab.json", data)
            with self.assertRaisesRegex(RuntimeError, "manifest"):
                two_vms.load(root)

    def test_wrong_vm_identity_cannot_be_stopped(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.manifest(root)
            (root / "execution").mkdir()
            (root / "execution" / "qmp").touch()
            with mock.patch.object(two_vms, "qmp", return_value={"name": "another-vm"}) as monitor:
                with self.assertRaisesRegex(RuntimeError, "foreign VM"):
                    two_vms.stop(root)
                monitor.assert_called_once_with(root / "execution" / "qmp", "query-name")

    def test_unreachable_vm_does_not_prevent_stopping_other_owned_vm(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.manifest(root)
            for role in two_vms.ROLES:
                (root / role).mkdir()
                (root / role / "qmp").touch()
            def monitor(path, command):
                if path.parent.name == "execution":
                    raise ConnectionRefusedError("stale socket")
                return {"name": "test-lab-control"}
            with mock.patch.object(two_vms, "qmp", side_effect=monitor) as calls:
                with self.assertRaises(RuntimeError):
                    two_vms.stop(root)
                self.assertIn(mock.call(root / "control" / "qmp", "quit"), calls.call_args_list)

    def test_bad_image_does_not_create_lab_or_start_process(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            base = root / "bad.img"
            base.write_bytes(b"not the pinned image")
            with mock.patch.object(two_vms, "run") as processes:
                with self.assertRaisesRegex(RuntimeError, "checksum"):
                    two_vms.up(root / "lab", base)
                processes.assert_not_called()
                self.assertFalse((root / "lab").exists())

    def test_manifest_round_trip(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            data = self.manifest(root)
            self.assertEqual((root, data), two_vms.load(root))
            self.assertEqual(data, json.loads((root / "lab.json").read_text()))


if __name__ == "__main__":
    unittest.main()
