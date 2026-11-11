package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/veandco/go-sdl2/ttf"
)

// legibleTestFace writes the bundled Inter's bytes under a candidate's file
// name, so the file both resolves in the chain and opens in SDL_ttf.
func legibleTestFace(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(homeUIFontFile)
	if err != nil {
		t.Fatalf("read bundled Inter: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// legibleTestInitFonts brings SDL_ttf up for the font path these tests open.
func legibleTestInitFonts(t *testing.T) {
	t.Helper()
	if err := ttf.Init(); err != nil {
		t.Fatalf("initialize SDL_ttf: %v", err)
	}
}

// legibleTestSetup points the chain at dir and restores the global state.
func legibleTestSetup(t *testing.T, dir string) {
	t.Helper()
	oldA11y, oldDirs := a11y, legibleFontDirs
	t.Cleanup(func() {
		a11y, legibleFontDirs = oldA11y, oldDirs
		legibleFontReset()
		a11yRefreshFonts()
	})
	resetA11yForTest()
	legibleFontDirs = func() []string { return []string{dir} }
	legibleFontReset()
}

func TestLegibleFontChainIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	legibleTestSetup(t, dir)
	a11y.legibleFont = true

	// Nothing installed: the chain resolves to nothing, which the font path
	// reads as "keep the bundled Inter".
	if got := legibleFontFile(); got != "" {
		t.Fatalf("an empty font folder resolved %q", got)
	}
	if face := legibleFontFaceName(); face != "" {
		t.Fatalf("an empty font folder named the face %q", face)
	}

	// Verdana outranks Tahoma and Segoe UI by the chain's own order.
	legibleTestFace(t, dir, "tahoma.ttf")
	legibleTestFace(t, dir, "segoeui.ttf")
	verdana := legibleTestFace(t, dir, "verdana.ttf")
	legibleFontReset()
	if got := legibleFontFile(); got != verdana {
		t.Fatalf("chain picked %q, want Verdana %q", got, verdana)
	}
	if face := legibleFontFaceName(); face != "Verdana" {
		t.Fatalf("chain named %q", face)
	}

	// Atkinson Hyperlegible, when installed, heads the chain.
	atk := legibleTestFace(t, dir, "AtkinsonHyperlegible-Regular.ttf")
	legibleFontReset()
	if got := legibleFontFile(); got != atk {
		t.Fatalf("chain picked %q, want Atkinson %q", got, atk)
	}
	if face := legibleFontFaceName(); face != "Atkinson Hyperlegible" {
		t.Fatalf("chain named %q", face)
	}

	// Bold text with no bold cut keeps the regular legible face...
	a11y.boldText = true
	legibleFontReset()
	if got := legibleFontFile(); got != atk {
		t.Fatalf("bold text dropped the legible face for %q", got)
	}
	// ...and uses the bold cut when the face ships one.
	bold := legibleTestFace(t, dir, "AtkinsonHyperlegible-Bold.ttf")
	legibleFontReset()
	if got := legibleFontFile(); got != bold {
		t.Fatalf("bold text picked %q, want the bold cut %q", got, bold)
	}
}

func TestLegibleFontFallsBackToBundledInter(t *testing.T) {
	legibleTestInitFonts(t)
	legibleTestSetup(t, t.TempDir())
	a11y.legibleFont = true

	if got := homeUIFontForRole(homeUIFontFile); got != homeUIFontFile {
		t.Fatalf("no installed face still resolved %q", got)
	}
	a11yRefreshFonts()
	cfg := &Config{}
	if font, size := getCachedFont(cfg, "body"); font == nil || size <= 0 {
		t.Fatal("getCachedFont returned no face, so the UI would draw no text")
	}
	if font := getFontSized(cfg, "body", 18); font == nil {
		t.Fatal("getFontSized returned no face with the setting on")
	}
	if font := loadCachedFont(homeUIFontFile, 20); font == nil {
		t.Fatal("loadCachedFont returned no face with the setting on")
	}
}

func TestLegibleFontDrivesTheRealFontPath(t *testing.T) {
	legibleTestInitFonts(t)
	dir := t.TempDir()
	legibleTestSetup(t, dir)
	verdana := legibleTestFace(t, dir, "verdana.ttf")
	a11y.legibleFont = true
	legibleFontReset()

	// The same substitution every role uses at draw time.
	if got := homeUIFontForRole(homeUIFontFile); got != verdana {
		t.Fatalf("the UI face is %q, want %q", got, verdana)
	}
	a11yRefreshFonts()
	cfg := &Config{}
	font, size := getCachedFont(cfg, "body")
	if font == nil || size <= 0 {
		t.Fatal("the resolved face does not open through getCachedFont")
	}
	if font := getFontSized(cfg, "body", 18); font == nil {
		t.Fatal("the resolved face does not open through getFontSized")
	}

	// Bold text on a face without a bold cut still draws that face.
	a11y.boldText = true
	a11yRefreshFonts()
	if got := homeUIFontForRole(homeUIFontFile); got != verdana {
		t.Fatalf("bold text left the legible face for %q", got)
	}
	a11y.boldText = false

	// Turning the setting off returns every role to the bundled face without a
	// restart; the caches are dropped, so the next frame opens Inter.
	if !a11ySetLegibleFont(false) {
		t.Fatal("the setting did not turn off")
	}
	if got := homeUIFontForRole(homeUIFontFile); got != homeUIFontFile {
		t.Fatalf("after turning off the UI face is %q", got)
	}
	if font, _ := getCachedFont(cfg, "body"); font == nil {
		t.Fatal("the bundled face did not open after turning the setting off")
	}
}

func TestLegibleFontSettingPersistsResetsAndHasARow(t *testing.T) {
	legibleTestSetup(t, t.TempDir())
	cfg := &Config{}

	if !a11ySetLegibleFont(true) || !a11y.legibleFont {
		t.Fatal("the setting did not turn on")
	}
	a11yStore(cfg)
	a11y.legibleFont = false
	a11yLoad(cfg)
	if !a11y.legibleFont {
		t.Fatal("the stored value did not load")
	}

	// A value that is not a boolean falls back to off, never to a half state.
	cfg.Variables.Custom[a11yKeyLegibleFont] = "bogus"
	a11yLoad(cfg)
	if a11y.legibleFont {
		t.Fatal("an invalid stored value turned the face on")
	}
	a11yResetDefaults(cfg)
	if a11y.legibleFont {
		t.Fatal("reset left the legible face on")
	}

	rows := a11ySettingsSectionRows(t, cfg)
	for _, row := range rows {
		if row.ID != "legibleFont" {
			continue
		}
		if row.Kind != setKindToggle {
			t.Fatalf("legible font row kind = %v", row.Kind)
		}
		row.Set(cfg, true)
		if !a11y.legibleFont || !row.Get(cfg) {
			t.Fatal("the Settings row did not turn the face on")
		}
		row.Set(cfg, false)
		if a11y.legibleFont || row.Get(cfg) {
			t.Fatal("the Settings row did not turn the face off")
		}
		return
	}
	t.Fatal("Settings -> Accessibility has no legibleFont row")
}
