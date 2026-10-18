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

	if err := sdl.Init(sdl.INIT_VIDEO | sdl.INIT_AUDIO | sdl.INIT_JOYSTICK | sdl.INIT_GAMECONTROLLER); err != nil {
		log.Fatal("SDL init error:", err)
	}
	defer sdl.Quit()

	if err := ttf.Init(); err != nil {
		log.Fatal("TTF init error:", err)
	}
	defer ttf.Quit()

	if err := img.Init(img.INIT_PNG | img.INIT_JPG); err != nil {
		log.Fatal("IMG init error:", err)
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

	SetAccentColor(resolveColor(config, "$buttonColor", sdl.Color{R: 120, G: 130, B: 255, A: 255}))

	// Reapply the persisted theme preset (if any) so the chosen theme is
	// visible immediately on launch, not just after re-selecting it.
	if presetName, ok := config.Variables.Custom["theme_preset"].(string); ok && presetName != "" {
		ApplyThemeColors(GetThemePreset(presetName))
	}

	// Defaults for optional settings
	if config.Variables.WeatherUnit == "" {
		config.Variables.WeatherUnit = "C"
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
	startWeather(config)

	// Tool readiness: check whether the helper tools (yt-dlp, ffmpeg, ffprobe)
	// are present and auto-install the missing ones, showing the install bar
	// while they download. This is non-fatal: offline launches stay usable and
	// retry on the next run.
	startToolHealthCheck()

	// Patch module: load persisted state and roll back any interrupted update
	// transaction so JukaHub always launches in a consistent state.
	InitPatchModule()

	// Automatic updater: check the release feed in the background and offer a
	// newer build in Patch. It never downloads or installs without the user
	// confirming there.
	startAutoUpdateCheck(config)

	// Start disk space pie chart auto-refresh (every 5 seconds).
	startDiskPieAutoRefresh()

	// Start network connectivity monitoring (tool health is already started
	// above; running it twice would double the startup probes).
	startNetworkMonitor()

	// Tools panel / FTP: publish the initial IP and server state, and make sure
	// the listener never outlives the app.
	initToolsModule()
	defer ftpSrv.Stop()

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

	// Render into a fixed logical 1280x720 coordinate system and let SDL
	// letterbox to the physical window size. This preserves the Trimui layout
	// on Windows when the window is resized.
	renderer.SetLogicalSize(screenWidth, screenHeight)

	// Enable alpha blending for draw operations (FillRect, etc.) so
	// semi-transparent overlays/panels composite correctly over the
	// background image instead of painting opaque.
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)

	// Animated particle splash: particles converge from screen edges,
	// glow pulse reveals the JukaHub logo, then dissolves into the UI.
	ShowSplash(renderer, config)
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
	go thumbnailPrefetchWorker()

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

	// Adaptive frame rate and memory monitoring for TSP ARM64 devices
	frameRateCtrl := NewFrameRateController()
	memMonitor := NewMemoryMonitor()
	defer memMonitor.Stop()
	LogDeviceDiagnostics()

	running := true
	for running {
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
				if e.Type == sdl.KEYDOWN {
					frameRateCtrl.NotifyInput()
					screensaverNotifyInput()
					if shortcutsOpen {
						handleShortcutsInput(e)
						continue
					}
					if clipSearchOpen {
						handleClipboardInput(e)
						continue
					}
					if globalSearchOpen {
						handleGSInput(e, config)
						continue
					}
					// Tools is modal and consumes all keys while open.
					if toolsPanel.open && handleToolsKey(e, config) {
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
							if len(inputTextBuffer) > 0 {
								inputTextBuffer = inputTextBuffer[:len(inputTextBuffer)-1]
							}
							updateInputVariable(config)
						case sdl.K_RETURN:
							activeSceneIndex = -1
							activeElementIndex = -1
						}
					} else {
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
						// Video control shortcuts (always active when video is playing)
						HandleVideoKeyInput(e)
						// Handle navigation
						curScene := config.Scenes[currentSceneIndex]
						if selectedButtonIndex < 0 || selectedButtonIndex >= len(curScene.Elements) {
							selectedButtonIndex = findFirstSelectableElement(curScene)
						}
						if selectedButtonIndex < 0 || selectedButtonIndex >= len(curScene.Elements) {
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
									moveSelection(config, -1)
								}
							}
						} else if curElem.Type == "themegallery" {
							presets := ListThemePresets()
							switch e.Keysym.Sym {
							case sdl.K_LEFT:
								if themeGalleryIndex > 0 {
									themeGalleryIndex--
								}
							case sdl.K_RIGHT:
								if themeGalleryIndex < len(presets)-1 {
									themeGalleryIndex++
								}
							case sdl.K_RETURN, sdl.K_SPACE:
								if themeGalleryIndex >= 0 && themeGalleryIndex < len(presets) {
									ApplyThemePreset(config, presets[themeGalleryIndex])
								}
							case sdl.K_ESCAPE:
								moveSelection(config, -1)
							}
						} else if curElem.Type == "toggle" {
							switch e.Keysym.Sym {
							case sdl.K_RETURN, sdl.K_SPACE, sdl.K_LEFT, sdl.K_RIGHT:
								toggleVariable(config, curElem)
							case sdl.K_ESCAPE:
								moveSelection(config, -1)
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
								}
							}
						} else if curElem.Type == "packagelist" {
							handlePackageInput(e, config)
						} else if curElem.Type == "recent" {
							handleRecentInput(e, config)
						} else if curScene.Name == "Pomodoro" {
							handlePomodoroInput(e, config)
						} else if curScene.Name == "Calculator" {
							handleCalcInput(e)
						} else if curScene.Name == "Alarm" {
							handleAlarmInput(e, config)
						} else if curScene.Name == "Dashboard" {
							handleDashInput(e, config)
						} else if curScene.Name == "About" {
							handleAboutInput(e, config)
						} else if curScene.Name == "Dictionary" {
							handleDictInput(e, config)
						} else if curScene.Name == "PDFReader" {
							handlePDFInput(e, config)
						} else if curScene.Name == "MusicPlayer" {
							handleMusicInput(e, config)
						} else if curScene.Name == "Notes" {
							handleNotesInput(e, config)
						} else if curScene.Name == "Games" {
							if activeGame != gameNone {
								handleActiveGameInput(e)
							} else {
								handleGamesMenuInput(e)
							}
						} else if socialNotifOpen {
							handleSocialNotifInput(e)
						} else if wpOpen {
							handleWallpaperInput(e, config)
						} else if curScene.Name == "JukaLand" {
							handleJukaLandInput(e)
						} else {
							// Normal button/input navigation
							switch e.Keysym.Sym {
							case sdl.K_UP, sdl.K_DOWN:
								if homeLayoutActive && curElem.Type == "button" && curElem.Style == "tile" {
									moveHomeSelection(config, 0, map[sdl.Keycode]int{
										sdl.K_UP: -1, sdl.K_DOWN: 1,
									}[e.Keysym.Sym])
								} else {
									moveSelection(config, map[sdl.Keycode]int{
										sdl.K_UP: -1, sdl.K_DOWN: 1,
									}[e.Keysym.Sym])
								}
							case sdl.K_LEFT, sdl.K_RIGHT:
								if homeLayoutActive && curElem.Type == "button" && curElem.Style == "tile" {
									moveHomeSelection(config, map[sdl.Keycode]int{
										sdl.K_LEFT: -1, sdl.K_RIGHT: 1,
									}[e.Keysym.Sym], 0)
								} else {
									moveSelection(config, map[sdl.Keycode]int{
										sdl.K_LEFT: -1, sdl.K_RIGHT: 1,
									}[e.Keysym.Sym])
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
							case sdl.K_ESCAPE:
								// Generic back on non-home scenes: returns to the
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
				if clipSearchOpen {
					handleClipboardTextInput(string(e.Text[:]))
				} else if globalSearchOpen {
					handleGlobalSearchTextInput(string(e.Text[:]))
				} else if virtualKeyboardActive {
					// While the keyboard is open its target owns the text, so
					// scene-specific handlers must not also consume it.
					vkInsertText(string(e.Text[:]), config)
				} else {
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
						vkInsertText(string(e.Text[:]), config)
					}
				}
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
				if e.Button == sdl.BUTTON_LEFT {
					mx, my := physicalToLogical(int32(e.X), int32(e.Y))
					if e.Type == sdl.MOUSEBUTTONDOWN {
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
						// Tools panel is modal: a click selects a row, a click outside
						// closes it, and nothing reaches the scene underneath.
						if toolsPanel.open {
							handleToolsMouse(mx, my, config)
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
						// Image viewer overlay: only the close button is interactive.
						if imageViewerPath != "" {
							if mx >= imageViewerCloseBtn.X && mx <= imageViewerCloseBtn.X+imageViewerCloseBtn.W &&
								my >= imageViewerCloseBtn.Y && my <= imageViewerCloseBtn.Y+imageViewerCloseBtn.H {
								closeImageViewer()
							}
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

						// Notification dropdown click.
						if notifIsPointInDrop(mx, my) {
							break // consume click inside dropdown
						}
						// Notification dropdown open -> click outside dismisses it.
						if nc.open {
							notifDismiss()
							break
						}

						// Taskbar Start button.
						if mx >= tb.startRect.X && mx <= tb.startRect.X+tb.startRect.W &&
							my >= tb.startRect.Y && my <= tb.startRect.Y+tb.startRect.H {
							startMenuToggle()
							break
						}

						// Notification bell.
						if notifIsPointInBell(mx, my) {
							notifToggle()
							break
						}

						// Home cards: select + activate on click.
						if homeLayoutActive && handleHomeMouseClick(mx, my, config) {
							break
						}
						// Scene-title-row back affordance (mouse-only back for scenes
						// without an explicit Back button; controller/keyboard use B/Esc).
						if sceneTitleBackRect.W > 0 && mx >= sceneTitleBackRect.X && mx <= sceneTitleBackRect.X+sceneTitleBackRect.W &&
							my >= sceneTitleBackRect.Y && my <= sceneTitleBackRect.Y+sceneTitleBackRect.H {
							goBackScene(config)
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
								sendChatMessage("You", chatInputText)
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
						// Check video controls
						HandleVideoControlClick(mx, my)
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
										elemH := getElementHeight(elem, 500)
										lineH := int32(40)
										maxVisible := int((elemH - 30) / lineH)
										if maxVisible < 1 {
											maxVisible = 1
										}
										start := 0
										if focusedFileIndex >= maxVisible {
											start = focusedFileIndex - maxVisible + 1
										}
										end := start + maxVisible
										if end > len(entries) {
											end = len(entries)
										}
										listY := elem.Y + 30
										for i2 := start; i2 < end; i2++ {
											y := listY + int32(i2-start)*lineH
											if mx >= elem.X && mx <= elem.X+elemW && my >= y-2 && my <= y+lineH-2 {
												selectedButtonIndex = i
												focusEngine.SetByElementIndex(i)
												focusedFileIndex = i2
												feEnterFocused(config)
												break
											}
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
										thumbW := int32(160)
										cellW := thumbW + 20
										elemW := getElementWidth(elem, 1080)
										nCols := int32(elemW / cellW)
										if nCols < 1 {
											nCols = 1
										}
										cellH := int32(330)
										for idx, vid := range vids {
											col := int32(idx) % nCols
											row := int32(idx) / nCols
											xPos := elem.X + 20 + col*cellW
											yPos := elem.Y + 10 + row*(cellH+10) - shortsScrollY
											if mx >= xPos && mx <= xPos+thumbW && my >= yPos && my <= yPos+cellH {
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
				if e.Type == sdl.CONTROLLERBUTTONDOWN {
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
								if themeGalleryIndex > 0 {
									themeGalleryIndex--
								}
							case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
								if themeGalleryIndex < len(presets)-1 {
									themeGalleryIndex++
								}
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
							case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
								goBackScene(config)
							}
						} else {
							// Normal button/input navigation
							switch e.Button {
							case sdl.CONTROLLER_BUTTON_DPAD_UP:
								moveSelection(config, -1)
							case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
								moveSelection(config, 1)
							case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
								moveSelection(config, -1)
							case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
								moveSelection(config, 1)
							case sdl.CONTROLLER_BUTTON_A:
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
								// Search shortcut: jump to the Tube/search scene.
								if idx := findSceneIndex(config, "Tube"); idx >= 0 {
									changeSceneTo(config, idx)
								}
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

		// Screensaver takes over when idle.
		if ss.active {
			renderScreensaver(renderer, config, sdl.GetTicks64())
		} else {
			renderScene(renderer, config, config.Scenes[currentSceneIndex])
		}

		// Scene transition effects
		if config.Variables.ReducedMotion && (transitionPhase == "fade-out" || transitionPhase == "fade-in") {
			if transitionPhase == "fade-out" {
				completeSceneTransition(config)
			} else {
				transitionPhase = "none"
			}
		}
		if transitionPhase == "fade-out" {
			elapsed := float64(sdl.GetTicks64() - sceneFadeStart)
			duration := float64(250)
			if elapsed >= duration {
				sceneFadeAlpha = 255
				completeSceneTransition(config)
			} else {
				t := easeOutCubic(elapsed / duration)
				sceneFadeAlpha = uint8(t * 255)
			}
			renderer.SetDrawColor(15, 17, 21, sceneFadeAlpha)
			renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
			// accent sweep along the bottom edge
			renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, sceneFadeAlpha/2)
			renderer.FillRect(&sdl.Rect{X: 0, Y: screenHeight - 4, W: screenWidth, H: 4})
		} else if transitionPhase == "fade-in" {
			elapsed := float64(sdl.GetTicks64() - sceneFadeStart)
			duration := float64(300)
			if elapsed >= duration {
				sceneFadeAlpha = 255
				transitionPhase = "none"
			} else {
				t := easeOutCubic(elapsed / duration)
				sceneFadeAlpha = uint8(t * 255)
			}
			renderer.SetDrawColor(15, 17, 21, 255-sceneFadeAlpha)
			renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
			// accent sweep along the bottom edge
			renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, (255-sceneFadeAlpha)/2)
			renderer.FillRect(&sdl.Rect{X: 0, Y: screenHeight - 4, W: screenWidth, H: 4})
		}

		// Image viewer overlay
		if imageViewerPath != "" {
			renderImageViewer(renderer, config, imageViewerPath)
		}

		// Loading overlay
		if config.Variables.LoadingSpinner || transitionPhase != "none" {
			// frosted backdrop
			fillRoundedRect(renderer, 0, 0, screenWidth, screenHeight, 0, sdl.Color{R: 6, G: 8, B: 14, A: 200})
			// centered glass card
			cardW := int32(340)
			cardH := int32(220)
			cardX := (screenWidth - cardW) / 2
			cardY := (screenHeight - cardH) / 2
			fillRoundedRect(renderer, cardX+4, cardY+6, cardW, cardH, 20, ShadowFill(80))
			fillRoundedRect(renderer, cardX, cardY, cardW, cardH, 20, sdl.Color{R: 18, G: 24, B: 36, A: 235})
			renderer.SetDrawColor(255, 255, 255, 14)
			renderer.FillRect(&sdl.Rect{X: cardX + 12, Y: cardY + 1, W: cardW - 24, H: 1})
			renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 80)
			renderer.DrawRect(&sdl.Rect{X: cardX + 1, Y: cardY + 1, W: cardW - 2, H: 1})
			renderer.DrawRect(&sdl.Rect{X: cardX + 1, Y: cardY + 1, W: 1, H: cardH - 2})
			renderer.DrawRect(&sdl.Rect{X: cardX + 1, Y: cardY + cardH - 2, W: cardW - 2, H: 1})
			renderer.DrawRect(&sdl.Rect{X: cardX + cardW - 2, Y: cardY + 1, W: 1, H: cardH - 2})
			renderSpinner(renderer, cardX+cardW/2, cardY+60, 40, sdl.Color{R: 140, G: 180, B: 255, A: 220})
			font, _ := getCachedFont(config, "medium")
			if font == nil {
				font, _ = getCachedFont(config, "small")
			}
			if font != nil {
				loadingTxt := config.Variables.SpinnerText
				if loadingTxt == "" {
					loadingTxt = "Loading..."
				}
				lw, _, _ := font.SizeUTF8(loadingTxt)
				renderText(renderer, config, font, loadingTxt, sdl.Color{R: 220, G: 230, B: 245, A: 255}, cardX+(cardW-int32(lw))/2, cardY+120)
				// Rotating Juka / JukaLang themed joke
				if len(loadingJokes) > 0 {
					ji := int(sdl.GetTicks64() / 2800 % uint64(len(loadingJokes)))
					joke := loadingJokes[ji]
					smallFont, _ := getCachedFont(config, "small")
					if smallFont == nil {
						smallFont = font
					}
					jw, _, _ := smallFont.SizeUTF8(joke)
					renderText(renderer, config, smallFont, joke, sdl.Color{R: 150, G: 170, B: 200, A: 200}, cardX+(cardW-int32(jw))/2, cardY+155)
				}
			}
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
		if appRestartRequested.Load() {
			// A staged update was handed to the swap helper; quit so it can
			// replace the executable and relaunch JukaHub.
			running = false
			continue
		}

		// Quick Settings overlay.
		if qs.open {
			renderQuickSettings(renderer, config)
		}

		// Tools overlay (IP address + FTP server).
		if toolsPanel.open {
			renderToolsPanel(renderer, config)
		}

		// Wallpaper picker overlay.
		if wpOpen {
			renderWallpaperPicker(renderer, config)
		}

		// Social notifications panel.
		if socialNotifOpen {
			renderSocialNotifPanel(renderer, config)
		}

		// Shortcut hint at bottom (hidden when overlays are open).
		if !shortcutsOpen && !clipSearchOpen && !globalSearchOpen && !qs.open {
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

		renderToast(renderer, config)
		renderInstallBar(renderer, config)

		renderer.Present()
		animTime = float64(sdl.GetTicks64()) / 1000.0

		// Adaptive frame rate: faster while interacting/animating, slower when
		// idle. LowPower picks the slowest cadence instead of forcing 60fps.
		animating := transitionPhase != "none" || pressedButtonIndex >= 0 || hoverAnimProgress > 0
		frameRateCtrl.SetAnimationActive(animating)
		frameRateCtrl.SetLowPower(config.Variables.LowPower)
		sdl.Delay(frameRateCtrl.Delay())

		frameCount++
		// Periodic memory pressure check (every ~10 frames on TSP)
		if IsTrimuiDevice() && memMonitor != nil && frameCount%10 == 0 {
			EvictCachesForPressure(memMonitor)
		}

		// Check alarms every second.
		if frameCount%60 == 0 {
			alarmCheck(time.Now())
		}
	}

	// Persist favorites and resume positions on every graceful quit path
	// (window close, exit button, scene-exit trigger).
	saveFavorites()
	ClearTextCache()
	DestroyVKStaticCache()
	for _, tex := range textureCache {
		tex.Destroy()
	}
	for _, tex := range homeTextCache {
		tex.Destroy()
	}
	for _, font := range fontCache {
		font.Close()
	}
}
