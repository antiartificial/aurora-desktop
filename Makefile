# Aurora. `make` lists the targets; `make desktop` opens the desktop in a window.
#
# Everything here is a thin wrapper over ./aurora-up and the compositor binary:
# nothing is built or started that those two do not already do, so a target
# failing here means the underlying command failed, not the Makefile.

SHELL := /bin/bash
RUN   ?= /tmp/aurora-up-$(shell id -u)
COMP  := compositor/target/release/aurora-compositor
SIZE  ?= 1600x1000

.DEFAULT_GOAL := help
.PHONY: help build desktop up env stop status test demos clean

help:
	@printf '\nAurora\n\n'
	@grep -hE '^[a-z][a-z-]*:.*## ' $(MAKEFILE_LIST) \
	  | sed -E 's/^([a-z-]+):.*## /\1|/' \
	  | awk -F'|' '{printf "  make %-9s %s\n", $$1, $$2}'
	@printf '\n  run dir   %s   (override: make desktop RUN=...)\n' '$(RUN)'
	@printf '  headless  %s        (make up SIZE=1920x1200; a host window takes its size from the host)\n\n' '$(SIZE)'

build: ## build everything: the Go modules, the compositor, the Haiku kits
	./aurora-up --build-only

desktop: build ## open the desktop in a window on this host, with the stack behind it
	@set -u; \
	if [ -f '$(RUN)/ready' ] && [ -S '$(RUN)/bus.sock' ]; then \
	  echo "make: reusing the stack already up in $(RUN)"; up=; \
	else \
	  echo "make: starting the stack in $(RUN) (log: $(RUN).up.log)"; \
	  ./aurora-up --no-compositor --no-build --run '$(RUN)' > '$(RUN).up.log' 2>&1 & \
	  up=$$!; \
	  for _ in $$(seq 600); do \
	    [ -f '$(RUN)/ready' ] && break; \
	    kill -0 $$up 2>/dev/null || { echo "make: the stack died; see $(RUN).up.log" >&2; tail -20 '$(RUN).up.log' >&2; exit 1; }; \
	    sleep 0.1; \
	  done; \
	  [ -f '$(RUN)/ready' ] || { echo "make: the stack did not come up in 60s; see $(RUN).up.log" >&2; exit 1; }; \
	fi; \
	trap '[ -n "$$up" ] && kill $$up 2>/dev/null' EXIT; \
	[ -d '$(RUN)/theme' ] || cp -r contracts/theme '$(RUN)/theme'; \
	echo "make: the desktop is a host window. Right-click it for the menu; close it to stop."; \
	XDG_RUNTIME_DIR='$(RUN)/xdg' ./$(COMP) \
	  --bus-socket '$(RUN)/bus.sock' \
	  --trusted-socket '$(RUN)/xdg/aurora-trusted.sock' \
	  --theme '$(RUN)/theme'

up: ## run the stack in the foreground, headless, with no host window (Ctrl-C stops it)
	./aurora-up --run '$(RUN)' --size '$(SIZE)'

env: ## print the `export` lines a second terminal needs to drive a running stack
	@./aurora-up --env --run '$(RUN)'

status: ## pid, uptime, restarts and health of each service in a running stack
	@source '$(RUN)/env' && auroractl status

stop: ## stop a stack started by `make desktop` and remove its domains and sockets
	@set -u; \
	pid=$$(ps -o pid=,ppid=,args= -u "$$(id -u)" \
	  | awk -v me=$$$$ '$$1 != me && $$2 != me && /aurora-up/ && !/--env/ { print $$1; exit }'); \
	if [ -n "$$pid" ]; then kill "$$pid" && echo "make: stopped aurora-up (pid $$pid); it removes its own domains and sockets"; \
	else echo "make: no aurora-up running"; fi

test: ## the compositor suite and the Go module tests
	cd compositor && cargo test --release
	go test ./...

demos: ## every demos/*/run.sh; each starts a real stack and exits non-zero on failure
	@set -e; for d in demos/*/run.sh; do echo "== $$d"; "$$d"; done

clean: ## remove build output (build/bin, cargo target dirs, kits/build)
	rm -rf build/bin kits/build
	cd compositor && cargo clean
	cd harness/capture && cargo clean
