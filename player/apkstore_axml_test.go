package main

import (
	"encoding/binary"
	"runtime"
	"strings"
	"testing"
)

func TestParseApkManifestNativeActivity(t *testing.T) {
	for _, utf8 := range []bool{false, true} {
		raw := buildAXML(axTestManifest("com.example.tunnel", "Tunnel Run", false, "game"), utf8, false)
		m, err := parseApkManifest(raw)
		if err != nil {
			t.Fatalf("utf8=%v: %v", utf8, err)
		}
		if m.Package != "com.example.tunnel" || m.VersionName != "1.2.3" || m.VersionCode != 42 {
			t.Errorf("utf8=%v: identity = %q %q %d", utf8, m.Package, m.VersionName, m.VersionCode)
		}
		if m.MinSDK != 21 || m.TargetSDK != 33 {
			t.Errorf("utf8=%v: sdk = %d/%d", utf8, m.MinSDK, m.TargetSDK)
		}
		if m.Label != "Tunnel Run" || m.LabelRef != 0 {
			t.Errorf("utf8=%v: label = %q (ref %#x)", utf8, m.Label, m.LabelRef)
		}
		if m.HasCode {
			t.Errorf("utf8=%v: hasCode=false was not read", utf8)
		}
		if m.Launcher != "android.app.NativeActivity" || m.NativeLib != "game" {
			t.Errorf("utf8=%v: launcher %q lib %q", utf8, m.Launcher, m.NativeLib)
		}
		if m.GLES != 0x00030000 {
			t.Errorf("utf8=%v: GLES = %#x", utf8, m.GLES)
		}
		if len(m.Activities) != 1 || m.Activities[0] != "android.app.NativeActivity" {
			t.Errorf("utf8=%v: activities = %v", utf8, m.Activities)
		}
	}
}

// A shrunk manifest keeps the resource map but empties the attribute names;
// the decoder must still find every framework attribute by its id.
func TestParseApkManifestStrippedNames(t *testing.T) {
	raw := buildAXML(axTestManifest("com.example.strip", "Stripped", true, "main"), false, true)
	m, err := parseApkManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.Package != "com.example.strip" || m.VersionName != "1.2.3" || m.Label != "Stripped" {
		t.Errorf("identity from resource map: %+v", m)
	}
	if m.Launcher != "android.app.NativeActivity" || m.NativeLib != "main" || !m.HasCode {
		t.Errorf("launcher from resource map: %q %q hasCode=%v", m.Launcher, m.NativeLib, m.HasCode)
	}
}

// The launcher is the activity with MAIN + LAUNCHER, relative names are
// qualified with the package, and an activity-alias names its target.
func TestParseApkManifestLauncherAndAlias(t *testing.T) {
	launcherFilter := func() *axNode {
		return axEl("intent-filter", nil,
			axEl("action", []axAttr{axString("name", axIDName, "android.intent.action.MAIN")}),
			axEl("category", []axAttr{axString("name", axIDName, "android.intent.category.LAUNCHER")}),
		)
	}
	root := axEl("manifest", []axAttr{{name: "package", typ: apkxTypeString, str: "org.demo.game"}},
		axEl("application", []axAttr{axRef("label", axIDLabel, 0x7f010000), axRef("icon", axIDIcon, 0x7f020000)},
			axEl("activity", []axAttr{axString("name", axIDName, ".Settings")}),
			axEl("activity", []axAttr{axString("name", axIDName, "MainActivity")}),
			axEl("activity-alias", []axAttr{
				axString("name", axIDName, ".Launcher"),
				axString("targetActivity", axIDTargetActivity, "org.demo.game.MainActivity"),
			}, launcherFilter()),
		),
	)
	m, err := parseApkManifest(buildAXML(root, true, false))
	if err != nil {
		t.Fatal(err)
	}
	if m.Launcher != "org.demo.game.MainActivity" {
		t.Errorf("launcher = %q", m.Launcher)
	}
	if m.LabelRef != 0x7f010000 || m.IconRef != 0x7f020000 || m.Label != "" {
		t.Errorf("label/icon refs = %#x %#x %q", m.LabelRef, m.IconRef, m.Label)
	}
	want := []string{"org.demo.game.Settings", "org.demo.game.MainActivity"}
	if strings.Join(m.Activities, ",") != strings.Join(want, ",") {
		t.Errorf("activities = %v", m.Activities)
	}
}

