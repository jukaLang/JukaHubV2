package main

// tvchannels.go — Live TV playlist parsing, categories, search and favorites.
//
// The Live TV screen used to publish a flat list of name/URL pairs parsed by a
// two-regex reader. Long playlists (the public iptv-org index has thousands of
// channels) were then a single endless D-pad scroll. This module parses the
// playlist properly (group-title / tvg-name / #EXTGRP, quoted commas, CRLF,
// BOM), keeps the channels grouped, filters them by category and search text,
// and persists the user's favorite channels by URL so they survive a reload.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// TVChannel is one channel parsed from an M3U playlist.
type TVChannel struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	Group string `json:"group"`
}

const (
	// tvEntriesVar is the config variable the Live TV dynamiclist renders.
	tvEntriesVar = "iptv_entries"
	// tvAllCategory shows every channel; tvFavoritesCategory only starred ones.
	tvAllCategory       = "All"
	tvFavoritesCategory = "Favorites"
	// tvOtherGroup collects channels whose playlist has no group.
	tvOtherGroup = "Other"
	// tvFavoritesFileName stores starred channels next to the other app data.
	tvFavoritesFileName = "tvfavorites.json"
	// tvPublicPlaylistURL is fetched when the tv/ folder has no playlists.
	tvPublicPlaylistURL = "https://iptv-org.github.io/iptv/index.m3u"
)

// TVFavorite is a starred channel. It carries its own name/group so the
// Favorites category still lists it when the playlist file disappears.
type TVFavorite struct {
	URL   string `json:"url"`
	Name  string `json:"name"`
	Group string `json:"group"`
}

type tvLibrary struct {
	mu       sync.Mutex
	channels []TVChannel
	index    map[string]TVChannel // URL -> channel, for row metadata
	category string
	query    string
	favs     map[string]TVFavorite
	favsRead bool

	// visible is the size of the last filtered list. The filter bar draws
	// the count on every frame; recomputing it there meant copying and
	// lowercasing the whole playlist (the public iptv-org index has
	// thousands of channels) at 30-60fps. It is refreshed whenever the
	// filter changes, which is the only time the answer can change.
	visible     int
	countsValid bool
}

var tvLib tvLibrary

// tvFavoritesFilePath is a variable so tests can point the store at a temp file.
var tvFavoritesFilePath = defaultTVFavoritesFilePath

func defaultTVFavoritesFilePath() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		dir = "."
	}
	return filepath.Join(dir, tvFavoritesFileName)
}

// ---------------------------------------------------------------------------
// Playlist parsing
// ---------------------------------------------------------------------------

// parseM3UChannels extracts channels with their category from playlist text.
// It understands the forms real playlists use: CRLF line endings, a UTF-8 BOM,
// #EXTINF attributes (quoted values may contain spaces and commas), the
// #EXTGRP grouping directive, and comment/directive lines in between.
func parseM3UChannels(data string) []TVChannel {
	data = strings.TrimPrefix(data, "\ufeff")
	var out []TVChannel

	var attrs map[string]string
	name := ""
	groupHint := ""

	flush := func(url string) {
		url = strings.TrimSpace(url)
		if url == "" || !looksLikeStreamURL(url) {
			return
		}
		group := strings.TrimSpace(attrs["group-title"])
		if group == "" {
			group = strings.TrimSpace(attrs["tvg-group"])
		}
		if group == "" {
			group = groupHint
		}
		if group == "" {
			group = tvOtherGroup
		}
		display := strings.TrimSpace(name)
		if display == "" {
			display = strings.TrimSpace(attrs["tvg-name"])
		}
		if display == "" {
			display = channelNameFromURL(url)
		}
		out = append(out, TVChannel{Name: display, URL: url, Group: group})
		attrs = nil
		name = ""
		groupHint = ""
	}

	for _, raw := range strings.Split(data, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" {
			continue
		}
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "#EXTM3U"):
			continue
		case strings.HasPrefix(upper, "#EXTINF"):
			attrs, name = parseM3UInfoLine(line)
		case strings.HasPrefix(upper, "#EXTGRP:"):
			// Applies to the channel being described, or to the next one.
			groupHint = strings.TrimSpace(line[len("#EXTGRP:"):])
		case strings.HasPrefix(line, "#"):
			// #EXTVLCOPT, #KODIPROP, plain comments, ...
			continue
		default:
			// Bare URL lists (no #EXTINF) still work: the name falls back to
			// the host of the URL.
			flush(line)
		}
	}
	return dedupeTVChannels(out)
}

