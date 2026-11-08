package main

// inmap_test.go — the input mapping language: what the reader accepts, what
// it reports (with line and column) when it cannot, what validation adds, the
// named actions, and the registry of profile files.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// inmapTestRegistry saves the registry's fields and puts them back after the
// test (the struct holds a mutex, so it is saved field by field), and points
// the profiles folder at a temporary directory.
func inmapTestRegistry(t *testing.T) string {
	t.Helper()
	inmapReg.mu.Lock()
	builtins, user, external := inmapReg.builtins, inmapReg.user, inmapReg.external
	base, auto, autoOn, hotkeyOn := inmapReg.base, inmapReg.auto, inmapReg.autoOn, inmapReg.hotkeyOn
	gen, validGen := inmapReg.gen, inmapReg.validGen
	inmapReg.user, inmapReg.external = nil, nil
	inmapReg.base, inmapReg.auto, inmapReg.autoOn, inmapReg.hotkeyOn = "", "", false, true
	inmapReg.gen++
	inmapReg.mu.Unlock()
	prevDir := inmapProfilesDirHook
	dir := t.TempDir()
	inmapProfilesDirHook = func() (string, error) { return dir, nil }
	t.Cleanup(func() {
		inmapProfilesDirHook = prevDir
		inmapReg.mu.Lock()
		inmapReg.builtins, inmapReg.user, inmapReg.external = builtins, user, external
		inmapReg.base, inmapReg.auto, inmapReg.autoOn, inmapReg.hotkeyOn = base, auto, autoOn, hotkeyOn
		inmapReg.gen, inmapReg.validGen = gen+1, validGen
		inmapReg.mu.Unlock()
	})
	return dir
}

// inmapProblemsText joins a profile's problems for failure messages.
func inmapProblemsText(p *inmapProfile) string {
	var lines []string
	for _, pr := range p.problems {
		kind := "error"
		if pr.warning {
			kind = "warning"
		}
		lines = append(lines, kind+" "+strconv.Itoa(pr.line)+":"+strconv.Itoa(pr.col)+" "+pr.msg)
	}
	return strings.Join(lines, "\n")
}

// TestInmapParsesTheExampleProfile reads the profile the ideas list sketched
// and checks every line became the mapping it says.
func TestInmapParsesTheExampleProfile(t *testing.T) {
	p := inmapParse(`profile: Media browsing
description: Seek with the D-pad
scenes: Tube, MusicPlayer      # or *
map:
  dpad_left            -> seek:-10s
  dpad_right           -> seek:+10s
  l1                   -> key:PageUp
  a hold 500ms         -> action:context_menu
  l1+x                 -> profile:next
  y double             -> action:favorite
  r3                   -> none
`)
	if len(p.parsed) != 0 {
		t.Fatalf("the example has problems:\n%s", inmapProblemsText(p))
	}
	if p.name != "Media browsing" || p.description != "Seek with the D-pad" {
		t.Fatalf("name %q description %q", p.name, p.description)
	}
	if len(p.scenes) != 2 || p.scenes[0] != "Tube" || p.scenes[1] != "MusicPlayer" {
		t.Fatalf("scenes = %v, want [Tube MusicPlayer] (the comment cut off)", p.scenes)
	}
	if len(p.bindings) != 7 {
		t.Fatalf("%d bindings, want 7", len(p.bindings))
	}
	left := p.bindings[0]
	if left.trigger.buttons[0] != inmapBtnLeft || left.target.kind != inmapTargetAction ||
		left.target.action != "seek" || !left.target.hasArg || left.target.arg != -10 {
		t.Fatalf("dpad_left = %+v", left)
	}
	if right := p.bindings[1].target; right.arg != 10 {
		t.Fatalf("seek:+10s read as %v seconds", right.arg)
	}
	if key := p.bindings[2].target; key.kind != inmapTargetKey || key.keyName != "PageUp" {
		t.Fatalf("l1 -> key:PageUp read as %+v", key)
	}
	hold := p.bindings[3]
	if hold.trigger.kind != inmapPressHold || hold.trigger.holdMs != 500 || hold.target.action != "context_menu" {
		t.Fatalf("a hold 500ms = %+v", hold)
	}
	chord := p.bindings[4]
	if len(chord.trigger.buttons) != 2 || chord.trigger.buttons[0] != inmapBtnL1 || chord.trigger.buttons[1] != inmapBtnX ||
		chord.target.kind != inmapTargetProfile || chord.target.profile != "next" {
		t.Fatalf("l1+x = %+v", chord)
	}
	if dbl := p.bindings[5]; dbl.trigger.kind != inmapPressDouble || dbl.target.action != "favorite" {
		t.Fatalf("y double = %+v", dbl)
	}
	if none := p.bindings[6]; none.target.kind != inmapTargetNone || none.trigger.buttons[0] != inmapBtnR3 {
		t.Fatalf("r3 -> none = %+v", none)
	}
}

