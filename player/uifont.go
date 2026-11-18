package main

// uifont.go — interface fonts you can download.
//
// JukaHub draws every label in the bundled Inter (home_fonts.go), and that
// stays the default: nothing here downloads a font until the owner picks one.
// Settings → Display → Font lists Inter, a short catalogue of free, legible
// faces and any font file already in the fonts folder. Picking a catalogue
// face that is not on the card downloads it (its regular cut, then its bold
// cut when it has one), checks that it really is a font with the letters the
// interface needs, installs it and applies it — one press, no second step.
// Picking an installed face applies it at once, and Left/Right on the row only
// step through installed faces, so a stray press never starts a download.
//
// Where they live: <data>/fonts (P().DataDir(): the card's data folder on the
// handheld, %APPDATA%\JukaHub on Windows). A .ttf or .otf copied there by hand
// shows up in the list as well ("From your card"), so a font can be added with
// no connection at all. Remove a font deletes a downloaded (or copied) face;
// removing the one in use goes back to Inter.
//
// Every catalogue face is a STATIC cut from google/fonts (OFL / Ubuntu Font
// Licence), fetched from raw.githubusercontent.com with jsDelivr's GitHub
// mirror as the second try; one Japanese face comes from notofonts/noto-cjk
// the same way. Variable fonts are left out on purpose: SDL_ttf draws only
// their default instance, which is often not the regular weight.
//
// The choice is the Custom key uiFont: empty (or "inter") for the bundled
// face, a catalogue id, or "file:<name>" for a copied file. It is read with
// the accessibility settings at launch (a11yLoad → uiFontLoad), resolved to a
// file path once per change (homeUIFontForRole asks on every font open, so
// the answer is cached), and a face whose file has gone falls back to Inter
// with one log line. The legible reading face (legiblefont.go) still wins
// over this choice: it is an accessibility setting.
//
// A face must also draw the interface language: most catalogue faces have no
// Cyrillic, and fonts.go would turn every Russian label into "…" — the way
// back to this row included. So a face is applied only when its character map
// covers the language's letters (uiFontLanguageGap), and a stored face that
// cannot (the language changed since) draws in Inter until it can.

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/veandco/go-sdl2/ttf"
)

const (
	// uiFontKey is the Custom key holding the choice.
	uiFontKey = "uiFont"
	// uiFontDefaultValue names the bundled Inter in the rows' option lists.
	uiFontDefaultValue = "inter"
	// uiFontFilePrefix marks a face copied into the fonts folder by hand.
	uiFontFilePrefix = "file:"
	// uiFontGetPrefix marks a sheet entry that downloads its face: only the
	// full list carries it, so Left/Right (which step through installed faces
	// by their plain ids) can never start a download.
	uiFontGetPrefix = "get:"
	// uiFontMaxBytes caps one downloaded file; the largest catalogue face (the
	// Japanese one) is about 4.5 MB.
	uiFontMaxBytes = 20 << 20
	// uiFontTimeout bounds a whole install, every cut and mirror included.
	uiFontTimeout = 6 * time.Minute
)

// The two repositories the catalogue's files come from.
const (
	uiFontRepoGoogle  = "google/fonts"
	uiFontRepoNotoCJK = "notofonts/noto-cjk"
)

// uiFontEntry is one catalogue face: its files inside the repository, the
// approximate download for both cuts, and a size scale for faces whose letters
// are much wider or narrower than Inter's at the same pixel size (a pixel font
// drawn at Inter's sizes would overflow every row).
type uiFontEntry struct {
	ID       string
	Name     string
	Note     string
	Repo     string
	Regular  string
	Bold     string
	KB       int
	Scale    float64
	Cyrillic bool // has Russian letters (the sheet says so in Russian)
}

