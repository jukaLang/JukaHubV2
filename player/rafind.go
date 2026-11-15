package main

// rafind.go — finding the handheld's RetroArch wherever the card keeps it.
//
// Every reader used to look in a short fixed list — RetroArch/ and
// Apps/RetroArch/ on /mnt/SDCARD and the card JukaHub runs from — for a binary
// with one of four names, and a card that kept RetroArch anywhere else (a
// CFW's Emus/RetroArch or System/RetroArch, a second card, a renamed build)
// got "RetroArch not found" beside games its own launch scripts could start
// (Juka, 2026-11-15: "have better searching for RetroArch"). raLocateDevice is
// the search they now fall back on when that list finds nothing. It looks, in
// order, at:
//
//  1. more fixed folders on every card (RetroArch, Apps/RetroArch,
//     Emus/RetroArch, Emus/RA, System/RetroArch, RetroArch64…) and the
//     firmware's own (/usr/trimui/RetroArch…), for any RetroArch-named file;
//  2. a RetroArch running right now (/proc/<pid>/exe);
//  3. what the firmware's launch scripts name — Emus/<system>/*.sh and
//     CrossMix's shared System/usr/trimui/scripts — : a path to a RetroArch
//     binary, or a folder a variable is set to (RA_DIR=/mnt/SDCARD/RetroArch)
//     that holds one, with $VARS and $(dirname "$0") filled in where the
//     script sets them to plain paths;
//  4. a walk of each card, three folders deep, past the folders that only
//     hold games, art and saves.
//
// Apart from the first step a hit must be an ELF program: a file named
// "retroarch" can be a firmware's wrapper script. The answer is cached (a hit
// until the file goes away, a miss for raLocateMissTTL) and logged once per
// change, with every place looked at when nothing was found, so a report says
// where the card was searched. The handheld only: Windows has its own catalog
// (emuWinExe), and a desktop Linux without a card has nothing to search.

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// raLocateMissTTL is how long "no RetroArch anywhere" is trusted: the
	// catalog asks often, and a walk of the card is not free. An install
	// (invalidateEmulatorCache) forgets it at once.
	raLocateMissTTL = 30 * time.Minute
	// raLocateWalkDepth and raLocateWalkMax bound the card walk: folders below
	// the card root, and directory entries read in all.
	raLocateWalkDepth = 3
	raLocateWalkMax   = 6000
	// raLocateScriptMax bounds one launch script's read; raLocateScriptsPerDir
	// how many scripts of one emulator folder are read.
	raLocateScriptMax     = 64 << 10
	raLocateScriptsPerDir = 8
)

// raBinaryNameRe is a RetroArch binary's file name: the upstream name (and
// 32/64 variants) or TrimUI's own builds, ra64.trimui and the like.
var raBinaryNameRe = regexp.MustCompile(`(?i)^(?:retroarch(?:64|32)?|ra(?:64|32)\.[a-z0-9_]+)$`)

// raScriptRefRe finds a RetroArch binary or folder named in a launch script:
// an optional path, $VARS allowed, ending in such a name.
var raScriptRefRe = regexp.MustCompile(`(?i)[A-Za-z0-9_./${}:~+-]*(?:retroarch(?:64|32)?|ra(?:64|32)\.[a-z0-9_]+)`)

// raScriptAssignRe is a plain assignment in a script: NAME=value, quoted or
// not, optionally exported.
var raScriptAssignRe = regexp.MustCompile(`(?m)^[ \t]*(?:export[ \t]+|local[ \t]+|readonly[ \t]+)?([A-Za-z_][A-Za-z0-9_]*)=(?:"([^"\n]*)"|'([^'\n]*)'|([^\s;#&|]*))`)

// raScriptVarRe is a variable use: ${NAME}, ${NAME:-default} / ${NAME-default}
// (groups 1–3), or $NAME (group 4).
var raScriptVarRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:?-([^}]*))?\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// raLocateSkipDirs are folders the card walk never enters: games, art, saves
// and other bulk that never holds RetroArch (lower case).
var raLocateSkipDirs = map[string]bool{
	"roms": true, "imgs": true, "images": true, "saves": true, "savestates": true,
	"states": true, "screenshots": true, "videos": true, "music": true, "bios": true,
	"thumbnails": true, "covers": true, "boxart": true, "lost+found": true,
	"cache": true, "logs": true, "jukahub": true, "cores": true, "playlists": true,
	"overlays": true, "shaders": true, "system volume information": true,
}

// raLocation is one search's answer: the binary ("" when none), how it was
// found, and every folder or script looked at.
type raLocation struct {
	Bin    string
	Via    string
	Looked []string
}

