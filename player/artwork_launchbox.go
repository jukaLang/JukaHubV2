package main

// artwork_launchbox.go — box art from the LaunchBox Games Database, and the
// order the cover sources are asked in.
//
// The libretro thumbnail server names covers after No-Intro titles, so a game
// it does not carry (a hack, a homebrew, a rare release, a translation) kept a
// blank card however often the fetch ran. The LaunchBox Games Database
// (gamesdb.launchbox-app.com) is a large, community-kept collection with a
// front box for most retail games. It is now the second automatic source: a
// game the thumbnail server has nothing for (os.ErrNotExist from
// fetchROMArtworkFor) is looked up there before it is filed as a miss. Choose
// cover (artwork_choose.go) also gets a "Search LaunchBox…" row: a title typed
// on the keyboard, the matches listed with their picture, and the one picked
// saved the way any chosen cover is.
//
// LaunchBox has no public JSON API, so this reads the public website the way
// a browser does, defensively: the search page (/games/results/<title>) for
// links to /games/details/<id>-<slug> with the platform named near each, and
// a game's image page (/games/images/<id>) for pictures on
// images.launchbox-app.com captioned "Box - Front (<region>)". Only those two
// hosts are asked; pages are capped at launchBoxPageMax, pictures at
// artworkImageMaxBytes; at most one request a second (launchBoxGate); a title
// LaunchBox has no cover for is not asked again for launchBoxMissTTL
// (launchBoxMisses, data/game-art/.launchbox-missing.json; deleting the
// game-art folder starts it over). A picture is stored as the PNG the game-art
// folder keeps, at most the size the library draws (artworkAsCoverPNG).
//
// The order (fetchROMCoverAllSources): ScreenScraper first when the user has
// signed in (artwork_screenscraper.go), then the libretro thumbnails, then
// LaunchBox. The extra sources are asked only for the public thumbnail
// server's address: a test or a mirror never reaches them.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// launchBoxSite is the Games Database's website.
	launchBoxSite = "https://gamesdb.launchbox-app.com"
	// launchBoxImageHost serves its pictures.
	launchBoxImageHost = "images.launchbox-app.com"
	// launchBoxPageMax bounds one page of HTML.
	launchBoxPageMax = 3 << 20
	// launchBoxTimeout bounds one request.
	launchBoxTimeout = 25 * time.Second
	// launchBoxGap is the least time between two requests.
	launchBoxGap = time.Second
	// launchBoxMissTTL is how long a title without a cover is not asked again.
	launchBoxMissTTL = 30 * 24 * time.Hour
	// launchBoxMissFile is the not-found cache, beside the covers.
	launchBoxMissFile = ".launchbox-missing.json"
	// launchBoxMaxResults bounds the matches one search lists.
	launchBoxMaxResults = 12
	// launchBoxTitleMax bounds a search title.
	launchBoxTitleMax = 120
)

