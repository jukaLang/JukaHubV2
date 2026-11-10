package main

// ambient.go — the dock and ambient screen.
//
// A handheld that is charging or left alone should not sit on the launcher.
// This mode turns it into a bedside/dock display: a large clock, the Wi-Fi and
// battery chips, today's weather, the track that is playing and a full-screen
// picture — the lock-screen picture chosen in Files, or a slideshow of photos —
// all drawn over one calm screen. It is entered automatically while charging
// (after a short quiet period) or after the normal idle timeout, and can be
// opened from the Start menu at any time.
//
// Two things make it useful beyond a pretty screen:
//
//   - A sleep timer (15/30/60/90 minutes) that stops playback and darkens the
//     panel, so an audiobook does not play all night.
//   - A screen-off mode that blanks the panel immediately while audio keeps
//     decoding. The main loop draws nothing at all in that state, so the only
//     work left is the audio thread — the point of leaving music on overnight.
//
// While the dock is up the device also drops into the idle low-power state
// (saver_power.go): a lower CPU ceiling, a slow frame rate that still answers a
// press at once, and parked background pollers.
//
// SDL is not thread-safe, so the auto-entry monitor only flips flags under a
// mutex; every texture and pixel is touched on the render thread. Pictures are
// decoded and scaled by the worker in saver_backdrop.go and only uploaded here.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

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
	// ambientPhotoFade is how long a newly shown photo takes to cross-fade in.
	ambientPhotoFade = 900 * time.Millisecond
	// ambientPhotoRescan bounds directory scans so a slideshow never turns into
	// constant SD-card traffic.
	ambientPhotoRescan = 5 * time.Minute
	// ambientStatusRefresh throttles the battery sysfs read.
	ambientStatusRefresh = 5 * time.Second
	// ambientPhotoLimit caps how many files are listed from one folder.
	ambientPhotoLimit = 300
	// saverDockFlashTTL is how long a button shows it was pressed, so a press
	// from the pad or the keyboard is visibly answered on the dock too.
	saverDockFlashTTL = 220 * time.Millisecond
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
	changedAt   time.Time // when the photo on screen appeared; its fade starts here
	texture     *sdl.Texture
	texturePath string
	prev        *sdl.Texture  // the photo fading out under a new one
	owner       *sdl.Renderer // the renderer both textures belong to
	failed      map[string]bool
	scannedAt   time.Time
	scanDir     string
	wantPath    string // the photo the worker is producing
	wantStamp   string
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

// saverDockCardRect is the now-playing card's hit area: a tap on the card is the
// same as the Play button, the way a media widget behaves.
var saverDockCardRect sdl.Rect

// saverDockFlash remembers the last action, for the pressed look of its button.
var saverDockFlash struct {
	action ambientAction
	at     time.Time
}

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
	return ambientEntryBlockReason(config) != ""
}

// ambientEntryBlockReason names the surface that owns the screen, or "" when
// auto-entry may proceed. Naming it matters in the ambient audit: a suite that
// leaks a panel through one test reads "notification centre is open" instead of
// a generic "something is open".
func ambientEntryBlockReason(config *Config) string {
	if config == nil {
		return "no config"
	}
	open := []struct {
		name string
		yes  bool
	}{
		{"onboarding", onboarding.active},
		{"Quick Settings", qs.open},
		{"update prompt", updatePrompt.open},
		{"companion", companion.open},
		{"shortcuts sheet", shortcutsOpen},
		{"clipboard search", clipSearchOpen},
		{"global search", globalSearchOpen},
		{"on-screen keyboard", virtualKeyboardActive},
		{"Tools panel", toolsPanel.open},
		{"image viewer", imageViewerPath != ""},
		{"patch modal", patchModal != nil},
		{"scene transition (" + transitionPhase + ")", transitionPhase != "none"},
		{"full-screen video", videoFullscreen},
		{"media player", playerPhase() != "idle"},
		{"notification centre", nc.open},
		{"start menu", tb.startOpen},
		{"process panel", processPanel.open},
		{"Wi-Fi panel", wifiPanel.open},
		{"IP panel", ipPanelOpen},
		{"emulator panel", emuPanel.open},
		{"game library", gameLib.open},
		{"game profiles panel", gpPanel.open},
		{"Windows apps panel", winAppsPanel.open},
		{"ports panel", portsPanel.open},
		{"save manager", savePanel.open},
		{"curation panel", curationPanel.open},
		{"remote play panel", chiakiPanel.open},
		{"achievements panel", raPanel.open},
		{"accessibility panel", a11y.open},
		{"wave background panel", wpOpen},
		{"smart home panel", smartHomePanel.open},
		{"memory panel", memPanel.open},
		{"text editor", fileEditor.open},
		{"controller diagnostics", ctrldiag.open},
	}
	for _, b := range open {
		if b.yes {
			return b.name + " is open"
		}
	}
	if retroArchRunning() {
		return "a game is running"
	}
	return ""
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

// ambientEnter opens the dock screen. It runs on the event thread (the auto
// tick and the Start menu): leaving the starfield frees its texture.
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
	saverDockFlash.at = time.Time{}
	// The status chips are re-read as the dock appears.
	saverWifiInvalidate()
}

