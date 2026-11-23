package main

// gamemanual_ui.go — "Manual & guide" on the game page: the game's controls,
// every manual and guide found for it (gamemanual.go), and the rows that add
// one (gamemanual_add.go).
//
// The page gets a "Manual" chip when documents exist and a More row for every
// library game (gamepage_more.go). The list opens over the page: Controls
// first — the chart of the game's control layout when it has one
// (ctrlLayoutForGame, ctrllayout_*.go), else the console's Buttons and Turbo
// choice in words (racontrols.go) and the handheld's own chords — then each
// document, then "Add a manual or guide" and "Add a guide link". A opens a
// document in JukaHub's own viewer: a PDF or EPUB in the Reader, a text or
// Markdown guide in the text viewer, a scanned manual's pictures in the
// picture viewer (L2/R2 turn its pages), a link in the Browser. With KOReader
// on the card a PDF or EPUB follows "Open books with", and Y opens it in the
// other reader (koreader.go).
//
// Those viewers are drawn under the Games library (it is an overlay of every
// screen, main.go), so the library is parked while one is up, the way the
// handoff to the emulator panels parks it (handoff.go): gameLib.open goes
// false with the page and this list still open behind it, and a check run
// once a frame (manAwayCheck, through tbxPost) brings it back as soon as the
// viewer closes or the Reader's or Browser's screen is left — Back lands on
// the list it was opened from.
//
// The Reader only opens files inside Files' root (resolveExplorerEntry), and a
// manual may sit elsewhere (another drive on a PC, JukaHub's data folder), so
// for the one call that opens it Files' location is the file's drive, as
// chatShowInFiles does to show a file; it is put back at once.

