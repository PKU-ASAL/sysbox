# Stage 2 capability and fault ledger

This is closing stage 2, stacked PR #54, not the earlier GC stage2 selector.
Original wording remains authoritative in campaign.json (65 P0–P9 entries).
No omitted requirement, skipped test or previously successful source closes a cell.

## Delivered slices

| Capability | Implemented and exercised | Remaining boundary |
| --- | --- | --- |
| Public ownership transfer | Explicit CLI prepare/prepared/submit/abort; file/SQLite/Postgres public route contracts and cold retries; Docker/libvirt/Firecracker real host completion and crash chains | Deployed CLI/proxy/supervisor integration; fixtures supply admin subject and invoke host preparation directly |
| Transfer fault positions | Before effect, after ownership, after observation, before report, after state commit, after report, for all three providers | These are host process SIGKILL positions; do not imply every provider-internal write or API/helper process position |
| Libvirt attachment retry | Same logical attachment is idempotent; changed MAC, address, gateway, target or generation is rejected; persisted cold retry and corrupt duplicate record refusal | Existing corrupt duplicate records remain rejected; no silent rewrite |
| Docker NAT attachment | Real libvirt domain start/stop/restart with one interface; real Firecracker TAP, cold inventory and start preflight; stale target refusal | Diskless libvirt and manifest/TAP fixtures do not prove guest traffic or Firecracker boot |
| SSH access | Real sshd key rotation/delete, endpoint restrictions, administrator and independent managed access preservation; staged config validation, drop-in/reload/cold service recovery | Combined provider guest service and enclosing topology reset remain separate |

## Fixed next completion items

1. P7 enclosing lifecycle: three running provider guests on a Docker NAT network,
   pairwise traffic and outbound NAT, reset/recovery with refreshed target proof,
   old target/same-name replacement refusal, exact no-residue destruction. The
   existing heterogeneous matrix uses an isolated network, not Docker NAT.
2. Complete the 41 newly inventoried P0–P4/P7 requirement mappings. Attach precise
   implementation and test entries; retain pending current evidence. Do not
   report the current 65-entry inventory as 65 completed requirements.
3. Reconcile original section-5 failure contracts and P9.3 positions against
   existing API/Agent/helper tests. Every missing cell must identify operation,
   process, fault boundary and expected durable outcome before implementation.
   Current transfer SIGKILL evidence cannot substitute for guest transport loss,
   disk-full, database outage or provider-internal partial-write evidence.
4. Current-source strict campaign and final independent review for stage 2.
   Final latest-main integration, synchronized runtime images and actual upgrade
   rehearsal remain closing stage 3 (including P9.5/P9.6).

## Current evidence and limitations

Final real slice report: /tmp/sysbox-stage2-real-final-20261026/run.json.
The runner records exact test events, two repetitions, local/remote binary hashes,
helper hash and source hash; each group must pass without skips. This is a scoped
slice report, not the full P0–P9 campaign. Contract report:
/tmp/sysbox-stage2-p7-contract-final-20261026/run.json.
Full libvirt race: /tmp/sysbox-stage2-libvirt-race-20261026.log.

Earlier failed attempts remain preserved. SSH fixture execution required its
normal primary group plus a temporary docker supplementary group; the test does
not alter persistent user groups or the host's production sshd. Firecracker NAT
fixture initially used an invalid manifest directory; corrected to the existing
owner/node/generation layout, retaining production identity validation.
