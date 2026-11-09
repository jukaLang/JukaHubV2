package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// --- Favorites types ---

type FavoriteItem struct {
	Type      string      `json:"type"`
	Data      interface{} `json:"data"`
	Timestamp time.Time   `json:"timestamp"`
}

func (f FavoriteItem) MarshalJSON() ([]byte, error) {
	type Alias FavoriteItem
	var dataBytes []byte
	var err error
	switch f.Type {
	case "video":
		if v, ok := f.Data.(VideoInfo); ok {
			dataBytes, err = json.Marshal(v)
		} else {
			dataBytes, err = json.Marshal(f.Data)
		}
	case "file", "iptv", "audio":
		if fe, ok := f.Data.(FileEntry); ok {
			dataBytes, err = json.Marshal(fe)
		} else {
			dataBytes, err = json.Marshal(f.Data)
		}
	case "podcast":
		if ep, ok := f.Data.(podcastEpisode); ok {
			dataBytes, err = json.Marshal(ep)
		} else {
			dataBytes, err = json.Marshal(f.Data)
		}
	default:
		dataBytes, err = json.Marshal(f.Data)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(&struct {
		Type      string
		Data      json.RawMessage
		Timestamp time.Time
	}{
		Type:      f.Type,
		Data:      dataBytes,
		Timestamp: f.Timestamp,
	})
}

func (f *FavoriteItem) UnmarshalJSON(data []byte) error {
	type Alias FavoriteItem
	aux := &struct {
		Data json.RawMessage
		*Alias
	}{
		Alias: (*Alias)(f),
	}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	switch f.Type {
	case "video":
		var v VideoInfo
		if err := json.Unmarshal(aux.Data, &v); err == nil {
			f.Data = v
			return nil
		}
	case "file", "iptv", "audio":
		var fe FileEntry
		if err := json.Unmarshal(aux.Data, &fe); err == nil {
			f.Data = fe
			return nil
		}
	case "podcast":
		var ep podcastEpisode
		if err := json.Unmarshal(aux.Data, &ep); err == nil {
			f.Data = ep
			return nil
		}
	}
	var raw interface{}
	if err := json.Unmarshal(aux.Data, &raw); err != nil {
		return err
	}
	f.Data = raw
	return nil
}

type FavoritesStore struct {
	Videos []FavoriteItem `json:"videos"`
	Recent []FavoriteItem `json:"recent"`
	Files  []FavoriteItem `json:"files"`
	IPTV   []FavoriteItem `json:"iptv"`
}

func (f *FavoriteItem) Label() string {
	switch f.Type {
	case "video":
		if v, ok := f.Data.(VideoInfo); ok {
			if v.Title != "" {
				return v.Title
			}
			return v.GetURL()
		}
	case "file":
		if fe, ok := f.Data.(FileEntry); ok {
			return fe.Name
		}
	case "audio":
		// A track recorded by the Continue card (home_continue.go) carries its
		// display title as the entry name.
		if fe, ok := f.Data.(FileEntry); ok {
			return fe.Name
		}
	case "iptv":
		if fe, ok := f.Data.(FileEntry); ok {
			return fe.Name
		}
	case "podcast":
		if ep, ok := f.Data.(podcastEpisode); ok {
			if ep.Title != "" {
				return ep.Title
			}
			return ep.URL
		}
	case "game":
		if g, ok := f.Data.(recentGame); ok {
			if g.Title != "" {
				return g.Title
			}
			return filepath.Base(g.Path)
		}
	}
	return fmt.Sprintf("%v", f.Data)
}

func (f *FavoriteItem) Play(config *Config) {
	switch f.Type {
	case "video":
		if v, ok := f.Data.(VideoInfo); ok {
			// v.Position flows through to the embedded player so the Continue
			// card can truly resume where the user left off. A saved video keeps
			// its own copy of the metadata, so its resume offset is read from the
			// recent timeline when the copy does not carry one.
			if v.Position <= 0 {
				v.Position = favoriteResumePosition(v.GetURL())
			}
			playVideoInfo(config, v)
		}
	case "audio":
		if fe, ok := f.Data.(FileEntry); ok {
			// Songs open the audio player with the whole folder as playlist.
			openAudioPlayer(config, fe.Path)
		}
	case "iptv":
		if fe, ok := f.Data.(FileEntry); ok {
			playStream(config, fe.Path)
		}
	case "file":
		if fe, ok := f.Data.(FileEntry); ok {
			openInExplorer(fe.Path)
		}
	case "game":
		if g, ok := f.Data.(recentGame); ok {
			playRecentGame(g)
		}
	case "podcast":
		if ep, ok := f.Data.(podcastEpisode); ok {
			if strings.TrimSpace(ep.URL) == "" {
				ShowToast("That episode has no audio link", ToastKindWarning)
				return
			}
			// The audio player resumes a long episode from the saved position
			// and continues through the show's queue, exactly as the Podcasts
			// panel does.
			openAudioPlayer(config, ep.URL)
		}
	}
}

// --- Favorites global state ---

var (
	favoritesStore      FavoritesStore
	favoritesMutex      sync.Mutex
	favoritesCurrentTab int
	favoritesFocusIndex int
)

func loadFavorites() {
	favoritesMutex.Lock()
	defer favoritesMutex.Unlock()
	if userConfigCache != nil && userConfigCache.Favorites != nil {
		favoritesStore = *userConfigCache.Favorites
	}
	if favoritesStore.Videos == nil {
		favoritesStore.Videos = []FavoriteItem{}
	}
	if favoritesStore.Recent == nil {
		favoritesStore.Recent = []FavoriteItem{}
	}
	if favoritesStore.Files == nil {
		favoritesStore.Files = []FavoriteItem{}
	}
	if favoritesStore.IPTV == nil {
		favoritesStore.IPTV = []FavoriteItem{}
	}
	if favoritesFocusIndex < 0 {
		favoritesFocusIndex = 0
	}
}

func loadFavoritesFrom(path string) {
	favoritesMutex.Lock()
	defer favoritesMutex.Unlock()
	// A damaged favorites.json used to unmarshal straight into the live store:
	// a half document left half a list, and the next save wrote that over the
	// real one. The save now leaves a .bak, and the load reads it into a local
	// value so nothing is replaced unless a whole list came back.
	var loaded FavoritesStore
	if _, err := readJSONWithBackup(path, &loaded); err != nil {
		// An absent file is a first run; anything else that got this far also
		// failed the backup, so the list really is gone and the log says why.
		if !os.IsNotExist(err) {
			logWarn("favorites", "loadFavorites: %v", err)
		}
		return
	}
	favoritesStore = loaded
	if favoritesStore.Videos == nil {
		favoritesStore.Videos = []FavoriteItem{}
	}
	if favoritesStore.Recent == nil {
		favoritesStore.Recent = []FavoriteItem{}
	}
	if favoritesStore.Files == nil {
		favoritesStore.Files = []FavoriteItem{}
	}
	if favoritesStore.IPTV == nil {
		favoritesStore.IPTV = []FavoriteItem{}
	}
	if favoritesFocusIndex < 0 {
		favoritesFocusIndex = 0
	}
}

func saveFavorites() {
	favoritesMutex.Lock()
	fs := favoritesStore
	favoritesMutex.Unlock()
	if userConfigCache == nil {
		userConfigCache = &UserConfig{Variables: UserVariables{Custom: make(map[string]interface{})}}
	}
	userConfigCache.Favorites = &fs
	saveUserConfig(userConfigCache)
}

func saveFavoritesTo(path string) {
	favoritesMutex.Lock()
	defer favoritesMutex.Unlock()
	data, err := json.MarshalIndent(favoritesStore, "", "  ")
	if err != nil {
		logWarn("favorites", "saveFavorites: marshal error: %v", err)
		return
	}
	// Same crash-safe path as the notes and the config: the legacy favorites
	// file is still read by older cards, so it must not be truncated by a
	// power cut either.
	if err := AtomicWrite(path, data, 0644); err != nil {
		logWarn("favorites", "saveFavorites: write error: %v", err)
	}
}

func addFavoriteVideo(v VideoInfo) {
	favoritesMutex.Lock()
	defer favoritesMutex.Unlock()
	favoritesStore.Videos = removeDuplicateFavorite(favoritesStore.Videos, v.GetURL(), "video", v)
	favoritesStore.Videos = append([]FavoriteItem{{Type: "video", Data: v, Timestamp: time.Now()}}, favoritesStore.Videos...)
}

// addRecentVideo records a video at the top of the recently-played list. The
// incoming VideoInfo keeps any Position it carries (the Continue card passes
// the saved offset so resume survives a scene round-trip).
func addRecentVideo(v VideoInfo) {
	favoritesMutex.Lock()
	defer favoritesMutex.Unlock()
	if v.GetURL() == "" {
		return
	}
	favoritesStore.Recent = removeDuplicateFavorite(favoritesStore.Recent, v.GetURL(), "video", v)
	favoritesStore.Recent = append([]FavoriteItem{{Type: "video", Data: v, Timestamp: time.Now()}}, favoritesStore.Recent...)
	const maxRecent = 20
	if len(favoritesStore.Recent) > maxRecent {
		favoritesStore.Recent = favoritesStore.Recent[:maxRecent]
	}
}

// updateRecentPosition records how far into a video the user has watched so
// the Continue card can show real resume progress. Videos watched to the end
// reset to 0 so the next play starts fresh.
func updateRecentPosition(url string, positionSec float64) {
	if url == "" || positionSec < 0 {
		return
	}
	favoritesMutex.Lock()
	defer favoritesMutex.Unlock()
	for i := range favoritesStore.Recent {
		it := &favoritesStore.Recent[i]
		if it.Type != "video" {
			continue
		}
		v, ok := it.Data.(VideoInfo)
		if !ok || v.GetURL() != url {
			continue
		}
		if v.Duration > 0 && positionSec >= v.Duration*0.95 {
			v.Position = 0
		} else {
			v.Position = positionSec
		}
		it.Data = v
		return
	}
}

// addRecentPodcast puts a started episode on the same timeline as videos and
// games, so Continue answers "what was I in the middle of?" across every media
// type instead of only the ones that live in this store.
func addRecentPodcast(ep podcastEpisode) {
	url := strings.TrimSpace(ep.URL)
	if url == "" {
		return
	}
	favoritesMutex.Lock()
	defer favoritesMutex.Unlock()
	kept := favoritesStore.Recent[:0]
	for _, item := range favoritesStore.Recent {
		if item.Type == "podcast" {
			if old, ok := item.Data.(podcastEpisode); ok && strings.TrimSpace(old.URL) == url {
				continue
			}
		}
		kept = append(kept, item)
	}
	favoritesStore.Recent = append([]FavoriteItem{{Type: "podcast", Data: ep, Timestamp: time.Now()}}, kept...)
	const maxRecent = 20
	if len(favoritesStore.Recent) > maxRecent {
		favoritesStore.Recent = favoritesStore.Recent[:maxRecent]
	}
}

func removeDuplicateFavorite(list []FavoriteItem, key string, itemType string, data interface{}) []FavoriteItem {
	filtered := make([]FavoriteItem, 0, len(list))
	for _, item := range list {
		if item.Type == itemType {
			switch item.Type {
			case "video":
				if v, ok := item.Data.(VideoInfo); ok && v.GetURL() == key {
					continue
				}
			case "file", "iptv":
				if fe, ok := item.Data.(FileEntry); ok && fe.Path == key {
					continue
				}
			}
		}
		filtered = append(filtered, item)
	}
	return filtered
}

// --- Favorites library ---
//
// The page used to be four flat lists drawn by hand, with its own Back button
// inside the panel. Nothing in the app added a favorite, so the only content it
// could show was whatever an older build had written into favorites.json, and
// the row geometry the pointer tested was not the geometry the rows were drawn
// with. The page is a library now: it opens on what is in progress, keeps saved
// videos, saved streams and the recent timeline in separate tabs, and every tab
// draws the same card — artwork, title, metadata and resume progress — from one
// layout that drawing, the D-pad and the pointer all share.

const (
	favTabContinue = iota
	favTabVideos
	favTabStreams
	favTabRecent
	favTabCount
)

const (
	favRowH         = int32(68)
	favRowGap       = int32(8)
	favGroupHeaderH = int32(30)
	favPanelPad     = int32(18)
	favTabH         = int32(34)
)

// favoritesTabNames is the tab strip's fixed order. The count beside each name
// is measured at draw time, so the strip says how much is behind every tab
// before it is opened.
var favoritesTabNames = [favTabCount]string{"Continue", "Videos", "Streams", "Recent"}

var (
	favTabRects       [favTabCount]sdl.Rect
	favoritesRowHits  []favoritesRowHit
	favoritesListRect sdl.Rect
)

// favoritesRowHit is one drawn row: the item it shows and where it landed.
// Drawing records it, so a tap resolves the row that was actually painted
// instead of a second, hand-maintained guess at the geometry.
type favoritesRowHit struct {
	index int
	rect  sdl.Rect
}

// favoritesLayout is the page's geometry: one source for the panel, the title
// band, the tab strip and the list viewport.
type favoritesLayout struct {
	panel     sdl.Rect
	titleY    int32
	subtitleY int32
	tabsY     int32
	list      sdl.Rect
}

func newFavoritesLayout(element Element) favoritesLayout {
	panelW := getElementWidth(element, 1160)
	panelH := getElementHeight(element, 500)
	if panelW < 320 {
		panelW = 320
	}
	if panelH < 220 {
		panelH = 220
	}
	panel := sdl.Rect{X: element.X, Y: element.Y, W: panelW, H: panelH}
	titleY := panel.Y + favPanelPad
	subtitleY := titleY + 30
	tabsY := subtitleY + 28
	listTop := tabsY + favTabH + 12
	list := sdl.Rect{
		X: panel.X + favPanelPad,
		Y: listTop,
		W: panel.W - 2*favPanelPad,
		H: panel.Y + panel.H - favPanelPad - listTop,
	}
	if list.H < favRowH {
		list.H = favRowH
	}
	return favoritesLayout{panel: panel, titleY: titleY, subtitleY: subtitleY, tabsY: tabsY, list: list}
}

// --- Favorites sources ---

// favoritesSnapshot copies the store so the renderer and the hit tests never
// hold the favorites lock while they measure, draw or load artwork.
func favoritesSnapshot() FavoritesStore {
	favoritesMutex.Lock()
	defer favoritesMutex.Unlock()
	return FavoritesStore{
		Videos: append([]FavoriteItem{}, favoritesStore.Videos...),
		Recent: append([]FavoriteItem{}, favoritesStore.Recent...),
		Files:  append([]FavoriteItem{}, favoritesStore.Files...),
		IPTV:   append([]FavoriteItem{}, favoritesStore.IPTV...),
	}
}

// favoritesInProgress is the Continue tab: videos the user started but did not
// finish. The recent store is where resume offsets are written, so it is the
// source for this shelf rather than a second copy of the videos.
func favoritesInProgress(recent []FavoriteItem) []FavoriteItem {
	out := make([]FavoriteItem, 0, len(recent))
	for _, it := range recent {
		v, ok := it.Data.(VideoInfo)
		if it.Type != "video" || !ok || v.Position <= 0 {
			continue
		}
		if v.Duration > 0 && v.Position >= v.Duration*0.95 {
			continue
		}
		out = append(out, it)
	}
	return out
}

// favoritesWithResume folds the resume offset recorded in Recent into a saved
// video, so a saved video shows (and resumes from) the same place the Continue
// shelf does. Saved copies keep their own data when it already carries a
// position.
func favoritesWithResume(videos, recent []FavoriteItem) []FavoriteItem {
	positions := map[string]float64{}
	for _, it := range recent {
		if v, ok := it.Data.(VideoInfo); ok && v.Position > 0 {
			positions[v.GetURL()] = v.Position
		}
	}
	if len(positions) == 0 {
		return videos
	}
	out := make([]FavoriteItem, len(videos))
	copy(out, videos)
	for i := range out {
		v, ok := out[i].Data.(VideoInfo)
		if !ok || v.Position > 0 {
			continue
		}
		if p, ok := positions[v.GetURL()]; ok {
			v.Position = p
			out[i].Data = v
		}
	}
	return out
}

// favoriteStreamItems merges the IP-stream library with channels an older build
// saved into the favorites file, so the tab lists every stream the user kept
// and never the same URL twice.
func favoriteStreamItems(legacy []FavoriteItem) []FavoriteItem {
	out := make([]FavoriteItem, 0, len(legacy)+8)
	seen := map[string]bool{}
	for _, st := range ipStreamList() {
		url := strings.TrimSpace(st.URL)
		if url == "" || seen[url] {
			continue
		}
		seen[url] = true
		out = append(out, FavoriteItem{Type: "iptv", Data: FileEntry{Name: st.Name, Path: url}})
	}
	for _, it := range legacy {
		fe, ok := it.Data.(FileEntry)
		if !ok || fe.Path == "" || seen[fe.Path] {
			continue
		}
		seen[fe.Path] = true
		out = append(out, it)
	}
	return out
}

// favoritesItemsForTab is the list behind one tab. Continue and Streams are
// views built from other stores, so they cannot drift out of sync with the
// screens that own that data.
func favoritesItemsForTab(tab int) []FavoriteItem {
	snap := favoritesSnapshot()
	switch tab {
	case favTabContinue:
		return favoritesInProgress(snap.Recent)
	case favTabVideos:
		return favoritesWithResume(snap.Videos, snap.Recent)
	case favTabStreams:
		return favoriteStreamItems(snap.IPTV)
	case favTabRecent:
		return snap.Recent
	default:
		return snap.Videos
	}
}

func getCurrentFavorites() []FavoriteItem {
	return favoritesItemsForTab(favoritesCurrentTab)
}

func favoritesTabCounts() [favTabCount]int {
	snap := favoritesSnapshot()
	var counts [favTabCount]int
	counts[favTabContinue] = len(favoritesInProgress(snap.Recent))
	counts[favTabVideos] = len(snap.Videos)
	counts[favTabStreams] = len(favoriteStreamItems(snap.IPTV))
	counts[favTabRecent] = len(snap.Recent)
	return counts
}

func favoritesTabLabel(tab, count int) string {
	if tab < 0 || tab >= favTabCount {
		return ""
	}
	return fmt.Sprintf("%s %d", favoritesTabNames[tab], count)
}

// --- Favorites mutations ---

func favoriteItemKey(it FavoriteItem) string {
	switch d := it.Data.(type) {
	case VideoInfo:
		return d.GetURL()
	case FileEntry:
		return d.Path
	}
	return ""
}

func sameFavorite(a, b FavoriteItem) bool {
	if a.Type != b.Type {
		return false
	}
	key := favoriteItemKey(b)
	return key != "" && favoriteItemKey(a) == key
}

func dropFavorite(list []FavoriteItem, it FavoriteItem) ([]FavoriteItem, bool) {
	out := make([]FavoriteItem, 0, len(list))
	removed := false
	for _, cur := range list {
		if !removed && sameFavorite(cur, it) {
			removed = true
			continue
		}
		out = append(out, cur)
	}
	return out, removed
}

// removeFavoriteItem removes the entry from the list its tab draws and persists
// that list. Continue is a view of Recent, so forgetting an item there forgets
// the resume point it was offering. Streams live in the IP-stream library, so
// removing one removes the saved stream itself.
func removeFavoriteItem(tab int, it FavoriteItem) bool {
	removed := false
	switch tab {
	case favTabVideos:
		favoritesMutex.Lock()
		favoritesStore.Videos, removed = dropFavorite(favoritesStore.Videos, it)
		favoritesMutex.Unlock()
		if removed {
			saveFavorites()
		}
	case favTabContinue, favTabRecent:
		favoritesMutex.Lock()
		favoritesStore.Recent, removed = dropFavorite(favoritesStore.Recent, it)
		favoritesMutex.Unlock()
		if removed {
			saveFavorites()
		}
	case favTabStreams:
		if fe, ok := it.Data.(FileEntry); ok && fe.Path != "" {
			url := strings.TrimSpace(fe.Path)
			for i, st := range ipStreamList() {
				if strings.TrimSpace(st.URL) != url {
					continue
				}
				if removeIPStreamAt(i) {
					removed = true
					if err := saveIPStreams(); err != nil {
						logWarn("favorites", "removeFavoriteItem: saving streams: %v", err)
					}
				}
				break
			}
		}
		var removedLegacy bool
		favoritesMutex.Lock()
		favoritesStore.IPTV, removedLegacy = dropFavorite(favoritesStore.IPTV, it)
		favoritesMutex.Unlock()
		if removedLegacy {
			removed = true
			saveFavorites()
		}
	}
	return removed
}

// favoriteVideoSaved reports whether a URL is already in the Videos tab.
func favoriteVideoSaved(url string) bool {
	if url == "" {
		return false
	}
	favoritesMutex.Lock()
	defer favoritesMutex.Unlock()
	for _, it := range favoritesStore.Videos {
		if v, ok := it.Data.(VideoInfo); ok && v.GetURL() == url {
			return true
		}
	}
	return false
}

// removeFavoriteVideo drops a saved video. The caller persists the store.
func removeFavoriteVideo(url string) bool {
	if url == "" {
		return false
	}
	favoritesMutex.Lock()
	defer favoritesMutex.Unlock()
	var removed bool
	favoritesStore.Videos, removed = dropFavorite(favoritesStore.Videos,
		FavoriteItem{Type: "video", Data: VideoInfo{WebpageURL: url}})
	return removed
}

// toggleFavoriteVideo saves a video, or removes it when it is already saved. It
// reports whether the video is saved afterwards, so the Save action can say
// which way the press went.
func toggleFavoriteVideo(v VideoInfo) bool {
	url := v.GetURL()
	if url == "" {
		return false
	}
	if favoriteVideoSaved(url) {
		removeFavoriteVideo(url)
		return false
	}
	addFavoriteVideo(v)
	return true
}

// favoriteResumePosition is the saved offset for a video, used by Play when the
// item itself does not carry one.
func favoriteResumePosition(url string) float64 {
	if url == "" {
		return 0
	}
	favoritesMutex.Lock()
	defer favoritesMutex.Unlock()
	for _, it := range favoritesStore.Recent {
		if v, ok := it.Data.(VideoInfo); ok && v.GetURL() == url {
			return v.Position
		}
	}
	return 0
}

// --- Favorites grouping ---

type favoritesGroup struct {
	label   string
	indexes []int
}

type favoritesLine struct {
	header string
	item   int
	y      int32
	h      int32
}

// favoriteDateBucket is how the Recent tab's timeline is broken up. Calendar
// days, not hours: a clip watched at 23:50 and one at 00:10 belong to different
// days for the reader even though they are twenty minutes apart.
func favoriteDateBucket(t time.Time) string {
	if t.IsZero() {
		return "Earlier"
	}
	now := time.Now()
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	ty, tm, td := t.In(now.Location()).Date()
	day := time.Date(ty, tm, td, 0, 0, 0, 0, now.Location())
	days := int(today.Sub(day).Hours() / 24)
	switch {
	case days <= 0:
		return "Today"
	case days == 1:
		return "Yesterday"
	case days < 7:
		return "This week"
	default:
		return "Earlier"
	}
}

func favoriteGroupLabel(tab int, it FavoriteItem) string {
	switch tab {
	case favTabVideos:
		if v, ok := it.Data.(VideoInfo); ok {
			if name := strings.TrimSpace(v.Uploader); name != "" {
				return name
			}
			if name := strings.TrimSpace(v.Channel); name != "" {
				return name
			}
		}
		return "Other videos"
	case favTabRecent:
		return favoriteDateBucket(it.Timestamp)
	}
	return ""
}

// favoritesGroups splits a tab into headings. Videos are grouped by channel and
// Recent by day; Continue and Streams are short lists that read better without
// a heading between every row.
func favoritesGroups(tab int, items []FavoriteItem) []favoritesGroup {
	if len(items) == 0 {
		return nil
	}
	if tab != favTabVideos && tab != favTabRecent {
		indexes := make([]int, len(items))
		for i := range indexes {
			indexes[i] = i
		}
		return []favoritesGroup{{indexes: indexes}}
	}
	order := []string{}
	byLabel := map[string][]int{}
	for i, it := range items {
		label := favoriteGroupLabel(tab, it)
		if _, ok := byLabel[label]; !ok {
			order = append(order, label)
		}
		byLabel[label] = append(byLabel[label], i)
	}
	if tab == favTabRecent {
		// The timeline is already newest-first, so the buckets follow it.
		groups := make([]favoritesGroup, 0, len(order))
		for _, label := range []string{"Today", "Yesterday", "This week", "Earlier"} {
			if idx, ok := byLabel[label]; ok && len(idx) > 0 {
				groups = append(groups, favoritesGroup{label: label, indexes: idx})
			}
		}
		return groups
	}
	sort.SliceStable(order, func(i, j int) bool {
		return strings.ToLower(order[i]) < strings.ToLower(order[j])
	})
	groups := make([]favoritesGroup, 0, len(order))
	for _, label := range order {
		groups = append(groups, favoritesGroup{label: label, indexes: byLabel[label]})
	}
	if len(groups) == 1 {
		// One channel needs no heading repeating what every row already says.
		groups[0].label = ""
	}
	return groups
}

func favoritesLines(groups []favoritesGroup, list sdl.Rect) []favoritesLine {
	lines := make([]favoritesLine, 0, 32)
	y := list.Y
	for _, g := range groups {
		if g.label != "" {
			lines = append(lines, favoritesLine{header: g.label, item: -1, y: y, h: favGroupHeaderH})
			y += favGroupHeaderH
		}
		for _, idx := range g.indexes {
			lines = append(lines, favoritesLine{item: idx, y: y, h: favRowH})
			y += favRowH + favRowGap
		}
	}
	return lines
}

// favoritesScrollShift moves the list just far enough to show the focused row.
// It is computed from the same line geometry that is drawn, so a heading can
// never be scrolled over the row the D-pad is holding.
func favoritesScrollShift(lines []favoritesLine, focus int, list sdl.Rect) int32 {
	var top, bottom, contentBottom int32 = 0, 0, list.Y
	found := false
	for _, ln := range lines {
		if ln.y+ln.h > contentBottom {
			contentBottom = ln.y + ln.h
		}
		if ln.item == focus && ln.item >= 0 {
			top, bottom = ln.y, ln.y+ln.h
			found = true
		}
	}
	if !found {
		return 0
	}
	shift := int32(0)
	if top < list.Y {
		shift = top - list.Y
	}
	if bottom-shift > list.Y+list.H {
		shift = bottom - (list.Y + list.H)
	}
	if maxShift := contentBottom - (list.Y + list.H); shift > maxShift {
		shift = maxShift
	}
	if shift < 0 {
		shift = 0
	}
	return shift
}

// --- Favorites rendering ---

func drawFavoritesTab(renderer *sdl.Renderer, config *Config, font *ttf.Font, rect sdl.Rect, label string, active bool) {
	// Home's segmented-pill language (the PS4/PS5 group): the active tab is
	// accent-tinted glass with an accent edge, the others quiet glass.
	if active {
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, rect.H/2, WithAlpha(glassAccent(), 40))
		strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, rect.H/2, 2, glassAccent())
	} else {
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, rect.H/2, glassChipFill())
		strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, rect.H/2, 1, glassEdge())
	}
	color := ColorTextSecondary()
	if active {
		color = ColorTextPrimary()
	}
	tw, th, err := font.SizeUTF8(label)
	if err != nil {
		return
	}
	renderText(renderer, config, font, label, color,
		rect.X+(rect.W-int32(tw))/2, rect.Y+(rect.H-int32(th))/2)
}

