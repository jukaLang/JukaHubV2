package main

// chip8_library.go — CHIP-8 as a console of the Games library.
//
// The shelf is Roms/CHIP8 (also chip8, CHIP-8, "chip 8": the names Onion,
// Knulli, muOS and MinUI-style cards use, and NextUI's "(CHIP8)" tag through
// the same table). It lists .ch8, .c8, .sc8 and .xo8 — only there: those
// extensions are nothing anywhere else, so they are a shelf rule
// (romshelf.go) rather than rows of the shared extension table. There is no
// libretro thumbnail set for CHIP-8; covers come from the shelf's installer
// (chip8_archive.go) or the player's own first frames (chip8_scene.go).
//
// There is no emulator to find either: the games play in JukaHub's own
// interpreter (chip8_vm.go, chip8_scene.go). The library's launch path asks
// for an emulator before anything else — the game page, Continue, Home and
// the recents all say "No emulator found" when the answer is empty — so the
// console's one "emulator" is a stand-in launcher (chip8LibraryLauncher, a
// note in data/chip8 returned by chip8LauncherEntries from
// findGameLaunchersFor), and a launch gate (gamelaunch_hooks.go) takes
// every launch that names it. The gate does
// what launchLibraryGame does for a started game — the play-time limit, the
// recents, the first-launch milestone — reads the cartridge on a goroutine
// and opens the player scene. A game the player pinned to another emulator
// in its options still starts in that one.
//
// What the player remembers per game lives in data/chip8/games.json, keyed by
// the cartridge's SHA-1 so a renamed or moved file keeps it: a Mode or Speed
// chosen in the pause menu, the key map, sound on or off, and the
// SUPER-CHIP RPL flags (a game's "save", eight or sixteen bytes). The options
// the archive gave an installed game are in data/chip8/installed.json under
// the same key; a file copied in with an Octo options file beside it
// ("Game.json") gets those.

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// chip8System is the Games library's name for the console.
	chip8System = "CHIP-8"
	// chip8StandInName is the stand-in launcher's file name, which is what
	// the Game Ready report names as the game's emulator.
	chip8StandInName = "JukaHub CHIP-8 player"
	// chip8MaxROM bounds a cartridge read: XO-CHIP's whole memory.
	chip8MaxROM = chip8MaxProgram
)

// chip8Folders are the Roms folder names the shelf is found under (lower
// case: the alias table is matched without case).
var chip8Folders = []string{"chip8", "chip-8", "chip 8"}

// chip8Extensions are the files the shelf lists.
var chip8Extensions = map[string]bool{".ch8": true, ".c8": true, ".sc8": true, ".xo8": true}

func init() {
	for _, folder := range chip8Folders {
		if _, taken := romSystemAliases[folder]; !taken {
			romSystemAliases[folder] = romSystem{Name: chip8System}
		}
	}
	romShelfRegister(chip8System, func(_, _, ext string) bool { return chip8Extensions[ext] })
	gameLaunchRegisterGate(chip8Gate)
	gamePageRegisterExtra(gamePageExtra{ID: "chip8-get", Order: 75, Icon: "rom", Row: chip8PageGetRow, Open: func(libraryGame) { chip8ShelfOpen(appConfig) }})
	gamePageRegisterOpened(chip8PageOpened)
	gamePageRegisterStatus(gamePageStatus{ID: "chip8", Order: 12, Chip: chip8PageChip})
}

// chip8LauncherEntries is the CHIP-8 console's emulator list for
// findGameLaunchersFor: the built-in player only.
func chip8LauncherEntries(system string) ([]emuEntry, bool) {
	if system != chip8System {
		return nil, false
	}
	return []emuEntry{{Name: "JukaHub CHIP-8", Detail: "Built-in player", Status: emuAvailable, Launch: chip8LibraryLauncher()}}, true
}

// chip8StandIn is the stand-in launcher: a small note in data/chip8, so the
// checks that look for an emulator's file on the card (the game's diagnosis,
// "the emulator picked last") find one and name it plainly. When the data
// folder cannot be written it is a name that is no file.
var chip8StandIn struct {
	once sync.Once
	path string
}

// chip8StandInText is the note's content.
const chip8StandInText = "CHIP-8 games play inside JukaHub. This file stands in for an emulator so the Games library\n" +
	"knows the CHIP-8 shelf can play; it is safe to delete (JukaHub writes it again when it next starts).\n"

