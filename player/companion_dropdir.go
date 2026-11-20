package main

// companion_dropdir.go — whole folders on the Phone Companion's drop card: a
// books collection, an album, a folder of pictures or videos, a disc game with
// its tracks, a DOS or ScummVM game folder, sent in one go with the folder's
// structure kept (as the community app TrimUI Drop 1.1 does).
//
// A folder travels like a file (companion_drop.go), in steps behind the PIN:
//
//   - POST /api/drop/folder with the folder's listing as JSON, {"name",
//     "batch", "to", "files": [{"path", "size"}]}, before any byte of a file.
//     Each relative path is checked here (companionDropRelPath): one that
//     would leave the folder or that the card's FAT cannot hold is left out,
//     and the answer says which and why. The handheld keeps the listing under
//     a token and answers one plan for the whole folder
//     (companionDropDirRoute): where it goes ("/Books/Asimov",
//     "/Roms/PS/Final Fantasy VII") and what it is; or "need", the first or
//     last bytes of the one file that decides a game's console (the page
//     posts them to ?id=…&part=head|tail); or "ask" (?id=…&as=<choice>, or
//     ?id=…&to=drop for PhoneDrop).
//   - POST /api/drop/folder/file?id=<token>&i=<n>[&replace=1] carries file n
//     of the listing, written at its relative path under the plan's folder by
//     companionFilesStore (a hidden temporary name renamed when complete, a
//     file of that name replaced only on request). The page names a file by
//     its place in the listing, never by a path, so nothing is written where
//     the plan did not say, nor more bytes than the listing gave.
//   - POST /api/drop/done?batch=… (companion_drop.go) ends the batch: one
//     line on the handheld, "Phone sent the folder Asimov (42 books)", and
//     the libraries look again.
//
// Where a folder goes is decided by what is inside it, first rule that fits:
//
//   - a PortMaster port, or anything with programs or scripts the handheld
//     could run (.sh, .so, .py, .apk …): PhoneDrop/<Folder>, never Apps or
//     Roms/Ports — the folders that hold programs are not written from the
//     network;
//   - a ScummVM game (a .scummvm file, or files the ScummVM shelf names,
//     shelf_scummvm_ids.go): Roms/<ScummVM folder>/<Folder>, where the shelf
//     writes its .scummvm and lists it;
//   - a Windows program (.dll, .msi) that is not a DOS game with its dosbox
//     .conf (GOG's releases): PhoneDrop/<Folder>;
//   - a DOS game (.exe, .com, .bat, a dosbox .conf): Roms/<DOS folder>/<Folder>,
//     which the DOS shelf lists by its start program (shelf_dos.go);
//   - a disc game (a .cue or .ccd with its tracks, a .gdi, an .m3u with its
//     discs, a .chd or .iso …), a folder of zips or of .bin cartridges:
//     Roms/<console>/<Folder>. The library walks into the folder and lists a
//     disc set once (dropSheetParts). The console comes from one file through
//     companionDropRoute — the header and zip checks a single file gets — or
//     is asked;
//   - cartridge games of one console: Roms/<console>/<Folder>;
//   - otherwise the media inside: books and comics → Books/<Folder>, songs
//     (with their covers, .cue and .log) → Music/<Folder>, videos →
//     Videos/<Folder>, pictures → Pictures/<Folder>; asked when two kinds are
//     close (10 songs and 8 videos);
//   - anything else: PhoneDrop/<Folder>.
//
// A folder named after a console's folder ("GBA", "Game Boy (GB)", "DOS") is
// that folder: its files merge into the console's own folder rather than a
// folder inside it, and its name settles the console of files nothing else
// can tell (zips, discs). The same for a folder named like its media folder
// ("Books" into Books).

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// The kinds of folder beyond a file's (companion_drop_route.go): a folder of
// songs is companionDropMusicKind, of games companionDropGameKind, and so on.
const (
	companionDropBooksKind    = "books"
	companionDropPicturesKind = "pictures"
	companionDropDOSKind      = "dos"
	companionDropScummVMKind  = "scummvm"
)

const (
	// companionDropDirMaxFiles bounds one folder's listing.
	companionDropDirMaxFiles = 5000
	// companionDropDirMaxDepth bounds the folders a path goes down.
	companionDropDirMaxDepth = 12
	// companionDropDirMaxPath bounds a clean relative path, in bytes.
	companionDropDirMaxPath = 1024
	// companionDropDirMaxBody bounds the listing's JSON.
	companionDropDirMaxBody = 4 << 20
	// companionDropDirMaxFolders bounds the folders the handheld keeps.
	companionDropDirMaxFolders = 16
	// companionDropDirMargin is the free space a folder leaves on the card.
	companionDropDirMargin = 32 << 20
)

// Where the media kinds live on the card, first name found wins; the first
// is made when there is none.
var (
	companionDropBooksDirs    = []string{"Books"}
	companionDropPicturesDirs = []string{"Pictures", "Photos", "Images"}
)

// companionDropBookExts are books and comics; companionDropBookWeakExts are
// the ones a game or an album also carries (a manual, a booklet, a readme).
var (
	companionDropBookExts = map[string]bool{
		".epub": true, ".pdf": true, ".mobi": true, ".azw3": true, ".azw": true, ".fb2": true,
		".djvu": true, ".cbz": true, ".cbr": true, ".cb7": true, ".txt": true,
	}
	companionDropBookWeakExts = map[string]bool{".pdf": true, ".txt": true}
)

// companionDropPictureExts are photos and pictures.
var companionDropPictureExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".gif": true, ".bmp": true,
	".heic": true, ".heif": true, ".tif": true, ".tiff": true, ".avif": true,
}

// companionDropNativeExts are programs and scripts that run on a Linux or
// Android device like the handheld; companionDropWindowsExts are a Windows
// program's (a DOS program DOSBox runs is neither). A GOG release of a DOS or
// ScummVM game carries DOSBox for Windows (SDL.dll) beside the game, so a
// Windows file only sends a folder to PhoneDrop when nothing else names it.
var (
	companionDropNativeExts = map[string]bool{
		".sh": true, ".so": true, ".py": true, ".pyc": true, ".elf": true, ".apk": true,
		".appimage": true, ".deb": true, ".ipk": true, ".run": true,
	}
	companionDropWindowsExts = map[string]bool{".dll": true, ".msi": true}
)

