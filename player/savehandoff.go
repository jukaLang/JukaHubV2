package main

// savehandoff.go — One-tap save handoff: one game's saves moved between this
// device and one paired JukaHub (the desktop and the handheld, or two
// handhelds) with one press, from the game's own page (More → Saves on …).
//
// It is Save sync (ssync.go) narrowed to one game and made explicit. The other
// device is asked for the manifest Save sync already uses
// (/peer/v1/saves/manifest: every save file with its SHA-256, under the folder
// keys both devices agree on, such as "retroarch-saves"), and this device
// describes only the game's own files. There is no new endpoint: the files
// move through Save sync's prepare, file and done calls, so its safety rules
// hold unchanged — the device about to lose a version takes a Save Manager
// snapshot first, every file lands through a temp file checked against its
// checksum, and nothing is ever deleted.
//
// Which files are the game's. RetroArch names battery saves and states after
// the ROM's file name, so they are found by that stem on both devices,
// whatever path the ROM has there (C:\Games\GBA on the PC, Roms/GBA on the
// card): "<stem>.srm" (.sav, .rtc …), "<stem>.state", ".stateN", ".state.auto"
// and their ".png" screenshots, at the top of a save folder or in RetroArch's
// per-core and per-content sub-folders — but not in the folder of a core that
// only runs another console ("Snes9x/Mario.srm" is not NES Mario's). NextUI's
// "<rom file>.sav" and DraStic's "<stem>_N.dss" are matched as well. PSP
// SAVEDATA is not: its folders are named after the disc's ID, which nothing in
// JukaHub reads yet. The previous auto save (".jukahub-prev") stays on its own
// device, and the copies an earlier conflict kept ("<stem> (from …).srm") are
// not compared; they are only looked at so a new copy never lands on one.
//
// Which side is newer. Content first: equal checksums are the same save, and
// the base Save sync records after every sync between two devices (the
// version both had) says which side changed since. Only without a base do the
// modification times decide, corrected for the other device's clock, and the
// screen then says "probably newer": a TrimUI boots before NTP, so its file
// times can be hours off. When both sides changed since the base — or the
// battery save changed on one device and a state on the other — it is a
// conflict, and the screen shows both versions side by side.
//
// What a press does. "Send to <device>" makes the other device's copy this
// one's, "Get from <device>" the reverse; files only one side has are copied
// across either way, so afterwards both hold the same set. A version that is
// overwritten without the winning side ever having seen it (it changed since
// the last sync, or with no base it is not clearly the older one) is kept as
// "<name> (from <device>).<ext>" beside the winner on both devices, the way
// Save sync keeps a conflict; a version that is simply older is overwritten
// and stays in that device's "before sync" snapshot. Both devices then record
// the new base, so the next comparison — this one or Save sync's — starts
// from it.
//
// Nothing here waits on the network on the render thread or the reaper
// goroutine. The page's chip is filled by a goroutine when the page opens and
// only asks devices with a known address, so a device that is away costs no
// scan; the hint after a game reuses the manifest that check fetched.

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// shoRemoteTTL is how long the other device's manifest answers the page
	// chips: flicking through a shelf asks once, not once per game.
	shoRemoteTTL = 45 * time.Second
	// shoChipBudget bounds the chip's check. The very first one may wait for
	// the other device to hash its whole card; the chip then appears late.
	shoChipBudget = 40 * time.Second
	// shoCheckBudget bounds the screen's check, shoRunBudget one press.
	shoCheckBudget = 3 * time.Minute
	shoRunBudget   = 15 * time.Minute
	// shoChipPeers caps how many paired devices one chip asks.
	shoChipPeers = 3
	// shoHintMinRun: a game that ran less than this gets no hint after it.
	shoHintMinRun = time.Minute
	// shoHintAge is how old the other device's manifest may be for the hint.
	shoHintAge = 2 * time.Hour
	// shoHintGap spaces hints out overall, shoHintGameGap for one game.
	shoHintGap     = 10 * time.Minute
	shoHintGameGap = 2 * time.Hour
	// shoThumbKeep is how many screenshots of the other device's states stay
	// in the data folder.
	shoThumbKeep = 12
)

var (
	errShoBusy  = errors.New("Save sync is busy with another job — try again when it finishes")
	errShoStale = errors.New("This game's saves changed here since the comparison — check again")
	errShoCase  = errors.New("These saves are named with different upper/lower case on the two devices — rename one to match")
)

// ── Which files are the game's ──────────────────────────────────────────────

// shoGame is what finds a game's saves on either device.
type shoGame struct {
	path    string
	stem    string // the ROM's file name without its extension, lower case
	romFile string // the ROM's file name, lower case (NextUI's save names)
	system  string
}

func shoGameOf(game libraryGame) shoGame {
	base := strings.TrimSpace(game.Path)
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	return shoGame{path: game.Path, stem: ssyncGameStem(game.Path), romFile: strings.ToLower(base), system: game.System}
}

// shoApplies reports whether the game keeps emulator saves at all: Android
// apps, web games, ports and the built-in games keep theirs elsewhere.
func shoApplies(game libraryGame) bool {
	switch game.System {
	case builtInLibrarySystem, androidLibrarySystem, html5LibrarySystem, portsLibrarySystem:
		return false
	}
	return ssyncGameStem(game.Path) != ""
}

