package main

// chiptune_meta.go — what a soundtrack file says about itself.
//
// Game music files are not recordings: an NSF is the NES game's own sound
// driver plus its music data, an SPC a snapshot of the SNES sound chip's RAM,
// a VGM a log of the writes a Mega Drive made to its chips. The player cannot
// ask ffprobe for their titles and lengths (libgme reports one song and loops
// it for ever), so the headers are read here, in Go, from the bytes:
//
//   - NSF: song count, starting song, game, artist, copyright (128-byte
//     header); NSF2 files may carry NSFe chunks after the program.
//   - NSFE: INFO, auth, tlbl (song titles), time and fade (per song, in ms),
//     plst (the play order libgme follows).
//   - GBS: song count, first song, title, author, copyright.
//   - SPC: ID666 — song, game, artist, length and fade, text or binary form.
//   - VGM/VGZ: the header's sample counts (length, loop) and the GD3 tag
//     (UTF-16: track, game, system, author); VGZ is gzip.
//   - GYM: the optional GYMX header, and the length counted in frames.
//   - AY (ZXAYEMUL): songs with names, lengths and fades.
//   - SAP: AUTHOR/NAME/DATE/SONGS/DEFSONG/TIME lines.
//   - HES and KSS have no song list libgme agrees on: one entry each.
//   - SID: sidplay_psid.go reads the PSID header.
//
// Every parser works on bytes it was handed and never trusts a length or an
// offset from the file. Results are cached per file (size and modification
// time), so a playlist of a hundred NSFs costs a hundred small reads once.

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	// chiptuneDefaultSeconds is the play time of a song whose file names none
	// (libgme's own default is 2:30); chiptuneDefaultFade is the fade added
	// after a song that loops.
	chiptuneDefaultSeconds = 150.0
	chiptuneDefaultFade    = 8.0
	// chiptuneMaxSeconds caps a length read from a file: a broken tag must not
	// make one song last a day.
	chiptuneMaxSeconds = 30 * 60.0
	// chiptuneMaxRead bounds how much of one file is ever read for its tags
	// (a VGZ is decompressed in memory; the biggest VGMs are a few MB).
	chiptuneMaxRead = 32 << 20
	// chiptuneMetaCacheMax bounds the per-file cache.
	chiptuneMetaCacheMax = 1024
)

var errChiptuneHeader = errors.New("not a soundtrack file this player understands")

// chiptuneMeta is one file's tags. Song numbers are 1-based everywhere
// outside libgme's track_index.
type chiptuneMeta struct {
	Format    string // "NSF", "SPC", "VGM", "SID", …
	System    string // "NES", "SNES", "Game Boy", "Mega Drive", "C64", …
	Game      string
	Artist    string
	Copyright string
	Songs     int // 1 for a single-song file
	Start     int // the song a player starts with
	// Base is libgme's track index of song 1 (HES names its first song).
	Base int
	// Per song (index n-1), each slice nil or Songs long.
	Titles []string
	Secs   []float64 // play time before the fade; 0 = unknown
	Fades  []float64 // fade; -1 = the default for a looping song
}

// title is song n's own title, "" when the file names none.
func (m chiptuneMeta) title(n int) string {
	if n >= 1 && n <= len(m.Titles) {
		return strings.TrimSpace(m.Titles[n-1])
	}
	return ""
}

// timing is song n's play time and fade in seconds: the file's own when it
// has them, else the defaults.
func (m chiptuneMeta) timing(n int) (secs, fade float64) {
	secs, fade = 0, -1
	if n >= 1 && n <= len(m.Secs) {
		secs = m.Secs[n-1]
	}
	if n >= 1 && n <= len(m.Fades) {
		fade = m.Fades[n-1]
	}
	// Written so a NaN (a SAP "TIME nan") takes the default too.
	if !(secs > 0 && secs <= chiptuneMaxSeconds) {
		secs = chiptuneDefaultSeconds - chiptuneDefaultFade
	}
	if !(fade >= 0) {
		fade = chiptuneDefaultFade
	}
	if fade > 60 {
		fade = 60
	}
	return secs, fade
}

