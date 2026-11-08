package main

// romm_detect.go — what a file on the card is.
//
// The ROM manager (romm_ui.go) has to tell a Game Boy Advance cartridge lying
// in Downloads from a Mega Drive one, a PlayStation disc from a PSP one, and a
// BIOS image from both. The answers come from three places, best first:
//
//   - the header: most cartridges and discs carry a signature their console's
//     own boot code checks — the iNES magic, the Nintendo logo of a Game Boy,
//     GBA or DS cart, "SEGA" in a Mega Drive header, the SNES internal header
//     whose checksum and complement add up, an ISO 9660 volume descriptor that
//     names the system, BOOT2 in a PlayStation 2 disc's SYSTEM.CNF;
//   - an extension only one console uses (.gba, .sfc, .nds …);
//   - nothing: a container nobody can see into cheaply (.chd, .7z) or a .bin
//     without a header is left alone unless the folder it sits in says what it
//     is.
//
// Every sniffer reads a bounded prefix and works on plain bytes, so the tests
// build synthetic headers instead of shipping ROMs.

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// rommSystem is one console the ROM manager files games for.
type rommSystem struct {
	ID string
	// Name is the Games library's name for it (romSystemAliases).
	Name string
	// Folder is the folder created under Roms when the card has none for the
	// console yet: the names the TrimUI firmware and CrossMix use.
	Folder string
	// Exts are the extensions only this console uses.
	Exts []string
}

// rommSystems is every console the manager can recognise, in the order the plan
// lists them.
var rommSystems = []rommSystem{
	{ID: "nes", Name: "NES", Folder: "FC", Exts: []string{".nes", ".fds", ".unf", ".unif"}},
	{ID: "snes", Name: "SNES", Folder: "SFC", Exts: []string{".sfc", ".smc", ".swc", ".fig"}},
	{ID: "gb", Name: "Game Boy", Folder: "GB", Exts: []string{".gb"}},
	{ID: "gbc", Name: "Game Boy Color", Folder: "GBC", Exts: []string{".gbc"}},
	{ID: "gba", Name: "Game Boy Advance", Folder: "GBA", Exts: []string{".gba"}},
	{ID: "nds", Name: "Nintendo DS", Folder: "NDS", Exts: []string{".nds"}},
	{ID: "n64", Name: "Nintendo 64", Folder: "N64", Exts: []string{".n64", ".z64", ".v64"}},
	{ID: "md", Name: "Mega Drive", Folder: "MD", Exts: []string{".gen", ".smd"}},
	{ID: "sms", Name: "Master System", Folder: "MS", Exts: []string{".sms"}},
	{ID: "gg", Name: "Game Gear", Folder: "GG", Exts: []string{".gg"}},
	{ID: "pce", Name: "PC Engine", Folder: "PCE", Exts: []string{".pce"}},
	{ID: "ps1", Name: "PlayStation", Folder: "PS"},
	{ID: "ps2", Name: "PlayStation 2", Folder: "PS2"},
	{ID: "psp", Name: "PlayStation Portable", Folder: "PSP", Exts: []string{".cso"}},
	{ID: "saturn", Name: "Saturn", Folder: "SATURN"},
	{ID: "dc", Name: "Dreamcast", Folder: "DC", Exts: []string{".cdi", ".gdi"}},
	{ID: "gc", Name: "GameCube", Folder: "GC"},
	{ID: "wii", Name: "Wii", Folder: "WII", Exts: []string{".wbfs"}},
	{ID: "xbox", Name: "Xbox", Folder: "XBOX", Exts: []string{".xiso"}},
	{ID: "neogeo", Name: "Neo Geo", Folder: "NEOGEO"},
}

// rommSystemByID finds a console by its short id.
func rommSystemByID(id string) (rommSystem, bool) {
	for _, s := range rommSystems {
		if s.ID == id {
			return s, true
		}
	}
	return rommSystem{}, false
}

// rommSystemByName finds a console by the library's name for it.
func rommSystemByName(name string) (rommSystem, bool) {
	for _, s := range rommSystems {
		if strings.EqualFold(s.Name, name) {
			return s, true
		}
	}
	return rommSystem{}, false
}

