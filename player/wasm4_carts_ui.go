package main

// wasm4_carts_ui.go — "Get WASM-4 carts": wasm4.org's games as a list, each
// installed onto the WASM-4 shelf with one press.
//
// Reached from the game page's More sheet of any WASM-4 game (wasm4.go) and
// from the Start menu's Games section. The screen is the tool screens' frame
// (tbxui.go): the page header, a list of carts on the left — title, author,
// and a pill for Installed / Installing / Queued / Failed — and, where the
// screen is wide enough, the highlighted cart's screenshot and details on the
// right. A plays an installed cart or installs one that is not; X deletes one
// this screen installed (its saves stay); Y opens its details: the author,
// the page on wasm4.org with a code to it (and the browser on a PC), and what
// the licence means here. L1/R1 (PgUp/PgDn) page through the list.
//
// Threading is Get Homebrew's: the screen's state belongs to the main thread;
// the list, the screenshots, a cart's manual and the installs run on
// goroutines and post their results back with tbxPost. One screenshot is
// fetched at a time, for the cart the player is on, and only one texture is
// kept (a 160×160 picture). One install runs at a time; more wait in a queue.

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"
)

const (
	wasm4CartsSceneName = "WASM4Carts"
	wasm4CartsEntryName = "Get WASM-4 carts"
)

// The states of a cart's install job.
const (
	wasm4JobQueued = iota + 1
	wasm4JobWorking
	wasm4JobFailed
)

type wasm4Job struct {
	state int
	err   string
}

// w4c is the screen's state. Main thread only.
var w4c struct {
	visit     tbxVisit
	nav       tbxNav
	carts     []wasm4Cart
	ids       []string // "cart:<slug>" per cart, and its row's second line:
	subs      []string // built when the list changes, not every frame
	source    string
	loaded    bool      // the saved list was read once
	loading   bool      // the network read runs
	fetchedAt time.Time // when this run last read the list (monotonic)
	loadErr   string
	installed map[string]wasm4Installed
	jobs      map[string]wasm4Job
	queue     []wasm4Cart
	working   string
	mdBusy    bool
	shot      struct {
		renderer *sdl.Renderer
		slug     string
		tex      *sdl.Texture
		busy     bool
		ready    map[string]bool
		missing  map[string]bool
	}
}

type wasm4CartsScene struct{ sceneBase }

func (wasm4CartsScene) render(r *sdl.Renderer, c *Config) bool {
	wasm4CartsRender(r, c)
	return true
}

func (wasm4CartsScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), wasm4CartsAction)
}

func (wasm4CartsScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), wasm4CartsAction)
}

func init() { registerScene(wasm4CartsScene{sceneBase{name: wasm4CartsSceneName}}) }

// wasm4CartsOnScreen reports whether the screen is the scene showing.
func wasm4CartsOnScreen(config *Config) bool {
	return config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
		config.Scenes[currentSceneIndex].Name == wasm4CartsSceneName
}

// wasm4CartsSceneClick routes a click or a tap (store_scenes.go): the sheet,
// else a cart — the first click picks it, a click on the picked one is A.
func wasm4CartsSceneClick(mx, my int32, config *Config) bool {
	if !wasm4CartsOnScreen(config) {
		return false
	}
	if tbxSheetVisible(config) {
		tbxSheetMouse(mx, my)
		return true
	}
	it, ok := w4c.nav.hit(mx, my)
	if !ok {
		return false
	}
	if w4c.nav.focus != it.id {
		w4c.nav.focus = it.id
		PlayNavSound()
		return true
	}
	wasm4CartsAction(config, tbxActConfirm)
	return true
}