// raLocateCache is the last answer. gen counts raLocateForget calls: a search
// that started before one does not store its answer. The lock is never held
// during a search, so the main thread never waits on another's.
var raLocateCache struct {
	mu  sync.Mutex
	at  time.Time
	loc raLocation
	ok  bool
	gen uint64
}

// The firmware's own RetroArch folders, and the running-RetroArch probe:
// variables so a test is not answered by the machine it runs on.
var raSystemDirs = []string{"/usr/trimui/RetroArch", "/usr/trimui/apps/RetroArch", "/usr/local/RetroArch", "/opt/retroarch", "/opt/RetroArch"}

var raRunningBinaryFn = raRunningBinary

// raLocateDevice is the handheld's RetroArch as the search finds it, cached:
// a hit is kept while its file is there, a miss for raLocateMissTTL or until
// raLocateForget.
func raLocateDevice() raLocation {
	raLocateCache.mu.Lock()
	if raLocateCache.ok {
		loc := raLocateCache.loc
		if (loc.Bin != "" && regularFile(loc.Bin)) || (loc.Bin == "" && time.Since(raLocateCache.at) < raLocateMissTTL) {
			raLocateCache.mu.Unlock()
			return loc
		}
	}
	gen := raLocateCache.gen
	raLocateCache.mu.Unlock()

	loc := raLocateSearch(raLocateCardRoots())

	raLocateCache.mu.Lock()
	if raLocateCache.gen == gen {
		raLocateCache.loc, raLocateCache.at, raLocateCache.ok = loc, time.Now(), true
	}
	raLocateCache.mu.Unlock()
	raLocateNote(loc)
	return loc
}

// raLocateForget drops the cached answer, so the next ask searches again (a
// RetroArch just installed, a card swapped).
func raLocateForget() {
	raLocateCache.mu.Lock()
	raLocateCache.ok = false
	raLocateCache.gen++
	raLocateCache.mu.Unlock()
}

// raLocateNote logs the answer once per change.
func raLocateNote(loc raLocation) {
	if !raPathChanged("locate", loc.Bin) {
		return
	}
	if loc.Bin != "" {
		logInfo("emulators", "RetroArch: %s (found by %s)", loc.Bin, loc.Via)
		return
	}
	if len(loc.Looked) == 0 {
		return
	}
	looked := loc.Looked
	if len(looked) > 40 {
		looked = append(append([]string(nil), looked[:40]...), "…")
	}
	logWarn("emulators", "no RetroArch binary anywhere; looked in %s, at running programs, and walked the card %d folders deep",
		strings.Join(looked, ", "), raLocateWalkDepth)
}

