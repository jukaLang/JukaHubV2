package main

// musicautoeq.go — AutoEQ headphone profiles: a correction curve that makes a
// headphone sound neutral, from the AutoEq project's measurements
// (github.com/jaakkopasanen/AutoEq, MIT licensed).
//
// The list of every measured headphone is the project's results/INDEX.md. It
// is downloaded once and kept (the offline database): the picker searches it
// with no network at all. Picking a headphone downloads its ParametricEQ.txt
// once and keeps that too, so a profile used before applies offline. A
// ParametricEQ.txt or FixedBandEQ.txt dropped into Music/AutoEQ (or made with
// autoeq.app) is listed as well, first.
//
// The profile is an Equalizer APO filter list: a preamp and up to ten peak
// and shelf filters. It becomes ffmpeg filters in front of the 31-band PEQ
// (musicPEQCurrentChain), so the two stack: the headphone is corrected first,
// then shaped to taste. Bit-perfect output bypasses it with everything else.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	musicAutoEQKey      = "musicAutoEQ"
	autoEQIndexURL      = "https://raw.githubusercontent.com/jaakkopasanen/AutoEq/master/results/INDEX.md"
	autoEQResultsURL    = "https://raw.githubusercontent.com/jaakkopasanen/AutoEq/master/results/"
	autoEQMaxIndexBytes = 8 << 20
	autoEQMaxFileBytes  = 64 << 10
	autoEQMaxFilters    = 20
)

// autoEQFilter is one line of an Equalizer APO filter list.
type autoEQFilter struct {
	Type string // PK, LSC, HSC
	Fc   float64
	Gain float64
	Q    float64
}

// autoEQProfile is a parsed profile.
type autoEQProfile struct {
	Name    string
	Preamp  float64
	Filters []autoEQFilter
}

// autoEQParse reads ParametricEQ.txt / FixedBandEQ.txt. Lines it does not know
// are skipped; a file without a single filter is an error. Pure.
func autoEQParse(name, text string) (autoEQProfile, error) {
	p := autoEQProfile{Name: name}
	hasPreamp := false
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		low := strings.ToLower(line)
		switch {
		case strings.HasPrefix(low, "preamp:"):
			f := strings.Fields(line[len("preamp:"):])
			if len(f) > 0 {
				if v, err := strconv.ParseFloat(f[0], 64); err == nil && v > -40 && v < 20 {
					p.Preamp, hasPreamp = v, true
				}
			}
		case strings.HasPrefix(low, "filter"):
			if flt, ok := autoEQParseFilter(line); ok && len(p.Filters) < autoEQMaxFilters {
				p.Filters = append(p.Filters, flt)
			}
		}
	}
	if len(p.Filters) == 0 {
		return p, errors.New("no filters in that file")
	}
	if !hasPreamp {
		// No preamp given: leave headroom for the largest boost.
		for _, f := range p.Filters {
			if -f.Gain < p.Preamp {
				p.Preamp = -f.Gain
			}
		}
	}
	return p, nil
}

// autoEQParseFilter reads "Filter 1: ON PK Fc 105 Hz Gain 7.8 dB Q 0.70".
func autoEQParseFilter(line string) (autoEQFilter, bool) {
	_, rest, ok := strings.Cut(line, ":")
	if !ok {
		return autoEQFilter{}, false
	}
	f := strings.Fields(rest)
	if len(f) < 2 || !strings.EqualFold(f[0], "ON") {
		return autoEQFilter{}, false
	}
	out := autoEQFilter{Type: strings.ToUpper(f[1]), Q: 0.707}
	switch out.Type {
	case "PK", "PEQ":
		out.Type = "PK"
	case "LSC", "LS", "LSQ":
		out.Type = "LSC"
	case "HSC", "HS", "HSQ":
		out.Type = "HSC"
	default:
		return autoEQFilter{}, false
	}
	for i := 2; i+1 < len(f); i++ {
		v, err := strconv.ParseFloat(f[i+1], 64)
		if err != nil {
			continue
		}
		switch strings.ToLower(f[i]) {
		case "fc":
			out.Fc = v
		case "gain":
			out.Gain = v
		case "q":
			out.Q = v
		}
	}
	if out.Fc < 10 || out.Fc > 22000 || out.Q <= 0 || out.Q > 20 || out.Gain < -30 || out.Gain > 30 {
		return autoEQFilter{}, false
	}
	return out, true
}

