package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Music Player with Visualizer and Playlist
// ──────────────────────────────────────────────────────────────────────

const (
	audioExtensions = ".mp3,.wav,.ogg,.m4a,.flac,.aac,.wma"
	visualBars      = 32
	visualBarW      = 12
	visualBarGap    = 4
	visualMaxH      = 120
)

type musicTrack struct {
	Path     string
	Name     string
	Duration string
	Playing  bool
}

type musicState struct {
	tracks     []musicTrack
	current    int
	playing    bool
	paused     bool
	volume     float64
	progress   float64 // 0..1
	position   time.Duration
	duration   time.Duration
	visualizer [visualBars]float64 // smoothed bar heights
	fileList   []FileEntry         // current directory
	cursor     int                 // file browser cursor
	dir        string              // current directory
	showVisual bool
	shuffle    bool
	repeat     bool
	mu         sync.Mutex
}

var music musicState

func musicInit() {
	music = musicState{
		volume:     0.8,
		showVisual: true,
		dir:        ".",
	}
	musicScanDir(".")
}

func musicScanDir(dir string) {
	music.dir = dir
	music.fileList = music.fileList[:0]

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if strings.Contains(audioExtensions, ext) {
			fullPath := filepath.Join(dir, e.Name())
			music.fileList = append(music.fileList, FileEntry{
				Name:  e.Name(),
				Path:  fullPath,
				IsDir: false,
			})
		}
	}

	// Add directories for navigation.
	var dirs []FileEntry
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			fullPath := filepath.Join(dir, e.Name())
			dirs = append(dirs, FileEntry{
				Name:  e.Name(),
				Path:  fullPath,
				IsDir: true,
			})
		}
	}
	music.fileList = append(dirs, music.fileList...)

	// Add parent directory.
	if dir != "." {
		parent := filepath.Dir(dir)
		music.fileList = append([]FileEntry{{Name: "..", Path: parent, IsDir: true}}, music.fileList...)
	}

	music.cursor = 0
}

func musicPlayTrack(index int) {
	if index < 0 || index >= len(music.tracks) {
		return
	}

	// Stop current.
	musicStop()

	music.current = index
	music.tracks[index].Playing = true
	music.playing = true
	music.paused = false
	music.position = 0

	// Play using external player.
	track := music.tracks[index]
	go musicPlayFile(track.Path)
	PlayActivateSound()
}

func musicPlayFile(path string) {
	// Use ffplay or mpv for playback.
	player := "ffplay"
	playerPath := ""
	if p, err := lookPath(player); err == nil {
		playerPath = p
	} else if p, err := lookPath("mpv"); err == nil {
		playerPath = p
		player = "mpv"
	}

	if playerPath == "" {
		showToast("No audio player found (ffplay/mpv)", ToastError())
		return
	}

	args := []string{"-nodisp", "-autoexit", path}
	if player == "mpv" {
		args = []string{"--no-video", "--really-quiet", path}
	}

	cmd := execCommand(playerPath, args...)
	if cmd == nil {
		return
	}
	cmd.Stdout = nil
	cmd.Stderr = nil
	_ = cmd.Run()

	// Track finished.
	music.mu.Lock()
	music.playing = false
	music.tracks[music.current].Playing = false
	music.mu.Unlock()

	// Auto-advance.
	if music.repeat {
		musicPlayTrack(music.current)
	} else if music.current+1 < len(music.tracks) {
		musicPlayTrack(music.current + 1)
	}
}

func musicStop() {
	music.mu.Lock()
	if music.current >= 0 && music.current < len(music.tracks) {
		music.tracks[music.current].Playing = false
	}
	music.playing = false
	music.paused = false
	music.mu.Unlock()

	// Kill any running player process.
	killAudioProcesses()
}

func musicTogglePause() {
	music.mu.Lock()
	music.paused = !music.paused
	music.mu.Unlock()
	if music.paused {
		pauseAudio(true)
		PlayBackSound()
	} else {
		pauseAudio(false)
		PlayActivateSound()
	}
}

func musicNext() {
	if len(music.tracks) == 0 {
		return
	}
	next := music.current + 1
	if next >= len(music.tracks) {
		next = 0
	}
	musicPlayTrack(next)
}

