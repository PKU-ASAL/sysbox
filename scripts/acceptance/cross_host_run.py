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
    parser.add_argument("--provision-seconds", type=int, default=0, help="run a real foreground provisioner longer than the RPC budget")
    parser.add_argument("--background", action="store_true", help="use a finite durable guest task with the prepared p9 guest image")
    parser.add_argument("--reset", action="store_true", help="reset after Apply and verify stable resource UID and next generation before Destroy")
    parser.add_argument("--hold-after-apply", type=int, default=0, help="keep the successful Apply and running child observable before Destroy")
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
                summary = {key: result.get(key) for key in ("id", "status", "error")}
                raise RuntimeError("run failed: " + json.dumps(summary) + "; full evidence retained at " + str(evidence_path))
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
    if not 0 <= args.provision_seconds <= 600:
        parser.error("provision-seconds must be between 0 and 600")
    if not 0 <= args.hold_after_apply <= 600:
        parser.error("hold-after-apply must be between 0 and 600")
    hcl = HCL
    if args.background:
        if args.provision_seconds < 1:
            parser.error("background requires a positive provision-seconds")
        hcl = hcl.replace('source = "alpine:3.22"', 'source = "sysbox-p9-guest:local"')
        hcl = hcl.replace('provider "docker" {', 'provider "docker" {\n    privileged = true\n    cgroupns_mode = "private"')
        evidence["background"] = True
    if args.provision_seconds:
        hcl = hcl.rstrip()[:-1] + '\n  provisioner "exec" {\n    program = "sleep"\n    shell = "none"\n    args = ["' + str(args.provision_seconds) + '"]\n  }\n}\n'
        if args.background:
            hcl = hcl.replace('provisioner "exec" {', 'provisioner "exec" {\n    background = true')
        evidence["provision_seconds"] = args.provision_seconds
    revision = request("POST", "/v1/revisions", {"files": {"field.sysbox.hcl": hcl}})["revision"]
    evidence["revision"] = revision
    topology = evidence["topology"]
    evidence["phase"] = "apply"
    save()
    accepted = request("POST", f"/v1/topologies/{topology}/apply", {"revision": revision}, topology + "-apply")
    evidence["runs"].append(accepted["run_id"])
    save()
    evidence["apply_run"] = wait(accepted["run_id"])
    evidence["state_after_apply"] = request("GET", f"/v1/topologies/{topology}/state")
    if args.reset:
        evidence["phase"] = "reset"
        save()
        accepted = request("POST", f"/v1/topologies/{topology}/reset", {"revision": revision})
        evidence["runs"].append(accepted["run_id"])
        save()
        evidence["reset_run"] = wait(accepted["run_id"])
        evidence["state_after_reset"] = request("GET", f"/v1/topologies/{topology}/state")
        verify_reset_identity(evidence["state_after_apply"], evidence["state_after_reset"])
        save()
    if args.hold_after_apply:
        evidence["hold_after_apply_seconds"] = args.hold_after_apply
        evidence["phase"] = "apply_hold"
        save()
        time.sleep(args.hold_after_apply)
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


def verify_reset_identity(before, after):
    def nodes(value):
        resources = [r for r in value.get("resources", []) if r.get("resource_type") == "sysbox_node"]
        result = {r["address"]: r for r in resources}
        if len(result) != len(resources):
            raise RuntimeError("duplicate reset node address")
        return result
    if not before.get("topology_uid") or before["topology_uid"] != after.get("topology_uid"):
        raise RuntimeError("reset topology identity changed")
    old, new = nodes(before), nodes(after)
    if not old or old.keys() != new.keys():
        raise RuntimeError("reset node inventory changed or empty")
    for name, resource in old.items():
        replacement = new[name]
        if (not resource.get("resource_uid") or resource["resource_uid"] != replacement.get("resource_uid") or
                not resource.get("owner_uid") or resource["owner_uid"] != replacement.get("owner_uid") or
                replacement.get("generation") != resource.get("generation", 0) + 1 or
                not replacement.get("external_id") or replacement["external_id"] == resource.get("external_id")):
            raise RuntimeError("reset did not preserve ownership and advance external incarnation")


if __name__ == "__main__":
    os.umask(0o077)
    main()
