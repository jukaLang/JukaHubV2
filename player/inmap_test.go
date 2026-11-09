package main

// inmap_test.go — the input mapping language: what the reader accepts, what
// it reports (with line and column) when it cannot, what validation adds, and
// that the built-in profiles and the saved settings stay clean.

import (
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
