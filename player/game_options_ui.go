package main

// game_options_ui.go — the per-game options sheet in the library.
//
// The library is a browser: A plays, X opens LAN play, L1 fills covers. The
// settings that belong to one title rather than the whole card live behind
// Start (or O on a keyboard): a CPU profile and ceiling for that game, its
// RetroArch video filter (videofilter.go) and extra arguments for its
// launcher. Nothing here writes to the emulator or the firmware
// directly — the sheet stores a small JSON record and the launch path
// (game_library.go) applies it for that child only. The file rows are the
// exception, each behind a keyboard's Rename or a question that opens on
// Cancel: Rename file (game_rename.go), Remove cover (artwork_nocover.go) and
// Delete game (game_delete.go) change the card.

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	gameOptionsRowTitle = iota
	gameOptionsRowFileName
	gameOptionsRowHidden
	gameOptionsRowSort
	gameOptionsRowLayout
	gameOptionsRowConsoleLook // game_library_pixel_look.go
	gameOptionsRowShowHidden
	gameOptionsRowEmulator
	gameOptionsRowDisc // discpage.go: only for a game on more than one disc
	gameOptionsRowCPUProfile
	gameOptionsRowCeiling
	gameOptionsRowVideo
	gameOptionsRowSpeed
	gameOptionsRowArgs
	gameOptionsRowOpenIn
	gameOptionsRowCheats
	gameOptionsRowButtons // racontrols_ui.go
	gameOptionsRowTurbo   // racontrols_ui.go
	gameOptionsRowDiagnose
	gameOptionsRowChooseCover
	gameOptionsRowRemoveCover
	gameOptionsRowCoverName
	gameOptionsRowCovers
	gameOptionsRowReset
	gameOptionsRowDelete
	gameOptionsRowCount
)

// The sheet's second step: a short list of answers that replaces the rows
// for a destructive action, with the safe answer first or last as the action
// needs (gameOptionsChoiceRows).
const (
	gameOptionsChoiceNone = iota
	gameOptionsChoiceReset
	gameOptionsChoiceCovers
	gameOptionsChoiceRemoveCover // artwork_nocover.go
	gameOptionsChoiceDelete      // game_delete.go
)

var gameOptsUI struct {
	open   bool
	game   libraryGame
	cursor int
	scroll int
	// visible is how many rows the last paint fit; gameOptionsMove uses it to
	// keep the cursor on the card on a short screen.
	visible int
	opts    gameOptions
	panel   sdl.Rect
	// rowHits are the clickable rows, filled by the renderer.
	rowHits []gameLibRowHit
	// choice is the open second step (gameOptionsChoice*); mainCursor and
	// mainScroll put the highlight back on its row when it closes.
	choice     int
	mainCursor int
	mainScroll int
	// coverFiles are the unused covers the cleanup found, waiting for the
	// answer; coverWait is a scan started from this sheet.
	coverFiles []artCleanupFile
	coverBytes int64
	coverWait  bool
	// coverCard is the card's own picture Remove cover may also take
	// (gameCoverCardPicture); deleteFiles and deleteBytes are what Delete
	// game would remove (gameDeleteFiles). Both are read when the question
	// opens.
	coverCard   string
	deleteFiles []string
	deleteBytes int64
	// emulators are the ones that can play the game (findGameLaunchers), read
	// when the sheet opens for the Emulator row.
	emulators []emuEntry

	// systemLauncher is the console's game-launchers.json pin, if any.
	systemLauncher string
	// systemClearable: Left on the Emulator row may remove that pin
	// (emuConsoleLauncherClearable), worked out when it changes, not per frame.
	systemClearable bool
	// moreCores counts the cores X on the Emulator row would fetch for the
	// console (emumorecores.go), worked out on open.
	moreCores int
}

// gameArgsBuffer is what the on-screen keyboard edits for the arguments row.
// It is separate from gameOptsUI.opts so a dismissed keyboard changes nothing.
var gameArgsBuffer string

func gameOptionsRowName(row int) string {
	switch row {
	case gameOptionsRowTitle:
		return "Rename title"
	case gameOptionsRowFileName:
		return "Rename file"
	case gameOptionsRowHidden:
		return "Visibility"
	case gameOptionsRowSort:
		return "Library sort"
	case gameOptionsRowLayout:
		return "Library view"
	case gameOptionsRowConsoleLook:
		return "Console look"
	case gameOptionsRowShowHidden:
		return "Show hidden"
	case gameOptionsRowEmulator:
		return "Emulator"
	case gameOptionsRowDisc:
		return "Disc"
	case gameOptionsRowCPUProfile:
		return "CPU profile"
	case gameOptionsRowCeiling:
		return "CPU limit"
	case gameOptionsRowVideo:
		return "Video filter"
	case gameOptionsRowSpeed:
		return "Shader speed"
	case gameOptionsRowArgs:
		return "Extra arguments"
	case gameOptionsRowOpenIn:
		return "Open in…"
	case gameOptionsRowCheats:
		return "Cheats"
	case gameOptionsRowButtons:
		return "Buttons"
	case gameOptionsRowTurbo:
		return "Turbo"
	case gameOptionsRowDiagnose:
		return "Check why it won't start"
	case gameOptionsRowChooseCover:
		return "Choose cover"
	case gameOptionsRowRemoveCover:
		if gameOptionsNoCover() {
			return "Restore cover"
		}
		return "Remove cover"
	case gameOptionsRowCoverName:
		return "Name for covers"
	case gameOptionsRowCovers:
		return "Clean up covers"
	case gameOptionsRowReset:
		return "Reset game…"
	case gameOptionsRowDelete:
		return "Delete game…"
	}
	return ""
}

// gameOptionsNoCover reports the sheet's game's cover as removed
// (artwork_nocover.go). A cached folder lookup, cheap per frame.
func gameOptionsNoCover() bool {
	rom, ok := gameCoverROM(gameOptsUI.game)
	return ok && artworkNoCover(rom, libraryArtworkRoot())
}