// uiFontCatalog is the list Settings offers, in the order it shows them:
// reading faces first, then the playful ones, then the Japanese face (large).
var uiFontCatalog = []uiFontEntry{
	{ID: "atkinson", Name: "Atkinson Hyperlegible", Note: "Drawn for low vision: every letter distinct",
		Repo: uiFontRepoGoogle, Regular: "ofl/atkinsonhyperlegible/AtkinsonHyperlegible-Regular.ttf",
		Bold: "ofl/atkinsonhyperlegible/AtkinsonHyperlegible-Bold.ttf", KB: 110},
	{ID: "lato", Name: "Lato", Note: "Warm and calm",
		Repo: uiFontRepoGoogle, Regular: "ofl/lato/Lato-Regular.ttf", Bold: "ofl/lato/Lato-Bold.ttf", KB: 1300},
	{ID: "firasans", Name: "Fira Sans", Note: "Clear on small screens",
		Repo: uiFontRepoGoogle, Regular: "ofl/firasans/FiraSans-Regular.ttf",
		Bold: "ofl/firasans/FiraSans-SemiBold.ttf", KB: 900, Cyrillic: true},
	{ID: "ubuntu", Name: "Ubuntu", Note: "Friendly and modern",
		Repo: uiFontRepoGoogle, Regular: "ufl/ubuntu/Ubuntu-Regular.ttf", Bold: "ufl/ubuntu/Ubuntu-Bold.ttf", KB: 680,
		Cyrillic: true},
	{ID: "poppins", Name: "Poppins", Note: "Round and geometric",
		Repo: uiFontRepoGoogle, Regular: "ofl/poppins/Poppins-Regular.ttf",
		Bold: "ofl/poppins/Poppins-SemiBold.ttf", KB: 310},
	{ID: "kanit", Name: "Kanit", Note: "Sporty, with Thai letters too",
		Repo: uiFontRepoGoogle, Regular: "ofl/kanit/Kanit-Regular.ttf", Bold: "ofl/kanit/Kanit-SemiBold.ttf", KB: 300},
	{ID: "comicneue", Name: "Comic Neue", Note: "Playful, hand-drawn feel",
		Repo: uiFontRepoGoogle, Regular: "ofl/comicneue/ComicNeue-Regular.ttf",
		Bold: "ofl/comicneue/ComicNeue-Bold.ttf", KB: 90},
	{ID: "sharetechmono", Name: "Share Tech Mono", Note: "Techy monospace",
		Repo: uiFontRepoGoogle, Regular: "ofl/sharetechmono/ShareTechMono-Regular.ttf", KB: 45},
	{ID: "vt323", Name: "VT323", Note: "An old terminal screen",
		Repo: uiFontRepoGoogle, Regular: "ofl/vt323/VT323-Regular.ttf", KB: 150, Scale: 1.15},
	{ID: "silkscreen", Name: "Silkscreen", Note: "Tiny pixel letters",
		Repo: uiFontRepoGoogle, Regular: "ofl/silkscreen/Silkscreen-Regular.ttf",
		Bold: "ofl/silkscreen/Silkscreen-Bold.ttf", KB: 60, Scale: 0.75},
	{ID: "pressstart", Name: "Press Start 2P", Note: "8-bit arcade pixels",
		Repo: uiFontRepoGoogle, Regular: "ofl/pressstart2p/PressStart2P-Regular.ttf", KB: 115, Scale: 0.6},
	{ID: "notosansjp", Name: "Noto Sans JP", Note: "Shows Japanese and kanji game names · large",
		Repo: uiFontRepoNotoCJK, Regular: "Sans/SubsetOTF/JP/NotoSansJP-Regular.otf", KB: 4500, Cyrillic: true},
}

// uiFontEntryFor is the catalogue face with id.
func uiFontEntryFor(id string) (uiFontEntry, bool) {
	for _, e := range uiFontCatalog {
		if e.ID == id {
			return e, true
		}
	}
	return uiFontEntry{}, false
}

// uiFontMirrors are the addresses one repository file is tried at, in order:
// GitHub's raw host, then jsDelivr's mirror of the same repository. noto-cjk
// is pinned to its 2.004 release tag, google/fonts follows main.
func uiFontMirrors(repo, path string) []string {
	ref := "main"
	if repo == uiFontRepoNotoCJK {
		ref = "Sans2.004"
	}
	return []string{
		"https://raw.githubusercontent.com/" + repo + "/" + ref + "/" + path,
		"https://cdn.jsdelivr.net/gh/" + repo + "@" + ref + "/" + path,
	}
}

// uiFontExt is a file's font extension, lower case, or "" when it is not a
// .ttf or .otf.
func uiFontExt(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if ext == ".ttf" || ext == ".otf" {
		return ext
	}
	return ""
}

// uiFontInstallName is the file a catalogue cut is saved as: "<id>-Regular.ttf"
// or "<id>-Bold.otf". The fixed names keep the downloads apart from files the
// owner copied in.
func uiFontInstallName(e uiFontEntry, bold bool) string {
	src := e.Regular
	cut := "-Regular"
	if bold {
		src = e.Bold
		cut = "-Bold"
	}
	ext := uiFontExt(src)
	if ext == "" {
		ext = ".ttf"
	}
	return e.ID + cut + ext
}

// ── The folder ─────────────────────────────────────────────────────────────

var uiFontDirCache struct {
	sync.Mutex
	dir string
}

// uiFontDir is where downloaded and copied fonts live. Cached: DataDir creates
// its folder on every call.
func uiFontDir() string {
	uiFontDirCache.Lock()
	defer uiFontDirCache.Unlock()
	if uiFontDirCache.dir == "" {
		dir := filepath.Join("data", "fonts")
		if d, err := P().DataDir(); err == nil && strings.TrimSpace(d) != "" {
			dir = filepath.Join(d, "fonts")
		}
		uiFontDirCache.dir = dir
	}
	return uiFontDirCache.dir
}

// uiFontFileExists reports whether path is a non-empty regular file.
func uiFontFileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular() && st.Size() > 0
}

// uiFontCardFile is one face copied into the folder by hand: its file and, if
// a sibling such as "Foo-Bold.ttf" sits beside "Foo-Regular.ttf", the bold cut.
type uiFontCardFile struct {
	Name string // the file name, the choice's "file:" part
	Path string
	Bold string
	Size int64
}

