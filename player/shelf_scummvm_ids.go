package main

// shelf_scummvm_ids.go — which ScummVM game a folder holds, from the names of
// its files (and, for engines whose data files are named the same in every
// game, from the folder's name).
//
// The libretro scummvm core plays a game from a .scummvm file whose text is
// the game's ScummVM ID ("sky", "tentacle"), in the folder that file is in.
// An empty file asks the core to detect the game itself, which newer cores
// do and older ones (the ones firmwares ship) do not, and which may pick the
// wrong language or version; so the shelf writes the ID when it can tell it
// for certain and nothing otherwise.
//
// Three tiers, first answer wins:
//   - signatures: files only that game has (sky.dnr, tentacle.000, comi.la0);
//   - the Sierra AGI and SCI engines and the early SCUMM games (logdir and
//     vol.0, resource.map, 00.lfl) name their files the same in every game, and
//     ScummVM tells those apart by checksum. There the folder's name decides
//     ("King's Quest IV" → kq4sci) when the files say which engine it is;
//   - anything else is "not recognised": no file is written and the folder is
//     named in the log and on the shelf's More row.
//
// The IDs are ScummVM's own (its compatibility list). Plain game IDs, not
// "engine:game": the cores firmwares ship predate the qualified form.

import (
	"strings"
	"unicode"
)

// shelfSVSig is a game's file signature: every name in All is in the folder
// (lower case) and, when Any is set, at least one name of Any.
type shelfSVSig struct {
	ID  string
	All []string
	Any []string
}

// shelfSVSigs are checked in order; a game whose files are a subset of
// another's comes after it (sword1 before sword2, simon2 before simon1).
var shelfSVSigs = []shelfSVSig{
	// The freeware games ScummVM offers on its own site.
	{ID: "sky", All: []string{"sky.dnr", "sky.dsk"}},
	{ID: "queen", Any: []string{"queen.1", "queen.1c"}},
	{ID: "lure", All: []string{"disk1.vga"}},
	{ID: "drascula", All: []string{"packet.001"}},
	{ID: "dreamweb", All: []string{"dreamweb.r00", "dreamweb.r02"}},
	{ID: "teenagent", All: []string{"lan_000.res", "lan_500.res"}},
	{ID: "sword25", All: []string{"data.b25c"}},
	// SCUMM version 5 and later: each game names its resources after itself.
	{ID: "comi", All: []string{"comi.la0"}},
	{ID: "dig", All: []string{"dig.la0"}},
	{ID: "ft", All: []string{"ft.la0"}},
	{ID: "samnmax", Any: []string{"samnmax.000", "samnmax.sm0"}},
	{ID: "tentacle", All: []string{"tentacle.000"}},
	{ID: "atlantis", All: []string{"atlantis.000"}},
	{ID: "monkey2", All: []string{"monkey2.000"}},
	{ID: "monkey", Any: []string{"monkey.000", "monkey1.000"}},
	// Revolution, Adventure Soft, Westwood, Tinsel, Cyan.
	{ID: "sword1", All: []string{"swordres.rif"}},
	{ID: "sword2", All: []string{"general.clu", "resource.inf"}},
	{ID: "simon2", Any: []string{"gsptr30", "simon2.gme"}},
	{ID: "simon1", Any: []string{"simon.gme"}},
	{ID: "simon1", All: []string{"gamepc", "icon.dat", "stripped.txt", "tbllist"}},
	{ID: "dw2", Any: []string{"dw2.scn", "dw2.gra"}},
	{ID: "dw", Any: []string{"dw.scn", "dw.gra"}},
	{ID: "kyra3", Any: []string{"malcolm.exe"}},
	{ID: "kyra2", Any: []string{"fate.exe"}},
	{ID: "kyra1", Any: []string{"gemcut.emc", "gemcut.pak"}},
	{ID: "myst", All: []string{"myst.dat"}},
}

// shelfSVHint names a game by its folder's name, per engine family: the ID
// when the folder holds AGI, SCI or early SCUMM files ("" none).
type shelfSVHint struct {
	Names []string
	AGI   string
	SCI   string
	SCUMM string
}

