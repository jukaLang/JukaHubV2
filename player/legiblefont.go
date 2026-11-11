package main

// legiblefont.go — A11Y-8: the legible reading face.
//
// The UI draws in bundled Inter. Some players read a low-vision face more
// easily, so the accessibility store can ask for one; the player then uses the
// best face the machine already has, resolved at runtime in a fixed order, and
// keeps the bundled Inter when none is installed. No font is downloaded and
// nothing is added to the binary: this is a face choice, not an asset.
//
// The chain is deterministic: the faces below in order, the regular cut first,
// then each directory in defaultLegibleFontDirs order (sorted subdirectories
// included). A face that exists but whose file cannot be opened still leaves
// the text drawn: homeUIFontSourceFor falls back to the bundled Inter.

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
// every platform.
type legibleFontFace struct {
	Name    string
	Regular []string
	Bold    []string
}

// legibleFontFaces is the whole chain, best first. It ends by returning to the
// bundled Inter (an empty resolution), so the setting always renders.
var legibleFontFaces = []legibleFontFace{
	{
		Name:    "Atkinson Hyperlegible",
		Regular: []string{"atkinsonhyperlegible-regular.ttf", "atkinsonhyperlegible-regular.otf"},
		Bold:    []string{"atkinsonhyperlegible-bold.ttf", "atkinsonhyperlegible-bold.otf"},
	},
	{
		Name:    "Lexend",
		Regular: []string{"lexend-regular.ttf", "lexenddeca-regular.ttf", "lexend.ttf"},
		Bold:    []string{"lexend-bold.ttf", "lexenddeca-bold.ttf"},
	},
	{Name: "Verdana", Regular: []string{"verdana.ttf"}, Bold: []string{"verdanab.ttf"}},
	{Name: "Tahoma", Regular: []string{"tahoma.ttf"}, Bold: []string{"tahomabd.ttf"}},
	{Name: "Segoe UI", Regular: []string{"segoeui.ttf"}, Bold: []string{"segoeuib.ttf"}},
}

// legibleFontDirs is where a system face can live, in search order. It is a
// variable so a test can point it at a folder it built.
var legibleFontDirs = defaultLegibleFontDirs

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
	Face    string
	Regular string
	Bold    string
}

var legibleFontCache struct {
	sync.Mutex
	resolved bool
	value    legibleFont
}

// legibleFontReset drops the scan, so the next frame sees a changed setting or
// a changed font folder. The scan never runs while the setting is off.
func legibleFontReset() {
	legibleFontCache.Lock()
	legibleFontCache.resolved = false
	legibleFontCache.value = legibleFont{}
	legibleFontCache.Unlock()
}

// currentLegibleFont resolves (and caches) the face the setting asks for. It is
// the zero value when the setting is off or no candidate is installed; callers
// then keep the bundled Inter.
func currentLegibleFont() legibleFont {
	if !a11y.legibleFont {
		return legibleFont{}
	}
	legibleFontCache.Lock()
	defer legibleFontCache.Unlock()
	if legibleFontCache.resolved {
		return legibleFontCache.value
	}
	legibleFontCache.value = resolveLegibleFont(legibleFontDirs())
	legibleFontCache.resolved = true
	return legibleFontCache.value
}

// resolveLegibleFont walks the chain over the given directories: each face's
// regular file names in order, then the next face, and finally nothing found.
func resolveLegibleFont(dirs []string) legibleFont {
	files := map[string]string{}
	seen := map[string]bool{}
	for _, dir := range dirs {
		for _, sub := range legibleCollectDir(dir, seen, files) {
			legibleCollectDir(sub, seen, files)
		}
	}
	for _, face := range legibleFontFaces {
		for _, name := range face.Regular {
			path, ok := files[strings.ToLower(name)]
			if !ok {
				continue
			}
			found := legibleFont{Face: face.Name, Regular: path}
			for _, bold := range face.Bold {
				if p, ok := files[strings.ToLower(bold)]; ok {
					found.Bold = p
					break
				}
			}
			return found
		}
	}
	return legibleFont{}
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
		files[strings.ToLower(e.Name())] = filepath.Join(dir, e.Name())
	}
	sort.Strings(subdirs)
	return subdirs
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
