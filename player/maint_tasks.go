package main

// maint_tasks.go — Settings → About → Tasks: the maintenance jobs in one list.
//
// JukaHub had most of the tidying already, spread over three sections and a
// plugin; what it lacked was a way back for an emulator whose settings were changed into a corner
// (RetroArch's video driver, a core option that makes every game black, a
// PPSSPP backend this GPU lacks). This page lists both kinds:
//
//   - the resets, new here. Nothing is deleted: every file moves to JukaHub's
//     reset trash (gamereset.go: <data>/reset-trash/<stamp>-<what>/, with a
//     restore.txt naming where each file came from; the newest few folders
//     stay), and retroarch.cfg itself is copied to
//     retroarch.cfg.jukahub-reset-<n>.bak beside it before it is replaced.
//     RetroArch has no factory copy of its config on a TrimUI card, and a
//     missing retroarch.cfg would lose the firmware's controls and paths, so
//     "reset RetroArch" means the one untouched copy JukaHub has: the
//     retroarch.cfg.jukahub-backup savestate_resume.go kept the first time it
//     changed the file. Where there is none, the page says so and offers what
//     does not need one: core options (each core then starts from its own
//     defaults) and the per-core / per-game override files (RetroArch then
//     uses retroarch.cfg alone). Remaps stay: they are the user's buttons,
//     and Reset game in the library already covers one game's. Every reset
//     waits until no game runs: RetroArch rewrites its config when it quits.
//   - the existing tools, linked rather than copied: Clear cache (the
//     Privacy row's own path), Rebuild library index (libindex.go), Library
//     doctor's litter removal and check (plugnative_tools.go — the Mac "._"
//     files are among its litter) when that plugin is on, and Report a bug
//     (bugrep.go).
//
// The list is the Settings list sheet (setsheet.go); a reset opens a
// confirmation with Keep focused.

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
)

// maintTasksTitle is the sheet's title, and how a late answer finds it.
const maintTasksTitle = "Tasks"

// maintTasksBusy is up while the list's availability is being read.
var maintTasksBusy atomic.Bool

// maintTasksRow is Settings → About → Tasks.
func maintTasksRow() setRow {
	return setRow{
		ID: "maintenanceTasks", Label: "Tasks", Kind: setKindAction,
		Detail: setStatic("Reset RetroArch or PPSSPP settings, tidy the card, report a bug"),
		Open:   maintTasksOpen,
	}
}

// maintTaskState is what the list depends on, read off the render thread.
type maintTaskState struct {
	raCfg        string // the retroarch.cfg RetroArch reads, "" when none
	raBefore     bool   // its retroarch.cfg.jukahub-backup exists
	raOptions    int    // core option files there are to reset
	raOverrides  int    // override files there are to clear
	ppssppIni    string // the ppsspp.ini PSP games use, "" when none
	doctorLoaded bool   // the Library doctor plugin is on
}

// maintTasksOpen opens the list at once and fills it when the card has been
// looked at.
func maintTasksOpen(c *Config) {
	setSheetOpen(&setHub.sheet, maintTasksTitle, "Maintenance · resets move files aside, nothing is deleted",
		[]setOption{{Value: "reading", Label: "Looking at the card…"}}, "", nil)
	if !maintTasksBusy.CompareAndSwap(false, true) {
		return
	}
	_, doctor := GetPlugins()["librarydoctor"]
	go func() {
		defer maintTasksBusy.Store(false)
		st := maintReadTaskState()
		st.doctorLoaded = doctor
		opts := maintTaskOptions(st)
		tbxPost(func() {
			if setHub.sheet.open && setHub.sheet.title == maintTasksTitle {
				setHub.sheet.options = opts
				setHub.sheet.cursor = 0
				setHub.sheet.scroll = 0
				setHub.sheet.choose = func(value string) { maintTaskRun(c, st, value) }
			}
		})
	}()
}

// maintReadTaskState looks at the emulators' files. Off the render thread.
func maintReadTaskState() maintTaskState {
	var st maintTaskState
	if text, cfgPath, ok := retroArchConfig(); ok {
		st.raCfg = cfgPath
		st.raBefore = regularFile(cfgPath + savestateBackupSuffix)
		st.raOptions = len(maintRAOptionFiles(text, cfgPath))
		st.raOverrides = len(maintRAOverrideFiles(text, cfgPath))
	}
	st.ppssppIni = findPPSSPPConfig(ppssppSearchRoots())
	return st
}

