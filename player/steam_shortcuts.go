package main

// steam_shortcuts.go — the non-Steam games added to the Steam app on this
// computer ("Add a Non-Steam Game": emulators, other stores' games, tools).
//
// Steam keeps them per account in userdata/<account id>/config/shortcuts.vdf,
// a binary KeyValues file with one block per shortcut: its appid, name,
// program and last-played time. Steam starts one through
// steam://rungameid/<game id>, the 64-bit id made of the shortcut's 32-bit
// appid and the shortcut type, so the link the library uses for Steam games
// starts these too. Their pictures are the ones set in Steam
// (userdata/…/config/grid); Steam's CDN has none for them.
//
// A shortcut's appid always has its top bit set: as an unsigned 32-bit number
// it is at least 2^31, where no Steam app id reaches, so the library tells
// the two apart by the number alone (steamIsShortcut). The account is the one
// the Steam app here is signed in with, never the account signed in to
// JukaHub on the web: the shortcuts belong to this computer's Steam.

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"
)

const (
	// steamShortcutsLimit bounds shortcuts.vdf; a few hundred shortcuts with
	// their tags are well under 1 MB.
	steamShortcutsLimit = 8 << 20
	// steamShortcutsMax bounds the shortcuts read from one file.
	steamShortcutsMax = 2000
	// steamShortcutArtLimit bounds one grid picture the user set in Steam.
	steamShortcutArtLimit = 4 << 20
	// steamShortcutType is the game-id type bits of a shortcut (k_EGameIDTypeShortcut).
	steamShortcutType = 0x02000000
	// binaryVDFMaxDepth bounds the nesting parseBinaryVDF follows.
	binaryVDFMaxDepth = 16
)

// steamIsShortcut reports whether appid is a non-Steam game's id: its 32-bit
// appid read unsigned, 2^31 or more. The int64 keeps the bounds compiling on
// any int size.
func steamIsShortcut(appid int) bool {
	v := int64(appid)
	return v >= 1<<31 && v <= 1<<32-1
}

// steamShortcutGameID is the 64-bit game id steam://rungameid takes for a
// shortcut: its appid in the high half, the shortcut type in the low one.
func steamShortcutGameID(appid int) uint64 {
	return uint64(uint32(appid))<<32 | steamShortcutType
}

// steamShortcutLegacyID is the appid Steam derived for a shortcut before it
// stored one: the CRC-32 of the program and the name, top bit set.
func steamShortcutLegacyID(exe, name string) int {
	return int(crc32.ChecksumIEEE([]byte(exe+name)) | 0x80000000)
}

// steamAccountIDFor is the 32-bit account id (userdata's folder name) of a
// SteamID64, or "".
func steamAccountIDFor(steamID string) string {
	if !steamValidID(steamID) {
		return ""
	}
	id64, err := strconv.ParseUint(steamID, 10, 64)
	if err != nil || id64 < steamAccountIDBase {
		return ""
	}
	return strconv.FormatUint(id64-steamAccountIDBase, 10)
}

// steamShortcutConfigDir is the userdata config folder whose shortcuts.vdf
// lists steamID's shortcuts: that account's, or — when no account is known —
// the one account folder that has shortcuts at all. "" when there is none.
func steamShortcutConfigDir(root, steamID string) string {
	if root == "" {
		return ""
	}
	if acct := steamAccountIDFor(steamID); acct != "" {
		return filepath.Join(root, "userdata", acct, "config")
	}
	matches, _ := filepath.Glob(filepath.Join(root, "userdata", "*", "config", "shortcuts.vdf"))
	if len(matches) == 1 {
		return filepath.Dir(matches[0])
	}
	return ""
}

// steamShortcutGames lists the non-Steam games of steamID (the account the
// Steam app at root uses), in the file's order and always installed. Hidden
// ones and ones with no name are left out.
func steamShortcutGames(root, steamID string) []steamGame {
	dir := steamShortcutConfigDir(root, steamID)
	if dir == "" {
		return nil
	}
	data, err := steamReadSmall(filepath.Join(dir, "shortcuts.vdf"), steamShortcutsLimit)
	if err != nil || len(data) == 0 {
		return nil
	}
	list := parseBinaryVDF(data).child("shortcuts")
	if list == nil {
		return nil
	}
	var games []steamGame
	seen := map[int]bool{}
	for _, k := range list.Order {
		if len(games) >= steamShortcutsMax {
			break
		}
		s := list.Kids[k]
		if s == nil || s.Kids == nil || s.get("ishidden") == "1" {
			continue
		}
		rawName := s.get("appname")
		name := steamCleanText(rawName, 120)
		if name == "" {
			continue
		}
		id := 0
		if v, err := strconv.ParseInt(strings.TrimSpace(s.get("appid")), 10, 64); err == nil && v != 0 {
			id = int(uint32(v))
		}
		if id == 0 {
			id = steamShortcutLegacyID(s.get("exe"), rawName)
		}
		if !steamIsShortcut(id) || seen[id] {
			continue
		}
		seen[id] = true
		g := steamGame{AppID: id, Name: name, Installed: true}
		if v, err := strconv.ParseInt(strings.TrimSpace(s.get("lastplaytime")), 10, 64); err == nil && v != 0 {
			g.LastPlayed = int64(uint32(v))
		}
		games = append(games, g)
	}
	return games
}

