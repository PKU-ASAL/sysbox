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
API_DATA_ABS := $(abspath $(API_DATA_DIR))
AGENT_ID ?= local-docker
AGENT_CAPABILITIES ?= docker,network
WEB_HOST_ADDR ?= $(or $(SYSBOX_WEB_HOST_ADDR),0.0.0.0)
WEB_HOST_PORT ?= $(or $(SYSBOX_WEB_HOST_PORT),3001)
WEB_URL ?= http://127.0.0.1:$(WEB_HOST_PORT)

HCL := examples/$(TOPO)/field.sysbox.hcl
STATE := .sysbox/runs/$(TOPO)/state.json
SYSBOX := $(BINARY) --state $(STATE) -f $(HCL)
LAB_SSH_KEY ?= .sysbox/runs/$(TOPO)/lab_key
LAB_SSH_PUBKEY ?= $(LAB_SSH_KEY).pub

COMPOSE_DIR := deploy/docker
COMPOSE := docker compose --project-directory .
COMPOSE_API := -f $(COMPOSE_DIR)/compose.yml
COMPOSE_FULL := -f $(COMPOSE_DIR)/compose.yml -f $(COMPOSE_DIR)/compose.agent.yml
COMPOSE_ALL := -f $(COMPOSE_DIR)/compose.yml -f $(COMPOSE_DIR)/compose.agent.yml -f $(COMPOSE_DIR)/compose.web.yml

FIRST_GOAL := $(firstword $(MAKECMDGOALS))
SUBCOMMAND := $(word 2,$(MAKECMDGOALS))

.DEFAULT_GOAL := help

.PHONY: help build build-all web-build test test-e2e test-docker-launch test-privileged-compile test-privileged test-privileged-container prepare-libvirt-cloud-image test-heterogeneous-matrix test-heterogeneous-reset release-test release-workflow-test release-build release-verify lint ci clean \
	cli api \
	cli-help cli-validate cli-plan cli-apply cli-destroy cli-output cli-state \
	api-help api-build-api api-build-ui api-seed api-deploy api-deploy-full api-status api-down api-clean api-logs api-config \
	validate plan apply destroy output state up \
	build-api build-ui seed deploy deploy-full status down logs config \
	.agent-register

help: ## Show command groups
	@echo "Usage:"
	@echo "  make cli <validate|plan|apply|destroy|output|state> [TOPO=two-networks]"
	@echo "  make api <build-api|build-ui|seed|deploy|deploy-full|status|down|clean|logs|config>"
	@echo ""
	@echo "Common:"
	@echo "  make build          Build bin/sysbox"
	@echo "  make test           Run unit tests"
	@echo "  make lint           Run go vet"
	@echo "  make test-e2e       Run API e2e smoke test against make api deploy-full"
	@echo "  make test-docker-launch  Run Docker ENTRYPOINT/CMD override acceptance"
	@echo "  make test-privileged-compile  Compile privileged recovery tests without running them"
	@echo "  make test-privileged          Run privileged recovery tests (requires root/CAP_NET_ADMIN)"
	@echo "  make test-privileged-container  Run privileged acceptance tests through Docker"
	@echo "  make prepare-libvirt-cloud-image  Cache the pinned Ubuntu libvirt image"
	@echo "  make test-heterogeneous-matrix  Run the full Docker/Firecracker/libvirt matrix"

build: $(INITDIR)/sysbox-init.linux-$(ARCH).bin ## Build bin/sysbox
	$(GOENV) CGO_ENABLED=0 $(GO) build -buildvcs=false -o $(BINARY) ./cmd/sysbox

build-all: ## Build sysbox-init for amd64 and arm64
	$(GOENV) GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build -ldflags="-s -w" -o $(INITDIR)/sysbox-init.linux-amd64.bin ./cmd/sysbox-init
	$(GOENV) GOOS=linux GOARCH=arm64 CGO_ENABLED=0 $(GO) build -ldflags="-s -w" -o $(INITDIR)/sysbox-init.linux-arm64.bin ./cmd/sysbox-init

