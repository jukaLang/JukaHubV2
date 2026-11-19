package main

// companion_drop_route.go — where a file the phone drops belongs.
//
// The Phone Companion's drop card (companion_drop.go) sends each file's name
// and size first, and the handheld answers where it goes: a game into its
// console's folder under Roms, a cover beside the game it shows, a song into
// the folder the media library reads, a firmware file into BIOS, anything
// else into PhoneDrop. This file is that decision, kept free of the disk so
// the table test can feed it any card:
//
//   - games by the ROM manager's tables (romm_detect.go): an extension only
//     one console uses, else the file's own header (the page sends the first
//     bytes when they are needed), else a .zip's directory (the page sends the
//     last bytes); the folder is the one the library scans for that console
//     on this card (romFolderSystem: "GB", NextUI's "Game Boy (GB)" …), or
//     the firmware's name for it when the card has none yet;
//   - a container nothing can see into (.7z, .chd, a .cue without its tracks)
//     comes back as "ask", with the consoles it could be for: the page shows a
//     picker and asks again with the console chosen, before any byte is sent;
//   - covers by the game's file name: the same name, then the same title once
//     tags like "(USA)" are set aside; otherwise the closest titles (the cover
//     matcher's score, artwork_closest.go) are offered to choose from;
//   - firmware by the names the ROM manager and the BIOS doctor know
//     (rommBIOSFor, gameDiagBIOSTable);
//   - PortMaster ports are recognised, but wait in PhoneDrop: they run
//     programs, and the folders that hold those are never written from the
//     network (ftp_server.go ftpShareNames).
//
// Every folder here is a share path ("/Roms/GBA"); companion_drop.go maps the
// device's folders onto the shares and checks the answer against them.

import (
	"bytes"
	"encoding/binary"
	"path"
	"sort"
	"strings"
)

// The kinds of answer.
const (
	companionDropGameKind  = "game"
	companionDropCoverKind = "cover"
	companionDropMusicKind = "music"
	companionDropVideoKind = "video"
	companionDropBIOSKind  = "bios"
	companionDropPortKind  = "port"
	companionDropPlainKind = "drop"
	companionDropAskKind   = "ask"
	companionDropNeedKind  = "need"
)

const (
	// companionDropHeadBytes is how much of a file's start the page sends
	// when its header decides the console: a cartridge header, or a disc's
	// first sectors (what rommDetectZip reads of a disc in a zip).
	companionDropHeadBytes = 192 << 10
	// companionDropTailBytes is how much of a .zip's end the page sends: its
	// directory, which lists every file inside.
	companionDropTailBytes = 256 << 10
	// companionDropZipMaxNames bounds the names read from one directory.
	companionDropZipMaxNames = 20000
	// companionDropCDMax is the most a 2048-byte-sector CD image holds (80
	// minutes); a larger PlayStation image is a PlayStation 2 DVD.
	companionDropCDMax = 360000 * 2048
	// companionDropCoverChoices is how many games a cover with no exact
	// match offers.
	companionDropCoverChoices = 6
)

// companionDropIn is one file the page asks about. Name is already cleaned
// (sanitizeUploadName).
type companionDropIn struct {
	Name   string
	Size   int64
	Head   []byte // the file's first bytes, when the page sent them
	Tail   []byte // the file's last bytes, when the page sent them
	System string // a console the player picked: a rommSystems id or a library name
	Game   string // the game the player picked for a cover (its share path)
	Plain  bool   // the player asked for PhoneDrop
}

// companionDropGame is one library game a cover may belong to.
type companionDropGame struct {
	Path   string // share path of the game's file: "/Roms/GB/Pokemon Red.gb"
	Stem   string // its name for covers (romGameStem)
	System string // the library's console name
}

// companionDropEnv is the card as the decision sees it, in share paths; ""
// for a folder that is not shared on this device.
type companionDropEnv struct {
	Roms     string
	RomsDirs []string // folder names right under Roms
	BIOS     string
	Music    string
	Video    string
	Drop     string // PhoneDrop, always set
	NextUI   bool   // the card runs NextUI: its "Name (TAG)" folders are the consoles
	Games    []companionDropGame
}