import (
	"path/filepath"
	"strconv"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// manRowKind is what a row of the list is.
type manRowKind int

const (
	manRowControls manRowKind = iota
	manRowDoc
	manRowAddFile
	manRowAddLink
)

// manRow is one row of the list.
type manRow struct {
	kind   manRowKind
	doc    int
	label  string
	detail string
	icon   string
}

// manUI is the list over the game page. Main thread only.
var manUI struct {
	open    bool
	game    libraryGame
	docs    []manDoc
	loading bool
	rows    []manRow
	cursor  int
	first   int
	hits    []gameLibRowHit
	card    sdl.Rect
	// armed is the row X pressed once (a second X removes it), until armedAt
	// is older than feArmWindow.
	armed   int
	armedAt time.Time
	// The Controls view, open over the list.
	controls  bool
	ctrlTitle string
	ctrlNote  string
	ctrlLines []ctrlLayoutLine
	ctrlFirst int
	ctrlRows  int
}

func init() {
	gamePageRegisterOpened(func(game libraryGame) {
		manRefresh(game, false, func([]manDoc) { manDocsLanded(game.Path) })
	})
	gamePageRegisterStatus(gamePageStatus{ID: "manual", Order: 30, Chip: func(game libraryGame) (string, int, bool) {
		docs, known := manDocsFor(game.Path)
		if !known || len(docs) == 0 {
			return "", 0, false
		}
		return "Manual", gamePageToneNeutral, true
	}})
	gamePageRegisterExtra(gamePageExtra{ID: "manual", Order: 20, Icon: "pdf", Row: manMoreRow, Open: manOpen})
	gamePageRegisterOverlay(gamePageOverlay{
		ID:     "manual",
		IsOpen: func() bool { return manUI.open },
		Key: func(e *sdl.KeyboardEvent) bool {
			manAction(tbxKeyAction(e))
			return true
		},
		Pad: func(e *sdl.ControllerButtonEvent) bool {
			manAction(tbxPadAction(e))
			return true
		},
		Mouse: func(mx, my int32) bool {
			manMouse(mx, my)
			return true
		},
		Render: manRender,
		Close:  manClose,
	})
}

// manMoreRow is the More sheet's row: every game with a file has one.
func manMoreRow(game libraryGame) (string, string, bool) {
	if !manGameHasFile(game) {
		return "", "", false
	}
	docs, known := manDocsFor(game.Path)
	switch {
	case !known:
		return "Manual & guide", "Controls · looking for manuals…", true
	case len(docs) == 1:
		return "Manual & guide", "Controls · 1 document", true
	case len(docs) > 1:
		return "Manual & guide", "Controls · " + strconv.Itoa(len(docs)) + " documents", true
	}
	return "Manual & guide", "Controls · add a manual or guide", true
}

// manOpen shows the list for game.
func manOpen(game libraryGame) {
	docs, known := manDocsFor(game.Path)
	manUI.open = true
	manUI.game = game
	manUI.docs = docs
	manUI.loading = !known
	manUI.cursor, manUI.first = 0, 0
	manUI.armed = -1
	manUI.controls = false
	manBuildRows()
	manRefresh(game, false, func([]manDoc) { manDocsLanded(game.Path) })
}

// manClose shuts the list without a sound (the page is closing).
func manClose() {
	manUI.open = false
	manUI.controls = false
	manUI.rows, manUI.docs, manUI.hits = nil, nil, nil
}

// manDocsLanded takes a finished discovery for path into the open list.
func manDocsLanded(path string) {
	if !manUI.open || manUI.game.Path != path {
		return
	}
	docs, _ := manDocsFor(path)
	manUI.docs = docs
	manUI.loading = false
	manUI.armed = -1
	manBuildRows()
}

// manBuildRows lays the list out from the documents.
func manBuildRows() {
	ui := &manUI
	title, _, _ := manControls(ui.game)
	rows := []manRow{{kind: manRowControls, label: "Controls", detail: title, icon: "gamepad"}}
	for i, d := range ui.docs {
		icon := "pdf"
		switch d.Kind {
		case manKindText:
			icon = "doc"
		case manKindPictures:
			icon = "folder"
		case manKindLink:
			icon = "help"
		}
		rows = append(rows, manRow{kind: manRowDoc, doc: i, label: d.Title, detail: manDocDetail(d), icon: icon})
	}
	if ui.loading {
		rows = append(rows, manRow{kind: manRowDoc, doc: -1, label: "Looking for manuals…", detail: "Next to the game, in manuals folders and frontends' media", icon: "list"})
	}
	rows = append(rows,
		manRow{kind: manRowAddFile, label: "Add a manual or guide", detail: "A PDF, text file or pictures, chosen in Files", icon: "download"},
		manRow{kind: manRowAddLink, label: "Add a guide link", detail: "A web page, opened in the Browser", icon: "list"},
	)
	ui.rows = rows
	if ui.cursor >= len(rows) {
		ui.cursor = len(rows) - 1
	}
}

// manControls is the game's controls: a title for the row, the lines of the
// chart, and a note under it. The chart of its control layout when the game
// has one; else what the console's Buttons and Turbo choice does.
func manControls(game libraryGame) (title string, lines []ctrlLayoutLine, note string) {
	if l, ok := ctrlLayoutForGame(game); ok && len(l.Chart) > 0 {
		title, note = l.Name, l.Description
		lines = append(lines, padChartLines(l.Chart)...)
	} else {
		buttons, turbo := gameButtonsForGame(appConfig, game), gameTurboForGame(appConfig, game)
		title = "Buttons: " + gameButtonsLabel(buttons)
		lines = append(lines,
			ctrlLayoutLine{Button: "Buttons", Action: gameButtonsLabel(buttons) + " — " + gameButtonsNote(buttons)},
			ctrlLayoutLine{Button: "Turbo", Action: gameTurboLabel(turbo) + " — " + gameTurboNote(turbo)},
		)
		note = "For RetroArch games. Change them in the game's options (Start in the library) or Settings → Controls → Game buttons."
	}
	if P().IsHandheld() && gameAssistSupported() {
		if gameMenuEnabled(appConfig) {
			lines = append(lines, ctrlLayoutLine{Button: "Select + L2", Action: "In-game menu: save, load, manual, quit"})
		}
		if gameAssistEnabled(appConfig) {
			lines = append(lines, ctrlLayoutLine{Button: "Select + R2", Action: "Ask Juka about the game"})
		}
		lines = append(lines, ctrlLayoutLine{Button: "Select + Start (hold)", Action: "Quit the game"})
	}
	return title, lines, note
}

// ── Input ─────────────────────────────────────────────────────────────────

// manAction is the list's (or the Controls view's) input.
func manAction(act tbxAction) {
	ui := &manUI
	if ui.controls {
		manControlsAction(act)
		return
	}
	n := len(ui.rows)
	switch act {
	case tbxActUp:
		if ui.cursor > 0 {
			ui.cursor--
			ui.armed = -1
			PlayNavSound()
		}
	case tbxActDown:
		if ui.cursor < n-1 {
			ui.cursor++
			ui.armed = -1
			PlayNavSound()
		}
	case tbxActPageUp, tbxActHome:
		if ui.cursor != 0 {
			ui.cursor = 0
			ui.armed = -1
			PlayNavSound()
		}
	case tbxActPageDown, tbxActEnd:
		if ui.cursor != n-1 && n > 0 {
			ui.cursor = n - 1
			ui.armed = -1
			PlayNavSound()
		}
	case tbxActConfirm:
		manChoose()
	case tbxActX:
		manRemoveFocused()
	case tbxActY:
		manOpenOther()
	case tbxActBack:
		ui.open = false
		ui.rows, ui.docs = nil, nil
		PlayBackSound()
	}
}

// manMouse is a click: a row opens, outside the card closes.
func manMouse(mx, my int32) {
	ui := &manUI
	if ui.controls {
		if ui.card.W == 0 || !pointInRect(mx, my, ui.card) {
			ui.controls = false
			PlayBackSound()
		}
		return
	}
	for _, hit := range ui.hits {
		if pointInRect(mx, my, hit.rect) {
			ui.cursor = hit.index
			manChoose()
			return
		}
	}
	if ui.card.W == 0 || !pointInRect(mx, my, ui.card) {
		ui.open = false
		PlayBackSound()
	}
}

// manFocusedDoc is the focused row's document, if it is one.
func manFocusedDoc() (manDoc, bool) {
	ui := &manUI
	if ui.cursor < 0 || ui.cursor >= len(ui.rows) {
		return manDoc{}, false
	}
	row := ui.rows[ui.cursor]
	if row.kind != manRowDoc || row.doc < 0 || row.doc >= len(ui.docs) {
		return manDoc{}, false
	}
	return ui.docs[row.doc], true
}

// manChoose opens the focused row.
func manChoose() {
	ui := &manUI
	if ui.cursor < 0 || ui.cursor >= len(ui.rows) {
		return
	}
	switch ui.rows[ui.cursor].kind {
	case manRowControls:
		ui.ctrlTitle, ui.ctrlLines, ui.ctrlNote = manControls(ui.game)
		ui.controls, ui.ctrlFirst = true, 0
		PlayActivateSound()
	case manRowAddFile:
		manAddFromFiles(ui.game)
	case manRowAddLink:
		manAddLinkPrompt(ui.game)
	case manRowDoc:
		if d, ok := manFocusedDoc(); ok {
			manOpenDoc(d)
		}
	}
}

// manControlsAction is the Controls view's input.
func manControlsAction(act tbxAction) {
	ui := &manUI
	maxFirst := len(ui.ctrlLines) - ui.ctrlRows
	if maxFirst < 0 {
		maxFirst = 0
	}
	switch act {
	case tbxActUp, tbxActPageUp:
		if ui.ctrlFirst > 0 {
			ui.ctrlFirst--
			PlayNavSound()
		}
	case tbxActDown, tbxActPageDown:
		if ui.ctrlFirst < maxFirst {
			ui.ctrlFirst++
			PlayNavSound()
		}
	case tbxActBack, tbxActConfirm:
		ui.controls = false
		PlayBackSound()
	}
}

// ── Opening a document ────────────────────────────────────────────────────

// manAwayKind is the viewer a document opened in.
type manAwayKind int

const (
	manAwayText  manAwayKind = iota // the text viewer (fileEditor.open)
	manAwayImage                    // the picture viewer (imageViewerPath)
	manAwayScene                    // another screen: the Reader, the Browser, Files
)

// manAway is the parked library and what it waits for. Main thread only.
var manAway struct {
	active bool
	kind   manAwayKind
	scene  int
	left   bool
	checks int
}

// manPark hides the library (the page and this list stay open behind it) and
// starts waiting for the viewer to close.
func manPark(kind manAwayKind) {
	manAway.active, manAway.kind = true, kind
	manAway.scene, manAway.left, manAway.checks = currentSceneIndex, false, 0
	gameLib.open = false
	tbxPost(manAwayCheck)
}

// manParkScene parks the library for the screen at idx (the Reader, the
// Browser, Files), just asked for. When no scene change started because that
// screen is already the one under the library (the Start menu opens the
// library over any screen), there is no screen to come back from: parked, the
// library would return over it a moment later and hide what was opened, so it
// closes instead and leaves that screen showing.
func manParkScene(idx int) {
	if idx >= 0 && idx == currentSceneIndex && transitionPhase == "none" {
		closeGameLibraryPanel()
		return
	}
	manPark(manAwayScene)
}

// manAwayCheck runs once a frame while the library is parked: it brings the
// library back once the viewer is gone. A scene change that never started (a
// fade already running) gives up after a few frames.
func manAwayCheck() {
	a := &manAway
	if !a.active {
		return
	}
	if gameLib.open {
		// The library came back another way (opened again from Home): that is
		// a fresh visit, so the page and this list left parked behind it go,
		// or the old game's page would greet the new visit.
		a.active = false
		manAdd.game = libraryGame{}
		gmetaCloseAll()
		return
	}
	a.checks++
	waiting := false
	switch a.kind {
	case manAwayText:
		waiting = fileEditor.open
	case manAwayImage:
		waiting = imageViewerPath != ""
	case manAwayScene:
		switch {
		case currentSceneIndex != a.scene || transitionPhase != "none":
			a.left, waiting = true, true
		case !a.left && a.checks < 30:
			waiting = true
		}
	}
	if waiting {
		tbxPost(manAwayCheck)
		return
	}
	a.active = false
	if a.kind != manAwayScene && (currentSceneIndex != a.scene || transitionPhase != "none") {
		// The text or picture viewer went with a change of screen (a scene
		// change closes the text viewer): the player is somewhere else now,
		// and the library must not come back over that screen. It closes the
		// way it always does, page and list with it.
		gameLib.open = true
		closeGameLibraryPanel()
		return
	}
	gameLib.open = true
	if a.kind == manAwayScene {
		// Back from Files without choosing: the next visit to Files is not
		// for this game any more.
		manAdd.game = libraryGame{}
	}
	if manUI.open {
		// A guide added or removed meanwhile shows at once.
		manRefresh(manUI.game, false, func([]manDoc) { manDocsLanded(manUI.game.Path) })
	}
}

// manOpenDoc opens one document in its viewer.
func manOpenDoc(d manDoc) {
	switch d.Kind {
	case manKindReader:
		if !regularFile(d.Path) {
			manGone(d)
			return
		}
		// "Open books with: KOReader" (koreader.go); the reader when it does
		// not start.
		if koreaderTakesBooks(appConfig) && manOpenInKOReader(d.Path) {
			return
		}
		manOpenInReader(d.Path)
	case manKindText:
		if !regularFile(d.Path) {
			manGone(d)
			return
		}
		if fileEditorOpen(d.Path) {
			manPark(manAwayText)
		}
	case manKindPictures:
		if len(d.Pages) == 0 || !regularFile(d.Pages[0]) {
			manGone(d)
			return
		}
		// The scan's pages, in order, are what L2/R2 and Left/Right walk. Not
		// openImageViewer: it would first check every picture of the folder
		// Files last listed, on this thread, only to replace that list.
		imageViewerFiles = append([]string(nil), d.Pages...)
		imageViewerIndex = 0
		imgv.hideUI = false
		imageViewerShow(filepath.Clean(d.Pages[0]))
		PlayActivateSound()
		manPark(manAwayImage)
	case manKindLink:
		if openBrowserAtURL(appConfig, d.URL) {
			PlayActivateSound()
			// A YouTube link plays in Media's player, not the Browser.
			idx := sceneIndexExact(appConfig, browserSceneName)
			if address, err := normalizeTextBrowserURL(d.URL); err == nil {
				if _, video := brwYouTubeVideo(address); video {
					idx = sceneWithVideoElement(appConfig)
				}
			}
			manParkScene(idx)
		}
	}
}

// manOpenInReader opens a PDF or EPUB manual in JukaHub's reader, whatever
// "Open books with" says. Files' location is the file's drive for this one
// call (see the top).
func manOpenInReader(path string) {
	saved := filesNav.location
	filesNav.location = filesVolumeRoot(path)
	ok := pdfvOpenDocumentHere(appConfig, path)
	filesNav.location = saved
	if ok {
		PlayActivateSound()
		manParkScene(sceneIndexExact(appConfig, pdfvSceneName))
	}
}

// manOpenInKOReader opens a manual in KOReader (koreader.go). KOReader takes
// the screen itself, so nothing is parked: this list is still up when it
// quits.
func manOpenInKOReader(path string) bool {
	if !koreaderOpen(path) {
		return false
	}
	PlayActivateSound()
	return true
}

// manOtherReader names what Y does on the focused row: the reader "Open books
// with" did not pick, for a PDF or EPUB while KOReader is on the card ("" for
// none).
func manOtherReader() string {
	d, ok := manFocusedDoc()
	if !ok || d.Kind != manKindReader || !koreaderAvailable() {
		return ""
	}
	if koreaderBooksChoice(appConfig) == koreaderBooksKOReader {
		return "JukaHub reader"
	}
	return "KOReader"
}

// manOpenOther is Y: the focused manual in that other reader.
func manOpenOther() {
	other := manOtherReader()
	if other == "" {
		return
	}
	d, _ := manFocusedDoc()
	if !regularFile(d.Path) {
		manGone(d)
		return
	}
	if other == "KOReader" {
		manOpenInKOReader(d.Path)
		return
	}
	manOpenInReader(d.Path)
}

// manGone is a document that is no longer there: say so and look again.
func manGone(d manDoc) {
	PlayErrorSound()
	ShowToast(filepath.Base(d.Path)+" is not there any more", ToastKindWarning)
	manInvalidate(manUI.game.Path)
	manRefresh(manUI.game, true, func([]manDoc) { manDocsLanded(manUI.game.Path) })
}

// ── Drawing ───────────────────────────────────────────────────────────────

// manRender draws the list, or the Controls view over it.
func manRender(renderer *sdl.Renderer, config *Config) {
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	if manUI.controls {
		manRenderControls(renderer, config, fonts)
		return
	}
	ui := &manUI
	header := UIPanelHeader("Manual & guide", ui.game.Label(), "pdf")
	headerH := UIPanelHeaderBand(config, header, 10)
	rowH := fonts.bodyH + fonts.tinyH + 22
	n := len(ui.rows)
	footerH := fonts.smallH + 20
	cardW := min(int32(680), screenWidth-64)
	cardH := min(headerH+int32(n)*rowH+footerH+8, screenHeight-48)
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}
	ui.card = card
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	UIPageHeaderDraw(renderer, config, overlayHeaderOnCard(header, card))

	listY := card.Y + headerH
	visible := int((card.Y + card.H - footerH - 8 - listY) / rowH)
	if visible < 1 {
		visible = 1
	}
	if ui.cursor < ui.first {
		ui.first = ui.cursor
	}
	if ui.cursor >= ui.first+visible {
		ui.first = ui.cursor - visible + 1
	}
	armed := ui.armed >= 0 && time.Since(ui.armedAt) <= feArmWindow
	ui.hits = ui.hits[:0]
	for i := ui.first; i < n && i < ui.first+visible; i++ {
		row := ui.rows[i]
		rect := sdl.Rect{X: card.X + OverlayPadX, Y: listY + int32(i-ui.first)*rowH, W: card.W - 2*OverlayPadX, H: rowH - 6}
		UIListRow(renderer, rect, glassRowRadius, i == ui.cursor, false)
		icon := sdl.Rect{X: rect.X + 14, Y: rect.Y + (rect.H-28)/2, W: 28, H: 28}
		DrawHomeIcon(renderer, row.icon, icon, i == ui.cursor)
		textX := icon.X + icon.W + 14
		textW := rect.X + rect.W - 14 - textX
		detail := row.detail
		if armed && i == ui.armed {
			detail = "Press X again to remove it"
		}
		top := rect.Y + (rect.H-fonts.bodyH-fonts.tinyH-2)/2
		renderText(renderer, config, fonts.body, ellipsize(fonts.body, row.label, textW), ColorTextPrimary(), textX, top)
		ink := ColorTextSecondary()
		if armed && i == ui.armed {
			ink = ColorWarning
		}
		renderText(renderer, config, fonts.tiny, ellipsize(fonts.tiny, detail, textW), ink, textX, top+fonts.bodyH+2)
		ui.hits = append(ui.hits, gameLibRowHit{index: i, rect: rect})
	}
	hints := []FooterHint{{"A", "Open"}}
	if other := manOtherReader(); other != "" {
		hints = append(hints, FooterHint{"Y", other})
	}
	if d, ok := manFocusedDoc(); ok && d.User {
		hints = append(hints, FooterHint{"X", "Remove"})
	}
	hints = append(hints, FooterHint{"B", "Back"})
	hint := hintLine(hints...)
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, hint, card.W-2*OverlayPadX), ColorTextSecondary(),
		card.X+OverlayPadX, card.Y+card.H-footerH+4)
}

