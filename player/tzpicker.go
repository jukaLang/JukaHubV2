package main

// tzpicker.go — the on-device time-zone picker (Settings → Date & time → Time
// zone, and the first-run tour's "Set your time zone" on the handheld).
//
// It is a sheet over the Settings hub, drawn like the hub's list sheet
// (setsheet.go): every IANA zone from tzzones.go grouped under its region,
// the zone in force marked (also when it goes by an older or alias name), its
// current UTC offset on the right (worked out off the render thread when the
// picker opens). Typing narrows the list — by city, country, or an old name
// such as Calcutta or Kiev: letters on a keyboard go straight into the search
// line, and on a pad Y opens the on-screen keyboard aimed at it (the list
// re-filters on every key, behind the keyboard). The first row asks the
// network where the device is and uses that zone ("Set automatically", the
// weather's geolocation services); its answer is applied on the main thread,
// and only if the user has not chosen, closed or reopened the picker since.
//
// Keys: Up/Down move, Left/Right (PgUp/PgDn) page, L1/R1 (Tab) jump between
// regions, A/Enter sets the zone, B/Esc clears the search first and then
// closes.

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// tzPickerVKTarget aims the on-screen keyboard at the picker's search line. A
// target of its own, like homeSearchVKTarget, so the keyboard's switches carry
// one case per hook (vkCurrentText, vkSetText, vkTitle, vkPlaceholder,
// vkActionLabel, vkSubmit, vkLiveFilterTarget).
const tzPickerVKTarget vkTargetKind = 65

// tzPickerMaxQuery bounds the search line.
const tzPickerMaxQuery = 48

// tzRowKind is what a picker row is.
type tzRowKind uint8

const (
	tzRowAuto   tzRowKind = iota // "Set automatically"
	tzRowHeader                  // a region's name; not selectable
	tzRowZone                    // one zone
)

// tzPickerRow is one line of the filtered list.
type tzPickerRow struct {
	kind tzRowKind
	zone int    // index into tzZones (zone rows)
	text string // the region, upper-case (header rows)
}

// tzPickerState is the open picker.
type tzPickerState struct {
	open bool
	// title is the header's "Now Europe/Berlin · UTC+1", read when the picker
	// opens rather than formatted every frame.
	title string
	// pending keeps the picker through the Settings visit a deep link starts
	// (dateTimeOpenSettings): the hub closes it on any other new visit.
	pending bool
	query   string
	// empty is the line shown when nothing matches, built when the query
	// changes rather than every frame.
	empty  string
	rows   []tzPickerRow
	cursor int
	scroll int32
	// currentZone is the zone in force as an index into tzZones (-1 when it
	// is none of them, such as Etc/GMT+5).
	currentZone int
	// The last frame's geometry, for the mouse and the page size.
	card  sdl.Rect
	field sdl.Rect
	view  sdl.Rect
	rowH  int32
	rects []sdl.Rect
}

var tzPicker tzPickerState

// tzPickerGen counts openings, closings and choices (main thread). "Set
// automatically" remembers the count it was asked at (tzAutoWant) and its
// answer applies only while nothing has happened since: a lookup that takes
// twenty seconds must not overwrite a zone the user picked meanwhile.
var (
	tzPickerGen uint64
	tzAutoWant  uint64
)

// tzAutoBusy is set while "Set automatically" is asking the network.
var tzAutoBusy atomic.Bool

// tzOffsetLabels holds every zone's "UTC+1" label for the current opening,
// indexed like tzZones; nil until tzOffsetsCompute has run. Loading a zone is
// a search of the zone data, so it happens on a goroutine, not while drawing.
var (
	tzOffsetLabels atomic.Pointer[[]string]
	tzOffsetsBusy  atomic.Bool
)

// tzOffsetsCompute works out every zone's current offset label (off the
// render thread).
func tzOffsetsCompute() {
	now := time.Now()
	labels := make([]string, len(tzZones))
	for i := range tzZones {
		if loc, err := time.LoadLocation(tzZones[i].Name); err == nil {
			labels[i] = utcOffsetLabel(now.In(loc))
		}
	}
	// Not busy before the labels are out: an opening between the two would
	// wipe them, find this run still busy, start none, and show no offsets.
	// This way it either starts a new run or its wipe comes before this
	// store; either way labels arrive.
	tzOffsetsBusy.Store(false)
	tzOffsetLabels.Store(&labels)
}

