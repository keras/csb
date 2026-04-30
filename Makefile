PYTHON := .venv/bin/python3
PYTEST  := $(PYTHON) -m pytest

BIN_DIR      := bin
CSB_BIN_DIR  := src/csb/bin

BROKER     := $(BIN_DIR)/csb-host-broker
BROKER_PKG := $(CSB_BIN_DIR)/csb-host-broker
CLIENT     := $(CSB_BIN_DIR)/csb-host-run

GO_BUILD := CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath

.PHONY: all build build-broker build-client test test-go test-py test-smoke test-host-exec clean

all: build

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------

build: build-broker build-client

build-broker:
	$(GO_BUILD) -o $(BROKER) ./cmd/csb-host-broker
	cp $(BROKER) $(BROKER_PKG)

build-client:
	$(GO_BUILD) -o $(CLIENT) ./cmd/csb-host-run

# ---------------------------------------------------------------------------
# Test
# ---------------------------------------------------------------------------

test: test-go test-py

test-go:
	go test ./internal/...

test-py:
	$(PYTEST) src/csb/ -q -m "not docker and not podman and not smoke and not host_exec"

test-smoke: build
	$(PYTEST) src/csb/ -v -m "smoke and not host_exec"

test-host-exec: build
	$(PYTEST) src/csb/ -v -m "host_exec"

# ---------------------------------------------------------------------------
# Clean
# ---------------------------------------------------------------------------

clean:
	rm -f $(BROKER) $(BROKER_PKG) $(CLIENT)
	go clean -cache
