package main

// processes.go — the Hardware Monitor's live process list and process killer.
//
// The handheld ships without a task manager: the Hardware screen showed totals
// only, so a stuck emulator or helper tool could be seen in "top" nowhere. This
// panel lists the busiest processes and terminates one only after the user
// confirms twice; the config never needs to expose it (a "processes_open"
// trigger or the button injected at startup opens it).

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/process"
	"github.com/veandco/go-sdl2/sdl"
)

// procRow is one process as the panel and the Hardware text show it.
type procRow struct {
	PID  int32
	Name string
	CPU  float64 // percent of one core, as reported by gopsutil
	Mem  float64 // percent of total RAM
}

type processPanelState struct {
	open       bool
	cursor     int
	rows       []procRow
	loadedAt   time.Time
	err        string
	confirmPID int32
	confirmAt  time.Time
}

var processPanel processPanelState

// processRowRects maps a click back to a row; only touched on the render and
// event thread.
var processRowRects []sdl.Rect

var processPanelRect sdl.Rect

const (
	// processListLimit keeps the list readable on a 720p screen.
	processListLimit = 60
	// processPanelRefresh reloads the list while it is open.
	processPanelRefresh = 3 * time.Second
	// processKillConfirmWindow is how long the first A press stays armed.
	processKillConfirmWindow = 4 * time.Second
	// processHardwarePreview is how many processes the Hardware text lists.
	processHardwarePreview = 8
)

// processIsProtected reports processes the panel refuses to kill: init, this
// app and its parent. Terminating any of them takes the session down.
func processIsProtected(pid int32) bool {
	if pid <= 1 {
		return true
	}
	if pid == int32(os.Getpid()) {
		return true
	}
	if ppid := os.Getppid(); ppid > 1 && pid == int32(ppid) {
		return true
	}
	return false
}

// processKillFn performs the termination. Tests replace it so nothing is ever
// killed during a test run.
var processKillFn = func(pid int32) error {
	p, err := process.NewProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

// killProcess terminates pid after the safety guard.
func killProcess(pid int32) error {
	if processIsProtected(pid) {
		return fmt.Errorf("refusing to kill protected process %d", pid)
	}
	return processKillFn(pid)
}

// collectProcesses samples the running processes, busiest CPU first (memory
// breaks ties, which is what a fresh sample looks like on Linux). Callers
// refresh on a timer instead of every frame.
func collectProcesses(limit int) ([]procRow, error) {
	procs, err := process.Processes()
	if err != nil {
		return nil, err
	}
	rows := make([]procRow, 0, len(procs))
	for _, p := range procs {
		if p == nil {
			continue
		}
		name, err := p.Name()
		name = strings.TrimSpace(name)
		if err != nil || name == "" {
			name = fmt.Sprintf("pid %d", p.Pid)
		}
		cpu, _ := p.CPUPercent()
		mem, _ := p.MemoryPercent()
		rows = append(rows, procRow{PID: p.Pid, Name: name, CPU: cpu, Mem: float64(mem)})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CPU != rows[j].CPU {
			return rows[i].CPU > rows[j].CPU
		}
		if rows[i].Mem != rows[j].Mem {
			return rows[i].Mem > rows[j].Mem
		}
		return rows[i].PID < rows[j].PID
	})
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

// formatProcessList renders rows as the fixed-width block both the panel header
// and the Hardware text use.
func formatProcessList(rows []procRow) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%-7s %5s %5s  %s\n", "PID", "CPU%", "MEM%", "NAME"))
	sb.WriteString(strings.Repeat("-", 40) + "\n")
	for _, r := range rows {
		marker := ""
		if processIsProtected(r.PID) {
			marker = " *"
		}
		sb.WriteString(fmt.Sprintf("%-7d %5.1f %5.1f  %s%s\n", r.PID, r.CPU, r.Mem, r.Name, marker))
	}
	return sb.String()
}

// refreshProcessPanel reloads the rows and clamps the cursor.
func refreshProcessPanel() {
	rows, err := collectProcesses(processListLimit)
	if err != nil {
		processPanel.err = err.Error()
		return
	}
	processPanel.err = ""
	processPanel.rows = rows
	processPanel.loadedAt = time.Now()
	if processPanel.cursor >= len(rows) {
		processPanel.cursor = len(rows) - 1
	}
	if processPanel.cursor < 0 {
		processPanel.cursor = 0
	}
}

// openProcessPanel shows the panel and takes the first sample.
func openProcessPanel() {
	// Close the other modals so two lists never stack on each other.
	qs.open = false
	toolsPanel.open = false
	processPanel.open = true
	processPanel.cursor = 0
	processPanel.confirmPID = 0
	processPanelRect = sdl.Rect{}
	processRowRects = nil
	refreshProcessPanel()
	PlayActivateSound()
}

