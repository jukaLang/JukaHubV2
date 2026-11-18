package main

// nextui.go — JukaHub on a NextUI card, with everything left where NextUI
// keeps it.
//
// A NextUI (MinUI family) card is laid out like this (NextUI's PAKS.md and
// docs at nextui.loveretro.games; MinUI's PAKS.md and MinUI.pak/launch.sh):
//
//	.system/<platform>/paks/Emus/<TAG>.pak/launch.sh   NextUI's own emulators
//	Emus/<platform>/<TAG>.pak/launch.sh                 extra (user) emulators
//	Roms/<Display Name (TAG)>/…                         games; the TAG picks the pak
//	Bios/<TAG>/, Saves/<TAG>/, Cheats/<TAG>/            per-TAG firmware, saves, cheats
//	.userdata/shared/<TAG>-<core>/<rom>.st0…st9         save states (slot 9 = auto-resume)
//	.userdata/<platform>/<TAG>-<core>/                  per-device core settings
//	.userdata/shared/.minui/recent.txt                  Recently played
//	Roms/<…>/.media/<rom stem>.png (NextUI), .res/<rom file>.png (MinUI)   covers
//	Roms/<…>/map.txt                                    "file<TAB>Display name"; a
//	                                                    name starting "." hides the file
//
// platform is tg5040 on the Smart Pro, Brick and Hammer and tg5050 on the
// Smart Pro S. A pak is started as `<pak>/launch.sh "<absolute rom path>"`
// with the variables NextUI's own launcher exports (PLATFORM, SDCARD_PATH,
// BIOS_PATH, SAVES_PATH, CHEATS_PATH, SYSTEM_PATH, CORES_PATH, USERDATA_PATH,
// SHARED_USERDATA_PATH, LOGS_PATH); the pak's script makes Bios/<TAG> and
// Saves/<TAG> and runs minarch.elf, which reads and writes those same folders.
//
// So a NextUI user keeps every file where it is:
//
//   - The library scan reads "Name (TAG)" folders (romFolderSystem): a known
//     TAG files the games under JukaHub's console, an unknown one is still
//     listed under the folder's own name, map.txt names and hides games.
//   - On NextUI a game in such a folder starts through the TAG's pak (the
//     card's Emus/<platform> first, then NextUI's own), with NextUI's
//     environment (nextuiChildEnv), so saves stay in Saves/<TAG> and states in
//     .userdata — nothing is relocated. Another emulator can still be pinned
//     per game in its options sheet.
//   - Covers already in .media or .res are used first, and only read.
//   - The save manager (and Syncthing through it) sees Saves and the state
//     folders (nextuiSaveRoots).
//   - NextUI's Recently played is copied into JukaHub's Continue once.
//   - Resume: nextuiRequestResume arms minarch's slot-9 resume for the next
//     launch of a game, the way NextUI's own auto-resume does.
//   - NextUI's Tools list gets a JukaHub entry (nextuiInstallToolPak).
//
// Nothing here writes into .system, Roms, Saves or .userdata, except the
// /tmp resume flag minarch consumes, removing NextUI's one-shot auto-resume
// marker when JukaHub resumes that game itself (nextuiTakeAutoResume),
// putting that marker back, word for word, when minarch's wake removed it
// just before Power off after sleeping powers the device off
// (nextui_sleep.go), and the Tools/<platform>/JukaHub.pak entry JukaHub
// creates (and only once).

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// nextuiResumeSlotFile is the flag minarch reads (and deletes) at start:
	// the save-state slot to load. NextUI writes it on Resume.
	nextuiResumeSlotFile = "/tmp/resume_slot.txt"
	// nextuiAutoResumeSlot is minarch's auto-resume slot (.st9).
	nextuiAutoResumeSlot = 9
	// nextuiMapFile is MinUI's per-folder display-name file.
	nextuiMapFile = "map.txt"
	// nextuiMapMaxBytes bounds a map.txt read: a real one is a few kilobytes.
	nextuiMapMaxBytes = 1 << 20
	// nextuiPakCacheTTL is how long a TAG's pak lookup is trusted. The options
	// sheet asks every frame; a pak copied onto the card shows up within it.
	nextuiPakCacheTTL = 30 * time.Second
	// nextuiRecentsMarker is the file in JukaHub's data folder that says
	// NextUI's Recently played was already copied.
	nextuiRecentsMarker = "nextui-recents-imported"
	// nextuiToolPakName is JukaHub's entry in NextUI's Tools list.
	nextuiToolPakName = "JukaHub.pak"
)

// nextuiTagSystems are the NextUI TAGs romSystemAliases does not name: the
// extra cores' tags (MGBA, SUPA, FBN, SGB) and systems JukaHub has no core
// for but NextUI paks play.
var nextuiTagSystems = map[string]romSystem{
	"mgba":   {"Game Boy Advance", "Nintendo - Game Boy Advance"},
	"supa":   {"SNES", "Nintendo - Super Nintendo Entertainment System"},
	"sgb":    {"Game Boy", "Nintendo - Game Boy"},
	"fbn":    {"Arcade", "MAME"},
	"p8":     {"PICO-8", "PICO-8"},
	"pkm":    {"Pokemon Mini", "Nintendo - Pokemon Mini"},
	"vb":     {"Virtual Boy", "Nintendo - Virtual Boy"},
	"fds":    {"Famicom Disk System", "Nintendo - Family Computer Disk System"},
	"coleco": {"ColecoVision", "Coleco - ColecoVision"},
	"msx":    {"MSX", "Microsoft - MSX"},
	"c64":    {"Commodore 64", "Commodore - 64"},
	"cpc":    {"Amstrad CPC", "Amstrad - CPC"},
	"gw":     {"Game & Watch", "Handheld Electronic Game"},
}

