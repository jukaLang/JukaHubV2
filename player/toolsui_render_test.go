package main

// Offscreen checks for the Tools pages: the hub and the Network tools page are
// drawn with the software renderer, and their geometry and touch targets are
// checked on the frame they produce. The report layout behind every result
// sheet is checked here too, against the real Inter face.

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// bandLuma averages the brightness of a band near a rect's top-right corner;
// the render tests of several panels compare a focused row with a resting one.
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

// toolsHubTestFrame draws one frame of the current scene's page and reads the
// pixels back.
func toolsHubTestFrame(t *testing.T, renderer *sdl.Renderer, draw func(*sdl.Renderer)) *probeFrame {
	t.Helper()
	f := &probeFrame{buf: make([]byte, probeW*probeH*4), bg: sdl.Color{R: 9, G: 14, B: 25, A: 255}}
	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	draw(renderer)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
	}
	return f
}

// TestToolsHubRendersThePage draws the hub at the canvases the app runs on: the
// chip sits in the header's top-right corner clear of every tile, the tiles stay
// inside the body, Watch Party spans the row, and every visible tile is a
// painted card.
func TestToolsHubRendersThePage(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	config := toolsHubTestNav(t)
	renderer := probeRenderer(t)
	prevEngine := focusEngine
	focusEngine = NewFocusEngine()
	t.Cleanup(func() { focusEngine = prevEngine })
	toolsHubPinIP(t, "192.168.1.20")
	toolsHubPinOnline(t, true)
	currentSceneIndex = sceneIndexExact(config, toolsHubSceneName)
	// The slow values are read: drawing must not wait on the disk.
	toolsHub.slow = toolsHubSlowValues{toolbox: "2 installed", audit: "0 of 9 installed", security: "Looks good", at: time.Now()}

	for _, size := range []struct{ w, h int32 }{{probeW, probeH}, {1024, 768}, {960, 720}} {
		withScreen(t, size.w, size.h)
		toolsHub.tiles = nil
		toolsHub.page = newToolsHubPage()
		f := toolsHubTestFrame(t, renderer, func(r *sdl.Renderer) { renderToolsHub(r, config) })

		p := &toolsHub.page
		// The page stands on Home's backdrop, so a drawn card is told apart
		// from it by its glass: the card's middle differs from the side margin
		// level with it.
		if size.w == probeW {
			hero := p.tileRects[0]
			inside := f.at(hero.X+hero.W/2, hero.Y+hero.H/2)
			outside := f.at(hero.X-4, hero.Y+hero.H/2)
			if inside == outside {
				t.Errorf("Watch Party's card is not drawn: its middle %+v matches the margin beside it %+v", inside, outside)
			}
		}
		chip := p.chipRect
		if chip.W <= 0 || chip.X+chip.W > size.w || chip.Y < headerBarHeight() || chip.Y+chip.H > p.view.Y {
			t.Fatalf("%dx%d: chip %+v is not in the header's corner (body starts at %d)", size.w, size.h, chip, p.view.Y)
		}
		if len(p.tileRects) != len(toolsHub.tiles) || len(toolsHub.tiles) == 0 {
			t.Fatalf("%dx%d: %d tile targets for %d tiles", size.w, size.h, len(p.tileRects), len(toolsHub.tiles))
		}
		hero := p.tileRects[0]
		if hero.W != p.view.W {
			t.Errorf("%dx%d: Watch Party is %dpx wide in a %dpx body", size.w, size.h, hero.W, p.view.W)
		}
		visible := 0
		for i, r := range p.tileRects {
			if r.W == 0 {
				continue
			}
			visible++
			if r.X < p.view.X || r.X+r.W > p.view.X+p.view.W || r.Y < p.view.Y || r.Y+r.H > p.view.Y+p.view.H {
				t.Errorf("%dx%d: tile %q %+v leaves the body %+v", size.w, size.h, toolsHub.tiles[i].id, r, p.view)
			}
			if overlapsRect(r, chip) {
				t.Errorf("%dx%d: tile %q overlaps the chip", size.w, size.h, toolsHub.tiles[i].id)
			}
		}
		if visible < 4 {
			t.Errorf("%dx%d: only %d tiles visible on the first screen", size.w, size.h, visible)
		}
	}

	// Offline the chip says so, Watch Party is dimmed with its tag, and the
	// page still draws every tile.
	withScreen(t, probeW, probeH)
	toolsHubPinIP(t, "")
	toolsHubPinOnline(t, false)
	toolsHub.tiles = nil
	toolsHubTestFrame(t, renderer, func(r *sdl.Renderer) { renderToolsHub(r, config) })
	if party := toolsHub.tiles[toolsHubIndexOf(toolsHub.tiles, "party")]; party.blocked != toolsHubTagWiFi {
		t.Fatalf("offline Watch Party = %+v, want it dimmed for Wi-Fi", party)
	}
	if toolsHub.page.chipRect.W <= 0 {
		t.Fatal("offline the chip disappeared; it should read No Wi-Fi")
	}

	// A tap on the chip copies (or explains); a tap on a tile focuses and opens.
	clearToastAfterTest(t)
	c := toolsHub.page.chipRect
	if !toolsHubClick(c.X+c.W/2, c.Y+c.H/2, config) || !toolsHub.page.chip {
		t.Fatal("a tap on the chip was not taken")
	}
	if !strings.Contains(currentToast.Message, "Wi-Fi") {
		t.Fatalf("a tap on the offline chip said %q", currentToast.Message)
	}
	party := toolsHub.page.tileRects[0]
	if !toolsHubClick(party.X+party.W/2, party.Y+party.H/2, config) || toolsHub.page.chip || toolsHub.page.cursor != 0 {
		t.Fatal("a tap on Watch Party did not take the focus")
	}
	if transitionPhase != "none" {
		t.Fatal("a tap on a dimmed tile navigated")
	}
	if toolsHubClick(2, probeH-2, config) {
		t.Fatal("a tap on nothing was taken")
	}
}