// shelfSVHints are checked in order: numbered sequels before the first game,
// whose plain name ("king's quest") every sequel's name contains.
var shelfSVHints = []shelfSVHint{
	{Names: []string{"kings quest 7", "kq7", "princeless bride"}, SCI: "kq7"},
	{Names: []string{"kings quest 6", "kq6", "heir today gone tomorrow"}, SCI: "kq6"},
	{Names: []string{"kings quest 5", "kq5", "absence makes the heart go yonder"}, SCI: "kq5"},
	{Names: []string{"kings quest 4", "kq4", "perils of rosella"}, SCI: "kq4sci"},
	{Names: []string{"kings quest 3", "kq3", "to heir is human"}, AGI: "kq3"},
	{Names: []string{"kings quest 2", "kq2", "romancing the throne"}, AGI: "kq2"},
	{Names: []string{"kings quest", "kq1", "quest for the crown"}, AGI: "kq1", SCI: "kq1sci"},
	{Names: []string{"space quest 6", "sq6", "spinal frontier"}, SCI: "sq6"},
	{Names: []string{"space quest 5", "sq5", "the next mutation"}, SCI: "sq5"},
	{Names: []string{"space quest 4", "sq4", "time rippers"}, SCI: "sq4"},
	{Names: []string{"space quest 3", "sq3", "pirates of pestulon"}, SCI: "sq3"},
	{Names: []string{"space quest 2", "sq2", "vohauls revenge"}, AGI: "sq2"},
	{Names: []string{"space quest", "sq1", "sarien encounter"}, AGI: "sq1", SCI: "sq1sci"},
	{Names: []string{"leisure suit larry 7", "lsl7", "love for sail"}, SCI: "lsl7"},
	{Names: []string{"leisure suit larry 6", "lsl6", "shape up or slip out"}, SCI: "lsl6"},
	{Names: []string{"leisure suit larry 5", "lsl5", "passionate patti"}, SCI: "lsl5"},
	{Names: []string{"leisure suit larry 3", "lsl3", "pulsating pectorals"}, SCI: "lsl3"},
	{Names: []string{"leisure suit larry 2", "lsl2", "goes looking for love"}, SCI: "lsl2"},
	{Names: []string{"leisure suit larry", "lsl1", "land of the lounge lizards"}, AGI: "lsl1", SCI: "lsl1sci"},
	{Names: []string{"police quest 4", "pq4", "open season"}, SCI: "pq4"},
	{Names: []string{"police quest 3", "pq3", "the kindred"}, SCI: "pq3"},
	{Names: []string{"police quest 2", "pq2", "the vengeance"}, SCI: "pq2"},
	{Names: []string{"police quest", "pq1", "death angel"}, AGI: "pq1", SCI: "pq1sci"},
	{Names: []string{"quest for glory 4", "qfg4", "shadows of darkness"}, SCI: "qfg4"},
	{Names: []string{"quest for glory 3", "qfg3", "wages of war"}, SCI: "qfg3"},
	{Names: []string{"quest for glory 2", "qfg2", "trial by fire"}, SCI: "qfg2"},
	{Names: []string{"quest for glory", "qfg1", "heros quest", "so you want to be a hero"}, SCI: "qfg1"},
	{Names: []string{"gabriel knight 2", "gk2", "the beast within"}, SCI: "gk2"},
	{Names: []string{"gabriel knight", "gk1", "sins of the fathers"}, SCI: "gk1"},
	{Names: []string{"last crusade", "indy3", "indiana jones 3"}, SCUMM: "indy3"},
	{Names: []string{"zak mckracken", "zak"}, SCUMM: "zak"},
	{Names: []string{"maniac mansion", "maniac"}, SCUMM: "maniac"},
	{Names: []string{"loom"}, SCUMM: "loom"},
	{Names: []string{"monkey island", "monkey"}, SCUMM: "monkey"},
	{Names: []string{"passport to adventure"}, SCUMM: "pass"},
}

// shelfSVHiRes are the games drawn at 640×400 or more, whose pointer crosses
// the screen too slowly at the core's default speed (the libretro docs
// recommend 2.0 for them).
var shelfSVHiRes = map[string]bool{
	"comi": true, "sword1": true, "sword2": true, "sword25": true, "kq7": true, "lsl7": true,
	"sq6": true, "gk2": true, "myst": true, "dw2": true, "lsl6hires": true,
}