// nextuiFolderTag splits a MinUI-family console folder name, "Game Boy
// Advance (GBA)", into its display name and TAG. The TAG is what MinUI's
// getEmuName reads: from the last "(" to the ")" after it, so "Game Boy (GB)
// Hacks" is GB and "Arcade (FBN) (2024)" is 2024. It must be a bare word
// (letters, digits, "_" or "-"), so "Hacks (2024 set)" is no tag.
func nextuiFolderTag(name string) (display, tag string, ok bool) {
	name = strings.TrimSpace(name)
	open := strings.LastIndexByte(name, '(')
	if open < 0 {
		return "", "", false
	}
	end := strings.IndexByte(name[open+1:], ')')
	if end < 0 {
		return "", "", false
	}
	tag = strings.TrimSpace(name[open+1 : open+1+end])
	if tag == "" || len(tag) > 16 {
		return "", "", false
	}
	for _, r := range tag {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return "", "", false
		}
	}
	display = strings.TrimSpace(name[:open])
	if display == "" {
		display = tag
	}
	return display, tag, true
}

// nextuiTagSystem is the JukaHub console a NextUI TAG plays.
func nextuiTagSystem(tag string) (romSystem, bool) {
	key := strings.ToLower(strings.TrimSpace(tag))
	if key == "" {
		return romSystem{}, false
	}
	if s, ok := romSystemAliases[key]; ok {
		return s, true
	}
	s, ok := nextuiTagSystems[key]
	return s, ok
}

// romFolderSystem is the console a folder right under Roms holds: its name in
// the alias table ("GBA", "Sony - PlayStation"), else the TAG of a NextUI
// "Name (TAG)" folder, else a plugin's folder, else — for a TAG nobody knows —
// the folder's display name with no thumbnail collection. tagged reports a
// NextUI-style folder, whose files are listed the way NextUI lists them
// (nextuiListsFile).
func romFolderSystem(name string, pluginByFolder map[string]romSystem) (system romSystem, tagged, ok bool) {
	if s, found := romSystemAliases[strings.ToLower(name)]; found {
		return s, false, true
	}
	display, tag, isTagged := nextuiFolderTag(name)
	if isTagged {
		if s, found := nextuiTagSystem(tag); found {
			return s, true, true
		}
	}
	if s, found := pluginByFolder[romPluginFolderKey(name)]; found {
		return s, isTagged, true
	}
	if isTagged {
		return romSystem{Name: display}, true, true
	}
	return romSystem{}, false, false
}

// nextuiSkipExts are files a NextUI console folder holds beside its games
// that are not games: pictures, notes, saves and lists.
var nextuiSkipExts = map[string]bool{
	".txt": true, ".md": true, ".nfo": true, ".pdf": true, ".xml": true, ".json": true,
	".dat": true, ".db": true, ".ini": true, ".cfg": true, ".log": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".bmp": true, ".webp": true,
	".sav": true, ".srm": true, ".state": true, ".cht": true, ".sh": true,
}

// nextuiListsFile reports whether NextUI would list name in a TAG folder: it
// shows every file (the pak decides what it plays), so a Virtual Boy .vb or a
// Pokemon mini .min is a game even though JukaHub's extension table does not
// know it. Hidden files are skipped before this is asked.
func nextuiListsFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		return false
	}
	if strings.EqualFold(name, nextuiMapFile) {
		return false
	}
	if nextuiSkipExts[ext] {
		return false
	}
	// A state slot (.st0…) or a numbered backup is never a game.
	if len(ext) == 4 && strings.HasPrefix(ext, ".st") && ext[3] >= '0' && ext[3] <= '9' {
		return false
	}
	return true
}

// ── map.txt ───────────────────────────────────────────────────────────────

// nextuiParseMap reads a map.txt: one "file name<TAB>display name" per line.
// Keys are lower-cased (the cards are FAT); a display name starting with "."
// is kept as is, meaning "hidden".
func nextuiParseMap(data []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 4096), 64<<10)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		file, alias, ok := strings.Cut(line, "\t")
		file = strings.TrimSpace(strings.TrimPrefix(file, "\ufeff"))
		alias = strings.TrimSpace(alias)
		if !ok || file == "" || alias == "" {
			continue
		}
		out[strings.ToLower(file)] = alias
	}
	return out
}

// nextuiMapCache holds the map.txt of each folder a scan or a library build
// has read: one read per folder. Not shared between goroutines; each scan
// makes its own.
type nextuiMapCache map[string]map[string]string

// alias is the map.txt name of the file at romPath, if its folder has one.
func (c nextuiMapCache) alias(romPath string) (string, bool) {
	dir := filepath.Dir(romPath)
	m, seen := c[dir]
	if !seen {
		m = nil
		if f, err := os.Open(filepath.Join(dir, nextuiMapFile)); err == nil {
			data, rerr := io.ReadAll(io.LimitReader(f, nextuiMapMaxBytes))
			f.Close()
			if rerr == nil {
				m = nextuiParseMap(data)
			}
		}
		c[dir] = m
	}
	alias, ok := m[strings.ToLower(filepath.Base(romPath))]
	return alias, ok
}

// hidden reports whether map.txt hides the file (a name starting with ".",
// such as a BIOS zip in an arcade folder).
func (c nextuiMapCache) hidden(romPath string) bool {
	alias, ok := c.alias(romPath)
	return ok && strings.HasPrefix(alias, ".")
}

// nextuiApplyMapTitles gives each game without a display title of its own the
// name its folder's map.txt gives it. It reports whether any title changed, so
// the caller can sort again. Only on a NextUI card.
func nextuiApplyMapTitles(games []libraryGame) bool {
	if !nextuiActive() {
		return false
	}
	maps := nextuiMapCache{}
	changed := false
	for i := range games {
		g := &games[i]
		if strings.TrimSpace(g.DisplayTitle) != "" || strings.TrimSpace(g.Path) == "" {
			continue
		}
		if alias, ok := maps.alias(g.Path); ok && !strings.HasPrefix(alias, ".") && alias != g.Title {
			g.DisplayTitle = alias
			changed = true
		}
	}
	return changed
}

