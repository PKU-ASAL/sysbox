GO ?= $(shell command -v go 2>/dev/null || echo /usr/local/go/bin/go)
GOCACHE ?= /tmp/sysbox-gocache
GOENV := GOCACHE=$(GOCACHE)

ifneq (,$(wildcard .env))
include .env
endif

BINARY := bin/sysbox
INITDIR := pkg/provider/firecracker/initbin
ARCH = $(shell $(GO) env GOARCH)

TOPO ?= two-networks
API_URL ?= http://127.0.0.1:9876
AGENT_API_URL ?= $(API_URL)
API_DATA_DIR ?= $(or $(SYSBOX_HOST_HOME_DIR),.sysbox/api)
API_DATA_ABS := $(abspath $(API_DATA_DIR))
AGENT_ID ?= local-docker
AGENT_SUBSTRATES ?= docker

HCL := examples/$(TOPO)/field.sysbox.hcl
STATE := .sysbox/runs/$(TOPO)/state.json
SYSBOX := $(BINARY) --state $(STATE) -f $(HCL)
SERVICE_SYSBOX := $(BINARY) --api $(API_URL) --topology $(TOPO)
LAB_SSH_KEY ?= .sysbox/runs/$(TOPO)/lab_key
LAB_SSH_PUBKEY ?= $(LAB_SSH_KEY).pub

COMPOSE_DIR := deploy/docker
COMPOSE := docker compose --project-directory .
COMPOSE_API := -f $(COMPOSE_DIR)/compose.yml
COMPOSE_FULL := -f $(COMPOSE_DIR)/compose.yml -f $(COMPOSE_DIR)/compose.agent.yml
COMPOSE_ALL := -f $(COMPOSE_DIR)/compose.yml -f $(COMPOSE_DIR)/compose.agent.yml

FIRST_GOAL := $(firstword $(MAKECMDGOALS))
SUBCOMMAND := $(word 2,$(MAKECMDGOALS))

.DEFAULT_GOAL := help

.PHONY: test-firecracker-e2e
.PHONY: test-docker-generation-e2e
.PHONY: test-docker-adoption-e2e
test-docker-adoption-e2e: ## Verify adoption and cold lifecycle of an isolated Docker container
	SYSBOX_DOCKER_ADOPTION_E2E=1 $(GOENV) $(GO) test -race -tags e2e ./pkg/provider/docker \
	  -run '^TestDockerAdoptionColdLifecycleE2E$$' -count=1 -v -timeout=2m

.PHONY: test-managed-bridge-e2e

test-managed-bridge-e2e: ## Verify Docker managed bridge proof and real TAP fencing on the host network
	@set -eu; test_binary=$$(mktemp /tmp/sysbox-managed-bridge-e2e.XXXXXX); trap 'rm -f "$$test_binary"' EXIT; \
	CGO_ENABLED=0 $(GOENV) $(GO) test -c -tags e2e -o "$$test_binary" ./pkg/provider/docker; \
	docker run --rm --network host --cap-add NET_ADMIN --cap-add SYS_ADMIN --security-opt apparmor=unconfined --device /dev/net/tun \
	  --mount type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock \
	  --mount "type=bind,src=$$test_binary,dst=/managed-bridge.test,readonly" \
	  -e SYSBOX_MANAGED_BRIDGE_E2E=1 --entrypoint /managed-bridge.test alpine:3.22 \
	  -test.run '^TestDockerManagedBridgeTargetE2E$$' -test.v -test.timeout=90s
.PHONY: test-libvirt-generation-e2e
.PHONY: test-network-generation-e2e
.PHONY: test-firecracker-attachment-e2e

.PHONY: test-guesttask-e2e
.PHONY: test-host-supervisor-e2e
.PHONY: test-host-power-e2e
.PHONY: test-host-file-e2e
test-host-file-e2e: ## Verify signed durable file installation and host recovery with real Docker
	@set -eu; file_helper=$$(mktemp /tmp/sysbox-file-helper.XXXXXX); trap 'rm -f "$$file_helper"' EXIT; \
	CGO_ENABLED=0 $(GOENV) $(GO) build -buildvcs=false -o "$$file_helper" ./cmd/sysbox-init; \
	SYSBOX_GUESTTASK_HELPER="$$file_helper" $(GOENV) $(GO) test -race -tags e2e ./pkg/api \
	  -run '^TestHostSupervisorRealFileInstallE2E$$' -count=1 -v -timeout=3m

