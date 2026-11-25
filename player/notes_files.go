package main

// notes_files.go — Notes' files, menus and the way to the phone (Juka,
// 2026-11-25: "make sure you can save notes as a file and sync to telephone if
// needed").
//
// Save as file writes a note as plain UTF-8 text, <Notes>/<its folder>/<its
// title>.txt, where a PC reading the SD card, the FTP server and the phone's
// Files card find it: the card's Notes folder on a handheld, the shared
// folder beside JukaHub.exe on a PC (ftpRootDir). Saving the same note again
// replaces its own file (noteEntry.File) — renamed with the note when its
// title or folder changed — and never another note's file or one the user put
// there: two notes called the same get "Title (2).txt". "Every note as a file"
// writes them all. The writing runs off the main thread; the SD card can take
// a moment.
//
// The phone side is companion_notes.go: Open on phone shows a QR code that
// opens the Phone Companion's Notes page (at the open note, from the editor).
//
// The menus and the delete question are the toolbox's sheets (tbxui.go),
// drawn and fed by notes.go: Select on the list, Y or Select in the editor.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/skip2/go-qrcode"
)

// notesFilesDirName is the folder notes are saved into, at the card's root.
const notesFilesDirName = "Notes"

// notesFilesDirOverride redirects the folder (tests).
var notesFilesDirOverride string

func notesFilesDir() string {
	if notesFilesDirOverride != "" {
		return notesFilesDirOverride
	}
	return filepath.Join(ftpRootDir(), notesFilesDirName)
}

// notesShownPath is where a saved file is, as a person looks for it: "SD
// card/Notes/…" on a handheld, the full path on a PC.
func notesShownPath(rel string) string {
	if P().IsHandheld() {
		return filepath.ToSlash(filepath.Join("SD card", notesFilesDirName, rel))
	}
	return filepath.Join(notesFilesDir(), filepath.FromSlash(rel))
}

// notesWindowsReserved are names Windows and FAT refuse for a file.
var notesWindowsReserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// notesFileName is a file or folder name for a title: characters FAT, exFAT
// and Windows refuse become "-", the length is cut to 60 bytes, a name of
// only dots or spaces becomes "Note", and a reserved name gets "_". Pure.
func notesFileName(title string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(title) {
		switch {
		case r < 0x20 || r == 0x7f:
		case strings.ContainsRune(`<>:"/\|?*`, r):
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}
	name := strings.TrimRight(vkTruncateUTF8(strings.TrimSpace(b.String()), 60), ". ")
	name = strings.TrimLeft(name, " ")
	if name == "" || strings.Trim(name, ".") == "" {
		name = "Note"
	}
	if notesWindowsReserved[strings.ToUpper(name)] {
		name += "_"
	}
	return name
}

// notesFolderOnDisk is a notes folder's folder name.
func notesFolderOnDisk(folder string) string {
	if folder == "" || folder == "All" {
		folder = "Personal"
	}
	return notesFileName(folder)
}

// notesFileText is what a note's file holds: the body with a final line
// break, or the title when the body is empty.
func notesFileText(n noteEntry) []byte {
	text := strings.ReplaceAll(n.Content, "\r\n", "\n")
	if strings.TrimSpace(text) == "" {
		text = n.Title
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return []byte(text)
}

// notesExportOne writes n under root and returns its path relative to root
// (slash-separated). owned maps the lowercased paths other notes saved to the
// notes that own them; their files, and files nobody saved, are not written
// over.
func notesExportOne(root string, n noteEntry, owned map[string]int) (string, error) {
	dir := notesFolderOnDisk(n.Folder)
	base := notesFileName(n.Title)
	if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
		return "", err
	}
	own := strings.ToLower(n.File)
	rel := ""
	for k := 1; k < 100 && rel == ""; k++ {
		name := base
		if k > 1 {
			name = fmt.Sprintf("%s (%d)", base, k)
		}
		cand := dir + "/" + name + ".txt"
		key := strings.ToLower(cand)
		if owner, ok := owned[key]; ok && owner != n.ID {
			continue
		}
		if key != own {
			if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(cand))); err == nil {
				continue
			}
		}
		rel = cand
	}
	if rel == "" {
		return "", errors.New("too many files called " + base)
	}
	if err := AtomicWrite(filepath.Join(root, filepath.FromSlash(rel)), notesFileText(n), 0o644); err != nil {
		return "", err
	}
	if n.File != "" && !strings.EqualFold(n.File, rel) {
		// The note was renamed or moved: its old file goes.
		_ = os.Remove(filepath.Join(root, filepath.FromSlash(n.File)))
	}
	owned[strings.ToLower(rel)] = n.ID
	return rel, nil
}

// notesExporting is set while files are being written. Any goroutine.
var notesExporting atomic.Bool

