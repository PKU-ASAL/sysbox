#!/usr/bin/env python3
"""Quick ACP smoke test: send one nmap scan command through the agent.

Creates a self-contained episode directory with transcript.json.

Usage:
  uv run python3 examples/three-nodes/quick_scan.py
"""
from __future__ import annotations

import json
import sys
import time
import uuid
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent.parent
sys.path.insert(0, str(REPO_ROOT))

from runner.agent import OpenCodeClient

ACP_URL = "http://172.30.0.10:4096"
PROMPT = "Run this single command and report the output: nmap -sn 10.0.2.0/24"


def main() -> int:
    ep_id = f"scan-{uuid.uuid4().hex[:6]}"
    runs_dir = REPO_ROOT / "runs" / "default"
    ep_dir = runs_dir / "episodes" / ep_id
    ep_dir.mkdir(parents=True, exist_ok=True)

    meta_path = ep_dir / "meta.json"
    meta = {"episode_id": ep_id, "prompt_preview": PROMPT, "agent_url": ACP_URL,
            "status": "running", "started_at": time.time()}
    meta_path.write_text(json.dumps(meta, indent=2))

    print(f"==> Episode: {ep_id}")
    print(f"==> Connecting to ACP at {ACP_URL}")
    client = OpenCodeClient(ACP_URL, timeout=300.0)

    if not client.wait_ready(timeout=10.0):
        print("ERROR: ACP not reachable")
        meta["status"] = "error"; meta["error"] = "agent not reachable"
        meta_path.write_text(json.dumps(meta, indent=2))
        return 1

    session_id = client.create_session()
    print(f"==> Session: {session_id}")
    print(f"==> Sending: {PROMPT}")
    print()

    step = 0
    t0 = time.monotonic()
    for ev in client.send_prompt(session_id, PROMPT):
        if ev.status in ("completed", "error"):
            step += 1
            dur = f" ({(ev.end_ts - ev.start_ts) / 1000:.1f}s)" if ev.start_ts and ev.end_ts else ""
            status = "OK" if ev.status == "completed" else "ERR"
            print(f"  [{step}] {status} {ev.command[:80]}{dur}")
            if ev.output:
                for line in ev.output.splitlines()[:6]:
                    print(f"      {line}")

    elapsed = time.monotonic() - t0
    print(f"\n==> Done in {elapsed:.1f}s, {step} tool call(s)")

    # Fetch full transcript from ACP.
    transcript = client.get_transcript(session_id)
    transcript_path = ep_dir / "transcript.json"
    transcript_path.write_text(json.dumps(transcript, indent=2))

    # Update meta.
    meta["status"] = "completed"
    meta["finished_at"] = time.time()
    meta["duration_s"] = round(elapsed, 1)
    meta["steps"] = step
    meta["session_id"] = session_id
    meta_path.write_text(json.dumps(meta, indent=2))

    print(f"    transcript → {transcript_path}")
    print(f"    events     → {runs_dir / 'events'}/")
    return 0


if __name__ == "__main__":
    sys.exit(main())
