package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// ──────────────────────────────────────────────────────────────────────
// Notes App — markdown editor with folders and search
// ──────────────────────────────────────────────────────────────────────

type noteEntry struct {
	ID        int       `json:"id"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	Folder    string    `json:"folder"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type noteStore struct {
	Notes   []noteEntry `json:"notes"`
	NextID  int         `json:"next_id"`
	Folders []string    `json:"folders"`
}

type notesState struct {
	store       noteStore
	cursor      int
	tab         int // 0=list, 1=edit-title, 2=edit-content, 3=new-note
	editTitle   string
	editContent string
	editID      int // -1=new
	search      string
	filter      string // folder filter
	scrollY     int
	storePath   string
}

var notes notesState

func notesInit() {
	notes.storePath = "notes.json"
	notesLoad()
	if len(notes.store.Folders) == 0 {
		notes.store.Folders = []string{"All", "Personal", "Work", "Ideas"}
	}
}

func notesLoad() {
	var store noteStore
	// A damaged notes.json now falls back to the .bak the atomic save leaves
	// behind instead of silently starting empty, which is how a reader used to
	// conclude their notes had been deleted.
	if _, err := readJSONWithBackup(notes.storePath, &store); err != nil {
		notes.store = noteStore{NextID: 1}
		return
	}
	notes.store = store
}

func notesSave() {
	data, err := json.MarshalIndent(notes.store, "", "  ")
	if err != nil {
		return
	}
	// Notes are the one thing in the app nobody can regenerate, so the write
	// goes through the same temp+fsync+rename path as the config: a power cut
	// on a handheld must not leave a half-written notes.json behind.
	if err := AtomicWrite(notes.storePath, data, 0644); err != nil {
		Log().Warn("notes could not be saved", "path", notes.storePath, "err", err)
	}
}

func notesNewNote() {
	notes.store.NextID++
	n := noteEntry{
		ID:        notes.store.NextID,
		Title:     "New Note",
		Content:   "",
		Folder:    "Personal",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	notes.store.Notes = append([]noteEntry{n}, notes.store.Notes...)
	notes.editID = n.ID
	notes.editTitle = n.Title
	notes.editContent = n.Content
	notes.tab = 1
	notesSave()
}

func notesDeleteNote(id int) {
	for i, n := range notes.store.Notes {
		if n.ID == id {
			notes.store.Notes = append(notes.store.Notes[:i], notes.store.Notes[i+1:]...)
			break
		}
	}
	notesSave()
}

func notesSaveCurrentEdit() {
	if notes.editID < 0 {
		return
	}
	for i := range notes.store.Notes {
		if notes.store.Notes[i].ID == notes.editID {
			notes.store.Notes[i].Title = notes.editTitle
			notes.store.Notes[i].Content = notes.editContent
			notes.store.Notes[i].UpdatedAt = time.Now()
			break
		}
	}
	notesSave()
}

func notesFiltered() []noteEntry {
	var filtered []noteEntry
	for _, n := range notes.store.Notes {
		if notes.filter != "" && notes.filter != "All" && n.Folder != notes.filter {
			continue
		}
		if notes.search != "" {
			q := strings.ToLower(notes.search)
			if !strings.Contains(strings.ToLower(n.Title), q) &&
				!strings.Contains(strings.ToLower(n.Content), q) {
				continue
			}
		}
		filtered = append(filtered, n)
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].UpdatedAt.After(filtered[j].UpdatedAt)
	})
	return filtered
}

// ──────────────────────────────────────────────────────────────────────
// Rendering
// ──────────────────────────────────────────────────────────────────────

func renderNotes(renderer *sdl.Renderer, config *Config) {
	renderer.SetDrawColor(8, 10, 18, 255)
	renderer.Clear()

	font, _ := getCachedFont(config, "small")
	medFont, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}

	// The app's one bar owns the top of the screen; the body starts under it.
	barH := headerBarHeight()

	if notes.tab == 0 {
		notesRenderList(renderer, config, font, medFont, barH)
	} else {
		notesRenderEditor(renderer, config, font, medFont, barH)
	}

	// Note count. It used to sit in the bespoke header; the shared bar's
	// caption already names the scene, so the count rides the search row it
	// describes instead of competing with the wordmark.
	if notes.tab == 0 {
		count := len(notesFiltered())
		countStr := fmt.Sprintf("%d notes", count)
		if count == 1 {
			countStr = "1 note"
		}
		cw, _, _ := font.SizeUTF8(countStr)
		renderText(renderer, config, font, countStr,
			sdl.Color{R: 140, G: 155, B: 180, A: 200},
			screenWidth-int32(cw)-16, barH+16)
	}

	// The shared bars last: the header, its Back pill and the footer badges are
	// the same chrome every other screen draws.
	renderStatusBar(renderer, config)
	renderFooter(renderer, config)
}

