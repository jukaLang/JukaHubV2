package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Lock-screen image
//
// Files can hand the picture it is showing to the idle screen. The choice is a
// user setting, so it lives in jukauser.json, and the renderer treats it as
// best-effort: a photo that was deleted, unplugged or replaced falls back to the
// default idle screen instead of failing a frame.
//
// Both idle screens draw it: the starfield saver and the dock, which is the idle
// screen most devices actually show (ambientMode is on by default). The dock
// used to ignore the setting entirely, which is why a picture set from Files
// "did nothing" — the photo was saved, and the screen that appeared never
// looked for it.
//
// The picture is decoded and scaled once per version and screen size by the
// worker in saver_backdrop.go; this file owns the setting, the on-disk
// bookkeeping and the one screen-sized texture uploaded from that rendition.
// ──────────────────────────────────────────────────────────────────────

const screenlockImageKey = "screenlockImage"

// screenlockRetryInterval keeps a missing or broken file from being stat'ed and
// logged on every rendered frame. The idle screen redraws continuously and the
// handheld's SD card is slow enough that per-frame I/O is visible.
const screenlockRetryInterval = 5 * time.Second

// saverLockFadeIn is how long a freshly decoded picture takes to appear. The
// worker finishes a moment after the idle screen does, and a picture that
// fades in reads as intended instead of as a pop.
const saverLockFadeIn = 600 * time.Millisecond

// ssImage is the render thread's view of the lock picture. path and stamp say
// which file version it describes; texture is the screen-sized upload of that
// version's rendition and belongs to owner.
var ssImage struct {
	path    string
	stamp   string
	texture *sdl.Texture
	owner   *sdl.Renderer
	width   int32
	height  int32
	failed  bool
	checked time.Time
	logged  string
	shownAt time.Time
}

// screenlockPathCache keeps the resolved path off the render loop. The idle
// screen asks for the picture every frame, and resolving a relative path means
// asking the OS for the working directory, which is not free on a handheld.
var screenlockPathCache struct {
	raw      string
	resolved string
}

// screenlockImagePath returns the configured backdrop, or "" when none is set.
func screenlockImagePath(config *Config) string {
	if config == nil {
		return ""
	}
	raw, _ := config.Variables.Custom[screenlockImageKey].(string)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if screenlockPathCache.raw == raw {
		return screenlockPathCache.resolved
	}
	resolved := filepath.Clean(raw)
	if !filepath.IsAbs(resolved) {
		if abs, err := filepath.Abs(resolved); err == nil {
			resolved = filepath.Clean(abs)
		}
	}
	screenlockPathCache.raw, screenlockPathCache.resolved = raw, resolved
	return resolved
}

// validateScreenlockImage checks a Files entry before it is remembered: it must
// be a picture the idle screen can actually decode, and it must exist right
// now. It returns the absolute path that gets stored, so a later
// working-directory change cannot redirect it.
func validateScreenlockImage(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("no image selected")
	}
	if !isViewableImagePath(path) {
		return "", fmt.Errorf("not an image: %s", filepath.Base(path))
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("image not found: %s", filepath.Base(path))
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("not a file: %s", filepath.Base(path))
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	abs = filepath.Clean(abs)
	// The header is read now, so a picture the idle screen could never show
	// is refused here with a reason rather than silently drawing nothing.
	if err := saverPicProbe(abs); err != nil {
		return "", err
	}
	return abs, nil
}

