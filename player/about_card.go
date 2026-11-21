package main

// about_card.go — Settings → About: how full the card is, and which emulator
// versions are installed.
//
// Both answer questions people otherwise ask in a bug report: "is my card
// full?" and "which RetroArch / which core do I have?". Two rows:
//
//   - Card space: used, free and total of the card the games are on (the
//     filesystem under the first library root, measured as sdhealth.go does),
//     with the format and, on a TrimUI handheld, its internal storage when
//     that is a separate partition (/mnt/UDISK, Allwinner's usual user
//     partition). The numbers come from one statfs call, refreshed off the
//     render thread at most every aboutCardTTL, since a row's value is asked
//     every frame.
//   - Emulator versions: a read-only list sheet (setsheet.go), filled in the
//     background like Drivers & hardware is. RetroArch's version comes from,
//     in order: the log JukaHub has RetroArch write for its own launches
//     (gamediag.go) when that log is newer than the binary; the binary itself
//     — RetroArch keeps its version as a bare string ("1.19.1"), so a single
//     plausible candidate among the binary's strings is taken, and two or more
//     mean "not reported" rather than a guess; and on Linux, as a last resort,
//     "--version" under a three-second deadline (RetroArch prints it and quits
//     before touching the screen). Each libretro core's version is its .info
//     file's display_version (the info folder retroarch.cfg names, else the
//     cores folder and an "info" folder beside it), else the version its file
//     name carries. PPSSPP on the handheld is the line the Emulators page's PSP
//     tile shows (ppsspp_managed_ui.go), and on Windows every emulator JukaHub
//     installed reports the release it installed (emuwininstall.go). Answers
//     are cached by the binary's size and modification time, so a second look
//     reads nothing big again.

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// aboutCardTTL is how often the card row measures again.
	aboutCardTTL = 10 * time.Second
	// aboutVersionsTitle is the sheet's title, and how a late answer finds it.
	aboutVersionsTitle = "Emulator versions"
	// aboutVersionsTTL keeps a collected list for a second look.
	aboutVersionsTTL = 2 * time.Minute
	// aboutRAScanMax bounds the RetroArch binary scan.
	aboutRAScanMax = 128 << 20
	// aboutRARunLimit is the "--version" deadline.
	aboutRARunLimit = 3 * time.Second
	// aboutInfoFileMax bounds a core .info or log head read.
	aboutInfoFileMax = 64 << 10
)

// aboutInternalStorage is the handheld's separate user partition, when there
// is one. A variable so a test can point it elsewhere.
var aboutInternalStorage = "/mnt/UDISK"

// ── Card usage ───────────────────────────────────────────────────────────

// aboutCardUsage is one measurement.
type aboutCardUsage struct {
	Total, Free   int64
	FSType        string
	InternalTotal int64
	InternalFree  int64
	OK            bool
}

var aboutCardCache struct {
	mu    sync.Mutex
	at    time.Time
	usage aboutCardUsage
	busy  atomic.Bool
}

// aboutCardPath is the folder whose filesystem counts as "the card": the
// first library root that exists, else the card JukaHub runs from, else
// JukaHub's own folder.
func aboutCardPath() string {
	for _, root := range romLibraryRoots() {
		if dirExists(root) {
			return root
		}
	}
	if root := bkupCardRoot(); root != "" {
		return root
	}
	if dir, err := P().ExecutableDir(); err == nil {
		return dir
	}
	return ""
}

// aboutMeasureCard takes one measurement. Off the render thread.
func aboutMeasureCard() aboutCardUsage {
	var u aboutCardUsage
	path := aboutCardPath()
	if path == "" {
		return u
	}
	total, free, err := diskUsage(path)
	if err != nil || total <= 0 {
		return u
	}
	u.Total, u.Free, u.OK = total, free, true
	u.FSType = fsTypeFor(readMounts(), path)
	if P().IsHandheld() && dirExists(aboutInternalStorage) {
		// Only a partition of its own: a folder on the card (or on the root
		// filesystem) reports the same numbers as something already shown.
		if it, ifree, err := diskUsage(aboutInternalStorage); err == nil && it > 0 && it != total {
			u.InternalTotal, u.InternalFree = it, ifree
		}
	}
	return u
}

