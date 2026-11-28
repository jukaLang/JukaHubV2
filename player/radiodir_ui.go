package main

// radiodir_ui.go — the Radio directory screen and the Music screen's Radio
// shelf.
//
// The Music screen's shelves (Albums, Artists, Audiobooks, History) gain a
// fifth, Radio: "Radio directory" on top, then the user's stations (the
// IPStream library's radio and music channels, radiodir.go). A plays a station
// in the music player with the others after it for Next/Previous.
//
// The directory is a page of its own in the tool screens' frame (tbxui.go),
// with four tabs — Top (most voted), Country (the device's country first,
// A on the country row picks another), Genre (a list of genres, then that
// genre's stations) and Search (Y or A on the search row types a name). Left
// and Right or L1 and R1 switch tabs, A plays, X keeps the station in your
// stations (or takes it out), Y searches or reloads, B steps back.
//
// While something plays, both screens show it on a Now playing row on top: A
// stops it (pauses a song) or tunes in again, X opens the player. A on the
// station that is on the air does the same Play / Stop in place. A tester
// could not stop the radio from here (2026-11-26; music_transport.go).

import (
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// radioDirSceneName is the directory's scene, added in memory when opened.
const radioDirSceneName = "RadioDirectory"

// radioDirNowID is the directory's Now playing row, above every tab's list.
const radioDirNowID = "np"

// musicShelfTabRadio is the Music screen's Radio shelf (musichistory.go
// counts the shelves).
const musicShelfTabRadio = 4

// radioDirTabNames are the tabs, in order.
var radioDirTabNames = [...]string{"Top", "Country", "Genre", "Search"}

const (
	radioDirTabTop = iota
	radioDirTabCountry
	radioDirTabGenre
	radioDirTabSearch
)

// radioDirGenres are the Genre tab's tags: Radio Browser's most used ones.
var radioDirGenres = []string{
	"pop", "rock", "jazz", "classical", "electronic", "dance", "hip hop", "chillout",
	"ambient", "lounge", "lofi", "news", "talk", "sports", "country", "metal",
	"oldies", "80s", "90s", "reggae", "blues", "soul", "funk", "latin",
	"folk", "indie", "house", "techno", "trance", "rnb", "kids", "christmas",
}

// radioDirList is one list on the screen.
type radioDirList struct {
	stations []radioStation
	loading  bool
	fresh    bool   // fetched this visit (else the saved copy, or nothing)
	err      string // why the last fetch failed
}

var radioDirUI struct {
	tab         int
	country     string // the Country tab's code
	countryPick bool   // the country list is open
	countries   []radioCountry
	countriesAt bool // a country list fetch ran this session
	genre       string
	query       string
	lists       map[string]*radioDirList
	saved       map[string]bool
	nav         tbxNav
	visit       tbxVisit
}

// radioDirOpen opens the directory (the Radio shelf's first row). Main thread.
func radioDirOpen(config *Config) {
	if config == nil {
		config = appConfig
	}
	if config == nil {
		return
	}
	if sceneIndexExact(config, radioDirSceneName) < 0 {
		config.Scenes = append(config.Scenes, SceneConfig{Name: radioDirSceneName, Description: "Radio directory"})
	}
	if !tbxOpenScene(config, radioDirSceneName) {
		ShowToast("The radio directory is not available in this configuration", ToastKindWarning)
	}
}

type radioDirScene struct{ sceneBase }

func (radioDirScene) render(r *sdl.Renderer, c *Config) bool {
	radioDirRender(r, c)
	return true
}

func (radioDirScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), radioDirAction)
}

func (radioDirScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), radioDirAction)
}

func init() { registerScene(radioDirScene{sceneBase{name: radioDirSceneName}}) }

// radioDirOnVisit sets the screen up when it is reached: the device's country
// for the Country tab, the user's stations, and the open tab's list.
func radioDirOnVisit() {
	if radioDirUI.lists == nil {
		radioDirUI.lists = map[string]*radioDirList{}
	}
	if radioDirUI.country == "" {
		radioDirUI.country = radioDirHomeCountry()
	}
	radioDirUI.saved = radioDirSavedSet()
	radioDirUI.nav.scroll = 0
	radioDirLoadTab(false)
}

