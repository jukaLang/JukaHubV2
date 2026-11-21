package main

// devsync_data.go — Device Sync's merges: collections, recents and play time,
// and "Send my settings" (devsync.go has the categories and the file copies).
//
// Collections and recents travel in one POST each way: the device that syncs
// sends its own, the other device answers with its own (as it was before the
// merge) and then merges what it got; the first device merges the answer.
// Both merges are the same rule, so both devices end up alike:
//
//   - collections by name: every item of both, in the order of the one that
//     changed last (collections_nextui.go stamps changes), the other's extra
//     items after them. Games travel as card paths ("card:/Roms/GB/x.gb"), so
//     the same game on two cards is one item. The automatic collections stay
//     where they are, never sent.
//   - recents: the games of both that are on this card, by last played (the
//     other clock corrected like Save sync does), cut to the usual length.
//   - play time: per game, the higher of the two figures (never added up, so
//     syncing twice changes nothing).
//
// Settings go one way, on purpose: the sending user confirms, and the
// receiving device holds them until its user chooses "Use them" on the Save
// sync page. Only the personal look-and-feel set travels (profilesPrefKeys:
// theme, colours, wallpaper, Home layout, language, text size, sounds,
// accessibility) minus the name Home greets and the pad remap; never Wi-Fi,
// accounts, passwords, power or hardware settings. A wallpaper or theme file
// that is not on the receiving card is left as it was there.

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	devsyncDocLimit = 8 << 20
	// devsyncCardRef marks a game path from the card root.
	devsyncCardRef = "card:"
)

// devsyncSkew is how far another device's clock is ahead (0 when within a
// minute, the way ssyncCompare treats it).
func devsyncSkew(remoteNow int64) int64 {
	skew := remoteNow - time.Now().Unix()
	if skew > -60 && skew < 60 {
		return 0
	}
	return skew
}

// devsyncPortableRef is a collection ref as it travels: a game on the card
// as its path from the card root, anything else as it is.
func devsyncPortableRef(card, ref string) string {
	if strings.HasPrefix(ref, homeTabRefGame) {
		if rel, ok := colNextuiCardRel(card, strings.TrimPrefix(ref, homeTabRefGame)); ok {
			return devsyncCardRef + rel
		}
	}
	return ref
}

// devsyncLocalRef is a ref from another device as this one stores it; "" for
// one that would leave the card.
func devsyncLocalRef(card, ref string) string {
	if strings.HasPrefix(ref, devsyncCardRef) {
		if abs, ok := colNextuiLineFile(card, strings.TrimPrefix(ref, devsyncCardRef)); ok {
			return homeTabRefGame + abs
		}
		return ""
	}
	return ref
}

// devsyncCleanRef is a ref from another device, bounded and one line.
func devsyncCleanRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if len(ref) > 2048 || strings.ContainsAny(ref, "\r\n\x00") {
		return ""
	}
	return ref
}

// ── Collections ──────────────────────────────────────────────────────────

// devsyncCol is one collection on the wire. Changed is when it last changed
// on the sending device (its clock).
type devsyncCol struct {
	Name    string            `json:"name"`
	Items   []homeTabsColItem `json:"items"`
	Changed int64             `json:"changed"`
}

// devsyncColDoc is a device's collections.
type devsyncColDoc struct {
	V    int          `json:"v"`
	Now  int64        `json:"now"`
	Cols []devsyncCol `json:"cols"`
}

// devsyncLocalCollections is this device's collections, ready to send.
func devsyncLocalCollections() devsyncColDoc {
	changed := colNextuiChangedAt()
	card := saveManagerSDRoot()
	doc := devsyncColDoc{V: 1, Now: time.Now().Unix()}
	for _, col := range homeTabsColList() {
		if homeTabsIsAuto(col.ID) {
			continue
		}
		c := devsyncCol{Name: col.Name, Changed: changed[col.ID], Items: []homeTabsColItem{}}
		if c.Changed == 0 {
			c.Changed = col.Created.Unix()
		}
		for _, it := range col.Items {
			c.Items = append(c.Items, homeTabsColItem{Ref: devsyncPortableRef(card, it.Ref), Title: it.Title})
		}
		doc.Cols = append(doc.Cols, c)
	}
	return doc
}