test-host-power-e2e: ## Verify real Docker pause/resume fencing, durable receipts and cold recovery
	SYSBOX_HOST_POWER_E2E=1 $(GOENV) $(GO) test -race -tags e2e ./pkg/api \
	  -run '^TestHostSupervisorRealNodePowerE2E$$' -count=1 -v -timeout=2m

test-host-supervisor-e2e: ## Verify signed guest tasks, host recovery and successor grants with real Docker
	@set -eu; task_helper=$$(mktemp /tmp/sysbox-supervisor-helper.XXXXXX); trap 'rm -f "$$task_helper"' EXIT; \
	CGO_ENABLED=0 $(GOENV) $(GO) build -buildvcs=false -o "$$task_helper" ./cmd/sysbox-init; \
	SYSBOX_GUESTTASK_HELPER="$$task_helper" $(GOENV) $(GO) test -race -tags e2e ./pkg/api \
	  -run '^TestHostSupervisorRealTaskStopAndSuccessorE2E$$' -count=1 -v -timeout=3m

test-guesttask-e2e: ## Verify durable tasks and cancellation in a disposable private cgroup
	@set -eu; task_binary=$$(mktemp /tmp/sysbox-guesttask-e2e.XXXXXX); trap 'rm -f "$$task_binary"' EXIT; \
	CGO_ENABLED=0 $(GOENV) $(GO) test -c -o "$$task_binary" ./pkg/guesttask; \
	docker run --rm --privileged --cgroupns=private --network none \
	  --mount "type=bind,src=$$task_binary,dst=/task.test,readonly" \
	  alpine:3.22 sh -c 'mkdir /sys/fs/cgroup/sysbox-test; SYSBOX_GUESTTASK_TEST_CGROUP=/sys/fs/cgroup/sysbox-test /task.test -test.run "^TestReal" -test.v -test.timeout=90s'

test-firecracker-attachment-e2e: ## Test real TAP ownership without booting a VM (Linux x86_64)
	@set -eu; test "$$(uname -m)" = x86_64; \
	test_binary=$$(mktemp /tmp/sysbox-tap-e2e.XXXXXX); trap 'rm -f "$$test_binary"' EXIT; \
	CGO_ENABLED=0 $(GOENV) $(GO) test -c -tags e2e -o "$$test_binary" ./pkg/provider/firecracker; \
	docker run --rm --network none --cap-add NET_ADMIN --cap-add SYS_ADMIN --device /dev/net/tun --security-opt apparmor=unconfined \
	  --mount "type=bind,src=$$test_binary,dst=/tap.test,readonly" \
	  --mount "type=bind,src=$$(command -v ip),dst=/usr/local/bin/ip,readonly" \
	  --mount type=bind,src=/lib/x86_64-linux-gnu,dst=/lib/x86_64-linux-gnu,readonly \
	  --mount type=bind,src=/lib64,dst=/lib64,readonly -e SYSBOX_ATTACHMENT_E2E=1 \
	  --entrypoint /tap.test ubuntu:24.04 -test.run '^TestFirecrackerOwnedTapE2E$$' -test.v -test.timeout=90s
.PHONY: test-policy-namespace-e2e
.PHONY: test-firewall-recovery-e2e

