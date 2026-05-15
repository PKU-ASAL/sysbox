BINARY     := bin/sysbox
INIT_EMBED := pkg/provider/firecracker/initbin/sysbox-init.bin
GO         := $(shell which go 2>/dev/null || echo /usr/local/go/bin/go)
GOFLAGS    := CGO_ENABLED=0

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
build: build-init ## Compile bin/sysbox (auto-builds embedded sysbox-init)
	$(GOFLAGS) $(GO) build -o $(BINARY) ./cmd/sysbox

.PHONY: build-init
build-init: $(INIT_EMBED) ## Cross-compile sysbox-init for linux/amd64 and embed it

$(INIT_EMBED): cmd/sysbox-init/main.go
	GOOS=linux GOARCH=amd64 $(GOFLAGS) $(GO) build -ldflags="-s -w" -o $(INIT_EMBED) ./cmd/sysbox-init

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
