package main

import (
	"fmt"
	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
	"math"
	"os"
	"runtime/debug"
	"strings"
	"time"
)

// frameRateCtrl owns the adaptive render cadence. It is assigned in main()
// and consulted from any file that needs the current frame budget (the Power
// Mode selector retunes it from elsewhere).
var frameRateCtrl *FrameRateController

func main() {
	defer func() {
		if r := recover(); r != nil {
			// Runs after every other deferred cleanup, then re-panics: the
			// runtime prints the stack to errors.txt and the crash report and
			// exits 2, so a crash is never mistaken for a quit (crashreport.go).
			crashOnMainPanic(r, debug.Stack())
		}
	}()

	// Secret management (set/check encrypted config values) runs before SDL so
	// it works without a display, e.g. over SSH on the handheld.
	if handled, code := handleSecretCommand(os.Args[1:]); handled {
		os.Exit(code)
	}

	InitLogging()
	// What the last session's crash left is filed as a report, and this
	// session's crash output is opened (crashreport.go).
	initCrashReports()
	// The stop signals (the firmware's power handling, a launcher that stops
	// the app) have to land on the same exit path as the Exit button: killed by
	// the default handler, the app would leave a running game over the display.
	installExitSignalHandler()
	startupMarkNow("pre-sdl")

	if err := sdl.Init(sdl.INIT_VIDEO | sdl.INIT_AUDIO | sdl.INIT_JOYSTICK | sdl.INIT_GAMECONTROLLER); err != nil {
		logFatal("startup", "SDL init error: %v", err)
	}
	// The SDL teardown calls each run under a short deadline of their own: on
	// the TrimUI they can sit in the GPU driver at exit (sdlteardown.go).
	defer sdlQuitBounded()
	// Runs before sdl.Quit on every path out of main, panics included: a chime
	// still streaming must close its device before SDL shuts audio down.
	defer shutdownChimeWait()
	startupMarkNow("sdl")

	if err := ttf.Init(); err != nil {
		logFatal("startup", "TTF init error: %v", err)
	}
	defer ttf.Quit()

	if err := img.Init(img.INIT_PNG | img.INIT_JPG); err != nil {
		logFatal("startup", "IMG init error: %v", err)
	}
	// WebP box art: best-effort, because a build without libwebp must still
	// start. Every shipped SDL_image is built with it, so the error is only a
	// log line if a custom runtime drops the codec.
	if err := img.Init(img.INIT_WEBP); err != nil {
		logWarn("main", "WEBP init unavailable: %v", err)
	}

	// Safe mode, the recovery chord and the boot guard run before the config is
	// loaded, so a start that dies loading or applying it is counted too. None
	// of them reads the config: the flag file is looked up in the folder
	// JukaHub runs in, the chord reads the pad SDL has just opened, and the
	// boot record lives in the Patch state folder, which depends only on the
	// device and the executable's folder.
	//
	// Safe mode is decided before any background work starts, so a bad plugin,
	// update feed or network setup can be bypassed for this launch without
	// touching the user's config.
	initSafeMode()
	// The recovery chord (L+R+Start held at boot) turns safe mode on and opens
	// the recovery card with the first frame (previous version, last good
	// settings, safe mode, reset settings), so a bad update or a bad setting is
	// reversible without a terminal or a card reader.
	initSafeboot()
	// The boot guard is the automatic half (bootguard.go): it counts boots, not
	// clock time. Three failed starts in a row start safe mode; a new build
	// that crashed its first starts is rolled back after InitPatchModule below.
	initBootGuard()

	// Enable text input so physical keyboard typing works in input fields
	// (both the on-screen keyboard modal and the main loop).
	sdl.StartTextInput()
	defer img.Quit()

	// Cleanup runs before the SDL teardown below. Deferred earlier it would run
	// after sdl.Quit(), where any entry that still touches an SDL-owned object
	// (a listener posting a frame, a font) is a crash on the way out instead of
	// a clean exit. (They run after the window and renderer are gone, before
	// sdl.Quit.) Once per process: a wedged SDL teardown call runs them on its
	// way out too (sdlteardown.go).
	defer runCleanupHooks()

	// Try XML config first (exported by the web editor), then fall back to JSON.
	config, configPath, err := LoadXMLConfigWithFallback("jukaconfig.json")
	if err != nil {
		logFatal("startup", "Failed to load config: %v", err)
	}
	LogScene("init").Info("config loaded", "path", configPath, "loadErrors", configHealth.LoadErrors)
	startupMarkNow("config")

	// Validate and apply defaults.
	if err := NewConfigValidator().Validate(config); err != nil {
		configHealth.RecordLoad(configPath, fmt.Errorf("validation failed: %w", err))
		Log().Error("Config validation failed", "error", err, "path", configPath)
		configHealth.Log()
		LogScene("init").Warn("config validation failed, applying defaults", "err", err)
	} else {
		configHealth.MarkLoadOK()
		configHealth.Log()
		SetLaunchMode("boot")
	}

	// Merge mutable user settings from jukauser.json over design defaults.
	mergeUserConfig(config, loadUserConfig())

	// Register user theme files from themes/*.json before any preset is applied.
	if added, failed := LoadThemeFiles(themeDir); len(added) > 0 || len(failed) > 0 {
		LogScene("init").Info("theme files loaded", "added", added, "skipped", len(failed))
		for path, err := range failed {
			LogScene("init").Warn("theme file skipped", "path", path, "err", err)
		}
	}

	// A battery saver still engaged when JukaHub last stopped is undone first,
	// so its OLED theme and motion overrides do not outlive it. Theme files
	// are loaded just above, so a saved user theme can be put back.
	restoreBatterySaverLeftover(config)

	// Expose the active config for sub-packages (Discord chat, canvas, ...).
	appConfig = config

	// Tune the collector before the first heavy allocation (thumbnail cache,
	// video frames, render layers). On the TrimUI this installs a soft heap
	// limit so a 4K video + thumbnail grid cannot balloon the Go heap.
	applyGCForDevice()

	// The alarm UI renders through the scene pipeline; register its scene at
	// startup (in-memory, never written back) so the status-bar clock and the
	// Start menu can open it on every config. The loader stays pure.
	ensureAlarmScene(config)
	// Keyboard settings (languages, suggestions, voice typing) is a Go-owned
	// screen reached from Settings and the keyboard's globe (vksettings.go).
	vkEnsureSettingsScene(config)
	// Watch Party, Toolbox, Audit toolkit, Security & trust and Report a bug
	// are Go-owned screens reached from the Tools hub (tbxui.go); no shipped
	// config defines them, so they are added in memory the same way.
	tbxEnsureScenes(config)
	// Contextual widget toggles (start at login, battery saver, time-of-day
	// theme) live on the Smart Widgets page, reached from Settings → General.
	// Injected in memory like the alarm scene, so no shipped config is edited.
	ensureSettingsWidgets(config)
	// Replay entry point + first-run welcome tour. The tour is a global modal,
	// so it is started once here and rendered above every other screen.
	ensureOnboardingEntry(config)
	onboardingStartIfNeeded(config)
	// Make the music player and game collection reachable from the Start menu
	// even on configs created before those scenes existed.
	ensureMusicScene(config)
	ensureGamesScene(config)
	// The PDF/EPUB reader has no scene in either shipped config; Files opens
	// documents into this injected one.
	pdfvEnsureScene(config)
	// Playtime stats and badges have their own screen under Tools.
	ensurePlaytimeScene(config)
	ensurePlaytimeEntry(config)
	playtimeInit(config)
	// The Tools hub, its Network tools and Wi-Fi pages (and the Calculator
	// they link to) are Go-owned scenes injected in memory; the classic "Misc"
	// page an older config still carries is retired here, before the first
	// scene is chosen, and its buttons are pointed at the hub.
	ensureToolsHubScene(config)
	// Devices, Save sync and Nearby (your other JukaHubs on this Wi-Fi) are
	// Go-owned pages injected the same way (peer.go).
	peerEnsureScenes(config)
	// The ROM manager (Tools → ROM manager) is a Go-owned page injected the
	// same way (romm_ui.go).
	ensureRommScene(config)
	// The APK Store (Start → Games) likewise (apkstore.go).
	ensureApkStoreScene(config)
	// Voice command and Bluetooth were only injected when first opened (or by a
	// reload), so their trophies came and went from the room mid-run.
	ensureVoiceScene(config)
	if btDevicesAvailable() {
		btEnsureScene(config)
	}
	// Control profiles and Local play are Go-owned screens reached from Tools
	// and Settings → Controls, injected in memory the same way.
	inmapEnsureScenes(config)
	// Experience packs, Config and Backup & restore (Tools → Your setup) are
	// injected the same way, and the pack in force gets back its own scenes,
	// theme file and wallpaper before the theme below is applied (xpack.go).
	xpackEnsure(config)
	// Apply the saved "Start JukaHub at login" setting to the platform's
	// startup folder (only ever our own launcher file).
	reconcileAutoStart(config)

	// The accent is the theme's (cyan by default), the colour Home's focus ring
	// and pills use — not $buttonColor, which is a surface colour (slate by
	// default) and left every scene's accent a navy that barely showed.
	SetAccentColor(themeAccentColor())

	// Reapply the persisted theme preset (if any) so the chosen theme is
	// visible immediately on launch, not just after re-selecting it.
	if presetName, ok := config.Variables.Custom["theme_preset"].(string); ok && presetName != "" {
		ApplyThemeColors(GetThemePreset(presetName))
	}

	// Default reduced-motion and low-power on handheld targets
	// (e.g. Trimui Smart Pro) for a safer out-of-box experience. Windows
	// development builds keep full animation. Out of the box only: with a
	// jukauser.json present both values are the user's, and turning them off
	// used to last until the next boot switched them back on.
	_, userFileErr := os.Stat("jukauser.json")
	if !IsWindows() && os.IsNotExist(userFileErr) && !config.Variables.ReducedMotion && !config.Variables.LowPower {
		config.Variables.ReducedMotion = true
		config.Variables.LowPower = true
	}

	// Load persisted Recently Played and start the (best-effort) weather fetch.
	loadRecentlyPlayed()
	loadFavorites()
	initCalc()
	alarmLoad()
	// The time zone chosen on the device and the network-time switch
	// (datetime.go), before anything prints a time.
	dateTimeInit(config)
	notesInit()
	qsInit()
	// Accessibility settings load before the first frame so a saved text size is
	// already in effect on the splash screen, not applied later.
	a11yInit(config)
	// The chosen control profile, its switches, the D-pad modes and the
	// user's profile files (inmap_profiles.go), before the first press.
	inmapInit(config)
	// The reader's text zoom is a saved preference too, loaded before the first
	// browser frame so a page opens at the size the reader left it.
	browserZoomLoad(config)
	// Push the saved mixer/backlight levels once the panel state exists, so the
	// device comes up where Quick Settings left it. Only values the user has
	// actually set are written, and it runs off the startup path because the
	// mixer probe may need to wait on a daemon.
	go qsApplyPersistedSystemState()
	// Restore the audio-reactive LEDs if the user left them on. SetLEDSync is a
	// no-op on a device with no addressable LED, and logs why.
	StartLEDSyncIfEnabled(config)
	// Network work is queued until after the first frame: weather, the tool
	// probe, telemetry, the update check and the network/companion listeners all
	// compete with cold font rasterisation, and none of them are needed to paint
	// the home screen. See runDeferredStartupFor.
	if safeModeAllows(SafeFeatureNetwork) {
		deferStartupTask("weather", func() { startWeather(config) })
	}

	// Tool readiness: check whether the helper tools (yt-dlp, ffmpeg, ffprobe)
	// are present and auto-install the missing ones, showing the install bar
	// while they download. This is non-fatal: offline launches stay usable and
	// retry on the next run. The boot card's tool check usually starts the
	// install during the splash; this pass then stands down (bootchecks.go).
	if safeModeAllows(SafeFeatureToolInstall) {
		deferStartupTask("tool-health", bootDeferredToolHealth)
	}

	// Patch module: load persisted state and roll back any interrupted update
	// transaction so JukaHub always launches in a consistent state.
	InitPatchModule()
	// A rollback the boot guard decided on needs Version history, loaded just
	// above; it restarts JukaHub into the previous version through the swap
	// helper, and this launch stays in safe mode until then.
	bootGuardStartRollbackIfDue(config)
	// Folder skeletons: the game-profile catalog, the compatibility catalog
	// and its starter reports, the Save Manager's snapshot/sync/resume
	// folders, the Curation Hub and the Windows-apps drop folders. They are
	// mkdirs and README writes — a few dozen stat calls on a cold SD card —
	// that nothing reads before one of those panels opens, so they run beside
	// the window and the splash instead of in front of the first frame. The
	// catalogs, discovery and feeds behind them stay lazy as before.
	go func() {
		InitGameProfiles()
		InitCompatibility()
		InitSaveManager()
		InitCuration()
		InitWindowsApps()
	}()

	// Show the release notes once on the first launch of a newly installed
	// build. The notes were cached before the restart, so this works offline.
	if safeModeAllows(SafeFeatureUpdates) {
		maybeShowChangelogAfterUpdate(config)
	}
	// Opt-in device census (off by default, no identifier).
	if safeModeAllows(SafeFeatureNetwork) {
		deferStartupTask("telemetry", func() { TelemetryBoot(config) })
	}

	// Automatic updater: check the release feed in the background and offer a
	// newer build in Patch. It never downloads or installs without the user
	// confirming there.
	if safeModeAllows(SafeFeatureUpdates) {
		deferStartupTask("update-check", func() { startAutoUpdateCheck(config) })
	}
	// Automatic emulator-core updates: on the handheld the cores are the
	// packages that matter, and waiting for someone to open the Emulators page
	// and press Update is how a card ends up running a nightly from months ago.
	// It downloads and activates in the background and reports through the same
	// install bar, so the progress is visible from any screen. Its scheduler is
	// started once, by the boot card's "cores" step (bootEnqueueChecks), which
	// reports it on the splash.

	// Expired thumbnail and feed entries leave jukahub.cache two minutes in,
	// after boot's own reads.
	deferStartupTask("cache-sweep", func() { sweepSharedCacheLater(2 * time.Minute) })
	// Temp video downloads a crash or power cut left on the card.
	deferStartupTask("temp-videos", func() { go sweepTempVideos() })
	// RetroArch settings a launch changed and a crash or power cut left in
	// retroarch.cfg go back to the user's (racfgscope.go; its own goroutine).
	deferStartupTask("retroarch-scope", raScopeStartupRecover)
	// Plugins can add themes, ROM formats, control profiles and experience
	// packs (plugx.go), so their folder is scanned once after the first frame
	// rather than only when the Plugins page opens; safe mode skips the scan.
	deferStartupTask("plugins", func() { LoadPlugins(config) })
	// The experience pack's start screen, the weekly backup when it is due and
	// the check for newly shipped defaults (Tools → Your setup).
	deferStartupTask("xpack-start", func() { xpackOpenStartScene(config) })
	deferStartupTask("bkup-auto", func() { bkupMaybeAutoBackup(config) })
	deferStartupTask("cfgdoc-upstream", cfgdocCheckUpstreamAsync)
	// Box art for games without a cover, found in the background when online;
	// it picks its own moment and stands down in safe mode (boxart_auto.go).
	deferStartupTask("boxart-auto", func() { startBoxArtAuto(config) })

	// Start disk space pie chart auto-refresh (every 5 seconds).
	startDiskPieAutoRefresh()

	// Start network connectivity monitoring (tool health is already started
	// above; running it twice would double the startup probes).
	if safeModeAllows(SafeFeatureNetwork) {
		deferStartupTask("network-monitor", startNetworkMonitor)
	}

	// Watch the audio output (Bluetooth device or the 3.5mm jack) so the top-bar
	// chip can name a connected headset while nothing plays. Local probes only,
	// on their own ticker.
	deferStartupTask("audio-output", startAudioOutputMonitor)

	// Watch the kernel's CPU thermal limit on the handheld; the poller parks
	// whenever a game owns the screen or the desktop window is hidden.
	deferStartupTask("thermal-monitor", startThermalMonitor)

	// A USB or Bluetooth keyboard plugged into the handheld turns the hints into
	// keyboard keys (inputhints.go); a PC always has one, so this is a no-op there.
	deferStartupTask("input-hints", startInputHintsMonitor)

	// Tools panel / FTP: publish the initial IP and server state, and make sure
	// the listener never outlives the app.
	initToolsModule()
	publishSSHState()
	defer ftpSrv.Stop()
	defer sshSrv.Stop()
	// The Phone Companion starts just after the first frame so the QR in the
	// home header is live by the time the user looks at it. It retries from the
	// event loop if Wi-Fi comes up later.
	deferStartupTask("companion", companionEnsureStarted)
	defer stopCompanionServer()

	// Start screensaver idle monitor.
	// Set screensaver timeout from config
	if config.Variables.ScreensaverTimeout > 0 {
		screensaverIdleTimeout = time.Duration(config.Variables.ScreensaverTimeout) * time.Second
	}
	startScreensaverMonitor()

	// Game tick for Pong/Snake/Tetris. The ticker only runs while a game is
	// open: parked, it used to wake the CPU sixty times a second for the whole
	// session to find there was nothing to tick (startGame wakes it).
	go func() {
		ticker := time.NewTicker(time.Second / 60)
		defer ticker.Stop()
		for {
			if activeGame == gameNone {
				ticker.Stop()
				<-minigameWake
				ticker.Reset(time.Second / 60)
				continue
			}
			<-ticker.C
			// A ringing alarm pauses the mini-games under its takeover, the
			// way it pauses a running emulator (alarmvol.go).
			if alarmFiringActive() {
				continue
			}
			tickActiveGame()
		}
	}()

	// Initialize scene index early so logging and window setup can safely
	// reference the first scene.
	currentSceneIndex = 0

	// Pick the logical canvas for this panel (the Brick/Hammer family needs the
	// compact 4:3 canvas). Scene geometry is scaled once here, after every
	// injected scene has been added, so the whole UI uses one coordinate space.
	//
	// When the hardware cannot be named the panel still can: the 16:9 design
	// drawn into a 4:3 display is letterboxed into a strip with bars above and
	// below, so the display's own aspect picks the compact canvas instead.
	if !applyDeviceUIProfile(config) {
		if mode, err := sdl.GetCurrentDisplayMode(0); err == nil {
			applyPanelCanvas(config, mode.W, mode.H)
		}
	}

	// Optional resolution / fullscreen configuration (great for TrimuiSmartPro)
	screenW, screenH := 1280, 720
	if config.Variables.ScreenWidth > 0 {
		screenW = config.Variables.ScreenWidth
	}
	if config.Variables.ScreenHeight > 0 {
		screenH = config.Variables.ScreenHeight
	}
	// The saved choice wins on every platform: F11 and Settings write it, so a
	// Windows session left fullscreen comes back that way instead of being
	// gated behind the platform default.
	fullscreen := config.Variables.Fullscreen

	windowFlags := sdl.WINDOW_SHOWN
	if fullscreen {
		windowFlags |= sdl.WINDOW_FULLSCREEN_DESKTOP
	}
	// Keep the window resizable on Windows so the logical 1280x720 canvas can
	// be letterboxed to arbitrary window sizes for development/testing.
	if P().ResizableDefault() {
		windowFlags |= sdl.WINDOW_RESIZABLE
	}
	startupMarkNow("modules")
	LogSceneOp(config.Scenes[currentSceneIndex].Name, "init").Info("creating window",
		"platform", P().Name(),
		"width", screenW,
		"height", screenH,
		"fullscreen", fullscreen,
		"resizable", P().ResizableDefault(),
	)
	screenWidth, screenHeight = int32(screenW), int32(screenH)
	// The Browser has no shipped handheld scene either. It is injected once the
	// panel size is known so its address bar, page area and toolbar scale to
	// that display instead of assuming the 1280x720 design size.
	ensureBrowserScene(config)
	window, err := sdl.CreateWindow(config.Title, sdl.WINDOWPOS_CENTERED, sdl.WINDOWPOS_CENTERED, screenWidth, screenHeight, uint32(windowFlags))
	mainWindow = window
	if err != nil {
		logFatal("startup", "Window creation error: %v", err)
	}
	defer sdlDestroyWindowBounded(window)
	// Title bar, Alt-Tab and the taskbar read the window's own icon, so set it
	// before anything is drawn instead of relying on the executable resource.
	applyWindowIcon(window)

	// On the handheld prefer the device's GLES2 GPU renderer explicitly: on
	// the A133P boards that is the PowerVR GE8300 driver. If the vendor stack
	// can't provide it, SDL falls back to its normal default renderer per the
	// hint's documented behaviour, and the software fallback below still
	// stands. Windows development keeps its usual Direct3D default.
	if !IsWindows() {
		sdl.SetHint(sdl.HINT_RENDER_DRIVER, "opengles2")
		// Naming a render driver switches SDL2's command batching off (it
		// assumes the app may mix in its own GL calls), so every Copy and
		// FillRect became its own draw call with its own state changes — on
		// the PowerVR driver that is CPU time per blit. JukaHub never touches
		// GL directly, which is the contract this hint asks for. Windows names
		// no driver, so its default renderer already batches.
		sdl.SetHint(sdl.HINT_RENDER_BATCHING, "1")
	}

	// Touch maps to mouse exactly once (see disableSyntheticTouchMouse); the
	// duplicate tap pipeline turned a single play-card tap into two yt-dlp
	// resolves.
	disableSyntheticTouchMouse()

	renderer, err := sdl.CreateRenderer(window, -1, sdl.RENDERER_ACCELERATED)
	if err != nil {
		// Fall back to a software renderer when the accelerated backend is
		// unavailable (e.g. dummy/offscreen drivers used in CI/headless).
		renderer, err = sdl.CreateRenderer(window, -1, sdl.RENDERER_SOFTWARE)
		if err != nil {
			logFatal("startup", "Renderer creation error: %v", err)
		}
	}
	defer sdlDestroyRendererBounded(renderer)

	// Log which renderer we actually ended up with. The GPU driver name
	// decides where the per-frame cost lands (GPU passes vs CPU fills) and
	// whether the cached texture paths stay cheap, so record driver, name
	// and max texture size once at startup (adaptive.go; verifiable in
	// renderpipeline_test.go).
	logRenderPipeline(renderer)

	// Mouse and touch arrive in physical window pixels. The handheld canvases
	// are scaled to their panel (notably 960x720 onto the Brick's 1024x768), so
	// seed the inverse transform now instead of waiting for a resize event that
	// never comes in fullscreen.
	if ow, oh, err := renderer.GetOutputSize(); err == nil && ow > 0 && oh > 0 {
		updateWindowScale(ow, oh)
	} else if ww, wh := window.GetSize(); ww > 0 && wh > 0 {
		updateWindowScale(ww, wh)
	}

	// Render into a fixed logical canvas and let SDL scale it to the physical
	// window size. This preserves the layout on Windows when the window is
	// resized and maps the compact 4:3 canvas onto the Brick panels.
	renderer.SetLogicalSize(screenWidth, screenHeight)

	// Enable alpha blending for draw operations (FillRect, etc.) so
	// semi-transparent overlays/panels composite correctly over the
	// background image instead of painting opaque.
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)

	// Boot splash (splash.go, bootos.go): an app launch gets a quick hello in
	// whatever is left of the two-second boot budget. A warm launch skips it
	// entirely — the greeting is for a cold start, and a relaunch has the card's
	// caches warm. A cold card with nothing left opens the UI directly (see
	// startup.go). A power-on gets the full boot instead.
	startupMarkNow("window")
	bootElapsed := startupElapsed()
	allotment := splashAllotment(bootElapsed, startupTarget(), splashReserve(bootElapsed))
	if startupWarmStart(time.Now()) {
		allotment = 0
		logInfo("startup", "warm start (last launch within %s); splash skipped", startupWarmWindow)
	}
	if note := startupBudgetNote(bootElapsed); note != "" && allotment > 0 {
		logInfo("startup", "%s", note)
	}
	// Started with the device (or asked to play it every time): the full boot,
	// whatever the budget left (bootos.go). Decided before the queue is built,
	// so the warm render the first screen arrives from is queued for it too.
	allotment = bootOSPlanStartup(config, allotment)
	// Everything below is real setup that the animation does not need, so it is
	// queued and executed between splash frames instead of after them
	// (bootwork.go). The cache handle is the one piece that must not block a
	// frame at all, so it opens on its own goroutine while the warp runs.
	bootWarmCacheInBackground()
	// The checks the boot card reports are real, and they go first so they run
	// beside everything below: settings health, a write probe on the card, the
	// helper-tool inventory (missing tools start downloading during the splash),
	// the emulator-core updater and the UI sounds (bootchecks.go).
	bootEnqueueChecks(config)

	// Create placeholder texture for missing thumbnails
	bootEnqueue("placeholder-texture", func() {
		placeholderSurface, _ := sdl.CreateRGBSurfaceWithFormat(0, 120, 90, 32, sdl.PIXELFORMAT_RGBA8888)
		if placeholderSurface == nil {
			return
		}
		placeholderSurface.FillRect(&sdl.Rect{X: 0, Y: 0, W: 120, H: 90}, sdl.MapRGBA(placeholderSurface.Format, 30, 35, 50, 255))
		placeholderTexture, _ = renderer.CreateTextureFromSurface(placeholderSurface)
		placeholderSurface.Free()
	})

	bootEnqueue("gamepad", func() {
		// Add Xbox 360 controller mapping for Trimui Smart Pro / Linux
		sdl.GameControllerAddMapping("030000005e0400008e02000014010000,X360 Controller,a:b0,b:b1,back:b6,dpdown:h0.4,dpleft:h0.8,dpright:h0.2,dpup:h0.1,guide:b8,leftshoulder:b4,leftstick:b9,lefttrigger:a2,leftx:a0,lefty:a1,rightshoulder:b5,rightstick:b10,righttrigger:a5,rightx:a3,righty:a4,start:b7,x:b2,y:b3,platform:Linux,")

		// Initialize gamepad support
		if sdl.NumJoysticks() > 0 {
			logDebug("gamepad", "Found %d joystick(s)", sdl.NumJoysticks())
			for i := 0; i < sdl.NumJoysticks(); i++ {
				if sdl.IsGameController(i) {
					// go-sdl2 has no GameControllerFromIndex: the name/ID are
					// queried per index, and the handle comes from Open.
					logDebug("gamepad", "Controller %d: %s (GUID: %s)", i,
						sdl.GameControllerNameForIndex(i),
						sdl.JoystickGetGUIDString(sdl.JoystickGetDeviceGUID(i)))
				} else {
					logDebug("gamepad", "Joystick %d: %s (not a game controller)", i, sdl.JoystickNameForIndex(i))
				}
			}
			if c := sdl.GameControllerOpen(0); c != nil {
				// Closed by the defer below once the loop owns the handle; a
				// defer inside this step would close it the moment it returns.
				gameController = c
				logDebug("gamepad", "Opened controller 0: %s", c.Name())
			}
		} else {
			logDebug("gamepad", "No joysticks/gamepads detected")
		}
		// The hints' first vocabulary, now that the pad is known: the first
		// frame already names keys on a PC without one (inputhints.go).
		inputHintsTick(config)
	})

	// Find the vibration motor before the Settings switch is injected: the row
	// exists only on hardware that can actually buzz.
	bootEnqueue("haptics", func() {
		hapticsBind(config)
		ensureHapticsSetting(config)
	})

	bootEnqueue("keyboard", initKeyboard)

	// More than one worker: prefetching a results grid is latency-bound, and a
	// single worker downloaded exactly one thumbnail per network round trip.
	bootEnqueue("thumbnail-workers", func() {
		thumbnailDownloadCh = make(chan string, 256)
		for i := 0; i < thumbnailWorkerCount(); i++ {
			go thumbnailPrefetchWorker()
		}
	})

	// The focus graph is the last thing the first frame needs and the first
	// thing it would otherwise wait for.
	bootEnqueue("focus-graph", func() {
		currentSceneIndex = 0
		// The first scene skips the transition that normally lays a Tools menu
		// out as tiles, so it is applied here before the focus graph is built.
		menuTilesApply(config, currentSceneIndex)
		selectedButtonIndex = findFirstSelectableElement(config.Scenes[currentSceneIndex])
		focusEngine.SetGraph(config.Scenes[currentSceneIndex].Name, focusEngine.BuildGraph(config.Scenes[currentSceneIndex]))
		focusEngine.SyncSelected()
	})

	// The first frame's own draw is the largest remaining cost: every label and
	// shape is rasterised cold. Drawing the scene once into a throwaway target
	// while the splash is up leaves those caches warm, so the frame the user
	// sees is fast. Runs after the focus graph, which is what lays the scene out.
	//
	// Only worth it when there is an animation to hide it in: on a warm start
	// the claim on the splash is zero, and paying for a second cold draw with
	// nothing on screen would make that launch slower, not faster.
	if allotment > 0 {
		bootEnqueue("scene-warm", func() {
			warmSceneCaches(renderer, config, config.Scenes[currentSceneIndex])
		})
	}

	// Hardware volume buttons: a long hold skips a track even while the screen
	// is off or the player is in the background. The OS still owns the short
	// press, so nothing about the volume itself changes.
	bootEnqueue("volume-buttons", startVolumeButtonWatcher)

	// An idle screen that died in its low-power state (saver_power.go) left
	// the CPU clocked down: put it back first, so the values recorded next are
	// the firmware's and not the crashed idle screen's.
	saverPowerRecover()
	// Remember the governor and ceiling the machine booted with, so the teardown
	// can put the firmware back exactly as it was found even if a child died
	// holding a performance profile.
	captureBootCPUSettings()

	// The startup chime rises with the boot (bootchime.go). A warm relaunch
	// draws no splash, so it starts quietly too; the full boot starts its own,
	// later in its score, so the bells land on its blooms (bootos.go).
	if allotment > 0 && !bootOSPowerOnActive() {
		bootChimeStart(config)
	}
	ShowSplash(renderer, config, allotment)
	startupMarkNow("splash")
	// Whatever did not fit between frames runs now — and on a warm start, where
	// no splash was drawn, this is where all of it happens.
	bootWorkDrain()
	startupMarkNow("boot-work")
	config.Variables.LoadingSpinner = false
	// Whatever pad is open at exit is closed then — not the one open now: a
	// hot-plug (gamepad_hotplug.go) may have closed and replaced it, and a
	// second Close on that handle would be a use-after-free in C.
	defer replaceGameController(nil)

	// Start config file watcher for hot-reload
	StartConfigWatcher("jukaconfig.json", func(newCfg *Config) {
		// This runs on the watcher goroutine: the config is only handed over
		// here, and the event loop applies it between frames
		// (applyConfigReload, hotreload.go).
		queueConfigReload(newCfg)
	})
	defer StopConfigWatcher()

	// Wi-Fi designer: the static web builder pushes jukaconfig.json here and the
	// watcher above reloads it in place. Off on the handheld until switched on
	// (Settings → Network → Live Builder), guarded by a pairing code with a
	// lockout, and off again after 30 idle minutes (livebuilder.go).
	startLiveBuilderIfEnabled()
	defer stopLiveBuilder()

	// Adaptive frame rate and memory monitoring for TSP ARM64 devices.
	// The controller is package-level so the Power Mode selector
	// (powermode.go, quicksettings.go) can retune the same controller that
	// this loop sleeps against, without threading it through every scene.
	frameRateCtrl = NewFrameRateController()

	memMonitor := NewMemoryMonitor()
	defer memMonitor.Stop()
	LogDeviceDiagnostics()

	// An archive extraction is cancelled on the way out, after the scene work
	// below, and given a moment to delete its staging folder
	// (files_extract.go).
	defer feExtractShutdown()
	// Cancel the active scene's background work on the way out, before the
	// Cleanup hooks and the display teardown run. Registered last so it is the
	// first deferred call to execute.
	defer shutdownSceneWork()
	// ...except on a panic: the crash guard runs before it, writes the panic
	// down, stops a game that owns the screen and bounds the cleanup below, so a
	// panic can neither exit 0 nor hang on a frozen frame (crashreport.go).
	defer crashGuardMain()

	// A config that had to be recovered from its backup is reported here, not
	// during load: the toast needs the render loop's fonts and the audio device.
	if notice := takeUserConfigRecoveryNotice(); notice != "" {
		ShowToast(notice, ToastKindWarning)
	}
	if notice := takeConfigRecoveryNotice(); notice != "" {
		ShowToast(notice, ToastKindWarning)
	}
	// The recovery card: the chord's recovery choices, why this launch is in
	// safe mode, or "JukaHub closed unexpectedly" after a crash (recoverycard.go).
	recoveryCardOpenAtBoot(config)

	running := true
	startupReported := false
	// The tasks deferStartupTask queued run a few per frame once the first
	// frame is up (startup.go runDeferredStartupFor).
	deferredStartupPending := false
	// Alarms are checked on the clock, not on frameCount: the adaptive frame
	// rate and the darked ambient panel both run far below 60 fps, which would
	// make a frame-counted check ring late.
	lastAlarmCheck := time.Now()
	// Set for the duration of one game handover, consumed on the first frame
	// presented afterwards: that is where the "While you were playing" summary
	// goes, because under the old behaviour every completion raced its own
	// invisible toast and only the last one survived, unreadable.
	whileAwayWasActive := false
	wallpaperParkedForGame := false
	for running {
		// The frame clock starts here, before this iteration's input and scene
		// work: what the frame-time histogram measures is everything a frame
		// costs up to Present, not just the draw calls. Iterations that park
		// (a game owns the screen) present nothing and record nothing.
		frameStart := time.Now()
		// A game, emulator, port or Wine app owns the screen. JukaHub and the
		// child share one framebuffer on handheld firmwares (see
		// launch_handover_test.go for the same fight after a self-update), so
		// any frame presented here makes the panel blink between the UI and
		// the game. Park the presentation loop: drain events so the queue
		// cannot fill and a quit request still stops the app, keep the
		// widgets tick running (the low-battery guard must still save and
		// suspend mid-game, and the battery saver via toastText), and let the
		// child have the framebuffer to itself. A ringing alarm is the one
		// exception: it paused the game (alarmvol.go) and takes the screen
		// until it is snoozed or dismissed.
		if gameScreenSuspended() && !alarmFiringActive() {
			whileAwayWasActive = true
			if !wallpaperParkedForGame {
				gifWallPauseForGame()
				wallpaperParkedForGame = true
			}
			for event := sdl.PollEvent(); event != nil; event = sdl.PollEvent() {
				if _, quit := event.(*sdl.QuitEvent); quit {
					running = false
				}
			}
			widgetsTick(config)
			// Nothing listens behind a game: the voice orb goes (voiceorb.go).
			voiceOrbAway()
			// Mouse in games and apps: a goroutine watches the pad for the
			// Select + L1 chord while the child runs (vcursor_bridge_linux.go).
			vcursorBridgeTick(config)
			if now := time.Now(); now.Sub(lastAlarmCheck) >= time.Second {
				lastAlarmCheck = now
				alarmCheck(config, now)
			}
			// An exit request while a game owns the screen (power-key shutdown,
			// the Exit button reached from the companion, a signal) used to be
			// ignored here, because this branch continues before the request is
			// checked below. The teardown card cannot be drawn over the game, but
			// app still has to quit: the shutdown path stops the child first.
			if exitRequested.Load() {
				running = false
				continue
			}
			sdl.Delay(100)
			frameCount++
			continue
		}
		if wallpaperParkedForGame && !gameScreenSuspended() {
			gifWallResumeAfterGame()
			wallpaperParkedForGame = false
		}
		// The game handed the screen back. One summary replaces the burst of
		// toasts that piled up while no frame was being presented. (An alarm
		// drawn over a paused game is not the game handing it back.)
		if whileAwayWasActive && !gameScreenSuspended() {
			whileAwayWasActive = false
			whileAwayNotify()
		}
		// Keep the home-header pairing QR live: start the companion when Wi-Fi
		// appears and rebuild the code if DHCP moved the address. The first
		// frame is painted before this runs, so the listener never delays boot.
		if startupReported {
			companionEnsureStarted()
		}
		// Phone Companion actions arrive on an HTTP goroutine. They are queued
		// and applied here, on the main loop, so a phone keypress takes the same
		// path as the handheld's own input.
		drainCompanionQueue(config)
		// Exit request (home Exit button or config "exit" trigger): leave the
		// loop, then run the glass teardown below (stop children, persist, restore
		// the CPU, close files) before the deferred SDL cleanup.
		if exitRequested.Load() && !exitHandled {
			exitHandled = true
			running = false
			continue
		}
		// Set when a key press only woke the screen, so the text event SDL
		// sends with it in the same batch is dropped too.
		wakeSwallowedText := false
		for event := sdl.PollEvent(); event != nil; event = sdl.PollEvent() {
			applyButtonSwap(config, event)
			switch e := event.(type) {
			case *sdl.QuitEvent:
				running = false
			case *sdl.DropEvent:
				// A file dragged onto the window (desktop build). The handheld has
				// no pointer, so this never fires there.
				pollDropEvent(config, e)
			case *sdl.WindowEvent:
				switch e.Event {
				case sdl.WINDOWEVENT_SIZE_CHANGED:
					w, h := mainWindow.GetSize()
					updateWindowScale(w, h)
				case sdl.WINDOWEVENT_MINIMIZED, sdl.WINDOWEVENT_HIDDEN:
					// Nothing this process draws can be seen, so the background
					// pollers park (backgroundwork.go). The loop keeps running: SDL
					// still delivers the restore below.
					setWindowMinimised(true)
				case sdl.WINDOWEVENT_RESTORED, sdl.WINDOWEVENT_SHOWN:
					setWindowMinimised(false)
				}
			case *sdl.KeyboardEvent:
				if e.Type == sdl.KEYUP {
					// The release of a key that only woke the screen is forgotten
					// here; nothing below acts on a release except ending a hold.
					wakeSwallowRelease(wakeInputKey, int32(e.Keysym.Scancode))
					// Only the audio player cares: letting go of a volume key ends a
					// hold, so the next tap changes the volume instead of looking
					// like one long press that already skipped a track.
					if musicSceneActive() {
						musicHandleVolumeKey(e)
					}
					// Releasing the key that opened an on-screen keyboard ends
					// the watch on its auto-repeats (browserTextOpenerRepeat).
					browserTextOpenerRepeat(e)
					// F4 let go after a long hold sends the voice orb's clip.
					voiceOrbKeyUp(e, config)
					continue
				}
				if e.Type == sdl.KEYDOWN {
					frameRateCtrl.NotifyInput()
					// A real key press makes the hints name keys (inputhints.go).
					inputHintsNoteKey(e)
					// The key that opened an on-screen keyboard may still be held:
					// its auto-repeats neither type into the field it opened nor
					// act. vkDropOpenerText dropped the first character; this
					// drops the rest, as runInputModal does for the address bar
					// ("Playlist 3xxx" from a held X).
					if virtualKeyboardActive && browserTextOpenerRepeat(e) {
						continue
					}
					// A ringing alarm owns the keyboard: snooze and dismiss are the
					// only actions it accepts, and nothing behind the card reacts —
					// not even to the key's text, which SDL sends after the key,
					// once the ring it answered is already over.
					if alarmHandleKey(e) {
						wakeSwallowedText = true
						continue
					}
					// The idle screen's own backdrop-clear button answers without
					// waking it, so the photo can be taken away from the screen
					// that shows it instead of a trip to Files. With nothing to
					// clear the press is not consumed and wakes as usual.
					if ss.active && screensaverClearKey(e.Keysym.Sym, e.Keysym.Mod) &&
						screensaverBackdropPress(config, time.Now()) {
						continue
					}
					// A key that wakes the screen only wakes it (and the text it
					// would have typed is dropped with it).
					if wakeSwallowPress(wakeInputKey, int32(e.Keysym.Scancode), e.Repeat != 0) {
						wakeSwallowedText = true
						continue
					}
					// The voice orb's keys (voiceorb.go): F4 anywhere, and Enter,
					// Esc and the arrows while it listens or offers a choice. The
					// text a taken key would type is dropped with it.
					if voiceOrbKey(e, config) {
						wakeSwallowedText = true
						continue
					}
					// Shared clipboard chords (Ctrl+V paste, Ctrl+C copy) run before
					// every modal and scene dispatch, because each of the four text
					// surfaces — Browser, Notes, Terminal and Chat — can own the
					// keyboard in a different state. The routing in clipboard.go is
					// a no-op when nothing is being edited, so the chords fall
					// through here and ordinary handling proceeds untouched.
					// While the history overlay is up it owns every key; Enter already
					// copies the focused entry back (see handleClipboardInput).
					if !clipSearchOpen && e.Keysym.Sym == sdl.K_v && (e.Keysym.Mod&sdl.KMOD_CTRL) != 0 && (e.Keysym.Mod&sdl.KMOD_SHIFT) == 0 {
						if clipboardPasteIntoActiveField(config) {
							continue
						}
					}
					if !clipSearchOpen && e.Keysym.Sym == sdl.K_c && (e.Keysym.Mod&sdl.KMOD_CTRL) != 0 {
						if clipboardCopyFromActiveField(config) {
							continue
						}
					}
					// A video being prepared is modal: only Esc (cancel) is accepted,
					// so the results grid cannot scroll behind the spinner.
					if videoPrepareActive() {
						if e.Keysym.Sym == sdl.K_ESCAPE {
							cancelVideoPrepare()
						}
						continue
					}
					// The dock screen owns every key while it is up, including the
					// first press that wakes a darked panel.
					if ambientHandleKey(e, config) {
						continue
					}
					// The recovery card (crash, safe mode, recovery choices) is
					// modal and owns every key while it is up.
					if recoveryCardHandleKey(e, config) {
						continue
					}
					if launchFailure.open {
						handleLaunchFailureKey(e)
						continue
					}
					// The first-run tour is the topmost modal: while it is open it
					// owns every key, including escape and the usual global chords.
					// Its setup checklist opens real panels, so while one of those
					// is up the tour steps aside and lets the keys through.
					if onboarding.active && !onboardingCoveredByOverlay() {
						handleOnboardingKey(e)
						continue
					}
					// Watch Party is a global modal; F8 opens and closes it while
					// no text field is capturing the keyboard.
					if e.Keysym.Sym == sdl.K_F8 && !virtualKeyboardActive {
						partyTogglePanel()
						continue
					}
					if shortcutsOpen {
						handleShortcutsInput(e)
						continue
					}
					// The controller legend is modal too, and owns every key while
					// it is up; F1 inside it hands over to the shortcuts overlay.
					if controlsLegend.open {
						handleControlsLegendKey(e)
						continue
					}
					if clipSearchOpen {
						handleClipboardInput(e)
						continue
					}
					// An enlarged QR code is the topmost card: it owns every key
					// while it is up, including the panel's own bindings.
					if handleQRZoomKey(e) {
						continue
					}
					// The QR overlay is modal: it owns every key while it is up.
					if handleCompanionKey(e, config) {
						continue
					}
					if globalSearchOpen {
						handleGSInput(e, config)
						continue
					}
					// LED style panel is modal and consumes all keys while open.
					if ledPanel.open && handleLEDPanelKey(e, config) {
						continue
					}
					// Podcast library is modal and consumes all keys while open,
					// except while its feed keyboard is up: Backspace there used to
					// remove a show instead of a character.
					if podPanel.open && !virtualKeyboardActive && handlePodcastPanelKey(e, config) {
						continue
					}
					// IPStream library is modal and consumes all keys while open.
					if ipPanelOpen && handleIPPanelKey(e, config) {
						continue
					}
					// Process list is modal and consumes all keys while open.
					if processPanel.open && handleProcessKey(e, config) {
						continue
					}
					// Memory panel is modal and yields only to its value keyboard.
					if memPanel.open && handleMemKey(e, config) {
						continue
					}
					// Text editor is modal and yields only to its line keyboard.
					if fileEditor.open && handleFileEditorKey(e, config) {
						continue
					}
					// Controller test is modal; the pad closes it by holding SELECT.
					if ctrldiag.open && handleCtrldiagKey(e, config) {
						continue
					}
					// Update prompt is the topmost modal.
					if updatePrompt.open && handleUpdatePromptKey(e, config) {
						continue
					}
					// Community hub is above the other overlays.
					if communityPanel.open && handleCommunityKey(e, config) {
						continue
					}
					// Community theme library is modal when open.
					if themeCommunityPanel.open && handleThemeCommunityKey(e, config) {
						continue
					}
					// Power tools are above the app chrome while open.
					if powerTools.open && handlePowerToolsKey(e, config) {
						continue
					}
					// RetroAchievements is modal. While the keyboard is editing a
					// username or API key the keyboard owns the keys instead.
					if raPanel.open && !virtualKeyboardActive && handleRetroAchievementsKey(e, config) {
						continue
					}
					// Game Profiles is modal and consumes all keys while open.
					if gpPanel.open && handleGameProfileKey(e, config) {
						continue
					}
					// Smart Pro Compatibility keeps the ROM walk off the render
					// thread and owns every key while open.
					if cpPanel.open && handleCompatKey(e) {
						continue
					}
					// Kept core builds is modal in the same way.
					if kbPanel.open && handleCoreBuildsKey(e) {
						continue
					}
					// Save Manager is modal and owns every key while open.
					if savePanel.open && handleSaveManagerKey(e) {
						continue
					}
					// Curation Hub is modal and owns every key while open.
					if curationPanel.open && handleCurationKey(e, config) {
						continue
					}
					// Windows Apps is modal and consumes all keys while open.
					if winAppsPanel.open && handleWindowsAppsKey(e, config) {
						continue
					}
					if castPanel.open && handleCastKey(e, config) {
						continue
					}
					// The Audio panel yields to its playlist-name keyboard.
					if musicSettings.open && !virtualKeyboardActive && handleMusicSettingsKey(e, config) {
						continue
					}
					if a11y.open && handleAccessibilityKey(e, config) {
						continue
					}
					// The PS Remote Play client panel is modal too, but its address,
					// Account-ID and PIN keyboard owns the keys while it is up:
					// Backspace and Esc used to close the panel and Enter re-ran a row.
					if chiakiPanel.open && !virtualKeyboardActive && handleChiakiKey(e) {
						continue
					}
					// PortMaster Ports is modal too.
					if portsPanel.open && handlePortsKey(e) {
						continue
					}
					// The browser's link picker is modal too.
					if browserLinkPanel.open && handleBrowserLinksKey(e, config) {
						continue
					}
					// As is the site picker that forgets one jar's worth of cookies.
					if browserCookiePanel.open && handleBrowserCookiesKey(e, config) {
						continue
					}
					// And the saved-page picker, where A opens and X removes.
					if browserBookmarkPanel.open && handleBrowserBookmarksKey(e, config) {
						continue
					}
					// And the browser's site-info sheet.
					if brwInfo.open && brwInfoKey(e, config) {
						continue
					}
					// And the browser's Select menu, which M or the Menu key opens
					// over the page and which owns the keys while it is up
					// (brw_menu.go).
					if brwMenuKey(renderer, e, config) {
						continue
					}
					// The stick calibration wizard is modal while it runs.
					if calibrationPanel.open && handleStickCalibrationKey(e, config) {
						continue
					}
					// Smart Home is modal too.
					if smartHomePanel.open && handleSmartHomeKey(e, config) {
						continue
					}
					// LAN play is modal over the library. The address field is typed
					// on the on-screen keyboard, which is handled earlier, so only
					// route here while no text field is capturing keys.
					if netplayPanel.open && !virtualKeyboardActive && handleNetplayKey(e, config) {
						continue
					}
					// Downloads is modal too, and yields to the keyboard it opens
					// for a link.
					if dlPanel.open && !virtualKeyboardActive && handleDownloadsKey(e, config) {
						continue
					}
					// The game library is modal too. It yields to the on-screen
					// keyboard so an address typed from LAN play is not swallowed by
					// arrow-key navigation behind it.
					if gameLib.open && !virtualKeyboardActive && handleGameLibraryKey(e, config) {
						continue
					}
					// The Emulators panel is modal too.
					if emuPanel.open && handleEmuKey(e, config) {
						continue
					}
					// Quick Settings takes priority when open.
					if qs.open {
						handleQSInput(e, config)
						continue
					}
					// The wallpaper picker covers whatever scene it was opened
					// over (Home, from Start or search): it owns the keys while it
					// is up, as it owns the pad (controller.go). Further down,
					// Home's own key handling took the arrows, Enter and Esc first.
					if wpOpen && !virtualKeyboardActive {
						handleWallpaperInput(e, config)
						continue
					}
					// Chat member list and profile card float above the chat panel
					// and take every key while one is up.
					if !virtualKeyboardActive && chatOverlayOwnsInput(config) && handleChatOverlayKey(e, config) {
						continue
					}
					// A selected message turns the composer into its action bar, while
					// that panel is the scene on screen (a stale bar is dropped).
					if !virtualKeyboardActive && chatSelectionOwnsInput(config) && handleChatSelectionKey(e, config) {
						continue
					}
					focusEngine.SyncSelected()
					if imageViewerPath != "" {
						handleImageViewerInput(e, config)
						continue
					}
					if virtualKeyboardActive {
						handleVirtualKeyboardInput(e, config)
					} else if activeSceneIndex != -1 {
						switch e.Keysym.Sym {
						case sdl.K_BACKSPACE:
							inputTextBuffer = trimLastRune(inputTextBuffer)
							updateInputVariable(config)
						case sdl.K_RETURN:
							activeSceneIndex = -1
							activeElementIndex = -1
						}
					} else {
						// The clock popover floats above every scene and owns its own
						// keys before the chrome or the scene sees them: Left/Right
						// change month, the month title returns to today, Escape/B
						// close it.
						if calendarPopoverKey(e) {
							continue
						}
						// The app menu and notification center are modal:
						// while one is open it owns the D-pad and Confirm/Back keys.
						if handleChromeOverlayKey(e, config) {
							continue
						}
						// F10 is the keyboard's Start: it opens the Start menu
						// wherever the pad's Start would (startmenu.go).
						if startMenuKeyOpen(e, config) {
							continue
						}
						// F1 (this screen's controls), F2 (search), F12
						// (screenshot) and Ctrl+Q (Quick Settings) are global:
						// below the scene dispatch they never reached screens
						// that consume every key (shortcuts.go).
						if handleGlobalKeys(renderer, e, config) {
							continue
						}
						// Log viewer (Ctrl+L). It is handled here, before the per-scene
						// dispatch, because it has to work on scenes with no config
						// elements (Games, Patch, Music) as well — that is exactly when
						// a log is worth opening. The terminal keeps the key to clear
						// its output.
						inTerminalScene := currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
							config.Scenes[currentSceneIndex].Name == "Terminal"
						if handleLogShortcut(e, config, inTerminalScene) {
							continue
						}
						// A handoff offer or a pairing code floats over Home and owns
						// the keys while it is up (hoffcard.go).
						if hoffCardKey(e, config) {
							continue
						}
						// Home scene uses the dedicated focus graph + activation.
						if homeLayoutActive && handleHomeKey(e, config) {
							continue
						}
						// Patch scene owns its list + modals (no config elements).
						if curSceneName := config.Scenes[currentSceneIndex].Name; curSceneName == "Patch" {
							if handlePatchKey(e, config) {
								continue
							}
						}
						// The Alarm scene owns its keys like Patch does: it has no
						// config elements, so the element dispatch below would never
						// reach its handler.
						if curSceneName := config.Scenes[currentSceneIndex].Name; curSceneName == "Alarm" {
							handleAlarmInput(e, config)
							continue
						}
						// A playing video is modal over the scene chrome: its controls are
						// the only thing on screen the D-pad may reach. This runs before
						// the top-bar focus handling, which otherwise swallowed the
						// arrows while a chrome button held the focus and left the player
						// unreachable.
						if HandleVideoKeyInput(e) {
							continue
						}
						// The Media screen draws its own layout and owns its keys
						// (media_hub_input.go); its config elements are data only.
						if mediaHubHandleKey(e, config) {
							continue
						}
						// Media scenes keep the search field reachable from anywhere:
						// X opens the keyboard even while the results grid has focus,
						// which is the state after playing a video. Without this the
						// field could only be reached by walking back up the grid.
						if e.Keysym.Sym == sdl.K_x && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
							sceneHasSearchResults(config.Scenes[currentSceneIndex]) {
							if openSearchShortcut(config) {
								continue
							}
						}
						// The Tools hub, Network tools and Wi-Fi pages draw their own
						// tiles and sheets, so like Patch they own their keys before
						// the element dispatch (they step aside while the top bar
						// holds the focus).
						if toolsScenesHandleKey(e, config) {
							continue
						}
						// Top-bar chrome (back pill, status clock) is focusable on
						// non-home scenes; while it holds focus it owns the keys so
						// the element dispatch cannot steal the focus back.
						if handleChromeKey(e, config) {
							continue
						}
						// The Files screen's action sheet and remote browser are modal
						// over the list, and its two own keys (Y actions, X mark) act
						// before the list's D-pad.
						if feSceneActive(config) {
							if handleFeSheetKey(e, config) {
								continue
							}
							if handleFeRemoteKey(e, config) {
								continue
							}
							if feFilesKey(e.Keysym.Sym, config) {
								continue
							}
						}
						// The Chat scene owns its keys before the element dispatch: its
						// "chat" element used to take Escape (it only cleared the draft)
						// and type "b" into the message, so B never left Chat on Windows.
						if chatSceneKey(e, config) {
							continue
						}
						// Handle navigation
						curScene := config.Scenes[currentSceneIndex]
						if selectedButtonIndex < 0 || selectedButtonIndex >= len(curScene.Elements) {
							selectedButtonIndex = findFirstSelectableElement(curScene)
						}
						if selectedButtonIndex < 0 || selectedButtonIndex >= len(curScene.Elements) {
							// Registered scenes without config elements (Games, Music,
							// Playtime) still need their own keys: otherwise the empty
							// element list swallowed Enter and B before the registry ran.
							if m, ok := lookupScene(curScene.Name); ok && m.handleKey(e, config) {
								continue
							}
							// The keyboard twin of the pad's Up: a screen with nothing
							// focusable that left Up alone hands it to the top bar.
							if !homeLayoutActive && e.Keysym.Sym == sdl.K_UP && focusEngine.ElementIndex() < 0 && focusChromeBar() {
								continue
							}
							if !homeLayoutActive && (e.Keysym.Sym == sdl.K_ESCAPE || e.Keysym.Sym == sdl.K_b) {
								goBackScene(config)
							}
							continue
						}
						curElem := curScene.Elements[selectedButtonIndex]
						// The terminal renders its own screen and owns its keys. The chat
						// element left in its config used to intercept Enter/Escape, which
						// left no way to run a command or leave the scene.
						inTerminal := curScene.Name == "Terminal"
						if inTerminal {
							handleTermInput(e, config)
						} else if curElem.Type == "searchresults" {
							// Grid navigation via the semantic action handler.
							switch e.Keysym.Sym {
							case sdl.K_UP:
								handleSearchResultsAction(ActionNavigateUp, config)
							case sdl.K_DOWN:
								handleSearchResultsAction(ActionNavigateDown, config)
							case sdl.K_LEFT:
								handleSearchResultsAction(ActionNavigateLeft, config)
							case sdl.K_RIGHT:
								handleSearchResultsAction(ActionNavigateRight, config)
							case sdl.K_RETURN, sdl.K_SPACE:
								handleSearchResultsAction(ActionConfirm, config)
							case sdl.K_PAGEDOWN:
								// Keyboard twin of R2: next page, fetching it if needed.
								mediaResultsPageJump(config, 1)
							case sdl.K_PAGEUP:
								mediaResultsPageJump(config, -1)
							case sdl.K_ESCAPE:
								handleSearchResultsAction(ActionBack, config)
							}
						} else if curElem.Type == "dynamiclist" {
							if curScene.Name == "FileExplorer" {
								if action, ok := feKeyAction(e.Keysym.Sym); ok && feListAction(config, action) {
									continue
								}
							}
							// The Apps launcher is a tile grid: the D-pad walks it in two
							// dimensions instead of stepping a single column of rows.
							if curElem.Variable == appsEntriesVar && appGridKey(e.Keysym.Sym, config) {
								continue
							}
							// Live TV: Left/Right switch category, X searches, Y stars
							// the highlighted channel. Other lists keep their keys.
							if curElem.Variable == tvEntriesVar && tvListKeyAction(e.Keysym.Sym, config) {
								continue
							}
							entries, _ := getSceneFileEntries(config, curScene)
							n := 0
							if entries != nil {
								n = len(entries)
							}
							switch e.Keysym.Sym {
							case sdl.K_UP:
								if focusedFileIndex > 0 {
									focusedFileIndex--
								}
							case sdl.K_DOWN:
								if focusedFileIndex < n-1 {
									focusedFileIndex++
								}
							case sdl.K_RETURN, sdl.K_SPACE:
								feEnterFocused(config)
							case sdl.K_PAGEDOWN:
								if focusedFileIndex+dynamicListMaxVisible(curElem) < n {
									focusedFileIndex += dynamicListMaxVisible(curElem)
								} else {
									focusedFileIndex = n - 1
								}
							case sdl.K_PAGEUP:
								if focusedFileIndex-dynamicListMaxVisible(curElem) > 0 {
									focusedFileIndex -= dynamicListMaxVisible(curElem)
								} else {
									focusedFileIndex = 0
								}
							case sdl.K_ESCAPE:
								// File Explorer: B/Escape climbs the directory tree and
								// only leaves the scene at the root.
								if curScene.Name == "FileExplorer" {
									feBack(config)
								} else {
									moveSelection(config, ActionNavigateUp)
								}
							}
						} else if curElem.Type == "themegallery" {
							presets := themeGalleryPresets()
							switch e.Keysym.Sym {
							case sdl.K_TAB:
								// Tab moves between Colour and Total conversion:
								// the two are different promises about what a
								// preset changes, and Left/Right already walk the
								// cards of the open one.
								step := 1
								if e.Keysym.Mod&sdl.KMOD_SHIFT != 0 {
									step = -1
								}
								if themeGallerySwitchTab(step) {
									PlayNavSound()
								}
							case sdl.K_LEFT:
								themeGalleryMove(curElem, -1)
								PlayNavSound()
							case sdl.K_RIGHT:
								themeGalleryMove(curElem, 1)
								PlayNavSound()
							case sdl.K_UP:
								themeGalleryMove(curElem, -themeGalleryColumns(curElem))
								PlayNavSound()
							case sdl.K_DOWN:
								themeGalleryMove(curElem, themeGalleryColumns(curElem))
								PlayNavSound()
							case sdl.K_RETURN, sdl.K_SPACE:
								if themeGalleryIndex >= 0 && themeGalleryIndex < len(presets) {
									ApplyThemePreset(config, presets[themeGalleryIndex])
								}
							case sdl.K_c:
								// Browse the community repository without leaving the gallery.
								openThemeCommunityPanel(config)
							case sdl.K_ESCAPE:
								moveSelection(config, ActionNavigateUp)
							}
						} else if curElem.Type == "toggle" {
							switch e.Keysym.Sym {
							case sdl.K_UP:
								handleToggleAction(config, curElem, ActionNavigateUp)
							case sdl.K_DOWN:
								handleToggleAction(config, curElem, ActionNavigateDown)
							case sdl.K_RETURN, sdl.K_SPACE, sdl.K_LEFT, sdl.K_RIGHT:
								handleToggleAction(config, curElem, ActionConfirm)
							case sdl.K_ESCAPE:
								moveSelection(config, ActionNavigateUp)
							}
						} else if curElem.Type == "favorites" {
							handleFavoritesInput(e, config)
						} else if curElem.Type == "unitconverter" {
							handleUnitInput(e, config)
						} else if curElem.Type == "chat" {
							// The Chat scene handled its keys above (chatSceneKey). A
							// "chat" element on any other scene opens the keyboard on
							// Enter; Escape and B fall through to the shared Back.
							switch e.Keysym.Sym {
							case sdl.K_RETURN, sdl.K_KP_ENTER:
								vkOpenChat()
							case sdl.K_ESCAPE, sdl.K_b:
								goBackScene(config)
							}
						} else if curElem.Type == "shortslist" {
							handleShortsInput(e, config)
						} else if curElem.Type == "canvas" {
							handleCanvasInput(e, config)
						} else if curElem.Type == "textbrowser" {
							if textBrowserElementSource(curElem) == textBrowserSourceWeb {
								// The web browser owns the keyboard too: on the Trimui
								// Smart Pro the D-pad and face buttons arrive as SDL
								// keys, and browser.go gives them the same model the
								// controller path uses (Tab cycles scroll ⇄ links).
								handleBrowserKey(renderer, e, config, curElem)
							} else {
								switch e.Keysym.Sym {
								case sdl.K_UP:
									textBrowserScrollVelocity -= textBrowserScrollAccel
									if textBrowserScrollVelocity < -textBrowserScrollMaxVel {
										textBrowserScrollVelocity = -textBrowserScrollMaxVel
									}
									textBrowserScrollCooldown = 8
								case sdl.K_DOWN:
									textBrowserScrollVelocity += textBrowserScrollAccel
									if textBrowserScrollVelocity > textBrowserScrollMaxVel {
										textBrowserScrollVelocity = textBrowserScrollMaxVel
									}
									textBrowserScrollCooldown = 8
								case sdl.K_PAGEUP:
									textBrowserScrollY -= 10
									if textBrowserScrollY < 0 {
										textBrowserScrollY = 0
									}
									textBrowserScrollVelocity = 0
									textBrowserScrollCooldown = 0
								case sdl.K_PAGEDOWN:
									textBrowserScrollY += 10
									textBrowserScrollVelocity = 0
									textBrowserScrollCooldown = 0
								case sdl.K_HOME:
									textBrowserScrollY = 0
									textBrowserScrollVelocity = 0
									textBrowserScrollCooldown = 0
								case sdl.K_END:
									textBrowserScrollY = 1 << 30
									textBrowserScrollVelocity = 0
									textBrowserScrollCooldown = 0
								case sdl.K_l, sdl.K_x:
									if textBrowserElementSource(curElem) == textBrowserSourceWeb {
										openBrowserLinkPanel(config, curElem)
									}
								}
							}
						} else if curElem.Type == "packagelist" {
							handlePackageInput(e, config)
						} else if curElem.Type == "recent" {
							handleRecentInput(e, config)
						} else if m, ok := lookupScene(curScene.Name); ok && m.handleKey(e, config) {
							// The scene consumed the key. Overlays are checked first so a
							// picker floating over a scene always wins the D-pad.
						} else {
							// Normal button/input navigation
							switch e.Keysym.Sym {
							case sdl.K_UP, sdl.K_DOWN:
								if homeLayoutActive && curElem.Type == "button" && curElem.Style == "tile" {
									moveHomeSelection(config, 0, map[sdl.Keycode]int{
										sdl.K_UP: -1, sdl.K_DOWN: 1,
									}[e.Keysym.Sym])
								} else {
									if curScene.Name != "FileExplorer" || e.Keysym.Sym != sdl.K_UP || curElem.Type != "button" || !feFocusList(config) {
										moveSelection(config, map[sdl.Keycode]Action{sdl.K_UP: ActionNavigateUp, sdl.K_DOWN: ActionNavigateDown}[e.Keysym.Sym])
									}
								}
							case sdl.K_LEFT, sdl.K_RIGHT:
								if homeLayoutActive && curElem.Type == "button" && curElem.Style == "tile" {
									moveHomeSelection(config, map[sdl.Keycode]int{
										sdl.K_LEFT: -1, sdl.K_RIGHT: 1,
									}[e.Keysym.Sym], 0)
								} else {
									moveSelection(config, map[sdl.Keycode]Action{sdl.K_LEFT: ActionNavigateLeft, sdl.K_RIGHT: ActionNavigateRight}[e.Keysym.Sym])
								}
							case sdl.K_RETURN, sdl.K_SPACE, sdl.K_a:
								if e.Keysym.Sym == sdl.K_a && curScene.Name != "FileExplorer" {
									break
								}
								if selectedButtonIndex >= 0 && selectedButtonIndex < len(config.Scenes[currentSceneIndex].Elements) {
									elem := config.Scenes[currentSceneIndex].Elements[selectedButtonIndex]
									if elem.Type == "input" {
										handleInputSelection(renderer, config, currentSceneIndex, selectedButtonIndex)
									} else if elem.Type == "button" {
										pressedButtonIndex = selectedButtonIndex
										pressStartTime = sdl.GetTicks64()
										handleTrigger(renderer, config, elem)
									}
								}
							case sdl.K_x:
								// The footer advertises "X Search" on Tube: open the
								// keyboard for the search field, matching the controller.
								if curScene.Name == "Tube" {
									openSearchShortcut(config)
								}
							case sdl.K_ESCAPE, sdl.K_b:
								// Generic back on non-home scenes (Escape or B, matching every
								// other screen and the footer's "B Back" hint): returns to the
								// previous scene (or Home). Never exits the app.
								if !homeLayoutActive {
									if curScene.Name == "FileExplorer" {
										feBack(config)
									} else {
										goBackScene(config)
									}
								}
							}
						}
						focusEngine.SyncSelected()
						// Scene switching keys — not in the terminal, where q/e are
						// ordinary characters in a command, and not while JukaLand
						// owns the keyboard (E builds there).
						// Ctrl/Alt chords are not scene keys: Ctrl+Q used to open
						// Quick Settings and switch the scene too. The time-zone
						// picker's search types letters, q and e included.
						if !inTerminal && !jlandKeysCaptured(config) && !tzPickerVisible() && e.Keysym.Mod&(sdl.KMOD_CTRL|sdl.KMOD_ALT) == 0 {
							switch e.Keysym.Sym {
							case sdl.K_q:
								changeScene(config, -1)
							case sdl.K_e:
								changeScene(config, 1)
							}
						}
						// Global search chord (Ctrl+K) jumps to the Tube/search scene.
						if e.Keysym.Sym == sdl.K_k && (e.Keysym.Mod&sdl.KMOD_CTRL) != 0 {
							if idx := findSceneIndex(config, "Tube"); idx >= 0 {
								changeSceneTo(config, idx)
							}
						}
						// Shorts feed (Ctrl+Shift+S): opens Media's Shorts feed, pre-loads
						// it and plays the first short. The chord is used instead of a
						// plain S so typing a capital S in a field cannot trigger it.
						if e.Keysym.Sym == sdl.K_s && (e.Keysym.Mod&sdl.KMOD_CTRL) != 0 && (e.Keysym.Mod&sdl.KMOD_SHIFT) != 0 {
							openShortsFeed(config)
							continue
						}
						// F1, F2, F12 and Ctrl+Q are handled before the scene
						// (handleGlobalKeys in shortcuts.go).
						// Clipboard manager (Ctrl+Shift+V).
						if e.Keysym.Sym == sdl.K_v && (e.Keysym.Mod&sdl.KMOD_CTRL) != 0 && (e.Keysym.Mod&sdl.KMOD_SHIFT) != 0 {
							clipSearchOpen = !clipSearchOpen
							globalSearchOpen = false
							shortcutsOpen = false
							clipCursorIdx = 0
							continue
						}
						// Fullscreen toggle (F11 / Alt+Enter).
						if e.Keysym.Sym == sdl.K_F11 || (e.Keysym.Sym == sdl.K_RETURN && (e.Keysym.Mod&sdl.KMOD_ALT) != 0) {
							applyFullscreen(config, !config.Variables.Fullscreen)
						}
					}
					focusEngine.SyncSelected()
				}
			case *sdl.TextInputEvent:
				frameRateCtrl.NotifyInput()
				if wakeSwallowedText {
					continue
				}
				screensaverNotifyInput()
				handleTextInputEvent(e, config)
			case *sdl.MouseMotionEvent:
				frameRateCtrl.NotifyInput()
				screensaverNotifyInput()
				mouseX, mouseY = physicalToLogical(int32(e.X), int32(e.Y))
				// A ringing alarm is modal: the pointer moves, hovers and drags
				// nothing behind its takeover. So is the recovery card.
				if alarmFiringActive() || recoveryCard.open {
					continue
				}
				// The wallpaper picker covers the screen: the pointer hovers its
				// rows and nothing behind it (wallpaper.go).
				if wallpaperOwnsPointer() {
					wallpaperMouseMove(mouseX, mouseY)
					continue
				}
				// The volume card follows a drag on its slider and keeps the
				// pointer from hovering the cards behind it (volumepopover.go).
				if volumePopoverPointerMotion(config, mouseX, mouseY, e.State&sdl.ButtonLMask() != 0) {
					continue
				}
				// So does the brightness card (brightnesspopover.go).
				if brightnessPopoverPointerMotion(config, mouseX, mouseY, e.State&sdl.ButtonLMask() != 0) {
					continue
				}
				// A held press on the package store drags its shelves and
				// lists (a no-op unless the store recorded the press).
				pkgPointerMotion(config, mouseX, mouseY, e.State&sdl.ButtonLMask() != 0)
				// Hovering a home card focuses it so mouse users get the same
				// highlight as D-pad navigation. A deliberate click remembers
				// the card for restore-on-return (see handleHomeMouseClick).
				if homeLayoutActive && homeLayout != nil {
					// Edit Home follows the pointer itself: hover, and a press
					// that turns into carrying a tile (home_edit.go).
					if !homeEditPointerMotion(config, mouseX, mouseY, e.State&sdl.ButtonLMask() != 0) {
						homeLayout.FocusAt(mouseX, mouseY)
					}
				} else if !mediaHubHover(config, mouseX, mouseY) {
					focusMediaResultAt(config, mouseX, mouseY)
				}
				if isDraggingSeekBar {
					fraction := float64(mouseX-seekBarDragRect.X) / float64(seekBarDragRect.W)
					if fraction < 0 {
						fraction = 0
					}
					if fraction > 1 {
						fraction = 1
					}
					if math.IsNaN(fraction) || math.IsInf(fraction, 0) {
						fraction = 0
					}
					SeekVideo(fraction)
				}
				// Dragging a zoomed picture pans it (imageviewer.go); dragging a
				// PDF page pans the page.
				filesMouseMotion(config, mouseX, mouseY, e.State&sdl.ButtonLMask() != 0)
			case *sdl.MouseWheelEvent:
				// Scrolling is input too: the wheel used to leave the idle clock
				// running, so a long read with the wheel ended in the screensaver.
				frameRateCtrl.NotifyInput()
				// A ringing alarm is modal: nothing behind it scrolls.
				if alarmFiringActive() {
					continue
				}
				if screensaverWakeOnPress() {
					continue
				}
				// Nor does anything behind the recovery card.
				if recoveryCard.open {
					continue
				}
				// Nor behind the wallpaper picker, whose list it scrolls.
				if wallpaperOwnsPointer() {
					wallpaperWheel(e.Y)
					continue
				}
				// The wheel is a volume knob while the volume card is up, and
				// over the speaker button in the header (volumepopover.go); a
				// brightness knob the same way for the sun's card
				// (brightnesspopover.go). An open card takes it first, wherever
				// the pointer is.
				if brightnessPopoverOpen && brightnessWheel(config, mouseX, mouseY, e.Y) {
					continue
				}
				if volumeWheel(config, mouseX, mouseY, e.Y) {
					continue
				}
				if brightnessWheel(config, mouseX, mouseY, e.Y) {
					continue
				}
				// The Home search sheet floats above every screen, so its result
				// list takes the wheel first (globalsearch.go).
				if homeSearchWheel(e.Y) {
					continue
				}
				// The open notification centre walks its rows under the wheel,
				// the way its D-pad does (notifications.go).
				if notifWheel(e.Y) {
					continue
				}
				// Home's widget grid scrolls a row per notch and its carousel
				// turns a card; Edit Home's add sheet takes the wheel while it is
				// open (home_edit.go).
				if homeWheel(config, e.Y) {
					continue
				}
				// The Files viewers own the wheel while they are up: the picture
				// zooms, the text page and the reader scroll, the list pages.
				if filesHandleWheel(config, e.Y) {
					continue
				}
				// The Media screen scrolls its rows and grids a row per notch.
				if mediaHubWheel(config, e.Y) {
					continue
				}
				// The Chat history scrolls smoothly under the wheel.
				if chatSceneWheel(config, e.Y) {
					continue
				}
				if powerTools.open && handlePowerToolsWheel(e.Y) {
					continue
				}
				if communityPanel.open && handleCommunityWheel(e.Y) {
					continue
				}
				// Watch Party, Toolbox, Audit toolkit, Security & trust and
				// Report a bug scroll their list, reader or open sheet.
				if tbxSceneWheel(e.Y, config) {
					continue
				}
				if brwInfo.open && brwInfoWheel(e.Y) {
					continue
				}
				// The browser's Select menu walks its rows under the wheel.
				if brwMenuWheel(e.Y) {
					continue
				}
				// The Core builds list and the Emulators grid scroll with the wheel
				// the way their D-pad does.
				if kbPanel.open && handleCoreBuildsWheel(e.Y) {
					continue
				}
				if emuPanel.open && handleEmuWheel(e.Y) {
					continue
				}
				// Playtime is a long, scrolling page on a config-driven canvas: the
				// wheel and a touchpad drag move it like the D-pad does.
				if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
					config.Scenes[currentSceneIndex].Name == playtimeSceneName && handlePlaytimeWheel(e.Y) {
					continue
				}
				// Keyboard settings scrolls its list, or its open sheet.
				if !virtualKeyboardActive && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
					config.Scenes[currentSceneIndex].Name == vkSettingsSceneName && vkSettingsHandleWheel(e.Y) {
					continue
				}
				// The Tools hub, Network tools and Wi-Fi pages scroll their
				// tiles, list or open sheet.
				if toolsScenesWheel(config, e.Y) {
					continue
				}
				// Devices, Save sync and Nearby scroll their list or open sheet.
				if peerScenesWheel(config, e.Y) {
					continue
				}
				// So does the Bluetooth page (bluetooth_devices_ui.go).
				if btScenesWheel(config, e.Y) {
					continue
				}
				// Control profiles and Local play scroll their own lists.
				if inmapScenesWheel(config, e.Y) {
					continue
				}
				// The ROM manager scrolls its plan, or its open sheet.
				if !virtualKeyboardActive && rommSceneWheel(config, e.Y) {
					continue
				}
				// The APK Store scrolls its tab, or its open sheet.
				if !virtualKeyboardActive && apkStoreSceneWheel(config, e.Y) {
					continue
				}
				// Voice command scrolls its choices, or its phone sheet.
				if !virtualKeyboardActive && voiceSceneWheel(config, e.Y) {
					continue
				}
				// Experience packs, Config and Backup & restore scroll their
				// lists or open sheet (xpackroute.go).
				if !virtualKeyboardActive && xpackRouteWheel(e.Y, config) {
					continue
				}
				// Settings scrolls its rows (or its open list), Plugins its list.
				if !virtualKeyboardActive && setSceneWheel(e.Y, config) {
					continue
				}
				// The Terminal, Scheduled tasks and the system tool screens
				// scroll their own lists and output (systool.go).
				if sysToolHandleWheel(e.Y, config) {
					continue
				}
				if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
					for _, elem := range config.Scenes[currentSceneIndex].Elements {
						if elem.Type == "textbrowser" {
							elemW := getElementWidth(elem, 1100)
							elemH := getElementHeight(elem, 480)
							if mouseX >= elem.X && mouseX <= elem.X+elemW && mouseY >= elem.Y && mouseY <= elem.Y+elemH {
								textBrowserScrollVelocity -= float64(e.Y) * 2.5
								if textBrowserScrollVelocity < -textBrowserScrollMaxVel {
									textBrowserScrollVelocity = -textBrowserScrollMaxVel
								} else if textBrowserScrollVelocity > textBrowserScrollMaxVel {
									textBrowserScrollVelocity = textBrowserScrollMaxVel
								}
								textBrowserScrollCooldown = 10
								break
							}
						}
						if elem.Type == "packagelist" {
							// The store scrolls what is under the pointer: the
							// page, a shelf (horizontal wheel), the grid, the
							// list or the details sheet (pkginput.go).
							if pkgHandleWheel(config, elem, mouseX, mouseY, e.X, e.Y) {
								break
							}
						}
					}
				}
			case *sdl.MouseButtonEvent:
				frameRateCtrl.NotifyInput()
				// A click that wakes the screen only wakes it, release included
				// (several surfaces act on the release); a release never wakes.
				if e.Type == sdl.MOUSEBUTTONDOWN {
					if wakeSwallowPress(wakeInputMouse, int32(e.Button), false) {
						continue
					}
				} else if wakeSwallowRelease(wakeInputMouse, int32(e.Button)) {
					continue
				}
				if alarmFiringActive() {
					// The alarm is drawn above every panel, including the dock and
					// the tour; a tap either presses one of its two buttons or is
					// swallowed by the red wash.
					if e.Button == sdl.BUTTON_LEFT && e.Type == sdl.MOUSEBUTTONDOWN {
						mx, my := physicalToLogical(int32(e.X), int32(e.Y))
						alarmHandleMouse(mx, my)
					}
					// A seek drag under way when the ring began ends with this
					// release, instead of seeking on the first move after it.
					if e.Type == sdl.MOUSEBUTTONUP {
						isDraggingSeekBar = false
					}
					continue
				}
				if ambientIsActive() {
					// Taps on the dock screen act on its chips; everything else
					// only wakes it, so a stray tap cannot press the UI behind.
					if e.Button == sdl.BUTTON_LEFT && e.Type == sdl.MOUSEBUTTONDOWN {
						mx, my := physicalToLogical(int32(e.X), int32(e.Y))
						ambientHandleClick(mx, my)
					}
					continue
				}
				// The recovery card is modal: a left press reaches its rows
				// (recoveryCardHandleClick below); a release or another button
				// reaches nothing behind it (a package card opens on release).
				if recoveryCard.open && (e.Type == sdl.MOUSEBUTTONUP || e.Button != sdl.BUTTON_LEFT) {
					isDraggingSeekBar = false
					continue
				}
				// The wallpaper picker covers the screen: a left press reaches its
				// rows, and nothing behind it hears a press or a release
				// (wallpaper.go).
				if wallpaperOwnsPointer() {
					if e.Button == sdl.BUTTON_LEFT && e.Type == sdl.MOUSEBUTTONDOWN {
						px, py := physicalToLogical(int32(e.X), int32(e.Y))
						wallpaperMouseDown(px, py, e.Clicks, config)
					}
					continue
				}
				// A right click is the mouse's Select on the Browser: it opens the
				// menu over the page and closes it again (brw_menu.go).
				if e.Button == sdl.BUTTON_RIGHT && e.Type == sdl.MOUSEBUTTONDOWN && brwMenuRightClick(config) {
					continue
				}
				// On a Carousel or Widgets Home a right click edits the tile
				// under the pointer (home_edit.go).
				if e.Button == sdl.BUTTON_RIGHT && e.Type == sdl.MOUSEBUTTONDOWN && homeLayoutActive {
					rx, ry := physicalToLogical(int32(e.X), int32(e.Y))
					if homeRightClick(config, rx, ry) {
						continue
					}
				}
				if e.Button == sdl.BUTTON_LEFT {
					// Taps are swallowed while a video is being prepared: the
					// spinner is modal, and a stray tap must not press the grid
					// behind it.
					if videoPrepareActive() {
						continue
					}
					mx, my := physicalToLogical(int32(e.X), int32(e.Y))
					// The voice orb takes a click on itself and its bubble, and
					// while it listens a click anywhere, release included
					// (voiceorb.go).
					if voiceOrbMouse(e.Type == sdl.MOUSEBUTTONDOWN, mx, my, config) {
						continue
					}
					if e.Type == sdl.MOUSEBUTTONDOWN {
						if recoveryCardHandleClick(mx, my, config) {
							break
						}
						if launchFailure.open {
							handleLaunchFailureMouse()
							break
						}
						if handleOnboardingMouse(mx, my, config) {
							break
						}
						// A tap anywhere on the enlarged code closes it, so the
						// panel underneath cannot be pressed through the card.
						if qrZoomActive() {
							qrZoomCloseCard()
							break
						}
						if handleCompanionMouse(mx, my) {
							break
						}
						// The clipboard history is modal: a click picks an entry or
						// closes it, never the screen behind (clipboard.go).
						if clipSearchOpen {
							vkClipHandleClick(mx, my)
							break
						}
						// The Home search sheet sits above the scene with its own
						// keyboard under it: a tap on a result opens it, a tap on
						// the keyboard is left to the keyboard (globalsearch.go).
						if globalSearchOpen && homeSearchMouseDown(mx, my, config) {
							break
						}
						if virtualKeyboardActive {
							// The keyboard routes the click itself: its popups, the
							// tool row and Close, the strip, the field (caret), the
							// keys, or the dimmed screen behind it (dismiss).
							kmx, kmy := physicalToLogical(int32(e.X), int32(e.Y))
							vkHandlePointerDown(kmx, kmy, vkHoldMouse, config)
							break
						}

						// A click dismisses the toast. One that was cut short opens
						// the notification centre on its message, which the
						// centre shows whole (notifToastOpensCentre says when).
						if toastRect.W > 0 && toastRect.H > 0 && pointInRect(mx, my, toastRect) {
							currentToastMu.Lock()
							toastMsg := currentToast.Message
							currentToast = ToastState{}
							currentToastMu.Unlock()
							openFull := notifToastOpensCentre(toastMsg, toastCutMsg)
							toastRect = sdl.Rect{}
							toastCutMsg = ""
							if openFull {
								notifOpenOn(toastMsg)
							}
							break
						}
						// LED style panel is modal the same way.
						if ledPanel.open {
							handleLEDPanelMouse(mx, my, config)
							break
						}
						// Podcast library panel is modal the same way.
						if podPanel.open {
							handlePodcastPanelMouse(mx, my, config)
							break
						}
						// IPStream library panel is modal the same way.
						if ipPanelOpen {
							handleIPPanelMouse(mx, my, config)
							break
						}
						// Process panel is modal the same way.
						if processPanel.open {
							handleProcessMouse(mx, my, config)
							break
						}
						// Memory panel is modal the same way.
						if memPanel.open {
							handleMemMouse(mx, my, config)
							break
						}
						// Text editor is modal the same way.
						if fileEditor.open {
							handleFileEditorMouse(mx, my, config)
							break
						}
						// Controller test is modal the same way.
						if ctrldiag.open {
							handleCtrldiagMouse(mx, my, config)
							break
						}
						// Update prompt is modal and above every other overlay.
						if updatePrompt.open {
							handleUpdatePromptMouse(mx, my, config)
							break
						}
						// Community hub is modal and above every other overlay.
						if communityPanel.open {
							handleCommunityMouse(mx, my, config)
							break
						}
						// Community theme library sits above the app while open.
						if themeCommunityPanel.open {
							handleThemeCommunityMouse(mx, my, config)
							break
						}
						// Power tools replace the app while open.
						if powerTools.open {
							handlePowerToolsMouse(mx, my, config)
							break
						}
						// RetroAchievements overlay is modal and above the panels
						// that launch it.
						if raPanel.open {
							handleRetroAchievementsMouse(mx, my, config)
							break
						}
						// Game Profiles is modal the same way.
						if gpPanel.open {
							handleGameProfileMouse(mx, my, config)
							break
						}
						// Smart Pro Compatibility is modal the same way.
						if cpPanel.open {
							handleCompatMouse(mx, my)
							break
						}
						// Kept core builds is modal the same way.
						if kbPanel.open {
							handleCoreBuildsMouse(mx, my)
							break
						}
						// Windows Apps is modal the same way.
						if winAppsPanel.open {
							handleWindowsAppsMouse(mx, my, config)
							break
						}
						// PS Remote Play is modal the same way.
						if chiakiPanel.open {
							handleChiakiMouse(mx, my)
							break
						}
						// The Remote Cast panel is modal the same way.
						if castPanel.open {
							handleCastMouse(mx, my, config)
							break
						}
						// The Audio settings panel is modal the same way.
						if musicSettings.open {
							handleMusicSettingsMouse(mx, my)
							break
						}
						// Accessibility is modal the same way.
						if a11y.open {
							handleAccessibilityMouse(mx, my, config)
							break
						}
						// Save Manager is modal the same way.
						if savePanel.open {
							handleSaveManagerMouse(mx, my)
							break
						}
						// Curation Hub is modal the same way.
						if curationPanel.open {
							handleCurationMouse(mx, my, config)
							break
						}
						// PortMaster Ports is modal the same way.
						if portsPanel.open {
							handlePortsMouse(mx, my)
							break
						}
						// Browser link picker floats above the browser scene.
						if browserLinkPanel.open {
							handleBrowserLinksMouse(mx, my, config)
							break
						}
						// The site picker floats above it the same way.
						if browserCookiePanel.open {
							handleBrowserCookiesMouse(mx, my, config)
							break
						}
						// And the saved-page picker.
						if browserBookmarkPanel.open {
							handleBrowserBookmarksMouse(mx, my, config)
							break
						}
						// And the browser's site-info sheet.
						if brwInfo.open {
							brwInfoMouse(mx, my, config)
							break
						}
						// And the browser's Select menu.
						if brwMenu.open {
							brwMenuMouse(renderer, mx, my, config)
							break
						}
						// The calibration wizard is modal the same way.
						if calibrationPanel.open {
							handleStickCalibrationMouse(mx, my, config)
							break
						}
						// Smart Home is modal the same way.
						if smartHomePanel.open {
							handleSmartHomeMouse(mx, my, config)
							break
						}
						// LAN play is modal over the game library.
						if netplayPanel.open {
							handleNetplayMouse(mx, my)
							break
						}
						// Downloads is modal the same way.
						if dlPanel.open {
							handleDownloadsMouse(mx, my, config)
							break
						}
						// Game library is modal the same way.
						if gameLib.open {
							handleGameLibraryMouse(mx, my, config)
							break
						}
						// Emulators panel is modal the same way.
						if emuPanel.open {
							handleEmuMouse(mx, my, config)
							break
						}

						// Full screen video owns every click: the player covers the whole
						// panel, so nothing underneath may react through it.
						if videoFullscreen && playerPhase() != "idle" {
							HandleVideoControlClick(mx, my)
							break
						}
						// Windowed video owns its own element: the close and back
						// buttons it draws have to be tappable there too, and its handler
						// declines clicks on the rest of the scene.
						if playerPhase() != "idle" && HandleVideoControlClick(mx, my) {
							break
						}

						// The floating mini player (videomini.go) is drawn above the
						// scene, so a tap on it must not reach the elements underneath.
						if handleVideoMiniClick(mx, my, config) {
							break
						}

						// Clock popover: drawn above the scene on every screen, so it
						// receives clicks before anything underneath. The Set alarm row
						// opens the alarm editor; any other click dismisses it.
						if homeCalendarOpen && calendarPopoverClick(mx, my, config) {
							break
						}

						// Local weather card: it floats above the scene and dismisses
						// on an outside click before the scene sees the tap.
						if weatherPopoverClick(mx, my, config) {
							break
						}

						// The volume card the same way: its slider and mute button
						// take the click, anything else closes it (volumepopover.go).
						if volumePopoverMouseDown(mx, my, config) {
							break
						}
						// And the brightness card (brightnesspopover.go).
						if brightnessPopoverMouseDown(mx, my, config) {
							break
						}

						// The alarm screen draws its own Back pill and rows, so it owns
						// the clicks on its surface.
						if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
							config.Scenes[currentSceneIndex].Name == "Alarm" {
							handleAlarmMouseClick(mx, my, config)
							break
						}
						// The Terminal, Scheduled tasks and the system tool screens
						// draw their own controls, so they own the clicks on their
						// body; a click on the top bar (or with its menus open)
						// falls through to the shared handlers below (systool.go).
						if sysToolHandleClick(mx, my, config) {
							break
						}
						// Check if clicking on seek bar for dragging. Only a bar on
						// screen: an idle or floating player's last layout is stale,
						// and an empty rect would match the origin.
						if playerPhase() != "idle" && !videoMiniVisible() && vcHit(mx, my, videoControlRects.progress) {
							isDraggingSeekBar = true
							seekBarDragRect = videoControlRects.progress
							fraction := float64(mx-videoControlRects.progress.X) / float64(videoControlRects.progress.W)
							SeekVideo(fraction)
						}
						// Image viewer: its chips (close, zoom, fit, lock screen) act;
						// any other press starts a drag that pans a zoomed picture, and
						// a tap that does not move hides or shows the controls.
						if imageViewerPath != "" {
							imgvMouseDown(mx, my, config)
							break
						}

						// Chat member list / profile card: a scrim over the scene, so
						// they take every click before anything underneath sees it.
						if chatOverlayOwnsInput(config) && chatOverlayClick(mx, my, config) {
							break
						}

						// Start menu click.
						if tb.startOpen && startMenuIsPointIn(mx, my) {
							startMenuHandleClick(mx, my, config)
							break
						}
						// Start menu open -> click outside dismisses it.
						if tb.startOpen {
							startMenuDismiss()
							break
						}

						// Notification dropdown click (Clear All, or swallow the click
						// so it cannot reach the scene underneath).
						if notifHandleClick(mx, my) {
							break
						}
						// Notification dropdown open -> click outside dismisses it.
						if nc.open {
							notifDismiss()
							break
						}
						// A tap on a trophy celebration card opens the trophy room;
						// it stands aside while a video plays (trophy_celebrate.go).
						if trophyCardClick(mx, my, config) {
							break
						}
						// The full-screen Browser draws no top bar, so a click there is
						// its page's or its status pills' (brw_fullscreen.go), never a
						// bar button the screen before left behind.
						if brwFullscreenClick(renderer, mx, my, config) {
							break
						}

						// Apps pill in the top bar (opens the app menu).
						if tb.startRect.W > 0 && mx >= tb.startRect.X && mx <= tb.startRect.X+tb.startRect.W &&
							my >= tb.startRect.Y && my <= tb.startRect.Y+tb.startRect.H {
							startMenuToggle()
							break
						}

						// Notification bell.
						if notifIsPointInBell(mx, my) {
							notifToggle()
							break
						}

						// Live TV filter bar: category arrows and the search pill are
						// touch targets on the handheld.
						if handleTVFilterMouse(mx, my, config) {
							break
						}
						// A handoff offer or a pairing code over Home is modal: it
						// takes the taps before the cards under it (hoffcard.go).
						if hoffCardClick(mx, my, config) {
							break
						}
						// Home cards: select + activate on click.
						if homeLayoutActive && handleHomeMouseClick(mx, my, config) {
							break
						}
						// Top-bar chrome buttons: the back pill and the status clock
						// activate on click/touch like any focused button would.
						if handleChromeBarClick(mx, my, config) {
							break
						}
						// Activity strip chips: the X stops the work behind the
						// chip, the body jumps to where it is managed. A miss
						// falls through to the scene underneath.
						if activityStripClick(mx, my) {
							break
						}
						// Playtime has no config elements; its RA touch target lives
						// in the header.
						if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
							config.Scenes[currentSceneIndex].Name == playtimeSceneName &&
							handlePlaytimeMouse(mx, my) {
							break
						}
						// Keyboard settings draws its own rows, row buttons and sheets.
						if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
							config.Scenes[currentSceneIndex].Name == vkSettingsSceneName &&
							vkSettingsHandleClick(mx, my, config) {
							break
						}
						// Watch Party, Toolbox, Audit toolkit, Security & trust and
						// Report a bug draw their own rows, buttons and sheets.
						if tbxSceneMouse(mx, my, config) {
							break
						}
						// JukaLand takes clicks and taps on its world and its pause
						// sheet (a finger's tap only ever arrives as this click).
						if jlandSceneClick(mx, my, config) {
							break
						}
						// The Tools hub, Network tools and Wi-Fi pages draw their own
						// tiles, list and sheets, so they own the taps on them.
						if toolsScenesClick(mx, my, config) {
							break
						}
						// Devices, Save sync and Nearby draw their own rows and
						// sheets (peerui.go).
						if peerScenesClick(mx, my, config) {
							break
						}
						// The Bluetooth page's rows and sheets
						// (bluetooth_devices_ui.go).
						if btScenesClick(mx, my, config) {
							break
						}
						// Control profiles and Local play draw their own rows,
						// slots, buttons and sheets (inmap_ui.go, hotseat_ui.go).
						if inmapScenesClick(mx, my, config) {
							break
						}
						// The ROM manager draws its own card, rows and sheets.
						if rommSceneClick(mx, my, config) {
							break
						}
						// The APK Store's tabs, rows and sheets (apkstore.go).
						if apkStoreSceneClick(mx, my, config) {
							break
						}
						// Voice command's microphone, buttons and choices.
						if voiceSceneClick(mx, my, config) {
							break
						}
						// The Chat scene owns every click inside it: tabs, banner,
						// pictures, links, names and avatars, message bodies, the
						// action bar and the composer (chatinput.go).
						if chatSceneClick(mx, my, config) {
							break
						}
						// A "chat" element on another scene keeps its own targets.
						if currentSceneHasChat(config) && handleChatImageClick(mx, my) {
							break
						}
						if currentSceneHasChat(config) && handleChatLinkClick(mx, my, config) {
							break
						}
						if currentSceneHasChat(config) && chatMessageClick(mx, my, config) {
							break
						}
						if handleChatComposerMouse(mx, my, config) {
							break
						}
						// Calculator's compact graph and variable controls sit in the result
						// panel and own taps before clicks fall through to the page.
						if handleCalculatorMouse(mx, my, config) {
							break
						}
						// Unit converter controls, including its input, only own clicks
						// while that scene is visible.
						if handleUnitConverterMouse(mx, my, config) {
							break
						}
						// The audio player draws its own transport, so it owns clicks
						// while it is on screen.
						if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
							config.Scenes[currentSceneIndex].Name == "MusicPlayer" {
							if HandleMusicClick(mx, my) {
								break
							}
						}
						// Check video controls (a consumed click must not fall through
						// to the scene elements drawn underneath the player).
						if HandleVideoControlClick(mx, my) {
							break
						}
						// The Media screen draws its own body (media_hub_render.go) and
						// takes every click on it, so none reaches its hidden elements.
						if mediaHubClick(mx, my, config) {
							break
						}
						// Experience packs, Config and Backup & restore draw their
						// own cards, rows, buttons and sheets (xpackroute.go).
						if xpackRouteMouse(mx, my, config) {
							break
						}
						// Settings and Plugins draw their own rows (setui.go,
						// plugui.go) and take every click on them, so none reaches
						// the config buttons the pages no longer draw.
						if setSceneMouse(mx, my, config) {
							break
						}
						// Patch's list and modals are drawn by the scene renderer, so
						// they need their own click routing (rows used to be keyboard
						// only on a touch-capable device).
						if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
							config.Scenes[currentSceneIndex].Name == "Patch" {
							if handlePatchMouse(mx, my, config) {
								break
							}
						}
						// The reader draws its own toolbar and page: chips act, and a
						// press on the page starts a drag that scrolls it (pdfv_view.go).
						if pdfvMouseDown(mx, my, config) {
							break
						}
						// Files: the action sheet and the remote transfer browser are
						// modal over the list, and the breadcrumb bar and sort chip sit
						// above it, so they take a tap before the list rows do. The remote
						// browser is a Files session: off that screen it must not take the
						// tap, or it would swallow a press meant for the scene on display
						// (and its rows would download into a folder nobody can see).
						if feSheet.open {
							handleFeSheetMouse(mx, my, config)
							break
						}
						if feRemotePanelOnScreen(config) {
							handleFeRemoteMouse(mx, my, config)
							break
						}
						if handleFilesChromeMouse(mx, my, config) {
							break
						}
						// The floating page chip sits above scene elements and must
						// consume the tap before an overlapping input can open.
						if handleMediaNextPageClick(config, mx, my) {
							break
						}
						// Check recent panel (Continue cards)
						handleRecentMouseClick(mx, my, config)
						// Check other elements (input, button, searchresults)
						if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
							for i, elem := range config.Scenes[currentSceneIndex].Elements {
								if elem.Type == "input" {
									width, height := resolveElementDimensions(elem, 200, 40)
									if mx >= elem.X && mx <= elem.X+width && my >= elem.Y && my <= elem.Y+height {
										handleInputSelection(renderer, config, currentSceneIndex, i)
										// The keyboard owns input now; do not also run the clicks below.
										break
									}
								} else if elem.Type == "toggle" {
									tw, th := resolveElementDimensions(elem, 320, 48)
									if mx >= elem.X && mx <= elem.X+tw && my >= elem.Y && my <= elem.Y+th {
										selectedButtonIndex = i
										focusEngine.SetByElementIndex(i)
										toggleVariable(config, elem)
									}
								} else if elem.Type == "chat" {
									// Clicking the chat panel focuses the composer and
									// opens the virtual keyboard so the user can type.
									cw := getElementWidth(elem, 1160)
									chh := getElementHeight(elem, 480)
									if mx >= elem.X && mx <= elem.X+cw && my >= elem.Y && my <= elem.Y+chh {
										selectedButtonIndex = i
										focusEngine.SetByElementIndex(i)
										vkOpenChat()
									}
								} else if elem.Type == "themegallery" {
									if tab := themeGalleryTabAt(elem, mx, my); tab >= 0 {
										selectedButtonIndex = i
										focusEngine.SetByElementIndex(i)
										if tab != themeGalleryTab {
											themeGalleryTab = tab
											themeGalleryIndex = 0
											themeGalleryScroll = 0
											PlayNavSound()
										}
										continue
									}
									if idx := themeGalleryCardAt(elem, mx, my); idx >= 0 {
										themeGalleryIndex = idx
										selectedButtonIndex = i
										focusEngine.SetByElementIndex(i)
										presets := themeGalleryPresets()
										if idx < len(presets) {
											ApplyThemePreset(config, presets[idx])
										}
									}
								} else if elem.Type == "button" {
									// Home's cards are hit-tested by the layout
									// (handleHomeMouseClick); a config tile it does
									// not draw — the hidden Exit card — must not
									// stay pressable at its old spot.
									if homeLayoutActive && strings.EqualFold(elem.Style, "tile") {
										continue
									}
									font, _ := getCachedFont(config, elem.Font)
									width, height := buttonHitSize(elem, font)
									bx := elem.X
									by := elem.Y
									bw := width
									bh := height
									if homeLayoutActive && elem.Style == "tile" {
										if rect, ok := homeTileRect(i); ok {
											bx, by, bw, bh = rect.X, rect.Y, rect.W, rect.H
										}
									}
									if mx >= bx && mx <= bx+bw && my >= by && my <= by+bh {
										pressedButtonIndex = i
										pressStartTime = sdl.GetTicks64()
										selectedButtonIndex = i
										focusEngine.SetByElementIndex(i)
										handleTrigger(renderer, config, elem)
									}
								} else if elem.Type == "searchresults" {
									videos, ok := config.Variables.Custom[elem.Variable].([]VideoInfo)
									if !ok {
										continue
									}
									// The results box owns the tap: rows scrolled under an
									// edge keep hit rects the clip hides, and those must
									// not catch a press aimed at the controls above or
									// the footer below.
									if mx < elem.X || mx > elem.X+getElementWidth(elem, 1232) ||
										my < elem.Y || my > elem.Y+getElementHeight(elem, 456) {
										continue
									}
									// A click on a row's Audio / Video / Chat pill presses
									// that pill, even when the row is not the highlighted one.
									if action, idx := resultActionHitAt(mx, my); action != resultActionNone {
										// The pressed row becomes the highlighted one, so the
										// accent ring and the D-pad land where the finger did.
										focusedResultIndex = idx
										focusedResultAction = action
										performResultAction(action, idx, videos)
										continue
									}
									// Row hit detection, mirroring what the renderer drew:
									// the list layout draws 100px rows at a 114px pitch, the
									// grid draws cells, and both follow the smooth-scroll
									// offset. The old path always used the grid's math and
									// stopped at the first screenful, so on the shipped list
									// layout a tap landed a row short of the card it showed
									// and every row after the sixth was untappable.
									if idx := searchResultIndexAt(elem, videos, mx, my); idx >= 0 {
										selectedButtonIndex = i
										focusEngine.SetByElementIndex(i)
										focusedResultIndex = idx
										playVideoInfo(config, videos[idx])
									}
								} else if elem.Type == "dynamiclist" && elem.Variable == appsEntriesVar {
									// Apps launcher: hit-test the tile grid, not list rows.
									if entries := appGridEntries(config, elem); len(entries) > 0 {
										if idx := appGridIndexAt(elem, entries, mx, my); idx >= 0 {
											selectedButtonIndex = i
											focusEngine.SetByElementIndex(i)
											focusedFileIndex = idx
											launchAppEntry(config, entries[idx])
										}
									}
								} else if elem.Type == "dynamiclist" {
									entries, ok := config.Variables.Custom[elem.Variable].([]FileEntry)
									if ok {
										elemW := getElementWidth(elem, 600)
										maxVisible := dynamicListMaxVisible(elem)
										start := 0
										if focusedFileIndex >= maxVisible {
											start = focusedFileIndex - maxVisible + 1
										}
										end := start + maxVisible
										if end > len(entries) {
											end = len(entries)
										}
										// The row map is shared with renderDynamicList, so the
										// Live TV filter bar cannot shift clicks onto the row
										// above.
										if i2 := dynamicListRowAt(elem, start, end, my); i2 >= 0 && mx >= elem.X && mx <= elem.X+elemW {
											selectedButtonIndex = i
											focusEngine.SetByElementIndex(i)
											focusedFileIndex = i2
											feEnterFocused(config)
										}
									}
								} else if elem.Type == "favorites" {
									handleFavoritesMouseClick(mx, my, config)
								} else if elem.Type == "shortslist" {
									// Click on a Shorts cell to play it.
									shortsMutex.Lock()
									vids := shortsList
									shortsMutex.Unlock()
									if len(vids) > 0 {
										elemW := getElementWidth(elem, 1080)
										elemH := getElementHeight(elem, 500)
										layout := newShortsGridLayout(elem, elemW)
										for idx := range vids {
											cell := shortsGridCellRect(elem, layout, idx, shortsScrollY)
											if mx >= cell.X && mx <= cell.X+cell.W && my >= elem.Y && my <= elem.Y+elemH && my >= cell.Y && my <= cell.Y+cell.H {
												currentShortIdx = idx
												selectedButtonIndex = i
												focusEngine.SetByElementIndex(i)
												startShortAt(config, idx)
												break
											}
										}
									}
								} else if elem.Type == "packagelist" {
									handlePackageMouseClick(mx, my, config)
								} else if elem.Type == "textbrowser" {
									selectedButtonIndex = i
									focusEngine.SetByElementIndex(i)
									if textBrowserElementSource(elem) == textBrowserSourceWeb {
										addressRect, searchRect, filterRect, modeRect, zoomRect, goRect := textBrowserToolbarTapRects(config, elem)
										backRect, forwardRect := textBrowserHistoryRects(config, elem)
										if pointInRect(mx, my, textBrowserTouchTarget(backRect)) {
											browserBack(config)
										} else if pointInRect(mx, my, textBrowserTouchTarget(forwardRect)) {
											browserForward(config)
										} else if brwPadlockTap(config, elem, mx, my) {
											// The padlock at the start of the address field
											// opens Site info (brw_siteinfo.go).
										} else if hitTouchTarget(addressRect, mx, my) {
											handleTextBrowserAddressInput(renderer, config, elem)
										} else if searchRect.W > 0 && hitTouchTarget(searchRect, mx, my) {
											openBrowserSearch(renderer, config, elem)
										} else if filterRect.W > 0 && hitTouchTarget(filterRect, mx, my) {
											browserToggleAdFilter(config)
										} else if modeRect.W > 0 && hitTouchTarget(modeRect, mx, my) {
											browserToggleNavMode()
										} else if zoomRect.W > 0 && hitTouchTarget(zoomRect, mx, my) {
											// The chip steps the reader's text scale, wrapping so a
											// tap always does something.
											browserZoomCycle(config, elem)
										} else if mx >= goRect.X && mx <= goRect.X+goRect.W && my >= goRect.Y && my <= goRect.Y+goRect.H {
											loadTextBrowserURL(config, elem, textBrowserAddress(config))
										} else if browserCookieStripTap(mx, my, config) {
											// The strip names the hosts; tapping it is how a touch
											// user picks one to sign out.
										} else {
											// Tapped a link inside the rendered page (a no-op when the tap
											// landed on none).
											browserTapPageLink(renderer, config, elem, mx, my)
										}
									}
								}
							}
						}
					} else if e.Type == sdl.MOUSEBUTTONUP {
						isDraggingSeekBar = false
						// A tile carried across Edit Home's grid is put down
						// (home_edit.go).
						homeEditPointerUp(config, mx, my)
						// A press on a package card opens it on release, unless
						// it turned into a drag (pkginput.go).
						pkgPointerUp(config, mx, my)
						// A picture or PDF page drag ends; a tap that never moved
						// toggles the viewer's controls.
						filesMouseUp(config)
					}
				}
				focusEngine.SyncSelected()
			case *sdl.ControllerButtonEvent:
				// Controller input counts as activity too, otherwise a user
				// navigating with the D-pad sat at the idle cadence and every
				// press felt late (the frame rate never woke up).
				frameRateCtrl.NotifyInput()
				// A pad press makes the hints name buttons again (inputhints.go).
				if e.Type == sdl.CONTROLLERBUTTONDOWN {
					inputHintsNotePad()
				}
				// A ringing alarm owns the pad before anything else: the dock, the
				// first-run tour, a video's spinner and the wake filter below all
				// used to see the press first (alarmring.go).
				if alarmFiringActive() {
					alarmHandleController(e)
					continue
				}
				// The pad's version of that same clear button, matched to the hint
				// the idle screen draws. It has to be answered before the wake
				// below, which would take the screen away mid-action.
				if ss.active && e.Type == sdl.CONTROLLERBUTTONDOWN && screensaverClearButton(e.Button) &&
					screensaverBackdropPress(config, time.Now()) {
					continue
				}
				// A press that wakes the screen (the screensaver, or the dock's
				// darkened panel) only wakes it, and its release is swallowed
				// with it. Releases never wake: the release of X, which darkens
				// the dock, used to light it straight back up.
				if e.Type == sdl.CONTROLLERBUTTONDOWN {
					if wakeSwallowPress(wakeInputPad, int32(e.Button), false) {
						continue
					}
				} else if wakeSwallowRelease(wakeInputPad, int32(e.Button)) {
					continue
				}
				// The voice orb (voiceorb.go): the Menu button — the physical
				// GUIDE, before its fold into Back — opens and stops it, and
				// while it listens or offers a choice it owns the pad. It runs
				// before the quit chord and feeds that chord the presses it
				// keeps (Menu still counts as Select there, so Menu + Start
				// quits).
				if voiceOrbController(e, padEventRaw, config) {
					continue
				}
				// Select + Start is the handheld's system quit chord and is read
				// from the physical buttons, before remapping or any overlay: it
				// has to leave the app from a panel, a video or the keyboard too.
				if exitChordPressed(e) {
					requestAppExit()
					continue
				}
				// The controller test owns every pad event, releases included: a
				// held button is only visible if its release is seen. It reads the
				// physical buttons, before accessibility remapping — it is a
				// hardware test, and with SELECT remapped or used as the chord
				// modifier the hold-SELECT close never arrived, leaving a handheld
				// with no way out of the panel.
				if ctrldiag.open {
					ctrldiagButtonEvent(e)
					continue
				}
				// Hold Select + L1: mouse mode on / off (vcursor_mode.go). Read
				// from the physical buttons like the quit chord, before the remap;
				// L1's press is taken while Select is held, so the shoulder does
				// not also turn a page. vcursorTick times the hold.
				if e.Which != padRepeatWhich && e.Which != inmapSynthWhich && vcursorChordButton(e, config) {
					continue
				}
				// Accessibility remapping rewrites the physical button once, here,
				// before the overlay router or the scene sees the event. A chord
				// modifier press is consumed: it arms the next direction instead of
				// firing its own action. A queued hold-to-repeat press is already
				// the remapped direction (padRepeatWhich), and so is a press a
				// control profile queued (inmapSynthWhich).
				if e.Which != padRepeatWhich && e.Which != inmapSynthWhich && a11yRemapEvent(e) {
					continue
				}
				// While the virtual cursor is up, A is its click (a queued left
				// mouse press at the pointer); a D-pad press puts it away, or in
				// mouse mode moves it. After the remap, so a one-handed chord or a
				// remapped confirm clicks too.
				if vcursorButton(e, config) {
					continue
				}
				// Control profiles see every release here, before anything below
				// can drop one: a press they rewrote is released as what it
				// became, and one they held back for a hold, a double tap or the
				// Select + R1 switch is settled (inmap_runtime.go).
				if e.Type == sdl.CONTROLLERBUTTONUP && inmapButtonUp(e, config) {
					continue
				}
				if e.Type == sdl.CONTROLLERBUTTONDOWN && ambientHandleController(e) {
					continue
				}
				if e.Type == sdl.CONTROLLERBUTTONDOWN {
					// A video being prepared owns the pad: the spinner says so, and
					// B is the one way out of it.
					if videoPrepareActive() {
						if e.Button == sdl.CONTROLLER_BUTTON_B {
							cancelVideoPrepare()
						}
						continue
					}
					// The recovery card is modal and owns the pad while it is up.
					if recoveryCardHandleController(e, config) {
						continue
					}
					if handleLaunchFailureController(e) {
						continue
					}
					// The first-run tour is modal and above every other overlay.
					if handleOnboardingController(e, config) {
						continue
					}
					// Route overlays first (highest priority).
					if handleControllerEvent(e, config) {
						continue
					}
					// Select opens the browser's menu from anywhere on its screen,
					// and the menu owns the pad while it is up (brw_menu.go). It is
					// routed here rather than in the overlay router because its rows
					// open keyboards, which need the renderer.
					if brwMenuController(renderer, e, config) {
						continue
					}
					focusEngine.SyncSelected()
					if virtualKeyboardActive {
						vkHandleControllerButton(e.Button, config)
					} else if activeSceneIndex != -1 {
						// Input field active - handled by virtualKeyboardActive
					} else {
						// The top bar, the home layout, the Patch list and the screen's
						// own module, in that order: a focused bar button answers to A
						// before the screen binds it, and Up on a screen with nothing
						// focusable steps onto the bar.
						if screenPadPress(e, config) {
							continue
						}
						// Video control shortcuts (always active when video is playing).
						// The player owns B while it is on screen, so the scene's generic
						// back must not fire on the same press. Once the video floats in
						// its mini window the scene owns the pad again.
						videoWasPlaying := snapshotVideoState().phase != "idle" && !videoMiniVisible()
						if HandleVideoControllerInput(e) {
							continue
						}
						// X opens the media search field from anywhere in a results
						// scene, matching the keyboard path above.
						if e.Button == sdl.CONTROLLER_BUTTON_X && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
							sceneHasSearchResults(config.Scenes[currentSceneIndex]) {
							if openSearchShortcut(config) {
								continue
							}
						}
						// Y in Media opens the Shorts feed (the controller twin of
						// Ctrl+Shift+S) and keeps playing after each short ends.
						if e.Button == sdl.CONTROLLER_BUTTON_Y && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
							config.Scenes[currentSceneIndex].Name == "Tube" {
							if openShortsFeed(config) {
								continue
							}
						}
						// Files: the action sheet and the remote browser are modal,
						// and the screen's own Y / X act before the element dispatch.
						if feSceneActive(config) {
							if ctrlFeSheet(e, config) {
								continue
							}
							if feRemotePanel.open && ctrlFeRemote(e, config) {
								continue
							}
							if handleFilesController(e, config) {
								continue
							}
						}
						// Handle navigation
						curScene := config.Scenes[currentSceneIndex]
						if selectedButtonIndex < 0 || selectedButtonIndex >= len(curScene.Elements) {
							selectedButtonIndex = findFirstSelectableElement(curScene)
						}
						if selectedButtonIndex < 0 || selectedButtonIndex >= len(curScene.Elements) {
							// Scenes drawn without config elements (Video, Pomodoro, ...)
							// have no element branch above, but B still has to leave them.
							if !homeLayoutActive && (e.Button == sdl.CONTROLLER_BUTTON_B || e.Button == sdl.CONTROLLER_BUTTON_BACK) {
								goBackScene(config)
							}
							continue
						}
						curElem := curScene.Elements[selectedButtonIndex]
						// B/BACK always leaves the scene unless a deeper view owns it.
						// Handling it once, before the per-element switches, means no
						// element type can silently swallow the back button.
						if !homeLayoutActive && !videoWasPlaying && (e.Button == sdl.CONTROLLER_BUTTON_B || e.Button == sdl.CONTROLLER_BUTTON_BACK) {
							switch {
							case curElem.Type == "searchresults":
								handleSearchResultsAction(ActionBack, config)
							case curElem.Type == "textbrowser" && textBrowserElementSource(curElem) == textBrowserSourceWeb:
								handleBrowserController(renderer, e, config, curElem)
							case curElem.Type == "packagelist":
								packageListController(e, config)
							case curScene.Name == "FileExplorer":
								feBack(config)
							default:
								goBackScene(config)
							}
							focusEngine.SyncSelected()
							continue
						}
						if curElem.Type == "searchresults" {
							// Grid navigation with controller via the semantic action handler.
							if focusedResultIndex < 0 {
								focusedResultIndex = 0
							}
							switch e.Button {
							case sdl.CONTROLLER_BUTTON_DPAD_UP:
								handleSearchResultsAction(ActionNavigateUp, config)
							case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
								handleSearchResultsAction(ActionNavigateDown, config)
							case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
								handleSearchResultsAction(ActionNavigateLeft, config)
							case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
								handleSearchResultsAction(ActionNavigateRight, config)
							case sdl.CONTROLLER_BUTTON_A:
								handleSearchResultsAction(ActionConfirm, config)
							case sdl.CONTROLLER_BUTTON_B:
								handleSearchResultsAction(ActionBack, config)
							}
						} else if curElem.Type == "dynamiclist" {
							if curScene.Name == "FileExplorer" {
								if action, ok := feControllerAction(e.Button); ok && feListAction(config, action) {
									continue
								}
							}
							// Apps launcher: two-dimensional D-pad navigation over tiles.
							if curElem.Variable == appsEntriesVar && appGridController(e.Button, config) {
								continue
							}
							// Live TV: D-pad Left/Right switch category, X searches,
							// Y stars the highlighted channel.
							if curElem.Variable == tvEntriesVar && tvListControllerAction(e.Button, config) {
								continue
							}
							entries, _ := getSceneFileEntries(config, curScene)
							n := 0
							if entries != nil {
								n = len(entries)
							}
							switch e.Button {
							case sdl.CONTROLLER_BUTTON_DPAD_UP:
								if focusedFileIndex > 0 {
									focusedFileIndex--
								}
							case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
								if focusedFileIndex < n-1 {
									focusedFileIndex++
								}
							case sdl.CONTROLLER_BUTTON_A:
								feEnterFocused(config)
							case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
								// File Explorer: B climbs the directory tree; at the root
								// it returns to the previous scene. Every other list
								// treats B as back instead of "move up", which looked
								// like the button did nothing at the top of a list.
								if curScene.Name == "FileExplorer" {
									feBack(config)
								} else {
									goBackScene(config)
								}
							}
						} else if curElem.Type == "themegallery" {
							presets := themeGalleryPresets()
							switch e.Button {
							case sdl.CONTROLLER_BUTTON_LEFTSHOULDER, sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
								step := 1
								if e.Button == sdl.CONTROLLER_BUTTON_LEFTSHOULDER {
									step = -1
								}
								if themeGallerySwitchTab(step) {
									PlayNavSound()
								}
								// Consumed: the list pager below must not also
								// see this press.
								continue
							case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
								themeGalleryMove(curElem, -1)
								PlayNavSound()
							case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
								themeGalleryMove(curElem, 1)
								PlayNavSound()
							case sdl.CONTROLLER_BUTTON_DPAD_UP:
								themeGalleryMove(curElem, -themeGalleryColumns(curElem))
								PlayNavSound()
							case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
								themeGalleryMove(curElem, themeGalleryColumns(curElem))
								PlayNavSound()
							case sdl.CONTROLLER_BUTTON_A:
								if themeGalleryIndex >= 0 && themeGalleryIndex < len(presets) {
									ApplyThemePreset(config, presets[themeGalleryIndex])
								}
							case sdl.CONTROLLER_BUTTON_Y:
								// The community library is a keyboard C on a desktop; a
								// handheld has no keyboard, so Y opens it from the pad.
								// Consumed here so no later handler re-reads the press.
								openThemeCommunityPanel(config)
								continue
							case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
								goBackScene(config)
							}
						} else if curElem.Type == "toggle" {
							switch e.Button {
							case sdl.CONTROLLER_BUTTON_DPAD_UP:
								handleToggleAction(config, curElem, ActionNavigateUp)
							case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
								handleToggleAction(config, curElem, ActionNavigateDown)
							case sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
								handleToggleAction(config, curElem, ActionConfirm)
							case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
								goBackScene(config)
							}
						} else if curElem.Type == "favorites" {
							// The library owns its pad handling so the tabs, grouping and
							// scrolling stay in one place with the renderer.
							handleFavoritesController(e, config)
						} else if curElem.Type == "textbrowser" && textBrowserElementSource(curElem) == textBrowserSourceWeb {
							// The web browser owns its pad: links, the address bar and the
							// way out to the button row all live in browser.go.
							handleBrowserController(renderer, e, config, curElem)
						} else if curElem.Type == "textbrowser" {
							switch e.Button {
							case sdl.CONTROLLER_BUTTON_X:
								if textBrowserElementSource(curElem) == textBrowserSourceWeb {
									openBrowserLinkPanel(config, curElem)
								}
							case sdl.CONTROLLER_BUTTON_DPAD_UP:
								textBrowserScrollVelocity -= textBrowserScrollAccel
								if textBrowserScrollVelocity < -textBrowserScrollMaxVel {
									textBrowserScrollVelocity = -textBrowserScrollMaxVel
								}
								textBrowserScrollCooldown = 8
							case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
								textBrowserScrollVelocity += textBrowserScrollAccel
								if textBrowserScrollVelocity > textBrowserScrollMaxVel {
									textBrowserScrollVelocity = textBrowserScrollMaxVel
								}
								textBrowserScrollCooldown = 8
							case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
								textBrowserScrollY -= 10
								if textBrowserScrollY < 0 {
									textBrowserScrollY = 0
								}
								textBrowserScrollVelocity = 0
								textBrowserScrollCooldown = 0
							case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
								textBrowserScrollY += 10
								textBrowserScrollVelocity = 0
								textBrowserScrollCooldown = 0
							case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
								goBackScene(config)
							}
						} else if curElem.Type == "shortslist" {
							switch e.Button {
							case sdl.CONTROLLER_BUTTON_DPAD_UP:
								if currentShortIdx > 0 {
									currentShortIdx--
								}
							case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
								shortsMutex.Lock()
								vCount := len(shortsList)
								shortsMutex.Unlock()
								if currentShortIdx < vCount-1 {
									currentShortIdx++
								}
							case sdl.CONTROLLER_BUTTON_A:
								shortsMutex.Lock()
								vs := shortsList
								shortsMutex.Unlock()
								if currentShortIdx >= 0 && currentShortIdx < len(vs) {
									playVideoInfo(config, vs[currentShortIdx])
								}
							case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
								goBackScene(config)
							}
						} else if curElem.Type == "packagelist" {
							packageListController(e, config)
						} else if curElem.Type == "chat" {
							// The Chat scene's module owned the pad before this point
							// (chatSceneController). A "chat" element elsewhere — the
							// Terminal keeps one for its prompt row — only opens the
							// keyboard; the Terminal binds A itself, so it never gets here.
							if e.Button == sdl.CONTROLLER_BUTTON_A && !isTerminalScene() {
								vkOpenChat()
							}
						} else {
							// Normal button/input navigation
							switch e.Button {
							case sdl.CONTROLLER_BUTTON_DPAD_UP:
								if curScene.Name != "FileExplorer" || curElem.Type != "button" || !feFocusList(config) {
									moveSelection(config, ActionNavigateUp)
								}
							case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
								moveSelection(config, ActionNavigateDown)
							case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
								moveSelection(config, ActionNavigateLeft)
							case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
								moveSelection(config, ActionNavigateRight)
							case sdl.CONTROLLER_BUTTON_A:
								if activateChromeFocus(config) {
									break
								}
								if selectedButtonIndex >= 0 && selectedButtonIndex < len(config.Scenes[currentSceneIndex].Elements) {
									elem := config.Scenes[currentSceneIndex].Elements[selectedButtonIndex]
									if elem.Type == "input" {
										handleInputSelection(renderer, config, currentSceneIndex, selectedButtonIndex)
									} else if elem.Type == "textbrowser" && textBrowserElementSource(elem) == textBrowserSourceWeb {
										handleTextBrowserAddressInput(renderer, config, elem)
									} else if elem.Type == "textbrowser" || elem.Type == "shortslist" || elem.Type == "packagelist" {
										// Display-only elements: A is a no-op (scroll with D-pad).
									} else {
										handleTrigger(renderer, config, elem)
									}
								}
							case sdl.CONTROLLER_BUTTON_B:
								// Generic back on non-home scenes: returns to the
								// previous scene (or Home). Never exits the app.
								if !homeLayoutActive {
									goBackScene(config)
								}
							case sdl.CONTROLLER_BUTTON_X:
								// Search shortcut: open the Tube search field on Tube,
								// jump there from anywhere else.
								openSearchShortcut(config)
							case sdl.CONTROLLER_BUTTON_BACK:
								// BACK is the handheld's back button too: leaving the
								// scene is what users expect, not a jump to Settings.
								goBackScene(config)
							}
							// Shoulders are contextual now (see shoulders.go): they page
							// lists, seek and change video, instead of switching scene.
							if e.Button == sdl.CONTROLLER_BUTTON_LEFTSHOULDER || e.Button == sdl.CONTROLLER_BUTTON_RIGHTSHOULDER {
								side := padL1
								if e.Button == sdl.CONTROLLER_BUTTON_RIGHTSHOULDER {
									side = padR1
								}
								if handleShoulderPress(side, config) {
									continue
								}
							}
						}
						focusEngine.SyncSelected()
					}
				}
			case *sdl.ControllerAxisEvent:
				// The sticks' live values see every sample, swallowed or not:
				// stick navigation, the virtual cursor and the calibration
				// screen read them (stickcal.go).
				stickNoteAxis(e)
				// A ringing alarm is modal: the stick must not move the focus of
				// the screen behind it (its two buttons answer the D-pad).
				if alarmFiringActive() {
					continue
				}
				// Only a deliberate deflection is input (axisDeflected): a
				// drifting stick kept the screen awake and the frame rate up.
				// The controller test still sees every value.
				// A stick's dead zone and resting centre are its calibration
				// (stickcal.go), so a stick resting off centre is not input.
				deflected := axisDeflectedOn(e.Axis, e.Value)
				if ctrldiag.open || deflected {
					frameRateCtrl.NotifyInput()
				}
				if deflected {
					inputHintsNotePad()
				}
				if ctrldiag.open {
					if deflected {
						screensaverNotifyInput()
					}
					ctrldiagAxisEvent(int(e.Axis), e.Value)
				} else if axisWakeFilter(e.Axis, e.Value) {
					// A push that woke the screen only woke it (axisWakeFilter).
					// The right stick is the virtual cursor's, and L2/R2 scroll
					// under it while it is up (virtualcursor.go).
					if !vcursorAxis(e, config) {
						handleControllerAxis(e, config)
					}
				}
			case *sdl.ControllerDeviceEvent:
				handleControllerDevice(e)
				// After the pad is (re)opened, so the sticks are read from it
				// (virtualcursor.go, stickcal.go).
				vcursorResetAxes()
			}
		}

		drainCustom(config)
		// A held right stick moves the virtual cursor every frame, and a held
		// L2/R2 keeps scrolling under it.
		vcursorTick(config)
		// A hot-reloaded config is installed here, on the main thread and
		// between frames, never under a frame that is still drawing.
		if next := takePendingConfigReload(); next != nil {
			config = applyConfigReload(config, next)
		}
		syncLEDLoopSettingsIfDue(config, time.Now())

		// Apply kinetic scroll for text browser panels
		if textBrowserScrollCooldown > 0 {
			textBrowserScrollCooldown--
		} else if textBrowserScrollVelocity != 0 {
			textBrowserScrollVelocity *= textBrowserScrollFriction
			if textBrowserScrollVelocity > -0.05 && textBrowserScrollVelocity < 0.05 {
				textBrowserScrollVelocity = 0
			}
		}
		if textBrowserScrollVelocity != 0 {
			textBrowserScrollY += int32(textBrowserScrollVelocity)
			// Clamp scroll position based on current scene textbrowser elements.
			// The limit comes from the renderer's own measure: a styled web page
			// scrolls by layout rows, which the old 22px-line count here could
			// not see, so every kinetic scroll on a page snapped back to the top.
			if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
				for _, elem := range config.Scenes[currentSceneIndex].Elements {
					if elem.Type != "textbrowser" {
						continue
					}
					if scrollMax := textBrowserScrollLimit(config, elem); scrollMax >= 0 {
						if textBrowserScrollY < 0 {
							textBrowserScrollY = 0
						}
						if textBrowserScrollY > scrollMax {
							textBrowserScrollY = scrollMax
						}
					}
					break
				}
			}
		}

		// Update hover state for buttons
		hoveredButtonIndex = -1
		if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
			for i, elem := range config.Scenes[currentSceneIndex].Elements {
				if elem.Type == "button" {
					font, _ := getCachedFont(config, elem.Font)
					width, height := buttonHitSize(elem, font)
					if mouseX >= elem.X && mouseX <= elem.X+width && mouseY >= elem.Y && mouseY <= elem.Y+height {
						hoveredButtonIndex = i
						break
					}
				}
			}
		}

		// Animate hover progress
		targetHover := float64(0)
		if hoveredButtonIndex >= 0 {
			targetHover = 1.0
		}
		if effectiveReducedMotion(config) {
			hoverAnimProgress = targetHover
		} else {
			hoverAnimProgress = lerp(hoverAnimProgress, targetHover, 0.2)
			if hoverAnimProgress > targetHover-0.01 && hoverAnimProgress < targetHover+0.01 {
				hoverAnimProgress = targetHover
			}
		}

		// Clear press feedback after duration (instantly in reduced-motion mode).
		if pressedButtonIndex >= 0 {
			if effectiveReducedMotion(config) || sdl.GetTicks64()-pressStartTime > pressDuration {
				pressedButtonIndex = -1
			}
		}

		// Ambient screen-off: the panel is black and audio keeps decoding, so
		// there is nothing to draw. Paint the black frame once, then idle
		// cheaply instead of presenting identical frames all night.
		if ambientIsScreenOff() {
			if ambientNeedsBlackFrame() {
				renderer.SetDrawColor(0, 0, 0, 255)
				renderer.Clear()
				renderer.Present()
				ambientMarkBlackFrameDrawn()
			}
			// The dark dock is the idle screen too: its low-power state keeps
			// following it (saver_power.go), e.g. dropping to the quiet profile
			// when the sleep timer stops the music.
			saverPowerTick(time.Now())
			sdl.Delay(200)
			frameCount++
			if now := time.Now(); now.Sub(lastAlarmCheck) >= time.Second {
				lastAlarmCheck = now
				alarmCheck(config, now)
			}
			continue
		}

		// Contextual widgets: battery saver and time-of-day theme, checked once a
		// second before the frame is drawn so a theme switch lands immediately.
		widgetsTick(config)
		// Which words the hints use this frame: the pad's buttons or the
		// keyboard's keys (inputhints.go).
		inputHintsTick(config)
		// A video whose decoder reached the end releases the player here, even
		// when its scene is not the one on screen (the flag used to wait for a
		// repaint of a scene the user had already left).
		videoSyncPlaybackEnd()
		// Frame textures a stopped video left behind are destroyed here, on the
		// render thread (videoplayer.go).
		mediaReleaseRetiredFrames()
		// The Media screen follows connectivity and its background loads.
		mediaHubTick(config)
		// Shorts feed: start the download- pending first short, then keep the
		// feed rolling after each one finishes.
		drainShortsAutoplay(config)
		// Up Next: a video that ended with something queued hands over here,
		// after drainShortsAutoplay has had the first say over a Shorts feed.
		drainUpNextAutoplay(config)
		playtimeTick(config)
		// Per-game milestones (1, 5 and 10 hours) follow the playtime ledger;
		// a cheap compare unless a game's time just moved (gmeta_ui.go).
		gmetaTick(config)
		// Dock and ambient screen: clock, weather, now-playing and photos when the
		// device is charging or idle (see ambient.go). Runs here, on the event
		// thread, because its entry check reads modal flags.
		ambientAutoTick(config, time.Now())
		// Screensaver: the same idle timeout, checked here on the event thread
		// so playback, downloads and open panels can hold it off.
		screensaverAutoTick(config, time.Now())
		// Remote transfers report here, not from their copying goroutine. The
		// pump runs even when the Files remote browser is off screen: a transfer
		// the user walked away from still has to finish and clear its busy flag.
		feRemoteDrain(config)
		// Archive extractions report here the same way: the toast, the relisted
		// folder and the cursor on the result (files_extract_ui.go).
		feExtractDrain(config)
		// The dock/ambient screen takes the whole panel when it is up, then the
		// screensaver, then the scene.
		if ambientIsActive() {
			renderAmbient(renderer, config)
		} else if ss.active {
			renderScreensaver(renderer, config, sdl.GetTicks64())
		} else {
			renderScene(renderer, config, config.Scenes[currentSceneIndex])
		} // Full screen video covers the scene and its chrome entirely.
		if videoFullscreen && playerPhase() != "idle" {
			renderVideoFullscreen(renderer, config)
		} else if homeCalendarOpen && !homeLayoutActive {
			// The clock popover is shared chrome: Home draws it in its own
			// renderer, every other scene draws it here above the status bar.
			renderCalendarPopover(renderer, config)
		}
		// A video the user backed out of keeps playing in a small window over
		// the scene. The dock and the screensaver take the whole panel, so the
		// window stays out of them.
		if !ambientIsActive() && !ss.active {
			renderVideoMini(renderer, config)
		}

		// Scene transition effects. Reduced motion and the transition config
		// both snap the change instead of waiting out a fade.
		if (effectiveReducedMotion(config) || motionInstant(config)) && (transitionPhase == "fade-out" || transitionPhase == "fade-in") {
			if transitionPhase == "fade-out" {
				completeSceneTransition(config)
			} else {
				transitionPhase = "none"
			}
		}
		// Scene fade-out -> fade-in, timed by anim.go so the handheld gets a
		// snappy transition and both phases read the same theme colour.
		if transitionPhase == "fade-out" || transitionPhase == "fade-in" {
			elapsed := float64(sdl.GetTicks64() - sceneFadeStart)
			alive := !uiSceneFadeDone(config, transitionPhase, elapsed)
			if transitionPhase == "fade-out" {
				if !alive {
					sceneFadeAlpha = 255
					completeSceneTransition(config)
				} else {
					sceneFadeAlpha = uiSceneScrimAlpha(config, transitionPhase, elapsed)
				}
			} else {
				if !alive {
					sceneFadeAlpha = 0
					transitionPhase = "none"
				} else {
					sceneFadeAlpha = uiSceneScrimAlpha(config, transitionPhase, elapsed)
				}
			}

			// During fade-out the old scene is covered; during fade-in the new
			// scene is revealed. sceneFadeAlpha now carries the opacity directly
			// (255 = covered), which is 0..255 in both phases.
			if sceneFadeAlpha > 0 {
				scrim := uiSceneScrimColor()
				renderer.SetDrawColor(scrim.R, scrim.G, scrim.B, sceneFadeAlpha)
				renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
				// accent sweep along the bottom edge
				renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, sceneFadeAlpha/2)
				renderer.FillRect(&sdl.Rect{X: 0, Y: screenHeight - 4, W: screenWidth, H: 4})
			}
		}

		// Image viewer overlay
		if imageViewerPath != "" {
			renderImageViewer(renderer, config, imageViewerPath)
		}

		// Loading overlay (spinner, status line and a wrapped JukaLang slogan).
		// A video being resolved by yt-dlp uses the same card: it is the moment
		// the app has nothing else to show, and it also blocks input below.
		if config.Variables.LoadingSpinner || transitionPhase != "none" || videoPrepareActive() {
			renderLoadingCard(renderer, config)
		}

		// A held D-pad keeps moving the keyboard highlight; without this a long
		// walk across the grid was twenty separate presses. Holding a direction
		// also counts as input, or the idle cadence would drop the repeat rate
		// after three seconds.
		if virtualKeyboardActive {
			vkTickNavRepeat()
			if dx, dy := vkControllerNavDirection(); dx != 0 || dy != 0 {
				frameRateCtrl.NotifyInput()
			}
		}
		// A held D-pad repeats in menus too (navrepeat.go). The keyboard owns the
		// pad while it is up, so that path stays with vkTickNavRepeat above.
		if padTickRepeat() && frameRateCtrl != nil {
			frameRateCtrl.NotifyInput()
		}
		// Control profiles decide their held-back presses here — a hold that
		// reached its time, a lone tap whose double-tap window closed — and
		// follow the screen for per-screen profiles; the pass-and-play turn
		// timer runs from here too (inmap_runtime.go, hotseat.go).
		if inmapTick(config) && frameRateCtrl != nil {
			frameRateCtrl.NotifyInput()
		}
		// SDL text input stays enabled for the whole session: the on-screen
		// keyboard, the chat composer and the unit converter all depend on
		// text-input events, and the keyboard target decides where each
		// character lands. Toggling it per frame used to swallow keystrokes.
		if !virtualKeyboardActive {
			// The chat composer takes typed text only while it is typing inline
			// (Enter started it). It used to be on whenever the chat element had
			// focus, which typed "b" into the draft instead of going back.
			chatInputActive = chatComposerTakesText(config)
		}

		// Patch scene: drain worker events and apply hold-to-confirm progress.
		PatchSceneFrame()
		// Watch Party polling and remote playhead correction. It never blocks:
		// the HTTP request runs on its own goroutine.
		partyTick(config)
		// Chat: load the history, notice the scene opening and closing, start
		// the sender for queued messages, persist what arrived (chatstate.go).
		chatTick(config)
		// Bug reports queued while offline go out on their own once the
		// device is back online (bugrepsend.go); this is a cheap check.
		bugrepOutboxTick()
		// Nearby devices: the listener follows its switch, and handoff samples
		// what is on screen and asks the paired devices when Home comes up
		// (peer.go, hoff.go). The per-frame cost is a clock read.
		peerTick(config)
		if appRestartRequested.Load() {
			// A staged update was handed to the swap helper; quit so it can
			// replace the executable and relaunch JukaHub.
			running = false
			continue
		}

		// Offer a newly discovered release once.
		maybeOfferUpdate()

		// Quick Settings overlay.
		if qs.open {
			renderQuickSettings(renderer, config)
		}

		// Smart Home overlay (Home Assistant, Pi-hole, Tailscale, WireGuard).
		if smartHomePanel.open {
			renderSmartHomePanel(renderer, config)
		}

		// PPSSPP stick calibration wizard.
		if calibrationPanel.open {
			renderStickCalibrationPanel(renderer, config)
		}

		// Community hub (changelog, bug report, telemetry consent).
		if communityPanel.open {
			renderCommunityPanel(renderer, config)
		}

		// Community theme library (browse and install palettes from the gallery).
		if themeCommunityPanel.open {
			renderThemeCommunityPanel(renderer, config)
		}

		// Developer / power-user tools overlay.
		if powerTools.open {
			renderPowerToolsPanel(renderer, config)
		}

		// Process list overlay (inspect and kill processes).
		if processPanel.open {
			renderProcessPanel(renderer, config)
		}

		// Files: the action sheet and the remote transfer browser. The
		// browser is drawn only on the screen it belongs to; the panels
		// around it are top-bar overlays that belong to every screen.
		if feSheet.open {
			renderFeSheet(renderer, config)
		}
		if feRemotePanelOnScreen(config) {
			renderFeRemotePanel(renderer, config)
		}

		// Memory panel (RAM scan and edit for one process).
		if memPanel.open {
			renderMemPanel(renderer, config)
		}

		// Text file editor (opened from Files or Patch).
		if fileEditor.open {
			renderFileEditor(renderer, config)
		}

		// Controller test (raw axes, drift verdicts, button chips).
		if ctrldiag.open {
			renderCtrldiagPanel(renderer, config)
		}

		// Podcast library overlay (shows, episodes and their resume marks).
		if podPanel.open {
			renderPodcastPanel(renderer, config)
		}

		// LED style panel (mode, effect, colour, brightness, sync).
		if ledPanel.open {
			renderLEDPanel(renderer, config)
		}

		// IPStream library overlay.
		if ipPanelOpen {
			renderIPPanel(renderer, config)
		}

		// Downloads overlay (offline music queue and SD card usage).
		if dlPanel.open {
			renderDownloadsPanel(renderer, config)
		}

		// Game library overlay (consoles, games and box art).
		if gameLib.open {
			renderGameLibraryPanel(renderer, config)
		}

		// LAN play overlay: sits on top of the library it was opened from.
		if netplayPanel.open {
			renderNetplayPanel(renderer, config)
		}

		// Emulators overlay (availability list).
		if emuPanel.open {
			renderEmuPanel(renderer, config)
		}

		// Game Profiles overlay (Wine/Box64/native launcher).
		if gpPanel.open {
			renderGameProfilesPanel(renderer, config)
		}

		// Smart Pro Compatibility overlay (reports, boost plans, core builds).
		if cpPanel.open {
			renderCompatibilityPanel(renderer, config)
		}

		// Kept core builds overlay: every retained build, with its date.
		if kbPanel.open {
			renderCoreBuildsPanel(renderer, config)
		}

		// Windows Apps overlay (Wine + WestonPack launcher).
		if winAppsPanel.open {
			renderWindowsAppsPanel(renderer, config)
		}

		// PS Remote Play overlay (JukaHub's own PS4/PS5 client).
		if chiakiPanel.open {
			renderChiakiPanel(renderer, config)
		}

		// Remote cast: share link, QR and the on/off switch.
		if castPanel.open {
			renderCastPanel(renderer, config)
		}

		// Audio settings overlay: layout, art opacity, screen-off controls.
		if musicSettings.open {
			renderMusicSettingsPanel(renderer, config)
		}

		// Accessibility overlay: text size, palettes and controller mapping.
		if a11y.open {
			renderAccessibilityPanel(renderer, config)
		}

		// Save Manager overlay (snapshots, restore, sync preparation).
		if savePanel.open {
			renderSaveManagerPanel(renderer, config)
		}

		// Curation Hub overlay (essentials, UI profiles, compatibility votes).
		if curationPanel.open {
			renderCurationPanel(renderer, config)
		}

		// PortMaster Ports overlay (install PortMaster, launch installed ports).
		if portsPanel.open {
			renderPortsPanel(renderer, config)
		}

		// Browser link picker (anchors extracted from the current page).
		if browserLinkPanel.open {
			renderBrowserLinkPanel(renderer, config)
		}

		// Saved-cookie picker (forget one site instead of the whole jar).
		if browserCookiePanel.open {
			renderBrowserCookiePanel(renderer, config)
		}

		// Saved-page picker (open a bookmark, or remove one with X).
		if browserBookmarkPanel.open {
			renderBrowserBookmarkPanel(renderer, config)
		}

		// Site info for the page on screen (connection, certificate, headers).
		if brwInfo.open {
			brwInfoRender(renderer, config)
		}

		// The browser's Select menu: every action the full-screen page keeps
		// out of sight (brw_menu.go).
		if brwMenu.open {
			brwMenuRender(renderer, config)
		}

		// RetroAchievements overlay (recent unlocks + game progress).
		if raPanel.open {
			renderRetroAchievementsPanel(renderer, config)
		}

		// Wallpaper picker overlay.
		if wpOpen {
			renderWallpaperPicker(renderer, config)
		}

		// Phone Companion pairing card.
		if companion.open {
			renderCompanionOverlay(renderer, config)
		}

		// Chat overlays: the member list and the speaker's profile card float
		// above the chat panel (which was drawn with the scene). The card is
		// drawn last so opening a profile from the list covers it.
		if chatMembers.open {
			renderChatMembers(renderer, config)
		}
		if chatProfile.open {
			renderChatProfile(renderer, config)
		}
		// A message's list of files (chatfiles.go).
		if chatFileSheet.open {
			setSheetDraw(renderer, config, &chatFileSheet)
		}

		// Update prompt is the topmost modal.
		if updatePrompt.open {
			renderUpdatePrompt(renderer, config)
		}

		// Shortcut hint at bottom (hidden when overlays are open).
		if !shortcutsOpen && !clipSearchOpen && !globalSearchOpen && !qs.open && !updatePrompt.open && !companion.open && !smartHomePanel.open && !calibrationPanel.open && !chatOverlayOpen() && !ambientIsActive() {
			renderShortcutHint(renderer, config)
		}

		// Overlay renderers (rendered on top of everything).
		if shortcutsOpen {
			renderShortcutsOverlay(renderer, config)
		}
		// The controller legend sits with the other help overlays, drawn last so
		// it covers everything it opened over (including Quick Settings rows).
		if controlsLegend.open {
			renderControlsLegend(renderer, config)
		}
		if clipSearchOpen {
			renderClipboardOverlay(renderer, config)
		}
		if globalSearchOpen {
			renderGlobalSearch(renderer, config)
		}
		// Virtual keyboard overlay (open for chat and input elements).
		if virtualKeyboardActive {
			renderKeyboard(renderer, config)
		}
		// First-run tour sits above every scene and overlay.
		if onboarding.active {
			renderOnboarding(renderer, config)
		}
		if launchFailure.open {
			renderLaunchFailure(renderer, config)
		}
		// The recovery card sits above the tour and the launch-failure card.
		renderRecoveryCard(renderer, config)
		// An enlarged QR code covers everything: the panel that opened it is
		// what made it unscannable, so it must not show through.
		if qrZoomActive() {
			renderQRZoom(renderer, config)
		}

		// A finished time-lapse is assembled off the render thread; its notice is
		// shown here, where the toast state belongs.
		timelapseDrainNotice()
		renderToast(renderer, config)
		renderInstallBar(renderer, config)
		// The activity strip sits with the installer card so it is drawn over
		// every scene the way the toasts are, and it can echo the card's own
		// operation when the card is under it (see activity.go).
		renderActivityStrip(renderer, config)
		// A trophy unlock celebrates above every scene and panel (card, confetti,
		// chime); only a ringing alarm, drawn next, covers it. trophy_celebrate.go.
		trophyRenderOverlay(renderer, config)
		// A ringing alarm covers everything: the toasts, the installer card, the
		// first-run tour and any panel under them. It is the one thing on screen
		// that cannot wait for the user to finish what they were doing.
		if alarmFiringActive() {
			renderAlarmRing(renderer, config)
		}

		// Companion screen view: capture on the render thread only when the
		// phone asked for a frame (see companion_screen.go).
		captureCompanionScreen(renderer)
		// A Screenshot picked from the Start menu lands on the frame the user
		// was looking at, not on whatever is drawn next.
		capturePendingScreenshot(renderer)
		// The voice orb, after both captures: the screenshot "take a
		// screenshot" asked for shows the screen, not the orb (voiceorb.go).
		voiceOrbRender(renderer, config)
		// The virtual cursor goes on last, after both captures, so it is on
		// screen but never in a screenshot (virtualcursor.go).
		vcursorRender(renderer)
		// The brightness controller's main-thread work for the frame (the
		// probe, a saved level, the header cards a full-screen surface now
		// covers), then — where no brightness control exists — the software
		// dimmer, which darkens the finished frame the way a backlight would
		// (brightnessctl.go): after the captures, so a screenshot is not
		// darkened with it.
		brightnessFrameTick()
		brightnessSoftRender(renderer)

		renderer.Present()
		// Measured after Present, so the idle delay at the end of the loop is
		// not counted as frame cost.
		frameStatsRecord(time.Since(frameStart))
		if !startupReported {
			startupReported = true
			startupMarkNow("first-frame")
			startupLog()
			// Remember this successful boot so a relaunch inside the warm window
			// can skip the splash. Best-effort; a read-only card still boots.
			startupRecordFirstFrame(startupElapsed())
			// The boot guard's health clock starts at the first frame: a session
			// that keeps running past it is a healthy boot (bootguard.go).
			bootGuardFirstFrame()
			// Bench mode measures boot and stops before the event loop settles
			// into idle work, so CI can assert on the number.
			if startupBenchEnabled() {
				running = false
				continue
			}
			// Only now do the network/background tasks get the CPU — a frame's
			// worth at a time, so the screen just shown stays responsive.
			deferredStartupPending = true
		}
		if deferredStartupPending {
			deferredStartupPending = runDeferredStartupFor(deferredStartupFrameBudget)
		}
		animTime = float64(sdl.GetTicks64()) / 1000.0

		// Adaptive frame rate: faster while interacting/animating, slower when
		// idle. LowPower picks the slowest cadence instead of forcing 60fps.
		animating := transitionPhase != "none" || pressedButtonIndex >= 0 || hoverAnimProgress > 0 || uiAnimating() ||
			trophyOverlayActive()
		frameRateCtrl.SetAnimationActive(animating)
		// A playing video is drawn from the loop, so the loop's cadence is
		// its frame rate: it keeps the active one while the clip is visible.
		frameRateCtrl.SetVideoActive(playerPhase() == "playing" && !ambientIsActive() && !ss.active)
		applyPowerMode(config)
		// The idle screens' low-power state stretches this sleep to their idle
		// cadence, but ends it the moment a press is queued (saver_power.go).
		// While someone is using the screen the cadence is a budget the frame's
		// own cost comes out of (adaptive.go Pace).
		saverFrameSleep(frameRateCtrl.Pace(time.Since(frameStart)))

		frameCount++
		// Periodic memory pressure check (every ~10 frames on TSP). The
		// evictor inside is debounced, so this is a cheap level read most of
		// the time instead of a cache clear every poll.
		if IsTrimuiDevice() && memMonitor != nil && frameCount%10 == 0 {
			EvictCachesForPressure(memMonitor)
		}
		if textureStatsLoggingEnabled && frameCount%textureStatsInterval == 0 {
			logTextureMemoryStats("runtime")
		}

		// Check alarms every second, independent of the frame cadence.
		if now := time.Now(); now.Sub(lastAlarmCheck) >= time.Second {
			lastAlarmCheck = now
			alarmCheck(config, now)
		}
	}

	// The loop is over: from here the process must be gone within the grace
	// period. A cleanup step that blocks (a child that will not exit, a server
	// Stop that waits on a connection, a flush on a dying SD card) is the other
	// half of the "app locked" report, and the watchdog is what bounds it.
	armExitDeadline(exitShutdownGrace)
	// What follows is a quit, not a panic unwinding (sdlteardown.go).
	sdlMainLoopEnded.Store(true)
	// The loop ended on a quit, so this start worked: recorded now, not in the
	// teardown, where a second stop signal, the watchdog or a power cut could
	// still turn a normal session into a failed start (bootguard.go). Off the
	// main thread: a slow card must not hold up stopping a game.
	go clearBootGuard()

	// The glass teardown: stop what owns the screen and the speaker, persist
	// state, put the CPU back the way the firmware had it, close the files and
	// caches — each row is the real step, not decoration over a sleep. Every
	// graceful exit lands here (window close, Exit button, scene trigger, stop
	// signal).
	ShowShutdown(renderer, config)
	// The fonts the card itself used close after its last frame, so a glyph
	// texture is never freed under a frame that is about to draw with it.
	for _, font := range fontCache {
		font.Close()
	}
	closeHomeUIFontSources()
	closeSplashFonts()
}

