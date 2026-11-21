package main

// collections_nextui.go — NextUI's collections, shared both ways.
//
// NextUI (and MinUI before it) keeps a collection as a text file,
// <card>/Collections/<name>.txt, one game per line as a path from the card
// root ("/Roms/Game Boy (GB)/Tetris.gb"). With Settings → Games & emulators →
// "Share collections with NextUI" on (it starts on where the card has a
// Collections folder or NextUI itself):
//
//   - every .txt there is a JukaHub collection named after the file, in the
//     file's order. A line whose game is not on the card is not shown, but it
//     is kept, at its place, for the next write: nothing NextUI listed is lost;
//   - a collection that came from a .txt writes back to it whenever it changes
//     here — through a temp file and a rename, in the file's own line endings
//     and path style, each game written as the very line it was read from;
//   - a collection made in JukaHub gets a .txt of its own;
//   - a .txt changed outside JukaHub (NextUI, a PC) is read again rather than
//     overwritten: its size, time and SHA-256 from the last read or write are
//     kept in collections-nextui.json in the data folder;
//   - a collection deleted here moves its .txt to Collections/.deleted (NextUI
//     does not list hidden folders); a .txt deleted outside only unlinks the
//     collection, which stays here and is not written out again until it
//     changes here.
//
// Items NextUI cannot hold (apps, consoles, games off the card) stay in the
// JukaHub collection only. The automatic collections (autocoll.go) are never
// written out. The same file also stamps when each collection last changed
// here, which Device Sync's merge reads (devsync_data.go).

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	// colNextuiKey is the switch: "on", "off", or unset (automatic).
	colNextuiKey      = "nextuiCollections"
	colNextuiMetaName = "collections-nextui.json"
	colNextuiDirName  = "Collections"
	colNextuiTrashDir = ".deleted"
	// colNextuiMaxFile bounds one .txt: 500 games of long paths fit easily.
	colNextuiMaxFile = 256 << 10
)

// colNextuiKept is a line whose game is not on the card, and its place.
type colNextuiKept struct {
	At   int    `json:"at"`
	Line string `json:"line"`
}

// colNextuiLink ties a collection to its .txt: the file as JukaHub last read
// or wrote it, and how to write it again in the same form.
type colNextuiLink struct {
	File       string            `json:"file"`
	Size       int64             `json:"size"`
	MTime      int64             `json:"mtime"`
	SHA        string            `json:"sha256"`
	CRLF       bool              `json:"crlf,omitempty"`
	NoSlash    bool              `json:"noslash,omitempty"`
	NoTrailing bool              `json:"notrailing,omitempty"`
	Lines      map[string]string `json:"lines,omitempty"`
	Kept       []colNextuiKept   `json:"kept,omitempty"`
}

// colNextuiMeta is collections-nextui.json. Sigs and Changed are kept for
// every collection, shared with NextUI or not: Changed is when it last
// changed on this device (Unix seconds). Detached holds the collections whose
// .txt was deleted outside JukaHub, with their sig then: such a collection is
// not written out again until it changes here.
type colNextuiMeta struct {
	Version  int                       `json:"version"`
	Links    map[string]*colNextuiLink `json:"links,omitempty"`
	Sigs     map[string]string         `json:"sigs,omitempty"`
	Changed  map[string]int64          `json:"changed,omitempty"`
	Detached map[string]string         `json:"detached,omitempty"`
}

// colNextui is the loaded meta. seen holds the collections this session has
// seen, so a link whose collection vanished is told apart: deleted here now
// (its file goes to .deleted) or lost with the store (the file is read again).
var colNextui struct {
	mu     sync.Mutex
	loaded bool
	meta   colNextuiMeta
	seen   map[string]bool
}

// colNextuiAutoCache keeps colNextuiAuto's answer (its own lock: a pass asks
// while it holds colNextui.mu).
var colNextuiAutoCache struct {
	mu sync.Mutex
	at time.Time
	v  bool
}

var (
	// colNextuiMode mirrors the switch for the goroutines: 0 automatic, 1 on,
	// 2 off.
	colNextuiMode    atomic.Int32
	colNextuiPending atomic.Bool
	colNextuiRunning atomic.Bool
)

