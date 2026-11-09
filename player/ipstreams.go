package main

// ipstreams.go — the IP stream library behind the IPStream screen.
//
// The screen used to have a single URL box: a stream was forgotten as soon as
// the next link was typed. This keeps a small library in the app data directory,
// lets the user add and remove entries, and can bulk-import public IPTV channels
// from the iptv-org playlist on GitHub.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// IPStream is one saved stream: a display name and a playable URL, plus what the
// app has learned about it.
//
// Group is the playlist's group-title (or tvg-country) and drives the panel's
// tabs. LastPlayed and LastResult are written by the play path, so the list can
// put the channel just watched on top and paint a link that never starts red
// before the user presses play on it again.
type IPStream struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	Group      string `json:"group,omitempty"`
	LastPlayed int64  `json:"lastPlayed,omitempty"` // unix milliseconds; 0 = never played
	LastResult string `json:"lastResult,omitempty"` // ipStreamPlayed / ipStreamFailed
}

const (
	// ipStreamPlayed and ipStreamFailed are the two remembered outcomes of a play
	// attempt. An empty LastResult means the channel was never tried.
	ipStreamPlayed = "ok"
	ipStreamFailed = "failed"
	// ipStreamTabAll is the tab that filters nothing out.
	ipStreamTabAll = "All"
	// ipStreamTabOther collects the channels that came from the library file or
	// from a hand-typed link, which carry no playlist group of their own.
	ipStreamTabOther = "Ungrouped"
)

const (
	// ipStreamGitHubURL is the public playlist the "import" action reads.
	ipStreamGitHubURL = "https://iptv-org.github.io/iptv/index.m3u"
	// ipStreamImportLimit keeps the import from filling the SD card with a
	// ten-thousand-entry playlist.
	ipStreamImportLimit = 200
	// ipStreamLibraryFileName is the persisted library file.
	ipStreamLibraryFileName = "ipstreams.json"
)

var (
	ipStreamsMu     sync.Mutex
	ipStreams       []IPStream
	ipStreamsLoaded bool
	ipStreamsCursor int
	ipPanelOpen     bool
	ipPanelStatus   string

	// ipPanelTab is the index into ipPanelTabs and ipPanelQuery is the text the
	// Find row applied; together they are the panel's filter.
	ipPanelTab   int
	ipPanelQuery string
	// ipPanelTabStart is the first pill the tab row draws. The row scrolls so the
	// active tab stays on screen when an import brings more groups than fit.
	ipPanelTabStart int
)

// ipStreamsFilePath is the library location. It sits next to the other app data
// so it survives updates (which never touch the data directory). It is a
// variable so tests can point the store at a temp file.
var ipStreamsFilePath = defaultIPStreamsFilePath

func defaultIPStreamsFilePath() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		dir = "."
	}
	return filepath.Join(dir, ipStreamLibraryFileName)
}

// loadIPStreams reads the library once. A missing file is an empty library.
// The save keeps the previous list as .bak, so an unreadable file comes back
// as the channels the user added instead of as an empty library.
func loadIPStreams() {
	ipStreamsMu.Lock()
	defer ipStreamsMu.Unlock()
	if ipStreamsLoaded {
		return
	}
	ipStreamsLoaded = true
	var list []IPStream
	if _, err := readJSONWithBackup(ipStreamsFilePath(), &list); err != nil {
		// A recovered store is logged by the helper; this line is for the case
		// where the backup failed too, which used to pass for "no channels".
		if !os.IsNotExist(err) {
			logWarn("ipstream", "ignoring unreadable library: %v", err)
		}
		return
	}
	ipStreams = sanitizeIPStreams(list)
}

// sanitizeIPStreams drops unusable entries and duplicates, keeping the first
// occurrence of each URL.
func sanitizeIPStreams(list []IPStream) []IPStream {
	seen := map[string]bool{}
	out := make([]IPStream, 0, len(list))
	for _, s := range list {
		url := strings.TrimSpace(s.URL)
		if !looksLikeStreamURL(url) || seen[url] {
			continue
		}
		s.URL = url
		s.Name = strings.TrimSpace(s.Name)
		if s.Name == "" {
			s.Name = url
		}
		s.Group = sanitizeStreamGroup(s.Group)
		if s.LastResult != ipStreamPlayed && s.LastResult != ipStreamFailed {
			s.LastResult = ""
		}
		if s.LastResult == "" {
			// A time without an outcome is a half-written record: it would sort
			// the channel to the top while painting no verdict, so it is dropped.
			s.LastPlayed = 0
		}
		seen[url] = true
		out = append(out, s)
	}
	return out
}

// sanitizeStreamGroup normalises a playlist group label for a tab. The entries
// come from third-party playlists, so runs of spaces and absurdly long names
// would otherwise break the pill row.
func sanitizeStreamGroup(group string) string {
	group = strings.Join(strings.Fields(group), " ")
	if runes := []rune(group); len(runes) > 28 {
		group = strings.TrimSpace(string(runes[:28]))
	}
	if strings.EqualFold(group, ipStreamTabAll) || strings.EqualFold(group, ipStreamTabOther) {
		// A playlist may name a group after a tab; keep it inside the group tabs
		// so All still means everything.
		group += " group"
	}
	return group
}

