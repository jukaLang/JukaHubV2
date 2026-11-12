package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// ConfigWatcher monitors the project's config files for changes and triggers a
// reload. It watches jukaconfig.json and jukaconfig.xml together and resolves
// between them with ResolveConfigPath, so a hot reload picks exactly the same
// file (and therefore the same design) as a restart would.
type ConfigWatcher struct {
	mu       sync.Mutex
	path     string // primary config path (jukaconfig.json)
	paths    []string
	lastMod  map[string]time.Time
	running  bool
	paused   bool // poller off while no Live Builder session is paired (PERF-6)
	stopCh   chan struct{}
	reloadCh chan struct{}
	onReload func(*Config)
	onSource func(string)
	debounce *time.Timer
}

// NewConfigWatcher creates a watcher for the given config path. Its XML sibling
// is watched too, so exporting jukaconfig.xml from the builder is picked up.
func NewConfigWatcher(path string) *ConfigWatcher {
	xmlPath := XMLSidecarPath(path)
	paths := []string{path}
	if xmlPath != path {
		paths = append(paths, xmlPath)
	}
	return &ConfigWatcher{
		path:     path,
		paths:    paths,
		lastMod:  make(map[string]time.Time),
		stopCh:   make(chan struct{}),
		reloadCh: make(chan struct{}, 1),
	}
}

// SetOnReload sets the callback invoked with the reloaded config.
func (cw *ConfigWatcher) SetOnReload(fn func(*Config)) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	cw.onReload = fn
}

// SetOnSource sets an optional callback invoked with the path the config was
// loaded from (useful for diagnostics and UI hints).
func (cw *ConfigWatcher) SetOnSource(fn func(string)) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	cw.onSource = fn
}

// NotifyWritten records freshly written files and requests an immediate reload.
// The live builder saves through AtomicWrite and then calls this, so a push is
// visible on the handheld in well under a second instead of waiting for the
// two-second poll. Recording the mtime first keeps the poller from reloading the
// same bytes again and showing a second "Config reloaded" toast.
func (cw *ConfigWatcher) NotifyWritten(paths ...string) {
	cw.mu.Lock()
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil {
			cw.lastMod[p] = info.ModTime()
		}
	}
	if cw.debounce != nil {
		cw.debounce.Stop()
		cw.debounce = nil
	}
	running := cw.running
	cw.mu.Unlock()
	if !running {
		return
	}
	select {
	case cw.reloadCh <- struct{}{}:
	default:
	}
}

// IgnoreWrite records a write the app made itself, so the poller does not
// reload it. Settings → Save rewrites the design file, and reloading those
// bytes ran the whole reload path for a config the app already holds.
func (cw *ConfigWatcher) IgnoreWrite(paths ...string) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil {
			cw.lastMod[p] = info.ModTime()
		}
	}
}

// SetPolling turns the modification poller on or off. The handheld only needs
// to poll while a Live Builder session is paired: a design pushed from the
// browser calls NotifyWritten itself, so an unpaired session was paying a stat
// of every config file every two seconds for nothing (PERF-6). Desktop keeps
// polling always, where hand-editing the file is the usual way it changes.
// Resuming takes a fresh baseline, so a write made while the poller was off is
// not replayed as a reload when the session pairs.
func (cw *ConfigWatcher) SetPolling(active bool) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	if cw.paused == !active {
		return
	}
	cw.paused = !active
	if active {
		for _, p := range cw.paths {
			if info, err := os.Stat(p); err == nil {
				cw.lastMod[p] = info.ModTime()
			}
		}
	}
}

// setConfigWatcherPolling switches the shared watcher's poller, when there is
// one. The live builder calls it as its server starts and stops (PERF-6).
func setConfigWatcherPolling(active bool) {
	if globalConfigWatcher != nil {
		globalConfigWatcher.SetPolling(active)
	}
}

// Start begins the background file watcher. It polls the modification times
// every 2 seconds and debounces changes for 500ms to avoid rapid reloads while
// a file is still being written.
func (cw *ConfigWatcher) Start() {
	cw.mu.Lock()
	if cw.running {
		cw.mu.Unlock()
		return
	}
	cw.running = true
	for _, p := range cw.paths {
		if info, err := os.Stat(p); err == nil {
			cw.lastMod[p] = info.ModTime()
		}
	}
	cw.mu.Unlock()

	go cw.watchLoop()
	logDebug("hotreload", "watching %s for changes", strings.Join(cw.paths, ", "))
}

