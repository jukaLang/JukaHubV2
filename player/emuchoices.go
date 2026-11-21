package main

// emuchoices.go — every emulator a console has, offered when one of its games
// starts.
//
// A TrimUI owner on Reddit (2026-11) asked how to play a game in another core:
// MainUI keeps a console's other emulators behind Select on the game list
// (the "launchlist" of the console's Emus/<system>/config.json — two entries on
// the stock firmware, more on CrossMix), and cores dropped into RetroArch's
// cores folder by hand have no launch script, so the firmware never uses them.
// JukaHub had the same two blind spots: the catalog took only the first
// launcher of a folder (emuFolderLauncher), and on the handheld a core row was
// a tile, never something a game could start in. Installing a core from the
// Emulators page for a console whose launch script names another one changed
// nothing a game started in.
//
// So the emulator picker (emupicker.go) and the game options' Emulator row now
// list, after the console's own launchers:
//
//   - the folder's alternatives: every other .sh script its launchlist names,
//     inside the folder (the rule emuFolderLauncher applies), named the way the
//     launchlist names it ("N64 · Mupen64Plus Standalone");
//   - on the handheld, every installed RetroArch core that plays the console
//     and that none of the listed scripts already starts. Such a core is
//     started directly — RetroArch from its own folder with -L, HOME pointing
//     there as every firmware launch.sh does (emuCoreChoicePrepareLaunch) — and
//     a core built for another architecture than that RetroArch (a 32-bit core
//     beside ra64.trimui) is not offered.
//
// At first these extras changed nothing by themselves: the question opened
// only for two launchers of the console's own. Since 2026-11-18 (Juka: "ask
// which core to use when launching if more than one core") every choice
// counts, so a game with one launcher and an installed core asks — the first
// time, by default, and then remembers (emupicker.go, emuPickAsks and the "Ask
// which emulator" setting); "Get more cores" (emumorecores.go) fills the list.
// The recommendation and the plain default (emuBestPick) still only look at
// the extras when a console has nothing else, so the highlight and a start
// that does not ask stay on the console's own emulator. Picking is per game
// (the options row, the sheet's A or Y) or, from the sheet's X, for every
// game of the console (game-launchers.json, emuConsoleLauncherSet; not for a
// console split over several folders of its own, like Arcade's CPS2 and MAME
// sets); the options row's Left takes that back.
//
// A core started directly gets none of the per-core settings a firmware ships
// for its own cores. For the consoles this chip only just runs (N64,
// Dreamcast, PSP — systemCompatTier "mixed") a core that has never run here
// (no options file anywhere) starts with the handheld's speed settings, the
// ones the Performance panel's boost plan uses (addSystemBoostOptions), written
// as its options file: RetroArch reads it like one it wrote itself, and any
// change made in RetroArch's own menu replaces it.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// The kinds of extra choice an emuEntry can be (emuEntry.Extra).
const (
	emuChoiceAlt  = "alt"  // another launcher the folder's launchlist names
	emuChoiceCore = "core" // an installed RetroArch core started directly
)

// emuFolderAlt is one launchlist entry: the name it carries ("" when none) and
// the launcher it names, as written.
type emuFolderAlt struct {
	Name   string
	Launch string
}

// emuConfigLaunchList lists a parsed config.json's launchlist with the names
// its entries carry, in file order. An entry is either a plain string (the
// launcher, no name) or an object with "launch" and a "name" ("label" and
// "title" are read too). An entry of another shape is skipped.
func emuConfigLaunchList(fields map[string]json.RawMessage) []emuFolderAlt {
	var list []json.RawMessage
	if json.Unmarshal(emuConfigField(fields, "launchlist"), &list) != nil {
		return nil
	}
	var out []emuFolderAlt
	for _, item := range list {
		var launch string
		if json.Unmarshal(item, &launch) == nil {
			if launch = strings.TrimSpace(launch); launch != "" {
				out = append(out, emuFolderAlt{Launch: launch})
			}
			continue
		}
		var entry map[string]json.RawMessage
		if json.Unmarshal(item, &entry) != nil {
			continue
		}
		launch = ""
		if json.Unmarshal(emuConfigField(entry, "launch"), &launch) != nil || strings.TrimSpace(launch) == "" {
			continue
		}
		name := ""
		for _, key := range []string{"name", "label", "title"} {
			var s string
			if json.Unmarshal(emuConfigField(entry, key), &s) == nil && strings.TrimSpace(s) != "" {
				name = strings.TrimSpace(s)
				break
			}
		}
		out = append(out, emuFolderAlt{Name: name, Launch: strings.TrimSpace(launch)})
	}
	return out
}

