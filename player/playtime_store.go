package main

// playtime_store.go — playtime's own files, and the session log (LIB-13).
//
// The snapshot used to ride in jukauser.json under "playtime_v1", so every
// playtime save rewrote the whole user config (and its .bak): on a FAT32 card
// with no journal a handheld sitting on Home wrote it 120 times an hour. The
// snapshot is playtime.json now, beside the per-day store (playtime-days.json),
// written atomically (temp file, fsync, rename, .bak). A save of playtime no
// longer touches the user config; the one write left is the migration that
// removes the old key from an existing config, once.
//
// The session log is playtime-sessions.json: one record per finished game
// session, appended where the session is credited (playtimeCreditGameGap),
// keyed by the cartridge's content hash (libraryContentKey) rather than its
// path, so a renamed or moved ROM keeps its sessions. It is bounded twice —
// playtimeSessionsMax records and the same 400 days the per-day store keeps —
// and keeps the newest when it has to drop one, so a long-running device
// forgets its oldest mornings, never a game's whole history.
//
// playtime.json is the lifetime snapshot (total, screens, days, the per-game
// ledger, trophies); playtime-sessions.json is what the Wrapped year and the
// muOS-style export read for hashes and dates.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	playtimeStoreVersion     = 1
	playtimeFileName         = "playtime.json"
	playtimeSessionsVersion  = 1
	playtimeSessionsFileName = "playtime-sessions.json"
	// playtimeSessionsMax bounds the log's records; playtimeDaysKept bounds
	// what playtimeData.Days, GameDays and the session log keep, and is the
	// window Wrapped draws (53 weeks) and the per-day store uses.
	playtimeSessionsMax = 5000
	playtimeDaysKept    = 400
)

// playtimeFileDoc is playtime.json's shape.
type playtimeFileDoc struct {
	Version int          `json:"version"`
	Data    playtimeData `json:"data"`
}

// playtimeSession is one finished game session.
type playtimeSession struct {
	// Hash is the cartridge's content hash (or "path:<path>" when the file
	// could not be read), so a moved ROM keeps its history.
	Hash    string    `json:"hash"`
	System  string    `json:"system,omitempty"`
	Title   string    `json:"title"`
	Start   time.Time `json:"start"`
	Seconds int64     `json:"seconds"`
	// Day is the local day the session counts for ("2006-01-02").
	Day string `json:"day"`
}

// playtimeSessionsDoc is playtime-sessions.json's shape.
type playtimeSessionsDoc struct {
	Version  int               `json:"version"`
	Sessions []playtimeSession `json:"sessions"`
}

// playtimeStorePath and playtimeSessionsPath are variables so a test can point
// them at a temporary folder (the suite already does that for the other
// stores).
var playtimeStorePath = func() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, playtimeFileName)
}

var playtimeSessionsPath = func() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, playtimeSessionsFileName)
}

// playtimeCleanData makes every map non-nil, so a save written by an older
// build (or a hand-edited one) reads back without a nil check at every use.
func playtimeCleanData(d playtimeData) playtimeData {
	if d.Scenes == nil {
		d.Scenes = map[string]int64{}
	}
	if d.Days == nil {
		d.Days = map[string]int64{}
	}
	if d.Unlocked == nil {
		d.Unlocked = map[string]string{}
	}
	if d.Games == nil {
		d.Games = map[string]int64{}
	}
	if d.GameDays == nil {
		d.GameDays = map[string]int64{}
	}
	if d.Counts == nil {
		d.Counts = map[string]int64{}
	}
	if d.Sets == nil {
		d.Sets = map[string][]uint32{}
	}
	return d
}

// playtimeStoreLoad reads playtime.json. fromFile is false when there is no
// usable file (a first run, or a damaged one), which is when the config's old
// key still has something to offer.
func playtimeStoreLoad() (data playtimeData, fromFile bool) {
	path := playtimeStorePath()
	if path == "" {
		return newPlaytimeData(), false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return newPlaytimeData(), false
	}
	var doc playtimeFileDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		logWarn("playtime", "cannot read %s: %v", filepath.Base(path), err)
		return newPlaytimeData(), false
	}
	return playtimeCleanData(doc.Data), true
}

// playtimeStoreSave writes the snapshot atomically. It is the only writer of
// playtime.json; nothing here touches the user config.
func playtimeStoreSave(data playtimeData) error {
	path := playtimeStorePath()
	if path == "" {
		return nil
	}
	b, err := json.Marshal(playtimeFileDoc{Version: playtimeStoreVersion, Data: playtimeCleanData(data)})
	if err != nil {
		return err
	}
	return AtomicWrite(path, b, 0o644)
}