// aboutCardCached is the last measurement; a stale one starts a new one.
func aboutCardCached() aboutCardUsage {
	aboutCardCache.mu.Lock()
	u, at := aboutCardCache.usage, aboutCardCache.at
	aboutCardCache.mu.Unlock()
	if (at.IsZero() || time.Since(at) > aboutCardTTL) && aboutCardCache.busy.CompareAndSwap(false, true) {
		go func() {
			defer aboutCardCache.busy.Store(false)
			m := aboutMeasureCard()
			aboutCardCache.mu.Lock()
			aboutCardCache.usage, aboutCardCache.at = m, time.Now()
			aboutCardCache.mu.Unlock()
		}()
	}
	return u
}

// aboutCardRow is Settings → About → Card space ("Game storage" on a
// computer, where the library is a folder on a disk).
func aboutCardRow() setRow {
	label := "Card space"
	if !P().IsHandheld() {
		label = "Game storage"
	}
	return setRow{
		ID: "cardUsage", Label: label, Kind: setKindInfo,
		Detail: aboutCardDetail,
		Value: func(*Config) string {
			u := aboutCardCached()
			if !u.OK {
				return "…"
			}
			return formatSize(u.Free) + " free of " + formatSize(u.Total)
		},
	}
}

// aboutCardDetail is the row's second line: used space, format, internal
// storage.
func aboutCardDetail(*Config) string {
	u := aboutCardCached()
	if !u.OK {
		return "Measuring the card where your games are"
	}
	used := u.Total - u.Free
	if used < 0 {
		used = 0
	}
	parts := []string{formatSize(used) + " used (" + strconv.Itoa(aboutPercent(used, u.Total)) + "%)"}
	if u.FSType != "" {
		parts = append(parts, u.FSType)
	}
	if u.InternalTotal > 0 {
		parts = append(parts, "internal storage "+formatSize(u.InternalFree)+" free of "+formatSize(u.InternalTotal))
	}
	return strings.Join(parts, " · ")
}

// aboutPercent is part of whole in whole percent, 0 for an empty whole.
func aboutPercent(part, whole int64) int {
	if whole <= 0 || part <= 0 {
		return 0
	}
	return int((part*100 + whole/2) / whole)
}

// ── Emulator versions ────────────────────────────────────────────────────

// aboutVersionsBusy is up while a collection runs, so a second press does not
// start another (the running one fills the sheet).
var aboutVersionsBusy atomic.Bool

var aboutVersionsCache struct {
	mu   sync.Mutex
	at   time.Time
	opts []setOption
}

// aboutVersionsRow is Settings → About → Emulator versions.
func aboutVersionsRow() setRow {
	return setRow{
		ID: "emuVersions", Label: "Emulator versions", Kind: setKindAction,
		Detail: setStatic("RetroArch, each core and the standalone emulators; worth quoting in a bug report"),
		Open:   aboutVersionsOpen,
	}
}

// aboutVersionsOpen opens the sheet at once and fills it when the answers
// are in.
func aboutVersionsOpen(*Config) {
	aboutVersionsCache.mu.Lock()
	opts, at := aboutVersionsCache.opts, aboutVersionsCache.at
	aboutVersionsCache.mu.Unlock()
	note := "Installed emulators and cores · read-only"
	if len(opts) > 0 && time.Since(at) < aboutVersionsTTL {
		setSheetOpen(&setHub.sheet, aboutVersionsTitle, note, opts, "", nil)
		return
	}
	setSheetOpen(&setHub.sheet, aboutVersionsTitle, note,
		[]setOption{{Value: "reading", Label: "Reading versions…"}}, "", nil)
	if !aboutVersionsBusy.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer aboutVersionsBusy.Store(false)
		opts := aboutCollectVersions()
		aboutVersionsCache.mu.Lock()
		aboutVersionsCache.opts, aboutVersionsCache.at = opts, time.Now()
		aboutVersionsCache.mu.Unlock()
		for _, o := range opts {
			logInfo("about", "version: %s · %s", o.Label, o.Note)
		}
		tbxPost(func() {
			if setHub.sheet.open && setHub.sheet.title == aboutVersionsTitle {
				setHub.sheet.options = opts
				setHub.sheet.cursor = 0
				setHub.sheet.scroll = 0
			}
		})
	}()
}

