package main

// pkgstate.go — the Packages store's state and the work behind it.
//
// Everything the screen shows lives in pkgUI and is only touched on the main
// thread (the event loop and the render loop are one goroutine). Background
// work — reading the cache, scanning the card, fetching the registry,
// installing, removing — never writes that state: it posts a pkgEvent, and
// pkgSync applies the events at the top of the next frame. Leaving the screen
// cancels the scene's tasks (scenetasks.go); an install keeps running behind
// the install card, and its result is applied on the next visit.
//
// Arrival is automatic: the cached list (or the one carried over from the old
// jukauser.json key) is on screen in the first frame, the card is rescanned in
// the background, and the registry is fetched and swapped in place. Offline,
// the fetch is not attempted at all — nothing waits for a timeout — and the
// store refreshes by itself when the connection comes back.

import (
	"context"
	"encoding/json"
	"log"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// ── View modes ───────────────────────────────────────────────────────────

// pkgViewMode is how the store lays packages out.
type pkgViewMode int

const (
	pkgViewRows pkgViewMode = iota
	pkgViewGrid
	pkgViewList
	pkgViewModes
)

// pkgViewKey is the value a view is remembered under in the user config.
func pkgViewKey(v pkgViewMode) string {
	switch v {
	case pkgViewGrid:
		return "grid"
	case pkgViewList:
		return "list"
	}
	return "rows"
}

// pkgViewFromKey is pkgViewKey's inverse; unknown values are the rows view.
func pkgViewFromKey(s string) pkgViewMode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "grid":
		return pkgViewGrid
	case "list":
		return pkgViewList
	}
	return pkgViewRows
}

// ── Scrolling ────────────────────────────────────────────────────────────

// pkgScroll is one eased scroll offset: input moves target, every frame moves
// cur part of the way there.
type pkgScroll struct {
	cur    float64
	target float64
}

// step eases cur toward target over roughly 70ms, frame-rate independent. It
// reports whether the offset is still moving.
func (s *pkgScroll) step(dtMS float64, motion bool) bool {
	d := s.target - s.cur
	if !motion || (d > -0.5 && d < 0.5) {
		s.cur = s.target
		return false
	}
	if dtMS <= 0 {
		return true
	}
	s.cur += d * (1 - math.Exp(-dtMS/70))
	return true
}

// clamp keeps the target (and the current offset) inside [0, limit].
func (s *pkgScroll) clamp(limit float64) {
	if limit < 0 {
		limit = 0
	}
	if s.target > limit {
		s.target = limit
	}
	if s.target < 0 {
		s.target = 0
	}
	if s.cur > limit {
		s.cur = limit
	}
	if s.cur < 0 {
		s.cur = 0
	}
}

// jump puts the offset somewhere at once (a finger drag follows the finger).
func (s *pkgScroll) jump(v float64) {
	s.cur, s.target = v, v
}

// ── Hit targets ──────────────────────────────────────────────────────────

// pkgHitKind is what a recorded click target does.
type pkgHitKind int

const (
	pkgHitNone pkgHitKind = iota
	pkgHitView
	pkgHitRefresh
	pkgHitChip
	pkgHitSort
	pkgHitHeroPrimary
	pkgHitHeroDetails
	pkgHitHeroDot
	pkgHitCard
	pkgHitShelfArrow
	pkgHitSheetButton
	pkgHitSheetClose
	pkgHitSheetText
	pkgHitSheet
	pkgHitBody
)

// pkgHit is one click target recorded by the last frame. Index is the view,
// chip, dot, sheet button or grid/list position; Row is the rows-view row (or
// the arrow's direction); Key names the item a card shows.
type pkgHit struct {
	Kind  pkgHitKind
	Rect  sdl.Rect
	Index int
	Row   int
	Key   string
}

// ── Sheet, pointer and state ─────────────────────────────────────────────

// pkgSheetLine is one wrapped line of the details sheet's text column.
type pkgSheetLine struct {
	Text string
	Kind int
}

const (
	pkgLineHeading = iota
	pkgLineBody
	pkgLineMeta
	pkgLineGap
)

