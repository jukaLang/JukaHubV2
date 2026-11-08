package main

// Tests for the frame the five tool screens share (tbxui.go): the in-memory
// scenes, the focus list, text wrapping, the details sheet, and a render pass
// of every screen — and of the SECURITY.md reader — on the probe renderer.

import (
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

func TestTbxEnsureScenesIsIdempotent(t *testing.T) {
	config := &Config{Scenes: []SceneConfig{{Name: "Main"}}}
	tbxEnsureScenes(config)
	tbxEnsureScenes(config)
	if len(config.Scenes) != 1+len(tbxSceneNames) {
		t.Fatalf("scenes = %d, want Main plus the %d tool screens", len(config.Scenes), len(tbxSceneNames))
	}
	for _, name := range tbxSceneNames {
		n := 0
		for _, s := range config.Scenes {
			if s.Name != name {
				continue
			}
			n++
			if s.Icon != "" {
				t.Errorf("%s has an icon, so the Start menu would list it beside the Tools hub", name)
			}
		}
		if n != 1 {
			t.Errorf("%s appears %d times", name, n)
		}
		if _, ok := lookupScene(name); !ok {
			t.Errorf("%s has no registered module", name)
		}
		if tbxDisplayName(name) == "" || !tbxIsToolScene(name) {
			t.Errorf("%s has no display name", name)
		}
	}
	if tbxDisplayName("Main") != "" || tbxIsToolScene("Main") {
		t.Fatal("Main was taken for a tool screen")
	}
	tbxEnsureScenes(nil)
}

func TestTbxWrapByMeasure(t *testing.T) {
	measure := func(s string) int32 { return int32(len(s)) }
	cases := []struct {
		text string
		w    int32
		want []string
	}{
		{"aaa bbb ccc", 7, []string{"aaa bbb", "ccc"}},
		{"one\n\ntwo", 10, []string{"one", "", "two"}},
		{"abcdefghij", 4, []string{"abcd", "efgh", "ij"}},
		// A wrapped bullet keeps its indent on the continuation lines.
		{"• item with words", 10, []string{"• item", "  with", "  words"}},
	}
	for _, c := range cases {
		got := tbxWrapByMeasure(c.text, c.w, measure)
		if len(got) != len(c.want) {
			t.Errorf("wrap(%q, %d) = %q, want %q", c.text, c.w, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("wrap(%q, %d) = %q, want %q", c.text, c.w, got, c.want)
				break
			}
		}
	}
}

// The focus list walks rows with Up/Down (to the nearest control), side-by-side
// controls with Left/Right, keeps the focus in view, and maps clicks back.
func TestTbxNavWalksRowsAndColumns(t *testing.T) {
	var n tbxNav
	n.begin(100, 200)
	n.add("a", sdl.Rect{X: 0, Y: 0, W: 300, H: 40}, 0, false)
	n.add("b", sdl.Rect{X: 0, Y: 60, W: 180, H: 40}, 1, false)
	n.add("c", sdl.Rect{X: 220, Y: 60, W: 180, H: 40}, 1, false)
	n.add("d", sdl.Rect{X: 240, Y: 300, W: 160, H: 40}, 2, false)
	n.end()
	if n.focus != "a" {
		t.Fatalf("initial focus = %q, want the first control", n.focus)
	}
	steps := []struct {
		move func() bool
		ok   bool
		want string
	}{
		{func() bool { return n.moveRow(1) }, true, "b"},
		{func() bool { return n.moveCol(1) }, true, "c"},
		{func() bool { return n.moveCol(1) }, false, "c"},
		{func() bool { return n.moveRow(1) }, true, "d"},
		{func() bool { return n.moveRow(1) }, false, "d"},
		{func() bool { return n.moveRow(-1) }, true, "c"},
		{func() bool { return n.moveRow(-1) }, true, "a"},
		{func() bool { return n.moveRow(-1) }, false, "a"},
	}
	for i, s := range steps {
		if ok := s.move(); ok != s.ok || n.focus != s.want {
			t.Fatalf("step %d: moved=%v focus=%q, want moved=%v focus=%q", i, ok, n.focus, s.ok, s.want)
		}
	}
	if n.scroll != 0 {
		t.Fatalf("the first row did not scroll back to the top: %d", n.scroll)
	}
	// Up at the top is left for the top bar.
	if tbxNavAction(&n, tbxActUp) {
		t.Fatal("Up at the top was used by the list")
	}
	n.setFocus("d")
	if n.scroll != 140 {
		t.Fatalf("scroll = %d, want the last row revealed at the limit (140)", n.scroll)
	}
	n.setFocus("a")
	if it, ok := n.hit(10, 110); !ok || it.id != "a" {
		t.Fatalf("a click on the first row hit %+v %v", it, ok)
	}
	if _, ok := n.hit(10, 99); ok {
		t.Fatal("a click above the viewport hit a row")
	}
	if _, ok := n.hit(10, 300); ok {
		t.Fatal("a click below the viewport hit a row")
	}
}

func TestTbxSheetFlow(t *testing.T) {
	config := &Config{Scenes: []SceneConfig{{Name: "Main"}}}
	tbxEnsureScenes(config)
	prevScene := currentSceneIndex
	currentSceneIndex = sceneIndexExact(config, tbxSceneName)
	t.Cleanup(func() {
		currentSceneIndex = prevScene
		tbxCloseSheet()
	})
	ran := ""
	tbxOpenSheet(config, tbxSheetSpec{
		token: "t1",
		title: "Test",
		body:  "Body",
		buttons: []tbxSheetButton{
			{label: "Copy", keepOpen: true, run: func() { ran = "copy" }},
			{label: "Done", primary: true, run: func() { ran = "done" }},
		},
		focus: 1,
	})
	if !tbxSheetVisible(config) || tbxSheet.focus != 1 {
		t.Fatalf("sheet visible=%v focus=%d, want visible on the second button", tbxSheetVisible(config), tbxSheet.focus)
	}
	tbxSheetAction(tbxActLeft)
	tbxSheetAction(tbxActConfirm)
	if ran != "copy" || !tbxSheetVisible(config) {
		t.Fatalf("a keep-open button closed the sheet or did not run (ran %q)", ran)
	}
	if !tbxUpdateSheet("t1", "New body", nil) || tbxSheet.spec.body != "New body" {
		t.Fatal("the sheet was not updated through its token")
	}
	if tbxUpdateSheet("other", "x", nil) {
		t.Fatal("a stale token updated the sheet")
	}
	tbxSheetAction(tbxActRight)
	tbxSheetAction(tbxActConfirm)
	if ran != "done" || tbxSheet.open {
		t.Fatalf("Done did not run and close the sheet (ran %q, open %v)", ran, tbxSheet.open)
	}

	// A sheet belongs to the screen that opened it.
	tbxOpenSheet(config, tbxSheetSpec{title: "Left behind"})
	currentSceneIndex = sceneIndexExact(config, partySceneName)
	if tbxSheetVisible(config) || tbxSheet.open {
		t.Fatal("a sheet followed the user to another screen")
	}

	// Back closes the sheet; the screen behind it never sees the press.
	currentSceneIndex = sceneIndexExact(config, tbxSceneName)
	tbxOpenSheet(config, tbxSheetSpec{title: "Back"})
	screenSaw := false
	used := tbxDispatch(config, tbxActBack, func(*Config, tbxAction) bool {
		screenSaw = true
		return true
	})
	if !used || screenSaw || tbxSheet.open {
		t.Fatalf("Back over a sheet: used=%v screenSaw=%v open=%v", used, screenSaw, tbxSheet.open)
	}
}

// Every tool screen draws on the probe renderer with the shipped config, lays
// out focusable controls, has footer hints, and draws a sheet over itself; the
// SECURITY.md reader opens at a section and lists its contents.
func TestTbxScreensRender(t *testing.T) {
	config := layoutTestConfig(t)
	renderer := probeRenderer(t)
	if font, _ := getCachedFont(config, "small"); font == nil {
		t.Fatalf("no usable font in this environment: the shipped Inter-Regular.ttf did not load, so the screens cannot be laid out")
	}
	tbxEnsureScenes(config)

	prevApp, prevScene := appConfig, currentSceneIndex
	prevDisc, prevUpd := partyDiscoveryEnabled, tbxUpdateCheckEnabled
	prevTools, prevAudit, prevOutbox := toolboxToolsDirOverride, auditInstallRootOverride, bugrepOutboxDirOverride
	appConfig = config
	partyDiscoveryEnabled, tbxUpdateCheckEnabled = false, false
	toolboxToolsDirOverride, auditInstallRootOverride, bugrepOutboxDirOverride = t.TempDir(), t.TempDir(), t.TempDir()
	t.Cleanup(func() {
		appConfig, currentSceneIndex = prevApp, prevScene
		partyDiscoveryEnabled, tbxUpdateCheckEnabled = prevDisc, prevUpd
		toolboxToolsDirOverride, auditInstallRootOverride, bugrepOutboxDirOverride = prevTools, prevAudit, prevOutbox
		tbxCloseSheet()
		sectrustDoc = sectrustDocState{}
	})

	for _, name := range tbxSceneNames {
		idx := sceneIndexExact(config, name)
		if idx < 0 {
			t.Fatalf("%s was not added to the config", name)
		}
		currentSceneIndex = idx
		m, ok := lookupScene(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		for pass := 0; pass < 2; pass++ {
			if !m.render(renderer, config) {
				t.Fatalf("%s did not draw itself", name)
			}
		}
		nav := tbxSceneNav(name)
		if nav == nil || len(nav.items) == 0 || nav.focus == "" {
			t.Fatalf("%s laid out no focusable control", name)
		}
		if hints, ok := tbxFooterHints(config, name); !ok || len(hints) == 0 {
			t.Fatalf("%s has no footer hints", name)
		}
		tbxOpenSheet(config, tbxSheetSpec{
			title:   "Sheet",
			body:    "## Heading\n• a bullet that is long enough to wrap onto a second line of the sheet\nText",
			qr:      tbxQRModules("https://example.com/invite"),
			buttons: []tbxSheetButton{{label: "Close", primary: true}},
		})
		m.render(renderer, config)
		if tbxSheet.card.W <= 0 || len(tbxSheet.buttons) != 1 || tbxSheet.buttons[0].W <= 0 {
			t.Fatalf("%s: the sheet was not laid out (card %+v)", name, tbxSheet.card)
		}
		if hints, _ := tbxFooterHints(config, name); len(hints) == 0 || hints[len(hints)-1].Label != "Close" {
			t.Fatalf("%s: the footer does not describe the open sheet: %+v", name, hints)
		}
		tbxCloseSheet()
	}
	// The Audit toolkit detects its tools on a goroutine that reads the install
	// root; let it finish before the cleanup puts the real root back.
	for deadline := time.Now().Add(20 * time.Second); auditScreen.loading && time.Now().Before(deadline); {
		tbxDrainUI()
		time.Sleep(10 * time.Millisecond)
	}

	// The SECURITY.md reader, opened at a section from a row's "Read more".
	currentSceneIndex = sceneIndexExact(config, sectrustSceneName)
	sectrustDoc = sectrustDocState{}
	sectrustOpenDoc("Threat model")
	for deadline := time.Now().Add(5 * time.Second); sectrustDoc.blocks == nil && time.Now().Before(deadline); {
		tbxDrainUI()
		time.Sleep(5 * time.Millisecond)
	}
	if sectrustDoc.blocks == nil {
		t.Fatal("SECURITY.md never finished loading")
	}
	m, _ := lookupScene(sectrustSceneName)
	m.render(renderer, config)
	if len(sectrustDoc.lines) == 0 || sectrustDoc.pageText == "" {
		t.Fatalf("the reader laid out %d lines, page %q", len(sectrustDoc.lines), sectrustDoc.pageText)
	}
	if sectrustDoc.pending != "" || sectrustDoc.nav.scroll <= 0 {
		t.Fatalf("the reader did not jump to the section (pending %q, scroll %d)", sectrustDoc.pending, sectrustDoc.nav.scroll)
	}
	if !sectrustDocAction(config, tbxActY) || !sectrustDoc.toc {
		t.Fatal("Y did not open the contents")
	}
	m.render(renderer, config)
	if len(sectrustDoc.tocNav.items) == 0 || len(sectrustDoc.tocNav.items) != len(sectrustDoc.headings) {
		t.Fatalf("contents rows = %d, headings = %d", len(sectrustDoc.tocNav.items), len(sectrustDoc.headings))
	}
	sectrustDocAction(config, tbxActBack)
	sectrustDocAction(config, tbxActBack)
	if sectrustDoc.open || sectrustDoc.toc {
		t.Fatal("two Backs did not leave the contents and the reader")
	}
}
