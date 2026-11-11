package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// apkStoreProfileSandbox points the game profiles at a temp folder whose
// games root is apks.
func apkStoreProfileSandbox(t *testing.T) (profiles, apks string) {
	t.Helper()
	base := t.TempDir()
	profiles = filepath.Join(base, "game-profiles")
	apks = filepath.Join(base, "card", "APKs")
	if err := os.MkdirAll(apks, 0o755); err != nil {
		t.Fatal(err)
	}
	oldDir, oldRoots := gameProfilesDirOverride, gameProfileRootsOverride
	gameProfilesDirOverride = profiles
	gameProfileRootsOverride = []string{filepath.Join(base, "card")}
	invalidateGameProfileCache()
	t.Cleanup(func() {
		gameProfilesDirOverride, gameProfileRootsOverride = oldDir, oldRoots
		invalidateGameProfileCache()
	})
	return profiles, apks
}

func TestApkStoreProfileID(t *testing.T) {
	cases := map[string]string{
		"com.Mojang.MinecraftPE": "apk-com.mojang.minecraftpe",
		"org.weird pkg!name":     "apk-org.weird-pkg-name",
		"":                       "apk-from-file",
	}
	for pkg, want := range cases {
		info := apkStoreInfo{Path: "/card/From File.apk", Manifest: apkManifest{Package: pkg}}
		if got := apkStoreProfileID(info); got != want {
			t.Errorf("id(%q) = %q, want %q", pkg, got, want)
		}
	}
	long := apkStoreProfileID(apkStoreInfo{Manifest: apkManifest{Package: strings.Repeat("abcdefghij.", 12)}})
	if len(long) > 64 || !gameProfileIDPattern.MatchString(long) {
		t.Errorf("long id %q (%d) does not fit the profile id rules", long, len(long))
	}
	if got := apkStoreProfileID(apkStoreInfo{Path: "/x/!!!.apk"}); got != "apk-game" {
		t.Errorf("symbol-only name = %q", got)
	}
}

func TestApkStoreAddAndRemoveProfile(t *testing.T) {
	profiles, apks := apkStoreProfileSandbox(t)
	p := nativeTestAPK(t, apks, "tunnel.apk", "com.example.tunnel")
	info := apkStoreAnalyze(p, false)
	if info.Tier != apkTierNative {
		t.Fatalf("fixture tier %v", info.Tier)
	}
	prof, err := apkStoreAddProfile(info)
	if err != nil {
		t.Fatal(err)
	}
	if prof.ID != "apk-com.example.tunnel" || prof.Runtime != gameRuntimeAndroidPak || prof.MainLib != "libgame.so" ||
		prof.Executable != "tunnel.apk" || prof.Title != "Tunnel Run" {
		t.Errorf("profile = %+v", prof)
	}
	file := filepath.Join(profiles, "local", apkStoreProfilePrefix+prof.ID+".json")
	if !regularFile(file) {
		t.Fatalf("profile file %s was not written", file)
	}
	// The loader reads it back as an AndroidPak profile (its issue, if any, is
	// the missing runtime, not the profile).
	states := apkStoreGameStates([]apkStoreInfo{info})
	st, ok := states[info.Path]
	if !ok || st.ID != prof.ID || !st.Ours {
		t.Fatalf("game state = %+v (found %v)", st, ok)
	}
	if st.Issue != "" && !strings.Contains(strings.ToLower(st.Issue), "runtime") {
		t.Errorf("unexpected profile issue: %s", st.Issue)
	}
	if err := apkStoreRemoveProfile(prof.ID); err != nil {
		t.Fatal(err)
	}
	if regularFile(file) || regularFile(file+".bak") {
		t.Error("the profile file (or its backup) is still there")
	}
	if states := apkStoreGameStates([]apkStoreInfo{info}); len(states) != 0 {
		t.Errorf("still in Games after remove: %+v", states)
	}
	// Removing twice is not an error.
	if err := apkStoreRemoveProfile(prof.ID); err != nil {
		t.Errorf("second remove: %v", err)
	}
}

