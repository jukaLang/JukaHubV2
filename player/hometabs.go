package main

// hometabs.go — the Tabs Home (JukaOS v2): its state, its settings, focus and
// input. hometabs_draw.go draws it, hometabs_items.go fills Recent, Main and
// Entertainment, hometabs_edit.go is the L2 sheet that arranges the tabs; the
// shared vocabulary is hometabs_model.go.
//
// Tabs is a Home arrangement (homeArrangementTabs, the default when none is
// stored). The top bar keeps the wordmark on its left and the system controls
// on its right; between them, in the middle, a strip of tabs — Recent, Main,
// Games, Entertainment, Favourites, Collections — fades out at both ends. The
// body under it is the active tab's page, drawn in the view the user picked
// for it (grid, row, list, carousel, compact). The built-in cards and the
// search pill are off in this arrangement: Y still searches.
//
// Focus: Home's focus controller knows two stops of the shell, the page
// (homeTabsPageID) and the strip (homeTabsStripID); which tile of the page is
// focused is the page's own index, kept per tab and per folder level, so a
// tab comes back where it was left. The D-pad walks the page by its drawn
// geometry; Up from its top row reaches the strip, Down from its bottom row
// the power pill. On the strip Left/Right change tab and A changes the view.
//
// Buttons: L1/R1 (Tab / Shift+Tab, PgUp/PgDn) change tab and wrap round; A
// opens; a folder (the Store, a collection) opens as a sub-page that B leaves;
// X opens the item sheet (favourite, add to a collection — the providers');
// L2 arranges the tabs; Y, Start, Select, R2 and Menu are Home's as ever.
//
// Stored (Custom, jukauser.json): homeTabViews ("games=row;col:abc=list"),
// homeTabsShown ("recent,main,-games,…": order, "-" hidden), homeTabLast (the
// tab to come back to; bookkeeping, cfgdocRuntimeKeys) and homeTabsRemember.

import (
	"math"
	"sort"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// The shell's two focus stops.
const (
	homeTabsPageID  HomeFocusID = "tabs:page"
	homeTabsStripID HomeFocusID = "tabs:strip"
)

// The settings the shell stores in Custom.
const (
	homeTabsViewsKey    = "homeTabViews"
	homeTabsShownKey    = "homeTabsShown"
	homeTabLastKey      = "homeTabLast"
	homeTabsRememberKey = "homeTabsRemember"
)

const (
	// homeTabsSlideMS is how long a page takes to slide in.
	homeTabsSlideMS = 220
	// homeTabsRecentEveryMS is how often Recent reads its lists again while
	// it is shown; every other page waits for homeTabsContentGen.
	homeTabsRecentEveryMS = 4000
	// homeTabsAwayMS: a gap this long between two refreshes means Home was
	// not on screen, and the page is read again on return.
	homeTabsAwayMS = 500
)

func init() {
	// The tab to come back to is bookkeeping, not a setting anyone chose.
	cfgdocRuntimeKeys[homeTabLastKey] = true
}

// homeTabsLevel is one page of a tab: the tab's own (levels[0]) or a folder
// opened from it.
type homeTabsLevel struct {
	// folder is the item the level was opened from (zero at the top).
	folder homeTabItem
	// key names the level's view setting: the tab ID or the folder's
	// FolderView.
	key   string
	title string
	items []homeTabItem
	// favs marks the favourited items (a heart on the tile), read with them.
	favs []bool
	// hero: the first item is drawn large in the row view (Recent).
	hero  bool
	focus int
	// scroll is the eased offset along the view's scroll axis, scrollTo its
	// target; placed says scroll is set.
	scroll, scrollTo float64
	placed           bool
	// hover: the pointer set the focus, which must not scroll the page.
	hover bool
	gen   uint64
	at    uint64
}

// homeTabsPage is one tab's stack of levels.
type homeTabsPage struct {
	levels []homeTabsLevel
}

// homeTabsHit is a tile as last drawn, for the pointer.
type homeTabsHit struct {
	i int
	r sdl.Rect
}

// homeTabs is the shell's state. Render/event thread only.
var homeTabs struct {
	on    bool
	pages [len(homeTabOrder)]homeTabsPage
	cur   string
	// autoStrip: the focus went to the strip only because the page it was on
	// is empty; the next tab with something on it takes it back.
	autoStrip bool

	// The visible tabs, in order, and the views, parsed from Custom.
	shown    []string
	shownRaw string
	shownOK  bool
	views    map[string]string
	viewsRaw string
	viewsOK  bool

	// The strip: the free span of the top bar (drawHeader), the strip drawn
	// in it, each tab's rect, and the eased pill and scroll.
	spanL, spanR int32
	spanOK       bool
	// brandYield: the wordmark gives the bar's room to the strip; brandW is
	// its width when last drawn.
	brandYield   bool
	brandW       int32
	strip        sdl.Rect
	tabHits      []sdl.Rect
	pillX, pillW float64
	pillPlaced   bool
	stripScroll  float64

	// The page slide: the level that was left, and from which side the new
	// one comes in.
	slide    uiEnter
	slideOld homeTabsLevel
	slideDir int
	slideOn  bool

	body     sdl.Rect
	back     sdl.Rect
	hits     []homeTabsHit
	tick     uint64
	dt       float64
	lastSync uint64
	anim     bool

	// renderer is the one the page was last drawn with: the Art lookups
	// (cache only) need it.
	renderer *sdl.Renderer
}

// ── Names ────────────────────────────────────────────────────────────────

// homeTabLabel is a tab's name on the strip.
func homeTabLabel(id string) string {
	switch id {
	case homeTabRecent:
		return "Recent"
	case homeTabMain:
		return "Main"
	case homeTabGames:
		return "Games"
	case homeTabEntertainment:
		return "Entertainment"
	case homeTabFavourites:
		return "Favourites"
	case homeTabCollections:
		return "Collections"
	}
	return id
}

// homeTabViewLabel is a view's name.
func homeTabViewLabel(v string) string {
	switch v {
	case homeTabViewRow:
		return "Rows"
	case homeTabViewList:
		return "List"
	case homeTabViewCarousel:
		return "Carousel"
	case homeTabViewCompact:
		return "Compact"
	}
	return "Grid"
}

// homeTabIndex is a tab's place in homeTabOrder, or -1.
func homeTabIndex(id string) int {
	for i, t := range homeTabOrder {
		if t == id {
			return i
		}
	}
	return -1
}

// homeTabIndexIn is id's place in list, or -1.
func homeTabIndexIn(list []string, id string) int {
	for i, t := range list {
		if t == id {
			return i
		}
	}
	return -1
}

// homeTabViewKnown reports whether v names a view.
func homeTabViewKnown(v string) bool {
	for _, k := range homeTabViews {
		if k == v {
			return true
		}
	}
	return false
}

// ── Settings ─────────────────────────────────────────────────────────────

// homeTabsCustom reads one of the shell's Custom strings.
func homeTabsCustom(c *Config, key string) string {
	if c == nil {
		return ""
	}
	s, _ := customString(c.Variables.Custom, key)
	return strings.TrimSpace(s)
}

// homeTabsWrite stores one of the shell's keys and saves jukauser.json (after
// the writer's own debounce) when it changed. It writes Custom directly:
// setStoreString's override sync would put a same-named mirror back.
func homeTabsWrite(c *Config, key, value string) {
	if c == nil {
		return
	}
	if c.Variables.Custom == nil {
		c.Variables.Custom = map[string]interface{}{}
	}
	if cur, ok := c.Variables.Custom[key].(string); ok && cur == value {
		return
	}
	c.Variables.Custom[key] = value
	saveUserConfigDebounced(userConfigFromVariables(c))
}

// parseHomeTabViews reads homeTabViews: "key=view" pairs split by ";". A pair
// naming no view is dropped.
func parseHomeTabViews(raw string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(raw, ";") {
		k, v, ok := strings.Cut(part, "=")
		k, v = strings.TrimSpace(k), strings.ToLower(strings.TrimSpace(v))
		if !ok || k == "" || !homeTabViewKnown(v) {
			continue
		}
		out[k] = v
	}
	return out
}

// formatHomeTabViews writes homeTabViews, keys in order.
func formatHomeTabViews(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(';')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(m[k])
	}
	return b.String()
}

