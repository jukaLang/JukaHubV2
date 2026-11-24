package main

// toolsearch.go — the Tools hub's search and pins.
//
// The hub holds some fifty tools in seven sections, and reaching the one
// wanted meant scrolling past the rest. Now:
//
//   - Y (or /, Ctrl+F) opens the keyboard and the page filters as the letters
//     are typed: every word must appear in a tool's name, its line, its
//     section or the words people use for it (toolsSearchWords), names that
//     start with the first word come first. The Network tools page's tools are
//     found from here too, and open that page on the tool. B clears the search
//     before it leaves the hub; opening the hub afresh starts unfiltered;
//   - X pins the focused tool: pinned tools lead the page in a Pinned section,
//     in the order they were pinned (twelve at most), and keep a star on their
//     own tile. X on either copy unpins. The pins live in tools-pins.json in
//     JukaHub's data folder, which backups carry.
//
// Both act on the tile list toolsHubBuildTiles makes (toolsHubFindApply), so a
// tool this device or config does not have is neither found nor pinned.

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// toolsPinPrefix starts the id of a tile's copy in the Pinned section,
	// toolsNetPrefix the id of a Network tools tool found by the search.
	toolsPinPrefix = "pin:"
	toolsNetPrefix = "net:"
	toolsPinsFile  = "tools-pins.json"
	toolsPinsMax   = 12
)

// toolsHubFind is the search and the pins. Main thread only.
var toolsHubFind struct {
	// query is the applied search; typing says the search keyboard is up and
	// the page follows its text. applied is the search the tiles on screen
	// were built with.
	query   string
	typing  bool
	applied string
	pins    []string
	loaded  bool
}

// toolsPinsDoc is tools-pins.json.
type toolsPinsDoc struct {
	Pins []string `json:"pins"`
}

// toolsPinsLoad reads the pins once.
func toolsPinsLoad() {
	if toolsHubFind.loaded {
		return
	}
	toolsHubFind.loaded = true
	data, err := os.ReadFile(osAppsDataPath(toolsPinsFile))
	if err != nil {
		return
	}
	var doc toolsPinsDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		logWarn("tools", "ignoring an unreadable %s: %v", toolsPinsFile, err)
		return
	}
	toolsHubFind.pins = toolsPinsClean(doc.Pins)
}

// toolsPinsClean drops blanks, duplicates and copies' ids, and keeps at most
// toolsPinsMax. Pure.
func toolsPinsClean(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, id := range in {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] || strings.HasPrefix(id, toolsPinPrefix) {
			continue
		}
		seen[id] = true
		out = append(out, id)
		if len(out) == toolsPinsMax {
			break
		}
	}
	return out
}

// toolsPinsSave orders the writes: only the newest one runs.
var toolsPinsSave struct {
	mu  sync.Mutex
	gen uint64
}

// toolsPinsPersist writes the pins off the main thread.
func toolsPinsPersist() {
	data, err := json.MarshalIndent(toolsPinsDoc{Pins: append([]string(nil), toolsHubFind.pins...)}, "", " ")
	if err != nil {
		return
	}
	data = append(data, '\n')
	toolsPinsSave.mu.Lock()
	toolsPinsSave.gen++
	gen := toolsPinsSave.gen
	toolsPinsSave.mu.Unlock()
	go func() {
		toolsPinsSave.mu.Lock()
		defer toolsPinsSave.mu.Unlock()
		if gen != toolsPinsSave.gen {
			return
		}
		if err := AtomicWrite(osAppsDataPath(toolsPinsFile), data, 0o644); err != nil {
			logWarn("tools", "could not save the pinned tools: %v", err)
		}
	}()
}

// toolsHubBaseID is a tile's own id, without the Pinned copy's prefix.
func toolsHubBaseID(id string) string {
	return strings.TrimPrefix(id, toolsPinPrefix)
}

// toolsPinIndex is where id is among the pins, or -1.
func toolsPinIndex(id string) int {
	for i, p := range toolsHubFind.pins {
		if p == id {
			return i
		}
	}
	return -1
}