// ambientLeave returns to the normal UI and drops the photo textures.
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

	// The CPU comes back before the first awake frame is drawn.
	saverPowerWake()
	ambientFreePhoto()
	// The slideshow's renditions go with it; the lock picture's stays, so the
	// next idle screen has it at once.
	saverPicForget(screenlockImagePath(appConfig))
	releaseScreenlockTexture()
	ambientButtons = nil
	saverDockCardRect = sdl.Rect{}
	noteUserInputTime(time.Now())
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
		if entry.IsDir() || skipScanEntryName(entry.Name()) {
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
	ambientPhotos.wantPath = ""
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

// ambientPhotoTexture returns the texture of the photo on screen, advancing the
// slideshow on schedule. Photos are produced by the backdrop worker: the old
// photo stays up while the next one decodes, then the new one cross-fades in.
// It must be called on the render thread.
func ambientPhotoTexture(renderer *sdl.Renderer, now time.Time) *sdl.Texture {
	if renderer == nil || len(ambientPhotos.files) == 0 {
		return nil
	}
	if ambientPhotos.owner != nil && ambientPhotos.owner != renderer {
		// Another renderer's textures are dropped, never destroyed here.
		ambientPhotos.texture, ambientPhotos.prev, ambientPhotos.owner = nil, nil, nil
		ambientPhotos.texturePath = ""
	}
	n := len(ambientPhotos.files)
	if ambientPhotos.index >= n || ambientPhotos.index < 0 {
		ambientPhotos.index = 0
	}
	if ambientPhotos.changedAt.IsZero() {
		ambientPhotos.changedAt = now
	}
	// The dwell time counts from when a photo actually appeared, so a slow
	// decode never shortens the next photo's turn.
	if ambientPhotos.texture != nil && ambientPhotos.texturePath == ambientPhotos.files[ambientPhotos.index] &&
		now.Sub(ambientPhotos.changedAt) >= ambientPhotoInterval {
		ambientPhotos.index = (ambientPhotos.index + 1) % n
	}
	if ambientPhotos.failed == nil {
		ambientPhotos.failed = make(map[string]bool)
	}
	w, h := int(screenWidth), int(screenHeight)
	for attempts := 0; attempts < n; attempts++ {
		path := ambientPhotos.files[ambientPhotos.index]
		if ambientPhotos.texture != nil && ambientPhotos.texturePath == path {
			return ambientPhotos.texture
		}
		if ambientPhotos.failed[path] {
			ambientPhotos.index = (ambientPhotos.index + 1) % n
			continue
		}
		if ambientPhotos.wantPath != path {
			// One stat per photo, when it becomes the one wanted.
			ambientPhotos.wantPath = path
			ambientPhotos.wantStamp = screenlockStamp(path)
		}
		if ambientPhotos.wantStamp == "" {
			ambientPhotos.failed[path] = true
			ambientPhotos.index = (ambientPhotos.index + 1) % n
			continue
		}
		pix, done, err := saverPicRequest(saverPicKey{path: path, stamp: ambientPhotos.wantStamp, w: w, h: h})
		if !done {
			return ambientPhotos.texture
		}
		var tex *sdl.Texture
		if err == nil {
			tex, err = saverPicTexture(renderer, pix)
		}
		if err != nil {
			// A corrupt JPEG or a codec missing from the runtime: walk on to
			// the next photo instead of retrying this one every frame.
			ambientPhotos.failed[path] = true
			ambientPhotos.index = (ambientPhotos.index + 1) % n
			continue
		}
		if ambientPhotos.prev != nil {
			ambientPhotos.prev.Destroy()
		}
		ambientPhotos.prev = ambientPhotos.texture
		ambientPhotos.texture, ambientPhotos.texturePath = tex, path
		ambientPhotos.owner = renderer
		ambientPhotos.changedAt = now
		return tex
	}
	return ambientPhotos.texture
}

func ambientFreePhoto() {
	if ambientPhotos.texture != nil {
		ambientPhotos.texture.Destroy()
		ambientPhotos.texture = nil
	}
	if ambientPhotos.prev != nil {
		ambientPhotos.prev.Destroy()
		ambientPhotos.prev = nil
	}
	ambientPhotos.owner = nil
	ambientPhotos.texturePath = ""
	ambientPhotos.files = nil
	ambientPhotos.index = 0
	ambientPhotos.failed = nil
	ambientPhotos.scannedAt = time.Time{}
	ambientPhotos.scanDir = ""
	ambientPhotos.wantPath = ""
	ambientPhotos.wantStamp = ""
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

// saverDockPhotoFading reports whether a photo is cross-fading right now, which
// is when the idle frame budget gives way to the normal one.
func saverDockPhotoFading(now time.Time) bool {
	return ambientPhotos.texture != nil && ambientPhotoAlpha(ambientPhotos.changedAt, now) < 255
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

// saverDockTrackPath is the current track's file, for its cover art.
func saverDockTrackPath() string {
	music.mu.Lock()
	defer music.mu.Unlock()
	if music.current >= 0 && music.current < len(music.tracks) {
		return music.tracks[music.current].Path
	}
	return ""
}

// saverDockHasPlaylist reports whether Prev and Next have anything to step to.
func saverDockHasPlaylist() bool {
	music.mu.Lock()
	defer music.mu.Unlock()
	return len(music.tracks) > 0
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
// hides it here without touching the rest of the UI. A screenshot has no
// weather chip at all (screenshotmode.go).
func ambientWeather(config *Config) (string, string) {
	if !ambientCustomBool(config, "ambientWeather", true) || shotModeOn() {
		return "", ""
	}
	wxMutex.Lock()
	defer wxMutex.Unlock()
	if !weatherReady || len(weatherDaily) == 0 {
		return "Weather", "loading…"
	}
	day, ok := weatherDayFor(weatherDaily, time.Now())
	if !ok {
		return "Weather", "no forecast for today yet"
	}
	return weatherCodeDesc(day.Code), formatAmbientTemps(day, weatherUnit(config))
}

// saverDockWeatherIcon is the glyph for today's forecast: the sun (or the moon
// after dark) for a clear sky, a cloud for anything else.
func saverDockWeatherIcon(now time.Time) string {
	wxMutex.Lock()
	day, ok := weatherDayFor(weatherDaily, now)
	ready := weatherReady
	wxMutex.Unlock()
	if !ready || !ok {
		return "cloud"
	}
	if day.Code <= 1 {
		if h := now.Hour(); h < 6 || h >= 20 {
			return "moon"
		}
		return "sun"
	}
	return "cloud"
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

// ambientReadStatus reads the battery at most every ambientStatusRefresh. Both
// idle screens' battery chips read it.
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
// visible buttons.
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
		ambientActivate(ambientActionExit)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE, sdl.K_a, sdl.K_p:
		ambientActivate(ambientActionPlayPause)
	case sdl.K_LEFT:
		ambientActivate(ambientActionPrev)
	case sdl.K_RIGHT:
		ambientActivate(ambientActionNext)
	case sdl.K_UP:
		musicAdjustVolume(0.05)
	case sdl.K_DOWN:
		musicAdjustVolume(-0.05)
	case sdl.K_y, sdl.K_s:
		ambientActivate(ambientActionSleep)
	case sdl.K_x:
		ambientActivate(ambientActionScreenOff)
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
}

// ambientTogglePlayback is the dock's Play button. It always does something:
// pause or resume the current track, replay a stopped one, start a loaded list,
// or find the music on the card and start that. The old button only knew the
// first two, and on a device whose Files root held no audio files of its own it
// silently did nothing.
func ambientTogglePlayback() {
	_, _, _, playing, paused := ambientNowPlaying()
	if playing || paused {
		musicTogglePause()
		return
	}
	music.mu.Lock()
	hasCurrent := music.current >= 0 && music.current < len(music.tracks)
	count := len(music.tracks)
	cursor := music.cursor
	music.mu.Unlock()
	if hasCurrent {
		// musicTogglePause plays a stopped current track from where it was.
		musicTogglePause()
		return
	}
	if count > 0 {
		if cursor < 0 || cursor >= count {
			cursor = 0
		}
		musicPlayTrack(cursor)
		return
	}
	dir := saverFindMusicDir(appConfig)
	if dir == "" {
		showToast("No music found — copy songs into a Music folder to play them here", ToastInfo())
		return
	}
	idx := musicLoadPlaylist(dir, "")
	musicPlayTrack(idx)
}

// saverMusicDirCandidates are the folders the dock looks in for music when
// nothing has been played yet, most specific first.
func saverMusicDirCandidates(config *Config) []string {
	var dirs []string
	if dir := strings.TrimSpace(musicCurrentDir()); dir != "" && dir != "." {
		dirs = append(dirs, dir)
	}
	if config != nil {
		if root := strings.TrimSpace(config.Variables.FileExplorerRoot); root != "" {
			dirs = append(dirs, filepath.Join(root, "Music"), filepath.Join(root, "music"), root)
		}
	}
	dirs = append(dirs, "/mnt/SDCARD/Music", "/mnt/SDCARD/music", "/mnt/SDCARD/MUSIC")
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs, filepath.Join(home, "Music"))
	}
	return dirs
}

// saverFindMusicDir returns the first candidate folder that holds audio
// files, looking one level into album folders as well.
func saverFindMusicDir(config *Config) string {
	seen := make(map[string]bool)
	for _, dir := range saverMusicDirCandidates(config) {
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		if found := saverMusicIn(dir, 1); found != "" {
			return found
		}
	}
	return ""
}

// saverMusicIn returns dir when it holds audio files, else the first subfolder
// (in name order, depth levels down) that does. The walk is bounded: it runs
// on a button press, not in a frame, but a card can hold thousands of folders.
func saverMusicIn(dir string, depth int) string {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return ""
	}
	if len(audioFilesIn(dir)) > 0 {
		return dir
	}
	if depth <= 0 {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	checked := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if !e.IsDir() && e.Type()&os.ModeSymlink == 0 {
			continue
		}
		checked++
		if checked > 64 {
			break
		}
		if found := saverMusicIn(filepath.Join(dir, e.Name()), depth-1); found != "" {
			return found
		}
	}
	return ""
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
		ambientActivate(ambientActionPlayPause)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_START:
		ambientActivate(ambientActionExit)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		ambientActivate(ambientActionPrev)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT, sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		ambientActivate(ambientActionNext)
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		musicAdjustVolume(0.05)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		musicAdjustVolume(-0.05)
	case sdl.CONTROLLER_BUTTON_X:
		ambientActivate(ambientActionScreenOff)
	case sdl.CONTROLLER_BUTTON_Y:
		ambientActivate(ambientActionSleep)
	}
	return true
}

