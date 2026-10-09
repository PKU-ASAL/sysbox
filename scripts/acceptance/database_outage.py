#!/usr/bin/env python3
"""Verify guest watchdog and original Run recovery across isolated PostgreSQL outage."""
import argparse
import json
import re
import shlex
import time

import two_vms as lab


PID_QUERY = "sudo systemctl show postgresql@16-main --property=MainPID --value"
# Match the signed-grant protocol maximum (5 minutes), plus observation slack.
STOP_BUDGET_SECONDS = 315


def running(observed):
    record = observed["record"]
    return ("populated 1" in observed["cgroup_events"] and record.get("status") == "running"
            and not record.get("cancel_requested") and not record.get("termination_confirmed"))


def cancelled(observed):
    record = observed["record"]
    return ("populated 0" in observed["cgroup_events"] and record.get("termination_confirmed")
            and record.get("cancel_requested") and record.get("status") == "cancelled")


def same_guest(before, after):
    if before["cgroup_inode"] != after["cgroup_inode"] or any(
            before["record"].get(key) != after["record"].get(key)
            for key in ("pid", "start_ticks", "supervisor_pid", "supervisor_start_ticks")):
        raise RuntimeError("guest execution incarnation changed during outage")


def during_outage(execute, probe, evidence, save):
    try:
        execute("control", "sudo systemctl stop postgresql@16-main")
        probe()
    finally:
        execute("control", "sudo systemctl start postgresql@16-main")
        execute("control", "sudo -u postgres pg_isready -h 127.0.0.1 -p 5432")
        evidence["database_restored"] = True
        save(evidence)


def inject(topology, execute, save):
    if not re.fullmatch(r"p9-[a-zA-Z0-9-]+", topology):
        raise RuntimeError("test topology required")
    query = ("sudo find /var/lib/sysbox-supervisor/journal -maxdepth 1 -name '*.json' "
             "-exec jq -c 'select(.ref.topology==\"" + topology + "\")' {} \\;")

    def record():
        rows = execute("execution", query).splitlines()
        if len(rows) != 1:
            raise RuntimeError("expected exactly one durable child")
        return json.loads(rows[0])

    before = record()
    if before["result"]["Status"] != "running" or before.get("stop_receipt") or before.get("cancel_requested"):
        raise RuntimeError("child must be running before database outage")
    handle = before["handle"]
    container = before["provider_node_id"]
    if not re.fullmatch(r"[a-f0-9]{64}", container) or not re.fullmatch(r"[a-zA-Z0-9_-]+", handle["ID"]):
        raise RuntimeError("invalid durable container/task identity")
    checkpoint_query = ("sudo find /var/lib/sysbox-supervisor/journal/runs -maxdepth 1 -name '*.json' "
                        "-exec jq -c '.outcomes[] | select(.run_checkpoint.request.topology==\"" + topology + "\")' {} \\;")
    checkpoints = execute("execution", checkpoint_query).splitlines()
    if len(checkpoints) != 1:
        raise RuntimeError("resource checkpoint required before outage")
    parent = json.loads(checkpoints[0])
    cp = parent["run_checkpoint"]
    if parent["state"] != "intent" or cp["request"]["run_id"] != before["run_id"] or len(cp["children"]) != 1 or cp["children"][0]["handle"] != handle:
        raise RuntimeError("parent checkpoint does not match live child")
    labels = json.loads(execute("execution", "sudo docker inspect --format '{{json .Config.Labels}}' " + container))
    for label, expected in (("sysbox.owner_uid", handle["TopologyUID"]), ("sysbox.resource_uid", handle["ResourceUID"]), ("sysbox.generation", str(handle["Generation"]))):
        if labels.get(label) != expected:
            raise RuntimeError("provider identity mismatch")
    prefix = "sudo docker exec " + container + " "
    group = "/sys/fs/cgroup/sysbox-tasks/sysbox-task-" + handle["ID"]
    task_path = "/var/lib/sysbox/guest-tasks/" + handle["ID"] + "/record.json"

    def guest():
        current = json.loads(execute("execution", prefix + "cat " + shlex.quote(task_path)))
        if any(current.get(field) != handle[key] for field, key in (("id", "ID"), ("digest", "Digest"), ("boot_id", "BootID"))):
            raise RuntimeError("guest task incarnation changed")
        inode = int(execute("execution", prefix + "stat -c %i " + shlex.quote(group)).strip())
        if inode != current["cgroup_inode"]:
            raise RuntimeError("guest cgroup identity changed")
        events = execute("execution", prefix + "cat " + shlex.quote(group + "/cgroup.events"))
        return {"record": current, "cgroup_events": events, "cgroup_inode": inode}

    guest_before = guest()
    if not running(guest_before):
        raise RuntimeError("actual task cgroup must be populated before outage")
    old_pid = int(execute("control", PID_QUERY).strip())
    if old_pid < 2:
        raise RuntimeError("PostgreSQL must be running before outage")
    evidence = {"topology": topology, "fault": "postgresql_cluster_outage", "before": before,
                "parent_before": parent, "guest_before": guest_before, "old_database_pid": old_pid}
    save(evidence)

    def probe():
        start = time.monotonic()
        deadline = start + STOP_BUDGET_SECONDS
        while time.monotonic() < deadline:
            if int(execute("control", PID_QUERY).strip()) != 0:
                raise RuntimeError("database unexpectedly running during fault")
            observed = guest()
            same_guest(guest_before, observed)
            evidence["guest_during"] = observed
            save(evidence)
            if cancelled(observed):
                evidence["guest_stopped_after_seconds"] = time.monotonic() - start
                return
            time.sleep(1)
        raise RuntimeError("guest cancellation not confirmed within signed-grant maximum during database outage")

    during_outage(execute, probe, evidence, save)
    new_pid = int(execute("control", PID_QUERY).strip())
    if new_pid < 2 or new_pid == old_pid:
        raise RuntimeError("database process replacement not proven")
    evidence["new_database_pid"] = new_pid
    deadline = time.monotonic() + 45
    while time.monotonic() < deadline:
        after = record()
        evidence["after"] = after
        save(evidence)
        if after.get("reported") and after.get("stop_receipt"):
            if after["handle"] != handle or after["run_id"] != before["run_id"] or after["result"]["RemoteTermination"] != "confirmed":
                raise RuntimeError("original child identity/termination not preserved")
            return evidence
        time.sleep(1)
    raise RuntimeError("original child stop not acknowledged after database restart")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", required=True)
    parser.add_argument("--topology", required=True)
    args = parser.parse_args()
    if not re.fullmatch(r"p9-[a-zA-Z0-9-]+", args.topology):
        parser.error("test topology required")
    root, manifest = lab.load(args.root)
    path = root / (args.topology + "-database-outage.json")
    if path.exists():
        raise RuntimeError("refusing to overwrite fault evidence")

    def execute(role, command):
        result = lab.ssh(root, manifest, role, command, timeout=30)
        if result.returncode:
            raise RuntimeError(f"{role}: {result.stderr}")
        return result.stdout

    evidence = inject(args.topology, execute, lambda value: lab.save(path, {"lab_id": manifest["id"], **value}))
    print(f"Guest stopped during DB outage in {evidence['guest_stopped_after_seconds']:.2f}s; original child stop acknowledged after DB restart.")
    print(f"Evidence: {path}. Parent convergence and normal Destroy still require verification.")


if __name__ == "__main__":
    main()
