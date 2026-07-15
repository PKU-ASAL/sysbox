# Sysbox README and Overview Refresh Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give users a clear, accurate entry point to Sysbox and give technical evaluators a complete product overview grounded in checked-in HCL and acceptance evidence.

**Architecture:** `README.md` remains the user-facing root and leads with product definition, representative HCL, capabilities, quick starts, lifecycle commands, and explicit boundaries. A new `docs/overview.md` explains the complete model and guarantees, while `docs/README.md` becomes the stable navigation index. All claims and snippets derive from checked-in examples, CLI help, architecture contracts, and Batch 4/5 verification reports.

**Tech Stack:** Markdown, HCL examples, Go CLI, GNU Make, shell link and content checks.

## Global Constraints

- Documentation changes only; do not alter runtime, HCL schema, providers, or Web UI.
- Describe networking as IPv4-first and do not claim production IPv6 topology support.
- Describe only Docker, Firecracker, libvirt, and Linux networking capabilities present in the repository.
- Do not claim arbitrary guest operating systems, Terraform provider compatibility, or Terraform module compatibility.
- Link reliability and reset claims to the checked-in Batch 4 and Batch 5 acceptance evidence.
- Derive public HCL snippets from `examples/heterogeneous-matrix/field.sysbox.hcl`.

---

### Task 1: User-Facing README

**Files:**
- Modify: `README.md`
- Reference: `examples/heterogeneous-matrix/field.sysbox.hcl`
- Reference: `examples/two-networks/field.sysbox.hcl`

**Interfaces:**
- Consumes: checked-in CLI commands, Make targets, HCL resource names, and verification results.
- Produces: the primary user entry point and links consumed by `docs/README.md`.

- [x] **Step 1: Record the README facts that must remain true**

Run:

```bash
bin/sysbox reset --help
make help
bin/sysbox -f examples/two-networks/field.sysbox.hcl validate
bin/sysbox -f examples/heterogeneous-matrix/field.sysbox.hcl validate
```

Expected: reset help includes `--target`; Make help includes heterogeneous matrix/reset targets; both HCL files validate when their referenced environment artifacts are supplied by their existing acceptance path. If direct heterogeneous validation needs artifact environment variables, use the acceptance target rather than inventing placeholder paths.

- [x] **Step 2: Rewrite the README opening and product journey**

Replace the current contract-first opening with these sections in this order:

```markdown
# Sysbox

<literal product definition and bounded scope>

## What You Can Build
<representative heterogeneous HCL excerpt and link to the complete example>

## Core Capabilities
<compact capability table>

## Quick Start
<Docker-first path, then privileged heterogeneous path>

## Lifecycle
<validate, plan, apply, reset, targeted reset, output, state, destroy>

## Current Boundaries
<IPv4-first, tested Linux guests/providers, host privilege model>
```

Keep the existing detailed API, deployment, architecture, backend, configuration, resource, and repository sections below the new user journey. Correct stale artifact wording so images are described through explicit `kind`, `source`, `architecture`, `guest_family`, and optional digest rather than node-level `rootfs` or `qcow2` fields.

- [x] **Step 3: Validate README commands and links**

Run:

```bash
rg -n '^## ' README.md
rg -n 'reset --target|test-heterogeneous-reset|docs/overview.md' README.md
git diff --check -- README.md
```

Expected: the user journey appears before deep reference sections; reset and Overview links are present; no whitespace errors.

- [x] **Step 4: Commit the README entry point**

```bash
git add README.md
git commit -m "docs: present sysbox heterogeneous topology workflow"
```

---

### Task 2: Technical Product Overview

**Files:**
- Create: `docs/overview.md`
- Reference: `docs/architecture/*.md`
- Reference: `docs/verification/2026-07-13-batch4-network-acceptance.md`
- Reference: `docs/verification/2026-07-14-batch5-reset-acceptance.md`

**Interfaces:**
- Consumes: terminology established by the README and the architecture/verification contracts.
- Produces: the authoritative narrative overview linked from README and the docs index.

