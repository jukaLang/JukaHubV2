package main

// game_history.go keeps a small per-game session log beside the existing
// lifetime playtime ledger. A launch is written before the game takes over the
// screen; an unfinished launch survives a power loss as an interrupted entry.

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	gameHistoryVersion     = 1
	gameHistoryMaxGames    = 128
	gameHistoryMaxSessions = 48
)

type gameHistorySession struct {
	Started     time.Time `json:"started"`
	Ended       time.Time `json:"ended,omitempty"`
	Seconds     int64     `json:"seconds,omitempty"`
	Interrupted bool      `json:"interrupted,omitempty"`
}

type gameHistoryRecord struct {
	Launches  int                  `json:"launches"`
	Completed int                  `json:"completed,omitempty"`
	Total     int64                `json:"total,omitempty"`
	Longest   int64                `json:"longest,omitempty"`
	Days      map[string]int64     `json:"days,omitempty"`
	Sessions  []gameHistorySession `json:"sessions,omitempty"`
}

type gameHistoryFile struct {
	Version int                          `json:"version"`
	Games   map[string]gameHistoryRecord `json:"games"`
}

var gameHistoryStore struct {
	mu     sync.Mutex
	loaded bool
	data   gameHistoryFile
}

// gameHistoryPath is replaceable in tests.
var gameHistoryPath = func() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, "game-history.json")
}

func gameHistoryLoadLocked() {
	if gameHistoryStore.loaded {
		return
	}
	gameHistoryStore.loaded = true
	gameHistoryStore.data = gameHistoryFile{Version: gameHistoryVersion, Games: map[string]gameHistoryRecord{}}
	path := gameHistoryPath()
	if path == "" {
		return
	}
	var saved gameHistoryFile
	if _, err := readJSONWithBackup(path, &saved); err != nil || saved.Version != gameHistoryVersion {
		return
	}
	if saved.Games == nil {
		saved.Games = map[string]gameHistoryRecord{}
	}
	gameHistoryStore.data = saved
	changed := false
	for key, rec := range saved.Games {
		if (rec.Completed == 0 || rec.Total == 0) && len(rec.Sessions) > 0 {
			count, total := 0, int64(0)
			for _, s := range rec.Sessions {
				if s.Seconds > 0 {
					count++
					total += s.Seconds
				}
			}
			if rec.Completed == 0 {
				rec.Completed = count
			}
			if rec.Total == 0 {
				rec.Total = total
			}
			changed = changed || total > 0
		}
		for i := range rec.Sessions {
			if rec.Sessions[i].Ended.IsZero() && !rec.Sessions[i].Interrupted {
				rec.Sessions[i].Interrupted = true
				changed = true
			}
		}
		saved.Games[key] = rec
	}
	gameHistoryStore.data = saved
	if changed {
		gameHistorySaveLocked()
	}
}

func gameHistorySaveLocked() {
	path := gameHistoryPath()
	if path == "" {
		return
	}
	data, err := json.MarshalIndent(gameHistoryStore.data, "", "  ")
	if err == nil {
		if err = AtomicWrite(path, append(data, '\n'), 0o644); err != nil {
			logWarn("library", "cannot save game history: %v", err)
		}
	}
}

func gameHistoryPruneLocked() {
	for len(gameHistoryStore.data.Games) > gameHistoryMaxGames {
		oldestKey := ""
		var oldest time.Time
		for key, rec := range gameHistoryStore.data.Games {
			last := time.Time{}
			if n := len(rec.Sessions); n > 0 {
				last = rec.Sessions[n-1].Started
			}
			if oldestKey == "" || last.Before(oldest) || (last.Equal(oldest) && key < oldestKey) {
				oldestKey, oldest = key, last
			}
		}
		delete(gameHistoryStore.data.Games, oldestKey)
	}
}

func gameHistoryLaunch(game libraryGame, when time.Time) {
	key := playtimeGameKey(game.System, game.Title)
	if key == "" || when.IsZero() {
		return
	}
	gameHistoryStore.mu.Lock()
	defer gameHistoryStore.mu.Unlock()
	gameHistoryLoadLocked()
	rec := gameHistoryStore.data.Games[key]
	for i := range rec.Sessions {
		if rec.Sessions[i].Ended.IsZero() && !rec.Sessions[i].Interrupted {
			rec.Sessions[i].Interrupted = true
		}
	}
	rec.Launches++
	rec.Sessions = append(rec.Sessions, gameHistorySession{Started: when})
	if len(rec.Sessions) > gameHistoryMaxSessions {
		rec.Sessions = append([]gameHistorySession(nil), rec.Sessions[len(rec.Sessions)-gameHistoryMaxSessions:]...)
	}
	gameHistoryStore.data.Games[key] = rec
	gameHistoryPruneLocked()
	gameHistorySaveLocked()
}

