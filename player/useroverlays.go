package main

// useroverlays.go — your own overlays: an Overlays/<Console>/ folder on the
// card for every console with games, where a player drops a border picture
// (or a whole RetroArch overlay) for that console's games.
//
// RetroArch can draw an overlay over a game, but its overlays are .cfg files
// that point at pictures, kept in RetroArch's own folder and picked three
// menus deep. Other firmwares for these handhelds let a player drop a picture
// into a folder named after the console and be done. This is that, on top of
// the Overlay choice Settings → Emulation already has (ratuning.go):
//
//   - The folders. After a library scan (startGameLibraryScan) every console
//     with games gets Overlays/<Console>/ beside the Roms folder — on the
//     handheld /mnt/SDCARD/Overlays/Game Boy/ — with a README.txt saying what
//     goes in. Each folder is made once: one the player deletes stays deleted
//     (data/overlays-made.txt remembers). A read-only card is left alone. A
//     folder under a short name (Overlays/GBA, Overlays/PS) counts as the
//     console's too — the names the Roms folders take work here — and while
//     one is there no long-named folder is made beside it. NextUI keeps its
//     own overlays that way (Overlays/<TAG>/), so a card it shares gets no
//     second set of folders.
//   - What goes in: a PNG the size of the screen with a see-through hole where
//     the game shows (1024×768 on the Brick, 1280×720 on the Smart Pro), or a
//     RetroArch overlay .cfg with its pictures. A picture a .cfg in the same
//     folder uses is not offered on its own.
//   - Which one plays. The console's own Overlay choice (Settings → Emulation
//     → Adjust for <Console> → Overlay) lists every folder's overlays as "Your
//     overlay · <name>". Left at Default, a console whose own folder — the
//     one named like the console, the one JukaHub makes — holds an overlay
//     uses it (the first by name when there are several): dropping a picture
//     there is the choice. A short-named folder's overlays play only when
//     picked: in NextUI's folders they are choices for its in-game menu, not
//     something every game should get. Otherwise Default follows All
//     consoles. The console's own None or RetroArch's own leaves the folders
//     unused.
//   - A bare picture has no .cfg for RetroArch to load, so the launch writes
//     one into JukaHub's data folder (data/overlays/): one full-screen,
//     normalised layer with the picture's absolute path and no buttons. The
//     launch's input_overlay points at it like at any other overlay, through
//     raScopeApply and at the opacity ratuning.go uses (raTuneOverlayOpacity).
//
// The folders are shared over FTP and on the phone's Files card ("Overlays"
// in ftpShareNames), so a picture can be dropped from a computer or a phone.
//
// PICO-8: fake-08 runs in RetroArch and takes Overlays/PICO-8 like any other
// console. The native PICO-8 (pico8native.go) is a program of its own that
// draws the whole screen itself: no overlay reaches it.

import (
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	userOverlaysDirName    = "Overlays"
	userOverlaysReadme     = "README.txt"
	userOverlaysMadeRecord = "overlays-made.txt" // in JukaHub's data folder
	userOverlaysGenDir     = "overlays"          // generated .cfg files, in the data folder

	// The scans are bounded: a card full of something else must not stall
	// a launch or the settings page's background refresh.
	userOverlaysRootMax = 300      // entries read in Overlays/
	userOverlaysDirMax  = 200      // entries read in one console's folder
	userOverlaysListMax = 60       // overlays offered for one console
	userOverlaysCfgMax  = 64 << 10 // the most of a .cfg read for the pictures it names

	// userOverlaysFresh is how old the menus' list may be before a look at it
	// starts a rescan in the background.
	userOverlaysFresh = 10 * time.Second
)

// userOverlay is one overlay in a console's folder.
type userOverlay struct {
	Path string // the .png or .cfg
	Name string // its file name without the extension
	Dir  string // the folder under Overlays/ it is in ("Game Boy", "GBA")
}

// userOverlaysSkip reports a console that never plays in RetroArch, so no
// overlay folder is made for it.
func userOverlaysSkip(console string) bool {
	switch strings.ToLower(strings.TrimSpace(console)) {
	case "", "godot", "j2me", "xbox", "xbox 360", "android", "html5", "steam", "ports",
		strings.ToLower(chip8System), strings.ToLower(wasm4System), strings.ToLower(emu3DSSystem):
		return true
	}
	return false
}

