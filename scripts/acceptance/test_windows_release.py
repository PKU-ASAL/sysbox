import hashlib
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
import windows_tasks

class WindowsReleaseTests(unittest.TestCase):
    def test_acceptance_uses_pinned_helper_and_rejects_tampering(self):
        with tempfile.TemporaryDirectory() as root:
            root=Path(root);out=root/'out';out.mkdir()
            data={'sysbox-windows-task.exe':b'released-helper','install.ps1':b'installer','build-metadata.json':json.dumps({'commit':'a'*40,'version':'v1.0.0'}).encode()}
            archive=root/'windows.tar.gz'
            with tarfile.open(archive,'w:gz') as tar:
                for name,raw in data.items():
                    m=tarfile.TarInfo(name);m.size=len(raw);tar.addfile(m,io.BytesIO(raw))
            target={'os':'windows','architecture':'amd64','archive':archive.name,'sha256':hashlib.sha256(archive.read_bytes()).hexdigest(),'helper_sha256':hashlib.sha256(data['sysbox-windows-task.exe']).hexdigest(),'installer_sha256':hashlib.sha256(data['install.ps1']).hexdigest()}
            metadata=root/'build-metadata.json';metadata.write_text(json.dumps({'commit':'a'*40,'tag':'v1.0.0','targets':[target]}))
            self.assertEqual(windows_tasks.release_helper(metadata,out),'a'*40)
            self.assertEqual((out/'sysbox-windows-task.exe').read_bytes(),data['sysbox-windows-task.exe'])
            archive.write_bytes(b'changed')
            with self.assertRaises(ValueError):windows_tasks.release_helper(metadata,out)

    def test_linux_acceptance_ignores_windows_target(self):
        import deploy_two_vms
        import mixed_lifecycle
        with tempfile.TemporaryDirectory() as root:
            root=Path(root)
            target={'os':'linux','architecture':'amd64'}
            for name in ('sysbox','sysbox-init','sysbox-netns'):
                (root/name).write_bytes(name.encode())
                target[name.replace('-','_')+'_sha256']=hashlib.sha256(name.encode()).hexdigest()
            metadata=root/'metadata.json'
            metadata.write_text(json.dumps({'commit':'a'*40,'targets':[target,{'os':'windows','architecture':'amd64'}]}))
            self.assertEqual(len(deploy_two_vms.release_files(root/'sysbox',metadata)),3)
            self.assertEqual(mixed_lifecycle.release_inputs(metadata,root/'sysbox',root/'sysbox-init')[2]['commit'],'a'*40)