// ── Detection ─────────────────────────────────────────────────────────────

// nextuiCardRoot is the card NextUI runs from: the one JukaHub is installed
// on, else /mnt/SDCARD.
func nextuiCardRoot() string {
	if root := strings.TrimSpace(trimuiSDRootFn()); root != "" {
		return filepath.Clean(root)
	}
	return "/mnt/SDCARD"
}

// nextuiPlatform is NextUI's platform folder on this card ("tg5040",
// "tg5050"), or "" when the card has no NextUI. NextUI's own PLATFORM wins
// when JukaHub was started from its Tools list; else the only platform the
// card carries; else the one this model runs.
var nextuiPlatform = sync.OnceValue(func() string {
	if IsWindows() {
		return ""
	}
	system := filepath.Join(nextuiCardRoot(), ".system")
	has := func(p string) bool { return p != "" && dirExists(filepath.Join(system, p)) }
	if p := strings.TrimSpace(os.Getenv("PLATFORM")); has(p) {
		return p
	}
	var found []string
	for _, p := range []string{"tg5040", "tg5050"} {
		if has(p) {
			found = append(found, p)
		}
	}
	switch len(found) {
	case 0:
		return ""
	case 1:
		return found[0]
	}
	if p, _ := ppssppPlatformFor(ppssppDevice()); has(p) {
		return p
	}
	return found[0]
})

// nextuiActive reports whether JukaHub runs on a NextUI card.
func nextuiActive() bool { return nextuiPlatform() != "" }

// nextuiROMTag is the TAG of the NextUI folder right under Roms that holds
// romPath ("…/Roms/Game Boy (GB)/Hacks/x.gb" is "GB"), or "". The card's
// Roms is the first one from the root: a "roms" folder inside a console
// folder (an arcade set's) is not it.
func nextuiROMTag(romPath string) string {
	parts := strings.FieldsFunc(romPath, func(r rune) bool { return r == '/' || r == '\\' })
	for i := 0; i+2 < len(parts); i++ {
		if strings.EqualFold(parts[i], "roms") {
			if _, tag, ok := nextuiFolderTag(parts[i+1]); ok {
				return tag
			}
			return ""
		}
	}
	return ""
}

// ── Paks ──────────────────────────────────────────────────────────────────

var nextuiPakCache struct {
	mu    sync.Mutex
	byTag map[string]nextuiPakHit
}

type nextuiPakHit struct {
	launch string
	at     time.Time
}

// nextuiPakDirs are the folders NextUI looks for emulator paks in, in its
// order: the card's Emus/<platform> (user paks), then its own .system paks.
func nextuiPakDirs(root, platform string) []string {
	return []string{
		filepath.Join(root, "Emus", platform),
		filepath.Join(root, ".system", platform, "paks", "Emus"),
	}
}

// nextuiPakForTag is the launch.sh of the pak that plays TAG, or "".
func nextuiPakForTag(tag string) string {
	platform := nextuiPlatform()
	if platform == "" || tag == "" {
		return ""
	}
	now := time.Now()
	nextuiPakCache.mu.Lock()
	if hit, ok := nextuiPakCache.byTag[tag]; ok && now.Sub(hit.at) < nextuiPakCacheTTL {
		nextuiPakCache.mu.Unlock()
		return hit.launch
	}
	nextuiPakCache.mu.Unlock()
	launch := ""
	for _, dir := range nextuiPakDirs(nextuiCardRoot(), platform) {
		if p := filepath.Join(dir, tag+".pak", "launch.sh"); regularFile(p) {
			launch = p
			break
		}
	}
	nextuiPakCache.mu.Lock()
	if nextuiPakCache.byTag == nil {
		nextuiPakCache.byTag = map[string]nextuiPakHit{}
	}
	nextuiPakCache.byTag[tag] = nextuiPakHit{launch: launch, at: now}
	nextuiPakCache.mu.Unlock()
	return launch
}

// nextuiPakFor is the pak NextUI itself would start romPath with, or "" when
// JukaHub does not run on NextUI or the game's folder has no TAG with a pak.
func nextuiPakFor(romPath string) string {
	if !nextuiActive() {
		return ""
	}
	return nextuiPakForTag(nextuiROMTag(romPath))
}

// nextuiMissingPakHint is the message for a game in a NextUI TAG folder that
// nothing can start: the pak NextUI itself would need. "" off NextUI, outside
// a TAG folder, or when the pak is there.
func nextuiMissingPakHint(romPath string) string {
	platform := nextuiPlatform()
	tag := nextuiROMTag(romPath)
	if platform == "" || tag == "" || nextuiPakForTag(tag) != "" {
		return ""
	}
	return fmt.Sprintf("no emulator for %s games — NextUI needs %s.pak in Emus/%s on the card", tag, tag, platform)
}

// nextuiIsPakLauncher reports whether launcher is a NextUI pak's launch.sh.
func nextuiIsPakLauncher(launcher string) bool {
	launcher = filepath.Clean(strings.TrimSpace(launcher))
	return strings.EqualFold(filepath.Base(launcher), "launch.sh") &&
		strings.EqualFold(filepath.Ext(filepath.Dir(launcher)), ".pak")
}

// nextuiPakTag is a pak launcher's TAG ("…/GBA.pak/launch.sh" is "GBA").
func nextuiPakTag(launcher string) string {
	dir := filepath.Base(filepath.Dir(filepath.Clean(launcher)))
	return strings.TrimSuffix(dir, filepath.Ext(dir))
}