// saveIPStreams persists the library atomically.
func saveIPStreams() error {
	ipStreamsMu.Lock()
	list := append([]IPStream(nil), ipStreams...)
	ipStreamsMu.Unlock()
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	path := ipStreamsFilePath()
	// The comment above promised atomicity: a plain WriteFile truncates the
	// list first, so a power cut mid-save lost every channel the user added.
	return AtomicWrite(path, data, 0o644)
}

// ipStreamList returns a copy of the saved streams.
func ipStreamList() []IPStream {
	loadIPStreams()
	ipStreamsMu.Lock()
	defer ipStreamsMu.Unlock()
	return append([]IPStream(nil), ipStreams...)
}

// addIPStream stores a stream. It reports whether the entry was added (a
// duplicate URL is refused rather than appended twice). group comes from an
// imported playlist and is empty for a hand-typed link.
func addIPStream(name, url, group string) bool {
	url = strings.TrimSpace(url)
	if !looksLikeStreamURL(url) {
		return false
	}
	loadIPStreams()
	ipStreamsMu.Lock()
	defer ipStreamsMu.Unlock()
	for _, s := range ipStreams {
		if s.URL == url {
			return false
		}
	}
	if strings.TrimSpace(name) == "" {
		name = url
	}
	ipStreams = append(ipStreams, IPStream{
		Name:  strings.TrimSpace(name),
		URL:   url,
		Group: sanitizeStreamGroup(group),
	})
	return true
}

// ipStreamRecordResult stores how the last play attempt on a saved channel went:
// the panel paints a channel green after a successful start and red after a
// failed one. Anything that is not in the library is ignored, so playing a
// YouTube video never touches the stream file, and a cancelled load is not
// passed here at all — pressing B is not a verdict on the link.
func ipStreamRecordResult(url string, ok bool) bool {
	url = strings.TrimSpace(url)
	if url == "" {
		return false
	}
	loadIPStreams()
	result := ipStreamFailed
	if ok {
		result = ipStreamPlayed
	}
	ipStreamsMu.Lock()
	found := false
	for i := range ipStreams {
		if ipStreams[i].URL != url {
			continue
		}
		ipStreams[i].LastResult = result
		ipStreams[i].LastPlayed = time.Now().UnixMilli()
		found = true
		break
	}
	ipStreamsMu.Unlock()
	if !found {
		return false
	}
	if err := saveIPStreams(); err != nil {
		logWarn("ipstream", "saving the play result: %v", err)
	}
	return true
}

// removeIPStreamByURL deletes one saved channel by its link. The panel's rows are
// sorted for display, so a row index is not a library index and the menu must
// never remove by position.
func removeIPStreamByURL(url string) bool {
	url = strings.TrimSpace(url)
	if url == "" {
		return false
	}
	loadIPStreams()
	ipStreamsMu.Lock()
	idx := -1
	for i := range ipStreams {
		if ipStreams[i].URL == url {
			idx = i
			break
		}
	}
	ipStreamsMu.Unlock()
	return removeIPStreamAt(idx)
}

// removeIPStreamAt deletes one entry, by library position.
func removeIPStreamAt(idx int) bool {
	loadIPStreams()
	ipStreamsMu.Lock()
	defer ipStreamsMu.Unlock()
	if idx < 0 || idx >= len(ipStreams) {
		return false
	}
	ipStreams = append(ipStreams[:idx], ipStreams[idx+1:]...)
	if ipStreamsCursor >= len(ipStreams) {
		ipStreamsCursor = len(ipStreams) - 1
	}
	if ipStreamsCursor < 0 {
		ipStreamsCursor = 0
	}
	return true
}