// homeTabsViewsMap is homeTabViews parsed, re-read only when it changed.
func homeTabsViewsMap(c *Config) map[string]string {
	raw := homeTabsCustom(c, homeTabsViewsKey)
	if homeTabs.viewsOK && raw == homeTabs.viewsRaw && homeTabs.views != nil {
		return homeTabs.views
	}
	homeTabs.viewsRaw, homeTabs.viewsOK = raw, true
	homeTabs.views = parseHomeTabViews(raw)
	return homeTabs.views
}

// homeTabsDefaultView is a page's view until the user picks one: Recent is a
// row of covers to scroll through, everything else a grid.
func homeTabsDefaultView(key string) string {
	if key == homeTabRecent {
		return homeTabViewRow
	}
	return homeTabViewGrid
}

// homeTabsViewFor is the view the page stored under key is drawn in.
func homeTabsViewFor(c *Config, key string) string {
	if v, ok := homeTabsViewsMap(c)[key]; ok {
		return v
	}
	return homeTabsDefaultView(key)
}

// homeTabsSetView stores the view of the page under key.
func homeTabsSetView(c *Config, key, view string) {
	if c == nil || key == "" || !homeTabViewKnown(view) {
		return
	}
	m := homeTabsViewsMap(c)
	next := make(map[string]string, len(m)+1)
	for k, v := range m {
		next[k] = v
	}
	next[key] = view
	homeTabsWrite(c, homeTabsViewsKey, formatHomeTabViews(next))
}

// homeTabsNextView is the view after (step 1) or before (step -1) v.
func homeTabsNextView(v string, step int) string {
	n := len(homeTabViews)
	i := 0
	for k, name := range homeTabViews {
		if name == v {
			i = k
		}
	}
	return homeTabViews[((i+step)%n+n)%n]
}

// parseHomeTabsShown reads homeTabsShown: the tabs in their order, "-" before
// a hidden one. Unknown and repeated names are dropped; a tab the value does
// not name (a new one) is shown at the end.
func parseHomeTabsShown(raw string) ([]string, map[string]bool) {
	hidden := map[string]bool{}
	seen := map[string]bool{}
	order := make([]string, 0, len(homeTabOrder))
	for _, part := range strings.Split(raw, ",") {
		p := strings.ToLower(strings.TrimSpace(part))
		hide := strings.HasPrefix(p, "-")
		p = strings.TrimSpace(strings.TrimPrefix(p, "-"))
		if homeTabIndex(p) < 0 || seen[p] {
			continue
		}
		seen[p] = true
		order = append(order, p)
		if hide {
			hidden[p] = true
		}
	}
	for _, id := range homeTabOrder {
		if !seen[id] {
			order = append(order, id)
		}
	}
	return order, hidden
}