// TestInmapAcceptsTheIdeasListPhrasing reads "input: d-pad_up →
// action:next_item, modifier:hold_duration_ms:500" and the other spellings a
// person might write.
func TestInmapAcceptsTheIdeasListPhrasing(t *testing.T) {
	cases := []struct {
		line   string
		button inmapButton
		kind   inmapPressKind
		holdMs int
		target inmapTargetKind
	}{
		{"input: d-pad_up → action:next_item, modifier:hold_duration_ms:500", inmapBtnUp, inmapPressHold, 500, inmapTargetAction},
		{"D-Pad-Down => button:A", inmapBtnDown, inmapPressTap, 0, inmapTargetButton},
		{"dpadleft -> b", inmapBtnLeft, inmapPressTap, 0, inmapTargetButton},
		{"LB hold 1s -> home", inmapBtnL1, inmapPressHold, 1000, inmapTargetAction},
		{"x hold 750 ms -> screenshot", inmapBtnX, inmapPressHold, 750, inmapTargetAction},
		{"y hold:0.3s -> quick_settings", inmapBtnY, inmapPressHold, 300, inmapTargetAction},
		{"start x2 -> action:search", inmapBtnStart, inmapPressDouble, 0, inmapTargetAction},
		{"rt -> volume:+5", inmapBtnR2, inmapPressTap, 0, inmapTargetAction},
		{"select -> nothing", inmapBtnSelect, inmapPressTap, 0, inmapTargetNone},
	}
	for _, c := range cases {
		p := inmapParse("profile: T\n" + c.line + "\n")
		if len(p.parsed) != 0 || len(p.bindings) != 1 {
			t.Errorf("%q: %d bindings, problems:\n%s", c.line, len(p.bindings), inmapProblemsText(p))
			continue
		}
		b := p.bindings[0]
		if b.trigger.buttons[0] != c.button || b.trigger.kind != c.kind || b.trigger.holdMs != c.holdMs || b.target.kind != c.target {
			t.Errorf("%q read as %+v", c.line, b)
		}
	}
}

// TestInmapReportsProblemsWithLineAndColumn: every broken line names its line,
// its column and what is wrong (with the likely word for a typo), and costs
// that line only.
func TestInmapReportsProblemsWithLineAndColumn(t *testing.T) {
	cases := []struct {
		line string
		col  int
		want string
	}{
		{"  dpda_up -> button:a", 3, `unknown button "dpda_up" (did you mean dpad_up?)`},
		{"a -> action:contxt_menu", 13, "did you mean context_menu?"},
		{"a -> key:PgUpp", 10, "unknown key"},
		{"a -> telport", 6, "unknown target"},
		{"a hold 50ms -> home", 8, "a hold lasts between"},
		{"a+ -> home", 1, "a chord is written a+b"},
		{"a b -> home", 3, "two buttons in a row"},
		{"a ->", 5, "nothing after the arrow"},
		{"a hold double -> home", 8, "held or double-tapped on one line, not both"},
		{"a -> home:5", 6, "home takes no value"},
		{"a -> seek", 6, "seek needs a time"},
		{"a -> volume:+500", 6, "needs a step between"},
		{"a -> home, modifier:spin", 12, "unknown modifier"},
		{"just words", 1, `expected "button -> target"`},
	}
	for _, c := range cases {
		p := inmapParse("profile: Broken\nb -> button:a\n" + c.line + "\n")
		if len(p.bindings) != 1 {
			t.Errorf("%q: the good line was lost or the bad one kept (%d bindings)", c.line, len(p.bindings))
		}
		if len(p.parsed) != 1 {
			t.Errorf("%q: %d problems, want 1:\n%s", c.line, len(p.parsed), inmapProblemsText(p))
			continue
		}
		pr := p.parsed[0]
		if pr.line != 3 || pr.col != c.col || pr.warning || !strings.Contains(pr.msg, c.want) {
			t.Errorf("%q: problem %d:%d %q (warning %v), want 3:%d containing %q", c.line, pr.line, pr.col, pr.msg, pr.warning, c.col, c.want)
		}
	}
}

