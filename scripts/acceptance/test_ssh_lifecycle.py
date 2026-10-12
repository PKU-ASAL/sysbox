import unittest

import ssh_lifecycle as acceptance


class SSHLifecycleTests(unittest.TestCase):
    def test_deployment_isolated_from_historical_services(self):
        configs = acceptance.service_configs("sshgate123", 29871, "test-token", "test-password")
        for config in configs.values():
            self.assertIn("/var/lib/sysbox-p9/sshgate123/", config["paths"]["home"])
        self.assertIn("sshgate123", configs["api"]["state"]["backend"])
        self.assertIn("/sshgate123/", configs["host"]["host_supervisor"]["root"])
        self.assertEqual(configs["api"]["state"]["backend"], configs["host"]["state"]["backend"])

    def test_invalid_deployment_rejected(self):
        for name in ("../old", "bad;command", "", "old/path"):
            with self.assertRaises(ValueError):
                acceptance.service_configs(name, 29871, "token", "password")

    def test_auth_failure_is_not_network_failure(self):
        self.assertTrue(acceptance.authentication_rejected(255, "Permission denied (publickey)."))
        self.assertFalse(acceptance.authentication_rejected(255, "Connection refused"))
        self.assertFalse(acceptance.authentication_rejected(255, "Host key verification failed."))
        self.assertFalse(acceptance.authentication_rejected(0, "Permission denied"))

    def test_docker_absence_requires_exact_identity_and_definitive_error(self):
        for message in ("Error: No such object: abc", "error: no such object: abc"):
            self.assertTrue(acceptance.container_absent(1, "[]\n", message, "abc"))
        self.assertFalse(acceptance.container_absent(1, "", "Cannot connect to Docker", "abc"))
        self.assertFalse(acceptance.container_absent(1, "[]", "no such object: foreign", "abc"))
        self.assertFalse(acceptance.container_absent(0, "[{}]", "", "abc"))


if __name__ == "__main__":
    unittest.main()