// manRenderControls draws the Controls view: button and action in two
// columns, the note under them.
func manRenderControls(renderer *sdl.Renderer, config *Config, fonts tbxFonts) {
	ui := &manUI
	header := UIPanelHeader("Controls", ui.ctrlTitle, "gamepad")
	headerH := UIPanelHeaderBand(config, header, 10)
	lineH := fonts.bodyH + 12
	footerH := fonts.smallH + 20
	cardW := min(int32(720), screenWidth-64)
	inner := cardW - 2*OverlayPadX
	var noteLines []string
	if ui.ctrlNote != "" {
		noteLines = pdfWrapLines(fonts.tiny, ui.ctrlNote, inner)
	}
	noteH := int32(len(noteLines)) * (fonts.tinyH + 2)
	if noteH > 0 {
		noteH += 8
	}
	want := headerH + int32(len(ui.ctrlLines))*lineH + noteH + footerH + 8
	cardH := min(want, screenHeight-48)
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}
	ui.card = card
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	UIPageHeaderDraw(renderer, config, overlayHeaderOnCard(header, card))

	// The button column is as wide as its longest name, at most 40%.
	colW := int32(0)
	for _, l := range ui.ctrlLines {
		if w, _ := measureText(fonts.body, l.Button); w > colW {
			colW = w
		}
	}
	colW = min(colW+24, inner*2/5)
	y := card.Y + headerH
	rows := int((card.Y + card.H - footerH - noteH - 8 - y) / lineH)
	if rows < 1 {
		rows = 1
	}
	ui.ctrlRows = rows
	if ui.ctrlFirst > len(ui.ctrlLines)-rows {
		ui.ctrlFirst = max(0, len(ui.ctrlLines)-rows)
	}
	x := card.X + OverlayPadX
	for i := ui.ctrlFirst; i < len(ui.ctrlLines) && i < ui.ctrlFirst+rows; i++ {
		l := ui.ctrlLines[i]
		renderText(renderer, config, fonts.body, ellipsize(fonts.body, l.Button, colW-12), glassAccent(), x, y+6)
		renderText(renderer, config, fonts.body, ellipsize(fonts.body, l.Action, inner-colW), ColorTextPrimary(), x+colW, y+6)
		y += lineH
	}
	if len(noteLines) > 0 {
		y += 8
		for _, line := range noteLines {
			renderText(renderer, config, fonts.tiny, line, ColorTextSecondary(), x, y)
			y += fonts.tinyH + 2
		}
	}
	hints := []FooterHint{{"B", "Back"}}
	if len(ui.ctrlLines) > rows {
		hints = append([]FooterHint{{"Up/Down", "Scroll"}}, hints...)
	}
	hint := hintLine(hints...)
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, hint, inner), ColorTextSecondary(),
		card.X+OverlayPadX, card.Y+card.H-footerH+4)
}
