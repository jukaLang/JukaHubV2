package main

// Tests for the keyboard's word list and suggestion engine (vkdict.go): the
// embedded list loads and is searchable, the strip completes and corrects like
// a phone keyboard, and learned words stay bounded, per language and on disk.

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// withLearnedStore points the learned-word store at a temp file and empties
// it, restoring the real store afterwards.
func withLearnedStore(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vk-words.json")
	prevPath := vkLearnedPath
	vkLearned.mu.Lock()
	prevLoaded, prevWords := vkLearned.loaded, vkLearned.words
	vkLearned.loaded, vkLearned.words = true, map[string]*vkLearnedWord{}
	vkLearned.mu.Unlock()
	prevDirty := vkLearnedDirtyAt.Load()
	vkLearnedPath = func() string { return path }
	vkLearnedDirtyAt.Store(0)
	t.Cleanup(func() {
		vkLearnedPath = prevPath
		vkLearned.mu.Lock()
		vkLearned.loaded, vkLearned.words = prevLoaded, prevWords
		vkLearned.mu.Unlock()
		vkLearnedDirtyAt.Store(prevDirty)
	})
	return path
}

func TestVKDictLoadsTheEmbeddedList(t *testing.T) {
	list := vkDictLoadSync()
	if list == nil {
		t.Fatal("the embedded word list did not load")
	}
	if len(list.keys) < 100000 || len(list.keys) != len(list.words) || len(list.keys) != len(list.rank) {
		t.Fatalf("word list has %d keys, %d words, %d ranks", len(list.keys), len(list.words), len(list.rank))
	}
	for i := 1; i < len(list.keys); i++ {
		if list.keys[i-1] > list.keys[i] {
			t.Fatalf("keys out of order at %d: %q > %q", i, list.keys[i-1], list.keys[i])
		}
	}
	the, rare := list.find("the"), list.find("aardvark")
	if the < 0 || rare < 0 {
		t.Fatalf("common words missing: the=%d aardvark=%d", the, rare)
	}
	if list.rank[the] <= list.rank[rare] {
		t.Fatalf("rank(the)=%d should beat rank(aardvark)=%d", list.rank[the], list.rank[rare])
	}
	if list.find("i") >= 0 {
		t.Fatal("a lower-case \"i\" entry competes with the pronoun")
	}
	if i := list.find("i'm"); i < 0 || list.words[i] != "I'm" {
		t.Fatalf("\"I'm\" should be stored with its capital, got index %d", i)
	}
	if !strings.Contains(vkDictNotice, "Kevin Atkinson") {
		t.Fatal("the SCOWL notice is not embedded with the list")
	}
}

func TestVKSuggestCompletesAndCorrects(t *testing.T) {
	if vkDictLoadSync() == nil {
		t.Fatal("word list unavailable")
	}
	withLearnedStore(t)
	cases := []struct {
		typed   string
		middle  string
		literal bool
	}{
		{"th", "the", true},
		{"teh", "the", true},
		{"recieve", "receive", true},
		{"definately", "definitely", true},
		{"dont", "don't", true},
		{"im", "I'm", true},
		{"Hel", "Hello", false},
		{"HEL", "HELLO", false},
		{"zelda", "Zelda", false},
	}
	for _, tc := range cases {
		row := vkSuggestFor(tc.typed, "en")
		if row[1].Text != tc.middle {
			t.Errorf("%q: best guess %q, want %q (row %+v)", tc.typed, row[1].Text, tc.middle, row)
		}
		if got := row[0].Literal; got != tc.literal {
			t.Errorf("%q: literal cell = %v, want %v (row %+v)", tc.typed, got, tc.literal, row)
		}
		if tc.literal && row[0].Text != tc.typed {
			t.Errorf("%q: literal cell shows %q", tc.typed, row[0].Text)
		}
		seen := map[string]bool{}
		for _, s := range row {
			if s.Text == "" {
				continue
			}
			if seen[strings.ToLower(s.Text)] {
				t.Errorf("%q: %q offered twice (row %+v)", tc.typed, s.Text, row)
			}
			seen[strings.ToLower(s.Text)] = true
		}
	}
	// One stem's endings must not fill the strip: "watch" hides "watched".
	row := vkSuggestFor("wat", "en")
	stems := 0
	for _, s := range row {
		if strings.HasPrefix(strings.ToLower(s.Text), "watch") {
			stems++
		}
	}
	if stems > 1 {
		t.Errorf("\"wat\" offered %d forms of one stem: %+v", stems, row)
	}
	// The English list stays out of other languages.
	if row := vkSuggestFor("th", "de"); row[1].Text != "" {
		t.Errorf("German typing got English suggestion %+v", row)
	}
}

