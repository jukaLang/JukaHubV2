package main

// apkstore.go — the APK Store (Start → Games → APK Store).
//
// AndroidPak can start one kind of Android game — native (NDK) code, through
// the AndroidRT runtime — but using it meant writing a game profile by hand.
// The store is the front door: it finds the APKs on the card (and the ones a
// phone sends), says plainly what each one is and whether it can run here, adds
// the ones worth trying to the game profiles in one press, and plays them
// through the same launch path. Its three tabs:
//
//   - Your APKs: every APK found, with a verdict (apkstore_verdict.go) — "Native
//     game — try it", "Minecraft class pack", "May not run", "Needs Android's
//     Java runtime", "Wrong CPU" — and A for what can be done with it.
//   - Store: a short, honest catalogue (apkstore_catalog.go) of titles and
//     kinds of APK, where to get them legally and the route that works when
//     AndroidRT cannot.
//   - Runtime: whether AndroidRT is installed, whether this machine can run it,
//     and whether a real Android (Waydroid) is there to install APKs into.
//
// Nothing pretends. AndroidRT has not run a game on a handheld yet, so "try it"
// is the strongest word on the screen, and a Java app is never offered as
// something it can start.
//
// The screen uses the tool screens' frame (tbxui.go): Home's glass, the shared
// header, a focus list that scrolls, the shared sheet. Reading the card is scene
// work, so leaving the screen stops it.

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	apkStoreSceneName = "ApkStore"
	apkStoreEntryName = "APK Store"
	// apkStoreGlyph is a phone: "store" draws the packages glyph, which the
	// PortMaster row right above it in the Start menu already uses.
	apkStoreGlyph = "phone"
	// apkStoreIconTextures bounds the icon textures kept between frames.
	apkStoreIconTextures = 48
)

// The tabs.
const (
	apkTabMine = iota
	apkTabStore
	apkTabRuntime
	apkTabCount
)

var apkStoreTabNames = [apkTabCount]string{"Your APKs", "Store", "Runtime"}

// apkStoreUIState is the screen's state; render-thread only (goroutines post
// their results through tbxPost).
type apkStoreUIState struct {
	visit tbxVisit
	nav   tbxNav
	tab   int

	scanning  bool
	gen       int
	scanned   bool
	apks      []apkStoreInfo
	games     map[string]apkStoreGameState
	searched  []string
	roots     []string
	truncated bool
	took      time.Duration

	runtime  apkStoreRuntime
	waydroid apkStoreWaydroid
	probed   bool

	catalog    apkStoreCatalogData
	catalogSrc string

	// busy names work in flight by APK path or catalogue id ("Installing…").
	busy map[string]string
	// icons are the uploaded launcher icons by APK path.
	icons map[string]*sdl.Texture

	// The words the page draws, rebuilt when what they describe changes
	// (apkStoreRebuildText) rather than on every frame.
	status      string
	tabLabels   [apkTabCount]string
	subs        []string
	rtRows      []apkStoreRow
	rootsShort  string
	rootsLong   string
	catalogNote string
}

// apkStoreRow is one prepared row of the Runtime tab.
type apkStoreRow struct {
	id   string
	spec tbxRowSpec
}

var apkStoreUI apkStoreUIState

// apkStoreLive is the scan's progress, written by the scan goroutine.
var apkStoreLive struct {
	mu          sync.Mutex
	done, total int
}

func apkStoreLiveSet(done, total int) {
	apkStoreLive.mu.Lock()
	apkStoreLive.done, apkStoreLive.total = done, total
	apkStoreLive.mu.Unlock()
}

func apkStoreLiveGet() (int, int) {
	apkStoreLive.mu.Lock()
	defer apkStoreLive.mu.Unlock()
	return apkStoreLive.done, apkStoreLive.total
}

// apkStoreScanEnabled is a test seam: render tests never walk the card.
var apkStoreScanEnabled = true

// ── Scene ───────────────────────────────────────────────────────────────────

// ensureApkStoreScene gives the screen its scene, in memory, like
// ensureRommScene: no shipped config carries it and the user's config is never
// rewritten for it. Idempotent; called at startup and after every reload.
func ensureApkStoreScene(config *Config) {
	if config == nil {
		return
	}
	if sceneIndexExact(config, apkStoreSceneName) < 0 {
		config.Scenes = append(config.Scenes, SceneConfig{Name: apkStoreSceneName})
	}
	for i := range config.Scenes {
		if config.Scenes[i].Name == apkStoreSceneName {
			config.Scenes[i].Elements = nil
			config.Scenes[i].Layout = ""
		}
	}
}

// apkStoreOpen opens the store (the Start menu row).
func apkStoreOpen(config *Config) {
	if config == nil {
		config = appConfig
	}
	if config == nil {
		return
	}
	ensureApkStoreScene(config)
	idx := sceneIndexExact(config, apkStoreSceneName)
	if idx < 0 {
		return
	}
	qs.open = false
	if idx == currentSceneIndex {
		return
	}
	changeSceneTo(config, idx)
}

type apkStoreScene struct{ sceneBase }

func (apkStoreScene) render(r *sdl.Renderer, c *Config) bool {
	apkStoreRender(r, c)
	return true
}

func (apkStoreScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	// Tab and Shift+Tab are the keyboard's L1/R1 for tabs, everywhere.
	if e != nil && e.Type == sdl.KEYDOWN && e.Keysym.Sym == sdl.K_TAB && !tbxSheetVisible(c) {
		dir := 1
		if e.Keysym.Mod&sdl.KMOD_SHIFT != 0 {
			dir = -1
		}
		apkStoreSwitchTab(apkStoreUI.tab + dir)
		return true
	}
	return tbxDispatch(c, tbxKeyAction(e), apkStoreOnAction)
}

func (apkStoreScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), apkStoreOnAction)
}

func init() { registerScene(apkStoreScene{sceneBase{name: apkStoreSceneName}}) }

// ── Work ────────────────────────────────────────────────────────────────────