// pkgSheet is the details sheet A opens over a package.
type pkgSheet struct {
	open      bool
	key       string
	openedAt  uint64
	btn       int
	textFocus bool
	scroll    pkgScroll
	armed     string
	armedAt   uint64
	lines     []pkgSheetLine
	linesAt   pkgWrapKey
	contentH  int32
	viewH     int32
}

// pkgPress is a pointer press that has not been released yet. A press on a
// card only opens it on release, and only if it did not turn into a drag.
type pkgPress struct {
	active   bool
	hit      pkgHit
	x, y     int32
	dragging bool
	axisX    bool
	shelfID  string
	start    float64
}

// pkgState is the whole screen.
type pkgState struct {
	entries     []pkgEntry
	haveCatalog bool
	cacheTried  bool
	fetchedAt   time.Time
	fromNetwork bool
	loading     bool
	fetchGen    uint64
	fetchErr    string
	lastOK      time.Time
	installed   map[string]bool
	meta        map[string]pkgInstalledMeta
	sizes       map[string]int64
	icons       map[string]string
	art         map[string]string
	localReady  bool
	appUpdate   string
	appChecked  uint64
	online      bool
	model       *pkgModel
	dirty       bool
	modelGen    uint64
	removing    map[string]bool
	entered     bool
	prefsLoaded bool

	view     pkgViewMode
	sortMode pkgSortMode

	rowFocus   int
	rowFocusID string
	rowCols    map[string]int
	heroBtn    int
	heroIndex  int
	heroSince  uint64
	pageScroll pkgScroll
	rowScroll  map[string]*pkgScroll
	reveal     bool

	chip        string
	chipFocus   bool
	chipCursor  int
	gridIndex   int
	listIndex   int
	gridKey     string
	listKey     string
	gridScroll  pkgScroll
	listScroll  pkgScroll
	filtered    []*pkgItem
	filterValid bool
	filterChip  string
	filterSort  pkgSortMode
	filterGen   uint64
	gridCols    int

	liftKey   pkgFocusKey
	liftSince uint64
	dropKey   pkgFocusKey
	dropSince uint64

	sheet pkgSheet
	press pkgPress
	hits  []pkgHit
	viewW int32

	lastTick uint64
	iconDims map[string][2]int32
}

// pkgFocusKey names the focused control for the lift animation. It is a
// comparable struct rather than a joined string, so naming every visible card
// each frame allocates nothing.
type pkgFocusKey struct {
	kind  byte
	shelf string
	key   string
	btn   int
}

// pkgUI is the Packages screen. Main thread only.
var pkgUI pkgState

// pkgEnsureMaps allocates the state's maps on first use.
func pkgEnsureMaps() {
	st := &pkgUI
	if st.installed == nil {
		st.installed = map[string]bool{}
	}
	if st.meta == nil {
		st.meta = map[string]pkgInstalledMeta{}
	}
	if st.sizes == nil {
		st.sizes = map[string]int64{}
	}
	if st.icons == nil {
		st.icons = map[string]string{}
	}
	if st.art == nil {
		st.art = map[string]string{}
	}
	if st.removing == nil {
		st.removing = map[string]bool{}
	}
	if st.rowCols == nil {
		st.rowCols = map[string]int{}
	}
	if st.rowScroll == nil {
		st.rowScroll = map[string]*pkgScroll{}
	}
	if st.iconDims == nil {
		st.iconDims = map[string][2]int32{}
	}
}

// ── Events from background work ──────────────────────────────────────────

type pkgEventKind int

const (
	pkgEvCatalog pkgEventKind = iota
	pkgEvFetchDone
	pkgEvLocal
	pkgEvInstalled
	pkgEvRemoved
	pkgEvArt
)

// pkgEvent is one finished piece of background work.
type pkgEvent struct {
	kind      pkgEventKind
	gen       uint64
	ok        bool
	network   bool
	at        time.Time
	err       string
	key       string
	entries   []pkgEntry
	installed map[string]bool
	meta      map[string]pkgInstalledMeta
	sizes     map[string]int64
	icons     map[string]string
}

// pkgEvents carries finished work to the main thread. It is drained every
// frame the screen is up; off screen a full queue drops, which is safe because
// arriving on the screen rescans everything.
var pkgEvents = make(chan pkgEvent, 32)

