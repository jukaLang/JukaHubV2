package main

// game_library_ui.go — the modal library browser.
//
// Three columns: consoles (with a leading Continue row built from the recents
// file), the games of the selected console, and a box-art preview. A launches,
// X opens LAN play (netplay_ui.go), L1 fetches missing covers, Y rescans after
// copying new files onto the card, B closes. It is a modal overlay like the
// Emulators panel, so it works on every config, including the desktop one with
// no Roms folder.

import (
	"path/filepath"
	"strings"

	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"
)

type gameLibRowHit struct {
	index int
	rect  sdl.Rect
}

type gameLibraryPanel struct {
	open     bool
	lib      gameLibraryReport
	recents  []recentGame
	system   int
	cursor   int
	scroll   int
	focus    int // 0 = console column, 1 = games column
	artCache map[string]*sdl.Texture
	// artSeen is the artwork-fetch generation whose images are in artCache. A
	// newer generation means the cache holds "missing" entries that now exist.
	artSeen uint64
	panel   sdl.Rect

	systemHits []gameLibRowHit
	gameHits   []gameLibRowHit
	// artRect is the preview frame, filled by the renderer for click tests.
	artRect sdl.Rect
}

var gameLib gameLibraryPanel

const continueSystemName = "Continue"

// librarySystemsForUI returns the console list with the Continue row first.
// Recents are a view over the same games, not a second library, so launching
// one goes through exactly the same resolution path.
func librarySystemsForUI(lib gameLibraryReport, recents []recentGame) []librarySystem {
	systems := make([]librarySystem, 0, len(lib.Systems)+1)
	if len(recents) > 0 {
		recentGames := make([]libraryGame, 0, len(recents))
		for _, it := range recents {
			title := strings.TrimSpace(it.Title)
			if title == "" {
				title = gameDisplayTitle(it.Path)
			}
			recentGames = append(recentGames, libraryGame{
				Path:   it.Path,
				System: it.System,
				Title:  title,
				Art: romArtworkPath(scannedROM{
					Path:   it.Path,
					System: it.System,
				}, libraryArtworkRoot()),
			})
		}
		systems = append(systems, librarySystem{Name: continueSystemName, Games: recentGames})
	}
	systems = append(systems, lib.Systems...)
	return systems
}

// libraryArtworkRoot is a seam for tests: the artwork directory normally lives
// under the app data folder, which a unit test cannot write to.
var libraryArtworkRoot = romArtworkDir

func currentGameSystems() []librarySystem {
	return librarySystemsForUI(gameLib.lib, gameLib.recents)
}

func selectedLibrarySystem() (librarySystem, bool) {
	systems := currentGameSystems()
	if gameLib.system < 0 || gameLib.system >= len(systems) {
		return librarySystem{}, false
	}
	return systems[gameLib.system], true
}

// selectedLibraryGame returns the highlighted game, clamped to the system.
func selectedLibraryGame() (libraryGame, bool) {
	system, ok := selectedLibrarySystem()
	if !ok || len(system.Games) == 0 {
		return libraryGame{}, false
	}
	if gameLib.cursor < 0 {
		gameLib.cursor = 0
	}
	if gameLib.cursor >= len(system.Games) {
		gameLib.cursor = len(system.Games) - 1
	}
	return system.Games[gameLib.cursor], true
}

func scanGameLibrary() gameLibraryReport {
	return buildGameLibrary(scanROMLibrary(romLibraryRoots()), libraryArtworkRoot())
}

func openGameLibraryPanel() {
	// One modal at a time: the library replaces the panels that can open it.
	emuPanel.open = false
	qs.open = false
	toolsPanel.open = false
	closeGameLibraryArt()
	gameLib.open = true
	gameLib.focus = 0
	gameLib.system = 0
	gameLib.cursor = 0
	gameLib.scroll = 0
	gameLib.artSeen = artworkFetchGeneration()
	gameLib.systemHits = nil
	gameLib.gameHits = nil
	gameLib.recents = recentGames()
	gameLib.lib = scanGameLibrary()
	PlayActivateSound()
}

func closeGameLibraryPanel() {
	if !gameLib.open {
		return
	}
	gameLib.open = false
	closeGameLibraryArt()
	PlayBackSound()
}

// closeGameLibraryArt frees the preview textures. They belong to the current
// renderer, so keeping them across a renderer recreation would dangle.
func closeGameLibraryArt() {
	for path, tex := range gameLib.artCache {
		if tex != nil {
			tex.Destroy()
		}
		delete(gameLib.artCache, path)
	}
	gameLib.artRect = sdl.Rect{}
}