// ambientActivate runs one dock action. Pad, keyboard, mouse and touch all come
// through here, so every route gets the same sound and the same pressed look.
func ambientActivate(action ambientAction) {
	saverDockFlash.action = action
	saverDockFlash.at = time.Now()
	switch action {
	case ambientActionPlayPause:
		ambientTogglePlayback()
	case ambientActionPrev:
		if saverDockHasPlaylist() {
			PlayNavSound()
		}
		musicPrev()
	case ambientActionNext:
		if saverDockHasPlaylist() {
			PlayNavSound()
		}
		musicNext()
	case ambientActionSleep:
		PlayActivateSound()
		ambientCycleSleep()
	case ambientActionScreenOff:
		ambientDarken()
	case ambientActionExit:
		PlayBackSound()
		ambientLeave()
	}
}

// ambientHandleClick handles taps on the dock screen: buttons act, a tap on the
// now-playing card plays or pauses, anything else only wakes a darked panel. It
// reports whether the click was consumed.
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
	if saverDockCardRect.W > 0 && pointInRect(mx, my, saverDockCardRect) {
		ambientActivate(ambientActionPlayPause)
	}
	return true
}

// ──────────────────────────────────────────────────────────────────────
// Rendering
// ──────────────────────────────────────────────────────────────────────