// gameOptionsRowHint is the second line under a row label, explaining what the
// value does rather than repeating it.
func gameOptionsRowHint(row int) string {
	switch row {
	case gameOptionsRowTitle:
		return "Shown in the library; the ROM file keeps its name"
	case gameOptionsRowFileName:
		return "Renames the ROM on the card with its saves, states and covers"
	case gameOptionsRowHidden:
		return "Hide this game; Show hidden brings it back"
	case gameOptionsRowSort:
		return "Order of every console's games"
	case gameOptionsRowLayout:
		return "List, a 3D shelf, or a vertical cover column"
	case gameOptionsRowConsoleLook:
		return gameOptionsConsoleLookHint()
	case gameOptionsRowShowHidden:
		return "Reveal games hidden from the library"
	case gameOptionsRowEmulator:
		if gameOptsUI.opts.Emulator == "" && gameOptsUI.systemClearable {
			// Set for the whole console (the picker's X, emuchoices.go).
			left := "Left asks again"
			if emuPickAskModeNow() == emuAskNever {
				left = "Left goes back to Automatic"
			}
			return "Set for every " + gameOptsUI.game.System + " game · " + left + ", Right picks one for this game"
		}
		if gameOptsUI.moreCores > 0 {
			// The console's other cores, one press away (emumorecores.go).
			return fmt.Sprintf("Which emulator plays it · X gets %s for %s",
				pluralizeCount(gameOptsUI.moreCores, "more core", "more cores"), gameOptsUI.game.System)
		}
		if gameOptsUI.opts.Emulator == "" && gameOptsUI.systemLauncher == "" && len(gameOptsUI.emulators) > 1 &&
			!emuPickAsks(gameOptsUI.emulators) {
			// Ask which emulator: Never — this row is where the others are.
			return fmt.Sprintf("Starts in the best one · Right picks one of %d", len(gameOptsUI.emulators))
		}
		return "Which emulator plays it, or ask when several can"
	case gameOptionsRowDisc:
		return discOptionsHint()
	case gameOptionsRowCPUProfile:
		return "Battery saver, Balanced or Performance while this game runs"
	case gameOptionsRowCeiling:
		return "Clock ceiling for this game, over the profile's; restored on exit"
	case gameOptionsRowVideo:
		return "RetroArch's picture for this game; looks need shaders on the card"
	case gameOptionsRowSpeed:
		_, detail := shaderSpeedLabel(gameOptsUI.game.Path, videoFilterForGame(gameOptsUI.game.Path))
		return detail
	case gameOptionsRowArgs:
		return "Added after the ROM path, for launchers that read options"
	case gameOptionsRowOpenIn:
		return "Hand this game to its tool: profile, compatibility or emulator"
	case gameOptionsRowCheats:
		return "Cheats from libretro's database (RetroArch or NextUI), on for this game only"
	case gameOptionsRowButtons:
		return gameControlsSheetHint(gameOptsUI.game, false)
	case gameOptionsRowTurbo:
		return gameControlsSheetHint(gameOptsUI.game, true)
	case gameOptionsRowDiagnose:
		return "Missing BIOS, core or game files, and where to copy them"
	case gameOptionsRowChooseCover:
		return "The closest box art on the server or in the Imgs folder"
	case gameOptionsRowRemoveCover:
		if gameOptionsNoCover() {
			return "No picture is shown or fetched for it · A brings covers back"
		}
		return "Show no picture for this game, and fetch none again"
	case gameOptionsRowCoverName:
		return "The title box art is looked up by, if the file name differs"
	case gameOptionsRowCovers:
		return "Find downloaded box art no game in the library uses"
	case gameOptionsRowReset:
		return "Settings, core overrides, remaps, DIP switches, high scores"
	case gameOptionsRowDelete:
		return "Removes the game's files from the card; saves and states stay"
	}
	return ""
}

// gameOptionsRowValue is the right-hand value of a row.
func gameOptionsRowValue(row int, opts gameOptions) string {
	switch row {
	case gameOptionsRowTitle:
		if title := strings.TrimSpace(gameOptsUI.game.DisplayTitle); title != "" {
			return title
		}
		return "Default"
	case gameOptionsRowFileName:
		return gameRenameRowValue(gameOptsUI.game)
	case gameOptionsRowHidden:
		if gameOptsUI.game.Hidden {
			return "Hidden"
		}
		return "Visible"
	case gameOptionsRowSort:
		return librarySortLabel()
	case gameOptionsRowLayout:
		return libraryLayoutLabel()
	case gameOptionsRowConsoleLook:
		return libraryConsoleLookLabel()
	case gameOptionsRowShowHidden:
		if libraryShowHidden() {
			return "Shown"
		}
		return "Only visible"
	case gameOptionsRowEmulator:
		if opts.Emulator == "" && gameOptsUI.systemLauncher != "" {
			// game-launchers.json pins the console: no question is asked.
			// Named as the picker names it when it is one of the choices.
			name := filepath.Base(gameOptsUI.systemLauncher)
			for _, e := range gameOptsUI.emulators {
				if strings.EqualFold(filepath.Clean(e.Launch), filepath.Clean(gameOptsUI.systemLauncher)) {
					name = e.Name
					break
				}
			}
			return name + " (console setting)"
		}
		if opts.Emulator == "" {
			// NextUI starts it through its pak without asking (nextui.go).
			if pak := nextuiPakFor(gameOptsUI.game.Path); pak != "" {
				return "Automatic · " + nextuiPakLabel(pak)
			}
		}
		return gameEmulatorLabel(opts.Emulator, gameOptsUI.emulators)
	case gameOptionsRowDisc:
		return discOptionsValue()
	case gameOptionsRowCPUProfile:
		return gameCPUProfileLabel(opts.CPUProfile)
	case gameOptionsRowCeiling:
		return formatGameCeiling(opts.MaxFreqKHz)
	case gameOptionsRowVideo:
		return gameVideoFilterValue(opts.VideoFilter)
	case gameOptionsRowSpeed:
		label, _ := shaderSpeedLabel(gameOptsUI.game.Path, videoFilterForGame(gameOptsUI.game.Path))
		return label
	case gameOptionsRowArgs:
		if strings.TrimSpace(opts.Args) == "" {
			return "None"
		}
		return strings.TrimSpace(opts.Args)
	case gameOptionsRowOpenIn:
		return "→"
	case gameOptionsRowCheats:
		return cheatsOptionsValue(gameOptsUI.game.Path)
	case gameOptionsRowButtons:
		return gameButtonsSheetValue(gameOptsUI.game)
	case gameOptionsRowTurbo:
		return gameTurboSheetValue(gameOptsUI.game)
	case gameOptionsRowDiagnose:
		return "Check"
	case gameOptionsRowChooseCover:
		return "→"
	case gameOptionsRowRemoveCover:
		switch {
		case gameOptionsNoCover():
			return "Removed"
		case !artworkCoverPresent(gameOptsUI.game.Art):
			return "None"
		}
		return "Choose…"
	case gameOptionsRowCoverName:
		return artworkAliasRowValue(gameOptsUI.game)
	case gameOptionsRowCovers:
		if gameOptsUI.coverWait || artCleanupBusy() {
			return "Looking…"
		}
		return "→"
	case gameOptionsRowReset, gameOptionsRowDelete:
		return "Choose…"
	}
	return ""
}