// refreshGameLibrary rescans the card and keeps the highlight on a real row.
func refreshGameLibrary() {
	gameLib.lib = scanGameLibrary()
	gameLib.recents = recentGames()
	systems := currentGameSystems()
	if gameLib.system >= len(systems) {
		gameLib.system = len(systems) - 1
	}
	if gameLib.system < 0 {
		gameLib.system = 0
	}
	if system, ok := selectedLibrarySystem(); ok && gameLib.cursor >= len(system.Games) {
		gameLib.cursor = len(system.Games) - 1
	}
	if gameLib.cursor < 0 {
		gameLib.cursor = 0
	}
	gameLib.scroll = 0
}

func gameLibraryStatus() string {
	if gameLib.lib.Total == 0 {
		if len(gameLib.recents) > 0 {
			return itoa(len(gameLib.recents)) + " recent game(s) — no ROM folders found, press Y to rescan"
		}
		return "No games found — copy ROMs into Roms/<system>/ and press Y"
	}
	status := itoa(gameLib.lib.Total) + " game(s) in " + itoa(len(gameLib.lib.Systems)) + " console(s)"
	if len(gameLib.recents) > 0 {
		status += " · " + itoa(len(gameLib.recents)) + " recent"
	}
	if artworkFetchRunning() {
		status += " · fetching art…"
	}
	return status
}

// gameLibraryMove handles D-pad navigation between the columns.
func gameLibraryMove(dx, dy int) {
	systems := currentGameSystems()
	if len(systems) == 0 {
		return
	}
	if gameLib.focus == 0 {
		if dx > 0 {
			gameLib.focus = 1
			PlayNavSound()
			return
		}
		if dy != 0 {
			gameLib.system += dy
			if gameLib.system < 0 {
				gameLib.system = len(systems) - 1
			}
			if gameLib.system >= len(systems) {
				gameLib.system = 0
			}
			gameLib.cursor = 0
			gameLib.scroll = 0
			PlayNavSound()
		}
		return
	}
	if dx < 0 {
		gameLib.focus = 0
		PlayNavSound()
		return
	}
	system, ok := selectedLibrarySystem()
	if !ok || len(system.Games) == 0 {
		return
	}
	if dy == 0 {
		return
	}
	gameLib.cursor += dy
	if gameLib.cursor < 0 {
		gameLib.cursor = len(system.Games) - 1
	}
	if gameLib.cursor >= len(system.Games) {
		gameLib.cursor = 0
	}
	PlayNavSound()
}

// launchSelectedGame resolves the emulator for the highlighted game and starts
// it. Returns false when there is nothing to launch.
func launchSelectedGame() bool {
	game, ok := selectedLibraryGame()
	if !ok {
		return false
	}
	if gameLib.focus == 0 {
		// Pressing A on a console opens its list instead of guessing a game.
		gameLib.focus = 1
		PlayNavSound()
		return true
	}
	launcher := resolveGameLauncher(game.System)
	if launcher == "" {
		ShowToast("No emulator found for "+game.System+" — add one on the Emulators screen", ToastKindWarning)
		return true
	}
	if err := launchLibraryGame(game, launcher); err != nil {
		ShowToast("Cannot launch "+game.Title+": "+err.Error(), ToastKindError)
		return true
	}
	gameLib.recents = recentGames()
	PlaySuccessSound()
	ShowToast("Playing "+game.Title, ToastKindSuccess)
	return true
}

// fetchMissingLibraryArtwork starts a background download for every scanned
// game without a local cover. The scan is repeated here instead of reusing the
// panel's snapshot because a card can be updated while the panel is open.
func fetchMissingLibraryArtwork() {
	if artworkFetchRunning() {
		ShowToast(artworkFetchSummary(), ToastKindInfo)
		return
	}
	artRoot := libraryArtworkRoot()
	if strings.TrimSpace(artRoot) == "" {
		ShowToast("The artwork folder is unavailable.", ToastKindWarning)
		return
	}
	report := scanROMLibrary(romLibraryRoots())
	missing := romsMissingArtwork(report.Games, artRoot)
	if len(missing) == 0 {
		ShowToast("Every scanned game already has box art.", ToastKindSuccess)
		return
	}
	if !startLibraryArtworkFetch(missing) {
		ShowToast("An artwork fetch is already running.", ToastKindInfo)
		return
	}
	ShowToast("Fetching "+itoa(len(missing))+" missing cover(s) in the background", ToastKindInfo)
}

