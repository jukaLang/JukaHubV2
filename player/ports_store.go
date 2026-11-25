package main

// ports_store.go — the Ports Store (Start → Games → Ports Store): PortMaster's
// whole catalogue as a store.
//
// The Ports panel (ports.go) lists what is installed and, behind X, the
// catalogue as plain rows. The store is the place to find something new:
//
//   - Discover: shelves of the most downloaded "Ready to Run" ports (free
//     games that need no files), new ports, recently updated ones, popular
//     ports that run your own copy of a game, and the biggest genres. Each
//     shelf is one row of cards and ends in "See all".
//   - All ports, Ready to Run, Bring your game: the catalogue as a grid with
//     genre chips, a sort and search.
//   - Installed: the ports on the card, which of them PortMaster's catalogue
//     has a newer build of, and "Update all".
//
// A card shows the port's screenshot (ports_store_images.go), its title,
// whether it needs the player's own game files and how often it has been
// downloaded (PortMaster-Info's port_stats.json). A opens the details: the
// big screenshot, porters, genres, size, dates, the description, what the
// port needs from you, its runtimes and whether its own manifest says it can
// run on this handheld. X installs straight from a card.
//
// Installing is the shared queue's job (ports_store_install.go): it downloads
// the zip, checks PortMaster's MD5 and lets PortMaster's own installer put it
// on the card — headless when it can, else through the PortMaster app.
//
// "May run here" (on by default) hides only ports whose own metadata rules
// this handheld out (portCatalogMayRun); a port without that metadata is
// shown, because unknown is not "no". On a PC the store is for browsing:
// ports install and run on the handheld.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// pstoreStatsURL is PortMaster-Info's download counter: {"ports":
	// {"2048.zip": 102785, …}}.
	pstoreStatsURL = "https://raw.githubusercontent.com/PortsMaster/PortMaster-Info/main/port_stats.json"
	// pstoreStatsMax bounds the counter read (about 50 KB today).
	pstoreStatsMax = 4 << 20
	// pstoreCacheTTL is how long the saved catalogue and counter are fresh.
	pstoreCacheTTL = 6 * time.Hour
	// pstoreShelfMax bounds the cards a Discover shelf keeps.
	pstoreShelfMax = 12
	// pstoreGenreShelves is how many genre shelves Discover shows.
	pstoreGenreShelves = 3
	// pstoreInstallerKey is the Custom setting: "auto" or "portmaster".
	pstoreInstallerKey = "portsStoreInstaller"
)

// pstoreTab is one tab of the store.
type pstoreTab int

const (
	pstoreTabDiscover pstoreTab = iota
	pstoreTabAll
	pstoreTabRTR
	pstoreTabBYO
	pstoreTabInstalled
	pstoreTabCount
)

var pstoreTabNames = [pstoreTabCount]string{"Discover", "All ports", "Ready to Run", "Bring your game", "Installed"}

// pstoreTabIDs are the tab strip's focus ids.
var pstoreTabIDs = [pstoreTabCount]string{"tab:0", "tab:1", "tab:2", "tab:3", "tab:4"}

// pstoreSort orders the grid.
type pstoreSort int

const (
	pstoreSortPopular pstoreSort = iota
	pstoreSortNewest
	pstoreSortUpdated
	pstoreSortName
	pstoreSortCount
)

var pstoreSortNames = [pstoreSortCount]string{"Most downloaded", "Newest", "Recently updated", "A–Z"}

// pstoreShelf is one Discover row and where its "See all" leads.
type pstoreShelf struct {
	title string
	icon  string
	items []int    // catalogue indices
	ids   []string // the cards' focus ids, one per item
	allID string
	more  string // the "See all" card's count
	tab   pstoreTab
	sort  pstoreSort
	genre string
}

// pstoreInstalledPort is one port on the card.
type pstoreInstalledPort struct {
	name   string // the launcher's name
	launch string
	compat portCompat
	meta   portMeta
	entry  int // catalogue index, -1 when the catalogue does not have it
	update bool
	id     string
}

