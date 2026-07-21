# CLI Reference

本页对应当前 `sysbox --help`。具体版本以正在运行的 binary help 为准。

## Global Options

| Flag | Default | Meaning |
|---|---|---|
| `-f, --file PATH` | `field.sysbox.hcl` | HCL topology file |
| `--state PATH_OR_URL` | `.sysbox/runs/default/state.json` | State file or remote URL |
| `--backend URL` | empty | Backend URL; overrides `--state` |
| `--auto-approve` | false | Skip mutation confirmation |
| `--allow-unsafe-state` | false | Allow mutation without backend locking or CAS |

## Workspace And Validation

### `init`

Initialize a new Sysbox workspace.

### `validate`

```text
sysbox validate
```

Parse and validate HCL without contacting a provider.

### `plan`

```text
sysbox plan [--refresh]
```

Show ordered changes. `--refresh` probes existing resources for drift.

## Mutation

### `apply`

```text
sysbox apply [--refresh] [--target ADDRESS]
```

Provision planned resources. `--target` limits apply to one canonical resource address and its required dependencies; it is not a substitute for a converged full plan.

### `reset`

```text
sysbox reset [--target sysbox_node.name]
```

Recreate all managed guests or exactly one node from immutable baselines.

### `destroy`

```text
sysbox destroy
```

Tear down resources represented in state in reverse dependency order.

### `import`

```text
sysbox import ADDRESS EXTERNAL_ID --substrate NAME
```

Import an existing node through the resource handler and provider import capability.

## Runtime Control

### `pause` / `resume`

Pause or resume a node using its provider capability. These commands do not change desired topology.

### `show`

Print one resource's state details as JSON.

### `output`

Print evaluated topology output values.

## State

```text
sysbox state list
sysbox state show ADDRESS
sysbox state get ADDRESS[.ATTRIBUTE]
sysbox state mv SOURCE DESTINATION
sysbox state rm ADDRESS
```

- `list` lists canonical addresses.
- `show` prints full public instance attributes.
- `get` prints a resource or one attribute.
- `mv` renames logical state identity without touching the external object.
- `rm` forgets state and does **not** destroy the real object.

Quote addresses containing string keys:

```bash
sysbox state show 'module.lab.sysbox_node.web["blue"]'
```

## Service Commands

### `serve`

Start the HTTP API server using the selected Sysbox config.

### `agent`

Manage or run the host Agent. Use `sysbox agent --help` for the version-specific registration and execution subcommands.

## Other Commands

- `version [--json]`: build version, commit, time and Go version.
- `completion`: shell completion generation.
- `help`: command help.

## Safety Notes

- Mutation rejects backends without locking and CAS unless `--allow-unsafe-state` is explicit.
- `state rm` is not cleanup; it removes the ownership record needed for safe destroy.
- A stale stored plan or changed state serial fails before provider mutation.
- Configuration diagnostics, state conflicts and provider failures all return non-zero status; automation should use structured API diagnostics where available rather than matching prose.