// handleGameLibraryKey consumes every key while the panel is open so the scene
// underneath never reacts behind the modal.
func handleGameLibraryKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !gameLib.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		gameLibraryMove(0, -1)
	case sdl.K_DOWN:
		gameLibraryMove(0, 1)
	case sdl.K_LEFT:
		gameLibraryMove(-1, 0)
	case sdl.K_RIGHT:
		gameLibraryMove(1, 0)
	case sdl.K_RETURN, sdl.K_SPACE:
		launchSelectedGame()
	case sdl.K_x:
		openNetplayForSelectedGame()
	case sdl.K_f:
		fetchMissingLibraryArtwork()
	case sdl.K_y:
		refreshGameLibrary()
		ShowToast(gameLibraryStatus(), ToastKindInfo)
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		closeGameLibraryPanel()
	}
	return true
}

func ctrlGameLibrary(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		gameLibraryMove(0, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		gameLibraryMove(0, 1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		gameLibraryMove(-1, 0)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		gameLibraryMove(1, 0)
	case sdl.CONTROLLER_BUTTON_A:
		launchSelectedGame()
	case sdl.CONTROLLER_BUTTON_X:
		openNetplayForSelectedGame()
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		fetchMissingLibraryArtwork()
	case sdl.CONTROLLER_BUTTON_Y:
		refreshGameLibrary()
		ShowToast(gameLibraryStatus(), ToastKindInfo)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeGameLibraryPanel()
	default:
		return false
	}
	return true
}

// handleGameLibraryMouse selects a console, launches a game, or closes the
// panel when the click lands outside it.
func handleGameLibraryMouse(mx, my int32, config *Config) bool {
	if !gameLib.open {
		return false
	}
	for _, hit := range gameLib.systemHits {
		if pointInRect(mx, my, hit.rect) {
			gameLib.focus = 0
			gameLib.system = hit.index
			gameLib.cursor = 0
			gameLib.scroll = 0
			return true
		}
	}
	for _, hit := range gameLib.gameHits {
		if pointInRect(mx, my, hit.rect) {
			gameLib.focus = 1
			gameLib.cursor = hit.index
			launchSelectedGame()
			return true
		}
	}
	if gameLib.panel.W > 0 && pointInRect(mx, my, gameLib.panel) {
		return true
	}
	closeGameLibraryPanel()
	return true
}

// artTexture loads and caches a box-art texture. A failed load is cached as a
// nil so a missing file does not hit the disk on every frame.
func artTexture(renderer *sdl.Renderer, path string) *sdl.Texture {
	if renderer == nil || strings.TrimSpace(path) == "" {
		return nil
	}
	if tex, ok := gameLib.artCache[path]; ok {
		return tex
	}
	if gameLib.artCache == nil {
		gameLib.artCache = map[string]*sdl.Texture{}
	}
	surface, err := img.Load(path)
	if err != nil {
		gameLib.artCache[path] = nil
		return nil
	}
	tex, _ := renderer.CreateTextureFromSurface(surface)
	surface.Free()
	gameLib.artCache[path] = tex
	return tex
}

// drawLibraryArt draws box art fitted inside rect, or a monogram placeholder
// when the game has no downloaded image yet.
func drawLibraryArt(renderer *sdl.Renderer, config *Config, rect sdl.Rect, game libraryGame) {
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 14, WithAlpha(ColorSurfaceRow, 160))
	strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 14, 1, ColorBorderDefault)

	tex := artTexture(renderer, game.Art)
	if tex != nil {
		_, _, tw, th, err := tex.Query()
		if err == nil && tw > 0 && th > 0 {
			inner := rect.W - 16
			if rect.H-16 < inner {
				inner = rect.H - 16
			}
			dw := inner
			dh := int32(float64(inner) * float64(th) / float64(tw))
			if dh > inner {
				dh = inner
				dw = int32(float64(inner) * float64(tw) / float64(th))
			}
			dst := sdl.Rect{
				X: rect.X + (rect.W-dw)/2,
				Y: rect.Y + (rect.H-dh)/2,
				W: dw,
				H: dh,
			}
			renderer.Copy(tex, nil, &dst)
			return
		}
	}

	// Placeholder: initials on an accent tint.
	font, _ := getCachedFont(config, "large")
	if font == nil {
		return
	}
	label := strings.TrimSpace(game.Title)
	if label == "" {
		label = "?"
	}
	runes := []rune(label)
	initials := strings.ToUpper(string(runes[0]))
	if len(runes) > 1 {
		if idx := strings.IndexRune(label, ' '); idx >= 0 && idx+1 < len(runes) {
			initials += strings.ToUpper(string(runes[idx+1]))
		}
	}
	tw, th, _ := font.SizeUTF8(initials)
	renderText(renderer, config, font, initials,
		WithAlpha(installHighlight(), 230),
		rect.X+(rect.W-int32(tw))/2, rect.Y+(rect.H-int32(th))/2)
}

