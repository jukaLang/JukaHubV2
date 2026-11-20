package main

// profiles_saves.go — each profile's game saves (profiles_store.go).
//
// RetroArch: a game started from the library while someone other than the
// owner plays gets that profile's own save and state folders for the launch
// (<data>/profiles/<id>/saves and states; a Guest's are wiped when the session
// ends), through the same scoped retroarch.cfg edit the resume switches use
// (racfgscope.go): the owner's values are back the moment the game exits, and
// a game started from the firmware's own menu never sees them. The same
// launch switches RetroAchievements off, so nobody else's unlocks land on the
// owner's account.
//
// Standalone emulators (PPSSPP, DraStic, PortMaster ports, …) keep one save
// folder of their own that JukaHub cannot point elsewhere. Their saves are
// shared with the owner, and that is never left silent: the first start of
// each one per profile and session stops with a notice, and the second start
// plays. The profile's page lists them too.
//
// JukaHub's own save tools — Save Manager, Google Drive and RomM sync, the
// handoff of saves to another device — read RetroArch's configured folders,
// which outside a launch are the owner's: they work on the owner's saves only,
// so they can never mix a profile's saves into the owner's. Save Manager,
// which can also write into those folders, opens only while the owner plays;
// a backup is always the owner's (bkup.go), and a restore waits for the owner.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

func init() {
	gameLaunchRegisterGate(profilesLaunchGate)
	gameLaunchRegisterPrepare(profilesPrepareLaunch)
}

// profilesSaveDirs are a profile's RetroArch save and state folders.
func profilesSaveDirs(dir string) (saves, states string) {
	return filepath.Join(dir, "saves"), filepath.Join(dir, "states")
}

// profilesRetroArchValues are the one-launch RetroArch settings for a profile
// whose folder is dir. Pure.
func profilesRetroArchValues(dir string) []raScopeValue {
	saves, states := profilesSaveDirs(dir)
	return []raScopeValue{
		{Key: "savefile_directory", Value: saves},
		{Key: "savestate_directory", Value: states},
		// Saves beside the game would be the owner's again.
		{Key: "savefiles_in_content_dir", Value: "false"},
		{Key: "savestates_in_content_dir", Value: "false"},
		{Key: "cheevos_enable", Value: "false"},
	}
}

// profilesPrepareLaunch gives a RetroArch launch the playing profile's
// folders (gameLaunchRegisterPrepare). The owner's launches are untouched.
func profilesPrepareLaunch(launcher string, _ libraryGame) {
	a := profilesCurrent()
	if a == nil || a.Dir == "" || !retroArchLauncher(launcher) {
		return
	}
	saves, states := profilesSaveDirs(a.Dir)
	for _, d := range []string{saves, states} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			logWarn("profiles", "cannot create %s: %v", d, err)
		}
	}
	values := profilesRetroArchValues(a.Dir)
	for _, path := range raScopeTargets(raLaunchCandidates(launcher, savestateResumeCandidates())) {
		if _, err := raScopeApply(path, values); err != nil {
			logWarn("profiles", "cannot give this launch the profile's save folders in %s: %v", path, err)
		}
	}
}

// profilesScopeKey reports whether key is one only a profile's launch writes
// (profilesRetroArchValues).
func profilesScopeKey(key string) bool {
	for _, v := range profilesRetroArchValues("") {
		if strings.EqualFold(v.Key, key) {
			return true
		}
	}
	return false
}

// profilesRecoverScope puts the owner's RetroArch folders back at start,
// before the first frame, when the last run ended (a crash, a power cut)
// while someone else's game was running. racfgscope.go's own recovery runs
// only after the first frame, on a goroutine: a game started before it — a
// power-on resume, a quick press of Continue — would play as the owner with
// that person's save folders, or with a Guest folder this start wipes, and
// the owner's in-game saves would land there. Main thread, profilesBoot.
func profilesRecoverScope() {
	path := raScopeJournalPath()
	if path == "" || !fileExists(path) {
		return
	}
	if raScopeChildRunning() || raScopeRetroArchRunning() {
		return // still playing: its exit, or the next start, restores
	}
	raScope.mu.Lock()
	defer raScope.mu.Unlock()
	ours := false
	for _, e := range raScopeLoadLocked().Edits {
		if profilesScopeKey(e.Key) {
			ours = true
			break
		}
	}
	if !ours {
		return
	}
	if n, err := raScopeRestoreLocked(); err != nil {
		logWarn("profiles", "could not put back the RetroArch folders an unfinished game left: %v", err)
	} else if n > 0 {
		logInfo("profiles", "%d RetroArch setting(s) an unfinished game as another profile left were put back", n)
	}
}