// textInputBlockedByOverlay reports whether a panel drawn above the scene owns
// the keyboard. Its letter shortcuts (B closes, R refreshes, X rescans) arrive
// as text events too, and were typed into the note, terminal or chat composer
// hidden behind it.
func textInputBlockedByOverlay() bool {
	return modalPanelBlocksShoulders() || triggerOwnedByOverlay() ||
		tb.startOpen || nc.open || wpOpen || shortcutsOpen ||
		imageViewerPath != "" || recoveryCard.open
}

// handleTextInputEvent routes one SDL text event to whatever currently owns
// typed text. It is shared by real keyboards, the on-screen keyboard and the
// Phone Companion, so a phone keystroke lands exactly where a handheld one
// would.
func handleTextInputEvent(e *sdl.TextInputEvent, config *Config) {
	if e == nil {
		return
	}
	// A ringing alarm owns the keyboard: its S/D/Space keys also arrive as
	// text, which a note or a terminal behind it would otherwise receive.
	if alarmFiringActive() {
		return
	}
	// SDL fills Text with a NUL-terminated C string padded with zero bytes.
	// Converting the whole fixed-size array produced "a\x00\x00...", which the
	// renderer truncated at the first NUL: typing looked frozen after one key
	// and backspace deleted invisible padding. GetText stops at the NUL.
	text := e.GetText()
	if text == "" {
		return
	}
	if clipSearchOpen {
		handleClipboardTextInput(text)
		return
	}
	if globalSearchOpen {
		handleGlobalSearchTextInput(text)
		return
	}
	if virtualKeyboardActive {
		// While the keyboard is open its target owns the text, so
		// scene-specific handlers must not also consume it.
		vkInsertText(text, config)
		return
	}
	if textInputBlockedByOverlay() {
		return
	}
	// The Chat composer typing inline owns the text outright, whatever target
	// the on-screen keyboard was last aimed at.
	if chatComposerTakesText(config) {
		chatComposerInsert(text)
		return
	}
	if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
		// Route text input to scene-specific handlers.
		switch config.Scenes[currentSceneIndex].Name {
		case "Alarm":
			handleAlarmTextInput(e)
		case "Terminal":
			handleTermTextInput(e)
		case "Dictionary":
			handleDictTextInput(e)
		case "Notes":
			handleNotesTextInput(e)
		}
	}
	// Chat composer and unit converter typing without the modal.
	if unitInputActive || chatInputActive || activeSceneIndex != -1 {
		vkInsertText(text, config)
	}
}
