package main

// The classic Tools menu renders as a launcher page (menutiles.go): a title,
// section headings and icon tiles. These tests pin the parts that are easy to
// get subtly wrong — the grid must fit the panel, the grouping must be stable,
// and a config save must not persist the generated geometry.

import (
	"strings"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// miscSceneIndex finds the Tools/Misc scene in a loaded config.
func miscSceneIndex(t *testing.T, config *Config) int {
	t.Helper()
	for i, scene := range config.Scenes {
		if scene.Name == "Misc" || scene.Name == "Tools" {
			return i
		}
	}
	t.Fatal("the config has no Misc/Tools scene")
	return -1
}

// withRuntimeRows adds the shortcuts the running app injects into the scene
// (FTP & IP and Playtime), so the layout is measured against the menu a device
// actually shows. Both are idempotent: a config that already has them keeps
// one copy each.
func withRuntimeRows(config *Config, idx int) {
	rows := []struct{ text, trigger, scene string }{
		{"FTP & IP", "tools_open", ""},
		{"Playtime & Achievements", "change_scene:Playtime", "Playtime"},
	}
	for _, row := range rows {
		found := false
		for _, elem := range config.Scenes[idx].Elements {
			if strings.EqualFold(elem.Text, row.text) {
				found = true
				break
			}
		}
		if found {
			continue
		}
		config.Scenes[idx].Elements = append(config.Scenes[idx].Elements, Element{
			Type:    "button",
			Text:    row.text,
			Trigger: row.trigger,
			X:       24,
			Y:       347,
			Width:   "296",
			Height:  "55",
		})
	}
}

func TestMenuTilesSceneEligibility(t *testing.T) {
	buttons := []Element{
		{Type: "button", Text: "Terminal", Trigger: "change_scene:Terminal"},
		{Type: "button", Text: "Cron / Tasks", Trigger: "change_scene:Cron"},
		{Type: "button", Text: "Patch", Trigger: "change_scene:Patch"},
		{Type: "button", Text: "FTP & IP", Trigger: "tools_open"},
	}

	cases := []struct {
		name  string
		scene SceneConfig
		want  bool
	}{
		{"tools menu", SceneConfig{Name: "Misc", Elements: buttons}, true},
		{"renamed menu", SceneConfig{Name: "Tools", Elements: buttons}, true},
		{"too short", SceneConfig{Name: "Misc", Elements: buttons[:3]}, false},
		{"carries a list", SceneConfig{Name: "Misc", Elements: append(buttons,
			Element{Type: "textlog"})}, false},
		{"built from labels", SceneConfig{Name: "Misc", Elements: []Element{
			{Type: "label", Text: "Utilities"},
			buttons[0], buttons[1], buttons[2],
		}}, false},
		{"tile home layout", SceneConfig{Name: "Misc", Layout: "home", Elements: buttons}, false},
		{"settings menu", SceneConfig{Name: "Settings", Elements: buttons}, true},
		{"another menu", SceneConfig{Name: "FanControl", Elements: buttons}, false},
		{"button with no trigger", SceneConfig{Name: "Misc", Elements: []Element{
			buttons[0], buttons[1], buttons[2], {Type: "button", Text: "Dead"},
		}}, false},
	}
	for _, tc := range cases {
		if got := menuTilesScene(tc.scene); got != tc.want {
			t.Errorf("%s: menuTilesScene = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestMenuTilesGroupsAndIcons(t *testing.T) {
	cases := []struct {
		elem  Element
		group string
		icon  string
	}{
		{Element{Text: "Terminal", Trigger: "change_scene:Terminal"}, menuTilesSystem, "terminal"},
		{Element{Text: "Cron / Tasks", Trigger: "change_scene:Cron"}, menuTilesSystem, "clock"},
		{Element{Text: "Log Exporter", Trigger: "change_scene:LogExporter"}, menuTilesSystem, "logs"},
		{Element{Text: "Clear Cache", Trigger: "cache_clear"}, menuTilesSystem, "cache"},
		{Element{Text: "Playtime & Achievements", Trigger: "change_scene:Playtime"}, menuTilesSystem, "star"},
		{Element{Text: "Net Speed", Trigger: "change_scene:NetSpeed"}, menuTilesDiagnostics, "speed"},
		{Element{Text: "Benchmark", Trigger: "change_scene:Benchmark"}, menuTilesDiagnostics, "chart"},
		{Element{Text: "Hardware Monitor", Trigger: "change_scene:Hardware"}, menuTilesDiagnostics, "chip"},
		{Element{Text: "Disk Space", Trigger: "change_scene:DiskSpace"}, menuTilesDiagnostics, "disk"},
		{Element{Text: "Patch", Trigger: "change_scene:Patch"}, menuTilesUpdates, "update"},
		{Element{Text: "FTP & IP", Trigger: "tools_open"}, menuTilesUpdates, "network"},
		// A tool nobody anticipated still gets a home on the page.
		{Element{Text: "Fan Control", Trigger: "change_scene:Fan"}, menuTilesMore, ""},
	}
	for _, tc := range cases {
		if got := toolsMenuGroup(tc.elem); got != tc.group {
			t.Errorf("%q: group = %q, want %q", tc.elem.Text, got, tc.group)
		}
		if got := toolsMenuIcon(tc.elem); got != tc.icon {
			t.Errorf("%q: icon = %q, want %q", tc.elem.Text, got, tc.icon)
		}
	}
}

func TestMenuTilesLayoutFitsThePanel(t *testing.T) {
	config := layoutTestConfig(t)
	idx := miscSceneIndex(t, config)
	withRuntimeRows(config, idx)
	scene := config.Scenes[idx]

	layout, ok := menuTilesLayoutFor(config, scene, scene.Elements)
	if !ok {
		t.Fatal("the real Tools menu did not lay out")
	}
	if len(layout.rects) != len(scene.Elements) {
		t.Fatalf("placed %d of %d buttons", len(layout.rects), len(scene.Elements))
	}
	if len(layout.sections) != 3 {
		t.Fatalf("expected the three sections, got %d: %+v", len(layout.sections), layout.sections)
	}

	// Every section is present, in display order, and every tile is accounted
	// for exactly once.
	seen := map[int]bool{}
	for si, sec := range layout.sections {
		if si > 0 && layout.sections[si-1].y >= sec.y {
			t.Errorf("section %q is not below the one before it", sec.title)
		}
		if len(sec.elems) == 0 {
			t.Errorf("section %q has no tiles", sec.title)
		}
		for _, i := range sec.elems {
			if seen[i] {
				t.Errorf("element %d is in two sections", i)
			}
			seen[i] = true
		}
	}

	var first sdl.Rect
	for i, rect := range layout.rects {
		if rect.W != layout.tileW || rect.H != layout.tileH {
			t.Errorf("tile %d is %dx%d, want %dx%d", i, rect.W, rect.H, layout.tileW, layout.tileH)
		}
		if rect.W <= 0 || rect.H <= 0 {
			t.Errorf("tile %d has no area: %+v", i, rect)
		}
		// Tiles stay inside the content panel: below the title band's floor and
		// clear of the footer.
		if rect.X < layout.panel.X || rect.X+rect.W > layout.panel.X+layout.panel.W {
			t.Errorf("tile %d is outside the panel horizontally: %+v", i, rect)
		}
		if rect.Y < layout.bodyTop {
			t.Errorf("tile %d overlaps the page header: %+v", i, rect)
		}
		if bottom := layout.panel.Y + layout.panel.H; rect.Y+rect.H > bottom {
			t.Errorf("tile %d runs past the footer (%d > %d)", i, rect.Y+rect.H, bottom)
		}
		if i == 0 || rect.Y < first.Y || (rect.Y == first.Y && rect.X < first.X) {
			first = rect
		}
		for j, other := range layout.rects {
			if j <= i {
				continue
			}
			if overlapsRect(rect, other) {
				t.Errorf("tiles %d and %d overlap: %+v / %+v", i, j, rect, other)
			}
		}
	}

	// The menu reads in sections, so the first tile of the page is Terminal
	// (SYSTEM) and the update tools sit below the diagnostics.
	terminal := layout.rects[0]
	patch := layout.rects[8]
	if terminal.Y >= patch.Y {
		t.Errorf("Terminal (y=%d) is not above Patch (y=%d)", terminal.Y, patch.Y)
	}
	if layout.sections[0].title != menuTilesSystem {
		t.Errorf("first section is %q, want %q", layout.sections[0].title, menuTilesSystem)
	}
}

// TestMenuTilesSizesCoverTheDevices checks the layout on the panels the app
// runs on, and that a screen too short for the page keeps the authored buttons
// instead of drawing tiles over the footer.
func TestMenuTilesSizesCoverTheDevices(t *testing.T) {
	config := layoutTestConfig(t)
	idx := miscSceneIndex(t, config)
	withRuntimeRows(config, idx)
	scene := config.Scenes[idx]

	cases := []struct {
		w, h int32
		want bool
	}{
		{1280, 720, true}, // Smart Pro
		{1024, 768, true}, // Brick
		{1280, 800, true}, // a taller panel still fits
		{960, 544, false}, // too short for three sections
		{640, 480, false}, // too short and too narrow
	}
	for _, tc := range cases {
		withScreen(t, tc.w, tc.h)
		layout, ok := menuTilesLayoutFor(config, scene, scene.Elements)
		if ok != tc.want {
			t.Errorf("%dx%d: laid out = %v, want %v", tc.w, tc.h, ok, tc.want)
			continue
		}
		if !ok {
			continue
		}
		for i, rect := range layout.rects {
			if rect.X < layout.panel.X || rect.X+rect.W > layout.panel.X+layout.panel.W ||
				rect.Y < layout.bodyTop || rect.Y+rect.H > layout.panel.Y+layout.panel.H {
				t.Errorf("%dx%d: tile %d is outside the panel: %+v", tc.w, tc.h, i, rect)
			}
		}
	}
}

// TestMenuTilesSnapshotDoesNotLeakAcrossReloads proves a reloaded config keeps
// its authored geometry: the snapshot belongs to the element array it was
// taken from, not to a scene index that a reload can reuse.
func TestMenuTilesSnapshotDoesNotLeakAcrossReloads(t *testing.T) {
	before := layoutTestConfig(t)
	idx := miscSceneIndex(t, before)
	withRuntimeRows(before, idx)
	t.Cleanup(func() { menuTilesSnapshot.valid = false })
	menuTilesApply(before, idx)

	// A reload parses the file again, so the scene is a fresh slice with the
	// geometry the author wrote.
	after := layoutTestConfig(t)
	idx = miscSceneIndex(t, after)
	withRuntimeRows(after, idx)
	want := after.Scenes[idx].Elements
	wantY := want[5].Y
	wantStyle := want[5].Style

	menuTilesRestore(after)
	if got := after.Scenes[idx].Elements[5]; got.Y != wantY || got.Style != wantStyle {
		t.Fatalf("a stale snapshot rewrote the reloaded config: %+v, want y=%d style=%q", got, wantY, wantStyle)
	}
}

// TestMenuTilesNavigationFollowsTheGrid checks the focus graph the app builds
// after the layout ran: the D-pad must walk the tiles that are drawn, not the
// authored button positions.
func TestMenuTilesNavigationFollowsTheGrid(t *testing.T) {
	config := layoutTestConfig(t)
	idx := miscSceneIndex(t, config)
	withRuntimeRows(config, idx)
	t.Cleanup(func() { menuTilesSnapshot.valid = false })
	menuTilesApply(config, idx)

	// The top-bar rects are measured while the chrome renders. A scene entered
	// in a test has none, which keeps the graph element-only.
	prevBack, prevStart := sceneTitleBackRect, tb.startRect
	prevClock, prevBell, prevWiFi := statusClockRect, nc.bellRect, statusWiFiRect
	sceneTitleBackRect, tb.startRect = sdl.Rect{}, sdl.Rect{}
	statusClockRect, nc.bellRect, statusWiFiRect = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
	t.Cleanup(func() {
		sceneTitleBackRect, tb.startRect = prevBack, prevStart
		statusClockRect, nc.bellRect, statusWiFiRect = prevClock, prevBell, prevWiFi
	})

	scene := config.Scenes[idx]
	fe := NewFocusEngine()
	fe.SetGraph(scene.Name, fe.BuildGraph(scene))
	if !fe.SetByElementIndex(0) {
		t.Fatal("could not focus the first tool")
	}

	// Right walks the SYSTEM row: Terminal → Cron / Tasks.
	if got := fe.Navigate(ActionNavigateRight); got != 1 {
		t.Errorf("right from Terminal focused element %d, want 1 (Cron / Tasks)", got)
	}
	// Down drops into the diagnostics row under the same column: Cron / Tasks
	// (column 2) → Benchmark.
	if got := fe.Navigate(ActionNavigateDown); got != 3 {
		t.Errorf("down from Cron / Tasks focused element %d, want 3 (Benchmark)", got)
	}
	if got := fe.Navigate(ActionNavigateLeft); got != 2 {
		t.Errorf("left from Benchmark focused element %d, want 2 (Net Speed)", got)
	}
	if got := fe.Navigate(ActionNavigateUp); got != 0 {
		t.Errorf("up from Net Speed focused element %d, want 0 (Terminal)", got)
	}
}

func overlapsRect(a, b sdl.Rect) bool {
	return a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H
}

func TestMenuTilesApplyLeavesOtherScenesAlone(t *testing.T) {
	config := layoutTestConfig(t)
	idx := miscSceneIndex(t, config)
	t.Cleanup(func() { menuTilesSnapshot.valid = false })

	// A menu that is not a Tools page keeps the geometry the author placed.
	other := len(config.Scenes)
	config.Scenes = append(config.Scenes, SceneConfig{Name: "FanControl", Elements: []Element{
		{Type: "button", Text: "Low", Trigger: "fan_low", X: 10, Y: 20, Width: "100", Height: "40"},
		{Type: "button", Text: "High", Trigger: "fan_high", X: 10, Y: 70, Width: "100", Height: "40"},
		{Type: "button", Text: "Auto", Trigger: "fan_auto", X: 10, Y: 120, Width: "100", Height: "40"},
		{Type: "button", Text: "Off", Trigger: "fan_off", X: 10, Y: 170, Width: "100", Height: "40"},
	}})
	menuTilesApply(config, other)
	for i, elem := range config.Scenes[other].Elements {
		if elem.Style == menuTilesStyle {
			t.Errorf("element %d was restyled outside the Tools menu", i)
		}
	}
	if config.Scenes[other].Elements[1].Y != 70 {
		t.Errorf("the authored geometry changed: %+v", config.Scenes[other].Elements[1])
	}
	_ = idx
}

func TestMenuTilesApplyAndSaveRoundTrip(t *testing.T) {
	config := layoutTestConfig(t)
	idx := miscSceneIndex(t, config)
	withRuntimeRows(config, idx)
	t.Cleanup(func() { menuTilesSnapshot.valid = false })

	authored := make([]Element, len(config.Scenes[idx].Elements))
	copy(authored, config.Scenes[idx].Elements)

	menuTilesApply(config, idx)
	styled := 0
	for _, elem := range config.Scenes[idx].Elements {
		if elem.Style != menuTilesStyle {
			t.Errorf("%q was not drawn as a tile", elem.Text)
			continue
		}
		styled++
		if elem.Icon == "" {
			t.Errorf("%q has no glyph", elem.Text)
		}
	}
	if styled != len(authored) {
		t.Fatalf("%d of %d buttons were restyled", styled, len(authored))
	}
	if config.Scenes[idx].Elements[0].X == authored[0].X && config.Scenes[idx].Elements[0].Y == authored[0].Y {
		t.Error("the layout did not move anything")
	}

	// Saving detaches the generated geometry and reattaches it afterwards.
	reattach := menuTilesDetachForSave(config)
	for i, elem := range config.Scenes[idx].Elements {
		if elem.Style != authored[i].Style {
			t.Fatalf("detach left %q styled as %q", elem.Text, elem.Style)
		}
		if elem.X != authored[i].X || elem.Y != authored[i].Y ||
			elem.Width != authored[i].Width || elem.Height != authored[i].Height {
			t.Fatalf("detach did not restore %q: %+v, want %+v", elem.Text, elem, authored[i])
		}
		if elem.Icon != authored[i].Icon {
			t.Fatalf("detach left %q with glyph %q", elem.Text, elem.Icon)
		}
	}
	reattach()
	if config.Scenes[idx].Elements[0].Style != menuTilesStyle {
		t.Error("the page was not re-applied after the save")
	}

	menuTilesRestore(config)
	if config.Scenes[idx].Elements[0].Style != "" {
		t.Error("restore left the scene styled")
	}

	// Detaching an unattached page is a no-op, not a panic.
	menuTilesDetachForSave(config)()
}

// TestMenuTilesRenderProbe draws the page and reads the pixels back: the tiles
// must be painted cards, the focused one must carry the accent ring, and the
// section headings must have text.
func TestMenuTilesRenderProbe(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	config := layoutTestConfig(t)
	idx := miscSceneIndex(t, config)
	withRuntimeRows(config, idx)
	t.Cleanup(func() { menuTilesSnapshot.valid = false })
	t.Cleanup(ClearTextCache)

	prevAccent := accentColor
	accentColor = ColorAccent
	t.Cleanup(func() { accentColor = prevAccent })

	menuTilesApply(config, idx)
	scene := config.Scenes[idx]
	layout, ok := menuTilesLayoutFor(config, scene, scene.Elements)
	if !ok {
		t.Fatal("no layout for the render probe")
	}

	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderMenuTilesDecor(renderer, config, scene)
	const focus = 0
	for i, elem := range scene.Elements {
		renderButtonElement(renderer, config, elem, i, i == focus, false, 0, false)
	}
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("ReadPixels unavailable: %v", err)
	}

	// Every tile is a filled card, not an empty well.
	for i, rect := range layout.rects {
		inner := sdl.Rect{X: rect.X + 8, Y: rect.Y + 8, W: rect.W - 16, H: rect.H - 16}
		if painted := countPixelsMatching(f, inner, func(c sdl.Color) bool {
			return !(c.R == f.bg.R && c.G == f.bg.G && c.B == f.bg.B)
		}); painted < int(inner.W*inner.H)/2 {
			t.Errorf("tile %d is mostly unpainted (%d of %d pixels)", i, painted, inner.W*inner.H)
		}
	}

	// The focused tile's border is the accent, so it must differ from the
	// border of a tile that is not focused.
	focusRect := layout.rects[focus]
	other := layout.rects[1]
	accentPixels := func(rect sdl.Rect) int {
		return countPixelsMatching(f, sdl.Rect{X: rect.X - FocusRing, Y: rect.Y - FocusRing,
			W: rect.W + 2*FocusRing, H: rect.H + 2*FocusRing}, func(c sdl.Color) bool {
			return int(c.R)+int(c.G)+int(c.B) > int(f.bg.R)+int(f.bg.G)+int(f.bg.B)+90
		})
	}
	if accentPixels(focusRect) <= accentPixels(other) {
		t.Error("the focused tile does not stand out from the others")
	}

	// The first section heading has drawn text inside its band.
	sec := layout.sections[0]
	band := sdl.Rect{X: layout.panel.X, Y: sec.y, W: layout.panel.W, H: 24}
	if n := countPixelsMatching(f, band, func(c sdl.Color) bool {
		return int(c.R)+int(c.G)+int(c.B) > int(f.bg.R)+int(f.bg.G)+int(f.bg.B)+60
	}); n < 40 {
		t.Errorf("the section heading band has almost no ink (%d pixels)", n)
	}
}

// settingsSceneIndex finds the Settings menu in a loaded config.
func settingsSceneIndex(t *testing.T, config *Config) int {
	t.Helper()
	for i, scene := range config.Scenes {
		if strings.EqualFold(scene.Name, "Settings") {
			return i
		}
	}
	t.Fatal("the config has no Settings scene")
	return -1
}

// TestSettingsMenuGroupsAndIcons pins the Settings page's own grouping: the
// rows are categories of settings, not tools, so the Tools grouping would file
// every one of them under MORE.
func TestSettingsMenuGroupsAndIcons(t *testing.T) {
	cases := []struct {
		elem  Element
		group string
		icon  string
	}{
		{Element{Text: "General", Trigger: "change_scene:SettingsGeneral"}, menuSettingsSystem, "gear"},
		{Element{Text: "Appearance", Trigger: "change_scene:SettingsAppearance"}, menuSettingsAppearance, "palette"},
		{Element{Text: "Theme Presets", Trigger: "change_scene:ThemePresets"}, menuSettingsAppearance, "star"},
		{Element{Text: "Plugins", Trigger: "change_scene:Plugins"}, menuSettingsSystem, "box"},
		// Categories the config does not carry yet still land in a section and
		// still get a glyph, so growing the menu needs no code change here.
		{Element{Text: "Wi-Fi", Trigger: "change_scene:Wifi"}, menuSettingsNetwork, "network"},
		{Element{Text: "Bluetooth", Trigger: "change_scene:Bluetooth"}, menuSettingsNetwork, "network"},
		{Element{Text: "Sound & Vibration", Trigger: "change_scene:Sound"}, menuSettingsSound, "mic"},
		{Element{Text: "Fan Curve", Trigger: "change_scene:Fan"}, menuSettingsSystem, "gear"},
	}
	for _, tc := range cases {
		if got := settingsMenuGroup(tc.elem); got != tc.group {
			t.Errorf("%q: group = %q, want %q", tc.elem.Text, got, tc.group)
		}
		if got := settingsMenuIcon(tc.elem); got != tc.icon {
			t.Errorf("%q: icon = %q, want %q", tc.elem.Text, got, tc.icon)
		}
	}

	// Every section heading carries a glyph of its own.
	seen := map[string]bool{}
	for _, title := range menuSettingsSectionOrder {
		if glyph := settingsSectionGlyph(title); glyph == "" || seen[glyph] {
			t.Errorf("section %q has a missing or duplicated glyph %q", title, glyph)
		}
		seen[settingsSectionGlyph(title)] = true
	}
}

// TestSettingsMenuPageHeader checks the Settings page opens with the shared
// page header: its own title, glyph and subtitle, and no network read-out (that
// belongs to the Tools page, where it is the page's subject).
func TestSettingsMenuPageHeader(t *testing.T) {
	config := layoutTestConfig(t)
	scene := config.Scenes[settingsSceneIndex(t, config)]
	profile, ok := menuPageProfileFor(scene)
	if !ok {
		t.Fatal("the Settings scene is not drawn as a page")
	}
	if profile.status != nil {
		t.Error("the Settings page asks for a network status read-out")
	}

	h := menuTilesHeader(config, scene, 0, 0, 1000)
	if strings.TrimSpace(h.Title) == "" {
		t.Error("the Settings header has no title")
	}
	if h.Glyph == nil {
		t.Error("the Settings header has no glyph")
	}
	if h.Status != "" {
		t.Errorf("the Settings header carries the status %q", h.Status)
	}
	if h.Subtitle != profile.subtitle {
		t.Errorf("subtitle = %q, want the profile's %q", h.Subtitle, profile.subtitle)
	}
	if h.TitleSize != menuTitleSize {
		t.Errorf("title size = %d, want the shared %d", h.TitleSize, menuTitleSize)
	}
}

// TestSettingsMenuLaysOutAsAPage runs the real Settings scene through the same
// layout the Tools page uses: two sections (appearance, then system), every row
// placed exactly once, and every tile given a glyph.
func TestSettingsMenuLaysOutAsAPage(t *testing.T) {
	config := layoutTestConfig(t)
	idx := settingsSceneIndex(t, config)
	t.Cleanup(func() { menuTilesSnapshot.valid = false })

	menuTilesApply(config, idx)
	scene := config.Scenes[idx]
	layout, ok := menuTilesLayoutFor(config, scene, scene.Elements)
	if !ok {
		t.Fatal("the real Settings menu did not lay out")
	}
	if len(layout.rects) != len(scene.Elements) {
		t.Fatalf("placed %d of %d rows", len(layout.rects), len(scene.Elements))
	}
	if len(layout.sections) != 2 {
		t.Fatalf("expected two sections, got %d: %+v", len(layout.sections), layout.sections)
	}
	if layout.sections[0].title != menuSettingsAppearance {
		t.Errorf("first section is %q, want %q", layout.sections[0].title, menuSettingsAppearance)
	}
	for _, elem := range scene.Elements {
		if elem.Style != menuTilesStyle {
			t.Errorf("%q was not drawn as a tile", elem.Text)
		}
		if elem.Icon == "" {
			t.Errorf("%q has no glyph", elem.Text)
		}
	}
	// Nothing may run over the footer, at any supported size.
	for _, size := range [][2]int32{{1280, 720}, {1024, 768}} {
		withScreen(t, size[0], size[1])
		layout, ok := menuTilesLayoutFor(config, scene, scene.Elements)
		if !ok {
			t.Errorf("%dx%d: the Settings page did not lay out", size[0], size[1])
			continue
		}
		for i, rect := range layout.rects {
			if rect.Y < layout.bodyTop || rect.Y+rect.H > layout.panel.Y+layout.panel.H {
				t.Errorf("%dx%d: tile %d is outside the panel: %+v", size[0], size[1], i, rect)
			}
		}
	}
}

// TestSettingsPageRendersAsAPage draws the page and reads the pixels back: the
// header's glyph well is painted, the tiles are cards, and the section headings
// carry ink, exactly like the Tools page.
func TestSettingsPageRendersAsAPage(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	config := layoutTestConfig(t)
	idx := settingsSceneIndex(t, config)
	t.Cleanup(func() { menuTilesSnapshot.valid = false })
	t.Cleanup(ClearTextCache)

	prevAccent := accentColor
	accentColor = ColorAccent
	t.Cleanup(func() { accentColor = prevAccent })

	menuTilesApply(config, idx)
	scene := config.Scenes[idx]
	layout, ok := menuTilesLayoutFor(config, scene, scene.Elements)
	if !ok {
		t.Fatal("no layout for the render probe")
	}

	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderMenuTilesDecor(renderer, config, scene)
	for i, elem := range scene.Elements {
		renderButtonElement(renderer, config, elem, i, i == 0, false, 0, false)
	}
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("ReadPixels unavailable: %v", err)
	}

	// The header: glyph ink inside the well, title text to its right.
	h := menuTilesHeader(config, scene, layout.panel.X+SpaceXL, layout.titleY, layout.panel.W-2*SpaceXL)
	m := uiPageHeaderMetricsFor(config, h)
	well := sdl.Rect{X: h.X, Y: h.Y, W: m.WellBox, H: m.WellBox}
	if n := countPixelsMatching(f, insetRect(well, 5), func(c sdl.Color) bool {
		return !(c.R == f.bg.R && c.G == f.bg.G && c.B == f.bg.B)
	}); n < 20 {
		t.Errorf("the header glyph well has %d painted pixels, want the Settings glyph", n)
	}

	// The tiles are cards, and the first section heading sits above them.
	for i, rect := range layout.rects {
		inner := insetRect(rect, 8)
		painted := countPixelsMatching(f, inner, func(c sdl.Color) bool {
			return !(c.R == f.bg.R && c.G == f.bg.G && c.B == f.bg.B)
		})
		if painted < int(inner.W*inner.H)/2 {
			t.Errorf("tile %d is mostly unpainted (%d of %d pixels)", i, painted, inner.W*inner.H)
		}
	}
	sec := layout.sections[0]
	band := sdl.Rect{X: layout.panel.X, Y: sec.y, W: layout.panel.W, H: 24}
	if n := countPixelsMatching(f, band, func(c sdl.Color) bool {
		return int(c.R)+int(c.G)+int(c.B) > int(f.bg.R)+int(f.bg.G)+int(f.bg.B)+60
	}); n < 40 {
		t.Errorf("the section heading band has almost no ink (%d pixels)", n)
	}
	for i, rect := range layout.rects {
		if rect.Y < sec.y {
			t.Errorf("tile %d is above its section heading", i)
		}
	}
}