// seconds is song n's whole length, fade included.
func (m chiptuneMeta) seconds(n int) float64 {
	secs, fade := m.timing(n)
	return secs + fade
}

// track is libgme's track_index for song n.
func (m chiptuneMeta) track(n int) int {
	if n < 1 {
		n = 1
	}
	return m.Base + n - 1
}

// ── Reading a file ───────────────────────────────────────────────────────────

type chiptuneMetaEntry struct {
	size int64
	mod  time.Time
	meta chiptuneMeta
	err  error
}

var chiptuneMetaCache struct {
	mu     sync.Mutex
	byFile map[string]chiptuneMetaEntry
}

// chiptuneMetaFor returns the tags of the file behind path (a song address
// "x.nsf#3" reads x.nsf). It reads the disk on a miss: not for the render
// thread, which uses chiptuneMetaCached.
func chiptuneMetaFor(path string) (chiptuneMeta, bool) {
	file := chiptuneFileOf(path)
	info, err := os.Stat(file)
	if err != nil || !info.Mode().IsRegular() {
		return chiptuneMeta{}, false
	}
	key := dlPathKey(file)
	chiptuneMetaCache.mu.Lock()
	if e, ok := chiptuneMetaCache.byFile[key]; ok && e.size == info.Size() && e.mod.Equal(info.ModTime()) {
		chiptuneMetaCache.mu.Unlock()
		return e.meta, e.err == nil
	}
	chiptuneMetaCache.mu.Unlock()

	meta, err := chiptuneReadMeta(file, info.Size())
	if err != nil {
		logInfo("chiptune", "%s: %v", filepath.Base(file), err)
	}
	chiptuneMetaCache.mu.Lock()
	if chiptuneMetaCache.byFile == nil || len(chiptuneMetaCache.byFile) >= chiptuneMetaCacheMax {
		chiptuneMetaCache.byFile = map[string]chiptuneMetaEntry{}
	}
	chiptuneMetaCache.byFile[key] = chiptuneMetaEntry{size: info.Size(), mod: info.ModTime(), meta: meta, err: err}
	chiptuneMetaCache.mu.Unlock()
	return meta, err == nil
}

// chiptuneMetaCached is chiptuneMetaFor without touching the disk: the render
// thread asks it, and the decoder (which reads the file anyway) fills it.
func chiptuneMetaCached(path string) (chiptuneMeta, bool) {
	key := dlPathKey(chiptuneFileOf(path))
	chiptuneMetaCache.mu.Lock()
	defer chiptuneMetaCache.mu.Unlock()
	e, ok := chiptuneMetaCache.byFile[key]
	return e.meta, ok && e.err == nil
}

// chiptuneReadMeta reads only what each format needs: a header for most, a
// chunk walk for NSFE, the header and the GD3 tag for VGM, the whole
// (decompressed) stream for VGZ and GYM.
func chiptuneReadMeta(file string, size int64) (chiptuneMeta, error) {
	f, err := os.Open(file)
	if err != nil {
		return chiptuneMeta{}, err
	}
	defer f.Close()
	ext := strings.ToLower(filepath.Ext(file))
	head := func(n int) []byte {
		if int64(n) > size {
			n = int(size)
		}
		buf := make([]byte, n)
		k, _ := io.ReadFull(f, buf)
		return buf[:k]
	}
	switch ext {
	case ".nsf":
		m, err := chiptuneParseNSF(head(0x80))
		if err == nil {
			chiptuneNSF2Chunks(&m, f, size)
		}
		return m, err
	case ".nsfe":
		return chiptuneParseNSFE(f, size)
	case ".gbs":
		return chiptuneParseGBS(head(0x70))
	case ".spc":
		return chiptuneParseSPC(head(0x100))
	case ".vgm":
		return chiptuneParseVGM(f, size)
	case ".vgz":
		data, err := chiptuneGunzip(f)
		if err != nil {
			return chiptuneMeta{}, err
		}
		return chiptuneParseVGM(bytes.NewReader(data), int64(len(data)))
	case ".gym":
		return chiptuneParseGYM(head(chiptuneMaxRead))
	case ".ay":
		return chiptuneParseAY(head(1 << 20))
	case ".sap":
		return chiptuneParseSAP(head(16 << 10))
	case ".hes":
		return chiptuneParseHES(head(0x20))
	case ".kss":
		return chiptuneParseKSS(head(0x20))
	case ".sid", ".psid":
		t, err := sidParse(head(sidMaxFile))
		if err != nil {
			return chiptuneMeta{}, err
		}
		return t.meta(), nil
	}
	return chiptuneMeta{}, errChiptuneHeader
}