// maintTaskOptions are the list's rows for st.
func maintTaskOptions(st maintTaskState) []setOption {
	var opts []setOption
	switch {
	case st.raCfg == "":
		opts = append(opts, setOption{Value: "ra-none", Label: "Reset RetroArch settings",
			Note: "No retroarch.cfg found on this device"})
	case st.raBefore:
		opts = append(opts, setOption{Value: "ra-before", Label: "Reset RetroArch settings",
			Note: "Back to retroarch.cfg as it was before JukaHub first changed it; the current one is kept"})
	default:
		opts = append(opts, setOption{Value: "ra-none", Label: "Reset RetroArch settings",
			Note: "No untouched copy of retroarch.cfg was kept; the two rows below reset what can be"})
	}
	if st.raCfg != "" {
		opts = append(opts,
			setOption{Value: "ra-options", Label: "Reset core options",
				Note: maintFilesNote(st.raOptions, "Every core starts from its own defaults")},
			setOption{Value: "ra-overrides", Label: "Clear per-core and per-game overrides",
				Note: maintFilesNote(st.raOverrides, "RetroArch then uses retroarch.cfg alone; button remaps stay")},
		)
	}
	if st.ppssppIni != "" {
		opts = append(opts, setOption{Value: "ppsspp", Label: "Reset PPSSPP settings",
			Note: "ppsspp.ini and per-game settings; saves, states and button mapping stay"})
	}
	opts = append(opts,
		setOption{Value: "cache", Label: "Clear cache", Note: "Thumbnails and cached pages; files and saved pages stay"},
		setOption{Value: "libindex", Label: "Rebuild library index", Note: "Read the card's games again from scratch"},
	)
	if st.doctorLoaded {
		opts = append(opts,
			setOption{Value: "doctor-junk", Label: "Remove Mac and Windows litter", Note: "Library doctor: ._ files, .DS_Store, Thumbs.db, desktop.ini, __MACOSX in Roms"},
			setOption{Value: "doctor-check", Label: "Check my games", Note: "Library doctor: copies, cut-off zips and empty files"},
		)
	}
	opts = append(opts, setOption{Value: "bug", Label: "Report a bug", Note: "Tell us what went wrong, with a diagnostics bundle"})
	return opts
}

// maintFilesNote is a reset row's second line: what it does, or that there is
// nothing to do.
func maintFilesNote(n int, what string) string {
	if n == 0 {
		return "Nothing to reset: no such files"
	}
	return what + " · " + pluralizeCount(n, "file", "files")
}

// maintTaskRun is a row's press (the sheet has already closed).
func maintTaskRun(c *Config, st maintTaskState, value string) {
	switch value {
	case "ra-none":
		if st.raCfg == "" {
			setToast("No retroarch.cfg found on this device")
			return
		}
		setToast("No untouched copy of retroarch.cfg was kept; reset core options or overrides instead")
	case "ra-before":
		maintTaskConfirm(c, "Reset RetroArch settings?",
			"retroarch.cfg goes back to the copy JukaHub kept the first time it changed it. Everything changed in RetroArch's menu since then is undone.\n\n"+
				"The current file is kept beside it as retroarch.cfg.jukahub-reset-<number>.bak.",
			"Reset", func() { maintRunReset("retroarch", func() (string, error) { return maintRARestoreBefore(st.raCfg) }) })
	case "ra-options":
		if st.raOptions == 0 {
			setToast("Every core already uses its own default options")
			return
		}
		maintTaskConfirm(c, "Reset core options?",
			"RetroArch's core options file and every per-core and per-game options file ("+pluralizeCount(st.raOptions, "file", "files")+") move to JukaHub's reset trash. "+
				"Each core then starts from its own defaults.",
			"Reset", func() { maintRunMoveReset("-core-options", maintRAOptionFiles) })
	case "ra-overrides":
		if st.raOverrides == 0 {
			setToast("There are no RetroArch overrides to clear")
			return
		}
		maintTaskConfirm(c, "Clear RetroArch overrides?",
			"Every per-core, per-folder and per-game override ("+pluralizeCount(st.raOverrides, "file", "files")+") moves to JukaHub's reset trash. "+
				"RetroArch then uses retroarch.cfg alone. Button remaps stay.",
			"Clear", func() { maintRunMoveReset("-overrides", maintRAOverrideFiles) })
	case "ppsspp":
		maintTaskConfirm(c, "Reset PPSSPP settings?",
			"ppsspp.ini and the per-game settings beside it move to JukaHub's reset trash; PPSSPP writes fresh defaults at its next start. "+
				"Saves, save states and the button mapping (controls.ini) stay.",
			"Reset", func() { maintRunReset("ppsspp", func() (string, error) { return maintPPSSPPReset(st.ppssppIni) }) })
	case "cache":
		// The Privacy row's path (setmodel.go "clearCache").
		browserClearPageCache()
		handleTrigger(nil, c, Element{Trigger: "cache_clear"})
	case "libindex":
		setToast("Rebuilding the library index…")
		libIndexRebuild()
	case "doctor-junk":
		// Library doctor deletes litter outright (it is litter), so the
		// confirmation is the shared one that says it cannot be undone.
		setConfirm(c, "Remove litter files?",
			"Library doctor deletes the ._ copies a Mac leaves beside every file, .DS_Store, Thumbs.db, desktop.ini and __MACOSX folders in your Roms folders. Games are never touched.",
			"trash", "Remove", func(c *Config) { plugRunLoaded(c, "librarydoctor", "junk") })
	case "doctor-check":
		plugRunLoaded(c, "librarydoctor", "check")
	case "bug":
		bugrepOpenScreen(c)
	}
}