// renderGameLibraryPanel draws the three-column browser.
func renderGameLibraryPanel(renderer *sdl.Renderer, config *Config) {
	if !gameLib.open || renderer == nil {
		return
	}
	// A fetch that finished after the last frame turned placeholder tiles into
	// real covers. The textures must be released on the render thread, so the
	// reload happens here rather than in the downloader.
	if gen := artworkFetchGeneration(); gen != gameLib.artSeen {
		closeGameLibraryArt()
		gameLib.artSeen = gen
	}
	if gameLib.artCache == nil {
		gameLib.artCache = map[string]*sdl.Texture{}
	}

	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	large, _ := getCachedFont(config, "large")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}
	if large == nil {
		large = medium
	}
	smallH := int32(font.Height())
	medH := int32(medium.Height())
	largeH := int32(large.Height())

	const pad = int32(24)
	headerH := int32(18) + largeH + int32(6) + smallH + int32(14)
	footerH := int32(16) + smallH
	panelW := int32(1120)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	panelH := screenHeight - 56
	if panelH < 360 {
		panelH = screenHeight - 16
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	gameLib.panel = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 190)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, ColorPanelRaised)
	strokeRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, 1, ColorBorderDefault)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 110)
	renderer.FillRect(&sdl.Rect{X: panelX + 18, Y: panelY + 1, W: panelW - 36, H: 2})

	renderText(renderer, config, large, "Games", ColorTextPrimary(), panelX+pad, panelY+14)
	renderText(renderer, config, font, gameLibraryStatus(), ColorTextTertiary(),
		panelX+pad, panelY+14+largeH+6)

	systems := currentGameSystems()
	if gameLib.system >= len(systems) {
		gameLib.system = len(systems) - 1
	}
	if gameLib.system < 0 {
		gameLib.system = 0
	}

	bodyY := panelY + headerH
	bodyH := panelH - headerH - footerH
	innerW := panelW - 2*pad

	artW := int32(250)
	if innerW < 760 {
		artW = 0 // narrow screens drop the preview instead of squeezing the list
	}
	sysW := innerW / 4
	if sysW < 190 {
		sysW = 190
	}
	if sysW > 300 {
		sysW = 300
	}
	gap := int32(16)
	gameW := innerW - sysW - artW - 2*gap
	if gameW < 200 {
		gameW = innerW - sysW - gap
		artW = 0
	}
	sysX := panelX + pad
	gameX := sysX + sysW + gap
	artX := gameX + gameW + gap

	focusColor := installHighlight()

	// ── Consoles ────────────────────────────────────────────────────────
	sysRowH := int32(44)
	visibleSys := int(bodyH / sysRowH)
	if visibleSys < 1 {
		visibleSys = 1
	}
	gameLib.systemHits = gameLib.systemHits[:0]
	startSys := 0
	if gameLib.system >= visibleSys {
		startSys = gameLib.system - visibleSys + 1
	}
	for i := 0; i < visibleSys; i++ {
		idx := startSys + i
		if idx >= len(systems) {
			break
		}
		rect := sdl.Rect{X: sysX, Y: bodyY + int32(i)*sysRowH, W: sysW, H: sysRowH - 6}
		gameLib.systemHits = append(gameLib.systemHits, gameLibRowHit{index: idx, rect: rect})
		selected := idx == gameLib.system
		bg := WithAlpha(ColorSurfaceRow, 170)
		if selected {
			bg = WithAlpha(focusColor, 52)
		}
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, bg)
		if selected && gameLib.focus == 0 {
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, 2, focusColor)
		}
		name := systems[idx].Name
		count := itoa(len(systems[idx].Games))
		cw, _, _ := font.SizeUTF8(count)
		renderText(renderer, config, medium, ellipsize(medium, name, rect.W-24-int32(cw)-12),
			ColorTextPrimary(), rect.X+12, rect.Y+(rect.H-medH)/2)
		renderText(renderer, config, font, count, ColorTextTertiary(),
			rect.X+rect.W-12-int32(cw), rect.Y+(rect.H-smallH)/2)
	}

	// ── Games ───────────────────────────────────────────────────────────
	system, hasSystem := selectedLibrarySystem()
	gameRowH := int32(58)
	visibleGames := int(bodyH / gameRowH)
	if visibleGames < 1 {
		visibleGames = 1
	}
	if len(system.Games) > 0 {
		if gameLib.cursor >= len(system.Games) {
			gameLib.cursor = len(system.Games) - 1
		}
		if gameLib.cursor < 0 {
			gameLib.cursor = 0
		}
		if gameLib.cursor < gameLib.scroll {
			gameLib.scroll = gameLib.cursor
		}
		if gameLib.cursor >= gameLib.scroll+visibleGames {
			gameLib.scroll = gameLib.cursor - visibleGames + 1
		}
		if gameLib.scroll > len(system.Games)-visibleGames {
			gameLib.scroll = len(system.Games) - visibleGames
		}
		if gameLib.scroll < 0 {
			gameLib.scroll = 0
		}
	} else {
		gameLib.scroll = 0
	}
	gameLib.gameHits = gameLib.gameHits[:0]
	if !hasSystem || len(system.Games) == 0 {
		message := "No games yet"
		detail := "Copy ROMs into Roms/<system>/ on the card, then press Y to rescan."
		if strings.HasPrefix(system.Name, continueSystemName) {
			detail = "Launch a game and it appears here for one-press resume."
		}
		renderText(renderer, config, medium, message, ColorTextSecondary(), gameX, bodyY+8)
		renderText(renderer, config, font, detail, ColorTextTertiary(), gameX, bodyY+8+medH+8)
	} else {
		for i := 0; i < visibleGames; i++ {
			idx := gameLib.scroll + i
			if idx >= len(system.Games) {
				break
			}
			rect := sdl.Rect{X: gameX, Y: bodyY + int32(i)*gameRowH, W: gameW, H: gameRowH - 6}
			gameLib.gameHits = append(gameLib.gameHits, gameLibRowHit{index: idx, rect: rect})
			selected := idx == gameLib.cursor
			bg := WithAlpha(ColorSurfaceRow, 150)
			if selected {
				bg = WithAlpha(focusColor, 46)
			}
			fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, bg)
			if selected && gameLib.focus == 1 {
				strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, 2, focusColor)
			}
			game := system.Games[idx]
			renderText(renderer, config, medium, ellipsize(medium, game.Title, rect.W-24),
				ColorTextPrimary(), rect.X+12, rect.Y+8)
			renderText(renderer, config, font, ellipsize(font, game.System+" · "+filepath.Base(game.Path), rect.W-24),
				ColorTextTertiary(), rect.X+12, rect.Y+8+medH+2)
		}
		if gameLib.scroll > 0 || gameLib.scroll+visibleGames < len(system.Games) {
			rng := itoa(gameLib.scroll+1) + "-" + itoa(gameLib.scroll+visibleGames) + " of " + itoa(len(system.Games))
			renderText(renderer, config, font, rng, ColorTextTertiary(),
				gameX, bodyY+bodyH-smallH)
		}
	}

	// ── Box art preview ─────────────────────────────────────────────────
	if artW > 0 {
		preview := libraryGame{}
		if hasSystem && len(system.Games) > 0 {
			preview = system.Games[gameLib.cursor]
		}
		artSize := artW
		if bodyH-70 < artSize {
			artSize = bodyH - 70
		}
		rect := sdl.Rect{X: artX, Y: bodyY, W: artSize, H: artSize}
		gameLib.artRect = rect
		drawLibraryArt(renderer, config, rect, preview)
		if preview.Title != "" {
			label := ellipsize(font, preview.Title, artW)
			renderText(renderer, config, font, label, ColorTextSecondary(), artX, bodyY+artSize+10)
			renderText(renderer, config, font, preview.System, ColorTextTertiary(), artX, bodyY+artSize+10+smallH+2)
		}
	} else {
		gameLib.artRect = sdl.Rect{}
	}

	hint := "D-Pad: browse   A: play   X: LAN play   L1/F: fetch art   Y: rescan   B: close"
	if gameLib.lib.Total == 0 {
		hint = "Y: rescan   B: close"
	}
	renderText(renderer, config, font, hint, ColorTextSecondary(), panelX+pad, panelY+panelH-footerH+4)
	if artW > 0 && len(gameLib.lib.Roots) > 0 {
		root := ellipsize(font, gameLib.lib.Roots[0], panelW-pad*2-260)
		renderText(renderer, config, font, root, ColorTextTertiary(), panelX+pad+260, panelY+panelH-footerH+4)
	}
}