// radioDirHomeCountry is the device's country: the one the weather's
// location found, else the Wi-Fi regulatory country, else "" (the Country tab
// then opens on the country list).
func radioDirHomeCountry() string {
	for _, cc := range []string{weatherCountryCode(), wifiSupplicantCountry()} {
		cc = strings.ToUpper(strings.TrimSpace(cc))
		if radioDirCountryCodeValid(cc) && cc != "00" {
			return cc
		}
	}
	return ""
}

// radioDirKey is the list the open tab shows, "" when it shows a picker (the
// countries, the genres) or nothing yet (an empty search).
func radioDirKey() string {
	switch radioDirUI.tab {
	case radioDirTabTop:
		return "top"
	case radioDirTabCountry:
		if radioDirUI.countryPick || radioDirUI.country == "" {
			return ""
		}
		return "cc:" + radioDirUI.country
	case radioDirTabGenre:
		if radioDirUI.genre == "" {
			return ""
		}
		return "tag:" + radioDirUI.genre
	case radioDirTabSearch:
		if radioDirUI.query == "" {
			return ""
		}
		return "q:" + strings.ToLower(radioDirUI.query)
	}
	return ""
}

// radioDirLoadTab loads what the open tab needs; force fetches again.
func radioDirLoadTab(force bool) {
	if radioDirUI.tab == radioDirTabCountry && (radioDirUI.countryPick || radioDirUI.country == "") {
		radioDirLoadCountries(force)
		return
	}
	if key := radioDirKey(); key != "" {
		radioDirLoad(key, force)
	}
}

// radioDirLoad shows a list: the saved copy at once, then a fresh fetch when
// online. Main thread; the fetch is on its own goroutine.
func radioDirLoad(key string, force bool) {
	if radioDirUI.lists == nil {
		radioDirUI.lists = map[string]*radioDirList{}
	}
	l := radioDirUI.lists[key]
	if l == nil {
		l = &radioDirList{stations: radioDirCached(key)}
		radioDirUI.lists[key] = l
	}
	if l.loading || (l.fresh && !force) {
		return
	}
	if !IsOnline() {
		l.err = "Offline"
		return
	}
	l.loading, l.err = true, ""
	// Leaving the screen cancels the fetch; the next visit asks again.
	ctx := sceneWorkContext()
	go func() {
		stations, err := radioDirFetchList(ctx, key)
		if err == nil {
			radioDirCacheStore(key, stations, nil)
		}
		tbxPost(func() {
			l.loading = false
			if err != nil {
				logWarn("radiodir", "%s: %v", key, err)
				l.err = "Radio Browser did not answer"
				if ctx.Err() != nil {
					l.err = ""
				}
				return
			}
			l.stations, l.fresh, l.err = stations, true, ""
		})
	}()
}

// radioDirLoadCountries fetches the country list once a session (the saved
// copy shows meanwhile).
func radioDirLoadCountries(force bool) {
	if len(radioDirUI.countries) == 0 {
		radioDirUI.countries = radioDirCachedCountries()
	}
	if (radioDirUI.countriesAt && !force) || !IsOnline() {
		return
	}
	radioDirUI.countriesAt = true
	ctx := sceneWorkContext()
	go func() {
		countries, err := radioDirFetchCountries(ctx)
		if err != nil {
			logWarn("radiodir", "countries: %v", err)
			tbxPost(func() { radioDirUI.countriesAt = false })
			return
		}
		radioDirCacheStore("", nil, countries)
		tbxPost(func() { radioDirUI.countries = countries })
	}()
}

// radioDirCurrentList is the open tab's list, nil when it shows a picker.
func radioDirCurrentList() *radioDirList {
	key := radioDirKey()
	if key == "" || radioDirUI.lists == nil {
		return nil
	}
	return radioDirUI.lists[key]
}

// ── Drawing ──────────────────────────────────────────────────────────────