// autoEQFilterChain is the profile as ffmpeg filters. Pure.
func autoEQFilterChain(p autoEQProfile) string {
	if len(p.Filters) == 0 {
		return ""
	}
	num := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	parts := make([]string, 0, len(p.Filters)+1)
	if p.Preamp < -0.05 || p.Preamp > 0.05 {
		parts = append(parts, "volume="+num(p.Preamp)+"dB")
	}
	for _, f := range p.Filters {
		switch f.Type {
		case "LSC":
			parts = append(parts, fmt.Sprintf("bass=g=%s:f=%s:t=q:w=%s", num(f.Gain), num(f.Fc), num(f.Q)))
		case "HSC":
			parts = append(parts, fmt.Sprintf("treble=g=%s:f=%s:t=q:w=%s", num(f.Gain), num(f.Fc), num(f.Q)))
		default:
			parts = append(parts, fmt.Sprintf("equalizer=f=%s:t=q:w=%s:g=%s", num(f.Fc), num(f.Q), num(f.Gain)))
		}
	}
	return strings.Join(parts, ",")
}

// autoEQEntry is one headphone of the index (or one local file).
type autoEQEntry struct {
	Name   string
	Source string // "oratory1990", "crinacle on …", or "Your file"
	Rel    string // folder under results/, unescaped
	File   string // a local profile's path
}

// autoEQParseIndex reads INDEX.md's "- [Name](./source/type/Name) by source"
// lines. Pure.
func autoEQParseIndex(md string) []autoEQEntry {
	var out []autoEQEntry
	for _, raw := range strings.Split(md, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "*") {
			continue
		}
		open := strings.Index(line, "[")
		mid := strings.Index(line, "](")
		if open < 0 || mid < open {
			continue
		}
		end := strings.Index(line[mid:], ")")
		if end < 0 {
			continue
		}
		name := strings.TrimSpace(line[open+1 : mid])
		link := strings.TrimPrefix(strings.TrimSpace(line[mid+2:mid+end]), "./")
		if name == "" || link == "" || strings.Contains(link, "://") || strings.Contains(link, "..") {
			continue
		}
		rel, err := url.PathUnescape(link)
		if err != nil {
			continue
		}
		rest := strings.TrimSpace(line[mid+end+1:])
		source := strings.TrimSpace(strings.TrimPrefix(rest, "by "))
		out = append(out, autoEQEntry{Name: name, Source: source, Rel: strings.Trim(rel, "/")})
	}
	return out
}

// autoEQProfileURL is where an entry's ParametricEQ.txt is. Pure.
func autoEQProfileURL(e autoEQEntry) string {
	segs := strings.Split(e.Rel, "/")
	esc := make([]string, 0, len(segs)+1)
	for _, s := range segs {
		esc = append(esc, url.PathEscape(s))
	}
	last := segs[len(segs)-1]
	esc = append(esc, url.PathEscape(last+" ParametricEQ.txt"))
	return autoEQResultsURL + strings.Join(esc, "/")
}

// ── Storage ─────────────────────────────────────────────────────────────────

func autoEQDir() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "autoeq")
}

// autoEQLocalDirs are the folders a profile file may be dropped into.
func autoEQLocalDirs() []string {
	var out []string
	for _, r := range mlibDefaultRoots() {
		if strings.EqualFold(filepath.Base(r), "Music") {
			out = append(out, filepath.Join(r, "AutoEQ"))
		}
	}
	if d := autoEQDir(); d != "" {
		out = append(out, filepath.Join(d, "import"))
	}
	return out
}

// autoEQSafeName is a file name for a cached profile.
func autoEQSafeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|' || r < 0x20:
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	s := strings.TrimSpace(b.String())
	if len(s) > 120 {
		s = s[:120]
	}
	return s + ".txt"
}