// parseM3UInfoLine splits an #EXTINF line into its attributes and display
// name. The name is the text after the last comma that is not inside quotes,
// so attributes like tvg-name="BBC, One" cannot truncate it.
func parseM3UInfoLine(line string) (map[string]string, string) {
	rest := line
	if i := strings.IndexByte(rest, ':'); i >= 0 {
		rest = rest[i+1:]
	}
	name := ""
	if cut := lastUnquotedComma(rest); cut >= 0 {
		name = strings.TrimSpace(rest[cut+1:])
		rest = rest[:cut]
	}
	return parseM3UAttrs(rest), name
}

// lastUnquotedComma returns the index of the last comma outside double quotes.
func lastUnquotedComma(s string) int {
	inQuotes := false
	last := -1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuotes = !inQuotes
		case ',':
			if !inQuotes {
				last = i
			}
		}
	}
	return last
}

// parseM3UAttrs reads the key="value" pairs of an #EXTINF line, skipping the
// leading duration token. Values may be quoted (and then contain spaces,
// commas or '=') or bare.
func parseM3UAttrs(s string) map[string]string {
	attrs := make(map[string]string)
	i := 0
	// Duration token ("-1", "0", ...).
	for i < len(s) && s[i] != ' ' && s[i] != '\t' {
		i++
	}
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			break
		}
		start := i
		for i < len(s) && s[i] != '=' && s[i] != ' ' && s[i] != '\t' {
			i++
		}
		if i >= len(s) || s[i] != '=' {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(s[start:i]))
		i++ // '='
		value := ""
		if i < len(s) && s[i] == '"' {
			i++
			var b strings.Builder
			for i < len(s) && s[i] != '"' {
				b.WriteByte(s[i])
				i++
			}
			if i < len(s) {
				i++ // closing quote
			}
			value = b.String()
		} else {
			vs := i
			for i < len(s) && s[i] != ' ' && s[i] != '\t' {
				i++
			}
			value = s[vs:i]
		}
		if key != "" {
			attrs[key] = value
		}
	}
	return attrs
}

// dedupeTVChannels keeps the first occurrence of each URL.
func dedupeTVChannels(list []TVChannel) []TVChannel {
	seen := make(map[string]bool, len(list))
	out := make([]TVChannel, 0, len(list))
	for _, ch := range list {
		url := strings.TrimSpace(ch.URL)
		if url == "" || seen[url] {
			continue
		}
		seen[url] = true
		name := strings.TrimSpace(ch.Name)
		if name == "" {
			name = channelNameFromURL(url)
		}
		group := strings.TrimSpace(ch.Group)
		if group == "" {
			group = tvOtherGroup
		}
		out = append(out, TVChannel{Name: name, URL: url, Group: group})
	}
	return out
}

// loadTVChannelsFromDir merges every .m3u/.m3u8 file in dir, in file-name
// order so the channel order is stable between launches.
func loadTVChannelsFromDir(dir string) []TVChannel {
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		lower := strings.ToLower(f.Name())
		if strings.HasSuffix(lower, ".m3u") || strings.HasSuffix(lower, ".m3u8") {
			names = append(names, f.Name())
		}
	}
	sort.Strings(names)
	var out []TVChannel
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		out = append(out, parseM3UChannels(string(data))...)
	}
	return dedupeTVChannels(out)
}

// loadTVPublicPlaylist reads the bundled public playlist, using the six-hour
// disk cache so a cold launch on the handheld does not always hit the network.
func loadTVPublicPlaylist() []TVChannel {
	if db, err := cacheOpen("jukahub.cache"); err == nil {
		defer db.Close()
		if cached, cErr := cacheGet(db, "iptv:"+tvPublicPlaylistURL); cErr == nil && cached != nil {
			return parseM3UChannels(string(cached))
		}
		if data := httpGetText(tvPublicPlaylistURL, 20*time.Second); data != "" {
			cacheSet(db, "iptv:"+tvPublicPlaylistURL, []byte(data), 6*time.Hour)
			return parseM3UChannels(data)
		}
		return nil
	}
	return parseM3UChannels(httpGetText(tvPublicPlaylistURL, 20*time.Second))
}