// tzPickerBump marks a change the pending automatic lookup must not undo.
func tzPickerBump() { tzPickerGen++ }

// ── The zone table, for search ────────────────────────────────────────────

// tzSearchKeys are the zones' lower-case search texts ("europe berlin germany,
// denmark…") and tzCities the names the rows show; both are built once, on
// first use, so drawing a row allocates nothing.
var (
	tzSearchKeys []string
	tzCities     []string
)

// tzNameSpaces reads a zone name as words: "America/Buenos_Aires" →
// "America Buenos Aires".
var tzNameSpaces = strings.NewReplacer("_", " ", "/", " ")

// tzFold drops the few accents in the country names (Åland, Côte d’Ivoire,
// Curaçao, Réunion) after lower-casing, so a keyboard without them finds them.
var tzFold = strings.NewReplacer("å", "a", "ô", "o", "ç", "c", "é", "e", "’", "'")

// tzSearchText is how a zone name, a country list or a query is searched:
// lower case, words, no accents.
func tzSearchText(s string) string {
	return tzFold.Replace(strings.ToLower(tzNameSpaces.Replace(s)))
}

// tzEnsureSearchKeys builds the search texts: each zone's name and countries,
// plus the old and alias names that lead to it (tzAliases: "asia calcutta"
// finds Asia/Kolkata).
func tzEnsureSearchKeys() {
	if tzSearchKeys != nil {
		return
	}
	keys := make([]string, len(tzZones))
	cities := make([]string, len(tzZones))
	for i, z := range tzZones {
		keys[i] = tzSearchText(z.Name + " " + z.Countries)
		cities[i] = tzCity(z.Name)
	}
	for _, a := range tzAliases {
		if zi := tzZoneIndex(a.Zone); zi >= 0 {
			keys[zi] += " " + tzSearchText(a.Alias)
		}
	}
	tzSearchKeys, tzCities = keys, cities
}

