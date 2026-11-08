package main

// pkgcatalog.go — the Packages store's data model.
//
// The registry (packages.json in the jukaLang/Packages repository) is a flat
// list of apps: a name, an author, a description, a release date and a
// download link. The store screen needs more than that list — which shelves to
// show, which category a package belongs to, whether the copy on the card is
// older than the registry's, what a card's monogram tile looks like — and it
// needs all of it every frame. This file derives everything once per change,
// off the render path, so the screen only ever reads finished values:
//
//   - pkgParseCatalog decodes the registry one entry at a time (a malformed
//     entry no longer empties the whole store) and picks up the optional store
//     metadata a newer registry may carry: category, icon, size, changelog,
//     featured, homepage.
//   - pkgClassify derives a category from the name and description when the
//     registry names none.
//   - pkgBuildModel joins the catalog with what is on the card and produces
//     the shelves, the hero candidates and the filter chips.
//   - the catalog cache and the installed-version record live next to the
//     installed packages, so the store opens with data when it is offline.

import (
	"encoding/json"
	"errors"
	"hash/fnv"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/veandco/go-sdl2/sdl"
)

// pkgExtra is the optional store metadata a registry entry may carry on top of
// the fields Package declares. Every field is read leniently: a value of an
// unexpected type is ignored instead of failing the entry.
type pkgExtra struct {
	Category  string
	Icon      string
	Size      int64
	Changelog string
	Featured  bool
	Homepage  string
}

// pkgEntry is one registry entry with its extras.
type pkgEntry struct {
	Pkg   Package
	Extra pkgExtra
}

// pkgParseCatalog decodes the registry. Entries are decoded one at a time: a
// field of the wrong type zeroes that field, an entry that is not an object or
// has no name is skipped, and a duplicate name (two entries that would install
// into the same folder) keeps the first. The result is sorted by name.
func pkgParseCatalog(raw []byte) ([]pkgEntry, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	out := make([]pkgEntry, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		var p Package
		if err := json.Unmarshal(item, &p); err != nil {
			var typeErr *json.UnmarshalTypeError
			if !errors.As(err, &typeErr) {
				continue
			}
		}
		p.Name = strings.TrimSpace(p.Name)
		if p.Name == "" {
			continue
		}
		key := packageDirName(p.Name)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, pkgEntry{Pkg: p, Extra: pkgParseExtra(item)})
	}
	pkgSortEntries(out)
	return out, nil
}

// pkgSortEntries orders entries by name, case-insensitively.
func pkgSortEntries(entries []pkgEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Pkg.Name) < strings.ToLower(entries[j].Pkg.Name)
	})
}

// pkgParseExtra reads the optional store fields of one registry entry.
func pkgParseExtra(item json.RawMessage) pkgExtra {
	var fields map[string]json.RawMessage
	if json.Unmarshal(item, &fields) != nil {
		return pkgExtra{}
	}
	return pkgExtra{
		Category:  pkgRawString(fields, "category", "categories"),
		Icon:      pkgRawString(fields, "icon", "image", "artwork", "logo"),
		Size:      pkgRawSize(fields, "size", "download_size", "filesize"),
		Changelog: pkgRawText(fields, "changelog", "whats_new", "whatsnew", "release_notes"),
		Featured:  pkgRawBool(fields, "featured"),
		Homepage:  pkgRawString(fields, "homepage", "website", "repository", "repo"),
	}
}

// pkgRawString is the first key holding a string, or the first string of an
// array of strings.
func pkgRawString(fields map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		raw, ok := fields[k]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			if s = strings.TrimSpace(s); s != "" {
				return s
			}
			continue
		}
		var list []string
		if json.Unmarshal(raw, &list) == nil {
			for _, v := range list {
				if v = strings.TrimSpace(v); v != "" {
					return v
				}
			}
		}
	}
	return ""
}

// pkgRawText is pkgRawString for prose: an array of strings is one line each.
func pkgRawText(fields map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		raw, ok := fields[k]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			if s = strings.TrimSpace(s); s != "" {
				return s
			}
			continue
		}
		var list []string
		if json.Unmarshal(raw, &list) == nil {
			lines := make([]string, 0, len(list))
			for _, v := range list {
				if v = strings.TrimSpace(v); v != "" {
					lines = append(lines, "• "+v)
				}
			}
			if len(lines) > 0 {
				return strings.Join(lines, "\n")
			}
		}
	}
	return ""
}

// pkgRawBool reads true/false, "true"/"yes"/"1" or a non-zero number.
func pkgRawBool(fields map[string]json.RawMessage, key string) bool {
	raw, ok := fields[key]
	if !ok {
		return false
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true", "yes", "1":
			return true
		}
		return false
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		return n != 0
	}
	return false
}