// TestNetToolsRendersThePageAndTheSheet draws the Network tools page and a
// result sheet: every tile has a target inside the body, and the sheet's
// report and pills sit on its card.
func TestNetToolsRendersThePageAndTheSheet(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	config := toolsHubTestNav(t)
	netToolsTestJobs(t)
	renderer := probeRenderer(t)
	t.Setenv("JUKAHUB_GATEWAY", "192.168.1.1")
	toolsHubPinIP(t, "192.168.1.20")
	toolsHubPinOnline(t, true)
	currentSceneIndex = sceneIndexExact(config, netToolsSceneName)
	netTools.tiles = nil
	netTools.page = newToolsHubPage()
	netTools.sheet = toolsHubSheet{}

	for _, size := range []struct{ w, h int32 }{{probeW, probeH}, {960, 720}} {
		withScreen(t, size.w, size.h)
		toolsHubTestFrame(t, renderer, func(r *sdl.Renderer) { renderNetTools(r, config) })
		p := &netTools.page
		if len(p.tileRects) != len(netToolsSpecs) {
			t.Fatalf("%dx%d: %d tile targets for %d tools", size.w, size.h, len(p.tileRects), len(netToolsSpecs))
		}
		for i, r := range p.tileRects {
			if r.W == 0 {
				continue
			}
			if r.X < p.view.X || r.X+r.W > p.view.X+p.view.W || r.Y < p.view.Y || r.Y+r.H > p.view.Y+p.view.H {
				t.Errorf("%dx%d: tile %q %+v leaves the body %+v", size.w, size.h, netTools.tiles[i].id, r, p.view)
			}
		}
		if netTools.gateway != "192.168.1.1" {
			t.Errorf("the page did not read the router's address (%q)", netTools.gateway)
		}
	}

	withScreen(t, probeW, probeH)
	netToolsSetResult("listening", netToolsResult{
		text: "22      0.0.0.0, ::            -\n8080    127.0.0.1              4242\n\n2 TCP ports listening",
		at:   time.Now(),
	})
	netToolsShowSheet("listening")
	toolsHubTestFrame(t, renderer, func(r *sdl.Renderer) { renderNetTools(r, config) })
	s := &netTools.sheet
	if s.card.W <= 0 || s.card.X < 0 || s.card.X+s.card.W > probeW || s.card.Y+s.card.H > probeH {
		t.Fatalf("sheet card %+v leaves the screen", s.card)
	}
	if len(s.lines) == 0 || s.fits <= 0 {
		t.Fatalf("the sheet laid out %d lines with room for %d", len(s.lines), s.fits)
	}
	if len(s.pillRects) != len(s.pills) || len(s.pills) < 2 {
		t.Fatalf("%d pill targets for %d pills", len(s.pillRects), len(s.pills))
	}
	for i, r := range s.pillRects {
		if !pointInRect(r.X, r.Y, s.card) || !pointInRect(r.X+r.W-1, r.Y+r.H-1, s.card) || r.H < 36 {
			t.Errorf("pill %q %+v is not a target on the card %+v", s.pills[i].id, r, s.card)
		}
	}
	closeRect := s.pillRects[len(s.pillRects)-1]
	if !netToolsClick(closeRect.X+closeRect.W/2, closeRect.Y+closeRect.H/2, config) || s.open {
		t.Fatal("tapping Close did not close the sheet")
	}
}