// Stop halts the background file watcher.
func (cw *ConfigWatcher) Stop() {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	if !cw.running {
		return
	}
	cw.running = false
	close(cw.stopCh)
}

// watchLoop polls the config files every 2 seconds.
func (cw *ConfigWatcher) watchLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-cw.stopCh:
			return
		case <-ticker.C:
			cw.checkFile()
		case <-cw.reloadCh:
			cw.performReload()
		}
	}
}

// checkFile checks whether any watched config file changed since the last check
// (including a file that has just been created - the editor's first XML export,
// for example, which previously was ignored until a restart).
func (cw *ConfigWatcher) checkFile() {
	cw.mu.Lock()
	// A paused poller does no I/O at all: the paired builder pushes through
	// NotifyWritten, which is the only change a session can produce.
	if cw.paused {
		cw.mu.Unlock()
		return
	}
	changed := false
	for _, p := range cw.paths {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		modTime := info.ModTime()
		lastMod, seen := cw.lastMod[p]
		if seen && !modTime.After(lastMod) {
			continue
		}
		cw.lastMod[p] = modTime
		// A file seen for the first time starts a reload only when another
		// watched file already existed, so Start() recording an absent sibling
		// cannot trigger a spurious reload.
		if seen || modTime.After(activeStart) {
			changed = true
		}
	}
	if !changed {
		cw.mu.Unlock()
		return
	}

	// Debounce: wait 500ms after the last detected change
	if cw.debounce != nil {
		cw.debounce.Stop()
	}
	cw.debounce = time.AfterFunc(500*time.Millisecond, func() {
		select {
		case cw.reloadCh <- struct{}{}:
		default:
		}
	})
	cw.mu.Unlock()
}

// performReload loads the newest config and invokes the callback.
func (cw *ConfigWatcher) performReload() {
	cw.mu.Lock()
	fn := cw.onReload
	srcFn := cw.onSource
	primary := cw.path
	cw.mu.Unlock()

	if fn == nil {
		return
	}

	cfg, source, err := LoadXMLConfigWithFallback(primary)
	if err != nil {
		logWarn("hotreload", "reload failed: %v", err)
		return
	}

	// Validate
	if err := NewConfigValidator().Validate(cfg); err != nil {
		logWarn("hotreload", "validation failed: %v", err)
		configHealth.RecordLoad(source, fmt.Errorf("validation failed: %w", err))
		configHealth.Log()
		return
	}

	if srcFn != nil {
		srcFn(source)
	}
	fn(cfg)
	configHealth.RecordLoad(source, nil)
	configHealth.MarkLoadOK()
	logDebug("hotreload", "config reloaded successfully from %s", source)
}

// activeStart is when the process started; a config file created after this
// point is a genuine edit rather than an already-present sibling.
var activeStart = time.Now()

// globalConfigWatcher is the shared instance used by main.
var globalConfigWatcher *ConfigWatcher

// pendingConfigReload carries a reloaded config from the watcher goroutine to
// the main loop, which applies it between frames (applyConfigReload).
var pendingConfigReload struct {
	mu  sync.Mutex
	cfg *Config
}

// queueConfigReload hands a freshly loaded config to the main loop. The
// watcher goroutine used to apply it itself: it swapped the config the render
// loop was drawing, retinted the palette mid-frame, and iterated the user
// config's live Custom map — a fatal concurrent map access — and it never
// clamped the scene index, so reloading while on an injected scene (Alarm,
// Music, Games, Playtime, Browser) indexed past the end of the new scene list
// and the app exited.
func queueConfigReload(cfg *Config) {
	pendingConfigReload.mu.Lock()
	pendingConfigReload.cfg = cfg
	pendingConfigReload.mu.Unlock()
}

// takePendingConfigReload returns the queued config once, or nil.
func takePendingConfigReload() *Config {
	pendingConfigReload.mu.Lock()
	defer pendingConfigReload.mu.Unlock()
	cfg := pendingConfigReload.cfg
	pendingConfigReload.cfg = nil
	return cfg
}

