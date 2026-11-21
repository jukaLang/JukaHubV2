package main

// libsort_articles.go — "The Legend of Zelda" sorts under L: the Games
// library's name order ignores a leading "The", "A" or "An".
//
// Articles sort "to the end of the name", as on a shelf in a shop. Without it a
// card's "The …" games pile up under T, while the No-Intro sets already
// spell the same games "Legend of Zelda, The - A Link to the Past" and sort
// them under L — so one game sat in two places depending on where the file
// came from. With Settings → Games & emulators → "Ignore 'The' when sorting"
// on (the default), both spellings sort as "legend of zelda …":
//
//   - The sort key (librarySortKey) is the name in lower case without a
//     leading article, and without No-Intro's trailing ", The" / ", A" / ", An"
//     where it ends the name or comes before a subtitle (" - ", ": ", " (").
//     It is the key only: the name on screen is unchanged.
//   - Every place that orders a console's games by name uses it
//     (sortLibraryGames — the Title sort and every other sort's tie-breaker —
//     and Home's Favourites), and the letter scrub (game_library_scrub.go)
//     names a game's letter from the same name, so L2/R2 to "L" lands on
//     Zelda and the bubble never shows a letter the order does not have.
//   - It costs one key per game per sort, computed before sorting
//     (librarySortStable) instead of two lower-casings per comparison.
//
// English articles only: the library's names are No-Intro's and the
// player's, and a foreign article ("Die", "Le") is as often the first word of
// a title as an article.
//
// The setting is read where the sorts run, including the scan's goroutine,
// which must not read the config map the main thread writes. So the sorts
// read an atomic copy, refreshed on the main thread by the Settings row and
// before every library build (librarySortArticlesSync).

import (
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// librarySortArticlesKey is the Custom setting (default true).
const librarySortArticlesKey = "librarySortIgnoreArticles"

// libSortArticlesOff is the setting as the sorts read it; the zero value is
// the default, On.
var libSortArticlesOff atomic.Bool

// libSortLeading are the articles a name may start with, lower case, with
// the space after them.
var libSortLeading = [...]string{"the ", "an ", "a "}

// libSortTrailing are No-Intro's articles moved behind the name.
var libSortTrailing = [...]string{", the", ", an", ", a"}

// librarySortArticlesOn is the stored setting (main thread).
func librarySortArticlesOn(c *Config) bool {
	if c == nil {
		return true
	}
	return customBool(c.Variables.Custom, librarySortArticlesKey, true)
}

// librarySortArticlesSync copies the setting for the sorts. Main thread,
// before a library build or re-sort.
func librarySortArticlesSync(c *Config) {
	if c == nil {
		return
	}
	libSortArticlesOff.Store(!librarySortArticlesOn(c))
}

// librarySortKey is the key a game's name sorts by: lower case and, with the
// setting on, without its article.
func librarySortKey(name string) string {
	key := strings.ToLower(strings.TrimSpace(name))
	if libSortArticlesOff.Load() {
		return key
	}
	return libSortDropArticles(key)
}

// libSortDropArticles takes a lower-case name's leading article and
// No-Intro's trailing one out. A name that is nothing but an article ("The")
// stays as it is. Pure.
func libSortDropArticles(key string) string {
	for _, art := range libSortLeading {
		if strings.HasPrefix(key, art) {
			if rest := strings.TrimLeft(key[len(art):], " "); rest != "" {
				key = rest
			}
			break
		}
	}
	for _, art := range libSortTrailing {
		if i := strings.Index(key, art); i > 0 && libSortArticleEnds(key[i+len(art):]) {
			return key[:i] + key[i+len(art):]
		}
	}
	return key
}

// libSortArticleEnds reports what may follow a trailing article: the end of
// the name, or a subtitle. ("Boy and His Blob, A - Trouble on Blobolonia",
// not "Pirates, Ahoy".)
func libSortArticleEnds(rest string) bool {
	if rest == "" {
		return true
	}
	for _, sep := range []string{" -", ":", " (", " ["} {
		if strings.HasPrefix(rest, sep) {
			return true
		}
	}
	return false
}

// librarySortLead is a name as the letter scrub reads it: without its
// leading article when the setting is on, in its own case. It agrees with
// librarySortKey on the first letter.
func librarySortLead(name string) string {
	name = strings.TrimSpace(name)
	if libSortArticlesOff.Load() {
		return name
	}
	for _, art := range libSortLeading {
		if len(name) > len(art) && strings.EqualFold(name[:len(art)], art) {
			if rest := strings.TrimLeft(name[len(art):], " "); rest != "" {
				return rest
			}
			return name
		}
	}
	return name
}

// libSortGames is a console's games beside their keys, swapped together.
type libSortGames struct {
	games []libraryGame
	keys  []string
	less  func(a, b *libraryGame, keyA, keyB string) bool
}

func (s libSortGames) Len() int { return len(s.games) }

func (s libSortGames) Less(i, j int) bool {
	return s.less(&s.games[i], &s.games[j], s.keys[i], s.keys[j])
}

func (s libSortGames) Swap(i, j int) {
	s.games[i], s.games[j] = s.games[j], s.games[i]
	s.keys[i], s.keys[j] = s.keys[j], s.keys[i]
}

// librarySortStable sorts games stably by less, which is handed each game's
// name key (librarySortKey), computed once per game.
func librarySortStable(games []libraryGame, less func(a, b *libraryGame, keyA, keyB string) bool) {
	if len(games) < 2 || less == nil {
		return
	}
	keys := make([]string, len(games))
	for i := range games {
		keys[i] = librarySortKey(games[i].Label())
	}
	sort.Stable(libSortGames{games: games, keys: keys, less: less})
}

// librarySortArticlesSettingsRow is the switch in Settings → Games &
// emulators. The library re-sorts at once; Home's copy at its next look.
func librarySortArticlesSettingsRow() setRow {
	return setRow{
		ID: "sortArticles", Label: "Ignore 'The' when sorting", Kind: setKindToggle,
		Detail: setStatic("The Legend of Zelda sorts under L · also A and An"),
		Get:    librarySortArticlesOn,
		Set: func(c *Config, on bool) {
			setStoreBool(c, librarySortArticlesKey, on)
			libSortArticlesOff.Store(!on)
			refreshLibraryView()
			homeTabsLib.at = time.Time{}
			if on {
				setToast("Games sort without The, A and An")
			} else {
				setToast("Games sort by their full names")
			}
		},
	}
}