// TestInmapReaderIsForgiving: CRLF endings, a byte-order mark, comments in
// both styles, a description holding an arrow, settings in any case and an
// unknown setting (a warning, not an error).
func TestInmapReaderIsForgiving(t *testing.T) {
	src := string(rune(0xFEFF)) + "Profile: Windows file\r\n" +
		"Description: Left -> seek back // not a mapping\r\n" +
		"colour: blue\r\n" +
		"MAP:\r\n" +
		"   A  ->  Button:B   # spaces everywhere\r\n"
	p := inmapParse(src)
	if p.name != "Windows file" || p.description != "Left -> seek back" {
		t.Fatalf("name %q description %q", p.name, p.description)
	}
	if len(p.bindings) != 1 || p.bindings[0].target.button != inmapBtnB {
		t.Fatalf("bindings = %+v", p.bindings)
	}
	if len(p.parsed) != 1 || !p.parsed[0].warning || !strings.Contains(p.parsed[0].msg, "unknown setting") {
		t.Fatalf("problems:\n%s", inmapProblemsText(p))
	}
}

// TestInmapSceneScopes: a profile's scenes, an inline "scene: … /" line, and a
// scene block inside map: that "scene: *" ends.
func TestInmapSceneScopes(t *testing.T) {
	p := inmapParse(`profile: Scoped
scenes: *
map:
  a -> button:b
  scene: Tube / x -> button:y
scene: MusicPlayer, Podcasts
  l1 -> button:r1
scene: *
  r1 -> button:l1
`)
	if len(p.parsed) != 0 {
		t.Fatalf("problems:\n%s", inmapProblemsText(p))
	}
	if !p.everywhere() {
		t.Fatalf("scenes: * read as %v", p.scenes)
	}
	want := [][]string{nil, {"Tube"}, {"MusicPlayer", "Podcasts"}, nil}
	if len(p.bindings) != len(want) {
		t.Fatalf("%d bindings, want %d", len(p.bindings), len(want))
	}
	for i, w := range want {
		got := p.bindings[i].scenes
		if strings.Join(got, ",") != strings.Join(w, ",") || (w == nil) != (got == nil) {
			t.Errorf("line %d scenes = %v, want %v", p.bindings[i].line, got, w)
		}
	}
}