// rommSystemLabel is the readable name of a console id ("" for an unknown one).
func rommSystemLabel(id string) string {
	if s, ok := rommSystemByID(id); ok {
		return s.Name
	}
	return ""
}

// rommExtSystems maps an extension only one console uses to that console.
var rommExtSystems = rommBuildExtIndex()

func rommBuildExtIndex() map[string]string {
	out := map[string]string{}
	for _, s := range rommSystems {
		for _, ext := range s.Exts {
			out[ext] = s.ID
		}
	}
	return out
}

// rommContainerExts are extensions several consoles share: the file has to be
// opened, or its folder read, to know which console it belongs to. ".md" is
// here rather than with the Mega Drive because it is also Markdown: only the
// header makes it a cartridge. CloneCD's ".img" is left out on purpose: it
// only works beside its .ccd and .sub, which no sheet names, so moving it alone
// would break the game.
var rommContainerExts = map[string]bool{
	".bin": true, ".iso": true, ".chd": true, ".cue": true, ".m3u": true,
	".pbp": true, ".zip": true, ".7z": true, ".rom": true, ".md": true, ".gcm": true,
	".gcz": true, ".rvz": true, ".ciso": true,
}

// rommIsROMExt reports whether a file with this extension could be a game.
func rommIsROMExt(ext string) bool {
	ext = strings.ToLower(ext)
	if _, unique := rommExtSystems[ext]; unique {
		return true
	}
	return rommContainerExts[ext]
}

// rommDetection is what a file turned out to be.
type rommDetection struct {
	// System is the rommSystem id, "" when the file could not be identified.
	System string
	// Strong is true when a header proved it, or an extension only that
	// console uses: strong enough to move a file out of another console's
	// folder.
	Strong bool
	// How is the evidence, said plainly, for the plan's reason line.
	How string
	// CRC is the game's CRC32 when it came for free (a zip entry).
	CRC    uint32
	HasCRC bool
}

// rommGBLogo is the Nintendo logo every Game Boy and Game Boy Color cartridge
// carries at 0x104; the boot ROM refuses a cart without it.
var rommGBLogo = []byte{
	0xCE, 0xED, 0x66, 0x66, 0xCC, 0x0D, 0x00, 0x0B, 0x03, 0x73, 0x00, 0x83, 0x00, 0x0C, 0x00, 0x0D,
	0x00, 0x08, 0x11, 0x1F, 0x88, 0x89, 0x00, 0x0E, 0xDC, 0xCC, 0x6E, 0xE6, 0xDD, 0xDD, 0xD9, 0x99,
	0xBB, 0xBB, 0x67, 0x63, 0x6E, 0x0E, 0xEC, 0xCC, 0xDD, 0xDC, 0x99, 0x9F, 0xBB, 0xB9, 0x33, 0x3E,
}

// rommGBALogoPrefix is the start of the logo a GBA cart carries at 0x04 and a
// DS cart at 0xC0. Twelve fixed bytes plus the second check each console has
// (0x96 at 0xB2 on a GBA, the logo's CRC at 0x15C on a DS) do not happen by
// chance.
var rommGBALogoPrefix = []byte{0x24, 0xFF, 0xAE, 0x51, 0x69, 0x9A, 0xA2, 0x21, 0x3D, 0x84, 0x82, 0x0A}

// rommHeadSize is how much of a cartridge the sniffers read: enough for the
// SNES HiROM header behind a 512-byte copier header, and for the Master System
// header near the end of the first 32 KiB.
const rommHeadSize = 0x10000 + 0x200 + 0x40

// rommHasAt reports whether b holds sig at off.
func rommHasAt(b []byte, off int, sig []byte) bool {
	return off >= 0 && off+len(sig) <= len(b) && bytes.Equal(b[off:off+len(sig)], sig)
}