// companionDropChoice is one option of an "ask" answer.
type companionDropChoice struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// companionDropPlan is the answer for one file. Need and NeedBytes ask the
// page for the file's "head" or "tail"; Pick says which parameter a choice
// is sent back as ("system" or "game").
type companionDropPlan struct {
	Kind      string
	Dir       string
	Name      string
	Label     string
	Note      string
	Need      string
	NeedBytes int
	Pick      string
	Choices   []companionDropChoice
	// System is the console a game was filed for (a library name); Game the
	// share path of the game a cover belongs to.
	System string
	Game   string
}

// companionDropExtraExts are cartridge types the library lists
// (romFileExtensions) for consoles the ROM manager does not file games for:
// they go into a folder the card already has for the console, never a new
// one, since the firmware's name for it is not known.
var companionDropExtraExts = map[string]string{
	".a26": "Atari 2600", ".a52": "Atari 5200", ".a78": "Atari 7800", ".lnx": "Atari Lynx",
	".ws": "WonderSwan", ".wsc": "WonderSwan Color", ".ngp": "Neo Geo Pocket", ".32x": "32X",
	".p8": "PICO-8",
}

// companionDropHeadExts are types whose header names the console.
var companionDropHeadExts = map[string]bool{
	".bin": true, ".iso": true, ".img": true, ".pbp": true, ".rom": true, ".md": true,
	".gcm": true, ".rvz": true, ".ciso": true,
}

// companionDropAskSystems are the consoles a type nothing could identify is
// offered for; a type not listed is offered every console.
var companionDropAskSystems = map[string][]string{
	".bin":  {"ps1", "md", "saturn", "dc"},
	".img":  {"ps1", "saturn"},
	".iso":  {"ps2", "psp", "ps1", "gc", "wii", "saturn", "dc", "xbox", "xbox360"},
	".chd":  {"ps1", "ps2", "saturn", "dc", "psp"},
	".cue":  {"ps1", "saturn", "pce"},
	".m3u":  {"ps1", "ps2", "saturn", "dc"},
	".pbp":  {"ps1", "psp"},
	".gcm":  {"gc", "wii"},
	".gcz":  {"gc", "wii"},
	".rvz":  {"gc", "wii"},
	".ciso": {"gc", "wii", "psp"},
}

// companionDropCoverExts are the pictures a cover may be.
var companionDropCoverExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true}

