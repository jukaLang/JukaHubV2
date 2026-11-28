package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// ──────────────────────────────────────────────────────────────────────
// Notes App — markdown editor with folders and search
//
// A note is typed straight into the editor with a keyboard, or with the
// on-screen keyboard on a handheld (notes_keyboard.go: A on the title or the
// body). Long lines wrap; the caret shows where the next letter goes and the
// body scrolls to keep it above the keyboard. A note can be saved as a text
// file (notes_files.go), and a phone can read, write, download and add notes
// over Wi-Fi (companion_notes.go, the Phone Companion's /notes page).
//
// The store is shared with the phone's requests, so notesMu guards it: every
// read and change of notes.store takes it, and so does writing notes.json. The
// editor's buffer (editTitle, editContent) is the main thread's alone. Each
// note's Rev counts its saves, so an edit made on the phone and one made here
// never silently overwrite each other: notesSaveCurrentEdit keeps both.
// ──────────────────────────────────────────────────────────────────────

const (
	// notesTitleMax and notesBodyMax bound a note's title and body, in bytes;
	// notesSearchMax the search field.
	notesTitleMax  = 80
	notesBodyMax   = 16000
	notesSearchMax = 60
	// notesLineH is a body line's height: the body font's line box is 30px on
	// this canvas, so 20px spacing used to print every line through the next.
	notesLineH = int32(30)
)

type noteEntry struct {
	ID        int       `json:"id"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	Folder    string    `json:"folder"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Rev counts the note's saves (notesSaveCurrentEdit, companion_notes.go).
	Rev int `json:"rev,omitempty"`
	// File is the name its last "Save as file" wrote (notes_files.go), so the
	// next save of the same note replaces that file.
	File string `json:"file,omitempty"`
}

type noteStore struct {
	Notes   []noteEntry `json:"notes"`
	NextID  int         `json:"next_id"`
	Folders []string    `json:"folders"`
}

type notesState struct {
	store       noteStore
	cursor      int
	tab         int // 0=list, 1=edit-title, 2=edit-content, 3=search
	editTitle   string
	editContent string
	editID      int // -1=new
	// editRev and editOrig* are the note as it was opened, or last saved,
	// here: what tells an edit made here from one made on the phone.
	editRev         int
	editOrigTitle   string
	editOrigContent string
	search          string
	filter          string // folder filter
	scrollY         int
	storePath       string
	skipText        string // a shortcut key's own text, dropped once (N/Y new, S search)
	// lastLen and lastCaret are the body and caret the editor last drew: a
	// change means typing, and the body scrolls to keep the caret in view.
	lastLen, lastCaret int
	body               notesBodyLayout
}

var (
	notes notesState
	// notesMu guards notes.store and the writes of notes.json.
	notesMu sync.Mutex
	// notesOpenNow is the note open in the editor (0: none), for the phone
	// page's "open on the handheld" mark. Any goroutine.
	notesOpenNow atomic.Int64
)

// notesDataDir is the folder the notes store travels in: a normal Save Sync
// root (saveRootsFor), separate from the files a note is exported to.
var notesDataDirOverride string

func notesDataDir() string {
	if notesDataDirOverride != "" {
		return notesDataDirOverride
	}
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return filepath.Join(dir, "notes")
	}
	return filepath.Join("data", "notes")
}

func notesStorePath() string { return filepath.Join(notesDataDir(), "notes.json") }

// notesMigrateLegacy copies the old notes.json beside the app into the sync
// folder once. The old file is left where it is: it is the player's data, and
// a read-only card must not turn a migration into a deletion.
func notesMigrateLegacy() {
	if fileExists(notes.storePath) {
		return
	}
	data, err := os.ReadFile("notes.json")
	if err != nil {
		return
	}
	if err := AtomicWrite(notes.storePath, data, 0o644); err != nil {
		logWarn("notes", "could not move the old notes store into the sync folder: %v", err)
		return
	}
	logInfo("notes", "moved notes.json into the sync folder %s", notesDataDir())
}

func notesInit() {
	notes.storePath = notesStorePath()
	if err := os.MkdirAll(notesDataDir(), 0o755); err != nil {
		logWarn("notes", "could not create the notes sync folder: %v", err)
	}
	notesMigrateLegacy()
	notesLoad()
	notesMu.Lock()
	if len(notes.store.Folders) == 0 {
		notes.store.Folders = []string{"All", "Personal", "Work", "Ideas"}
	}
	notesMu.Unlock()
}

func notesLoad() {
	var store noteStore
	notesMu.Lock()
	defer notesMu.Unlock()
	// A damaged notes.json now falls back to the .bak the atomic save leaves
	// behind instead of silently starting empty, which is how a reader used to
	// conclude their notes had been deleted.
	if _, err := readJSONWithBackup(notes.storePath, &store); err != nil {
		notes.store = noteStore{NextID: 1}
		return
	}
	notes.store = store
}

// Tests call this seam directly; it is kept for their table cases.
// notesSave writes the store, taking notesMu.
func notesSave() {
	notesMu.Lock()
	defer notesMu.Unlock()
	notesSaveLocked()
}

