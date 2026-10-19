package main

// party.go — the Watch Party room model.
//
// A room is hosted by one handheld and joined over the LAN by the others. The
// host is only special in that it runs the HTTP listener; the room state itself
// is server-authoritative, so any member can add to the shared queue or press
// play/pause and everyone converges on the same stream and position.
//
// Everything here is SDL-free and clock-injectable so the sync rules can be
// tested headless: presence expiry, watch-time leaderboards, queue edits and
// drift correction.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	partyPresenceTTL      = 15 * time.Second
	partyMaxMembers       = 16
	partyMaxQueue         = 50
	partyMaxWatchDelta    = 15.0 // seconds credited by one heartbeat
	partyMaxWatchPoll     = 10.0 // seconds a client may claim per state poll
	partyNameLimit        = 24
	partyDefaultPort      = 8318
	partyQueueTitleLimit  = 80
	partyLeaderboardLimit = 8
)

var (
	errPartyUnknownMember = errors.New("not a member of this party")
	errPartyUnknownRoom   = errors.New("no such party code")
	errPartyFull          = errors.New("the party is full")
	errPartyBadPlayback   = errors.New("invalid playback update")
	errPartyEmptyQueue    = errors.New("the queue is empty")
)

// partyQueueItem is one shared video.
type partyQueueItem struct {
	ID      int    `json:"id"`
	URL     string `json:"url"`
	Title   string `json:"title,omitempty"`
	AddedBy string `json:"added_by,omitempty"`
}

// partyPlayback is what the room is watching. Position is advanced to the
// moment the snapshot is taken, so clients only add their own elapsed time and
// never need their clocks synchronised with the host.
type partyPlayback struct {
	URL      string  `json:"url"`
	Title    string  `json:"title,omitempty"`
	Position float64 `json:"position"`
	Playing  bool    `json:"playing"`
	Updated  int64   `json:"updated_ms"`
	// Command changes only when someone actually presses something. Updated
	// changes on every read (the position is projected), so it cannot be used
	// to distinguish a new instruction from a poll of the same one.
	Command int64 `json:"command"`
}

// partyMember is one connected device, as the room sees it.
type partyMember struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Host      bool    `json:"host"`
	LastSeen  int64   `json:"last_seen_ms"`
	WatchSecs float64 `json:"watch_seconds"`
}

// partyLeaderRow is one line of the watch-time leaderboard. Departed members
// keep their score for the lifetime of the room, which is what makes a
// leaderboard meaningful across a session.
type partyLeaderRow struct {
	Name      string  `json:"name"`
	WatchSecs float64 `json:"watch_seconds"`
	Online    bool    `json:"online"`
}

// partySnapshot is the full public state of a room.
type partySnapshot struct {
	Code        string           `json:"code"`
	Name        string           `json:"name"`
	HostID      string           `json:"host_id"`
	HostAddr    string           `json:"host_addr,omitempty"`
	Queue       []partyQueueItem `json:"queue"`
	Current     int              `json:"current"`
	Playback    partyPlayback    `json:"playback"`
	Members     []partyMember    `json:"members"`
	Leaderboard []partyLeaderRow `json:"leaderboard"`
	Version     int64            `json:"version"`
	ServerTime  int64            `json:"server_time_ms"`
}

// partyLivePosition is the host position projected to a local point in time.
// snap must be the snapshot as received at receivedAt.
func partyLivePosition(snap partySnapshot, receivedAt, now time.Time) float64 {
	pos := snap.Playback.Position
	if !snap.Playback.Playing {
		return pos
	}
	elapsed := now.Sub(receivedAt).Seconds()
	if elapsed < 0 {
		elapsed = 0
	}
	return math.Max(0, pos+elapsed)
}

// partyNeedsSeek reports whether a remote playhead is far enough from the
// local one to justify restarting the decoder. Small differences are left
// alone: seeking every poll would stutter the picture for a few milliseconds
// of drift.
func partyNeedsSeek(local, target, threshold float64) bool {
	if threshold <= 0 {
		threshold = 2.5
	}
	return math.Abs(local-target) > threshold
}

// partyMemberName cleans a display name without rejecting it. Empty names get
// a stable fallback so the presence list never has blank rows.
func partyMemberName(name string) string {
	name = strings.TrimSpace(strings.Join(strings.Fields(name), " "))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	if name == "" {
		return "Guest"
	}
	runes := []rune(name)
	if len(runes) > partyNameLimit {
		name = string(runes[:partyNameLimit])
	}
	return name
}

// partyCodeAlphabet avoids characters that are easy to confuse when read off a
// screen: I/1, O/0 and L.
const partyCodeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

func partyNewCode() string {
	buf := make([]byte, 5)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%05d", time.Now().UnixNano()%100000)
	}
	out := make([]byte, len(buf))
	for i, b := range buf {
		out[i] = partyCodeAlphabet[int(b)%len(partyCodeAlphabet)]
	}
	return string(out)
}

func partyNewMemberID() string {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%012x", time.Now().UnixNano()&0xffffffffffff)
	}
	return hex.EncodeToString(buf)
}