// gameOptionsChoiceRows is the number of answers the open second step offers.
func gameOptionsChoiceRows() int {
	switch gameOptsUI.choice {
	case gameOptionsChoiceReset, gameOptionsChoiceDelete:
		return 3
	case gameOptionsChoiceCovers:
		return 2
	case gameOptionsChoiceRemoveCover:
		if gameOptsUI.coverCard != "" {
			return 3
		}
		return 2
	}
	return 0
}

// gameOptionsChoiceText is one answer's label and the line under it.
func gameOptionsChoiceText(row int) (string, string) {
	switch gameOptsUI.choice {
	case gameOptionsChoiceReset:
		switch row {
		case 0:
			return "Reset settings", "Options, core overrides, remaps, DIP switches, high scores"
		case 1:
			return "Reset settings and delete save states", "Also its save states, their screenshots and arcade NVRAM"
		case 2:
			return "Cancel", "Keep everything as it is"
		}
	case gameOptionsChoiceCovers:
		switch row {
		case 0:
			return "Move to the trash", artCleanupSummary(len(gameOptsUI.coverFiles), gameOptsUI.coverBytes)
		case 1:
			return "Cancel", "Keep every cover"
		}
	case gameOptionsChoiceRemoveCover:
		if row == gameOptionsChoiceRows()-1 {
			return "Cancel", "Keep the cover"
		}
		switch row {
		case 0:
			return "Remove the cover", "No picture and none fetched again; JukaHub's copy goes to the trash"
		case 1:
			return "Also remove the card's picture",
				gameCoverCardLabel(gameOptsUI.coverCard) + " goes to the trash too, so the firmware shows none"
		}
	case gameOptionsChoiceDelete:
		size := gameResetFilesWord(len(gameOptsUI.deleteFiles)) + ", " + humanBytes(gameOptsUI.deleteBytes)
		switch row {
		case 0:
			return "Move to the trash", size + " to reset-trash; saves and states stay"
		case 1:
			return "Delete for good", "Frees " + humanBytes(gameOptsUI.deleteBytes) + " now; saves and states stay"
		case 2:
			return "Cancel", "Keep the game"
		}
	}
	return "", ""
}

// gameCoverCardLabel names a card picture by its folder and file
// ("Imgs/Tetris.png").
func gameCoverCardLabel(path string) string {
	return filepath.Base(filepath.Dir(path)) + "/" + filepath.Base(path)
}

// gameOptionsChoiceNote is the line under the answers.
func gameOptionsChoiceNote() string {
	switch gameOptsUI.choice {
	case gameOptionsChoiceReset:
		return "The ROM, battery saves and playtime stay; removed files go to reset-trash."
	case gameOptionsChoiceCovers:
		return "Only JukaHub's own game-art folder; the trash keeps them for a while."
	case gameOptionsChoiceRemoveCover:
		return "Restore cover or Choose cover brings a picture back."
	case gameOptionsChoiceDelete:
		return "The trash keeps the " + itoa(resetTrashKeepDeleted) + " newest deleted games; Reset game removes saves."
	}
	return ""
}

// openGameOptionsChoice replaces the rows with a second step's answers.
func openGameOptionsChoice(choice int) {
	gameOptsUI.mainCursor = gameOptsUI.cursor
	gameOptsUI.mainScroll = gameOptsUI.scroll
	gameOptsUI.choice = choice
	// Cancel, the last answer, is the highlighted one: a reset or a removal
	// opens on the safe choice (confirmfocus.go), so an A pressed by habit
	// changes nothing.
	gameOptsUI.cursor = gameOptionsChoiceRows() - 1
	if gameOptsUI.cursor < 0 {
		gameOptsUI.cursor = 0
	}
	gameOptsUI.scroll = 0
	PlayActivateSound()
}

// closeGameOptionsChoice goes back to the rows.
func closeGameOptionsChoice(sound bool) {
	gameOptsUI.choice = gameOptionsChoiceNone
	gameOptsUI.cursor = gameOptsUI.mainCursor
	gameOptsUI.scroll = gameOptsUI.mainScroll
	gameOptsUI.coverFiles = nil
	gameOptsUI.coverBytes = 0
	gameOptsUI.coverCard = ""
	gameOptsUI.deleteFiles = nil
	gameOptsUI.deleteBytes = 0
	if sound {
		PlayBackSound()
	}
}