test-firewall-recovery-e2e: ## Run real Docker/nft firewall engine recovery (Linux x86_64)
	@set -eu; test "$$(uname -m)" = x86_64; \
	test_dir=$$(mktemp -d /tmp/sysbox-firewall-e2e.XXXXXX); \
	trap 'rm -f "$$test_dir/firewall.test" "$$test_dir/sysbox-netns"; rmdir "$$test_dir"' EXIT; \
	CGO_ENABLED=0 $(GOENV) $(GO) test -c -tags e2e -o "$$test_dir/firewall.test" ./pkg/runtime; \
	CGO_ENABLED=0 $(GOENV) $(GO) build -buildvcs=false -o "$$test_dir/sysbox-netns" ./cmd/sysbox-netns; \
	docker run --rm --network none --pid host --cap-add NET_ADMIN --cap-add SYS_ADMIN --cap-add SYS_PTRACE --security-opt apparmor=unconfined \
	  --mount type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock \
	  --mount "type=bind,src=$$test_dir/firewall.test,dst=/firewall.test,readonly" \
	  --mount "type=bind,src=$$test_dir/sysbox-netns,dst=/usr/local/bin/sysbox-netns,readonly" \
	  --mount "type=bind,src=$$(command -v nft),dst=/usr/local/bin/nft,readonly" \
	  --mount "type=bind,src=$$(command -v nsenter),dst=/usr/local/bin/nsenter,readonly" \
	  --mount type=bind,src=/lib/x86_64-linux-gnu,dst=/lib/x86_64-linux-gnu,readonly \
	  --mount type=bind,src=/lib64,dst=/lib64,readonly -e SYSBOX_FIREWALL_E2E=1 \
	  --entrypoint /firewall.test ubuntu:24.04 -test.run '^TestFirewallRealRepositoryRecoveryE2E$$' -test.v -test.timeout=2m

test-policy-namespace-e2e: ## Verify real nft namespace FD isolation in a disposable container (Linux x86_64)
	@set -eu; test "$$(uname -m)" = x86_64; \
	test_dir=$$(mktemp -d /tmp/sysbox-policy-fd.XXXXXX); \
	trap 'rm -f "$$test_dir/network.test" "$$test_dir/sysbox-netns"; rmdir "$$test_dir"' EXIT; \
	CGO_ENABLED=0 $(GOENV) $(GO) test -c -tags e2e -o "$$test_dir/network.test" ./pkg/provider/network; \
	CGO_ENABLED=0 $(GOENV) $(GO) build -buildvcs=false -o "$$test_dir/sysbox-netns" ./cmd/sysbox-netns; \
	docker run --rm --network none --cap-add NET_ADMIN --cap-add SYS_ADMIN --security-opt apparmor=unconfined \
	  --mount "type=bind,src=$$test_dir/network.test,dst=/network.test,readonly" \
	  --mount "type=bind,src=$$test_dir/sysbox-netns,dst=/usr/local/bin/sysbox-netns,readonly" \
	  --mount "type=bind,src=$$(command -v nft),dst=/usr/local/bin/nft,readonly" \
	  --mount "type=bind,src=$$(command -v nsenter),dst=/usr/local/bin/nsenter,readonly" \
	  --mount type=bind,src=/lib/x86_64-linux-gnu,dst=/lib/x86_64-linux-gnu,readonly \
	  --mount type=bind,src=/lib64,dst=/lib64,readonly -e SYSBOX_NETWORK_E2E=1 \
	  --entrypoint /network.test ubuntu:24.04 -test.run '^TestPolicy(NamespaceFDReplacement|AtomicReplacementRollback|GenerationFencing)E2E$$' -test.v -test.timeout=90s

test-network-generation-e2e: ## Run real network generation tests in a disposable isolated container
	@set -eu; test_binary=$$(mktemp /tmp/sysbox-network-e2e.XXXXXX); \
	trap 'rm -f "$$test_binary"' EXIT; \
	CGO_ENABLED=0 $(GOENV) $(GO) test -c -tags e2e -o "$$test_binary" ./pkg/provider/network; \
	docker run --rm --network none --cap-add NET_ADMIN --cap-add SYS_ADMIN --security-opt apparmor=unconfined \
	  --mount "type=bind,src=$$test_binary,dst=/network.test,readonly" -e SYSBOX_NETWORK_E2E=1 \
	  alpine:3.22 /network.test -test.run '^(TestIsolatedNetworkGenerationRecoveryE2E|TestRootBridgeProxyLifecycleE2E|TestOwnedAttachmentGenerationE2E|TestNetworkBridgeReplacementRejectedE2E|TestAttachmentTargetFencingE2E|TestRootAttachmentRejectsReplacedNetworkNamespaceE2E)$$' -test.v -test.timeout=90s