func pkgPost(ev pkgEvent) {
	select {
	case pkgEvents <- ev:
	default:
	}
}

// pkgDrainEvents applies every finished piece of work.
func pkgDrainEvents() {
	for {
		select {
		case ev := <-pkgEvents:
			pkgApplyEvent(ev)
		default:
			return
		}
	}
}

func pkgApplyEvent(ev pkgEvent) {
	st := &pkgUI
	pkgEnsureMaps()
	switch ev.kind {
	case pkgEvCatalog:
		if !ev.network {
			st.cacheTried = true
		}
		if !ev.ok {
			return
		}
		// A cached list read after a fresher one arrived is ignored.
		if st.haveCatalog && ev.at.Before(st.fetchedAt) {
			return
		}
		st.entries = ev.entries
		st.haveCatalog = true
		st.fetchedAt = ev.at
		if ev.network {
			st.fromNetwork = true
			st.lastOK = ev.at
		}
		st.dirty = true
		// Pictures the registry names are fetched after the list, lazily.
		if wanted := pkgArtWanted(ev.entries); len(wanted) > 0 {
			runSceneTask(func(ctx context.Context) { pkgLoadArt(ctx, wanted) })
		}
	case pkgEvArt:
		// Merged, not replaced: two passes of one visit (the cached list's and
		// the fresh one's) may report in either order.
		for k, v := range ev.icons {
			st.art[k] = v
		}
		st.dirty = true
	case pkgEvFetchDone:
		if ev.gen != st.fetchGen {
			return
		}
		st.loading = false
		st.fetchErr = ev.err
	case pkgEvLocal:
		st.installed = ev.installed
		st.meta = ev.meta
		st.sizes = ev.sizes
		st.icons = ev.icons
		st.localReady = true
		st.dirty = true
	case pkgEvInstalled:
		if ev.ok {
			st.installed[ev.key] = true
			if ev.meta != nil {
				st.meta = ev.meta
			}
		}
		st.dirty = true
		// The card changed under the scan: read it again for sizes and icons.
		runSceneTask(pkgLoadLocal)
	case pkgEvRemoved:
		delete(st.removing, ev.key)
		if ev.ok {
			delete(st.installed, ev.key)
			delete(st.meta, ev.key)
			delete(st.sizes, ev.key)
			delete(st.icons, ev.key)
		}
		st.dirty = true
	}
}

// ── Arrival and departure ────────────────────────────────────────────────

// pkgOnSceneEnter is the screen's arrival: what is known is shown at once and
// the background work that brings it up to date starts. core.go calls it when
// a scene with a packagelist element becomes current; pkgSync calls it when
// the screen is drawn without a transition (a hot reload).
func pkgOnSceneEnter(config *Config) {
	st := &pkgUI
	pkgEnsureMaps()
	pkgLoadPrefs(config)
	st.entered = true
	st.sheet = pkgSheet{}
	st.press = pkgPress{}
	st.chipFocus = false
	st.reveal = true
	st.heroSince = sdl.GetTicks64()
	// Whatever fetch the last visit started was cancelled with that visit.
	st.loading = false
	st.fetchGen++
	pkgMigrateLegacy(config)
	runSceneTask(pkgLoadLocal)
	if !st.haveCatalog {
		runSceneTask(pkgLoadCache)
	}
	st.online = IsOnline()
	pkgMaybeRefresh(config, false)
}

// packageActionsForget is the departure: switchScene calls it on every scene
// change, before the fade, so a press during the fade cannot reach a sheet the
// user walked away from. The data stays; the next visit starts from it.
func packageActionsForget() {
	st := &pkgUI
	st.entered = false
	st.sheet = pkgSheet{}
	st.press = pkgPress{}
	st.chipFocus = false
	st.hits = st.hits[:0]
}