// A disc game's files: sheets that list tracks or discs, the tracks a sheet
// lists (a .bin alone may also be a cartridge), and images that are a game
// on their own.
var (
	companionDropDirSheetExts = map[string]bool{".cue": true, ".ccd": true, ".gdi": true, ".m3u": true, ".mds": true}
	companionDropDirTrackExts = map[string]bool{".bin": true, ".img": true, ".raw": true, ".sub": true, ".mdf": true}
	companionDropDirImageExts = map[string]bool{
		".iso": true, ".chd": true, ".cdi": true, ".pbp": true, ".cso": true, ".gcz": true, ".rvz": true,
		".wbfs": true, ".ciso": true, ".gcm": true, ".xiso": true, ".zar": true,
	}
)

// companionDropDirFile is one file of the page's listing.
type companionDropDirFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// companionDropDirEntry is one listed file once checked: its clean relative
// path ("/"-separated), or "" and why it is left out. Quiet marks a file left
// out without a word (hidden and system files: .DS_Store, Thumbs.db).
type companionDropDirEntry struct {
	Rel   string
	Size  int64
	Why   string
	Quiet bool
}

// companionDropDirIn is one folder the page asks about. Name is clean
// (companionDropDirName); Head or Tail are the bytes of the file the last
// "need" answer named.
type companionDropDirIn struct {
	Name  string
	Files []companionDropDirEntry
	As    string // what the player picked: a folder kind, a console id, "drop"
	Plain bool   // the player asked for PhoneDrop
	Head  []byte
	Tail  []byte
}

// companionDropDirEnv is the card as a folder's decision sees it: the
// single-file card plus Books and Pictures, all share paths ("" not shared).
type companionDropDirEnv struct {
	companionDropEnv
	Books    string
	Pictures string
}

// companionDropDirPlan is the answer for one folder. Dir is the share path
// its files go under. Need, NeedFile and NeedBytes ask for the head or tail
// of the listing's file NeedFile.
type companionDropDirPlan struct {
	Kind      string
	Dir       string
	Label     string // what it is: "Books", "PlayStation", "DOS game"
	Note      string
	Need      string
	NeedFile  int
	NeedBytes int
	Choices   []companionDropChoice
	System    string // a game folder's console (a library name)
}

// companionDropDirFacts is what a folder holds, counted by kind.
type companionDropDirFacts struct {
	books, booksStrong, audio, video, pictures int
	carts, zips                                int
	cartSystems                                map[string]bool
	sheets, tracks, images                     int
	native, windows                            int
	port, dos, dosConf, scummvm, disc, byFile  bool
	rep                                        int    // the file that decides a disc's or a zip's console, -1
	console                                    string // the console the folder's name is ("" none)
}

// ── Paths ───────────────────────────────────────────────────────────────

// companionDropRelPath checks one relative path of a listing and returns it
// clean, "/"-separated, or why it is left out (quiet for hidden and system
// files). A path is refused, never repaired, when it would leave the folder
// (".." , an absolute path, a drive letter), holds NUL or control characters,
// or goes too deep; each name is then cleaned as sanitizeUploadName cleans an
// upload's (the card's FAT refuses :*?"<>|, trailing dots and spaces), and a
// reserved device name (CON, NUL, COM1 …) gets a "_" in front.
func companionDropRelPath(raw string) (string, string, bool) {
	if raw == "" || strings.ContainsRune(raw, 0) {
		return "", "not a usable name", false
	}
	if !utf8.ValidString(raw) {
		return "", "its name is not valid text", false
	}
	// Twelve names of 255 bytes come to about 3 KB; a longer path is refused
	// before it is split (a 4 MB "a/a/a/…" would make two million names).
	if len(raw) > 8*companionDropDirMaxPath {
		return "", "its path is too long", false
	}
	p := strings.ReplaceAll(raw, "\\", "/")
	if strings.HasPrefix(p, "/") {
		return "", "an absolute path", false
	}
	if c := p[0] | 0x20; len(p) >= 2 && p[1] == ':' && c >= 'a' && c <= 'z' {
		return "", "an absolute path", false
	}
	var out []string
	for _, part := range strings.Split(p, "/") {
		switch part {
		case "", ".":
			continue
		case "..":
			return "", "it would leave the folder", false
		}
		for _, r := range part {
			if r < 0x20 || r == 0x7f {
				return "", "control characters in its name", false
			}
		}
		if strings.HasPrefix(part, ".") || companionDropDirJunk(part) {
			return "", "a hidden or system file", true
		}
		clean := strings.TrimRight(sanitizeUploadName(part), ". ")
		if clean == "" || strings.Trim(part, ". ") == "" {
			return "", "not a usable name", false
		}
		out = append(out, companionDropNotReserved(clean))
	}
	if len(out) == 0 {
		return "", "not a usable name", false
	}
	if len(out) > companionDropDirMaxDepth {
		return "", "too many folders deep", false
	}
	rel := strings.Join(out, "/")
	if len(rel) > companionDropDirMaxPath {
		return "", "its path is too long", false
	}
	return rel, "", false
}

// companionDropDirJunk reports the system files a computer leaves in folders.
func companionDropDirJunk(name string) bool {
	switch strings.ToLower(name) {
	case "__macosx", "thumbs.db", "desktop.ini", "$recycle.bin", "system volume information":
		return true
	}
	return false
}

// companionDropNotReserved puts "_" before a name Windows and FAT reserve
// for devices, with or without an extension ("CON", "nul.txt", "COM1.bin").
func companionDropNotReserved(name string) string {
	stem := strings.ToUpper(name)
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	stem = strings.TrimRight(stem, " ")
	switch stem {
	case "CON", "PRN", "AUX", "NUL":
		return "_" + name
	}
	if len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9' {
		return "_" + name
	}
	return name
}

// companionDropDirName is the folder's own name made one the card holds.
func companionDropDirName(raw string) string {
	raw = strings.Trim(strings.ReplaceAll(raw, "\\", "/"), "/")
	if i := strings.LastIndexByte(raw, '/'); i >= 0 {
		raw = raw[i+1:]
	}
	name := strings.TrimRight(sanitizeUploadName(raw), ". ")
	if strings.Trim(raw, ". ") == "" || name == "" {
		name = "Folder"
	}
	return companionDropNotReserved(name)
}

// companionDropDirCheck checks a listing: each path clean, or left out with
// why. Two paths the card would take for one (it ignores case) keep the
// first, and a file named like a folder other paths make is left out.
func companionDropDirCheck(files []companionDropDirFile) []companionDropDirEntry {
	out := make([]companionDropDirEntry, len(files))
	seen := make(map[string]bool, len(files))
	dirs := make(map[string]bool)
	for i, f := range files {
		e := companionDropDirEntry{Size: f.Size}
		rel, why, quiet := companionDropRelPath(f.Path)
		key := strings.ToLower(rel)
		switch {
		case why != "":
			e.Why, e.Quiet = why, quiet
		case f.Size < 0:
			e.Why = "its size is not known"
		case f.Size > companionMaxUpload-(64<<10):
			e.Why = "larger than the handheld takes (4 GB)"
		case seen[key]:
			e.Why = "another file has the same name on the card"
		default:
			e.Rel = rel
			seen[key] = true
			for j := 0; j < len(key); j++ {
				if key[j] == '/' {
					dirs[key[:j]] = true
				}
			}
		}
		out[i] = e
	}
	for i := range out {
		if out[i].Rel != "" && dirs[strings.ToLower(out[i].Rel)] {
			out[i].Rel, out[i].Why = "", "a folder of the same name is sent too"
		}
	}
	return out
}

