package main

// notifications_store.go — the notification centre's rows survive a restart,
// and each row belongs to a source.
//
// The centre (notifications.go) used to keep its 50 rows in memory only, so an
// update offer, a failed download or a friend's challenge filed before a
// restart was gone after it. Rows worth reading again are written to
// <data>/notifications.json a couple of seconds after any change (and at
// exit), and read back the first time the bell or the centre needs them:
//
//   - kept: unread rows, warnings and errors, and every row that carries an
//     action; dropped: confirmations already seen ("Saved", "Copied"), which
//     only meant something at the moment they flashed by;
//   - bounded like the centre itself: notifHistoryMax rows, each message
//     notifStoreMaxMsg bytes, the file notifStoreMaxFile bytes — a larger or
//     unreadable file is ignored, never trusted;
//   - an action whose kind this build does not know is dropped from its row,
//     the row stays.
//
// Sources: a row names the part of JukaHub it came from, so the centre can
// keep a source's rows together ("Updates", "Downloads"…). Producers that
// know say so (notifPost); a plain toast is placed by a few words of its text
// (notifGuessSource), and anything else is "JukaHub".
//
// Nothing here runs under go test: the history there is the test's own.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

const (
	// notifStoreFile is the file under the data folder.
	notifStoreFile = "notifications.json"
	// notifStoreVersion is written into the file; another version is ignored.
	notifStoreVersion = 1
	// notifStoreMaxFile bounds what is read back.
	notifStoreMaxFile = 256 << 10
	// notifStoreMaxMsg bounds one message (bytes, cut at a rune).
	notifStoreMaxMsg = 1200
	// notifStoreDelay is how long after a change the file is written: a burst
	// of rows (a queue finishing) is one write.
	notifStoreDelay = 2 * time.Second
)

// The sources a producer can name. "" is JukaHub itself.
const (
	notifSrcUpdates    = "updates"
	notifSrcDownloads  = "downloads"
	notifSrcGames      = "games"
	notifSrcChallenges = "challenges"
	notifSrcMail       = "mail"
	notifSrcNetwork    = "network"
	notifSrcPower      = "power"
	notifSrcReminders  = "reminders"
)

// notifSourceLabel is a source's name above its first row.
func notifSourceLabel(src string) string {
	switch src {
	case notifSrcUpdates:
		return "Updates"
	case notifSrcDownloads:
		return "Downloads"
	case notifSrcGames:
		return "Games"
	case notifSrcChallenges:
		return "Ghost Challenges"
	case notifSrcMail:
		return "Mail"
	case notifSrcNetwork:
		return "Network"
	case notifSrcPower:
		return "Battery & power"
	case notifSrcReminders:
		return "Reminders"
	}
	return "JukaHub"
}

// notifGuessWords place a plain toast by the words it contains, first match
// wins. Wrong guesses cost nothing but the row's heading.
var notifGuessWords = []struct {
	src   string
	words []string
}{
	{notifSrcUpdates, []string{"update", "jukahub 20", "version history", "rolled back"}},
	{notifSrcDownloads, []string{"download"}},
	{notifSrcChallenges, []string{"challenge", "ghost"}},
	{notifSrcReminders, []string{"alarm", "timer", "reminder", "task due", "stopwatch"}},
	{notifSrcPower, []string{"battery", "charging", "charger", "power off", "suspend"}},
	{notifSrcMail, []string{"email", "e-mail", "inbox"}},
	{notifSrcNetwork, []string{"wi-fi", "wifi", "bluetooth", "vpn", "network", "internet", "offline"}},
	{notifSrcGames, []string{"trophy", "achievement", "milestone", "save state", "retroarch", "game"}},
}

// notifGuessSource is the source a row without one is filed under.
func notifGuessSource(msg string) string {
	lower := strings.ToLower(msg)
	for _, g := range notifGuessWords {
		for _, w := range g.words {
			if strings.Contains(lower, w) {
				return g.src
			}
		}
	}
	return ""
}

