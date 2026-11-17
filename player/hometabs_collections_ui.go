package main

// hometabs_collections_ui.go — the item sheet of the Tabs Home (X on any
// item, in any tab) and the keyboard that names a collection.
//
// The sheet is a small glass card over Home, built like the emulator picker
// (emupicker.go) and hooked into main.go the same way, so no key, click,
// wheel or trigger reaches the page behind it. Its rows depend on the item:
//
//	Play / Open                       the item's own A
//	Add to / Remove from Favourites   anything with a ref
//	Add to collection…                a list of the collections + New collection
//	Move earlier / later, Remove from "<collection>"   on a collection's page
//	Rename, Delete collection…        on a collection's folder (Delete asks)
//
// B steps back from the collection list and the delete question, then
// closes; X closes it from anywhere (it is what opened it).

import (
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// The sheet's pages.
const (
	homeTabsSheetMain = iota
	homeTabsSheetPick
	homeTabsSheetConfirm
)

// homeTabsSheetRow is one row: what it says, a note at its right edge, and
// what A does. danger draws it in the danger ink (delete).
type homeTabsSheetRow struct {
	label  string
	note   string
	danger bool
	run    func(c *Config)
}

var homeTabsSheet struct {
	open   bool
	mode   int
	item   homeTabItem
	tab    string
	parent string
	config *Config
	rows   []homeTabsSheetRow
	cursor int
	scroll int
	panel  sdl.Rect
	hits   []gameLibRowHit
	// eatUp swallows the release of a click the sheet took.
	eatUp bool
}

// homeTabsItemSheetActive reports whether the sheet owns input
// (hometabs_model.go).
func homeTabsItemSheetActive() bool { return homeTabsSheet.open }

// homeTabsItemSheetOpen opens the sheet for an item (hometabs_model.go).
// parent is the FolderView of the page the item is on ("" = a tab's top).
func homeTabsItemSheetOpen(c *Config, tab, parent string, it homeTabItem) {
	if virtualKeyboardActive || emuPickOpen() {
		return
	}
	s := &homeTabsSheet
	s.item, s.tab, s.parent, s.config = it, tab, parent, c
	s.mode = homeTabsSheetMain
	homeTabsSheetBuild()
	// The shell has played the press already (homeTabsX).
	if len(s.rows) == 0 {
		ShowToast("Nothing more to do with "+homeTabsItemTitle(it), ToastKindInfo)
		return
	}
	s.open = true
}

// homeTabsSheetClose puts the sheet away.
func homeTabsSheetClose() {
	s := &homeTabsSheet
	s.open = false
	s.rows = nil
	s.hits = s.hits[:0]
	s.config = nil
}

// homeTabsSheetUserCollection is the user collection a FolderView or ref
// names ("col:<id>"), when it is one (not an automatic one) and still there.
func homeTabsSheetUserCollection(ref string) (homeTabsCollection, bool) {
	if !strings.HasPrefix(ref, homeTabRefCollection) {
		return homeTabsCollection{}, false
	}
	id := strings.TrimPrefix(ref, homeTabRefCollection)
	if homeTabsIsAuto(id) {
		return homeTabsCollection{}, false
	}
	return homeTabsColGet(id)
}

// homeTabsSheetBuild lays out the rows of the current page.
func homeTabsSheetBuild() {
	s := &homeTabsSheet
	it := s.item
	ref := it.Ref
	title := homeTabsItemTitle(it)
	var rows []homeTabsSheetRow
	switch s.mode {
	case homeTabsSheetPick:
		for _, col := range homeTabsColList() {
			if homeTabsColHas(col, ref) {
				rows = append(rows, homeTabsSheetRow{label: col.Name, note: "Already here", run: func(*Config) {
					ShowToast(title+" is already in "+col.Name, ToastKindInfo)
				}})
				continue
			}
			rows = append(rows, homeTabsSheetRow{
				label: col.Name,
				note:  pluralizeCount(len(col.Items), "item", "items"),
				run: func(*Config) {
					homeTabsSheetClose()
					if ok, why := homeTabsColAdd(col.ID, ref, title); !ok {
						ShowToast(why, ToastKindWarning)
						return
					}
					PlaySuccessSound()
					ShowToast("Added to "+col.Name, ToastKindSuccess)
				},
			})
		}
		rows = append(rows, homeTabsSheetRow{label: "New collection…", run: func(*Config) {
			homeTabsSheetClose()
			homeTabsNewCollection(ref, title)
		}})
	case homeTabsSheetConfirm:
		col, ok := homeTabsSheetUserCollection(ref)
		if !ok {
			break
		}
		rows = append(rows,
			homeTabsSheetRow{label: "Delete " + col.Name, danger: true, run: func(*Config) {
				homeTabsSheetClose()
				if homeTabsColDelete(col.ID) {
					ShowToast("Deleted "+col.Name, ToastKindInfo)
				}
			}},
			homeTabsSheetRow{label: "Keep it", run: func(*Config) { homeTabsSheetPage(homeTabsSheetMain) }},
		)
	default:
		// A dimmed item opens only when it says why it is dimmed: a console
		// with an Install badge offers the install; a missing one does not.
		if it.Activate != nil && !it.Folder && (!it.Dim || it.Badge == "Install") {
			label := "Open"
			switch {
			case it.Badge == "Install":
				label = "Install an emulator"
			case strings.HasPrefix(ref, homeTabRefGame):
				label = "Play"
			}
			activate := it.Activate
			rows = append(rows, homeTabsSheetRow{label: label, run: func(c *Config) {
				homeTabsSheetClose()
				activate(c)
			}})
		}
		if ref != "" {
			if homeTabsIsFavourite(ref) {
				rows = append(rows, homeTabsSheetRow{label: "Remove from Favourites", run: func(*Config) {
					homeTabsSheetClose()
					homeTabsSetFavourite(it, false)
				}})
			} else {
				rows = append(rows, homeTabsSheetRow{label: "Add to Favourites", run: func(*Config) {
					homeTabsSheetClose()
					homeTabsSetFavourite(it, true)
				}})
			}
		}
		if ref != "" && !strings.HasPrefix(ref, homeTabRefCollection) {
			rows = append(rows, homeTabsSheetRow{label: "Add to collection…", run: func(*Config) {
				homeTabsSheetPage(homeTabsSheetPick)
			}})
		}
		if col, ok := homeTabsSheetUserCollection(s.parent); ok && ref != "" && homeTabsColHas(col, ref) {
			at := -1
			for i, item := range col.Items {
				if item.Ref == ref {
					at = i
				}
			}
			if at > 0 {
				rows = append(rows, homeTabsSheetRow{label: "Move earlier", run: func(*Config) {
					homeTabsSheetClose()
					homeTabsColMove(col.ID, ref, -1)
				}})
			}
			if at >= 0 && at < len(col.Items)-1 {
				rows = append(rows, homeTabsSheetRow{label: "Move later", run: func(*Config) {
					homeTabsSheetClose()
					homeTabsColMove(col.ID, ref, 1)
				}})
			}
			rows = append(rows, homeTabsSheetRow{label: "Remove from " + col.Name, run: func(*Config) {
				homeTabsSheetClose()
				if homeTabsColRemove(col.ID, ref) {
					ShowToast("Removed from "+col.Name, ToastKindInfo)
				}
			}})
		}
		if col, ok := homeTabsSheetUserCollection(ref); ok {
			rows = append(rows,
				homeTabsSheetRow{label: "Rename", run: func(*Config) {
					homeTabsSheetClose()
					homeTabsRenameCollection(col.ID, col.Name)
				}},
				homeTabsSheetRow{label: "Delete collection…", danger: true, run: func(*Config) {
					homeTabsSheetPage(homeTabsSheetConfirm)
				}},
			)
		}
	}
	s.rows = rows
	if s.cursor >= len(rows) {
		s.cursor = len(rows) - 1
	}
	if s.cursor < 0 {
		s.cursor = 0
	}
}

// homeTabsSheetPage shows another page of the sheet.
func homeTabsSheetPage(mode int) {
	s := &homeTabsSheet
	s.mode = mode
	s.cursor, s.scroll = 0, 0
	homeTabsSheetBuild()
	if len(s.rows) == 0 {
		homeTabsSheetClose()
		return
	}
	PlayNavSound()
}

// homeTabsSheetBack is B: the collection list and the question go back to
// the first page; the first page closes.
func homeTabsSheetBack() {
	s := &homeTabsSheet
	if s.mode != homeTabsSheetMain {
		s.mode = homeTabsSheetMain
		s.cursor, s.scroll = 0, 0
		homeTabsSheetBuild()
		PlayBackSound()
		return
	}
	homeTabsSheetClose()
	PlayBackSound()
}

// homeTabsSheetMove walks the rows.
func homeTabsSheetMove(delta int) {
	s := &homeTabsSheet
	next := s.cursor + delta
	if next < 0 {
		next = 0
	}
	if next >= len(s.rows) {
		next = len(s.rows) - 1
	}
	if next == s.cursor || next < 0 {
		return
	}
	s.cursor = next
	PlayNavSound()
}

// homeTabsSheetChoose runs the highlighted row.
func homeTabsSheetChoose() {
	s := &homeTabsSheet
	if s.cursor < 0 || s.cursor >= len(s.rows) {
		return
	}
	c := s.config
	if c == nil {
		c = appConfig
	}
	run := s.rows[s.cursor].run
	PlayActivateSound()
	if run != nil {
		run(c)
	}
}

// homeTabsSheetKey owns every key while the sheet is up (main.go).
func homeTabsSheetKey(e *sdl.KeyboardEvent) bool {
	if e == nil || !homeTabsSheet.open || virtualKeyboardActive {
		return false
	}
	if e.Type != sdl.KEYDOWN {
		return true
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		homeTabsSheetMove(-1)
	case sdl.K_DOWN:
		homeTabsSheetMove(1)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		homeTabsSheetChoose()
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		homeTabsSheetBack()
	case sdl.K_x:
		homeTabsSheetClose()
		PlayBackSound()
	}
	return true
}

// homeTabsSheetPad owns the pad while the sheet is up (main.go).
func homeTabsSheetPad(e *sdl.ControllerButtonEvent) bool {
	if e == nil || !homeTabsSheet.open || virtualKeyboardActive {
		return false
	}
	if e.Type != sdl.CONTROLLERBUTTONDOWN {
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		homeTabsSheetMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		homeTabsSheetMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		homeTabsSheetChoose()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		homeTabsSheetBack()
	case sdl.CONTROLLER_BUTTON_X:
		homeTabsSheetClose()
		PlayBackSound()
	}
	return true
}

// homeTabsSheetWheel scrolls the rows (main.go's wheel pass).
func homeTabsSheetWheel(y int32) {
	if y > 0 {
		homeTabsSheetMove(-1)
	} else if y < 0 {
		homeTabsSheetMove(1)
	}
}

// homeTabsSheetMouse: a row runs, a click off the card closes it.
func homeTabsSheetMouse(mx, my int32) bool {
	s := &homeTabsSheet
	if !s.open {
		return false
	}
	s.eatUp = true
	for _, hit := range s.hits {
		if pointInRect(mx, my, hit.rect) {
			s.cursor = hit.index
			homeTabsSheetChoose()
			return true
		}
	}
	if s.panel.W > 0 && pointInRect(mx, my, s.panel) {
		return true
	}
	homeTabsSheetClose()
	PlayBackSound()
	return true
}

// homeTabsSheetEatRelease reports (once) whether a left-button release
// belongs to the sheet.
func homeTabsSheetEatRelease() bool {
	eat := homeTabsSheet.open || homeTabsSheet.eatUp
	homeTabsSheet.eatUp = false
	return eat
}

// homeTabsProvidersRender is the tabs providers' overlay pass (main.go, every
// frame): the cover uploads, then the sheet when it is up.
func homeTabsProvidersRender(renderer *sdl.Renderer, config *Config) {
	homeTabsArtPump(renderer)
	homeTabsWatch()
	if !homeTabsSheet.open || virtualKeyboardActive || renderer == nil || config == nil {
		return
	}
	renderHomeTabsSheet(renderer, config)
}

// renderHomeTabsSheet draws the sheet, centred over whatever is behind it.
func renderHomeTabsSheet(renderer *sdl.Renderer, config *Config) {
	s := &homeTabsSheet
	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil || len(s.rows) == 0 {
		return
	}
	if medium == nil {
		medium = font
	}
	smallH := int32(font.Height())
	medH := int32(medium.Height())

	const pad = OverlayPadX
	rowH := max32(52, medH+22)
	subtitle := s.item.Sub
	switch s.mode {
	case homeTabsSheetPick:
		subtitle = "Add to collection"
	case homeTabsSheetConfirm:
		subtitle = "The games and apps in it stay where they are"
	}
	glyph := s.item.Glyph
	if glyph == "" {
		glyph = "apps"
	}
	header := UIPanelHeader(homeTabsItemTitle(s.item), subtitle, glyph)
	rows := len(s.rows)
	headerH := UIPanelHeaderBand(config, header, 14)
	footerH := int32(14) + smallH
	panelW := int32(560)
	if panelW > screenWidth-64 {
		panelW = screenWidth - 64
	}
	panelH := headerH + rowH*int32(rows) + footerH
	if panelH > screenHeight-24 {
		panelH = screenHeight - 24
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	card := sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}
	s.panel = card

	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, panelX, panelY, panelW)
	header = overlayHeaderOnCard(header, card)
	UIPageHeaderDraw(renderer, config, header)

	bodyY := panelY + headerH
	innerW := panelW - 2*pad
	visible := int((panelH - headerH - footerH) / rowH)
	if visible < 1 {
		visible = 1
	}
	if visible > rows {
		visible = rows
	}
	if s.cursor < s.scroll {
		s.scroll = s.cursor
	}
	if s.cursor >= s.scroll+visible {
		s.scroll = s.cursor - visible + 1
	}
	if s.scroll > rows-visible {
		s.scroll = rows - visible
	}
	if s.scroll < 0 {
		s.scroll = 0
	}
	s.hits = s.hits[:0]
	focusColor := installHighlight()
	for i := 0; i < visible; i++ {
		idx := s.scroll + i
		row := s.rows[idx]
		rect := sdl.Rect{X: panelX + pad, Y: bodyY + int32(i)*rowH, W: innerW, H: rowH - 8}
		s.hits = append(s.hits, gameLibRowHit{index: idx, rect: rect})
		selected := idx == s.cursor
		bg := WithAlpha(ColorSurfaceRow, 165)
		if selected {
			bg = WithAlpha(focusColor, 46)
		}
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, bg)
		if selected {
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, 2, focusColor)
		}
		ink := ColorTextPrimary()
		if row.danger {
			ink = ColorDanger
		}
		noteW := int32(0)
		if row.note != "" {
			nw, _, _ := font.SizeUTF8(row.note)
			noteW = int32(nw)
			if noteW > rect.W/2 {
				noteW = rect.W / 2
			}
			renderText(renderer, config, font, ellipsize(font, row.note, noteW), ColorTextTertiary(),
				rect.X+rect.W-14-noteW, rect.Y+(rect.H-smallH)/2)
		}
		labelW := rect.W - 28
		if noteW > 0 {
			labelW -= noteW + 12
		}
		renderText(renderer, config, medium, ellipsize(medium, row.label, labelW), ink,
			rect.X+14, rect.Y+(rect.H-medH)/2)
	}

	var hint string
	switch s.mode {
	case homeTabsSheetMain:
		hint = hintLine(FooterHint{"D-Pad", "Choose"}, FooterHint{"A", "Select"}, FooterHint{"B", "Close"})
	case homeTabsSheetPick:
		hint = hintLine(FooterHint{"D-Pad", "Collection"}, FooterHint{"A", "Add"}, FooterHint{"B", "Back"})
	default:
		hint = hintLine(FooterHint{"D-Pad", "Choose"}, FooterHint{"A", "Select"}, FooterHint{"B", "Back"})
	}
	renderText(renderer, config, font, ellipsize(font, hint, innerW), ColorTextSecondary(),
		panelX+pad, panelY+panelH-footerH+2)
}

