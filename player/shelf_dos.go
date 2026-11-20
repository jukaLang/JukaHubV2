package main

// shelf_dos.go — the DOS shelf: what in Roms/DOS is a game for DOSBox Pure,
// which program starts a game kept as a folder, and the button layouts that
// make a keyboard-and-mouse PC playable on a handheld without sticks.
//
// DOSBox Pure opens a .zip or .dosz of a whole game (and asks once which
// program starts it), a program (.exe, .com, .bat), a DOSBox .conf, and disc
// and disk images. Those are games when they sit straight in Roms/DOS. A
// subfolder of Roms/DOS is one game, listed by one file: its start program,
// picked by the player in the game's DOS settings or else by a guess — a
// start script (dosbox.bat, start.bat …), the one program there is, the one
// named like the folder ("Prince of Persia" → PRINCE.EXE), else the largest
// that is not an installer or a sound setup. Nothing else in a game folder is
// listed: its SETUP.EXE, a .zip of extras, its CD image all belong to it. A
// folder with no program at all but one .zip, .dosz or disc image is listed
// by that file.
//
// Changing the start program changes the file the library lists, so the title
// (the folder's name), the settings and the playtime, all keyed by the folder
// or the title, stay; and a launch that still names the old program (a page
// opened before the change, the recents) is sent to the new one by the shelf's
// gate (shelf_controls_card.go).
//
// The rule runs inside the scan's walk, so it answers from a cache keyed by
// the game folder and its modification time, trusted for a few seconds
// without a stat.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// shelfDOSTopExts are what DOSBox Pure loads (its core info's extension list,
// without the cartridge and image formats nobody keeps in a DOS folder). .bin
// is a disc's track, listed through its .cue.
var shelfDOSTopExts = map[string]bool{
	".zip": true, ".dosz": true, ".exe": true, ".com": true, ".bat": true, ".conf": true,
	".iso": true, ".cue": true, ".img": true, ".ima": true, ".vhd": true, ".chd": true, ".m3u": true,
}

// shelfDOSDiscOrder ranks a folder game's disc images when it has no program.
var shelfDOSDiscOrder = []string{".m3u", ".cue", ".chd", ".iso", ".img", ".ima", ".vhd"}

// shelfDOSStartScripts are the names a start script goes by, best first.
var shelfDOSStartScripts = []string{"dosbox.bat", "start.bat", "run.bat", "play.bat", "game.bat", "go.bat"}

// shelfDOSNotStart are the stems (prefixes) of programs that are never a
// game's start: installers, setups, drivers, extenders, readers.
var shelfDOSNotStart = []string{
	"setup", "install", "instal", "uninst", "config", "setsound", "sound", "readme", "read", "order",
	"regist", "catalog", "ipxsetup", "sersetup", "dos4gw", "cwsdpmi", "pkunzip", "pkzip", "unzip",
	"univbe", "vesa", "help", "info", "manual", "patch", "update", "mouse", "drivers", "loadfix",
}

const (
	// shelfDOSSubMax bounds the subfolders a folder game's program is looked
	// for in when it has none of its own.
	shelfDOSSubMax = 8
	// shelfDOSReadMax bounds the names read from one folder.
	shelfDOSReadMax = 2000
)

// shelfDOSProg is one program (or image) of a folder game.
type shelfDOSProg struct {
	Rel  string // path under the game folder
	Size int64
}

// shelfDOSFolder is what the rule knows about one game folder.
type shelfDOSFolder struct {
	mod     time.Time
	checked time.Time
	start   string // full path of the listed file ("" none)
}

var shelfDOS struct {
	mu      sync.Mutex
	folders map[string]*shelfDOSFolder
}

