package main

// gbprinter.go — Game Boy Printer: a Game Boy or Game Boy Color game that
// prints (Pokémon's Pokédex, the Game Boy Camera's photos, Link's Awakening
// DX's album) prints into JukaHub's scrapbook.
//
// Which emulator. Among the libretro cores only DoubleCherryGB (a fork of TGB
// Dual) emulates the printer: its README promises "Gameboy Printer Emulation
// with PNG-Print", and its link-cable hub switches the cable to the printer by
// itself when a game starts printing. SameBoy and mGBA print only as
// standalone programs; their cores, Gambatte, Gearboy and TGB Dual do not. So
// the game page's More sheet has a "Game Boy Printer" row for every Game Boy
// and Game Boy Color game — at the top for the games known to print
// (gbpKnownGames), at the bottom for the rest — that opens a card saying what
// happens, and one press of "Play with the printer":
//
//   - installs the core the first time, through the Emulators page's install
//     (planEmuInstall → applyEmuInstallPlan: the buildbot index's CRC32, the
//     size cap, the load check against this firmware's C library — and on the
//     handheld JukaHub's private glibc when a nightly needs a newer one,
//     docs/PRIVATE_GLIBC.md), with the install card up;
//   - makes it this game's emulator (the game options' Emulator, the pin the
//     emulator picker honours), remembering what the game used before;
//   - copies the game's battery save across when RetroArch keeps one folder
//     per core (gbpCarrySave), so the adventure continues where it was;
//   - and starts the game.
//
// The core is deliberately not added to the console's list of emulators: it
// would make every Game Boy game ask "which emulator?" once it is installed.
// The card's "Usual emulator" puts the game back (and its save with it).
//
// The printer's core options: the only key we could confirm is
// dcgb_emulated_gameboys, which must be 1 for one player with a printer (the
// README). It is set for each printing launch (raScopeApply: one launch, put
// back after it), in the file RetroArch reads for this game. The key that
// picks the link-cable device, if the hub needs one, could not be read from
// the core's source here; data/gbprinter/state.json's "core_options" adds keys
// without a new build, and after each printing game the core's dcgb_ keys
// about printing and the link cable are logged and kept in "seen_options", so
// a tester can name them (docs/GB_PRINTER.md, GBPRINT-HW-1).
//
// Collecting the prints is gbprinter_collect.go; the scrapbook screen is
// gbprinter_ui.go; the phone's view is gbprinter_companion.go.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// gbpCoreName is DoubleCherryGB's core name (the file is
	// DoubleCherryGB_libretro.so; names are compared lower-case).
	gbpCoreName = "doublecherrygb"
	// gbpCoreLabel is its library name: RetroArch's folder for its options
	// and its per-core saves.
	gbpCoreLabel = "DoubleCherryGB"
	// gbpStateName is the printer's own record in gbpRootDir.
	gbpStateName = "state.json"
	// gbpInstallTimeout bounds the first install on handheld Wi-Fi.
	gbpInstallTimeout = 10 * time.Minute
	// gbpPlayAfterWithin: a game starts by itself after the install only
	// while the press is this recent.
	gbpPlayAfterWithin = 5 * time.Minute
	// gbpSaveMax bounds a battery save copied across (a Game Boy cart has at
	// most 128 KB).
	gbpSaveMax = 1 << 20
)

// gbpKnownGames are the games known to print, as letters and digits of their
// title (gbpTitleKey): Nintendo's printer list, the ones players look for.
// Any Game Boy game may still use the row; these get it first.
var gbpKnownGames = []string{
	"pokemonyellow", "pokemongold", "pokemonsilver", "pokemoncrystal", "pokemonpinball",
	"pokemontradingcardgame", "pokemoncardgb", "gameboycamera", "pocketcamera",
	"linksawakeningdx", "yumewomitorushimadx", "supermariobrosdeluxe", "harvestmoon", "bokujoumonogatari",
	"donkeykongcountry", "mariogolf", "mariotennis", "kirbytiltntumble", "korokorokirby",
	"aliceinwonderland", "perfectdark", "etdigitalcompanion", "102dalmatians", "cardhero", "medarot",
}