// notesExportStart writes the notes ids names (nil: every note) on a
// goroutine, records each note's file and says what happened. Main thread.
func notesExportStart(ids []int) {
	if !notesExporting.CompareAndSwap(false, true) {
		ShowToast("Already saving notes as files", ToastKindInfo)
		return
	}
	want := map[int]bool{}
	for _, id := range ids {
		want[id] = true
	}
	notesMu.Lock()
	var list []noteEntry
	owned := map[string]int{}
	for _, n := range notes.store.Notes {
		if n.File != "" {
			owned[strings.ToLower(n.File)] = n.ID
		}
		if ids == nil || want[n.ID] {
			list = append(list, n)
		}
	}
	notesMu.Unlock()
	if len(list) == 0 {
		notesExporting.Store(false)
		ShowToast("No notes to save", ToastKindInfo)
		return
	}
	root := notesFilesDir()
	PlayActivateSound()
	go func() {
		written := map[int]string{}
		var firstErr error
		for _, n := range list {
			rel, err := notesExportOne(root, n, owned)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			written[n.ID] = rel
		}
		tbxPost(func() {
			notesExporting.Store(false)
			notesExportFinished(list, written, firstErr)
		})
	}()
}

// notesExportFinished records the files written and says so. Main thread.
func notesExportFinished(list []noteEntry, written map[int]string, err error) {
	if len(written) > 0 {
		notesMu.Lock()
		for id, rel := range written {
			if i := notesIndexLocked(id); i >= 0 {
				notes.store.Notes[i].File = rel
			}
		}
		notesSaveLocked()
		notesMu.Unlock()
	}
	switch {
	case len(written) == 0 && err != nil:
		logWarn("notes", "could not save notes as files: %v", err)
		PlayErrorSound()
		ShowToast("Could not save the note as a file: "+err.Error(), ToastKindWarning)
	case len(list) == 1:
		for _, rel := range written {
			logInfo("notes", "note saved as %s", rel)
			PlaySuccessSound()
			ShowToast("Saved as "+notesShownPath(rel), ToastKindSuccess)
		}
	default:
		msg := fmt.Sprintf("Saved %d notes as files in %s", len(written), notesShownPath(""))
		if err != nil {
			msg += fmt.Sprintf(" · %d could not be saved: %v", len(list)-len(written), err)
		}
		logInfo("notes", "%s", msg)
		PlaySuccessSound()
		ShowToast(msg, ToastKindSuccess)
	}
}

// notesExportAllStart saves every note as a file.
func notesExportAllStart() { notesExportStart(nil) }

// notesExportCurrent saves the open note, with what was just typed, as a file.
func notesExportCurrent() {
	if notes.editID < 0 {
		return
	}
	notesSaveCurrentEdit()
	notesExportStart([]int{notes.editID})
}

// ── Menus ────────────────────────────────────────────────────────────────

// notesOpenListMenu is Select (M on a keyboard) on the list.
func notesOpenListMenu(config *Config) {
	body := "Search narrows the list as you type.\n\n" +
		"Every note as a file writes each note as a .txt file into " + notesShownPath("") +
		", in one folder per notes folder — the same file again each time.\n\n" +
		"Open on phone shows a code for your phone's camera: read, write, add and download notes there."
	PlayActivateSound()
	tbxOpenSheet(config, tbxSheetSpec{title: "Notes", icon: "edit", body: body, buttons: []tbxSheetButton{
		{label: "Search", primary: true, run: notesStartSearch},
		{label: "Every note as a file", run: notesExportAllStart},
		{label: "Open on phone", run: func() { notesOpenOnPhone(0) }},
	}})
}

// notesStartSearch puts the focus in the search field: the on-screen
// keyboard opens on it, or a keyboard types into it.
func notesStartSearch() {
	if hintsUseKeyboard() {
		notes.tab = 3
		return
	}
	notesVKOpen(notesVKSearch)
}

// notesOpenNoteMenu is Y or Select in the editor.
func notesOpenNoteMenu(config *Config) {
	if notes.editID < 0 {
		return
	}
	n, _ := notesFind(notes.editID)
	where := notesShownPath(notesFolderOnDisk(n.Folder) + "/" + notesFileName(notes.editTitle) + ".txt")
	if n.File != "" {
		where = notesShownPath(n.File)
	}
	body := "Save as file writes this note, as you see it now, to " + where + ". Saving again updates the same file.\n\n" +
		"Open on phone shows a code that opens this note on your phone, to read it or type the rest there.\n\n" +
		"Delete note asks first."
	PlayActivateSound()
	tbxOpenSheet(config, tbxSheetSpec{title: notesSheetTitle(notes.editTitle), icon: "edit", body: body, buttons: []tbxSheetButton{
		{label: "Save as file", primary: true, run: notesExportCurrent},
		{label: "Open on phone", run: func() {
			notesSaveCurrentEdit()
			notesOpenOnPhone(notes.editID)
		}},
		{label: "Delete note", run: func() {
			if n, ok := notesFind(notes.editID); ok {
				notesConfirmDelete(config, n)
			}
		}},
	}})
}