// shelfSVRoman turns the Roman numerals of sequels into digits for matching.
var shelfSVRoman = map[string]string{"ii": "2", "iii": "3", "iv": "4", "v": "5", "vi": "6", "vii": "7"}

// shelfSVNormName folds a folder name for matching: lower case, letters and
// digits only, Roman numerals as digits, padded with spaces so a match is
// always on whole words (" kq1 ").
func shelfSVNormName(name string) string {
	s := strings.NewReplacer("'", "", "\u2019", "", "&", " and ").Replace(strings.ToLower(name))
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	fields := strings.Fields(b.String())
	for i, f := range fields {
		if d, ok := shelfSVRoman[f]; ok {
			fields[i] = d
		}
	}
	return " " + strings.Join(fields, " ") + " "
}

// shelfSVDetect names the game in a folder from its file names (lower case,
// files straight in the folder) and the folder's own name. why says how
// ("its files", "its folder name"); id is "" when it cannot tell.
func shelfSVDetect(names map[string]bool, folder string) (id, why string) {
	for _, sig := range shelfSVSigs {
		if shelfSVSigMatch(sig, names) {
			return sig.ID, "its files"
		}
	}
	// The two CGE games share their two data files; Soltys is the one people
	// have (both are free).
	if names["vol.cat"] && names["vol.dat"] {
		if strings.Contains(shelfSVNormName(folder), " sfinx ") {
			return "sfinx", "its files and folder name"
		}
		return "soltys", "its files"
	}
	agi := names["words.tok"] && names["object"]
	sci32 := names["resmap.000"] || names["resmap.001"] || names["ressci.000"] || names["ressci.001"]
	sci := sci32 || (names["resource.map"] && (names["resource.000"] || names["resource.001"]))
	scumm := names["00.lfl"] || names["000.lfl"]
	if !agi && !sci && !scumm {
		return "", ""
	}
	norm := shelfSVNormName(folder)
	for _, h := range shelfSVHints {
		if !shelfSVNameMatch(norm, h.Names) {
			continue
		}
		switch {
		case agi && h.AGI != "":
			return h.AGI, "its folder name"
		case sci && h.SCI != "":
			id := h.SCI
			// The two versions whose IDs differ by their engine: Larry 6's
			// high-resolution CD and Quest for Glory's VGA remake.
			if id == "lsl6" && sci32 {
				id = "lsl6hires"
			}
			if id == "qfg1" && strings.Contains(norm, " vga ") {
				id = "qfg1vga"
			}
			return id, "its folder name"
		case scumm && h.SCUMM != "":
			return h.SCUMM, "its folder name"
		}
		// The name fits a game of another engine: a later, more general
		// hint must not claim it either.
		return "", ""
	}
	return "", ""
}

func shelfSVSigMatch(sig shelfSVSig, names map[string]bool) bool {
	for _, n := range sig.All {
		if !names[n] {
			return false
		}
	}
	if len(sig.Any) == 0 {
		return len(sig.All) > 0
	}
	for _, n := range sig.Any {
		if names[n] {
			return true
		}
	}
	return false
}

func shelfSVNameMatch(norm string, names []string) bool {
	for _, n := range names {
		if strings.Contains(norm, " "+n+" ") {
			return true
		}
	}
	return false
}

// shelfSVBareID is a .scummvm file's ID without an engine prefix
// ("sci:kq7" → "kq7"), lower case.
func shelfSVBareID(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if i := strings.LastIndexByte(id, ':'); i >= 0 {
		id = id[i+1:]
	}
	return id
}

// shelfSVDataLike reports whether a folder's files look like a game's data
// rather than pictures and notes, so an unrecognised folder is worth naming.
func shelfSVDataLike(names map[string]bool) bool {
	for n := range names {
		switch strings.ToLower(shelfExtOf(n)) {
		case ".png", ".jpg", ".jpeg", ".bmp", ".gif", ".webp", ".txt", ".nfo", ".pdf", ".md",
			".url", ".ico", ".htm", ".html", ".diz", ".srm", ".state", ".scummvm":
			continue
		}
		return true
	}
	return false
}

// shelfExtOf is name's extension with its dot ("" none), for a bare file
// name.
func shelfExtOf(name string) string {
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		return name[i:]
	}
	return ""
}
