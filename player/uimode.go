package main

// uimode.go — Interface mode: Full, Kiosk and Kid: a handheld that can be
// handed to someone else — a child — without them changing or breaking the
// setup.
//
//   - Full is JukaHub as it always was.
//   - Kiosk is play-only: games, the game switcher, the music and video
//     players, favourites and collections work. Settings shrinks to one page
//     (volume, brightness, Wi-Fi on/off and "Leave Kiosk mode"), and every
//     screen and action that installs, deletes, renames, moves, updates or
//     configures something, or reaches the files, is refused.
//   - Kid is Kiosk with kid-friendly content only: the library, Home's game
//     rows, search, the switcher and the random pick show the games marked
//     "For kids" (a tag on the game's details record, gmeta_store.go, set from
//     the game page's More sheet — uimode_kids.go) and every game of a console
//     marked "whole console". What comes from the internet — streaming,
//     remote play, cloud gaming, Steam — goes too (the Browser, Chat, Mail,
//     the AI and the Party and Voice rooms are not Kiosk screens either).
//     Time limits are playtime_limits.go's rows; the Settings page links to
//     them.
//
// One gate. uiModeAllows(feature) answers for a kind of feature,
// uiModeSceneOK for a screen, and uiModeGate says "Locked in Kid mode" when
// it refuses. The shared files only call in here, at their choke points:
//   - switchScene (core.go): every screen change. A refused one stays put; a
//     Back through the history to a locked screen lands on Home instead;
//   - startMenuGroups (startmenu.go): the Start menu lists only what is
//     allowed, plus Unlock. Home's app tiles resolve through the same list
//     (homeAppEntries), so a pinned locked tool says it is not available;
//   - setSections (setmodel.go): Settings is the one small page while locked;
//   - the game options sheet, the game page's tags and More sheet, Edit Home,
//     Quick Settings' edit, the power card's Exit and the Select + Start quit
//     chord (exit_chord.go), Home's header cards, the update offer, the voice
//     orb and the phone companion's acting endpoints (HTTP 423);
//   - the library's console list (currentGameSystems — the random pick and
//     the library search read it) and the recents views, in Kid mode;
//   - Home search (globalsearch.go): only games and the allowed screens;
//   - the downloads behind the online rows Kiosk keeps: GreenOvercast for the
//     X1 tile and Cloud Gaming (goInstall), PS Remote Play's and Moonlight's
//     helpers (psHelpersStartFor, silently, and the panel's install row), and
//     Steam's port install, file copy and PC installs (steam_ports.go,
//     steam_remote.go steamRemoteDo, steam_ui.go steamLocalDo);
//   - launchLibraryGame's first line, which refuses a game not for kids
//     whatever view it came from (ports, Android and HTML5 games included),
//     and the embedded player, which plays no web address in Kid mode.
//
// Device-wide, not per profile: profiles (profiles_*.go) are switched from the
// Start menu by whoever holds the handheld, so a per-profile lock would be one
// switch away from gone. The profile switcher is locked with the rest.
//
// Leaving (uimode_unlock.go): the parent code — 4 to 8 presses of the D-pad,
// A, B, X, Y, L1 and R1, the startup lock's button code hashed the same way
// (bootLockHash) — chosen when the mode is turned on and typed on the Unlock
// card (Start menu → Unlock, or Settings → Leave). Without a code the Konami
// code leaves, anywhere: Up Up Down Down Left Right Left Right B A, as on
// Batocera. With a code set, the Konami code opens the Unlock card instead: a
// combination anyone can look up must not undo the code.
//
// Never locked out: deleting data/ui-mode.json from a computer is Full mode
// again, and so is starting JukaHub with JUKAHUB_UI_MODE=full (launch.sh, or a
// PC's shell). docs/KID_MODE.md says both.
//
// Storage: data/ui-mode.json — the mode, the code's salted PBKDF2 hash (never
// the code), the wrong-try count and the consoles marked "whole console".

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// The three modes, as stored.
const (
	uiModeFull  = "full"
	uiModeKiosk = "kiosk"
	uiModeKid   = "kid"
)

const (
	uiModeFileName    = "ui-mode.json"
	uiModeFileVersion = 1
	// uiModeEnvVar=full starts JukaHub in Full mode whatever the file says,
	// and saves it: the escape hatch for a forgotten code.
	uiModeEnvVar = "JUKAHUB_UI_MODE"
	// uiModeMaxConsoles bounds the "whole console" list a hand edit can grow.
	uiModeMaxConsoles = 200
)

