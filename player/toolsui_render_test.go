package main

// Offscreen checks for the Tools panel. The panel is drawn with the software
// renderer against a known background so the geometry, the focused-row
// highlight and the "only when the config has a Misc scene" rule are verified
// on real pixels instead of by inspection.

import (
	"fmt"
	"strings"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

func renderToolsProbe(t *testing.T, config *Config, cursor int) (*probeFrame, *sdl.Rect, []sdl.Rect) {
	t.Helper()
	f := newProbeFrame(t)
	if font, _ := getCachedFont(config, "small"); font == nil {
		t.Fatalf("no usable font in this environment: the shipped Inter-Regular.ttf did not load, so this check cannot run")
	}

	prevW, prevH := screenWidth, screenHeight
	prevOpen, prevCursor := toolsPanel.open, toolsPanel.cursor
	screenWidth, screenHeight = probeW, probeH
	toolsPanel.open = true
	toolsPanel.cursor = cursor
	t.Cleanup(func() {
		screenWidth, screenHeight = prevW, prevH
		toolsPanel.open, toolsPanel.cursor = prevOpen, prevCursor
		toolsRowRects = nil
	})

	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)
	// Cached text textures belong to the renderer that created them; each probe
	// makes a fresh renderer, so drop the previous probe's textures.
	ClearTextCache()

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderToolsPanel(renderer, config)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
	}

	panel := toolsPanelRect
	rects := append([]sdl.Rect(nil), toolsRowRects...)
	return f, &panel, rects
}