// gbpTitleKey is a title's lower-case letters and digits, without what sits
// in brackets ("Pokemon - Yellow Version (USA)" → "pokemonyellowversion").
// Pure.
func gbpTitleKey(title string) string {
	var b strings.Builder
	depth := 0
	for _, r := range strings.ToLower(title) {
		switch r {
		case '(', '[':
			depth++
			continue
		case ')', ']':
			if depth > 0 {
				depth--
			}
			continue
		case 'é', 'è', 'ê':
			r = 'e'
		}
		if depth == 0 && r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// gbpIsGameBoy reports a Game Boy or Game Boy Color game.
func gbpIsGameBoy(game libraryGame) bool {
	switch canonicalCompatSystem(game.System) {
	case "Game Boy", "Game Boy Color":
		return true
	}
	switch strings.ToLower(filepath.Ext(game.Path)) {
	case ".gb", ".gbc", ".dmg", ".cgb", ".sgb":
		return true
	}
	return false
}

// gbpKnownPrinterGame reports a game on the printer list.
func gbpKnownPrinterGame(game libraryGame) bool {
	keys := []string{gbpTitleKey(game.Title), gbpTitleKey(filepath.Base(game.Path))}
	for _, want := range gbpKnownGames {
		for _, k := range keys {
			if k != "" && strings.Contains(k, want) {
				return true
			}
		}
	}
	return false
}

// gbpIsCoreLauncher reports a launcher that starts DoubleCherryGB.
func gbpIsCoreLauncher(launcher, system string) bool {
	return strings.TrimSpace(launcher) != "" && normalizeCoreName(gameCoreFromLauncher(launcher, system)) == gbpCoreName
}

// gbpIsPrinterLaunch reports a Game Boy game starting in DoubleCherryGB.
func gbpIsPrinterLaunch(launcher string, game libraryGame) bool {
	return gbpIsGameBoy(game) && gbpIsCoreLauncher(launcher, game.System)
}

// ── The printer's record ─────────────────────────────────────────────────

// gbpGameState is what the printer changed for one game.
type gbpGameState struct {
	// PinBefore is the game's own emulator choice before the printer (""
	// for none); RanBefore the emulator it ran in, where its save was.
	PinBefore string `json:"pin_before,omitempty"`
	RanBefore string `json:"ran_before,omitempty"`
	// LastBefore is the game options' "emulator it ran in last" before the
	// printer, put back as it was by "Usual emulator".
	LastBefore string `json:"last_before,omitempty"`
	// Pinned: the printer made DoubleCherryGB this game's emulator (the
	// fields above are the state to go back to).
	Pinned bool `json:"pinned,omitempty"`
	// Saves is each battery save's SHA-256 right after the last copy across,
	// so a later copy knows whether that file changed since.
	Saves map[string]string `json:"saves,omitempty"`
}

// gbpState is data/gbprinter/state.json.
type gbpState struct {
	Games map[string]gbpGameState `json:"games,omitempty"`
	// CoreOptions are set for every printing launch; absent means the
	// defaults (gbpDefaultCoreOptions). A tester may add keys here.
	CoreOptions map[string]string `json:"core_options,omitempty"`
	// SeenOptions are the core's printer and link-cable keys found in its
	// options file after a printing game, for the hardware check.
	SeenOptions []string `json:"seen_options,omitempty"`
}

// gbpDefaultCoreOptions are the options a printing launch sets.
var gbpDefaultCoreOptions = map[string]string{"dcgb_emulated_gameboys": "1"}

var (
	gbpStateMu     sync.Mutex
	gbpOptionKeyRe = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
	// gbpBusy: an install or a save copy is running; gbpCountsLoaded: the
	// scrapbook list is in memory, so the More row can count from it.
	gbpBusy         atomic.Bool
	gbpCountsLoaded atomic.Bool
)

func gbpStatePath() string {
	root := gbpRootDir()
	if root == "" {
		return ""
	}
	return filepath.Join(root, gbpStateName)
}

// gbpLoadState reads the record; gbpStateMu is held.
func gbpLoadStateLocked() gbpState {
	var st gbpState
	if p := gbpStatePath(); p != "" {
		if raw, err := os.ReadFile(p); err == nil && len(raw) < 1<<20 {
			if err := json.Unmarshal(raw, &st); err != nil {
				logWarn("gbprinter", "ignoring %s: %v", p, err)
				st = gbpState{}
			}
		}
	}
	if st.Games == nil {
		st.Games = map[string]gbpGameState{}
	}
	return st
}

// gbpSaveStateLocked writes the record; gbpStateMu is held.
func gbpSaveStateLocked(st gbpState) error {
	p := gbpStatePath()
	if p == "" {
		return errors.New("JukaHub has no data folder")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(p, append(data, '\n'), 0o644)
}

// gbpUpdateState changes the record under its lock.
func gbpUpdateState(change func(st *gbpState)) error {
	gbpStateMu.Lock()
	defer gbpStateMu.Unlock()
	st := gbpLoadStateLocked()
	change(&st)
	return gbpSaveStateLocked(st)
}

// gbpGameRecord is the record for one game (zero when none).
func gbpGameRecord(rom string) (gbpGameState, bool) {
	gbpStateMu.Lock()
	defer gbpStateMu.Unlock()
	st := gbpLoadStateLocked()
	for k, v := range st.Games {
		if gbpSameROM(k, rom) {
			return v, true
		}
	}
	return gbpGameState{}, false
}

// gbpCoreOptionValues are the core options for a printing launch: the
// record's, else the defaults; a key or value that could break the file is
// dropped.
func gbpCoreOptionValues() []raScopeValue {
	gbpStateMu.Lock()
	st := gbpLoadStateLocked()
	gbpStateMu.Unlock()
	opts := st.CoreOptions
	if len(opts) == 0 {
		opts = gbpDefaultCoreOptions
	}
	keys := make([]string, 0, len(opts))
	for k, v := range opts {
		if gbpOptionKeyRe.MatchString(k) && !strings.ContainsAny(v, "\"\r\n") && len(v) <= 64 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := make([]raScopeValue, 0, len(keys))
	for _, k := range keys {
		out = append(out, raScopeValue{Key: k, Value: opts[k]})
	}
	return out
}

// ── Core options ─────────────────────────────────────────────────────────

// gbpOptionsFiles are the options files of DoubleCherryGB for game, in the
// order RetroArch prefers them, and the core's own file, for the config at
// cfgPath; shared is the one file of a RetroArch with global core options.
func gbpOptionsFiles(cfg, cfgPath, romPath string) (perGame []string, coreFile, shared string, global bool) {
	sharedPath := filepath.Join(filepath.Dir(cfgPath), "retroarch-core-options.cfg")
	if v, ok := raConfigGet(cfg, "core_options_path"); ok {
		if p := videoFilterExpandDir(v, cfgPath); p != "" {
			sharedPath = p
		}
	}
	// A game's or folder's own options file wins whatever global_core_options
	// says (raTuneCoreOptionFile reads them in the same order).
	dir := filepath.Join(raTuneMenuConfigDir(cfg, cfgPath), gbpCoreLabel)
	stem := gameRenameStem(romPath)
	parent := filepath.Base(filepath.Dir(romPath))
	for _, name := range []string{stem, parent} {
		if name != "" && name != "." && name != string(filepath.Separator) {
			perGame = append(perGame, filepath.Join(dir, name+".opt"))
		}
	}
	g, has := raConfigGet(cfg, "global_core_options")
	if !has || raValueEqual(g, "true") {
		// Before RetroArch 1.9, or switched on: one file for every core.
		return perGame, "", sharedPath, true
	}
	return perGame, filepath.Join(dir, gbpCoreLabel+".opt"), sharedPath, false
}

// gbpApplyCoreOptions sets the printer's options for this launch in the file
// RetroArch will read for the game: the game's or folder's own options file
// when there is one, else the core's, else the shared file — each for one
// launch (raScopeApply). A core that never ran here has no file: it gets one
// with these options, as a first run of a directly started core does
// (emuCoreChoiceSeedOptions). Never blocks or fails a launch.
func gbpApplyCoreOptions(launcher string, game libraryGame) {
	values := gbpCoreOptionValues()
	if len(values) == 0 {
		return
	}
	targets := raScopeTargets(raLaunchCandidates(launcher, raTuneCandidates()))
	if len(targets) == 0 {
		logInfo("gbprinter", "no RetroArch config found: the printer starts with the core's own options")
		return
	}
	cfgPath := targets[0]
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return
	}
	perGame, coreFile, shared, global := gbpOptionsFiles(string(raw), cfgPath, game.Path)
	target := ""
	candidates := append(perGame, coreFile)
	if global {
		candidates = append(perGame, shared)
	}
	for _, p := range candidates {
		if p != "" && regularFile(p) {
			target = p
			break
		}
	}
	if target != "" {
		if _, err := raScopeApply(target, values); err != nil {
			logWarn("gbprinter", "could not set the printer's options in %s: %v", target, err)
			return
		}
		logInfo("gbprinter", "printer options for this launch in %s", target)
		return
	}
	if global {
		return // no shared file yet: RetroArch's defaults (one Game Boy)
	}
	var b strings.Builder
	for _, v := range values {
		fmt.Fprintf(&b, "%s = %q\n", v.Key, v.Value)
	}
	if err := os.MkdirAll(filepath.Dir(coreFile), 0o755); err == nil {
		if err := AtomicWrite(coreFile, []byte(b.String()), 0o644); err != nil {
			logWarn("gbprinter", "could not write %s: %v", coreFile, err)
			return
		}
	}
	logInfo("gbprinter", "first run of %s: options file %s written", gbpCoreLabel, coreFile)
}

// gbpNoteOptionKeys logs, after a printing game, the core's options about the
// printer and the link cable as RetroArch saved them, and keeps their names
// for the hardware check. Off the main thread.
func gbpNoteOptionKeys(launcher string, game libraryGame) {
	targets := raScopeTargets(raLaunchCandidates(launcher, raTuneCandidates()))
	if len(targets) == 0 {
		return
	}
	raw, err := os.ReadFile(targets[0])
	if err != nil {
		return
	}
	perGame, coreFile, shared, global := gbpOptionsFiles(string(raw), targets[0], game.Path)
	files := append(perGame, coreFile)
	if global {
		files = append(perGame, shared)
	}
	var found []string
	for _, p := range files {
		data, err := os.ReadFile(p)
		if err != nil || len(data) > 1<<20 {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
			key = strings.TrimSpace(key)
			if !ok || !strings.HasPrefix(key, "dcgb_") {
				continue
			}
			lower := strings.ToLower(key)
			if strings.Contains(lower, "print") || strings.Contains(lower, "link") || strings.Contains(lower, "device") || strings.Contains(lower, "gameboys") {
				found = append(found, key+" = "+strings.TrimSpace(value))
			}
		}
		if len(found) > 0 {
			break
		}
	}
	if len(found) == 0 {
		return
	}
	sort.Strings(found)
	logInfo("gbprinter", "%s's printer and link options: %s", gbpCoreLabel, strings.Join(found, "; "))
	_ = gbpUpdateState(func(st *gbpState) { st.SeenOptions = found })
}

// ── The core ─────────────────────────────────────────────────────────────

// gbpFindCore is the installed DoubleCherryGB core's file, or "". Off the
// main thread (it lists the cores folders).
func gbpFindCore() string {
	inv := scanCoreInventory()
	if b, ok := inv.Active[gbpCoreName]; ok && regularFile(b.Path) {
		return b.Path
	}
	return ""
}

// gbpCoreUsable says why the core at path cannot start a game here ("" when
// it can).
func gbpCoreUsable(path string) string {
	if path == "" {
		return "DoubleCherryGB is not installed"
	}
	if emuIsCoreLaunch(path) {
		if err := emuCoreLaunchCheck(path); err != nil {
			return err.Error()
		}
		return ""
	}
	if strings.EqualFold(filepath.Ext(path), ".dll") && emuRetroArchForCore(path) == "" {
		return "RetroArch is not installed: install it on the Emulators page first"
	}
	return ""
}

// gbpInstallCore fetches DoubleCherryGB through the Emulators page's install.
func gbpInstallCore(ctx context.Context) (string, error) {
	client := newEmulatorUpdateClient()
	index, err := fetchCoreIndex(ctx, client)
	if err != nil {
		return "", err
	}
	p := planEmuInstall(emuEntry{Name: gbpCoreLabel, Core: gbpCoreName}, scanCoreInventory(), index)
	switch p.Action {
	case emuInstallNew, emuInstallUpdate:
	case emuInstallCurrent:
		if path := gbpFindCore(); path != "" {
			return path, nil
		}
		return "", errors.New("DoubleCherryGB is installed but RetroArch cannot see it")
	default:
		return "", errors.New("DoubleCherryGB is not published for this device yet")
	}
	// A build the load check refused here before is not fetched again
	// (dolphinInstallCore does the same); a later build gets its own check.
	if reason, refused := coreBuildRefused(gbpCoreName, p.Index.CRC32); refused {
		return "", &coreRefusedError{Core: gbpCoreName, Build: p.Index.Date, Reason: "was refused here before: " + reason}
	}
	pool := compatCorePoolDir()
	build, _, err := applyEmuInstallPlan(ctx, client, p, pool)
	if err != nil {
		return "", err
	}
	if !coreKeepActivatedInPool() {
		if err := coreDropPoolBuild(pool, build); err != nil {
			logDebug("gbprinter", "kept the pool copy of %s: %v", gbpCoreName, err)
		}
	}
	invalidateEmulatorCache()
	invalidateCompatCache()
	coreBuildsRequestReload()
	if err := emuConfirmInstalledCore(gbpCoreName, build); err != nil {
		return "", err
	}
	logInfo("gbprinter", "installed %s (%s)", gbpCoreName, build.Version)
	if path := gbpFindCore(); path != "" {
		return path, nil
	}
	return "", errors.New("the download finished but RetroArch cannot see DoubleCherryGB")
}

// ── Saves across cores ───────────────────────────────────────────────────

// gbpSaveFile is where launcher's RetroArch keeps game's battery save with
// ext (".srm", ".rtc"), as discIndexPaths works it out; "" for an emulator
// that is not RetroArch.
func gbpSaveFile(launcher string, game libraryGame, ext string) string {
	if !retroArchLauncher(launcher) && !gbpIsCoreLauncher(launcher, game.System) {
		return ""
	}
	stem := gameRenameStem(game.Path)
	if stem == "" {
		return ""
	}
	contentDir := filepath.Dir(filepath.Clean(game.Path))
	text, cfgPath := gbpLaunchConfig(launcher)
	saveDir := ""
	if v, ok := retroArchConfigValue(text, "savefile_directory"); ok && cfgPath != "" {
		saveDir = resolveRetroArchSaveDir(v, cfgPath)
	}
	if saveDir == "" || discConfigOn(text, "savefiles_in_content_dir") {
		return filepath.Join(contentDir, stem+ext)
	}
	dir := saveDir
	if discConfigOn(text, "sort_savefiles_by_content_enable") {
		dir = filepath.Join(dir, filepath.Base(contentDir))
	}
	if discConfigOn(text, "sort_savefiles_enable") {
		folder := gbpCoreLabel
		if core := normalizeCoreName(gameCoreFromLauncher(launcher, game.System)); core != gbpCoreName {
			folder = coreConfigDisplayName(core)
			if names := resetCoreFolders(core); len(names) > 0 {
				folder = names[0]
			}
		}
		if folder == "" {
			return ""
		}
		dir = filepath.Join(dir, folder)
	}
	return filepath.Join(dir, stem+ext)
}

func gbpFileSum(path string) (string, []byte, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > gbpSaveMax {
		return "", nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, false
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), data, true
}

// gbpCarrySave copies the battery saves (.srm and .rtc) of game from the
// emulator from to the emulator to when they keep them in different files.
// A file is written only where there is none, or where it is unchanged since
// the last copy (its SHA-256 is the one recorded then); the version replaced
// is kept beside it as "<name>.jukahub-before-printer". When both sides
// changed, nothing is copied and note says so. Off the main thread.
func gbpCarrySave(game libraryGame, from, to string) (note string) {
	rec, _ := gbpGameRecord(game.Path)
	sums := map[string]string{}
	for k, v := range rec.Saves {
		sums[k] = v
	}
	copied, conflict := 0, false
	for _, ext := range []string{".srm", ".rtc"} {
		src, dst := gbpSaveFile(from, game, ext), gbpSaveFile(to, game, ext)
		if src == "" || dst == "" || strings.EqualFold(filepath.Clean(src), filepath.Clean(dst)) {
			continue
		}
		srcSum, data, ok := gbpFileSum(src)
		if !ok {
			continue
		}
		if a, errA := os.Stat(src); errA == nil {
			if b, errB := os.Stat(dst); errB == nil && os.SameFile(a, b) {
				continue
			}
		}
		dstSum, old, had := gbpFileSum(dst)
		if !had {
			// Missing is the only case written without a copy kept first: a
			// save there that cannot be read (or is no plain file, or too
			// large) is left alone.
			if _, err := os.Lstat(dst); !os.IsNotExist(err) {
				conflict = true
				logWarn("gbprinter", "%s is there but cannot be read: nothing copied", dst)
				continue
			}
		}
		switch {
		case had && dstSum == srcSum:
			sums[src], sums[dst] = srcSum, dstSum
			continue
		case had && sums[dst] != dstSum && sums[src] == srcSum:
			// Only the destination moved on since the last copy: it is the
			// newer save, and nothing needs copying.
			logInfo("gbprinter", "%s is newer than %s: nothing copied", dst, src)
			continue
		case had && sums[dst] != dstSum:
			// Changed on both sides since the last copy (or never copied):
			// neither is overwritten.
			conflict = true
			logWarn("gbprinter", "%s and %s both have their own save: nothing copied", src, dst)
			continue
		case had:
			if err := AtomicWrite(dst+".jukahub-before-printer", old, 0o644); err != nil {
				logWarn("gbprinter", "save not copied: cannot keep %s first: %v", dst, err)
				continue
			}
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			continue
		}
		if err := AtomicWrite(dst, data, 0o644); err != nil {
			logWarn("gbprinter", "save not copied to %s: %v", dst, err)
			continue
		}
		sums[src], sums[dst] = srcSum, srcSum
		copied++
		logInfo("gbprinter", "save copied %s → %s", src, dst)
	}
	_ = gbpUpdateState(func(st *gbpState) {
		key := game.Path
		for k := range st.Games {
			if gbpSameROM(k, game.Path) {
				key = k
			}
		}
		g := st.Games[key]
		g.Saves = sums
		st.Games[key] = g
	})
	switch {
	case conflict:
		return "both emulators have their own save — nothing was copied"
	case copied > 0:
		return "your save came along"
	}
	return ""
}

// ── The card ─────────────────────────────────────────────────────────────

// gbpCard is the "Game Boy Printer" card over the game page. Main thread.
var gbpCard struct {
	open    bool
	game    libraryGame
	focus   int
	known   bool
	pinned  bool
	core    string // the core's file once looked up
	why     string // why that core cannot start a game here ("" when it can)
	looked  bool
	prints  int
	buttons []string
	hits    []sdl.Rect
	card    sdl.Rect
	wrap    tbxWrapCache
}

// The card's buttons.
const (
	gbpBtnPlay   = "Play with the printer"
	gbpBtnBook   = "Scrapbook"
	gbpBtnUsual  = "Usual emulator"
	gbpRowOrder  = 15 // a game known to print: near the top of More
	gbpRowOrder2 = 85 // any other Game Boy game: at the bottom
)

// gbpIsCoreFile reports DoubleCherryGB's core file by its name alone — the
// pin the card sets is always the core file — so the main thread never opens
// a launch script to find out.
func gbpIsCoreFile(p string) bool {
	p = strings.TrimSpace(p)
	switch strings.ToLower(filepath.Ext(p)) {
	case ".so", ".dll":
		core, _ := parseCoreFile(filepath.Base(p))
		return normalizeCoreName(core) == gbpCoreName
	}
	return false
}

// gbpPinnedHere reports a game whose own emulator is DoubleCherryGB. No disk
// read (main thread).
func gbpPinnedHere(game libraryGame) bool {
	return gbpIsCoreFile(gameOptionsFor(game.Path).Emulator)
}

// gbpMoreRow is the More sheet's row.
func gbpMoreRow(known bool) func(game libraryGame) (string, string, bool) {
	return func(game libraryGame) (string, string, bool) {
		if !gbpIsGameBoy(game) || gbpKnownPrinterGame(game) != known {
			return "", "", false
		}
		detail := "Prints go to your scrapbook · DoubleCherryGB"
		if known {
			detail = "This game prints · pictures go to your scrapbook"
		}
		if n, ok := gbpCountIfLoaded(game.Path); ok && n > 0 {
			detail = fmt.Sprintf("%d in the scrapbook · %s", n, detail)
		}
		return "Game Boy Printer", detail, true
	}
}

// gbpCountIfLoaded is game's number of prints ("" every game's) when the
// scrapbook is in memory (the page reads it as it opens); ok false before.
// The main thread calls it, so it never waits for the lock: an import or a
// delete holds it while it writes to the card, and then ok is false too.
func gbpCountIfLoaded(rom string) (int, bool) {
	if !gbpCountsLoaded.Load() || !gbpStore.mu.TryLock() {
		return 0, false
	}
	defer gbpStore.mu.Unlock()
	if !gbpStore.loaded {
		return 0, false
	}
	n := 0
	for _, p := range gbpStore.index.Prints {
		if rom == "" || gbpSameROM(p.ROM, rom) {
			n++
		}
	}
	return n, true
}

// gbpCardOpen opens the card for game. Main thread.
func gbpCardOpen(game libraryGame) {
	gbpCard.open = true
	gbpCard.game = game
	gbpCard.focus = 0
	gbpCard.known = gbpKnownPrinterGame(game)
	gbpCard.pinned = gbpPinnedHere(game)
	gbpCard.prints, _ = gbpCountIfLoaded(game.Path)
	gbpCard.core, gbpCard.why, gbpCard.looked = "", "", false
	gbpCardButtons()
	rom := game.Path
	go func() {
		core := gbpFindCore()
		why := ""
		if core != "" {
			why = gbpCoreUsable(core)
		}
		prints, _ := gbpPrints(rom)
		gbpCountsLoaded.Store(true)
		tbxPost(func() {
			if !gbpCard.open || !gbpSameROM(gbpCard.game.Path, rom) {
				return
			}
			gbpCard.core, gbpCard.why, gbpCard.looked = core, why, true
			gbpCard.prints = len(prints)
			gbpCardButtons()
		})
	}()
}

func gbpCardButtons() {
	label := gbpBtnBook
	if gbpCard.prints > 0 {
		label = fmt.Sprintf("%s (%d)", gbpBtnBook, gbpCard.prints)
	}
	gbpCard.buttons = []string{gbpBtnPlay, label}
	if gbpCard.pinned {
		gbpCard.buttons = append(gbpCard.buttons, gbpBtnUsual)
	}
	if gbpCard.focus >= len(gbpCard.buttons) {
		gbpCard.focus = len(gbpCard.buttons) - 1
	}
}

// gbpCardText is the card's explanation.
func gbpCardText() string {
	var b strings.Builder
	if gbpCard.known {
		b.WriteString("This game can print. ")
	} else {
		b.WriteString("This game is not on the list of games that print; it may never send anything. ")
	}
	b.WriteString("JukaHub starts it in DoubleCherryGB, the RetroArch core with an emulated Game Boy Printer, and keeps this game in it until you pick its usual emulator here. ")
	b.WriteString("Print from the game's own menu (the Pokédex, the Camera's album). Each picture lands in the scrapbook when you quit the game.")
	switch {
	case !gbpCard.looked:
		b.WriteString(" Checking for DoubleCherryGB…")
	case gbpCard.core == "":
		b.WriteString(" DoubleCherryGB is downloaded the first time (a few MB, needs internet).")
	case gbpCard.why != "":
		b.WriteString(" " + capitalizeFirst(gbpCard.why) + ".")
	}
	if gbpCard.pinned {
		b.WriteString(" This game already starts in DoubleCherryGB.")
	}
	return b.String()
}

// gbpCardPress runs the focused button. Main thread.
func gbpCardPress() {
	if gbpCard.focus < 0 || gbpCard.focus >= len(gbpCard.buttons) {
		return
	}
	game := gbpCard.game
	switch label := gbpCard.buttons[gbpCard.focus]; {
	case label == gbpBtnPlay:
		gbpStartPrinting(game)
	case strings.HasPrefix(label, gbpBtnBook):
		gbpCard.open = false
		gbpBookOpenOverlay(game)
	case label == gbpBtnUsual:
		gbpCard.open = false
		gbpBackToUsual(game)
	}
}

// gbpStartPrinting is "Play with the printer": the core (installed first when
// it is missing), the pin, the save, then the game. Main thread.
func gbpStartPrinting(game libraryGame) {
	if !gbpBusy.CompareAndSwap(false, true) {
		ShowToast("The printer is being set up — a moment", ToastKindInfo)
		return
	}
	pressed := time.Now()
	gbpCard.open = false
	PlayActivateSound()
	go func() {
		core := gbpFindCore()
		var err error
		if core == "" {
			core, err = gbpInstallForGame(game)
		}
		note := ""
		if err == nil && core != "" {
			if why := gbpCoreUsable(core); why != "" {
				err = errors.New(why)
			} else if !gbpPinnedHere(game) {
				// Where the save is now: the emulator the game ran in. Worked
				// out here, off the main thread (it may list the emulators).
				prev := resolveGameLauncherFor(game)
				if gbpIsCoreLauncher(prev, game.System) {
					prev = ""
				}
				if prev != "" {
					note = gbpCarrySave(game, prev, core)
				}
				gbpRemember(game, prev)
			}
		}
		tbxPost(func() {
			gbpBusy.Store(false)
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					ShowToast("Game Boy Printer: "+emuFriendlyError(err), ToastKindWarning)
				}
				return
			}
			if core == "" {
				return
			}
			gbpPin(game, core)
			if note != "" {
				ShowToast(capitalizeFirst(note), ToastKindInfo)
			}
			gbpPlay(game, core, pressed)
		})
	}()
}

// gbpInstallForGame installs the core with the install card up. Off the main
// thread; the card and the patch slot are its own.
func gbpInstallForGame(game libraryGame) (string, error) {
	if !emuCoreInstallSupported() {
		return "", errors.New("this build cannot install emulator cores")
	}
	if !IsOnline() {
		return "", errors.New("DoubleCherryGB is needed once (a few MB) — connect to the internet and press again")
	}
	if !tryBeginPatchOp() {
		return "", errors.New("another download or update is running — press again when it is done")
	}
	defer endPatchOp()
	gen, barCtx := installBarBeginRun("Game Boy Printer")
	ShowToast("Getting DoubleCherryGB for the Game Boy Printer (once) — "+game.Label()+" starts when it is in", ToastKindInfo)
	installBarUpdateRun(gen, installPhaseResolving, 0, 0)
	ctx, cancel := context.WithTimeout(barCtx, gbpInstallTimeout)
	defer cancel()
	var core string
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("the install stopped: %v", r)
			}
		}()
		installBarUpdateRun(gen, installPhaseDownload, 0, 0)
		core, err = gbpInstallCore(ctx)
	}()
	if err != nil {
		var refused *coreRefusedError
		if errors.As(err, &refused) {
			err = errors.New("DoubleCherryGB cannot run on this device: " + refused.Error())
		}
		logWarn("gbprinter", "install: %v", err)
		installBarFailRun(gen, firstLine(emuFriendlyError(err)))
		if barCtx.Err() != nil {
			return "", context.Canceled
		}
		return "", err
	}
	installBarDoneRun(gen)
	return core, nil
}