// saverDockMargin is the dock's frame margin.
func saverDockMargin() int32 { return saverScaled(56, 20) }

// renderAmbient draws one frame of the dock screen. It returns true while
// active so the caller can skip the scene; the screen-off state is handled
// before this is reached.
func renderAmbient(renderer *sdl.Renderer, config *Config) bool {
	if renderer == nil || !ambientIsActive() {
		return false
	}
	now := time.Now()
	ambientButtons = ambientButtons[:0]
	saverDockCardRect = sdl.Rect{}
	photo := ambientDrawBackdrop(renderer, config, now)

	margin := saverDockMargin()
	right := screenWidth - saverScaled(40, 16)

	// Clock, date and the next alarm, top-left. The dock clock is sized to the
	// panel rather than to a config role: it has to be readable across a room.
	below := saverDrawClock(renderer, config, margin, saverScaled(20, 8), ambientClockSize(), now)
	saverDrawAlarmChip(renderer, config, margin+saverScaled(4, 2), below+saverScaled(12, 6))

	// Wi-Fi and battery, top-right, and today's weather under them.
	top := saverScaled(28, 12)
	saverDrawStatusRow(renderer, config, right, top, now)
	m := saverChipSize()
	saverDockDrawWeather(renderer, config, right, top+m.h+saverScaled(10, 6), now)

	// The bottom: the button row, the now-playing card above it on the left,
	// the timer and photo lines on the right, and new chat above the card.
	rowTop := ambientDrawButtons(renderer, config, now)
	cardBottom := rowTop - saverScaled(18, 8)
	cardTop := ambientDrawNowPlaying(renderer, config, cardBottom)
	ambientDrawStatus(renderer, config, right, cardBottom, photo)
	ambientDrawChat(renderer, config, now, cardTop-saverScaled(14, 8))
	return true
}