func musicPrev() {
	if len(music.tracks) == 0 {
		return
	}
	prev := music.current - 1
	if prev < 0 {
		prev = len(music.tracks) - 1
	}
	musicPlayTrack(prev)
}

// ──────────────────────────────────────────────────────────────────────
// Visualizer
// ──────────────────────────────────────────────────────────────────────

func musicUpdateVisualizer() {
	if !music.playing || music.paused {
		// Decay bars when paused.
		for i := range music.visualizer {
			music.visualizer[i] *= 0.9
		}
		return
	}

	// Simulate audio visualization based on time.
	// In a real implementation, this would analyze audio buffers.
	now := float64(time.Now().UnixNano()) / 1e9
	for i := range music.visualizer {
		// Create organic-looking bars with multiple sine waves.
		freq1 := math.Sin(now*3.0+float64(i)*0.5) * 0.3
		freq2 := math.Sin(now*5.0+float64(i)*0.8) * 0.2
		freq3 := math.Sin(now*7.0+float64(i)*1.2) * 0.15
		target := (0.4 + freq1 + freq2 + freq3) * float64(visualMaxH)

		// Smooth animation.
		diff := target - music.visualizer[i]
		music.visualizer[i] += diff * 0.15

		// Clamp.
		if music.visualizer[i] < 0 {
			music.visualizer[i] = 0
		}
		if music.visualizer[i] > float64(visualMaxH) {
			music.visualizer[i] = float64(visualMaxH)
		}
	}
}

func renderVisualizer(renderer *sdl.Renderer, x, y int32) {
	musicUpdateVisualizer()

	barTotalW := int32(visualBars * (visualBarW + visualBarGap))
	startX := x + (screenWidth-int32(barTotalW))/2

	for i := range music.visualizer {
		barH := int32(music.visualizer[i])
		if barH < 2 {
			barH = 2
		}
		bx := startX + int32(i)*(visualBarW+visualBarGap)
		by := y + visualMaxH - barH

		// Gradient bar: bottom is accent color, top fades.
		for dy := int32(0); dy < barH; dy++ {
			frac := float64(dy) / float64(visualMaxH)
			r := uint8(float64(accentColor.R) * (0.5 + 0.5*frac))
			g := uint8(float64(accentColor.G) * (0.5 + 0.5*frac))
			b := uint8(float64(accentColor.B) * (0.5 + 0.5*frac))
			a := uint8(180 + 75*frac)
			renderer.SetDrawColor(r, g, b, a)
			renderer.FillRect(&sdl.Rect{X: bx, Y: by + dy, W: visualBarW, H: 1})
		}

		// Peak indicator.
		if barH > 4 {
			renderer.SetDrawColor(255, 255, 255, 120)
			renderer.FillRect(&sdl.Rect{X: bx, Y: by - 2, W: visualBarW, H: 2})
		}
	}
}

// ──────────────────────────────────────────────────────────────────────
// Rendering
// ──────────────────────────────────────────────────────────────────────

