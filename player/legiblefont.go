package main

// legiblefont.go — A11Y-8: the legible reading face.
//
// The UI draws in bundled Inter. Some players read a low-vision face more
// easily, so the accessibility store can ask for one; the player then uses the
// best readable face it can find, resolved at runtime in a fixed order, and
// keeps the bundled Inter when none is there. The faces come from two places:
// JukaHub's own fonts folder (uifont.go), where the setting downloads Atkinson
// Hyperlegible — or Andika, which has Cyrillic, for a Russian interface — by
// itself (legiblefont_get.go), and the machine's system font folders. Nothing
// is added to the binary.
//
// The chain is deterministic: the face picked in Settings → Accessibility →
// Readable font first (when it is there and can draw the language), then the
// faces below in order, the regular cut first. The folders are searched in
// legibleFontDirs order (sorted subdirectories included), and the first file
// of a name wins. A face that cannot draw the interface language's letters
// (uiFontLanguageGap) is skipped, so a Russian interface never gets a
// Latin-only face and "…" labels. A face that exists but whose file cannot be
// opened still leaves the text drawn: homeUIFontSourceFor falls back to the
// bundled Inter.

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// legibleFontFace is one candidate: the lower-case file names carrying its
// regular and bold cuts. Lower case because the lookup is case-insensitive on
// every platform. ID is the Readable font row's value; when it is also a
// uiFontCatalog id the face can be downloaded, and the name uifont.go saves
// it under is searched as well (legibleFaceNames).
type legibleFontFace struct {
	ID      string
	Name    string
	Regular []string
	Bold    []string
}

// legibleFontFaces is the whole chain, best first. It ends by returning to the
// bundled Inter (an empty resolution), so the setting always renders.
var legibleFontFaces = []legibleFontFace{
	{
		ID:      "atkinson",
		Name:    "Atkinson Hyperlegible",
		Regular: []string{"atkinsonhyperlegible-regular.ttf", "atkinsonhyperlegible-regular.otf"},
		Bold:    []string{"atkinsonhyperlegible-bold.ttf", "atkinsonhyperlegible-bold.otf"},
	},
	{
		// SIL's face for new readers, with Cyrillic: the readable face for a
		// Russian interface. Andika 6 names its files Andika-Regular/-Bold,
		// Andika 5 Andika-R/-B, the older reduced cut AndikaNewBasic-R/-B.
		ID:      "andika",
		Name:    "Andika",
		Regular: []string{"andika-regular.ttf", "andika-r.ttf", "andikanewbasic-r.ttf"},
		Bold:    []string{"andika-bold.ttf", "andika-b.ttf", "andikanewbasic-b.ttf"},
	},
	{
		ID:      "lexend",
		Name:    "Lexend",
		Regular: []string{"lexend-regular.ttf", "lexenddeca-regular.ttf", "lexend.ttf"},
		Bold:    []string{"lexend-bold.ttf", "lexenddeca-bold.ttf"},
	},
	{ID: "verdana", Name: "Verdana", Regular: []string{"verdana.ttf"}, Bold: []string{"verdanab.ttf"}},
	{ID: "tahoma", Name: "Tahoma", Regular: []string{"tahoma.ttf"}, Bold: []string{"tahomabd.ttf"}},
	{ID: "segoeui", Name: "Segoe UI", Regular: []string{"segoeui.ttf"}, Bold: []string{"segoeuib.ttf"}},
}

// legibleFontFaceFor is the chain face with id.
func legibleFontFaceFor(id string) (legibleFontFace, bool) {
	for _, f := range legibleFontFaces {
		if f.ID == id {
			return f, true
		}
	}
	return legibleFontFace{}, false
}

