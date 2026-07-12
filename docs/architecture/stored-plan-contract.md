# Stored Plan Contract

A plan contains one ordered `actions` list. Apply executes that list and does
not recompute side indexes.

```text
create  read  no-op  replace  delete  unknown
```

`replace` deletes the prior object and then creates the desired object. There
is no `update` until a handler implements real in-place update. `unknown` cannot
be applied. `lifecycle.prevent_destroy` makes delete or replacement planning
fail instead of silently producing a partial topology.

Stored plans bind the exact HCL bytes, state lineage and serial, schema
versions, selected drivers, artifact digests, and non-secret variable digest.
Apply compares every field before invoking a provider. Any mismatch returns a
field-specific stale-plan error without external mutation.

```json
{
  "address": "module.lab.sysbox_node.web[\"blue\"]",
  "action": "replace",
  "reason": "desired configuration changed; replacement required"
}
```
