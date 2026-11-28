package main

// solitaire.go — the rules, the state and the saved game of Solitaire, one of
// the everyday apps (osapps.go). The drawing and the controls live in
// solitaire_ui.go.
//
// The game is Klondike: a stock, a waste, four foundations and seven columns,
// turning one card (the default) or three, with as many passes through the
// stock as the player likes. The rules are written over a list of piles that
// each have a kind (stock, waste, foundation, column, free cell) rather than
// over Klondike's fixed thirteen, so a second layout — FreeCell, with its four
// cells and eight columns — is a new pile list, a deal and a few rule
// branches, not a rewrite.
//
// The whole game is a few hundred bytes of text: every pile is a string of
// two-letter cards ("TH" is the ten of hearts, a lower-case suit letter is a
// face-down card), so an undo step is one string and the saved game is the
// current piles plus up to solUndoCap earlier ones. It is written to
// solitaire.json in JukaHub's data folder after every move (debounced) and
// whenever the screen is left, so a game can be put down at any moment and
// picked up exactly where it was, undo history included.

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// solSaveFileName is the saved game and the statistics, in
	// osAppsDataPath.
	solSaveFileName = "solitaire.json"
	// solSaveVersion is written into the file. A reader keeps whatever it
	// understands of a newer file and starts a new deal if the game itself
	// does not parse.
	solSaveVersion = 2
	// solUndoCap bounds the undo history, in memory and on the card. Each
	// step is about 120 bytes, so the full history is some 25 KB.
	solUndoCap = 200
	// solSaveDelay is how long the game waits after a move before writing,
	// so a burst of moves is one write to the SD card, not one each.
	solSaveDelay = 1500 * time.Millisecond

	solVariantKlondike = "klondike"
	solVariantFreeCell = "freecell"
)

// FreeCell's sixteen piles: four cells, four foundations and eight columns,
// all face up. solMaxPiles also bounds the UI's per-pile arrays.
const (
	solMaxPiles = 16
	solFCell0   = 0
	solFFound0  = 4
	solFTab0    = 8
	solFPiles   = 16
)

var solFreeCellKinds = [solFPiles]solPileKind{
	solPileCell, solPileCell, solPileCell, solPileCell,
	solPileFoundation, solPileFoundation, solPileFoundation, solPileFoundation,
	solPileTableau, solPileTableau, solPileTableau, solPileTableau,
	solPileTableau, solPileTableau, solPileTableau, solPileTableau,
}

// Suits, in the order their letters appear in solSuitChars.
const (
	solSpades uint8 = iota
	solHearts
	solDiamonds
	solClubs
)

const (
	solRankChars = "A23456789TJQK"
	solSuitChars = "SHDC"
)

// solCard is one card: a suit, a rank from 1 (ace) to 13 (king), and whether
// it lies face up.
type solCard struct {
	suit uint8
	rank uint8
	up   bool
}

func solSuitRed(suit uint8) bool { return suit == solHearts || suit == solDiamonds }

func (c solCard) red() bool { return solSuitRed(c.suit) }

// code is the card's two letters: rank then suit, the suit in lower case
// while the card is face down.
func (c solCard) code() string {
	if c.rank < 1 || c.rank > 13 || c.suit > 3 {
		return "??"
	}
	s := solSuitChars[c.suit]
	if !c.up {
		s += 'a' - 'A'
	}
	return string([]byte{solRankChars[c.rank-1], s})
}

// solParseCard reads two letters written by code.
func solParseCard(r, s byte) (solCard, bool) {
	rank := strings.IndexByte(solRankChars, r)
	up := true
	if s >= 'a' && s <= 'z' {
		up = false
		s -= 'a' - 'A'
	}
	suit := strings.IndexByte(solSuitChars, s)
	if rank < 0 || suit < 0 {
		return solCard{}, false
	}
	return solCard{suit: uint8(suit), rank: uint8(rank + 1), up: up}, true
}

// solPileKind is what a pile is for, which decides what may be taken from it
// and put on it.
type solPileKind uint8

const (
	solPileStock solPileKind = iota
	solPileWaste
	solPileFoundation
	solPileTableau
	solPileCell // FreeCell's free cells; Klondike has none
)

// Klondike's piles, in the order the game keeps them: the stock, the waste,
// the four foundations and the seven columns.
const (
	solKStock  = 0
	solKWaste  = 1
	solKFound0 = 2
	solKTab0   = 6
	solKPiles  = 13
)

var solKlondikeKinds = [solKPiles]solPileKind{
	solPileStock, solPileWaste,
	solPileFoundation, solPileFoundation, solPileFoundation, solPileFoundation,
	solPileTableau, solPileTableau, solPileTableau, solPileTableau, solPileTableau, solPileTableau, solPileTableau,
}

