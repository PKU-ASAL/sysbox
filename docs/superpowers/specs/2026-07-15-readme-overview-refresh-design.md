# Sysbox README and Overview Refresh Design

Date: 2026-07-15

## Goal

Present Sysbox accurately as a usable declarative control plane for heterogeneous Linux experiment topologies. A new reader should quickly understand what Sysbox builds, see representative HCL, run a suitable example, and find the deeper contracts and acceptance evidence without reading internal planning documents.

## Audience

The documentation serves two audiences through separate entry points:

- `README.md` targets users and experiment-platform engineers evaluating or running Sysbox.
- `docs/overview.md` targets technical evaluators who need the complete resource model, lifecycle guarantees, provider boundaries, and verification evidence.

The documentation must remain useful to both local CLI users and users evaluating the optional API, Agent, and Web control plane.

## README Structure

The README opening will be reorganized around the user journey:

1. A literal one-paragraph product definition: Sysbox turns HCL into managed Docker, Firecracker, libvirt, and Linux networking resources.
2. A concise statement of the intended scope: reproducible Linux labs and experiment topologies, not a general cloud provisioning ecosystem.
3. A representative HCL excerpt showing an isolated IPv4 network with Docker, Firecracker, and libvirt nodes and explicit immutable artifact identity.
4. A capability summary covering topology resources, lifecycle operations, provider-owned guest initialization, structured execution, state and recovery, and optional service deployment.
5. A short quick-start path using an accessible Docker example, followed by the privileged heterogeneous acceptance path for hosts with KVM and libvirt.
6. Lifecycle examples for `validate`, `plan`, `apply`, whole-topology `reset`, targeted `reset --target`, `output`, `state`, and `destroy`.
7. Explicit current boundaries, including IPv4-first networking and the tested Linux guest/provider combinations.
8. Links to the Overview, deployment, API, artifact, architecture-contract, and verification documentation.

Existing detailed sections on deployment, architecture, backends, service configuration, repository layout, and APIs remain available. They may be reordered and corrected where Batch 4 or Batch 5 made existing wording stale, but this refresh will not turn the README into a complete schema reference.

## Overview Document

`docs/overview.md` will explain Sysbox through its stable public concepts:

- HCL resources and dependency graph.
- Docker, Firecracker, libvirt, and Linux network provider responsibilities.
- Explicit artifact kind, architecture, guest family, and immutable identity.
- IPv4 topology links and the reserved address-family extension boundary.
- Provider-owned guest network initialization through common capability contracts.
- Structured guest execution and provisioners.
- `validate -> plan -> apply -> reset -> destroy` lifecycle.
- Whole-topology reset and exact `sysbox_node.name` targeted reset.
- State schema v6, strict rejection of older state, stored-plan fingerprinting, checkpoint recovery, and ownership-safe cleanup.
- Local CLI and optional API/Agent/Web operating models.
- Real heterogeneous communication and reset acceptance evidence.

The overview will distinguish declared logical identity from replaceable provider external identity. It will explain that reset preserves topology intent, network identity, and immutable baseline identity while replacing mutable guest state.

## Documentation Index

`docs/README.md` will become a maintained navigation page with these groups:

- Start here: Overview and repository README.
- Operate: Deployment, API, and Firecracker artifact preparation.
- Contracts: resource addresses, typed state, stored plans, backend safety, secrets, and handler/driver boundaries.
- Verification: Batch 4 networking and Batch 5 reset acceptance reports.

Superpowers specs and plans remain implementation records and will not be presented as end-user documentation.

## Accuracy Rules

The refresh will use these claims only where the repository contains implementation and verification evidence:

- Sysbox can declare and manage Docker, Firecracker, and libvirt nodes on a shared isolated IPv4 topology.
- The heterogeneous acceptance proves all six directed communication paths.
- Reset supports three consecutive whole-topology cycles and targeted reset of each provider node while preserving logical network and artifact identity.
- Final destroy audits topology-owned residue.
- Runtime orchestration remains provider-neutral; provider-specific guest initialization and reset mechanics stay in providers.

The documentation will not claim:

- Production-ready IPv6 topology support.
- Arbitrary operating-system guest support.
- Compatibility with arbitrary Terraform providers or the Terraform module ecosystem.
- A security boundary stronger than the documented ownership checks and host privilege model.

## Examples and Commands

README snippets will be derived from checked-in examples rather than invented syntax. The full heterogeneous example remains `examples/heterogeneous-matrix/field.sysbox.hcl`; the README excerpt may omit repeated blocks but will link to the complete file.

The primary quick start will use a Docker-only example so it does not require KVM or libvirt. The heterogeneous path will clearly list its additional host requirements and use the existing Make targets.

## Verification

The documentation change is complete when:

- Every relative Markdown link resolves to a checked-in file.
- HCL files referenced by quick-start and heterogeneous examples pass the repository validation path.
- Every documented command and flag exists in the CLI or Makefile.
- README and Overview agree on providers, IPv4 scope, reset semantics, state compatibility, and acceptance results.
- Searches find no stale public image fields such as `rootfs` or `qcow2` presented as node-level image configuration.
- A documentation-focused code review finds no unsupported product claims or missing navigation links.

## Scope

This work changes documentation only. It does not add providers, alter HCL schema, change runtime behavior, create a schema reference generator, or redesign the Web UI.
