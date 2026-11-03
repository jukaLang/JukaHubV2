package main

// The lock-screen picture is a user file, so it can disappear or be replaced
// between the moment it is chosen in Files and the moment the idle screen asks
// for it. These tests pin both halves: what Files is allowed to remember, and
// that a missing picture is a quiet fall back to the default idle screen rather
// than a failure.

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

func resetScreenlockCache(t *testing.T) {
	t.Helper()
	resetScreenlockState()
	t.Cleanup(resetScreenlockState)
}

// writeTestPNG writes a small solid-colour PNG and returns its path.
func writeTestPNG(t *testing.T, name string, c color.RGBA) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, c)
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	if err := png.Encode(file, img); err != nil {
		file.Close()
		t.Fatalf("encode %s: %v", path, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
	return path
}

func screenlockTestConfig(path string) *Config {
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{}
	if path != "" {
		config.Variables.Custom[screenlockImageKey] = path
	}
	return config
}

func TestValidateScreenlockImageRejectsNonImages(t *testing.T) {
	if _, err := validateScreenlockImage(""); err == nil {
		t.Error("an empty path was accepted")
	}
	notImage := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(notImage, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := validateScreenlockImage(notImage); err == nil {
		t.Error("a text file was accepted as a lock-screen picture")
	}
	if _, err := validateScreenlockImage(filepath.Join(t.TempDir(), "gone.png")); err == nil {
		t.Error("a missing picture was accepted")
	}

	picture := writeTestPNG(t, "photo.png", color.RGBA{R: 220, G: 40, B: 40, A: 255})
	got, err := validateScreenlockImage(picture)
	if err != nil {
		t.Fatalf("a real png was rejected: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("stored path %q is not absolute, so a directory change would redirect it", got)
	}
}

// setScreenlockImage is the only writer: it must remember the picture in the
// user config, and refuse anything the idle screen could not draw.
func TestSetScreenlockImageStoresUsablePicture(t *testing.T) {
	resetScreenlockCache(t)
	config := screenlockTestConfig("")
	picture := writeTestPNG(t, "lock.png", color.RGBA{R: 20, G: 120, B: 220, A: 255})

	if err := setScreenlockImage(config, picture); err != nil {
		t.Fatalf("setScreenlockImage: %v", err)
	}
	stored, _ := config.Variables.Custom[screenlockImageKey].(string)
	if stored == "" {
		t.Fatal("the picture was not remembered in the config")
	}
	if screenlockImagePath(config) != stored {
		t.Errorf("screenlockImagePath = %q, want the stored %q", screenlockImagePath(config), stored)
	}

	// The setting has to survive the round trip through jukauser.json, which is
	// how it comes back on the next launch.
	saved := userConfigFromVariables(config)
	reloaded := &Config{}
	reloaded.Variables.Custom = map[string]interface{}{}
	mergeUserConfig(reloaded, saved)
	if got := screenlockImagePath(reloaded); got != stored {
		t.Errorf("after a save and load the backdrop is %q, want %q", got, stored)
	}

	if err := setScreenlockImage(config, filepath.Join(t.TempDir(), "gone.png")); err == nil {
		t.Fatal("a missing picture replaced the working setting")
	}
	if screenlockImagePath(config) != stored {
		t.Errorf("a rejected picture changed the setting to %q", screenlockImagePath(config))
	}
}

// A picture that was deleted after being chosen must leave the default idle
// screen in place: no texture, no panic, and the saved setting kept so the
// picture returns if the file does.
func TestMissingScreenlockImageFallsBackToDefault(t *testing.T) {
	resetScreenlockCache(t)
	newProbeFrame(t)
	renderer := newKeyboardTestRenderer(t)
	t.Cleanup(releaseScreenlockTexture)
	withScreen(t, 320, 200)

	missing := filepath.Join(t.TempDir(), "unplugged.png")
	config := screenlockTestConfig(missing)

	texture, w, h := screenlockTexture(renderer, config)
	if texture != nil || w != 0 || h != 0 {
		t.Fatalf("a missing picture produced texture=%v %dx%d", texture, w, h)
	}
	drawScreenlockImage(renderer, config, 255)

	// The failure is remembered for a while, so the idle screen does not stat
	// (and log) the same missing file on every frame.
	before := ssImage.checked
	screenlockTexture(renderer, config)
	if !ssImage.checked.Equal(before) {
		t.Error("a remembered failure was re-checked inside the retry window")
	}
	if screenlockImagePath(config) != missing {
		t.Error("the saved setting was dropped when the file went missing")
	}
}

// The picture really is painted: a bright red photo over the dark idle page
// changes the pixels, and removing it afterwards goes back to no backdrop.
func TestScreenlockImageDrawsAsIdleBackdrop(t *testing.T) {
	resetScreenlockCache(t)
	newProbeFrame(t)
	renderer := newKeyboardTestRenderer(t)
	t.Cleanup(releaseScreenlockTexture)
	withScreen(t, 320, 200)

	picture := writeTestPNG(t, "bright.png", color.RGBA{R: 230, G: 30, B: 30, A: 255})
	config := screenlockTestConfig(picture)

	texture, w, h := screenlockTexture(renderer, config)
	if texture == nil || w == 0 || h == 0 {
		t.Fatalf("a valid png was not decoded: texture=%v %dx%d", texture, w, h)
	}

	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.Clear()
	drawScreenlockImage(renderer, config, 255)

	frame := &probeFrame{buf: make([]byte, probeW*probeH*4)}
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&frame.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("cannot read the test frame: %v", err)
	}
	c := frame.at(screenWidth/2, screenHeight/2)
	if int(c.R)+int(c.G)+int(c.B) < 120 {
		t.Fatalf("the idle backdrop stayed dark (%v); the picture was not drawn", c)
	}

	// Removing the file drops the backdrop on the next recheck rather than
	// drawing a stale or broken picture.
	if err := os.Remove(picture); err != nil {
		t.Fatal(err)
	}
	ssImage.checked = time.Now().Add(-2 * screenlockRetryInterval)
	if texture, _, _ := screenlockTexture(renderer, config); texture != nil {
		t.Fatal("a removed picture kept its texture")
	}
	drawScreenlockImage(renderer, config, 255)
}

// Clearing the lock screen must put the default idle screen back, and must not
// touch the photo: the setting is forgotten, the file is left alone.
func TestClearScreenlockImageRestoresDefaultLook(t *testing.T) {
	resetScreenlockCache(t)
	newProbeFrame(t)
	renderer := newKeyboardTestRenderer(t)
	t.Cleanup(releaseScreenlockTexture)
	withScreen(t, 320, 200)

	picture := writeTestPNG(t, "keep.png", color.RGBA{R: 40, G: 160, B: 90, A: 255})
	config := screenlockTestConfig("")
	if err := setScreenlockImage(config, picture); err != nil {
		t.Fatalf("setScreenlockImage: %v", err)
	}
	if texture, _, _ := screenlockTexture(renderer, config); texture == nil {
		t.Fatal("the picture was not decoded before it was cleared")
	}

	if err := clearScreenlockImage(config); err != nil {
		t.Fatalf("clearScreenlockImage: %v", err)
	}
	if _, still := config.Variables.Custom[screenlockImageKey]; still {
		t.Error("the setting survived the clear")
	}
	if got := screenlockImagePath(config); got != "" {
		t.Errorf("screenlockImagePath = %q after clearing, want \"\"", got)
	}
	if texture, _, _ := screenlockTexture(renderer, config); texture != nil {
		t.Error("the cleared picture kept its texture, so the idle screen would stay covered")
	}
	if _, err := os.Stat(picture); err != nil {
		t.Fatalf("clearing the lock screen removed the photo: %v", err)
	}

	// The clear has to survive the round trip through jukauser.json; otherwise
	// the next launch paints the picture again.
	saved := userConfigFromVariables(config)
	reloaded := &Config{}
	reloaded.Variables.Custom = map[string]interface{}{}
	mergeUserConfig(reloaded, saved)
	if got := screenlockImagePath(reloaded); got != "" {
		t.Errorf("after a save and load the cleared backdrop is %q", got)
	}

	// Clearing an already-default screen is a quiet no-op.
	if err := clearScreenlockImage(config); err != nil {
		t.Errorf("clearing twice returned %v", err)
	}
	if err := clearScreenlockImage(nil); err == nil {
		t.Error("clearing with no configuration loaded reported success")
	}
}

// The viewer's one lock action is a toggle: the picture on screen becomes the
// backdrop, and pressing it again (or tapping the now-"Clear" button) takes it
// back without deleting anything.
func TestToggleScreenlockFromViewerClearsCurrentPicture(t *testing.T) {
	resetScreenlockCache(t)
	prevPath, prevRemote := imageViewerPath, imageViewerRemote
	t.Cleanup(func() { imageViewerPath, imageViewerRemote = prevPath, prevRemote })

	picture := writeTestPNG(t, "viewer.png", color.RGBA{R: 200, G: 120, B: 40, A: 255})
	other := writeTestPNG(t, "other.png", color.RGBA{R: 30, G: 60, B: 200, A: 255})
	config := screenlockTestConfig("")

	imageViewerRemote = false
	imageViewerPath = picture
	toggleScreenlockFromViewer(config)
	if !screenlockImageIsCurrent(config, picture) {
		t.Fatal("the viewed picture did not become the backdrop")
	}

	toggleScreenlockFromViewer(config)
	if screenlockImagePath(config) != "" {
		t.Fatalf("a second press left the backdrop as %q, want the default screen", screenlockImagePath(config))
	}
	if _, err := os.Stat(picture); err != nil {
		t.Fatalf("toggling the lock screen off removed the photo: %v", err)
	}

	// A different picture is still a set, not a clear, and paths reached in
	// another form still count as the current backdrop.
	imageViewerPath = other
	toggleScreenlockFromViewer(config)
	if !screenlockImageIsCurrent(config, other) || screenlockImageIsCurrent(config, picture) {
		t.Fatalf("viewing a second picture did not replace the backdrop (now %q)", screenlockImagePath(config))
	}
	if !screenlockImageIsCurrent(config, filepath.Join(filepath.Dir(other), ".", "other.png")) {
		t.Error("an uncleaned path to the current backdrop was not recognised")
	}

	// A remote chat image has no file in Files, so it can neither set nor clear
	// the backdrop.
	before := screenlockImagePath(config)
	imageViewerRemote = true
	imageViewerPath = other
	toggleScreenlockFromViewer(config)
	if screenlockImagePath(config) != before {
		t.Errorf("a remote image changed the backdrop from %q to %q", before, screenlockImagePath(config))
	}
}

func TestCloseImageViewerClearsLockButton(t *testing.T) {
	prevPath, prevBtn := imageViewerPath, imageViewerLockBtn
	t.Cleanup(func() { imageViewerPath, imageViewerLockBtn = prevPath, prevBtn })

	imageViewerPath = "/photos/a.png"
	imageViewerLockBtn = sdl.Rect{X: 10, Y: 10, W: 100, H: 40}
	closeImageViewer()
	if imageViewerLockBtn.W != 0 || imageViewerLockBtn.H != 0 {
		t.Fatalf("closing left a stale lock button at %+v", imageViewerLockBtn)
	}
}