// ambientDrawBackdrop paints the dock's backdrop: the lock-screen picture when
// one is set, else the photo slideshow, else Home's dark backdrop. It reports
// whether the slideshow is what is showing (for the photo counter). A
// screenshot keeps the plain backdrop: the photos are the user's own.
func ambientDrawBackdrop(renderer *sdl.Renderer, config *Config, now time.Time) bool {
	saverDockDrawBase(renderer)
	if shotModeOn() {
		return false
	}
	if screenlockImagePath(config) != "" {
		if drawScreenlockImage(renderer, config, 255) || saverLockShowing(config) {
			// The chosen picture wins over the slideshow; while it is still
			// decoding, the base backdrop stands in rather than a photo that
			// would then be replaced.
			return false
		}
	}
	ambientRefreshPhotos(config, now)
	texture := ambientPhotoTexture(renderer, now)
	if texture == nil {
		return false
	}
	full := sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight}
	alpha := ambientPhotoAlpha(ambientPhotos.changedAt, now)
	if ambientPhotos.prev != nil {
		if alpha < 255 {
			_ = ambientPhotos.prev.SetAlphaMod(255)
			renderer.Copy(ambientPhotos.prev, nil, &full)
		} else {
			ambientPhotos.prev.Destroy()
			ambientPhotos.prev = nil
		}
	}
	_ = texture.SetAlphaMod(alpha)
	renderer.Copy(texture, nil, &full)
	_ = texture.SetAlphaMod(255)
	return true
}