func init() { deferStartupTask("nextui-collections", colNextuiBoot) }

// colNextuiBoot reads the switch (main thread) and runs the first pass off it.
func colNextuiBoot() {
	colNextuiMode.Store(colNextuiModeOf(appConfig))
	colNextuiKick()
}

// ── The switch ───────────────────────────────────────────────────────────

// colNextuiModeOf reads the stored switch: 0 never set, 1 on, 2 off.
func colNextuiModeOf(c *Config) int32 {
	if c == nil {
		return 0
	}
	v, ok := customValue(c.Variables.Custom, colNextuiKey)
	if !ok || v == nil {
		return 0
	}
	switch strings.ToLower(strings.TrimSpace(fmt.Sprint(v))) {
	case "on", "true", "1":
		return 1
	case "off", "false", "0":
		return 2
	}
	return 0
}

// colNextuiDir is the card's Collections folder.
func colNextuiDir() string { return filepath.Join(nextuiCardRoot(), colNextuiDirName) }

// colNextuiUsable reports whether there is a card to share with at all.
func colNextuiUsable() bool { return dirExists(nextuiCardRoot()) }

// colNextuiAuto is the switch's default: on where NextUI's files are. The
// answer is kept a few seconds (Settings asks every frame).
func colNextuiAuto() bool {
	c := &colNextuiAutoCache
	c.mu.Lock()
	if !c.at.IsZero() && time.Since(c.at) < 5*time.Second {
		v := c.v
		c.mu.Unlock()
		return v
	}
	c.mu.Unlock()
	v := colNextuiUsable() && (dirExists(colNextuiDir()) || nextuiActive())
	c.mu.Lock()
	c.v, c.at = v, time.Now()
	c.mu.Unlock()
	return v
}

// colNextuiOn reports whether collections are shared with NextUI's folder.
func colNextuiOn() bool {
	if !colNextuiUsable() {
		return false
	}
	switch colNextuiMode.Load() {
	case 1:
		return true
	case 2:
		return false
	}
	return colNextuiAuto()
}

// colNextuiSettingsRow is Settings → Games & emulators → Share collections
// with NextUI.
func colNextuiSettingsRow() setRow {
	return setRow{
		ID: colNextuiKey, Label: "Share collections with NextUI", Kind: setKindToggle,
		Detail: func(*Config) string {
			return "Lists in the card's Collections folder show up here, and your collections are written there for NextUI"
		},
		Visible: func(*Config) bool { return colNextuiUsable() },
		Get: func(c *Config) bool {
			mode := colNextuiModeOf(c)
			colNextuiMode.Store(mode)
			return mode == 1 || (mode == 0 && colNextuiAuto())
		},
		Set: func(c *Config, on bool) {
			if on {
				setStoreString(c, colNextuiKey, "on")
				colNextuiMode.Store(1)
				colNextuiKick()
				setToast("Collections are shared with NextUI's Collections folder")
				return
			}
			setStoreString(c, colNextuiKey, "off")
			colNextuiMode.Store(2)
			setToast("NextUI's Collections folder is left alone")
		},
	}
}

// ── Reading and writing a .txt ───────────────────────────────────────────

// colNextuiParse reads a collection file: one game per line, a path from the
// card root, optionally followed by a TAB and a name. Blank lines are
// dropped; crlf and trailing describe the file so a rewrite keeps its form.
func colNextuiParse(data []byte) (lines []string, crlf, trailing bool) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	text := string(data)
	crlf = strings.Contains(text, "\r\n")
	trailing = strings.HasSuffix(text, "\n")
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines, crlf, trailing
}

// colNextuiLinePath is a line's path, without the name after a TAB.
func colNextuiLinePath(line string) string {
	p, _, _ := strings.Cut(line, "\t")
	return strings.TrimSpace(p)
}

