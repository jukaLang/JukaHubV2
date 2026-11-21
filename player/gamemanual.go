package main

// gamemanual.go — a game's manual and guides, found where people keep them,
// and the player's own additions to them.
//
// The game page shows a "Manual" chip and a More row ("Manual & guide",
// gamemanual_ui.go) that lists the game's controls and every document found
// for it; the in-game menu has a Manual page (gamemanual_ingame.go). This file
// is the part with no drawing: where documents are looked for, how a file is
// matched to a game, and the small record of what the player added.
//
// Where it looks, in this order (the list shows them in the same order):
//
//   - next to the game: "<stem>.pdf", "<stem> (Manual).pdf", "<stem>-manual.pdf",
//     "<stem>.txt" or ".md" as a guide, and the same names with the title
//     instead of the file's name;
//   - the game's own folder, when the game is a folder (a disc set, a ScummVM
//     or DOS game): every PDF or EPUB in it, and the text files whose name
//     says manual, guide, readme, walkthrough or FAQ;
//   - the console's folders: Roms/<SYS>/manuals, manual, docs, guides,
//     media/manuals (Skraper), images/manuals; Batocera and RetroBat
//     scrape to manuals/<stem>-manual.pdf and write it into gamelist.xml's
//     <manual> tag, which is read too;
//   - the frontends' media folders beside the Roms folder (the card's root on
//     a handheld) and, on a PC, in the user's folder: ES-DE's
//     downloaded_media/<system>/manuals/<stem>.pdf (also under ES-DE/ and
//     .emulationstation/, and EmuDeck's Emulation/tools/), LaunchBox's
//     Manuals/<Platform>/<Title>-01.pdf, and a plain Manuals/<console>/;
//   - JukaHub's own folder for the game, <data>/game-manuals/<system>/<stem>/:
//     what the player added (gamemanual_add.go). Everything in it belongs to
//     the game, whatever its name; pictures there are one set of pages.
//
// A folder of page pictures (a scanned manual) counts as one document when it
// sits in a manuals folder under the game's name, or next to the game with a
// manual word in its name ("<stem> (Manual)/"): a folder next to the game
// that is only named like it is the game itself (a ScummVM folder game).
//
// Matching is loose on purpose: names are folded by artworkNormalizeTitle
// (dump tags, accents, punctuation and case go), spaces dropped and a leading
// or trailing "The" ignored, so "Kirby_s Adventure-01.pdf" (LaunchBox writes
// apostrophes as underscores and numbers the files) finds "Kirby's Adventure
// (USA).nes". A manual word at the end ("manual", "guide", "walkthrough",
// "faq", "instructions") is dropped before comparing.
//
// Discovery runs on a goroutine when the game's page opens, and when a game
// starts (so the in-game menu knows at once); the answer is cached per game
// path and read from memory by the chip, the More row and the menu. A page
// opened again after an add, or more than manCacheFor later, looks again.
//
// The record (<data>/game-manuals.json, versioned, written atomically) keeps
// what is not a file: the guide links the player added, per game path. A
// missing or broken file reads as "nothing added".

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	manSchemaVersion = 1
	manStoreName     = "game-manuals.json"
	manFolderName    = "game-manuals"
	// manCacheFor is how long a game's list is trusted before a page opened
	// again looks again (a manual copied in from a PC meanwhile).
	manCacheFor = 2 * time.Minute
	// manMaxDocs bounds one game's list.
	manMaxDocs = 32
	// manMaxDirEntries skips a folder too big to list on every page open.
	manMaxDirEntries = 20000
	// manMaxPages bounds the pictures of one scanned manual.
	manMaxPages = 600
	// manMaxLinks bounds the links one game keeps.
	manMaxLinks = 16
	// manMaxGamelist is the largest gamelist.xml that is read.
	manMaxGamelist = 32 << 20
)

// manKind is what a document is and so what opens it.
type manKind int

