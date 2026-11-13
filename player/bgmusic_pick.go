package main

// The background-music picker borrows the Files screen, just as chat
// attachments do. A opens a file or enters a folder; X selects the focused
// item, and the breadcrumb bar offers a clickable "Use this folder" chip.

import (
	"os"
	"path/filepath"
	"time"
)

var bgmPick struct {
	active        bool
	settingsScene int
	seenFiles     bool
	started       time.Time
}

func bgmPickActive() bool { return bgmPick.active }

func bgmPickBegin(c *Config) bool {
	if c == nil || transitionPhase != "none" {
		return false
	}
	idx := sceneIndexExact(c, "FileExplorer")
	if idx < 0 {
		showToast("Files isn't part of this layout", ToastWarn())
		return false
	}
	bgmPick.active = true
	bgmPick.settingsScene = currentSceneIndex
	bgmPick.seenFiles = currentSceneIndex == idx
	bgmPick.started = time.Now()
	feSelectionClear()

	// Reopen at the previous selection's folder when it still exists. Files' active
	// location is the filesystem root, allowing the chosen path to live beyond
	// the app's default Files root without modifying that preference.
	if selected := bgmSelectedPathIn(c); selected != "" {
		dir := selected
		if info, err := os.Stat(selected); err != nil || !info.IsDir() {
			dir = filepath.Dir(selected)
		}
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			filesNav.location = filesVolumeRoot(dir)
			filesNav.atLocations = false
			if c.Variables.Custom == nil {
				c.Variables.Custom = map[string]interface{}{}
			}
			c.Variables.Custom["fe_path"] = dir
			filesNav.restore = true
		}
	}
	if currentSceneIndex != idx {
		changeSceneTo(c, idx)
	}
	showToast("Choose a song with A, or a folder with X", ToastInfo())
	return true
}

func bgmPickCancel() {
	bgmPick.active = false
	bgmPick.seenFiles = false
}

func bgmPickReturn(c *Config) {
	idx := bgmPick.settingsScene
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

func bgmPickBack(c *Config) bool {
	if !bgmPick.active {
		return false
	}
	if transitionPhase != "none" {
		return true
	}
	bgmPickCancel()
	feSelectionClear()
	PlayBackSound()
	bgmPickReturn(c)
	return true
}

func bgmPickTick(c *Config) {
	if !bgmPick.active {
		return
	}
	if feSceneActive(c) {
		bgmPick.seenFiles = true
		return
	}
	if bgmPick.seenFiles || time.Since(bgmPick.started) > 3*time.Second {
		bgmPickCancel()
	}
}

// bgmPickChoose accepts only entries that Files could browse. This keeps a
// stale or forged list row from turning into a saved playback path.
func bgmPickChoose(c *Config, path string) bool {
	if !bgmPick.active || c == nil {
		return false
	}
	if transitionPhase != "none" {
		return true
	}
	root := feRoot(c)
	var resolved string
	var err error
	if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
		resolved, err = existingDirWithin(root, path)
	} else {
		resolved, err = regularFileWithin(root, path)
		if err == nil && !isAudioFile(resolved) {
			showToast("Choose a music file or folder", ToastInfo())
			return true
		}
	}
	if err != nil {
		showToast("Cannot choose that music: "+filesFriendlyError(err), ToastWarn())
		return true
	}
	if err := bgmSetSelectedPath(c, resolved); err != nil {
		showToast("Cannot choose that music: "+filesFriendlyError(err), ToastWarn())
		return true
	}
	bgmPickCancel()
	feSelectionClear()
	showToast("Background music: "+filepath.Base(resolved), ToastSuccess())
	bgmPickReturn(c)
	return true
}

// A on a file chooses it; A on a folder still enters it.
func bgmPickFromFiles(c *Config, entry FileEntry) bool {
	if !bgmPick.active || entry.IsDir || entry.Name == ".." {
		return false
	}
	return bgmPickChoose(c, entry.Path)
}

// X on a folder chooses it; X on a file is also a direct way to choose it.
// With the parent row focused, X chooses the folder currently on screen.
func bgmPickFocused(c *Config) bool {
	if !bgmPick.active {
		return false
	}
	if filesNav.atLocations {
		showToast("Open a location, then choose music", ToastInfo())
		return true
	}
	if entry, ok := feFocusEntry(c); ok && entry.Name != ".." {
		return bgmPickChoose(c, entry.Path)
	}
	return bgmPickChoose(c, feCurrentPath(c))
}