// pkgRawSize reads a byte count given as a number, a numeric string or a
// human size ("1.2 MB").
func pkgRawSize(fields map[string]json.RawMessage, keys ...string) int64 {
	for _, k := range keys {
		raw, ok := fields[k]
		if !ok {
			continue
		}
		var n float64
		if json.Unmarshal(raw, &n) == nil {
			if n > 0 {
				return int64(n)
			}
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			if v := pkgParseSize(s); v > 0 {
				return v
			}
		}
	}
	return 0
}

// pkgParseSize turns "1234", "512 KB", "1.2MB" or "2 GiB" into bytes; anything
// else is 0 (unknown).
func pkgParseSize(s string) int64 {
	s = strings.ToLower(strings.TrimSpace(s))
	i := 0
	for i < len(s) && ((s[i] >= '0' && s[i] <= '9') || s[i] == '.') {
		i++
	}
	if i == 0 {
		return 0
	}
	v, err := strconv.ParseFloat(s[:i], 64)
	if err != nil || v <= 0 {
		return 0
	}
	mult := float64(1)
	switch strings.TrimSpace(s[i:]) {
	case "", "b", "byte", "bytes":
	case "k", "kb", "kib":
		mult = 1 << 10
	case "m", "mb", "mib":
		mult = 1 << 20
	case "g", "gb", "gib":
		mult = 1 << 30
	default:
		return 0
	}
	return int64(v * mult)
}

// ── Versions ─────────────────────────────────────────────────────────────

// pkgCompareVersions orders two version strings: 1 when a is newer, -1 when b
// is, 0 for the same release. Numeric parts compare as numbers ("1.10" is
// newer than "1.9"), a leading "v" is ignored, a missing part counts as zero
// ("2.0" equals "2.0.0") and a number outranks a tag ("1.0" > "1.0-beta").
func pkgCompareVersions(a, b string) int {
	pa, pb := pkgVersionParts(a), pkgVersionParts(b)
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		x, y := "0", "0"
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if c := pkgCompareVersionPart(x, y); c != 0 {
			return c
		}
	}
	return 0
}

func pkgVersionParts(v string) []string {
	v = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(v)), "v")
	return strings.FieldsFunc(v, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func pkgCompareVersionPart(x, y string) int {
	xi, xerr := strconv.ParseInt(x, 10, 64)
	yi, yerr := strconv.ParseInt(y, 10, 64)
	switch {
	case xerr == nil && yerr == nil:
		if xi > yi {
			return 1
		}
		if xi < yi {
			return -1
		}
		return 0
	case xerr == nil:
		return 1
	case yerr == nil:
		return -1
	}
	return strings.Compare(x, y)
}

// pkgVersionLabel is a version as cards print it ("v2.0").
func pkgVersionLabel(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if v[0] == 'v' || v[0] == 'V' {
		return v
	}
	return "v" + v
}

// ── Categories ───────────────────────────────────────────────────────────

// pkgCategory is one store shelf.
type pkgCategory struct {
	ID    string
	Title string
	Short string
	Icon  string
}

// pkgShelves is the order the category shelves appear in. Icons are names from
// the shared glyph set (DrawHomeIcon).
var pkgShelves = []pkgCategory{
	{ID: "games", Title: "Games & ROMs", Short: "Games", Icon: "gamepad"},
	{ID: "emulators", Title: "Emulators & cores", Short: "Emulators", Icon: "emulator"},
	{ID: "look", Title: "Look & feel", Short: "Look & feel", Icon: "palette"},
	{ID: "network", Title: "Network & sharing", Short: "Network", Icon: "network"},
	{ID: "media", Title: "Music & media", Short: "Media", Icon: "music"},
	{ID: "system", Title: "System tools", Short: "System", Icon: "tools"},
	{ID: "assets", Title: "Fonts & assets", Short: "Assets", Icon: "text"},
	{ID: "other", Title: "More apps", Short: "More", Icon: "apps"},
}

// pkgShelfByID finds a static shelf definition.
func pkgShelfByID(id string) (pkgCategory, bool) {
	for _, c := range pkgShelves {
		if c.ID == id {
			return c, true
		}
	}
	return pkgCategory{}, false
}

// pkgCategoryKeywords drives pkgClassify, in tie-break order. A trailing "*"
// matches any word with that prefix ("emu*" catches "EmuCleaner"); a keyword
// with a space matches that phrase.
var pkgCategoryKeywords = []struct {
	id       string
	keywords []string
}{
	{"games", []string{"rom", "roms", "game", "games", "gaming", "collection", "collections", "scraper", "scrape", "screenscraper", "box art", "boxart", "resume", "playlist", "random", "downloader", "ports", "portmaster", "arcade"}},
	{"emulators", []string{"emu*", "emulator", "emulators", "retroarch", "core", "cores", "ppsspp", "drastic", "bios", "shader", "shaders", "overlay", "overlays", "mupen", "pcsx", "duckstation", "flycast", "dolphin"}},
	{"look", []string{"theme*", "bgm", "boot logo", "bootlogo", "logo", "led", "leds", "icon", "icons", "wallpaper*", "background", "backgrounds", "rainbow", "color", "colors", "colour", "colours", "skin", "skins", "splash", "font", "fonts", "sound", "sounds"}},
	{"network", []string{"ftp", "dns", "wifi", "wi fi", "mac", "connectivity", "wlan", "network", "networking", "telnet*", "ssh", "sftp", "syncthing", "sync", "samba", "smb", "server", "vpn", "tailscale", "bluetooth", "mac address", "internet", "proxy", "remote"}},
	{"media", []string{"video", "videos", "movie", "movies", "player", "podcast*", "radio", "mp3", "audio", "photo", "photos", "image", "images", "camera", "youtube", "stream", "streaming", "ebook*", "reader", "comic", "comics", "music player"}},
	{"system", []string{"backup", "restore", "cpu", "governor", "reboot", "restart", "udisk", "disk", "storage", "partition", "format", "battery", "temperature", "system", "tool", "tools", "firmware", "config", "configuration", "clean", "cleaner", "performance", "usb", "swap", "memory", "overclock", "utility", "utilities", "update", "updater"}},
}

// pkgWords lower-cases text into its words.
func pkgWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// pkgFirstSentence is the description up to its first full stop: the sentence
// that says what a package is for.
func pkgFirstSentence(s string) string {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '.', '!', '?':
			if i+1 == len(s) || s[i+1] == ' ' || s[i+1] == '\n' {
				return s[:i]
			}
		}
	}
	return s
}