// launchBoxPlatforms names each libretro collection's LaunchBox platform.
var launchBoxPlatforms = map[string]string{
	"Nintendo - Nintendo Entertainment System":       "Nintendo Entertainment System",
	"Nintendo - Family Computer Disk System":         "Nintendo Famicom Disk System",
	"Nintendo - Super Nintendo Entertainment System": "Super Nintendo Entertainment System",
	"Nintendo - Game Boy":                            "Nintendo Game Boy",
	"Nintendo - Game Boy Color":                      "Nintendo Game Boy Color",
	"Nintendo - Game Boy Advance":                    "Nintendo Game Boy Advance",
	"Nintendo - Nintendo 64":                         "Nintendo 64",
	"Nintendo - Nintendo DS":                         "Nintendo DS",
	"Nintendo - Nintendo 3DS":                        "Nintendo 3DS",
	"Nintendo - GameCube":                            "Nintendo GameCube",
	"Nintendo - Wii":                                 "Nintendo Wii",
	"Nintendo - Virtual Boy":                         "Nintendo Virtual Boy",
	"Nintendo - Pokemon Mini":                        "Nintendo Pokemon Mini",
	"Sega - Mega Drive - Genesis":                    "Sega Genesis",
	"Sega - Master System - Mark III":                "Sega Master System",
	"Sega - Game Gear":                               "Sega Game Gear",
	"Sega - Mega-CD - Sega CD":                       "Sega CD",
	"Sega - 32X":                                     "Sega 32X",
	"Sega - Saturn":                                  "Sega Saturn",
	"Sega - Dreamcast":                               "Sega Dreamcast",
	"Sega - SG-1000":                                 "Sega SG-1000",
	"Sony - PlayStation":                             "Sony Playstation",
	"Sony - PlayStation 2":                           "Sony Playstation 2",
	"Sony - PlayStation Portable":                    "Sony PSP",
	"NEC - PC Engine - TurboGrafx 16":                "NEC TurboGrafx-16",
	"NEC - PC Engine CD - TurboGrafx-CD":             "NEC TurboGrafx-CD",
	"SNK - Neo Geo":                                  "SNK Neo Geo AES",
	"SNK - Neo Geo CD":                               "SNK Neo Geo CD",
	"SNK - Neo Geo Pocket":                           "SNK Neo Geo Pocket",
	"SNK - Neo Geo Pocket Color":                     "SNK Neo Geo Pocket Color",
	"Atari - 2600":                                   "Atari 2600",
	"Atari - 5200":                                   "Atari 5200",
	"Atari - 7800":                                   "Atari 7800",
	"Atari - Jaguar":                                 "Atari Jaguar",
	"Atari - Lynx":                                   "Atari Lynx",
	"Bandai - WonderSwan":                            "WonderSwan",
	"Bandai - WonderSwan Color":                      "WonderSwan Color",
	"Microsoft - Xbox":                               "Microsoft Xbox",
	"Microsoft - Xbox 360":                           "Microsoft Xbox 360",
	"MAME":                                           "Arcade",
	"FBNeo - Arcade Games":                           "Arcade",
	"Coleco - ColecoVision":                          "ColecoVision",
	"GCE - Vectrex":                                  "GCE Vectrex",
	"Mattel - Intellivision":                         "Mattel Intellivision",
}

// launchBoxOtherPlatforms are platforms whose names begin with one above
// ("Sony Playstation 3", "Nintendo DSi"): known, so a result on them is not
// taken for the shorter one.
var launchBoxOtherPlatforms = []string{
	"Sony Playstation 3", "Sony Playstation 4", "Sony Playstation 5", "Sony Playstation Vita",
	"Sony PSP Minis", "Nintendo DSi", "Nintendo Wii U", "Nintendo Switch", "Nintendo 64DD",
	"Microsoft Xbox One", "Microsoft Xbox Series X/S", "Sega CD 32X", "SNK Neo Geo MVS",
	"Sega Genesis 32X", "Windows", "MS-DOS",
}

// launchBoxResult is one match of a search.
type launchBoxResult struct {
	ID       string // the database's game id
	Title    string
	Platform string // "" when the page did not say
	Thumb    string // a picture of it on the image host, "" when none
}

// launchBoxImage is one front box on a game's image page.
type launchBoxImage struct {
	URL    string
	Region string // as captioned: "North America", "Europe", "Japan", "World" …
}

// ── Requests ────────────────────────────────────────────────────────────────

// launchBoxGate spaces the requests: at most one every launchBoxGap, from
// every goroutine together (the cover fetch runs several workers).
var launchBoxGate struct {
	sync.Mutex
	last time.Time
}

