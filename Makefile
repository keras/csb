PYTHON := .venv/bin/python3
PYTEST  := $(PYTHON) -m pytest

BIN_DIR      := bin
CSB_BIN_DIR  := src/csb/bin
EMBED_DIR    := cmd/csb/files

BROKER     := $(BIN_DIR)/csb-host-broker
BROKER_PKG := $(CSB_BIN_DIR)/csb-host-broker
CLIENT_AMD64 := $(CSB_BIN_DIR)/csb-host-run.amd64
CLIENT_ARM64 := $(CSB_BIN_DIR)/csb-host-run.arm64
CLIENT_AMD64_XZ := $(EMBED_DIR)/csb-host-run.amd64.xz
CLIENT_ARM64_XZ := $(EMBED_DIR)/csb-host-run.arm64.xz
CSB_CLI    := $(BIN_DIR)/csb

GO_BUILD := CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath

.PHONY: all build build-broker build-client build-csb test test-go test-py test-smoke test-host-exec clean

all: build

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------

build: build-broker build-client build-csb

build-broker:
	$(GO_BUILD) -o $(BROKER) ./cmd/csb-host-broker
	cp $(BROKER) $(BROKER_PKG)

build-client: $(CLIENT_AMD64_XZ) $(CLIENT_ARM64_XZ)

$(CLIENT_AMD64):
	GOOS=linux GOARCH=amd64 $(GO_BUILD) -o $@ ./cmd/csb-host-run

$(CLIENT_ARM64):
	GOOS=linux GOARCH=arm64 $(GO_BUILD) -o $@ ./cmd/csb-host-run

$(CLIENT_AMD64_XZ): $(CLIENT_AMD64)
	xz -9 -k -f -c $< > $@

$(CLIENT_ARM64_XZ): $(CLIENT_ARM64)
	xz -9 -k -f -c $< > $@

build-csb: $(CLIENT_AMD64_XZ) $(CLIENT_ARM64_XZ)
	$(GO_BUILD) -o $(CSB_CLI) ./cmd/csb

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
	rm -f $(BROKER) $(BROKER_PKG) $(CLIENT_AMD64) $(CLIENT_ARM64) \
		$(CLIENT_AMD64_XZ) $(CLIENT_ARM64_XZ) $(CSB_CLI)
	go clean -cache
