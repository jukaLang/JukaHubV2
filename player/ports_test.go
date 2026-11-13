package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPortsRowNameWidthReservesChipAndStatus is the Ports row's layout
// contract: the title budget plus the chip, its gap and the right-aligned
// status exactly fill the row. It used to measure the full title, so a long
// port name painted through the chip and status and off the card.
func TestPortsRowNameWidthReservesChipAndStatus(t *testing.T) {
	cases := []struct {
		name                  string
		rowW, badgeW, statusW int32
		want                  int32
	}{
		{name: "name only", rowW: 400, want: 368},
		{name: "status", rowW: 400, statusW: 70, want: 298},
		{name: "chip and status", rowW: 400, badgeW: 48, statusW: 70, want: 242},
		{name: "narrow row floors at zero", rowW: 40, badgeW: 48, statusW: 70, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := portsRowNameWidth(tc.rowW, tc.badgeW, tc.statusW)
			if got != tc.want {
				t.Fatalf("portsRowNameWidth(%d, %d, %d) = %d, want %d",
					tc.rowW, tc.badgeW, tc.statusW, got, tc.want)
			}
			if got > 0 {
				fill := got + 32 + tc.statusW
				if tc.badgeW > 0 {
					fill += tc.badgeW + 8
				}
				if fill != tc.rowW {
					t.Fatalf("title + status + chip fill %d px of a %d px row", fill, tc.rowW)
				}
			}
		})
	}
}

