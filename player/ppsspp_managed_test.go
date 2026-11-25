package main

// ppsspp_managed_test.go — the rules JukaHub's own PPSSPP must keep: no build
// without a digest, the right binary for the model, a launcher that shares the
// firmware's saves and falls back when the build does not start, a launcher
// file restored exactly, and never a deleted folder that still leads into saves.

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"
)

// ppssppTestEnv points the manager at a temporary data folder and forgets any
// loaded state; everything is put back when the test ends.
func ppssppTestEnv(t *testing.T) (root, overrides string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "data", "emulators", "ppsspp")
	overrides = filepath.Join(base, "data", "game-launchers.json")
	prevRoot, prevOverrides := ppssppManagedRootFn, ppssppOverridesPathFn
	prevCard, prevEmu := ppssppCardRootFn, ppssppEmuRootsFn
	prevLogin := ppssppRALoginFn
	ppssppManagedRootFn = func() string { return root }
	ppssppOverridesPathFn = func() string { return overrides }
	ppssppCardRootFn = func() string { return "" }
	ppssppEmuRootsFn = func() []string { return nil }
	ppssppRALoginFn = func() (string, string) { return "", "" }
	ppssppResetForTest()
	t.Cleanup(func() {
		ppssppManagedRootFn, ppssppOverridesPathFn = prevRoot, prevOverrides
		ppssppCardRootFn, ppssppEmuRootsFn = prevCard, prevEmu
		ppssppRALoginFn = prevLogin
		ppssppResetForTest()
	})
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	return root, overrides
}

// ppssppFakeELF is a file that starts like an executable and carries PPSSPP's
// version string among a library's (which must not be taken for it).
func ppssppFakeELF(version string) string {
	return "\x7fELFjunk\x00GLIBC_2.17\x00zlib v1.2.11\x00v" + version + "\x00tail"
}

// ppssppFiles builds a file set from name, content pairs.
func ppssppFiles(pairs ...string) map[string][]byte {
	files := make(map[string][]byte, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		files[pairs[i]] = []byte(pairs[i+1])
	}
	return files
}