// solVariantKinds is the pile list of a layout, nil for one this build does
// not know.
func solVariantKinds(variant string) []solPileKind {
	switch variant {
	case solVariantKlondike:
		return solKlondikeKinds[:]
	case solVariantFreeCell:
		return solFreeCellKinds[:]
	}
	return nil
}

// solSnap is one undo step: the piles and the move count before a move.
type solSnap struct {
	piles string
	moves int
}

// solGame is one deal in play.
type solGame struct {
	variant string
	kinds   []solPileKind
	piles   [][]solCard
	draw    int   // cards turned per draw: 1 or 3
	seed    int64 // the shuffle, kept so the deal can be restarted
	moves   int
	elapsed time.Duration
	won     bool
	// counted is set by the deal's first move, which is when it counts as a
	// game played; dealing and dealing again counts nothing.
	counted bool
	undo    []solSnap
	redo    []solSnap
}

// solDealFor lays out a new game of variant. FreeCell uses the published
// Microsoft numbered-deal generator so deal 1, 617 and the rest match the
// cards players remember; Klondike keeps math/rand's stable shuffle.
func solDealFor(variant string, seed int64, draw int) *solGame {
	if variant == solVariantFreeCell {
		return solDealFreeCell(seed)
	}
	return solDeal(seed, draw)
}

// solDealFreeCell reproduces Jim Horne's numbered deals: a 31-bit MSVC LCG,
// each draw swapping the chosen card with the last remaining one, then dealt
// 7/7/7/7/6/6/6/6 into eight face-up columns.
func solDealFreeCell(seed int64) *solGame {
	state := uint64(seed) & 0x7fffffff
	// Index k is rank k/4 with ace at 1, in CDHS order.
	deck := make([]uint8, 52)
	for k := range deck {
		deck[k] = uint8(k)
	}
	order := make([]uint8, 0, 52)
	for n := len(deck); n > 0; n-- {
		state = (state*214013 + 2531011) & 0x7fffffff
		pick := int((state >> 16) % uint64(n))
		deck[pick], deck[n-1] = deck[n-1], deck[pick]
		order = append(order, deck[n-1])
	}
	g := &solGame{variant: solVariantFreeCell, kinds: solFreeCellKinds[:], seed: seed}
	g.piles = make([][]solCard, solFPiles)
	for i := range g.piles {
		g.piles[i] = []solCard{}
	}
	for i, raw := range order {
		col := solFTab0
		switch {
		case i < 28:
			col += i / 7
		default:
			col += 4 + (i-28)/6
		}
		suits := [4]uint8{solClubs, solDiamonds, solHearts, solSpades} // CDHS
		c := solCard{suit: suits[raw%4], rank: raw/4 + 1, up: true}
		g.piles[col] = append(g.piles[col], c)
	}
	return g
}

// solDeal shuffles a deck with seed and lays out a new Klondike game.
// math/rand's seeded source is stable across Go releases, so a seed always
// deals the same cards: that is what "Restart this deal" relies on.
func solDeal(seed int64, draw int) *solGame {
	if draw != 3 {
		draw = 1
	}
	deck := make([]solCard, 0, 52)
	for s := uint8(0); s < 4; s++ {
		for r := uint8(1); r <= 13; r++ {
			deck = append(deck, solCard{suit: s, rank: r})
		}
	}
	rng := rand.New(rand.NewSource(seed))
	for i := len(deck) - 1; i > 0; i-- {
		j := rng.Intn(i + 1)
		deck[i], deck[j] = deck[j], deck[i]
	}
	g := &solGame{variant: solVariantKlondike, kinds: solKlondikeKinds[:], draw: draw, seed: seed}
	g.piles = make([][]solCard, solKPiles)
	n := 0
	for col := 0; col < 7; col++ {
		for k := 0; k <= col; k++ {
			c := deck[n]
			n++
			c.up = k == col
			g.piles[solKTab0+col] = append(g.piles[solKTab0+col], c)
		}
	}
	for ; n < len(deck); n++ {
		c := deck[n]
		c.up = false
		g.piles[solKStock] = append(g.piles[solKStock], c)
	}
	return g
}

// solNewSeed is a fresh shuffle. It stays within 48 bits so it survives a
// round trip through JSON readers that hold numbers as doubles.
func solNewSeed() int64 {
	return time.Now().UnixNano() & (1<<48 - 1)
}

// pileOf is the first pile of a kind, -1 when the layout has none.
func (g *solGame) pileOf(kind solPileKind) int {
	for i, k := range g.kinds {
		if k == kind {
			return i
		}
	}
	return -1
}

// top is pile p's top card; ok is false for an empty pile.
func (g *solGame) top(p int) (solCard, bool) {
	if p < 0 || p >= len(g.piles) || len(g.piles[p]) == 0 {
		return solCard{}, false
	}
	pile := g.piles[p]
	return pile[len(pile)-1], true
}

