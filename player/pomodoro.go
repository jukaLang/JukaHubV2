package main

import (
	"fmt"
	"math"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Pomodoro timer with visual circular countdown
// ──────────────────────────────────────────────────────────────────────

const (
	pomodoroWorkMin            = 25
	pomodoroBreakMin           = 5
	pomodoroLongBreakMin       = 15
	pomodoroSessionsBeforeLong = 4
	pomodoroArcRadius          = 140
	pomodoroCenterX            = 640
	pomodoroCenterY            = 380
)

type pomodoroPhase int

const (
	pomWork pomodoroPhase = iota
	pomBreak
	pomLongBreak
	pomPaused
	pomIdle
)

type pomodoroState struct {
	phase       pomodoroPhase
	total       time.Duration
	remaining   time.Duration
	sessions    int
	lastTick    time.Time
	completed   int
	sessionName string
}

var pom pomodoroState

func pomodoroStart() {
	pom.phase = pomWork
	pom.total = pomodoroWorkMin * time.Minute
	pom.remaining = pom.total
	pom.lastTick = time.Now()
	pom.sessionName = fmt.Sprintf("Session %d", pom.completed+1)
}

func pomodoroTogglePause() {
	if pom.phase == pomPaused {
		pom.lastTick = time.Now()
		// Determine what to resume to.
		if pom.remaining > time.Duration(float64(pomodoroWorkMin)*float64(time.Minute)*0.5) {
			pom.phase = pomWork
		} else {
			pom.phase = pomBreak
		}
	} else if pom.phase == pomWork || pom.phase == pomBreak || pom.phase == pomLongBreak {
		pom.phase = pomPaused
	}
}

func pomodoroSkip() {
	switch pom.phase {
	case pomWork:
		pomodoroFinishWork()
	case pomBreak, pomLongBreak:
		pomodoroStart()
	case pomPaused:
		pom.phase = pomIdle
		pom.remaining = 0
	}
}

func pomodoroFinishWork() {
	pom.completed++
	pom.sessions++
	if pom.sessions >= pomodoroSessionsBeforeLong {
		pom.sessions = 0
		pom.phase = pomLongBreak
		pom.total = pomodoroLongBreakMin * time.Minute
	} else {
		pom.phase = pomBreak
		pom.total = pomodoroBreakMin * time.Minute
	}
	pom.remaining = pom.total
	pom.lastTick = time.Now()
	PlaySuccessSound()
}

func pomodoroFinishBreak() {
	pom.phase = pomWork
	pom.total = pomodoroWorkMin * time.Minute
	pom.remaining = pom.total
	pom.lastTick = time.Now()
	pom.sessionName = fmt.Sprintf("Session %d", pom.completed+1)
	PlayActivateSound()
}

func pomodoroReset() {
	pom.phase = pomIdle
	pom.remaining = 0
	pom.sessions = 0
	pom.completed = 0
}

// renderPomodoro draws the full-screen Pomodoro timer scene.
func renderPomodoro(renderer *sdl.Renderer, config *Config) {
	// Tick the timer.
	if pom.phase == pomWork || pom.phase == pomBreak || pom.phase == pomLongBreak {
		now := time.Now()
		elapsed := now.Sub(pom.lastTick)
		pom.lastTick = now
		pom.remaining -= elapsed
		if pom.remaining <= 0 {
			pom.remaining = 0
			if pom.phase == pomWork {
				pomodoroFinishWork()
			} else {
				pomodoroFinishBreak()
			}
		}
	}

	// Background.
	renderer.SetDrawColor(8, 10, 18, 255)
	renderer.Clear()

	cx := int32(pomodoroCenterX)
	cy := int32(pomodoroCenterY)
	r := int32(pomodoroArcRadius)

	// Outer ring (track).
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	for i := int32(0); i < 3; i++ {
		renderer.SetDrawColor(40, 50, 65, uint8(80-i*20))
		strokeCircle(renderer, cx, cy, r+i, sdl.Color{R: 40, G: 50, B: 65, A: uint8(80 - i*20)})
	}

	// Progress arc.
	var progress float64
	if pom.total > 0 {
		progress = float64(pom.remaining) / float64(pom.total)
	}
	if progress > 1 {
		progress = 1
	}

	// Color based on phase.
	arcColor := sdl.Color{R: 80, G: 160, B: 255, A: 255} // work: blue
	phaseText := "WORK"
	switch pom.phase {
	case pomBreak:
		arcColor = sdl.Color{R: 80, G: 220, B: 140, A: 255} // break: green
		phaseText = "BREAK"
	case pomLongBreak:
		arcColor = sdl.Color{R: 220, G: 160, B: 80, A: 255} // long break: gold
		phaseText = "LONG BREAK"
	case pomPaused:
		arcColor = sdl.Color{R: 200, G: 200, B: 200, A: 200}
		phaseText = "PAUSED"
	case pomIdle:
		arcColor = sdl.Color{R: 80, G: 100, B: 140, A: 150}
		phaseText = "READY"
	}

	// Draw progress arc as filled circle sectors.
	if progress > 0 && pom.phase != pomIdle {
		renderCircleSectorColor(renderer, cx, cy, r+1, r-6,
			-math.Pi/2, -math.Pi/2+2*math.Pi*progress, arcColor)
	}

	// Inner fill (darker, makes it a ring).
	innerFill := sdl.Color{R: 10, G: 14, B: 24, A: 255}
	if pom.phase == pomPaused {
		innerFill = sdl.Color{R: 14, G: 18, B: 28, A: 255}
	}
	fillCircle(renderer, cx, cy, r-7, innerFill)

	// Phase label above the circle.
	font, _ := getCachedFont(config, "medium")
	if font != nil {
		lw, _, _ := font.SizeUTF8(phaseText)
		lx := cx - int32(lw)/2
		ly := cy - r - 36
		renderText(renderer, config, font, phaseText,
			sdl.Color{R: arcColor.R, G: arcColor.G, B: arcColor.B, A: 200}, lx, ly)
	}

	// Large time display in center.
	minutes := int(pom.remaining.Minutes())
	seconds := int(pom.remaining.Seconds()) % 60
	timeStr := fmt.Sprintf("%02d:%02d", minutes, seconds)

	bigFont, _ := getCachedFont(config, "big")
	if bigFont == nil {
		bigFont = font
	}
	if bigFont != nil {
		tw, th, _ := bigFont.SizeUTF8(timeStr)
		tx := cx - int32(tw)/2
		ty := cy - int32(th)/2

		// Subtle glow behind time.
		if pom.phase == pomWork {
			renderer.SetDrawColor(arcColor.R, arcColor.G, arcColor.B, 30)
			renderer.FillRect(&sdl.Rect{X: tx - 10, Y: ty - 6, W: int32(tw) + 20, H: int32(th) + 12})
		}

		renderText(renderer, config, bigFont, timeStr,
			sdl.Color{R: 230, G: 240, B: 255, A: 255}, tx, ty)
	}

	// Session info below circle.
	smallFont, _ := getCachedFont(config, "small")
	if smallFont != nil {
		sessionStr := fmt.Sprintf("Completed: %d sessions", pom.completed)
		sw, _, _ := smallFont.SizeUTF8(sessionStr)
		sx := cx - int32(sw)/2
		sy := cy + r + 18
		renderText(renderer, config, smallFont, sessionStr,
			sdl.Color{R: 140, G: 155, B: 180, A: 200}, sx, sy)

		// Active session name.
		if pom.phase == pomWork || pom.phase == pomPaused {
			nw, _, _ := smallFont.SizeUTF8(pom.sessionName)
			nx := cx - int32(nw)/2
			ny := sy + 20
			renderText(renderer, config, smallFont, pom.sessionName,
				sdl.Color{R: 180, G: 190, B: 210, A: 180}, nx, ny)
		}
	}

	// Controls hint at bottom.
	hintFont, _ := getCachedFont(config, "small")
	if hintFont != nil {
		controls := "A: Start/Pause  |  B: Skip  |  Y: Reset"
		chw, _, _ := hintFont.SizeUTF8(controls)
		chx := (screenWidth - int32(chw)) / 2
		chy := screenHeight - 60
		renderText(renderer, config, hintFont, controls,
			sdl.Color{R: 100, G: 115, B: 140, A: 150}, chx, chy)
	}

	// Progress dots (4 dots for 4 sessions before long break).
	dotR := int32(8)
	dotGap := int32(28)
	startDotX := cx - int32(pomodoroSessionsBeforeLong-1)*dotGap/2
	dotY := cy + r + 52
	for i := 0; i < pomodoroSessionsBeforeLong; i++ {
		dx := startDotX + int32(i)*dotGap
		col := sdl.Color{R: 50, G: 60, B: 80, A: 150}
		if i < pom.sessions {
			col = arcColor
			col.A = 220
		}
		fillCircle(renderer, dx, dotY, dotR, col)
		if i == pom.sessions && (pom.phase == pomWork || pom.phase == pomPaused) {
			strokeCircle(renderer, dx, dotY, dotR+2, sdl.Color{
				R: arcColor.R, G: arcColor.G, B: arcColor.B, A: 100,
			})
		}
	}
}

// handlePomodoroInput processes controller/keyboard input in the Pomodoro scene.
func handlePomodoroInput(e *sdl.KeyboardEvent, config *Config) {
	if e == nil || e.Type != sdl.KEYDOWN {
		return
	}
	switch e.Keysym.Sym {
	case sdl.K_RETURN, sdl.K_SPACE, sdl.K_a:
		if pom.phase == pomIdle {
			pomodoroStart()
		} else {
			pomodoroTogglePause()
		}
	case sdl.K_b, sdl.K_ESCAPE:
		pomodoroSkip()
	case sdl.K_y:
		pomodoroReset()
	}
}