// saverDockDrawBase is Home's backdrop, kept dark on every theme: a navy
// base with the cyan bloom behind the clock and the violet one in the far
// corner. It costs a clear and two copies of one cached glow, and it is static,
// so the low-power frame rate shows no stepping.
func saverDockDrawBase(renderer *sdl.Renderer) {
	base := ColorBackground
	if glassLightTheme() {
		base = sdl.Color{R: 11, G: 15, B: 23, A: 255}
	}
	renderer.SetDrawColor(base.R, base.G, base.B, 255)
	renderer.Clear()
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	w, h := screenWidth, screenHeight
	blob := scaleX(900, w)
	homeGlowBlob(renderer, sdl.Rect{X: scaleX(60, w) - blob/2, Y: scaleY(300, h) - blob/2, W: blob, H: blob}, ColorAccent, 44)
	homeGlowBlob(renderer, sdl.Rect{X: w - blob/2 - scaleX(60, w), Y: h - blob/2, W: blob, H: blob}, sdl.Color{R: 124, G: 58, B: 237, A: 255}, 36)
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

// saverDockDrawWeather draws today's forecast as a chip, right-aligned at right.
func saverDockDrawWeather(renderer *sdl.Renderer, config *Config, right, y int32, now time.Time) {
	desc, temps := ambientWeather(config)
	if desc == "" {
		return
	}
	font := saverChipFont(config)
	if font == nil {
		return
	}
	label := desc
	if temps != "" {
		label += " · " + temps
	}
	label = ellipsize(font, label, saverScaled(380, 200))
	saverDrawLabelChip(renderer, font, saverDockWeatherIcon(now), label, right, y)
}

// ambientShowNowPlaying decides whether the dock has a now-playing card at all.
// With no track loaded the card would be an empty box reading "Nothing playing"
// with a 0:00 clock, which looks like a broken panel rather than a screen saver,
// so the dock simply has none until there is something to name.
func ambientShowNowPlaying(title string, playing, paused bool) bool {
	return title != "" || playing || paused
}

// ambientDrawNowPlaying draws the now-playing card with its bottom edge at
// bottom and returns its top (bottom itself when there is no card).
func ambientDrawNowPlaying(renderer *sdl.Renderer, config *Config, bottom int32) int32 {
	title, pos, dur, playing, paused := ambientNowPlaying()
	if !ambientShowNowPlaying(title, playing, paused) {
		return bottom
	}
	title = strings.TrimSuffix(title, filepath.Ext(title))
	margin := saverDockMargin()
	w := saverScaled(480, 260)
	if limit := screenWidth - 2*margin - saverScaled(240, 120); w > limit {
		w = limit
	}
	h := saverScaled(100, 72)
	rect := sdl.Rect{X: margin, Y: bottom - h, W: w, H: h}
	saverDockCardRect = rect

	g := saverGlassStyle()
	radius := saverScaled(20, 14)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, g.fill)
	strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, 1, g.edge)

	pad := saverScaled(14, 8)
	art := rect.H - 2*pad
	artRect := sdl.Rect{X: rect.X + pad, Y: rect.Y + pad, W: art, H: art}
	drewArt := false
	if cover := musicCoverArtPath(saverDockTrackPath()); cover != "" {
		if tex := artTexture(renderer, cover); tex != nil {
			if _, _, tw, th, err := tex.Query(); err == nil && tw > 0 && th > 0 {
				dst := saverCoverRect(tw, th, artRect)
				renderWithClip(renderer, artRect.X, artRect.Y, artRect.W, artRect.H, func(rr *sdl.Renderer) {
					rr.Copy(tex, nil, &dst)
				})
				drewArt = true
			}
		}
	}
	if !drewArt {
		fillRoundedRect(renderer, artRect.X, artRect.Y, artRect.W, artRect.H, saverScaled(12, 8), g.raised)
		glyph := art / 2
		saverDrawIcon(renderer, "music", sdl.Rect{X: artRect.X + (art-glyph)/2, Y: artRect.Y + (art-glyph)/2, W: glyph, H: glyph}, false, g)
	}

	textX := artRect.X + art + pad
	textW := rect.X + rect.W - pad - textX
	if textW <= 0 {
		return rect.Y
	}
	titleFont := getFontSized(config, "small", int(saverScaled(22, 15)))
	smallFont := saverChipFont(config)
	y := rect.Y + pad - 2
	if titleFont != nil && title != "" {
		line := ellipsize(titleFont, title, textW)
		_, th := measureText(titleFont, line)
		drawText(renderer, titleFont, line, textX, y, g.ink, textAlignLeft)
		y += th + 2
	}
	state := "Stopped"
	switch {
	case paused:
		state = "Paused"
	case playing:
		state = "Playing"
	}
	if smallFont != nil {
		drawText(renderer, smallFont, state, textX, y, g.muted, textAlignLeft)
	}
	// Progress along the bottom of the text column. Without a known length
	// there is nothing to draw along, so the bar is left out.
	if dur > 0 && smallFont != nil {
		timing := ambientTimeText(pos) + " / " + ambientTimeText(dur)
		tw, tth := measureText(smallFont, timing)
		barH := saverScaled(5, 4)
		barY := rect.Y + rect.H - pad - tth/2 - barH/2
		barW := textW - tw - saverScaled(12, 6)
		if barW > 20 {
			fillRoundedRect(renderer, textX, barY, barW, barH, barH/2, saverTrackColor(g))
			if frac := ambientProgressFraction(pos, dur); frac > 0 {
				fw := int32(float64(barW) * frac)
				if fw < barH {
					fw = barH
				}
				fillRoundedRect(renderer, textX, barY, fw, barH, barH/2, glassAccent())
			}
		}
		drawText(renderer, smallFont, timing, rect.X+rect.W-pad-tw, rect.Y+rect.H-pad-tth, g.muted, textAlignLeft)
	}
	return rect.Y
}

