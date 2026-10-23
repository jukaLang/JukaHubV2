package main

// Availability-list tests for the Emulators panel: catalog detection on both
// platforms (against real temp folders) and the home card wiring that used to
// dead-end on configs without an "Emulator" scene.

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

func writeFakeEmuFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func noLookPath(string) (string, error) { return "", os.ErrNotExist }

// TestWindowsEmulatorDetection pins the desktop fix: the catalog marks found
// installs (at the root or in their vendor folder) as available with a launch
// path, and everything else as not installed.
func TestWindowsEmulatorDetection(t *testing.T) {
	dir := t.TempDir()
	writeFakeEmuFile(t, filepath.Join(dir, "retroarch.exe"))
	writeFakeEmuFile(t, filepath.Join(dir, "PPSSPP", "PPSSPPWindows.exe"))

	entries := windowsEmulatorEntries([]string{dir}, noLookPath)
	byName := map[string]emuEntry{}
	for _, e := range entries {
		byName[e.Name] = e
	}

	ra := byName["RetroArch"]
	if ra.Status != emuAvailable || ra.Launch != filepath.Join(dir, "retroarch.exe") {
		t.Errorf("RetroArch = %+v, want available at %s", ra, filepath.Join(dir, "retroarch.exe"))
	}
	pp := byName["PPSSPP"]
	if pp.Status != emuAvailable || pp.Launch != filepath.Join(dir, "PPSSPP", "PPSSPPWindows.exe") {
		t.Errorf("PPSSPP = %+v, want available in its vendor folder", pp)
	}
	if d := byName["Dolphin"]; d.Status != emuMissing || d.Launch != "" {
		t.Errorf("Dolphin = %+v, want not installed with no launch path", d)
	}
	if len(entries) != len(winEmuCatalog) {
		t.Errorf("listed %d entries, want the full catalog of %d", len(entries), len(winEmuCatalog))
	}
}

// TestDeviceEmulatorCatalog pins the handheld list: emulator folders with
// their availability (deduped across roots), RetroArch cores, and ROM systems
// that have games but no emulator.
func TestDeviceEmulatorCatalog(t *testing.T) {
	root := t.TempDir()
	emuA := filepath.Join(root, "sd", "Emus")
	emuB := filepath.Join(root, "usr", "emus")
	roms := filepath.Join(root, "sd", "Roms")
	cores := filepath.Join(root, "sd", "RetroArch", ".retroarch", "cores")

	writeFakeEmuFile(t, filepath.Join(emuA, "GBA", "launch.sh"))
	writeFakeEmuFile(t, filepath.Join(emuB, "GBA", "launch.sh"))           // duplicate root
	if err := os.MkdirAll(filepath.Join(emuA, "PSP"), 0o755); err != nil { // no launcher
		t.Fatal(err)
	}
	writeFakeEmuFile(t, filepath.Join(roms, "FC", "game.nes"))
	writeFakeEmuFile(t, filepath.Join(roms, "GBA", "game.gba"))
	writeFakeEmuFile(t, filepath.Join(cores, "pcsx_rearmed_libretro.so"))

	entries := deviceEmulatorEntries([]string{emuA, emuB}, roms, cores)
	byName := map[string]emuEntry{}
	count := map[string]int{}
	for _, e := range entries {
		byName[e.Name] = e
		count[e.Name]++
	}

	if count["GBA"] != 1 {
		t.Errorf("GBA listed %d times, want 1 (roots must dedupe)", count["GBA"])
	}
	if g := byName["GBA"]; g.Status != emuAvailable || g.Launch == "" {
		t.Errorf("GBA = %+v, want available with a launcher", g)
	}
	if p := byName["PSP"]; p.Status != emuBroken {
		t.Errorf("PSP = %+v, want unavailable (folder without launcher)", p)
	}
	if f := byName["FC"]; f.Status != emuMissing || f.Launch != "" {
		t.Errorf("FC = %+v, want missing (ROMs but no emulator)", f)
	}
	if c := byName["pcsx_rearmed"]; c.Status != emuAvailable {
		t.Errorf("pcsx_rearmed = %+v, want available as a RetroArch core", c)
	}
}