func init() {
	romShelfRegister("DOS", shelfDOSListsFile)
	// DOSBox Pure's own mapping for the games it knows, and its on-screen
	// keyboard on Select: the core opens its menu and keyboard on L3, which
	// the Brick does not have and the Smart Pro's sticks may not click.
	ctrlLayoutRegister(ctrlLayout{
		ID:          "dos-auto",
		Name:        "DOS: the game's own mapping",
		Description: "DOSBox Pure maps the buttons for the games it knows; Select opens its keyboard.",
		Systems:     []string{"DOS"},
		DefaultFor:  []string{"DOS"},
		Buttons:     map[string]string{"select": "l3"},
		Chart: []ctrlLayoutLine{
			{Button: "Select", Action: "On-screen keyboard and menu"},
			{Button: "D-pad / stick", Action: "As DOSBox Pure set up this game"},
			{Button: "A B X Y", Action: "Keys DOSBox Pure picked"},
			{Button: "Controller Mapper", Action: "In the Select menu: see or change every button"},
		},
		Source: "dos-shelf",
	})
	// The same keys in every game: DOSBox Pure's generic keyboard keys on
	// every button instead of its per-game mapping. Not a key table of
	// JukaHub's own: the layouts' Keys are not sent to the core yet
	// (ctrllayout_apply.go), and turning the core's mapping off with nothing
	// in its place would leave the pad dead.
	ctrlLayoutRegister(ctrlLayout{
		ID:          "dos-keyboard",
		Name:        "DOS keyboard",
		Description: "DOSBox Pure's plain keyboard keys on every button, the same in every game.",
		Systems:     []string{"DOS"},
		Buttons:     map[string]string{"select": "l3"},
		CoreOptions: map[string]string{"dosbox_pure_auto_mapping": "false"},
		Chart: []ctrlLayoutLine{
			{Button: "Every button", Action: "A keyboard key, the same in every game"},
			{Button: "Select", Action: "On-screen keyboard and menu"},
			{Button: "Controller Mapper", Action: "In the Select menu: see or change the keys"},
		},
		Source: "dos-shelf",
	})
	// Point-and-click PC games. DOSBox Pure's mouse on the stick is one of its
	// own presets (picked in its menu, kept per game); the clicks are its
	// defaults for that preset, left on B and right on A, which also keeps
	// its menu's confirm and back where they are.
	ctrlLayoutRegister(ctrlLayout{
		ID:          "dos-mouse",
		Name:        "DOS mouse",
		Description: "The mouse on the left stick (DOSBox Pure's preset), clicks on B and A.",
		Systems:     []string{"DOS"},
		Buttons:     map[string]string{"select": "l3"},
		Chart: []ctrlLayoutLine{
			{Button: "Left stick", Action: "Mouse"},
			{Button: "Once per game", Action: "Select, Controller Mapper, preset: Mouse with Left Analog Stick"},
			{Button: "B", Action: "Left click"},
			{Button: "A", Action: "Right click"},
			{Button: "X", Action: "Middle click"},
			{Button: "L2 / R2", Action: "Slower / faster mouse"},
			{Button: "Select", Action: "On-screen keyboard and menu"},
		},
		Source: "dos-shelf",
	})
}

// shelfDOSListsFile is the DOS shelf rule (romshelf.go).
func shelfDOSListsFile(consoleDir, path, ext string) bool {
	gameDir, top := shelfGameDir(consoleDir, path, false)
	if top {
		return shelfDOSTopExts[ext]
	}
	if gameDir == "" || !shelfDOSTopExts[ext] {
		return false
	}
	start := shelfDOSStart(gameDir)
	return start != "" && shelfSamePath(start, path)
}

// shelfDOSStart is the file a game folder is listed (and started) by.
func shelfDOSStart(gameDir string) string {
	now := time.Now()
	shelfDOS.mu.Lock()
	f := shelfDOS.folders[gameDir]
	if f != nil && now.Sub(f.checked) < shelfTrust {
		start := f.start
		shelfDOS.mu.Unlock()
		return start
	}
	shelfDOS.mu.Unlock()
	info, err := os.Stat(gameDir)
	if err != nil {
		return ""
	}
	shelfDOS.mu.Lock()
	if f != nil && f.mod.Equal(info.ModTime()) {
		f.checked = now
		start := f.start
		shelfDOS.mu.Unlock()
		return start
	}
	shelfDOS.mu.Unlock()
	start := ""
	if rel := shelfDOSGet(gameDir).Start; rel != "" {
		if p := filepath.Join(gameDir, filepath.FromSlash(rel)); shelfDOSInside(gameDir, p) && regularFile(p) {
			start = p
		}
	}
	if start == "" {
		progs, others := shelfDOSPrograms(gameDir)
		if rel := shelfDOSGuess(filepath.Base(gameDir), progs, others); rel != "" {
			start = filepath.Join(gameDir, filepath.FromSlash(rel))
		}
	}
	shelfDOS.mu.Lock()
	if shelfDOS.folders == nil {
		shelfDOS.folders = map[string]*shelfDOSFolder{}
	}
	shelfDOS.folders[gameDir] = &shelfDOSFolder{mod: info.ModTime(), checked: now, start: start}
	shelfDOS.mu.Unlock()
	return start
}

// shelfDOSForget drops a folder's cached start, after its override changed.
func shelfDOSForget(gameDir string) {
	shelfDOS.mu.Lock()
	delete(shelfDOS.folders, gameDir)
	shelfDOS.mu.Unlock()
}

