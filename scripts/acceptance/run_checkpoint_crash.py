#!/usr/bin/env python3
"""Kill the isolated execution VM's supervisor only after a live Run checkpoint."""
import argparse
import json
import time

import two_vms as lab


def inject(topology, execute, save):
    if not topology.startswith("p9-") or not all(c.isalnum() or c == "-" for c in topology):
        raise RuntimeError("test topology required")
    query = ("sudo find /var/lib/sysbox-host/journal/runs -maxdepth 1 -name '*.json' "
             "-exec jq -c '.outcomes[] | select(.run_checkpoint.request.topology==\"" + topology + "\")' {} \\;")

    def record():
        rows = execute(query).splitlines()
        if len(rows) != 1:
            raise RuntimeError("expected exactly one checkpointed Run")
        return json.loads(rows[0])

    before = record()
    checkpoint = before["run_checkpoint"]
    if before["state"] != "intent" or checkpoint["version"] != 1 or not checkpoint["children"]:
        raise RuntimeError("checkpointed intent with children required")
    task_query = ("sudo find /var/lib/sysbox-host/journal -maxdepth 1 -name '*.json' "
                  "-exec jq -c 'select(.ref.topology==\"" + topology + "\")' {} \\;")
    tasks = [json.loads(row) for row in execute(task_query).splitlines()]
    original_tasks = {task["handle"]["ID"]: task for task in tasks}
    expected = {child["handle"]["ID"] for child in checkpoint["children"]}
    if {task["handle"]["ID"] for task in tasks} != expected or not tasks or any(
            task.get("stop_receipt") or task["result"]["Status"] != "running" for task in tasks):
        raise RuntimeError("all checkpoint children must be observed running")
    pid_query = "sudo systemctl show sysbox-host --property=MainPID --value"
    old_pid = int(execute(pid_query).strip())
    if old_pid < 2:
        raise RuntimeError("supervisor is not running")
    evidence = {"topology": topology, "fault": "supervisor_sigkill", "before": before,
                "children_before": tasks, "old_pid": old_pid}
    save(evidence)
    start = time.monotonic()
    execute("sudo systemctl kill --signal=SIGKILL --kill-who=main sysbox-host")
    deadline = start + 45
    while time.monotonic() < deadline:
        current = record()
        evidence["after"] = current
        save(evidence)
        if current["state"] == "complete" and current.get("reported"):
            result = current["result"]
            if result["status"] != "failed" or result.get("error") != "run controller stopped after resource execution; child termination confirmed":
                raise RuntimeError("unexpected recovery result")
            tasks = [json.loads(row) for row in execute(task_query).splitlines()]
            if {task["handle"]["ID"] for task in tasks} != expected or any(
                    not task.get("stop_receipt") or not task.get("reported") for task in tasks):
                raise RuntimeError("missing acknowledged child stop evidence")
            for task in tasks:
                original = original_tasks[task["handle"]["ID"]]
                if any(task.get(field) != original.get(field) for field in ("handle", "run_id", "provider_node_id")):
                    raise RuntimeError("child incarnation changed after supervisor crash")
                if task.get("result", {}).get("RemoteTermination") != "confirmed":
                    raise RuntimeError("child termination not confirmed")
            new_pid = int(execute(pid_query).strip())
            if new_pid < 2 or new_pid == old_pid:
                raise RuntimeError("replacement supervisor not proven")
            evidence.update(new_pid=new_pid, children_after=tasks, recovered_after_seconds=time.monotonic()-start)
            save(evidence)
            return evidence
        time.sleep(1)
    raise RuntimeError("checkpoint Run did not converge; evidence retained")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", required=True)
    parser.add_argument("--topology", required=True)
    args = parser.parse_args()
    root, manifest = lab.load(args.root)
    if not args.topology.startswith("p9-") or not all(c.isalnum() or c == "-" for c in args.topology):
        parser.error("test topology required")
    path = root / (args.topology + "-supervisor-crash.json")
    if path.exists():
        raise RuntimeError("refusing to overwrite crash evidence")

    def execute(command):
        result = lab.ssh(root, manifest, "execution", command, timeout=30)
        if result.returncode:
            raise RuntimeError(result.stderr)
        return result.stdout

    evidence = inject(args.topology, execute, lambda value: lab.save(path, {"lab_id": manifest["id"], **value}))
    print(f"Supervisor {evidence['old_pid']} -> {evidence['new_pid']}; failed Run reported in {evidence['recovered_after_seconds']:.2f}s")
    print(f"Evidence: {path}. Normal Destroy and independent absence verification remain required.")


if __name__ == "__main__":
    main()
