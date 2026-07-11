# mixed capture example

This example runs a mixed Docker + Firecracker topology and captures a minimal
research episode:

```text
opencode actor on node_attack
  -> deterministic curl prompt
  -> target node_web
  -> Tetragon raw event capture
```

The capture script is intentionally outside sysbox core. It writes data under:

```text
.sysbox/runs/mixed/episodes/<episode_id>/
```

## Usage

```bash
sudo -E examples/mixed/lab.sh up
sudo -E examples/mixed-capture/capture_opencode_tetragon.sh
```

If your Tetragon installation needs a custom command, set:

```bash
TETRAGON_CAPTURE_CMD='tetragon --export-filename {output}' \
  sudo -E examples/mixed-capture/capture_opencode_tetragon.sh --episode ep-001
```

The `{output}` placeholder is replaced with the episode's
`tetragon.raw.jsonl` path.

## Outputs

```text
meta.json
episode_context.json
agent_actions.jsonl
transcript.json
tetragon.raw.jsonl
```

The first prompt is deliberately deterministic:

```text
Run exactly this command and report the output: curl -sS http://10.0.2.10/ | head
```

Start with this narrow case before adding free-form agent behavior or broader
normalization.