func closeProcessPanel() {
	processPanel.open = false
	processPanel.confirmPID = 0
	PlayBackSound()
}

func processPanelMove(delta int) {
	if len(processPanel.rows) == 0 {
		return
	}
	processPanel.cursor += delta
	if processPanel.cursor < 0 {
		processPanel.cursor = 0
	}
	if processPanel.cursor >= len(processPanel.rows) {
		processPanel.cursor = len(processPanel.rows) - 1
	}
	processPanel.confirmPID = 0
	PlayNavSound()
}

// processPanelActivate handles A on a row: the first press arms the kill and
// the second press within processKillConfirmWindow performs it.
func processPanelActivate() {
	if processPanel.cursor < 0 || processPanel.cursor >= len(processPanel.rows) {
		return
	}
	row := processPanel.rows[processPanel.cursor]
	if processIsProtected(row.PID) {
		ShowToast(fmt.Sprintf("%s (PID %d) is protected", row.Name, row.PID), ToastKindInfo)
		return
	}
	armed := processPanel.confirmPID == row.PID && time.Since(processPanel.confirmAt) <= processKillConfirmWindow
	if !armed {
		processPanel.confirmPID = row.PID
		processPanel.confirmAt = time.Now()
		PlayNavSound()
		ShowToast(fmt.Sprintf("Press A again to kill %s (PID %d)", row.Name, row.PID), ToastKindWarning)
		return
	}
	processPanel.confirmPID = 0
	if err := killProcess(row.PID); err != nil {
		ShowToast("Kill failed: "+firstLine(err.Error()), ToastKindError)
		return
	}
	PlaySuccessSound()
	ShowToast(fmt.Sprintf("Killed %s (PID %d)", row.Name, row.PID), ToastKindSuccess)
	refreshProcessPanel()
}

// ctrlProcessPanel is called before every other controller overlay.
func ctrlProcessPanel(e *sdl.ControllerButtonEvent, config *Config) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		processPanelMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		processPanelMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		processPanelActivate()
	case sdl.CONTROLLER_BUTTON_Y:
		refreshProcessPanel()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeProcessPanel()
	}
	return true
}

// handleProcessKey consumes every key while the panel is open so the scene
// underneath never reacts to typing behind the modal.
func handleProcessKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !processPanel.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		processPanelMove(-1)
	case sdl.K_DOWN:
		processPanelMove(1)
	case sdl.K_PAGEUP:
		processPanelMove(-10)
	case sdl.K_PAGEDOWN:
		processPanelMove(10)
	case sdl.K_RETURN, sdl.K_SPACE:
		processPanelActivate()
	case sdl.K_r:
		refreshProcessPanel()
		PlayNavSound()
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		closeProcessPanel()
	}
	return true
}

// handleProcessMouse selects and activates a clicked row, or closes the panel
// when the click lands outside it.
func handleProcessMouse(mx, my int32, config *Config) bool {
	if !processPanel.open {
		return false
	}
	for i, r := range processRowRects {
		if r.W > 0 && pointInRect(mx, my, r) {
			processPanel.cursor = i
			processPanelActivate()
			return true
		}
	}
	if processPanelRect.W > 0 && !pointInRect(mx, my, processPanelRect) {
		closeProcessPanel()
	}
	return true
}