// nextuiPakLabel is how a pak is named on screen: "GBA.pak (NextUI)".
func nextuiPakLabel(launcher string) string {
	return nextuiPakTag(launcher) + ".pak (NextUI)"
}

// nextuiPakEntry is the catalog row for a pak launcher.
func nextuiPakEntry(launch string) emuEntry {
	tag := nextuiPakTag(launch)
	core := ""
	if s, ok := nextuiTagSystem(tag); ok {
		core = defaultCoreForSystem(s.Name)
	}
	return emuEntry{
		Name:   tag + " (NextUI)",
		Detail: filepath.Dir(launch),
		Status: emuAvailable,
		Launch: launch,
		Glyph:  emuGlyphFor(tag, core),
	}
}

// nextuiDecorateCatalog adapts the handheld catalog to a NextUI card: the
// Emus/<platform> folder is no emulator (it was listed as a broken one), and
// every pak — the card's first, then NextUI's own for the TAGs the card does
// not override — becomes a row, so the options sheet can pin one and the
// Emulators page shows what NextUI plays. Elsewhere entries is unchanged.
func nextuiDecorateCatalog(entries []emuEntry) []emuEntry {
	platform := nextuiPlatform()
	if platform == "" {
		return entries
	}
	root := nextuiCardRoot()
	userDir := filepath.Join(root, "Emus", platform)
	out := entries[:0:0]
	for _, e := range entries {
		if strings.EqualFold(e.Name, platform) && e.Status != emuAvailable && strings.HasPrefix(filepath.Clean(e.Detail), filepath.Clean(userDir)) {
			continue
		}
		// "Games in Roms/Game Boy (GB) — no emulator found" is wrong when
		// GB.pak plays them, and Roms/.media is NextUI's art, not games.
		if e.Status == emuMissing {
			if strings.HasPrefix(e.Name, ".") {
				continue
			}
			if _, tag, ok := nextuiFolderTag(e.Name); ok && nextuiPakForTag(tag) != "" {
				continue
			}
		}
		out = append(out, e)
	}
	seen := map[string]bool{}
	for _, dir := range nextuiPakDirs(root, platform) {
		list, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, de := range list {
			name := de.Name()
			if !de.IsDir() || skipScanEntryName(name) || !strings.EqualFold(filepath.Ext(name), ".pak") {
				continue
			}
			tag := strings.TrimSuffix(name, filepath.Ext(name))
			launch := filepath.Join(dir, name, "launch.sh")
			if seen[strings.ToLower(tag)] || !regularFile(launch) {
				continue
			}
			seen[strings.ToLower(tag)] = true
			out = append(out, nextuiPakEntry(launch))
		}
	}
	return out
}

// nextuiWithPakFirst puts the pak NextUI would start romPath with at the
// front of list (adding it when the catalog lacks it): the options sheet's
// Emulator ladder then offers it beside the others.
func nextuiWithPakFirst(romPath string, list []emuEntry) []emuEntry {
	pak := nextuiPakFor(romPath)
	if pak == "" {
		return list
	}
	out := make([]emuEntry, 0, len(list)+1)
	entry := nextuiPakEntry(pak)
	for _, e := range list {
		if strings.EqualFold(filepath.Clean(e.Launch), filepath.Clean(pak)) {
			entry = e
			continue
		}
		out = append(out, e)
	}
	return append([]emuEntry{entry}, out...)
}

// ── Launching ─────────────────────────────────────────────────────────────

// nextuiChildEnv is environ with NextUI's launcher variables filled in, the
// way its MinUI.pak/launch.sh exports them, for a pak started outside NextUI
// (JukaHub at boot). Variables already set — JukaHub started from NextUI's
// Tools list inherits them — are kept. NextUI's bin and lib folders go first
// on PATH and LD_LIBRARY_PATH, where its launcher puts them, so minarch.elf
// runs against NextUI's libraries; HOME is USERDATA_PATH, as the paks set it.
func nextuiChildEnv(environ []string, root, platform string) []string {
	system := root + "/.system/" + platform
	userdata := root + "/.userdata/" + platform
	shared := root + "/.userdata/shared"
	out := append(make([]string, 0, len(environ)+16), environ...)
	for _, kv := range [][2]string{
		{"PLATFORM", platform},
		{"SDCARD_PATH", root},
		{"BIOS_PATH", root + "/Bios"},
		{"ROMS_PATH", root + "/Roms"},
		{"SAVES_PATH", root + "/Saves"},
		{"CHEATS_PATH", root + "/Cheats"},
		{"SYSTEM_PATH", system},
		{"CORES_PATH", system + "/cores"},
		{"USERDATA_PATH", userdata},
		{"SHARED_USERDATA_PATH", shared},
		{"LOGS_PATH", userdata + "/logs"},
		{"DATETIME_PATH", shared + "/datetime.txt"},
	} {
		if v, ok := childEnvLookup(out, kv[0]); !ok || strings.TrimSpace(v) == "" {
			out = append(out, kv[0]+"="+kv[1])
		}
	}
	prepend := func(key, dir string) {
		value, _ := childEnvLookup(out, key)
		for _, part := range strings.Split(value, childEnvListSep) {
			if filepath.Clean(part) == filepath.Clean(dir) {
				return
			}
		}
		if value != "" {
			dir += childEnvListSep + value
		}
		out = append(out, key+"="+dir)
	}
	prepend("PATH", system+"/bin")
	prepend("LD_LIBRARY_PATH", system+"/lib")
	userdataNow, _ := childEnvLookup(out, "USERDATA_PATH")
	out = append(out, "HOME="+userdataNow)
	return out
}

