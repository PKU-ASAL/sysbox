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
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z0-9_-]+:.*##/ { printf "  %-22s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
	@echo ""

# ── Build ─────────────────────────────────────────────────────────────────────

.PHONY: build
build: build-init ## Compile bin/sysbox (auto-builds embedded sysbox-init for host arch)
	$(GOFLAGS) $(GO) build -o $(BINARY) ./cmd/sysbox

.PHONY: build-init
build-init: $(INIT_DEFAULT) ## Cross-compile sysbox-init for the host arch only

.PHONY: build-init-all
build-init-all: $(INIT_AMD64) $(INIT_ARM64) ## Cross-compile sysbox-init for amd64 AND arm64

$(INIT_AMD64): cmd/sysbox-init/main.go cmd/sysbox-init/network.go cmd/sysbox-init/server.go
	rm -f $@
	GOOS=linux GOARCH=amd64 $(GOFLAGS) $(GO) build -ldflags="-s -w" -o $@ ./cmd/sysbox-init

$(INIT_ARM64): cmd/sysbox-init/main.go cmd/sysbox-init/network.go cmd/sysbox-init/server.go
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

# ── Lab lifecycle (require sudo -E) ───────────────────────────────────────────

.PHONY: lab-up
lab-up: ## Build image, destroy old lab, apply topology, start sensor
	sudo -E examples/three-nodes/lab.sh up

.PHONY: lab-down
lab-down: ## Destroy lab containers and stop sensor
	sudo -E examples/three-nodes/lab.sh down

.PHONY: lab-sensor-restart
lab-sensor-restart: build ## Restart sensor (re-resolves mntns after node reprovision)
	sudo -E examples/three-nodes/lab.sh sensor-restart

.PHONY: lab-logs
lab-logs: ## Tail sensor log
	examples/three-nodes/lab.sh logs

.PHONY: lab-status
lab-status: ## Show container, state, and sensor status
	examples/three-nodes/lab.sh status

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
