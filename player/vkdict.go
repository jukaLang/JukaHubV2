package main

// vkdict.go — the on-screen keyboard's word list and suggestion engine.
//
// The strip above the keys works like a phone keyboard's: while a word is being
// typed it offers the likeliest completions and corrections, ranked by how
// common each word is, and a tap swaps the word in. Two sources feed it:
//
//   - an embedded English word list (vkdata/en_words.txt.gz: ~130k forms built
//     from the SCOWL-derived Hunspell dictionary by vkdata/vkdict_build.py,
//     each with a frequency rank). It is decompressed on the first keyboard
//     use, off the render thread, never at boot;
//   - the user's own words, learned as they are typed (bounded, kept in
//     vk-words.json in the data directory, never from a password field).
//
// Nothing here touches SDL, so the engine is testable on its own. The other
// "dictionary" (dictionary.go) is a different feature: the online definition
// lookup.

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

//go:embed vkdata/en_words.txt.gz
var vkDictGzip []byte

// vkDictNotice is the SCOWL copyright and permission notice. The licence asks
// for it to travel with every copy of the list, so it is embedded beside the
// list and shown from Keyboard settings.
//
//go:embed vkdata/SCOWL-COPYRIGHT.txt
var vkDictNotice string

// vkWordList is the decompressed list: keys sorted for binary search, the
// display form (proper nouns keep their capitals) and a 1..255 rank per word.
// It is immutable once published, so the render thread reads it without locks.
type vkWordList struct {
	keys  []string
	words []string
	rank  []uint8
	block []uint8 // best rank in each run of vkDictBlock entries
}

// vkDictBlock is the run length behind vkWordList.block. A one-letter prefix
// covers thousands of words; skipping whole runs that cannot beat the current
// third-best keeps that query to a few hundred comparisons on the handheld.
const vkDictBlock = 32

var (
	vkDictLoaded  atomic.Pointer[vkWordList]
	vkDictLoading atomic.Bool
	vkDictMu      sync.Mutex
)

// vkDict returns the list once it has loaded, nil before (and on failure). The
// strip simply stays empty until then.
func vkDict() *vkWordList { return vkDictLoaded.Load() }

// vkDictStartLoading decompresses the list on a goroutine. The keyboard calls
// it when it opens, so the first word typed usually finds it ready.
func vkDictStartLoading() {
	if vkDictLoaded.Load() != nil || !vkDictLoading.CompareAndSwap(false, true) {
		return
	}
	go func() {
		vkDictLoadSync()
		vkLearnedEnsureLoaded()
	}()
}

// vkDictLoadSync loads the list on the calling goroutine (tests, and the
// background loader above).
func vkDictLoadSync() *vkWordList {
	if list := vkDictLoaded.Load(); list != nil {
		return list
	}
	vkDictMu.Lock()
	defer vkDictMu.Unlock()
	if list := vkDictLoaded.Load(); list != nil {
		return list
	}
	list, err := vkParseWordList(vkDictGzip)
	if err != nil {
		LogScene("keyboard").Warn("word list unavailable", "err", err)
		return nil
	}
	vkDictLoaded.Store(list)
	return list
}

// vkParseWordList reads the gzip of "word<TAB>rank" lines. Words are slices of
// one decompressed string, so ~130k entries cost their headers and nothing more.
func vkParseWordList(gz []byte) (*vkWordList, error) {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		return nil, err
	}
	blob := string(raw)
	n := strings.Count(blob, "\n") + 1
	list := &vkWordList{
		keys:  make([]string, 0, n),
		words: make([]string, 0, n),
		rank:  make([]uint8, 0, n),
	}
	for len(blob) > 0 {
		line := blob
		if nl := strings.IndexByte(blob, '\n'); nl >= 0 {
			line, blob = blob[:nl], blob[nl+1:]
		} else {
			blob = ""
		}
		tab := strings.IndexByte(line, '\t')
		if tab <= 0 {
			continue
		}
		rank := 0
		for _, c := range line[tab+1:] {
			if c >= '0' && c <= '9' {
				rank = rank*10 + int(c-'0')
			}
		}
		if rank > 255 {
			rank = 255
		}
		if rank < 1 {
			rank = 1
		}
		word := line[:tab]
		list.words = append(list.words, word)
		list.keys = append(list.keys, strings.ToLower(word))
		list.rank = append(list.rank, uint8(rank))
	}
	if len(list.keys) == 0 {
		return nil, errors.New("empty word list")
	}
	// The build sorts by the lower-cased word; a hand-edited list must not be
	// able to break the binary searches, so it is re-sorted if needed.
	if !sort.StringsAreSorted(list.keys) {
		sort.Stable(vkWordListOrder{list})
	}
	list.block = make([]uint8, (len(list.rank)+vkDictBlock-1)/vkDictBlock)
	for i, r := range list.rank {
		if b := i / vkDictBlock; r > list.block[b] {
			list.block[b] = r
		}
	}
	return list, nil
}