// profilesOthersPlayed is set once someone other than the owner has played in
// this run (profilesSwitchWith). Main thread.
var profilesOthersPlayed bool

// profilesShared remembers which standalone emulator each profile was told
// about this session. Main thread (gates run there).
var profilesShared = map[string]bool{}

// profilesEmulatorLabel names a launcher the way the card lays them out: the
// system folder of Emus/<SYSTEM>/launch.sh, else the program's own name.
func profilesEmulatorLabel(launcher string) string {
	base := filepath.Base(launcher)
	if strings.EqualFold(filepath.Ext(base), ".sh") || strings.EqualFold(base, "launch.sh") {
		if dir := filepath.Base(filepath.Dir(launcher)); dir != "" && dir != "." && dir != string(filepath.Separator) {
			return dir
		}
	}
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// profilesLaunchGate stops the first start of a standalone emulator for each
// profile with a notice that its saves are shared with the owner; starting
// the game again plays (gameLaunchRegisterGate).
func profilesLaunchGate(game libraryGame, launcher string) (bool, error) {
	// "Who is playing?" is up (the start-up question, which a power-on
	// resume would otherwise play under): the person is picked first.
	if profilesPicker.sheet.open {
		ShowToast("Choose who is playing first", ToastKindInfo)
		return true, nil
	}
	a := profilesCurrent()
	if a == nil {
		// The owner's launch does not set the profile folders, so ones a
		// person's game left in retroarch.cfg (its restore failed, or has not
		// run yet) would be the owner's for this game: they go back first.
		if profilesOthersPlayed {
			profilesRecoverScope()
		}
		return false, nil
	}
	if strings.TrimSpace(launcher) == "" || retroArchLauncher(launcher) {
		return false, nil
	}
	emu := profilesEmulatorLabel(launcher)
	key := a.ID + "|" + strings.ToLower(emu)
	if profilesShared[key] {
		return false, nil
	}
	profilesShared[key] = true
	profilesNoteShared(emu)
	owner := profilesOwnerLabel(profilesSnapshot())
	msg := emu + " keeps one save folder for everyone: saves made as " + a.Name + " are shared with " + owner + ". Start the game again to play."
	if a.Guest {
		msg = emu + " keeps one save folder for everyone: a Guest's saves here are " + owner + "'s saves. Start the game again to play anyway."
	}
	ShowToast(msg, ToastKindWarning)
	return true, nil
}

// profilesSharedSeen is every standalone emulator met this session, for the
// profile page.
var profilesSharedSeen struct {
	mu    sync.Mutex
	names map[string]bool
}

func profilesNoteShared(emu string) {
	profilesSharedSeen.mu.Lock()
	if profilesSharedSeen.names == nil {
		profilesSharedSeen.names = map[string]bool{}
	}
	profilesSharedSeen.names[emu] = true
	profilesSharedSeen.mu.Unlock()
}

// profilesSharedScan is the card's standalone emulators (Emus/<SYSTEM>/
// launch.sh scripts that do not start RetroArch), found once off the render
// thread.
var profilesSharedScan struct {
	mu      sync.Mutex
	started bool
	names   []string
}

// profilesSharedEmulators lists the emulators whose saves every profile
// shares with the owner: the card's (scanned once, in the background — the
// first call may return before the scan is done) and any met this session.
func profilesSharedEmulators() []string {
	profilesSharedScan.mu.Lock()
	if !profilesSharedScan.started {
		profilesSharedScan.started = true
		go profilesScanShared()
	}
	names := map[string]bool{}
	for _, n := range profilesSharedScan.names {
		names[n] = true
	}
	profilesSharedScan.mu.Unlock()
	profilesSharedSeen.mu.Lock()
	for n := range profilesSharedSeen.names {
		names[n] = true
	}
	profilesSharedSeen.mu.Unlock()
	out := make([]string, 0, len(names))
	for n := range names {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func profilesScanShared() {
	root := strings.TrimSpace(trimuiSDRootFn())
	if root == "" || IsWindows() {
		return
	}
	entries, err := os.ReadDir(filepath.Join(root, "Emus"))
	if err != nil {
		return
	}
	var found []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		script := filepath.Join(root, "Emus", e.Name(), "launch.sh")
		if _, err := os.Stat(script); err != nil {
			continue
		}
		if !retroArchLauncher(script) {
			found = append(found, e.Name())
		}
	}
	profilesSharedScan.mu.Lock()
	profilesSharedScan.names = found
	profilesSharedScan.mu.Unlock()
}
