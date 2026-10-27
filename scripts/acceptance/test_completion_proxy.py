import http.client
import http.server
import json
from pathlib import Path
import tempfile
import threading
import unittest

from completion_proxy import CompletionProxy


class ProxyTests(unittest.TestCase):
    def test_committed_receipt_withheld_until_restart_and_exact_replay(self):
        class Backend(http.server.BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_POST(self):
                self.rfile.read(int(self.headers['Content-Length']))
                self.send_response(200)
                self.send_header('Transfer-Encoding', 'chunked')
                self.end_headers()
                self.wfile.write(b'2\r\n{}\r\n0\r\n\r\n')

        backend = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Backend)
        threading.Thread(target=backend.serve_forever, daemon=True).start()
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            proxy = CompletionProxy(('127.0.0.1', 0), backend.server_address, root)
            threading.Thread(target=proxy.serve_forever, daemon=True).start()
            def post(path, body=b'original'):
                client = http.client.HTTPConnection(*proxy.server_address)
                try:
                    client.request('POST', path, body)
                    response = client.getresponse()
                    return response.status, response.read()
                finally:
                    client.close()
            try:
                path = '/v1/hosts/agent/runs/run/completed'
                (root / 'armed').touch()
                self.assertEqual(post('/v1/hosts/agent/authorize'), (200, b'{}'))
                for _ in range(2):
                    with self.assertRaises(http.client.RemoteDisconnected):
                        post(path)
                proof = json.loads((root / 'dropped.json').read_text())
                proxy.shutdown(); proxy.server_close()
                proxy = CompletionProxy(('127.0.0.1', 0), backend.server_address, root)
                threading.Thread(target=proxy.serve_forever, daemon=True).start()
                (root / 'armed').unlink()
                self.assertEqual(post(path, b'changed')[0], 409)
                self.assertFalse((root / 'recovered.json').exists())
                self.assertEqual(post(path), (200, b'{}'))
                self.assertEqual(json.loads((root / 'recovered.json').read_text()), proof)
            finally:
                proxy.shutdown(); proxy.server_close()
                backend.shutdown(); backend.server_close()
