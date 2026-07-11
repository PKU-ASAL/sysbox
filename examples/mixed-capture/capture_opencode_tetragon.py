#!/usr/bin/env python3
"""Capture a minimal mixed-topology episode with opencode + Tetragon.

This script is intentionally outside sysbox core. It is a research capture
utility for producing the first real data slice:

  mixed topology + opencode actor + deterministic curl prompt + Tetragon raw log

Output:
  .sysbox/runs/mixed/episodes/<episode>/
    meta.json
    episode_context.json
    agent_actions.jsonl
    transcript.json
    tetragon.raw.jsonl

The Tetragon command is configurable because local installations differ.
Set TETRAGON_CAPTURE_CMD with "{output}" as the output placeholder, e.g.:

  TETRAGON_CAPTURE_CMD='tetragon --export-filename {output}'
"""

from __future__ import annotations

import argparse
import json
import os
import shlex
import signal
import subprocess
import sys
import time
import uuid
from pathlib import Path
from typing import Any, Iterable


REPO_ROOT = Path(__file__).resolve().parent.parent.parent
sys.path.insert(0, str(REPO_ROOT))

from runner.agent import OpenCodeClient, ToolCallEvent  # noqa: E402


DEFAULT_TOPOLOGY = "mixed"
DEFAULT_STATE = REPO_ROOT / ".sysbox" / "runs" / DEFAULT_TOPOLOGY / "state.json"
DEFAULT_PROMPT = "Run exactly this command and report the output: curl -sS http://10.0.2.10/ | head"
DEFAULT_ACTOR = "red"


def load_dotenv() -> None:
    env_file = REPO_ROOT / ".env"
    if not env_file.exists():
        return
    for line in env_file.read_text().splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, _, val = line.partition("=")
        key = key.strip()
        val = val.strip().strip('"').strip("'")
        if key and key not in os.environ:
            os.environ[key] = val


def read_json(path: Path) -> dict[str, Any]:
    return json.loads(path.read_text())


def write_json(path: Path, data: dict[str, Any] | list[Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, indent=2, sort_keys=True) + "\n")


