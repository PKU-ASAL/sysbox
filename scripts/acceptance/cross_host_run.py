#!/usr/bin/env python3
"""Run on the control VM. Records evidence before each remote lifecycle phase."""
import argparse
import json
import http.client
import os
import subprocess
from pathlib import Path
import time
import urllib.error
import urllib.request
import uuid

HCL = '''resource "sysbox_image" "base" {
  substrate = "docker"
  source = "alpine:3.22"
  kind = "oci"
  architecture = "amd64"
  guest_family = "linux"
}
resource "sysbox_node" "node" {
  substrate = "docker"
  image = sysbox_image.base.id
  provider "docker" {
    entrypoint = ["sleep"]
    command = ["infinity"]
  }
}
'''


def request_json(url, token, method, path, payload=None, key=None):
    raw = None if payload is None else json.dumps(payload).encode()
    headers = {"Authorization": "Bearer " + token, "Content-Type": "application/json", "X-Sysbox-Roles": "admin"}
    if key:
        headers["Idempotency-Key"] = key
    attempts = 10 if method == "GET" or key else 1
    for attempt in range(attempts):
        req = urllib.request.Request(url + path, data=raw, headers=headers, method=method)
        try:
            with urllib.request.urlopen(req, timeout=20) as response:
                return json.load(response)
        except urllib.error.HTTPError as err:
            if err.code not in (502, 503, 504) or attempt + 1 == attempts:
                raise RuntimeError(f"{method} {path}: HTTP {err.code}: {err.read().decode()}") from None
        except (urllib.error.URLError, http.client.RemoteDisconnected, TimeoutError):
            if attempt + 1 == attempts:
                raise
        time.sleep(1)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", default="/etc/sysbox-api/api1.json")
    parser.add_argument("--url", default="http://10.77.0.10:19870")
    parser.add_argument("--evidence", required=True)
    parser.add_argument("--finalize", action="store_true", help="delete metadata after independent provider absence verification")
    parser.add_argument("--stop-primary", action="store_true", help="control VM only: stop the test primary API before acceptance")
    args = parser.parse_args()
    token = json.loads(Path(args.config).read_text())["api"]["token"]
    evidence_path = Path(args.evidence)
    if evidence_path.exists() and not args.finalize:
        raise RuntimeError("refusing to overwrite previous acceptance evidence")
    evidence = json.loads(evidence_path.read_text()) if args.finalize else {"topology": "p9-" + uuid.uuid4().hex[:12], "phase": "initial", "runs": []}
    def save():
        temp = evidence_path.with_suffix(".tmp")
        with temp.open("w") as output:
            json.dump(evidence, output, indent=2)
            output.flush()
            os.fsync(output.fileno())
        temp.replace(evidence_path)
    def request(method, path, payload=None, key=None):
        return request_json(args.url, token, method, path, payload, key)
    def wait(run_id):
        deadline = time.monotonic() + 180
        previous_status = None
        while time.monotonic() < deadline:
            result = request("GET", "/v1/runs/" + run_id)
            evidence["latest_run"] = result
            save()
            if result.get("status") == "done":
                return result
            if result.get("status") in ("failed", "cancelled"):
                raise RuntimeError("run failed: " + json.dumps(result))
            if result.get("status") != previous_status:
                print(f"{run_id}: {result.get('status')}", flush=True)
                previous_status = result.get("status")
            time.sleep(2)
        raise RuntimeError("run did not finish; original evidence retained")
    if args.finalize:
        if evidence.get("phase") != "lifecycle_finished_provider_verification_required" or evidence.get("state_after_destroy", {}).get("resources") != []:
            raise RuntimeError("successful empty-state destruction evidence required")
        evidence["metadata_delete"] = request("DELETE", "/v1/topologies/" + evidence["topology"])
        evidence["phase"] = "metadata_deleted"
        save()
        print("metadata deleted without force")
        return
    save()
    if args.stop_primary:
        start = time.monotonic()
        subprocess.run(["systemctl", "stop", "sysbox-api1"], check=True, timeout=15)
        evidence["primary_stop_seconds"] = time.monotonic() - start
        evidence["fault"] = "primary_api_stopped"
        save()
    agents = request("GET", "/v1/agents")["agents"]
    print("agents:", json.dumps([{key: agent.get(key) for key in ("id", "status", "capabilities")} for agent in agents]), flush=True)
    revision = request("POST", "/v1/revisions", {"files": {"field.sysbox.hcl": HCL}})["revision"]
    evidence["revision"] = revision
    topology = evidence["topology"]
    evidence["phase"] = "apply"
    save()
    accepted = request("POST", f"/v1/topologies/{topology}/apply", {"revision": revision}, topology + "-apply")
    evidence["runs"].append(accepted["run_id"])
    save()
    wait(accepted["run_id"])
    evidence["state_after_apply"] = request("GET", f"/v1/topologies/{topology}/state")
    evidence["phase"] = "destroy"
    save()
    accepted = request("POST", f"/v1/topologies/{topology}/destroy", {}, topology + "-destroy")
    evidence["runs"].append(accepted["run_id"])
    save()
    wait(accepted["run_id"])
    evidence["state_after_destroy"] = request("GET", f"/v1/topologies/{topology}/state")
    evidence["phase"] = "lifecycle_finished_provider_verification_required"
    save()
    print("API apply/destroy finished; verify provider absence against recorded identity.")


if __name__ == "__main__":
    os.umask(0o077)
    main()
