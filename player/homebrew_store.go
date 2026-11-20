package main

// homebrew_store.go — Get Homebrew (Start → Games → Get Homebrew): new games
// for twelve consoles from itch.io, in one list.
//
// Homebrew is alive on itch.io: Game Boy games made with GB Studio, NES and
// Mega Drive releases with cartridge runs, PICO-8 and TIC-80 carts, RPG Maker
// 2000/2003 games for EasyRPG (homebrew_tic80.go, homebrew_easyrpg.go). JukaHub's Games
// library plays them all, but getting one onto a shelf meant a PC, a browser
// and a card reader. This screen is the shop window and the delivery:
//
//   - The list is itch.io's own browse pages for each console, read through
//     their RSS twins (homebrew_store_itch.go) — several tags per console
//     ("tag-game-boy", "made-with-gb-studio" …) merged and de-duplicated by
//     game page, so a game tagged twice shows once. Console chips (All and
//     the twelve), a sort (Popular, Newest, Top rated, Free, Yours — the games
//     the player bought or installed) and a search over what is loaded. The
//     Newest, Top rated and Free lists are itch.io's own orderings, fetched
//     the first time that sort is picked. Feeds are saved under
//     data/homebrew-store/feeds for homebrewFeedTTL, so a second visit is
//     instant and offline shows the saved lists with their date.
//   - A opens the game's page: cover, title, author, price (Free, a price,
//     Bought, Installed, Update available), the description as text, and
//     Install / Update / Play / Delete. A paid game the player does not own
//     shows "Buy on itch.io" with a code to its page: JukaHub never works
//     around a payment.
//   - Installing (homebrew_store_install.go) needs the player's own itch.io
//     API key (homebrew_store_key.go: typed here or sent from the phone). It
//     asks itch.io for the game's files, picks the cartridge for the console,
//     downloads it (from a .zip when it comes in one), puts it in that
//     console's Roms folder under a clean name with its cover where the
//     library looks for covers, and the game is on its shelf.
//   - Updates: what was installed is remembered with the file's id, date,
//     md5 and build. Opening the store, and once a day in the background,
//     compares that with itch.io; an update replaces the file under the same
//     name, so its saves still match.
//
// Threading is the Ports Store's (ports_store.go): this file's state belongs
// to the main thread; every network or disk job runs on a goroutine and posts
// its result back with tbxPost.

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	homebrewSceneName = "GetHomebrew"
	homebrewEntryName = "Get Homebrew"
	homebrewGlyph     = "cartridge"

	// homebrewFeedTTL is how long a saved feed is fresh.
	homebrewFeedTTL = 12 * time.Hour
	// homebrewFeedRetry is how soon lists that failed are read again.
	homebrewFeedRetry = 10 * time.Minute
	// homebrewFeedMax bounds one feed's download.
	homebrewFeedMax = 4 << 20
	// homebrewFeedWorkers is how many feeds load at once.
	homebrewFeedWorkers = 4
	// homebrewOwnedTTL is how long the owned-games list is fresh.
	homebrewOwnedTTL = time.Hour
	// homebrewUserAgent is the real User-Agent itch.io asks clients for.
	homebrewUserAgent = "JukaHub-GetHomebrew/1.0 (+https://jukalang.com)"
)

// hbConsole is one console of the store.
type hbConsole struct {
	id    string
	name  string // the Games library's system name (romSystemAliases)
	label string // the chip
	glyph string
	// feeds are itch.io browse filters; each becomes
	// https://itch.io/games/<sort>/<filter>.xml.
	feeds []string
	exts  []string // ROM extensions, lower case
	romm  string   // rommSystems id (its Roms folder name); "" is PICO-8 or hbFolderConsoles
}

