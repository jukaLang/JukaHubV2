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

// TestPortManifestParsing reads the two port.json formats PortMaster has
// shipped (format 2 with nulls, format 4 with lists), the md5 harbourmaster
// records for an installed port, and a port's gameinfo.xml.
func TestPortManifestParsing(t *testing.T) {
	v2 := `{"version": 2, "name": "2048.zip", "items": ["2048.sh", "2048/"], "items_opt": null,
	 "attr": {"title": "2048", "porter": "Christian_Haitian", "runtime": null, "inst": "Ready to run.", "arch": ["aarch64"]}}`
	m, ok := parsePortJSON([]byte(v2))
	if !ok || m.Zip != "2048.zip" || m.Title != "2048" || len(m.Items) != 2 || m.ItemsOpt != nil ||
		m.Runtime != nil || len(m.Porters) != 1 || m.Inst != "Ready to run." || m.MD5 != "" {
		t.Fatalf("format 2: %+v ok=%v", m, ok)
	}
	v4 := "\ufeff" + `{"version": 4, "name": "defblade.zip", "items": ["Defblade.sh", "defblade"], "items_opt": [],
	 "attr": {"title": "Defender's Blade", "porter": ["Cebion"], "runtime": ["mono-6.12.0.122-aarch64.squashfs"]},
	 "status": {"md5": "0123456789abcdef0123456789abcdef", "status": "Installed"}}`
	m, ok = parsePortJSON([]byte(v4))
	if !ok || m.Title != "Defender's Blade" || len(m.Runtime) != 1 || m.MD5 != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("format 4: %+v ok=%v", m, ok)
	}
	if !portUpdateAvailable(m, mcCatalogEntry{MD5: "ffffffffffffffffffffffffffffffff"}) ||
		portUpdateAvailable(m, mcCatalogEntry{MD5: strings.ToUpper(m.MD5)}) || portUpdateAvailable(portMeta{}, mcCatalogEntry{MD5: m.MD5}) {
		t.Fatal("an update needs two different known md5s")
	}
	if _, ok := parsePortJSON([]byte(`[1,2]`)); ok {
		t.Fatal("a list is not a manifest")
	}
	xml := `<?xml version="1.0"?><gameList>
	 <game><path>./Other.sh</path><name>Other</name></game>
	 <game><path>./Defblade.sh</path><name>Defender's Blade</name><desc>Hack and slash</desc><image>./defblade/screenshot.jpg</image></game>
	</gameList>`
	name, desc, pic := parsePortGameInfo([]byte(xml), "/mnt/SDCARD/Roms/PORTS/Defblade.sh")
	if name != "Defender's Blade" || desc != "Hack and slash" || pic != "./defblade/screenshot.jpg" {
		t.Fatalf("gameinfo: %q %q %q", name, desc, pic)
	}
	if !portVersionNewer("2026.10.18-1234", "2026.9.30") || portVersionNewer("2026.10.18", "2026.10.18") || portVersionNewer("x", "2026.1.1") {
		t.Fatal("portVersionNewer")
	}
}

// TestPortUninstallStaysInPortsFolders is the uninstall's safety rule: only
// the script and plain names from the manifest found directly in a ports
// folder are deleted — never a path, "..", PortMaster or a ports folder.
func TestPortUninstallStaysInPortsFolders(t *testing.T) {
	card := t.TempDir()
	roms := filepath.Join(card, "Roms", "PORTS")
	data := filepath.Join(card, "Data", "ports")
	for _, d := range []string{filepath.Join(data, "defblade"), filepath.Join(data, "PortMaster"), filepath.Join(card, "Data", "keep"), roms} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(roms, "Defblade.sh")
	if err := os.WriteFile(script, []byte("#!/bin/bash\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	meta := portMeta{
		Dir:      filepath.Join(data, "defblade"),
		Items:    []string{"Defblade.sh", "defblade/", "../keep", "PortMaster", "/etc", ".hidden", ""},
		ItemsOpt: []string{"ports"},
	}
	got := portUninstallTargets(script, meta, []string{roms, data})
	want := map[string]bool{filepath.Clean(script): true, filepath.Join(data, "defblade"): true}
	if len(got) != len(want) {
		t.Fatalf("targets = %v", got)
	}
	for _, p := range got {
		if !want[p] {
			t.Fatalf("unexpected target %s in %v", p, got)
		}
	}
}

// TestPortScriptFindsPortMaster follows a port script's own search for
// PortMaster: the fixed folders, then ${XDG_DATA_HOME:-$HOME/.local/share}.
func TestPortScriptFindsPortMaster(t *testing.T) {
	dirs := map[string]bool{}
	isDir := func(p string) bool { return dirs[filepath.ToSlash(p)] }
	env := []string{"HOME=/root"}
	if got := portScriptControlPick(env, isDir); got != portScriptControlFallback {
		t.Fatalf("nothing there: %s", got)
	}
	dirs["/root/.local/share/PortMaster"] = true
	if got := filepath.ToSlash(portScriptControlPick(env, isDir)); got != "/root/.local/share/PortMaster" {
		t.Fatalf("HOME: %s", got)
	}
	env = append(env, "XDG_DATA_HOME=/x")
	dirs["/x/PortMaster"] = true
	if got := filepath.ToSlash(portScriptControlPick(env, isDir)); got != "/x/PortMaster" {
		t.Fatalf("XDG_DATA_HOME: %s", got)
	}
	dirs["/opt/tools/PortMaster"] = true
	if got := filepath.ToSlash(portScriptControlPick(env, isDir)); got != "/opt/tools/PortMaster" {
		t.Fatalf("/opt first: %s", got)
	}
}

func TestStockPortsLauncherFromConfig(t *testing.T) {
	card := t.TempDir()
	emu := filepath.Join(card, "Emus", "PORTS")
	roms := filepath.Join(card, "Roms", "PORTS")
	for _, dir := range []string{emu, roms} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	launcher := filepath.Join(emu, "launch_balanced.sh")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(emu, "config.json"), []byte(`{"launch":"launch_balanced.sh"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := portFirmwareLauncher(card, filepath.Join(roms, "Game.sh")); got != launcher {
		t.Fatalf("configured launcher = %q, want %q", got, launcher)
	}
	if got := portFirmwareLauncher(card, filepath.Join(card, "Data", "ports", "Game.sh")); got != "" {
		t.Fatalf("Data/ports was sent through firmware launcher: %q", got)
	}
	if err := os.WriteFile(filepath.Join(emu, "config.json"), []byte(`{"launch":"../../outside.sh"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := portFirmwareLauncher(card, filepath.Join(roms, "Game.sh")); got != "" {
		t.Fatalf("unsafe configured launcher accepted: %q", got)
	}
}

func TestInstalledPortNeedsFirmwareGlibc(t *testing.T) {
	port := t.TempDir()
	manifest := []byte(`{"attr":{"arch":["aarch64"],"min_glibc":"2.38"}}`)
	if err := os.WriteFile(filepath.Join(port, "port.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	need := portManifestMinGlibc(port)
	if need != "2.38" {
		t.Fatalf("manifest min_glibc = %q", need)
	}
	arm64 := classifyPort([]string{"aarch64"})
	if got := portCompatWithGlibc(arm64, need, "GLIBC_2.33"); got.State != portCompatNeedsNewerLibc || !portCompatBlocked(got.State) {
		t.Fatalf("old firmware allowed the port: %+v", got)
	}
	if got := portCompatWithGlibc(arm64, need, "GLIBC_2.38"); got.State != portCompatSupported {
		t.Fatalf("compatible firmware refused the port: %+v", got)
	}
}