// wasm4CartsSceneWheel scrolls the list or the sheet (store_scenes.go).
func wasm4CartsSceneWheel(config *Config, y int32) bool {
	if y == 0 || !wasm4CartsOnScreen(config) {
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
	w4c.nav.scrollBy(-y * 48)
	return true
}

// wasm4CartsEnsureScene gives the screen its scene in memory, like Get
// Homebrew's (startup and every hot reload: storeScenesEnsure).
func wasm4CartsEnsureScene(config *Config) {
	if config != nil && sceneIndexExact(config, wasm4CartsSceneName) < 0 {
		config.Scenes = append(config.Scenes, SceneConfig{Name: wasm4CartsSceneName})
	}
}

// wasm4CartsOpen opens the screen (the More sheet, the Start menu).
func wasm4CartsOpen(config *Config) {
	if config == nil {
		config = appConfig
	}
	if config == nil {
		return
	}
	wasm4CartsEnsureScene(config)
	idx := sceneIndexExact(config, wasm4CartsSceneName)
	if idx < 0 {
		return
	}
	qs.open = false
	// From a game's More sheet the library is up over the scenes: it closes,
	// or it would hide the screen.
	if gameLib.open {
		closeGameLibraryPanel()
	}
	if idx != currentSceneIndex {
		changeSceneTo(config, idx)
	}
}

// ── Loading ──────────────────────────────────────────────────────────────

// wasm4CartsEnter runs once per visit: the saved list and what is installed
// (the first time), then the network when the list is not fresh.
func wasm4CartsEnter(renderer *sdl.Renderer) {
	wasm4ShotDrop(renderer)
	first := !w4c.loaded
	w4c.loaded = true
	if first {
		// "Reading" until the saved list is in, not "no list yet".
		w4c.loading = true
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("wasm4", "reading the saved carts list: %v", r)
				tbxPost(func() { w4c.loading = false })
			}
		}()
		var carts []wasm4Cart
		source := ""
		if first {
			carts, source = wasm4LoadCarts()
		}
		inst := wasm4InstalledNow()
		tbxPost(func() {
			if first {
				w4c.loading = false
				if len(w4c.carts) == 0 {
					wasm4SetCarts(carts, source)
				}
			}
			w4c.installed = inst
			wasm4CartsRefresh(false)
		})
	}()
}

// wasm4CartsRefresh reads the list from the network unless this run read it
// within wasm4CartsTTL (force: always). Main thread.
func wasm4CartsRefresh(force bool) {
	if w4c.loading || (!force && !w4c.fetchedAt.IsZero() && time.Since(w4c.fetchedAt) < wasm4CartsTTL) {
		return
	}
	if !IsOnline() {
		w4c.loadErr = "offline"
		return
	}
	w4c.loading, w4c.loadErr = true, ""
	old := append([]wasm4Cart(nil), w4c.carts...)
	go func() {
		var carts []wasm4Cart
		source := ""
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("reading the carts list stopped: %v", r)
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			carts, source, err = wasm4FetchCarts(ctx)
		}()
		if err == nil {
			carts = wasm4MergeKnown(carts, old)
			wasm4SaveCarts(carts, source)
		} else {
			logInfo("wasm4", "reading the carts list: %v", err)
		}
		tbxPost(func() {
			w4c.loading = false
			if err != nil {
				w4c.loadErr = firstLine(err.Error())
				return
			}
			wasm4SetCarts(carts, source)
			w4c.fetchedAt = time.Now()
		})
	}()
}

// wasm4MergeKnown keeps what an older list knew about a cart that the new
// one does not say (a listing from GitHub has no authors until the manuals
// are read). Pure.
func wasm4MergeKnown(fresh, old []wasm4Cart) []wasm4Cart {
	known := make(map[string]wasm4Cart, len(old))
	for _, c := range old {
		known[c.Slug] = c
	}
	for i, c := range fresh {
		k, ok := known[c.Slug]
		if !ok {
			continue
		}
		if c.Author == "" {
			fresh[i].Author = k.Author
		}
		if c.Date == "" {
			fresh[i].Date = k.Date
		}
		if c.Title == wasm4TitleFromSlug(c.Slug) && k.Title != "" {
			fresh[i].Title = k.Title
		}
	}
	return wasm4UniqueCarts(fresh)
}

// ── Drawing ──────────────────────────────────────────────────────────────