// musicAutoEQ is the live state.
var musicAutoEQ struct {
	mu      sync.Mutex
	loaded  bool
	name    string
	chain   string
	index   []autoEQEntry
	indexAt time.Time
	busy    atomic.Bool
}

// musicAutoEQChain is the chain the decoder puts in front of the PEQ: ""
// with no profile.
func musicAutoEQChain() string {
	musicAutoEQ.mu.Lock()
	defer musicAutoEQ.mu.Unlock()
	if !musicAutoEQ.loaded {
		return ""
	}
	return musicAutoEQ.chain
}

// musicAutoEQName is the headphone in use, "" for none.
func musicAutoEQName() string {
	musicAutoEQ.mu.Lock()
	defer musicAutoEQ.mu.Unlock()
	return musicAutoEQ.name
}

// musicAutoEQLoad reads the chosen profile back at start. Main thread (it
// reads the config).
func musicAutoEQLoad(config *Config) {
	musicAutoEQ.mu.Lock()
	if musicAutoEQ.loaded {
		musicAutoEQ.mu.Unlock()
		return
	}
	musicAutoEQ.loaded = true
	musicAutoEQ.mu.Unlock()
	if config == nil {
		return
	}
	name := strings.TrimSpace(customStringValue(config.Variables.Custom, musicAutoEQKey))
	if name == "" {
		return
	}
	if p, err := autoEQReadCached(name); err == nil {
		musicAutoEQ.mu.Lock()
		musicAutoEQ.name, musicAutoEQ.chain = name, autoEQFilterChain(p)
		musicAutoEQ.mu.Unlock()
	}
}

// autoEQReadCached reads a profile kept on the card: the download cache first,
// then the import folders.
func autoEQReadCached(name string) (autoEQProfile, error) {
	var paths []string
	if d := autoEQDir(); d != "" {
		paths = append(paths, filepath.Join(d, "profiles", autoEQSafeName(name)))
	}
	for _, dir := range autoEQLocalDirs() {
		paths = append(paths, filepath.Join(dir, name))
	}
	for _, p := range paths {
		data, err := readSmallFile(p, autoEQMaxFileBytes)
		if err != nil {
			continue
		}
		return autoEQParse(name, string(data))
	}
	return autoEQProfile{}, os.ErrNotExist
}

// readSmallFile reads a file up to limit bytes.
func readSmallFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}

// musicAutoEQApply makes p the profile in use (nil turns AutoEQ off) and
// restarts the decoder so it is heard at once. Main thread.
func musicAutoEQApply(config *Config, p *autoEQProfile) {
	musicAutoEQ.mu.Lock()
	musicAutoEQ.loaded = true
	if p == nil {
		musicAutoEQ.name, musicAutoEQ.chain = "", ""
	} else {
		musicAutoEQ.name, musicAutoEQ.chain = p.Name, autoEQFilterChain(*p)
	}
	name := musicAutoEQ.name
	musicAutoEQ.mu.Unlock()
	if config != nil {
		if config.Variables.Custom == nil {
			config.Variables.Custom = map[string]interface{}{}
		}
		config.Variables.Custom[musicAutoEQKey] = name
	}
	musicRestartAtHeard()
}

// autoEQHTTPClient talks to GitHub's raw file host, with the firmware CA
// fallback a handheld needs.
var autoEQHTTPClient = sync.OnceValue(func() *http.Client {
	return musicHTTPClient(30 * time.Second)
})

// autoEQFetch downloads one file from the AutoEq repository.
func autoEQFetch(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "JukaHub/"+strings.TrimPrefix(versionString(nil), "v")+" (AutoEQ)")
	resp, err := autoEQHTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub answered %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// autoEQLoadIndex reads the kept index (and the local files); refresh