// pkgKeywordHit reports whether keyword k occurs in a word list (joined is the
// list as " w1 w2 … ", for phrases).
func pkgKeywordHit(k string, words []string, joined string) bool {
	if strings.HasSuffix(k, "*") {
		prefix := strings.TrimSuffix(k, "*")
		for _, w := range words {
			if strings.HasPrefix(w, prefix) {
				return true
			}
		}
		return false
	}
	return strings.Contains(joined, " "+k+" ")
}

// pkgClassify picks a shelf for a package from its name and description: a
// keyword in the name scores 4, in the first sentence 2, elsewhere in the
// description 1 (the description is capped at 5, so a backup tool that lists
// six emulators is still a backup tool). No hit at all is "other".
func pkgClassify(name, description string) string {
	nameWords := pkgWords(name)
	descWords := pkgWords(description)
	firstWords := pkgWords(pkgFirstSentence(description))
	nameJoined := " " + strings.Join(nameWords, " ") + " "
	descJoined := " " + strings.Join(descWords, " ") + " "
	firstJoined := " " + strings.Join(firstWords, " ") + " "

	best, bestScore := "other", 0
	for _, cat := range pkgCategoryKeywords {
		nameScore, descScore := 0, 0
		for _, k := range cat.keywords {
			if pkgKeywordHit(k, nameWords, nameJoined) {
				nameScore += 4
			}
			if pkgKeywordHit(k, firstWords, firstJoined) {
				descScore += 2
			} else if pkgKeywordHit(k, descWords, descJoined) {
				descScore++
			}
		}
		if descScore > 5 {
			descScore = 5
		}
		if score := nameScore + descScore; score > bestScore {
			best, bestScore = cat.id, score
		}
	}
	return best
}

// pkgCategoryAlias maps a registry's own category name onto a shelf, or "".
func pkgCategoryAlias(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "game", "games", "rom", "roms", "ports", "gaming":
		return "games"
	case "emulator", "emulators", "emulation", "core", "cores":
		return "emulators"
	case "theme", "themes", "look", "look & feel", "appearance", "customization", "customisation", "personalization":
		return "look"
	case "network", "networking", "internet", "sharing", "connectivity":
		return "network"
	case "media", "music", "video", "audio", "multimedia":
		return "media"
	case "system", "tool", "tools", "utility", "utilities", "system tools":
		return "system"
	case "font", "fonts", "asset", "assets", "resources":
		return "assets"
	case "other", "misc", "miscellaneous", "app", "apps":
		return "other"
	}
	return ""
}

// pkgCategoryFor is the shelf of one entry: the registry's own category when
// it names one (an unknown name becomes a shelf of its own), otherwise the
// classifier's pick. It returns the id, the shelf title and its glyph.
func pkgCategoryFor(e pkgEntry) (id, title, icon string) {
	if raw := strings.TrimSpace(e.Extra.Category); raw != "" {
		if alias := pkgCategoryAlias(raw); alias != "" {
			id = alias
		} else {
			return "x:" + strings.ToLower(raw), pkgTitleWords(raw), "apps"
		}
	} else {
		id = pkgClassify(e.Pkg.Name, e.Pkg.Description)
	}
	if c, ok := pkgShelfByID(id); ok {
		return c.ID, c.Title, c.Icon
	}
	return "other", "More apps", "apps"
}

// pkgTitleWords capitalises the first letter of every word ("trimui_brick" →
// "Trimui Brick").
func pkgTitleWords(s string) string {
	words := strings.FieldsFunc(s, func(r rune) bool { return r == '_' || r == '-' || r == ' ' })
	for i, w := range words {
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		words[i] = string(r)
	}
	return strings.Join(words, " ")
}

