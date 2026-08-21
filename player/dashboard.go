package main

import (
	"fmt"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Live System Dashboard — animated gauges
// ──────────────────────────────────────────────────────────────────────

type gaugeData struct {
	Value   float64 // 0..100
	Max     float64
	Label   string
	Unit    string
	Current string // formatted display string
	Color   sdl.Color
	AnimVal float64 // smoothly animated value
}

type dashboardState struct {
	gauges    [5]gaugeData // CPU, RAM, Temp, Disk, Uptime
	inited    bool
	lastPoll  time.Time
	startTime time.Time
	cpuPrev   uint64
	cpuIdle   uint64
}

var dash dashboardState

func dashInit() {
	dash.startTime = time.Now()
	dash.gauges = [5]gaugeData{
		{Label: "CPU", Unit: "%", Color: sdl.Color{R: 80, G: 160, B: 255, A: 255}},
		{Label: "RAM", Unit: "%", Color: sdl.Color{R: 100, G: 220, B: 140, A: 255}},
		{Label: "Temp", Unit: "°C", Max: 80, Color: sdl.Color{R: 240, G: 180, B: 60, A: 255}},
		{Label: "Disk", Unit: "%", Color: sdl.Color{R: 200, G: 100, B: 255, A: 255}},
		{Label: "Net", Unit: "", Color: sdl.Color{R: 60, G: 200, B: 220, A: 255}},
	}
	dash.lastPoll = time.Now()
	dash.inited = true
	dashPoll()
}

func dashPoll() {
	now := time.Now()
	if now.Sub(dash.lastPoll) < 2*time.Second {
		return
	}
	dash.lastPoll = now

	// CPU usage from /proc/stat (Linux only).
	if runtime.GOOS == "linux" {
		if data, err := os.ReadFile("/proc/stat"); err == nil {
			lines := strings.SplitN(string(data), "\n", 2)
			if len(lines) > 0 {
				fields := strings.Fields(lines[0])
				if len(fields) >= 5 {
					user, _ := strconv.ParseUint(fields[1], 10, 64)
					nice, _ := strconv.ParseUint(fields[2], 10, 64)
					system, _ := strconv.ParseUint(fields[3], 10, 64)
					idle, _ := strconv.ParseUint(fields[4], 10, 64)
					total := user + nice + system + idle
					dTotal := total - dash.cpuPrev
					dIdle := idle - dash.cpuIdle
					dash.cpuPrev = total
					dash.cpuIdle = idle
					if dTotal > 0 {
						pct := float64(dTotal-dIdle) / float64(dTotal) * 100
						dash.gauges[0].Value = pct
						dash.gauges[0].Current = fmt.Sprintf("%.0f%%", pct)
					}
				}
			}
		}
	} else {
		// On Windows, approximate with a random-ish value based on goroutines.
		dash.gauges[0].Value = float64(runtime.NumGoroutine()) / 50.0 * 100
		if dash.gauges[0].Value > 100 {
			dash.gauges[0].Value = 100
		}
		dash.gauges[0].Current = fmt.Sprintf("%.0f%%", dash.gauges[0].Value)
	}

	// RAM usage.
	if runtime.GOOS == "linux" {
		var total, avail int64
		if data, err := os.ReadFile("/proc/meminfo"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "MemTotal:") {
					total = parseMemKB(line)
				} else if strings.HasPrefix(line, "MemAvailable:") {
					avail = parseMemKB(line)
				}
			}
		}
		if total > 0 {
			used := total - avail
			pct := float64(used) / float64(total) * 100
			dash.gauges[1].Value = pct
			usedMB := used / 1024 / 1024
			totalMB := total / 1024 / 1024
			dash.gauges[1].Current = fmt.Sprintf("%d/%d MB", usedMB, totalMB)
		}
	}

	// Temperature.
	if runtime.GOOS == "linux" {
		candidates := []string{
			"/sys/class/thermal/thermal_zone0/temp",
			"/sys/class/thermal/thermal_zone1/temp",
		}
		for _, path := range candidates {
			if data, err := os.ReadFile(path); err == nil {
				if v, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && v > 0 {
					celsius := float64(v) / 1000.0
					dash.gauges[2].Value = celsius
					dash.gauges[2].Current = fmt.Sprintf("%.1f°C", celsius)
					break
				}
			}
		}
	}

	// Disk usage.
	if runtime.GOOS == "linux" {
		total, used, _ := getDiskInfo()
		if total > 0 {
			pct := float64(used) / float64(total) * 100
			dash.gauges[3].Value = pct
			usedGB := float64(used) / (1024 * 1024 * 1024)
			totalGB := float64(total) / (1024 * 1024 * 1024)
			dash.gauges[3].Current = fmt.Sprintf("%.1f/%.1f GB", usedGB, totalGB)
		}
	}

	// Network status.
	if IsOnline() {
		dash.gauges[4].Value = 100
		dash.gauges[4].Current = "Online"
		dash.gauges[4].Color = sdl.Color{R: 60, G: 200, B: 140, A: 255}
	} else {
		dash.gauges[4].Value = 0
		dash.gauges[4].Current = "Offline"
		dash.gauges[4].Color = sdl.Color{R: 220, G: 80, B: 80, A: 255}
	}
}

