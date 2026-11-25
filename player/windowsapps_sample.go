package main

// windowsapps_sample.go — the sample program Windows Apps downloads, so a
// handheld whose list is empty has something to try Wine with (Juka,
// 2026-11-21: "downloading a simple application to run on the Smart Pro").
//
// The sample is Notepad++'s portable build: a real Win32 program that draws
// its own window with GDI, the kind docs/WINDOWS_APPS.md calls the target;
// about 8 MB; released on GitHub, inside the download allowlist, with a
// SHA-256 per file, pinned below. Both builds come down: the 64-bit one runs
// on Box64 directly, the 32-bit one also goes through Wine's WoW64 layer, as
// most old Windows games do, so their two rows tell which path works on a
// device.
//
// For each build that is not there yet, an install
//
//   - downloads the zip (wineDownload: the pinned SHA-256, the allowlist, a
//     cut-off download resumed) to data/windows-runtime/downloads;
//   - unpacks it into a hidden folder of data/windows-apps (emuExtractZip:
//     through os.Root, entry and size caps, no links), which the scanner
//     skips;
//   - drops the updater (GUP.exe: the portable build does not use it, and the
//     list would show it as a program) and checks that the program is a
//     Windows program for the expected CPU (debug/pe);
//   - writes its marker and renames the folder into place, so the list never
//     shows a half-unpacked build, and a folder without the marker (someone's
//     own) is never replaced.
//
// The list is then scanned again and, if the panel that asked is still open,
// the 64-bit build starts the way A on its row would: the setup page first
// when the runtime still misses a piece.

import (
	"context"
	"debug/pe"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// windowsSampleName and windowsSampleVersion name the sample on screen;
	// the version is the pinned release's.
	windowsSampleName    = "Notepad++"
	windowsSampleVersion = "8.9.9.1"
	// windowsSampleAbout is the two zips' size (8.0 and 7.6 MB).
	windowsSampleAbout = "about 16 MB"
	// windowsSampleRoom is the free space an install asks for: each build
	// unpacks to about 20 MB, and its zip waits beside it until then.
	windowsSampleRoom = int64(96 << 20)
	// windowsSampleMarker is the file an installed build carries.
	windowsSampleMarker = ".jukahub-sample.json"
	// windowsSampleHidden starts the names of the folders an install unpacks
	// into and steps an older build aside to; the scanner skips them.
	windowsSampleHidden = ".jukahub-sample-"
	// windowsSampleTimeout bounds a whole install.
	windowsSampleTimeout = 30 * time.Minute
)

// windowsSampleBuild is one build of the sample program.
type windowsSampleBuild struct {
	// Dir is the build's folder under windowsAppsDir(), slash-separated.
	Dir string
	// Title is its row in the list.
	Title string
	// Exe is the program inside Dir.
	Exe string
	// Machine is the CPU the program must be built for.
	Machine uint16
	// Drop are folders of the zip the install leaves out.
	Drop []string
	// Spec is the zip: its name, URL and pinned SHA-256.
	Spec wineTarballSpec
}

// windowsSampleBuilds are Notepad++ 8.9.9.1's portable zips, 64-bit first
// (the one A starts). The SHA-256s are the ones the release lists under
// "SHA256 Digest" and in its npp.8.9.9.1.checksums.sha256; download mirrors
// that hash their own copy show the same 64-bit value.
var windowsSampleBuilds = []windowsSampleBuild{
	{
		Dir:     "Notepad++/64-bit",
		Title:   "Notepad++ (64-bit)",
		Exe:     "notepad++.exe",
		Machine: pe.IMAGE_FILE_MACHINE_AMD64,
		Drop:    []string{"updater"},
		Spec: wineTarballSpec{
			Version: windowsSampleVersion,
			Asset:   "npp.8.9.9.1.portable.x64.zip",
			URL:     "https://github.com/notepad-plus-plus/notepad-plus-plus/releases/download/v8.9.9.1/npp.8.9.9.1.portable.x64.zip",
			SHA256:  "cbcefa34677e10fe6dd19802f7d6b3b9860cd6de62afeaae7680e09c51b47779",
		},
	},
	{
		Dir:     "Notepad++/32-bit",
		Title:   "Notepad++ (32-bit)",
		Exe:     "notepad++.exe",
		Machine: pe.IMAGE_FILE_MACHINE_I386,
		Drop:    []string{"updater"},
		Spec: wineTarballSpec{
			Version: windowsSampleVersion,
			Asset:   "npp.8.9.9.1.portable.zip",
			URL:     "https://github.com/notepad-plus-plus/notepad-plus-plus/releases/download/v8.9.9.1/npp.8.9.9.1.portable.zip",
			SHA256:  "7b48fceec762e80c2bc781a59739403f7af8b6136ed0ffc369a2680c535ab78a",
		},
	},
}