// ── Presentation helpers ─────────────────────────────────────────────────

// pkgTilePalette is the monogram tiles' colours: deep, saturated hues that
// carry white type at better than 4.5:1 on any theme.
var pkgTilePalette = []sdl.Color{
	{R: 30, G: 58, B: 138, A: 255},
	{R: 15, G: 118, B: 110, A: 255},
	{R: 154, G: 52, B: 18, A: 255},
	{R: 91, G: 33, B: 182, A: 255},
	{R: 157, G: 23, B: 77, A: 255},
	{R: 21, G: 101, B: 52, A: 255},
	{R: 3, G: 105, B: 161, A: 255},
	{R: 146, G: 64, B: 14, A: 255},
	{R: 67, G: 56, B: 202, A: 255},
	{R: 134, G: 25, B: 143, A: 255},
}

// pkgTileColor picks a package's tile colour from its name, so a package keeps
// its colour across refreshes and views.
func pkgTileColor(name string) sdl.Color {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(name)))
	return pkgTilePalette[int(h.Sum32()%uint32(len(pkgTilePalette)))]
}

// pkgAllUpper reports whether every letter in w is upper case (and there is
// at least one).
func pkgAllUpper(w []rune) bool {
	letters := 0
	for _, r := range w {
		if unicode.IsLetter(r) {
			letters++
			if !unicode.IsUpper(r) {
				return false
			}
		}
	}
	return letters > 0
}

// pkgInitials is the monogram a card shows when a package has no artwork:
// the first letters of the first two words ("Backup Config Files" → "BC"),
// the two halves of a CamelCase name ("EmuDrop" → "ED"), a short acronym whole
// ("FTP"), or a single initial.
func pkgInitials(name string) string {
	words := strings.FieldsFunc(name, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(words) == 0 {
		return "?"
	}
	if len(words) == 1 {
		w := []rune(words[0])
		if pkgAllUpper(w) {
			if len(w) <= 3 {
				return string(w)
			}
			return string(w[:2])
		}
		for i := 1; i < len(w); i++ {
			if unicode.IsUpper(w[i]) && unicode.IsLower(w[i-1]) {
				return string(unicode.ToUpper(w[0])) + string(w[i])
			}
		}
		return string(unicode.ToUpper(w[0]))
	}
	out := make([]rune, 0, 2)
	for _, word := range words {
		switch strings.ToLower(word) {
		case "a", "an", "at", "of", "the", "and", "for", "to", "in", "on":
			continue
		}
		out = append(out, unicode.ToUpper([]rune(word)[0]))
		if len(out) == 2 {
			break
		}
	}
	if len(out) == 0 {
		out = append(out, unicode.ToUpper([]rune(words[0])[0]))
	}
	return string(out)
}

// pkgNormalizeDevice folds a registry device name for comparison
// ("TrimUI-Smart Pro" → "trimui_smart_pro").
func pkgNormalizeDevice(dev string) string {
	d := strings.ToLower(strings.TrimSpace(dev))
	return strings.NewReplacer("-", "_", " ", "_").Replace(d)
}

// pkgDeviceLabel names a registry device for people.
func pkgDeviceLabel(dev string) string {
	switch pkgNormalizeDevice(dev) {
	case "trimui_smart_pro", "tg5040":
		return "TrimUI Smart Pro"
	case "trimui_smart_pro_s", "tg5050":
		return "TrimUI Smart Pro S"
	case "trimui_brick", "tg3040":
		return "TrimUI Brick"
	case "trimui_brick_pro", "tg4040":
		return "TrimUI Brick Pro"
	case "trimui_hammer":
		return "TrimUI Hammer"
	case "all", "any", "*":
		return "All devices"
	}
	if strings.TrimSpace(dev) == "" {
		return ""
	}
	return pkgTitleWords(dev)
}

// pkgDeviceFit says whether a package is made for the device in hand: 1 when
// the registry lists it (or "all"), -1 when it lists only other devices, 0
// when it lists none.
func pkgDeviceFit(devs []string, cur DeviceModel) int {
	if len(devs) == 0 {
		return 0
	}
	want := pkgNormalizeDevice(string(cur))
	code := strings.ToLower(cur.hardwareModelID())
	for _, d := range devs {
		n := pkgNormalizeDevice(d)
		if n == "all" || n == "any" || n == "*" || n == want || (code != "" && n == code) {
			return 1
		}
	}
	return -1
}

// pkgParseDate reads the registry's release date.
func pkgParseDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{"2006-01-02", time.RFC3339, "2006-01-02 15:04:05", "2006/01/02", "2006-01"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// pkgJoinDot joins the non-empty parts with the middle dot cards use.
func pkgJoinDot(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, " · ")
}

// pkgAgo is a short relative time ("just now", "5 min ago", "3 days ago").
func pkgAgo(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + " min ago"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + " h ago"
	case d < 48*time.Hour:
		return "yesterday"
	case d < 30*24*time.Hour:
		return strconv.Itoa(int(d/(24*time.Hour))) + " days ago"
	}
	return t.Format("Jan 2, 2006")
}