// activateGameOptionsChoice runs the highlighted answer.
func activateGameOptionsChoice() {
	choice, row, rows := gameOptsUI.choice, gameOptsUI.cursor, gameOptionsChoiceRows()
	files := gameOptsUI.coverFiles
	card, doomed := gameOptsUI.coverCard, gameOptsUI.deleteFiles
	closeGameOptionsChoice(false)
	switch choice {
	case gameOptionsChoiceRemoveCover:
		if row == rows-1 {
			PlayBackSound()
			return
		}
		PlayActivateSound()
		if row == 0 {
			card = ""
		}
		runGameCoverRemove(gameOptsUI.game, card) // artwork_nocover.go
	case gameOptionsChoiceDelete:
		if row > 1 {
			PlayBackSound()
			return
		}
		PlayActivateSound()
		runGameDelete(gameOptsUI.game, doomed, row == 1) // game_delete.go
	case gameOptionsChoiceReset:
		switch row {
		case 0:
			PlayActivateSound()
			runGameReset(gameOptsUI.game, false)
		case 1:
			PlayActivateSound()
			runGameReset(gameOptsUI.game, true)
		default:
			PlayBackSound()
		}
	case gameOptionsChoiceCovers:
		if row == 0 {
			PlayActivateSound()
			runArtCleanup(files)
			return
		}
		PlayBackSound()
	}
}

// gameOptionsPollCovers picks up a cover scan this sheet started and opens its
// question, or says there is nothing to clean. Render thread.
func gameOptionsPollCovers() {
	if !gameOptsUI.coverWait {
		return
	}
	files, bytes, why, ok := artCleanupTake()
	if !ok {
		return
	}
	gameOptsUI.coverWait = false
	switch {
	case why != "":
		ShowToast(why, ToastKindInfo)
	case len(files) == 0:
		ShowToast("No unused covers · every cover belongs to a game", ToastKindSuccess)
	case !gameOptsUI.open || gameOptsUI.choice != gameOptionsChoiceNone:
		ShowToast(artCleanupSummary(len(files), bytes)+" · Clean up covers moves them", ToastKindInfo)
	default:
		openGameOptionsChoice(gameOptionsChoiceCovers)
		gameOptsUI.coverFiles = files
		gameOptsUI.coverBytes = bytes
	}
}

// openGameOptions opens the sheet for the highlighted game. It returns false
// when the library has no game to edit, so the caller can fall through.
func openGameOptions() bool {
	if !gameLib.open || gameOptsUI.open {
		return false
	}
	// The sheet edits one game, and the consoles grid highlights a console,
	// not a game: the options open from a console's page.
	if gameLib.focus == 0 {
		ShowToast("Open a console first — Options are for one of its games", ToastKindInfo)
		return true
	}
	game, ok := selectedLibraryGame()
	if !ok {
		ShowToast("No game selected", ToastKindInfo)
		return true
	}
	if isBuiltInLibraryGame(game) {
		ShowToast(game.Title+" has no emulator options", ToastKindInfo)
		return true
	}
	// The sheet edits the highlighted title, and its header names the game.
	if game.ContentKey == "" {
		libraryDecorateGame(&game)
	}
	cheatsClose(false) // a Cheats sheet left from a sheet closed elsewhere (cheats_ui.go)
	artChooseClose(false)
	gameOptsUI.open = true
	gameOptsUI.game = game
	gameOptsUI.cursor = 0
	gameOptsUI.scroll = 0
	gameOptsUI.visible = 0
	gameOptsUI.opts = gameOptionsFor(game.Path)
	discOptionsOpen(game) // the Disc row's discs and start disc (discpage.go)
	gameOptsUI.rowHits = gameOptsUI.rowHits[:0]
	gameOptsUI.choice = gameOptionsChoiceNone
	gameOptsUI.coverFiles = nil
	gameOptsUI.coverWait = false
	gameOptsUI.coverCard, gameOptsUI.deleteFiles, gameOptsUI.deleteBytes = "", nil, 0
	gameOptsUI.emulators = nil
	gameOptsUI.systemLauncher = ""
	if game.System != androidLibrarySystem && game.System != html5LibrarySystem && game.System != portsLibrarySystem {
		// On NextUI the game's own pak leads the ladder (nextui.go).
		entries := emulatorEntries()
		launchers := findGameLaunchers(game.System, entries)
		// A Color cartridge on the Game Boy shelf is offered the Game Boy
		// Color emulators, as its start is (gbcsort.go).
		if nextuiPakFor(game.Path) == "" && gbcLaunchAsColor(game.System, game.Path) {
			if color := findGameLaunchers(gbcLibrarySystem, entries); len(color) > 0 {
				launchers = color
			}
		}
		gameOptsUI.emulators = nextuiWithPakFirst(game.Path, launchers)
		gameOptsUI.systemLauncher = configuredGameLauncher(game.System, loadGameLauncherOverrides(gameLauncherOverridesPath()))
	}
	gameOptsUI.systemClearable = emuConsoleLauncherClearable(gameOptsUI.systemLauncher)
	gameOptsUI.moreCores = emuMoreCoresAvailableFor(game.System, len(gameOptsUI.emulators) > 0)
	PlayActivateSound()
	return true
}

func closeGameOptions() {
	if !gameOptsUI.open {
		return
	}
	gameOptsUI.open = false
	gameOptsUI.rowHits = gameOptsUI.rowHits[:0]
	gameOptsUI.choice = gameOptionsChoiceNone
	gameOptsUI.coverFiles = nil
	gameOptsUI.coverCard, gameOptsUI.deleteFiles, gameOptsUI.deleteBytes = "", nil, 0
	cheatsClose(false)
	artChooseClose(false)
	PlayBackSound()
}

// gameOptionsRows is how many rows the sheet shows now: its options, or the
// open second step's answers.
func gameOptionsRows() int {
	if n := gameOptionsChoiceRows(); n > 0 {
		return n
	}
	if gameOptsUI.game.System == html5LibrarySystem {
		return len(gameOptionsHTML5Rows)
	}
	if gameOptsUI.game.System == portsLibrarySystem {
		return len(gameOptionsPortRows)
	}
	return gameOptionsDefaultRows() // discpage.go: the Disc row is for multi-disc games
}

// gameOptionsHTML5Rows are a browser game's rows: title, visibility, sort and
// view. Its files belong to its folder (no Rename file or Delete game), and
// emulator launch options and reset controls do not apply to it.
var gameOptionsHTML5Rows = [...]int{
	gameOptionsRowTitle, gameOptionsRowHidden, gameOptionsRowSort, gameOptionsRowLayout,
	gameOptionsRowConsoleLook, gameOptionsRowShowHidden,
}