// solRunOK reports whether cards, bottom to top, are a run that may move as
// one: all face up, each one rank lower than the card under it and of the
// other colour.
func solRunOK(cards []solCard) bool {
	if len(cards) == 0 {
		return false
	}
	for i, c := range cards {
		if !c.up {
			return false
		}
		if i > 0 {
			prev := cards[i-1]
			if c.rank+1 != prev.rank || c.red() == prev.red() {
				return false
			}
		}
	}
	return true
}

// runLen is how many cards at the top of pile p could be lifted together.
func (g *solGame) runLen(p int) int {
	n := 0
	for n < len(g.piles[p]) && g.canPick(p, n+1) {
		n++
	}
	return n
}

// canPick reports whether the top n cards of pile p may be lifted.
func (g *solGame) canPick(p, n int) bool {
	if p < 0 || p >= len(g.piles) || n < 1 || n > len(g.piles[p]) {
		return false
	}
	pile := g.piles[p]
	switch g.kinds[p] {
	case solPileWaste, solPileFoundation, solPileCell:
		return n == 1 && pile[len(pile)-1].up
	case solPileTableau:
		return solRunOK(pile[len(pile)-n:])
	}
	return false
}

// canDrop reports whether cards (bottom to top) may be put on pile p.
func (g *solGame) canDrop(cards []solCard, p int) bool {
	if len(cards) == 0 || p < 0 || p >= len(g.piles) {
		return false
	}
	first := cards[0]
	top, has := g.top(p)
	switch g.kinds[p] {
	case solPileFoundation:
		if len(cards) != 1 {
			return false
		}
		if !has {
			return first.rank == 1
		}
		return top.suit == first.suit && first.rank == top.rank+1
	case solPileTableau:
		if !has {
			// Klondike fills an empty column with a king only.
			return g.variant != solVariantKlondike || first.rank == 13
		}
		return top.up && top.red() != first.red() && first.rank+1 == top.rank
	case solPileCell:
		return !has && len(cards) == 1
	}
	return false
}

// foundations is the list of foundation piles in this layout.
func (g *solGame) foundations() []int {
	var out []int
	for p, k := range g.kinds {
		if k == solPileFoundation {
			out = append(out, p)
		}
	}
	return out
}

// emptyCells and emptyColumns feed FreeCell's supermove limit. A destination
// column that is itself empty does not count: the moving run fills it.
func (g *solGame) emptyCells() int {
	n := 0
	for p, k := range g.kinds {
		if k == solPileCell && len(g.piles[p]) == 0 {
			n++
		}
	}
	return n
}

func (g *solGame) emptyColumns(exclude int) int {
	n := 0
	for p, k := range g.kinds {
		if k == solPileTableau && p != exclude && len(g.piles[p]) == 0 {
			n++
		}
	}
	return n
}

func (g *solGame) supermoveLimit(to int) int {
	if to < 0 || to >= len(g.kinds) || g.kinds[to] != solPileTableau {
		return 1
	}
	cells := g.emptyCells()
	cols := g.emptyColumns(-1)
	if len(g.piles[to]) == 0 {
		cols--
		if cols < 0 {
			cols = 0
		}
	}
	limit := (cells + 1) << uint(cols)
	if limit > 52 {
		limit = 52
	}
	return limit
}

// foundationFor is the foundation card c can go up on now, -1 for none. A
// foundation already holding c's suit is preferred; an ace takes the first
// empty one.
func (g *solGame) foundationFor(c solCard) int {
	empty := -1
	for p, k := range g.kinds {
		if k != solPileFoundation {
			continue
		}
		if top, ok := g.top(p); ok {
			if top.suit == c.suit {
				if c.rank == top.rank+1 {
					return p
				}
				return -1
			}
		} else if empty < 0 {
			empty = p
		}
	}
	if c.rank == 1 {
		return empty
	}
	return -1
}

// foundationRanks is the highest rank each suit has on the foundations.
func (g *solGame) foundationRanks() [4]uint8 {
	var hi [4]uint8
	for p, k := range g.kinds {
		if k != solPileFoundation {
			continue
		}
		if top, ok := g.top(p); ok && top.suit < 4 && top.rank > hi[top.suit] {
			hi[top.suit] = top.rank
		}
	}
	return hi
}

// safeToFoundation is the usual auto-play rule: aces and twos always go up;
// a higher card only once both cards of the other colour one rank lower are
// up, because then nothing left on the table could ever need it to build on.
func (g *solGame) safeToFoundation(c solCard) bool {
	if c.rank <= 2 {
		return true
	}
	hi := g.foundationRanks()
	for s := uint8(0); s < 4; s++ {
		if solSuitRed(s) != c.red() && hi[s]+1 < c.rank {
			return false
		}
	}
	return true
}

