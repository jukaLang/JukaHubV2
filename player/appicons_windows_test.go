//go:build windows

package main

// The extractor is the only icon source for Start Menu shortcuts, so this test
// runs the real helper against a real shortcut and checks that the launcher can
// find and decode the cached PNG afterwards.

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func firstStartMenuShortcut() string {
	for _, dir := range systemAppDirs() {
		found := ""
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || found != "" {
				return nil
			}
			if !info.IsDir() && strings.EqualFold(filepath.Ext(path), ".lnk") {
				found = path
			}
			return nil
		})
		if found != "" {
			return found
		}
	}
	return ""
}

func TestExtractAppIconsCachesADecodablePng(t *testing.T) {
	link := firstStartMenuShortcut()
	if link == "" {
		t.Skip("no Start Menu shortcut is available to read an icon from")
	}

	prev := appIconCacheDir
	cacheRoot := filepath.Join(t.TempDir(), "appicons")
	appIconCacheDir = func() string { return cacheRoot }
	t.Cleanup(func() { appIconCacheDir = prev })

	if cached := extractedAppIcon(link); cached != "" {
		t.Fatalf("a fresh cache already had %q", cached)
	}
	extractAppIcons([]string{link})

	cached := extractedAppIcon(link)
	if cached == "" {
		t.Fatalf("the extractor cached no icon for %q", link)
	}
	data, err := os.ReadFile(cached)
	if err != nil {
		t.Fatalf("read cached icon: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("cached icon is not a PNG: %v", err)
	}
	bounds := img.Bounds()
	if bounds.Dx() < 24 || bounds.Dy() < 24 {
		t.Errorf("cached icon is %dx%d, too small to draw", bounds.Dx(), bounds.Dy())
	}
}

// A launcher scan must hand the extractor only the programs that could have an
// icon, so a .sh/.bat row does not start a shell for nothing.
func TestRequestAppIconExtractionSkipsNonPrograms(t *testing.T) {
	prev := appIconCacheDir
	cacheRoot := filepath.Join(t.TempDir(), "appicons")
	appIconCacheDir = func() string { return cacheRoot }
	t.Cleanup(func() { appIconCacheDir = prev })

	state := &appIconExtractState
	state.mu.Lock()
	state.tried = map[string]bool{}
	state.busy = false
	state.mu.Unlock()
	t.Cleanup(func() {
		state.mu.Lock()
		state.tried = nil
		state.mu.Unlock()
	})

	dir := t.TempDir()
	requestAppIconExtraction([]string{
		filepath.Join(dir, "thing.sh"),
		filepath.Join(dir, "thing.bat"),
	})

	state.mu.Lock()
	busy := state.busy
	state.mu.Unlock()
	if busy {
		t.Error("a script row started an icon extraction run")
	}
	if files, err := os.ReadDir(cacheRoot); err == nil && len(files) > 0 {
		t.Errorf("the extractor wrote %d files for scripts that have no icon", len(files))
	}
}
