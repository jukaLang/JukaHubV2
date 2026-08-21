package main

import (
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Controller Input — Xbox 360 / Trimui Smart Pro D-pad routing
//
// Button mapping (Xbox 360 → SDL):
//   A (bottom)     → CONTROLLER_BUTTON_A         → Select / confirm
//   B (right)      → CONTROLLER_BUTTON_B         → Back / cancel
//   X (left)       → CONTROLLER_BUTTON_X         → Secondary action
//   Y (top)        → CONTROLLER_BUTTON_Y         → Create / tertiary
//   D-pad          → CONTROLLER_BUTTON_DPAD_*    → Navigate
//   L1 / R1        → LEFTSHOULDER / RIGHTSHOULDER → Scene switch / tab / page
//   Start           → CONTROLLER_BUTTON_START     → Open start menu / pause
//   Back            → CONTROLLER_BUTTON_BACK      → Settings / menu
//   Left stick     → CONTROLLERAXISMOTION        → Navigate (deadzone)
// ──────────────────────────────────────────────────────────────────────

// handleControllerEvent routes all controller button events to overlays and scenes.
// Returns true if consumed.
func handleControllerEvent(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}

	// ── Overlay priority (highest to lowest) ──
	if shortcutsOpen {
		return ctrlShortcuts(e)
	}
	if clipSearchOpen {
		return ctrlClipboard(e)
	}
	if globalSearchOpen {
		return ctrlGlobalSearch(e, config)
	}
	if qs.open {
		return ctrlQuickSettings(e, config)
	}
	if wpOpen {
		return ctrlWallpaper(e)
	}
	if socialNotifOpen {
		return ctrlSocialNotif(e)
	}
	if imageViewerPath != "" {
		return ctrlImageViewer(e)
	}
	return false
}

// ── Shortcuts overlay ──

func ctrlShortcuts(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_START:
		shortcutsOpen = false
		PlayBackSound()
		return true
	}
	return true // consume all
}

// ── Clipboard overlay ──

func ctrlClipboard(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if clipCursorIdx > 0 {
			clipCursorIdx--
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		if clipCursorIdx < len(clipHistory)-1 {
			clipCursorIdx++
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_A:
		if clipCursorIdx >= 0 && clipCursorIdx < len(clipHistory) {
			sdl.SetClipboardText(clipHistory[clipCursorIdx].text)
			ShowToast("Copied to clipboard", ToastKindSuccess)
			clipSearchOpen = false
			clipSearchBuf = ""
			PlayActivateSound()
		}
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		clipSearchOpen = false
		clipSearchBuf = ""
		PlayBackSound()
	case sdl.CONTROLLER_BUTTON_X:
		if clipCursorIdx >= 0 && clipCursorIdx < len(clipHistory) {
			clipHistory = append(clipHistory[:clipCursorIdx], clipHistory[clipCursorIdx+1:]...)
			if clipCursorIdx >= len(clipHistory) {
				clipCursorIdx = len(clipHistory) - 1
			}
			PlayNavSound()
		}
	}
	return true
}

// ── Global search ──

func ctrlGlobalSearch(e *sdl.ControllerButtonEvent, config *Config) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if gsCursor > 0 {
			gsCursor--
		} else if len(gsResults) > 0 {
			gsCursor = len(gsResults) - 1
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		if gsCursor < len(gsResults)-1 {
			gsCursor++
		} else {
			gsCursor = 0
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_A:
		if gsCursor >= 0 && gsCursor < len(gsResults) && gsResults[gsCursor].Action != nil {
			gsResults[gsCursor].Action(config)
			gsQuery = ""
			gsResults = nil
			globalSearchOpen = false
			PlayActivateSound()
		}
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		gsQuery = ""
		gsResults = nil
		globalSearchOpen = false
		PlayBackSound()
	case sdl.CONTROLLER_BUTTON_X:
		if len(gsQuery) > 0 {
			gsQuery = ""
			gsResults = gsSearch("")
			gsCursor = 0
			PlayNavSound()
		}
	}
	return true
}

// ── Quick Settings ──

func ctrlQuickSettings(e *sdl.ControllerButtonEvent, config *Config) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		qsNavigate(-1)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		qsNavigate(1)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		qsNavigateSlider(-5)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT, sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		qsNavigateSlider(5)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_A:
		qsSelect()
		PlayActivateSound()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		qsToggleOpen()
		PlayBackSound()
	default:
		return true
	}
	return true
}