func wasm4CartsHeader() UIPageHeader {
	status, ink := "", ColorTextTertiary()
	switch {
	case w4c.working != "":
		status, ink = "Installing "+wasm4CartTitle(w4c.working), ToastSuccessInk()
		if n := len(w4c.queue); n > 0 {
			status += " · " + strconv.Itoa(n) + " more queued"
		}
	case w4c.loading && len(w4c.carts) == 0:
		status = "Reading wasm4.org…"
	case w4c.loadErr == "offline" && len(w4c.carts) == 0:
		status, ink = "Offline", ToastWarnInk()
	case w4c.loadErr != "" && len(w4c.carts) == 0:
		status, ink = "List not reachable", ToastWarnInk()
	case len(w4c.carts) > 0:
		status = strconv.Itoa(len(w4c.carts)) + " carts"
		if w4c.loadErr != "" {
			status += " · saved list"
		}
	}
	return tbxHeader(wasm4CartsEntryName, "Free fantasy-console games from wasm4.org, by their authors", "rom", status, ink)
}

// wasm4CartsRender draws the screen.
func wasm4CartsRender(renderer *sdl.Renderer, config *Config) {
	if w4c.visit.begin() {
		wasm4CartsEnter(renderer)
	}
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, wasm4CartsHeader())
	body := frame.bodyRect()
	nav := &w4c.nav
	nav.begin(body.Y, body.H)
	if len(w4c.carts) == 0 {
		nav.end()
		title, text := "Reading the carts list…", "wasm4.org's games appear here in a moment."
		if !w4c.loading {
			title = "No carts list yet"
			text = "Connect to the internet, then press A to read wasm4.org's list."
			if w4c.loadErr != "" && w4c.loadErr != "offline" {
				text = "The list could not be read (" + w4c.loadErr + "). Press A to try again."
			}
		}
		tbxDrawEmpty(renderer, config, body, "rom", title, text)
		peerEndFrame(renderer, config, wasm4CartsHints())
		return
	}
	paneW := int32(0)
	if body.W >= 760 {
		paneW = body.W * 32 / 100
		if paneW > 420 {
			paneW = 420
		}
	}
	listW := body.W
	if paneW > 0 {
		listW = body.W - paneW - 20
	}
	rowH := tbxRowHeight(fonts)
	const gap = int32(8)
	for i := range w4c.carts {
		nav.add(w4c.ids[i], sdl.Rect{X: body.X, Y: int32(i) * (rowH + gap), W: listW, H: rowH}, i, false)
	}
	prev := nav.focus
	nav.end()
	if nav.focus != prev {
		nav.reveal()
	}
	tbxClip(renderer, sdl.Rect{X: body.X, Y: body.Y, W: listW, H: body.H})
	first := int(nav.scroll / (rowH + gap))
	for i := first; i < len(w4c.carts); i++ {
		r := sdl.Rect{X: body.X, Y: body.Y + int32(i)*(rowH+gap) - nav.scroll, W: listW, H: rowH}
		if r.Y > body.Y+body.H {
			break
		}
		tbxDrawRow(renderer, config, fonts, r, wasm4CartRow(i), nav.focused(w4c.ids[i]))
	}
	tbxUnclip(renderer)
	tbxDrawScrollbar(renderer, body.X+listW+6, body.Y, body.H, nav.contentH, nav.scroll)
	if c, ok := wasm4FocusedCart(); ok && paneW > 0 {
		wasm4DrawPane(renderer, config, fonts, sdl.Rect{X: body.X + body.W - paneW, Y: body.Y, W: paneW, H: body.H}, c)
	}
	peerEndFrame(renderer, config, wasm4CartsHints())
}

// wasm4SetCarts takes a new list and builds its row strings. Main thread.
func wasm4SetCarts(carts []wasm4Cart, source string) {
	w4c.carts, w4c.source = carts, source
	w4c.ids = make([]string, len(carts))
	w4c.subs = make([]string, len(carts))
	for i, c := range carts {
		w4c.ids[i] = "cart:" + c.Slug
		w4c.subs[i] = "wasm4.org/play/" + c.Slug
		if c.Author != "" {
			w4c.subs[i] = "by " + c.Author
		}
	}
}