$(INITDIR)/sysbox-init.linux-%.bin: cmd/sysbox-init/main.go cmd/sysbox-init/network.go cmd/sysbox-init/server.go
	$(GOENV) GOOS=linux GOARCH=$* CGO_ENABLED=0 $(GO) build -ldflags="-s -w" -o $@ ./cmd/sysbox-init

test: ## Run unit tests
	$(GOENV) $(GO) test ./...

test-e2e: ## Run black-box API e2e tests
	bash tests/e2e/api_smoke.sh

test-docker-launch: ## Run Docker launch override lifecycle acceptance
	bash tests/e2e/docker_launch_override.sh

test-privileged-compile: ## Compile privileged recovery tests without running them
	$(GOENV) $(GO) test -tags e2e -run '^$$' ./pkg/api ./pkg/provider/network ./pkg/provider/docker

test-privileged: ## Run privileged recovery tests (requires root/CAP_NET_ADMIN)
	$(GOENV) $(GO) test -tags e2e -v -run '^Test(Checkpoint|OwnedPolicy|DockerOwnedPolicy).*E2E$$' ./pkg/api ./pkg/provider/network ./pkg/provider/docker

test-privileged-container: ## Run privileged acceptance tests through Docker
	bash tests/e2e/privileged_container.sh

prepare-libvirt-cloud-image: ## Cache and verify the pinned Ubuntu libvirt image
	bash scripts/prepare-libvirt-cloud-image.sh

test-heterogeneous-matrix: ## Run the full heterogeneous IPv4 acceptance matrix
	bash tests/e2e/heterogeneous_matrix.sh

test-heterogeneous-reset: ## Run three full and targeted heterogeneous reset cycles
	bash tests/e2e/heterogeneous_reset.sh

release-test: ## Test deterministic release artifacts and GitHub Release audit
	bash scripts/release/test.sh
	bash scripts/release/github_test.sh
	bash scripts/release/oci_test.sh

release-workflow-test: ## Validate GitHub workflow trigger and permission contracts
	bash scripts/release/workflow_test.sh

release-build: ## Build release artifacts for VERSION=vMAJOR.MINOR.PATCH
	bash scripts/release/build.sh --tag "$(VERSION)" --output dist

release-verify: ## Verify artifacts in dist/
	bash scripts/release/verify.sh dist

web-build: ## Build the Web UI
	npm --prefix web install
	npm --prefix web run build

lint: ## Run go vet
	$(GOENV) $(GO) vet ./...

ci: build lint test ## Run the local CI gate
	@status=0; for topo in two-networks three-nodes microvm mixed libvirt-vm controlled-egress; do \
		output="$$( $(BINARY) -f examples/$$topo/field.sysbox.hcl --state /tmp/sysbox-ci-$$topo.json plan 2>&1 )"; rc=$$?; \
		printf "  %-14s%s\n" "$$topo:" "$$(printf '%s\n' "$$output" | head -1)"; \
		if [ $$rc -ne 0 ]; then status=$$rc; fi; \
	done; exit $$status

clean: ## Remove build outputs, or API data when called as "make api clean"
	@if [ "$(FIRST_GOAL)" = "api" ]; then :; else rm -f $(BINARY); fi

cli:
	@$(MAKE) --no-print-directory cli-$(or $(SUBCOMMAND),help)

cli-help:
	@echo "Usage: make cli <validate|plan|apply|destroy|output|state> [TOPO=$(TOPO)]"

cli-validate: build
	$(SYSBOX) validate

cli-plan: build
	$(SYSBOX) plan