// companionDropRoute decides where one file goes.
func companionDropRoute(in companionDropIn, env companionDropEnv) companionDropPlan {
	name := in.Name
	lower := strings.ToLower(name)
	ext := strings.ToLower(path.Ext(name))
	plain := func(note string) companionDropPlan {
		return companionDropPlan{Kind: companionDropPlainKind, Dir: env.Drop, Name: name, Label: "PhoneDrop", Note: note}
	}
	if in.Plain {
		return plain("")
	}
	if in.System != "" {
		if sys, ok := rommSystemByID(in.System); ok {
			return companionDropGameTo(env, name, sys.Name, sys.Folder, plain)
		}
		if companionDropKnownSystem(in.System) {
			return companionDropGameTo(env, name, in.System, "", plain)
		}
		return plain("That console is not known here.")
	}
	isCover := companionDropCoverExts[ext] && !strings.HasSuffix(lower, ".p8.png")
	if isCover && in.Size > artworkImageMaxBytes {
		// The library reads no bigger picture (artworkLocalCover).
		return plain("Too big for a cover (over 8 MB), so it waits in PhoneDrop.")
	}
	if in.Game != "" && isCover {
		for _, g := range env.Games {
			if g.Path == in.Game {
				return companionDropCoverFor(env, g, ext)
			}
		}
		return plain("That game is no longer in the library.")
	}
	if b, ok := companionDropBIOSFor(name, in.Size); ok {
		if env.BIOS == "" {
			return plain("A " + b.System + " BIOS file, but the BIOS folder is not shared on this device.")
		}
		dir := env.BIOS
		if _, tag, ok := nextuiFolderTag(companionDropRomFolder(env, b.System)); env.NextUI && ok {
			// NextUI's paks read Bios/<TAG> (nextui.go): the tag of the
			// console's Roms folder.
			dir += "/" + tag
		} else if b.Sub != "" {
			dir += "/" + b.Sub
		}
		return companionDropPlan{Kind: companionDropBIOSKind, Dir: dir, Name: name, Label: b.System + " BIOS", System: b.System}
	}
	switch {
	case isAudioFile(name):
		if env.Music == "" {
			return plain("Music has no shared folder on this device.")
		}
		return companionDropPlan{Kind: companionDropMusicKind, Dir: env.Music, Name: name, Label: "Music"}
	case mlibVideoExts[ext]:
		if env.Video == "" {
			return plain("Videos have no shared folder on this device.")
		}
		return companionDropPlan{Kind: companionDropVideoKind, Dir: env.Video, Name: name, Label: "Videos"}
	case strings.HasSuffix(lower, ".p8.png"):
		return companionDropGameTo(env, name, companionDropExtraExts[".p8"], "", plain)
	case isCover:
		return companionDropCover(env, name, ext, plain)
	}
	if id, ok := rommExtSystems[ext]; ok {
		sys, _ := rommSystemByID(id)
		return companionDropGameTo(env, name, sys.Name, sys.Folder, plain)
	}
	if system, ok := companionDropExtraExts[ext]; ok {
		return companionDropGameTo(env, name, system, "", plain)
	}
	if ext == ".gdi" || ext == ".cdi" {
		sys, _ := rommSystemByID("dc")
		return companionDropGameTo(env, name, sys.Name, sys.Folder, plain)
	}
	ask := func(ids []string, note string) companionDropPlan {
		return companionDropAsk(name, ids, note)
	}
	switch {
	case ext == ".zip":
		if in.Tail == nil {
			return companionDropPlan{Kind: companionDropNeedKind, Name: name, Need: "tail", NeedBytes: companionDropTailBytes}
		}
		names, complete, ok := companionDropZipNames(in.Tail, in.Size)
		if !ok {
			return plain("This zip could not be read.")
		}
		if companionDropIsPort(names) {
			p := plain("A PortMaster port. Ports run programs, so it waits in PhoneDrop: install it with PortMaster on the handheld.")
			p.Kind, p.Label = companionDropPortKind, "PortMaster port"
			return p
		}
		if !complete {
			return ask(nil, "")
		}
		ids, sure := companionDropZipGame(names)
		if sure && len(ids) == 1 {
			sys, _ := rommSystemByID(ids[0])
			return companionDropGameTo(env, name, sys.Name, sys.Folder, plain)
		}
		return ask(ids, "")
	case companionDropHeadExts[ext]:
		if in.Head == nil {
			return companionDropPlan{Kind: companionDropNeedKind, Name: name, Need: "head", NeedBytes: companionDropHeadBytes}
		}
		id, ids := companionDropSniff(ext, in.Head, in.Size)
		if id != "" {
			sys, _ := rommSystemByID(id)
			return companionDropGameTo(env, name, sys.Name, sys.Folder, plain)
		}
		if ext == ".md" {
			return plain("") // a note, not a Mega Drive cartridge
		}
		return ask(ids, "")
	case ext == ".7z" || ext == ".chd" || ext == ".cue" || ext == ".m3u" || ext == ".gcz":
		return ask(nil, "")
	}
	return plain("")
}

// companionDropKnownSystem reports whether name is a console the library
// lists under some folder: an extra cartridge type's console.
func companionDropKnownSystem(name string) bool {
	for _, s := range companionDropExtraExts {
		if s == name {
			return true
		}
	}
	return false
}

// companionDropAsk is an "ask" answer offering the consoles ids (every
// console when ids is empty), in the plan's console order.
func companionDropAsk(name string, ids []string, note string) companionDropPlan {
	if len(ids) == 0 {
		ids = companionDropAskSystems[strings.ToLower(path.Ext(name))]
	}
	var choices []companionDropChoice
	for _, s := range rommSystems {
		if len(ids) > 0 && !companionDropHas(ids, s.ID) {
			continue
		}
		choices = append(choices, companionDropChoice{ID: s.ID, Label: s.Name})
	}
	sort.SliceStable(choices, func(i, j int) bool {
		return companionDropIndex(ids, choices[i].ID) < companionDropIndex(ids, choices[j].ID)
	})
	if note == "" {
		note = "Which console is it for?"
	}
	return companionDropPlan{Kind: companionDropAskKind, Name: name, Pick: "system", Choices: choices, Note: note}
}

func companionDropHas(list []string, s string) bool { return companionDropIndex(list, s) >= 0 }