// nextAutoMove finds one card auto-play would put up now: the waste first,
// then the columns (and FreeCell's cells) left to right. all drops the safety
// rule, for finishing a game whose every card is face up.
func (g *solGame) nextAutoMove(all bool) (from, to int, ok bool) {
	for p, k := range g.kinds {
		if k != solPileWaste && k != solPileTableau && k != solPileCell {
			continue
		}
		c, has := g.top(p)
		if !has || !c.up {
			continue
		}
		if f := g.foundationFor(c); f >= 0 && (all || g.safeToFoundation(c)) {
			return p, f, true
		}
	}
	return -1, -1, false
}

// canAutoFinish reports whether the rest of the game is a formality: the
// stock and the waste are empty and every card on the table is face up. In
// Klondike every column is then one run, so the lowest card left is always
// on top of one and the cards can go up one by one.
func (g *solGame) canAutoFinish() bool {
	if g.won {
		return false
	}
	if g.variant == solVariantFreeCell {
		// Unlike Klondike, every FreeCell card starts face up and an empty
		// column takes any rank, so the visible-state test would say "finish"
		// on the first deal. Play the remaining foundation moves on a copy.
		if g.complete() {
			return false
		}
		sim := g.cloneForAuto()
		for i := 0; i < 52; i++ {
			from, to, ok := sim.nextAutoMove(true)
			if !ok {
				return false
			}
			sim.apply(from, 1, to)
			if sim.complete() {
				return true
			}
		}
		return false
	}
	left := 0
	for p, k := range g.kinds {
		switch k {
		case solPileStock, solPileWaste:
			if len(g.piles[p]) > 0 {
				return false
			}
		case solPileTableau, solPileCell:
			for _, c := range g.piles[p] {
				if !c.up {
					return false
				}
				left++
			}
		}
	}
	return left > 0
}

// cloneForAuto copies just the piles: auto-finish tries the rest of a game
// without touching the real deal, its undo history or its move count.
func (g *solGame) cloneForAuto() *solGame {
	c := *g
	c.piles = make([][]solCard, len(g.piles))
	for i, pile := range g.piles {
		c.piles[i] = append([]solCard(nil), pile...)
	}
	c.undo, c.redo = nil, nil
	return &c
}

// complete reports whether every card is on the foundations.
func (g *solGame) complete() bool {
	n := 0
	for p, k := range g.kinds {
		if k == solPileFoundation {
			n += len(g.piles[p])
		}
	}
	return n == 52
}

// smartTarget is where the top n cards of pile p would go on a smart move:
// a foundation for a single card, else a column with cards to build on
// (searching rightwards from p, so pressing again tries the next place),
// else an empty column, but only when that achieves something — a king that
// leaves face-down cards behind, or one from the waste. -1 when nothing fits.
func (g *solGame) smartTarget(p, n int, foundation bool) int {
	if !g.canPick(p, n) {
		return -1
	}
	pile := g.piles[p]
	cards := pile[len(pile)-n:]
	if foundation && n == 1 && g.kinds[p] != solPileFoundation {
		if f := g.foundationFor(cards[0]); f >= 0 {
			return f
		}
	}
	empty := -1
	count := len(g.kinds)
	for k := 1; k <= count; k++ {
		t := (p + k) % count
		if t == p || g.kinds[t] != solPileTableau || !g.canDrop(cards, t) {
			continue
		}
		if len(g.piles[t]) > 0 {
			return t
		}
		if empty < 0 && (g.kinds[p] != solPileTableau || len(pile) > n) {
			empty = t
		}
	}
	if empty >= 0 {
		return empty
	}
	// A free cell is the last resort: it empties a column but holds only one
	// card, so it is worse than building on any tableau card.
	if g.variant == solVariantFreeCell && n == 1 {
		for p, k := range g.kinds {
			if k == solPileCell && len(g.piles[p]) == 0 {
				return p
			}
		}
	}
	return -1
}

// smartMoveFrom picks the move a smart move makes from pile p with depth
// cards chosen. With the top card chosen (depth 1) it also looks at the
// longer runs under it, so one press moves a whole run onto the column that
// takes it.
func (g *solGame) smartMoveFrom(p, depth int) (n, to int) {
	if depth < 1 {
		depth = 1
	}
	if depth > 1 {
		return depth, g.smartTarget(p, depth, true)
	}
	if c, ok := g.top(p); ok && g.kinds[p] != solPileFoundation {
		if f := g.foundationFor(c); f >= 0 {
			return 1, f
		}
	}
	for k := g.runLen(p); k >= 1; k-- {
		if t := g.smartTarget(p, k, false); t >= 0 {
			return k, t
		}
	}
	return 0, -1
}

