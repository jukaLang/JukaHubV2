package main

// steam_add.go — "Add JukaHub to Steam": write a non-Steam shortcut into the
// signed-in Steam account's shortcuts.vdf without touching any existing game.
// Steam's binary KeyValues format is appended to, not round-tripped, so the
// types and values of the other shortcuts survive byte for byte.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// vdfWriteString writes a type-0x01 key: a type byte, NUL-terminated name and
// value.
func vdfWriteString(buf *bytes.Buffer, key, value string) {
	buf.WriteByte(0x01)
	buf.WriteString(key)
	buf.WriteByte(0)
	buf.WriteString(value)
	buf.WriteByte(0)
}

func vdfWriteInt(buf *bytes.Buffer, key string, value int32) {
	buf.WriteByte(0x02)
	buf.WriteString(key)
	buf.WriteByte(0)
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], uint32(value))
	buf.Write(b[:])
}

// steamShortcutBlock builds one shortcut entry. Steam numbers the entries as
// string keys ("0", "1", …) under the root block.
func steamShortcutBlock(index, appid int, name, exe, icon string) []byte {
	var b bytes.Buffer
	b.WriteByte(0x00)
	b.WriteString(strconv.Itoa(index))
	b.WriteByte(0)
	vdfWriteInt(&b, "appid", int32(uint32(appid)))
	vdfWriteString(&b, "appname", name)
	vdfWriteString(&b, "exe", exe)
	vdfWriteString(&b, "icon", icon)
	b.WriteByte(0x00) // tags block
	b.WriteString("tags")
	b.WriteByte(0)
	vdfWriteString(&b, "0", "JukaHub")
	b.WriteByte(0x08)
	vdfWriteInt(&b, "LastPlayTime", 0)
	b.WriteByte(0x08)
	return b.Bytes()
}

// steamNextShortcutIndex reads the existing file's block names so a new
// shortcut never overwrites one already there.
func steamNextShortcutIndex(data []byte) int {
	root := parseBinaryVDF(data)
	if root == nil || root.Kids == nil {
		return 0
	}
	next := 0
	for key := range root.Kids {
		n, err := strconv.Atoi(key)
		if err == nil && n >= next {
			next = n + 1
		}
	}
	return next
}

// steamAppendShortcut inserts a new entry just before the root block's end.
// A missing or corrupt file starts a fresh one.
func steamAppendShortcut(data []byte, exe, icon string) ([]byte, error) {
	if len(data) > steamShortcutsLimit {
		return nil, errors.New("shortcuts.vdf is too large")
	}
	index := steamNextShortcutIndex(data)
	appid := steamShortcutLegacyID(exe, "JukaHub")
	block := steamShortcutBlock(index, appid, "JukaHub", exe, icon)
	out := make([]byte, 0, len(data)+len(block)+1)
	out = append(out, data...)
	// Binary VDF ends with 0x08; strip the root terminator, append our block,
	// then put the terminator back. An unreadable file has no terminator and
	// starts with the block.
	end := bytes.LastIndexByte(out, 0x08)
	if end >= 0 {
		out = out[:end]
	}
	out = append(out, block...)
	out = append(out, 0x08)
	return out, nil
}

// steamAddShortcut writes the shortcut for the given Steam account (a 17-digit
// ID or the 32-bit userdata folder name). The executable and its icon come
// from this running binary.
func steamAddShortcut(steamID string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if abs, err := filepath.Abs(exe); err == nil {
		exe = abs
	}
	dirRoot := steamRoot()
	if dirRoot == "" {
		return errors.New("Steam is not installed on this computer")
	}
	account := steamAccountIDFor(steamID)
	if strings.TrimSpace(steamID) != "" && account == "" {
		// A hand-entered 32-bit account id is the userdata folder name.
		account = strings.TrimSpace(steamID)
	}
	if account == "" {
		// Only numeric userdata folders are real accounts; pick the first one
		// that already holds a shortcuts file, else the first numeric folder.
		matches, _ := filepath.Glob(filepath.Join(dirRoot, "userdata", "*", "config"))
		for _, match := range matches {
			name := filepath.Base(filepath.Dir(match))
			if _, err := strconv.ParseUint(name, 10, 64); err != nil {
				continue
			}
			if regularFile(filepath.Join(match, "shortcuts.vdf")) {
				account = name
				break
			}
			if account == "" {
				account = name
			}
		}
	}
	if account == "" {
		return errors.New("no Steam account found on this computer — pass its ID")
	}
	dir := steamShortcutConfigDir(dirRoot, account)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "shortcuts.vdf")
	old, _ := os.ReadFile(path)
	data, err := steamAppendShortcut(old, exe, exe)
	if err != nil {
		return err
	}
	return AtomicWrite(path, data, 0o644)
}

// handleSteamShortcutCommand is the no-display command form used by the
// installer and by users from a terminal: JukaHub --add-steam-shortcut [id].
func handleSteamShortcutCommand(args []string) (bool, int) {
	if len(args) == 0 || args[0] != "--add-steam-shortcut" {
		return false, 0
	}
	id := ""
	if len(args) > 1 {
		id = strings.TrimSpace(args[1])
	}
	if err := steamAddShortcut(id); err != nil {
		fmt.Fprintln(os.Stderr, "JukaHub: add Steam shortcut:", err)
		return true, 1
	}
	fmt.Println("JukaHub is now in Steam. Restart Steam to see it.")
	return true, 0
}