// chip8LibraryLauncher is the stand-in launcher's path. Any goroutine.
func chip8LibraryLauncher() string {
	chip8StandIn.once.Do(func() {
		chip8StandIn.path = "chip8:/" + chip8StandInName
		base, err := P().DataDir()
		if err != nil || base == "" {
			return
		}
		dir := filepath.Join(base, "chip8")
		p := filepath.Join(dir, chip8StandInName)
		if !regularFile(p) {
			if os.MkdirAll(dir, 0o755) != nil || os.WriteFile(p, []byte(chip8StandInText), 0o644) != nil {
				return
			}
		}
		chip8StandIn.path = p
	})
	return chip8StandIn.path
}

// chip8IsLauncher reports the stand-in (or no launcher at all).
func chip8IsLauncher(launcher string) bool {
	l := strings.TrimSpace(launcher)
	return l == "" || l == chip8LibraryLauncher() || filepath.Base(l) == chip8StandInName
}

// chip8LauncherChosen reports an outside launcher picked on purpose: the
// emulator pinned in the game's options, or one configured for the console.
// Any other — an emulator remembered from before the built-in player (the
// game's "last emulator", which Continue and the AI tools resolve), a NextUI
// pak — is not, and the game plays here, as the emulator picker offers it.
// Main thread (the launch path).
func chip8LauncherChosen(game libraryGame, launcher string) bool {
	l := strings.TrimSpace(launcher)
	if l == "" {
		return false
	}
	if l == gameLauncherPinned(gameOptionsFor(game.Path)) {
		return true
	}
	return l == configuredGameLauncher(game.System, loadGameLauncherOverrides(gameLauncherOverridesPath()))
}

// chip8Loading is set while a cartridge is being read for the player, so a
// second press does not start a second load.
var chip8Loading atomic.Bool

// chip8Gate takes a CHIP-8 launch for the built-in player. Main thread.
func chip8Gate(game libraryGame, launcher string) (bool, error) {
	if game.System != chip8System {
		return false, nil
	}
	if !chip8IsLauncher(launcher) && chip8LauncherChosen(game, launcher) {
		return false, nil // an emulator pinned for this game
	}
	if chip8Loading.Load() {
		return true, nil // the first press is still loading it
	}
	if err := playtimeAllowGameLaunch(game.Title); err != nil {
		return false, err
	}
	// What launchLibraryGame does for every game it starts.
	gmetaNoteLaunch(game)
	recordGameLaunch(game)
	gameLaunchStarts++ // started, not held: callers say "Playing …"
	chip8Loading.Store(true)
	playedAt := time.Now()
	go func() {
		ld, err := chip8LoadGame(game)
		tbxPost(func() {
			chip8Loading.Store(false)
			if err != nil {
				PlayErrorSound()
				ShowToast("Cannot start "+game.Label()+": "+err.Error(), ToastKindError)
				return
			}
			ld.playedAt = playedAt
			chip8SceneOpen(appConfig, ld)
		})
	}()
	return true, nil
}

// chip8Loaded is a cartridge ready for the player: its bytes, its key, what
// it wants, and the player's own settings for it.
type chip8Loaded struct {
	game     libraryGame
	rom      []byte
	hash     string
	base     chip8Options // the game's own (archive, options file or file type)
	settings chip8GameSettings
	playedAt time.Time
	// coverPath is where the player saves the game's screen as its cover
	// ("" when it has one, or the player removed it).
	coverPath string
}

// chip8ReadROM reads a cartridge with its size checked.
func chip8ReadROM(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, errors.New("the file is not there any more")
	}
	if info.IsDir() || info.Size() == 0 {
		return nil, errors.New("the file is empty")
	}
	if info.Size() > chip8MaxROM {
		return nil, errors.New("the file is too big to be a CHIP-8 game")
	}
	return os.ReadFile(path)
}

// chip8HashROM is the key a cartridge's settings are kept under.
func chip8HashROM(rom []byte) string {
	sum := sha1.Sum(rom)
	return hex.EncodeToString(sum[:])
}

// chip8LoadGame reads everything the player needs. Off the main thread.
func chip8LoadGame(game libraryGame) (chip8Loaded, error) {
	rom, err := chip8ReadROM(game.Path)
	if err != nil {
		return chip8Loaded{}, err
	}
	ld := chip8Loaded{game: game, rom: rom, hash: chip8HashROM(rom)}
	ld.base = chip8GameOptions(game.Path, rom, ld.hash)
	ld.settings = chip8SettingsFor(ld.hash)
	if root := libraryArtworkRoot(); root != "" {
		sr := scannedROM{Path: game.Path, System: chip8System}
		// romCoverPath is "" for a cover the player removed (Game options →
		// Remove cover), and the download path when there is none.
		if c := romCoverPath(sr, root); c != "" && !artworkCoverPresent(c) {
			ld.coverPath = romArtworkPath(sr, root)
		}
	}
	return ld, nil
}

