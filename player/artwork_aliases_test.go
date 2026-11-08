package main

import "testing"

func artworkVariantsContain(t *testing.T, title string, want ...string) []string {
	t.Helper()
	variants := artworkTitleVariants(title)
	for _, w := range want {
		found := false
		for _, v := range variants {
			if v == w {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%q: variants %q do not include %q", title, variants, w)
		}
	}
	return variants
}

// The documented miss: a folder trimmed to "Zelda II" cannot grow the reference
// subtitle back by rewriting, so the alias table has to supply it.
func TestArtworkAliasSuppliesTheReferenceSubtitle(t *testing.T) {
	variants := artworkVariantsContain(t, "Zelda II (USA)",
		"Zelda II - The Adventure of Link",
		"Zelda_II_-_The_Adventure_of_Link",
	)
	if variants[0] != "Zelda II (USA)" {
		t.Fatalf("the exact name must stay first, got %q", variants[0])
	}
	// The alias is normalised the same way for a bare stem and a tagged one.
	artworkVariantsContain(t, "zelda 2", "Zelda II - The Adventure of Link")
}

func TestArtworkSpellingVariantsCoverTheCommonMismatches(t *testing.T) {
	cases := []struct {
		title string
		want  string
	}{
		{"Zelda II", "Zelda 2"},
		{"Final Fantasy 4", "Final Fantasy IV"},
		{"Dragon Quest VIII", "Dragon Quest 8"},
		{"Dungeons & Dragons", "Dungeons and Dragons"},
		{"Kirby and the Amazing Mirror", "Kirby & the Amazing Mirror"},
		{"Pokémon Red", "Pokemon Red"},
		{"Café Kid", "Cafe Kid"},
		{"Zelda II: The Adventure of Link", "Zelda II - The Adventure of Link"},
		{"Super Mario Bros.", "Super Mario Bros"},
	}
	for _, tc := range cases {
		artworkVariantsContain(t, tc.title, tc.want)
	}
}

func TestArtworkNormalizeTitleIgnoresTagsCaseAndPunctuation(t *testing.T) {
	cases := map[string]string{
		"Zelda II (USA)":          "zelda ii",
		"  zelda  ii  [!]  ":      "zelda ii",
		"Kirby's Dream Land":      "kirbys dream land",
		"Super Mario Bros. 3":     "super mario bros 3",
		"Pokémon: Red Version":    "pokemon red version",
		"Game - Subtitle (Rev 1)": "game subtitle",
	}
	for in, want := range cases {
		if got := artworkNormalizeTitle(in); got != want {
			t.Errorf("artworkNormalizeTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// The guesses must not turn one title into a sweep, and the exact name must
// remain the first thing asked for.
func TestArtworkSpellingVariantsStayBounded(t *testing.T) {
	long := "The Legend of Zelda: The Adventure of Link & Friends (USA) (Rev 1) [!]"
	variants := artworkTitleVariants(long)
	if len(variants) > artworkVariantLimit {
		t.Fatalf("%d variants, cap is %d: %q", len(variants), artworkVariantLimit, variants)
	}
	seen := map[string]bool{}
	for _, v := range variants {
		if seen[v] {
			t.Fatalf("duplicate variant %q in %q", v, variants)
		}
		seen[v] = true
	}
	if variants[0] != long {
		t.Fatalf("exact name not first: %q", variants[0])
	}
}
