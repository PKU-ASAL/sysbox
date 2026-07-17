# Docker Launch Overrides and Actor Boundary Design

## Goal

Make Docker-backed Sysbox nodes faithfully represent OCI image launch
overrides while removing ACP-specific actor orchestration from the Sysbox core
resource model.

This change prepares Sysbox for topology definitions migrated from Docker
Compose and containerlab without assigning container-only launch semantics to
libvirt or Firecracker nodes.

## Scope

This design makes two related changes:

1. add optional Docker provider `entrypoint` and `command` overrides; and
2. remove `sysbox_actor` and its ACP metadata from the Sysbox configuration,
   planning, runtime, state, API, examples, and documentation surfaces.

The change does not introduce a generic workload or service resource. Scenario
roles, playbooks, entry points, ACP processes, and result validation belong to
the SysField or `sysbox-topology` scenario layer.

## Model Boundary

`sysbox_node` remains substrate-neutral. Its common fields describe compute
identity, image identity, guest family, resources, environment, network
attachments, ports, routes, connections, provisioners, dependencies, and
lifecycle policy.

Container launch configuration is not common node configuration. Docker uses
OCI `ENTRYPOINT` and `CMD`; a VM boots a kernel and init system and may consume
cloud-init or a provider-specific configuration drive. Sysbox therefore must
not add `entrypoint` or `command` to the top-level `sysbox_node` schema.

Docker launch overrides live in the existing provider-owned block:

```hcl
resource "sysbox_node" "mongo" {
  substrate = substrate.docker.local
  image     = sysbox_image.mongo.id

  provider "docker" {
    command = ["mongod", "--bind_ip", "0.0.0.0"]
  }
}
```

When both values need replacement:

```hcl
provider "docker" {
  entrypoint = ["/usr/local/bin/lab-entrypoint"]
  command    = ["--listen", "0.0.0.0"]
}
```

Libvirt and Firecracker keep independent provider schemas for their boot
contracts. Guest configuration after boot continues to use structured
provisioners and transport capabilities.

## Docker Launch Contract

The fields use OCI/Docker array form only. Shell-form strings are rejected.
Sysbox never inserts an implicit shell.

The effective launch configuration is:

```text
effective entrypoint = declared entrypoint when present, otherwise image ENTRYPOINT
effective command    = declared command when present, otherwise image CMD
effective argv       = effective entrypoint followed by effective command
```

Presence and value are distinct. The decoder must preserve all three states:

| HCL state | Meaning |
|---|---|
| attribute omitted | inherit the corresponding image value |
| non-empty array | replace the corresponding image value |
| explicit `[]` | clear the corresponding image value |

An empty effective argv is valid for creating a provisionable idle node. In
that case Sysbox does not start an image entry process after provisioning.

The provider decoder must not represent these attributes as plain slices whose
zero value conflates omission with an explicit empty array. It must retain an
explicit presence bit or equivalent typed optional value.

## Lifecycle

Sysbox retains its staged Docker lifecycle:

1. resolve and verify the immutable image;
2. inspect the image's original entrypoint and command;
3. compute the effective launch configuration;
4. create an idle container so network wiring and provisioners can converge;
5. attach declared networks and apply provider configuration;
6. run structured provisioners;
7. start the effective launch argv as the managed image-entry process.

The launch process continues to use direct argv execution with no implicit
`sh -c`. Errors must identify whether image inspection, override decoding, or
image-entry startup failed without logging secret environment values.

Changing either launch override changes the desired node configuration and
plans node replacement. The first implementation does not attempt an in-place
process restart because doing so would leave image, provisioner, and process
state with ambiguous ownership.

## Plan, State, and Reset

Plans retain the declared provider configuration in the node's typed desired
value. Secret resolution rules remain unchanged.

Docker provider state and operation checkpoints store the effective entrypoint
and command required to recover or reset a managed node. They do not store a
shell-rendered command. State continues to store the verified image identity,
so reset cannot silently adopt launch metadata from a different image.