// chip8GameOptions is a cartridge's own options: the archive's for a game
// the shelf installed, an Octo options file beside it, or its file type's
// preset. Off the main thread.
func chip8GameOptions(path string, rom []byte, hash string) chip8Options {
	if rec, ok := chip8InstalledFor(hash); ok {
		plat, _ := chip8PlatformFromName(rec.Platform)
		var raw map[string]json.RawMessage
		_ = json.Unmarshal(rec.Options, &raw)
		return chip8ApplyOptions(chip8OctoDefaults(plat), raw)
	}
	if o, ok := chip8SidecarOptions(path); ok {
		return o
	}
	return chip8Preset(chip8GuessPlatform(strings.ToLower(filepath.Ext(path)), rom))
}

// chip8SidecarMax bounds an options file read.
const chip8SidecarMax = 64 << 10

// chip8SidecarOptions reads "Game.json" beside "Game.ch8": an Octo options
// object, or an archive-style entry with "platform" and "options".
func chip8SidecarOptions(path string) (chip8Options, bool) {
	side := strings.TrimSuffix(path, filepath.Ext(path)) + ".json"
	info, err := os.Stat(side)
	if err != nil || info.IsDir() || info.Size() > chip8SidecarMax {
		return chip8Options{}, false
	}
	raw, err := os.ReadFile(side)
	if err != nil {
		return chip8Options{}, false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return chip8Options{}, false
	}
	plat := chip8PlatChip8
	if s, ok := chip8JSONString(obj["platform"]); ok {
		plat, _ = chip8PlatformFromName(s)
	}
	opts := obj
	if inner, ok := obj["options"]; ok {
		var m map[string]json.RawMessage
		if json.Unmarshal(inner, &m) == nil {
			opts = m
		}
	}
	o := chip8ApplyOptions(chip8OctoDefaults(plat), opts)
	o.Source = "options file"
	return o, true
}

// ── Per-game settings ────────────────────────────────────────────────────────

// chip8GameSettings is what the player remembers for one cartridge.
type chip8GameSettings struct {
	Title string `json:"title,omitempty"`
	// Mode is a preset picked in the pause menu ("chip8", "schip", "xochip");
	// empty plays the game's own options.
	Mode string `json:"mode,omitempty"`
	// Tickrate is a Speed picked in the pause menu; 0 is the game's own.
	Tickrate int `json:"tickrate,omitempty"`
	// Keys maps a pad button ("up", "a") to a hex key, -1 for none; missing
	// buttons keep the default map.
	Keys  map[string]int `json:"keys,omitempty"`
	Mute  bool           `json:"mute,omitempty"`
	Flags []byte         `json:"flags,omitempty"`
}

var chip8Store struct {
	mu      sync.Mutex
	loaded  bool
	games   map[string]chip8GameSettings
	saving  bool
	pending bool
}