// companionDropDirUnder is the share path of name inside the folder base.
func companionDropDirUnder(base, name string) string {
	if base == "" {
		return ""
	}
	return base + "/" + name
}

// ── The decision ────────────────────────────────────────────────────────

// companionDropDirRoute decides where one folder goes (see the file comment).
func companionDropDirRoute(in companionDropDirIn, env companionDropDirEnv) companionDropDirPlan {
	name := in.Name
	plain := func(note string) companionDropDirPlan {
		return companionDropDirPlan{Kind: companionDropPlainKind, Dir: companionDropDirUnder(env.Drop, name), Label: "PhoneDrop", Note: note}
	}
	if in.Plain {
		return plain("")
	}
	f := companionDropDirSurvey(in.Files, name)
	switch {
	case f.port:
		p := plain("A PortMaster port. Ports run programs, so it waits in PhoneDrop: install it with PortMaster on the handheld.")
		p.Kind, p.Label = companionDropPortKind, "PortMaster port"
		return p
	case f.native > 0:
		return plain("It holds programs or scripts, and nothing that runs is written from the network, so it waits in PhoneDrop.")
	case in.As != "":
		// A pick (?as=, which any request may carry) never moves programs
		// out of PhoneDrop: the two cases above come first.
		return companionDropDirChosen(in, env, plain)
	case f.scummvm:
		return companionDropDirShelf(env, name, "ScummVM", "SCUMMVM", companionDropScummVMKind, plain)
	case f.windows > 0 && !f.dosConf:
		return plain("A Windows program, which the handheld does not run, so it waits in PhoneDrop.")
	case f.dos:
		return companionDropDirShelf(env, name, "DOS", "DOS", companionDropDOSKind, plain)
	case f.disc || f.byFile:
		return companionDropDirByFile(in, env, f, plain)
	case f.carts > 0 && f.carts >= f.audio+f.video+f.booksStrong:
		return companionDropDirCarts(env, name, f, plain)
	}
	return companionDropDirMedia(env, name, f, plain)
}

// companionDropDirSurvey counts what a folder holds.
func companionDropDirSurvey(files []companionDropDirEntry, folder string) companionDropDirFacts {
	f := companionDropDirFacts{cartSystems: map[string]bool{}, rep: -1, console: companionDropDirConsoleNamed(folder)}
	top := map[string]bool{}
	var names []string
	progs, svFile, gdi, m3u, cueSet := 0, false, false, false, false
	repRank := 0
	for i, e := range files {
		if e.Rel == "" {
			continue
		}
		names = append(names, e.Rel)
		base := strings.ToLower(path.Base(e.Rel))
		ext := path.Ext(base)
		if !strings.Contains(e.Rel, "/") {
			top[base] = true
		}
		switch {
		case companionDropNativeExts[ext]:
			f.native++
		case companionDropWindowsExts[ext]:
			f.windows++
		case shelfDOSIsProgram(ext):
			progs++
		case ext == ".conf" && strings.HasPrefix(base, "dosbox"):
			f.dosConf = true
		case ext == ".scummvm":
			svFile = true
		case strings.HasSuffix(base, ".p8.png"):
			f.carts++
			f.cartSystems[companionDropExtraExts[".p8"]] = true
		case isAudioFile(base):
			f.audio++
		case mlibVideoExts[ext]:
			f.video++
		case companionDropPictureExts[ext]:
			f.pictures++
		case companionDropBookExts[ext]:
			f.books++
			if !companionDropBookWeakExts[ext] {
				f.booksStrong++
			}
		case ext == ".zip" || ext == ".7z":
			f.zips++
		case companionDropDirSheetExts[ext]:
			f.sheets++
			gdi = gdi || ext == ".gdi"
			m3u = m3u || ext == ".m3u"
			cueSet = cueSet || ext == ".cue" || ext == ".ccd" || ext == ".mds"
		case companionDropDirTrackExts[ext]:
			f.tracks++
		case companionDropDirImageExts[ext]:
			f.images++
		default:
			if sys := companionDropDirCartSystem(ext); sys != "" {
				f.carts++
				f.cartSystems[sys] = true
			}
		}
		if rank := companionDropDirRepRank(base, ext); rank > repRank || (rank == repRank && rank > 0 && e.Size > files[f.rep].Size) {
			f.rep, repRank = i, rank
		}
	}
	f.port = companionDropIsPort(names)
	f.dos = progs > 0 || f.dosConf
	// ScummVM before DOS: a Sierra or LucasArts game folder holds its DOS
	// program too, and ScummVM plays it better. Engine files with no program
	// are an adventure the shelf could not name (its More row says so).
	id, _ := shelfSVDetect(top, folder)
	engine := (top["words.tok"] && top["object"]) || top["resource.map"] || top["resmap.000"] ||
		top["resmap.001"] || top["ressci.000"] || top["00.lfl"] || top["000.lfl"]
	f.scummvm = svFile || id != "" || (engine && progs == 0)
	f.disc = f.images > 0 || gdi || (cueSet && f.tracks > 0) || (m3u && f.tracks > 0)
	media := f.audio + f.video + f.booksStrong
	f.byFile = !f.disc && f.carts == 0 && f.zips+f.tracks > 0 && media == 0 && f.pictures <= f.zips+f.tracks
	return f
}

// companionDropDirCartSystem is the console a cartridge type belongs to, by
// the tables a single file is filed by; "" when the type is not one console's.
func companionDropDirCartSystem(ext string) string {
	if id, ok := rommExtSystems[ext]; ok {
		if sys, ok := rommSystemByID(id); ok {
			return sys.Name
		}
	}
	return companionDropExtraExts[ext]
}

