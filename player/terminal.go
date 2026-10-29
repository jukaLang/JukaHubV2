package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Terminal emulator
// ──────────────────────────────────────────────────────────────────────

const (
	termMaxLines    = 200
	termPrompt      = "juka> "
	termCursorBlink = 500 // ms
	termLineH       = int32(20)
)

type termLine struct {
	text  string
	isErr bool
}

type terminalState struct {
	lines   []termLine
	input   string
	cursor  int
	scrollY int
	started time.Time
	history []string
	histIdx int // -1 = editing a fresh line, otherwise history index
}

var term terminalState

// Mouse hitboxes for the terminal chrome, in logical screen coordinates. They
// are filled in every frame by renderTerminal so clicks always match the
// current layout.
var (
	termBackRect     sdl.Rect
	termKeyboardRect sdl.Rect
	termInputRect    sdl.Rect
)

func termInit() {
	term.input = ""
	term.cursor = 0
	term.scrollY = 0
	term.histIdx = -1
	// Keep the session across visits: leaving the terminal and coming back
	// should not wipe what the user was reading.
	// Commands survive a restart: the history file is small, read once, and
	// shared with Tab completion.
	if len(term.history) == 0 {
		term.history = termLoadHistory()
	}
	if len(term.lines) == 0 {
		term.started = time.Now()
		term.lines = []termLine{
			{text: "JukaHub Terminal v1.0", isErr: false},
			{text: fmt.Sprintf("Platform: %s/%s | %s", runtime.GOOS, runtime.GOARCH, P().Name()), isErr: false},
			{text: "Type 'help' for available commands.", isErr: false},
			{text: "", isErr: false},
		}
	}
}

func termPushLine(text string, isErr bool) {
	term.lines = append(term.lines, termLine{text: text, isErr: isErr})
	if len(term.lines) > termMaxLines {
		term.lines = term.lines[len(term.lines)-termMaxLines:]
	}
	// Auto-scroll to bottom.
	term.scrollY = 0
}

// termScroll moves the output viewport by delta lines (positive = older
// output), clamped to the available history.
func termScroll(delta int) {
	maxScroll := len(term.lines) - 12
	if maxScroll < 0 {
		maxScroll = 0
	}
	term.scrollY += delta
	if term.scrollY < 0 {
		term.scrollY = 0
	}
	if term.scrollY > maxScroll {
		term.scrollY = maxScroll
	}
}

func termHistoryStep(delta int) {
	if len(term.history) == 0 {
		return
	}
	if term.histIdx == -1 {
		term.histIdx = len(term.history)
	}
	term.histIdx += delta
	if term.histIdx < 0 {
		term.histIdx = 0
	}
	if term.histIdx >= len(term.history) {
		term.histIdx = -1
		term.input = ""
		term.cursor = 0
		return
	}
	term.input = term.history[term.histIdx]
	term.cursor = len(term.input)
}