// pstoreState is the screen. Main thread only; goroutines post results
// through tbxPost.
type pstoreState struct {
	visit tbxVisit
	nav   tbxNav

	tab    pstoreTab
	sort   pstoreSort
	genre  string
	mayRun bool
	query  string

	// The catalogue and what is known about each entry, index-aligned.
	entries  []mcCatalogEntry
	mayRunOK []bool
	hay      []string // lower-case search text
	titleKey []string // lower-case title for A–Z
	cardIDs  []string // "g:<zip>"
	meta     []string // a card's line under the title
	launchOf []string // the installed launcher, "" when not installed
	updateOf []bool   // installed, and the catalogue has a newer build
	dl       []int64  // downloads, 0 unknown (made with the catalogue)
	byZip    map[string]int
	byZipLow map[string]int // lower-case zip → index, for port.json's "name"
	glibc    string
	device   DeviceModel
	stats    map[string]int64 // lower-case zip → downloads
	loading  bool
	loadErr  string
	savedAt  time.Time // set when the catalogue came from the saved copy
	loadGen  uint64
	loadedAt time.Time

	installed      []pstoreInstalledPort
	installedNames map[string]string // lower-case launcher name → path
	installedSig   string            // the launchers and their md5s, to spot a change
	scanning       bool
	scanned        bool
	scanAt         time.Time
	rescanAt       time.Time
	updates        int

	dirty   bool
	view    []int
	shelves []pstoreShelf
	genres  []string // by count, for the chips
	// Labels made when the lists change, not every frame.
	genreIDs       []string
	genreLabels    []string
	gridNote       string
	headerStatus   string
	installedLabel string
	updatesNote    string
	searchLabel    string

	detail    pstoreDetail
	lastFrame time.Time
}

var pstore = pstoreState{mayRun: true, dirty: true, searchLabel: "Search"}

// ── Scene ───────────────────────────────────────────────────────────────

// portsStoreEnsureScene gives the store its scene in memory (store_scenes.go).
func portsStoreEnsureScene(config *Config) {
	if config == nil {
		return
	}
	if sceneIndexExact(config, portsStoreSceneName) < 0 {
		config.Scenes = append(config.Scenes, SceneConfig{Name: portsStoreSceneName})
	}
	for i := range config.Scenes {
		if config.Scenes[i].Name == portsStoreSceneName {
			config.Scenes[i].Elements = nil
			config.Scenes[i].Layout = ""
		}
	}
}

// portsStoreOpen opens the store (Start menu, search, the Ports panel).
func portsStoreOpen(config *Config) {
	if config == nil {
		config = appConfig
	}
	if config == nil {
		return
	}
	portsStoreEnsureScene(config)
	idx := sceneIndexExact(config, portsStoreSceneName)
	if idx < 0 {
		return
	}
	qs.open = false
	if portsPanel.open {
		portsPanel = portsPanelState{}
		portsPanelRect = sdl.Rect{}
		portRowHits = nil
	}
	if idx == currentSceneIndex {
		return
	}
	changeSceneTo(config, idx)
}

// portsStoreOnScreen reports whether the store is the scene showing.
func portsStoreOnScreen(config *Config) bool {
	return config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
		config.Scenes[currentSceneIndex].Name == portsStoreSceneName
}

type pstoreSceneModule struct{ sceneBase }

func (pstoreSceneModule) render(r *sdl.Renderer, c *Config) bool {
	pstoreRender(r, c)
	return true
}

func (pstoreSceneModule) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e != nil && e.Type == sdl.KEYDOWN && !tbxSheetVisible(c) {
		switch e.Keysym.Sym {
		case sdl.K_TAB:
			// Tab and Shift+Tab are the keyboard's L1/R1: tabs, or the next
			// port while its details are open.
			dir := 1
			if e.Keysym.Mod&sdl.KMOD_SHIFT != 0 {
				dir = -1
			}
			if pstore.detail.open {
				pstoreDetailStep(dir)
			} else {
				pstoreSwitchTab(pstore.tab + pstoreTab(dir))
			}
			return true
		case sdl.K_SLASH, sdl.K_s, sdl.K_f:
			if !pstore.detail.open {
				pstoreOpenSearch()
				return true
			}
		}
	}
	return tbxDispatch(c, tbxKeyAction(e), pstoreAction)
}

func (pstoreSceneModule) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), pstoreAction)
}

func init() { registerScene(pstoreSceneModule{sceneBase{name: portsStoreSceneName}}) }

