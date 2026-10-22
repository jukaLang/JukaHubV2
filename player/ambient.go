package main

// ambient.go — the dock and ambient screen.
//
// A handheld that is charging or left alone should not sit on the launcher.
// This mode turns it into a bedside/dock display: a large clock, today's
// weather, the track that is playing and a slideshow of photos, all drawn over
// one calm screen. It is entered automatically while charging (after a short
// quiet period) or after the normal idle timeout, and can be opened from the
// Start menu at any time.
//
// Two things make it useful beyond a pretty screen:
//
//   - A sleep timer (15/30/60/90 minutes) that stops playback and darkens the
//     panel, so an audiobook does not play all night.
//   - A screen-off mode that blanks the panel immediately while audio keeps
//     decoding. The main loop draws nothing at all in that state, so the only
//     work left is the audio thread — the point of leaving music on overnight.
//
// SDL is not thread-safe, so the auto-entry monitor only flips flags under a
// mutex; every texture and pixel is touched on the render thread.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	// ambientDockIdleTimeout is how long a charging device waits without input
	// before the dock screen appears. Short enough to feel automatic, long
	// enough that plugging in while reading does not steal the screen.
	ambientDockIdleTimeout = 45 * time.Second
	// ambientPhotoInterval is the dwell time for one slideshow photo.
	ambientPhotoInterval = 20 * time.Second
	// ambientPhotoFade is how long a newly shown photo takes to fade in.
	ambientPhotoFade = 900 * time.Millisecond
	// ambientPhotoRescan bounds directory scans so a slideshow never turns into
	// constant SD-card traffic.
	ambientPhotoRescan = 5 * time.Minute
	// ambientStatusRefresh throttles the battery sysfs read.
	ambientStatusRefresh = 5 * time.Second
	// ambientPhotoLimit caps how many files are listed from one folder.
	ambientPhotoLimit = 300
)

// ambientSleepOptions is the order the sleep chip steps through.
var ambientSleepOptions = []time.Duration{
	0,
	15 * time.Minute,
	30 * time.Minute,
	60 * time.Minute,
	90 * time.Minute,
}

// ──────────────────────────────────────────────────────────────────────
// State
// ──────────────────────────────────────────────────────────────────────

type ambientState struct {
	mu         sync.Mutex
	active     bool
	screenOff  bool
	blackFrame bool // one black frame is still owed to the panel
	entered    time.Time
	sleepUntil time.Time
	sleepGen   int
}

var ambient ambientState

// ambientPhotoState is owned by the render thread. Keeping textures out of the
// mutex-guarded state makes the locking rule trivial: the monitor goroutine
// never touches a texture, and the renderer never takes the state lock.
type ambientPhotoState struct {
	files       []string
	index       int
	changedAt   time.Time
	texture     *sdl.Texture
	texturePath string
	failed      map[string]bool
	scannedAt   time.Time
	scanDir     string
}

var ambientPhotos ambientPhotoState

type ambientAction int

const (
	ambientActionPlayPause ambientAction = iota
	ambientActionPrev
	ambientActionNext
	ambientActionSleep
	ambientActionScreenOff
	ambientActionExit
)

type ambientButton struct {
	action ambientAction
	rect   sdl.Rect
}

var ambientButtons []ambientButton

// ──────────────────────────────────────────────────────────────────────
// Settings and entry decisions
// ──────────────────────────────────────────────────────────────────────

// ambientModeEnabled reports whether the dock screen replaces the classic
// screensaver. It is on by default: the feature needs no setup, and the photo
// folder is optional. Setting "ambientMode": false in the user config restores
// the starfield screensaver.
func ambientModeEnabled(config *Config) bool {
	return ambientCustomBool(config, "ambientMode", true)
}

// ambientCustomBool reads an optional boolean from the user config. Missing
// keys and unparseable values fall back to def, so a typo never turns a display
// off by accident.
func ambientCustomBool(config *Config, key string, def bool) bool {
	if config == nil {
		return def
	}
	v, ok := customValue(config.Variables.Custom, key)
	if !ok {
		return def
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true", "1", "on", "yes":
			return true
		case "false", "0", "off", "no":
			return false
		}
		return def
	case float64:
		return t != 0
	default:
		return def
	}
}

// ambientShouldEnter is the pure auto-entry decision. On power it waits for the
// dock timeout; on battery it waits for the normal idle timeout, so unplugged
// use still gets the clock after the screen has been idle long enough.
func ambientShouldEnter(charging bool, idle, dockIdle, idleTimeout time.Duration) bool {
	if idleTimeout > 0 && idle >= idleTimeout {
		return true
	}
	return charging && dockIdle > 0 && idle >= dockIdle
}