// rommSniffCart identifies a cartridge image from its first bytes. size is the
// whole file's size; a SNES copier header is told from it.
func rommSniffCart(head []byte, size int64) string {
	switch {
	case rommHasAt(head, 0, []byte("NES\x1a")), rommHasAt(head, 0, []byte("FDS\x1a")),
		rommHasAt(head, 1, []byte("*NINTENDO-HVC*")):
		return "nes"
	case rommHasAt(head, 0, []byte{0x80, 0x37, 0x12, 0x40}), rommHasAt(head, 0, []byte{0x37, 0x80, 0x40, 0x12}),
		rommHasAt(head, 0, []byte{0x40, 0x12, 0x37, 0x80}):
		// Big-endian (.z64), byte-swapped (.v64) and little-endian (.n64).
		return "n64"
	case rommHasAt(head, 0xC0, rommGBALogoPrefix) && len(head) >= 0x15E &&
		binary.LittleEndian.Uint16(head[0x15C:0x15E]) == 0xCF56:
		return "nds"
	case rommHasAt(head, 0x04, rommGBALogoPrefix) && len(head) > 0xB2 && head[0xB2] == 0x96:
		return "gba"
	case rommHasAt(head, 0x104, rommGBLogo):
		// The CGB flag: 0x80 runs on both, 0xC0 only on a Game Boy Color. Both
		// are Game Boy Color games in every set the library matches against.
		if len(head) > 0x143 && (head[0x143] == 0x80 || head[0x143] == 0xC0) {
			return "gbc"
		}
		return "gb"
	}
	if len(head) >= 0x110 {
		sig := head[0x100:0x110]
		// "SEGA MEGA DRIVE", "SEGA GENESIS", " SEGA GENESIS"; a 32X cart says
		// "SEGA 32X", and that is a console the library does not list.
		if bytes.Contains(sig, []byte("SEGA")) && !bytes.Contains(sig, []byte("32X")) {
			return "md"
		}
	}
	for _, off := range []int{0x7FF0, 0x3FF0, 0x1FF0} {
		if !rommHasAt(head, off, []byte("TMR SEGA")) {
			continue
		}
		// The region nibble: 3 and 4 are Master System, 5 to 7 Game Gear.
		if len(head) > off+0x0F {
			switch head[off+0x0F] >> 4 {
			case 5, 6, 7:
				return "gg"
			}
		}
		return "sms"
	}
	if rommSNESHeader(head, size) {
		return "snes"
	}
	return ""
}

// rommSNESHeader looks for the internal header a SNES cartridge carries at
// 0x7FC0 (LoROM) or 0xFFC0 (HiROM), behind a 512-byte copier header when the
// size says there is one: a map mode the console knows, a plausible ROM size,
// and a checksum and checksum complement that add up to 0xFFFF.
func rommSNESHeader(head []byte, size int64) bool {
	base := 0
	if size%1024 == 512 {
		base = 512
	}
	for _, off := range []int{0x7FC0, 0xFFC0} {
		h := base + off
		if h+0x20 > len(head) {
			continue
		}
		if head[h+0x15]&0xE0 != 0x20 {
			continue
		}
		if romSize := head[h+0x17]; romSize < 0x07 || romSize > 0x0D {
			continue
		}
		complement := binary.LittleEndian.Uint16(head[h+0x1C : h+0x1E])
		checksum := binary.LittleEndian.Uint16(head[h+0x1E : h+0x20])
		// Zero halves are padding (0x0000 / 0xFFFF), not a header.
		if checksum != 0 && complement != 0 && complement^checksum == 0xFFFF {
			return true
		}
	}
	return false
}

// rommDiscLayout is where a disc image keeps a sector's 2048 bytes of data.
type rommDiscLayout struct {
	sector int64 // bytes per stored sector
	offset int64 // where the user data starts inside one
}

// rommRawSync starts every sector of a raw (2352-byte) CD image.
var rommRawSync = []byte{0x00, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x00}

// rommDiscLayoutOf tells a raw CD image (Mode 1 or Mode 2) from a plain ISO.
func rommDiscLayoutOf(first []byte) rommDiscLayout {
	if rommHasAt(first, 0, rommRawSync) && len(first) > 15 {
		if first[15] == 2 {
			return rommDiscLayout{sector: 2352, offset: 24}
		}
		return rommDiscLayout{sector: 2352, offset: 16}
	}
	return rommDiscLayout{sector: 2048, offset: 0}
}

// rommReadSector reads the user data of one sector.
func rommReadSector(r io.ReaderAt, layout rommDiscLayout, lba int64) ([]byte, error) {
	buf := make([]byte, 2048)
	n, err := r.ReadAt(buf, lba*layout.sector+layout.offset)
	if n == len(buf) {
		return buf, nil
	}
	if err == nil {
		err = io.ErrUnexpectedEOF
	}
	return nil, err
}