// gameOptionsPortRows are the rows a PortMaster port shows: the library's own,
// the CPU profile and limit its launch applies (ports.go startPortScript →
// beginLaunchPowerProfile), its own checks (gamediag.go → portWhyRun) and the
// covers cleanup. Emulator, video filter, shader speed, extra arguments,
// Open in…, cheats and reset are for emulated games: a port's script starts
// its own engine and reads none of them. Its files belong to its folder, so
// Rename file and Delete game are not offered; Remove cover is.
var gameOptionsPortRows = [...]int{
	gameOptionsRowTitle, gameOptionsRowHidden, gameOptionsRowSort, gameOptionsRowLayout,
	gameOptionsRowConsoleLook, gameOptionsRowShowHidden, gameOptionsRowCPUProfile, gameOptionsRowCeiling,
	gameOptionsRowDiagnose, gameOptionsRowRemoveCover, gameOptionsRowCovers,
}

// gameOptionsRowAt is the row shown at position pos: the cursor and the hit
// boxes count positions, which are the rows themselves except for a port and
// a browser game. -1 is no row.
func gameOptionsRowAt(pos int) int {
	var rows []int
	switch gameOptsUI.game.System {
	case portsLibrarySystem:
		rows = gameOptionsPortRows[:]
	case html5LibrarySystem:
		rows = gameOptionsHTML5Rows[:]
	default:
		return gameOptionsDefaultRowAt(pos) // discpage.go
	}
	if pos >= 0 && pos < len(rows) {
		return rows[pos]
	}
	return -1
}

// gameOptionsMove keeps the highlight on a real row.
func gameOptionsMove(delta int) {
	if delta == 0 {
		return
	}
	rows := gameOptionsRows()
	gameOptsUI.cursor += delta
	if gameOptsUI.cursor < 0 {
		gameOptsUI.cursor = 0
	}
	if gameOptsUI.cursor >= rows {
		gameOptsUI.cursor = rows - 1
	}
	if gameOptsUI.visible > 0 {
		if gameOptsUI.cursor < gameOptsUI.scroll {
			gameOptsUI.scroll = gameOptsUI.cursor
		}
		if gameOptsUI.cursor >= gameOptsUI.scroll+gameOptsUI.visible {
			gameOptsUI.scroll = gameOptsUI.cursor - gameOptsUI.visible + 1
		}
	}
	PlayNavSound()
}

// adjustGameOptions changes the highlighted value with Left/Right.
func adjustGameOptions(delta int) {
	if delta == 0 || gameOptsUI.choice != gameOptionsChoiceNone {
		return
	}
	switch gameOptionsRowAt(gameOptsUI.cursor) {
	case gameOptionsRowDisc:
		if !discOptionsStep(delta) {
			return
		}
	case gameOptionsRowEmulator:
		if delta < 0 && gameOptsUI.opts.Emulator == "" && gameOptsUI.systemClearable {
			// Left of the console's own choice (the picker's X) takes it back:
			// the console's games ask again (emuchoices.go).
			system := gameOptsUI.game.System
			if err := emuConsoleLauncherSet(system, ""); err != nil {
				ShowToast(err.Error(), ToastKindWarning)
				return
			}
			gameOptsUI.systemLauncher = configuredGameLauncher(system, loadGameLauncherOverrides(gameLauncherOverridesPath()))
			gameOptsUI.systemClearable = emuConsoleLauncherClearable(gameOptsUI.systemLauncher)
			if emuPickAskModeNow() == emuAskNever {
				// Ask which emulator is Never: nothing asks (emupicker.go).
				ShowToast(system+" games are back on Automatic", ToastKindInfo)
			} else {
				ShowToast(system+" games ask which emulator again", ToastKindInfo)
			}
			PlayNavSound()
			return
		}
		next := cycleGameEmulator(gameOptsUI.opts.Emulator, gameOptsUI.emulators, delta)
		if next == gameOptsUI.opts.Emulator {
			return
		}
		gameOptsUI.opts = updateGameOptions(gameOptsUI.game.Path, func(o gameOptions) gameOptions {
			o.Emulator = next
			return o
		})
	case gameOptionsRowCeiling:
		next := cycleGameCeiling(gameOptsUI.opts.MaxFreqKHz, delta)
		if next == gameOptsUI.opts.MaxFreqKHz {
			return
		}
		gameOptsUI.opts = updateGameOptions(gameOptsUI.game.Path, func(o gameOptions) gameOptions {
			o.MaxFreqKHz = next
			return o
		})
	case gameOptionsRowCPUProfile:
		next := cycleGameCPUProfile(gameOptsUI.opts.CPUProfile, delta)
		if next == gameOptsUI.opts.CPUProfile {
			return
		}
		gameOptsUI.opts = updateGameOptions(gameOptsUI.game.Path, func(o gameOptions) gameOptions {
			o.CPUProfile = next
			return o
		})
	case gameOptionsRowVideo:
		next := cycleGameVideoFilter(gameOptsUI.opts.VideoFilter, delta)
		if next == gameOptsUI.opts.VideoFilter {
			return
		}
		gameOptsUI.opts = updateGameOptions(gameOptsUI.game.Path, func(o gameOptions) gameOptions {
			o.VideoFilter = next
			return o
		})
	case gameOptionsRowButtons, gameOptionsRowTurbo:
		// Left/Right stop at the ends (racontrols_ui.go).
		if !gameControlsSheetStep(gameOptsUI.game, gameOptionsRowAt(gameOptsUI.cursor) == gameOptionsRowTurbo, delta, false) {
			return
		}
	case gameOptionsRowSort:
		cycleLibrarySort(delta)
		refreshLibraryView()
		ShowToast("Sorted by "+strings.ToLower(librarySortLabel()), ToastKindInfo)
	case gameOptionsRowLayout:
		cycleLibraryLayout(delta)
		gameLib.scroll = 0
		ShowToast("Library view: "+libraryLayoutLabel(), ToastKindInfo)
	case gameOptionsRowConsoleLook:
		stepLibraryConsoleLook(delta)
	case gameOptionsRowShowHidden:
		setLibraryShowHidden(delta > 0)
		refreshLibraryView()
		if libraryShowHidden() {
			ShowToast("Hidden games shown", ToastKindInfo)
		} else {
			ShowToast("Hidden games concealed", ToastKindInfo)
		}
	case gameOptionsRowHidden:
		toggleGameHidden()
	default:
		return
	}
	PlayNavSound()
}