// chip8DataDir is data/chip8, created on demand.
func chip8DataDir() string {
	dir := filepath.Join("data", "chip8")
	if base, err := P().DataDir(); err == nil && base != "" {
		dir = filepath.Join(base, "chip8")
	}
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

func chip8SettingsPath() string { return filepath.Join(chip8DataDir(), "games.json") }

// chip8LoadStoreLocked reads games.json once; a missing or damaged file is an
// empty store (the atomic save's .bak is tried first).
func chip8LoadStoreLocked() {
	if chip8Store.loaded {
		return
	}
	chip8Store.loaded = true
	chip8Store.games = map[string]chip8GameSettings{}
	var games map[string]chip8GameSettings
	if _, err := readJSONWithBackup(chip8SettingsPath(), &games); err != nil {
		if !os.IsNotExist(err) {
			logWarn("chip8", "ignoring %s: %v", chip8SettingsPath(), err)
		}
		return
	}
	for k, g := range games {
		if len(k) == 40 {
			chip8Store.games[k] = g
		}
	}
}

// chip8SettingsFor is one cartridge's settings, a copy the caller may change
// (its map and slice are its own: the store's are written by the save
// goroutine's marshal). Any goroutine.
func chip8SettingsFor(hash string) chip8GameSettings {
	chip8Store.mu.Lock()
	defer chip8Store.mu.Unlock()
	chip8LoadStoreLocked()
	g := chip8Store.games[hash]
	if g.Keys != nil {
		keys := make(map[string]int, len(g.Keys))
		for k, v := range g.Keys {
			keys[k] = v
		}
		g.Keys = keys
	}
	g.Flags = append([]byte(nil), g.Flags...)
	return g
}

// chip8SettingsUpdate changes one cartridge's settings and saves the store
// on a goroutine (one write at a time; a change during a write is written
// right after it).
func chip8SettingsUpdate(hash string, fn func(*chip8GameSettings)) {
	if hash == "" || fn == nil {
		return
	}
	chip8Store.mu.Lock()
	chip8LoadStoreLocked()
	g := chip8Store.games[hash]
	fn(&g)
	chip8Store.games[hash] = g
	if chip8Store.saving {
		chip8Store.pending = true
		chip8Store.mu.Unlock()
		return
	}
	chip8Store.saving = true
	chip8Store.mu.Unlock()
	go chip8SaveStoreLoop()
}

func chip8SaveStoreLoop() {
	for {
		chip8Store.mu.Lock()
		data, err := json.MarshalIndent(chip8Store.games, "", "  ")
		chip8Store.pending = false
		chip8Store.mu.Unlock()
		if err == nil {
			if werr := AtomicWrite(chip8SettingsPath(), append(data, '\n'), 0o644); werr != nil {
				logWarn("chip8", "saving the player's settings: %v", werr)
			}
		}
		chip8Store.mu.Lock()
		if !chip8Store.pending {
			chip8Store.saving = false
			chip8Store.mu.Unlock()
			return
		}
		chip8Store.mu.Unlock()
	}
}

// chip8EffectiveOptions is what a game plays with: its own options, or the
// preset of the Mode picked for it (keeping its colours when it had its
// own), with the Speed picked for it.
func chip8EffectiveOptions(base chip8Options, s chip8GameSettings) chip8Options {
	o := base
	if p, ok := chip8PlatformFromName(s.Mode); ok && s.Mode != "" {
		o = chip8Preset(p)
		if base.Source != "file type" {
			o.Colors = base.Colors
			o.Rotation = base.Rotation
			o.Buzz = base.Buzz
		}
		o.Keys = base.Keys
		o.Source = "picked in the menu"
	}
	if s.Tickrate > 0 {
		o.Tickrate = min(s.Tickrate, chip8MaxTickrate)
	}
	if o.Tickrate <= 0 {
		o.Tickrate = 20
	}
	return o
}

// chip8DefaultKeys is the Octo convention on a pad: W A S D (5 7 8 9) on
// the D-pad, E (6) on A and Q (4) on B.
var chip8DefaultKeys = map[string]int{"up": 5, "down": 8, "left": 7, "right": 9, "a": 6, "b": 4}

// chip8KeyMap is the pad map a game plays with: the default, then the key
// information its entry carried, then the player's own choices.
func chip8KeyMap(o chip8Options, s chip8GameSettings) map[string]int {
	out := make(map[string]int, len(chip8KeyNames))
	for _, name := range chip8KeyNames {
		out[name] = -1
	}
	for k, v := range chip8DefaultKeys {
		out[k] = v
	}
	for k, v := range o.Keys {
		out[k] = v
	}
	for k, v := range s.Keys {
		if _, known := out[k]; known && v >= -1 && v < 16 {
			out[k] = v
		}
	}
	return out
}

// ── Installed archive games ─────────────────────────────────────────────────

// chip8InstallRec is one game the shelf installed: its archive key and
// entry, and where it went.
type chip8InstallRec struct {
	Key      string          `json:"key"`
	Title    string          `json:"title"`
	Authors  string          `json:"authors,omitempty"`
	Platform string          `json:"platform,omitempty"`
	Options  json.RawMessage `json:"options,omitempty"`
	Path     string          `json:"path"`
}

var chip8Installed struct {
	mu     sync.Mutex
	loaded bool
	byHash map[string]chip8InstallRec
}

func chip8InstalledPath() string { return filepath.Join(chip8DataDir(), "installed.json") }

func chip8LoadInstalledLocked() {
	if chip8Installed.loaded {
		return
	}
	chip8Installed.loaded = true
	chip8Installed.byHash = map[string]chip8InstallRec{}
	var recs map[string]chip8InstallRec
	if _, err := readJSONWithBackup(chip8InstalledPath(), &recs); err != nil {
		if !os.IsNotExist(err) {
			logWarn("chip8", "ignoring %s: %v", chip8InstalledPath(), err)
		}
		return
	}
	for h, r := range recs {
		if len(h) == 40 && r.Key != "" {
			chip8Installed.byHash[h] = r
		}
	}
}

// chip8InstalledFor is the shelf's record of a cartridge. Any goroutine.
func chip8InstalledFor(hash string) (chip8InstallRec, bool) {
	chip8Installed.mu.Lock()
	defer chip8Installed.mu.Unlock()
	chip8LoadInstalledLocked()
	r, ok := chip8Installed.byHash[hash]
	return r, ok
}

// chip8InstalledAll is every record (a copy). Any goroutine.
func chip8InstalledAll() map[string]chip8InstallRec {
	chip8Installed.mu.Lock()
	defer chip8Installed.mu.Unlock()
	chip8LoadInstalledLocked()
	out := make(map[string]chip8InstallRec, len(chip8Installed.byHash))
	for h, r := range chip8Installed.byHash {
		out[h] = r
	}
	return out
}

// chip8InstalledPut records an installed game and saves the index. Off the
// main thread (the installer's goroutine).
func chip8InstalledPut(hash string, rec chip8InstallRec) error {
	chip8Installed.mu.Lock()
	defer chip8Installed.mu.Unlock()
	chip8LoadInstalledLocked()
	chip8Installed.byHash[hash] = rec
	data, err := json.MarshalIndent(chip8Installed.byHash, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(chip8InstalledPath(), append(data, '\n'), 0o644)
}

// ── Play time ────────────────────────────────────────────────────────────────

// chip8CreditPlay adds a stretch of play to the game's own ledger. The
// seconds already count toward JukaHub's total (the player is a scene, so
// the frame ticks credit it); only the per-game figures are added here, as
// playtimeCreditGameGap does for an emulator. firstFrom is the launch time
// for the stretch that began with the launch (it closes the game history's
// session), zero for a later return to the paused game. Main thread.
func chip8CreditPlay(game libraryGame, secs int64, firstFrom time.Time) {
	if secs <= 0 || !playtime.loaded || strings.TrimSpace(game.Title) == "" {
		return
	}
	secs = min(secs, int64(playtimeGameMaxSession/time.Second))
	key := playtimeGameKey(game.System, game.Title)
	d := &playtime.data
	if d.Games == nil {
		d.Games = map[string]int64{}
	}
	if d.GameDays == nil {
		d.GameDays = map[string]int64{}
	}
	if d.Unlocked == nil {
		d.Unlocked = map[string]string{}
	}
	d.Games[key] += secs
	d.GameDays[playtimeTodayKey()] += secs
	trophyCountAdd(d, tkGameSecs, secs)
	dailyChallengeCredit(game.Path, secs)
	playtime.dirty = true
	playtime.rev++
	if !firstFrom.IsZero() {
		gameHistoryFinishGap(recentGames(), firstFrom, firstFrom.Add(time.Duration(secs)*time.Second))
	}
}

// ── The game page ───────────────────────────────────────────────────────────

// chip8PageGetRow is the More sheet's way to the shelf on a CHIP-8 game.
func chip8PageGetRow(game libraryGame) (string, string, bool) {
	if game.System != chip8System {
		return "", "", false
	}
	return "Get more CHIP-8 games", "Free homebrew from the chip8Archive", true
}

// chip8PageInfo is the open page's machine label, read when it opened.
var chip8PageInfo struct {
	path  string
	label string
}

// chip8PageOpened reads which machine the page's game is for. Main thread;
// the read is on a goroutine.
func chip8PageOpened(game libraryGame) {
	chip8PageInfo.path, chip8PageInfo.label = game.Path, ""
	if game.System != chip8System {
		return
	}
	path := game.Path
	go func() {
		rom, err := chip8ReadROM(path)
		if err != nil {
			return
		}
		hash := chip8HashROM(rom)
		o := chip8EffectiveOptions(chip8GameOptions(path, rom, hash), chip8SettingsFor(hash))
		label := o.Platform.label() + " · plays in JukaHub"
		tbxPost(func() {
			if chip8PageInfo.path == path {
				chip8PageInfo.label = label
			}
		})
	}()
}

// chip8PageChip is the page's chip: the machine, from memory.
func chip8PageChip(game libraryGame) (string, int, bool) {
	if game.System != chip8System || chip8PageInfo.path != game.Path || chip8PageInfo.label == "" {
		return "", 0, false
	}
	return chip8PageInfo.label, gamePageToneNeutral, true
}