// apkStoreEnter runs once per visit: the textures of the last visit go, and
// the catalogue and the card are read again in the background (even the
// catalogue's first read touches the disk, which a frame must not wait for).
func apkStoreEnter() {
	apkStoreDropIcons()
	apkStoreUI.nav.scroll = 0
	apkStoreUI.nav.focus = "tab" + strconv.Itoa(apkStoreUI.tab)
	apkStoreStartScan()
	apkStoreRebuildText()
	if !apkStoreScanEnabled {
		// Tests: no goroutines and no network, the built-in list at once.
		apkStoreUI.catalog, apkStoreUI.catalogSrc = apkStoreCatalog()
		apkStoreRebuildText()
		return
	}
	runSceneTask(func(ctx context.Context) {
		data, src := apkStoreCatalog()
		tbxPost(func() {
			apkStoreUI.catalog, apkStoreUI.catalogSrc = data, src
			apkStoreRebuildText()
		})
		if !apkStoreCatalogDue(time.Now()) {
			return
		}
		updated, err := apkStoreRefreshCatalog(ctx, apkStoreCatalogClient(), apkStoreCatalogURL)
		if err != nil {
			logDebug("apkstore", "catalogue refresh: %v", err)
			return
		}
		if updated {
			data, src := apkStoreCatalog()
			tbxPost(func() {
				apkStoreUI.catalog, apkStoreUI.catalogSrc = data, src
				apkStoreRebuildText()
			})
		}
	})
}

// apkStoreStartScan reads the card, the runtime and Waydroid in the background.
// A scan the user walked away from is dropped, not shown half done.
func apkStoreStartScan() {
	apkStoreUI.gen++
	gen := apkStoreUI.gen
	if !apkStoreScanEnabled {
		apkStoreUI.scanning = false
		return
	}
	apkStoreUI.scanning = true
	apkStoreLiveSet(0, 0)
	started := runSceneTask(func(ctx context.Context) {
		roots := apkStoreScanRoots()
		tbxPost(func() {
			if gen == apkStoreUI.gen {
				apkStoreUI.roots = roots
				apkStoreRebuildText()
			}
		})
		rt := apkStoreProbeRuntime()
		wd := apkStoreProbeWaydroid(apkStoreCurrentWaydroidHost())
		res := apkStoreScan(ctx, roots, apkStoreLiveSet)
		if ctx.Err() != nil {
			tbxPost(func() {
				if gen == apkStoreUI.gen {
					apkStoreUI.scanning = false
					apkStoreRebuildText()
				}
			})
			return
		}
		games := apkStoreGameStates(res.apks)
		tbxPost(func() {
			if gen != apkStoreUI.gen {
				return
			}
			// The highlight stays on the same APK when the list re-sorts.
			focusPath := ""
			if i, ok := apkStoreRowIndex(apkStoreUI.nav.focus, "apk"); ok {
				focusPath = apkStoreUI.apks[i].Path
			}
			apkStoreUI.scanning = false
			apkStoreUI.scanned = true
			apkStoreUI.apks = res.apks
			apkStoreUI.games = games
			apkStoreUI.searched = res.searched
			apkStoreUI.truncated = res.truncated
			apkStoreUI.took = res.took
			apkStoreUI.runtime, apkStoreUI.waydroid, apkStoreUI.probed = rt, wd, true
			if apkStoreUI.tab == apkTabRuntime {
				apkStoreMaybeCheckUpdate()
			}
			if focusPath != "" {
				for i, info := range res.apks {
					if info.Path == focusPath {
						apkStoreUI.nav.focus = apkStoreRowID("apk", i)
						break
					}
				}
			}
			apkStoreDropIcons()
			apkStoreRebuildText()
		})
	})
	if !started {
		apkStoreUI.scanning = false
	}
	apkStoreRebuildText()
}

// apkStoreRefreshGames re-reads which APKs are in the game profiles after an
// add or a remove.
func apkStoreRefreshGames() {
	gen := apkStoreUI.gen
	apks := append([]apkStoreInfo(nil), apkStoreUI.apks...)
	runSceneTask(func(context.Context) {
		games := apkStoreGameStates(apks)
		tbxPost(func() {
			if gen == apkStoreUI.gen {
				apkStoreUI.games = games
			}
		})
	})
}

// apkStoreSetBusy marks (or, with "", clears) work in flight on key.
func apkStoreSetBusy(key, what string) {
	if apkStoreUI.busy == nil {
		apkStoreUI.busy = map[string]string{}
	}
	if what == "" {
		delete(apkStoreUI.busy, key)
		return
	}
	apkStoreUI.busy[key] = what
}

// ── Icons ───────────────────────────────────────────────────────────────────

// apkStoreDropIcons destroys the uploaded icons (render thread).
func apkStoreDropIcons() {
	for k, t := range apkStoreUI.icons {
		if t != nil {
			t.Destroy()
		}
		delete(apkStoreUI.icons, k)
	}
}

// apkStoreIconTexture uploads an APK's icon the first time its row is drawn.
// The cache is bounded; past the bound it starts over (rows re-upload as they
// come into view).
func apkStoreIconTexture(renderer *sdl.Renderer, info apkStoreInfo) *sdl.Texture {
	if renderer == nil || info.Icon == nil {
		return nil
	}
	if t, ok := apkStoreUI.icons[info.Path]; ok {
		return t
	}
	if apkStoreUI.icons == nil {
		apkStoreUI.icons = map[string]*sdl.Texture{}
	}
	if len(apkStoreUI.icons) >= apkStoreIconTextures {
		apkStoreDropIcons()
	}
	t := imgvTexture(renderer, info.Icon)
	apkStoreUI.icons[info.Path] = t // nil too: a failed upload is not retried every frame
	return t
}

// ── Input ───────────────────────────────────────────────────────────────────

// apkStoreSwitchTab shows tab i (wrapping).
func apkStoreSwitchTab(i int) {
	i = (i%apkTabCount + apkTabCount) % apkTabCount
	if i == apkStoreUI.tab {
		return
	}
	apkStoreUI.tab = i
	apkStoreUI.nav.scroll = 0
	apkStoreUI.nav.focus = "tab" + strconv.Itoa(i)
	PlayNavSound()
	if i == apkTabRuntime {
		apkStoreMaybeCheckUpdate()
	}
}