func radioDirRender(renderer *sdl.Renderer, config *Config) {
	if radioDirUI.visit.begin() {
		radioDirOnVisit()
	}
	status, statusColor := "", ColorTextMuted()
	if l := radioDirCurrentList(); l != nil {
		switch {
		case l.loading:
			status = "Loading…"
		case !l.fresh && len(l.stations) > 0:
			status = "Saved copy"
		case len(l.stations) > 0:
			status, statusColor = strconv.Itoa(len(l.stations))+" stations", ToastSuccessInk()
		}
	}
	header := tbxHeader("Radio directory", "Free internet radio from Radio Browser", "podcast", status, statusColor)
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, header)
	body := frame.bodyRect()
	const tabsH = int32(52)
	radioDirDrawTabs(renderer, config, fonts, sdl.Rect{X: body.X, Y: body.Y, W: body.W, H: tabsH - 12})
	view := sdl.Rect{X: body.X, Y: body.Y + tabsH, W: body.W, H: body.H - tabsH}
	nav := &radioDirUI.nav
	nav.begin(view.Y, view.H)
	tbxClip(renderer, view)
	top := view.Y - nav.scroll
	rowH := tbxRowHeight(fonts)
	y := int32(0)
	row := 0
	drawRow := func(id string, spec tbxRowSpec) {
		rect := sdl.Rect{X: view.X, Y: y, W: view.W, H: rowH}
		screen := rect
		screen.Y += top
		// Only the rows in view are drawn: a list holds up to 200.
		if screen.Y+screen.H >= view.Y && screen.Y <= view.Y+view.H {
			tbxDrawRow(renderer, config, fonts, screen, spec, nav.focused(id))
		}
		nav.add(id, rect, row, false)
		row++
		y += rowH + 8
	}
	empty := func(icon, title, text string) {
		tbxDrawEmpty(renderer, config, sdl.Rect{X: view.X, Y: top + y, W: view.W, H: 180}, icon, title, text)
		y += 180
	}

	// What plays comes first on every tab: its pill is what A does.
	if np, ok := radioNowShown(); ok {
		drawRow(radioDirNowID, tbxRowSpec{icon: "music", title: radioNowTitle(np), subtitle: radioNowLine(np),
			pill: np.action(), tone: tbxToneAccent})
	}

	switch radioDirUI.tab {
	case radioDirTabCountry:
		if radioDirUI.countryPick || radioDirUI.country == "" {
			if len(radioDirUI.countries) == 0 {
				if IsOnline() {
					empty("globe", "Loading countries…", "The list of countries comes from Radio Browser")
				} else {
					empty("wifi-off", "Offline", "Connect to Wi-Fi to choose a country")
				}
			}
			for _, c := range radioDirUI.countries {
				spec := tbxRowSpec{icon: "globe", title: c.Name, subtitle: strconv.Itoa(c.Count) + " stations"}
				if c.Code == radioDirUI.country {
					spec.pill, spec.tone = "Chosen", tbxToneAccent
				}
				drawRow("c:"+c.Code, spec)
			}
		} else {
			drawRow("country", tbxRowSpec{icon: "globe", title: "Country: " + radioDirCountryName(radioDirUI.country),
				subtitle: "A to choose another country", pill: "Change", tone: tbxToneNeutral})
			radioDirDrawStations(drawRow, empty)
		}
	case radioDirTabGenre:
		if radioDirUI.genre == "" {
			for i, g := range radioDirGenres {
				drawRow("g:"+strconv.Itoa(i), tbxRowSpec{icon: "music", title: radioDirGenreLabel(g)})
			}
		} else {
			drawRow("genre", tbxRowSpec{icon: "music", title: "Genre: " + radioDirGenreLabel(radioDirUI.genre),
				subtitle: "A or B for the other genres", pill: "Change", tone: tbxToneNeutral})
			radioDirDrawStations(drawRow, empty)
		}
	case radioDirTabSearch:
		sub := "Find a station by its name"
		title := "Search stations"
		if radioDirUI.query != "" {
			title = "Search: " + radioDirUI.query
			sub = "A to search for another name"
		}
		drawRow("search", tbxRowSpec{icon: "search", title: title, subtitle: sub, pill: "Type", tone: tbxToneAccent})
		if radioDirUI.query != "" {
			radioDirDrawStations(drawRow, empty)
		}
	default:
		radioDirDrawStations(drawRow, empty)
	}

	nav.extend(y)
	tbxUnclip(renderer)
	// A list that lost its focused row (a new tab, genre, country or search)
	// starts on its first row, not on the Now playing row above it; while the
	// list is still loading, the focus waits for its rows.
	waiting := false
	if nav.index() < 0 {
		waiting = true
		for _, it := range nav.items {
			if it.id != radioDirNowID {
				nav.focus, waiting = it.id, false
				break
			}
		}
	}
	nav.end()
	if waiting {
		nav.focus = ""
	}
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, view.Y, view.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