func termExecute(input string) {
	termRecordHistory(input)
	term.histIdx = -1
	termPushLine(termPrompt+input, false)
	cmd := strings.TrimSpace(input)
	if cmd == "" {
		return
	}

	parts := strings.Fields(cmd)
	command := parts[0]
	args := parts[1:]

	switch command {
	case "help":
		termPushLine("Available commands:", false)
		termPushLine("  help        Show this help", false)
		termPushLine("  ls/dir      List files in current directory", false)
		termPushLine("  pwd         Print working directory", false)
		termPushLine("  cd <dir>    Change directory", false)
		termPushLine("  cat <file>  Print file contents", false)
		termPushLine("  echo <text> Print text", false)
		termPushLine("  date        Show current date/time", false)
		termPushLine("  uptime      Show system uptime", false)
		termPushLine("  whoami      Show current user", false)
		termPushLine("  hostname    Show hostname", false)
		termPushLine("  history     Show recent commands (saved across restarts)", false)
		termPushLine("  clear       Clear the terminal (Ctrl+L)", false)
		termPushLine("  sysinfo     Show system information", false)
		termPushLine("  neofetch    System info with ASCII art", false)
		termPushLine("  calc <expr> Evaluate a math expression", false)
		termPushLine("  ssh [on|off|status]  SFTP server for PC file access", false)
		termPushLine("  exit        Return to home", false)

	case "ls", "dir":
		entries, err := os.ReadDir(".")
		if err != nil {
			termPushLine(fmt.Sprintf("Error: %v", err), true)
			return
		}
		for _, e := range entries {
			prefix := "  "
			if e.IsDir() {
				prefix = "[+] "
			} else {
				prefix = "[d] "
			}
			termPushLine(prefix+e.Name(), false)
		}

	case "pwd":
		dir, _ := os.Getwd()
		termPushLine(dir, false)

	case "cd":
		if len(args) == 0 {
			home, _ := os.UserHomeDir()
			if err := os.Chdir(home); err != nil {
				termPushLine(fmt.Sprintf("Error: %v", err), true)
			}
			return
		}
		dir, pathErr := cleanUserPath(args[0])
		if pathErr != nil {
			termPushLine("Error: "+pathErr.Error(), true)
			return
		}
		if err := os.Chdir(dir); err != nil {
			termPushLine(fmt.Sprintf("Error: %v", err), true)
		}

	case "cat":
		if len(args) == 0 {
			termPushLine("Usage: cat <filename>", true)
			return
		}
		// The terminal is an intentional user shell: `cat` reads the path the
		// user typed. Reject malformed paths (empty/NUL) before the OS call.
		file, pathErr := cleanUserPath(args[0])
		if pathErr != nil {
			termPushLine("Error: "+pathErr.Error(), true)
			return
		}
		data, err := os.ReadFile(file)
		if err != nil {
			termPushLine(fmt.Sprintf("Error: %v", err), true)
			return
		}
		for _, line := range strings.Split(string(data), "\n") {
			termPushLine(line, false)
		}

	case "echo":
		termPushLine(strings.Join(args, " "), false)

	case "date":
		termPushLine(time.Now().Format("Mon Jan 2 15:04:05 MST 2006"), false)

	case "uptime":
		uptime := time.Since(term.started)
		h := int(uptime.Hours())
		m := int(uptime.Minutes()) % 60
		s := int(uptime.Seconds()) % 60
		termPushLine(fmt.Sprintf("up %dh %dm %ds", h, m, s), false)

	case "whoami":
		if runtime.GOOS == "windows" {
			termPushLine(os.Getenv("USERNAME"), false)
		} else {
			termPushLine(os.Getenv("USER"), false)
		}

	case "hostname":
		host, _ := os.Hostname()
		termPushLine(host, false)

	case "history":
		if len(term.history) == 0 {
			termPushLine("No commands yet.", false)
			return
		}
		for i, h := range term.history {
			termPushLine(fmt.Sprintf("%4d  %s", i+1, h), false)
		}

	case "clear", "cls":
		term.lines = term.lines[:0]
		term.scrollY = 0

	case "sysinfo":
		termPushLine("=== System Info ===", false)
		termPushLine(fmt.Sprintf("OS:       %s", runtime.GOOS), false)
		termPushLine(fmt.Sprintf("Arch:     %s", runtime.GOARCH), false)
		termPushLine(fmt.Sprintf("CPUs:     %d", runtime.NumCPU()), false)
		termPushLine(fmt.Sprintf("Platform: %s", P().Name()), false)
		if runtime.GOOS == "linux" {
			if data, err := os.ReadFile("/proc/meminfo"); err == nil {
				for _, line := range strings.Split(string(data), "\n") {
					if strings.HasPrefix(line, "MemTotal:") || strings.HasPrefix(line, "MemAvailable:") {
						termPushLine("  "+line, false)
					}
				}
			}
		}

	case "neofetch":
		termPushLine("       .--.        JukaHub Terminal", false)
		termPushLine("      |o_o |       -----------------", false)
		termPushLine("      |:_/ |       OS:       "+runtime.GOOS, false)
		termPushLine("     //   \\ \\      Arch:     "+runtime.GOARCH, false)
		termPushLine("    (|     | )     Platform: "+P().Name(), false)
		termPushLine("   /'\\_   _/`\\    CPUs:     "+fmt.Sprintf("%d", runtime.NumCPU()), false)
		termPushLine("   \\___)=(___/    Uptime:   just started", false)

	case "calc":
		if len(args) == 0 {
			termPushLine("Usage: calc <expression> (e.g. calc 2+2*3)", true)
			return
		}
		expr := strings.Join(args, "")
		val, err := calcEvalExpr(expr)
		if err != nil {
			termPushLine(fmt.Sprintf("Error: %v", err), true)
		} else {
			termPushLine(fmt.Sprintf("= %g", val), false)
		}

	case "ssh":
		termSSHCommand(args)

	case "exit", "quit":
		goBackScene(appConfig)

	default:
		// Try running as external command.
		binPath, err := exec.LookPath(command)
		if err != nil {
			termPushLine(fmt.Sprintf("Command not found: %s", command), true)
			return
		}
		termPushLine(fmt.Sprintf("$ %s %s", command, strings.Join(args, " ")), false)
		cmdExec := exec.Command(binPath, args...)
		cmdExec.Dir, _ = os.Getwd()
		out, err := cmdExec.CombinedOutput()
		if len(out) > 0 {
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				termPushLine(line, false)
			}
		}
		if err != nil {
			termPushLine(fmt.Sprintf("Error: %v", err), true)
		}
	}
}

