package main

// steam_ports.go — Steam games that run on the handheld itself.
//
// A TrimUI cannot run Steam, but PortMaster has native ports of many games
// sold there (FNA and XNA games, GameMaker, Godot and LÖVE games, source
// ports): the port is the engine, the game's data is the player's own copy.
// This file joins the two:
//
//   - Matching. PortMaster's catalogue (mcPortsCatalog) lists the shops that
//     sell the game a port needs (attr.store, mcCatalogEntry.Stores); a Steam
//     store link there names the app id. Without one, a port whose title
//     equals the game's (case, punctuation and a "(…)" suffix aside, a year
//     in brackets kept) matches — unless its links name other Steam apps, its
//     name is one Steam also sells as a remake (steamPortTitleOnlyDenied), or
//     two games of the library would take it. Matches that may run on this
//     device (portCatalogMayRun) make the library's Handheld tab.
//   - State. Whether the port is installed (its launcher in a Ports folder),
//     where it wants the game's files (the script's GAMEDIR and its gamedata,
//     data, assets or game folder, found the way gamediag.go finds them) and
//     whether they are there yet.
//   - Copying. A paired JukaHub PC with the game installed in Steam serves
//     the game's folder: GET /peer/v1/steam/files?appid=N lists it and
//     GET /peer/v1/steam/file?appid=N&path=REL sends one file. The handheld
//     copies it into the port's folder through os.Root, keeping a record
//     (.jukahub-steam-copy.json) of what it wrote: a copy that stops shows as
//     unfinished and resumes, and a file the port itself shipped is never
//     replaced. Only paired devices reach the endpoints (peerGuard), and only
//     regular files inside an installed Steam game's own folder are served —
//     never through a link or a Windows junction, never Steam's settings.
//
// The catalogue entries that matched are kept in data/steam/ports.json, so
// the tab still works offline after one look.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	steamPeerFilesPath = "/peer/v1/steam/files"
	steamPeerFilePath  = "/peer/v1/steam/file"
	// steamPortCopyMaxFiles and steamPortCopyMaxBytes bound one game's copy;
	// the ports PortMaster has are well under both.
	steamPortCopyMaxFiles = 100000
	steamPortCopyMaxBytes = int64(64) << 30
	// steamPortListMaxDirs and steamPortListMaxPathBytes bound the walk of a
	// game's folder and keep its list under steamPortListLimit.
	steamPortListMaxDirs      = 50000
	steamPortListMaxPathBytes = 32 << 20
	// steamPortListLimit bounds the file list one PC sends.
	steamPortListLimit = 48 << 20
	// steamPortFileSlack is added per copied file to the space a copy needs:
	// an exFAT card rounds every file up to a cluster (often 128 KB).
	steamPortFileSlack = 64 << 10
	// steamPortsRescanEvery is how often an open panel looks at the Ports
	// folders again (a port PortMaster just installed, files copied by hand).
	steamPortsRescanEvery = 5 * time.Second
	steamPortsFileVersion = 1
	// steamCopyManifestName records, in the folder a copy writes to, what it
	// wrote and whether it finished.
	steamCopyManifestName = ".jukahub-steam-copy.json"
)

// ── Matching ─────────────────────────────────────────────────────────────

var (
	steamStoreAppRe  = regexp.MustCompile(`(?i)(?:store\.steampowered\.com|steamcommunity\.com)/app/([0-9]{1,10})`)
	steamPortYearRe  = regexp.MustCompile(`[(\[]\s*((?:19|20)[0-9]{2})\s*[)\]]`)
	steamPortDenyKey = map[string]bool{
		// Names Steam sells for an old game and its remake or reboot alike: a
		// port of one must name its Steam app to match.
		"doom": true, "quake": true, "tombraider": true, "thief": true, "shadowwarrior": true,
		"wolfenstein": true, "prey": true, "syndicate": true, "turok": true, "hitman": true,
		"xcom": true, "systemshock": true, "bloodrayne": true, "outrun": true, "flashback": true,
	}
)

// steamPortStoreLinks reads a catalogue entry's attr.store: shop links as
// plain strings or as objects with the link in one of their fields. Only
// strings that look like links are kept (at most eight, in a stable order),
// so shop names do not crowd a Steam link out.
func steamPortStoreLinks(v interface{}) []string {
	var out []string
	var walk func(v interface{}, depth int)
	walk = func(v interface{}, depth int) {
		if depth > 3 || len(out) >= 8 {
			return
		}
		switch t := v.(type) {
		case string:
			s := strings.TrimSpace(t)
			lower := strings.ToLower(s)
			if s != "" && len(s) <= 512 && (strings.Contains(s, "://") ||
				strings.Contains(lower, "steampowered.com") || strings.Contains(lower, "steamcommunity.com")) {
				out = append(out, s)
			}
		case []interface{}:
			for _, child := range t {
				walk(child, depth+1)
			}
		case map[string]interface{}:
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(t[k], depth+1)
			}
		}
	}
	walk(v, 0)
	return out
}