// vkWordListOrder sorts the three parallel slices together.
type vkWordListOrder struct{ l *vkWordList }

func (o vkWordListOrder) Len() int           { return len(o.l.keys) }
func (o vkWordListOrder) Less(i, j int) bool { return o.l.keys[i] < o.l.keys[j] }
func (o vkWordListOrder) Swap(i, j int) {
	o.l.keys[i], o.l.keys[j] = o.l.keys[j], o.l.keys[i]
	o.l.words[i], o.l.words[j] = o.l.words[j], o.l.words[i]
	o.l.rank[i], o.l.rank[j] = o.l.rank[j], o.l.rank[i]
}

// find returns the index of key, or -1. Duplicate keys ("will", "Will") are
// stored best-ranked first, so the common word wins.
func (l *vkWordList) find(key string) int {
	i := sort.SearchStrings(l.keys, key)
	if i < len(l.keys) && l.keys[i] == key {
		return i
	}
	return -1
}

// findBytes is find for a scratch buffer; the comparisons convert without
// allocating, which matters for the few hundred edits tried per keystroke.
func (l *vkWordList) findBytes(b []byte) int {
	lo, hi := 0, len(l.keys)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if l.keys[mid] < string(b) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(l.keys) && l.keys[lo] == string(b) {
		return lo
	}
	return -1
}

// prefixRange returns [lo, hi) of the keys that start with prefix.
func (l *vkWordList) prefixRange(prefix string) (int, int) {
	lo := sort.SearchStrings(l.keys, prefix)
	hi := lo
	for step := 1; hi < len(l.keys) && strings.HasPrefix(l.keys[hi], prefix); step *= 2 {
		// Gallop, then bisect: most prefixes cover a handful of words.
		next := hi + step
		if next > len(l.keys) || !strings.HasPrefix(l.keys[next-1], prefix) {
			a, b := hi, next
			if b > len(l.keys) {
				b = len(l.keys)
			}
			for a < b {
				mid := int(uint(a+b) >> 1)
				if strings.HasPrefix(l.keys[mid], prefix) {
					a = mid + 1
				} else {
					b = mid
				}
			}
			return lo, a
		}
		hi = next
	}
	return lo, hi
}

// prefixRangeBytes is prefixRange for a scratch buffer.
func (l *vkWordList) prefixRangeBytes(b []byte) (int, int) {
	lo, hi := 0, len(l.keys)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if l.keys[mid] < string(b) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	end := lo
	for end < len(l.keys) && len(l.keys[end]) >= len(b) && l.keys[end][:len(b)] == string(b) {
		end++
		if end-lo > 4096 {
			break // a fuzzy prefix only needs its best word, not an exact count
		}
	}
	return lo, end
}

// topIn returns up to k indices in [lo, hi), best rank first.
func (l *vkWordList) topIn(lo, hi, k int, dst []int) []int {
	dst = dst[:0]
	for i := lo; i < hi; {
		if i%vkDictBlock == 0 && i+vkDictBlock <= hi && len(dst) == k &&
			l.block[i/vkDictBlock] <= l.rank[dst[k-1]] {
			i += vkDictBlock
			continue
		}
		r := l.rank[i]
		if len(dst) < k || r > l.rank[dst[len(dst)-1]] {
			pos := len(dst)
			for pos > 0 && l.rank[dst[pos-1]] < r {
				pos--
			}
			if len(dst) < k {
				dst = append(dst, 0)
			}
			copy(dst[pos+1:], dst[pos:len(dst)-1])
			dst[pos] = i
		}
		i++
	}
	return dst
}

// ── Suggestions ──────────────────────────────────────────────────────────