// shelfDOSInside reports whether p is in gameDir (an override from the file
// cannot point out of the game's folder).
func shelfDOSInside(gameDir, p string) bool {
	rel, err := filepath.Rel(gameDir, p)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// shelfDOSPrograms lists a game folder's programs (.exe, .com, .bat), from its
// subfolders too when it has none of its own, and its other loadable files
// (archives, images) straight in it. Rel paths use forward slashes.
func shelfDOSPrograms(gameDir string) (progs, others []shelfDOSProg) {
	read := func(dir, prefix string, subs *[]string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for i, e := range entries {
			if i >= shelfDOSReadMax {
				break
			}
			name := e.Name()
			if skipScanEntryName(name) {
				continue
			}
			if e.IsDir() {
				if subs != nil && !plugGameSkipDir[strings.ToLower(name)] {
					*subs = append(*subs, name)
				}
				continue
			}
			ext := strings.ToLower(filepath.Ext(name))
			if !shelfDOSTopExts[ext] {
				continue
			}
			var size int64
			if info, err := e.Info(); err == nil {
				size = info.Size()
			}
			p := shelfDOSProg{Rel: prefix + name, Size: size}
			switch {
			case shelfDOSIsProgram(ext):
				progs = append(progs, p)
			case subs != nil:
				others = append(others, p)
			}
		}
	}
	var subs []string
	read(gameDir, "", &subs)
	if len(progs) == 0 {
		for i, sub := range subs {
			if i >= shelfDOSSubMax {
				break
			}
			read(filepath.Join(gameDir, sub), sub+"/", nil)
		}
	}
	sort.Slice(progs, func(i, j int) bool { return strings.ToLower(progs[i].Rel) < strings.ToLower(progs[j].Rel) })
	return progs, others
}

func shelfDOSIsProgram(ext string) bool {
	return ext == ".exe" || ext == ".com" || ext == ".bat"
}

// shelfDOSNotStartName reports a program that is never the game's start.
func shelfDOSNotStartName(rel string) bool {
	base := strings.ToLower(filepath.Base(filepath.FromSlash(rel)))
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	for _, p := range shelfDOSNotStart {
		if strings.HasPrefix(stem, p) {
			return true
		}
	}
	return false
}

// shelfDOSFold keeps the letters and digits of s, lower case.
func shelfDOSFold(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// shelfDOSGuess picks a folder game's start among its programs (see the file
// comment), then among its other files; "" when there is nothing to start.
func shelfDOSGuess(folder string, progs, others []shelfDOSProg) string {
	// Programs straight in the folder come first; a subfolder's count only
	// when the folder has none (shelfDOSPrograms reads them only then).
	for _, script := range shelfDOSStartScripts {
		for _, p := range progs {
			if strings.EqualFold(filepath.Base(filepath.FromSlash(p.Rel)), script) {
				return p.Rel
			}
		}
	}
	var cands []shelfDOSProg
	for _, p := range progs {
		if !shelfDOSNotStartName(p.Rel) {
			cands = append(cands, p)
		}
	}
	if len(cands) == 1 {
		return cands[0].Rel
	}
	name := shelfDOSFold(folder)
	best, bestLen := "", 0
	for _, p := range cands {
		base := filepath.Base(filepath.FromSlash(p.Rel))
		stem := shelfDOSFold(strings.TrimSuffix(base, filepath.Ext(base)))
		if stem == "" {
			continue
		}
		if (stem == name || (len(stem) >= 3 && strings.HasPrefix(name, stem))) && len(stem) > bestLen {
			best, bestLen = p.Rel, len(stem)
		}
	}
	if best != "" {
		return best
	}
	// The largest program that is not a script: a game's .exe dwarfs its
	// helpers. A folder of scripts only starts with its first.
	var largest shelfDOSProg
	for _, p := range cands {
		if !strings.EqualFold(filepath.Ext(p.Rel), ".bat") && p.Size > largest.Size {
			largest = p
		}
	}
	if largest.Rel != "" {
		return largest.Rel
	}
	if len(cands) > 0 {
		return cands[0].Rel
	}
	if len(progs) > 0 {
		return ""
	}
	// No program: one archive, else the best disc image.
	var archives []shelfDOSProg
	for _, o := range others {
		if ext := strings.ToLower(filepath.Ext(o.Rel)); ext == ".zip" || ext == ".dosz" {
			archives = append(archives, o)
		}
	}
	if len(archives) == 1 {
		return archives[0].Rel
	}
	for _, want := range shelfDOSDiscOrder {
		for _, o := range others {
			if strings.EqualFold(filepath.Ext(o.Rel), want) {
				return o.Rel
			}
		}
	}
	return ""
}
