# Build / test / lint tasks for working ON hermes-hands. End users install via
# install.sh (see README); this Makefile is for maintainers.
#
# The toolchain is pinned IN the repo: every go / gofmt / vet / test / build
# runs inside $(GO_IMAGE) via `docker run` — the host needs nothing installed
# but Docker: no system Go, no PATH hunting, no version drift; the checkout
# itself says which Go it needs (keep GO_IMAGE's version equal to go.mod's
# `go` line). `GO_MODE=local` falls back to a host-installed go — that is what
# the CI workflows set (their runners pin go.mod's version via setup-go).
#
# CI never calls `make` directly — it calls ./build.sh. But `build.sh release`
# runs `make clean dist` internally, so the `dist` target IS on the release
# path: the published binaries are cross-compiled by it. What is NOT here is
# the GitHub-specific release logic (compute the next version, tag it, generate
# notes, publish) — that lives entirely in .github/workflows/release.yml.
#
# There is no VERSION file — git tags are the source of truth. `git describe`
# (restricted to real vX.Y.Z tags) gives `0.10.0` on a release,
# `0.10.0-3-gabc123` in between; a checkout with no such tag yet falls back to
# `0.0.0-dev`. release.yml overrides via the environment with the tag it cuts:
#   make VERSION=0.10.0 dist
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
# decrypts — the container mounts the host's /etc/machine-id read-only and
# runs as your uid/gid, so the binding stays identical); `make dev-setup`
# instead prompts for a separate key. The prompt is read LIVE from
# share/instructions.md — edit it, `make dev` again, no rebuild.
DEV_HOME  ?= $(CURDIR)/.dev
REAL_HOME ?= $(or $(HERMES_HANDS_HOME),$(HOME)/hermes-hands)
ARGS      ?=

# ---- toolchain: pinned in the repo, executed in a container ----------------
GO_MODE  ?= docker
# GO_IMAGE version MUST equal go.mod's `go` line
GO_IMAGE ?= golang:1.26.7-alpine
# persistent build+module cache, host-owned, wiped by `make clean`
GO_CACHE := $(CURDIR)/.dev/gocache

UID      := $(shell id -u)
GID      := $(shell id -g)
TTY      := $(shell [ -t 0 ] && echo -t)  # -t only with a real terminal (the make dev REPL)
MID      := $(if $(wildcard /etc/machine-id),-v /etc/machine-id:/etc/machine-id:ro,)

# The pinned toolchain, run in the checkout, as your uid/gid: every file go
# writes (dist/, .dev/) stays owned by you. Caches persist under ./.dev/gocache.
docker-run = docker run --rm -i $(TTY) $(MID) -v $(CURDIR):/src -w /src -v $(GO_CACHE):/cache -e HOME=/tmp -e GOPATH=/cache/gopath -e GOCACHE=/cache/build -e GOMODCACHE=/cache/mod --user $(UID):$(GID)
# the same offline flags the CI gate uses: vendored deps, CGO off
go-flags   = -e CGO_ENABLED=0 -e GOFLAGS=-mod=vendor

.PHONY: help toolchain dev dev-setup dev-install uninstall test lint ci build dist clean
.DEFAULT_GOAL := help

help:  ## this list
	@awk 'BEGIN{FS = ":.*## "} \
	     /^##@/               {printf "\n%s\n", substr($$0, 5); next} \
	     /^[a-zA-Z_-]+:.*## /  {printf "  %-13s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

##@ Develop  (local only — writes ./.dev/ or your PATH)

toolchain:  ## docker pull the pinned Go image + print its version (once, first use)
	docker pull $(GO_IMAGE)
	docker run --rm $(GO_IMAGE) go version

dev: | $(GO_CACHE)  ## run from source out of ./.dev/ — live share/instructions.md; ARGS= forwards CLI flags
	@mkdir -p $(DEV_HOME)
	@if [ ! -e "$(DEV_HOME)/secrets.enc" ] && [ -f "$(REAL_HOME)/secrets.enc" ] && [ -f "$(REAL_HOME)/keyseed" ]; then \
	  install -m 0600 "$(REAL_HOME)/secrets.enc" "$(DEV_HOME)/secrets.enc"; \
	  install -m 0600 "$(REAL_HOME)/keyseed"     "$(DEV_HOME)/keyseed"; \
	  echo "make dev: reused the key from $(REAL_HOME)/  (make clean to refresh)"; \
	fi