// notesSaveLocked writes the store; the caller holds notesMu.
func notesSaveLocked() {
	if notes.storePath == "" {
		return
	}
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

// notesIndexLocked is note id's index in the store, -1 when it is not there.
// The caller holds notesMu.
func notesIndexLocked(id int) int {
	for i := range notes.store.Notes {
		if notes.store.Notes[i].ID == id {
			return i
		}
	}
	return -1
}

// notesFind is a copy of note id.
func notesFind(id int) (noteEntry, bool) {
	notesMu.Lock()
	defer notesMu.Unlock()
	if i := notesIndexLocked(id); i >= 0 {
		return notes.store.Notes[i], true
	}
	return noteEntry{}, false
}

// notesFolders is a copy of the folder list ("All" first).
func notesFolders() []string {
	notesMu.Lock()
	defer notesMu.Unlock()
	return append([]string(nil), notes.store.Folders...)
}

// notesNoteFolders is the folders a note can be in (every one but "All").
func notesNoteFolders() []string {
	var out []string
	for _, f := range notesFolders() {
		if f != "All" {
			out = append(out, f)
		}
	}
	return out
}

// notesNewFolder is where a new note goes: the folder the list is showing, or
// Personal.
func notesNewFolder() string {
	if notes.filter != "" && notes.filter != "All" {
		return notes.filter
	}
	return "Personal"
}

func notesNewNote() {
	now := time.Now()
	notesMu.Lock()
	notes.store.NextID++
	n := noteEntry{
		ID:        notes.store.NextID,
		Title:     "New Note",
		Content:   "",
		Folder:    notesNewFolder(),
		CreatedAt: now,
		UpdatedAt: now,
		Rev:       1,
	}
	notes.store.Notes = append([]noteEntry{n}, notes.store.Notes...)
	notesSaveLocked()
	notesMu.Unlock()
	notesOpen(n, 1)
	// A pad has no letters to type with: the on-screen keyboard opens on the
	// title, "New Note" selected so the first letter replaces it.
	if !hintsUseKeyboard() {
		notesVKOpen(notesVKTitle)
	}
}

// notesOpen puts note n in the editor, on the title (tab 1) or the body (2).
func notesOpen(n noteEntry, tab int) {
	notes.editID = n.ID
	notes.editTitle, notes.editContent = n.Title, n.Content
	notes.editRev = n.Rev
	notes.editOrigTitle, notes.editOrigContent = n.Title, n.Content
	notes.tab = tab
	notes.scrollY = 0
	notes.lastLen, notes.lastCaret = -1, -1
	notesOpenNow.Store(int64(n.ID))
}

// notesCloseEditor goes back to the list (the caller saved what needed it).
func notesCloseEditor() {
	notes.tab = 0
	notesOpenNow.Store(0)
}

func notesDeleteNote(id int) {
	notesMu.Lock()
	if i := notesIndexLocked(id); i >= 0 {
		notes.store.Notes = append(notes.store.Notes[:i], notes.store.Notes[i+1:]...)
	}
	notesSaveLocked()
	notesMu.Unlock()
}

// notesEditDirty reports changes in the editor that are not saved yet.
func notesEditDirty() bool {
	return notes.editTitle != notes.editOrigTitle || notes.editContent != notes.editOrigContent
}

// notesTrimTitle cuts a title to notesTitleMax bytes on a rune boundary.
func notesTrimTitle(s string) string {
	return vkTruncateUTF8(s, notesTitleMax)
}

// notesSaveCurrentEdit writes the editor's note into the store. When the phone
// changed the note since it was opened here, nothing is lost: with nothing
// typed here the phone's version stands (and fills the editor); with both
// changed, the text typed here goes on the note and the phone's version is
// kept beside it as "<title> (phone copy)". A note deleted on the phone while
// it was open here is saved again, as a new note, if anything was typed.
func notesSaveCurrentEdit() {
	if notes.editID < 0 {
		return
	}
	title, content := notes.editTitle, notes.editContent
	changedHere := notesEditDirty()
	now := time.Now()
	copyTitle, resaved := "", false
	notesMu.Lock()
	i := notesIndexLocked(notes.editID)
	switch {
	case i < 0:
		if !changedHere {
			notesMu.Unlock()
			return
		}
		notes.store.NextID++
		n := noteEntry{ID: notes.store.NextID, Title: title, Content: content, Folder: notesNewFolder(),
			CreatedAt: now, UpdatedAt: now, Rev: 1}
		notes.store.Notes = append([]noteEntry{n}, notes.store.Notes...)
		notes.editID, notes.editRev = n.ID, n.Rev
		resaved = true
		notesSaveLocked()
	default:
		stored := notes.store.Notes[i]
		var theirs noteEntry
		if stored.Rev != notes.editRev {
			if !changedHere {
				// The phone changed it and nothing was typed here.
				notes.editTitle, notes.editContent = stored.Title, stored.Content
				notes.editRev = stored.Rev
				notes.editOrigTitle, notes.editOrigContent = stored.Title, stored.Content
				notesMu.Unlock()
				return
			}
			notes.store.NextID++
			theirs = stored
			theirs.ID = notes.store.NextID
			theirs.Title = notesTrimTitle(stored.Title + " (phone copy)")
			theirs.Rev, theirs.File = 1, ""
			copyTitle = theirs.Title
		}
		if stored.Title == title && stored.Content == content && copyTitle == "" {
			notes.editRev = stored.Rev
			notes.editOrigTitle, notes.editOrigContent = title, content
			notesMu.Unlock()
			return
		}
		n := &notes.store.Notes[i]
		n.Title, n.Content, n.UpdatedAt = title, content, now
		n.Rev = stored.Rev + 1
		notes.editRev = n.Rev
		if copyTitle != "" {
			rest := append([]noteEntry{theirs}, notes.store.Notes[i+1:]...)
			notes.store.Notes = append(notes.store.Notes[:i+1], rest...)
		}
		notesSaveLocked()
	}
	notes.editOrigTitle, notes.editOrigContent = title, content
	notesMu.Unlock()
	notesOpenNow.Store(int64(notes.editID))
	if changedHere {
		trophyEvent(tkNoteSave) // only a save that changed the note
	}
	switch {
	case copyTitle != "":
		ShowToast("Also changed on your phone: that version is kept as “"+copyTitle+"”", ToastKindInfo)
	case resaved:
		ShowToast("This note was deleted on your phone; it is saved again here", ToastKindInfo)
	}
}

// notesSyncOpenNote follows the phone while nothing is typed here: a newer
// version of the open note fills the editor, and a note deleted there closes
// it. Main thread, every frame the editor is drawn.
func notesSyncOpenNote() {
	if (notes.tab != 1 && notes.tab != 2) || notes.editID < 0 || notesEditDirty() {
		return
	}
	n, ok := notesFind(notes.editID)
	switch {
	case !ok:
		if notesVKActive() {
			vkCloseKeyboard()
		}
		notesCloseEditor()
		ShowToast("This note was deleted on your phone", ToastKindInfo)
	case n.Rev != notes.editRev:
		notes.editTitle, notes.editContent = n.Title, n.Content
		notes.editRev = n.Rev
		notes.editOrigTitle, notes.editOrigContent = n.Title, n.Content
	}
}

// notesFlushOnExit saves the note being edited, if any, when the app quits.
func notesFlushOnExit() {
	// Tab 1 edits the title, tab 2 the body.
	if (notes.tab != 1 && notes.tab != 2) || notes.editID < 0 {
		return
	}
	if notesEditDirty() {
		notesSaveCurrentEdit()
	}
}

// notesSetNoteFolder moves the open note to the next (dir 1) or previous
// (dir -1) folder and saves it.
func notesSetNoteFolder(dir int) {
	folders := notesNoteFolders()
	if len(folders) == 0 || notes.editID < 0 {
		return
	}
	notesMu.Lock()
	i := notesIndexLocked(notes.editID)
	if i < 0 {
		notesMu.Unlock()
		return
	}
	n := &notes.store.Notes[i]
	at := -1
	for k, f := range folders {
		if f == n.Folder {
			at = k
		}
	}
	at = ((at+dir)%len(folders) + len(folders)) % len(folders)
	n.Folder = folders[at]
	// A move made here is this editor's own change, not the phone's.
	if n.Rev == notes.editRev {
		notes.editRev++
	}
	n.Rev++
	n.UpdatedAt = time.Now()
	notesSaveLocked()
	notesMu.Unlock()
	PlayNavSound()
}

// notesFolderOf is the open note's folder.
func notesFolderOf(id int) string {
	if n, ok := notesFind(id); ok {
		return n.Folder
	}
	return ""
}

func notesFiltered() []noteEntry {
	notesMu.Lock()
	all := append([]noteEntry(nil), notes.store.Notes...)
	notesMu.Unlock()
	var filtered []noteEntry
	q := strings.ToLower(strings.TrimSpace(notes.search))
	for _, n := range all {
		if notes.filter != "" && notes.filter != "All" && n.Folder != notes.filter {
			continue
		}
		if q != "" {
			if !strings.Contains(strings.ToLower(n.Title), q) &&
				!strings.Contains(strings.ToLower(n.Content), q) {
				continue
			}
		}
		filtered = append(filtered, n)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		return filtered[i].UpdatedAt.After(filtered[j].UpdatedAt)
	})
	return filtered
}