// gbpStateKey is the record's key for rom: the one already there for the same
// file, else rom.
func gbpStateKey(st *gbpState, rom string) string {
	for k := range st.Games {
		if gbpSameROM(k, rom) {
			return k
		}
	}
	return rom
}

// gbpRemember records, before the pin, what the game had: its own pin, the
// emulator it last ran in, and ranBefore, where its save was ("" when
// unknown). Nothing when DoubleCherryGB is its emulator already. Off the main
// thread (state.json).
func gbpRemember(game libraryGame, ranBefore string) {
	opts := gameOptionsFor(game.Path)
	if gbpIsCoreFile(opts.Emulator) {
		return
	}
	pinBefore := strings.TrimSpace(opts.Emulator)
	lastBefore := strings.TrimSpace(opts.LastEmulator)
	if gbpIsCoreFile(lastBefore) {
		// It ran in DoubleCherryGB last (pinned and unpinned by hand since):
		// what it had before that is the record's, if there is one.
		lastBefore = ""
		if rec, ok := gbpGameRecord(game.Path); ok {
			lastBefore = rec.LastBefore
		}
	}
	if err := gbpUpdateState(func(st *gbpState) {
		key := gbpStateKey(st, game.Path)
		g := st.Games[key]
		g.PinBefore, g.LastBefore, g.Pinned = pinBefore, lastBefore, true
		if ranBefore != "" || g.RanBefore == "" {
			g.RanBefore = ranBefore
		}
		st.Games[key] = g
	}); err != nil {
		logWarn("gbprinter", "could not remember %s's emulator: %v", filepath.Base(game.Path), err)
	}
	logInfo("gbprinter", "%s: emulator before the printer %q, last ran in %q", filepath.Base(game.Path), setOr(pinBefore, "automatic"), lastBefore)
}