func renderMusicPlayer(renderer *sdl.Renderer, config *Config) {
	renderer.SetDrawColor(8, 10, 18, 255)
	renderer.Clear()

	font, _ := getCachedFont(config, "small")
	medFont, _ := getCachedFont(config, "medium")
	bigFont, _ := getCachedFont(config, "big")
	if font == nil {
		return
	}

	// Header.
	barH := int32(44)
	renderer.SetDrawColor(14, 18, 28, 240)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: barH})
	renderer.SetDrawColor(40, 50, 65, 120)
	renderer.FillRect(&sdl.Rect{X: 0, Y: barH - 1, W: screenWidth, H: 1})

	if medFont != nil {
		title := "Music Player"
		renderText(renderer, config, medFont, title,
			sdl.Color{R: 220, G: 230, B: 245, A: 240}, 16, 10)
	}

	// Visualizer at top center.
	visY := int32(60)
	renderVisualizer(renderer, 0, visY)

	// Now playing section.
	npY := visY + visualMaxH + 30
	if music.current >= 0 && music.current < len(music.tracks) {
		track := music.tracks[music.current]

		// Track name.
		if bigFont != nil {
			name := track.Name
			if len(name) > 30 {
				name = name[:30] + "..."
			}
			nw, _, _ := bigFont.SizeUTF8(name)
			renderText(renderer, config, bigFont, name,
				sdl.Color{R: 240, G: 245, B: 255, A: 255},
				(screenWidth-int32(nw))/2, npY)
		}

		// Progress bar.
		barW := int32(500)
		barX := (screenWidth - barW) / 2
		barY2 := npY + 40
		barH2 := int32(6)

		// Track.
		fillRoundedRect(renderer, barX, barY2, barW, barH2, 3,
			sdl.Color{R: 30, G: 36, B: 50, A: 200})

		// Progress.
		progressW := int32(float64(barW) * music.progress)
		if progressW > 0 {
			fillRoundedRect(renderer, barX, barY2, progressW, barH2, 3,
				accentColor)
		}

		// Play state.
		stateStr := "> Playing"
		if music.paused {
			stateStr = "|| Paused"
		} else if !music.playing {
			stateStr = "[Stopped]"
		}
		if font != nil {
			sw, _, _ := font.SizeUTF8(stateStr)
			renderText(renderer, config, font, stateStr,
				sdl.Color{R: 160, G: 175, B: 200, A: 200},
				(screenWidth-int32(sw))/2, barY2+14)
		}

		// Volume indicator.
		volStr := fmt.Sprintf("Vol: %d%%", int(music.volume*100))
		vw, _, _ := font.SizeUTF8(volStr)
		renderText(renderer, config, font, volStr,
			sdl.Color{R: 140, G: 155, B: 180, A: 200},
			screenWidth-int32(vw)-16, npY+40)
	}

	// Controls.
	ctrlY := npY + 70
	ctrlW := int32(300)
	ctrlX := (screenWidth - ctrlW) / 2

	// Prev button.
	prevW := int32(60)
	prevH := int32(40)
	prevX := ctrlX
	fillRoundedRect(renderer, prevX, ctrlY, prevW, prevH, 10,
		sdl.Color{R: 30, G: 36, B: 50, A: 200})
	strokeRoundedRect(renderer, prevX, ctrlY, prevW, prevH, 10, 1, ColorBorder)
	if font != nil {
		renderText(renderer, config, font, "|<",
			sdl.Color{R: 200, G: 210, B: 230, A: 220},
			prevX+20, ctrlY+8)
	}

	// Play/Pause button.
	playW := int32(80)
	playX := ctrlX + (ctrlW-playW)/2
	playFill := sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 255}
	if music.paused {
		playFill = sdl.Color{R: 200, G: 210, B: 230, A: 220}
	}
	fillRoundedRect(renderer, playX, ctrlY, playW, prevH, 10, playFill)
	if font != nil {
		playIcon := ">"
		if music.playing && !music.paused {
			playIcon = "||"
		}
		renderText(renderer, config, font, playIcon,
			sdl.Color{R: 255, G: 255, B: 255, A: 255},
			playX+30, ctrlY+8)
	}

	// Next button.
	nextX := ctrlX + ctrlW - prevW
	fillRoundedRect(renderer, nextX, ctrlY, prevW, prevH, 10,
		sdl.Color{R: 30, G: 36, B: 50, A: 200})
	strokeRoundedRect(renderer, nextX, ctrlY, prevW, prevH, 10, 1, ColorBorder)
	if font != nil {
		renderText(renderer, config, font, ">|",
			sdl.Color{R: 200, G: 210, B: 230, A: 220},
			nextX+20, ctrlY+8)
	}

	// File browser / playlist.
	listY := ctrlY + prevH + 20
	listH := screenHeight - listY - 50

	drawCard(renderer, 40, listY, screenWidth-80, listH, 16)

	if len(music.fileList) == 0 {
		if font != nil {
			empty := "No music files in current directory"
			ew, _, _ := font.SizeUTF8(empty)
			renderText(renderer, config, font, empty,
				sdl.Color{R: 100, G: 115, B: 140, A: 150},
				(screenWidth-int32(ew))/2, listY+40)
		}
	} else {
		lineH := int32(28)
		maxVisible := int((listH - 16) / lineH)

		start := 0
		if music.cursor >= maxVisible {
			start = music.cursor - maxVisible + 1
		}
		end := start + maxVisible
		if end > len(music.fileList) {
			end = len(music.fileList)
		}

		for i := start; i < end; i++ {
			entry := music.fileList[i]
			ry := listY + 8 + int32(i-start)*lineH
			focused := i == music.cursor

			if focused {
				fillRoundedRect(renderer, 48, ry, screenWidth-96, lineH-2, 6,
					sdl.Color{R: 30, G: 36, B: 50, A: 200})
			}

			// Icon.
			icon := "[d]"
			if entry.IsDir {
				icon = "[+]"
			} else {
				ext := strings.ToLower(filepath.Ext(entry.Name))
				switch ext {
				case ".mp3", ".wav", ".ogg", ".m4a":
					icon = "[M]"
				case ".flac":
					icon = "[M]"
				}
			}
			renderText(renderer, config, font, icon,
				sdl.Color{R: 180, G: 190, B: 210, A: 220}, 56, ry+2)

			// Name.
			name := entry.Name
			if len(name) > 50 {
				name = name[:50] + "..."
			}
			nameCol := sdl.Color{R: 180, G: 190, B: 210, A: 220}
			if entry.IsDir {
				nameCol = sdl.Color{R: 140, G: 200, B: 255, A: 220}
			}
			renderText(renderer, config, font, name, nameCol, 80, ry+2)

			// Highlight indicator.
			if focused {
				renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 200)
				renderer.FillRect(&sdl.Rect{X: 44, Y: ry + 2, W: 3, H: lineH - 6})
			}
		}

		// Scrollbar.
		if len(music.fileList) > maxVisible {
			thumbFrac := float64(maxVisible) / float64(len(music.fileList))
			scrollFrac := float64(start) / float64(len(music.fileList)-maxVisible)
			drawScrollbar(renderer, screenWidth-56, listY+8, 3, listH-16, thumbFrac, scrollFrac)
		}
	}

	// Controls hint.
	if font != nil {
		controls := "D-Pad: Navigate | A: Play/Open | Enter: Select | B: Back"
		cw, _, _ := font.SizeUTF8(controls)
		renderText(renderer, config, font, controls,
			sdl.Color{R: 80, G: 90, B: 110, A: 120},
			(screenWidth-int32(cw))/2, screenHeight-24)
	}
}

