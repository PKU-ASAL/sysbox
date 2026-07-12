# Resource Addresses

Sysbox uses one canonical address across HCL expansion, graph, plan, state,
checkpoint, CLI, API, and logs.

```text
sysbox_node.web
sysbox_node.web[0]
sysbox_node.web["blue"]
module.lab.sysbox_network.dmz
module.segment["red"].sysbox_node.target[1]
```

String keys are JSON-quoted. Module paths and keys are structural and are never
flattened with underscores. CLI commands accept the complete address:

```bash
sysbox state show 'sysbox_node.web[0]'
sysbox state get 'module.lab.sysbox_node.web["blue"].primary_ip'
sysbox state mv 'sysbox_node.web[0]' 'sysbox_node.web[1]'
sysbox state rm 'module.lab.sysbox_network.dmz'
```

Malformed addresses are rejected. State schema v3 stores canonical addresses.
State v2 and older are rejected without mutation: destroy the old lab with its
original Sysbox binary, then recreate it with the current binary.