// shoBatteryExts are the battery-save extensions written after the ROM's stem
// by the RetroArch cores and the standalone handheld emulators.
var shoBatteryExts = map[string]bool{
	"srm": true, "sav": true, "rtc": true, "eep": true, "sra": true, "fla": true, "mpk": true,
	"mcr": true, "mcd": true, "dsv": true, "nv": true, "brm": true, "bcr": true, "hi": true,
}

// shoMultiCores run more than one console, so their folder says nothing about
// which console a save belongs to.
var shoMultiCores = map[string]bool{
	"genesis_plus_gx": true, "picodrive": true, "smsplus": true, "gearsystem": true,
	"gambatte": true, "sameboy": true, "gearboy": true, "tgbdual": true, "mgba": true,
	"fbneo": true, "fbalpha": true, "fb_alpha": true, "mame": true, "beetle_pce": true,
	"mednafen_pce": true, "beetle_ngp": true, "mednafen_ngp": true, "race": true,
	"beetle_wswan": true, "mednafen_wswan": true, "dosbox": true, "flycast": true,
	"reicast": true, "atari800": true, "dolphin": true,
}

// shoCoreFolders maps a RetroArch core folder name (lower case) to the one
// console its core runs, "" for a core of several consoles.
var shoCoreFolders struct {
	once sync.Once
	m    map[string]string
}

func shoCoreFolderSystem(folder string) (system string, known bool) {
	shoCoreFolders.once.Do(func() {
		m := map[string]string{}
		for _, row := range emuCoreSystems {
			sys := row.system
			if shoMultiCores[row.prefix] {
				sys = ""
			}
			for _, name := range resetCoreFolders(row.prefix) {
				key := strings.ToLower(name)
				if old, seen := m[key]; seen && old != sys {
					m[key] = ""
					continue
				}
				m[key] = sys
			}
		}
		shoCoreFolders.m = m
	})
	system, known = shoCoreFolders.m[strings.ToLower(folder)]
	return system, known
}

// shoConsoleKey folds a console name for comparing: a Game Boy Color save and
// a Game Boy save live under the same cores.
func shoConsoleKey(system string) string {
	s := strings.ToLower(canonicalCompatSystem(system))
	if s == "game boy color" {
		return "game boy"
	}
	return s
}

// shoDirFits reports whether a save in dir (slash path inside its save
// folder) can belong to a game of system: no folder of it is a core or a Roms
// folder of another console.
func shoDirFits(dir, system string) bool {
	want := shoConsoleKey(system)
	if dir == "" || dir == "." || want == "" {
		return true
	}
	for _, seg := range strings.Split(dir, "/") {
		if seg == "" {
			continue
		}
		if sys, known := shoCoreFolderSystem(seg); known {
			if sys != "" && shoConsoleKey(sys) != want {
				return false
			}
			continue
		}
		if alias, ok := romSystemAliases[strings.ToLower(seg)]; ok && shoConsoleKey(alias.Name) != want {
			return false
		}
	}
	return true
}

// shoDigits reports a non-empty run of ASCII digits.
func shoDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// shoNameKind sorts a lower-case file name: one of the game's saves (match),
// a copy an earlier conflict kept of one (kept), or neither.
func shoNameKind(name string, g shoGame) (match, kept bool) {
	if g.stem == "" {
		return false, false
	}
	for _, own := range []string{g.stem, g.romFile} {
		if own != "" && strings.Contains(name, " (from ") && (strings.HasPrefix(name, own+" (from ") || strings.HasPrefix(name, own+".")) {
			return false, true
		}
	}
	if strings.Contains(name, gmetaPrevSuffix) {
		return false, false
	}
	if gameResetStateFile(name, g.stem) {
		return true, false
	}
	if rest, ok := strings.CutPrefix(name, g.stem+"."); ok && shoBatteryExts[rest] {
		return true, false
	}
	if g.romFile != "" && g.romFile != g.stem {
		if rest, ok := strings.CutPrefix(name, g.romFile+"."); ok {
			nextuiState := len(rest) == 3 && strings.HasPrefix(rest, "st") && shoDigits(rest[2:])
			if shoBatteryExts[rest] || nextuiState {
				return true, false
			}
		}
	}
	if rest, ok := strings.CutPrefix(name, g.stem+"_"); ok {
		if n, ok := strings.CutSuffix(rest, ".dss"); ok && shoDigits(n) {
			return true, false
		}
	}
	return false, false
}

// shoFileKind is shoNameKind for a slash path inside a save folder, with the
// folder check.
func shoFileKind(rel string, g shoGame) (match, kept bool) {
	dir, name := path.Split(rel)
	match, kept = shoNameKind(strings.ToLower(name), g)
	if (match || kept) && !shoDirFits(strings.TrimSuffix(dir, "/"), g.system) {
		return false, false
	}
	return match, kept
}

// shoIsState reports a save state itself (not its screenshot).
func shoIsState(rel string, g shoGame) bool {
	name := strings.ToLower(path.Base(rel))
	if strings.HasSuffix(name, ".png") {
		return false
	}
	if gameResetStateFile(name, g.stem) {
		return true
	}
	return strings.HasSuffix(name, ".dss") || (len(name) > 4 && name[len(name)-4:len(name)-1] == ".st" && shoDigits(name[len(name)-1:]))
}

// ── This device's files ─────────────────────────────────────────────────────

