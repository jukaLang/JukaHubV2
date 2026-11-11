package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestApkStoreFindBounds(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("a.apk")
	mk("B.APK")
	mk("notes.txt")
	mk("one/b.apk")
	mk("one/two/c.apk")
	mk("one/two/three/too-deep.apk")
	mk(".hidden/secret.apk")
	files, searched, truncated := apkStoreFind(context.Background(), []string{root, filepath.Join(root, "missing")}, 100, 2, time.Minute)
	if truncated {
		t.Error("a small tree was reported truncated")
	}
	if len(searched) != 1 || searched[0] != root {
		t.Errorf("searched = %v", searched)
	}
	got := map[string]bool{}
	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		got[filepath.ToSlash(rel)] = true
	}
	for _, want := range []string{"a.apk", "B.APK", "one/b.apk", "one/two/c.apk"} {
		if !got[want] {
			t.Errorf("missing %s (got %v)", want, got)
		}
	}
	for _, not := range []string{"notes.txt", "one/two/three/too-deep.apk", ".hidden/secret.apk"} {
		if got[not] {
			t.Errorf("found %s, which the bounds exclude", not)
		}
	}
	// The count bound truncates.
	files, _, truncated = apkStoreFind(context.Background(), []string{root}, 2, 2, time.Minute)
	if len(files) != 2 || !truncated {
		t.Errorf("max 2: %d files, truncated=%v", len(files), truncated)
	}
	// A cancelled walk stops at once.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	files, _, truncated = apkStoreFind(ctx, []string{root}, 100, 2, time.Minute)
	if len(files) != 0 || !truncated {
		t.Errorf("cancelled: %d files, truncated=%v", len(files), truncated)
	}
}

func TestApkStoreAnalyzeNativeGame(t *testing.T) {
	dir := t.TempDir()
	p := nativeTestAPK(t, dir, "tunnel.apk", "com.example.tunnel")
	info := apkStoreAnalyze(p, true)
	if info.Tier != apkTierNative {
		t.Fatalf("tier = %v (%s: %s; triage %q, manifest %q)", info.Tier, info.Verdict, info.Why, info.TriageErr, info.ManifestErr)
	}
	if info.Label != "Tunnel Run" || info.Manifest.Package != "com.example.tunnel" {
		t.Errorf("label %q package %q", info.Label, info.Manifest.Package)
	}
	if info.ChosenABI != "arm64-v8a" || info.MainLib != "libgame.so" {
		t.Errorf("abi %q mainLib %q (triage picked %q)", info.ChosenABI, info.MainLib, info.EntryLib)
	}
	if len(info.ABIs) != 2 || info.LibCount != 2 {
		t.Errorf("abis %v libs %d", info.ABIs, info.LibCount)
	}
	if info.Engine != apkEngineNativeActivity {
		t.Errorf("engine %q", info.Engine)
	}
	if info.Icon == nil || info.Icon.Rect.Dx() != apkStoreIconSize || info.IconEntry != "res/mipmap-hdpi-v4/ic_launcher.png" {
		t.Errorf("icon %v from %q", info.Icon != nil, info.IconEntry)
	}
	// Without icons nothing is decoded.
	if bare := apkStoreAnalyze(p, false); bare.Icon != nil {
		t.Error("withIcon=false decoded an icon")
	}
}