func ppssppWriteFiles(t *testing.T, dir string, files map[string][]byte) {
	t.Helper()
	for name, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func ppssppTestZip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(files[n]); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// ppssppTestPak is a package laid out the way minui-psp's launch.sh describes
// it: the binaries and assets in PSP.pak/PPSSPP, libraries in PSP.pak/lib.
func ppssppTestPak(version string) map[string][]byte {
	return ppssppFiles(
		"PSP.pak/launch.sh", "#!/bin/sh\n",
		"PSP.pak/lib/libstdc++.so.6", "lib",
		"PSP.pak/PPSSPP/PPSSPPSDL_tg5040", ppssppFakeELF(version),
		"PSP.pak/PPSSPP/PPSSPPSDL_tg5050", ppssppFakeELF(version),
		"PSP.pak/PPSSPP/assets/ppge_atlas.zim", "atlas",
		"PSP.pak/PPSSPP/assets/gamecontrollerdb.txt", "pads",
		"PSP.pak/PPSSPP/assets/lib/libnot.so", "data",
		"PSP.pak/PPSSPP/.config/ppsspp/PSP/SYSTEM/ppsspp.ini", "[General]\n",
	)
}

func ppssppDigestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestPPSSPPReleaseRequiresDigest(t *testing.T) {
	digest := ppssppDigestOf([]byte("pak"))
	release := func(asset string) []byte {
		return []byte(`{"tag_name":"6.2.0","name":"6.2.0","body":"* chore: update ppsspp 1.20.3 by @ben16w in #81\n* fix: bundle libstdc++.so.6 for PPSSPP by @ben16w in #79","assets":[` + asset + `]}`)
	}
	good := `{"name":"PSP.pak.zip","browser_download_url":"https://github.com/ben16w/minui-psp/releases/download/6.2.0/PSP.pak.zip","size":52428800,"digest":"` + strings.ToUpper(digest[:7]) + digest[7:] + `"}`
	rel, err := ppssppReleaseFrom(release(`{"name":"PSP.pak.zip.sha256","browser_download_url":"https://example.invalid/x","size":1,"digest":"` + digest + `"},` + good))
	if err != nil {
		t.Fatalf("release with a digest refused: %v", err)
	}
	if rel.Tag != "6.2.0" || rel.Version != "1.20.3" || rel.Asset != "PSP.pak.zip" || rel.Digest != strings.ToLower(digest) {
		t.Fatalf("release = %+v", rel)
	}
	refused := []struct{ name, asset string }{
		{"no digest", `{"name":"PSP.pak.zip","browser_download_url":"https://github.com/x/PSP.pak.zip","size":10}`},
		{"md5 digest", `{"name":"PSP.pak.zip","browser_download_url":"https://github.com/x/PSP.pak.zip","size":10,"digest":"md5:0123"}`},
		{"short digest", `{"name":"PSP.pak.zip","browser_download_url":"https://github.com/x/PSP.pak.zip","size":10,"digest":"sha256:abcd"}`},
		{"too large", `{"name":"PSP.pak.zip","browser_download_url":"https://github.com/x/PSP.pak.zip","size":999999999999,"digest":"` + digest + `"}`},
		{"no pak", `{"name":"PSP.pak.tar.gz","browser_download_url":"https://github.com/x/PSP.pak.tar.gz","size":10,"digest":"` + digest + `"}`},
	}
	for _, c := range refused {
		if _, err := ppssppReleaseFrom(release(c.asset)); err == nil {
			t.Errorf("%s: release accepted", c.name)
		}
	}
	if _, err := ppssppReleaseFrom([]byte(`{"tag_name":"6.2.0","draft":true,"assets":[` + good + `]}`)); err == nil {
		t.Error("draft release accepted")
	}

	// JukaHub's own rolling release: version from the title only, tag per version.
	ownAsset := `{"name":"PPSSPP-tg5040.zip","browser_download_url":"https://github.com/jukaLang/JukaHubV2/releases/download/ppsspp-tg5040/PPSSPP-tg5040.zip","size":30000000,"digest":"` + digest + `"}`
	own, err := ppssppOwnReleaseFrom([]byte(`{"tag_name":"ppsspp-tg5040","name":"PPSSPP 1.20.4 for TrimUI (tg5040)","body":"SDL2 2.26.1; GPL-2.0","assets":[` + ownAsset + `]}`))
	if err != nil || own.Tag != "jukahub-1.20.4-"+digest[7:15] || own.Version != "1.20.4" || own.Asset != "PPSSPP-tg5040.zip" {
		t.Fatalf("own release = %+v, %v", own, err)
	}
	// A build with the save on request says so in its notes (BUILDINFO's
	// Resume line, ppsspp_resume.go); one without does not claim it.
	resumable, err := ppssppOwnReleaseFrom([]byte(`{"tag_name":"ppsspp-tg5040","name":"PPSSPP 1.20.4 for TrimUI (tg5040)","body":"Resume:    save-on-request (JukaHub's change)","assets":[` + ownAsset + `]}`))
	if err != nil || !resumable.Resume || own.Resume {
		t.Errorf("save on request: with %+v (%v), without %t", resumable, err, own.Resume)
	}
	for _, bad := range []string{
		`{"tag_name":"ppsspp-tg5040","name":"PPSSPP for TrimUI","assets":[` + ownAsset + `]}`,
		`{"tag_name":"ppsspp-tg5040","name":"PPSSPP 1.20.4 for TrimUI (tg5040)","prerelease":true,"assets":[` + ownAsset + `]}`,
		`{"tag_name":"ppsspp-tg5040","name":"PPSSPP 1.20.4 for TrimUI (tg5040)","assets":[` + good + `]}`,
	} {
		if _, err := ppssppOwnReleaseFrom([]byte(bad)); err == nil {
			t.Errorf("own release accepted: %s", bad)
		}
	}
	pak := ppssppRelease{Tag: "6.2.0", Version: "1.20.3"}
	for _, c := range []struct {
		name      string
		pak, own  ppssppRelease
		installed string
		want      string
	}{
		{"own newer", pak, own, "", own.Tag},
		{"pak newer", ppssppRelease{Tag: "6.3.0", Version: "1.20.5"}, own, "", "6.3.0"},
		{"pak unknown", ppssppRelease{Tag: "6.3.0"}, own, "", own.Tag},
		{"tie, nothing installed", ppssppRelease{Tag: "6.3.0", Version: "1.20.4"}, own, "", "6.3.0"},
		{"tie, own installed", ppssppRelease{Tag: "6.3.0", Version: "1.20.4"}, own, own.Tag, own.Tag},
		{"tie, an earlier build of own installed", ppssppRelease{Tag: "6.3.0", Version: "1.20.4"}, own, "jukahub-1.20.4-0badc0de", own.Tag},
		{"own unknown", pak, ppssppRelease{}, "", "6.2.0"},
	} {
		if got := ppssppPickRelease(c.pak, c.own, c.installed); got.Tag != c.want {
			t.Errorf("pick %s: %s, want %s", c.name, got.Tag, c.want)
		}
	}
}

func TestPPSSPPVersions(t *testing.T) {
	notes := []struct{ text, want string }{
		{"chore: update ppsspp 1.20.3 by @ben16w in #81", "1.20.3"},
		{"Bump PPSSPP to v1.20.4 (was 1.20.3)", "1.20.4"},
		{"fix: bundle libstdc++.so.6 for PPSSPP by @ben16w in #79", ""},
		{"PPSSPP-spruce v1.20.3 for tg5040; PPSSPP 1.19.3 before", "1.20.3"},
	}
	for _, c := range notes {
		if got := ppssppVersionFromNotes(c.text); got != c.want {
			t.Errorf("notes %q: version %q, want %q", c.text, got, c.want)
		}
	}
	history := []byte(`[{"tag_name":"6.3.0","body":"update ppsspp 1.20.5"},` +
		`{"tag_name":"6.2.0","body":"chore(deps): bump actions/checkout"},` +
		`{"tag_name":"6.1.1-rc","prerelease":true,"body":"try ppsspp 1.20.9"},` +
		`{"tag_name":"6.1.0","body":"fix: bundle libstdc++.so.6 for PPSSPP"},` +
		`{"tag_name":"6.0.0","body":"feat: update ppsspp 1.20.3 by @ben16w in #67"},` +
		`{"tag_name":"5.0.0","body":"ppsspp 1.19.3"}]`)
	for _, c := range []struct{ tag, want string }{
		{"6.2.0", "1.20.3"}, // carried from 6.0.0, past a pre-release and a fix
		{"6.3.0", "1.20.5"}, // its own notes
		{"7.0.0", ""},       // not listed
	} {
		if got := ppssppVersionFromHistory(history, c.tag); got != c.want {
			t.Errorf("history for %s: %q, want %q", c.tag, got, c.want)
		}
	}
	if got := ppssppVersionFromHistory([]byte(`{"message":"rate limited"}`), "6.2.0"); got != "" {
		t.Errorf("history from an error body: %q", got)
	}
	names := []struct{ name, want string }{
		{"ppsspp_1.17.1_gl.sh", "1.17.1"},
		{"PPSSPP_1.18.1", "1.18.1"},
		{"ppsspp_1.19.0.sh", "1.19.0"},
		{"launch.sh", ""},
		{"PPSSPPSDL_tg5040", ""},
	}
	for _, c := range names {
		if got := ppssppVersionFromName(c.name); got != c.want {
			t.Errorf("name %q: version %q, want %q", c.name, got, c.want)
		}
	}
	if !ppssppNewer("1.20.3", "1.18.1") || ppssppNewer("1.9", "1.17.1") || ppssppNewer("", "1.0") || ppssppNewer("1.18.1", "1.18.1") {
		t.Error("version comparison is wrong")
	}
	if compareCoreVersions("1.9", "1.17.1") >= 0 {
		t.Error("1.9 must sort below 1.17.1")
	}
}

func TestPPSSPPScanVersion(t *testing.T) {
	cases := []struct{ data, want string }{
		{"\x00zlib v1.2.11\x00v1.20.3\x00", "1.20.3"},
		{"x\x00v1.17\x00y", "1.17"},
		{"\x00v1.18.1-23-gabcdef0\x00", "1.18.1"},
		{"\x00dev1.20.3\x00", ""},
		{"\x00v1.20.3 and more", ""},
		{"\x00v1.19.3\x00junk\x00v1.20.1\x00x", "1.20.1"},
		{"\x00v1.200.1\x00\x00v1.5.1\x00\x00", ""},
		{strings.Repeat("a", 5000) + "\x00v1.18.1\x00", "1.18.1"},
	}
	for _, c := range cases {
		tail := c.data[max(0, len(c.data)-40):]
		if got := ppssppScanVersion(strings.NewReader(c.data)); got != c.want {
			t.Errorf("scan %q: %q, want %q", tail, got, c.want)
		}
		// A string cut by every read is still found.
		if got := ppssppScanVersion(iotest.OneByteReader(strings.NewReader(c.data))); got != c.want {
			t.Errorf("one-byte scan %q: %q, want %q", tail, got, c.want)
		}
	}
}

func TestPPSSPPDiscoverLayout(t *testing.T) {
	dir := t.TempDir()
	ppssppWriteFiles(t, dir, ppssppTestPak("1.20.3"))
	for _, platform := range []string{"tg5040", "tg5050"} {
		layout, err := ppssppDiscoverLayout(dir, platform)
		if err != nil {
			t.Fatalf("%s: %v", platform, err)
		}
		want := ppssppLayout{
			Binary:       "PSP.pak/PPSSPP/PPSSPPSDL_" + platform,
			Work:         "PSP.pak/PPSSPP",
			Home:         "PSP.pak/PPSSPP",
			Assets:       "PSP.pak/PPSSPP/assets",
			ControllerDB: "PSP.pak/PPSSPP/assets/gamecontrollerdb.txt",
			Libs:         []string{"PSP.pak/lib"},
		}
		gotJSON, _ := json.Marshal(layout)
		wantJSON, _ := json.Marshal(want)
		if !bytes.Equal(gotJSON, wantJSON) {
			t.Errorf("%s layout = %s, want %s", platform, gotJSON, wantJSON)
		}
	}
	if _, err := ppssppDiscoverLayout(dir, "tg3040"); err == nil {
		t.Error("a package without a build for the model was accepted")
	}

	// Per-platform folders with bare names: the model's own folder wins and
	// another model's binary is never taken.
	bins := t.TempDir()
	ppssppWriteFiles(t, bins, ppssppFiles(
		"bin/tg5040/PPSSPPSDL", ppssppFakeELF("1.20.3"),
		"bin/tg5050/PPSSPPSDL", ppssppFakeELF("1.20.3"),
		"PPSSPP/assets/compat.ini", "[x]",
		"PPSSPP/assets/flash0/font", "f",
	))
	layout, err := ppssppDiscoverLayout(bins, "tg5050")
	if err != nil || layout.Binary != "bin/tg5050/PPSSPPSDL" || layout.Work != "PPSSPP" {
		t.Errorf("per-platform layout = %+v, %v", layout, err)
	}

	// A file named like the binary that is not an executable is not one.
	text := t.TempDir()
	ppssppWriteFiles(t, text, ppssppFiles(
		"PPSSPPSDL_tg5040", "#!/bin/sh\necho no\n",
		"assets/ppge_atlas.zim", "atlas",
		"lib/libSDL2-2.0.so.0", "lib",
		"PPSSPPSDL_tg5040.txt", "notes",
		"docs/PPSSPPSDL_tg5040.md", "notes",
	))
	if _, err := ppssppDiscoverLayout(text, "tg5040"); err == nil {
		t.Error("a script named PPSSPPSDL_tg5040 was taken for the binary")
	}
}

func TestPPSSPPInspectZipRefusesEscapes(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, files map[string][]byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, ppssppTestZip(t, files), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := write("good.zip", ppssppTestPak("1.20.3"))
	if n, err := ppssppInspectZip(good); err != nil || n <= 0 {
		t.Fatalf("good package: %d, %v", n, err)
	}
	bad := []struct {
		name  string
		files map[string][]byte
	}{
		{"slip.zip", ppssppFiles("PSP.pak/../../evil.sh", "x")},
		{"absolute.zip", ppssppFiles("/etc/evil", "x")},
		{"twice.zip", ppssppFiles("PSP.pak/A.txt", "x", "psp.pak/a.TXT", "y")},
	}
	for _, c := range bad {
		if _, err := ppssppInspectZip(write(c.name, c.files)); err == nil {
			t.Errorf("%s accepted", c.name)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.sh")); err == nil {
		t.Error("an entry was written outside the folder")
	}
}

func TestPPSSPPLauncherScript(t *testing.T) {
	spec := ppssppScriptSpec{
		Build:        "/mnt/SDCARD/Apps/JukaHub/data/emulators/ppsspp/6.2.0",
		Binary:       "/mnt/SDCARD/Apps/JukaHub/data/emulators/ppsspp/6.2.0/PSP.pak/PPSSPP/PPSSPPSDL_tg5040",
		Work:         "/mnt/SDCARD/Apps/JukaHub/data/emulators/ppsspp/6.2.0/PSP.pak/PPSSPP",
		Home:         "/mnt/SDCARD/Apps/JukaHub/data/emulators/ppsspp/6.2.0/PSP.pak/PPSSPP",
		Platform:     "tg5040",
		Libs:         []string{"/mnt/SDCARD/Apps/JukaHub/data/emulators/ppsspp/6.2.0/PSP.pak/lib"},
		ControllerDB: "/mnt/SDCARD/Apps/JukaHub/data/emulators/ppsspp/6.2.0/PSP.pak/PPSSPP/assets/gamecontrollerdb.txt",
		Stick:        "/mnt/SDCARD/Emus/PSP/PPSSPP_1.17.1/.config/ppsspp/PSP",
		Fallback:     "/mnt/SDCARD/Emus/PSP/ppsspp_1.17.1_gl.sh",
		Version:      "1.20.3",
		Tag:          "6.2.0\nrm -rf /",
	}
	text := ppssppLauncherScript(spec)
	for _, want := range []string{
		"#!/bin/sh\n",
		"FALLBACK='/mnt/SDCARD/Emus/PSP/ppsspp_1.17.1_gl.sh'\n",
		"FIRMWARE_STICK='/mnt/SDCARD/Emus/PSP/PPSSPP_1.17.1/.config/ppsspp/PSP'\n",
		"PPSSPP_LIBS='/mnt/SDCARD/Apps/JukaHub/data/emulators/ppsspp/6.2.0/PSP.pak/lib:/usr/trimui/lib'\n",
		`mount --bind "$1" "$2"`,
		`bind "$FIRMWARE_STICK/SAVEDATA" "$STICK/SAVEDATA" || firmware`,
		`bind "$FIRMWARE_STICK/PPSSPP_STATE" "$STICK/PPSSPP_STATE" || firmware`,
		"trap 'unbind; exit 130' INT",
		`"$PPSSPP_BIN" --pause-menu-exit "$@"`,
		`SDL_VIDEODRIVER=mali HOME="$PPSSPP_HOME"`,
		`"$@" --fullscreen`,
		`[ "$ELAPSED" -lt 10 ]`,
		`HOME="$PPSSPP_HOME" XDG_CONFIG_HOME="$PPSSPP_HOME/.config" LD_LIBRARY_PATH="$LIBS"`,
		`exec /bin/sh "$FALLBACK" "$@"`,
		`LIBS="$PPSSPP_LIBS:/usr/lib:/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"`,
		`if (: >"$LOG") 2>/dev/null; then`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("script lacks %q", want)
		}
	}
	if strings.Contains(text, "\r") || strings.Contains(text, "\nrm -rf /") {
		t.Error("script has a CR, or a value from the network broke out of its comment")
	}
	// The build's HOME and libraries are set on PPSSPP's command alone: the
	// firmware's launcher, when the game falls back, must not inherit them.
	if strings.Contains(text, "export ") {
		t.Error("script exports into its own environment")
	}
	// A launcher that mentions RetroArch is configured as one at launch
	// (savestate_resume.go); this one must never read as that.
	if strings.Contains(strings.ToLower(text), "retroarch") {
		t.Error("script mentions RetroArch")
	}
	// Not even when the firmware's launcher or the tag names it.
	marked := spec
	marked.Fallback = "/mnt/SDCARD/RetroArch/ra64.trimui_PSP.sh"
	marked.Tag = "RetroArch_libretro.ra32."
	markedText := ppssppLauncherScript(marked)
	for _, m := range ppssppScriptMarkers {
		if strings.Contains(strings.ToLower(markedText), m) {
			t.Errorf("script with a RetroArch fallback holds %q", m)
		}
	}
	sh, err := exec.LookPath("sh")
	if err == nil {
		parse := exec.Command(sh, "-n")
		parse.Stdin = strings.NewReader(text)
		if out, parseErr := parse.CombinedOutput(); parseErr != nil {
			t.Fatalf("sh -n: %v\n%s", parseErr, out)
		}
	}
	if runtime.GOOS == "windows" || err != nil {
		t.Skip("no POSIX sh to parse the script")
	}
	// The split marker is still the same path to the shell.
	for _, l := range strings.Split(markedText, "\n") {
		if strings.HasPrefix(l, "FALLBACK=") {
			out, err := exec.Command(sh, "-c", l+`; printf %s "$FALLBACK"`).CombinedOutput()
			if err != nil || string(out) != marked.Fallback {
				t.Errorf("FALLBACK reads %q (%v), want %q", out, err, marked.Fallback)
			}
		}
	}
	spec.Build = "/tmp/it's here"
	file := filepath.Join(t.TempDir(), "launcher.sh")
	if err := os.WriteFile(file, []byte(ppssppLauncherScript(spec)), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(sh, "-n", file).CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v\n%s", err, out)
	}
}

// TestPPSSPPLauncherFallsBack runs the script with stand-ins for mount, umount,
// PPSSPP and the firmware's launcher: a build that exits with an error at once
// is marked bad and the game goes to the firmware's launcher, with the
// environment JukaHub gave the script (never the build's HOME); a clean exit
// does neither; and a build marked bad is not started again.
func TestPPSSPPLauncherFallsBack(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if runtime.GOOS == "windows" || err != nil {
		t.Skip("no POSIX sh to run the script")
	}
	dir := t.TempDir()
	fakeBin := filepath.Join(dir, "bin")
	marker := filepath.Join(dir, "fallback-ran")
	played := filepath.Join(dir, "ppsspp-ran")
	ppssppWriteFiles(t, dir, ppssppFiles(
		"bin/mount", "#!/bin/sh\nexit 0\n",
		"bin/umount", "#!/bin/sh\nexit 0\n",
		"build/PPSSPP/fail", "#!/bin/sh\necho 'no EGL display' >&2\nexit 3\n",
		"build/PPSSPP/ok", "#!/bin/sh\nprintf '%s|%s' \"$*\" \"$HOME\" >"+shellQuote(played)+"\nexit 0\n",
		"firmware/fallback.sh", "#!/bin/sh\nprintf '%s|%s' \"$*\" \"$HOME\" >"+shellQuote(marker)+"\n",
		"stick/SYSTEM/ppsspp.ini", "[General]\n",
	))
	// The fallback gets the ROM in MainUI's form, through the interpreter its
	// first line names.
	fallbackSaw := "/card/Emus/PSP/../../Roms/Game.iso|/original/home"
	withFirmware := true
	run := func(binary string) {
		t.Helper()
		spec := ppssppScriptSpec{
			Build:   filepath.Join(dir, "build"),
			Binary:  filepath.Join(dir, "build", "PPSSPP", binary),
			Work:    filepath.Join(dir, "build", "PPSSPP"),
			Home:    filepath.Join(dir, "home"),
			Version: "1.20.3",
			Tag:     "6.2.0",
		}
		if withFirmware {
			spec.Stick = filepath.Join(dir, "stick")
			spec.Fallback = filepath.Join(dir, "firmware", "fallback.sh")
			spec.FallbackShell = sh
			spec.FallbackRoms = "/card/Roms"
			spec.FallbackRomsAs = "/card/Emus/PSP/../../Roms"
		}
		script := filepath.Join(dir, "build", ppssppScriptName)
		if err := os.WriteFile(script, []byte(ppssppLauncherScript(spec)), 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(sh, script, "/card/Roms/Game.iso")
		cmd.Env = append(os.Environ(), "HOME=/original/home", "PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
		_ = cmd.Run()
	}
	bad := filepath.Join(dir, "build", ppssppBadName)

	run("ok")
	if regularFile(bad) || regularFile(marker) {
		t.Fatal("a clean exit marked the build bad or ran the fallback")
	}
	if got, _ := os.ReadFile(played); string(got) != "--pause-menu-exit /card/Roms/Game.iso --fullscreen|"+filepath.Join(dir, "home") {
		t.Fatalf("PPSSPP ran as %q", got)
	}
	run("fail")
	reason, isBad := ppssppReadFirstLine(bad)
	if !isBad || !strings.Contains(reason, "exited with status 3") || !strings.Contains(reason, "no EGL display") {
		t.Fatalf("bad marker = %q, %v", reason, isBad)
	}
	if got, _ := os.ReadFile(marker); string(got) != fallbackSaw {
		t.Fatalf("fallback got %q, want %q", got, fallbackSaw)
	}
	_ = os.Remove(marker)
	run("ok")
	if got, _ := os.ReadFile(marker); string(got) != fallbackSaw {
		t.Fatal("a build marked bad was started instead of the firmware's launcher")
	}
	b := ppssppBuild{Dir: filepath.Join(dir, "build")}
	if reason := ppssppFallbackReason(ppssppLastStatusLine(b)); !strings.Contains(reason, "did not start last time") {
		t.Errorf("last status reason = %q", reason)
	}

	// A card with no firmware PPSSPP at all: nothing to share, nothing to fall
	// back to, so the build plays with the saves in its own memstick.
	_ = os.Remove(bad)
	_ = os.Remove(played)
	withFirmware = false
	run("ok")
	if got, _ := os.ReadFile(played); string(got) != "--pause-menu-exit /card/Roms/Game.iso --fullscreen|"+filepath.Join(dir, "home") {
		t.Errorf("without a firmware PPSSPP the build ran as %q", got)
	}
	if !dirExists(filepath.Join(dir, "home", ".config", "ppsspp", "PSP", "SAVEDATA")) {
		t.Error("the build's own SAVEDATA was not created")
	}
}

func TestPPSSPPOverrideMergeAndRestore(t *testing.T) {
	root, file := ppssppTestEnv(t)
	original := "{\n  \"SNES\": \"/mnt/SDCARD/Emus/SFC/launch.sh\",\n  \"playstation portable\": \"/mnt/SDCARD/Emus/PSP/old.sh\"\n}\n"
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(root, "6.2.0", ppssppScriptName)
	second := filepath.Join(root, "6.3.0", ppssppScriptName)
	point := func(launcher string) {
		t.Helper()
		ppssppMgr.mu.Lock()
		defer ppssppMgr.mu.Unlock()
		if err := ppssppPointPSPLocked(ppssppLockedState(), launcher); err != nil {
			t.Fatal(err)
		}
	}
	point(first)
	point(second)
	raw := map[string]string{}
	data, _ := os.ReadFile(file)
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["PlayStation Portable"] != second || raw["SNES"] != "/mnt/SDCARD/Emus/SFC/launch.sh" || len(raw) != 2 {
		t.Fatalf("launcher file = %v", raw)
	}
	if got := loadGameLauncherOverrides(file)["playstation portable"]; got != second {
		t.Fatalf("the library reads %q for PSP", got)
	}
	// Pointing alone does not publish (its callers save), so read under the lock.
	ppssppMgr.mu.Lock()
	st := ppssppLockedState().clone()
	ppssppMgr.mu.Unlock()
	if len(st.Replaced) != 1 || st.Replaced["playstation portable"] != `"/mnt/SDCARD/Emus/PSP/old.sh"` {
		t.Fatalf("remembered %v, want only the user's entry", st.Replaced)
	}

	ppssppMgr.mu.Lock()
	err := ppssppRestorePSPLocked(ppssppLockedState())
	ppssppMgr.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	back := loadGameLauncherOverrides(file)
	if back["playstation portable"] != "/mnt/SDCARD/Emus/PSP/old.sh" || back["snes"] != "/mnt/SDCARD/Emus/SFC/launch.sh" || len(back) != 2 {
		t.Fatalf("restored file = %v", back)
	}
	raw = map[string]string{}
	data, _ = os.ReadFile(file)
	_ = json.Unmarshal(data, &raw)
	if _, ok := raw["playstation portable"]; !ok {
		t.Errorf("the entry came back under another key: %v", raw)
	}
}

func TestPPSSPPOverrideRestoreRemovesCreatedFile(t *testing.T) {
	root, file := ppssppTestEnv(t)
	ppssppMgr.mu.Lock()
	err := ppssppPointPSPLocked(ppssppLockedState(), filepath.Join(root, "6.2.0", ppssppScriptName))
	ppssppMgr.mu.Unlock()
	if err != nil || !regularFile(file) {
		t.Fatalf("point: %v", err)
	}
	ppssppMgr.mu.Lock()
	err = ppssppRestorePSPLocked(ppssppLockedState())
	ppssppMgr.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if regularFile(file) {
		t.Error("the launcher file JukaHub created is still there with nothing in it")
	}
}

// TestPPSSPPOverrideKeepsOrder: a hand-ordered launcher file keeps its order
// through a switch, with the PSP entry in its place, and comes back byte for
// byte when PSP games go back to the firmware's PPSSPP.
func TestPPSSPPOverrideKeepsOrder(t *testing.T) {
	root, file := ppssppTestEnv(t)
	original := "{\n  \"SNES\": \"/s.sh\",\n  \"playstation portable\": \"/p.sh\",\n  \"GBA\": \"/g.sh\"\n}\n"
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	ppssppMgr.mu.Lock()
	err := ppssppPointPSPLocked(ppssppLockedState(), filepath.Join(root, "6.2.0", ppssppScriptName))
	ppssppMgr.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(file)
	if got := strings.Join(ppssppObjectKeys(data), ","); got != "SNES,PlayStation Portable,GBA" {
		t.Errorf("keys after the switch = %s", got)
	}
	ppssppMgr.mu.Lock()
	err = ppssppRestorePSPLocked(ppssppLockedState())
	ppssppMgr.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if data, _ = os.ReadFile(file); string(data) != original {
		t.Errorf("restored file =\n%s\nwant\n%s", data, original)
	}
}

func TestPPSSPPOverrideKeepsHandPinAfterUs(t *testing.T) {
	root, file := ppssppTestEnv(t)
	ppssppMgr.mu.Lock()
	err := ppssppPointPSPLocked(ppssppLockedState(), filepath.Join(root, "6.2.0", ppssppScriptName))
	ppssppMgr.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`{"PlayStation Portable": "/mnt/SDCARD/Emus/PSP/mine.sh"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ppssppMgr.mu.Lock()
	pinned := ppssppUserPinnedLocked()
	err = ppssppRestorePSPLocked(ppssppLockedState())
	ppssppMgr.mu.Unlock()
	if err != nil || !pinned {
		t.Fatalf("pinned %v, restore %v", pinned, err)
	}
	if got := loadGameLauncherOverrides(file)["playstation portable"]; got != "/mnt/SDCARD/Emus/PSP/mine.sh" {
		t.Errorf("a pin written by hand was replaced: %q", got)
	}
}

func TestPPSSPPOverrideRefusesBrokenFile(t *testing.T) {
	root, file := ppssppTestEnv(t)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`{"SNES": `), 0o644); err != nil {
		t.Fatal(err)
	}
	ppssppMgr.mu.Lock()
	err := ppssppPointPSPLocked(ppssppLockedState(), filepath.Join(root, "6.2.0", ppssppScriptName))
	ppssppMgr.mu.Unlock()
	if err == nil {
		t.Fatal("a launcher file that is not JSON was rewritten")
	}
	if data, _ := os.ReadFile(file); string(data) != `{"SNES": ` {
		t.Errorf("file changed to %q", data)
	}
}

// The user's own PSP entry is in state.json before game-launchers.json loses
// it, so a power cut between the two writes cannot leave it only in memory —
// and a build gone back from stays recorded across a restart.
func TestPPSSPPStateIsWrittenBeforeTheLauncherFile(t *testing.T) {
	root, file := ppssppTestEnv(t)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`{"PlayStation Portable": "/mnt/SDCARD/Emus/PSP/mine.sh"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	older := ppssppTestBuild(t, root, "6.1.0", "1.20.2")
	newer := ppssppTestBuild(t, root, "6.2.0", "1.20.3")
	fw := ppssppFirmwareInfo{Launcher: "/mnt/SDCARD/Emus/PSP/launch.sh", Version: "1.18.1", Stick: filepath.Join(filepath.Dir(root), "stick")}
	ppssppMgr.mu.Lock()
	st := ppssppLockedState()
	st.Builds = []ppssppBuild{newer, older}
	err := ppssppUseBuildLocked(st, older, fw)
	ppssppMgr.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// No caller saved: what is on the card is what the switch wrote first.
	var onCard ppssppState
	if data, err := os.ReadFile(ppssppStatePath()); err != nil || json.Unmarshal(data, &onCard) != nil {
		t.Fatalf("state.json: %v", err)
	}
	if !onCard.Override || onCard.Active != "6.1.0" || onCard.Replaced["PlayStation Portable"] != `"/mnt/SDCARD/Emus/PSP/mine.sh"` {
		t.Fatalf("state on the card = %+v, want the user's entry and the build it points at", onCard)
	}

	ppssppMgr.mu.Lock()
	st = ppssppLockedState()
	err = ppssppUseBuildLocked(st, newer, fw)
	if err == nil {
		err = ppssppSaveLocked()
	}
	ppssppMgr.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := ppssppGoBackTarget(ppssppSnapshot()); !ok || got.Tag != "6.1.0" {
		t.Fatalf("go-back target = %q, %v; want the build used before", got.Tag, ok)
	}
	from, to, err := ppssppGoBack("6.1.0", fw)
	if err != nil || from.Tag != "6.2.0" || to.Tag != "6.1.0" {
		t.Fatalf("go back = %q → %q, %v", from.Tag, to.Tag, err)
	}
	ppssppResetForTest()
	if snap := ppssppSnapshot(); snap.Active != "6.1.0" || snap.RolledBack != "6.2.0" {
		t.Fatalf("after a restart: active %q, rolled back from %q", snap.Active, snap.RolledBack)
	}
	if got := loadGameLauncherOverrides(file)["playstation portable"]; got != older.script() {
		t.Fatalf("PSP games resolve to %q, want the build gone back to", got)
	}
}

// The mount and process checks match a folder reached through a link: the
// mount table names the real path.
func TestPPSSPPMountChecksResolveLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need privileges on Windows")
	}
	base := t.TempDir()
	realDir := filepath.Join(base, "real")
	stick := filepath.Join(realDir, "home", ".config", "ppsspp", "PSP", "SAVEDATA")
	if err := os.MkdirAll(stick, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	realStick, err := filepath.EvalSymlinks(stick)
	if err != nil {
		t.Fatal(err)
	}
	mounts := "/dev/mmcblk0p1 " + strings.ReplaceAll(filepath.ToSlash(realStick), " ", `\040`) + " vfat rw 0 0\n"
	if got := ppssppMountPointsFor(mounts, link); len(got) != 1 {
		t.Fatalf("mounts within the linked folder = %v, want the bind under its real path", got)
	}
	if got := ppssppResolvePath(filepath.Join(link, "builds", "not-yet")); !strings.HasSuffix(got, filepath.Join("real", "builds", "not-yet")) {
		t.Errorf("a path not created yet resolves to %q", got)
	}
	// The Brick's aspect ratio replaces only a value the pak itself writes.
	for _, tc := range []struct {
		cur   string
		found bool
		want  string
		set   bool
	}{
		{"", false, "0.848000", true},
		{"1.000000", true, "0.848000", true},
		{"0.848000", true, "1.000000", true},
		{"", false, "1.000000", false},
		{"0.750000", true, "0.848000", false},
		{"0.848", true, "0.848000", false},
	} {
		if got := ppssppAspectNeedsSet(tc.cur, tc.found, tc.want); got != tc.set {
			t.Errorf("aspect %q (found %v) → %s: set = %v, want %v", tc.cur, tc.found, tc.want, got, tc.set)
		}
	}
}

// ppssppTestBuild lays down an installed build.
func ppssppTestBuild(t *testing.T, root, tag, version string) ppssppBuild {
	t.Helper()
	dir := filepath.Join(root, tag)
	ppssppWriteFiles(t, dir, ppssppFiles("PSP.pak/PPSSPP/PPSSPPSDL_tg5040", ppssppFakeELF(version)))
	return ppssppBuild{
		Tag:      tag,
		Version:  version,
		Platform: "tg5040",
		Dir:      dir,
		Layout:   ppssppLayout{Binary: "PSP.pak/PPSSPP/PPSSPPSDL_tg5040", Work: "PSP.pak/PPSSPP", Home: "PSP.pak/PPSSPP", Assets: "PSP.pak/PPSSPP/assets"},
	}
}

func TestPPSSPPRevertBad(t *testing.T) {
	root, file := ppssppTestEnv(t)
	older := ppssppTestBuild(t, root, "6.1.0", "1.20.2")
	newer := ppssppTestBuild(t, root, "6.2.0", "1.20.3")
	fw := ppssppFirmwareInfo{Launcher: "/mnt/SDCARD/Emus/PSP/launch.sh", Version: "1.18.1", Stick: filepath.Join(filepath.Dir(root), "stick")}
	ppssppMgr.mu.Lock()
	st := ppssppLockedState()
	st.Builds = []ppssppBuild{newer, older}
	if err := ppssppUseBuildLocked(st, older, fw); err != nil {
		t.Fatal(err)
	}
	if err := ppssppUseBuildLocked(st, newer, fw); err != nil {
		t.Fatal(err)
	}
	ppssppMgr.mu.Unlock()

	msg, err := ppssppRevertBad("6.2.0", "PPSSPP 1.20.3 exited with status 139 after 2s", fw)
	if err != nil {
		t.Fatal(err)
	}
	if want := "PPSSPP 1.20.3 did not start on this device (PPSSPP 1.20.3 exited with status 139 after 2s) — PSP games use PPSSPP 1.20.2 again"; msg != want {
		t.Errorf("toast = %q, want %q", msg, want)
	}
	snap := ppssppSnapshot()
	if snap.Active != "6.1.0" || snap.Bad["6.2.0"] == "" || snap.Previous != "" {
		t.Fatalf("after the first revert: active %q previous %q bad %v", snap.Active, snap.Previous, snap.Bad)
	}
	if got := loadGameLauncherOverrides(file)["playstation portable"]; got != older.script() {
		t.Fatalf("PSP games resolve to %q, want the build before", got)
	}
	if !regularFile(older.script()) {
		t.Fatal("the build gone back to has no launcher")
	}

	msg, err = ppssppRevertBad("6.1.0", "PPSSPP 1.20.2 exited with status 1 after 1s", fw)
	if err != nil {
		t.Fatal(err)
	}
	if want := "PPSSPP 1.20.2 did not start on this device (PPSSPP 1.20.2 exited with status 1 after 1s) — PSP games use PPSSPP 1.18.1 again"; msg != want {
		t.Errorf("toast = %q, want %q", msg, want)
	}
	if snap := ppssppSnapshot(); snap.Active != "" || snap.Override {
		t.Fatalf("after the second revert: active %q override %v", snap.Active, snap.Override)
	}
	if regularFile(file) {
		t.Error("the launcher file JukaHub created is still there")
	}
	if msg, err := ppssppRevertBad("6.1.0", "again", fw); err != nil || msg != "" {
		t.Errorf("a revert for a build no longer active: %q, %v", msg, err)
	}
}

func TestPPSSPPFirmwareDetection(t *testing.T) {
	ppssppTestEnv(t)
	// CrossMix: config.json names the default build's script, two builds sit
	// beside it, and the one played most recently is not the default.
	sd := t.TempDir()
	psp := filepath.Join(sd, "Emus", "PSP")
	ppssppWriteFiles(t, psp, ppssppFiles(
		"config.json", `{"name":"PSP","launch":"ppsspp_1.17.1_gl.sh"}`,
		"ppsspp_1.17.1_gl.sh", "#!/bin/sh\n",
		"ppsspp_1.19.0.sh", "#!/bin/sh\n",
		"PPSSPP_1.17.1/.config/ppsspp/PSP/SYSTEM/ppsspp.ini", "[General]\n",
		"PPSSPP_1.19.0/.config/ppsspp/PSP/SYSTEM/ppsspp.ini", "[General]\n",
	))
	if err := os.MkdirAll(filepath.Join(psp, "PPSSPP_1.19.0", ".config", "ppsspp", "PSP", "SAVEDATA"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(psp, "PPSSPP_1.17.1", ".config", "ppsspp", "PSP", "SYSTEM", "ppsspp.ini"), old, old)
	var scan ppssppBinaryScan
	info := ppssppDetectFirmware(sd, []string{filepath.Join(sd, "Emus")}, "", &scan)
	if info.Launcher != filepath.Join(psp, "ppsspp_1.17.1_gl.sh") || info.Version != "1.17.1" {
		t.Fatalf("CrossMix firmware = %+v", info)
	}
	if want := filepath.Join(psp, "PPSSPP_1.17.1", ".config", "ppsspp", "PSP"); info.Stick != want {
		t.Errorf("CrossMix memstick = %q, want the default build's %q", info.Stick, want)
	}

	// Stock: Emus/PPSSPP/launch.sh with the binary beside it, saves at the
	// card root's .config.
	stock := t.TempDir()
	ppssppWriteFiles(t, stock, ppssppFiles(
		"Emus/PPSSPP/launch.sh", "#!/bin/sh\ncd \"$(dirname \"$0\")\"\n./PPSSPPSDL \"$1\"\n",
		"Emus/PPSSPP/PPSSPPSDL", ppssppFakeELF("1.18.1"),
		".config/ppsspp/PSP/SYSTEM/ppsspp.ini", "[General]\n",
		".config/ppsspp/PSP/SAVEDATA/ULUS1/a.bin", "save",
	))
	scan = ppssppBinaryScan{}
	info = ppssppDetectFirmware(stock, []string{filepath.Join(stock, "Emus")}, "", &scan)
	if info.Launcher != filepath.Join(stock, "Emus", "PPSSPP", "launch.sh") || info.Version != "1.18.1" {
		t.Fatalf("stock firmware = %+v", info)
	}
	if want := filepath.Join(stock, ".config", "ppsspp", "PSP"); info.Stick != want {
		t.Errorf("stock memstick = %q, want %q", info.Stick, want)
	}
	if scan.Version != "1.18.1" || scan.Path == "" {
		t.Errorf("binary scan not cached: %+v", scan)
	}
	// A pinned launcher wins over the catalog's.
	pin := filepath.Join(stock, "Emus", "PPSSPP", "mine.sh")
	ppssppWriteFiles(t, stock, ppssppFiles("Emus/PPSSPP/mine.sh", "#!/bin/sh\n"))
	if info := ppssppDetectFirmware(stock, []string{filepath.Join(stock, "Emus")}, pin, &scan); info.Launcher != pin {
		t.Errorf("pinned launcher ignored: %q", info.Launcher)
	}
}

func TestPPSSPPSafeRemove(t *testing.T) {
	root, _ := ppssppTestEnv(t)
	withSaves := filepath.Join(root, "6.0.0")
	ppssppWriteFiles(t, withSaves, ppssppFiles("PSP.pak/PPSSPP/.config/ppsspp/PSP/SAVEDATA/ULUS1/DATA.BIN", "save"))
	if err := ppssppSafeRemove(withSaves); err == nil || !dirExists(withSaves) {
		t.Errorf("a build holding saves was removed (%v)", err)
	}
	empty := filepath.Join(root, "6.1.0")
	if err := os.MkdirAll(filepath.Join(empty, "PSP.pak", "PPSSPP", ".config", "ppsspp", "PSP", "SAVEDATA"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ppssppSafeRemove(empty); err != nil || dirExists(empty) {
		t.Errorf("an old build with empty mount points was kept (%v)", err)
	}
	if err := ppssppSafeRemove(t.TempDir()); err == nil {
		t.Error("a folder outside the manager's was removed")
	}
	if err := ppssppSafeRemove(root); err == nil {
		t.Error("the manager's own folder was removed")
	}
	if err := os.MkdirAll(ppssppManagedMemstick(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ppssppSafeRemove(ppssppHomeDir()); err == nil || !dirExists(ppssppHomeDir()) {
		t.Errorf("the shared HOME with the user's PPSSPP settings was removed (%v)", err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 2 {
		t.Errorf("removals left %d entries in the manager's folder, want the build with saves and home", len(entries))
	}

	mounts := "/dev/mmcblk0p1 /mnt/SDCARD vfat rw 0 0\n" +
		"/dev/mmcblk0p1 /mnt/SDCARD/Apps/JukaHub/data/emulators/ppsspp/6.2.0/PSP.pak/PPSSPP/.config/ppsspp/PSP/SAVEDATA vfat rw 0 0\n" +
		"/dev/mmcblk0p1 /mnt/SDCARD/Apps/JukaHub/data/emulators/ppsspp/6.2.0x vfat rw 0 0\n" +
		"/dev/mmcblk0p1 /mnt/with\\040space/a vfat rw 0 0\n"
	if got := ppssppMountPointsWithin(mounts, "/mnt/SDCARD/Apps/JukaHub/data/emulators/ppsspp/6.2.0"); len(got) != 1 || !strings.HasSuffix(got[0], "/SAVEDATA") {
		t.Errorf("mounts inside the build = %v", got)
	}
	if got := ppssppMountPointsWithin(mounts, "/mnt/with space"); len(got) != 1 || got[0] != "/mnt/with space/a" {
		t.Errorf("escaped mount point = %v", got)
	}
}

func TestPPSSPPAutoDecide(t *testing.T) {
	installed := ppssppState{Builds: []ppssppBuild{{Tag: "6.1.0", Version: "1.20.2"}}}
	rel := ppssppRelease{Tag: "6.2.0", Version: "1.20.3"}
	cases := []struct {
		name     string
		mode     coreUpdateMode
		st       ppssppState
		rel      ppssppRelease
		fw       string
		untested bool
		want     ppssppAutoAction
	}{
		{"off", coreUpdateOff, ppssppState{}, rel, "1.18.1", false, ppssppAutoNothing},
		{"notify newer", coreUpdateNotify, ppssppState{}, rel, "1.18.1", false, ppssppAutoNotify},
		{"install newer", coreUpdateInstall, ppssppState{}, rel, "1.18.1", false, ppssppAutoInstall},
		{"install unknown release version", coreUpdateInstall, ppssppState{}, ppssppRelease{Tag: "6.2.0"}, "1.18.1", false, ppssppAutoNothing},
		{"install unknown firmware", coreUpdateInstall, ppssppState{}, rel, "", false, ppssppAutoInstall},
		{"firmware newer", coreUpdateInstall, ppssppState{}, ppssppRelease{Tag: "6.2.0", Version: "1.17.0"}, "1.18.1", false, ppssppAutoNothing},
		{"update installed", coreUpdateInstall, installed, ppssppRelease{Tag: "6.2.0"}, "1.18.1", false, ppssppAutoInstall},
		{"already installed", coreUpdateInstall, installed, ppssppRelease{Tag: "6.1.0"}, "1.18.1", false, ppssppAutoNothing},
		{"bad tag", coreUpdateInstall, ppssppState{Bad: map[string]string{"6.2.0": "x"}}, rel, "1.18.1", false, ppssppAutoNothing},
		{"untested model", coreUpdateInstall, ppssppState{}, rel, "1.18.1", true, ppssppAutoNotify},
		{"chose firmware", coreUpdateInstall, ppssppState{Choice: ppssppChoiceFirmware}, rel, "1.18.1", false, ppssppAutoNotify},
		// Against the newest build that starts: never down, never the same
		// PPSSPP from the other source, a rebuild from the same source yes.
		{"older than installed", coreUpdateInstall, ppssppState{Builds: []ppssppBuild{{Tag: "jukahub-1.20.4-aaaaaaaa", Version: "1.20.4"}}}, rel, "1.18.1", false, ppssppAutoNothing},
		{"same version other source", coreUpdateInstall, ppssppState{Builds: []ppssppBuild{{Tag: "jukahub-1.20.3-aaaaaaaa", Version: "1.20.3"}}}, rel, "1.18.1", false, ppssppAutoNothing},
		{"rebuild same source", coreUpdateInstall, ppssppState{Builds: []ppssppBuild{{Tag: "jukahub-1.20.4-aaaaaaaa", Version: "1.20.4"}}}, ppssppRelease{Tag: "jukahub-1.20.4-bbbbbbbb", Version: "1.20.4"}, "1.18.1", false, ppssppAutoInstall},
		{"newer own over minui", coreUpdateInstall, ppssppState{Builds: []ppssppBuild{{Tag: "6.2.0", Version: "1.20.3"}}}, ppssppRelease{Tag: "jukahub-1.20.4-bbbbbbbb", Version: "1.20.4"}, "1.18.1", false, ppssppAutoInstall},
		{"installed one did not start", coreUpdateInstall, ppssppState{Builds: []ppssppBuild{{Tag: "7.0.0", Version: "1.21.0"}}, Bad: map[string]string{"7.0.0": "x"}}, ppssppRelease{Tag: "jukahub-1.20.4-bbbbbbbb", Version: "1.20.4"}, "1.18.1", false, ppssppAutoInstall},
		{"kept older build is this tag", coreUpdateInstall, ppssppState{Builds: []ppssppBuild{{Tag: "jukahub-1.20.4-aaaaaaaa", Version: "1.20.4"}, {Tag: "6.2.0", Version: "1.20.3"}}}, rel, "1.18.1", false, ppssppAutoNothing},
	}
	for _, c := range cases {
		if got := ppssppAutoDecide(c.mode, c.st, c.rel, c.fw, c.untested); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}

	b := ppssppBuild{Tag: "6.2.0", Version: "1.20.3"}
	switches := []struct {
		name   string
		st     ppssppState
		fw     string
		pinned bool
		want   bool
	}{
		{"chose firmware", ppssppState{Choice: ppssppChoiceFirmware}, "1.18.1", false, false},
		{"chose JukaHub", ppssppState{Choice: ppssppChoiceJukaHub}, "1.21.0", false, true},
		{"chose JukaHub, then pinned by hand", ppssppState{Choice: ppssppChoiceJukaHub}, "1.18.1", true, false},
		{"already on JukaHub", ppssppState{Active: "6.1.0"}, "1.21.0", false, true},
		{"on JukaHub, then pinned by hand", ppssppState{Active: "6.1.0"}, "1.18.1", true, false},
		{"hand pin", ppssppState{}, "1.18.1", true, false},
		{"newer than firmware", ppssppState{}, "1.18.1", false, true},
		{"firmware unknown", ppssppState{}, "", false, true},
		{"firmware newer", ppssppState{}, "1.21.0", false, false},
	}
	for _, c := range switches {
		if got := ppssppShouldSwitch(c.st, b, c.fw, c.pinned); got != c.want {
			t.Errorf("switch %s: %v, want %v", c.name, got, c.want)
		}
	}
	if ppssppShouldSwitch(ppssppState{}, ppssppBuild{Tag: "6.2.0"}, "", false) {
		t.Error("switched to a build of unknown version over a firmware of unknown version")
	}
}

// TestPPSSPPCheckDue: daily, at once under an older rule, and once per run of
// JukaHub when the last check is more than ppssppStartRecheck old.
func TestPPSSPPCheckDue(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	stamp := func(ago time.Duration, rule int) ppssppState {
		return ppssppState{CheckedAt: now.Add(-ago).Format(time.RFC3339), CheckedRule: rule}
	}
	cases := []struct {
		name  string
		st    ppssppState
		first bool
		want  bool
	}{
		{"never checked", ppssppState{}, false, true},
		{"older rule", stamp(time.Hour, ppssppCheckRule-1), false, true},
		{"a day old", stamp(25*time.Hour, ppssppCheckRule), false, true},
		{"fresh, later in the run", stamp(3*time.Hour, ppssppCheckRule), false, false},
		{"fresh, first in the run", stamp(3*time.Hour, ppssppCheckRule), true, true},
		{"just checked, first in the run", stamp(time.Hour, ppssppCheckRule), true, false},
		{"stamp from the future", stamp(-time.Hour, ppssppCheckRule), false, true},
	}
	for _, c := range cases {
		if got := ppssppCheckDue(c.st, now, c.first); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPPSSPPTileLine(t *testing.T) {
	fw := ppssppFirmwareInfo{Launcher: "/mnt/SDCARD/Emus/PPSSPP/launch.sh", Version: "1.18.1"}
	if got := ppssppTileLine(ppssppState{LatestVersion: "1.20.3", LatestTag: "6.2.0"}, fw, true, "tg5040"); got != "PPSSPP 1.18.1 (firmware) · 1.20.3 available" {
		t.Errorf("firmware line = %q", got)
	}
	active := ppssppState{Active: "6.2.0", Builds: []ppssppBuild{{Tag: "6.2.0", Version: "1.20.3"}}}
	if got := ppssppTileLine(active, fw, true, "tg5040"); got != "PPSSPP 1.20.3 (minui-psp build) · firmware 1.18.1 kept" {
		t.Errorf("JukaHub line = %q", got)
	}
	if got := ppssppTileLine(ppssppState{}, fw, true, ""); got != "PPSSPP 1.18.1 (firmware) · no JukaHub build for this model" {
		t.Errorf("unsupported line = %q", got)
	}
	installed := ppssppState{Builds: []ppssppBuild{{Tag: "6.2.0", Version: "1.20.3"}}}
	if got := ppssppTileLine(installed, fw, true, "tg5040"); got != "PPSSPP 1.18.1 (firmware) · JukaHub's 1.20.3 installed" {
		t.Errorf("installed line = %q", got)
	}
}

// TestPPSSPPInstallFromRelease runs the whole install against a local server:
// release → download → digest → unpack → layout → launcher → state, then two
// updates, which keep one previous build and remove the one before it.
func TestPPSSPPInstallFromRelease(t *testing.T) {
	root, _ := ppssppTestEnv(t)
	var mu sync.Mutex
	paks := map[string][]byte{}
	latest := ""
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()
	allowUpdateTestServer(t, server.URL)
	prevAPI := standaloneAPIBase
	standaloneAPIBase = server.URL
	t.Cleanup(func() { standaloneAPIBase = prevAPI })
	mux.HandleFunc("/repos/"+ppssppPakRepo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tag, pak := latest, paks[latest]
		mu.Unlock()
		fmt.Fprintf(w, `{"tag_name":%q,"name":%q,"body":"update ppsspp 1.20.3","assets":[{"name":"PSP.pak.zip","browser_download_url":%q,"size":%d,"digest":%q}]}`,
			tag, tag, server.URL+"/dl/"+tag+"/PSP.pak.zip", len(pak), ppssppDigestOf(pak))
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		tag := strings.Split(strings.TrimPrefix(r.URL.Path, "/dl/"), "/")[0]
		mu.Lock()
		pak := paks[tag]
		mu.Unlock()
		_, _ = w.Write(pak)
	})
	publish := func(tag, version string) {
		data := ppssppTestZip(t, ppssppTestPak(version))
		mu.Lock()
		paks[tag] = data
		latest = tag
		mu.Unlock()
	}

	stick := filepath.Join(t.TempDir(), "PSP")
	ppssppWriteFiles(t, stick, ppssppFiles(
		"SYSTEM/controls.ini", "[ControlMapping]\nUp = 10-19\n",
		"SYSTEM/ULUS10041_ppsspp.ini", "[Graphics]\nFrameSkip = 1\n",
		"SYSTEM/ppsspp.ini", "[Graphics]\nGraphicsBackend = 3 (VULKAN)\n",
	))
	fw := ppssppFirmwareInfo{Launcher: "/mnt/SDCARD/Emus/PSP/launch.sh", Version: "1.18.1", Stick: stick}
	install := func(tag, version string) ppssppBuild {
		t.Helper()
		publish(tag, version)
		ctx := context.Background()
		rel, err := ppssppFetchRelease(ctx, server.Client())
		if err != nil {
			t.Fatalf("%s: %v", tag, err)
		}
		b, err := ppssppInstall(ctx, server.Client(), rel, "tg5040", fw, nil)
		if err != nil {
			t.Fatalf("install %s: %v", tag, err)
		}
		return b
	}

	b := install("6.2.0", "1.20.3")
	if b.Dir != filepath.Join(root, "builds", "6.2.0") || b.Version != "1.20.3" || !regularFile(b.binary()) {
		t.Fatalf("installed build = %+v", b)
	}
	// Every build runs with the one HOME outside the build folders.
	if b.memstick() != filepath.Join(root, "home", ".config", "ppsspp", "PSP") || isPathWithin(b.memstick(), b.Dir) {
		t.Fatalf("memstick %q is not the shared one", b.memstick())
	}
	if data, _ := os.ReadFile(filepath.Join(b.memstick(), "SYSTEM", "ppsspp.ini")); !strings.Contains(string(data), "[General]") || !strings.Contains(string(data), "GraphicsBackend = 0 (OPENGL)") {
		t.Errorf("the package's settings and the TrimUI graphics backend were not kept: %q", data)
	}
	script, err := os.ReadFile(b.script())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "FIRMWARE_STICK="+shellQuote(stick)) || !strings.Contains(string(script), "FALLBACK='/mnt/SDCARD/Emus/PSP/launch.sh'") {
		t.Errorf("launcher does not share the firmware's saves or fall back:\n%s", script)
	}
	if !regularFile(filepath.Join(b.Dir, ppssppManifestName)) {
		t.Error("no provenance file beside the build")
	}
	if data, _ := os.ReadFile(filepath.Join(b.memstick(), "SYSTEM", "controls.ini")); !strings.Contains(string(data), "Up = 10-19") {
		t.Error("the firmware's controls.ini was not carried into the first install")
	}
	if !regularFile(filepath.Join(b.memstick(), "SYSTEM", "ULUS10041_ppsspp.ini")) {
		t.Error("the firmware's per-game config was not carried into the first install")
	}
	if data, _ := os.ReadFile(filepath.Join(b.memstick(), "SYSTEM", "ppsspp.ini")); strings.Contains(string(data), "VULKAN") {
		t.Error("the firmware's ppsspp.ini replaced the build's own")
	}
	if !dirExists(filepath.Join(b.memstick(), "SAVEDATA")) || !dirExists(filepath.Join(b.memstick(), "PPSSPP_STATE")) {
		t.Error("the mount points for the shared saves are missing")
	}
	if st := ppssppSnapshot(); len(st.Builds) != 1 || st.Builds[0].Tag != "6.2.0" || st.Active != "" {
		t.Errorf("state after install = %+v (an install must not switch by itself)", st)
	}

	// Settings changed in PPSSPP, and the user's own files (cheats here), stay
	// across updates: the next builds read the same HOME, and removing the
	// oldest build takes none of it along.
	if err := os.WriteFile(filepath.Join(b.memstick(), "SYSTEM", "ppsspp.ini"), []byte("[Graphics]\nFrameSkip = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ppssppWriteFiles(t, b.memstick(), ppssppFiles("CHEATS/ULUS10041.ini", "_S ULUS-10041\n"))
	b2 := install("6.3.0", "1.20.4")
	if data, _ := os.ReadFile(filepath.Join(b2.memstick(), "SYSTEM", "ppsspp.ini")); !strings.Contains(string(data), "FrameSkip = 2") {
		t.Error("the user's settings did not survive an update")
	}
	b3 := install("6.4.0", "1.20.5")
	st := ppssppSnapshot()
	if len(st.Builds) != 2 || st.Builds[0].Tag != b3.Tag || st.Builds[1].Tag != b2.Tag {
		t.Fatalf("builds after two updates = %+v", st.Builds)
	}
	if dirExists(b.Dir) {
		t.Error("the oldest build was not removed")
	}
	if !regularFile(filepath.Join(b3.memstick(), "CHEATS", "ULUS10041.ini")) {
		t.Error("removing an old build took the user's cheats along")
	}
	leftovers, _ := os.ReadDir(filepath.Join(root, "builds"))
	if len(leftovers) != 2 || regularFile(filepath.Join(root, ".download", ppssppPakAsset)) {
		t.Errorf("builds folder holds %d entries (want the two builds) or the download was left behind", len(leftovers))
	}

	// A download whose bytes do not match the digest is refused.
	publish("6.5.0", "1.20.6")
	rel, err := ppssppFetchRelease(context.Background(), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	rel.Digest = ppssppDigestOf([]byte("something else"))
	if _, err := ppssppInstall(context.Background(), server.Client(), rel, "tg5040", fw, nil); err == nil {
		t.Error("a download that does not match its digest was installed")
	}
	if dirExists(filepath.Join(root, "builds", "6.5.0")) {
		t.Error("a refused download left a build folder")
	}
}

// JukaHub's PPSSPP signs in to RetroAchievements with JukaHub's account
// (GAME-21): the merge rule, the files it writes (the token only in its own
// 0600 store, never the ini) and the RetroArch login it reads.
func TestPPSSPPRetroAchievementsSignIn(t *testing.T) {
	const token = "AbCdEf0123456789"
	for _, tc := range []struct {
		name      string
		optOut    bool
		user, tok string
		iniUser   string
		haveToken bool
		write     bool
	}{
		{"nobody signed in", false, "Juka", token, "", false, true},
		{"a token left without an account", false, "Juka", token, "", true, true},
		{"the same account without a token", false, "Juka", token, " juka ", false, true},
		{"the same account already signed in", false, "Juka", token, "JUKA", true, false},
		{"another account signed in", false, "Juka", token, "Someone", true, false},
		{"another account without a token", false, "Juka", token, "Someone", false, false},
		{"off on the card", true, "Juka", token, "", false, false},
		{"no token to share", false, "Juka", "", "", false, false},
		{"no account", false, "", token, "", false, false},
	} {
		plan := ppssppRAMerge(tc.optOut, tc.user, tc.tok, tc.iniUser, tc.haveToken)
		if plan.write != tc.write {
			t.Errorf("%s: write = %v, want %v (%s)", tc.name, plan.write, tc.write, plan.reason)
		}
		if plan.reason == "" || strings.Contains(plan.reason, token) {
			t.Errorf("%s: outcome %q is empty or names the token", tc.name, plan.reason)
		}
	}

	// Signing in: token in its store, account in [Achievements], hardcore off,
	// the rest of the ini kept, the token in no other file.
	stick := filepath.Join(t.TempDir(), "PSP")
	sys := filepath.Join(stick, "SYSTEM")
	ini := ppssppMemstickIni(stick)
	store := filepath.Join(sys, ppssppRATokenFile)
	if err := os.MkdirAll(sys, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ini, []byte("[Graphics]\nFullScreen = True\n[Achievements]\nAchievementsChallengeMode = True\nAchievementsUserName = \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	iniValue := func(key string) string {
		raw, _ := os.ReadFile(ini)
		v, _ := ppssppINIValue(strings.Split(string(raw), "\n"), "Achievements", key)
		return v
	}
	if outcome := ppssppRASignIn(stick, false, "Juka", token); strings.Contains(outcome, token) {
		t.Fatal("the outcome names the token")
	}
	if got, err := os.ReadFile(store); err != nil || string(got) != token {
		t.Fatalf("the token store does not hold exactly the token (%v)", err)
	}
	if info, err := os.Stat(store); err == nil && runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("token store mode %v, want 0600", info.Mode().Perm())
	}
	if iniValue("AchievementsUserName") != "Juka" || iniValue("AchievementsEnable") != "True" || iniValue("AchievementsChallengeMode") != "False" {
		t.Errorf("[Achievements] after sign-in: user %q, enable %q, hardcore %q", iniValue("AchievementsUserName"),
			iniValue("AchievementsEnable"), iniValue("AchievementsChallengeMode"))
	}
	if raw, _ := os.ReadFile(ini); !strings.Contains(string(raw), "FullScreen = True") {
		t.Error("another setting was lost")
	}
	entries, _ := os.ReadDir(sys)
	for _, e := range entries {
		if data, _ := os.ReadFile(filepath.Join(sys, e.Name())); e.Name() != ppssppRATokenFile && strings.Contains(string(data), token) {
			t.Errorf("%s holds the token", e.Name())
		}
	}

	// Signed in: a hardcore mode chosen in PPSSPP and its token stay.
	if err := writePPSSPPControls(ini, func(lines []string) []string {
		return ppssppSetSectionValue(lines, "Achievements", "AchievementsChallengeMode", "True")
	}); err != nil {
		t.Fatal(err)
	}
	ppssppRASignIn(stick, false, "Juka", "Other0123456789x")
	if got, _ := os.ReadFile(store); string(got) != token || iniValue("AchievementsChallengeMode") != "True" {
		t.Error("an existing sign-in of the same account was replaced")
	}

	// Another account signed in inside PPSSPP is left alone.
	if err := writePPSSPPControls(ini, func(lines []string) []string {
		return ppssppSetSectionValue(lines, "Achievements", "AchievementsUserName", "Someone")
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store); err != nil {
		t.Fatal(err)
	}
	ppssppRASignIn(stick, false, "Juka", token)
	if regularFile(store) || iniValue("AchievementsUserName") != "Someone" {
		t.Error("another account's PPSSPP sign-in was replaced")
	}

	// Off on the card, or a token that could break the ini: nothing written.
	for _, tc := range []struct {
		optOut bool
		token  string
	}{{true, token}, {false, "abc\nAchievementsUserName = x"}} {
		fresh := filepath.Join(t.TempDir(), "PSP")
		ppssppRASignIn(fresh, tc.optOut, "Juka", tc.token)
		if regularFile(ppssppMemstickIni(fresh)) || regularFile(filepath.Join(fresh, "SYSTEM", ppssppRATokenFile)) {
			t.Errorf("sign-in wrote files (off %v)", tc.optOut)
		}
	}
	// A HOME PPSSPP has not run in yet gets a minimal [Achievements].
	fresh := filepath.Join(t.TempDir(), "PSP")
	ppssppRASignIn(fresh, false, "Juka", token)
	if raw, _ := os.ReadFile(ppssppMemstickIni(fresh)); !strings.Contains(string(raw), "[Achievements]") ||
		!strings.Contains(string(raw), "AchievementsUserName = Juka") {
		t.Errorf("no [Achievements] written to a new ini: %q", raw)
	}

	// The login: RetroArch's token only for JukaHub's own account.
	dir := t.TempDir()
	other, mine := filepath.Join(dir, "other.cfg"), filepath.Join(dir, "retroarch.cfg")
	settings := filepath.Join(dir, "retroachievements.json")
	for path, body := range map[string]string{
		other:    "cheevos_username = \"Someone\"\ncheevos_token = \"Zzzzzzzzzzzzzzzz\"\n",
		mine:     "cheevos_password = \"\"\ncheevos_username = \"juka\"\ncheevos_token = \"" + token + "\"\n",
		settings: `{"username": "Juka"}`,
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	prevPaths, prevGlobs, prevSettings := raRetroArchConfigPaths, raRetroArchConfigGlobs, raSettingsPathOverride
	raReset := func() {
		raSettingsState.mu.Lock()
		raSettingsState.loaded, raSettingsState.data = false, raSettings{}
		raSettingsState.mu.Unlock()
		raInvalidateCredentialCache()
	}
	t.Cleanup(func() {
		raRetroArchConfigPaths, raRetroArchConfigGlobs, raSettingsPathOverride = prevPaths, prevGlobs, prevSettings
		raReset()
	})
	raRetroArchConfigPaths, raRetroArchConfigGlobs, raSettingsPathOverride = []string{other, mine}, nil, settings
	raReset()
	if user, tok := ppssppRALogin(); user != "juka" || tok != token {
		t.Errorf("login = %q and a %d-character token, want RetroArch's for juka", user, len(tok))
	}
	if err := os.WriteFile(settings, []byte(`{"username": "Nobody"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	raReset()
	if user, tok := ppssppRALogin(); user != "" || tok != "" {
		t.Error("another account's RetroArch token was taken")
	}
}