// wasm4CartRow is the list row of cart i.
func wasm4CartRow(i int) tbxRowSpec {
	c := w4c.carts[i]
	row := tbxRowSpec{icon: "rom", title: c.Title, subtitle: w4c.subs[i]}
	switch job := w4c.jobs[c.Slug]; {
	case job.state == wasm4JobWorking:
		row.pill, row.tone = "Installing", tbxToneAccent
	case job.state == wasm4JobQueued:
		row.pill = "Queued"
	case job.state == wasm4JobFailed:
		row.pill, row.tone = "Failed", tbxToneBad
	default:
		if _, ok := w4c.installed[c.Slug]; ok {
			row.pill, row.tone = "Installed", tbxToneGood
		}
	}
	return row
}

// wasm4DrawPane draws the highlighted cart: its screenshot, title, author
// and page.
func wasm4DrawPane(renderer *sdl.Renderer, config *Config, fonts tbxFonts, pane sdl.Rect, c wasm4Cart) {
	fillRoundedRect(renderer, pane.X, pane.Y, pane.W, pane.H, glassRowRadius, glassA(22))
	strokeRoundedRect(renderer, pane.X, pane.Y, pane.W, pane.H, glassRowRadius, 1, glassA(40))
	// The 160×160 screenshot at a whole-pixel scale that fits.
	inner := pane.W - 32
	avail := inner
	if h := pane.H * 7 / 10; h < avail {
		avail = h
	}
	side := int32(160)
	if k := avail / 160; k >= 1 {
		side = 160 * k
	} else if avail > 0 {
		side = avail
	}
	shot := sdl.Rect{X: pane.X + (pane.W-side)/2, Y: pane.Y + 16, W: side, H: side}
	if tex := wasm4ShotTexture(renderer, c.Slug); tex != nil {
		_ = renderer.Copy(tex, nil, &shot)
	} else {
		fillRoundedRect(renderer, shot.X, shot.Y, shot.W, shot.H, 8, glassA(30))
		DrawHomeIcon(renderer, "rom", sdl.Rect{X: shot.X + shot.W/2 - 24, Y: shot.Y + shot.H/2 - 24, W: 48, H: 48}, false)
	}
	x, y := pane.X+16, shot.Y+shot.H+14
	renderText(renderer, config, fonts.title, ellipsize(fonts.title, c.Title, inner), ColorTextPrimary(), x, y)
	y += fonts.titleH + 6
	who := "Author on its page"
	if c.Author != "" {
		who = "by " + c.Author
	}
	if c.Date != "" {
		who += " · " + c.Date
	}
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, who, inner), ColorTextMuted(), x, y)
	y += fonts.smallH + 6
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, "wasm4.org/play/"+c.Slug, inner), ColorTextTertiary(), x, y)
	y += fonts.smallH + 10
	if rec, ok := w4c.installed[c.Slug]; ok && y+fonts.tinyH < pane.Y+pane.H {
		renderText(renderer, config, fonts.tiny, ellipsize(fonts.tiny, "On the shelf as "+filepath.Base(rec.Path), inner), ColorTextTertiary(), x, y)
	} else if job := w4c.jobs[c.Slug]; job.state == wasm4JobFailed && y+fonts.tinyH < pane.Y+pane.H {
		renderText(renderer, config, fonts.tiny, ellipsize(fonts.tiny, job.err, inner), ToastWarnInk(), x, y)
	}
}

