package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Wallpaper Picker — browse and set custom wallpapers
// ──────────────────────────────────────────────────────────────────────

type wallpaperEntry struct {
	name string
	path string
}

var (
	wpEntries []wallpaperEntry
	wpCursor  int
	wpDir     string
	wpOpen    bool
	wpPreview string // currently selected preview path
	wpScrollY int
)

func initWallpaperPicker() {
	home := os.Getenv("HOME")
	if home == "" {
		home = os.Getenv("USERPROFILE")
	}
	// Common wallpaper directories
	dirs := []string{
		filepath.Join(home, "Pictures", "Wallpapers"),
		filepath.Join(home, "Pictures", "Backgrounds"),
		filepath.Join(home, "Pictures"),
		filepath.Join(home, "Downloads"),
		".",
	}
	for _, d := range dirs {
		if info, err := os.Stat(d); err == nil && info.IsDir() {
			wpDir = d
			break
		}
	}
	scanWallpaperDir()
}

func scanWallpaperDir() {
	wpEntries = nil
	entries, err := os.ReadDir(wpDir)
	if err != nil {
		return
	}
	// Add parent directory option
	wpEntries = append(wpEntries, wallpaperEntry{
		name: "[+] .. (parent directory)",
		path: filepath.Dir(wpDir),
	})
	for _, e := range entries {
		if e.IsDir() {
			name := strings.ToLower(e.Name())
			if strings.HasPrefix(name, ".") {
				continue
			}
			wpEntries = append(wpEntries, wallpaperEntry{
				name: "[+] " + e.Name(),
				path: filepath.Join(wpDir, e.Name()),
			})
		} else {
			name := strings.ToLower(e.Name())
			ext := filepath.Ext(name)
			switch ext {
			case ".jpg", ".jpeg", ".png", ".bmp":
				wpEntries = append(wpEntries, wallpaperEntry{
					name: e.Name(),
					path: filepath.Join(wpDir, e.Name()),
				})
			}
		}
	}
	wpCursor = 0
	wpScrollY = 0
}