// chiptuneGunzip decompresses a VGZ (bounded).
func chiptuneGunzip(r io.Reader) ([]byte, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	data, err := io.ReadAll(io.LimitReader(zr, chiptuneMaxRead))
	if err != nil && len(data) == 0 {
		return nil, err
	}
	return data, nil
}

// ── Text helpers ─────────────────────────────────────────────────────────────

// chiptuneText is a fixed-size, NUL-padded text field. Rips are ASCII, UTF-8
// or an 8-bit code page; invalid UTF-8 is read as Latin-1 rather than dropped.
func chiptuneText(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	s := string(b)
	if !utf8.ValidString(s) {
		r := make([]rune, len(b))
		for i, c := range b {
			r[i] = rune(c)
		}
		s = string(r)
	}
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if s == "<?>" || s == "?" {
		return ""
	}
	return s
}

// chiptuneCStrings splits NUL-terminated strings.
func chiptuneCStrings(b []byte) []string {
	var out []string
	for len(b) > 0 {
		i := bytes.IndexByte(b, 0)
		if i < 0 {
			out = append(out, chiptuneText(b))
			break
		}
		out = append(out, chiptuneText(b[:i]))
		b = b[i+1:]
	}
	return out
}

func chiptuneField(b []byte, off, n int) string {
	if off < 0 || off >= len(b) {
		return ""
	}
	end := off + n
	if end > len(b) {
		end = len(b)
	}
	return chiptuneText(b[off:end])
}

// chiptuneSongs bounds a song count and a starting song read from a header.
func chiptuneSongs(count, start int) (int, int) {
	if count < 1 {
		count = 1
	}
	if count > 256 {
		count = 256
	}
	if start < 1 || start > count {
		start = 1
	}
	return count, start
}

// ── NES ──────────────────────────────────────────────────────────────────────

// chiptuneParseNSF reads the 128-byte NSF header.
func chiptuneParseNSF(h []byte) (chiptuneMeta, error) {
	if len(h) < 0x80 || !bytes.Equal(h[:5], []byte("NESM\x1a")) {
		return chiptuneMeta{}, errChiptuneHeader
	}
	songs, start := chiptuneSongs(int(h[6]), int(h[7]))
	return chiptuneMeta{Format: "NSF", System: "NES", Songs: songs, Start: start,
		Game: chiptuneField(h, 0x0E, 32), Artist: chiptuneField(h, 0x2E, 32),
		Copyright: chiptuneField(h, 0x4E, 32)}, nil
}

// chiptuneNSF2Chunks reads the NSFe-style chunks an NSF2 file may carry after
// its program data: song titles and times.
func chiptuneNSF2Chunks(m *chiptuneMeta, f io.ReaderAt, size int64) {
	var h [0x80]byte
	if _, err := f.ReadAt(h[:], 0); err != nil || h[5] < 2 {
		return
	}
	progLen := int64(h[0x7D]) | int64(h[0x7E])<<8 | int64(h[0x7F])<<16
	if progLen == 0 || 0x80+progLen >= size {
		return
	}
	chunks := chiptuneNSFEChunks(f, 0x80+progLen, size)
	chiptuneApplyNSFEChunks(m, chunks)
}