// apkStoreMaybeCheckUpdate asks, in the background, whether a newer AndroidRT
// than the downloaded copy exists — only when that copy is the one in use
// (the bundled one updates with JukaHub). The Runtime tab then offers Update.
func apkStoreMaybeCheckUpdate() {
	rt := apkStoreUI.runtime
	if !apkStoreScanEnabled || !apkStoreUI.probed || rt.dir == "" || rt.source != androidRTSourceDownloaded {
		return
	}
	androidRTCheckUpdate(false)
}

// apkStoreRuntimeChanged reads where AndroidRT is again after an install, and
// which added games can start now (render thread; the reading runs behind).
func apkStoreRuntimeChanged() {
	if !apkStoreScanEnabled {
		apkStoreRebuildText()
		return
	}
	gen := apkStoreUI.gen
	go func() {
		rt := apkStoreProbeRuntime()
		tbxPost(func() {
			if gen == apkStoreUI.gen {
				apkStoreUI.runtime, apkStoreUI.probed = rt, true
			}
			apkStoreRebuildText()
		})
	}()
	if apkStoreOnScreen() {
		apkStoreRefreshGames()
	}
}

// apkStoreRuntimeMissing reports whether this machine could run AndroidRT but
// has no usable copy — the case where Play and Add offer to install it. Not
// while it is being installed: the sheets then say so and the install bar
// shows the progress.
func apkStoreRuntimeMissing() bool {
	if androidRTInstalling() {
		return false
	}
	rt := apkStoreUI.runtime
	return apkStoreUI.probed && rt.canRun && rt.dir == ""
}

// apkStoreOfferRuntime is the one-press offer Play and Add make when AndroidRT
// is missing: what it is, where it comes from, and a button that installs it
// and then does what was asked (then runs only after a successful install).
func apkStoreOfferRuntime(config *Config, title, what string, then func()) {
	if androidRTInstalling() {
		ShowToast("AndroidRT is being installed — "+title+" can start once the install bar is done", ToastKindInfo)
		return
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title: "AndroidRT is not installed", subtitle: title, icon: "chip",
		body: title + " runs on AndroidRT, the runtime JukaHub uses for Android games, and this device does not " +
			"have it yet. JukaHub can install it now from its own GitHub releases: a few MB, checked against the " +
			"release before it is used. The install bar shows its progress; the Runtime tab's AndroidRT row can " +
			"stop it.",
		buttons: []tbxSheetButton{
			{label: "Not now"},
			{label: what, primary: true, run: func() {
				androidRTStartInstall(func(err error) {
					// Only where it was asked for: someone who left the store
					// meanwhile gets the toast, not a game starting.
					if err == nil && then != nil && apkStoreOnScreen() {
						then()
					}
				})
			}},
		},
		focus: 1,
	})
	PlayActivateSound()
}

// apkStoreOnAction is the screen's input, whatever device it came from.
func apkStoreOnAction(config *Config, act tbxAction) bool {
	nav := &apkStoreUI.nav
	switch act {
	case tbxActConfirm:
		apkStoreActivate(config, nav.focus)
		return true
	case tbxActX:
		if i, ok := apkStoreRowIndex(nav.focus, "apk"); ok {
			apkStoreOpenDetails(config, apkStoreUI.apks[i])
			return true
		}
		if i, ok := apkStoreRowIndex(nav.focus, "cat"); ok {
			apkStoreOpenCatalogEntry(config, apkStoreUI.catalog.Entries[i])
			return true
		}
		return true
	case tbxActY:
		if !apkStoreUI.scanning {
			PlayActivateSound()
			apkStoreStartScan()
		}
		return true
	case tbxActPageUp:
		apkStoreSwitchTab(apkStoreUI.tab - 1)
		return true
	case tbxActPageDown:
		apkStoreSwitchTab(apkStoreUI.tab + 1)
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(nav, act)
}

// apkStoreRowIndex parses a row id ("apk3") into its index, checked against
// the list it belongs to.
func apkStoreRowIndex(id, prefix string) (int, bool) {
	rest, ok := strings.CutPrefix(id, prefix)
	if !ok || rest == "" {
		return 0, false
	}
	i, err := strconv.Atoi(rest)
	if err != nil || i < 0 {
		return 0, false
	}
	switch prefix {
	case "apk":
		return i, i < len(apkStoreUI.apks)
	case "cat":
		return i, i < len(apkStoreUI.catalog.Entries)
	}
	return 0, false
}

// apkStoreActivate presses the control with focus id.
func apkStoreActivate(config *Config, id string) {
	switch {
	case id == "":
		return
	case strings.HasPrefix(id, "tab"):
		if i, err := strconv.Atoi(strings.TrimPrefix(id, "tab")); err == nil {
			apkStoreSwitchTab(i)
		}
		return
	case id == "rescan":
		// The button is drawn disabled while a scan runs; A on it then does
		// nothing rather than starting a second walk of the card.
		if !apkStoreUI.scanning {
			PlayActivateSound()
			apkStoreStartScan()
		}
		return
	case id == "rt-androidrt":
		apkStoreOpenRuntimeSheet(config)
		return
	case id == "rt-device":
		apkStoreOpenDeviceSheet(config)
		return
	case id == "rt-waydroid":
		apkStoreOpenWaydroidSheet(config)
		return
	case id == "rt-profiles":
		PlayActivateSound()
		openGameProfilePanel(false)
		return
	case id == "rt-about":
		apkStoreOpenAboutSheet(config)
		return
	}
	if i, ok := apkStoreRowIndex(id, "apk"); ok {
		apkStoreOpenAPK(config, apkStoreUI.apks[i])
		return
	}
	if i, ok := apkStoreRowIndex(id, "cat"); ok {
		apkStoreOpenCatalogEntry(config, apkStoreUI.catalog.Entries[i])
	}
}

// apkStoreSceneClick routes a click or a tap (or the pointer's A): the sheet
// first, then the control under the pointer. A miss is left for the bars.
func apkStoreSceneClick(mx, my int32, config *Config) bool {
	if tbxCurrentScene(config) != apkStoreSceneName {
		return false
	}
	if tbxSheetVisible(config) {
		tbxSheetMouse(mx, my)
		return true
	}
	it, ok := apkStoreUI.nav.hit(mx, my)
	if !ok {
		return false
	}
	apkStoreUI.nav.focus = it.id
	if !it.disabled {
		apkStoreActivate(config, it.id)
	}
	return true
}

// apkStoreSceneWheel scrolls the screen, or its open sheet.
func apkStoreSceneWheel(config *Config, y int32) bool {
	if y == 0 || tbxCurrentScene(config) != apkStoreSceneName {
		return false
	}
	if tbxSheetVisible(config) {
		if y > 0 {
			tbxSheetAction(tbxActUp)
		} else {
			tbxSheetAction(tbxActDown)
		}
		return true
	}
	apkStoreUI.nav.scrollBy(-y * 48)
	return true
}

// apkStoreFooterHints is the footer while the store is on screen.
func apkStoreFooterHints(config *Config) []FooterHint {
	if tbxSheetVisible(config) {
		return tbxSheetHints()
	}
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}}
	focus := apkStoreUI.nav.focus
	switch {
	case strings.HasPrefix(focus, "tab"):
		hints = append(hints, FooterHint{Button: "A", Label: "Show"})
	case focus == "rescan":
		hints = append(hints, FooterHint{Button: "A", Label: "Rescan"})
	case strings.HasPrefix(focus, "apk"):
		hints = append(hints, FooterHint{Button: "A", Label: "Options"}, FooterHint{Button: "X", Label: "Details"})
	case strings.HasPrefix(focus, "cat"), strings.HasPrefix(focus, "rt-"):
		hints = append(hints, FooterHint{Button: "A", Label: "Open"})
	}
	hints = append(hints, FooterHint{Button: "B", Label: "Back"})
	if !apkStoreUI.scanning {
		// Y does nothing while a scan runs, so it is not offered then.
		hints = append(hints, FooterHint{Button: "Y", Label: "Rescan"})
	}
	return append(hints, FooterHint{Button: "L1/R1", Label: "Tabs"})
}