// uiFontCompanionCuts are name endings that mark a weight or slant of another
// face rather than a face of its own.
var uiFontCompanionCuts = []string{"-bold", "-semibold", "-medium", "-light", "-thin", "-black",
	"-extrabold", "-extralight", "-italic", "-bolditalic", "-semibolditalic", "-lightitalic"}

// uiFontDirFile is one entry of the fonts folder: read once per listing, so
// the rows (re-read about once a second) never stat the card themselves.
type uiFontDirFile struct {
	name string
	size int64
	dir  bool
}

// uiFontListCache keeps the folder listing for a few seconds: the Settings hub
// re-reads its rows about once a second, and the card is slow.
var uiFontListCache struct {
	sync.Mutex
	at    time.Time
	files []uiFontDirFile
}

func uiFontDirEntries() []uiFontDirFile {
	uiFontListCache.Lock()
	defer uiFontListCache.Unlock()
	if !uiFontListCache.at.IsZero() && time.Since(uiFontListCache.at) < 3*time.Second {
		return uiFontListCache.files
	}
	entries, _ := os.ReadDir(uiFontDir())
	files := make([]uiFontDirFile, 0, len(entries))
	for _, e := range entries {
		f := uiFontDirFile{name: e.Name(), dir: e.IsDir()}
		if !f.dir {
			if info, err := e.Info(); err == nil {
				f.size = info.Size()
			}
		}
		files = append(files, f)
	}
	uiFontListCache.files = files
	uiFontListCache.at = time.Now()
	return files
}

// uiFontListChanged drops the cached listing after an install or a removal.
func uiFontListChanged() {
	uiFontListCache.Lock()
	uiFontListCache.at = time.Time{}
	uiFontListCache.Unlock()
}

// uiFontIsInstallName reports whether name is one of the catalogue's own
// download names, which the list shows under the catalogue face instead.
func uiFontIsInstallName(name string) bool {
	for _, e := range uiFontCatalog {
		if strings.EqualFold(name, uiFontInstallName(e, false)) ||
			(e.Bold != "" && strings.EqualFold(name, uiFontInstallName(e, true))) {
			return true
		}
	}
	return false
}

