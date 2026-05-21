BIN_DIR      := bin
EMBED_DIR    := cmd/csb/files

CLIENT_AMD64 := $(BIN_DIR)/csb-host-run.amd64
CLIENT_ARM64 := $(BIN_DIR)/csb-host-run.arm64
CLIENT_TAR_XZ := $(EMBED_DIR)/csb-host-run.tar.xz
CSB_CLI      := $(BIN_DIR)/csb

GO_BUILD := CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath

.PHONY: all build build-client build-csb test clean

all: build

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------

build: build-client build-csb

build-client: $(CLIENT_TAR_XZ)

$(CLIENT_AMD64):
	GOOS=linux GOARCH=amd64 $(GO_BUILD) -o $@ ./cmd/csb-host-run

$(CLIENT_ARM64):
	GOOS=linux GOARCH=arm64 $(GO_BUILD) -o $@ ./cmd/csb-host-run

$(CLIENT_TAR_XZ): $(CLIENT_AMD64) $(CLIENT_ARM64)
	tar --sort=name --mtime='@0' --owner=0 --group=0 --numeric-owner \
	    -cf - -C $(BIN_DIR) csb-host-run.amd64 csb-host-run.arm64 \
	| xz -9 -c > $@

build-csb: $(CLIENT_TAR_XZ)
	$(GO_BUILD) -o $(CSB_CLI) ./cmd/csb

# ---------------------------------------------------------------------------
# Test
# ---------------------------------------------------------------------------

test:
	go test ./...

# ---------------------------------------------------------------------------
# Clean
# ---------------------------------------------------------------------------

clean:
	rm -f $(CLIENT_AMD64) $(CLIENT_ARM64) $(CLIENT_TAR_XZ) $(CSB_CLI)
	go clean -cache