// toolsHubPinFocused is X on the hub: pin the focused tool, or unpin it.
func toolsHubPinFocused() {
	p := &toolsHub.page
	if p.chip || p.cursor < 0 || p.cursor >= len(toolsHub.tiles) {
		return
	}
	toolsPinsLoad()
	t := toolsHub.tiles[p.cursor]
	id := toolsHubBaseID(t.id)
	if i := toolsPinIndex(id); i >= 0 {
		toolsHubFind.pins = append(toolsHubFind.pins[:i], toolsHubFind.pins[i+1:]...)
		PlayBackSound()
		ShowToast("Unpinned "+t.label, ToastKindInfo)
	} else {
		if len(toolsHubFind.pins) >= toolsPinsMax {
			ShowToast("Up to twelve tools can be pinned — unpin one first", ToastKindWarning)
			return
		}
		toolsHubFind.pins = append(toolsHubFind.pins, id)
		PlayActivateSound()
		ShowToast("Pinned "+t.label+" to the top of Tools", ToastKindSuccess)
	}
	toolsPinsPersist()
	toolsHubRebuild(appConfig)
}

// ── Search ────────────────────────────────────────────────────────────────

// toolsHubSearchOpen is Y on the hub: the keyboard, starting from the search
// on screen.
func toolsHubSearchOpen() {
	PlayActivateSound()
	toolsHubFind.typing = true
	osPromptOpen("Search tools", "A tool's name, or what it does", "Search", toolsHubFind.query, 40, toolsHubSearchSubmit)
}

// toolsHubSearchSubmit is the keyboard's Search: the text becomes the filter
// ("" shows every tool again).
func toolsHubSearchSubmit(text string) {
	toolsHubFind.typing = false
	toolsHubFind.query = strings.TrimSpace(text)
	if appConfig != nil {
		toolsHubRebuild(appConfig)
	}
}

// toolsHubSearchClear is B while a search is applied. It reports whether there
// was one to clear.
func toolsHubSearchClear() bool {
	if toolsHubFind.query == "" {
		return false
	}
	toolsHubFind.query = ""
	PlayBackSound()
	if appConfig != nil {
		toolsHubRebuild(appConfig)
	}
	return true
}

// toolsHubFindQuery is the search the page should show: the keyboard's text
// while it is up, else the applied one.
func toolsHubFindQuery() string {
	if toolsHubFind.typing {
		if osPromptActive() {
			return strings.TrimSpace(osPrompt.buffer)
		}
		toolsHubFind.typing = false
	}
	return toolsHubFind.query
}

// toolsHubFindStale reports whether the tiles on screen were built for another
// search than the one to show (a letter was just typed).
func toolsHubFindStale() bool {
	return toolsHubFindQuery() != toolsHubFind.applied
}

// toolsHubFindApply turns the hub's whole tile list into what the page shows:
// the tools matching the search, or the pinned tools above the list.
func toolsHubFindApply(tiles []toolsHubTile, net toolsHubNet) []toolsHubTile {
	toolsPinsLoad()
	query := toolsHubFindQuery()
	toolsHubFind.applied = query
	extra := toolsNetSearchTiles(tiles, net)
	if query != "" {
		return toolsHubFindFilter(tiles, extra, query)
	}
	return toolsHubWithPins(tiles, extra)
}

// toolsNetSearchTiles are the Network tools page's tools as hub tiles, for the
// search and the pins; the ones the hub has a tile of its own for are left
// out.
func toolsNetSearchTiles(tiles []toolsHubTile, net toolsHubNet) []toolsHubTile {
	have := make(map[string]bool, len(tiles))
	for _, t := range tiles {
		have[t.id] = true
	}
	out := make([]toolsHubTile, 0, len(netToolsSpecs))
	for _, spec := range netToolsSpecs {
		if have[spec.id] {
			continue
		}
		value := "Network tools"
		if spec.idle != "" {
			value += " · " + spec.idle
		}
		out = append(out, toolsHubTile{id: toolsNetPrefix + spec.id, label: spec.label, icon: spec.icon, need: spec.need, value: value})
	}
	toolsHubApplyNeeds(out, net)
	return out
}

// toolsHubWithPins puts the pinned tools' copies in a Pinned section at the
// top and stars their own tiles.
func toolsHubWithPins(tiles, extra []toolsHubTile) []toolsHubTile {
	if len(toolsHubFind.pins) == 0 {
		return tiles
	}
	byID := make(map[string]toolsHubTile, len(tiles)+len(extra))
	for _, t := range extra {
		byID[t.id] = t
	}
	for i := range tiles {
		if toolsPinIndex(tiles[i].id) >= 0 {
			tiles[i].pinned = true
		}
		byID[tiles[i].id] = tiles[i]
	}
	out := make([]toolsHubTile, 0, len(toolsHubFind.pins)+len(tiles))
	for _, id := range toolsHubFind.pins {
		t, ok := byID[id]
		if !ok {
			// A tool this device or config does not have: kept, not shown.
			continue
		}
		t.id = toolsPinPrefix + id
		t.section, t.sectionIcon = "", ""
		t.wide, t.pinned = false, true
		out = append(out, t)
	}
	if len(out) == 0 {
		return tiles
	}
	out[0].section, out[0].sectionIcon = "Pinned", "star"
	return append(out, tiles...)
}

