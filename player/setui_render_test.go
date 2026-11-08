package main

// setui_render_test.go — the Settings hub drawn for real, on the 1280-wide
// canvas and on the Brick's 960. These are the checks that keep the old page's
// faults from coming back: every row inside its pane and above the footer once
// it is scrolled to, every label of every section drawn whole at the default
// text size, and the rows that used to run out of their buttons (Video snaps,
// the two cookie rows) drawn with nothing cut short at all.
//
// JUKAHUB_SETTINGS_DUMP=<dir> writes each section's frame there as a PNG.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// setHubProbe draws the Settings hub offscreen. One probe keeps one renderer
// for the whole test: the hidden test window takes one renderer at a time.
type setHubProbe struct {
	t        *testing.T
	config   *Config
	scene    int
	w, h     int32
	section  string
	renderer *sdl.Renderer
	frame    *probeFrame
}

// setNewHubProbe makes the probe and gives the test a fresh hub, putting the
// shared hub state, the scene index and the config back afterwards.
func setNewHubProbe(t *testing.T) *setHubProbe {
	t.Helper()
	frame := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	prevHub := setHub
	prevScene, prevConfig := currentSceneIndex, appConfig
	t.Cleanup(func() {
		destroyTestRenderer(renderer)
		setHub = prevHub
		currentSceneIndex, appConfig = prevScene, prevConfig
	})
	return &setHubProbe{t: t, renderer: renderer, frame: frame}
}

// open shows Settings at w×h with section id open and its first row focused,
// the way a deep link from search opens it, and draws it.
func (p *setHubProbe) open(config *Config, sectionID string, w, h int32) {
	p.t.Helper()
	idx := sceneIndexExact(config, "Settings")
	if idx < 0 {
		p.t.Fatal("the config has no Settings scene")
	}
	section := setSectionIndex(sectionID)
	if section < 0 {
		p.t.Fatalf("Settings has no %q section", sectionID)
	}
	p.config, p.scene, p.w, p.h, p.section = config, idx, w, h, sectionID
	currentSceneIndex, appConfig = idx, config
	withScreen(p.t, w, h)
	// A fresh hub is a new visit: the next frame enters it on the section.
	setHub = setHubState{viewSection: -1}
	setHub.lastSection = section
	setHub.deepLink = true
	p.draw()
	if setHub.section != section || !setHub.inPane {
		p.t.Fatalf("%s: the hub opened section %d (rows focused: %v), want %q's rows", p.where(""), setHub.section, setHub.inPane, sectionID)
	}
}

// draw renders frames until the rows' scroll has settled, then reads the last
// one back.
func (p *setHubProbe) draw() {
	p.t.Helper()
	for i := 0; i < 60; i++ {
		p.renderer.SetDrawColor(p.frame.bg.R, p.frame.bg.G, p.frame.bg.B, 255)
		p.renderer.Clear()
		renderScene(p.renderer, p.config, p.config.Scenes[p.scene])
		if setHub.scroll == setHub.target {
			break
		}
	}
	if err := p.renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&p.frame.buf[0]), int(probeW)*4); err != nil {
		p.t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
	}
}

// where names the canvas, the section and a row for a failure message.
func (p *setHubProbe) where(id string) string {
	if id == "" {
		return fmt.Sprintf("%dx%d %s", p.w, p.h, p.section)
	}
	return fmt.Sprintf("%dx%d %s/%s", p.w, p.h, p.section, id)
}

// focus puts the focus on the row with id, scrolls it into view and draws.
// It returns the row's index.
func (p *setHubProbe) focus(id string) int {
	p.t.Helper()
	i := setTestRowIndex(p.t, id)
	if !setHubRowVisible(i) {
		p.t.Fatalf("%s: the row is hidden here", p.where(id))
	}
	setHub.inPane = true
	setHub.row = i
	setHub.follow = true
	p.draw()
	return i
}

