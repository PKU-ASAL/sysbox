"""runner/agent.py — Agent-agnostic client abstraction.

Defines a Protocol (AgentClient) that any AI coding agent must satisfy,
plus a concrete OpenCodeClient implementation for opencode server mode.

Protocol flow:
  client = OpenCodeClient(base_url="http://172.20.0.10:4096")
  session_id = client.create_session()
  for event in client.send_prompt(session_id, "Attack the network"):
      print(event)  # ToolCallEvent(status="in_progress", command="nmap ...")

Adding a new agent (e.g. ACP-compliant):
  1. Implement AgentClient Protocol
  2. Pass as `client=` to the episode runner
  No changes to run_opencode.py needed.
"""

from __future__ import annotations

import json
import os
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from typing import Iterator, Protocol, runtime_checkable


# ── Data model ────────────────────────────────────────────────────────────────

@dataclass
class ToolCallEvent:
    """One bash tool-call lifecycle event emitted by the agent."""
    call_id:   str
    tool:      str            # e.g. "bash"
    status:    str            # "in_progress" | "completed" | "error"
    command:   str            # the shell command text
    start_ts:  int | None     # unix milliseconds, set when status=in_progress
    end_ts:    int | None     # unix milliseconds, set when status=completed/error
    output:    str = ""       # stdout/stderr preview
    error:     str = ""       # error message if status=error


# ── Protocol ──────────────────────────────────────────────────────────────────

@runtime_checkable
class AgentClient(Protocol):
    """Minimal interface every agent backend must implement.

    create_session() → session_id string
    send_prompt(session_id, text) → Iterator[ToolCallEvent]
      Blocks until the agent completes the prompt, yielding one event
      per tool-call state transition (in_progress, completed, error).
    """

    def create_session(self) -> str: ...

    def send_prompt(self, session_id: str, text: str) -> Iterator[ToolCallEvent]: ...


# ── OpenCode client ───────────────────────────────────────────────────────────

