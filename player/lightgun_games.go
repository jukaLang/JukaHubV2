package main

// lightgun_games.go — the games that were made for a light gun, so the Light
// gun layout can come by itself the first time one starts (lightgunGate).
//
// The list is matched against the library's title loosely: lower case, the
// region and revision tags in brackets dropped, punctuation and a leading or
// trailing "The" ignored, roman numerals II–IV read as digits. A name of two
// words or more also matches inside a longer title ("Super Mario Bros. + Duck
// Hunt"), the longest name winning ("Die Hard Trilogy 2" over "Die Hard
// Trilogy"); a one-word name has to be the whole title ("Wanted", "Chiller").
// Arcade games are matched by their set name as well ("opwolf.zip").
//
// Such a match inside a longer title tells which gun a game wants and says
// "made for a light gun", but only a whole-title match (or a set name) gives
// the gun by itself: "Hang-On & Safari Hunt" is a racing game too, and a
// Light Phaser on port 1 takes its pad; "Super Mario Bros. + Duck Hunt" on a
// Brick would lose its D-pad. Games that need the pad beside the gun (a gun
// stage among others) are marked NoAuto for the same reason.
//
// Kind is the gun a game wants when its console had two: "justifier" for the
// Konami Justifier games on the SNES, the Mega Drive, the Sega CD and the
// PlayStation, "" for the console's usual gun (lightgunCores).

import (
	"path/filepath"
	"strings"
	"sync"
)

// lightgunTitle is one game of the list.
type lightgunTitle struct {
	System string
	Kind   string
	// Names are the title and its other names, as written; Sets are arcade
	// set names.
	Names []string
	Sets  []string
	// NoAuto: the game needs the pad as well, so it never gets the gun by
	// itself. Tag is text the title or file name must hold ("light phaser":
	// the game was also sold for the pad under the same name).
	NoAuto bool
	Tag    string
}