// toolsHubFindFilter keeps the tools every word of query appears in, names
// that start with the first word first, then names that hold it, then the
// rest, each group in the page's order.
func toolsHubFindFilter(tiles, extra []toolsHubTile, query string) []toolsHubTile {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return tiles
	}
	type hit struct {
		t    toolsHubTile
		rank int
	}
	hits := make([]hit, 0, 16)
	consider := func(t toolsHubTile, section string) {
		label := strings.ToLower(t.label)
		hay := label + " " + strings.ToLower(t.value) + " " + strings.ToLower(section) + " " +
			toolsSearchWords(strings.TrimPrefix(t.id, toolsNetPrefix))
		for _, w := range words {
			if !strings.Contains(hay, w) {
				return
			}
		}
		rank := 2
		switch {
		case strings.HasPrefix(label, words[0]):
			rank = 0
		case strings.Contains(label, words[0]):
			rank = 1
		}
		hits = append(hits, hit{t: t, rank: rank})
	}
	section := ""
	for _, t := range tiles {
		if t.section != "" {
			section = t.section
		}
		consider(t, section)
	}
	for _, t := range extra {
		consider(t, "Network tools")
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].rank < hits[j].rank })
	out := make([]toolsHubTile, 0, len(hits))
	for _, h := range hits {
		t := h.t
		t.section, t.sectionIcon = "", ""
		t.wide = false
		t.pinned = toolsPinIndex(t.id) >= 0
		out = append(out, t)
	}
	if len(out) > 0 {
		out[0].section, out[0].sectionIcon = "Results for “"+query+"”", "search"
	}
	return out
}

// toolsHubFindHeader says on the page bar what the page is showing while a
// search filters it.
func toolsHubFindHeader(h *UIPageHeader, shown int) {
	query := toolsHubFind.applied
	if query == "" {
		return
	}
	h.Subtitle = pluralizeCount(shown, "tool", "tools") + " for “" + query + "”"
	if !toolsHubFind.typing {
		h.Subtitle += " — " + hintSentence("%s shows every tool", "B", "Clear search")
	}
	h.SubtitleColor = sdl.Color{}
}

// toolsHubFindDrawEmpty draws the page's body when a search found nothing.
func toolsHubFindDrawEmpty(renderer *sdl.Renderer, config *Config, header UIPageHeader, ip string) {
	fonts := toolsHubLoadFonts(config)
	frame := toolsHubMeasureFrame(config, header, fonts, ip)
	body := sdl.Rect{X: frame.contentX, Y: frame.bodyTop, W: frame.contentW, H: frame.bodyBot - frame.bodyTop}
	UIEmptyState(renderer, config, body, "@search", "No tool matches “"+toolsHubFind.applied+"”",
		"Try another word, like “wifi”, “storage” or “screen”.")
}

// toolsHubFindHints adds the hub's search and pin keys to its footer, and
// says B clears a search before it leaves.
func toolsHubFindHints(hints []FooterHint) []FooterHint {
	pin := "Pin"
	if c := toolsHub.page.cursor; c >= 0 && c < len(toolsHub.tiles) && toolsHub.tiles[c].pinned {
		pin = "Unpin"
	}
	if toolsHubFind.query != "" {
		for i := range hints {
			if hints[i].Button == "B" {
				hints[i].Label = "Clear search"
			}
		}
	}
	return append(hints, FooterHint{Button: "Y", Label: "Search"}, FooterHint{Button: "X", Label: pin})
}

// netToolsOpenAt opens the Network tools page focused on tool id (a search
// result or a pin from the hub).
func netToolsOpenAt(config *Config, id string) {
	netToolsOpen(config)
	for i, spec := range netToolsSpecs {
		if spec.id == id {
			netTools.page.cursor = i
			netTools.page.follow = true
			return
		}
	}
}