// chiptuneNSFEChunks walks NSFe chunks from off, keeping the small ones that
// carry tags. Each chunk is a little-endian length, a four-letter id and the
// data; DATA (the program) is skipped by seeking past it.
func chiptuneNSFEChunks(f io.ReaderAt, off, size int64) map[string][]byte {
	out := map[string][]byte{}
	for i := 0; i < 64 && off+8 <= size; i++ {
		var hdr [8]byte
		if _, err := f.ReadAt(hdr[:], off); err != nil {
			break
		}
		n := int64(binary.LittleEndian.Uint32(hdr[:4]))
		id := string(hdr[4:8])
		off += 8
		if n < 0 || off+n > size {
			break
		}
		if id == "NEND" {
			break
		}
		switch id {
		case "INFO", "auth", "tlbl", "time", "fade", "plst":
			if n <= 64<<10 {
				buf := make([]byte, n)
				if _, err := f.ReadAt(buf, off); err == nil {
					out[id] = buf
				}
			}
		}
		off += n
	}
	return out
}

// chiptuneParseNSFE reads an NSFE file's chunks.
func chiptuneParseNSFE(f io.ReaderAt, size int64) (chiptuneMeta, error) {
	var magic [4]byte
	if _, err := f.ReadAt(magic[:], 0); err != nil || string(magic[:]) != "NSFE" {
		return chiptuneMeta{}, errChiptuneHeader
	}
	chunks := chiptuneNSFEChunks(f, 4, size)
	info := chunks["INFO"]
	if len(info) < 8 {
		return chiptuneMeta{}, errChiptuneHeader
	}
	count, first := 1, 0
	if len(info) >= 9 {
		count = int(info[8])
	}
	if len(info) >= 10 {
		first = int(info[9]) // 0-based here
	}
	songs, start := chiptuneSongs(count, first+1)
	m := chiptuneMeta{Format: "NSFE", System: "NES", Songs: songs, Start: start}
	chiptuneApplyNSFEChunks(&m, chunks)
	return m, nil
}

// chiptuneApplyNSFEChunks folds auth/tlbl/time/fade/plst into m. With a plst
// chunk libgme plays the songs in that order, so the list follows it too.
func chiptuneApplyNSFEChunks(m *chiptuneMeta, chunks map[string][]byte) {
	if auth := chiptuneCStrings(chunks["auth"]); len(auth) > 0 {
		m.Game = dlFirstNonEmpty(auth[0], m.Game)
		if len(auth) > 1 {
			m.Artist = dlFirstNonEmpty(auth[1], m.Artist)
		}
		if len(auth) > 2 {
			m.Copyright = dlFirstNonEmpty(auth[2], m.Copyright)
		}
	}
	titles := chiptuneCStrings(chunks["tlbl"])
	times := chunks["time"]
	fades := chunks["fade"]
	songTitle := func(i int) string {
		if i >= 0 && i < len(titles) {
			return titles[i]
		}
		return ""
	}
	ms := func(b []byte, i int, unknown float64) float64 {
		if i < 0 || 4*i+4 > len(b) {
			return unknown
		}
		v := int32(binary.LittleEndian.Uint32(b[4*i:]))
		if v < 0 {
			return unknown
		}
		return float64(v) / 1000
	}
	// libgme's track_index counts plst entries when there is a plst, so song
	// n is the n-th entry and its tags are those of the song it names.
	// An entry naming no song ends the list (skipping it would shift every
	// later row against libgme's numbers), and the list is bounded like a
	// header's song count: a 64 KB plst must not become 65536 rows.
	raw := m.Songs
	order := make([]int, 0, raw)
	for _, b := range chunks["plst"] {
		if int(b) >= raw || len(order) >= 256 {
			break
		}
		order = append(order, int(b))
	}
	if len(order) > 0 {
		m.Songs, m.Start = len(order), 1
	} else {
		for i := 0; i < raw; i++ {
			order = append(order, i)
		}
	}
	m.Titles = make([]string, m.Songs)
	m.Secs = make([]float64, m.Songs)
	m.Fades = make([]float64, m.Songs)
	for n, src := range order {
		m.Titles[n] = songTitle(src)
		m.Secs[n] = ms(times, src, 0)
		m.Fades[n] = ms(fades, src, -1)
	}
}

