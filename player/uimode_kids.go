package main

// uimode_kids.go — which games Kid mode shows (uimode.go).
//
// A game is for kids when:
//   - its details record (gmeta_store.go, keyed by console and title, so a
//     renamed or moved file keeps it) carries the "For kids" tag, or
//   - its console is marked "whole console" (data/ui-mode.json), or
//   - it is one of JukaHub's own built-in games.
//
// The tag is set from the game page's More sheet (gamepage_more.go): one row,
// "For kids", whose A goes round No → This game → Every <console> game. The
// tag is not added to the tag sheet's list on purpose: in Kiosk mode the tag
// sheet is locked, and a tag a player could pick there would be a way into
// Kid mode's library. The library's metadata store has no age rating
// (gmetaRecord keeps the user's 1–5 stars), so there is no "from the rating"
// choice.
//
// The filters: uiModeKidSystems narrows a console list (the library's,
// currentGameSystems, which the random pick and the library search read, and
// the Tabs Home's consoles), uiModeKidRecents a recents list (the game
// switcher, Home's recent rows, Home search). The launch gate refuses
// anything else, so a view that was not filtered still cannot start it.

import (
	"strings"
)

// uiModeKidTag is the details-record tag that marks one game for kids.
const uiModeKidTag = "For kids"

// The "For kids" row's stops.
const (
	uiKidNo = iota
	uiKidGame
	uiKidConsole
)

// uiModeKidConsoleSet is the "whole console" list, folded for lookups.
func uiModeKidConsoleSet() map[string]bool {
	f := uiModeSnapshot()
	out := make(map[string]bool, len(f.KidConsoles))
	for _, c := range f.KidConsoles {
		out[strings.ToLower(c)] = true
	}
	return out
}

// uiModeKidGame reports whether one game is for kids. consoles is
// uiModeKidConsoleSet's answer (nil reads it).
func uiModeKidGame(system, title string, consoles map[string]bool) bool {
	if system == builtInLibrarySystem {
		return true
	}
	if consoles == nil {
		consoles = uiModeKidConsoleSet()
	}
	if consoles[strings.ToLower(strings.TrimSpace(system))] {
		return true
	}
	rec, ok := gmetaGet(system, title)
	return ok && gmetaHasTag(rec, uiModeKidTag)
}

// uiModeKidState is the "For kids" stop a game is on.
func uiModeKidState(game libraryGame) int {
	if uiModeKidConsoleSet()[strings.ToLower(strings.TrimSpace(game.System))] {
		return uiKidConsole
	}
	if rec, ok := gmetaGet(game.System, game.Title); ok && gmetaHasTag(rec, uiModeKidTag) {
		return uiKidGame
	}
	return uiKidNo
}

// uiModeSetKidState moves a game to a stop: No takes the tag and the
// console's mark away, This game sets the tag and takes the console's mark
// away, Every game marks the console (the game's own tag stays as it was).
func uiModeSetKidState(game libraryGame, state int) {
	system := strings.TrimSpace(game.System)
	if system == "" {
		return
	}
	switch state {
	case uiKidConsole:
		uiModeSetKidConsole(system, true)
	case uiKidGame:
		gmetaSetTag(game.System, game.Title, uiModeKidTag, true)
		uiModeSetKidConsole(system, false)
	default:
		gmetaSetTag(game.System, game.Title, uiModeKidTag, false)
		uiModeSetKidConsole(system, false)
	}
	invalidateGameLibrarySystemsCache()
}

// uiModeSetKidConsole marks or unmarks a console's every game.
func uiModeSetKidConsole(system string, on bool) {
	key := strings.ToLower(system)
	have := uiModeKidConsoleSet()[key]
	if have == on {
		return
	}
	uiModeUpdate(func(f *uiModeFile) {
		kept := f.KidConsoles[:0]
		for _, c := range f.KidConsoles {
			if strings.ToLower(c) != key {
				kept = append(kept, c)
			}
		}
		f.KidConsoles = kept
		if on {
			f.KidConsoles = append(f.KidConsoles, system)
		}
	})
}

// uiModeKidStateLabel is a stop's name on the row.
func uiModeKidStateLabel(state int, system string) string {
	switch state {
	case uiKidGame:
		return "This game"
	case uiKidConsole:
		if system == "" {
			return "Whole console"
		}
		return "Every " + system + " game"
	}
	return "No"
}

// uiModeKidCount counts the games tagged for kids in the details store and
// the consoles marked whole. Settings' row; not per frame.
func uiModeKidCount() (games, consoles int) {
	gmetaStore.mu.Lock()
	gmetaEnsureLocked()
	for _, rec := range gmetaStore.games {
		if rec != nil && gmetaContains(rec.Tags, uiModeKidTag) {
			games++
		}
	}
	gmetaStore.mu.Unlock()
	return games, len(uiModeSnapshot().KidConsoles)
}

