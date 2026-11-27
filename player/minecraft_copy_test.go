package main

// minecraft_copy_test.go — AND-21: the copy plan keeps only what the installed
// port lacks, and the copy row says how large the copy will be.

import (
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMinecraftCopyPlanSkipsPortFilesAndShowsSize(t *testing.T) {
	gameDir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(gameDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The port's own copy already holds the platform library and one asset.
	write("libraries/org/lwjgl/lwjgl/3.3.1/lwjgl-3.3.1.jar", "arm library")
	sum := sha1.Sum([]byte("asset"))
	assetSHA := hex.EncodeToString(sum[:])
	write("assets/objects/"+assetSHA[:2]+"/"+assetSHA, "asset")
	plan := mcPlan{Version: "1.12.2", Bytes: 12 + 5 + 5 + 6, Missing: 1, Files: []mcPlanFile{
		{Path: "libraries/org/lwjgl/lwjgl/3.3.1/lwjgl-3.3.1.jar", Size: 11},
		{Path: "libraries/com/mojang/a/1/a-1.jar", Size: 5, SHA1: strings.Repeat("b", 40)},
		{Path: "assets/objects/" + assetSHA[:2] + "/" + assetSHA, Size: 5, SHA1: assetSHA},
		{Path: "versions/1.12.2/1.12.2.json", Size: 6},
	}}
	got := mcPlanKeepOnlyPortLacks(plan, gameDir)
	if len(got.Files) != 2 {
		t.Fatalf("plan keeps %d files, want the 2 the port lacks: %+v", len(got.Files), got.Files)
	}
	if got.Files[0].Path != "libraries/com/mojang/a/1/a-1.jar" || got.Files[1].Path != "versions/1.12.2/1.12.2.json" {
		t.Fatalf("remaining files = %+v", got.Files)
	}
	if got.Bytes != 11 || got.Missing != 1 {
		t.Fatalf("bytes = %d, missing = %d; want 11 and 1", got.Bytes, got.Missing)
	}

	// The row says the size before A, in the same units as the copy card.
	prev := mcScreen
	t.Cleanup(func() { mcScreen = prev })
	mcScreen = mcScreenState{}
	mcScreen.pcs = []mcPCVersions{{peerName: "Studio PC"}}
	mcScreen.ports[mcPortJava] = mcPortState{Installed: true, GameDir: gameDir}
	row := mcCopyRowSpec(mcCopyRow{pc: 0, version: mcPeerVersion{ID: "1.12.2", Size: 1536}})
	if !strings.Contains(row.subtitle, "1.5 KB") {
		t.Fatalf("row subtitle %q does not name the 1.5 KB copy", row.subtitle)
	}
}

func TestMinecraftVersionListShowsDeclaredSize(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	hash := strings.Repeat("c", 40)
	write("versions/1.12.2/1.12.2.json", `{"id":"1.12.2","type":"release","downloads":{"client":{"size":100}},
		"libraries":[{"name":"com.mojang:a:1","downloads":{"artifact":{"path":"com/mojang/a/1/a-1.jar","size":10}}}],
		"assetIndex":{"id":"1.12","size":20}}`)
	write("assets/indexes/1.12.json", `{"objects":{"a.ogg":{"hash":"`+hash+`","size":500}}}`)
	size := mcDeclaredSize(root, "1.12.2")
	if size < 630 {
		t.Fatalf("declared size = %d, want the json, jar, library, index and asset sizes", size)
	}
	versions := mcListVersions(root)
	if len(versions) != 1 || versions[0].Size != size {
		t.Fatalf("versions = %+v, want one with size %d", versions, size)
	}
}