// move lifts the top n cards of from onto to as one undoable step, turning
// up the card it uncovers. It changes nothing and reports false when the
// move is not legal.
func (g *solGame) move(from, n, to int) bool {
	if g.won || from == to || !g.canPick(from, n) {
		return false
	}
	pile := g.piles[from]
	if !g.canDrop(pile[len(pile)-n:], to) {
		return false
	}
	if g.variant == solVariantFreeCell && n > g.supermoveLimit(to) {
		return false
	}
	g.pushUndo()
	g.apply(from, n, to)
	g.moves++
	return true
}

// apply moves the cards with no undo step and no move counted: the second
// half of move, and auto-play's moves, which belong to the move that set
// them off (undoing that move takes them back too).
func (g *solGame) apply(from, n, to int) {
	src := g.piles[from]
	cut := len(src) - n
	moved := append([]solCard(nil), src[cut:]...)
	g.piles[from] = src[:cut]
	g.piles[to] = append(g.piles[to], moved...)
	g.flipTop(from)
	if g.complete() {
		g.won = true
	}
}

// flipTop turns up a column's top card when a move uncovered it.
func (g *solGame) flipTop(p int) {
	if g.kinds[p] != solPileTableau {
		return
	}
	if n := len(g.piles[p]); n > 0 && !g.piles[p][n-1].up {
		g.piles[p][n-1].up = true
	}
}

// drawStock turns the next one or three cards onto the waste, or, with the
// stock empty, turns the whole waste back over as a new stock. Passes are
// unlimited. It reports false when both piles are empty.
func (g *solGame) drawStock() bool {
	stock, waste := g.pileOf(solPileStock), g.pileOf(solPileWaste)
	if g.won || stock < 0 || waste < 0 {
		return false
	}
	if len(g.piles[stock]) == 0 {
		w := g.piles[waste]
		if len(w) == 0 {
			return false
		}
		g.pushUndo()
		fresh := make([]solCard, 0, len(w))
		for i := len(w) - 1; i >= 0; i-- {
			c := w[i]
			c.up = false
			fresh = append(fresh, c)
		}
		g.piles[stock] = fresh
		g.piles[waste] = nil
		g.moves++
		return true
	}
	g.pushUndo()
	for i := 0; i < g.draw && len(g.piles[stock]) > 0; i++ {
		s := g.piles[stock]
		c := s[len(s)-1]
		g.piles[stock] = s[:len(s)-1]
		c.up = true
		g.piles[waste] = append(g.piles[waste], c)
	}
	g.moves++
	return true
}

// encodePiles writes the piles as text: each pile's cards, piles separated
// by "|".
func (g *solGame) encodePiles() string {
	var b strings.Builder
	b.Grow(52*2 + len(g.piles))
	for i, pile := range g.piles {
		if i > 0 {
			b.WriteByte('|')
		}
		for _, c := range pile {
			b.WriteString(c.code())
		}
	}
	return b.String()
}

// solDecodePiles reads encodePiles' text for a layout of kinds, insisting on
// every one of the 52 cards exactly once: a save edited by hand or cut short
// must not deal a game that cannot be finished.
func solDecodePiles(text string, kinds []solPileKind) ([][]solCard, error) {
	parts := strings.Split(text, "|")
	if len(kinds) == 0 || len(parts) != len(kinds) {
		return nil, fmt.Errorf("solitaire: %d piles, want %d", len(parts), len(kinds))
	}
	var seen [4][14]bool
	total := 0
	piles := make([][]solCard, len(parts))
	for i, part := range parts {
		if len(part)%2 != 0 {
			return nil, errors.New("solitaire: a pile is cut short")
		}
		for j := 0; j+1 < len(part); j += 2 {
			c, ok := solParseCard(part[j], part[j+1])
			if !ok {
				return nil, fmt.Errorf("solitaire: bad card %q", part[j:j+2])
			}
			if seen[c.suit][c.rank] {
				return nil, fmt.Errorf("solitaire: %s twice", c.code())
			}
			seen[c.suit][c.rank] = true
			piles[i] = append(piles[i], c)
			total++
		}
	}
	if total != 52 {
		return nil, fmt.Errorf("solitaire: %d cards, want 52", total)
	}
	return piles, nil
}

func (g *solGame) snap() solSnap { return solSnap{piles: g.encodePiles(), moves: g.moves} }

// restore puts the piles back as a step recorded them.
func (g *solGame) restore(s solSnap) bool {
	piles, err := solDecodePiles(s.piles, g.kinds)
	if err != nil {
		return false
	}
	g.piles = piles
	g.moves = s.moves
	g.won = g.complete()
	return true
}

// pushUndo records the state before a move and ends any redo: a new move
// starts a new line of play.
func (g *solGame) pushUndo() {
	g.undo = append(g.undo, g.snap())
	if over := len(g.undo) - solUndoCap; over > 0 {
		g.undo = append(g.undo[:0], g.undo[over:]...)
	}
	g.redo = g.redo[:0]
}