// downloads it again first. Any goroutine but the main one when refreshing.
func autoEQLoadIndex(ctx context.Context, refresh bool) ([]autoEQEntry, error) {
	dir := autoEQDir()
	if dir == "" {
		return nil, errors.New("no data folder")
	}
	path := filepath.Join(dir, "INDEX.md")
	var fetchErr error
	if _, err := os.Stat(path); refresh || err != nil {
		data, err := autoEQFetch(ctx, autoEQIndexURL, autoEQMaxIndexBytes)
		if err == nil && len(autoEQParseIndex(string(data))) > 0 {
			_ = os.MkdirAll(dir, 0o755)
			if werr := AtomicWrite(path, data, 0o644); werr != nil {
				fetchErr = werr
			}
		} else if err != nil {
			fetchErr = err
		} else {
			fetchErr = errors.New("the AutoEq index had no headphones in it")
		}
	}
	var out []autoEQEntry
	for _, d := range autoEQLocalDirs() {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			n := e.Name()
			if e.IsDir() || !strings.EqualFold(filepath.Ext(n), ".txt") {
				continue
			}
			out = append(out, autoEQEntry{Name: n, Source: "Your file", File: filepath.Join(d, n)})
		}
	}
	if data, err := readSmallFile(path, autoEQMaxIndexBytes); err == nil {
		list := autoEQParseIndex(string(data))
		sort.SliceStable(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
		out = append(out, list...)
	} else if fetchErr == nil {
		fetchErr = err
	}
	return out, fetchErr
}

// autoEQGetProfile returns an entry's profile, downloading and keeping it the
// first time.
func autoEQGetProfile(ctx context.Context, e autoEQEntry) (autoEQProfile, error) {
	if e.File != "" {
		data, err := readSmallFile(e.File, autoEQMaxFileBytes)
		if err != nil {
			return autoEQProfile{}, err
		}
		return autoEQParse(e.Name, string(data))
	}
	if p, err := autoEQReadCached(e.Name); err == nil {
		return p, nil
	}
	data, err := autoEQFetch(ctx, autoEQProfileURL(e), autoEQMaxFileBytes)
	if err != nil {
		return autoEQProfile{}, err
	}
	p, err := autoEQParse(e.Name, string(data))
	if err != nil {
		return p, err
	}
	if dir := autoEQDir(); dir != "" {
		pdir := filepath.Join(dir, "profiles")
		if os.MkdirAll(pdir, 0o755) == nil {
			_ = AtomicWrite(filepath.Join(pdir, autoEQSafeName(e.Name)), data, 0o644)
		}
	}
	return p, nil
}

// ──────────────────────────────────────────────────────────────────────
// Picker
// ──────────────────────────────────────────────────────────────────────

// autoEQPanel is the headphone picker. Main thread only.
var autoEQPanel struct {
	open    bool
	query   string
	all     []autoEQEntry
	hits    []int
	cursor  int
	scroll  int
	status  string
	loading bool
	card    sdl.Rect
	rows    []sdl.Rect
}

// musicAutoEQOpen opens the picker and reads the kept list (downloading it
// when there is none yet).
func musicAutoEQOpen(refresh bool) {
	musicEQ.panel = false
	musicFXPanel.open = false
	autoEQPanel.open = true
	autoEQPanel.cursor, autoEQPanel.scroll = 0, 0
	if autoEQPanel.loading {
		return
	}
	autoEQPanel.loading = true
	autoEQPanel.status = "Reading the headphone list…"
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		list, err := autoEQLoadIndex(ctx, refresh)
		tbxPost(func() {
			autoEQPanel.loading = false
			autoEQPanel.all = list
			autoEQFilterHits()
			switch {
			case err != nil && len(list) == 0:
				autoEQPanel.status = "Could not get the AutoEq list: " + firstLine(err.Error()) + ". Connect to Wi-Fi, or drop ParametricEQ.txt files into Music/AutoEQ."
			case err != nil:
				autoEQPanel.status = fmt.Sprintf("%d headphones (kept list; the update failed)", len(list))
			default:
				autoEQPanel.status = fmt.Sprintf("%d headphones", len(list))
			}
		})
	}()
	PlayActivateSound()
}

