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
API_URL ?= http://127.0.0.1:9876
AGENT_API_URL ?= http://sysbox-api:9876
API_DATA_DIR ?= $(or $(SYSBOX_HOST_HOME_DIR),.sysbox/api)
AGENT_ID ?= local-docker

HCL := examples/$(TOPO)/field.sysbox.hcl
STATE := .sysbox/runs/$(TOPO)/state.json
SYSBOX := $(BINARY) --state $(STATE) -f $(HCL)

COMPOSE_DIR := deploy/docker
COMPOSE := docker compose --project-directory .
COMPOSE_API := -f $(COMPOSE_DIR)/compose.yml
COMPOSE_FULL := -f $(COMPOSE_DIR)/compose.yml -f $(COMPOSE_DIR)/compose.agent.yml

.DEFAULT_GOAL := help
.PHONY: help build build-all test test-e2e lint ci \
	plan apply destroy up down \
	image seed deploy deploy-full undeploy reset logs config \
	.agent-register clean

help: ## Show available targets
	@echo "Usage: make <target> [TOPO=two-networks|three-nodes|microvm|mixed|libvirt-vm]"
	@echo ""
	@awk 'BEGIN{FS=":.*##"} /^[a-z][a-z0-9-]+:.*##/{printf "  %-16s %s\n",$$1,$$2}' $(MAKEFILE_LIST)

build: $(INITDIR)/sysbox-init.linux-$(ARCH).bin ## Build bin/sysbox
	$(GOENV) CGO_ENABLED=0 $(GO) build -buildvcs=false -o $(BINARY) ./cmd/sysbox

build-all: ## Build sysbox-init for amd64 and arm64
	$(GOENV) GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build -ldflags="-s -w" -o $(INITDIR)/sysbox-init.linux-amd64.bin ./cmd/sysbox-init
	$(GOENV) GOOS=linux GOARCH=arm64 CGO_ENABLED=0 $(GO) build -ldflags="-s -w" -o $(INITDIR)/sysbox-init.linux-arm64.bin ./cmd/sysbox-init

$(INITDIR)/sysbox-init.linux-%.bin: cmd/sysbox-init/main.go cmd/sysbox-init/network.go cmd/sysbox-init/server.go cmd/sysbox-init/sensor.go
	$(GOENV) GOOS=linux GOARCH=$* CGO_ENABLED=0 $(GO) build -ldflags="-s -w" -o $@ ./cmd/sysbox-init

test: ## Run unit tests
	$(GOENV) $(GO) test ./...

test-e2e: ## Run black-box API e2e tests against make deploy-full
	bash tests/e2e/api_smoke.sh

lint: ## Run go vet
	$(GOENV) $(GO) vet ./...

ci: build lint test ## Run the local CI gate
	@for topo in two-networks three-nodes microvm mixed libvirt-vm; do \
		printf "  %-14s" "$$topo:"; \
		$(BINARY) -f examples/$$topo/field.sysbox.hcl --state /tmp/sysbox-ci-$$topo.json plan 2>&1 | head -1; \
	done

plan: build ## Plan an example topology locally
	$(SYSBOX) plan

apply: build ## Apply an example topology locally
	@mkdir -p .sysbox/runs/$(TOPO)
	$(SYSBOX) apply --auto-approve

destroy: build ## Destroy an example topology locally
	$(SYSBOX) destroy --auto-approve

up: apply ## Alias for apply
down: destroy ## Alias for destroy

image: ## Build the sysbox container image
	docker build --network=host --no-cache -t sysbox:latest .

seed: ## Seed API workspaces from examples when missing
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

deploy: image seed ## Deploy API + Postgres
	$(COMPOSE) $(COMPOSE_API) up -d
	@echo "API server: $(API_URL)/v1/health"

deploy-full: deploy .agent-register ## Deploy API + Postgres + Docker agent
	$(COMPOSE) $(COMPOSE_FULL) up -d sysbox-agent
	@echo "Agent: $(AGENT_ID)"

.agent-register:
	$(COMPOSE) $(COMPOSE_FULL) run --rm --no-deps --entrypoint sysbox sysbox-agent \
		agent register --api $(AGENT_API_URL) --token "$(SYSBOX_API_TOKEN)" --id $(AGENT_ID) --name $(AGENT_ID) --capabilities docker --identity /var/lib/sysbox/agent/identity.json

undeploy: ## Stop API, Postgres, and agent
	$(COMPOSE) $(COMPOSE_FULL) down

reset: ## Stop compose and remove local Postgres volume
	$(COMPOSE) $(COMPOSE_FULL) down -v

logs: ## Tail compose logs
	$(COMPOSE) $(COMPOSE_FULL) logs -f

config: ## Print resolved compose config
	$(COMPOSE) $(COMPOSE_FULL) config

clean: ## Remove build outputs
	rm -f $(BINARY)