// ── Filters ───────────────────────────────────────────────────────────────

// uiModeKidSystems narrows a console list to the games for kids in Kid mode
// (consoles left empty go); any other mode gets the list back untouched.
func uiModeKidSystems(systems []librarySystem) []librarySystem {
	if !uiModeIsKid() {
		return systems
	}
	tagged := gmetaTaggedPaths(systems, uiModeKidTag)
	consoles := uiModeKidConsoleSet()
	out := make([]librarySystem, 0, len(systems))
	for _, system := range systems {
		var games []libraryGame
		for _, g := range system.Games {
			if tagged[g.Path] || g.System == builtInLibrarySystem || consoles[strings.ToLower(strings.TrimSpace(g.System))] {
				games = append(games, g)
			}
		}
		if len(games) == 0 {
			continue
		}
		out = append(out, librarySystem{Name: system.Name, Games: games, WithArt: countCoveredGames(games), Collection: system.Collection})
	}
	return out
}

// uiModeKidRecents narrows a recents list to the games for kids in Kid mode.
// It returns a new slice: the list it is given may be the store's copy that a
// caller saves back, and nothing here may drop a game from the file.
func uiModeKidRecents(list []recentGame) []recentGame {
	if !uiModeIsKid() {
		return list
	}
	consoles := uiModeKidConsoleSet()
	out := make([]recentGame, 0, len(list))
	for _, r := range list {
		title := strings.TrimSpace(r.Title)
		if title == "" {
			title = gameDisplayTitle(r.Path)
		}
		if uiModeKidGame(r.System, title, consoles) {
			out = append(out, r)
		}
	}
	return out
}

// uiModeLaunchHeld is launchLibraryGame's first line (game_library.go): in
// Kid mode a game that is not for kids does not start, whichever view offered
// it. It runs before the Android, HTML5 and ports branches, which return
// before the launch gates (gamelaunch_hooks.go), and before the gates
// themselves — CHIP-8's starts its game on its own, and Game Ready's or a
// shelf's card must not come up for a game that will not start. A refused
// launch counts as held (gameLaunchWasHeld), so no caller says "Playing".
// Main thread.
func uiModeLaunchHeld(game libraryGame) bool {
	if !uiModeIsKid() {
		return false
	}
	// The title the recents filter reads when a recents entry has none.
	title := strings.TrimSpace(game.Title)
	if title == "" {
		title = gameDisplayTitle(game.Path)
	}
	if uiModeKidGame(game.System, title, nil) {
		return false
	}
	gameLaunchHeld = game.Path
	PlayErrorSound()
	ShowToast(game.Label()+" is not marked for kids", ToastKindWarning)
	logInfo("uimode", "kid mode: launch of a game not for kids refused (%s)", game.System)
	return true
}

// ── The game page ─────────────────────────────────────────────────────────

// uiKidPage is the open page's stop, read when it opens: the chip is drawn
// every frame and must not take the store's lock each time.
var uiKidPage struct {
	path  string
	state int
}

func init() {
	gamePageRegisterOpened(func(game libraryGame) {
		uiKidPage.path, uiKidPage.state = game.Path, uiModeKidState(game)
	})
	gamePageRegisterExtra(gamePageExtra{
		ID: "forkids", Order: 90, Icon: "user",
		Row: func(game libraryGame) (string, string, bool) {
			if isBuiltInLibraryGame(game) {
				return "", "", false
			}
			state := uiModeKidState(game)
			return "For kids: " + uiModeKidStateLabel(state, game.System),
				"Kid mode shows only these · A changes it", true
		},
		Open: func(game libraryGame) {
			if !uiModeGate(uiFeatSetup) {
				return
			}
			next := (uiModeKidState(game) + 1) % 3
			uiModeSetKidState(game, next)
			uiKidPage.path, uiKidPage.state = game.Path, next
			switch next {
			case uiKidConsole:
				ShowToast("Every "+game.System+" game is for kids", ToastKindSuccess)
			case uiKidGame:
				ShowToast(game.Label()+" is for kids", ToastKindSuccess)
			default:
				ShowToast(game.Label()+": not for kids", ToastKindInfo)
			}
		},
	})
	gamePageRegisterStatus(gamePageStatus{
		ID: "forkids", Order: 90,
		Chip: func(game libraryGame) (string, int, bool) {
			if game.Path != uiKidPage.path || uiKidPage.state == uiKidNo {
				return "", gamePageToneNeutral, false
			}
			return "For kids", gamePageToneGood, true
		},
	})
}
