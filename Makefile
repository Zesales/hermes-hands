# Dev helpers for the Go build. End users install via install.sh (see README);
# `make` is only for working ON hermes-hands.
#
# There is no VERSION file — git tags are the source of truth. `git describe`
# (restricted to real vX.Y.Z tags) gives `0.10.0` on a release,
# `0.10.0-3-gabc123` in between; a checkout with no such tag yet falls back to
# `0.0.0-dev`. release.yml overrides with the tag it is about to cut:
#   make VERSION=0.10.0 release
VERSION  ?= $(shell { git describe --tags --match 'v[0-9]*.[0-9]*.[0-9]*' --dirty 2>/dev/null || echo 0.0.0-dev; } | sed 's/^v//')
GIT_SHA  := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS  := -s -w -X main.version=$(VERSION) -X main.commit=$(GIT_SHA)
PREFIX   ?= $(HOME)/.local
BINDIR   ?= $(PREFIX)/bin
# Linux + macOS only. On Windows hermes-hands runs inside WSL (a Linux binary):
# native Windows has no `bash` for the `shell` tool, no /dev/tty, no $HOME.
GOOSARCH := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

# `make dev` runs a self-contained dev instance out of ./.dev/ (gitignored):
# its own HERMES_HANDS_HOME, so sessions/config never touch your real
# ~/hermes-hands/. The machine-bound key is copied over from your real home on
# first run (it is bound to machine-id + uid, not its path, so the copy still
# decrypts); `make dev-setup` instead prompts for a separate key. The prompt is
# read LIVE from share/instructions.md — edit it, `make dev` again, no rebuild.
DEV_HOME  ?= $(CURDIR)/.dev
REAL_HOME ?= $(or $(HERMES_HANDS_HOME),$(HOME)/hermes-hands)
ARGS      ?=

.PHONY: help dev dev-setup test lint staticcheck build dev-install uninstall release clean

help:
	@echo "Develop:"
	@echo "  make dev [ARGS=…]   run from source out of ./.dev/  (live share/instructions.md)"
	@echo "  make dev-setup      prompt for a separate key for ./.dev/  (else it reuses ~/hermes-hands/)"
	@echo "  make test           go test ./..."
	@echo "  make lint           gofmt check + go vet   (STATICCHECK=1 also runs staticcheck)"
	@echo
	@echo "Build & install:"
	@echo "  make build          dist/hermes-hands for this host  (VERSION + git sha baked in)"
	@echo "  make dev-install    build, then copy it into $(BINDIR)"
	@echo "  make uninstall      remove $(BINDIR)/hermes-hands"
	@echo
	@echo "Release:"
	@echo "  make release        cross-compile $(words $(GOOSARCH)) targets into dist/"
	@echo
	@echo "  make clean          remove dist/ and ./.dev/"

# --- develop ---------------------------------------------------------------

dev:
	@mkdir -p $(DEV_HOME)
	@if [ ! -e "$(DEV_HOME)/secrets.enc" ] && [ -f "$(REAL_HOME)/secrets.enc" ] && [ -f "$(REAL_HOME)/keyseed" ]; then \
	  install -m 0600 "$(REAL_HOME)/secrets.enc" "$(DEV_HOME)/secrets.enc"; \
	  install -m 0600 "$(REAL_HOME)/keyseed"     "$(DEV_HOME)/keyseed"; \
	  echo "make dev: reused the key from $(REAL_HOME)/  (make clean to refresh)"; \
	fi
	HERMES_HANDS_HOME=$(DEV_HOME) \
	HERMES_HANDS_INSTRUCTIONS=$(CURDIR)/share/instructions.md \
	go run -ldflags '$(LDFLAGS)' . $(ARGS)

dev-setup:
	@mkdir -p $(DEV_HOME)
	HERMES_HANDS_HOME=$(DEV_HOME) go run -ldflags '$(LDFLAGS)' . setup

test:
	CGO_ENABLED=0 go test ./...

lint:
	@bad=$$(gofmt -l $$(find . -name '*.go' -not -path './vendor/*')); \
	 test -z "$$bad" || { echo "gofmt needs a run on:"; echo "$$bad"; exit 1; }
	go vet ./...
ifdef STATICCHECK
	go run honnef.co/go/tools/cmd/staticcheck@v0.5.1 ./...
endif

# --- build & install -----------------------------------------------------

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/hermes-hands .

dev-install: build
	@mkdir -p $(BINDIR)
	install -m 0755 dist/hermes-hands $(BINDIR)/hermes-hands
	@echo "installed $(BINDIR)/hermes-hands"
	@echo "next: hermes-hands setup"

uninstall:
	rm -f $(BINDIR)/hermes-hands
	@echo "removed $(BINDIR)/hermes-hands"

# --- release -----------------------------------------------------------------

release:
	@set -e; for t in $(GOOSARCH); do \
	  os=$${t%/*}; arch=$${t#*/}; ext=; [ "$$os" = windows ] && ext=.exe; \
	  echo "building dist/hermes-hands_$${os}_$${arch}$$ext"; \
	  GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' \
	    -o dist/hermes-hands_$${os}_$${arch}$$ext . ; \
	done

clean:
	rm -rf dist $(DEV_HOME)