// ── Model ────────────────────────────────────────────────────────────────

// pkgItemKind separates registry packages from folders the registry no longer
// lists and from the JukaHub release the Updates shelf also offers.
type pkgItemKind int

const (
	pkgKindPackage pkgItemKind = iota
	pkgKindLocal
	pkgKindApp
)

// pkgAppItemKey is the model key of the JukaHub release card.
const pkgAppItemKey = "\x00jukahub-update"

// pkgAppInstallTool is the name the header updater gives the install card.
const pkgAppInstallTool = "JukaHub update"

// pkgItem is one package as every view draws it. All display strings are
// computed when the model is built.
type pkgItem struct {
	Kind             pkgItemKind
	Pkg              Package
	Extra            pkgExtra
	Key              string
	Category         string
	CategoryTitle    string
	CategoryIcon     string
	Installed        bool
	InstalledVersion string
	HasUpdate        bool
	Size             int64
	Date             time.Time
	HasDate          bool
	DeviceFit        int
	Initials         string
	Tile             sdl.Color
	Icon             string

	Meta     string
	SizeText string
	DateText string
	Devices  string
	Summary  string
	lower    string
}

// installTool is the name the install card carries while this item installs.
func (it *pkgItem) installTool() string {
	if it.Kind == pkgKindApp {
		return pkgAppInstallTool
	}
	return it.Pkg.Name
}

// pkgShelf is one horizontal row of the rows view. CountText is the count
// pill's label, made once instead of every frame.
type pkgShelf struct {
	ID        string
	Title     string
	Icon      string
	Items     []*pkgItem
	CountText string
}

// pkgChip is one filter chip of the grid and list views; Text is what the chip
// says ("Games  6").
type pkgChip struct {
	ID    string
	Label string
	Count int
	Text  string
}

// pkgModel is everything the screen draws, built by pkgBuildModel.
// UpdatesTitle and UpdateNames are the updates hero's headline and list.
type pkgModel struct {
	Items        []*pkgItem
	ByKey        map[string]*pkgItem
	App          *pkgItem
	Shelves      []pkgShelf
	Featured     []*pkgItem
	Chips        []pkgChip
	Updates      int
	Installed    int
	Available    int
	UpdatesTitle string
	UpdateNames  string
}

// pkgModelInput is what a model is built from.
type pkgModelInput struct {
	Entries   []pkgEntry
	Installed map[string]bool
	Meta      map[string]pkgInstalledMeta
	Sizes     map[string]int64
	Icons     map[string]string
	Art       map[string]string
	Device    DeviceModel
	// AppUpdate is the label of the JukaHub release the header advertises,
	// "" when there is none.
	AppUpdate string
}

// pkgNewItem derives one item's display values.
func pkgNewItem(kind pkgItemKind, e pkgEntry, key string, in pkgModelInput) *pkgItem {
	p := e.Pkg
	it := &pkgItem{Kind: kind, Pkg: p, Extra: e.Extra, Key: key}
	it.Installed = in.Installed[key]
	if meta, ok := in.Meta[key]; ok && it.Installed {
		it.InstalledVersion = meta.Version
	}
	if kind == pkgKindPackage && it.Installed && it.InstalledVersion != "" && p.Version != "" &&
		pkgCompareVersions(p.Version, it.InstalledVersion) > 0 {
		it.HasUpdate = true
	}
	it.Category, it.CategoryTitle, it.CategoryIcon = pkgCategoryFor(e)
	it.Size = e.Extra.Size
	if n := in.Sizes[key]; it.Installed && n > 0 {
		it.Size = n
	}
	it.Date, it.HasDate = pkgParseDate(p.Date)
	it.DeviceFit = pkgDeviceFit(p.Device, in.Device)
	it.Initials = pkgInitials(p.Name)
	it.Tile = pkgTileColor(p.Name)
	// The folder's own icon wins; a registry artwork URL, once downloaded,
	// stands in for packages that are not on the card.
	if it.Installed {
		it.Icon = in.Icons[key]
	}
	if it.Icon == "" {
		it.Icon = in.Art[key]
	}
	it.lower = strings.ToLower(p.Name)
	if it.Size > 0 {
		it.SizeText = formatInstallBytes(it.Size)
	}
	if it.HasDate {
		it.DateText = it.Date.Format("Jan 2, 2006")
	}
	second := it.SizeText
	if second == "" {
		second = strings.TrimSpace(p.Author)
	}
	it.Meta = pkgJoinDot(pkgVersionLabel(p.Version), second)
	devices := make([]string, 0, len(p.Device))
	for _, d := range p.Device {
		if label := pkgDeviceLabel(d); label != "" {
			devices = append(devices, label)
		}
	}
	it.Devices = strings.Join(devices, ", ")
	it.Summary = strings.TrimSpace(pkgFirstSentence(strings.TrimSpace(p.Description)))
	if it.Summary != "" && !strings.HasSuffix(it.Summary, ".") {
		it.Summary += "."
	}
	return it
}