// companionDropIndex is s's place in list; -1 when it is not there, and 0
// for every entry when list is empty (the consoles keep their own order).
func companionDropIndex(list []string, s string) int {
	if len(list) == 0 {
		return 0
	}
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}

// companionDropGameTo files a game of the library console system under Roms:
// the folder the library scans for it on this card, else a new folder named
// firmware (the TrimUI firmware's name for it) when one is known.
func companionDropGameTo(env companionDropEnv, name, system, firmware string, plain func(string) companionDropPlan) companionDropPlan {
	if env.Roms == "" {
		return plain("A " + system + " game, but the Roms folder is not shared on this device.")
	}
	folder := companionDropRomFolder(env, system)
	note := ""
	if folder == "" {
		// Game Boy and Game Boy Color games share their cores, and many cards
		// keep both in one folder (rommSameConsole).
		for _, other := range []string{"Game Boy", "Game Boy Color"} {
			if folder == "" && other != system && rommSameConsole(system, other) {
				folder = companionDropRomFolder(env, other)
			}
		}
	}
	if folder == "" {
		if firmware == "" {
			return plain("A " + system + " game, but the card has no " + system + " folder in Roms yet.")
		}
		folder = firmware
		note = "New folder Roms/" + firmware
	}
	return companionDropPlan{Kind: companionDropGameKind, Dir: env.Roms + "/" + folder, Name: name, Label: system, Note: note, System: system}
}

// companionDropRomFolder is the folder under Roms the library scans for
// system, "" when the card has none. It is chosen as scanROMLibrary reads the
// card: a NextUI "Name (TAG)" folder only counts on NextUI, where it is the
// one NextUI shows too; elsewhere the firmware's own name comes first.
func companionDropRomFolder(env companionDropEnv, system string) string {
	firmware := ""
	for _, s := range rommSystems {
		if s.Name == system {
			firmware = s.Folder
		}
	}
	dirs := append([]string(nil), env.RomsDirs...)
	sort.Strings(dirs)
	best, bestRank := "", 0
	for _, dir := range dirs {
		sys, tagged, ok := romFolderSystem(dir, nil)
		if !ok || sys.Name != system || (tagged && !env.NextUI) {
			continue
		}
		rank := 1
		switch {
		case tagged:
			rank = 3
		case firmware != "" && strings.EqualFold(dir, firmware):
			rank = 2
		}
		if rank > bestRank {
			best, bestRank = dir, rank
		}
	}
	return best
}

// companionDropBIOSFor reports a firmware file: by the ROM manager's table,
// which also checks the size where a name is shared with other files (a 2 MB
// "bios7.bin" is no DS BIOS), else by the BIOS doctor's names.
func companionDropBIOSFor(name string, size int64) (rommBIOS, bool) {
	if b, ok := rommBIOSFor(name, size); ok {
		return b, true
	}
	lower := strings.ToLower(path.Base(name))
	for _, b := range rommBIOSFiles {
		if b.Name == lower {
			return rommBIOS{}, false // the right name, the wrong size
		}
	}
	if strings.HasPrefix(lower, "scph") {
		return rommBIOS{}, false // rommBIOSFor knows the PlayStation sizes
	}
	for _, row := range gameDiagBIOSTable {
		for _, group := range row.Groups {
			for _, n := range group {
				if strings.Contains(n, ".") && strings.EqualFold(n, lower) {
					return rommBIOS{Name: lower, System: row.System, Sub: row.Sub}, true
				}
			}
		}
	}
	return rommBIOS{}, false
}