// parseM3UStreams extracts channel entries from an M3U playlist. Entries without
// an http(s) URL are skipped (the player cannot open them).
func parseM3UStreams(data string, limit int) []IPStream {
	var out []IPStream
	name, group := "", ""
	for _, raw := range strings.Split(data, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#EXTINF") {
			// #EXTINF:-1 tvg-id="..." group-title="...",Channel Name
			if comma := strings.LastIndex(line, ","); comma >= 0 && comma+1 < len(line) {
				name = strings.TrimSpace(line[comma+1:])
			}
			// group-title is the genre/country the panel offers tabs for;
			// tvg-country is the fallback for the playlists that only carry a
			// country code.
			group = m3uAttribute(line, "group-title")
			if group == "" {
				group = m3uAttribute(line, "tvg-country")
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(line), "http://") && !strings.HasPrefix(strings.ToLower(line), "https://") {
			name, group = "", ""
			continue
		}
		entry := IPStream{Name: name, URL: line, Group: group}
		if entry.Name == "" {
			entry.Name = line
		}
		out = append(out, entry)
		name, group = "", ""
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// m3uAttribute reads one key="value" attribute out of an #EXTINF line.
func m3uAttribute(line, key string) string {
	needle := key + `="`
	i := strings.Index(line, needle)
	if i < 0 {
		return ""
	}
	rest := line[i+len(needle):]
	if end := strings.IndexByte(rest, '"'); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(rest)
}

// ipStreamFetchFn downloads the playlist; tests replace it.
var ipStreamFetchFn = func(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("playlist replied %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// importIPStreamsFromGitHub downloads the public playlist and merges new
// channels into the library. It reports how many entries were added.
func importIPStreamsFromGitHub(ctx context.Context, url string) (int, error) {
	if strings.TrimSpace(url) == "" {
		url = ipStreamGitHubURL
	}
	data, err := ipStreamFetchFn(ctx, url)
	if err != nil {
		return 0, err
	}
	list := parseM3UStreams(data, ipStreamImportLimit)
	if len(list) == 0 {
		return 0, fmt.Errorf("no channels found in the playlist")
	}
	added := 0
	for _, s := range list {
		if addIPStream(s.Name, s.URL, s.Group) {
			added++
		}
	}
	if added > 0 {
		if err := saveIPStreams(); err != nil {
			return added, err
		}
	}
	return added, nil
}

// ipPanelRows is the library in the order the panel lists it: most recently
// played first, then the channels that were never played in the order they were
// added. Watching something puts it back on top the next time the menu opens,
// while a fresh import stays in playlist order until it is used.
func ipPanelRows() []IPStream {
	loadIPStreams()
	ipStreamsMu.Lock()
	rows := append([]IPStream(nil), ipStreams...)
	ipStreamsMu.Unlock()
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].LastPlayed > rows[j].LastPlayed })
	return rows
}

// ipPanelTabs lists the tab row: All, then one tab per playlist group in the
// order the channels are stored, then Ungrouped when a library mixes grouped and
// hand-added channels. A library with no groups has a single tab and the panel
// hides the row.
func ipPanelTabs() []string {
	tabs := []string{ipStreamTabAll}
	seen := map[string]bool{}
	ungrouped := false
	for _, s := range ipPanelRows() {
		if s.Group == "" {
			ungrouped = true
			continue
		}
		if seen[s.Group] {
			continue
		}
		seen[s.Group] = true
		tabs = append(tabs, s.Group)
	}
	if ungrouped && len(tabs) > 1 {
		tabs = append(tabs, ipStreamTabOther)
	}
	return tabs
}

// ipPanelTabLabel is the active tab, clamped to the tabs the library has: the
// last channel of a group can be removed while its tab is selected.
func ipPanelTabLabel() string {
	tabs := ipPanelTabs()
	if ipPanelTab < 0 {
		ipPanelTab = 0
	}
	if ipPanelTab >= len(tabs) {
		ipPanelTab = len(tabs) - 1
	}
	return tabs[ipPanelTab]
}

// ipPanelCycleTab moves to the next tab and puts the cursor on its first channel,
// so the D-pad right after L1/R1 lands on a playable row instead of a stale slot.
func ipPanelCycleTab(delta int) bool {
	tabs := ipPanelTabs()
	if len(tabs) < 2 || delta == 0 {
		return false
	}
	next := (ipPanelTab + delta) % len(tabs)
	if next < 0 {
		next += len(tabs)
	}
	if next == ipPanelTab {
		return false
	}
	ipPanelTab = next
	ipPanelCursorToFirstChannel()
	return true
}

// ipStreamInTab reports whether a channel belongs to a tab.
func ipStreamInTab(s IPStream, tab string) bool {
	switch tab {
	case ipStreamTabAll:
		return true
	case ipStreamTabOther:
		return s.Group == ""
	}
	return strings.EqualFold(s.Group, tab)
}

// ipStreamMatchesQuery reports whether a channel matches the Find row's text:
// the name, the group and the link all count, so "bbc" and "m3u8" both find
// something.
func ipStreamMatchesQuery(s IPStream, query string) bool {
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return true
	}
	for _, field := range []string{s.Name, s.Group, s.URL} {
		if strings.Contains(strings.ToLower(field), query) {
			return true
		}
	}
	return false
}

// ipPanelCursorToFirstChannel puts the cursor on the first channel row after the
// list narrows. An empty result falls back to the top, so the cursor is never
// left on a row that no longer exists.
func ipPanelCursorToFirstChannel() {
	items := ipPanelItems()
	if len(items) > ipPanelActionRows {
		ipStreamsCursor = ipPanelActionRows
		return
	}
	ipPanelClamp(items)
}

// ipPanelChannelList is the channels the menu is showing, in row order. Play and
// remove read it so a filtered list acts on the row under the cursor rather than
// on the same number in the unfiltered library.
func ipPanelChannelList() []IPStream {
	var out []IPStream
	for _, it := range ipPanelItems() {
		if it.Kind == ipItemChannel {
			out = append(out, it.Stream)
		}
	}
	return out
}

// ipPanelItemKind is the kind of row in the stream menu.
type ipPanelItemKind int

const (
	// ipItemChannel is a saved stream: A plays it, Start removes it.
	ipItemChannel ipPanelItemKind = iota
	// ipItemAdd asks for a link, saves it as a channel and plays it.
	ipItemAdd
	// ipItemBrowse fetches the public IPTV playlist.
	ipItemBrowse
	// ipItemFind narrows the list to the channels that match a typed query.
	ipItemFind
)