// A hand-written profile for the same APK is recognised, but it is not the
// store's to remove.
func TestApkStoreRecognisesHandWrittenProfile(t *testing.T) {
	profiles, apks := apkStoreProfileSandbox(t)
	p := nativeTestAPK(t, apks, "tunnel.apk", "com.example.tunnel")
	info := apkStoreAnalyze(p, false)
	hand := `{"schemaVersion":1,"id":"my-tunnel","title":"My Tunnel","runtime":"androidpak","dir":` +
		strconv.Quote(filepath.ToSlash(apks)) + `,"executable":"tunnel.apk"}`
	if err := os.MkdirAll(filepath.Join(profiles, "local"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profiles, "local", "mine.json"), []byte(hand), 0o644); err != nil {
		t.Fatal(err)
	}
	invalidateGameProfileCache()
	st, ok := apkStoreGameStates([]apkStoreInfo{info})[info.Path]
	if !ok || st.ID != "my-tunnel" || st.Ours {
		t.Errorf("hand-written profile state = %+v (found %v)", st, ok)
	}
}

func TestApkStoreRefusesWhatCannotRun(t *testing.T) {
	_, apks := apkStoreProfileSandbox(t)
	java := apkStoreInfo{Path: filepath.Join(apks, "notes.apk"), Tier: apkTierJava, Verdict: "Needs Android's Java runtime"}
	if _, err := apkStoreBuildProfile(java, gameProfileRoots()); err == nil {
		t.Error("a Java app became a game profile")
	}
	outside := filepath.Join(t.TempDir(), "elsewhere")
	p := nativeTestAPK(t, outside, "x.apk", "com.example.x")
	info := apkStoreAnalyze(p, false)
	if _, err := apkStoreBuildProfile(info, gameProfileRoots()); err == nil {
		t.Error("an APK outside the game folders became a profile")
	}
}

// Two APKs of one package (two versions of a game) get two profiles: adding
// the second must not overwrite the first, and both are recognised.
func TestApkStoreSamePackageTwice(t *testing.T) {
	_, apks := apkStoreProfileSandbox(t)
	first := apkStoreAnalyze(nativeTestAPK(t, apks, "tunnel-v1.apk", "com.example.tunnel"), false)
	second := apkStoreAnalyze(nativeTestAPK(t, filepath.Join(apks, "new"), "tunnel-v2.apk", "com.example.tunnel"), false)
	p1, err := apkStoreAddProfile(first)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := apkStoreAddProfile(second)
	if err != nil {
		t.Fatal(err)
	}
	if p1.ID == p2.ID || !gameProfileIDPattern.MatchString(p2.ID) || len(p2.ID) > 64 {
		t.Fatalf("ids %q and %q", p1.ID, p2.ID)
	}
	if target, ok := apkStoreProfileTarget(p1.ID); !ok || apkStorePathKey(target) != apkStorePathKey(first.Path) {
		t.Errorf("the first profile was re-pointed: %q", target)
	}
	states := apkStoreGameStates([]apkStoreInfo{first, second})
	if states[first.Path].ID != p1.ID || states[second.Path].ID != p2.ID || !states[second.Path].Ours {
		t.Errorf("states = %+v", states)
	}
	// Adding the same file again keeps its id.
	if again, err := apkStoreAddProfile(second); err != nil || again.ID != p2.ID {
		t.Errorf("re-add: %q %v", again.ID, err)
	}
}

// A profile's title is limited in bytes; a long Chinese or Japanese label is
// cut on a character boundary instead of failing Add.
func TestApkStoreLongMultibyteTitle(t *testing.T) {
	_, apks := apkStoreProfileSandbox(t)
	info := apkStoreAnalyze(nativeTestAPK(t, apks, "cjk.apk", "com.example.cjk"), false)
	info.Label = apkStoreClipLabel(strings.Repeat("冒険", 50))
	p, err := apkStoreAddProfile(info)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Title) > apkStoreTitleMaxBytes || !utf8.ValidString(p.Title) || p.Title == "" {
		t.Errorf("title %q (%d bytes)", p.Title, len(p.Title))
	}
}

func TestApkStoreLastRun(t *testing.T) {
	profiles, _ := apkStoreProfileSandbox(t)
	logs := filepath.Join(profiles, "logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(text string) string {
		p := filepath.Join(logs, "apk-x.log")
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := write("=== apk-x start 2026-11-10T10:00:00Z ===\nexit: 0\n")
	if got := apkStoreLastRun("apk-x", p); !strings.Contains(got, "ended normally") {
		t.Errorf("clean exit: %q", got)
	}
	p = write("=== apk-x start 1 ===\nexit: 0\n=== apk-x start 2 ===\n[androidrt] undefined symbol: AAsset_openFd64\nexit: exit status 1\n")
	if got := apkStoreLastRun("apk-x", p); !strings.Contains(got, "AAsset_openFd64") {
		t.Errorf("failed run should name the symbol: %q", got)
	}
}