// radioDirDrawStations lays out the open list's stations, or its empty state.
func radioDirDrawStations(drawRow func(string, tbxRowSpec), empty func(string, string, string)) {
	l := radioDirCurrentList()
	if l == nil || len(l.stations) == 0 {
		switch {
		case l != nil && l.loading:
			empty("podcast", "Loading stations…", "Asking Radio Browser")
		case l != nil && l.err == "Offline":
			empty("wifi-off", "Offline", "Connect to Wi-Fi to load stations; lists you opened before are kept")
		case l != nil && l.err != "":
			empty("warning", l.err, "Y tries again")
		case l != nil && l.fresh:
			empty("search", "No stations", "Radio Browser lists none here")
		}
		return
	}
	np, npOK := radioNowShown()
	for i, s := range l.stations {
		spec := tbxRowSpec{icon: "podcast", title: s.Name, subtitle: radioDirStationLine(s)}
		if radioDirUI.saved[s.URL] {
			spec.pill, spec.tone = "Saved", tbxToneGood
		}
		if npOK && s.URL == np.path {
			// The station on the air (or stopped): A is its Play / Stop.
			if spec.pill != "" {
				spec.note = spec.pill
			}
			spec.pill, spec.tone = np.state(), tbxToneAccent
		}
		drawRow("st:"+strconv.Itoa(i), spec)
	}
}

// radioDirStationLine is a station's second line: "DE · jazz, smooth · MP3 128 kbps".
func radioDirStationLine(s radioStation) string {
	var parts []string
	if s.Country != "" {
		parts = append(parts, s.Country)
	}
	if s.Tags != "" {
		parts = append(parts, s.Tags)
	}
	tech := s.Codec
	if s.Bitrate > 0 {
		tech = strings.TrimSpace(tech + " " + strconv.Itoa(s.Bitrate) + " kbps")
	}
	if tech != "" {
		parts = append(parts, tech)
	}
	if len(parts) == 0 {
		return channelNameFromURL(s.URL)
	}
	return strings.Join(parts, " · ")
}

// radioDirCountryName names a code from the country list, else the code.
func radioDirCountryName(code string) string {
	for _, c := range radioDirUI.countries {
		if c.Code == code {
			return c.Name
		}
	}
	return code
}

// radioDirGenreLabel capitalises a tag for the list: "hip hop" → "Hip hop".
func radioDirGenreLabel(g string) string {
	if g == "" {
		return ""
	}
	return strings.ToUpper(g[:1]) + g[1:]
}

// radioDirDrawTabs draws the four tab pills.
func radioDirDrawTabs(renderer *sdl.Renderer, config *Config, fonts tbxFonts, r sdl.Rect) {
	n := int32(len(radioDirTabNames))
	gap := int32(10)
	w := (r.W - gap*(n-1)) / n
	for i, name := range radioDirTabNames {
		rect := sdl.Rect{X: r.X + int32(i)*(w+gap), Y: r.Y, W: w, H: r.H}
		col := WithAlpha(ColorPanelRaised, 205)
		ink := ColorTextMuted()
		if i == radioDirUI.tab {
			col = WithAlpha(glassAccent(), 230)
			ink = ColorTextPrimary()
		}
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, rect.H/2, col)
		tw, th := measureText(fonts.body, name)
		renderText(renderer, config, fonts.body, name, ink, rect.X+(rect.W-tw)/2, rect.Y+(rect.H-th)/2)
	}
}

// ── Input ────────────────────────────────────────────────────────────────

func radioDirAction(config *Config, act tbxAction) bool {
	nav := &radioDirUI.nav
	switch act {
	case tbxActLeft, tbxActPageUp:
		radioDirSwitchTab(-1)
		return true
	case tbxActRight, tbxActPageDown:
		radioDirSwitchTab(1)
		return true
	case tbxActConfirm:
		radioDirActivate(config, nav.focus)
		return true
	case tbxActX:
		if nav.focus == radioDirNowID {
			radioOpenPlayer(config, false)
			return true
		}
		radioDirToggleFocused()
		return true
	case tbxActY:
		if radioDirUI.tab == radioDirTabSearch {
			radioDirAskQuery()
		} else {
			PlayActivateSound()
			radioDirLoadTab(true)
		}
		return true
	case tbxActBack:
		switch {
		case radioDirUI.tab == radioDirTabCountry && radioDirUI.countryPick && radioDirUI.country != "":
			radioDirUI.countryPick = false
		case radioDirUI.tab == radioDirTabGenre && radioDirUI.genre != "":
			radioDirUI.genre = ""
		default:
			return false
		}
		PlayBackSound()
		radioDirFocusTop()
		return true
	}
	return tbxNavAction(nav, act)
}