test-libvirt-generation-e2e: ## Run real libvirt managed-generation effect rejection
	SYSBOX_LIBVIRT_E2E=1 $(GOENV) $(GO) test -tags e2e ./pkg/provider/libvirt -run '^TestLibvirtRealGenerationEffectsE2E$$' -count=1 -v -timeout=2m

test-docker-generation-e2e: ## Run real Docker managed-generation effect rejection
	$(GOENV) $(GO) test -tags e2e ./pkg/provider/docker -run '^TestDockerRealGenerationEffectsE2E$$' -count=1 -v -timeout=3m

.PHONY: test-remote-docker-fault-e2e
test-remote-docker-fault-e2e: ## Real HTTP/Agent/Docker completion-loss and API restart acceptance
	SYSBOX_REMOTE_DOCKER_E2E=1 $(GOENV) $(GO) test -tags e2e ./pkg/api -run '^TestRemoteDockerCompletionLossRecoveryE2E$$' -count=1 -v -timeout=3m

.PHONY: test-p9-ssh-two-vm
test-p9-ssh-two-vm: ## Run isolated two-VM managed SSH login and reset acceptance
	@test -n "$(SYSBOX_P9_LAB_ROOT)" -a -n "$(SYSBOX_CLI_BINARY)" -a -n "$(SYSBOX_GUESTTASK_HELPER)" || { echo 'Set SYSBOX_P9_LAB_ROOT, SYSBOX_CLI_BINARY and SYSBOX_GUESTTASK_HELPER'; exit 2; }
	python3 scripts/acceptance/ssh_lifecycle.py --root "$(SYSBOX_P9_LAB_ROOT)" --binary "$(SYSBOX_CLI_BINARY)" --helper "$(SYSBOX_GUESTTASK_HELPER)"

.PHONY: test-p9-ssh-reboot
test-p9-ssh-reboot: ## Reboot dedicated lab VMs and verify managed SSH recovery
	@test -n "$(SYSBOX_P9_LAB_ROOT)" -a -n "$(SYSBOX_CLI_BINARY)" -a -n "$(SYSBOX_GUESTTASK_HELPER)" || { echo 'Set SYSBOX_P9_LAB_ROOT, SYSBOX_CLI_BINARY and SYSBOX_GUESTTASK_HELPER'; exit 2; }
	python3 scripts/acceptance/ssh_lifecycle.py --root "$(SYSBOX_P9_LAB_ROOT)" --binary "$(SYSBOX_CLI_BINARY)" --helper "$(SYSBOX_GUESTTASK_HELPER)" --reboot-vms