// colNextuiLineFile is where a line's game is on this card; ok=false for a
// line that would leave the card.
func colNextuiLineFile(root, line string) (string, bool) {
	rel := strings.TrimLeft(strings.ReplaceAll(colNextuiLinePath(line), "\\", "/"), "/")
	if rel == "" || strings.TrimSpace(root) == "" {
		return "", false
	}
	for _, part := range strings.Split(rel, "/") {
		if part == ".." {
			return "", false
		}
	}
	abs := filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))
	if !isPathWithin(abs, root) {
		return "", false
	}
	return abs, true
}

// colNextuiCardRel is a file on the card the way NextUI writes it,
// "/Roms/Game Boy (GB)/Tetris.gb"; ok=false off the card.
func colNextuiCardRel(root, abs string) (string, bool) {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(abs) == "" || !isPathWithin(abs, root) {
		return "", false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return "/" + filepath.ToSlash(rel), true
}

// colNextuiRefLine is a collection item as a NextUI line; ok=false for items
// NextUI cannot hold (apps, consoles, games off the card).
func colNextuiRefLine(root, ref string, slash bool) (string, bool) {
	if !strings.HasPrefix(ref, homeTabRefGame) {
		return "", false
	}
	rel, ok := colNextuiCardRel(root, strings.TrimPrefix(ref, homeTabRefGame))
	if !ok {
		return "", false
	}
	if !slash {
		rel = strings.TrimPrefix(rel, "/")
	}
	return rel, true
}

// colNextuiItemsFrom turns a file's lines into collection items: the games on
// the card in the file's order, then old's items NextUI cannot hold (they stay
// JukaHub's) and, with keepGames, old's games the file does not list (a
// collection made here under the same name). Lines whose game is not on the
// card, repeats and lines past the size limit come back as kept, with their
// place. extra reports whether old added games the file does not have.
func colNextuiItemsFrom(root string, lines []string, old []homeTabsColItem, slash, keepGames bool) (items []homeTabsColItem, from map[string]string, kept []colNextuiKept, extra bool) {
	from = map[string]string{}
	seen := map[string]bool{}
	for i, line := range lines {
		abs, ok := colNextuiLineFile(root, line)
		ref := homeTabRefGame + abs
		if !ok || seen[strings.ToLower(ref)] || len(items) >= homeTabsColItemsMax || !regularFile(abs) {
			kept = append(kept, colNextuiKept{At: i, Line: line})
			continue
		}
		seen[strings.ToLower(ref)] = true
		items = append(items, homeTabsColItem{Ref: ref, Title: gameDisplayTitle(abs)})
		from[ref] = line
	}
	for _, it := range old {
		if seen[strings.ToLower(it.Ref)] {
			continue
		}
		if _, ok := colNextuiRefLine(root, it.Ref, slash); ok {
			if !keepGames {
				continue
			}
			extra = true
		}
		seen[strings.ToLower(it.Ref)] = true
		items = append(items, it)
	}
	return items, from, kept, extra
}

// colNextuiRender is the file for items: the line each game was read from (or
// a new one in the file's path style) with the kept lines put back at their
// places, in the file's line endings. lines is what each game was written as.
func colNextuiRender(root string, link *colNextuiLink, items []homeTabsColItem) (data []byte, lines map[string]string) {
	lines = map[string]string{}
	var out []string
	for _, it := range items {
		line, ok := link.Lines[it.Ref]
		if !ok {
			line, ok = colNextuiRefLine(root, it.Ref, !link.NoSlash)
		}
		if !ok {
			continue
		}
		lines[it.Ref] = line
		out = append(out, line)
	}
	kept := append([]colNextuiKept(nil), link.Kept...)
	sort.SliceStable(kept, func(a, b int) bool { return kept[a].At < kept[b].At })
	for _, k := range kept {
		at := k.At
		if at < 0 {
			at = 0
		}
		if at > len(out) {
			at = len(out)
		}
		out = append(out, "")
		copy(out[at+1:], out[at:])
		out[at] = k.Line
	}
	if len(out) == 0 {
		return []byte{}, lines
	}
	eol := "\n"
	if link.CRLF {
		eol = "\r\n"
	}
	text := strings.Join(out, eol)
	if !link.NoTrailing {
		text += eol
	}
	return []byte(text), lines
}

// colNextuiStat is a file's size and modification time.
func colNextuiStat(p string) (size, mtime int64, ok bool) {
	info, err := os.Stat(p)
	if err != nil || !info.Mode().IsRegular() {
		return 0, 0, false
	}
	return info.Size(), info.ModTime().UnixNano(), true
}

// colNextuiRead reads a .txt and its SHA-256.
func colNextuiRead(p string) ([]byte, string, error) {
	data, err := nextuiReadSmall(p, colNextuiMaxFile)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

// colNextuiWriteFile replaces p through a hidden temp file beside it (NextUI
// does not list dot files, so a power cut leaves no stray collection) and a
// rename; no .bak is left in the folder NextUI lists.
func colNextuiWriteFile(p string, data []byte) error {
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+filepath.Base(p)+".jukahub-tmp")
	if err := writeFileSynced(tmp, data, 0o644); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	syncDirectory(dir)
	return nil
}

// colNextuiFileName is a collection name as a file name FAT32 and NextUI can
// hold.
func colNextuiFileName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f:
			continue
		case strings.ContainsRune(`\/:*?"<>|`, r):
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimLeft(strings.TrimRight(strings.TrimSpace(b.String()), ". "), ". ")
	if out == "" {
		out = "Collection"
	}
	return out + ".txt"
}