// shoLocalManifest describes this device's files of the game, and the kept
// copies beside them, the way ssyncBuildManifest describes every save. It
// shares Save sync's hash cache, so an unchanged save state is not read again.
func shoLocalManifest(ctx context.Context, g shoGame, roots []saveRoot) (ssyncManifest, error) {
	m := ssyncManifest{V: 1, Now: time.Now().Unix(), Name: peerDeviceName()}
	if peerIdentityExists() {
		if self, ok := peerSelf(); ok {
			m.Device, m.Name = self.id, self.name
		}
	}
	if g.stem == "" {
		return m, nil
	}
	keys := ssyncRootKeys(roots)
	cache := ssyncHashSnapshot()
	for i, root := range roots {
		if !dirExists(root.Path) {
			continue
		}
		key := keys[i]
		m.Roots = append(m.Roots, ssyncRoot{Key: key, Name: root.Name})
		rootPath := root.Path
		walkErr := filepath.WalkDir(rootPath, func(p string, d os.DirEntry, err error) error {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if err != nil || d == nil || d.IsDir() {
				// An unreadable folder is skipped; the rest still counts.
				return nil
			}
			if !d.Type().IsRegular() || !isSaveFile(p) {
				return nil
			}
			rel, err := filepath.Rel(rootPath, p)
			if err != nil || strings.HasPrefix(rel, "..") {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if match, kept := shoFileKind(rel, g); !match && !kept {
				return nil
			}
			if _, ok := ssyncCleanRel(rel); !ok {
				m.Skipped++
				return nil
			}
			info, err := d.Info()
			if err != nil || info.Size() > saveManagerMaxFile {
				return nil
			}
			mod := info.ModTime()
			sum := ""
			if e, ok := cache[p]; ok && e.Size == info.Size() && e.MTime == mod.UnixNano() && ssyncValidSHA(e.SHA) {
				sum = e.SHA
			} else {
				s, err := SHA256File(p)
				if err != nil {
					return nil
				}
				sum = strings.ToLower(s)
				shoHashRemember(p, info.Size(), mod.UnixNano(), sum)
			}
			m.Files = append(m.Files, ssyncFile{Root: key, Path: rel, Size: info.Size(), MTime: mod.Unix(), SHA: sum})
			return nil
		})
		if walkErr != nil {
			return ssyncManifest{}, walkErr
		}
	}
	sort.Slice(m.Files, func(a, b int) bool { return m.Files[a].key() < m.Files[b].key() })
	return m, nil
}

// shoHashRemember adds a checksum to Save sync's hash cache under the size and
// time the file had before it was read (ssyncHashNote stats it afterwards): a
// save written while it was being read is then read again next time instead of
// being answered with the old checksum by every later manifest.
func shoHashRemember(p string, size, mtime int64, sum string) {
	ssyncHashes.mu.Lock()
	defer ssyncHashes.mu.Unlock()
	ssyncHashLoadLocked()
	if ssyncHashes.entries == nil {
		ssyncHashes.entries = make(map[string]ssyncHashEntry)
	}
	ssyncHashes.entries[p] = ssyncHashEntry{Size: size, MTime: mtime, SHA: sum}
}

// shoSameFiles reports whether two lists hold the same files with the same
// content.
func shoSameFiles(a, b []ssyncFile) bool {
	if len(a) != len(b) {
		return false
	}
	have := make(map[string]string, len(a))
	for _, f := range a {
		have[f.key()] = f.SHA
	}
	for _, f := range b {
		if sha, ok := have[f.key()]; !ok || !strings.EqualFold(sha, f.SHA) {
			return false
		}
	}
	return true
}

// ── The other device's manifest ─────────────────────────────────────────────

// shoRemote is a paired device's whole save manifest, with how far its clock
// is ahead of this one (0 when within a minute).
type shoRemote struct {
	man  ssyncManifest
	skew int64
	// sent is when it was asked for, at when the answer came.
	sent time.Time
	at   time.Time
}

var shoNet struct {
	mu     sync.Mutex
	remote map[string]shoRemote
	// slot keeps one request per device in flight: the chip and the screen
	// asking at once share the answer. A channel, so a caller can stop
	// waiting for it.
	slot map[string]chan struct{}
	// changed is when a press last changed a device's saves: an answer to a
	// request sent before then is not kept for later asks.
	changed map[string]time.Time
}

// shoCachedRemote is the device's manifest when one no older than maxAge is
// kept.
func shoCachedRemote(peerID string, maxAge time.Duration) (shoRemote, bool) {
	shoNet.mu.Lock()
	defer shoNet.mu.Unlock()
	r, ok := shoNet.remote[peerID]
	if !ok || time.Since(r.at) > maxAge {
		return shoRemote{}, false
	}
	return r, true
}

// shoForgetRemote drops a device's kept manifest (its saves just changed).
func shoForgetRemote(peerID string) {
	shoNet.mu.Lock()
	delete(shoNet.remote, peerID)
	if shoNet.changed == nil {
		shoNet.changed = map[string]time.Time{}
	}
	shoNet.changed[peerID] = time.Now()
	shoNet.mu.Unlock()
}

// shoFetchRemote asks a paired device for its manifest. fresh skips the copy
// kept from an earlier ask, unless that ask was sent after this one started.
//
// ctx only bounds how long the caller waits, not the request: the first ask
// can make the other device hash every save on its card, and a page flicked
// past (or the chip's budget running out) must not stop that half way — the
// device would start over on every ask and never finish, and a dropped
// request also makes this device forget the other's address. The answer is
// kept for the next ask.
func shoFetchRemote(ctx context.Context, peerID, peerName string, fresh bool) (shoRemote, error) {
	asked := time.Now()
	shoNet.mu.Lock()
	if shoNet.slot == nil {
		shoNet.slot = map[string]chan struct{}{}
	}
	slot := shoNet.slot[peerID]
	if slot == nil {
		slot = make(chan struct{}, 1)
		shoNet.slot[peerID] = slot
	}
	shoNet.mu.Unlock()
	select {
	case slot <- struct{}{}:
	case <-ctx.Done():
		return shoRemote{}, ctx.Err()
	}
	if r, ok := shoCachedRemote(peerID, shoRemoteTTL); ok && (!fresh || !r.sent.Before(asked)) {
		<-slot
		return r, nil
	}
	if err := ctx.Err(); err != nil {
		<-slot
		return shoRemote{}, err
	}
	type answer struct {
		r   shoRemote
		err error
	}
	got := make(chan answer, 1)
	go func() {
		defer func() { <-slot }()
		r, err := shoAskManifest(peerID, peerName)
		got <- answer{r, err}
	}()
	select {
	case a := <-got:
		return a.r, a.err
	case <-ctx.Done():
		return shoRemote{}, ctx.Err()
	}
}

// shoAskManifest is one manifest request, bounded by the screen's budget. Its
// answer is kept unless a press changed the device's saves after it was sent.
func shoAskManifest(peerID, peerName string) (shoRemote, error) {
	ctx, cancel := context.WithTimeout(context.Background(), shoCheckBudget)
	defer cancel()
	sent := time.Now()
	var man ssyncManifest
	if err := peerDoJSON(ctx, peerID, http.MethodGet, "/peer/v1/saves/manifest", nil, &man, ssyncManifestLimit); err != nil {
		return shoRemote{}, err
	}
	fetched := time.Now().Unix()
	if man.V != 1 || len(man.Files) > ssyncMaxFiles {
		return shoRemote{}, errors.New(peerName + " sent a save list this version cannot read")
	}
	// The same correction Save sync makes: a clock within a minute is
	// NTP-close and left alone.
	skew := man.Now - fetched
	if skew > -60 && skew < 60 {
		skew = 0
	}
	r := shoRemote{man: man, skew: skew, sent: sent, at: time.Now()}
	shoNet.mu.Lock()
	if !sent.Before(shoNet.changed[peerID]) {
		if shoNet.remote == nil {
			shoNet.remote = map[string]shoRemote{}
		}
		shoNet.remote[peerID] = r
	}
	shoNet.mu.Unlock()
	return r, nil
}

// ── Comparing ───────────────────────────────────────────────────────────────

// shoVerdict is what one game's comparison says.
type shoVerdict int

const (
	// shoNoSaves: neither device has a save of the game.
	shoNoSaves shoVerdict = iota
	// shoSame: both hold the same files.
	shoSame
	// shoNewerHere: only this device has changes the other lacks.
	shoNewerHere
	// shoNewerThere: only the other device has changes this one lacks.
	shoNewerThere
	// shoBothChanged: both have changes the other lacks.
	shoBothChanged
)

// shoSide is one device's copy of the game's saves, as the screen shows it.
type shoSide struct {
	Files []ssyncFile
	Bytes int64
	// Newest is the newest believable modification time, on this device's
	// clock (0 when none is believable).
	Newest int64
	// Thumb is the screenshot of the newest save state, when it has one.
	Thumb    ssyncFile
	HasThumb bool
}

// shoComparison is one game compared with one paired device.
type shoComparison struct {
	PeerID   string
	PeerName string
	Verdict  shoVerdict
	// Sure is false when only file times ordered two copies of a file.
	Sure   bool
	Local  shoSide
	Remote shoSide
	At     time.Time
	game   shoGame
	plan   ssyncPlan
	base   map[string]string
	skew   int64
	roots  []saveRoot
	local  ssyncManifest
	remote ssyncManifest
	// clash is the one-sided files the other device spells with other
	// upper/lower case (shoCaseClashes): a press leaves them alone.
	clash map[string]bool
}

// shoSideOf summarises one device's files of the game; skew moves the other
// device's times onto this clock.
func shoSideOf(files []ssyncFile, g shoGame, skew int64) shoSide {
	var s shoSide
	have := map[string]ssyncFile{}
	for _, f := range files {
		have[f.key()] = f
	}
	var newestState int64 = -1
	for _, f := range files {
		if _, kept := shoFileKind(f.Path, g); kept {
			continue
		}
		s.Files = append(s.Files, f)
		s.Bytes += f.Size
		t := f.MTime - skew
		if f.MTime >= ssyncMinTime && t > s.Newest {
			s.Newest = t
		}
		if shoIsState(f.Path, g) && t > newestState {
			if png, ok := have[f.key()+".png"]; ok {
				s.Thumb, s.HasThumb, newestState = png, true, t
			}
		}
	}
	return s
}

// shoCompare compares this device's files of the game (local, from roots)
// with a paired device's manifest.
func shoCompare(g shoGame, local ssyncManifest, roots []saveRoot, r shoRemote, peerID, peerName string) shoComparison {
	remote := ssyncManifest{V: r.man.V, Device: r.man.Device, Name: r.man.Name, Now: r.man.Now, Roots: r.man.Roots}
	for _, f := range r.man.Files {
		if match, kept := shoFileKind(f.Path, g); match || kept {
			remote.Files = append(remote.Files, f)
		}
	}
	base := ssyncLoadBase(peerID)
	plan := ssyncBuildPlan(local, remote, base, r.skew, local.Name, peerName)
	plan = ssyncFilterPlan(plan, func(it ssyncItem) bool {
		_, kept := shoFileKind(it.Path, g)
		return !kept
	})
	cmp := shoComparison{
		PeerID:   peerID,
		PeerName: peerName,
		Sure:     true,
		Local:    shoSideOf(local.Files, g, 0),
		Remote:   shoSideOf(remote.Files, g, r.skew),
		At:       time.Now(),
		game:     g,
		plan:     plan,
		base:     base,
		skew:     r.skew,
		roots:    roots,
		local:    local,
		remote:   r.man,
		clash:    shoCaseClashes(plan, local.Files, remote.Files),
	}
	for _, it := range plan.Items {
		if it.HasLocal && it.HasRemote && base[it.Key] == "" {
			cmp.Sure = false
		}
	}
	switch {
	case len(plan.Items) == 0 && len(cmp.Local.Files)+len(cmp.Remote.Files) == 0:
		cmp.Verdict = shoNoSaves
	case len(plan.Items) == 0:
		cmp.Verdict = shoSame
	case plan.Conflict > 0 || (plan.Pull > 0 && plan.Push > 0):
		cmp.Verdict = shoBothChanged
	case plan.Pull > 0:
		cmp.Verdict = shoNewerThere
	default:
		cmp.Verdict = shoNewerHere
	}
	return cmp
}

// shoCaseClashes is the one-sided files of a plan whose name the other device
// spells with other upper/lower case ("Mario.srm" here, "mario.srm" there).
// FAT32, exFAT and Windows take the two names for one file, so copying either
// across would land on the other device's file — with no snapshot, since the
// plan thinks nothing is there. They are left where they are.
func shoCaseClashes(plan ssyncPlan, local, remote []ssyncFile) map[string]bool {
	fold := func(files []ssyncFile) map[string]string {
		m := make(map[string]string, len(files))
		for _, f := range files {
			m[strings.ToLower(f.key())] = f.key()
		}
		return m
	}
	here, there := fold(local), fold(remote)
	out := map[string]bool{}
	for _, it := range plan.Items {
		if it.HasLocal == it.HasRemote {
			continue
		}
		other := there
		if it.HasRemote {
			other = here
		}
		if k, ok := other[strings.ToLower(it.Key)]; ok && k != it.Key {
			out[it.Key] = true
		}
	}
	return out
}

// shoCheck compares the game with one paired device: its manifest (fresh, or
// one kept from a recent ask) and this device's files.
func shoCheck(ctx context.Context, g shoGame, peer peerRecord, fresh bool) (shoComparison, error) {
	r, err := shoFetchRemote(ctx, peer.ID, peer.Name, fresh)
	if err != nil {
		return shoComparison{}, err
	}
	roots := saveManagerRoots()
	local, err := shoLocalManifest(ctx, g, roots)
	if err != nil {
		return shoComparison{}, err
	}
	return shoCompare(g, local, roots, r, peer.ID, peer.Name), nil
}

// ── One press ───────────────────────────────────────────────────────────────

// shoLoserChanged reports whether the version a press overwrites may hold
// play the winning side never saw: it changed since the last sync, or, with
// no base, it is not clearly the older of the two.
func shoLoserChanged(it ssyncItem, base string, skew int64, keepLocal bool) bool {
	loser := it.Local
	if keepLocal {
		loser = it.Remote
	}
	if base != "" {
		return !strings.EqualFold(loser.SHA, base)
	}
	localNewer, ordered := ssyncNewer(it.Local.MTime, it.Remote.MTime-skew)
	if !ordered {
		return true
	}
	// The loser is clearly older exactly when the winner is the newer one.
	return localNewer != keepLocal
}

// shoPlanFor turns a comparison into the copies one press makes: keepLocal is
// "Send", false is "Get".
func shoPlanFor(cmp shoComparison, keepLocal bool) ssyncPlan {
	out := ssyncPlan{Same: cmp.plan.Same}
	all := append(append([]ssyncFile(nil), cmp.local.Files...), cmp.remote.Files...)
	taken := make(map[string]bool, len(all))
	for _, f := range all {
		taken[f.key()] = true
	}
	rootDirs := ssyncRootMap(cmp.roots)
	loserName := cmp.local.Name
	if keepLocal {
		loserName = cmp.PeerName
	}
	for _, it := range cmp.plan.Items {
		if cmp.clash[it.Key] {
			continue
		}
		switch {
		case it.HasLocal && !it.HasRemote:
			it.Action, it.LocalWins = ssyncPush, true
		case !it.HasLocal && it.HasRemote:
			it.Action, it.LocalWins = ssyncPull, false
		default:
			loser := it.Local
			if keepLocal {
				loser = it.Remote
			}
			keepCopy := shoLoserChanged(it, cmp.base[it.Key], cmp.skew, keepLocal) &&
				!(ssyncHasKeptCopy(cmp.local.Files, it.Root, it.Path, loser.SHA) && ssyncHasKeptCopy(cmp.remote.Files, it.Root, it.Path, loser.SHA))
			switch {
			case keepCopy:
				it.Action, it.LocalWins = ssyncConflict, keepLocal
				root := it.Root
				it.CopyPath = ssyncConflictName(it.Path, loserName, func(rel string) bool {
					if taken[root+"/"+rel] {
						return true
					}
					dir, ok := rootDirs[root]
					if !ok {
						return false
					}
					p, err := ssyncLocalPath(dir, rel)
					return err != nil || fileExists(p)
				})
				taken[root+"/"+it.CopyPath] = true
			case keepLocal:
				it.Action, it.LocalWins = ssyncPush, true
			default:
				it.Action, it.LocalWins = ssyncPull, false
			}
		}
		out.Items = append(out.Items, it)
		switch it.Action {
		case ssyncPull:
			out.Pull++
			out.PullBytes += it.Remote.Size
		case ssyncPush:
			out.Push++
			out.PushBytes += it.Local.Size
		case ssyncConflict:
			out.Conflict++
			out.PullBytes += it.Remote.Size
			out.PushBytes += it.Local.Size + it.Remote.Size
		}
	}
	return out
}

// shoRun carries out one press. It holds Save sync's job slot, so the Save
// sync page and a handoff never write the same files at once, and the page
// shows the progress too.
func shoRun(cmp shoComparison, keepLocal bool, progress func(done, total int, current string)) (ssyncResult, error) {
	plan := shoPlanFor(cmp, keepLocal)
	if len(plan.Items) == 0 {
		if len(cmp.clash) > 0 {
			return ssyncResult{}, errShoCase
		}
		return ssyncResult{Peer: cmp.PeerName, At: time.Now()}, nil
	}
	label := "Getting saves from " + cmp.PeerName
	if keepLocal {
		label = "Sending saves to " + cmp.PeerName
	}
	jobCtx, ok := ssyncWorkStart(label)
	if !ok {
		return ssyncResult{}, errShoBusy
	}
	defer ssyncWorkEnd()
	ctx, cancel := context.WithTimeout(jobCtx, shoRunBudget)
	defer cancel()
	// The comparison can be minutes old. When this game's saves changed here
	// since (a sync from the other device, a game), the plan would replace a
	// save nobody compared — or, for a file that was not here then, replace
	// it with no snapshot at all.
	if now, err := shoLocalManifest(ctx, cmp.game, cmp.roots); err != nil {
		return ssyncResult{}, err
	} else if !shoSameFiles(now.Files, cmp.local.Files) {
		return ssyncResult{}, errShoStale
	}
	session := ssyncNewSession()
	pio := ssyncNetIO{peerID: cmp.PeerID, session: session}
	res, updates := ssyncExecute(ctx, plan, pio, cmp.roots, cmp.PeerName, "before handoff with "+cmp.PeerName,
		func(done, total int, current string) {
			ssyncWorkProgress(done, total, current)
			if progress != nil {
				progress(done, total, current)
			}
		})
	// Only this game's entries change; every other file keeps the base the
	// last full sync recorded, on both devices.
	base := ssyncLoadBase(cmp.PeerID)
	for k, v := range updates {
		base[k] = v
	}
	if err := ssyncSaveBase(cmp.PeerID, base); err != nil {
		logWarn("savehandoff", "could not save the sync base: %v", err)
	}
	// The other device toasts "Saves synced with …" on done: not after a press
	// that copied nothing (it refused the snapshot, a game is running there).
	if res.Pulled+res.Pushed+res.Conflicts > 0 {
		dctx, dcancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = peerDoJSON(dctx, cmp.PeerID, http.MethodPost, "/peer/v1/saves/done", ssyncDoneReq{
			Session: session,
			Base:    base,
			Updated: res.Pushed + res.Conflicts,
		}, nil, 0)
		dcancel()
	}
	shoForgetRemote(cmp.PeerID)
	logInfo("savehandoff", "%s: %d got, %d sent, %d kept as copies, %d failed",
		label, res.Pulled, res.Pushed, res.Conflicts, res.Failed)
	return res, nil
}

// shoResultText says what a press did, for its toast.
func shoResultText(res ssyncResult, keepLocal bool, peerName string) (string, ToastKind) {
	moved := res.Pulled + res.Pushed + res.Conflicts
	if res.Failed > 0 {
		msg := pluralizeCount(res.Failed, "save file", "save files") + " not copied"
		if len(res.Errors) > 0 {
			msg += ": " + res.Errors[0]
		}
		return msg, ToastKindWarning
	}
	if moved == 0 {
		return "Already the same on both — nothing to copy", ToastKindInfo
	}
	msg := "Got " + pluralizeCount(moved, "save file", "save files") + " from " + peerName
	if keepLocal {
		msg = "Sent " + pluralizeCount(moved, "save file", "save files") + " to " + peerName
	}
	if res.Conflicts > 0 {
		msg += " · the other version is kept as a \"(from …)\" copy"
	}
	return msg, ToastKindSuccess
}

// ── Words ───────────────────────────────────────────────────────────────────

// shoAgo says how long ago a Unix time was, briefly; "" for a time that is
// not believable (before 2020, or more than a few minutes in the future).
func shoAgo(t int64, now time.Time) string {
	if t < ssyncMinTime {
		return ""
	}
	d := now.Sub(time.Unix(t, 0))
	switch {
	case d < -5*time.Minute:
		return ""
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + " min ago"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + " h ago"
	case d < 60*24*time.Hour:
		return strconv.Itoa(int(d/(24*time.Hour))) + " days ago"
	}
	return formatDateShort(nil, time.Unix(t, 0))
}

// shoVerdictLine is the comparison in one short line ("Newer on Juka's PC ·
// 2 h ago"), the More row's second line and the screen's headline.
func shoVerdictLine(cmp shoComparison, now time.Time) string {
	probably := ""
	if !cmp.Sure {
		probably = "Probably newer"
	}
	switch cmp.Verdict {
	case shoNoSaves:
		return "No saves yet on either device"
	case shoSame:
		return "Same on both devices"
	case shoBothChanged:
		return "Changed here and on " + cmp.PeerName + " — pick the one to keep"
	case shoNewerThere:
		line := "Newer on " + cmp.PeerName
		if probably != "" {
			line = probably + " on " + cmp.PeerName
		}
		if ago := shoAgo(cmp.Remote.Newest, now); ago != "" {
			line += " · " + ago
		}
		return line
	}
	line := "Newer here"
	if probably != "" {
		line = probably + " here"
	}
	if ago := shoAgo(cmp.Local.Newest, now); ago != "" {
		line += " · " + ago
	}
	return line
}

// ── The page's chip and the More row ────────────────────────────────────────

// shoChipInfo is one game's answer for its page. Main thread only.
type shoChipInfo struct {
	text   string
	tone   int
	detail string
}

var shoPage struct {
	chips  map[string]shoChipInfo
	cancel context.CancelFunc
}

// shoReachablePeers is the paired devices with a known address right now:
// asking one costs no scan.
func shoReachablePeers() []peerRecord {
	var out []peerRecord
	for _, p := range peerPairedList() {
		if peerAddrFor(p.ID) != "" {
			out = append(out, p)
		}
	}
	return out
}

// shoChipFrom picks what the chip says from the devices that answered: a
// device with newer saves first, then a conflict, then newer here, then "in
// sync" when every device that answered is.
func shoChipFrom(cmps []shoComparison, now time.Time) (shoChipInfo, bool) {
	rank := func(v shoVerdict) int {
		switch v {
		case shoNewerThere:
			return 4
		case shoBothChanged:
			return 3
		case shoNewerHere:
			return 2
		case shoSame:
			return 1
		}
		return 0
	}
	best := -1
	for i, c := range cmps {
		if best < 0 || rank(c.Verdict) > rank(cmps[best].Verdict) {
			best = i
		}
	}
	if best < 0 || cmps[best].Verdict == shoNoSaves {
		return shoChipInfo{}, false
	}
	c := cmps[best]
	info := shoChipInfo{detail: shoVerdictLine(c, now)}
	switch c.Verdict {
	case shoNewerThere:
		info.text, info.tone = "Saves: newer on "+c.PeerName, gamePageToneWarn
		if !c.Sure {
			info.text = "Saves: probably newer on " + c.PeerName
		}
	case shoBothChanged:
		info.text, info.tone = "Saves: changed on both", gamePageToneWarn
	case shoNewerHere:
		info.text, info.tone = "Saves: newer here", gamePageToneNeutral
	default:
		info.text, info.tone = "Saves in sync", gamePageToneGood
	}
	return info, true
}

// shoPageOpened starts the chip's check (gamePageRegisterOpened, main thread).
func shoPageOpened(game libraryGame) {
	if shoPage.cancel != nil {
		shoPage.cancel()
		shoPage.cancel = nil
	}
	if !shoApplies(game) || !peerEnabledFlag.Load() {
		return
	}
	g := shoGameOf(game)
	peers := shoReachablePeers()
	if len(peers) == 0 {
		return
	}
	if len(peers) > shoChipPeers {
		peers = peers[:shoChipPeers]
	}
	ctx, cancel := context.WithTimeout(context.Background(), shoChipBudget)
	shoPage.cancel = cancel
	go func() {
		defer cancel()
		var cmps []shoComparison
		for _, p := range peers {
			cmp, err := shoCheck(ctx, g, p, false)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				continue
			}
			cmps = append(cmps, cmp)
		}
		info, ok := shoChipFrom(cmps, time.Now())
		tbxPost(func() { shoSetChip(game.Path, info, ok) })
	}()
}

