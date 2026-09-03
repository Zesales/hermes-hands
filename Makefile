# Dev helpers for the Go build. End users install via install.sh (see README).
VERSION  := $(shell tr -d '[:space:]' < VERSION)
GIT_SHA  := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS  := -s -w -X main.version=$(VERSION) -X main.commit=$(GIT_SHA)
PREFIX   ?= $(HOME)/.local
BINDIR   ?= $(PREFIX)/bin
GOOSARCH := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

.PHONY: help build test lint staticcheck dev dev-install uninstall release clean

# `make dev` runs straight from source (go run) against a checkout-local dev
# state: sessions go under ./.dev/ (not your real ~/hermes-hands/sessions), and
# the prompt is read LIVE from share/instructions.md so you can edit it and
# re-run with no rebuild. The gateway URL + key are inherited from your real
# ~/hermes-hands/ (or the env) — no separate setup. Pass CLI args with ARGS=…
#   make dev                 # continue this repo's dev session
#   make dev ARGS=--new
#   make dev ARGS=setup      # first run only, if you want an isolated key too:
#                            #   HERMES_HANDS_HOME=$(CURDIR)/.dev make dev ARGS=setup
DEV_HOME ?= $(CURDIR)/.dev
ARGS     ?=

help:
	@echo "make build        - build dist/hermes-hands for this host (VERSION + git sha baked in)"
	@echo "make dev [ARGS=…]  - go run from source: live share/instructions.md, sessions under ./.dev/"
	@echo "make test         - go test ./..."
	@echo "make lint         - gofmt check + go vet ./...  (STATICCHECK=1 also runs staticcheck)"
	@echo "make dev-install  - build, then copy dist/hermes-hands into $(BINDIR)"
	@echo "make uninstall    - remove $(BINDIR)/hermes-hands"
	@echo "make release      - cross-compile $(words $(GOOSARCH)) targets into dist/"
	@echo "make clean        - remove dist/ and ./.dev/"

dev:
	@mkdir -p $(DEV_HOME)
	HERMES_HANDS_STATE=$(DEV_HOME) \
	HERMES_HANDS_INSTRUCTIONS=$(CURDIR)/share/instructions.md \
	go run -ldflags '$(LDFLAGS)' . $(ARGS)

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/hermes-hands .

test:
	CGO_ENABLED=0 go test ./...

lint:
	@bad=$$(gofmt -l $$(find . -name '*.go' -not -path './vendor/*')); \
	 test -z "$$bad" || { echo "gofmt needs a run on:"; echo "$$bad"; exit 1; }
	go vet ./...
ifdef STATICCHECK
	go run honnef.co/go/tools/cmd/staticcheck@v0.5.1 ./...
endif

dev-install: build
	@mkdir -p $(BINDIR)
	install -m 0755 dist/hermes-hands $(BINDIR)/hermes-hands
	@echo "installed $(BINDIR)/hermes-hands"
	@echo "next: hermes-hands setup"

uninstall:
	rm -f $(BINDIR)/hermes-hands
	@echo "removed $(BINDIR)/hermes-hands"

release:
	@set -e; for t in $(GOOSARCH); do \
	  os=$${t%/*}; arch=$${t#*/}; ext=; [ "$$os" = windows ] && ext=.exe; \
	  echo "building dist/hermes-hands_$${os}_$${arch}$$ext"; \
	  GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' \
	    -o dist/hermes-hands_$${os}_$${arch}$$ext . ; \
	done

clean:
	rm -rf dist $(DEV_HOME)