// portsStoreSceneClick routes a click or a tap: the sheet, the details, then
// the cards.
func portsStoreSceneClick(mx, my int32, config *Config) bool {
	if !portsStoreOnScreen(config) {
		return false
	}
	if tbxSheetVisible(config) {
		tbxSheetMouse(mx, my)
		return true
	}
	if pstore.detail.open {
		pstoreDetailClick(mx, my)
		return true
	}
	it, ok := pstore.nav.hit(mx, my)
	if !ok {
		return false
	}
	pstore.nav.focus = it.id
	if !it.disabled {
		pstoreActivate(config, it.id)
	}
	return true
}

// portsStoreSceneWheel scrolls the store, its details or its sheet.
func portsStoreSceneWheel(config *Config, y int32) bool {
	if y == 0 || !portsStoreOnScreen(config) {
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
	if pstore.detail.open {
		pstore.detail.scroll -= y * 48
		pstoreDetailClampScroll()
		return true
	}
	pstore.nav.scrollBy(-y * 48)
	return true
}

// ── Visits and loading ──────────────────────────────────────────────────

// pstoreEnter runs once per visit.
func pstoreEnter() {
	pstore.detail = pstoreDetail{}
	if len(pstore.entries) == 0 || time.Since(pstore.loadedAt) > pstoreCacheTTL {
		pstoreLoad()
	}
	pstoreScanInstalled()
	// PortMaster's files when the card has none (portmaster_autoupdate.go),
	// and zips waiting in its autoinstall folder (ports_native_install.go).
	pmAutoInstallOnOpen()
	if portMasterInstalled() {
		pnAutoinstallSweep()
	}
}

// pstoreLoad reads the catalogue (the network, else the saved copy), the
// download counter and this handheld's facts on a goroutine.
func pstoreLoad() {
	if pstore.loading {
		return
	}
	pstore.loading = true
	pstore.loadErr = ""
	pstore.loadGen++
	gen := pstore.loadGen
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("portsstore", "loading the catalogue failed: %v", r)
				tbxPost(func() {
					if gen == pstore.loadGen {
						pstore.loading, pstore.loadErr = false, "The catalogue could not be read"
					}
				})
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		entries, savedAt, err := pstoreFetchCatalog(ctx)
		stats := pstoreFetchStats(ctx)
		device, glibc := DetectDevice(), portCatalogHostGlibc()
		mayRun := make([]bool, len(entries))
		hay := make([]string, len(entries))
		titles := make([]string, len(entries))
		ids := make([]string, len(entries))
		dl := make([]int64, len(entries))
		byZip := make(map[string]int, len(entries))
		byZipLow := make(map[string]int, len(entries))
		for i := range entries {
			e := &entries[i]
			mayRun[i] = portCatalogMayRun(*e, device, glibc)
			title := pstoreTitle(*e)
			titles[i] = strings.ToLower(title)
			hay[i] = strings.ToLower(title + " " + e.Zip + " " + e.Description + " " +
				strings.Join(e.Porters, " ") + " " + strings.Join(e.Genres, " "))
			ids[i] = "g:" + e.Zip
			byZip[e.Zip] = i
			byZipLow[strings.ToLower(e.Zip)] = i
			if stats != nil {
				dl[i] = stats[strings.ToLower(e.Zip)]
			}
		}
		tbxPost(func() {
			if gen != pstore.loadGen {
				return
			}
			pstore.loading = false
			if err != nil && len(entries) == 0 {
				pstore.loadErr = firstLine(err.Error())
				return
			}
			pstore.entries, pstore.mayRunOK, pstore.hay, pstore.titleKey, pstore.cardIDs = entries, mayRun, hay, titles, ids
			pstore.byZip, pstore.byZipLow, pstore.dl = byZip, byZipLow, dl
			pstore.stats, pstore.device, pstore.glibc, pstore.savedAt = stats, device, glibc, savedAt
			pstore.loadedAt = time.Now()
			pstore.dirty = true
			// The Ports panel's update lines read the same catalogue.
			invalidatePortsCache()
		})
	}()
}

