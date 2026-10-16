# Build / test / lint tasks for working ON hermes-hands. End users install via
# install.sh (see README); this Makefile is for maintainers.
#
# The toolchain is pinned IN the repo: every go / gofmt / vet / test / build
# runs inside $(GO_IMAGE), a throwaway `docker run --rm` container — the host
# needs nothing but Docker: no system Go, no version drift; the checkout itself
# says which Go it needs (keep GO_IMAGE's version equal to go.mod's `go` line).
# Only the BUILD runs in the container: `make dev` / `dev-setup` / `dev-install`
# run the resulting binary on THIS host — hermes-hands is the local hands, its
# shell must be your real bash, PATH, tools and machine-id, not the image's.
# `GO_MODE=local` swaps the container for a host-installed go (same flags).
#
# CI runs the same container: test.yml calls `make lint test` and
# `./build.sh release` (which runs `make clean dist`), release.yml calls
# `./build.sh release` — so GO_IMAGE is the ONE place the Go version is pinned
# for builds (go.mod's `go` line must match it). What is NOT here is the
# GitHub-specific release logic (compute the next version, tag it, generate
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
# Linux (WSL included) + macOS only. On Windows hermes-hands runs inside WSL (a
# Linux binary) — a native windows build fails on purpose (internal/shell/platform.go).
GOOSARCH := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64
# The host the binary must run on — the container is always linux, so `build`
# cross-compiles for the host explicitly (a macOS host gets a darwin binary).
HOST_OS   := $(shell uname -s | tr A-Z a-z)
HOST_ARCH := $(shell uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')

# `make dev` runs a self-contained dev instance out of ./.dev/ (gitignored):
# its own HERMES_HANDS_HOME, so sessions/config never touch your real
# ~/hermes-hands/. The machine-bound key is copied over from your real home on
# first run (it is bound to machine-id + uid, not its path, so the copy still
# decrypts); `make dev-setup` instead prompts for a separate key. The prompt is
# read LIVE from share/instructions.md — edit it, `make dev` again; code changes
# are rebuilt (incrementally, cached) on every `make dev`.
DEV_HOME  ?= $(CURDIR)/.dev
REAL_HOME ?= $(or $(HERMES_HANDS_HOME),$(HOME)/hermes-hands)
ARGS      ?=

# ---- toolchain: pinned in the repo, executed in a throwaway container ------
GO_MODE  ?= docker
# GO_IMAGE version MUST equal go.mod's `go` line. Debian, not alpine: the
# shell / dispatch / loop tests need bash and skip silently without it.
GO_IMAGE ?= golang:1.26.9-bookworm
# persistent build+module cache, host-owned, wiped by `make clean`
GO_CACHE := $(CURDIR)/.dev/gocache

UID      := $(shell id -u)
GID      := $(shell id -g)

# The pinned toolchain, run in the checkout, as your uid/gid: every file go
# writes (dist/, .dev/) stays owned by you. Caches persist under ./.dev/gocache.
docker-run = docker run --rm -v $(CURDIR):/src -w /src -v $(GO_CACHE):/cache -e HOME=/tmp -e GOPATH=/cache/gopath -e GOCACHE=/cache/build -e GOMODCACHE=/cache/mod --user $(UID):$(GID)

# $(call tc,VAR=value ...) prefixes a toolchain command: in the container
# (GO_MODE=docker) or on the host's go (GO_MODE=local). Both get the offline
# flags the CI gate uses — vendored deps, CGO off — plus any extra VAR=value.
ifeq ($(GO_MODE),docker)
tc = $(docker-run) -e CGO_ENABLED=0 -e GOFLAGS=-mod=vendor $(addprefix -e ,$(1)) $(GO_IMAGE)
else
tc = env CGO_ENABLED=0 GOFLAGS=-mod=vendor $(1)
endif

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

dev: build  ## build in the container, run it on this host out of ./.dev/ — live share/instructions.md; ARGS= forwards CLI flags
	@mkdir -p $(DEV_HOME)
	@if [ ! -e "$(DEV_HOME)/secrets.enc" ] && [ -f "$(REAL_HOME)/secrets.enc" ] && [ -f "$(REAL_HOME)/keyseed" ]; then \
	  install -m 0600 "$(REAL_HOME)/secrets.enc" "$(DEV_HOME)/secrets.enc"; \
	  install -m 0600 "$(REAL_HOME)/keyseed"     "$(DEV_HOME)/keyseed"; \
	  echo "make dev: reused the key from $(REAL_HOME)/  (make clean to refresh)"; \
	fi
	HERMES_HANDS_HOME=$(DEV_HOME) \
	HERMES_HANDS_INSTRUCTIONS=$(CURDIR)/share/instructions.md \
	./dist/hermes-hands $(ARGS)

dev-setup: build  ## prompt for a separate key for ./.dev/  (else `make dev` reuses ~/hermes-hands/)
	@mkdir -p $(DEV_HOME)
	HERMES_HANDS_HOME=$(DEV_HOME) ./dist/hermes-hands setup

dev-install: build  ## build, then copy the binary onto your PATH  (PREFIX=/BINDIR= override)
	@mkdir -p $(BINDIR)
	install -m 0755 dist/hermes-hands $(BINDIR)/hermes-hands
	@echo "installed $(BINDIR)/hermes-hands"
	@[ -e "$(REAL_HOME)/secrets.enc" ] || [ -e "$(REAL_HOME)/secrets" ] || echo "next: hermes-hands setup"

uninstall:  ## remove the installed binary from your PATH
	rm -f $(BINDIR)/hermes-hands
	@echo "removed $(BINDIR)/hermes-hands"

##@ Verify  (the same checks the PR gate runs)

test: | $(GO_CACHE)  ## go test ./... — offline (vendored), no network, no model
	$(call tc) go test ./...

golden: | $(GO_CACHE)  ## rewrite testdata/golden/ (REPL + rpc transcripts) after an intended UI change
	$(call tc) go test -run '^TestGolden' . -update

cover: | $(GO_CACHE)  ## coverage per package: the unit tests, then what the golden runs reach (the real REPL + --rpc)
	@echo "unit tests (each package's own):"
	@$(call tc) go test -count=1 -cover ./... | awk '/%/ {p=$$2; sub("hermes-hands/?","",p); if (p=="") p="(main)"; for (i=1; i<=NF; i++) if ($$i ~ /%$$/) c=$$i; printf "  %-22s %7s\n", p, c}'
	@echo "golden runs (the real REPL + --rpc, in a subprocess):"
	@$(call tc) sh -c 'd=$$(mktemp -d) && trap "rm -rf $$d" EXIT && HH_GOLDEN_COVERDIR=$$d go test -count=1 -coverpkg=./... -run "^TestGolden" . >/dev/null && go tool covdata percent -i=$$d' | awk '{p=$$1; sub("hermes-hands/?","",p); if (p=="") p="(main)"; printf "  %-22s %7s\n", p, $$3}'

# govulncheck, pinned: known vulnerabilities in the code paths hermes-hands
# actually calls (stdlib + the vendored deps) — the one check that needs the
# network (the Go module proxy and vuln.go.dev). x/vuln ships no patch
# releases; this one is weeks old and has a successor.
GOVULNCHECK ?= golang.org/x/vuln/cmd/govulncheck@v1.8.0

vuln: | $(GO_CACHE)  ## govulncheck (pinned) — known vulnerabilities in the code paths we call; needs network
	$(call tc,GOFLAGS=) go run $(GOVULNCHECK) ./...

race: | $(GO_CACHE)  ## go test -race ./... — data races between the stream, the watchdog and the turn (CGO on, the image's gcc)
	$(call tc,CGO_ENABLED=1) go test -race ./...

lint: | $(GO_CACHE)  ## gofmt check + go vet for linux AND darwin   (STATICCHECK=1 also runs staticcheck — needs network)
	@bad=$$($(call tc) sh -c 'find . -name "*.go" -not -path "./vendor/*" -not -path "./.dev/*" -exec gofmt -l {} +'); \
	 test -z "$$bad" || { echo "gofmt needs a run on:"; echo "$$bad"; exit 1; }
	$(call tc) go vet ./...
	$(call tc,GOOS=darwin GOARCH=arm64) go vet ./...
ifdef STATICCHECK
	$(call tc,GOFLAGS=-mod=mod) go run honnef.co/go/tools/cmd/staticcheck@v0.5.1 ./...
endif

ci: lint test race dist  ## the PR gate, locally: lint (linux + darwin) + test + race + release cross-build

##@ Artifacts

build: | $(GO_CACHE)  ## dist/hermes-hands for this host  (VERSION + git sha baked in)
	@mkdir -p dist
	$(call tc,GOOS=$(HOST_OS) GOARCH=$(HOST_ARCH)) go build -trimpath -ldflags '$(LDFLAGS)' -o dist/hermes-hands .

dist: | $(GO_CACHE)  ## cross-compile every target into dist/  (build.sh + release.yml call this; does NOT publish)
	@mkdir -p dist
	@set -e; for t in $(GOOSARCH); do \
	  os=$${t%/*}; arch=$${t#*/}; \
	  echo "building dist/hermes-hands_$${os}_$${arch}"; \
	  $(call tc,GOOS=$$os GOARCH=$$arch) go build -trimpath -ldflags '$(LDFLAGS)' -o dist/hermes-hands_$${os}_$${arch} . ; \
	done

$(GO_CACHE):
	@mkdir -p $(GO_CACHE)

clean:  ## remove dist/ and ./.dev/ (incl. the toolchain caches)
	rm -rf dist $(DEV_HOME)
