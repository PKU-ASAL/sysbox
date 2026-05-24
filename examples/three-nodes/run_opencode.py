#!/usr/bin/env python3
"""run_opencode.py — Drive the opencode agent inside node_attack via ACP.

Each execution creates a self-contained episode directory:
  .sysbox/runs/<run-id>/episodes/<ep-id>/
    meta.json          — episode metadata (prompt, timestamps, step count)
    step_log.jsonl      — agent tool-call timeline

The sensor's events/ directory is append-only and shared across episodes;
episode isolation is by timestamp, not by file boundary.

Prerequisites:
  sudo -E ./lab.sh up
  Set DEEPSEEK_API_KEY (or your provider key) in .env or the shell.

Usage:
  uv run python3 examples/three-nodes/run_opencode.py
  uv run python3 examples/three-nodes/run_opencode.py --episode ep-001
  uv run python3 examples/three-nodes/run_opencode.py --prompt "Run nmap -sn 10.0.2.0/24"
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
import uuid
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent.parent
sys.path.insert(0, str(REPO_ROOT))

from runner.agent import OpenCodeClient, ToolCallEvent

DEFAULT_AGENT_URL = "http://172.20.0.10:4096"
STATE_FILE = REPO_ROOT / "runs" / "default" / "state.json"
PROMPT_FILE = Path(__file__).parent / "prompts" / "attack_in_container.txt"


def load_env() -> None:
    env_file = REPO_ROOT / ".env"
    if env_file.exists():
        for line in env_file.read_text().splitlines():
            line = line.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            key, _, val = line.partition("=")
            key = key.strip()
            val = val.strip().strip('"').strip("'")
            if key and key not in os.environ:
                os.environ[key] = val
    if not os.environ.get("DEEPSEEK_API_KEY"):
        raise RuntimeError(
            "DEEPSEEK_API_KEY not set.\n"
            "  Export it or add it to .env before running."
        )


def load_state() -> dict:
    if not STATE_FILE.exists():
        raise FileNotFoundError(
            f"State file not found: {STATE_FILE}\n"
            "  Run: sudo -E ./lab.sh up"
        )
    return json.loads(STATE_FILE.read_text())


def find_actor(state: dict, name: str) -> dict | None:
    for r in state.get("resources", []):
        if r.get("type") == "sysbox_actor" and r.get("name") == name:
            return r.get("instance", {})
    return None


def print_banner(agent_url: str, ep_id: str, ep_dir: Path) -> None:
    print("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
    print(" sysbox — opencode episode runner")
    print("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
    print(f" agent:    {agent_url}")
    print(f" episode:  {ep_id}")
    print(f" output:   {ep_dir}")
    print()


def print_step(i: int, ev: ToolCallEvent) -> None:
    status_sym = {"in_progress": "▶", "completed": "✓", "error": "✗"}.get(ev.status, "?")
    cmd_short = ev.command[:70] + "..." if len(ev.command) > 70 else ev.command
    dur = ""
    if ev.start_ts and ev.end_ts:
        dur = f" ({(ev.end_ts - ev.start_ts) / 1000:.1f}s)"
    print(f"  [{i:2d}] {status_sym} {cmd_short}{dur}")


def main() -> int:
    parser = argparse.ArgumentParser(description="sysbox opencode episode runner")
    parser.add_argument("--episode", default=None, help="episode ID (default: auto-generated)")
    parser.add_argument("--prompt", default=None, help="prompt text (default: read from prompts/attack_in_container.txt)")
    parser.add_argument("--agent-url", default=None,
                        help=f"opencode ACP URL (default: {DEFAULT_AGENT_URL})")
    parser.add_argument("--actor", default="red",
                        help="sysbox_actor name to look up ACP URL from state")
    args = parser.parse_args()

    ep_id = args.episode or f"ep-{uuid.uuid4().hex[:8]}"
    load_env()

    try:
        state = load_state()
    except FileNotFoundError as exc:
        print(f"ERROR: {exc}")
        return 1

    agent_url = args.agent_url
    if not agent_url:
        actor = find_actor(state, args.actor)
        if actor:
            agent_url = actor.get("acp_url") or \
                f"http://172.20.0.10:{int(actor.get('port', 4096))}"
    agent_url = agent_url or DEFAULT_AGENT_URL

    # Episode directory: .sysbox/runs/<run-id>/episodes/<ep-id>/
    runs_dir = STATE_FILE.parent
    ep_dir = runs_dir / "episodes" / ep_id
    ep_dir.mkdir(parents=True, exist_ok=True)

    print_banner(agent_url, ep_id, ep_dir)

    actor = find_actor(state, args.actor)
    if actor:
        print(f" actor: sysbox_actor.{args.actor}  pid={actor.get('pid', '?')}")
    else:
        print(f" warn: sysbox_actor.{args.actor} not found in state")

    # Load prompt.
    prompt = args.prompt or PROMPT_FILE.read_text()
    prompt_preview = prompt.split("\n")[0][:80]

    # Write meta.json at start (will be updated at the end).
    meta = {
        "episode_id": ep_id,
        "prompt_preview": prompt_preview,
        "agent_url": agent_url,
        "status": "running",
        "started_at": time.time(),
    }
    meta_path = ep_dir / "meta.json"
    meta_path.write_text(json.dumps(meta, indent=2))

    # Connect to agent.
    print()
    print("==> Connecting to opencode agent...")
    client = OpenCodeClient(agent_url, timeout=600.0)
    if not client.wait_ready(timeout=30.0):
        print(f"ERROR: Agent not reachable at {agent_url}")
        meta["status"] = "error"
        meta["error"] = "agent not reachable"
        meta_path.write_text(json.dumps(meta, indent=2))
        return 1
    print("    Connected.")

    session_id = client.create_session()
    print(f"    Session: {session_id}")
    print()
    print("==> Sending prompt...")
    print(f"    {prompt_preview}")
    print()

    all_events: list[ToolCallEvent] = []
    step_num = 0
    t0 = time.monotonic()

    try:
        for ev in client.send_prompt(session_id, prompt):
            all_events.append(ev)
            if ev.status in ("completed", "error"):
                step_num += 1
                print_step(step_num, ev)
    except Exception as exc:
        print(f"ERROR during agent run: {exc}")
        meta["status"] = "error"
        meta["error"] = str(exc)
        meta_path.write_text(json.dumps(meta, indent=2))
        return 1

    elapsed = time.monotonic() - t0
    errors = sum(1 for e in all_events if e.status == "error")
    print()
    print(f"  Done in {elapsed:.1f}s — {step_num} tool calls ({errors} errors)")

    # Fetch the full transcript from ACP (user prompt + reasoning + tool
    # calls with full output + assistant text responses).
    transcript = client.get_transcript(session_id)
    transcript_path = ep_dir / "transcript.json"
    transcript_path.write_text(json.dumps(transcript, indent=2))
    msg_count = len(transcript)
    print(f"  transcript → {transcript_path}  ({msg_count} messages)")

    # Update meta.json with final status.
    meta["status"] = "completed"
    meta["finished_at"] = time.time()
    meta["duration_s"] = round(elapsed, 1)
    meta["steps"] = step_num
    meta["errors"] = errors
    meta["session_id"] = session_id
    meta_path.write_text(json.dumps(meta, indent=2))

    print()
    print("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
    print(f" Episode {ep_id} — {meta['status']}")
    print(f"   steps:    {step_num}")
    print(f"   duration: {elapsed:.1f}s")
    print(f"   output:   {ep_dir}/")
    print(f"   events:   {runs_dir / 'events'}/  (append-only, sensor-managed)")
    print("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

    return 0


if __name__ == "__main__":
    sys.exit(main())