// applyConfigReload installs a reloaded config on the main thread and returns
// it. It redoes what boot does to a loaded config — the user's settings, the
// device profile, the scenes injected in memory — and keeps the user on the
// scene they were on.
func applyConfigReload(prev, next *Config) *Config {
	if next == nil {
		return prev
	}
	// The user's settings as they are now: the pending save is written first
	// and jukauser.json read back. Merging the boot-time cache undid every
	// setting changed in this session (its typed fields never change after
	// load), and Settings → Save itself triggered this path.
	flushPendingUserConfig()
	mergeUserConfig(next, reloadUserConfigFromDisk())
	applyDeviceUIProfile(next)
	ensureWeatherUnitSetting(next)
	sysToolMigrateScenes(next)
	ensureAlarmScene(next)
	vkEnsureSettingsScene(next)
	tbxEnsureScenes(next)
	ensureSettingsWidgets(next)
	ensureOnboardingEntry(next)
	ensureMusicScene(next)
	ensureGamesScene(next)
	pdfvEnsureScene(next)
	ensurePlaytimeScene(next)
	ensurePlaytimeEntry(next)
	ensureToolsHubScene(next)
	peerEnsureScenes(next)
	ensureRommScene(next)
	ensureApkStoreScene(next)
	// Voice command (voicecmd.go) is injected the same way; a reload while it
	// is up must not drop the page under the user.
	ensureVoiceScene(next)
	btEnsureScene(next)
	ensureBrowserScene(next)
	// Control profiles and Local play, and the chosen profile, its switches
	// and the D-pad modes as the reloaded settings say.
	inmapEnsureScenes(next)
	inmapLoadSettings(next)
	// Experience packs, Config and Backup & restore, and the pack in force's
	// scenes, theme file and wallpaper, before the preset below (xpack.go).
	xpackEnsure(next)
	if presetName, ok := next.Variables.Custom["theme_preset"].(string); ok && presetName != "" {
		ApplyThemeColors(GetThemePreset(presetName))
	}
	// After the preset, so a reload keeps the preset's accent.
	SetAccentColor(themeAccentColor())

	// Stay on the same scene, found by name in the new list; anything that
	// indexes the old list (a pending transition, an input being edited)
	// is reset rather than left pointing past the end.
	name := ""
	if prev != nil && currentSceneIndex >= 0 && currentSceneIndex < len(prev.Scenes) {
		name = prev.Scenes[currentSceneIndex].Name
	}
	idx := 0
	for i, scene := range next.Scenes {
		if scene.Name == name {
			idx = i
			break
		}
	}
	currentSceneIndex = idx
	if pendingSceneIndex < 0 || pendingSceneIndex >= len(next.Scenes) {
		pendingSceneIndex = idx
	}
	if idx < len(next.Scenes) && selectedButtonIndex >= len(next.Scenes[idx].Elements) {
		selectedButtonIndex = -1
	}
	activeSceneIndex = -1
	activeElementIndex = -1

	appConfig = next
	// In-memory injections are lost with the replaced scene list, so the
	// Vibration row and its saved value are re-applied here.
	hapticsBind(next)
	ensureHapticsSetting(next)
	// The UI-sound settings' live value comes from the new config too
	// (sound.go; the switch is the Settings hub's row).
	bootSfxBind(next)
	// So does the background Remote Play download's switch
	// (psremote_helpers_bg.go), which its goroutine reads from a copy.
	psBgBind(next)
	// And the VPN's switch and route, which its worker reads from a copy
	// (vpn.go).
	vpnPublish(next, false)
	showToast("Config reloaded", ColorToastSuccess)
	return next
}

// StartConfigWatcher begins watching the given config file for changes.
// The reloadFn callback runs on the watcher goroutine; main passes one that
// only queues the config for the event loop (queueConfigReload).
func StartConfigWatcher(path string, reloadFn func(*Config)) {
	globalConfigWatcher = NewConfigWatcher(path)
	globalConfigWatcher.SetOnReload(reloadFn)
	globalConfigWatcher.Start()
	// On the handheld the poller starts off: it only matters while a Live
	// Builder session is paired, and liveBuilderServer.Start switches it on
	// (PERF-6). Desktop keeps it on, where the file is edited by hand.
	if P().IsHandheld() {
		globalConfigWatcher.SetPolling(false)
	}
	// An update finished at startup (update_resume.go) replaced the design
	// file after it was loaded; the watcher's baseline already holds the new
	// file's time, so it is reloaded here, like an edit pushed from the builder.
	if updateResumeReplacedConfig.Swap(false) {
		globalConfigWatcher.NotifyWritten(path, XMLSidecarPath(path))
	}
}

// StopConfigWatcher halts the config file watcher.
func StopConfigWatcher() {
	if globalConfigWatcher != nil {
		globalConfigWatcher.Stop()
	}
}