// rommSniffDisc identifies a CD or DVD image: GameCube and Wii by their disc
// magic, the Sega consoles by their boot sector, the PlayStations and the PSP
// by the ISO 9660 volume descriptor, the Xbox by its own file system.
func rommSniffDisc(r io.ReaderAt, size int64) string {
	first := make([]byte, 0x60)
	if n, _ := r.ReadAt(first, 0); n < len(first) {
		first = first[:n]
	}
	if len(first) >= 0x20 {
		if binary.BigEndian.Uint32(first[0x1C:0x20]) == 0xC2339F3D {
			return "gc"
		}
		if binary.BigEndian.Uint32(first[0x18:0x1C]) == 0x5D1C9EA3 {
			return "wii"
		}
	}
	// Dolphin's RVZ and WIA keep the disc type right after their 0x48-byte
	// file header: 1 GameCube, 2 Wii.
	if (rommHasAt(first, 0, []byte("RVZ\x01")) || rommHasAt(first, 0, []byte("WIA\x01"))) && len(first) >= 0x4C {
		switch binary.BigEndian.Uint32(first[0x48:0x4C]) {
		case 1:
			return "gc"
		case 2:
			return "wii"
		}
		return ""
	}
	if rommHasAt(first, 0, []byte("CISO")) && len(first) >= 8 {
		// The PSP's compressed ISO has a 24-byte header; Dolphin's CISO keeps
		// the uncompressed disc header 32 KiB in.
		if binary.LittleEndian.Uint32(first[4:8]) == 0x18 {
			return "psp"
		}
		head := make([]byte, 0x20)
		if n, _ := r.ReadAt(head, 0x8000); n == len(head) {
			if binary.BigEndian.Uint32(head[0x1C:0x20]) == 0xC2339F3D {
				return "gc"
			}
			if binary.BigEndian.Uint32(head[0x18:0x1C]) == 0x5D1C9EA3 {
				return "wii"
			}
		}
		return ""
	}
	layout := rommDiscLayoutOf(first)
	if boot, err := rommReadSector(r, layout, 0); err == nil {
		switch {
		case bytes.HasPrefix(boot, []byte("SEGA SEGASATURN")):
			return "saturn"
		case bytes.HasPrefix(boot, []byte("SEGA SEGAKATANA")):
			return "dc"
		case bytes.HasPrefix(boot, []byte("SEGADISCSYSTEM")):
			// Sega CD: a console the library does not list.
			return ""
		}
	}
	if pvd, err := rommReadSector(r, layout, 16); err == nil && pvd[0] == 1 && string(pvd[1:6]) == "CD001" {
		system := strings.TrimSpace(string(pvd[8:40]))
		switch {
		case strings.HasPrefix(system, "PSP GAME"):
			return "psp"
		case strings.HasPrefix(system, "PLAYSTATION"):
			if rommISOHasBoot2(r, layout, pvd) {
				return "ps2"
			}
			return "ps1"
		}
	}
	// An Xbox disc keeps its file system 64 KiB into the game partition: at
	// the start of a trimmed image, or behind the video partition of a full one.
	for _, base := range []int64{0, 0x18300000} {
		if base+0x10000+20 > size {
			continue
		}
		magic := make([]byte, 20)
		if n, _ := r.ReadAt(magic, base+0x10000); n == len(magic) && string(magic) == "MICROSOFT*XBOX*MEDIA" {
			return "xbox"
		}
	}
	return ""
}

