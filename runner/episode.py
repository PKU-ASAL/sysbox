"""SysboxEpisode: runs one attack episode via the Claude Agent SDK.

Both Mode A (CLI) and Mode B (SDK) ultimately write to the same
predictions.jsonl and events.jsonl files. The scoring is always done
by `sysbox match run`.

Mode A (CLI + HTTP hook server): handled by lab.sh / sysbox hook serve.
Mode B (SDK + in-process hook):  handled by this module.
"""

from __future__ import annotations

import json
import os
import subprocess
import time
import uuid
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from .resolver import NodeResolver

try:
    from dotenv import load_dotenv
    _DOTENV_AVAILABLE = True
except ImportError:
    _DOTENV_AVAILABLE = False

try:
    from claude_agent_sdk import (
        query,
        ClaudeAgentOptions,
        HookMatcher,
        SystemMessage,
        ResultMessage,
    )
    _SDK_AVAILABLE = True
except ImportError:
    _SDK_AVAILABLE = False


def _load_env(repo_root: Path) -> dict[str, str]:
    """Load .env from repo root (if present) and return relevant SDK env vars.

    Priority: existing os.environ > .env file.
    The returned dict is passed as ClaudeAgentOptions(env=...) so the claude
    subprocess inherits ANTHROPIC_API_KEY and ANTHROPIC_BASE_URL.
    """
    env_file = repo_root / ".env"
    if _DOTENV_AVAILABLE and env_file.exists():
        # override=False: don't clobber variables already set in the shell.
        load_dotenv(env_file, override=False)
    elif env_file.exists():
        # dotenv not available: parse manually so we don't lose the values.
        for line in env_file.read_text().splitlines():
            line = line.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            key, _, val = line.partition("=")
            key = key.strip()
            val = val.strip().strip('"').strip("'")
            if key and key not in os.environ:
                os.environ[key] = val

    sdk_env: dict[str, str] = {}
    for key in ("ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"):
        val = os.environ.get(key)
        if val:
            sdk_env[key] = val

    if not sdk_env.get("ANTHROPIC_API_KEY"):
        raise RuntimeError(
            "ANTHROPIC_API_KEY not found.\n"
            "  Set it in .env or export it before running:\n"
            "    set -a && source .env && set +a"
        )
    return sdk_env


@dataclass
class EpisodeResult:
    run_id: str
    session_id: str | None
    steps: int
    predictions_written: int
    episode_reward: float
    hit_rate: float
    ttps_covered: list[str]
    match_report: dict = field(default_factory=dict)

    def __str__(self) -> str:
        ttps = ", ".join(self.ttps_covered) or "none"
        return (
            f"Episode {self.run_id}\n"
            f"  steps:      {self.steps}\n"
            f"  reward:     {self.episode_reward:.3f}\n"
            f"  hit_rate:   {self.hit_rate*100:.0f}%\n"
            f"  TTPs:       {ttps}\n"
            f"  preds out:  {self.predictions_written}"
        )