// saverTrackColor is a progress track that reads on either glass: the glass
// ink at a low alpha.
func saverTrackColor(g saverGlass) sdl.Color {
	c := g.ink
	c.A = 46
	return c
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

// ambientDrawStatus draws the dock's bottom-right column — the sleep timer and
// the photo counter — right-aligned at right, its last line ending at bottom.
func ambientDrawStatus(renderer *sdl.Renderer, config *Config, right, bottom int32, slideshow bool) {
	font := saverChipFont(config)
	if font == nil {
		return
	}
	photoTotal := 0
	if slideshow {
		photoTotal = len(ambientPhotos.files)
	}
	lines := ambientStatusLines(ambientSleepRemaining(), ambientPhotos.index, photoTotal)
	lineH := int32(font.Height()) + saverScaled(6, 3)
	y := bottom - lineH*int32(len(lines))
	for _, line := range lines {
		lw, _ := measureText(font, line.text)
		renderText(renderer, config, font, line.text, line.color, right-lw, y)
		y += lineH
	}
}

// ambientStatusLine is one line of the dock's bottom-right column.
type ambientStatusLine struct {
	text  string
	color sdl.Color
}

// ambientStatusLines builds that column from the lines that actually have
// something to say: the sleep timer while one is armed, and the slideshow's
// position while one runs. The battery lives in its chip at the top now, next
// to the Wi-Fi chip, so the column never repeats it.
func ambientStatusLines(sleepLeft time.Duration, photoIndex, photoTotal int) []ambientStatusLine {
	var lines []ambientStatusLine
	if sleepLeft > 0 {
		lines = append(lines, ambientStatusLine{"Sleep in " + ambientTimeText(sleepLeft.Round(time.Second).Seconds()),
			sdl.Color{R: 236, G: 200, B: 140, A: 235}})
	}
	if photoTotal > 0 {
		lines = append(lines, ambientStatusLine{fmt.Sprintf("Photo %d/%d", photoIndex+1, photoTotal), saverInkSoft})
	}
	return lines
}

// ambientDrawChat keeps the newest incoming messages visible on the dock, with
// the card's bottom edge at bottom (above the now-playing card).
func ambientDrawChat(renderer *sdl.Renderer, config *Config, now time.Time, bottom int32) {
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
	drawChatCard(renderer, config, notes, 255, saverDockMargin(), bottom-cardH)
}

// saverDockChip is one button of the dock's bottom row.
type saverDockChip struct {
	action  ambientAction
	badge   string // the pad button that presses it
	icon    string // shared glyph name; "" for the drawn skip glyphs
	label   string
	enabled bool
}

// ambientDrawButtons lays out the bottom button row, records the hit rects and
// returns the row's top edge. Each button is glass with the pad button that
// presses it, its glyph and its name; on a narrow screen the badges go first,
// then the secondary labels, so a button is never cut.
func ambientDrawButtons(renderer *sdl.Renderer, config *Config, now time.Time) int32 {
	h := saverScaled(46, 32)
	top := screenHeight - h - saverScaled(22, 8)
	font := saverChipFont(config)
	if font == nil {
		return top
	}
	_, _, _, playing, paused := ambientNowPlaying()
	hasList := saverDockHasPlaylist()
	playIcon, playLabel := "play", "Play"
	if playing {
		playIcon, playLabel = "pause", "Pause"
	} else if paused {
		playLabel = "Resume"
	}
	sleepLabel := "Sleep off"
	if remaining := ambientSleepRemaining(); remaining > 0 {
		sleepLabel = "Sleep " + ambientDurationText(remaining)
	}
	chips := [6]saverDockChip{
		{ambientActionPlayPause, "A", playIcon, playLabel, true},
		{ambientActionPrev, "L1", "", "Prev", hasList},
		{ambientActionNext, "R1", "", "Next", hasList},
		{ambientActionSleep, "Y", "clock", sleepLabel, true},
		{ambientActionScreenOff, "X", "moon", "Screen off", true},
		{ambientActionExit, "B", "back", "Exit", true},
	}

	pad := saverScaled(14, 8)
	gap := saverScaled(8, 5)
	iconSize := saverScaled(20, 14)
	spacing := saverScaled(10, 6)
	badgeH := h - saverScaled(18, 12)
	badgePad := saverScaled(12, 8)
	avail := screenWidth - 2*saverScaled(40, 12)
	// Measure each layout, richest first, and keep the first that fits: with
	// badges, then without, then with only Play keeping its label.
	var widths [6]int32
	var total int32
	var badges, labels bool
	for mode := 0; mode < 3; mode++ {
		badges, labels = mode == 0, mode < 2
		total = spacing * int32(len(chips)-1)
		for i, c := range chips {
			w := pad + iconSize + pad
			if badges {
				bw, _ := measureText(font, c.badge)
				w += bw + badgePad + gap
			}
			if labels || i == 0 {
				lw, _ := measureText(font, c.label)
				w += gap + lw
			}
			widths[i] = w
			total += w
		}
		if total <= avail {
			break
		}
	}
	x := (screenWidth - total) / 2
	if x < 0 {
		x = 0
	}
	g := saverGlassStyle()
	flashing := !saverDockFlash.at.IsZero() && now.Sub(saverDockFlash.at) < saverDockFlashTTL
	for i, c := range chips {
		rect := sdl.Rect{X: x, Y: top, W: widths[i], H: h}
		pressed := flashing && saverDockFlash.action == c.action
		hovered := pointInRect(mouseX, mouseY, rect)
		saverDockDrawButton(renderer, font, rect, c, g, saverDockButtonLook{
			badge: badges, label: labels || i == 0, pressed: pressed, hovered: hovered,
			badgeH: badgeH, badgePad: badgePad, pad: pad, gap: gap, icon: iconSize,
		})
		ambientButtons = append(ambientButtons, ambientButton{action: c.action, rect: rect})
		x += widths[i] + spacing
	}
	return top
}

// saverDockButtonLook is how one bottom-row button is drawn this frame.
type saverDockButtonLook struct {
	badge, label     bool
	pressed, hovered bool
	badgeH, badgePad int32
	pad, gap, icon   int32
}

// saverDockDrawButton paints one bottom-row button: glass, the accent while it
// shows a press, a lighter glass under the mouse.
func saverDockDrawButton(renderer *sdl.Renderer, font *ttf.Font, rect sdl.Rect, c saverDockChip, g saverGlass, look saverDockButtonLook) {
	radius := rect.H / 2
	fill, edge := g.fill, g.edge
	switch {
	case look.pressed:
		fill = glassAccent()
		fill.A = 110
		edge = glassAccent()
	case look.hovered:
		fill = g.raised
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, fill)
	strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, 1, edge)

	ink := g.ink
	if !c.enabled {
		ink = g.muted
	}
	x := rect.X + look.pad
	cy := rect.Y + rect.H/2
	if look.badge {
		bw, bh := measureText(font, c.badge)
		pill := sdl.Rect{X: x, Y: cy - look.badgeH/2, W: bw + look.badgePad, H: look.badgeH}
		pillFill := g.ink
		pillFill.A = 34
		fillRoundedRect(renderer, pill.X, pill.Y, pill.W, pill.H, pill.H/2, pillFill)
		drawText(renderer, font, c.badge, pill.X+(pill.W-bw)/2, cy-bh/2, g.muted, textAlignLeft)
		x += pill.W + look.gap
	}
	iconRect := sdl.Rect{X: x, Y: cy - look.icon/2, W: look.icon, H: look.icon}
	switch c.action {
	case ambientActionPrev:
		saverDrawSkipGlyph(renderer, iconRect, false, ink)
	case ambientActionNext:
		saverDrawSkipGlyph(renderer, iconRect, true, ink)
	default:
		saverDrawIcon(renderer, c.icon, iconRect, !c.enabled, g)
	}
	x += look.icon
	if look.label {
		lw, lh := measureText(font, c.label)
		if x+look.gap+lw <= rect.X+rect.W-look.pad+1 {
			drawText(renderer, font, c.label, x+look.gap, cy-lh/2, ink, textAlignLeft)
		}
	}
}