func autoEQFilterHits() {
	q := strings.ToLower(strings.TrimSpace(autoEQPanel.query))
	autoEQPanel.hits = autoEQPanel.hits[:0]
	for i, e := range autoEQPanel.all {
		if q == "" || strings.Contains(strings.ToLower(e.Name), q) {
			autoEQPanel.hits = append(autoEQPanel.hits, i)
		}
	}
	autoEQPanel.cursor, autoEQPanel.scroll = 0, 0
}

func autoEQClose() {
	if !autoEQPanel.open {
		return
	}
	autoEQPanel.open = false
	PlayBackSound()
}

// autoEQRowCount is the picker's rows: "Off", "Search", "Update", then hits.
const autoEQFixedRows = 3

func autoEQMove(delta int) {
	n := autoEQFixedRows + len(autoEQPanel.hits)
	autoEQPanel.cursor = ((autoEQPanel.cursor+delta)%n + n) % n
	PlayNavSound()
}

func autoEQSearch() {
	osPromptOpen("Find your headphones", "Model, e.g. HD 650", "Search", autoEQPanel.query, 60, func(q string) {
		autoEQPanel.query = q
		autoEQFilterHits()
	})
}

// autoEQActivate runs the focused row.
func autoEQActivate(config *Config) {
	switch autoEQPanel.cursor {
	case 0:
		musicAutoEQApply(config, nil)
		showToast("AutoEQ off", ToastInfo())
		PlayActivateSound()
		return
	case 1:
		autoEQSearch()
		return
	case 2:
		musicAutoEQOpen(true)
		return
	}
	i := autoEQPanel.cursor - autoEQFixedRows
	if i < 0 || i >= len(autoEQPanel.hits) || musicAutoEQ.busy.Load() {
		return
	}
	e := autoEQPanel.all[autoEQPanel.hits[i]]
	musicAutoEQ.busy.Store(true)
	autoEQPanel.status = "Getting " + e.Name + "…"
	go func() {
		defer musicAutoEQ.busy.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		p, err := autoEQGetProfile(ctx, e)
		tbxPost(func() {
			if err != nil {
				autoEQPanel.status = "Could not get that profile: " + firstLine(err.Error())
				showToast("AutoEQ: "+firstLine(err.Error()), ToastWarn())
				return
			}
			musicAutoEQApply(config, &p)
			autoEQPanel.status = fmt.Sprintf("%s: %d filters, preamp %.1f dB", p.Name, len(p.Filters), p.Preamp)
			showToast("AutoEQ: "+p.Name, ToastSuccess())
		})
	}()
	PlayActivateSound()
}

func handleAutoEQKey(e *sdl.KeyboardEvent, config *Config) bool {
	if !autoEQPanel.open || e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		autoEQMove(-1)
	case sdl.K_DOWN:
		autoEQMove(1)
	case sdl.K_PAGEUP:
		autoEQMove(-8)
	case sdl.K_PAGEDOWN:
		autoEQMove(8)
	case sdl.K_RETURN, sdl.K_SPACE:
		autoEQActivate(config)
	case sdl.K_SLASH, sdl.K_f:
		autoEQSearch()
	case sdl.K_ESCAPE, sdl.K_BACKSPACE:
		autoEQClose()
	}
	return true
}

func ctrlAutoEQ(e *sdl.ControllerButtonEvent, config *Config) bool {
	if !autoEQPanel.open || e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		autoEQMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		autoEQMove(1)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER, sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		autoEQMove(-8)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER, sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		autoEQMove(8)
	case sdl.CONTROLLER_BUTTON_A:
		autoEQActivate(config)
	case sdl.CONTROLLER_BUTTON_X, sdl.CONTROLLER_BUTTON_Y:
		autoEQSearch()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		autoEQClose()
	}
	return true
}

func handleAutoEQClick(mx, my int32, config *Config) bool {
	if !autoEQPanel.open {
		return false
	}
	for i, r := range autoEQPanel.rows {
		if pointInRect(mx, my, r) {
			autoEQPanel.cursor = autoEQPanel.scroll + i
			autoEQActivate(config)
			return true
		}
	}
	if !pointInRect(mx, my, autoEQPanel.card) {
		autoEQClose()
	}
	return true
}

