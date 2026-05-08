# sysbox Gap Analysis — 2026-05-08

## 1. Phase 1 Checklist (Tasks 0–19)

| Task | Description | Status |
|---|---|---|
| 0 | Project scaffolding (go.mod, Makefile, README, .gitignore) | **DONE** |
| 1 | State data structures (`State`, `Resource`, marshal/unmarshal) | **DONE** |
| 2 | State Manager (Load/Save with flock + atomic rename) | **DONE** |
| 3 | HCL schema Go structs | **DONE** (schema diverges: LinkConfig uses `hcl:"link,block"` vs plan's `hcl:"links,optional"`) |
| 4 | HCL parser (`ParseFile`, `DecodeResource`) | **DONE** + bonus `BuildEvalContext` (real HCL traversal resolution) |
| 5 | Resource DAG (Kahn topo sort, cycle detection, reverse walk) | **DONE** |
| 6 | Substrate interface + registry | **DONE** + bonus `NodeStatus()` method |
| 7 | Docker `PrepareImage` (pull + inspect) | **DONE** |
| 8 | Docker node lifecycle (Create/Start/Stop/Destroy/Exec) | **DONE** + bonus `Sysctls` support for IP forwarding |
| 9 | Network provider: netns + bridge | **DONE** |
| 10 | Network provider: veth pair | **DONE** |
| 11 | Docker `AttachNIC` (veth move from network netns → container netns) | **DONE** (more correct than plan: handles source-netns parameter) |
| 12 | Runtime plan (graph ⊕ state diff) | **DONE** + bonus `Change` field for drift |
| 13 | Runtime Apply + Destroy executors | **DONE** + bonus `sysbox_router` and `sysbox_firewall` handlers |
| 14 | CLI root + `init` | **DONE** |
| 15 | CLI `plan` / `apply` / `destroy` | **DONE** + bonus `--refresh` flag (drift-triggered re-create) |
| 16 | CLI `state list` / `show` / `output` | **DONE** |
| 17 | Example HCL files | **DONE** (both hello-world and two-networks, plan only required hello-world) |
| 18 | E2E tests | **DONE** (helloworld + twonetworks including drift recovery test) |
| 19 | README + docs | **DONE** |

**All 20 tasks are complete.** Several ship features beyond plan scope (EvalContext, drift refresh, router/firewall, two-networks E2E).

---

## 2. Correctness Bugs

**Bug 1 — Veth name collision on long node names** (`pkg/runtime/executor.go`, `wireLink`)
```
hostEnd := fmt.Sprintf("vh-%s-%d", nodeName, idx)
if len(hostEnd) > 15 { hostEnd = hostEnd[:15] }
```
For `nodeName = "abcdefghijk"` (11 chars), NIC 0 and NIC 1 both truncate to `"vh-abcdefghijk-"`. `netlink.LinkAdd` will fail with "file exists" on the second NIC. Any node name > 10 chars with 2+ links hits this.

**Bug 2 — Firewall `src_net` silently widened** (`pkg/provider/network/firewall.go`, `ApplyFirewall`)
```go
if r.SrcNet != "" {
    fmt.Printf("[firewall] warning: src_net %q not implemented in Phase 1, skipping match\n", ...)
}
```
A rule `src_net = "10.0.2.0/24", action = "drop"` silently becomes a rule that drops traffic from **all** sources. This is a security correctness failure — rules are less restrictive than declared.

**Bug 3 — Dead fields in `VethSpec`** (`pkg/provider/network/veth.go`)
`VethSpec.GuestIP` and `.Gateway` are set in `wireLink` but never read inside `CreateVethPair`. IP/gateway configuration only happens in `nic.go`'s `attachVethToContainer`. No runtime breakage, but the dead parameters are confusing and could mask future misuse.

---

## 3. Feature Gaps

### a) Networking
- **No DNS**: containers use raw IPs; no built-in name resolution between nodes.
- **NAT requires iptables image**: `two-networks/field.sysbox.hcl` uses `alpine:latest`, which doesn't ship iptables. Router NAT silently fails; user sees no error.
- **`src_net` unimplemented**: IP-source filtering in `sysbox_firewall` rules is a no-op (Bug 2 above).
- **No traffic shaping / packet loss injection**: essential for adversarial lab realism.
- **`br_netfilter` assumption**: nftables `FORWARD` hooks on a bridge netns only fire if `br_netfilter` is loaded on the host. No check or documentation.

### b) Security / Isolation
- **`NET_ADMIN` without `--cap-drop ALL`**: containers get full capability baseline + NET_ADMIN. A compromised container can re-configure host networking.
- **No seccomp / AppArmor profiles**: no syscall filtering.
- **`sysbox_ssh_access` parsed but not implemented**: executor silently warns and removes from state. Users declaring SSH access get no error, just a ghost resource.
- **No user-namespace mapping**: all containers run as host root.

### c) Usability
- **No root check**: if `apply` is run without sudo, it fails deep inside netlink with a cryptic `operation not permitted`; no top-level hint.
- **No confirmation prompt**: `apply` executes immediately after printing the plan. Terraform-style `yes/no` prompt absent.
- **Container name collisions across fields**: `sysbox-<node_name>` is global in the Docker daemon. Two simultaneous fields with the same node name will collide.
- **No `validate` command**: users can't syntax-check HCL without a full apply attempt.
- **`--file` default relative path**: `field.sysbox.hcl` resolves from CWD, not from the binary location; running from a different directory silently fails.

### d) Robustness
- **No rollback on partial apply failure**: if node 3 of 4 fails, state holds 3 partial resources and the field is left in a broken half-up state. There is no automatic cleanup.
- **Orphan containers on mid-apply crash**: a container created in Docker between `CreateNode` and `StartNode` (or before state is written) is invisible to subsequent `destroy`.
- **No operation timeout**: `docker pull` and `ContainerCreate` calls have no deadline; a slow registry or hung daemon blocks forever.
- **No graceful Ctrl+C**: no `signal.NotifyContext`; interrupt mid-apply leaves resources orphaned.

---

## 4. Quick Wins (≤50 lines each)

1. **Root check in `apply_cmd.go`** (~5 lines): `os.Getuid() != 0` → print `"sysbox apply requires root (netns/netlink); re-run with sudo"` and exit 1.
2. **Fix veth name collision** (`executor.go`, ~10 lines): use `fmt.Sprintf("vh-%x%d", fnv32(nodeName), idx)[:15]` — deterministic hash avoids truncation collisions.
3. **Implement `src_net` in firewall** (`network/firewall.go`, ~30 lines): add `expr.Payload` for IPv4 source address + `expr.Cmp` against the parsed net mask; straightforward nftables extension.
4. **`sysbox validate` command** (`commands/validate_cmd.go`, ~20 lines): `ParseFile` + `BuildEvalContext` + `buildGraph` + `TopoSort`; print `"OK"` or render HCL diagnostics without touching providers.
5. **Plan confirm prompt** (`apply_cmd.go`, ~10 lines): after printing plan, prompt `"Apply? (yes/no): "` unless `--auto-approve` is given; prevents accidental destructive runs.
6. **Field-scoped container names** (`executor.go`, ~5 lines): append a short hash of `flagStateFile` to `sysbox-<node>` to avoid cross-field Docker name collisions.
