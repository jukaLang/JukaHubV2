package main

import (
	"fmt"
	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
	"log"
	"math"
	"os"
	"runtime/debug"
	"strings"
	"time"
)

func main() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Panic: %v\n%s", r, debug.Stack())
			// Only the fact that a panic happened leaves the device, and only
			// when the user opted in. The stack above stays local.
			TelemetryCrash(appConfig)
		}
	}()

	// Secret management (set/check encrypted config values) runs before SDL so
	// it works without a display, e.g. over SSH on the handheld.
	if handled, code := handleSecretCommand(os.Args[1:]); handled {
		os.Exit(code)
	}

	// Register cleanup functions to run on exit
	defer func() {
		for _, fn := range Cleanup {
			fn()
		}
	}()

	initLogging()
	startupMarkNow("pre-sdl")

	if err := sdl.Init(sdl.INIT_VIDEO | sdl.INIT_AUDIO | sdl.INIT_JOYSTICK | sdl.INIT_GAMECONTROLLER); err != nil {
		log.Fatal("SDL init error:", err)
	}
	defer sdl.Quit()
	startupMarkNow("sdl")

	if err := ttf.Init(); err != nil {
		log.Fatal("TTF init error:", err)
	}
	defer ttf.Quit()

	if err := img.Init(img.INIT_PNG | img.INIT_JPG); err != nil {
		log.Fatal("IMG init error:", err)
	}
	// WebP box art: best-effort, because a build without libwebp must still
	// start. Every shipped SDL_image is built with it, so the error is only a
	// log line if a custom runtime drops the codec.
	if err := img.Init(img.INIT_WEBP); err != nil {
		log.Printf("WEBP init unavailable: %v", err)
	}
	// Enable text input so physical keyboard typing works in input fields
	// (both the on-screen keyboard modal and the main loop).
	sdl.StartTextInput()
	defer img.Quit()

	// Try XML config first (exported by the web editor), then fall back to JSON.
	config, configPath, err := LoadXMLConfigWithFallback("jukaconfig.json")
	if err != nil {
		log.Fatal("Failed to load config:", err)
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

	// Expose the active config for sub-packages (Discord chat, canvas, ...).
	appConfig = config

	// Tune the collector before the first heavy allocation (thumbnail cache,
	// video frames, render layers). On the TrimUI this installs a soft heap
	// limit so a 4K video + thumbnail grid cannot balloon the Go heap.
	applyGCForDevice()

	// Safe mode is decided before any background work starts, so a bad plugin,
	// update feed or network setup can be bypassed for this launch without
	// touching the user's config.
	initSafeMode()
	// The recovery chord (L+R+Start held at boot) turns safe mode on and puts the
	// previous JukaHub binary back, so a bad update is reversible without a
	// terminal or a card reader.
	initSafeboot()

	// The alarm UI renders through the scene pipeline; register its scene at
	// startup (in-memory, never written back) so the status-bar clock and the
	// Start menu can open it on every config. The loader stays pure.
	ensureAlarmScene(config)
	// The Browser has no shipped handheld scene either. It is injected here so
	// the Apps entry, Start menu and GitHub quick links work on every config.
	ensureBrowserScene(config)
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
	// Playtime stats and badges have their own screen under Tools.
	ensurePlaytimeScene(config)
	ensurePlaytimeEntry(config)
	playtimeInit(config)
	// Apply the saved "Start JukaHub at login" setting to the platform's
	// startup folder (only ever our own launcher file).
	reconcileAutoStart(config)

	SetAccentColor(resolveColor(config, "$buttonColor", sdl.Color{R: 120, G: 130, B: 255, A: 255}))

	// Reapply the persisted theme preset (if any) so the chosen theme is
	// visible immediately on launch, not just after re-selecting it.
	if presetName, ok := config.Variables.Custom["theme_preset"].(string); ok && presetName != "" {
		ApplyThemeColors(GetThemePreset(presetName))
	}

	// Default reduced-motion and low-power on handheld targets
	// (e.g. Trimui Smart Pro) for a safer out-of-box experience. Windows
	// development builds keep full animation.
	if !IsWindows() && !config.Variables.ReducedMotion && !config.Variables.LowPower {
		config.Variables.ReducedMotion = true
		config.Variables.LowPower = true
	}

	// Load persisted Recently Played and start the (best-effort) weather fetch.
	loadRecentlyPlayed()
	loadFavorites()
	initCalc()
	alarmLoad()
	notesInit()
	qsInit()
	// Accessibility settings load before the first frame so a saved text size is
	// already in effect on the splash screen, not applied later.
	a11yInit(config)
	// Push the saved mixer/backlight levels once the panel state exists, so the
	// device comes up where Quick Settings left it. Only values the user has
	// actually set are written, and it runs off the startup path because the
	// mixer probe may need to wait on a daemon.
	go qsApplyPersistedSystemState()
	// Restore the audio-reactive LEDs if the user left them on. SetLEDSync is a
	// no-op on a device with no addressable LED, and logs why.
	StartLEDSyncIfEnabled(config)
	if safeModeAllows(SafeFeatureNetwork) {
		startWeather(config)
	}

	// Tool readiness: check whether the helper tools (yt-dlp, ffmpeg, ffprobe)
	// are present and auto-install the missing ones, showing the install bar
	// while they download. This is non-fatal: offline launches stay usable and
	// retry on the next run.
	if safeModeAllows(SafeFeatureToolInstall) {
		startToolHealthCheck()
	}

	// Patch module: load persisted state and roll back any interrupted update
	// transaction so JukaHub always launches in a consistent state.
	InitPatchModule()
	// Game profiles: create the catalog folders once. The catalog itself is
	// loaded lazily when the launcher opens, so a broken profile cannot slow
	// startup or prevent booting.
	InitGameProfiles()
	// Compatibility catalog: create the folder skeleton and starter reports.
	// The catalog stays lazy; a large community pack only loads when the
	// intelligence panel opens.
	InitCompatibility()
	// Save Manager: create the snapshot/sync/resume folders. Discovery and the
	// snapshot list stay lazy so a slow card does not delay boot.
	InitSaveManager()
	// Curation Hub: create the profile/vote folders. The feed and the ROM
	// scan stay lazy so boot does not wait on the network or the card.
	InitCuration()
	// Windows Apps: create the drop folders. Runtimes themselves are not
	// installed here; the panel detects Wine and WestonPack instead.
	InitWindowsApps()

	// Show the release notes once on the first launch of a newly installed
	// build. The notes were cached before the restart, so this works offline.
	if safeModeAllows(SafeFeatureUpdates) {
		maybeShowChangelogAfterUpdate(config)
	}
	// Opt-in device census (off by default, no identifier).
	if safeModeAllows(SafeFeatureNetwork) {
		TelemetryBoot(config)
	}

	// Automatic updater: check the release feed in the background and offer a
	// newer build in Patch. It never downloads or installs without the user
	// confirming there.
	if safeModeAllows(SafeFeatureUpdates) {
		startAutoUpdateCheck(config)
	}

	// Start disk space pie chart auto-refresh (every 5 seconds).
	startDiskPieAutoRefresh()

	// Start network connectivity monitoring (tool health is already started
	// above; running it twice would double the startup probes).
	if safeModeAllows(SafeFeatureNetwork) {
		startNetworkMonitor()
	}

	// Tools panel / FTP: publish the initial IP and server state, and make sure
	// the listener never outlives the app.
	initToolsModule()
	publishSSHState()
	defer ftpSrv.Stop()
	defer sshSrv.Stop()
	// The Phone Companion starts with the app so the QR in the home header is
	// live immediately. It retries from Home if Wi-Fi comes up later.
	companionEnsureStarted()
	defer stopCompanionServer()

	// Start screensaver idle monitor.
	// Set screensaver timeout from config
	if config.Variables.ScreensaverTimeout > 0 {
		screensaverIdleTimeout = time.Duration(config.Variables.ScreensaverTimeout) * time.Second
	}
	startScreensaverMonitor()

	// Start periodic social notifications.
	// Start periodic social notifications (if enabled)
	if config.Variables.SocialNotifs {
		startSocialNotifs()
	}

	// Game tick for Pong/Snake/Tetris.
	go func() {
		ticker := time.NewTicker(time.Second / 60)
		defer ticker.Stop()
		for range ticker.C {
			tickActiveGame()
		}
	}()

	// Initialize scene index early so logging and window setup can safely
	// reference the first scene.
	currentSceneIndex = 0

	// Pick the logical canvas for this panel (the Brick/Hammer family needs the
	// compact 4:3 canvas). Scene geometry is scaled once here, after every
	// injected scene has been added, so the whole UI uses one coordinate space.
	applyDeviceUIProfile(config)

	// Optional resolution / fullscreen configuration (great for TrimuiSmartPro)
	screenW, screenH := 1280, 720
	if config.Variables.ScreenWidth > 0 {
		screenW = config.Variables.ScreenWidth
	}
	if config.Variables.ScreenHeight > 0 {
		screenH = config.Variables.ScreenHeight
	}
	// Fullscreen is intended for the handheld device. On Windows we
	// always stay windowed so the app is usable during development.
	fullscreen := config.Variables.Fullscreen && P().FullscreenDefault()

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
	window, err := sdl.CreateWindow(config.Title, sdl.WINDOWPOS_CENTERED, sdl.WINDOWPOS_CENTERED, screenWidth, screenHeight, uint32(windowFlags))
	mainWindow = window
	if err != nil {
		log.Fatal("Window creation error:", err)
	}
	defer window.Destroy()

	renderer, err := sdl.CreateRenderer(window, -1, sdl.RENDERER_ACCELERATED)
	if err != nil {
		// Fall back to a software renderer when the accelerated backend is
		// unavailable (e.g. dummy/offscreen drivers used in CI/headless).
		renderer, err = sdl.CreateRenderer(window, -1, sdl.RENDERER_SOFTWARE)
		if err != nil {
			log.Fatal("Renderer creation error:", err)
		}
	}
	defer renderer.Destroy()

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

	// Boot splash: the starfield warp in splash.go, played for whatever is left
	// of the two-second boot budget. A warm launch sees a short, complete
	// animation instead of a fixed one; a cold card with nothing left opens the
	// UI directly (see startup.go).
	startupMarkNow("window")
	bootElapsed := startupElapsed()
	allotment := splashAllotment(bootElapsed, startupTarget(), splashReserve(bootElapsed))
	if note := startupBudgetNote(bootElapsed); note != "" {
		log.Printf("startup: %s", note)
	}
	ShowSplash(renderer, config, allotment)
	startupMarkNow("splash")
	config.Variables.LoadingSpinner = false

	// Create placeholder texture for missing thumbnails
	placeholderSurface, _ := sdl.CreateRGBSurfaceWithFormat(0, 120, 90, 32, sdl.PIXELFORMAT_RGBA8888)
	if placeholderSurface != nil {
		placeholderSurface.FillRect(&sdl.Rect{X: 0, Y: 0, W: 120, H: 90}, sdl.MapRGBA(placeholderSurface.Format, 30, 35, 50, 255))
		placeholderTexture, _ = renderer.CreateTextureFromSurface(placeholderSurface)
		placeholderSurface.Free()
	}

	// Add Xbox 360 controller mapping for Trimui Smart Pro / Linux
	sdl.GameControllerAddMapping("030000005e0400008e02000014010000,X360 Controller,a:b0,b:b1,back:b6,dpdown:h0.4,dpleft:h0.8,dpright:h0.2,dpup:h0.1,guide:b8,leftshoulder:b4,leftstick:b9,lefttrigger:a2,leftx:a0,lefty:a1,rightshoulder:b5,rightstick:b10,righttrigger:a5,rightx:a3,righty:a4,start:b7,x:b2,y:b3,platform:Linux,")

	// Initialize gamepad support
	if sdl.NumJoysticks() > 0 {
		log.Printf("[GAMEPAD] Found %d joystick(s)", sdl.NumJoysticks())
		for i := 0; i < sdl.NumJoysticks(); i++ {
			if sdl.IsGameController(i) {
				// go-sdl2 has no GameControllerFromIndex: the name/ID are
				// queried per index, and the handle comes from Open.
				log.Printf("[GAMEPAD] Controller %d: %s (GUID: %s)", i,
					sdl.GameControllerNameForIndex(i),
					sdl.JoystickGetGUIDString(sdl.JoystickGetDeviceGUID(i)))
			} else {
				log.Printf("[GAMEPAD] Joystick %d: %s (not a game controller)", i, sdl.JoystickNameForIndex(i))
			}
		}
		if c := sdl.GameControllerOpen(0); c != nil {
			defer c.Close()
			log.Printf("[GAMEPAD] Opened controller 0: %s", c.Name())
		}
	} else {
		log.Printf("[GAMEPAD] No joysticks/gamepads detected")
	}

	initKeyboard()

	thumbnailDownloadCh = make(chan string, 256)
	// More than one worker: prefetching a results grid is latency-bound, and a
	// single worker downloaded exactly one thumbnail per network round trip.
	for i := 0; i < thumbnailWorkerCount(); i++ {
		go thumbnailPrefetchWorker()
	}

	currentSceneIndex = 0
	selectedButtonIndex = findFirstSelectableElement(config.Scenes[currentSceneIndex])
	focusEngine.SetGraph(config.Scenes[currentSceneIndex].Name, focusEngine.BuildGraph(config.Scenes[currentSceneIndex]))
	focusEngine.SyncSelected()

	// Start config file watcher for hot-reload
	StartConfigWatcher("jukaconfig.json", func(newCfg *Config) {
		// Apply the reloaded config: merge user settings, refresh theme,
		// and update the scene list. The render loop will pick up the new
		// config on the next frame.
		mergeUserConfig(newCfg, loadUserConfig())
		applyDeviceUIProfile(newCfg)
		if presetName, ok := newCfg.Variables.Custom["theme_preset"].(string); ok && presetName != "" {
			ApplyThemeColors(GetThemePreset(presetName))
		}
		SetAccentColor(resolveColor(newCfg, "$buttonColor", sdl.Color{R: 120, G: 130, B: 255, A: 255}))
		// Update the global config pointer atomically
		appConfig = newCfg
		config = newCfg
		showToast("Config reloaded", ColorToastSuccess)
	})
	defer StopConfigWatcher()

	// Wi-Fi designer: the static web builder pushes jukaconfig.json here and the
	// watcher above reloads it in place. Local-only, guarded by a pairing code.
	if liveBuilder := startLiveBuilderIfEnabled(); liveBuilder != nil {
		defer liveBuilder.Stop()
	}

	// Hardware volume buttons: a long hold skips a track even while the screen
	// is off or the player is in the background. The OS still owns the short
	// press, so nothing about the volume itself changes.
	startVolumeButtonWatcher()

	// Adaptive frame rate and memory monitoring for TSP ARM64 devices
	frameRateCtrl := NewFrameRateController()
	memMonitor := NewMemoryMonitor()
	defer memMonitor.Stop()
	LogDeviceDiagnostics()

	running := true
	startupReported := false
	// Alarms are checked on the clock, not on frameCount: the adaptive frame
	// rate and the darked ambient panel both run far below 60 fps, which would
	// make a frame-counted check ring late.
	lastAlarmCheck := time.Now()
	for running {
		// Keep the home-header pairing QR live: start the companion when Wi-Fi
		// appears and rebuild the code if DHCP moved the address.
		companionEnsureStarted()
		// Phone Companion actions arrive on an HTTP goroutine. They are queued
		// and applied here, on the main loop, so a phone keypress takes the same
		// path as the handheld's own input.
		drainCompanionQueue(config)
		// Exit request (home Exit button or config "exit" trigger): play the
		// farewell sequence, then shut down through the deferred cleanup below
		// (persist favorites, free textures/fonts, sdl.Quit).
		if exitRequested && !goodbyePlayed {
			goodbyePlayed = true
			ShowGoodbye(renderer, config)
			running = false
			continue
		}
		for event := sdl.PollEvent(); event != nil; event = sdl.PollEvent() {
			applyButtonSwap(config, event)
			switch e := event.(type) {
			case *sdl.QuitEvent:
				running = false
			case *sdl.WindowEvent:
				if e.Event == sdl.WINDOWEVENT_SIZE_CHANGED {
					w, h := mainWindow.GetSize()
					updateWindowScale(w, h)
				}
			case *sdl.KeyboardEvent:
				if e.Type == sdl.KEYUP {
					// Only the audio player cares: letting go of a volume key ends a
					// hold, so the next tap changes the volume instead of looking
					// like one long press that already skipped a track.
					if musicSceneActive() {
						musicHandleVolumeKey(e)
					}
					continue
				}
				if e.Type == sdl.KEYDOWN {
					frameRateCtrl.NotifyInput()
					screensaverNotifyInput()
					// The dock screen owns every key while it is up, including the
					// first press that wakes a darked panel.
					if ambientHandleKey(e, config) {
						continue
					}
					if launchFailure.open {
						handleLaunchFailureKey(e)
						continue
					}
					// The first-run tour is the topmost modal: while it is open it
					// owns every key, including escape and the usual global chords.
					if onboarding.active {
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
					if clipSearchOpen {
						handleClipboardInput(e)
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
					// IPStream library is modal and consumes all keys while open.
					if ipPanelOpen && handleIPPanelKey(e, config) {
						continue
					}
					// Process list is modal and consumes all keys while open.
					if processPanel.open && handleProcessKey(e, config) {
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
					// Power tools are above the app chrome while open.
					if powerTools.open && handlePowerToolsKey(e, config) {
						continue
					}
					// RetroAchievements is modal. While the keyboard is editing a
					// username or API key the keyboard owns the keys instead.
					if raPanel.open && !virtualKeyboardActive && handleRetroAchievementsKey(e, config) {
						continue
					}
					// Watch Party is modal too, but the on-screen keyboard owns the
					// keys while it is typing an invite or a link.
					if partyUI.open && !virtualKeyboardActive && partyHandleKey(e, config) {
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
					if musicSettings.open && handleMusicSettingsKey(e, config) {
						continue
					}
					if a11y.open && handleAccessibilityKey(e, config) {
						continue
					}
					// PS Remote Play installer/launcher is modal too.
					if chiakiPanel.open && handleChiakiKey(e) {
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
					// The stick calibration wizard is modal while it runs.
					if calibrationPanel.open && handleStickCalibrationKey(e, config) {
						continue
					}
					// Smart Home is modal too.
					if smartHomePanel.open && handleSmartHomeKey(e, config) {
						continue
					}
					// Tools is modal and consumes all keys while open.
					if toolsPanel.open && handleToolsKey(e, config) {
						continue
					}
					// LAN play is modal over the library. The address field is typed
					// on the on-screen keyboard, which is handled earlier, so only
					// route here while no text field is capturing keys.
					if netplayPanel.open && !virtualKeyboardActive && handleNetplayKey(e, config) {
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
					focusEngine.SyncSelected()
					if imageViewerPath != "" {
						handleImageViewerInput(e)
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
						// The app menu and notification center are modal:
						// while one is open it owns the D-pad and Confirm/Back keys.
						if handleChromeOverlayKey(e, config) {
							continue
						}
						// The clock popover is shared by every scene, so Escape closes
						// it before the scene's own input runs.
						if homeCalendarOpen && (e.Keysym.Sym == sdl.K_ESCAPE || e.Keysym.Sym == sdl.K_b) {
							homeCalendarOpen = false
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
						// Top-bar chrome (back pill, status clock) is focusable on
						// non-home scenes; while it holds focus it owns the keys so
						// the element dispatch cannot steal the focus back.
						if handleChromeKey(e, config) {
							continue
						}
						// Video control shortcuts (always active when video is playing)
						HandleVideoKeyInput(e)
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
							case sdl.K_ESCAPE:
								handleSearchResultsAction(ActionBack, config)
							}
						} else if curElem.Type == "dynamiclist" {
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
							presets := ListThemePresets()
							switch e.Keysym.Sym {
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
							case sdl.K_ESCAPE:
								moveSelection(config, ActionNavigateUp)
							}
						} else if curElem.Type == "toggle" {
							switch e.Keysym.Sym {
							case sdl.K_RETURN, sdl.K_SPACE, sdl.K_LEFT, sdl.K_RIGHT:
								toggleVariable(config, curElem)
							case sdl.K_ESCAPE:
								moveSelection(config, ActionNavigateUp)
							}
						} else if curElem.Type == "favorites" {
							handleFavoritesInput(e, config)
						} else if curElem.Type == "unitconverter" {
							handleUnitInput(e, config)
						} else if curElem.Type == "chat" {
							// Pressing A/Enter/Space opens the virtual keyboard
							// with chat routing (works for both controller and keyboard).
							if e.Keysym.Sym == sdl.K_RETURN || e.Keysym.Sym == sdl.K_SPACE || e.Keysym.Sym == sdl.K_a {
								vkOpenChat()
							} else {
								handleChatInput(e, config)
							}
						} else if curElem.Type == "shortslist" {
							handleShortsInput(e, config)
						} else if curElem.Type == "canvas" {
							handleCanvasInput(e, config)
						} else if curElem.Type == "textbrowser" {
							if e.Keysym.Sym == sdl.K_RETURN && textBrowserElementSource(curElem) == textBrowserSourceWeb {
								handleTextBrowserAddressInput(renderer, config, curElem)
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
						} else if socialNotifOpen {
							handleSocialNotifInput(e)
						} else if wpOpen {
							handleWallpaperInput(e, config)
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
									moveSelection(config, map[sdl.Keycode]Action{sdl.K_UP: ActionNavigateUp, sdl.K_DOWN: ActionNavigateDown}[e.Keysym.Sym])
								}
							case sdl.K_LEFT, sdl.K_RIGHT:
								if homeLayoutActive && curElem.Type == "button" && curElem.Style == "tile" {
									moveHomeSelection(config, map[sdl.Keycode]int{
										sdl.K_LEFT: -1, sdl.K_RIGHT: 1,
									}[e.Keysym.Sym], 0)
								} else {
									moveSelection(config, map[sdl.Keycode]Action{sdl.K_LEFT: ActionNavigateLeft, sdl.K_RIGHT: ActionNavigateRight}[e.Keysym.Sym])
								}
							case sdl.K_RETURN, sdl.K_SPACE:
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
									goBackScene(config)
								}
							}
						}
						focusEngine.SyncSelected()
						// Scene switching keys — not in the terminal, where q/e are
						// ordinary characters in a command.
						if !inTerminal {
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
						} // Quick Settings (Ctrl+Q).
						if e.Keysym.Sym == sdl.K_q && (e.Keysym.Mod&sdl.KMOD_CTRL) != 0 {
							qsToggleOpen()
						}
						// Global shortcuts (F1 = help, F2 = global search, F12 = screenshot).
						if e.Keysym.Sym == sdl.K_F1 {
							shortcutsOpen = !shortcutsOpen
							globalSearchOpen = false
							continue
						}
						if e.Keysym.Sym == sdl.K_F2 {
							globalSearchOpen = !globalSearchOpen
							shortcutsOpen = false
							continue
						}
						if e.Keysym.Sym == sdl.K_F12 {
							captureScreenshot(renderer)
							continue
						}
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
							config.Variables.Fullscreen = !config.Variables.Fullscreen
							if mainWindow != nil {
								if config.Variables.Fullscreen {
									mainWindow.SetFullscreen(sdl.WINDOW_FULLSCREEN_DESKTOP)
								} else {
									mainWindow.SetFullscreen(0)
								}
							}
						}
					}
					focusEngine.SyncSelected()
				}
			case *sdl.TextInputEvent:
				frameRateCtrl.NotifyInput()
				screensaverNotifyInput()
				handleTextInputEvent(e, config)
			case *sdl.MouseMotionEvent:
				frameRateCtrl.NotifyInput()
				screensaverNotifyInput()
				mouseX, mouseY = physicalToLogical(int32(e.X), int32(e.Y))
				// Hovering a home card focuses it so mouse users get the same
				// highlight as D-pad navigation. A deliberate click remembers
				// the card for restore-on-return (see handleHomeMouseClick).
				if homeLayoutActive && homeLayout != nil {
					homeLayout.FocusAt(mouseX, mouseY)
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
			case *sdl.MouseWheelEvent:
				if powerTools.open && handlePowerToolsWheel(e.Y) {
					continue
				}
				if communityPanel.open && handleCommunityWheel(e.Y) {
					continue
				}
				if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
					config.Scenes[currentSceneIndex].Name == "Terminal" {
					handleTermWheel(e.Y)
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
							elemW := getElementWidth(elem, 1160)
							elemH := getElementHeight(elem, 500)
							if mouseX >= elem.X && mouseX <= elem.X+elemW && mouseY >= elem.Y && mouseY <= elem.Y+elemH {
								packagesMutex.Lock()
								n := len(packagesList)
								packagesMutex.Unlock()
								step := 3
								if e.Y < 0 {
									packagesFocusIndex -= step
								} else {
									packagesFocusIndex += step
								}
								if packagesFocusIndex < 0 {
									packagesFocusIndex = 0
								}
								if packagesFocusIndex >= n {
									packagesFocusIndex = n - 1
								}
								break
							}
						}
					}
				}
			case *sdl.MouseButtonEvent:
				frameRateCtrl.NotifyInput()
				screensaverNotifyInput()
				if ambientIsActive() {
					// Taps on the dock screen act on its chips; everything else
					// only wakes it, so a stray tap cannot press the UI behind.
					if e.Button == sdl.BUTTON_LEFT && e.Type == sdl.MOUSEBUTTONDOWN {
						mx, my := physicalToLogical(int32(e.X), int32(e.Y))
						ambientHandleClick(mx, my)
					}
					continue
				}
				if e.Button == sdl.BUTTON_LEFT {
					mx, my := physicalToLogical(int32(e.X), int32(e.Y))
					if e.Type == sdl.MOUSEBUTTONDOWN {
						if launchFailure.open {
							handleLaunchFailureMouse()
							break
						}
						if handleOnboardingMouse(mx, my, config) {
							break
						}
						if virtualKeyboardActive {
							kmx, kmy := physicalToLogical(int32(e.X), int32(e.Y))
							if vkCloseHit(kmx, kmy) {
								vkCloseKeyboard()
								break
							}
							if row, col, ok := vkHitTest(kmx, kmy); ok {
								keyboardPosY, keyboardPosX = row, col
								key := vkFocusedKey()
								vkActivateFocused(config)
								if vkIsActionKey(key) {
									vkCloseKeyboard()
								}
								break
							}
							// A click on the dimmed content behind the panel dismisses it.
							if vkOutsidePanel(kmx, kmy) {
								vkCloseKeyboard()
								break
							}
							break
						}

						// Dismiss toast on click
						if toastRect.W > 0 && toastRect.H > 0 && pointInRect(mx, my, toastRect) {
							currentToast = ToastState{}
							toastRect = sdl.Rect{}
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
						// Watch Party panel is modal the same way.
						if partyUI.open {
							partyHandleMouse(mx, my, config)
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
						// Tools panel is modal: a click selects a row, a click outside
						// closes it, and nothing reaches the scene underneath.
						if toolsPanel.open {
							handleToolsMouse(mx, my, config)
							break
						}
						// LAN play is modal over the game library.
						if netplayPanel.open {
							handleNetplayMouse(mx, my)
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

						// The alarm screen draws its own Back pill and rows, so it owns
						// the clicks on its surface.
						if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
							config.Scenes[currentSceneIndex].Name == "Alarm" {
							handleAlarmMouseClick(mx, my, config)
							break
						}
						// The terminal owns every click on its screen: its Back and
						// Keyboard controls are drawn by the terminal renderer, not by
						// config elements.
						if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
							config.Scenes[currentSceneIndex].Name == "Terminal" {
							handleTermMouseClick(mx, my, config)
							break
						}
						// Check if clicking on seek bar for dragging
						if pointInRect(mx, my, videoControlRects.progress) {
							isDraggingSeekBar = true
							seekBarDragRect = videoControlRects.progress
							fraction := float64(mx-videoControlRects.progress.X) / float64(videoControlRects.progress.W)
							SeekVideo(fraction)
						}
						// Image viewer overlay: a click anywhere closes it, matching
						// the tap-to-open / tap-to-close gesture.
						if imageViewerPath != "" {
							closeImageViewer()
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
						// Home cards: select + activate on click.
						if homeLayoutActive && handleHomeMouseClick(mx, my, config) {
							break
						}
						// Top-bar chrome buttons: the back pill and the status clock
						// activate on click/touch like any focused button would.
						if handleChromeBarClick(mx, my, config) {
							break
						}
						// Playtime has no config elements; its RA touch target lives
						// in the header.
						if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
							config.Scenes[currentSceneIndex].Name == playtimeSceneName &&
							handlePlaytimeMouse(mx, my) {
							break
						}
						// Chat image previews open full screen; clicking again closes.
						if currentSceneHasChat(config) && handleChatImageClick(mx, my) {
							break
						}
						// Chat link pills open YouTube/media links in the player.
						if currentSceneHasChat(config) && handleChatLinkClick(mx, my, config) {
							break
						}
						// Chat header: the display-name pill renames the local chat user.
						if chatNameRect.W > 0 && mx >= chatNameRect.X && mx <= chatNameRect.X+chatNameRect.W &&
							my >= chatNameRect.Y && my <= chatNameRect.Y+chatNameRect.H {
							vkOpenName()
							break
						}
						// Chat input area: clicking the text field opens the virtual keyboard.
						if chatInputRect.W > 0 && mx >= chatInputRect.X && mx <= chatInputRect.X+chatInputRect.W &&
							my >= chatInputRect.Y && my <= chatInputRect.Y+chatInputRect.H {
							vkOpenChat()
							break
						}
						// Unit converter input area.
						if unitInputRect.W > 0 && mx >= unitInputRect.X && mx <= unitInputRect.X+unitInputRect.W &&
							my >= unitInputRect.Y && my <= unitInputRect.Y+unitInputRect.H {
							vkOpenUnit()
							break
						}
						// Chat composer: Send button.
						if chatSendBtnRect.W > 0 && mx >= chatSendBtnRect.X && mx <= chatSendBtnRect.X+chatSendBtnRect.W &&
							my >= chatSendBtnRect.Y && my <= chatSendBtnRect.Y+chatSendBtnRect.H {
							if chatInputText != "" {
								sendChatMessage(chatDisplayName(), chatInputText)
								chatInputText = ""
							}
							break
						}
						// Chat composer: "Ask AI" pill (controller/keyboard use Y).
						if chatAskAIBtnRect.W > 0 && mx >= chatAskAIBtnRect.X && mx <= chatAskAIBtnRect.X+chatAskAIBtnRect.W &&
							my >= chatAskAIBtnRect.Y && my <= chatAskAIBtnRect.Y+chatAskAIBtnRect.H {
							if strings.TrimSpace(chatInputText) != "" {
								sendGroqChatMessage(chatInputText)
								chatInputText = ""
							}
							break
						}
						// Chat composer: Attach image pill.
						if chatAttachBtnRect.W > 0 && mx >= chatAttachBtnRect.X && mx <= chatAttachBtnRect.X+chatAttachBtnRect.W &&
							my >= chatAttachBtnRect.Y && my <= chatAttachBtnRect.Y+chatAttachBtnRect.H {
							config.Variables.Custom["chat_attach_image"] = "1"
							showToast("Browse to attach an image to your message", ToastInfo())
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
						// Patch's list and modals are drawn by the scene renderer, so
						// they need their own click routing (rows used to be keyboard
						// only on a touch-capable device).
						if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
							config.Scenes[currentSceneIndex].Name == "Patch" {
							if handlePatchMouse(mx, my, config) {
								break
							}
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
									if idx := themeGalleryCardAt(elem, mx, my); idx >= 0 {
										themeGalleryIndex = idx
										selectedButtonIndex = i
										focusEngine.SetByElementIndex(i)
										presets := ListThemePresets()
										if idx < len(presets) {
											ApplyThemePreset(config, presets[idx])
										}
									}
								} else if elem.Type == "button" {
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
									// Grid hit detection (account for smooth scroll offset)
									cols, rows := gridColsRows(elem)
									elemWidth := getElementWidth(elem, 1080)
									elemHeight := getElementHeight(elem, 500)
									cellWidth := (elemWidth - 30) / cols
									cellHeight := (elemHeight - 30) / rows
									for idx := range videos {
										if idx >= int(cols*rows) {
											break
										}
										col := int32(idx) % cols
										row := int32(idx) / cols
										xPos := elem.X + col*(cellWidth+10)
										yPos := elem.Y + row*(cellHeight+10) - scrollY
										if mx >= xPos && mx <= xPos+cellWidth && my >= yPos && my <= yPos+cellHeight {
											selectedButtonIndex = i
											focusEngine.SetByElementIndex(i)
											focusedResultIndex = idx
											if idx < len(videos) {
												playVideoInfo(config, videos[idx])
											}
											break
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
										for idx, vid := range vids {
											cell := shortsGridCellRect(elem, layout, idx, shortsScrollY)
											if mx >= cell.X && mx <= cell.X+cell.W && my >= elem.Y && my <= elem.Y+elemH && my >= cell.Y && my <= cell.Y+cell.H {
												currentShortIdx = idx
												selectedButtonIndex = i
												focusEngine.SetByElementIndex(i)
												playVideoInfo(config, vid)
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
										addressRect, goRect := textBrowserToolbarRects(elem)
										if mx >= addressRect.X && mx <= addressRect.X+addressRect.W && my >= addressRect.Y && my <= addressRect.Y+addressRect.H {
											handleTextBrowserAddressInput(renderer, config, elem)
										} else if mx >= goRect.X && mx <= goRect.X+goRect.W && my >= goRect.Y && my <= goRect.Y+goRect.H {
											loadTextBrowserURL(config, elem, textBrowserAddress(config))
										}
									}
								}
							}
						}
					} else if e.Type == sdl.MOUSEBUTTONUP {
						isDraggingSeekBar = false
					}
				}
				focusEngine.SyncSelected()
			case *sdl.ControllerButtonEvent:
				// Controller input counts as activity too, otherwise a user
				// navigating with the D-pad sat at the idle cadence and every
				// press felt late (the frame rate never woke up).
				frameRateCtrl.NotifyInput()
				screensaverNotifyInput()
				// Accessibility remapping rewrites the physical button once, here,
				// before the overlay router or the scene sees the event.
				a11yRemapEvent(e)
				if e.Type == sdl.CONTROLLERBUTTONDOWN && ambientHandleController(e) {
					continue
				}
				if e.Type == sdl.CONTROLLERBUTTONDOWN {
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
					focusEngine.SyncSelected()
					if virtualKeyboardActive {

						switch e.Button {

						case sdl.CONTROLLER_BUTTON_DPAD_UP:

							vkMoveFocus(0, -1)

						case sdl.CONTROLLER_BUTTON_DPAD_DOWN:

							vkMoveFocus(0, 1)

						case sdl.CONTROLLER_BUTTON_DPAD_LEFT:

							vkMoveFocus(-1, 0)

						case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:

							vkMoveFocus(1, 0)

						case sdl.CONTROLLER_BUTTON_A:

							vkActivateFocused(config)

						case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_START:

							vkCloseKeyboard()

						case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:

							toggleKeyboardCase()

						case sdl.CONTROLLER_BUTTON_Y:
							if vkTarget == vkTargetChat {
								vkSubmit(config, true)
							} else {
								vkSubmit(config, false)
							}

						}

					} else if activeSceneIndex != -1 {
						// Input field active - handled by virtualKeyboardActive
					} else {
						// Home scene uses the dedicated focus graph + activation.
						if homeLayoutActive && handleHomeController(e, config) {
							continue
						}
						// Patch scene owns its list + modals (no config elements).
						if config.Scenes[currentSceneIndex].Name == "Patch" {
							if handlePatchController(e, config) {
								continue
							}
						}
						// Scene-specific controller routing.
						if handleSceneController(e, config) {
							continue
						}
						// Video control shortcuts (always active when video is playing).
						// Remember whether the press was consumed by a player: B there
						// stops playback instead of also leaving the scene.
						videoWasPlaying := snapshotVideoState().phase != "idle"
						HandleVideoControllerInput(e)
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
							presets := ListThemePresets()
							switch e.Button {
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
							case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
								goBackScene(config)
							}
						} else if curElem.Type == "toggle" {
							switch e.Button {
							case sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
								toggleVariable(config, curElem)
							case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
								goBackScene(config)
							}
						} else if curElem.Type == "favorites" {
							items := getCurrentFavorites()
							n := len(items)
							switch e.Button {
							case sdl.CONTROLLER_BUTTON_DPAD_UP:
								if favoritesFocusIndex > 0 {
									favoritesFocusIndex--
								}
							case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
								if favoritesFocusIndex < n-1 {
									favoritesFocusIndex++
								}
							case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
								if favoritesCurrentTab > 0 {
									favoritesCurrentTab--
									favoritesFocusIndex = 0
								}
							case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
								if favoritesCurrentTab < 3 {
									favoritesCurrentTab++
									favoritesFocusIndex = 0
								}
							case sdl.CONTROLLER_BUTTON_A:
								if favoritesFocusIndex >= 0 && favoritesFocusIndex < n {
									items[favoritesFocusIndex].Play(config)
								}
							case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
								goBackScene(config)
							}
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
							switch e.Button {
							case sdl.CONTROLLER_BUTTON_DPAD_UP:
								if packagesFocusIndex > 0 {
									packagesFocusIndex--
								}
							case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
								packagesMutex.Lock()
								pCount := len(packagesList)
								packagesMutex.Unlock()
								if packagesFocusIndex < pCount-1 {
									packagesFocusIndex++
								}
							case sdl.CONTROLLER_BUTTON_A:
								packagesMutex.Lock()
								if packagesFocusIndex >= 0 && packagesFocusIndex < len(packagesList) {
									pkg := packagesList[packagesFocusIndex]
									packagesMutex.Unlock()
									go downloadAndInstallPackage(config, pkg)
								} else {
									packagesMutex.Unlock()
								}
							case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
								goBackScene(config)
							}
						} else if curElem.Type == "chat" {
							switch e.Button {
							case sdl.CONTROLLER_BUTTON_DPAD_UP:
								chatScrollOffset++
							case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
								if chatScrollOffset > 0 {
									chatScrollOffset--
								}
							case sdl.CONTROLLER_BUTTON_A:
								// A opens the virtual keyboard to type a message.
								vkOpenChat()
							case sdl.CONTROLLER_BUTTON_Y:
								if strings.TrimSpace(chatInputText) != "" {
									sendGroqChatMessage(chatInputText)
									chatInputText = ""
								}
							case sdl.CONTROLLER_BUTTON_X:
								// X renames the local chat user.
								vkOpenName()
							case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
								goBackScene(config)
							}
						} else {
							// Normal button/input navigation
							switch e.Button {
							case sdl.CONTROLLER_BUTTON_DPAD_UP:
								moveSelection(config, ActionNavigateUp)
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
							// Scene switching (L1/R1 or L2/R2)
							switch e.Button {
							case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
								changeScene(config, -1)
							case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
								changeScene(config, 1)
							}
						}
						focusEngine.SyncSelected()
					}
				}
			case *sdl.ControllerAxisEvent:
				frameRateCtrl.NotifyInput()
				screensaverNotifyInput()
				handleControllerAxis(e)
			}
		}

		drainCustom(config)

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
			// Clamp scroll position based on current scene textbrowser elements
			if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
				for _, elem := range config.Scenes[currentSceneIndex].Elements {
					if elem.Type == "textbrowser" {
						elemH := getElementHeight(elem, 480)
						headerH := int32(28)
						contentH := elemH - headerH - 8
						lineH := int32(22)
						maxLines := int((contentH) / lineH)
						totalLines := 0
						if v, ok := config.Variables.Custom[elem.Variable].(string); ok {
							totalLines = len(strings.Split(v, "\n"))
						}
						scrollMax := int32(0)
						if totalLines > maxLines {
							scrollMax = int32(totalLines) - int32(maxLines)
						}
						if textBrowserScrollY < 0 {
							textBrowserScrollY = 0
						}
						if textBrowserScrollY > scrollMax {
							textBrowserScrollY = scrollMax
						}
						break
					}
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
		if config.Variables.ReducedMotion {
			hoverAnimProgress = targetHover
		} else {
			hoverAnimProgress = lerp(hoverAnimProgress, targetHover, 0.2)
			if hoverAnimProgress > targetHover-0.01 && hoverAnimProgress < targetHover+0.01 {
				hoverAnimProgress = targetHover
			}
		}

		// Clear press feedback after duration (instantly in reduced-motion mode).
		if pressedButtonIndex >= 0 {
			if config.Variables.ReducedMotion || sdl.GetTicks64()-pressStartTime > pressDuration {
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
			sdl.Delay(200)
			frameCount++
			if now := time.Now(); now.Sub(lastAlarmCheck) >= time.Second {
				lastAlarmCheck = now
				alarmCheck(now)
			}
			continue
		}

		// Contextual widgets: battery saver and time-of-day theme, checked once a
		// second before the frame is drawn so a theme switch lands immediately.
		widgetsTick(config)
		playtimeTick(config)
		// Dock and ambient screen: clock, weather, now-playing and photos when the
		// device is charging or idle (see ambient.go). Runs here, on the event
		// thread, because its entry check reads modal flags.
		ambientAutoTick(config, time.Now())
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

		// Scene transition effects
		if config.Variables.ReducedMotion && (transitionPhase == "fade-out" || transitionPhase == "fade-in") {
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
			alive := !uiSceneFadeDone(transitionPhase, elapsed)
			if transitionPhase == "fade-out" {
				if !alive {
					sceneFadeAlpha = 255
					completeSceneTransition(config)
				} else {
					sceneFadeAlpha = uiSceneScrimAlpha(transitionPhase, elapsed)
				}
			} else {
				if !alive {
					sceneFadeAlpha = 0
					transitionPhase = "none"
				} else {
					sceneFadeAlpha = uiSceneScrimAlpha(transitionPhase, elapsed)
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
		if config.Variables.LoadingSpinner || transitionPhase != "none" {
			renderLoadingCard(renderer, config)
		}

		// SDL text input stays enabled for the whole session: the on-screen
		// keyboard, the chat composer and the unit converter all depend on
		// text-input events, and the keyboard target decides where each
		// character lands. Toggling it per frame used to swallow keystrokes.
		if !virtualKeyboardActive {
			chatFocused := currentSceneIndex >= 0 && selectedButtonIndex >= 0 &&
				selectedButtonIndex < len(config.Scenes[currentSceneIndex].Elements) &&
				config.Scenes[currentSceneIndex].Elements[selectedButtonIndex].Type == "chat" &&
				// The terminal's prompt row is not the chat composer, even though
				// its scene config still contains a chat element.
				!isTerminalScene()
			chatInputActive = chatFocused
		}

		// Patch scene: drain worker events and apply hold-to-confirm progress.
		PatchSceneFrame()
		// Watch Party polling and remote playhead correction. It never blocks:
		// the HTTP request runs on its own goroutine.
		partyTick(config)
		if appRestartRequested.Load() {
			// A staged update was handed to the swap helper; quit so it can
			// replace the executable and relaunch JukaHub.
			running = false
			continue
		}

		// A home-launcher press that downloaded the PS5 beta launches it as soon
		// as the install reports success. Polled here, on the event thread.
		ps5PollPendingLaunch()
		ps4PollPendingLaunch()

		// Offer a newly discovered release once.
		maybeOfferUpdate()

		// Quick Settings overlay.
		if qs.open {
			renderQuickSettings(renderer, config)
		}

		// Watch Party overlay (LAN shared queue + synced playback).
		if partyUI.open {
			partyRender(renderer, config)
		}

		// Tools overlay (IP address + FTP server).
		if toolsPanel.open {
			renderToolsPanel(renderer, config)
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

		// Developer / power-user tools overlay.
		if powerTools.open {
			renderPowerToolsPanel(renderer, config)
		}

		// Process list overlay (inspect and kill processes).
		if processPanel.open {
			renderProcessPanel(renderer, config)
		}

		// IPStream library overlay.
		if ipPanelOpen {
			renderIPPanel(renderer, config)
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

		// Windows Apps overlay (Wine + WestonPack launcher).
		if winAppsPanel.open {
			renderWindowsAppsPanel(renderer, config)
		}

		// PS Remote Play overlay (install and launch the TrimUI Chiaki-ng port).
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

		// RetroAchievements overlay (recent unlocks + game progress).
		if raPanel.open {
			renderRetroAchievementsPanel(renderer, config)
		}

		// Wallpaper picker overlay.
		if wpOpen {
			renderWallpaperPicker(renderer, config)
		}

		// Social notifications panel.
		if socialNotifOpen {
			renderSocialNotifPanel(renderer, config)
		}

		// Phone Companion pairing card.
		if companion.open {
			renderCompanionOverlay(renderer, config)
		}

		// Update prompt is the topmost modal.
		if updatePrompt.open {
			renderUpdatePrompt(renderer, config)
		}

		// Shortcut hint at bottom (hidden when overlays are open).
		if !shortcutsOpen && !clipSearchOpen && !globalSearchOpen && !qs.open && !updatePrompt.open && !companion.open && !smartHomePanel.open && !calibrationPanel.open && !ambientIsActive() {
			renderShortcutHint(renderer, config)
		}

		// Overlay renderers (rendered on top of everything).
		if shortcutsOpen {
			renderShortcutsOverlay(renderer, config)
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

		renderToast(renderer, config)
		renderInstallBar(renderer, config)

		// Companion screen view: capture on the render thread only when the
		// phone asked for a frame (see companion_screen.go).
		captureCompanionScreen(renderer)

		renderer.Present()
		if !startupReported {
			startupReported = true
			startupMarkNow("first-frame")
			startupLog()
			// Bench mode measures boot and stops before the event loop settles
			// into idle work, so CI can assert on the number.
			if startupBenchEnabled() {
				running = false
				continue
			}
		}
		animTime = float64(sdl.GetTicks64()) / 1000.0

		// Adaptive frame rate: faster while interacting/animating, slower when
		// idle. LowPower picks the slowest cadence instead of forcing 60fps.
		animating := transitionPhase != "none" || pressedButtonIndex >= 0 || hoverAnimProgress > 0 || uiAnimating()
		frameRateCtrl.SetAnimationActive(animating)
		frameRateCtrl.SetLowPower(config.Variables.LowPower)
		sdl.Delay(frameRateCtrl.Delay())

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
			alarmCheck(now)
		}
	}

	// Persist favorites and resume positions on every graceful quit path
	// (window close, exit button, scene-exit trigger).
	// Blank the LEDs first so they do not stay lit on the last audio frame.
	_ = SetLEDSync(false)
	playtimeFlush(config)
	partyHistoryFlush()
	saveFavorites()
	ClearTextCache()
	DestroyVKStaticCache()
	clearThumbnailCache()
	DestroyVKVideoOverlayCache()
	DestroyVKChatCache()
	clearHomeTextCache()
	for _, font := range fontCache {
		font.Close()
	}
}

// handleTextInputEvent routes one SDL text event to whatever currently owns
// typed text. It is shared by real keyboards, the on-screen keyboard and the
// Phone Companion, so a phone keystroke lands exactly where a handheld one
// would.
func handleTextInputEvent(e *sdl.TextInputEvent, config *Config) {
	if e == nil {
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
