#!/usr/bin/env python3
"""run_opencode.py — Episode runner for opencode agent inside node_attack.

Architecture:
  opencode serve (inside sysbox-node_attack, 172.20.0.10:4096)
      │  ACP-style HTTP API + SSE
      ▼
  run_opencode.py (host, this script)
      │  reads agent PID from sysbox state
      ▼
  sysbox match run --agent red
      │  PID tree BFS → episode_report.json
      ▼
  EpisodeReport printed

Prerequisites:
  sudo -E ./lab.sh up          # builds image, applies field, starts sensor
                               # sysbox_agent.red starts opencode in container
  set ANTHROPIC_API_KEY in .env or shell

Usage:
  uv run python3 examples/three-nodes/run_opencode.py
  uv run python3 examples/three-nodes/run_opencode.py --run-id ep-001
  uv run python3 examples/three-nodes/run_opencode.py --agent-url http://172.20.0.10:4096
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import time
import uuid
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent.parent
sys.path.insert(0, str(REPO_ROOT))

from runner.agent import OpenCodeClient, ToolCallEvent  # noqa: E402

# Default agent URL: node_attack uplink IP (Docker bridge, reachable from host).
DEFAULT_AGENT_URL = "http://172.20.0.10:4096"

STATE_FILE  = REPO_ROOT / "runs" / "default" / "state.json"
SYSBOX_BIN  = str(REPO_ROOT / "bin" / "sysbox")
# Use the in-container prompt (agent runs directly, no docker exec prefix).
PROMPT_FILE = Path(__file__).parent / "prompts" / "attack_in_container.txt"


# ── Environment ───────────────────────────────────────────────────────────────

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


# ── State helpers ─────────────────────────────────────────────────────────────

def load_state() -> dict:
    if not STATE_FILE.exists():
        raise FileNotFoundError(
            f"State file not found: {STATE_FILE}\n"
            "  Run: sudo -E ./lab.sh up"
        )
    return json.loads(STATE_FILE.read_text())


def find_agent_resource(state: dict, agent_name: str = "red") -> dict | None:
    """Find actor/agent resource; prefers sysbox_actor, falls back to sysbox_agent."""
    for r in state.get("resources", []):
        if r.get("type") == "sysbox_actor" and r.get("name") == agent_name:
            return r.get("instance", {})
    for r in state.get("resources", []):
        if r.get("type") == "sysbox_agent" and r.get("name") == agent_name:
            return r.get("instance", {})
    return None


# ── Matcher ───────────────────────────────────────────────────────────────────

def run_match(run_id: str) -> dict:
    """Call `sysbox match run --agent red` and return the EpisodeReport."""
    result = subprocess.run(
        [SYSBOX_BIN, "--state", str(STATE_FILE), "match", "run",
         "--agent", "red", "--run-id", run_id],
        capture_output=True, text=True, timeout=60,
    )
    if result.returncode != 0:
        lines = (result.stderr or result.stdout).strip().splitlines()
        print(f"  warn: match run failed: {lines[-1] if lines else '?'}")
        return {}

    report_path = STATE_FILE.parent / "episode_report.json"
    try:
        return json.loads(report_path.read_text())
    except Exception:
        return {}


# ── Display ───────────────────────────────────────────────────────────────────

def print_banner(agent_url: str, run_id: str) -> None:
    print("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
    print(" sysbox — opencode episode runner")
    print("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
    print(f" agent:  {agent_url}")
    print(f" state:  {STATE_FILE}")
    print(f" run_id: {run_id}")
    print()


def print_step(i: int, ev: ToolCallEvent) -> None:
    status_sym = {"in_progress": "▶", "completed": "✓", "error": "✗"}.get(ev.status, "?")
    cmd_short = ev.command[:70] + "..." if len(ev.command) > 70 else ev.command
    dur = ""
    if ev.start_ts and ev.end_ts:
        dur = f" ({(ev.end_ts - ev.start_ts) / 1000:.1f}s)"
    print(f"  [{i:2d}] {status_sym} {cmd_short}{dur}")


def print_report(report: dict, step_count: int, events: list | None = None) -> None:
    print()
    print("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
    print(" Episode Result (PID tree attribution)")
    print("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
    print(f"  tool steps:       {step_count}")
    print(f"  anchor_pid:       {report.get('anchor_pid', '?')}")
    print(f"  events scanned:   {report.get('total_events_scanned', '?')}")
    print(f"  attack events:    {len(report.get('attack_events') or [])}")
    if report.get("events_by_type"):
        print("  by type:")
        for evt_type, count in sorted(report["events_by_type"].items()):
            print(f"    {evt_type:<28} {count}")
    print()


# ── Main ──────────────────────────────────────────────────────────────────────

def main() -> int:
    parser = argparse.ArgumentParser(description="sysbox opencode episode runner")
    parser.add_argument("--run-id", default=None)
    parser.add_argument("--agent-url", default=None,
                        help=f"opencode server URL (default: {DEFAULT_AGENT_URL})")
    parser.add_argument("--agent", default="red",
                        help="sysbox_actor (or sysbox_agent) name to look up URL from state")
    args = parser.parse_args()

    run_id = args.run_id or f"ep-{uuid.uuid4().hex[:8]}"

    load_env()

    # Resolve agent URL: CLI flag > state lookup > default
    agent_url = args.agent_url
    if not agent_url:
        try:
            state = load_state()
            agent_res = find_agent_resource(state, args.agent)
            if agent_res:
                # Prefer acp_url stored at apply time (sysbox_actor).
                # Fall back to constructing from port (legacy sysbox_agent).
                agent_url = agent_res.get("acp_url") or \
                    f"http://172.20.0.10:{int(agent_res.get('port', 4096))}"
        except FileNotFoundError as exc:
            print(f"ERROR: {exc}")
            return 1

    agent_url = agent_url or DEFAULT_AGENT_URL

    print_banner(agent_url, run_id)

    # Verify the lab is up.
    try:
        state = load_state()
    except FileNotFoundError as exc:
        print(f"ERROR: {exc}")
        return 1

    agent_res = find_agent_resource(state, args.agent)
    if agent_res:
        pid = int(agent_res.get("pid", 0))
        print(f" opencode PID (in container): {pid}")
    else:
        print(f" warn: sysbox_actor.{args.agent} not found in state")
        print("  Ensure lab.sh up has completed and sysbox_actor.red was applied.")

    # Clear per-episode artefacts.
    runs_dir = STATE_FILE.parent
    for fname in ("episode_report.json", "step_log.jsonl"):
        p = runs_dir / fname
        if p.exists():
            p.unlink()

    # Truncate per-node event files so this episode starts clean.
    # The sensor keeps its file descriptors open; truncating (not deleting)
    # resets each file without breaking the write stream on Linux.
    events_dir = runs_dir / "events"
    if events_dir.exists():
        for f in events_dir.glob("*.jsonl"):
            open(f, "w").close()

    prompt = PROMPT_FILE.read_text()

    print()
    print("==> Connecting to opencode agent...")
    client = OpenCodeClient(agent_url, timeout=600.0)
    if not client.wait_ready(timeout=30.0):
        print(f"ERROR: Agent not reachable at {agent_url}")
        print("  Check: docker exec sysbox-node_attack pgrep -a opencode")
        return 1
    print("    Connected.")

    session_id = client.create_session()
    print(f"    Session: {session_id}")
    print()
    print("==> Sending attack prompt (waiting for agent to finish)...")
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
        return 1

    elapsed = time.monotonic() - t0
    errors = sum(1 for e in all_events if e.status == "error")
    print()
    print(f"  Done in {elapsed:.1f}s — {step_num} tool calls ({errors} errors)")

    # Write step_log.jsonl for debugging.
    step_log_path = runs_dir / "step_log.jsonl"
    with open(step_log_path, "w") as fh:
        for i, ev in enumerate(all_events, 1):
            fh.write(json.dumps({
                "step": i, "run_id": run_id,
                "call_id": ev.call_id, "tool": ev.tool, "status": ev.status,
                "command": ev.command, "start_ts": ev.start_ts, "end_ts": ev.end_ts,
            }) + "\n")
    print(f"  step_log → {step_log_path}")

    # Run PID-tree matcher.
    print()
    print("==> Running PID-tree matcher...")
    report = run_match(run_id)

    print_report(report, step_num, all_events)

    # Archive episode artefacts to runs/episodes/{run_id}/ for isolation.
    import shutil
    ep_dir = runs_dir / "episodes" / run_id
    ep_dir.mkdir(parents=True, exist_ok=True)
    for src in (step_log_path, runs_dir / "episode_report.json"):
        if src.exists():
            shutil.copy2(src, ep_dir / src.name)
    # Archive per-node event files as a snapshot of this episode.
    if events_dir.exists():
        shutil.copytree(events_dir, ep_dir / "events", dirs_exist_ok=True)
    print(f"  archived → {ep_dir}/")

    return 0


if __name__ == "__main__":
    sys.exit(main())