// shoPageClosed stops the chip's check: nobody will see its answer
// (gamePageRegisterClosed, main thread).
func shoPageClosed() {
	if shoPage.cancel != nil {
		shoPage.cancel()
		shoPage.cancel = nil
	}
}

// shoSetChip stores (or clears) a game's chip. Main thread.
func shoSetChip(gamePath string, info shoChipInfo, ok bool) {
	if !ok {
		delete(shoPage.chips, gamePath)
		return
	}
	if shoPage.chips == nil {
		shoPage.chips = map[string]shoChipInfo{}
	}
	shoPage.chips[gamePath] = info
}

// shoChip is the page's chip, from memory (gamePageRegisterStatus).
func shoChip(game libraryGame) (string, int, bool) {
	info, ok := shoPage.chips[game.Path]
	if !ok || info.text == "" {
		return "", 0, false
	}
	return info.text, info.tone, true
}

// shoRowLabel is the More row's label: the device's name when there is one.
func shoRowLabel(peers []peerRecord) string {
	if len(peers) == 1 {
		return "Saves on " + peers[0].Name
	}
	return "Saves on another device"
}

// shoRow is the More sheet's row (gamePageRegisterExtra).
func shoRow(game libraryGame) (label, detail string, ok bool) {
	if !shoApplies(game) {
		return "", "", false
	}
	peers := peerPairedList()
	label = shoRowLabel(peers)
	switch {
	case len(peers) == 0:
		detail = "Pair your PC or handheld to move this game's saves"
	case !peerEnabledFlag.Load():
		detail = "Nearby devices is off"
	default:
		detail = "Compare, then send or get this game's saves"
		if info, ok := shoPage.chips[game.Path]; ok && info.detail != "" {
			detail = info.detail
		}
	}
	return label, detail, true
}