// steamPortStoreApps is every Steam app id e's shop links name.
func steamPortStoreApps(e mcCatalogEntry) []int {
	var ids []int
	for _, link := range e.Stores {
		for _, m := range steamStoreAppRe.FindAllStringSubmatch(link, 4) {
			id, err := strconv.Atoi(m[1])
			if err != nil || id <= 0 || id > 1<<31-1 {
				continue
			}
			dup := false
			for _, have := range ids {
				dup = dup || have == id
			}
			if !dup {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// steamPortNorm is a title reduced for comparing: lower case, a trailing
// "(…)" or "[…]" dropped ("Celeste (FNA)", "DOOM (1993)"), "&" read as
// "and", a leading "The" dropped, and only letters and digits kept.
func steamPortNorm(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for i := 0; i < 3; i++ {
		t := strings.TrimSpace(s)
		open := ""
		switch {
		case strings.HasSuffix(t, ")"):
			open = "("
		case strings.HasSuffix(t, "]"):
			open = "["
		}
		j := strings.LastIndex(t, open)
		if open == "" || j <= 0 {
			break
		}
		s = t[:j]
	}
	s = strings.ReplaceAll(s, "&", " and ")
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	words := strings.Fields(b.String())
	if len(words) > 1 && words[0] == "the" {
		words = words[1:]
	}
	return strings.Join(words, "")
}

// steamPortKey is the title-matching key: steamPortNorm plus a year given
// in brackets, so "DOOM (1993)" and "DOOM" stay apart.
func steamPortKey(s string) string {
	key := steamPortNorm(s)
	if m := steamPortYearRe.FindStringSubmatch(s); m != nil {
		key += m[1]
	}
	return key
}

// steamPortTitleOnlyDenied reports whether a port titled title may only
// match through a Steam store link.
func steamPortTitleOnlyDenied(title string) bool {
	return steamPortDenyKey[steamPortNorm(title)]
}

// steamPortIndex finds catalogue entries by Steam app id and by title.
type steamPortIndex struct {
	entries []mcCatalogEntry
	byApp   map[int][]int  // app id → entries whose store links name it
	byTitle map[string]int // title key → entry; -1 when two share it
}

func steamBuildPortIndex(entries []mcCatalogEntry) steamPortIndex {
	idx := steamPortIndex{entries: entries, byApp: map[int][]int{}, byTitle: map[string]int{}}
	for i, e := range entries {
		apps := steamPortStoreApps(e)
		for _, id := range apps {
			idx.byApp[id] = append(idx.byApp[id], i)
		}
		if len(apps) > 0 || steamPortTitleOnlyDenied(e.Title) {
			// The port says which Steam game it is, or its name is too
			// common to guess by: no matching by title.
			continue
		}
		t := steamPortKey(e.Title)
		if len([]rune(t)) < 3 {
			continue
		}
		if _, dup := idx.byTitle[t]; dup {
			idx.byTitle[t] = -1
			continue
		}
		idx.byTitle[t] = i
	}
	return idx
}

// find is the port for a Steam game: one its store links name (the first
// that may run here when there are several), else one with its exact title.
func (idx steamPortIndex) find(appid int, name string, mayRun func(mcCatalogEntry) bool) (mcCatalogEntry, bool, bool) {
	if list := idx.byApp[appid]; len(list) > 0 {
		for _, i := range list {
			if mayRun(idx.entries[i]) {
				return idx.entries[i], true, true
			}
		}
		return idx.entries[list[0]], true, true
	}
	if i, ok := idx.byTitle[steamPortKey(name)]; ok && i >= 0 {
		return idx.entries[i], false, true
	}
	return mcCatalogEntry{}, false, false
}

// ── State on this handheld ───────────────────────────────────────────────

// steamPortState is what the handheld knows about a Steam game's port.
type steamPortState struct {
	Entry   mcCatalogEntry
	ByStore bool   // matched by a Steam store link, not only by title
	MayRun  bool   // the port's own metadata allows this device
	Script  string // its launcher once installed
	// DataDir is where the game's own files go: the port's data folder
	// (DataKnown) or, when it ships none, the port's folder itself.
	DataDir   string
	DataKnown bool
	HasData   bool // the game's files are there (see steamPortDataDir)
	Copied    bool // a copy from a PC finished into DataDir
	Partial   bool // a copy from a PC started there and did not finish
	Queued    bool // the zip waits in PortMaster's autoinstall folder
}

func (s steamPortState) installed() bool { return s.Script != "" }

// needsData reports whether the port is installed but still waits for the
// game's own files (or for a copy that stopped to finish).
func (s steamPortState) needsData() bool {
	return s.Script != "" && !s.Entry.RTR && (s.Partial || (s.DataKnown && !s.HasData))
}

// ready reports whether the port can be started as it is.
func (s steamPortState) ready() bool { return s.Script != "" && !s.needsData() }

// same reports whether two scans of one game found the same thing.
func (s steamPortState) same(o steamPortState) bool {
	return s.Entry.Zip == o.Entry.Zip && s.ByStore == o.ByStore && s.MayRun == o.MayRun && s.Script == o.Script &&
		s.DataDir == o.DataDir && s.DataKnown == o.DataKnown && s.HasData == o.HasData && s.Copied == o.Copied &&
		s.Partial == o.Partial && s.Queued == o.Queued
}

func (s steamPortState) title() string {
	if s.Entry.Title != "" {
		return s.Entry.Title
	}
	return strings.TrimSuffix(s.Entry.Zip, filepath.Ext(s.Entry.Zip))
}

// steamPortsAvailable reports whether ports are a way to play here: a Linux
// handheld (or a card with PortMaster) without the Steam app.
func steamPortsAvailable() bool {
	if runtime.GOOS != "linux" || steamPanel.local {
		return false
	}
	return DetectDevice().IsTrimui() || portMasterInstalled()
}

// steamPortGame is the part of a library game the scan needs.
type steamPortGame struct {
	appid int
	name  string
}

var steamPortGlibc struct {
	once  sync.Once
	value string
}

func steamPortHostGlibc() string {
	steamPortGlibc.once.Do(func() { steamPortGlibc.value = portCatalogHostGlibc() })
	return steamPortGlibc.value
}

// steamPortsScan matches games against the catalogue and reads each match's
// state from the card. Any goroutine; it reads folders.
func steamPortsScan(games []steamPortGame, idx steamPortIndex) map[int]steamPortState {
	out := map[int]steamPortState{}
	if len(idx.entries) == 0 {
		return out
	}
	device, glibc := DetectDevice(), steamPortHostGlibc()
	mayRun := func(e mcCatalogEntry) bool { return portCatalogMayRun(e, device, glibc) }
	type match struct {
		entry   mcCatalogEntry
		byStore bool
	}
	matches := map[int]match{}
	byTitle := map[string]int{} // a port matched by title → how many games took it
	for _, g := range games {
		if g.appid <= 0 || steamIsShortcut(g.appid) {
			continue
		}
		if _, dup := matches[g.appid]; dup {
			continue
		}
		e, byStore, ok := idx.find(g.appid, g.name, mayRun)
		if !ok {
			continue
		}
		matches[g.appid] = match{entry: e, byStore: byStore}
		if !byStore {
			byTitle[e.Zip]++
		}
	}
	scripts := steamPortLaunchers()
	inbox := mcAutoinstallDir(portMasterLauncher(portMasterSDRoot))
	for appid, m := range matches {
		if !m.byStore && byTitle[m.entry.Zip] > 1 {
			// Two games of the library share the port's name: neither is
			// sure to be the one it is for.
			continue
		}
		st := steamPortState{Entry: m.entry, ByStore: m.byStore, MayRun: mayRun(m.entry)}
		st.Script = steamPortScript(m.entry, scripts)
		switch {
		case st.Script != "":
			steamPortReadData(&st)
		case inbox != "" && m.entry.Zip != "" && regularFile(filepath.Join(inbox, m.entry.Zip)):
			st.Queued = true
		}
		out[appid] = st
	}
	return out
}

// steamPortLaunchers lists the launcher scripts in the Ports folders by
// lower-case name. A light version of scanPortLaunchers (no manifest reads),
// as it runs every few seconds while the panel is open.
func steamPortLaunchers() map[string]string {
	out := map[string]string{}
	for _, root := range portRoots(portMasterSDRoot) {
		items, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, it := range items {
			name := it.Name()
			if it.IsDir() || strings.HasPrefix(name, ".") || !strings.EqualFold(filepath.Ext(name), ".sh") ||
				strings.EqualFold(name, "PortMaster.sh") {
				continue
			}
			key := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(name, filepath.Ext(name))))
			if key == "" || out[key] != "" {
				continue
			}
			if p := filepath.Join(root, name); regularFile(p) {
				out[key] = p
			}
		}
	}
	return out
}

// steamPortScript is e's installed launcher among scripts (lower-case name
// → path), or "": the zip's own name, then the scripts the port installs.
func steamPortScript(e mcCatalogEntry, scripts map[string]string) string {
	if p := scripts[strings.ToLower(strings.TrimSuffix(e.Zip, filepath.Ext(e.Zip)))]; p != "" {
		return p
	}
	for _, item := range e.Items {
		if !strings.EqualFold(filepath.Ext(item), ".sh") {
			continue
		}
		base := filepath.Base(filepath.FromSlash(item))
		if p := scripts[strings.ToLower(strings.TrimSuffix(base, filepath.Ext(base)))]; p != "" {
			return p
		}
	}
	return ""
}

// steamPortReadData fills in where an installed port wants the game's files
// and whether they are there: its data folder when it ships one, else one
// its instructions name, else the port's own folder. A copy record there
// decides (finished or not); without one, any game file in a data folder
// counts.
func steamPortReadData(st *steamPortState) {
	portDir, _ := gameDiagPortDir(st.Script)
	if portDir == "" {
		return
	}
	st.DataDir = portDir
	for _, sub := range []string{"gamedata", "data", "assets", "game"} {
		p, ok := gameDiagFileIn(portDir, sub)
		if !ok {
			continue
		}
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			st.DataDir, st.DataKnown = p, true
			break
		}
	}
	if !st.DataKnown {
		lower := strings.ToLower(st.Entry.Instructions)
		for _, sub := range []string{"gamedata", "assets"} {
			if strings.Contains(lower, sub) {
				st.DataDir, st.DataKnown = filepath.Join(portDir, sub), true
				break
			}
		}
	}
	if man, ok := steamReadCopyManifest(st.DataDir); ok {
		st.Copied, st.Partial = man.Complete, !man.Complete
		st.HasData = man.Complete
		return
	}
	if st.DataKnown && dirExists(st.DataDir) {
		st.HasData = gameDiagDirHasGameFiles(st.DataDir, 2)
	}
}

// ── The copy record ──────────────────────────────────────────────────────

// steamCopyManifest is .jukahub-steam-copy.json: which files a copy from a
// PC wrote (path → size) and whether it finished.
type steamCopyManifest struct {
	Version  int              `json:"version"`
	AppID    int              `json:"appid"`
	Complete bool             `json:"complete"`
	Files    map[string]int64 `json:"files"`
}

func steamReadCopyManifest(dir string) (steamCopyManifest, bool) {
	var man steamCopyManifest
	if dir == "" {
		return man, false
	}
	data, err := steamReadSmall(filepath.Join(dir, steamCopyManifestName), 32<<20)
	if err != nil || json.Unmarshal(data, &man) != nil || man.Version != 1 {
		return steamCopyManifest{}, false
	}
	if man.Files == nil {
		man.Files = map[string]int64{}
	}
	return man, true
}

func steamWriteCopyManifest(dir string, man steamCopyManifest) error {
	man.Version = 1
	data, err := json.Marshal(man)
	if err != nil {
		return err
	}
	return AtomicWrite(filepath.Join(dir, steamCopyManifestName), append(data, '\n'), 0o644)
}

// ── The catalogue, kept for offline ──────────────────────────────────────

func steamPortsPath() string { return filepath.Join(steamDataDir(), "ports.json") }

type steamPortsFile struct {
	Version int              `json:"version"`
	Entries []mcCatalogEntry `json:"entries"`
}

// steamPortsLoadCached is the matched entries of the last look, or nil.
func steamPortsLoadCached() []mcCatalogEntry {
	var f steamPortsFile
	if _, err := readJSONWithBackup(steamPortsPath(), &f); err != nil || f.Version != steamPortsFileVersion {
		return nil
	}
	out := f.Entries[:0]
	for _, e := range f.Entries {
		// Written by this file, but a card can be edited: the live
		// catalogue's rules (mcCatalogFromObject) apply.
		if !strings.HasSuffix(strings.ToLower(e.Zip), ".zip") || strings.ContainsAny(e.Zip, `/\`) ||
			strings.HasPrefix(e.Zip, ".") || !strings.HasPrefix(e.URL, "https://") {
			continue
		}
		if len(e.MD5) != 32 {
			e.MD5 = ""
		}
		out = append(out, e)
		if len(out) >= 2000 {
			break
		}
	}
	return out
}

// steamPortsSaveMatched keeps the entries that matched the library.
func steamPortsSaveMatched(found map[int]steamPortState) {
	seen := map[string]bool{}
	var entries []mcCatalogEntry
	for _, st := range found {
		if seen[st.Entry.Zip] {
			continue
		}
		seen[st.Entry.Zip] = true
		entries = append(entries, st.Entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Zip < entries[j].Zip })
	data, err := json.Marshal(steamPortsFile{Version: steamPortsFileVersion, Entries: entries})
	if err != nil {
		return
	}
	steamStateMu.Lock()
	defer steamStateMu.Unlock()
	if err := os.MkdirAll(steamDataDir(), 0o700); err != nil {
		return
	}
	if err := AtomicWrite(steamPortsPath(), append(data, '\n'), 0o600); err != nil {
		logWarn("steam", "saving ports.json failed: %v", err)
	}
}

// steamPortsCatalog is the catalogue the scans use: the live one once read
// (again after mcCatalogTTL), else the matched entries kept on the card.
var steamPortsCatalog struct {
	mu     sync.Mutex
	idx    steamPortIndex
	liveAt time.Time // zero until the live catalogue was read
}

// ── The panel's side ─────────────────────────────────────────────────────

// steamPortsGames copies what the scan needs out of the panel's list.
func steamPortsGames() []steamPortGame {
	out := make([]steamPortGame, 0, len(steamPanel.games))
	for _, g := range steamPanel.games {
		out = append(out, steamPortGame{appid: g.AppID, name: g.Name})
	}
	return out
}

// steamPortsRefresh scans again in the background; fetch also reads the live
// catalogue when it was not read yet (tried at most once a minute; the
// catalogue itself is cached for hours by mcPortsCatalog). Main thread.
func steamPortsRefresh(fetch bool) {
	if !steamPanel.open || steamPanel.portsBusy {
		return
	}
	steamPanel.portsScanAt = time.Now()
	if !steamPortsAvailable() {
		return
	}
	if fetch {
		fetch = time.Since(steamPanel.portsFetchAt) >= time.Minute
	}
	if fetch {
		steamPanel.portsFetchAt = time.Now()
	}
	steamPanel.portsBusy = true
	steamGenSeq++
	steamPanel.portsGen = steamGenSeq
	gen := steamPanel.portsGen
	games := steamPortsGames()
	go func() {
		steamPortsCatalog.mu.Lock()
		idx, liveAt := steamPortsCatalog.idx, steamPortsCatalog.liveAt
		steamPortsCatalog.mu.Unlock()
		if len(idx.entries) == 0 {
			// Offline so far: the entries the last look kept, read once.
			idx = steamBuildPortIndex(steamPortsLoadCached())
			if len(idx.entries) > 0 {
				steamPortsCatalog.mu.Lock()
				if len(steamPortsCatalog.idx.entries) == 0 {
					steamPortsCatalog.idx = idx
				}
				steamPortsCatalog.mu.Unlock()
			}
		}
		if len(idx.entries) > 0 {
			// Nothing to match with yet keeps "Looking for ports…" up.
			first := steamPortsScan(games, idx)
			tbxPost(func() { steamPortsApply(gen, first, false) })
		}
		live := !liveAt.IsZero() && time.Since(liveAt) < mcCatalogTTL
		if !fetch || live || !IsOnline() {
			tbxPost(func() { steamPortsApply(gen, nil, true) })
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		entries, err := mcPortsCatalog(ctx)
		cancel()
		if err != nil {
			logDebug("steam", "PortMaster's catalogue: %v", err)
			tbxPost(func() { steamPortsApply(gen, nil, true) })
			return
		}
		fresh := steamBuildPortIndex(entries)
		steamPortsCatalog.mu.Lock()
		steamPortsCatalog.idx, steamPortsCatalog.liveAt = fresh, time.Now()
		steamPortsCatalog.mu.Unlock()
		found := steamPortsScan(games, fresh)
		steamPortsSaveMatched(found)
		logDebug("steam", "%d of %d Steam games have a PortMaster port", len(found), len(games))
		tbxPost(func() { steamPortsApply(gen, found, true) })
	}()
}

// steamPortsSame reports whether two scans found the same ports.
func steamPortsSame(a, b map[int]steamPortState) bool {
	if a == nil || len(a) != len(b) {
		return false
	}
	for id, st := range a {
		o, ok := b[id]
		if !ok || !st.same(o) {
			return false
		}
	}
	return true
}

// steamPortsApply takes a scan on the main loop; done ends the run (found
// may then be nil: nothing new).
func steamPortsApply(gen int, found map[int]steamPortState, done bool) {
	if !steamPanel.open || steamPanel.portsGen != gen {
		return
	}
	if found != nil && !steamPortsSame(steamPanel.ports, found) {
		// The Handheld and Installed tabs list by it: rebuild the view,
		// keeping the focused game.
		focused := steamPanelFocusedID()
		steamPanel.ports = found
		steamPanel.dirty = true
		steamPanelKeepFocus(focused)
	}
	if done {
		if steamPanel.ports == nil {
			// Looked, nothing found: the tab says so rather than "Looking…".
			steamPanel.ports = map[int]steamPortState{}
		}
		steamPanel.portsBusy = false
		steamPanel.portsScanAt = time.Now()
	}
}

// steamPortsTick rescans now and then while the panel is open, so a port
// PortMaster installed or files copied by hand show up. Main loop.
func steamPortsTick() {
	if steamPanel.portsBusy || time.Since(steamPanel.portsScanAt) < steamPortsRescanEvery {
		return
	}
	steamPortsRefresh(true)
}

// steamPortFor is g's port state, when PortMaster has one that may run here.
func steamPortFor(g steamGame) (steamPortState, bool) {
	if steamPanel.ports == nil {
		return steamPortState{}, false
	}
	st, ok := steamPanel.ports[g.AppID]
	if !ok || !st.MayRun {
		return steamPortState{}, false
	}
	return st, true
}

// steamPortReadyHere reports whether g plays on this handheld through its
// port as things are (the Installed tab counts it).
func steamPortReadyHere(g steamGame) bool {
	st, ok := steamPortFor(g)
	return ok && st.ready()
}

// steamPortsCount is how many of the library's games have a port that may
// run here. Main thread.
func steamPortsCount() int {
	n := 0
	for _, st := range steamPanel.ports {
		if st.MayRun {
			n++
		}
	}
	return n
}

// steamPortSource is a paired JukaHub PC that has g installed in Steam (the
// only kind that can send its files): the game's own remote first.
func steamPortSource(g steamGame) (steamRemotePC, bool) {
	var best steamRemotePC
	found := false
	for _, pc := range steamPanel.remote.pcs {
		if pc.kind != steamRemotePeer {
			continue
		}
		for _, a := range pc.apps {
			if a.appid != g.AppID || !a.installed || (a.dlTotal > 0 && a.dlDone < a.dlTotal) {
				continue
			}
			if !found || (g.Remote.kind == steamRemotePeer && g.Remote.id == pc.id) {
				best, found = pc, true
			}
		}
	}
	return best, found
}

// steamPortLabel is the port's state in a few words for a row's detail line,
// "" when the game has none.
func steamPortLabel(g steamGame) string {
	st, ok := steamPortFor(g)
	if !ok {
		return ""
	}
	switch {
	case st.ready():
		return "Runs here"
	case st.Partial:
		return "Copy not finished"
	case st.needsData():
		return "Port needs game files"
	case st.Queued:
		return "Port waiting in PortMaster"
	}
	return "PortMaster port available"
}

// steamPortSheetLine is the port's state as the game's sheet says it.
func steamPortSheetLine(g steamGame) string {
	st, ok := steamPortFor(g)
	if !ok {
		return ""
	}
	name := "PortMaster port \"" + st.title() + "\""
	if !st.ByStore {
		name += " (matched by name)"
	}
	switch {
	case st.ready() && st.Entry.RTR:
		return name + " · installed, ready to run"
	case st.ready():
		return name + " · installed"
	case st.Partial:
		return name + " · installed, the copy of the game's files did not finish"
	case st.needsData():
		return name + " · installed, needs the game's files in " + filepath.Base(st.DataDir)
	case st.Queued:
		return name + " · waiting in PortMaster"
	case st.Entry.RTR:
		return name + " · free to install, ready to run"
	}
	return name + " · uses your own copy of the game"
}

// steamPortPrimaryLabel is what A does through g's port, "" when A plays
// through Steam instead: the port once it is ready, and on the Handheld tab
// the port's next step.
func steamPortPrimaryLabel(g steamGame, st steamPortState) string {
	if done, total, copying := steamPortCopyProgress(g.AppID); copying {
		return "Copying " + strconv.Itoa(steamPercent(done, total)) + "%"
	}
	if st.ready() {
		return "Play here"
	}
	if steamPanel.tab != steamTabHandheld {
		return ""
	}
	switch {
	case st.Queued:
		return "Finish in PortMaster"
	case !st.installed():
		return "Install port"
	}
	if pc, ok := steamPortSource(g); ok {
		return "Copy files from " + pc.name
	}
	return "Needs game files"
}

// steamPortPrimary does what steamPortPrimaryLabel says; false when A plays
// through Steam instead.
func steamPortPrimary(g steamGame, st steamPortState) bool {
	if _, _, copying := steamPortCopyProgress(g.AppID); copying {
		ShowToast("Still copying "+g.Name+" · the card shows the progress", ToastKindInfo)
		return true
	}
	if !st.ready() && steamPanel.tab != steamTabHandheld {
		return false
	}
	steamPortNextStep(g, st)
	return true
}

// steamPortNextStep moves g's port one step on: play it when it is ready,
// finish a queued install in PortMaster, install it, or copy the game's
// files from a paired PC.
func steamPortNextStep(g steamGame, st steamPortState) {
	switch {
	case st.ready():
		steamPortPlay(g, st)
	case st.Queued:
		launcher := portMasterLauncher(portMasterSDRoot)
		if launcher == "" {
			ShowToast("PortMaster is not installed", ToastKindWarning)
			return
		}
		launchPortScript(portMasterAppName, launcher)
		steamPanel.portsScanAt = time.Time{}
	case !st.installed():
		steamPortInstall(g, st)
	default:
		pc, ok := steamPortSource(g)
		if !ok {
			ShowToast(steamPortNoSourceText(g), ToastKindInfo)
			return
		}
		steamPortCopyFrom(g, st, pc)
	}
}

// steamPortPlay starts g's installed port.
func steamPortPlay(g steamGame, st steamPortState) {
	if !st.installed() {
		return
	}
	if st.needsData() {
		ShowToast(g.Name+" needs its game files first · "+hintKeyFor("Y", "Options")+" → Copy game files", ToastKindInfo)
		return
	}
	launchPortScript(st.title(), st.Script)
	steamPanelUpdateGame(g.AppID, func(x *steamGame) { x.LastPlayed = time.Now().Unix() })
	steamPanel.portsScanAt = time.Time{}
}

// steamPortInstall downloads g's port into PortMaster (which then installs
// it and the runtimes it names).
func steamPortInstall(g steamGame, st steamPortState) {
	if st.installed() {
		return
	}
	if err := portCatalogInstallStart(st.Entry); err != nil {
		ShowToast("Could not install the port: "+firstLine(err.Error()), ToastKindError)
		return
	}
	PlayActivateSound()
	steamPanel.portsScanAt = time.Time{}
}

// steamPortCopyFrom starts copying g's files from pc into the port.
func steamPortCopyFrom(g steamGame, st steamPortState, pc steamRemotePC) {
	if !st.installed() || st.DataDir == "" {
		ShowToast("Install the port first", ToastKindInfo)
		return
	}
	gen := steamPanel.gen
	err := steamPortCopyStart(g, pc.id, pc.name, st, func(skipped int, err error) {
		switch {
		case err == nil && skipped > 0:
			PlaySuccessSound()
			ShowToast(g.Name+" is ready on this handheld · "+pluralizeCount(skipped, "file", "files")+
				" the card cannot hold were left out", ToastKindSuccess)
		case err == nil:
			PlaySuccessSound()
			ShowToast(g.Name+" is ready on this handheld · press "+hintKeyFor("A", "Play")+" to play", ToastKindSuccess)
		case !errors.Is(err, context.Canceled):
			ShowToast("Copying "+g.Name+" stopped — "+firstLine(peerErrorText(err, pc.name)), ToastKindError)
		}
		if steamPanel.open && steamPanel.gen == gen {
			steamPanel.portsScanAt = time.Time{}
		}
	})
	if err != nil {
		ShowToast(firstLine(err.Error()), ToastKindWarning)
		return
	}
	PlayActivateSound()
	ShowToast("Copying "+g.Name+" from "+pc.name+" · the card shows the progress", ToastKindInfo)
}

// steamPortNoSourceText says how to get g's files when no paired PC has it.
func steamPortNoSourceText(g steamGame) string {
	if g.Remote.kind == steamRemoteSteam && g.Remote.installed {
		return "Copying needs JukaHub on " + g.Remote.where + " · install it there and pair it under Devices"
	}
	return "Install " + g.Name + " in Steam on a paired JukaHub PC first, or copy its files to the port's folder by hand"
}

// ── Copying (the handheld's side) ────────────────────────────────────────

var steamPortCopy struct {
	mu    sync.Mutex
	busy  bool
	appid int
	done  int64
	total int64
}

// steamPortCopyProgress is the copy of appid in progress, if any.
func steamPortCopyProgress(appid int) (done, total int64, ok bool) {
	steamPortCopy.mu.Lock()
	defer steamPortCopy.mu.Unlock()
	if !steamPortCopy.busy || steamPortCopy.appid != appid {
		return 0, 0, false
	}
	return steamPortCopy.done, steamPortCopy.total, true
}

func steamPortCopyNote(done, total int64) {
	steamPortCopy.mu.Lock()
	steamPortCopy.done, steamPortCopy.total = done, total
	steamPortCopy.mu.Unlock()
}

// steamPortCopyStart copies g's folder from a paired PC into st's data
// folder. finished runs on the main thread with how many of the PC's files
// were left out. Only one copy runs, and none while another download holds
// the install card.
func steamPortCopyStart(g steamGame, peerID, peerName string, st steamPortState, finished func(skipped int, err error)) error {
	if st.DataDir == "" {
		return errors.New("the port's folder was not found")
	}
	if _, busy := mcCopyBusy(); busy || installBarActive() {
		return errors.New("another download is in progress")
	}
	steamPortCopy.mu.Lock()
	if steamPortCopy.busy {
		steamPortCopy.mu.Unlock()
		return errors.New("a copy is already running")
	}
	steamPortCopy.busy, steamPortCopy.appid, steamPortCopy.done, steamPortCopy.total = true, g.AppID, 0, 0
	steamPortCopy.mu.Unlock()
	gen, card := installBarBeginRun(g.Name + " from " + peerName)
	ctx, cancel := mcRunContext(gen, card)
	dst := st.DataDir
	go func() {
		defer cancel()
		skipped, err := steamPortCopyRun(ctx, gen, peerID, g.AppID, dst)
		switch {
		case err == nil:
			logInfo("steam", "copied %s (%d) from %s into %s", g.Name, g.AppID, steamCleanText(peerName, 40), dst)
			installBarDoneRun(gen)
		case ctx.Err() != nil:
			installBarAbortRun(gen)
			err = context.Canceled
		default:
			logWarn("steam", "copying %d from %s: %v", g.AppID, steamCleanText(peerName, 40), err)
			installBarFailRun(gen, firstLine(peerErrorText(err, peerName)))
		}
		steamPortCopy.mu.Lock()
		steamPortCopy.busy = false
		steamPortCopy.mu.Unlock()
		tbxPost(func() {
			if finished != nil {
				finished(skipped, err)
			}
		})
	}()
	return nil
}

// errSteamPCTooOld is a PC whose JukaHub predates the file copy.
var errSteamPCTooOld = errors.New("update JukaHub on the PC: its version cannot send game files yet")

// steamPortCopyRun lists the game's files on the PC, checks the card's free
// space and copies what is missing into dst. A file already there at the
// right size is kept; one of the wrong size is fetched again only when an
// earlier copy wrote it (the copy record says so) — any other file is the
// port's own and stays. The record is marked unfinished before the first
// file and finished after the last.
func steamPortCopyRun(ctx context.Context, gen uint64, peerID string, appid int, dst string) (int, error) {
	installBarUpdateRun(gen, installPhaseResolving, 0, 0)
	lctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	var list steamPeerFiles
	err := peerDoJSON(lctx, peerID, http.MethodGet, steamPeerFilesPath+"?appid="+strconv.Itoa(appid), nil, &list, steamPortListLimit)
	cancel()
	var se *peerStatusError
	if errors.As(err, &se) && se.code == http.StatusNotFound && strings.Contains(se.msg, "404 page not found") {
		return 0, errSteamPCTooOld // Go's mux answer: no such call there
	}
	if err != nil {
		return 0, err
	}
	if len(list.Files) == 0 {
		return list.Skipped, errors.New("the PC listed no files for this game")
	}
	if len(list.Files) > steamPortCopyMaxFiles {
		return list.Skipped, errors.New("the PC listed too many files")
	}
	var total int64
	for _, f := range list.Files {
		if !steamPeerRelOK(f.Path) || f.Size < 0 || f.Size > steamPortCopyMaxBytes {
			return list.Skipped, fmt.Errorf("the PC listed a file JukaHub will not write: %q", f.Path)
		}
		total += f.Size
	}
	if total > steamPortCopyMaxBytes {
		return list.Skipped, errors.New("this game is larger than a handheld copy allows")
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return list.Skipped, fmt.Errorf("cannot create %s: %w", dst, err)
	}
	// Every write goes through root: no path from the PC, and no link in the
	// port's folder, can reach outside dst.
	root, err := os.OpenRoot(dst)
	if err != nil {
		return list.Skipped, err
	}
	defer root.Close()
	man, ok := steamReadCopyManifest(dst)
	if !ok || man.AppID != appid {
		man = steamCopyManifest{AppID: appid, Files: map[string]int64{}}
	}
	// What to fetch: missing files, and wrong-sized ones an earlier copy wrote.
	fetch := make([]bool, len(list.Files))
	var need int64
	for i, f := range list.Files {
		info, err := root.Lstat(filepath.FromSlash(f.Path))
		switch {
		case err != nil:
			fetch[i] = true
		case info.Mode().IsRegular() && info.Size() != f.Size:
			_, wrote := man.Files[f.Path]
			fetch[i] = wrote
		}
		if fetch[i] {
			need += f.Size + steamPortFileSlack
		}
	}
	if free, err := freeBytes(dst); err == nil && free < need+64<<20 {
		return list.Skipped, fmt.Errorf("needs %s free on the card, %s free", humanBytes(need+64<<20), humanBytes(free))
	}
	man.Complete = false
	if err := steamWriteCopyManifest(dst, man); err != nil {
		return list.Skipped, err
	}
	var done int64
	steamPortCopyNote(0, total)
	installBarUpdateRun(gen, installPhaseDownload, 0, total)
	have := make(map[string]int64, len(list.Files))
	for i, f := range list.Files {
		if err := ctx.Err(); err != nil {
			return list.Skipped, err
		}
		rel := filepath.FromSlash(f.Path)
		if !fetch[i] {
			if info, err := root.Lstat(rel); err == nil && info.Mode().IsRegular() && info.Size() == f.Size {
				have[f.Path] = f.Size
			}
			done += f.Size
			steamPortCopyNote(done, total)
			installBarUpdateRun(gen, installPhaseDownload, done, total)
			continue
		}
		base := done
		progress := func(n int64) {
			steamPortCopyNote(base+n, total)
			installBarUpdateRun(gen, installPhaseDownload, base+n, total)
		}
		if err := steamPortFetchFile(ctx, root, peerID, appid, f, progress); err != nil {
			return list.Skipped, fmt.Errorf("%s: %w", path.Base(f.Path), err)
		}
		have[f.Path] = f.Size
		done += f.Size
		steamPortCopyNote(done, total)
		installBarUpdateRun(gen, installPhaseDownload, done, total)
	}
	man.Files, man.Complete = have, true
	if err := steamWriteCopyManifest(dst, man); err != nil {
		return list.Skipped, err
	}
	return list.Skipped, nil
}

// steamPortFetchFile downloads one file below root through a hidden .part
// file, checking its size and flushing it before it takes the real name.
func steamPortFetchFile(ctx context.Context, root *os.Root, peerID string, appid int, f steamPeerFile, progress func(n int64)) error {
	// Five minutes plus 512 KB/s: a slow Wi-Fi still finishes a large file.
	fctx, cancel := context.WithTimeout(ctx, 5*time.Minute+time.Duration(f.Size/(512<<10))*time.Second)
	defer cancel()
	q := url.Values{}
	q.Set("appid", strconv.Itoa(appid))
	q.Set("path", f.Path)
	resp, err := peerDo(fctx, peerID, http.MethodGet, steamPeerFilePath+"?"+q.Encode(), nil, -1)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return peerResponseError(resp)
	}
	rel := filepath.FromSlash(f.Path)
	if dir := filepath.Dir(rel); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	part := filepath.Join(filepath.Dir(rel), "."+filepath.Base(rel)+".part")
	out, err := root.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, &mcCountingReader{r: io.LimitReader(resp.Body, f.Size+1), fn: progress})
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != f.Size {
		err = fmt.Errorf("got %d bytes, expected %d", n, f.Size)
	}
	if err != nil {
		_ = root.Remove(part)
		return err
	}
	if info, err := root.Lstat(rel); err == nil && info.Mode().IsRegular() {
		_ = root.Remove(rel)
	}
	if err := root.Rename(part, rel); err != nil {
		_ = root.Remove(part)
		return err
	}
	return nil
}

// ── Serving a game's files (the PC's side) ───────────────────────────────

// steamPeerFile is one file in /peer/v1/steam/files, its path relative to
// the game's folder with "/" between names.
type steamPeerFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// steamPeerFiles is the answer of /peer/v1/steam/files.
type steamPeerFiles struct {
	AppID int             `json:"appid"`
	Name  string          `json:"name"`
	Files []steamPeerFile `json:"files"`
	Bytes int64           `json:"bytes"`
	// Skipped counts links and names a handheld's card cannot hold.
	Skipped int `json:"skipped,omitempty"`
}

// steamPeerRelOK reports whether rel is a path JukaHub serves and writes: a
// clean relative path of names a FAT/exFAT card can hold, none hidden.
func steamPeerRelOK(rel string) bool {
	if rel == "" || len(rel) > 512 || strings.ContainsAny(rel, "\\\x00:") || strings.HasPrefix(rel, "/") {
		return false
	}
	if path.Clean(rel) != rel {
		return false
	}
	segs := strings.Split(rel, "/")
	if len(segs) > 24 {
		return false
	}
	for _, seg := range segs {
		if !mcSafeName(seg) {
			return false
		}
	}
	return true
}

// steamBusyFlags are the EAppState bits of a game Steam is changing or
// checking: files missing or corrupt, updating, uninstalling, backing up,
// reconfiguring, validating, adding files, preallocating, downloading,
// staging, committing or stopping an update.
const steamBusyFlags = 32 | 128 | 256 | 512 | 1024 | 2048 | 4096 | 65536 | 131072 | 262144 | 524288 |
	1<<20 | 1<<21 | 1<<22 | 1<<23

// steamAppInstallDir is the folder of a Steam game fully installed on this
// PC, and its name; "" while it is not installed or Steam is changing it.
func steamAppInstallDir(appid int) (string, string) {
	root := steamRoot()
	if root == "" || appid <= 0 || appid > 1<<31-1 {
		return "", ""
	}
	for _, lib := range steamLibraryFolders(root) {
		data, err := steamReadSmall(filepath.Join(lib, "appmanifest_"+strconv.Itoa(appid)+".acf"), 256<<10)
		if err != nil {
			continue
		}
		st := parseVDF(string(data)).child("appstate")
		if st == nil {
			continue
		}
		flags, err := strconv.ParseInt(st.get("stateflags"), 10, 64)
		if err != nil || flags&steamStateFullyInstalled == 0 || flags&steamBusyFlags != 0 {
			return "", ""
		}
		inst := strings.TrimSpace(st.get("installdir"))
		if !mcSafeName(inst) {
			return "", ""
		}
		dir := filepath.Join(lib, "common", inst)
		if !dirExists(dir) {
			continue
		}
		return dir, steamCleanText(st.get("name"), 120)
	}
	return "", ""
}

var (
	errSteamTooManyFiles = errors.New("this game has too many files to copy")
	errSteamTooLarge     = errors.New("this game is larger than a handheld copy allows")
)

// steamListGameFiles walks a game's folder: regular files only, never
// following a link or a junction, skipping names a card cannot hold, and
// stopping when the asking device gives up (ctx) or the list grows too big.
func steamListGameFiles(ctx context.Context, dir string) (steamPeerFiles, error) {
	out := steamPeerFiles{Files: []steamPeerFile{}}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return out, err
	}
	dirs, pathBytes := 0, 0
	err = filepath.WalkDir(realDir, func(p string, d fs.DirEntry, err error) error {
		if p == realDir {
			return err
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			out.Skipped++
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(realDir, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if d.Type()&fs.ModeSymlink != 0 || !steamPeerRelOK(rel) {
			out.Skipped++
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if dirs++; dirs > steamPortListMaxDirs {
				return errSteamTooManyFiles
			}
			return nil
		}
		if !d.Type().IsRegular() {
			// A Windows junction or another reparse point lands here.
			out.Skipped++
			return nil
		}
		info, err := d.Info()
		if err != nil {
			out.Skipped++
			return nil
		}
		pathBytes += len(rel) + 32
		if len(out.Files) >= steamPortCopyMaxFiles || pathBytes > steamPortListMaxPathBytes {
			return errSteamTooManyFiles
		}
		out.Files = append(out.Files, steamPeerFile{Path: rel, Size: info.Size()})
		out.Bytes += info.Size()
		if out.Bytes > steamPortCopyMaxBytes {
			return errSteamTooLarge
		}
		return nil
	})
	return out, err
}

// steamPeerAppID reads the appid query of a peer call (a Steam app, never a
// non-Steam game: those have no folder Steam manages).
func steamPeerAppID(r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.URL.Query().Get("appid"))
	return id, err == nil && id > 0 && id <= 1<<31-1
}

// steamHandlePeerFiles lists an installed game's files for a paired
// handheld. Only paired devices reach it (peerGuard).
func steamHandlePeerFiles(w http.ResponseWriter, r *http.Request, call peerCall) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	appid, ok := steamPeerAppID(r)
	if !ok {
		http.Error(w, "bad app id", http.StatusBadRequest)
		return
	}
	who := steamCleanText(call.rec.Name, 40)
	dir, name := steamAppInstallDir(appid)
	if dir == "" {
		http.Error(w, "that game is not installed in Steam on this PC, or Steam is updating it", http.StatusNotFound)
		return
	}
	list, err := steamListGameFiles(r.Context(), dir)
	if err != nil {
		msg := "the game's folder could not be read"
		if errors.Is(err, errSteamTooManyFiles) || errors.Is(err, errSteamTooLarge) {
			msg = err.Error()
		}
		logWarn("steam", "listing app %d for %s: %s", appid, who, msg)
		http.Error(w, msg, http.StatusConflict)
		return
	}
	list.AppID, list.Name = appid, name
	logInfo("steam", "%s is copying %s (%d files, %s)", who, name, len(list.Files), humanBytes(list.Bytes))
	peerWriteJSONResponse(w, http.StatusOK, list)
}

var errPeerNoFile = errors.New("not found")

// peerOpenRegularUnder opens the regular file rel ("/"-separated, already
// checked as clean and relative) below dir for a peer. Every folder on the
// way must be a real folder — a symlink, and a Windows junction (which
// Lstat reports as irregular since Go 1.23), are refused — the last name a
// regular file, and the open itself goes through os.Root, which refuses any
// escape from dir. The file opened must be the one checked.
func peerOpenRegularUnder(dir, rel string) (*os.File, os.FileInfo, error) {
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, nil, errPeerNoFile
	}
	segs := strings.Split(rel, "/")
	cur := realDir
	var last os.FileInfo
	for i, seg := range segs {
		cur = filepath.Join(cur, seg)
		info, err := os.Lstat(cur)
		if err != nil {
			return nil, nil, errPeerNoFile
		}
		if (i < len(segs)-1 && !info.IsDir()) || (i == len(segs)-1 && !info.Mode().IsRegular()) {
			return nil, nil, errPeerNoFile
		}
		last = info
	}
	root, err := os.OpenRoot(realDir)
	if err != nil {
		return nil, nil, errPeerNoFile
	}
	defer root.Close()
	f, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		return nil, nil, errPeerNoFile
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(last, info) {
		f.Close()
		return nil, nil, errPeerNoFile
	}
	return f, info, nil
}

// steamHandlePeerFile sends one file of an installed game to a paired
// handheld. Only paired devices reach it (peerGuard).
func steamHandlePeerFile(w http.ResponseWriter, r *http.Request, call peerCall) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	appid, ok := steamPeerAppID(r)
	rel := r.URL.Query().Get("path")
	if !ok || !steamPeerRelOK(rel) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	dir, _ := steamAppInstallDir(appid)
	if dir == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	f, info, err := peerOpenRegularUnder(dir, rel)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	if info.Size() > steamPortCopyMaxBytes {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, io.LimitReader(f, info.Size()))
}
