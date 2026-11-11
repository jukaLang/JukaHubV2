package main

// Kept from the subtitle tests when the suite was cut to its core; the
// isolation TestMain needs is in testhelpers_test.go.

import "testing"

// The subtitle language from the config becomes a yt-dlp argument: only a
// literal language code passes, never an option, a pattern or "all".
func TestSubtitleLanguageAcceptsOnlyLiteralCodes(t *testing.T) {
	for in, want := range map[string]string{"": "en", "pt-BR": "pt-BR", "  de ": "de", "zh-Hans": "zh-Hans"} {
		got, err := subtitleLanguage(&Config{Variables: Variables{SubtitleLanguage: in}})
		if err != nil || got != want {
			t.Errorf("subtitleLanguage(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"-en", "en.*", "1en", "en,de", "e n", "all", "ALL"} {
		if _, err := subtitleLanguage(&Config{Variables: Variables{SubtitleLanguage: bad}}); err == nil {
			t.Errorf("subtitleLanguage(%q) accepted a selector", bad)
		}
	}
}
