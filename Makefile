# JukaHub developer entry point.
#
# One place for the commands the setup, check and release docs refer to, so a
# fresh clone is one command from a running app and the builder tests are not
# forgotten. The underlying commands are the ones CI runs; this file only names
# them.
#
#   make            list the targets
#   make dev        build and launch the player
#   make check      everything a pull request must pass locally
#
# Windows: the toolchain (Go, GCC, SDL2) is bootstrapped by player\build.bat, so
# the player targets delegate to it. Linux/macOS: install the SDL2 dev packages
# first, see docs/BUILDING.md.
#
# GNU make and a POSIX-ish shell are required (Git Bash on Windows).

.DEFAULT_GOAL := help

GO     ?= go
NODE   ?= node
PLAYER := player

ifeq ($(OS),Windows_NT)
  DEV    := cmd.exe /c .\\$(PLAYER)\\build.bat
  BUILD  := cmd.exe /c .\\$(PLAYER)\\build.bat nolaunch
  CLEAN  := cmd.exe /c .\\$(PLAYER)\\build.bat clean
  RUNBIN := ./$(PLAYER)/JukaHub.exe

  # player\build.bat installs SDL2 into this local tree and only points its own
  # shell at it, so a bare `go test` cannot find the headers. Point cgo (and the
  # DLL search for test binaries) at the same tree so `make test` and `make vet`
  # work from any shell. Delete player\.sdl2 and run `make build` to recreate it.
  SDL_ROOT := $(abspath $(PLAYER)/.sdl2/x86_64-w64-mingw32)
  ifneq ($(wildcard $(SDL_ROOT)/include/SDL2/SDL.h),)
    export CGO_CFLAGS  := -O2 -g -I$(SDL_ROOT)/include
    export CGO_LDFLAGS := -L$(SDL_ROOT)/lib
    export PATH        := $(SDL_ROOT)/bin:$(PATH)
  endif
else
  DEV    := cd $(PLAYER) && $(GO) run .
  BUILD  := cd $(PLAYER) && $(GO) build -o JukaHub .
  CLEAN  := rm -f $(PLAYER)/JukaHub
  RUNBIN := ./$(PLAYER)/JukaHub
endif

# The vendored SDL bindings are generated upstream and must not be reformatted,
# so the file list comes from find with vendor pruned. find|xargs also keeps the
# command line short enough for Windows, where one gofmt call with every path
# overflows it.
GO_SOURCES := find $(PLAYER) -path '*/vendor' -prune -o -name '*.go' -print

# Builder scripts the browser smoke test loads; `node --check` catches syntax
# errors without starting anything.
WEB_SCRIPTS := script.js builder-device.js builder-gallery.js gallery/layouts.js

.PHONY: help dev build run test test-web e2e vet fmt fmt-check lint bench web \
        release clean check coverage

help: ## List the targets
	@awk 'BEGIN {FS = ":.*## "}; /^[a-zA-Z0-9_-]+:.*## / {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

dev: ## Build and launch the player
	$(DEV)

build: ## Build the player without launching it
	$(BUILD)

run: ## Launch the last build (no rebuild)
	$(RUNBIN)

test: ## Run the player unit and render tests
	cd $(PLAYER) && $(GO) test ./... -count=1

test-web: ## Run the browser-free builder tests (jsdom)
	@command -v $(NODE) >/dev/null 2>&1 || { echo "node is required for the web tests: https://nodejs.org"; exit 1; }
	cd tests/web && npm ci --no-audit --no-fund && npm test

e2e: ## Run the builder Chromium smoke test (needs npx playwright install chromium)
	@command -v $(NODE) >/dev/null 2>&1 || { echo "node is required for the browser test: https://nodejs.org"; exit 1; }
	cd tests/e2e && npm ci --no-audit --no-fund && npx playwright test

vet: ## go vet the player
	cd $(PLAYER) && $(GO) vet ./...

fmt: ## gofmt the player (vendored packages are skipped)
	@$(GO_SOURCES) | xargs gofmt -w

fmt-check: ## Fail if any Go file is not gofmt'd
	@out="$$($(GO_SOURCES) | xargs gofmt -l)"; \
	if [ -n "$$out" ]; then echo "not gofmt'd (run 'make fmt'):"; echo "$$out"; exit 1; fi

lint: fmt-check vet ## Formatting, vet, builder syntax, ESLint and config JSON
	@command -v $(NODE) >/dev/null 2>&1 || { echo "node is required for the builder syntax check: https://nodejs.org"; exit 1; }
	@for f in $(WEB_SCRIPTS); do $(NODE) --check "$$f" || exit 1; done
	@$(NODE) -e "JSON.parse(require('fs').readFileSync('$(PLAYER)/jukaconfig.json','utf8'))"
	@if [ -x tests/web/node_modules/.bin/eslint ]; then \
		tests/web/node_modules/.bin/eslint -c tests/web/eslint.config.mjs $(WEB_SCRIPTS); \
	else \
		echo "lint: ESLint skipped (run 'make test-web' to install the builder test dependencies)"; \
	fi
	@echo "lint: ok"

coverage: ## Go coverage total plus the worst-covered functions (advisory)
	@cd $(PLAYER) && $(GO) test ./... -count=1 -covermode=atomic -coverprofile="$${TMPDIR:-/tmp}/jukahub-cover.out" >/dev/null 2>&1; \
	status=$$?; \
	if [ $$status -ne 0 ]; then echo "coverage: the test run failed (exit $$status)"; exit $$status; fi; \
	$(GO) tool cover -func="$${TMPDIR:-/tmp}/jukahub-cover.out" > "$${TMPDIR:-/tmp}/jukahub-cover-func.txt"; \
	grep -E '^total:' "$${TMPDIR:-/tmp}/jukahub-cover-func.txt"; \
	echo ""; \
	echo "Functions with 0% coverage (first 25):"; \
	awk '$$NF == "0.0%" && $$1 != "total:" {print}' "$${TMPDIR:-/tmp}/jukahub-cover-func.txt" | head -25; \
	rm -f "$${TMPDIR:-/tmp}/jukahub-cover.out" "$${TMPDIR:-/tmp}/jukahub-cover-func.txt"

bench: ## Cold-boot benchmark (see player/startup-bench.sh)
	cd $(PLAYER) && bash ./startup-bench.sh

web: ## Serve the builder at http://127.0.0.1:8123
	@command -v $(NODE) >/dev/null 2>&1 || { echo "node is required for the builder: https://nodejs.org"; exit 1; }
	$(NODE) tests/e2e/serve.mjs .

release: ## Package the release artifacts into player/dist
	cd $(PLAYER) && bash ./release.sh

clean: ## Remove build outputs (Windows also removes the local SDL copy)
	$(CLEAN)

check: lint test test-web ## Everything a pull request must pass (no browser needed); ESLint is skipped by lint until test-web installs it
	@echo "check: ok"