// colNextuiFreeName is name, or "name 2.txt"… when another file or link has it.
func colNextuiFreeName(dir, name, self string) string {
	taken := func(n string) bool {
		for id, l := range colNextui.meta.Links {
			if id != self && strings.EqualFold(l.File, n) {
				return true
			}
		}
		return fileExists(filepath.Join(dir, n))
	}
	if !taken(name) {
		return name
	}
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	for n := 2; n < 100; n++ {
		candidate := stem + " " + strconv.Itoa(n) + ".txt"
		if !taken(candidate) {
			return candidate
		}
	}
	return ""
}

// colNextuiTrash moves a collection file to Collections/.deleted.
func colNextuiTrash(dir, name string) {
	trash := filepath.Join(dir, colNextuiTrashDir)
	if err := os.MkdirAll(trash, 0o755); err != nil {
		return
	}
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	dst := filepath.Join(trash, name)
	for n := 2; fileExists(dst) && n < 1000; n++ {
		dst = filepath.Join(trash, stem+" "+strconv.Itoa(n)+filepath.Ext(name))
	}
	if err := os.Rename(filepath.Join(dir, name), dst); err != nil {
		logWarn("collections", "could not move a deleted collection's NextUI file aside: %v", err)
	}
}

// colNextuiSig is a collection's name and items in one string.
func colNextuiSig(col homeTabsCollection) string {
	h := sha256.New()
	h.Write([]byte(col.Name))
	for _, it := range col.Items {
		h.Write([]byte{'\n'})
		h.Write([]byte(it.Ref))
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// ── The store ────────────────────────────────────────────────────────────

func colNextuiMetaPath() string {
	// The suite's collections must not land in the developer's data folder.
	if testing.Testing() {
		return ""
	}
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, colNextuiMetaName)
}

// colNextuiLoadLocked reads the meta once.
func colNextuiLoadLocked() {
	if !colNextui.loaded {
		colNextui.loaded = true
		if p := colNextuiMetaPath(); p != "" {
			if _, err := readJSONWithBackup(p, &colNextui.meta); err != nil && !os.IsNotExist(err) {
				logWarn("collections", "ignoring damaged %s: %v", colNextuiMetaName, err)
				colNextui.meta = colNextuiMeta{}
			}
		}
	}
	m := &colNextui.meta
	if m.Links == nil {
		m.Links = map[string]*colNextuiLink{}
	}
	for id, l := range m.Links {
		// A link names one visible .txt right in the folder, nothing else.
		if l == nil || strings.TrimSpace(l.File) == "" || strings.ContainsAny(l.File, "/\\\x00") ||
			skipScanEntryName(l.File) || !strings.EqualFold(filepath.Ext(l.File), ".txt") {
			delete(m.Links, id)
		}
	}
	if m.Sigs == nil {
		m.Sigs = map[string]string{}
	}
	if m.Changed == nil {
		m.Changed = map[string]int64{}
	}
	if m.Detached == nil {
		m.Detached = map[string]string{}
	}
	if colNextui.seen == nil {
		colNextui.seen = map[string]bool{}
	}
}

func colNextuiSaveLocked() {
	p := colNextuiMetaPath()
	if p == "" {
		return
	}
	colNextui.meta.Version = 1
	data, err := json.MarshalIndent(colNextui.meta, "", "  ")
	if err != nil {
		return
	}
	if err := AtomicWrite(p, append(data, '\n'), 0o644); err != nil {
		logWarn("collections", "could not save %s: %v", colNextuiMetaName, err)
	}
}

// colNextuiPut sets a collection's items, making it when id is "" or gone,
// and returns its id and what it now holds; "" when there is no room for
// another collection.
func colNextuiPut(id, name string, items []homeTabsColItem) (string, homeTabsCollection) {
	homeTabsColStore.mu.Lock()
	defer homeTabsColStore.mu.Unlock()
	homeTabsColEnsureLocked()
	items = homeTabsColCleanItems(items, true, homeTabsColItemsMax)
	i := -1
	if id != "" {
		i = homeTabsColIndexLocked(id)
	}
	changed := true
	if i < 0 {
		name = homeTabsColCleanName(name)
		if name == "" {
			name = "Collection"
		}
		if len(homeTabsColStore.file.Collections) >= homeTabsColMax {
			return "", homeTabsCollection{}
		}
		id = homeTabsColNewID()
		homeTabsColStore.file.Collections = append(homeTabsColStore.file.Collections, homeTabsCollection{
			ID: id, Name: name, Items: items, Created: time.Now().UTC().Truncate(time.Second),
		})
		i = len(homeTabsColStore.file.Collections) - 1
	} else {
		col := &homeTabsColStore.file.Collections[i]
		changed = len(col.Items) != len(items)
		for j := 0; !changed && j < len(items); j++ {
			changed = col.Items[j].Ref != items[j].Ref
		}
		if changed {
			col.Items = items
		}
	}
	if changed {
		homeTabsColChangedLocked()
	}
	out := homeTabsColStore.file.Collections[i]
	out.Items = append([]homeTabsColItem(nil), out.Items...)
	return id, out
}

// colNextuiChangedAt is when each collection last changed on this device
// (Unix seconds), after a pass that stamps any change not seen yet.
func colNextuiChangedAt() map[string]int64 {
	colNextuiSync()
	colNextui.mu.Lock()
	defer colNextui.mu.Unlock()
	out := make(map[string]int64, len(colNextui.meta.Changed))
	for k, v := range colNextui.meta.Changed {
		out[k] = v
	}
	return out
}

// colNextuiKick runs a pass off the caller: at start, after every change to
// the collections (homeTabsColChangedLocked) and when the switch goes on.
// Passes never overlap, and a kick during one runs another after it.
func colNextuiKick() {
	// The suite changes collections all the time: no pass reads the card or
	// races a test that swaps the store.
	if testing.Testing() {
		return
	}
	colNextuiPending.Store(true)
	if !colNextuiRunning.CompareAndSwap(false, true) {
		return
	}
	go func() {
		for {
			for colNextuiPending.Swap(false) {
				colNextuiSync()
			}
			colNextuiRunning.Store(false)
			if !colNextuiPending.Load() || !colNextuiRunning.CompareAndSwap(false, true) {
				return
			}
		}
	}()
}

// ── The pass ─────────────────────────────────────────────────────────────

// colNextuiSync is one pass: the Collections folder read (new files, files
// changed outside, files gone), then the changes made here stamped and, when
// sharing, written out.
func colNextuiSync() {
	colNextui.mu.Lock()
	defer colNextui.mu.Unlock()
	colNextuiLoadLocked()
	on := colNextuiOn()
	root, dir := nextuiCardRoot(), colNextuiDir()
	dirty := false
	if on && colNextuiReadFolderLocked(root, dir) {
		dirty = true
	}
	m := &colNextui.meta
	now := time.Now().Unix()
	live := map[string]bool{}
	for _, col := range homeTabsColList() {
		if homeTabsIsAuto(col.ID) {
			continue
		}
		live[col.ID] = true
		colNextui.seen[col.ID] = true
		sig := colNextuiSig(col)
		if was, ok := m.Detached[col.ID]; ok && m.Links[col.ID] == nil {
			if was == sig {
				// Its .txt was deleted outside JukaHub: that is respected
				// (not written again) until the collection changes here.
				if m.Sigs[col.ID] != sig {
					m.Sigs[col.ID] = sig
					m.Changed[col.ID] = now
					dirty = true
				}
				continue
			}
			delete(m.Detached, col.ID)
			dirty = true
		}
		if m.Sigs[col.ID] == sig && (!on || m.Links[col.ID] != nil) {
			continue
		}
		if m.Sigs[col.ID] != sig {
			m.Sigs[col.ID] = sig
			m.Changed[col.ID] = now
		}
		dirty = true
		if on {
			colNextuiWriteOutLocked(col, root, dir)
		}
	}
	for id := range m.Sigs {
		if !live[id] {
			delete(m.Sigs, id)
			delete(m.Changed, id)
			dirty = true
		}
	}
	for id := range m.Detached {
		if !live[id] {
			delete(m.Detached, id)
			dirty = true
		}
	}
	if on {
		for id, l := range m.Links {
			if live[id] {
				continue
			}
			// Deleted here: the file goes to .deleted, where NextUI does not
			// look and the user still can.
			if fileExists(filepath.Join(dir, l.File)) {
				colNextuiTrash(dir, l.File)
				logInfo("collections", "collection deleted: moved its NextUI file to %s", colNextuiTrashDir)
			}
			delete(m.Links, id)
			dirty = true
		}
	}
	if dirty {
		colNextuiSaveLocked()
	}
}

// colNextuiReadFolderLocked reads the Collections folder into the store: a
// file not seen before becomes a collection (or joins one made here under the
// same name), a file changed outside is read again, a file gone unlinks its
// collection. It reports whether the meta changed.
func colNextuiReadFolderLocked(root, dir string) bool {
	m := &colNextui.meta
	dirty := false
	entries, _ := os.ReadDir(dir)
	files := map[string]string{}
	var order []string
	for _, de := range entries {
		name := de.Name()
		if de.IsDir() || skipScanEntryName(name) || !strings.EqualFold(filepath.Ext(name), ".txt") {
			continue
		}
		if len(order) >= homeTabsColMax {
			break
		}
		files[strings.ToLower(name)] = name
		order = append(order, strings.ToLower(name))
	}
	sort.Strings(order)
	cols := map[string]homeTabsCollection{}
	var list []homeTabsCollection
	for _, col := range homeTabsColList() {
		cols[col.ID] = col
		list = append(list, col)
	}
	byFile := map[string]string{}
	for id, l := range m.Links {
		if _, ok := files[strings.ToLower(l.File)]; !ok {
			if col, live := cols[id]; live {
				// Deleted outside JukaHub: the collection stays, unlinked, and
				// is not written back until it changes here (colNextuiSync).
				logInfo("collections", "a NextUI collection file was removed; the JukaHub collection stays")
				delete(m.Links, id)
				m.Detached[id] = colNextuiSig(col)
				dirty = true
				continue
			}
		}
		byFile[strings.ToLower(l.File)] = id
	}
	for _, lower := range order {
		name := files[lower]
		p := filepath.Join(dir, name)
		id, linked := byFile[lower]
		col, live := cols[id]
		if linked && !live {
			if colNextui.seen[id] {
				// Deleted here this session: colNextuiSync moves the file.
				continue
			}
			// The collection is gone from the store, not deleted by hand:
			// the file is read again as a new one.
			delete(m.Links, id)
			linked = false
			dirty = true
		}
		size, mtime, ok := colNextuiStat(p)
		if !ok {
			continue
		}
		if linked {
			l := m.Links[id]
			if l.Size == size && l.MTime == mtime {
				continue
			}
			data, sum, err := colNextuiRead(p)
			if err != nil {
				continue
			}
			dirty = true
			if sum == l.SHA {
				l.Size, l.MTime = size, mtime
				continue
			}
			// Changed outside JukaHub (NextUI, a PC): the file wins.
			colNextuiImportLocked(id, col.Name, col.Items, l, data, root, false)
			l.Size, l.MTime, l.SHA = size, mtime, sum
			logInfo("collections", "read a NextUI collection again after it changed outside JukaHub")
			continue
		}
		data, sum, err := colNextuiRead(p)
		if err != nil {
			continue
		}
		stem := homeTabsColCleanName(strings.TrimSuffix(name, filepath.Ext(name)))
		target := ""
		var old []homeTabsColItem
		for _, c := range list {
			if m.Links[c.ID] == nil && strings.EqualFold(c.Name, stem) {
				target, old = c.ID, c.Items
				break
			}
		}
		l := &colNextuiLink{File: name}
		newID, extra := colNextuiImportLocked(target, stem, old, l, data, root, true)
		if newID == "" {
			continue
		}
		l.Size, l.MTime, l.SHA = size, mtime, sum
		m.Links[newID] = l
		delete(m.Detached, newID)
		dirty = true
		if extra {
			// Games the collection made here had: written into the file too.
			if now, ok := homeTabsColGet(newID); ok {
				colNextuiWriteOutLocked(now, root, dir)
			}
		}
	}
	return dirty
}

// colNextuiImportLocked puts a file's games into collection id (made when ""),
// fills l with the file's form, and records the result as seen, so the pass
// that follows the store's change finds nothing new to write.
func colNextuiImportLocked(id, name string, old []homeTabsColItem, l *colNextuiLink, data []byte, root string, keepGames bool) (string, bool) {
	lines, crlf, trailing := colNextuiParse(data)
	slash := true
	if len(lines) > 0 {
		slash = strings.HasPrefix(colNextuiLinePath(lines[0]), "/")
	}
	l.CRLF, l.NoSlash, l.NoTrailing = crlf, !slash, len(lines) > 0 && !trailing
	items, from, kept, extra := colNextuiItemsFrom(root, lines, old, slash, keepGames)
	l.Lines, l.Kept = from, kept
	newID, col := colNextuiPut(id, name, items)
	if newID == "" {
		return "", false
	}
	colNextui.meta.Sigs[newID] = colNextuiSig(col)
	colNextui.meta.Changed[newID] = time.Now().Unix()
	colNextui.seen[newID] = true
	return newID, extra
}

// colNextuiWriteOutLocked writes a collection to its .txt, giving one made
// here a file of its own and following a rename. A file changed outside since
// JukaHub last read it is never overwritten: the next pass reads it again.
func colNextuiWriteOutLocked(col homeTabsCollection, root, dir string) {
	if homeTabsIsAuto(col.ID) {
		return
	}
	m := &colNextui.meta
	l := m.Links[col.ID]
	if l == nil {
		name := colNextuiFreeName(dir, colNextuiFileName(col.Name), col.ID)
		if name == "" {
			return
		}
		l = &colNextuiLink{File: name}
	} else {
		p := filepath.Join(dir, l.File)
		if size, mtime, ok := colNextuiStat(p); ok && (size != l.Size || mtime != l.MTime) {
			if _, sum, err := colNextuiRead(p); err != nil || sum != l.SHA {
				colNextuiPending.Store(true)
				return
			}
		}
		// Renamed here: the file follows when the new name is free.
		if want := colNextuiFileName(col.Name); !strings.EqualFold(want, l.File) {
			if free := colNextuiFreeName(dir, want, col.ID); free != "" && !fileExists(filepath.Join(dir, free)) {
				if err := os.Rename(p, filepath.Join(dir, free)); err == nil {
					l.File = free
				}
			}
		}
	}
	p := filepath.Join(dir, l.File)
	data, lines := colNextuiRender(root, l, col.Items)
	if err := colNextuiWriteFile(p, data); err != nil {
		logWarn("collections", "could not write a NextUI collection: %v", err)
		return
	}
	sum := sha256.Sum256(data)
	l.Lines, l.SHA = lines, hex.EncodeToString(sum[:])
	l.Size, l.MTime, _ = colNextuiStat(p)
	m.Links[col.ID] = l
}