// radioDirFocusTop puts the focus on the open list's first row, under the Now
// playing row: radioDirRender picks it once the list is laid out.
func radioDirFocusTop() {
	radioDirUI.nav.focus = ""
	radioDirUI.nav.scroll = 0
}

// radioDirSwitchTab steps to the next tab and loads what it shows.
func radioDirSwitchTab(dir int) {
	n := len(radioDirTabNames)
	radioDirUI.tab = ((radioDirUI.tab+dir)%n + n) % n
	radioDirUI.nav.focus = ""
	radioDirUI.nav.scroll = 0
	PlayNavSound()
	radioDirLoadTab(false)
}

// radioDirActivate is A on a row.
func radioDirActivate(config *Config, id string) {
	switch {
	case id == radioDirNowID:
		radioToggleNow()
	case strings.HasPrefix(id, "st:"):
		i, err := strconv.Atoi(id[3:])
		l := radioDirCurrentList()
		if err != nil || l == nil || i < 0 || i >= len(l.stations) {
			return
		}
		// The station on the air: A stops it, or tunes in again, in place.
		if radioIsNow(l.stations[i].URL) {
			radioToggleNow()
			return
		}
		PlayActivateSound()
		if radioDirPlay(config, l.stations, i) {
			ShowToast("Tuning in to "+l.stations[i].Name+"…", ToastKindInfo)
		}
	case id == "country":
		PlayActivateSound()
		radioDirUI.countryPick = true
		radioDirUI.nav.focus = "c:" + radioDirUI.country
		radioDirLoadCountries(false)
	case strings.HasPrefix(id, "c:"):
		code := id[2:]
		if !radioDirCountryCodeValid(code) {
			return
		}
		PlayActivateSound()
		radioDirUI.country, radioDirUI.countryPick = code, false
		radioDirFocusTop()
		radioDirLoadTab(false)
	case id == "genre":
		PlayActivateSound()
		radioDirUI.genre = ""
		radioDirFocusTop()
	case strings.HasPrefix(id, "g:"):
		i, err := strconv.Atoi(id[2:])
		if err != nil || i < 0 || i >= len(radioDirGenres) {
			return
		}
		PlayActivateSound()
		radioDirUI.genre = radioDirGenres[i]
		radioDirFocusTop()
		radioDirLoadTab(false)
	case id == "search":
		radioDirAskQuery()
	}
}

// radioDirAskQuery opens the keyboard for a station name.
func radioDirAskQuery() {
	PlayActivateSound()
	osPromptOpen("Search radio stations", "Station name", "Search", radioDirUI.query, 60, func(text string) {
		text = radioDirOneLine(text, 60)
		radioDirUI.query = text
		radioDirUI.tab = radioDirTabSearch
		radioDirFocusTop()
		if text != "" {
			radioDirLoad("q:"+strings.ToLower(text), false)
		}
	})
}

// radioDirToggleFocused is X on a station: keep it in your stations, or take
// it out.
func radioDirToggleFocused() {
	id := radioDirUI.nav.focus
	if !strings.HasPrefix(id, "st:") {
		return
	}
	i, err := strconv.Atoi(id[3:])
	l := radioDirCurrentList()
	if err != nil || l == nil || i < 0 || i >= len(l.stations) {
		return
	}
	s := l.stations[i]
	saved, err := radioDirToggleSaved(s)
	radioDirUI.saved = radioDirSavedSet()
	// The Music screen's Radio shelf lists the stations again when next drawn.
	musicShelf.gen = 0
	switch {
	case err != nil:
		PlayErrorSound()
		ShowToast(err.Error(), ToastKindWarning)
	case saved:
		PlayActivateSound()
		ShowToast(s.Name+" is in your stations (Music → Radio)", ToastKindSuccess)
	default:
		PlayBackSound()
		ShowToast(s.Name+" removed from your stations", ToastKindInfo)
	}
}