// loadIPTV reloads the playlists and republishes the filtered list. Local
// playlists in tv/ win; the public iptv-org list is the fallback.
func loadIPTV(config *Config) {
	channels := loadTVChannelsFromDir("tv")
	if len(channels) == 0 {
		channels = loadTVPublicPlaylist()
	}
	tvSetChannels(channels)
	tvSync()
	focusedFileIndex = 0
}

// ---------------------------------------------------------------------------
// Library state: categories, search, favorites
// ---------------------------------------------------------------------------

// tvSetChannels replaces the parsed library, keeping the current category when
// it still exists (a group may have vanished between reloads).
func tvSetChannels(list []TVChannel) {
	tvLib.mu.Lock()
	defer tvLib.mu.Unlock()
	tvLib.channels = dedupeTVChannels(list)
	tvLib.countsValid = false
	tvLib.index = make(map[string]TVChannel, len(tvLib.channels))
	for _, ch := range tvLib.channels {
		tvLib.index[ch.URL] = ch
	}
	tvLoadFavoritesLocked()
	if tvLib.category == "" {
		tvLib.category = tvAllCategory
	}
	if !tvCategoryExistsLocked(tvLib.category) {
		tvLib.category = tvAllCategory
	}
}

func tvCategoryExistsLocked(name string) bool {
	if name == "" || name == tvAllCategory || name == tvFavoritesCategory {
		return true
	}
	for _, ch := range tvLib.channels {
		if ch.Group == name {
			return true
		}
	}
	return false
}

// tvCategories lists the filter choices: All, Favorites, then every group in
// case-insensitive alphabetical order.
func tvCategories() []string {
	tvLib.mu.Lock()
	defer tvLib.mu.Unlock()
	cats := []string{tvAllCategory, tvFavoritesCategory}
	seen := make(map[string]bool)
	for _, ch := range tvLib.channels {
		if !seen[ch.Group] {
			seen[ch.Group] = true
			cats = append(cats, ch.Group)
		}
	}
	groups := cats[2:]
	sort.SliceStable(groups, func(i, j int) bool {
		return strings.ToLower(groups[i]) < strings.ToLower(groups[j])
	})
	return cats
}

func tvCategory() string {
	tvLib.mu.Lock()
	defer tvLib.mu.Unlock()
	if tvLib.category == "" {
		return tvAllCategory
	}
	return tvLib.category
}

// tvSetCategory switches the category. It reports whether a reload is needed.
func tvSetCategory(name string) bool {
	tvLib.mu.Lock()
	defer tvLib.mu.Unlock()
	if name == "" || !tvCategoryExistsLocked(name) {
		name = tvAllCategory
	}
	if tvLib.category == name {
		return false
	}
	tvLib.category = name
	tvLib.countsValid = false
	focusedFileIndex = 0
	return true
}

// tvCycleCategory moves to the next/previous category (wrapping).
func tvCycleCategory(delta int) {
	cats := tvCategories()
	if len(cats) == 0 {
		return
	}
	cur := tvCategory()
	idx := 0
	for i, c := range cats {
		if c == cur {
			idx = i
			break
		}
	}
	idx = (idx + delta + len(cats)) % len(cats)
	if tvSetCategory(cats[idx]) {
		tvSync()
	}
}

// tvSetQuery updates the search text and refreshes the visible list.
func tvSetQuery(q string) {
	tvLib.mu.Lock()
	q = strings.TrimSpace(q)
	if tvLib.query == q {
		tvLib.mu.Unlock()
		return
	}
	tvLib.query = q
	tvLib.countsValid = false
	tvLib.mu.Unlock()
	focusedFileIndex = 0
	tvSync()
}

func tvQuery() string {
	tvLib.mu.Lock()
	defer tvLib.mu.Unlock()
	return tvLib.query
}

// tvFilteredChannels returns the channels matching the active category and
// search text. The category is applied first, the query on top.
func tvFilteredChannels() []TVChannel {
	tvLib.mu.Lock()
	defer tvLib.mu.Unlock()
	return tvFilteredLocked()
}