// companionDropDirRepRank ranks a file as the one whose header or directory
// tells a folder's console: a Dreamcast sheet or image settles it; a disc
// image or a track the header names comes next (a later "(Track 2)" after the
// first, which holds the disc's header); then a zip, whose directory is read;
// then a sheet or container nothing can see into, which is asked about.
func companionDropDirRepRank(base, ext string) int {
	switch {
	case ext == ".gdi" || ext == ".cdi":
		return 6
	case ext == ".bin" || ext == ".img":
		if companionDropDirLaterTrack(base) {
			return 3
		}
		return 5
	case ext == ".md" || ext == ".rom":
		return 0 // a note or a firmware file as often as a game
	case companionDropHeadExts[ext] || companionDropDirImageExts[ext]:
		if ext == ".chd" || ext == ".gcz" {
			return 1
		}
		return 5
	case ext == ".zip":
		return 4
	case ext == ".7z" || companionDropDirSheetExts[ext]:
		return 1
	}
	return 0
}

// companionDropDirLaterTrack reports a track after the first: "(Track 2)",
// "track 02", "Track 10" (the first holds the disc's header).
func companionDropDirLaterTrack(base string) bool {
	i := strings.LastIndex(base, "track")
	if i < 0 {
		return false
	}
	rest := strings.TrimLeft(base[i+len("track"):], " _-0")
	if rest == "" || rest[0] < '1' || rest[0] > '9' {
		return false
	}
	return rest[0] != '1' || (len(rest) > 1 && rest[1] >= '0' && rest[1] <= '9')
}

// companionDropDirConsoleNamed is the console a folder named like a console's
// folder holds ("GBA", "PS", NextUI's "Game Boy (GB)"); "" for any other name.
// A "(USA)" or "(Disc 1)" is no console tag.
func companionDropDirConsoleNamed(folder string) string {
	folder = strings.TrimSpace(folder)
	if s, ok := romSystemAliases[strings.ToLower(folder)]; ok {
		return s.Name
	}
	if _, tag, ok := nextuiFolderTag(folder); ok {
		if s, found := nextuiTagSystem(tag); found {
			return s.Name
		}
	}
	return ""
}

// companionDropDirFilePlain is a single file's PhoneDrop answer, for
// companionDropGameTo: only its note is read.
func companionDropDirFilePlain(note string) companionDropPlan {
	return companionDropPlan{Kind: companionDropPlainKind, Note: note}
}

// companionDropDirGames files a folder of games of the library console
// system under Roms: a folder inside the console's folder, or the console's
// folder itself when the folder is named after it.
func companionDropDirGames(env companionDropDirEnv, name, system string, plain func(string) companionDropDirPlan) companionDropDirPlan {
	firmware := ""
	for _, s := range rommSystems {
		if s.Name == system {
			firmware = s.Folder
		}
	}
	p := companionDropGameTo(env.companionDropEnv, name, system, firmware, companionDropDirFilePlain)
	if p.Kind != companionDropGameKind {
		return plain(p.Note)
	}
	dir := companionDropDirUnder(p.Dir, name)
	if named := companionDropDirConsoleNamed(name); named != "" && rommSameConsole(named, system) {
		dir = p.Dir
	}
	return companionDropDirPlan{Kind: companionDropGameKind, Dir: dir, Label: system, Note: p.Note, System: system}
}

// companionDropDirShelf files a DOS or ScummVM game folder where its shelf
// looks: a folder inside the console's folder under Roms (made with the
// name firmware when the card has none), or that folder itself when the
// folder is named after it (a folder of game folders).
func companionDropDirShelf(env companionDropDirEnv, name, system, firmware, kind string, plain func(string) companionDropDirPlan) companionDropDirPlan {
	p := companionDropGameTo(env.companionDropEnv, name, system, firmware, companionDropDirFilePlain)
	if p.Kind != companionDropGameKind {
		return plain(p.Note)
	}
	dir := companionDropDirUnder(p.Dir, name)
	if companionDropDirConsoleNamed(name) == system {
		dir = p.Dir
	}
	note := "The DOS shelf lists it by its start program."
	if kind == companionDropScummVMKind {
		note = "The ScummVM shelf names it from its files."
	}
	if p.Note != "" {
		note = p.Note + ". " + note
	}
	return companionDropDirPlan{Kind: kind, Dir: dir, Label: system + " game", Note: note, System: system}
}

// companionDropDirByFile files a disc game, a folder of zips or of .bin
// cartridges by its console: the one the folder's name says, else the one
// the representative file shows through the single-file route (asking for
// its first or last bytes), else asked.
func companionDropDirByFile(in companionDropDirIn, env companionDropDirEnv, f companionDropDirFacts, plain func(string) companionDropDirPlan) companionDropDirPlan {
	if f.console != "" {
		return companionDropDirGames(env, in.Name, f.console, plain)
	}
	if f.rep < 0 {
		return companionDropDirAskConsole(nil)
	}
	rep := in.Files[f.rep]
	p := companionDropRoute(companionDropIn{Name: path.Base(rep.Rel), Size: rep.Size, Head: in.Head, Tail: in.Tail}, env.companionDropEnv)
	switch p.Kind {
	case companionDropNeedKind:
		return companionDropDirPlan{Kind: companionDropNeedKind, Need: p.Need, NeedFile: f.rep, NeedBytes: p.NeedBytes}
	case companionDropAskKind:
		return companionDropDirAskConsole(p.Choices)
	case companionDropGameKind:
		return companionDropDirGames(env, in.Name, p.System, plain)
	case companionDropPortKind:
		pp := plain(p.Note)
		pp.Kind, pp.Label = companionDropPortKind, p.Label
		return pp
	}
	if p.Note == "" && f.disc {
		// A disc set the single-file route has no answer for (an .mds sheet).
		return companionDropDirAskConsole(nil)
	}
	return plain(p.Note)
}

// companionDropDirAskConsole asks which console a folder of games is for:
// choices (every console when nil), and DOS, whose games come as zips and
// disc images too.
func companionDropDirAskConsole(choices []companionDropChoice) companionDropDirPlan {
	if choices == nil {
		for _, s := range rommSystems {
			choices = append(choices, companionDropChoice{ID: s.ID, Label: s.Name})
		}
	}
	choices = append(append([]companionDropChoice(nil), choices...), companionDropChoice{ID: companionDropDOSKind, Label: "DOS"})
	return companionDropDirPlan{Kind: companionDropAskKind, Choices: choices, Note: "Which console are the games in this folder for?"}
}

// companionDropDirCarts files a folder of cartridge games by their console,
// when they are all one console's (Game Boy and Game Boy Color count as one);
// a folder of several consoles' games is asked about with PhoneDrop alone.
func companionDropDirCarts(env companionDropDirEnv, name string, f companionDropDirFacts, plain func(string) companionDropDirPlan) companionDropDirPlan {
	var systems []string
	for s := range f.cartSystems {
		systems = append(systems, s)
	}
	sort.Strings(systems)
	if len(systems) == 2 && rommSameConsole(systems[0], systems[1]) {
		systems = systems[:1]
	}
	if len(systems) == 1 {
		return companionDropDirGames(env, name, systems[0], plain)
	}
	return companionDropDirPlan{Kind: companionDropAskKind,
		Note: "It holds games of " + strings.Join(systems, ", ") + ": send each console's games on their own, or put the folder in PhoneDrop."}
}