const (
	manKindReader   manKind = iota // PDF or EPUB: the Reader (pdfreader.go)
	manKindText                    // TXT or Markdown: the text viewer (fileeditor.go)
	manKindPictures                // page pictures: the picture viewer (imageviewer.go)
	manKindLink                    // a web page: the Browser
)

// manDoc is one document of a game.
type manDoc struct {
	Kind  manKind
	Title string
	// Path is the file (the first page of a set of pictures).
	Path  string
	Pages []string
	URL   string
	// Where says where it was found ("Next to the game", "ES-DE media").
	Where string
	Size  int64
	// User marks what the player added: X removes it.
	User bool
}

// manDocKindLabel is the document's type in words.
func manDocKindLabel(d manDoc) string {
	switch d.Kind {
	case manKindText:
		if strings.EqualFold(filepath.Ext(d.Path), ".md") {
			return "Markdown"
		}
		return "Text"
	case manKindPictures:
		return "Pictures"
	case manKindLink:
		return "Web page"
	}
	if strings.EqualFold(filepath.Ext(d.Path), ".epub") {
		return "EPUB"
	}
	return "PDF"
}

// manDocDetail is a document's second line: type, size or pages, and where.
func manDocDetail(d manDoc) string {
	parts := []string{manDocKindLabel(d)}
	switch {
	case d.Kind == manKindPictures:
		n := len(d.Pages)
		if n == 1 {
			parts = append(parts, "1 page")
		} else {
			parts = append(parts, strconv.Itoa(n)+" pages")
		}
	case d.Kind == manKindLink:
		parts = append(parts, manLinkHost(d.URL))
	case d.Size > 0:
		parts = append(parts, humanBytes(d.Size))
	}
	if d.Where != "" {
		parts = append(parts, d.Where)
	}
	return strings.Join(parts, " · ")
}

// manLinkHost is a link's site, for the list ("gamefaqs.gamespot.com").
func manLinkHost(u string) string {
	s := u
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimPrefix(s, "www.")
}

// ── Names ─────────────────────────────────────────────────────────────────

// manReaderExts, manTextExts and manPictureExts are the files a manual can be.
var (
	manReaderExts  = map[string]bool{".pdf": true, ".epub": true}
	manTextExts    = map[string]bool{".txt": true, ".md": true, ".markdown": true}
	manPictureExts = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".bmp": true, ".gif": true}
)

// manDocWords are the words a manual's file name may end in.
var manDocWords = []string{"instructions", "walkthrough", "manual", "guide", "scans", "faq"}

// manCompact folds a name to its compact key: artworkNormalizeTitle's fold,
// a leading "the" or a trailing ", The" dropped, and no spaces. Pure.
func manCompact(s string) string {
	n := artworkNormalizeTitle(s)
	n = strings.TrimPrefix(n, "the ")
	n = strings.TrimSuffix(n, " the")
	return strings.ReplaceAll(n, " ", "")
}

// manDocKey is the key of a document's file name (no extension): the compact
// name without LaunchBox's "-01" number and without a manual word at its end.
// tagged reports that a manual word was there. Pure.
func manDocKey(name string) (key string, tagged bool) {
	n := strings.TrimSpace(name)
	// LaunchBox numbers a game's manuals: "Title-01", "Title-02".
	if i := strings.LastIndexByte(n, '-'); i > 0 && len(n)-i-1 >= 1 && len(n)-i-1 <= 2 {
		if _, err := strconv.Atoi(n[i+1:]); err == nil {
			n = n[:i]
		}
	}
	// "(Manual)" is a tag group artworkNormalizeTitle drops; the word is looked
	// for in the raw name too, so it still counts as tagged.
	lower := strings.ToLower(n)
	for _, w := range manDocWords {
		if strings.Contains(lower, "("+w+")") || strings.Contains(lower, "["+w+"]") {
			tagged = true
		}
	}
	key = manCompact(n)
	for _, w := range manDocWords {
		if len(key) > len(w) && strings.HasSuffix(key, w) {
			return strings.TrimSuffix(key, w), true
		}
	}
	return key, tagged
}