// emuAltCache keeps each folder's parsed launchlist per config.json version,
// so a picker, the options sheet and a launch never parse one file twice.
var emuAltCache struct {
	mu sync.Mutex
	m  map[string]emuAltCached
}

type emuAltCached struct {
	size int64
	mod  time.Time
	list []emuFolderAlt
}

// emuFolderLaunchList is folder's launchlist, read once per config.json
// version; nil when the folder has no config.json or no launchlist.
func emuFolderLaunchList(folder string) []emuFolderAlt {
	info, err := os.Stat(filepath.Join(folder, "config.json"))
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	emuAltCache.mu.Lock()
	if c, ok := emuAltCache.m[folder]; ok && c.size == info.Size() && c.mod.Equal(info.ModTime()) {
		emuAltCache.mu.Unlock()
		return c.list
	}
	emuAltCache.mu.Unlock()
	list := emuConfigLaunchList(emuFolderConfig(folder))
	emuAltCache.mu.Lock()
	if emuAltCache.m == nil {
		emuAltCache.m = map[string]emuAltCached{}
	}
	emuAltCache.m[folder] = emuAltCached{size: info.Size(), mod: info.ModTime(), list: list}
	emuAltCache.mu.Unlock()
	return list
}

// emuAltNameFromScript turns a launcher file name into a readable name when
// the launchlist gives none: "mupen64plus_standalone.sh" is "Mupen64plus
// standalone".
func emuAltNameFromScript(file string) string {
	name := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	name = strings.Join(strings.FieldsFunc(name, func(r rune) bool { return r == '_' || r == '-' || r == ' ' }), " ")
	if name == "" {
		return filepath.Base(file)
	}
	r, n := utf8.DecodeRuneInString(name)
	return string(unicode.ToUpper(r)) + name[n:]
}

// emuFolderAlternatives is every other launcher own's folder offers: the .sh
// scripts its launchlist names that are inside the folder, exist and are not
// own's launcher, each once, as picker rows that follow own.
func emuFolderAlternatives(own emuEntry) []emuEntry {
	folder := strings.TrimSpace(own.Folder)
	if folder == "" {
		return nil
	}
	list := emuFolderLaunchList(folder)
	if len(list) == 0 {
		return nil
	}
	seen := map[string]bool{strings.ToLower(filepath.Clean(strings.TrimSpace(own.Launch))): true}
	var out []emuEntry
	for _, alt := range list {
		if !strings.EqualFold(filepath.Ext(alt.Launch), ".sh") {
			continue
		}
		path, err := resolveWithinRoot(folder, alt.Launch)
		if err != nil || !regularFile(path) {
			continue
		}
		key := strings.ToLower(filepath.Clean(path))
		if seen[key] {
			continue
		}
		seen[key] = true
		name := alt.Name
		if name == "" {
			name = emuAltNameFromScript(path)
		}
		out = append(out, emuEntry{
			Name:     own.Name + " · " + name,
			Detail:   folder + " · " + filepath.Base(path),
			Status:   emuAvailable,
			Launch:   path,
			Folder:   folder,
			Extra:    emuChoiceAlt,
			IconPath: own.IconPath,
			Glyph:    own.Glyph,
		})
	}
	return out
}

// ── Cores started directly ───────────────────────────────────────────────

