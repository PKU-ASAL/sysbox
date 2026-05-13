#!/usr/bin/env python3
"""run_sdk.py — Mode B: single automated attack episode via the Claude Agent SDK.

Prerequisites:
    pip install claude-agent-sdk
    sudo -E ./lab.sh up        # apply field + start tracee sensor

Unlike Mode A (CLI), this does NOT require `sysbox hook serve` to be running.
Predictions are extracted in-process via Python PreToolUse callbacks and written
to predictions.jsonl by calling `sysbox predict submit`.

Usage:
    python3 run_sdk.py [--run-id my-episode-001]
"""

import argparse
import asyncio
import sys
from pathlib import Path

# Make the runner package importable from this directory.
REPO_ROOT = Path(__file__).resolve().parent.parent.parent
sys.path.insert(0, str(REPO_ROOT))

from runner.episode import SysboxEpisode

STATE_FILE  = REPO_ROOT / "runs" / "default" / "state.json"
RULES_DIR   = REPO_ROOT / "rules"
SYSBOX_BIN  = str(REPO_ROOT / "bin" / "sysbox")
PROMPT_FILE = Path(__file__).parent / "prompts" / "attack.txt"


async def main(run_id: str | None) -> int:
    if not STATE_FILE.exists():
        print(f"ERROR: state file not found: {STATE_FILE}")
        print("  Run: sudo -E ./lab.sh up")
        return 1

    # Clear per-episode files so the matcher only sees predictions from this run.
    # events.jsonl is NOT cleared here — the sensor process holds it open and
    # deleting it would cause writes to go to the unlinked inode (lost).
    runs_dir = STATE_FILE.parent
    for fname in ("predictions.jsonl", "match_report.json"):
        p = runs_dir / fname
        if p.exists():
            p.unlink()

    prompt = PROMPT_FILE.read_text()

    print("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
    print(" sysbox Mode B — Automated SDK Episode")
    print("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
    print(f" state:  {STATE_FILE}")
    print(f" rules:  {RULES_DIR}")
    print()
    print(" Running agent... (predictions captured in-process)")
    print("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

    episode = SysboxEpisode(
        state_file=STATE_FILE,
        rules_dir=RULES_DIR,
        sysbox_bin=SYSBOX_BIN,
        run_id=run_id,
        allowed_tools=["Bash"],
    )

    result = await episode.run(prompt=prompt)

    print()
    print("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
    print(" Episode Result")
    print("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
    print(result)
    print()

    for step in result.match_report.get("steps", []):
        print(f"  Step {step['agent_step']:2d} [{step['node']}]"
              f"  hit={step['prediction_hit_rate']*100:.0f}%"
              f"  unscripted={len(step.get('unscripted_iocs') or [])}"
              f"  reward={step['step_reward']:.3f}"
              f"  ttp={step.get('ttp', '-')}")

    return 0 if result.episode_reward >= 0 else 1


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="sysbox Mode B: single SDK episode")
    parser.add_argument("--run-id", default=None, help="episode run ID (default: auto-generated)")
    args = parser.parse_args()
    sys.exit(asyncio.run(main(args.run_id)))