// windowsSampleMarkerFile is what an installed build records about itself.
type windowsSampleMarkerFile struct {
	Asset       string `json:"asset"`
	Version     string `json:"version"`
	SHA256      string `json:"sha256"`
	InstalledAt string `json:"installed_at"`
}

// windowsSampleState is the running install, for the setup row and the empty
// list. Any goroutine.
var windowsSampleState struct {
	mu      sync.Mutex
	running bool
	status  string // what the running install is doing
	failed  string // why the last install failed, "" when it did not
}

// windowsSampleDir is where build lives.
func windowsSampleDir(b windowsSampleBuild) string {
	return filepath.Join(windowsAppsDir(), filepath.FromSlash(b.Dir))
}

// windowsSampleExe is build's program, as the scanner walks to it.
func windowsSampleExe(b windowsSampleBuild) string {
	return filepath.Join(windowsSampleDir(b), b.Exe)
}

// windowsSampleKey is the form sample paths are compared in.
func windowsSampleKey(p string) string { return strings.ToLower(filepath.Clean(p)) }

// windowsSampleTitles maps each build's program to its row title, for the
// scanner: both builds' programs are called notepad++.exe.
func windowsSampleTitles() map[string]string {
	out := make(map[string]string, len(windowsSampleBuilds)+1)
	for _, b := range windowsSampleBuilds {
		out[windowsSampleKey(windowsSampleExe(b))] = b.Title
	}
	// The built-in Wine test too (windowsapps_winetest.go).
	out[windowsSampleKey(wineTestExePath())] = wineTestTitle
	return out
}

// windowsSampleOldDir is where an earlier install of build steps aside while
// the new one is renamed into place.
func windowsSampleOldDir(dest string) string {
	return filepath.Join(filepath.Dir(dest), windowsSampleHidden+filepath.Base(dest)+".old")
}

// windowsSampleMarked reports a folder this installer put there.
func windowsSampleMarked(dir string) bool {
	return regularFile(filepath.Join(dir, windowsSampleMarker))
}

// windowsSampleInstalled reports build installed from its pinned zip, with
// its program in place.
func windowsSampleInstalled(b windowsSampleBuild) bool {
	dir := windowsSampleDir(b)
	data, err := os.ReadFile(filepath.Join(dir, windowsSampleMarker))
	if err != nil {
		return false
	}
	var marker windowsSampleMarkerFile
	if json.Unmarshal(stripUTF8BOM(data), &marker) != nil || !strings.EqualFold(marker.SHA256, b.Spec.SHA256) {
		return false
	}
	return regularFile(filepath.Join(dir, b.Exe))
}

// windowsSampleMissing lists the builds still to install, in catalog order.
func windowsSampleMissing() []windowsSampleBuild {
	var out []windowsSampleBuild
	for _, b := range windowsSampleBuilds {
		if !windowsSampleInstalled(b) {
			out = append(out, b)
		}
	}
	return out
}

// windowsSampleEntry is the 64-bit build's row in the current scan.
func windowsSampleEntry() (windowsAppEntry, bool) {
	if len(windowsSampleBuilds) == 0 {
		return windowsAppEntry{}, false
	}
	exe := windowsSampleExe(windowsSampleBuilds[0])
	keys := map[string]bool{windowsSampleKey(exe): true}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		keys[windowsSampleKey(real)] = true
	}
	for _, entry := range windowsAppEntries() {
		if keys[windowsSampleKey(entry.Path)] {
			return entry, true
		}
	}
	return windowsAppEntry{}, false
}