// ── Actions ─────────────────────────────────────────────────────────────────

// apkStoreCanPlay reports whether this machine can start AndroidRT games, and
// if not, why — the same sentence everywhere it is needed.
func apkStoreCanPlay() (bool, string) {
	rt := apkStoreUI.runtime
	switch {
	case !rt.canRun && IsWindows():
		return false, "Android games run on the handheld: AndroidRT is a Linux arm64 runtime, so this PC can check " +
			"APKs but not play them."
	case !rt.canRun:
		return false, "AndroidRT runs on arm64 Linux (the handheld); this machine is " + rt.host + "."
	case rt.dir == "" && androidRTInstalling():
		return false, "AndroidRT, the runtime these games run on, is being installed right now: the install bar shows " +
			"its progress, and Play works once it is done."
	case rt.dir == "":
		return false, "AndroidRT, the runtime these games run on, is not installed yet: JukaHub can install it from " +
			"its own releases (Runtime tab)."
	case rt.err != "":
		return false, "AndroidRT at " + rt.dir + " is incomplete: " + rt.err + "."
	}
	return true, ""
}

// apkStoreOnScreen reports whether the store is the screen in front, so an
// answer that arrives later opens a sheet only where it belongs (elsewhere a
// toast says it).
func apkStoreOnScreen() bool {
	return tbxCurrentScene(appConfig) == apkStoreSceneName
}

// apkStoreAdd adds an APK to the game profiles. Writing the profile (with its
// fsyncs) and dropping the profile cache — which can wait for a scan holding
// it — happen in the background; the row says "Adding…" meanwhile.
func apkStoreAdd(config *Config, info apkStoreInfo) {
	if _, busy := apkStoreUI.busy[info.Path]; busy {
		return
	}
	apkStoreSetBusy(info.Path, "Adding…")
	go func() {
		p, err := apkStoreAddProfile(info)
		tbxPost(func() {
			apkStoreSetBusy(info.Path, "")
			if err != nil {
				PlayErrorSound()
				if apkStoreOnScreen() {
					tbxOpenSheet(config, tbxSheetSpec{
						title: "Could not add " + info.Label, icon: "warning",
						body: err.Error(),
					})
				} else {
					ShowToast("Could not add "+info.Label+": "+err.Error(), ToastKindError)
				}
				return
			}
			if apkStoreUI.games == nil {
				apkStoreUI.games = map[string]apkStoreGameState{}
			}
			apkStoreUI.games[info.Path] = apkStoreGameState{ID: p.ID, Ours: true, Title: p.Title}
			apkStoreRefreshGames()
			PlaySuccessSound()
			ShowToast("Added "+p.Title+" to Games — on the Games Library's Android row and in Game Profiles", ToastKindSuccess)
		})
	}()
}

// apkStoreAddOrOffer is Add to Games: with AndroidRT missing on a device that
// can run it, it first offers the install (one press installs it, then adds
// the game); otherwise it adds at once.
func apkStoreAddOrOffer(config *Config, info apkStoreInfo) {
	if !apkStoreRuntimeMissing() {
		apkStoreAdd(config, info)
		return
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title: "Add " + info.Label + " to Games", subtitle: "AndroidRT is not installed yet", icon: "chip",
		body: info.Label + " will run on AndroidRT, the runtime JukaHub uses for Android games, and this device " +
			"does not have it yet. JukaHub can install it from its own GitHub releases (a few MB, checked against the " +
			"release before it is used) and add the game. Adding it alone is fine too: it is ready the moment " +
			"AndroidRT is.",
		buttons: []tbxSheetButton{
			{label: "Just add", run: func() { apkStoreAdd(config, info) }},
			{label: "Install and add", primary: true, run: func() {
				// The game is added at once (it needs nothing to be added);
				// the install runs beside it on the install bar.
				apkStoreAdd(config, info)
				androidRTStartInstall(nil)
			}},
		},
		focus: 1,
	})
	PlayActivateSound()
}