// TestEmulatorCardOpensPanel pins the "doesn't work on Windows" bug: the card
// must open the availability panel whether or not the config has an Emulator
// scene (the desktop config has none; the handheld must get the listing too).
func TestEmulatorCardOpensPanel(t *testing.T) {
	snapshotHomeGlobals(t)
	emuPanel.open = false
	t.Cleanup(func() {
		emuPanel.open = false
		invalidateEmulatorCache()
	})

	for _, scenes := range [][]SceneConfig{
		nil,
		{{Name: "Main", Layout: "home"}, {Name: "Emulator"}},
	} {
		homeLayout = NewHomeLayout(probeW, probeH)
		if !homeLayout.Focus.Set(HomeFocusEmulator) {
			t.Fatal("Emulator card must be focusable")
		}
		homeActivate(&Config{Scenes: scenes})
		if !emuPanel.open {
			t.Fatalf("Emulator card did not open the panel (scenes=%v)", scenes)
		}
		emuPanel.open = false
	}
}

// TestEmuGridTargetClampsInsideTheGrid pins the tile navigation: sideways
// stays in the row, vertical moves a whole row, and a partial last row lands on
// its last tile instead of a hole.
func TestEmuGridTargetClampsInsideTheGrid(t *testing.T) {
	cases := []struct {
		cursor, total, cols, dx, dy int
		want                        int
	}{
		{0, 9, 3, 1, 0, 1},
		{2, 9, 3, 1, 0, 2},  // right at the row edge clamps
		{2, 9, 3, 0, 1, 5},  // down a row
		{1, 9, 3, -1, 0, 0}, // left at the edge clamps
		{0, 9, 3, 0, -1, 0}, // up at the top clamps
		{7, 9, 3, 0, 1, 8},  // partial row: last tile, not a gap
		{8, 9, 3, 1, 0, 8},  // right on the partial row stays put
		{4, 0, 3, 1, 0, 0},  // empty catalog
		{4, 9, 1, 1, 0, 4},  // single column ignores horizontal moves
	}
	for _, c := range cases {
		if got := emuGridTarget(c.cursor, c.total, c.cols, c.dx, c.dy); got != c.want {
			t.Errorf("emuGridTarget(%d,%d,%d,%d,%d) = %d, want %d",
				c.cursor, c.total, c.cols, c.dx, c.dy, got, c.want)
		}
	}
}

// TestEmuGridViewAdaptsToTheCompactCanvas checks the page still lays out a
// usable grid on the 960x720 Brick canvas, not just at 1280x720.
func TestEmuGridViewAdaptsToTheCompactCanvas(t *testing.T) {
	prevW, prevH := screenWidth, screenHeight
	t.Cleanup(func() { screenWidth, screenHeight = prevW, prevH })

	screenWidth, screenHeight = 1280, 720
	wide := emuGridView()
	screenWidth, screenHeight = 960, 720
	compact := emuGridView()

	if compact.cols < 2 || compact.rows < 2 {
		t.Fatalf("compact grid = %d columns x %d rows, want a real grid", compact.cols, compact.rows)
	}
	if wide.cols <= compact.cols {
		t.Errorf("wide grid has %d columns, compact %d; the wide page should fit more", wide.cols, compact.cols)
	}
	if compact.tileW < emuTileMinW {
		t.Errorf("compact tile width = %d, below the minimum %d", compact.tileW, emuTileMinW)
	}
	if compact.body.X < compact.page.X ||
		compact.body.X+compact.body.W > compact.page.X+compact.page.W ||
		compact.body.Y+compact.body.H > compact.page.Y+compact.page.H {
		t.Errorf("tile area %+v escapes the page %+v", compact.body, compact.page)
	}
}

// TestEmuFolderIconPrefersTheDeclaredIcon checks the picture lookup for a
// TrimUI-style emulator folder: config.json's icon wins, an unsafe path falls
// back to the conventional file, and a folder with neither yields nothing.
func TestEmuFolderIconPrefersTheDeclaredIcon(t *testing.T) {
	dir := t.TempDir()
	writeFakeEmuFile(t, filepath.Join(dir, "launch.sh"))
	declared := filepath.Join(dir, "art.png")
	if err := os.WriteFile(declared, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "icon.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"icon":"art.png"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := emuFolderIcon(dir); got != declared {
		t.Errorf("emuFolderIcon = %q, want the declared %q", got, declared)
	}

	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"icon":"../escape.png"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := emuFolderIcon(dir); got != filepath.Join(dir, "icon.png") {
		t.Errorf("unsafe icon path fell back to %q, want icon.png", got)
	}

	empty := t.TempDir()
	if got := emuFolderIcon(empty); got != "" {
		t.Errorf("folder with no picture returned %q, want empty", got)
	}
}