// rommISOHasBoot2 reads SYSTEM.CNF from the root directory: a PlayStation 2
// disc boots with "BOOT2 = …", a PlayStation one with "BOOT = …" (or has no
// SYSTEM.CNF at all).
func rommISOHasBoot2(r io.ReaderAt, layout rommDiscLayout, pvd []byte) bool {
	if len(pvd) < 190 {
		return false
	}
	root := pvd[156:190]
	lba := int64(binary.LittleEndian.Uint32(root[2:6]))
	length := int64(binary.LittleEndian.Uint32(root[10:14]))
	if lba <= 0 || length <= 0 {
		return false
	}
	if length > 64<<10 {
		length = 64 << 10
	}
	for read := int64(0); read < length; read += 2048 {
		sec, err := rommReadSector(r, layout, lba+read/2048)
		if err != nil {
			return false
		}
		for pos := 0; pos < len(sec); {
			recLen := int(sec[pos])
			if recLen == 0 {
				break // the rest of this sector is padding
			}
			if recLen < 34 || pos+recLen > len(sec) {
				break
			}
			nameLen := int(sec[pos+32])
			if 33+nameLen <= recLen {
				name := strings.ToUpper(string(sec[pos+33 : pos+33+nameLen]))
				if name == "SYSTEM.CNF;1" || name == "SYSTEM.CNF" {
					fileLBA := int64(binary.LittleEndian.Uint32(sec[pos+2 : pos+6]))
					cnf, err := rommReadSector(r, layout, fileLBA)
					if err != nil {
						return false
					}
					return bytes.Contains(bytes.ToUpper(cnf), []byte("BOOT2"))
				}
			}
			pos += recLen
		}
	}
	return false
}

// rommSniffPBP tells a PlayStation game converted for the PSP (its DATA.PSAR
// starts with PSISOIMG or PSTITLEIMG) from a PSP game or homebrew.
func rommSniffPBP(r io.ReaderAt) string {
	head := make([]byte, 0x28)
	if n, _ := r.ReadAt(head, 0); n < len(head) || !rommHasAt(head, 0, []byte("\x00PBP")) {
		return ""
	}
	psar := int64(binary.LittleEndian.Uint32(head[0x24:0x28]))
	magic := make([]byte, 10)
	if n, _ := r.ReadAt(magic, psar); n == len(magic) {
		if bytes.HasPrefix(magic, []byte("PSISOIMG")) || bytes.HasPrefix(magic, []byte("PSTITLEIMG")) {
			return "ps1"
		}
	}
	return "psp"
}

// rommZipIgnored are the files a ROM zip carries beside the game.
var rommZipIgnored = map[string]bool{
	".txt": true, ".nfo": true, ".diz": true, ".url": true, ".htm": true, ".html": true,
	".pdf": true, ".jpg": true, ".jpeg": true, ".png": true, ".dat": true, ".xml": true,
}

// rommNeoGeoSet reports whether a zip's file names are a Neo Geo ROM set: the
// program (p1), sprite (c1) and sound driver (m1) chips every MVS set has, named
// "201-p1.p1" in MAME's sets or "<id>-p1.bin" in older ones.
func rommNeoGeoSet(names []string) bool {
	var p, c, m bool
	has := func(name, chip string) bool {
		ext := filepath.Ext(name)
		stem := strings.TrimSuffix(name, ext)
		return ext == "."+chip || strings.HasSuffix(stem, "-"+chip) || strings.HasSuffix(stem, "_"+chip)
	}
	for _, n := range names {
		switch {
		case has(n, "p1"):
			p = true
		case has(n, "c1"):
			c = true
		case has(n, "m1"):
			m = true
		}
	}
	return p && c && m
}

// rommDiscExts are the image formats rommSniffDisc can look into.
var rommDiscExts = map[string]bool{
	".iso": true, ".bin": true, ".gcm": true, ".ciso": true, ".rvz": true,
	".wbfs": true, ".xiso": true, ".cso": true,
}

