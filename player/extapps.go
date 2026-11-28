package main

// extapps.go — TrimUI apps JukaHub knows by what they do, not by name: the
// installed KOReader that opens books and comics (koreader.go), the GamePad to
// PC and Bluetooth pad apps Controller Mode offers next to its own links
// (ctlmode_ui.go), CrossMix's Screen Recorder whose clips the Recordings page
// lists (screenrec.go), and TrimUI Drop.
//
// The Apps screen already lists every folder on the card's Apps folder with a
// config.json (apps.go scanAppSubdir); this file recognises a few of them by
// their folder name and label, folded to lower-case letters and digits, and
// starts one with arguments (a book's path) through the same hand-off as
// the Apps screen: the screen is parked while the app runs and JukaHub
// comes back when it exits.
//
// The scan runs on a goroutine and is cached: extAppFind answers from memory
// on the main thread, every frame if it must, and the first call (or a call
// after extAppRescanAge) only starts a rescan.

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The kinds of app this file recognises.
const (
	extAppKOReader       = "koreader"
	extAppGamepadPC      = "gamepad-pc"
	extAppGamepadBT      = "gamepad-bt"
	extAppScreenRecorder = "screenrecorder"
	extAppTrimuiDrop     = "trimui-drop"
)

// extAppRescanAge is how old the cached scan may be before a call starts a
// new one (an app copied onto the card shows up within it).
const extAppRescanAge = 30 * time.Second

// extApp is one recognised app.
type extApp struct {
	Kind string
	// Name is the app's label (config.json), else its folder's name.
	Name string
	// Dir is the app's folder; Launch its launch script inside it.
	Dir    string
	Launch string
	Icon   string
}

var extApps struct {
	mu       sync.Mutex
	byKind   map[string]extApp
	scanned  time.Time
	scanning atomic.Bool
}

// extAppFolded keeps the lower-case letters and digits of s.
func extAppFolded(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// extAppKindOf names what an app folder is ("" when it is none of ours),
// from its folder's name and its label.
func extAppKindOf(dirName, label string) string {
	f := extAppFolded(dirName) + " " + extAppFolded(label)
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(f, w) {
				return true
			}
		}
		return false
	}
	switch {
	case has("koreader"):
		return extAppKOReader
	case has("screenrecorder", "screenrecord", "screenrec"):
		return extAppScreenRecorder
	case has("trimuidrop", "sendfile"):
		return extAppTrimuiDrop
	case has("gamepad", "mando", "xinput", "xboxpad", "joypad") && has("bt", "bluetooth"):
		return extAppGamepadBT
	case has("gamepadtopc", "gamepadapc", "padtopc", "padapc", "pcgamepad"),
		has("gamepad", "mando", "xinput", "xboxpad", "joypad") && has("pc"):
		return extAppGamepadPC
	}
	return ""
}

// extAppScanDirs is where the apps live: the card's Apps folder on a TrimUI
// and the portable apps folder next to the program.
func extAppScanDirs() []string {
	dirs := systemAppDirs()
	if runtime.GOOS == "windows" {
		// The Start Menu holds shortcuts, not TrimUI app folders.
		dirs = nil
	}
	return append(dirs, appsFolder())
}

// extAppScan reads the app folders (a goroutine; it touches the card).
func extAppScan(dirs []string) map[string]extApp {
	found := map[string]extApp{}
	for _, root := range dirs {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, de := range entries {
			if !de.IsDir() || strings.HasPrefix(de.Name(), ".") {
				continue
			}
			dir := filepath.Join(root, de.Name())
			entry, ok := scanAppSubdir(dir)
			if !ok {
				continue
			}
			kind := extAppKindOf(de.Name(), entry.Name)
			if kind == "" && extAppKOReaderTree(dir) != "" {
				// A KOReader under another label ("Ebook Reader") is known
				// by its own entry, reader.lua.
				kind = extAppKOReader
			}
			if kind == "" {
				continue
			}
			if _, dup := found[kind]; dup {
				continue
			}
			found[kind] = extApp{Kind: kind, Name: entry.Name, Dir: dir, Launch: entry.Path, Icon: entry.Icon}
		}
	}
	return found
}

// extAppKOReaderTree is the folder holding KOReader's reader.lua inside an app
// folder: the folder itself or its koreader subfolder ("" when neither).
func extAppKOReaderTree(dir string) string {
	for _, d := range []string{dir, filepath.Join(dir, "koreader"), filepath.Join(dir, "KOReader")} {
		if st, err := os.Stat(filepath.Join(d, "reader.lua")); err == nil && st.Mode().IsRegular() {
			return d
		}
	}
	return ""
}

