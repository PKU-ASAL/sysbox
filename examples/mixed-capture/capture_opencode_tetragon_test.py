#!/usr/bin/env python3
from __future__ import annotations

import importlib.util
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace


SCRIPT = Path(__file__).with_name("capture_opencode_tetragon.py")


def load_module():
    spec = importlib.util.spec_from_file_location("capture_opencode_tetragon", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    assert spec and spec.loader
    spec.loader.exec_module(module)
    return module


class CaptureOpencodeTetragonTest(unittest.TestCase):
    def test_build_episode_context_extracts_actor_and_nodes(self) -> None:
        mod = load_module()
        state = {
            "resources": [
                {
                    "type": "sysbox_actor",
                    "name": "red",
                    "provider": "docker",
                    "instance": {
                        "node": "node_attack",
                        "acp_url": "http://172.30.1.10:4096",
                        "port": 4096,
                    },
                },
                {
                    "type": "sysbox_node",
                    "name": "node_attack",
                    "provider": "docker",
                    "instance": {
                        "primary_ip": "10.0.1.10",
                        "links": [
                            {"ip": "10.0.1.10/24"},
                            {"ip": "172.30.1.10/24"},
                        ],
                    },
                },
                {
                    "type": "sysbox_node",
                    "name": "node_db",
                    "provider": "firecracker",
                    "instance": {"primary_ip": "10.0.2.20"},
                },
            ]
        }

        ctx = mod.build_episode_context(
            episode_id="ep-test",
            topology="mixed",
            state_path=Path(".sysbox/runs/mixed/state.json"),
            state=state,
            actor_name="red",
            prompt="Run exactly: curl -sS http://10.0.2.10/ | head",
        )

        self.assertEqual(ctx["schema"], "sysfield.episode.v1")
        self.assertEqual(ctx["actor"]["id"], "sysbox_actor.red")
        self.assertEqual(ctx["actor"]["node"], "sysbox_node.node_attack")
        self.assertIn("172.30.1.10", ctx["actor"]["ips"])
        self.assertEqual(ctx["nodes"]["sysbox_node.node_db"]["substrate"], "firecracker")

    def test_normalize_tool_call_event_writes_minimal_agent_action(self) -> None:
        mod = load_module()
        event = SimpleNamespace(
            call_id="call-1",
            tool="bash",
            status="completed",
            command="curl -sS http://10.0.2.10/ | head",
            start_ts=1783677601123,
            end_ts=1783677603456,
            output="<html>",
            error="",
        )
        context = {
            "episode_id": "ep-test",
            "topology": "mixed",
            "actor": {
                "id": "sysbox_actor.red",
                "agent": "opencode",
                "session_id": "ses-1",
                "node": "sysbox_node.node_attack",
                "ips": ["10.0.1.10", "172.30.1.10"],
            },
        }

        action = mod.normalize_tool_call_event(event, seq=1, context=context)

        self.assertEqual(action["schema"], "sysfield.agent_action.v1")
        self.assertEqual(action["action_id"], "act-000001")
        self.assertEqual(action["actor"]["origin_node"], "sysbox_node.node_attack")
        self.assertEqual(action["tool"]["argv"][0], "curl")
        self.assertEqual(action["result"]["status"], "completed")

    def test_write_jsonl_appends_records(self) -> None:
        mod = load_module()
        with tempfile.TemporaryDirectory() as td:
            path = Path(td) / "records.jsonl"
            mod.write_jsonl(path, [{"a": 1}, {"b": 2}])

            lines = path.read_text().splitlines()
            self.assertEqual(len(lines), 2)
            self.assertIn('"a": 1', lines[0])


if __name__ == "__main__":
    unittest.main()