// hbConsoles are the twelve consoles. The index is a bit in hbItem.consoles.
var hbConsoles = []hbConsole{
	{"gb", "Game Boy", "Game Boy", "emu-gb", []string{"tag-game-boy", "tag-gameboy", "made-with-gb-studio"}, []string{".gb"}, "gb"},
	{"gbc", "Game Boy Color", "GB Color", "emu-gbc", []string{"tag-game-boy-color", "tag-gameboy-color", "tag-gbc"}, []string{".gbc"}, "gbc"},
	{"gba", "Game Boy Advance", "GB Advance", "emu-gba", []string{"tag-game-boy-advance", "tag-gameboy-advance", "tag-gba"}, []string{".gba"}, "gba"},
	{"nes", "NES", "NES", "emu-nes", []string{"tag-nes", "tag-nintendo-entertainment-system", "tag-famicom"}, []string{".nes"}, "nes"},
	{"snes", "SNES", "SNES", "emu-snes", []string{"tag-snes", "tag-super-nintendo", "tag-super-famicom"}, []string{".sfc", ".smc"}, "snes"},
	{"md", "Mega Drive", "Mega Drive", "emu-genesis", []string{"tag-sega-genesis", "tag-mega-drive", "tag-genesis"}, []string{".md", ".gen", ".smd", ".bin"}, "md"},
	{"sms", "Master System", "Master System", "emu-sms", []string{"tag-sega-master-system", "tag-master-system", "tag-sms"}, []string{".sms"}, "sms"},
	{"gg", "Game Gear", "Game Gear", "emu-gg", []string{"tag-game-gear", "tag-sega-game-gear", "tag-gamegear"}, []string{".gg"}, "gg"},
	{"pce", "PC Engine", "PC Engine", "emu-pce", []string{"tag-pc-engine", "tag-turbografx-16", "tag-pcengine"}, []string{".pce"}, "pce"},
	{"pico8", pico8SystemName, "PICO-8", "rom", []string{"tag-pico-8", "made-with-pico-8"}, []string{".p8.png", ".p8"}, ""},
	// Fantasy-console carts and RPG Maker 2000/2003 folders
	// (homebrew_tic80.go, homebrew_easyrpg.go).
	{hbTIC80ID, hbTIC80System, "TIC-80", "rom", []string{"made-with-tic-80", "tag-tic-80", "tag-tic80"}, []string{".tic"}, ""},
	{hbEasyRPGID, hbEasyRPGSystem, "RPG Maker", "rom", []string{"made-with-easyrpg", "tag-rpg-maker-2003", "tag-rpg-maker-2000", "tag-rm2k3"}, []string{".easyrpg"}, ""},
}

// hbSort orders (and for Free and Yours, filters) the list.
type hbSort int

const (
	hbSortPopular hbSort = iota
	hbSortNewest
	hbSortTopRated
	hbSortFree
	hbSortYours
	hbSortCount
)

var hbSortNames = [hbSortCount]string{"Popular", "Newest", "Top rated", "Free", "Yours"}

// hbSortPath is the browse path each sort's feeds use ("" none: Yours).
var hbSortPath = [hbSortCount]string{"", "newest/", "top-rated/", "free/", ""}

// hbNoRank marks an item not in a sort's feeds.
const hbNoRank = 1 << 30

// hbItem is one game in the list. Main thread only.
type hbItem struct {
	key      string // the normalised game page
	title    string
	image    string
	price    string
	desc     string
	author   string
	pub      time.Time
	created  time.Time
	updated  time.Time
	consoles uint16
	rank     [hbSortCount]int
	owned    bool
	gameID   int64
	keyID    int64
	hay      string
	cardID   string
}

// hbFree reports whether the game costs nothing.
func (it *hbItem) hbFree() bool { return hbPriceFree(it.price) }

// hbState is the screen. Main thread only.
type hbState struct {
	visit tbxVisit
	nav   tbxNav

	console int // -1 all, else an hbConsoles index
	sort    hbSort
	query   string

	items []*hbItem
	byKey map[string]*hbItem

	loadedAt  [hbSortCount]time.Time
	loading   bool
	loadSort  hbSort
	loadDone  int
	loadTotal int
	loadGen   uint64
	loadErr   string
	savedAt   time.Time // the oldest saved feed shown, when offline

	ownedAt      time.Time
	ownedLoading bool
	ownedErr     string

	installs map[string]hbInstallRec // by game page
	updates  int
	checking bool

	dirty        bool
	view         []*hbItem
	gridNote     string
	headerStatus string
	searchLabel  string
	conIDs       []string

	detail    hbDetail
	lastFrame time.Time
}