// ambientEntryBlocked reports whether some other UI owns the screen right now.
// Auto-entry must never cover a modal, a game or a full-screen video.
func ambientEntryBlocked(config *Config) bool {
	if config == nil {
		return true
	}
	if onboarding.active || qs.open || updatePrompt.open || companion.open ||
		shortcutsOpen || clipSearchOpen || globalSearchOpen || virtualKeyboardActive ||
		toolsPanel.open || imageViewerPath != "" || patchModal != nil ||
		transitionPhase != "none" || videoFullscreen || playerPhase() != "idle" ||
		nc.open || tb.startOpen || processPanel.open || wifiPanel.open || ipPanelOpen || emuPanel.open || gameLib.open ||
		gpPanel.open || winAppsPanel.open || portsPanel.open || savePanel.open || curationPanel.open || chiakiPanel.open ||
		raPanel.open || a11y.open || wpOpen || socialNotifOpen || smartHomePanel.open {
		return true
	}
	return retroArchRunning()
}

// ──────────────────────────────────────────────────────────────────────
// Lifecycle
// ──────────────────────────────────────────────────────────────────────

func ambientIsActive() bool {
	ambient.mu.Lock()
	defer ambient.mu.Unlock()
	return ambient.active
}

func ambientIsScreenOff() bool {
	ambient.mu.Lock()
	defer ambient.mu.Unlock()
	return ambient.active && ambient.screenOff
}

// ambientEnter opens the dock screen. It is safe to call from the monitor
// goroutine: nothing here touches SDL.
func ambientEnter(config *Config) {
	ambient.mu.Lock()
	if ambient.active {
		ambient.mu.Unlock()
		return
	}
	ambient.active = true
	ambient.screenOff = false
	ambient.blackFrame = false
	ambient.entered = time.Now()
	ambient.mu.Unlock()

	// The starfield screensaver and the dock screen are two answers to the same
	// question; never let both be on.
	deactivateScreensaver()
	noteUserInputTime(time.Now())
}

// ambientLeave returns to the normal UI and drops the photo texture.
func ambientLeave() {
	ambient.mu.Lock()
	if !ambient.active {
		ambient.mu.Unlock()
		return
	}
	ambient.active = false
	ambient.screenOff = false
	ambient.blackFrame = false
	ambient.sleepGen++ // an armed timer must not fire after leaving
	ambient.sleepUntil = time.Time{}
	ambient.mu.Unlock()

	ambientFreePhoto()
	ambientButtons = nil
	noteUserInputTime(time.Now())
}

// ambientResetState clears every flag without replacing the mutex, so tests and
// a future "reset settings" path can return to a known state safely.
func ambientResetState() {
	ambient.mu.Lock()
	ambient.active = false
	ambient.screenOff = false
	ambient.blackFrame = false
	ambient.entered = time.Time{}
	ambient.sleepUntil = time.Time{}
	ambient.sleepGen++
	ambient.mu.Unlock()
}

// ambientNotifyInput wakes a darked panel. Any real input calls this through
// screensaverNotifyInput, so the same key that dims the screen brightens it.
func ambientNotifyInput() {
	ambient.mu.Lock()
	woke := ambient.active && ambient.screenOff
	if woke {
		ambient.screenOff = false
		ambient.blackFrame = false
	}
	ambient.mu.Unlock()
	if woke {
		noteUserInputTime(time.Now())
	}
}

// ambientDarken blanks the panel immediately. Audio is untouched: decoding runs
// on its own goroutine and the main loop simply stops drawing.
func ambientDarken() {
	ambient.mu.Lock()
	if !ambient.active {
		ambient.mu.Unlock()
		return
	}
	ambient.screenOff = true
	ambient.blackFrame = true
	ambient.mu.Unlock()
}

// ambientNeedsBlackFrame reports whether the main loop still has to paint the
// panel black before it can stop presenting frames.
func ambientNeedsBlackFrame() bool {
	ambient.mu.Lock()
	defer ambient.mu.Unlock()
	return ambient.active && ambient.screenOff && ambient.blackFrame
}

func ambientMarkBlackFrameDrawn() {
	ambient.mu.Lock()
	ambient.blackFrame = false
	ambient.mu.Unlock()
}

// ──────────────────────────────────────────────────────────────────────
// Sleep timer
// ──────────────────────────────────────────────────────────────────────

// ambientSleepCycle picks the next preset. It is driven by the remaining time
// rather than an exact match, because a timer armed two minutes ago no longer
// equals its preset: off -> 15 -> 30 -> 60 -> 90 -> off.
func ambientSleepCycle(remaining time.Duration, options []time.Duration) time.Duration {
	if len(options) == 0 {
		return 0
	}
	step := 0
	for i := 1; i < len(options); i++ {
		if remaining > 0 && remaining <= options[i] {
			step = i
			break
		}
	}
	return options[(step+1)%len(options)]
}