// devsyncUnionItems is first's items, then second's that first lacks.
func devsyncUnionItems(first, second []homeTabsColItem) []homeTabsColItem {
	out := make([]homeTabsColItem, 0, len(first)+len(second))
	seen := map[string]bool{}
	for _, list := range [][]homeTabsColItem{first, second} {
		for _, it := range list {
			if it.Ref == "" || seen[it.Ref] {
				continue
			}
			seen[it.Ref] = true
			out = append(out, it)
		}
	}
	return out
}

// devsyncSameRefs reports whether two lists hold the same refs in order.
func devsyncSameRefs(a, b []homeTabsColItem) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Ref != b[i].Ref {
			return false
		}
	}
	return true
}

// devsyncMergeCollections merges another device's collections into this
// one's and returns how many were made or changed here.
func devsyncMergeCollections(remote devsyncColDoc) int {
	if remote.V != 1 {
		return 0
	}
	skew := devsyncSkew(remote.Now)
	card := saveManagerSDRoot()
	changed := colNextuiChangedAt()
	byName := map[string]homeTabsCollection{}
	for _, col := range homeTabsColList() {
		byName[strings.ToLower(col.Name)] = col
	}
	n := 0
	for i, rc := range remote.Cols {
		if i >= homeTabsColMax {
			break
		}
		name := homeTabsColCleanName(partyCleanLabel(rc.Name, 200))
		if name == "" {
			continue
		}
		var items []homeTabsColItem
		for j, it := range rc.Items {
			if j >= homeTabsColItemsMax {
				break
			}
			ref := devsyncLocalRef(card, devsyncCleanRef(it.Ref))
			if ref == "" || strings.HasPrefix(ref, homeTabRefCollection) {
				continue
			}
			items = append(items, homeTabsColItem{Ref: ref, Title: partyCleanLabel(it.Title, 160)})
		}
		lc, ok := byName[strings.ToLower(name)]
		if !ok {
			if id, col := colNextuiPut("", name, items); id != "" {
				byName[strings.ToLower(name)] = col
				n++
			}
			continue
		}
		localAt := changed[lc.ID]
		if localAt == 0 {
			localAt = lc.Created.Unix()
		}
		first, second := lc.Items, items
		if rc.Changed-skew > localAt {
			first, second = items, lc.Items
		}
		merged := devsyncUnionItems(first, second)
		if !devsyncSameRefs(merged, lc.Items) {
			colNextuiPut(lc.ID, lc.Name, merged)
			n++
		}
	}
	return n
}

// devsyncHandleCollections answers with this device's collections and merges
// the caller's.
func devsyncHandleCollections(w http.ResponseWriter, r *http.Request, call peerCall) {
	var in devsyncColDoc
	if !peerDecodeBody(w, r, devsyncDocLimit, &in) {
		return
	}
	if gameScreenSuspended() {
		http.Error(w, "a game is running there — close it first", http.StatusConflict)
		return
	}
	out := devsyncLocalCollections()
	if n := devsyncMergeCollections(in); n > 0 {
		name := call.rec.Name
		tbxPost(func() {
			ShowToast("Collections merged with "+name+" · "+pluralizeCount(n, "collection", "collections")+" changed here", ToastKindSuccess)
		})
	}
	peerWriteJSONResponse(w, http.StatusOK, out)
}

// devsyncExchangeCollections merges collections with a paired device, both
// ways; n is how many changed here.
func devsyncExchangeCollections(ctx context.Context, peerID string) (int, error) {
	var theirs devsyncColDoc
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := peerDoJSON(cctx, peerID, http.MethodPost, devsyncCollectionsPath, devsyncLocalCollections(), &theirs, devsyncDocLimit); err != nil {
		return 0, err
	}
	return devsyncMergeCollections(theirs), nil
}

// ── Recents and play time ────────────────────────────────────────────────

// devsyncRecent is one Continue entry on the wire (path from the card root).
type devsyncRecent struct {
	Path     string `json:"path"`
	System   string `json:"system"`
	Title    string `json:"title"`
	PlayedAt int64  `json:"played_at"`
}