// emuCoreAlsoPlays are the consoles a core plays besides the one emuCoreInfo
// names for it, by core-name prefix: the Game Boy cores play Game Boy Color,
// mGBA plays both, Genesis Plus GX the 8-bit Segas.
var emuCoreAlsoPlays = []struct {
	prefix  string
	systems []string
}{
	{"gambatte", []string{"Game Boy Color"}},
	{"sameboy", []string{"Game Boy Color"}},
	{"gearboy", []string{"Game Boy Color"}},
	{"tgbdual", []string{"Game Boy Color"}},
	{"mgba", []string{"Game Boy", "Game Boy Color"}},
	{"vbam", []string{"Game Boy", "Game Boy Color"}},
	{"genesis_plus_gx", []string{"Master System", "Game Gear", "Sega CD"}},
	{"picodrive", []string{"Master System", "Sega CD", "32X"}},
	{"gearsystem", []string{"Game Gear"}},
	{"smsplus", []string{"Game Gear"}},
	{"fbneo", []string{"Neo Geo"}},
	{"beetle_ngp", []string{"Neo Geo Pocket Color"}},
	{"mednafen_ngp", []string{"Neo Geo Pocket Color"}},
	{"race", []string{"Neo Geo Pocket Color"}},
	{"beetle_wswan", []string{"WonderSwan Color"}},
	{"mednafen_wswan", []string{"WonderSwan Color"}},
	{"dolphin", []string{"Wii"}},
}

// emuCoreChoicePlays reports whether core plays system's games.
func emuCoreChoicePlays(core, system string) bool {
	want := canonicalCompatSystem(system)
	core = normalizeCoreName(core)
	if want == "" || core == "" {
		return false
	}
	if sys, _ := emuCoreInfo(core); sys != "" && strings.EqualFold(canonicalCompatSystem(sys), want) {
		return true
	}
	for _, row := range emuCoreAlsoPlays {
		if !strings.HasPrefix(core, row.prefix) {
			continue
		}
		for _, s := range row.systems {
			if strings.EqualFold(canonicalCompatSystem(s), want) {
				return true
			}
		}
	}
	return false
}

// emuCoreChoiceName is how the picker names a core: the ranking's name for it
// ("ParaLLEl N64 (RetroArch)"), else its options name with "(RetroArch)".
func emuCoreChoiceName(core, system string) string {
	core = normalizeCoreName(core)
	cands := emuBestCandidatesFor(system)
	// An exact name before a prefix: snes9x2005 is not "Snes9x (RetroArch)".
	if i := emuBestCoreRank(core, cands); i >= 0 && cands[i].Core == core {
		return cands[i].Name
	}
	return coreConfigDisplayName(core) + " (RetroArch)"
}

// emuArchCache remembers, per core file and RetroArch binary version, whether
// the core is built for that RetroArch's architecture.
var emuArchCache struct {
	mu sync.Mutex
	m  map[string]emuArchCached
}

type emuArchCached struct {
	size   int64
	mod    time.Time
	raSize int64
	raMod  time.Time
	why    string // "" when it fits
}

// emuCoreArchProblem says why RetroArch cannot load core ("built for 32-bit
// ARM; this RetroArch is 64-bit ARM (aarch64)"), or "" when it can or when
// either file is not an ELF this check can read. Only the ELF headers are read:
// the full load check (coreLoadProblemFile) runs when a game that started in
// the core stops at once (gamediag.go).
func emuCoreArchProblem(core, retroArch string) string {
	info, err := os.Stat(core)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	raInfo, err := os.Stat(retroArch)
	if err != nil || !raInfo.Mode().IsRegular() {
		return ""
	}
	key := core + "\x00" + retroArch
	emuArchCache.mu.Lock()
	if c, ok := emuArchCache.m[key]; ok && c.size == info.Size() && c.mod.Equal(info.ModTime()) &&
		c.raSize == raInfo.Size() && c.raMod.Equal(raInfo.ModTime()) {
		emuArchCache.mu.Unlock()
		return c.why
	}
	emuArchCache.mu.Unlock()
	why := ""
	if raClass, raMachine, ok := elfArchOfFile(retroArch); ok {
		if class, machine, ok := elfArchOfFile(core); ok && (class != raClass || machine != raMachine) {
			why = "built for " + elfArchName(class, machine) + "; this RetroArch is " + elfArchName(raClass, raMachine)
		}
	}
	emuArchCache.mu.Lock()
	if emuArchCache.m == nil {
		emuArchCache.m = map[string]emuArchCached{}
	}
	emuArchCache.m[key] = emuArchCached{size: info.Size(), mod: info.ModTime(), raSize: raInfo.Size(), raMod: raInfo.ModTime(), why: why}
	emuArchCache.mu.Unlock()
	if why != "" {
		logInfo("emulators", "not offering %s for games: %s", filepath.Base(core), why)
	}
	return why
}

