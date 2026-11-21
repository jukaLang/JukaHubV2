package main

// raoffline_hash.go — RetroAchievements' game hash for the ROMs whose rule is
// simple, so "Cache recently played" (raoffline_ui.go) can ask the server
// which game a ROM is before RetroArch ever played it online.
//
// RetroAchievements identifies a game by an MD5 that rcheevos computes per
// console (rc_hash.c). For most cartridge consoles it is the MD5 of the whole
// file; a few skip a header first, and N64 dumps are put in big-endian byte
// order. Only those rules are here, as recalled from rcheevos (UNVERIFIED on
// a device: a wrong hash only means "not found" and the game needs one online
// play). Disc images, arcade sets, DS and everything else return "" — their
// hashes read parts of the disc or the file name, and RetroArch's own online
// play caches them instead. A .zip with exactly one known ROM inside is
// hashed by that ROM, as RetroArch hashes the file it extracts.

import (
	"archive/zip"
	"bytes"
	"crypto/md5" // RetroAchievements' game hash is MD5 by protocol
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// raoHashMaxBytes bounds a ROM read for hashing (the largest N64 carts are
// 64 MB). raoHashChunk is how much of it is held at once: the hash is
// streamed, so a big cart never sits in a TrimUI's memory whole. A multiple
// of 4, so the N64 word swaps never straddle two chunks.
const (
	raoHashMaxBytes = 64 << 20
	raoHashChunk    = 64 << 10
)

// raoHashPlain are the extensions hashed as the whole file.
var raoHashPlain = map[string]bool{
	".gb": true, ".gbc": true, ".gba": true, ".md": true, ".gen": true,
	".sms": true, ".gg": true, ".32x": true, ".sg": true, ".col": true,
	".vb": true, ".min": true, ".ws": true, ".wsc": true, ".ngp": true,
	".ngc": true, ".a26": true, ".int": true, ".vec": true, ".pce": true,
	".nes": true, ".fds": true, ".sfc": true, ".smc": true, ".a78": true,
	".lnx": true, ".z64": true, ".n64": true, ".v64": true,
}

// raoRomHash is the RetroAchievements hash of the ROM at path, "" when this
// does not know its console's rule.
func raoRomHash(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".zip" {
		return raoZipHash(path)
	}
	if !raoHashPlain[ext] {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return raoHashStream(ext, f, info.Size())
}

// raoZipHash hashes the one known ROM inside a zip.
func raoZipHash(path string) string {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return ""
	}
	defer zr.Close()
	var only *zip.File
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if only != nil {
			return "" // several files: which one RetroArch loads is its choice
		}
		only = f
	}
	if only == nil || only.UncompressedSize64 > raoHashMaxBytes {
		return ""
	}
	ext := strings.ToLower(filepath.Ext(only.Name))
	if !raoHashPlain[ext] {
		return ""
	}
	rc, err := only.Open()
	if err != nil {
		return ""
	}
	defer rc.Close()
	return raoHashStream(ext, rc, int64(only.UncompressedSize64))
}

// raoHashStream applies a console's rule to a ROM of size bytes read from r.
// "" when the ROM is empty, over raoHashMaxBytes, not size bytes long after
// all, or an N64 dump in no byte order this knows.
func raoHashStream(ext string, r io.Reader, size int64) string {
	if size <= 0 || size > raoHashMaxBytes {
		return ""
	}
	headLen := int64(512)
	if size < headLen {
		headLen = size
	}
	head := make([]byte, headLen)
	if _, err := io.ReadFull(r, head); err != nil {
		return ""
	}
	skip := raoHashSkip(ext, head, size)
	order := 0
	switch ext {
	case ".z64", ".n64", ".v64":
		if order = raoN64Order(head, size); order < 0 {
			return ""
		}
	}
	body := io.MultiReader(bytes.NewReader(head[skip:]), io.LimitReader(r, size-headLen))
	h := md5.New()
	buf := make([]byte, raoHashChunk)
	got := int64(skip)
	for {
		n, err := io.ReadFull(body, buf)
		if n > 0 {
			raoN64Swap(buf[:n], order)
			h.Write(buf[:n])
			got += int64(n)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return ""
		}
	}
	if got != size {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// raoHashSkip is the header a console's rule leaves out, told by the ROM's
// first bytes (head) and its size, as rc_hash.c does. Pure.
func raoHashSkip(ext string, head []byte, size int64) int {
	switch ext {
	case ".nes", ".fds":
		// rc_hash_nes takes either header for both consoles.
		if len(head) > 16 && (bytes.HasPrefix(head, []byte("NES\x1a")) || bytes.HasPrefix(head, []byte("FDS\x1a"))) {
			return 16
		}
	case ".sfc", ".smc":
		// A copier's 512-byte header.
		if size%8192 == 512 {
			return 512
		}
	case ".pce":
		if size%131072 == 512 {
			return 512
		}
	case ".a78":
		if len(head) > 128 && bytes.Equal(head[1:10], []byte("ATARI7800")) {
			return 128
		}
	case ".lnx":
		if len(head) > 64 && bytes.HasPrefix(head, []byte("LYNX\x00")) {
			return 64
		}
	}
	return 0
}

// raoN64Order tells an N64 dump's byte order by its first word: 0 for
// big-endian (.z64, hashed as it is), 2 for byte-swapped (.v64), 4 for
// little-endian (.n64); -1 for none of them. Pure.
func raoN64Order(head []byte, size int64) int {
	if len(head) < 4 || size%4 != 0 {
		return -1
	}
	switch {
	case head[0] == 0x80 && head[1] == 0x37 && head[2] == 0x12 && head[3] == 0x40:
		return 0
	case head[0] == 0x37 && head[1] == 0x80 && head[2] == 0x40 && head[3] == 0x12:
		return 2
	case head[0] == 0x40 && head[1] == 0x12 && head[2] == 0x37 && head[3] == 0x80:
		return 4
	}
	return -1
}

// raoN64Swap puts a chunk of an N64 dump in big-endian order, in place.
// The chunk starts on a word boundary and is whole words long.
func raoN64Swap(b []byte, order int) {
	switch order {
	case 2:
		for i := 0; i+1 < len(b); i += 2 {
			b[i], b[i+1] = b[i+1], b[i]
		}
	case 4:
		for i := 0; i+3 < len(b); i += 4 {
			b[i], b[i+1], b[i+2], b[i+3] = b[i+3], b[i+2], b[i+1], b[i]
		}
	}
}