var hb = hbState{console: -1, dirty: true, searchLabel: "Search"}

// ── Scene ───────────────────────────────────────────────────────────────

// homebrewEnsureScene gives the store its scene in memory.
func homebrewEnsureScene(config *Config) {
	if config == nil {
		return
	}
	if sceneIndexExact(config, homebrewSceneName) < 0 {
		config.Scenes = append(config.Scenes, SceneConfig{Name: homebrewSceneName})
	}
	for i := range config.Scenes {
		if config.Scenes[i].Name == homebrewSceneName {
			config.Scenes[i].Elements = nil
			config.Scenes[i].Layout = ""
		}
	}
}

// homebrewStoreOpen opens Get Homebrew (Start menu, search, Home, Packages'
// store strip, the ROM manager).
func homebrewStoreOpen(config *Config) {
	if config == nil {
		config = appConfig
	}
	if config == nil {
		return
	}
	homebrewEnsureScene(config)
	idx := sceneIndexExact(config, homebrewSceneName)
	if idx < 0 {
		return
	}
	qs.open = false
	if idx == currentSceneIndex {
		return
	}
	changeSceneTo(config, idx)
}

// homebrewOnScreen reports whether the store is the scene showing.
func homebrewOnScreen(config *Config) bool {
	return config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
		config.Scenes[currentSceneIndex].Name == homebrewSceneName
}

type hbSceneModule struct{ sceneBase }

func (hbSceneModule) render(r *sdl.Renderer, c *Config) bool {
	hbRender(r, c)
	return true
}

func (hbSceneModule) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e != nil && e.Type == sdl.KEYDOWN && !tbxSheetVisible(c) {
		switch e.Keysym.Sym {
		case sdl.K_TAB:
			dir := 1
			if e.Keysym.Mod&sdl.KMOD_SHIFT != 0 {
				dir = -1
			}
			if hb.detail.open {
				hbDetailStep(dir)
			} else {
				hbStepConsole(dir)
			}
			return true
		case sdl.K_SLASH, sdl.K_s, sdl.K_f:
			if !hb.detail.open {
				hbOpenSearch()
				return true
			}
		}
	}
	return tbxDispatch(c, tbxKeyAction(e), hbAction)
}

func (hbSceneModule) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), hbAction)
}

func init() { registerScene(hbSceneModule{sceneBase{name: homebrewSceneName}}) }

// homebrewSceneClick routes a click or a tap: the sheet, the page, the cards.
func homebrewSceneClick(mx, my int32, config *Config) bool {
	if !homebrewOnScreen(config) {
		return false
	}
	if tbxSheetVisible(config) {
		tbxSheetMouse(mx, my)
		return true
	}
	if hb.detail.open {
		hbDetailClick(mx, my)
		return true
	}
	it, ok := hb.nav.hit(mx, my)
	if !ok {
		return false
	}
	hb.nav.focus = it.id
	if !it.disabled {
		hbActivate(config, it.id)
	}
	return true
}

// homebrewSceneWheel scrolls the store, the game page or the sheet.
func homebrewSceneWheel(config *Config, y int32) bool {
	if y == 0 || !homebrewOnScreen(config) {
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
	if hb.detail.open {
		hb.detail.scroll -= y * 48
		hbDetailClampScroll()
		return true
	}
	hb.nav.scrollBy(-y * 48)
	return true
}

// ── Visits and loading ──────────────────────────────────────────────────

// hbEnter runs once per visit.
func hbEnter() {
	hb.detail = hbDetail{}
	hbInstallsRefresh()
	hbLoadSort(hb.sort)
	if hbKeyGet() != "" {
		if time.Since(hb.ownedAt) > homebrewOwnedTTL {
			hbLoadOwned()
		}
		hbCheckUpdatesSoon(30 * time.Minute)
	}
}

// hbDataDir is data/homebrew-store, created on demand.
func hbDataDir() string {
	dir := filepath.Join("data", "homebrew-store")
	if base, err := P().DataDir(); err == nil && base != "" {
		dir = filepath.Join(base, "homebrew-store")
	}
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// hbHash is a short file-safe name for a URL.
func hbHash(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:10])
}