// TestEmuEntryIconPathFallsBackToSidecar covers a Windows-style install with a
// picture stored next to the executable.
func TestEmuEntryIconPathFallsBackToSidecar(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "retroarch.exe")
	if err := os.WriteFile(exe, []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}
	icon := filepath.Join(dir, "retroarch.png")
	if err := os.WriteFile(icon, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := emuEntryIconPath(emuEntry{Name: "RetroArch", Launch: exe}); got != icon {
		t.Errorf("emuEntryIconPath = %q, want the sidecar %q", got, icon)
	}
	if got := emuEntryIconPath(emuEntry{Name: "GBA"}); got != "" {
		t.Errorf("entry with no launcher returned %q, want empty", got)
	}
}

func TestEmuTileDisplayNameReadsCores(t *testing.T) {
	if got := emuTileDisplayName("pcsx_rearmed"); got != "pcsx rearmed" {
		t.Errorf("emuTileDisplayName = %q, want %q", got, "pcsx rearmed")
	}
	if got := emuTileDisplayName("GBA"); got != "GBA" {
		t.Errorf("emuTileDisplayName(GBA) = %q", got)
	}
}

// writeTestIconPNG writes a small solid-colour PNG so the tile renderer has a
// real image to decode.
func writeTestIconPNG(t *testing.T, path string, c color.Color) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.Set(x, y, c)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// TestEmulatorEntriesSortAvailableFirst pins the order the page depends on:
// usable emulators first, then installed-but-broken, then absent, alphabetical
// inside each group, with the pinned action rows kept above all of them.
func TestEmulatorEntriesSortAvailableFirst(t *testing.T) {
	entries := []emuEntry{
		{Name: "Zeta", Status: emuMissing},
		{Name: "Beta", Status: emuAvailable},
		{Name: "Broken", Status: emuBroken},
		{Name: "Pinned", Status: emuMissing, Pinned: true},
		{Name: "Alpha", Status: emuMissing},
		{Name: "Yoshi", Status: emuAvailable},
	}
	sortEmulatorEntries(entries)

	want := []string{"Pinned", "Beta", "Yoshi", "Broken", "Alpha", "Zeta"}
	got := make([]string, len(entries))
	for i, e := range entries {
		got[i] = e.Name
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// TestEmuStatusChipFitsEveryTile keeps the pill inside the tile at the narrowest
// supported width: text wider than its own chip is the failure mode this guards.
func TestEmuStatusChipFitsEveryTile(t *testing.T) {
	probeRenderer(t) // initialises SDL_ttf so the chip font can be opened
	font := emuStatusChipFont(&Config{})
	if font == nil {
		t.Skip("no font available in this build")
	}
	cell := sdl.Rect{X: 100, Y: 100, W: emuTileMinW, H: emuTileH}
	for _, status := range []emuStatus{emuAvailable, emuBroken, emuMissing} {
		label, _ := emuStatusLabel(status)
		if label == "" {
			t.Fatalf("status %d has no label", status)
		}
		chip := emuStatusChipRect(cell, font, label)
		if chip.X < cell.X+8 || chip.X+chip.W > cell.X+cell.W-8 {
			t.Errorf("%s chip %+v does not fit inside tile %+v", label, chip, cell)
		}
		if chip.Y < cell.Y || chip.Y+chip.H > cell.Y+cell.H-8 {
			t.Errorf("%s chip %+v overlaps the tile edge", label, chip)
		}
		if tw, _, err := font.SizeUTF8(label); err == nil && int32(tw) > chip.W-emuStatusChipPad {
			t.Errorf("%s chip is %dpx wide but its text needs %dpx", label, chip.W, tw)
		}
	}
}

// TestEmuTileStatusChipIsDrawn checks the pill is painted in the status colour,
// so availability is readable without the footer.
func TestEmuTileStatusChipIsDrawn(t *testing.T) {
	renderer := probeRenderer(t)
	cell := sdl.Rect{X: 100, Y: 100, W: emuTileMinW, H: emuTileH}

	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.Clear()
	chip := drawEmuTileStatus(renderer, &Config{}, cell, emuEntry{Name: "GBA", Status: emuAvailable}, cell.Y+84, 23)
	if chip.W == 0 {
		t.Fatal("no status chip for an available emulator")
	}

	buf := make([]byte, probeW*probeH*4)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&buf[0]), int(probeW)*4); err != nil {
		t.Skipf("renderer.ReadPixels unavailable: %v", err)
	}
	green := 0
	for y := chip.Y + 2; y < chip.Y+chip.H-2; y++ {
		for x := chip.X + 2; x < chip.X+chip.W-2; x++ {
			i := (int(y)*int(probeW) + int(x)) * 4
			if buf[i+1] > buf[i]+25 && buf[i+1] > buf[i+2]+15 {
				green++
			}
		}
	}
	if green == 0 {
		t.Errorf("no green pixels inside the available chip %+v", chip)
	}

	// The pinned action rows are doors, not emulators: no pill when they work.
	if rect := drawEmuTileStatus(nil, &Config{}, cell, emuEntry{Name: "Profiles", Pinned: true, Status: emuAvailable}, cell.Y+84, 23); rect.W != 0 {
		t.Errorf("pinned available row drew a status chip %+v", rect)
	}

	// With no room left for even one name line, the name wins and the pill is
	// dropped rather than drawn through the text (largest accessibility size).
	if rect := drawEmuTileStatus(nil, &Config{}, cell, emuEntry{Name: "GBA", Status: emuMissing}, cell.Y+84, 999); rect.W != 0 {
		t.Errorf("status chip %+v drawn over the name", rect)
	}
}