// lightgunTitles is the list, by console.
var lightgunTitles = []lightgunTitle{
	// NES Zapper.
	{System: "NES", Names: []string{"Duck Hunt"}},
	{System: "NES", Names: []string{"Hogan's Alley"}},
	{System: "NES", Names: []string{"Wild Gunman"}},
	{System: "NES", Names: []string{"Gumshoe"}},
	{System: "NES", Names: []string{"Gotcha! The Sport!", "Gotcha"}},
	{System: "NES", Names: []string{"Shooting Range"}},
	{System: "NES", Names: []string{"To the Earth"}},
	{System: "NES", Names: []string{"Mechanized Attack"}},
	{System: "NES", Names: []string{"Operation Wolf"}},
	{System: "NES", Names: []string{"Freedom Force"}},
	{System: "NES", Names: []string{"Baby Boomer"}},
	{System: "NES", NoAuto: true, Names: []string{"Laser Invasion"}},
	{System: "NES", Names: []string{"Chiller"}},
	{System: "NES", NoAuto: true, Names: []string{"The Adventures of Bayou Billy"}},
	{System: "NES", Names: []string{"Barker Bill's Trick Shooting"}},
	{System: "NES", Names: []string{"Space Shadow"}},
	{System: "NES", NoAuto: true, Names: []string{"The Lone Ranger"}},

	// SNES Super Scope, and the Justifier.
	{System: "SNES", Names: []string{"Super Scope 6"}},
	{System: "SNES", Names: []string{"Battle Clash", "Space Bazooka"}},
	{System: "SNES", Names: []string{"Metal Combat: Falcon's Revenge"}},
	{System: "SNES", Names: []string{"T2: The Arcade Game", "Terminator 2: The Arcade Game"}},
	{System: "SNES", Names: []string{"Yoshi's Safari", "Yoshi no Road Hunting"}},
	{System: "SNES", Names: []string{"X-Zone"}},
	{System: "SNES", Names: []string{"Operation Thunderbolt"}},
	{System: "SNES", Names: []string{"Tin Star"}},
	{System: "SNES", Names: []string{"Bazooka Blitzkrieg"}},
	{System: "SNES", Kind: "justifier", Names: []string{"Lethal Enforcers"}},

	// Mega Drive Menacer, and the Justifier.
	{System: "Mega Drive", Names: []string{"Menacer 6-Game Cartridge", "Menacer 6 in 1"}},
	{System: "Mega Drive", Names: []string{"T2: The Arcade Game", "Terminator 2: The Arcade Game"}},
	{System: "Mega Drive", Names: []string{"Body Count"}},
	{System: "Mega Drive", Kind: "justifier", Names: []string{"Lethal Enforcers"}},
	{System: "Mega Drive", Kind: "justifier", Names: []string{"Lethal Enforcers II: Gun Fighters"}},

	// Master System Light Phaser.
	{System: "Master System", Names: []string{"Safari Hunt"}},
	{System: "Master System", Names: []string{"Marksman Shooting & Trap Shooting", "Marksman Shooting"}},
	{System: "Master System", Names: []string{"Shooting Gallery"}},
	{System: "Master System", Names: []string{"Gangster Town"}},
	{System: "Master System", Names: []string{"Rescue Mission"}},
	{System: "Master System", Names: []string{"Missile Defense 3-D"}},
	{System: "Master System", Names: []string{"Wanted"}},
	{System: "Master System", Names: []string{"Laser Ghost"}},
	{System: "Master System", Names: []string{"Operation Wolf"}},
	{System: "Master System", Names: []string{"Space Gun"}},
	{System: "Master System", Names: []string{"Rambo III"}},
	{System: "Master System", Tag: "light phaser", Names: []string{"Assault City"}},

	// Sega CD: American Laser Games and Konami (Justifier-compatible).
	{System: "Sega CD", Kind: "justifier", Names: []string{"Mad Dog McCree"}},
	{System: "Sega CD", Kind: "justifier", Names: []string{"Mad Dog II: The Lost Gold"}},
	{System: "Sega CD", Kind: "justifier", Names: []string{"Who Shot Johnny Rock?"}},
	{System: "Sega CD", Kind: "justifier", Names: []string{"Crime Patrol"}},
	{System: "Sega CD", Kind: "justifier", Names: []string{"Drug Wars"}},
	{System: "Sega CD", Kind: "justifier", Names: []string{"Space Pirates"}},
	{System: "Sega CD", Kind: "justifier", Names: []string{"Corpse Killer"}},
	{System: "Sega CD", Kind: "justifier", Names: []string{"Lethal Enforcers"}},
	{System: "Sega CD", Kind: "justifier", Names: []string{"Lethal Enforcers II: Gun Fighters"}},
	{System: "Sega CD", Kind: "justifier", NoAuto: true, Names: []string{"Snatcher"}},

	// PlayStation GunCon, and the Konami Justifier ("Hyper Blaster").
	{System: "PlayStation", Names: []string{"Time Crisis"}},
	{System: "PlayStation", Names: []string{"Time Crisis: Project Titan"}},
	{System: "PlayStation", Names: []string{"Point Blank", "Gunbullet"}},
	{System: "PlayStation", Names: []string{"Point Blank 2", "Gunbarl"}},
	{System: "PlayStation", Names: []string{"Point Blank 3"}},
	{System: "PlayStation", Names: []string{"Ghoul Panic", "Oh! Bakyuuun"}},
	{System: "PlayStation", Names: []string{"Rescue Shot"}},
	{System: "PlayStation", Names: []string{"Elemental Gearbolt"}},
	{System: "PlayStation", Names: []string{"Gunfighter: The Legend of Jesse James"}},
	{System: "PlayStation", NoAuto: true, Names: []string{"Die Hard Trilogy 2: Viva Las Vegas", "Die Hard Trilogy 2"}},
	{System: "PlayStation", Names: []string{"Extreme Ghostbusters: The Ultimate Invasion", "Extreme Ghostbusters: Ultimate Invasion"}},
	{System: "PlayStation", NoAuto: true, Names: []string{"Resident Evil Survivor", "Biohazard Gun Survivor"}},
	{System: "PlayStation", Names: []string{"Mighty Hits Special"}},
	{System: "PlayStation", Names: []string{"The Gun Shooting", "Simple 1500 Series Vol. 24: The Gun Shooting"}},
	{System: "PlayStation", Names: []string{"Judge Dredd"}},
	{System: "PlayStation", Kind: "justifier", Names: []string{"Lethal Enforcers I & II", "Lethal Enforcers"}},
	{System: "PlayStation", Kind: "justifier", NoAuto: true, Names: []string{"Die Hard Trilogy"}},
	{System: "PlayStation", Kind: "justifier", Names: []string{"Project: Horned Owl"}},
	{System: "PlayStation", Kind: "justifier", Names: []string{"Area 51"}},
	{System: "PlayStation", Kind: "justifier", Names: []string{"Maximum Force"}},
	{System: "PlayStation", Kind: "justifier", Names: []string{"Crypt Killer"}},

	// Arcade gun games FinalBurn Neo or MAME 2003-Plus run.
	{System: "Arcade", Names: []string{"Operation Wolf"}, Sets: []string{"opwolf", "opwolfa", "opwolfj", "opwolfu", "opwolfb", "opwolfp"}},
	{System: "Arcade", Names: []string{"Operation Wolf 3"}, Sets: []string{"opwolf3", "opwolf3u"}},
	{System: "Arcade", Names: []string{"Operation Thunderbolt"}, Sets: []string{"othunder", "othunderu", "othunderj", "othunderuo"}},
	{System: "Arcade", Names: []string{"Line of Fire"}, Sets: []string{"loffire", "loffireu", "loffirej"}},
	{System: "Arcade", Names: []string{"Space Gun"}, Sets: []string{"spacegun", "spacegunj", "spacegunu"}},
	{System: "Arcade", Names: []string{"Beast Busters"}, Sets: []string{"bbusters", "bbustersu", "bbustersj"}},
	{System: "Arcade", Names: []string{"Mechanized Attack"}, Sets: []string{"mechatt", "mechattu", "mechattj"}},
	{System: "Arcade", Names: []string{"Laser Ghost"}, Sets: []string{"lghost", "lghostu", "lghostj"}},
	{System: "Arcade", Names: []string{"Lethal Enforcers"}, Sets: []string{"lethalen", "lethalenj", "lethalenua", "lethalenux"}},
	{System: "Arcade", Names: []string{"Area 51"}, Sets: []string{"area51", "area51a", "area51t"}},
	{System: "Arcade", Names: []string{"Area 51 / Maximum Force Duo", "Maximum Force"}, Sets: []string{"maxforce", "maxf_102", "area51mx", "a51mxr3k"}},
	{System: "Arcade", Names: []string{"Terminator 2: Judgment Day"}, Sets: []string{"term2", "term2la1", "term2la2", "term2la3"}},
	{System: "Arcade", Names: []string{"Crypt Killer"}, Sets: []string{"cryptklr"}},
	{System: "Arcade", Names: []string{"Lord of Gun"}, Sets: []string{"lordgun"}},
	{System: "Arcade", Names: []string{"Zero Point"}, Sets: []string{"zeropnt", "zeropnta"}},
	{System: "Arcade", Names: []string{"Zero Point 2"}, Sets: []string{"zeropnt2"}},
	{System: "Arcade", Names: []string{"Bang!", "Gun Gabacho"}, Sets: []string{"bang", "bangj"}},
	{System: "Arcade", Names: []string{"Steel Gunner"}, Sets: []string{"sgunner", "sgunnerj"}},
	{System: "Arcade", Names: []string{"Steel Gunner 2"}, Sets: []string{"sgunner2", "sgunner2j"}},
	{System: "Arcade", Names: []string{"Golly! Ghost!"}, Sets: []string{"golly"}},
	{System: "Arcade", Names: []string{"Tickee Tickats"}, Sets: []string{"tickee"}},
	{System: "Arcade", Names: []string{"Gunbuster"}, Sets: []string{"gunbustr", "gunbustru", "gunbustrj"}},
	{System: "Arcade", Names: []string{"Under Fire"}, Sets: []string{"undrfire", "undrfireu", "undrfirej"}},
	{System: "Arcade", Names: []string{"Dragon Gun"}, Sets: []string{"dragngun", "dragngunj"}},
	{System: "Arcade", Names: []string{"Point Blank", "Gun Bullet"}, Sets: []string{"ptblank", "gunbulet"}},
	{System: "Arcade", Names: []string{"Who Dunit"}, Sets: []string{"whodunit"}},
	{System: "Arcade", Names: []string{"Turkey Hunting USA"}, Sets: []string{"turkhunt"}},
	{System: "Arcade", Names: []string{"Egg Venture"}, Sets: []string{"eggventr", "eggvent7", "eggventdx"}},
	{System: "Arcade", Names: []string{"Revolution X"}, Sets: []string{"revx", "revxp5"}},
	{System: "Arcade", Names: []string{"Born to Fight"}, Sets: []string{"borntofi"}},
}