// The kinds of feature the gate answers for.
const (
	// uiFeatPlay is playing and watching: every mode.
	uiFeatPlay = "play"
	// uiFeatOnline is the internet things (streaming, remote play, cloud
	// gaming, the AI): Full and Kiosk.
	uiFeatOnline = "online"
	// uiFeatSetup changes, installs, deletes or configures: Full only.
	uiFeatSetup = "setup"
)

// uiModeFile is data/ui-mode.json.
type uiModeFile struct {
	Version int    `json:"version"`
	Mode    string `json:"mode"`
	// Code is the parent code as the startup lock stores its button code
	// ("jhlock1$code$…", bootLockHash); "" for none.
	Code string `json:"code,omitempty"`
	// Fails counts wrong codes in a row; it survives a restart.
	Fails int `json:"fails,omitempty"`
	// KidConsoles are the consoles whose every game is for kids.
	KidConsoles []string `json:"kidConsoles,omitempty"`
}

var uiModeStore struct {
	mu     sync.Mutex
	loaded bool
	file   uiModeFile
}

// uiModeLevel mirrors the mode for any goroutine (the companion's HTTP
// handlers ask): 0 Full, 1 Kiosk, 2 Kid. uiModeReady is set once the file
// was read.
var (
	uiModeLevel atomic.Int32
	uiModeReady atomic.Bool
)

// uiModePath is the file in JukaHub's data folder; a variable for tests.
var uiModePath = func() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, uiModeFileName)
}

// ── The file (pure parts) ─────────────────────────────────────────────────

// uiModeNormalize keeps a read or hand-edited file sane: an unknown mode is
// Full, a code that is not a startup-lock button-code hash is no code, the
// count is bounded and the consoles are trimmed and unique.
func uiModeNormalize(f uiModeFile) uiModeFile {
	f.Version = uiModeFileVersion
	f.Mode = strings.ToLower(strings.TrimSpace(f.Mode))
	switch f.Mode {
	case uiModeKiosk, uiModeKid:
	default:
		f.Mode = uiModeFull
	}
	f.Code = strings.TrimSpace(f.Code)
	if f.Code != "" {
		if st, ok := bootLockParse(f.Code); !ok || st.kind != bootLockKindCode {
			f.Code = ""
		}
	}
	if f.Fails < 0 {
		f.Fails = 0
	}
	if f.Fails > 1000 {
		f.Fails = 1000
	}
	var consoles []string
	seen := map[string]bool{}
	for _, c := range f.KidConsoles {
		c = strings.TrimSpace(c)
		key := strings.ToLower(c)
		if c == "" || seen[key] || len(consoles) >= uiModeMaxConsoles {
			continue
		}
		seen[key] = true
		consoles = append(consoles, c)
	}
	sort.Strings(consoles)
	f.KidConsoles = consoles
	return f
}

// uiModeLoad reads the file; absent or unreadable is Full mode with no code.
func uiModeLoad(path string) uiModeFile {
	if strings.TrimSpace(path) == "" {
		return uiModeNormalize(uiModeFile{})
	}
	var f uiModeFile
	if _, err := readJSONWithBackup(path, &f); err != nil {
		if !os.IsNotExist(err) {
			logWarn("uimode", "ignoring %s: %v", path, err)
		}
		return uiModeNormalize(uiModeFile{})
	}
	return uiModeNormalize(f)
}

// uiModeSave writes the file at once (small; a wrong try counted must
// survive pulling the battery).
func uiModeSave(path string, f uiModeFile) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	data, err := json.MarshalIndent(uiModeNormalize(f), "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(path, append(data, '\n'), 0o644)
}

// uiModeLevelOf is a mode's level (uiModeLevel).
func uiModeLevelOf(mode string) int32 {
	switch mode {
	case uiModeKiosk:
		return 1
	case uiModeKid:
		return 2
	}
	return 0
}

// ── State ─────────────────────────────────────────────────────────────────

// uiModeEnsure reads the file once. Any goroutine.
func uiModeEnsure() {
	if uiModeReady.Load() {
		return
	}
	uiModeStore.mu.Lock()
	uiModeEnsureLocked()
	uiModeStore.mu.Unlock()
}

