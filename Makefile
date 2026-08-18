SHELL        := bash
.SHELLFLAGS  := -o pipefail -c

# Pin the Go toolchain so all `go` invocations below (build, mktar, test,
# test-addons, clean) produce byte-identical output regardless of the
# locally installed Go version. Bump deliberately, and keep in sync with
# the `go`/`toolchain` lines in go.mod.
export GOTOOLCHAIN := go1.26.5

BIN_DIR      := bin
EMBED_DIR    := cmd/csb/files

CLIENT_AMD64 := $(BIN_DIR)/csb-host-run.amd64
CLIENT_ARM64 := $(BIN_DIR)/csb-host-run.arm64
CLIENT_TAR_XZ := $(EMBED_DIR)/csb-host-run.tar.xz
CSB_CLI      := $(BIN_DIR)/csb

DIST_DIR      := dist
DIST_PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

GO_BUILD := CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath

GO_SRCS := $(shell find cmd internal -name '*.go') go.mod go.sum

.PHONY: all build build-client build-csb dist test test-addons clean

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
	go run ./internal/tools/mktar $(CLIENT_AMD64) $(CLIENT_ARM64) | xz -9 -c > $@

build-csb: $(CLIENT_TAR_XZ)
	$(GO_BUILD) -o $(CSB_CLI) ./cmd/csb

# Cross-compile csb for release: linux/amd64, linux/arm64, darwin/amd64,
# darwin/arm64, written to dist/csb-<os>-<arch>. Uses the same $(GO_BUILD)
# flags as build-csb so released binaries match locally-built ones. Used
# by the release workflow; safe to run locally too.
dist: $(CLIENT_TAR_XZ)
	mkdir -p $(DIST_DIR)
	for os_arch in $(DIST_PLATFORMS); do \
		GOOS=$${os_arch%/*} GOARCH=$${os_arch#*/} $(GO_BUILD) \
			-o $(DIST_DIR)/csb-$${os_arch%/*}-$${os_arch#*/} ./cmd/csb; \
	done

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
	rm -rf $(DIST_DIR)
	go clean -cache