// nextuiDeviceName is the DEVICE NextUI's launcher exports for a model:
// "brick" for the 1024x768 Brick (and the Hammer, which shares its panel),
// "brickpro" for the Brick Pro, "smartpro" for the Smart Pro (NextUI's
// PAKS.md: on tg5040 DEVICE is brick, brickpro or smartpro). The Smart Pro S
// (tg5050) is left unset: its value is not documented.
func nextuiDeviceName(model DeviceModel) string {
	switch model {
	case DeviceTrimuiBrick, DeviceTrimuiHammer:
		return "brick"
	case DeviceTrimuiBrickPro:
		return "brickpro"
	case DeviceTrimuiSmartPro:
		return "smartpro"
	}
	return ""
}

// nextuiResume is the game the next pak launch should resume (slot 9).
var nextuiResume struct {
	mu   sync.Mutex
	path string
}

// nextuiRequestResume asks the next launch of romPath through its NextUI pak
// to load minarch's auto-resume state, as NextUI's own auto-resume does,
// before the usual launch (launchLibraryGame). quickresume.go asks for it when
// JukaHub, not NextUI, is the first UI at power-on and NextUI's
// auto_resume.txt names a game. A launch of any other game clears it.
func nextuiRequestResume(romPath string) {
	nextuiResume.mu.Lock()
	nextuiResume.path = filepath.Clean(romPath)
	nextuiResume.mu.Unlock()
}

// nextuiTakeResume reports (once) whether romPath was asked to resume.
func nextuiTakeResume(romPath string) bool {
	nextuiResume.mu.Lock()
	defer nextuiResume.mu.Unlock()
	want := nextuiResume.path != "" && strings.EqualFold(nextuiResume.path, filepath.Clean(romPath))
	nextuiResume.path = ""
	return want
}

// nextuiArmResumeSlot writes or clears minarch's resume flag. A stale flag
// would make the next minarch game load slot 9 of whatever it plays, so a
// launch without a resume removes it (the lesson minui-psp learned).
func nextuiArmResumeSlot(resume bool) {
	if !resume {
		if err := os.Remove(nextuiResumeSlotFile); err != nil && !os.IsNotExist(err) {
			logWarn("nextui", "could not clear %s: %v", nextuiResumeSlotFile, err)
		}
		return
	}
	if err := os.WriteFile(nextuiResumeSlotFile, []byte(fmt.Sprintf("%d", nextuiAutoResumeSlot)), 0o644); err != nil {
		logWarn("nextui", "could not arm the resume slot: %v", err)
	}
}

// nextuiPrepareLaunch readies cmd, about to run a NextUI pak, the way NextUI
// would: its environment, its logs folder, and the resume flag (set only when
// nextuiRequestResume asked for this game). Other launchers are left alone.
// It runs before screenChildGroupConfigure, which keeps an Env already set.
func nextuiPrepareLaunch(launcher, romPath string, cmd *exec.Cmd) {
	if cmd == nil || !nextuiIsPakLauncher(launcher) {
		return
	}
	platform := nextuiPlatform()
	if platform == "" {
		return
	}
	root := filepath.ToSlash(nextuiCardRoot())
	env := cmd.Env
	if env == nil {
		env = screenChildEnv(os.Environ())
	}
	cmd.Env = nextuiChildEnv(env, root, platform)
	// NextUI's launcher tells a Brick from a Smart Pro by DEVICE (the same
	// tg5040 platform, another screen); minarch sizes its picture by it.
	if v, ok := childEnvLookup(cmd.Env, "DEVICE"); !ok || strings.TrimSpace(v) == "" {
		if name := nextuiDeviceName(ppssppDevice()); name != "" {
			cmd.Env = append(cmd.Env, "DEVICE="+name)
		}
	}
	if logs, ok := childEnvLookup(cmd.Env, "LOGS_PATH"); ok && logs != "" {
		_ = os.MkdirAll(logs, 0o755)
	}
	resume := nextuiTakeResume(romPath)
	nextuiArmResumeSlot(resume)
	logInfo("nextui", "starting %s through %s (resume %v)", filepath.Base(romPath), nextuiPakLabel(launcher), resume)
}

// nextuiLaunchCommand is the command NextUI would run for romPath — its pak's
// launch.sh with the ROM, in the pak's folder, with NextUI's environment —
// for a caller that starts the game itself. resume arms minarch's slot-9
// state (nextuiArmResumeSlot) right away. The caller still owns process
// groups and screen hand-off (screenChildGroupConfigure, screenChildStart).
func nextuiLaunchCommand(romPath string, resume bool) (*exec.Cmd, error) {
	pak := nextuiPakFor(romPath)
	if pak == "" {
		return nil, fmt.Errorf("no NextUI pak plays %s", filepath.Base(romPath))
	}
	if !regularFile(romPath) {
		return nil, fmt.Errorf("ROM is missing: %s", filepath.Base(romPath))
	}
	name, args := gameLaunchCommand(pak, romPath)
	name, args = firmwareScriptCommand(pak, name, args)
	cmd := exec.Command(name, args...)
	cmd.Dir = filepath.Dir(pak)
	if resume {
		nextuiRequestResume(romPath)
	}
	nextuiPrepareLaunch(pak, romPath, cmd)
	return cmd, nil
}

// nextuiResumeStatePresent reports whether minarch left an auto-resume state
// (slot 9) for romPath: .userdata/shared/<TAG>-<core>/<rom file>.st9.
func nextuiResumeStatePresent(romPath string) bool {
	_, ok := nextuiResumeStateTime(romPath)
	return ok
}

// nextuiResumeStateTime is when minarch last wrote romPath's auto-resume
// state (the newest, should two cores hold one), and whether there is one.
func nextuiResumeStateTime(romPath string) (time.Time, bool) {
	tag := nextuiROMTag(romPath)
	if tag == "" || !nextuiActive() {
		return time.Time{}, false
	}
	// Only the folder part is a pattern: a ROM named "Tetris [!].gb" would be
	// one too (the TAG is a bare word, so tag+"-*" is safe).
	dirs, _ := filepath.Glob(filepath.Join(nextuiCardRoot(), ".userdata", "shared", tag+"-*"))
	state := filepath.Base(romPath) + fmt.Sprintf(".st%d", nextuiAutoResumeSlot)
	var newest time.Time
	found := false
	for _, dir := range dirs {
		info, err := os.Stat(filepath.Join(dir, state))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if !found || info.ModTime().After(newest) {
			newest, found = info.ModTime(), true
		}
	}
	return newest, found
}

