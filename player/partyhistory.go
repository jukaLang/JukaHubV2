package main

// partyhistory.go — durable Watch Party statistics.
//
// A room's own leaderboard lasts only as long as the host listener. This file
// keeps what survives it: every member's cumulative watch time across rooms,
// and a short per-host history of the rooms this device joined. The archive is
// local (partyhistory.json, beside the friends list); no account or server is
// involved.
//
// Room snapshots carry cumulative scores, not deltas. Re-reading the same
// snapshot must therefore not count its time twice, so the archive remembers
// the last score it saw for each (room, member) pair and adds only the growth.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	partyHistoryFileName     = "partyhistory.json"
	partyHistoryVersion      = 1
	partyHistoryPeopleLimit  = 64
	partyHistoryFriendLimit  = 32
	partyHistoryRoomLimit    = 60
	partyHistoryFriendRooms  = 4
	partyHistorySaveInterval = 10 * time.Second
)

// partyAllTimeEntry is one person's cumulative watch time on this handheld,
// regardless of which room or host it happened in.
type partyAllTimeEntry struct {
	Name      string  `json:"name"`
	WatchSecs float64 `json:"watch_seconds"`
	Rooms     int     `json:"rooms"`
	LastSeen  string  `json:"last_seen,omitempty"`
}

// partyFriendHistory groups the rooms served by one host address.
type partyFriendHistory struct {
	Host      string  `json:"host"`
	Name      string  `json:"name,omitempty"`
	WatchSecs float64 `json:"watch_seconds"`
	LastSeen  string  `json:"last_seen,omitempty"`
}

// partyRoomHistory is one room this device has spent time in.
type partyRoomHistory struct {
	Key       string  `json:"key"`
	Host      string  `json:"host"`
	HostName  string  `json:"host_name,omitempty"`
	Code      string  `json:"code"`
	Name      string  `json:"name,omitempty"`
	Title     string  `json:"last_watched,omitempty"`
	WatchSecs float64 `json:"watch_seconds"`
	Members   int     `json:"members,omitempty"`
	LastSeen  string  `json:"last_seen,omitempty"`
}

type partyHistoryData struct {
	Version   int                           `json:"version"`
	AllTime   []partyAllTimeEntry           `json:"all_time"`
	Friends   []partyFriendHistory          `json:"friends"`
	Rooms     []partyRoomHistory            `json:"rooms"`
	Baselines map[string]map[string]float64 `json:"baselines,omitempty"`
}

// partyHistoryView is an immutable view for rendering and tests.
type partyHistoryView struct {
	People  []partyAllTimeEntry
	Friends []partyFriendHistory
	Rooms   []partyRoomHistory
}

var partyHistoryStore struct {
	mu       sync.Mutex
	loaded   bool
	data     partyHistoryData
	lastSave time.Time
	dirty    bool
}

// partyHistoryPath is a variable so tests can use a temp file instead of the
// app data folder.
var partyHistoryPath = func() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		dir = "."
	}
	return filepath.Join(dir, partyHistoryFileName)
}

func newPartyHistoryData() partyHistoryData {
	return partyHistoryData{
		Version:   partyHistoryVersion,
		Baselines: make(map[string]map[string]float64),
	}
}

func partyHistoryLoadLocked() {
	if partyHistoryStore.loaded {
		return
	}
	partyHistoryStore.loaded = true
	partyHistoryStore.data = newPartyHistoryData()

	raw, err := os.ReadFile(partyHistoryPath())
	if err != nil {
		return
	}
	var stored partyHistoryData
	if json.Unmarshal(raw, &stored) != nil {
		return
	}
	if stored.Version == 0 {
		stored.Version = partyHistoryVersion
	}
	if stored.Baselines == nil {
		stored.Baselines = make(map[string]map[string]float64)
	}
	partyHistoryStore.data = stored
}