func TestVKSuggestUsesLearnedWordsPerLanguage(t *testing.T) {
	if vkDictLoadSync() == nil {
		t.Fatal("word list unavailable")
	}
	withLearnedStore(t)

	vkLearnWord("Strasse", "de", 1)
	if row := vkSuggestFor("Stra", "de"); row[1].Text == "Strasse" {
		t.Fatal("a word typed once must not be offered yet")
	}
	vkLearnWord("Strasse", "de", 1)
	if row := vkSuggestFor("stra", "de"); row[1].Text != "strasse" && row[1].Text != "Strasse" {
		t.Fatalf("learned German word not offered: %+v", row)
	}
	if row := vkSuggestFor("Stra", "de"); row[1].Text != "Strasse" {
		t.Fatalf("learned word lost its capital: %+v", row)
	}
	// A tap on the quoted literal keeps it at once (weight 2).
	vkLearnWord("Juka", "en", 2)
	if !vkDictKnows("juka", "en") {
		t.Fatal("explicitly kept word is not known")
	}
	if vkDictKnows("juka", "fr") {
		t.Fatal("a word learned on the English layout leaked into French")
	}
	// Common English words are never stored: the list already ranks them, and
	// a sentence-initial "Hello" must not become a learned spelling.
	vkLearnWord("Hello", "en", 2)
	vkLearned.mu.Lock()
	_, stored := vkLearned.words[vkLearnedKey("en", "hello")]
	vkLearned.mu.Unlock()
	if stored {
		t.Fatal("a dictionary word was learned")
	}
	// Links, numbers and fragments are not words.
	for _, junk := range []string{"https://x.y", "1234", "a", "e-mail@example.com"} {
		if w := vkLearnableWord(junk); w != "" && junk != "a" {
			t.Errorf("vkLearnableWord(%q) = %q, want nothing", junk, w)
		}
	}
}

func TestVKLearnedWordsAreBoundedAndSaved(t *testing.T) {
	if vkDictLoadSync() == nil {
		t.Fatal("word list unavailable")
	}
	path := withLearnedStore(t)
	for i := 0; i < vkLearnedMax+25; i++ {
		vkLearnWord(fmt.Sprintf("zq%sword", strings.Repeat("x", i%7)+vkTestLetters(i)), "en", 1)
	}
	if n := vkLearnedCount(); n > vkLearnedMax {
		t.Fatalf("store holds %d words, cap is %d", n, vkLearnedMax)
	}
	vkLearnWord("Stepanovich", "en", 2)
	vkLearnedSaveNow()

	// A fresh load reads the same store back.
	vkLearned.mu.Lock()
	vkLearned.loaded, vkLearned.words = false, nil
	vkLearned.mu.Unlock()
	prevPath := vkLearnedPath
	vkLearnedPath = func() string { return path }
	t.Cleanup(func() { vkLearnedPath = prevPath })
	vkLearnedEnsureLoaded()
	if !vkDictKnows("stepanovich", "en") {
		t.Fatal("learned word did not survive a reload")
	}
	vkLearnedClear()
	if vkDictKnows("stepanovich", "en") || vkLearnedCount() != 0 {
		t.Fatal("clearing left learned words behind")
	}
	vkLearnedSaveNow()
	vkLearned.mu.Lock()
	vkLearned.loaded, vkLearned.words = false, nil
	vkLearned.mu.Unlock()
	vkLearnedEnsureLoaded()
	if vkLearnedCount() != 0 {
		t.Fatal("the cleared store came back after a reload")
	}
}

// vkTestLetters spells i in letters, so generated words stay words.
func vkTestLetters(i int) string {
	var b strings.Builder
	for {
		b.WriteByte(byte('a' + i%26))
		i /= 26
		if i == 0 {
			break
		}
	}
	return b.String()
}

func TestVKEditDistanceWithin(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		ok   bool
	}{
		{"the", "the", 0, true},
		{"teh", "the", 1, true},
		{"recieve", "receive", 1, true},
		{"helo", "hello", 1, true},
		{"seperate", "separate", 1, true},
		{"tommorow", "tomorrow", 2, true},
		{"abc", "xyz", 0, false},
		{"short", "muchlongerword", 0, false},
	}
	for _, tc := range cases {
		got, ok := vkEditDistanceWithin(tc.a, tc.b, 2)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("distance(%q,%q) = %d,%v want %d,%v", tc.a, tc.b, got, ok, tc.want, tc.ok)
		}
	}
}

func TestVKMatchCase(t *testing.T) {
	cases := []struct{ typed, word, want string }{
		{"hel", "hello", "hello"},
		{"Hel", "hello", "Hello"},
		{"HEL", "hello", "HELLO"},
		{"i", "I'm", "I'm"},
		{"par", "Paris", "Paris"},
		{"Ст", "строка", "Строка"},
	}
	for _, tc := range cases {
		if got := vkMatchCase(tc.typed, tc.word); got != tc.want {
			t.Errorf("vkMatchCase(%q,%q) = %q, want %q", tc.typed, tc.word, got, tc.want)
		}
	}
}
