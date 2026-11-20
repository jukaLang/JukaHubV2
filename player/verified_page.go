package main

// verified_page.go — Settings → About → Tested on this device: everything
// JukaHub knows was seen to work, or not, on this model (verified.go keeps
// the results; verified_ui.go has the game page's chip and the check card).
//
// One tool screen (tbxui.go's frame): the header names the model and counts
// what is verified, partly checked and has a known issue; tabs split Games,
// Ports, Apps and Emulators (L1/R1, Tab and Shift+Tab, or A on a tab); each
// row is a title, its verdict as a pill and the verdict's detail line. A on a
// row opens the same check card a game's page opens, over this page. "Export
// results" (or Y) writes this device's own results as a file to share or to
// ship with the next release (vfExport, off the main thread). One quiet line
// says where results come from; a problem reading the files shows above it.
//
// The rows are built when the screen is entered and whenever vfGeneration
// moves, never per frame: a frame lays out and draws prepared strings.

import (
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/veandco/go-sdl2/sdl"
)

// verifiedSceneName is the page's scene, injected in memory with the other
// tool screens (tbxEnsureScenes).
const verifiedSceneName = "TestedOnDevice"

const (
	vfPageTabH     = int32(46)
	vfPageExportID = "export"
)

// vfPageHowText is the line that says where results come from.
const vfPageHowText = "Results come from three places: what JukaHub saw while you played, what you marked on a " +
	"game's card (its page → More → Tested on this device), and lists shipped with JukaHub or copied to its verified/import " +
	"folder. Only this model's results show here."

// vfPageEmptyBody is the empty list's line: nothing to do but play.
const vfPageEmptyBody = "Play something — JukaHub notes what works."

// The tabs: what each lists, its name, its focus id and its rows' glyph.
var (
	vfPageTabKinds = [...]string{vfKindGame, vfKindPort, vfKindApp, vfKindEmulator}
	vfPageTabNames = [...]string{"Games", "Ports", "Apps", "Emulators"}
	vfPageTabIDs   = [...]string{"tab:0", "tab:1", "tab:2", "tab:3"}
	vfPageTabIcons = [...]string{"gamepad", "packages", "apps", "emulator"}
)

// vfPageRow is one prepared row.
type vfPageRow struct {
	navID  string
	kind   string
	id     string
	title  string
	detail string
	pill   string
	tone   tbxTone
}

// vfPage is the screen's state. Main thread only.
var vfPage struct {
	visit tbxVisit
	nav   tbxNav
	tab   int
	// built and gen say the rows are those of vfGeneration gen.
	built     bool
	gen       int64
	loaded    bool
	total     int
	rows      [len(vfPageTabKinds)][]vfPageRow
	tabLabels [len(vfPageTabKinds)]string
	empty     [len(vfPageTabKinds)]string
	header    UIPageHeader
	notice    string
	wrapHow   tbxWrapCache
	wrapNote  tbxWrapCache
}

// vfPageExporting is up while an export runs.
var vfPageExporting atomic.Bool

// verifiedPageOpen opens the page (Settings → About).
func verifiedPageOpen(config *Config) {
	vfEnsureLoaded()
	tbxOpenScene(config, verifiedSceneName)
}

// verifiedSettingsRow is the Settings → About row.
func verifiedSettingsRow() setRow {
	return setRow{
		ID: "verified", Label: "Tested on this device", Kind: setKindAction,
		Detail: setStatic("Games, ports, apps and emulators seen to work on this model"),
		Open:   verifiedPageOpen,
	}
}

type vfPageScene struct{ sceneBase }

func (vfPageScene) render(r *sdl.Renderer, c *Config) bool {
	vfPageRender(r, c)
	return true
}

func (vfPageScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	if vfPageCardUp() {
		return vfCardKey(e)
	}
	if e != nil && e.Type == sdl.KEYDOWN && e.Keysym.Sym == sdl.K_TAB && !tbxSheetVisible(c) {
		// Tab and Shift+Tab are the keyboard's L1/R1.
		dir := 1
		if e.Keysym.Mod&sdl.KMOD_SHIFT != 0 {
			dir = -1
		}
		vfPageSwitchTab(vfPage.tab + dir)
		return true
	}
	return tbxDispatch(c, tbxKeyAction(e), vfPageAction)
}