// uiFontCardFiles lists the faces copied into the folder, sorted by name. A
// weight or slant file is folded into its regular sibling when there is one.
func uiFontCardFiles() []uiFontCardFile {
	dir := uiFontDir()
	byLower := map[string]uiFontDirFile{}
	for _, e := range uiFontDirEntries() {
		if e.dir || e.size <= 0 || uiFontExt(e.name) == "" || uiFontIsInstallName(e.name) {
			continue
		}
		byLower[strings.ToLower(e.name)] = e
	}
	var out []uiFontCardFile
	for lower, e := range byLower {
		ext := uiFontExt(lower)
		stem := strings.TrimSuffix(lower, ext)
		companion := false
		for _, cut := range uiFontCompanionCuts {
			if base, ok := strings.CutSuffix(stem, cut); ok {
				if _, has := byLower[base+"-regular"+ext]; has {
					companion = true
				} else if _, has := byLower[base+ext]; has {
					companion = true
				}
				break
			}
		}
		if companion {
			continue
		}
		f := uiFontCardFile{Name: e.name, Path: filepath.Join(dir, e.name), Size: e.size}
		base := strings.TrimSuffix(stem, "-regular")
		for _, cut := range []string{"-bold", "-semibold"} {
			if b, ok := byLower[base+cut+ext]; ok {
				f.Bold = filepath.Join(dir, b.name)
				break
			}
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// uiFontCardFileFor finds a copied face by its file name.
func uiFontCardFileFor(name string) (uiFontCardFile, bool) {
	for _, f := range uiFontCardFiles() {
		if f.Name == name {
			return f, true
		}
	}
	return uiFontCardFile{}, false
}

// uiFontCardLabel is a copied face's name as the list shows it:
// "Foo-Regular.ttf" reads "Foo".
func uiFontCardLabel(name string) string {
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	if i := strings.LastIndex(strings.ToLower(stem), "-regular"); i > 0 && i == len(stem)-len("-regular") {
		stem = stem[:i]
	}
	stem = strings.NewReplacer("_", " ", "-", " ").Replace(stem)
	return strings.TrimSpace(stem)
}

// uiFontInstalled reports whether a catalogue face's regular cut is in the
// folder (as last listed), and the bytes its cuts take.
func uiFontInstalled(e uiFontEntry) (bool, int64) {
	reg := uiFontInstallName(e, false)
	bold := ""
	if e.Bold != "" {
		bold = uiFontInstallName(e, true)
	}
	var size int64
	ok := false
	for _, f := range uiFontDirEntries() {
		if f.dir || f.size <= 0 {
			continue
		}
		switch {
		case strings.EqualFold(f.name, reg):
			ok = true
			size += f.size
		case bold != "" && strings.EqualFold(f.name, bold):
			size += f.size
		}
	}
	return ok, size
}

// ── The choice ─────────────────────────────────────────────────────────────

// uiFontState is the stored choice and what it resolves to. Every font open
// asks (homeUIFontForRole), so the resolution is computed once per change.
var uiFontState struct {
	sync.Mutex
	choice   string
	resolved bool
	regular  string
	bold     string
	scale    float64
	logged   string // the missing choice already logged
	// lang is the interface language the resolution was made for; a change
	// resolves again (a face may not draw the new one).
	lang string
	// fallback says why a stored choice draws in Inter: "missing" (no file)
	// or "letters" (the face lacks the language's letters); "" otherwise.
	fallback string
	// gen counts applied choices, so a download that finishes after the owner
	// picked something else installs without taking over (uiFontStartInstall).
	gen int
}

// uiFontNormalize maps a stored or picked value to the choice: "" for Inter.
func uiFontNormalize(v string) string {
	v = strings.TrimSpace(v)
	if strings.EqualFold(v, uiFontDefaultValue) {
		return ""
	}
	return v
}

// uiFontLoad reads the stored choice; a11yLoad calls it before the first
// frame, so the interface opens in the chosen face.
func uiFontLoad(c *Config) {
	uiFontSweepParts()
	choice := ""
	if c != nil {
		if s, ok := customString(c.Variables.Custom, uiFontKey); ok {
			choice = uiFontNormalize(s)
		}
	}
	uiFontSetChoice(choice)
}

// uiFontSweepOnce runs the leftover-download sweep once per run.
var uiFontSweepOnce sync.Once

// uiFontSweepParts deletes the ".part" files a download killed mid-way (a
// crash, a power-off) left in the fonts folder. Once per run, at the first
// load, before any download can have started; a later config reload calls
// uiFontLoad again and must not delete a download in progress.
func uiFontSweepParts() {
	uiFontSweepOnce.Do(func() {
		if busy, _ := uiFontDownloadStatus(); busy != "" {
			return
		}
		dir := uiFontDir()
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".part") {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	})
}

// uiFontSetChoice records the choice and drops its resolution.
func uiFontSetChoice(choice string) {
	uiFontState.Lock()
	uiFontState.choice = choice
	uiFontState.resolved = false
	uiFontState.Unlock()
}

// uiFontChoice is the stored choice, "" for Inter.
func uiFontChoice() string {
	uiFontState.Lock()
	defer uiFontState.Unlock()
	return uiFontState.choice
}

// uiFontResolveLocked turns the choice into files. The caller holds the lock.
func uiFontResolveLocked() {
	if uiFontState.resolved {
		return
	}
	uiFontState.resolved = true
	uiFontState.lang = uiLanguageCode()
	uiFontState.regular, uiFontState.bold, uiFontState.scale = "", "", 1
	uiFontState.fallback = ""
	choice := uiFontState.choice
	if choice == "" {
		return
	}
	regular, bold, scale := uiFontChoiceFiles(choice)
	if regular == "" {
		uiFontState.fallback = "missing"
		if uiFontState.logged != choice {
			uiFontState.logged = choice
			logWarn("font", "the chosen font %q is not in %s; drawing in Inter", choice, uiFontDir())
		}
		return
	}
	if gap := uiFontLanguageGap(regular); gap != 0 {
		uiFontState.fallback = "letters"
		if logKey := choice + "\n" + uiFontState.lang; uiFontState.logged != logKey {
			uiFontState.logged = logKey
			logWarn("font", "the chosen font %q has no %q for %s; drawing in Inter", choice, gap, uiFontState.lang)
		}
		return
	}
	uiFontState.regular, uiFontState.bold, uiFontState.scale = regular, bold, scale
}

// uiFontChoiceFiles are a choice's files on the card — the regular cut ("" when
// it is not there), the bold cut if any — and its size scale.
func uiFontChoiceFiles(choice string) (regular, bold string, scale float64) {
	scale = 1
	if name, ok := strings.CutPrefix(choice, uiFontFilePrefix); ok {
		// Only a plain file name inside the folder: a hand-edited config must
		// not point the UI at an arbitrary path.
		if name == filepath.Base(name) && name != "." && name != ".." && uiFontExt(name) != "" {
			if f, found := uiFontCardFileFor(name); found && uiFontFileExists(f.Path) {
				return f.Path, f.Bold, scale
			}
		}
		return "", "", scale
	}
	e, ok := uiFontEntryFor(choice)
	if !ok {
		return "", "", scale
	}
	dir := uiFontDir()
	reg := filepath.Join(dir, uiFontInstallName(e, false))
	if !uiFontFileExists(reg) {
		return "", "", scale
	}
	if e.Bold != "" {
		if b := filepath.Join(dir, uiFontInstallName(e, true)); uiFontFileExists(b) {
			bold = b
		}
	}
	if e.Scale > 0 {
		scale = e.Scale
	}
	return reg, bold, scale
}

// uiFontLanguageSamples are letters each interface language needs beyond
// A–Z (the validator's floor). A face missing any of them would draw the
// labels with "…" in place of letters.
var uiFontLanguageSamples = map[string]string{
	"es":    "áéíóúñÁÉÍÓÚÑ¿¡",
	"pt-BR": "ãõçáéíóúâêôàÃÕÇ",
	"fr":    "éèêàâçîôûÉÀÇ",
	"de":    "äöüßÄÖÜ",
	"it":    "àèéìòùÀÈ",
	"ru":    "АБВГДЕЖЗИЙКЛМНОПРСТУФХЦЧШЩЪЫЬЭЮЯабвгдежзийклмнопрстуфхцчшщъыьэюяЁё",
}

// uiFontLanguageGap is the first letter the interface language needs that the
// face at path cannot draw, or 0 when it draws them all. The sample is the
// language's own letters plus a few translated labels. A character map that
// cannot be read counts as covering everything, as it does in fonts.go.
func uiFontLanguageGap(path string) rune {
	code := uiLanguageCode()
	if code == uiLanguageDefault {
		return 0
	}
	sample := uiFontLanguageSamples[code] + uiT("Settings") + uiT("Font") + uiT("Language")
	ranges, ok := coverageFor(path)
	if !ok {
		return 0
	}
	for _, r := range sample {
		if unicode.IsLetter(r) && !uiFontRangesHave(ranges, r) {
			return r
		}
	}
	return 0
}

// uiFontRangesHave reports whether r falls in one of ranges. Pure.
func uiFontRangesHave(ranges []runeRange, r rune) bool {
	for _, rr := range ranges {
		if rr.lo <= r && r <= rr.hi {
			return true
		}
	}
	return false
}

// uiFontLanguageRefusal is the toast for a face that cannot draw the
// interface language, or "" when it can.
func uiFontLanguageRefusal(choice string) string {
	regular, _, _ := uiFontChoiceFiles(choice)
	if regular == "" || uiFontLanguageGap(regular) == 0 {
		return ""
	}
	return uiFontChoiceName(choice) + " has no letters for " + uiLanguageNative(uiLanguageCode())
}

// uiFontFile is the face homeUIFontForRole substitutes, or "" to keep the
// bundled Inter. Bold text uses the face's bold cut when it has one.
func uiFontFile() string {
	uiFontState.Lock()
	defer uiFontState.Unlock()
	if uiFontState.choice == "" {
		return ""
	}
	if uiFontState.resolved && uiFontState.lang != uiLanguageCode() {
		uiFontState.resolved = false
	}
	uiFontResolveLocked()
	if a11y.boldText && uiFontState.bold != "" {
		return uiFontState.bold
	}
	return uiFontState.regular
}

// uiFontScaledSize is size for the chosen face's files (some faces draw much
// wider or narrower than Inter at one pixel size), and size unchanged for any
// other file.
func uiFontScaledSize(path string, size int) int {
	uiFontState.Lock()
	defer uiFontState.Unlock()
	if uiFontState.choice == "" || !uiFontState.resolved || path == "" {
		return size
	}
	if path != uiFontState.regular && path != uiFontState.bold {
		return size
	}
	scale := uiFontState.scale
	if scale <= 0 || scale == 1 {
		return size
	}
	scaled := int(float64(size)*scale + 0.5)
	if scaled < 8 {
		// The legibility floor: below 8 px no face is readable.
		scaled = 8
	}
	return scaled
}

// uiFontChoiceName names the face the choice means, for the row and toasts.
func uiFontChoiceName(choice string) string {
	choice = uiFontNormalize(choice)
	if choice == "" {
		return "Inter"
	}
	if name, ok := strings.CutPrefix(choice, uiFontFilePrefix); ok {
		return uiFontCardLabel(name)
	}
	if e, ok := uiFontEntryFor(choice); ok {
		return e.Name
	}
	return choice
}

// uiFontFallbackNote is the Font row's read-out when the stored face is not
// what the screen draws: "Inter (Lato missing)", or "" when it is.
func uiFontFallbackNote() string {
	uiFontState.Lock()
	if uiFontState.choice != "" && uiFontState.resolved && uiFontState.lang != uiLanguageCode() {
		uiFontState.resolved = false
	}
	uiFontResolveLocked()
	choice, why := uiFontState.choice, uiFontState.fallback
	uiFontState.Unlock()
	switch {
	case choice == "" || why == "":
		return ""
	case why == "letters":
		return "Inter (" + uiFontChoiceName(choice) + " lacks these letters)"
	}
	return "Inter (" + uiFontChoiceName(choice) + " missing)"
}

// uiFontGen is the count of applied choices (see uiFontState.gen).
func uiFontGen() int {
	uiFontState.Lock()
	defer uiFontState.Unlock()
	return uiFontState.gen
}

// uiFontChoiceInstalled reports whether a choice can be applied without a
// download.
func uiFontChoiceInstalled(choice string) bool {
	choice = uiFontNormalize(choice)
	if choice == "" {
		return true
	}
	if name, ok := strings.CutPrefix(choice, uiFontFilePrefix); ok {
		_, found := uiFontCardFileFor(name)
		return found
	}
	if e, ok := uiFontEntryFor(choice); ok {
		installed, _ := uiFontInstalled(e)
		return installed
	}
	return false
}

// uiFontApply stores and shows a choice. Main thread: it drops every
// font-dependent cache (a11yRefreshFonts).
func uiFontApply(c *Config, choice string) {
	choice = uiFontNormalize(choice)
	if c != nil {
		if c.Variables.Custom == nil {
			c.Variables.Custom = map[string]interface{}{}
		}
		if choice == "" {
			delete(c.Variables.Custom, uiFontKey)
		} else {
			c.Variables.Custom[uiFontKey] = choice
		}
		saveUserConfigDebounced(userConfigFromVariables(c))
	}
	uiFontState.Lock()
	uiFontState.logged = ""
	uiFontState.gen++
	uiFontState.Unlock()
	uiFontSetChoice(choice)
	// A file replaced on the card under the same name is read again, not
	// drawn from the bytes kept from before (home_fonts.go).
	if regular, bold, _ := uiFontChoiceFiles(choice); regular != "" {
		homeUIFontSourceRefresh(regular)
		if bold != "" {
			homeUIFontSourceRefresh(bold)
		}
	}
	a11yRefreshFonts()
	// The italic reader faces are cached apart and a11yRefreshFonts keeps
	// them; they must follow the new face too.
	fontStyleCache = make(map[string]*ttf.Font)
	if c != nil && setIsHubScene(setHubSceneName(c)) {
		setHubRefresh(c)
	}
}

// uiFontChoose is what a pick in the Font row does: apply an installed face,
// or download, install and then apply one that is not on the card yet.
//
// Only a "get:" value (the full list's entry for a face not on the card)
// downloads; a plain id that is not installed — a listing a few seconds old,
// a file deleted meanwhile — only says so, so Left/Right never download.
func uiFontChoose(c *Config, value string) {
	id, get := strings.CutPrefix(strings.TrimSpace(value), uiFontGetPrefix)
	choice := uiFontNormalize(id)
	if uiFontChoiceInstalled(choice) {
		if choice != "" {
			if msg := uiFontLanguageRefusal(choice); msg != "" {
				ShowToast(msg, ToastKindWarning)
				return
			}
		}
		if choice == uiFontChoice() {
			setToast("Font: " + uiFontChoiceName(choice))
			return
		}
		uiFontApply(c, choice)
		setToast("Font: " + uiFontChoiceName(choice))
		return
	}
	e, ok := uiFontEntryFor(choice)
	if !ok || !get {
		ShowToast("That font is no longer in the fonts folder", ToastKindWarning)
		return
	}
	uiFontStartInstall(c, e)
}

// uiFontRemove deletes a downloaded or copied face; removing the face in use
// goes back to Inter. Main thread.
func uiFontRemove(c *Config, value string) {
	choice := uiFontNormalize(value)
	if choice == "" {
		return
	}
	if busy, _ := uiFontDownloadStatus(); busy == choice {
		ShowToast("Wait for the download to finish", ToastKindInfo)
		return
	}
	var paths []string
	if name, ok := strings.CutPrefix(choice, uiFontFilePrefix); ok {
		f, found := uiFontCardFileFor(name)
		if !found {
			return
		}
		paths = append(paths, f.Path)
		if f.Bold != "" {
			paths = append(paths, f.Bold)
		}
	} else if e, ok := uiFontEntryFor(choice); ok {
		paths = append(paths, filepath.Join(uiFontDir(), uiFontInstallName(e, false)))
		if e.Bold != "" {
			paths = append(paths, filepath.Join(uiFontDir(), uiFontInstallName(e, true)))
		}
	} else {
		return
	}
	name := uiFontChoiceName(choice)
	var failed error
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			failed = err
		}
		uiFontForgetCoverage(p)
	}
	uiFontListChanged()
	inUse := choice == uiFontChoice()
	if inUse {
		// Even when a cut would not go: half a face is no face to draw in.
		uiFontApply(c, "")
	}
	if failed != nil {
		logWarn("font", "could not remove %s: %v", name, failed)
		ShowToast("Could not remove "+name+": "+firstLine(failed.Error()), ToastKindWarning)
		return
	}
	if inUse {
		setToast("Removed " + name + " · back to Inter")
		return
	}
	uiFontSetChoice(uiFontChoice())
	if c != nil && setIsHubScene(setHubSceneName(c)) {
		setHubRefresh(c)
	}
	setToast("Removed " + name)
}

// uiFontForgetCoverage drops the glyph table read for path (fonts.go) and the
// bytes kept for it (home_fonts.go), so a file installed again under the same
// name is read afresh.
func uiFontForgetCoverage(path string) {
	coverageMu.Lock()
	delete(coverageCache, path)
	coverageMu.Unlock()
	homeUIFontSourceRetire(path)
}

// ── Downloading ────────────────────────────────────────────────────────────

// uiFontDL is the one install that may run at a time.
var uiFontDL struct {
	sync.Mutex
	id      string // the face being installed, "" when idle
	percent int
}

// uiFontDownloadStatus is the face being installed ("" when none) and how far
// along it is, 0..100.
func uiFontDownloadStatus() (string, int) {
	uiFontDL.Lock()
	defer uiFontDL.Unlock()
	return uiFontDL.id, uiFontDL.percent
}

func uiFontSetPercent(p int) {
	uiFontDL.Lock()
	uiFontDL.percent = p
	uiFontDL.Unlock()
}

// uiFontStartInstall downloads e on a goroutine; the result is applied on the
// main loop (tbxPost). Main thread.
func uiFontStartInstall(c *Config, e uiFontEntry) {
	uiFontDL.Lock()
	if uiFontDL.id != "" {
		busy := uiFontDL.id
		uiFontDL.Unlock()
		ShowToast("Already downloading "+uiFontChoiceName(busy), ToastKindInfo)
		return
	}
	uiFontDL.id, uiFontDL.percent = e.ID, 0
	uiFontDL.Unlock()
	showToastPrimitive("Downloading "+e.Name+"…", ToastKindInfo)
	gen := uiFontGen()
	go func() {
		err := uiFontInstall(e)
		uiFontDL.Lock()
		uiFontDL.id, uiFontDL.percent = "", 0
		uiFontDL.Unlock()
		uiFontListChanged()
		tbxPost(func() {
			if err != nil {
				logWarn("font", "installing %s failed: %v", e.Name, err)
				ShowToast("Could not install "+e.Name+": "+uiFontProblem(err), ToastKindWarning)
				if c != nil && setIsHubScene(setHubSceneName(c)) {
					setHubRefresh(c)
				}
				return
			}
			logInfo("font", "installed %s in %s", e.Name, uiFontDir())
			refresh := func() {
				if c != nil && setIsHubScene(setHubSceneName(c)) {
					setHubRefresh(c)
				}
			}
			if uiFontGen() != gen {
				// Another face was picked while this one downloaded: it is
				// installed, the later choice stands.
				ShowToast(e.Name+" installed", ToastKindSuccess)
				refresh()
				return
			}
			if msg := uiFontLanguageRefusal(e.ID); msg != "" {
				ShowToast(msg, ToastKindWarning)
				refresh()
				return
			}
			uiFontApply(c, e.ID)
			ShowToast("Font: "+e.Name, ToastKindSuccess)
		})
	}()
}

// uiFontInstall fetches e's cuts into the folder. The regular cut must arrive;
// a bold cut that fails is logged and skipped (Bold text then uses the regular
// cut). Runs on its own goroutine.
func uiFontInstall(e uiFontEntry) error {
	dir := uiFontDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), uiFontTimeout)
	defer cancel()
	client := uiFontHTTPClient()
	// The regular cut is most of the progress when there is a bold one.
	regShare := 100
	if e.Bold != "" {
		regShare = 55
	}
	if err := uiFontFetchCut(ctx, client, e, false, 0, regShare); err != nil {
		return err
	}
	if e.Bold != "" {
		if err := uiFontFetchCut(ctx, client, e, true, regShare, 100-regShare); err != nil {
			logWarn("font", "the bold cut of %s did not install: %v", e.Name, err)
		}
	}
	return nil
}