// vkSuggestion is one cell of the strip. Literal marks the word exactly as
// typed when it is not a known word: the strip shows it in quotes, and a tap
// keeps it and teaches it to the keyboard.
type vkSuggestion struct {
	Text    string
	Literal bool
}

// vkSuggestRow is the strip, left to right; the middle cell is the best guess.
type vkSuggestRow [3]vkSuggestion

// Scoring. Ranks are 1..255; these costs say how much less likely a word is
// for being a correction rather than what was typed.
const (
	vkScoreExact      = 25  // the typed word is itself a word: keep it in play
	vkExactFloor      = 120 // a whole word typed scores at least this as itself
	vkCostPerLetter   = 3   // each letter a completion adds beyond what was typed
	vkCostEdit1       = 35  // one typo away ("teh" -> "the")
	vkCostEdit1Prefix = 50  // a completion of the typed prefix with one typo
	vkCostEdit2       = 75  // two typos away
	vkBonusAdjacent   = 10  // the typo was a neighbouring key or a swap
	// vkBonusApostrophe makes a missing apostrophe almost free: "dont" and
	// "im" are how contractions get typed on a pad.
	vkBonusApostrophe = 27
	vkLearnedBase     = 170
	vkMaxWordRunes    = 32
)

type vkCandidate struct {
	word  string
	key   string
	score int
}

// vkSuggestScratch is reused across keystrokes: the engine runs on the render
// thread, once per edit, and allocating per call would be per-keystroke garbage
// on a 1 GB device.
var vkSuggestScratch struct {
	cands []vkCandidate
	top   []int
	edit  []byte
}