// ── Game Boy ─────────────────────────────────────────────────────────────────

func chiptuneParseGBS(h []byte) (chiptuneMeta, error) {
	if len(h) < 0x70 || string(h[:3]) != "GBS" {
		return chiptuneMeta{}, errChiptuneHeader
	}
	songs, start := chiptuneSongs(int(h[4]), int(h[5]))
	return chiptuneMeta{Format: "GBS", System: "Game Boy", Songs: songs, Start: start,
		Game: chiptuneField(h, 0x10, 32), Artist: chiptuneField(h, 0x30, 32),
		Copyright: chiptuneField(h, 0x50, 32)}, nil
}

// ── SNES ─────────────────────────────────────────────────────────────────────

// chiptuneDigits reads a text number field: digits, then NULs or spaces. ok
// is false when the field is not text (the binary ID666 form).
func chiptuneDigits(b []byte) (int, bool) {
	n, digits := 0, 0
	for i, c := range b {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
			digits++
			continue
		}
		if c != 0 && c != ' ' {
			return 0, false
		}
		// Padding: nothing but padding may follow it.
		for _, rest := range b[i+1:] {
			if rest != 0 && rest != ' ' {
				return 0, false
			}
		}
		break
	}
	return n, digits > 0
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// chiptuneParseSPC reads an SPC's ID666 tag. The tag has a text and a binary
// form that the format does not flag; the length field decides, as libgme
// does: three ASCII digits are text.
func chiptuneParseSPC(h []byte) (chiptuneMeta, error) {
	if len(h) < 0x100 || !strings.HasPrefix(string(h[:27]), "SNES-SPC700 Sound File Data") {
		return chiptuneMeta{}, errChiptuneHeader
	}
	m := chiptuneMeta{Format: "SPC", System: "SNES", Songs: 1, Start: 1,
		Titles: []string{""}, Secs: []float64{0}, Fades: []float64{-1}}
	if h[0x23] != 26 {
		return m, nil // no ID666 tag
	}
	m.Titles[0] = chiptuneField(h, 0x2E, 32)
	m.Game = chiptuneField(h, 0x4E, 32)
	secs, text := chiptuneDigits(h[0xA9:0xAC])
	fadeMs := -1
	if text {
		if f, ok := chiptuneDigits(h[0xAC:0xB1]); ok {
			fadeMs = f
		}
	} else if !allZero(h[0xA9:0xAC]) {
		secs = int(h[0xA9]) | int(h[0xAA])<<8 | int(h[0xAB])<<16
		if f := int(binary.LittleEndian.Uint32(h[0xAC:0xB0])); f >= 0 && f <= 600000 {
			fadeMs = f
		}
	}
	// libgme's rule for the artist: at 0xB0 in the binary form, one byte on
	// (0xB1) in the text form, whose fade's fifth byte is a digit or a NUL.
	authorAt := 0xB0
	if c := h[0xB0]; c < ' ' || (c >= '0' && c <= '9') {
		authorAt = 0xB1
	}
	if secs > 0 && secs < 0x1FFF {
		m.Secs[0] = float64(secs)
	}
	if fadeMs >= 0 && m.Secs[0] > 0 {
		m.Fades[0] = float64(fadeMs) / 1000
	}
	m.Artist = chiptuneField(h, authorAt, 32)
	return m, nil
}

// ── Sega (VGM, GYM) ──────────────────────────────────────────────────────────