// ── After a game ────────────────────────────────────────────────────────────

var shoHint struct {
	mu     sync.Mutex
	last   time.Time
	byGame map[string]time.Time
}

// shoHintDue claims the hint for a game when the spacing allows one.
func shoHintDue(gamePath string, now time.Time) bool {
	shoHint.mu.Lock()
	defer shoHint.mu.Unlock()
	if !shoHint.last.IsZero() && now.Sub(shoHint.last) < shoHintGap {
		return false
	}
	if t, ok := shoHint.byGame[gamePath]; ok && now.Sub(t) < shoHintGameGap {
		return false
	}
	if shoHint.byGame == nil {
		shoHint.byGame = map[string]time.Time{}
	}
	shoHint.last = now
	shoHint.byGame[gamePath] = now
	return true
}

// shoGameExited offers the handoff once after a game (gameLaunchRegisterExited,
// reaper goroutine). It never asks the network: only a device whose manifest a
// recent check kept is compared, on a goroutine of its own, against this
// device's files as the game left them.
func shoGameExited(game libraryGame, launcher string, ranFor time.Duration, waitErr error) {
	if ranFor < shoHintMinRun || !peerEnabledFlag.Load() || !shoApplies(game) {
		return
	}
	g := shoGameOf(game)
	peers := shoReachablePeers()
	if len(peers) != 1 {
		return
	}
	peer := peers[0]
	r, ok := shoCachedRemote(peer.ID, shoHintAge)
	if !ok {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		roots := saveManagerRoots()
		local, err := shoLocalManifest(ctx, g, roots)
		if err != nil {
			return
		}
		cmp := shoCompare(g, local, roots, r, peer.ID, peer.Name)
		info, chipOK := shoChipFrom([]shoComparison{cmp}, time.Now())
		hint := cmp.Verdict == shoNewerHere && shoHintDue(game.Path, time.Now())
		msg := "Saves newer here than on " + peer.Name + " — More → " + shoRowLabel(peerPairedList()) + " sends them"
		tbxPost(func() {
			shoSetChip(game.Path, info, chipOK)
			if hint {
				ShowToast(msg, ToastKindInfo)
			}
		})
	}()
}