// pstoreDataDir is data/ports-store, created on demand.
func pstoreDataDir() string {
	dir := filepath.Join("data", "ports-store")
	if base, err := P().DataDir(); err == nil && base != "" {
		dir = filepath.Join(base, "ports-store")
	}
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// pstoreWriteFile writes data atomically (a temporary name, then a rename).
func pstoreWriteFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// pstoreGet reads url with a size bound.
func pstoreGet(ctx context.Context, url string, max int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "JukaHub-PortsStore/1.0")
	resp, err := (&http.Client{Timeout: 45 * time.Second, Transport: ytTransport()}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %d", req.URL.Host, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s sent more than %d bytes", req.URL.Host, max)
	}
	return data, nil
}

// pstoreFetchCatalog returns PortMaster's catalogue: the shared memory copy
// when it is fresh, else the network (saved for later), else the saved copy,
// whose time is returned so the header can say the list may be old.
func pstoreFetchCatalog(ctx context.Context) ([]mcCatalogEntry, time.Time, error) {
	mcCatalogCache.mu.Lock()
	if len(mcCatalogCache.entries) > 0 && time.Since(mcCatalogCache.at) < mcCatalogTTL {
		out := mcCatalogCache.entries
		mcCatalogCache.mu.Unlock()
		return out, time.Time{}, nil
	}
	mcCatalogCache.mu.Unlock()
	saved := filepath.Join(pstoreDataDir(), "ports.json")
	raw, err := pstoreGet(ctx, mcPortsCatalogURL, mcCatalogMax)
	if err == nil {
		if entries := mcParsePortsCatalog(raw); len(entries) > 0 {
			if werr := pstoreWriteFile(saved, raw); werr != nil {
				logDebug("portsstore", "saving the catalogue: %v", werr)
			}
			mcCatalogCache.mu.Lock()
			mcCatalogCache.entries, mcCatalogCache.at = entries, time.Now()
			mcCatalogCache.mu.Unlock()
			return entries, time.Time{}, nil
		}
		err = errors.New("PortMaster's catalogue lists no ports this version can read")
	}
	logDebug("portsstore", "catalogue: %v", err)
	info, serr := os.Stat(saved)
	if serr != nil {
		return nil, time.Time{}, err
	}
	data, rerr := os.ReadFile(saved)
	if rerr != nil {
		return nil, time.Time{}, err
	}
	entries := mcParsePortsCatalog(data)
	if len(entries) == 0 {
		return nil, time.Time{}, err
	}
	return entries, info.ModTime(), nil
}

var pstoreStatsCache struct {
	at    time.Time
	stats map[string]int64
}

// pstoreFetchStats returns the download counter (lower-case zip names): a
// fresh saved copy, else the network, else any saved copy, else nil. Only
// the load goroutine calls it.
func pstoreFetchStats(ctx context.Context) map[string]int64 {
	if pstoreStatsCache.stats != nil && time.Since(pstoreStatsCache.at) < pstoreCacheTTL {
		return pstoreStatsCache.stats
	}
	saved := filepath.Join(pstoreDataDir(), "port_stats.json")
	if info, err := os.Stat(saved); err == nil && time.Since(info.ModTime()) < pstoreCacheTTL {
		if data, err := os.ReadFile(saved); err == nil {
			if stats := pstoreParseStats(data); len(stats) > 0 {
				pstoreStatsCache.stats, pstoreStatsCache.at = stats, info.ModTime()
				return stats
			}
		}
	}
	if raw, err := pstoreGet(ctx, pstoreStatsURL, pstoreStatsMax); err == nil {
		if stats := pstoreParseStats(raw); len(stats) > 0 {
			_ = pstoreWriteFile(saved, raw)
			pstoreStatsCache.stats, pstoreStatsCache.at = stats, time.Now()
			return stats
		}
	} else {
		logDebug("portsstore", "download counts: %v", err)
	}
	if data, err := os.ReadFile(saved); err == nil {
		return pstoreParseStats(data)
	}
	return nil
}

// pstoreParseStats reads port_stats.json: {"ports": {zip: n}}, or the map
// itself. Pure.
func pstoreParseStats(raw []byte) map[string]int64 {
	var doc map[string]interface{}
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	src := doc
	if inner, ok := doc["ports"].(map[string]interface{}); ok {
		src = inner
	}
	out := make(map[string]int64, len(src))
	for k, v := range src {
		if n, ok := v.(float64); ok && n > 0 && strings.HasSuffix(strings.ToLower(k), ".zip") {
			out[strings.ToLower(k)] = int64(n)
		}
	}
	return out
}