// pkgBuildModel joins the catalog with the card. Folders on the card that the
// catalog does not list are kept as local items, so they can still be removed;
// the advertised JukaHub release becomes the first card of the Updates shelf.
func pkgBuildModel(in pkgModelInput) *pkgModel {
	m := &pkgModel{ByKey: make(map[string]*pkgItem, len(in.Entries)+len(in.Installed))}
	for _, e := range in.Entries {
		key := packageDirName(e.Pkg.Name)
		if _, dup := m.ByKey[key]; dup {
			continue
		}
		it := pkgNewItem(pkgKindPackage, e, key, in)
		m.Items = append(m.Items, it)
		m.ByKey[key] = it
	}
	m.Available = len(m.Items)

	local := make([]string, 0)
	for key, ok := range in.Installed {
		if !ok {
			continue
		}
		if _, listed := m.ByKey[key]; !listed {
			local = append(local, key)
		}
	}
	sort.Strings(local)
	for _, key := range local {
		e := pkgEntry{Pkg: Package{Name: strings.ReplaceAll(key, "_", " ")}}
		if meta, ok := in.Meta[key]; ok {
			if meta.Name != "" {
				e.Pkg.Name = meta.Name
			}
			e.Pkg.Version = meta.Version
			e.Pkg.Date = meta.Date
		}
		it := pkgNewItem(pkgKindLocal, e, key, in)
		it.Category, it.CategoryTitle, it.CategoryIcon = "other", "More apps", "apps"
		m.Items = append(m.Items, it)
		m.ByKey[key] = it
	}
	sort.SliceStable(m.Items, func(i, j int) bool { return m.Items[i].lower < m.Items[j].lower })

	if in.AppUpdate != "" {
		app := &pkgItem{
			Kind: pkgKindApp,
			Pkg: Package{
				Name:        "JukaHub",
				Author:      "JukaHub",
				Version:     in.AppUpdate,
				Description: "A new JukaHub release is ready. It downloads in the background, is verified, and JukaHub restarts into it when the update is in place.",
			},
			Key:           pkgAppItemKey,
			Category:      "system",
			CategoryTitle: "System tools",
			CategoryIcon:  "update",
			Installed:     true,
			HasUpdate:     true,
			Initials:      "JH",
			Tile:          sdl.Color{R: 8, G: 145, B: 178, A: 255},
			Meta:          "Release " + in.AppUpdate,
			Summary:       "A new JukaHub release is ready to install.",
			lower:         "jukahub",
		}
		m.App = app
		m.ByKey[app.Key] = app
	}

	var updates, installed, dated []*pkgItem
	if m.App != nil {
		updates = append(updates, m.App)
	}
	for _, it := range m.Items {
		if it.HasUpdate {
			updates = append(updates, it)
		}
		if it.Installed {
			installed = append(installed, it)
		}
		if it.HasDate && it.Kind == pkgKindPackage {
			dated = append(dated, it)
		}
	}
	m.Updates = len(updates)
	m.Installed = len(installed)
	sort.SliceStable(dated, func(i, j int) bool { return dated[i].Date.After(dated[j].Date) })

	if len(updates) > 0 {
		m.Shelves = append(m.Shelves, pkgShelf{ID: "updates", Title: "Updates available", Icon: "update", Items: updates})
	}
	if len(installed) > 0 {
		m.Shelves = append(m.Shelves, pkgShelf{ID: "installed", Title: "On this device", Icon: "check", Items: installed})
	}
	if len(dated) > 0 {
		top := dated
		if len(top) > 10 {
			top = top[:10]
		}
		m.Shelves = append(m.Shelves, pkgShelf{ID: "new", Title: "New & updated", Icon: "sparkles", Items: top})
	}

	byCategory := map[string][]*pkgItem{}
	titles := map[string]string{}
	for _, it := range m.Items {
		if it.Kind != pkgKindPackage {
			continue
		}
		byCategory[it.Category] = append(byCategory[it.Category], it)
		titles[it.Category] = it.CategoryTitle
	}
	all := len(m.Items)
	if m.App != nil {
		all++
	}
	m.Chips = append(m.Chips, pkgChip{ID: "all", Label: "All", Count: all})
	if m.Installed > 0 {
		m.Chips = append(m.Chips, pkgChip{ID: "installed", Label: "Installed", Count: m.Installed})
	}
	if m.Updates > 0 {
		m.Chips = append(m.Chips, pkgChip{ID: "updates", Label: "Updates", Count: m.Updates})
	}
	for _, c := range pkgShelves {
		items := byCategory[c.ID]
		if len(items) == 0 {
			continue
		}
		m.Shelves = append(m.Shelves, pkgShelf{ID: "cat:" + c.ID, Title: c.Title, Icon: c.Icon, Items: items})
		m.Chips = append(m.Chips, pkgChip{ID: "cat:" + c.ID, Label: c.Short, Count: len(items)})
	}
	extraIDs := make([]string, 0)
	for id := range byCategory {
		if strings.HasPrefix(id, "x:") {
			extraIDs = append(extraIDs, id)
		}
	}
	sort.Slice(extraIDs, func(i, j int) bool { return titles[extraIDs[i]] < titles[extraIDs[j]] })
	for _, id := range extraIDs {
		items := byCategory[id]
		m.Shelves = append(m.Shelves, pkgShelf{ID: "cat:" + id, Title: titles[id], Icon: "apps", Items: items})
		m.Chips = append(m.Chips, pkgChip{ID: "cat:" + id, Label: titles[id], Count: len(items)})
	}

	for i := range m.Shelves {
		m.Shelves[i].CountText = strconv.Itoa(len(m.Shelves[i].Items))
	}
	for i := range m.Chips {
		m.Chips[i].Text = m.Chips[i].Label + "  " + strconv.Itoa(m.Chips[i].Count)
	}
	if m.Updates == 1 {
		m.UpdatesTitle = "1 update available"
	} else if m.Updates > 1 {
		m.UpdatesTitle = strconv.Itoa(m.Updates) + " updates available"
	}
	names := make([]string, 0, len(updates))
	for _, it := range updates {
		names = append(names, it.Pkg.Name)
	}
	m.UpdateNames = strings.Join(names, ", ")

	for _, it := range m.Items {
		if it.Extra.Featured && it.Kind == pkgKindPackage {
			m.Featured = append(m.Featured, it)
		}
	}
	if len(m.Featured) == 0 {
		for _, it := range dated {
			if len(m.Featured) == 5 {
				break
			}
			m.Featured = append(m.Featured, it)
		}
	}
	if len(m.Featured) == 0 {
		for _, it := range m.Items {
			if len(m.Featured) == 5 {
				break
			}
			if it.Kind == pkgKindPackage {
				m.Featured = append(m.Featured, it)
			}
		}
	}
	return m
}

