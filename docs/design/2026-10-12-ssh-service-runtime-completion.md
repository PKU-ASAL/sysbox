# Managed SSH service and runtime completion

This continues the approved P7 design. Public mutation dispatch is accepted on
Docker; runtime and live sshd management are separate, unfinished boundaries.

## Service deployment

Use an administrator-enrolled OpenSSH configuration, an explicit final Include
for a Sysbox-only file, a trusted helper executable, connection probes and an
explicit service reload implementation. Do not infer a distribution or overwrite
third-party AuthorizedKeysCommand. The first supported layout has one final
Include and no other Includes; unsupported layouts must be enrolled explicitly.

The installer serializes with a process-independent lock, validates the complete
staged configuration, records intent before publishing the drop-in, then validates
the installed configuration, reloads and checks service readiness. A private
journal pins the main config, profile and old/new drop-in digests. Interrupted
publication/reload is recovered from those exact bytes. Foreign edits, symlinks,
bad modes and damaged journals fail closed. Administrator key files are preserved.
Reload success plus readiness is evidence of deployment, not a proof of every
possible administrator Match condition; probes remain an explicit enrollment
requirement.

## Runtime integration

The runtime must discover current configuration using node UID/generation and
the canonical account; persist a versioned access handle; and perform exact CAS
mutations with stable action IDs. Read distinguishes historical completion from
current configuration. Delete uses the persisted revision and never a file path.

A remote Run owns topology authority, while an SSH mutation targets its node.
Derived SSH grants, signed child completion, parent cleanup and recovery must be
implemented together. Local CLI needs an explicit local authority path. Neither
an unsigned SPI call nor a CopyFile fallback is an acceptable implementation.

## Execution and evidence

- [x] Add a durable sshd installer and regression tests for conflict, publication,
  reload failure, cold recovery and administrator edits.
- [x] Add the trusted guest service adapter and administrator helper commands;
  exercise actual sshd validation/reload and login behavior in isolation.
- [x] Add derived topology-Run SSH authority, child receipts and recovery; migrate runtime
  Read/Create/Delete and remove the old file path implementation.
- [ ] Update the two-VM lab and run cross-host public and runtime SSH lifecycle
  acceptance, including restart and lost completion. Record actual evidence.
- [ ] Run affected race suites, full repository tests and independent review.

No compatibility decoder or fallback will be added for old runtime SSH state.

## Evidence and remaining boundaries

Runtime now persists schema 2 access handles and node identity, uses live Inspect
for Read, and performs exact-revision CAS deletion without needing old HCL. Lost
create replies recover through the original action identity without mutation
replay. Canonical connections retain declared SSH/vsock selection, host key trust
and secret references. Libvirt forwards managed SSH through its domain fence.

The real Docker topology-Run test creates node and SSH together, loses completion
acknowledgement, reconstructs API/Agent/host, observes the original resources and
destroys them with a signed cleanup receipt. This passed under race. SQLite file
creation is now private before opening the database, matching host trust checks.

The two-VM service acceptance runs the real systemd adapter and current helper in
the execution VM; the control VM authenticates with a dedicated administrator key
before installation and after install/recovery. Main config is unchanged and the
temporary service is stopped. This is service-only acceptance, not a combined
cross-host API/Agent managed-key lifecycle.

Local CLI authority/bootstrap is now implemented; see
[local-ssh-authority.md](../operations/local-ssh-authority.md).
Still open: remote resource-targeted Run child authority;
automatic enrollment/provisioning of the trusted service profile in managed
images; and combined two-VM API/Agent SSH lifecycle/login fault acceptance. The
service profile is intentionally administrator-controlled and is not inferred or
overwritten by an unprivileged mutation request.