// pstoreScanInstalled reads the card's ports on a goroutine.
func pstoreScanInstalled() {
	if pstore.scanning {
		return
	}
	if !portsLibraryEnabled() {
		pstore.scanned = true
		return
	}
	pstore.scanning = true
	pstore.scanAt = time.Now()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("portsstore", "reading the installed ports failed: %v", r)
				tbxPost(func() { pstore.scanning = false })
			}
		}()
		found := scanPortLaunchers(portRoots(portMasterSDRoot))
		out := make([]pstoreInstalledPort, 0, len(found))
		names := make(map[string]string, len(found))
		var sig strings.Builder
		for _, p := range found {
			if p.Master || p.Install || p.Info || p.Launch == "" {
				continue
			}
			out = append(out, pstoreInstalledPort{
				name: p.Name, launch: p.Launch, compat: p.Compat, meta: portMetaFor(p.Launch), entry: -1,
				id: "i:" + p.Launch,
			})
			names[strings.ToLower(p.Name)] = p.Launch
			m := out[len(out)-1].meta
			sig.WriteString(p.Launch + "\x00" + m.MD5 + "\x00" + m.Title + "\x00" + strconv.Itoa(int(p.Compat.State)) + "\n")
		}
		sort.Slice(out, func(i, j int) bool {
			return strings.ToLower(pstoreInstalledTitle(out[i])) < strings.ToLower(pstoreInstalledTitle(out[j]))
		})
		signature := sig.String()
		tbxPost(func() {
			pstore.scanning, pstore.scanned = false, true
			pstore.scanAt = time.Now()
			// The lists are rebuilt only when what is installed changed:
			// the scan runs every minute and after every visit away.
			if signature == pstore.installedSig && pstore.installed != nil {
				return
			}
			pstore.installed, pstore.installedNames, pstore.installedSig = out, names, signature
			pstore.dirty = true
		})
	}()
}

// pstoreAfterChange is called when an install, update or uninstall settled:
// every list that shows ports reads the card again.
func pstoreAfterChange() {
	portMetaForget()
	invalidatePortsCache()
	invalidateEmulatorCache()
	if gameLib.open {
		portsLibraryRefresh()
	}
	// The Ports panel's catalogue rows say which ports are installed.
	if portsPanel.open && portsPanel.catalog {
		portsPanelRebuildCatalogRows()
	}
	pstore.rescanAt = time.Now().Add(1500 * time.Millisecond)
}

// ── Derived lists ───────────────────────────────────────────────────────

// pstoreTitle is the name a port goes by.
func pstoreTitle(e mcCatalogEntry) string {
	if e.Title != "" {
		return e.Title
	}
	return strings.TrimSuffix(e.Zip, filepath.Ext(e.Zip))
}

// pstoreInstalledTitle is an installed port's name.
func pstoreInstalledTitle(p pstoreInstalledPort) string {
	return setOr(p.meta.Title, p.name)
}

// pstoreDownloads is a catalogue entry's download count (0 unknown).
func pstoreDownloads(i int) int64 {
	if i < 0 || i >= len(pstore.dl) {
		return 0
	}
	return pstore.dl[i]
}

// pstoreMatchInstalled is an installed port's catalogue index, or -1: by the
// zip name its port.json records, else by its launcher among an entry's
// items (portCatalogMatch's rules, without copying entries).
func pstoreMatchInstalled(p *pstoreInstalledPort) int {
	if p.meta.Zip != "" {
		if i, ok := pstore.byZipLow[strings.ToLower(p.meta.Zip)]; ok {
			return i
		}
	}
	base := filepath.Base(p.launch)
	for i := range pstore.entries {
		for _, item := range pstore.entries[i].Items {
			if strings.EqualFold(filepath.Base(strings.TrimRight(item, "/")), base) {
				return i
			}
		}
	}
	return -1
}

