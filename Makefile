BINARY      = relay
CMD         = ./cmd/relay
BIN_DIR     = $(HOME)/.local/bin
COMMANDS_DIR = $(HOME)/.claude/commands/build
CLAUDE_SKILLS_DIR = $(HOME)/.claude/skills
COPILOT_SKILLS_DIR = $(HOME)/.copilot/skills
REPO_DIR    = $(shell pwd)
CLAUDE_PKG_DIR = $(REPO_DIR)/dist/claude
COPILOT_PKG_DIR = $(HOME)/.relay/agents/copilot
MANAGE_SKILLS = $(REPO_DIR)/scripts/manage-skills.sh
# Roots relay considers its own when replacing/removing skill symlinks: the repo
# (Claude's dist package) and ~/.relay (per-agent generated packages).
MANAGED_ROOTS = $(REPO_DIR) $(HOME)/.relay

HOST_OS := $(shell uname -s | tr '[:upper:]' '[:lower:]')
HOST_ARCH_RAW := $(shell uname -m)
ifeq ($(HOST_ARCH_RAW),x86_64)
  HOST_ARCH := amd64
else ifeq ($(HOST_ARCH_RAW),aarch64)
  HOST_ARCH := arm64
else
  HOST_ARCH := $(HOST_ARCH_RAW)
endif

.PHONY: all darwin linux host clean install install-copilot uninstall generate generate-agents

all: darwin linux

darwin:
	GOOS=darwin GOARCH=arm64 go build -o bin/darwin/$(BINARY) $(CMD)

linux:
	GOOS=linux GOARCH=amd64 go build -o bin/linux/$(BINARY) $(CMD)

host:
	GOOS=$(HOST_OS) GOARCH=$(HOST_ARCH) go build -o bin/$(HOST_OS)/$(BINARY) $(CMD)

# generate renders the agent-neutral root source (plugin.json + skills/) into
# the installable Claude package under dist/claude. install consumes that
# generated package for Claude's ~/.claude/skills links, so the on-disk plugin
# is generated, never hand-maintained.
generate: host
	@echo "Generating Claude package into $(CLAUDE_PKG_DIR)..."
	rm -rf $(CLAUDE_PKG_DIR)
	$(REPO_DIR)/bin/$(HOST_OS)/$(BINARY) generate --agent claude --src $(REPO_DIR) --out $(CLAUDE_PKG_DIR)

# generate-agents renders every registered agent into its stable source path
# under ~/.relay/agents/<agent>/. install links those generated skills into each
# agent's personal skills directory.
generate-agents: host
	@echo "Generating all agent packages into their install paths..."
	rm -rf $(HOME)/.relay/agents
	$(REPO_DIR)/bin/$(HOST_OS)/$(BINARY) generate --src $(REPO_DIR)

install: generate generate-agents
	@echo "Installing relay CLI for $(HOST_OS)/$(HOST_ARCH)..."
	mkdir -p $(BIN_DIR)
	ln -sf $(REPO_DIR)/bin/$(HOST_OS)/$(BINARY) $(BIN_DIR)/$(BINARY)
	@echo "Removing legacy command install (package is skills-only)..."
	rm -rf $(COMMANDS_DIR)
	rm -f $(HOME)/.claude/commands/todo.md
	@echo "Installing Claude skills..."
	@sh $(MANAGE_SKILLS) link "$(CLAUDE_PKG_DIR)" "$(CLAUDE_SKILLS_DIR)" $(MANAGED_ROOTS)
	@echo "Installing Copilot skills..."
	@sh $(MANAGE_SKILLS) link "$(COPILOT_PKG_DIR)" "$(COPILOT_SKILLS_DIR)" $(MANAGED_ROOTS)
	@echo "Done. Run /reload-plugins or restart Claude Code; restart Copilot sessions to pick up regenerated skills."

install-copilot: host
	@echo "Generating Copilot package into $(COPILOT_PKG_DIR)..."
	rm -rf $(COPILOT_PKG_DIR)
	$(REPO_DIR)/bin/$(HOST_OS)/$(BINARY) generate --agent copilot --src $(REPO_DIR) --out $(COPILOT_PKG_DIR)
	@echo "Installing Copilot skills..."
	@sh $(MANAGE_SKILLS) link "$(COPILOT_PKG_DIR)" "$(COPILOT_SKILLS_DIR)" $(MANAGED_ROOTS)
	@echo "Done. Copilot skills are available from $(COPILOT_SKILLS_DIR)."

uninstall:
	rm -f $(BIN_DIR)/$(BINARY)
	rm -rf $(COMMANDS_DIR)
	rm -f $(HOME)/.claude/commands/todo.md
	@sh $(MANAGE_SKILLS) unlink "$(CLAUDE_PKG_DIR)" "$(CLAUDE_SKILLS_DIR)" $(MANAGED_ROOTS)
	@sh $(MANAGE_SKILLS) unlink "$(COPILOT_PKG_DIR)" "$(COPILOT_SKILLS_DIR)" $(MANAGED_ROOTS)
	@echo "Uninstalled."

clean:
	rm -rf bin/darwin bin/linux dist
