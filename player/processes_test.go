package main

// Tests for the Hardware Monitor's process list and killer: the safety guard,
// the two-press confirmation, and the in-memory entry button on the Hardware
// scene. Nothing is ever killed: processKillFn is replaced by a recorder.

import (
	"os"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// stubKill replaces the kill implementation and records the pids it is asked
// to terminate.
func stubKill(t *testing.T) *[]int32 {
	t.Helper()
	called := &[]int32{}
	prev := processKillFn
	processKillFn = func(pid int32) error {
		*called = append(*called, pid)
		return nil
	}
	t.Cleanup(func() { processKillFn = prev })
	return called
}

func TestKillProcessRefusesProtectedPIDs(t *testing.T) {
	called := stubKill(t)

	for _, pid := range []int32{0, 1, -5, int32(os.Getpid())} {
		if err := killProcess(pid); err == nil {
			t.Errorf("killProcess(%d) succeeded, want a refusal", pid)
		}
	}
	if ppid := os.Getppid(); ppid > 1 {
		if err := killProcess(int32(ppid)); err == nil {
			t.Errorf("killProcess(parent %d) succeeded, want a refusal", ppid)
		}
	}
	if len(*called) != 0 {
		t.Fatalf("refused pids reached the killer: %v", *called)
	}

	if err := killProcess(424242); err != nil {
		t.Fatalf("killProcess(424242) = %v, want nil", err)
	}
	if len(*called) != 1 || (*called)[0] != 424242 {
		t.Fatalf("killer called with %v, want [424242]", *called)
	}
}

func TestCollectProcessesSortedAndLimited(t *testing.T) {
	rows, err := collectProcesses(5)
	if err != nil {
		t.Skipf("process listing unavailable here: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("no processes listed")
	}
	if len(rows) > 5 {
		t.Fatalf("listed %d processes, want at most 5", len(rows))
	}
	for i := 1; i < len(rows); i++ {
		prev, cur := rows[i-1], rows[i]
		if cur.CPU > prev.CPU+0.001 {
			t.Fatalf("rows not CPU-sorted: %v before %v", prev, cur)
		}
		if prev.CPU == cur.CPU && cur.Mem > prev.Mem+0.001 {
			t.Fatalf("memory tie-break not applied: %v before %v", prev, cur)
		}
	}
	for _, r := range rows {
		if r.Name == "" {
			t.Errorf("process %d has no name", r.PID)
		}
	}
}

func TestFormatProcessListMarksProtectedProcesses(t *testing.T) {
	out := formatProcessList([]procRow{
		{PID: 1, Name: "init", CPU: 0.1, Mem: 0.2},
		{PID: 4242, Name: "ffplay", CPU: 12.5, Mem: 3.4},
	})
	for _, want := range []string{"PID", "CPU%", "MEM%", "NAME", "init", "ffplay"} {
		if !strings.Contains(out, want) {
			t.Errorf("process table lacks %q:\n%s", want, out)
		}
	}
	// Init can never be killed, so its row must be marked.
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "1 ") && !strings.Contains(line, "*") {
			t.Errorf("protected process row is not marked: %q", line)
		}
	}
}

// TestProcessPanelKillNeedsConfirmation pins the two-press contract: one A
// press only arms the kill, and the row is not protected by accident.
func TestProcessPanelKillNeedsConfirmation(t *testing.T) {
	called := stubKill(t)
	prev := processPanel
	t.Cleanup(func() { processPanel = prev })

	processPanel = processPanelState{open: true, rows: []procRow{{PID: 4242, Name: "ffplay"}}}

	processPanelActivate()
	if len(*called) != 0 {
		t.Fatalf("first press killed immediately: %v", *called)
	}
	if processPanel.confirmPID != 4242 {
		t.Fatalf("first press did not arm the kill (confirmPID=%d)", processPanel.confirmPID)
	}

	processPanelActivate()
	if len(*called) != 1 || (*called)[0] != 4242 {
		t.Fatalf("second press called killer with %v, want [4242]", *called)
	}
	if processPanel.confirmPID != 0 {
		t.Fatal("confirmation survived the kill")
	}
}

// TestProcessPanelConfirmationExpires keeps a stale arm from killing a row the
// user merely scrolled past.
func TestProcessPanelConfirmationExpires(t *testing.T) {
	called := stubKill(t)
	prev := processPanel
	t.Cleanup(func() { processPanel = prev })

	processPanel = processPanelState{
		open:       true,
		rows:       []procRow{{PID: 4242, Name: "ffplay"}},
		confirmPID: 4242,
		confirmAt:  time.Now().Add(-2 * processKillConfirmWindow),
	}
	processPanelActivate()
	if len(*called) != 0 {
		t.Fatalf("stale confirmation killed the process: %v", *called)
	}
	if processPanel.confirmPID != 4242 {
		t.Fatal("expired confirmation was not re-armed for the next press")
	}
}