// ambientArmSleep arms (or cancels, with d <= 0) the sleep timer. When it fires
// it stops playback first and then darkens the screen, so a bedtime book ends
// silently instead of playing until the battery dies.
func ambientArmSleep(d time.Duration) {
	ambient.mu.Lock()
	ambient.sleepGen++
	gen := ambient.sleepGen
	if d <= 0 {
		ambient.sleepUntil = time.Time{}
		ambient.mu.Unlock()
		return
	}
	ambient.sleepUntil = time.Now().Add(d)
	ambient.mu.Unlock()

	go func() {
		timer := time.NewTimer(d)
		defer timer.Stop()
		<-timer.C
		ambient.mu.Lock()
		valid := gen == ambient.sleepGen && ambient.active
		if valid {
			ambient.sleepUntil = time.Time{}
			ambient.screenOff = true
			ambient.blackFrame = true
		}
		ambient.mu.Unlock()
		if !valid {
			return
		}
		// No toast: the panel is about to be black, and the audio stopping is
		// the feedback. This also keeps the audio/notification paths off this
		// goroutine.
		musicStop()
	}()
}

func ambientSleepRemaining() time.Duration {
	ambient.mu.Lock()
	defer ambient.mu.Unlock()
	if ambient.sleepUntil.IsZero() {
		return 0
	}
	left := time.Until(ambient.sleepUntil)
	if left < 0 {
		return 0
	}
	return left
}

// ambientCycleSleep steps the timer and reports the armed duration (0 = off).
func ambientCycleSleep() time.Duration {
	next := ambientSleepCycle(ambientSleepRemaining(), ambientSleepOptions)
	ambientArmSleep(next)
	return next
}

// ──────────────────────────────────────────────────────────────────────
// Photos
// ──────────────────────────────────────────────────────────────────────

// ambientPhotosDir picks the slideshow folder: the user's "ambientPhotos"
// setting when present, otherwise the usual Photos/Pictures locations.
func ambientPhotosDir(config *Config) string {
	if config != nil {
		if v, ok := customString(config.Variables.Custom, "ambientPhotos"); ok {
			if p, err := cleanUserPath(v); err == nil {
				return p
			}
		}
	}
	candidates := []string{"/mnt/SDCARD/Photos", "/mnt/SDCARD/Pictures"}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates, filepath.Join(home, "Pictures"), filepath.Join(home, "Pictures", "JukaHub"))
	}
	for _, dir := range candidates {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	return ""
}

// ambientPhotoFiles lists the decodable images in dir, sorted so the slideshow
// order is stable between launches. Subdirectories and hidden files are skipped.
func ambientPhotoFiles(dir string) []string {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if !isImageExtension(filepath.Ext(entry.Name())) {
			continue
		}
		files = append(files, filepath.Join(dir, entry.Name()))
		if len(files) >= ambientPhotoLimit {
			break
		}
	}
	sort.Strings(files)
	return files
}

// ambientRefreshPhotos rescans the folder at most every ambientPhotoRescan, and
// more often while nothing was found so adding the first photo is noticed.
func ambientRefreshPhotos(config *Config, now time.Time) {
	dir := ambientPhotosDir(config)
	age := now.Sub(ambientPhotos.scannedAt)
	if dir == ambientPhotos.scanDir && age < ambientPhotoRescan &&
		(len(ambientPhotos.files) > 0 || age < 30*time.Second) {
		return
	}
	ambientPhotos.scanDir = dir
	ambientPhotos.scannedAt = now
	ambientPhotos.files = ambientPhotoFiles(dir)
	ambientPhotos.failed = make(map[string]bool)
	if len(ambientPhotos.files) == 0 {
		ambientPhotos.index = 0
		ambientPhotos.changedAt = now
		return
	}
	if ambientPhotos.index >= len(ambientPhotos.files) {
		ambientPhotos.index = 0
	}
	ambientPhotos.changedAt = now
}