// renderProcessPanel draws the modal list over the current scene.
func renderProcessPanel(renderer *sdl.Renderer, config *Config) {
	if !processPanel.open || renderer == nil {
		return
	}
	if processPanel.loadedAt.IsZero() || time.Since(processPanel.loadedAt) > processPanelRefresh {
		refreshProcessPanel()
	}

	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}
	smallH := int32(font.Height())
	const padX = int32(24)
	lineH := smallH + 8
	visible := 12
	if screenHeight < 700 {
		visible = 10
	}
	if len(processPanel.rows) < visible {
		visible = len(processPanel.rows)
	}
	if visible < 1 {
		visible = 1
	}

	// The title band is the shared page header, built before the card is sized so
	// the panel and the bar that fills it measure the same height.
	sub := fmt.Sprintf("%d running · busiest first", len(processPanel.rows))
	if processPanel.err != "" {
		sub = "Process list unavailable: " + firstLine(processPanel.err)
	}
	header := UIPanelHeader("Processes", sub, "apps")
	headerH := UIPanelHeaderBand(config, header, 10)
	tableHeadH := smallH + 8
	footerH := 20 + smallH
	panelW := int32(760)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	panelH := headerH + tableHeadH + int32(visible)*lineH + footerH
	if panelH > screenHeight-40 {
		panelH = screenHeight - 40
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	processPanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 180)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, ColorPanelRaised)
	strokeRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, 1, ColorBorderDefault)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 110)
	renderer.FillRect(&sdl.Rect{X: panelX + 18, Y: panelY + 1, W: panelW - 36, H: 2})

	header.X = panelX + padX
	header.Y = panelY + UIPanelHeaderInset
	header.W = panelW - 2*padX
	UIPageHeaderDraw(renderer, config, header)

	colPID := panelX + padX
	colCPU := colPID + 90
	colMem := colCPU + 80
	colName := colMem + 80
	headY := panelY + headerH
	renderText(renderer, config, font, "PID", ColorTextTertiary(), colPID, headY)
	renderText(renderer, config, font, "CPU%", ColorTextTertiary(), colCPU, headY)
	renderText(renderer, config, font, "MEM%", ColorTextTertiary(), colMem, headY)
	renderText(renderer, config, font, "NAME", ColorTextTertiary(), colName, headY)

	// Scroll the window so the cursor stays visible in a long list.
	first := 0
	if processPanel.cursor >= visible {
		first = processPanel.cursor - visible + 1
	}
	focusColor := installHighlight()
	rowW := panelW - 2*padX
	processRowRects = make([]sdl.Rect, 0, visible)
	rowY := headY + tableHeadH
	for i := 0; i < visible; i++ {
		idx := first + i
		if idx >= len(processPanel.rows) {
			break
		}
		r := processPanel.rows[idx]
		rect := sdl.Rect{X: panelX + padX, Y: rowY, W: rowW, H: lineH - 2}
		processRowRects = append(processRowRects, rect)
		focused := idx == processPanel.cursor
		if focused {
			fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 8, WithAlpha(focusColor, 46))
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 8, 1, focusColor)
		}
		textColor := ColorTextSecondary()
		if focused {
			textColor = ColorTextPrimary()
		}
		armed := focused && processPanel.confirmPID == r.PID
		if armed {
			textColor = ColorToastWarn
		}
		renderText(renderer, config, font, fmt.Sprintf("%d", r.PID), textColor, colPID, rowY+2)
		renderText(renderer, config, font, fmt.Sprintf("%.1f", r.CPU), textColor, colCPU, rowY+2)
		renderText(renderer, config, font, fmt.Sprintf("%.1f", r.Mem), textColor, colMem, rowY+2)
		name := r.Name
		if processIsProtected(r.PID) {
			name += "  (protected)"
		}
		maxW := panelX + panelW - padX - colName
		renderText(renderer, config, font, ellipsize(font, name, maxW), textColor, colName, rowY+2)
		rowY += lineH
	}

	footer := hintLine(FooterHint{"A", "Kill"}, FooterHint{"Y", "Refresh"}, FooterHint{"B", "Close"})
	if processPanel.confirmPID != 0 {
		footer = hintLine(FooterHint{"A", "Confirm Again"}, FooterHint{"B", "Cancel"})
	}
	fw, _, _ := font.SizeUTF8(footer)
	footerColor := ColorTextTertiary()
	if processPanel.confirmPID != 0 {
		footerColor = ColorToastWarn
	}
	renderText(renderer, config, font, footer, footerColor,
		panelX+(panelW-int32(fw))/2, panelY+panelH-footerH+8)
}

// ensureHardwareProcessEntry gives the Hardware scene a "Processes" button
// without touching the config on disk, mirroring ensureToolsMenuEntry. The edit
// is in-memory and idempotent.
func ensureHardwareProcessEntry(config *Config) {
	if config == nil {
		return
	}
	for i := range config.Scenes {
		scene := &config.Scenes[i]
		if scene.Name != "Hardware" {
			continue
		}
		for _, e := range scene.Elements {
			if e.Trigger == "processes_open" {
				return
			}
		}
		// Sit beside the existing action buttons (Refresh), on their row.
		x := int32(60)
		y := int32(config.Height - 109)
		for _, e := range scene.Elements {
			if e.Type != "button" {
				continue
			}
			if right := e.X + getElementWidth(e, 140) + 20; right > x {
				x = right
			}
			if e.Y > y-20 {
				y = e.Y
			}
		}
		scene.Elements = append(scene.Elements, Element{
			Type:    "button",
			Text:    "Processes",
			Color:   "$labelColor",
			BgColor: "$buttonColor",
			X:       x,
			Y:       y,
			Width:   "160",
			Height:  "45",
			Trigger: "processes_open",
		})
		return
	}
}