// pstoreLaunchFor finds an entry's launcher among the installed names.
func pstoreLaunchFor(e mcCatalogEntry, names map[string]string) string {
	if len(names) == 0 {
		return ""
	}
	if p := names[strings.ToLower(strings.TrimSuffix(e.Zip, filepath.Ext(e.Zip)))]; p != "" {
		return p
	}
	for _, item := range e.Items {
		if strings.EqualFold(filepath.Ext(item), ".sh") {
			if p := names[strings.ToLower(strings.TrimSuffix(filepath.Base(item), filepath.Ext(item)))]; p != "" {
				return p
			}
		}
	}
	return ""
}

// pstoreHasGenre reports whether e is in genre.
func pstoreHasGenre(e mcCatalogEntry, genre string) bool {
	for _, g := range e.Genres {
		if g == genre {
			return true
		}
	}
	return false
}

// pstoreLess orders entries i and j by s.
func pstoreLess(s pstoreSort, i, j int) bool {
	a, b := &pstore.entries[i], &pstore.entries[j]
	switch s {
	case pstoreSortPopular:
		if da, db := pstoreDownloads(i), pstoreDownloads(j); da != db {
			return da > db
		}
	case pstoreSortNewest:
		if a.DateAdded != b.DateAdded {
			return a.DateAdded > b.DateAdded
		}
	case pstoreSortUpdated:
		ua, ub := setOr(a.DateUpdated, a.DateAdded), setOr(b.DateUpdated, b.DateAdded)
		if ua != ub {
			return ua > ub
		}
	}
	return pstore.titleKey[i] < pstore.titleKey[j]
}

// pstoreSorted is idx ordered by s (in place).
func pstoreSorted(idx []int, s pstoreSort) []int {
	sort.SliceStable(idx, func(x, y int) bool { return pstoreLess(s, idx[x], idx[y]) })
	return idx
}

// pstoreRebuild recomputes the installed matches, the grid, the shelves and
// the genre chips. Main thread, only when something they depend on changed.
func pstoreRebuild() {
	pstore.dirty = false
	n := len(pstore.entries)
	if cap(pstore.launchOf) < n {
		pstore.launchOf = make([]string, n)
	}
	pstore.launchOf = pstore.launchOf[:n]
	if cap(pstore.updateOf) < n {
		pstore.updateOf = make([]bool, n)
	}
	pstore.updateOf = pstore.updateOf[:n]
	if len(pstore.meta) != n {
		pstore.meta = make([]string, n)
	}
	for i := range pstore.entries {
		e := &pstore.entries[i]
		pstore.launchOf[i] = pstoreLaunchFor(*e, pstore.installedNames)
		pstore.updateOf[i] = false
		kind := "Your game files"
		if e.RTR {
			kind = "Ready to Run"
		}
		if c := pstoreCount(pstoreDownloads(i)); c != "" {
			kind += " · " + c
		}
		pstore.meta[i] = kind
	}
	pstore.updates = 0
	for k := range pstore.installed {
		p := &pstore.installed[k]
		p.update = false
		p.entry = pstoreMatchInstalled(p)
		if p.entry >= 0 {
			p.update = portUpdateAvailable(p.meta, pstore.entries[p.entry])
			if p.update {
				pstore.updates++
				pstore.updateOf[p.entry] = true
			}
		}
	}

	pass := func(i int) bool { return !pstore.mayRun || pstore.mayRunOK[i] }
	query := strings.ToLower(strings.Join(strings.Fields(pstore.query), " "))

	// The grid.
	pstore.view = pstore.view[:0]
	for i := range pstore.entries {
		e := &pstore.entries[i]
		if !pass(i) {
			continue
		}
		switch pstore.tab {
		case pstoreTabRTR:
			if !e.RTR {
				continue
			}
		case pstoreTabBYO:
			if e.RTR {
				continue
			}
		}
		if pstore.genre != "" && !pstoreHasGenre(*e, pstore.genre) {
			continue
		}
		if query != "" && !strings.Contains(pstore.hay[i], query) {
			continue
		}
		pstore.view = append(pstore.view, i)
	}
	pstoreSorted(pstore.view, pstore.sort)

	// The genre chips: the genres of what may show, biggest first.
	counts := map[string]int{}
	for i := range pstore.entries {
		if pass(i) {
			for _, g := range pstore.entries[i].Genres {
				if g != "" {
					counts[g]++
				}
			}
		}
	}
	pstore.genres = pstore.genres[:0]
	for g := range counts {
		pstore.genres = append(pstore.genres, g)
	}
	sort.Slice(pstore.genres, func(i, j int) bool {
		a, b := pstore.genres[i], pstore.genres[j]
		if counts[a] != counts[b] {
			return counts[a] > counts[b]
		}
		return a < b
	})

	pstore.genreIDs, pstore.genreLabels = pstore.genreIDs[:0], pstore.genreLabels[:0]
	for _, g := range pstore.genres {
		pstore.genreIDs = append(pstore.genreIDs, "genre:"+g)
		pstore.genreLabels = append(pstore.genreLabels, pstoreGenreLabel(g))
	}

	pstoreBuildShelves(pass)
	pstoreBuildLabels()
}

