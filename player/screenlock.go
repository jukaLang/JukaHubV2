package main

import (
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Lock-screen image
//
// Files can hand the picture it is showing to the idle screen. The choice is a
// user setting, so it lives in jukauser.json, and the renderer treats it as
// best-effort: a photo that was deleted, unplugged or replaced falls back to the
// default idle screen instead of failing a frame.
// ──────────────────────────────────────────────────────────────────────

const screenlockImageKey = "screenlockImage"

// screenlockRetryInterval keeps a missing or broken file from being stat'ed and
// logged on every rendered frame. The idle screen redraws continuously and the
// handheld's SD card is slow enough that per-frame I/O is visible.
const screenlockRetryInterval = 5 * time.Second

var ssImage struct {
	path    string
	stamp   string
	texture *sdl.Texture
	width   int32
	height  int32
	failed  bool
	checked time.Time
	logged  string
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
// be a viewable image file that exists right now. It returns the absolute path
// that gets stored, so a later working-directory change cannot redirect it.
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
	return filepath.Clean(abs), nil
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
	// Drop the cached picture so the new one is decoded on the next idle frame.
	releaseScreenlockTexture()
	ssImage.path = ""
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
	// instead of the one that was just cleared.
	resetScreenlockState()
	if !had {
		return nil
	}
	saveUserConfigDebounced(userConfigFromVariables(config))
	return nil
}

// releaseScreenlockTexture frees the decoded picture but keeps the remembered
// path and failure state, which the retry and logging logic still needs.
func releaseScreenlockTexture() {
	if ssImage.texture != nil {
		ssImage.texture.Destroy()
		ssImage.texture = nil
	}
	ssImage.width, ssImage.height = 0, 0
	ssImage.stamp = ""
	ssImage.failed = false
}

// resetScreenlockState forgets the cached picture and the failure memory, so
// the next draw asks the disk again.
func resetScreenlockState() {
	releaseScreenlockTexture()
	ssImage.path = ""
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

// screenlockTexture returns the configured picture, decoded once and reused.
// A nil texture is the normal "use the default idle screen" answer, not an
// error: the caller simply draws no backdrop.
func screenlockTexture(renderer *sdl.Renderer, config *Config) (*sdl.Texture, int32, int32) {
	if renderer == nil {
		return nil, 0, 0
	}
	path := screenlockImagePath(config)
	if path == "" {
		releaseScreenlockTexture()
		ssImage.path = ""
		return nil, 0, 0
	}
	if ssImage.path != path {
		releaseScreenlockTexture()
		ssImage.path = path
	}
	// A cached picture is served without touching the disk until the recheck
	// window passes; then one stat notices a replaced or removed file.
	if ssImage.texture != nil && time.Since(ssImage.checked) < screenlockRetryInterval {
		return ssImage.texture, ssImage.width, ssImage.height
	}
	if ssImage.texture == nil && ssImage.failed && time.Since(ssImage.checked) < screenlockRetryInterval {
		return nil, 0, 0
	}
	ssImage.checked = time.Now()

	stamp := screenlockStamp(path)
	if stamp == "" {
		releaseScreenlockTexture()
		ssImage.failed = true
		if ssImage.logged != path {
			log.Printf("Screen lock image %q is unavailable; using the default idle screen", path)
			ssImage.logged = path
		}
		return nil, 0, 0
	}
	if ssImage.texture != nil && ssImage.stamp == stamp {
		return ssImage.texture, ssImage.width, ssImage.height
	}

	// The file changed (or is new): decode this version. Any failure leaves the
	// saved setting alone, so restoring the file brings the picture back.
	surface, err := img.Load(path)
	if err != nil || surface == nil || surface.W <= 0 || surface.H <= 0 {
		if surface != nil {
			surface.Free()
		}
		releaseScreenlockTexture()
		ssImage.stamp, ssImage.failed = stamp, true
		if ssImage.logged != path+stamp {
			log.Printf("Screen lock image %q cannot be decoded (%v); using the default idle screen", path, err)
			ssImage.logged = path + stamp
		}
		return nil, 0, 0
	}
	defer surface.Free()

	texture, texErr := renderer.CreateTextureFromSurface(surface)
	if texErr != nil || texture == nil {
		releaseScreenlockTexture()
		ssImage.stamp, ssImage.failed = stamp, true
		if ssImage.logged != path+stamp {
			log.Printf("Screen lock image %q cannot be uploaded (%v); using the default idle screen", path, texErr)
			ssImage.logged = path + stamp
		}
		return nil, 0, 0
	}
	ssImage.texture = texture
	ssImage.width, ssImage.height = surface.W, surface.H
	ssImage.stamp, ssImage.failed = stamp, false
	return texture, surface.W, surface.H
}

// drawScreenlockImage paints the chosen picture across the idle screen, cropped
// to fill (not letterboxed) and dimmed so the clock, quote and chat card stay
// legible. alpha follows the saver's fade, so the picture fades in with it.
func drawScreenlockImage(renderer *sdl.Renderer, config *Config, alpha uint8) {
	if renderer == nil || alpha == 0 || screenWidth <= 0 || screenHeight <= 0 {
		return
	}
	texture, w, h := screenlockTexture(renderer, config)
	if texture == nil || w <= 0 || h <= 0 {
		return
	}
	scale := math.Max(float64(screenWidth)/float64(w), float64(screenHeight)/float64(h))
	dw := int32(math.Round(float64(w) * scale))
	dh := int32(math.Round(float64(h) * scale))
	if dw <= 0 || dh <= 0 {
		return
	}

	// The scrim below relies on the renderer's blend mode being on; without
	// this an explicit alpha paints the page solid instead of tinting it.
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	_ = texture.SetBlendMode(sdl.BLENDMODE_BLEND)
	_ = texture.SetAlphaMod(alpha)
	renderer.Copy(texture, nil, &sdl.Rect{
		X: (screenWidth - dw) / 2,
		Y: (screenHeight - dh) / 2,
		W: dw,
		H: dh,
	})
	_ = texture.SetAlphaMod(255)

	// The same scrim the default saver gets from its dark page: without it a
	// bright photo swallows the clock and the rotating quote.
	renderer.SetDrawColor(6, 8, 14, uint8(float64(alpha)*0.45))
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
}