// companionDropDirMedia files a folder by its media: songs, videos or books,
// whichever there are most of, or pictures when there are more of them than
// of that (an album's or a book's covers are fewer). A close second kind (at
// least half as many, and two or more) asks; a .txt or .pdf beside songs or
// videos is a booklet, not a rival book.
func companionDropDirMedia(env companionDropDirEnv, name string, f companionDropDirFacts, plain func(string) companionDropDirPlan) companionDropDirPlan {
	best, bestN := companionDropMusicKind, f.audio
	if f.video > bestN {
		best, bestN = companionDropVideoKind, f.video
	}
	if f.books > bestN {
		best, bestN = companionDropBooksKind, f.books
	}
	if f.pictures > bestN {
		best, bestN = companionDropPicturesKind, f.pictures
	}
	if bestN == 0 {
		return plain("")
	}
	rivals := []struct {
		kind, label string
		n           int
	}{
		{companionDropMusicKind, "Music", f.audio},
		{companionDropVideoKind, "Videos", f.video},
		{companionDropBooksKind, "Books", f.booksStrong},
		{companionDropPicturesKind, "Pictures", 0},
	}
	var choices []companionDropChoice
	for _, r := range rivals {
		if r.kind == best {
			choices = append([]companionDropChoice{{ID: r.kind, Label: r.label}}, choices...)
			continue
		}
		if r.n >= 2 && r.n*2 >= bestN {
			choices = append(choices, companionDropChoice{ID: r.kind, Label: r.label})
		}
	}
	if len(choices) > 1 {
		return companionDropDirPlan{Kind: companionDropAskKind, Choices: choices, Note: "What is in this folder?"}
	}
	return companionDropDirMediaTo(env, name, best, plain)
}

// companionDropDirMediaTo files a folder into the media folder of kind: a
// folder inside it, or the folder itself when the folder is named like it.
func companionDropDirMediaTo(env companionDropDirEnv, name, kind string, plain func(string) companionDropDirPlan) companionDropDirPlan {
	base, label := "", ""
	switch kind {
	case companionDropBooksKind:
		base, label = env.Books, "Books"
	case companionDropMusicKind:
		base, label = env.Music, "Music"
	case companionDropVideoKind:
		base, label = env.Video, "Videos"
	case companionDropPicturesKind:
		base, label = env.Pictures, "Pictures"
	default:
		return plain("")
	}
	if base == "" {
		return plain(label + " has no shared folder on this device.")
	}
	dir := companionDropDirUnder(base, name)
	if strings.EqualFold(path.Base(base), name) {
		dir = base
	}
	return companionDropDirPlan{Kind: kind, Dir: dir, Label: label}
}

// companionDropDirChosen files a folder as the player picked: a media kind,
// DOS or ScummVM, a console, or PhoneDrop.
func companionDropDirChosen(in companionDropDirIn, env companionDropDirEnv, plain func(string) companionDropDirPlan) companionDropDirPlan {
	switch in.As {
	case companionDropBooksKind, companionDropMusicKind, companionDropVideoKind, companionDropPicturesKind:
		return companionDropDirMediaTo(env, in.Name, in.As, plain)
	case companionDropDOSKind:
		return companionDropDirShelf(env, in.Name, "DOS", "DOS", companionDropDOSKind, plain)
	case companionDropScummVMKind:
		return companionDropDirShelf(env, in.Name, "ScummVM", "SCUMMVM", companionDropScummVMKind, plain)
	case companionDropPlainKind:
		return plain("")
	}
	if sys, ok := rommSystemByID(in.As); ok {
		return companionDropDirGames(env, in.Name, sys.Name, plain)
	}
	if companionDropKnownSystem(in.As) {
		return companionDropDirGames(env, in.Name, in.As, plain)
	}
	return plain("That choice is not known here.")
}

// companionDropDirCounted marks the files a folder of kind is counted by in
// the handheld's line: its books, songs, videos or pictures; a game folder's
// games (its playlists, else its sheets, else its disc images, else its
// cartridges and zips). Other kinds count no files.
func companionDropDirCounted(files []companionDropDirEntry, kind string) []bool {
	out := make([]bool, len(files))
	m3u, sheet, image := false, false, false
	for _, e := range files {
		ext := strings.ToLower(path.Ext(e.Rel))
		if e.Rel == "" {
			continue
		}
		m3u = m3u || ext == ".m3u"
		sheet = sheet || (companionDropDirSheetExts[ext] && ext != ".m3u")
		image = image || companionDropDirImageExts[ext]
	}
	for i, e := range files {
		if e.Rel == "" {
			continue
		}
		base := strings.ToLower(path.Base(e.Rel))
		ext := path.Ext(base)
		switch kind {
		case companionDropBooksKind:
			out[i] = companionDropBookExts[ext]
		case companionDropMusicKind:
			out[i] = isAudioFile(base)
		case companionDropVideoKind:
			out[i] = mlibVideoExts[ext]
		case companionDropPicturesKind:
			out[i] = companionDropPictureExts[ext]
		case companionDropGameKind:
			switch {
			case m3u:
				out[i] = ext == ".m3u"
			case sheet:
				out[i] = companionDropDirSheetExts[ext]
			case image:
				out[i] = companionDropDirImageExts[ext]
			default:
				out[i] = ext == ".zip" || ext == ".7z" || companionDropDirTrackExts[ext] ||
					companionDropDirCartSystem(ext) != "" || strings.HasSuffix(base, ".p8.png")
			}
		}
	}
	return out
}

// ── The handheld's side ─────────────────────────────────────────────────

// companionDropDirFolder is a folder the page is sending.
type companionDropDirFolder struct {
	name  string
	batch string
	files []companionDropDirEntry
	plain bool
	as    string
	need  int // the file whose bytes the last answer asked for, -1
	// Set when the plan is final.
	final   bool
	kind    string
	label   string
	dest    string // share path
	counted []bool
	arrived []bool
	sent    int // files arrived
	count   int // of them, the ones counted (books, songs …)
	made    time.Time
	at      time.Time
}

// companionDropDirs are the folders being sent, by token. Any goroutine.
var companionDropDirs struct {
	sync.Mutex
	folders map[string]*companionDropDirFolder
}

// companionDropDirDone is a sent folder for the handheld's line.
type companionDropDirDone struct {
	Name  string
	Kind  string
	Dest  string
	Count int
	Files int
	made  time.Time
}