// notesSheetTitle is a note's title for a sheet's heading.
func notesSheetTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "Untitled note"
	}
	return truncateRunesSuffix(title, 40, "…")
}

// notesConfirmDelete asks before deleting n (X on the list, the editor's
// menu). A file saved from the note stays.
func notesConfirmDelete(config *Config, n noteEntry) {
	body := "The note is deleted here and on the phone page."
	if n.File != "" {
		body += " Its file, " + notesShownPath(n.File) + ", stays."
	}
	tbxOpenSheet(config, tbxSheetSpec{title: "Delete “" + notesSheetTitle(n.Title) + "”?", icon: "trash", body: body,
		buttons: []tbxSheetButton{
			{label: "Keep"},
			{label: "Delete", primary: true, run: func() {
				if notes.editID == n.ID {
					// Deleted here: nothing is saved again on the way out.
					if notesVKActive() {
						notes.editOrigTitle, notes.editOrigContent = notes.editTitle, notes.editContent
						vkCloseKeyboard()
					}
					notes.editID = -1
					notesCloseEditor()
				}
				notesDeleteNote(n.ID)
				if left := len(notesFiltered()); notes.cursor >= left {
					notes.cursor = max(0, left-1)
				}
				PlayBackSound()
				ShowToast("Note deleted", ToastKindInfo)
			}},
		}})
}

// notesFooterHints is the footer (extras.go): base is the screen's default.
// The list takes its four action keys and the menu; the editor names the keys
// a pad types and moves with, or a keyboard's own; the search its two.
func notesFooterHints(base []FooterHint) []FooterHint {
	if tbxSheetVisible(appConfig) {
		return tbxSheetHints()
	}
	keys := hintsUseKeyboard()
	switch notes.tab {
	case 0:
		hints := append([]FooterHint(nil), base...)
		if len(hints) > 1 {
			hints[1] = FooterHint{Button: "A", Label: "Edit"}
		}
		return append(hints,
			FooterHint{Button: "Y", Label: "New"},
			FooterHint{Button: "X", Label: "Delete"},
			// "/ F": the keyboard cycles folders with F (inputhints.go shows
			// whichever half the user can press); M is Select's twin.
			FooterHint{Button: "L1/R1 / F", Label: "Folder"},
			FooterHint{Button: "Select / M", Label: "Menu"},
		)
	case 3:
		if keys {
			return []FooterHint{{Button: "Enter", Label: "Done"}, {Button: "Backspace", Label: "Delete"}}
		}
		// Search is a draft, not an edit: B leaves the field.
		return []FooterHint{{Button: "A", Label: "Type"}, {Button: "B", Label: "Done"}}
	}
	if keys {
		hints := []FooterHint{{Button: "Esc", Label: "Save & Back"}, {Button: "Ctrl+S", Label: "Save"}, {Button: "Ctrl+M", Label: "More"}}
		if notes.tab == 1 {
			return append(hints, FooterHint{Button: "Tab", Label: "Note"})
		}
		return append(hints, FooterHint{Button: "Ctrl+Up", Label: "Title"}, FooterHint{Button: "PgUp/PgDn", Label: "Scroll"})
	}
	typeLabel := "Type"
	if notes.tab == 1 {
		typeLabel = "Type title"
	}
	return []FooterHint{
		{Button: "A", Label: typeLabel},
		{Button: "Up/Down", Label: "Title / note"},
		{Button: "L1/R1", Label: "Folder"},
		{Button: "Y", Label: "More"},
		{Button: "B", Label: "Save & Back"},
	}
}

// ── The phone ────────────────────────────────────────────────────────────

// notesOpenOnPhone shows the code that opens the Phone Companion's Notes
// page (at note id, when it is not 0), starting the companion if it is off.
func notesOpenOnPhone(id int) {
	companionStart()
	if companion.url == "" {
		msg := companion.err
		if msg == "" {
			msg = "Your phone reaches the notes over Wi-Fi: connect this device first"
		}
		ShowToast(msg, ToastKindWarning)
		return
	}
	code, err := qrcode.New(companionNotesURL(companion.url, id), qrcode.Low)
	if err != nil {
		ShowToast("Cannot draw the QR code: "+err.Error(), ToastKindWarning)
		return
	}
	detail := "Open it with your phone's camera"
	if caption := companionPairingCaption(companion.url); caption != "" {
		detail += " · " + caption
	}
	qrZoomOpenCardDetail(code.Bitmap(), "Notes on your phone", detail)
}
