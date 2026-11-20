package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// The move record comes back from a card: every path in it is outside data.
func TestSDMoveManifestPathSafety(t *testing.T) {
	hash := strings.Repeat("a", 64)
	good := `{"format":"jukahub-sd-move","version":1,"state":"verified","files":[{"path":"Roms/GBA/a.gba","size":3,"sha256":"` + hash + `","cat":"games","mtime":1,"srcMtime":1}]}`
	m, err := sdMoveParseManifest([]byte(good))
	if err != nil || len(m.Files) != 1 || m.TotalBytes != 3 {
		t.Fatalf("good manifest: %v %+v", err, m)
	}
	for _, bad := range []string{"../x", "/etc/passwd", "a/../../b", "a//b", "./a", `a\b`, "C:/x", ".jukahub-move/manifest.json", ".JUKAHUB-MOVE/x", ""} {
		doc := strings.Replace(good, "Roms/GBA/a.gba", strings.ReplaceAll(bad, `\`, `\\`), 1)
		if _, err := sdMoveParseManifest([]byte(doc)); err == nil {
			t.Errorf("path %q was accepted", bad)
		}
	}
	for _, doc := range []string{
		strings.Replace(good, `"version":1`, `"version":2`, 1),
		strings.Replace(good, hash, "xyz", 1),
		strings.Replace(good, `"state":"verified"`, `"state":"done"`, 1),
		strings.Replace(good, `"format":"jukahub-sd-move"`, `"format":"other"`, 1),
		strings.Replace(good, `]}`, `,{"path":"Roms/GBA/a.gba","size":3,"sha256":"`+hash+`"}]}`, 1),
	} {
		if _, err := sdMoveParseManifest([]byte(doc)); err == nil {
			t.Errorf("manifest accepted: %s", doc)
		}
	}

	root := t.TempDir()
	if p, err := sdMoveJoin(root, "Saves/x.srm"); err != nil || p != filepath.Join(root, "Saves", "x.srm") {
		t.Fatalf("join: %q %v", p, err)
	}
	if _, err := sdMoveJoin(root, "../outside"); err == nil {
		t.Fatal("join left the root")
	}

	// A problem whose name the record refuses still counts once read back.
	p := sdMoveRecordable(sdMoveProblem{Path: `a\b`, Why: sdMoveWhyBadName})
	if p.Path != "" || len(sdMoveCleanProblems([]sdMoveProblem{p})) != 1 {
		t.Fatalf("recordable: %+v", p)
	}
}

// A journal cut by a power loss keeps the lines before the cut; a "v" line
// only counts for the copy it names.
func TestSDMoveJournalReplay(t *testing.T) {
	h1, h2 := strings.Repeat("1", 64), strings.Repeat("2", 64)
	m := sdMoveManifest{Format: sdMoveFormat, Version: sdMoveVersion, State: sdMoveStateCopying}
	j := `{"k":"c","f":{"path":"a","size":1,"sha256":"` + h1 + `","cat":"other"}}
{"k":"c","f":{"path":"../evil","size":1,"sha256":"` + h1 + `"}}
{"k":"v","f":{"path":"a","size":1,"sha256":"` + h2 + `"}}
{"k":"v","f":{"path":"a","size":1,"sha256":"` + h1 + `"}}
{"k":"c","f":{"path":"b","si`
	if n := sdMoveApplyJournal(&m, []byte(j)); n != 2 {
		t.Fatalf("applied %d lines, want 2", n)
	}
	if len(m.Files) != 1 || m.Files[0].Path != "a" || !m.Files[0].Verified {
		t.Fatalf("replayed files: %+v", m.Files)
	}
}