// extAppRefresh starts a rescan unless one is running.
func extAppRefresh() {
	if !extApps.scanning.CompareAndSwap(false, true) {
		return
	}
	dirs := extAppScanDirs()
	go func() {
		defer extApps.scanning.Store(false)
		found := extAppScan(dirs)
		extApps.mu.Lock()
		extApps.byKind = found
		extApps.scanned = time.Now()
		extApps.mu.Unlock()
	}()
}

// extAppFind is the installed app of a kind. It answers from the last scan
// and starts a new one when that is old, so it never touches the disk.
func extAppFind(kind string) (extApp, bool) {
	extApps.mu.Lock()
	app, ok := extApps.byKind[kind]
	stale := extApps.scanned.IsZero() || time.Since(extApps.scanned) > extAppRescanAge
	extApps.mu.Unlock()
	if stale {
		extAppRefresh()
	}
	return app, ok
}

// extAppFindNow is extAppFind for a press that must not miss an app only
// because no scan has finished yet (the first book opened after start-up):
// with no result at all it scans on this thread, once.
func extAppFindNow(kind string) (extApp, bool) {
	extApps.mu.Lock()
	never := extApps.scanned.IsZero()
	extApps.mu.Unlock()
	if never {
		found := extAppScan(extAppScanDirs())
		extApps.mu.Lock()
		if extApps.scanned.IsZero() {
			extApps.byKind = found
			extApps.scanned = time.Now()
		}
		extApps.mu.Unlock()
	}
	return extAppFind(kind)
}

// extAppLaunch starts an app with arguments (a file to open), detached and
// with the screen handed over like the Apps screen's launches: JukaHub waits
// parked and comes back when the app exits. It reports whether the app
// started. The arguments are passed as they are, never through a shell.
func extAppLaunch(app extApp, args ...string) bool {
	return extAppLaunchWith(app, "", nil, args...)
}

// extAppLaunchWith is extAppLaunch with another program of the app's own
// folder in place of its launch script (exe; "" is the script) and extra
// "NAME=value" environment entries over JukaHub's. KOReader's own entry runs
// this way when its launch script ignores a book's path (koreader.go).
func extAppLaunchWith(app extApp, exe string, env []string, args ...string) bool {
	if runtime.GOOS == "windows" {
		return false
	}
	target := app.Launch
	if exe != "" {
		target = exe
	}
	clean, err := cleanUserPath(target)
	if err != nil || !isLaunchableAppPath(clean) || !appLaunchPathRE.MatchString(clean) ||
		(exe != "" && (app.Dir == "" || !isPathWithin(clean, app.Dir))) {
		logWarn("apps", "security: refusing to launch %q: outside the app folders", target)
		ShowToast("Refusing to launch an app outside the app folders", ToastKindError)
		return false
	}
	info, err := os.Stat(clean)
	if err != nil || !info.Mode().IsRegular() {
		ShowToast("Could not launch "+app.Name, ToastKindError)
		return false
	}
	for _, a := range append(append([]string(nil), args...), env...) {
		if strings.ContainsAny(a, "\x00\r\n") {
			// A caller (koreaderFilesOpen) takes false as "said why".
			logWarn("apps", "refusing to start %s: an argument holds a line break or NUL", app.Name)
			ShowToast("Cannot pass this file to "+app.Name+": its name has a line break", ToastKindError)
			return false
		}
	}
	var cmd *exec.Cmd
	if strings.EqualFold(filepath.Ext(clean), ".sh") {
		// sh reads the script file; the path and the arguments are operands.
		cmd = hiddenCommand("sh", append([]string{"--", clean}, args...)...)
	} else {
		cmd = hiddenCommand(clean, args...)
	}
	cmd.Dir = filepath.Dir(clean)
	if len(env) > 0 {
		// The environment every screen-taking child gets (the firmware's
		// paths, childenv.go), then these: exec keeps the last of a name.
		cmd.Env = append(screenChildEnv(os.Environ()), env...)
	}
	screenChildGroupConfigure(cmd)
	if err := screenChildStart(cmd); err != nil {
		logWarn("apps", "launch failed for %s: %v", clean, err)
		ShowToast("Could not launch "+app.Name, ToastKindError)
		return false
	}
	logInfo("apps", "launched %s (%s) with %d argument(s)", app.Name, app.Kind, len(args))
	trophyEvent(tkAppLaunch)
	noteGameChildStarted(cmd)
	go func() {
		_ = cmd.Wait()
		screenChildReaped(cmd)
	}()
	return true
}
