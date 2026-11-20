package main

// solitaire_ui.go — Solitaire's table: the layout, the cards, the focus and
// the held cards, the game menu, the statistics, the win, and every way of
// playing it (pad, keyboard, mouse or touch). The rules and the saved game
// are in solitaire.go.
//
// Play is two-step so it works on a D-pad: A lifts the focused card (or the
// run chosen with Up/Down in a column), the held cards follow the focus drawn
// lifted, and A again puts them down. Y is the one-press shortcut ("smart
// move"), X turns the stock from anywhere, L1/R1 undo and redo, and Start
// opens the game menu. A click or a tap does the same as A on the pile under
// it; a second click on the same pile is a smart move.
//
// The suits are not font glyphs (the UI face has no ♠♥♦♣ on every build):
// they are painted with the icon set's anti-aliased rasterizer
// (vecraster.go) into one small white texture per suit and size, tinted at
// draw time.

import (
	"fmt"
	"math/rand"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// solAction is one input, whichever device it came from.
type solAction int

const (
	solActNone solAction = iota
	solActUp
	solActDown
	solActLeft
	solActRight
	solActA
	solActB
	solActDraw
	solActSmart
	solActUndo
	solActRedo
	solActMenu
	solActNew
)

// The game menu's rows, top to bottom.
const (
	solMenuResume = iota
	solMenuNew
	solMenuRestart
	solMenuDraw
	solMenuAuto
	solMenuStats
	solMenuQuit
	solMenuRows
)

const (
	// solAutoFlight is how long a card auto-play puts up takes to fly;
	// solFinishFlight is the quicker flight of the auto-finish.
	solAutoFlight   = 150 * time.Millisecond
	solFinishFlight = 70 * time.Millisecond
	// solCascadeEvery is the gap between two cards leaving the foundations
	// in the win celebration, and solCascadeMax its longest run.
	solCascadeEvery = 70 * time.Millisecond
	solCascadeMax   = 7 * time.Second
	// solDoubleClick is the longest gap between two clicks on one pile that
	// still makes a smart move.
	solDoubleClick = 400 * time.Millisecond
)

// solFlight is a card auto-play is putting up. The game already has it on
// its foundation; until the flight lands that pile's top card is hidden and
// drawn here instead, in flight.
type solFlight struct {
	card                   solCard
	fromX, fromY, toX, toY int32
	start                  time.Time
	dur                    time.Duration
	to                     int
}

// solFly is one card bouncing across the screen after a win.
type solFly struct {
	card           solCard
	x, y, vx, vy   float64
	bounces, maxBn int
}

// solUI is the table's state that is not the game: the focus, the held
// cards, the open sheets, the animations. Main thread only.
var solUI struct {
	focus int // the focused pile
	depth int // cards chosen in a column, from its top (1 = the top card)

	held     bool
	heldFrom int
	heldN    int

	menuOpen  bool
	menuRow   int
	menuArmed int // the row waiting for a second A to confirm, -1 for none
	menuRects [solMenuRows]sdl.Rect
	menuSheet sdl.Rect
	statsOpen bool

	// winPanel is the "You won" card; winHandled is set once a win has been
	// counted, so a won game reopened from the save is not counted again.
	winPanel   bool
	winHandled bool
	winButton  sdl.Rect

	autoPending bool // auto-play should look for its next card
	finishing   bool // every card goes up, safe or not
	flying      bool
	flight      solFlight

	cascade struct {
		active  bool
		start   time.Time
		last    time.Time
		spawned int
		taken   [solKPiles]int // cards launched off each foundation
		cards   []solFly
		rng     *rand.Rand
	}

	lastClickAt    time.Time
	lastClickPile  int
	lastClickDepth int

	// entered is set by solitaireEnter and cleared by solitaireLeave, so a
	// first frame that arrives without an Enter still sets the table up (and
	// a won game from the save is never counted twice).
	entered bool
}

func init() {
	solUI.menuArmed = -1
	solUI.lastClickPile = -1
}

// ── Layout ────────────────────────────────────────────────────────────────

// solLayout is where everything goes on this frame's canvas. Cards are sized
// from the body between the header and the footer: the top row, a gap and
// a column of about two and a half cards' height, then capped by the width
// of seven columns.
type solLayout struct {
	cw, ch, gap, radius int32
	left, top, tabY     int32
	bottom              int32
	pileX, pileY        [solKPiles]int32
	wasteFan            int32 // x step between the waste's fanned cards
	downStep, upStep    int32 // a column's steps before compression
	rankPx              int
	pip, bigPip         int32
}

func solComputeLayout() solLayout {
	var L solLayout
	small := screenHeight > 0 && screenHeight < 600
	L.top = headerBarHeight() + 8
	L.bottom = screenHeight - footerBarHeight() - 6
	padX := int32(24)
	if small {
		padX = 8
	}
	availW := screenWidth - 2*padX
	availH := L.bottom - L.top
	gap := min(max(availW/80, 6), 16)
	cw := (availW - 6*gap) / 7
	ch := (availH - gap) * 10 / 36
	if byHeight := ch * 100 / 142; byHeight < cw {
		cw = byHeight
	}
	cw = max(cw, 24)
	ch = cw * 142 / 100
	L.cw, L.ch, L.gap = cw, ch, gap
	L.radius = max(cw/12, 3)
	L.left = (screenWidth - (7*cw + 6*gap)) / 2
	L.tabY = L.top + ch + max(gap, ch/10)
	col := func(i int) int32 { return L.left + int32(i)*(cw+gap) }
	L.pileX[solKStock], L.pileY[solKStock] = col(0), L.top
	L.pileX[solKWaste], L.pileY[solKWaste] = col(1), L.top
	for f := 0; f < 4; f++ {
		L.pileX[solKFound0+f], L.pileY[solKFound0+f] = col(3+f), L.top
	}
	for c := 0; c < 7; c++ {
		L.pileX[solKTab0+c], L.pileY[solKTab0+c] = col(c), L.tabY
	}
	L.wasteFan = cw / 5
	L.downStep = max(ch/11, 4)
	L.upStep = ch * 26 / 100
	L.rankPx = max(int(ch*17/100), 10)
	L.pip = max(int32(float64(L.rankPx)*0.8), 8)
	L.bigPip = cw * 46 / 100
	return L
}

// steps is the y step after a face-down card and after a face-up one in a
// column, squeezed so the column ends above the footer: face-up steps first
// (down to where the rank still shows), then face-down ones, then both.
func (L *solLayout) steps(pile []solCard) (down, up int32) {
	down, up = L.downStep, L.upStep
	var nd, nu int32
	for i := 0; i+1 < len(pile); i++ {
		if pile[i].up {
			nu++
		} else {
			nd++
		}
	}
	room := L.bottom - L.tabY - L.ch
	if nd*down+nu*up <= room {
		return down, up
	}
	minUp := max(L.ch*15/100, 8)
	if nu > 0 {
		if u := (room - nd*down) / nu; u >= minUp {
			return down, u
		}
		up = minUp
	}
	if nd > 0 {
		d := (room - nu*up) / nd
		if d >= 3 {
			return min(d, down), up
		}
		down = 3
	}
	if nu > 0 {
		up = max((room-nd*down)/nu, 2)
	}
	return down, up
}

// cardPos is where card i of pile p is drawn.
func (L *solLayout) cardPos(g *solGame, p, i int) (int32, int32) {
	x, y := L.pileX[p], L.pileY[p]
	pile := g.piles[p]
	switch g.kinds[p] {
	case solPileTableau:
		down, up := L.steps(pile)
		for k := 0; k < i && k < len(pile); k++ {
			if pile[k].up {
				y += up
			} else {
				y += down
			}
		}
	case solPileWaste:
		if f := solWasteFanIndex(g, len(pile), i); f > 0 {
			x += int32(f) * L.wasteFan
		}
	}
	return x, y
}

// solWasteFanIndex is card i's place in the waste's fan: Draw 3 shows the
// top three cards spread, Draw 1 stacks them.
func solWasteFanIndex(g *solGame, n, i int) int {
	if g.draw != 3 {
		return 0
	}
	visible := min(n, 3)
	return max(i-(n-visible), 0)
}

// dropPos is where cards put on pile p would lie: on top of a column, at the
// spot of any other pile.
func (L *solLayout) dropPos(g *solGame, p int) (int32, int32) {
	pile := g.piles[p]
	if g.kinds[p] != solPileTableau || len(pile) == 0 {
		if len(pile) > 0 {
			return L.cardPos(g, p, len(pile)-1)
		}
		return L.pileX[p], L.pileY[p]
	}
	down, up := L.steps(pile)
	x, y := L.cardPos(g, p, len(pile)-1)
	if pile[len(pile)-1].up {
		return x, y + up
	}
	return x, y + down
}

// ── Focus ─────────────────────────────────────────────────────────────────

// solTopRow is the top row's piles left to right.
var solTopRow = [...]int{solKStock, solKWaste, solKFound0, solKFound0 + 1, solKFound0 + 2, solKFound0 + 3}

func solInTopRow(p int) bool { return p >= 0 && p < solKTab0 }

// solColumnOf is the column a pile sits in (the top row's piles stand over
// columns 0, 1 and 3 to 6).
func solColumnOf(p int) int {
	switch {
	case p == solKStock:
		return 0
	case p == solKWaste:
		return 1
	case p >= solKFound0 && p < solKFound0+4:
		return 3 + p - solKFound0
	case p >= solKTab0 && p < solKPiles:
		return p - solKTab0
	}
	return 0
}

// solTopAt is the top-row pile over a column; the empty place over column 2
// belongs to the waste.
func solTopAt(col int) int {
	switch {
	case col <= 0:
		return solKStock
	case col <= 2:
		return solKWaste
	}
	return solKFound0 + min(col-3, 3)
}

// solClampFocus keeps the focus on a pile and the depth inside the run that
// can be lifted there.
func solClampFocus() {
	if solUI.focus < 0 || solUI.focus >= solKPiles {
		solUI.focus = solKStock
	}
	g := sol.game
	if g == nil || g.kinds[solUI.focus] != solPileTableau {
		solUI.depth = 1
		return
	}
	solUI.depth = min(max(solUI.depth, 1), max(g.runLen(solUI.focus), 1))
}

// ── Scene contract ────────────────────────────────────────────────────────

// solitaireEnter opens the table: the saved game, or a new deal. The game's
// clock starts from this frame, not from when the screen was last left.
func solitaireEnter(config *Config) {
	solitaireEnsureLoaded()
	if solUI.entered {
		// Entered again without a Leave (the same scene chosen again): land
		// what is still flying first, so a win it completes is counted here
		// rather than taken below for one counted already.
		solSettle()
	}
	solUI.entered = true
	solUI.held = false
	solUI.menuOpen, solUI.statsOpen = false, false
	solUI.menuArmed = -1
	solUI.flying, solUI.autoPending, solUI.finishing = false, false, false
	solUI.cascade.active = false
	solUI.cascade.cards = solUI.cascade.cards[:0]
	solUI.cascade.taken = [solKPiles]int{}
	g := sol.game
	solUI.winHandled = g != nil && g.won
	solUI.winPanel = solUI.winHandled
	sol.lastTick = time.Time{}
	solClampFocus()
}

// solitaireLeave puts down whatever is held, lands any card still flying and
// saves now.
func solitaireLeave() {
	if !sol.loaded {
		return
	}
	solSettle()
	solUI.held = false
	solUI.entered = false
	sol.lastTick = time.Time{}
	solSaveNow(false)
}

// solitaireFlush saves before JukaHub exits, waiting for the card.
func solitaireFlush() {
	if !sol.loaded {
		return
	}
	solSettle()
	solUI.held = false
	solSaveNow(true)
}

// solitaireHubValue is a line for a tile that opens Solitaire: the game in
// progress, else the wins so far. It reads the saved game once per run.
func solitaireHubValue() (string, bool) {
	solitaireEnsureLoaded()
	g := sol.game
	if g != nil && g.counted && !g.won {
		if g.moves == 1 {
			return "Game in progress · 1 move", true
		}
		return fmt.Sprintf("Game in progress · %d moves", g.moves), true
	}
	if sol.stats.Won > 0 {
		return fmt.Sprintf("%d won of %d", sol.stats.Won, sol.stats.Played), true
	}
	return "", false
}

// solitaireWheel: the table does not scroll.
func solitaireWheel(y int32) bool { return false }

// solitaireFooterHints names the keys of what is on screen.
func solitaireFooterHints() []FooterHint {
	back := FooterHint{Button: "B", Label: "Back"}
	switch {
	case solUI.statsOpen:
		return []FooterHint{back}
	case solUI.menuOpen:
		return []FooterHint{
			{Button: "D-Pad", Label: "Choose"},
			{Button: "A", Label: "Select"},
			{Button: "B", Label: "Close"},
		}
	case solUI.cascade.active || solUI.winPanel:
		return []FooterHint{
			{Button: "A", Label: "New game"},
			{Button: "Start / M", Label: "Menu"},
			back,
		}
	case solUI.held:
		return []FooterHint{
			{Button: "D-Pad", Label: "Move"},
			{Button: "A", Label: "Place"},
			{Button: "Y / F", Label: "Auto-place"},
			{Button: "B", Label: "Put back"},
		}
	}
	g := sol.game
	a := FooterHint{Button: "A", Label: "Pick up"}
	draw := FooterHint{Button: "X / D", Label: "Draw"}
	if g != nil {
		if solUI.focus == solKStock {
			a.Label = "Draw"
		}
		if g.canAutoFinish() {
			draw.Label = "Finish"
		}
	}
	hints := []FooterHint{
		{Button: "D-Pad", Label: "Move"},
		a,
		draw,
		{Button: "Y / F", Label: "Smart move"},
		{Button: "L1 / Z", Label: "Undo"},
	}
	if g != nil && len(g.redo) > 0 {
		hints = append(hints, FooterHint{Button: "R1 / R", Label: "Redo"})
	}
	return append(hints, FooterHint{Button: "Start / M", Label: "Menu"}, back)
}

// solitaireClick plays the pile under a click or a tap: the same as A with
// the focus there, and a second click on the same pile is a smart move.
func solitaireClick(mx, my int32, config *Config) bool {
	if my < headerBarHeight() || my >= screenHeight-footerBarHeight() {
		return false
	}
	if !solUI.entered {
		solitaireEnter(config)
	}
	if solSettle() {
		return true
	}
	if solUI.cascade.active {
		solEndCascade()
		return true
	}
	if solUI.statsOpen {
		solUI.statsOpen = false
		PlayBackSound()
		return true
	}
	if solUI.menuOpen {
		for i := 0; i < solMenuRows; i++ {
			if solPointIn(mx, my, solUI.menuRects[i]) {
				solUI.menuRow = i
				solMenuActivate(config)
				return true
			}
		}
		if !solPointIn(mx, my, solUI.menuSheet) {
			solCloseMenu()
		}
		return true
	}
	if solUI.winPanel {
		if solPointIn(mx, my, solUI.winButton) {
			solStartNewGame()
		}
		return true
	}
	L := solComputeLayout()
	p, depth, ok := solHitTest(&L, mx, my)
	now := time.Now()
	if !ok {
		if solUI.held {
			solUI.held = false
			PlayBackSound()
		}
		return true
	}
	if solUI.held && solUI.heldFrom != p {
		// Cards in hand go where the click is; the click after that is a
		// new one, not the second half of a double click.
		solUI.focus = p
		solUI.lastClickPile = -1
		solPlace(p)
		return true
	}
	double := p == solUI.lastClickPile && depth == solUI.lastClickDepth && now.Sub(solUI.lastClickAt) < solDoubleClick
	solUI.lastClickAt, solUI.lastClickPile, solUI.lastClickDepth = now, p, depth
	if double && sol.game.kinds[p] != solPileStock {
		// The first click lifted the cards; the second sends them on.
		solUI.held = false
		solUI.focus, solUI.depth = p, depth
		solClampFocus()
		solUI.lastClickPile = -1
		solDoSmart()
		return true
	}
	solUI.focus = p
	if solUI.held {
		// A slow second click on the pile the cards came from puts them back.
		solPlace(p)
		return true
	}
	solUI.depth = depth
	solClampFocus()
	solPressA()
	return true
}

func solPointIn(x, y int32, r sdl.Rect) bool {
	return r.W > 0 && x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

// solHitTest finds the pile under a point and, in a column, how many cards
// from the top the clicked card is. A click below a column's last card
// still means that column, so dropping is not a precision task.
func solHitTest(L *solLayout, mx, my int32) (pile, depth int, ok bool) {
	g := sol.game
	if g == nil {
		return -1, 0, false
	}
	for _, p := range solTopRow {
		x, y, w := L.pileX[p], L.pileY[p], L.cw
		if p == solKWaste && g.draw == 3 {
			w += 2 * L.wasteFan
		}
		if mx >= x && mx < x+w && my >= y && my < y+L.ch {
			return p, 1, true
		}
	}
	for c := 0; c < 7; c++ {
		p := solKTab0 + c
		x := L.pileX[p]
		if mx < x || mx >= x+L.cw || my < L.tabY {
			continue
		}
		pile := g.piles[p]
		for i := len(pile) - 1; i >= 0; i-- {
			_, y := L.cardPos(g, p, i)
			if my >= y {
				return p, len(pile) - i, true
			}
		}
		return p, 1, true
	}
	return -1, 0, false
}

// ── Actions ───────────────────────────────────────────────────────────────

// solitaireAction runs one input. It reports whether the input was used; Up
// on the top row is left to the header, as on every other screen.
func solitaireAction(act solAction, config *Config) bool {
	if act == solActNone {
		return false
	}
	if !solUI.entered {
		solitaireEnter(config)
	}
	// Input lands every card still flying at once: the player is ahead of
	// the animation, not waiting for it. A press that lands the winning card
	// only does that, so it cannot also answer the "You won" card.
	if solSettle() {
		return true
	}
	if solUI.cascade.active {
		solEndCascade()
		return true
	}
	if solUI.statsOpen {
		switch act {
		case solActA, solActB, solActMenu:
			solUI.statsOpen = false
			PlayBackSound()
		}
		return true
	}
	if solUI.menuOpen {
		return solMenuInput(act, config)
	}
	if solUI.winPanel {
		switch act {
		case solActA, solActNew:
			solStartNewGame()
		case solActB:
			solLeaveScene(config)
		case solActMenu:
			solOpenMenu(solMenuNew)
		}
		return true
	}
	switch act {
	case solActUp, solActDown, solActLeft, solActRight:
		return solNavigate(act)
	case solActA:
		if solUI.held {
			solPlace(solUI.focus)
		} else {
			solPressA()
		}
	case solActB:
		if solUI.held {
			solUI.held = false
			PlayBackSound()
			return true
		}
		solLeaveScene(config)
	case solActDraw:
		solDoDraw()
	case solActSmart:
		solDoSmart()
	case solActUndo, solActRedo:
		solUI.held = false
		g := sol.game
		var ok bool
		if act == solActUndo {
			ok = g.undoStep()
		} else {
			ok = g.redoStep()
		}
		if ok {
			solMarkDirty()
			solClampFocus()
			PlayNavSound()
		} else {
			PlayErrorSound()
		}
	case solActMenu:
		solOpenMenu(solMenuResume)
	case solActNew:
		if solGameUntouched() || sol.game.won {
			solStartNewGame()
		} else {
			solOpenMenu(solMenuNew)
			solUI.menuArmed = solMenuNew
		}
	}
	return true
}

// solLeaveScene goes back, saving first: the game is the user's to resume.
func solLeaveScene(config *Config) {
	solUI.held = false
	solSaveNow(false)
	goBackScene(config)
}

// solNavigate moves the focus. Without cards in hand Up and Down in a column
// choose how deep in its face-up run to lift; with cards in hand they only
// change rows.
func solNavigate(act solAction) bool {
	g := sol.game
	p := solUI.focus
	switch act {
	case solActLeft, solActRight:
		dx := 1
		if act == solActLeft {
			dx = -1
		}
		if solInTopRow(p) {
			i := 0
			for k, q := range solTopRow {
				if q == p {
					i = k
				}
			}
			solUI.focus = solTopRow[(i+dx+len(solTopRow))%len(solTopRow)]
		} else {
			solUI.focus = solKTab0 + (p-solKTab0+dx+7)%7
		}
		solUI.depth = 1
	case solActUp:
		switch {
		case solInTopRow(p):
			// Up from the top row reaches the header, unless cards are held.
			return solUI.held
		case !solUI.held && solUI.depth < g.runLen(p):
			solUI.depth++
		default:
			solUI.focus = solTopAt(solColumnOf(p))
			solUI.depth = 1
		}
	case solActDown:
		switch {
		case solInTopRow(p):
			solUI.focus = solKTab0 + solColumnOf(p)
			solUI.depth = 1
		case !solUI.held && solUI.depth > 1:
			solUI.depth--
		default:
			return true
		}
	}
	solClampFocus()
	PlayNavSound()
	return true
}

// solPressA lifts the focused cards, or turns the stock when it is focused.
func solPressA() {
	g := sol.game
	p := solUI.focus
	if g.kinds[p] == solPileStock {
		solDoDraw()
		return
	}
	n := 1
	if g.kinds[p] == solPileTableau {
		n = max(solUI.depth, 1)
	}
	if !g.canPick(p, n) {
		PlayErrorSound()
		return
	}
	solUI.held, solUI.heldFrom, solUI.heldN = true, p, n
	PlayActivateSound()
}

// solPlace puts the held cards on pile to; on the pile they came from it
// puts them back.
func solPlace(to int) {
	g := sol.game
	if to == solUI.heldFrom {
		solUI.held = false
		PlayBackSound()
		return
	}
	if !g.move(solUI.heldFrom, solUI.heldN, to) {
		PlayErrorSound()
		return
	}
	solUI.held = false
	solUI.focus, solUI.depth = to, 1
	solAfterPlayerMove()
	PlayActivateSound()
}

// solDoDraw turns the stock; once every card is face up it finishes the game
// instead (the stock is empty then, so nothing is lost).
func solDoDraw() {
	g := sol.game
	solUI.held = false
	if g.canAutoFinish() {
		solUI.finishing, solUI.autoPending = true, true
		PlayActivateSound()
		return
	}
	if !g.drawStock() {
		PlayErrorSound()
		return
	}
	solAfterPlayerMove()
	PlayNavSound()
}

// solDoSmart sends the held cards, or the focused ones, where they fit best
// (smartTarget). The focus stays on the pile they left, so pressing again
// keeps working down a column.
func solDoSmart() {
	g := sol.game
	if solUI.held {
		from, n := solUI.heldFrom, solUI.heldN
		to := g.smartTarget(from, n, true)
		if to < 0 || !g.move(from, n, to) {
			PlayErrorSound()
			return
		}
		solUI.held = false
		solUI.focus, solUI.depth = from, 1
		solAfterPlayerMove()
		PlayActivateSound()
		return
	}
	p := solUI.focus
	if g.kinds[p] == solPileStock {
		solDoDraw()
		return
	}
	n, to := g.smartMoveFrom(p, solUI.depth)
	if to < 0 || !g.move(p, n, to) {
		PlayErrorSound()
		return
	}
	solUI.depth = 1
	solAfterPlayerMove()
	PlayActivateSound()
}

// solAfterPlayerMove follows every move the player makes: the bookkeeping,
// and auto-play's turn.
func solAfterPlayerMove() {
	solNoteMove()
	solUI.autoPending = true
	solClampFocus()
}

func solAutoMoveOn() bool { return !sol.opts.AutoMoveOff }

// solStartNewGame deals a new game and clears the table's state.
func solStartNewGame() {
	solNewGame()
	solResetTable()
	PlayActivateSound()
}

func solResetTable() {
	solUI.held = false
	solUI.winPanel, solUI.winHandled = false, false
	solUI.flying, solUI.autoPending, solUI.finishing = false, false, false
	solUI.cascade.active = false
	solUI.focus, solUI.depth = solKStock, 1
}

// ── Auto-play and the win ─────────────────────────────────────────────────

// solUpdate runs the frame's animations: a flight landing, auto-play's next
// card, the win. With reduced motion every auto-played card goes up at once.
func solUpdate(now time.Time, L *solLayout, reduced bool) {
	g := sol.game
	if g == nil {
		return
	}
	if solUI.flying {
		if now.Sub(solUI.flight.start) < solUI.flight.dur {
			requestFrameEvery(16)
			return
		}
		solUI.flying = false
	}
	if solUI.held || solUI.menuOpen {
		// Auto-play waits while cards are in hand: it could otherwise take
		// the very card the player is holding. It waits under the menu too
		// (turning Auto-move on there), so no card flies and no win
		// celebration starts behind the sheet.
		return
	}
	if solUI.autoPending && !g.won {
		if reduced {
			solRunAuto()
		} else if from, to, ok := solNextAuto(); ok {
			fx, fy := L.cardPos(g, from, len(g.piles[from])-1)
			card := g.piles[from][len(g.piles[from])-1]
			g.apply(from, 1, to)
			solMarkDirty()
			tx, ty := L.cardPos(g, to, len(g.piles[to])-1)
			dur := solAutoFlight
			if solUI.finishing {
				dur = solFinishFlight
			}
			solUI.flight = solFlight{card: card, fromX: fx, fromY: fy, toX: tx, toY: ty, start: now, dur: dur, to: to}
			solUI.flying = true
			requestFrameEvery(16)
			return
		} else {
			solUI.autoPending, solUI.finishing = false, false
		}
	}
	if g.won && !solUI.winHandled {
		solOnWin(now, reduced)
	}
	if !g.won && !solUI.autoPending && solAutoMoveOn() && g.canAutoFinish() {
		// Every card is face up: auto-play finishes the game by itself.
		solUI.finishing, solUI.autoPending = true, true
		requestFrameEvery(16)
	}
}

// solNextAuto is auto-play's next card, if it has one: any card while
// finishing, a safe one while auto-play is on, none otherwise.
func solNextAuto() (from, to int, ok bool) {
	g := sol.game
	if !solUI.finishing && !solAutoMoveOn() {
		return -1, -1, false
	}
	return g.nextAutoMove(solUI.finishing)
}

// solRunAuto plays every pending auto-play card at once.
func solRunAuto() {
	g := sol.game
	for i := 0; i < 52; i++ {
		from, to, ok := solNextAuto()
		if !ok {
			break
		}
		g.apply(from, 1, to)
		solMarkDirty()
	}
	solUI.autoPending, solUI.finishing = false, false
}

// solSettle lands every animation now: before input, and before the game is
// saved or left. A win it completes still gets counted; it reports whether
// it did.
func solSettle() bool {
	g := sol.game
	if g == nil {
		return false
	}
	solUI.flying = false
	if solUI.autoPending && !solUI.menuOpen {
		// Under the menu auto-play waits for it to close (solUpdate).
		solRunAuto()
	}
	if g.won && !solUI.winHandled {
		solOnWin(time.Now(), true)
		return true
	}
	return false
}

// solOnWin counts the win once and starts the celebration (or, with reduced
// motion, shows the result straight away).
func solOnWin(now time.Time, quiet bool) {
	solUI.winHandled = true
	solUI.held = false
	solRecordWin()
	solSaveNow(false)
	PlaySuccessSound()
	if quiet {
		solUI.winPanel = true
		return
	}
	c := &solUI.cascade
	c.active, c.start, c.last, c.spawned = true, now, now, 0
	c.cards = c.cards[:0]
	c.taken = [solKPiles]int{}
	c.rng = rand.New(rand.NewSource(now.UnixNano()))
}

// solEndCascade stops the celebration and shows the result.
func solEndCascade() {
	solUI.cascade.active = false
	solUI.cascade.cards = solUI.cascade.cards[:0]
	solUI.cascade.taken = [solKPiles]int{}
	solUI.winPanel = true
}

// solStepCascade moves the celebration on: the foundations launch their
// cards top first, one every solCascadeEvery, and each bounces along the
// floor until it leaves the screen.
func solStepCascade(now time.Time, L *solLayout) {
	c := &solUI.cascade
	g := sol.game
	dt := now.Sub(c.last).Seconds()
	c.last = now
	if dt > 0.05 {
		dt = 0.05
	}
	scale := float64(L.cw) / 114
	for c.spawned < 52 && now.Sub(c.start) >= time.Duration(c.spawned)*solCascadeEvery {
		launched := false
		for k := 0; k < 4 && !launched; k++ {
			f := solKFound0 + (c.spawned+k)%4
			idx := len(g.piles[f]) - 1 - c.taken[f]
			if idx < 0 {
				continue
			}
			c.taken[f]++
			vx := (180 + c.rng.Float64()*260) * scale
			if c.rng.Intn(2) == 0 {
				vx = -vx
			}
			c.cards = append(c.cards, solFly{card: g.piles[f][idx], x: float64(L.pileX[f]), y: float64(L.pileY[f]),
				vx: vx, vy: -c.rng.Float64() * 320 * scale, maxBn: 6})
			launched = true
		}
		c.spawned++
	}
	floor := float64(L.bottom - L.ch)
	grav := 1500 * scale
	live := c.cards[:0]
	for _, f := range c.cards {
		f.vy += grav * dt
		f.x += f.vx * dt
		f.y += f.vy * dt
		if f.y > floor {
			f.y = floor
			f.vy = -f.vy * 0.72
			f.bounces++
		}
		if f.x > -float64(L.cw) && f.x < float64(screenWidth) && f.bounces <= f.maxBn {
			live = append(live, f)
		}
	}
	c.cards = live
	if (c.spawned >= 52 && len(c.cards) == 0) || now.Sub(c.start) > solCascadeMax {
		solEndCascade()
		return
	}
	requestFrameEvery(16)
}

// ── Menu ──────────────────────────────────────────────────────────────────

func solOpenMenu(row int) {
	solUI.held = false
	solUI.menuOpen = true
	solUI.menuRow = row
	solUI.menuArmed = -1
	PlayActivateSound()
}

func solCloseMenu() {
	solUI.menuOpen = false
	solUI.menuArmed = -1
	PlayBackSound()
}

// solMenuInput walks and runs the menu. Left and Right flip the two
// settings in place.
func solMenuInput(act solAction, config *Config) bool {
	switch act {
	case solActUp:
		solUI.menuRow = (solUI.menuRow + solMenuRows - 1) % solMenuRows
		solUI.menuArmed = -1
		PlayNavSound()
	case solActDown:
		solUI.menuRow = (solUI.menuRow + 1) % solMenuRows
		solUI.menuArmed = -1
		PlayNavSound()
	case solActLeft, solActRight:
		if solUI.menuRow == solMenuDraw || solUI.menuRow == solMenuAuto {
			solMenuActivate(config)
		}
	case solActA:
		solMenuActivate(config)
	case solActB, solActMenu:
		solCloseMenu()
	case solActNew:
		solUI.menuRow = solMenuNew
		solMenuActivate(config)
	}
	return true
}

// solGameInProgress reports whether leaving this deal would lose play.
func solGameInProgress() bool {
	g := sol.game
	return g != nil && !g.won && !solGameUntouched()
}

// solMenuActivate runs the focused row. New game and Restart ask for a
// second A while a game is in progress: one stray press must not throw a
// game away.
func solMenuActivate(config *Config) {
	row := solUI.menuRow
	switch row {
	case solMenuResume:
		solCloseMenu()
	case solMenuNew, solMenuRestart:
		if solGameInProgress() && solUI.menuArmed != row {
			solUI.menuArmed = row
			PlayNavSound()
			return
		}
		solUI.menuOpen, solUI.menuArmed = false, -1
		if row == solMenuNew {
			solStartNewGame()
		} else {
			solRestartDeal()
			solResetTable()
			PlayActivateSound()
		}
	case solMenuDraw:
		sol.opts.Draw = 4 - sol.opts.Draw // 1 ↔ 3
		if sol.opts.Draw != 3 {
			sol.opts.Draw = 1
		}
		if g := sol.game; g != nil && solGameUntouched() {
			// Nothing played yet: the same deal, turned the new way.
			g.draw = sol.opts.Draw
		} else {
			ShowToast(fmt.Sprintf("Draw %d starts with the next deal", sol.opts.Draw), ToastKindInfo)
		}
		solMarkDirty()
		PlayNavSound()
	case solMenuAuto:
		sol.opts.AutoMoveOff = !sol.opts.AutoMoveOff
		if solAutoMoveOn() {
			solUI.autoPending = true
		}
		solMarkDirty()
		PlayNavSound()
	case solMenuStats:
		solUI.statsOpen = true
		PlayActivateSound()
	case solMenuQuit:
		solUI.menuOpen = false
		solLeaveScene(config)
	}
}

// solMenuLabel is a row's name and, for the settings, its value.
func solMenuLabel(row int) (string, string) {
	armed := solUI.menuArmed == row
	switch row {
	case solMenuResume:
		return "Resume", ""
	case solMenuNew:
		if armed {
			return "Abandon this game?", hintSentence("%s again", "A", "New game")
		}
		return "New game", ""
	case solMenuRestart:
		if armed {
			return "Start this deal over?", hintSentence("%s again", "A", "Restart")
		}
		return "Restart this deal", ""
	case solMenuDraw:
		if sol.opts.Draw == 3 {
			return "Draw", "3 cards"
		}
		return "Draw", "1 card"
	case solMenuAuto:
		if solAutoMoveOn() {
			return "Auto-move", "On"
		}
		return "Auto-move", "Off"
	case solMenuStats:
		return "Statistics", ""
	case solMenuQuit:
		return "Quit", ""
	}
	return "", ""
}

// ── Keyboard and pad ──────────────────────────────────────────────────────

// solKeyAction maps a key to an action. Every pad button has its twin here:
// arrows, Enter/Space (A), Esc/Backspace (B), D or X (draw), F, Tab or Y
// (smart move), Z or Ctrl+Z (undo), R, Ctrl+Y or Ctrl+Shift+Z (redo), M
// (menu) and N (new game).
func solKeyAction(e *sdl.KeyboardEvent) solAction {
	ctrl := e.Keysym.Mod&sdl.KMOD_CTRL != 0
	shift := e.Keysym.Mod&sdl.KMOD_SHIFT != 0
	repeat := e.Repeat != 0
	switch e.Keysym.Sym {
	case sdl.K_UP:
		return solActUp
	case sdl.K_DOWN:
		return solActDown
	case sdl.K_LEFT:
		return solActLeft
	case sdl.K_RIGHT:
		return solActRight
	case sdl.K_z:
		if ctrl && shift {
			return solActRedo
		}
		return solActUndo // held down, it rewinds
	case sdl.K_u:
		return solActUndo
	case sdl.K_r:
		if !ctrl {
			return solActRedo
		}
	case sdl.K_y:
		if ctrl {
			return solActRedo
		}
		if !repeat {
			return solActSmart
		}
	}
	if repeat || ctrl {
		return solActNone
	}
	switch e.Keysym.Sym {
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		return solActA
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		return solActB
	case sdl.K_d, sdl.K_x:
		return solActDraw
	case sdl.K_f, sdl.K_TAB:
		return solActSmart
	case sdl.K_m:
		return solActMenu
	case sdl.K_n:
		return solActNew
	}
	return solActNone
}

func solPadAction(button uint8) solAction {
	switch button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		return solActUp
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		return solActDown
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		return solActLeft
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		return solActRight
	case sdl.CONTROLLER_BUTTON_A:
		return solActA
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		return solActB
	case sdl.CONTROLLER_BUTTON_X:
		return solActDraw
	case sdl.CONTROLLER_BUTTON_Y:
		return solActSmart
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		return solActUndo
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		return solActRedo
	case sdl.CONTROLLER_BUTTON_START:
		return solActMenu
	}
	return solActNone
}

// ── Drawing ───────────────────────────────────────────────────────────────

var (
	solFaceFill  = sdl.Color{R: 250, G: 250, B: 246, A: 255}
	solFaceEdge  = sdl.Color{R: 0, G: 0, B: 0, A: 70}
	solRedInk    = sdl.Color{R: 204, G: 38, B: 50, A: 255}
	solBlackInk  = sdl.Color{R: 26, G: 28, B: 34, A: 255}
	solShadowInk = sdl.Color{R: 0, G: 0, B: 0, A: 255}
)

var solRankText = [14]string{"", "A", "2", "3", "4", "5", "6", "7", "8", "9", "10", "J", "Q", "K"}

// solRankFont is the face the ranks are printed in: the bundled semi-bold
// Inter at the card's size. Card faces are pictures, so the text-size
// setting does not stretch them out of their corners.
func solRankFont(config *Config, px int) *ttf.Font {
	if f := loadCachedFont(homeUIFontBoldFile, px); f != nil {
		return f
	}
	return getFontSized(config, "medium", px)
}

// renderSolitaire draws the table and runs its frame: the animations, the
// game clock and the debounced save.
func renderSolitaire(renderer *sdl.Renderer, config *Config) {
	if !solUI.entered {
		solitaireEnter(config)
	}
	g := sol.game
	now := time.Now()
	reduced := effectiveReducedMotion(config)
	L := solComputeLayout()
	if solUI.cascade.active && reduced {
		solEndCascade()
	}
	solUpdate(now, &L, reduced)
	solTickClock(now, !solUI.menuOpen && !solUI.statsOpen)
	if !g.won || solUI.winHandled {
		// Not while the winning card is still in flight: that save would
		// carry the won game without its win in the statistics.
		solSaveIfDue(now)
	}
	if (g.counted && !g.won && !solUI.menuOpen && !solUI.statsOpen) || sol.dirty {
		// The clock on the table ticks, and a change waiting for its
		// debounced write gets the frame that writes it (a menu setting,
		// a move made just before the menu opened).
		requestFrameEvery(500)
	}

	drawAppBackdrop(renderer, config, screenWidth, screenHeight)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)

	for p := 0; p < solKPiles; p++ {
		solDrawPile(renderer, config, &L, p)
	}
	solDrawInfo(renderer, config, &L)
	if solUI.flying {
		f := &solUI.flight
		t := float64(now.Sub(f.start)) / float64(f.dur)
		t = min(max(t, 0), 1)
		t = 1 - (1-t)*(1-t) // ease out
		x := f.fromX + int32(float64(f.toX-f.fromX)*t)
		y := f.fromY + int32(float64(f.toY-f.fromY)*t)
		solDrawShadow(renderer, &L, x, y, L.ch)
		solDrawCard(renderer, config, &L, f.card, x, y)
	}
	if !solUI.cascade.active && !solUI.winPanel {
		solDrawHeldAndFocus(renderer, config, &L)
	}
	if solUI.cascade.active {
		solStepCascade(now, &L)
		for _, f := range solUI.cascade.cards {
			solDrawCard(renderer, config, &L, f.card, int32(f.x), int32(f.y))
		}
	}
	if solUI.winPanel {
		solDrawWinPanel(renderer, config)
	}
	if solUI.menuOpen {
		solDrawMenu(renderer, config)
		if solUI.statsOpen {
			solDrawStats(renderer, config)
		}
	}

	renderStatusBar(renderer, config)
	renderFooter(renderer, config)
}