// nextuiTakeAutoResume reads NextUI's one-shot auto-resume marker
// (.userdata/shared/.minui/auto_resume.txt, the game's path from the card
// root, written when minarch powers the device off in a game) and removes it,
// exactly as NextUI does when it acts on it. ok is false when there is none
// or the game is gone; the marker is then left alone.
func nextuiTakeAutoResume() (romPath string, ok bool) {
	romPath, marker, ok := nextuiPeekAutoResume()
	if !ok {
		return "", false
	}
	if err := os.Remove(marker); err != nil {
		logWarn("nextui", "could not remove the auto-resume marker: %v", err)
	}
	return romPath, true
}

// nextuiPeekAutoResume is nextuiTakeAutoResume without the removal: the game
// and the marker's own path, for a caller that removes it later
// (quickresume.go removes it only once the start is known to go on).
func nextuiPeekAutoResume() (romPath, marker string, ok bool) {
	if !nextuiActive() {
		return "", "", false
	}
	marker = nextuiAutoResumePath()
	data, err := nextuiReadSmall(marker, 4096)
	if err != nil {
		return "", "", false
	}
	romPath, ok = nextuiAutoResumeROM(data)
	if !ok {
		return "", "", false
	}
	return romPath, marker, true
}

// nextuiAutoResumePath is NextUI's one-shot auto-resume marker on this card.
func nextuiAutoResumePath() string {
	return filepath.Join(nextuiCardRoot(), ".userdata", "shared", ".minui", "auto_resume.txt")
}

// nextuiAutoResumeROM is the game an auto-resume marker's text names: its
// first line, a path from the card root. ok is false for an empty marker, a
// path that leaves the card, or a game that is gone.
func nextuiAutoResumeROM(data []byte) (string, bool) {
	root := nextuiCardRoot()
	rel := strings.TrimSpace(strings.SplitN(string(data), "\n", 2)[0])
	if rel == "" {
		return "", false
	}
	romPath := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(rel, "/")))
	if !isPathWithin(romPath, root) || !regularFile(romPath) {
		return "", false
	}
	return romPath, true
}

// nextuiROMSystem is the console the library files romPath under: its
// "Name (TAG)" folder right under Roms, read by romFolderSystem; "" outside
// one.
func nextuiROMSystem(romPath string) string {
	parts := strings.FieldsFunc(romPath, func(r rune) bool { return r == '/' || r == '\\' })
	for i := 0; i+2 < len(parts); i++ {
		if strings.EqualFold(parts[i], "roms") {
			if s, _, ok := romFolderSystem(parts[i+1], nil); ok {
				return s.Name
			}
			return ""
		}
	}
	return ""
}

// nextuiStartedByNextUI reports whether NextUI started this JukaHub (its
// Tools entry sets JUKAHUB_HOST=nextui; NextUI's launcher exports
// SHARED_USERDATA_PATH to everything it runs, auto.sh included) — the same
// test launch.sh uses. NextUI then was the first UI of this power-on and acts
// on its own auto-resume marker itself.
func nextuiStartedByNextUI() bool {
	return os.Getenv("JUKAHUB_HOST") == "nextui" || os.Getenv("SHARED_USERDATA_PATH") != ""
}

// ── minarch and the power key ─────────────────────────────────────────────
//
// minarch reads the power key itself (MinUI's PWR_update, kept by NextUI): a
// press sleeps (on the tg5040, NextUI's deep sleep), a hold — or staying
// asleep past NextUI's timeout — powers off. Its power off writes the slot-9
// state and auto_resume.txt, removes /tmp/nextui_exec, touches /tmp/poweroff
// and exits (PLAT_powerOff); NextUI's own launch loop (MinUI.pak/launch.sh)
// finds the flag when the pak returns and powers the device off. Under
// JukaHub the pak returns to JukaHub, so JukaHub is that loop: it leaves the
// key alone while a pak game holds the screen (powerkey.go asks
// nextuiPakOwnsPowerKey) and powers off when the flag appeared during the
// game (nextuiPakExited).

// nextuiPowerOffFlag is the file minarch leaves for NextUI's loop when it
// powers the device off.
const nextuiPowerOffFlag = "/tmp/poweroff"

// nextuiPakKeyGrace is how long after a pak game ended the power key is still
// minarch's: the press or hold that ended it is not JukaHub's to act on.
const nextuiPakKeyGrace = 3 * time.Second

// nextuiPaks are the NextUI pak games running now, each with whether the
// power-off flag was already there when it started (its presence at the end
// then proves nothing), and when the last one ended.
var nextuiPaks struct {
	mu      sync.Mutex
	running map[*exec.Cmd]bool
	endedAt time.Time
}

// nextuiPakStarted notes a NextUI pak game that just took the screen
// (launchLibraryGame, after screenChildStart). Other launchers are ignored.
func nextuiPakStarted(launcher string, cmd *exec.Cmd) {
	if cmd == nil || !nextuiIsPakLauncher(launcher) || !nextuiActive() {
		return
	}
	stale := regularFile(nextuiPowerOffFlag)
	nextuiPaks.mu.Lock()
	if nextuiPaks.running == nil {
		nextuiPaks.running = map[*exec.Cmd]bool{}
	}
	nextuiPaks.running[cmd] = stale
	nextuiPaks.mu.Unlock()
	if stale {
		logWarn("nextui", "%s was already there before this game; a power off from it will not be acted on", nextuiPowerOffFlag)
	}
	// minarch's own sleep gets Power off after sleeping too (nextui_sleep.go).
	nextuiSleepGuardStart()
}

