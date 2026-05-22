GO ?= $(shell command -v go 2>/dev/null || echo /usr/local/go/bin/go)
GOCACHE ?= /tmp/sysbox-gocache
GOENV := GOCACHE=$(GOCACHE)

BINARY := bin/sysbox
INITDIR := pkg/provider/firecracker/initbin
ARCH := $(shell $(GO) env GOARCH)

TOPO ?= two-networks
API_ADDR ?= :9876

HCL := examples/$(TOPO)/field.sysbox.hcl
STATE := runs/$(TOPO)/state.json
SYSBOX := $(BINARY) --state $(STATE) -f $(HCL)

.DEFAULT_GOAL := help
.PHONY: help build build-all test test-e2e lint ci \
	plan apply destroy up down \
	api-build api-seed api-up api-up-fc api-down api-logs \
	docker-build docker-seed docker-up docker-up-fc docker-down docker-logs \
	clean

help: ## Show available targets
	@echo "Usage: make <target> [TOPO=two-networks|three-nodes|microvm|mixed|libvirt-vm]"
	@echo ""
	@awk 'BEGIN{FS=":.*##"} /^[a-z][a-z0-9-]+:.*##/{printf "  %-14s %s\n",$$1,$$2}' $(MAKEFILE_LIST)

build: $(INITDIR)/sysbox-init.linux-$(ARCH).bin ## Build bin/sysbox
	$(GOENV) CGO_ENABLED=0 $(GO) build -buildvcs=false -o $(BINARY) ./cmd/sysbox

build-all: ## Build sysbox-init for amd64 and arm64
	$(GOENV) GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build -ldflags="-s -w" -o $(INITDIR)/sysbox-init.linux-amd64.bin ./cmd/sysbox-init
	$(GOENV) GOOS=linux GOARCH=arm64 CGO_ENABLED=0 $(GO) build -ldflags="-s -w" -o $(INITDIR)/sysbox-init.linux-arm64.bin ./cmd/sysbox-init

$(INITDIR)/sysbox-init.linux-%.bin: cmd/sysbox-init/main.go cmd/sysbox-init/network.go cmd/sysbox-init/server.go cmd/sysbox-init/sensor.go
	$(GOENV) GOOS=linux GOARCH=$* CGO_ENABLED=0 $(GO) build -ldflags="-s -w" -o $@ ./cmd/sysbox-init

test: ## Run unit tests
	$(GOENV) $(GO) test ./...

test-e2e: ## Run integration tests; root/CAP_NET_ADMIN required for real netns paths
	$(GOENV) $(GO) test -tags e2e ./pkg/api ./tests/e2e/...

lint: ## Run go vet
	$(GOENV) $(GO) vet ./...

ci: build lint test ## Run the local CI gate
	@for topo in two-networks three-nodes microvm mixed libvirt-vm; do \
		printf "  %-14s" "$$topo:"; \
		$(BINARY) -f examples/$$topo/field.sysbox.hcl --state /tmp/sysbox-ci-$$topo.json plan 2>&1 | head -1; \
	done

plan: build ## Plan an example topology
	$(SYSBOX) plan

apply: build ## Apply an example topology
	@mkdir -p runs/$(TOPO)
	$(SYSBOX) apply --auto-approve

destroy: build ## Destroy an example topology
	$(SYSBOX) destroy --auto-approve

up: apply ## Alias for apply
down: destroy ## Alias for destroy

api-build: ## Build the sysbox API container image
	docker build --network=host --no-cache -t sysbox:latest .

api-seed: ## Seed data/workspaces from examples when missing
	@mkdir -p data/workspaces
	@for dir in examples/*; do \
		if [ -f "$$dir/field.sysbox.hcl" ]; then \
			name=$$(basename "$$dir"); \
			mkdir -p "data/workspaces/$$name"; \
			if [ ! -f "data/workspaces/$$name/field.sysbox.hcl" ]; then \
				cp "$$dir/field.sysbox.hcl" "data/workspaces/$$name/field.sysbox.hcl"; \
				echo "seeded $$name"; \
			fi; \
		fi; \
	done

api-up: api-build api-seed ## Start API + Postgres; auto-mount Firecracker when available
	@if [ -n "$${SYSBOX_FIRECRACKER_BIN:-}" ] || command -v firecracker >/dev/null 2>&1; then \
		fc_bin="$${SYSBOX_FIRECRACKER_BIN:-$$(command -v firecracker)}"; \
		echo "Firecracker detected: $$fc_bin"; \
		SYSBOX_FIRECRACKER_BIN="$$fc_bin" docker compose -f docker-compose.yml -f docker-compose.firecracker.yml up -d; \
	else \
		echo "Firecracker not detected; starting Docker substrate only."; \
		docker compose up -d; \
	fi
	@echo "API server: http://localhost:9876/v1/health"

api-up-fc: api-build api-seed ## Start API + Postgres with explicit Firecracker mounts
	docker compose -f docker-compose.yml -f docker-compose.firecracker.yml up -d
	@echo "API server: http://localhost:9876/v1/health"

api-down: ## Stop API + Postgres
	docker compose down

api-logs: ## Tail API compose logs
	docker compose logs -f

docker-build: api-build ## Alias for api-build
docker-seed: api-seed ## Alias for api-seed
docker-up: api-up ## Alias for api-up
docker-up-fc: api-up-fc ## Alias for api-up-fc
docker-down: api-down ## Alias for api-down
docker-logs: api-logs ## Alias for api-logs

clean: ## Remove build outputs
	rm -f $(BINARY)