// userOverlaysConsole is the console a folder name stands for, the way the
// Roms folders are read: "GBA" and "Game Boy Advance" are the same console.
func userOverlaysConsole(name string) string {
	name = strings.TrimSpace(name)
	if sys, ok := romSystemAliases[strings.ToLower(name)]; ok && sys.Name != "" {
		return sys.Name
	}
	return canonicalCompatSystem(name)
}

// userOverlaysSameConsole reports whether a folder under Overlays/ is the
// console's.
func userOverlaysSameConsole(dirName, console string) bool {
	return strings.EqualFold(userOverlaysConsole(dirName), userOverlaysConsole(console))
}

// userOverlaysFolderName is a console's folder name, or "" for a name that
// cannot be one folder.
func userOverlaysFolderName(console string) string {
	console = strings.TrimSpace(console)
	if console == "" || strings.HasPrefix(console, ".") || strings.ContainsAny(console, `/\:*?"<>|`) {
		return ""
	}
	return console
}

// userOverlaysRoots are the Overlays folders: one beside each Roms folder the
// library reads (the card's, on the handheld). They need not exist yet.
func userOverlaysRoots() []string {
	var out []string
	for _, roms := range romLibraryRoots() {
		if !filepath.IsAbs(roms) || !strings.EqualFold(filepath.Base(roms), "Roms") {
			continue
		}
		root := filepath.Join(filepath.Dir(roms), userOverlaysDirName)
		dup := false
		for _, have := range out {
			if strings.EqualFold(filepath.Clean(have), filepath.Clean(root)) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, root)
		}
	}
	return out
}

// userOverlaysReadDir lists at most limit entries of dir, in the order the
// file system keeps them.
func userOverlaysReadDir(dir string, limit int) []os.DirEntry {
	f, err := os.Open(dir)
	if err != nil {
		return nil
	}
	defer f.Close()
	entries, _ := f.ReadDir(limit)
	return entries
}

// userOverlaysCfgPictures are the pictures (lower-case base names) a .cfg
// names in its overlayN_overlay and overlayN_descM_overlay lines.
func userOverlaysCfgPictures(path string) map[string]bool {
	out := map[string]bool{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	raw, _ := io.ReadAll(io.LimitReader(f, userOverlaysCfgMax))
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, _, ok := raConfigLine(line)
		if !ok || value == "" || !strings.HasSuffix(strings.ToLower(key), "_overlay") {
			continue
		}
		out[strings.ToLower(filepath.Base(filepath.FromSlash(value)))] = true
	}
	return out
}

