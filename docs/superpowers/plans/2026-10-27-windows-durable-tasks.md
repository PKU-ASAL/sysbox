# Windows durable task implementation plan

**Goal:** Carry signed persistent guest tasks over QGA without replaying raw
Windows commands after a lost response.
**Architecture:** Extract portable task wire types while preserving their JSON
and signature digests. A Windows helper owns a protected journal and per-task
Job Object. QGA invokes only this helper for durable operations. Ordinary tasks
use an explicitly configured non-administrator Windows account.
**Tech stack:** Go, Ed25519 grants, Windows Job Objects and process handles,
QGA bounded JSON calls, atomic durable records.

## Constraints

Task IDs/digests, boot IDs, epochs and cancellation signatures remain the same
protocol as Linux. No PID-only identity. No SYSTEM execution of arbitrary
commands. Unknown startup/result states are retained and never replayed.
Windows process creation must atomically join its Job Object (job-list process
attribute), so controller death between CreateProcess and Assign cannot leak an
unowned suspended process. Windows releases without this primitive fail closed.

## Tasks

- [x] Extract Request/Record into portable `pkg/taskprotocol` with digest parity
  tests; retain Linux guesttask aliases and all existing behavior.
- [x] Implement `pkg/windowstask` durable admission/observe/cancel/output/renew
  against a process-platform interface. Cover exact replay, response loss,
  changed boot, stale epoch, missing-task cancellation and journal corruption.
- [x] Implement Windows platform process handles, boot identity, monotonic
  expiry, restricted account logon, atomic Job Object assignment and bounded
  output in a detached supervisor. Cross-compile the helper.
- [x] Integrate QGA through the same transport task client with UUID validation;
  reject unsupported capabilities explicitly. Install pinned helper/trust
  configuration using Windows artifact tooling.
- [x] Validate normal, lost response, supervisor/guest reboot and late epoch
  with Windows-specific process tests and real isolated Windows VM. Record any
  unavailable real-platform evidence explicitly; do not substitute compilation.
- [x] Review, run affected Linux/full repository checks, commit and create the
  second PR against the first adaptation branch, so each review has one scope.

The user supplied the PKU disk share and source branches. Windows Server 2025
media was downloaded and pinned; the Member builder from
`feat/windows-server-2025-builders` is installing inside the isolated execution
VM. Image export and QGA passed. Real native normal/lost-response/supervisor
crash/cancel/renew/ACL tests passed in the first matrix. That run exposed a
firmware GUID that stayed constant across a cold boot; boot identity was replaced
with a protected volatile registry token, and legacy formats now block readiness.
The final guest reboot, signed unknown stop receipt and whole-host power-loss
recovery passed on the same retained guest. The first run's host journal fixture
was incorrectly under `/var/tmp`; continuation moved it under private `/var/lib`
and completed without replaying old work. Evidence is archived under
`scripts/acceptance/evidence/windows-tasks-20261027/`. Both isolated Windows
domains are powered off and their journals/disks retained.

Fixed acceptance ledger:

| Case | Actual evidence |
| --- | --- |
| Normal completion | Host QGA Start/Observe/output returned exit 7 and expected bytes |
| Lost response | Same signed Start replay returned original task; cold helper retained PID/result |
| Execution-time guest/host restart | Supervisor kill, guest power loss, whole execution VM power loss; unknown exit preserved, stop confirmed from valid evidence |
| Late old generation | Wrong generation, stale epoch, cancellation tombstone and old boot grants rejected |

Full Go tests, focused race, Windows/Linux vet, cross-compilation, documentation
checks and independent review passed. Windows Server 2025 amd64 was tested;
Windows 10 and other Windows releases are not claimed as separately accepted.

The QGA call envelope is explicitly limited to 32 KiB. Privileged ETW and durable
file publication are not enabled by this ordinary-task adapter. No raw SYSTEM
replay fallback is introduced. Installer upgrades require drain/reprovisioning;
shared images must not contain per-node identity or worker credentials.