// toolsHubTestFont is the report face the sheets use.
func toolsHubTestFont(t *testing.T) *ttf.Font {
	t.Helper()
	newProbeFrame(t)
	font := toolsHubSheetFont(&Config{})
	if font == nil {
		t.Fatal("no usable font: the shipped Inter face did not load, so the layout cannot be measured")
	}
	return font
}

// TestToolsHubClassifyReadsTheReports: the reports are written for a terminal;
// the classifier finds the pairs, the tables and the prose in them.
func TestToolsHubClassifyReadsTheReports(t *testing.T) {
	text := "Public IP: 203.0.113.9\nName servers: a.example\n    b.example\n\n" +
		"IP               MAC                 DEVICE\n192.168.1.1      a4:2b:8c:00:11:22   wlan0\n" +
		"64 bytes from 1.1.1.1: icmp_seq=1 ttl=57 time=12.3 ms\nRead-only: nothing was changed."
	raw := toolsHubClassify(text)
	want := []toolsHubLineKind{
		toolsHubLineKV, toolsHubLineKV, toolsHubLineCont, toolsHubLineBlank,
		toolsHubLineTable, toolsHubLineTable, toolsHubLinePara, toolsHubLineKV,
	}
	if len(raw) != len(want) {
		t.Fatalf("classified %d lines, want %d: %+v", len(raw), len(want), raw)
	}
	for i := range want {
		if raw[i].kind != want[i] {
			t.Errorf("line %d kind = %d, want %d (%+v)", i, raw[i].kind, want[i], raw[i])
		}
	}
	if raw[0].label != "Public IP" || raw[0].value != "203.0.113.9" {
		t.Errorf("pair = %+v", raw[0])
	}
	if raw[2].value != "b.example" {
		t.Errorf("continuation = %+v", raw[2])
	}
	if !toolsHubIsHeaderRow(raw[4].cells) || toolsHubIsHeaderRow(raw[5].cells) {
		t.Error("the table header was not told apart from its rows")
	}
	if len(toolsHubClassify("")) != 1 {
		t.Error("an empty report is not one blank line")
	}
}

// TestToolsHubLayoutAlignsAndWraps: pairs share a value column, table cells
// line up, and nothing runs past the width — not even a token with no spaces.
func TestToolsHubLayoutAlignsAndWraps(t *testing.T) {
	font := toolsHubTestFont(t)
	const width = int32(420)

	lines := toolsHubLayoutReport(font, "Status: Running\nAddress: ftp://192.168.1.20:2121\nSign in: any user name", width)
	if len(lines) != 3 {
		t.Fatalf("pairs laid out as %d lines", len(lines))
	}
	valueX := lines[0].runs[1].x
	for i, line := range lines {
		if len(line.runs) != 2 || line.runs[1].x != valueX {
			t.Errorf("pair %d does not share the value column: %+v", i, line.runs)
		}
	}

	table := toolsHubLayoutReport(font, "PORT    ADDRESS      PID\n22      0.0.0.0      -\n8080    127.0.0.1    4242", width)
	if len(table) != 3 {
		t.Fatalf("table laid out as %d lines", len(table))
	}
	for c := 0; c < 3; c++ {
		if table[0].runs[c].x != table[1].runs[c].x || table[1].runs[c].x != table[2].runs[c].x {
			t.Errorf("column %d does not line up: %d %d %d", c, table[0].runs[c].x, table[1].runs[c].x, table[2].runs[c].x)
		}
	}
	if table[0].runs[0].color != ColorTextTertiary() {
		t.Error("the header row is not drawn in the label ink")
	}

	token := strings.Repeat("QUJDREVGR0hJSktMTU5PUA", 8)
	pieces := toolsHubWrap(font, token, 150)
	if len(pieces) < 2 || strings.Join(pieces, "") != token {
		t.Fatalf("a long token was cut into %d pieces that do not rebuild it", len(pieces))
	}
	for _, piece := range pieces {
		if w := toolsHubTextWidth(font, piece); w > 150 {
			t.Errorf("token piece %q is %dpx, past 150px", piece, w)
		}
	}
	words := strings.TrimSpace(strings.Repeat("the quick brown fox ", 12))
	wrapped := toolsHubWrap(font, words, width)
	if len(wrapped) < 2 || strings.Join(wrapped, " ") != words {
		t.Fatalf("prose wrapped into %d lines that do not rebuild it", len(wrapped))
	}
	for _, line := range wrapped {
		if w := toolsHubTextWidth(font, line); w > width {
			t.Errorf("line %q is %dpx, past %dpx", line, w, width)
		}
	}
	if got := toolsHubWrap(nil, "no font", 10); len(got) != 1 || got[0] != "no font" {
		t.Errorf("wrap without a font = %v", got)
	}
	if got := toolsHubWrap(font, "   ", 100); len(got) != 1 || got[0] != "" {
		t.Errorf("wrap of blanks = %q", got)
	}
}

