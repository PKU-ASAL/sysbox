#!/usr/bin/env python3
"""Request normal Destroy only after a test Run has a confirmed failure outcome."""
import argparse
import json
from pathlib import Path
import time
from cross_host_run import request_json


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--evidence", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--finalize", action="store_true")
    args = parser.parse_args()
    old = json.loads(Path(args.evidence).read_text())
    output = Path(args.output)
    if output.exists() and not args.finalize:
        raise RuntimeError("retain original cleanup evidence; do not overwrite")
    token = json.loads(Path("/etc/sysbox-api/api1.json").read_text())["api"]["token"]
    def request(method, path, payload=None, key=None):
        return request_json("http://10.77.0.10:19870", token, method, path, payload, key)
    run = request("GET", "/v1/runs/" + old["runs"][0])
    if run.get("status") != "failed" or run.get("error") != "run child execution failed after confirmed termination":
        raise RuntimeError("confirmed failed Run required; unknown intent cannot be bypassed")
    topology = old["topology"]
    if args.finalize:
        evidence = json.loads(output.read_text())
        if evidence.get("topology") != topology or evidence.get("destroy", {}).get("status") != "done" or not evidence["destroy"].get("cleanup", {}).get("empty"):
            raise RuntimeError("matching successful cleanup evidence required")
        evidence["metadata_delete"] = request("DELETE", f"/v1/topologies/{topology}")
        output.write_text(json.dumps(evidence, indent=2))
        print("metadata deleted without force; independent provider check is required before this step")
        return
    evidence = {"topology": topology, "failed_run": run, "state_before_destroy": request("GET", f"/v1/topologies/{topology}/state")}
    output.write_text(json.dumps(evidence, indent=2))
    accepted = request("POST", f"/v1/topologies/{topology}/destroy", {}, topology + "-recovered-destroy")
    evidence["destroy_run_id"] = accepted["run_id"]
    output.write_text(json.dumps(evidence, indent=2))
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        result = request("GET", "/v1/runs/" + accepted["run_id"])
        evidence["destroy"] = result
        output.write_text(json.dumps(evidence, indent=2))
        if result.get("status") == "done":
            if not result.get("cleanup", {}).get("empty"):
                raise RuntimeError("destroy did not produce an empty-state receipt")
            print(json.dumps({"topology": topology, "owner": evidence["state_before_destroy"]["topology_uid"], "destroy": "done"}))
            return
        if result.get("status") in ("failed", "cancelled"):
            raise RuntimeError("destroy failed; evidence retained")
        time.sleep(1)
    raise RuntimeError("destroy timeout; original operation retained")


if __name__ == "__main__":
    main()