// companionDropCover files a picture as a game's cover when its name is the
// game's, else offers the games whose titles are closest.
func companionDropCover(env companionDropEnv, name, ext string, plain func(string) companionDropPlan) companionDropPlan {
	stem := strings.TrimSpace(strings.TrimSuffix(name, path.Ext(name)))
	if stem == "" || len(env.Games) == 0 {
		return plain("")
	}
	var same []companionDropGame
	for _, g := range env.Games {
		if strings.EqualFold(g.Stem, stem) {
			same = append(same, g)
		}
	}
	if len(same) == 0 {
		// The same title once tags and spacing are set aside:
		// "Pokemon Red (USA).png" for "Pokemon Red.gb".
		key := strings.Join(artworkMatchTokens(stem), " ")
		for _, g := range env.Games {
			if key != "" && strings.Join(artworkMatchTokens(g.Stem), " ") == key {
				same = append(same, g)
			}
		}
	}
	if len(same) == 1 {
		return companionDropCoverFor(env, same[0], ext)
	}
	cands := same
	if len(cands) == 0 {
		cands = companionDropClosestGames(stem, env.Games, companionDropCoverChoices)
	}
	if len(cands) == 0 {
		return plain("No game in the library has a name like this picture's.")
	}
	choices := make([]companionDropChoice, 0, len(cands))
	for _, g := range cands {
		choices = append(choices, companionDropChoice{ID: g.Path, Label: g.Stem + " (" + g.System + ")"})
	}
	return companionDropPlan{Kind: companionDropAskKind, Name: name, Pick: "game", Choices: choices,
		Note: "Which game is this cover for?"}
}

// companionDropCoverFor files the picture where the library reads a cover
// put on the card for g (romLocalCoverPath): the Imgs folder beside the game,
// as the TrimUI firmware and CrossMix keep them, or NextUI's .media folder.
func companionDropCoverFor(env companionDropEnv, g companionDropGame, ext string) companionDropPlan {
	sub := "Imgs"
	if env.NextUI {
		sub = ".media"
	}
	return companionDropPlan{Kind: companionDropCoverKind, Dir: path.Dir(g.Path) + "/" + sub, Name: g.Stem + ext,
		Label: "Cover of " + g.Stem, System: g.System, Game: g.Path}
}

// companionDropClosestGames ranks the games by how alike their titles are to
// stem, with the cover matcher's score, and keeps the best n that share
// something with it.
func companionDropClosestGames(stem string, games []companionDropGame, n int) []companionDropGame {
	want := artworkClosestWords(artworkMatchTokens(stem))
	if len(want) == 0 {
		return nil
	}
	type scored struct {
		g     companionDropGame
		score float64
	}
	var list []scored
	for _, g := range games {
		score, cover := artworkClosestScore(want, artworkClosestWords(artworkMatchTokens(g.Stem)))
		if cover <= 0 || score <= 0 {
			continue
		}
		list = append(list, scored{g, score})
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].score != list[j].score {
			return list[i].score > list[j].score
		}
		return strings.ToLower(list[i].g.Stem) < strings.ToLower(list[j].g.Stem)
	})
	out := make([]companionDropGame, 0, n)
	for i := 0; i < len(list) && i < n; i++ {
		out = append(out, list[i].g)
	}
	return out
}

// companionDropSniff identifies a file from its first bytes: a cartridge
// header, a disc's boot sector or volume descriptor, a PSP EBOOT. When the
// bytes narrow it down without settling it, ids are the consoles left.
func companionDropSniff(ext string, head []byte, size int64) (string, []string) {
	if id := rommSniffCart(head, size); id != "" {
		return id, nil
	}
	if ext == ".pbp" {
		if len(head) < 0x28 || !rommHasAt(head, 0, []byte("\x00PBP")) {
			return "", nil
		}
		// The PlayStation-or-PSP mark is at the start of DATA.PSAR, which may
		// lie beyond the bytes sent.
		if psar := int64(binary.LittleEndian.Uint32(head[0x24:0x28])); psar+10 <= int64(len(head)) {
			return rommSniffPBP(bytes.NewReader(head)), nil
		}
		return "", []string{"ps1", "psp"}
	}
	if !rommDiscExts[ext] && ext != ".img" {
		return "", nil
	}
	id := rommSniffDisc(bytes.NewReader(head), int64(len(head)))
	if id == "ps1" && rommDiscLayoutOf(head).sector == 2048 {
		// rommSniffDisc says PlayStation 2 only when it finds BOOT2 in
		// SYSTEM.CNF, which may lie beyond the bytes sent. A plain-sector
		// image larger than any CD is a DVD; a smaller one could be either.
		if size > companionDropCDMax {
			return "ps2", nil
		}
		return "", []string{"ps1", "ps2"}
	}
	return id, nil
}

