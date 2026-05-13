"""sysbox runner — Python interface for automated attack episodes.

Supports two modes:
  Mode A (CLI):  Use `lab.sh up` + `run_cli.sh` for interactive Claude Code sessions.
  Mode B (SDK):  Use SysboxEpisode for programmatic episodes via the Agent SDK.

Quick start (Mode B):
    from runner.episode import SysboxEpisode
    import asyncio

    async def main():
        ep = SysboxEpisode(state_file="runs/default/state.json")
        result = await ep.run(prompt=open("examples/three-nodes/prompts/attack.txt").read())
        print(result)

    asyncio.run(main())
"""

from .episode import SysboxEpisode, EpisodeResult
from .resolver import NodeResolver

__all__ = ["SysboxEpisode", "EpisodeResult", "NodeResolver"]
