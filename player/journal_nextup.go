package main

// journal_nextup.go — the "next up" suggestion from the Backlog tag. A game
// that has waited longer is a stronger candidate, so the oldest wait wins;
// there is no dice roll, which makes the suggestion repeatable and easy to
// explain. The title breaks a tie.

import (
	"sort"
	"strings"
	"time"
)

type gameBacklogWait struct {
	game libraryGame
	days int
}

// gameBacklogWaits is the library's Backlog games, waited-on longest first.
// It reads the tag store directly: the library snapshot can be a scan or two
// behind a status the user just changed on the game page.
func gameBacklogWaits(lib gameLibraryReport) []gameBacklogWait {
	now := time.Now()
	seen := map[string]bool{}
	var out []gameBacklogWait
	for _, system := range lib.Systems {
		if system.Name == continueSystemName {
			continue
		}
		for _, game := range system.Games {
			rec, ok := gmetaGet(game.System, game.Title)
			if !ok || !gmetaHasTag(rec, gmetaTagBacklog) {
				continue
			}
			key := strings.TrimSpace(game.Path)
			if key == "" {
				key = game.System + "|" + game.Title
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			days := 0
			if !game.AddedAt.IsZero() && game.AddedAt.Before(now) {
				days = int(now.Sub(game.AddedAt).Hours() / 24)
			}
			out = append(out, gameBacklogWait{game: game, days: days})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].days != out[j].days {
			return out[i].days > out[j].days
		}
		return strings.ToLower(out[i].game.Label()) < strings.ToLower(out[j].game.Label())
	})
	return out
}

// gameNextUpPick is the game to suggest, its wait in days, and whether there
// was a backlog to pick from.
func gameNextUpPick(lib gameLibraryReport) (libraryGame, int, bool) {
	list := gameBacklogWaits(lib)
	if len(list) == 0 {
		return libraryGame{}, 0, false
	}
	return list[0].game, list[0].days, true
}

// gameWaitLabel says how long a backlog game has waited, for a menu line or a
// toast. A game added in this session has no days yet.
func gameWaitLabel(days int) string {
	if days <= 0 {
		return "just added"
	}
	return "waiting " + pluralizeCount(days, "day", "days")
}