// maintTaskConfirm asks before a reset, Keep focused. The subtitle says what
// is true here: the files are moved, not gone.
func maintTaskConfirm(c *Config, title, body, action string, run func()) {
	tbxOpenSheet(c, tbxSheetSpec{
		title:    title,
		subtitle: "Files are moved to JukaHub's reset trash, not deleted",
		icon:     "refresh",
		body:     setConfirmBody(body),
		buttons: []tbxSheetButton{
			{label: "Keep"},
			{label: action, primary: true, run: run},
		},
	})
}

// maintGameRunning reports a game on the screen or a RetroArch still up:
// RetroArch writes its config when it quits, over whatever a reset put there.
func maintGameRunning() bool {
	return gameScreenSuspended() || retroArchRunning()
}

// maintRunReset runs one reset off the render thread, one at a time with the
// library's resets and cleanups (gameResetBusy), and toasts the answer run
// gives. The game check runs on the goroutine too: retroArchRunning reads
// every process's command line, which waits on a process stalled on the card.
func maintRunReset(what string, run func() (string, error)) {
	if !gameResetBusy.CompareAndSwap(false, true) {
		ShowToast("A reset or cleanup is still moving files", ToastKindInfo)
		return
	}
	go func() {
		defer gameResetBusy.Store(false)
		if maintGameRunning() {
			ShowToast("Quit the game first: the emulator saves its settings when it closes", ToastKindInfo)
			return
		}
		msg, err := run()
		logInfo("tasks", "reset %s: %s err=%v", what, msg, err)
		if err != nil {
			ShowToast("Reset failed: "+firstLine(err.Error()), ToastKindWarning)
			return
		}
		ShowToast(msg, ToastKindInfo)
	}()
}

// maintRunMoveReset moves the files plan lists for the current retroarch.cfg
// into a fresh reset-trash folder named <stamp><suffix>.
func maintRunMoveReset(suffix string, plan func(cfg, cfgPath string) []gameResetFile) {
	maintRunReset("retroarch"+suffix, func() (string, error) {
		text, cfgPath, ok := retroArchConfig()
		if !ok {
			return "", errors.New("no retroarch.cfg found")
		}
		return maintMoveToTrash(suffix, plan(text, cfgPath))
	})
}

// maintMoveToTrash moves files into a new reset-trash folder, each under its
// Kind (the folder it came from), prunes old folders and says what moved.
func maintMoveToTrash(suffix string, files []gameResetFile) (string, error) {
	if len(files) == 0 {
		return "Nothing to reset", nil
	}
	dir, err := resetTrashNewDir(suffix)
	if err != nil {
		return "", err
	}
	moved, failed := resetTrashMoveAll(dir, files, func(f gameResetFile) string { return f.Kind })
	pruneResetTrash(dir)
	if failed > 0 {
		return "", errors.New(pluralizeCount(moved, "file", "files") + " moved, " + pluralizeCount(failed, "file", "files") + " could not be moved")
	}
	return "Done · " + pluralizeCount(moved, "file", "files") + " moved to the reset trash", nil
}

// maintRAOptionFiles are RetroArch's core option files: the global one
// (core_options_path, else retroarch-core-options.cfg beside the config) and
// every .opt in the menu config folder's core folders.
func maintRAOptionFiles(cfg, cfgPath string) []gameResetFile {
	var out []gameResetFile
	global := ""
	if v, ok := raConfigGet(cfg, "core_options_path"); ok {
		global = videoFilterExpandDir(v, cfgPath)
	}
	if global == "" || !regularFile(global) {
		global = filepath.Join(filepath.Dir(cfgPath), "retroarch-core-options.cfg")
	}
	if regularFile(global) {
		out = append(out, gameResetFile{Path: global, Kind: "retroarch"})
	}
	return append(out, maintRAConfigFiles(raTuneMenuConfigDir(cfg, cfgPath), ".opt")...)
}