// chiptuneVGMSystems maps a GD3 system name to the Games library's console
// names (game_library.go), so the cover lookup finds the game.
var chiptuneVGMSystems = []struct{ match, system string }{
	{"32x", "32X"},
	{"mega-cd", "Sega CD"}, {"mega cd", "Sega CD"}, {"sega cd", "Sega CD"},
	{"mega drive", "Mega Drive"}, {"megadrive", "Mega Drive"}, {"genesis", "Mega Drive"},
	{"master system", "Master System"}, {"mark iii", "Master System"},
	{"game gear", "Game Gear"},
	{"game boy", "Game Boy"},
	{"famicom", "NES"}, {"nes", "NES"},
	{"pc engine", "PC Engine"}, {"turbografx", "PC Engine"},
	{"neo geo", "Neo Geo"}, {"neogeo", "Neo Geo"},
	{"msx", "MSX"},
	{"arcade", "Arcade"},
}

func chiptuneVGMSystem(gd3 string) string {
	low := strings.ToLower(gd3)
	for _, s := range chiptuneVGMSystems {
		if strings.Contains(low, s.match) {
			return s.system
		}
	}
	return strings.TrimSpace(gd3)
}

// chiptuneParseVGM reads a VGM header and its GD3 tag. Lengths are in
// samples at 44.1 kHz; a looping song plays its intro and the loop twice and
// fades, one that ends stops at its end.
func chiptuneParseVGM(r io.ReaderAt, size int64) (chiptuneMeta, error) {
	h := make([]byte, 0x100)
	n, _ := r.ReadAt(h, 0)
	h = h[:n]
	if len(h) < 0x40 || string(h[:4]) != "Vgm " {
		return chiptuneMeta{}, errChiptuneHeader
	}
	u32 := func(off int) uint32 {
		if off+4 > len(h) {
			return 0
		}
		return binary.LittleEndian.Uint32(h[off:])
	}
	version := u32(0x08)
	// Fields past 0x34 exist only up to where the data starts.
	headerEnd := 0x40
	if version >= 0x150 {
		if rel := u32(0x34); rel > 0 {
			headerEnd = int(0x34 + rel)
		}
	}
	chip := func(off int) bool { return off+4 <= headerEnd && u32(off)&0x3FFFFFFF != 0 }
	m := chiptuneMeta{Format: "VGM", Songs: 1, Start: 1,
		Titles: []string{""}, Secs: []float64{0}, Fades: []float64{0}}
	total, loop := float64(u32(0x18))/44100, float64(u32(0x20))/44100
	if total > 0 {
		m.Secs[0] = total
		if loop > 0 {
			m.Secs[0] = total + loop
			m.Fades[0] = chiptuneDefaultFade
		}
	} else {
		m.Fades[0] = -1
	}
	switch {
	case chip(0x70):
		m.System = "32X"
	case chip(0x2C) && version >= 0x110:
		m.System = "Mega Drive"
	case chip(0x80):
		m.System = "Game Boy"
	case chip(0x84):
		m.System = "NES"
	case chip(0xA4):
		m.System = "PC Engine"
	case chip(0x30) && version >= 0x110:
		m.System = "Arcade"
	case chip(0x0C):
		m.System = "Master System"
	}
	if rel := int64(u32(0x14)); rel > 0 && 0x14+rel+12 <= size {
		off := 0x14 + rel
		var gh [12]byte
		if _, err := r.ReadAt(gh[:], off); err == nil && string(gh[:4]) == "Gd3 " {
			ln := int64(binary.LittleEndian.Uint32(gh[8:]))
			if ln > 0 && ln <= 64<<10 && off+12+ln <= size {
				buf := make([]byte, ln)
				if _, err := r.ReadAt(buf, off+12); err == nil {
					tags := chiptuneUTF16Strings(buf)
					get := func(i int) string {
						if i < len(tags) {
							return tags[i]
						}
						return ""
					}
					m.Titles[0] = dlFirstNonEmpty(get(0), get(1))
					m.Game = dlFirstNonEmpty(get(2), get(3))
					if sys := get(4); sys != "" {
						m.System = chiptuneVGMSystem(sys)
					}
					m.Artist = dlFirstNonEmpty(get(6), get(7))
					m.Copyright = get(8)
				}
			}
		}
	}
	if m.System == "" {
		m.System = "Sega"
	}
	return m, nil
}

