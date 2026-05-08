.PHONY: build test lint clean e2e install

# Resolve go binary (sudo strips PATH, so prefer absolute path).
GO ?= $(shell command -v go 2>/dev/null || echo /usr/local/go/bin/go)

build:
	$(GO) build -o bin/sysbox ./cmd/sysbox

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

install: build
	install -m 0755 bin/sysbox /usr/local/bin/sysbox