// rommDetectZip reads a zip's directory (cheap: the end of the file) and
// identifies the single game inside it. The entry's CRC32 comes with the
// directory, so the metadata lookup gets it for free.
func rommDetectZip(path string) rommDetection {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return rommDetection{}
	}
	defer zr.Close()
	var games []*zip.File
	names := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := strings.ToLower(filepath.Base(f.Name))
		names = append(names, name)
		if rommZipIgnored[filepath.Ext(name)] {
			continue
		}
		games = append(games, f)
	}
	if rommNeoGeoSet(names) {
		return rommDetection{System: "neogeo", Strong: true, How: "a Neo Geo ROM set"}
	}
	if len(games) != 1 {
		return rommDetection{}
	}
	f := games[0]
	det := rommDetection{CRC: f.CRC32, HasCRC: true}
	ext := strings.ToLower(filepath.Ext(f.Name))
	if id, ok := rommExtSystems[ext]; ok {
		det.System, det.Strong, det.How = id, true, "a "+ext+" file in the zip"
	}
	if ext == ".zip" || ext == ".7z" || ext == ".chd" {
		return det
	}
	// The header confirms the extension, and names a .bin or .iso.
	limit := int64(rommHeadSize)
	if rommDiscExts[ext] {
		limit = 192 << 10
	}
	rc, err := f.Open()
	if err != nil {
		return det
	}
	head, _ := io.ReadAll(io.LimitReader(rc, limit))
	rc.Close()
	if id := rommSniffCart(head, int64(f.UncompressedSize64)); id != "" {
		det.System, det.Strong, det.How = id, true, rommSystemLabel(id)+" header in the zip"
		return det
	}
	if rommDiscExts[ext] {
		if id := rommSniffDisc(bytes.NewReader(head), int64(len(head))); id != "" {
			det.System, det.Strong, det.How = id, true, rommSystemLabel(id)+" disc in the zip"
		}
	}
	return det
}

// rommDetectSheet names the console of a .cue or .m3u from the disc it points
// at: the first track of a cue sheet, the first disc of a playlist.
func rommDetectSheet(path string, depth int) rommDetection {
	if depth > 2 {
		return rommDetection{}
	}
	for _, ref := range romSheetReferences(path) {
		info, err := os.Stat(ref)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		switch strings.ToLower(filepath.Ext(ref)) {
		case ".cue", ".m3u":
			return rommDetectSheet(ref, depth+1)
		case ".gdi", ".cdi":
			return rommDetection{System: "dc", Strong: true, How: "a Dreamcast disc"}
		case ".chd", ".7z", ".zip":
			return rommDetection{}
		case ".pbp":
			return rommDetectFile(ref, info.Size())
		}
		f, err := os.Open(ref)
		if err != nil {
			return rommDetection{}
		}
		id := rommSniffDisc(f, info.Size())
		f.Close()
		if id == "" {
			return rommDetection{}
		}
		return rommDetection{System: id, Strong: true, How: "a " + rommSystemLabel(id) + " disc"}
	}
	return rommDetection{}
}

// rommExtDetection is what the extension alone says.
func rommExtDetection(ext string) rommDetection {
	if id, ok := rommExtSystems[strings.ToLower(ext)]; ok {
		return rommDetection{System: id, Strong: true, How: "a " + strings.ToLower(ext) + " file"}
	}
	return rommDetection{}
}

// rommDetectFile identifies one file. It reads at most a cartridge header, a
// zip directory or a few disc sectors.
func rommDetectFile(path string, size int64) rommDetection {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".zip":
		return rommDetectZip(path)
	case ".cue", ".m3u":
		return rommDetectSheet(path, 0)
	case ".gdi", ".cdi":
		return rommDetection{System: "dc", Strong: true, How: "a Dreamcast disc image"}
	case ".chd", ".7z", ".gcz":
		// Compressed: only the folder the file sits in can say.
		return rommDetection{}
	}
	f, err := os.Open(path)
	if err != nil {
		return rommExtDetection(ext)
	}
	defer f.Close()
	if ext == ".pbp" {
		if id := rommSniffPBP(f); id != "" {
			return rommDetection{System: id, Strong: true, How: "a " + rommSystemLabel(id) + " EBOOT"}
		}
		return rommDetection{}
	}
	limit := int64(rommHeadSize)
	if size >= 0 && size < limit {
		limit = size
	}
	head := make([]byte, limit)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if id := rommSniffCart(head, size); id != "" {
		return rommDetection{System: id, Strong: true, How: rommSystemLabel(id) + " header"}
	}
	if rommDiscExts[ext] {
		if id := rommSniffDisc(f, size); id != "" {
			return rommDetection{System: id, Strong: true, How: rommSystemLabel(id) + " disc"}
		}
	}
	return rommExtDetection(ext)
}

// rommBIOS is a firmware file emulators look for by name.
type rommBIOS struct {
	Name   string // lower case
	System string // the console it boots, for the plan's line
	Sub    string // the folder under BIOS it belongs in ("" for BIOS itself)
	Sizes  []int64
}