func parseMemKB(line string) int64 {
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return 0
	}
	v, _ := strconv.ParseInt(parts[1], 10, 64)
	return v * 1024 // kB to bytes
}

// renderDashboard draws the full-screen system dashboard.
func renderDashboard(renderer *sdl.Renderer, config *Config) {
	if !dash.inited {
		dashInit()
	}
	dashPoll()

	renderer.SetDrawColor(8, 10, 18, 255)
	renderer.Clear()

	font, _ := getCachedFont(config, "small")
	bigFont, _ := getCachedFont(config, "big")
	if bigFont == nil {
		bigFont, _ = getCachedFont(config, "medium")
	}
	medFont, _ := getCachedFont(config, "medium")
	if medFont == nil {
		medFont = font
	}

	// Title.
	title := "System Monitor"
	if medFont != nil {
		tw, _, _ := medFont.SizeUTF8(title)
		renderText(renderer, config, medFont, title,
			sdl.Color{R: 200, G: 210, B: 230, A: 220},
			(screenWidth-int32(tw))/2, 20)
	}

	// Animated gauges — 5 in a row.
	gaugeR := int32(68)
	gaugeGap := int32(140)
	totalW := gaugeGap * 4
	startX := (screenWidth - totalW) / 2
	gaugeY := int32(160)

	for i := range dash.gauges {
		g := &dash.gauges[i]

		// Smooth animation toward target.
		diff := g.Value - g.AnimVal
		g.AnimVal += diff * 0.08
		if math.Abs(diff) < 0.5 {
			g.AnimVal = g.Value
		}

		gx := startX + int32(i)*gaugeGap
		gy := gaugeY

		// Gauge track (dark ring).
		renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		for ring := int32(0); ring < 3; ring++ {
			a := uint8(100 - ring*30)
			renderer.SetDrawColor(30, 35, 48, a)
			strokeCircle(renderer, gx, gy, gaugeR+ring, sdl.Color{R: 30, G: 35, B: 48, A: a})
		}

		// Inner fill.
		fillCircle(renderer, gx, gy, gaugeR-3, sdl.Color{R: 10, G: 14, B: 22, A: 255})

		// Progress arc — sweep from -90° (top) clockwise.
		var pct float64
		if g.Max > 0 {
			pct = g.AnimVal / g.Max
		} else {
			pct = g.AnimVal / 100.0
		}
		if pct > 1 {
			pct = 1
		}
		if pct < 0 {
			pct = 0
		}
		if pct > 0.005 {
			// Draw arc as filled sectors.
			arcColor := g.Color
			// Warn colors for high values.
			if pct > 0.85 && (i == 0 || i == 1 || i == 3) {
				arcColor = sdl.Color{R: 220, G: 80, B: 60, A: 255}
			}
			renderCircleSectorColor(renderer, gx, gy, gaugeR, gaugeR-8,
				-math.Pi/2, -math.Pi/2+2*math.Pi*pct, arcColor)
		}

		// Glow behind value text.
		glowA := uint8(30 * pct)
		if glowA > 0 {
			renderer.SetDrawColor(g.Color.R, g.Color.G, g.Color.B, glowA)
			renderer.FillRect(&sdl.Rect{X: gx - 30, Y: gy - 14, W: 60, H: 28})
		}

		// Value text in center.
		if bigFont != nil && g.Current != "" {
			vw, vh, _ := bigFont.SizeUTF8(g.Current)
			vx := gx - int32(vw)/2
			vy := gy - int32(vh)/2
			renderText(renderer, config, bigFont, g.Current,
				sdl.Color{R: 240, G: 245, B: 255, A: 255}, vx, vy)
		}

		// Label below gauge.
		if font != nil {
			lw, _, _ := font.SizeUTF8(g.Label)
			renderText(renderer, config, font, g.Label,
				sdl.Color{R: 160, G: 175, B: 200, A: 200},
				gx-int32(lw)/2, gy+gaugeR+12)
		}
	}

	// Detailed info grid below gauges.
	infoY := gaugeY + gaugeR + 50
	infoX := int32(60)
	infoW := int32(screenWidth - 120)
	infoH := int32(200)

	drawCard(renderer, infoX, infoY, infoW, infoH, 16)

	if font != nil {
		col1X := infoX + 24
		col2X := infoX + infoW/2 + 12
		lineH := int32(22)
		y := infoY + 16

		// Column 1: system info
		infoLines := []string{
			fmt.Sprintf("OS:       %s", runtime.GOOS),
			fmt.Sprintf("Arch:     %s", runtime.GOARCH),
			fmt.Sprintf("CPUs:     %d", runtime.NumCPU()),
			fmt.Sprintf("Platform: %s", P().Name()),
		}
		for _, line := range infoLines {
			renderText(renderer, config, font, line,
				sdl.Color{R: 140, G: 155, B: 180, A: 200}, col1X, y)
			y += lineH
		}

		// Column 2: live stats
		y2 := infoY + 16
		uptime := time.Since(dash.startTime)
		uStr := fmt.Sprintf("Uptime:   %dh %dm %ds", int(uptime.Hours()), int(uptime.Minutes())%60, int(uptime.Seconds())%60)
		renderText(renderer, config, font, uStr,
			sdl.Color{R: 140, G: 155, B: 180, A: 200}, col2X, y2)
		y2 += lineH

		memStr := fmt.Sprintf("RAM:      %s", dash.gauges[1].Current)
		renderText(renderer, config, font, memStr,
			sdl.Color{R: 140, G: 155, B: 180, A: 200}, col2X, y2)
		y2 += lineH

		diskStr := fmt.Sprintf("Disk:     %s", dash.gauges[3].Current)
		renderText(renderer, config, font, diskStr,
			sdl.Color{R: 140, G: 155, B: 180, A: 200}, col2X, y2)
		y2 += lineH

		tempStr := fmt.Sprintf("Temp:     %s", dash.gauges[2].Current)
		renderText(renderer, config, font, tempStr,
			sdl.Color{R: 140, G: 155, B: 180, A: 200}, col2X, y2)
	}

	// Controls hint.
	if font != nil {
		hint := "B/Esc: Back"
		hw, _, _ := font.SizeUTF8(hint)
		renderText(renderer, config, font, hint,
			sdl.Color{R: 80, G: 90, B: 110, A: 120},
			(screenWidth-int32(hw))/2, screenHeight-30)
	}
}

// handleDashInput processes keyboard input for the dashboard scene.
func handleDashInput(e *sdl.KeyboardEvent, config *Config) {
	if e == nil || e.Type != sdl.KEYDOWN {
		return
	}
	if e.Keysym.Sym == sdl.K_ESCAPE || e.Keysym.Sym == sdl.K_b {
		goBackScene(config)
		PlayBackSound()
	}
}