// apkStoreRemove takes the store's own profile away again; the APK stays.
func apkStoreRemove(info apkStoreInfo) {
	st, ok := apkStoreUI.games[info.Path]
	if !ok || !st.Ours {
		return
	}
	if _, busy := apkStoreUI.busy[info.Path]; busy {
		return
	}
	apkStoreSetBusy(info.Path, "Removing…")
	go func() {
		err := apkStoreRemoveProfile(st.ID)
		tbxPost(func() {
			apkStoreSetBusy(info.Path, "")
			if err != nil {
				PlayErrorSound()
				ShowToast("Could not remove the profile: "+err.Error(), ToastKindError)
				return
			}
			delete(apkStoreUI.games, info.Path)
			apkStoreRefreshGames()
			PlayBackSound()
			ShowToast("Removed "+info.Label+" from Games — the APK stays on the card", ToastKindInfo)
		})
	}()
}

// apkStorePlay launches the APK's profile through the Game Profiles path. The
// whole launch runs in the background: finding the profile rebuilds the
// catalogue (every profile read, every AndroidPak APK triaged) and starting it
// stages the game's libraries and assets to tmpfs, which takes seconds for a
// big game. The row says "Starting…" until the answer is back. The start is
// claimed here, on the render thread, so a Play in the Games Library meanwhile
// cannot start the game a second time (launchGameProfile).
func apkStorePlay(config *Config, info apkStoreInfo) {
	st, ok := apkStoreUI.games[info.Path]
	if !ok {
		return
	}
	if _, busy := apkStoreUI.busy[info.Path]; busy {
		return
	}
	if gameProfileRunningPID(st.ID) > 0 {
		ShowToast(info.Label+" is already running", ToastKindInfo)
		return
	}
	if !gameProfileClaimStart(st.ID) {
		ShowToast(info.Label+" is already starting", ToastKindInfo)
		return
	}
	// Read here: the launch goroutine must not read the config.
	tl := timelapseSettingsFor(appConfig)
	apkStoreSetBusy(info.Path, "Starting…")
	path := info.Path
	go func() {
		var entry gameProfileEntry
		var err error
		found, rtMissing := false, false
		func() {
			// Released once the game has started (its PID is recorded by
			// then) or the start failed, before the answer is posted.
			defer gameProfileReleaseStart(st.ID)
			entry, found = apkStoreFindEntry(st.ID)
			if found && entry.ready() {
				err = launchGameProfileClaimed(entry, tl)
			} else if found {
				// The one thing Play can put right itself: no AndroidRT yet.
				rtMissing = androidRTSupported() && androidRuntimeMissingNow()
			}
		}()
		tbxPost(func() {
			apkStoreSetBusy(path, "")
			switch {
			case !found:
				PlayErrorSound()
				ShowToast("The game profile is gone — add it again", ToastKindError)
				apkStoreRefreshGames()
			case rtMissing && apkStoreOnScreen():
				apkStoreOfferRuntime(config, entry.Profile.Title, "Install and play", func() { apkStorePlay(config, info) })
			case !entry.ready():
				PlayErrorSound()
				if !apkStoreOnScreen() {
					ShowToast("Can't start "+entry.Profile.Title+" yet: "+entry.Issue, ToastKindError)
					return
				}
				tbxOpenSheet(config, tbxSheetSpec{
					title: "Can't start " + entry.Profile.Title + " yet", icon: "warning",
					body: entry.Issue + "\n\nThe Runtime tab says what is installed and what is missing.",
					buttons: []tbxSheetButton{
						{label: "Close"},
						{label: "Runtime", primary: true, run: func() { apkStoreSwitchTab(apkTabRuntime) }},
					},
					focus: 1,
				})
			case err != nil:
				logWarn("apkstore", "launch %s failed: %v", entry.Profile.ID, err)
				logPath := filepath.Join(gameProfilesLogsDir(), entry.Profile.ID+".log")
				if !regularFile(logPath) {
					logPath = ""
				}
				showLaunchFailureDialog(entry.Profile.Title, err, logPath)
			default:
				PlaySuccessSound()
				ShowToast("Launching "+entry.Profile.Title+" (experimental)", ToastKindSuccess)
			}
		})
	}()
}

// apkStoreInstallWaydroid hands the APK to Waydroid in the background. Only an
// install Waydroid's own app list confirms is called installed.
func apkStoreInstallWaydroid(info apkStoreInfo) {
	wd := apkStoreUI.waydroid
	if !wd.canInstall() {
		return
	}
	if _, busy := apkStoreUI.busy[info.Path]; busy {
		return
	}
	apkStoreSetBusy(info.Path, "Installing…")
	label := info.Label
	path := info.Path
	pkg := info.Manifest.Package
	go func() {
		res, err := apkStoreWaydroidInstall(context.Background(), apkStoreCurrentWaydroidHost(), wd.binary, path, pkg)
		tbxPost(func() {
			apkStoreSetBusy(path, "")
			switch {
			case err != nil:
				PlayErrorSound()
				ShowToast(err.Error(), ToastKindError)
			case res == apkWaydroidInstalled:
				PlaySuccessSound()
				ShowToast("Installed "+label+" into Android — open it from Waydroid", ToastKindSuccess)
			default:
				ShowToast("Sent "+label+" to Waydroid — check its app list; it did not show up there yet", ToastKindInfo)
			}
		})
	}()
}

// apkStoreDownloadEntry fetches a catalogue entry's verified APK.
func apkStoreDownloadEntry(e apkStoreCatalogEntry) {
	if _, busy := apkStoreUI.busy[e.ID]; busy {
		return
	}
	if !IsOnline() {
		PlayErrorSound()
		ShowToast("Downloading needs internet", ToastKindWarning)
		return
	}
	apkStoreSetBusy(e.ID, "Downloading…")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), patchDownloadTimeout)
		defer cancel()
		dest, err := apkStoreDownload(ctx, e)
		tbxPost(func() {
			apkStoreSetBusy(e.ID, "")
			if err != nil {
				PlayErrorSound()
				ShowToast("Download failed: "+err.Error(), ToastKindError)
				return
			}
			PlaySuccessSound()
			ShowToast("Saved "+e.Title+" to "+filepath.Dir(dest)+" (checked against its SHA-256)", ToastKindSuccess)
			if tbxCurrentScene(appConfig) == apkStoreSceneName {
				apkStoreStartScan()
			}
		})
	}()
}

// ── Sheets ──────────────────────────────────────────────────────────────────

