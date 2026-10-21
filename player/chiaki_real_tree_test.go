package main

// Verification against a real extracted trimui-chiaki-ng tree. The fixture
// tests in chiaki_translate_test.go prove the rewriting logic; this one proves
// the table still matches the strings the port actually ships, which is what
// catches a release that rewords a line.
//
// It edits the tree it is pointed at, so point it at a copy:
//
//	JUKAHUB_TEST_CHIAKI_TREE=/tmp/chiaki-copy go test -run TestChiakiPatchEnglishRealTree .

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChiakiPatchEnglishRealTree(t *testing.T) {
	app := os.Getenv("JUKAHUB_TEST_CHIAKI_TREE")
	if app == "" {
		t.Skip("set JUKAHUB_TEST_CHIAKI_TREE to a copy of an Apps/Chiaki tree")
	}
	// The package ships settings.json with "language": "VI"; this is the same
	// call the installer makes after copying the tree in.
	chiakiInstallLanguageDefault(app, false)

	if got := chiakiSettingsLanguage(app); got != "EN" {
		t.Fatalf("language = %q, want EN", got)
	}
	byFile := map[string][]chiakiUILiteral{}
	var order []string
	for _, lit := range chiakiUILiterals {
		if _, ok := byFile[lit.file]; !ok {
			order = append(order, lit.file)
		}
		byFile[lit.file] = append(byFile[lit.file], lit)
	}
	for _, rel := range order {
		body, err := os.ReadFile(filepath.Join(app, filepath.FromSlash(rel)))
		if err != nil {
			continue // a release without this module (PS5 code in a PS4-only build)
		}
		text := string(body)
		matched := 0
		for _, lit := range byFile[rel] {
			if strings.Contains(text, lit.vi) {
				t.Errorf("%s still contains %q", rel, lit.vi)
			}
			if strings.Contains(text, lit.en) {
				matched++
			}
		}
		// An older build may simply not have a newer message (the PS5 strings
		// arrived in v0.4), but a file where nothing matched means this table no
		// longer describes the release.
		if matched == 0 {
			t.Errorf("no literal from the table matched %s; it has been reworded upstream", rel)
		}
	}
	i18n, err := os.ReadFile(filepath.Join(app, "rh", "i18n.py"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(i18n), `"pair_required"`) != 2 {
		t.Errorf("pair_required count = %d, want 2", strings.Count(string(i18n), `"pair_required"`))
	}
	home, err := os.ReadFile(filepath.Join(app, "rh", "screens", "home.py"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(home), `if "pair_required" in`) {
		t.Error("toast ternary survived")
	}
}