// launchBoxGet fetches one page or picture. A 404 is os.ErrNotExist; another
// failure is returned as is (not a miss).
func launchBoxGet(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || (u.Host != "gamesdb.launchbox-app.com" && u.Host != launchBoxImageHost) {
		return nil, fmt.Errorf("launchbox: refusing %q", rawURL)
	}
	launchBoxGate.Lock()
	if wait := launchBoxGap - time.Since(launchBoxGate.last); !launchBoxGate.last.IsZero() && wait > 0 {
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			launchBoxGate.Unlock()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
	launchBoxGate.last = time.Now()
	launchBoxGate.Unlock()

	rctx, cancel := context.WithTimeout(ctx, launchBoxTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "JukaHub")
	resp, err := artworkHTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, os.ErrNotExist
	case resp.StatusCode != http.StatusOK:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil, fmt.Errorf("launchbox returned %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errArtworkTooBig
	}
	return data, nil
}

// launchBoxSearch lists the database's games titled like title.
func launchBoxSearch(ctx context.Context, title string) ([]launchBoxResult, error) {
	title = strings.Join(strings.Fields(title), " ")
	if runes := []rune(title); len(runes) > launchBoxTitleMax {
		title = string(runes[:launchBoxTitleMax])
	}
	if title == "" {
		return nil, os.ErrNotExist
	}
	esc := url.PathEscape(title)
	page, err := launchBoxGet(ctx, launchBoxSite+"/games/results/"+esc+"?searchText="+url.QueryEscape(title), launchBoxPageMax)
	if err != nil {
		return nil, err
	}
	return parseLaunchBoxResults(page), nil
}

// launchBoxFronts lists a game's front boxes, best first for romTags (the
// ROM's region tags).
func launchBoxFronts(ctx context.Context, id string, romTags []string) ([]launchBoxImage, error) {
	if id == "" || strings.Trim(id, "0123456789") != "" {
		return nil, os.ErrNotExist
	}
	page, err := launchBoxGet(ctx, launchBoxSite+"/games/images/"+id, launchBoxPageMax)
	if err != nil {
		return nil, err
	}
	imgs := parseLaunchBoxImages(page)
	launchBoxSortRegions(imgs, romTags)
	return imgs, nil
}

// launchBoxPicture downloads a picture from the image host as a cover PNG.
func launchBoxPicture(ctx context.Context, rawURL string) ([]byte, error) {
	data, err := launchBoxGet(ctx, rawURL, artworkImageMaxBytes)
	if errors.Is(err, errArtworkTooBig) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, err
	}
	return artworkAsCoverPNG(ctx, data)
}

// artworkAsCoverPNG is a downloaded picture as the PNG the game-art folder
// keeps: a PNG no larger than the library draws as it is, anything else
// (JPEG, a bigger PNG, WebP) decoded, scaled to libraryArtDecodeMaxEdge and
// encoded. Not a picture is os.ErrNotExist. Off the render thread.
func artworkAsCoverPNG(ctx context.Context, data []byte) ([]byte, error) {
	if artworkIsPNG(data) {
		if cfg, err := png.DecodeConfig(bytes.NewReader(data)); err == nil && max(cfg.Width, cfg.Height) <= libraryArtDecodeMaxEdge {
			return data, nil
		}
	} else if !artworkIsJPEG(data) && !artworkIsWebP(data) {
		return nil, os.ErrNotExist
	}
	pic, err := artChooseDecodeBytes(ctx, data)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, os.ErrNotExist
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, pic); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ── Reading the pages ───────────────────────────────────────────────────────

var (
	launchBoxDetailsRe = regexp.MustCompile(`/games/details/([0-9]{1,10})(-[A-Za-z0-9-]{0,200})?`)
	launchBoxImageRe   = regexp.MustCompile(`https://images\.launchbox-app\.com/[A-Za-z0-9_./-]{1,200}\.(?:jpg|jpeg|png|JPG|JPEG|PNG)`)
	launchBoxTagRe     = regexp.MustCompile(`(?s)<[^>]{0,1000}>`) // RE2 counts stop at 1000
	launchBoxTitleRe   = regexp.MustCompile(`(?is)<(h[1-6]|strong|b)\b[^>]{0,400}>(.{1,400}?)</(?:h[1-6]|strong|b)>`)
	launchBoxAttrRe    = regexp.MustCompile(`(?i)\b(?:title|alt|data-title|aria-label)="([^"]{1,300})"`)
)

// launchBoxText is a piece of HTML as plain text: tags dropped, entities
// decoded, spaces folded.
func launchBoxText(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(launchBoxTagRe.ReplaceAllString(s, " "))), " ")
}