// ──────────────────────────────────────────────────────────────────────
// The body as drawn: wrapped lines and the caret
// ──────────────────────────────────────────────────────────────────────

// The styles of the simple markdown the body shows.
const (
	notesStylePlain uint8 = iota
	notesStyleH1
	notesStyleH2
	notesStyleBullet
	notesStyleQuote
	notesStyleCode
)

// notesCheckboxShown turns a task-list line into its reader form. The glyph
// replacement is three runes wide ("☐  "/"☑  ") exactly like "[ ]"/"[x]", so
// the line's rune offsets still point into the body for the caret.
func notesCheckboxShown(para string) (string, bool) {
	if len(para) < 5 || (para[0] != '-' && para[0] != '*') || para[1] != ' ' {
		return "", false
	}
	rest := para[2:]
	if len(rest) < 3 || rest[0] != '[' || rest[2] != ']' {
		return "", false
	}
	mark := "☐  "
	switch rest[1] {
	case 'x', 'X':
		mark = "☑  "
	case ' ':
	default:
		return "", false
	}
	return "• " + mark + rest[3:], true
}

// notesBodyLine is one line of the body as drawn: where its first character
// is in the body (runes), how many runes it has, its text as shown ("- "
// shown as "• ", the same length) and its paragraph's style.
type notesBodyLine struct {
	start int
	runes int
	text  string
	style uint8
}

// notesBodyLayout is the wrapped body for one text, width and pair of fonts.
type notesBodyLayout struct {
	text  string
	width int32
	font  *ttf.Font
	head  *ttf.Font
	lines []notesBodyLine
}

// notesSeg is a run of a paragraph, in runes: [a, b).
type notesSeg struct{ a, b int }

