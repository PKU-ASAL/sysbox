# SSH public workflow closure

Goal: close the public admission → Agent → credential-owning host → guest CAS
→ authenticated completion path. This executes the approved acceptance repair
plan; old file-copy mutations are not a compatibility interface.

The host owns durable intent and receipts. It verifies current authority before
dispatch, pins the canonical account and provider ID, and serializes each action
using a process-independent lock. Recovery observes historical guest evidence;
it never repeats an uncertain mutation. Corrupt records remain errors. A receipt
is signed once, saved before reporting, and retried unchanged until acknowledged.

## Execution checklist

- [x] Harden `pkg/hostsupervisor/ssh_guest.go` and add
  `ssh_guest_test.go`: revoked grants, damaged journals, mismatched provider
  identities, concurrent duplicate delivery, lost response/ack and cold recovery.
  Run `go test -race ./pkg/hostsupervisor -run SSHGuest -count=1`.
- [x] Use native SSHMutation and signed-grant RPCs in `protocol.go`, `client.go`
  and `server.go`; install a dedicated journal root and recovery worker in
  `cmd/sysbox/commands/supervisor_cmd.go`. Verify recovery through Server.
- [x] Replace old Agent relay with guest command decoding and canonical
  discovery; verify all signed command/session/attempt bindings and retain host
  ownership of completion. Test actual command dispatch and malformed grants.
- [x] Exercise public HTTP admission, durable leasing, Agent dispatch, host
  authorization, guest execution and authenticated completion in one integration
  test. Run the real provider/helper version where available. Do not substitute
  fixture-issued grants for the production issuer.
- [x] Remove obsolete production SSH mutation callbacks and update acceptance
  evidence. Run affected race suites and the full repository tests, then review.

Automatic sshd installation/reload and runtime resource migration remain separate
items in the broader SSH plan; this checklist closes the execution-path blockers
identified by the latest acceptance run.
