BINARY        := bin/sysbox
INIT_DIR      := pkg/provider/firecracker/initbin
INIT_AMD64    := $(INIT_DIR)/sysbox-init.linux-amd64.bin
INIT_ARM64    := $(INIT_DIR)/sysbox-init.linux-arm64.bin
GO            := $(shell which go 2>/dev/null || echo /usr/local/go/bin/go)
GOFLAGS       := CGO_ENABLED=0
# Default build arch matches the host so 'make build' is fast on either
# amd64 or arm64. Use 'make build-init-all' to cross-compile both.
HOST_ARCH     := $(shell $(GO) env GOARCH)
INIT_DEFAULT  := $(INIT_DIR)/sysbox-init.linux-$(HOST_ARCH).bin

.DEFAULT_GOAL := help

# ── Help ──────────────────────────────────────────────────────────────────────

.PHONY: help
help:
	@echo ""
	@echo "Usage: make <target>"
	@echo ""
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z0-9_-]+:.*##/ { printf "  %-28s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
	@echo ""

# ── Build ─────────────────────────────────────────────────────────────────────

.PHONY: build
build: build-init ## Compile bin/sysbox (auto-builds embedded sysbox-init for host arch)
	$(GOFLAGS) $(GO) build -o $(BINARY) ./cmd/sysbox

.PHONY: build-init
build-init: $(INIT_DEFAULT) ## Cross-compile sysbox-init for the host arch only

.PHONY: build-init-all
build-init-all: $(INIT_AMD64) $(INIT_ARM64) ## Cross-compile sysbox-init for amd64 AND arm64

INIT_SRCS = cmd/sysbox-init/main.go cmd/sysbox-init/network.go cmd/sysbox-init/server.go cmd/sysbox-init/sensor.go

$(INIT_AMD64): $(INIT_SRCS)
	rm -f $@
	GOOS=linux GOARCH=amd64 $(GOFLAGS) $(GO) build -ldflags="-s -w" -o $@ ./cmd/sysbox-init

$(INIT_ARM64): $(INIT_SRCS)
	rm -f $@
	GOOS=linux GOARCH=arm64 $(GOFLAGS) $(GO) build -ldflags="-s -w" -o $@ ./cmd/sysbox-init

# ── Test ──────────────────────────────────────────────────────────────────────

.PHONY: test
test: ## Run unit tests (no Docker required)
	$(GO) test ./...

.PHONY: test-e2e
test-e2e: build ## Go topology tests: apply/route/drift/destroy (requires Docker + root)
	sudo -E "$(GO)" test -tags e2e -v -count=1 ./tests/e2e/... -timeout 120s

# ── Code quality ──────────────────────────────────────────────────────────────

.PHONY: fmt
fmt: ## Run go fmt
	$(GO) fmt ./...

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: lint
lint: fmt vet ## fmt + vet

.PHONY: plan-examples
plan-examples: build ## Parse + plan all HCL examples (validates schema, no infra changes)
	@echo "==> two-networks"
	@$(BINARY) plan -f examples/two-networks/field.sysbox.hcl
	@echo "==> three-nodes"
	@$(BINARY) plan -f examples/three-nodes/field.sysbox.hcl
	@echo "==> microvm"
	@$(BINARY) plan -f examples/microvm/field.sysbox.hcl
	@echo "==> mixed"
	@$(BINARY) plan -f examples/mixed/field.sysbox.hcl
	@echo "==> smoke"
	@$(BINARY) plan -f examples/microvm/smoke.hcl

.PHONY: ci
ci: lint test plan-examples ## Full CI check: fmt + vet + unit tests + example plans

# ── Lab lifecycle: three-nodes (docker only) ──────────────────────────────────

.PHONY: lab-up
lab-up: ## three-nodes: build image + apply topology + start sensor
	sudo -E examples/three-nodes/lab.sh up

.PHONY: lab-down
lab-down: ## three-nodes: destroy containers + stop sensor
	sudo -E examples/three-nodes/lab.sh down

.PHONY: lab-sensor-restart
lab-sensor-restart: build ## three-nodes: restart sensor (re-resolves mntns after reprovision)
	sudo -E examples/three-nodes/lab.sh sensor-restart

.PHONY: lab-logs
lab-logs: ## three-nodes: tail sensor log
	examples/three-nodes/lab.sh logs

.PHONY: lab-status
lab-status: ## three-nodes: show container, state, and sensor status
	examples/three-nodes/lab.sh status

.PHONY: lab-exec
lab-exec: ## three-nodes: open shell in a node (NODE=node_attack)
	examples/three-nodes/lab.sh exec $(NODE)

# ── Lab lifecycle: microvm (firecracker, requires KVM + rootfs) ───────────────
#
# Prereqs:  firecracker binary in PATH, SYSBOX_ROOTFS set (or default cache).
# See examples/microvm/field.sysbox.hcl for kernel/rootfs configuration.

MICROVM_STATE := runs/microvm/state.json
MICROVM_HCL   := examples/microvm/field.sysbox.hcl

.PHONY: microvm-up
microvm-up: build ## microvm: apply firecracker topology (requires KVM + SYSBOX_ROOTFS)
	@mkdir -p runs/microvm
	sudo -E $(BINARY) --state $(MICROVM_STATE) -f $(MICROVM_HCL) apply --auto-approve

.PHONY: microvm-down
microvm-down: ## microvm: destroy firecracker topology
	sudo -E $(BINARY) --state $(MICROVM_STATE) -f $(MICROVM_HCL) destroy --auto-approve

.PHONY: microvm-status
microvm-status: ## microvm: show topology state
	@$(BINARY) --state $(MICROVM_STATE) -f $(MICROVM_HCL) state list 2>/dev/null || echo "(no state)"

# ── Lab lifecycle: mixed (docker + firecracker) ────────────────────────────────
#
# Prereqs: firecracker binary in PATH, SYSBOX_ROOTFS set, Docker running.
# Docker nodes use veth injection; FC node uses TAP on the same Linux bridge.

MIXED_STATE := runs/mixed/state.json
MIXED_HCL   := examples/mixed/field.sysbox.hcl

.PHONY: mixed-up
mixed-up: build ## mixed: apply docker+firecracker topology (requires KVM + SYSBOX_ROOTFS)
	@mkdir -p runs/mixed
	sudo -E $(BINARY) --state $(MIXED_STATE) -f $(MIXED_HCL) apply --auto-approve

.PHONY: mixed-down
mixed-down: ## mixed: destroy mixed topology
	sudo -E $(BINARY) --state $(MIXED_STATE) -f $(MIXED_HCL) destroy --auto-approve

.PHONY: mixed-status
mixed-status: ## mixed: show topology state
	@$(BINARY) --state $(MIXED_STATE) -f $(MIXED_HCL) state list 2>/dev/null || echo "(no state)"

# ── Clean ─────────────────────────────────────────────────────────────────────

.PHONY: clean
clean: ## Remove compiled binary
	rm -f $(BINARY)

.PHONY: clean-runs
clean-runs: ## Remove per-episode artefacts (keeps state and SSH keys)
	examples/three-nodes/lab.sh clean

# ── Developer setup ───────────────────────────────────────────────────────────

.PHONY: setup-init-skip-worktree
setup-init-skip-worktree: ## Stop tracking local sysbox-init binary changes (run once after clone)
	git update-index --skip-worktree $(INIT_AMD64) $(INIT_ARM64)
	@echo "Local sysbox-init binaries are now skip-worktree."
	@echo "Run 'git update-index --no-skip-worktree $(INIT_AMD64) $(INIT_ARM64)' to undo."
