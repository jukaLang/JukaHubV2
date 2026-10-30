# Roadmap — 30 / 60 / 90 days

Community and engineering plan for JukaHubV2. For the concise view of current
progress and recommended improvements, start at [docs/PROJECT_STATUS.md](docs/PROJECT_STATUS.md).
This file is the detailed repository/process plan; each active plan item has a
**definition of done** so "added X" is measurable, not aspirational. Completed
items remain listed for context.

One lead per item (the maintainer by default). **The main risk is process
outrunning contributors**, so nothing here grows a required check or an
approval gate without a problem it solves — see [Appendix D](#appendix-d-keeping-it-small).

**Scope, so this stays one list instead of two.** This file is about the
*project*: GitHub mechanics, CI, release engineering, contributor experience.
What the **app itself** still needs — renderer, scene layouts, reliability,
device polish — is tracked and re-verified against the code in
[player/MODERNIZATION.md](player/MODERNIZATION.md), and that file wins if the
two ever disagree about a code item. The idea lists
([docs/FEATURE-SLATE.md](docs/FEATURE-SLATE.md),
[docs/FEATURE-BACKLOG.md](docs/FEATURE-BACKLOG.md),
[docs/IMPROVEMENTS.md](docs/IMPROVEMENTS.md)) are inputs, not plans: an idea
becomes work when it is adopted into one of these two lists, with a definition of
done and a status — and it then leaves the idea list, so no idea is tracked in
two places. The idea lists hold only what has not been adopted.

## Days 0–30 — contributor and CI scaffolding

| # | Item | Status | Definition of done |
|---|------|--------|--------------------|
| 1 | GitHub Discussions with Q&A, Ideas, Show and tell, Development categories | ⬜ maintainer click: Settings → General → Discussions | Categories exist, a welcome post is pinned in Q&A linking [SUPPORT.md](SUPPORT.md) and the docs, README links to Discussions |
| 2 | Issue templates: bug + feature + config | ✅ done | `.github/ISSUE_TEMPLATE/` ships `bug_report.yml`, `feature_request.yml`, `config.yml` (blank issues + security/contact links) |
| 3 | PR template | ✅ done | `.github/PULL_REQUEST_TEMPLATE.md` describes what the user sees / how it was verified / CHANGELOG entry |
| 4 | Dependabot (Actions, Go, npm) | ✅ done | `.github/dependabot.yml` covers all three ecosystems, grouped so one PR per ecosystem per week |
| 5 | CodeQL for Go | ✅ done | `.github/workflows/codeql.yml`, weekly + on main, least-privilege permissions |
| 6 | CODEOWNERS | ✅ done | `.github/CODEOWNERS` assigns default review ownership; PRs auto-request review |
| 7 | SECURITY.md with vulnerability reporting | ✅ done | SECURITY.md has a "Reporting a vulnerability" section pointing at GitHub's private security advisories; the issue-template config links it |
| 8 | SUPPORT.md | ✅ done | Root SUPPORT.md names the help channels (docs, Discussions, Discord), has a before-you-file checklist and response-time expectations |
| 9 | Good first issues open | ⬜ two left to paste: [Appendix A](#appendix-a-starter-issues-ready-to-paste) #4 and #5 (its first three are done and kept as the bar for a starter task) | Five `good first issue` issues exist or have shipped, each scoped to one file, with the verification the PR must add |
| 10 | docs/QUICK_START.md — one copy-paste path to build, test, run | ✅ done | `make dev` + `make check` cover it; deeper docs are linked per topic |
| 11 | Label set: `area/*`, `priority/*`, `good first issue`, `help wanted`, `flaky`, `needs reproduction` | ⬜ one command: Appendix C | Labels exist and the templates above default to them |

## Days 30–60 — quality gates

| # | Item | Status | Definition of done |
|---|------|--------|--------------------|
| 12 | Coverage reporting | 🟡 started | CI already publishes a coverage summary (total + 0 % functions) per run. **Finish:** comment the per-PR delta so regressions are visible per change. What the suite itself covers is verified in [player/MODERNIZATION.md](player/MODERNIZATION.md) (Phase 6), not here |
| 13 | Non-blocking coverage threshold, raised gradually | ⬜ | A warning-only threshold (start at current −2 %) produces a warning, never a red ❌; raised only once the trend is stable |
| 14 | ESLint + Prettier for the web builder | 🟡 mostly | `make check` already runs ESLint over builder and tests. **Finish:** adopt Prettier so format churn stops showing up in diffs |
| 15 | Flaky-test quarantine | 🟡 in progress | QUICK_START.md documents the one known-flaky test. **Finish:** a `flaky` label plus quarantine list keeps repeat offenders out of required checks, and the CONTRIBUTING triage note says who unflags what |
| 16 | docs/ARCHITECTURE.md | ✅ done | Exists, linked from QUICK_START + CONTRIBUTING |
| 17 | docs/adr/ directory | ✅ done | `docs/adr/0000-record-architecture-decisions.md` records the process and its trigger; `0001` (the physical → logical keymap) and `0002` (renderer-owned texture caches) cover the two newest decisions; CONTRIBUTING links the directory |
| 18 | Release-note automation | ✅ done | `release.yml` uses `generate_release_notes`. **Later polish:** group notes by kind (features / fixes / install) |

## Days 60–90 — scale and polish

| # | Item | Status | Definition of done |
|---|------|--------|--------------------|
| 19 | Beta / pre-release channel | ⬜ | Pre-release path documented in BUILDING.md; the self-updater offers dev/beta/stable behind a config switch; ≥1 beta shipped |
| 20 | i18n scaffolding | ⬜ | A string-extraction boundary exists (no bare UI literals scattered), the translation workflow is documented, one full locale ships instead of none |
| 21 | Accessibility audit | 🟡 code side done | **Shipped:** text scaling, high-contrast and colour-blind palettes, reduced motion, the physical → logical keymap ([docs/adr/0001](docs/adr/0001-physical-logical-keymap.md)) and [docs/ACCESSIBILITY.md](docs/ACCESSIBILITY.md); focus navigation is covered by tests. **Finish:** the screen-by-screen pass on player + builder with findings filed as `area/a11y` issues, so the gaps become tickets rather than a document |
| 22 | Lightweight RFC process | ⬜ | RFC template (problem, options, DoD) in `docs/rfcs/`; the rule is written down: RFCs only for work touching more than three modules |
| 23 | Contributor ladder | ⬜ | CONTRIBUTOR_LADDER.md defines contributor → reviewer → maintainer with objective gates (merged PRs, review quality); only needed once a second active maintainer exists |

## Days 90+ — open items from the tooling review

Second-pass additions, triaged with the same rule: only what a real problem
solves. Items marked ✅ were shipped in that review; the rest are candidates
with their trigger for starting, not a shopping list.

| # | Item | Status | Definition of done / trigger |
|---|------|--------|------------------------------|
| 24 | Reproducible dev environment (`.devcontainer/` + `mise.toml`) | ✅ done | Opening the repo in VS Code/Codespaces gives a working `make dev` with Go, Node and SDL2 preinstalled; mise users get pinned editions |
| 25 | Commit-time hooks (`.pre-commit-config.yaml`, `.editorconfig`) | ✅ already existed | Local `pre-commit install` runs gofmt/vet/builder-syntax before CI sees the commit |
| 26 | OpenSSF Scorecard + badge | ✅ done | Weekly `scorecard.yml` run publishes to the Security tab; README badge reads from scorecard.dev |
| 27 | Nightly cross-platform matrix | ✅ done | `nightly.yml`: native macOS and Windows build + test runs plus a fuzz smoke window, none blocking PRs |
| 28 | Docs site (MkDocs Material) | 🟡 scaffold | `mkdocs.yml` renders docs/ with search + Edit-this-page; done when it is deployed to a URL and README links it |
| 29 | CI cold-boot job lost its id (silently replaced the `player` test job) | ✅ fixed | `ci.yml` job list parses with all eight jobs as their own entries |
| 30 | Fuzz targets for parsers (configs, package metadata, URL handling) | ⬜ | Start when a parser gains parsing tests; at least one `Fuzz*` test per parser, seeded corpora, refined by the nightly fuzz smoke |
| 31 | Golden/snapshot tests for config output | ⬜ | Start when config emission churns; a `testdata/golden/` diff in CI, updated deliberately via `-update` |
| 32 | `llms.txt` at repo root | ⬜ | One command on release regeneration effort only — add when the docs site is live so it can be generated from the same nav |
| 33 | CHANGELOG distribution: signed artifacts + SBOM + sha256sums per release | ⬜ | Next time `release.yml` changes; smallest accepted slice first (checksums file), then cosign/SBOM if a consumer asks |
| 34 | PRIVACY.md | ⬜ | Before any telemetry/collector feature ships (the player is offline-first, so this may stay unwritten until then) |
| 35 | Homebrew/Scoop/AUR/docker distribution channels | ⬜ | Only after the Windows/Linux desktop builds have users asking; a channel nobody installs from is maintenance without payoff |
| 36 | docs/LOGGING.md | 🟡 logging shipped | **Shipped:** `log/slog` with scene/operation fields (`LogScene`, `LogOp`), the in-app log viewer, rotation and the redacted diagnostics bundle; conventions are in [docs/STYLE.md](docs/STYLE.md) and CONTRIBUTING. **Finish:** the dedicated page (what to log, field names, levels) — write it when someone has to read a device log they did not write |
| 37 | Self-updater resilience: transactional swap + recovery chord + boot guard | 🟡 core done | Verification (signature, size, free space, disk-full guard) and journal-driven rollback of the swap are in place. **Done this review:** recovery chord (L+R+Start restores the previous binary from the newest backup set, [player/safeboot.go](player/safeboot.go)) and the boot guard: a counter in the patch state directory counts three consecutive fast (≤90 s) failed boots within a 72 h update window and auto-rolls back, booting the restored build in safe mode; the counter resets on graceful exit, version change and long healthy runs. Tests cover counting, the clock-reset case and the counter reset at version change |
| 38 | TUF-secured updater metadata (root/targets/snapshot/timestamp) | ⬜ triggering conditions agreed | Design note written: [docs/UPDATER_TUF.md](docs/UPDATER_TUF.md) records what TUF adds over the current Ed25519 package signatures, the three adopt triggers (external mirror, key-compromise exposure, second publisher) and the smallest slices; code starts only when one fires |

## Appendix B: KPIs

| Metric | Signal it answers | Where to read it | Target after 90 days |
|--------|-------------------|------------------|----------------------|
| Time to first response on issues/PRs | Is anyone *here*? | Insights → Reviews / Issue analytics (manual while <30 datapoints) | <72 h median |
| Time to first merged PR by a new contributor | Onboarding works | Insights → Contributors, first-merge dates | <14 days median |
| CI pass rate | The gate is green and trustworthy | Insights → Merge queue | >90 % |
| Flaky-test rate | Tests mean something | `flaky` label changes per sprint | <1 per 2 weeks |
| Coverage trend | More code ships with tests | CI coverage summaries over time | Trending up; the absolute number is secondary |
| Dependency age + vulnerability MTTR | Supply chain is fresh | Dependabot PR age; GitHub Security tab | No PR older than 21 days; advisories acted on <7 days |
| Release cadence + beta feedback volume | Shipping regularly | Releases page | One release ~every 4 weeks |
| Open good first issues | Newcomers always have a path | Label filter | ≥1 at all times |
| Cold-boot time trend | Interactions still feel snappy | `player-boot` CI job medians | Never above the 5 s desktop budget |
| CI wall-clock (PR feedback) | Waiting is the #1 PR deterrent | Insights → CI minutes, per-job durations | <10 min to first runnable result |
| Good-first-issue conversion | Starter tasks are real work, not decoration | Issues closed by a first-time contributor | ≥1 in the current six |

Every roadmap item gets its DoD line before work starts — "enable Discussions"
is done when the categories exist, the welcome post is pinned, *and* the
README links to Discussions, not when the toggle is flipped.

## Appendix A: Starter issues (ready to paste)

Five small, real, verifiable tasks, written so a newcomer needs no codebase
history. Three have shipped and stay here as the bar a starter task has to clear;
the last two are open and ready to paste with
`gh issue create --title ... --body-file body.md --label "good first issue"`.

### 1. Make `errors.Is` work across the launch-error chain

**Files:** the wraps are spread across the player; `player/launch_errors.go`
consumes causes (it maps them to suggestions) but contains no `fmt.Errorf` of
its own.
A launch failure that goes through `fmt.Errorf("...: %v", err)` breaks callers
that want to check the cause with `errors.Is` / `errors.As`. Convert those wraps
to `%w`, then prove sentinel matching works end to end.

**Status: done.** Every `fmt.Errorf` that formats an error now uses `%w`
(`netplay.go`, `tools.go`, `crypto.go`, `systemcontrol.go`,
`windows_runtime_install.go`, `patch_resolve.go` and the rest), and the
convention is enforced rather than documented:
`player/errorwrap_audit_test.go` walks the player's source with `go/ast` and
fails on a `%v`-formatted error argument. The launch path is covered
behaviourally too: a launch failure that keeps `exec.ErrNotFound` gets the
specific "launcher missing" suggestion from `launchFailureSuggestion` instead
of the generic one.

### 2. Edge-case tests for `parsePosInt` / `StringOrInt`

**Files:** `player/core.go`, `player/config.go` + `player/config_numeric_dialect_test.go`
Config values arrive as `30`, `"30"`, `"30px"`, `""`, huge numbers. The
helpers are load-bearing for every element position on screen but had thin
direct coverage: separate truth from accident for `30px`, negative values,
overflowing integers, and whitespace.

**Status: done.** The table tests pin the two readers: sizes are strict
(`parsePosInt`: integer, non-negative, no unit suffix), while `x`/`y` go through
the lenient `intOrString` (`"30px"`, padding, truncating floats). The one
surprise they exposed was fixed: `parsePosInt` rejected padded values while
`intOrString` trimmed, so a `" 30 "` width fell back to the element default;
it now trims, and the `width`/`height` row in
[docs/CONFIGURATION.md](docs/CONFIGURATION.md#elements) says what is accepted.

### 3. Check every relative link in docs/*.md and README.md

**Files:** `player/docs_links_test.go`
Docs move around (`CONFIGURATION.md` was restructured this month), so relative
links rot silently. The check walks every Markdown file in the checkout
(vendored and generated trees excluded), resolves each relative link target and
fails on missing paths, with URL fragments stripped before checking.

**Status: done.** It runs as part of `make test`/`make check`, and it found
four broken links on the way in, including links to an Android-on-A133P study
that was later removed and a `player/README.md` link to a
`player/SECURITY.md` that does not exist.

### 4. Visible focus states in the web builder

**Files:** root `style.css`, `index.html`
The builder is meant to be keyboard-friendly (config authors open it next to
a handheld), but buttons and inputs rely on default browser styles: with a
custom dark palette the native focus ring is nearly invisible.

**Done when:** interactive controls show a clear `:focus-visible` state from
the theme variables, established builder widgets keep their appearance
between keyboard and mouse use, and a screenshot in the PR shows before/after.

### 5. Accessible names for the builder toolbar

**Files:** root `index.html`
Buttons whose entire content is a single icon or a `::before` glyph expose
nothing to assistive tech. Give each toolbar button an accessible name
(`aria-label`, or a visually-hidden text span for the icon-only controls).

**Done when:** every button/field in `index.html` has an accessible name, and
the browser's accessibility tree (DevTools → Accessibility) shows it.

## Appendix C: Maintainer setup checklist

GitHub-side clicks and one-off commands (cannot live in a commit):

```bash
# 1. Labels, once, from the repo root (gh CLI)
gh label create area/docs --color 0075ca --force || true
gh label create area/go --color 1d76db --force || true
gh label create area/web-builder --color 0e8a16 --force || true
gh label create area/a11y --color d93f0b --force || true
gh label create priority/high --color b60205 --force || true
gh label create priority/low --color cccccc --force || true
gh label create flaky --color fef2c0 --force || true
gh label create "needs reproduction" --color e99695 --force || true

# 2. Paste each Appendix A issue, e.g.
#    gh issue create --title "Accessible names for the builder toolbar" \
#      --body-file body.md --label "good first issue, area/web-builder"
```

In the web UI:

- **Settings → General → Discussions** → enable → create Q&A, Ideas,
  Show and tell, Development → pin the welcome post (link SUPPORT.md + docs).
- **Settings → General → Features → Issues** → enable. The three templates in
  `.github/ISSUE_TEMPLATE/` only appear on *New issue* once the tracker is on,
  and both the README and the templates assume it is.
- **Settings → Security → Private vulnerability reporting** → enable; this is
  the channel SECURITY.md's reporting section links to.
- **Settings → Branches / Actions** — required checks stay the small set CI
  already runs. Resist a bigger matrix (Appendix D).

## Appendix D: Keeping it small

- Templates + Discussions + Dependabot + CodeQL + good first issues come
  first; they cost nothing per contributor.
- Coverage thresholds and RFCs only when they solve an observed problem, not
  an imagined one.
- Required checks stay the small set CI runs today — a large CI matrix slows
  every PR and teaches people to ignore red.
- A triage rotation, office-hours thread, and GOVERNANCE.md wait until there
  are two or more active maintainers; a solo-maintainer version of those is
  just a calendar reminder.