// nextuiPakGames are the NextUI pak games running now. Any goroutine.
func nextuiPakGames() []*exec.Cmd {
	nextuiPaks.mu.Lock()
	defer nextuiPaks.mu.Unlock()
	out := make([]*exec.Cmd, 0, len(nextuiPaks.running))
	for cmd := range nextuiPaks.running {
		out = append(out, cmd)
	}
	return out
}

// nextuiPakOwnsPowerKey reports whether the power key is minarch's now: a
// NextUI pak game holds the screen, or one ended a moment ago. JukaHub acting
// on it too would sleep the device a second time on top of minarch's sleep.
// Any goroutine.
func nextuiPakOwnsPowerKey() bool {
	nextuiPaks.mu.Lock()
	defer nextuiPaks.mu.Unlock()
	if len(nextuiPaks.running) > 0 {
		return true
	}
	return !nextuiPaks.endedAt.IsZero() && time.Since(nextuiPaks.endedAt) < nextuiPakKeyGrace
}

// nextuiPakExited is the end of a pak game (launchLibraryGame's reaper, after
// the launch's own cleanup). When the power-off flag appeared during the game,
// minarch powered off: JukaHub removes the flag — a NextUI loop that started
// JukaHub must not power off a second time behind it — and powers off through
// Power off's own path (devPowerRequest: the clean teardown, sync, poweroff),
// without the chime, as the sleep's own power off. The state and NextUI's
// auto-resume marker are minarch's, written before it exited. The reaper
// goroutine.
func nextuiPakExited(cmd *exec.Cmd) {
	nextuiPaks.mu.Lock()
	stale, known := nextuiPaks.running[cmd]
	delete(nextuiPaks.running, cmd)
	if known {
		nextuiPaks.endedAt = time.Now()
	}
	last := known && len(nextuiPaks.running) == 0
	nextuiPaks.mu.Unlock()
	if last {
		// No minarch sleep left to watch: its wake alarm goes, before any
		// power off below (nextui_sleep.go).
		nextuiSleepGuardDisarm()
	}
	if !known || stale || !regularFile(nextuiPowerOffFlag) {
		return
	}
	if err := os.Remove(nextuiPowerOffFlag); err != nil && !os.IsNotExist(err) {
		logWarn("nextui", "could not remove %s: %v", nextuiPowerOffFlag, err)
	}
	if !devPowerSupported() {
		logWarn("nextui", "minarch asked for a power off, but JukaHub cannot power this device off")
		return
	}
	logInfo("nextui", "minarch powered the game off (%s): powering off", nextuiPowerOffFlag)
	quickPowerOff.silent.Store(true)
	devPowerRequest(devPowerPoweroff)
}

// ── Saves ─────────────────────────────────────────────────────────────────

// nextuiSaveRoots are a MinUI-family card's save folders for the save manager
// and Syncthing: Saves (in-game saves, Saves/<TAG>) and every state folder,
// .userdata/shared/<TAG>-<core> — named by folder so each keeps its own sync
// key — plus a .userdata/<platform>/<TAG>-<core> that holds states (older
// MinUI builds kept them there). .minui (recents, the auto-resume marker) and
// the per-device settings are left out: syncing the resume marker would make
// another handheld resume this one's game. Only on a card with NextUI's
// .system (a .userdata left over from an old MinUI install on a CrossMix card
// is not one); paths that do not exist are dropped by the caller.
func nextuiSaveRoots(sdRoot string) []saveRoot {
	if sdRoot == "" {
		return nil
	}
	userdata := filepath.Join(sdRoot, ".userdata")
	if !dirExists(filepath.Join(sdRoot, ".system")) {
		return nil
	}
	roots := []saveRoot{{Name: "NextUI saves", Path: filepath.Join(sdRoot, "Saves")}}
	if list, err := os.ReadDir(filepath.Join(userdata, "shared")); err == nil {
		for _, de := range list {
			if de.IsDir() && !skipScanEntryName(de.Name()) && strings.Contains(de.Name(), "-") {
				roots = append(roots, saveRoot{Name: "NextUI states (" + de.Name() + ")", Path: filepath.Join(userdata, "shared", de.Name())})
			}
		}
	}
	for _, platform := range []string{"tg5040", "tg5050"} {
		list, err := os.ReadDir(filepath.Join(userdata, platform))
		if err != nil {
			continue
		}
		for _, de := range list {
			if !de.IsDir() || skipScanEntryName(de.Name()) || !strings.Contains(de.Name(), "-") {
				continue
			}
			dir := filepath.Join(userdata, platform, de.Name())
			if matches, _ := filepath.Glob(filepath.Join(dir, "*.st[0-9]")); len(matches) > 0 {
				roots = append(roots, saveRoot{Name: "NextUI states (" + platform + " " + de.Name() + ")", Path: dir})
			}
		}
	}
	return roots
}

// ── Startup: Tools entry, Recently played ─────────────────────────────────

// nextuiStartup runs once at launch, off the render thread: on a NextUI card
// it adds JukaHub to NextUI's Tools list and copies NextUI's Recently played
// into Continue the first time.
func nextuiStartup() {
	if !nextuiActive() {
		return
	}
	nextuiInstallToolPak()
	nextuiImportRecentsOnce()
}

