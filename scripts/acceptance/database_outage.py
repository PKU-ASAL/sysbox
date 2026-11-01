#!/usr/bin/env python3
"""Verify guest watchdog and original Run recovery across database or network outage."""
import argparse
import json
import re
import shlex
import time

import two_vms as lab


PID_QUERY = "sudo systemctl show postgresql@16-main --property=MainPID --value"
# Match the signed-grant protocol maximum (5 minutes), plus observation slack.
STOP_BUDGET_SECONDS = 315


def require_connectivity(execute, reachable):
    script = '''import json, socket
result = {}
for port in (19870, 5432):
    try:
        with socket.create_connection(("10.77.0.10", port), timeout=1):
            result[str(port)] = True
    except OSError:
        result[str(port)] = False
print(json.dumps(result))
'''
    observed = json.loads(execute("execution", "python3 -c " + shlex.quote(script)))
    if observed != {"19870": reachable, "5432": reachable}:
        raise RuntimeError("unexpected API/database connectivity: " + json.dumps(observed))
    return observed


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


def verify_stop_report(before, after):
    if any(after.get(key) != before[key] for key in ("handle", "run_id", "provider_node_id")) or not after.get("reported") or after.get("result", {}).get("RemoteTermination") != "confirmed":
        raise RuntimeError("original child identity/termination not preserved")
    receipt = after.get("stop_receipt") or {}
    original = before["grant"]["grant"]
    grant = receipt.get("grant", {})
    # Renewal may change expiry and state; execution identity must stay fixed.
    identity = ("scope", "target_boot_id", "agent_id", "session_id", "action_id", "command_id",
                "attempt", "epoch", "parent_epoch", "operation", "digest")
    handle = before["handle"]
    if (any(key not in original or grant.get(key) != original[key] for key in identity)
            or receipt.get("task_id") != handle["ID"] or receipt.get("task_digest") != handle["Digest"]
            or receipt.get("guest_boot_id") != handle["BootID"]
            or receipt.get("quiescence") != "confirmed" or not receipt.get("signature")):
        raise RuntimeError("stop receipt does not match original child")


def during_outage(execute, probe, evidence, save, fault="database"):
    if fault not in ("database", "private-network"):
        raise RuntimeError("unsupported fault")
    failure = None
    try:
        if fault == "database":
            execute("control", "sudo systemctl stop postgresql@16-main")
        else:
            execute("execution", "sudo ip link set dev lan0 down")
        probe()
    except BaseException as error:
        failure = error
        evidence["fault_error"] = str(error)
        raise
    finally:
        try:
            if fault == "database":
                execute("control", "sudo systemctl start postgresql@16-main")
                execute("control", "sudo -u postgres pg_isready -h 127.0.0.1 -p 5432")
                evidence["database_restored"] = True
            else:
                execute("execution", "sudo ip link set dev lan0 up")
                # Bringing the link up does not synchronously restore its route/address.
                for attempt in range(10):
                    try:
                        execute("execution", "ping -c 1 -W 1 10.77.0.10")
                        break
                    except RuntimeError as error:
                        evidence.setdefault("network_readiness_errors", []).append(str(error))
                        if attempt == 9:
                            raise
                        time.sleep(1)
                evidence["network_restored"] = True
        except Exception as error:
            evidence["restoration_error"] = str(error)
            if failure is None:
                raise
        finally:
            save(evidence)