func notesRenderList(renderer *sdl.Renderer, config *Config, font, medFont *ttf.Font, barH int32) {
	// Search bar.
	searchW := int32(400)
	searchH := int32(32)
	searchX := int32(16)
	searchY := barH + 8
	fillRoundedRect(renderer, searchX, searchY, searchW, searchH, 8,
		sdl.Color{R: 20, G: 24, B: 34, A: 200})
	strokeRoundedRect(renderer, searchX, searchY, searchW, searchH, 8, 1, ColorBorder)
	searchIcon := "[S]"
	renderText(renderer, config, font, searchIcon,
		sdl.Color{R: 100, G: 115, B: 140, A: 180}, searchX+8, searchY+6)
	// The text starts past the icon's real advance; a fixed +30 ran
	// "Search notes..." through the "[S]" badge.
	iconW, _ := measureText(font, searchIcon)
	textX := searchX + 8 + iconW + 8
	searchText := notes.search
	if searchText == "" {
		searchText = "Search notes..."
		renderText(renderer, config, font, searchText,
			sdl.Color{R: 80, G: 90, B: 110, A: 150}, textX, searchY+6)
	} else {
		renderText(renderer, config, font, searchText,
			sdl.Color{R: 200, G: 210, B: 230, A: 220}, textX, searchY+6)
	}

	// Folder tabs.
	folderX := searchX + searchW + 16
	for _, folder := range notes.store.Folders {
		fw, _, _ := font.SizeUTF8(folder)
		fw += 20
		fY := searchY
		fH := searchH
		active := notes.filter == folder || (notes.filter == "" && folder == "All")
		fill := sdl.Color{R: 25, G: 30, B: 40, A: 180}
		if active {
			fill = sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 60}
		}
		fillRoundedRect(renderer, folderX, fY, int32(fw), fH, 8, fill)
		col := sdl.Color{R: 140, G: 155, B: 180, A: 200}
		if active {
			col = sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 220}
		}
		renderText(renderer, config, font, folder, col, folderX+10, fY+6)
		folderX += int32(fw) + 8
	}

	// Note list. It stops at the shared footer, which is drawn over this band.
	listY := barH + 50
	listH := screenHeight - listY - footerBarHeight() - 4
	listX := int32(16)
	listW := screenWidth - 32

	drawCard(renderer, listX, listY, listW, listH, 16)

	filtered := notesFiltered()
	if len(filtered) == 0 {
		if medFont != nil {
			empty := "No notes yet"
			ew, _, _ := medFont.SizeUTF8(empty)
			renderText(renderer, config, medFont, empty,
				sdl.Color{R: 100, G: 115, B: 140, A: 150},
				(screenWidth-int32(ew))/2, listY+40)
			hint := "Press N to create a new note"
			hw, _, _ := font.SizeUTF8(hint)
			renderText(renderer, config, font, hint,
				sdl.Color{R: 80, G: 90, B: 110, A: 120},
				(screenWidth-int32(hw))/2, listY+70)
		}
		return
	}

	// The row stacks three lines. "small" and "medium" are both 24px here
	// (30px line box), so the preview and the metadata use a 16px face: three
	// 30px lines would need a 96px row. At 80 the title clears the preview and
	// the metadata instead of printing through them, as the old 64px row did.
	lineH := int32(80)
	subFont := getFontSized(config, "small", 16)
	if subFont == nil {
		subFont = font
	}
	maxVisible := int((listH - 16) / lineH)
	start := 0
	if notes.cursor >= maxVisible {
		start = notes.cursor - maxVisible + 1
	}
	end := start + maxVisible
	if end > len(filtered) {
		end = len(filtered)
	}

	for i := start; i < end; i++ {
		n := filtered[i]
		ry := listY + 8 + int32(i-start)*lineH
		focused := i == notes.cursor

		if focused {
			fillRoundedRect(renderer, listX+4, ry, listW-8, lineH-4, 10,
				sdl.Color{R: 30, G: 36, B: 50, A: 200})
			renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 200)
			renderer.FillRect(&sdl.Rect{X: listX + 4, Y: ry + 4, W: 3, H: lineH - 12})
		}

		// Title.
		title := truncateRunesSuffix(n.Title, 45, "...")
		renderText(renderer, config, medFont, title,
			sdl.Color{R: 220, G: 230, B: 245, A: 240}, listX+16, ry+2)

		// Preview.
		preview := truncateRunesSuffix(strings.ReplaceAll(n.Content, "\n", " "), 60, "...")
		renderText(renderer, config, subFont, preview,
			sdl.Color{R: 120, G: 135, B: 160, A: 180}, listX+16, ry+36)

		// Metadata.
		folderStr := n.Folder
		timeStr := n.UpdatedAt.Format("Jan 2 15:04")
		meta := fmt.Sprintf("%s  •  %s", folderStr, timeStr)
		renderText(renderer, config, subFont, meta,
			sdl.Color{R: 80, G: 95, B: 120, A: 140}, listX+16, ry+58)
	}

	// Scrollbar.
	if len(filtered) > maxVisible {
		thumbFrac := float64(maxVisible) / float64(len(filtered))
		scrollFrac := float64(start) / float64(len(filtered)-maxVisible)
		drawScrollbar(renderer, listX+listW-6, listY+8, 3, listH-16, thumbFrac, scrollFrac)
	}
}