func bandLuma(f *probeFrame, r sdl.Rect) int {
	if r.W <= 0 || r.H <= 0 {
		return 0
	}
	var sum, n int
	for y := r.Y + 4; y < r.Y+12 && y < r.Y+r.H; y++ {
		for x := r.X + r.W - 120; x < r.X+r.W-24; x++ {
			c := f.at(x, y)
			sum += int(c.R) + int(c.G) + int(c.B)
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / n
}

func TestToolsPanelRenderLayout(t *testing.T) {
	prevAccent := accentColor
	accentColor = ColorAccent
	t.Cleanup(func() { accentColor = prevAccent })

	config := &Config{}
	f, panel, rects := renderToolsProbe(t, config, 0)

	if panel.W != 880 {
		t.Fatalf("panel width = %d, want 880 on a 1280px screen", panel.W)
	}
	if panel.X != (probeW-panel.W)/2 {
		t.Fatalf("panel is not horizontally centred: x=%d w=%d screen=%d", panel.X, panel.W, probeW)
	}
	if panel.Y < 0 || panel.Y+panel.H > probeH {
		t.Fatalf("panel vertically out of bounds: y=%d h=%d screen=%d", panel.Y, panel.H, probeH)
	}

	// The grid scrolls, so only the window that fits is drawn and the rest
	// keep zero rects. The drawn tiles form columns, so instead of a vertical
	// no-overlap walk like a list, every tile is checked to stay inside its
	// row lane and the panel.
	rows := toolsRows(config)
	if len(rects) != len(rows) {
		t.Fatalf("drew %d tile rects for %d rows", len(rects), len(rows))
	}
	drawn := 0
	for i, r := range rects {
		if r.W == 0 && r.H == 0 {
			// Off screen: the grid scrolls, so only the window in the body is
			// drawn and the rest keep zero rects.
			continue
		}
		drawn++
		if r.X < panel.X || r.X+r.W > panel.X+panel.W ||
			r.Y < panel.Y || r.Y+r.H > panel.Y+panel.H {
			t.Fatalf("tile %d %+v escapes panel %+v", i, r, *panel)
		}
	}
	if drawn == 0 {
		t.Fatal("no tiles were drawn")
	}

	// The selected tile must be visibly highlighted against its neighbours.
	if lit, plain := bandLuma(f, rects[0]), bandLuma(f, rects[1]); lit <= plain {
		t.Fatalf("focused tile is not highlighted: focused luma %d <= unfocused %d", lit, plain)
	}

	// The panel body is opaque over the dimmed scene.
	body := f.at(panel.X+panel.W/2, panel.Y+panel.H-4)
	if body.R == f.bg.R && body.G == f.bg.G && body.B == f.bg.B {
		t.Fatal("panel body is transparent")
	}
}

// The tile grid is taller than the panel, so moving the cursor to the last
// tile has to scroll the window and keep the focused tile fully in view.
func TestToolsPanelScrollsToTheCursor(t *testing.T) {
	config := &Config{}
	rows := toolsRows(config)
	if len(rows) < 2 {
		// The scroll behaviour is the subject; a one-row Tools list cannot
		// exercise it, and a skip would hide that the list stopped growing.
		t.Fatalf("the Tools list has %d row(s); the scroll test needs at least two", len(rows))
	}
	prevCursor, prevScroll := toolsPanel.cursor, toolsPanel.scroll
	t.Cleanup(func() { toolsPanel.cursor, toolsPanel.scroll = prevCursor, prevScroll })
	m := toolsPanelMetrics(config, rows)
	g := toolsGridBuild(rows, m)
	if g.bodyH <= m.tileH {
		t.Fatalf("the Tools grid is %dpx tall in a %dpx body; there is nothing to scroll", g.bodyH, m.tileH)
	}
	// One lane in view is the tightest scroll a small panel can give.
	viewH := m.tileH

	toolsPanel.cursor, toolsPanel.scroll = len(rows)-1, 0
	scroll := toolsGridEnsureVisible(g, toolsPanel.cursor, viewH)
	if scroll == 0 {
		t.Fatalf("cursor at the last of %d tiles did not scroll the grid", len(rows))
	}
	last := g.rects[toolsPanel.cursor]
	if last.Y-scroll < 0 || last.Y+last.H-scroll > viewH {
		t.Fatalf("window %d..%d does not cover the focused tile %+v", scroll, scroll+viewH, last)
	}

	// Walking back up keeps the focused tile fully in view; at this tight
	// one-lane view that lands at the tile's own offset, not at zero.
	toolsPanel.cursor = 0
	scroll = toolsGridEnsureVisible(g, toolsPanel.cursor, viewH)
	first := g.rects[toolsPanel.cursor]
	if first.Y-scroll < 0 || first.Y+first.H-scroll > viewH {
		t.Fatalf("window %d..%d does not cover the focused tile %+v", scroll, scroll+viewH, first)
	}
}

// A near-black theme accent must not turn the selected row into a hole.
func TestToolsPanelFocusVisibleWithDarkAccent(t *testing.T) {
	prevAccent := accentColor
	accentColor = sdl.Color{R: 30, G: 41, B: 59, A: 255}
	t.Cleanup(func() { accentColor = prevAccent })

	f, _, rects := renderToolsProbe(t, &Config{}, 0)
	if len(rects) < 2 {
		t.Fatalf("row rects = %d", len(rects))
	}
	if lit, plain := bandLuma(f, rects[0]), bandLuma(f, rects[1]); lit <= plain {
		t.Fatalf("focused row is darker than the others: focused luma %d <= unfocused %d", lit, plain)
	}
}

// While the share is open the panel must say so and show the LAN warning
// without pushing the last row or the footer off the panel.
func TestToolsPanelRunningFTP(t *testing.T) {
	if err := ftpSrv.startAt(t.TempDir(), "127.0.0.1:0"); err != nil {
		t.Fatalf("start FTP: %v", err)
	}
	defer ftpSrv.Stop()

	rows := toolsRows(&Config{})
	ftp := toolsRowByAction(rows, toolsActionFTP)
	if ftp == nil {
		t.Fatal("no FTP row")
	}
	if ftp.value != "Running" {
		t.Fatalf("FTP row value = %q, want Running", ftp.value)
	}
	if ftp.hint != "A Stop" {
		t.Fatalf("FTP row hint = %q, want A Stop", ftp.hint)
	}
	if !strings.HasPrefix(ftp.detail, "ftp://") {
		t.Fatalf("FTP row detail = %q, want the ftp:// address", ftp.detail)
	}

	f, panel, rects := renderToolsProbe(t, &Config{}, 0)
	if len(rects) == 0 {
		t.Fatal("no rows drawn")
	}
	// The warning line is pinned above the footer, so it is checked where the
	// layout puts it rather than under the last row: the list scrolls now.
	layout := toolsPanelMetrics(&Config{}, toolsRows(&Config{}))
	warnTop := panel.Y + panel.H - layout.footH - layout.infoH + 2
	warnBottom := panel.Y + panel.H - layout.footH
	if warnBottom <= warnTop {
		t.Fatalf("no room for the warning: top=%d bottom=%d", warnTop, warnBottom)
	}
	if n := f.countColor(ColorToastWarn, panel.X+24, warnTop, panel.X+panel.W-24, warnBottom); n < 20 {
		t.Fatalf("warning text missing above the footer (only %d accent pixels)", n)
	}
}

func TestToolsRowsWithoutMiscScene(t *testing.T) {
	config := &Config{Scenes: []SceneConfig{{Name: "Home"}}}
	rows := toolsRows(config)
	if len(rows) != 16 {
		t.Fatalf("rows = %d, want the ten FTP & IP rows, party, power tools, toolbox, security, disc helpers and community only", len(rows))
	}
	want := []toolsAction{
		toolsActionCopyIP, toolsActionFTP, toolsActionCompanion, toolsActionSSH,
		toolsActionGatewayScan, toolsActionClipboardScan, toolsActionPing,
		toolsActionTraceroute, toolsActionDNSLookup, toolsActionWiFiSignal,
		toolsActionParty, toolsActionPowerTools, toolsActionToolbox, toolsActionSecurity,
		toolsActionDiscHelpers, toolsActionCommunity,
	}
	for i, action := range want {
		if rows[i].action != action {
			t.Fatalf("row %d = %v, want %v (order: %+v)", i, rows[i].action, action, rows)
		}
	}
	// Most-used first: the copyable address, then the transfer tools and the
	// network probes they go with.
	if rows[0].section != "FTP & IP" || rows[10].section != "WATCH TOGETHER" || rows[11].section != "SYSTEM & UPDATES" {
		t.Fatalf("section headings are wrong: %q %q %q", rows[0].section, rows[10].section, rows[11].section)
	}
	// The probes came out of Power tools; each one must still carry its action
	// hint so the panel reads like the rest of the list.
	for _, action := range want[4:10] {
		row := toolsRowByAction(rows, action)
		if row == nil || row.hint == "" {
			t.Fatalf("probe row %v = %+v, want an action hint", action, row)
		}
	}
	ftp := toolsRowByAction(rows, toolsActionFTP)
	if ftp == nil || ftp.value != "Stopped" {
		t.Fatalf("FTP row = %+v, want Stopped", ftp)
	}
	ssh := toolsRowByAction(rows, toolsActionSSH)
	if ssh == nil || ssh.value != "Stopped" || ssh.hint != "A Start" {
		t.Fatalf("SSH row = %+v, want stopped with a start hint", ssh)
	}
	companion := toolsRowByAction(rows, toolsActionCompanion)
	if companion == nil || companion.value != "Scan to connect" || companion.hint != "A Show QR Code" {
		t.Fatalf("companion row = %+v, want the idle QR hint", companion)
	}
}

func TestToolsRowsWithMiscScene(t *testing.T) {
	config := &Config{Scenes: []SceneConfig{{Name: "Home"}, {Name: "Misc"}}}
	rows := toolsRows(config)
	if len(rows) != 17 {
		t.Fatalf("rows = %d, want a Classic Tools row too", len(rows))
	}
	if rows[len(rows)-1].action != toolsActionUtilities {
		t.Fatalf("last row = %v, want the Classic Tools row", rows[len(rows)-1].action)
	}
}

func TestEnsureToolsMenuEntryRespectsTrigger(t *testing.T) {
	config := &Config{
		Height: 720,
		Scenes: []SceneConfig{{
			Name: "Misc",
			Elements: []Element{
				{Type: "button", Text: "Terminal", Y: 100, Height: "55"},
				{Type: "button", Text: "Network", Y: 200, Height: "55", Trigger: "tools_open"},
			},
		}},
	}
	ensureToolsMenuEntry(config)
	if got := len(config.Scenes[0].Elements); got != 2 {
		t.Fatalf("elements = %d, want an existing tools_open entry to be left alone", got)
	}
}

// While SFTP is running the row and the credentials line must reflect it.
func TestToolsRowsRunningSSH(t *testing.T) {
	t.Setenv("JUKAHUB_SSH_KEY_DIR", t.TempDir())
	if err := sshSrv.startAt(t.TempDir(), "127.0.0.1:0"); err != nil {
		t.Fatalf("start SSH: %v", err)
	}
	defer sshSrv.Stop()

	rows := toolsRows(&Config{})
	ssh := toolsRowByAction(rows, toolsActionSSH)
	if ssh == nil || ssh.value != "Running" || ssh.hint != "A Stop" {
		t.Fatalf("SSH row = %+v, want running with a stop hint", ssh)
	}
	if !strings.HasPrefix(ssh.detail, "sftp://") {
		t.Fatalf("SSH row detail = %q, want an sftp:// address", ssh.detail)
	}

	info := toolsInfoLines()
	if len(info) == 0 || !strings.Contains(info[0].text, sshSrv.Password()) {
		t.Fatalf("credentials line missing the generated password: %+v", info)
	}
	if len(info) > 1 {
		t.Fatalf("info lines = %d, want the single compact line", len(info))
	}
}

// FTP and SFTP running together is the worst case for the panel's height:
// nine rows plus two info lines must still leave the footer its space.
func TestToolsPanelRunningBothServersFits(t *testing.T) {
	t.Setenv("JUKAHUB_SSH_KEY_DIR", t.TempDir())
	if err := sshSrv.startAt(t.TempDir(), "127.0.0.1:0"); err != nil {
		t.Fatalf("start SSH: %v", err)
	}
	defer sshSrv.Stop()
	if err := ftpSrv.startAt(t.TempDir(), "127.0.0.1:0"); err != nil {
		t.Fatalf("start FTP: %v", err)
	}
	defer ftpSrv.Stop()

	config := &Config{Scenes: []SceneConfig{{Name: "Home"}, {Name: "Misc"}}}
	_, panel, rects := renderToolsProbe(t, config, 0)
	if len(rects) != 17 {
		t.Fatalf("tile rects = %d, want 17 with the Misc scene", len(rects))
	}
	if font, _ := getCachedFont(config, "small"); font == nil {
		t.Fatalf("no usable font: the shipped Inter-Regular.ttf did not load, so this check cannot run")
	}
	layout := toolsPanelMetrics(config, toolsRows(config))
	if panel.H > probeH-40 {
		t.Fatalf("panel is %dpx tall on a %dpx screen", panel.H, probeH)
	}
	// The grid is longer than the panel, so it scrolls; what has to hold is
	// that the body still shows tiles and that the credentials line survives.
	if layout.tileH < 96 {
		t.Fatalf("a tile is %dpx, shorter than its minimum height", layout.tileH)
	}
	if len(layout.info) == 0 {
		t.Fatal("compact layout dropped the SFTP credentials line")
	}
}

// The password is shown on the device only: publishing it as a custom variable
// would write it into jukauser.json and any scene text using $ssh_password.
func TestPublishSSHStateOmitsPassword(t *testing.T) {
	t.Setenv("JUKAHUB_SSH_KEY_DIR", t.TempDir())
	if err := sshSrv.startAt(t.TempDir(), "127.0.0.1:0"); err != nil {
		t.Fatalf("start SSH: %v", err)
	}
	defer sshSrv.Stop()

	config := &Config{Variables: Variables{Custom: make(map[string]interface{})}}
	drainCustom(config)
	publishSSHState()
	drainCustom(config)

	if got := config.Variables.Custom["ssh_status"]; got != "running" {
		t.Fatalf("ssh_status = %v, want running", got)
	}
	password := sshSrv.Password()
	for key, value := range config.Variables.Custom {
		if strings.Contains(key, "password") || strings.Contains(fmt.Sprint(value), password) {
			t.Fatalf("custom variable %q leaked the SSH password: %v", key, value)
		}
	}
}

func TestToolsRowsOfflineHint(t *testing.T) {
	// Force the monitor offline rather than hanging the check off whatever the
	// host's connection happens to be: the CI runner is always online, so the
	// skip this replaced meant the offline hint was never tested there at all.
	netMon.mu.Lock()
	prevOnline, prevChecked, prevReason := netMon.online, netMon.checked, netMon.reason
	netMon.online, netMon.checked, netMon.reason = false, true, "test"
	netMon.mu.Unlock()
	t.Cleanup(func() {
		netMon.mu.Lock()
		netMon.online, netMon.checked, netMon.reason = prevOnline, prevChecked, prevReason
		netMon.mu.Unlock()
	})
	if IsOnline() {
		t.Fatal("the offline condition could not be forced; the row check would assert nothing")
	}
	rows := toolsRows(&Config{})
	ip := toolsRowByAction(rows, toolsActionCopyIP)
	if ip == nil || ip.detail != "Offline — connect WiFi first" {
		t.Fatalf("offline IP row = %+v", ip)
	}
	if ip.status != toolsStatusWarn {
		t.Fatalf("offline IP status = %v, want warn", ip.status)
	}
}

// toolsRowByAction finds one row by what it does, so tests do not depend on the
// order of unrelated rows.
func toolsRowByAction(rows []toolsRow, action toolsAction) *toolsRow {
	for i := range rows {
		if rows[i].action == action {
			return &rows[i]
		}
	}
	return nil
}

// Every section heading gets a glyph for its group, and a group this build has
// never seen still gets one, so adding a section can never leave a bare
// heading behind.
func TestToolsSectionGlyphPerGroup(t *testing.T) {
	cases := map[string]toolsAction{
		"FTP & IP":         toolsActionFTP,
		"WATCH TOGETHER":   toolsActionParty,
		"System & updates": toolsActionPatch,
		"":                 toolsActionPowerTools,
		"Something new":    toolsActionPowerTools,
	}
	for title, want := range cases {
		if got := toolsSectionGlyph(title); got != want {
			t.Errorf("toolsSectionGlyph(%q) = %v, want %v", title, got, want)
		}
	}
}

// The header and each section heading open with an icon now: the title has to
// clear the header's glyph well, and the heading chips must carry real glyph
// ink rather than an empty tinted box.
func TestToolsPanelDrawsHeaderAndSectionGlyphs(t *testing.T) {
	prevAccent := accentColor
	accentColor = ColorAccent
	t.Cleanup(func() { accentColor = prevAccent })

	config := &Config{}
	medium, _ := getCachedFont(config, "medium")
	if medium == nil {
		t.Fatalf("no usable font in this environment: the shipped Inter-Regular.ttf did not load, so this check cannot run")
	}
	f, panel, _ := renderToolsProbe(t, config, 0)
	box := int32(medium.Height()) + 10

	// Header: the glyph lives in the well left of the title.
	hdrX, hdrY := panel.X+24, panel.Y+12
	if n := f.countColor(ColorTextPrimary(), hdrX, hdrY, hdrX+box, hdrY+box); n < 12 {
		t.Fatalf("header icon has %d glyph pixels, want the Tools glyph", n)
	}

	// The title starts right of the well instead of on the panel pad.
	titleX := panel.X + 24 + box + 14
	if n := f.countColor(ColorTextPrimary(), titleX, panel.Y+12, titleX+140, panel.Y+12+box); n == 0 {
		t.Fatal("panel title is missing to the right of the header icon")
	}

	// The first section heading opens with its chip at the left of its band.
	rows := toolsRows(config)
	m := toolsPanelMetrics(config, rows)
	g := toolsGridBuild(rows, m)
	if len(g.lines) == 0 || g.lines[0].heading == "" {
		t.Fatal("first grid line has no section heading")
	}
	headTop := panel.Y + m.headerH + g.lines[0].headingY
	tint := appGridTileTint(strings.ToUpper(g.lines[0].heading))
	if n := f.countColor(tint, panel.X+24, headTop, panel.X+64, headTop+m.sectionH); n < 8 {
		t.Fatalf("section heading %q has %d glyph pixels, want its chip icon", g.lines[0].heading, n)
	}
}