// ipPanelActionRows is how many always-available rows sit above the channels.
const ipPanelActionRows = 3

// ipPanelItem is one row of the stream menu: an action, or a saved channel.
type ipPanelItem struct {
	Kind   ipPanelItemKind
	Stream IPStream
}

// ipPanelItems builds the menu: the two ways to get content first, then every
// saved channel. The panel used to be the channel list alone, so on a fresh
// install the IPStream button opened a note telling the user to type a link on
// a screen it had not opened, with nothing to play — a dead end on the Media
// page, where the button is the only way in.
func ipPanelItems() []ipPanelItem {
	rows := ipPanelRows()
	tab := ipPanelTabLabel()
	items := make([]ipPanelItem, 0, len(rows)+ipPanelActionRows)
	items = append(items,
		ipPanelItem{Kind: ipItemAdd},
		ipPanelItem{Kind: ipItemBrowse},
		ipPanelItem{Kind: ipItemFind},
	)
	for _, s := range rows {
		if !ipStreamInTab(s, tab) || !ipStreamMatchesQuery(s, ipPanelQuery) {
			continue
		}
		items = append(items, ipPanelItem{Kind: ipItemChannel, Stream: s})
	}
	return items
}

// ipPanelChannelIndex converts a menu cursor into an index in the channel rows the
// menu is showing; -1 means the cursor is on one of the action rows.
func ipPanelChannelIndex(cursor int) int {
	if cursor < ipPanelActionRows {
		return -1
	}
	return cursor - ipPanelActionRows
}

// ipPanelClamp keeps the cursor inside the menu after a removal or a reload.
func ipPanelClamp(items []ipPanelItem) {
	if ipStreamsCursor >= len(items) {
		ipStreamsCursor = len(items) - 1
	}
	if ipStreamsCursor < 0 {
		ipStreamsCursor = 0
	}
}

// openIPStreamPanel shows the stream menu.
func openIPStreamPanel() {
	qs.open = false
	toolsPanel.open = false
	processPanel.open = false
	loadIPStreams()
	ipPanelOpen = true
	ipPanelStatus = ""
	// A search does not outlive the menu: opening Streams a week later with a
	// stale query would look like a library that lost its channels. The tab is
	// kept, because the pill row shows which one is selected.
	ipPanelQuery = ""
	ipPanelClamp(ipPanelItems())
	PlayActivateSound()
}

func closeIPStreamPanel() {
	ipPanelOpen = false
	PlayBackSound()
}

// ipPanelActivate runs the highlighted row: an action row does its action, a
// channel row plays that channel. D-pad, Enter and a click all land here, so a
// row can never look selected without doing anything.
func ipPanelActivate(config *Config) {
	items := ipPanelItems()
	ipPanelClamp(items)
	if len(items) == 0 {
		return
	}
	switch items[ipStreamsCursor].Kind {
	case ipItemAdd:
		ipPanelAskForLink(config)
	case ipItemBrowse:
		ipPanelImport()
	case ipItemFind:
		ipPanelAskForQuery()
	default:
		ipPanelPlay(config)
	}
}

// ipPromptPending marks the one keyboard session opened by the menu's "Add a
// stream link" row, so its submit saves a channel instead of searching.
var ipPromptPending bool

// ipPanelAskForLink asks for a stream link without leaving the app. The
// keyboard edits the screen's own link field when it has one (the Streams
// screen and Media both do), so the typed link stays visible afterwards.
func ipPanelAskForLink(config *Config) bool {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return false
	}
	scene := config.Scenes[currentSceneIndex]
	field := -1
	for i, e := range scene.Elements {
		if e.Type != "input" {
			continue
		}
		if field < 0 {
			field = i
		}
		if e.Variable == "stream_url" {
			field = i
			break
		}
	}
	if field < 0 {
		ShowToast("Open Streams to add a channel", ToastKindInfo)
		return false
	}
	ipPanelOpen = false
	ipPromptPending = true
	vkOpenSceneInput(config, currentSceneIndex, field)
	inputTextBuffer = ""
	vkTextSelected = false
	ShowToast("Paste a stream link, then Save", ToastKindInfo)
	return true
}

// ipPanelAskForQuery opens the keyboard for the Find row. The panel stays open
// behind it and re-filters on every keystroke, so the list narrows while the
// query is typed instead of after it is submitted.
func ipPanelAskForQuery() {
	vkOpenIPStreamSearch()
	ShowToast("Type part of a name, a group or a link", ToastKindInfo)
}

// ipPanelSubmitQuery commits the Find row's text. The filter already applied
// while typing; this trims it, drops back to All and says how many channels are
// left, because a query that matches nothing would otherwise look like a menu
// that lost the library.
func ipPanelSubmitQuery(text string) {
	ipPanelQuery = strings.TrimSpace(text)
	ipPanelTab = 0
	ipPanelCursorToFirstChannel()
	ipPanelOpen = true
	shown := len(ipPanelChannelList())
	if ipPanelQuery == "" {
		ShowToast("Showing every channel", ToastKindInfo)
		return
	}
	if shown == 0 {
		ShowToast("Nothing matches \""+ipPanelQuery+"\"", ToastKindWarning)
		return
	}
	ShowToast(fmt.Sprintf("%d channels match \"%s\"", shown, ipPanelQuery), ToastKindInfo)
}

