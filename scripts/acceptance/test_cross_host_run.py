import http.client
import io
import unittest
from unittest import mock
from cross_host_run import request_json, verify_reset_identity


class FailoverRequests(unittest.TestCase):
    def test_reset_preserves_uid_and_advances_generation(self):
        old = {"resource_type": "sysbox_node", "address": "sysbox_node.node", "resource_uid": "node", "owner_uid": "owner", "external_id": "old", "generation": 1}
        new = dict(old, external_id="new", generation=2)
        verify_reset_identity({"topology_uid":"owner", "resources": [old]}, {"topology_uid":"owner", "resources": [new]})
        for change in ({"generation": 1}, {"resource_uid": "other"}, {"external_id": "old"}):
            with self.subTest(change=change), self.assertRaises(RuntimeError):
                verify_reset_identity({"topology_uid":"owner", "resources": [old]}, {"topology_uid":"owner", "resources": [dict(new, **change)]})
        with self.assertRaisesRegex(RuntimeError, "topology identity"):
            verify_reset_identity({"topology_uid":"owner", "resources":[old]}, {"topology_uid":"other", "resources":[new]})
        with self.assertRaisesRegex(RuntimeError, "duplicate"):
            verify_reset_identity({"topology_uid":"owner", "resources":[old]}, {"topology_uid":"owner", "resources":[new,new]})
    def test_reads_and_keyed_mutations_retry_identical_request(self):
        for method, key in (("GET", None), ("POST", "original-key")):
            with self.subTest(method=method), mock.patch("urllib.request.urlopen", side_effect=[http.client.RemoteDisconnected(), io.BytesIO(b'{"ok":true}')]) as call, mock.patch("time.sleep"):
                self.assertEqual({"ok": True}, request_json("http://api", "token", method, "/test", {"value": 1}, key))
                first, second = (item.args[0] for item in call.call_args_list)
                self.assertEqual(first.data, second.data)
                self.assertEqual(first.headers, second.headers)

    def test_unkeyed_mutation_is_never_replayed(self):
        with mock.patch("urllib.request.urlopen", side_effect=http.client.RemoteDisconnected()) as call:
            with self.assertRaises(http.client.RemoteDisconnected):
                request_json("http://api", "token", "POST", "/test", {})
            self.assertEqual(1, call.call_count)


if __name__ == "__main__":
    unittest.main()
