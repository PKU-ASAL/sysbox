GO      := $(shell which go 2>/dev/null || echo /usr/local/go/bin/go)
BINARY  := bin/sysbox
INITDIR := pkg/provider/firecracker/initbin
ARCH    := $(shell $(GO) env GOARCH)

TOPO ?= three-nodes
API_ADDR ?= :9876
API_PID  := $(shell pgrep -f '$(BINARY) serve' 2>/dev/null | head -1)
_HCL   := examples/$(TOPO)/field.sysbox.hcl
_STATE := runs/$(TOPO)/state.json
_SB    := $(BINARY) --state $(_STATE) -f $(_HCL)
_LAB   := examples/$(TOPO)/lab.sh

.DEFAULT_GOAL := help
.PHONY: help build build-all test test-e2e lint ci plan up down lab lab-down logs \
        serve serve-restart serve-stop \
        docker-build docker-seed docker-up docker-up-fc docker-down docker-logs clean

help:
	@echo "Usage: make <target>  [TOPO=three-nodes|microvm|mixed|two-networks]"
	@echo ""
	@awk 'BEGIN{FS=":.*##"} /^[a-z][a-z-]+:.*##/{printf "  %-18s %s\n",$$1,$$2}' $(MAKEFILE_LIST)
	@echo ""

# ── build ─────────────────────────────────────────────────────────────────────

build: $(INITDIR)/sysbox-init.linux-$(ARCH).bin ## Compile bin/sysbox
	CGO_ENABLED=0 $(GO) build -buildvcs=false -o $(BINARY) ./cmd/sysbox

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

test-e2e: ## E2E/integration tests (requires root for netns/firecracker paths)
	$(GO) test -tags e2e ./pkg/api ./tests/e2e/...

lint: ## go fmt + go vet
	$(GO) fmt ./...
	$(GO) vet ./...

ci: build lint test ## CI gate: lint + tests + plan all examples
	@for s in two-networks three-nodes microvm mixed; do \
	    printf "  %-14s" "$$s:"; \
	    $(BINARY) -f examples/$$s/field.sysbox.hcl plan 2>&1 | head -1; \
	done

# ── topology  [TOPO=three-nodes] ────────────────────────────────────────────

plan: build ## sysbox plan  (no infra changes)
	$(_SB) plan

up: build ## sysbox apply  (sudo required for firecracker/mixed)
	@mkdir -p runs/$(TOPO)
	$(_SB) apply --auto-approve

down: ## sysbox destroy
	$(_SB) destroy --auto-approve

# ── lab lifecycle  [TOPO=three-nodes]  (image build + SSH keys + sensor) ─────

lab: ## Full lab setup: image build + apply + start sensor
	sudo -E $(_LAB) up

lab-down: ## Destroy lab + stop sensor
	sudo -E $(_LAB) down

logs: ## Tail sensor log
	$(_LAB) logs

# ── API server  [API_ADDR=:8080] ────────────────────────────────────────────

serve: build ## Start API server (bg, auto-restarts if already running)
	@if [ -n "$(API_PID)" ]; then \
	    echo "Stopping existing API server (PID $(API_PID))..."; \
	    kill $(API_PID) 2>/dev/null || true; \
	    sleep 0.3; \
	fi
	@$(BINARY) serve --addr $(API_ADDR) & APID=$$!; echo "API server started on $(API_ADDR)  PID=$$APID"

serve-restart: build ## Rebuild + restart API server
	@if [ -n "$(API_PID)" ]; then \
	    echo "Stopping existing API server (PID $(API_PID))..."; \
	    kill $(API_PID) 2>/dev/null || true; \
	    sleep 0.3; \
	fi
	@$(BINARY) serve --addr $(API_ADDR) & APID=$$!; echo "API server restarted on $(API_ADDR)  PID=$$APID"

serve-stop: ## Stop the running API server
	@if [ -z "$(API_PID)" ]; then \
	    echo "No running API server found."; \
	else \
	    echo "Stopping API server (PID $(API_PID))..."; \
	    kill $(API_PID) 2>/dev/null && echo "Stopped." || echo "Already stopped."; \
	fi

# ── Docker deployment  [API_ADDR=:9876] ─────────────────────────────────────

docker-build: ## Build sysbox Docker image (no cache)
	docker build --network=host --no-cache -t sysbox:latest .

docker-seed: ## Copy examples into API-owned data/workspaces if missing
	@mkdir -p data/workspaces
	@for d in examples/*; do \
	    if [ -f "$$d/field.sysbox.hcl" ]; then \
	        name=$$(basename "$$d"); \
	        mkdir -p "data/workspaces/$$name"; \
	        if [ ! -f "data/workspaces/$$name/field.sysbox.hcl" ]; then \
	            cp "$$d/field.sysbox.hcl" "data/workspaces/$$name/field.sysbox.hcl"; \
	            echo "seeded $$name"; \
	        fi; \
	    fi; \
	done

docker-up: docker-build docker-seed ## Start sysbox-api container (auto-enable Firecracker when available)
	@if [ -n "$${SYSBOX_FIRECRACKER_BIN:-}" ] || command -v firecracker >/dev/null 2>&1; then \
	    fc_bin="$${SYSBOX_FIRECRACKER_BIN:-$$(command -v firecracker)}"; \
	    echo "Firecracker detected: $$fc_bin"; \
	    SYSBOX_FIRECRACKER_BIN="$$fc_bin" docker compose -f docker-compose.yml -f docker-compose.firecracker.yml up -d; \
	else \
	    echo "Firecracker not detected; starting Docker substrate only."; \
	    docker compose up -d; \
	fi
	@echo "API server: http://localhost:9876/v1/health"

docker-up-fc: docker-build docker-seed ## Start sysbox-api with Firecracker override (set SYSBOX_FIRECRACKER_BIN)
	docker compose -f docker-compose.yml -f docker-compose.firecracker.yml up -d
	@echo "API server: http://localhost:9876/v1/health"

docker-down: ## Stop and remove sysbox-api container
	docker compose down

docker-logs: ## Tail container logs
	docker compose logs -f

# ── maintenance ───────────────────────────────────────────────────────────────

clean: ## Remove bin/sysbox
	rm -f $(BINARY)
