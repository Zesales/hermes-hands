# Dev helpers for the Go build. End users install via install.sh (see README).
VERSION  := $(shell tr -d '[:space:]' < VERSION)
GIT_SHA  := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS  := -s -w -X main.version=$(VERSION) -X main.commit=$(GIT_SHA)
PREFIX   ?= $(HOME)/.local
BINDIR   ?= $(PREFIX)/bin
GOOSARCH := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

.PHONY: help build test lint staticcheck dev-install uninstall release clean

help:
	@echo "make build        - build dist/hermes-hands for this host (VERSION + git sha baked in)"
	@echo "make test         - go test ./..."
	@echo "make lint         - gofmt check + go vet ./...  (STATICCHECK=1 also runs staticcheck)"
	@echo "make dev-install  - build, then copy dist/hermes-hands into $(BINDIR)"
	@echo "make uninstall    - remove $(BINDIR)/hermes-hands"
	@echo "make release      - cross-compile $(words $(GOOSARCH)) targets into dist/"
	@echo "make clean        - remove dist/"

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
	rm -rf dist