// undoStep goes back one move, keeping it for redo. A won game stays won:
// its statistics are already counted.
func (g *solGame) undoStep() bool {
	if g.won || len(g.undo) == 0 {
		return false
	}
	s := g.undo[len(g.undo)-1]
	cur := g.snap()
	if !g.restore(s) {
		return false
	}
	g.undo = g.undo[:len(g.undo)-1]
	g.redo = append(g.redo, cur)
	return true
}

// redoStep replays a move undone since the last new move.
func (g *solGame) redoStep() bool {
	if g.won || len(g.redo) == 0 {
		return false
	}
	s := g.redo[len(g.redo)-1]
	cur := g.snap()
	if !g.restore(s) {
		return false
	}
	g.redo = g.redo[:len(g.redo)-1]
	g.undo = append(g.undo, cur)
	return true
}

// ── Statistics and the saved file ─────────────────────────────────────────

// solStats are kept across games, in the same file as the game.
type solStats struct {
	Played      int   `json:"played"`
	Won         int   `json:"won"`
	Streak      int   `json:"streak"`
	BestStreak  int   `json:"bestStreak"`
	BestTimeMs  int64 `json:"bestTimeMs,omitempty"`
	FewestMoves int   `json:"fewestMoves,omitempty"`
}

// solOptions are the player's settings. AutoMoveOff is stored inverted so a
// file without it (every file so far) means auto-play on, the default.
type solOptions struct {
	Draw        int    `json:"draw"`
	AutoMoveOff bool   `json:"autoMoveOff,omitempty"`
	Variant     string `json:"variant,omitempty"`
}

type solSaveSnap struct {
	Piles string `json:"p"`
	Moves int    `json:"m"`
}

type solSaveGame struct {
	Variant   string        `json:"variant"`
	Seed      int64         `json:"seed"`
	Draw      int           `json:"draw"`
	Piles     string        `json:"piles"`
	Moves     int           `json:"moves"`
	ElapsedMs int64         `json:"elapsedMs"`
	Won       bool          `json:"won,omitempty"`
	Counted   bool          `json:"counted,omitempty"`
	Undo      []solSaveSnap `json:"undo,omitempty"`
	Redo      []solSaveSnap `json:"redo,omitempty"`
}

// solSaveFile is solitaire.json. The game is kept raw while the file is read,
// so a game that does not parse (a newer layout, a damaged card) costs only
// the game: the statistics and options beside it still load.
type solSaveFile struct {
	Version int             `json:"version"`
	Stats   solStats        `json:"stats"` // the active variant, for older readers
	Options solOptions      `json:"options"`
	Game    json.RawMessage `json:"game,omitempty"`
	// StatsByVariant keeps each game's record separate. A version-1 file has
	// none; its Stats become Klondike's on read.
	StatsByVariant map[string]solStats `json:"statsByVariant,omitempty"`
}

func (g *solGame) toSave() solSaveGame {
	s := solSaveGame{
		Variant:   g.variant,
		Seed:      g.seed,
		Draw:      g.draw,
		Piles:     g.encodePiles(),
		Moves:     g.moves,
		ElapsedMs: g.elapsed.Milliseconds(),
		Won:       g.won,
		Counted:   g.counted,
	}
	for _, u := range g.undo {
		s.Undo = append(s.Undo, solSaveSnap{Piles: u.piles, Moves: u.moves})
	}
	for _, r := range g.redo {
		s.Redo = append(s.Redo, solSaveSnap{Piles: r.piles, Moves: r.moves})
	}
	return s
}

// solGameFromSave rebuilds a saved game, checking the piles. Undo steps that
// do not parse are dropped rather than failing the game: the game itself is
// what the player wants back.
func solGameFromSave(s solSaveGame) (*solGame, error) {
	if strings.TrimSpace(s.Variant) == "" {
		s.Variant = solVariantKlondike // a version-1 game predates variants
	}
	kinds := solVariantKinds(s.Variant)
	if kinds == nil {
		return nil, fmt.Errorf("solitaire: unknown layout %q", s.Variant)
	}
	piles, err := solDecodePiles(s.Piles, kinds)
	if err != nil {
		return nil, err
	}
	g := &solGame{variant: s.Variant, kinds: kinds, piles: piles, draw: s.Draw, seed: s.Seed,
		moves: max(s.Moves, 0), counted: s.Counted}
	if g.draw != 3 {
		g.draw = 1
	}
	if s.ElapsedMs > 0 {
		g.elapsed = time.Duration(s.ElapsedMs) * time.Millisecond
	}
	g.won = g.complete()
	keep := func(list []solSaveSnap) []solSnap {
		var out []solSnap
		for _, u := range list {
			if _, err := solDecodePiles(u.Piles, kinds); err == nil {
				out = append(out, solSnap{piles: u.Piles, moves: max(u.Moves, 0)})
			}
		}
		if len(out) > solUndoCap {
			out = out[len(out)-solUndoCap:]
		}
		return out
	}
	g.undo, g.redo = keep(s.Undo), keep(s.Redo)
	return g, nil
}