// ambientPhotoTexture returns the texture for the current photo, advancing the
// slideshow on schedule and fading each new photo in. It must be called on the
// render thread.
func ambientPhotoTexture(renderer *sdl.Renderer, now time.Time) *sdl.Texture {
	if renderer == nil || len(ambientPhotos.files) == 0 {
		return nil
	}
	if ambientPhotos.index >= len(ambientPhotos.files) || ambientPhotos.index < 0 {
		ambientPhotos.index = 0
	}
	if !ambientPhotos.changedAt.IsZero() && now.Sub(ambientPhotos.changedAt) >= ambientPhotoInterval {
		ambientPhotos.index = (ambientPhotos.index + 1) % len(ambientPhotos.files)
		ambientPhotos.changedAt = now
	}
	if ambientPhotos.changedAt.IsZero() {
		ambientPhotos.changedAt = now
	}

	// Loading can fail (a corrupt JPEG, a codec missing from the runtime). Walk
	// on to the next photo instead of retrying the same broken file every frame.
	for attempts := 0; attempts < len(ambientPhotos.files); attempts++ {
		path := ambientPhotos.files[ambientPhotos.index]
		if ambientPhotos.texture != nil && ambientPhotos.texturePath == path {
			return ambientPhotos.texture
		}
		if ambientPhotos.failed[path] {
			ambientPhotos.index = (ambientPhotos.index + 1) % len(ambientPhotos.files)
			continue
		}
		if ambientPhotos.texture != nil {
			ambientPhotos.texture.Destroy()
			ambientPhotos.texture = nil
			ambientPhotos.texturePath = ""
		}
		texture, err := img.LoadTexture(renderer, path)
		if err != nil {
			if ambientPhotos.failed == nil {
				ambientPhotos.failed = make(map[string]bool)
			}
			ambientPhotos.failed[path] = true
			ambientPhotos.index = (ambientPhotos.index + 1) % len(ambientPhotos.files)
			continue
		}
		_ = texture.SetBlendMode(sdl.BLENDMODE_BLEND)
		ambientPhotos.texture = texture
		ambientPhotos.texturePath = path
		return texture
	}
	return nil
}

func ambientFreePhoto() {
	if ambientPhotos.texture != nil {
		ambientPhotos.texture.Destroy()
		ambientPhotos.texture = nil
	}
	ambientPhotos.texturePath = ""
	ambientPhotos.files = nil
	ambientPhotos.index = 0
	ambientPhotos.failed = nil
	ambientPhotos.scannedAt = time.Time{}
	ambientPhotos.scanDir = ""
}

// ambientPhotoAlpha is the fade-in alpha for a photo that changed at changedAt.
func ambientPhotoAlpha(changedAt, now time.Time) uint8 {
	if changedAt.IsZero() {
		return 255
	}
	elapsed := now.Sub(changedAt)
	if elapsed <= 0 {
		return 0
	}
	if elapsed >= ambientPhotoFade {
		return 255
	}
	return uint8(255 * elapsed / ambientPhotoFade)
}

// ──────────────────────────────────────────────────────────────────────
// Read-only data for the screen
// ──────────────────────────────────────────────────────────────────────

// ambientNowPlaying snapshots the audio player without holding its lock during
// drawing.
func ambientNowPlaying() (title string, position, duration float64, playing, paused bool) {
	music.mu.Lock()
	defer music.mu.Unlock()
	playing, paused = music.playing, music.paused
	if music.current >= 0 && music.current < len(music.tracks) {
		title = music.tracks[music.current].Name
	}
	position, duration = music.position, music.duration
	return
}

