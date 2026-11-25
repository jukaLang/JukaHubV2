package main

// notes_keyboard.go — typing a note with the on-screen keyboard (Juka,
// 2026-11-25: "For Notes, add ability to type using virtual keyboard on Trimui
// devices").
//
// A handheld has no letters to press, and Notes used to take only a real
// keyboard's: on a TrimUI a note could be opened but not written. Now A in
// the editor opens the keyboard on the field in focus — the title or the body
// — and Y makes a new note with the keyboard already on its title.
//
// The keyboard edits the note itself (notesVKTarget), live, so the editor
// above the sheet shows the text as it grows, wrapped, with the caret where
// the keyboard's is and the body scrolled to keep it in view (notes.go).
// Unlike the one-line fields elsewhere the body keeps its lines: its action
// key is New line (Enter on a real keyboard), and Start, Y or Ctrl+Enter is
// Done; the sheet's own field shows the line the caret is on (vkFieldView).
// Done on the title moves on to the body. Closing the keyboard, any way, saves
// the note: nothing typed waits for a second button.
//
// The same target types the list's search, which narrows the list as it is
// typed (Select → Search).

import (
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// notesVKTarget aims the on-screen keyboard at the open note or the search.
const notesVKTarget vkTargetKind = 2612

// notesVKFieldKind is the field the keyboard types into.
type notesVKFieldKind int

const (
	notesVKTitle notesVKFieldKind = iota
	notesVKBody
	notesVKSearch
)

// notesVK is the keyboard's field. Main thread only.
var notesVK struct {
	field notesVKFieldKind
}

// notesVKActive reports the keyboard up on Notes.
func notesVKActive() bool {
	return virtualKeyboardActive && vkTarget == notesVKTarget
}

// notesVKOpen opens the keyboard on field: the editor's focus follows it, and
// a new note's "New Note" title opens selected, so the first letter replaces
// it.
func notesVKOpen(field notesVKFieldKind) {
	chatInputActive = false
	unitInputActive = false
	notesVK.field = field
	switch field {
	case notesVKTitle:
		notes.tab = 1
	case notesVKBody:
		notes.tab = 2
	case notesVKSearch:
		notes.tab = 3
	}
	vkTarget = notesVKTarget
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	vkTextSelected = field == notesVKTitle && notes.editTitle == "New Note"
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	// The body is redrawn above the sheet with the caret in view.
	notes.lastLen, notes.lastCaret = -1, -1
	sdl.StartTextInput()
	vkEnsureLayout()
}

// notesVKText is the field's text (vkCurrentText).
func notesVKText() string {
	switch notesVK.field {
	case notesVKTitle:
		return notes.editTitle
	case notesVKSearch:
		return notes.search
	}
	return notes.editContent
}

// notesVKOneLine folds line breaks and tabs into spaces, for the title and
// the search (a paste may carry them).
var notesVKOneLine = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ")

// notesVKSetText stores what the keyboard typed (vkSetText), within the
// field's limit; the search narrows the list at once.
func notesVKSetText(text string) {
	switch notesVK.field {
	case notesVKTitle:
		notes.editTitle = vkTruncateUTF8(notesVKOneLine.Replace(text), notesTitleMax)
	case notesVKSearch:
		notes.search = vkTruncateUTF8(notesVKOneLine.Replace(text), notesSearchMax)
		notes.cursor = 0
	default:
		notes.editContent = vkTruncateUTF8(strings.ReplaceAll(text, "\r\n", "\n"), notesBodyMax)
	}
}

// notesVKMaxLen is the field's limit in bytes (vkMaxLen).
func notesVKMaxLen() int {
	switch notesVK.field {
	case notesVKTitle:
		return notesTitleMax
	case notesVKSearch:
		return notesSearchMax
	}
	return notesBodyMax
}

// notesVKHeading names the field on the sheet.
func notesVKHeading() string {
	switch notesVK.field {
	case notesVKTitle:
		return "NOTE TITLE"
	case notesVKSearch:
		return "SEARCH NOTES"
	}
	return "NOTE"
}

// notesVKPlaceholder is an empty field's grey example.
func notesVKPlaceholder() string {
	switch notesVK.field {
	case notesVKTitle:
		return "Shopping list, Ideas, Passwords to change…"
	case notesVKSearch:
		return "A word from a title or a note"
	}
	return "Start typing your note…"
}

// notesVKActionLabel is the action key's word: the title moves on to the
// body, the body takes a new line, the search shows its matches.
func notesVKActionLabel() string {
	switch notesVK.field {
	case notesVKTitle:
		return "Next"
	case notesVKSearch:
		return "Search"
	}
	return "New line"
}

// notesVKNewlineField reports the body: Enter and the action key type a line
// break there (vkEnterTypesNewline).
func notesVKNewlineField() bool {
	return notesVK.field == notesVKBody
}

// notesVKProse reports a field of sentences, where a sentence starts with a
// capital (vkSentenceTarget).
func notesVKProse() bool {
	return notesVK.field != notesVKSearch
}

// notesVKClosed is the keyboard closing, however it closed: the note is
// saved, so nothing typed waits for another button. Called from
// vkCloseKeyboard while the target is still Notes.
func notesVKClosed() {
	switch notesVK.field {
	case notesVKTitle, notesVKBody:
		if notes.editID >= 0 && (notes.tab == 1 || notes.tab == 2) && notesEditDirty() {
			notesSaveCurrentEdit()
		}
	}
}

// notesVKDone is the keyboard's Done (Start, Y, Ctrl+Enter, the title's
// Next), after it closed: the title moves on to the body, the search shows
// the list with the cursor on the first match.
func notesVKDone(field notesVKFieldKind) {
	switch field {
	case notesVKTitle:
		if notes.editID >= 0 && notes.tab == 1 {
			notesVKOpen(notesVKBody)
		}
	case notesVKSearch:
		notes.tab = 0
		notes.cursor = 0
	}
}