// manGameKeys are the keys a document may match the game by: its file's
// name, its title and the name the player gave it.
func manGameKeys(game libraryGame) map[string]bool {
	keys := map[string]bool{}
	for _, s := range []string{romGameStem(game.Path), gameDisplayTitle(game.Path), game.Title, game.DisplayTitle} {
		if k := manCompact(s); k != "" {
			keys[k] = true
		}
	}
	return keys
}

// manSafeName makes a name safe as one folder name.
func manSafeName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return '_'
		}
		if r < 32 {
			return -1
		}
		return r
	}, s)
	s = strings.Trim(s, ". ")
	if s == "" {
		return "game"
	}
	return s
}

// manGameDir is JukaHub's own folder for game's added documents ("" without a
// data folder).
func manGameDir(game libraryGame) string {
	dir, err := P().DataDir()
	if err != nil || dir == "" || strings.TrimSpace(game.Path) == "" {
		return ""
	}
	system := strings.ReplaceAll(strings.TrimSpace(game.System), " ", "_")
	if system == "" {
		system = "Other"
	}
	return filepath.Join(dir, manFolderName, manSafeName(system), manSafeName(romGameStem(game.Path)))
}

// manConsoleDir is the console's folder of a ROM: the folder right under a
// "Roms" folder that holds it, or the ROM's own folder outside such a tree.
func manConsoleDir(romPath string) string {
	dir := filepath.Dir(romPath)
	for d, i := dir, 0; i < 6; i++ {
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		if strings.EqualFold(filepath.Base(parent), "roms") {
			return d
		}
		d = parent
	}
	return dir
}

// manPlatformKeys are the compact keys a frontend's folder for the game's
// console may have: JukaHub's name ("SNES"), its Roms folder names ("sfc",
// "snes"), the libretro collection ("Nintendo - Super Nintendo Entertainment
// System") and its parts with and without the maker (LaunchBox's "Super
// Nintendo Entertainment System", "Sega Genesis", "Sony Playstation").
func manPlatformKeys(system string) map[string]bool {
	keys := map[string]bool{}
	add := func(s string) {
		if k := strings.ReplaceAll(artworkNormalizeTitle(s), " ", ""); k != "" {
			keys[k] = true
		}
	}
	add(system)
	for folder, s := range romSystemAliases {
		if s.Name != system {
			continue
		}
		add(folder)
		add(s.Collection)
		parts := strings.Split(s.Collection, " - ")
		for i, p := range parts {
			add(p)
			if i > 0 {
				add(parts[0] + " " + p)
			}
		}
	}
	return keys
}

// ── Discovery ─────────────────────────────────────────────────────────────

// manScan is one discovery: the game's keys, the folders already listed and
// the documents found.
type manScan struct {
	keys  map[string]bool
	rom   string
	seen  map[string]bool
	lists map[string][]os.DirEntry
	docs  []manDoc
}

// list reads dir once per discovery; a missing or huge folder lists nothing.
func (s *manScan) list(dir string) []os.DirEntry {
	if dir == "" {
		return nil
	}
	if l, ok := s.lists[dir]; ok {
		return l
	}
	f, err := os.Open(dir)
	if err != nil {
		s.lists[dir] = nil
		return nil
	}
	defer f.Close()
	entries, err := f.ReadDir(manMaxDirEntries + 1)
	if (err != nil && err != io.EOF) || len(entries) > manMaxDirEntries {
		entries = nil
	}
	// The folder's own order differs between card formats: by name, so a
	// game's list reads the same on every device.
	sort.Slice(entries, func(i, j int) bool { return manNaturalLess(entries[i].Name(), entries[j].Name()) })
	s.lists[dir] = entries
	return entries
}