// playtimeSessionsLoad reads the session log, dropping records past the log's
// bounds (a file written by an older build, or one whose clock ran ahead).
func playtimeSessionsLoad() []playtimeSession {
	path := playtimeSessionsPath()
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc playtimeSessionsDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		logWarn("playtime", "cannot read %s: %v", filepath.Base(path), err)
		return nil
	}
	return playtimeSessionsPrune(doc.Sessions, time.Now())
}

func playtimeSessionsSave(list []playtimeSession) error {
	path := playtimeSessionsPath()
	if path == "" {
		return nil
	}
	b, err := json.Marshal(playtimeSessionsDoc{Version: playtimeSessionsVersion, Sessions: list})
	if err != nil {
		return err
	}
	return AtomicWrite(path, b, 0o644)
}

// playtimeSessionsPrune keeps the newest playtimeSessionsMax records that are
// inside the 400-day window. A record with no day is dropped: it could not be
// placed on the year, and keeping it would only grow the file.
func playtimeSessionsPrune(list []playtimeSession, now time.Time) []playtimeSession {
	if len(list) == 0 {
		return nil
	}
	cutoff := now.AddDate(0, 0, -playtimeDaysKept).Format(wrappedDayKey)
	out := make([]playtimeSession, 0, len(list))
	for _, s := range list {
		if s.Day == "" || s.Day < cutoff || s.Seconds <= 0 {
			continue
		}
		out = append(out, s)
	}
	if len(out) > playtimeSessionsMax {
		// Newest first, so the oldest go: a sort keeps a hand-edited file from
		// deciding which records survive by their order in the file.
		sort.SliceStable(out, func(i, j int) bool {
			if !out[i].Start.Equal(out[j].Start) {
				return out[i].Start.After(out[j].Start)
			}
			return out[i].Day > out[j].Day
		})
		out = out[:playtimeSessionsMax]
		sort.SliceStable(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	}
	return out
}

// playtimeSessionNote appends one finished session to the log, keyed by the
// game's content hash. path is the ROM's path as the recents recorded it; the
// library's decorated game already carries the hash, and a file that is gone
// falls back to libraryContentKey's "path:" key rather than losing the record.
func playtimeSessionNote(path, system, title string, start time.Time, seconds int64, day string) {
	if seconds <= 0 || strings.TrimSpace(title) == "" {
		return
	}
	rec := playtimeSession{
		Hash:    playtimeGameHash(path),
		System:  playtimeKeySystem(system),
		Title:   title,
		Start:   start,
		Seconds: seconds,
		Day:     day,
	}
	playtime.sessions = append(playtime.sessions, rec)
	playtime.sessions = playtimeSessionsPrune(playtime.sessions, time.Now())
}

// playtimeGameHash is a library game's content hash by path, computed only when
// the library does not have one (a game launched from outside the scan). A
// variable so a test can key its fixture sessions without a card to hash.
var playtimeGameHash = func(path string) string {
	path = strings.TrimSpace(path)
	for _, system := range gameLib.lib.Systems {
		for _, g := range system.Games {
			if g.Path == path && g.ContentKey != "" {
				return g.ContentKey
			}
		}
	}
	if path == "" {
		return ""
	}
	return libraryContentKey(path)
}

// playtimeExportRow is one line of the muOS-style export: a game's total time,
// its last session and its content hash.
type playtimeExportRow struct {
	System     string
	Title      string
	Hash       string
	Seconds    int64
	LastPlayed string
}

// playtimeExportRows joins the lifetime ledger (the figures) with the session
// log (the hash and the last date), sorted by time and then by title so the
// file reads the same every time.
func playtimeExportRows(games map[string]int64, sessions []playtimeSession) []playtimeExportRow {
	last := map[string]playtimeSession{}
	for _, s := range sessions {
		key := playtimeGameKey(s.System, s.Title)
		if cur, ok := last[key]; !ok || s.Start.After(cur.Start) {
			last[key] = s
		}
	}
	rows := make([]playtimeExportRow, 0, len(games))
	for key, secs := range games {
		if secs <= 0 {
			continue
		}
		system, title := playtimeGameKeyParts(key)
		row := playtimeExportRow{System: system, Title: title, Seconds: secs}
		if s, ok := last[key]; ok {
			row.Hash = s.Hash
			row.LastPlayed = s.Start.Format("2006-01-02 15:04")
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Seconds != rows[j].Seconds {
			return rows[i].Seconds > rows[j].Seconds
		}
		if rows[i].Title != rows[j].Title {
			return rows[i].Title < rows[j].Title
		}
		return rows[i].System < rows[j].System
	})
	return rows
}
