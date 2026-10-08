import http.client
import io
import unittest
from unittest import mock
from cross_host_run import request_json


class FailoverRequests(unittest.TestCase):
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
