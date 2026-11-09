# JukaHub UX review — November 2026

A design review of the handheld shell as it stands after the glass rollout, written
from the chair of a console-UI designer (PS5, Switch, Steam Deck). It covers the
Home screen first, because every session starts there, then the shared components
every scene is built from, then the most-used scenes. Each finding has a severity,
the evidence in the code, the fix, and whether this pass implemented it.

**Method and limits.** The review was done by reading the renderers and layout code
against the design sheet (`mockups/home-16x9-and-4x3.html`) and one rendered Home
frame at 1280×720. Nothing was run on a device: every "implemented" item is
statically checked, not seen on the panel. The items marked *needs device* should be
confirmed on a Smart Pro and a Brick before they are closed.

**Severity.** P0 blocks a task or misleads the user; P1 hurts every session; P2 is
friction or inconsistency a user notices; P3 is polish.

## Principles for a 5-inch handheld at arm's length

1. **Legibility is physical.** The Smart Pro's 4.96" 1280×720 panel is ~296 PPI and is
   read from 30–40 cm. A 14 px canvas glyph is about 1.2 mm tall — the smallest text a
   user should ever have to read. Body text belongs at 16–18 px, labels at 20 px and up.
   The Brick's 960-wide canvas scales type by 0.75, so its floor is 12 px.
2. **Focus is the cursor.** On a D-pad device the focus indicator is the only pointer.
   It has to be visible at a glance on any wallpaper or box art: one treatment
   everywhere — a 3 px accent ring standing off the control, a brighter fill, and the
   control's own content lit (icon tint).
3. **One primary purpose per screen.** The most valuable space goes to the thing the
   device is picked up for (games, then media), not to plumbing (packages, tools).
4. **The D-pad goes where the eye goes.** Every press lands on the visually adjacent
   control; nothing invisible can take focus; edges behave the same everywhere.
5. **One of each pattern.** One header, one footer-hint row, one list row, one empty
   state, one focus ring. Shared components (`components.go`, `ui_glass.go`) beat
   per-scene drawing, because a fix there fixes fifty screens.
6. **Every state is designed.** Empty, loading, error, offline and success — and an
   empty state says what will appear and offers the first step.
7. **Speak the player's language.** "Games", not "Emulator"; "Screens", not "Scenes";
   "3 games", not "3 game(s)"; never a path placeholder like `Roms/<system>/`.
8. **Destructive actions ask.** Quitting, deleting and resetting are never one stray
   press away and never dressed as a hint.

## Home