// ── The other device's state screenshot ─────────────────────────────────────

// shoThumbDir keeps the screenshots fetched from other devices.
func shoThumbDir() string {
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return filepath.Join(dir, "savehandoff", "thumbs")
	}
	return filepath.Join("data", "savehandoff", "thumbs")
}

// shoFetchThumb copies the other device's state screenshot f here (named by
// its checksum, so one already fetched is not fetched again) and returns its
// path. Only the newest few are kept.
func shoFetchThumb(ctx context.Context, peerID string, f ssyncFile) (string, error) {
	if !ssyncValidSHA(f.SHA) || f.Size <= 0 || f.Size > 4<<20 {
		return "", errors.New("no usable screenshot")
	}
	dir := shoThumbDir()
	dst := filepath.Join(dir, strings.ToLower(f.SHA)+".png")
	if regularFile(dst) {
		now := time.Now()
		_ = os.Chtimes(dst, now, now)
		return dst, nil
	}
	if err := (ssyncNetIO{peerID: peerID}).pull(ctx, f, dst); err != nil {
		return "", err
	}
	now := time.Now()
	_ = os.Chtimes(dst, now, now)
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > shoThumbKeep {
		type aged struct {
			path string
			at   time.Time
		}
		var all []aged
		for _, e := range entries {
			if info, err := e.Info(); err == nil && info.Mode().IsRegular() {
				all = append(all, aged{filepath.Join(dir, e.Name()), info.ModTime()})
			}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].at.After(all[j].at) })
		for i := shoThumbKeep; i < len(all); i++ {
			_ = os.Remove(all[i].path)
		}
	}
	return dst, nil
}

func init() {
	gamePageRegisterExtra(gamePageExtra{ID: "savehandoff", Order: 40, Icon: "handoff", Row: shoRow, Open: shoOpen})
	gamePageRegisterStatus(gamePageStatus{ID: "savehandoff", Order: 40, Chip: shoChip})
	gamePageRegisterOpened(shoPageOpened)
	gamePageRegisterClosed(shoPageClosed)
	gamePageRegisterOverlay(gamePageOverlay{
		ID:     "savehandoff",
		IsOpen: shoUIIsOpen,
		Key:    shoUIKey,
		Pad:    shoUIPad,
		Mouse:  shoUIMouse,
		Render: shoUIRender,
		Close:  shoUIClose,
	})
	gameLaunchRegisterExited(shoGameExited)
}