// solLoadFile reads solitaire.json. A missing file is a first run (no error,
// no game). A file that does not parse at all falls back to its .bak; a game
// that does not parse comes back nil with the statistics kept, and the caller
// deals a new one.
func solLoadFile(path string) (solSaveFile, *solGame, error) {
	var f solSaveFile
	if _, err := readJSONWithBackup(path, &f); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return solSaveFile{}, nil, nil
		}
		// Neither the file nor its .bak could be read. The new deal's first
		// save would replace both (statistics included), so the file is moved
		// aside first, as the task lists do (tasks_store.go): a card that
		// failed one read keeps the player's record for a later look.
		if mvErr := os.Rename(path, corruptAsidePath(path)); mvErr == nil {
			pruneCorruptCopies(path, 3)
		} else if !errors.Is(mvErr, os.ErrNotExist) {
			logWarn("solitaire", "cannot move the unreadable save aside: %v", mvErr)
		}
		return solSaveFile{}, nil, err
	}
	if f.Stats.Played < 0 || f.Stats.Won < 0 || f.Stats.Won > f.Stats.Played {
		// Hand-edited nonsense; the counts start over, the game survives.
		f.Stats = solStats{}
	}
	if f.Options.Draw != 3 {
		f.Options.Draw = 1
	}
	if strings.TrimSpace(f.Options.Variant) == "" {
		f.Options.Variant = solVariantKlondike
	}
	if len(f.StatsByVariant) == 0 {
		// Version 1 kept only one set of counters, for Klondike.
		f.StatsByVariant = map[string]solStats{solVariantKlondike: f.Stats}
	}
	raw := strings.TrimSpace(string(f.Game))
	if raw == "" || raw == "null" {
		return f, nil, nil
	}
	var sg solSaveGame
	if err := json.Unmarshal(f.Game, &sg); err != nil {
		return f, nil, fmt.Errorf("solitaire: saved game: %w", err)
	}
	g, err := solGameFromSave(sg)
	if err != nil {
		return f, nil, err
	}
	f.Options.Variant = g.variant
	return f, g, nil
}

// solEncodeFile is the bytes solitaire.json is written with.
func solEncodeFile(g *solGame, stats solStats, opts solOptions, byVariant map[string]solStats) ([]byte, error) {
	variant := opts.Variant
	if strings.TrimSpace(variant) == "" {
		variant = solVariantKlondike
	}
	all := make(map[string]solStats, len(byVariant)+1)
	for name, record := range byVariant {
		all[name] = record
	}
	all[variant] = stats
	f := solSaveFile{
		Version: solSaveVersion, Stats: stats, Options: opts,
		StatsByVariant: all,
	}
	if g != nil {
		raw, err := json.Marshal(g.toSave())
		if err != nil {
			return nil, err
		}
		f.Game = raw
	}
	return json.Marshal(f)
}

// solWriter serialises the background writes: a slow card must not let an
// older save land after a newer one.
var solWriter struct {
	mu      sync.Mutex
	written uint64 // the newest save sequence on disk
}

// solWrite writes one save unless a newer one has landed already.
func solWrite(path string, data []byte, seq uint64) {
	solWriter.mu.Lock()
	defer solWriter.mu.Unlock()
	if seq <= solWriter.written {
		return
	}
	if err := AtomicWrite(path, data, 0644); err != nil {
		logDebug("solitaire", "save failed: %v", err)
		return
	}
	solWriter.written = seq
}

// ── The session: the game on the table and its bookkeeping ───────────────

// sol is the game the scene shows. Main thread only, except solWrite's
// goroutine, which gets its own copy of the bytes.
var sol struct {
	loaded         bool
	game           *solGame
	stats          solStats // the active variant's counters
	statsByVariant map[string]solStats
	opts           solOptions
	dirty          bool      // changed since the last write
	dirtyAt        time.Time // when it first changed
	saveSeq        uint64
	// lastTick is the previous frame the clock counted from; zero while the
	// screen is away, so the time spent elsewhere is never counted.
	lastTick time.Time
}

// solitaireEnsureLoaded reads the saved game once per run, dealing a new one
// when there is none (or it is damaged).
func solitaireEnsureLoaded() {
	if sol.loaded {
		return
	}
	sol.loaded = true
	f, g, err := solLoadFile(osAppsDataPath(solSaveFileName))
	if err != nil {
		Log().Warn("solitaire: the saved game could not be read; dealing a new one", "err", err)
	}
	sol.statsByVariant = f.StatsByVariant
	if len(sol.statsByVariant) == 0 {
		sol.statsByVariant = map[string]solStats{solVariantKlondike: f.Stats}
	}
	sol.opts = f.Options
	if strings.TrimSpace(sol.opts.Variant) == "" {
		sol.opts.Variant = solVariantKlondike
	}
	if g != nil {
		sol.opts.Variant = g.variant
	}
	sol.stats = sol.statsByVariant[sol.opts.Variant]
	if sol.opts.Draw != 3 {
		sol.opts.Draw = 1
	}
	if g == nil {
		g = solDealFor(sol.opts.Variant, solNewSeed(), sol.opts.Draw)
		sol.game = g
		solMarkDirty()
		return
	}
	sol.game = g
}