// tzZoneLookup finds the row for a zone name, through the alias table when the
// name is an old or alternative one (Asia/Calcutta, Etc/UTC); -1 when it is
// neither.
func tzZoneLookup(name string) int {
	if zi := tzZoneIndex(name); zi >= 0 {
		return zi
	}
	lo, hi := 0, len(tzAliases)
	for lo < hi {
		mid := (lo + hi) / 2
		if tzAliases[mid].Alias < name {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(tzAliases) && tzAliases[lo].Alias == name {
		return tzZoneIndex(tzAliases[lo].Zone)
	}
	return -1
}

// tzRegion is a zone's region: the part before the first slash ("Europe"), or
// the whole name for "UTC".
func tzRegion(name string) string {
	if i := strings.IndexByte(name, '/'); i > 0 {
		return name[:i]
	}
	return name
}

// tzCity is how a zone reads in the list: the place after the region with
// spaces for underscores ("Buenos Aires · Argentina" for
// America/Argentina/Buenos_Aires), the name itself for "UTC".
func tzCity(name string) string {
	i := strings.IndexByte(name, '/')
	if i < 0 {
		return name
	}
	parts := strings.Split(name[i+1:], "/")
	for j := range parts {
		parts[j] = strings.ReplaceAll(parts[j], "_", " ")
	}
	if len(parts) == 1 {
		return parts[0]
	}
	// The place comes first; the sub-region ("Argentina", "Indiana") after it.
	last := parts[len(parts)-1]
	return last + " · " + strings.Join(parts[:len(parts)-1], " · ")
}

// tzMatches lists the zones whose search text holds every word of query, in
// table order. An empty query matches every zone.
func tzMatches(query string) []int {
	tzEnsureSearchKeys()
	words := strings.Fields(tzSearchText(query))
	out := make([]int, 0, len(tzZones))
	for i, key := range tzSearchKeys {
		ok := true
		for _, w := range words {
			if !strings.Contains(key, w) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, i)
		}
	}
	return out
}

// tzBuildRows lays the matches out under their regions, with the automatic
// row first while nothing is typed.
func tzBuildRows(query string, auto bool) []tzPickerRow {
	matches := tzMatches(query)
	rows := make([]tzPickerRow, 0, len(matches)+12)
	if auto && strings.TrimSpace(query) == "" {
		rows = append(rows, tzPickerRow{kind: tzRowAuto})
	}
	region := ""
	for _, zi := range matches {
		if r := tzRegion(tzZones[zi].Name); r != region {
			region = r
			rows = append(rows, tzPickerRow{kind: tzRowHeader, text: strings.ToUpper(r)})
		}
		rows = append(rows, tzPickerRow{kind: tzRowZone, zone: zi})
	}
	return rows
}

// tzZoneIndex finds a zone in the table (-1 when it is not there).
func tzZoneIndex(name string) int {
	lo, hi := 0, len(tzZones)
	for lo < hi {
		mid := (lo + hi) / 2
		if tzZones[mid].Name < name {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(tzZones) && tzZones[lo].Name == name {
		return lo
	}
	return -1
}

// tzRowSelectable reports whether row i can hold the cursor.
func tzRowSelectable(rows []tzPickerRow, i int) bool {
	return i >= 0 && i < len(rows) && rows[i].kind != tzRowHeader
}

// tzStep is the next selectable row from i in dir (i itself at the ends).
func tzStep(rows []tzPickerRow, i, dir int) int {
	for j := i + dir; j >= 0 && j < len(rows); j += dir {
		if tzRowSelectable(rows, j) {
			return j
		}
	}
	return i
}

// tzRegionJump is the first zone of the next (dir > 0) or the current or
// previous (dir < 0) region from row i.
func tzRegionJump(rows []tzPickerRow, i, dir int) int {
	if dir > 0 {
		for j := i + 1; j < len(rows); j++ {
			if rows[j].kind == tzRowHeader {
				return tzStep(rows, j, 1)
			}
		}
		return i
	}
	// Back to this region's header, then to the one before it.
	headers := 0
	for j := i - 1; j >= 0; j-- {
		if rows[j].kind != tzRowHeader {
			continue
		}
		headers++
		// The first header above is this region's own; when the cursor is
		// already on its first zone, go one further.
		if headers == 1 && tzStep(rows, j, 1) != i {
			return tzStep(rows, j, 1)
		}
		if headers == 2 {
			return tzStep(rows, j, 1)
		}
	}
	return tzStep(rows, -1, 1)
}

// ── Opening, choosing, closing ────────────────────────────────────────────

// tzPickerOpen shows the picker with the zone in force under the cursor.
func tzPickerOpen(*Config) {
	tzPickerBump()
	tzPicker = tzPickerState{
		open:  true,
		title: "Now " + dateTimeZoneLabel(time.Now()),
		// An alias such as Europe/Kiev (an old TZ, or a geolocation answer)
		// marks the zone it stands for.
		currentZone: tzZoneLookup(dateTimeZoneName()),
		rects:       tzPicker.rects[:0],
	}
	// This opening's offsets, off the render thread; the rows show none for
	// the few milliseconds that takes.
	tzOffsetLabels.Store(nil)
	if tzOffsetsBusy.CompareAndSwap(false, true) {
		go tzOffsetsCompute()
	}
	tzPickerRebuild()
	if zi := tzPicker.currentZone; zi >= 0 {
		for i, r := range tzPicker.rows {
			if r.kind == tzRowZone && r.zone == zi {
				tzPicker.cursor = i
				break
			}
		}
	}
	PlayActivateSound()
}

// tzPickerRebuild re-filters after the search line changed; the cursor goes to
// the first selectable row.
func tzPickerRebuild() {
	tzPicker.rows = tzBuildRows(tzPicker.query, true)
	tzPicker.cursor = tzStep(tzPicker.rows, -1, 1)
	tzPicker.scroll = 0
	tzPicker.empty = ""
	if len(tzPicker.rows) == 0 {
		tzPicker.empty = "No zone matches \"" + tzPicker.query + "\""
	}
}

// tzPickerSetQuery replaces the search line (the on-screen keyboard's hook),
// cut to tzPickerMaxQuery characters, never inside one.
func tzPickerSetQuery(text string) {
	text = truncateRunesSuffix(text, tzPickerMaxQuery, "")
	if text == tzPicker.query {
		return
	}
	tzPicker.query = text
	tzPickerRebuild()
}

// tzPickerDismiss closes the picker without a sound (after a choice, or when
// the hub starts another visit).
func tzPickerDismiss() {
	tzPickerBump()
	tzPicker.open = false
	tzPicker.pending = false
}

// tzPickerClose dismisses the picker without choosing.
func tzPickerClose() {
	if !tzPicker.open {
		return
	}
	tzPickerDismiss()
	PlayBackSound()
}

// tzPickerHubEnter is the hub's new-visit hook (setHubEnter): a picker left
// open on another visit closes, the one a deep link opened for this visit
// stays.
func tzPickerHubEnter() {
	if tzPicker.pending {
		tzPicker.pending = false
		return
	}
	if tzPicker.open {
		tzPickerDismiss()
	}
}

// tzPickerVisible reports whether the picker is on screen: open, and the
// Settings hub is the scene drawing it. The first-run tour steps aside for it.
func tzPickerVisible() bool {
	return tzPicker.open && appConfig != nil && setIsHubScene(setHubSceneName(appConfig))
}

// tzPickerChoose acts on row i.
func tzPickerChoose(i int) {
	if !tzRowSelectable(tzPicker.rows, i) {
		return
	}
	row := tzPicker.rows[i]
	if row.kind == tzRowAuto {
		tzAutoStart()
		return
	}
	// A choice: a lookup still on its way must not undo it.
	tzPickerBump()
	name := tzZones[row.zone].Name
	if err := dateTimeApplyZone(name); err != nil {
		PlayBackSound()
		ShowToast("Time zone: "+err.Error(), ToastKindWarning)
		return
	}
	tzPickerDismiss()
	setHub.dirty = true
	PlayActivateSound()
	ShowToast("Time zone: "+name+" · "+utcOffsetLabel(time.Now()), ToastKindSuccess)
}

// tzAutoStart looks the zone up from the connection's location, off the
// render thread; tzAutoApply takes the answer on the main thread. Pressed
// again while a lookup runs, it adopts that lookup instead of starting one.
func tzAutoStart() {
	if !IsOnline() {
		ShowToast("Connect to Wi-Fi to find your time zone", ToastKindInfo)
		return
	}
	tzPickerBump()
	tzAutoWant = tzPickerGen
	PlayActivateSound()
	ShowToast("Finding your time zone…", ToastKindInfo)
	if !tzAutoBusy.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer tzAutoBusy.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		// The weather's providers, in its order, until one names a zone.
		client := &http.Client{Timeout: 6 * time.Second}
		name := ""
		for _, provider := range []func(context.Context, *http.Client) (geoLocation, bool){
			lookupIPWhoIs, lookupIPAPICo, lookupIPInfo,
		} {
			if ctx.Err() != nil {
				break
			}
			if g, ok := provider(ctx, client); ok && tzValidName(strings.TrimSpace(g.TimeZone)) {
				name = strings.TrimSpace(g.TimeZone)
				break
			}
		}
		tbxPost(func() { tzAutoApply(name) })
	}()
}

// tzAutoApply takes "Set automatically"'s answer (main thread, through
// tbxPost): it sets the zone and closes the picker, unless the user chose,
// closed or reopened it since the press — then the answer is dropped.
func tzAutoApply(name string) {
	if tzAutoWant != tzPickerGen {
		logDebug("clock", "automatic time zone %q dropped: the picker changed meanwhile", name)
		return
	}
	if name == "" {
		ShowToast("Your time zone could not be found; choose it from the list", ToastKindWarning)
		return
	}
	if err := dateTimeApplyZone(name); err != nil {
		ShowToast("Time zone: "+err.Error(), ToastKindWarning)
		return
	}
	if tzPicker.open {
		tzPickerDismiss()
	} else {
		tzPickerBump()
	}
	setHub.dirty = true
	ShowToast("Time zone set automatically: "+name+" · "+utcOffsetLabel(time.Now()), ToastKindSuccess)
}

// tzPickerOpenKeyboard aims the on-screen keyboard at the search line, the
// way the other live filters open it (homeSearchOpenKeyboard).
func tzPickerOpenKeyboard() {
	chatInputActive = false
	unitInputActive = false
	vkTarget = tzPickerVKTarget
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// ── Input ─────────────────────────────────────────────────────────────────

// tzPickerMove moves the cursor by delta selectable rows (a page when large).
func tzPickerMove(delta int) {
	if len(tzPicker.rows) == 0 || delta == 0 {
		return
	}
	dir := 1
	if delta < 0 {
		dir, delta = -1, -delta
	}
	next := tzPicker.cursor
	for k := 0; k < delta; k++ {
		next = tzStep(tzPicker.rows, next, dir)
	}
	if next != tzPicker.cursor {
		tzPicker.cursor = next
		PlayNavSound()
	}
}

// tzPickerPage is how many rows a page jump moves: a view's worth, from the
// last frame's row height (the rects of rows scrolled out are zeroed).
func tzPickerPage() int {
	if tzPicker.view.H > 0 && tzPicker.rowH > 0 {
		if n := int(tzPicker.view.H / (tzPicker.rowH + 6)); n > 1 {
			return n - 1
		}
	}
	return 5
}

// tzPickerBack is B/Esc: clear the search first, close when it is empty.
func tzPickerBack() {
	if tzPicker.query != "" {
		tzPicker.query = ""
		tzPickerRebuild()
		PlayBackSound()
		return
	}
	tzPickerClose()
}

// tzPickerTypeKey turns a printable key into search text. Zone names and the
// country list are ASCII apart from a few accented country names, so the key
// itself is enough; a keyboard user never needs the on-screen one.
func tzPickerTypeKey(e *sdl.KeyboardEvent) bool {
	if e.Keysym.Mod&(sdl.KMOD_CTRL|sdl.KMOD_ALT|sdl.KMOD_GUI) != 0 {
		return false
	}
	sym := e.Keysym.Sym
	var c byte
	switch {
	case sym >= sdl.K_a && sym <= sdl.K_z:
		c = byte(sym)
	case sym >= sdl.K_0 && sym <= sdl.K_9:
		c = byte(sym)
	case sym == sdl.K_SPACE:
		c = ' '
	case sym == sdl.K_MINUS:
		c = '-'
	case sym == sdl.K_SLASH:
		c = '/'
	default:
		return false
	}
	if tzPicker.query == "" && c == ' ' {
		return true
	}
	tzPickerSetQuery(tzPicker.query + string(rune(c)))
	return true
}

// tzPickerKey handles a key while the picker is open.
func tzPickerKey(e *sdl.KeyboardEvent) {
	if e == nil || e.Type != sdl.KEYDOWN {
		return
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		tzPickerMove(-1)
	case sdl.K_DOWN:
		tzPickerMove(1)
	case sdl.K_PAGEUP:
		tzPickerMove(-tzPickerPage())
	case sdl.K_PAGEDOWN:
		tzPickerMove(tzPickerPage())
	case sdl.K_HOME:
		tzPickerMove(-len(tzPicker.rows))
	case sdl.K_END:
		tzPickerMove(len(tzPicker.rows))
	case sdl.K_TAB:
		dir := 1
		if e.Keysym.Mod&sdl.KMOD_SHIFT != 0 {
			dir = -1
		}
		tzPickerJumpRegion(dir)
	case sdl.K_RETURN, sdl.K_KP_ENTER:
		tzPickerChoose(tzPicker.cursor)
	case sdl.K_ESCAPE:
		tzPickerBack()
	case sdl.K_BACKSPACE:
		if q := tzPicker.query; q != "" {
			tzPickerSetQuery(trimLastRune(q))
		}
	default:
		tzPickerTypeKey(e)
	}
}

// tzPickerJumpRegion moves to the next or previous region.
func tzPickerJumpRegion(dir int) {
	if next := tzRegionJump(tzPicker.rows, tzPicker.cursor, dir); next != tzPicker.cursor {
		tzPicker.cursor = next
		PlayNavSound()
	}
}

// tzPickerPad handles a controller button while the picker is open.
func tzPickerPad(button uint8) {
	switch button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		tzPickerMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		tzPickerMove(1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		tzPickerMove(-tzPickerPage())
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		tzPickerMove(tzPickerPage())
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		tzPickerJumpRegion(-1)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		tzPickerJumpRegion(1)
	case sdl.CONTROLLER_BUTTON_A:
		tzPickerChoose(tzPicker.cursor)
	case sdl.CONTROLLER_BUTTON_Y:
		PlayActivateSound()
		tzPickerOpenKeyboard()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		tzPickerBack()
	}
}

// tzPickerMouse handles a click: a row chooses, the search line opens the
// on-screen keyboard, outside the card closes.
func tzPickerMouse(mx, my int32) {
	if tzPicker.field.W > 0 && pointInRect(mx, my, tzPicker.field) {
		PlayActivateSound()
		tzPickerOpenKeyboard()
		return
	}
	for i, r := range tzPicker.rects {
		if r.W > 0 && r.H > 0 && pointInRect(mx, my, r) && tzRowSelectable(tzPicker.rows, i) {
			tzPicker.cursor = i
			tzPickerChoose(i)
			return
		}
	}
	if tzPicker.card.W > 0 && !pointInRect(mx, my, tzPicker.card) {
		tzPickerClose()
	}
}

// tzPickerWheel moves the cursor three rows per notch.
func tzPickerWheel(dy int32) {
	switch {
	case dy > 0:
		tzPickerMove(-3)
	case dy < 0:
		tzPickerMove(3)
	}
}

// tzPickerHints is the footer while the picker is open.
func tzPickerHints() []FooterHint {
	back := "Close"
	if tzPicker.query != "" {
		back = "Clear search"
	}
	search := FooterHint{Button: "Y", Label: "Search"}
	if hintsUseKeyboard() {
		search = FooterHint{Button: "A-Z", Label: "Search"}
	}
	return []FooterHint{{Button: "D-Pad", Label: "Choose"}, {Button: "A", Label: "Set"}, search,
		{Button: "L1/R1 / Tab", Label: "Region"}, {Button: "B", Label: back}}
}

// ── Drawing ───────────────────────────────────────────────────────────────

// tzPickerDraw paints the picker over the Settings hub.
func tzPickerDraw(renderer *sdl.Renderer, config *Config) {
	if renderer == nil || !tzPicker.open {
		return
	}
	label := getFontSized(config, "medium", 19)
	small := getFontSized(config, "small", 15)
	if label == nil || small == nil {
		return
	}
	drawOverlayScrim(renderer)

	w := int32(680)
	if w > screenWidth-48 {
		w = screenWidth - 48
	}
	top := headerBarHeight() + 8
	h := screenHeight - top - bottomBarHeight("Settings") - 16
	if h > 620 {
		h = 620
	}
	card := sdl.Rect{X: (screenWidth - w) / 2, Y: top + (screenHeight-top-bottomBarHeight("Settings")-16-h)/2, W: w, H: h}
	tzPicker.card = card
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	header := overlayPageHeader(card, "Time zone", tzPicker.title, "globe")
	bodyTop := UIPageHeaderDraw(renderer, config, header) + 10

	// The search line.
	lh, sh := int32(label.Height()), int32(small.Height())
	field := sdl.Rect{X: card.X + OverlayPadX, Y: bodyTop, W: card.W - 2*OverlayPadX, H: lh + 18}
	tzPicker.field = field
	drawGlassSurface(renderer, field, glassRowRadius, glassState{Focused: tzPicker.query != "", NoLift: true})
	iconBox := sh + 4
	DrawHomeIcon(renderer, "search", sdl.Rect{X: field.X + 14, Y: field.Y + (field.H-iconBox)/2, W: iconBox, H: iconBox}, false)
	textX := field.X + 14 + iconBox + 10
	if tzPicker.query != "" {
		drawText(renderer, label, ellipsize(label, tzPicker.query, field.X+field.W-14-textX), textX, field.Y+(field.H-lh)/2, ColorTextPrimary(), textAlignLeft)
	} else {
		prompt := "Type a city or country"
		if !hintsUseKeyboard() {
			prompt = hintSentence("Press %s and type a city or country", "Y", "Search")
		}
		drawText(renderer, small, ellipsize(small, prompt, field.X+field.W-14-textX), textX, field.Y+(field.H-sh)/2, ColorTextMuted(), textAlignLeft)
	}

	listTop := field.Y + field.H + 10
	view := sdl.Rect{X: card.X + OverlayPadX, Y: listTop, W: card.W - 2*OverlayPadX, H: card.Y + card.H - OverlayPadX - listTop}
	tzPicker.view = view
	rows := tzPicker.rows
	if cap(tzPicker.rects) < len(rows) {
		tzPicker.rects = make([]sdl.Rect, len(rows))
	}
	tzPicker.rects = tzPicker.rects[:len(rows)]
	if view.H <= 0 {
		return
	}
	if len(rows) == 0 {
		drawText(renderer, small, tzPicker.empty, view.X+4, view.Y+8, ColorTextMuted(), textAlignLeft)
		return
	}

	rowH := lh + sh + 16
	if rowH < 52 {
		rowH = 52
	}
	tzPicker.rowH = rowH
	headH := sh + 14
	const gap = int32(6)
	// Rows in content space, so the scroll can keep the cursor in view.
	y := int32(0)
	curTop, curBottom := int32(0), int32(0)
	for i, r := range rows {
		hgt := rowH
		if r.kind == tzRowHeader {
			hgt = headH
		}
		tzPicker.rects[i] = sdl.Rect{X: view.X, Y: y, W: view.W, H: hgt}
		if i == tzPicker.cursor {
			curTop, curBottom = y, y+hgt
			// Keep the region's name in view above its first zone.
			if i > 0 && rows[i-1].kind == tzRowHeader {
				curTop -= headH + gap
			}
		}
		y += hgt + gap
	}
	content := y - gap
	if curTop < tzPicker.scroll {
		tzPicker.scroll = curTop
	}
	if curBottom > tzPicker.scroll+view.H {
		tzPicker.scroll = curBottom - view.H
	}
	if maxScroll := content - view.H; tzPicker.scroll > maxScroll {
		tzPicker.scroll = maxScroll
	}
	if tzPicker.scroll < 0 {
		tzPicker.scroll = 0
	}

	check := sh + 6
	// nil until this opening's offsets are worked out (tzOffsetsCompute).
	var offsets []string
	if p := tzOffsetLabels.Load(); p != nil {
		offsets = *p
	}
	clip := view
	_ = renderer.SetClipRect(&clip)
	for i, row := range rows {
		r := tzPicker.rects[i]
		r.Y += view.Y - tzPicker.scroll
		if r.Y+r.H < view.Y || r.Y > view.Y+view.H {
			tzPicker.rects[i] = sdl.Rect{}
			continue
		}
		tzPicker.rects[i] = r
		if row.kind == tzRowHeader {
			drawText(renderer, small, row.text, r.X+6, r.Y+(r.H-sh)/2+2, glassAccent(), textAlignLeft)
			continue
		}
		hovered := pointInRect(mouseX, mouseY, r) && i != tzPicker.cursor
		UIListRow(renderer, r, glassRowRadius, i == tzPicker.cursor, hovered)
		x := r.X + 16
		right := r.X + r.W - 16
		title, note := "", ""
		if row.kind == tzRowAuto {
			title = "Set automatically"
			note = "Uses your connection's location (the weather's lookup)"
			if tzAutoBusy.Load() {
				note = "Looking it up…"
			}
		} else {
			title, note = tzCities[row.zone], tzZones[row.zone].Countries
			if row.zone == tzPicker.currentZone {
				box := sdl.Rect{X: right - check, Y: r.Y + (r.H-check)/2, W: check, H: check}
				DrawHomeIcon(renderer, "check", box, true)
				right = box.X - 10
			}
			if row.zone < len(offsets) && offsets[row.zone] != "" {
				off := offsets[row.zone]
				ow, oh := measureText(small, off)
				drawText(renderer, small, off, right-ow, r.Y+(r.H-oh)/2, ColorTextSecondary(), textAlignLeft)
				right -= ow + 12
			}
		}
		avail := right - x
		ty := r.Y + (r.H-lh-sh-2)/2
		drawText(renderer, label, ellipsize(label, title, avail), x, ty, ColorTextPrimary(), textAlignLeft)
		drawText(renderer, small, ellipsize(small, note, avail), x, ty+lh+2, ColorTextSecondary(), textAlignLeft)
	}
	_ = renderer.SetClipRect(nil)
	if content > view.H && view.H > 0 {
		thumb := float64(view.H) / float64(content)
		frac := float64(tzPicker.scroll) / float64(content-view.H)
		drawScrollbar(renderer, view.X+view.W+6, view.Y, 3, view.H, thumb, frac)
	}
}
