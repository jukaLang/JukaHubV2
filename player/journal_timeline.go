package main

// journal_timeline.go — the Journal page's data. It reads the launch history
// (game-history.json) and the game page's statuses; it writes nothing. The
// per-game history overlay (game_history_ui.go) already shows one game's
// sessions; this is the card-level view across the whole library.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type journalSessionEntry struct {
	System  string
	Title   string
	Started time.Time
	Seconds int64
}

// journalTimeline is the latest sessions across every game, newest first.
func journalTimeline(limit int) []journalSessionEntry {
	gameHistoryStore.mu.Lock()
	gameHistoryLoadLocked()
	games := make(map[string]gameHistoryRecord, len(gameHistoryStore.data.Games))
	for key, rec := range gameHistoryStore.data.Games {
		games[key] = rec
	}
	gameHistoryStore.mu.Unlock()

	var out []journalSessionEntry
	for key, rec := range games {
		system, title := playtimeGameKeyParts(key)
		for _, s := range rec.Sessions {
			if s.Started.IsZero() {
				continue
			}
			secs := s.Seconds
			if secs == 0 && !s.Ended.IsZero() {
				secs = int64(s.Ended.Sub(s.Started) / time.Second)
			}
			if secs < 0 {
				secs = 0
			}
			out = append(out, journalSessionEntry{System: system, Title: title, Started: s.Started, Seconds: secs})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// journalYear is this year's total play time and most-played game. A session
// belongs to the year it started in, so a game left running at midnight stays
// with the day the player sat down.
type journalYear struct {
	Seconds    int64
	Sessions   int
	Days       map[string]bool
	MostPlayed string
	MostTime   int64
}

// journalThisYear sums the current year's sessions.
func journalThisYear() journalYear {
	year := time.Now().Year()
	byGame := map[string]int64{}
	out := journalYear{Days: map[string]bool{}}
	gameHistoryStore.mu.Lock()
	gameHistoryLoadLocked()
	copyGames := make(map[string]gameHistoryRecord, len(gameHistoryStore.data.Games))
	for key, rec := range gameHistoryStore.data.Games {
		copyGames[key] = rec
	}
	gameHistoryStore.mu.Unlock()
	for key, rec := range copyGames {
		_, title := playtimeGameKeyParts(key)
		for _, s := range rec.Sessions {
			if s.Started.Year() != year {
				continue
			}
			secs := s.Seconds
			if secs == 0 && !s.Ended.IsZero() {
				secs = int64(s.Ended.Sub(s.Started) / time.Second)
			}
			if secs < 0 {
				secs = 0
			}
			out.Seconds += secs
			out.Sessions++
			out.Days[s.Started.Format("2006-01-02")] = true
			byGame[title] += secs
			if byGame[title] > out.MostTime {
				out.MostTime, out.MostPlayed = byGame[title], title
			}
		}
	}
	return out
}

// journalStreak is the run of days, ending today or yesterday, that had a
// session. A gap of one day is a streak of one; two days is a streak of two.
func journalStreak(days map[string]bool) int {
	if len(days) == 0 {
		return 0
	}
	day, err := time.Parse("2006-01-02", time.Now().Format("2006-01-02"))
	if err != nil {
		return 0
	}
	if !days[day.Format("2006-01-02")] {
		day = day.AddDate(0, 0, -1)
	}
	streak := 0
	for days[day.Format("2006-01-02")] {
		streak++
		day = day.AddDate(0, 0, -1)
	}
	return streak
}

// journalCounts is one count per built-in status, from the game store.
func journalCounts() map[string]int {
	out := map[string]int{}
	gmetaStore.mu.Lock()
	gmetaEnsureLocked()
	for _, rec := range gmetaStore.games {
		out[gmetaStatus(*rec)]++
	}
	gmetaStore.mu.Unlock()
	return out
}

// journalBeatenThisYear is how many games have a completion stamp this year.
func journalBeatenThisYear() int {
	year := time.Now().Year()
	n := 0
	gmetaStore.mu.Lock()
	gmetaEnsureLocked()
	for _, rec := range gmetaStore.games {
		if autoCollCompletionTime(*rec).Year() == year {
			n++
		}
	}
	gmetaStore.mu.Unlock()
	return n
}

// journalSave is the page's X action.
func journalSave() {
	path, err := journalExport()
	if err != nil {
		ShowToast("Could not save the journal recap: "+err.Error(), ToastKindError)
		return
	}
	ShowToast("Year in review saved to "+path, ToastKindSuccess)
}

// journalRecapText is the shareable card: this year's figures and the
// library's statuses, without the session-by-session detail.
func journalRecapText() string {
	year := journalThisYear()
	counts := journalCounts()
	title := "JukaHub Play Journal — " + itoa(time.Now().Year())
	lines := []string{title, strings.Repeat("=", len([]rune(title))),
		"Hours played: " + journalDuration(year.Seconds),
		"Sessions: " + pluralizeCount(year.Sessions, "session", "sessions") + " across " + pluralizeCount(len(year.Days), "day", "days"),
		"Longest streak: " + pluralizeCount(journalStreak(year.Days), "day", "days"),
		"Games beaten this year: " + itoa(journalBeatenThisYear()),
	}
	if year.MostPlayed != "" {
		lines = append(lines, "Most played: "+year.MostPlayed+" ("+journalDuration(year.MostTime)+")")
	}
	parts := make([]string, 0, 6)
	for _, tag := range []string{gmetaTagPlaying, gmetaTagBacklog, gmetaTagWishlist, gmetaTagDropped, gmetaTagCompleted, gmetaTagFavorite} {
		if n := counts[tag]; n > 0 {
			parts = append(parts, itoa(n)+" "+tag)
		}
	}
	if len(parts) > 0 {
		lines = append(lines, "Library: "+strings.Join(parts, " · "))
	}
	return strings.Join(lines, "\n") + "\n"
}

// journalExport writes the recap beside the other data folders and returns
// its path. It is plain text so it can be copied off the card or pasted into
// a message; nothing is sent anywhere.
func journalExport() (string, error) {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("no data folder")
	}
	dir = filepath.Join(dir, "journal")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "recap-"+itoa(time.Now().Year())+".txt")
	if err := AtomicWrite(path, []byte(journalRecapText()), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// journalDuration writes a play time the way the rest of the app does: hours
// once it reaches one, minutes before that.
func journalDuration(seconds int64) string {
	if seconds < 60 {
		return "0 min"
	}
	minutes := seconds / 60
	if minutes < 60 {
		return fmt.Sprintf("%d min", minutes)
	}
	return fmt.Sprintf("%.1f hours", float64(seconds)/3600)
}

// journalLines is the page as plain text, like the BIOS Doctor and ROM Audit
// reports: it can be scrolled, wrapped and saved without a second list model.
func journalLines(full bool) []string {
	year := journalThisYear()
	counts := journalCounts()
	var lines []string
	lines = append(lines, "This year: "+journalDuration(year.Seconds)+
		" in "+pluralizeCount(year.Sessions, "session", "sessions")+
		" on "+pluralizeCount(len(year.Days), "day", "days"))
	if year.MostPlayed != "" {
		lines = append(lines, "  Most played: "+year.MostPlayed+" ("+journalDuration(year.MostTime)+")")
	}
	lines = append(lines, "  Beaten this year: "+itoa(journalBeatenThisYear())+
		" · longest streak: "+pluralizeCount(journalStreak(year.Days), "day", "days"))
	status := []string{gmetaTagPlaying, gmetaTagBacklog, gmetaTagWishlist, gmetaTagDropped, gmetaTagCompleted, gmetaTagFavorite}
	parts := make([]string, 0, len(status))
	for _, tag := range status {
		if n := counts[tag]; n > 0 {
			parts = append(parts, itoa(n)+" "+tag)
		}
	}
	if len(parts) > 0 {
		lines = append(lines, "Library: "+strings.Join(parts, " · "))
	} else {
		lines = append(lines, "Library: no statuses set yet")
	}
	lines = append(lines, "")
	sessions := journalTimeline(300)
	if len(sessions) == 0 {
		lines = append(lines, "No sessions yet. Play a game and its time will appear here.")
		return lines
	}
	lines = append(lines, "Sessions")
	for _, s := range sessions {
		name := s.Title
		if s.System != "" {
			name += " (" + s.System + ")"
		}
		lines = append(lines, "  "+s.Started.Format("2006-01-02 15:04")+"  "+journalDuration(s.Seconds)+"  "+name)
	}
	if !full && len(sessions) == 300 {
		lines = append(lines, "  ... the newest 300 sessions are shown")
	}
	return lines
}