// devsyncPlayDoc is a device's recents and per-game play time.
type devsyncPlayDoc struct {
	V       int              `json:"v"`
	Now     int64            `json:"now"`
	Recents []devsyncRecent  `json:"recents"`
	Games   map[string]int64 `json:"games,omitempty"`
}

// devsyncPlaytimeGames is a copy of the per-game play time, read on the main
// loop (nil when it does not answer within a few seconds).
func devsyncPlaytimeGames() map[string]int64 {
	ch := make(chan map[string]int64, 1)
	tbxPost(func() {
		var out map[string]int64
		if playtime.loaded {
			out = make(map[string]int64, len(playtime.data.Games))
			for k, v := range playtime.data.Games {
				out[k] = v
			}
		}
		ch <- out
	})
	select {
	case m := <-ch:
		return m
	case <-time.After(5 * time.Second):
		return nil
	}
}

// devsyncLocalPlay is this device's recents (games on the card only) and play
// time, ready to send.
func devsyncLocalPlay() devsyncPlayDoc {
	card := saveManagerSDRoot()
	doc := devsyncPlayDoc{V: 1, Now: time.Now().Unix(), Recents: []devsyncRecent{}}
	for _, r := range recentGames() {
		rel, ok := colNextuiCardRel(card, r.Path)
		if !ok {
			continue
		}
		doc.Recents = append(doc.Recents, devsyncRecent{Path: devsyncCardRef + rel, System: r.System, Title: r.Title, PlayedAt: r.PlayedAt.Unix()})
	}
	doc.Games = devsyncPlaytimeGames()
	return doc
}

// devsyncMergeRecents merges another device's recents into Continue: games on
// this card only, newest first, cut to the usual length. It returns how many
// entries were added or moved up.
func devsyncMergeRecents(remote devsyncPlayDoc) int {
	if remote.V != 1 {
		return 0
	}
	skew := devsyncSkew(remote.Now)
	card := saveManagerSDRoot()
	// The games are looked up on the card first, outside the recents' lock
	// (Home reads the recents while it draws).
	var found []recentGame
	for i, r := range remote.Recents {
		if i >= 100 {
			break
		}
		// A clock that reset cannot say when the game was played.
		if r.PlayedAt < ssyncMinTime || !strings.HasPrefix(r.Path, devsyncCardRef) {
			continue
		}
		abs, ok := colNextuiLineFile(card, devsyncCleanRef(strings.TrimPrefix(r.Path, devsyncCardRef)))
		if !ok || !regularFile(abs) {
			continue
		}
		title := partyCleanLabel(r.Title, 160)
		if title == "" {
			title = gameDisplayTitle(abs)
		}
		found = append(found, recentGame{Path: abs, System: partyCleanLabel(r.System, 80), Title: title, PlayedAt: time.Unix(r.PlayedAt-skew, 0)})
	}
	gameRecentsMu.Lock()
	if !gameRecentsLoaded {
		gameRecents = loadGameRecentsFile(gameRecentsFile())
		gameRecentsLoaded = true
	}
	next := append([]recentGame(nil), gameRecents...)
	idx := map[string]int{}
	for i, it := range next {
		idx[strings.ToLower(it.Path)] = i
	}
	n := 0
	for _, g := range found {
		if j, ok := idx[strings.ToLower(g.Path)]; ok {
			if g.PlayedAt.After(next[j].PlayedAt) {
				next[j].PlayedAt = g.PlayedAt
				n++
			}
			continue
		}
		next = append(next, g)
		idx[strings.ToLower(g.Path)] = len(next) - 1
		n++
	}
	if n > 0 {
		sort.SliceStable(next, func(a, b int) bool { return next[a].PlayedAt.After(next[b].PlayedAt) })
		if len(next) > maxRecentGames {
			next = next[:maxRecentGames]
		}
		if err := saveGameRecentsFile(gameRecentsFile(), next); err != nil {
			logWarn("devsync", "could not save the merged recents: %v", err)
			n = 0
		} else {
			gameRecents = next
		}
	}
	gameRecentsMu.Unlock()
	if n > 0 {
		homeTabsTouch()
	}
	return n
}