// ambientProgressFraction is the played fraction for the progress bar (0 when
// the duration is still unknown).
func ambientProgressFraction(position, duration float64) float64 {
	if duration <= 0 || position <= 0 {
		return 0
	}
	f := position / duration
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

// formatAmbientTemps renders today's high/low for the weather chip.
func formatAmbientTemps(day WeatherDay, unit string) string {
	return fmt.Sprintf("%.0f° / %.0f°%s",
		weatherTemp(day.TMax, unit), weatherTemp(day.TMin, unit), unit)
}

// ambientWeather returns the condition and temperature line, empty when the
// dock forecast is switched off. The dock reads the same forecast the header
// does but is not tied to the header chip's visibility: a user who keeps the
// top bar minimal still gets weather on the dock, and ambientWeather=false
// hides it here without touching the rest of the UI.
func ambientWeather(config *Config) (string, string) {
	if !ambientCustomBool(config, "ambientWeather", true) {
		return "", ""
	}
	wxMutex.Lock()
	defer wxMutex.Unlock()
	if !weatherReady || len(weatherDaily) == 0 {
		return "Weather", "loading…"
	}
	day := weatherDaily[0]
	return weatherCodeDesc(day.Code), formatAmbientTemps(day, weatherUnit(config))
}

type ambientStatus struct {
	battery  int
	charging bool
}

var ambientStatusCache struct {
	mu   sync.Mutex
	at   time.Time
	last ambientStatus
}

// ambientReadStatus reads the battery at most every ambientStatusRefresh.
func ambientReadStatus(now time.Time) ambientStatus {
	ambientStatusCache.mu.Lock()
	defer ambientStatusCache.mu.Unlock()
	if !ambientStatusCache.at.IsZero() && now.Sub(ambientStatusCache.at) < ambientStatusRefresh {
		return ambientStatusCache.last
	}
	ambientStatusCache.at = now
	ambientStatusCache.last = ambientStatus{
		battery:  getBatteryPercent(),
		charging: getBatteryCharging(),
	}
	return ambientStatusCache.last
}

// ──────────────────────────────────────────────────────────────────────
// Input
// ──────────────────────────────────────────────────────────────────────

// ambientHandleKey consumes keyboard input while the dock screen is up. The
// first key after the panel was darkened only wakes it; the rest act on the
// visible chips.
func ambientHandleKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || !ambientIsActive() {
		return false
	}
	if e.Type != sdl.KEYDOWN {
		return true
	}
	if ambientIsScreenOff() {
		ambientNotifyInput()
		return true
	}
	switch e.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		ambientLeave()
	case sdl.K_RETURN, sdl.K_SPACE, sdl.K_a, sdl.K_p:
		ambientTogglePlayback()
	case sdl.K_LEFT:
		musicPrev()
	case sdl.K_RIGHT:
		musicNext()
	case sdl.K_UP:
		musicAdjustVolume(0.05)
	case sdl.K_DOWN:
		musicAdjustVolume(-0.05)
	case sdl.K_y, sdl.K_s:
		ambientCycleSleep()
	case sdl.K_x:
		ambientDarken()
	case sdl.K_n:
		ambientNextPhoto()
	}
	return true
}

func ambientNextPhoto() {
	if len(ambientPhotos.files) == 0 {
		return
	}
	ambientPhotos.index = (ambientPhotos.index + 1) % len(ambientPhotos.files)
	ambientPhotos.changedAt = time.Now()
}

func ambientTogglePlayback() {
	_, _, _, playing, paused := ambientNowPlaying()
	if !playing && !paused {
		// Nothing loaded: start the user's music folder so the chip is not a
		// dead end on a fresh device.
		if dir := musicDefaultDir(appConfig); dir != "" {
			if idx := musicLoadPlaylist(dir, ""); idx >= 0 {
				musicPlayTrack(idx)
			}
		}
		return
	}
	musicTogglePause()
}

// ambientHandleController consumes controller input while the dock screen is up.
func ambientHandleController(e *sdl.ControllerButtonEvent) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN || !ambientIsActive() {
		return false
	}
	if ambientIsScreenOff() {
		ambientNotifyInput()
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_A:
		ambientTogglePlayback()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		ambientLeave()
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		musicPrev()
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT, sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		musicNext()
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		musicAdjustVolume(0.05)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		musicAdjustVolume(-0.05)
	case sdl.CONTROLLER_BUTTON_X:
		ambientDarken()
	case sdl.CONTROLLER_BUTTON_Y:
		ambientCycleSleep()
	case sdl.CONTROLLER_BUTTON_START:
		ambientLeave()
	}
	return true
}

func ambientActivate(action ambientAction) {
	switch action {
	case ambientActionPlayPause:
		ambientTogglePlayback()
	case ambientActionPrev:
		musicPrev()
	case ambientActionNext:
		musicNext()
	case ambientActionSleep:
		ambientCycleSleep()
	case ambientActionScreenOff:
		ambientDarken()
	case ambientActionExit:
		ambientLeave()
	}
}

// ambientHandleClick handles taps on the dock screen: chips act, anything else
// only wakes a darked panel. It reports whether the click was consumed.
func ambientHandleClick(mx, my int32) bool {
	if !ambientIsActive() {
		return false
	}
	if ambientIsScreenOff() {
		ambientNotifyInput()
		return true
	}
	for _, b := range ambientButtons {
		if b.rect.W > 0 && pointInRect(mx, my, b.rect) {
			ambientActivate(b.action)
			return true
		}
	}
	return true
}

// ──────────────────────────────────────────────────────────────────────
// Rendering
// ──────────────────────────────────────────────────────────────────────