// notifGroupStarts reports whether row i is its source's first on the list.
func notifGroupStarts(history []notification, i int) bool {
	return i >= 0 && i < len(history) && (i == 0 || history[i-1].Source != history[i].Source)
}

// notifSpec is a row a producer files with notifPost.
type notifSpec struct {
	Message  string
	Priority notifPriority
	Source   string
	Action   notifAction
	// Key replaces an older row with the same key (one update offer).
	Key string
	// Toast shows the message as a toast too; Read files it already read (a
	// confirmation the user just watched).
	Toast bool
	Read  bool
}

// notifPost files a row with a source, an action or a key, and shows its toast
// when asked. The plain ShowToast and notifPush keep working as before; this
// is for the rows that can be acted on.
func notifPost(s notifSpec) {
	if s.Action.Kind != "" && s.Action.Label == "" {
		s.Action.Label = "Open"
	}
	notifFile(notification{Message: s.Message, Priority: s.Priority, Read: s.Read,
		Source: s.Source, Action: s.Action, Key: s.Key})
	if s.Toast {
		showToastPrimitive(s.Message, priorityToastKind(s.Priority))
	}
}

// ── On the card ───────────────────────────────────────────────────────────

// notifStoredRow is one row in the file.
type notifStoredRow struct {
	Msg    string `json:"msg"`
	Prio   int    `json:"prio,omitempty"`
	At     int64  `json:"at,omitempty"` // unix milliseconds
	N      uint64 `json:"n,omitempty"`  // order filed: larger is newer
	Read   bool   `json:"read,omitempty"`
	Repeat int    `json:"repeat,omitempty"`
	Source string `json:"source,omitempty"`
	Key    string `json:"key,omitempty"`
	// Live download rows are deliberately not written to the card.
	Act *notifAction `json:"act,omitempty"`
}

// notifStoredFile is the file.
type notifStoredFile struct {
	V    int              `json:"v"`
	Rows []notifStoredRow `json:"rows"`
}

var notifStore struct {
	once   sync.Once
	loaded bool // set by the load (under notifHistoryMu)
	mu     sync.Mutex
	queued bool       // a write is waiting for notifStoreDelay
	write  sync.Mutex // one write at a time
}

// notifStorePath is the file, or "" when there is no data folder.
func notifStorePath() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, notifStoreFile)
}

// notifKeeps reports whether a row is written to the card.
func notifKeeps(n notification) bool {
	return !n.Read || n.Priority >= notifWarning || n.Action.Kind != ""
}

// notifEnsureLoaded reads the card's rows into the history, once.
func notifEnsureLoaded() {
	if testing.Testing() {
		return
	}
	notifStore.once.Do(notifLoad)
}

// notifLoad reads the file. Rows filed before it (none, normally: every
// writer loads first) stay newest; the card's rows follow, regrouped.
func notifLoad() {
	path := notifStorePath()
	var rows []notification
	if path != "" {
		if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() && st.Size() <= notifStoreMaxFile {
			if data, err := os.ReadFile(path); err == nil {
				rows = notifParseStore(data)
			}
		}
	}
	notifHistoryMu.Lock()
	defer notifHistoryMu.Unlock()
	notifStore.loaded = true
	if len(rows) == 0 {
		return
	}
	// Oldest first gets the smallest number, then this run's rows above them.
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	notifSeq = 0
	for i := range rows {
		notifSeq++
		rows[i].ID = notifSeq
	}
	own := append([]notification(nil), nc.history...)
	sort.SliceStable(own, func(i, j int) bool { return own[i].ID < own[j].ID })
	for i := range own {
		notifSeq++
		own[i].ID = notifSeq
	}
	nc.history = nc.history[:0]
	for _, n := range append(rows, own...) {
		notifInsertLocked(n)
	}
	logInfo("notif", "%d notifications back from the card", len(rows))
}

