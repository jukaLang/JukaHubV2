package main

// windowsapps_winetest.go — the Wine test: a small Windows program JukaHub
// carries itself, so a handheld can show at once that Windows programs run
// there (Juka, 2026-11-25: "generate 1 windows test app so people can see that
// wine emulator is working").
//
// The program is player/winetest/winetest.c, built into winetest.exe.gz by
// player/winetest/mkwinetest.py — the host gcc in the Microsoft calling
// convention and a PE writer of its own, no Windows toolchain — and embedded
// here, compressed. It opens one full-screen window that says "Wine is
// working!" with the Wine version, the host, the Windows version Wine reports,
// the processor, the screen and the memory; a ball moves and a clock ticks so a
// frozen screen shows; every button, key and click it receives is named (the
// pad layout JukaHub lends Windows programs); and a chime plays through
// Windows' own sound. Y / Esc, its Close button or Select + Start closes it.
//
// Where it lives: data/windows-apps/Wine Test/WineTest.exe, beside a marker
// (.jukahub-winetest.json) naming the embedded build. Opening Windows Apps on a
// handheld puts it there the first time and replaces an older build. A folder
// of that name without the marker is someone's own and is never touched, and
// once it was put there, a user who deletes it is not overruled: the setup
// page's Wine test row puts it back. The list names it "Wine Test (built in)"
// and keeps it below the user's own programs. On Windows nothing is installed
// — Windows runs programs itself.
//
// When a session of it ends, the lines it printed into the app's log say how
// it went, and a toast says so on the handheld (wineTestVerdict).

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

//go:embed winetest/winetest.exe.gz
var wineTestGz []byte

const (
	// wineTestDirName and wineTestExeName are where the program goes inside
	// the Windows Apps folder; wineTestTitle is its row.
	wineTestDirName = "Wine Test"
	wineTestExeName = "WineTest.exe"
	wineTestTitle   = "Wine Test (built in)"
	// wineTestMarker names the build a folder holds.
	wineTestMarker = ".jukahub-winetest.json"
	// wineTestPlacedFlag, in the runtime folder, records that the test was put
	// in the list once: a user who removed it keeps it removed.
	wineTestPlacedFlag = "winetest-placed"
	// wineTestMaxBytes bounds the unpacked program.
	wineTestMaxBytes = 4 << 20
)

// wineTestMarkerFile is what the marker records.
type wineTestMarkerFile struct {
	Build       string `json:"build"`
	InstalledAt string `json:"installed_at"`
}

var wineTestBuild struct {
	once sync.Once
	id   string
}

// wineTestBuildID names the embedded build: the SHA-256 of its compressed
// bytes, which mkwinetest.py writes reproducibly.
func wineTestBuildID() string {
	wineTestBuild.once.Do(func() {
		sum := sha256.Sum256(wineTestGz)
		wineTestBuild.id = hex.EncodeToString(sum[:])
	})
	return wineTestBuild.id
}

func wineTestDir() string     { return filepath.Join(windowsAppsDir(), wineTestDirName) }
func wineTestExePath() string { return filepath.Join(wineTestDir(), wineTestExeName) }

// wineTestKeys are the test program's path as compared (windowsSampleKey):
// as named, and through the Windows Apps folder's real path, which is what
// the scanner records. Both are worked out once; the list asks every frame.
var wineTestKeys struct {
	mu   sync.Mutex
	raw  string
	real string
}

func wineTestKeysNow() (string, string) {
	wineTestKeys.mu.Lock()
	defer wineTestKeys.mu.Unlock()
	if wineTestKeys.raw == "" {
		wineTestKeys.raw = windowsSampleKey(wineTestExePath())
	}
	if wineTestKeys.real == "" {
		if dir, err := filepath.EvalSymlinks(windowsAppsDir()); err == nil {
			wineTestKeys.real = windowsSampleKey(filepath.Join(dir, wineTestDirName, wineTestExeName))
		}
	}
	return wineTestKeys.raw, wineTestKeys.real
}

// wineTestPath reports whether p is the Wine test's program. Any goroutine.
func wineTestPath(p string) bool {
	if p == "" {
		return false
	}
	raw, real := wineTestKeysNow()
	k := windowsSampleKey(p)
	return k == raw || (real != "" && k == real)
}

// wineTestMarked reports a folder JukaHub put the test in, and its build.
func wineTestMarked(dir string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(dir, wineTestMarker))
	if err != nil {
		return "", false
	}
	var m wineTestMarkerFile
	if json.Unmarshal(stripUTF8BOM(data), &m) != nil {
		return "", false
	}
	return m.Build, true
}