// renderAmbient draws one frame of the dock screen. It returns true while
// active so the caller can skip the scene; the screen-off state is handled
// before this is reached.
func renderAmbient(renderer *sdl.Renderer, config *Config) bool {
	if renderer == nil || !ambientIsActive() {
		return false
	}
	now := time.Now()
	ambientButtons = ambientButtons[:0]
	ambientRefreshPhotos(config, now)
	ambientDrawBackdrop(renderer, config, now)

	// The dock clock is sized to the panel rather than to a config role: it has
	// to be readable across a room, and "big" is only 52 px on a Smart Pro.
	clockFont := getFontSized(config, "big", ambientClockSize())
	mediumFont, _ := getCachedFont(config, "medium")
	smallFont, _ := getCachedFont(config, "small")
	if mediumFont == nil {
		mediumFont = smallFont
	}
	if smallFont == nil {
		smallFont = mediumFont
	}

	// Clock and date, top-left.
	labelY := int32(36)
	if clockFont != nil {
		clock := now.Format("15:04")
		_, clockH := renderText(renderer, config, clockFont, clock,
			sdl.Color{R: 245, G: 248, B: 255, A: 255}, 56, 20)
		labelY = 20 + clockH + 8
	}
	if smallFont != nil {
		date := now.Format("Monday, 2 January")
		renderText(renderer, config, smallFont, date, sdl.Color{R: 176, G: 190, B: 214, A: 220}, 60, labelY)
		if next := alarmNextUpcoming(); next != nil {
			label := fmt.Sprintf("Alarm %02d:%02d", next.Hour, next.Minute)
			renderText(renderer, config, smallFont, label, sdl.Color{R: 150, G: 200, B: 160, A: 200}, 60, labelY+28)
		}
	}

	// Weather, top-right.
	if mediumFont != nil {
		desc, temps := ambientWeather(config)
		if desc != "" {
			dw, _, _ := mediumFont.SizeUTF8(desc)
			renderText(renderer, config, mediumFont, desc, sdl.Color{R: 228, G: 236, B: 250, A: 235},
				screenWidth-56-int32(dw), 44)
			if smallFont != nil {
				tw, _, _ := smallFont.SizeUTF8(temps)
				renderText(renderer, config, smallFont, temps, sdl.Color{R: 168, G: 184, B: 210, A: 210},
					screenWidth-56-int32(tw), 76)
			}
		}
	}

	ambientDrawNowPlaying(renderer, config, smallFont, mediumFont)
	ambientDrawStatus(renderer, config, smallFont, now)
	// Messages that arrive while the dock is up are readable here too, the same
	// card the starfield saver shows.
	ambientDrawChat(renderer, config, now)
	ambientDrawButtons(renderer, config, smallFont)

	return true
}

// ambientDrawBackdrop paints the photo (cover-fit, fading in) or the theme
// gradient, then a scrim so the text stays readable over bright pictures.
func ambientDrawBackdrop(renderer *sdl.Renderer, config *Config, now time.Time) {
	texture := ambientPhotoTexture(renderer, now)
	drewPhoto := false
	if texture != nil {
		if _, _, tw, th, err := texture.Query(); err == nil && tw > 0 && th > 0 {
			scale := float64(screenWidth) / float64(tw)
			if s := float64(screenHeight) / float64(th); s > scale {
				scale = s
			}
			dw := int32(float64(tw) * scale)
			dh := int32(float64(th) * scale)
			dst := sdl.Rect{X: (screenWidth - dw) / 2, Y: (screenHeight - dh) / 2, W: dw, H: dh}
			_ = texture.SetAlphaMod(ambientPhotoAlpha(ambientPhotos.changedAt, now))
			if err := renderer.Copy(texture, nil, &dst); err == nil {
				drewPhoto = true
			}
			_ = texture.SetAlphaMod(255)
		}
	}

	if !drewPhoto {
		renderer.SetDrawColor(7, 9, 16, 255)
		renderer.Clear()
		ensureGradientTexture(renderer, config, ColorBackground)
		if gradientTexture != nil {
			_ = renderer.Copy(gradientTexture, nil, &sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
		}
		// A soft accent wash gives the empty backdrop some depth without an
		// animation loop the device has to keep feeding.
		renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 16)
		renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth / 2, H: screenHeight / 2})
	}

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(5, 7, 12, 110)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	// A cached fade under the chips, instead of a hard band: the controls need
	// contrast, but the photo must not appear to end in a straight line across
	// the panel. Building it costs one surface at startup-size changes only.
	if scrim := ambientScrimTexture(renderer); scrim != nil {
		bandH := screenHeight * 2 / 5
		_ = renderer.Copy(scrim, nil, &sdl.Rect{X: 0, Y: screenHeight - bandH, W: screenWidth, H: bandH})
	}
}

// ambientScrim caches the vertical fade under the chips. Rebuilding it every
// frame would be both slower and noisier (banding) than one texture.
var ambientScrim struct {
	texture *sdl.Texture
	w, h    int32
}