// userOverlaysScanDir lists one console folder's overlays: its .cfg files,
// and its PNGs that no .cfg there uses. Sorted by name.
func userOverlaysScanDir(dir string) []userOverlay {
	var cfgs, pngs []os.DirEntry
	for _, e := range userOverlaysReadDir(dir, userOverlaysDirMax) {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		switch strings.ToLower(filepath.Ext(name)) {
		case ".cfg":
			cfgs = append(cfgs, e)
		case ".png":
			pngs = append(pngs, e)
		}
	}
	used := map[string]bool{}
	for _, e := range cfgs {
		used[strings.ToLower(strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())))+".png"] = true
		for pic := range userOverlaysCfgPictures(filepath.Join(dir, e.Name())) {
			used[pic] = true
		}
	}
	base := filepath.Base(dir)
	var out []userOverlay
	add := func(e os.DirEntry) {
		full := filepath.Join(dir, e.Name())
		if !regularFile(full) { // a link to nowhere, a fifo
			return
		}
		out = append(out, userOverlay{Path: full, Name: strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())), Dir: base})
	}
	for _, e := range cfgs {
		add(e)
	}
	for _, e := range pngs {
		if !used[strings.ToLower(e.Name())] {
			add(e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	if len(out) > userOverlaysListMax {
		out = out[:userOverlaysListMax]
	}
	return out
}

// userOverlaysScanAll reads every console folder under the Overlays roots:
// overlays by console (userOverlaysConsole's name, lower case).
func userOverlaysScanAll(roots []string) map[string][]userOverlay {
	out := map[string][]userOverlay{}
	for _, root := range roots {
		for _, e := range userOverlaysReadDir(root, userOverlaysRootMax) {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			list := userOverlaysScanDir(filepath.Join(root, e.Name()))
			if len(list) == 0 {
				continue
			}
			key := strings.ToLower(userOverlaysConsole(e.Name()))
			out[key] = append(out[key], list...)
		}
	}
	for key, list := range out {
		sort.SliceStable(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
		if len(list) > userOverlaysListMax {
			out[key] = list[:userOverlaysListMax]
		}
	}
	return out
}

// userOverlaysFor reads one console's overlays from the card now: the
// launch's question, a handful of directory reads.
func userOverlaysFor(console string) []userOverlay {
	if userOverlaysSkip(console) {
		return nil
	}
	var out []userOverlay
	for _, root := range userOverlaysRoots() {
		for _, e := range userOverlaysReadDir(root, userOverlaysRootMax) {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && userOverlaysSameConsole(e.Name(), console) {
				out = append(out, userOverlaysScanDir(filepath.Join(root, e.Name()))...)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// userOverlaysAutoPick is the overlay a console's Default plays from a list
// of its overlays: the first in the folder named like the console (the one
// JukaHub makes); ok is false when that folder has none. A short-named
// folder's overlays are choices only. Pure.
func userOverlaysAutoPick(list []userOverlay, console string) (userOverlay, bool) {
	name := userOverlaysFolderName(console)
	for _, o := range list {
		if name != "" && strings.EqualFold(o.Dir, name) {
			return o, true
		}
	}
	return userOverlay{}, false
}

// userOverlayAuto is userOverlaysAutoPick's path, read from the card now: ""
// when there is none. Disk; launch time.
func userOverlayAuto(console string) string {
	if o, ok := userOverlaysAutoPick(userOverlaysFor(console), console); ok {
		return o.Path
	}
	return ""
}

// userOverlayIsOurs reports a path in a console folder of one of the
// Overlays roots the last scan read — not RetroArch's own "overlays" folder,
// which has console folders too. No disk access: the menus ask every frame.
func userOverlayIsOurs(path string) bool {
	if path == "" || !filepath.IsAbs(path) {
		return false
	}
	root := filepath.Clean(filepath.Dir(filepath.Dir(path)))
	if !strings.EqualFold(filepath.Base(root), userOverlaysDirName) {
		return false
	}
	userOverlaysCache.mu.Lock()
	defer userOverlaysCache.mu.Unlock()
	for _, have := range userOverlaysCache.roots {
		if strings.EqualFold(filepath.Clean(have), root) {
			return true
		}
	}
	return false
}

// userOverlayLabel names a user overlay in the menus.
func userOverlayLabel(path string) string {
	return "Your overlay · " + strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

// ── The menus' list ──────────────────────────────────────────────────────

// userOverlaysCache is the list the Settings rows show, filled in the
// background so a frame never waits for the card.
var userOverlaysCache struct {
	mu        sync.Mutex
	at        time.Time
	running   bool
	byConsole map[string][]userOverlay
	roots     []string // the Overlays folders that scan read
}

// userOverlaysCached is a console's overlays as last scanned. A list older
// than userOverlaysFresh starts a rescan; the old one shows until it is in.
// Safe from any goroutine.
func userOverlaysCached(console string) []userOverlay {
	userOverlaysCache.mu.Lock()
	defer userOverlaysCache.mu.Unlock()
	if !userOverlaysCache.running && time.Since(userOverlaysCache.at) >= userOverlaysFresh {
		userOverlaysCache.running = true
		go userOverlaysRescan()
	}
	return userOverlaysCache.byConsole[strings.ToLower(userOverlaysConsole(console))]
}

// userOverlaysRescan reads the folders again and installs the result.
func userOverlaysRescan() {
	defer func() {
		if r := recover(); r != nil {
			logWarn("overlays", "scanning the Overlays folders failed: %v", r)
		}
		userOverlaysCache.mu.Lock()
		userOverlaysCache.running = false
		userOverlaysCache.at = time.Now()
		userOverlaysCache.mu.Unlock()
	}()
	roots := userOverlaysRoots()
	list := userOverlaysScanAll(roots)
	userOverlaysCache.mu.Lock()
	userOverlaysCache.byConsole = list
	userOverlaysCache.roots = roots
	userOverlaysCache.mu.Unlock()
}

// ── The folders ──────────────────────────────────────────────────────────

// userOverlaysMade is the record of the folders JukaHub made once
// ("<Overlays root>\t<console>" lines), loaded on first use.
var userOverlaysMade struct {
	mu       sync.Mutex
	loaded   bool
	set      map[string]bool
	readOnly bool // a folder could not be made: no more tries this run
}

func userOverlaysMadePath() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, userOverlaysMadeRecord)
}

func userOverlaysMadeKey(root, console string) string {
	return strings.ToLower(filepath.Clean(root)) + "\t" + strings.ToLower(console)
}

func userOverlaysMadeLoadLocked() {
	if userOverlaysMade.loaded {
		return
	}
	userOverlaysMade.loaded = true
	userOverlaysMade.set = map[string]bool{}
	path := userOverlaysMadePath()
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimRight(line, "\r"); strings.TrimSpace(line) != "" {
			userOverlaysMade.set[strings.ToLower(line)] = true
		}
	}
}

func userOverlaysMadeSaveLocked() {
	path := userOverlaysMadePath()
	if path == "" {
		return
	}
	lines := make([]string, 0, len(userOverlaysMade.set))
	for k := range userOverlaysMade.set {
		lines = append(lines, k)
	}
	sort.Strings(lines)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		_ = os.Remove(tmp)
		logWarn("overlays", "cannot save %s: %v", path, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		logWarn("overlays", "cannot save %s: %v", path, err)
	}
}

// userOverlaysReadmeText is a console folder's README.txt.
func userOverlaysReadmeText(console string) string {
	size := "the screen's size"
	if hw := deviceHardwareCurrent(); hw.PanelW > 0 && hw.PanelH > 0 {
		size = fmt.Sprintf("the screen's size, %d x %d on this handheld", hw.PanelW, hw.PanelH)
	}
	return strings.Join([]string{
		"Your own overlays for " + console + " games",
		"",
		"Put an overlay picture here (a PNG of " + size + ",",
		"see-through where the game shows) or a RetroArch overlay .cfg with its pictures.",
		"",
		"JukaHub puts it over " + console + " games started from its library.",
		"With more than one, the first by name plays; pick another in",
		"Settings > Emulation > Adjust for " + console + " > Overlay, where None turns it off.",
		"",
	}, "\r\n")
}

// userOverlaysAfterScan makes the folders of the consoles a library scan
// found games for (each once) and refreshes the menus' list. Called on the
// scan's goroutine; roots are the library's Roms folders. Only the walk over
// the games list happens there: the card work runs on a goroutine of its own,
// so the library is never held up.
func userOverlaysAfterScan(roots []string, games []scannedROM) {
	wants := userOverlaysWanted(roots, games)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("overlays", "making the Overlays folders failed: %v", r)
			}
		}()
		if len(wants) > 0 {
			userOverlaysMakeFolders(wants)
		}
		userOverlaysCache.mu.Lock()
		start := !userOverlaysCache.running
		if start {
			userOverlaysCache.running = true
		}
		userOverlaysCache.mu.Unlock()
		if start {
			userOverlaysRescan()
		}
	}()
}

// userOverlaysWanted is the console folders a scan's games ask for: one per
// console and Overlays folder (the one beside the Roms folder the game is
// in). Pure.
func userOverlaysWanted(roots []string, games []scannedROM) []userOverlaysWant {
	var wants []userOverlaysWant
	seen := map[string]bool{}
	lastSystem, lastRoms := "", ""
	for _, g := range games {
		// A scan lists a console's games together: the rest of a run is known.
		if g.System == lastSystem && lastRoms != "" && strings.HasPrefix(g.Path, lastRoms) {
			continue
		}
		lastSystem, lastRoms = g.System, ""
		console := userOverlaysFolderName(g.System)
		if console == "" || userOverlaysSkip(console) {
			continue
		}
		for _, roms := range roots {
			if !filepath.IsAbs(roms) || !strings.EqualFold(filepath.Base(roms), "Roms") {
				continue
			}
			rel, err := filepath.Rel(roms, g.Path)
			if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			lastRoms = roms + string(filepath.Separator)
			root := filepath.Join(filepath.Dir(roms), userOverlaysDirName)
			key := userOverlaysMadeKey(root, console)
			if !seen[key] {
				seen[key] = true
				wants = append(wants, userOverlaysWant{root: root, console: console})
			}
			break
		}
	}
	return wants
}

// userOverlaysWant is a console folder a scan asks for.
type userOverlaysWant struct {
	root    string // the Overlays folder
	console string // the library's console name
}

// userOverlaysMakeFolders makes each wanted folder that was never made and is
// not there under another name, with its README.
func userOverlaysMakeFolders(wants []userOverlaysWant) {
	userOverlaysMade.mu.Lock()
	defer userOverlaysMade.mu.Unlock()
	if userOverlaysMade.readOnly {
		return
	}
	userOverlaysMadeLoadLocked()
	changed := false
	listed := map[string][]os.DirEntry{}
	for _, w := range wants {
		key := userOverlaysMadeKey(w.root, w.console)
		if userOverlaysMade.set[key] || !dirExists(filepath.Dir(w.root)) {
			continue
		}
		entries, ok := listed[w.root]
		if !ok {
			entries = userOverlaysReadDir(w.root, userOverlaysRootMax)
			listed[w.root] = entries
		}
		own := false
		for _, e := range entries {
			if e.IsDir() && userOverlaysSameConsole(e.Name(), w.console) {
				own = true // the player's own folder for it (Overlays/GBA)
				break
			}
		}
		if !own {
			dir := filepath.Join(w.root, w.console)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				logInfo("overlays", "cannot make %s (%v); no Overlays folders this run", dir, err)
				userOverlaysMade.readOnly = true
				break
			}
			readme := filepath.Join(dir, userOverlaysReadme)
			if !regularFile(readme) {
				if err := os.WriteFile(readme, []byte(userOverlaysReadmeText(w.console)), 0o644); err != nil {
					logInfo("overlays", "cannot write %s: %v", readme, err)
				}
			}
			logInfo("overlays", "made %s for your own overlays", dir)
		}
		userOverlaysMade.set[key] = true
		changed = true
	}
	if changed {
		userOverlaysMadeSaveLocked()
	}
}

// ── A picture as an overlay ──────────────────────────────────────────────

// userOverlayCfgText is the RetroArch overlay that shows one picture over the
// whole screen. Pure.
func userOverlayCfgText(png string) string {
	return strings.Join([]string{
		"# Made by JukaHub for " + filepath.Base(png) + " (Overlays folder); written again at each launch.",
		"overlays = 1",
		`overlay0_overlay = "` + filepath.ToSlash(png) + `"`,
		"overlay0_full_screen = true",
		"overlay0_normalized = true",
		"overlay0_descs = 0",
		"",
	}, "\n")
}

// userOverlayCfgPath is the generated .cfg's place for a picture: one file
// per picture path, in JukaHub's data folder.
func userOverlayCfgPath(png string) string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(strings.ToLower(filepath.Clean(png))))
	stem := raTuneSlug(strings.TrimSuffix(filepath.Base(png), filepath.Ext(png)))
	if len(stem) > 40 {
		stem = stem[:40]
	}
	if stem == "" {
		stem = "overlay"
	}
	return filepath.Join(dir, userOverlaysGenDir, fmt.Sprintf("%s-%016x.cfg", stem, h.Sum64()))
}

