import json
import unittest
from unittest import mock
import matrix_guest


class GuestProbeTests(unittest.TestCase):
    def run_probe(self, terminal):
        requests = []
        def request(url, token, method, path, body=None):
            requests.append((method, path, body))
            return {'id': 'task'} if method == 'POST' else terminal
        with mock.patch.object(matrix_guest.sys, 'argv', ['matrix_guest.py', 'p9-mixed-1234567890', 'firecracker', '["true"]']), \
             mock.patch.object(matrix_guest.Path, 'read_text', return_value=json.dumps({'api': {'token': 'test'}})), \
             mock.patch.object(matrix_guest, 'request_json', side_effect=request):
            matrix_guest.main()
        return requests

    def test_success_uses_api_task_and_observed_exit(self):
        calls = self.run_probe({'status': 'completed', 'result': {'exit_code': 0}})
        self.assertEqual(calls[0][2], {'argv': ['true']})
        self.assertEqual(calls[1][1], '/v1/executions/task')

    def test_unknown_and_failed_exit_never_pass(self):
        for result in [{'status': 'unknown'}, {'status': 'failed'}, {'status': 'completed', 'result': {'exit_code': 1}}]:
            with self.subTest(result=result), self.assertRaises(RuntimeError):
                self.run_probe(result)