// notesWrap breaks s into lines no wider than width, after the last space
// that fits, or inside a word longer than the line.
func notesWrap(font *ttf.Font, s string, width int32) []notesSeg {
	r := []rune(s)
	if len(r) == 0 || width <= 0 {
		return []notesSeg{{0, len(r)}}
	}
	fits := func(a, b int) bool {
		w, _ := measureTextRaw(font, string(r[a:b]))
		return w <= width
	}
	var out []notesSeg
	for a := 0; a < len(r); {
		if fits(a, len(r)) {
			out = append(out, notesSeg{a, len(r)})
			break
		}
		lo, hi := a+1, len(r)
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if fits(a, mid) {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		cut := lo
		if lo >= len(r) {
			// One glyph wider than the line: it gets the line to itself.
			cut = len(r)
		} else if r[lo] == ' ' {
			cut = lo + 1
		} else {
			for k := lo; k > a+1; k-- {
				if r[k-1] == ' ' {
					cut = k
					break
				}
			}
		}
		out = append(out, notesSeg{a, cut})
		a = cut
	}
	return out
}

// notesLayoutBody wraps the body, reusing the last layout while nothing it
// depends on changed.
func notesLayoutBody(font, head *ttf.Font, text string, width int32) []notesBodyLine {
	L := &notes.body
	if L.lines != nil && L.text == text && L.width == width && L.font == font && L.head == head {
		return L.lines
	}
	lines := make([]notesBodyLine, 0, 32)
	start := 0
	inCode := false
	for _, para := range strings.Split(text, "\n") {
		style := notesStylePlain
		switch {
		case strings.HasPrefix(para, "```"):
			style = notesStyleCode
			inCode = !inCode
		case inCode:
			style = notesStyleCode
		case strings.HasPrefix(para, "# "):
			style = notesStyleH1
		case strings.HasPrefix(para, "## "):
			style = notesStyleH2
		case strings.HasPrefix(para, "- "), strings.HasPrefix(para, "* "):
			style = notesStyleBullet
		case strings.HasPrefix(para, "> "):
			style = notesStyleQuote
		}
		shown := para
		if box, ok := notesCheckboxShown(para); ok {
			style, shown = notesStyleBullet, box
		} else if style == notesStyleBullet {
			shown = "• " + para[2:]
		}
		f := font
		if style == notesStyleH1 && head != nil {
			f = head
		}
		r := []rune(shown)
		for _, seg := range notesWrap(f, shown, width) {
			lines = append(lines, notesBodyLine{start: start + seg.a, runes: seg.b - seg.a, text: string(r[seg.a:seg.b]), style: style})
		}
		start += utf8.RuneCountInString(para) + 1
	}
	*L = notesBodyLayout{text: text, width: width, font: font, head: head, lines: lines}
	return lines
}

// notesCaretLine is the line the caret (a rune index into the body) is on:
// at a wrap the caret shows at the next line's start, at a paragraph's end at
// the end of its last line.
func notesCaretLine(lines []notesBodyLine, caret int) int {
	for i, l := range lines {
		end := l.start + l.runes
		if caret < end {
			return i
		}
		if caret == end && (i+1 == len(lines) || lines[i+1].start > end) {
			return i
		}
	}
	return len(lines) - 1
}

// notesBodyCaret is the caret's rune index in the body: the on-screen
// keyboard's when it is typing there, else the end (a keyboard types at the
// end). -1 when no caret is shown.
func notesBodyCaret() int {
	switch {
	case notesVKActive() && notesVK.field == notesVKBody:
		return vkCaretIndex(notes.editContent)
	case notes.tab == 2 && !virtualKeyboardActive:
		return utf8.RuneCountInString(notes.editContent)
	}
	return -1
}

// ──────────────────────────────────────────────────────────────────────
// Rendering
// ──────────────────────────────────────────────────────────────────────

func renderNotes(renderer *sdl.Renderer, config *Config) {
	// Home's backdrop, the one every screen stands on (ui_glass.go).
	drawAppBackdrop(renderer, config, screenWidth, screenHeight)

	font, _ := getCachedFont(config, "small")
	medFont, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}

	// The app's one bar owns the top of the screen; the body starts under it.
	barH := headerBarHeight()

	if notes.tab == 0 || notes.tab == 3 {
		notesRenderList(renderer, config, font, medFont, barH)
	} else {
		notesSyncOpenNote()
		notesRenderEditor(renderer, config, font, medFont, barH)
	}

	// Note count. It used to sit in the bespoke header; the shared bar's
	// caption already names the scene, so the count rides the search row it
	// describes instead of competing with the wordmark.
	if notes.tab == 0 || notes.tab == 3 {
		count := len(notesFiltered())
		countStr := fmt.Sprintf("%d notes", count)
		if count == 1 {
			countStr = "1 note"
		}
		cw, _, _ := font.SizeUTF8(countStr)
		renderText(renderer, config, font, countStr,
			WithAlpha(ColorTextSecondary(), 200),
			screenWidth-int32(cw)-16, barH+16)
	}

	// The shared bars last: the header, its Back pill and the footer badges are
	// the same chrome every other screen draws.
	renderStatusBar(renderer, config)
	renderFooter(renderer, config)
	// The menus and the delete confirmation (notes_files.go).
	tbxRenderSheet(renderer, config)
}