// wineTestInstalled reports the embedded build in place.
func wineTestInstalled() bool {
	build, ok := wineTestMarked(wineTestDir())
	return ok && build == wineTestBuildID() && regularFile(wineTestExePath())
}

// wineTestProgram is the embedded program, unpacked.
func wineTestProgram() ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(wineTestGz))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	data, err := io.ReadAll(io.LimitReader(zr, wineTestMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > wineTestMaxBytes || len(data) < 1024 || !bytes.HasPrefix(data, []byte("MZ")) {
		return nil, errors.New("the built-in Wine test is damaged")
	}
	return data, nil
}

// wineTestEnsure puts the test in the Windows Apps folder, or a newer build
// over an older one. A folder of that name that JukaHub did not make is left
// alone; so is a test the user removed, unless asked (byHand: the setup row).
// Nothing happens on Windows. Main thread or any goroutine; file work only.
func wineTestEnsure(byHand bool) error {
	if IsWindows() {
		if byHand {
			return errors.New("Windows runs programs itself; the Wine test is for handhelds")
		}
		return nil
	}
	dir := wineTestDir()
	flag := filepath.Join(windowsRuntimeDir(), wineTestPlacedFlag)
	build, marked := wineTestMarked(dir)
	_, statErr := os.Lstat(dir)
	exists := statErr == nil
	switch {
	case exists && !marked:
		if byHand {
			return fmt.Errorf("%s already holds other files; move them away, then try again", dir)
		}
		return nil
	case !exists && regularFile(flag) && !byHand:
		// Removed by the user after JukaHub put it there.
		return nil
	case marked && build == wineTestBuildID() && regularFile(wineTestExePath()):
		return nil
	}
	data, err := wineTestProgram()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		if disk := relPackDiskErr(err); disk != nil {
			return disk
		}
		return err
	}
	if err := AtomicWrite(wineTestExePath(), data, 0o755); err != nil {
		if disk := relPackDiskErr(err); disk != nil {
			return disk
		}
		return err
	}
	marker, err := json.MarshalIndent(wineTestMarkerFile{Build: wineTestBuildID(), InstalledAt: time.Now().UTC().Format(time.RFC3339)}, "", "  ")
	if err != nil {
		return err
	}
	if err := AtomicWrite(filepath.Join(dir, wineTestMarker), append(marker, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(flag), 0o755); err == nil {
		_ = AtomicWrite(flag, []byte("1\n"), 0o644)
	}
	logInfo("windowsapps", "built-in Wine test placed in %s", dir)
	return nil
}

// wineTestEntry is the test's row in the current scan.
func wineTestEntry() (windowsAppEntry, bool) {
	for _, entry := range windowsAppEntries() {
		if wineTestPath(entry.Path) {
			return entry, true
		}
	}
	return windowsAppEntry{}, false
}

// windowsAppsOnlyBuiltIn reports a list that holds the Wine test and nothing
// of the user's: the list still says how to add programs and offers the
// sample download (on Y, since A starts the test).
func windowsAppsOnlyBuiltIn(entries []windowsAppEntry) bool {
	if len(entries) == 0 {
		return false
	}
	for _, e := range entries {
		if !wineTestPath(e.Path) {
			return false
		}
	}
	return true
}

// windowsAppsPanelY is Y on the list: the focused app's display switch, or —
// while the list holds only the Wine test — the sample program's download.
// Main thread.
func windowsAppsPanelY() {
	if !IsWindows() && windowsAppsOnlyBuiltIn(windowsAppEntries()) {
		windowsAppsPanelSample()
		return
	}
	windowsAppsPanelToggleDisplay()
}

// windowsAppsDrawStarterLines draws, under the Wine test's row, what an empty
// list says: Y downloads the sample program (a tap on the line is Y), and
// where programs are copied to. Main thread.
func windowsAppsDrawStarterLines(renderer *sdl.Renderer, config *Config, font *ttf.Font, x, y, w, lineH int32) {
	text, busy, failed := windowsSampleEmptyLine("Y")
	color := accentColor
	switch {
	case busy:
		color = WarningInk()
	case failed:
		color = ToastErrorInk()
	}
	text = ellipsize(font, text, w)
	tw, _, _ := font.SizeUTF8(text)
	windowsSampleLineRect = sdl.Rect{X: x, Y: y - 2, W: int32(tw), H: lineH + 4}
	renderText(renderer, config, font, text, color, x, y)
	y += lineH + 4
	renderText(renderer, config, font, "or copy Windows apps into:", ColorTextSecondary(), x, y)
	y += lineH + 4
	renderText(renderer, config, font, ellipsize(font, windowsAppsDir(), w), accentColor, x, y)
	y += lineH + 4
	renderText(renderer, config, font, "then press "+hintKeyFor("Start", "Rescan")+" to rescan.", ColorTextTertiary(), x, y)
}

// windowsWineTestSetupRow is the setup page's Wine test row.
func windowsWineTestSetupRow() windowsSetupRow {
	row := windowsSetupRow{Title: "Wine test (built in)", Status: "optional"}
	if wineTestInstalled() {
		row.Status = "installed"
		row.Detail = hintSentence("%s starts it: one window that shows Wine works, with its version, the pad and sound", "A", "Start")
		return row
	}
	row.Detail = "A small Windows program inside JukaHub, no download · " + hintSentence("%s puts it in the list and starts it", "A", "Start")
	return row
}

// windowsAppsPanelWineTest is A on the setup page's Wine test row: the test is
// put back if it is missing and starts the way A on its row would (the setup
// page first when the runtime still misses a piece). Main thread.
func windowsAppsPanelWineTest() {
	if err := wineTestEnsure(true); err != nil {
		PlayErrorSound()
		ShowToast("Wine test: "+capitalizeFirst(err.Error()), ToastKindWarning)
		return
	}
	invalidateWindowsAppsCache()
	invalidateWindowsSetupSnapshot()
	entry, ok := wineTestEntry()
	if !ok {
		ShowToast("The Wine test is in "+wineTestDir()+" · "+hintSentence("%s rescans the folders", "Start", "Rescan"), ToastKindInfo)
		return
	}
	windowsAppsStartSample(entry)
}

// wineTestVerdict reads the session the test just had from its log and says
// how it went. It runs on the launch's goroutine; the toast is handed to the
// main thread, which shows it once JukaHub has the screen back.
func wineTestVerdict(logPath string, err error) {
	say := func(msg string, kind ToastKind) { tbxPost(func() { ShowToast(msg, kind) }) }
	lines := wineTestSessionLines(logPath)
	frames := ""
	drawn, sound, noSound := false, false, false
	for _, line := range lines {
		switch {
		case strings.Contains(line, "jukahub-winetest: first frame drawn"):
			drawn = true
		case strings.Contains(line, "jukahub-winetest: sound played"):
			sound = true
		case strings.Contains(line, "jukahub-winetest: sound not available"):
			noSound = true
		case strings.Contains(line, "jukahub-winetest: closed after"):
			frames = wineTestFrames(line)
		}
	}
	switch {
	case drawn:
		msg := "Wine test passed: Windows drew its window"
		if frames != "" {
			msg += " (" + frames + " frames)"
		}
		switch {
		case sound:
			msg += " and played its chime"
		case noSound:
			msg += " · Windows found no sound device"
		}
		logInfo("windowsapps", "%s", msg)
		say(msg, ToastKindSuccess)
	case len(lines) > 0 && wineTestStarted(lines):
		logWarn("windowsapps", "Wine test ran but its window never drew (log %s)", logPath)
		say("Wine test: the program ran but its window never appeared · its log says why", ToastKindWarning)
	case err == nil:
		// Nothing of its own in the log: it never started (the launch's own
		// failure dialog covers the errors).
		say("Wine test: the program did not report back · see its log", ToastKindWarning)
	}
}

// wineTestDrew reports a session of the test, in the log at logPath, whose
// window was drawn: it worked.
func wineTestDrew(logPath string) bool {
	for _, line := range wineTestSessionLines(logPath) {
		if strings.Contains(line, "jukahub-winetest: first frame drawn") {
			return true
		}
	}
	return false
}

func wineTestStarted(lines []string) bool {
	for _, line := range lines {
		if strings.Contains(line, "jukahub-winetest: started") {
			return true
		}
	}
	return false
}

// wineTestFrames is the frame count of a "closed after N s, M frames" line.
func wineTestFrames(line string) string {
	i := strings.Index(line, " frames")
	if i < 0 {
		return ""
	}
	head := line[:i]
	j := strings.LastIndex(head, " ")
	if j < 0 {
		return ""
	}
	if _, err := strconv.Atoi(head[j+1:]); err != nil {
		return ""
	}
	return head[j+1:]
}

// wineTestSessionLines are the log's lines since its last session started
// (wineOpenLog's "=== … start" line), at most the last 64 KB of it.
func wineTestSessionLines(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	const tail = 64 << 10
	if info, err := f.Stat(); err == nil && info.Size() > tail {
		_, _ = f.Seek(info.Size()-tail, io.SeekStart)
	}
	data, _ := io.ReadAll(io.LimitReader(f, tail))
	text := string(data)
	if i := strings.LastIndex(text, "\n=== "); i >= 0 {
		text = text[i+1:]
	}
	return strings.Split(text, "\n")
}