class SysboxEpisode:
    """Runs one attack episode via the Claude Agent SDK (Mode B).

    Usage:
        episode = SysboxEpisode(
            state_file="runs/default/state.json",
            rules_dir="rules/",
        )
        result = await episode.run(prompt="You are a red team ...")
        print(result)
    """

    def __init__(
        self,
        state_file: str | Path = "runs/default/state.json",
        rules_dir: str | Path = "rules/",
        sysbox_bin: str = "sysbox",
        run_id: str | None = None,
        allowed_tools: list[str] | None = None,
        model: str | None = None,
        max_turns: int | None = None,
    ):
        if not _SDK_AVAILABLE:
            raise ImportError(
                "claude_agent_sdk is not installed.\n"
                "  uv add claude-agent-sdk"
            )

        self.state_file = Path(state_file).resolve()
        self.rules_dir = Path(rules_dir).resolve()
        self.sysbox_bin = sysbox_bin
        self.run_id = run_id or f"ep-{uuid.uuid4().hex[:8]}"
        self.allowed_tools = allowed_tools or ["Bash"]

        # Model / turn limit: env vars take precedence, then args, then defaults.
        self.model = model or os.environ.get("SYSBOX_MODEL", "claude-sonnet-4-5")
        self.max_turns = max_turns or int(os.environ.get("SYSBOX_MAX_TURNS", "30"))

        # Load .env from repo root (two levels up from runner/).
        repo_root = Path(__file__).resolve().parent.parent
        self._sdk_env = _load_env(repo_root)

        self._resolver = NodeResolver(state_file)
        self._step = 0
        self._predictions_written = 0
        self._session_id: str | None = None

    async def run(self, prompt: str) -> EpisodeResult:
        """Run the full episode: agent loop → predict → match → reward."""
        self._step = 0
        self._predictions_written = 0
        self._session_id = None

        options = ClaudeAgentOptions(
            allowed_tools=self.allowed_tools,
            permission_mode="acceptEdits",
            model=self.model,
            max_turns=self.max_turns,
            env=self._sdk_env or None,
            hooks={
                "PreToolUse": [
                    HookMatcher(matcher="Bash", hooks=[self._pre_tool_hook])
                ]
            },
        )

        print(f"  run_id: {self.run_id}  model: {self.model}  max_turns: {self.max_turns}")
        print()

        async for message in query(prompt=prompt, options=options):
            if isinstance(message, SystemMessage) and message.subtype == "init":
                self._session_id = message.data.get("session_id")
            elif isinstance(message, ResultMessage):
                self._session_id = message.session_id or self._session_id

        print()
        return self._score()

    async def _pre_tool_hook(
        self, input_data: dict[str, Any], tool_use_id: str, context: Any
    ) -> dict:
        """In-process PreToolUse callback: extract IoC prediction and persist it."""
        self._step += 1
        command = input_data.get("tool_input", {}).get("command", "")
        cmd_preview = command[:72].replace("\n", " ") if command else "(empty)"

        if not command:
            print(f"  [{self._step:02d}] (no command)")
            return {}

        node = self._resolver.resolve(command)
        start_ts = int(time.time() * 1000)  # unix ms at PreToolUse time

        args = [
            self.sysbox_bin,
            "--state", str(self.state_file),
            "predict", "submit",
            "--command", command,
            "--node", node,
            "--step", str(self._step),
            "--run-id", self.run_id,
            "--start-ts", str(start_ts),
        ]
        result = subprocess.run(args, capture_output=True, text=True, timeout=10)

        if result.returncode == 0:
            self._predictions_written += 1
            # Extract rule/ttp from stdout for display.
            rule = ""
            for line in result.stdout.splitlines():
                if "rule=" in line:
                    rule = line.strip()
                    break
            print(f"  [{self._step:02d}] node={node:<12} cmd={cmd_preview!r}")
            if rule:
                print(f"         → {rule}")
        else:
            err = (result.stderr or result.stdout).strip().splitlines()
            err_summary = err[-1] if err else "unknown error"
            print(f"  [{self._step:02d}] node={node:<12} cmd={cmd_preview!r}")
            print(f"         ✗ predict submit failed: {err_summary}")

        return {}  # always allow the tool call to proceed

    def _score(self) -> EpisodeResult:
        """Run sysbox match run and parse the result."""
        args = [
            self.sysbox_bin,
            "--state", str(self.state_file),
            "match", "run",
            "--run-id", self.run_id,
        ]
        subprocess.run(args, capture_output=True, text=True, timeout=60)

        report_path = self.state_file.parent / "match_report.json"
        try:
            report = json.loads(report_path.read_text())
        except (FileNotFoundError, json.JSONDecodeError):
            report = {}

        return EpisodeResult(
            run_id=self.run_id,
            session_id=self._session_id,
            steps=self._step,
            predictions_written=self._predictions_written,
            episode_reward=report.get("episode_reward", 0.0),
            hit_rate=report.get("episode_prediction_hit_rate", 0.0),
            ttps_covered=report.get("ttps_covered", []),
            match_report=report,
        )