// hbFeedURL is one console filter's feed for a sort.
func hbFeedURL(s hbSort, filter string) string {
	return "https://itch.io/games/" + hbSortPath[s] + filter + ".xml"
}

// hbFeedResult is one feed as the loader read it.
type hbFeedResult struct {
	console int
	pos     int // the feed's place in the console's list, for ranking
	items   []hbFeedItem
	savedAt time.Time // set when the saved copy answered
	err     error
}

// hbLoadSort reads every console's feeds for sort s on goroutines, unless
// they are fresh. Yours has no feeds.
func hbLoadSort(s hbSort) {
	if hbSortPath[s] == "" && s != hbSortPopular {
		return
	}
	if hb.loading || (!hb.loadedAt[s].IsZero() && time.Since(hb.loadedAt[s]) < homebrewFeedTTL) {
		return
	}
	type job struct {
		console, pos int
		url          string
	}
	var jobs []job
	for c, con := range hbConsoles {
		for k, f := range con.feeds {
			jobs = append(jobs, job{c, k, hbFeedURL(s, f)})
		}
	}
	hb.loading, hb.loadSort, hb.loadDone, hb.loadTotal, hb.loadErr = true, s, 0, len(jobs), ""
	hb.loadGen++
	gen := hb.loadGen
	force := hbFeedForce
	hbFeedForce = false
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("homebrew", "reading itch.io's lists failed: %v", r)
				tbxPost(func() {
					if gen == hb.loadGen {
						hb.loading, hb.loadErr = false, "The lists could not be read"
					}
				})
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		results := make([]hbFeedResult, len(jobs))
		next := make(chan int)
		var wg sync.WaitGroup
		for w := 0; w < homebrewFeedWorkers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() {
					if r := recover(); r != nil {
						logWarn("homebrew", "reading a list failed: %v", r)
						// The sender must not block on a worker that stopped.
						for range next {
						}
					}
				}()
				for i := range next {
					j := jobs[i]
					items, savedAt, err := hbFetchFeed(ctx, j.url, force)
					results[i] = hbFeedResult{console: j.console, pos: j.pos, items: items, savedAt: savedAt, err: err}
					tbxPost(func() {
						if gen == hb.loadGen {
							hb.loadDone++
						}
					})
				}
			}()
		}
		for i := range jobs {
			next <- i
		}
		close(next)
		wg.Wait()
		meta := hbGameCacheSnapshot()
		tbxPost(func() {
			if gen != hb.loadGen {
				return
			}
			hb.loading = false
			hbMergeFeeds(s, results, meta)
			// A sort picked while these loaded gets its own lists now.
			if hb.sort != s {
				hbLoadSort(hb.sort)
			}
		})
	}()
}

// hbFeedForce makes the next load skip the fresh saved copies (Reload
// lists). Main thread.
var hbFeedForce bool

// hbFetchFeed returns a feed: a fresh saved copy (unless force), else the
// network (saved for later), else any saved copy (with its time). A 404 is an
// empty feed.
func hbFetchFeed(ctx context.Context, url string, force bool) ([]hbFeedItem, time.Time, error) {
	dir := filepath.Join(hbDataDir(), "feeds")
	_ = os.MkdirAll(dir, 0o755)
	saved := filepath.Join(dir, hbHash(url)+".xml")
	info, statErr := os.Stat(saved)
	if statErr == nil && !force && time.Since(info.ModTime()) < homebrewFeedTTL {
		if raw, err := os.ReadFile(saved); err == nil {
			if items, err := hbParseFeed(raw); err == nil {
				return items, time.Time{}, nil
			}
		}
	}
	raw, status, err := hbGet(ctx, url, "", homebrewFeedMax)
	if err == nil && status == 404 {
		return nil, time.Time{}, nil
	}
	if err == nil && status == 200 {
		items, perr := hbParseFeed(raw)
		if perr == nil {
			if werr := pstoreWriteFile(saved, raw); werr != nil {
				logDebug("homebrew", "saving a feed: %v", werr)
			}
			return items, time.Time{}, nil
		}
		err = perr
	} else if err == nil {
		err = hbStatusError(status)
	}
	if statErr == nil {
		if raw, rerr := os.ReadFile(saved); rerr == nil {
			if items, perr := hbParseFeed(raw); perr == nil {
				return items, info.ModTime(), nil
			}
		}
	}
	return nil, time.Time{}, err
}