// wasm4ShotTexture is the screenshot of slug, or nil while it is fetched (or
// when there is none). One texture is kept. Main thread.
func wasm4ShotTexture(renderer *sdl.Renderer, slug string) *sdl.Texture {
	s := &w4c.shot
	if s.renderer != renderer {
		// A new renderer (a reload): the old texture went with the old one.
		s.renderer, s.tex, s.slug = renderer, nil, ""
	}
	if s.slug == slug && s.tex != nil {
		return s.tex
	}
	if s.slug != slug {
		if s.tex != nil {
			_ = s.tex.Destroy()
			s.tex = nil
		}
		s.slug = slug
	}
	if s.ready[slug] {
		// One try per visit: a picture that does not load is not read again
		// every frame.
		delete(s.ready, slug)
		if s.missing == nil {
			s.missing = map[string]bool{}
		}
		surface, err := img.Load(wasm4ShotPath(slug))
		if err != nil || surface == nil {
			s.missing[slug] = true
			return nil
		}
		tex, err := renderer.CreateTextureFromSurface(surface)
		surface.Free()
		if err != nil {
			s.missing[slug] = true
			return nil
		}
		s.ready[slug] = true
		s.tex = tex
		return tex
	}
	if s.busy || s.missing[slug] || !wasm4SlugOK(slug) {
		return nil
	}
	s.busy = true
	go func() {
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("screenshot: %v", r)
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			err = wasm4FetchShot(ctx, slug)
		}()
		tbxPost(func() {
			s.busy = false
			if err != nil {
				logDebug("wasm4", "screenshot of %s: %v", slug, err)
				if s.missing == nil {
					s.missing = map[string]bool{}
				}
				s.missing[slug] = true
				return
			}
			if s.ready == nil {
				s.ready = map[string]bool{}
			}
			s.ready[slug] = true
		})
	}()
	return nil
}

// wasm4ShotDrop lets the texture go (a new visit). A texture of another
// renderer went with it and is not destroyed again. Main thread.
func wasm4ShotDrop(renderer *sdl.Renderer) {
	if w4c.shot.tex != nil && w4c.shot.renderer == renderer {
		_ = w4c.shot.tex.Destroy()
	}
	w4c.shot.tex, w4c.shot.slug = nil, ""
	// A screenshot that failed while offline gets another chance.
	w4c.shot.missing = nil
}

// ── Input ────────────────────────────────────────────────────────────────

// wasm4FocusedCart is the cart the focus is on.
func wasm4FocusedCart() (wasm4Cart, bool) {
	slug := strings.TrimPrefix(w4c.nav.focus, "cart:")
	for _, c := range w4c.carts {
		if c.Slug == slug {
			return c, true
		}
	}
	return wasm4Cart{}, false
}

// wasm4CartTitle is the title of slug in the list (the id when unknown).
func wasm4CartTitle(slug string) string {
	for _, c := range w4c.carts {
		if c.Slug == slug {
			return c.Title
		}
	}
	return slug
}

// wasm4CartsAction handles one action (the sheet already had its chance in
// tbxDispatch).
func wasm4CartsAction(config *Config, act tbxAction) bool {
	if tbxNavAction(&w4c.nav, act) {
		return true
	}
	c, ok := wasm4FocusedCart()
	switch act {
	case tbxActConfirm:
		if !ok {
			if len(w4c.carts) == 0 && !w4c.loading {
				PlayActivateSound()
				wasm4CartsRefresh(true)
				if w4c.loadErr == "offline" {
					ShowToast("Connect to the internet to read wasm4.org's list", ToastKindWarning)
				}
			}
			return true
		}
		if _, installed := w4c.installed[c.Slug]; installed {
			wasm4PlayCart(config, c)
		} else {
			wasm4Enqueue(c)
		}
		return true
	case tbxActX:
		if _, installed := w4c.installed[c.Slug]; ok && installed {
			wasm4ConfirmDelete(config, c)
		}
		return true
	case tbxActY:
		if ok {
			wasm4OpenDetails(config, c)
		}
		return true
	}
	return false
}

func wasm4CartsHints() []FooterHint {
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}}
	if c, ok := wasm4FocusedCart(); ok {
		job := w4c.jobs[c.Slug]
		if _, installed := w4c.installed[c.Slug]; installed {
			hints = append(hints, FooterHint{Button: "A", Label: "Play"}, FooterHint{Button: "X", Label: "Delete"})
		} else if job.state != wasm4JobQueued && job.state != wasm4JobWorking {
			hints = append(hints, FooterHint{Button: "A", Label: "Install"})
		}
		hints = append(hints, FooterHint{Button: "Y", Label: "Details"}, FooterHint{Button: "L1/R1", Label: "Page"})
	} else if len(w4c.carts) == 0 && !w4c.loading {
		hints = append(hints, FooterHint{Button: "A", Label: "Try again"})
	}
	return append(hints, FooterHint{Button: "B", Label: "Back"})
}