// pkgMigrateLegacy moves the registry list older builds kept inside
// jukauser.json (Custom["packages_list"], rewritten on every settings save)
// into the cache file, and shows it at once.
func pkgMigrateLegacy(config *Config) {
	if config == nil || config.Variables.Custom == nil {
		return
	}
	v, ok := config.Variables.Custom["packages_list"]
	if !ok {
		return
	}
	delete(config.Variables.Custom, "packages_list")
	delete(config.Variables.Custom, "packages_error")
	saveUserConfigDebounced(userConfigFromVariables(config))
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	entries, err := pkgParseCatalog(data)
	if err != nil || len(entries) == 0 {
		return
	}
	st := &pkgUI
	if !st.haveCatalog {
		st.entries = entries
		st.haveCatalog = true
		st.fetchedAt = time.Time{}
		st.dirty = true
	}
	go func() {
		if _, statErr := os.Stat(pkgCatalogCachePath()); os.IsNotExist(statErr) {
			if werr := pkgWriteCatalogCache(data, time.Time{}); werr != nil {
				log.Printf("packages: caching the carried-over list: %v", werr)
			}
		}
	}()
}

// pkgLoadCache reads the cached registry for the first frame.
func pkgLoadCache(ctx context.Context) {
	entries, at, err := pkgReadCatalogCache()
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		pkgPost(pkgEvent{kind: pkgEvCatalog})
		return
	}
	pkgPost(pkgEvent{kind: pkgEvCatalog, ok: true, entries: entries, at: at})
}

// pkgLoadLocal scans the card: which package folders exist, the versions they
// were installed at, their sizes and their own icons. It runs off the render
// thread; a folder walk on an SD card is not frame work.
func pkgLoadLocal(ctx context.Context) {
	installed := pkgScanInstalled()
	set := make(map[string]bool, len(installed))
	for k := range installed {
		set[k] = true
	}
	installedPackagesMu.Lock()
	installedPackages = set
	installedPackagesMu.Unlock()

	meta := pkgLoadInstalledMeta()
	sizes := make(map[string]int64, len(installed))
	icons := make(map[string]string, len(installed))
	root := packageInstallDir()
	for key := range installed {
		if ctx.Err() != nil {
			return
		}
		dir := filepath.Join(root, key)
		if icon := iconInFolder(dir); icon != "" {
			icons[key] = icon
		}
		sizes[key] = pkgFolderSize(dir, 20000)
	}
	if ctx.Err() != nil {
		return
	}
	pkgPost(pkgEvent{kind: pkgEvLocal, installed: installed, meta: meta, sizes: sizes, icons: icons})
}

// ── Refresh ──────────────────────────────────────────────────────────────

// pkgRefreshInterval is how fresh a fetched list must be for arrival to skip
// the network: going in and out of the screen should not hit GitHub each time.
const pkgRefreshInterval = 2 * time.Minute

// pkgMaybeRefresh starts a registry fetch when one is due. force is a refresh
// the user asked for. Offline nothing is started, so nothing waits on a
// timeout; a forced refresh says why it did nothing.
func pkgMaybeRefresh(config *Config, force bool) {
	_ = config
	st := &pkgUI
	if st.loading {
		return
	}
	if !IsOnline() {
		if force {
			showToast("Offline — connect Wi-Fi to refresh the package list", ToastWarn())
		}
		return
	}
	if !force && !st.lastOK.IsZero() && time.Since(st.lastOK) < pkgRefreshInterval {
		return
	}
	gen := st.fetchGen
	st.loading = true
	st.fetchErr = ""
	if !runSceneTask(func(ctx context.Context) { pkgFetchTask(ctx, gen) }) {
		st.loading = false
	}
}

// pkgRequestRefresh is X on the pad, R or F5 on a keyboard, the toolbar chip
// and the packages_refresh trigger older configs still bind to a button.
func pkgRequestRefresh(config *Config) {
	if pkgUI.loading {
		return
	}
	PlayActivateSound()
	pkgMaybeRefresh(config, true)
}

// pkgCatalogFetcher downloads the registry. A variable so tests can answer
// without a network.
var pkgCatalogFetcher = func(ctx context.Context) (string, error) {
	return fetchURLContext(ctx, packagesURL, 15*time.Second)
}