// hbMergeFeeds adds what a sort's feeds found to the list. Main thread.
func hbMergeFeeds(s hbSort, results []hbFeedResult, meta map[string]hbGameData) {
	if hb.byKey == nil {
		hb.byKey = map[string]*hbItem{}
	}
	failed, got := 0, 0
	var firstErr error
	oldest := time.Time{}
	for _, r := range results {
		if r.err != nil {
			failed++
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		if !r.savedAt.IsZero() && (oldest.IsZero() || r.savedAt.Before(oldest)) {
			oldest = r.savedAt
		}
		for pos, fi := range r.items {
			got++
			it := hbItemFor(fi)
			it.consoles |= 1 << uint(r.console)
			// A feed's first games rank first; the console's later feeds
			// rank a little behind its first.
			if rank := pos*4 + r.pos; rank < it.rank[s] {
				it.rank[s] = rank
			}
			if d, ok := meta[it.key]; ok {
				hbApplyGameData(it, d)
			}
		}
	}
	switch {
	case got == 0 && failed > 0 && firstErr != nil:
		hb.loadErr = firstLine(firstErr.Error())
	case failed > 0:
		// Some lists failed (slow Wi-Fi, a timeout): try them again in ten
		// minutes rather than in twelve hours.
		hb.loadedAt[s] = time.Now().Add(homebrewFeedRetry - homebrewFeedTTL)
	default:
		hb.loadedAt[s] = time.Now()
	}
	if got > 0 {
		// The date of the oldest saved list shown, or none when every list
		// came fresh.
		hb.savedAt = oldest
	}
	hb.dirty = true
}

// hbItemFor is the list's item for a feed game, made on first sight and
// filled from the newest feed that names it.
func hbItemFor(fi hbFeedItem) *hbItem {
	it := hb.byKey[fi.Link]
	if it == nil {
		it = &hbItem{key: fi.Link, author: hbLinkAuthor(fi.Link), cardID: "g:" + fi.Link}
		for i := range it.rank {
			it.rank[i] = hbNoRank
		}
		hb.byKey[fi.Link] = it
		hb.items = append(hb.items, it)
	}
	if fi.Title != "" {
		it.title = fi.Title
	}
	if fi.Image != "" {
		it.image = fi.Image
	}
	if fi.Price != "" || it.price == "" {
		it.price = fi.Price
	}
	if fi.Desc != "" {
		it.desc = fi.Desc
	}
	if !fi.Pub.IsZero() {
		it.pub = fi.Pub
	}
	if !fi.Created.IsZero() {
		it.created = fi.Created
	}
	if !fi.Updated.IsZero() {
		it.updated = fi.Updated
	}
	it.hay = strings.ToLower(it.title + " " + it.author + " " + it.desc)
	return it
}

// hbApplyGameData fills an item from its data.json.
func hbApplyGameData(it *hbItem, d hbGameData) {
	if d.ID > 0 {
		it.gameID = d.ID
	}
	if d.Author != "" {
		it.author = d.Author
	}
	if it.image == "" && d.Cover != "" {
		it.image = d.Cover
	}
	if d.Price != "" {
		it.price = d.Price
	}
	it.hay = strings.ToLower(it.title + " " + it.author + " " + it.desc)
}

// hbLoadOwned reads the games the player owns on a goroutine.
func hbLoadOwned() {
	key := hbKeyGet()
	if hb.ownedLoading || key == "" {
		return
	}
	hb.ownedLoading, hb.ownedErr = true, ""
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("homebrew", "reading owned games failed: %v", r)
				tbxPost(func() { hb.ownedLoading = false })
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		owned, err := hbFetchOwned(ctx, key)
		tbxPost(func() {
			hb.ownedLoading = false
			if err != nil {
				hb.ownedErr = firstLine(err.Error())
				return
			}
			hb.ownedAt = time.Now()
			hbMergeOwned(owned)
		})
	}()
}