// solHiddenTop is how many cards at the top of pile p are drawn elsewhere
// this frame: the held ones, one still flying there, those launched in the
// celebration.
func solHiddenTop(p int) int {
	n := 0
	if solUI.held && solUI.heldFrom == p {
		n += solUI.heldN
	}
	if solUI.flying && solUI.flight.to == p {
		n++
	}
	if solUI.cascade.active {
		n += solUI.cascade.taken[p]
	}
	return n
}

// solDrawPile draws one pile as it lies, without its hidden top cards.
func solDrawPile(r *sdl.Renderer, config *Config, L *solLayout, p int) {
	g := sol.game
	pile := g.piles[p]
	show := max(len(pile)-solHiddenTop(p), 0)
	x, y := L.pileX[p], L.pileY[p]
	if show == 0 {
		solDrawEmpty(r, config, L, p, x, y)
		return
	}
	switch g.kinds[p] {
	case solPileStock:
		// A thicker stack reads as more cards: one edge per eight.
		edges := min((show-1)/8, 3)
		for k := edges; k >= 1; k-- {
			solDrawBack(r, config, L, x+int32(k)*2, y+int32(k)*2)
		}
		solDrawBack(r, config, L, x, y)
	case solPileTableau:
		for i := 0; i < show; i++ {
			cx, cy := L.cardPos(g, p, i)
			solDrawCard(r, config, L, pile[i], cx, cy)
		}
	default:
		// The waste and the foundations: the top card, the one under it (so
		// a card lifted or flying off uncovers something) and, on a Draw 3
		// waste, the fan.
		from := max(show-2, 0)
		if g.kinds[p] == solPileWaste && g.draw == 3 {
			from = max(show-4, 0)
		}
		for i := from; i < show; i++ {
			cx, cy := L.cardPos(g, p, i)
			if g.kinds[p] == solPileWaste && g.draw == 3 {
				// Fan the cards that show now that some may be hidden.
				cx = L.pileX[p] + int32(solWasteFanIndex(g, show, i))*L.wasteFan
			}
			solDrawCard(r, config, L, pile[i], cx, cy)
		}
	}
}