// assertRowPlaced checks row i on the last frame: inside the pane's list,
// scrolled fully into it, the list above the footer, and its label drawn.
func (p *setHubProbe) assertRowPlaced(i int) {
	p.t.Helper()
	id := setHub.rows[i].ID
	r, list := setHub.rowRects[i], setHub.listRect
	if r.W <= 0 || r.H <= 0 {
		p.t.Errorf("%s: the row has no rect on the frame", p.where(id))
		return
	}
	if r.X < list.X || r.X+r.W > list.X+list.W {
		p.t.Errorf("%s: row %+v leaves the pane %+v", p.where(id), r, list)
	}
	if r.Y < list.Y || r.Y+r.H > list.Y+list.H {
		p.t.Errorf("%s: row %+v is not scrolled fully into the pane %+v", p.where(id), r, list)
	}
	if footer := p.h - bottomBarHeight(setHub.scene); list.Y+list.H > footer {
		p.t.Errorf("%s: the rows run to y=%d, into the footer at %d", p.where(id), list.Y+list.H, footer)
	}
	if r.X+r.W > p.w {
		p.t.Errorf("%s: row %+v runs off the %dpx canvas", p.where(id), r, p.w)
	}
	// The label starts at the row's left padding; the inset keeps the focus
	// ring out of the count.
	band := sdl.Rect{X: r.X + 18, Y: r.Y + 6, W: r.W / 3, H: r.H - 12}
	if n := setBrightIn(p.frame, band); n < 30 {
		p.t.Errorf("%s: the label drew %d bright pixels in %+v", p.where(id), n, band)
	}
}

// assertRowWhole focuses the row with id and holds it to the strictest rule:
// placed like every row, and nothing on it — label, line under it, value —
// shortened with an ellipsis. It returns the row's index.
func (p *setHubProbe) assertRowWhole(id string) int {
	p.t.Helper()
	i := p.focus(id)
	p.assertRowPlaced(i)
	if clip := setHub.rowClip[i]; clip != 0 {
		p.t.Errorf("%s: text is cut short (label %v, detail %v, value %v): %q / %q / %q", p.where(id),
			clip&setClipLabel != 0, clip&setClipDetail != 0, clip&setClipValue != 0,
			setHub.rows[i].Label, setHub.views[i].detail, setHub.views[i].value)
	}
	return i
}

// dump writes the last frame to dir as <section>_<width>.png.
func (p *setHubProbe) dump(dir string) {
	p.t.Helper()
	if dir == "" {
		return
	}
	path := filepath.Join(dir, fmt.Sprintf("settings_%s_%d.png", p.section, p.w))
	if err := writeFramePNG(path, p.frame.buf); err != nil {
		p.t.Errorf("could not write %s: %v", path, err)
		return
	}
	p.t.Log(path)
}

// setTestRowIndex finds a row of the open section by id.
func setTestRowIndex(t *testing.T, id string) int {
	t.Helper()
	for i, row := range setHub.rows {
		if row.ID == id {
			return i
		}
	}
	t.Fatalf("the open section has no %q row", id)
	return -1
}

// setBrightIn counts near-white pixels inside r: text is drawn in the theme's
// primary ink, far brighter than any glass surface under it.
func setBrightIn(f *probeFrame, r sdl.Rect) int {
	n := 0
	for y := r.Y; y < r.Y+r.H; y++ {
		if y < 0 || y >= probeH {
			continue
		}
		for x := r.X; x < r.X+r.W; x++ {
			if x < 0 || x >= probeW {
				continue
			}
			c := f.at(x, y)
			if int(c.R)+int(c.G)+int(c.B) > 420 {
				n++
			}
		}
	}
	return n
}

// setRenderConfig is the shipped config, injections and all, with motion off
// so a row scrolled to is in place on the next frame.
func setRenderConfig(t *testing.T) *Config {
	t.Helper()
	config := widgetRenderConfig(t)
	config.Variables.PowerMode = ""
	config.Variables.ReducedMotion = true
	return config
}

// setSkipUnlessDefaultTextSize skips a fit check while another test's text
// size is in force: the rows are measured from the fonts, so at 145% a label
// may ellipsize by design.
func setSkipUnlessDefaultTextSize(t *testing.T) {
	t.Helper()
	if s := a11y.fontScale; s != 0 && (s < 0.99 || s > 1.01) {
		t.Skipf("the text size is %.2f; these checks are for the default size", s)
	}
}

// setTestCanvases are the widths the hub has to fit: the Smart Pro's and the
// Brick's.
var setTestCanvases = []int32{1280, 960}

