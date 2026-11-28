# JukaHub engineering backlog

Open tasks: **7**.

Open work an agent can finish and check without a person at the device —
builds, tests, static checks, scripted probes, screenshots it can read — plus
an ideas list. Finished work goes to the [changelog](../CHANGELOG.md); checks
on a handheld or a PC go to the
[hardware validation log](../docs/HARDWARE_VALIDATION.md).

Each task is `### ID — title (priority, size) [tags]` with **Where.** (the
files), **Done when.** (acceptance) and, where it is not obvious, **Verify.**
P0 blocks a release, P1 is current user pain, P2 planned value, P3 polish; S
is at most half a day, M two days, L a week (split anything bigger). [WIN]
needs a Windows build, [NET] the network, [OWNER] something only the owner has.

When a task is done: delete it, add the change under `[Unreleased]` in the
changelog, and put any device check in the hardware log. Every change keeps
`make check` and both release builds green. Ground rules: the 1 GiB Smart Pro
stays responsive (nothing blocking or allocating per frame on the render
thread); use the shared design and input-hint helpers; log with
`logDebug`/`logInfo`/`logWarn`/`logError`, never `log`; tests only for data
integrity, security, installers and outside parsers. See
[development](../docs/DEVELOPMENT.md) and [contributing](../CONTRIBUTING.md).

---

## Mail and calendar (MAIL)

---

## Performance and graphics (PERF, GPU)

### PERF-7 — Trim heavy dependencies (P2, L) [WIN]
**Where.** goja (the Browser's DOM pass: `webdom*.go`, `webscript.go`),
gopsutil (`processes.go`, `systool_*.go`, `terminal.go`, `textbrowser.go`,
`plugnative.go`), zeroconf (`textbrowser.go`, `moonlight_discovery.go`,
`nas_ui.go`).
**Done when.** goja and the DOM pass sit behind a `//go:build !nojs` split (the
lite build shows pages as they arrived and hides Settings → Browser →
JavaScript); gopsutil gives way to `/proc` + `statfs` and two Win32 calls, and
zeroconf to a one-shot mDNS query that still finds Moonlight hosts and SMB
shares; features unchanged.
**Verify.** Size per package (`go tool nm -size -sort size`, the top ten) and
cold-cache build time before and after, in a new `docs/PERFORMANCE.md`.

### ARCH-1 — Package split, step 1: pure leaves (P1, M) [WIN]
**Where.** `vecraster.go`, `icondefs.go`, `retry.go`, `browserpsl.go`,
`ramsearch*.go`, `ppsspp_calibration.go`, `port_compat.go`, `lyrics.go`,
`textutil.go`, `scenetasks.go` (they reference no other file of the package).
**Done when.** Each lives in `player/internal/<name>` with exported API; `main`
keeps thin aliases (`var retryDo = retry.Do`) so call sites stay unchanged;
`make check` green after each move.
**Verify.** `go build -x` shows the moved packages cached on an edit to `core.go`.

### ARCH-3 — Package split, step 3: models and parsers (P2, L) [WIN]
**Where.** PDF (`pdfreader.go`, `pdfv_doc.go`), EPUB/HTML reader (`reader.go`),
web page model (`webpage*.go`: split layout from drawing), feeds/podcasts,
M3U playlists, patch engine (`patch_*.go`: journal, staging, signatures),
emulator catalogs (`emusystems.go`, compat tables), media library scanner
(`mlib.go`), JSON stores (atomic write + `.bak` from `config_health.go`).
**Done when.** Each moves with SDL-free code only; one PR per package.

### ARCH-5 — Shrink the main loop (P2, L) [WIN]
**Done when.** The loop body is a short sequence of named phases (input, update,
draw, present, pace) in their own files; `core.go` is split by responsibility
(render helpers, triggers, video entry points) without behaviour change.

### SEC-1 — Every extraction through `os.Root` (P2, M)
**Where.** `emu7z.go`, `ports_store_install.go`, `ppsspp_managed.go`,
`tts_install.go`, `emulatorupdates.go`, `androidpak.go`, `relpack.go`,
`ssyncbundle.go`, `bkup.go` (restore). `extractZipSafe` and `extractTarGzSafe`
(`patch_engine.go`, `openExtractRoot`) and `pkgExtractZip` are the pattern.
**Done when.** Each writes through an `os.Root` opened on its destination;
traversal tests stay green.

### APP-10 — Live bindings between ES modules (P2, M)
**Where.** `webdom_module.go` (references to imported names),
`webdom/85_scripts.js` (`modContext`, the namespaces).
**Done when.** References to an imported name in a module body read the
exporting module's namespace (with scope analysis in the rewrite, so local
variables and property names of the same spelling stay untouched), cycles
evaluate in the standard's order, and a test links a two-module cycle that
reassigns an exported `let`.

### WEB-1 — Split `script.js` into modules (P1, L)
**Done when.** ES modules by concern (canvas, inspector, scenes, export/import,
validation, gallery, AI panel), the CSP still holds (`_headers`), and the jsdom
+ Playwright suites pass unchanged.

## Waiting on Juka (OWNER)

Each needs something only the owner can give — an account, a key, a secret,
a person — before an agent can finish it.

