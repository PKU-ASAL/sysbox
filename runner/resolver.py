"""NodeResolver: maps Bash commands to sysbox node names.

Resolution order:
1. `ssh [user@]<ip>` → lookup IP in state.json NIC list
2. `docker exec sysbox-<node>` → direct node name
3. Last observed node for this session (sticky)
4. Fallback: "node_attack" (attacker is the default executor)
"""

import json
import re
from pathlib import Path


_SSH_RE = re.compile(r'\bssh\b[^\n]*?(?:[\w.]+@)?([\d.]+|[\w][\w.-]+)\b')
_DOCKER_EXEC_RE = re.compile(r'docker\s+exec\s+(?:-\w+\s+)*sysbox-([\w_-]+)')


class NodeResolver:
    def __init__(self, state_file: str | Path):
        self._ip_map: dict[str, str] = {}   # "10.0.2.10" → "node_web"
        self._last_node: str = "node_attack"
        self._load_state(Path(state_file))

    def _load_state(self, path: Path) -> None:
        if not path.exists():
            return
        try:
            state = json.loads(path.read_text())
        except (json.JSONDecodeError, OSError):
            return
        for r in state.get("resources", []):
            if r.get("type") != "sysbox_node":
                continue
            node = r["name"]
            for nic in r.get("instance", {}).get("nics", []):
                ip_cidr = nic.get("ip", "")
                ip = ip_cidr.split("/")[0]
                if ip:
                    self._ip_map[ip] = node

    def resolve(self, command: str, session_id: str | None = None) -> str:
        """Return the sysbox node name most likely targeted by command."""
        # 1. docker exec sysbox-<node> → node
        m = _DOCKER_EXEC_RE.search(command)
        if m:
            node = m.group(1)
            self._last_node = node
            return node

        # 2. SSH target → IP map
        m = _SSH_RE.search(command)
        if m:
            target = m.group(1)
            if target in self._ip_map:
                node = self._ip_map[target]
                self._last_node = node
                return node
            # Hostname match: try direct node name
            for node in self._ip_map.values():
                if target == node or target == f"sysbox-{node}":
                    self._last_node = node
                    return node

        # 3. Sticky last observed node
        return self._last_node
