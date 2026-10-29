package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Clipboard Manager — copy/paste history with search
// Ctrl+Shift+V opens the history; Ctrl+C / Ctrl+V copy and paste
// ──────────────────────────────────────────────────────────────────────

// clipboardSetText / clipboardGetText stand in for the SDL clipboard calls so
// tests can drive copy and paste without touching the real system clipboard.
var clipboardSetText = func(text string) error { return sdl.SetClipboardText(text) }
var clipboardGetText = func() (string, error) { return sdl.GetClipboardText() }

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

// copyTextToClipboard puts text on the system clipboard and records it in
// the clipboard-manager history (Ctrl+Shift+V). Every copy in the app goes
// through here so the history fills itself for free and clipboard failures
// surface the same way everywhere. The error is returned so a caller that
// wants to speak for itself can; copyWithToast is the common flow.
func copyTextToClipboard(text, source string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("nothing to copy")
	}
	if err := clipboardSetText(text); err != nil {
		return err
	}
	clipPush(text, source)
	return nil
}

// pasteTextFromClipboard reads text off the system clipboard.
func pasteTextFromClipboard() (string, error) {
	return clipboardGetText()
}

// copyWithToast is the house copy flow: copy, record in the history, then one
// toast for whichever way it went. Callers keep their own wording for the
// success line ("Message copied", "Component report copied"...) so the toast
// still names what was copied.
func copyWithToast(text, source, successToast string) {
	if err := copyTextToClipboard(text, source); err != nil {
		ShowToast("Could not copy: "+err.Error(), ToastKindWarning)
		return
	}
	ShowToast(successToast, ToastKindSuccess)
}

// clipboardPasteIntoActiveField pastes the system clipboard into whatever
// text surface currently owns the keyboard, in the same way the shared text
// input path (handleTextInputEvent) would deliver typed text: the on-screen
// keyboard and its targets (Browser address and form fields, Chat composer,
// toolbox arguments, Wi-Fi passphrase...), then the two scenes that keep
// their own buffers (Terminal at the prompt cursor, Notes in the open field).
// It reports whether the chord was consumed and only touches the system
// clipboard when a surface is really editing, so Ctrl+V in a menu keeps its
// ordinary meaning instead of popping a failure toast.
func clipboardPasteIntoActiveField(config *Config) bool {
	switch {
	case virtualKeyboardActive, chatInputActive, unitInputActive, activeSceneIndex != -1,
		textBrowserAddressEditing, textBrowserSearchEditing, textBrowserFindEditing,
		browserFormEditing:
	case isTerminalScene():
		text, err := pasteTextFromClipboard()
		if err != nil {
			ShowToast("Clipboard is unavailable: "+err.Error(), ToastKindWarning)
			return true
		}
		if strings.TrimSpace(text) == "" {
			ShowToast("Nothing to paste", ToastKindInfo)
			return true
		}
		termInsertText(text)
	case isNotesScene():
		text, err := pasteTextFromClipboard()
		if err != nil {
			ShowToast("Clipboard is unavailable: "+err.Error(), ToastKindWarning)
			return true
		}
		if strings.TrimSpace(text) == "" {
			ShowToast("Nothing to paste", ToastKindInfo)
			return true
		}
		notesInsertText(text)
	default:
		return false
	}
	text, err := pasteTextFromClipboard()
	if err != nil {
		ShowToast("Clipboard is unavailable: "+err.Error(), ToastKindWarning)
		return true
	}
	if strings.TrimSpace(text) == "" {
		ShowToast("Nothing to paste", ToastKindInfo)
		return true
	}
	// vkInsertText is the app's one text-entry funnel: it strips control
	// bytes, replaces a field that opened with its old value selected, and
	// honours each target's own write-back.
	vkInsertText(text, config)
	return true
}

// clipboardCopyFromActiveField copies what the active text surface holds: the
// command line in the Terminal, the open field in Notes, the Chat composer
// draft, or the buffer an open on-screen keyboard is editing. Like the paste
// chord it is a no-op (with the chord left to ordinary handling) when no text
// is being edited.
func clipboardCopyFromActiveField(config *Config) bool {
	switch {
	case virtualKeyboardActive:
		text := vkCurrentText()
		if text == "" {
			return true
		}
		source := "Input"
		switch vkTarget {
		case vkTargetChat:
			source = "Chat"
		case vkTargetTerm:
			source = "Terminal"
		}
		copyWithToast(text, source, "Copied to clipboard")
	case chatInputActive:
		if chatInputText == "" {
			return true
		}
		copyWithToast(chatInputText, "Chat", "Copied to clipboard")
	case isTerminalScene():
		if term.input == "" {
			return true
		}
		copyWithToast(term.input, "Terminal", "Copied to clipboard")
	case isNotesScene():
		switch notes.tab {
		case 1:
			if notes.editTitle == "" {
				return true
			}
			copyWithToast(notes.editTitle, "Notes", "Copied to clipboard")
		case 2:
			if notes.editContent == "" {
				return true
			}
			copyWithToast(notes.editContent, "Notes", "Copied to clipboard")
		default:
			return false
		}
	default:
		return false
	}
	return true
}

// isNotesScene reports whether the Notes scene owns the screen now, using the
// same guarded read of the scene table isTerminalScene applies.
func isNotesScene() bool {
	if appConfig != nil && currentSceneIndex >= 0 && currentSceneIndex < len(appConfig.Scenes) {
		return appConfig.Scenes[currentSceneIndex].Name == "Notes"
	}
	return false
}

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
		text = truncateRunesSuffix(text, 80, "...")
		lines := strings.SplitN(text, "\n", 2)

		if isFocused {
			drawText(renderer, entryFont, lines[0], cardX+24, y+6, colorNew(220, 220, 235, 255), textAlignLeft)
		} else {
			drawText(renderer, entryFont, lines[0], cardX+24, y+6, colorNew(180, 180, 195, 255), textAlignLeft)
		}

		if len(lines) > 1 {
			sub := lines[1]
			sub = truncateRunesSuffix(sub, 60, "...")
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
	footer := hintLine(FooterHint{"↑ ↓", "Navigate"}, FooterHint{"Enter", "Copy"},
		FooterHint{"Del", "Clear"}, FooterHint{"Esc", "Close"})
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
				if err := copyTextToClipboard(clipHistory[clipCursorIdx].text, "History"); err != nil {
					ShowToast("Could not copy: "+err.Error(), ToastKindWarning)
				} else {
					ShowToast("Copied to clipboard", ToastKindSuccess)
				}
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
			clipSearchBuf = trimLastRune(clipSearchBuf)
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