// solDrawEmpty outlines an empty pile: an A on a foundation, a ring on a
// stock that can be turned over again.
func solDrawEmpty(r *sdl.Renderer, config *Config, L *solLayout, p int, x, y int32) {
	g := sol.game
	line := WithAlpha(ColorTextTertiary(), 110)
	fillRoundedRect(r, x, y, L.cw, L.ch, L.radius, WithAlpha(solShadowInk, 40))
	strokeRoundedRect(r, x, y, L.cw, L.ch, L.radius, 2, line)
	cx, cy := x+L.cw/2, y+L.ch/2
	switch g.kinds[p] {
	case solPileFoundation:
		if font := solRankFont(config, L.rankPx+L.rankPx/2); font != nil {
			drawTextRaw(r, font, "A", cx, cy-int32(font.Height())/2, line, textAlignCenter)
		}
	case solPileStock:
		if len(g.piles[solKWaste]) > 0 {
			rad := L.cw / 5
			strokeCircle(r, cx, cy, rad, line)
			strokeCircle(r, cx, cy, rad-1, line)
			fillCircle(r, cx+rad, cy, max(rad/4, 2), line)
		}
	}
}

// solDrawCard draws a card face up or down at (x, y).
func solDrawCard(r *sdl.Renderer, config *Config, L *solLayout, c solCard, x, y int32) {
	if !c.up {
		solDrawBack(r, config, L, x, y)
		return
	}
	fillRoundedRect(r, x, y, L.cw, L.ch, L.radius, solFaceFill)
	strokeRoundedRect(r, x, y, L.cw, L.ch, L.radius, 1, solFaceEdge)
	ink := solBlackInk
	if c.red() {
		ink = solRedInk
	}
	inset := max(L.cw/14, 3)
	if font := solRankFont(config, L.rankPx); font != nil {
		drawTextRaw(r, font, solRankText[c.rank], x+inset, y+inset/2, ink, textAlignLeft)
	}
	// The small suit sits beside the rank, in the strip a fanned column
	// leaves showing.
	solDrawSuit(r, c.suit, x+L.cw-inset-L.pip, y+inset, L.pip, ink)
	band := int32(L.rankPx) + inset
	bigY := y + band + (L.ch-band-L.bigPip)/2
	solDrawSuit(r, c.suit, x+(L.cw-L.bigPip)/2, bigY, L.bigPip, ink)
	if c.rank >= 11 {
		// Court cards get a frame around the big pip, so a K reads as more
		// than a large suit at a glance.
		pad := L.bigPip / 4
		strokeRoundedRect(r, x+(L.cw-L.bigPip)/2-pad, bigY-pad, L.bigPip+2*pad, L.bigPip+2*pad, L.radius, 1, WithAlpha(ink, 120))
	}
}