// aboutCollectVersions builds the sheet's lines: RetroArch, the standalone
// emulators, then every active core by name. Off the render thread.
func aboutCollectVersions() []setOption {
	var opts []setOption
	inv := scanCoreInventory()
	bin := netplayRetroArchPath()
	if bin == "" && inv.CoresDir != "" {
		bin = coreRetroArchBinary(inv.CoresDir)
	}
	if bin == "" {
		opts = append(opts, setOption{Value: "retroarch", Label: "RetroArch · not found"})
	} else {
		v, how := aboutRetroArchVersion(bin)
		opts = append(opts, setOption{
			Value: "retroarch",
			Label: "RetroArch · " + setOr(v, "version not reported"),
			Note:  aboutShortPath(bin) + " · " + how,
		})
	}
	opts = append(opts, aboutStandaloneVersions()...)

	if len(inv.Builds) == 0 {
		opts = append(opts, setOption{Value: "cores", Label: "No libretro cores found", Note: inv.Problem})
		return opts
	}
	infoDirs := aboutCoreInfoDirs(inv.CoresDir)
	cores := make([]setOption, 0, len(inv.Builds))
	for _, b := range inv.Builds {
		name, version, how := aboutCoreVersion(b, infoDirs)
		cores = append(cores, setOption{
			Value: "core:" + b.Core,
			Label: setOr(name, b.Core) + " · " + setOr(version, "version not reported"),
			Note:  b.File + " · " + how,
		})
	}
	sort.SliceStable(cores, func(i, j int) bool { return strings.ToLower(cores[i].Label) < strings.ToLower(cores[j].Label) })
	return append(opts, cores...)
}

// aboutShortPath names a program by its file and folder, not its full path
// (a Windows path carries the account's name).
func aboutShortPath(p string) string {
	return filepath.Base(p) + " in " + filepath.Base(filepath.Dir(p))
}

// aboutStandaloneVersions are the emulators outside RetroArch whose version
// JukaHub already tracks: PPSSPP on the handheld, and on Windows every
// emulator the Emulators page installed.
func aboutStandaloneVersions() []setOption {
	var opts []setOption
	if ppssppManagedEnabled() {
		fw, haveFw := ppssppFirmwareCached()
		if !haveFw {
			fw, haveFw = ppssppDetectFirmwareNow(), true
		}
		platform, _ := ppssppPlatform()
		opts = append(opts, setOption{
			Value: "ppsspp",
			Label: "PPSSPP",
			Note:  ppssppTileLine(ppssppSnapshot(), fw, haveFw, platform),
		})
	}
	if IsWindows() {
		for _, spec := range emuWinSpecs {
			if spec.Name == "RetroArch" {
				continue // its own line, read from the binary
			}
			if _, m, ok := emuWinManaged(spec.Name); ok {
				opts = append(opts, setOption{
					Value: "win:" + spec.Name,
					Label: spec.Name + " · " + setOr(strings.TrimSpace(m.Tag), "version not reported"),
					Note:  "Installed by JukaHub's Emulators page",
				})
			}
		}
	}
	return opts
}

// ── RetroArch ────────────────────────────────────────────────────────────

// aboutRAScan caches the binary's answer by path, size and modification time.
var aboutRAScan struct {
	mu      sync.Mutex
	path    string
	size    int64
	mod     int64
	version string
	how     string
}