// activateGameOptionsRow runs the highlighted row.
func activateGameOptionsRow() {
	if gameOptsUI.choice != gameOptionsChoiceNone {
		activateGameOptionsChoice()
		return
	}
	switch gameOptionsRowAt(gameOptsUI.cursor) {
	case gameOptionsRowTitle:
		vkOpenGameTitle()
	case gameOptionsRowFileName:
		vkOpenGameFileName() // game_rename.go
	case gameOptionsRowHidden:
		toggleGameHidden()
	case gameOptionsRowSort:
		cycleLibrarySort(1)
		refreshLibraryView()
		ShowToast("Sorted by "+strings.ToLower(librarySortLabel()), ToastKindInfo)
	case gameOptionsRowLayout:
		cycleLibraryLayout(1)
		gameLib.scroll = 0
		ShowToast("Library view: "+libraryLayoutLabel(), ToastKindInfo)
	case gameOptionsRowConsoleLook:
		stepLibraryConsoleLook(1)
	case gameOptionsRowShowHidden:
		setLibraryShowHidden(!libraryShowHidden())
		refreshLibraryView()
		if libraryShowHidden() {
			ShowToast("Hidden games shown", ToastKindInfo)
		} else {
			ShowToast("Hidden games concealed", ToastKindInfo)
		}
	case gameOptionsRowCeiling, gameOptionsRowCPUProfile, gameOptionsRowVideo, gameOptionsRowDisc:
		adjustGameOptions(1)
	case gameOptionsRowEmulator:
		// A walks the list and wraps back to "Ask each time" at its end, so
		// one button reaches every choice.
		if gameOptsUI.opts.Emulator != "" &&
			cycleGameEmulator(gameOptsUI.opts.Emulator, gameOptsUI.emulators, 1) == gameOptsUI.opts.Emulator {
			gameOptsUI.opts = updateGameOptions(gameOptsUI.game.Path, func(o gameOptions) gameOptions {
				o.Emulator = ""
				return o
			})
			PlayNavSound()
			return
		}
		adjustGameOptions(1)
	case gameOptionsRowArgs:
		vkOpenGameArgs()
	case gameOptionsRowSpeed:
		label, detail := shaderSpeedLabel(gameOptsUI.game.Path, videoFilterForGame(gameOptsUI.game.Path))
		ShowToast("Shader speed: "+label+" · "+detail, ToastKindInfo)
	case gameOptionsRowOpenIn:
		openGameLibraryHandoffFromSheet()
	case gameOptionsRowCheats:
		openCheatsSheet(gameOptsUI.game)
	case gameOptionsRowButtons, gameOptionsRowTurbo:
		// A goes round, so one button reaches every choice (racontrols_ui.go).
		if gameControlsSheetStep(gameOptsUI.game, gameOptionsRowAt(gameOptsUI.cursor) == gameOptionsRowTurbo, 1, true) {
			PlayNavSound()
		}
	case gameOptionsRowDiagnose:
		// The answer opens as the launch-failure card (gamediag.go).
		game := gameOptsUI.game
		closeGameOptions()
		gameDiagRunManual(game)
	case gameOptionsRowChooseCover:
		openArtChoose() // artwork_choose.go
	case gameOptionsRowRemoveCover:
		gameOptionsRemoveCover()
	case gameOptionsRowCoverName:
		vkOpenArtworkAlias()
	case gameOptionsRowCovers:
		if gameOptsUI.coverWait || artCleanupBusy() {
			ShowToast("Still looking for unused covers…", ToastKindInfo)
			return
		}
		PlayActivateSound()
		gameOptsUI.coverWait = startArtCleanupScan()
	case gameOptionsRowReset:
		if gameOptsUI.game.System == androidLibrarySystem {
			// An Android game keeps its data in the app runtime, not in
			// RetroArch's folders: only the sheet's own record goes.
			resetGameOptions()
			return
		}
		openGameOptionsChoice(gameOptionsChoiceReset)
	case gameOptionsRowDelete:
		gameDeleteAsk(gameOptsUI.game) // game_delete.go: counts, then asks
	}
}

// gameOptionsRemoveCover is A on Remove cover: the question, or for a cover
// already removed, Restore cover (artwork_nocover.go).
func gameOptionsRemoveCover() {
	game := gameOptsUI.game
	rom, ok := gameCoverROM(game)
	if !ok {
		ShowToast("This game's picture comes from its app, not from a cover", ToastKindInfo)
		return
	}
	if artworkNoCover(rom, libraryArtworkRoot()) {
		runGameCoverRestore(game)
		return
	}
	// Set before the question opens: it adds an answer, and the question opens
	// on its last one, Cancel.
	gameOptsUI.coverCard = gameCoverCardPicture(rom)
	openGameOptionsChoice(gameOptionsChoiceRemoveCover)
}

// applyGameArgs commits the keyboard buffer to the highlighted game.
func applyGameArgs() {
	if !gameOptsUI.open {
		return
	}
	gameOptsUI.opts = updateGameOptions(gameOptsUI.game.Path, func(o gameOptions) gameOptions {
		o.Args = gameArgsBuffer
		return o
	})
	if gameOptsUI.opts.Args == "" {
		ShowToast("No extra arguments for "+gameOptsUI.game.Title, ToastKindInfo)
		return
	}
	ShowToast("Arguments saved for "+gameOptsUI.game.Title, ToastKindSuccess)
}

// resetGameOptions drops both overrides for the highlighted game.
func resetGameOptions() {
	if gameOptsUI.opts.empty() {
		ShowToast("This game already uses the defaults.", ToastKindInfo)
		return
	}
	gameOptsUI.opts = updateGameOptions(gameOptsUI.game.Path, func(gameOptions) gameOptions {
		return gameOptions{}
	})
	ShowToast("Defaults restored for "+gameOptsUI.game.Title, ToastKindSuccess)
}