// solDrawBack is a face-down card in the theme's accent.
func solDrawBack(r *sdl.Renderer, config *Config, L *solLayout, x, y int32) {
	acc := getAccentColor(config)
	fillRoundedRect(r, x, y, L.cw, L.ch, L.radius, glassMix(acc, solShadowInk, 0.45))
	inset := max(L.cw/12, 3)
	strokeRoundedRect(r, x+inset, y+inset, L.cw-2*inset, L.ch-2*inset, max(L.radius-inset/2, 2), 1, WithAlpha(glassMix(acc, solFaceFill, 0.35), 200))
	emblem := L.cw * 36 / 100
	solDrawSuit(r, solDiamonds, x+(L.cw-emblem)/2, y+(L.ch-emblem)/2, emblem, WithAlpha(glassMix(acc, solFaceFill, 0.25), 150))
	strokeRoundedRect(r, x, y, L.cw, L.ch, L.radius, 1, WithAlpha(solShadowInk, 110))
}

// solDrawShadow is the soft shadow under lifted cards, h tall.
func solDrawShadow(r *sdl.Renderer, L *solLayout, x, y, h int32) {
	off := max(L.cw/24, 3)
	fillRoundedRect(r, x+off, y+off*2, L.cw, h, L.radius, WithAlpha(solShadowInk, 70))
}