// formatHomeTabsShown writes homeTabsShown.
func formatHomeTabsShown(order []string, hidden map[string]bool) string {
	parts := make([]string, 0, len(order))
	for _, id := range order {
		if hidden[id] {
			parts = append(parts, "-"+id)
		} else {
			parts = append(parts, id)
		}
	}
	return strings.Join(parts, ",")
}

// homeTabsVisible is the tabs the strip shows, in order; never empty.
func homeTabsVisible(c *Config) []string {
	raw := homeTabsCustom(c, homeTabsShownKey)
	if homeTabs.shownOK && raw == homeTabs.shownRaw && len(homeTabs.shown) > 0 {
		return homeTabs.shown
	}
	order, hidden := parseHomeTabsShown(raw)
	vis := make([]string, 0, len(order))
	for _, id := range order {
		if !hidden[id] {
			vis = append(vis, id)
		}
	}
	if len(vis) == 0 {
		vis = append(vis, homeTabMain)
	}
	homeTabs.shown, homeTabs.shownRaw, homeTabs.shownOK = vis, raw, true
	return vis
}

// homeTabsRemember reports whether Home comes back on the tab it was left on
// (Settings → Display & theme → Remember last tab; on unless switched off).
func homeTabsRemember(c *Config) bool {
	if c == nil {
		return true
	}
	return customBool(c.Variables.Custom, homeTabsRememberKey, true)
}

// homeTabsNoteLast remembers the tab on screen, when it changed and the user
// wants it remembered.
func homeTabsNoteLast(c *Config, id string) {
	if c == nil || id == "" || !homeTabsRemember(c) {
		return
	}
	homeTabsWrite(c, homeTabLastKey, id)
}

// ── The arrangement on the live layout ───────────────────────────────────

// tabsActive reports whether Home is the Tabs arrangement right now. Edit
// Home borrows the old cards while it is open on it.
func (h *HomeLayout) tabsActive() bool {
	return h != nil && h.Arrangement == homeArrangementTabs && !homeEdit.open
}

// homeTabsOn reports whether the Tabs Home is up on screen.
func homeTabsOn() bool {
	return homeLayoutActive && homeLayout != nil && homeTabs.on && homeLayout.tabsActive()
}

// homeTabsAnimating reports whether the strip or the page is still moving,
// so the frame rate stays up (themeconsole.go homeConsoleAnimating).
func homeTabsAnimating() bool {
	return homeTabsOn() && (homeTabs.anim || homeTabs.slide.active)
}

// tabsSync keeps the shell in step with the arrangement and refreshes the
// page shown. refreshHomeLayout calls it every frame Home is shown, before
// dynSync.
func (h *HomeLayout) tabsSync(config *Config) {
	if h == nil {
		return
	}
	if !h.tabsActive() {
		if homeTabs.on {
			h.tabsOff()
		}
		return
	}
	if !homeTabs.on {
		h.tabsOn(config)
	}
	vis := homeTabsVisible(config)
	if homeTabIndexIn(vis, homeTabs.cur) < 0 {
		// The tab on screen was hidden (the L2 sheet, a hand edit).
		homeTabsEnter(config, vis[0], 0)
	}
	now := sdl.GetTicks64()
	force := homeTabs.lastSync != 0 && now-homeTabs.lastSync > homeTabsAwayMS
	homeTabs.lastSync = now
	homeTabsRebuild(config, force)
	h.tabsKeepFocus()
}

// tabsOn sets the shell up on the live layout: its two focus stops in, the
// cards and the search pill out, and the tab to start on.
func (h *HomeLayout) tabsOn(config *Config) {
	homeTabs.on = true
	homeTabs.pillPlaced = false
	homeTabs.slide.cancel()
	homeTabs.slideOn = false
	h.Items[homeTabsPageID] = HomeItem{ID: homeTabsPageID, Title: "Page", Icon: "apps"}
	h.Items[homeTabsStripID] = HomeItem{ID: homeTabsStripID, Title: "Tabs", Icon: "apps"}
	h.Focus.SetDynamic([]HomeFocusID{homeTabsPageID, homeTabsStripID}, []HomeFocusID{homeTabsPageID})
	vis := homeTabsVisible(config)
	start := homeTabMain
	if homeTabsRemember(config) {
		if last := homeTabsCustom(config, homeTabLastKey); homeTabIndexIn(vis, last) >= 0 {
			start = last
		}
	}
	if homeTabIndexIn(vis, start) < 0 {
		start = vis[0]
	}
	homeTabs.cur = ""
	homeTabsEnter(config, start, 0)
	h.tabsKeepFocus()
	if h.Focus.Set(homeTabsPageID) {
		h.Focus.Remember()
	}
	h.tabsKeepFocus()
}