// termSSHCommand reports or toggles the SSH/SFTP server. It is the console-side
// equivalent of the Tools row: the random password is printed here because the
// terminal is the device's own screen and there is no other way to read it
// without leaving the terminal.
func termSSHCommand(args []string) {
	action := "status"
	if len(args) > 0 {
		action = strings.ToLower(args[0])
	}
	switch action {
	case "on", "start":
		if sshSrv.Running() {
			termPushLine("SSH/SFTP is already running.", false)
			break
		}
		if toolsIPAddress() == "" {
			termPushLine("No WiFi address — connect to a network before starting SFTP.", true)
			return
		}
		if err := sshSrv.Start(); err != nil {
			termPushLine("Error: "+err.Error(), true)
			return
		}
	case "off", "stop":
		if !sshSrv.Running() {
			termPushLine("SSH/SFTP is not running.", false)
			break
		}
		sshSrv.Stop()
		termPushLine("SSH/SFTP server stopped.", false)
		return
	case "status":
	case "help", "-h", "--help":
		termPushLine("Usage: ssh [on|off|status]", false)
		return
	default:
		termPushLine("Usage: ssh [on|off|status]", true)
		return
	}

	if !sshSrv.Running() {
		termPushLine("SSH/SFTP server: stopped", false)
		termPushLine(fmt.Sprintf("  Root: %s   Port: %d", sshRootDir(), sshPort()), false)
		return
	}
	termPushLine("SSH/SFTP server: running", false)
	addr := sshSrv.Address()
	if addr == "" {
		addr = "no WiFi address"
	}
	termPushLine("  Address:  "+addr, false)
	termPushLine("  User:     "+sshSrv.User(), false)
	termPushLine("  Password: "+sshSrv.Password(), false)
	termPushLine("  Root:     "+sshSrv.Root(), false)
	termPushLine("  Host key: "+sshSrv.Fingerprint(), false)
	if sshSrv.ReadOnly() {
		termPushLine("  Mode:     read-only", false)
	}
	termPushLine("  Accepts the sftp subsystem only (no shell or tunnels).", false)
}