// ── Wallpaper picker ──

func ctrlWallpaper(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if wpCursor > 0 {
			wpCursor--
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		if wpCursor < len(wpEntries)-1 {
			wpCursor++
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		wpCursor -= 5
		if wpCursor < 0 {
			wpCursor = 0
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		wpCursor += 5
		if wpCursor >= len(wpEntries) {
			wpCursor = len(wpEntries) - 1
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_A:
		if wpCursor >= 0 && wpCursor < len(wpEntries) {
			en := wpEntries[wpCursor]
			if strings.HasPrefix(en.name, "\U0001f4c1") {
				wpDir = en.path
				scanWallpaperDir()
			} else {
				appConfig.Variables.BackgroundImage = en.path
				dynbg.wallpaper = nil
				wpOpen = false
				ShowToast("[P] Wallpaper set!", ToastKindSuccess)
			}
			PlayActivateSound()
		}
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		wpOpen = false
		PlayBackSound()
	}
	return true
}

// ── Social notifications ──

func ctrlSocialNotif(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if socialNotifCursor > 0 {
			socialNotifCursor--
		}
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		if socialNotifCursor < len(socialNotifHistory)-1 {
			socialNotifCursor++
		}
	case sdl.CONTROLLER_BUTTON_X:
		socialNotifHistory = nil
		socialNotifCursor = 0
		PlayActivateSound()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		socialNotifOpen = false
		PlayBackSound()
	}
	return true
}

// ── Image viewer ──

func ctrlImageViewer(e *sdl.ControllerButtonEvent) bool {
	if e.Button == sdl.CONTROLLER_BUTTON_B || e.Button == sdl.CONTROLLER_BUTTON_BACK {
		imageViewerPath = ""
		PlayBackSound()
		return true
	}
	return true
}

// ──────────────────────────────────────────────────────────────────────
// Scene-specific controller routing
// ──────────────────────────────────────────────────────────────────────

func handleSceneController(e *sdl.ControllerButtonEvent, config *Config) bool {
	if currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return false
	}
	sceneName := config.Scenes[currentSceneIndex].Name

	// Let scene-specific controllers try first
	if homeLayoutActive {
		if handleHomeController(e, config) {
			return true
		}
	}

	switch sceneName {
	case "Calculator":
		return ctrlCalculator(e)
	case "Pomodoro":
		return ctrlPomodoro(e)
	case "Alarm":
		return ctrlAlarm(e)
	case "Notes":
		return ctrlNotes(e)
	case "Dictionary":
		return ctrlDictionary(e)
	case "MusicPlayer":
		return ctrlMusicPlayer(e)
	case "Terminal":
		return ctrlTerminal(e)
	case "Dashboard":
		return ctrlDashboard(e, config)
	case "About":
		return ctrlAbout(e, config)
	case "PDFReader":
		return ctrlPDFReader(e)
	case "Games":
		return ctrlGames(e, config)
	case "JukaLand":
		handleJukaLandController(e)
		return true
	case "Patch":
		return handlePatchController(e, config)
	}
	return false
}

// ── Calculator ──

func ctrlCalculator(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		calcNavigate(-1, 0)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		calcNavigate(1, 0)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		calcNavigate(0, -1)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		calcNavigate(0, 1)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_A:
		calcSelect()
		PlayActivateSound()
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		calcHandleFunc("C")
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		calcHandleFunc("<")
		PlayNavSound()
	default:
		return false
	}
	return true
}

// ── Pomodoro ──

func ctrlPomodoro(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_A:
		pomodoroTogglePause()
		PlayActivateSound()
	case sdl.CONTROLLER_BUTTON_Y:
		pomodoroReset()
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_X:
		pomodoroSkip()
		PlayNavSound()
	default:
		return false
	}
	return true
}