// ── Filtering and sorting (grid and list views) ──────────────────────────

// pkgSortMode is the grid and list order.
type pkgSortMode int

const (
	pkgSortName pkgSortMode = iota
	pkgSortUpdated
	pkgSortSize
	pkgSortInstalled
	pkgSortModes
)

// pkgSortKey is the value a sort is remembered under in the user config.
func pkgSortKey(m pkgSortMode) string {
	switch m {
	case pkgSortUpdated:
		return "updated"
	case pkgSortSize:
		return "size"
	case pkgSortInstalled:
		return "installed"
	}
	return "name"
}

// pkgSortFromKey is pkgSortKey's inverse; unknown values are Name.
func pkgSortFromKey(s string) pkgSortMode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "updated":
		return pkgSortUpdated
	case "size":
		return pkgSortSize
	case "installed":
		return pkgSortInstalled
	}
	return pkgSortName
}

// pkgChipMatch reports whether an item belongs under a filter chip.
func pkgChipMatch(it *pkgItem, chip string) bool {
	switch chip {
	case "", "all":
		return true
	case "installed":
		return it.Installed && it.Kind != pkgKindApp
	case "updates":
		return it.HasUpdate
	}
	if cat, ok := strings.CutPrefix(chip, "cat:"); ok {
		return it.Kind == pkgKindPackage && it.Category == cat
	}
	return false
}

// pkgLess orders two items for a sort mode; name breaks every tie.
func pkgLess(a, b *pkgItem, mode pkgSortMode) bool {
	switch mode {
	case pkgSortUpdated:
		if a.HasDate != b.HasDate {
			return a.HasDate
		}
		if a.HasDate && !a.Date.Equal(b.Date) {
			return a.Date.After(b.Date)
		}
	case pkgSortSize:
		if a.Size != b.Size {
			return a.Size > b.Size
		}
	case pkgSortInstalled:
		if a.Installed != b.Installed {
			return a.Installed
		}
		if a.HasUpdate != b.HasUpdate {
			return a.HasUpdate
		}
	}
	return a.lower < b.lower
}

// pkgFilterSort is the grid and list content for a chip and a sort. The
// JukaHub release card leads the All and Updates lists whatever the sort.
func pkgFilterSort(m *pkgModel, chip string, mode pkgSortMode) []*pkgItem {
	if m == nil {
		return nil
	}
	out := make([]*pkgItem, 0, len(m.Items)+1)
	for _, it := range m.Items {
		if pkgChipMatch(it, chip) {
			out = append(out, it)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return pkgLess(out[i], out[j], mode) })
	if m.App != nil && (chip == "" || chip == "all" || chip == "updates") {
		out = append([]*pkgItem{m.App}, out...)
	}
	return out
}

// ── Files next to the installed packages ─────────────────────────────────

const (
	pkgCatalogCacheFile  = ".jukahub-catalog.json"
	pkgInstalledMetaFile = ".jukahub-installed.json"
)