// companionDropZipNames lists the names in a zip from its end: tail is the
// file's last bytes, size the whole file's. complete reports that the whole
// directory was in tail; otherwise names are the entries from the first
// directory record found in tail on. ok is false when tail ends in no zip
// directory at all.
func companionDropZipNames(tail []byte, size int64) (names []string, complete, ok bool) {
	const eocdLen = 22
	u16 := func(at int) int { return int(binary.LittleEndian.Uint16(tail[at:])) }
	u32 := func(at int) uint32 { return binary.LittleEndian.Uint32(tail[at:]) }
	eocd := -1
	for i := len(tail) - eocdLen; i >= 0 && len(tail)-i <= eocdLen+0xFFFF; i-- {
		if u32(i) == 0x06054b50 && i+eocdLen+u16(i+20) == len(tail) {
			eocd = i
			break
		}
	}
	if eocd < 0 {
		return nil, false, false
	}
	entries := uint64(u16(eocd + 10))
	cdSize := uint64(u32(eocd + 12))
	cdEnd := eocd
	// Zip64: the locator before the record points at the 64-bit record, which
	// then ends the directory. Some tools write one for small archives too,
	// with the plain record's counts filled in, so it is looked for always.
	if loc := eocd - 20; loc >= 0 && u32(loc) == 0x07064b50 {
		// The record's offset is the phone's word: compared without adding
		// to it, so a huge one cannot wrap round into the slice.
		at := int64(binary.LittleEndian.Uint64(tail[loc+8:])) - (size - int64(len(tail)))
		if at >= 0 && at <= int64(loc)-56 && u32(int(at)) == 0x06064b50 {
			entries = binary.LittleEndian.Uint64(tail[at+32:])
			cdSize = binary.LittleEndian.Uint64(tail[at+40:])
			cdEnd = int(at)
		}
	}
	start := -1
	if cdSize <= uint64(cdEnd) {
		start = cdEnd - int(cdSize)
		complete = true
	} else if i := bytes.Index(tail[:cdEnd], []byte("PK\x01\x02")); i >= 0 {
		start = i
	}
	if start < 0 {
		return nil, false, true
	}
	pos := start
	for pos+46 <= cdEnd && u32(pos) == 0x02014b50 && len(names) < companionDropZipMaxNames {
		n, m, k := u16(pos+28), u16(pos+30), u16(pos+32)
		if pos+46+n+m+k > cdEnd {
			complete = false
			break
		}
		names = append(names, strings.ReplaceAll(string(tail[pos+46:pos+46+n]), "\\", "/"))
		pos += 46 + n + m + k
	}
	if complete && (pos != cdEnd || uint64(len(names)) != entries) {
		complete = false
	}
	return names, complete, true
}

// companionDropIsPort reports a PortMaster port's zip: a port.json, or a
// launch script at the top beside a folder (Port.sh and port/).
func companionDropIsPort(names []string) bool {
	script, folder := false, false
	for _, raw := range names {
		n := strings.TrimPrefix(raw, "./")
		isDir := strings.HasSuffix(n, "/")
		parts := strings.Split(strings.TrimSuffix(n, "/"), "/")
		if !isDir && len(parts) <= 3 && strings.EqualFold(parts[len(parts)-1], "port.json") {
			return true
		}
		if len(parts) == 1 && !isDir && strings.EqualFold(path.Ext(n), ".sh") {
			script = true
		}
		if len(parts) > 1 || isDir {
			folder = true
		}
	}
	return script && folder
}

// companionDropZipGame names the console of the game in a zip from its file
// names, as rommDetectZip does with the file on the card: a Neo Geo ROM set,
// or a single game file whose type only one console uses (sure). A single
// file of a shared type gives the consoles that type could be for.
func companionDropZipGame(names []string) (ids []string, sure bool) {
	var games, bases []string
	for _, n := range names {
		if strings.HasSuffix(n, "/") {
			continue
		}
		base := strings.ToLower(path.Base(n))
		bases = append(bases, base)
		if rommZipIgnored[path.Ext(base)] {
			continue
		}
		games = append(games, base)
	}
	if rommNeoGeoSet(bases) {
		return []string{"neogeo"}, true
	}
	if len(games) != 1 {
		return nil, false
	}
	ext := path.Ext(games[0])
	if id, ok := rommExtSystems[ext]; ok {
		return []string{id}, true
	}
	if ext == ".gdi" || ext == ".cdi" {
		return []string{"dc"}, true
	}
	return companionDropAskSystems[ext], false
}