.PHONY: test-p9-real
test-p9-real: build-all ## Run implemented real-provider gates; reject skips and missing test matches
	@set -eu; \
	test -x "$${SYSBOX_FIRECRACKER_BIN:?set Firecracker binary}"; \
	test -f "$${SYSBOX_FIRECRACKER_KERNEL:?set Firecracker kernel}"; \
	test -f "$${SYSBOX_FIRECRACKER_ROOTFS:?set Firecracker rootfs}"; \
	acceptance_dir=$$(mktemp -d /tmp/sysbox-acceptance.XXXXXX); \
	trap 'rm -f "$$acceptance_dir/check" "$$acceptance_dir/helper" "$$acceptance_dir/sysbox"; rmdir "$$acceptance_dir"' EXIT; \
	$(GOENV) $(GO) build -buildvcs=false -o "$$acceptance_dir/check" ./scripts/acceptance; \
	CGO_ENABLED=0 $(GOENV) $(GO) build -buildvcs=false -o "$$acceptance_dir/helper" ./cmd/sysbox-init; \
	$(GOENV) $(GO) build -buildvcs=false -o "$$acceptance_dir/sysbox" ./cmd/sysbox; \
	export GOCACHE="$(GOCACHE)" SYSBOX_GUESTTASK_HELPER="$$acceptance_dir/helper" SYSBOX_CLI_BINARY="$$acceptance_dir/sysbox"; \
	SYSBOX_REMOTE_DOCKER_E2E=1 SYSBOX_HOST_POWER_E2E=1 "$$acceptance_dir/check" -go "$(GO)" -race -package ./pkg/api -timeout 6m \
	  -tests TestRemoteDockerGCBatchSequentialRecoveryE2E,TestRemoteDockerAdoptionInspectionE2E,TestRemoteDockerGCDispatchAndLostReceiptE2E,TestRemoteDockerCompletionLossRecoveryE2E,TestRemoteDockerReturnedDestroyRecoveryE2E,TestRemoteDockerColdReturnedDestroyRecoveryE2E,TestRemoteDockerColdResetContinuationE2E,TestRemoteDockerRepeatedResetContinuationE2E,TestRemoteDockerSSHAccessRunE2E,TestRemoteDockerTargetedResetRunE2E,TestRemoteDockerTargetedResetSSHRunE2E,TestHostSupervisorRealTaskStopAndSuccessorE2E,TestHostSupervisorRealNodePowerE2E,TestHostSupervisorRealFileInstallE2E,TestPublicSSHAgentDockerE2E,TestLocalSSHColdReceiptRecoveryE2E; \
	"$$acceptance_dir/check" -go "$(GO)" -race -package ./cmd/sysbox/commands -tests TestLocalCLIManagedSSHApplyRotateDestroyE2E; \
	"$$acceptance_dir/check" -go "$(GO)" -race -package ./pkg/sshaccess -tests TestRealSSHDDeploymentReloadRecoveryE2E,TestRealSSHDEffectiveConfigurationConflictE2E; \
	SYSBOX_DOCKER_ADOPTION_E2E=1 SYSBOX_REMOTE_DOCKER_E2E=1 "$$acceptance_dir/check" -go "$(GO)" -race -package ./pkg/provider/docker -tests TestDockerAdoptionColdLifecycleE2E,TestDockerProductionSSHAccessE2E,TestDockerRealLostDeleteResponseE2E,TestDockerRealGCExactAbsenceE2E; \
	"$$acceptance_dir/check" -go "$(GO)" -package ./pkg/provider/firecracker -timeout 10m \
	  -tests TestFirecrackerRealGenerationLifecycleE2E,TestFirecrackerRealResetGenerationAndColdAdoptionE2E,TestFirecrackerRealCrossProcessRecoveryE2E,TestFirecrackerRealControllerCrashRecoveryE2E; \
	SYSBOX_LIBVIRT_E2E=1 "$$acceptance_dir/check" -go "$(GO)" -package ./pkg/provider/libvirt -tests TestLibvirtUnownedAdoptionColdLifecycleE2E,TestLibvirtRealGenerationEffectsE2E

.PHONY: help build build-all test docs-test test-e2e test-docker-launch test-docker-network-alias test-privileged-compile test-privileged test-privileged-container prepare-libvirt-cloud-image test-heterogeneous-matrix test-heterogeneous-reset test-windows-tools prepare-windows10-media build-windows10-image test-windows10-libvirt release-test release-workflow-test release-build release-verify lint ci clean \
	cli api \
	cli-help cli-validate cli-plan cli-apply cli-destroy cli-output cli-state \
	api-help api-build-api api-seed api-deploy api-deploy-full api-status api-down api-clean api-logs api-config \
	validate plan apply destroy output state up \
	build-api seed deploy deploy-full status down logs config \
	.agent-register