// raLocateCardRoots are the cards to search: the one JukaHub runs from, then
// the handheld's usual mount points, each once (a symlinked /mnt/sdcard is
// /mnt/SDCARD). None on Windows.
func raLocateCardRoots() []string {
	if IsWindows() {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, root := range []string{trimuiSDRootFn(), "/mnt/SDCARD", "/mnt/sdcard", "/mnt/SDCARD2", "/mnt/sdcard2", "/mnt/mmc"} {
		root = strings.TrimSpace(root)
		if root == "" || !dirExists(root) {
			continue
		}
		key := filepath.Clean(root)
		if real, err := filepath.EvalSymlinks(key); err == nil {
			key = real
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, filepath.Clean(root))
	}
	return out
}

// raLocateSearch is the search itself over the given cards (the steps in the
// file comment). With no card and not on a TrimUI it finds nothing: a desktop
// Linux RetroArch is netplayRetroArchPath's business.
func raLocateSearch(cards []string) raLocation {
	var loc raLocation
	if len(cards) == 0 && !IsTrimuiDevice() {
		return loc
	}
	// 1. The fixed folders.
	var dirs []string
	for _, card := range cards {
		for _, rel := range []string{"RetroArch", "Apps/RetroArch", "Emus/RetroArch", "Emus/RA",
			"System/RetroArch", "RetroArch64", "Apps/RetroArch64", "Tools/RetroArch"} {
			dirs = append(dirs, filepath.Join(card, filepath.FromSlash(rel)))
		}
	}
	dirs = append(dirs, raSystemDirs...)
	for _, dir := range dirs {
		if !dirExists(dir) {
			continue
		}
		loc.Looked = append(loc.Looked, dir)
		if bin := raBinaryIn(dir, false); bin != "" {
			loc.Bin, loc.Via = bin, "its folder"
			return loc
		}
	}
	// 2. A RetroArch running now.
	if bin := raRunningBinaryFn(); bin != "" {
		loc.Bin, loc.Via = bin, "the running RetroArch"
		return loc
	}
	// 3. The launch scripts.
	for _, script := range raLaunchScripts(cards) {
		loc.Looked = append(loc.Looked, script)
		if bin := raBinaryFromScript(script); bin != "" {
			loc.Bin, loc.Via = bin, "the launch script "+script
			return loc
		}
	}
	// 4. The cards themselves.
	for _, card := range cards {
		if bin := raWalkCard(card); bin != "" {
			loc.Bin, loc.Via = bin, "searching "+card
			return loc
		}
		loc.Looked = append(loc.Looked, card+" (searched)")
	}
	return loc
}

// raBinaryIn is the RetroArch binary in dir: the usual names first
// (netplayRetroArchNames), then any other RetroArch-named file. elfOnly: the
// file must be an ELF program, not a wrapper script.
func raBinaryIn(dir string, elfOnly bool) string {
	ok := func(p string) bool {
		return regularFile(p) && (!elfOnly || raIsELF(p))
	}
	for _, name := range netplayRetroArchNames {
		if p := filepath.Join(dir, name); ok(p) {
			return p
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && raBinaryNameRe.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool {
		if ri, rj := raNameRank(names[i]), raNameRank(names[j]); ri != rj {
			return ri < rj
		}
		return names[i] < names[j]
	})
	for _, name := range names {
		if p := filepath.Join(dir, name); ok(p) {
			return p
		}
	}
	return ""
}

// raNameRank orders RetroArch file names, best first: TrimUI's 64-bit build,
// its 32-bit one, then the upstream names.
func raNameRank(name string) int {
	lower := strings.ToLower(name)
	switch {
	case strings.HasPrefix(lower, "ra64."):
		return 0
	case strings.HasPrefix(lower, "ra32."):
		return 1
	case lower == "retroarch":
		return 2
	}
	return 3
}

// raIsELF reports whether p starts with the ELF magic.
func raIsELF(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 4)
	n, _ := io.ReadFull(f, head)
	return n == 4 && bytes.Equal(head, []byte("\x7fELF"))
}

// raRunningBinary is the RetroArch binary a running process runs, or "".
func raRunningBinary() string {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if strings.Trim(e.Name(), "0123456789") != "" {
			continue
		}
		exe, err := os.Readlink(filepath.Join("/proc", e.Name(), "exe"))
		if err != nil {
			continue
		}
		exe = strings.TrimSuffix(exe, " (deleted)")
		if raBinaryNameRe.MatchString(filepath.Base(exe)) && regularFile(exe) {
			return exe
		}
	}
	return ""
}

// raLaunchScripts lists the firmware's launch scripts to read: every
// emulator folder's (launch.sh first, at most raLocateScriptsPerDir each) on
// every card and in the firmware, then CrossMix's shared launcher scripts.
func raLaunchScripts(cards []string) []string {
	var roots []string
	for _, card := range cards {
		roots = append(roots, filepath.Join(card, "Emus"))
	}
	roots = append(roots, deviceEmuRoots...)
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if key := filepath.Clean(p); !seen[key] {
			seen[key] = true
			out = append(out, p)
		}
	}
	for _, root := range roots {
		emus, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, emu := range emus {
			if !emu.IsDir() {
				continue
			}
			for _, script := range raScriptsIn(filepath.Join(root, emu.Name())) {
				add(script)
			}
		}
	}
	for _, card := range cards {
		for _, script := range raScriptsIn(filepath.Join(card, "System", "usr", "trimui", "scripts")) {
			add(script)
		}
	}
	return out
}

// raScriptsIn lists the .sh files of one folder, launch.sh first, at most
// raLocateScriptsPerDir.
func raScriptsIn(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".sh") {
			names = append(names, e.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool {
		li, lj := strings.EqualFold(names[i], "launch.sh"), strings.EqualFold(names[j], "launch.sh")
		if li != lj {
			return li
		}
		return names[i] < names[j]
	})
	if len(names) > raLocateScriptsPerDir {
		names = names[:raLocateScriptsPerDir]
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, filepath.Join(dir, name))
	}
	return out
}

// raBinaryFromScript is the RetroArch binary a launch script names, or "".
func raBinaryFromScript(script string) string {
	f, err := os.Open(script)
	if err != nil {
		return ""
	}
	data, _ := io.ReadAll(io.LimitReader(f, raLocateScriptMax))
	_ = f.Close()
	return raBinaryFromScriptText(string(data), filepath.Dir(script))
}

