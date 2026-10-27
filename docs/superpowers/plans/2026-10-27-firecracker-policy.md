# Firecracker durable policy implementation plan

**Goal:** Enable Firecracker Policy through authenticated durable guest tasks.
**Architecture:** Reuse guesttask and host Run children; add a typed, privileged
built-in with exact digest binding and a reboot-stable guest policy journal.
**Tech stack:** Go, signed execution grants, cgroup v2, vsock, nftables.

## Constraints

Ordinary tasks remain unprivileged. Observation is read-only. Exact node and
policy generations must match. Failed/unknown operations are never replayed as
successful. Acceptance uses isolated hosts only.

## Tasks

- [x] Add guest-local policy request validation and journal in
  `pkg/provider/network/guest_policy.go`. Exercise apply/delete retry, durable
  reconstruction after a lost response, simulated guest kernel reset, and old
  generation replay in `guest_policy_test.go`. First tests must fail before
  implementation. Use the existing rule compiler and ownership marker parser.
- [x] Add reserved built-in task validation in `pkg/guesttask`, with request
  digest and grant scope checks. Launch only the current installed executable
  for the privileged helper, under the existing cgroup guard. Add negative
  tests for caller-selected paths, environment, scope and unsigned requests.
- [x] Add helper entry point and read-only inspection in `cmd/sysbox-init` and
  the existing task RPC. Reuse the public task record and output contract.
- [x] Implement Firecracker Apply/Observe/Delete/Discover with manifest identity
  checks, canonical node scope and exact policy digest. Register Policy only
  after protocol wiring is complete.
- [x] Run focused fault tests, real isolated nft/guest acceptance, full Go tests,
  go vet and docs checks. Review, commit, push and create PR against dev with
  four-case evidence and explicit limits.

Commands: `GOCACHE=/tmp/sysbox-gocache go test ./pkg/provider/network
./pkg/guesttask ./pkg/transport ./pkg/provider/firecracker ./cmd/sysbox-init`;
then `go test ./...`, `go vet ./...`, `make docs-test` with the same cache.

The second PR starts from this implementation and receives its own Windows
platform plan before modifications to Windows process lifecycle code.

## Acceptance evidence (2026-10-27)

| Case | Evidence | Result |
| --- | --- | --- |
| Normal completion | Real signed built-in task, private cgroup, nft apply/readback/delete | Passed |
| Lost response | Reconstruct provider and resend exact request; guest task and policy journal deduplicate | Passed |
| Guest restart during operation | Whole microVM stopped while nft task is blocked; new boot restores completed policy using new task identity; interrupted outcome is not success | Passed |
| Host restart during operation | Dedicated execution VM powered off with QMP and pidfd exit proof; same disk rebooted; signed guest task inspected offline and retained unresolved | Passed |
| Delayed generation | Older apply/delete rejected after replacement; deleted generation cannot resurrect | Passed |
| Failed delete recovery | Same-attempt retry retains failure; new authorized Run settles deleting tombstone; observation verifies absence | Passed |

Logs: `/tmp/sysbox-policy-acceptance/task.log`,
`/tmp/sysbox-policy-acceptance/firecracker-nft-v3.log`,
`/tmp/sysbox-policy-acceptance/host-reboot.log`.
Host power proof: lab `sysbox-p9-962db2d23380`, request
`2bb89a7b30bd4fcdab669a17cefb62c8`; proof SHA256
`cfafe0f753b3c754326fbe65f1d69089e4f2d5b21c815b8aa721125898560405`.
The interrupted test disk and task record remain retained, not replayed or deleted.

Acceptance kernel SHA256:
`9ff2bb190f208dd56c5cbfc3bcbb592db22f3514a2905b1a31b49f0eb7162a96`.
Kernel config SHA256:
`72eca05f8ceaeefe30de973eaf24448d90316090f63a86a8d06e559a3aff496e`.
The old cached kernel failed with unsupported Netfilter netlink; it is not an
accepted guest firewall artifact. Use the checked-in kernel build recipe and
install nft userspace when preparing guest artifacts.

All Go packages, focused race tests, go vet, and docs checks passed. The matrix
uses provider/vsock integration with a test grant issuer. Production host Run
child admission/receipt logic is reused and covered by existing host tests;
this does not claim a separate new CyberField end-to-end deployment.