// parseLaunchBoxResults reads a search page: each link to a game's details
// starts a result that runs to the next one. Its title is the first heading
// or bold text in it, else a title attribute, else the link's slug; its
// platform the longest platform name its text holds; its picture the first
// from the image host.
func parseLaunchBoxResults(page []byte) []launchBoxResult {
	text := string(page)
	locs := launchBoxDetailsRe.FindAllStringSubmatchIndex(text, -1)
	var out []launchBoxResult
	seen := map[string]bool{}
	for i, loc := range locs {
		id := text[loc[2]:loc[3]]
		if seen[id] {
			continue
		}
		end := len(text)
		for _, next := range locs[i+1:] {
			if next[0] >= loc[0]+6000 {
				break // past the block's cap: no quadratic scan of a hostile page
			}
			if text[next[2]:next[3]] != id {
				end = next[0]
				break
			}
		}
		start := loc[0]
		end = min(end, start+6000)
		block := text[start:end]
		r := launchBoxResult{ID: id}
		if m := launchBoxTitleRe.FindStringSubmatch(block); m != nil {
			r.Title = launchBoxText(m[2])
		}
		if r.Title == "" {
			if m := launchBoxAttrRe.FindStringSubmatch(block); m != nil {
				r.Title = launchBoxText(m[1])
			}
		}
		if r.Title == "" && loc[4] >= 0 {
			r.Title = strings.TrimSpace(strings.ReplaceAll(text[loc[4]+1:loc[5]], "-", " "))
		}
		if r.Title == "" {
			continue
		}
		r.Platform = launchBoxPlatformIn(launchBoxText(block))
		if img := launchBoxImageRe.FindString(block); img != "" {
			r.Thumb = img
		}
		seen[id] = true
		out = append(out, r)
		if len(out) >= launchBoxMaxResults*2 {
			break
		}
	}
	return out
}

// launchBoxPlatformIn is the longest known platform name text holds as whole
// words, "" for none.
func launchBoxPlatformIn(text string) string {
	lower := strings.ToLower(text)
	best := ""
	check := func(name string) {
		if len(name) <= len(best) {
			return
		}
		n := strings.ToLower(name)
		for from := 0; from < len(lower); {
			i := strings.Index(lower[from:], n)
			if i < 0 {
				return
			}
			i += from
			after := i + len(n)
			if (i == 0 || !launchBoxWordByte(lower[i-1])) && (after >= len(lower) || !launchBoxWordByte(lower[after])) {
				best = name
				return
			}
			from = i + 1
		}
	}
	for _, name := range launchBoxPlatforms {
		check(name)
	}
	for _, name := range launchBoxOtherPlatforms {
		check(name)
	}
	return best
}

func launchBoxWordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
}

// launchBoxFrontRe is a front box's caption and its region: "Box - Front
// (North America)"; the reconstructed and fan-art variants are other types
// (parseLaunchBoxImages leaves them out). launchBoxCaptionRe finds a caption
// as text.
var (
	launchBoxFrontRe   = regexp.MustCompile(`Box - Front(?: \(([^)]{1,60})\))?`)
	launchBoxCaptionRe = regexp.MustCompile(`Box - Front[^<"]{0,80}`)
)

// parseLaunchBoxImages reads a game's image page for its front boxes. A
// caption and its picture sit in one tile: each picture link's tile runs from
// the tag that holds it to the next picture link, and a tile captioned "Box -
// Front" (in an attribute or as text) gives one image. A page laid out some
// other way falls back to the picture nearest each caption.
func parseLaunchBoxImages(page []byte) []launchBoxImage {
	text := string(page)
	var out []launchBoxImage
	seen := map[string]bool{}
	add := func(u, caption string) {
		if seen[u] {
			return
		}
		m := launchBoxFrontRe.FindStringSubmatch(caption)
		if m == nil || strings.Contains(caption, "Fanart - Box - Front") || strings.Contains(caption, "Box - Front Reconstructed") {
			return
		}
		seen[u] = true
		out = append(out, launchBoxImage{URL: u, Region: strings.TrimSpace(m[1])})
	}
	locs := launchBoxImageRe.FindAllStringIndex(text, 4000) // bounded work on any page
	for i, loc := range locs {
		start := strings.LastIndexByte(text[max(0, loc[0]-600):loc[0]], '<')
		if start < 0 {
			start = loc[0]
		} else {
			start += max(0, loc[0]-600)
		}
		end := len(text)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		end = min(end, loc[1]+1500)
		tile := html.UnescapeString(text[start:end])
		if strings.Contains(tile, "Box - Front") {
			add(text[loc[0]:loc[1]], tile)
		}
	}
	if len(out) > 0 {
		return out
	}
	for _, c := range launchBoxCaptionRe.FindAllStringIndex(text, 200) {
		best, dist := "", 1<<30
		for _, loc := range locs {
			d := loc[0] - c[0]
			if d < 0 {
				d = c[0] - loc[1]
			}
			if d >= 0 && d < dist && d < 1500 {
				best, dist = text[loc[0]:loc[1]], d
			}
		}
		if best != "" {
			add(best, text[max(0, c[0]-2):c[1]])
		}
	}
	return out
}