// tabsKeepFocus keeps the shell's stops enabled and the cards off, and the
// focus off an empty page.
func (h *HomeLayout) tabsKeepFocus() {
	if !h.Focus.IsEnabled(homeTabsPageID) || !h.Focus.IsEnabled(homeTabsStripID) {
		h.Focus.SetDynamic([]HomeFocusID{homeTabsPageID, homeTabsStripID}, []HomeFocusID{homeTabsPageID})
	}
	if _, ok := h.Items[homeTabsPageID]; !ok {
		h.Items[homeTabsPageID] = HomeItem{ID: homeTabsPageID, Title: "Page", Icon: "apps"}
		h.Items[homeTabsStripID] = HomeItem{ID: homeTabsStripID, Title: "Tabs", Icon: "apps"}
	}
	for _, id := range homeBodyCards {
		if h.Focus.IsEnabled(id) {
			h.Focus.SetEnabled(id, false)
		}
	}
	if h.Focus.IsEnabled(HomeFocusSearch) {
		h.Focus.SetEnabled(HomeFocusSearch, false)
	}
	lvl := homeTabsLevelNow()
	empty := lvl == nil || len(lvl.items) == 0
	switch h.Focus.Current() {
	case homeTabsPageID:
		if empty && h.Focus.Set(homeTabsStripID) {
			homeTabs.autoStrip = true
		}
	case homeTabsStripID:
		if !empty && homeTabs.autoStrip && h.Focus.Set(homeTabsPageID) {
			homeTabs.autoStrip = false
		}
	}
}

// tabsOff hands Home back to the other arrangements.
func (h *HomeLayout) tabsOff() {
	homeTabs.on = false
	homeTabsEditClose(nil)
	delete(h.Items, homeTabsPageID)
	delete(h.Items, homeTabsStripID)
	homeTabs.strip, homeTabs.body, homeTabs.back = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
	homeTabs.tabHits = homeTabs.tabHits[:0]
	homeTabs.hits = homeTabs.hits[:0]
	homeTabs.spanOK = false
	homeTabs.slide.cancel()
	homeTabs.slideOn = false
	h.Focus.SetEnabled(HomeFocusSearch, true)
	if h.dyn.active {
		// The tile layouts have replaced the stops and switch the cards
		// themselves.
		return
	}
	for _, id := range homeBodyCards {
		h.Focus.SetEnabled(id, true)
	}
	h.Focus.SetDynamic(nil, nil)
}

// ── Pages ────────────────────────────────────────────────────────────────

// homeTabsPageOf is a tab's page, or nil.
func homeTabsPageOf(id string) *homeTabsPage {
	i := homeTabIndex(id)
	if i < 0 {
		return nil
	}
	return &homeTabs.pages[i]
}

// homeTabsLevelNow is the level on screen, or nil.
func homeTabsLevelNow() *homeTabsLevel {
	p := homeTabsPageOf(homeTabs.cur)
	if p == nil || len(p.levels) == 0 {
		return nil
	}
	return &p.levels[len(p.levels)-1]
}

// homeTabsFocusedItem is the item the page's focus is on.
func homeTabsFocusedItem() (homeTabItem, bool) {
	lvl := homeTabsLevelNow()
	if lvl == nil || lvl.focus < 0 || lvl.focus >= len(lvl.items) {
		return homeTabItem{}, false
	}
	return lvl.items[lvl.focus], true
}

// homeTabsBuild asks the tab's provider (or the folder) for the level's
// items; hero says the first one is drawn large in the row view.
func homeTabsBuild(c *Config, tab string, sub bool, folder homeTabItem) ([]homeTabItem, bool) {
	if sub {
		if folder.Children == nil {
			return nil, false
		}
		return folder.Children(c), false
	}
	switch tab {
	case homeTabRecent:
		return homeTabsRecentItems(c)
	case homeTabMain:
		return homeTabsMainItems(c), false
	case homeTabGames:
		return homeTabsGamesItems(c), false
	case homeTabEntertainment:
		return homeTabsEntertainmentItems(c), false
	case homeTabFavourites:
		return homeTabsFavouriteItems(c), false
	case homeTabCollections:
		return homeTabsCollectionItems(c), false
	}
	return nil, false
}

// homeTabsRebuild reads the level on screen again when force is set, when
// the providers' generation moved, or — Recent's top level — every few
// seconds. The focus stays on the item it was on (by Key), or the nearest
// place.
func homeTabsRebuild(c *Config, force bool) {
	p := homeTabsPageOf(homeTabs.cur)
	if p == nil {
		return
	}
	if len(p.levels) == 0 {
		p.levels = append(p.levels, homeTabsLevel{key: homeTabs.cur, title: homeTabLabel(homeTabs.cur)})
		force = true
	}
	lvl := &p.levels[len(p.levels)-1]
	gen := homeTabsContentGen.Load()
	now := sdl.GetTicks64()
	stale := homeTabs.cur == homeTabRecent && len(p.levels) == 1 && now-lvl.at >= homeTabsRecentEveryMS
	if !force && lvl.gen == gen && !stale && lvl.at != 0 {
		return
	}
	keep := ""
	if lvl.focus >= 0 && lvl.focus < len(lvl.items) {
		keep = lvl.items[lvl.focus].Key
	}
	items, hero := homeTabsBuild(c, homeTabs.cur, len(p.levels) > 1, lvl.folder)
	// The generation after the build: a provider that touches it while it
	// builds (a scan it starts) must not have the page rebuilt every frame.
	gen = homeTabsContentGen.Load()
	lvl.items, lvl.hero, lvl.gen, lvl.at = items, hero, gen, max(now, 1)
	lvl.focus = homeTabsFindKey(items, keep, lvl.focus)
	// A fresh slice: a sliding-out copy of this level may still hold the old.
	lvl.favs = make([]bool, len(items))
	for i, it := range items {
		lvl.favs[i] = it.Ref != "" && homeTabsIsFavourite(it.Ref)
	}
}