// handleDropFolder answers POST /api/drop/folder: a listing as JSON starts a
// folder; ?id=…[&part=head|tail][&as=…][&to=drop] asks again about it.
func (s *companionServer) handleDropFolder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	id := q.Get("id")
	if id == "" {
		folder, ok := companionDropDirRead(w, r)
		if !ok {
			return
		}
		token, err := companionDropDirKeep(folder)
		if err != nil {
			http.Error(w, "The handheld could not keep the plan.", http.StatusInternalServerError)
			return
		}
		id = token
	}
	companionDropDirs.Lock()
	folder := companionDropDirFind(id)
	if folder == nil || folder.final {
		companionDropDirs.Unlock()
		http.Error(w, "The handheld no longer knows this folder. Send it again.", http.StatusGone)
		return
	}
	if q.Get("to") == companionDropPlainKind {
		folder.plain = true
	}
	if as := strings.TrimSpace(q.Get("as")); as != "" {
		folder.as = as
	}
	need := folder.need
	in := companionDropDirIn{Name: folder.name, Files: append([]companionDropDirEntry(nil), folder.files...), As: folder.as, Plain: folder.plain}
	companionDropDirs.Unlock()
	if part := q.Get("part"); part == "head" || part == "tail" {
		if need < 0 {
			http.Error(w, "No file's bytes were asked for.", http.StatusBadRequest)
			return
		}
		limit := int64(companionDropHeadBytes)
		if part == "tail" {
			limit = companionDropTailBytes
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
		if err != nil {
			http.Error(w, "upload interrupted", http.StatusBadRequest)
			return
		}
		if int64(len(data)) > limit {
			http.Error(w, "too many bytes", http.StatusRequestEntityTooLarge)
			return
		}
		if data == nil {
			data = []byte{}
		}
		if part == "head" {
			in.Head = data
		} else {
			in.Tail = data
		}
	}
	// The media folder a plan may name is made before the shares are opened,
	// since a share is a folder that exists: decided once against a card
	// where every folder is there, to learn which.
	probe := companionDropDirRoute(in, companionDropDirEnv{
		companionDropEnv: companionDropEnv{Roms: "/Roms", Music: "/Music", Video: "/Videos", Drop: "/PhoneDrop"},
		Books:            "/Books", Pictures: "/Pictures",
	})
	dropDir := s.companionDropDirPrepare(probe.Kind)
	set, ok := openCompanionFileShares(w)
	if !ok {
		return
	}
	defer set.close()
	env := companionDropDirEnvFor(set, s.root, dropDir)
	plan := companionDropDirRoute(in, env)
	switch plan.Kind {
	case companionDropNeedKind:
		companionDropDirs.Lock()
		if f := companionDropDirFind(id); f != nil {
			f.need = plan.NeedFile
		}
		companionDropDirs.Unlock()
		writeCompanionJSON(w, map[string]interface{}{"kind": plan.Kind, "id": id, "need": plan.Need, "bytes": plan.NeedBytes, "file": plan.NeedFile})
		return
	case companionDropAskKind:
		writeCompanionJSON(w, map[string]interface{}{"kind": plan.Kind, "id": id, "pick": "as", "choices": plan.Choices, "note": plan.Note})
		return
	}
	dest, err := companionDropDirTarget(set, plan.Dir)
	if err != nil && plan.Kind != companionDropPlainKind {
		logWarn("companion", "drop: the folder %s cannot go to %s: %v", in.Name, plan.Dir, err)
		plan = companionDropDirPlan{Kind: companionDropPlainKind, Dir: companionDropDirUnder(env.Drop, in.Name), Label: "PhoneDrop",
			Note: "Its folder cannot be written from the phone, so it waits in PhoneDrop."}
		dest, err = companionDropDirTarget(set, plan.Dir)
	}
	if err != nil {
		logWarn("companion", "drop: PhoneDrop is not available for the folder %s: %v", in.Name, err)
		http.Error(w, "The PhoneDrop folder cannot be written.", http.StatusServiceUnavailable)
		return
	}
	// Stat, not Lstat: a console or media folder may be a link to a folder
	// inside the share (os.Root refuses one that leads out).
	if info, err := dest.share.root.Stat(dest.rel); err == nil && !info.IsDir() {
		http.Error(w, "A file named "+path.Base(dest.virtual)+" is already in "+path.Dir(dest.display())+".", http.StatusConflict)
		return
	}
	exists, taken, bytes := companionDropDirLook(dest, in.Files)
	if free, err := filesFreeSpace(dest.share.dir); err == nil && free < bytes+companionDropDirMargin {
		http.Error(w, in.Name+" needs "+humanBytes(bytes)+" and the card has "+humanBytes(free)+" free.", http.StatusInsufficientStorage)
		return
	}
	counted := companionDropDirCounted(in.Files, plan.Kind)
	companionDropDirs.Lock()
	folder = companionDropDirFind(id)
	if folder == nil || folder.final {
		companionDropDirs.Unlock()
		http.Error(w, "The handheld no longer knows this folder. Send it again.", http.StatusGone)
		return
	}
	for _, i := range taken {
		folder.files[i].Rel, folder.files[i].Why = "", "a folder of that name is already there"
	}
	folder.final, folder.kind, folder.label, folder.dest = true, plan.Kind, plan.Label, dest.virtual
	folder.counted, folder.arrived = counted, make([]bool, len(folder.files))
	var skips []companionDropDirSkip
	kept := 0
	for i, e := range folder.files {
		if e.Rel == "" {
			skips = append(skips, companionDropDirSkip{I: i, Why: e.Why, Quiet: e.Quiet})
			continue
		}
		kept++
	}
	companionDropDirs.Unlock()
	if exists == nil {
		exists = []int{}
	}
	writeCompanionJSON(w, map[string]interface{}{
		"kind":   plan.Kind,
		"id":     id,
		"dest":   dest.virtual,
		"label":  plan.Label,
		"note":   plan.Note,
		"system": companionDropSystemParam(plan.System),
		"exists": exists,
		"skip":   skips,
		"files":  kept,
		"bytes":  bytes,
	})
}

// companionDropDirSkip is a listed file the plan leaves out, for the page.
type companionDropDirSkip struct {
	I     int    `json:"i"`
	Why   string `json:"why"`
	Quiet bool   `json:"quiet,omitempty"`
}