// handleTermInput processes physical keyboard input for the terminal.
func handleTermInput(e *sdl.KeyboardEvent, config *Config) {
	if e == nil || e.Type != sdl.KEYDOWN {
		return
	}
	switch e.Keysym.Sym {
	case sdl.K_RETURN, sdl.K_KP_ENTER:
		input := term.input
		term.input = ""
		term.cursor = 0
		termExecute(input)
	case sdl.K_BACKSPACE:
		if term.cursor > 0 {
			term.input = term.input[:term.cursor-1] + term.input[term.cursor:]
			term.cursor--
		}
	case sdl.K_DELETE:
		if term.cursor < len(term.input) {
			term.input = term.input[:term.cursor] + term.input[term.cursor+1:]
		}
	case sdl.K_LEFT:
		if term.cursor > 0 {
			term.cursor--
		}
	case sdl.K_RIGHT:
		if term.cursor < len(term.input) {
			term.cursor++
		}
	case sdl.K_HOME:
		term.cursor = 0
	case sdl.K_END:
		term.cursor = len(term.input)
	case sdl.K_UP:
		termHistoryStep(-1)
	case sdl.K_DOWN:
		termHistoryStep(1)
	case sdl.K_PAGEUP:
		termScroll(10)
	case sdl.K_PAGEDOWN:
		termScroll(-10)
	case sdl.K_TAB:
		// Tab completes what is typed. On an empty line it still opens the
		// on-screen keyboard: the handheld has no hardware keyboard, so that
		// remains the only way to start typing.
		termComplete()
	case sdl.K_l:
		if e.Keysym.Mod&sdl.KMOD_CTRL != 0 {
			term.lines = term.lines[:0]
			term.scrollY = 0
		}
	case sdl.K_ESCAPE:
		goBackScene(config)
	}
}

// handleTermTextInput processes text input for the terminal.
func handleTermTextInput(e *sdl.TextInputEvent) {
	termInsertText(e.GetText())
}

// termInsertText inserts text at the prompt cursor. It is shared by typed
// input (SDL text events) and clipboard paste (Ctrl+V), so pasted text takes
// exactly the path typed text does — the same printable-ASCII filter, the
// same cursor arithmetic, nothing appended at the end when the cursor sits
// mid-command.
func termInsertText(text string) {
	for _, r := range text {
		if r >= 32 && r <= 126 {
			term.input = term.input[:term.cursor] + string(r) + term.input[term.cursor:]
			term.cursor++
		}
	}
}

// handleTermMouseClick routes clicks on the terminal chrome. Selecting the
// keyboard pill or the prompt row opens the on-screen keyboard; Back leaves the
// scene. Every click on the terminal is consumed so the config-driven elements
// (a leftover chat field in jukaconfig) cannot steal it.
func handleTermMouseClick(mx, my int32, config *Config) bool {
	switch {
	case pointInRect(mx, my, termKeyboardRect), pointInRect(mx, my, termInputRect):
		vkOpenTerminal()
	case pointInRect(mx, my, termBackRect):
		goBackScene(config)
	}
	return true
}

// handleTermWheel scrolls the output with the mouse wheel.
func handleTermWheel(dy int32) bool {
	if dy == 0 {
		return false
	}
	termScroll(int(dy) * 3)
	return true
}

// ── Rendering ────────────────────────────────────────────────────────────────