// hbMergeOwned marks owned games and adds the ones no feed listed. Main
// thread.
func hbMergeOwned(owned []hbOwnedGame) {
	if hb.byKey == nil {
		hb.byKey = map[string]*hbItem{}
	}
	for _, it := range hb.items {
		it.owned, it.keyID = false, 0
	}
	for _, o := range owned {
		if o.Link == "" {
			continue
		}
		it := hb.byKey[o.Link]
		if it == nil {
			it = hbItemFor(hbFeedItem{Title: o.Title, Link: o.Link, Image: o.Cover})
		}
		it.owned, it.keyID, it.gameID = true, o.KeyID, o.GameID
		if o.Author != "" {
			it.author = o.Author
		}
		if it.image == "" {
			it.image = o.Cover
		}
	}
	hb.dirty = true
}

// hbInstallsRefresh copies the install records for the main thread. A game
// installed earlier that no list loaded yet still gets its card (Yours).
func hbInstallsRefresh() {
	hb.installs = hbInstallsSnapshot()
	hb.updates = 0
	if hb.byKey == nil {
		hb.byKey = map[string]*hbItem{}
	}
	for _, r := range hb.installs {
		if r.Update {
			hb.updates++
		}
		if hb.byKey[r.Link] == nil {
			it := hbItemFor(hbFeedItem{Title: r.Title, Link: r.Link})
			it.gameID, it.keyID = r.GameID, r.KeyID
			if c := hbConsoleIndex(r.Console); c >= 0 {
				it.consoles |= 1 << uint(c)
			}
		}
	}
	hb.dirty = true
	hbOptionsRefresh()
}

// hbInstalled is the install record for an item whose file is still there.
func hbInstalled(it *hbItem) (hbInstallRec, bool) {
	if it == nil {
		return hbInstallRec{}, false
	}
	r, ok := hb.installs[it.key]
	return r, ok
}

// hbAfterChange is an install, update or delete that settled: the store's
// records and the library read the card again. Main thread.
func hbAfterChange() {
	hbInstallsRefresh()
	refreshGameLibrary()
	invalidateGameLibrarySystemsCache()
}

// ── Derived lists ───────────────────────────────────────────────────────

// hbLess orders items a and b for sort s.
func hbLess(s hbSort, a, b *hbItem) bool {
	switch s {
	case hbSortNewest:
		ta, tb := hbItemDate(a), hbItemDate(b)
		if a.rank[s] != b.rank[s] {
			return a.rank[s] < b.rank[s]
		}
		if !ta.Equal(tb) {
			return ta.After(tb)
		}
	case hbSortTopRated, hbSortFree:
		if a.rank[s] != b.rank[s] {
			return a.rank[s] < b.rank[s]
		}
		if a.rank[hbSortPopular] != b.rank[hbSortPopular] {
			return a.rank[hbSortPopular] < b.rank[hbSortPopular]
		}
	case hbSortYours:
	default:
		if a.rank[hbSortPopular] != b.rank[hbSortPopular] {
			return a.rank[hbSortPopular] < b.rank[hbSortPopular]
		}
	}
	return strings.ToLower(a.title) < strings.ToLower(b.title)
}

// hbItemDate is when a game came out, as best known.
func hbItemDate(it *hbItem) time.Time {
	if !it.created.IsZero() {
		return it.created
	}
	return it.pub
}