// pstoreBuildLabels makes the strings the frame draws.
func pstoreBuildLabels() {
	note := pluralizeCount(len(pstore.view), "port", "ports") + " · " + pstoreSortNames[pstore.sort]
	if pstore.genre != "" {
		note += " · " + pstoreGenreLabel(pstore.genre)
	}
	if pstore.mayRun {
		note += " · may run here"
	}
	pstore.gridNote = note
	status := pluralizeCount(len(pstore.entries), "port", "ports")
	if pstore.updates > 0 {
		status += " · " + pluralizeCount(pstore.updates, "update", "updates")
	}
	if !pstore.savedAt.IsZero() {
		status += " · saved list from " + formatDateNear(appConfig, pstore.savedAt, time.Now())
	}
	pstore.headerStatus = status
	pstore.installedLabel = ""
	if pstore.updates > 0 {
		pstore.installedLabel = "Installed · " + strconv.Itoa(pstore.updates)
	}
	pstore.updatesNote = pluralizeCount(pstore.updates, "port has", "ports have") + " a newer build in PortMaster's catalogue"
}

// pstoreBuildShelves lays Discover out.
func pstoreBuildShelves(pass func(int) bool) {
	pstore.shelves = pstore.shelves[:0]
	popular := pstore.stats != nil
	collect := func(keep func(e *mcCatalogEntry) bool, s pstoreSort) []int {
		var out []int
		for i := range pstore.entries {
			if pass(i) && keep(&pstore.entries[i]) {
				out = append(out, i)
			}
		}
		return pstoreSorted(out, s)
	}
	add := func(title, icon string, items []int, tab pstoreTab, s pstoreSort, genre string) {
		if len(items) == 0 {
			return
		}
		k := len(pstore.shelves)
		sh := pstoreShelf{title: title, icon: icon, tab: tab, sort: s, genre: genre,
			allID: "all:" + strconv.Itoa(k), more: pluralizeCount(len(items), "port", "ports")}
		if len(items) > pstoreShelfMax {
			items = items[:pstoreShelfMax]
		}
		sh.items = items
		sh.ids = make([]string, len(items))
		for j, i := range items {
			sh.ids[j] = "c:" + strconv.Itoa(k) + ":" + pstore.entries[i].Zip
		}
		pstore.shelves = append(pstore.shelves, sh)
	}
	rtrSort, rtrTitle := pstoreSortNewest, "Ready to Run · free, no files needed"
	if popular {
		rtrSort, rtrTitle = pstoreSortPopular, "Most downloaded · Ready to Run"
	}
	add(rtrTitle, "sparkles", collect(func(e *mcCatalogEntry) bool { return e.RTR }, rtrSort), pstoreTabRTR, rtrSort, "")
	add("New ports", "download", collect(func(e *mcCatalogEntry) bool { return e.DateAdded != "" }, pstoreSortNewest),
		pstoreTabAll, pstoreSortNewest, "")
	add("Recently updated", "refresh", collect(func(e *mcCatalogEntry) bool { return e.DateUpdated != "" }, pstoreSortUpdated),
		pstoreTabAll, pstoreSortUpdated, "")
	byoSort, byoTitle := pstoreSortName, "Bring your own game files"
	if popular {
		byoSort, byoTitle = pstoreSortPopular, "Popular · bring your own game files"
	}
	add(byoTitle, "folder", collect(func(e *mcCatalogEntry) bool { return !e.RTR }, byoSort), pstoreTabBYO, byoSort, "")
	shown := 0
	for _, g := range pstore.genres {
		if shown >= pstoreGenreShelves {
			break
		}
		genre := g
		items := collect(func(e *mcCatalogEntry) bool { return pstoreHasGenre(*e, genre) }, byoSort)
		if len(items) < 4 {
			continue
		}
		add(pstoreGenreLabel(genre), "gamepad", items, pstoreTabAll, byoSort, genre)
		shown++
	}
}