// aboutRetroArchVersion is RetroArch's version and how it was learnt.
func aboutRetroArchVersion(bin string) (version, how string) {
	info, err := os.Stat(bin)
	if err != nil {
		return "", "not readable"
	}
	// The log is cheap and names the build that ran, so it is asked first,
	// every time: a game played since the last look may have written it.
	if v := aboutRAVersionFromLog(info.ModTime()); v != "" {
		return v, "from the last game's log"
	}
	size, mod := info.Size(), info.ModTime().UnixNano()
	aboutRAScan.mu.Lock()
	if aboutRAScan.path == bin && aboutRAScan.size == size && aboutRAScan.mod == mod {
		version, how = aboutRAScan.version, aboutRAScan.how
		aboutRAScan.mu.Unlock()
		return version, how
	}
	aboutRAScan.mu.Unlock()

	how = "the program does not say"
	if v := aboutRAVersionFromBinary(bin); v != "" {
		version, how = v, "read from the program file"
	} else if runtime.GOOS == "linux" {
		if v := aboutRAVersionFromRun(bin); v != "" {
			version, how = v, "asked the program"
		}
	}
	aboutRAScan.mu.Lock()
	aboutRAScan.path, aboutRAScan.size, aboutRAScan.mod = bin, size, mod
	aboutRAScan.version, aboutRAScan.how = version, how
	aboutRAScan.mu.Unlock()
	return version, how
}

// aboutReadHead reads at most max bytes from the start of path.
func aboutReadHead(path string, max int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max))
	if err != nil {
		return ""
	}
	return string(data)
}

// aboutRAVersionFromLog reads the version line of the log RetroArch wrote for
// JukaHub's last launch, when the log is newer than the binary (a log from
// before an update names the old build). A TrimUI clock that started behind
// makes the log look older, which only skips this source.
func aboutRAVersionFromLog(binMod time.Time) string {
	dir := gameDiagLogDir()
	if dir == "" {
		return ""
	}
	path := filepath.Join(dir, "retroarch.log")
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 || info.ModTime().Before(binMod) {
		return ""
	}
	return aboutRAVersionInText(aboutReadHead(path, aboutInfoFileMax))
}

// aboutRAVersionRe finds RetroArch's version in its log or "--version" output:
// "RetroArch 1.19.1 (Git …)", "Version: 1.19.1", "RetroArch: Frontend for
// libretro -- v1.19.1". The word must stand alone, so "GL_VERSION: OpenGL ES
// 3.2" in the same log is not taken.
var aboutRAVersionRe = regexp.MustCompile(`(?i)\b(?:retroarch|version)\b[^0-9\n]{0,40}?v?([0-9]+\.[0-9]+(?:\.[0-9]+)?)`)

// aboutRAVersionInText is the first plausible RetroArch version in text (the
// build lines come first in RetroArch's log). Pure.
func aboutRAVersionInText(text string) string {
	for _, m := range aboutRAVersionRe.FindAllStringSubmatch(text, 8) {
		if aboutPlausibleRAVersion(m[1]) {
			return m[1]
		}
	}
	return ""
}

// aboutPlausibleRAVersion accepts 1.7 and newer: every RetroArch a TrimUI
// firmware or a Windows install has carried.
func aboutPlausibleRAVersion(v string) bool {
	parts := strings.Split(v, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	return major == 1 && minor >= 7 && minor < 100
}

// aboutRAVersionFromBinary scans the binary for its version string: a bare,
// NUL-terminated "1.N.N" with N of 7 or more. Bundled libraries carry version
// strings of their own (zlib's "1.3.1"), which the minor version mostly rules
// out; when more than one candidate survives, the answer is "not reported"
// rather than a guess.
func aboutRAVersionFromBinary(bin string) string {
	f, err := os.Open(bin)
	if err != nil {
		return ""
	}
	defer f.Close()
	found := map[string]bool{}
	const chunk = 1 << 20
	const carry = 16
	buf := make([]byte, chunk)
	work := make([]byte, 0, chunk+carry)
	r := io.LimitReader(f, aboutRAScanMax)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			work = append(work, buf[:n]...)
			aboutRACandidates(work, found)
			if len(work) > carry {
				copy(work, work[len(work)-carry:])
				work = work[:carry]
			}
		}
		if err != nil {
			break
		}
		if len(found) > 3 {
			return "" // a binary full of such strings: nothing to trust
		}
	}
	if len(found) != 1 {
		return ""
	}
	for v := range found {
		return v
	}
	return ""
}