// pkgFetchTask downloads, parses and caches the registry. A cancelled fetch
// (the screen was left) says nothing; a failure is reported in plain words.
func pkgFetchTask(ctx context.Context, gen uint64) {
	raw, err := pkgCatalogFetcher(ctx)
	if ctx.Err() != nil {
		pkgPost(pkgEvent{kind: pkgEvFetchDone, gen: gen})
		return
	}
	if err != nil {
		log.Printf("packages: fetch error: %v", err)
		pkgPost(pkgEvent{kind: pkgEvFetchDone, gen: gen, err: plainNetworkError(err)})
		return
	}
	entries, perr := pkgParseCatalog([]byte(raw))
	if perr != nil {
		log.Printf("packages: parse error: %v", perr)
		pkgPost(pkgEvent{kind: pkgEvFetchDone, gen: gen, err: "The package list could not be read."})
		return
	}
	now := time.Now()
	if werr := pkgWriteCatalogCache([]byte(raw), now); werr != nil {
		log.Printf("packages: caching the registry: %v", werr)
	}
	pkgPost(pkgEvent{kind: pkgEvCatalog, gen: gen, ok: true, network: true, entries: entries, at: now})
	pkgPost(pkgEvent{kind: pkgEvFetchDone, gen: gen, ok: true, at: now})
}

// ── Per-frame sync ───────────────────────────────────────────────────────

// pkgSync runs at the top of every frame the screen draws: it applies finished
// work, follows the connection, notices a JukaHub release the header starts
// advertising, and rebuilds the model when anything under it changed.
func pkgSync(config *Config) {
	st := &pkgUI
	pkgEnsureMaps()
	if !st.prefsLoaded {
		pkgLoadPrefs(config)
	}
	if !st.entered && pkgSceneIsLive(config) {
		pkgOnSceneEnter(config)
	}
	pkgDrainEvents()
	now := sdl.GetTicks64()
	if online := IsOnline(); online != st.online {
		st.online = online
		// Back online: refresh by itself if the list on screen is not one
		// this session fetched, or the last attempt failed.
		if online && st.entered && (!st.fromNetwork || st.fetchErr != "") {
			pkgMaybeRefresh(config, true)
		}
	}
	if st.appChecked == 0 || now-st.appChecked > 2000 {
		st.appChecked = now
		label := ""
		if updateAvailable() {
			label = availableUpdateLabel()
		}
		if label != st.appUpdate {
			st.appUpdate = label
			st.dirty = true
		}
	}
	if st.dirty || st.model == nil {
		pkgRebuild()
	}
}

// pkgSceneIsLive reports whether the scene on screen owns the scene-work scope
// (scenetasks.go): it was reached through a real transition, or reloaded in
// place. A test that paints every scene in a loop is not a visit, and must not
// start scans and registry fetches behind the screens that follow.
func pkgSceneIsLive(config *Config) bool {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return false
	}
	name := config.Scenes[currentSceneIndex].Name
	return name != "" && currentSceneWorkName() == name
}

// pkgModelSerial numbers every model ever built. The caches keyed on a model
// (the status line, the hero's wrapped text, the sheet's lines) compare this
// number, and it never repeats, even when the screen's state is reset.
var pkgModelSerial uint64

// pkgRebuild rebuilds the model and puts the focus back on the same shelf and
// package.
func pkgRebuild() {
	st := &pkgUI
	st.model = pkgBuildModel(pkgModelInput{
		Entries:   st.entries,
		Installed: st.installed,
		Meta:      st.meta,
		Sizes:     st.sizes,
		Icons:     st.icons,
		Art:       st.art,
		Device:    DetectDevice(),
		AppUpdate: st.appUpdate,
	})
	pkgModelSerial++
	st.modelGen = pkgModelSerial
	st.dirty = false
	st.filterValid = false
	// Shelves can come and go above the focused one (an Updates shelf appears
	// after a refresh); the view follows the focus to where it now is.
	st.reveal = true
	pkgRestoreRowFocus()
	if st.sheet.open {
		if _, ok := st.model.ByKey[st.sheet.key]; !ok {
			st.sheet = pkgSheet{}
		} else {
			st.sheet.linesAt = pkgWrapKey{}
		}
	}
}

// ── Preferences ──────────────────────────────────────────────────────────