| ID | Sev | Finding | Evidence | Fix | Status |
|---|---|---|---|---|---|
| H1 | P1 | The header read "JukaHub - v0.4.0": a build number is developer information, and it took the space the brand should own. Every scene repeated it ("JukaHub - v0.4.0 - Settings"). | `homelayout.go` `drawHeader`, `extras.go` `renderStatusBar`, `headerTitle` | Wordmark alone on Home, "JukaHub - <screen>" on scenes; the version lives in About, the Start menu footer and the update offer. | Implemented |
| H2 | P2 | "Good morning" at 00:30 reads as a broken clock. | `defaultGreeting` | Morning starts at 05:00. | Implemented |
| H3 | P1 | The bell showed "42": every toast — "Saved", "Copied", "Connected" — filed an unread notification, so the count measured activity, not things needing a look. The 11 px badge was also below the floor. | `ShowToast` → `notifRecord`; `drawNotifBell` | Confirmations (info/success) are filed already read; warnings, errors and unseen events still count. The badge caps at "9+" and is 13 px. | Implemented |
| H4 | P2 | "● PS4 ● PS5" read as two status lights; nothing said that pressing one streams a console. On Windows each pill could only answer with a warning. | `pspills.go`, `homelayout.go` `drawHeader` | The capsule opens with a "Remote Play" caption (dropped first on a narrow band) and is not drawn where Remote Play cannot run (`psRemoteSupported`). | Implemented |
| H5 | P1 | The largest card on the screen said "Nothing played recently" on every new device. | `drawContinueCard`, `homeContinueSnapshot` | A first-run card that says what it will hold and offers the first step: "Get started · Your next game starts here · Browse games" on a handheld (A opens the library), the media equivalent on the desktop. | Implemented |
| H6 | P1 | The device's main purpose was the fifth tile of the bottom row, named after its plumbing ("Emulator — Retro library") and opening the emulator manager rather than the games. | `rebuildItems`, `homeActivate` | The **Games** card sits beside the hero under Media and opens the game library (consoles, games, box art). The emulator manager stays one step away (Start → Games → Emulators). Unedited shipped layouts migrate (`home_migrate.go`); a config that still says "Emulator" maps to the card (`homeTitleAlias`). | Implemented |
| H7 | P1 | Tile subtitles, badges and metadata were drawn at 12–13 px, and the Brick's 0.75 scale took 14 px captions to 11 px on a 3.2" panel. | `loadHomeFonts`, ~20 `getFontSized(…, 12/13)` call sites | One legibility floor at the font choke point (`uiLegibleFontSize`): authored 12–13 px opens at 14 px, never below 12 px on the compact canvas. | Implemented — *needs device* for fixed pills |
| H8 | P1 | Focus was a 3 px stroke on the card's own edge — on the Media card it was indistinguishable from the resting accent border, so the screen looked like it had two selections. | `drawHomeFocusRing`, `drawMediaCard` | The ring stands 4 px off the card (a halo with a dark gap), the focused fill and edge step up, the tile's icon lights in the accent, and the Media card's resting edge is quieter. | Implemented |
| H9 | P1 | A permanent "Exit" pill sat in the footer among the controller hints: a destructive, unconfirmed action dressed as a hint, one Down press from the bottom row. | `drawFooter`, focus graph | Home draws no Exit pill and the D-pad cannot reach it. Quitting is the platform's Select + Start chord (named in the footer) or Start menu → X, which goes through a confirmation. **Revised 2026-11-09 (owner's request):** the pill is back as a power button at the footer's left end, and the missing confirmation is what changed — it opens an "Exit JukaHub?" card with Exit focused (`home_power.go`). | Revised |
| H10 | P2 | Ten equal-weight destinations; system plumbing (Packages, Apps, Tools, Settings) competes with content. | `rebuildItems` | Games promoted (H6). A content row + system dock composition is worth prototyping. | Follow-up task |
| H11 | P2 | In the "Hero grid" arrangement the D-pad followed the dashboard graph, so Down from Media went to a card on its right. | `HomeFocusController.neighbor` | Inside the body the grid arrangement steps by geometry (`homeGridStep`); leaving upwards still uses the graph. | Implemented |
| H12 | P3 | Icon stroke weights differ (the Packages box is drawn as a solid 3D shape next to outline glyphs). | `icondraw.go` | A stroke-weight pass over the glyph set at 32 px, checked on the panel. | Follow-up task — *needs device* |
| H13 | P3 | The Wi-Fi glyph in the status cluster reads faint next to the clock in the rendered frame. | header status cluster | Check that a connected meter draws at full contrast and only "no signal" is dim. | Follow-up task — *needs device* |

## Shared components (fix once, every scene benefits)

| ID | Sev | Finding | Evidence | Fix | Status |
|---|---|---|---|---|---|
| C1 | P1 | The selected list row wore a 2 px ring — a hairline at arm's length — and a marked or playing row's tint could out-shout it. | `UIListRow` | 3 px glass ring, a stronger accent wash. | Implemented |
| C2 | P2 | Empty states printed title and explanation at the same 24 px (two titles), and the explanation was one unclipped line that ran out of narrow panels. | `uiStateBlock` | The explanation is an 18 px caption wrapped to two lines inside 24 px side padding; the title is ellipsized. ~50 call sites use this block. | Implemented |
| C3 | P2 | Header chips, the update pill and the game-library rows used 2 px rings while tiles used 3 px. | `renderStatusBar`, `drawUpdatePill`, game library rows | Every ring is `glassFocusRingWidth` (3 px). | Implemented |
| C4 | P2 | Type-scale drift: screens pick literal pixel sizes instead of roles — the media hub alone uses 13, 14, 15, 16, 17, 18 and 19 px. Hierarchy differs from screen to screen. | `getFontSized(config, "small", N)` across `media_hub_render.go`, `pkgui.go`, `tbxui.go`, `wifiscreen.go`… | Map every literal to a five-step role scale (Caption 14, Label 16, Body 18, Heading 22, Title 28) via `FontForRole`. | Follow-up task |
| C5 | P3 | Engineering copy: "(s)" plurals in hundreds of strings, "N/A" in ~25 readouts. | grep `(s)`, `"N/A"` | `pluralizeCount`; "Unknown" or hide the row. | Partly (Games library) |
| C6 | P2 | The bar's title is the brand on every screen, and many screens repeat their name in a page header right under it, so the bar says "JukaHub - Settings" over a header that says "Settings". | `drawHeaderBrand`, `UIPageHeader` users | One page-title pattern: either the bar carries the screen name prominently, or the page header does and the bar shows only the brand. | Follow-up task — *needs device* |