// TestEmuTileDimsUnavailablePicture is the at-a-glance half of the redesign:
// the picture of an emulator you cannot start is veiled, so the grid separates
// usable from unusable before any label is read.
func TestEmuTileDimsUnavailablePicture(t *testing.T) {
	renderer := probeRenderer(t)
	cell := sdl.Rect{X: 100, Y: 100, W: emuTileMinW, H: emuTileH}
	boxX := cell.X + (cell.W-emuIconBox)/2
	boxY := cell.Y + 12

	luma := func(status emuStatus) int {
		renderer.SetDrawColor(0, 0, 0, 255)
		renderer.Clear()
		drawEmuTile(renderer, &Config{}, cell, emuEntry{Name: "GBA", Status: status, Glyph: "gamepad"}, false, false, nil)
		buf := make([]byte, probeW*probeH*4)
		if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&buf[0]), int(probeW)*4); err != nil {
			t.Skipf("renderer.ReadPixels unavailable: %v", err)
		}
		var sum int
		for y := boxY; y < boxY+emuIconBox; y++ {
			for x := boxX; x < boxX+emuIconBox; x++ {
				i := (int(y)*int(probeW) + int(x)) * 4
				sum += int(buf[i]) + int(buf[i+1]) + int(buf[i+2])
			}
		}
		return sum
	}

	available := luma(emuAvailable)
	if missing := luma(emuMissing); missing >= available {
		t.Errorf("missing emulator icon is not dimmed: %d vs %d for available", missing, available)
	}
	if broken := luma(emuBroken); broken >= available {
		t.Errorf("unlaunchable emulator icon is not dimmed: %d vs %d for available", broken, available)
	}
}

// TestEmuTileIconShrinksForLargeText keeps the icon well from pushing the name
// and the status pill off the tile when accessibility text scaling is at its
// largest.
func TestEmuTileIconShrinksForLargeText(t *testing.T) {
	probeRenderer(t)
	cell := sdl.Rect{X: 0, Y: 0, W: emuTileMinW, H: emuTileH}
	if got := emuTileIconSize(cell, nil, 0); got != emuIconBox {
		t.Errorf("tile without a name uses icon box %d, want %d", got, emuIconBox)
	}
	small := getFontSized(&Config{}, "small", 22)
	large := getFontSized(&Config{}, "small", 32)
	chipH := emuStatusChipHeight(emuStatusChipFont(&Config{}))
	if small == nil || large == nil {
		t.Skip("no font available in this build")
	}
	if got := emuTileIconSize(cell, small, chipH); got != emuIconBox {
		t.Errorf("default text shrank the icon to %d, want %d", got, emuIconBox)
	}
	got := emuTileIconSize(cell, large, chipH)
	if got >= emuIconBox || got < emuTileMinIconBox {
		t.Errorf("large text icon box = %d, want between %d and %d", got, emuTileMinIconBox, emuIconBox)
	}
}

