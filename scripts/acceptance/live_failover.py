#!/usr/bin/env python3
"""Stop the primary API only after verifying a live guest child, then restore it."""
import argparse
import json
import re
import shlex
import time

import two_vms as lab


def inject(execute, topology, save):
    if not re.fullmatch(r"p9-[a-zA-Z0-9-]+", topology):
        raise RuntimeError("test topology required")
    query = ("sudo find /var/lib/sysbox-host/journal -maxdepth 1 -name '*.json' "
             "-exec jq -c 'select(.ref.topology==\"" + topology + "\")' {} \\;")
    def record():
        rows = execute("execution", query).splitlines()
        if len(rows) != 1:
            raise RuntimeError("one durable child required")
        return json.loads(rows[0])
    before = record()
    if before["result"]["Status"] != "running" or before.get("stop_receipt") or before.get("cancel_requested"):
        raise RuntimeError("child must be running before fault")
    container, task = before["provider_node_id"], before["handle"]["ID"]
    if not re.fullmatch(r"[a-f0-9]{64}", container) or not re.fullmatch(r"[a-zA-Z0-9_-]+", task):
        raise RuntimeError("invalid child identity")
    probe = "sudo docker exec " + container + " cat /sys/fs/cgroup/sysbox-tasks/sysbox-task-" + task + "/cgroup.events"
    populated = execute("execution", probe)
    if "populated 1" not in populated:
        raise RuntimeError("actual child must be alive before failover")
    evidence = {"topology": topology, "fault": "primary_api_stop_during_child", "before": before, "cgroup_before": populated}
    save(evidence)
    try:
        evidence["stop_started_at"] = execute("control", "date -u +%Y-%m-%dT%H:%M:%S.%NZ").strip()
        execute("control", "sudo systemctl stop sysbox-api1 && ! systemctl is-active --quiet sysbox-api1 && systemctl is-active --quiet sysbox-api2")
        evidence["primary_stopped"] = True
        save(evidence)
        deadline = time.monotonic() + 180
        while time.monotonic() < deadline:
            after = record()
            evidence["after"] = after
            save(evidence)
            if after["handle"] != before["handle"] or after["run_id"] != before["run_id"] or after["provider_node_id"] != container:
                raise RuntimeError("child identity changed across failover")
            if after.get("cancel_requested"):
                raise RuntimeError("failover cancelled the running child")
            if after.get("reported") and after.get("stop_receipt"):
                result = after["result"]
                if not result.get("ExitCodeKnown") or result.get("ExitCode") != 0 or result.get("RemoteTermination") != "confirmed":
                    raise RuntimeError("child did not finish successfully across failover")
                run_id = before["run_id"]
                code = ("import json,sys;sys.path.insert(0,'/home/p9');from cross_host_run import request_json;"
                        "token=json.load(open('/etc/sysbox-api/api2.json'))['api']['token'];"
                        f"print(json.dumps(request_json('http://10.77.0.10:19872',token,'GET','/v1/runs/'+{run_id!r})))")
                parent = json.loads(execute("control", "sudo python3 -c " + shlex.quote(code)))
                evidence["parent_on_backup"] = parent
                save(evidence)
                if parent["status"] == "done":
                    if not parent.get("host_completion"):
                        raise RuntimeError("parent lacks signed completion")
                    execute("control", "! systemctl is-active --quiet sysbox-api1 && systemctl is-active --quiet sysbox-api2")
                    evidence["primary_still_stopped_at_completion"] = True
                    save(evidence)
                    return evidence
                if parent["status"] in ("failed", "cancelled"):
                    raise RuntimeError("parent failed on backup")
            time.sleep(1)
        raise RuntimeError("same Run did not complete on backup")
    except Exception as error:
        evidence["failure"] = str(error)
        save(evidence)
        raise
    finally:
        try:
            execute("control", "sudo systemctl start sysbox-api1 && systemctl is-active --quiet sysbox-api1")
            evidence["primary_restored"] = True
        except Exception as restore_error:
            evidence["restore_error"] = str(restore_error)
            raise
        finally:
            save(evidence)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", required=True)
    parser.add_argument("--topology", required=True)
    args = parser.parse_args()
    if not re.fullmatch(r"p9-[a-zA-Z0-9-]+", args.topology):
        parser.error("test topology required")
    root, manifest = lab.load(args.root)
    path = root / (args.topology + "-live-failover.json")
    if path.exists():
        raise RuntimeError("refusing to overwrite previous fault evidence")
    def execute(role, command):
        result = lab.ssh(root, manifest, role, command, timeout=30)
        if result.returncode:
            raise RuntimeError(f"{role}: {result.stderr}")
        return result.stdout
    inject(execute, args.topology, lambda value: lab.save(path, {"lab_id": manifest["id"], **value}))
    print("Original live child and parent completed on backup; primary restored. " + str(path))


if __name__ == "__main__":
    main()