func renderTerminal(renderer *sdl.Renderer, config *Config) {
	renderer.SetDrawColor(5, 7, 12, 255)
	renderer.Clear()

	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	medium, _ := getCachedFont(config, "medium")
	if medium == nil {
		medium = font
	}

	barH := headerBarHeight()
	padX := int32(28)
	secondary := ColorTextSecondary()

	// ── Header ──────────────────────────────────────────────────────────
	// The app's one bar, exactly as Home and every scene draw it: wordmark,
	// "Terminal" in the caption, the shared Back and Menu pills, the update
	// offer and the system cluster. The terminal used to draw its own dark
	// strip with a "< BACK" text pill, which left it as the only screen whose
	// Back was not the app's Back.
	renderStatusBar(renderer, config)
	// The working directory is the terminal's own read-out, so it sits under
	// the bar as a quiet line rather than taking the title slot.
	cwd, _ := os.Getwd()
	cwdLineH := int32(0)
	if cwd != "" {
		cwdFont := headerCaptionFont(config)
		if cwdFont == nil {
			cwdFont = font
		}
		if cwdFont != nil {
			label := fitTextWidth(renderer, config, cwdFont, cwd, screenWidth-padX*2)
			renderText(renderer, config, cwdFont, label, secondary, padX, barH+6)
			cwdLineH = int32(cwdFont.Height()) + 6
		}
	}

	// The keyboard chip is the terminal's own chrome beside the app menu, so it
	// keeps the bar's pill height and type instead of inventing a third size.
	termKeyboardRect = sdl.Rect{}
	if chipFont := headerCaptionFont(config); chipFont != nil && tb.startRect.W > 0 {
		label := "Keyboard"
		chipW, _, _ := chipFont.SizeUTF8(label)
		chipH := headerPillHeight()
		pad := int32(12)
		keyX := tb.startRect.X + tb.startRect.W + SpaceSM
		keyY := (barH - chipH) / 2
		accent := headerAccent()
		termKeyboardRect = sdl.Rect{X: keyX, Y: keyY, W: int32(chipW) + pad*2, H: chipH}
		fillRoundedRect(renderer, keyX, keyY, termKeyboardRect.W, chipH, chipH/2, WithAlpha(accent, 40))
		strokeRoundedRect(renderer, keyX, keyY, termKeyboardRect.W, chipH, chipH/2, 1, WithAlpha(accent, 150))
		renderText(renderer, config, chipFont, label, accent,
			keyX+pad, keyY+(chipH-int32(chipFont.Height()))/2)
	}

	// The generic chrome-bar click and focus paths own the Back pill now; the
	// terminal only has to remember where it was drawn.
	termBackRect = sceneTitleBackRect

	// ── Output ──────────────────────────────────────────────────────────
	outputTop := barH + 14 + cwdLineH
	inputY := screenHeight - 84
	contentW := screenWidth - padX*2
	maxLines := int((inputY - 18 - outputTop) / termLineH)
	if maxLines < 3 {
		maxLines = 3
	}

	totalLines := len(term.lines)
	startLine := totalLines - maxLines - term.scrollY
	if startLine < 0 {
		startLine = 0
	}
	endLine := startLine + maxLines
	if endLine > totalLines {
		endLine = totalLines
	}

	y := outputTop
	for i := startLine; i < endLine; i++ {
		line := term.lines[i]
		col := sdl.Color{R: 168, G: 184, B: 208, A: 235}
		if line.isErr {
			col = sdl.Color{R: 235, G: 115, B: 120, A: 235}
		}
		if strings.HasPrefix(line.text, termPrompt) {
			col = sdl.Color{R: 120, G: 214, B: 160, A: 240}
		}
		displayLine := fitTextWidth(renderer, config, font, line.text, contentW-20)
		renderText(renderer, config, font, displayLine, col, padX, y)
		y += termLineH
	}

	// Scrollbar: a thin thumb on the right when the buffer overflows.
	if totalLines > maxLines {
		trackX := screenWidth - padX + 6
		trackTop := outputTop
		trackH := inputY - 18 - outputTop
		renderer.SetDrawColor(70, 90, 120, 70)
		renderer.FillRect(&sdl.Rect{X: trackX, Y: trackTop, W: 2, H: trackH})
		thumbH := int32(float64(trackH) * float64(maxLines) / float64(totalLines))
		if thumbH < 18 {
			thumbH = 18
		}
		frac := 0.0
		if totalLines > maxLines {
			frac = float64(term.scrollY) / float64(totalLines-maxLines)
		}
		thumbY := trackTop + int32(float64(trackH-thumbH)*(1-frac))
		renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 180)
		renderer.FillRect(&sdl.Rect{X: trackX, Y: thumbY, W: 2, H: thumbH})
	}

	// ── Prompt row ──────────────────────────────────────────────────────
	rowH := int32(44)
	rowX := padX - 12
	rowW := screenWidth - (padX-12)*2
	rowY := inputY - 9
	termInputRect = sdl.Rect{X: rowX, Y: rowY, W: rowW, H: rowH}
	fillRoundedRect(renderer, rowX, rowY, rowW, rowH, 10, sdl.Color{R: 13, G: 19, B: 31, A: 255})
	strokeRoundedRect(renderer, rowX, rowY, rowW, rowH, 10, 1, sdl.Color{R: 42, G: 58, B: 84, A: 200})
	// A faint accent edge on the left marks the active input line.
	fillRoundedRect(renderer, rowX, rowY, 3, rowH, 2, WithAlpha(accentColor, 200))

	textY := rowY + (rowH-int32(font.Height()))/2
	promptCol := sdl.Color{R: 120, G: 214, B: 160, A: 245}
	renderText(renderer, config, font, termPrompt, promptCol, rowX+16, textY)

	promptW, _, _ := font.SizeUTF8(termPrompt)
	inputX := rowX + 16 + int32(promptW)

	// Keep the caret in view when the command is longer than the row.
	const maxVisible = 82
	inputStart := 0
	if len(term.input) > maxVisible {
		inputStart = len(term.input) - maxVisible
	}
	visible := term.input[inputStart:]
	renderText(renderer, config, font, visible, sdl.Color{R: 225, G: 235, B: 250, A: 255}, inputX, textY)

	cursorRel := term.cursor - inputStart
	if cursorRel < 0 {
		cursorRel = 0
	}
	if cursorRel > len(visible) {
		cursorRel = len(visible)
	}
	cw, _, _ := font.SizeUTF8(visible[:cursorRel])
	cursorX := inputX + int32(cw)
	if int(time.Now().UnixMilli()/termCursorBlink)%2 == 0 {
		renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 220)
		renderer.FillRect(&sdl.Rect{X: cursorX, Y: textY - 2, W: 8, H: int32(font.Height()) + 4})
	}

	// ── Footer hints ────────────────────────────────────────────────────
	hintStr := hintLine(FooterHint{"Enter", "Run"}, FooterHint{"Tab", "Complete (empty: keyboard)"},
		FooterHint{"PgUp / PgDn", "Scroll"}, FooterHint{"Esc", "Back"})
	hw, _, _ := font.SizeUTF8(hintStr)
	renderText(renderer, config, font, hintStr,
		sdl.Color{R: 96, G: 116, B: 146, A: 190},
		(screenWidth-int32(hw))/2, screenHeight-26)
}

// Scene controller mapping, moved here from controller.go so the scene owns
// its own input.

func ctrlTerminal(e *sdl.ControllerButtonEvent, config *Config) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_A:
		if term.input != "" {
			input := term.input
			term.input = ""
			term.cursor = 0
			termExecute(input)
		}
	case sdl.CONTROLLER_BUTTON_Y, sdl.CONTROLLER_BUTTON_X:
		// Open the on-screen keyboard: without it a controller-only device
		// cannot type a command at all.
		vkOpenTerminal()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		// goBackScene plays the back sound itself.
		goBackScene(config)
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		termScroll(3)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		termScroll(-3)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		termScroll(10)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		termScroll(-10)
	default:
		return false
	}
	return true
}

// Scene registration. Terminal has no key branch of its own: typing is handled
// by the terminal widget before scene routing, so handleKey stays false.
type terminalScene struct{ sceneBase }

func (terminalScene) render(r *sdl.Renderer, c *Config) bool { renderTerminal(r, c); return true }

func (terminalScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return ctrlTerminal(e, c)
}

func init() { registerScene(terminalScene{sceneBase{name: "Terminal"}}) }