// launchBoxRegionTags maps a caption's region to the ROM tags that want it.
var launchBoxRegionTags = map[string][]string{
	"north america": {"usa", "canada"}, "united states": {"usa"}, "usa": {"usa"},
	"europe": {"europe"}, "united kingdom": {"uk", "europe"}, "japan": {"japan"},
	"world": {"world"}, "france": {"france"}, "germany": {"germany"}, "spain": {"spain"},
	"italy": {"italy"}, "korea": {"korea"}, "brazil": {"brazil"}, "australia": {"australia"},
}

// launchBoxSortRegions puts the front boxes the ROM's region wants first,
// then North America, World, Europe, Japan, then the rest (stable).
func launchBoxSortRegions(imgs []launchBoxImage, romTags []string) {
	want := map[string]bool{}
	for _, t := range romTags {
		want[strings.ToLower(t)] = true
	}
	rank := func(region string) int {
		r := strings.ToLower(region)
		for _, t := range launchBoxRegionTags[r] {
			if want[t] {
				return 0
			}
		}
		switch r {
		case "north america", "united states", "usa":
			return 1
		case "world", "":
			return 2
		case "europe":
			return 3
		case "japan":
			return 4
		}
		return 5
	}
	sort.SliceStable(imgs, func(a, b int) bool { return rank(imgs[a].Region) < rank(imgs[b].Region) })
}

// ── The automatic source ────────────────────────────────────────────────────

// artworkSourceMisses is a source's not-found cache: lookup keys with the time
// the source had nothing, in a small JSON file beside the covers. A wrong
// clock only costs a lookup asked again. Any goroutine.
type artworkSourceMisses struct {
	mu     sync.Mutex
	file   string
	loaded bool
	m      map[string]time.Time
	dirty  int
}

func (c *artworkSourceMisses) path() string {
	dir := romArtworkDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, c.file)
}

func (c *artworkSourceMisses) loadLocked() {
	if c.loaded {
		return
	}
	c.loaded = true
	c.m = map[string]time.Time{}
	path := c.path()
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 4<<20 {
		return
	}
	var stored map[string]time.Time
	if json.Unmarshal(raw, &stored) == nil {
		for k, at := range stored {
			if len(c.m) < 20000 {
				c.m[k] = at
			}
		}
	}
}

// recent reports a lookup the source had nothing for within ttl.
func (c *artworkSourceMisses) recent(key string, ttl time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadLocked()
	at, ok := c.m[key]
	if !ok {
		return false
	}
	age := time.Since(at)
	return age >= 0 && age < ttl
}

// note remembers a miss; forget drops one. Every 20 changes reach the card
// (flush writes the rest at the end of a fetch).
func (c *artworkSourceMisses) note(key string, miss bool) {
	c.mu.Lock()
	c.loadLocked()
	if miss {
		c.m[key] = time.Now()
	} else if _, ok := c.m[key]; ok {
		delete(c.m, key)
	} else {
		c.mu.Unlock()
		return
	}
	c.dirty++
	save := c.dirty >= 20
	c.mu.Unlock()
	if save {
		c.flush()
	}
}

// flush writes the cache when it changed, dropping entries long expired.
func (c *artworkSourceMisses) flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dirty == 0 {
		return
	}
	c.dirty = 0
	path := c.path()
	if path == "" {
		return
	}
	kept := make(map[string]time.Time, len(c.m))
	for k, at := range c.m {
		if age := time.Since(at); age < 4*launchBoxMissTTL {
			kept[k] = at
		}
	}
	raw, err := json.Marshal(kept)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if err := AtomicWrite(path, raw, 0o644); err != nil {
		logWarn("art", "cannot save %s: %v", filepath.Base(path), err)
	}
}

// launchBoxMisses is LaunchBox's not-found cache.
var launchBoxMisses = &artworkSourceMisses{file: launchBoxMissFile}