// userOverlayRetroArchCfg is the .cfg a launch hands RetroArch for an overlay
// choice: a .cfg as it is, a picture through a generated one (written only
// when it changed). "" when the overlay cannot be used.
func userOverlayRetroArchCfg(path string) string {
	// RetroArch's config reader has no escapes: a quote or a line break in the
	// path would end the value early — in the generated .cfg, and in
	// retroarch.cfg's input_overlay, where the launch scope would then read
	// back a value other than the one it wrote and never put the user's back.
	if strings.ContainsAny(path, "\"\r\n") {
		logWarn("overlays", "%s: a quote or line break in the path; RetroArch cannot load it as an overlay", path)
		return ""
	}
	if !strings.EqualFold(filepath.Ext(path), ".png") {
		return path
	}
	cfg := userOverlayCfgPath(path)
	if cfg == "" {
		logWarn("overlays", "no data folder for the overlay of %s", filepath.Base(path))
		return ""
	}
	text := userOverlayCfgText(path)
	if old, err := os.ReadFile(cfg); err == nil && string(old) == text {
		return cfg
	}
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		logWarn("overlays", "cannot make %s: %v", filepath.Dir(cfg), err)
		return ""
	}
	tmp := cfg + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		_ = os.Remove(tmp)
		logWarn("overlays", "cannot write the overlay for %s: %v", filepath.Base(path), err)
		return ""
	}
	if err := os.Rename(tmp, cfg); err != nil {
		_ = os.Remove(tmp)
		logWarn("overlays", "cannot write the overlay for %s: %v", filepath.Base(path), err)
		return ""
	}
	logInfo("overlays", "wrote %s for %s", filepath.Base(cfg), path)
	return cfg
}