// gbpPin makes core the game's emulator (gbpRemember ran first). Main thread.
func gbpPin(game libraryGame, core string) {
	opts := gameOptionsFor(game.Path)
	if strings.EqualFold(filepath.Clean(strings.TrimSpace(opts.Emulator)), filepath.Clean(core)) {
		return
	}
	updateGameOptions(game.Path, func(o gameOptions) gameOptions { o.Emulator = core; return o })
	logInfo("gbprinter", "%s now starts in %s", filepath.Base(game.Path), core)
}

// gbpPlay starts the game in core when the press is recent and nothing took
// the screen meanwhile; otherwise a toast says it is ready. Main thread.
func gbpPlay(game libraryGame, core string, pressed time.Time) {
	if gameScreenSuspended() || emuPickOpen() || time.Since(pressed) > gbpPlayAfterWithin || !gmetaUI.open || !gbpSameROM(gmetaUI.game.Path, game.Path) {
		ShowToast("The printer is ready — start "+game.Label()+" to play with it", ToastKindSuccess)
		return
	}
	emuPickNoteRan(game, gameOptionsFor(game.Path), core)
	gmetaPlayWith(game, nil, core)
}

// gbpBackToUsual gives the game its usual emulator back, with its save. Main
// thread; the copy runs on a goroutine.
func gbpBackToUsual(game libraryGame) {
	if !gbpBusy.CompareAndSwap(false, true) {
		ShowToast("The printer is being set up — a moment", ToastKindInfo)
		return
	}
	core := strings.TrimSpace(gameOptionsFor(game.Path).Emulator)
	PlayActivateSound()
	go func() {
		// The record and the launchers' checks read the card: here, off the
		// main thread.
		rec, _ := gbpGameRecord(game.Path)
		usable := func(p string) bool { return p != "" && !gbpIsCoreFile(p) && gameLauncherStillUsable(p) }
		note := ""
		back := rec.RanBefore
		if !usable(back) {
			back = rec.PinBefore
		}
		if usable(back) && gbpIsCoreFile(core) {
			note = gbpCarrySave(game, core, back)
		}
		// What the options had before the printer, exactly; a launcher that
		// is gone since is left to the picker.
		pinBefore, lastBefore := "", ""
		if usable(rec.PinBefore) {
			pinBefore = rec.PinBefore
		}
		if usable(rec.LastBefore) {
			lastBefore = rec.LastBefore
		}
		// The save checksums stay: the next "Play with the printer" knows
		// which copy changed since.
		_ = gbpUpdateState(func(st *gbpState) {
			for k, g := range st.Games {
				if !gbpSameROM(k, game.Path) {
					continue
				}
				g.PinBefore, g.RanBefore, g.LastBefore, g.Pinned = "", "", "", false
				st.Games[k] = g
				if len(g.Saves) == 0 {
					delete(st.Games, k)
				}
			}
		})
		tbxPost(func() {
			gbpBusy.Store(false)
			updateGameOptions(game.Path, func(o gameOptions) gameOptions {
				if gbpIsCoreFile(o.Emulator) {
					o.Emulator = pinBefore
				}
				if rec.Pinned || gbpIsCoreFile(o.LastEmulator) {
					o.LastEmulator = lastBefore
				}
				return o
			})
			msg := game.Label() + " starts in its usual emulator again · prints stay in the scrapbook"
			if note != "" {
				msg += " · " + note
			}
			ShowToast(msg, ToastKindSuccess)
		})
	}()
}