// aboutRACandidates adds every NUL-delimited "1.N[.N]" string in data that
// aboutPlausibleRAVersion accepts. A string cut by the end of data is judged
// again with the carried tail. Pure.
func aboutRACandidates(data []byte, found map[string]bool) {
	for i := 0; i < len(data); {
		j := bytes.Index(data[i:], []byte("1."))
		if j < 0 {
			return
		}
		at := i + j
		i = at + 1
		// The byte before must be the previous string's NUL. At 0 it is not
		// in this window: a string cut there was judged with the window
		// before, where it did not start at 0 (the carry is longer).
		if at == 0 || data[at-1] != 0 {
			continue
		}
		k := at
		for k < len(data) && k-at < 10 && ((data[k] >= '0' && data[k] <= '9') || data[k] == '.') {
			k++
		}
		if k >= len(data) || data[k] != 0 {
			continue
		}
		if v := string(data[at:k]); aboutPlausibleRAVersion(v) {
			found[v] = true
		}
	}
}

// aboutRAVersionFromRun asks the binary itself, under a deadline, from its own
// folder. RetroArch parses "--version" before it opens a window or a config.
// Its argv[0] is not RetroArch's name, so the /proc scans that ask "is a game
// in RetroArch?" (retroArchRunning: the low-battery guard, the in-game
// watchers) never take this check for one; WaitDelay ends the wait when the
// deadline's kill leaves a child holding the output pipe.
func aboutRAVersionFromRun(bin string) string {
	ctx, cancel := context.WithTimeout(context.Background(), aboutRARunLimit)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--version")
	cmd.Args[0] = "jukahub-version-check"
	cmd.Dir = filepath.Dir(bin)
	cmd.Env = append(os.Environ(), "HOME="+filepath.Dir(bin))
	cmd.WaitDelay = time.Second
	out, _ := cmd.CombinedOutput()
	if len(out) > aboutInfoFileMax {
		out = out[:aboutInfoFileMax]
	}
	return aboutRAVersionInText(string(out))
}

// ── Cores ────────────────────────────────────────────────────────────────

// aboutCoreInfoDirs are the folders a core's .info file may be in: the one
// retroarch.cfg names (libretro_info_path), the cores folder itself (TrimUI's
// RetroArch keeps them together) and an "info" folder beside it or one up.
func aboutCoreInfoDirs(coresDir string) []string {
	var dirs []string
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" || !dirExists(dir) {
			return
		}
		dir = filepath.Clean(dir)
		for _, have := range dirs {
			if strings.EqualFold(have, dir) {
				return
			}
		}
		dirs = append(dirs, dir)
	}
	if text, cfgPath, ok := retroArchConfig(); ok {
		if v, ok := raConfigGet(text, "libretro_info_path"); ok {
			add(resolveRetroArchSaveDir(v, cfgPath))
		}
	}
	if coresDir != "" {
		add(coresDir)
		add(filepath.Join(filepath.Dir(coresDir), "info"))
		add(filepath.Join(filepath.Dir(filepath.Dir(coresDir)), "info"))
	}
	return dirs
}

// aboutCoreVersion is a core's display name and version and where the version
// came from.
func aboutCoreVersion(b coreBuild, infoDirs []string) (name, version, how string) {
	stem := strings.TrimSuffix(b.File, filepath.Ext(b.File))
	for _, dir := range infoDirs {
		text := aboutReadHead(filepath.Join(dir, stem+".info"), aboutInfoFileMax)
		if text == "" {
			continue
		}
		name, version = aboutParseCoreInfo(text)
		if version != "" {
			return name, version, "from its info file"
		}
		break
	}
	if b.Version != "" {
		return name, b.Version, "from its file name"
	}
	return name, "", "no info file says"
}

// aboutParseCoreInfo reads display_name and display_version from a libretro
// .info file (key = "value" lines). Pure.
func aboutParseCoreInfo(text string) (name, version string) {
	for _, line := range strings.Split(text, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"`)
		switch strings.TrimSpace(key) {
		case "display_name":
			name = strings.TrimSpace(value)
		case "display_version":
			version = strings.TrimSpace(value)
		}
	}
	return name, version
}