// raBinaryFromScriptText reads a script's text (the folder it sits in given)
// for a RetroArch binary that exists: the paths it names, its $VARS filled in
// from its own plain assignments, and the folders those variables name.
func raBinaryFromScriptText(text, scriptDir string) string {
	slashDir := filepath.ToSlash(scriptDir)
	for _, form := range []string{`$(dirname "$0")`, `$(dirname $0)`, "`dirname \"$0\"`", "`dirname $0`",
		`$(cd "$(dirname "$0")" && pwd)`, `$(cd "$(dirname "$0")"; pwd)`, `$(cd $(dirname $0) && pwd)`} {
		text = strings.ReplaceAll(text, form, slashDir)
	}
	vars := raScriptVars(text)
	var dirs []string
	check := func(raw string) string {
		p, ok := raExpand(raw, vars)
		if !ok {
			return ""
		}
		if !strings.HasPrefix(p, "/") {
			p = slashDir + "/" + p
		}
		p = filepath.Clean(filepath.FromSlash(p))
		if raBinaryNameRe.MatchString(filepath.Base(p)) && regularFile(p) && raIsELF(p) {
			return p
		}
		switch {
		case dirExists(p):
			dirs = append(dirs, p)
		case !regularFile(p) && dirExists(filepath.Dir(p)):
			// RetroArch/retroarch.cfg, RetroArch/ra64.trimui_PSP.sh: the name
			// was part of a longer one, and its folder may hold the binary.
			dirs = append(dirs, filepath.Dir(p))
		}
		return ""
	}
	for _, m := range raScriptRefRe.FindAllString(text, -1) {
		if bin := check(m); bin != "" {
			return bin
		}
	}
	// Folders the script named for RetroArch, then every plain folder a
	// variable is set to (RA_DIR=/mnt/SDCARD/RetroArch, then "$RA_DIR/$RA_BIN").
	names := make([]string, 0, len(vars))
	for name := range vars {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if v := vars[name]; strings.HasPrefix(v, "/") {
			if p := filepath.Clean(filepath.FromSlash(v)); dirExists(p) {
				dirs = append(dirs, p)
			}
		}
	}
	for _, dir := range dirs {
		if bin := raBinaryIn(dir, true); bin != "" {
			return bin
		}
	}
	return ""
}

// raScriptVars are a script's plain assignments with every $VAR they use
// filled in from the assignments before them; a value that still holds a $
// (a command, an unknown variable) is left out. The last plain assignment of
// a name wins.
func raScriptVars(text string) map[string]string {
	vars := map[string]string{}
	for _, m := range raScriptAssignRe.FindAllStringSubmatch(text, -1) {
		value := m[2] + m[3] + m[4]
		if value == "" {
			continue
		}
		if expanded, ok := raExpand(value, vars); ok {
			vars[m[1]] = expanded
		}
	}
	return vars
}

// raExpand fills $NAME and ${NAME} from vars, and ${NAME:-default} with the
// default when NAME is unknown (RA_DIR=${RA_DIR:-/mnt/SDCARD/RetroArch});
// false when a variable is unknown or a command substitution is left.
func raExpand(s string, vars map[string]string) (string, bool) {
	ok := true
	out := raScriptVarRe.ReplaceAllStringFunc(s, func(ref string) string {
		m := raScriptVarRe.FindStringSubmatch(ref)
		name := m[1] + m[4]
		if v, found := vars[name]; found && (v != "" || m[2] == "") {
			return v
		}
		if m[2] != "" {
			if def, defOK := raExpand(m[3], vars); defOK {
				return def
			}
		}
		ok = false
		return ref
	})
	out = strings.Trim(out, `"'`)
	if !ok || strings.ContainsAny(out, "$`()") || out == "" {
		return "", false
	}
	return out, true
}

// raWalkCard walks one card raLocateWalkDepth folders deep for a RetroArch
// binary, past raLocateSkipDirs and hidden folders, reading at most
// raLocateWalkMax entries. The first hit in name order wins.
func raWalkCard(card string) string {
	budget := raLocateWalkMax
	var walk func(dir string, depth int) string
	walk = func(dir string, depth int) string {
		if budget <= 0 {
			return ""
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return ""
		}
		budget -= len(entries)
		var sub []string
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() {
				if depth < raLocateWalkDepth && !strings.HasPrefix(name, ".") && !raLocateSkipDirs[strings.ToLower(name)] {
					sub = append(sub, name)
				}
				continue
			}
			if raBinaryNameRe.MatchString(name) {
				if p := filepath.Join(dir, name); e.Type().IsRegular() && raIsELF(p) {
					return p
				}
			}
		}
		for _, name := range sub {
			if bin := walk(filepath.Join(dir, name), depth+1); bin != "" {
				return bin
			}
		}
		return ""
	}
	return walk(card, 0)
}

// raCoresDirFor is the cores folder of the RetroArch at bin: TrimUI's layout
// (.retroarch/cores beside it) or a plain one (cores beside it), whichever
// exists, else TrimUI's.
func raCoresDirFor(bin string) string {
	dir := filepath.Dir(bin)
	for _, cores := range []string{filepath.Join(dir, ".retroarch", "cores"), filepath.Join(dir, "cores")} {
		if dirExists(cores) {
			return cores
		}
	}
	return filepath.Join(dir, ".retroarch", "cores")
}