// windowsSampleBusy reports an install under way. Any goroutine.
func windowsSampleBusy() bool {
	windowsSampleState.mu.Lock()
	defer windowsSampleState.mu.Unlock()
	return windowsSampleState.running
}

// windowsSampleProgress is what the running install is doing, or why the last
// one failed (failed true); "" when there is nothing to say. Any goroutine.
func windowsSampleProgress() (line string, failed bool) {
	windowsSampleState.mu.Lock()
	defer windowsSampleState.mu.Unlock()
	if windowsSampleState.running {
		return windowsSampleState.status, false
	}
	if windowsSampleState.failed != "" {
		return windowsSampleState.failed, true
	}
	return "", false
}

func windowsSampleSetStatus(line string) {
	windowsSampleState.mu.Lock()
	windowsSampleState.status = line
	windowsSampleState.mu.Unlock()
}

// startWindowsSampleInstall installs the missing builds on a goroutine; done
// (may be nil) gets the result on the main thread. It returns an error when
// an install already runs or nothing is missing.
func startWindowsSampleInstall(done func(error)) error {
	if IsWindows() {
		return errors.New("Windows runs .exe files directly; copy programs into " + windowsAppsDir())
	}
	missing := windowsSampleMissing()
	if len(missing) == 0 {
		return errors.New(windowsSampleName + " is already in Windows Apps")
	}
	windowsSampleState.mu.Lock()
	if windowsSampleState.running {
		windowsSampleState.mu.Unlock()
		return errors.New("already downloading " + windowsSampleName)
	}
	windowsSampleState.running = true
	windowsSampleState.status = "Downloading " + windowsSampleName + "…"
	windowsSampleState.failed = ""
	windowsSampleState.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), windowsSampleTimeout)
		err := installWindowsSample(ctx, missing, windowsSampleSetStatus)
		cancel()
		windowsSampleState.mu.Lock()
		windowsSampleState.running = false
		windowsSampleState.status = ""
		if err != nil {
			windowsSampleState.failed = relPackFriendly(err)
		}
		windowsSampleState.mu.Unlock()
		invalidateWindowsAppsCache()
		invalidateWindowsSetupSnapshot()
		if err != nil {
			logWarn("windowsapps", "sample program not installed: %v", err)
		} else {
			logInfo("windowsapps", "sample program %s %s installed in %s", windowsSampleName, windowsSampleVersion, windowsAppsDir())
		}
		if done != nil {
			tbxPost(func() { done(err) })
		}
	}()
	return nil
}

// installWindowsSample installs builds one after another; progress receives
// one-line updates.
func installWindowsSample(ctx context.Context, builds []windowsSampleBuild, progress func(string)) error {
	apps := windowsAppsDir()
	if err := os.MkdirAll(apps, 0o755); err != nil {
		if disk := relPackDiskErr(err); disk != nil {
			return disk
		}
		return err
	}
	if free, err := freeBytes(apps); err == nil && free < windowsSampleRoom {
		return fmt.Errorf("%w · %s needs about %s, the card has %s", errRelPackNoSpace, windowsSampleName,
			formatInstallBytes(windowsSampleRoom), formatInstallBytes(max64(free, 0)))
	}
	for _, b := range builds {
		if err := installWindowsSampleBuild(ctx, b, progress); err != nil {
			if disk := relPackDiskErr(err); disk != nil {
				return disk
			}
			return err
		}
	}
	return nil
}

