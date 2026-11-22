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
PYTHON ?= python3
# Extra flags for `make test`. Empty by default so Go's test cache answers a
# re-run of unchanged code instantly; `make test GOTESTFLAGS=-count=1` forces a
# fresh run (CI does its own uncached run).
GOTESTFLAGS ?=
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
WEB_SCRIPTS := builder-icons.js script.js builder-device.js builder-gallery.js gallery/layouts.js

.PHONY: help dev build helpdocs run test test-web e2e vet fmt fmt-check lint bench web \
        release os-package jukaboot clean check coverage workflows docs-links backlog-check deadcode-scan test-scripts test-spatialfx \
        vendor-sync

help: ## List the targets
	@awk 'BEGIN {FS = ":.*## "}; /^[a-zA-Z0-9_-]+:.*## / {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

dev: helpdocs ## Build and launch the player
	$(DEV)

build: helpdocs ## Build the player without launching it
	$(BUILD)

# Settings -> About -> Controls and Quick start (player/helpdoc.go) embed copies
# of docs/CONTROLS.md and docs/GETTING_STARTED.md from player/helpdocs/, because
# go:embed cannot reach ../docs. Refresh them (LF) before a build; the committed
# copies are what a plain `go build` embeds.
helpdocs:
	@mkdir -p $(PLAYER)/helpdocs
	@for f in GETTING_STARTED.md CONTROLS.md; do \
		tr -d '\r' < docs/$$f > $(PLAYER)/helpdocs/$$f.tmp && mv -f $(PLAYER)/helpdocs/$$f.tmp $(PLAYER)/helpdocs/$$f || exit 1; \
	done

run: ## Launch the last build (no rebuild)
	$(RUNBIN)

# player/vendor/ is a git-ignored local copy of the modules (update-deps.bat or
# `go mod vendor` makes it). While it exists every go command builds from it, so
# a pulled go.mod change (a merged Dependabot PR) stops them with "inconsistent
# vendoring". `go list -m` runs that check in a moment: refresh the copy when it
# fails. player\build.bat does the same for Windows builds.
vendor-sync:
	@cd $(PLAYER) && if [ -f vendor/modules.txt ] && ! $(GO) list -m >/dev/null 2>&1; then \
		echo "vendor-sync: player/vendor no longer matches go.mod, running go mod vendor"; \
		$(GO) mod vendor; \
	fi

test: vendor-sync ## Run the player's lean unit-test core (no render tests)
	cd $(PLAYER) && $(GO) test ./... $(GOTESTFLAGS)

test-web: ## Run the browser-free builder tests (jsdom)
	@command -v $(NODE) >/dev/null 2>&1 || { echo "node is required for the web tests: https://nodejs.org"; exit 1; }
	cd tests/web && npm ci --no-audit --no-fund && npm test

e2e: ## Run the builder Chromium smoke test (installs the pinned Chromium on the first run)
	@command -v $(NODE) >/dev/null 2>&1 || { echo "node is required for the browser test: https://nodejs.org"; exit 1; }
	cd tests/e2e && npm ci --no-audit --no-fund
	@cd tests/e2e && npx playwright install chromium
	cd tests/e2e && npx playwright test

vet: vendor-sync ## go vet the player
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

coverage: vendor-sync ## Go coverage total plus the worst-covered functions (advisory)
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

# APP is a built TrimUI app: the JukaHub-Trimui folder, JukaHub-Trimui.zip from a
# release, or player/dist/JukaHub-SDCARD/Apps/JukaHub.
APP ?= JukaHub-Trimui.zip
os-package: ## JukaHub OS SD-card zip from a TrimUI build: make os-package APP=JukaHub-Trimui.zip
	bash $(PLAYER)/trimui/make-os-package.sh "$(APP)" JukaHub-OS-Trimui.zip

# jukaboot is pure Go (no cgo, no SDL), so this works from any OS, Windows too.
jukaboot: vendor-sync ## JukaHub OS's process 1 for the Smart Pro image: player/jukaboot (static linux/arm64)
	cd $(PLAYER) && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -buildmode=exe -ldflags="-s -w" -o jukaboot ./cmd/jukaboot

clean: ## Remove build outputs (Windows also removes the local SDL copy)
	$(CLEAN)

workflows: ## Check the workflow pins: actions, images and downloaded archives
	@bash .github/scripts/check-workflows.sh

docs-links: ## Check that every relative Markdown link resolves (tests/scripts/docs-links.py, no Go toolchain needed)
	@$(PYTHON) tests/scripts/docs-links.py .

backlog-check: ## Check the open task count, IDs and local anchors
	@$(PYTHON) tests/scripts/backlog-check.py

deadcode-scan: ## Catch test-only player functions with no documented purpose
	@$(PYTHON) tests/scripts/deadcode-scan.py $(PLAYER)

test-scripts: ## Check the CI helper scripts (tampered archives refused, release-cache provenance, TrimUI build); CI runs it weekly
	@bash tests/scripts/install-sdl-mingw.test.sh
	@bash tests/scripts/go-build-cache.test.sh
	@bash tests/scripts/trimui-build.test.sh

# Linux only: the test builds an ELF library and loads it with dlopen; elsewhere
# it says it skipped. CI runs it weekly (weekly.yml).
test-spatialfx: ## Check the game-audio plugin against its DSP spec (Linux, gcc)
	@if [ "$$(uname -s 2>/dev/null)" = Linux ]; then sh $(PLAYER)/spatialfx/tests/run-tests.sh >/dev/null && echo "test-spatialfx: ok"; else echo "test-spatialfx: skipped (Linux only)"; fi

check: lint test test-web workflows docs-links backlog-check deadcode-scan ## Run local lint and tests (no browser needed); ESLint is skipped by lint until test-web installs it
	@echo "check: ok"