// solDrawHeldAndFocus draws the focus ring and the held cards over the
// focused pile, lifted, with the ring dimmed where they cannot go.
func solDrawHeldAndFocus(r *sdl.Renderer, config *Config, L *solLayout) {
	g := sol.game
	ring := glassAccent()
	p := solUI.focus
	lift := L.ch / 9
	if solUI.held {
		src := g.piles[solUI.heldFrom]
		if len(src) < solUI.heldN {
			solUI.held = false
			return
		}
		cards := src[len(src)-solUI.heldN:]
		var x, y int32
		if p == solUI.heldFrom {
			x, y = L.cardPos(g, p, len(src)-solUI.heldN)
		} else {
			x, y = L.dropPos(g, p)
			if !g.canDrop(cards, p) {
				ring = WithAlpha(ColorTextTertiary(), 200)
			}
		}
		y -= lift
		step := L.upStep
		if n := int32(len(cards)); n > 1 {
			// Keep a long run above the footer.
			if room := L.bottom - y - L.ch; room < step*(n-1) {
				step = max(room/(n-1), 8)
			}
		}
		h := L.ch + int32(len(cards)-1)*step
		solDrawShadow(r, L, x, y, h)
		for i, c := range cards {
			solDrawCard(r, config, L, c, x, y+int32(i)*step)
		}
		solDrawRing(r, L, x, y, h, ring)
		return
	}
	pile := g.piles[p]
	x, y := L.pileX[p], L.pileY[p]
	h := L.ch
	switch {
	case len(pile) == 0:
	case g.kinds[p] == solPileTableau:
		d := min(max(solUI.depth, 1), len(pile))
		x, y = L.cardPos(g, p, len(pile)-d)
		_, ly := L.cardPos(g, p, len(pile)-1)
		h = ly + L.ch - y
	case g.kinds[p] == solPileWaste:
		x, y = L.cardPos(g, p, len(pile)-1)
	}
	solDrawRing(r, L, x, y, h, ring)
}