// radioDirFooterHints is the screen's footer (extras.go).
func radioDirFooterHints(scene string) ([]FooterHint, bool) {
	if scene != radioDirSceneName {
		return nil, false
	}
	hints := []FooterHint{{Button: "L1/R1", Label: "Tabs"}}
	focus := radioDirUI.nav.focus
	switch {
	case focus == radioDirNowID:
		hints = append(hints, FooterHint{Button: "A", Label: radioNowAction()}, FooterHint{Button: "X", Label: "Player"})
	case strings.HasPrefix(focus, "st:"):
		play := "Play"
		if s, ok := radioDirStationFor(focus); ok && radioIsNow(s.URL) {
			play = radioNowAction()
		}
		hints = append(hints, FooterHint{Button: "A", Label: play}, FooterHint{Button: "X", Label: "Save"})
	case focus != "":
		hints = append(hints, FooterHint{Button: "A", Label: "Open"})
	}
	if radioDirUI.tab == radioDirTabSearch {
		hints = append(hints, FooterHint{Button: "Y", Label: "Search"})
	} else {
		hints = append(hints, FooterHint{Button: "Y", Label: "Reload"})
	}
	return append(hints, FooterHint{Button: "B", Label: "Back"}), true
}

// radioDirStationFor is the station row id names on the open list.
func radioDirStationFor(id string) (radioStation, bool) {
	if !strings.HasPrefix(id, "st:") {
		return radioStation{}, false
	}
	i, err := strconv.Atoi(id[3:])
	l := radioDirCurrentList()
	if err != nil || l == nil || i < 0 || i >= len(l.stations) {
		return radioStation{}, false
	}
	return l.stations[i], true
}

// ── Now playing, on both radio screens ───────────────────────────────────

// radioNowShown is what the Now playing rows show: the loaded track while it
// plays or is paused, or a station that was stopped (musicNowState.shown).
func radioNowShown() (musicNowState, bool) {
	np, ok := musicNow()
	return np, ok && np.shown()
}

// radioIsNow reports whether link is the station a Now playing row shows: A on
// its row is Play / Stop in place rather than a fresh tune-in.
func radioIsNow(link string) bool {
	np, ok := radioNowShown()
	return ok && link != "" && np.path == link
}

// radioNowAction is what A does on a Now playing row: Stop, Pause or Play.
func radioNowAction() string {
	np, _ := musicNow()
	return np.action()
}

// radioNowTitle is a Now playing row's title: the station's ICY title while it
// reports one, else the track's name, else its link's.
func radioNowTitle(np musicNowState) string {
	if t := radioDirOneLine(np.stationTitle, 80); t != "" {
		return t
	}
	if t := radioDirOneLine(np.title, 80); t != "" {
		return t
	}
	return channelNameFromURL(np.path)
}

// radioNowLine is a Now playing row's second line: where the track is, and what
// it is.
func radioNowLine(np musicNowState) string {
	where := "Now playing"
	if st := np.state(); st != "Playing" {
		where = st
	}
	if np.live {
		return where + " · Internet radio"
	}
	return where + " · Music"
}

// radioToggleNow is A on a Now playing row, or on the station it shows: Stop,
// Pause or Play, as the player's own control (musicTogglePause plays its own
// click). A stopped station tunes in again only online, and not in Kid mode.
func radioToggleNow() {
	np, ok := musicNow()
	if !ok {
		return
	}
	if np.live && !np.playing {
		if !uiModeStreamOK(np.path) {
			return
		}
		if !IsOnline() && !netReconnecting() {
			ShowToast(offlinePlainNotice, ToastKindWarning)
			return
		}
		ShowToast("Tuning in to "+radioNowTitle(np)+"…", ToastKindInfo)
	}
	musicTogglePause()
}

// radioOpenPlayer is X on a Now playing row: the music player, the pad on its
// Play / Stop for a station. fromShelf sends the player's B back to the Radio
// shelf (it lives in the player's scene); from the directory B returns there.
func radioOpenPlayer(config *Config, fromShelf bool) {
	PlayActivateSound()
	musicShelf.open, musicShelf.fromShelf = false, fromShelf
	openMusicPlayerScene(config)
	if musicCurrentLive() {
		musicCtlFocusPlay()
	}
}

// ── The Music screen's Radio shelf ───────────────────────────────────────

// radioShelfGen marks the Radio shelf's rows as built (musicShelf.gen; 0 is
// "rebuild").
const radioShelfGen = ^uint64(0) - 7