func (vfPageScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	if vfPageCardUp() {
		return vfCardPad(e)
	}
	return tbxDispatch(c, tbxPadAction(e), vfPageAction)
}

func init() { registerScene(vfPageScene{sceneBase{name: verifiedSceneName}}) }

// vfPageCardUp reports whether the check card is open over this page.
func vfPageCardUp() bool { return vfCard.open && vfCard.host == vfCardHostPage }

// ── Rows ─────────────────────────────────────────────────────────────────

// vfPageTabOf is the tab that lists kind, or -1.
func vfPageTabOf(kind string) int {
	for i, k := range vfPageTabKinds {
		if k == kind {
			return i
		}
	}
	return -1
}

// vfPageRefresh builds the rows, the tab labels and the header from the
// store.
func vfPageRefresh() {
	pg := &vfPage
	pg.gen = vfGeneration.Load()
	pg.loaded = vfLoaded()
	pg.built = true
	model := vfModelShort(vfModel())
	for i := range pg.rows {
		pg.rows[i] = pg.rows[i][:0]
	}
	verified, partly, issues := 0, 0, 0
	pg.total = 0
	for _, r := range vfList("") {
		t := vfPageTabOf(r.Kind)
		if t < 0 {
			continue
		}
		v := vfVerdictFor(r)
		row := vfPageRow{
			navID:  "r:" + r.Kind + ":" + r.ID,
			kind:   r.Kind,
			id:     r.ID,
			title:  setOr(r.Title, r.ID),
			detail: v.Detail,
			pill:   "Not tested",
			tone:   tbxToneNeutral,
		}
		switch v.Level {
		case vfLevelVerified:
			row.pill, row.tone = "Verified", tbxToneGood
			verified++
		case vfLevelPartial:
			row.pill = strconv.Itoa(v.Passed) + " of 5 checks"
			partly++
		case vfLevelIssue:
			row.pill, row.tone = "Known issue", tbxToneWarn
			issues++
		}
		pg.rows[t] = append(pg.rows[t], row)
		pg.total++
	}
	for i, name := range vfPageTabNames {
		pg.tabLabels[i] = name
		if n := len(pg.rows[i]); n > 0 {
			pg.tabLabels[i] = name + " · " + strconv.Itoa(n)
		}
		pg.empty[i] = "No " + strings.ToLower(name) + " tested here yet"
		if pg.total == 0 {
			pg.empty[i] = "Nothing tested here yet"
		}
	}
	status := strconv.Itoa(verified) + " verified · " + strconv.Itoa(partly) + " partly · " +
		pluralizeCount(issues, "issue", "issues")
	color := ColorTextTertiary()
	switch {
	case !pg.loaded:
		status = "Reading…"
	case issues > 0:
		color = ColorWarning
	case verified > 0:
		color = ColorSuccess
	}
	pg.header = tbxHeader("Tested on this device", "What JukaHub saw work on "+model, "devices", status, color)
	pg.notice = ""
	if found := vfIssues(); len(found) > 0 {
		pg.notice = "Some results could not be read: " + found[0]
		if len(found) > 1 {
			pg.notice += " (and " + strconv.Itoa(len(found)-1) + " more)"
		}
	}
}

// vfPageRowByNav is the current tab's row with focus id navID, or nil.
func vfPageRowByNav(navID string) *vfPageRow {
	rows := vfPage.rows[vfPage.tab]
	for i := range rows {
		if rows[i].navID == navID {
			return &rows[i]
		}
	}
	return nil
}

// ── Input ────────────────────────────────────────────────────────────────

// vfPageSwitchTab shows tab t (wrapping round) with its button focused.
func vfPageSwitchTab(t int) {
	n := len(vfPageTabKinds)
	t = (t%n + n) % n
	if t == vfPage.tab {
		return
	}
	vfPage.tab = t
	vfPage.nav.focus = vfPageTabIDs[t]
	vfPage.nav.scroll = 0
	PlayNavSound()
}