// devsyncMergePlaytime keeps, per game, the higher of the two play times. It
// runs on the main loop and saves once.
func devsyncMergePlaytime(games map[string]int64) {
	clean := map[string]int64{}
	for k, v := range games {
		if len(clean) >= 5000 {
			break
		}
		if v <= 0 || v > 100*365*24*3600 || k == "" || len(k) > 300 || strings.ContainsAny(k, "\r\n\x00") {
			continue
		}
		clean[k] = v
	}
	if len(clean) == 0 {
		return
	}
	tbxPost(func() {
		if !playtime.loaded || appConfig == nil {
			return
		}
		if playtime.data.Games == nil {
			playtime.data.Games = map[string]int64{}
		}
		changed := false
		for k, v := range clean {
			if v > playtime.data.Games[k] {
				playtime.data.Games[k] = v
				changed = true
			}
		}
		if changed {
			playtime.rev++
			playtimePersist(appConfig)
		}
	})
}

// devsyncHandleRecents answers with this device's recents and play time and
// merges the caller's.
func devsyncHandleRecents(w http.ResponseWriter, r *http.Request, call peerCall) {
	var in devsyncPlayDoc
	if !peerDecodeBody(w, r, devsyncDocLimit, &in) {
		return
	}
	if gameScreenSuspended() {
		http.Error(w, "a game is running there — close it first", http.StatusConflict)
		return
	}
	out := devsyncLocalPlay()
	devsyncMergeRecents(in)
	devsyncMergePlaytime(in.Games)
	peerWriteJSONResponse(w, http.StatusOK, out)
}

// devsyncExchangePlay merges recents and play time with a paired device, both
// ways; n is how many recents changed here.
func devsyncExchangePlay(ctx context.Context, peerID string) (int, error) {
	var theirs devsyncPlayDoc
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := peerDoJSON(cctx, peerID, http.MethodPost, devsyncRecentsPath, devsyncLocalPlay(), &theirs, devsyncDocLimit); err != nil {
		return 0, err
	}
	n := devsyncMergeRecents(theirs)
	devsyncMergePlaytime(theirs.Games)
	return n, nil
}

// ── Settings ─────────────────────────────────────────────────────────────

// devsyncSettingsDoc is "Send my settings": the personal keys that are set,
// the ones that are not (so they go back to their defaults there), and the
// typed look fields. Typed.Name is never filled.
type devsyncSettingsDoc struct {
	V     int                    `json:"v"`
	Keys  map[string]interface{} `json:"keys"`
	Unset []string               `json:"unset,omitempty"`
	Typed profileTyped           `json:"typed"`
}

// devsyncSettingsKeys are the keys that travel: the personal set of profiles
// without the name Home greets, the pad remap (each pad is its own) and the
// undo record of a look pack.
func devsyncSettingsKeys() map[string]bool {
	deny := map[string]bool{"tspUsername": true, xpackUndoKey: true, a11yKeyKeymap: true}
	out := map[string]bool{}
	for _, k := range profilesPrefKeys() {
		if !deny[k] {
			out[k] = true
		}
	}
	return out
}

// devsyncCaptureSettings reads this device's settings to send (main thread).
func devsyncCaptureSettings(c *Config) devsyncSettingsDoc {
	doc := devsyncSettingsDoc{V: 1, Keys: map[string]interface{}{}}
	if c == nil {
		return doc
	}
	keys := make([]string, 0, 128)
	for k := range devsyncSettingsKeys() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if volatileCustomVars[k] {
			continue
		}
		v, ok := c.Variables.Custom[k]
		if !ok || v == nil {
			doc.Unset = append(doc.Unset, k)
			continue
		}
		if p := xpackPlain(v); p != nil {
			doc.Keys[k] = p
		}
	}
	v := c.Variables
	doc.Typed = profileTyped{Button: v.ButtonColor, Label: v.LabelColor, Input: v.InputColor,
		ReducedMotion: v.ReducedMotion, Clock12: v.Clock12, DynamicBg: v.DynamicBg, Wallpaper: v.BackgroundImage}
	return doc
}

// devsyncAbsPath reports whether a value is an absolute file path.
func devsyncAbsPath(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "/") || (len(s) > 2 && s[1] == ':' && (s[2] == '\\' || s[2] == '/'))
}