// Every truncation and a sweep of single-byte corruptions of a valid
// manifest must come back as a result or an error — never a panic.
func TestParseApkManifestNeverPanics(t *testing.T) {
	raw := buildAXML(axTestManifest("com.example.fuzz", "Fuzz", false, "game"), false, false)
	for n := 0; n < len(raw); n++ {
		_, _ = parseApkManifest(raw[:n])
	}
	for i := 0; i < len(raw); i += 3 {
		mutated := append([]byte(nil), raw...)
		mutated[i] ^= 0xFF
		_, _ = parseApkManifest(mutated)
	}
	if _, err := parseApkManifest(nil); err == nil {
		t.Error("empty input parsed")
	}
	if _, err := parseApkManifest([]byte("<manifest package=\"x.y\"/>")); err == nil {
		t.Error("text XML parsed as binary XML")
	}
}

// A chunk claiming more strings than the cap, or a size of zero, is refused.
func TestApkxRefusesHostileSizes(t *testing.T) {
	raw := buildAXML(axTestManifest("com.example.big", "Big", false, "game"), false, false)
	// The string pool starts right after the 8-byte XML header; its count is
	// at +8.
	big := append([]byte(nil), raw...)
	binary.LittleEndian.PutUint32(big[8+8:], apkxMaxStrings+1)
	if _, err := parseApkManifest(big); err == nil {
		t.Error("an oversized string count was accepted")
	}
	zero := append([]byte(nil), raw...)
	binary.LittleEndian.PutUint32(zero[8+4:], 0)
	if _, err := parseApkManifest(zero); err == nil {
		t.Error("a zero-size chunk was accepted")
	}
}

func TestApkxResolveLabelAndIcon(t *testing.T) {
	global := []string{"My Game", "res/mipmap-xxhdpi-v4/ic_launcher.png", "res/mipmap-mdpi-v4/ic_launcher.png", "Mon Jeu",
		"res/mipmap-anydpi-v26/ic_launcher.xml"}
	for _, flags := range []uint8{0, apkxTypeFlagSparse, apkxTypeFlagOffset16} {
		arsc := buildArsc(global, 0x7f, []arscTypeT{
			{id: 1, configs: []arscConfigT{
				{lang: "fr", flags: flags, count: 2, entries: map[int]arscEntryT{0: {apkxTypeString, 3}}},
				{flags: flags, count: 2, entries: map[int]arscEntryT{0: {apkxTypeString, 0}, 1: {apkxTypeReference, 0x7f010000}}},
			}},
			{id: 2, configs: []arscConfigT{
				{density: 160, flags: flags, count: 1, entries: map[int]arscEntryT{0: {apkxTypeString, 2}}},
				{density: 480, flags: flags, count: 1, entries: map[int]arscEntryT{0: {apkxTypeString, 1}}},
				{density: 0xFFFE, flags: flags, count: 1, entries: map[int]arscEntryT{0: {apkxTypeString, 4}}},
			}},
		})
		if got := apkxResolveLabel(arsc, 0x7f010000); got != "My Game" {
			t.Errorf("flags=%d: label = %q, want the default locale's", flags, got)
		}
		// A reference to a reference is followed.
		if got := apkxResolveLabel(arsc, 0x7f010001); got != "My Game" {
			t.Errorf("flags=%d: referenced label = %q", flags, got)
		}
		if got := apkxResolveIconPath(arsc, 0x7f020000); got != "res/mipmap-xxhdpi-v4/ic_launcher.png" {
			t.Errorf("flags=%d: icon = %q, want the densest PNG", flags, got)
		}
		if got := apkxResolveLabel(arsc, 0x7f010005); got != "" {
			t.Errorf("flags=%d: a missing entry resolved to %q", flags, got)
		}
		if got := apkxResolveLabel(arsc, 0x01010001); got != "" {
			t.Errorf("flags=%d: another package's id resolved to %q", flags, got)
		}
	}
}

func TestApkxResolveNeverPanics(t *testing.T) {
	arsc := buildArsc([]string{"Name"}, 0x7f, []arscTypeT{
		{id: 1, configs: []arscConfigT{{count: 1, entries: map[int]arscEntryT{0: {apkxTypeString, 0}}}}},
	})
	for n := 0; n < len(arsc); n++ {
		_ = apkxResolveLabel(arsc[:n], 0x7f010000)
	}
	for i := 0; i < len(arsc); i += 2 {
		mutated := append([]byte(nil), arsc...)
		mutated[i] ^= 0xA5
		_ = apkxResolveLabel(mutated, 0x7f010000)
		_ = apkxResolveIconPath(mutated, 0x7f010000)
	}
}

