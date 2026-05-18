GO      := $(shell which go 2>/dev/null || echo /usr/local/go/bin/go)
BINARY  := bin/sysbox
INITDIR := pkg/provider/firecracker/initbin
ARCH    := $(shell $(GO) env GOARCH)

SUITE ?= three-nodes
_HCL   := examples/$(SUITE)/field.sysbox.hcl
_STATE := runs/$(SUITE)/state.json
_SB    := $(BINARY) --state $(_STATE) -f $(_HCL)

.DEFAULT_GOAL := help
.PHONY: help build build-all test lint ci plan up down lab lab-down sensor-restart logs clean

help:
	@echo "Usage: make <target>  [SUITE=three-nodes|microvm|mixed|two-networks]"
	@echo ""
	@awk 'BEGIN{FS=":.*##"} /^[a-z][a-z-]+:.*##/{printf "  %-18s %s\n",$$1,$$2}' $(MAKEFILE_LIST)
	@echo ""

# ── build ─────────────────────────────────────────────────────────────────────

build: $(INITDIR)/sysbox-init.linux-$(ARCH).bin ## Compile bin/sysbox
	CGO_ENABLED=0 $(GO) build -o $(BINARY) ./cmd/sysbox

build-all: ## Cross-compile sysbox-init for amd64 + arm64
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build -ldflags="-s -w" \
	    -o $(INITDIR)/sysbox-init.linux-amd64.bin ./cmd/sysbox-init
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 $(GO) build -ldflags="-s -w" \
	    -o $(INITDIR)/sysbox-init.linux-arm64.bin ./cmd/sysbox-init

$(INITDIR)/sysbox-init.linux-%.bin: \
    cmd/sysbox-init/main.go cmd/sysbox-init/network.go \
    cmd/sysbox-init/server.go cmd/sysbox-init/sensor.go
	GOOS=linux GOARCH=$* CGO_ENABLED=0 $(GO) build -ldflags="-s -w" -o $@ ./cmd/sysbox-init

# ── quality ───────────────────────────────────────────────────────────────────

test: ## Unit tests
	$(GO) test ./...

lint: ## go fmt + go vet
	$(GO) fmt ./...
	$(GO) vet ./...

ci: build lint test ## CI gate: lint + tests + plan all examples
	@for s in two-networks three-nodes microvm mixed; do \
	    printf "  %-14s" "$$s:"; \
	    $(BINARY) -f examples/$$s/field.sysbox.hcl plan 2>&1 | head -1; \
	done

# ── topology  [SUITE=three-nodes] ────────────────────────────────────────────

plan: build ## sysbox plan  (no infra changes)
	$(_SB) plan

up: build ## sysbox apply  (sudo required for firecracker/mixed)
	@mkdir -p runs/$(SUITE)
	$(_SB) apply --auto-approve

down: ## sysbox destroy
	$(_SB) destroy --auto-approve

# ── three-nodes lab  (attacker image + SSH keys + tracee sensor) ──────────────

lab: ## Build attacker image, apply, start sensor
	sudo -E examples/three-nodes/lab.sh up

lab-down: ## Destroy + stop sensor
	sudo -E examples/three-nodes/lab.sh down

sensor-restart: build ## Restart tracee sensor (re-resolves mntns)
	sudo -E examples/three-nodes/lab.sh sensor-restart

logs: ## Tail sensor log
	examples/three-nodes/lab.sh logs

# ── maintenance ───────────────────────────────────────────────────────────────

clean: ## Remove bin/sysbox
	rm -f $(BINARY)