// setScreenlockImage remembers path as the lock-screen backdrop and saves it.
func setScreenlockImage(config *Config, path string) error {
	abs, err := validateScreenlockImage(path)
	if err != nil {
		return err
	}
	if config == nil {
		return fmt.Errorf("no configuration loaded")
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[screenlockImageKey] = abs
	// Forget the previous picture so the new one is used on the next idle
	// frame, and start decoding it now: by the time the screen goes idle the
	// rendition is usually ready, so the picture is there from the first frame.
	resetScreenlockState()
	if stamp := screenlockStamp(abs); stamp != "" && screenWidth > 0 && screenHeight > 0 {
		saverPicRequest(saverPicKey{path: abs, stamp: stamp, w: int(screenWidth), h: int(screenHeight)})
	}
	saveUserConfigDebounced(userConfigFromVariables(config))
	return nil
}

// screenlockImageIsCurrent reports whether path is the picture already assigned
// to the idle screen. Both sides are compared cleaned and absolute, so a photo
// opened through another route is still recognised as the current backdrop.
func screenlockImageIsCurrent(config *Config, path string) bool {
	current := screenlockImagePath(config)
	if current == "" || strings.TrimSpace(path) == "" {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	return filepath.Clean(abs) == current
}

// clearScreenlockImage puts the idle screen back to its default look. Only the
// setting is forgotten; the picture on the card is left exactly where it is, so
// clearing the lock screen never deletes a photo.
func clearScreenlockImage(config *Config) error {
	if config == nil {
		return fmt.Errorf("no configuration loaded")
	}
	_, had := config.Variables.Custom[screenlockImageKey]
	delete(config.Variables.Custom, screenlockImageKey)
	// Drop the decoded picture so the next idle frame draws the default screen
	// instead of the one that was just cleared, and free its rendition.
	resetScreenlockState()
	saverPicForget("")
	if !had {
		return nil
	}
	saveUserConfigDebounced(userConfigFromVariables(config))
	return nil
}

// releaseScreenlockTexture frees the uploaded picture but keeps what is known
// about the file — its version, a decode failure, the last check — which the
// retry and logging logic still needs. The rendition stays cached, so the next
// idle screen uploads it again without decoding.
func releaseScreenlockTexture() {
	if ssImage.texture != nil {
		ssImage.texture.Destroy()
	}
	ssImage.texture = nil
	ssImage.owner = nil
	ssImage.width, ssImage.height = 0, 0
	ssImage.shownAt = time.Time{}
}

// resetScreenlockState forgets the cached picture and the failure memory, so
// the next draw asks the disk again.
func resetScreenlockState() {
	releaseScreenlockTexture()
	ssImage.path = ""
	ssImage.stamp = ""
	ssImage.failed = false
	ssImage.checked = time.Time{}
	ssImage.logged = ""
}

// screenlockStamp identifies one version of the file on disk. An empty stamp
// means the file is missing or is no longer a regular file.
func screenlockStamp(path string) string {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size())
}

// saverLockLogOnce logs one line per file version and problem, so a picture
// that keeps failing is reported once rather than every few seconds.
func saverLockLogOnce(key, format string, args ...interface{}) {
	if ssImage.logged == key {
		return
	}
	ssImage.logged = key
	log.Printf(format, args...)
}

// screenlockTexture returns the configured picture as a screen-sized texture.
// A nil texture is the normal "use the default idle screen" answer, not an
// error: the picture is not set, is missing, cannot be decoded, or is still
// being decoded by the worker (it appears on a later frame).
func screenlockTexture(renderer *sdl.Renderer, config *Config) (*sdl.Texture, int32, int32) {
	if renderer == nil {
		return nil, 0, 0
	}
	path := screenlockImagePath(config)
	if path == "" {
		if ssImage.path != "" || ssImage.texture != nil {
			resetScreenlockState()
		}
		return nil, 0, 0
	}
	if ssImage.path != path {
		resetScreenlockState()
		ssImage.path = path
	}
	if ssImage.texture != nil && ssImage.owner != renderer {
		// A texture belongs to the renderer that made it. Another renderer's
		// handle is dropped, never destroyed through this one.
		ssImage.texture, ssImage.owner = nil, nil
		ssImage.width, ssImage.height = 0, 0
	}

	// The file is looked at again at most every screenlockRetryInterval: one
	// stat notices a replaced, removed or restored picture.
	now := time.Now()
	if ssImage.checked.IsZero() || now.Sub(ssImage.checked) >= screenlockRetryInterval {
		ssImage.checked = now
		stamp := screenlockStamp(path)
		if stamp == "" {
			releaseScreenlockTexture()
			ssImage.stamp = ""
			ssImage.failed = false
			saverLockLogOnce(path, "Screen lock image %q is unavailable; using the default idle screen", path)
			return nil, 0, 0
		}
		if stamp != ssImage.stamp {
			// A new version: drop the old upload and any failure of the old
			// file, so restoring or replacing the picture brings it back.
			releaseScreenlockTexture()
			ssImage.stamp = stamp
			ssImage.failed = false
		}
	}
	if ssImage.stamp == "" || ssImage.failed {
		return nil, 0, 0
	}

	w, h := screenWidth, screenHeight
	if w <= 0 || h <= 0 {
		return nil, 0, 0
	}
	if ssImage.texture != nil && ssImage.width == w && ssImage.height == h {
		return ssImage.texture, w, h
	}

	pix, done, err := saverPicRequest(saverPicKey{path: path, stamp: ssImage.stamp, w: int(w), h: int(h)})
	if !done {
		return nil, 0, 0
	}
	if err != nil {
		// The setting is left alone, so fixing or replacing the file brings
		// the picture back on the next check.
		releaseScreenlockTexture()
		ssImage.failed = true
		saverLockLogOnce(path+ssImage.stamp, "Screen lock image %q cannot be decoded (%v); using the default idle screen", path, err)
		return nil, 0, 0
	}
	texture, upErr := saverPicTexture(renderer, pix)
	if upErr != nil {
		releaseScreenlockTexture()
		ssImage.failed = true
		saverLockLogOnce(path+ssImage.stamp, "Screen lock image %q cannot be uploaded (%v); using the default idle screen", path, upErr)
		return nil, 0, 0
	}
	releaseScreenlockTexture()
	ssImage.texture, ssImage.owner = texture, renderer
	ssImage.width, ssImage.height = w, h
	ssImage.shownAt = now
	return texture, w, h
}

// saverBackdropFading reports whether the lock picture is still fading in, so
// the idle frame budget keeps the fade smooth instead of stepping through it.
func saverBackdropFading(now time.Time) bool {
	return ssImage.texture != nil && !ssImage.shownAt.IsZero() && now.Sub(ssImage.shownAt) < saverLockFadeIn
}

// saverLockShowing reports whether the idle screen should treat the
// lock picture as its backdrop this frame: one is set, and it is neither
// missing nor undecodable (it may still be on its way from the worker). The
// starfield and the quote stay out of a photo's way; a broken picture leaves
// them exactly as the default screen has them.
func saverLockShowing(config *Config) bool {
	path := screenlockImagePath(config)
	return path != "" && ssImage.path == path && ssImage.stamp != "" && !ssImage.failed
}

// drawScreenlockImage paints the chosen picture across the whole idle screen.
// The rendition is already cover-cropped to the screen and carries its own
// legibility scrim, so this is one copy; alpha follows the caller's fade, and a
// picture that has only just been decoded fades in on top of it. It reports
// whether a picture was drawn.
func drawScreenlockImage(renderer *sdl.Renderer, config *Config, alpha uint8) bool {
	if renderer == nil || alpha == 0 || screenWidth <= 0 || screenHeight <= 0 {
		return false
	}
	texture, w, h := screenlockTexture(renderer, config)
	if texture == nil || w <= 0 || h <= 0 {
		return false
	}
	a := saverFadeIn(ssImage.shownAt, time.Now(), saverLockFadeIn)
	if alpha < a {
		a = alpha
	}
	if a == 0 {
		return true
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	_ = texture.SetAlphaMod(a)
	renderer.Copy(texture, nil, &sdl.Rect{X: 0, Y: 0, W: w, H: h})
	_ = texture.SetAlphaMod(255)
	return true
}