// ── Install, play, delete ────────────────────────────────────────────────

// wasm4Enqueue queues a cart's install. Main thread.
func wasm4Enqueue(c wasm4Cart) {
	if job := w4c.jobs[c.Slug]; job.state == wasm4JobQueued || job.state == wasm4JobWorking {
		return
	}
	if !IsOnline() {
		PlayErrorSound()
		ShowToast("Connect to the internet to install "+c.Title, ToastKindWarning)
		return
	}
	if w4c.jobs == nil {
		w4c.jobs = map[string]wasm4Job{}
	}
	PlayActivateSound()
	w4c.jobs[c.Slug] = wasm4Job{state: wasm4JobQueued}
	w4c.queue = append(w4c.queue, c)
	wasm4RunNext()
}

// wasm4RunNext starts the next queued install when none runs. Main thread.
func wasm4RunNext() {
	if w4c.working != "" || len(w4c.queue) == 0 {
		return
	}
	c := w4c.queue[0]
	w4c.queue = w4c.queue[1:]
	w4c.working = c.Slug
	w4c.jobs[c.Slug] = wasm4Job{state: wasm4JobWorking}
	go func() {
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("the install stopped: %v", r)
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			_, err = wasm4InstallCart(ctx, c)
		}()
		inst := wasm4InstalledNow()
		tbxPost(func() {
			w4c.working = ""
			w4c.installed = inst
			if err != nil {
				logWarn("wasm4", "installing %s: %v", c.Slug, err)
				w4c.jobs[c.Slug] = wasm4Job{state: wasm4JobFailed, err: firstLine(err.Error())}
				ShowToast("Could not install "+c.Title+": "+firstLine(err.Error()), ToastKindError)
			} else {
				delete(w4c.jobs, c.Slug)
				refreshGameLibrary()
				invalidateGameLibrarySystemsCache()
				ShowToast(c.Title+" is on the WASM-4 shelf", ToastKindSuccess)
			}
			wasm4RunNext()
		})
	}()
}

// wasm4PlayCart starts an installed cart through the library's own start, so
// its first start installs the core (wasm4LauncherForGame). Main thread.
func wasm4PlayCart(config *Config, c wasm4Cart) {
	rec := w4c.installed[c.Slug]
	game := libraryGame{Path: rec.Path, System: wasm4System, Title: gameDisplayTitle(rec.Path)}
	chooseGameLauncher(game, func(launcher string) {
		if strings.TrimSpace(launcher) == "" {
			ShowToast("No emulator for WASM-4 games here — see docs/WASM4.md", ToastKindWarning)
			return
		}
		if err := launchLibraryGame(game, launcher); err != nil {
			gameLaunchErrorToast(c.Title, err)
			return
		}
		if gameLaunchWasHeld(game.Path) {
			return // a card asks first (gamelaunch_hooks.go)
		}
		recordPlayed(config, rec.Path)
		PlaySuccessSound()
		ShowToast("Playing "+c.Title, ToastKindSuccess)
	})
}

// wasm4ConfirmDelete asks before an installed cart goes. Main thread.
func wasm4ConfirmDelete(config *Config, c wasm4Cart) {
	rec := w4c.installed[c.Slug]
	PlayErrorSound()
	slug, title := c.Slug, c.Title
	tbxOpenSheet(config, tbxSheetSpec{
		title: "Delete " + title + "?", subtitle: filepath.Base(rec.Path), icon: "trash",
		body: "The cart and the cover this screen saved for it go. Its saves stay, so installing it again later picks up where you left off.",
		buttons: []tbxSheetButton{{label: "Keep it", primary: true}, {label: "Delete", run: func() {
			go func() {
				err := wasm4DeleteCart(slug)
				inst := wasm4InstalledNow()
				tbxPost(func() {
					w4c.installed = inst
					if err != nil {
						ShowToast("Could not delete "+title+": "+firstLine(err.Error()), ToastKindError)
						return
					}
					refreshGameLibrary()
					invalidateGameLibrarySystemsCache()
					ShowToast(title+" deleted", ToastKindInfo)
				})
			}()
		}}},
	})
}