// handleGameOptionsKey consumes every key while the sheet is open, so the list
// behind it neither scrolls nor launches anything.
func handleGameOptionsKey(e *sdl.KeyboardEvent) bool {
	if cheatsSheetOpen() && gameOptsUI.open {
		return handleCheatsKey(e) // the Cheats sheet sits on this one (cheats_ui.go)
	}
	if artChooseIsOpen() && gameOptsUI.open {
		return handleArtChooseKey(e) // so does Choose cover (artwork_choose.go)
	}
	if e == nil || e.Type != sdl.KEYDOWN || !gameOptsUI.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		gameOptionsMove(-1)
	case sdl.K_DOWN:
		gameOptionsMove(1)
	case sdl.K_LEFT:
		adjustGameOptions(-1)
	case sdl.K_RIGHT:
		adjustGameOptions(1)
	case sdl.K_RETURN, sdl.K_SPACE:
		activateGameOptionsRow()
	case sdl.K_x:
		gameOptionsMoreCores()
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		gameOptionsBack()
	}
	return true
}

// gameOptionsMoreCoresHere reports whether X fetches the console's other
// cores now: the highlight is on the Emulator row and there are some.
func gameOptionsMoreCoresHere() bool {
	return gameOptsUI.choice == gameOptionsChoiceNone && gameOptsUI.moreCores > 0 &&
		gameOptionsRowAt(gameOptsUI.cursor) == gameOptionsRowEmulator
}

// gameOptionsMoreCores is X on the Emulator row: the sheet closes and the
// console's other cores download (emumorecores.go). It reports whether it
// acted.
func gameOptionsMoreCores() bool {
	if !gameOptsUI.open || !gameOptionsMoreCoresHere() {
		return false
	}
	system := gameOptsUI.game.System
	closeGameOptions()
	emuMoreCoresStart(system)
	return true
}

// gameOptionsBack is B: out of a second step back to the rows, else the sheet
// closes.
func gameOptionsBack() {
	if gameOptsUI.choice != gameOptionsChoiceNone {
		closeGameOptionsChoice(true)
		return
	}
	closeGameOptions()
}