// notifParseStore reads a file's rows, bounded and checked. The rows' IDs are
// the file's order numbers (larger is newer); notifLoad renumbers them.
func notifParseStore(data []byte) []notification {
	if len(data) == 0 || len(data) > notifStoreMaxFile {
		return nil
	}
	var f notifStoredFile
	if err := json.Unmarshal(data, &f); err != nil || f.V != notifStoreVersion {
		return nil
	}
	out := make([]notification, 0, min(len(f.Rows), notifHistoryMax))
	for i, r := range f.Rows {
		if len(out) >= notifHistoryMax {
			break
		}
		msg := strings.TrimSpace(notifCutBytes(r.Msg, notifStoreMaxMsg))
		if msg == "" {
			continue
		}
		n := notification{
			Message:  msg,
			Priority: notifPriority(min(max(r.Prio, int(notifInfo)), int(notifError))),
			Read:     r.Read,
			Repeat:   min(max(r.Repeat, 1), 9999),
			Source:   notifCleanWord(r.Source, 24),
			Key:      notifCleanWord(r.Key, 64),
			ID:       r.N,
		}
		if n.ID == 0 {
			// A file without order numbers lists the newest first.
			n.ID = uint64(len(f.Rows) - i)
		}
		n.Timestamp = time.Now()
		if r.At > 0 {
			n.Timestamp = time.UnixMilli(r.At)
		}
		if r.Act != nil {
			a := notifAction{
				Label: strings.TrimSpace(notifCutBytes(r.Act.Label, 60)),
				Kind:  notifCleanWord(r.Act.Kind, 40),
				Arg:   notifCutBytes(r.Act.Arg, 1024),
			}
			if notifActionKnown(a.Kind) && a.Label != "" {
				n.Action = a
			}
		}
		out = append(out, n)
	}
	return out
}

// notifEncodeStore is the file for rows (the history's order).
func notifEncodeStore(rows []notification) []byte {
	f := notifStoredFile{V: notifStoreVersion}
	for _, n := range rows {
		if !notifKeeps(n) || n.Key == notifDownloadKey {
			continue
		}
		r := notifStoredRow{Msg: notifCutBytes(n.Message, notifStoreMaxMsg), Prio: int(n.Priority), N: n.ID,
			Read: n.Read, Repeat: n.Repeat, Source: n.Source, Key: n.Key}
		if !n.Timestamp.IsZero() {
			r.At = n.Timestamp.UnixMilli()
		}
		if n.Action.Kind != "" {
			a := n.Action
			r.Act = &a
		}
		f.Rows = append(f.Rows, r)
	}
	data, err := json.Marshal(f)
	if err != nil {
		return nil
	}
	return data
}

// notifChanged writes the rows to the card a moment from now.
func notifChanged() {
	if testing.Testing() {
		return
	}
	notifStore.mu.Lock()
	if notifStore.queued {
		notifStore.mu.Unlock()
		return
	}
	notifStore.queued = true
	notifStore.mu.Unlock()
	time.AfterFunc(notifStoreDelay, notifSaveNow)
}

// notifSaveNow writes the rows now (and at exit, through Cleanup). Nothing is
// written before the card's rows were read: that would replace them.
func notifSaveNow() {
	notifStore.mu.Lock()
	notifStore.queued = false
	notifStore.mu.Unlock()
	notifStore.write.Lock()
	defer notifStore.write.Unlock()
	notifHistoryMu.Lock()
	loaded := notifStore.loaded
	rows := append([]notification(nil), nc.history...)
	notifHistoryMu.Unlock()
	path := notifStorePath()
	if !loaded || path == "" {
		return
	}
	data := notifEncodeStore(rows)
	if data == nil {
		return
	}
	if err := AtomicWrite(path, data, 0o644); err != nil {
		logWarn("notif", "could not keep the notifications: %v", err)
	}
}

func init() {
	if !testing.Testing() {
		Cleanup = append(Cleanup, notifSaveNow)
	}
}

// notifCutBytes cuts s to at most n bytes at a rune boundary.
func notifCutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// notifCleanWord keeps a stored name to lower-case letters, digits, dots,
// dashes and underscores, at most n of them; anything else empties it.
func notifCleanWord(s string, n int) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > n {
		return ""
	}
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_'
		if !ok {
			return ""
		}
	}
	return s
}