// chiptuneUTF16Strings splits GD3's NUL-terminated UTF-16LE strings.
func chiptuneUTF16Strings(b []byte) []string {
	var out []string
	var cur []uint16
	for i := 0; i+1 < len(b); i += 2 {
		c := binary.LittleEndian.Uint16(b[i:])
		if c == 0 {
			out = append(out, strings.TrimSpace(string(utf16.Decode(cur))))
			cur = cur[:0]
			continue
		}
		cur = append(cur, c)
	}
	if len(cur) > 0 {
		out = append(out, strings.TrimSpace(string(utf16.Decode(cur))))
	}
	return out
}

// chiptuneParseGYM reads a GYM: the GYMX header when there is one, and the
// length counted from the stream's frame waits (60 a second).
func chiptuneParseGYM(data []byte) (chiptuneMeta, error) {
	m := chiptuneMeta{Format: "GYM", System: "Mega Drive", Songs: 1, Start: 1,
		Titles: []string{""}, Secs: []float64{0}, Fades: []float64{0}}
	body := data
	loopFrame := uint32(0)
	if len(data) >= 0x1AC && string(data[:4]) == "GYMX" {
		m.Titles[0] = chiptuneField(data, 0x04, 32)
		m.Game = chiptuneField(data, 0x24, 32)
		m.Copyright = chiptuneField(data, 0x44, 32)
		loopFrame = binary.LittleEndian.Uint32(data[0x1A4:])
		if binary.LittleEndian.Uint32(data[0x1A8:]) != 0 {
			m.Fades[0] = -1 // packed: no length without inflating it
			return m, nil
		}
		body = data[0x1AC:]
	}
	frames := 0
	for i := 0; i < len(body); {
		switch body[i] {
		case 0:
			frames++
			i++
		case 1, 2:
			i += 3
		case 3:
			i += 2
		default:
			// Not a GYM command stream.
			if frames == 0 {
				return chiptuneMeta{}, errChiptuneHeader
			}
			i = len(body)
		}
	}
	if frames == 0 {
		return chiptuneMeta{}, errChiptuneHeader
	}
	m.Secs[0] = float64(frames) / 60
	if loopFrame > 0 && int(loopFrame) < frames {
		m.Secs[0] += float64(frames-int(loopFrame)) / 60
		m.Fades[0] = chiptuneDefaultFade
	}
	return m, nil
}

// ── ZX Spectrum (AY), Atari 8-bit (SAP), PC Engine (HES), MSX (KSS) ─────────

// chiptuneParseAY reads a ZXAYEMUL file: big-endian pointers relative to the
// pointer's own position, song lengths and fades in 1/50 s.
func chiptuneParseAY(d []byte) (chiptuneMeta, error) {
	if len(d) < 0x14 || string(d[:8]) != "ZXAYEMUL" {
		return chiptuneMeta{}, errChiptuneHeader
	}
	ptr := func(at int) int {
		if at < 0 || at+2 > len(d) {
			return -1
		}
		p := at + int(int16(binary.BigEndian.Uint16(d[at:])))
		if p < 0 || p >= len(d) {
			return -1
		}
		return p
	}
	str := func(p int) string {
		if p < 0 {
			return ""
		}
		end := p + 256
		if end > len(d) {
			end = len(d)
		}
		return chiptuneText(d[p:end])
	}
	songs, start := chiptuneSongs(int(d[0x10])+1, int(d[0x11])+1)
	m := chiptuneMeta{Format: "AY", System: "ZX Spectrum", Songs: songs, Start: start,
		Artist: str(ptr(0x0C)), Game: str(ptr(0x0E)),
		Titles: make([]string, songs), Secs: make([]float64, songs), Fades: make([]float64, songs)}
	list := ptr(0x12)
	for i := 0; i < songs; i++ {
		m.Fades[i] = -1
		if list < 0 {
			continue
		}
		entry := list + 4*i
		if entry+4 > len(d) {
			break
		}
		m.Titles[i] = str(ptr(entry))
		if data := ptr(entry + 2); data >= 0 && data+8 <= len(d) {
			if frames := binary.BigEndian.Uint16(d[data+4:]); frames > 0 {
				m.Secs[i] = float64(frames) / 50
			}
			fade := binary.BigEndian.Uint16(d[data+6:])
			m.Fades[i] = float64(fade) / 50
		}
	}
	return m, nil
}