// vkSuggestFor ranks suggestions for the word being typed in language lang
// (the layout's ISO code). The embedded list only answers for English; the
// words the user taught the keyboard answer for their own language. The
// returned row is display-ready: case matches what was typed.
func vkSuggestFor(typed, lang string) vkSuggestRow {
	var row vkSuggestRow
	typed = strings.TrimSpace(typed)
	if typed == "" || utf8.RuneCountInString(typed) > vkMaxWordRunes {
		return row
	}
	english := lang == "en"
	key := strings.ToLower(typed)
	cands := vkSuggestScratch.cands[:0]
	add := func(word string, score int) {
		k := strings.ToLower(word)
		for i := range cands {
			if cands[i].key == k {
				if score > cands[i].score {
					cands[i].score = score
					cands[i].word = word
				}
				return
			}
		}
		cands = append(cands, vkCandidate{word: word, key: k, score: score})
	}

	known := false
	if list := vkDict(); english && list != nil && vkASCIIWord(key) {
		if i := list.find(key); i >= 0 {
			known = true
			// A word typed in full is what was meant far more often than a
			// correction of it, even when it is rare ("Zelda", "roms").
			exact := int(list.rank[i])
			if exact < vkExactFloor {
				exact = vkExactFloor
			}
			add(list.words[i], exact+vkScoreExact)
		}
		lo, hi := list.prefixRange(key)
		top := list.topIn(lo, hi, 8, vkSuggestScratch.top)
		for _, i := range top {
			if list.keys[i] != key {
				add(list.words[i], int(list.rank[i])-(len(list.keys[i])-len(key))*vkCostPerLetter)
			}
		}
		vkSuggestScratch.top = top
		if len(key) >= 2 {
			buf := vkSuggestScratch.edit
			buf = vkForEachEdit1(key, buf, func(v []byte, bonus int) {
				if i := list.findBytes(v); i >= 0 {
					add(list.words[i], int(list.rank[i])-vkCostEdit1+bonus)
				}
				if len(key) >= 4 {
					flo, fhi := list.prefixRangeBytes(v)
					best, bestRank := -1, uint8(0)
					for j := flo; j < fhi; j++ {
						if list.rank[j] > bestRank {
							best, bestRank = j, list.rank[j]
						}
					}
					if best >= 0 && list.keys[best] != string(v) {
						add(list.words[best], int(bestRank)-vkCostEdit1Prefix+bonus)
					}
				}
			})
			vkSuggestScratch.edit = buf
		}
		if !known && len(key) >= 4 && len(key) <= 12 && vkBestScore(cands) < 150 {
			lo, hi := list.prefixRange(key[:1])
			for i := lo; i < hi; i++ {
				w := list.keys[i]
				if list.rank[i] < 60 || vkAbsDiff(len(w), len(key)) > 2 {
					continue
				}
				if d, ok := vkEditDistanceWithin(key, w, 2); ok && d == 2 {
					add(list.words[i], int(list.rank[i])-vkCostEdit2)
				}
			}
		}
	}
	if vkLearnedCandidates(lang, key, add) {
		known = true
	}
	vkSuggestScratch.cands = cands

	sort.SliceStable(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
	// The middle cell is the best guess; the literal word, when it is not a
	// word yet, takes the left so a name or slang can be kept with one tap.
	// Inflections of a word already shown are skipped ("watch" hides
	// "watched" and "watches"), so the three cells are three different ideas
	// and a correction is not crowded out by one stem's endings.
	var chosen [3]string
	nChosen := 0
	pickPass := func(diverse bool) (vkSuggestion, bool) {
		for i := range cands {
			c := &cands[i]
			if c.score <= -1<<20 {
				continue // already shown
			}
			if diverse {
				dup := false
				for _, k := range chosen[:nChosen] {
					if strings.HasPrefix(c.key, k) {
						dup = true
						break
					}
				}
				if dup {
					continue
				}
			}
			text := vkMatchCase(typed, c.word)
			c.score = -1 << 21
			if text == "" {
				continue
			}
			// The typed word itself does not hide its completions: after
			// "the" the strip should still offer "they" and "there".
			if c.key != key && nChosen < len(chosen) {
				chosen[nChosen] = c.key
				nChosen++
			}
			return vkSuggestion{Text: text}, true
		}
		return vkSuggestion{}, false
	}
	pick := func() (vkSuggestion, bool) {
		if s, ok := pickPass(true); ok {
			return s, true
		}
		return pickPass(false)
	}
	if s, ok := pick(); ok {
		row[1] = s
	}
	// Until the English list has loaded every word would look unknown, so the
	// quoted cell waits for it.
	literal := !known && utf8.RuneCountInString(typed) >= 2 && !strings.EqualFold(row[1].Text, typed) &&
		(!english || vkDict() != nil)
	if literal {
		row[0] = vkSuggestion{Text: typed, Literal: true}
	} else if s, ok := pick(); ok {
		row[0] = s
	}
	if s, ok := pick(); ok {
		row[2] = s
	}
	return row
}

func vkBestScore(cands []vkCandidate) int {
	best := -1 << 30
	for _, c := range cands {
		if c.score > best {
			best = c.score
		}
	}
	return best
}

// vkASCIIWord reports whether key is a plain English token the embedded list
// could hold (it has no accented forms).
func vkASCIIWord(key string) bool {
	for i := 0; i < len(key); i++ {
		c := key[i]
		if c >= utf8.RuneSelf {
			return false
		}
		if !(c >= 'a' && c <= 'z') && c != '\'' && c != '-' {
			return false
		}
	}
	return true
}

// vkMatchCase gives a suggestion the case of what was typed: "Hel" offers
// "Hello", "HEL" offers "HELLO". A word that carries its own capitals (a name,
// "I'm") keeps them unless the whole input is upper case.
func vkMatchCase(typed, word string) string {
	if word == "" {
		return ""
	}
	letters, upper := 0, 0
	for _, r := range typed {
		if unicode.IsLetter(r) {
			letters++
			if unicode.IsUpper(r) {
				upper++
			}
		}
	}
	if letters >= 2 && upper == letters {
		return strings.ToUpper(word)
	}
	if strings.ToLower(word) != word {
		return word
	}
	first, _ := utf8.DecodeRuneInString(typed)
	if unicode.IsUpper(first) {
		r, size := utf8.DecodeRuneInString(word)
		return string(unicode.ToUpper(r)) + word[size:]
	}
	return word
}

// vkQwertyNeighbors lists each letter's neighbouring keys on a QWERTY layout,
// for the typo bonus: "tge" is much more likely "the" than "tie".
var vkQwertyNeighbors = [26]string{
	"qwsz", "vghn", "xdfv", "serfcx", "wsdr", "drtgvc", "ftyhbv", "gyujnb", "ujko",
	"huikmn", "jiolm", "kop", "njk", "bhjm", "iklp", "ol", "wa", "edft", "awedxz",
	"rfgy", "yhji", "cfgb", "qase", "zsdc", "tghu", "asx",
}

// vkForEachEdit1 calls fn with every string one edit away from key (deletion,
// transposition, substitution, insertion over a-z and the apostrophe) and the
// score bonus that kind of slip earns, in a reused buffer that fn must not
// keep. It returns the buffer for reuse.
func vkForEachEdit1(key string, buf []byte, fn func(v []byte, bonus int)) []byte {
	const alphabet = "abcdefghijklmnopqrstuvwxyz'"
	n := len(key)
	if cap(buf) < n+1 {
		buf = make([]byte, 0, n+8)
	}
	// Deletions.
	for i := 0; i < n; i++ {
		buf = append(append(buf[:0], key[:i]...), key[i+1:]...)
		fn(buf, 0)
	}
	// Transpositions.
	for i := 0; i+1 < n; i++ {
		buf = append(buf[:0], key...)
		buf[i], buf[i+1] = buf[i+1], buf[i]
		fn(buf, vkBonusAdjacent)
	}
	// Substitutions.
	for i := 0; i < n; i++ {
		orig := key[i]
		var near string
		if orig >= 'a' && orig <= 'z' {
			near = vkQwertyNeighbors[orig-'a']
		}
		for j := 0; j < len(alphabet); j++ {
			c := alphabet[j]
			if c == orig {
				continue
			}
			buf = append(buf[:0], key...)
			buf[i] = c
			bonus := 0
			if strings.IndexByte(near, c) >= 0 {
				bonus = vkBonusAdjacent
			}
			fn(buf, bonus)
		}
	}
	// Insertions.
	for i := 0; i <= n; i++ {
		for j := 0; j < len(alphabet); j++ {
			buf = append(append(append(buf[:0], key[:i]...), alphabet[j]), key[i:]...)
			bonus := 0
			if alphabet[j] == '\'' {
				bonus = vkBonusApostrophe
			}
			fn(buf, bonus)
		}
	}
	return buf
}

// vkEditDistanceWithin is the optimal-string-alignment distance between a and
// b (byte strings), or ok=false once it must exceed limit. Rows live on the
// stack: words over 30 bytes are simply too far apart to matter.
func vkEditDistanceWithin(a, b string, limit int) (int, bool) {
	const maxLen = 31
	if len(a) > maxLen || len(b) > maxLen || vkAbsDiff(len(a), len(b)) > limit {
		return 0, false
	}
	var prev2, prev, cur [maxLen + 1]int
	for j := 0; j <= len(b); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		rowMin := cur[0]
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			v := prev[j] + 1
			if c := cur[j-1] + 1; c < v {
				v = c
			}
			if c := prev[j-1] + cost; c < v {
				v = c
			}
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				if c := prev2[j-2] + 1; c < v {
					v = c
				}
			}
			cur[j] = v
			if v < rowMin {
				rowMin = v
			}
		}
		if rowMin > limit {
			return 0, false
		}
		prev2, prev = prev, cur
	}
	if d := prev[len(b)]; d <= limit {
		return d, true
	}
	return 0, false
}