// pkgCatalogFile is the cached registry: the response as it arrived, and when.
type pkgCatalogFile struct {
	Schema    int             `json:"schema"`
	FetchedAt string          `json:"fetched_at"`
	Packages  json.RawMessage `json:"packages"`
}

func pkgCatalogCachePath() string {
	return filepath.Join(packageInstallDir(), pkgCatalogCacheFile)
}

// pkgWriteCatalogCache stores a registry response for the next (possibly
// offline) visit.
func pkgWriteCatalogCache(raw []byte, at time.Time) error {
	if !json.Valid(raw) {
		return errors.New("registry response is not JSON")
	}
	data, err := json.Marshal(pkgCatalogFile{
		Schema:    1,
		FetchedAt: at.UTC().Format(time.RFC3339),
		Packages:  json.RawMessage(raw),
	})
	if err != nil {
		return err
	}
	return AtomicWrite(pkgCatalogCachePath(), data, 0o644)
}

// pkgReadCatalogCache loads the cached registry and when it was fetched.
func pkgReadCatalogCache() ([]pkgEntry, time.Time, error) {
	data, err := os.ReadFile(pkgCatalogCachePath())
	if err != nil {
		return nil, time.Time{}, err
	}
	var f pkgCatalogFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, time.Time{}, err
	}
	entries, err := pkgParseCatalog(f.Packages)
	if err != nil {
		return nil, time.Time{}, err
	}
	at, _ := time.Parse(time.RFC3339, f.FetchedAt)
	return entries, at, nil
}

// pkgInstalledMeta records what was installed into one package folder.
type pkgInstalledMeta struct {
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Date        string `json:"date,omitempty"`
	InstalledAt string `json:"installed_at,omitempty"`
}

// pkgInstalledFile is the installed-version record on disk.
type pkgInstalledFile struct {
	Schema    int                         `json:"schema"`
	Installed map[string]pkgInstalledMeta `json:"installed"`
}

// pkgMetaMu serialises read-modify-write of the installed-version record: the
// install worker and a removal can finish at the same moment.
var pkgMetaMu sync.Mutex

func pkgInstalledMetaPath() string {
	return filepath.Join(packageInstallDir(), pkgInstalledMetaFile)
}

// pkgLoadInstalledMeta reads the installed-version record (empty when absent).
func pkgLoadInstalledMeta() map[string]pkgInstalledMeta {
	pkgMetaMu.Lock()
	defer pkgMetaMu.Unlock()
	return pkgReadInstalledMetaLocked()
}

func pkgReadInstalledMetaLocked() map[string]pkgInstalledMeta {
	out := map[string]pkgInstalledMeta{}
	data, err := os.ReadFile(pkgInstalledMetaPath())
	if err != nil {
		return out
	}
	var f pkgInstalledFile
	if json.Unmarshal(data, &f) != nil {
		return out
	}
	for k, v := range f.Installed {
		out[k] = v
	}
	return out
}

func pkgWriteInstalledMetaLocked(m map[string]pkgInstalledMeta) error {
	data, err := json.MarshalIndent(pkgInstalledFile{Schema: 1, Installed: m}, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(pkgInstalledMetaPath(), data, 0o644)
}

// pkgRecordInstalled notes the version a package was installed at, which is
// what lets the store say "Update" when the registry moves on.
func pkgRecordInstalled(p Package) {
	pkgMetaMu.Lock()
	defer pkgMetaMu.Unlock()
	m := pkgReadInstalledMetaLocked()
	m[packageDirName(p.Name)] = pkgInstalledMeta{
		Name:        p.Name,
		Version:     strings.TrimSpace(p.Version),
		Date:        strings.TrimSpace(p.Date),
		InstalledAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := pkgWriteInstalledMetaLocked(m); err != nil {
		log.Printf("packages: recording %s: %v", p.Name, err)
	}
}

// pkgForgetInstalled drops a removed package from the record.
func pkgForgetInstalled(key string) {
	pkgMetaMu.Lock()
	defer pkgMetaMu.Unlock()
	m := pkgReadInstalledMetaLocked()
	if _, ok := m[key]; !ok {
		return
	}
	delete(m, key)
	if err := pkgWriteInstalledMetaLocked(m); err != nil {
		log.Printf("packages: forgetting %s: %v", key, err)
	}
}

// pkgScanInstalled lists the package folders on the card. Dot-folders are
// skipped: they are the store's own bookkeeping, not packages.
func pkgScanInstalled() map[string]bool {
	set := map[string]bool{}
	entries, err := os.ReadDir(packageInstallDir())
	if err != nil {
		return set
	}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			set[e.Name()] = true
		}
	}
	return set
}

// pkgFolderSize is the bytes under a package folder. The walk stops after
// limit entries, so a folder full of ROMs cannot stall the background scan;
// the size is then a lower bound.
func pkgFolderSize(dir string, limit int) int64 {
	var total int64
	seen := 0
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		seen++
		if seen > limit {
			return filepath.SkipAll
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}
