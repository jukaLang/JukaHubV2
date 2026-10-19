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
		t.Skip("no usable font in this environment")
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
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer renderer.Destroy()
	// Cached text textures belong to the renderer that created them; each probe
	// makes a fresh renderer, so drop the previous probe's textures.
	ClearTextCache()

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderToolsPanel(renderer, config)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("renderer.ReadPixels unavailable: %v", err)
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

	if panel.W != 760 {
		t.Fatalf("panel width = %d, want 760 on a 1280px screen", panel.W)
	}
	if panel.X != (probeW-panel.W)/2 {
		t.Fatalf("panel is not horizontally centred: x=%d w=%d screen=%d", panel.X, panel.W, probeW)
	}
	if panel.Y < 0 || panel.Y+panel.H > probeH {
		t.Fatalf("panel vertically out of bounds: y=%d h=%d screen=%d", panel.Y, panel.H, probeH)
	}

	rows := toolsRows(config)
	if len(rects) != len(rows) {
		t.Fatalf("drew %d row rects for %d rows", len(rects), len(rows))
	}
	for i, r := range rects {
		if r.X < panel.X || r.X+r.W > panel.X+panel.W ||
			r.Y < panel.Y || r.Y+r.H > panel.Y+panel.H {
			t.Fatalf("row %d %+v escapes panel %+v", i, r, *panel)
		}
		if i > 0 {
			step := rects[1].Y - rects[0].Y
			if step < r.H {
				t.Fatalf("rows overlap: step %d < row height %d", step, r.H)
			}
			if r.Y != rects[i-1].Y+step {
				t.Fatalf("row %d starts at y=%d, want %d", i, r.Y, rects[i-1].Y+step)
			}
		}
	}

	// The selected row must be visibly highlighted against its neighbours.
	if lit, plain := bandLuma(f, rects[0]), bandLuma(f, rects[1]); lit <= plain {
		t.Fatalf("focused row is not highlighted: focused luma %d <= unfocused %d", lit, plain)
	}

	// The panel body is opaque over the dimmed scene.
	body := f.at(panel.X+panel.W/2, panel.Y+panel.H-4)
	if body.R == f.bg.R && body.G == f.bg.G && body.B == f.bg.B {
		t.Fatal("panel body is transparent")
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
	if rows[0].value != "Running" {
		t.Fatalf("FTP row value = %q, want Running", rows[0].value)
	}
	if rows[0].hint != "A: stop" {
		t.Fatalf("FTP row hint = %q, want A: stop", rows[0].hint)
	}
	if !strings.HasPrefix(rows[0].detail, "ftp://") {
		t.Fatalf("FTP row detail = %q, want the ftp:// address", rows[0].detail)
	}

	f, panel, rects := renderToolsProbe(t, &Config{}, 0)
	last := rects[len(rects)-1]
	warnTop := last.Y + last.H + 2
	warnBottom := panel.Y + panel.H - 24
	if warnBottom <= warnTop {
		t.Fatalf("no room for the warning: top=%d bottom=%d", warnTop, warnBottom)
	}
	if n := f.countColor(ColorToastWarn, panel.X+24, warnTop, panel.X+panel.W-24, warnBottom); n < 20 {
		t.Fatalf("warning text missing below the rows (only %d accent pixels)", n)
	}
}

func TestToolsRowsWithoutMiscScene(t *testing.T) {
	config := &Config{Scenes: []SceneConfig{{Name: "Home"}}}
	rows := toolsRows(config)
	if len(rows) != 6 {
		t.Fatalf("rows = %d, want FTP, SSH, phone companion, Watch Party, IP and Close only", len(rows))
	}
	if rows[0].action != toolsActionFTP || rows[1].action != toolsActionSSH ||
		rows[2].action != toolsActionCompanion || rows[3].action != toolsActionParty ||
		rows[4].action != toolsActionCopyIP || rows[5].action != toolsActionClose {
		t.Fatalf("unexpected row order: %+v", rows)
	}
	if rows[0].value != "Stopped" {
		t.Fatalf("FTP row value = %q, want Stopped", rows[0].value)
	}
	if rows[1].value != "Stopped" || rows[1].hint != "A: start" {
		t.Fatalf("SSH row = %+v, want stopped with a start hint", rows[1])
	}
	if rows[2].value != "Scan to connect" || rows[2].hint != "A: show QR code" {
		t.Fatalf("companion row = %+v, want the idle QR hint", rows[2])
	}
}

func TestToolsRowsWithMiscScene(t *testing.T) {
	config := &Config{Scenes: []SceneConfig{{Name: "Home"}, {Name: "Misc"}}}
	rows := toolsRows(config)
	if len(rows) != 7 {
		t.Fatalf("rows = %d, want a System utilities row too", len(rows))
	}
	found := false
	for i, r := range rows {
		if r.action == toolsActionUtilities {
			if i != 5 {
				t.Fatalf("utilities row at %d, want before Close", i)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no System utilities row for a config with a Misc scene")
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
	if rows[1].value != "Running" || rows[1].hint != "A: stop" {
		t.Fatalf("SSH row = %+v, want running with a stop hint", rows[1])
	}
	if !strings.HasPrefix(rows[1].detail, "sftp://") {
		t.Fatalf("SSH row detail = %q, want an sftp:// address", rows[1].detail)
	}

	info := toolsInfoLines()
	if len(info) == 0 || !strings.Contains(info[0].text, sshSrv.Password()) {
		t.Fatalf("credentials line missing the generated password: %+v", info)
	}
	if len(info) > 2 {
		t.Fatalf("info lines = %d, want at most 2 to fit the panel", len(info))
	}
}

// FTP and SFTP running together is the worst case for the panel's height:
// seven rows plus two info lines must still leave the footer its space.
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
	if len(rects) != 7 {
		t.Fatalf("row rects = %d, want 7 with the Misc scene", len(rects))
	}
	if font, _ := getCachedFont(config, "small"); font == nil {
		t.Skip("no usable font")
	}
	layout := toolsPanelMetrics(config, len(rects))
	content := layout.headerH + int32(len(rects))*layout.rowH + layout.infoH + layout.footerH
	if content > probeH-40 {
		t.Fatalf("layout needs %dpx on a %dpx screen", content, probeH)
	}
	if content > panel.H {
		t.Fatalf("layout needs %dpx but the panel is %dpx tall", content, panel.H)
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
	if IsOnline() {
		t.Skip("environment reports network access")
	}
	rows := toolsRows(&Config{})
	if rows[1].detail != "Offline — connect WiFi first" {
		t.Fatalf("offline IP detail = %q", rows[1].detail)
	}
}