// steamShortcutListed finds appid among the shortcuts of the Steam app at
// root (its own signed-in account).
func steamShortcutListed(root string, appid int) (steamGame, bool) {
	if root == "" || !steamIsShortcut(appid) {
		return steamGame{}, false
	}
	user, _ := steamFindLocalUserIn(root)
	for _, g := range steamShortcutGames(root, user.SteamID) {
		if g.AppID == appid {
			return g, true
		}
	}
	return steamGame{}, false
}

// steamShortcutArt is the picture set in Steam for a shortcut: the wide grid
// picture first, then the portrait one (also under the old 64-bit names), or
// nil. JPEG and PNG only.
func steamShortcutArt(root string, appid int) []byte {
	if root == "" || !steamIsShortcut(appid) {
		return nil
	}
	user, _ := steamFindLocalUserIn(root)
	dir := steamShortcutConfigDir(root, user.SteamID)
	if dir == "" {
		return nil
	}
	grid := filepath.Join(dir, "grid")
	id := strconv.FormatUint(uint64(uint32(appid)), 10)
	id64 := strconv.FormatUint(steamShortcutGameID(appid), 10)
	for _, stem := range []string{id, id64, id + "p", id64 + "p"} {
		for _, ext := range []string{".png", ".jpg", ".jpeg"} {
			p := filepath.Join(grid, stem+ext)
			info, err := os.Stat(p)
			if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > steamShortcutArtLimit {
				continue
			}
			if b, err := steamReadSmall(p, steamShortcutArtLimit); err == nil && steamArtLooksLikeImage(b) {
				return b
			}
		}
	}
	return nil
}

// ── Binary KeyValues ─────────────────────────────────────────────────────

// parseBinaryVDF reads Valve's binary KeyValues (shortcuts.vdf): a type byte,
// a NUL-terminated name, then the value — a nested block (0x00) up to its end
// byte (0x08), a NUL-terminated string (0x01), a little-endian int32 (0x02,
// and the 32-bit pointer/colour kinds), a float32 (0x03), a UTF-16 string
// (0x05), a uint64 (0x07) or an int64 (0x0A). Keys are kept lower-case like
// parseVDF's and numbers become decimal strings (int32 signed, as stored). It
// never fails: a truncated file or an unknown type ends the read and keeps
// what came before it.
func parseBinaryVDF(data []byte) *vdfNode {
	if len(data) > steamShortcutsLimit {
		data = data[:steamShortcutsLimit]
	}
	root := &vdfNode{Kids: map[string]*vdfNode{}}
	r := binaryVDFReader{data: data}
	r.readBlock(root, 0)
	return root
}

type binaryVDFReader struct {
	data []byte
	pos  int
}

// cstring reads a NUL-terminated string.
func (r *binaryVDFReader) cstring() (string, bool) {
	if r.pos >= len(r.data) {
		return "", false
	}
	i := bytes.IndexByte(r.data[r.pos:], 0)
	if i < 0 {
		return "", false
	}
	s := string(r.data[r.pos : r.pos+i])
	r.pos += i + 1
	return s, true
}

// take reads n bytes.
func (r *binaryVDFReader) take(n int) ([]byte, bool) {
	if n < 0 || len(r.data)-r.pos < n {
		return nil, false
	}
	b := r.data[r.pos : r.pos+n]
	r.pos += n
	return b, true
}

// wstring reads a UTF-16LE string up to its two-byte NUL.
func (r *binaryVDFReader) wstring() (string, bool) {
	var units []uint16
	for {
		b, ok := r.take(2)
		if !ok {
			return "", false
		}
		u := binary.LittleEndian.Uint16(b)
		if u == 0 {
			return string(utf16.Decode(units)), true
		}
		if len(units) < 1<<16 {
			units = append(units, u)
		}
	}
}

// readBlock reads keys into n up to the block's end byte. It reports false
// when the data stops short or holds a type it does not know; what was read
// stays in n.
func (r *binaryVDFReader) readBlock(n *vdfNode, depth int) bool {
	for r.pos < len(r.data) {
		typ := r.data[r.pos]
		r.pos++
		if typ == 0x08 || typ == 0x0B {
			return true
		}
		name, ok := r.cstring()
		if !ok {
			return false
		}
		node := &vdfNode{}
		switch typ {
		case 0x00:
			if depth+1 >= binaryVDFMaxDepth {
				return false
			}
			node.Kids = map[string]*vdfNode{}
		case 0x01:
			s, ok := r.cstring()
			if !ok {
				return false
			}
			node.Value = s
		case 0x02, 0x04, 0x06:
			b, ok := r.take(4)
			if !ok {
				return false
			}
			node.Value = strconv.FormatInt(int64(int32(binary.LittleEndian.Uint32(b))), 10)
		case 0x03:
			b, ok := r.take(4)
			if !ok {
				return false
			}
			node.Value = strconv.FormatFloat(float64(math.Float32frombits(binary.LittleEndian.Uint32(b))), 'g', -1, 32)
		case 0x05:
			s, ok := r.wstring()
			if !ok {
				return false
			}
			node.Value = s
		case 0x07:
			b, ok := r.take(8)
			if !ok {
				return false
			}
			node.Value = strconv.FormatUint(binary.LittleEndian.Uint64(b), 10)
		case 0x0A:
			b, ok := r.take(8)
			if !ok {
				return false
			}
			node.Value = strconv.FormatInt(int64(binary.LittleEndian.Uint64(b)), 10)
		default:
			return false
		}
		key := strings.ToLower(name)
		if _, seen := n.Kids[key]; !seen {
			n.Order = append(n.Order, key)
		}
		n.Kids[key] = node
		if typ == 0x00 && !r.readBlock(node, depth+1) {
			return false
		}
	}
	return false
}
