import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

PATH = Path(__file__).with_name('inspect-artifact.py')

class ArtifactTests(unittest.TestCase):
    def setUp(self):
        self.assertTrue(PATH.exists(), 'offline artifact verifier missing')
        spec = importlib.util.spec_from_file_location('artifact', PATH)
        self.module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.module)
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.image = Path(self.tmp.name)/'base.qcow2'
        self.image.write_bytes(b'qcow2 test fixture')
        self.image.chmod(0o444)
        self.manifest = Path(self.tmp.name)/'artifact.json'

    def inspect(self):
        return self.module.inspect_artifact(self.image, self.manifest, '/dev/sda1')

    def replies(self, argv):
        if argv[0] == 'qemu-img':
            return json.dumps({'format':'qcow2', 'virtual-size': 4096}).encode()
        self.assertIn('--ro', argv)
        return '[State]\nImageState=IMAGE_STATE_GENERALIZE_RESEAL_TO_OOBE\n'.encode('utf-16')

    def test_sealed_manifest(self):
        with patch.object(self.module, 'run_readonly', side_effect=self.replies):
            self.inspect()
        record=json.loads(self.manifest.read_text())
        self.assertEqual(record['guest_family'], 'windows')
        self.assertEqual(record['size'], self.image.stat().st_size)
        self.assertEqual(len(record['sha256']),64)
        self.assertEqual(record['image_state'],'IMAGE_STATE_GENERALIZE_RESEAL_TO_OOBE')
        self.assertFalse(record['runtime_accepted'])

    def test_writable_or_link_rejected(self):
        self.image.chmod(0o644)
        with patch.object(self.module,'run_readonly') as command:
            with self.assertRaisesRegex(ValueError,'read-only'): self.inspect()
            command.assert_not_called()
        self.image.chmod(0o444)
        link=self.image.with_name('link.qcow2');link.symlink_to(self.image)
        with self.assertRaisesRegex(ValueError,'regular'):
            self.module.inspect_artifact(link,self.manifest,'/dev/sda1')

    def test_unsealed_or_backing_chain_not_published(self):
        for case in ['unsealed','backing']:
            def reply(argv):
                if argv[0]=='qemu-img':
                    return json.dumps({'format':'qcow2','virtual-size':4096,**({'backing-filename':'parent.qcow2'} if case=='backing' else {})}).encode()
                return b'[State]\nImageState=IMAGE_STATE_COMPLETE\n'
            with patch.object(self.module,'run_readonly',side_effect=reply):
                with self.assertRaises(ValueError): self.inspect()
            self.assertFalse(self.manifest.exists())

    def test_existing_report_and_bad_partition_refused(self):
        self.manifest.write_text('keep')
        with self.assertRaises(FileExistsError):self.inspect()
        self.assertEqual(self.manifest.read_text(),'keep')
        self.manifest.unlink()
        with self.assertRaises(ValueError): self.module.inspect_artifact(self.image,self.manifest,'auto')

    def test_inspection_failure_not_empty_success(self):
        with patch.object(self.module,'run_readonly',side_effect=RuntimeError('inspection unavailable')):
            with self.assertRaises(RuntimeError):self.inspect()
        self.assertFalse(self.manifest.exists())

    def test_changed_image_and_malformed_state_refused(self):
        def changed(argv):
            if argv[0]=='qemu-img': return self.replies(argv)
            self.image.chmod(0o644)
            self.image.write_bytes(b'changed')
            self.image.chmod(0o444)
            return self.replies(argv)
        with patch.object(self.module,'run_readonly',side_effect=changed):
            with self.assertRaisesRegex(ValueError,'changed'): self.inspect()
        self.assertFalse(self.manifest.exists())
        def malformed(argv):
            return self.replies(argv) if argv[0]=='qemu-img' else b'ImageState=IMAGE_STATE_COMPLETE\nImageState=IMAGE_STATE_GENERALIZE_RESEAL_TO_OOBE\n'
        with patch.object(self.module,'run_readonly',side_effect=malformed):
            with self.assertRaisesRegex(ValueError,'sealed'): self.inspect()
        self.assertFalse(self.manifest.exists())

if __name__=='__main__':unittest.main()