// nextuiToolPakScript is JukaHub's Tools entry: it starts JukaHub's own
// launch.sh, which takes the screen and hands it back when JukaHub quits.
// JUKAHUB_HOST=nextui tells launch.sh that NextUI's own loop brings its menu
// back, so it never starts the stock MainUI over it.
func nextuiToolPakScript(appDir string) string {
	return "#!/bin/sh\n" +
		"# Written by JukaHub: opens JukaHub from NextUI's Tools list.\n" +
		"# Delete this JukaHub.pak folder to remove the entry; JukaHub does not add it again.\n" +
		"cd " + shellQuote(appDir) + " || exit 1\n" +
		"JUKAHUB_HOST=nextui\n" +
		"export JUKAHUB_HOST\n" +
		"exec sh ./launch.sh\n"
}

// nextuiInstallToolPak creates Tools/<platform>/JukaHub.pak once, when
// JukaHub is installed in <card>/Apps/JukaHub. An existing pak (the user's
// own, or one they edited) is never touched, and the marker in JukaHub's data
// folder keeps a deleted entry deleted.
func nextuiInstallToolPak() {
	platform := nextuiPlatform()
	appDir, err := trimuiAppDir()
	if platform == "" || err != nil || !regularFile(filepath.Join(appDir, "launch.sh")) {
		return
	}
	data, derr := P().DataDir()
	if derr != nil || data == "" {
		return
	}
	marker := filepath.Join(data, "nextui-tool-pak-written")
	if regularFile(marker) {
		return
	}
	pak := filepath.Join(nextuiCardRoot(), "Tools", platform, nextuiToolPakName)
	script := filepath.Join(pak, "launch.sh")
	if !regularFile(script) {
		if err := os.MkdirAll(pak, 0o755); err != nil {
			logWarn("nextui", "could not add JukaHub to NextUI's Tools: %v", err)
			return
		}
		if err := AtomicWrite(script, []byte(nextuiToolPakScript(filepath.ToSlash(appDir))), 0o755); err != nil {
			logWarn("nextui", "could not add JukaHub to NextUI's Tools: %v", err)
			return
		}
		logInfo("nextui", "added JukaHub to NextUI's Tools list (%s)", pak)
	}
	_ = AtomicWrite(marker, []byte("1\n"), 0o644)
}

// nextuiParseRecents reads NextUI's recent.txt: one game per line, its path
// from the card root ("/Roms/Game Boy (GB)/Tetris.gb"), optionally followed by
// a TAB and a display name. Newest first; at most max paths.
func nextuiParseRecents(data []byte, max int) []string {
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		path, _, _ := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		out = append(out, path)
		if len(out) >= max {
			break
		}
	}
	return out
}

// errNextuiTooBig is nextuiReadSmall's answer for a file over its limit.
var errNextuiTooBig = errors.New("file too large")

// nextuiReadSmall reads a NextUI bookkeeping file of at most max bytes.
func nextuiReadSmall(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, errNextuiTooBig
	}
	return data, nil
}

// nextuiImportRecentsOnce copies NextUI's Recently played into JukaHub's
// recents (Continue) the first time JukaHub runs on the card: after the
// games JukaHub already knows, newest first, only games still on the card.
// NextUI's file is only read.
func nextuiImportRecentsOnce() {
	data, err := P().DataDir()
	if err != nil || data == "" {
		return
	}
	marker := filepath.Join(data, nextuiRecentsMarker)
	if regularFile(marker) {
		return
	}
	root := nextuiCardRoot()
	// A damaged FAT entry can claim gigabytes: never read more than a list's
	// worth, and a file too big to be one counts as none.
	raw, rerr := nextuiReadSmall(filepath.Join(root, ".userdata", "shared", ".minui", "recent.txt"), nextuiMapMaxBytes)
	if rerr != nil {
		if os.IsNotExist(rerr) || errors.Is(rerr, errNextuiTooBig) {
			_ = AtomicWrite(marker, []byte("0\n"), 0o644)
		}
		return
	}
	var imported []recentGame
	for _, rel := range nextuiParseRecents(raw, maxRecentGames) {
		path := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(rel, "/")))
		if !isPathWithin(path, root) || !regularFile(path) {
			continue
		}
		folder := ""
		parts := strings.Split(filepath.ToSlash(strings.TrimPrefix(rel, "/")), "/")
		if len(parts) >= 3 && strings.EqualFold(parts[0], "Roms") {
			folder = parts[1]
		}
		system, _, ok := romFolderSystem(folder, nil)
		title := gameDisplayTitle(path)
		if !ok || title == "" {
			continue
		}
		imported = append(imported, recentGame{Path: filepath.Clean(path), System: system.Name, Title: title})
	}
	gameRecentsMu.Lock()
	if !gameRecentsLoaded {
		gameRecents = loadGameRecentsFile(gameRecentsFile())
		gameRecentsLoaded = true
	}
	have := map[string]bool{}
	oldest := gameClock()
	for _, it := range gameRecents {
		have[strings.ToLower(it.Path)] = true
		if !it.PlayedAt.IsZero() && it.PlayedAt.Before(oldest) {
			oldest = it.PlayedAt
		}
	}
	next := append([]recentGame(nil), gameRecents...)
	added := 0
	for _, it := range imported {
		if len(next) >= maxRecentGames || have[strings.ToLower(it.Path)] {
			continue
		}
		have[strings.ToLower(it.Path)] = true
		added++
		// No times in recent.txt: each one a minute older than the last, so
		// the order NextUI showed is kept.
		it.PlayedAt = oldest.Add(-time.Duration(added) * time.Minute)
		next = append(next, it)
	}
	var serr error
	if added > 0 {
		if serr = saveGameRecentsFile(gameRecentsFile(), next); serr == nil {
			gameRecents = next
		}
	}
	gameRecentsMu.Unlock()
	if serr != nil {
		logWarn("nextui", "could not copy NextUI's Recently played: %v", serr)
		return
	}
	if added > 0 {
		logInfo("nextui", "copied %d games from NextUI's Recently played into Continue", added)
	}
	_ = AtomicWrite(marker, []byte("1\n"), 0o644)
}