func ctrlGameOptions(e *sdl.ControllerButtonEvent) bool {
	if cheatsSheetOpen() && gameOptsUI.open {
		return handleCheatsController(e)
	}
	if artChooseIsOpen() && gameOptsUI.open {
		return handleArtChooseController(e)
	}
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN || !gameOptsUI.open {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		gameOptionsMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		gameOptionsMove(1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		adjustGameOptions(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		adjustGameOptions(1)
	case sdl.CONTROLLER_BUTTON_A:
		activateGameOptionsRow()
	case sdl.CONTROLLER_BUTTON_X:
		return gameOptionsMoreCores()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_START:
		gameOptionsBack()
	default:
		return false
	}
	return true
}

// handleGameOptionsMouse clicks a row or dismisses the sheet. Clicks are
// consumed even when they land on the card, so the library does not act on the
// same tap.
func handleGameOptionsMouse(mx, my int32) bool {
	if !gameOptsUI.open {
		return false
	}
	if cheatsSheetOpen() {
		return handleCheatsMouse(mx, my)
	}
	if artChooseIsOpen() {
		return handleArtChooseMouse(mx, my)
	}
	for _, hit := range gameOptsUI.rowHits {
		if pointInRect(mx, my, hit.rect) {
			gameOptsUI.cursor = hit.index
			activateGameOptionsRow()
			return true
		}
	}
	if gameOptsUI.panel.W > 0 && pointInRect(mx, my, gameOptsUI.panel) {
		return true
	}
	gameOptionsBack()
	return true
}

// renderGameOptionsPanel draws the sheet over the library.
func renderGameOptionsPanel(renderer *sdl.Renderer, config *Config) {
	if !gameOptsUI.open || renderer == nil {
		return
	}
	defer renderCheatsSheet(renderer, config) // drawn over the sheet when open (cheats_ui.go)
	defer renderArtChooseSheet(renderer, config)
	gameOptionsPollCovers()
	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}
	smallH := int32(font.Height())
	medH := int32(medium.Height())

	const pad = OverlayPadX
	// Measured from the type: at the shipped 24px roles the fixed 58px row put
	// each hint 12px into the row below (8 above, 2 between, 6 under, 8 gap).
	rowH := max32(58, medH+smallH+24)
	// The title band is the shared page header, built before the card is sized so
	// the panel and the bar drawn into it measure the same height.
	subtitle := gameOptsUI.game.Label()
	if system := strings.TrimSpace(gameOptsUI.game.System); system != "" {
		subtitle += " · " + system
	}
	title := "Game options"
	switch gameOptsUI.choice {
	case gameOptionsChoiceReset:
		title = "Reset game"
	case gameOptionsChoiceCovers:
		title = "Clean up covers"
		subtitle = "Box art no game in the library uses"
	case gameOptionsChoiceRemoveCover:
		title = "Remove cover"
	case gameOptionsChoiceDelete:
		title = "Delete game"
		subtitle = filepath.Base(gameOptsUI.game.Path)
	}
	header := UIPanelHeader(title, subtitle, "gamepad")
	rows := gameOptionsRows()
	choosing := gameOptsUI.choice != gameOptionsChoiceNone
	headerH := UIPanelHeaderBand(config, header, 14)
	noteH := int32(12) + smallH
	footerH := int32(14) + smallH
	panelW := int32(620)
	if panelW > screenWidth-64 {
		panelW = screenWidth - 64
	}
	panelH := headerH + rowH*int32(rows) + noteH + footerH
	if panelH > screenHeight-24 {
		panelH = screenHeight - 24
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	gameOptsUI.panel = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	// A second dim layer separates the sheet from the list it sits on.
	card := sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, panelX, panelY, panelW)

	header = overlayHeaderOnCard(header, card)
	UIPageHeaderDraw(renderer, config, header)

	bodyY := panelY + headerH
	innerW := panelW - 2*pad
	// The panel is capped to the screen, so a short display shows a window of
	// the rows rather than drawing the last ones through the card edge.
	bodyH := panelH - headerH - noteH - footerH
	visible := int(bodyH / rowH)
	if visible < 1 {
		visible = 1
	}
	if visible > rows {
		visible = rows
	}
	if gameOptsUI.cursor >= rows {
		gameOptsUI.cursor = rows - 1
	}
	if gameOptsUI.scroll > rows-visible {
		gameOptsUI.scroll = rows - visible
	}
	if gameOptsUI.scroll < 0 {
		gameOptsUI.scroll = 0
	}
	if gameOptsUI.cursor < gameOptsUI.scroll {
		gameOptsUI.scroll = gameOptsUI.cursor
	}
	if gameOptsUI.cursor >= gameOptsUI.scroll+visible {
		gameOptsUI.scroll = gameOptsUI.cursor - visible + 1
	}
	gameOptsUI.visible = visible
	gameOptsUI.rowHits = gameOptsUI.rowHits[:0]
	focusColor := installHighlight()
	for i := 0; i < visible; i++ {
		idx := gameOptsUI.scroll + i
		rect := sdl.Rect{X: panelX + pad, Y: bodyY + int32(i)*rowH, W: innerW, H: rowH - 8}
		gameOptsUI.rowHits = append(gameOptsUI.rowHits, gameLibRowHit{index: idx, rect: rect})
		selected := idx == gameOptsUI.cursor
		bg := WithAlpha(ColorSurfaceRow, 165)
		if selected {
			bg = WithAlpha(focusColor, 46)
		}
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, bg)
		if selected {
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, 2, focusColor)
		}

		if choosing {
			// A second step's answer: its label and what it does, full width.
			name, line := gameOptionsChoiceText(idx)
			renderText(renderer, config, medium, ellipsize(medium, name, rect.W-28), ColorTextPrimary(),
				rect.X+14, rect.Y+8)
			renderText(renderer, config, font, ellipsize(font, line, rect.W-28), ColorTextTertiary(),
				rect.X+14, rect.Y+8+medH+2)
			continue
		}
		// From here idx is the row shown at this position (a port skips the
		// emulator rows, gameOptionsRowAt).
		idx = gameOptionsRowAt(idx)
		renderText(renderer, config, medium, gameOptionsRowName(idx), ColorTextPrimary(),
			rect.X+14, rect.Y+8)
		// Leave room for the right-aligned value instead of letting a long hint
		// run underneath it.
		renderText(renderer, config, font, ellipsize(font, gameOptionsRowHint(idx), rect.W-28-170), ColorTextTertiary(),
			rect.X+14, rect.Y+8+medH+2)

		value := gameOptionsRowValue(idx, gameOptsUI.opts)
		valueColor := ColorTextSecondary()
		if idx == gameOptionsRowTitle && strings.TrimSpace(gameOptsUI.game.DisplayTitle) != "" {
			valueColor = focusColor
		}
		if idx == gameOptionsRowHidden && gameOptsUI.game.Hidden {
			valueColor = focusColor
		}
		if idx == gameOptionsRowRemoveCover && gameOptionsNoCover() {
			valueColor = focusColor
		}
		if idx == gameOptionsRowCeiling && gameOptsUI.opts.MaxFreqKHz > 0 {
			valueColor = focusColor
		}
		if (idx == gameOptionsRowCPUProfile && gameOptsUI.opts.CPUProfile != "") ||
			(idx == gameOptionsRowVideo && gameOptsUI.opts.VideoFilter != "") ||
			(idx == gameOptionsRowEmulator && gameOptsUI.opts.Emulator != "") {
			valueColor = focusColor
		}
		if idx == gameOptionsRowArgs && strings.TrimSpace(gameOptsUI.opts.Args) != "" {
			valueColor = focusColor
		}
		if idx == gameOptionsRowCheats && cheatsEnabledCount(gameOptsUI.game.Path) > 0 {
			valueColor = focusColor
		}
		buttonsRow := idx == gameOptionsRowButtons || idx == gameOptionsRowTurbo
		if buttonsRow && gameControlsSheetOwn(gameOptsUI.game, idx == gameOptionsRowTurbo) {
			valueColor = focusColor
		}
		// Ladders and toggles show which way they move; a static value would
		// leave Left/Right undiscoverable.
		if selected && (buttonsRow || idx == gameOptionsRowCeiling || idx == gameOptionsRowCPUProfile ||
			idx == gameOptionsRowVideo || idx == gameOptionsRowSort || idx == gameOptionsRowLayout ||
			idx == gameOptionsRowConsoleLook || idx == gameOptionsRowShowHidden || idx == gameOptionsRowHidden ||
			idx == gameOptionsRowEmulator) {
			value = "‹  " + value + "  ›"
		}
		value = ellipsize(font, value, innerW/2)
		vw, _, _ := font.SizeUTF8(value)
		renderText(renderer, config, font, value, valueColor,
			rect.X+rect.W-14-int32(vw), rect.Y+(rect.H-smallH)/2)
	}

	noteY := bodyY + rowH*int32(visible) + 4
	note := "Display, visibility, sort and view are saved; launch rows apply to this game only."
	if gameOptsUI.game.System == html5LibrarySystem {
		note = "Display, visibility, sort and view are saved for browser games."
	}
	if choosing {
		note = gameOptionsChoiceNote()
	}
	renderText(renderer, config, font, ellipsize(font, note, innerW),
		ColorTextTertiary(), panelX+pad, noteY)

	hint := hintLine(FooterHint{"D-Pad", "Row"}, FooterHint{"A", "Change"},
		FooterHint{"B", "Back"})
	if gameOptionsMoreCoresHere() {
		hint = hintLine(FooterHint{"D-Pad", "Row"}, FooterHint{"A", "Change"},
			FooterHint{"X", "More cores"}, FooterHint{"B", "Back"})
	}
	if choosing {
		hint = hintLine(FooterHint{"D-Pad", "Row"}, FooterHint{"A", "Choose"},
			FooterHint{"B", "Cancel"})
	}
	renderText(renderer, config, font, ellipsize(font, hint, innerW), ColorTextSecondary(),
		panelX+pad, panelY+panelH-footerH+2)
}