def write_jsonl(path: Path, records: Iterable[dict[str, Any]]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("a", encoding="utf-8") as f:
        for record in records:
            f.write(json.dumps(record, sort_keys=True) + "\n")


def resource_id(resource: dict[str, Any]) -> str:
    return f"{resource.get('type')}.{resource.get('name')}"


def iter_resources(state: dict[str, Any], typ: str) -> Iterable[dict[str, Any]]:
    for resource in state.get("resources", []):
        if resource.get("type") == typ:
            yield resource


def strip_cidr(ip: str) -> str:
    return ip.split("/", 1)[0]


def collect_ips(instance: dict[str, Any]) -> list[str]:
    ips: list[str] = []
    for key in ("primary_ip", "ip", "acp_ip"):
        val = instance.get(key)
        if isinstance(val, str) and val:
            ips.append(strip_cidr(val))
    for link in instance.get("links", []) or []:
        if isinstance(link, dict):
            ip = link.get("ip")
            if isinstance(ip, str) and ip:
                ips.append(strip_cidr(ip))
    deduped: list[str] = []
    for ip in ips:
        if ip not in deduped:
            deduped.append(ip)
    return deduped


def find_resource(state: dict[str, Any], typ: str, name: str) -> dict[str, Any] | None:
    for resource in iter_resources(state, typ):
        if resource.get("name") == name:
            return resource
    return None


def normalize_node_ref(node_name: str) -> str:
    if node_name.startswith("sysbox_node."):
        return node_name
    return f"sysbox_node.{node_name}"


def build_episode_context(
    *,
    episode_id: str,
    topology: str,
    state_path: Path,
    state: dict[str, Any],
    actor_name: str,
    prompt: str,
) -> dict[str, Any]:
    actor = find_resource(state, "sysbox_actor", actor_name)
    actor_inst = actor.get("instance", {}) if actor else {}
    actor_node = normalize_node_ref(str(actor_inst.get("node") or ""))

    nodes: dict[str, Any] = {}
    for node in iter_resources(state, "sysbox_node"):
        inst = node.get("instance", {}) or {}
        rid = resource_id(node)
        nodes[rid] = {
            "substrate": node.get("provider", ""),
            "ips": collect_ips(inst),
            "raw_name": node.get("name", ""),
        }

    actor_ips = list(nodes.get(actor_node, {}).get("ips", []))
    for ip in collect_ips(actor_inst):
        if ip not in actor_ips:
            actor_ips.append(ip)

    return {
        "schema": "sysfield.episode.v1",
        "episode_id": episode_id,
        "topology": topology,
        "state_path": str(state_path),
        "prompt": prompt,
        "created_unix_ms": int(time.time() * 1000),
        "actor": {
            "id": f"sysbox_actor.{actor_name}",
            "agent": "opencode",
            "node": actor_node,
            "acp_url": actor_inst.get("acp_url", ""),
            "port": actor_inst.get("port"),
            "ips": actor_ips,
        },
        "nodes": nodes,
    }


def normalize_tool_call_event(event: ToolCallEvent, *, seq: int, context: dict[str, Any]) -> dict[str, Any]:
    actor = context.get("actor", {})
    command = event.command or ""
    try:
        argv = shlex.split(command)
    except ValueError:
        argv = [command] if command else []
    return {
        "schema": "sysfield.agent_action.v1",
        "action_id": f"act-{seq:06d}",
        "episode_id": context.get("episode_id", ""),
        "topology": context.get("topology", ""),
        "seq": seq,
        "actor": {
            "id": actor.get("id", ""),
            "agent": actor.get("agent", "opencode"),
            "session_id": actor.get("session_id", ""),
            "origin_node": actor.get("node", ""),
            "origin_ips": actor.get("ips", []),
        },
        "time": {
            "start_unix_ms": event.start_ts,
            "end_unix_ms": event.end_ts,
        },
        "tool": {
            "name": event.tool,
            "command": command,
            "argv": argv,
            "cwd": None,
        },
        "result": {
            "status": event.status,
            "exit_code": None,
            "output_preview": event.output,
            "error": event.error,
        },
        "raw_ref": {
            "source": "opencode",
            "call_id": event.call_id,
            "transcript_path": f"episodes/{context.get('episode_id', '')}/transcript.json",
        },
    }


def tetragon_command(raw_path: Path) -> list[str]:
    template = os.environ.get("TETRAGON_CAPTURE_CMD", "tetragon --export-filename {output}")
    rendered = template.format(output=str(raw_path))
    return shlex.split(rendered)


def start_tetragon(raw_path: Path) -> subprocess.Popen[Any]:
    cmd = tetragon_command(raw_path)
    try:
        return subprocess.Popen(cmd, stdout=subprocess.DEVNULL, stderr=subprocess.STDOUT)
    except FileNotFoundError as exc:
        raise RuntimeError(
            "tetragon executable not found. Install Tetragon or set "
            "TETRAGON_CAPTURE_CMD='your-command --output {output}'."
        ) from exc


def stop_process(proc: subprocess.Popen[Any], timeout: float = 5.0) -> None:
    if proc.poll() is not None:
        return
    proc.send_signal(signal.SIGINT)
    try:
        proc.wait(timeout=timeout)
        return
    except subprocess.TimeoutExpired:
        proc.terminate()
    try:
        proc.wait(timeout=timeout)
        return
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait(timeout=timeout)


def run_episode(args: argparse.Namespace) -> int:
    load_dotenv()
    state_path = Path(args.state)
    if not state_path.exists():
        raise FileNotFoundError(f"state file not found: {state_path}. Run sudo -E examples/mixed/lab.sh up first.")

    state = read_json(state_path)
    episode_id = args.episode or f"mixed-{uuid.uuid4().hex[:8]}"
    runs_dir = state_path.parent
    ep_dir = runs_dir / "episodes" / episode_id
    ep_dir.mkdir(parents=True, exist_ok=True)

    prompt = args.prompt
    context = build_episode_context(
        episode_id=episode_id,
        topology=args.topology,
        state_path=state_path,
        state=state,
        actor_name=args.actor,
        prompt=prompt,
    )
    write_json(ep_dir / "episode_context.json", context)

    meta = {
        "schema": "sysfield.capture_meta.v1",
        "episode_id": episode_id,
        "topology": args.topology,
        "status": "running",
        "started_unix_ms": int(time.time() * 1000),
        "prompt": prompt,
        "sensor": "tetragon",
    }
    write_json(ep_dir / "meta.json", meta)

    raw_tetragon = ep_dir / "tetragon.raw.jsonl"
    tetragon_proc: subprocess.Popen[Any] | None = None
    actions: list[dict[str, Any]] = []
    errors = 0
    try:
        print(f"==> Episode: {episode_id}")
        print(f"==> Output:   {ep_dir}")
        print("==> Starting Tetragon capture...")
        tetragon_proc = start_tetragon(raw_tetragon)
        time.sleep(args.sensor_warmup_s)
        if tetragon_proc.poll() is not None:
            raise RuntimeError(
                "Tetragon capture command exited during warmup. "
                "Check TETRAGON_CAPTURE_CMD and local Tetragon installation."
            )

        acp_url = args.agent_url or context["actor"].get("acp_url")
        if not acp_url:
            raise RuntimeError("actor ACP URL missing; pass --agent-url")
        print(f"==> Connecting opencode ACP: {acp_url}")
        client = OpenCodeClient(acp_url, timeout=args.agent_timeout_s)
        if not client.wait_ready(timeout=30.0):
            raise RuntimeError(f"agent not reachable at {acp_url}")

        session_id = client.create_session()
        context["actor"]["session_id"] = session_id
        write_json(ep_dir / "episode_context.json", context)
        print(f"==> Session: {session_id}")
        print(f"==> Prompt:  {prompt}")

        seq = 0
        for event in client.send_prompt(session_id, prompt):
            if event.status in ("completed", "error"):
                seq += 1
                action = normalize_tool_call_event(event, seq=seq, context=context)
                actions.append(action)
                write_jsonl(ep_dir / "agent_actions.jsonl", [action])
                if event.status == "error":
                    errors += 1
                print(f"  [{seq}] {event.status} {event.command[:100]}")

        time.sleep(args.sensor_grace_s)
        transcript = client.get_transcript(session_id)
        write_json(ep_dir / "transcript.json", transcript)

        meta["status"] = "completed"
        meta["finished_unix_ms"] = int(time.time() * 1000)
        meta["actions"] = len(actions)
        meta["errors"] = errors
        write_json(ep_dir / "meta.json", meta)
        print(f"==> Completed: {len(actions)} action(s), {errors} error(s)")
        print(f"==> Raw Tetragon: {raw_tetragon}")
        return 0
    except Exception as exc:
        meta["status"] = "error"
        meta["error"] = str(exc)
        meta["finished_unix_ms"] = int(time.time() * 1000)
        write_json(ep_dir / "meta.json", meta)
        print(f"ERROR: {exc}", file=sys.stderr)
        return 1
    finally:
        if tetragon_proc is not None:
            print("==> Stopping Tetragon capture...")
            stop_process(tetragon_proc)


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Capture mixed opencode + Tetragon episode")
    parser.add_argument("--episode", default="", help="episode id; default auto-generated")
    parser.add_argument("--topology", default=DEFAULT_TOPOLOGY)
    parser.add_argument("--state", default=str(DEFAULT_STATE))
    parser.add_argument("--actor", default=DEFAULT_ACTOR)
    parser.add_argument("--agent-url", default="", help="override opencode ACP URL")
    parser.add_argument("--prompt", default=DEFAULT_PROMPT)
    parser.add_argument("--sensor-warmup-s", type=float, default=5.0)
    parser.add_argument("--sensor-grace-s", type=float, default=8.0)
    parser.add_argument("--agent-timeout-s", type=float, default=300.0)
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    return run_episode(parse_args(argv or sys.argv[1:]))


if __name__ == "__main__":
    raise SystemExit(main())
