package main

// Tests for character substitution and rune-safe truncation. The goal is that
// no text reaches SDL_ttf as invalid UTF-8 and no unsupported character is
// drawn as a .notdef box.

import (
	"testing"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

func testInterFont(t *testing.T, size int) *ttf.Font {
	t.Helper()
	if err := ttf.Init(); err != nil {
		t.Skipf("SDL_ttf unavailable: %v", err)
	}
	f, err := ttf.OpenFont(resolvePath("Inter-Regular.ttf"), size)
	if err != nil {
		t.Fatalf("the repository ships Inter-Regular.ttf but it would not open: %v", err)
	}
	t.Cleanup(f.Close)
	registerFontPath(f, "Inter-Regular.ttf")
	return f
}

// The coverage check is parsed from the font's own cmap, so it must agree with
// what the font actually contains.
func TestInterCoverage(t *testing.T) {
	covered := []rune{'A', 'z', '0', 'П', 'я', 'Ω', 'é'}
	for _, r := range covered {
		if !fontCoversRune("Inter-Regular.ttf", r) {
			t.Errorf("Inter should cover %q (U+%04X)", r, r)
		}
	}
	missing := []rune{'日', 'こ', '한', '你', 'ส', 'ש', 'م', 'न', '\U0001F525'}
	for _, r := range missing {
		if fontCoversRune("Inter-Regular.ttf", r) {
			t.Errorf("Inter should not cover %q (U+%04X)", r, r)
		}
	}
}

// Supported scripts must pass through untouched: substitution must never alter
// ordinary Cyrillic or Greek UI text.
func TestSanitizeKeepsSupportedText(t *testing.T) {
	font := testInterFont(t, 20)
	cases := []string{
		"Привет, мир!",
		"ΩΔΦ — λόγος",
		"Café déjà vu",
		"Русский 2024",
		"",
	}
	for _, in := range cases {
		if got := sanitizeForFont(font, in); got != in {
			t.Errorf("sanitizeForFont(%q) = %q, want unchanged", in, got)
		}
	}
}

// Closest-character substitution: full-width forms, typographic punctuation and
// CJK brackets become their ASCII lookalikes.
func TestSanitizeLookalikes(t *testing.T) {
	font := testInterFont(t, 20)
	cases := []struct{ in, want string }{
		{"ＡＢＣ１２３", "ABC123"},
		{"「Hello」", "\"Hello\""},
		{"【MV】", "[MV]"},
		{"★ song ♪", "★ song ~"}, // ★ and quotes are in Inter already
	}
	// Typographic punctuation Inter already covers must stay untouched.
	for _, keep := range []string{"“Hi” — ok…", "Ⅰ Ⅱ Ⅲ", "→ ←"} {
		if got := sanitizeForFont(font, keep); got != keep {
			t.Errorf("sanitizeForFont(%q) = %q, want unchanged", keep, got)
		}
	}
	for _, tc := range cases {
		if got := sanitizeForFont(font, tc.in); got != tc.want {
			t.Errorf("sanitizeForFont(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Characters with no lookalike collapse into one ellipsis per run, so a title
// reads as "something was omitted" instead of a row of boxes.
func TestSanitizeOmittedRuns(t *testing.T) {
	font := testInterFont(t, 20)
	cases := []struct{ in, want string }{
		{"こんにちは", "…"},
		{"【MV】残酷な天使のテーゼ", "[MV]…"},
		{"Song 残酷 2024", "Song … 2024"},
		{"残酷な天使 Song", "… Song"},
		{"\U0001F680\U0001F525", "…"},
		{"Song \U0001F680 2024", "Song … 2024"},
		// Inter has U+2764, so the supported heart survives between omissions.
		{"\U0001F525\u2764\U0001F525", "… ❤ …"},
	}
	for _, tc := range cases {
		if got := sanitizeForFont(font, tc.in); got != tc.want {
			t.Errorf("sanitizeForFont(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Byte slicing used to cut multi-byte characters in half. Rune-safe truncation
// must always produce valid UTF-8.
func TestTruncateRunesKeepsValidUTF8(t *testing.T) {
	texts := []string{
		"Привет, мир! Это длинный заголовок",
		"こんにちは世界これはテストです",
		"日本語 mixed with Latin text",
		"\U0001F525 emoji fire",
	}
	for _, text := range texts {
		for n := 0; n <= utf8.RuneCountInString(text)+2; n++ {
			got := truncateRunes(text, n)
			if !utf8.ValidString(got) {
				t.Fatalf("truncateRunes(%q, %d) produced invalid UTF-8: %q", text, n, got)
			}
		}
	}

	if got := truncateRunes("Привет", 3); got != "При…" {
		t.Errorf("truncateRunes = %q, want %q", got, "При…")
	}
	if got := truncateRunes("abc", 5); got != "abc" {
		t.Errorf("short text should not gain an ellipsis, got %q", got)
	}
}

// Fitting text to a pixel width must respect the width and stay valid UTF-8,
// including when the text has to be substituted first.
func TestFitTextToWidthValidUTF8(t *testing.T) {
	font := testInterFont(t, 20)
	texts := []string{
		"【MV】残酷な天使のテーゼ 高橋洋子",
		"Привет, это очень длинное название видео",
		"Song 残酷 2024 extended mix",
	}
	for _, text := range texts {
		got := fitTextToWidth(font, text, 140)
		if !utf8.ValidString(got) {
			t.Fatalf("fitTextToWidth(%q) produced invalid UTF-8: %q", text, got)
		}
		w, _, err := font.SizeUTF8(got)
		if err != nil {
			t.Fatalf("SizeUTF8(%q): %v", got, err)
		}
		if int32(w) > 140 {
			t.Errorf("fitTextToWidth(%q) = %q (%dpx), want <= 140px", text, got, w)
		}
	}
}

// End-to-end: the gallery's title string is drawn as the substituted text, so
// the rendered width matches the placeholder and never a tofu box.
func TestRenderTextUsesSubstitution(t *testing.T) {
	renderer := probeRenderer(t)
	font := testInterFont(t, 20)

	wCJK, hCJK := renderText(renderer, &Config{}, font, "こんにちは", sdl.Color{R: 255, G: 255, B: 255, A: 255}, 10, 10)
	wMark, hMark := renderText(renderer, &Config{}, font, "…", sdl.Color{R: 255, G: 255, B: 255, A: 255}, 10, 10)
	if wCJK != wMark || hCJK != hMark {
		t.Fatalf("CJK title rendered as %dx%d, want the placeholder size %dx%d", wCJK, hCJK, wMark, hMark)
	}
	if wCJK == 0 {
		t.Fatal("substituted title rendered nothing")
	}

	// A mixed title keeps its readable ASCII parts.
	wMixed, _ := renderText(renderer, &Config{}, font, "【MV】Song", sdl.Color{R: 255, G: 255, B: 255, A: 255}, 10, 10)
	wPlain, _ := renderText(renderer, &Config{}, font, "[MV]Song", sdl.Color{R: 255, G: 255, B: 255, A: 255}, 10, 10)
	if wMixed != wPlain {
		t.Fatalf("mixed title rendered %dpx, want %dpx ([MV]Song)", wMixed, wPlain)
	}
}