// ── Card input and drawing ───────────────────────────────────────────────

func gbpCardAction(act tbxAction) {
	switch act {
	case tbxActLeft, tbxActUp:
		if gbpCard.focus > 0 {
			gbpCard.focus--
			PlayNavSound()
		}
	case tbxActRight, tbxActDown:
		if gbpCard.focus < len(gbpCard.buttons)-1 {
			gbpCard.focus++
			PlayNavSound()
		}
	case tbxActConfirm:
		gbpCardPress()
	case tbxActBack:
		gbpCard.open = false
		PlayBackSound()
	}
}

func gbpCardMouse(mx, my int32) bool {
	for i, r := range gbpCard.hits {
		if pointInRect(mx, my, r) {
			gbpCard.focus = i
			gbpCardPress()
			return true
		}
	}
	if gbpCard.card.W > 0 && !pointInRect(mx, my, gbpCard.card) {
		gbpCard.open = false
		PlayBackSound()
	}
	return true
}

func gbpCardRender(renderer *sdl.Renderer, config *Config) {
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	header := UIPanelHeader("Game Boy Printer", gbpCard.game.Label(), "emu-gbc")
	headerH := UIPanelHeaderBand(config, header, 10)
	cardW := min(int32(680), screenWidth-48)
	textW := cardW - 2*OverlayPadX
	lines := gbpCard.wrap.get(fonts.small, gbpCardText(), textW)
	lineH := fonts.smallH + 4
	btnH := fonts.bodyH + 18
	footerH := fonts.smallH + 16
	cardH := min(headerH+int32(len(lines))*lineH+16+btnH+16+footerH, screenHeight-32)
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}
	gbpCard.card = card
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	UIPageHeaderDraw(renderer, config, overlayHeaderOnCard(header, card))
	y := card.Y + headerH
	for _, line := range lines {
		if y+lineH > card.Y+card.H-btnH-footerH-16 {
			break
		}
		renderText(renderer, config, fonts.small, line, ColorTextSecondary(), card.X+OverlayPadX, y)
		y += lineH
	}
	by := card.Y + card.H - footerH - btnH - 10
	x := card.X + OverlayPadX
	gbpCard.hits = gbpCard.hits[:0]
	for i, label := range gbpCard.buttons {
		w := tbxButtonWidth(fonts.body, label, 120)
		if x+w > card.X+card.W-OverlayPadX {
			w = card.X + card.W - OverlayPadX - x
		}
		r := sdl.Rect{X: x, Y: by, W: w, H: btnH}
		tbxDrawButton(renderer, config, fonts.body, r, label, i == 0, i == gbpCard.focus, gbpBusy.Load() && i == 0)
		gbpCard.hits = append(gbpCard.hits, r)
		x += w + 10
	}
	hint := hintLine(FooterHint{"D-Pad", "Choose"}, FooterHint{"A", "Select"}, FooterHint{"B", "Back"})
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, hint, card.W-2*OverlayPadX), ColorTextSecondary(),
		card.X+OverlayPadX, card.Y+card.H-footerH+2)
}