func TestApkStoreAnalyzeVerdicts(t *testing.T) {
	dir := t.TempDir()
	manifest := buildAXML(axTestManifest("com.example.app", "Notes", true, ""), false, false)
	java := writeTestAPK(t, dir, "notes.apk", map[string][]byte{
		"AndroidManifest.xml": manifest,
		"classes.dex":         []byte("dex\n035\x00 Lcom/example/Notes;"),
	})
	x86 := writeTestAPK(t, dir, "emu.apk", map[string][]byte{
		"AndroidManifest.xml":   manifest,
		"lib/x86_64/libgame.so": []byte("elf"),
	})
	unity := writeTestAPK(t, dir, "unity.apk", map[string][]byte{
		"AndroidManifest.xml":        manifest,
		"lib/arm64-v8a/libunity.so":  bytes.Repeat([]byte{1}, 100),
		"lib/arm64-v8a/libil2cpp.so": bytes.Repeat([]byte{1}, 200),
	})
	garbage := filepath.Join(dir, "garbage.apk")
	if err := os.WriteFile(garbage, []byte("not a zip at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := map[string]apkStoreTier{java: apkTierJava, x86: apkTierWrongCPU, unity: apkTierJava, garbage: apkTierBroken}
	for p, want := range cases {
		info := apkStoreAnalyze(p, true)
		if info.Tier != want {
			t.Errorf("%s: tier %v, want %v (%s)", filepath.Base(p), info.Tier, want, info.Verdict)
		}
		if info.Verdict == "" || info.Why == "" {
			t.Errorf("%s: no verdict text", filepath.Base(p))
		}
	}
	// A Java start in front of native code: the library exports GameActivity's
	// entry, and the DEX naming GameActivity (across a read boundary) makes it
	// a native game.
	javaStart := buildAXML(axJavaManifest("com.example.jstart"), true, false)
	ga := writeTestAPK(t, dir, "ga.apk", map[string][]byte{
		"AndroidManifest.xml":      javaStart,
		"classes.dex":              append(bytes.Repeat([]byte{'z'}, apkStoreDexChunk-10), []byte("Lcom/google/androidgamesdk/GameActivity;")...),
		"lib/arm64-v8a/libgame.so": testELF64([]string{"android_main", "GameActivity_onCreate"}, true),
	})
	if info := apkStoreAnalyze(ga, false); info.Tier != apkTierNative || info.Engine != apkEngineGameActivity || info.Entry != apkEntryFound {
		t.Errorf("GameActivity across a read boundary: tier %v engine %q entry %v", info.Tier, info.Engine, info.Entry)
	}
	// A Java app carrying a JNI helper library: AndroidRT's runner needs a
	// native entry point, and there is none, so it is a Java app — no Add.
	plain := writeTestAPK(t, dir, "plain.apk", map[string][]byte{
		"AndroidManifest.xml":        javaStart,
		"classes.dex":                []byte("dex Lcom/example/Main;"),
		"lib/arm64-v8a/libhelper.so": testELF64([]string{"Java_com_example_Main_crc32", "JNI_OnLoad"}, true),
	})
	if info := apkStoreAnalyze(plain, false); info.Tier != apkTierJava || info.Entry != apkEntryNone ||
		info.Manifest.Launcher != "com.example.jstart.MainActivity" || info.Tier.canAdd() {
		t.Errorf("native helper with a Java start: tier %v entry %v, launcher %q", info.Tier, info.Entry, info.Manifest.Launcher)
	}
	// The same with the entry point exported, and no DEX hint: may not run,
	// but it can be tried.
	maybe := writeTestAPK(t, dir, "maybe.apk", map[string][]byte{
		"AndroidManifest.xml":      javaStart,
		"classes.dex":              []byte("dex Lcom/example/Main;"),
		"lib/arm64-v8a/libgame.so": testELF64([]string{"ANativeActivity_onCreate"}, true),
	})
	if info := apkStoreAnalyze(maybe, false); info.Tier != apkTierMaybe || info.Entry != apkEntryFound || !info.Tier.canAdd() {
		t.Errorf("native entry with a Java start: tier %v entry %v", info.Tier, info.Entry)
	}
}

// Uncompressed libraries are read through their ELF section headers; the
// answer must match the streamed one, and an ELF with no dynamic symbols
// exports nothing.
func TestApkStoreEntryPointStoredAndStreamed(t *testing.T) {
	dir := t.TempDir()
	javaStart := buildAXML(axJavaManifest("com.example.stored"), true, false)
	storeLibs := func(name string) bool { return strings.HasSuffix(name, ".so") }
	cases := []struct {
		name string
		lib  []byte
		want apkStoreEntryCheck
	}{
		{"entry", testELF64([]string{"foo", "ANativeActivity_onCreate"}, true), apkEntryFound},
		{"jni-glue", testELF64([]string{"Java_com_google_androidgamesdk_GameActivity_initializeNativeCode"}, true), apkEntryFound},
		{"helper", testELF64([]string{"Java_com_x_Y_z", "JNI_OnLoad"}, true), apkEntryNone},
		{"no-dynsym", testELF64([]string{"ANativeActivity_onCreate"}, false), apkEntryNone},
		// A prefix of the name is not the name: the needle ends in its NUL.
		{"prefix-only", testELF64([]string{"ANativeActivity_onCreateX"}, true), apkEntryNone},
	}
	for _, c := range cases {
		for _, stored := range []bool{true, false} {
			var store func(string) bool
			if stored {
				store = storeLibs
			}
			p := writeTestAPKWith(t, dir, c.name+strconv.FormatBool(stored)+".apk", map[string][]byte{
				"AndroidManifest.xml":      javaStart,
				"lib/arm64-v8a/libmain.so": c.lib,
			}, store)
			want := c.want
			if !stored && c.name == "no-dynsym" {
				// Streamed, the bytes are searched without the section
				// headers, so the name in the string table is found.
				want = apkEntryFound
			}
			if info := apkStoreAnalyze(p, false); info.Entry != want {
				t.Errorf("%s (stored=%v): entry %v, want %v (tier %v)", c.name, stored, info.Entry, want, info.Tier)
			}
		}
	}
}

// AndroidRT loads 64-bit libraries only: a 32-bit-only native game is the
// wrong CPU, never "try it".
func TestApkStoreAnalyze32BitOnly(t *testing.T) {
	dir := t.TempDir()
	p := writeTestAPK(t, dir, "old.apk", map[string][]byte{
		"AndroidManifest.xml":        buildAXML(axTestManifest("com.example.old", "Old Game", false, "game"), false, false),
		"lib/armeabi-v7a/libgame.so": bytes.Repeat([]byte{3}, 64),
	})
	info := apkStoreAnalyze(p, false)
	if info.Tier != apkTierWrongCPU || info.Tier.canAdd() || info.ChosenABI != "armeabi-v7a" {
		t.Fatalf("32-bit only: tier %v (%s), abi %q", info.Tier, info.Verdict, info.ChosenABI)
	}
	if !strings.Contains(info.Verdict+info.Why, "64-bit") {
		t.Errorf("the verdict does not say why: %q / %q", info.Verdict, info.Why)
	}
}

// A split app's pieces cannot run alone: the library piece (split=…), and the
// base piece — by its manifest, or by the pieces lying beside it.
func TestApkStoreAnalyzeSplitPieces(t *testing.T) {
	dir := t.TempDir()
	piece := axEl("manifest", []axAttr{
		{name: "package", typ: apkxTypeString, str: "com.example.bundle"},
		{name: "split", typ: apkxTypeString, str: "config.arm64_v8a"},
	}, axEl("application", []axAttr{axBool("hasCode", axIDHasCode, false)}))
	p := writeTestAPK(t, dir, "split_config.arm64_v8a.apk", map[string][]byte{
		"AndroidManifest.xml":      buildAXML(piece, false, false),
		"lib/arm64-v8a/libgame.so": bytes.Repeat([]byte{1}, 64),
	})
	if info := apkStoreAnalyze(p, false); info.Tier != apkTierSplit || info.Manifest.Split != "config.arm64_v8a" || info.Tier.canAdd() {
		t.Errorf("library piece: tier %v split %q", info.Tier, info.Manifest.Split)
	}
	base := writeTestAPK(t, dir, "base.apk", map[string][]byte{
		"AndroidManifest.xml": buildAXML(axTestManifest("com.example.bundle", "Bundle", true, "game"), false, false),
	})
	if info := apkStoreAnalyze(base, false); info.Tier != apkTierSplit {
		t.Errorf("base piece beside its splits: tier %v (%s)", info.Tier, info.Verdict)
	}
	// Play's marker in the base manifest is enough on its own.
	marked := axEl("manifest", []axAttr{{name: "package", typ: apkxTypeString, str: "com.example.marked"}},
		axEl("application", nil,
			axEl("meta-data", []axAttr{
				axString("name", axIDName, "com.android.vending.splits.required"),
				axBool("value", axIDValue, true),
			}),
		),
	)
	m, err := parseApkManifest(buildAXML(marked, true, false))
	if err != nil || !m.SplitsRequired {
		t.Errorf("splits.required meta-data: %v %+v", err, m)
	}
}

func TestApkStoreScanCachesAndSorts(t *testing.T) {
	dir := t.TempDir()
	nativeTestAPK(t, dir, "z-native.apk", "com.example.z")
	writeTestAPK(t, dir, "a-java.apk", map[string][]byte{
		"AndroidManifest.xml": buildAXML(axTestManifest("com.example.a", "Alpha", true, ""), false, false),
	})
	var progress []int
	res := apkStoreScan(context.Background(), []string{dir}, func(done, total int) { progress = append(progress, done) })
	if len(res.apks) != 2 {
		t.Fatalf("%d APKs", len(res.apks))
	}
	if res.apks[0].Tier != apkTierNative {
		t.Errorf("the runnable APK is not first: %v", res.apks[0].Label)
	}
	if len(progress) != 2 || progress[1] != 2 {
		t.Errorf("progress = %v", progress)
	}
	st, _ := os.Stat(res.apks[0].Path)
	if _, ok := apkStoreCached(res.apks[0].Path, st.Size(), st.ModTime()); !ok {
		t.Error("the analysis was not cached")
	}
	if _, ok := apkStoreCached(res.apks[0].Path, st.Size()+1, st.ModTime()); ok {
		t.Error("a changed size still hit the cache")
	}
}

func TestApkStoreFileLabelAndIconRank(t *testing.T) {
	if got := apkStoreFileLabel("/x/super_game-v1.2.apk"); got != "super game v1 2" {
		t.Errorf("file label = %q", got)
	}
	if got := apkStoreFileLabel("/x/.apk"); got != "APK" {
		t.Errorf("empty label = %q", got)
	}
	long := ""
	for i := 0; i < 30; i++ {
		long += "word" + strconv.Itoa(i) + " "
	}
	if r := []rune(apkStoreClipLabel(long)); len(r) > apkStoreMaxLabelRunes {
		t.Errorf("label not bounded: %d runes", len(r))
	}
	if apkStoreIconRank("res/mipmap-xxhdpi/ic_launcher.png") <= apkStoreIconRank("res/drawable/icon.png") {
		t.Error("ic_launcher must outrank icon")
	}
	if apkStoreIconRank("res/drawable/ic_launcher_background.png") != 0 {
		t.Error("an adaptive icon's background is not an icon")
	}
}

func TestApkStoreMemTotal(t *testing.T) {
	p := filepath.Join(t.TempDir(), "meminfo")
	if err := os.WriteFile(p, []byte("MemTotal:        1009128 kB\nMemFree: 1 kB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := apkStoreMemTotalMB(p); got != 985 {
		t.Errorf("MemTotal = %d MB", got)
	}
	if got := apkStoreMemTotalMB(filepath.Join(t.TempDir(), "none")); got != 0 {
		t.Errorf("missing file = %d", got)
	}
}