// noteFilteredLocked records how many rows the current filter produces.
func tvNoteFilteredLocked(visible int) {
	tvLib.visible = visible
	tvLib.countsValid = true
}

func tvFilteredLocked() []TVChannel {
	cat := tvLib.category
	if cat == "" {
		cat = tvAllCategory
	}
	query := strings.ToLower(strings.TrimSpace(tvLib.query))
	matchesQuery := func(ch TVChannel) bool {
		if query == "" {
			return true
		}
		return strings.Contains(strings.ToLower(ch.Name), query) ||
			strings.Contains(strings.ToLower(ch.Group), query) ||
			strings.Contains(strings.ToLower(ch.URL), query)
	}

	// Favorites are listed from the star store, not from the playlist, so a
	// channel the user starred stays reachable (its stream URL still plays)
	// when the playlist file is replaced or missing.
	if cat == tvFavoritesCategory {
		out := make([]TVChannel, 0, len(tvLib.favs))
		for _, f := range tvLib.favs {
			ch := TVChannel{Name: f.Name, URL: f.URL, Group: f.Group}
			// A playlist reload may have renamed the channel or moved it to
			// another group; the live entry wins over the stored snapshot.
			if live, ok := tvLib.index[f.URL]; ok {
				ch = live
			}
			if matchesQuery(ch) {
				out = append(out, ch)
			}
		}
		sort.Slice(out, func(i, j int) bool {
			return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
		})
		tvNoteFilteredLocked(len(out))
		return out
	}

	out := make([]TVChannel, 0, len(tvLib.channels))
	for _, ch := range tvLib.channels {
		if cat != tvAllCategory && ch.Group != cat {
			continue
		}
		if !matchesQuery(ch) {
			continue
		}
		out = append(out, ch)
	}
	tvNoteFilteredLocked(len(out))
	return out
}

// tvCounts reports the visible and total channel counts for the filter bar.
// The visible count is cached: the filter bar is redrawn every frame.
func tvCounts() (visible, total int) {
	tvLib.mu.Lock()
	defer tvLib.mu.Unlock()
	if !tvLib.countsValid {
		tvFilteredLocked()
	}
	return tvLib.visible, len(tvLib.channels)
}

// tvCategoryLabel is the human label of the active category.
func tvCategoryLabel() string {
	cat := tvCategory()
	if cat == tvAllCategory {
		return "All channels"
	}
	return cat
}

// tvSync republishes the filtered list for the scene's dynamiclist and clamps
// the selection so filtering never leaves the focus past the end.
func tvSync() {
	channels := tvFilteredChannels()
	entries := make([]FileEntry, 0, len(channels))
	for _, ch := range channels {
		entries = append(entries, FileEntry{Name: ch.Name, Path: ch.URL})
	}
	publishCustom(tvEntriesVar, entries)
	if focusedFileIndex >= len(entries) {
		focusedFileIndex = len(entries) - 1
	}
	if focusedFileIndex < 0 {
		focusedFileIndex = 0
	}
}

// tvRowMeta returns the category and favorite state for a listed URL. The
// renderer calls it only for visible rows.
func tvRowMeta(url string) (group string, fav bool) {
	tvLib.mu.Lock()
	defer tvLib.mu.Unlock()
	tvLoadFavoritesLocked()
	if f, ok := tvLib.favs[url]; ok {
		return f.Group, true
	}
	if ch, ok := tvLib.index[url]; ok {
		return ch.Group, false
	}
	return "", false
}

// ---------------------------------------------------------------------------
// Favorites
// ---------------------------------------------------------------------------

func tvLoadFavoritesLocked() {
	if tvLib.favsRead {
		return
	}
	tvLib.favsRead = true
	tvLib.favs = make(map[string]TVFavorite)
	// The atomic save keeps the previous star list as .bak, so a corrupt file
	// comes back as the channels the user starred instead of an empty list.
	var list []TVFavorite
	if _, err := readJSONWithBackup(tvFavoritesFilePath(), &list); err != nil {
		return
	}
	for _, f := range list {
		url := strings.TrimSpace(f.URL)
		if url == "" || !looksLikeStreamURL(url) {
			continue
		}
		name := strings.TrimSpace(f.Name)
		if name == "" {
			name = channelNameFromURL(url)
		}
		group := strings.TrimSpace(f.Group)
		if group == "" {
			group = tvOtherGroup
		}
		tvLib.favs[url] = TVFavorite{URL: url, Name: name, Group: group}
	}
}

