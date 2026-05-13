.PHONY: build test lint clean e2e install

# Resolve go binary (sudo strips PATH, so prefer absolute path).
GO ?= $(shell command -v go 2>/dev/null || echo /usr/local/go/bin/go)

build: build-hook
	$(GO) build -o bin/sysbox ./cmd/sysbox

build-hook:
	$(GO) build -o bin/sysbox-sshd-hook ./cmd/sysbox-sshd-hook

test:
	$(GO) test ./pkg/... -race -cover

lint:
	$(GO) vet ./...
	gofmt -l . | diff -u /dev/null -

clean:
	rm -rf ./bin ./runs

e2e:
	@if [ "$$(id -u)" != "0" ]; then \
		echo "e2e tests require root (netns/veth). Use: sudo -E make e2e"; \
		exit 1; \
	fi
	$(GO) test ./tests/e2e/... -tags=e2e -v -timeout 5m

# Phase 2 sensor tests, split by required privilege level:
#   Layer 1 (no root):   make e2e-sensor-registry
#   Layer 3 (docker grp): make e2e-sensor-live
#   Full (root):          sudo -E make e2e-sensor
e2e-sensor-registry:
	$(GO) test ./tests/e2e/... -tags=e2e -v -run TestPhase2Registry -timeout 2m

e2e-sensor-live:
	$(GO) test ./tests/e2e/... -tags=e2e -v -run TestPhase2LiveTracee -timeout 3m

e2e-sensor:
	@if [ "$$(id -u)" != "0" ]; then \
		echo "Full sensor e2e requires root. Use: sudo -E make e2e-sensor"; \
		exit 1; \
	fi
	$(GO) test ./tests/e2e/... -tags=e2e -v -run TestPhase2 -timeout 5m

# Phase 3 Matcher tests — no root/docker required.
e2e-matcher:
	$(GO) test ./tests/e2e/... -tags=e2e -v -run TestMatcher -timeout 60s

install: build
	install -m 0755 bin/sysbox /usr/local/bin/sysbox