// homeTabsFindKey is the index of the item keyed key, or old clamped to the
// list.
func homeTabsFindKey(items []homeTabItem, key string, old int) int {
	if key != "" {
		for i, it := range items {
			if it.Key == key {
				return i
			}
		}
	}
	if old >= len(items) {
		old = len(items) - 1
	}
	if old < 0 {
		old = 0
	}
	return old
}

// homeTabsSlideFrom starts the page slide: old is the level being left, dir
// the side the new one comes from (1 = from the right).
func homeTabsSlideFrom(old homeTabsLevel, dir int) {
	if dir == 0 || homeLayout == nil || !homeLayout.Motion {
		homeTabs.slide.cancel()
		homeTabs.slideOn = false
		return
	}
	homeTabs.slideOld, homeTabs.slideDir, homeTabs.slideOn = old, dir, true
	homeTabs.slide.begin(uiClockMS(), homeTabsSlideMS)
}

// homeTabsEnter puts tab id on screen; dir is the side its page slides in
// from (0 = no slide).
func homeTabsEnter(c *Config, id string, dir int) {
	if id == homeTabs.cur || homeTabIndex(id) < 0 {
		return
	}
	if old := homeTabsLevelNow(); old != nil {
		homeTabsSlideFrom(*old, dir)
	} else {
		homeTabsSlideFrom(homeTabsLevel{}, 0)
	}
	homeTabs.cur = id
	homeTabsRebuild(c, true)
	homeTabsNoteLast(c, id)
}

// homeTabsGo changes tab from a press, keeping the focus on the page when it
// was there and the new page has something to focus.
func homeTabsGo(c *Config, id string, dir int) {
	h := homeLayout
	if h == nil {
		return
	}
	cur := h.Focus.Current()
	onPage := cur == homeTabsPageID || (cur == homeTabsStripID && homeTabs.autoStrip)
	homeTabsEnter(c, id, dir)
	lvl := homeTabsLevelNow()
	empty := lvl == nil || len(lvl.items) == 0
	switch {
	case onPage && !empty:
		h.Focus.Set(homeTabsPageID)
		homeTabs.autoStrip = false
	case onPage && empty:
		h.Focus.Set(homeTabsStripID)
		homeTabs.autoStrip = true
	}
	h.Focus.Remember()
}

// homeTabsStepTab moves step tabs along the strip, round from the last to
// the first (L1/R1, Tab).
func homeTabsStepTab(c *Config, step int) {
	vis := homeTabsVisible(c)
	if len(vis) <= 1 {
		return
	}
	i := homeTabIndexIn(vis, homeTabs.cur)
	if i < 0 {
		i = 0
	}
	n := len(vis)
	homeTabsGo(c, vis[((i+step)%n+n)%n], step)
	PlayNavSound()
}

// homeTabsOpenFolder opens a folder item as a sub-page.
func homeTabsOpenFolder(c *Config, it homeTabItem) {
	p := homeTabsPageOf(homeTabs.cur)
	if p == nil || len(p.levels) == 0 {
		return
	}
	key := it.FolderView
	if key == "" {
		key = p.levels[0].key
	}
	homeTabsSlideFrom(p.levels[len(p.levels)-1], 1)
	p.levels = append(p.levels, homeTabsLevel{folder: it, key: key, title: it.Title})
	homeTabsRebuild(c, true)
	if homeLayout != nil {
		lvl := homeTabsLevelNow()
		if lvl != nil && len(lvl.items) > 0 {
			homeLayout.Focus.Set(homeTabsPageID)
		} else {
			homeLayout.Focus.Set(homeTabsStripID)
			homeTabs.autoStrip = true
		}
	}
}

// homeTabsBack leaves a sub-page for its parent (B, Esc, the back chip). It
// reports false at a tab's top level, where B keeps Home's meaning.
func homeTabsBack(c *Config) bool {
	if !homeTabsOn() {
		return false
	}
	p := homeTabsPageOf(homeTabs.cur)
	if p == nil || len(p.levels) <= 1 {
		return false
	}
	old := p.levels[len(p.levels)-1]
	p.levels = p.levels[:len(p.levels)-1]
	homeTabsSlideFrom(old, -1)
	homeTabsRebuild(c, false)
	if lvl := homeTabsLevelNow(); lvl != nil && len(lvl.items) > 0 {
		homeLayout.Focus.Set(homeTabsPageID)
		homeTabs.autoStrip = false
	}
	PlayBackSound()
	return true
}

// homeTabsCycleView steps the view of the page on screen (A on the strip, a
// click on the active tab) and says which it is now.
func homeTabsCycleView(c *Config) {
	lvl := homeTabsLevelNow()
	if lvl == nil || c == nil {
		return
	}
	next := homeTabsNextView(homeTabsViewFor(c, lvl.key), 1)
	homeTabsSetView(c, lvl.key, next)
	lvl.placed = false
	ShowToast("View: "+homeTabViewLabel(next), ToastKindInfo)
}

// ── Activation and the buttons ───────────────────────────────────────────