func drawFavoritesGroupHeader(renderer *sdl.Renderer, config *Config, font *ttf.Font, rect sdl.Rect, label string) {
	lw, lh, err := font.SizeUTF8(label)
	if err != nil {
		return
	}
	y := rect.Y + (rect.H-int32(lh))/2 + 2
	renderText(renderer, config, font, label, ColorTextTertiary(), rect.X, y)
	lineX := rect.X + int32(lw) + 12
	if lineX < rect.X+rect.W {
		renderer.SetDrawColor(ColorBorderSubtle.R, ColorBorderSubtle.G, ColorBorderSubtle.B, 70)
		renderer.DrawLine(lineX, y+int32(lh)/2, rect.X+rect.W, y+int32(lh)/2)
	}
}

// favoriteStreamHost is the host part of a stream URL: enough to tell two
// channels from two providers apart without printing the whole link.
func favoriteStreamHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if i := strings.Index(raw, "://"); i >= 0 {
		raw = raw[i+3:]
	}
	if i := strings.IndexAny(raw, "/?#"); i >= 0 {
		raw = raw[:i]
	}
	return raw
}

func favoriteMetaLine(it FavoriteItem) string {
	parts := []string{}
	switch d := it.Data.(type) {
	case VideoInfo:
		// The duration is already on the artwork's badge, so this line only
		// carries who made it and where playback would resume.
		if line := strings.TrimSpace(resultMetaLine(d)); line != "" {
			parts = append(parts, line)
		}
		if d.Position > 0 && (d.Duration <= 0 || d.Position < d.Duration*0.95) {
			parts = append(parts, "resume "+formatDuration(d.Position))
		}
	case FileEntry:
		if it.Type == "iptv" {
			// Every row on this tab is a stream, so the provider is the only
			// thing that tells two of them apart.
			if host := favoriteStreamHost(d.Path); host != "" {
				parts = append(parts, host)
			}
		} else if dir := filepath.Dir(d.Path); dir != "" && dir != "." {
			parts = append(parts, filepath.Base(dir))
		}
	}
	if len(parts) == 0 {
		return "Saved"
	}
	return strings.Join(parts, "  ·  ")
}