// legibleFaceNames are the file names that carry one cut of face: its own
// names, then the name uifont.go saves a downloaded cut under
// ("atkinson-Regular.ttf"), when the face is in the download catalogue.
func legibleFaceNames(face legibleFontFace, bold bool) []string {
	names := face.Regular
	if bold {
		names = face.Bold
	}
	e, ok := uiFontEntryFor(face.ID)
	if !ok || (bold && e.Bold == "") {
		return names
	}
	own := strings.ToLower(uiFontInstallName(e, bold))
	for _, n := range names {
		if n == own {
			return names
		}
	}
	// A copy: appending must never write into the table's backing array.
	return append(names[:len(names):len(names)], own)
}

// legibleFontDirs is where a face can live, in search order. It is a variable
// so a test can point it at a folder it built.
var legibleFontDirs = defaultLegibleFontDirs

// defaultLegibleFontDirs is JukaHub's fonts folder (where the setting's own
// downloads land), then the system font folders.
func defaultLegibleFontDirs() []string {
	var dirs []string
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		for _, d := range dirs {
			if strings.EqualFold(d, dir) {
				return
			}
		}
		dirs = append(dirs, dir)
	}
	add(uiFontDir())
	if runtime.GOOS == "windows" {
		windir := os.Getenv("WINDIR")
		if windir == "" {
			windir = `C:\Windows`
		}
		add(filepath.Join(windir, "Fonts"))
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			add(filepath.Join(local, "Microsoft", "Windows", "Fonts"))
		}
		return dirs
	}
	add("/usr/share/fonts")
	add("/usr/local/share/fonts")
	if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, ".fonts"))
		add(filepath.Join(home, ".local", "share", "fonts"))
	}
	return dirs
}

// legibleFont is one resolution: the face found and the files of its regular
// and bold cuts.
type legibleFont struct {
	ID      string
	Face    string
	Regular string
	Bold    string
}

// legibleFontCache keeps the folder index (scanned once per reset) and the
// resolution, which is made again when the interface language or the picked
// face changes: every font open asks (homeUIFontForRole), so both are cheap to
// read.
var legibleFontCache struct {
	sync.Mutex
	scanned  bool
	index    map[string]string // lower-case file name → path, first folder wins
	resolved bool
	lang     string // the interface language of the last resolution
	prefer   string // the picked face value was resolved for
	value    legibleFont
}

// legibleFontReset drops the scan, so the next frame sees a changed setting or
// a changed font folder. The scan never runs while the setting is off.
func legibleFontReset() {
	legibleFontCache.Lock()
	legibleFontCache.scanned = false
	legibleFontCache.index = nil
	legibleFontCache.resolved = false
	legibleFontCache.value = legibleFont{}
	legibleFontCache.Unlock()
}

// legibleFontIndexLocked is the folders' index, scanned on first use after a
// reset. The caller holds legibleFontCache.
func legibleFontIndexLocked() map[string]string {
	if !legibleFontCache.scanned {
		legibleFontCache.index = legibleScanDirs(legibleFontDirs())
		legibleFontCache.scanned = true
	}
	return legibleFontCache.index
}

// currentLegibleFont resolves (and caches) the face the setting asks for. It is
// the zero value when the setting is off or no candidate is there; callers
// then keep the bundled Inter.
func currentLegibleFont() legibleFont {
	if !a11y.legibleFont {
		return legibleFont{}
	}
	lang, prefer := uiLanguageCode(), a11y.legibleFace
	legibleFontCache.Lock()
	c := &legibleFontCache
	if c.resolved && c.lang == lang && c.prefer == prefer {
		v := c.value
		c.Unlock()
		return v
	}
	// lang outlives a reset, so a language change is noticed even when
	// something else dropped the resolution in between.
	relang := c.lang != "" && c.lang != lang
	c.value = resolveLegibleFont(legibleFontIndexLocked(), prefer)
	c.resolved, c.lang, c.prefer = true, lang, prefer
	v := c.value
	c.Unlock()
	if relang {
		// The interface language changed under the setting: the cached
		// fonts may hold a face that cannot draw it, and the new language
		// may want a readable face that is not here yet.
		legibleFontLanguageChanged()
	}
	return v
}