## Start menu

| ID | Sev | Finding | Evidence | Fix | Status |
|---|---|---|---|---|---|
| S1 | P2 | Rows printed their config icon as text — "*", "~", ">>", "[D]", ">_", "pkg" — which reads as stray punctuation. | `renderStartMenu` | Vector glyphs from the shared icon set (`startMenuGlyph`); a row without one keeps its mark. | Implemented |
| S2 | P2 | The first group was "Scenes" — the config's word, not the player's. | `startMenuGroups` | "Screens". | Implemented |
| S3 | P1 | The 24 px subtitle ran from the 64 px header band into the first row, and the footer hung past the card's bottom edge. | `renderStartMenu` | Caption-size subtitle and footer, vertically centred in their bands. | Implemented |
| S4 | P1 | With the Exit pill gone, quitting needs an obvious, safe path. | — | X = "Quit JukaHub" (named in the footer, beside the build number), through the Exit screen's confirmation. | Implemented |

## Games library

| ID | Sev | Finding | Evidence | Fix | Status |
|---|---|---|---|---|---|
| G1 | P1 | Status and empty-state copy was engineering text: "12 game(s) in 3 console(s) · 9 cover(s) (75%)", "copy ROMs into Roms/<system>/". This is now the screen Home's Games card opens. | `gameLibraryStatus`, `renderGameLibraryPanel` | "12 games in 3 consoles · 75% with covers"; "Put games in the Roms folder on the card, one folder per console (Roms/GBA, Roms/PS), then press Y to rescan." | Implemented |
| G2 | P2 | The game rows' focus ring was 2 px. | `renderGameLibraryPanel` | 3 px. | Implemented |
| G3 | P2 | The empty library was two left-aligned text lines in the list area instead of the shared empty-state block with a glyph. | `renderGameLibraryPanel` | `UIEmptyState` with the gamepad glyph, centred in the games column, like the ~50 other empty states. | Implemented |

## Exit screen

| ID | Sev | Finding | Evidence | Fix | Status |
|---|---|---|---|---|---|
| E1 | P1 | The confirmation offered **Save · Back · Exit now** under "Save your settings first…" — but settings, favorites and progress are written as they change and flushed by the teardown, and "Save" was the Appearance page's design save. Three buttons for a yes/no question, with the riskiest default focus. | both `jukaconfig*.json`, Exit scene; `shutdownSteps` | "Quit JukaHub?" · "Your settings, favorites and progress are saved as you go. Quit now, or go back." · **Back** (default focus) · **Quit**. | Implemented |

## Other scenes — read in code, not changed this pass

- **Tools hub, Settings hub, Downloads, Files:** already on the shared page header, tile
  and list-row components, so they inherit C1–C3. Their remaining issues are C4 (type
  drift) and C6 (double titles).
- **Media hub, Music, Tube:** the richest screens and the worst type drift (C4). The
  queue and resume work is solid; the 13 px badge text is lifted by the floor.
- **Splash and teardown:** the glass card is honest (each row is a real step) and short
  on a fast boot. Owned by the performance pass; no change here.

## What the next design pass should do

The follow-up tasks with acceptance criteria are carried into
`player/MODERNIZATION.md`: the role-based type scale (C4), one page-title pattern (C6),
the remaining "(s)"/"N/A" copy (C5), the icon stroke pass (H12), the status-glyph
contrast (H13), a content-row + system-dock Home prototype (H10), and an on-device
review of everything marked *needs device*.
