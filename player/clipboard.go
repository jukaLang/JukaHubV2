package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Clipboard Manager — copy/paste history with search
// Press Ctrl+V (hold) or Ctrl+Shift+V to open
// ──────────────────────────────────────────────────────────────────────

const clipMaxHistory = 30

type clipEntry struct {
	text      string
	timestamp time.Time
	source    string // where it was copied from
}

var clipHistory []clipEntry
var clipSearchOpen bool
var clipSearchBuf string
var clipCursorIdx int // which entry is focused

func clipPush(text, source string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	// Don't duplicate the most recent entry
	if len(clipHistory) > 0 && clipHistory[0].text == text {
		clipHistory[0].timestamp = time.Now()
		return
	}
	entry := clipEntry{text: text, timestamp: time.Now(), source: source}
	clipHistory = append([]clipEntry{entry}, clipHistory...)
	if len(clipHistory) > clipMaxHistory {
		clipHistory = clipHistory[:clipMaxHistory]
	}
}

func renderClipboardOverlay(renderer *sdl.Renderer, config *Config) {
	if !clipSearchOpen {
		return
	}

	// Dim background
	drawRect(renderer, &sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight}, 0, 0, 0, 180)

	// Card
	cardW := int32(700)
	cardH := int32(520)
	cardX := (screenWidth - cardW) / 2
	cardY := (screenHeight - cardH) / 2
	drawRoundedRect(renderer, &sdl.Rect{X: cardX, Y: cardY, W: cardW, H: cardH}, 16, 22, 22, 28, 245)

	// Title
	titleFont := getDisplayFont(config, 22)
	title := "[Clipboard] History"
	titleW, _, _ := titleFont.SizeUTF8(title)
	drawText(renderer, titleFont, title, cardX+(cardW-int32(titleW))/2, cardY+16, getAccentColor(config), textAlignLeft)

	// Search bar
	searchY := cardY + 52
	drawRoundedRect(renderer, &sdl.Rect{X: cardX + 16, Y: searchY, W: cardW - 32, H: 32}, 6, 35, 35, 45, 255)
	searchFont := getBodyFont(config, 14)
	searchPlaceholder := "[Search] Type to search..."
	if clipSearchBuf != "" {
		drawText(renderer, searchFont, clipSearchBuf+"_", cardX+24, searchY+8, colorNew(200, 200, 215, 255), textAlignLeft)
	} else {
		drawText(renderer, searchFont, searchPlaceholder, cardX+24, searchY+8, colorNew(100, 100, 115, 255), textAlignLeft)
	}

	// Filter entries
	filtered := clipHistory
	if clipSearchBuf != "" {
		q := strings.ToLower(clipSearchBuf)
		filtered = nil
		for _, e := range clipHistory {
			if strings.Contains(strings.ToLower(e.text), q) || strings.Contains(strings.ToLower(e.source), q) {
				filtered = append(filtered, e)
			}
		}
	}

	// Clamp cursor
	if len(filtered) == 0 {
		clipCursorIdx = 0
	} else if clipCursorIdx >= len(filtered) {
		clipCursorIdx = len(filtered) - 1
	}

	// Entry list
	listY := searchY + 42
	listH := cardH - 100
	entryFont := getBodyFont(config, 13)
	smallFont := getBodyFont(config, 11)
	entryH := int32(58)
	maxVisible := int(listH / entryH)

	startIdx := 0
	if clipCursorIdx >= maxVisible {
		startIdx = clipCursorIdx - maxVisible + 1
	}
	endIdx := startIdx + maxVisible
	if endIdx > len(filtered) {
		endIdx = len(filtered)
	}

	for i := startIdx; i < endIdx; i++ {
		e := filtered[i]
		y := listY + int32(i-startIdx)*entryH
		isFocused := i == clipCursorIdx

		// Entry background
		if isFocused {
			drawRoundedRect(renderer, &sdl.Rect{X: cardX + 16, Y: y, W: cardW - 32, H: entryH - 4}, 6, 40, 40, 55, 255)
		}

		// Text preview (truncate to 1-2 lines)
		text := e.text
		if len(text) > 80 {
			text = text[:77] + "..."
		}
		lines := strings.SplitN(text, "\n", 2)

		if isFocused {
			drawText(renderer, entryFont, lines[0], cardX+24, y+6, colorNew(220, 220, 235, 255), textAlignLeft)
		} else {
			drawText(renderer, entryFont, lines[0], cardX+24, y+6, colorNew(180, 180, 195, 255), textAlignLeft)
		}

		if len(lines) > 1 {
			sub := lines[1]
			if len(sub) > 60 {
				sub = sub[:57] + "..."
			}
			drawText(renderer, smallFont, sub, cardX+24, y+24, colorNew(110, 110, 125, 255), textAlignLeft)
		}

		// Timestamp
		ts := fmt.Sprintf("%s ago", timeSince(e.timestamp))
		tsW, _, _ := smallFont.SizeUTF8(ts)
		drawText(renderer, smallFont, ts, cardX+cardW-24-int32(tsW), y+6, colorNew(100, 100, 115, 255), textAlignLeft)

		// Source tag
		if e.source != "" {
			drawText(renderer, smallFont, e.source, cardX+cardW-24-int32(tsW), y+22, getAccentColor(config), textAlignLeft)
		}
	}

	if len(filtered) == 0 {
		emptyFont := getBodyFont(config, 16)
		emptyText := "No clipboard entries"
		ew, _, _ := emptyFont.SizeUTF8(emptyText)
		drawText(renderer, emptyFont, emptyText, cardX+(cardW-int32(ew))/2, listY+80, colorNew(100, 100, 115, 255), textAlignLeft)
	}

	// Footer
	footerFont := getBodyFont(config, 12)
	footer := "↑↓ navigate • Enter copy • Del clear • Esc close"
	fw, _, _ := footerFont.SizeUTF8(footer)
	drawText(renderer, footerFont, footer, cardX+(cardW-int32(fw))/2, cardY+cardH-28, colorNew(100, 100, 120, 255), textAlignLeft)

	// Scrollbar
	if len(filtered) > maxVisible {
		scrollBarH := listH - 4
		knobH := scrollBarH * int32(maxVisible) / int32(len(filtered))
		knobY := listY + scrollBarH*int32(clipCursorIdx)/int32(len(filtered))
		scrollX := cardX + cardW - 12
		drawRoundedRect(renderer, &sdl.Rect{X: scrollX, Y: listY, W: 4, H: scrollBarH}, 2, 35, 35, 45, 120)
		drawRoundedRect(renderer, &sdl.Rect{X: scrollX, Y: knobY, W: 4, H: knobH}, 2, 80, 80, 100, 255)
	}
}

