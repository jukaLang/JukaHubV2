package main

// artwork_closest.go — the last step of cover matching: the closest title.
//
// artworkIndex.match only accepts a cover whose title is the ROM's title once
// tags, articles and spacing are set aside. A ROM named "Zelda Minish Cap",
// "Pokemon Saphire" or "SuperMarioWorld2" passed none of those steps and got
// no picture at all. Some picture beats none, so when every exact step fails
// the cover whose title shares the most words with the ROM's is taken, as
// long as it shares enough of them that it is plausibly the same game (or at
// least the same series).

import (
	"strings"
)

// artworkStopWords count little toward a match: two titles sharing only "of"
// are not the same game.
var artworkStopWords = map[string]bool{
	"of": true, "in": true, "on": true, "to": true, "for": true, "vs": true,
	"de": true, "la": true, "le": true, "no": true, "at": true, "with": true,
}

const (
	// artworkClosestMinScore and artworkClosestMinCover are how alike the best
	// title must be: its overall score, and the share of the ROM's own words
	// it contains.
	artworkClosestMinScore = 0.45
	artworkClosestMinCover = 0.5
	// artworkClosestMinLoose is the shortest run of letters a joined-up name
	// ("SuperMarioWorld2") may be matched on by its start alone.
	artworkClosestMinLoose = 6
)

// artworkClosestWords are a title's comparable words without the loose drop
// words ("version", "edition", "a").
func artworkClosestWords(tokens []string) []string {
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if !artworkLooseDropWords[t] {
			out = append(out, t)
		}
	}
	return out
}

func artworkWordWeight(w string) float64 {
	if artworkStopWords[w] {
		return 0.3
	}
	return 1
}

