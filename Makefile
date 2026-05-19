# azcost — Azure cost CLI.
#
# Common targets:
#   make build      — build ./azcost in repo root
#   make install    — install to ~/.local/bin/azcost  (no sudo)
#   make uninstall  — remove from ~/.local/bin
#   make test       — go test -race -count=1 ./...
#   make completion — print shell completion install instructions
#   make clean      — remove build artifacts

VERSION   ?= $(shell git -C $(CURDIR) describe --tags --always --dirty 2>/dev/null || echo "0.1.0")
COMMIT    ?= $(shell git -C $(CURDIR) rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE      ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS   := -s -w \
             -X main.version=$(VERSION) \
             -X main.commit=$(COMMIT) \
             -X main.buildDate=$(DATE)

INSTALL_DIR ?= $(HOME)/.local/bin
BIN          := azcost

.PHONY: all build install uninstall test clean completion help

all: build

build:
	@echo "→ building $(BIN) v$(VERSION) (commit $(COMMIT))"
	@CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/azcost
	@./$(BIN) --version

install: build
	@mkdir -p $(INSTALL_DIR)
	@install -m 0755 $(BIN) $(INSTALL_DIR)/$(BIN)
	@echo "✓ installed → $(INSTALL_DIR)/$(BIN)"
	@command -v $(BIN) >/dev/null && echo "  in PATH ✓" || echo "  ⚠ $(INSTALL_DIR) not in PATH; add to ~/.bashrc"
	@$(BIN) --version 2>/dev/null || true

uninstall:
	@rm -f $(INSTALL_DIR)/$(BIN)
	@echo "✓ removed $(INSTALL_DIR)/$(BIN)"

test:
	@go test -race -count=1 ./...

clean:
	@rm -f $(BIN)
	@echo "✓ cleaned"

completion:
	@echo "Bash:  azcost completion bash | sudo tee /etc/bash_completion.d/azcost"
	@echo "Zsh:   azcost completion zsh  > $${fpath[1]}/_azcost  # then exec zsh"
	@echo "Fish:  azcost completion fish > ~/.config/fish/completions/azcost.fish"

help:
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "%-12s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