func notesRenderList(renderer *sdl.Renderer, config *Config, font, medFont *ttf.Font, barH int32) {
	// Search bar.
	searchW := int32(400)
	searchH := int32(32)
	searchX := int32(16)
	searchY := barH + 8
	fillRoundedRect(renderer, searchX, searchY, searchW, searchH, 8,
		glassA(10))
	strokeRoundedRect(renderer, searchX, searchY, searchW, searchH, 8, 1, ColorBorder)
	if notes.tab == 3 {
		// The search is being typed: its field carries the focus ring.
		strokeRoundedRect(renderer, searchX, searchY, searchW, searchH, 8, 2, accentColor)
	}
	searchIcon := "[S]"
	renderText(renderer, config, font, searchIcon,
		WithAlpha(ColorTextTertiary(), 180), searchX+8, searchY+6)
	// The text starts past the icon's real advance; a fixed +30 ran
	// "Search notes..." through the "[S]" badge.
	iconW, _ := measureText(font, searchIcon)
	textX := searchX + 8 + iconW + 8
	searchText := notes.search
	if searchText == "" {
		searchText = "Search notes..."
		renderText(renderer, config, font, searchText,
			WithAlpha(ColorTextTertiary(), 150), textX, searchY+6)
	} else {
		renderTextRaw(renderer, config, font, searchText,
			WithAlpha(ColorTextPrimary(), 220), textX, searchY+6)
	}
	if notes.tab == 3 && !virtualKeyboardActive && time.Now().UnixMilli()%1000 < 500 {
		cx := textX
		if notes.search != "" {
			w, _ := measureTextRaw(font, notes.search)
			cx += w + 1
		}
		renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 200)
		renderer.FillRect(&sdl.Rect{X: cx, Y: searchY + 7, W: 2, H: searchH - 14})
	}

	// Folder tabs.
	folderX := searchX + searchW + 16
	for _, folder := range notesFolders() {
		fw, _, _ := font.SizeUTF8(folder)
		fw += 20
		fY := searchY
		fH := searchH
		active := notes.filter == folder || (notes.filter == "" && folder == "All")
		fill := glassChipFill()
		if active {
			fill = sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 60}
		}
		fillRoundedRect(renderer, folderX, fY, int32(fw), fH, 8, fill)
		col := WithAlpha(ColorTextSecondary(), 200)
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
	if notes.cursor >= len(filtered) {
		notes.cursor = len(filtered) - 1
	}
	if notes.cursor < 0 {
		notes.cursor = 0
	}
	if len(filtered) == 0 {
		if medFont != nil {
			empty := "No notes yet"
			if notes.search != "" {
				empty = "No notes match"
			}
			ew, _, _ := medFont.SizeUTF8(empty)
			renderText(renderer, config, medFont, empty,
				WithAlpha(ColorTextTertiary(), 150),
				(screenWidth-int32(ew))/2, listY+40)
			hint := hintSentence("Press %s to write a new note", "Y", "New")
			hw, _, _ := font.SizeUTF8(hint)
			renderText(renderer, config, font, hint,
				WithAlpha(ColorTextTertiary(), 120),
				(screenWidth-int32(hw))/2, listY+70)
		}
		return
	}

	// The row stacks three lines. "small" and "medium" are both 24px here
	// (30px line box), so the preview and the metadata use a 16px face: three
	// 30px lines would need a 96px row. At 80 the title clears the preview and
	// the metadata instead of printing through them, as the old 64px row did.
	lineH := int32(80)
	subFont := FontForRole(config, TypeLabel)
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
	openID := int(notesOpenNow.Load())

	for i := start; i < end; i++ {
		n := filtered[i]
		ry := listY + 8 + int32(i-start)*lineH
		focused := i == notes.cursor

		if focused {
			// The cursor row is the shared selected list row.
			UIListRow(renderer, sdl.Rect{X: listX + 4, Y: ry, W: listW - 8, H: lineH - 4}, 10, true, false)
		}

		// Title.
		title := truncateRunesSuffix(n.Title, 45, "...")
		renderTextRaw(renderer, config, medFont, title,
			WithAlpha(ColorTextPrimary(), 240), listX+16, ry+2)

		// Preview.
		preview := truncateRunesSuffix(strings.ReplaceAll(n.Content, "\n", " "), 60, "...")
		renderTextRaw(renderer, config, subFont, preview,
			WithAlpha(ColorTextTertiary(), 180), listX+16, ry+36)

		// Metadata.
		folderStr := n.Folder
		timeStr := formatDateTimeNear(config, n.UpdatedAt, time.Now())
		meta := fmt.Sprintf("%s  •  %s", folderStr, timeStr)
		if n.File != "" {
			meta += "  •  saved as a file"
		}
		if n.ID == openID && notes.tab != 0 {
			meta += "  •  open"
		}
		renderText(renderer, config, subFont, meta,
			WithAlpha(ColorTextTertiary(), 140), listX+16, ry+58)
	}

	// Scrollbar.
	if len(filtered) > maxVisible {
		thumbFrac := float64(maxVisible) / float64(len(filtered))
		scrollFrac := float64(start) / float64(len(filtered)-maxVisible)
		drawScrollbar(renderer, listX+listW-6, listY+8, 3, listH-16, thumbFrac, scrollFrac)
	}
}