// The launcher filter is judged when it ends: a LAUNCHER category written
// before the MAIN action still makes the activity the launcher.
func TestParseApkManifestCategoryBeforeAction(t *testing.T) {
	root := axEl("manifest", []axAttr{{name: "package", typ: apkxTypeString, str: "org.demo.order"}},
		axEl("application", []axAttr{axBool("hasCode", axIDHasCode, true)},
			axEl("activity", []axAttr{axString("name", axIDName, "android.app.NativeActivity")},
				axEl("intent-filter", nil,
					axEl("category", []axAttr{axString("name", axIDName, "android.intent.category.LAUNCHER")}),
					axEl("action", []axAttr{axString("name", axIDName, "android.intent.action.MAIN")}),
				),
			),
			// MAIN and LAUNCHER in different filters is not a launcher.
			axEl("activity", []axAttr{axString("name", axIDName, ".Other")},
				axEl("intent-filter", nil, axEl("action", []axAttr{axString("name", axIDName, "android.intent.action.MAIN")})),
				axEl("intent-filter", nil, axEl("category", []axAttr{axString("name", axIDName, "android.intent.category.LAUNCHER")})),
			),
		),
	)
	m, err := parseApkManifest(buildAXML(root, false, false))
	if err != nil {
		t.Fatal(err)
	}
	if m.Launcher != "android.app.NativeActivity" {
		t.Errorf("launcher = %q", m.Launcher)
	}
}

// apkxAllocDuring reports the bytes allocated while f runs.
func apkxAllocDuring(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// A manifest whose thousands of attributes all point at one long string must
// decode it once, not once per reference: unbounded, this shape keeps about a
// gigabyte of text alive from a 170 KiB file and the out-of-memory kill takes
// the hub down (no recover() catches that).
func TestParseApkManifestSharedStringBlowUp(t *testing.T) {
	long := strings.Repeat("x", apkxMaxStringUnits)
	attrs := make([]axAttr, 7000)
	for i := range attrs {
		attrs[i] = axAttr{name: long, typ: apkxTypeString, str: long}
	}
	attrs = append(attrs, axAttr{name: "package", typ: apkxTypeString, str: "com.example.hostile"})
	raw := buildAXML(axEl("manifest", attrs), false, false)
	if len(raw) > 512<<10 {
		t.Fatalf("fixture is %d bytes, expected a small file", len(raw))
	}
	var m apkManifest
	var err error
	alloc := apkxAllocDuring(func() { m, err = parseApkManifest(raw) })
	if alloc > 32<<20 {
		t.Errorf("parsing a %d-byte manifest allocated %d MiB", len(raw), alloc>>20)
	}
	// The package attribute sits past the attribute cap: the file is refused
	// for naming no package, which is fine for a hostile file.
	if err == nil && m.Package != "com.example.hostile" {
		t.Errorf("package = %q", m.Package)
	}
}

// The same blow-up through resources.arsc: thousands of configurations of one
// label, each pointing at the same long string.
func TestApkxResolveSharedStringBlowUp(t *testing.T) {
	long := strings.Repeat("y", apkxMaxStringUnits)
	configs := make([]arscConfigT, 0, 20000)
	configs = append(configs, arscConfigT{count: 1, entries: map[int]arscEntryT{0: {apkxTypeString, 0}}})
	for i := 1; i < cap(configs); i++ {
		configs = append(configs, arscConfigT{lang: "zz", count: 1, entries: map[int]arscEntryT{0: {apkxTypeString, 0}}})
	}
	arsc := buildArsc([]string{long}, 0x7f, []arscTypeT{{id: 1, configs: configs}})
	var label string
	alloc := apkxAllocDuring(func() { label = apkxResolveLabel(arsc, 0x7f010000) })
	if alloc > 32<<20 {
		t.Errorf("resolving one label in a %d-byte table allocated %d MiB", len(arsc), alloc>>20)
	}
	if label != long {
		t.Errorf("label has %d bytes, want the default locale's %d", len(label), len(long))
	}
}

// One pool decodes at most apkxMaxDecoded bytes of text, however many
// distinct long strings a file references.
func TestApkxPoolBudget(t *testing.T) {
	strs := make([]string, 0, 700)
	for i := 0; i < cap(strs); i++ {
		strs = append(strs, strings.Repeat(string(rune('a'+i%26)), apkxMaxStringUnits))
	}
	chunk := axPoolChunk(strs, true)
	pool, err := apkxParsePool(chunk, 28)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for i := range strs {
		total += len(pool.get(uint32(i)))
	}
	if total > apkxMaxDecoded {
		t.Errorf("decoded %d bytes, budget %d", total, apkxMaxDecoded)
	}
	if got := pool.get(0); got != strs[0] {
		t.Error("a remembered string changed")
	}
}

func TestApkxQualify(t *testing.T) {
	cases := map[string]string{
		".Main":                      "com.x.Main",
		"Main":                       "com.x.Main",
		"org.libsdl.app.SDLActivity": "org.libsdl.app.SDLActivity",
		"":                           "",
	}
	for in, want := range cases {
		if got := apkxQualify("com.x", in); got != want {
			t.Errorf("apkxQualify(%q) = %q, want %q", in, got, want)
		}
	}
}