// hbRebuild recomputes the grid and its labels. Main thread, only when
// something it depends on changed.
func hbRebuild() {
	hb.dirty = false
	query := strings.ToLower(strings.Join(strings.Fields(hb.query), " "))
	hb.view = hb.view[:0]
	for _, it := range hb.items {
		// A purchase no console list names (a PC game, a book) is not a
		// cartridge: only an install keeps it in the store.
		if it.consoles == 0 {
			if _, ok := hbInstalled(it); !ok {
				continue
			}
		}
		if hb.console >= 0 && it.consoles&(1<<uint(hb.console)) == 0 {
			if r, ok := hbInstalled(it); !ok || hbConsoleIndex(r.Console) != hb.console {
				continue
			}
		}
		switch hb.sort {
		case hbSortFree:
			if !it.hbFree() {
				continue
			}
		case hbSortYours:
			if _, ok := hbInstalled(it); !ok && !it.owned {
				continue
			}
		}
		if query != "" && !strings.Contains(it.hay, query) {
			continue
		}
		hb.view = append(hb.view, it)
	}
	s := hb.sort
	sort.SliceStable(hb.view, func(i, j int) bool { return hbLess(s, hb.view[i], hb.view[j]) })

	if len(hb.conIDs) != len(hbConsoles)+1 {
		hb.conIDs = make([]string, len(hbConsoles)+1)
		for i := range hb.conIDs {
			hb.conIDs[i] = "con:" + strconv.Itoa(i-1)
		}
	}
	note := pluralizeCount(len(hb.view), "game", "games") + " · " + hbSortNames[hb.sort]
	if hb.console >= 0 {
		note += " · " + hbConsoles[hb.console].name
	}
	if q := strings.TrimSpace(hb.query); q != "" {
		note += " · \"" + q + "\""
	}
	hb.gridNote = note
	status := pluralizeCount(len(hb.items), "game", "games") + " on itch.io"
	if n := len(hb.installs); n > 0 {
		status += " · " + strconv.Itoa(n) + " installed"
	}
	if hb.updates > 0 {
		status += " · " + pluralizeCount(hb.updates, "update", "updates")
	}
	if !hb.savedAt.IsZero() {
		status += " · saved lists from " + hb.savedAt.Local().Format("Jan 2")
	}
	hb.headerStatus = status
}

// hbConsoleIndex is the index of a console id, or -1.
func hbConsoleIndex(id string) int {
	for i, c := range hbConsoles {
		if c.id == id {
			return i
		}
	}
	return -1
}

// hbItemConsoles is the names of the consoles an item is listed under.
func hbItemConsoles(it *hbItem) string {
	var names []string
	for i, c := range hbConsoles {
		if it.consoles&(1<<uint(i)) != 0 {
			names = append(names, c.name)
		}
	}
	return strings.Join(names, ", ")
}

// hbSetConsole shows console c (-1 all).
func hbSetConsole(c int) {
	if c < -1 || c >= len(hbConsoles) || c == hb.console {
		return
	}
	hb.console = c
	hb.nav.scroll = 0
	hb.dirty = true
	PlayToggleSound()
}

// hbStepConsole is L1/R1: the previous or next console chip, wrapping.
func hbStepConsole(dir int) {
	n := len(hbConsoles) + 1
	next := ((hb.console+1+dir)%n+n)%n - 1
	hbSetConsole(next)
	if next >= -1 && next+1 < len(hb.conIDs) {
		hb.nav.focus = hb.conIDs[next+1]
	}
}

// hbSetSort picks a sort and loads its feeds when they are not here yet.
func hbSetSort(s hbSort) {
	s = (s%hbSortCount + hbSortCount) % hbSortCount
	hb.sort = s
	hb.nav.scroll = 0
	hb.dirty = true
	hbLoadSort(s)
	if s == hbSortYours && hbKeyGet() != "" && time.Since(hb.ownedAt) > homebrewOwnedTTL {
		hbLoadOwned()
	}
}

// homebrewSetQuery is the search keyboard typing: the grid narrows on every
// key.
func homebrewSetQuery(q string) {
	if q == hb.query {
		return
	}
	hb.query = q
	hb.searchLabel = "Search"
	if t := strings.TrimSpace(q); t != "" {
		hb.searchLabel = "Search: " + t
	}
	hb.nav.scroll = 0
	hb.dirty = true
}