func notesRenderEditor(renderer *sdl.Renderer, config *Config, font, medFont *ttf.Font, barH int32) {
	if medFont == nil {
		medFont = font
	}
	// The on-screen keyboard's top, when it is up on this note: the body
	// stays readable above it.
	kbTop := screenHeight
	if notesVKActive() && vkPanelRect.H > 0 {
		kbTop = vkPanelRect.Y
	}
	bodyByKeyboard := notesVKActive() && notesVK.field == notesVKBody

	contentX := int32(32)
	contentW := screenWidth - 64
	var contentY, contentH int32

	if bodyByKeyboard {
		// Typing the body: the title and folders step aside, the body takes
		// everything between the bar and the keyboard.
		contentY = barH + 8
		contentH = kbTop - contentY - 8
	} else {
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
			renderText(renderer, config, medFont, "Note title...",
				WithAlpha(ColorTextTertiary(), 150), titleX+12, titleY+8)
		} else {
			if notesVKActive() && notesVK.field == notesVKTitle && vkTextSelected {
				w, _ := measureTextRaw(medFont, titleText)
				fillRoundedRect(renderer, titleX+10, titleY+6, w+4, titleH-12, 4, WithAlpha(accentColor, 90))
			}
			renderTextRaw(renderer, config, medFont, titleText,
				ColorTextPrimary(), titleX+12, titleY+8)
		}
		// The title's caret: the on-screen keyboard's, or the end for a keyboard.
		if notes.tab == 1 && time.Now().UnixMilli()%1000 < 500 {
			caret := utf8.RuneCountInString(notes.editTitle)
			if notesVKActive() && notesVK.field == notesVKTitle {
				caret = vkCaretIndex(notes.editTitle)
			}
			w, _ := measureTextRaw(medFont, string([]rune(notes.editTitle)[:caret]))
			renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 200)
			renderer.FillRect(&sdl.Rect{X: titleX + 12 + w, Y: titleY + 9, W: 2, H: titleH - 18})
		}

		// Folder selector: the note's folder lit (L1/R1 move it).
		folderY := titleY + titleH + 8
		folderX := int32(32)
		current := notesFolderOf(notes.editID)
		for _, folder := range notesNoteFolders() {
			fw, _, _ := font.SizeUTF8(folder)
			fw += 16
			active := folder == current
			fill := glassChipFill()
			if active {
				fill = sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 60}
			}
			fillRoundedRect(renderer, folderX, folderY, int32(fw), 24, 8, fill)
			col := WithAlpha(ColorTextSecondary(), 200)
			if active {
				col = sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 220}
			}
			renderText(renderer, config, font, folder, col, folderX+8, folderY+4)
			folderX += int32(fw) + 8
		}
		contentY = folderY + 36
		contentH = screenHeight - contentY - 60
		if kbTop < screenHeight {
			contentH = min(contentH, kbTop-contentY-8)
		}
	}
	if contentH < notesLineH+32 {
		contentH = notesLineH + 32
	}

	drawCard(renderer, contentX, contentY, contentW, contentH, 16)
	if notes.tab == 2 {
		strokeRoundedRect(renderer, contentX, contentY, contentW, contentH, 16, 2, accentColor)
	}
	notesRenderBody(renderer, config, font, medFont, sdl.Rect{X: contentX, Y: contentY, W: contentW, H: contentH})
}

// notesRenderBody draws the wrapped body inside area, scrolled so the caret
// stays in view while it moves, with the caret and a scrollbar.
func notesRenderBody(renderer *sdl.Renderer, config *Config, font, head *ttf.Font, area sdl.Rect) {
	padX, padY := int32(16), int32(16)
	if notes.editContent == "" {
		renderText(renderer, config, font, "Start typing your note...",
			WithAlpha(ColorTextTertiary(), 150), area.X+padX, area.Y+padY)
		if notes.tab == 2 && time.Now().UnixMilli()%1000 < 500 {
			renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 200)
			renderer.FillRect(&sdl.Rect{X: area.X + padX, Y: area.Y + padY + 4, W: 2, H: notesLineH - 8})
		}
		notes.scrollY = 0
		return
	}
	lines := notesLayoutBody(font, head, notes.editContent, area.W-2*padX)
	viewH := area.H - 2*padY
	totalH := int32(len(lines)) * notesLineH
	caret := notesBodyCaret()
	ci := -1
	if caret >= 0 {
		ci = notesCaretLine(lines, caret)
	}
	// Typing (or the caret moving) keeps the caret in view.
	if caret >= 0 && (len(notes.editContent) != notes.lastLen || caret != notes.lastCaret) {
		cy := int32(ci) * notesLineH
		if cy < int32(notes.scrollY) {
			notes.scrollY = int(cy)
		}
		if cy+notesLineH > int32(notes.scrollY)+viewH {
			notes.scrollY = int(cy + notesLineH - viewH)
		}
	}
	notes.lastLen, notes.lastCaret = len(notes.editContent), caret
	maxScroll := max(0, int(totalH-viewH))
	notes.scrollY = max(0, min(notes.scrollY, maxScroll))

	clip := sdl.Rect{X: area.X + 4, Y: area.Y + 4, W: area.W - 8, H: area.H - 8}
	renderer.SetClipRect(&clip)
	first := notes.scrollY / int(notesLineH)
	for i := first; i < len(lines); i++ {
		ly := area.Y + padY + int32(i)*notesLineH - int32(notes.scrollY)
		if ly > area.Y+area.H {
			break
		}
		l := lines[i]
		f := font
		col := WithAlpha(ColorTextSecondary(), 220)
		switch l.style {
		case notesStyleH1:
			f, col = head, ColorTextPrimary()
		case notesStyleH2:
			col = sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 220}
		case notesStyleQuote:
			col = sdl.Color{R: 120, G: 160, B: 200, A: 180}
		case notesStyleCode:
			col = sdl.Color{R: 100, G: 200, B: 140, A: 200}
		}
		if l.text != "" {
			renderTextRaw(renderer, config, f, l.text, col, area.X+padX, ly)
		}
		if i == ci && time.Now().UnixMilli()%1000 < 500 {
			w, _ := measureTextRaw(f, string([]rune(l.text)[:max(0, min(caret-l.start, l.runes))]))
			renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 200)
			renderer.FillRect(&sdl.Rect{X: area.X + padX + w, Y: ly + 4, W: 2, H: notesLineH - 8})
		}
	}
	renderer.SetClipRect(nil)
	if totalH > viewH {
		thumbFrac := float64(viewH) / float64(totalH)
		scrollFrac := float64(notes.scrollY) / float64(maxScroll)
		drawScrollbar(renderer, area.X+area.W-8, area.Y+8, 3, area.H-16, thumbFrac, scrollFrac)
	}
}