Reset recreates the node from its immutable image, recomputes the effective
launch configuration from the current declared override and the verified image
metadata, restores network attachments, runs the normal provisioning sequence,
and starts the same effective argv. A mismatch between the stored image
identity and inspected image is an error before destructive replacement.

Refresh observes container and attachment health. It does not attempt to infer
the original argv from a mutable process list. Declared launch changes are
detected through desired configuration diffing, while missing image-entry
process health remains a runtime health concern.

## Removing `sysbox_actor`

The current actor resource is an ACP application integration rather than an
infrastructure primitive:

- an internal actor starts a background command inside a node;
- an external actor duplicates node/container creation;
- `entry_points` and `acp_url` are scenario-runner metadata; and
- attacker/victim roles are properties of a scenario, not infrastructure.

The following production surfaces are removed:

- `ActorConfig` and `sysbox_actor` HCL decoding;
- actor graph dependencies and desired schema entries;
- actor resource handler, runtime create/delete paths, and checkpoint recovery;
- actor state attributes including command, entry points, PID, and ACP URL;
- topology API DTO fields and operations that exist only for actor resources;
- actor examples, documentation claims, and architecture diagrams.

Ordinary attacker machines remain `sysbox_node` resources. An external scenario
runner can execute playbook steps through the node connection/session APIs or
other explicit SysField integrations. `sysbox-topology` owns scenario role
mapping and endpoint metadata outside the HCL infrastructure graph.

No replacement `sysbox_workload`, generic background process, or ACP plugin is
introduced in this change. Such a resource requires its own design covering
supervision, readiness, restart policy, logs, health, and cross-guest service
managers.

## Compatibility and Release

Sysbox is currently at v0.1.0 and explicitly rejects incompatible state schema
versions. This change is intentionally breaking:

- HCL containing `sysbox_actor` fails validation with a diagnostic directing
  users to define an ordinary node and move role/ACP configuration to their
  scenario layer.
- State containing actor resources is rejected without mutation. Users destroy
  actor-bearing v0.1.0 workspaces with the v0.1.0 binary before upgrading, then
  recreate them with the new release.
- No state migration, compatibility actor handler, or silent actor omission is
  provided.

The Docker launch fields require a new Sysbox release. `sysbox-topology` must
update its release lock before committing a topology that depends on them.

## Validation and Testing

Configuration tests cover:

- omitted, non-empty, and explicitly empty launch arrays;
- rejection of string/shell-form launch values;
- rejection of Docker launch fields on non-Docker provider blocks;
- validation failure for `sysbox_actor` with the migration diagnostic.

Docker provider and runtime tests cover:

- inheritance of both image values;
- command-only, entrypoint-only, and combined overrides;
- explicit clearing of either image value;
- direct argv preservation, including whitespace and metacharacters;
- staged provisioning before image-entry startup;
- launch changes producing replacement plans;
- state/checkpoint round trips of effective launch values;
- reset preserving the effective launch contract;
- empty effective argv leaving an idle managed node;
- image-entry startup failure producing a failed operation and recoverable
  checkpoint.

Removal tests and repository audits prove that production code, public HCL
examples, API contracts, and architecture documentation contain no
`sysbox_actor`, `ActorConfig`, `entry_points`, or `acp_url` surface. Historical
design documents may retain those terms as historical records.

An end-to-end Docker acceptance uses a small image with known image defaults
and verifies inheritance, override, reset, repeated no-op plan, and destroy.
After the new release is locked by `sysbox-topology`, the migrated
CVE-2019-10758 range validates the command-only override with a real MongoDB
node.

## Completion Criteria

The change is complete when:

1. Docker node launch overrides have deterministic omitted/replace/clear
   semantics and survive plan, apply, recovery, and reset;
2. libvirt and Firecracker public node schemas gain no container launch fields;
3. actor orchestration and ACP metadata are absent from active Sysbox product
   surfaces;
4. existing non-actor topology tests remain green;
5. the Docker launch acceptance passes through apply, reset, no-op plan, and
   destroy; and
6. release documentation identifies the actor removal and the required
   workspace recreation procedure.