// lightgunNormTitle is a title as the list compares it: lower-case words of
// letters and digits, the bracketed tags gone, "The" at either end dropped,
// "&" as "and", II–IV as digits. Pure.
func lightgunNormTitle(s string) []string {
	var b strings.Builder
	depth := 0
	for _, r := range strings.ToLower(s) {
		switch {
		case r == '(' || r == '[':
			depth++
		case r == ')' || r == ']':
			if depth > 0 {
				depth--
			}
		case depth > 0:
		case r == '\'' || r == '’':
			// "Hogan's" is "hogans"
		case r == '&':
			b.WriteString(" and ")
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	words := strings.Fields(b.String())
	for i, w := range words {
		switch w {
		case "ii":
			words[i] = "2"
		case "iii":
			words[i] = "3"
		case "iv":
			words[i] = "4"
		}
	}
	if len(words) > 1 && words[0] == "the" {
		words = words[1:]
	}
	if len(words) > 1 && words[len(words)-1] == "the" {
		words = words[:len(words)-1]
	}
	return words
}

// lightgunWordsIn reports whether name's words appear in title's, in a row.
func lightgunWordsIn(title, name []string) bool {
	if len(name) == 0 || len(name) > len(title) {
		return false
	}
	for i := 0; i+len(name) <= len(title); i++ {
		same := true
		for j := range name {
			if title[i+j] != name[j] {
				same = false
				break
			}
		}
		if same {
			return true
		}
	}
	return false
}

// lightgunNames are the list's names as lightgunNormTitle reads them, by
// title: worked out once, because the settings screen asks every frame.
var lightgunNames struct {
	once  sync.Once
	words [][][]string
}

func lightgunNameWords() [][][]string {
	lightgunNames.once.Do(func() {
		lightgunNames.words = make([][][]string, len(lightgunTitles))
		for i, t := range lightgunTitles {
			for _, n := range t.Names {
				lightgunNames.words[i] = append(lightgunNames.words[i], lightgunNormTitle(n))
			}
		}
	})
	return lightgunNames.words
}

// lightgunMatchTitle finds the list's game for a title (and, for an arcade
// game, its file's set name) on system: the longest name that matches. whole
// is a name that is the whole title, or a set name. Pure.
func lightgunMatchTitle(system, title, file string) (lightgunTitle, bool, bool) {
	words := lightgunNormTitle(title)
	set := strings.ToLower(strings.TrimSuffix(filepath.Base(file), filepath.Ext(file)))
	raw := strings.ToLower(title + " " + filepath.Base(file))
	names := lightgunNameWords()
	best, bestLen, whole := lightgunTitle{}, 0, false
	for i, t := range lightgunTitles {
		if !lightgunSameSystem(t.System, system) || (t.Tag != "" && !strings.Contains(raw, t.Tag)) {
			continue
		}
		for _, s := range t.Sets {
			if set != "" && s == set {
				return t, true, true
			}
		}
		for _, name := range names[i] {
			hit := false
			switch {
			case len(name) == 0:
			case len(name) == 1:
				hit = len(words) == 1 && words[0] == name[0]
			default:
				hit = lightgunWordsIn(words, name)
			}
			if hit && len(name) > bestLen {
				best, bestLen, whole = t, len(name), len(name) == len(words)
			}
		}
	}
	return best, whole, bestLen > 0
}

// lightgunGameMatch finds a library game in the list.
func lightgunGameMatch(game libraryGame) (lightgunTitle, bool, bool) {
	title := game.Title
	if strings.TrimSpace(title) == "" {
		title = strings.TrimSuffix(filepath.Base(game.Path), filepath.Ext(game.Path))
	}
	if t, whole, ok := lightgunMatchTitle(game.System, title, game.Path); ok {
		return t, whole, true
	}
	if dt := strings.TrimSpace(game.DisplayTitle); dt != "" && dt != title {
		return lightgunMatchTitle(game.System, dt, "")
	}
	return lightgunTitle{}, false, false
}

// lightgunKnownGame finds a library game in the list, a match inside a longer
// title included: which gun it wants, and "made for a light gun".
func lightgunKnownGame(game libraryGame) (lightgunTitle, bool) {
	t, _, ok := lightgunGameMatch(game)
	return t, ok
}

// lightgunAutoGame is a known game the gun may be given by itself: its whole
// title is in the list (no compilation cart) and it does not need the pad.
func lightgunAutoGame(game libraryGame) (lightgunTitle, bool) {
	t, whole, ok := lightgunGameMatch(game)
	return t, ok && whole && !t.NoAuto
}