func init() {
	gamePageRegisterExtra(gamePageExtra{ID: "gbprinter", Order: gbpRowOrder, Icon: "emu-gbc", Row: gbpMoreRow(true), Open: gbpCardOpen})
	gamePageRegisterExtra(gamePageExtra{ID: "gbprinter-any", Order: gbpRowOrder2, Icon: "emu-gbc", Row: gbpMoreRow(false), Open: gbpCardOpen})
	gamePageRegisterOverlay(gamePageOverlay{
		ID:     "gbprinter-card",
		IsOpen: func() bool { return gbpCard.open },
		Key: func(e *sdl.KeyboardEvent) bool {
			gbpCardAction(tbxKeyAction(e))
			return true
		},
		Pad: func(e *sdl.ControllerButtonEvent) bool {
			gbpCardAction(tbxPadAction(e))
			return true
		},
		Mouse:  func(mx, my int32) bool { return gbpCardMouse(mx, my) },
		Render: gbpCardRender,
		Close:  func() { gbpCard.open = false },
	})
	// The page reads the scrapbook as it opens, so the More row can count a
	// game's prints from memory.
	gamePageRegisterOpened(func(game libraryGame) {
		if gbpIsGameBoy(game) && !gbpCountsLoaded.Load() {
			go func() {
				gbpPrints("")
				gbpCountsLoaded.Store(true)
			}()
		}
	})
}
