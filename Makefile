GO ?= $(shell command -v go 2>/dev/null || echo /usr/local/go/bin/go)
GOCACHE ?= /tmp/sysbox-gocache
GOENV := GOCACHE=$(GOCACHE)

ifneq (,$(wildcard .env))
include .env
endif

BINARY := bin/sysbox
INITDIR := pkg/provider/firecracker/initbin
ARCH := $(shell $(GO) env GOARCH)

TOPO ?= two-networks
API_ADDR ?= :9876
SYSBOX_DEPLOYMENT ?= docker
API_DATA_DIR ?= $(or $(SYSBOX_HOST_HOME_DIR),.sysbox/api)

HCL := examples/$(TOPO)/field.sysbox.hcl
STATE := .sysbox/runs/$(TOPO)/state.json
SYSBOX := $(BINARY) --state $(STATE) -f $(HCL)

COMPOSE_DIR := deploy/docker
COMPOSE_FILES_docker := -f $(COMPOSE_DIR)/compose.yml
COMPOSE_FILES_vm := -f $(COMPOSE_DIR)/compose.yml -f $(COMPOSE_DIR)/compose.vm.yml
COMPOSE_FILES_full := -f $(COMPOSE_DIR)/compose.yml -f $(COMPOSE_DIR)/compose.vm.yml -f $(COMPOSE_DIR)/compose.full.yml
COMPOSE_FILES = $(COMPOSE_FILES_$(SYSBOX_DEPLOYMENT))
COMPOSE := docker compose --project-directory .

.DEFAULT_GOAL := help
.PHONY: help build build-all test test-e2e lint ci \
	plan apply destroy up down \
	api-build api-seed api-config api-up api-up-docker api-up-vm api-up-full api-down api-logs \
	docker-build docker-seed docker-config docker-up docker-up-docker docker-up-vm docker-up-full docker-down docker-logs \
	clean

help: ## Show available targets
	@echo "Usage: make <target> [TOPO=two-networks|three-nodes|microvm|mixed|libvirt-vm]"
	@echo "       make api-up [SYSBOX_DEPLOYMENT=docker|vm|full]"
	@echo ""
	@awk 'BEGIN{FS=":.*##"} /^[a-z][a-z0-9-]+:.*##/{printf "  %-18s %s\n",$$1,$$2}' $(MAKEFILE_LIST)

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
	@mkdir -p .sysbox/runs/$(TOPO)
	$(SYSBOX) apply --auto-approve

destroy: build ## Destroy an example topology
	$(SYSBOX) destroy --auto-approve

up: apply ## Alias for apply
down: destroy ## Alias for destroy

api-build: ## Build the sysbox API container image
	docker build --network=host --no-cache -t sysbox:latest .

api-seed: ## Seed API workspaces from examples when missing
	@mkdir -p "$(API_DATA_DIR)/workspaces"
	@for dir in examples/*; do \
		if [ -f "$$dir/field.sysbox.hcl" ]; then \
			name=$$(basename "$$dir"); \
			mkdir -p "$(API_DATA_DIR)/workspaces/$$name"; \
			if [ ! -f "$(API_DATA_DIR)/workspaces/$$name/field.sysbox.hcl" ]; then \
				cp "$$dir/field.sysbox.hcl" "$(API_DATA_DIR)/workspaces/$$name/field.sysbox.hcl"; \
				echo "seeded $$name"; \
			fi; \
		fi; \
	done

api-config: ## Print resolved Docker Compose config for SYSBOX_DEPLOYMENT
	@test -n "$(COMPOSE_FILES)" || { echo "unknown SYSBOX_DEPLOYMENT=$(SYSBOX_DEPLOYMENT)"; exit 2; }
	$(COMPOSE) $(COMPOSE_FILES) config

api-up: api-build api-seed ## Start API + Postgres using SYSBOX_DEPLOYMENT
	@test -n "$(COMPOSE_FILES)" || { echo "unknown SYSBOX_DEPLOYMENT=$(SYSBOX_DEPLOYMENT)"; exit 2; }
	@echo "Deployment: $(SYSBOX_DEPLOYMENT)"
	$(COMPOSE) $(COMPOSE_FILES) up -d
	@echo "API server: http://localhost:9876/v1/health"

api-up-docker: SYSBOX_DEPLOYMENT=docker
api-up-docker: api-up

api-up-vm: SYSBOX_DEPLOYMENT=vm
api-up-vm: api-up

api-up-full: SYSBOX_DEPLOYMENT=full
api-up-full: api-up

api-down: ## Stop API + Postgres
	@test -n "$(COMPOSE_FILES)" || { echo "unknown SYSBOX_DEPLOYMENT=$(SYSBOX_DEPLOYMENT)"; exit 2; }
	$(COMPOSE) $(COMPOSE_FILES) down

api-logs: ## Tail API compose logs
	@test -n "$(COMPOSE_FILES)" || { echo "unknown SYSBOX_DEPLOYMENT=$(SYSBOX_DEPLOYMENT)"; exit 2; }
	$(COMPOSE) $(COMPOSE_FILES) logs -f

docker-build: api-build
docker-seed: api-seed
docker-config: api-config
docker-up: api-up
docker-up-docker: api-up-docker
docker-up-vm: api-up-vm
docker-up-full: api-up-full
docker-down: api-down
docker-logs: api-logs

clean: ## Remove build outputs
	rm -f $(BINARY)