// musicShelfRefreshRadio builds the Radio shelf: what plays, the directory,
// then the user's stations (musicShelfRefresh).
func musicShelfRefreshRadio() {
	if musicShelf.gen == radioShelfGen {
		return
	}
	var rows []musicShelfRow
	// The Now playing row's words are read as it is drawn: A changes them
	// under it (radioShelfNowRow).
	if _, ok := radioNowShown(); ok {
		rows = append(rows, musicShelfRow{kind: "nowplaying"})
	}
	rows = append(rows, musicShelfRow{kind: "radiodir", title: "Radio directory",
		sub: "Stations by country, genre or name — free, from Radio Browser"})
	for _, s := range radioDirSavedStations() {
		sub := s.Group
		if sub == "" {
			sub = channelNameFromURL(s.URL)
		}
		rows = append(rows, musicShelfRow{kind: "station", title: radioDirOneLine(s.Name, 80), sub: sub, path: s.URL})
	}
	musicShelf.rows = rows
	musicShelf.gen = radioShelfGen
	if musicShelf.cursor >= len(musicShelf.rows) {
		musicShelf.cursor = len(musicShelf.rows) - 1
	}
	if musicShelf.cursor < 0 {
		musicShelf.cursor = 0
	}
}

// musicShelfChooseRadio is A on a Radio shelf row.
func musicShelfChooseRadio(config *Config, row musicShelfRow) {
	if row.kind == "radiodir" {
		radioDirOpen(config)
		return
	}
	var stations []radioStation
	start := 0
	for _, r := range musicShelf.rows {
		if r.kind != "station" {
			continue
		}
		if r.path == row.path {
			start = len(stations)
		}
		stations = append(stations, radioStation{Name: r.title, URL: r.path})
	}
	if radioDirPlay(config, stations, start) {
		musicShelf.fromShelf = true // B goes back to the shelf, as History does
		ShowToast("Tuning in to "+row.title+"…", ToastKindInfo)
	}
}

// musicShelfChooseNow is A on the Radio shelf's Now playing row, or on the
// saved station it shows: Play / Stop in place. It reports false for any
// other row, which musicShelfChooseRadio opens.
func musicShelfChooseNow(row musicShelfRow) bool {
	if row.kind != "nowplaying" && (row.kind != "station" || !radioIsNow(row.path)) {
		return false
	}
	radioToggleNow()
	return true
}

// musicShelfNowFocused reports whether the shelf's cursor is on the Now
// playing row, where X opens the player instead of rescanning.
func musicShelfNowFocused() bool {
	c := musicShelf.cursor
	return c >= 0 && c < len(musicShelf.rows) && musicShelf.rows[c].kind == "nowplaying"
}

// radioShelfNowRow is a Radio shelf row as it is drawn this frame: the Now
// playing row's words, and the saved station on the air marked where it is.
func radioShelfNowRow(row musicShelfRow, np musicNowState, npOK bool) musicShelfRow {
	switch {
	case row.kind == "nowplaying" && npOK:
		row.title, row.sub = radioNowTitle(np), radioNowLine(np)
	case row.kind == "nowplaying":
		row.title, row.sub = "Nothing playing", "Pick a station below"
	case row.kind == "station" && npOK && row.path == np.path:
		row.sub = np.state() + " · " + row.sub
	}
	return row
}

// radioShelfNowGlyph is the mark in the Now playing row's artwork square: what
// A does.
func radioShelfNowGlyph(np musicNowState) string {
	switch np.action() {
	case "Stop":
		return "stop"
	case "Pause":
		return "pause"
	}
	return "play"
}

// musicShelfNowFooter is the shelf's footer on the Now playing row or on the
// station it shows: A's Play / Stop and, on the row, X's player. ok is false
// elsewhere, where the shelf keeps its own line.
func musicShelfNowFooter() (string, bool) {
	c := musicShelf.cursor
	if c < 0 || c >= len(musicShelf.rows) {
		return "", false
	}
	switch row := musicShelf.rows[c]; {
	case row.kind == "nowplaying":
		return hintLine(FooterHint{Button: "A", Label: radioNowAction()}, FooterHint{Button: "X", Label: "Player"},
			FooterHint{Button: "L1/R1", Label: "Shelves"}), true
	case row.kind == "station" && radioIsNow(row.path):
		return hintLine(FooterHint{Button: "A", Label: radioNowAction()}, FooterHint{Button: "L1/R1", Label: "Shelves"}), true
	}
	return "", false
}