// ipPanelPlayLink plays a channel. It is a variable so the menu's row actions
// can be tested without starting a real player.
var ipPanelPlayLink = playVideoURL

// submitIPStreamLink saves the link typed into the menu and plays it.
func submitIPStreamLink(config *Config, text string) {
	link := strings.TrimSpace(text)
	if !looksLikeStreamURL(link) {
		ShowToast("That is not a stream link", ToastKindWarning)
		return
	}
	name := channelNameFromURL(link)
	if addIPStream(name, link, "") {
		if err := saveIPStreams(); err != nil {
			ShowToast("Could not save the channel: "+firstLine(err.Error()), ToastKindError)
		} else {
			ShowToast("Added "+name, ToastKindSuccess)
		}
	}
	ipPanelPlayLink(config, link)
}

// ipPanelSaveCurrentLink saves whatever link the scene's URL box holds.
func ipPanelSaveCurrentLink(config *Config) {
	url := ""
	if config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
		url = sceneInputURL(config, config.Scenes[currentSceneIndex])
	}
	if url == "" {
		ShowToast("Type a stream link in the URL box first", ToastKindWarning)
		return
	}
	name := channelNameFromURL(url)
	if !addIPStream(name, url, "") {
		ShowToast("That stream is already saved", ToastKindInfo)
		return
	}
	if err := saveIPStreams(); err != nil {
		ShowToast("Could not save the stream: "+firstLine(err.Error()), ToastKindError)
		return
	}
	PlaySuccessSound()
	ShowToast("Saved "+name, ToastKindSuccess)
}

// ipPanelPlay plays the highlighted stream.
func ipPanelPlay(config *Config) {
	list := ipPanelChannelList()
	idx := ipPanelChannelIndex(ipStreamsCursor)
	if idx < 0 || idx >= len(list) {
		return
	}
	entry := list[idx]
	ipPanelOpen = false
	ipPanelPlayLink(config, entry.URL)
}

// ipPanelRemove deletes the highlighted stream. The action rows above the
// channels are not removable, so the cursor maps through them first, and the link
// is what gets removed: the rows are sorted for display, so their index is not a
// library index.
func ipPanelRemove() {
	list := ipPanelChannelList()
	idx := ipPanelChannelIndex(ipStreamsCursor)
	if idx < 0 || idx >= len(list) {
		return
	}
	name := list[idx].Name
	if !removeIPStreamByURL(list[idx].URL) {
		return
	}
	if err := saveIPStreams(); err != nil {
		ShowToast("Could not update the library: "+firstLine(err.Error()), ToastKindError)
		return
	}
	ShowToast("Removed "+name, ToastKindInfo)
}

// ipPanelImport fetches the GitHub playlist in the background.
func ipPanelImport() {
	ipPanelStatus = "Importing from GitHub..."
	ShowToast("Importing channels from GitHub...", ToastKindInfo)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		added, err := importIPStreamsFromGitHub(ctx, "")
		if err != nil {
			ipPanelStatus = "Import failed: " + firstLine(err.Error())
			showToast("Import failed: "+firstLine(err.Error()), ToastError())
			return
		}
		ipPanelStatus = fmt.Sprintf("Imported %d channels", added)
		showToast(fmt.Sprintf("Imported %d channels", added), ToastSuccess())
	}()
}

// channelNameFromURL derives a readable label from a stream URL.
func channelNameFromURL(url string) string {
	s := strings.TrimSpace(url)
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "rtsp://")
	s = strings.TrimPrefix(s, "rtmp://")
	s = strings.TrimPrefix(s, "udp://")
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "stream"
	}
	return s
}

// ipPanelMove moves the menu cursor by one row and reports whether it moved.
// The cursor walks every row, actions included, so the D-pad reaches the way
// to add a channel even when the library is empty.
func ipPanelMove(delta int) bool {
	items := ipPanelItems()
	ipPanelClamp(items)
	next := ipStreamsCursor + delta
	if next < 0 || next >= len(items) {
		return false
	}
	ipStreamsCursor = next
	return true
}

