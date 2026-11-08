package main

// hotseat_ui.go — the Local play screen.
//
// Three views on one screen:
//
//   - the lobby: four player slots. Every player presses A on their own pad
//     to join and gets a colour and a P1–P4 badge; the pad JukaHub runs on
//     joins as player 1 when the screen opens. L1/R1 move the focused player
//     to another port, Y removes them, and B on a guest's pad leaves. Under
//     the slots: Pick a game (the library — a RetroArch game starts with this
//     player order), Pass and play, and Reset.
//   - pass and play: how many players and the turn timer, then the turn view
//     — whose turn, the turn and round, the time left — where A ends the turn.
//   - the pass card: between turns a full-screen glass card hands the device
//     to the next player, and A starts their turn.
//
// Pad: the D-pad walks, A presses, B goes back (a view at a time); guests'
// pads only join and leave. Keys: arrows, Enter, Page Up/Down (move a
// player), Delete (remove one), Esc/Backspace. Mouse and touch: every slot
// and button is a target.

import (
	"context"
	"strconv"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// hotseatScene is the Local play screen's scene name.
const hotseatScene = "LocalPlay"

// hotseatView is which of the screen's views is up.
type hotseatView uint8

const (
	hotseatViewLobby hotseatView = iota
	hotseatViewSetup
	hotseatViewTurns
)

// hotseatTimerSteps are the turn timer's choices in seconds; 0 is off.
var hotseatTimerSteps = []int{0, 30, 60, 120, 300}

// hotseatUI is the screen's focus and the last frame's geometry.
var hotseatUI struct {
	visit  context.Context
	view   hotseatView
	zone   int
	slot   int
	button int
	// The pass-and-play setup: the focused row, the seats and the timer step.
	setupRow   int
	setupSeats int
	setupTimer int
	// Text that changes once a second at most, cached between frames.
	turnKey   int
	turnText  string
	timeLeft  int
	timeText  string
	setupKey  [2]int
	setupText [2]string
	// The last frame's geometry, for the mouse.
	slotRects   [hotseatMaxPlayers]sdl.Rect
	buttonRects []sdl.Rect
	setupRects  [3]sdl.Rect
}

// The header's icon, built once.
var hotseatHeaderGlyph = func(r *sdl.Renderer, box sdl.Rect, _ sdl.Color) {
	DrawHomeIcon(r, "users", box, false)
}

// hotseatOpen opens Local play (the Tools hub tile, Settings → Controls and
// the local_play action).
func hotseatOpen(config *Config) {
	if config == nil {
		return
	}
	inmapEnsureScenes(config)
	idx := sceneIndexExact(config, hotseatScene)
	if idx < 0 || idx == currentSceneIndex {
		return
	}
	qs.open = false
	changeSceneTo(config, idx)
}

// hotseatToolsTile is Local play's tile in the Tools hub's Together section
// (toolshub.go): it says how many players or controllers are ready.
func hotseatToolsTile() toolsHubTile {
	tile := toolsHubTile{
		id: "localplay", label: "Local play", icon: "users",
		value: "Join with your own controllers, or pass one device around",
	}
	switch {
	case len(hotseat.players) >= 2:
		tile.value, tile.tone = strconv.Itoa(len(hotseat.players))+" players ready", toolsHubToneGood
	case hotseat.count >= 2:
		tile.value, tile.tone = strconv.Itoa(hotseat.count)+" controllers connected", toolsHubToneGood
	case hotseat.turns.active:
		tile.value, tile.tone = "Pass and play · "+hotseatSeatName(hotseat.turns.current)+"'s turn", toolsHubToneGood
	}
	return tile
}

// hotseatLobbyOnScreen reports whether Local play is the screen on display.
func hotseatLobbyOnScreen() bool {
	return inmapCurrentScene(appConfig) == hotseatScene
}

// hotseatUIEnsureVisit sets the screen up when it is entered: the other pads
// are opened so their A can be read, the pad JukaHub runs on joins as player
// 1, and a running pass-and-play session comes back to its turn view.
func hotseatUIEnsureVisit() {
	ctx := sceneWorkContext()
	if ctx == hotseatUI.visit {
		return
	}
	hotseatUI.visit = ctx
	hotseatUI.zone, hotseatUI.slot, hotseatUI.button = 0, 0, 0
	hotseatUI.view = hotseatViewLobby
	if hotseat.turns.active {
		hotseatUI.view = hotseatViewTurns
	}
	if currentSceneWorkName() != hotseatScene {
		return
	}
	hotseatOpenExtras()
	if id := hotseatPrimaryID(); id >= 0 && len(hotseat.players) == 0 {
		hotseatJoin(id, hotseatPadName(id))
	}
}

// hotseatLobbyButtons are the lobby's buttons, in order.
var hotseatLobbyButtons = []string{"Pick a game", "Pass and play", "Reset"}

// hotseatTurnButtons are the turn view's buttons, in order.
var hotseatTurnButtons = []string{"End turn", "Players", "End game"}

// ── Actions ──────────────────────────────────────────────────────────────

// hotseatUIJoinOrLeave answers a guest's press in the lobby: A, X or Y joins,
// B leaves.
func hotseatUIJoinOrLeave(id sdl.JoystickID, button uint8) {
	switch button {
	case sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_X, sdl.CONTROLLER_BUTTON_Y:
		if hotseatJoin(id, hotseatPadName(id)) {
			PlayActivateSound()
		}
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		if hotseatLeave(id) {
			PlayBackSound()
		}
	}
}

// hotseatUIMovePlayer moves the focused player one port left or right.
func hotseatUIMovePlayer(dir int) {
	if hotseatMove(hotseatUI.slot, dir) {
		hotseatUI.slot += dir
		PlayNavSound()
	}
}

// hotseatUIRemovePlayer removes the focused player.
func hotseatUIRemovePlayer() {
	i := hotseatUI.slot
	if i < 0 || i >= len(hotseat.players) {
		return
	}
	hotseatLeave(hotseat.players[i].id)
	PlayBackSound()
}

// hotseatUIPressLobby runs a lobby button.
func hotseatUIPressLobby(c *Config, i int) {
	switch i {
	case 0:
		PlayActivateSound()
		if len(hotseat.players) < 2 {
			ShowToast("Games start with one player until a second one joins", ToastKindInfo)
		}
		openGameLibraryPanel()
	case 1:
		PlayActivateSound()
		hotseatUI.view = hotseatViewSetup
		hotseatUI.setupRow = 0
		hotseatUI.setupSeats = len(hotseat.players)
		if hotseatUI.setupSeats < 2 {
			hotseatUI.setupSeats = 2
		}
	case 2:
		PlayBackSound()
		hotseatReset()
		hotseatUI.slot = 0
		if id := hotseatPrimaryID(); id >= 0 {
			hotseatOpenExtras()
			hotseatJoin(id, hotseatPadName(id))
		}
		ShowToast("Local play reset", ToastKindInfo)
	}
}

// hotseatUISetupChange steps the focused setup row's value.
func hotseatUISetupChange(dir int) {
	switch hotseatUI.setupRow {
	case 0:
		seats := hotseatUI.setupSeats + dir
		if seats < 2 || seats > hotseatMaxPlayers {
			return
		}
		hotseatUI.setupSeats = seats
	case 1:
		step := hotseatUI.setupTimer + dir
		if step < 0 || step >= len(hotseatTimerSteps) {
			return
		}
		hotseatUI.setupTimer = step
	default:
		return
	}
	PlayNavSound()
}

// hotseatUIStartTurns starts pass and play on its first card.
func hotseatUIStartTurns() {
	hotseat.turns.start(hotseatUI.setupSeats, hotseatTimerSteps[hotseatUI.setupTimer])
	hotseatUI.view = hotseatViewTurns
	hotseatUI.button = 0
	PlayActivateSound()
}

// hotseatUIPressTurns runs a turn-view button: 0 ends the turn (on the pass
// card, starts the next one), 1 goes back to the players, 2 ends the game.
func hotseatUIPressTurns(c *Config, i int) {
	t := &hotseat.turns
	switch {
	case i == 2:
		hotseat.turns = hotseatTurns{}
		hotseatUI.view = hotseatViewLobby
		PlayBackSound()
		ShowToast("Pass and play ended", ToastKindInfo)
	case t.passing:
		t.begin(hotseatNow())
		PlayActivateSound()
	case i == 0:
		hotseatPassTurn(c)
	case i == 1:
		hotseatUI.view = hotseatViewLobby
		PlayBackSound()
	}
}

// hotseatUIBack is B: a view back, then off the screen.
func hotseatUIBack(c *Config) {
	switch hotseatUI.view {
	case hotseatViewSetup, hotseatViewTurns:
		hotseatUI.view = hotseatViewLobby
		PlayBackSound()
	default:
		goBackScene(c)
	}
}

// hotseatUIMove walks the focused view with the D-pad (or the arrows).
func hotseatUIMove(dx, dy int) {
	switch hotseatUI.view {
	case hotseatViewSetup:
		if dy != 0 {
			row := hotseatUI.setupRow + dy
			if row < 0 {
				focusEngine.ClearFocus()
				focusChromeBar()
				return
			}
			if row < 3 {
				hotseatUI.setupRow = row
				PlayNavSound()
			}
			return
		}
		hotseatUISetupChange(dx)
	case hotseatViewTurns:
		if hotseat.turns.passing || dx == 0 {
			return
		}
		next := hotseatUI.button + dx
		if next >= 0 && next < len(hotseatTurnButtons) {
			hotseatUI.button = next
			PlayNavSound()
		}
	default:
		switch {
		case dy < 0 && hotseatUI.zone == 1:
			hotseatUI.zone = 0
			PlayNavSound()
		case dy < 0:
			focusEngine.ClearFocus()
			focusChromeBar()
		case dy > 0 && hotseatUI.zone == 0:
			hotseatUI.zone = 1
			PlayNavSound()
		case dx != 0 && hotseatUI.zone == 0:
			if next := hotseatUI.slot + dx; next >= 0 && next < hotseatMaxPlayers {
				hotseatUI.slot = next
				PlayNavSound()
			}
		case dx != 0:
			if next := hotseatUI.button + dx; next >= 0 && next < len(hotseatLobbyButtons) {
				hotseatUI.button = next
				PlayNavSound()
			}
		}
	}
}

// hotseatUIConfirm is A (Enter) for the host.
func hotseatUIConfirm(c *Config, from sdl.JoystickID) {
	switch hotseatUI.view {
	case hotseatViewSetup:
		if hotseatUI.setupRow == 2 {
			hotseatUIStartTurns()
			return
		}
		hotseatUISetupChange(1)
	case hotseatViewTurns:
		if hotseat.turns.passing {
			// The pass card has one button: start the turn.
			hotseatUIPressTurns(c, 0)
			return
		}
		hotseatUIPressTurns(c, hotseatUI.button)
	default:
		if hotseatUI.zone == 1 {
			hotseatUIPressLobby(c, hotseatUI.button)
			return
		}
		// The host's own pad joins with A when it has not yet.
		if from >= 0 && hotseatPlayerIndex(from) < 0 && hotseatJoin(from, hotseatPadName(from)) {
			PlayActivateSound()
		}
	}
}

// ── Input ────────────────────────────────────────────────────────────────

func hotseatUIPad(e *sdl.ControllerButtonEvent, c *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	hotseatUIEnsureVisit()
	// A guest's pad (one Local play opened) only joins and leaves, and only
	// in the lobby; elsewhere its presses are dropped here.
	if e.Which >= 0 && e.Which != hotseatPrimaryID() && hotseatExtraIndex(e.Which) >= 0 {
		if hotseatUI.view == hotseatViewLobby {
			hotseatUIJoinOrLeave(e.Which, e.Button)
		}
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_START, sdl.CONTROLLER_BUTTON_GUIDE,
		sdl.CONTROLLER_BUTTON_LEFTSTICK, sdl.CONTROLLER_BUTTON_RIGHTSTICK:
		return false
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		hotseatUIMove(0, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		hotseatUIMove(0, 1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		hotseatUIMove(-1, 0)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		hotseatUIMove(1, 0)
	case sdl.CONTROLLER_BUTTON_A:
		hotseatUIConfirm(c, e.Which)
	case sdl.CONTROLLER_BUTTON_X:
		if hotseatUI.view == hotseatViewTurns {
			hotseatUIPressTurns(c, 2)
		}
	case sdl.CONTROLLER_BUTTON_Y:
		if hotseatUI.view == hotseatViewLobby && hotseatUI.zone == 0 {
			hotseatUIRemovePlayer()
		}
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		if hotseatUI.view == hotseatViewLobby && hotseatUI.zone == 0 {
			hotseatUIMovePlayer(-1)
		}
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		if hotseatUI.view == hotseatViewLobby && hotseatUI.zone == 0 {
			hotseatUIMovePlayer(1)
		}
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		hotseatUIBack(c)
	}
	return true
}

func hotseatUIKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	hotseatUIEnsureVisit()
	switch e.Keysym.Sym {
	case sdl.K_UP:
		hotseatUIMove(0, -1)
	case sdl.K_DOWN:
		hotseatUIMove(0, 1)
	case sdl.K_LEFT:
		hotseatUIMove(-1, 0)
	case sdl.K_RIGHT:
		hotseatUIMove(1, 0)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		hotseatUIConfirm(c, -1)
	case sdl.K_PAGEUP:
		if hotseatUI.view == hotseatViewLobby && hotseatUI.zone == 0 {
			hotseatUIMovePlayer(-1)
		}
	case sdl.K_PAGEDOWN:
		if hotseatUI.view == hotseatViewLobby && hotseatUI.zone == 0 {
			hotseatUIMovePlayer(1)
		}
	case sdl.K_DELETE:
		if hotseatUI.view == hotseatViewLobby && hotseatUI.zone == 0 {
			hotseatUIRemovePlayer()
		}
	case sdl.K_ESCAPE, sdl.K_BACKSPACE:
		hotseatUIBack(c)
	default:
		return false
	}
	return true
}

// hotseatUIClick handles a click or a tap on the screen.
func hotseatUIClick(mx, my int32, c *Config) bool {
	hotseatUIEnsureVisit()
	switch hotseatUI.view {
	case hotseatViewSetup:
		for i, r := range hotseatUI.setupRects {
			if r.W > 0 && pointInRect(mx, my, r) {
				hotseatUI.setupRow = i
				if i == 2 {
					hotseatUIStartTurns()
					return true
				}
				// A tap on the left half steps down, on the right half up.
				if mx < r.X+r.W/2 {
					hotseatUISetupChange(-1)
				} else {
					hotseatUISetupChange(1)
				}
				return true
			}
		}
	case hotseatViewTurns:
		for i, r := range hotseatUI.buttonRects {
			if r.W > 0 && hitTouchTarget(r, mx, my) {
				hotseatUI.button = i
				hotseatUIPressTurns(c, i)
				return true
			}
		}
	default:
		for i, r := range hotseatUI.buttonRects {
			if r.W > 0 && hitTouchTarget(r, mx, my) {
				hotseatUI.zone, hotseatUI.button = 1, i
				hotseatUIPressLobby(c, i)
				return true
			}
		}
		for i, r := range hotseatUI.slotRects {
			if r.W > 0 && pointInRect(mx, my, r) {
				hotseatUI.zone, hotseatUI.slot = 0, i
				PlayNavSound()
				return true
			}
		}
	}
	return true
}

// hotseatUIHints is the screen's footer, following the view.
func hotseatUIHints() []FooterHint {
	switch hotseatUI.view {
	case hotseatViewSetup:
		return []FooterHint{{Button: "D-Pad", Label: "Choose"}, {Button: "A", Label: "Start"}, {Button: "B", Label: "Players"}}
	case hotseatViewTurns:
		if hotseat.turns.passing {
			return []FooterHint{{Button: "A", Label: "Start turn"}, {Button: "X", Label: "End game"}, {Button: "B", Label: "Players"}}
		}
		return []FooterHint{{Button: "A", Label: "Press"}, {Button: "X", Label: "End game"}, {Button: "B", Label: "Players"}}
	}
	if hotseatUI.zone == 0 {
		return []FooterHint{{Button: "A", Label: "Join"}, {Button: "L1/R1", Label: "Move"},
			{Button: "Y", Label: "Remove"}, {Button: "B", Label: "Back"}}
	}
	return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Open"}, {Button: "B", Label: "Back"}}
}

// ── Drawing ──────────────────────────────────────────────────────────────

// hotseatUIRender draws the screen.
func hotseatUIRender(renderer *sdl.Renderer, c *Config) {
	drawAppBackdrop(renderer, c, screenWidth, screenHeight)
	hotseatUIEnsureVisit()
	title := getFontSized(c, "medium", 19)
	small := getFontSized(c, "small", 15)
	big := getFontSized(c, "medium", 34)
	if title == nil || small == nil || big == nil {
		renderStatusBar(renderer, c)
		renderFooter(renderer, c)
		return
	}
	margin := scaleX(homeSideMargin, screenWidth)
	top := headerBarHeight() + scaleY(8, screenHeight)
	bottom := screenHeight - bottomBarHeight(hotseatScene) - 16
	sub := "Every player presses A on their own controller to join"
	if hotseat.count < 2 && len(hotseat.players) < 2 {
		sub = "Connect another controller to play together, or pass this one around"
	}
	header := UIPageHeader{
		Title:     "Local play",
		Subtitle:  sub,
		TitleSize: menuTitleSize,
		Glyph:     hotseatHeaderGlyph,
		Status:    hotseatCountText(),
		X:         margin,
		Y:         top,
		W:         screenWidth - 2*margin,
	}
	contentY := UIPageHeaderDraw(renderer, c, header) + 20
	body := sdl.Rect{X: margin, Y: contentY, W: screenWidth - 2*margin, H: bottom - contentY}
	hotseatUI.buttonRects = hotseatUI.buttonRects[:0]
	hotseatUI.slotRects = [hotseatMaxPlayers]sdl.Rect{}
	hotseatUI.setupRects = [3]sdl.Rect{}
	switch hotseatUI.view {
	case hotseatViewSetup:
		hotseatUIDrawSetup(renderer, c, title, small, body)
	case hotseatViewTurns:
		hotseatUIDrawTurns(renderer, c, title, small, big, body)
	default:
		hotseatUIDrawLobby(renderer, c, title, small, body)
	}
	renderStatusBar(renderer, c)
	renderFooter(renderer, c)
}

// hotseatCountCache keeps the read-out between frames: the count only
// changes when a controller comes or goes.
var hotseatCountCache struct {
	n    int
	text string
}

// hotseatCountText is the header's read-out.
func hotseatCountText() string {
	n := hotseat.count
	switch {
	case n <= 0:
		return ""
	case n == 1:
		return "1 controller"
	}
	if hotseatCountCache.n != n || hotseatCountCache.text == "" {
		hotseatCountCache.n, hotseatCountCache.text = n, strconv.Itoa(n)+" controllers"
	}
	return hotseatCountCache.text
}

// hotseatChromeFocused reports whether the top bar holds the focus, so the
// screen draws no ring of its own.
func hotseatChromeFocused() bool {
	return chromeTier(focusEngine.ElementIndex()) != 0
}

// hotseatBadgeLabels are the slots' badges.
var hotseatBadgeLabels = [hotseatMaxPlayers]string{"P1", "P2", "P3", "P4"}

// hotseatDrawBadge draws a player's "P1" pill in their colour at x, y and
// returns its width.
func hotseatDrawBadge(renderer *sdl.Renderer, font *ttf.Font, i int, x, y int32, muted bool) int32 {
	label := hotseatBadgeLabels[i%hotseatMaxPlayers]
	tw, th := measureText(font, label)
	w, h := tw+20, th+8
	fill := hotseatColor(i)
	ink := contrastText(fill)
	if muted {
		fill = glassA(24)
		ink = ColorTextTertiary()
	}
	fillRoundedRect(renderer, x, y, w, h, h/2, fill)
	drawText(renderer, font, label, x+10, y+4, ink, textAlignLeft)
	return w
}

// hotseatUIDrawLobby draws the four slots, the buttons and the last note.
func hotseatUIDrawLobby(renderer *sdl.Renderer, c *Config, title, small *ttf.Font, body sdl.Rect) {
	const gap = int32(16)
	slotW := (body.W - gap*(hotseatMaxPlayers-1)) / hotseatMaxPlayers
	slotH := body.H * 52 / 100
	if slotH > 230 {
		slotH = 230
	}
	chrome := hotseatChromeFocused()
	th, sh := int32(title.Height()), int32(small.Height())
	for i := 0; i < hotseatMaxPlayers; i++ {
		r := sdl.Rect{X: body.X + int32(i)*(slotW+gap), Y: body.Y, W: slotW, H: slotH}
		hotseatUI.slotRects[i] = r
		focused := hotseatUI.zone == 0 && hotseatUI.slot == i && !chrome
		joined := i < len(hotseat.players)
		if focused {
			drawGlassFocusGlow(renderer, r)
		}
		drawGlassSurface(renderer, r, glassControlRadius, glassState{
			Focused:  focused,
			Hovered:  pointInRect(mouseX, mouseY, r),
			Disabled: !joined && !focused,
		})
		hotseatDrawBadge(renderer, small, i, r.X+16, r.Y+16, !joined)
		icon := int32(56)
		if icon > r.H/3 {
			icon = r.H / 3
		}
		glyph := drawGlassBadge(renderer, sdl.Rect{X: r.X + (r.W-icon)/2, Y: r.Y + r.H/2 - icon + 6, W: icon, H: icon})
		ty := r.Y + r.H/2 + 16
		if !joined {
			DrawHomeIcon(renderer, "plus", glyph, false)
			for k, line := range []string{"Press A on a", "controller to join"} {
				drawText(renderer, small, ellipsize(small, line, r.W-24), r.X+r.W/2, ty+int32(k)*(sh+2), ColorTextTertiary(), textAlignCenter)
			}
			continue
		}
		p := hotseat.players[i]
		DrawHomeIcon(renderer, "gamepad", glyph, focused)
		drawText(renderer, title, ellipsize(title, p.name, r.W-24), r.X+r.W/2, ty, ColorTextPrimary(), textAlignCenter)
		role := "Joined"
		if p.id == hotseatPrimaryID() {
			role = "This device"
		}
		drawText(renderer, small, role, r.X+r.W/2, ty+th+4, ColorTextSecondary(), textAlignCenter)
		// The player's colour runs along the bottom edge of the card.
		fillRoundedRect(renderer, r.X+16, r.Y+r.H-10, r.W-32, 4, 2, hotseatColor(i))
	}

	y := body.Y + slotH + 26
	btnH := sh + 22
	if btnH < 44 {
		btnH = 44
	}
	btnW := (body.W - gap*int32(len(hotseatLobbyButtons)-1)) / int32(len(hotseatLobbyButtons))
	if btnW > 300 {
		btnW = 300
	}
	for i, label := range hotseatLobbyButtons {
		r := sdl.Rect{X: body.X + int32(i)*(btnW+gap), Y: y, W: btnW, H: btnH}
		UIButton(renderer, c, r, label, UIButtonStyle{Font: title}, UIButtonState{
			Focused: hotseatUI.zone == 1 && hotseatUI.button == i && !chrome,
			Hovered: pointInRect(mouseX, mouseY, r),
		})
		hotseatUI.buttonRects = append(hotseatUI.buttonRects, r)
	}
	y += btnH + 18
	note := hotseat.note
	if note == "" {
		note = "A RetroArch game picked from here starts with players in this order"
	}
	if y+sh <= body.Y+body.H {
		drawText(renderer, small, ellipsize(small, note, body.W), body.X, y, ColorTextTertiary(), textAlignLeft)
	}
}

// hotseatUIDrawSetup draws pass and play's two choices and its Start button.
func hotseatUIDrawSetup(renderer *sdl.Renderer, c *Config, title, small *ttf.Font, body sdl.Rect) {
	w := min32(body.W, 640)
	x := body.X + (body.W-w)/2
	th := int32(title.Height())
	rowH := th + 30
	chrome := hotseatChromeFocused()
	labels := [2]string{"Players", "Turn timer"}
	keys := [2]int{hotseatUI.setupSeats, hotseatUI.setupTimer}
	for i := range keys {
		if hotseatUI.setupText[i] == "" || hotseatUI.setupKey[i] != keys[i] {
			hotseatUI.setupKey[i] = keys[i]
			value := hotseatSeatsLabel(hotseatUI.setupSeats)
			if i == 1 {
				value = hotseatTimerLabel(hotseatTimerSteps[hotseatUI.setupTimer])
			}
			hotseatUI.setupText[i] = "‹  " + value + "  ›"
		}
	}
	y := body.Y + 8
	for i := 0; i < 2; i++ {
		r := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		hotseatUI.setupRects[i] = r
		UIListRow(renderer, r, glassRowRadius, hotseatUI.setupRow == i && !chrome, pointInRect(mouseX, mouseY, r))
		drawText(renderer, title, labels[i], r.X+18, r.Y+(r.H-th)/2, ColorTextPrimary(), textAlignLeft)
		drawText(renderer, title, hotseatUI.setupText[i], r.X+r.W-18, r.Y+(r.H-th)/2, ColorTextSecondary(), textAlignRight)
		y += rowH + 10
	}
	y += 8
	start := sdl.Rect{X: x, Y: y, W: w, H: rowH}
	hotseatUI.setupRects[2] = start
	UIButton(renderer, c, start, "Start pass and play", UIButtonStyle{Font: title, Variant: UIButtonPrimary}, UIButtonState{
		Focused: hotseatUI.setupRow == 2 && !chrome,
		Hovered: pointInRect(mouseX, mouseY, start),
	})
	y += rowH + 16
	help := "One device goes round the table. Between turns a card says whose turn it is."
	if y+int32(small.Height()) <= body.Y+body.H {
		drawText(renderer, small, ellipsize(small, help, w), x, y, ColorTextTertiary(), textAlignLeft)
	}
}

// hotseatSeatsLabel is "2 players".
func hotseatSeatsLabel(n int) string {
	return strconv.Itoa(n) + " players"
}

// hotseatTimerLabel is a timer step as the setup shows it.
func hotseatTimerLabel(sec int) string {
	switch {
	case sec <= 0:
		return "Off"
	case sec < 60:
		return strconv.Itoa(sec) + " seconds"
	case sec == 60:
		return "1 minute"
	}
	return strconv.Itoa(sec/60) + " minutes"
}

// hotseatClock writes seconds as m:ss.
func hotseatClock(sec int) string {
	s := strconv.Itoa(sec % 60)
	if len(s) < 2 {
		s = "0" + s
	}
	return strconv.Itoa(sec/60) + ":" + s
}

// hotseatTurnLine is "Turn 5 · Round 3", rebuilt when the turn changes.
func hotseatTurnLine(t *hotseatTurns) string {
	if t.turn != hotseatUI.turnKey || hotseatUI.turnText == "" {
		hotseatUI.turnKey = t.turn
		hotseatUI.turnText = "Turn " + strconv.Itoa(t.turn) + " · Round " + strconv.Itoa(t.round())
	}
	return hotseatUI.turnText
}

// hotseatTimeLine is the time left as m:ss, rebuilt once a second.
func hotseatTimeLine(left int) string {
	if left != hotseatUI.timeLeft || hotseatUI.timeText == "" {
		hotseatUI.timeLeft = left
		hotseatUI.timeText = hotseatClock(left)
	}
	return hotseatUI.timeText
}

// hotseatUIDrawTurns draws the turn view, or the pass card between turns.
func hotseatUIDrawTurns(renderer *sdl.Renderer, c *Config, title, small, big *ttf.Font, body sdl.Rect) {
	t := &hotseat.turns
	if !t.active {
		hotseatUI.view = hotseatViewLobby
		return
	}
	chrome := hotseatChromeFocused()
	w := min32(body.W, 720)
	card := sdl.Rect{X: body.X + (body.W-w)/2, Y: body.Y, W: w, H: body.H}
	if t.passing {
		// The pass card covers the whole screen: whoever holds the device
		// must not see the last player's turn.
		drawOverlayScrim(renderer)
		h := min32(body.H, 360)
		card = sdl.Rect{X: (screenWidth - w) / 2, Y: (screenHeight - h) / 2, W: w, H: h}
		drawOverlayCard(renderer, card)
	} else {
		UICard(renderer, card, DefaultUICardStyle())
	}
	cx := card.X + card.W/2
	y := card.Y + 28
	caption := "Your turn"
	if t.passing {
		caption = "Pass the device to"
		if t.expired {
			caption = "Time's up · pass the device to"
		}
	}
	drawText(renderer, small, caption, cx, y, ColorTextSecondary(), textAlignCenter)
	y += int32(small.Height()) + 12
	name := hotseatSeatName(t.current)
	nw, nh := measureText(big, name)
	pill := sdl.Rect{X: cx - (nw+48)/2, Y: y, W: nw + 48, H: nh + 16}
	fill := hotseatColor(t.current)
	fillRoundedRect(renderer, pill.X, pill.Y, pill.W, pill.H, pill.H/2, fill)
	drawText(renderer, big, name, cx, pill.Y+8, contrastText(fill), textAlignCenter)
	y = pill.Y + pill.H + 16
	drawText(renderer, title, hotseatTurnLine(t), cx, y, ColorTextPrimary(), textAlignCenter)
	y += int32(title.Height()) + 12
	if left := t.remaining(hotseatNow()); left >= 0 {
		drawText(renderer, big, hotseatTimeLine(left), cx, y, ColorTextPrimary(), textAlignCenter)
		y += int32(big.Height()) + 8
		bar := sdl.Rect{X: card.X + 48, Y: y, W: card.W - 96, H: 8}
		frac := 0.0
		if t.limitSec > 0 {
			frac = float64(left) / float64(t.limitSec)
		}
		UIProgressBar(renderer, bar, frac, glassA(20), fill)
		y += 24
	}
	btnH := int32(small.Height()) + 22
	if btnH < 44 {
		btnH = 44
	}
	if t.passing {
		r := sdl.Rect{X: cx - 140, Y: card.Y + card.H - btnH - 24, W: 280, H: btnH}
		UIButton(renderer, c, r, "Start turn", UIButtonStyle{Font: title, Variant: UIButtonPrimary}, UIButtonState{
			Focused: !chrome,
			Hovered: pointInRect(mouseX, mouseY, r),
		})
		hotseatUI.buttonRects = append(hotseatUI.buttonRects, r)
		return
	}
	const gap = int32(14)
	btnW := (card.W - 48 - gap*int32(len(hotseatTurnButtons)-1)) / int32(len(hotseatTurnButtons))
	by := card.Y + card.H - btnH - 24
	if by < y {
		by = y
	}
	for i, label := range hotseatTurnButtons {
		r := sdl.Rect{X: card.X + 24 + int32(i)*(btnW+gap), Y: by, W: btnW, H: btnH}
		style := UIButtonStyle{Font: title}
		if i == 0 {
			style.Variant = UIButtonPrimary
		}
		UIButton(renderer, c, r, label, style, UIButtonState{
			Focused: hotseatUI.button == i && !chrome,
			Hovered: pointInRect(mouseX, mouseY, r),
		})
		hotseatUI.buttonRects = append(hotseatUI.buttonRects, r)
	}
}

// ── Scene registration ───────────────────────────────────────────────────

type hotseatModule struct{ sceneBase }

func (hotseatModule) render(r *sdl.Renderer, c *Config) bool {
	hotseatUIRender(r, c)
	return true
}

func (hotseatModule) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return hotseatUIKey(e, c)
}

func (hotseatModule) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return hotseatUIPad(e, c)
}

func init() { registerScene(hotseatModule{sceneBase{name: hotseatScene}}) }