// notesScrollBy moves the body by lines (the pad's Up/Down while reading).
func notesScrollBy(lines int) {
	notes.scrollY += lines * int(notesLineH)
	if notes.scrollY < 0 {
		notes.scrollY = 0
	}
	// The upper bound is applied when the body is next drawn.
}

// ──────────────────────────────────────────────────────────────────────
// Input
// ──────────────────────────────────────────────────────────────────────

func handleNotesInput(e *sdl.KeyboardEvent, config *Config) {
	if e == nil || e.Type != sdl.KEYDOWN {
		return
	}
	if tbxSheetVisible(config) {
		tbxDispatch(config, tbxKeyAction(e), nil)
		return
	}
	notes.skipText = ""
	ctrl := e.Keysym.Mod&sdl.KMOD_CTRL != 0

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
				notesOpen(filtered[notes.cursor], 2)
				PlayActivateSound()
			}
		case sdl.K_n, sdl.K_y:
			// Y as well as N: the footer's "Y New" is the pad's key, and a
			// keyboard's Y is its twin (inputhints.go). The letter's text event
			// follows and would land in the new title, so it is dropped.
			notes.skipText = string(rune(e.Keysym.Sym))
			notesNewNote()
			PlayActivateSound()
		case sdl.K_x, sdl.K_DELETE:
			if notes.cursor >= 0 && notes.cursor < len(filtered) {
				notesConfirmDelete(config, filtered[notes.cursor])
			}
		case sdl.K_s:
			if ctrl {
				// Ctrl+S on the list: every note as a file.
				notesExportAllStart()
				return
			}
			notes.search = ""
			notes.tab = 3        // search mode
			notes.skipText = "s" // the S's own text must not start the query
			PlayActivateSound()
		case sdl.K_m, sdl.K_APPLICATION, sdl.K_MENU:
			notesOpenListMenu(config)
		case sdl.K_f:
			// Cycle folder filter.
			folders := notesFolders()
			if len(folders) == 0 {
				return
			}
			idx := 0
			for i, f := range folders {
				if f == notes.filter {
					idx = i
					break
				}
			}
			idx = (idx + 1) % len(folders)
			notes.filter = folders[idx]
			notes.cursor = 0
			PlayNavSound()
		case sdl.K_ESCAPE, sdl.K_b:
			goBackScene(config)
			PlayBackSound()
		}
	case 1, 2:
		// The note's menu: Ctrl+M or the Menu key (a letter would be typed).
		if (e.Keysym.Sym == sdl.K_m && ctrl) || e.Keysym.Sym == sdl.K_MENU || e.Keysym.Sym == sdl.K_APPLICATION {
			notesOpenNoteMenu(config)
			return
		}
		notesEditorKey(e, ctrl)
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

// notesEditorKey is a keyboard's key in the editor (tab 1 title, 2 body);
// letters arrive as text (handleNotesTextInput).
func notesEditorKey(e *sdl.KeyboardEvent, ctrl bool) {
	switch notes.tab {
	case 1: // Edit title
		switch {
		case e.Keysym.Sym == sdl.K_s && ctrl:
			notesSaveCurrentEdit()
			ShowToast("Saved", ToastKindSuccess)
		case e.Keysym.Sym == sdl.K_RETURN, e.Keysym.Sym == sdl.K_TAB, e.Keysym.Sym == sdl.K_DOWN:
			notes.tab = 2
			PlayActivateSound()
		case e.Keysym.Sym == sdl.K_ESCAPE:
			// The footer promises "Save & Back" in both edit tabs; leaving the
			// title unsaved because Escape was pressed here contradicted it. B is
			// deliberately not a shortcut in this tab: it is a letter here.
			notesSaveCurrentEdit()
			notesCloseEditor()
			PlayBackSound()
		case e.Keysym.Sym == sdl.K_BACKSPACE:
			notes.editTitle = trimLastRune(notes.editTitle)
		}
	case 2: // Edit content
		switch {
		case e.Keysym.Sym == sdl.K_s && ctrl:
			notesSaveCurrentEdit()
			ShowToast("Saved", ToastKindSuccess)
		case e.Keysym.Sym == sdl.K_ESCAPE:
			notesSaveCurrentEdit()
			notesCloseEditor()
			PlayBackSound()
		case e.Keysym.Sym == sdl.K_BACKSPACE:
			notes.editContent = trimLastRune(notes.editContent)
		case e.Keysym.Sym == sdl.K_RETURN:
			if len(notes.editContent) < notesBodyMax {
				notes.editContent += "\n"
			}
		case e.Keysym.Sym == sdl.K_UP && ctrl:
			notes.tab = 1
		case e.Keysym.Sym == sdl.K_PAGEUP:
			notesScrollBy(-4)
		case e.Keysym.Sym == sdl.K_PAGEDOWN:
			notesScrollBy(4)
		}
	}
}

