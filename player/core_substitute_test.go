package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// SEC-20: a label can be captured in a screenshot or shared by the companion,
// so substitution must leave a protected name or an encrypted value as its
// placeholder instead of printing the stored value.
func TestSubstituteVarsProtectsSecrets(t *testing.T) {
	vars := map[string]interface{}{
		"player_name":      "Alex",
		"discord_token":    "hunter2",           // protected word
		"wifi_password":    "pa55word",          // protected word
		"apiKey":           "sk-live-123",       // protected phrase
		"ha_url":           "http://home.local", // protected name
		"toolſ_path":       "/mnt/private",      // non-ASCII folds onto tools_path
		"livebuilder_code": "246810",            // protected phrase
		"encrypted":        "ENC:3f9a",          // an encrypted value
		"battery":          87,
	}
	cases := []struct {
		text string
		want string
	}{
		{"Hello $player_name", "Hello Alex"},
		{"$discord_token", "$discord_token"},
		{"$wifi_password", "$wifi_password"},
		{"$apiKey", "$apiKey"},
		{"$ha_url", "$ha_url"},
		{"$toolſ_path", "$toolſ_path"},
		{"$livebuilder_code", "$livebuilder_code"},
		{"$encrypted", "$encrypted"},
		{"$missing", "$missing"},
		{"no variables", "no variables"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := substituteVars(tc.text, vars); got != tc.want {
			t.Errorf("substituteVars(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}

// An ordinary label is still rendered: the guard must not turn every variable
// into its placeholder.
func TestSubstituteVarsAllowsPlainLabels(t *testing.T) {
	vars := map[string]interface{}{"wifi_status": "Connected", "battery": 87}
	if got, want := substituteVars("$wifi_status ($battery%)", vars), "Connected (87%)"; got != want {
		t.Fatalf("substituteVars = %q, want %q", got, want)
	}
}

// TestUICataloguesParse checks every interface-language catalogue (i18n.go):
// it decodes, names its own language, has no empty value, keeps each key's
// placeholders, draws only characters the bundled Inter has, and never maps a
// value that is itself a key onto something else (renderText may translate a
// string fitTextCached already translated).
func TestUICataloguesParse(t *testing.T) {
	legibleTestInitFonts(t)
	font := loadCachedFont(homeUIFontFile, 20)
	if font == nil {
		t.Fatal("load the UI font")
	}
	const fitW = int32(1 << 20)
	for _, l := range uiLanguages {
		if l.Code == uiLanguageDefault {
			continue
		}
		data, err := uiCatalogFiles.ReadFile("i18n/" + l.Code + ".json")
		if err != nil {
			t.Fatalf("%s: %v", l.Code, err)
		}
		var f uiCatalogFile
		if err := json.Unmarshal(data, &f); err != nil {
			t.Fatalf("%s: %v", l.Code, err)
		}
		if f.Language != l.Code || len(f.Strings) < 300 {
			t.Fatalf("%s: language %q with %d strings", l.Code, f.Language, len(f.Strings))
		}
		uiSetLanguage(l.Code)
		if uiLanguageCode() != l.Code {
			t.Fatalf("%s: catalogue did not load", l.Code)
		}
		for k, v := range f.Strings {
			if v == "" || k == "" {
				t.Errorf("%s: empty entry %q -> %q", l.Code, k, v)
			}
			if string(uiSortedBytes(uiPlaceholders(k))) != string(uiSortedBytes(uiPlaceholders(v))) {
				t.Errorf("%s: placeholders differ: %q -> %q", l.Code, k, v)
			}
			if again, ok := f.Strings[v]; ok && again != v {
				// A value may legitimately be another key (Network -> Red,
				// Red -> Rojo). fitTextCached marks the fitted translation,
				// so the draw lookup must leave it alone.
				if got := uiT(fitTextCached(font, k, fitW)); got != v {
					t.Errorf("%s: %q -> %q, which fit/draw retranslates to %q", l.Code, k, v, got)
				}
			}
			for _, r := range v {
				if r > 0x7f && !fontCoversRune(homeUIFontFile, r) {
					t.Errorf("%s: %q uses %q, which Inter cannot draw", l.Code, k, r)
				}
			}
		}
	}

	t.Cleanup(func() { uiSetLanguage(uiLanguageDefault) })
	uiSetLanguage("es")
	if uiLanguageCode() != "es" || uiT("Settings") == "Settings" {
		t.Fatalf("Spanish not in force: %q", uiT("Settings"))
	}
	if got := uiT(fitTextCached(font, "Network", fitW)); got != "Red" {
		t.Errorf("Spanish Network = %q, want Red", got)
	}
	if got := uiT(fitTextCached(font, "Red", fitW)); got != "Rojo" {
		t.Errorf("Spanish Red = %q, want Rojo", got)
	}
	if got := uiT(fitTextCached(font, "Network: Wi-Fi", fitW)); got != "Red: Wi-Fi" {
		t.Errorf("Spanish Network: value = %q, want Red: Wi-Fi", got)
	}
	if got := uiT("Weather: On"); got == "Weather: On" {
		t.Errorf("label: value join not translated: %q", got)
	}
	if got := uiT("Score: 12"); got == "Score: 12" || !strings.Contains(got, "12") {
		t.Errorf("pattern not applied: %q", got)
	}
	if got := uiT("some game title"); got != "some game title" {
		t.Errorf("content changed: %q", got)
	}
	uiSetLanguage(uiLanguageDefault)
	if got := uiT("Settings"); got != "Settings" {
		t.Errorf("English is not the identity: %q", got)
	}
}

func TestSpanishFittedLabelsRenderTranslatedAtTheirFitWidth(t *testing.T) {
	legibleTestInitFonts(t)
	if err := sdl.Init(sdl.INIT_VIDEO); err != nil {
		t.Fatalf("initialize SDL video: %v", err)
	}
	surface, err := sdl.CreateRGBSurface(0, 320, 80, 32, 0, 0, 0, 0)
	if err != nil {
		t.Fatalf("create test surface: %v", err)
	}
	renderer, err := sdl.CreateSoftwareRenderer(surface)
	if err != nil {
		surface.Free()
		t.Fatalf("create software renderer: %v", err)
	}
	defer func() {
		renderer.Destroy()
		surface.Free()
	}()
	font := loadCachedFont(homeUIFontFile, 20)
	if font == nil {
		t.Fatal("load the UI font")
	}
	uiSetLanguage("es")
	defer uiSetLanguage(uiLanguageDefault)

	for _, tc := range []struct{ source, want string }{
		{"Network", "Red"},
		{"Red", "Rojo"},
		{"Network: Wi-Fi", "Red: Wi-Fi"},
		{"Settings", "Ajustes"},
	} {
		fitted := fitTextCached(font, tc.source, 300)
		if !strings.Contains(fitted, uiTranslatedStart) || !strings.Contains(fitted, uiTranslatedEnd) {
			t.Fatalf("fitTextCached(%q) = %q, want a translate-once marked result", tc.source, fitted)
		}
		w, h := renderText(renderer, &Config{}, font, fitted, sdl.Color{R: 255, G: 255, B: 255, A: 255}, 0, 0)
		wantW, wantH := measureTextRaw(font, tc.want)
		if w != wantW || h != wantH {
			t.Errorf("renderText(%q -> %q) = %dx%d, want %dx%d", tc.source, tc.want, w, h, wantW, wantH)
		}
		if markedW, _, _ := font.SizeUTF8(fitted); int32(markedW) != wantW {
			t.Errorf("font.SizeUTF8(fitted %q) = %d, want %d (marker must be zero width)", tc.source, markedW, wantW)
		}
	}

	// A clipped translation must stay inside the width that was fitted, even
	// though its source is shorter (Settings -> Ajustes).
	fitted := fitTextCached(font, "Settings", 24)
	w, _ := renderText(renderer, &Config{}, font, fitted, sdl.Color{R: 255, G: 255, B: 255, A: 255}, 0, 0)
	if w > 24 {
		t.Errorf("clipped Spanish Settings rendered %dpx wide, want <= 24px", w)
	}
	plain := uiUnmarkTranslated(fitted)
	wantW, _ := measureTextRaw(font, plain)
	if w != wantW {
		t.Errorf("clipped Spanish Settings rendered %dpx, fitted text measures %dpx", w, wantW)
	}
}