// emuScriptNamesCore reports whether a RetroArch script's lower-cased text
// gives core as a variable's value — CORE=mupen64plus_next, core="fbneo" — the
// way a launcher names its core when "${CORE}_libretro.so" builds the file
// name (coreNamesIn finds the literal file names). A folder in a path
// (/Roms/MAME/) or a longer name (snes9x in snes9x2005) does not count.
func emuScriptNamesCore(text, core string) bool {
	if text == "" || core == "" {
		return false
	}
	word := func(b byte) bool {
		return b == '_' || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
	}
	for from := 0; from < len(text); {
		i := strings.Index(text[from:], core)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(core)
		assigned := start > 0 && text[start-1] == '='
		if !assigned && start > 1 && (text[start-1] == '"' || text[start-1] == '\'') && text[start-2] == '=' {
			assigned = true
		}
		if assigned && (end == len(text) || !word(text[end])) {
			return true
		}
		from = start + 1
	}
	return false
}

// emuCoreChoices is every installed core in entries (the handheld catalog's
// core rows, emuEntry.CorePath) that plays system and that none of listed
// starts already, as picker rows, best ranked first.
func emuCoreChoices(system string, listed, entries []emuEntry) []emuEntry {
	var installed []emuEntry
	for _, e := range entries {
		if e.CorePath != "" && e.Status == emuAvailable && emuCoreChoicePlays(e.Core, system) {
			installed = append(installed, e)
		}
	}
	if len(installed) == 0 {
		return nil
	}
	covered := map[string]bool{}
	var scripts []string
	for _, e := range listed {
		if c := normalizeCoreName(e.Core); c != "" {
			covered[c] = true
		}
		launch := strings.TrimSpace(e.Launch)
		switch strings.ToLower(filepath.Ext(launch)) {
		case ".so", ".dll":
			if c, _ := parseCoreFile(filepath.Base(launch)); c != "" {
				covered[c] = true
			}
			continue
		}
		// A script that names one or several cores starts those: a direct
		// row for one of them would be the same emulator twice.
		script := emuBestReadScript(launch)
		for _, c := range script.cores {
			covered[normalizeCoreName(c)] = true
		}
		if script.retroArch {
			// A standalone's script ("./mgba") names its own program, not
			// RetroArch's core of the same name.
			scripts = append(scripts, script.text)
		}
	}
	cands := emuBestCandidatesFor(system)
	var out []emuEntry
	for _, e := range installed {
		core := normalizeCoreName(e.Core)
		if core == "" || covered[core] || !regularFile(e.CorePath) {
			continue
		}
		named := false
		for _, text := range scripts {
			if emuScriptNamesCore(text, core) {
				named = true
				break
			}
		}
		if named {
			covered[core] = true
			continue
		}
		retroArch := coreRetroArchBinary(filepath.Dir(e.CorePath))
		if retroArch == "" || emuCoreArchProblem(e.CorePath, retroArch) != "" {
			continue
		}
		covered[core] = true
		out = append(out, emuEntry{
			Name:   emuCoreChoiceName(core, system),
			Detail: e.CorePath,
			Status: emuAvailable,
			Launch: e.CorePath,
			Core:   core,
			Extra:  emuChoiceCore,
			Glyph:  e.Glyph,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := emuBestCoreRank(out[i].Core, cands), emuBestCoreRank(out[j].Core, cands)
		if ri < 0 {
			ri = len(cands)
		}
		if rj < 0 {
			rj = len(cands)
		}
		if ri != rj {
			return ri < rj
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// emuWithExtras is list followed by the extra choices: right after each
// folder entry its launchlist alternatives, then — when cores is true — the
// installed cores that play system and no listed launcher starts. A launcher
// already in the list is never repeated.
func emuWithExtras(system string, list, entries []emuEntry, cores bool) []emuEntry {
	if len(list) == 0 && !cores {
		return list
	}
	seen := map[string]bool{}
	for _, e := range list {
		seen[strings.ToLower(filepath.Clean(strings.TrimSpace(e.Launch)))] = true
	}
	out := make([]emuEntry, 0, len(list))
	for _, e := range list {
		out = append(out, e)
		if e.Extra != "" {
			continue
		}
		for _, alt := range emuFolderAlternatives(e) {
			key := strings.ToLower(filepath.Clean(alt.Launch))
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, alt)
		}
	}
	if !cores {
		return out
	}
	for _, c := range emuCoreChoices(system, out, entries) {
		key := strings.ToLower(filepath.Clean(c.Launch))
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}

// emuIsCoreLaunch reports whether launcher is a libretro core the handheld's
// RetroArch loads directly (a .so file).
func emuIsCoreLaunch(launcher string) bool {
	return !IsWindows() && strings.EqualFold(filepath.Ext(strings.TrimSpace(launcher)), ".so")
}

// emuCoreLaunchCheck is the reason a core cannot be started directly, or nil.
func emuCoreLaunchCheck(launcher string) error {
	if !emuIsCoreLaunch(launcher) {
		return nil
	}
	retroArch := coreRetroArchBinary(filepath.Dir(launcher))
	if retroArch == "" {
		core, _ := parseCoreFile(filepath.Base(launcher))
		return fmt.Errorf("no RetroArch next to the %s core — pick another emulator in the game's options", coreConfigDisplayName(core))
	}
	if why := emuCoreArchProblem(launcher, retroArch); why != "" {
		return errors.New(filepath.Base(launcher) + " cannot load here: " + why)
	}
	return nil
}

// emuCoreChoicePrepareLaunch runs a directly started core the way the firmware
// starts RetroArch: from its folder (gameLaunchCommandArgs set cmd.Dir) and,
// for TrimUI's own build, with HOME pointing there, which is where it finds
// retroarch.cfg and .retroarch/. It runs after screenChildGroupConfigure, so
// the rest of the environment is the firmware's (childenv.go); the last HOME
// wins.
func emuCoreChoicePrepareLaunch(launcher string, cmd *exec.Cmd) {
	if cmd == nil || !emuIsCoreLaunch(launcher) {
		return
	}
	if netplayTrimuiBuild(cmd.Path) && cmd.Dir != "" {
		cmd.Env = append(cmd.Env, "HOME="+cmd.Dir)
	}
	// Under JukaHub OS's own init the card's RetroArch is told its config,
	// as its Emulators tile starts it (raemu.go).
	if jukaosNativeActive() && filepath.Clean(cmd.Dir) == "/mnt/SDCARD/RetroArch" && len(cmd.Args) > 0 {
		cmd.Args = append([]string{cmd.Args[0], "--config", filepath.Join(cmd.Dir, "retroarch.cfg")}, cmd.Args[1:]...)
	}
	logInfo("library", "starting the %s core directly in %s", filepath.Base(launcher), cmd.Path)
}

// ── First-run speed settings ─────────────────────────────────────────────

// emuCoreChoiceSeedOptions gives a directly started core of a console this chip
// only just runs the handheld's speed settings when it has never had options
// of its own here (see the file comment). It never blocks a launch and never
// changes a file that exists. GameCube and Wii get Dolphin's (dolphin_video.go),
// PS2 ARMSX2's (ps2.go).
func emuCoreChoiceSeedOptions(launcher string, game libraryGame) {
	if !emuIsCoreLaunch(launcher) || (systemCompatTier(game.System) != compatTierMixed && !dolphinIsSystem(game.System) && !ps2IsSystem(game.System)) {
		return
	}
	core := gameCoreFromLauncher(launcher, game.System)
	options := map[string]string{}
	addSystemBoostOptions(options, core, boostPlan{ResolutionScale: 75, AudioQuality: "high"})
	if len(options) == 0 {
		return
	}
	targets := raScopeTargets(raLaunchCandidates(launcher, raTuneCandidates()))
	if len(targets) == 0 {
		return
	}
	cfgPath := targets[0]
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return
	}
	cfg := string(raw)
	// Per-core option files exist only with global core options off (the
	// default since RetroArch 1.9, written into its config); the shared file
	// of an older or switched RetroArch is the user's and stays untouched.
	if global, ok := raConfigGet(cfg, "global_core_options"); !ok || raValueEqual(global, "true") {
		return
	}
	folder := coreConfigDisplayName(core)
	dir := filepath.Join(raTuneMenuConfigDir(cfg, cfgPath), folder)
	stem := strings.TrimSuffix(filepath.Base(game.Path), filepath.Ext(game.Path))
	for _, name := range []string{folder, stem, filepath.Base(filepath.Dir(game.Path))} {
		if name != "" && regularFile(filepath.Join(dir, name+".opt")) {
			return // options of its own already: the core has run, or someone set them
		}
	}
	// A RetroArch that kept this core's options in the shared file before
	// carries them over into the new one by itself: core_options_path, else
	// retroarch-core-options.cfg beside the config (raTuneCoreOptionFile).
	sharedPath := filepath.Join(filepath.Dir(cfgPath), "retroarch-core-options.cfg")
	if v, ok := raConfigGet(cfg, "core_options_path"); ok {
		if p := videoFilterExpandDir(v, cfgPath); p != "" {
			sharedPath = p
		}
	}
	if shared, err := os.ReadFile(sharedPath); err == nil {
		for key := range options {
			if _, ok := raConfigGet(string(shared), key); ok {
				return
			}
		}
	}
	keys := make([]string, 0, len(options))
	for key := range options {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&b, "%s = %q\n", key, options[key])
	}
	path := filepath.Join(dir, folder+".opt")
	if err := AtomicWrite(path, []byte(b.String()), 0o644); err != nil {
		logWarn("emulation", "could not give %s its speed settings (%s): %v", folder, path, err)
		return
	}
	logInfo("emulation", "%s starts with the handheld's speed settings (%s): %s", folder, path, strings.Join(keys, ", "))
}

// ── Whole-console choice ─────────────────────────────────────────────────

// emuConsoleLauncherSet makes launcher the one every game of system starts in
// (launcher "" removes the console's entry), in game-launchers.json, keeping
// the file's other entries and their order. An entry JukaHub's own PPSSPP
// manages is not removed here: its card puts PSP games back. The PPSSPP
// manager rewrites the same file (ppssppPointPSPLocked) under its lock, which
// serialises the two read-modify-writes so neither loses the other's entry.
// That lock is also held while an old PPSSPP build folder is removed, and only
// background work may wait for it (ppsspp_managed.go): the picker and the
// options sheet call this on the main thread, so a busy lock is a "try again"
// rather than a stalled screen.
func emuConsoleLauncherSet(system, launcher string) error {
	system = strings.TrimSpace(system)
	if system == "" {
		return errors.New("no console to set")
	}
	file := ppssppOverridesPathFn()
	if file == "" {
		return errors.New("JukaHub has no data folder for game-launchers.json")
	}
	if !ppssppMgr.mu.TryLock() {
		return errors.New("JukaHub's PPSSPP is updating its files — try again in a moment")
	}
	defer ppssppMgr.mu.Unlock()
	m, existed, err := ppssppReadOverrides(file)
	if err != nil {
		return err
	}
	changed := false
	for key, raw := range m {
		if !strings.EqualFold(strings.TrimSpace(key), system) {
			continue
		}
		if launcher == "" && ppssppIsManagedLauncher(ppssppJSONString(raw)) {
			return errors.New("PSP games use JukaHub's PPSSPP — change that on its card in Emulators")
		}
		delete(m, key)
		changed = true
	}
	if launcher != "" {
		quoted, err := json.Marshal(launcher)
		if err != nil {
			return err
		}
		m[system] = quoted
		changed = true
	}
	if !changed || (!existed && len(m) == 0) {
		return nil
	}
	return ppssppWriteOverrides(file, m)
}

// emuConsolePinOffered reports whether the sheet offers X for system: not for
// a console whose games are split over three or more folders of their own
// (Arcade's CPS1, CPS2, MAME, FBNEO…), where each folder's games need that
// folder's emulator (findGameLaunchersFor) and one emulator for every game
// would take them away from it.
func emuConsolePinOffered(system string) bool {
	if strings.TrimSpace(system) == "" {
		return false
	}
	aliases := map[string]bool{}
	for _, c := range gameLauncherCandidates(system) {
		aliases[c] = true
	}
	own := 0
	for _, f := range gameLauncherFolders(system) {
		// Collection names ("nintendo - game boy") are no folder anyone makes.
		if strings.Contains(f, " ") || aliases[normaliseLauncherName(f)] {
			continue
		}
		own++
	}
	return own < 3
}

// emuConsoleLauncherClearable reports whether the options sheet may take the
// console's game-launchers.json entry back: not when it is JukaHub's managed
// PPSSPP (ppsspp_managed.go owns that one).
func emuConsoleLauncherClearable(launcher string) bool {
	return strings.TrimSpace(launcher) != "" && !ppssppIsManagedLauncher(launcher)
}