// toolsSearchWords are the words people use for a tool beyond its name and
// line: a Tools hub tile's id, or a Network tools tool's.
func toolsSearchWords(id string) string {
	if scene, ok := strings.CutPrefix(id, osAppsTilePrefix); ok {
		// The everyday apps carry their words in osAppSpecs.
		if s, found := osAppSpecFor(scene); found {
			return s.keys
		}
		switch scene {
		case "Calculator":
			return "calculator calc math sums"
		case "Alarm":
			return "alarm alarms wake up clock"
		}
		return ""
	}
	switch id {
	case "party":
		return "watch party together stream friends room movie"
	case "partymode":
		return "party mode game show quiz guess phones"
	case "localplay":
		return "local play multiplayer hotseat pass and play co-op couch players"
	case "nearby":
		return "nearby other jukahubs devices lan friends"
	case "ssync":
		return "save sync saves cloud across devices"
	case "mailcal":
		return "mail email calendar accounts"
	case "toolbox":
		return "toolbox command line tools install binaries"
	case "audit":
		return "audit security scan pentest nmap tls ssl"
	case "sectrust":
		return "security trust privacy signatures permissions"
	case "linuxpkg":
		return "linux packages apt ubuntu install programs"
	case "java":
		return "java jar jre jdk"
	case "nettools":
		return "network tools ftp sftp ssh telnet vnc smb ping dns traceroute whois port scan server ip"
	case "wifi":
		return "wifi wlan wireless network connect join password"
	case "netdrive":
		return "webdav network drive share map windows mac"
	case "hotspot":
		return "hotspot access point tether share wifi"
	case "netspeed":
		return "speed test bandwidth download upload ping latency"
	case "ctlmode":
		return "controller mode gamepad pc phone bluetooth"
	case "conncheck":
		return "connection check internet not working offline no internet diagnose troubleshoot router dns"
	case "terminal":
		return "terminal shell console command line bash"
	case "cmddeck":
		return "commands ready made shell scripts"
	case "sched":
		return "scheduled tasks cron timer startup automation"
	case "hardware":
		return "hardware cpu memory ram temperature monitor"
	case "disk":
		return "disk space storage free full card drive"
	case "spaceuse":
		return "space storage full card biggest largest folders files disk usage clean up free up"
	case "filecheck":
		return "verify checksum hash sha256 sha-256 sha1 md5 crc crc32 integrity file download"
	case "screentest":
		return "screen test display dead pixel stuck pixel backlight bleed colours colors banding ghosting lcd"
	case "qrmaker":
		return "qr code maker share phone link text"
	case "devcheck":
		return "device check buttons speaker headphones test"
	case "cardcheck":
		return "card check sd card health fake card read errors"
	case "usbotg":
		return "usb otg stick drive dac"
	case "usbpad":
		return "usb controller gamepad pc"
	case "battcal":
		return "battery calibration gauge"
	case "benchmark":
		return "benchmark speed storage read write"
	case "logs":
		return "logs export bug report diagnostics"
	case "cache":
		return "clear cache thumbnails"
	case "powertools":
		return "power tools files scripts sync"
	case "disc", "cuegen":
		return "disc cue sheet m3u playlist multi-disc"
	case "rommgr":
		return "rom manager bios duplicates"
	case "rommcloud":
		return "romm server"
	case "synth":
		return "synth music beats groovebox"
	case "pads":
		return "pads sampler drums sequencer"
	case "trophies":
		return "trophies achievements playtime stats"
	case "gbprinter":
		return "game boy printer scrapbook"
	case "xpack":
		return "experience packs themes"
	case "cfgdoc":
		return "config settings file diff"
	case "bkup":
		return "backup restore"
	case "sdmove":
		return "new sd card move copy migrate"
	case "updates":
		return "updates update patch upgrade rollback"
	case "bug":
		return "report bug feedback crash"
	case "community":
		return "community help support news"
	// The Network tools page's tools.
	case "ftp":
		return "ftp file transfer server share"
	case "ssh":
		return "ssh sftp secure file server"
	case "telnet":
		return "telnet shell remote"
	case "vnc":
		return "vnc remote screen control desktop"
	case "smb":
		return "smb samba windows share network folder"
	case "companion":
		return "phone companion remote qr pair"
	case "tailscale":
		return "tailscale vpn remote anywhere"
	case "gateway", "clipscan":
		return "port scan router host"
	case "ping":
		return "ping latency round trip"
	case "traceroute":
		return "traceroute route hops"
	case "dns":
		return "dns lookup name records mx"
	case "portcheck":
		return "port check open closed"
	case "neighbours":
		return "lan neighbours neighbors devices arp"
	case "wifisignal":
		return "wifi signal strength dbm"
	case "publicip":
		return "public ip address external"
	case "whois":
		return "whois domain registrar"
	case "clock":
		return "clock time ntp sync"
	case "subnet":
		return "subnet mask cidr network range"
	case "listening":
		return "listening open ports sockets"
	case "codec":
		return "base64 hex url encode decode"
	}
	return ""
}