func favoriteProgress(it FavoriteItem) (position, duration float64) {
	v, ok := it.Data.(VideoInfo)
	if !ok {
		return 0, 0
	}
	return v.Position, v.Duration
}

// drawFavoriteRow paints one library card. The artwork is a 16:9 frame for a
// video and a drawn tile for a stream, so a row is recognisable before its
// title is read. reserve keeps the row's right edge free for the focused row's
// Remove button, so the title never runs under it.
func drawFavoriteRow(renderer *sdl.Renderer, config *Config, rect sdl.Rect, it FavoriteItem, focused bool, titleFont, metaFont *ttf.Font, reserve int32) {
	// The shared list row: Home's glass, and the accent wash and ring when
	// selected.
	UIListRow(renderer, rect, 12, focused, false)

	const thumbW, thumbH = int32(96), int32(54)
	thumb := sdl.Rect{X: rect.X + 9, Y: rect.Y + (rect.H-thumbH)/2, W: thumbW, H: thumbH}
	if video, ok := it.Data.(VideoInfo); ok {
		if !drawThumbnail(renderer, config, video, thumb.X, thumb.Y, thumb.W, thumb.H) {
			// No artwork yet: a bordered frame with a play glyph keeps the slot
			// reading as a video instead of as a hole beside the title. The
			// glyph sits left of centre so the duration badge has its corner.
			strokeRoundedRect(renderer, thumb.X, thumb.Y, thumb.W, thumb.H, 6, 1, WithAlpha(ColorBorder, 200))
			drawTileIcon(renderer, thumb.X+20, thumb.Y+19, 24, "play", ColorTextSecondary(), WithAlpha(ColorIconSurface, 235))
		}
		if video.Duration > 0 {
			drawDurationBadge(renderer, config, metaFont, video.Duration,
				thumb.X+thumb.W-46, thumb.Y+thumb.H-18, 40, 14)
		}
	} else {
		fillRoundedRect(renderer, thumb.X, thumb.Y, thumb.W, thumb.H, 8, ColorIconSurface)
		kind := "files"
		if it.Type == "iptv" {
			kind = "tv"
		}
		drawTileIcon(renderer, thumb.X+thumb.W/2, thumb.Y+thumb.H/2, 26, kind, ColorTextSecondary(), ColorIconSurface)
	}

	textX := thumb.X + thumb.W + 14
	textW := rect.X + rect.W - 14 - textX - reserve
	if textW < 40 {
		textW = 40
	}
	title := ellipsize(titleFont, it.Label(), textW)
	meta := ellipsize(metaFont, favoriteMetaLine(it), textW)

	position, duration := favoriteProgress(it)
	hasProgress := duration > 0 && position > 0 && position < duration*0.95
	titleH := int32(titleFont.Height())
	metaH := int32(metaFont.Height())
	stackH := titleH + 4 + metaH
	textTop := rect.Y + (rect.H-stackH)/2
	renderText(renderer, config, titleFont, title, ColorTextPrimary(), textX, textTop)
	renderText(renderer, config, metaFont, meta, ColorTextMuted(), textX, textTop+titleH+4)
	if hasProgress {
		// A hairline along the card's foot reads as "how far in" without
		// crowding the two text lines.
		drawSimpleProgress(renderer, textX, rect.Y+rect.H-6, textW, 3, position/duration)
	}
}