// TestToolsHubSheetControls: the sheet scrolls within what the last frame
// could show, walks its pills without wrapping, and hit-tests them.
func TestToolsHubSheetControls(t *testing.T) {
	s := toolsHubSheet{
		lines: make([]toolsHubTextLine, 10),
		fits:  4,
		pills: []toolsHubPill{{id: "run"}, {id: "copy"}, {id: "close"}},
	}
	if !s.scrollBy(3) || s.scroll != 3 {
		t.Fatalf("scroll = %d", s.scroll)
	}
	if !s.scrollBy(50) || s.scroll != 6 {
		t.Fatalf("scroll past the end = %d, want 6", s.scroll)
	}
	if s.scrollBy(1) {
		t.Fatal("scrolling at the end reported movement")
	}
	if !s.scrollBy(-50) || s.scroll != 0 {
		t.Fatalf("scroll past the top = %d", s.scroll)
	}
	s.movePill(-1)
	if s.focus != 0 {
		t.Fatalf("Left from the first pill moved to %d", s.focus)
	}
	s.movePill(1)
	s.movePill(1)
	s.movePill(1)
	if s.focus != 2 || s.focusedPill() != "close" {
		t.Fatalf("Right past the last pill: focus %d (%q)", s.focus, s.focusedPill())
	}
	s.pillRects = []sdl.Rect{{X: 0, Y: 0, W: 80, H: 40}, {X: 100, Y: 0, W: 80, H: 40}, {X: 200, Y: 0, W: 80, H: 40}}
	if got := s.hitPill(140, 20); got != "copy" {
		t.Fatalf("hit = %q, want copy", got)
	}
	if got := s.hitPill(140, 300); got != "" {
		t.Fatalf("a tap far below the pills hit %q", got)
	}

	// Replacing the text drops the old layout; the same text keeps it.
	s.setText("A: b")
	s.lines = []toolsHubTextLine{{}}
	s.setText("A: b")
	if s.lines == nil {
		t.Fatal("setting the same text threw the layout away")
	}
	s.setText("C: d")
	if s.lines != nil || s.text != "C: d" {
		t.Fatal("new text kept the old layout")
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

// The SFTP sheet shows the session's password on the device, which is where
// the user needs it, and the address to type.
func TestNetToolsSSHSheetShowsTheSession(t *testing.T) {
	t.Setenv("JUKAHUB_SSH_KEY_DIR", t.TempDir())
	if err := sshSrv.startAt(t.TempDir(), "127.0.0.1:0"); err != nil {
		t.Fatalf("start SSH: %v", err)
	}
	defer sshSrv.Stop()
	prev := netTools.sheet
	prevID, prevKey := netTools.sheetID, netTools.sheetKey
	t.Cleanup(func() { netTools.sheet, netTools.sheetID, netTools.sheetKey = prev, prevID, prevKey })

	netToolsShowSheet("ssh")
	s := &netTools.sheet
	for _, want := range []string{"Status:   Running", "Password: " + sshSrv.Password(), "User:     " + sshSrv.User()} {
		if !strings.Contains(s.text, want) {
			t.Errorf("SFTP sheet is missing %q:\n%s", want, s.text)
		}
	}
	ids := make([]string, len(s.pills))
	for i, p := range s.pills {
		ids[i] = p.id
	}
	if got := strings.Join(ids, ","); got != "toggle,copyaddr,close" || s.pills[0].label != "Stop server" || !s.pills[0].danger {
		t.Fatalf("running server pills = %s (%q)", got, s.pills[0].label)
	}
}