// installWindowsSampleBuild downloads, checks and unpacks one build, and
// renames it into place.
func installWindowsSampleBuild(ctx context.Context, b windowsSampleBuild, progress func(string)) error {
	dest := windowsSampleDir(b)
	old := windowsSampleOldDir(dest)
	if _, err := os.Lstat(dest); errors.Is(err, fs.ErrNotExist) && windowsSampleMarked(old) {
		// An install cut off between its two renames: the earlier build
		// comes back first.
		_ = os.Rename(old, dest)
	}
	if info, err := os.Lstat(dest); err == nil && (!info.IsDir() || !windowsSampleMarked(dest)) {
		return fmt.Errorf("%s already holds other files; move them away, then try again", dest)
	}
	downloads := filepath.Join(windowsRuntimeDir(), "downloads")
	if err := os.MkdirAll(downloads, 0o755); err != nil {
		return err
	}
	archive := filepath.Join(downloads, b.Spec.Asset)
	err := wineDownload(ctx, relPackClientFn(), b.Spec, archive, func(got, _ int64) {
		progress("Downloading " + b.Title + " · " + formatInstallBytes(got))
	})
	if err != nil {
		return err
	}
	progress("Unpacking " + b.Title + "…")
	stage := filepath.Join(windowsAppsDir(), windowsSampleHidden+filepath.Base(dest))
	_ = os.RemoveAll(stage)
	defer os.RemoveAll(stage)
	if err := emuExtractZip(ctx, archive, stage, nil); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || relPackDiskErr(err) != nil {
			return err
		}
		// A zip that matched its pinned SHA-256 and does not unpack is not
		// worth keeping for a retry.
		_ = os.Remove(archive)
		return fmt.Errorf("%w: unpacking %s: %v", errRelPackUnverified, b.Spec.Asset, err)
	}
	root := emuArchiveContentRoot(stage)
	for _, name := range b.Drop {
		if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
			return err
		}
	}
	if problem := windowsPEProblem(filepath.Join(root, b.Exe), b.Machine); problem != "" {
		return fmt.Errorf("%w: %s in %s %s", errRelPackUnverified, b.Exe, b.Spec.Asset, problem)
	}
	marker, err := json.MarshalIndent(windowsSampleMarkerFile{
		Asset:       b.Spec.Asset,
		Version:     b.Spec.Version,
		SHA256:      b.Spec.SHA256,
		InstalledAt: time.Now().UTC().Format(time.RFC3339),
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := AtomicWrite(filepath.Join(root, windowsSampleMarker), append(marker, '\n'), 0o644); err != nil {
		return err
	}
	if err := windowsSampleSwap(root, dest); err != nil {
		return err
	}
	_ = os.Remove(archive)
	return nil
}

// windowsSampleSwap renames the unpacked build at src to dest. An earlier
// install of the sample there steps aside first and comes back when the
// rename fails; anything else at dest stops the install.
func windowsSampleSwap(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	old := windowsSampleOldDir(dest)
	_ = os.RemoveAll(old)
	had := false
	if _, err := os.Lstat(dest); err == nil {
		if !windowsSampleMarked(dest) {
			return fmt.Errorf("%s already holds other files; move them away, then try again", dest)
		}
		if err := os.Rename(dest, old); err != nil {
			return err
		}
		had = true
	}
	if err := os.Rename(src, dest); err != nil {
		if had {
			_ = os.Rename(old, dest)
		}
		return err
	}
	if had {
		_ = os.RemoveAll(old)
	}
	return nil
}

// windowsPEProblem says why path is not a Windows program built for machine,
// "" when it is one.
func windowsPEProblem(path string, machine uint16) string {
	if !regularFile(path) {
		return "is missing"
	}
	f, err := pe.Open(path)
	if err != nil {
		return "is not a Windows program"
	}
	defer f.Close()
	switch {
	case f.Machine != machine:
		return fmt.Sprintf("is built for CPU %#x, not %#x", f.Machine, machine)
	case f.Characteristics&pe.IMAGE_FILE_DLL != 0:
		return "is a library, not a program"
	}
	return ""
}

// ── Windows Apps panel ───────────────────────────────────────────────────

// windowsSampleSetupRow is the setup page's sample row as the files say; the
// install's progress is laid over it every frame (windowsAppsSetupRows).
func windowsSampleSetupRow() windowsSetupRow {
	row := windowsSetupRow{Title: "Sample program: " + windowsSampleName + " " + windowsSampleVersion, Status: "optional"}
	missing := windowsSampleMissing()
	switch {
	case len(missing) == 0:
		row.Status = "installed"
		row.Detail = hintSentence("In the list, 64- and 32-bit · %s starts the 64-bit one", "A", "Start")
	case len(missing) < len(windowsSampleBuilds):
		row.Detail = missing[0].Title + " is missing · " + hintSentence("%s downloads it", "A", "Download")
	default:
		row.Detail = "A Windows program to try: its 64- and 32-bit builds, " + windowsSampleAbout + " from GitHub, checked against their SHA-256"
	}
	return row
}

// windowsSampleSetupOverlay lays the install's progress, or its failure, over
// the sample row. Main thread.
func windowsSampleSetupOverlay(row *windowsSetupRow) {
	line, failed := windowsSampleProgress()
	switch {
	case line == "":
	case !failed:
		row.Status, row.Detail, row.Bad = "downloading", line, false
	case row.Status != "installed":
		row.Status, row.Bad = "failed", true
		row.Detail = line + " · " + hintSentence("%s tries again", "A", "Retry")
	}
}

// windowsSampleEmptyLine is the empty list's line about the sample: the
// running install (busy), its failure (failed), or what button does — A on an
// empty list, Y under the built-in Wine test's row. Main thread.
func windowsSampleEmptyLine(button string) (text string, busy, failed bool) {
	line, lastFailed := windowsSampleProgress()
	switch {
	case line != "" && !lastFailed:
		return line, true, false
	case lastFailed:
		return "Could not download " + windowsSampleName + ": " + line + " · " + hintSentence("%s tries again", button, "Retry"), false, true
	}
	return hintSentence("%s downloads "+windowsSampleName+" ("+windowsSampleAbout+") to try Windows apps with,", button, "Get Notepad++"), false, false
}

// windowsAppsPanelSample is A on the setup page's sample row, or on the empty
// list. An installed sample starts (its 64-bit build) the way A on its row
// would; a missing one downloads first and starts once it is in, if the panel
// is still open and setup was not left meanwhile (sampleWait). Main thread.
func windowsAppsPanelSample() {
	if IsWindows() {
		return
	}
	if windowsSampleBusy() {
		ShowToast("Already downloading "+windowsSampleName, ToastKindInfo)
		return
	}
	if len(windowsSampleMissing()) == 0 {
		if entry, ok := windowsSampleEntry(); ok {
			windowsAppsStartSample(entry)
			return
		}
		ShowToast(windowsSampleName+" is in "+windowsAppsDir()+" · "+hintSentence("%s rescans the folders", "Start", "Rescan"), ToastKindInfo)
		return
	}
	if !IsOnline() {
		PlayErrorSound()
		ShowToast("Downloading "+windowsSampleName+" needs internet — connect to Wi-Fi, then try again", ToastKindWarning)
		return
	}
	if err := startWindowsSampleInstall(windowsSampleFinished); err != nil {
		ShowToast(capitalizeFirst(err.Error()), ToastKindInfo)
		return
	}
	winAppsPanel.sampleWait = true
	PlayActivateSound()
	ShowToast("Downloading "+windowsSampleName+" ("+windowsSampleAbout+")…", ToastKindInfo)
}

// windowsSampleFinished is the install's end, on the main thread: a failure
// is said (and stays on the setup row and the empty list); a success starts
// the sample when the panel still waits for it, else says it is there.
func windowsSampleFinished(err error) {
	wait := winAppsPanel.open && winAppsPanel.sampleWait
	winAppsPanel.sampleWait = false
	if err != nil {
		PlayErrorSound()
		ShowToast("Could not download "+windowsSampleName+": "+relPackFriendly(err), ToastKindError)
		return
	}
	PlaySuccessSound()
	entry, ok := windowsSampleEntry()
	if !ok || !wait {
		ShowToast(windowsSampleName+" is ready in Windows Apps (64- and 32-bit)", ToastKindSuccess)
		return
	}
	windowsAppsStartSample(entry)
}

// windowsAppsStartSample puts the list on entry and presses A on it: the row
// shows the launch's progress, or the setup page says what the runtime still
// needs first.
func windowsAppsStartSample(entry windowsAppEntry) {
	winAppsPanel.help, winAppsPanel.pending, winAppsPanel.notice = false, nil, ""
	for i, e := range windowsAppEntries() {
		if e.Path == entry.Path {
			winAppsPanel.cursor = i
			break
		}
	}
	windowsAppsActivateEntry(entry)
}