// uiFontFetchCut downloads one cut, trying each mirror, validates it and moves
// it into place. Progress is reported as base + share*fraction.
func uiFontFetchCut(ctx context.Context, client *http.Client, e uiFontEntry, bold bool, base, share int) error {
	src := e.Regular
	if bold {
		src = e.Bold
	}
	dest := filepath.Join(uiFontDir(), uiFontInstallName(e, bold))
	var lastErr error
	for _, address := range uiFontMirrors(e.Repo, src) {
		if ctx.Err() != nil {
			break
		}
		err := uiFontDownloadTo(ctx, client, address, dest, e.Name, base, share)
		if err == nil {
			uiFontForgetCoverage(dest)
			return nil
		}
		logInfo("font", "%s from %s: %v", e.Name, address, err)
		// The first mirror's answer is the one worth showing: jsDelivr refuses
		// very large repositories, so its error would hide a real 404 or a
		// missing connection behind a 403.
		if lastErr == nil {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = ctx.Err()
	}
	return lastErr
}

// uiFontDownloadTo writes one address to dest through dest.part, checks the
// file and renames it into place. A failed or invalid file is removed.
func uiFontDownloadTo(ctx context.Context, client *http.Client, address, dest, name string, base, share int) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "JukaHub-Fonts/"+versionString(nil))
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return uiFontHTTPError(resp.StatusCode)
	}
	if resp.ContentLength > uiFontMaxBytes {
		return errors.New("the file is too large to be a font")
	}
	part := dest + ".part"
	out, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if out != nil {
			out.Close()
		}
		if !done {
			_ = os.Remove(part)
		}
	}()
	total := resp.ContentLength
	var got int64
	lastShown := -1
	lastToast := time.Time{}
	buf := make([]byte, 32<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			got += int64(n)
			if got > uiFontMaxBytes {
				return errors.New("the file is too large to be a font")
			}
			if _, werr := out.Write(buf[:n]); werr != nil {
				return werr
			}
			if total > 0 {
				pct := base + int(int64(share)*got/total)
				if pct > 99 {
					pct = 99
				}
				uiFontSetPercent(pct)
				// A toast every tenth, at most twice a second, outside the
				// notification history (the result toast is filed there).
				if pct/10 != lastShown/10 && time.Since(lastToast) >= 500*time.Millisecond {
					lastShown, lastToast = pct, time.Now()
					showToastPrimitive("Downloading "+name+"… "+strconv.Itoa(pct)+"%", ToastKindInfo)
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if total > 0 && got != total {
		return fmt.Errorf("the download stopped at %d of %d bytes", got, total)
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		out = nil
		return err
	}
	out = nil
	if err := uiFontValidateFile(part); err != nil {
		return err
	}
	// os.Rename replaces an existing file on Windows too (MoveFileEx with
	// MOVEFILE_REPLACE_EXISTING), so the good file stays until the new one is
	// in place.
	if err := os.Rename(part, dest); err != nil {
		return err
	}
	done = true
	return nil
}

// uiFontHTTPError words a failed answer.
func uiFontHTTPError(code int) error {
	if code == http.StatusNotFound {
		return errors.New("the font server no longer has this file (404)")
	}
	return fmt.Errorf("the font server answered %d", code)
}

// uiFontProblem is a download error as a toast says it.
func uiFontProblem(err error) string {
	if err == nil {
		return ""
	}
	var dnsErr *net.DNSError
	var opErr *net.OpError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "the download took too long"
	case errors.As(err, &dnsErr), errors.As(err, &opErr):
		return "no internet connection"
	case strings.Contains(strings.ToLower(err.Error()), "x509"),
		strings.Contains(strings.ToLower(err.Error()), "certificate"):
		return "the secure connection could not be checked"
	}
	return firstLine(err.Error())
}

var uiFontHTTP struct {
	once   sync.Once
	client *http.Client
}

// uiFontHTTPClient talks HTTPS only, to the font hosts only, with the
// firmware CA fallback a handheld needs (bugrepRootCAs).
func uiFontHTTPClient() *http.Client {
	uiFontHTTP.once.Do(func() {
		tr := &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			MaxIdleConns:          2,
			IdleConnTimeout:       30 * time.Second,
		}
		if pool := bugrepRootCAs(); pool != nil {
			tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		}
		uiFontHTTP.client = &http.Client{Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if !uiFontAllowedURL(req.URL) {
				return fmt.Errorf("redirect to %s refused", req.URL.Host)
			}
			return nil
		}}
	})
	return uiFontHTTP.client
}