// autoEQRowLabel is a row's text.
func autoEQRowLabel(row int) (string, string) {
	switch row {
	case 0:
		return "Off", "No headphone correction"
	case 1:
		q := autoEQPanel.query
		if q == "" {
			return "Search…", "Type part of the model name"
		}
		return "Search: " + q, fmt.Sprintf("%d match", len(autoEQPanel.hits))
	case 2:
		return "Update the list", "Download the newest AutoEq index"
	}
	i := row - autoEQFixedRows
	if i < 0 || i >= len(autoEQPanel.hits) {
		return "", ""
	}
	e := autoEQPanel.all[autoEQPanel.hits[i]]
	return e.Name, e.Source
}

func renderAutoEQPanel(renderer *sdl.Renderer, config *Config) {
	if !autoEQPanel.open || renderer == nil {
		return
	}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	smallH := int32(font.Height())
	w := screenWidth - 96
	if w > 900 {
		w = 900
	}
	h := screenHeight - 2*footerBarHeight()
	card := sdl.Rect{X: (screenWidth - w) / 2, Y: (screenHeight - h) / 2, W: w, H: h}
	autoEQPanel.card = card
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	current := musicAutoEQName()
	if current == "" {
		current = "Off"
	}
	header := overlayPageHeader(card, "AutoEQ headphones", current, "media")
	band := UIPanelHeaderBand(config, header, 6)
	UIPageHeaderDraw(renderer, config, header)

	statusY := card.Y + band
	renderText(renderer, config, font, ellipsize(font, autoEQPanel.status, card.W-2*OverlayPadX), ColorTextTertiary(), card.X+OverlayPadX, statusY)
	rowH := smallH + 18
	top := statusY + smallH + 10
	footer := smallH + 20
	visible := int((card.Y + card.H - footer - top) / rowH)
	if visible < 1 {
		visible = 1
	}
	total := autoEQFixedRows + len(autoEQPanel.hits)
	if autoEQPanel.cursor >= total {
		autoEQPanel.cursor = total - 1
	}
	if autoEQPanel.cursor < autoEQPanel.scroll {
		autoEQPanel.scroll = autoEQPanel.cursor
	}
	if autoEQPanel.cursor >= autoEQPanel.scroll+visible {
		autoEQPanel.scroll = autoEQPanel.cursor - visible + 1
	}
	autoEQPanel.rows = autoEQPanel.rows[:0]
	for row := autoEQPanel.scroll; row < total && row < autoEQPanel.scroll+visible; row++ {
		r := sdl.Rect{X: card.X + OverlayPadX, Y: top + int32(row-autoEQPanel.scroll)*rowH, W: card.W - 2*OverlayPadX, H: rowH - 4}
		if row == autoEQPanel.cursor {
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 10, WithAlpha(ColorCardFocus, 255))
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 10, 1, accentColor)
		}
		label, sub := autoEQRowLabel(row)
		ink := ColorTextPrimary()
		if row >= autoEQFixedRows && label == musicAutoEQName() {
			ink = accentColor
		}
		sw, _, _ := font.SizeUTF8(sub)
		renderText(renderer, config, font, ellipsize(font, label, r.W-int32(sw)-48), ink, r.X+14, r.Y+(r.H-smallH)/2)
		renderText(renderer, config, font, sub, ColorTextTertiary(), r.X+r.W-int32(sw)-14, r.Y+(r.H-smallH)/2)
		autoEQPanel.rows = append(autoEQPanel.rows, r)
	}
	hint := hintLine(FooterHint{"A", "Use"}, FooterHint{"Y", "Search"}, FooterHint{"L1/R1", "Page"}, FooterHint{"B", "Close"})
	hw, _, _ := font.SizeUTF8(hint)
	renderText(renderer, config, font, hint, ColorTextTertiary(), card.X+(card.W-int32(hw))/2, card.Y+card.H-smallH-10)
}