func handleClipboardInput(event *sdl.KeyboardEvent) bool {
	if !clipSearchOpen {
		return false
	}
	if event.Type == sdl.KEYDOWN {
		switch event.Keysym.Sym {
		case sdl.K_ESCAPE:
			clipSearchOpen = false
			clipSearchBuf = ""
			return true
		case sdl.K_UP:
			if clipCursorIdx > 0 {
				clipCursorIdx--
			}
			return true
		case sdl.K_DOWN:
			if clipCursorIdx < len(clipHistory)-1 {
				clipCursorIdx++
			}
			return true
		case sdl.K_RETURN, sdl.K_KP_ENTER:
			if clipCursorIdx >= 0 && clipCursorIdx < len(clipHistory) {
				sdl.SetClipboardText(clipHistory[clipCursorIdx].text)
				ShowToast("Copied to clipboard", ToastKindSuccess)
				clipSearchOpen = false
				clipSearchBuf = ""
			}
			return true
		case sdl.K_DELETE:
			if clipCursorIdx >= 0 && clipCursorIdx < len(clipHistory) {
				clipHistory = append(clipHistory[:clipCursorIdx], clipHistory[clipCursorIdx+1:]...)
				if clipCursorIdx >= len(clipHistory) {
					clipCursorIdx = len(clipHistory) - 1
				}
			}
			return true
		case sdl.K_BACKSPACE:
			if len(clipSearchBuf) > 0 {
				clipSearchBuf = clipSearchBuf[:len(clipSearchBuf)-1]
			}
			return true
		case sdl.K_u:
			if event.Keysym.Mod&sdl.KMOD_CTRL != 0 {
				clipSearchBuf = ""
				return true
			}
		}
	}
	return true
}

func handleClipboardTextInput(text string) {
	if !clipSearchOpen {
		return
	}
	clipSearchBuf += text
	clipCursorIdx = 0
}

func timeSince(t time.Time) string {
	d := time.Since(t)
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