// resolveLegibleFont walks the chain over a folder index: the picked face
// first, then each face's regular file names in order, skipping a face that
// cannot draw the interface language; finally nothing found.
func resolveLegibleFont(files map[string]string, prefer string) legibleFont {
	faces := legibleFontFaces
	if p, ok := legibleFontFaceFor(prefer); ok {
		faces = append([]legibleFontFace{p}, legibleFontFaces...)
	}
	for _, face := range faces {
		path := legibleFirstFile(files, legibleFaceNames(face, false))
		if path == "" {
			continue
		}
		if gap := uiFontLanguageGap(path); gap != 0 {
			logDebug("font", "legible face %s has no %q for %s; trying the next", face.Name, gap, uiLanguageCode())
			continue
		}
		return legibleFont{ID: face.ID, Face: face.Name, Regular: path,
			Bold: legibleFirstFile(files, legibleFaceNames(face, true))}
	}
	return legibleFont{}
}

// legibleFirstFile is the path of the first of names in the index, or "".
func legibleFirstFile(files map[string]string, names []string) string {
	for _, name := range names {
		if path, ok := files[strings.ToLower(name)]; ok {
			return path
		}
	}
	return ""
}

// legibleScanDirs indexes the font files of dirs and of their direct
// subdirectories, in order; the first file of a name wins.
func legibleScanDirs(dirs []string) map[string]string {
	files := map[string]string{}
	seen := map[string]bool{}
	for _, dir := range dirs {
		for _, sub := range legibleCollectDir(dir, seen, files) {
			legibleCollectDir(sub, seen, files)
		}
	}
	return files
}

// legibleCollectDir indexes one directory's regular files into files, and
// returns its subdirectories sorted. It is a package function rather than a
// closure because the walk is recursive, and a self-referencing closure needs
// the `var f func(...); f = func(...)` split that staticcheck's S1021 reports.
func legibleCollectDir(dir string, seen map[string]bool, files map[string]string) []string {
	if dir == "" || seen[dir] {
		return nil
	}
	seen[dir] = true
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var subdirs []string
	for _, e := range entries {
		if e.IsDir() {
			subdirs = append(subdirs, filepath.Join(dir, e.Name()))
			continue
		}
		key := strings.ToLower(e.Name())
		if _, have := files[key]; !have {
			files[key] = filepath.Join(dir, e.Name())
		}
	}
	sort.Strings(subdirs)
	return subdirs
}

// legibleFontFacePath is the regular cut of the chain face id on this machine
// (JukaHub's fonts folder or a system folder), or "" when it is not there.
func legibleFontFacePath(id string) string {
	face, ok := legibleFontFaceFor(id)
	if !ok {
		return ""
	}
	legibleFontCache.Lock()
	defer legibleFontCache.Unlock()
	return legibleFirstFile(legibleFontIndexLocked(), legibleFaceNames(face, false))
}

// legibleFontInstalledFaces are the chain faces this machine has, in chain
// order, whatever language they draw.
func legibleFontInstalledFaces() []legibleFontFace {
	legibleFontCache.Lock()
	defer legibleFontCache.Unlock()
	files := legibleFontIndexLocked()
	var out []legibleFontFace
	for _, face := range legibleFontFaces {
		if legibleFirstFile(files, legibleFaceNames(face, false)) != "" {
			out = append(out, face)
		}
	}
	return out
}

// legibleFontFile is the face homeUIFontForRole substitutes when the setting is
// on, or "" to keep the bundled face. Bold text still bolds the legible face
// when that face has a bold cut.
func legibleFontFile() string {
	found := currentLegibleFont()
	if found.Regular == "" {
		return ""
	}
	if a11y.boldText && found.Bold != "" {
		return found.Bold
	}
	return found.Regular
}

// legibleFontFaceName names the resolved face for a toast or a settings row, or
// "" when the bundled Inter is what the screen is drawn in.
func legibleFontFaceName() string { return currentLegibleFont().Face }
