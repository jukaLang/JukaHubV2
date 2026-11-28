package main

// playstyle.go — the "Play style" idea: a plain-language reading of the
// session log. Pure, so it can be tested without a card or a scene.

import "fmt"

// playStyleFromSessions names how someone plays, from the sessions already
// being kept for Wrapped and the playtime ledger. It never invents data.
func playStyleFromSessions(sessions []playtimeSession) (title, detail string) {
	if len(sessions) == 0 {
		return "Not enough data yet", "Play a few games and the pattern shows up here"
	}
	var total int64
	distinct := map[string]bool{}
	night := 0
	for _, s := range sessions {
		if s.Seconds <= 0 {
			continue
		}
		total += s.Seconds
		distinct[s.Hash] = true
		h := s.Start.Hour()
		if h >= 22 || h < 5 {
			night++
		}
	}
	if total <= 0 {
		return "Not enough data yet", "No finished sessions to read yet"
	}
	avg := total / int64(len(sessions))
	nightPct := night * 100 / len(sessions)
	switch {
	case nightPct >= 40:
		title = "Night owl"
	case len(distinct) >= 8 && avg < 15*60:
		title = "Explorer"
	case len(distinct) <= 3 && total >= 10*60*60:
		title = "One-game completionist"
	case avg >= 60*60:
		title = "Marathon player"
	default:
		title = "All-rounder"
	}
	detail = fmt.Sprintf("%d sessions across %d games · %s total · %d%% after dark",
		len(sessions), len(distinct), formatPlaytime(total), nightPct)
	return title, detail
}