func handleNotesTextInput(e *sdl.TextInputEvent) {
	if tbxSheetVisible(appConfig) {
		// A menu or a question is up: its keys are not letters for the note.
		return
	}
	text := e.GetText()
	if skip := notes.skipText; skip != "" {
		notes.skipText = ""
		if strings.EqualFold(text, skip) {
			return
		}
	}
	notesInsertText(text)
}

// notesInsertText appends text to whichever field the editor has open. Clipboard
// paste (Ctrl+V) shares this path, so pasted text lands in the open field under
// the same limits a keystroke obeys and never sneaks into the other fields.
func notesInsertText(text string) {
	for _, r := range text {
		switch notes.tab {
		case 1:
			if r == '\n' || r == '\r' || r == '\t' {
				r = ' '
			}
			if len(notes.editTitle)+utf8.RuneLen(r) <= notesTitleMax {
				notes.editTitle += string(r)
			}
		case 2:
			if r == '\r' {
				continue
			}
			if len(notes.editContent)+utf8.RuneLen(r) <= notesBodyMax {
				notes.editContent += string(r)
			}
		case 3:
			if r == '\n' || r == '\r' || r == '\t' {
				continue
			}
			if len(notes.search)+utf8.RuneLen(r) <= notesSearchMax {
				notes.search += string(r)
			}
		}
	}
}

// Scene controller mapping, moved here from controller.go so the scene owns
// its own input.

func ctrlNotes(e *sdl.ControllerButtonEvent) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	if tbxSheetVisible(appConfig) {
		tbxDispatch(appConfig, tbxPadAction(e), nil)
		return true
	}
	switch notes.tab {
	case 1, 2:
		return notesEditorPad(e)
	case 3:
		return notesSearchPad(e)
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
			// The body first: it is what a note is opened for. Up reaches the
			// title.
			notesOpen(filtered[notes.cursor], 2)
			PlayActivateSound()
			return true
		}
	case sdl.CONTROLLER_BUTTON_B:
		// The list is the top level of the scene, so B leaves Notes instead of
		// silently staying on the list (the old handler looked like a no-op).
		goBackScene(appConfig)
	case sdl.CONTROLLER_BUTTON_BACK:
		// Select is the menu here, as in Mail and the Browser: search, every
		// note as a file, the phone page.
		notesOpenListMenu(appConfig)
	case sdl.CONTROLLER_BUTTON_Y:
		// Y is the footer's "New": the same fresh note N makes from a keyboard,
		// with the on-screen keyboard up on its title.
		notesNewNote()
		PlayActivateSound()
	case sdl.CONTROLLER_BUTTON_X:
		filtered := notesFiltered()
		if notes.cursor >= 0 && notes.cursor < len(filtered) {
			notesConfirmDelete(appConfig, filtered[notes.cursor])
		}
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER, sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		// Cycle the folder filter.
		folders := notesFolders()
		if len(folders) > 0 {
			idx := 0
			for i, f := range folders {
				if f == notes.filter {
					idx = i
					break
				}
			}
			if e.Button == sdl.CONTROLLER_BUTTON_LEFTSHOULDER {
				idx--
			} else {
				idx++
			}
			idx = (idx%len(folders) + len(folders)) % len(folders)
			notes.filter = folders[idx]
			notes.cursor = 0
		}
		PlayNavSound()
	default:
		return false
	}
	return true
}

// notesEditorPad is the pad in the editor: Up/Down move between the title and
// the body (and scroll a long body), A types into the one in focus with the
// on-screen keyboard, L1/R1 move the note to another folder, Y and Select
// open the note's menu, and B saves and goes back to the list.
func notesEditorPad(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_B:
		// B saves what was typed and returns to the list instead of falling
		// through to the scene's own back and leaving Notes with the edit
		// unsaved.
		notesSaveCurrentEdit()
		notesCloseEditor()
		PlayBackSound()
	case sdl.CONTROLLER_BUTTON_A:
		if notes.tab == 1 {
			notesVKOpen(notesVKTitle)
		} else {
			notesVKOpen(notesVKBody)
		}
		PlayActivateSound()
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		switch {
		case notes.tab == 2 && notes.scrollY > 0:
			notesScrollBy(-1)
		case notes.tab == 2:
			notes.tab = 1
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		if notes.tab == 1 {
			notes.tab = 2
		} else {
			notesScrollBy(1)
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		notesSetNoteFolder(-1)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		notesSetNoteFolder(1)
	case sdl.CONTROLLER_BUTTON_Y, sdl.CONTROLLER_BUTTON_BACK:
		notesOpenNoteMenu(appConfig)
	case sdl.CONTROLLER_BUTTON_X:
		// Nothing: deleting is in the menu, behind a question.
	default:
		return false
	}
	return true
}

// notesSearchPad is the pad while the search field has the focus: A types
// into it, B (or Select) goes back to the list with the matches.
func notesSearchPad(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_A:
		notesVKOpen(notesVKSearch)
		PlayActivateSound()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		notes.tab = 0
		notes.cursor = 0
		PlayBackSound()
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