// chiptuneParseSAP reads a SAP's text header (CR LF lines up to FF FF).
func chiptuneParseSAP(d []byte) (chiptuneMeta, error) {
	if len(d) < 5 || !strings.HasPrefix(string(d), "SAP\r\n") {
		return chiptuneMeta{}, errChiptuneHeader
	}
	if i := bytes.Index(d, []byte{0xFF, 0xFF}); i >= 0 {
		d = d[:i]
	}
	m := chiptuneMeta{Format: "SAP", System: "Atari 8-bit"}
	count, def := 1, 0
	var times []float64
	var loops []bool
	unquote := func(s string) string {
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(s, "\"")
		s = strings.TrimSuffix(s, "\"")
		return chiptuneText([]byte(s))
	}
	for _, line := range strings.Split(string(d), "\n") {
		line = strings.TrimSpace(line)
		key, val, _ := strings.Cut(line, " ")
		switch strings.ToUpper(key) {
		case "AUTHOR":
			m.Artist = unquote(val)
		case "NAME":
			m.Game = unquote(val)
		case "DATE":
			m.Copyright = unquote(val)
		case "SONGS":
			count, _ = strconv.Atoi(strings.TrimSpace(val))
		case "DEFSONG":
			def, _ = strconv.Atoi(strings.TrimSpace(val))
		case "TIME":
			f := strings.Fields(val)
			secs := 0.0
			if len(f) > 0 {
				secs = chiptuneClock(f[0])
			}
			times = append(times, secs)
			loops = append(loops, len(f) > 1 && strings.EqualFold(f[1], "LOOP"))
		}
	}
	m.Songs, m.Start = chiptuneSongs(count, def+1)
	m.Titles = make([]string, m.Songs)
	m.Secs = make([]float64, m.Songs)
	m.Fades = make([]float64, m.Songs)
	for i := range m.Fades {
		m.Fades[i] = -1
		if i < len(times) && times[i] > 0 {
			m.Secs[i] = times[i]
			if !loops[i] {
				m.Fades[i] = 0
			}
		}
	}
	return m, nil
}

// chiptuneClock reads "mm:ss.xxx" (or "ss.xxx") into seconds.
func chiptuneClock(s string) float64 {
	secs := 0.0
	for _, part := range strings.Split(s, ":") {
		v, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil || !(v >= 0 && v < 1e6) { // also NaN and Inf
			return 0
		}
		secs = secs*60 + v
	}
	return secs
}

// chiptuneParseHES reads a HES header. HES names no song count (libgme offers
// 256 tracks, most of them silence), so the file is one song: the one its
// header starts with.
func chiptuneParseHES(h []byte) (chiptuneMeta, error) {
	if len(h) < 0x10 || string(h[:4]) != "HESM" {
		return chiptuneMeta{}, errChiptuneHeader
	}
	return chiptuneMeta{Format: "HES", System: "PC Engine", Songs: 1, Start: 1, Base: int(h[5])}, nil
}

// chiptuneParseKSS reads a KSS header (MSX, or Sega 8-bit when its SN76489
// flag is set). Like HES it is one song.
func chiptuneParseKSS(h []byte) (chiptuneMeta, error) {
	if len(h) < 0x10 || (string(h[:4]) != "KSCC" && string(h[:4]) != "KSSX") {
		return chiptuneMeta{}, errChiptuneHeader
	}
	system := "MSX"
	if h[0x0F]&0x02 != 0 {
		system = "Master System"
	}
	return chiptuneMeta{Format: "KSS", System: system, Songs: 1, Start: 1}, nil
}
