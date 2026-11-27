package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
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

// A port that stops at once is explained from what it printed (outside
// text): JukaHub's launch log splits into notes and output, and each known
// failure line lands in its cause — and only there.
func TestPortQuickExitLogFindings(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "SuperTuxKart.log")
	header := "# JukaHub port launch x\n# note: bash: none usable — /bin/bash cannot run bash arrays (BusyBox's sh)\n" + portLogOutputMarker + "\n"
	if err := os.WriteFile(logPath, []byte(header+"Starting\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	portLogNoteEnd(logPath, nil, 2*time.Second, "")
	notes, output := gameDiagPortJukaLog(logPath, time.Time{})
	if len(notes) != 1 || !strings.HasPrefix(notes[0], "bash: none usable") || len(output) == 0 || output[0] != "Starting" {
		t.Fatalf("launch log split: notes %q output %q", notes, output)
	}
	for _, line := range output {
		if strings.Contains(line, "launch ended") {
			t.Fatalf("JukaHub's end line counted as the script's output: %q", output)
		}
	}
	if end := pmLaunchEnd(pmTailLines(logPath, 5)); end != "after 2s (exit status 0)" {
		t.Fatalf("end line: %q", end)
	}
	// PortMaster's own log.txt: pugwash's errors name their cause.
	for _, c := range []struct{ line, want string }{
		{"ImportError: could not find any library for SDL2 (PYSDL2_DLL_PATH: /usr/lib)", "PortMaster's Python could not find SDL2"},
		{`/mnt/SDCARD/Apps/PortMaster/PortMaster/device_info.txt: line 915: syntax error: unexpected "("`, "A PortMaster script ran under BusyBox's sh"},
		{"env: can't execute 'python3': No such file or directory", "PortMaster could not start Python 3"},
		{"KeyError: 'primary_arch'", "PortMaster could not tell what handheld this is"},
		{"SDL_Init failed: kmsdrm not available", "PortMaster's menu could not open the screen"},
		{"Starting PortMaster", ""},
	} {
		var got []string
		for _, is := range pmOwnLogFindings([]string{c.line}) {
			got = append(got, is.Title)
		}
		if strings.Join(got, "|") != c.want {
			t.Errorf("log.txt %q: got %q, want %q", c.line, got, c.want)
		}
	}
	script := "/mnt/SDCARD/Data/ports/SuperTuxKart.sh"
	titles := func(lines ...string) string {
		issues, _, _ := gameDiagPortLogFindings(nil, lines, script)
		var out []string
		for _, is := range issues {
			out = append(out, is.Title)
		}
		return strings.Join(out, "|")
	}
	for _, c := range []struct{ line, want string }{
		{`/mnt/SDCARD/Apps/PortMaster/PortMaster/device_info.txt: line 12: syntax error: unexpected "("`, "It ran under BusyBox's sh, not bash"},
		{`run.sh: 4: Syntax error: "(" unexpected`, "It ran under BusyBox's sh, not bash"},
		{"./supertuxkart: /lib/libc.so.6: version `GLIBC_2.34' not found (required by ./supertuxkart)", "Built for a newer C library than this firmware's"},
		{"./supertuxkart: error while loading shared libraries: libGLESv2.so.2: cannot open shared object file: No such file or directory", "A library it needs is not on this handheld"},
		{"SDL_Init failed: No available video device", "Its graphics did not start"},
		{"line 40:  1234 Killed                  ./supertuxkart", "The system stopped it, likely for memory"},
		{"line 38: ./supertuxkart: not found", "Its game program could not be run"},
		{script + ": line 38: ./supertuxkart: No such file or directory", "Its game program could not be run"},
		{`line 5: 10 / : syntax error: operand expected (error token is "/ ")`, ""},
		{"line 9: gptokeyb: command not found", "Commands this firmware does not have"},
		{"ALC: could not create context", ""},
		{"sh: setterm: not found", ""},
		{"data/level1.pak: not found", ""},
		// PortMaster's own device probe asks for the CPU name; no port needs
		// it (JH-AGE2UH was told only this).
		{"/mnt/SDCARD/Apps/PortMaster/PortMaster/device_info.txt: line 268: lscpu: command not found", ""},
		{"line 3: syntax error near unexpected token `('", ""},
		// PortMaster's TrimUI control.txt was found: its own lines failed
		// (JH-AT4EEE blamed PortMaster for these).
		{"/roms/ports/PortMaster/control.txt: line 9: /mnt/SDCARD/System/etc/ex_config: No such file or directory", ""},
		{"/roms/ports/PortMaster/control.txt: line 25: sdl2imgshow: command not found", ""},
		{"/tmp/jukahub-greenovercast-1.sh: line 18: /roms/ports/PortMaster/control.txt: No such file or directory", "The script did not find PortMaster"},
		{"/roms/ports/PortMaster/control.txt: line 30: /roms/ports/PortMaster/funcs.txt: No such file or directory", "PortMaster is missing some of its own files"},
		{"Credential store could not be read", "Its saved sign-in could not be read"},
		// JH-7RKB39 (Billy Frontier): PortMaster's own cleanup stops its key
		// mapper as the port ends, so that "Killed" line is not the system
		// stopping the game (which segfaulted).
		{"/mnt/SDCARD/Apps/PortMaster/PortMaster/funcs.txt: line 140:  4478 Killed                  $GPTOKEYB \"BillyFrontier.aarch64\" -c \"./billyfrontier.gptk\"", ""},
		{"line 140:  4478 Killed                  /mnt/SDCARD/Apps/PortMaster/PortMaster/gptokeyb game.aarch64", ""},
		// A script that runs with set -u and reads a variable nothing gave it
		// (JH-ZBPUTA: a port gone after 700 ms with "line 5: $1: unbound
		// variable", and a card that said JukaHub found nothing).
		{script + ": line 5: $1: unbound variable", "A script stopped on a variable that was not set"},
		{"Data/ports/hakux-disc/launch.sh: 5: $1: parameter not set", "A script stopped on a variable that was not set"},
		{"/roms/ports/PortMaster/control.txt: line 30: XDG_DATA_HOME: unbound variable", "A script stopped on a variable that was not set"},
		// BusyBox refuses a bash builtin's option as `file: export: line 75:
		// illegal option -f` (JH-AN6U7W: OldBits Hub's `export -f`), naming a
		// card path that holds a space.
		{`/mnt/SDCARD/Roms/Ports/OldBits Hub.sh: export: line 75: illegal option -f`, "It ran under BusyBox's sh, not bash"},
		{"sh: set: line 9: illegal option -o pipefail", "It ran under BusyBox's sh, not bash"},
	} {
		if got := titles(c.line); got != c.want {
			t.Errorf("%q: got %q, want %q", c.line, got, c.want)
		}
	}
	if files := gameDiagPortMissingFiles(gameDiagPortDropHarmless([]string{
		"/roms/ports/PortMaster/control.txt: line 9: /mnt/SDCARD/System/etc/ex_config: No such file or directory"})); len(files) != 0 {
		t.Errorf("TRIMUI_EX's ex_config listed as a missing port file: %q", files)
	}
	// Under a working bash a sourced PortMaster file's syntax error is not
	// BusyBox's.
	good := []string{"bash: JukaHub's (Ubuntu's bash on the private glibc 2.39) — /bin/bash cannot run bash arrays"}
	if issues, _, _ := gameDiagPortLogFindings(good, []string{`/x/device_info.txt: line 12: syntax error: unexpected "("`}, script); len(issues) != 0 {
		t.Errorf("BusyBox blamed under a working bash: %+v", issues)
	}
	// The bash note says what JukaHub supplied. A firmware launcher runs the
	// port with its own `/bin/sh`, which that note does not cover, so the
	// port's own script meeting BusyBox is a cause there (JH-AN6U7W).
	busy := script + ": export: line 75: illegal option -f"
	viaFW := append(append([]string(nil), good...), "through the firmware's Ports launcher /mnt/SDCARD/Emus/PORTS/launch_balanced.sh")
	if issues, _, _ := gameDiagPortLogFindings(viaFW, []string{busy}, script); len(issues) != 1 ||
		issues[0].Title != "It ran under BusyBox's sh, not bash" {
		t.Errorf("BusyBox under the firmware launcher: %+v", issues)
	} else if !strings.Contains(issues[0].Detail, "Try it directly") {
		t.Errorf("launcher detail = %q", issues[0].Detail)
	}
	if issues, _, _ := gameDiagPortLogFindings(good, []string{busy}, script); len(issues) != 0 {
		t.Errorf("BusyBox blamed when JukaHub's bash ran the port: %+v", issues)
	}
	if got := gameDiagPortLineFile(`/mnt/SDCARD/Roms/Ports/OldBits Hub.sh: export: line 75: illegal option -f`); got != "/mnt/SDCARD/Roms/Ports/OldBits Hub.sh" {
		t.Errorf("file of a BusyBox builtin error = %q", got)
	}
}

// A port whose log holds an error JukaHub knows nothing specific about still
// gets a cause, so its card never says JukaHub found nothing (JH-AN6U7W).
// What another finding owns and what changes nothing stay out.
func TestPortErrorFallback(t *testing.T) {
	for _, c := range []struct{ line, want string }{
		{"ALC: could not create context", "ALC: could not create context"},
		{"OldBits Hub.sh: export: line 75: illegal option -f", "OldBits Hub.sh: export: line 75: illegal option -f"},
		{"data/level1.pak: not found", ""},
		{"sh: setterm: not found", ""},
		{"Starting game", ""},
	} {
		if got := gameDiagPortErrorLine([]string{c.line}); got != c.want {
			t.Errorf("%q: got %q, want %q", c.line, got, c.want)
		}
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "Port.sh")
	report := gameDiagReport{}
	gameDiagPortAddOutput(&report, nil, []string{"ALC: could not create context"}, script, dir)
	if len(report.Issues) != 1 || report.Issues[0].Title != "It stopped on an error it printed" ||
		report.Issues[0].Level != int(gameDiagMaybe) {
		t.Fatalf("fallback issue = %+v", report.Issues)
	}
	// A cause JukaHub knows wins over the fallback.
	report = gameDiagReport{}
	gameDiagPortAddOutput(&report, nil, []string{script + ": export: line 75: illegal option -f"}, script, dir)
	if len(report.Issues) != 1 || report.Issues[0].Title != "It ran under BusyBox's sh, not bash" {
		t.Fatalf("BusyBox issue = %+v", report.Issues)
	}
}

// portTestPlatform points P().DataDir at a temporary folder, so a test never
// writes into the developer's real data folder.
type portTestPlatform struct {
	trimuiLinuxPlatform
	dir string
}

func (p portTestPlatform) DataDir() (string, error) { return p.dir, nil }

// "Try it directly" remembers the choice for that port alone (JH-AN6U7W):
// the script goes in data/ports-direct.txt, and another port is untouched.
func TestPortDirectChoiceIsPerPort(t *testing.T) {
	prev := platform
	dir := t.TempDir()
	platform = portTestPlatform{dir: dir}
	t.Cleanup(func() { platform = prev })
	script := filepath.Join(dir, "Roms", "Ports", "OldBits Hub.sh")
	other := filepath.Join(dir, "Roms", "Ports", "Other.sh")
	if portDirectChosen(script) {
		t.Fatal("a port was already chosen before anything was saved")
	}
	if err := portDirectRemember(script); err != nil {
		t.Fatalf("remember: %v", err)
	}
	if !portDirectChosen(script) {
		t.Fatal("the chosen port is not read back")
	}
	if portDirectChosen(other) {
		t.Fatal("the choice leaked to another port")
	}
	// A second click does not add a second line.
	if err := portDirectRemember(script); err != nil {
		t.Fatalf("remember again: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, portDirectFileName))
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != filepath.ToSlash(script) {
		t.Fatalf("store = %q, want the one script", got)
	}
	// A card saved on FAT may come back in another case or with either
	// separator; it is still the same port.
	if !portDirectChosen(strings.ToLower(filepath.ToSlash(script))) {
		t.Fatal("a case variant of the saved path is not the same port")
	}
}

// The card offers "Try it directly" only when the cause is BusyBox and the
// firmware's launcher ran the port: a retry cannot mend a script's own
// #!/bin/bash line under a working bash.
func TestPortOffersDirectOnlyForLauncherBusyBox(t *testing.T) {
	busy := launchIssue{Level: int(gameDiagSure), Title: gameDiagPortBusyBoxTitle}
	notes := []string{"bash: JukaHub's (Ubuntu's bash on the private glibc 2.39)"}
	via := append(append([]string(nil), notes...), "through the firmware's Ports launcher /mnt/SDCARD/Emus/PORTS/launch_balanced.sh")
	if gameDiagPortOffersDirect([]launchIssue{busy}, notes) {
		t.Fatal("offered for a port that already ran under JukaHub's bash")
	}
	if gameDiagPortOffersDirect(nil, via) {
		t.Fatal("offered without the BusyBox finding")
	}
	if !gameDiagPortOffersDirect([]launchIssue{busy}, via) {
		t.Fatal("not offered for a BusyBox port the firmware launcher ran")
	}
}

// A port's environment carries no shell option set from JukaHub: a bash started
// from another bash inherits nounset and errexit through SHELLOPTS, and one
// that is handed BASH_ENV or ENV runs that file first (JH-ZBPUTA).
func TestPortEnvDropsShellOptions(t *testing.T) {
	env := portEnvDropShellVars([]string{
		"HOME=/",
		"SHELLOPTS=nounset:pipefail",
		"BASHOPTS=extglob",
		"BASH_ENV=/tmp/first.sh",
		"ENV=/tmp/also-first.sh",
		"PATH=/usr/bin:/bin",
	})
	if got, want := strings.Join(env, "|"), "HOME=/|PATH=/usr/bin:/bin"; got != want {
		t.Fatalf("port environment = %q, want %q", got, want)
	}
	// A variable that only looks like one stays.
	if got := portEnvDropShellVars([]string{"SHELLOPTS_X=1", "ENVS=2"}); strings.Join(got, "|") != "SHELLOPTS_X=1|ENVS=2" {
		t.Fatalf("near-miss names dropped: %q", got)
	}
}

// Half-Life's own check for its game data (JH-AGE2UH): the script prints
// "Missing game files" on the screen, which no log JukaHub reads holds, and
// exits, so the file it names is read from the script itself.
func TestPortScriptGameDataCheck(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "Half-Life.sh")
	body := `#!/bin/bash
# PORTMASTER: half-life.zip, Half-Life.sh
GAMEDIR="` + dir + `"
if [[ -f "${GAMEDIR}/binaries/valve_first_run" ]]; then
  if [[ ! -f "${GAMEDIR}/valve/halflife.wad" ]]; then
    echo "Missing game files, see README for more info." > /dev/tty0
    sleep 5
    $ESUDO systemctl restart oga_events &
    exit 1
  fi
fi
# A folder it makes when it is not there is no stop.
if [ ! -d "${GAMEDIR}/save" ]; then
  mkdir -p "${GAMEDIR}/save"
fi
# A note and no exit: no stop either.
if [ ! -f "${GAMEDIR}/port.json" ]; then
  echo "port.json is missing"
fi
`
	if err := os.WriteFile(script, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := gameDiagPortNeededFile(script, dir); got != "valve/halflife.wad" {
		t.Fatalf("missing data check: %q, want valve/halflife.wad", got)
	}
	// With the game's file there the port goes on.
	if err := os.MkdirAll(filepath.Join(dir, "valve"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "valve", "halflife.wad"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := gameDiagPortNeededFile(script, dir); got != "" {
		t.Fatalf("data present, still reported: %q", got)
	}
	// Half-Life builds GAMEDIR from PORTDIR: the folder is found, and the
	// check inside it read, once that is filled in.
	card := t.TempDir()
	indirect := `#!/bin/bash
PORTDIR="/$directory/ports/"
GAMEDIR="${PORTDIR}/Half-Life"
cd $GAMEDIR
if [[ ! -f "${GAMEDIR}/valve/halflife.wad" ]]; then
  echo "Missing game files, see README for more info." > $CUR_TTY
  sleep 5
  exit 1
fi
`
	if err := os.WriteFile(filepath.Join(card, "Half-Life.sh"), []byte(indirect), 0o644); err != nil {
		t.Fatal(err)
	}
	portDir := filepath.Join(card, "Half-Life")
	if err := os.MkdirAll(portDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if dir, found := gameDiagPortDir(filepath.Join(card, "Half-Life.sh")); dir != portDir || found != "Half-Life" {
		t.Fatalf("GAMEDIR built from PORTDIR: folder %q rel %q, want %q", dir, found, portDir)
	}
	if got := gameDiagPortNeededFile(filepath.Join(card, "Half-Life.sh"), portDir); got != "valve/halflife.wad" {
		t.Fatalf("GAMEDIR built from PORTDIR: missing data %q", got)
	}
	// A script that checks something outside its folder says nothing about
	// the port's data.
	outside := filepath.Join(dir, "Other.sh")
	if err := os.WriteFile(outside, []byte("if [ ! -f /roms/bios/scph1001.bin ]; then\n  echo \"Missing BIOS\"\n  exit 1\nfi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := gameDiagPortNeededFile(outside, dir); got != "" {
		t.Fatalf("outside-file check reported: %q", got)
	}
}

// Where a port's game files go: its gamedata folder when it has one, else the
// port's own folder, and the path written as the card shows it in a PC.
func TestPortGameDataFolder(t *testing.T) {
	old := portMasterSDRoot
	portMasterSDRoot = t.TempDir()
	defer func() { portMasterSDRoot = old }()

	dir := filepath.Join(portMasterSDRoot, "Data", "ports", "Half-Life")
	if err := os.MkdirAll(filepath.Join(dir, "binaries"), 0o755); err != nil {
		t.Fatal(err)
	}
	if folder, sub := portGameDataFolder(dir); folder != dir || sub != "" {
		t.Fatalf("no gamedata: got %q %q, want the port's own folder", folder, sub)
	}
	data := filepath.Join(dir, "gamedata")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	if folder, sub := portGameDataFolder(dir); folder != data || sub != "gamedata" {
		t.Fatalf("gamedata: got %q %q, want %q", folder, sub, data)
	}
	if got := gameDiagPortCardPath(data); got != "Data/ports/Half-Life/gamedata" {
		t.Fatalf("card path: %q, want Data/ports/Half-Life/gamedata", got)
	}
	// The card's detail names the folder on the card and the file the script
	// stops without, so a copy is made in the right place, one folder deep.
	detail := gameDiagPortDataDetail(dir, "valve/halflife.wad")
	for _, want := range []string{
		"Data/ports/Half-Life",
		"The port's startup script stops until valve/halflife.wad is there",
		"Copy the whole valve folder",
		"Data/ports/Half-Life/valve/halflife.wad",
		"not one folder deeper",
		"Start the port again when the files are in place",
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail %q does not name %q", detail, want)
		}
	}
	if strings.Contains(detail, "file below") {
		t.Errorf("detail %q still points below instead of naming the file", detail)
	}
	// Without a file the script checks, the detail still says where a copy goes.
	plain := gameDiagPortDataDetail(dir, "")
	if !strings.Contains(plain, "Data/ports/Half-Life") || strings.Contains(plain, "startup script") {
		t.Errorf("detail without a checked file: %q", plain)
	}
	// The finding names the port, so the card says whose data is missing, and
	// the port's own checks know it apart from their other findings.
	title := gameDiagPortDataTitle("Half-Life")
	if title != "Half-Life's game files are not there yet" || !gameDiagIsPortDataTitle(title) {
		t.Errorf("data finding title = %q", title)
	}
	if gameDiagIsPortDataTitle("Its game program could not be run") {
		t.Errorf("another finding counted as the data finding")
	}
}

// A port's start prepares PortMaster's files itself: a card with none installs
// them, and the TrimUI app without its control.txt is repaired (JH-7RKB39's
// card told the player to open PortMaster for a card JukaHub can set up).
func TestPortMasterLaunchNeed(t *testing.T) {
	for _, c := range []struct {
		control, launcher string
		app               bool
		install, repair   bool
	}{
		{"/mnt/SDCARD/Apps/PortMaster/PortMaster", "/mnt/SDCARD/Apps/PortMaster/launch.sh", true, false, false},
		{"/roms/ports/PortMaster", "", false, false, false},
		{"", "/mnt/SDCARD/Apps/PortMaster/launch.sh", true, false, true},
		{"", "", false, true, false},
		{"", "/mnt/SDCARD/Ports/PortMaster.sh", false, false, false},
	} {
		install, repair := portMasterLaunchNeedOf(c.control, c.app, c.launcher)
		if install != c.install || repair != c.repair {
			t.Errorf("control=%q app=%v launcher=%q: got install=%v repair=%v, want %v %v",
				c.control, c.app, c.launcher, install, repair, c.install, c.repair)
		}
	}
}

// GreenOvercast's settings folder (greenovercast_store.go): the last launch's
// log names the folder whose saved sign-in could not be read; the move to
// internal storage copies the small files but leaves that sign-in and the
// log on the card, and keeps the artwork cache there; a set-aside renames.
func TestGreenOvercastSettingsStore(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "card")
	card := filepath.Join(home, ".config", goSettingsDirName)
	priv := filepath.Join(root, "udisk", "config", goSettingsDirName)
	logPath := filepath.Join(root, "GreenOvercast.log")
	write := func(p, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"settings.json", "tokens.bin", "tokens.key", goLogName} {
		write(filepath.Join(card, name), name)
	}
	// A log from before the folder was noted: HOME names it.
	write(logPath, "# JukaHub port launch x\n# HOME="+home+"\n"+portLogOutputMarker+
		"\nSelected video decoder: ffmpeg-software\nCredential store could not be read\nLifecycle: cold_start -> shutdown\n")
	dir, at := goStoreFailedIn(logPath)
	if !goSameDir(dir, card) || at.IsZero() {
		t.Fatalf("failed folder %q (at %v), want %q", dir, at, card)
	}
	if err := goMoveSettings(card, priv, false); err != nil {
		t.Fatal(err)
	}
	if !regularFile(filepath.Join(priv, "settings.json")) || regularFile(filepath.Join(priv, "tokens.bin")) ||
		regularFile(filepath.Join(priv, goLogName)) || !regularFile(filepath.Join(card, "tokens.bin")) {
		t.Fatal("move: settings copied; the unreadable sign-in and the log stay on the card")
	}
	if !dirExists(filepath.Join(card, goArtworkDirName)) {
		t.Fatal("the artwork cache belongs on the card")
	}
	// The noted folder wins over HOME; its sign-in is renamed, not deleted.
	write(filepath.Join(priv, "tokens.bin"), "new")
	write(logPath, "# HOME="+home+"\n# note: "+goSettingsNote+priv+" (internal storage)\n"+portLogOutputMarker+"\nCredential store could not be read\n")
	if dir, at = goStoreFailedIn(logPath); !goSameDir(dir, priv) {
		t.Fatalf("noted folder %q, want %q", dir, priv)
	}
	if note := goStoreSetAside(priv, at); note == "" || regularFile(filepath.Join(priv, "tokens.bin")) || goStoreAsideCount(priv) != 1 {
		t.Fatalf("set aside: note %q, count %d", note, goStoreAsideCount(priv))
	}
	// A launch that read its sign-in sets nothing aside.
	write(logPath, "# note: "+goSettingsNote+priv+"\n"+portLogOutputMarker+"\nLifecycle: cold_start -> sign_in\n")
	if dir, _ = goStoreFailedIn(logPath); dir != "" {
		t.Fatalf("clean launch named %q", dir)
	}
}

// What a launcher leaves in its group (read from /proc, outside data) is
// ended only when all of it is helpers: PortMaster's splash and shells
// waiting for it go, a LÖVE port or any other game keeps the screen.
func TestPortOrphanPick(t *testing.T) {
	pm := "/mnt/SDCARD/Apps/PortMaster/PortMaster"
	splash := portOrphan{pid: 20, ppid: 10, comm: "love.aarch64", argv: []string{pm + "/utils/love.aarch64", pm + "/utils/pmsplash/"}}
	// JukaHub's bash on the private glibc: /proc shows the loader's command line.
	rt := "/mnt/SDCARD/JukaHub/data/private-glibc/current"
	wrapper := portOrphan{pid: 10, ppid: 1, comm: "ld-linux-aarch6", argv: []string{rt + "/ld-linux-aarch64.so.1", "--inhibit-cache",
		"--library-path", rt, "--argv0", "/tmp/jukahub-port-runtime/bash/bash", rt + "/usr/bin/bash", "launch.sh"}}
	mapper := portOrphan{pid: 30, ppid: 1, comm: "gptokeyb"}
	lovePort := portOrphan{pid: 40, ppid: 1, comm: "love.aarch64", argv: []string{"./love.aarch64", "gamedata"}, cwd: "/mnt/SDCARD/Data/ports/balatro"}
	idle := portOrphan{pid: 50, ppid: 1, comm: "sh"}
	byCwd := portOrphan{pid: 60, ppid: 1, comm: "love.aarch64", argv: []string{"./love.aarch64", "."}, cwd: pm + "/utils/pmsplash"}
	for _, c := range []struct {
		name string
		left []portOrphan
		want int
	}{
		{"splash, its shell and a key mapper", []portOrphan{splash, wrapper, mapper}, 3},
		{"splash known by its folder", []portOrphan{byCwd}, 1},
		{"a LÖVE port is a game", []portOrphan{lovePort}, 0},
		{"a game beside the splash keeps everything", []portOrphan{splash, wrapper, lovePort}, 0},
		{"a shell with nothing under it is not a helper", []portOrphan{splash, wrapper, idle}, 0},
		{"a shell whose child left the group is not a helper", []portOrphan{splash, func() portOrphan { w := wrapper; w.outKids = 1; return w }()}, 0},
		{"nothing left", nil, 0},
	} {
		procs, labels := portOrphanPick(c.left)
		if len(procs) != c.want || (c.want > 0 && !pmSplashLeft(labels)) {
			t.Errorf("%s: %d to end (%q), want %d", c.name, len(procs), labels, c.want)
		}
	}
	if !pmFdInfoCloexec([]byte("pos:\t0\nflags:\t02100002\nmnt_id:\t21\n")) || pmFdInfoCloexec([]byte("pos:\t0\nflags:\t0100002\n")) {
		t.Error("fdinfo close-on-exec flag misread")
	}
}

// JukaHub's own port installer reads zips and PortMaster's catalogue (outside
// data): an entry that could land outside its folder, or PortMaster's own
// update, refuses the zip before anything is written; port.json keeps its
// fields and gets harbourmaster's status block; runtimes are found by the
// names port.json uses, aarch64 builds and https links with an MD5 only.
func TestPortNativeInstallPlan(t *testing.T) {
	mk := func(entries map[string]string, links ...string) []*zip.File {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		names := make([]string, 0, len(entries))
		for n := range entries {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			w, err := zw.Create(n)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(entries[n]))
		}
		for _, l := range links {
			h := &zip.FileHeader{Name: l}
			h.SetMode(os.ModeSymlink | 0o777)
			w, err := zw.CreateHeader(h)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte("../../../etc"))
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
		if zr == nil {
			t.Fatalf("zip: %v", err)
		}
		return zr.File
	}
	p, err := pnPlanZip(mk(map[string]string{
		"Balatro.sh": "#!/bin/bash\n", "balatro/port.json": `{"name":"balatro.zip"}`,
		"balatro/game/data.bin": "x", "README.md": "stray", "balatro/saves/": "",
	}, "balatro/lib/libz.so"))
	if err != nil || len(p.scripts) != 1 || p.scripts[0].rel != "Balatro.sh" || strings.Join(p.dirs, ",") != "balatro" ||
		p.manifest != "balatro/port.json" || len(p.files) != 2 || p.skipped != 2 || strings.Join(p.empty, ",") != "balatro/saves" {
		t.Fatalf("plan: %+v, %v", p, err)
	}
	if !pnPathWithin("/mnt/SDCARD/Apps/PortMaster/PortMaster", "/mnt/SDCARD/apps/portmaster/portmaster/libs") ||
		pnPathWithin("/mnt/SDCARD/Data/ports", "/mnt/SDCARD/Data/ports2") {
		t.Error("pnPathWithin")
	}
	for _, bad := range []map[string]string{
		{"../evil.sh": "x"},
		{"Port.sh": "x", "port/../../x": "x"},
		{"Port.sh": "x", "/abs/x": "x"},
		{"Port.sh": "x", `port\..\..\x`: "x"},
		{"Port.sh": "x", "C:/x": "x"},
		{"PortMaster.sh": "x", "PortMaster/control.txt": "x"},
		{"Port.sh": "x", "PortMaster./control.txt": "x"},
		{"Port.sh": "x", "Imgs/Port.png": "x"},
		{"port/only.txt": "x"},
	} {
		if _, err := pnPlanZip(mk(bad)); !errors.Is(err, errPnNotAPort) {
			t.Errorf("%v: got %v, want the zip refused", bad, err)
		}
	}
	sum := strings.Repeat("ab", 16)
	out, err := pnStatusJSON([]byte(`{"version":2,"name":"balatro.zip","attr":{"title":"Balatro","runtime":["love_11.5.squashfs"],"desc":"<b>"},"status":{"note":"kept"}}`),
		"balatro.zip", strings.ToUpper(sum), nil)
	meta, ok := parsePortJSON(out)
	if err != nil || !ok || meta.MD5 != sum || meta.Title != "Balatro" || len(meta.Runtime) != 1 ||
		!strings.Contains(string(out), `"note": "kept"`) || !strings.Contains(string(out), `"source": "PortMaster"`) || !strings.Contains(string(out), `"<b>"`) {
		t.Fatalf("status block: %s", out)
	}
	out, _ = pnStatusJSON(nil, "Foo.zip", sum, []string{"Foo.sh", "foo/"})
	if meta, ok := parsePortJSON(out); !ok || meta.Zip != "foo.zip" || len(meta.Items) != 2 || meta.MD5 != sum {
		t.Fatalf("made-up manifest: %s", out)
	}
	h := func(c string) string { return strings.Repeat(c, 32) }
	idx := pnParseRuntimes([]byte(`{"ports":{},"utils":{
		"mono-6.12.0.122-aarch64.squashfs":{"md5":"` + h("1") + `","url":"https://github.com/PortsMaster/x/mono-6.12.0.122-aarch64.squashfs"},
		"frt_3.7.x86_64.squashfs":{"runtime_name":"frt_3.7","runtime_arch":"x86_64","md5":"` + h("2") + `","url":"https://x/frt_3.7.x86_64.squashfs"},
		"frt_3.7.aarch64.squashfs":{"runtime_name":"frt_3.7","runtime_arch":"aarch64","md5":"` + h("3") + `","url":"https://x/frt_3.7.aarch64.squashfs"},
		"plain.squashfs":{"md5":"` + h("4") + `","url":"http://x/plain.squashfs"},
		"short.squashfs":{"md5":"123","url":"https://x/short.squashfs"}}}`))
	for name, want := range map[string]string{
		"mono-6.12.0.122-aarch64.squashfs": h("1"), "mono-6.12.0.122-aarch64": h("1"), "frt_3.7": h("3"), "plain": "", "short.squashfs": "",
	} {
		r, ok := pnRuntimeLookup(idx, name)
		if (want == "") == ok || (ok && r.MD5 != want) {
			t.Errorf("runtime %q: %+v, %v", name, r, ok)
		}
	}
	if pnRuntimeFile("frt_3.7") != "frt_3.7.squashfs" || pnRuntimeFile("../x") != "" || pnRuntimeFile("mono.squashfs") != "mono.squashfs" {
		t.Error("runtime file names")
	}
}

// PortMaster's Python comes from python-build-standalone's SHA256SUMS: the
// chosen line decides both the file and the hash it must match.
func TestPortMasterPythonPick(t *testing.T) {
	h := func(c string) string { return strings.Repeat(c, 64) }
	sums := h("1") + "  cpython-3.11.17+20261026-aarch64-unknown-linux-gnu-install_only_stripped.tar.gz\n" +
		h("2") + "  cpython-3.11.18+20261026-aarch64-unknown-linux-gnu-install_only.tar.gz\n" +
		h("3") + " *cpython-3.11.18+20261026-aarch64-unknown-linux-gnu-install_only_stripped.tar.gz\n" +
		h("4") + "  cpython-3.11.18+20261026-x86_64-unknown-linux-gnu-install_only_stripped.tar.gz\n" +
		h("5") + "  cpython-3.11.18+20261026-aarch64-unknown-linux-musl-install_only_stripped.tar.gz\n" +
		h("6") + "  cpython-3.12.12+20261026-aarch64-unknown-linux-gnu-install_only_stripped.tar.gz\n" +
		h("7") + "  cpython-3.11.18+20261026-aarch64-unknown-linux-gnu-debug-full.tar.zst\n" +
		"nothex  cpython-3.11.99+20261026-aarch64-unknown-linux-gnu-install_only_stripped.tar.gz\n"
	a, ok := pmPythonPick([]byte(sums))
	if !ok || a.SHA256 != h("3") || a.Tag != "20261026" || a.Minor != 11 || a.Patch != 18 || !a.Stripped ||
		a.Name != "cpython-3.11.18+20261026-aarch64-unknown-linux-gnu-install_only_stripped.tar.gz" {
		t.Fatalf("picked %+v, %v", a, ok)
	}
	if a, ok := pmPythonPick([]byte(h("6") + "  cpython-3.12.12+20261026-aarch64-unknown-linux-gnu-install_only.tar.gz\n")); !ok || a.Minor != 12 {
		t.Fatalf("3.12 fallback: %+v, %v", a, ok)
	}
	if _, ok := pmPythonPick([]byte(h("8") + "  cpython-3.14.1+20261026-aarch64-unknown-linux-gnu-install_only.tar.gz\n")); ok {
		t.Fatal("an untested Python version was accepted")
	}
}

// Without TRIMUI_EX, PortMaster's launcher runs as a copy in which only the
// ex_config line is conditional and TRIMUI_EX's python3 (the whole path word)
// names the Python in use.
func TestPortMasterLauncherPatch(t *testing.T) {
	const py = "/mnt/SDCARD/Apps/JukaHub/data/portmaster-runtime/python/bin/python3"
	src := "#!/bin/bash\r\n" +
		"source /mnt/SDCARD/System/etc/ex_config\r\n" +
		"  . \"/mnt/SDCARD/System/etc/ex_config\"; # TRIMUI_EX\r\n" +
		"[ -x /mnt/SDCARD/System/bin/python3 ] && /mnt/SDCARD/System/bin/python3 ./pugwash $PORTMASTER_CMDS\r\n" +
		"ls /mnt/SDCARD/System/bin/python3.11\r\n" +
		"source $(cat /x)/System/etc/ex_config\r\n" +
		"source /mnt/SDCARD/System/etc/ex_config &\r\n" +
		"echo done\r\n"
	out, changed := pmPatchLauncher([]byte(src), py)
	want := "#!/bin/bash\r\n" +
		"if [ -f \"/mnt/SDCARD/System/etc/ex_config\" ]; then source /mnt/SDCARD/System/etc/ex_config; fi\r\n" +
		"  if [ -f \"/mnt/SDCARD/System/etc/ex_config\" ]; then . \"/mnt/SDCARD/System/etc/ex_config\"; fi\r\n" +
		"[ -x " + py + " ] && " + py + " ./pugwash $PORTMASTER_CMDS\r\n" +
		"ls /mnt/SDCARD/System/bin/python3.11\r\n" +
		"source $(cat /x)/System/etc/ex_config\r\n" +
		"source /mnt/SDCARD/System/etc/ex_config &\r\n" +
		"echo done\r\n"
	if !changed || string(out) != want {
		t.Fatalf("patched launcher:\n%q\nwant\n%q", out, want)
	}
	if _, changed := pmPatchLauncher([]byte("#!/bin/sh\nexec ./pugwash\n"), py); changed {
		t.Fatal("a launcher without TRIMUI_EX lines was changed")
	}
	if out, _ := pmPatchLauncher([]byte("/mnt/SDCARD/System/bin/python3 x\n"), ""); string(out) != "/mnt/SDCARD/System/bin/python3 x\n" {
		t.Fatalf("no Python chosen, yet the path changed: %q", out)
	}
}

// The Python archive is unpacked without links (FAT card): bin links become
// small scripts, other links copies, the test suite is left out, and a path
// out of the folder stops the install.
func TestPortMasterPythonExtract(t *testing.T) {
	build := func(entries []tar.Header, bodies map[string]string) string {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		for _, h := range entries {
			body := bodies[h.Name]
			h.Size = int64(len(body))
			if h.Typeflag != tar.TypeReg {
				h.Size = 0
			}
			if err := tw.WriteHeader(&h); err != nil {
				t.Fatal(err)
			}
			if h.Size > 0 {
				if _, err := tw.Write([]byte(body)); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(t.TempDir(), "py.tar.gz")
		if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := build([]tar.Header{
		{Name: "python/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "python/bin/python3.11", Typeflag: tar.TypeReg, Mode: 0o755},
		{Name: "python/bin/python3", Typeflag: tar.TypeSymlink, Linkname: "python3.11"},
		{Name: "python/lib/libpython3.11.so.1.0", Typeflag: tar.TypeReg, Mode: 0o755},
		{Name: "python/lib/libpython3.11.so", Typeflag: tar.TypeSymlink, Linkname: "libpython3.11.so.1.0"},
		{Name: "python/lib/python3.11/os.py", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "python/lib/python3.11/test/test_os.py", Typeflag: tar.TypeReg, Mode: 0o644},
	}, map[string]string{
		"python/bin/python3.11": "ELF", "python/lib/libpython3.11.so.1.0": "LIB",
		"python/lib/python3.11/os.py": "import sys", "python/lib/python3.11/test/test_os.py": "x",
	})
	dest := t.TempDir()
	if err := pmPythonExtract(context.Background(), good, dest); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "bin", "python3")); err != nil || !strings.Contains(string(b), `exec "$(dirname "$0")/python3.11" "$@"`) {
		t.Fatalf("bin/python3: %q %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "lib", "libpython3.11.so")); err != nil || string(b) != "LIB" {
		t.Fatalf("library link copy: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "lib", "python3.11", "test")); !os.IsNotExist(err) {
		t.Fatal("the test suite was unpacked")
	}
	// The install adds the PYSDL2_DLL_PATH guard beside the standard library;
	// a second call leaves it as it is.
	for i := 0; i < 2; i++ {
		if err := pmPythonEnsureSite(dest); err != nil {
			t.Fatal(err)
		}
	}
	if b, err := os.ReadFile(filepath.Join(dest, "lib", "python3.11", "site-packages", "sitecustomize.py")); err != nil || string(b) != pmPythonSiteText {
		t.Fatalf("sitecustomize.py: %v", err)
	}
	bad := build([]tar.Header{{Name: "python/../../escape", Typeflag: tar.TypeReg, Mode: 0o644}}, map[string]string{"python/../../escape": "x"})
	if err := pmPythonExtract(context.Background(), bad, t.TempDir()); err == nil {
		t.Fatal("a path out of the folder was accepted")
	}
}