// solDrawRing is the focus ring: a thick outline just outside the cards.
func solDrawRing(r *sdl.Renderer, L *solLayout, x, y, h int32, c sdl.Color) {
	pad := max(L.cw/30, 3)
	th := max(L.cw/40, 3)
	strokeRoundedRect(r, x-pad, y-pad, L.cw+2*pad, h+2*pad, L.radius+pad, th, c)
}

// solDrawInfo prints the clock and the move count in the empty place over
// the third column, when there is room for them.
func solDrawInfo(r *sdl.Renderer, config *Config, L *solLayout) {
	g := sol.game
	left := L.pileX[solKWaste] + L.cw
	if g.draw == 3 {
		left += 2 * L.wasteFan
	}
	right := L.pileX[solKFound0]
	if right-left < 40 {
		return
	}
	cx := (left + right) / 2
	big, _ := getCachedFont(config, "medium")
	small, _ := getCachedFont(config, "small")
	if big == nil || small == nil {
		return
	}
	clock := solFormatDuration(g.elapsed)
	if w, _, _ := big.SizeUTF8(clock); int32(w) > right-left-8 {
		big = small
	}
	moves := fmt.Sprintf("%d moves", g.moves)
	if g.moves == 1 {
		moves = "1 move"
	}
	if w, _, _ := small.SizeUTF8(moves); int32(w) > right-left-8 {
		moves = fmt.Sprintf("%d", g.moves)
	}
	y := L.top + L.ch/2 - int32(big.Height()+small.Height())/2
	drawTextRaw(r, big, clock, cx, y, ColorTextPrimary(), textAlignCenter)
	drawText(r, small, moves, cx, y+int32(big.Height()), ColorTextSecondary(), textAlignCenter)
	if g.draw == 3 {
		drawText(r, small, "Draw 3", cx, y+int32(big.Height()+small.Height()), ColorTextTertiary(), textAlignCenter)
	}
}