// apkStoreOpenAPK is A on an APK: what it is, and what can be done with it.
func apkStoreOpenAPK(config *Config, info apkStoreInfo) {
	st, inGames := apkStoreUI.games[info.Path]
	var body strings.Builder
	body.WriteString(info.Verdict + "\n\n" + info.Why)
	// Buttons read like a dialog: the quiet ones first, the primary last.
	buttons := []tbxSheetButton{{label: "Details", run: func() { apkStoreOpenDetails(config, info) }}}
	if busy := apkStoreUI.busy[info.Path]; busy != "" {
		body.WriteString("\n\n" + busy)
	}
	wd := apkStoreUI.waydroid
	if wd.usable() && info.Tier != apkTierBroken && info.Tier != apkTierWrongCPU && info.Tier != apkTierSplit {
		if wd.canInstall() {
			body.WriteString("\n\nThis machine has Waydroid, a full Android in a container: it can install this APK, Java " +
				"apps included. The app then opens from Waydroid, not from JukaHub.")
			buttons = append(buttons, tbxSheetButton{label: "Install into Android", run: func() { apkStoreInstallWaydroid(info) }})
		} else {
			body.WriteString("\n\nThis machine has Waydroid, a full Android in a container, but its session is stopped. " +
				"Start it with `waydroid session start`, then press Y here to read again: the store can then install " +
				"this APK into it.")
		}
	}
	canPlay, why := apkStoreCanPlay()
	switch {
	case inGames:
		body.WriteString("\n\n## In Games\n")
		if st.Ours {
			body.WriteString("Added by the APK Store as the game profile " + st.ID + ".")
		} else {
			body.WriteString("A game profile (" + st.ID + ") already launches this APK.")
		}
		if st.Issue != "" {
			body.WriteString(" It cannot start right now: " + st.Issue + ".")
		}
		// A hand-written profile can launch an APK the reading above says
		// cannot run; Play stays available, but it is not the suggestion.
		wontRun := !info.Tier.canAdd()
		if wontRun {
			body.WriteString(" The store's reading above says it cannot run on AndroidRT; the profile still starts " +
				"it if you want to see for yourself.")
		}
		if st.LastRun != "" {
			body.WriteString("\n\n## Last run\n" + st.LastRun)
		}
		if st.Ours {
			buttons = append(buttons, tbxSheetButton{label: "Remove from Games", run: func() { apkStoreRemove(info) }})
		}
		switch {
		case canPlay:
			buttons = append(buttons, tbxSheetButton{label: "Play", primary: !wontRun, run: func() { apkStorePlay(config, info) }})
		case apkStoreRuntimeMissing():
			body.WriteString("\n\n" + why + " The button below installs it (a few MB, checked against the release " +
				"before it is used) and then starts the game.")
			buttons = append(buttons, tbxSheetButton{label: "Install AndroidRT and play", primary: !wontRun, run: func() {
				androidRTStartInstall(func(err error) {
					// A game starts only where it was asked for: someone who
					// left the store meanwhile gets the toast, not a launch.
					if err == nil && apkStoreOnScreen() {
						apkStorePlay(config, info)
					}
				})
			}})
		default:
			body.WriteString("\n\n" + why)
		}
	case info.Tier.canAdd():
		if !canPlay {
			body.WriteString("\n\n" + why + " Adding it now is fine: it is ready the moment AndroidRT is.")
		}
		buttons = append(buttons, tbxSheetButton{label: "Add to Games", primary: true, run: func() { apkStoreAddOrOffer(config, info) }})
	}
	focus := 0
	for i, b := range buttons {
		if b.primary {
			focus = i
		}
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    info.Label,
		subtitle: filepath.Base(info.Path),
		icon:     apkStoreGlyph,
		body:     body.String(),
		buttons:  buttons,
		focus:    focus,
	})
	PlayActivateSound()
}