// saveTVFavorites writes the star list through the config's crash-safe path:
// temp file, fsync, rename, with the previous list kept as .bak so a card that
// loses the write still leaves the stars recoverable.
func saveTVFavorites() error {
	tvLib.mu.Lock()
	list := make([]TVFavorite, 0, len(tvLib.favs))
	for _, f := range tvLib.favs {
		list = append(list, f)
	}
	tvLib.mu.Unlock()
	sort.Slice(list, func(i, j int) bool {
		return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
	})
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := AtomicWrite(tvFavoritesFilePath(), data, 0o644); err != nil {
		return err
	}
	return nil
}

// tvToggleFavoriteFocused stars/unstars the highlighted channel. It reports the
// channel, the new state and whether anything was toggled.
func tvToggleFavoriteFocused() (TVChannel, bool, bool) {
	tvLib.mu.Lock()
	defer tvLib.mu.Unlock()
	tvLoadFavoritesLocked()
	list := tvFilteredLocked()
	if focusedFileIndex < 0 || focusedFileIndex >= len(list) {
		return TVChannel{}, false, false
	}
	ch := list[focusedFileIndex]
	tvLib.countsValid = false
	if _, ok := tvLib.favs[ch.URL]; ok {
		delete(tvLib.favs, ch.URL)
		return ch, false, true
	}
	tvLib.favs[ch.URL] = TVFavorite{URL: ch.URL, Name: ch.Name, Group: ch.Group}
	return ch, true, true
}

// tvFavoriteCount returns how many channels are starred.
func tvFavoriteCount() int {
	tvLib.mu.Lock()
	defer tvLib.mu.Unlock()
	tvLoadFavoritesLocked()
	return len(tvLib.favs)
}

// toggleTVFavorite is the input-facing wrapper: it saves the change and tells
// the user what happened.
func toggleTVFavorite() {
	ch, nowFav, ok := tvToggleFavoriteFocused()
	if !ok {
		return
	}
	if err := saveTVFavorites(); err != nil {
		ShowToast("Could not save favorites: "+firstLine(err.Error()), ToastKindError)
		return
	}
	PlayActivateSound()
	if nowFav {
		ShowToast("Added "+ch.Name+" to favorites", ToastKindSuccess)
	} else {
		ShowToast("Removed "+ch.Name+" from favorites", ToastKindInfo)
	}
	// The Favorites category must drop the row immediately.
	tvSync()
}

// ---------------------------------------------------------------------------
// List input
// ---------------------------------------------------------------------------

// tvListKeyAction handles Live TV-only keys while the channel list is focused:
// Left/Right switch category, X opens the search keyboard, Y stars a channel,
// F is an alternative favorite key for hardware keyboards.
func tvListKeyAction(sym sdl.Keycode, config *Config) bool {
	switch sym {
	case sdl.K_LEFT:
		tvCycleCategory(-1)
		PlayNavSound()
		return true
	case sdl.K_RIGHT:
		tvCycleCategory(1)
		PlayNavSound()
		return true
	case sdl.K_x:
		vkOpenTVSearch()
		return true
	case sdl.K_y, sdl.K_f:
		toggleTVFavorite()
		return true
	}
	return false
}

// tvListControllerAction mirrors tvListKeyAction for the D-pad and face buttons.
func tvListControllerAction(btn uint8, config *Config) bool {
	switch btn {
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		tvCycleCategory(-1)
		PlayNavSound()
		return true
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		tvCycleCategory(1)
		PlayNavSound()
		return true
	case sdl.CONTROLLER_BUTTON_X:
		vkOpenTVSearch()
		return true
	case sdl.CONTROLLER_BUTTON_Y:
		toggleTVFavorite()
		return true
	}
	return false
}

// --- Mouse / touch targets, filled while the filter bar renders ---

var (
	tvPrevRect   sdl.Rect
	tvNextRect   sdl.Rect
	tvSearchRect sdl.Rect
)