func artworkIsNumber(w string) bool {
	if w == "" {
		return false
	}
	for _, r := range w {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// artworkWordsAlike reports how well two different words match: 0.6 when one
// starts the other and is at most four letters longer ("bros" / "brothers"), 0.8 for a one-letter slip in a long
// word ("saphire" / "sapphire"), else 0. Numbers must be equal.
func artworkWordsAlike(a, b string) float64 {
	if artworkIsNumber(a) || artworkIsNumber(b) {
		return 0
	}
	short, long := a, b
	if len(short) > len(long) {
		short, long = long, short
	}
	// Only a few letters longer: "super" does not start "supermarioworld2"
	// in this sense (closestJoined handles joined-up names).
	if len(short) >= 4 && len(long)-len(short) <= 4 && strings.HasPrefix(long, short) {
		return 0.6
	}
	if len(short) >= 5 && len(long)-len(short) <= 1 && artworkOneEdit(a, b) {
		return 0.8
	}
	return 0
}

// artworkOneEdit reports whether a becomes b by one inserted, deleted or
// changed byte.
func artworkOneEdit(a, b string) bool {
	if a == b {
		return true
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	if len(b)-len(a) > 1 {
		return false
	}
	i := 0
	for i < len(a) && a[i] == b[i] {
		i++
	}
	if len(a) == len(b) {
		return a[i+1:] == b[i+1:]
	}
	return a[i:] == b[i+1:]
}

// artworkClosestScore rates how alike a ROM's words and a cover title's words
// are, 0..~1, and the share of the ROM's weight the title contains.
func artworkClosestScore(rom, cand []string) (score, cover float64) {
	var romW, candW float64
	candSet := make(map[string]bool, len(cand))
	for _, w := range cand {
		if !candSet[w] {
			candSet[w] = true
			candW += artworkWordWeight(w)
		}
	}
	seen := make(map[string]bool, len(rom))
	var matched float64
	romNums, candNums := map[string]bool{}, map[string]bool{}
	for _, w := range cand {
		if artworkIsNumber(w) {
			candNums[w] = true
		}
	}
	for _, w := range rom {
		if seen[w] {
			continue
		}
		seen[w] = true
		weight := artworkWordWeight(w)
		romW += weight
		if artworkIsNumber(w) {
			romNums[w] = true
		}
		if candSet[w] {
			matched += weight
			continue
		}
		best := 0.0
		for c := range candSet {
			if a := artworkWordsAlike(w, c); a > best {
				best = a
			}
		}
		matched += weight * best
	}
	if romW <= 0 || candW <= 0 {
		return 0, 0
	}
	cover = matched / romW
	score = 0.65*cover + 0.35*min(1, matched/candW)
	if len(rom) > 0 && len(cand) > 0 && rom[0] == cand[0] {
		score += 0.08
	}
	// Numbers tell sequels apart: another number is a different game (still
	// the right series), a missing one is the first game or a spin-off.
	switch {
	case len(romNums) > 0 && len(candNums) > 0:
		shared := false
		for n := range romNums {
			if candNums[n] {
				shared = true
				break
			}
		}
		if !shared {
			score -= 0.3
		}
	case len(romNums) > 0:
		score -= 0.1
	case len(candNums) > 0:
		score -= 0.05
	}
	return score, cover
}

// closestSetup builds the word → titles lookup on first use; most consoles'
// ROMs all match exactly and never need it.
func (idx *artworkIndex) closestSetup() {
	idx.closeOnce.Do(func() {
		idx.byWord = make(map[string][]int)
		for i, key := range idx.keys {
			if key == "" {
				continue
			}
			seen := map[string]bool{}
			for _, w := range strings.Fields(key) {
				if artworkStopWords[w] || artworkLooseDropWords[w] || seen[w] {
					continue
				}
				seen[w] = true
				idx.byWord[w] = append(idx.byWord[w], i)
			}
		}
	})
}

// closest is the cover most like stem when match found none, "" and false when
// no title is alike enough. Ties go to the release that suits the ROM's tags
// (artworkCandidateScore), then the shorter name.
func (idx *artworkIndex) closest(stem string) (string, bool) {
	stem = strings.TrimSpace(stem)
	if idx == nil || len(idx.names) == 0 || stem == "" {
		return "", false
	}
	romTokens := artworkMatchTokens(stem)
	if alias, ok := artworkAliasFor(stem); ok {
		romTokens = artworkMatchTokens(alias)
	}
	rom := artworkClosestWords(romTokens)
	if len(rom) == 0 {
		return "", false
	}
	idx.closestSetup()
	tags := artworkTags(stem)

	// Candidates share at least one real word with the ROM, exactly or
	// nearly (a prefix or a one-letter slip), so a whole console's list is
	// scored only when a word is unknown.
	cands := map[int]bool{}
	for _, w := range rom {
		if artworkStopWords[w] {
			continue
		}
		for _, i := range idx.byWord[w] {
			cands[i] = true
		}
	}
	if len(cands) == 0 {
		for word, list := range idx.byWord {
			alike := false
			for _, w := range rom {
				if !artworkStopWords[w] && artworkWordsAlike(w, word) > 0 {
					alike = true
					break
				}
			}
			if alike {
				for _, i := range list {
					cands[i] = true
				}
			}
		}
	}

	bestI, bestScore, bestTag := -1, 0.0, 0
	for i := range cands {
		cand := artworkClosestWords(strings.Fields(idx.keys[i]))
		score, cover := artworkClosestScore(rom, cand)
		if cover < artworkClosestMinCover || score < artworkClosestMinScore {
			continue
		}
		tag := artworkCandidateScore(tags, idx.names[i])
		if bestI < 0 || score > bestScore+1e-9 ||
			(score > bestScore-1e-9 && (tag > bestTag || (tag == bestTag && artworkNameBefore(idx.names[i], idx.names[bestI])))) {
			bestI, bestScore, bestTag = i, score, tag
		}
	}
	if bestI >= 0 {
		return idx.names[bestI], true
	}
	return idx.closestJoined(rom, tags)
}

// closestJoined matches a name typed without spaces ("SuperMarioWorld2") by
// the start of the joined-up titles: the cover whose joined title and the
// ROM's share the longest start, at least artworkClosestMinLoose letters and
// most of the shorter one.
func (idx *artworkIndex) closestJoined(rom, tags []string) (string, bool) {
	joined := strings.Join(rom, "")
	if len(joined) < artworkClosestMinLoose {
		return "", false
	}
	var best []int
	bestLen := 0
	for key, list := range idx.loose {
		n := 0
		for n < len(key) && n < len(joined) && key[n] == joined[n] {
			n++
		}
		short := len(key)
		if len(joined) < short {
			short = len(joined)
		}
		// The shorter of the two must be (nearly) all shared: "supermario"
		// in "supermarioworld2", not "superm" of "supermetroid".
		if n < artworkClosestMinLoose || n*10 < short*8 {
			continue
		}
		switch {
		case n > bestLen:
			best, bestLen = append([]int(nil), list...), n
		case n == bestLen:
			best = append(best, list...)
		}
	}
	if len(best) == 0 {
		return "", false
	}
	return idx.best(best, tags), true
}