// launchBoxMatch picks the result for a title on platform: the exact title
// among the platform's results, else the closest (artwork_closest.go). When no
// result names any platform (the page changed), only an exact title counts.
func launchBoxMatch(results []launchBoxResult, platform, title string) (launchBoxResult, bool) {
	var cands []launchBoxResult
	named := false
	for _, r := range results {
		if r.Platform != "" {
			named = true
		}
		if strings.EqualFold(r.Platform, platform) {
			cands = append(cands, r)
		}
	}
	if !named {
		cands = results
	}
	if len(cands) == 0 {
		return launchBoxResult{}, false
	}
	names := make([]string, len(cands))
	for i, r := range cands {
		names[i] = r.Title
	}
	idx := newArtworkIndex(names)
	name, ok := idx.match(title)
	if !ok && named {
		name, ok = idx.closest(title)
	}
	if !ok {
		return launchBoxResult{}, false
	}
	for _, r := range cands {
		if r.Title == name {
			return r, true
		}
	}
	return launchBoxResult{}, false
}

// fetchLaunchBoxCover finds a game's front box on LaunchBox: os.ErrNotExist
// when LaunchBox has none (remembered for launchBoxMissTTL), another error
// when it could not be asked.
func fetchLaunchBoxCover(ctx context.Context, rom scannedROM, stem string) ([]byte, error) {
	platform := launchBoxPlatforms[strings.TrimSpace(rom.Collection)]
	lookup := artworkLookupName(stem)
	if bare, numbered := artworkStripReleaseNumber(lookup); numbered {
		lookup = bare
	}
	title := stripArtworkTagGroups(lookup)
	if platform == "" || strings.TrimSpace(title) == "" {
		return nil, os.ErrNotExist
	}
	key := rom.Collection + "/" + lookup
	if launchBoxMisses.recent(key, launchBoxMissTTL) {
		return nil, os.ErrNotExist
	}
	results, err := launchBoxSearch(ctx, title)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	pick, ok := launchBoxMatch(results, platform, lookup)
	if !ok {
		launchBoxMisses.note(key, true)
		return nil, os.ErrNotExist
	}
	data, err := launchBoxCoverFor(ctx, pick, artworkTags(stem))
	switch {
	case err == nil:
		logInfo("art", "%s: cover from LaunchBox (%s, game %s)", stem, pick.Title, pick.ID)
		launchBoxMisses.note(key, false)
		return data, nil
	case errors.Is(err, os.ErrNotExist):
		launchBoxMisses.note(key, true)
	}
	return nil, err
}

// launchBoxCoverFor downloads a result's front box: the image page's best
// region, else the search's picture of it.
func launchBoxCoverFor(ctx context.Context, r launchBoxResult, romTags []string) ([]byte, error) {
	fronts, err := launchBoxFronts(ctx, r.ID, romTags)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for i, img := range fronts {
		if i >= 3 {
			break
		}
		data, err := launchBoxPicture(ctx, img.URL)
		if err == nil {
			return data, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	if r.Thumb != "" {
		return launchBoxPicture(ctx, r.Thumb)
	}
	return nil, os.ErrNotExist
}

// ── The order of the sources ────────────────────────────────────────────────

// fetchROMCoverAllSources is fetchROMArtworkFor with the other sources around
// it: ScreenScraper first when the user signed in, then the libretro
// thumbnails, then LaunchBox for a game they have no cover for. Its errors
// mean what fetchROMArtworkFor's do: os.ErrNotExist is a real miss, anything
// else a source that could not be asked.
func fetchROMCoverAllSources(ctx context.Context, client *http.Client, baseURL string, rom scannedROM, stem string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	_, public := artworkPlainBase(strings.TrimRight(baseURL, "/"))
	if public {
		if data, err := fetchScreenScraperCover(ctx, rom, stem); err == nil {
			return data, nil
		}
	}
	data, err := fetchROMArtworkFor(ctx, client, baseURL, rom, stem)
	if err == nil || !public || !errors.Is(err, os.ErrNotExist) || ctx.Err() != nil {
		return data, err
	}
	data, lbErr := fetchLaunchBoxCover(ctx, rom, stem)
	if lbErr == nil {
		return data, nil
	}
	if errors.Is(lbErr, os.ErrNotExist) {
		return nil, os.ErrNotExist
	}
	logDebug("art", "%s: LaunchBox: %v", stem, lbErr)
	return nil, lbErr
}

// artworkSourcesFlush writes the extra sources' not-found caches; the fetch
// loop calls it when it ends.
func artworkSourcesFlush() {
	launchBoxMisses.flush()
	screenScraperMisses.flush()
}
