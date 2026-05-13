#!/usr/bin/env python3
"""run_rl.py — Multi-episode RL training loop using the Claude Agent SDK.

Each episode:
  1. (Re)applies the field to reset the lab to a clean state
  2. Starts tracee sensor
  3. Runs one attack episode via SysboxEpisode
  4. Scores with sysbox match run
  5. Collects episode_reward for the RL trainer

The reward signal is directly usable by:
  - GRPO / PPO policy gradient trainers (via reward list)
  - Simple bandit / curriculum tracking

Usage:
    # Dry-run 3 episodes, print rewards:
    python3 run_rl.py --episodes 3

    # Integration with a GRPO trainer:
    python3 run_rl.py --episodes 20 --output rewards.jsonl
"""

import argparse
import asyncio
import json
import subprocess
import sys
import time
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent.parent
sys.path.insert(0, str(REPO_ROOT))

from runner.episode import SysboxEpisode, EpisodeResult

STATE_FILE  = REPO_ROOT / "runs" / "default" / "state.json"
RULES_DIR   = REPO_ROOT / "rules"
FIELD_FILE  = Path(__file__).parent / "field.sysbox.hcl"
PROMPT_FILE = Path(__file__).parent / "prompts" / "attack.txt"
SYSBOX_BIN  = str(REPO_ROOT / "bin" / "sysbox")


def reset_lab() -> None:
    """Destroy and re-apply the field to get a clean environment."""
    print("  [reset] destroying field...")
    subprocess.run([
        SYSBOX_BIN, "--state", str(STATE_FILE),
        "destroy", "--file", str(FIELD_FILE), "--auto-approve",
    ], capture_output=True)

    # Clear events + predictions from previous episode.
    for f in ["events.jsonl", "predictions.jsonl", "match_report.json"]:
        p = STATE_FILE.parent / f
        if p.exists():
            p.unlink()

    print("  [reset] re-applying field...")
    subprocess.run([
        SYSBOX_BIN, "--state", str(STATE_FILE),
        "apply", "--file", str(FIELD_FILE), "--auto-approve",
    ], check=True, capture_output=True)

    print("  [reset] installing attack tools on node_attack...")
    subprocess.run([
        "docker", "exec", "sysbox-node_attack",
        "apk", "add", "--no-cache", "nmap", "openssh-client", "curl",
    ], capture_output=True)


def start_sensor() -> subprocess.Popen:
    """Start tracee sensor in background, return Popen handle."""
    proc = subprocess.Popen([
        SYSBOX_BIN,
        "--state", str(STATE_FILE),
        "--file", str(FIELD_FILE),
        "sensor", "start",
    ], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    time.sleep(5)  # give tracee time to initialize eBPF programs
    return proc


def stop_sensor(proc: subprocess.Popen) -> None:
    proc.terminate()
    try:
        proc.wait(timeout=10)
    except subprocess.TimeoutExpired:
        proc.kill()


async def run_episode(episode_idx: int) -> EpisodeResult:
    run_id = f"ep-{episode_idx:04d}-{int(time.time())}"
    print(f"\n{'─'*52}")
    print(f" Episode {episode_idx:3d}  run_id={run_id}")
    print(f"{'─'*52}")

    episode = SysboxEpisode(
        state_file=STATE_FILE,
        rules_dir=RULES_DIR,
        sysbox_bin=SYSBOX_BIN,
        run_id=run_id,
        allowed_tools=["Bash"],
    )

    prompt = PROMPT_FILE.read_text()
    result = await episode.run(prompt=prompt)

    print(f"  reward={result.episode_reward:+.3f}  "
          f"hit={result.hit_rate*100:.0f}%  "
          f"steps={result.steps}  "
          f"ttps={result.ttps_covered}")
    return result


async def main(n_episodes: int, reset_between: bool, output_file: Path | None) -> None:
    print("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
    print(f" sysbox RL Training — {n_episodes} episodes")
    print(f" reset between episodes: {reset_between}")
    print("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

    results: list[EpisodeResult] = []
    rewards: list[float] = []

    for i in range(1, n_episodes + 1):
        if reset_between:
            reset_lab()

        sensor_proc = start_sensor()
        try:
            result = await run_episode(i)
        finally:
            stop_sensor(sensor_proc)

        results.append(result)
        rewards.append(result.episode_reward)

        if output_file:
            with output_file.open("a") as f:
                json.dump({
                    "episode": i,
                    "run_id": result.run_id,
                    "reward": result.episode_reward,
                    "hit_rate": result.hit_rate,
                    "steps": result.steps,
                    "ttps": result.ttps_covered,
                }, f)
                f.write("\n")

    # Summary.
    mean_r = sum(rewards) / len(rewards) if rewards else 0.0
    best_r = max(rewards) if rewards else 0.0

    print(f"\n{'━'*52}")
    print(f" Training Summary — {n_episodes} episodes")
    print(f"{'━'*52}")
    print(f"  mean reward:  {mean_r:+.3f}")
    print(f"  best reward:  {best_r:+.3f}")
    print(f"  rewards:      {[f'{r:+.2f}' for r in rewards]}")
    if output_file:
        print(f"  saved to:     {output_file}")
    print()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="sysbox multi-episode RL loop")
    parser.add_argument("--episodes", type=int, default=3,
                        help="number of episodes to run (default: 3)")
    parser.add_argument("--no-reset", action="store_true",
                        help="skip field reset between episodes (faster, less clean)")
    parser.add_argument("--output", type=Path, default=None,
                        help="append episode rewards to this JSONL file")
    args = parser.parse_args()

    asyncio.run(main(
        n_episodes=args.episodes,
        reset_between=not args.no_reset,
        output_file=args.output,
    ))