cli-apply: build
	@mkdir -p .sysbox/runs/$(TOPO)
	@if [ "$(TOPO)" = "three-nodes" ] && [ ! -f "$(LAB_SSH_KEY)" ]; then \
		echo "Generating lab SSH keypair: $(LAB_SSH_KEY)"; \
		ssh-keygen -t ed25519 -f "$(LAB_SSH_KEY)" -N "" -C "sysbox-lab" -q; \
		chmod 600 "$(LAB_SSH_KEY)"; \
		chmod 644 "$(LAB_SSH_PUBKEY)"; \
	fi
	LAB_SSH_PUBKEY="$(LAB_SSH_PUBKEY)" $(SYSBOX) apply --auto-approve

cli-destroy: build
	$(SYSBOX) destroy --auto-approve

cli-output: build
	$(SYSBOX) output

cli-state: build
	$(BINARY) --state $(STATE) state list

api:
	@$(MAKE) --no-print-directory api-$(or $(SUBCOMMAND),help)

api-help:
	@echo "Usage: make api <build-api|build-ui|seed|deploy|deploy-full|status|down|clean|logs|config>"

api-build-api:
	docker build --network=host --no-cache -t sysbox:latest .

api-build-ui: web-build
	docker build --network=host -t sysbox-web:latest ./web
	$(COMPOSE) $(COMPOSE_API) -f $(COMPOSE_DIR)/compose.web.yml up -d sysbox-web
	@echo "Web UI: $(WEB_URL)"
	@echo "Remote: http://<host-ip>:$(WEB_HOST_PORT)"

api-seed:
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

api-deploy: api-build-api
	$(COMPOSE) $(COMPOSE_API) up -d
	@echo "API server: $(API_URL)/v1/health"
	@echo "Seed examples with: make api seed"

api-deploy-full: api-deploy .agent-register
	$(COMPOSE) $(COMPOSE_FULL) up -d sysbox-agent
	@echo "Agent: $(AGENT_ID)"

api-status:
	@$(COMPOSE) $(COMPOSE_ALL) ps
	@echo ""
	@printf "API health: "
	@curl -sf "$(API_URL)/v1/health" 2>/dev/null || echo "unreachable"
	@printf "Web health: "
	@curl -sf "http://127.0.0.1:$(WEB_HOST_PORT)/v1/health" 2>/dev/null || echo "unreachable"

.agent-register:
	$(COMPOSE) $(COMPOSE_FULL) run --rm --no-deps --entrypoint sysbox sysbox-agent \
		agent register --api $(AGENT_API_URL) --token "$(SYSBOX_API_TOKEN)" --id $(AGENT_ID) --name $(AGENT_ID) --capabilities $(AGENT_CAPABILITIES) --identity /var/lib/sysbox/agent/identity.json

api-down:
	$(COMPOSE) $(COMPOSE_FULL) down

api-clean:
	$(COMPOSE) $(COMPOSE_FULL) down -v
	@echo "Removing API workspaces under $(API_DATA_DIR)/workspaces"
	@if [ -d "$(API_DATA_DIR)/workspaces" ]; then \
		rm -rf "$(API_DATA_DIR)/workspaces" 2>/dev/null || \
		docker run --rm -v "$(API_DATA_ABS):/var/lib/sysbox" --entrypoint sh sysbox:latest -c 'rm -rf /var/lib/sysbox/workspaces'; \
	fi

api-logs:
	$(COMPOSE) $(COMPOSE_FULL) logs -f

api-config:
	$(COMPOSE) $(COMPOSE_FULL) config

# Compatibility aliases. Prefer the grouped commands above.
validate plan apply destroy output state:
	@if [ "$(FIRST_GOAL)" = "cli" ]; then :; else $(MAKE) --no-print-directory cli-$@; fi

up: apply

build-api build-ui seed deploy deploy-full status logs config:
	@if [ "$(FIRST_GOAL)" = "api" ]; then :; else $(MAKE) --no-print-directory api-$@; fi

down:
	@if [ "$(FIRST_GOAL)" = "api" ]; then :; else $(MAKE) --no-print-directory api-down; fi
