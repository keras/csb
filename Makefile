SHELL        := sh -o pipefail

BIN_DIR      := bin
EMBED_DIR    := cmd/csb/files

CLIENT_AMD64 := $(BIN_DIR)/csb-host-run.amd64
CLIENT_ARM64 := $(BIN_DIR)/csb-host-run.arm64
CLIENT_TAR_XZ := $(EMBED_DIR)/csb-host-run.tar.xz
CSB_CLI      := $(BIN_DIR)/csb

GO_BUILD := CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath

GO_SRCS := $(shell find cmd internal -name '*.go') go.mod go.sum

.PHONY: all build build-client build-csb test test-addons clean

all: build

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------

build: build-csb

build-client: $(CLIENT_TAR_XZ)

$(CLIENT_AMD64): $(GO_SRCS)
	GOOS=linux GOARCH=amd64 $(GO_BUILD) -o $@ ./cmd/csb-host-run

$(CLIENT_ARM64): $(GO_SRCS)
	GOOS=linux GOARCH=arm64 $(GO_BUILD) -o $@ ./cmd/csb-host-run

$(CLIENT_TAR_XZ): $(CLIENT_AMD64) $(CLIENT_ARM64)
	COPYFILE_DISABLE=1 tar -cf - -C $(BIN_DIR) csb-host-run.amd64 csb-host-run.arm64 | xz -9 -c > $@

build-csb: $(CLIENT_TAR_XZ)
	$(GO_BUILD) -o $(CSB_CLI) ./cmd/csb

# ---------------------------------------------------------------------------
# Test
# ---------------------------------------------------------------------------

test:
	go test ./...

# Black-box addon tests: for each cmd/csb/files/addons/<name>/test.sh,
# launches a container with that addon enabled and runs the script inside.
# Requires Docker or Podman. Slow; run on demand.
# Filter with RUN=<regexp>, e.g. `make test-addons RUN=TestSystemd`.
test-addons: $(CSB_CLI)
	go test -tags addons -count=1 -timeout 30m $(if $(RUN),-run '$(RUN)') ./internal/addons/...

# ---------------------------------------------------------------------------
# Clean
# ---------------------------------------------------------------------------

clean:
	rm -f $(CLIENT_AMD64) $(CLIENT_ARM64) $(CLIENT_TAR_XZ) $(CSB_CLI)
	go clean -cache