// TestInmapDurations pins the time spellings.
func TestInmapDurations(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int
		ok   bool
	}{
		{"500", 500, true}, {"500ms", 500, true}, {"0.5s", 500, true}, {"1s", 1000, true},
		{"2sec", 2000, true}, {"-10s", -10000, true}, {"+30s", 30000, true}, {"1m", 60000, true},
		{"1.5 seconds", 1500, true}, {"", 0, false}, {"soon", 0, false}, {"ms", 0, false},
		// Minutes end in "s" too: they must not be read as seconds.
		{"5mins", 300000, true}, {"2 minutes", 120000, true}, {"1min", 60000, true},
	} {
		got, ok := inmapParseDurationMs(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("%q = %d, %v; want %d, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// TestInmapValidation: what validation adds on top of the reader — duplicates
// and reserved chords are errors that stop the line, the hotkey chord and
// self-maps and unknown names are warnings, and a profile that leaves no way
// back says so.
func TestInmapValidation(t *testing.T) {
	p := inmapParse(`profile: Checks
map:
  a -> button:b
  a -> button:x
  select+start -> home
  select+r1 -> home
  l1+a hold -> home
  y -> button:y
  x -> profile:Nobody
  scene: Nowhere / r3 -> home
`)
	inmapValidate(p, inmapKnown{profiles: map[string]bool{"checks": true}, scenes: []string{"Main", "Tube"}})
	byLine := map[int][]inmapProblem{}
	for _, pr := range p.problems {
		byLine[pr.line] = append(byLine[pr.line], pr)
	}
	expect := func(line int, warning bool, want string) {
		t.Helper()
		for _, pr := range byLine[line] {
			if pr.warning == warning && strings.Contains(pr.msg, want) {
				return
			}
		}
		t.Errorf("line %d: no %v problem containing %q in:\n%s", line, map[bool]string{true: "warning", false: "error"}[warning], want, inmapProblemsText(p))
	}
	expect(4, false, "already mapped on line 3")
	expect(5, false, "Select + Start quits JukaHub")
	expect(6, true, "switches profiles")
	expect(8, true, "mapped to itself")
	expect(9, true, `no profile called "Nobody"`)
	expect(10, true, `no screen called "Nowhere"`)
	// A held chord reads fine line by line; validation refuses it.
	expect(7, false, "chord cannot also be held")
	ok := map[int]bool{}
	for _, b := range p.bindings {
		ok[b.line] = b.ok
	}
	for line, want := range map[int]bool{3: true, 4: false, 5: false, 6: true, 7: false, 8: true, 9: true, 10: true} {
		if ok[line] != want {
			t.Errorf("line %d runs = %v, want %v", line, ok[line], want)
		}
	}

	// Validating again starts from the reader's findings: nothing doubles up.
	n := len(p.problems)
	inmapValidate(p, inmapKnown{profiles: map[string]bool{"checks": true}, scenes: []string{"Main", "Tube"}})
	if len(p.problems) != n {
		t.Fatalf("a second validation changed %d problems into %d", n, len(p.problems))
	}

	trapped := inmapParse("profile: Trap\nb -> button:x\nselect -> none\n")
	inmapValidate(trapped, inmapKnown{})
	found := false
	for _, pr := range trapped.problems {
		if pr.warning && strings.Contains(pr.msg, "nothing goes back") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a profile with B and Select both taken gave no warning:\n%s", inmapProblemsText(trapped))
	}
}

// TestInmapBuiltinsAreClean: the shipped profiles read and validate without a
// single error (they are the examples), and come in the documented order.
func TestInmapBuiltinsAreClean(t *testing.T) {
	inmapTestRegistry(t)
	list := inmapProfileList()
	want := []string{"Default", "Gaming", "Media browsing", "Left-handed", "One-handed"}
	if len(list) < len(want) {
		t.Fatalf("%d profiles, want at least the %d built in", len(list), len(want))
	}
	scenes := &Config{Scenes: []SceneConfig{{Name: "Main"}, {Name: "Tube"}, {Name: "MusicPlayer"}}}
	inmapEnsureValid(scenes)
	for i, name := range want {
		p := list[i]
		if p.name != name || !p.builtin {
			t.Errorf("profile %d is %q (built in %v), want %q", i, p.name, p.builtin, name)
		}
		if p.errorCount() != 0 {
			t.Errorf("%s has errors:\n%s", p.name, inmapProblemsText(p))
		}
		if i > 0 && p.usableBindings() == 0 {
			t.Errorf("%s maps nothing", p.name)
		}
	}
}

// TestInmapLabels pins how the editor shows triggers and targets.
func TestInmapLabels(t *testing.T) {
	p := inmapParse("profile: L\na hold 500ms -> seek:-10s\nl1+x -> profile:next\ny double -> key:PageUp\nb -> volume:+5\n")
	got := []string{
		inmapTriggerLabel(p.bindings[0].trigger), inmapTargetLabel(p.bindings[0].target),
		inmapTriggerLabel(p.bindings[1].trigger), inmapTargetLabel(p.bindings[1].target),
		inmapTriggerLabel(p.bindings[2].trigger), inmapTargetLabel(p.bindings[2].target),
		inmapTargetLabel(p.bindings[3].target),
	}
	want := []string{"Hold A · 0.5 s", "Seek −10 s", "L1 + X", "Next profile", "Double-tap Y", "Key PageUp", "Volume +5%"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("label %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestInmapActionRegistry: every action has a name, a label and a body, the
// names are unique, aliases land on real actions, and lookups fold case and
// dashes.
func TestInmapActionRegistry(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range inmapActionList {
		if a.name == "" || a.label == "" || a.run == nil {
			t.Errorf("incomplete action %+v", a.name)
		}
		if seen[a.name] {
			t.Errorf("action %q is listed twice", a.name)
		}
		seen[a.name] = true
	}
	for _, a := range inmapActionAliases {
		if !seen[a.name] {
			t.Errorf("alias %q points at the unknown action %q", a.alias, a.name)
		}
		if seen[a.alias] {
			t.Errorf("alias %q hides an action of the same name", a.alias)
		}
	}
	for _, name := range []string{"back", "home", "start_menu", "quick_settings", "screenshot", "next_tab",
		"prev_tab", "page_up", "page_down", "favorite", "search", "volume_up", "volume_down",
		"brightness_up", "brightness_down", "context_menu"} {
		if !seen[name] {
			t.Errorf("the brief's action %q is missing", name)
		}
	}
	if a := inmapFindAction("Quick-Settings"); a == nil || a.name != "quick_settings" {
		t.Fatalf("Quick-Settings found %+v", a)
	}
	if a := inmapFindAction("qs"); a == nil || a.name != "quick_settings" {
		t.Fatalf("the alias qs found %+v", a)
	}
	if inmapFindAction("teleport") != nil {
		t.Fatal("an unknown action was found")
	}
}

// TestInmapButtonTable: the table follows the constants, and every button maps
// back from its SDL code (the triggers have none).
func TestInmapButtonTable(t *testing.T) {
	if inmapButtons[inmapBtnSelect].name != "select" || inmapButtons[inmapBtnRight].name != "dpad_right" ||
		inmapButtons[inmapBtnL2].name != "l2" {
		t.Fatal("the button table is out of step with its constants")
	}
	for i := range inmapButtons {
		info := inmapButtons[i]
		if info.trigger {
			continue
		}
		if b, ok := inmapButtonFromSDL(info.code); !ok || b != inmapButton(i) {
			t.Errorf("%s does not map back from its SDL code (%d → %d, %v)", info.name, info.code, b, ok)
		}
		if b, ok := inmapLookupButton(strings.ToUpper(info.name)); !ok || b != inmapButton(i) {
			t.Errorf("%s is not found by its own name", info.name)
		}
	}
	for alias, want := range map[string]inmapButton{"D-Pad Up": inmapBtnUp, "dpadup": inmapBtnUp, "back": inmapBtnSelect,
		"LB": inmapBtnL1, "zr": inmapBtnR2, "cross": inmapBtnA} {
		if b, ok := inmapLookupButton(alias); !ok || b != want {
			t.Errorf("%q = %v (%v), want %v", alias, b, ok, want)
		}
	}
}

// TestInmapProfileFiles: the folder's files load and sort, a name taken twice
// is renamed with a warning, Duplicate writes a renamed copy, Delete removes a
// user file (and falls the choice back to Default), and a plugin's file
// registers by path.
func TestInmapProfileFiles(t *testing.T) {
	dir := inmapTestRegistry(t)
	clearToastAfterTest(t)
	prevApp := appConfig
	appConfig = nil
	t.Cleanup(func() { appConfig = prevApp })
	write := func(name, text string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("zeta.jkinput", "profile: Zeta\na -> button:b\n")
	write("alpha.jkinput", "map:\nx -> button:y\n")
	write("gaming.jkinput", "profile: Gaming\ny -> button:x\n")
	write("notes.txt", "not a profile")
	inmapReloadFiles()

	if _, err := os.Stat(filepath.Join(dir, "README.txt")); err != nil {
		t.Errorf("the folder got no README: %v", err)
	}
	names := inmapProfileNames()
	joined := strings.Join(names, "|")
	for _, want := range []string{"alpha", "Gaming (2)", "Zeta"} {
		if !strings.Contains(joined, want) {
			t.Errorf("profiles %v miss %q", names, want)
		}
	}
	renamed := inmapFindProfile("Gaming (2)")
	if renamed == nil || renamed.builtin || len(renamed.problems) == 0 || !strings.Contains(renamed.problems[0].msg, "already called") {
		t.Fatalf("the second Gaming = %+v", renamed)
	}

	zeta := inmapFindProfile("zeta")
	copied, err := inmapDuplicate(zeta)
	if err != nil || copied == nil {
		t.Fatalf("duplicate: %v, %v", copied, err)
	}
	if copied.name != "Zeta copy" || !strings.HasPrefix(strings.TrimSpace(copied.text), "profile: Zeta copy") {
		t.Fatalf("the copy is %q:\n%s", copied.name, copied.text)
	}
	if !inmapOwnedByUser(copied) || filepath.Dir(copied.path) != dir {
		t.Fatalf("the copy lives at %q", copied.path)
	}

	inmapChoose(nil, copied, false)
	if err := inmapDelete(nil, copied); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(copied.path); !os.IsNotExist(err) {
		t.Fatalf("the deleted profile's file is still there: %v", err)
	}
	if got := inmapChosen(); got == nil || got.name != inmapDefaultName {
		t.Fatalf("deleting the chosen profile left %+v chosen", got)
	}
	if err := inmapDelete(nil, inmapFindProfile("Gaming")); err == nil {
		t.Fatal("a built-in profile was deleted")
	}

	plugin := filepath.Join(t.TempDir(), "pack.jkinput")
	if err := os.WriteFile(plugin, []byte("profile: Pack controls\nstart hold -> home\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := inmapRegisterProfileFile(plugin); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := inmapRegisterProfileFile(plugin); err != nil {
		t.Fatalf("registering again: %v", err)
	}
	count := 0
	for _, n := range inmapProfileNames() {
		if strings.HasPrefix(n, "Pack controls") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("registering the same file twice listed it %d times", count)
	}
	if p := inmapFindProfile("pack controls"); p == nil || !p.external || inmapOwnedByUser(p) {
		t.Fatalf("the plugin's profile = %+v", p)
	}
	broken := filepath.Join(t.TempDir(), "broken.jkinput")
	if err := os.WriteFile(broken, []byte("nothing here -> works\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := inmapRegisterProfileFile(broken); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("a file with nothing usable registered: %v", err)
	}
	if !inmapActivateByName("PACK CONTROLS") || inmapChosen().name != "Pack controls" {
		t.Fatal("activating by name did not choose the plugin's profile")
	}
	if inmapActivateByName("No such profile") {
		t.Fatal("an unknown name activated something")
	}
}

// TestInmapRenameAndFileNames pins the helpers Duplicate and Export use.
func TestInmapRenameAndFileNames(t *testing.T) {
	if got := inmapRenameText("# mine\nname: Old\nmap:\na -> b\n", "New"); got != "# mine\nprofile: New\nmap:\na -> b\n" {
		t.Errorf("rename = %q", got)
	}
	if got := inmapRenameText("a -> b\n", "New"); got != "profile: New\na -> b\n" {
		t.Errorf("rename without a name line = %q", got)
	}
	for in, want := range map[string]string{"Media browsing": "Media browsing", "a/b:c": "a_b_c", "  ": "profile", "Ünïcode": "_n_code"} {
		if got := inmapFileBase(in); got != want {
			t.Errorf("file base %q = %q, want %q", in, got, want)
		}
	}
	if got := inmapNameFromFile("/x/media_v2.jkinput"); got != "media v2" {
		t.Errorf("name from file = %q", got)
	}
}

// TestInmapSettingsRoundTrip: the switches and the chosen profile are read
// from the config and written back under their keys.
func TestInmapSettingsRoundTrip(t *testing.T) {
	inmapTestRegistry(t)
	prevNav := navmodeState
	t.Cleanup(func() { navmodeState = prevNav })
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{
		inmapKeyProfile: "Gaming", inmapKeyAuto: "true", inmapKeyHotkey: false, "navModeLists": "grid",
	}
	inmapLoadSettings(config)
	if inmapChosen().name != "Gaming" || !inmapAutoEnabled() || inmapHotkeyEnabled() {
		t.Fatalf("loaded chosen %q auto %v hotkey %v", inmapChosen().name, inmapAutoEnabled(), inmapHotkeyEnabled())
	}
	if navmodeFor(navAreaLists) != navModeGrid {
		t.Fatal("the D-pad mode for lists was not loaded")
	}
	inmapSetHotkey(config, true)
	inmapChoose(config, inmapFindProfile("Left-handed"), false)
	if config.Variables.Custom[inmapKeyProfile] != "Left-handed" || config.Variables.Custom[inmapKeyHotkey] != true {
		t.Fatalf("stored %v / %v", config.Variables.Custom[inmapKeyProfile], config.Variables.Custom[inmapKeyHotkey])
	}
}
