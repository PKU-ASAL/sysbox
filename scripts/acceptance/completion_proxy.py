#!/usr/bin/env python3
"""Test-only host proxy: withhold one committed Run receipt until released."""
import argparse
import hashlib
import http.client
import http.server
import json
import os
from pathlib import Path
import threading


class CompletionProxy(http.server.ThreadingHTTPServer):
    def __init__(self, address, backend, root):
        self.backend, self.root = backend, Path(root)
        self.fault_lock = threading.Lock()
        super().__init__(address, Handler)


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_POST(self):
        self.forward()

    def do_GET(self):
        self.forward()

    def forward(self):
        body = self.rfile.read(int(self.headers.get('Content-Length', '0')))
        headers = {k: v for k, v in self.headers.items() if k.lower() not in ('host', 'connection', 'transfer-encoding')}
        upstream = http.client.HTTPConnection(*self.server.backend, timeout=30)
        try:
            upstream.request(self.command, self.path, body, headers)
            response = upstream.getresponse()
            data = response.read()
            with self.server.fault_lock:
                root = self.server.root
                marker, proof = root / 'armed', root / 'dropped.json'
                eligible = self.command == 'POST' and self.path.startswith('/v1/hosts/') and '/runs/' in self.path and self.path.endswith('/completed') and response.status == 200
                if eligible and marker.exists():
                    digest = hashlib.sha256(body).hexdigest()
                    if not proof.exists():
                        temp = root / 'dropped.tmp'
                        with temp.open('w') as out:
                            json.dump({'path': self.path, 'request_sha256': digest, 'backend_status': response.status}, out)
                            out.flush()
                            os.fsync(out.fileno())
                        temp.replace(proof)
                    original = json.loads(proof.read_text())
                    if original['path'] == self.path:
                        if original['request_sha256'] != digest:
                            self.send_error(409, 'completion changed')
                            return
                        self.close_connection = True
                        return
                if eligible and proof.exists() and not marker.exists():
                    original = json.loads(proof.read_text())
                    if original['path'] == self.path:
                        if original['request_sha256'] != hashlib.sha256(body).hexdigest():
                            self.send_error(409, 'completion changed')
                            return
                        (root / 'recovered.json').write_text(json.dumps(original))
            self.send_response(response.status)
            self.send_header('Content-Type', response.getheader('Content-Type', 'application/json'))
            self.send_header('Content-Length', str(len(data)))
            self.end_headers()
            self.wfile.write(data)
        finally:
            upstream.close()


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--listen-port', type=int, required=True)
    parser.add_argument('--backend-port', type=int, required=True)
    parser.add_argument('--root', required=True)
    args = parser.parse_args()
    CompletionProxy(('10.77.0.10', args.listen_port), ('10.77.0.10', args.backend_port), args.root).serve_forever()
