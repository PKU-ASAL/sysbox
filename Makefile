BINARY        := bin/sysbox
INIT_DIR      := pkg/provider/firecracker/initbin
INIT_AMD64    := $(INIT_DIR)/sysbox-init.linux-amd64.bin
INIT_ARM64    := $(INIT_DIR)/sysbox-init.linux-arm64.bin
GO            := $(shell which go 2>/dev/null || echo /usr/local/go/bin/go)
GOFLAGS       := CGO_ENABLED=0
HOST_ARCH     := $(shell $(GO) env GOARCH)
INIT_DEFAULT  := $(INIT_DIR)/sysbox-init.linux-$(HOST_ARCH).bin

# SUITE selects which example topology to plan/apply/destroy.
# Values: three-nodes (default) | microvm | mixed | two-networks
SUITE   ?= three-nodes
# NODE is used by the exec target.
NODE    ?= node_attack

_HCL    := examples/$(SUITE)/field.sysbox.hcl
_STATE  := runs/$(SUITE)/state.json
_SYSBOX := $(BINARY) --state $(_STATE) -f $(_HCL)

.DEFAULT_GOAL := help

# ── Help ──────────────────────────────────────────────────────────────────────

.PHONY: help
help:
	@echo ""
	@echo "Usage: make <target> [SUITE=three-nodes|microvm|mixed|two-networks] [NODE=...]"
	@echo ""
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z0-9_-]+:.*##/ { printf "  %-22s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
	@echo ""

# ── Build ─────────────────────────────────────────────────────────────────────

.PHONY: build
build: build-init ## Compile bin/sysbox (embeds sysbox-init for host arch)
	$(GOFLAGS) $(GO) build -o $(BINARY) ./cmd/sysbox

.PHONY: build-init
build-init: $(INIT_DEFAULT) ## Compile sysbox-init for host arch

.PHONY: build-init-all
build-init-all: $(INIT_AMD64) $(INIT_ARM64) ## Compile sysbox-init for amd64 + arm64

INIT_SRCS = cmd/sysbox-init/main.go cmd/sysbox-init/network.go \
            cmd/sysbox-init/server.go cmd/sysbox-init/sensor.go

$(INIT_AMD64): $(INIT_SRCS)
	rm -f $@
	GOOS=linux GOARCH=amd64 $(GOFLAGS) $(GO) build -ldflags="-s -w" -o $@ ./cmd/sysbox-init

$(INIT_ARM64): $(INIT_SRCS)
	rm -f $@
	GOOS=linux GOARCH=arm64 $(GOFLAGS) $(GO) build -ldflags="-s -w" -o $@ ./cmd/sysbox-init

# ── Quality ───────────────────────────────────────────────────────────────────

.PHONY: test
test: ## Run unit tests
	$(GO) test ./...

.PHONY: test-e2e
test-e2e: build ## Topology e2e tests (requires Docker + root)
	sudo -E "$(GO)" test -tags e2e -v -count=1 ./tests/e2e/... -timeout 120s

.PHONY: lint
lint: ## go fmt + go vet
	$(GO) fmt ./...
	$(GO) vet ./...

.PHONY: ci
ci: lint test ## Full CI gate: lint + tests + validate all example schemas
	@echo "==> plan examples"
	@for suite in two-networks three-nodes microvm mixed; do \
	    printf "  %-14s" "$$suite:"; \
	    $(BINARY) -f examples/$$suite/field.sysbox.hcl plan 2>&1 | head -1; \
	done
	@printf "  %-14s" "smoke:"; \
	$(BINARY) -f examples/microvm/smoke.hcl plan 2>&1 | head -1

# ── Topology (SUITE-parameterised) ────────────────────────────────────────────
# All targets below respect SUITE= (default: three-nodes).
# Example:  make plan SUITE=microvm
#           make up   SUITE=mixed
#           sudo -E make up SUITE=microvm   (firecracker requires root)

.PHONY: plan
plan: build ## Plan SUITE topology (no infra changes)
	$(_SYSBOX) plan

.PHONY: up
up: build ## Apply SUITE topology  [sudo required for FC/mixed]
	@mkdir -p runs/$(SUITE)
	$(_SYSBOX) apply --auto-approve

.PHONY: down
down: ## Destroy SUITE topology  [sudo required for FC/mixed]
	$(_SYSBOX) destroy --auto-approve

.PHONY: status
status: ## Show SUITE state
	@$(_SYSBOX) state list 2>/dev/null || echo "(no state)"

.PHONY: exec
exec: ## Exec into NODE inside SUITE  (NODE=node_attack)
	docker exec -it sysbox-$(NODE) /bin/bash

# ── three-nodes lab (image build + SSH keys + tracee sensor) ──────────────────
# These targets call lab.sh which handles attacker image, keypairs, and sensor.
# They always operate on examples/three-nodes regardless of SUITE.

.PHONY: lab
lab: ## three-nodes: build image + apply + start tracee sensor
	sudo -E examples/three-nodes/lab.sh up

.PHONY: lab-down
lab-down: ## three-nodes: destroy + stop sensor
	sudo -E examples/three-nodes/lab.sh down

.PHONY: sensor-restart
sensor-restart: build ## three-nodes: restart sensor (re-resolves mntns)
	sudo -E examples/three-nodes/lab.sh sensor-restart

.PHONY: logs
logs: ## three-nodes: tail sensor log
	examples/three-nodes/lab.sh logs

# ── Clean ─────────────────────────────────────────────────────────────────────

.PHONY: clean
clean: ## Remove bin/sysbox
	rm -f $(BINARY)

.PHONY: clean-runs
clean-runs: ## Remove episode artefacts (keeps state + SSH keys)
	examples/three-nodes/lab.sh clean

# ── Developer setup ───────────────────────────────────────────────────────────

.PHONY: setup-init-skip-worktree
setup-init-skip-worktree: ## Skip-worktree for sysbox-init binaries (run once after clone)
	git update-index --skip-worktree $(INIT_AMD64) $(INIT_ARM64)
	@echo "sysbox-init binaries are now skip-worktree."
	@echo "Undo: git update-index --no-skip-worktree $(INIT_AMD64) $(INIT_ARM64)"