// ── Naming a collection ───────────────────────────────────────────────────

// homeTabsNameVKTarget aims the on-screen keyboard at a collection's name.
// Submit-only, like the alarm label: nothing is created or renamed until
// Done, and B leaves everything as it was.
const homeTabsNameVKTarget vkTargetKind = 130

// homeTabsNameBuffer is the name while the keyboard is up.
var homeTabsNameBuffer string

// homeTabsNameEdit is what Done does: rename renameID, or make a collection
// and (when addRef is set) put that item into it.
var homeTabsNameEdit struct {
	renameID string
	addRef   string
	addTitle string
}

// homeTabsNewCollection asks for a new collection's name; addRef, when set,
// goes into it once it is made.
func homeTabsNewCollection(addRef, addTitle string) {
	homeTabsNameEdit.renameID = ""
	homeTabsNameEdit.addRef, homeTabsNameEdit.addTitle = addRef, addTitle
	homeTabsOpenNameKeyboard("")
}

// homeTabsRenameCollection asks for a collection's new name.
func homeTabsRenameCollection(id, name string) {
	homeTabsNameEdit.renameID = id
	homeTabsNameEdit.addRef, homeTabsNameEdit.addTitle = "", ""
	homeTabsOpenNameKeyboard(name)
}

// homeTabsOpenNameKeyboard opens the keyboard on the name field (the alarm
// label's recipe, alarmedit.go). The current name starts selected, so the
// first key replaces it.
func homeTabsOpenNameKeyboard(initial string) {
	chatInputActive = false
	unitInputActive = false
	vkTarget = homeTabsNameVKTarget
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	homeTabsNameBuffer = initial
	vkTextSelected = homeTabsNameBuffer != ""
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// homeTabsNameKeyboardTitle is the keyboard's title.
func homeTabsNameKeyboardTitle() string {
	if homeTabsNameEdit.renameID != "" {
		return "RENAME COLLECTION"
	}
	return "NEW COLLECTION"
}

// homeTabsNameSubmit is the keyboard's Done.
func homeTabsNameSubmit(text string) {
	name := homeTabsColCleanName(text)
	edit := homeTabsNameEdit
	homeTabsNameEdit.renameID, homeTabsNameEdit.addRef, homeTabsNameEdit.addTitle = "", "", ""
	if name == "" {
		ShowToast("A collection needs a name", ToastKindInfo)
		return
	}
	if edit.renameID != "" {
		if homeTabsColRename(edit.renameID, name) {
			ShowToast("Renamed to "+name, ToastKindSuccess)
		}
		return
	}
	id, why := homeTabsColCreate(name)
	if id == "" {
		ShowToast(why, ToastKindWarning)
		return
	}
	PlaySuccessSound()
	if edit.addRef == "" {
		ShowToast("Created "+name, ToastKindSuccess)
		return
	}
	if ok, why := homeTabsColAdd(id, edit.addRef, edit.addTitle); !ok {
		ShowToast(why, ToastKindWarning)
		return
	}
	ShowToast("Added to "+name, ToastKindSuccess)
}
