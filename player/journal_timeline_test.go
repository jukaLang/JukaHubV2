package main

import (
	"strings"
	"testing"
	"time"
)

func TestJournalTimelineAndThisYear(t *testing.T) {
	oldLoaded, oldData := gameHistoryStore.loaded, gameHistoryStore.data
	defer func() {
		gameHistoryStore.mu.Lock()
		gameHistoryStore.loaded, gameHistoryStore.data = oldLoaded, oldData
		gameHistoryStore.mu.Unlock()
	}()
	now := time.Now()
	yesterday := now.AddDate(0, 0, -1)
	gameHistoryStore.mu.Lock()
	gameHistoryStore.loaded = true
	gameHistoryStore.data = gameHistoryFile{Version: gameHistoryVersion, Games: map[string]gameHistoryRecord{
		playtimeGameKey("Game Boy Advance", "Chrono Trigger"): {Sessions: []gameHistorySession{
			{Started: yesterday, Seconds: 120},
			{Started: now, Seconds: 60},
		}},
		playtimeGameKey("SNES", "Last year"): {Sessions: []gameHistorySession{
			{Started: now.AddDate(-1, 0, 0), Seconds: 9999},
		}},
	}}
	gameHistoryStore.mu.Unlock()

	list := journalTimeline(10)
	if len(list) != 3 {
		t.Fatalf("timeline = %d sessions, want 3", len(list))
	}
	if list[0].Title != "Chrono Trigger" || list[0].System != "Game Boy Advance" || list[0].Seconds != 60 {
		t.Fatalf("newest = %+v", list[0])
	}
	if got := journalThisYear(); got.Sessions != 2 || got.Seconds != 180 || got.MostPlayed != "Chrono Trigger" {
		t.Fatalf("this year = %+v", got)
	}
	if streak := journalStreak(journalThisYear().Days); streak != 2 {
		t.Fatalf("streak = %d days, want 2", streak)
	}
	lines := strings.Join(journalLines(false), "\n")
	for _, want := range []string{"This year:", "Chrono Trigger", "Sessions", "Most played", "streak"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("journal has no %q:\n%s", want, lines)
		}
	}
}

func TestJournalRecapCard(t *testing.T) {
	oldLoaded, oldData := gameHistoryStore.loaded, gameHistoryStore.data
	defer func() {
		gameHistoryStore.mu.Lock()
		gameHistoryStore.loaded, gameHistoryStore.data = oldLoaded, oldData
		gameHistoryStore.mu.Unlock()
	}()
	gameHistoryStore.mu.Lock()
	gameHistoryStore.loaded = true
	gameHistoryStore.data = gameHistoryFile{Version: gameHistoryVersion, Games: map[string]gameHistoryRecord{
		playtimeGameKey("GBA", "A Game"): {Sessions: []gameHistorySession{
			{Started: time.Now(), Seconds: 3600},
		}},
	}}
	gameHistoryStore.mu.Unlock()
	card := journalRecapText()
	for _, want := range []string{"JukaHub Play Journal", "Hours played: 1.0 hours", "Most played: A Game", "Games beaten this year"} {
		if !strings.Contains(card, want) {
			t.Fatalf("recap has no %q:\n%s", want, card)
		}
	}
	if !strings.Contains(card, "1 session across 1 day") {
		t.Fatalf("recap should say one session:\n%s", card)
	}
}

func TestJournalDurationAndEmptyTimeline(t *testing.T) {
	if got := journalDuration(45); got != "0 min" {
		t.Fatalf("45 s = %q", got)
	}
	if got := journalDuration(600); got != "10 min" {
		t.Fatalf("10 min = %q", got)
	}
	if got := journalDuration(7200); got != "2.0 hours" {
		t.Fatalf("2 h = %q", got)
	}
	if got := journalStreak(nil); got != 0 {
		t.Fatalf("empty streak = %d", got)
	}
}