// uiFontAllowedHosts are the only hosts a font is fetched from, redirects
// included.
var uiFontAllowedHosts = map[string]bool{
	"raw.githubusercontent.com":     true,
	"objects.githubusercontent.com": true,
	"media.githubusercontent.com":   true,
	"github.com":                    true,
	"cdn.jsdelivr.net":              true,
	"fastly.jsdelivr.net":           true,
}

func uiFontAllowedURL(u *url.URL) bool {
	return u != nil && u.Scheme == "https" && u.User == nil && uiFontAllowedHosts[strings.ToLower(u.Hostname())]
}

// ── Checking a file ────────────────────────────────────────────────────────

// uiFontValidateFile accepts a TrueType or OpenType font whose character map
// covers the letters and digits every label uses. A web page, an error body or
// a WOFF file served by mistake is refused before it can become the UI face.
func uiFontValidateFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	var head [4]byte
	_, err = io.ReadFull(f, head[:])
	f.Close()
	if err != nil {
		return errors.New("the download is not a font file")
	}
	if !uiFontSfntMagic(head[:]) {
		return errors.New("the download is not a font file")
	}
	ranges, err := parseCMAP(path)
	if err != nil {
		return errors.New("the font file is damaged")
	}
	if missing := uiFontMissingBasics(ranges); missing != "" {
		return fmt.Errorf("the font has no %s", missing)
	}
	return nil
}

// uiFontSfntMagic reports whether head starts a TrueType (0x00010000, "true")
// or OpenType/CFF ("OTTO") file. Pure.
func uiFontSfntMagic(head []byte) bool {
	if len(head) < 4 {
		return false
	}
	switch string(head[:4]) {
	case "OTTO", "true":
		return true
	}
	return binary.BigEndian.Uint32(head[:4]) == 0x00010000
}

// uiFontMissingBasics names the first of A–Z, a–z, 0–9 the ranges lack, or ""
// when all are there. Pure.
func uiFontMissingBasics(ranges []runeRange) string {
	for _, set := range [][2]rune{{'A', 'Z'}, {'a', 'z'}, {'0', '9'}} {
		for r := set[0]; r <= set[1]; r++ {
			if !uiFontRangesHave(ranges, r) {
				return strconv.QuoteRune(r)
			}
		}
	}
	return ""
}