help: ## Show command groups
	@echo "Usage:"
	@echo "  make cli <validate|plan|apply|destroy|output|state> [TOPO=two-networks]"
	@echo "  make api <build-api|seed|deploy|deploy-full|status|down|clean|logs|config>"
	@echo ""
	@echo "Common:"
	@echo "  make build          Build bin/sysbox"
	@echo "  make test           Run unit tests"
	@echo "  make lint           Run go vet"
	@echo "  make test-e2e       Run API e2e smoke test against make api deploy-full"
	@echo "  make test-firecracker-e2e  Run jailed Firecracker/KVM lifecycle acceptance"
	@echo "  make test-docker-launch  Run Docker ENTRYPOINT/CMD override acceptance"
	@echo "  make test-docker-network-alias  Run Docker managed-network DNS alias acceptance"
	@echo "  make test-privileged-compile  Compile privileged recovery tests without running them"
	@echo "  make test-privileged          Run privileged recovery tests (requires root/CAP_NET_ADMIN)"
	@echo "  make test-privileged-container  Run privileged acceptance tests through Docker"
	@echo "  make prepare-libvirt-cloud-image  Cache the pinned Ubuntu libvirt image"
	@echo "  make test-heterogeneous-matrix  Run the full Docker/Firecracker/libvirt matrix"
	@echo "  make test-windows-tools  Test Windows image preparation tooling"
	@echo "  make prepare-windows10-media  Cache verified Windows and VirtIO installer media"
	@echo "  make build-windows10-image  Build a Windows 10 qcow2 with native libvirt"
	@echo "  make test-windows10-libvirt  Run the opt-in Windows domain-start lifecycle probe"

$(BINARY):
	@test -x "$@" || { echo "Build or install matching binaries first: bash scripts/release/build-binaries.sh bin" >&2; exit 1; }

build: $(INITDIR)/sysbox-init.linux-$(ARCH).bin ## Build bin/sysbox
	$(GOENV) CGO_ENABLED=0 $(GO) build -buildvcs=false -o $(BINARY) ./cmd/sysbox

build-all: ## Build sysbox-init for amd64 and arm64
	$(MAKE) $(INITDIR)/sysbox-init.linux-amd64.bin $(INITDIR)/sysbox-init.linux-arm64.bin

.PHONY: force-guest-build
force-guest-build:

# Go's cache tracks all transitive sources. Do not maintain a partial source
# list here: missing a guesttask dependency would silently embed an old helper.
$(INITDIR)/sysbox-init.linux-%.bin: force-guest-build
	@set -eu; guest_build=$$(mktemp -d /tmp/sysbox-init-build.XXXXXX); \
	trap 'rm -f "$$guest_build/sysbox-init"; rmdir "$$guest_build"' EXIT; \
	$(GOENV) GOOS=linux GOARCH=$* CGO_ENABLED=0 $(GO) build -buildvcs=false -ldflags="-s -w" -o "$$guest_build/sysbox-init" ./cmd/sysbox-init; \
	mv "$$guest_build/sysbox-init" "$@"

test: ## Run unit tests
	$(GOENV) $(GO) test ./...

docs-test: ## Validate canonical documentation structure and links
	$(GOENV) $(GO) run ./scripts/docscheck

test-e2e: ## Run black-box API e2e tests
	bash tests/e2e/api_smoke.sh

test-firecracker-e2e: build-all ## Run jailed Firecracker/KVM lifecycle E2E (requires prepared root host)
	$(GOENV) $(GO) test -tags e2e -v ./pkg/provider/firecracker -run '^Test(FirecrackerReal|JailerReal).*E2E$$' -count=1 -timeout=10m

test-docker-launch: ## Run Docker launch override lifecycle acceptance
	bash tests/e2e/docker_launch_override.sh

test-docker-network-alias: ## Run Docker managed-network DNS alias lifecycle acceptance
	bash tests/e2e/docker_network_alias.sh

test-privileged-compile: ## Compile privileged recovery tests without running them
	$(GOENV) $(GO) test -tags e2e -run '^$$' ./pkg/api ./pkg/provider/network ./pkg/provider/docker

test-privileged: ## Run privileged recovery tests (requires root/CAP_NET_ADMIN)
	$(GOENV) $(GO) test -tags e2e -v -run '^Test(OwnedPolicy|DockerOwnedPolicy).*E2E$$' ./pkg/provider/network ./pkg/provider/docker

test-privileged-container: ## Run privileged acceptance tests through Docker
	bash tests/e2e/privileged_container.sh

prepare-libvirt-cloud-image: ## Cache and verify the pinned Ubuntu libvirt image
	bash scripts/prepare-libvirt-cloud-image.sh

test-heterogeneous-matrix: ## Run the full heterogeneous IPv4 acceptance matrix
	bash tests/e2e/heterogeneous_matrix.sh