// ctrlIPPanel is called before every other controller overlay.
func ctrlIPPanel(e *sdl.ControllerButtonEvent, config *Config) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if ipPanelMove(-1) {
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		if ipPanelMove(1) {
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		// The D-pad and the shoulders do the same job here: the panel has one
		// list, so left/right belongs to the tab row.
		if ipPanelCycleTab(-1) {
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT, sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		if ipPanelCycleTab(1) {
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_A:
		ipPanelActivate(config)
	case sdl.CONTROLLER_BUTTON_X:
		ipPanelSaveCurrentLink(config)
	case sdl.CONTROLLER_BUTTON_Y:
		ipPanelImport()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeIPStreamPanel()
	case sdl.CONTROLLER_BUTTON_START:
		ipPanelRemove()
	}
	return true
}

// handleIPPanelKey consumes every key while the panel is open.
func handleIPPanelKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !ipPanelOpen {
		return false
	}
	// The Find row's keyboard owns the keys while it is up; the panel behind it
	// only draws the narrowed list.
	if virtualKeyboardActive {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		if ipPanelMove(-1) {
			PlayNavSound()
		}
	case sdl.K_DOWN:
		if ipPanelMove(1) {
			PlayNavSound()
		}
	case sdl.K_LEFT:
		if ipPanelCycleTab(-1) {
			PlayNavSound()
		}
	case sdl.K_RIGHT:
		if ipPanelCycleTab(1) {
			PlayNavSound()
		}
	case sdl.K_TAB:
		delta := 1
		if e.Keysym.Mod&sdl.KMOD_SHIFT != 0 {
			delta = -1
		}
		if ipPanelCycleTab(delta) {
			PlayNavSound()
		}
	case sdl.K_SLASH:
		ipPanelAskForQuery()
	case sdl.K_RETURN, sdl.K_SPACE:
		ipPanelActivate(config)
	case sdl.K_a, sdl.K_x:
		// X on the pad saves the URL box, so the keyboard's X does the same. A
		// stays as the older alias for the same action.
		ipPanelSaveCurrentLink(config)
	case sdl.K_y:
		ipPanelImport()
	case sdl.K_DELETE, sdl.K_BACKSPACE:
		ipPanelRemove()
	case sdl.K_ESCAPE, sdl.K_b:
		closeIPStreamPanel()
	}
	return true
}

// ipPanelRowRects is filled by renderIPPanel so a click maps back to a row.
var ipPanelRowRects []sdl.Rect

// ipPanelRowFirst is the list index of ipPanelRowRects[0]; the window scrolls
// with the cursor, so a clicked slot is first+slot, not the slot number.
var ipPanelRowFirst int

var ipPanelRect sdl.Rect

// ipPanelTabRects is filled by the tab row so a click maps back to a tab, and
// ipPanelTabFirst is the tab index of ipPanelTabRects[0] when the row scrolls.
var (
	ipPanelTabRects []sdl.Rect
	ipPanelTabFirst int
)

// handleIPPanelMouse activates a clicked row or tab, or closes on an outside
// click.
func handleIPPanelMouse(mx, my int32, config *Config) bool {
	if !ipPanelOpen || virtualKeyboardActive {
		return false
	}
	for i, r := range ipPanelTabRects {
		if r.W > 0 && pointInRect(mx, my, r) {
			if tab := ipPanelTabFirst + i; tab != ipPanelTab {
				ipPanelTab = tab
				ipPanelCursorToFirstChannel()
				PlayNavSound()
			}
			return true
		}
	}
	for i, r := range ipPanelRowRects {
		if r.W > 0 && pointInRect(mx, my, r) {
			ipStreamsCursor = ipPanelRowFirst + i
			ipPanelActivate(config)
			return true
		}
	}
	if ipPanelRect.W > 0 && !pointInRect(mx, my, ipPanelRect) {
		closeIPStreamPanel()
	}
	return true
}

// ipStreamStatusColor is the label colour for a channel's last attempt: green
// after a successful start, red after a failure, nothing when it was never tried.
func ipStreamStatusColor(s IPStream) (sdl.Color, bool) {
	switch s.LastResult {
	case ipStreamPlayed:
		return SuccessInk(), true
	case ipStreamFailed:
		return DangerInk(), true
	}
	return sdl.Color{}, false
}

// ipStreamStatusFill is the same verdict as a dot: the authored fills, which are
// graphics rather than text and so are not tinted for legibility.
func ipStreamStatusFill(s IPStream) (sdl.Color, bool) {
	switch s.LastResult {
	case ipStreamPlayed:
		return ColorSuccess, true
	case ipStreamFailed:
		return ColorDanger, true
	}
	return sdl.Color{}, false
}

// ipPanelItemText is a row's title and the dim detail shown on its right.
func ipPanelItemText(item ipPanelItem) (string, string) {
	switch item.Kind {
	case ipItemAdd:
		return "Add a stream link", "type or paste a URL"
	case ipItemBrowse:
		return "Browse channels", "import public IPTV channels"
	case ipItemFind:
		if query := strings.TrimSpace(ipPanelQuery); query != "" {
			return "Find channels", query
		}
		return "Find channels", "name, group or link"
	}
	return item.Stream.Name, item.Stream.URL
}

// renderIPPanel draws the stream menu.
func renderIPPanel(renderer *sdl.Renderer, config *Config) {
	if !ipPanelOpen || renderer == nil {
		return
	}
	items := ipPanelItems()
	ipPanelClamp(items)
	channels := len(items) - ipPanelActionRows
	total := len(ipPanelRows())

	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}
	smallH := int32(font.Height())
	const padX = OverlayPadX
	lineH := smallH + 10
	visible := 10
	if screenHeight < 700 {
		visible = 8
	}
	if len(items) < visible {
		visible = len(items)
	}
	if visible < 1 {
		visible = 1
	}

	// The title band is the shared page header, built before the card is sized so
	// the panel and the bar that fills it measure the same height.
	sub := fmt.Sprintf("%d saved channels", total)
	if channels != total {
		// The tab and the search narrow the list, and the count has to say so:
		// a channel missing from a tab otherwise looks deleted.
		sub = fmt.Sprintf("%d of %d channels", channels, total)
	}
	if ipPanelStatus != "" {
		sub = ipPanelStatus
	}
	header := UIPanelHeader("Stream", sub, "media")
	headerH := UIPanelHeaderBand(config, header, 8)
	footerH := 20 + smallH
	panelW := int32(760)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	tabs := ipPanelTabs()
	// The tab row costs one line, and only exists when the library has groups to
	// switch between.
	tabH := int32(0)
	if len(tabs) > 1 {
		tabH = smallH + 14
	}
	panelH := headerH + tabH + int32(visible)*lineH + footerH + smallH + 6
	if panelH > screenHeight-40 {
		panelH = screenHeight - 40
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	ipPanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH})
	drawOverlayCardAccent(renderer, panelX, panelY, panelW)

	header = overlayHeaderOnCard(header, sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH})
	UIPageHeaderDraw(renderer, config, header)

	rowW := panelW - 2*padX
	focusColor := installHighlight()
	ipPanelRowRects = make([]sdl.Rect, 0, visible)
	rowY := panelY + headerH
	if tabH > 0 {
		rowY = ipPanelDrawTabs(renderer, config, font, tabs,
			sdl.Rect{X: panelX + padX, Y: panelY + headerH + 4, W: rowW, H: tabH - 8}, focusColor)
	}
	first := 0
	if ipStreamsCursor >= visible {
		first = ipStreamsCursor - visible + 1
	}
	ipPanelRowFirst = first
	for i := 0; i < visible; i++ {
		idx := first + i
		if idx >= len(items) {
			break
		}
		item := items[idx]
		rect := sdl.Rect{X: panelX + padX, Y: rowY, W: rowW, H: lineH - 2}
		ipPanelRowRects = append(ipPanelRowRects, rect)
		focused := idx == ipStreamsCursor
		if focused {
			fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 8, WithAlpha(focusColor, 46))
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 8, 1, focusColor)
		}
		textColor := ColorTextSecondary()
		if focused || item.Kind != ipItemChannel {
			// The rows that get content read as actions, not as dim inventory.
			textColor = ColorTextPrimary()
		}
		nameX := rect.X + 12
		nameW := rowW - 24
		if item.Kind == ipItemChannel {
			// A dot before the name carries the last attempt — green started,
			// red failed, faint never tried — and the name is painted in the same
			// verdict. The dot column is reserved on every channel so the names
			// stay aligned.
			dotColor := WithAlpha(ColorTextTertiary(), 90)
			if c, ok := ipStreamStatusFill(item.Stream); ok {
				dotColor = c
				if ink, ok := ipStreamStatusColor(item.Stream); ok {
					textColor = ink
				}
			}
			fillRoundedRect(renderer, rect.X+12, rect.Y+lineH/2-7, 6, 6, 3, dotColor)
			nameX = rect.X + 24
			nameW = rowW - 36
		}
		name, detail := ipPanelItemText(item)
		name = ellipsize(font, name, nameW)
		renderText(renderer, config, font, name, textColor, nameX, rowY+3)
		detail = ellipsize(font, detail, rowW/2)
		uw, _, _ := font.SizeUTF8(detail)
		renderText(renderer, config, font, detail, ColorTextTertiary(), rect.X+rect.W-int32(uw)-12, rowY+3)
		rowY += lineH
	}
	if channels == 0 && rowY+lineH <= panelY+panelH-footerH {
		msg := "No saved channels yet — the rows above add some."
		if total > 0 {
			// An empty list that still has channels in it is a filter, not an
			// empty library, and saying so points at the way out.
			msg = "No channel matches this tab or search."
		}
		renderText(renderer, config, font, ellipsize(font, msg, rowW),
			ColorTextTertiary(), panelX+padX, rowY+3)
	}

	// Every hint names the pad button and the key that does the same thing, so a
	// handheld and a Windows keyboard are told the same story. Narrow panels get
	// the short spelling of the same list.
	footer := hintLine(FooterHint{"L1/R1", "Tab"}, FooterHint{"/", "Find"}, FooterHint{"A / Enter", "Open"},
		FooterHint{"X", "Save Link"}, FooterHint{"Y", "Import"}, FooterHint{"Start / Del", "Remove"},
		FooterHint{"B / Esc", "Close"})
	if fw, _, _ := font.SizeUTF8(footer); int32(fw) > rowW {
		footer = hintLine(FooterHint{"L1/R1", "Tab"}, FooterHint{"A", "Open"}, FooterHint{"X", "Save"},
			FooterHint{"Del", "Remove"}, FooterHint{"B / Esc", "Close"})
	}
	if fw, _, _ := font.SizeUTF8(footer); int32(fw) > rowW {
		// The tab hint survives to the last tier: the pill row is the newest
		// thing on the card, and the one people will not guess is there.
		footer = hintLine(FooterHint{"L1/R1", "Tab"}, FooterHint{"A", "Open"},
			FooterHint{"Del", "Remove"}, FooterHint{"B / Esc", "Close"})
	}
	fw, _, _ := font.SizeUTF8(footer)
	renderText(renderer, config, font, footer, ColorTextTertiary(),
		panelX+(panelW-int32(fw))/2, panelY+panelH-footerH+8)
}

