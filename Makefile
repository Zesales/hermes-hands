PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin
DIST   ?= dist/hermes-hands

.PHONY: help build install uninstall dev-install test lint clean

help:
	@echo "make build        - bundle into a single file ($(DIST))"
	@echo "make install      - build, then copy it to $(BINDIR)/hermes-hands"
	@echo "make dev-install  - symlink the checkout's bin/hermes-hands instead"
	@echo "make uninstall    - remove $(BINDIR)/hermes-hands"
	@echo "make test / lint  - offline test suite / shellcheck"

build:
	@./build.sh $(DIST)

install: build
	@mkdir -p $(BINDIR)
	@install -m 0755 $(DIST) $(BINDIR)/hermes-hands
	@echo "installed $(BINDIR)/hermes-hands ($$($(DIST) --version))"
	@echo "next: hermes-hands setup"

dev-install:
	@mkdir -p $(BINDIR)
	@ln -sf $(CURDIR)/bin/hermes-hands $(BINDIR)/hermes-hands
	@echo "linked $(BINDIR)/hermes-hands -> $(CURDIR)/bin/hermes-hands"

uninstall:
	@rm -f $(BINDIR)/hermes-hands
	@echo "removed $(BINDIR)/hermes-hands"

test:
	@./test/run_tests.sh

lint:
	@shellcheck -x bin/hermes-hands lib/*.sh test/run_tests.sh install.sh build.sh

clean:
	@rm -rf dist