// apkStoreDetailsText is everything the store read, for the details sheet.
func apkStoreDetailsText(info apkStoreInfo, st apkStoreGameState, inGames bool) string {
	m := info.Manifest
	var b strings.Builder
	line := func(label, value string) {
		if strings.TrimSpace(value) != "" {
			b.WriteString("• " + label + ": " + value + "\n")
		}
	}
	b.WriteString("## Verdict\n" + info.Verdict + "\n" + info.Why + "\n\n## The package\n")
	line("Package", m.Package)
	version := m.VersionName
	if m.VersionCode != 0 {
		if version != "" {
			version += " "
		}
		version += "(code " + strconv.FormatInt(m.VersionCode, 10) + ")"
	}
	line("Version", version)
	if m.MinSDK > 0 || m.TargetSDK > 0 {
		line("Android", fmt.Sprintf("minimum API %d, target API %d", m.MinSDK, m.TargetSDK))
	}
	line("Starts in", m.Launcher)
	if m.NativeLib != "" {
		// What Android loads for android.app.lib_name: libX.so, as written.
		line("NativeActivity library", "lib"+m.NativeLib+".so")
	}
	switch {
	case m.Split != "":
		line("Split piece", m.Split+" (one part of a split app)")
	case m.SplitsRequired:
		line("Split app", "the base piece; it needs its split_config pieces")
	}
	switch info.Entry {
	case apkEntryFound:
		line("Native entry", "a library exports a NativeActivity or GameActivity entry point")
	case apkEntryNone:
		line("Native entry", "none — the libraries are helpers for Java code")
	case apkEntryUnknown:
		line("Native entry", "not checked in full (the libraries are too large to read here)")
	}
	if !m.HasCode && info.ManifestErr == "" {
		line("Java code", "none (hasCode=false)")
	}
	if len(info.ABIs) > 0 {
		abis := strings.Join(info.ABIs, ", ")
		if info.ChosenABI != "" {
			abis += " — would run " + info.ChosenABI
		}
		line("Native code", abis)
	} else {
		line("Native code", "none")
	}
	if info.EntryLib != "" {
		entry := filepath.Base(info.EntryLib)
		if info.MainLib != "" {
			entry = info.MainLib + " (named by the manifest)"
		}
		line("Entry library", fmt.Sprintf("%s, %d libraries", entry, info.LibCount))
	}
	line("Engine", info.Engine)
	if m.GLES > 0 {
		line("OpenGL ES", fmt.Sprintf("%d.%d", m.GLES>>16, m.GLES&0xFFFF))
	}
	if info.Size > 0 {
		line("Size", humanBytes(info.Size))
	}
	line("File", info.Path)
	if info.ManifestErr != "" {
		line("Manifest", "not readable ("+info.ManifestErr+")")
	}
	if inGames {
		b.WriteString("\n## In Games\n")
		owner := "written by hand or imported"
		if st.Ours {
			owner = "added by the APK Store"
		}
		line("Profile", st.ID+" ("+owner+")")
		if st.Issue != "" {
			line("Can't start", st.Issue)
		} else {
			line("State", "ready to launch")
		}
		if st.LogPath != "" {
			line("Launch log", st.LogPath)
		}
		if st.LastRun != "" {
			b.WriteString("\n## Last run\n" + st.LastRun + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// apkStoreOpenDetails is X on an APK: everything the store knows.
func apkStoreOpenDetails(config *Config, info apkStoreInfo) {
	st, inGames := apkStoreUI.games[info.Path]
	tbxOpenSheet(config, tbxSheetSpec{
		title:    info.Label,
		subtitle: "What the APK says about itself",
		icon:     "info",
		body:     apkStoreDetailsText(info, st, inGames),
		tall:     true,
	})
	PlayActivateSound()
}

// apkStoreCatalogStatusLabel is the pill for a catalogue status.
func apkStoreCatalogStatusLabel(status string) (string, tbxTone) {
	switch status {
	case apkStatusWorks:
		return "Works", tbxToneGood
	case apkStatusExperimental:
		return "Experimental", tbxToneWarn
	}
	return "Won't run", tbxToneBad
}

// apkStoreOpenCatalogEntry is A on a Store row.
func apkStoreOpenCatalogEntry(config *Config, e apkStoreCatalogEntry) {
	status, _ := apkStoreCatalogStatusLabel(e.Status)
	var b strings.Builder
	b.WriteString(e.Summary)
	if e.Get != "" {
		b.WriteString("\n\n## Getting it\n" + e.Get)
	}
	if e.Notes != "" {
		b.WriteString("\n\n## What to expect\n" + e.Notes)
	}
	if e.Route != "" {
		b.WriteString("\n\n## What works instead\n" + e.Route)
	}
	if e.License != "" {
		b.WriteString("\n\nLicence: " + e.License)
	}
	var buttons []tbxSheetButton
	if e.Kind != "category" || e.Status != apkStatusWontRun {
		buttons = append(buttons, tbxSheetButton{label: "Your APKs", run: func() { apkStoreSwitchTab(apkTabMine) }})
	}
	focus := 0
	if e.downloadable() {
		b.WriteString("\n\nThe download is checked against its published SHA-256 before it is kept (" + humanBytes(e.Size) + ").")
		buttons = append(buttons, tbxSheetButton{label: "Download", primary: true, run: func() { apkStoreDownloadEntry(e) }})
		focus = len(buttons) - 1
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    e.Title,
		subtitle: status,
		icon:     apkStoreGlyph,
		body:     b.String(),
		buttons:  buttons,
		focus:    focus,
	})
	PlayActivateSound()
}

// apkStoreRuntimeText is the AndroidRT sheet.
func apkStoreRuntimeText(rt apkStoreRuntime) string {
	var b strings.Builder
	line, installing := androidRTInstallLine()
	newer, newerTag := androidRTUpdateKnown()
	switch {
	case installing:
		b.WriteString(line + "\n\nThe install bar shows it on every screen; Stop ends it.\n\n")
	case rt.dir != "" && rt.err == "":
		b.WriteString("AndroidRT is ready: " + apkStoreRuntimeWhere(rt) + ".\nFolder: " + rt.dir + " (started through its " +
			rt.form + ").\n\n")
		if rt.source == androidRTSourceDownloaded && newer {
			b.WriteString(newerTag + " is out. Update installs it; the copy you have stays in use until the new one " +
				"has been checked.\n\n")
		}
	case rt.dir != "":
		b.WriteString("AndroidRT was found at " + rt.dir + ", but " + rt.err + ".\n\n")
	case rt.canRun:
		b.WriteString("AndroidRT is not installed. Install downloads it from JukaHub's own GitHub releases (a few MB). " +
			"The package is checked against the release (its signed manifest, or GitHub's checksum) and its own " +
			"checksum list, and its loader has to start on this device before it is kept.\n\n")
	default:
		b.WriteString("AndroidRT is an arm64 Linux runtime: Android games run on the handheld, and this machine reads " +
			"APKs only.\n\n")
	}
	if len(rt.skipped) > 0 {
		b.WriteString("## Passed over\n")
		for _, s := range rt.skipped {
			b.WriteString("• " + s + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("## Where it comes from\n" +
		"• Every TrimUI JukaHub package carries it beside the app (Apps/JukaHub/androidrt); it updates with JukaHub.\n" +
		"• Without that copy, Install puts the same build in JukaHub's data folder, and the Emulator updates " +
		"setting keeps it current.\n" +
		"• A runtime you built yourself (player/androidrt/build-tsp.sh) in /mnt/SDCARD/Apps/AndroidRT is used " +
		"first; JUKAHUB_ANDROIDRT can point anywhere.\n\n" +
		"## What it is\n" +
		"AndroidRT loads an Android game's native library with its own bionic loader, answers the game's Java calls " +
		"itself (it has no Java runtime), and draws through the firmware's own SDL2 and EGL. It is experimental: no " +
		"game has been run on a handheld with it yet. docs/ANDROID.md has the details and the device-trial steps.")
	return b.String()
}

func apkStoreOpenRuntimeSheet(config *Config) {
	rt := apkStoreUI.runtime
	_, installing := androidRTInstallLine()
	newer, newerTag := androidRTUpdateKnown()
	var buttons []tbxSheetButton
	switch {
	case installing:
		buttons = []tbxSheetButton{{label: "Close"}, {label: "Stop", run: androidRTStopInstall}}
	case !apkStoreUI.probed:
	case rt.dir == "" && rt.canRun:
		buttons = []tbxSheetButton{{label: "Close"}, {label: "Install", primary: true, run: func() { androidRTStartInstall(nil) }}}
	case rt.dir != "" && rt.source == androidRTSourceDownloaded && newer:
		buttons = []tbxSheetButton{{label: "Close"}, {label: "Update to " + newerTag, primary: true, run: func() { androidRTStartInstall(nil) }}}
	case rt.dir != "" && rt.source == androidRTSourceDownloaded:
		buttons = []tbxSheetButton{{label: "Close"}, {label: "Check for an update", run: func() { androidRTCheckUpdate(true) }}}
	}
	focus := 0
	if len(buttons) > 0 {
		focus = len(buttons) - 1
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title: "AndroidRT", subtitle: "The runtime the APK Store's games run on", icon: "chip",
		body: apkStoreRuntimeText(rt), tall: true, buttons: buttons, focus: focus,
	})
	PlayActivateSound()
}

func apkStoreOpenDeviceSheet(config *Config) {
	rt := apkStoreUI.runtime
	var b strings.Builder
	b.WriteString("This machine is " + rt.host)
	if rt.memMB > 0 {
		b.WriteString(", with " + strconv.FormatInt(rt.memMB, 10) + " MB of RAM")
	}
	b.WriteString(".\n\n")
	if rt.canRun {
		b.WriteString("AndroidRT runs arm64 Android code natively here. Before a launch JukaHub checks that the game " +
			"fits in the free memory and refuses it with the numbers if it does not (a 1 GB handheld has little room " +
			"once the hub and the staged game are counted).")
	} else {
		b.WriteString("AndroidRT is an arm64 Linux runtime, so games run on the handheld, not here. The store still " +
			"reads APKs here, which is handy for checking a file before copying it over.")
	}
	tbxOpenSheet(config, tbxSheetSpec{title: "This device", icon: "hardware", body: b.String()})
	PlayActivateSound()
}

func apkStoreOpenWaydroidSheet(config *Config) {
	wd := apkStoreUI.waydroid
	var b strings.Builder
	if wd.usable() {
		state := "stopped — start it with `waydroid session start` before installing"
		if wd.running {
			state = "running"
		}
		b.WriteString("Waydroid is installed (" + wd.binary + ") and the kernel has a binder device. Its session is " +
			state + ".\n\nOn an APK, A offers \"Install into Android\": the APK goes into Waydroid's Android, Java apps " +
			"included, and opens from Waydroid's own window.")
	} else {
		b.WriteString(wd.reason + "\n\nWaydroid runs a whole Android in a container, which needs the binder kernel " +
			"module. A stock TrimUI kernel has none, so on the handheld AndroidRT (native games only) is the route; " +
			"on a Linux machine with Waydroid set up, the store can install APKs into it.")
	}
	tbxOpenSheet(config, tbxSheetSpec{title: "Android container (Waydroid)", icon: "devices", body: b.String()})
	PlayActivateSound()
}

func apkStoreOpenAboutSheet(config *Config) {
	body := "An APK is a zip of Java bytecode, native libraries and resources. Running an ordinary app needs " +
		"Android's runtime (ART) and framework — several hundred megabytes of RAM before the app starts, on a 1 GB " +
		"handheld. AndroidRT takes the other road: it runs the native part of a game directly, and answers the few " +
		"Java calls a native game makes itself.\n\n" +
		"## Can run (experimental)\n" +
		"• Games started by NativeActivity or GameActivity, built for 64-bit ARM (arm64-v8a)\n" +
		"• Minecraft: Bedrock Edition (64-bit), through its class pack\n\n" +
		"## Cannot run\n" +
		"• Java and Kotlin apps — almost every app on Android, including the ones that carry helper libraries\n" +
		"• Unity, Unreal, Godot, GameMaker, LÖVE, SDL and Cocos2d-x games: they start in Java\n" +
		"• Xamarin/.NET apps such as Terraria's mobile edition\n" +
		"• 32-bit-only builds (armeabi-v7a): AndroidRT loads 64-bit libraries only\n" +
		"• x86-only APKs (PC-emulator builds)\n" +
		"• One piece of a split app (base.apk, split_config…): it needs a single, universal APK\n\n" +
		"## What works instead\n" +
		"Many of those games have Linux builds that PortMaster runs on the handheld, and PortMaster's GMLoader runs a " +
		"number of GameMaker games from their APK. Your own APKs only: JukaHub never downloads paid apps."
	tbxOpenSheet(config, tbxSheetSpec{title: "What AndroidRT can run", icon: "info", body: body, tall: true})
	PlayActivateSound()
}

// apkStoreRebuildText prepares the strings the page draws: the header's
// read-out, the tab labels, each APK's line, the Runtime rows and the folder
// lists of the empty and working states.
func apkStoreRebuildText() {
	ui := &apkStoreUI
	ui.status = apkStoreHeaderStatus()
	for i := 0; i < apkTabCount; i++ {
		ui.tabLabels[i] = apkStoreTabNames[i]
	}
	if ui.scanned {
		ui.tabLabels[apkTabMine] += " · " + strconv.Itoa(len(ui.apks))
	}
	ui.subs = ui.subs[:0]
	for _, info := range ui.apks {
		ui.subs = append(ui.subs, apkStoreRowSubtitle(info))
	}
	ui.rtRows = apkStoreRuntimeRows()
	ui.rootsShort = apkStoreRootsLine(4)
	ui.rootsLong = apkStoreRootsLine(6)
	src := ui.catalogSrc
	if src == "" {
		src = "built in"
	}
	ui.catalogNote = "What is known about Android titles here, and where to get them legally. Catalogue: " + src + "."
}

// apkStoreHeaderStatus is the read-out opposite the title.
func apkStoreHeaderStatus() string {
	if apkStoreUI.scanning {
		return "Reading the card…"
	}
	if !apkStoreUI.scanned {
		return ""
	}
	n := len(apkStoreUI.apks)
	parts := []string{strconv.Itoa(n) + " APK"}
	if n != 1 {
		parts[0] += "s"
	}
	switch {
	case !apkStoreUI.probed:
	case apkStoreUI.runtime.dir != "" && apkStoreUI.runtime.err == "":
		parts = append(parts, "AndroidRT ready")
	case apkStoreUI.runtime.canRun:
		parts = append(parts, "AndroidRT missing")
	}
	return strings.Join(parts, " · ")
}
