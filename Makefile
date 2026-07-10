BINARY      = relay
CMD         = ./cmd/relay
BIN_DIR     = $(HOME)/.local/bin
REPO_DIR    = $(shell pwd)
CLAUDE_PKG_DIR = $(REPO_DIR)/dist/claude
TOOLS_DIR   = $(REPO_DIR)/.tools
TOOLS_BIN   = $(TOOLS_DIR)/bin
GOLANGCI_LINT_CACHE = $(TOOLS_DIR)/golangci-lint-cache
GOLANGCI_LINT_VERSION ?= v2.12.2
GORELEASER_VERSION ?= latest
GOLANGCI_LINT = $(TOOLS_BIN)/golangci-lint
GORELEASER = $(TOOLS_BIN)/goreleaser

HOST_OS := $(shell uname -s | tr '[:upper:]' '[:lower:]')
HOST_ARCH_RAW := $(shell uname -m)
ifeq ($(HOST_ARCH_RAW),x86_64)
  HOST_ARCH := amd64
else ifeq ($(HOST_ARCH_RAW),aarch64)
  HOST_ARCH := arm64
else
  HOST_ARCH := $(HOST_ARCH_RAW)
endif

.PHONY: all darwin linux host clean install install-copilot install-codex uninstall generate generate-agents install-tools lint release-check

all: darwin linux

darwin:
	GOOS=darwin GOARCH=arm64 go build -o bin/darwin/$(BINARY) $(CMD)

linux:
	GOOS=linux GOARCH=amd64 go build -o bin/linux/$(BINARY) $(CMD)

host:
	GOOS=$(HOST_OS) GOARCH=$(HOST_ARCH) go build -o bin/$(HOST_OS)/$(BINARY) $(CMD)

install-tools: $(GOLANGCI_LINT) $(GORELEASER)

$(GOLANGCI_LINT):
	mkdir -p $(TOOLS_BIN)
	GOBIN=$(TOOLS_BIN) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

$(GORELEASER):
	mkdir -p $(TOOLS_BIN)
	GOBIN=$(TOOLS_BIN) go install github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)

lint: $(GOLANGCI_LINT)
	mkdir -p $(GOLANGCI_LINT_CACHE)
	GOLANGCI_LINT_CACHE=$(GOLANGCI_LINT_CACHE) $(GOLANGCI_LINT) run ./...

release-check: $(GORELEASER)
	$(GORELEASER) check

# generate renders the agent-neutral root source (plugin.json + skills/) into
# the Claude package under dist/claude for developer inspection.
generate: host
	@echo "Generating Claude package into $(CLAUDE_PKG_DIR)..."
	rm -rf $(CLAUDE_PKG_DIR)
	$(REPO_DIR)/bin/$(HOST_OS)/$(BINARY) generate --agent claude --src $(REPO_DIR) --out $(CLAUDE_PKG_DIR)

# generate-agents renders every registered agent into its stable source path
# under ~/.relay/agents/<agent>/. setup links those generated skills into each
# agent's personal skills directory.
generate-agents: host
	@echo "Generating all agent packages into their install paths..."
	rm -rf $(HOME)/.relay/agents
	$(REPO_DIR)/bin/$(HOST_OS)/$(BINARY) generate --src $(REPO_DIR)

install: host
	@echo "Installing relay CLI for $(HOST_OS)/$(HOST_ARCH)..."
	mkdir -p $(BIN_DIR)
	ln -sf $(REPO_DIR)/bin/$(HOST_OS)/$(BINARY) $(BIN_DIR)/$(BINARY)
	@echo "Done. Run relay setup <agent> to install skills."

install-copilot: host
	$(REPO_DIR)/bin/$(HOST_OS)/$(BINARY) setup copilot --src $(REPO_DIR)

install-codex: host
	$(REPO_DIR)/bin/$(HOST_OS)/$(BINARY) setup codex --src $(REPO_DIR)

uninstall: host
	$(REPO_DIR)/bin/$(HOST_OS)/$(BINARY) setup claude --uninstall --src $(REPO_DIR)
	$(REPO_DIR)/bin/$(HOST_OS)/$(BINARY) setup codex --uninstall --src $(REPO_DIR)
	$(REPO_DIR)/bin/$(HOST_OS)/$(BINARY) setup copilot --uninstall --src $(REPO_DIR)
	rm -f $(BIN_DIR)/$(BINARY)
	@echo "Uninstalled."

clean:
	rm -rf bin/darwin bin/linux dist