// vkDictKnows reports whether word is a known word in language lang: in the
// embedded list (English only) or taught by the user.
func vkDictKnows(word, lang string) bool {
	key := strings.ToLower(strings.TrimSpace(word))
	if key == "" {
		return false
	}
	if list := vkDict(); lang == "en" && list != nil && list.find(key) >= 0 {
		return true
	}
	vkLearned.mu.Lock()
	defer vkLearned.mu.Unlock()
	_, ok := vkLearned.words[vkLearnedKey(lang, key)]
	return ok
}

// ── Learned words ────────────────────────────────────────────────────────

// vkLearnedWord is one word the user taught the keyboard, in the language of
// the layout it was typed on, so a German word is offered while typing German
// and an English one while typing English.
type vkLearnedWord struct {
	Word  string `json:"w"`
	Lang  string `json:"l,omitempty"`
	Count int    `json:"n"`
	Last  int64  `json:"t"`
}

// vkLearnedKey is the store's map key: language and the lower-cased word.
func vkLearnedKey(lang, lowerWord string) string { return lang + "|" + lowerWord }

type vkLearnedFile struct {
	Version int             `json:"version"`
	Words   []vkLearnedWord `json:"words"`
}

const (
	// vkLearnedMax bounds the store: the least-used, oldest word goes first.
	vkLearnedMax = 1000
	// vkLearnedMinCount is how often a word must be typed before it is offered.
	// One stray typo must not become a suggestion; a tap on the quoted literal
	// counts as two, so keeping a word on purpose works at once.
	vkLearnedMinCount = 2
	// vkLearnedSaveDelay batches writes: a burst of typing is one write.
	vkLearnedSaveDelay = 3 * time.Second
)