ifeq ($(GO_MODE),docker)
	$(docker-run) $(go-flags) -e HERMES_HANDS_HOME=/src/.dev -e HERMES_HANDS_INSTRUCTIONS=/src/share/instructions.md $(GO_IMAGE) go run -ldflags '$(LDFLAGS)' . $(ARGS)
else
	HERMES_HANDS_HOME=$(DEV_HOME) \
	HERMES_HANDS_INSTRUCTIONS=$(CURDIR)/share/instructions.md \
	go run -ldflags '$(LDFLAGS)' . $(ARGS)
endif

dev-setup: build  ## prompt for a separate key for ./.dev/  (else `make dev` reuses ~/hermes-hands/)
	@mkdir -p $(DEV_HOME)
ifeq ($(GO_MODE),docker)
	$(docker-run) $(go-flags) -e HERMES_HANDS_HOME=/src/.dev $(GO_IMAGE) go run -ldflags '$(LDFLAGS)' . setup
else
	HERMES_HANDS_HOME=$(DEV_HOME) go run -ldflags '$(LDFLAGS)' . setup
endif

dev-install: build  ## build, then copy the binary onto your PATH  (PREFIX=/BINDIR= override)
	@mkdir -p $(BINDIR)
	install -m 0755 dist/hermes-hands $(BINDIR)/hermes-hands
	@echo "installed $(BINDIR)/hermes-hands"
	@echo "next: hermes-hands setup"

uninstall:  ## remove the installed binary from your PATH
	rm -f $(BINDIR)/hermes-hands
	@echo "removed $(BINDIR)/hermes-hands"

##@ Verify  (the same checks the PR gate runs)

test: | $(GO_CACHE)  ## go test ./... — offline (vendored), no network, no model
ifeq ($(GO_MODE),docker)
	$(docker-run) $(go-flags) $(GO_IMAGE) go test ./...
else
	CGO_ENABLED=0 go test ./...
endif

lint: | $(GO_CACHE)  ## gofmt check + go vet   (STATICCHECK=1 also runs staticcheck — needs network)
ifeq ($(GO_MODE),docker)
	@bad=$$($(docker-run) $(go-flags) $(GO_IMAGE) sh -c 'find . -name "*.go" -not -path "./vendor/*" -not -path "./.dev/*" -exec gofmt -l {} +'); test -z "$$bad" || { echo "gofmt needs a run on:"; echo "$$bad"; exit 1; }
	$(docker-run) $(go-flags) $(GO_IMAGE) go vet ./...
ifdef STATICCHECK
	$(docker-run) -e CGO_ENABLED=0 -e GOFLAGS=-mod=mod $(GO_IMAGE) go run honnef.co/go/tools/cmd/staticcheck@v0.5.1 ./...
endif
else
	@bad=$$(gofmt -l $$(find . -name '*.go' -not -path './vendor/*' -not -path './.dev/*')); \
	 test -z "$$bad" || { echo "gofmt needs a run on:"; echo "$$bad"; exit 1; }
	go vet ./...
ifdef STATICCHECK
	go run honnef.co/go/tools/cmd/staticcheck@v0.5.1 ./...
endif
endif

ci: lint test dist  ## the PR gate, locally: lint + test + release cross-build + a windows compile
ifeq ($(GO_MODE),docker)
	$(docker-run) $(go-flags) -e GOOS=windows -e GOARCH=amd64 $(GO_IMAGE) go build ./...
else
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...
endif

##@ Artifacts

build: | $(GO_CACHE)  ## dist/hermes-hands for this host  (VERSION + git sha baked in)
	@mkdir -p dist
ifeq ($(GO_MODE),docker)
	$(docker-run) $(go-flags) $(GO_IMAGE) go build -trimpath -ldflags '$(LDFLAGS)' -o dist/hermes-hands .
else
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/hermes-hands .
endif

dist: | $(GO_CACHE)  ## cross-compile every target into dist/  (build.sh + release.yml call this; does NOT publish)
	@mkdir -p dist
	@set -e; for t in $(GOOSARCH); do \
	  os=$${t%/*}; arch=$${t#*/}; ext=; [ "$$os" = windows ] && ext=.exe; \
	  echo "building dist/hermes-hands_$${os}_$${arch}$$ext"; \
	  $(docker-run) $(go-flags) -e GOOS=$$os -e GOARCH=$$arch $(GO_IMAGE) go build -trimpath -ldflags '$(LDFLAGS)' -o dist/hermes-hands_$${os}_$${arch}$$ext . ; \
	done

$(GO_CACHE):
	@mkdir -p $(GO_CACHE)

clean:  ## remove dist/ and ./.dev/ (incl. the toolchain caches)
	rm -rf dist $(DEV_HOME)