// TestEmuTileDrawsTheEmulatorsPicture is the point of the page: a tile with an
// icon file draws that picture, not just the glyph placeholder.
func TestEmuTileDrawsTheEmulatorsPicture(t *testing.T) {
	renderer := probeRenderer(t)
	dir := t.TempDir()
	iconPath := filepath.Join(dir, "icon.png")
	writeTestIconPNG(t, iconPath, color.RGBA{R: 255, A: 255})

	cell := sdl.Rect{X: 100, Y: 100, W: 186, H: 150}
	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.Clear()
	drawEmuTile(renderer, &Config{}, cell, emuEntry{
		Name:     "GBA",
		Status:   emuAvailable,
		IconPath: iconPath,
	}, false, false, nil)

	buf := make([]byte, probeW*probeH*4)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&buf[0]), int(probeW)*4); err != nil {
		t.Skipf("renderer.ReadPixels unavailable: %v", err)
	}
	boxX := cell.X + (cell.W-emuIconBox)/2
	boxY := cell.Y + 12
	red := 0
	for y := boxY; y < boxY+emuIconBox; y++ {
		for x := boxX; x < boxX+emuIconBox; x++ {
			i := (int(y)*int(probeW) + int(x)) * 4
			if buf[i+2] > 180 && buf[i] < 90 && buf[i+1] < 90 {
				red++
			}
		}
	}
	if red < int(emuIconBox*emuIconBox)/2 {
		t.Fatalf("only %d red pixels in the icon box; the emulator picture was not drawn", red)
	}
}

// TestEmuPanelRendersTiles renders the page offscreen and checks the click
// hit-rects cover whole tiles inside the page.
func TestEmuPanelRendersTiles(t *testing.T) {
	snapshotHomeGlobals(t)
	invalidateEmulatorCache()
	emuPanel.open = true
	emuPanel.cursor = 0
	emuPanel.scroll = 0
	t.Cleanup(func() {
		emuPanel.open = false
		invalidateEmulatorCache()
	})

	renderer := probeRenderer(t)
	renderEmuPanel(renderer, &Config{Scenes: []SceneConfig{{Name: "Main", Layout: "home"}}})

	if emuPanelRect.W <= 0 || emuPanelRect.H <= 0 {
		t.Fatalf("page rect not set: %+v", emuPanelRect)
	}
	// The redesign is a page, not a popup: it should use most of the screen.
	if emuPanelRect.W < probeW*3/4 || emuPanelRect.H < probeH*3/4 {
		t.Errorf("page %+v does not fill the screen", emuPanelRect)
	}
	layout := emuGridView()
	if layout.cols < 2 {
		t.Fatalf("tile grid has %d column(s), want a grid", layout.cols)
	}
	entries := emulatorEntries()
	wantHits := layout.cols * layout.rows
	if wantHits > len(entries) {
		wantHits = len(entries)
	}
	if len(emuRowHits) != wantHits {
		t.Fatalf("tiles rendered = %d, want %d (page shows %d columns x %d rows)",
			len(emuRowHits), wantHits, layout.cols, layout.rows)
	}
	first := emuRowHits[0].rect
	for i, hit := range emuRowHits {
		r := hit.rect
		if r.X < emuPanelRect.X || r.Y < emuPanelRect.Y ||
			r.X+r.W > emuPanelRect.X+emuPanelRect.W ||
			r.Y+r.H > emuPanelRect.Y+emuPanelRect.H {
			t.Errorf("tile rect %+v escapes the page %+v", r, emuPanelRect)
		}
		if r.W != layout.tileW || r.H != layout.tileH {
			t.Errorf("tile %d = %dx%d, want %dx%d", i, r.W, r.H, layout.tileW, layout.tileH)
		}
		if hit.index < 0 || hit.index >= len(entries) {
			t.Errorf("tile hit index %d outside the catalog of %d", hit.index, len(entries))
		}
	}
	// Neighbouring tiles are one gap apart, not overlapping rows like the old
	// list: that is what makes the hit rects match what the eye sees.
	if len(emuRowHits) > 1 && layout.cols > 1 {
		second := emuRowHits[1].rect
		if second.X != first.X+first.W+layout.gap || second.Y != first.Y {
			t.Errorf("second tile at %+v, want one gap right of %+v", second, first)
		}
	}
	// The focused tile is drawn even when the page had to scroll to it.
	focusedVisible := false
	for _, hit := range emuRowHits {
		if hit.index == emuPanel.cursor {
			focusedVisible = true
		}
	}
	if !focusedVisible {
		t.Errorf("focused tile %d is not in the visible window", emuPanel.cursor)
	}
}