var vkLearned struct {
	mu     sync.Mutex
	loaded bool
	words  map[string]*vkLearnedWord
}

// vkLearnedDirtyAt is when the store last changed (unix nanoseconds), 0 when
// it is saved. The keyboard's frame tick reads it without taking the lock.
var vkLearnedDirtyAt atomic.Int64

// vkLearnedPath is an indirection so tests never touch the real data dir.
var vkLearnedPath = defaultVKLearnedPath

func defaultVKLearnedPath() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, "vk-words.json")
}

// vkLearnedEnsureLoaded reads the store once. A missing file is a first run.
func vkLearnedEnsureLoaded() {
	vkLearned.mu.Lock()
	defer vkLearned.mu.Unlock()
	if vkLearned.loaded {
		return
	}
	vkLearned.loaded = true
	if vkLearned.words == nil {
		vkLearned.words = map[string]*vkLearnedWord{}
	}
	path := vkLearnedPath()
	if path == "" {
		return
	}
	var file vkLearnedFile
	if _, err := readJSONWithBackup(path, &file); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			LogScene("keyboard").Warn("learned words unreadable", "err", err)
		}
		return
	}
	for _, w := range file.Words {
		word := strings.TrimSpace(w.Word)
		if word == "" || w.Count <= 0 {
			continue
		}
		if w.Lang == "" {
			w.Lang = "en"
		}
		key := vkLearnedKey(w.Lang, strings.ToLower(word))
		if _, dup := vkLearned.words[key]; dup {
			continue
		}
		entry := w
		entry.Word = word
		vkLearned.words[key] = &entry
	}
	vkLearnedTrimLocked()
}

// vkLearnableWord trims a typed token to the word it carries, or "" when it is
// not something to learn: too short or long, digits, a link, an address.
func vkLearnableWord(token string) string {
	word := strings.TrimFunc(token, func(r rune) bool {
		return !unicode.IsLetter(r) && r != '\''
	})
	n := utf8.RuneCountInString(word)
	if n < 2 || n > 24 {
		return ""
	}
	for _, r := range word {
		if !unicode.IsLetter(r) && r != '\'' && r != '-' {
			return ""
		}
	}
	return word
}

// vkLearnWord records one use of a word the user typed on a layout of language
// lang; weight 2 is an explicit keep (a tap on the quoted word). English words
// the embedded list already holds are not stored: the list ranks them, and a
// sentence-initial "Hello" must not become a learned spelling. Until the list
// has loaded nothing English is learned, for the same reason.
func vkLearnWord(token, lang string, weight int) {
	word := vkLearnableWord(token)
	if word == "" || weight <= 0 || lang == "" {
		return
	}
	lower := strings.ToLower(word)
	if lang == "en" {
		list := vkDict()
		if list == nil || list.find(lower) >= 0 {
			return
		}
	}
	key := vkLearnedKey(lang, lower)
	vkLearnedEnsureLoaded()
	vkLearned.mu.Lock()
	entry, ok := vkLearned.words[key]
	if !ok {
		entry = &vkLearnedWord{Word: word, Lang: lang}
		vkLearned.words[key] = entry
	}
	entry.Count += weight
	if entry.Count > 1000 {
		entry.Count = 1000
	}
	entry.Last = time.Now().Unix()
	// The latest spelling wins: "Jukahub" typed as "JukaHub" afterwards.
	entry.Word = word
	vkLearnedTrimLocked()
	vkLearned.mu.Unlock()
	vkLearnedDirtyAt.Store(time.Now().UnixNano())
}

