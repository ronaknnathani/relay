BINARY      = relay
CMD         = ./cmd/relay
BIN_DIR     = $(HOME)/.local/bin
COMMANDS_DIR = $(HOME)/.claude/commands/build
SKILLS_DIR  = $(HOME)/.claude/skills
REPO_DIR    = $(shell pwd)
PKG_DIR     = $(REPO_DIR)/dist/claude

HOST_OS := $(shell uname -s | tr '[:upper:]' '[:lower:]')
HOST_ARCH_RAW := $(shell uname -m)
ifeq ($(HOST_ARCH_RAW),x86_64)
  HOST_ARCH := amd64
else ifeq ($(HOST_ARCH_RAW),aarch64)
  HOST_ARCH := arm64
else
  HOST_ARCH := $(HOST_ARCH_RAW)
endif

.PHONY: all darwin linux host clean install uninstall generate generate-agents

all: darwin linux

darwin:
	GOOS=darwin GOARCH=arm64 go build -o bin/darwin/$(BINARY) $(CMD)

linux:
	GOOS=linux GOARCH=amd64 go build -o bin/linux/$(BINARY) $(CMD)

host:
	GOOS=$(HOST_OS) GOARCH=$(HOST_ARCH) go build -o bin/$(HOST_OS)/$(BINARY) $(CMD)

# generate renders the agent-neutral root source (plugin.json + skills/) into
# the installable Claude package under dist/claude. install consumes that
# generated package, so the on-disk plugin is generated, never hand-maintained.
generate: host
	@echo "Generating Claude package into $(PKG_DIR)..."
	rm -rf $(PKG_DIR)
	$(REPO_DIR)/bin/$(HOST_OS)/$(BINARY) generate --agent claude --src $(REPO_DIR) --out $(PKG_DIR)

# generate-agents renders every registered agent into its stable install path
# under ~/.relay/agents/<agent>/, so path-loaded packages (e.g. Copilot's
# --plugin-dir) resolve. The Claude on-disk plugin is still served via dist/claude
# symlinks below; this only populates the per-agent install tree.
generate-agents: host
	@echo "Generating all agent packages into their install paths..."
	$(REPO_DIR)/bin/$(HOST_OS)/$(BINARY) generate --src $(REPO_DIR)

install: generate generate-agents
	@echo "Installing relay CLI for $(HOST_OS)/$(HOST_ARCH)..."
	mkdir -p $(BIN_DIR)
	ln -sf $(REPO_DIR)/bin/$(HOST_OS)/$(BINARY) $(BIN_DIR)/$(BINARY)
	@echo "Removing legacy command install (package is skills-only)..."
	rm -rf $(COMMANDS_DIR)
	rm -f $(HOME)/.claude/commands/todo.md
	@echo "Installing skills..."
	mkdir -p $(SKILLS_DIR)
	for d in $(PKG_DIR)/skills/*/; do \
		name=$$(basename "$$d"); \
		target="$(SKILLS_DIR)/$$name"; \
		if [ -e "$$target" ] && [ ! -L "$$target" ]; then \
			echo "  skipping $$name: $$target exists and is not a symlink"; \
			continue; \
		fi; \
		rm -f "$$target"; \
		ln -sf "$$d" "$$target"; \
	done
	@echo "Done. Run /reload-plugins or restart Claude Code."

uninstall:
	rm -f $(BIN_DIR)/$(BINARY)
	rm -rf $(COMMANDS_DIR)
	rm -f $(HOME)/.claude/commands/todo.md
	for d in $(PKG_DIR)/skills/*/; do \
		name=$$(basename "$$d"); \
		target="$(SKILLS_DIR)/$$name"; \
		if [ -e "$$target" ] && [ ! -L "$$target" ]; then \
			echo "  skipping $$name: $$target exists and is not a symlink"; \
			continue; \
		fi; \
		rm -f "$$target"; \
	done
	@echo "Uninstalled."

clean:
	rm -rf bin/darwin bin/linux dist