- [x] **Step 1: Write the Overview around stable public concepts**

Create `docs/overview.md` with these sections:

```markdown
# Sysbox Overview

## Purpose and Scope
## Topology Model
## Heterogeneous Providers
## Artifact and Guest Identity
## Networking and Guest Initialization
## Structured Guest Execution
## Lifecycle and Reset
## State, Plans, and Recovery
## Ownership and Destructive Safety
## CLI and Service Operating Models
## Verification Evidence
## Current Boundaries
## Where to Go Next
```

Explain logical versus external identity, provider-owned mechanics behind common contracts, state schema v6 strict rejection, whole and targeted reset, checkpoint recovery, and acceptance evidence. Link technical claims to the relevant architecture or verification document.

- [x] **Step 2: Check the Overview for unsupported claims and stale schema**

Run:

```bash
rg -n 'IPv6|Terraform|rootfs|qcow2|state schema|reset|six|6' docs/overview.md
rg -n 'docker_ref|node-level.*(rootfs|qcow2)' docs/overview.md
git diff --check -- docs/overview.md
```

Expected: IPv6 and Terraform appear only as explicit boundaries; rootfs/qcow2 appear as artifact kinds, not legacy node fields; state/reset/acceptance claims are present; legacy-field search returns no matches.

- [x] **Step 3: Commit the product overview**

```bash
git add docs/overview.md
git commit -m "docs: add sysbox product overview"
```

---

### Task 3: Documentation Navigation and Final Verification

**Files:**
- Modify: `docs/README.md`
- Modify: `README.md` only if final consistency checks expose a factual mismatch
- Modify: `docs/overview.md` only if final consistency checks expose a factual mismatch

**Interfaces:**
- Consumes: final README and Overview paths.
- Produces: a maintained documentation map and a verified documentation set.

- [x] **Step 1: Replace the minimal docs index with grouped navigation**

Organize `docs/README.md` as:

```markdown
# Sysbox Documentation

## Start Here
## Operate Sysbox
## Architecture Contracts
## Verification
```

Link `../README.md`, `overview.md`, `deployment.md`, `api.md`, `firecracker-artifacts.md`, every current file under `docs/architecture/`, and both current files under `docs/verification/`. State that `docs/superpowers/` contains implementation records rather than product documentation.

- [x] **Step 2: Verify every changed Markdown link resolves**

Run a shell loop that extracts relative Markdown destinations from the three changed files, strips anchors, resolves them relative to the source file, and fails for missing files:

```bash
for file in README.md docs/README.md docs/overview.md; do
  while IFS= read -r link; do
    target=${link%%#*}
    case "$target" in ''|http://*|https://*|mailto:*) continue ;; esac
    test -e "$(dirname "$file")/$target" || { echo "$file: missing $link"; exit 1; }
  done < <(sed -n 's/.*](\([^)]*\)).*/\1/p' "$file")
done
```

Expected: exit 0 with no missing-link output.

- [x] **Step 3: Run content consistency and repository gates**

Run:

```bash
rg -n 'Docker|Firecracker|libvirt|IPv4|reset --target|state schema v6' README.md docs/overview.md
! rg -n 'production-ready IPv6|arbitrary Terraform|all operating systems' README.md docs/overview.md
git diff --check
go test ./...
```

Expected: both documents describe the supported providers, IPv4 scope, targeted reset, and state contract consistently; forbidden overclaims are absent; tests and whitespace checks pass.

- [x] **Step 4: Request documentation-focused code review**

Ask the reviewer to check factual accuracy, navigation completeness, unsupported capability claims, command validity, and consistency with the Batch 4/5 verification reports. Resolve all Critical and Important findings before commit.

- [x] **Step 5: Commit the documentation index and final corrections**

```bash
git add README.md docs/README.md docs/overview.md docs/superpowers/plans/2026-07-15-readme-overview-refresh-plan.md
git commit -m "docs: organize sysbox documentation entry points"
```