// pkgLoadPrefs reads the remembered view and sort from the user config.
func pkgLoadPrefs(config *Config) {
	st := &pkgUI
	st.prefsLoaded = true
	if config == nil || config.Variables.Custom == nil {
		return
	}
	if v, ok := config.Variables.Custom["pkg_view"].(string); ok {
		st.view = pkgViewFromKey(v)
	}
	if v, ok := config.Variables.Custom["pkg_sort"].(string); ok {
		st.sortMode = pkgSortFromKey(v)
	}
}

// pkgSavePrefs remembers the view and sort in jukauser.json.
func pkgSavePrefs(config *Config) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom["pkg_view"] = pkgViewKey(pkgUI.view)
	config.Variables.Custom["pkg_sort"] = pkgSortKey(pkgUI.sortMode)
	saveUserConfigDebounced(userConfigFromVariables(config))
}

// ── Installs, removals and opening ───────────────────────────────────────

// pkgQueue serialises installs. The install card shows one run at a time and
// a new run cancels the one before it, so two Get presses used to cancel each
// other's download; queued, the second waits for the first.
var pkgQueue struct {
	mu      sync.Mutex
	items   []Package
	current string
	running bool
}

// pkgInstallRunner is the install the queue runs; tests swap it to watch the
// queue without a network.
var pkgInstallRunner = pkgInstall

// pkgEnqueueInstall queues one install and starts the worker if it is idle.
func pkgEnqueueInstall(config *Config, p Package) {
	pkgQueue.mu.Lock()
	if pkgQueue.current == p.Name {
		pkgQueue.mu.Unlock()
		return
	}
	for _, q := range pkgQueue.items {
		if q.Name == p.Name {
			pkgQueue.mu.Unlock()
			return
		}
	}
	pkgQueue.items = append(pkgQueue.items, p)
	start := !pkgQueue.running
	pkgQueue.running = true
	pkgQueue.mu.Unlock()
	if start {
		go pkgInstallWorker(config)
	}
}

// pkgInstallWorker runs the queue until it is empty.
func pkgInstallWorker(config *Config) {
	for {
		pkgQueue.mu.Lock()
		if len(pkgQueue.items) == 0 {
			pkgQueue.running = false
			pkgQueue.current = ""
			pkgQueue.mu.Unlock()
			return
		}
		p := pkgQueue.items[0]
		pkgQueue.items = pkgQueue.items[1:]
		pkgQueue.current = p.Name
		pkgQueue.mu.Unlock()

		ok := pkgInstallRunner(config, p)
		ev := pkgEvent{kind: pkgEvInstalled, ok: ok, key: packageDirName(p.Name)}
		if ok {
			ev.meta = pkgLoadInstalledMeta()
		}
		pkgPost(ev)
	}
}

// pkgQueueState reports whether a package waits in the queue and whether it is
// the one installing now.
func pkgQueueState(name string) (queued, current bool) {
	pkgQueue.mu.Lock()
	defer pkgQueue.mu.Unlock()
	if pkgQueue.current == name {
		return false, true
	}
	for _, q := range pkgQueue.items {
		if q.Name == name {
			return true, false
		}
	}
	return false, false
}

// pkgDequeue takes a waiting install out of the queue.
func pkgDequeue(name string) bool {
	pkgQueue.mu.Lock()
	defer pkgQueue.mu.Unlock()
	for i, q := range pkgQueue.items {
		if q.Name == name {
			pkgQueue.items = append(pkgQueue.items[:i], pkgQueue.items[i+1:]...)
			return true
		}
	}
	return false
}

// pkgStartInstall installs (or updates, or reinstalls) an item. Offline it
// says so instead of starting a download that can only time out.
func pkgStartInstall(config *Config, it *pkgItem, reinstall bool) {
	if it == nil {
		return
	}
	if !IsOnline() {
		PlayErrorSound()
		showToast(offlinePlainNotice, ToastWarn())
		return
	}
	if it.Kind == pkgKindApp {
		PlayActivateSound()
		startHeaderUpdate()
		return
	}
	if it.Kind != pkgKindPackage {
		return
	}
	PlayActivateSound()
	if reinstall {
		// Fresh files, not an overlay on the old ones; the mark goes with the
		// wipe, so a failed download cannot leave a tick on an empty folder.
		if err := pkgRemoveDir(it.Key); err != nil {
			log.Printf("packages: wipe for reinstall of %s: %v", it.Pkg.Name, err)
		}
		pkgForgetInstalled(it.Key)
		delete(pkgUI.installed, it.Key)
		pkgUI.dirty = true
	}
	startPackageInstall(config, it.Pkg)
}

