package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// writeSolidPNG writes a small solid-colour PNG. The launcher render test uses
// an unmistakable colour so a drawn icon can be found in the frame.
func writeSolidPNG(t *testing.T, path string, w, h int, c color.RGBA) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := png.Encode(file, img); err != nil {
		t.Fatal(err)
	}
}

// A program folder app carries its icon, so the launcher shows the app's own
// artwork instead of an initial.
func TestScanAppsFolderPicksUpIcons(t *testing.T) {
	dir := t.TempDir()
	mustWriteAppFile(t, filepath.Join(dir, "CoolApp.exe"))
	coolIcon := filepath.Join(dir, "CoolApp.png")
	writeSolidPNG(t, coolIcon, 32, 32, color.RGBA{R: 200, G: 40, B: 40, A: 255})

	// TrimUI-style folder app: config.json declares the icon.
	sub := filepath.Join(dir, "JukaLand")
	mustWriteAppFile(t, filepath.Join(sub, "launch.sh"))
	landIcon := filepath.Join(sub, "art.png")
	writeSolidPNG(t, landIcon, 32, 32, color.RGBA{R: 40, G: 120, B: 200, A: 255})
	cfg := `{"label": "JukaLand", "launch": "launch.sh", "icon": "art.png"}`
	if err := os.WriteFile(filepath.Join(sub, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	// A folder that only ships the conventional icon.png.
	plain := filepath.Join(dir, "Plain")
	mustWriteAppFile(t, filepath.Join(plain, "launch.sh"))
	plainIcon := filepath.Join(plain, "icon.png")
	writeSolidPNG(t, plainIcon, 32, 32, color.RGBA{R: 40, G: 200, B: 120, A: 255})
	if err := os.WriteFile(filepath.Join(plain, "config.json"), []byte(`{"label": "Plain"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	got := map[string]string{}
	for _, e := range scanAppsFolder(dir) {
		got[e.Name] = e.Icon
	}
	want := map[string]string{
		"CoolApp":  coolIcon,
		"JukaLand": landIcon,
		"Plain":    plainIcon,
	}
	for name, icon := range want {
		if got[name] != icon {
			t.Errorf("entry %q icon = %q, want %q", name, got[name], icon)
		}
	}
}

// A tampered config.json must not point the launcher at an icon outside the app
// folder; the folder's own icon.png is used instead.
func TestScanAppSubdirRefusesIconOutsideTheFolder(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside.png")
	writeSolidPNG(t, outside, 8, 8, color.RGBA{R: 1, G: 2, B: 3, A: 255})

	appDir := filepath.Join(root, "apps", "Sneaky")
	mustWriteAppFile(t, filepath.Join(appDir, "launch.sh"))
	local := filepath.Join(appDir, "icon.png")
	writeSolidPNG(t, local, 8, 8, color.RGBA{R: 4, G: 5, B: 6, A: 255})
	cfg := `{"label": "Sneaky", "icon": "../../outside.png"}`
	if err := os.WriteFile(filepath.Join(appDir, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	entry, ok := scanAppSubdir(appDir)
	if !ok {
		t.Fatal("app folder was not recognised")
	}
	if entry.Icon != local {
		t.Fatalf("icon = %q, want the folder's own %q", entry.Icon, local)
	}
}

// The SD-card Apps root holds one folder per installed app. Each folder must
// become one launcher row carrying its label and icon, never a bare "launch"
// row per app.
func TestScanSystemAppsReadsTrimUIAppFolders(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"Alpha", "Beta"} {
		dir := filepath.Join(root, name)
		mustWriteAppFile(t, filepath.Join(dir, "launch.sh"))
		writeSolidPNG(t, filepath.Join(dir, "icon.png"), 16, 16, color.RGBA{R: 90, G: 90, B: 90, A: 255})
		cfg := `{"label": "` + name + ` App", "launch": "launch.sh"}`
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	entries := scanSystemApps(root)
	byName := map[string]FileEntry{}
	for _, e := range entries {
		byName[e.Name] = e
	}
	for _, want := range []string{"Alpha App", "Beta App"} {
		entry, ok := byName[want]
		if !ok {
			t.Fatalf("missing app folder row %q in %v", want, byName)
		}
		if entry.Icon == "" {
			t.Errorf("%q has no icon", want)
		}
		if filepath.Base(entry.Path) != "launch.sh" {
			t.Errorf("%q launches %q, want the folder's launch script", want, entry.Path)
		}
	}
	if _, ok := byName["launch"]; ok {
		t.Error("the launcher listed a bare launch script instead of the app folder")
	}
}

// Built-in applets have no file on disk, and a row without an icon path falls
// through to the initial; both must resolve to "no icon file" rather than
// stat-ing a scene name.
func TestAppEntryIconPathSkipsBuiltins(t *testing.T) {
	if got := appEntryIconPath(FileEntry{Name: "Browser (built-in)", Path: builtinScenePrefix + "Browser"}); got != "" {
		t.Errorf("built-in scene icon = %q, want none", got)
	}
	if got := appEntryIconPath(FileEntry{Name: "PS (built-in)", Path: builtinActionPrefix + "chiaki"}); got != "" {
		t.Errorf("built-in action icon = %q, want none", got)
	}
	dir := t.TempDir()
	icon := filepath.Join(dir, "app.png")
	writeSolidPNG(t, icon, 8, 8, color.RGBA{R: 7, G: 7, B: 7, A: 255})
	if got := appEntryIconPath(FileEntry{Name: "App", Path: filepath.Join(dir, "app.exe"), Icon: icon}); got != icon {
		t.Errorf("icon = %q, want %q", got, icon)
	}
}

// Every tile keeps a colour of its own but the same app keeps the same colour
// between launches.
func TestAppGridTileTintIsStable(t *testing.T) {
	if appGridTileTint("Firefox") != appGridTileTint("Firefox") {
		t.Error("tile tint is not stable for the same app")
	}
	seen := map[sdl.Color]bool{}
	for _, name := range []string{"Firefox", "Steam", "Terminal", "Notes", "Files", "Music"} {
		seen[appGridTileTint(name)] = true
	}
	if len(seen) < 3 {
		t.Errorf("six apps share %d tints: the launcher looks flat", len(seen))
	}
}