// solDrawSheet dims the table and draws an overlay card, returning its
// inner rect.
func solDrawSheet(r *sdl.Renderer, w, h int32, dim bool) sdl.Rect {
	if dim {
		setDrawColor(r, WithAlpha(solShadowInk, 130))
		r.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	}
	w = min(w, screenWidth-24)
	h = min(h, screenHeight-headerBarHeight()-footerBarHeight()-12)
	x := (screenWidth - w) / 2
	y := headerBarHeight() + (screenHeight-headerBarHeight()-footerBarHeight()-h)/2
	fillRoundedRect(r, x, y, w, h, 16, glassSheetFill())
	strokeRoundedRect(r, x, y, w, h, 16, 1, glassHairline())
	return sdl.Rect{X: x, Y: y, W: w, H: h}
}

// solDrawMenu is the game menu.
func solDrawMenu(r *sdl.Renderer, config *Config) {
	font, _ := getCachedFont(config, "medium")
	small, _ := getCachedFont(config, "small")
	if font == nil || small == nil {
		return
	}
	acc := getAccentColor(config)
	rowH := int32(font.Height()) + 14
	if screenHeight < 600 {
		rowH = int32(font.Height()) + 6
	}
	titleH := int32(font.Height()) + 20
	sheet := solDrawSheet(r, 460, titleH+rowH*solMenuRows+20, true)
	solUI.menuSheet = sheet
	pad := int32(20)
	drawText(r, font, "Solitaire", sheet.X+pad, sheet.Y+12, ColorTextPrimary(), textAlignLeft)
	sub := "Klondike"
	drawText(r, small, sub, sheet.X+sheet.W-pad, sheet.Y+12+int32(font.Height()-small.Height())/2, ColorTextTertiary(), textAlignRight)
	y := sheet.Y + titleH
	for i := 0; i < solMenuRows; i++ {
		rect := sdl.Rect{X: sheet.X + 8, Y: y, W: sheet.W - 16, H: rowH}
		solUI.menuRects[i] = rect
		label, value := solMenuLabel(i)
		col := ColorTextPrimary()
		if i == solUI.menuRow {
			fillRoundedRect(r, rect.X, rect.Y, rect.W, rect.H, 10, WithAlpha(acc, 60))
			col = acc
		}
		if solUI.menuArmed == i {
			col = sdl.Color{R: 240, G: 170, B: 70, A: 255}
		}
		ty := rect.Y + (rowH-int32(font.Height()))/2
		drawText(r, font, label, rect.X+12, ty, col, textAlignLeft)
		if value != "" {
			vy := rect.Y + (rowH-int32(small.Height()))/2
			drawText(r, small, value, rect.X+rect.W-12, vy, ColorTextSecondary(), textAlignRight)
		}
		y += rowH
	}
}

// solDrawStats is the statistics card, over the menu.
func solDrawStats(r *sdl.Renderer, config *Config) {
	font, _ := getCachedFont(config, "medium")
	small, _ := getCachedFont(config, "small")
	if font == nil || small == nil {
		return
	}
	s := sol.stats
	rate := "—"
	if s.Played > 0 {
		rate = fmt.Sprintf("%d%%", s.Won*100/s.Played)
	}
	best, fewest := "—", "—"
	if s.BestTimeMs > 0 {
		best = solFormatDuration(time.Duration(s.BestTimeMs) * time.Millisecond)
	}
	if s.FewestMoves > 0 {
		fewest = fmt.Sprintf("%d", s.FewestMoves)
	}
	rows := [...][2]string{
		{"Games played", fmt.Sprintf("%d", s.Played)},
		{"Games won", fmt.Sprintf("%d", s.Won)},
		{"Win rate", rate},
		{"Current streak", fmt.Sprintf("%d", s.Streak)},
		{"Best streak", fmt.Sprintf("%d", s.BestStreak)},
		{"Best time", best},
		{"Fewest moves", fewest},
	}
	rowH := int32(small.Height()) + 10
	titleH := int32(font.Height()) + 20
	sheet := solDrawSheet(r, 420, titleH+rowH*int32(len(rows))+20, false)
	pad := int32(24)
	drawText(r, font, "Statistics", sheet.X+pad, sheet.Y+12, ColorTextPrimary(), textAlignLeft)
	y := sheet.Y + titleH
	for _, row := range rows {
		drawText(r, small, row[0], sheet.X+pad, y, ColorTextSecondary(), textAlignLeft)
		drawTextRaw(r, small, row[1], sheet.X+sheet.W-pad, y, ColorTextPrimary(), textAlignRight)
		y += rowH
	}
}