func partyCodeValid(code string) bool {
	if len(code) != 5 {
		return false
	}
	for _, r := range strings.ToUpper(code) {
		if !strings.ContainsRune(partyCodeAlphabet, r) {
			return false
		}
	}
	return true
}

// partyRoom is the host's authoritative state. All access goes through the
// mutex; snapshots are copies so callers never hold it while encoding.
type partyRoom struct {
	mu sync.Mutex

	code     string
	name     string
	hostID   string
	hostAddr string
	queue    []partyQueueItem
	current  int
	playback partyPlayback
	members  map[string]*partyMember
	scores   map[string]float64 // by normalised display name, for the leaderboard
	names    map[string]string  // normalised key -> display name as typed
	online   map[string]bool
	nextID   int
	command  int64
	version  int64
	now      func() time.Time
	ttl      time.Duration
}

func newPartyRoom(name, hostName string) (*partyRoom, string) {
	r := &partyRoom{
		code:    partyNewCode(),
		name:    partyMemberName(name),
		current: -1,
		members: make(map[string]*partyMember),
		scores:  make(map[string]float64),
		names:   make(map[string]string),
		online:  make(map[string]bool),
		nextID:  1,
		now:     time.Now,
		ttl:     partyPresenceTTL,
	}
	hostID := r.addMemberLocked(hostName, true)
	r.hostID = hostID
	r.version++
	return r, hostID
}

func partyScoreKey(name string) string {
	return strings.ToLower(partyMemberName(name))
}

func (r *partyRoom) addMemberLocked(name string, host bool) string {
	name = partyMemberName(name)
	id := partyNewMemberID()
	m := &partyMember{ID: id, Name: name, Host: host, LastSeen: r.now().UnixMilli()}
	r.members[id] = m
	key := partyScoreKey(name)
	r.online[key] = true
	r.names[key] = name
	return id
}

// Join adds a device and returns its member ID.
func (r *partyRoom) Join(name string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked()
	if len(r.members) >= partyMaxMembers {
		return "", errPartyFull
	}
	id := r.addMemberLocked(name, false)
	r.version++
	return id, nil
}

// Authorize checks the room code and that the member is still present.
func (r *partyRoom) Authorize(code, memberID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !strings.EqualFold(strings.TrimSpace(code), r.code) {
		return errPartyUnknownRoom
	}
	if _, ok := r.members[memberID]; !ok {
		return errPartyUnknownMember
	}
	return nil
}

// Code returns the room code.
func (r *partyRoom) Code() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.code
}

// SetHostAddr records the address clients should use, so the invite can be
// copied without guessing which interface the app is listening on.
func (r *partyRoom) SetHostAddr(addr string) {
	r.mu.Lock()
	r.hostAddr = strings.TrimSpace(addr)
	r.mu.Unlock()
}

// Heartbeat refreshes presence and credits watch time. delta is clamped so a
// client cannot claim an hour of watching in one poll.
func (r *partyRoom) Heartbeat(memberID string, watchDelta float64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.members[memberID]
	if !ok {
		return errPartyUnknownMember
	}
	now := r.now()
	m.LastSeen = now.UnixMilli()
	if watchDelta > 0 {
		if watchDelta > partyMaxWatchDelta {
			watchDelta = partyMaxWatchDelta
		}
		m.WatchSecs += watchDelta
		key := partyScoreKey(m.Name)
		r.scores[key] += watchDelta
		r.names[key] = m.Name
	}
	r.pruneLocked()
	return nil
}

// AddQueue appends a link to the shared queue.
func (r *partyRoom) AddQueue(memberID, url, title string) (partyQueueItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.members[memberID]
	if !ok {
		return partyQueueItem{}, errPartyUnknownMember
	}
	url = strings.TrimSpace(url)
	if !looksLikeStreamURL(url) {
		return partyQueueItem{}, errors.New("only http(s) and stream links can be queued")
	}
	if len(r.queue) >= partyMaxQueue {
		return partyQueueItem{}, errors.New("the queue is full")
	}
	title = strings.TrimSpace(title)
	title = strings.Map(func(rn rune) rune {
		if rn < 0x20 || rn == 0x7f {
			return -1
		}
		return rn
	}, title)
	if runes := []rune(title); len(runes) > partyQueueTitleLimit {
		title = string(runes[:partyQueueTitleLimit])
	}
	if title == "" {
		title = partyTitleFromURL(url)
	}
	item := partyQueueItem{ID: r.nextID, URL: url, Title: title, AddedBy: m.Name}
	r.nextID++
	r.queue = append(r.queue, item)
	r.version++
	return item, nil
}

// partyTitleFromURL makes a readable queue label when the user only pasted a
// link. The path is better than the raw URL for most streams.
func partyTitleFromURL(url string) string {
	trimmed := strings.TrimRight(url, "/")
	if i := strings.LastIndexAny(trimmed, "/?#"); i >= 0 && i+1 < len(trimmed) {
		name := trimmed[i+1:]
		name = strings.TrimSuffix(name, ".mp4")
		name = strings.TrimSuffix(name, ".m3u8")
		if name != "" {
			return name
		}
	}
	return url
}