func partyHistorySaveLocked() error {
	path := partyHistoryPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(partyHistoryStore.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	partyHistoryStore.lastSave = time.Now()
	partyHistoryStore.dirty = false
	return nil
}

// partyHistoryMaybeSaveLocked writes at most once every ten seconds. Watch
// time grows every second, and rewriting flash that often would be wasteful on
// a handheld.
func partyHistoryMaybeSaveLocked() {
	if !partyHistoryStore.dirty {
		return
	}
	if time.Since(partyHistoryStore.lastSave) < partyHistorySaveInterval {
		return
	}
	_ = partyHistorySaveLocked()
}

// partyHistoryFlush writes pending data, for leaving a room or quitting.
func partyHistoryFlush() {
	partyHistoryStore.mu.Lock()
	defer partyHistoryStore.mu.Unlock()
	partyHistoryLoadLocked()
	if !partyHistoryStore.dirty {
		return
	}
	_ = partyHistorySaveLocked()
}

// partyHistoryFriendName is the host's display name from a snapshot.
func partyHistoryFriendName(snap partySnapshot) string {
	name := strings.TrimSpace(partyHostName(snap))
	if name == "" {
		name = strings.TrimSpace(snap.Name)
	}
	if name == "" {
		name = "Party host"
	}
	return partyMemberName(name)
}

// partyHistoryRoomKey identifies a hosted room independently of its display
// name. The code is random per room, so two movie nights are two entries.
func partyHistoryRoomKey(base, code string) string {
	return base + "#" + strings.ToUpper(strings.TrimSpace(code))
}

// partyHistoryAddress resolves the host base URL from the snapshot, falling
// back to the invite the user pasted.
func partyHistoryAddress(snap partySnapshot, invite string) (base, code string) {
	code = strings.ToUpper(strings.TrimSpace(snap.Code))
	if code == "" && strings.TrimSpace(invite) != "" {
		base, code, _ = partyParseInvite(invite)
	}
	if strings.TrimSpace(snap.HostAddr) != "" {
		if b, err := partyNormalizeBase(snap.HostAddr); err == nil {
			base = b
		}
	}
	if base == "" && strings.TrimSpace(invite) != "" {
		if b, c, err := partyParseInvite(invite); err == nil {
			base = b
			if code == "" {
				code = c
			}
		}
	}
	return base, code
}

func partyHistoryFindPerson(entries []partyAllTimeEntry, key string) int {
	for i := range entries {
		if partyScoreKey(entries[i].Name) == key {
			return i
		}
	}
	return -1
}

func partyHistoryFindFriend(entries []partyFriendHistory, host string) int {
	for i := range entries {
		if entries[i].Host == host {
			return i
		}
	}
	return -1
}

func partyHistoryFindRoom(entries []partyRoomHistory, key string) int {
	for i := range entries {
		if entries[i].Key == key {
			return i
		}
	}
	return -1
}

// partyHistorySortLocked keeps the lists bounded and in display order.
func partyHistorySortLocked() {
	data := &partyHistoryStore.data
	sort.Slice(data.AllTime, func(i, j int) bool {
		if data.AllTime[i].WatchSecs == data.AllTime[j].WatchSecs {
			return data.AllTime[i].Name < data.AllTime[j].Name
		}
		return data.AllTime[i].WatchSecs > data.AllTime[j].WatchSecs
	})
	if len(data.AllTime) > partyHistoryPeopleLimit {
		data.AllTime = data.AllTime[:partyHistoryPeopleLimit]
	}
	sort.Slice(data.Friends, func(i, j int) bool {
		if data.Friends[i].LastSeen == data.Friends[j].LastSeen {
			return data.Friends[i].Name < data.Friends[j].Name
		}
		return data.Friends[i].LastSeen > data.Friends[j].LastSeen
	})
	if len(data.Friends) > partyHistoryFriendLimit {
		data.Friends = data.Friends[:partyHistoryFriendLimit]
	}
	sort.Slice(data.Rooms, func(i, j int) bool {
		if data.Rooms[i].LastSeen == data.Rooms[j].LastSeen {
			return data.Rooms[i].Key < data.Rooms[j].Key
		}
		return data.Rooms[i].LastSeen > data.Rooms[j].LastSeen
	})
	if len(data.Rooms) > partyHistoryRoomLimit {
		data.Rooms = data.Rooms[:partyHistoryRoomLimit]
	}
}

// partyHistoryObserve folds one room snapshot into the durable archive. It is
// safe to call with the same snapshot repeatedly: only score growth is added.
// invite may be empty when the room snapshot already carries HostAddr.
func partyHistoryObserve(snap partySnapshot, invite string) {
	base, code := partyHistoryAddress(snap, invite)
	if base == "" || !partyCodeValid(code) {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	key := partyHistoryRoomKey(base, code)
	hostName := partyHistoryFriendName(snap)
	title := strings.TrimSpace(snap.Playback.Title)
	if title == "" && strings.TrimSpace(snap.Playback.URL) != "" {
		title = partyTitleFromURL(snap.Playback.URL)
	}
	roomName := strings.TrimSpace(snap.Name)
	if roomName == "" {
		roomName = hostName
	}
	roomName = partyMemberName(roomName)

	partyHistoryStore.mu.Lock()
	defer partyHistoryStore.mu.Unlock()
	partyHistoryLoadLocked()
	data := &partyHistoryStore.data
	changed := false

	// The room record exists even before anyone has watched a second, so the
	// friend history can show a room that was only just joined.
	roomIdx := partyHistoryFindRoom(data.Rooms, key)
	if roomIdx < 0 {
		data.Rooms = append(data.Rooms, partyRoomHistory{
			Key:      key,
			Host:     base,
			HostName: hostName,
			Code:     code,
			Name:     roomName,
			Title:    title,
			Members:  len(snap.Members),
			LastSeen: now,
		})
		roomIdx = len(data.Rooms) - 1
		changed = true
	} else {
		room := &data.Rooms[roomIdx]
		if hostName != "" && room.HostName != hostName {
			room.HostName = hostName
			changed = true
		}
		if room.Name != roomName {
			room.Name = roomName
			changed = true
		}
		if title != "" && room.Title != title {
			room.Title = title
			changed = true
		}
		if len(snap.Members) > 0 && room.Members != len(snap.Members) {
			room.Members = len(snap.Members)
			changed = true
		}
		if room.LastSeen != now {
			room.LastSeen = now
			changed = true
		}
	}

	friendIdx := partyHistoryFindFriend(data.Friends, base)
	if friendIdx < 0 {
		data.Friends = append(data.Friends, partyFriendHistory{
			Host:     base,
			Name:     hostName,
			LastSeen: now,
		})
		friendIdx = len(data.Friends) - 1
		changed = true
	} else {
		friend := &data.Friends[friendIdx]
		if hostName != "" && friend.Name != hostName {
			friend.Name = hostName
			changed = true
		}
		if friend.LastSeen != now {
			friend.LastSeen = now
			changed = true
		}
	}

	// Baseline per room and member. A negative delta means the host restarted
	// and reused the same room code; reset instead of subtracting.
	baseline := data.Baselines[key]
	if baseline == nil {
		baseline = make(map[string]float64)
		data.Baselines[key] = baseline
	}
	for _, row := range snap.Leaderboard {
		name := partyMemberName(row.Name)
		nameKey := partyScoreKey(name)
		prev, seen := baseline[nameKey]
		delta := row.WatchSecs - prev
		if delta < 0 {
			delta = 0
		}
		baseline[nameKey] = row.WatchSecs

		personIdx := partyHistoryFindPerson(data.AllTime, nameKey)
		if personIdx < 0 {
			data.AllTime = append(data.AllTime, partyAllTimeEntry{Name: name})
			personIdx = len(data.AllTime) - 1
			changed = true
		}
		person := &data.AllTime[personIdx]
		if person.Name != name {
			person.Name = name
			changed = true
		}
		if !seen {
			person.Rooms++
			changed = true
		}
		if delta > 0 {
			person.WatchSecs += delta
			person.LastSeen = now
			data.Rooms[roomIdx].WatchSecs += delta
			data.Friends[friendIdx].WatchSecs += delta
			data.Friends[friendIdx].LastSeen = now
			changed = true
		}
		if person.LastSeen == "" {
			person.LastSeen = now
			changed = true
		}
	}

	if changed {
		partyHistorySortLocked()
		partyHistoryStore.dirty = true
		partyHistoryMaybeSaveLocked()
	}
}

// partyHistoryReport returns a sorted copy. The UI can render it without
// holding the store lock while another sync updates the archive.
func partyHistoryReport() partyHistoryView {
	partyHistoryStore.mu.Lock()
	defer partyHistoryStore.mu.Unlock()
	partyHistoryLoadLocked()
	partyHistorySortLocked()
	data := partyHistoryStore.data
	return partyHistoryView{
		People:  append([]partyAllTimeEntry(nil), data.AllTime...),
		Friends: append([]partyFriendHistory(nil), data.Friends...),
		Rooms:   append([]partyRoomHistory(nil), data.Rooms...),
	}
}

// partyHistoryWhen turns an RFC3339 stamp into a short relative label.
func partyHistoryWhen(stamp string) string {
	t, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
