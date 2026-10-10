#!/usr/bin/env python3
"""Record terminal host proof and normally destroy one retained acceptance topology."""
import argparse
import json
import os
import re
from pathlib import Path
import time

from cross_host_run import request_json


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--evidence", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    output = Path(args.output)
    if output.exists():
        raise RuntimeError("preserve existing cleanup evidence; inspect its original Run instead")
    source = json.loads(Path(args.evidence).read_text())
    token = json.loads(Path("/etc/sysbox-api/api1.json").read_text())["api"]["token"]
    topology = source["topology"]
    if not re.fullmatch(r"p9-[a-zA-Z0-9-]+", topology) or not source["runs"]:
        raise RuntimeError("acceptance topology and original Run required")
    def request(method, path, body=None, key=None):
        return request_json("http://10.77.0.10:19870", token, method, path, body, key)
    original = request("GET", "/v1/runs/" + source["runs"][0])
    if original.get("topology") != topology or original.get("status") not in ("failed", "cancelled") or not original.get("host_completion"):
        raise RuntimeError("canonical terminal host evidence required before cleanup")
    evidence = {"topology": topology, "original_run": original,
                "state_before_destroy": request("GET", f"/v1/topologies/{topology}/state")}
    def save():
        temp = output.with_suffix(".tmp")
        with temp.open("w") as stream:
            json.dump(evidence, stream, indent=2)
            stream.flush()
            os.fsync(stream.fileno())
        temp.replace(output)
    save()
    accepted = request("POST", f"/v1/topologies/{topology}/destroy", {}, topology + "-fault-cleanup")
    evidence["destroy_run_id"] = accepted["run_id"]
    save()
    deadline = time.monotonic() + 180
    while time.monotonic() < deadline:
        result = request("GET", "/v1/runs/" + evidence["destroy_run_id"])
        evidence["destroy_run"] = result
        save()
        if result["status"] == "done":
            evidence["state_after_destroy"] = request("GET", f"/v1/topologies/{topology}/state")
            evidence["phase"] = "lifecycle_finished_provider_verification_required"
            save()
            print("Destroy complete; independent provider absence verification required.")
            return
        if result["status"] in ("failed", "cancelled"):
            raise RuntimeError("cleanup failed; retained original Run and resources")
        time.sleep(1)
    raise RuntimeError("cleanup observation timed out; inspect original destroy Run")


if __name__ == "__main__":
    os.umask(0o077)
    main()