// uiModeEnsureLocked is uiModeEnsure with uiModeStore.mu held.
func uiModeEnsureLocked() {
	if uiModeStore.loaded {
		return
	}
	uiModeStore.loaded = true
	path := uiModePath()
	f := uiModeLoad(path)
	if strings.EqualFold(strings.TrimSpace(os.Getenv(uiModeEnvVar)), uiModeFull) && f.Mode != uiModeFull {
		logWarn("uimode", "%s=full: leaving %s mode", uiModeEnvVar, f.Mode)
		f.Mode, f.Code, f.Fails = uiModeFull, "", 0
		if err := uiModeSave(path, f); err != nil {
			logWarn("uimode", "saving Full mode: %v", err)
		}
	}
	uiModeStore.file = f
	uiModeLevel.Store(uiModeLevelOf(f.Mode))
	uiModeReady.Store(true)
	if f.Mode != uiModeFull {
		logInfo("uimode", "starting in %s mode (parent code: %v)", f.Mode, f.Code != "")
	}
}

// uiModeSnapshot is a copy of the file as it stands.
func uiModeSnapshot() uiModeFile {
	uiModeStore.mu.Lock()
	defer uiModeStore.mu.Unlock()
	uiModeEnsureLocked()
	f := uiModeStore.file
	f.KidConsoles = append([]string(nil), f.KidConsoles...)
	return f
}

// uiModeUpdate changes the file and writes it.
func uiModeUpdate(change func(f *uiModeFile)) uiModeFile {
	uiModeStore.mu.Lock()
	uiModeEnsureLocked()
	f := uiModeStore.file
	f.KidConsoles = append([]string(nil), f.KidConsoles...)
	change(&f)
	f = uiModeNormalize(f)
	uiModeStore.file = f
	uiModeLevel.Store(uiModeLevelOf(f.Mode))
	uiModeStore.mu.Unlock()
	if err := uiModeSave(uiModePath(), f); err != nil {
		logWarn("uimode", "saving %s: %v", uiModeFileName, err)
		ShowToast("Could not save the interface mode: "+err.Error(), ToastKindWarning)
	}
	return f
}

// uiModeNow is the mode in force. Any goroutine.
func uiModeNow() string {
	uiModeEnsure()
	switch uiModeLevel.Load() {
	case 1:
		return uiModeKiosk
	case 2:
		return uiModeKid
	}
	return uiModeFull
}

// uiModeLocked reports Kiosk or Kid mode. Any goroutine.
func uiModeLocked() bool {
	uiModeEnsure()
	return uiModeLevel.Load() > 0
}

// uiModeIsKid reports Kid mode. Any goroutine.
func uiModeIsKid() bool {
	uiModeEnsure()
	return uiModeLevel.Load() == 2
}

// uiModeLabel is a mode's name in the interface.
func uiModeLabel(mode string) string {
	switch mode {
	case uiModeKiosk:
		return "Kiosk"
	case uiModeKid:
		return "Kid"
	}
	return "Full"
}

// ── The gate ──────────────────────────────────────────────────────────────

// uiModeAllows answers for a kind of feature (uiFeat*) in the mode in force.
// An unknown kind is setup: refused while locked.
func uiModeAllows(feat string) bool {
	switch feat {
	case uiFeatPlay:
		return true
	case uiFeatOnline:
		return !uiModeIsKid()
	}
	return !uiModeLocked()
}

// uiModeRefusedText is the toast for a refused feature.
func uiModeRefusedText() string {
	return "Locked in " + uiModeLabel(uiModeNow()) + " mode"
}

// uiModeGate is uiModeAllows that says so when it refuses: the hook lines in
// the shared files read `if !uiModeGate(uiFeatSetup) { return }`. Main thread.
func uiModeGate(feat string) bool {
	if uiModeAllows(feat) {
		return true
	}
	PlayErrorSound()
	ShowToast(uiModeRefusedText(), ToastKindWarning)
	return false
}

// uiModeOrbBlocked keeps the voice orb (voice commands and the AI) down while
// locked: a spoken "open Settings" or "install" must not go round the lock.
func uiModeOrbBlocked() bool { return uiModeLocked() }

// uiModeCompanionLocked makes the phone companion's acting endpoints answer
// 423 while locked (companion.go guard): files sent, apps installed and keys
// typed from a phone would go round the lock. Any goroutine.
func uiModeCompanionLocked() bool { return uiModeLocked() }

// ── Screens ───────────────────────────────────────────────────────────────

// uiModeKioskScenes are the screens Kiosk mode keeps: play, listen, watch,
// and Settings (which is then the one small page, uiModeSettingsSections).
var uiModeKioskScenes = map[string]bool{
	"Main": true, "Home": true, "Favorites": true, "Settings": true,
	"MusicPlayer": true, "Recordings": true, "PDFReader": true,
	"IPStream": true, "Podcasts": true, "LiveTV": true, "Tube": true, "Shorts": true,
	"Games": true, "JukaLand": true,
	gameSwitcherSceneName: true, playtimeSceneName: true, synthSceneName: true,
	solitaireSceneName: true, chip8SceneName: true, paintSceneName: true,
	stopwatchSceneName: true, gbpSceneName: true, cloudGamingSceneName: true,
}

