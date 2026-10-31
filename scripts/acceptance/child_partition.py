#!/usr/bin/env python3
"""Inject test API proxy outage only after observing a live durable child."""
import argparse
import json
import time
from pathlib import Path
import two_vms as lab


def execute(root, manifest, role, command):
    result = lab.ssh(root, manifest, role, command, timeout=30)
    if result.returncode:
        raise RuntimeError(f"{role}: {result.stderr}")
    return result.stdout


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", required=True)
    parser.add_argument("--topology", required=True)
    args = parser.parse_args()
    if not args.topology.startswith("p9-") or not all(c.isalnum() or c == "-" for c in args.topology):
        parser.error("a test topology identity is required")
    root, manifest = lab.load(args.root)
    path = root / (args.topology + "-partition.json")
    if path.exists():
        raise RuntimeError("refusing to overwrite fault evidence")
    query = ("sudo find /var/lib/sysbox-host/journal -maxdepth 1 -name '*.json' "
             "-exec jq -c 'select(.ref.topology==\"" + args.topology + "\")' {} \\;")
    def record():
        rows = execute(root, manifest, "execution", query).splitlines()
        if len(rows) != 1:
            raise RuntimeError("expected exactly one durable task record")
        return json.loads(rows[0])
    before = record()
    if before["result"]["Status"] != "running" or before.get("stop_receipt"):
        raise RuntimeError("child must be observed running before fault")
    evidence = {"lab_id": manifest["id"], "topology": args.topology,
                "fault": "test_api_proxy_unavailable", "before": before}
    lab.save(path, evidence)
    start = time.monotonic()
    try:
        execute(root, manifest, "control", "sudo systemctl stop haproxy")
        deadline = start + 25
        while time.monotonic() < deadline:
            current = record()
            evidence["during"] = current
            lab.save(path, evidence)
            if current.get("stop_receipt") and current["result"]["RemoteTermination"] == "confirmed":
                evidence["confirmed_after_seconds"] = time.monotonic() - start
                break
            time.sleep(1)
        else:
            raise RuntimeError("no confirmed child stop during API outage")
    finally:
        execute(root, manifest, "control", "sudo systemctl start haproxy")
        evidence["proxy_restored"] = True
        lab.save(path, evidence)
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        after = record()
        if after.get("reported"):
            if after["stop_receipt"] != evidence["during"]["stop_receipt"]:
                raise RuntimeError("stop receipt changed after reconnect")
            evidence["after"] = after
            lab.save(path, evidence)
            print(f"confirmed child stop in {evidence['confirmed_after_seconds']:.2f}s; identical receipt reported after reconnect")
            print(f"Evidence: {path}. Parent intent recovery remains a separate gate.")
            return
        time.sleep(1)
    raise RuntimeError("original child receipt not acknowledged after reconnect")


if __name__ == "__main__":
    main()