// ipPanelDrawTabs paints the tab row and returns the y the channel rows start at.
// The row scrolls sideways around the active tab, so a playlist with more groups
// than fit still shows the one selected; clicks land through ipPanelTabRects.
func ipPanelDrawTabs(renderer *sdl.Renderer, config *Config, font *ttf.Font, tabs []string, area sdl.Rect, focusColor sdl.Color) int32 {
	widths := make([]int32, len(tabs))
	for i, label := range tabs {
		w, _, _ := font.SizeUTF8(label)
		widths[i] = int32(w) + 22
	}
	if ipPanelTabStart < 0 {
		ipPanelTabStart = 0
	}
	if ipPanelTabStart > ipPanelTab {
		ipPanelTabStart = ipPanelTab
	}
	// Pull the first pill left until the active one fits beside it: the row is
	// anchored on the tab the user is looking at, not on the first group.
	for ipPanelTabStart < ipPanelTab {
		need := int32(0)
		for i := ipPanelTabStart; i <= ipPanelTab; i++ {
			need += widths[i] + 6
		}
		if need <= area.W {
			break
		}
		ipPanelTabStart++
	}

	textH := int32(font.Height())
	x := area.X
	ipPanelTabRects = ipPanelTabRects[:0]
	ipPanelTabFirst = ipPanelTabStart
	for i := ipPanelTabStart; i < len(tabs); i++ {
		w := widths[i]
		if x+w > area.X+area.W {
			break
		}
		rect := sdl.Rect{X: x, Y: area.Y, W: w, H: area.H}
		ipPanelTabRects = append(ipPanelTabRects, rect)
		label := tabs[i]
		color := ColorTextTertiary()
		if i == ipPanelTab {
			fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, rect.H/2, WithAlpha(focusColor, 46))
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, rect.H/2, 1, focusColor)
			color = ColorTextPrimary()
		} else {
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, rect.H/2, 1,
				WithAlpha(ColorTextTertiary(), 90))
		}
		label = ellipsize(font, label, w-12)
		lw, _, _ := font.SizeUTF8(label)
		renderText(renderer, config, font, label, color, rect.X+(w-int32(lw))/2, rect.Y+(rect.H-textH)/2)
		x += w + 6
	}
	return area.Y + area.H + 10
}