// TestProcessPanelRefusesProtectedRows makes sure pid 1 cannot be armed or
// killed even when the list shows it.
func TestProcessPanelRefusesProtectedRows(t *testing.T) {
	called := stubKill(t)
	prev := processPanel
	t.Cleanup(func() { processPanel = prev })

	processPanel = processPanelState{open: true, rows: []procRow{{PID: 1, Name: "init"}}}
	processPanelActivate()
	processPanelActivate()
	if len(*called) != 0 {
		t.Fatalf("protected row reached the killer: %v", *called)
	}
	if processPanel.confirmPID != 0 {
		t.Fatal("protected row was armed")
	}
}

// TestEnsureHardwareProcessEntry adds the panel's entry button in memory and
// never duplicates it.
func TestEnsureHardwareProcessEntry(t *testing.T) {
	cfg := &Config{
		Width:  1280,
		Height: 720,
		Scenes: []SceneConfig{{
			Name: "Hardware",
			Elements: []Element{
				{Type: "textlog", Variable: "hw_text", X: 24, Y: 72, Width: "1232", Height: "539"},
				{Type: "button", Text: "Refresh", X: 60, Y: 611, Width: "140", Height: "45", Trigger: "hw_load"},
			},
		}},
	}
	ensureHardwareProcessEntry(cfg)
	ensureHardwareProcessEntry(cfg)

	scene := cfg.Scenes[0]
	var found *Element
	count := 0
	for i := range scene.Elements {
		if scene.Elements[i].Trigger == "processes_open" {
			found = &scene.Elements[i]
			count++
		}
	}
	if count != 1 {
		t.Fatalf("Processes button injected %d times, want 1", count)
	}
	if found.X <= 60 {
		t.Errorf("Processes button at x=%d overlaps the Refresh button", found.X)
	}
	if found.Y != 611 {
		t.Errorf("Processes button y=%d, want the Refresh row (611)", found.Y)
	}
	if bottom := found.Y + getElementHeight(*found, 45); bottom > int32(cfg.Height)-20 {
		t.Errorf("Processes button bottom %d leaves the screen (%d)", bottom, cfg.Height)
	}
}

// TestProcessPanelRenders is a layout smoke test: the modal must fit the screen
// and draw one selectable rect per visible row.
func TestProcessPanelRenders(t *testing.T) {
	_ = newProbeFrame(t) // SDL video + fonts on the dummy driver
	r, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(r)
	withScreen(t, probeW, probeH)

	prev := processPanel
	t.Cleanup(func() {
		processPanel = prev
		ClearTextCache()
	})
	processPanel = processPanelState{
		open:     true,
		loadedAt: time.Now(),
		rows: []procRow{
			{PID: 1, Name: "init"},
			{PID: 4242, Name: "ffplay", CPU: 12.5, Mem: 3.4},
			{PID: 777, Name: "JukaHub", CPU: 1.0, Mem: 9.9},
		},
	}
	r.SetDrawColor(9, 14, 25, 255)
	r.Clear()
	renderProcessPanel(r, &Config{})

	if processPanelRect.W <= 0 || processPanelRect.H <= 0 {
		t.Fatal("process panel was not laid out")
	}
	if processPanelRect.X < 0 || processPanelRect.Y < 0 ||
		processPanelRect.X+processPanelRect.W > probeW || processPanelRect.Y+processPanelRect.H > probeH {
		t.Fatalf("process panel %+v leaves the %dx%d screen", processPanelRect, probeW, probeH)
	}
	if len(processRowRects) != len(processPanel.rows) {
		t.Fatalf("drew %d row rects for %d rows", len(processRowRects), len(processPanel.rows))
	}

	// The first row must actually carry text (PID and name).
	buf := make([]byte, probeW*probeH*4)
	if err := r.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
	}
	row := processRowRects[0]
	colors := []sdl.Color{ColorTextPrimary(), ColorTextSecondary()}
	ink := 0
	for y := row.Y; y < row.Y+row.H; y++ {
		for x := row.X; x < row.X+row.W; x++ {
			i := (y*probeW + x) * 4
			for _, s := range colors {
				if buf[i] == s.B && buf[i+1] == s.G && buf[i+2] == s.R {
					ink++
					break
				}
			}
		}
	}
	if ink == 0 {
		t.Fatal("process row painted no text")
	}
}

// TestShippedConfigHardwareGetsProcessEntry checks the real config: loading it
// must leave exactly one Processes button on the Hardware scene.
func TestShippedConfigHardwareGetsProcessEntry(t *testing.T) {
	cfg, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	idx := sceneIndexExact(cfg, "Hardware")
	if idx < 0 {
		t.Fatalf("the shipped config has a Hardware scene; it went missing")
	}
	count := 0
	for _, e := range cfg.Scenes[idx].Elements {
		if e.Trigger == "processes_open" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("Hardware scene has %d Processes buttons, want 1", count)
	}
}

// TestHardwareTextListsProcesses keeps the Hardware screen itself informative:
// the text the scene shows must include the active process table.
func TestHardwareTextListsProcesses(t *testing.T) {
	out := getHardwareInfo()
	if !strings.Contains(out, "=== Active Processes ===") {
		t.Fatalf("hardware text has no process section:\n%s", out)
	}
	if strings.Contains(out, "=== Active Processes ===\n\n") {
		t.Errorf("process section is empty")
	}
}
