package main

// webfile_pick.go — choosing a local file for a page's <input type=file>.
//
// The browser has no file dialog of its own; it borrows Files, exactly as the
// background-music and chat attachment pickers do. A opens folders and chooses
// a file, X chooses the focused file, and B goes back to the page without
// changing it. The chosen path lives on the webControl until the form's own
// button sends it as a multipart part (browserFormNavigationFiles), so a page
// never sees a path the user did not pick through Files.

import (
	"os"
	"path/filepath"
	"time"
)

var webPick struct {
	active        bool
	settingsScene int
	variable      string
	ctrl          *webControl
	seenFiles     bool
	started       time.Time
}

func webPickActive() bool { return webPick.active }

// webPickBegin opens Files to pick one file for ctrl on the page stored under
// variable.
func webPickBegin(c *Config, variable string, ctrl *webControl) bool {
	if c == nil || ctrl == nil || ctrl.Kind != "file" || transitionPhase != "none" {
		return false
	}
	idx := sceneIndexExact(c, "FileExplorer")
	if idx < 0 {
		showToast("Files isn't part of this layout", ToastWarn())
		return false
	}
	webPick.active = true
	webPick.settingsScene = currentSceneIndex
	webPick.variable = variable
	webPick.ctrl = ctrl
	webPick.seenFiles = currentSceneIndex == idx
	webPick.started = time.Now()
	feSelectionClear()
	if currentSceneIndex != idx {
		changeSceneTo(c, idx)
	}
	showToast("Choose a file with A", ToastInfo())
	return true
}

func webPickCancel() {
	webPick.active = false
	webPick.variable = ""
	webPick.ctrl = nil
	webPick.seenFiles = false
}

// webPickReturn puts the page back. It is the same history-aware return the
// other pickers use, so B in Files lands on the page, not on a duplicate.
func webPickReturn(c *Config) {
	idx := webPick.settingsScene
	if c == nil || idx < 0 || idx >= len(c.Scenes) || idx == currentSceneIndex {
		return
	}
	if n := len(sceneHistory); n > 0 && sceneHistory[n-1] == idx {
		sceneHistory = sceneHistory[:n-1]
		switchScene(c, idx, false)
		if pendingSceneIndex != idx {
			pushSceneHistory(idx)
		}
		return
	}
	changeSceneTo(c, idx)
}

// webPickBack is B in Files while it picks a file: the pick is called off and
// the page comes back.
func webPickBack(c *Config) bool {
	if !webPick.active {
		return false
	}
	if transitionPhase != "none" {
		return true
	}
	webPickCancel()
	feSelectionClear()
	PlayBackSound()
	webPickReturn(c)
	return true
}

// webPickTick ends a pick whose Files screen is gone (Home, the Start menu, B
// on Locations), so a stale pick cannot claim a later file.
func webPickTick(c *Config) {
	if !webPick.active {
		return
	}
	if feSceneActive(c) {
		webPick.seenFiles = true
		return
	}
	if webPick.seenFiles || time.Since(webPick.started) > 3*time.Second {
		webPickCancel()
	}
}

// webPickChoose accepts only a regular file below Files' active root and small
// enough for the analyzer's upload budget. The page sees the file's name, never
// the rest of the path, because CreateFormFile writes the base name.
func webPickChoose(c *Config, path string) bool {
	if !webPick.active || c == nil || webPick.ctrl == nil {
		return false
	}
	if transitionPhase != "none" {
		return true
	}
	resolved, err := regularFileWithin(feRoot(c), path)
	if err != nil {
		showToast("Cannot choose that file: "+filesFriendlyError(err), ToastWarn())
		return true
	}
	info, err := os.Stat(resolved)
	if err != nil || info.IsDir() {
		showToast("Choose a file, not a folder", ToastInfo())
		return true
	}
	if info.Size() > browserUploadMax {
		showToast("That file is larger than the upload limit", ToastWarn())
		return true
	}
	webPick.ctrl.Value = resolved
	if doc := browserDocumentFor(webPick.variable); doc != nil {
		doc.invalidateFormState()
	}
	name := filepath.Base(resolved)
	webPickCancel()
	feSelectionClear()
	showToast("File: "+name, ToastSuccess())
	webPickReturn(c)
	return true
}

// webPickFromFiles is A on a Files row in pick mode: a file is chosen, a folder
// still opens.
func webPickFromFiles(c *Config, entry FileEntry) bool {
	if !webPick.active || entry.IsDir || entry.Name == ".." {
		return false
	}
	return webPickChoose(c, entry.Path)
}

// webPickFocused is X in Files while it picks: choose the focused file. A
// folder is not an upload; A still enters it to reach the file inside.
func webPickFocused(c *Config) bool {
	if !webPick.active {
		return false
	}
	if filesNav.atLocations {
		showToast("Open a location, then choose a file", ToastInfo())
		return true
	}
	if entry, ok := feFocusEntry(c); ok && !entry.IsDir && entry.Name != ".." {
		return webPickChoose(c, entry.Path)
	}
	showToast("Choose a file with A", ToastInfo())
	return true
}