// homeTabsActivate is A (or a click) on one of the shell's stops: the page
// opens its focused item, the strip changes the view. It reports whether id
// was the shell's. homeActivate has played the sound.
func homeTabsActivate(c *Config, id HomeFocusID) bool {
	if !homeTabsOn() {
		return false
	}
	switch id {
	case homeTabsStripID:
		homeTabsCycleView(c)
		return true
	case homeTabsPageID:
		it, ok := homeTabsFocusedItem()
		if !ok {
			return true
		}
		if it.Folder {
			homeTabsOpenFolder(c, it)
			return true
		}
		if it.Activate != nil {
			it.Activate(c)
		}
		return true
	}
	return false
}

// homeTabsX is X on Home: the item sheet for the focused item. It reports
// false when the focus is not on the page, so X keeps its other jobs.
func homeTabsX(c *Config) bool {
	if !homeTabsOn() || homeLayout.Focus.Current() != homeTabsPageID {
		return false
	}
	it, ok := homeTabsFocusedItem()
	if !ok {
		return true
	}
	parent := ""
	if lvl := homeTabsLevelNow(); lvl != nil {
		parent = lvl.folder.FolderView
	}
	PlayActivateSound()
	homeTabsItemSheetOpen(c, homeTabs.cur, parent, it)
	return true
}

// homeTabsShoulder is L1/R1 on the Tabs Home: the previous or next tab. Only
// while Home itself is in front (a menu or a panel over it keeps its own).
func homeTabsShoulder(s padShoulder, c *Config) bool {
	if !homeTabsOn() || (s != padL1 && s != padR1) {
		return false
	}
	if homeTabsItemSheetActive() || homeTabsEdit.open {
		return true
	}
	if !homeOwnsPointer() {
		return false
	}
	step := 1
	if s == padL1 {
		step = -1
	}
	homeTabsStepTab(c, step)
	return true
}

// homeTabsSheetShoulder gives the shoulders to a sheet open over the Tabs
// Home: L2 closes the arranging sheet, the rest do nothing under it.
func homeTabsSheetShoulder(s padShoulder, c *Config) bool {
	if !homeTabsOn() {
		return false
	}
	if homeTabsEdit.open {
		if s == padL2 {
			PlayBackSound()
			homeTabsEditClose(c)
		}
		return true
	}
	return homeTabsItemSheetActive()
}

// homeTabsKey is a key the Tabs Home answers: Tab / Shift+Tab and PgUp /
// PgDn change tab. It reports false for anything else.
func homeTabsKey(e *sdl.KeyboardEvent, c *Config) bool {
	if !homeTabsOn() || e == nil {
		return false
	}
	step := 0
	switch e.Keysym.Sym {
	case sdl.K_TAB:
		step = 1
		if e.Keysym.Mod&sdl.KMOD_SHIFT != 0 {
			step = -1
		}
	case sdl.K_PAGEUP:
		step = -1
	case sdl.K_PAGEDOWN:
		step = 1
	}
	if step == 0 || e.Keysym.Mod&(sdl.KMOD_CTRL|sdl.KMOD_ALT) != 0 {
		return false
	}
	homeTabsStepTab(c, step)
	return true
}

// ── Focus ────────────────────────────────────────────────────────────────

// homeTabsHeaderIDs are the top bar's controls, the stops either side of the
// strip.
var homeTabsHeaderIDs = [...]HomeFocusID{
	HomeFocusBrightness, HomeFocusVolume, HomeFocusBluetooth, HomeFocusWiFi, HomeFocusWeather,
	HomeFocusClock, HomeFocusNowPlaying, HomeFocusUpdate, HomeFocusTrophy, HomeFocusBug, HomeFocusBell,
}

// tabsHeaderRight is the top bar control drawn nearest right of the strip:
// where Right from its last tab goes.
func (h *HomeLayout) tabsHeaderRight() HomeFocusID {
	best, bestX := HomeFocusID(""), int32(math.MaxInt32)
	for _, id := range homeTabsHeaderIDs {
		r := h.Items[id].Rect
		if !h.Focus.IsEnabled(id) || r.W <= 0 || r.X < homeTabs.strip.X {
			continue
		}
		if r.X < bestX {
			best, bestX = id, r.X
		}
	}
	return best
}

// tabsPageOrStrip focuses the page, or the strip when the page is empty.
func (h *HomeLayout) tabsPageOrStrip() {
	if lvl := homeTabsLevelNow(); lvl != nil && len(lvl.items) > 0 {
		h.Focus.Set(homeTabsPageID)
		homeTabs.autoStrip = false
		return
	}
	h.Focus.Set(homeTabsStripID)
}

