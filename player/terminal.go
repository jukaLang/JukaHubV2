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
}

var term terminalState

func termInit() {
	term = terminalState{
		started: time.Now(),
		lines: []termLine{
			{text: "JukaHub Terminal v1.0", isErr: false},
			{text: fmt.Sprintf("Platform: %s/%s | %s", runtime.GOOS, runtime.GOARCH, P().Name()), isErr: false},
			{text: "Type 'help' for available commands.", isErr: false},
			{text: "", isErr: false},
		},
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

func termExecute(input string) {
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
		termPushLine("  cat <file>  Print file contents", false)
		termPushLine("  echo <text> Print text", false)
		termPushLine("  date        Show current date/time", false)
		termPushLine("  uptime      Show system uptime", false)
		termPushLine("  whoami      Show current user", false)
		termPushLine("  hostname    Show hostname", false)
		termPushLine("  clear       Clear the terminal", false)
		termPushLine("  sysinfo     Show system information", false)
		termPushLine("  neofetch    System info with ASCII art", false)
		termPushLine("  calc <expr> Evaluate a math expression", false)
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

	case "cat":
		if len(args) == 0 {
			termPushLine("Usage: cat <filename>", true)
			return
		}
		data, err := os.ReadFile(args[0])
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

	case "clear":
		term.lines = term.lines[:0]

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
		termPushLine("      |o_o |       ─────────────────", false)
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

	case "exit":
		goBackScene(appConfig)

	default:
		// Try running as external command.
		binPath, err := exec.LookPath(command)
		if err != nil {
			termPushLine(fmt.Sprintf("Command not found: %s", command), true)
			return
		}
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

// handleTermInput processes keyboard input for the terminal.
func handleTermInput(e *sdl.KeyboardEvent, config *Config) {
	if e == nil || e.Type != sdl.KEYDOWN {
		return
	}
	switch e.Keysym.Sym {
	case sdl.K_RETURN:
		termExecute(term.input)
		term.input = ""
		term.cursor = 0
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
	case sdl.K_PAGEUP:
		term.scrollY += 10
		maxScroll := len(term.lines) - 15
		if maxScroll < 0 {
			maxScroll = 0
		}
		if term.scrollY > maxScroll {
			term.scrollY = maxScroll
		}
	case sdl.K_PAGEDOWN:
		term.scrollY -= 10
		if term.scrollY < 0 {
			term.scrollY = 0
		}
	case sdl.K_ESCAPE, sdl.K_b:
		goBackScene(config)
	}
}

// handleTermTextInput processes text input for the terminal.
func handleTermTextInput(e *sdl.TextInputEvent) {
	for _, r := range string(e.Text[:]) {
		if r >= 32 && r <= 126 {
			term.input = term.input[:term.cursor] + string(r) + term.input[term.cursor:]
			term.cursor++
		}
	}
}

// renderTerminal draws the full-screen terminal.
func renderTerminal(renderer *sdl.Renderer, config *Config) {
	renderer.SetDrawColor(6, 8, 14, 255)
	renderer.Clear()

	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}

	lineH := int32(20)
	padX := int32(16)
	padY := int32(12)
	maxLines := int((screenHeight - 60) / lineH) // leave room for input line

	// Determine visible lines.
	totalLines := len(term.lines)
	startLine := totalLines - maxLines - term.scrollY
	if startLine < 0 {
		startLine = 0
	}
	endLine := startLine + maxLines
	if endLine > totalLines {
		endLine = totalLines
	}

	// Render output lines.
	y := padY
	for i := startLine; i < endLine; i++ {
		line := term.lines[i]
		col := sdl.Color{R: 160, G: 175, B: 200, A: 220}
		if line.isErr {
			col = sdl.Color{R: 220, G: 100, B: 100, A: 220}
		}
		if strings.HasPrefix(line.text, termPrompt) {
			col = sdl.Color{R: 100, G: 200, B: 140, A: 220}
		}
		// Clip long lines.
		displayLine := line.text
		if len(displayLine) > 100 {
			displayLine = displayLine[:100] + "…"
		}
		renderText(renderer, config, font, displayLine, col, padX, y)
		y += lineH
	}

	// Input line at bottom.
	inputY := screenHeight - 50

	// Separator line.
	renderer.SetDrawColor(40, 50, 65, 120)
	renderer.FillRect(&sdl.Rect{X: 0, Y: inputY - 4, W: screenWidth, H: 1})

	// Prompt.
	promptCol := sdl.Color{R: 100, G: 200, B: 140, A: 240}
	renderText(renderer, config, font, termPrompt, promptCol, padX, inputY)

	// Input text with cursor.
	promptW, _, _ := font.SizeUTF8(termPrompt)
	inputX := padX + int32(promptW)
	inputText := term.input
	if len(inputText) > 80 {
		inputText = "…" + inputText[len(inputText)-79:]
	}
	renderText(renderer, config, font, inputText,
		sdl.Color{R: 220, G: 230, B: 245, A: 255}, inputX, inputY)

	// Blinking cursor.
	cursorInInput := term.cursor
	if len(term.input) > 80 {
		cursorInInput = len(term.input) - (len(term.input) - 79)
	}
	cursorStr := term.input[:cursorInInput]
	cw, _, _ := font.SizeUTF8(cursorStr)
	cursorX := inputX + int32(cw)

	if int(time.Now().UnixMilli()/termCursorBlink)%2 == 0 {
		renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 200)
		renderer.FillRect(&sdl.Rect{X: cursorX, Y: inputY - 2, W: 2, H: int32(font.Height()) + 4})
	}

	// Scroll indicator.
	if term.scrollY > 0 {
		scrollStr := fmt.Sprintf("v %d lines", term.scrollY)
		sw, _, _ := font.SizeUTF8(scrollStr)
		renderText(renderer, config, font, scrollStr,
			sdl.Color{R: 80, G: 90, B: 110, A: 140},
			screenWidth-int32(sw)-16, inputY-20)
	}

	// Controls hint.
	hintStr := "Enter: Execute | PgUp/PgDn: Scroll | B/Esc: Back"
	hw, _, _ := font.SizeUTF8(hintStr)
	renderText(renderer, config, font, hintStr,
		sdl.Color{R: 70, G: 80, B: 100, A: 100},
		(screenWidth-int32(hw))/2, screenHeight-22)
}