class OpenCodeClient:
    """AgentClient implementation for opencode server mode.

    opencode HTTP API (as of opencode v0.x):
      POST /session                           → {"id": "ses-xxx"}
      POST /session/:id/message               → blocks until done
      GET  /event   (SSE, project-scoped)     → stream of event objects

    SSE event schema (message.part.updated for tool calls):
      {
        "type": "message.part.updated",
        "properties": {
          "sessionID": "ses-xxx",
          "part": {
            "type":   "tool",
            "tool":   "bash",
            "callID": "toolu_xxx",
            "state": {
              "status": "running" | "completed" | "error",
              "input":  {"command": "nmap -sS 10.0.2.0/24"},
              "time":   {"start": 1700000000000, "end": 1700000005000},
              "output": "Starting Nmap ...",
              "error":  ""
            }
          }
        }
      }

    NOTE: The SSE schema was documented from opencode source/issues research
    but should be verified against a live instance.  If field names differ,
    only this class needs updating — the episode runner is unaffected.
    """

    def __init__(self, base_url: str, timeout: float = 3600.0, model: str = "") -> None:
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout
        self._default_model = model  # overridden by SYSBOX_MODEL env var at session creation

    # ── REST helpers ──────────────────────────────────────────────────────────

    def _request(self, method: str, path: str, body: dict | None = None, timeout: float | None = None) -> dict:
        url = self.base_url + path
        data = json.dumps(body).encode() if body is not None else None
        headers = {"Content-Type": "application/json", "Accept": "application/json"}
        req = urllib.request.Request(url, data=data, headers=headers, method=method)
        try:
            with urllib.request.urlopen(req, timeout=timeout or self.timeout) as resp:
                raw = resp.read().decode("utf-8", errors="replace")
                return json.loads(raw) if raw.strip() else {}
        except urllib.error.HTTPError as exc:
            body_bytes = exc.read() if hasattr(exc, "read") else b""
            raise RuntimeError(f"HTTP {exc.code} {method} {path}: {body_bytes[:300].decode(errors='replace')}")

    def create_session(self) -> str:
        # POST /session with empty body — opencode reads model from config.json.
        # Passing a "model" field causes HTTP 400.
        resp = self._request("POST", "/session", {}, timeout=10.0)
        session_id = resp.get("id") or resp.get("sessionId") or resp.get("session_id")
        if not session_id:
            raise RuntimeError(f"create_session: unexpected response {resp}")
        return session_id

    # ── SSE + prompt ──────────────────────────────────────────────────────────

    def send_prompt(self, session_id: str, text: str) -> Iterator[ToolCallEvent]:
        """Send prompt and yield ToolCallEvents via SSE event stream.

        Flow:
          1. Open GET /global/event SSE connection.
          2. POST /session/:id/prompt_async → 204 (non-blocking).
          3. Read SSE events in a background thread, put them on a queue.
          4. Yield ToolCallEvent for each completed/errored tool call.
          5. Stop when session.idle (or session.status{idle}) arrives for
             this session, or when the overall timeout expires.

        SSE event format (from opencode bus, /global/event):
          event: message
          data: {"directory":"/","project":"global","payload":{"id":"...","type":"<event_type>","properties":{...}}}
          (payload is extracted automatically; heartbeats omit directory/project)

        Relevant event types:
          message.part.updated   – tool call state transitions
          session.idle           – session finished (deprecated but still fired)
          session.status         – status update; stop when status.type=="idle"
        """
        import queue
        import sys as _sys
        import threading

        event_q: queue.Queue[dict | None] = queue.Queue()
        stop_event = threading.Event()

        def _sse_reader() -> None:
            url = self.base_url + "/global/event"
            req = urllib.request.Request(
                url, headers={"Accept": "text/event-stream", "Cache-Control": "no-cache"}
            )
            try:
                # 20s socket timeout — well above the 10s SSE heartbeat interval.
                with urllib.request.urlopen(req, timeout=20.0) as resp:
                    data_buf: list[str] = []
                    while not stop_event.is_set():
                        try:
                            raw = resp.readline()
                        except Exception:
                            break
                        if not raw:
                            break
                        line = raw.decode("utf-8", errors="replace").rstrip("\r\n")
                        if line.startswith("data:"):
                            data_buf.append(line[5:].strip())
                        elif line == "":
                            if data_buf:
                                data_str = "\n".join(data_buf)
                                data_buf = []
                                try:
                                    outer = json.loads(data_str)
                                    # /global/event wraps events: {"directory":...,"payload":{...}}
                                    event_q.put(outer.get("payload", outer))
                                except json.JSONDecodeError:
                                    pass
                            else:
                                data_buf = []
            except Exception:
                pass
            finally:
                event_q.put(None)  # sentinel: stream ended

        sse_thread = threading.Thread(target=_sse_reader, daemon=True)
        sse_thread.start()

        # Wait briefly for SSE to connect before firing the prompt.
        time.sleep(0.3)

        # Fire prompt asynchronously (returns 204 immediately).
        self._request(
            "POST",
            f"/session/{session_id}/prompt_async",
            {"parts": [{"type": "text", "text": text}]},
            timeout=10.0,
        )

        seen_call_ids: set[str] = set()
        t0 = time.monotonic()

        while True:
            elapsed = time.monotonic() - t0
            if elapsed > self.timeout:
                break

            try:
                payload = event_q.get(timeout=1.0)
            except queue.Empty:
                _sys.stdout.write(
                    f"\r  [thinking... {int(elapsed)}s, {len(seen_call_ids)} tools done]    "
                )
                _sys.stdout.flush()
                continue

            if payload is None:
                # SSE stream closed unexpectedly; stop.
                break

            ptype = payload.get("type", "")
            props = payload.get("properties", {})

            # Session completion signals.
            if ptype == "session.idle" and props.get("sessionID") == session_id:
                break
            if ptype == "session.status" and props.get("sessionID") == session_id:
                if isinstance(props.get("status"), dict) and props["status"].get("type") == "idle":
                    break

            ev = self._parse_sse_event(payload, session_id)
            if ev is None:
                continue
            if ev.call_id in seen_call_ids:
                continue
            if ev.status in ("completed", "error"):
                seen_call_ids.add(ev.call_id)
                _sys.stdout.write("\r" + " " * 60 + "\r")
                yield ev

        _sys.stdout.write("\r" + " " * 60 + "\r")
        stop_event.set()
        sse_thread.join(timeout=2.0)

    @staticmethod
    def _parse_sse_event(raw: dict, session_id: str) -> ToolCallEvent | None:
        if raw.get("type") != "message.part.updated":
            return None
        props = raw.get("properties", {})
        if props.get("sessionID") != session_id:
            return None
        part = props.get("part", {})
        if part.get("type") != "tool":
            return None

        state = part.get("state", {})
        status_raw = state.get("status", "")
        # opencode uses "running"; map to "in_progress" for our model.
        if status_raw == "running":
            status = "in_progress"
        elif status_raw in ("completed", "error"):
            status = status_raw
        else:
            return None

        timing = state.get("time", {})
        return ToolCallEvent(
            call_id=part.get("callID", ""),
            tool=part.get("tool", ""),
            status=status,
            command=state.get("input", {}).get("command", ""),
            start_ts=timing.get("start"),
            end_ts=timing.get("end"),
            output=(state.get("output") or "")[:500],
            error=state.get("error", ""),
        )

    # ── Health check ──────────────────────────────────────────────────────────

    def wait_ready(self, timeout: float = 30.0) -> bool:
        """Return True once the server answers any HTTP request, False on timeout."""
        url = self.base_url + "/"
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                req = urllib.request.Request(url, method="GET")
                urllib.request.urlopen(req, timeout=2.0).close()
                return True
            except urllib.error.HTTPError:
                return True   # any HTTP status means the server is up
            except Exception:
                time.sleep(0.5)
        return False

    # ── Transcript retrieval ──────────────────────────────────────────────────

    def get_transcript(self, session_id: str) -> list[dict]:
        """Fetch the full message transcript for a completed session.

        Returns a list of message dicts, each with:
          info:   {id, sessionID, role, time, agent, model, ...}
          parts:  [{type: "text"|"tool"|"reasoning"|"step-start"|"step-finish", ...}, ...]

        This is the single-source-of-truth record of the entire agent
        conversation — user prompt, reasoning, tool calls with full
        input/output/error, and assistant text responses.
        """
        resp = self._request("GET", f"/session/{session_id}/message", timeout=10.0)
        if isinstance(resp, list):
            return resp
        # Some versions wrap in a key.
        if isinstance(resp, dict) and "messages" in resp:
            return resp["messages"]
        return []