// gameHistoryFinishGap settles the launch that preceded a parked render loop.
// Short starts are retained with zero playtime; they count as launches but do
// not change the existing lifetime playtime rule.
func gameHistoryFinishGap(recents []recentGame, gapStart, gapEnd time.Time) {
	if len(recents) == 0 {
		return
	}
	g := recents[0]
	if g.PlayedAt.IsZero() || g.PlayedAt.Sub(gapStart) < -playtimeGameLaunchSlack ||
		g.PlayedAt.Sub(gapStart) > playtimeGameLaunchSlack {
		return
	}
	title := strings.TrimSpace(g.Title)
	if title == "" {
		title = gameDisplayTitle(g.Path)
	}
	key := playtimeGameKey(g.System, title)
	if key == "" {
		return
	}
	seconds := int64(0)
	length := gapEnd.Sub(gapStart)
	if length >= playtimeGameMinSession {
		if length > playtimeGameMaxSession {
			length = playtimeGameMaxSession
		}
		seconds = int64(length / time.Second)
	}
	gameHistoryStore.mu.Lock()
	defer gameHistoryStore.mu.Unlock()
	gameHistoryLoadLocked()
	rec := gameHistoryStore.data.Games[key]
	for i := len(rec.Sessions) - 1; i >= 0; i-- {
		s := &rec.Sessions[i]
		if s.Ended.IsZero() && !s.Interrupted && s.Started.Sub(g.PlayedAt) < time.Second &&
			s.Started.Sub(g.PlayedAt) > -time.Second {
			s.Ended, s.Seconds = gapEnd, seconds
			if seconds > 0 {
				rec.Completed++
				rec.Total += seconds
				if rec.Days == nil {
					rec.Days = map[string]int64{}
				}
				rec.Days[gapEnd.Format("2006-01-02")] += seconds
				cutoff := gapEnd.AddDate(0, 0, -29).Format("2006-01-02")
				for day := range rec.Days {
					if day < cutoff {
						delete(rec.Days, day)
					}
				}
			}
			if seconds > rec.Longest {
				rec.Longest = seconds
			}
			gameHistoryStore.data.Games[key] = rec
			gameHistorySaveLocked()
			return
		}
	}
}

func gameHistoryFor(system, title string) gameHistoryRecord {
	gameHistoryStore.mu.Lock()
	defer gameHistoryStore.mu.Unlock()
	gameHistoryLoadLocked()
	rec := gameHistoryStore.data.Games[playtimeGameKey(system, title)]
	rec.Sessions = append([]gameHistorySession(nil), rec.Sessions...)
	return rec
}

type gameHistorySummary struct {
	Launches, Sessions     int
	Longest, Last7, Last30 int64
	Interrupted            int
	Recent                 []gameHistorySession
}

func gameHistorySummarize(rec gameHistoryRecord, now time.Time) gameHistorySummary {
	out := gameHistorySummary{Launches: rec.Launches, Sessions: rec.Completed, Longest: rec.Longest}
	week, month := now.AddDate(0, 0, -6), now.AddDate(0, 0, -29)
	today := now.Format("2006-01-02")
	if len(rec.Days) > 0 {
		for day, secs := range rec.Days {
			if day >= week.Format("2006-01-02") && day <= today {
				out.Last7 += secs
			}
			if day >= month.Format("2006-01-02") && day <= today {
				out.Last30 += secs
			}
		}
	}
	for _, s := range rec.Sessions {
		if s.Interrupted {
			out.Interrupted++
		}
		if s.Seconds > 0 {
			if s.Seconds > out.Longest {
				out.Longest = s.Seconds
			}
			if len(rec.Days) == 0 {
				if !s.Ended.Before(week) && !s.Ended.After(now) {
					out.Last7 += s.Seconds
				}
				if !s.Ended.Before(month) && !s.Ended.After(now) {
					out.Last30 += s.Seconds
				}
			}
		}
	}
	out.Recent = append([]gameHistorySession(nil), rec.Sessions...)
	sort.SliceStable(out.Recent, func(i, j int) bool { return out.Recent[i].Started.After(out.Recent[j].Started) })
	return out
}