// vfPageAction is the screen's input (the sheet already had its chance in
// tbxDispatch). Up at the top and B fall through: the top bar, the way back.
func vfPageAction(config *Config, act tbxAction) bool {
	nav := &vfPage.nav
	switch act {
	case tbxActConfirm:
		if it, ok := nav.current(); ok && !it.disabled {
			vfPageActivate(config, it.id)
		}
		return true
	case tbxActY:
		vfPageExport()
		return true
	case tbxActPageUp:
		vfPageSwitchTab(vfPage.tab - 1)
		return true
	case tbxActPageDown:
		vfPageSwitchTab(vfPage.tab + 1)
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(nav, act)
}

// vfPageActivate is A (or a click) on the control id.
func vfPageActivate(_ *Config, id string) {
	switch {
	case strings.HasPrefix(id, "tab:"):
		if t, err := strconv.Atoi(id[len("tab:"):]); err == nil {
			vfPageSwitchTab(t)
		}
	case id == vfPageExportID:
		vfPageExport()
	default:
		if row := vfPageRowByNav(id); row != nil {
			PlayActivateSound()
			vfCardOpen(row.kind, row.id, row.title, vfCardHostPage)
		}
	}
}

// vfPageExport writes this device's results for sharing, on a goroutine;
// the toast names the file.
func vfPageExport() {
	if !vfPageExporting.CompareAndSwap(false, true) {
		return
	}
	PlayActivateSound()
	go func() {
		path, _, err := vfExport()
		vfPageExporting.Store(false)
		tbxPost(func() {
			switch {
			case err != nil && path == "" && strings.Contains(err.Error(), "nothing has been tested"):
				ShowToast("Nothing to share yet — play something first", ToastKindInfo)
			case err != nil:
				ShowToast("Could not save the results: "+firstLine(err.Error()), ToastKindWarning)
			default:
				ShowToast("Saved for sharing: "+vfDirName+"/"+filepath.Base(path), ToastKindSuccess)
			}
		})
	}()
}

// verifiedPageClick routes a click or tap on the page (tbxSceneMouse, after
// the sheet): the card first, then the page's tabs, button and rows.
func verifiedPageClick(mx, my int32, config *Config) bool {
	if vfPageCardUp() {
		return vfCardMouse(mx, my)
	}
	it, ok := vfPage.nav.hit(mx, my)
	if !ok {
		return false
	}
	vfPage.nav.focus = it.id
	if !it.disabled {
		vfPageActivate(config, it.id)
	}
	return true
}

// verifiedPageNav is the focus list the mouse wheel scrolls: the card's while
// it is open (tbxSceneNav).
func verifiedPageNav() *tbxNav {
	if vfPageCardUp() {
		return &vfCard.nav
	}
	return &vfPage.nav
}

// verifiedPageHints is the footer (tbxFooterHints): the card's keys while it
// is open, else the page's for the focused control.
func verifiedPageHints() []FooterHint {
	if vfPageCardUp() {
		return vfCardHints()
	}
	hints := make([]FooterHint, 0, 5)
	hints = append(hints, FooterHint{Button: "L1/R1", Label: "Tabs"})
	exportFocused := false
	if it, ok := vfPage.nav.current(); ok {
		switch {
		case strings.HasPrefix(it.id, "tab:"):
		case it.id == vfPageExportID:
			exportFocused = true
			hints = append(hints, FooterHint{Button: "A", Label: "Export"})
		default:
			hints = append(hints, FooterHint{Button: "A", Label: "Details"})
		}
	}
	if !exportFocused {
		hints = append(hints, FooterHint{Button: "Y", Label: "Export"})
	}
	return append(hints, FooterHint{Button: "B", Label: "Back"})
}

// ── Drawing ──────────────────────────────────────────────────────────────

// vfPageScr moves a body rect (y=0 at the top of the content) onto the
// screen.
func vfPageScr(r sdl.Rect, top int32) sdl.Rect {
	r.Y += top
	return r
}

// vfPageRender draws the page, and the card over it when open.
func vfPageRender(renderer *sdl.Renderer, config *Config) {
	pg := &vfPage
	focusFirst := false
	if pg.visit.begin() {
		if vfPageCardUp() {
			vfCardClose(false)
		}
		vfEnsureLoaded()
		pg.built = false
		pg.nav.scroll = 0
		pg.nav.focus = ""
		focusFirst = true
	}
	if !pg.built || pg.gen != vfGeneration.Load() {
		wasLoaded := pg.loaded
		vfPageRefresh()
		// The first rows to arrive take the focus, as on a fresh visit.
		focusFirst = focusFirst || (!wasLoaded && pg.loaded)
	}
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, pg.header)
	body := frame.bodyRect()
	nav := &pg.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	top := body.Y - nav.scroll

	// Row 0: the tabs, with Export at the right (below them when it does
	// not fit).
	x := frame.x
	for i := range vfPageTabNames {
		label := pg.tabLabels[i]
		w := tbxButtonWidth(fonts.body, label, 110)
		rect := sdl.Rect{X: x, Y: 0, W: w, H: vfPageTabH}
		tbxDrawButton(renderer, config, fonts.body, vfPageScr(rect, top), label, i == pg.tab, nav.focused(vfPageTabIDs[i]), false)
		nav.add(vfPageTabIDs[i], rect, 0, false)
		x += w + 10
	}
	y := vfPageTabH + 14
	nextRow := 1
	busy := vfPageExporting.Load()
	exportLabel := "Export results"
	if busy {
		exportLabel = "Exporting…"
	}
	ew := tbxButtonWidth(fonts.body, exportLabel, 150)
	export := sdl.Rect{X: frame.x + frame.w - ew, Y: 0, W: ew, H: vfPageTabH}
	exportRow := 0
	if export.X < x+10 {
		export = sdl.Rect{X: frame.x, Y: y, W: ew, H: vfPageTabH}
		exportRow, nextRow = 1, 2
		y += vfPageTabH + 14
	}
	tbxDrawButton(renderer, config, fonts.body, vfPageScr(export, top), exportLabel, false, nav.focused(vfPageExportID), busy)
	nav.add(vfPageExportID, export, exportRow, busy)

	// A problem reading the files, then where results come from.
	if pg.notice != "" {
		for i, line := range pg.wrapNote.get(fonts.small, pg.notice, frame.w) {
			if i == 2 {
				break
			}
			renderText(renderer, config, fonts.small, line, ColorWarning, frame.x, top+y)
			y += fonts.smallH + 4
		}
		y += 6
	}
	for _, line := range pg.wrapHow.get(fonts.small, vfPageHowText, frame.w) {
		renderText(renderer, config, fonts.small, line, ColorTextTertiary(), frame.x, top+y)
		y += fonts.smallH + 4
	}
	y += 12
	nav.extend(y)

	rows := pg.rows[pg.tab]
	switch {
	case !pg.loaded || len(rows) == 0:
		h := max(body.Y+body.H-(top+y), 160)
		title, text := pg.empty[pg.tab], vfPageEmptyBody
		if !pg.loaded {
			title, text = "Reading the results…", ""
		}
		tbxDrawEmpty(renderer, config, sdl.Rect{X: frame.x, Y: top + y, W: frame.w, H: h}, "devices", title, text)
		nav.extend(y + 160)
	default:
		rowH := tbxRowHeight(fonts)
		icon := vfPageTabIcons[pg.tab]
		for i := range rows {
			r := &rows[i]
			rect := sdl.Rect{X: frame.x, Y: y, W: frame.w, H: rowH}
			nav.add(r.navID, rect, nextRow+i, false)
			if screen := vfPageScr(rect, top); screen.Y+screen.H >= body.Y && screen.Y <= body.Y+body.H {
				spec := tbxRowSpec{icon: icon, title: r.title, subtitle: r.detail, pill: r.pill, tone: r.tone}
				tbxDrawRow(renderer, config, fonts, screen, spec, nav.focused(r.navID))
			}
			y += rowH + 8
		}
	}
	if nav.index() < 0 {
		// A fresh visit, or a row that went away (forgotten on its card):
		// the shown tab takes the focus, not the first one.
		nav.focus = vfPageTabIDs[pg.tab]
	}
	nav.end()
	if focusFirst && pg.loaded && len(rows) > 0 {
		nav.setFocus(rows[0].navID)
	}
	tbxUnclip(renderer)
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
	if vfPageCardUp() {
		vfCardRender(renderer, config)
	}
}