func handleMusicInput(e *sdl.KeyboardEvent, config *Config) {
	if e == nil || e.Type != sdl.KEYDOWN {
		return
	}

	switch e.Keysym.Sym {
	case sdl.K_UP:
		if music.cursor > 0 {
			music.cursor--
			PlayNavSound()
		}
	case sdl.K_DOWN:
		if music.cursor < len(music.fileList)-1 {
			music.cursor++
			PlayNavSound()
		}
	case sdl.K_RETURN, sdl.K_SPACE:
		if music.cursor >= 0 && music.cursor < len(music.fileList) {
			entry := music.fileList[music.cursor]
			if entry.IsDir {
				musicScanDir(entry.Path)
				PlayActivateSound()
			} else {
				// Play the file directly.
				music.tracks = []musicTrack{{
					Path:    entry.Path,
					Name:    entry.Name,
					Playing: true,
				}}
				music.current = 0
				music.playing = true
				music.paused = false
				go musicPlayFile(entry.Path)
				PlayActivateSound()
			}
		}
	case sdl.K_p:
		musicTogglePause()
	case sdl.K_n:
		musicNext()
	case sdl.K_b, sdl.K_ESCAPE:
		// Go up directory or back.
		if music.dir != "." && music.dir != "/" {
			parent := filepath.Dir(music.dir)
			musicScanDir(parent)
			PlayBackSound()
		} else {
			goBackScene(config)
			PlayBackSound()
		}
	}
}

// ──────────────────────────────────────────────────────────────────────
// Helper stubs for audio control
// ──────────────────────────────────────────────────────────────────────

func lookPath(name string) (string, error) {
	return P().LookPath(name)
}

func execCommand(name string, args ...string) *exec.Cmd {
	return P().CommandContext(context.Background(), name, args...)
}

func pauseAudio(paused bool) {
	PauseVideoAudio(paused)
}

func killAudioProcesses() {
	ClearVideoAudioQueue()
}
