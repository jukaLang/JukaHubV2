package main

import (
	"strings"
	"testing"
)

func TestWineAppTitleKeyFoldsSpellingDifferences(t *testing.T) {
	if !strings.Contains(wineAppTitleKey("7-Zip 24.09"), wineAppTitleKey("7-Zip")) {
		t.Fatal("a versioned entry would not be recognised as 7-Zip")
	}
	if wineAppTitleKey("Notepad++") != wineAppTitleKey("notepad plus") && wineAppTitleKey("Notepad++") != "notepad" {
		t.Fatalf("unexpected key %q", wineAppTitleKey("Notepad++"))
	}
}

func TestWineAppCatalogIsSafeAndReadable(t *testing.T) {
	if len(wineAppSuggestions) < 5 {
		t.Fatalf("only %d apps listed", len(wineAppSuggestions))
	}
	if !wineAppCatalogWellFormed(wineAppSuggestions) {
		t.Fatal("an app has no HTTPS project page, licence or purpose")
	}
	seen := map[string]bool{}
	for _, app := range wineAppSuggestions {
		if seen[strings.ToLower(app.Name)] {
			t.Fatalf("duplicate app %q", app.Name)
		}
		seen[strings.ToLower(app.Name)] = true
		if app.Note == "" {
			t.Fatalf("%s has no note", app.Name)
		}
	}
	for _, title := range []string{"7-Zip", "Notepad++", "WinSCP", "WinMerge", "Sumatra PDF", "KeePass", "MPC-HC", "Explorer++", "PuTTY"} {
		if !seen[strings.ToLower(title)] {
			t.Fatalf("catalogue has no %s", title)
		}
	}
	lines := strings.Join(wineAppCatalogLines(0), "\n")
	for _, want := range []string{"does not ship or download", "7-Zip", "GPL", "KeePass"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("card has no %q:\n%s", want, lines)
		}
	}
	if !strings.Contains(lines, "› 7-Zip") {
		t.Fatalf("the first app is not marked as focused:\n%s", lines)
	}
	wineAppCatalogMove(1)
	if wineAppCatalogUI.cursor != 1 {
		t.Fatalf("cursor = %d", wineAppCatalogUI.cursor)
	}
	wineAppCatalogMove(-1)
	if wineAppCatalogUI.cursor != 0 {
		t.Fatalf("cursor after wrap = %d", wineAppCatalogUI.cursor)
	}
}