// TestMinecraftPortsAndCopyPlan covers the Minecraft screen's logic
// (minecraft.go, minecraft_peer.go): PortMaster's catalogue in both of its
// layouts, which port a name belongs to, the port's start files, and the file
// list a PC offers a handheld — never outside versions/, libraries/, assets/.
func TestMinecraftPortsAndCopyPlan(t *testing.T) {
	dict := `{"ports": {"minecraft.zip": {"attr": {"title": "Minecraft"}, "source": {"md5": "0123456789abcdef0123456789abcdef"}},
		"minecraftforgepack.zip": {"attr": {"title": "Minecraft Forge Pack"}},
		"classicube.zip": {"name": "classicube.zip", "attr": {"title": "ClassiCube"}, "url": "https://example.org/classicube.zip"},
		"evil.zip": {"name": "../evil.zip", "attr": {"title": "Luanti"}}},
		"utils": {"luanti.zip": {"attr": {"title": "Luanti"}}}}`
	list := `{"ports": [{"name": "luanti.zip", "attr": {"title": "Luanti"}, "download_url": "http://insecure/luanti.zip"},
		{"name": "mineclonia.zip", "attr": {"title": "Mineclonia"}}]}`
	entries := mcParsePortsCatalog([]byte(dict))
	if len(entries) != 3 {
		t.Fatalf("dict layout: %d entries, want 3 (no path names, no utils): %+v", len(entries), entries)
	}
	java, ok := mcCatalogFind(entries, mcPortJava)
	if !ok || java.Zip != "minecraft.zip" || java.MD5 == "" || java.URL != mcPortDownloadBase+"minecraft.zip" {
		t.Fatalf("java port = %+v, %v", java, ok)
	}
	if cc, ok := mcCatalogFind(entries, mcPortClassiCube); !ok || cc.URL != "https://example.org/classicube.zip" {
		t.Fatalf("classicube = %+v, %v", cc, ok)
	}
	if lu, ok := mcCatalogFind(mcParsePortsCatalog([]byte(list)), mcPortLuanti); !ok || lu.Zip != "mineclonia.zip" {
		t.Fatalf("luanti (the http entry is dropped) = %+v, %v", lu, ok)
	}
	for name, want := range map[string]mcPortKind{"Minecraft": mcPortJava, "minecraft-java": mcPortJava,
		"ClassiCube": mcPortClassiCube, "Minetest": mcPortLuanti, "Luanti": mcPortLuanti} {
		if !mcPortMatches(want, name) {
			t.Errorf("%q does not match kind %d", name, want)
		}
	}
	for _, name := range []string{"Minecraft Classic", "Minecraft Dungeons", "minecraft pi edition", "ClassiCube"} {
		if mcPortMatches(mcPortJava, name) {
			t.Errorf("%q matched the Java port", name)
		}
	}
	starts := []string{"1.12.2", "1.7.10-Forge", "1.16.5-Fabric"}
	for v, want := range map[string]string{"1.12.2": "1.12.2", "1.7.10-Forge10.13.4.1614-1.7.10": "1.7.10-Forge",
		"1.16.5-fabric-0.14": "1.16.5-Fabric", "1.12.2-forge-14.23": "", "1.20.1": ""} {
		if got := mcStartFor(starts, v); got != want {
			t.Errorf("mcStartFor(%q) = %q, want %q", v, got, want)
		}
	}

	for _, rel := range []string{"../x", "versions/../launcher_accounts.json", "/etc/passwd", "launcher_accounts.json",
		"saves/w/level.dat", `versions\a`, "assets/objects/", "versions/a//b", "C:/x"} {
		if _, ok := mcPeerFileAllowed(rel); ok {
			t.Errorf("mcPeerFileAllowed(%q) allowed", rel)
		}
	}
	if got := mcMavenPath("org.lwjgl:lwjgl:3.3.1:natives-linux@zip"); got != "org/lwjgl/lwjgl/3.3.1/lwjgl-3.3.1-natives-linux.zip" {
		t.Errorf("maven path = %q", got)
	}
	if mcMavenPath("a:../b:1") != "" || mcLibAllowed([]mcRule{{Action: "allow", OS: &struct {
		Name string `json:"name"`
	}{Name: "osx"}}}) {
		t.Error("a bad maven name or an osx-only library got through")
	}

	// A Forge version built on 1.12.2, with one asset; the account file
	// beside it must never enter the plan.
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("launcher_accounts.json", "secret")
	write("versions/1.12.2/1.12.2.json", `{"id":"1.12.2","assetIndex":{"id":"1.12"},
		"libraries":[{"name":"com.mojang:a:1","downloads":{"artifact":{"path":"com/mojang/a/1/a-1.jar","sha1":"`+strings.Repeat("a", 40)+`","size":3}}},
		{"name":"x:mac:1","rules":[{"action":"allow","os":{"name":"osx"}}]},
		{"name":"org.lwjgl:lwjgl:3:natives-linux"}]}`)
	write("versions/1.12.2/1.12.2.jar", "jar")
	write("versions/forge/forge.json", `{"id":"forge","inheritsFrom":"1.12.2","libraries":[{"name":"net.minecraftforge:forge:1"}]}`)
	write("libraries/com/mojang/a/1/a-1.jar", "abc")
	write("libraries/net/minecraftforge/forge/1/forge-1.jar", "f")
	hash := strings.Repeat("b", 40)
	write("assets/indexes/1.12.json", `{"objects":{"x.ogg":{"hash":"`+hash+`","size":2},"y":{"hash":"../../x","size":1}}}`)
	write("assets/objects/bb/"+hash, "ok")
	plan, err := mcBuildPlan(root, "forge")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range plan.Files {
		got[f.Path] = f.SHA1
	}
	for _, want := range []string{"versions/forge/forge.json", "versions/1.12.2/1.12.2.json", "versions/1.12.2/1.12.2.jar",
		"libraries/net/minecraftforge/forge/1/forge-1.jar", "libraries/com/mojang/a/1/a-1.jar",
		"assets/indexes/1.12.json", "assets/objects/bb/" + hash} {
		if _, ok := got[want]; !ok {
			t.Errorf("plan lacks %s (has %v)", want, got)
		}
	}
	if len(plan.Files) != 7 || plan.Missing != 0 || got["assets/objects/bb/"+hash] != hash || got["libraries/com/mojang/a/1/a-1.jar"] == "" {
		t.Errorf("plan = %+v", plan.Files)
	}
	if _, err := mcBuildPlan(root, "../versions"); err == nil {
		t.Error("a path as version id was accepted")
	}
	for _, name := range []string{"COM1", "con.json", "NUL", "lpt9.txt", "CONIN$", "1.12.", "a "} {
		if mcSafeName(name) {
			t.Errorf("mcSafeName(%q) accepted a name Windows treats specially", name)
		}
	}
	if got := mcStartFor([]string{"1.12"}, "1.12.2"); got != "" {
		t.Errorf("start 1.12 fitted version 1.12.2 (%q)", got)
	}
	if got := mcStartForVersion(starts, mcPeerVersion{ID: "fabric-loader-0.14-1.16.5", Inherits: "1.16.5"}); got != "1.16.5-Fabric" {
		t.Errorf("Fabric profile start = %q", got)
	}
	// The version's json is copied last: it is what marks the version present.
	ordered := mcCopyOrder(plan.Files, "forge")
	if last := ordered[len(ordered)-1].Path; last != "versions/forge/forge.json" || len(ordered) != len(plan.Files) {
		t.Errorf("copy order ends with %q (%d of %d files)", last, len(ordered), len(plan.Files))
	}
}
