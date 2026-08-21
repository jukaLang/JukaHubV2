package main

import (
	"encoding/json"
	"fmt"
	"os"
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
	data, err := os.ReadFile(notes.storePath)
	if err != nil {
		notes.store = noteStore{NextID: 1}
		return
	}
	var store noteStore
	if json.Unmarshal(data, &store) == nil {
		notes.store = store
	}
}

func notesSave() {
	data, err := json.MarshalIndent(notes.store, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(notes.storePath, data, 0644)
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

	// Header.
	barH := int32(44)
	renderer.SetDrawColor(14, 18, 28, 240)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: barH})
	renderer.SetDrawColor(40, 50, 65, 120)
	renderer.FillRect(&sdl.Rect{X: 0, Y: barH - 1, W: screenWidth, H: 1})

	if medFont != nil {
		title := "Notes"
		renderText(renderer, config, medFont, title,
			sdl.Color{R: 220, G: 230, B: 245, A: 240}, 16, 10)
	}

	// Note count.
	count := len(notesFiltered())
	countStr := fmt.Sprintf("%d notes", count)
	cw, _, _ := font.SizeUTF8(countStr)
	renderText(renderer, config, font, countStr,
		sdl.Color{R: 140, G: 155, B: 180, A: 200},
		screenWidth-int32(cw)-16, 14)

	if notes.tab == 0 {
		notesRenderList(renderer, config, font, medFont, barH)
	} else {
		notesRenderEditor(renderer, config, font, medFont, barH)
	}

	// Controls.
	if font != nil {
		controls := ""
		switch notes.tab {
		case 0:
			controls = "D-Pad: Navigate | A: Edit | N: New | X: Delete | S: Search | F: Folder"
		case 1, 2:
			controls = "Type to edit | A: Save & Back | B: Cancel"
		}
		cw2, _, _ := font.SizeUTF8(controls)
		renderText(renderer, config, font, controls,
			sdl.Color{R: 80, G: 90, B: 110, A: 120},
			(screenWidth-int32(cw2))/2, screenHeight-24)
	}
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
	searchText := notes.search
	if searchText == "" {
		searchText = "Search notes..."
		renderText(renderer, config, font, searchText,
			sdl.Color{R: 80, G: 90, B: 110, A: 150}, searchX+30, searchY+6)
	} else {
		renderText(renderer, config, font, searchText,
			sdl.Color{R: 200, G: 210, B: 230, A: 220}, searchX+30, searchY+6)
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

	// Note list.
	listY := barH + 50
	listH := screenHeight - listY - 50
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

	lineH := int32(64)
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
		title := n.Title
		if len(title) > 45 {
			title = title[:45] + "..."
		}
		renderText(renderer, config, medFont, title,
			sdl.Color{R: 220, G: 230, B: 245, A: 240}, listX+16, ry+8)

		// Preview.
		preview := strings.ReplaceAll(n.Content, "\n", " ")
		if len(preview) > 60 {
			preview = preview[:60] + "..."
		}
		renderText(renderer, config, font, preview,
			sdl.Color{R: 120, G: 135, B: 160, A: 180}, listX+16, ry+28)

		// Metadata.
		folderStr := n.Folder
		timeStr := n.UpdatedAt.Format("Jan 2 15:04")
		meta := fmt.Sprintf("%s  •  %s", folderStr, timeStr)
		renderText(renderer, config, font, meta,
			sdl.Color{R: 80, G: 95, B: 120, A: 140}, listX+16, ry+46)
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
		lineH := int32(20)
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
		cursorY += int32(len(cursorLines)-1) * 20
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
			notes.tab = 0
			PlayBackSound()
		case sdl.K_BACKSPACE:
			if len(notes.editTitle) > 0 {
				notes.editTitle = notes.editTitle[:len(notes.editTitle)-1]
			}
		}
	case 2: // Edit content
		switch e.Keysym.Sym {
		case sdl.K_ESCAPE, sdl.K_b:
			notesSaveCurrentEdit()
			notes.tab = 0
			PlayBackSound()
		case sdl.K_BACKSPACE:
			if len(notes.editContent) > 0 {
				notes.editContent = notes.editContent[:len(notes.editContent)-1]
			}
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
			if len(notes.search) > 0 {
				notes.search = notes.search[:len(notes.search)-1]
			}
		}
	}
}

func handleNotesTextInput(e *sdl.TextInputEvent) {
	for _, r := range string(e.Text[:]) {
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
