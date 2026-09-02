# Dev helpers. End users install via install.sh (see README) - not this file.
PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin
DIST   ?= dist/hermes-hands

.PHONY: help dev-install uninstall build test lint clean release-check

help:
	@echo "make dev-install   - symlink bin/hermes-hands into $(BINDIR) (run from the repo)"
	@echo "make uninstall     - remove $(BINDIR)/hermes-hands"
	@echo "make build         - bundle into the single file $(DIST)"
	@echo "make test / lint   - offline test suite / shellcheck"
	@echo "make release-check  - build + smoke the bundle (what CI runs)"

dev-install:
	@mkdir -p $(BINDIR)
	@ln -sf $(CURDIR)/bin/hermes-hands $(BINDIR)/hermes-hands
	@echo "linked $(BINDIR)/hermes-hands -> $(CURDIR)/bin/hermes-hands"
	@echo "next: hermes-hands setup"

uninstall:
	@rm -f $(BINDIR)/hermes-hands
	@echo "removed $(BINDIR)/hermes-hands"

build:
	@./build.sh $(DIST)

test:
	@./test/run_tests.sh

lint:
	@shellcheck -x bin/hermes-hands lib/*.sh test/run_tests.sh install.sh build.sh

release-check: build
	@bash -n $(DIST) && $(DIST) --version && echo "bundle ok"

clean:
	@rm -rf dist
