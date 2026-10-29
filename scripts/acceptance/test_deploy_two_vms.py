import unittest
import deploy_two_vms


class DeploymentTests(unittest.TestCase):
    def test_database_is_separate_and_bound_in_dsn_and_sql(self):
        dsn, sql = deploy_two_vms.database_config("sysboxp9_reset", "test-password")
        self.assertIn("sysboxp9_reset:test-password@", dsn)
        self.assertIn("/sysboxp9_reset?", dsn)
        self.assertIn("CREATE DATABASE sysboxp9_reset OWNER sysboxp9_reset", sql)
        self.assertNotIn("DROP", sql)
        self.assertNotIn("ALTER ROLE", sql)
        self.assertIn("RAISE EXCEPTION", sql)

    def test_invalid_identifiers_rejected(self):
        for name in ("../old", "has space", "x;drop", "x'", ""):
            with self.subTest(name=name), self.assertRaises(ValueError):
                deploy_two_vms.database_config(name, "password")

    def test_release_files_reject_tampered_network_executable(self):
        import tempfile,json,hashlib
        from pathlib import Path
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            target={'os':'linux','architecture':'amd64'}
            for name in ('sysbox','sysbox-init','sysbox-netns'):
                (root/name).write_bytes(name.encode())
                target[name.replace('-','_')+'_sha256']=hashlib.sha256(name.encode()).hexdigest()
            metadata=root/'metadata.json'
            metadata.write_text(json.dumps({'targets':[target]}))
            files=deploy_two_vms.release_files(root/'sysbox',metadata)
            self.assertEqual(3,len(files))
            (root/'sysbox-netns').write_bytes(b'wrong')
            with self.assertRaises(ValueError):deploy_two_vms.release_files(root/'sysbox',metadata)