// pstoreGenreLabel is how a genre tag reads ("fps" → "FPS").
func pstoreGenreLabel(g string) string {
	switch g {
	case "":
		return "All genres"
	case "fps", "rpg", "mmo", "2d", "3d", "jrpg", "arpg":
		return strings.ToUpper(g)
	case "rhythm":
		return "Rhythm"
	}
	r := []rune(g)
	if len(r) == 0 {
		return g
	}
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

// pstoreSwitchTab moves to tab t (wrapping).
func pstoreSwitchTab(t pstoreTab) {
	t = (t%pstoreTabCount + pstoreTabCount) % pstoreTabCount
	if t == pstore.tab {
		return
	}
	pstore.tab = t
	pstore.nav.focus = pstoreTabIDs[t]
	pstore.nav.scroll = 0
	pstore.dirty = true
	if t == pstoreTabInstalled {
		pstoreScanInstalled()
	}
	PlayNavSound()
}

// pstoreSetQuery is the search keyboard typing (portsPanelSetQuery routes it
// here while the store is up). A search from Discover lands on All ports.
func pstoreSetQuery(q string) {
	if q == pstore.query {
		return
	}
	pstore.query = q
	pstore.searchLabel = "Search"
	if t := strings.TrimSpace(q); t != "" {
		pstore.searchLabel = "Search: " + t
	}
	if strings.TrimSpace(q) != "" && (pstore.tab == pstoreTabDiscover || pstore.tab == pstoreTabInstalled) {
		pstore.tab = pstoreTabAll
	}
	pstore.nav.scroll = 0
	pstore.dirty = true
}

// pstoreOpenSearch opens the search keyboard on the store's query.
func pstoreOpenSearch() {
	portsPanel.query = pstore.query
	PlayActivateSound()
	vkOpenPortsSearch()
}

// pstoreInstallerMode reads portsStoreInstaller: "auto", "jukahub",
// "harbourmaster" or "portmaster" (ports_store_install.go).
func pstoreInstallerMode() string {
	if c := appConfig; c != nil {
		if v, ok := customString(c.Variables.Custom, pstoreInstallerKey); ok {
			switch v = strings.ToLower(strings.TrimSpace(v)); v {
			case "jukahub", "harbourmaster", "portmaster":
				return v
			}
		}
	}
	return "auto"
}

// pstoreWhyNot says why an entry's own metadata rules this handheld out, or
// "" when it does not (portCatalogMayRun's checks, in words).
func pstoreWhyNot(e mcCatalogEntry) string {
	if !mcCatalogArchOK(e.Arch) {
		return "it is built for " + strings.Join(e.Arch, ", ") + ", not 64-bit ARM"
	}
	if e.MinGlibc != "" && pstore.glibc != "" &&
		elfVersionLess(pstore.glibc, "GLIBC_"+strings.TrimPrefix(e.MinGlibc, "GLIBC_")) &&
		!privateGlibcCatalogEligible(e, pstore.device) {
		return "it needs glibc " + strings.TrimPrefix(e.MinGlibc, "GLIBC_") + " and this system has " +
			strings.TrimPrefix(pstore.glibc, "GLIBC_")
	}
	if !portCatalogMayRun(e, pstore.device, pstore.glibc) {
		return "its requirements (" + strings.Join(e.Requirements, ", ") + ") rule this handheld out"
	}
	return ""
}

// pstoreCount formats a download count ("102k", "1.2M").
func pstoreCount(n int64) string {
	switch {
	case n <= 0:
		return ""
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 10000:
		return strconv.FormatFloat(float64(n)/1000, 'f', 1, 64) + "k"
	case n < 1000000:
		return strconv.FormatInt(n/1000, 10) + "k"
	}
	return strconv.FormatFloat(float64(n)/1000000, 'f', 1, 64) + "M"
}