// tabsMove is Move on the Tabs Home.
func (h *HomeLayout) tabsMove(direction FocusDirection) HomeFocusID {
	cur := h.Focus.Current()
	switch cur {
	case homeTabsPageID:
		if lvl := homeTabsLevelNow(); lvl != nil && len(lvl.items) > 0 {
			next, leave := h.tabsStepFrom(lvl, direction)
			if !leave {
				if next != lvl.focus {
					lvl.focus = next
					lvl.hover = false
					PlayNavSound()
					if h.Motion {
						h.focusEnter.begin(uiClockMS(), uiFocusBounceMS)
					}
				}
				return cur
			}
		}
		switch direction {
		case FocusUp:
			h.Focus.Set(homeTabsStripID)
			homeTabs.autoStrip = false
		case FocusDown:
			if h.Focus.IsEnabled(HomeFocusExit) {
				h.Focus.Set(HomeFocusExit)
			}
		}
	case homeTabsStripID:
		switch direction {
		case FocusLeft, FocusRight:
			vis := homeTabsVisible(appConfig)
			step := 1
			if direction == FocusLeft {
				step = -1
			}
			// The user walks the strip on purpose now: a tab with something
			// on it must not pull the focus down into its page.
			homeTabs.autoStrip = false
			if j := homeTabIndexIn(vis, homeTabs.cur) + step; j >= 0 && j < len(vis) {
				homeTabsEnter(appConfig, vis[j], step)
				PlayNavSound()
				return cur
			}
			if direction == FocusRight {
				if id := h.tabsHeaderRight(); id != "" {
					h.Focus.Set(id)
				}
			}
		case FocusDown:
			if lvl := homeTabsLevelNow(); lvl != nil && len(lvl.items) > 0 {
				h.Focus.Set(homeTabsPageID)
				homeTabs.autoStrip = false
			} else if h.Focus.IsEnabled(HomeFocusExit) {
				h.Focus.Set(HomeFocusExit)
			}
		}
	case HomeFocusExit:
		if direction == FocusUp {
			h.tabsPageOrStrip()
		} else {
			h.Focus.Move(direction)
		}
	default:
		// A top bar control: Down drops into the page; Left past the first
		// one comes back to the strip.
		if direction == FocusDown {
			h.tabsPageOrStrip()
			break
		}
		if next := h.Focus.Move(direction); next == cur && direction == FocusLeft {
			h.Focus.Set(homeTabsStripID)
		}
	}
	if c := h.Focus.Current(); c != cur {
		h.Focus.Remember()
	}
	return h.Focus.Current()
}

// tabsStepFrom is one D-pad step inside the page: the item reached, or
// leave=true when the press leaves the page (Up from the top, Down from the
// bottom; Left and Right stop at the ends).
func (h *HomeLayout) tabsStepFrom(lvl *homeTabsLevel, direction FocusDirection) (int, bool) {
	n := len(lvl.items)
	cur := lvl.focus
	if cur < 0 || cur >= n {
		return 0, false
	}
	view := homeTabsViewFor(appConfig, lvl.key)
	switch view {
	case homeTabViewList:
		switch direction {
		case FocusUp:
			if cur == 0 {
				return cur, true
			}
			return cur - 1, false
		case FocusDown:
			if cur == n-1 {
				return cur, true
			}
			return cur + 1, false
		}
		return cur, false
	case homeTabViewCarousel:
		switch direction {
		case FocusLeft:
			return max(cur-1, 0), false
		case FocusRight:
			return min(cur+1, n-1), false
		}
		return cur, true
	}
	g := h.tabsGeom(view, n, lvl.hero, h.tabsBody(lvl))
	if next := homeTabsNeighbor(g.rects, cur, direction); next >= 0 {
		return next, false
	}
	if direction == FocusUp || direction == FocusDown {
		return cur, true
	}
	return cur, false
}

// homeTabsNeighbor is the item a press from rects[cur] reaches by the drawn
// geometry: the nearest one wholly past its edge in that direction, one on
// the same line before one off it (the rule Home's grids step by). -1 when
// there is none.
func homeTabsNeighbor(rects []sdl.Rect, cur int, direction FocusDirection) int {
	if cur < 0 || cur >= len(rects) {
		return -1
	}
	const tol = 4
	f := rects[cur]
	best, bestScore := -1, int64(math.MaxInt64)
	for i, r := range rects {
		if i == cur || r.W <= 0 || r.H <= 0 {
			continue
		}
		var gap, offset int32
		switch direction {
		case FocusRight:
			if r.X < f.X+f.W-tol {
				continue
			}
			gap, offset = r.X-(f.X+f.W), spanOffset(f.Y, f.H, r.Y, r.H)
		case FocusLeft:
			if r.X+r.W > f.X+tol {
				continue
			}
			gap, offset = f.X-(r.X+r.W), spanOffset(f.Y, f.H, r.Y, r.H)
		case FocusDown:
			if r.Y < f.Y+f.H-tol {
				continue
			}
			gap, offset = r.Y-(f.Y+f.H), spanOffset(f.X, f.W, r.X, r.W)
		case FocusUp:
			if r.Y+r.H > f.Y+tol {
				continue
			}
			gap, offset = f.Y-(r.Y+r.H), spanOffset(f.X, f.W, r.X, r.W)
		default:
			return -1
		}
		if offset > 0 && (direction == FocusLeft || direction == FocusRight) {
			// Sideways stays on the line: the end of a grid row is an end.
			continue
		}
		if gap < 0 {
			gap = 0
		}
		score := int64(gap) + 2*int64(offset)
		if score < bestScore {
			best, bestScore = i, score
		}
	}
	return best
}

// tabsWheel is the wheel over the Tabs Home: it walks the page one step at a
// time, along the axis the view scrolls.
func (h *HomeLayout) tabsWheel(dy int32) bool {
	if !homeTabsOn() {
		return false
	}
	if homeTabsEdit.open {
		return true
	}
	lvl := homeTabsLevelNow()
	if lvl == nil || len(lvl.items) == 0 || dy == 0 {
		return true
	}
	view := homeTabsViewFor(appConfig, lvl.key)
	horiz := view == homeTabViewRow || view == homeTabViewCarousel
	dir := FocusDown
	switch {
	case dy > 0 && horiz:
		dir = FocusLeft
	case dy < 0 && horiz:
		dir = FocusRight
	case dy > 0:
		dir = FocusUp
	}
	if next, leave := h.tabsStepFrom(lvl, dir); !leave && next != lvl.focus {
		lvl.focus = next
		lvl.hover = false
		h.Focus.Set(homeTabsPageID)
		PlayNavSound()
	}
	return true
}