// rommBIOSFiles are the firmware files the handheld's cores and the desktop
// emulators ask for. A size list keeps a same-named file that is something
// else (a 2 MB "bios7.bin" is not a DS BIOS) where it is.
var rommBIOSFiles = []rommBIOS{
	{Name: "psxonpsp660.bin", System: "PlayStation", Sizes: []int64{512 << 10}},
	{Name: "ps1_rom.bin", System: "PlayStation"},
	{Name: "dc_boot.bin", System: "Dreamcast", Sub: "dc", Sizes: []int64{2 << 20}},
	{Name: "dc_bios.bin", System: "Dreamcast", Sub: "dc", Sizes: []int64{2 << 20}},
	{Name: "dc_flash.bin", System: "Dreamcast", Sub: "dc", Sizes: []int64{128 << 10}},
	{Name: "sega_101.bin", System: "Saturn", Sizes: []int64{512 << 10}},
	{Name: "mpr-17933.bin", System: "Saturn", Sizes: []int64{512 << 10}},
	{Name: "saturn_bios.bin", System: "Saturn", Sizes: []int64{512 << 10}},
	{Name: "gba_bios.bin", System: "Game Boy Advance", Sizes: []int64{16 << 10}},
	{Name: "gb_bios.bin", System: "Game Boy", Sizes: []int64{256}},
	{Name: "dmg_boot.bin", System: "Game Boy", Sizes: []int64{256}},
	{Name: "gbc_bios.bin", System: "Game Boy Color", Sizes: []int64{2304}},
	{Name: "cgb_boot.bin", System: "Game Boy Color", Sizes: []int64{2304}},
	{Name: "sgb_bios.bin", System: "Super Game Boy", Sizes: []int64{256}},
	{Name: "sgb_boot.bin", System: "Super Game Boy", Sizes: []int64{256}},
	{Name: "bios7.bin", System: "Nintendo DS", Sizes: []int64{16 << 10}},
	{Name: "bios9.bin", System: "Nintendo DS", Sizes: []int64{4 << 10}},
	{Name: "bios_cd_u.bin", System: "Sega CD", Sizes: []int64{128 << 10}},
	{Name: "bios_cd_e.bin", System: "Sega CD", Sizes: []int64{128 << 10}},
	{Name: "bios_cd_j.bin", System: "Sega CD", Sizes: []int64{128 << 10}},
	{Name: "syscard1.pce", System: "PC Engine CD"},
	{Name: "syscard2.pce", System: "PC Engine CD"},
	{Name: "syscard3.pce", System: "PC Engine CD"},
	{Name: "gexpress.pce", System: "PC Engine CD"},
	{Name: "disksys.rom", System: "Famicom Disk System", Sizes: []int64{8 << 10}},
	{Name: "neogeo.zip", System: "Neo Geo"},
	{Name: "lynxboot.img", System: "Lynx", Sizes: []int64{512}},
	{Name: "mcpx_1.0.bin", System: "Xbox", Sub: "xbox", Sizes: []int64{512}},
	{Name: "complex_4627.bin", System: "Xbox", Sub: "xbox", Sizes: []int64{256 << 10, 1 << 20}},
}

// rommBIOSFor reports whether a file is a known firmware image, by its name and
// size.
func rommBIOSFor(name string, size int64) (rommBIOS, bool) {
	lower := strings.ToLower(filepath.Base(name))
	for _, b := range rommBIOSFiles {
		if b.Name != lower {
			continue
		}
		if len(b.Sizes) == 0 {
			return b, true
		}
		for _, s := range b.Sizes {
			if s == size {
				return b, true
			}
		}
		return rommBIOS{}, false
	}
	// PlayStation firmware comes in many model numbers (scph1001, scph5501,
	// scph7502 …), and PlayStation 2 dumps use the same prefix at 4 MiB.
	if strings.HasPrefix(lower, "scph") && strings.HasSuffix(lower, ".bin") {
		switch size {
		case 512 << 10:
			return rommBIOS{Name: lower, System: "PlayStation"}, true
		case 4 << 20:
			return rommBIOS{Name: lower, System: "PlayStation 2"}, true
		}
	}
	return rommBIOS{}, false
}