// saverDrawSkipGlyph draws a previous/next-track glyph — two triangles and a
// bar — in rect. The shared icon set has play and pause but no skip glyphs.
func saverDrawSkipGlyph(renderer *sdl.Renderer, rect sdl.Rect, forward bool, ink sdl.Color) {
	if rect.W < 6 || rect.H < 6 {
		return
	}
	h := rect.H * 2 / 3
	tw := rect.W * 3 / 8
	top := rect.Y + (rect.H-h)/2
	mid := top + h/2
	bar := rect.W / 8
	if bar < 2 {
		bar = 2
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	if forward {
		x := rect.X + (rect.W-(2*tw+bar))/2
		fillTriangleFilled(renderer, pt{x: x, y: top}, pt{x: x, y: top + h}, pt{x: x + tw, y: mid}, ink)
		fillTriangleFilled(renderer, pt{x: x + tw, y: top}, pt{x: x + tw, y: top + h}, pt{x: x + 2*tw, y: mid}, ink)
		fillRoundedRect(renderer, x+2*tw, top, bar, h, 1, ink)
		return
	}
	x := rect.X + (rect.W-(2*tw+bar))/2
	fillRoundedRect(renderer, x, top, bar, h, 1, ink)
	fillTriangleFilled(renderer, pt{x: x + bar + tw, y: top}, pt{x: x + bar + tw, y: top + h}, pt{x: x + bar, y: mid}, ink)
	fillTriangleFilled(renderer, pt{x: x + bar + 2*tw, y: top}, pt{x: x + bar + 2*tw, y: top + h}, pt{x: x + bar + tw, y: mid}, ink)
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