// maintRAOverrideFiles are the override configs: every .cfg in the menu
// config folder's core folders (the core's, a content folder's, a game's).
func maintRAOverrideFiles(cfg, cfgPath string) []gameResetFile {
	return maintRAConfigFiles(raTuneMenuConfigDir(cfg, cfgPath), ".cfg")
}

// maintRANotCoreFolders are folders that are never a core's: remaps/, and
// RetroArch's own folders, which a menu config folder set to RetroArch's root
// (rgui_config_directory = ":") would list beside the core folders — their
// .cfg files are controller profiles and overlays, not overrides.
var maintRANotCoreFolders = map[string]bool{
	"remaps": true, "autoconfig": true, "overlay": true, "overlays": true, "shaders": true,
	"filters": true, "cores": true, "info": true, "system": true, "playlists": true,
	"assets": true, "database": true, "cheats": true, "thumbnails": true, "saves": true,
	"states": true, "logs": true, "records": true, "screenshots": true, "downloads": true,
	"config": true,
}

// maintRAConfigFiles lists the files with ext in each core folder of confDir
// (config/<Core>/), never in remaps/, RetroArch's own folders or a hidden
// folder, and never below a core folder.
func maintRAConfigFiles(confDir, ext string) []gameResetFile {
	entries, err := os.ReadDir(confDir)
	if err != nil {
		return nil
	}
	var out []gameResetFile
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, ".") || maintRANotCoreFolders[strings.ToLower(name)] {
			continue
		}
		dir := filepath.Join(confDir, name)
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.EqualFold(filepath.Ext(f.Name()), ext) {
				continue
			}
			out = append(out, gameResetFile{Path: filepath.Join(dir, f.Name()), Kind: filepath.Join("config", name)})
		}
	}
	return out
}

// maintResetBackupPath is the first free retroarch.cfg.jukahub-reset-<n>.bak
// beside path.
func maintResetBackupPath(path string) (string, error) {
	for n := 1; n < 1000; n++ {
		p := path + ".jukahub-reset-" + strconv.Itoa(n) + ".bak"
		if _, err := os.Lstat(p); os.IsNotExist(err) {
			return p, nil
		}
	}
	return "", errors.New("too many reset backups beside " + filepath.Base(path))
}

// maintRARestoreBefore puts the pre-JukaHub retroarch.cfg back, after
// copying the current file to a numbered .bak. Under raScope's lock, so no
// launch edit or restore lands in between.
func maintRARestoreBefore(cfgPath string) (string, error) {
	if cfgPath == "" {
		return "", errors.New("no retroarch.cfg found")
	}
	kept, err := os.ReadFile(cfgPath + savestateBackupSuffix)
	if err != nil {
		return "", err
	}
	if len(strings.TrimSpace(string(kept))) == 0 {
		return "", errors.New("the kept copy of retroarch.cfg is empty")
	}
	raScope.mu.Lock()
	defer raScope.mu.Unlock()
	perm := os.FileMode(0o644)
	if info, err := os.Stat(cfgPath); err == nil {
		perm = info.Mode().Perm()
	}
	saved := ""
	if current, err := os.ReadFile(cfgPath); err == nil {
		bak, err := maintResetBackupPath(cfgPath)
		if err != nil {
			return "", err
		}
		if err := AtomicWrite(bak, current, perm); err != nil {
			return "", err
		}
		saved = " · the old one is " + filepath.Base(bak)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	// Through a temporary file and one rename, not AtomicWrite: that keeps the
	// replaced content as retroarch.cfg.bak, over a .bak a firmware or the
	// player may keep there — and the numbered copy above already holds it.
	tmp := cfgPath + ".jukahub-reset.tmp"
	if err := writeFileSynced(tmp, kept, perm); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, cfgPath); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	syncDirectory(filepath.Dir(cfgPath))
	return "RetroArch settings reset" + saved, nil
}

// maintPPSSPPReset moves ppsspp.ini and the per-game <ID>_ppsspp.ini files
// beside it to the reset trash. controls.ini (the buttons) and the memstick's
// saves and states stay.
func maintPPSSPPReset(ini string) (string, error) {
	if ini == "" || !regularFile(ini) {
		return "", errors.New("no ppsspp.ini found")
	}
	dir := filepath.Dir(ini)
	files := []gameResetFile{{Path: ini, Kind: "ppsspp"}}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), "_ppsspp.ini") {
				files = append(files, gameResetFile{Path: filepath.Join(dir, e.Name()), Kind: "ppsspp"})
			}
		}
	}
	return maintMoveToTrash("-ppsspp", files)
}