func favoritesTabSubtitle(tab, count int) string {
	switch tab {
	case favTabContinue:
		return pluralizeCount(count, "video in progress", "videos in progress") + " · resume where you left off"
	case favTabVideos:
		return pluralizeCount(count, "saved video", "saved videos") + " · grouped by channel"
	case favTabStreams:
		return pluralizeCount(count, "saved stream", "saved streams") + " · add more on the IP Stream screen"
	default:
		return pluralizeCount(count, "recent item", "recent items") + " · newest first"
	}
}

func pluralizeCount(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func favoritesEmptyState(tab int) (glyph, title, body string) {
	switch tab {
	case favTabContinue:
		return "@media", "Nothing in progress", "Start a video and your place is kept here."
	case favTabVideos:
		return "@favorites", "No saved videos", "Highlight a result and press Save to keep it here."
	case favTabStreams:
		return "@media", "No saved streams", "Add a channel on the IP Stream screen and it lands here."
	default:
		return "@clock", "Nothing played yet", "Videos and channels you play show up here."
	}
}

func renderFavorites(renderer *sdl.Renderer, config *Config, element Element) {
	favoritesRowHits = favoritesRowHits[:0]
	favTabRects = [favTabCount]sdl.Rect{}
	if favoritesCurrentTab < 0 {
		favoritesCurrentTab = 0
	}
	if favoritesCurrentTab >= favTabCount {
		favoritesCurrentTab = favTabCount - 1
	}

	layout := newFavoritesLayout(element)
	favoritesListRect = layout.list

	drawPanel(renderer, layout.panel.X, layout.panel.Y, layout.panel.W, layout.panel.H,
		WithAlpha(ColorSurfacePanel, 235), WithAlpha(ColorBorder, 130))

	titleFont, _ := getCachedFont(config, "medium")
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	if titleFont == nil {
		titleFont = font
	}

	renderText(renderer, config, titleFont, "Favorites", ColorTextPrimary(),
		layout.panel.X+favPanelPad, layout.titleY)

	items := favoritesItemsForTab(favoritesCurrentTab)
	renderText(renderer, config, font, favoritesTabSubtitle(favoritesCurrentTab, len(items)),
		ColorTextSecondary(), layout.panel.X+favPanelPad, layout.subtitleY)

	// One measured width for every tab keeps the strip even as counts grow.
	counts := favoritesTabCounts()
	labels := [favTabCount]string{}
	tabW := int32(96)
	for i := 0; i < favTabCount; i++ {
		labels[i] = favoritesTabLabel(i, counts[i])
		if w, _, err := font.SizeUTF8(labels[i]); err == nil && int32(w)+28 > tabW {
			tabW = int32(w) + 28
		}
	}
	if limit := (layout.panel.W - 2*favPanelPad - (favTabCount-1)*10) / favTabCount; tabW > limit && limit > 0 {
		tabW = limit
	}
	tabX := layout.panel.X + favPanelPad
	for i := 0; i < favTabCount; i++ {
		rect := sdl.Rect{X: tabX, Y: layout.tabsY, W: tabW, H: favTabH}
		favTabRects[i] = rect
		drawFavoritesTab(renderer, config, font, rect, labels[i], i == favoritesCurrentTab)
		tabX += tabW + 10
	}

	// The focus is an item index, so a shorter list (a delete, a video leaving
	// Continue) must not leave the cursor past the end.
	if favoritesFocusIndex >= len(items) {
		favoritesFocusIndex = len(items) - 1
	}
	if favoritesFocusIndex < 0 {
		favoritesFocusIndex = 0
	}

	if len(items) == 0 {
		glyph, title, body := favoritesEmptyState(favoritesCurrentTab)
		UIEmptyState(renderer, config, layout.list, glyph, title, body)
		// Removing the last item still offers it back.
		favoritesRemoveHit = sdl.Rect{}
		drawFavoritesUndoBar(renderer, config, layout.list)
		drawFavoritesDeletePrompt(renderer, config, layout.panel)
		return
	}

	groups := favoritesGroups(favoritesCurrentTab, items)
	lines := favoritesLines(groups, layout.list)
	shift := favoritesScrollShift(lines, favoritesFocusIndex, layout.list)

	favoritesRemoveHit = sdl.Rect{}
	renderWithClip(renderer, layout.list.X, layout.list.Y, layout.list.W, layout.list.H, func(r *sdl.Renderer) {
		for _, ln := range lines {
			y := ln.y - shift
			if y+ln.h < layout.list.Y || y > layout.list.Y+layout.list.H {
				continue
			}
			rect := sdl.Rect{X: layout.list.X, Y: y, W: layout.list.W, H: ln.h}
			if ln.item < 0 {
				drawFavoritesGroupHeader(r, config, font, rect, ln.header)
				continue
			}
			if ln.item < 0 || ln.item >= len(items) {
				continue
			}
			focused := ln.item == favoritesFocusIndex
			reserve := int32(0)
			if focused {
				reserve = favoritesRemoveButtonW + 12
			}
			drawFavoriteRow(r, config, rect, items[ln.item], focused, titleFont, font, reserve)
			favoritesRowHits = append(favoritesRowHits, favoritesRowHit{index: ln.item, rect: rect})
			if focused {
				// Removing is one obvious button on the row it acts on, not a
				// key the user has to know about.
				favoritesRemoveHit = drawFavoritesRemoveButton(r, config, rect)
			}
		}
	})
	drawFavoritesUndoBar(renderer, config, layout.list)
	drawFavoritesDeletePrompt(renderer, config, layout.panel)

	// Scrollbar: the viewport only moves when the list is taller than it.
	contentBottom := int32(0)
	if len(lines) > 0 {
		contentBottom = lines[len(lines)-1].y + lines[len(lines)-1].h
	}
	if contentH := contentBottom - layout.list.Y; contentH > layout.list.H {
		thumb := float64(layout.list.H) / float64(contentH)
		frac := 0.0
		if maxShift := float64(contentH - layout.list.H); maxShift > 0 {
			frac = float64(shift) / maxShift
		}
		drawScrollbar(renderer, layout.list.X+layout.list.W-3, layout.list.Y+2, 4, layout.list.H-4, thumb, frac)
	}
}

// --- Favorites input ---

func favoritesSelectTab(tab int) {
	if tab < 0 {
		tab = 0
	}
	if tab >= favTabCount {
		tab = favTabCount - 1
	}
	if tab == favoritesCurrentTab {
		return
	}
	favoritesCurrentTab = tab
	favoritesFocusIndex = 0
	PlayNavSound()
}

func favoritesMoveFocus(delta int, config *Config) {
	items := getCurrentFavorites()
	if len(items) == 0 || favoritesFocusIndex+delta < 0 {
		// Leaving the list upward (or entering from an empty one) is the
		// scene's own navigation, so the D-pad can still reach the Play and
		// Remove buttons and the header.
		if delta < 0 {
			moveSelection(config, ActionNavigateUp)
		} else {
			moveSelection(config, ActionNavigateDown)
		}
		return
	}
	if favoritesFocusIndex+delta >= len(items) {
		// Past the last row is the action row below the panel.
		moveSelection(config, ActionNavigateDown)
		return
	}
	favoritesFocusIndex += delta
	PlayNavSound()
}

func favoritesPlayFocused(config *Config) {
	items := getCurrentFavorites()
	if favoritesFocusIndex < 0 || favoritesFocusIndex >= len(items) {
		return
	}
	items[favoritesFocusIndex].Play(config)
}

// --- Removing, with Undo ---
//
// Removing a favourite used to be a key nobody saw (X) and a toast; a slip
// lost the entry for good. The focused row now carries a Remove button, the
// removal is offered back for a few seconds in an Undo bar (Y, U, Ctrl+Z or a
// click), and a video that is also downloaded asks whether the file should go
// too — the favourite and the download are separate things the user may want
// to keep apart.

const (
	// favoritesUndoWindow is how long a removal can be taken back.
	favoritesUndoWindow = 6 * time.Second
	// favoritesRemoveButtonW is the Remove button's width on the focused row.
	favoritesRemoveButtonW = int32(116)
)

// favoritesUndoState is the last removal. index is the item's place in its
// store list, so Undo puts it back exactly where it was; label is the bar's
// text, built once.
type favoritesUndoState struct {
	item  FavoriteItem
	tab   int
	index int
	title string
	label string
	at    time.Time
}

var favoritesUndo favoritesUndoState

// favoritesDeletePrompt asks whether a removed favourite's download goes too.
var favoritesDeletePrompt struct {
	open  bool
	libID string
	body  string
	size  int64
	focus int // 0 = keep the file, 1 = delete it
	keep  sdl.Rect
	del   sdl.Rect
}

// The click targets drawn this frame.
var (
	favoritesRemoveHit sdl.Rect
	favoritesUndoHit   sdl.Rect
)

// favoritesUndoActive reports whether the last removal can still be undone.
func favoritesUndoActive() bool {
	return !favoritesUndo.at.IsZero() && time.Since(favoritesUndo.at) <= favoritesUndoWindow
}

// favoritesStoreIndex is an item's position in the store list its tab draws
// from, or -1.
func favoritesStoreIndex(tab int, it FavoriteItem) int {
	favoritesMutex.Lock()
	defer favoritesMutex.Unlock()
	list := favoritesStore.Recent
	if tab == favTabVideos {
		list = favoritesStore.Videos
	}
	for i, cur := range list {
		if sameFavorite(cur, it) {
			return i
		}
	}
	return -1
}

// favoritesInsertAt puts it back at index (the end when the list got shorter),
// unless an equal entry came back in the meantime.
func favoritesInsertAt(list []FavoriteItem, index int, it FavoriteItem) []FavoriteItem {
	for _, cur := range list {
		if sameFavorite(cur, it) {
			return list
		}
	}
	if index < 0 || index > len(list) {
		index = len(list)
	}
	out := make([]FavoriteItem, 0, len(list)+1)
	out = append(out, list[:index]...)
	out = append(out, it)
	out = append(out, list[index:]...)
	return out
}

func favoritesRemoveFocused(config *Config) {
	items := getCurrentFavorites()
	if favoritesFocusIndex < 0 || favoritesFocusIndex >= len(items) {
		return
	}
	it := items[favoritesFocusIndex]
	tab := favoritesCurrentTab
	storeIndex := favoritesStoreIndex(tab, it)
	if !removeFavoriteItem(tab, it) {
		return
	}
	PlayBackSound()
	if remaining := len(getCurrentFavorites()); favoritesFocusIndex >= remaining && favoritesFocusIndex > 0 {
		favoritesFocusIndex = remaining - 1
	}
	if tab == favTabStreams {
		// A stream lives in the IP-stream library, which keeps its own list;
		// it is not offered back here.
		favoritesUndo = favoritesUndoState{}
		ShowToast("Removed from saved streams", ToastKindWarning)
		return
	}
	title := it.Label()
	favoritesUndo = favoritesUndoState{item: it, tab: tab, index: storeIndex, title: title,
		label: "Removed “" + title + "”", at: time.Now()}
	if v, ok := it.Data.(VideoInfo); ok && tab == favTabVideos {
		if e, found := dlLibraryFindSource(v.GetURL(), true); found {
			favoritesDeletePrompt.open = true
			favoritesDeletePrompt.libID = e.ID
			favoritesDeletePrompt.size = e.Size
			favoritesDeletePrompt.focus = 0
			favoritesDeletePrompt.body = "“" + dlFirstNonEmpty(e.Title, title) + "” is downloaded on this device (" +
				humanBytes(e.Size) + "). Delete the file to free the space, or keep it in Downloads."
		}
	}
}

// favoritesUndoRemove puts the last removed favourite back where it was.
func favoritesUndoRemove() bool {
	if !favoritesUndoActive() {
		return false
	}
	u := favoritesUndo
	favoritesUndo = favoritesUndoState{}
	favoritesMutex.Lock()
	if u.tab == favTabVideos {
		favoritesStore.Videos = favoritesInsertAt(favoritesStore.Videos, u.index, u.item)
	} else {
		favoritesStore.Recent = favoritesInsertAt(favoritesStore.Recent, u.index, u.item)
	}
	favoritesMutex.Unlock()
	saveFavorites()
	PlayActivateSound()
	ShowToast("Restored "+dlShortTitle(u.title, 48), ToastKindSuccess)
	return true
}

// favoritesDeletePromptAnswer closes the prompt: deleting frees the file, keeping
// leaves it in Downloads.
func favoritesDeletePromptAnswer(deleteFile bool) {
	p := favoritesDeletePrompt
	favoritesDeletePrompt.open = false
	if !deleteFile {
		PlayBackSound()
		return
	}
	mediaHubStopIfPlaying(p.libID)
	if e, ok := dlLibraryRemove(p.libID); ok {
		PlayBackSound()
		ShowToast("Deleted the download · freed "+humanBytes(e.Size), ToastKindSuccess)
	}
}

// drawFavoritesRemoveButton paints the focused row's Remove button and returns
// its rect.
func drawFavoritesRemoveButton(renderer *sdl.Renderer, config *Config, row sdl.Rect) sdl.Rect {
	h := int32(34)
	if h > row.H-12 {
		h = row.H - 12
	}
	btn := sdl.Rect{X: row.X + row.W - favoritesRemoveButtonW - 12, Y: row.Y + (row.H-h)/2, W: favoritesRemoveButtonW, H: h}
	ink := DangerInk()
	fillRoundedRect(renderer, btn.X, btn.Y, btn.W, btn.H, h/2, WithAlpha(ink, 36))
	strokeRoundedRect(renderer, btn.X, btn.Y, btn.W, btn.H, h/2, 1, WithAlpha(ink, 170))
	glyph := h - 16
	DrawHomeIcon(renderer, "trash", sdl.Rect{X: btn.X + 12, Y: btn.Y + 8, W: glyph, H: glyph}, false)
	if font := getFontSized(config, "small", 15); font != nil {
		label := "Remove"
		_, th := measureText(font, label)
		drawText(renderer, font, label, btn.X+12+glyph+8, btn.Y+(btn.H-th)/2, ink, textAlignLeft)
	}
	return btn
}

// drawFavoritesUndoBar paints the "Removed … · Undo" bar along the bottom of the
// list while the removal can still be taken back.
func drawFavoritesUndoBar(renderer *sdl.Renderer, config *Config, list sdl.Rect) {
	favoritesUndoHit = sdl.Rect{}
	if !favoritesUndoActive() || favoritesDeletePrompt.open {
		return
	}
	font := getFontSized(config, "small", 16)
	if font == nil {
		return
	}
	w := list.W - 40
	if w > 640 {
		w = 640
	}
	h := int32(46)
	bar := sdl.Rect{X: list.X + (list.W-w)/2, Y: list.Y + list.H - h - 6, W: w, H: h}
	drawGlassSheet(renderer, bar, glassRaisedFill())
	btnLabel := "Undo"
	bw, bh := measureText(font, btnLabel)
	btn := sdl.Rect{X: bar.X + bar.W - bw - 70, Y: bar.Y + 7, W: bw + 56, H: bar.H - 14}
	fillRoundedRect(renderer, btn.X, btn.Y, btn.W, btn.H, btn.H/2, WithAlpha(glassAccent(), 60))
	strokeRoundedRect(renderer, btn.X, btn.Y, btn.W, btn.H, btn.H/2, 1, glassAccent())
	drawText(renderer, font, btnLabel, btn.X+16, btn.Y+(btn.H-bh)/2, ColorTextPrimary(), textAlignLeft)
	if small := getFontSized(config, "small", 13); small != nil {
		yw, yh := measureText(small, "Y")
		drawText(renderer, small, "Y", btn.X+btn.W-16-yw, btn.Y+(btn.H-yh)/2, ColorTextTertiary(), textAlignLeft)
	}
	text := ellipsize(font, favoritesUndo.label, btn.X-bar.X-36)
	_, th := measureText(font, text)
	drawText(renderer, font, text, bar.X+18, bar.Y+(bar.H-th)/2, ColorTextSecondary(), textAlignLeft)
	favoritesUndoHit = btn
}

// drawFavoritesDeletePrompt paints the "also delete the download?" card.
func drawFavoritesDeletePrompt(renderer *sdl.Renderer, config *Config, panel sdl.Rect) {
	p := &favoritesDeletePrompt
	if !p.open {
		return
	}
	titleFont := getFontSized(config, "small", 19)
	bodyFont := getFontSized(config, "small", 15)
	if titleFont == nil || bodyFont == nil {
		return
	}
	drawOverlayScrim(renderer)
	w := panel.W - 80
	if w > 560 {
		w = 560
	}
	pad := int32(22)
	body := mediaConfirmLines(bodyFont, p.body, w-2*pad)
	h := pad + int32(titleFont.Height()) + 10 + int32(len(body))*int32(bodyFont.Height()) + 22 + 44 + pad
	card := sdl.Rect{X: panel.X + (panel.W-w)/2, Y: panel.Y + (panel.H-h)/2, W: w, H: h}
	drawGlassSheet(renderer, card, glassSheetFill())
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	y := card.Y + pad
	drawText(renderer, titleFont, "Also delete the download?", card.X+pad, y, ColorTextPrimary(), textAlignLeft)
	y += int32(titleFont.Height()) + 10
	for _, line := range body {
		drawText(renderer, bodyFont, line, card.X+pad, y, ColorTextSecondary(), textAlignLeft)
		y += int32(bodyFont.Height())
	}
	bw := (w - 2*pad - 14) / 2
	by := card.Y + h - pad - 44
	p.keep = sdl.Rect{X: card.X + pad, Y: by, W: bw, H: 44}
	p.del = sdl.Rect{X: p.keep.X + bw + 14, Y: by, W: bw, H: 44}
	UIButton(renderer, nil, p.keep, "Keep file", UIButtonStyle{Variant: UIButtonSecondary, Font: bodyFont},
		UIButtonState{Focused: p.focus == 0})
	UIButton(renderer, nil, p.del, "Delete file", UIButtonStyle{Variant: UIButtonDanger, Font: bodyFont},
		UIButtonState{Focused: p.focus == 1})
}

// favoritesFooterHints is the Favorites footer: what X and Y do here, and the
// prompt's own keys while it is up.
func favoritesFooterHints(hints []FooterHint) []FooterHint {
	if favoritesDeletePrompt.open {
		return []FooterHint{{Button: "Left/Right", Label: "Choose"}, {Button: "A", Label: "Confirm"}, {Button: "B", Label: "Keep file"}}
	}
	if len(hints) > 1 {
		hints[1] = FooterHint{Button: "A", Label: "Play"}
	}
	hints = append(hints, FooterHint{Button: "X", Label: "Remove"})
	if favoritesUndoActive() {
		hints = append(hints, FooterHint{Button: "Y", Label: "Undo"})
	}
	return append(hints, FooterHint{Button: "Left/Right", Label: "Tabs"})
}

// favoritesPromptInput drives the delete prompt; it owns every press while up.
func favoritesPromptInput(left, right, confirm, back bool) {
	switch {
	case left:
		if favoritesDeletePrompt.focus != 0 {
			favoritesDeletePrompt.focus = 0
			PlayNavSound()
		}
	case right:
		if favoritesDeletePrompt.focus != 1 {
			favoritesDeletePrompt.focus = 1
			PlayNavSound()
		}
	case confirm:
		favoritesDeletePromptAnswer(favoritesDeletePrompt.focus == 1)
	case back:
		favoritesDeletePromptAnswer(false)
	}
}

// handleFavoritesController gives the pad the same model as the keyboard: the
// tab strip on Left/Right, rows on Up/Down, A plays what is highlighted, X
// removes it and Y takes the removal back.
func handleFavoritesController(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil {
		return false
	}
	if favoritesDeletePrompt.open {
		favoritesPromptInput(e.Button == sdl.CONTROLLER_BUTTON_DPAD_LEFT || e.Button == sdl.CONTROLLER_BUTTON_DPAD_UP,
			e.Button == sdl.CONTROLLER_BUTTON_DPAD_RIGHT || e.Button == sdl.CONTROLLER_BUTTON_DPAD_DOWN,
			e.Button == sdl.CONTROLLER_BUTTON_A,
			e.Button == sdl.CONTROLLER_BUTTON_B || e.Button == sdl.CONTROLLER_BUTTON_BACK)
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		favoritesMoveFocus(-1, config)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		favoritesMoveFocus(1, config)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		favoritesSelectTab(favoritesCurrentTab - 1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		favoritesSelectTab(favoritesCurrentTab + 1)
	case sdl.CONTROLLER_BUTTON_A:
		favoritesPlayFocused(config)
	case sdl.CONTROLLER_BUTTON_X:
		favoritesRemoveFocused(config)
	case sdl.CONTROLLER_BUTTON_Y:
		favoritesUndoRemove()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		goBackScene(config)
	default:
		return false
	}
	return true
}

func handleFavoritesInput(e *sdl.KeyboardEvent, config *Config) {
	if favoritesDeletePrompt.open {
		sym := e.Keysym.Sym
		favoritesPromptInput(sym == sdl.K_LEFT || sym == sdl.K_UP,
			sym == sdl.K_RIGHT || sym == sdl.K_DOWN,
			sym == sdl.K_RETURN || sym == sdl.K_SPACE || sym == sdl.K_KP_ENTER,
			sym == sdl.K_ESCAPE || sym == sdl.K_BACKSPACE || sym == sdl.K_b)
		return
	}
	if e.Keysym.Sym == sdl.K_z && e.Keysym.Mod&sdl.KMOD_CTRL != 0 {
		favoritesUndoRemove()
		return
	}
	switch e.Keysym.Sym {
	case sdl.K_LEFT:
		favoritesSelectTab(favoritesCurrentTab - 1)
	case sdl.K_RIGHT:
		favoritesSelectTab(favoritesCurrentTab + 1)
	case sdl.K_UP:
		favoritesMoveFocus(-1, config)
	case sdl.K_DOWN:
		favoritesMoveFocus(1, config)
	case sdl.K_RETURN, sdl.K_SPACE:
		favoritesPlayFocused(config)
	case sdl.K_x, sdl.K_d, sdl.K_DELETE:
		favoritesRemoveFocused(config)
	case sdl.K_y, sdl.K_u:
		favoritesUndoRemove()
	case sdl.K_BACKSPACE, sdl.K_ESCAPE, sdl.K_b:
		// The list has no text field, so B is the back key here too. On a
		// desktop keyboard Escape was the only way out, which the pad maps to
		// nowhere.
		goBackScene(config)
	}
}

func handleFavoritesMouseClick(mx, my int32, config *Config) {
	if favoritesDeletePrompt.open {
		switch {
		case pointInRect(mx, my, favoritesDeletePrompt.del):
			favoritesDeletePromptAnswer(true)
		case pointInRect(mx, my, favoritesDeletePrompt.keep):
			favoritesDeletePromptAnswer(false)
		}
		return
	}
	if favoritesUndoHit.W > 0 && pointInRect(mx, my, favoritesUndoHit) {
		favoritesUndoRemove()
		return
	}
	if favoritesRemoveHit.W > 0 && pointInRect(mx, my, favoritesRemoveHit) {
		favoritesRemoveFocused(config)
		return
	}
	for i, rect := range favTabRects {
		if rect.W > 0 && pointInRect(mx, my, rect) {
			favoritesSelectTab(i)
			return
		}
	}
	for _, hit := range favoritesRowHits {
		if pointInRect(mx, my, hit.rect) {
			favoritesFocusIndex = hit.index
			favoritesPlayFocused(config)
			return
		}
	}
}

// findSceneIndex is the index of a scene by name, or 0 when it is absent.
func findSceneIndex(config *Config, name string) int {
	for i, scene := range config.Scenes {
		if scene.Name == name {
			return i
		}
	}
	return 0
}

func openInExplorer(path string) {
	var cmd *exec.Cmd
	if IsWindows() {
		dir := filepath.Dir(path)
		if _, err := os.Stat(dir); err != nil {
			dir = "."
		}
		cmd = exec.Command("explorer.exe", dir)
	} else {
		cmd = exec.Command("xdg-open", filepath.Dir(path))
	}
	if err := cmd.Start(); err != nil {
		logWarn("favorites", "openInExplorer: %v", err)
		return
	}
	// Reap the shell process; without Wait it stays a zombie on the handheld's
	// Linux until JukaHub exits.
	go func() { _ = cmd.Wait() }()
}