// uiModeOnlineScenes are the Kiosk screens Kid mode drops: they stream from
// the internet.
var uiModeOnlineScenes = map[string]bool{
	"IPStream": true, "Podcasts": true, "LiveTV": true, "Tube": true, "Shorts": true, cloudGamingSceneName: true,
}

// uiModeSceneOK reports whether a screen may open in the mode in force.
func uiModeSceneOK(name string) bool {
	if !uiModeLocked() {
		return true
	}
	if !uiModeKioskScenes[name] {
		return false
	}
	return !(uiModeOnlineScenes[name] && uiModeIsKid())
}

// uiModePlayerPass is set while ensureVideoScene (core.go) opens the screen
// the embedded player draws on. That screen is Media's (Tube), which Kid mode
// keeps shut; without it a video on the card plays as sound alone. A web
// address never gets this far in Kid mode (uiModeStreamOK). Main thread.
var uiModePlayerPass bool

// uiModePlayerScene is ensureVideoScene's changeSceneTo with the pass up.
func uiModePlayerScene(config *Config, idx int) {
	uiModePlayerPass = true
	changeSceneTo(config, idx)
	uiModePlayerPass = false
}

// uiModeSceneTarget is switchScene's hook (core.go): the scene to switch to,
// or -1 to stay. A refused screen opened on purpose (record) says so; a Back
// through the history to one goes Home instead, quietly.
func uiModeSceneTarget(config *Config, targetIdx int, record bool) int {
	if config == nil || targetIdx < 0 || targetIdx >= len(config.Scenes) || !uiModeLocked() {
		return targetIdx
	}
	scene := config.Scenes[targetIdx]
	if scene.Layout == "home" || uiModeSceneOK(scene.Name) || uiModePlayerPass {
		return targetIdx
	}
	if record {
		uiModeGate(uiFeatSetup)
		return -1
	}
	return homeSceneIndex(config)
}

// uiModeSearchSources is Home search's hook (globalsearch.go homeSearchRun):
// while locked it keeps the games (the kids' games in Kid mode), what was
// played and the screens the mode allows. The apps, the files, the Settings
// rows and every catalogue action that opens a panel (App Center, Emulators,
// Wallpapers, Toolbox…) go: they would reach round the Start menu's filter.
// Main thread.
func uiModeSearchSources(src *homeSearchSources) {
	if src == nil || !uiModeLocked() {
		return
	}
	src.games = uiModeKidRecents(src.games)
	src.apps, src.files = nil, nil
	screens := make([]homeSearchScreen, 0, len(src.screens))
	for _, s := range src.screens {
		if s.scene != "" && uiModeSceneOK(s.scene) {
			screens = append(screens, s)
		}
	}
	src.screens = screens
	recent := make([]FavoriteItem, 0, len(src.recent))
	for _, it := range src.recent {
		if it.Type == "file" { // a file opens in Files
			continue
		}
		if g, ok := it.Data.(recentGame); ok && it.Type == "game" && len(uiModeKidRecents([]recentGame{g})) == 0 {
			continue
		}
		recent = append(recent, it)
	}
	src.recent = recent
}

// uiModeStreamOK is the players' hook (playVideoInfo, playStream,
// openAudioPlayer): in Kid mode a web address does not play, so a favourite,
// the Continue card, a search's history, a podcast or a YouTube link cannot
// stream what Kid mode hid. A file on the device plays in every mode. It says
// so when it refuses.
func uiModeStreamOK(url string) bool {
	if !uiModeIsKid() || !strings.Contains(url, "://") || strings.HasPrefix(strings.ToLower(url), "file://") {
		return true
	}
	return uiModeGate(uiFeatOnline)
}

// ── Start menu ────────────────────────────────────────────────────────────

// uiModeUnlockEntryName is the Start menu's row while locked.
const uiModeUnlockEntryName = "Unlock"

// uiModeMenuActions are the Start menu's action rows Kiosk mode keeps, with
// the kind of feature each is.
var uiModeMenuActions = map[string]string{
	gameSwitcherEntryName:   uiFeatPlay,
	gamesLibraryEntryName:   uiFeatPlay,
	randomPlayEntryName:     uiFeatPlay,
	"Quick Settings":        uiFeatPlay,
	"Screenshot":            uiFeatPlay,
	"Sleep Screen":          uiFeatPlay,
	"Ambient":               uiFeatPlay,
	"PS Remote Play":        uiFeatOnline,
	xboxRemotePlayEntryName: uiFeatOnline,
	moonlightEntryName:      uiFeatOnline,
	cloudGamingEntryName:    uiFeatOnline,
	steamEntryName:          uiFeatOnline,
}