// tabsFocusAt is the pointer's hover over the Tabs Home. handled says the
// point is the shell's (the strip, the page) and nothing under it may take
// the focus; focused that the focus moved to it.
func (h *HomeLayout) tabsFocusAt(x, y int32) (handled, focused bool) {
	if !homeTabsOn() {
		return false, false
	}
	if homeTabsEdit.open || homeTabsItemSheetActive() {
		return true, false
	}
	if pointInRect(x, y, homeTabs.strip) {
		homeTabs.autoStrip = false
		return true, h.Focus.Set(homeTabsStripID)
	}
	if lvl := homeTabsLevelNow(); lvl != nil {
		for _, hit := range homeTabs.hits {
			if hit.i < len(lvl.items) && pointInRect(x, y, hit.r) {
				if lvl.focus != hit.i {
					lvl.focus = hit.i
					lvl.hover = true
				}
				return true, h.Focus.Set(homeTabsPageID)
			}
		}
	}
	if pointInRect(x, y, homeTabs.back) || pointInRect(x, y, homeTabs.body) {
		return true, false
	}
	return false, false
}

// homeTabsClick is a click or a tap on the Tabs Home: a tab selects it (the
// active one changes the view), the back chip leaves the sub-page, a tile
// focuses and opens. It reports whether the click was the shell's.
func homeTabsClick(mx, my int32, c *Config) bool {
	if !homeTabsOn() {
		return false
	}
	h := homeLayout
	if homeTabsEdit.open {
		homeTabsEditClick(c, mx, my)
		return true
	}
	if homeTabsItemSheetActive() {
		return true
	}
	vis := homeTabsVisible(c)
	for i, r := range homeTabs.tabHits {
		if i >= len(vis) || !pointInRect(mx, my, r) {
			continue
		}
		cur := homeTabIndexIn(vis, homeTabs.cur)
		if i == cur {
			PlayActivateSound()
			homeTabsCycleView(c)
			return true
		}
		dir := 1
		if i < cur {
			dir = -1
		}
		PlayNavSound()
		homeTabsGo(c, vis[i], dir)
		return true
	}
	// The strip between and beside the tabs is not a button: a click there
	// must not reach FocusAt, which would focus the strip and change the view.
	if pointInRect(mx, my, homeTabs.strip) {
		return true
	}
	if pointInRect(mx, my, homeTabs.back) {
		homeTabsBack(c)
		return true
	}
	if lvl := homeTabsLevelNow(); lvl != nil {
		for _, hit := range homeTabs.hits {
			if hit.i >= len(lvl.items) || !pointInRect(mx, my, hit.r) {
				continue
			}
			lvl.focus, lvl.hover = hit.i, true
			if h.Focus.Set(homeTabsPageID) {
				h.Focus.Remember()
			}
			homeActivate(c)
			return true
		}
	}
	return pointInRect(mx, my, homeTabs.body)
}

// homeTabsRightClick opens the item sheet for the tile under the pointer.
func homeTabsRightClick(c *Config, mx, my int32) bool {
	if !homeTabsOn() {
		return false
	}
	if homeTabsEdit.open || homeTabsItemSheetActive() {
		return true
	}
	lvl := homeTabsLevelNow()
	if lvl == nil {
		return false
	}
	for _, hit := range homeTabs.hits {
		if hit.i >= len(lvl.items) || !pointInRect(mx, my, hit.r) {
			continue
		}
		lvl.focus, lvl.hover = hit.i, true
		homeLayout.Focus.Set(homeTabsPageID)
		return homeTabsX(c)
	}
	return false
}

// ── The footer ───────────────────────────────────────────────────────────

// homeTabsFooter is the footer's hint row on the Tabs Home.
func homeTabsFooter(showBack bool) [len(homeFooterHintsStart)]footerHint {
	if homeTabsEdit.open {
		return homeTabsEditFooter()
	}
	set := [len(homeFooterHintsStart)]footerHint{
		{button: "D-Pad", label: "Navigate"},
		{button: "Y", label: "Search"},
		{button: "L2", label: "Edit"},
		{button: "X", label: "Options"},
		{button: "A", label: "Open"},
		{button: "L1/R1", label: "Tabs"},
		{button: "Start", label: "Menu"},
		{}, // homeFooterVoiceSlot
	}
	cur := HomeFocusID("")
	if homeLayout != nil {
		cur = homeLayout.Focus.Current()
	}
	switch cur {
	case homeTabsStripID:
		set[3] = footerHint{}
		set[4].label = "View"
	case homeTabsPageID:
		if it, ok := homeTabsFocusedItem(); ok && it.Folder {
			set[4].label = "Open"
		}
	default:
		set[3] = footerHint{}
	}
	if p := homeTabsPageOf(homeTabs.cur); (p != nil && len(p.levels) > 1) || showBack {
		set[6] = footerHint{button: "B", label: "Back"}
	}
	// A video playing beside Home has the shoulders (shoulders.go).
	if !hintsUseKeyboard() && snapshotVideoState().phase != "idle" {
		set[2], set[5] = footerHint{}, footerHint{}
	}
	return set
}
