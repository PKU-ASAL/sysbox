# Local CLI SSH authority

This implements the remaining local CLI branch of the approved SSH migration.
The CLI is the credential-owning local operator. It must use explicitly enrolled
issuer and host signing keys; keys are not generated or rotated per invocation.

The local execution bridge prepares authorization after acquiring its existing
topology execution lock. Preparation is needed only when the graph or persisted
state contains managed SSH access. It constructs the existing API service on the
same canonical backend and a private host SSH journal. The API has no listener:
an in-process HTTP transport lets the existing authority client invoke the exact
signature, current-source and completion checks used by remote execution.

Each local SSH action becomes a normal persistent NodeOperation+command. The
existing lease, session, grant issuer, SSHGuestDispatcher and authenticated host
completion implement the mutation. Node scope and account come from canonical
state. The controller rejects a topology assigned to another agent and cannot
substitute an alternate ledger for configured SQL/remote storage. Explicit
private signing keys and a guest already enrolled with the matching issuer are
prerequisites; image/service enrollment remains a separate next step.

Preparation opens the stable topology-specific local agent's new process session
and recovers the existing SSH journal before allowing runtime effects. Recovery
observes unknown intents and reports original receipts without replay. The CLI
does not sign success for an absent guest record. Multiple CLI processes remain
serialized by the canonical topology execution lock, not an in-memory mutex.

## Implementation and validation

- [x] Add local bridge execution-context preparation under the topology lock,
  covering apply, destroy and reset and preserving no-op behavior without SSH.
- [x] Add the local API/host adapter using existing admission, grant and receipt
  paths. Validate keys, canonical storage, placement and exact action identity.
- [x] Wire CLI commands using service config and their actual state/backend.
- [x] Test missing/mismatched keys before effects, signed mutation/completion,
  cold receipt recovery, stale generation rejection and actual CLI apply/destroy.
- [ ] Run affected race/full tests, independent review and commit the result.

No unsigned transport call, old file path, per-process authority ledger or
compatibility state decoder is permitted.

Explicit `init --authority-dir` bootstraps private keys and configuration using
same-parent staging and rename-without-replacement. The file authority root remains
the service-configured directory even with a custom `--state` path. Local actors
remain offline and cannot attract remote work. Authenticated host completion
precedes command retirement; unknown receipts remain pending.

Embedded API construction skips API-restart console/Run recovery, including when
constructed as an execution bridge. It must not mark another process's live
console lost. Tests reproduce and protect that boundary. The local preparation
hook closes its temporary repository and holds canonical execution exclusion.