// ── Alarm ──

func ctrlAlarm(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		alarmNavigate(-1)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		alarmNavigate(1)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_A:
		alarmSelect()
		PlayActivateSound()
	case sdl.CONTROLLER_BUTTON_B:
		alarmBack()
		PlayBackSound()
	case sdl.CONTROLLER_BUTTON_Y:
		alarmInitUI()
		PlayActivateSound()
	case sdl.CONTROLLER_BUTTON_X:
		alarmToggleFocused()
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		alarmDeleteFocused()
		PlayNavSound()
	default:
		return false
	}
	return true
}

// ── Notes ──

func ctrlNotes(e *sdl.ControllerButtonEvent) bool {
	if notes.tab != 0 {
		// In edit mode — let keyboard handler manage text input
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if notes.cursor > 0 {
			notes.cursor--
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		filtered := notesFiltered()
		if notes.cursor < len(filtered)-1 {
			notes.cursor++
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_A:
		filtered := notesFiltered()
		if notes.cursor >= 0 && notes.cursor < len(filtered) {
			note := filtered[notes.cursor]
			// Find the note in the store and open for editing
			for _, n := range notes.store.Notes {
				if n.ID == note.ID {
					notes.tab = 1
					notes.editTitle = n.Title
					notes.editContent = n.Content
					notes.editID = n.ID
					PlayActivateSound()
					return true
				}
			}
		}
	case sdl.CONTROLLER_BUTTON_B:
		notes.tab = 0
		PlayBackSound()
	case sdl.CONTROLLER_BUTTON_Y:
		notes.tab = 3
		notes.editTitle = ""
		notes.editContent = ""
		notes.editID = -1
		PlayActivateSound()
	case sdl.CONTROLLER_BUTTON_X:
		filtered := notesFiltered()
		if notes.cursor >= 0 && notes.cursor < len(filtered) {
			notesDeleteNote(filtered[notes.cursor].ID)
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		// Cycle folder filter left
		folders := notes.store.Folders
		if len(folders) > 0 {
			idx := 0
			for i, f := range folders {
				if f == notes.filter {
					idx = i
					break
				}
			}
			idx--
			if idx < 0 {
				idx = len(folders) - 1
			}
			notes.filter = folders[idx]
			notes.cursor = 0
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		// Cycle folder filter right
		folders := notes.store.Folders
		if len(folders) > 0 {
			idx := 0
			for i, f := range folders {
				if f == notes.filter {
					idx = i
					break
				}
			}
			idx++
			if idx >= len(folders) {
				idx = 0
			}
			notes.filter = folders[idx]
			notes.cursor = 0
		}
		PlayNavSound()
	default:
		return false
	}
	return true
}

// ── Dictionary ──

func ctrlDictionary(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if dict.cursor > 0 {
			dict.cursor--
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		if dict.cursor < len(dict.results)-1 {
			dict.cursor++
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_A:
		if dict.cursor >= 0 && dict.cursor < len(dict.results) {
			word := dict.results[dict.cursor].Word
			dict.query = word
			dict.result = dictLookup(word)
			PlayActivateSound()
		}
	default:
		return false
	}
	return true
}

// ── Music Player ──

func ctrlMusicPlayer(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if music.cursor > 0 {
			music.cursor--
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		if music.cursor < len(music.fileList)-1 {
			music.cursor++
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_A:
		// Select current file/directory
		if music.cursor >= 0 && music.cursor < len(music.fileList) {
			entry := music.fileList[music.cursor]
			if entry.IsDir {
				musicScanDir(entry.Path)
				music.cursor = 0
			} else {
				// Find track index and play
				for i, t := range music.tracks {
					if t.Path == entry.Path {
						musicPlayTrack(i)
						break
					}
				}
			}
			PlayActivateSound()
		}
	case sdl.CONTROLLER_BUTTON_B:
		// Go up one directory
		if music.dir != "." && music.dir != "" {
			parent := ""
			for i := len(music.dir) - 2; i >= 0; i-- {
				if music.dir[i] == '/' || music.dir[i] == '\\' {
					parent = music.dir[:i]
					break
				}
			}
			if parent != "" {
				musicScanDir(parent)
				music.cursor = 0
			} else {
				goBackScene(appConfig)
			}
			PlayBackSound()
		} else {
			goBackScene(appConfig)
			PlayBackSound()
		}
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		musicPrev()
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT, sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		musicNext()
		PlayNavSound()
	default:
		return false
	}
	return true
}

// ── Terminal ──

func ctrlTerminal(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_A:
		if term.input != "" {
			termExecute(term.input)
			term.input = ""
			term.cursor = 0
			term.scrollY = 0
		}
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		term.scrollY -= 40
		if term.scrollY < 0 {
			term.scrollY = 0
		}
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		term.scrollY += 40
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		term.scrollY -= 200
		if term.scrollY < 0 {
			term.scrollY = 0
		}
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		term.scrollY += 200
	default:
		return false
	}
	return true
}

// ── Dashboard ──

func ctrlDashboard(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e.Button == sdl.CONTROLLER_BUTTON_B || e.Button == sdl.CONTROLLER_BUTTON_BACK {
		goBackScene(config)
		PlayBackSound()
		return true
	}
	return false
}

// ── About ──

func ctrlAbout(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e.Button == sdl.CONTROLLER_BUTTON_B || e.Button == sdl.CONTROLLER_BUTTON_BACK {
		goBackScene(config)
		PlayBackSound()
		return true
	}
	return false
}

// ── PDF Reader ──

func ctrlPDFReader(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		pdf.scrollY -= 60
		if pdf.scrollY < 0 {
			pdf.scrollY = 0
		}
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		pdf.scrollY += 60
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		if pdf.page > 1 {
			pdf.page--
			pdf.scrollY = 0
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT, sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		if pdf.page < pdf.doc.NumPages {
			pdf.page++
			pdf.scrollY = 0
			PlayNavSound()
		}
	default:
		return false
	}
	return true
}

// ── Games ──

func ctrlGames(e *sdl.ControllerButtonEvent, config *Config) bool {
	if activeGame != gameNone {
		return ctrlActiveGame(e)
	}
	return ctrlGamesMenu(e, config)
}

func ctrlGamesMenu(e *sdl.ControllerButtonEvent, config *Config) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if gameMenuCursor >= 2 {
			gameMenuCursor -= 2
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		if gameMenuCursor < len(gameList)-2 {
			gameMenuCursor += 2
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		if gameMenuCursor%2 == 1 {
			gameMenuCursor--
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		if gameMenuCursor%2 == 0 && gameMenuCursor+1 < len(gameList) {
			gameMenuCursor++
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_A:
		startGame(gameList[gameMenuCursor].id)
		PlayActivateSound()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		goBackScene(config)
		PlayBackSound()
	default:
		return false
	}
	return true
}

func ctrlActiveGame(e *sdl.ControllerButtonEvent) bool {
	// Universal game controls
	if e.Button == sdl.CONTROLLER_BUTTON_B && gameOver {
		activeGame = gameNone
		gameOver = false
		PlayBackSound()
		return true
	}
	if e.Button == sdl.CONTROLLER_BUTTON_START && gameOver {
		switch activeGame {
		case gameSnake:
			initSnake()
		case gameTetris:
			initTetris()
		case game2048:
			init2048()
		case gamePong:
			initPong()
		}
		gameOver = false
		gameScore = 0
		return true
	}

	switch activeGame {
	case gameSnake:
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_DPAD_UP:
			if snakeCurDir != snakeDown {
				snakeNextDir = snakeUp
			}
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
			if snakeCurDir != snakeUp {
				snakeNextDir = snakeDown
			}
		case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
			if snakeCurDir != snakeRight {
				snakeNextDir = snakeLeft
			}
		case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
			if snakeCurDir != snakeLeft {
				snakeNextDir = snakeRight
			}
		case sdl.CONTROLLER_BUTTON_A:
			if gameOver {
				initSnake()
				gameOver = false
				gameScore = 0
			}
		}
	case gameTetris:
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
			if tetrisCanPlace(tetrisPiece.shape, tetrisPiece.x-1, tetrisPiece.y) {
				tetrisPiece.x--
			}
		case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
			if tetrisCanPlace(tetrisPiece.shape, tetrisPiece.x+1, tetrisPiece.y) {
				tetrisPiece.x++
			}
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
			if tetrisCanPlace(tetrisPiece.shape, tetrisPiece.x, tetrisPiece.y+1) {
				tetrisPiece.y++
				gameScore++
			}
		case sdl.CONTROLLER_BUTTON_DPAD_UP:
			rotated := tetrisRotate()
			if tetrisCanPlace(rotated, tetrisPiece.x, tetrisPiece.y) {
				tetrisPiece.shape = rotated
			}
		case sdl.CONTROLLER_BUTTON_A:
			if gameOver {
				initTetris()
				gameOver = false
				gameScore = 0
			} else {
				for tetrisCanPlace(tetrisPiece.shape, tetrisPiece.x, tetrisPiece.y+1) {
					tetrisPiece.y++
					gameScore += 2
				}
				tetrisPlace()
			}
		case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
			if tetrisCanPlace(tetrisPiece.shape, tetrisPiece.x, tetrisPiece.y+1) {
				tetrisPiece.y++
				gameScore++
			}
		}
	case game2048:
		if gameOver {
			return true
		}
		scoreBefore := gameScore
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
			gameScore += g2048MoveLeft()
		case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
			gameScore += g2048MoveRight()
		case sdl.CONTROLLER_BUTTON_DPAD_UP:
			gameScore += g2048MoveUp()
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
			gameScore += g2048MoveDown()
		default:
			return true
		}
		if gameScore != scoreBefore {
			place2048Tile()
			PlayNavSound()
			if !g2048HasMoves() {
				gameOver = true
			}
		}
	case gamePong:
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_DPAD_UP:
			pongPaddleY[0] -= 30
			if pongPaddleY[0] < 0 {
				pongPaddleY[0] = 0
			}
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
			pongPaddleY[0] += 30
			if pongPaddleY[0] > float64(pongH)-float64(pongPaddleH) {
				pongPaddleY[0] = float64(pongH) - float64(pongPaddleH)
			}
		case sdl.CONTROLLER_BUTTON_A:
			if gameOver {
				initPong()
			}
		}
	}
	return true
}

// ──────────────────────────────────────────────────────────────────────
// Left stick analog → D-pad mapping
// ──────────────────────────────────────────────────────────────────────

const stickDeadzone = 12000

func handleControllerAxis(e *sdl.ControllerAxisEvent) bool {
	// Left stick X axis → left/right
	if e.Axis == sdl.CONTROLLER_AXIS_LEFTX {
		if e.Value > stickDeadzone {
			return handleStickDpad(sdl.CONTROLLER_BUTTON_DPAD_RIGHT)
		}
		if e.Value < -stickDeadzone {
			return handleStickDpad(sdl.CONTROLLER_BUTTON_DPAD_LEFT)
		}
	}
	// Left stick Y axis → up/down
	if e.Axis == sdl.CONTROLLER_AXIS_LEFTY {
		if e.Value > stickDeadzone {
			return handleStickDpad(sdl.CONTROLLER_BUTTON_DPAD_DOWN)
		}
		if e.Value < -stickDeadzone {
			return handleStickDpad(sdl.CONTROLLER_BUTTON_DPAD_UP)
		}
	}
	return false
}

var lastStickBtn uint8
var lastStickTime uint64

func handleStickDpad(btn uint8) bool {
	now := sdl.GetTicks64()
	if btn == lastStickBtn && now-lastStickTime < 150 {
		return false // repeat rate limiter
	}
	lastStickBtn = btn
	lastStickTime = now
	fakeEvent := &sdl.ControllerButtonEvent{
		Type:   sdl.CONTROLLERBUTTONDOWN,
		Button: btn,
	}
	return handleControllerEvent(fakeEvent, appConfig)
}