// devsyncApplySettings puts received settings in place and makes everything
// that keeps a copy read them again (profilesApplyLive's steps). A file path
// that is not on this card is skipped. Main thread; it returns how many
// settings changed.
func devsyncApplySettings(c *Config, doc devsyncSettingsDoc) int {
	if c == nil || doc.V != 1 {
		return 0
	}
	if c.Variables.Custom == nil {
		c.Variables.Custom = map[string]interface{}{}
	}
	allowed := devsyncSettingsKeys()
	for _, name := range []string{"reducedMotion", "clock12", "dynamicBg"} {
		dropCustomMirror(c, name)
	}
	custom := c.Variables.Custom
	n := 0
	for k, v := range doc.Keys {
		if !allowed[k] || v == nil || volatileCustomVars[k] {
			continue
		}
		if s, ok := v.(string); ok && devsyncAbsPath(s) && !wallpaperFileExists(s) {
			continue
		}
		custom[k] = xpackPlain(v)
		n++
	}
	for _, k := range doc.Unset {
		if !allowed[k] || volatileCustomVars[k] {
			continue
		}
		if _, ok := custom[k]; ok {
			delete(custom, k)
			n++
		}
	}
	t := doc.Typed
	c.Variables.ButtonColor, c.Variables.LabelColor, c.Variables.InputColor = t.Button, t.Label, t.Input
	c.Variables.ReducedMotion, c.Variables.Clock12, c.Variables.DynamicBg = t.ReducedMotion, t.Clock12, t.DynamicBg
	if w := strings.TrimSpace(t.Wallpaper); w != "" && w != c.Variables.BackgroundImage &&
		(appWallpaperIsShippedDefault(w) || wallpaperFileExists(w)) {
		c.Variables.BackgroundImage = w
		xpackDropDynamicWallpaper()
	}
	uiLanguageApply(c)
	a11yInit(c)
	xpackRepaint(c)
	menuColourSync(c)
	bootSfxBind(c)
	xpackRefreshLabel(c)
	saveUserConfigDebounced(userConfigFromVariables(c))
	return n
}

// devsyncInbox is settings another device sent, waiting for this user.
type devsyncInbox struct {
	from string
	doc  devsyncSettingsDoc
	at   time.Time
}

var devsyncInboxState struct {
	box *devsyncInbox
}

// devsyncInboxGet is the waiting settings (main thread), nil after half an
// hour.
func devsyncInboxGet() *devsyncInbox {
	b := devsyncInboxState.box
	if b != nil && time.Since(b.at) > 30*time.Minute {
		devsyncInboxState.box = nil
		return nil
	}
	return b
}

// devsyncHandleSettings receives another device's settings; nothing changes
// until this user chooses "Use them" on the Save sync page.
func devsyncHandleSettings(w http.ResponseWriter, r *http.Request, call peerCall) {
	var in devsyncSettingsDoc
	if !peerDecodeBody(w, r, 1<<20, &in) {
		return
	}
	if in.V != 1 {
		http.Error(w, "unknown settings format", http.StatusBadRequest)
		return
	}
	allowed := devsyncSettingsKeys()
	doc := devsyncSettingsDoc{V: 1, Keys: map[string]interface{}{}, Typed: in.Typed}
	doc.Typed.Name = ""
	for k, v := range in.Keys {
		if allowed[k] {
			doc.Keys[k] = v
		}
	}
	for _, k := range in.Unset {
		if allowed[k] && len(doc.Unset) < len(allowed) {
			doc.Unset = append(doc.Unset, k)
		}
	}
	name := call.rec.Name
	tbxPost(func() {
		devsyncInboxState.box = &devsyncInbox{from: name, doc: doc, at: time.Now()}
		ShowToast(name+" sent its settings — open Tools → Save sync to use them", ToastKindInfo)
	})
	peerWriteJSONResponse(w, http.StatusOK, map[string]bool{"ok": true})
}

// devsyncSendSettings sends this device's settings to a paired device.
func devsyncSendSettings(ctx context.Context, peerID string, doc devsyncSettingsDoc) error {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return peerDoJSON(cctx, peerID, http.MethodPost, devsyncSettingsPath, doc, nil, 0)
}