// handleDropFolderFile takes POST /api/drop/folder/file?id=…&i=…[&replace=1]
// with one multipart "file" part: file i of the folder's listing, stored at
// its path under the plan's folder.
func (s *companionServer) handleDropFolderFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	id := q.Get("id")
	i, err := strconv.Atoi(q.Get("i"))
	if err != nil {
		http.Error(w, "bad file number", http.StatusBadRequest)
		return
	}
	rel, size, destV, ok := companionDropDirFileFor(id, i)
	if !ok {
		http.Error(w, "The handheld no longer knows where this folder goes. Send it again.", http.StatusGone)
		return
	}
	limit := size + 64<<10
	if limit > companionMaxUpload {
		limit = companionMaxUpload
	}
	if r.ContentLength > limit {
		http.Error(w, "upload too large", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	set, ok := openCompanionFileShares(w)
	if !ok {
		return
	}
	defer set.close()
	dirV := destV
	if d := path.Dir(rel); d != "." {
		dirV += "/" + d
	}
	dir, ok := companionFilesResolve(w, set, dirV)
	if !ok {
		return
	}
	if dir.virtual != destV && !strings.HasPrefix(dir.virtual, destV+"/") {
		http.Error(w, companionFilesBadPathMsg, http.StatusBadRequest)
		return
	}
	if dir.isRoot() || dir.protected || companionFilesAliasProtected(dir) {
		http.Error(w, companionFilesReadOnlyMsg, http.StatusForbidden)
		return
	}
	if dir.rel != "." {
		if err := dir.share.root.MkdirAll(dir.rel, 0o755); err != nil {
			logWarn("companion", "drop: cannot make %s: %v", dir.display(), err)
			http.Error(w, "Cannot create the folder "+dir.display()+".", http.StatusInternalServerError)
			return
		}
	}
	file, ok := companionFilesFilePart(w, r)
	if !ok {
		return
	}
	defer file.Close()
	target, written, ok := companionFilesStore(w, set, dir, path.Base(rel), file, q.Get("replace") == "1")
	if !ok {
		return
	}
	switch companionDropDirArrived(id, i) {
	case companionDropGameKind, companionDropDOSKind, companionDropScummVMKind:
		// The covers' list of games is stale now (companionDropLibrary).
		companionDropArrived("", companionDropTicket{kind: companionDropGameKind})
	}
	logDebug("companion", "drop: %s (%d bytes)", target.display(), written)
	trophyEvent(tkFileTransfer)
	writeCompanionJSON(w, map[string]interface{}{"ok": true, "dest": target.virtual, "size": written})
}

// companionDropDirRead reads a new folder's listing, answering the request
// when it cannot.
func companionDropDirRead(w http.ResponseWriter, r *http.Request) (*companionDropDirFolder, bool) {
	var body struct {
		Name  string                 `json:"name"`
		Batch string                 `json:"batch"`
		To    string                 `json:"to"`
		Files []companionDropDirFile `json:"files"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, companionDropDirMaxBody)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "The folder's list is too long: send its folders one at a time.", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "bad folder list", http.StatusBadRequest)
		}
		return nil, false
	}
	if len(body.Files) == 0 {
		http.Error(w, "The folder is empty.", http.StatusBadRequest)
		return nil, false
	}
	if len(body.Files) > companionDropDirMaxFiles {
		http.Error(w, "More than "+strconv.Itoa(companionDropDirMaxFiles)+" files: send its folders one at a time.", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	files := companionDropDirCheck(body.Files)
	why := ""
	for _, e := range files {
		if e.Rel != "" {
			return &companionDropDirFolder{name: companionDropDirName(body.Name), batch: companionDropBatchID(body.Batch),
				files: files, plain: body.To == companionDropPlainKind, need: -1}, true
		}
		if why == "" && !e.Quiet {
			why = ": " + e.Why
		}
	}
	http.Error(w, "Nothing in this folder can be sent"+why+".", http.StatusBadRequest)
	return nil, false
}

// companionDropDirPrepare makes PhoneDrop, and the media folder a folder of
// kind goes in when the card has none (as companionDropPrepare does for a
// file). It returns PhoneDrop's folder, "" when it cannot be made.
func (s *companionServer) companionDropDirPrepare(kind string) string {
	drop, err := companionDropDirFor(s.root)
	if err != nil {
		logWarn("companion", "drop: cannot make PhoneDrop: %v", err)
		drop = ""
	}
	switch kind {
	case companionDropMusicKind:
		companionDropEnsureMedia([]string{"Music", "Media"}, "Music", downloadsMusicDir())
	case companionDropVideoKind:
		companionDropEnsureMedia([]string{"Videos", "Movies", "TV", "Media"}, "Videos", downloadsVideoDir())
	case companionDropBooksKind:
		companionDropDirEnsureCard(s.root, companionDropBooksDirs)
	case companionDropPicturesKind:
		companionDropDirEnsureCard(s.root, companionDropPicturesDirs)
	}
	return drop
}

// companionDropDirCardDir is the folder right under root named one of names
// (any case, in that order), "" when there is none.
func companionDropDirCardDir(root string, names []string) string {
	if strings.TrimSpace(root) == "" {
		return ""
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, want := range names {
		for _, e := range entries {
			if e.IsDir() && strings.EqualFold(e.Name(), want) {
				return filepath.Join(root, e.Name())
			}
		}
	}
	return ""
}

// companionDropDirEnsureCard makes names[0] under root when none of names is
// there: Books or Pictures on the card (on a PC, in the shared folder).
func companionDropDirEnsureCard(root string, names []string) {
	if strings.TrimSpace(root) == "" || companionDropDirCardDir(root, names) != "" {
		return
	}
	dir := filepath.Join(root, names[0])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logWarn("companion", "drop: cannot make %s: %v", dir, err)
	}
}

// companionDropDirEnvFor is the card in share paths for a folder.
func companionDropDirEnvFor(set *ftpShareSet, root, dropDir string) companionDropDirEnv {
	base, _ := companionDropEnvFor(set, dropDir, false)
	env := companionDropDirEnv{companionDropEnv: base}
	if dir := companionDropDirCardDir(root, companionDropBooksDirs); dir != "" {
		env.Books, _ = companionDropShared(set, dir)
	}
	if dir := companionDropDirCardDir(root, companionDropPicturesDirs); dir != "" {
		env.Pictures, _ = companionDropShared(set, dir)
	}
	return env
}

// companionDropDirTarget checks a plan's folder against the shares: inside a
// share and writable. It need not exist yet.
func companionDropDirTarget(set *ftpShareSet, dir string) (ftpPath, error) {
	if dir == "" {
		return ftpPath{}, errors.New("no folder")
	}
	p, err := set.resolvePath("/", dir)
	if err != nil {
		return ftpPath{}, err
	}
	if p.isRoot() || p.protected || companionFilesAliasProtected(p) {
		return ftpPath{}, errors.New("read-only folder")
	}
	return p, nil
}

// companionDropDirLook finds which listed files are already in dest (asked
// before they are replaced) and which have a folder in their place (left
// out), and the bytes the folder writes.
func companionDropDirLook(dest ftpPath, files []companionDropDirEntry) (exists, taken []int, bytes int64) {
	root := dest.share.root
	there := dest.rel == "."
	if !there {
		info, err := root.Stat(dest.rel)
		there = err == nil && info.IsDir()
	}
	for i, e := range files {
		if e.Rel == "" {
			continue
		}
		if there {
			if info, err := root.Lstat(filepath.Join(dest.rel, filepath.FromSlash(e.Rel))); err == nil {
				if info.IsDir() {
					taken = append(taken, i)
					continue
				}
				exists = append(exists, i)
			}
		}
		bytes += e.Size
	}
	return exists, taken, bytes
}

// companionDropDirKeep keeps a new folder and returns its token: an expired
// folder goes first, then the oldest when there are too many.
func companionDropDirKeep(f *companionDropDirFolder) (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	now := time.Now()
	f.made, f.at = now, now
	companionDropDirs.Lock()
	defer companionDropDirs.Unlock()
	if companionDropDirs.folders == nil {
		companionDropDirs.folders = make(map[string]*companionDropDirFolder)
	}
	oldest := ""
	for k, v := range companionDropDirs.folders {
		if now.Sub(v.at) > companionDropTicketTTL {
			delete(companionDropDirs.folders, k)
			continue
		}
		if oldest == "" || v.at.Before(companionDropDirs.folders[oldest].at) {
			oldest = k
		}
	}
	if len(companionDropDirs.folders) >= companionDropDirMaxFolders && oldest != "" {
		delete(companionDropDirs.folders, oldest)
	}
	companionDropDirs.folders[token] = f
	return token, nil
}

// companionDropDirFind is the folder of token while it has not expired; the
// caller holds companionDropDirs.
func companionDropDirFind(token string) *companionDropDirFolder {
	f := companionDropDirs.folders[token]
	if f == nil || token == "" {
		return nil
	}
	if time.Since(f.at) > companionDropTicketTTL {
		delete(companionDropDirs.folders, token)
		return nil
	}
	return f
}

// companionDropDirFileFor is file i of a planned folder: its path, size and
// the folder's share path.
func companionDropDirFileFor(token string, i int) (rel string, size int64, dest string, ok bool) {
	companionDropDirs.Lock()
	defer companionDropDirs.Unlock()
	f := companionDropDirFind(token)
	if f == nil || !f.final || i < 0 || i >= len(f.files) || f.files[i].Rel == "" {
		return "", 0, "", false
	}
	f.at = time.Now()
	return f.files[i].Rel, f.files[i].Size, f.dest, true
}

// companionDropDirArrived counts file i of a folder as arrived (once, however
// often it is sent) and returns the folder's kind.
func companionDropDirArrived(token string, i int) string {
	companionDropDirs.Lock()
	defer companionDropDirs.Unlock()
	// Not companionDropDirFind: the folder was known when this file began,
	// and a file that took longer than the TTL to arrive (4 GB over slow
	// Wi-Fi) still counts and keeps the folder for the next one.
	f := companionDropDirs.folders[token]
	if f == nil || token == "" || i < 0 || i >= len(f.arrived) {
		return ""
	}
	f.at = time.Now()
	if !f.arrived[i] {
		f.arrived[i] = true
		f.sent++
		if f.counted[i] {
			f.count++
		}
	}
	return f.kind
}

// companionDropDirTakeBatch returns the folders a batch sent, in the order
// they were started, and the files that arrived by kind (a DOS or ScummVM
// folder's as games), and forgets the batch's folders.
func companionDropDirTakeBatch(batch string) ([]companionDropDirDone, map[string]int) {
	if batch == "" {
		return nil, nil
	}
	companionDropDirs.Lock()
	defer companionDropDirs.Unlock()
	var done []companionDropDirDone
	kinds := map[string]int{}
	for k, f := range companionDropDirs.folders {
		if f.batch != batch {
			continue
		}
		delete(companionDropDirs.folders, k)
		if f.sent == 0 {
			continue
		}
		done = append(done, companionDropDirDone{Name: f.name, Kind: f.kind, Dest: f.dest, Count: f.count, Files: f.sent, made: f.made})
		kind := f.kind
		if kind == companionDropDOSKind || kind == companionDropScummVMKind {
			kind = companionDropGameKind
		}
		kinds[kind] += f.sent
	}
	sort.Slice(done, func(i, j int) bool { return done[i].made.Before(done[j].made) })
	return done, kinds
}

// companionDropDirPhrase is one sent folder in words: "Asimov (42 books)".
func companionDropDirPhrase(d companionDropDirDone) string {
	what := pluralizeCount(d.Files, "file", "files")
	nouns := map[string][2]string{
		companionDropBooksKind:    {"book", "books"},
		companionDropMusicKind:    {"song", "songs"},
		companionDropVideoKind:    {"video", "videos"},
		companionDropPicturesKind: {"picture", "pictures"},
		companionDropGameKind:     {"game", "games"},
	}
	switch {
	case d.Count > 0 && nouns[d.Kind][0] != "":
		what = pluralizeCount(d.Count, nouns[d.Kind][0], nouns[d.Kind][1])
	case d.Kind == companionDropDOSKind:
		what = "a DOS game"
	case d.Kind == companionDropScummVMKind:
		what = "a ScummVM game"
	case d.Kind == companionDropPortKind:
		what = "a PortMaster port"
	}
	return d.Name + " (" + what + ")"
}

// companionDropDirSummary adds the folders a batch sent to the line for its
// files (companionDropSummary): "the folder Asimov (42 books)", "3 games and
// the folders Asimov (42 books) and Abbey Road (17 songs)".
func companionDropDirSummary(files string, folders []companionDropDirDone) string {
	if len(folders) == 0 {
		return files
	}
	phrases := make([]string, 0, len(folders))
	for _, d := range folders {
		phrases = append(phrases, companionDropDirPhrase(d))
	}
	what := "the folder " + phrases[0]
	if n := len(phrases); n > 1 {
		what = "the folders " + strings.Join(phrases[:n-1], ", ") + " and " + phrases[n-1]
	}
	switch {
	case files == "":
		return what
	case strings.Contains(files, " and "):
		return files + ", and " + what
	}
	return files + " and " + what
}

// companionDropUpperFirst is s with its first letter in upper case, for the
// page's sentence.
func companionDropUpperFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}

// companionDropDirLog writes one log line per folder a batch sent.
func companionDropDirLog(folders []companionDropDirDone) {
	for _, d := range folders {
		logInfo("companion", "drop: the folder %s (%s) to %s, %d files", d.Name, d.Kind, strings.TrimPrefix(d.Dest, "/"), d.Files)
	}
}