test-heterogeneous-reset: ## Run three full and targeted heterogeneous reset cycles
	bash tests/e2e/heterogeneous_reset.sh

test-windows-tools: ## Test Windows unattended image tooling without libvirt side effects
	bash tests/scripts/windows_tools_test.sh
	python3 -m unittest discover -s scripts/windows -p test_artifact.py

prepare-windows10-media: ## Cache checksum-verified official Windows and VirtIO media
	bash scripts/windows/prepare-media.sh

build-windows10-image: ## Build the Windows 10 baseline via native libvirt and Autounattend.xml
	bash scripts/windows/build-windows10.sh

test-windows10-libvirt: ## Run the opt-in experimental Windows domain-start lifecycle probe
	bash tests/e2e/windows10_libvirt.sh

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

lint: ## Run go vet
	$(GOENV) $(GO) vet ./...

ci: build lint test docs-test ## Run the local CI gate
	@status=0; for topo in two-networks three-nodes microvm mixed libvirt-vm controlled-egress; do \
		output="$$( $(BINARY) -f examples/$$topo/field.sysbox.hcl validate 2>&1 )"; rc=$$?; \
		printf "  %-14s%s\n" "$$topo:" "$$(printf '%s\n' "$$output" | head -1)"; \
		if [ $$rc -ne 0 ]; then status=$$rc; fi; \
	done; exit $$status

clean: ## Remove build outputs, or API data when called as "make api clean"
	@if [ "$(FIRST_GOAL)" = "api" ]; then :; else rm -f $(BINARY); fi

cli:
	@$(MAKE) --no-print-directory cli-$(or $(SUBCOMMAND),help)

cli-help:
	@echo "Usage: make cli <validate|plan|apply|destroy|output|state> [TOPO=$(TOPO)]"

cli-validate: $(BINARY)
	$(BINARY) -f $(HCL) validate

cli-plan: $(BINARY)
	$(SERVICE_SYSBOX) plan -f $(HCL)

cli-apply: $(BINARY)
	$(SERVICE_SYSBOX) apply -f $(HCL) --auto-approve

cli-destroy: $(BINARY)
	$(SERVICE_SYSBOX) destroy --auto-approve

cli-output: build
	$(SYSBOX) output

cli-state: build
	$(BINARY) --state $(STATE) state list

api:
	@$(MAKE) --no-print-directory api-$(or $(SUBCOMMAND),help)

api-help:
	@echo "Usage: make api <build-api|seed|deploy|deploy-full|status|down|clean|logs|config>"

api-build-api:
	docker build --network=host --no-cache -t sysbox:latest .

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

api-deploy:
	$(COMPOSE) $(COMPOSE_API) up -d --no-build
	@echo "API server: $(API_URL)/v1/health"
	@echo "Seed examples with: make api seed"

api-deploy-full: api-deploy
	$(COMPOSE) $(COMPOSE_FULL) up -d --no-build sysbox-agent
	@echo "Agent: $(AGENT_ID)"

api-status:
	@$(COMPOSE) $(COMPOSE_ALL) ps
	@echo ""
	@printf "API health: "
	@curl -sf "$(API_URL)/v1/health" 2>/dev/null || echo "unreachable"

.agent-register: $(BINARY)
	@mkdir -p .sysbox/enrollment
	@test ! -e .sysbox/enrollment/identity.json || { echo "Enrollment already exists; inspect it before registering again"; exit 1; }
	$(BINARY) agent register --api $(AGENT_API_URL) --token "$(SYSBOX_API_TOKEN)" --id $(AGENT_ID) --name $(AGENT_ID) --substrates $(AGENT_SUBSTRATES) --identity .sysbox/enrollment/identity.json
	@echo "Install this identity using deploy/prepare-worker.sh; the worker never receives the enrollment token."

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

build-api seed deploy deploy-full status logs config:
	@if [ "$(FIRST_GOAL)" = "api" ]; then :; else $(MAKE) --no-print-directory api-$@; fi

down:
	@if [ "$(FIRST_GOAL)" = "api" ]; then :; else $(MAKE) --no-print-directory api-down; fi