func notesRenderEditor(renderer *sdl.Renderer, config *Config, font, medFont *ttf.Font, barH int32) {
	// Title field.
	titleY := barH + 16
	titleW := screenWidth - 64
	titleH := int32(40)
	titleX := int32(32)

	drawCard(renderer, titleX, titleY, titleW, titleH, 10)
	if notes.tab == 1 {
		strokeRoundedRect(renderer, titleX, titleY, titleW, titleH, 10, 2, accentColor)
	}

	titleText := notes.editTitle
	if titleText == "" {
		titleText = "Note title..."
	}
	renderText(renderer, config, medFont, titleText,
		sdl.Color{R: 220, G: 230, B: 245, A: 255}, titleX+12, titleY+8)

	// Folder selector.
	folderY := titleY + titleH + 8
	folderX := int32(32)
	for _, folder := range notes.store.Folders {
		if folder == "All" {
			continue
		}
		fw, _, _ := font.SizeUTF8(folder)
		fw += 16
		active := false
		if notes.editID >= 0 {
			for _, n := range notes.store.Notes {
				if n.ID == notes.editID {
					active = n.Folder == folder
					break
				}
			}
		}
		fill := sdl.Color{R: 25, G: 30, B: 40, A: 180}
		if active {
			fill = sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 60}
		}
		fillRoundedRect(renderer, folderX, folderY, int32(fw), 24, 8, fill)
		col := sdl.Color{R: 140, G: 155, B: 180, A: 200}
		if active {
			col = sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 220}
		}
		renderText(renderer, config, font, folder, col, folderX+8, folderY+4)
		folderX += int32(fw) + 8
	}

	// Content field.
	contentY := folderY + 36
	contentW := screenWidth - 64
	contentH := screenHeight - contentY - 60
	contentX := int32(32)

	drawCard(renderer, contentX, contentY, contentW, contentH, 16)
	if notes.tab == 2 {
		strokeRoundedRect(renderer, contentX, contentY, contentW, contentH, 16, 2, accentColor)
	}

	// Render content with simple markdown.
	content := notes.editContent
	if content == "" {
		content = "Start typing your note..."
		renderText(renderer, config, font, content,
			sdl.Color{R: 80, G: 90, B: 110, A: 150}, contentX+16, contentY+16)
	} else {
		lines := strings.Split(content, "\n")
		// 30px, not 20: the body font's line box is 30px on this canvas, so
		// 20px spacing printed every line through the one below it.
		lineH := int32(30)
		startLine := notes.scrollY / int(lineH)
		maxLines := int(contentH / lineH)

		for i, line := range lines {
			if i < startLine {
				continue
			}
			if i >= startLine+maxLines {
				break
			}
			ly := contentY + 16 + int32(i-startLine)*lineH
			col := sdl.Color{R: 180, G: 190, B: 210, A: 220}

			// Simple markdown rendering.
			if strings.HasPrefix(line, "# ") {
				if medFont != nil {
					renderText(renderer, config, medFont, line[2:],
						sdl.Color{R: 240, G: 245, B: 255, A: 255}, contentX+16, ly)
					continue
				}
			} else if strings.HasPrefix(line, "## ") {
				col = sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 220}
			} else if strings.HasPrefix(line, "- ") {
				line = "  • " + line[2:]
			} else if strings.HasPrefix(line, "> ") {
				line = "  | " + line[2:]
				col = sdl.Color{R: 120, G: 160, B: 200, A: 180}
			} else if strings.HasPrefix(line, "```") {
				col = sdl.Color{R: 100, G: 200, B: 140, A: 200}
			}
			renderText(renderer, config, font, line, col, contentX+16, ly)
		}
	}

	// Cursor indicator.
	if notes.tab == 2 {
		cursorY := contentY + 16
		cursorLines := strings.Split(notes.editContent[:min(len(notes.editContent), len(notes.editContent))], "\n")
		cursorY += int32(len(cursorLines)-1) * 30
		if time.Now().UnixMilli()%1000 < 500 {
			renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 200)
			renderer.FillRect(&sdl.Rect{X: contentX + 16, Y: cursorY, W: 2, H: 18})
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func handleNotesInput(e *sdl.KeyboardEvent, config *Config) {
	if e == nil || e.Type != sdl.KEYDOWN {
		return
	}

	filtered := notesFiltered()

	switch notes.tab {
	case 0: // List
		switch e.Keysym.Sym {
		case sdl.K_UP:
			if notes.cursor > 0 {
				notes.cursor--
				PlayNavSound()
			}
		case sdl.K_DOWN:
			if notes.cursor < len(filtered)-1 {
				notes.cursor++
				PlayNavSound()
			}
		case sdl.K_RETURN, sdl.K_SPACE:
			if notes.cursor >= 0 && notes.cursor < len(filtered) {
				n := filtered[notes.cursor]
				notes.editID = n.ID
				notes.editTitle = n.Title
				notes.editContent = n.Content
				notes.tab = 1
				PlayActivateSound()
			}
		case sdl.K_n:
			notesNewNote()
			PlayActivateSound()
		case sdl.K_x:
			if notes.cursor >= 0 && notes.cursor < len(filtered) {
				notesDeleteNote(filtered[notes.cursor].ID)
				if notes.cursor >= len(notesFiltered()) {
					notes.cursor = len(notesFiltered()) - 1
				}
				PlayBackSound()
			}
		case sdl.K_s:
			notes.search = ""
			notes.tab = 3 // search mode
			PlayActivateSound()
		case sdl.K_f:
			// Cycle folder filter.
			idx := 0
			for i, f := range notes.store.Folders {
				if f == notes.filter {
					idx = i
					break
				}
			}
			idx = (idx + 1) % len(notes.store.Folders)
			notes.filter = notes.store.Folders[idx]
			notes.cursor = 0
			PlayNavSound()
		case sdl.K_ESCAPE, sdl.K_b:
			goBackScene(config)
			PlayBackSound()
		}
	case 1: // Edit title
		switch e.Keysym.Sym {
		case sdl.K_RETURN, sdl.K_TAB:
			notes.tab = 2
			PlayActivateSound()
		case sdl.K_ESCAPE:
			// The footer promises "Save & Back" in both edit tabs; leaving the
			// title unsaved because Escape was pressed here contradicted it. B is
			// deliberately not a shortcut in this tab: it is a letter here.
			notesSaveCurrentEdit()
			notes.tab = 0
			PlayBackSound()
		case sdl.K_BACKSPACE:
			notes.editTitle = trimLastRune(notes.editTitle)
		}
	case 2: // Edit content
		switch e.Keysym.Sym {
		case sdl.K_ESCAPE:
			notesSaveCurrentEdit()
			notes.tab = 0
			PlayBackSound()
		case sdl.K_BACKSPACE:
			notes.editContent = trimLastRune(notes.editContent)
		case sdl.K_RETURN:
			notes.editContent += "\n"
		case sdl.K_PAGEUP:
			notes.scrollY -= 100
			if notes.scrollY < 0 {
				notes.scrollY = 0
			}
		case sdl.K_PAGEDOWN:
			notes.scrollY += 100
		}
	case 3: // Search
		switch e.Keysym.Sym {
		case sdl.K_RETURN, sdl.K_ESCAPE:
			notes.tab = 0
			notes.cursor = 0
			PlayBackSound()
		case sdl.K_BACKSPACE:
			notes.search = trimLastRune(notes.search)
		}
	}
}

func handleNotesTextInput(e *sdl.TextInputEvent) {
	for _, r := range e.GetText() {
		switch notes.tab {
		case 1:
			if len(notes.editTitle) < 80 {
				notes.editTitle += string(r)
			}
		case 2:
			if len(notes.editContent) < 10000 {
				notes.editContent += string(r)
			}
		case 3:
			if len(notes.search) < 60 {
				notes.search += string(r)
			}
		}
	}
}

// Scene controller mapping, moved here from controller.go so the scene owns
// its own input.

func ctrlNotes(e *sdl.ControllerButtonEvent) bool {
	if notes.tab != 0 {
		// In edit mode the pad only owns Back: B saves what was typed and
		// returns to the list instead of falling through to the scene's own
		// back and leaving Notes with the edit unsaved.
		if e.Button == sdl.CONTROLLER_BUTTON_B || e.Button == sdl.CONTROLLER_BUTTON_BACK {
			if notes.tab == 1 || notes.tab == 2 {
				notesSaveCurrentEdit()
			}
			notes.tab = 0
			PlayBackSound()
			return true
		}
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if notes.cursor > 0 {
			notes.cursor--
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		filtered := notesFiltered()
		if notes.cursor < len(filtered)-1 {
			notes.cursor++
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_A:
		filtered := notesFiltered()
		if notes.cursor >= 0 && notes.cursor < len(filtered) {
			note := filtered[notes.cursor]
			// Find the note in the store and open for editing
			for _, n := range notes.store.Notes {
				if n.ID == note.ID {
					notes.tab = 1
					notes.editTitle = n.Title
					notes.editContent = n.Content
					notes.editID = n.ID
					PlayActivateSound()
					return true
				}
			}
		}
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		// The list is the top level of the scene, so B leaves Notes instead of
		// silently staying on the list (the old handler looked like a no-op).
		goBackScene(appConfig)
	case sdl.CONTROLLER_BUTTON_Y:
		// Y is the footer's "New": the same fresh note N makes from a keyboard.
		// It used to drop into the search tab with an empty buffer, which looked
		// like a new note that could never be saved.
		notesNewNote()
		PlayActivateSound()
	case sdl.CONTROLLER_BUTTON_X:
		filtered := notesFiltered()
		if notes.cursor >= 0 && notes.cursor < len(filtered) {
			notesDeleteNote(filtered[notes.cursor].ID)
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		// Cycle folder filter left
		folders := notes.store.Folders
		if len(folders) > 0 {
			idx := 0
			for i, f := range folders {
				if f == notes.filter {
					idx = i
					break
				}
			}
			idx--
			if idx < 0 {
				idx = len(folders) - 1
			}
			notes.filter = folders[idx]
			notes.cursor = 0
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		// Cycle folder filter right
		folders := notes.store.Folders
		if len(folders) > 0 {
			idx := 0
			for i, f := range folders {
				if f == notes.filter {
					idx = i
					break
				}
			}
			idx++
			if idx >= len(folders) {
				idx = 0
			}
			notes.filter = folders[idx]
			notes.cursor = 0
		}
		PlayNavSound()
	default:
		return false
	}
	return true
}

// Scene registration.
type notesScene struct{ sceneBase }

func (notesScene) render(r *sdl.Renderer, c *Config) bool { renderNotes(r, c); return true }

func (notesScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	handleNotesInput(e, c)
	return true
}

func (notesScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return ctrlNotes(e)
}

func init() { registerScene(notesScene{sceneBase{name: "Notes"}}) }