// TestSettingsHubEveryRowFitsBothCanvases opens every section on both canvases
// and walks every row the way the D-pad does: each one scrolls fully into the
// pane, stays above the footer and draws its whole label.
func TestSettingsHubEveryRowFitsBothCanvases(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	setSkipUnlessDefaultTextSize(t)
	config := setRenderConfig(t)
	p := setNewHubProbe(t)
	dumpDir := os.Getenv("JUKAHUB_SETTINGS_DUMP")
	for _, w := range setTestCanvases {
		for _, sec := range setSections() {
			p.open(config, sec.ID, w, 720)
			p.dump(dumpDir)
			shown := 0
			for i := range setHub.rows {
				if !setHubRowVisible(i) {
					continue
				}
				shown++
				id := setHub.rows[i].ID
				p.focus(id)
				p.assertRowPlaced(i)
				if setHub.rowClip[i]&setClipLabel != 0 {
					t.Errorf("%s: the label %q is cut short", p.where(id), setHub.rows[i].Label)
				}
			}
			if shown == 0 {
				t.Errorf("%s: the section shows no rows", p.where(""))
			}
		}
	}
}

// TestSettingsHubOnceCutOffRowsDrawWhole is the report this page was rebuilt
// for: Video snaps and the cookie rows ran out of their buttons. On both
// canvases each now draws its label, its line and its value whole.
func TestSettingsHubOnceCutOffRowsDrawWhole(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	setSkipUnlessDefaultTextSize(t)
	useTempBrowserCookiePath(t)
	config := setRenderConfig(t)
	p := setNewHubProbe(t)
	for _, w := range setTestCanvases {
		p.open(config, "games", w, 720)
		p.assertRowWhole("snaps")
		p.open(config, "privacy", w, 720)
		p.assertRowWhole("cookies")
		p.assertRowWhole("forgetCookies")
		p.open(config, "media", w, 720)
		p.assertRowWhole("resolution")
		p.assertRowWhole("subtitles")
		p.open(config, "general", w, 720)
		p.assertRowWhole("weather")
		p.assertRowWhole("weatherUnit")
	}
}

// TestSettingsHubLegacyPagesOpenTheirSections: the scenes the hub replaced
// still open, each on the section that took its rows, so an old config button,
// a search result or an achievement lands where the setting now lives.
func TestSettingsHubLegacyPagesOpenTheirSections(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	config := setRenderConfig(t)
	p := setNewHubProbe(t)
	for _, c := range []struct{ scene, section string }{
		{"SettingsGeneral", "general"},
		{"SettingsAppearance", "display"},
		{smartSettingsScene, "power"},
	} {
		idx := sceneIndexExact(config, c.scene)
		if idx < 0 {
			t.Fatalf("the config has no %s scene", c.scene)
		}
		p.config, p.scene, p.w, p.h, p.section = config, idx, 1280, 720, c.section
		currentSceneIndex, appConfig = idx, config
		withScreen(t, 1280, 720)
		setHub = setHubState{viewSection: -1}
		p.draw()
		if got := setSections()[setHub.section].ID; got != c.section {
			t.Errorf("%s opened the %q section, want %q", c.scene, got, c.section)
		}
		if !setHub.inPane {
			t.Errorf("%s: the rows are not focused, so the page reads as the rail", c.scene)
		}
		if got := setActiveSectionAlias(); c.scene != smartSettingsScene && got != c.scene {
			t.Errorf("%s: the open section answers to %q", c.scene, got)
		}
	}
}

// TestSettingsHubFooterDescribesTheFocus: the footer follows the control in
// focus, so the controls legend never offers a button that does nothing.
func TestSettingsHubFooterDescribesTheFocus(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	config := setRenderConfig(t)
	p := setNewHubProbe(t)
	p.open(config, "media", 1280, 720)
	hintFor := func(button string) string {
		hints, ok := setFooterHints(config.Scenes[p.scene])
		if !ok {
			t.Fatal("the Settings scene has no footer of its own")
		}
		for _, h := range hints {
			if h.Button == button {
				return h.Label
			}
		}
		return ""
	}
	p.focus("resolution")
	if hintFor("Left/Right") != "Change" {
		t.Errorf("on a carousel the footer offers %q for Left/Right, want Change", hintFor("Left/Right"))
	}
	p.focus("subtitles")
	if got := hintFor("A"); got != "All" {
		t.Errorf("on the subtitle carousel A offers %q, want the full list", got)
	}
	p.focus("downloads")
	if got := hintFor("A"); got != "Open" {
		t.Errorf("on a link row A offers %q, want Open", got)
	}
	setHub.inPane = false
	if got := hintFor("A"); got != "Open" {
		t.Errorf("on the rail A offers %q, want Open", got)
	}
	if got := hintFor("B"); got != "Back" {
		t.Errorf("on the rail B offers %q, want Back", got)
	}
}