// RemoveQueue deletes one item by ID, keeping the current selection coherent.
func (r *partyRoom) RemoveQueue(memberID string, id int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.members[memberID]; !ok {
		return errPartyUnknownMember
	}
	idx := -1
	for i, item := range r.queue {
		if item.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	r.queue = append(r.queue[:idx], r.queue[idx+1:]...)
	switch {
	case len(r.queue) == 0:
		r.current = -1
		r.playback = partyPlayback{Updated: r.now().UnixMilli()}
	case idx < r.current:
		r.current--
	case idx == r.current:
		if r.current >= len(r.queue) {
			r.current = len(r.queue) - 1
		}
		item := r.queue[r.current]
		r.command++
		r.playback = partyPlayback{URL: item.URL, Title: item.Title, Playing: false, Updated: r.now().UnixMilli(), Command: r.command}
	}
	r.version++
	return nil
}

// SelectQueue makes a queued item the room's current video, starting from the
// top. Selecting the already-current item restarts it from the beginning.
func (r *partyRoom) SelectQueue(memberID string, index int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.members[memberID]; !ok {
		return errPartyUnknownMember
	}
	if len(r.queue) == 0 {
		return errPartyEmptyQueue
	}
	if index < 0 || index >= len(r.queue) {
		index = 0
	}
	r.current = index
	item := r.queue[index]
	r.command++
	r.playback = partyPlayback{
		URL:      item.URL,
		Title:    item.Title,
		Position: 0,
		Playing:  true,
		Updated:  r.now().UnixMilli(),
		Command:  r.command,
	}
	r.version++
	return nil
}

// SetPlayback updates the room playhead. An empty URL keeps the current
// stream; a new URL switches everyone to it.
func (r *partyRoom) SetPlayback(memberID, url, title string, position float64, playing bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.members[memberID]; !ok {
		return errPartyUnknownMember
	}
	url = strings.TrimSpace(url)
	if url == "" {
		url = r.playback.URL
	}
	if url == "" {
		return errPartyBadPlayback
	}
	if !looksLikeStreamURL(url) {
		return errPartyBadPlayback
	}
	if math.IsNaN(position) || math.IsInf(position, 0) || position < 0 {
		position = 0
	}
	if title == "" && url != r.playback.URL {
		title = partyTitleFromURL(url)
	}
	r.command++
	r.playback = partyPlayback{
		URL:      url,
		Title:    title,
		Position: position,
		Playing:  playing,
		Updated:  r.now().UnixMilli(),
		Command:  r.command,
	}
	r.version++
	return nil
}

// Leave removes a member immediately instead of waiting for the TTL. Scores
// stay on the leaderboard; presence does not.
func (r *partyRoom) Leave(memberID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m, ok := r.members[memberID]; ok {
		m.LastSeen = 0
		r.online[partyScoreKey(m.Name)] = false
		delete(r.members, memberID)
		r.version++
	}
}

func (r *partyRoom) pruneLocked() {
	cutoff := r.now().Add(-r.ttl).UnixMilli()
	for id, m := range r.members {
		if m.LastSeen < cutoff {
			r.online[partyScoreKey(m.Name)] = false
			delete(r.members, id)
			r.version++
		}
	}
}

// Snapshot returns a copy of the room with the playback position projected to
// now. Members are sorted by name so the presence list does not shuffle.
func (r *partyRoom) Snapshot() partySnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked()
	now := r.now()
	if r.playback.Playing && r.playback.URL != "" {
		r.playback.Position += now.Sub(time.UnixMilli(r.playback.Updated)).Seconds()
		r.playback.Updated = now.UnixMilli()
	}

	snap := partySnapshot{
		Code:       r.code,
		Name:       r.name,
		HostID:     r.hostID,
		HostAddr:   r.hostAddr,
		Queue:      append([]partyQueueItem(nil), r.queue...),
		Current:    r.current,
		Playback:   r.playback,
		Version:    r.version,
		ServerTime: now.UnixMilli(),
	}
	for _, m := range r.members {
		snap.Members = append(snap.Members, *m)
	}
	sort.Slice(snap.Members, func(i, j int) bool {
		if snap.Members[i].Name == snap.Members[j].Name {
			return snap.Members[i].ID < snap.Members[j].ID
		}
		return snap.Members[i].Name < snap.Members[j].Name
	})

	for key, secs := range r.scores {
		name := r.names[key]
		if name == "" {
			name = key
		}
		snap.Leaderboard = append(snap.Leaderboard, partyLeaderRow{
			Name:      name,
			WatchSecs: secs,
			Online:    r.online[key],
		})
	}
	sort.Slice(snap.Leaderboard, func(i, j int) bool {
		if snap.Leaderboard[i].WatchSecs == snap.Leaderboard[j].WatchSecs {
			return snap.Leaderboard[i].Name < snap.Leaderboard[j].Name
		}
		return snap.Leaderboard[i].WatchSecs > snap.Leaderboard[j].WatchSecs
	})
	if len(snap.Leaderboard) > partyLeaderboardLimit {
		snap.Leaderboard = snap.Leaderboard[:partyLeaderboardLimit]
	}
	return snap
}