// vkLearnedTrimLocked evicts the least used, then oldest, words past the cap.
func vkLearnedTrimLocked() {
	if len(vkLearned.words) <= vkLearnedMax {
		return
	}
	all := make([]*vkLearnedWord, 0, len(vkLearned.words))
	for _, w := range vkLearned.words {
		all = append(all, w)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Count != all[j].Count {
			return all[i].Count > all[j].Count
		}
		return all[i].Last > all[j].Last
	})
	for _, w := range all[vkLearnedMax:] {
		delete(vkLearned.words, vkLearnedKey(w.Lang, strings.ToLower(w.Word)))
	}
}

// vkLearnedCandidates adds the learned words of language lang that complete
// key (lower case) and reports whether key itself is a learned word.
func vkLearnedCandidates(lang, key string, add func(word string, score int)) bool {
	vkLearned.mu.Lock()
	defer vkLearned.mu.Unlock()
	known := false
	prefix := vkLearnedKey(lang, key)
	for k, w := range vkLearned.words {
		if w.Count < vkLearnedMinCount || !strings.HasPrefix(k, prefix) {
			continue
		}
		boost := w.Count * 4
		if boost > 40 {
			boost = 40
		}
		score := vkLearnedBase + boost - utf8.RuneCountInString(k[len(prefix):])
		if k == prefix {
			known = true
			score += vkScoreExact
		}
		add(w.Word, score)
	}
	return known
}

// vkLearnedCount is how many words the store holds (Keyboard settings).
func vkLearnedCount() int {
	vkLearnedEnsureLoaded()
	vkLearned.mu.Lock()
	defer vkLearned.mu.Unlock()
	return len(vkLearned.words)
}

// vkLearnedClear forgets every learned word. The empty store is written by the
// next save (the caller decides when: Keyboard settings saves at once).
func vkLearnedClear() {
	vkLearnedEnsureLoaded()
	vkLearned.mu.Lock()
	vkLearned.words = map[string]*vkLearnedWord{}
	vkLearned.mu.Unlock()
	vkLearnedDirtyAt.Store(time.Now().UnixNano())
}

// vkLearnedMaybeSave writes the store once typing has paused. It runs every
// frame the keyboard is open: one atomic load when there is nothing to do.
func vkLearnedMaybeSave(now time.Time) {
	at := vkLearnedDirtyAt.Load()
	if at == 0 || now.Sub(time.Unix(0, at)) < vkLearnedSaveDelay {
		return
	}
	vkLearnedSaveAsync()
}

// vkLearnedSaveAsync snapshots the store and writes it on a goroutine, so the
// SD card never stalls a frame.
func vkLearnedSaveAsync() {
	path, data, ok := vkLearnedTakeSnapshot()
	if !ok {
		return
	}
	go vkLearnedWrite(path, data)
}

// vkLearnedSaveNow is vkLearnedSaveAsync on the calling goroutine (tests, and
// the shutdown flush).
func vkLearnedSaveNow() {
	if path, data, ok := vkLearnedTakeSnapshot(); ok {
		vkLearnedWrite(path, data)
	}
}

// vkLearnedTakeSnapshot claims a pending change and serialises the store.
func vkLearnedTakeSnapshot() (string, []byte, bool) {
	at := vkLearnedDirtyAt.Load()
	if at == 0 || !vkLearnedDirtyAt.CompareAndSwap(at, 0) {
		return "", nil, false
	}
	path := vkLearnedPath()
	if path == "" {
		return "", nil, false
	}
	data, err := vkLearnedSnapshot()
	if err != nil {
		return "", nil, false
	}
	return path, data, true
}

// vkLearnedWriteMu keeps two saves from interleaving on the card.
var vkLearnedWriteMu sync.Mutex

func vkLearnedWrite(path string, data []byte) {
	vkLearnedWriteMu.Lock()
	defer vkLearnedWriteMu.Unlock()
	if err := AtomicWrite(path, data, 0o644); err != nil {
		LogScene("keyboard").Warn("learned words not saved", "err", err)
	}
}

func vkLearnedSnapshot() ([]byte, error) {
	vkLearned.mu.Lock()
	file := vkLearnedFile{Version: 1, Words: make([]vkLearnedWord, 0, len(vkLearned.words))}
	for _, w := range vkLearned.words {
		file.Words = append(file.Words, *w)
	}
	vkLearned.mu.Unlock()
	sort.Slice(file.Words, func(i, j int) bool {
		return strings.ToLower(file.Words[i].Word) < strings.ToLower(file.Words[j].Word)
	})
	return json.MarshalIndent(file, "", "  ")
}
