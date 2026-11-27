package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnalyzeAndStarter(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"en.json": `{"language":"en","strings":{"Play":"Play","Stop":"Stop","Open":"Open"}}`,
		"ja.json": `{"language":"ja","strings":{"Play":"再生"}}`,
		"fr.json": `{"language":"fr","strings":{"Play":"Jouer","Stop":"","Open":"Ouvrir"}}`,
		"en.tour.json": `{"language":"en","strings":{"Tour only":"ignored"}}`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := analyze(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got["ja"], ",") != "Open,Stop" {
		t.Fatalf("Japanese missing = %v", got["ja"])
	}
	if strings.Join(got["fr"], ",") != "Stop" {
		t.Fatalf("French missing = %v", got["fr"])
	}
	if len(got["en"]) != 0 {
		t.Fatalf("the union's own language missed nothing: %v", got["en"])
	}
	starterBytes := starter("ja", got["ja"])
	var doc catalogue
	if err := json.Unmarshal(starterBytes, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Language != "ja" || len(doc.Strings) != 2 || doc.Strings["Open"] != "" {
		t.Fatalf("starter = %+v", doc)
	}
}