// ensureIPStreamEntries gives the IPStream scene a library button and the Media
// (Tube) screen an IPStream entry point, in memory only.
func ensureIPStreamEntries(config *Config) {
	if config == nil {
		return
	}
	for i := range config.Scenes {
		scene := &config.Scenes[i]
		switch scene.Name {
		case "IPStream":
			if sceneHasTriggerElement(*scene, "ip_open") {
				continue
			}
			// "Channels" name the menu, not the library folder: the screen's own
			// Stream button plays whatever is in the URL box, and this one lists
			// (and adds) the saved ones.
			scene.Elements = append(scene.Elements, Element{
				Type: "button", Text: "Channels", Color: "$labelColor", BgColor: "$buttonColor",
				X: 912, Y: 72, Width: "160", Height: "48", Trigger: "ip_open",
			})
		case "Tube":
			if sceneHasTriggerElement(*scene, "ip_open") {
				continue
			}
			// Sit after the last button on the search row.
			x := int32(1000)
			for _, e := range scene.Elements {
				if e.Type == "button" && e.Y == 72 {
					if right := e.X + getElementWidth(e, 116) + 8; right > x {
						x = right
					}
				}
			}
			// Clamp against the config's own canvas, not the global screenWidth:
			// this runs while the config loads, before the window size is known,
			// so screenWidth was still 0 and pushed the button off the left edge
			// (very visible on the Brick, where it should follow Shorts).
			canvasW := int32(config.Width)
			if canvasW <= 0 {
				canvasW = int32(config.Variables.ScreenWidth)
			}
			if canvasW <= 0 {
				canvasW = 1280
			}
			if x+116 > canvasW-16 {
				x = canvasW - 16 - 116
			}
			// The label is the screen's own display name ("Streams"), not the
			// internal "IPStream": the button opens the Streams menu, and the
			// header it opens says the same word.
			scene.Elements = append(scene.Elements, Element{
				Type: "button", Text: "Streams", Color: "$labelColor", BgColor: "$buttonColor",
				X: x, Y: 72, Width: "116", Height: "48", Trigger: "ip_open",
			})
		}
	}
}

// sceneHasTriggerElement reports whether any element already uses trigger.
func sceneHasTriggerElement(scene SceneConfig, trigger string) bool {
	for _, e := range scene.Elements {
		if e.Trigger == trigger {
			return true
		}
	}
	return false
}