func renderWallpaperPicker(renderer *sdl.Renderer, config *Config) {
	if !wpOpen {
		return
	}

	// Background
	renderer.SetDrawColor(ColorBackground.R, ColorBackground.G, ColorBackground.B, 255)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	renderAmbientBackground(renderer)

	titleFont, _ := getCachedFont(config, "large")
	bodyFont, _ := getCachedFont(config, "medium")
	smallFont, _ := getCachedFont(config, "small")

	// Title
	if titleFont != nil {
		title := "[P]  Wallpaper Picker"
		tw, _, _ := titleFont.SizeUTF8(title)
		drawText(renderer, titleFont, title, (screenWidth-int32(tw))/2, 20, getAccentColor(config), textAlignLeft)
	}

	// Current directory
	if smallFont != nil {
		dirText := fmt.Sprintf("Dir: %s", wpDir)
		drawText(renderer, smallFont, dirText, 40, 60, ColorTextSecondary(), textAlignLeft)
	}

	// File list (left panel)
	listX := int32(40)
	listY := int32(90)
	listW := screenWidth/2 - 60
	listH := screenHeight - 140
	itemH := int32(36)
	maxVisible := int(listH / itemH)

	// Clamp cursor
	if len(wpEntries) == 0 {
		wpCursor = 0
	} else if wpCursor >= len(wpEntries) {
		wpCursor = len(wpEntries) - 1
	}
	if wpCursor < wpScrollY {
		wpScrollY = wpCursor
	}
	if wpCursor >= wpScrollY+maxVisible {
		wpScrollY = wpCursor - maxVisible + 1
	}

	for i := wpScrollY; i < len(wpEntries) && i < wpScrollY+maxVisible; i++ {
		e := wpEntries[i]
		y := listY + int32(i-wpScrollY)*itemH
		focused := i == wpCursor

		if focused {
			fillRoundedRect(renderer, listX-4, y, listW+8, itemH-2, 6, ColorCardFocus)
		}

		font := bodyFont
		if font == nil {
			font = smallFont
		}
		if font != nil {
			col := ColorTextPrimary()
			if focused {
				col = getAccentColor(config)
			}
			drawText(renderer, font, e.name, listX+8, y+6, col, textAlignLeft)
		}
	}

	// Preview panel (right side)
	previewX := screenWidth/2 + 20
	previewW := screenWidth/2 - 60
	previewH := int32(400)
	previewY := listY

	// Preview background
	fillRoundedRect(renderer, previewX, previewY, previewW, previewH, 8, ColorPanel)

	if wpCursor >= 0 && wpCursor < len(wpEntries) {
		e := wpEntries[wpCursor]
		if !strings.HasSuffix(e.name, "/") && !strings.HasPrefix(e.name, "[+]") {
			// Show file info
			if bodyFont != nil {
				drawText(renderer, bodyFont, e.name, previewX+16, previewY+16, ColorTextPrimary(), textAlignLeft)
			}
			if smallFont != nil {
				drawText(renderer, smallFont, e.path, previewX+16, previewY+48, ColorTextSecondary(), textAlignLeft)

				// File info
				if info, err := os.Stat(e.path); err == nil {
					sizeMB := float64(info.Size()) / (1024 * 1024)
					infoText := fmt.Sprintf("Size: %.1f MB", sizeMB)
					drawText(renderer, smallFont, infoText, previewX+16, previewY+76, ColorTextTertiary(), textAlignLeft)
				}

				// Instructions
				drawText(renderer, smallFont, "Press A to set as wallpaper", previewX+16, previewY+previewH-40,
					getAccentColor(config), textAlignLeft)
			}
		} else {
			if bodyFont != nil {
				drawText(renderer, bodyFont, "Open directory →", previewX+16, previewY+16,
					ColorTextSecondary(), textAlignLeft)
			}
			if smallFont != nil {
				drawText(renderer, smallFont, "Press A to enter", previewX+16, previewY+48,
					ColorTextTertiary(), textAlignLeft)
			}
		}
	}

	// Footer
	if smallFont != nil {
		footer := "↑↓ navigate • A select • B back • Esc close"
		fw, _, _ := smallFont.SizeUTF8(footer)
		drawText(renderer, smallFont, footer, (screenWidth-int32(fw))/2, screenHeight-30, ColorTextTertiary(), textAlignLeft)
	}
}

func handleWallpaperInput(event *sdl.KeyboardEvent, config *Config) bool {
	if !wpOpen {
		return false
	}
	switch event.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_b:
		wpOpen = false
		PlayBackSound()
	case sdl.K_UP:
		if wpCursor > 0 {
			wpCursor--
		}
		PlayNavSound()
	case sdl.K_DOWN:
		if wpCursor < len(wpEntries)-1 {
			wpCursor++
		}
		PlayNavSound()
	case sdl.K_RETURN, sdl.K_a:
		if wpCursor >= 0 && wpCursor < len(wpEntries) {
			e := wpEntries[wpCursor]
			if strings.HasPrefix(e.name, "[+]") {
				// Enter directory
				wpDir = e.path
				scanWallpaperDir()
			} else {
				// Set wallpaper
				config.Variables.BackgroundImage = e.path
				wpPreview = e.path
				// Reload wallpaper texture
				dynbg.wallpaper = nil // Force reload on next render
				ShowToast(fmt.Sprintf("[P] Wallpaper set: %s", e.name), ToastKindSuccess)
				wpOpen = false
			}
			PlayActivateSound()
		}
	case sdl.K_PAGEUP:
		wpCursor -= 10
		if wpCursor < 0 {
			wpCursor = 0
		}
	case sdl.K_PAGEDOWN:
		wpCursor += 10
		if wpCursor >= len(wpEntries) {
			wpCursor = len(wpEntries) - 1
		}
	}
	return true
}