// handleTVFilterMouse activates the filter bar widgets. It only reacts while
// the Live TV list is on screen, so a stale rect can never eat a click.
func handleTVFilterMouse(mx, my int32, config *Config) bool {
	if currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return false
	}
	if config.Scenes[currentSceneIndex].Name != "LiveTV" {
		return false
	}
	if tvPrevRect.W > 0 && pointInRect(mx, my, tvPrevRect) {
		tvCycleCategory(-1)
		PlayNavSound()
		return true
	}
	if tvNextRect.W > 0 && pointInRect(mx, my, tvNextRect) {
		tvCycleCategory(1)
		PlayNavSound()
		return true
	}
	if tvSearchRect.W > 0 && pointInRect(mx, my, tvSearchRect) {
		vkOpenTVSearch()
		return true
	}
	return false
}

// tvFilterBarH is the height the channel list must leave for the filter bar.
const tvFilterBarH = int32(46)

// renderTVFilterBar draws the category selector, channel counts, search state
// and key hints above the channel list. It returns the bar height.
func renderTVFilterBar(renderer *sdl.Renderer, config *Config, x, y, w int32) int32 {
	font, _ := getCachedFont(config, "small")
	tvPrevRect, tvNextRect, tvSearchRect = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
	if font == nil {
		return tvFilterBarH
	}

	fillRoundedRect(renderer, x, y, w, tvFilterBarH, 10, ColorCard)
	strokeRoundedRect(renderer, x, y, w, tvFilterBarH, 10, 1, ColorBorder)

	btn := int32(28)
	by := y + (tvFilterBarH-btn)/2
	textY := y + (tvFilterBarH-int32(font.Height()))/2

	tvPrevRect = sdl.Rect{X: x + 8, Y: by, W: btn, H: btn}
	tvDrawBarButton(renderer, config, font, "<", tvPrevRect)

	// Right side: the search state doubles as a touch target for the search
	// keyboard, so it is measured before the category label is placed.
	hint := hintLine(FooterHint{"X", "Search"}, FooterHint{"Y", "Favorite"})
	if q := tvQuery(); q != "" {
		hint = hintLine(FooterHint{Label: "Filter: " + ellipsize(font, q, 200)}, FooterHint{"X", "Change"})
	}
	hw, _, _ := font.SizeUTF8(hint)
	hintX := x + w - int32(hw) - 12
	hintVisible := hintX > tvPrevRect.X+btn+120
	if hintVisible {
		tvSearchRect = sdl.Rect{X: hintX - 6, Y: y + 6, W: int32(hw) + 12, H: tvFilterBarH - 12}
		renderText(renderer, config, font, hint, ColorTextTertiary(), hintX, textY)
	}

	maxLabelW := x + w - 40
	if hintVisible {
		maxLabelW = hintX - 16
	}
	label := ellipsize(font, tvBarLabel(), maxLabelW-tvPrevRect.X-btn-40)
	labelX := tvPrevRect.X + btn + 10
	col := ColorTextPrimary()
	if tvCategory() == tvFavoritesCategory {
		col = ColorTextAccent()
	}
	renderText(renderer, config, font, label, col, labelX, textY)
	lw, _, _ := font.SizeUTF8(label)

	tvNextRect = sdl.Rect{X: labelX + int32(lw) + 10, Y: by, W: btn, H: btn}
	tvDrawBarButton(renderer, config, font, ">", tvNextRect)
	return tvFilterBarH
}

// tvDrawBarButton draws one of the small category-arrow pills.
func tvDrawBarButton(renderer *sdl.Renderer, config *Config, font *ttf.Font, glyph string, r sdl.Rect) {
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 8, ColorIconSurface)
	gw, gh, _ := font.SizeUTF8(glyph)
	renderText(renderer, config, font, glyph, ColorTextSecondary(),
		r.X+(r.W-int32(gw))/2, r.Y+(r.H-int32(gh))/2)
}

// tvBarLabel is the text shown between the category arrows.
func tvBarLabel() string {
	visible, total := tvCounts()
	label := tvCategoryLabel()
	if total > 0 && visible != total {
		return fmt.Sprintf("%s  %d/%d", label, visible, total)
	}
	return fmt.Sprintf("%s  %d", label, visible)
}