// solMarkDirty asks for a write solSaveDelay from the first change.
func solMarkDirty() {
	if !sol.dirty {
		sol.dirty = true
		sol.dirtyAt = time.Now()
	}
}

// solSaveNow writes the game: in the background during play, and waiting
// for the card when JukaHub is about to exit.
func solSaveNow(wait bool) {
	if !sol.loaded || sol.game == nil {
		return
	}
	data, err := solEncodeFile(sol.game, sol.stats, sol.opts, sol.statsByVariant)
	if err != nil {
		logDebug("solitaire", "encode failed: %v", err)
		return
	}
	sol.dirty = false
	sol.saveSeq++
	seq, path := sol.saveSeq, osAppsDataPath(solSaveFileName)
	if wait {
		solWrite(path, data, seq)
		return
	}
	go solWrite(path, data, seq)
}

// solSaveIfDue writes once the debounce has passed. Called every frame.
func solSaveIfDue(now time.Time) {
	if sol.dirty && now.Sub(sol.dirtyAt) >= solSaveDelay {
		solSaveNow(false)
	}
}

// solTickClock adds the time since the previous frame to the game's clock.
// The clock runs from the first move until the game is won, and only while
// the table is on screen with no menu over it; one frame adds at most five
// seconds, so a handheld that slept on this screen does not count the night.
func solTickClock(now time.Time, running bool) {
	last := sol.lastTick
	sol.lastTick = now
	g := sol.game
	if g == nil || !running || g.won || !g.counted || last.IsZero() {
		return
	}
	d := now.Sub(last)
	if d <= 0 {
		return
	}
	if d > 5*time.Second {
		d = 5 * time.Second
	}
	g.elapsed += d
}

// solNoteMove does the bookkeeping after any move by the player: the deal's
// first move counts it as played, and the game is due a write.
func solNoteMove() {
	g := sol.game
	if g == nil {
		return
	}
	if !g.counted {
		g.counted = true
		sol.stats.Played++
	}
	solMarkDirty()
}

// solRecordWin updates the statistics once for a won game.
func solRecordWin() {
	g := sol.game
	if g == nil {
		return
	}
	if !g.counted {
		g.counted = true
		sol.stats.Played++
	}
	sol.stats.Won++
	sol.stats.Streak++
	if sol.stats.Streak > sol.stats.BestStreak {
		sol.stats.BestStreak = sol.stats.Streak
	}
	if ms := g.elapsed.Milliseconds(); ms > 0 && (sol.stats.BestTimeMs == 0 || ms < sol.stats.BestTimeMs) {
		sol.stats.BestTimeMs = ms
	}
	if g.moves > 0 && (sol.stats.FewestMoves == 0 || g.moves < sol.stats.FewestMoves) {
		sol.stats.FewestMoves = g.moves
	}
	solMarkDirty()
}

// solNewGame deals a new game. Walking away from a game in progress ends the
// win streak, the way giving up does.
func solNewGame() {
	if g := sol.game; g != nil && g.counted && !g.won {
		sol.stats.Streak = 0
	}
	sol.game = solDealFor(sol.opts.Variant, solNewSeed(), sol.opts.Draw)
	solMarkDirty()
}

// solSetVariant switches games, keeping each variant's own counters.
func solSetVariant(variant string) {
	if solVariantKinds(variant) == nil || variant == sol.opts.Variant {
		return
	}
	sol.statsByVariant[sol.opts.Variant] = sol.stats
	sol.opts.Variant = variant
	sol.stats = sol.statsByVariant[variant]
	sol.game = solDealFor(variant, solNewSeed(), sol.opts.Draw)
	solMarkDirty()
}

// solRestartDeal deals the same cards again from the start, with the
// current draw setting. It is the same game, so it is not counted again and
// the streak stands.
func solRestartDeal() {
	old := sol.game
	if old == nil {
		solNewGame()
		return
	}
	g := solDealFor(old.variant, old.seed, sol.opts.Draw)
	g.counted = old.counted && !old.won
	sol.game = g
	solMarkDirty()
}

// solGameUntouched reports whether no move has been made in this deal yet.
func solGameUntouched() bool {
	g := sol.game
	return g == nil || (!g.counted && g.moves == 0 && len(g.undo) == 0)
}

// solFormatDuration is a clock for the table and the statistics: 4:07, or
// 1:04:07 past an hour.
func solFormatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int64(d / time.Second)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