func ambientScrimTexture(renderer *sdl.Renderer) *sdl.Texture {
	bandH := screenHeight * 2 / 5
	if bandH < 8 {
		bandH = 8
	}
	if ambientScrim.texture != nil && ambientScrim.w == screenWidth && ambientScrim.h == bandH {
		return ambientScrim.texture
	}
	if ambientScrim.texture != nil {
		ambientScrim.texture.Destroy()
		ambientScrim.texture = nil
	}
	surface, err := sdl.CreateRGBSurfaceWithFormat(0, screenWidth, bandH, 32, sdl.PIXELFORMAT_RGBA8888)
	if err != nil {
		return nil
	}
	defer surface.Free()
	for y := int32(0); y < bandH; y++ {
		t := float64(y) / float64(bandH)
		a := uint32(190 * t * t) // ease-in keeps the top edge invisible
		c := uint32(5) | uint32(7)<<8 | uint32(12)<<16 | a<<24
		surface.FillRect(&sdl.Rect{X: 0, Y: y, W: screenWidth, H: 1}, c)
	}
	texture, err := renderer.CreateTextureFromSurface(surface)
	if err != nil {
		return nil
	}
	_ = texture.SetBlendMode(sdl.BLENDMODE_BLEND)
	ambientScrim.texture = texture
	ambientScrim.w, ambientScrim.h = screenWidth, bandH
	return texture
}

// ambientClockSize scales the dock clock to the panel, bounded so it still fits
// above the now-playing card on a short screen.
func ambientClockSize() int {
	size := int(screenHeight) / 5
	if size < 56 {
		size = 56
	}
	if size > 168 {
		size = 168
	}
	return size
}

func ambientDrawNowPlaying(renderer *sdl.Renderer, config *Config, smallFont, mediumFont *ttf.Font) {
	if smallFont == nil {
		return
	}
	title, pos, dur, playing, paused := ambientNowPlaying()
	cardW := int32(430)
	if cardW > screenWidth-112 {
		cardW = screenWidth - 112
	}
	cardH := int32(92)
	cardX := int32(56)
	cardY := screenHeight - 176

	fillRoundedRect(renderer, cardX, cardY, cardW, cardH, 16, sdl.Color{R: 12, G: 16, B: 26, A: 190})
	strokeRoundedRect(renderer, cardX, cardY, cardW, cardH, 16, 1, sdl.Color{R: 255, G: 255, B: 255, A: 28})

	label := "Nothing playing"
	col := sdl.Color{R: 150, G: 162, B: 184, A: 200}
	if title != "" {
		label = title
		col = sdl.Color{R: 238, G: 243, B: 252, A: 240}
	}
	if mediumFont != nil {
		renderText(renderer, config, mediumFont, truncateRunes(label, 34), col, cardX+18, cardY+12)
	}
	state := ""
	switch {
	case paused:
		state = "Paused"
	case playing:
		state = "Playing"
	}
	if state != "" {
		sw, _, _ := smallFont.SizeUTF8(state)
		renderText(renderer, config, smallFont, state, sdl.Color{R: 140, G: 170, B: 255, A: 210},
			cardX+cardW-18-int32(sw), cardY+14)
	}

	// Progress bar and time.
	barX := cardX + 18
	barY := cardY + 54
	barW := cardW - 36
	barH := int32(5)
	fillRoundedRect(renderer, barX, barY, barW, barH, barH/2, sdl.Color{R: 255, G: 255, B: 255, A: 30})
	if frac := ambientProgressFraction(pos, dur); frac > 0 {
		w := int32(float64(barW) * frac)
		if w < barH {
			w = barH
		}
		fillRoundedRect(renderer, barX, barY, w, barH, barH/2,
			sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 230})
	}
	if smallFont != nil {
		timing := "0:00"
		if dur > 0 {
			timing = fmt.Sprintf("%s / %s", ambientTimeText(pos), ambientTimeText(dur))
		}
		renderText(renderer, config, smallFont, timing, sdl.Color{R: 150, G: 162, B: 184, A: 190},
			barX, barY+12)
	}
}