// child is dir's sub-folder named name, matched without case ("" when none).
func (s *manScan) child(dir, name string) string {
	for _, e := range s.list(dir) {
		if e.IsDir() && strings.EqualFold(e.Name(), name) {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}

// find follows a relative path ("media/manuals") from dir without case.
func (s *manScan) find(dir, rel string) string {
	for _, part := range strings.Split(rel, "/") {
		if dir = s.child(dir, part); dir == "" {
			return ""
		}
	}
	return dir
}

// platformDirs are dir's sub-folders named for the game's console.
func (s *manScan) platformDirs(dir string, keys map[string]bool) []string {
	var out []string
	for _, e := range s.list(dir) {
		if e.IsDir() && keys[strings.ReplaceAll(artworkNormalizeTitle(e.Name()), " ", "")] {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

// add keeps a document once (by its file or link).
func (s *manScan) add(d manDoc) {
	if len(s.docs) >= manMaxDocs {
		return
	}
	key := d.URL
	if d.Path != "" {
		key = filepath.Clean(d.Path)
		if IsWindows() {
			key = strings.ToLower(key)
		}
	}
	if key == "" || s.seen[key] {
		return
	}
	s.seen[key] = true
	s.docs = append(s.docs, d)
}

// addFile adds one file as a document when it is a kind a manual can be.
func (s *manScan) addFile(path, where string, user bool) {
	ext := strings.ToLower(filepath.Ext(path))
	kind := manKindReader
	switch {
	case manReaderExts[ext]:
	case manTextExts[ext]:
		kind = manKindText
	default:
		return
	}
	if filepath.Clean(path) == filepath.Clean(s.rom) {
		return
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return
	}
	s.add(manDoc{Kind: kind, Title: manDocTitle(kind, path, s.keys), Path: path, Where: where, Size: info.Size(), User: user})
}

// matchIn adds dir's documents named for the game. scans: a sub-folder named
// for the game is a scanned manual or holds its documents; else only one with
// a manual word in its name is (a folder named only like the game is the game).
func (s *manScan) matchIn(dir, where string, scans bool) {
	for _, e := range s.list(dir) {
		name := e.Name()
		if e.IsDir() {
			if ok, tagged := s.matches(name); ok && (scans || tagged) {
				s.folderDocs(filepath.Join(dir, name), where, false)
			}
			continue
		}
		ext := filepath.Ext(name)
		if !manReaderExts[strings.ToLower(ext)] && !manTextExts[strings.ToLower(ext)] {
			continue
		}
		if ok, _ := s.matches(strings.TrimSuffix(name, ext)); ok {
			s.addFile(filepath.Join(dir, name), where, false)
		}
	}
}

// matches reports whether a file or folder name (no extension) is named for
// the game: with a manual word dropped, or whole (a game whose own name ends
// in one). tagged: the name carries a manual word.
func (s *manScan) matches(name string) (ok, tagged bool) {
	key, tagged := manDocKey(name)
	return s.keys[key] || s.keys[manCompact(name)], tagged
}

// gameFolderDocs adds the documents of a folder game's own folder: any PDF or
// EPUB, and text files whose name says what they are.
func (s *manScan) gameFolderDocs(dir, where string) {
	for _, e := range s.list(dir) {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		switch {
		case manReaderExts[ext]:
			s.addFile(filepath.Join(dir, name), where, false)
		case manTextExts[ext]:
			lower := strings.ToLower(name)
			for _, w := range []string{"manual", "guide", "readme", "read me", "walkthrough", "faq", "instructions", "help"} {
				if strings.Contains(lower, w) {
					s.addFile(filepath.Join(dir, name), where, false)
					break
				}
			}
		}
	}
}

// folderDocs adds everything in a folder that belongs to one game: its
// documents one by one and its pictures as one set of pages.
func (s *manScan) folderDocs(dir, where string, user bool) {
	var pages []string
	for _, e := range s.list(dir) {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if manPictureExts[strings.ToLower(filepath.Ext(e.Name()))] {
			if len(pages) < manMaxPages {
				pages = append(pages, p)
			}
			continue
		}
		s.addFile(p, where, user)
	}
	if len(pages) == 0 {
		return
	}
	sort.SliceStable(pages, func(i, j int) bool { return manNaturalLess(filepath.Base(pages[i]), filepath.Base(pages[j])) })
	title := "Scanned manual"
	if user {
		title = "Your pictures"
	}
	s.add(manDoc{Kind: manKindPictures, Title: title, Path: pages[0], Pages: pages, Where: where, User: user})
}

// manDocTitle is a document's name in the list: "Manual" or "Guide" when the
// file is only named for the game, else its own name.
func manDocTitle(kind manKind, path string, keys map[string]bool) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	key, _ := manDocKey(base)
	if !keys[key] {
		return strings.TrimSpace(strings.ReplaceAll(base, "_", " "))
	}
	lower := strings.ToLower(base)
	switch {
	case strings.Contains(lower, "walkthrough"):
		return "Walkthrough"
	case strings.Contains(lower, "faq"):
		return "FAQ"
	case strings.Contains(lower, "guide"):
		return "Guide"
	case kind == manKindText:
		return "Guide"
	}
	return "Manual"
}

// manNaturalLess orders names with their numbers by value ("page2" before
// "page10"), the order a scan's pages are numbered in. Pure.
func manNaturalLess(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	for a != "" && b != "" {
		da, db := manLeadDigits(a), manLeadDigits(b)
		if da != "" && db != "" {
			na, _ := strconv.Atoi(da)
			nb, _ := strconv.Atoi(db)
			if na != nb {
				return na < nb
			}
			a, b = a[len(da):], b[len(db):]
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

// manLeadDigits is the run of digits s starts with (at most nine).
func manLeadDigits(s string) string {
	i := 0
	for i < len(s) && i < 9 && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}

// manGameHasFile reports whether game is a file on disk (a built-in game's
// "builtin:…" path is not: it has no folder to look in).
func manGameHasFile(game libraryGame) bool {
	return filepath.IsAbs(strings.TrimSpace(game.Path))
}

// manDiscover lists game's documents. Disk work: a goroutine only.
func manDiscover(game libraryGame) []manDoc {
	path := strings.TrimSpace(game.Path)
	if !manGameHasFile(game) {
		return nil
	}
	s := &manScan{keys: manGameKeys(game), rom: path, seen: map[string]bool{}, lists: map[string][]os.DirEntry{}}
	if len(s.keys) == 0 {
		return nil
	}
	romDir := filepath.Dir(path)
	consoleDir := manConsoleDir(path)

	// Next to the game, and its own folder when it is a folder game.
	s.matchIn(romDir, "Next to the game", false)
	if consoleDir != romDir {
		// Only a folder named for the game is the game's own (a disc set, a
		// ScummVM game whose files sit deeper): a folder that sorts several
		// games ("Roms/GBA/Hacks") would hand each of them every PDF and
		// readme in it.
		for d := romDir; d != consoleDir && filepath.Dir(d) != d; d = filepath.Dir(d) {
			if ok, _ := s.matches(filepath.Base(d)); ok {
				s.gameFolderDocs(d, "In the game's folder")
				break
			}
		}
		s.matchIn(consoleDir, "Next to the game", false)
	}

	// The console's folders, and the gamelist a Batocera-style scraper wrote.
	// Batocera's images folder holds screenshots too: a sub-folder there is a
	// scan only when its name says manual.
	for _, rel := range []string{"manuals", "manual", "docs", "doc", "guides", "media/manuals", "media/manual", "images/manuals", "images"} {
		if dir := s.find(consoleDir, rel); dir != "" {
			s.matchIn(dir, "Console's manuals folder", rel != "images")
		}
	}
	if manual := manGamelistManual(consoleDir, path); manual != "" {
		s.addFile(manual, "From the gamelist", false)
	}

	// The frontends' media folders.
	platform := manPlatformKeys(game.System)
	for _, root := range manMediaRoots(consoleDir) {
		for _, rel := range []string{"downloaded_media", "ES-DE/downloaded_media", ".emulationstation/downloaded_media", "Emulation/tools/downloaded_media"} {
			if media := s.find(root, rel); media != "" {
				for _, sys := range s.platformDirs(media, platform) {
					if dir := s.child(sys, "manuals"); dir != "" {
						s.matchIn(dir, "ES-DE media", true)
					}
				}
			}
		}
		if lb := s.find(root, "LaunchBox/Manuals"); lb != "" {
			for _, dir := range s.platformDirs(lb, platform) {
				s.matchIn(dir, "LaunchBox", true)
			}
		}
		if plain := s.child(root, "Manuals"); plain != "" {
			s.matchIn(plain, "Manuals folder", true)
			for _, dir := range s.platformDirs(plain, platform) {
				s.matchIn(dir, "Manuals folder", true)
			}
		}
	}

	// What the player added: files, then links.
	if dir := manGameDir(game); dir != "" {
		s.folderDocs(dir, "Added by you", true)
	}
	for _, l := range manStoreLinks(path) {
		title := strings.TrimSpace(l.Title)
		if title == "" {
			title = "Guide on " + manLinkHost(l.URL)
		}
		s.add(manDoc{Kind: manKindLink, Title: title, URL: l.URL, Where: "Added by you", User: true})
	}
	return s.docs
}

// manMediaRoots are the folders a frontend's media folder may sit in: the
// one that holds the Roms folder (the card's root on a handheld), and on a PC
// the user's own folder.
func manMediaRoots(consoleDir string) []string {
	var roots []string
	parent := filepath.Dir(consoleDir)
	if strings.EqualFold(filepath.Base(parent), "roms") {
		roots = append(roots, filepath.Dir(parent))
	} else {
		roots = append(roots, parent)
	}
	if IsWindows() {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			roots = append(roots, home)
		}
	}
	out := roots[:0]
	seen := map[string]bool{}
	for _, r := range roots {
		r = filepath.Clean(r)
		// A card's or drive's root is fine; Linux's own root is not looked at.
		if r == "." || seen[r] || (filesIsFilesystemRoot(r) && !IsWindows()) {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}

// ── gamelist.xml ──────────────────────────────────────────────────────────

// manGamelistEntry is the part of a gamelist <game> the manual needs.
type manGamelistEntry struct {
	Path   string `xml:"path"`
	Manual string `xml:"manual"`
}

// manGamelists caches each console folder's manuals from its gamelist.xml,
// keyed by the ROM's file name in lower case, until the file changes.
var manGamelists struct {
	mu    sync.Mutex
	files map[string]manGamelistCache
}

type manGamelistCache struct {
	mod     time.Time
	size    int64
	manuals map[string]string
}

// manGamelistManual is the manual consoleDir's gamelist.xml names for romPath
// ("" when none). Disk work: a goroutine only.
func manGamelistManual(consoleDir, romPath string) string {
	file := filepath.Join(consoleDir, "gamelist.xml")
	info, err := os.Stat(file)
	if err != nil || !info.Mode().IsRegular() || info.Size() > manMaxGamelist {
		return ""
	}
	manGamelists.mu.Lock()
	c, ok := manGamelists.files[file]
	manGamelists.mu.Unlock()
	if !ok || !c.mod.Equal(info.ModTime()) || c.size != info.Size() {
		f, err := os.Open(file)
		if err != nil {
			return ""
		}
		// The cap again on the read: the file may have grown since the Stat.
		manuals := manParseGamelist(io.LimitReader(f, manMaxGamelist), consoleDir)
		f.Close()
		c = manGamelistCache{mod: info.ModTime(), size: info.Size(), manuals: manuals}
		manGamelists.mu.Lock()
		if manGamelists.files == nil {
			manGamelists.files = map[string]manGamelistCache{}
		}
		manGamelists.files[file] = c
		manGamelists.mu.Unlock()
	}
	rel, err := filepath.Rel(consoleDir, romPath)
	if err != nil {
		rel = filepath.Base(romPath)
	}
	return c.manuals[strings.ToLower(filepath.ToSlash(rel))]
}

// manParseGamelist reads the <game> entries that name a manual: the ROM's
// path relative to the console folder (lower case, forward slashes) → the
// manual's path. Paths are relative to the gamelist ("./manuals/x.pdf"); a
// manual outside the console's folder tree is ignored. A broken file yields
// what was read before the break.
func manParseGamelist(r io.Reader, consoleDir string) map[string]string {
	out := map[string]string{}
	dec := xml.NewDecoder(r)
	dec.Strict = false
	for {
		tok, err := dec.Token()
		if err != nil {
			return out
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "game" {
			continue
		}
		var g manGamelistEntry
		if dec.DecodeElement(&g, &start) != nil {
			return out
		}
		rom, manual := manGamelistPath(g.Path), manGamelistPath(g.Manual)
		if rom == "" || manual == "" || manual == ".." || strings.HasPrefix(manual, "../") {
			continue
		}
		out[strings.ToLower(rom)] = filepath.Join(consoleDir, filepath.FromSlash(manual))
	}
}

// manGamelistPath cleans a gamelist path to a relative, forward-slash path
// ("./manuals/x.pdf" is "manuals/x.pdf"); "" for an absolute one.
func manGamelistPath(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, ":") {
		return ""
	}
	p = filepath.ToSlash(filepath.Clean(filepath.FromSlash(p)))
	if p == "." {
		return ""
	}
	return p
}

// ── The cache ─────────────────────────────────────────────────────────────

// manCache is each game's last discovery, by game path. Any goroutine.
var manCache struct {
	mu    sync.Mutex
	games map[string]*manCacheEntry
}

type manCacheEntry struct {
	docs  []manDoc
	at    time.Time
	known bool // a discovery has finished
	dirty bool // something was added or removed: look again
	busy  bool
	// again: a forced look was asked while one ran (an add finished during
	// it), so another runs once this one ends.
	again bool
	// waiters hear the newest list once no further look is due.
	waiters []func([]manDoc)
}

// manDocsFor is game path's documents from memory; known is false until the
// first discovery for it finished. Any goroutine (the chip reads it every
// frame).
func manDocsFor(path string) (docs []manDoc, known bool) {
	manCache.mu.Lock()
	defer manCache.mu.Unlock()
	e := manCache.games[path]
	if e == nil {
		return nil, false
	}
	return e.docs, e.known
}

// manInvalidate marks a game's list stale (an add or a removal).
func manInvalidate(path string) {
	manCache.mu.Lock()
	if e := manCache.games[path]; e != nil {
		e.dirty = true
	}
	manCache.mu.Unlock()
}

// manRefresh starts a discovery for game unless a fresh one is cached or one
// is running; force looks again regardless (after the running one). done, if
// given, runs on the main thread with the newest list.
func manRefresh(game libraryGame, force bool, done func([]manDoc)) {
	path := strings.TrimSpace(game.Path)
	if path == "" {
		return
	}
	manCache.mu.Lock()
	defer manCache.mu.Unlock()
	if manCache.games == nil {
		manCache.games = map[string]*manCacheEntry{}
	}
	e := manCache.games[path]
	if e == nil {
		e = &manCacheEntry{}
		manCache.games[path] = e
	}
	if e.busy {
		if done != nil {
			e.waiters = append(e.waiters, done)
		}
		if force {
			e.again = true
		}
		return
	}
	if e.known && !e.dirty && !force && time.Since(e.at) < manCacheFor {
		return
	}
	if done != nil {
		e.waiters = append(e.waiters, done)
	}
	manStartLocked(game, e)
}

// manStartLocked runs one discovery for e on a goroutine (manCache.mu held).
func manStartLocked(game libraryGame, e *manCacheEntry) {
	e.busy, e.dirty, e.again = true, false, false
	go func() {
		var docs []manDoc
		defer func() {
			if r := recover(); r != nil {
				logWarn("manual", "looking for %s's manuals failed: %v", filepath.Base(game.Path), r)
			}
			manCache.mu.Lock()
			e.docs, e.at, e.known, e.busy = docs, time.Now(), true, false
			var waiters []func([]manDoc)
			if e.again || e.dirty {
				// Something changed while this one looked: the waiters hear
				// the next list instead.
				manStartLocked(game, e)
			} else {
				waiters, e.waiters = e.waiters, nil
			}
			manCache.mu.Unlock()
			for _, fn := range waiters {
				fn := fn
				tbxPost(func() { fn(docs) })
			}
		}()
		docs = manDiscover(game)
	}()
}

// ── The record (<data>/game-manuals.json) ─────────────────────────────────

// manLink is a guide on the web the player added.
type manLink struct {
	Title string `json:"title,omitempty"`
	URL   string `json:"url"`
}

type manStoreGame struct {
	Links []manLink `json:"links,omitempty"`
}

type manStoreFile struct {
	SchemaVersion int                     `json:"schemaVersion"`
	Games         map[string]manStoreGame `json:"games"`
}

// manStore is the record in memory, loaded on first use. Any goroutine.
var manStore struct {
	mu     sync.Mutex
	loaded bool
	// newer: the file on disk is a newer JukaHub's; it is read as nothing
	// added and never written over.
	newer bool
	file  manStoreFile
}

// manStorePath is the record's file ("" without a data folder).
func manStorePath() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, manStoreName)
}

// manStoreLoadLocked reads the record once; a missing or broken file, or one
// from a newer JukaHub, reads as nothing added.
func manStoreLoadLocked() {
	if manStore.loaded {
		return
	}
	manStore.loaded = true
	manStore.file = manStoreFile{SchemaVersion: manSchemaVersion, Games: map[string]manStoreGame{}}
	path := manStorePath()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if f, ok := manParseStore(data); ok {
		manStore.file = f
		return
	}
	var head struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if json.Unmarshal(data, &head) == nil && head.SchemaVersion > manSchemaVersion {
		manStore.newer = true
		logWarn("manual", "%s is from a newer JukaHub; added guides are not shown or changed", manStoreName)
		return
	}
	logWarn("manual", "%s could not be read; starting with no added guides", manStoreName)
}

// manParseStore decodes the record. ok is false for a broken file or one a
// newer JukaHub wrote. Pure.
func manParseStore(data []byte) (manStoreFile, bool) {
	var f manStoreFile
	if err := json.Unmarshal(data, &f); err != nil || f.SchemaVersion > manSchemaVersion {
		return manStoreFile{SchemaVersion: manSchemaVersion, Games: map[string]manStoreGame{}}, false
	}
	f.SchemaVersion = manSchemaVersion
	if f.Games == nil {
		f.Games = map[string]manStoreGame{}
	}
	for k, g := range f.Games {
		kept := g.Links[:0]
		for _, l := range g.Links {
			if strings.TrimSpace(l.URL) != "" && len(kept) < manMaxLinks {
				kept = append(kept, l)
			}
		}
		g.Links = kept
		if len(g.Links) == 0 {
			delete(f.Games, k)
			continue
		}
		f.Games[k] = g
	}
	return f, true
}

// manStoreLinks is the links added for a game path.
func manStoreLinks(path string) []manLink {
	manStore.mu.Lock()
	defer manStore.mu.Unlock()
	manStoreLoadLocked()
	return append([]manLink(nil), manStore.file.Games[path].Links...)
}

// manStoreUpdate changes one game's entry and writes the record (on the
// caller's goroutine: callers are goroutines). The lock is held through the
// write, so two quick changes reach the disk in the order they were made.
func manStoreUpdate(path string, change func(*manStoreGame)) error {
	manStore.mu.Lock()
	defer manStore.mu.Unlock()
	manStoreLoadLocked()
	if manStore.newer {
		return errors.New(manStoreName + " was written by a newer JukaHub")
	}
	file := manStorePath()
	if file == "" {
		return os.ErrNotExist
	}
	g := manStore.file.Games[path]
	change(&g)
	if len(g.Links) == 0 {
		delete(manStore.file.Games, path)
	} else {
		manStore.file.Games[path] = g
	}
	data, err := json.MarshalIndent(manStore.file, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(file, append(data, '\n'), 0o644)
}
