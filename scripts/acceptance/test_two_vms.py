import json
import hashlib
import subprocess
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import two_vms


class LabTests(unittest.TestCase):
    def retained_lab(self, root):
        manifest = self.manifest(root)
        manifest["ports"] = {"control": 30001, "execution": 30002, "lan": 30003}
        two_vms.save(root / "lab.json", manifest)
        for role in two_vms.ROLES:
            directory = root / role
            directory.mkdir()
            (directory / "disk.qcow2").write_bytes(b"retained-disk")
            (directory / "seed.iso").write_bytes(b"retained-seed")
        return manifest

    def test_start_reuses_retained_disks_without_recreating_keys(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.retained_lab(root)
            with mock.patch.object(two_vms.os, "access", return_value=True), \
                 mock.patch.object(two_vms, "run") as launch:
                two_vms.start(root)
            self.assertEqual(launch.call_count, 2)
            for call in launch.call_args_list:
                self.assertEqual(call.args[0], "qemu-system-x86_64")
                self.assertNotIn("qemu-img", call.args)
            for role in two_vms.ROLES:
                self.assertEqual((root / role / "disk.qcow2").read_bytes(), b"retained-disk")

    def test_start_checks_both_old_processes_before_launch(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.retained_lab(root)
            (root / "execution" / "pid").write_text(str(two_vms.os.getpid()))
            with mock.patch.object(two_vms.os, "access", return_value=True), \
                 mock.patch.object(two_vms, "run") as launch:
                with self.assertRaisesRegex(RuntimeError, "still exists"):
                    two_vms.start(root)
            launch.assert_not_called()

    def test_start_rejects_concurrent_starter(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.retained_lab(root)
            with (root / "start.lock").open("a") as lock, \
                 mock.patch.object(two_vms, "run") as launch:
                two_vms.fcntl.flock(lock, two_vms.fcntl.LOCK_EX | two_vms.fcntl.LOCK_NB)
                with self.assertRaises(BlockingIOError):
                    two_vms.start(root)
            launch.assert_not_called()

    def test_start_rejects_dangling_process_links(self):
        for name in ("pid", "qmp"):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                self.retained_lab(root)
                (root / "execution" / name).symlink_to(root / "outside")
                with mock.patch.object(two_vms.os, "access", return_value=True), \
                     mock.patch.object(two_vms, "run") as launch:
                    with self.assertRaisesRegex(RuntimeError, "invalid retained"):
                        two_vms.start(root)
                launch.assert_not_called()

    def test_partial_restart_never_recreates_a_disk(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.retained_lab(root)
            def launch(*args):
                if args[2].endswith("-execution"):
                    raise RuntimeError("second launch failed")
                return subprocess.CompletedProcess(args, 0)
            with mock.patch.object(two_vms.os, "access", return_value=True), \
                 mock.patch.object(two_vms, "run", side_effect=launch):
                with self.assertRaisesRegex(RuntimeError, "second launch"):
                    two_vms.start(root)
            for role in two_vms.ROLES:
                self.assertEqual((root / role / "disk.qcow2").read_bytes(), b"retained-disk")

    def test_ssh_timeout_does_not_replay_unknown_remote_command(self):
        failure = subprocess.TimeoutExpired(["ssh"], 30)
        with mock.patch.object(two_vms.subprocess, "run", side_effect=failure) as call, \
             mock.patch.object(two_vms.time, "sleep"):
            with self.assertRaisesRegex(RuntimeError, "did not become ready"):
                two_vms.ssh(Path("/tmp/lab"), {"ports": {"control": 1234}}, "control", "perform-mutation")
            self.assertEqual(call.call_count, 1)

    def test_ssh_remote_error_text_does_not_trigger_retry(self):
        failure = subprocess.CompletedProcess(["ssh"], 1, stdout="", stderr="Connection timed out")
        with mock.patch.object(two_vms.subprocess, "run", return_value=failure) as call, \
             mock.patch.object(two_vms.time, "sleep"):
            self.assertIs(two_vms.ssh(Path("/tmp/lab"), {"ports": {"control": 1234}}, "control", "perform-mutation"), failure)
            self.assertEqual(call.call_count, 1)

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
                 mock.patch.object(two_vms.shutil, "disk_usage", return_value=mock.Mock(free=20 * 1024**3)), \
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