def inject(topology, execute, save, fault="database"):
    if fault not in ("database", "private-network"):
        raise RuntimeError("unsupported fault")
    if not re.fullmatch(r"p9-[a-zA-Z0-9-]+", topology):
        raise RuntimeError("test topology required")
    query = ("sudo find /var/lib/sysbox-host/journal -maxdepth 1 -name '*.json' "
             "-exec jq -c 'select(.ref.topology==\"" + topology + "\")' {} \\;")

    def record():
        rows = execute("execution", query).splitlines()
        if len(rows) != 1:
            raise RuntimeError("expected exactly one durable child")
        return json.loads(rows[0])

    before = record()
    if before["result"]["Status"] != "running" or before.get("stop_receipt") or before.get("cancel_requested"):
        raise RuntimeError("child must be running before outage")
    handle = before["handle"]
    container = before["provider_node_id"]
    if not re.fullmatch(r"[a-f0-9]{64}", container) or not re.fullmatch(r"[a-zA-Z0-9_-]+", handle["ID"]):
        raise RuntimeError("invalid durable container/task identity")
    checkpoint_query = ("sudo find /var/lib/sysbox-host/journal/runs -maxdepth 1 -name '*.json' "
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
    evidence = {"topology": topology, "fault": "postgresql_cluster_outage" if fault == "database" else "private_network_partition", "before": before,
                "parent_before": parent, "guest_before": guest_before, "old_database_pid": old_pid}
    if fault == "private-network":
        links = json.loads(execute("execution", "ip -j address show dev lan0"))
        management = json.loads(execute("execution", "ip -j route get 10.0.2.2"))
        if len(links) != 1 or "UP" not in links[0]["flags"] or not any(a.get("local") == "10.77.0.20" for a in links[0]["addr_info"]) or not management or management[0].get("dev") != "nat0":
            raise RuntimeError("separate private lan0 and NAT management channel required")
        evidence["private_link_before"], evidence["management_route"] = links, management
        evidence["connectivity_before"] = require_connectivity(execute, True)
    save(evidence)

    def probe():
        start = time.monotonic()
        deadline = start + STOP_BUDGET_SECONDS
        while time.monotonic() < deadline:
            current_pid = int(execute("control", PID_QUERY).strip())
            if fault == "database":
                if current_pid != 0:
                    raise RuntimeError("database unexpectedly running during fault")
            else:
                links = json.loads(execute("execution", "ip -j link show dev lan0"))
                if current_pid != old_pid or "UP" in links[0]["flags"]:
                    raise RuntimeError("partition must keep database running and lan0 down")
                evidence["private_link_during"] = links
                evidence["connectivity_during"] = require_connectivity(execute, False)
            observed = guest()
            same_guest(guest_before, observed)
            evidence["guest_during"] = observed
            save(evidence)
            if cancelled(observed):
                evidence["guest_stopped_after_seconds"] = time.monotonic() - start
                return
            time.sleep(1)
        raise RuntimeError("guest cancellation not confirmed within signed-grant maximum during " + fault + " outage")

    during_outage(execute, probe, evidence, save, fault)
    if fault == "private-network":
        evidence["connectivity_after"] = require_connectivity(execute, True)
        links = json.loads(execute("execution", "ip -j address show dev lan0"))
        evidence["private_link_after"] = links
        save(evidence)
        if len(links) != 1 or "UP" not in links[0]["flags"] or not any(a.get("local") == "10.77.0.20" for a in links[0]["addr_info"]):
            raise RuntimeError("private network address not restored")
    new_pid = int(execute("control", PID_QUERY).strip())
    if new_pid < 2 or (fault == "database" and new_pid == old_pid) or (fault == "private-network" and new_pid != old_pid):
        raise RuntimeError("unexpected database process identity after " + fault + " outage")
    evidence["new_database_pid"] = new_pid
    deadline = time.monotonic() + 45
    while time.monotonic() < deadline:
        after = record()
        evidence["after"] = after
        save(evidence)
        if after.get("reported") and after.get("stop_receipt"):
            verify_stop_report(before, after)
            return evidence
        time.sleep(1)
    raise RuntimeError("original child stop not acknowledged after " + fault + " restoration")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", required=True)
    parser.add_argument("--topology", required=True)
    parser.add_argument("--fault", choices=("database", "private-network"), default="database")
    args = parser.parse_args()
    if not re.fullmatch(r"p9-[a-zA-Z0-9-]+", args.topology):
        parser.error("test topology required")
    root, manifest = lab.load(args.root)
    path = root / (args.topology + "-" + args.fault + "-outage.json")
    if path.exists():
        raise RuntimeError("refusing to overwrite fault evidence")

    def execute(role, command):
        result = lab.ssh(root, manifest, role, command, timeout=30)
        if result.returncode:
            raise RuntimeError(f"{role}: exit {result.returncode}: {result.stderr or result.stdout}")
        return result.stdout

    last_evidence = {}

    def save(value):
        last_evidence.clear()
        last_evidence.update(value)
        lab.save(path, {"lab_id": manifest["id"], **value})

    try:
        evidence = inject(args.topology, execute, save, args.fault)
    except BaseException as error:
        # This includes post-restoration validation/reporting, not just the fault.
        if last_evidence:
            lab.save(path, {"lab_id": manifest["id"], **last_evidence, "acceptance_error": str(error)})
        raise
    print(f"Guest stopped during {args.fault} outage in {evidence['guest_stopped_after_seconds']:.2f}s; original child stop acknowledged after restoration.")
    print(f"Evidence: {path}. Parent convergence and normal Destroy still require verification.")


if __name__ == "__main__":
    main()