// solDrawWinPanel is the result card after a win, with the New game button.
func solDrawWinPanel(r *sdl.Renderer, config *Config) {
	big, _ := getCachedFont(config, "large")
	font, _ := getCachedFont(config, "medium")
	small, _ := getCachedFont(config, "small")
	if big == nil || font == nil || small == nil {
		return
	}
	g := sol.game
	s := sol.stats
	acc := getAccentColor(config)
	lines := []string{
		fmt.Sprintf("%s · %d moves", solFormatDuration(g.elapsed), g.moves),
	}
	if s.BestTimeMs > 0 {
		lines = append(lines, fmt.Sprintf("Best %s · fewest moves %d",
			solFormatDuration(time.Duration(s.BestTimeMs)*time.Millisecond), s.FewestMoves))
	}
	lines = append(lines, fmt.Sprintf("Won %d of %d · streak %d", s.Won, s.Played, s.Streak))
	lineH := int32(small.Height()) + 6
	btnH := int32(font.Height()) + 16
	h := int32(big.Height()) + 24 + lineH*int32(len(lines)) + 20 + btnH + 24
	sheet := solDrawSheet(r, 440, h, false)
	cx := sheet.X + sheet.W/2
	y := sheet.Y + 18
	drawText(r, big, "You won!", cx, y, acc, textAlignCenter)
	y += int32(big.Height()) + 10
	for _, l := range lines {
		drawTextRaw(r, small, l, cx, y, ColorTextSecondary(), textAlignCenter)
		y += lineH
	}
	y += 14
	label := hintSentence("%s New game", "A", "New game")
	w, _, _ := font.SizeUTF8(label)
	bw := int32(w) + 48
	btn := sdl.Rect{X: cx - bw/2, Y: y, W: bw, H: btnH}
	solUI.winButton = btn
	fillRoundedRect(r, btn.X, btn.Y, btn.W, btn.H, btn.H/2, acc)
	drawTextRaw(r, font, label, cx, btn.Y+(btnH-int32(font.Height()))/2, ColorIconDark, textAlignCenter)
}

// ── Suits ─────────────────────────────────────────────────────────────────

// solSuitTex holds one baked texture per suit and size, for the renderer
// that made them.
var solSuitTex struct {
	owner  *sdl.Renderer
	tex    map[int32]*sdl.Texture // suit<<16 | size; nil = could not be made
	canvas vecCanvas
	path   vecPath
	mask   []uint8
	pixels []uint32
}

// solSuitTexture is suit drawn white at size×size, nil when the renderer
// cannot make it (the caller then draws a plain dot).
func solSuitTexture(r *sdl.Renderer, suit uint8, size int32) *sdl.Texture {
	if r == nil || size <= 0 || size > 512 {
		return nil
	}
	st := &solSuitTex
	if st.owner != r || st.tex == nil {
		// Textures belong to the renderer that made them; a new renderer
		// starts over (the old textures went with it).
		st.owner = r
		st.tex = map[int32]*sdl.Texture{}
	}
	key := int32(suit)<<16 | size
	if t, ok := st.tex[key]; ok {
		return t
	}
	if len(st.tex) >= 32 {
		// Many window sizes have come and gone: drop the old sizes.
		for _, t := range st.tex {
			if t != nil {
				t.Destroy()
			}
		}
		st.tex = map[int32]*sdl.Texture{}
	}
	n := int(size)
	st.canvas.Reset(n, n)
	solSuitPaint(&st.canvas, &st.path, suit, float64(size))
	if cap(st.mask) < n*n {
		st.mask = make([]uint8, n*n)
		st.pixels = make([]uint32, n*n)
	}
	st.mask, st.pixels = st.mask[:n*n], st.pixels[:n*n]
	st.canvas.AlphaBytes(st.mask)
	for i, a := range st.mask {
		// White ink with the coverage in alpha, as the icon atlas bakes.
		st.pixels[i] = uint32(a)<<24 | 0x00FFFFFF
	}
	t, err := r.CreateTexture(uint32(sdl.PIXELFORMAT_ARGB8888), sdl.TEXTUREACCESS_STATIC, size, size)
	if err == nil {
		if err = t.SetBlendMode(sdl.BLENDMODE_BLEND); err == nil {
			err = t.UpdateRGBA(nil, st.pixels, n)
		}
		if err != nil {
			t.Destroy()
			t = nil
		}
	}
	st.tex[key] = t
	return t
}

// solDrawSuit draws a suit in col with its top-left at (x, y).
func solDrawSuit(r *sdl.Renderer, suit uint8, x, y, size int32, col sdl.Color) {
	if t := solSuitTexture(r, suit, size); t != nil {
		_ = t.SetColorMod(col.R, col.G, col.B)
		_ = t.SetAlphaMod(col.A)
		_ = r.Copy(t, nil, &sdl.Rect{X: x, Y: y, W: size, H: size})
		return
	}
	fillCircle(r, x+size/2, y+size/2, max(size/3, 1), col)
}

// solHeartCurve is a heart in a 24-unit box, point down: a start point and
// six cubic segments of (c1, c2, end).
var solHeartCurve = [...][6]float64{
	{7, 17, 1.5, 13.4, 1.5, 8.4},
	{1.5, 5.2, 4, 3, 6.9, 3},
	{9, 3, 10.8, 4.2, 12, 6.1},
	{13.2, 4.2, 15, 3, 17.1, 3},
	{20, 3, 22.5, 5.2, 22.5, 8.4},
	{22.5, 13.4, 17, 17, 12, 21},
}

// solSuitPaint paints a suit's shape over a size×size canvas. Each part is
// its own paint, so overlapping parts join whatever their winding.
func solSuitPaint(cv *vecCanvas, p *vecPath, suit uint8, size float64) {
	k := size / 24
	fill := vecPaint{Opacity: 1}
	heart := func(tf func(x, y float64) (float64, float64)) {
		p.Reset()
		x, y := tf(12, 21)
		p.MoveTo(x*k, y*k)
		for _, s := range solHeartCurve {
			x1, y1 := tf(s[0], s[1])
			x2, y2 := tf(s[2], s[3])
			x3, y3 := tf(s[4], s[5])
			p.CubicTo(x1*k, y1*k, x2*k, y2*k, x3*k, y3*k)
		}
		p.Close()
		cv.Paint(p, fill)
	}
	stem := func(top float64) {
		p.Reset()
		p.MoveTo(12*k, top*k)
		p.LineTo(16*k, 22.5*k)
		p.LineTo(8*k, 22.5*k)
		p.Close()
		cv.Paint(p, fill)
	}
	switch suit {
	case solHearts:
		heart(func(x, y float64) (float64, float64) { return x, y + 0.5 })
	case solDiamonds:
		p.Reset()
		p.MoveTo(12*k, 1.5*k)
		p.LineTo(20*k, 12*k)
		p.LineTo(12*k, 22.5*k)
		p.LineTo(4*k, 12*k)
		p.Close()
		cv.Paint(p, fill)
	case solSpades:
		// The heart turned over and lifted, on a flared stem.
		heart(func(x, y float64) (float64, float64) {
			return 12 + (x-12)*0.92, 1.5 + (21-y)*0.92
		})
		stem(13)
	case solClubs:
		for _, c := range [...][2]float64{{12, 6.8}, {6.6, 13.6}, {17.4, 13.6}} {
			p.Reset()
			p.Circle(c[0]*k, c[1]*k, 4.9*k)
			cv.Paint(p, fill)
		}
		p.Reset()
		p.Circle(12*k, 12*k, 2.8*k)
		cv.Paint(p, fill)
		stem(11)
	}
}

// ── Scene registration ────────────────────────────────────────────────────

type solitaireScene struct{ sceneBase }

func (solitaireScene) render(r *sdl.Renderer, c *Config) bool {
	renderSolitaire(r, c)
	return true
}

func (solitaireScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return true
	}
	act := solKeyAction(e)
	if act == solActNone {
		return false
	}
	return solitaireAction(act, c)
}

func (solitaireScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	act := solPadAction(e.Button)
	if act == solActNone {
		return false
	}
	return solitaireAction(act, c)
}

func init() { registerScene(solitaireScene{sceneBase{name: solitaireSceneName}}) }