// uiModeMenuEntryOK reports whether a Start menu row stays.
func uiModeMenuEntryOK(e startMenuEntry) bool {
	if e.scene != "" {
		return uiModeSceneOK(e.scene)
	}
	feat, listed := uiModeMenuActions[e.name]
	return listed && uiModeAllows(feat)
}

// uiModeStartMenu is startMenuGroups' hook: while locked only the allowed
// rows stay, groups left empty go, and Device ends with Unlock.
func uiModeStartMenu(config *Config, groups []startMenuGroup) []startMenuGroup {
	if !uiModeLocked() {
		return groups
	}
	out := make([]startMenuGroup, 0, len(groups))
	for _, g := range groups {
		var items []startMenuEntry
		for _, e := range g.items {
			if uiModeMenuEntryOK(e) {
				items = append(items, e)
			}
		}
		if g.name == "Device" {
			items = append(items, startMenuEntry{
				name: uiModeUnlockEntryName, icon: "U", glyph: "lock",
				badge:  uiModeLabel(uiModeNow()) + " mode",
				action: func(c *Config) { uiModeOpenUnlock(c) },
			})
		}
		if len(items) == 0 {
			continue
		}
		g.items = items
		out = append(out, g)
	}
	return out
}

// ── Home ──────────────────────────────────────────────────────────────────

// uiModeHomeCardOK is homeActivate's hook (home.go) for the header cards that
// do not go through a screen change: the update chip, the pairing code, Edit
// and the remote-play tiles. It says so when it refuses.
func uiModeHomeCardOK(id HomeFocusID) bool {
	switch id {
	case HomeFocusUpdate, HomeFocusQR, HomeFocusEditPill, HomeFocusBug:
		return uiModeGate(uiFeatSetup)
	case HomeFocusPS5, HomeFocusPS4, HomeFocusX1, HomeFocusPC, HomeFocusBrowser, HomeFocusChat:
		return uiModeGate(uiFeatOnline)
	}
	return true
}

// uiModeQuitOK is the power card's Exit (home_power.go) and the Select +
// Start chord (exit_chord.go): on a stock firmware leaving JukaHub is its
// menu, every setting one press away. As the OS itself, Exit is Power off
// and stays.
func uiModeQuitOK() bool {
	if jukaosNativeActive() {
		return true
	}
	return uiModeGate(uiFeatSetup)
}

// ── Applying a change ─────────────────────────────────────────────────────

// uiModeChanged puts the screens in step after the mode changed: the library
// lists, Settings' page, and — when a lock begins — Home, with no history
// to go Back through to a locked screen. Main thread.
func uiModeChanged(config *Config, locked bool) {
	invalidateGameLibrarySystemsCache()
	setHub.section, setHub.lastSection = 0, 0
	setHub.row, setHub.inPane, setHub.onSearch = 0, false, false
	setHub.dirty = true
	if !locked || config == nil {
		return
	}
	// The library, a game's page and their sheets are panels over the
	// screens: none of them stays open into the lock.
	closeGameLibraryPanel()
	sceneHistory = sceneHistory[:0]
	if home := homeSceneIndex(config); home >= 0 && home != currentSceneIndex {
		switchScene(config, home, false)
	}
}

// uiModeTurnOn starts a mode with the code just chosen (its hash, or "").
func uiModeTurnOn(config *Config, mode, code string) {
	uiModeUpdate(func(f *uiModeFile) {
		f.Mode, f.Code, f.Fails = mode, code, 0
	})
	logInfo("uimode", "%s mode on (parent code: %v)", mode, code != "")
	uiModeChanged(config, true)
	how := "Start menu → Unlock to leave"
	if code == "" {
		how = "the Konami code leaves"
	}
	ShowToast(uiModeLabel(mode)+" mode is on · "+how, ToastKindSuccess)
}

// uiModeLeave is Full mode again; the code goes with the mode.
func uiModeLeave(config *Config) {
	was := uiModeNow()
	uiModeUpdate(func(f *uiModeFile) {
		f.Mode, f.Code, f.Fails = uiModeFull, "", 0
	})
	logInfo("uimode", "left %s mode", was)
	// A launch Kid mode refused is not held any more (uiModeLaunchHeld).
	gameLaunchHeld = ""
	uiModeChanged(config, false)
	PlayActivateSound()
	ShowToast("Full mode · everything is unlocked", ToastKindSuccess)
}
