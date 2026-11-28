package main

import (
	"fmt"
	"testing"
	"time"
)

func TestPlayStyleFromSessions(t *testing.T) {
	if title, _ := playStyleFromSessions(nil); title != "Not enough data yet" {
		t.Fatalf("empty history = %q", title)
	}
	mk := func(count, games int, hour int, minutes int64) []playtimeSession {
		var out []playtimeSession
		for i := 0; i < count; i++ {
			out = append(out, playtimeSession{
				Hash:    fmt.Sprintf("hash%d", i%games),
				Start:   time.Date(2026, 11, 20, hour, 0, 0, 0, time.UTC).AddDate(0, 0, i),
				Seconds: minutes * 60,
			})
		}
		return out
	}
	if title, _ := playStyleFromSessions(mk(20, 3, 23, 30)); title != "Night owl" {
		t.Fatalf("late sessions = %q", title)
	}
	if title, _ := playStyleFromSessions(mk(20, 12, 12, 8)); title != "Explorer" {
		t.Fatalf("many short games = %q", title)
	}
	if title, _ := playStyleFromSessions(mk(20, 2, 12, 60)); title != "One-game completionist" {
		t.Fatalf("two big games = %q", title)
	}
	if title, detail := playStyleFromSessions(mk(4, 4, 12, 40)); title != "All-rounder" || detail == "" {
		t.Fatalf("mixed history = %q, %q", title, detail)
	}
}