func ambientTimeText(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	total := int(seconds)
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

func ambientDrawStatus(renderer *sdl.Renderer, config *Config, smallFont *ttf.Font, now time.Time) {
	if smallFont == nil {
		return
	}
	status := ambientReadStatus(now)
	line := "Battery unknown"
	if status.battery >= 0 {
		line = fmt.Sprintf("Battery %d%%", status.battery)
	}
	if status.charging {
		line += " · Charging"
	}
	lw, _, _ := smallFont.SizeUTF8(line)
	renderText(renderer, config, smallFont, line, sdl.Color{R: 184, G: 196, B: 216, A: 215},
		screenWidth-56-int32(lw), screenHeight-176)

	if remaining := ambientSleepRemaining(); remaining > 0 {
		sleep := "Sleep in " + ambientTimeText(remaining.Round(time.Second).Seconds())
		sw, _, _ := smallFont.SizeUTF8(sleep)
		renderText(renderer, config, smallFont, sleep, sdl.Color{R: 190, G: 170, B: 140, A: 210},
			screenWidth-56-int32(sw), screenHeight-150)
	}
	if len(ambientPhotos.files) > 0 {
		hint := fmt.Sprintf("Photo %d/%d", ambientPhotos.index+1, len(ambientPhotos.files))
		hw, _, _ := smallFont.SizeUTF8(hint)
		renderText(renderer, config, smallFont, hint, sdl.Color{R: 140, G: 152, B: 174, A: 160},
			screenWidth-56-int32(hw), screenHeight-124)
	}
}

// ambientDrawChat keeps the newest incoming messages visible on the dock. It
// sits above the now-playing card so the two never overlap, and fades with the
// dock's own text rather than the saver's fade-in.
func ambientDrawChat(renderer *sdl.Renderer, config *Config, now time.Time) {
	notes := screensaverChatNotes(now, ssChatShown)
	if len(notes) == 0 {
		return
	}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	lineH := int32(font.Height()) + 6
	cardH := int32(38) + lineH*int32(len(notes))
	drawChatCard(renderer, config, notes, 255, 56, screenHeight-192-cardH)
}

// ambientDrawButtons lays out the bottom chip row and records the hit rects.
func ambientDrawButtons(renderer *sdl.Renderer, config *Config, font *ttf.Font) {
	if font == nil {
		return
	}
	_, _, _, playing, paused := ambientNowPlaying()
	playLabel := "A Play"
	if playing {
		playLabel = "A Pause"
	} else if paused {
		playLabel = "A Resume"
	}
	sleepLabel := "Y Sleep off"
	if remaining := ambientSleepRemaining(); remaining > 0 {
		sleepLabel = "Y Sleep " + ambientDurationText(remaining)
	}
	chips := []struct {
		action ambientAction
		label  string
	}{
		{ambientActionPlayPause, playLabel},
		{ambientActionPrev, "Prev"},
		{ambientActionNext, "Next"},
		{ambientActionSleep, sleepLabel},
		{ambientActionScreenOff, "X Screen off"},
		{ambientActionExit, "B Exit"},
	}

	y := screenHeight - 56
	x := int32(40)
	pad := int32(16)
	gap := int32(10)
	for _, chip := range chips {
		tw, th, _ := font.SizeUTF8(chip.label)
		w := int32(tw) + pad*2
		h := int32(th) + 14
		rect := sdl.Rect{X: x, Y: y - (h-30)/2, W: w, H: h}
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, h/2, sdl.Color{R: 16, G: 21, B: 33, A: 190})
		strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, h/2, 1, sdl.Color{R: 255, G: 255, B: 255, A: 34})
		renderText(renderer, config, font, chip.label, sdl.Color{R: 214, G: 224, B: 240, A: 225},
			rect.X+pad, rect.Y+(h-int32(th))/2)
		ambientButtons = append(ambientButtons, ambientButton{action: chip.action, rect: rect})
		x += w + gap
		if x > screenWidth-40 {
			break
		}
	}
}

func ambientDurationText(d time.Duration) string {
	minutes := int(d.Round(time.Minute) / time.Minute)
	if minutes < 1 {
		minutes = 1
	}
	return fmt.Sprintf("%dm", minutes)
}

// ambientAutoCheckInterval bounds how often the main loop checks the auto-entry
// rule. One second, like the screensaver, so the dock appears within a tick of
// the configured idle timeout instead of up to five seconds late. The battery
// read behind it is cached (ambientStatusRefresh).
const ambientAutoCheckInterval = time.Second

var ambientLastAutoCheck time.Time

// ambientAutoTick enters the dock screen after the device has been quiet long
// enough: quickly while charging, or at the normal idle timeout on battery.
func ambientAutoTick(config *Config, now time.Time) {
	if config == nil || !ambientModeEnabled(config) || ambientIsActive() {
		return
	}
	if !ambientLastAutoCheck.IsZero() && now.Sub(ambientLastAutoCheck) < ambientAutoCheckInterval {
		return
	}
	ambientLastAutoCheck = now
	if ambientEntryBlocked(config) {
		return
	}
	// A download or a patch install is visible progress: the dock must not
	// cover it. Music is allowed, since the dock shows what is playing.
	if installInProgress() || patchInstallBusy() {
		return
	}
	if ambientShouldEnter(ambientReadStatus(now).charging, userInputAge(now), ambientDockIdleTimeout, screensaverIdleTimeout) {
		ambientEnter(config)
	}
}