// pkgUpdateAll queues every package with an update. The JukaHub release is not
// part of it: that update restarts the app, which would cut the queue short.
func pkgUpdateAll(config *Config) {
	m := pkgUI.model
	if m == nil {
		return
	}
	if !IsOnline() {
		PlayErrorSound()
		showToast(offlinePlainNotice, ToastWarn())
		return
	}
	n := 0
	for _, it := range m.Items {
		if it.HasUpdate && it.Kind == pkgKindPackage {
			startPackageInstall(config, it.Pkg)
			n++
		}
	}
	if n == 0 && m.App != nil {
		pkgStartInstall(config, m.App, false)
		return
	}
	if n > 0 {
		PlayActivateSound()
		if n == 1 {
			showToast("Updating 1 package", ToastInfo())
		} else {
			showToast("Updating "+strconv.Itoa(n)+" packages", ToastInfo())
		}
	}
}

// pkgRemoveDir deletes one package folder. The key is a folder name, never a
// path: separators and dot names are refused, so the delete cannot leave the
// packages folder.
func pkgRemoveDir(key string) error {
	if key == "" || key == "." || key == ".." || strings.ContainsAny(key, `/\`) || filepath.Base(key) != key {
		return os.ErrInvalid
	}
	if err := os.RemoveAll(filepath.Join(packageInstallDir(), key)); err != nil {
		return err
	}
	installedPackagesMu.Lock()
	delete(installedPackages, key)
	installedPackagesMu.Unlock()
	return nil
}

// pkgStartRemove deletes an installed package in the background; the card
// says "Removing…" until it is gone.
func pkgStartRemove(it *pkgItem) {
	if it == nil || it.Kind == pkgKindApp || !it.Installed {
		return
	}
	st := &pkgUI
	pkgEnsureMaps()
	if st.removing[it.Key] {
		return
	}
	st.removing[it.Key] = true
	key, name := it.Key, it.Pkg.Name
	go func() {
		err := pkgRemoveDir(key)
		if err == nil {
			pkgForgetInstalled(key)
			showToast("Removed "+name, ToastSuccess())
		} else {
			showToast("Could not remove "+name+": "+firstLine(err.Error()), ToastError())
		}
		pkgPost(pkgEvent{kind: pkgEvRemoved, key: key, ok: err == nil})
	}()
}

// pkgOpenFolder shows an installed package's files in Files. Launching a
// downloaded script from the packages folder is deliberately not offered:
// apps.go only starts programs inside the app folders, and that rule stands.
func pkgOpenFolder(config *Config, it *pkgItem) {
	if config == nil || it == nil || !it.Installed || it.Kind == pkgKindApp {
		return
	}
	idx := sceneIndexExact(config, "FileExplorer")
	if idx < 0 {
		PlayErrorSound()
		showToast("Files is not part of this layout", ToastWarn())
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom["fe_path"] = filepath.Join(packageInstallDir(), it.Key)
	PlayActivateSound()
	changeSceneTo(config, idx)
}

// pkgSourceURL is the web page an item points at: its homepage, or the
// repository its download comes from.
func pkgSourceURL(it *pkgItem) (label, target string, ok bool) {
	if it == nil || it.Kind == pkgKindApp {
		return "", "", false
	}
	if hp := strings.TrimSpace(it.Extra.Homepage); hp != "" {
		if l, t, good := packageRepository(hp); good {
			return l, t, true
		}
	}
	return packageRepository(it.Pkg.Download)
}

// pkgOpenSource opens an item's source page in the built-in browser.
func pkgOpenSource(config *Config, it *pkgItem) {
	_, target, ok := pkgSourceURL(it)
	if !ok {
		return
	}
	if !IsOnline() {
		PlayErrorSound()
		showToast(offlinePlainNotice, ToastWarn())
		return
	}
	PlayActivateSound()
	openBrowserAtURL(config, target)
}
