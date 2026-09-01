PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin

.PHONY: help install uninstall test lint

help:
	@echo "make install    - symlink bin/hermes-hands into $(BINDIR)"
	@echo "make uninstall  - remove that symlink"
	@echo "make test       - offline test suite (mock Runs API)"
	@echo "make lint       - shellcheck"

install:
	@mkdir -p $(BINDIR)
	@ln -sf $(CURDIR)/bin/hermes-hands $(BINDIR)/hermes-hands
	@echo "linked $(BINDIR)/hermes-hands -> $(CURDIR)/bin/hermes-hands"
	@echo "next: hermes-hands setup"

uninstall:
	@rm -f $(BINDIR)/hermes-hands
	@echo "removed $(BINDIR)/hermes-hands"

test:
	@./test/run_tests.sh

lint:
	@shellcheck -x bin/hermes-hands lib/*.sh test/run_tests.sh install.sh