// wasm4DetailsBody is the details sheet's text for c.
func wasm4DetailsBody(c wasm4Cart) string {
	who := "Its author is named on its page."
	if c.Author != "" {
		who = "Made by " + c.Author + "."
	}
	if c.Date != "" {
		who += " Last updated " + c.Date + "."
	}
	return who + "\n\n" +
		"A free game for the WASM-4 fantasy console, shared on wasm4.org by its author. Its page has its manual and its licence; " +
		"JukaHub installs the cart unchanged for your own play and does not share it on.\n\n" +
		"## Playing\n" +
		"• The first WASM-4 game you start installs the WASM-4 player (once, under 1 MB).\n" +
		"• Games that use the mouse: turn on Settings → Controls → Mouse in games and apps, and the pad moves the pointer."
}

// wasm4OpenDetails shows a cart's details with a code to its page; a cart
// whose author is not known yet has its manual read meanwhile. Main thread.
func wasm4OpenDetails(config *Config, c wasm4Cart) {
	PlayActivateSound()
	link := wasm4PageURL(c.Slug)
	token := "wasm4:" + c.Slug
	buttons := []tbxSheetButton{{label: "Close"}}
	if runtime.GOOS == "windows" {
		buttons = append(buttons, tbxSheetButton{label: "Open in the browser", run: func() {
			if err := openExternalURL(link); err != nil {
				ShowToast("Could not open the browser — "+firstLine(err.Error()), ToastKindError)
			}
		}})
	}
	slug := c.Slug
	if _, installed := w4c.installed[slug]; installed {
		buttons = append(buttons, tbxSheetButton{label: "Play", primary: true, run: func() {
			if cart, ok := wasm4CartBySlug(slug); ok {
				wasm4PlayCart(config, cart)
			}
		}})
	} else {
		buttons = append(buttons, tbxSheetButton{label: "Install", primary: true, run: func() {
			if cart, ok := wasm4CartBySlug(slug); ok {
				wasm4Enqueue(cart)
			}
		}})
	}
	tbxOpenSheet(config, tbxSheetSpec{
		token: token, title: c.Title, subtitle: "wasm4.org/play/" + c.Slug, icon: "rom",
		body: wasm4DetailsBody(c), qr: tbxQRModules(link), qrLabel: strings.TrimPrefix(link, "https://"),
		buttons: buttons, focus: len(buttons) - 1,
	})
	if c.Author != "" || w4c.mdBusy || !IsOnline() {
		return
	}
	w4c.mdBusy = true
	go func() {
		var title, author, date string
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("manual: %v", r)
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			var raw []byte
			raw, err = wasm4GetCartFile(ctx, slug, ".md", wasm4MDMax)
			if err == nil {
				title, author, date = wasm4ParseCartMD(raw)
			}
		}()
		tbxPost(func() {
			w4c.mdBusy = false
			if err != nil || (author == "" && title == "") {
				logDebug("wasm4", "manual of %s: %v", slug, err)
				return
			}
			var updated wasm4Cart
			for i := range w4c.carts {
				if w4c.carts[i].Slug != slug {
					continue
				}
				if title != "" {
					w4c.carts[i].Title = title
				}
				if author != "" {
					w4c.carts[i].Author = author
				}
				if date != "" {
					w4c.carts[i].Date = date
				}
				updated = w4c.carts[i]
			}
			if updated.Slug == "" {
				return
			}
			wasm4SetCarts(w4c.carts, w4c.source)
			carts, source := append([]wasm4Cart(nil), w4c.carts...), w4c.source
			go wasm4SaveCarts(carts, source)
			tbxUpdateSheet(token, wasm4DetailsBody(updated), nil)
		})
	}()
}

// wasm4CartBySlug finds a cart of the list.
func wasm4CartBySlug(slug string) (wasm4Cart, bool) {
	for _, c := range w4c.carts {
		if c.Slug == slug {
			return c, true
		}
	}
	return wasm4Cart{}, false
}
