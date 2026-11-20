package main

// partymode.go — Party Mode: a game show the handheld hosts for the phones in
// the room. The first game is Guess the Game.
//
// One handheld hosts. Friends scan the QR code on its screen and join from a
// phone browser (partymode_net.go serves the page and the game's API); the
// handheld shows the round, the cover and the scoreboard (partymode_ui.go).
// Nothing needs an account or the internet: the phones talk to the handheld
// over the Wi-Fi they share, or over the handheld's own hotspot.
//
// Guess the Game, one round:
//
//   - intro: "Round 3 of 10" while the round's cover is decoded;
//   - reveal: a small square crop of a cover from your own library, zooming
//     out in five stages to the whole cover; the points at stake drop with
//     every stage (500, 400, 300, 200, 100);
//   - any player may buzz. The reveal pauses and the buzzer alone gets four
//     titles to pick from (the decoys come from the same console when the
//     library has them). Right: the points, and the round ends. Wrong, or no
//     pick in time: minus 100 (never below zero), that player is out for the
//     round and the reveal goes on for the others;
//   - result: the whole cover with its title and who got it.
//
// After the last round the final screen ranks everyone; the host can play
// again with the same players or go back to the lobby.
//
// This file is the room model only: no SDL, no HTTP, and every call takes the
// time as an argument, so the rules can be read (and, if ever needed, tested)
// without a device. Deadlines advance lazily: every call first moves the room
// past any deadline that has passed, and the server's ticker calls Tick so
// phones waiting on a change hear about a new stage on time.

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"image"
	"math"
	mrand "math/rand"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// pmodeDefaultPort follows the Phone Companion (8317) and Watch Party
	// (8318); a taken port falls back to any free one, which the QR code
	// carries.
	pmodeDefaultPort = 8319
	// pmodeMaxPlayers bounds the room, the scoreboard and the long polls.
	pmodeMaxPlayers = 12
	// pmodeNameLimit is the longest name in runes; the scoreboard has room for
	// it at the handheld's smallest font.
	pmodeNameLimit = 16
	// pmodeChoiceCount is how many titles the buzzer picks from.
	pmodeChoiceCount = 4
	// pmodeMinDeck is the smallest library the game starts with: one answer
	// and three decoys.
	pmodeMinDeck = pmodeChoiceCount
	// pmodeWrongPenalty is what a wrong pick (or no pick in time) costs.
	pmodeWrongPenalty = 100

	pmodeIntroFor     = 3 * time.Second
	pmodeStageFor     = 5 * time.Second
	pmodeLastStageFor = 7 * time.Second
	pmodeAnswerFor    = 10 * time.Second
	pmodeResultFor    = 7 * time.Second
	// pmodePictureWait is how long an intro waits for its cover to decode
	// before the round takes another game instead.
	pmodePictureWait = 8 * time.Second
	// pmodeAwayAfter: a phone that has not asked for the state this long is
	// away. It keeps its score but no longer holds a round open.
	pmodeAwayAfter = 20 * time.Second
	// pmodeLobbyDropAfter: an away phone is taken off the lobby's list.
	pmodeLobbyDropAfter = 2 * time.Minute
	// pmodeEventKeep bounds the event log the handheld's sounds read.
	pmodeEventKeep = 32
	// pmodeColors is the size of the player palette (the page and the
	// handheld use the same eight colours, by index).
	pmodeColors = 8
	// pmodeSeatsPerAddr is how many seats one phone (network address) may
	// hold.
	pmodeSeatsPerAddr = 2
	// pmodeMaxSpare is how many games a game keeps in reserve for covers
	// that will not open.
	pmodeMaxSpare = 24
)

// pmodeStageFracs is each stage's crop: the side of the square as a share of
// the cover's shorter side. The last stage is the whole cover.
var pmodeStageFracs = [...]float64{0.14, 0.24, 0.40, 0.65, 1}

// pmodeStagePoints is what a right answer earns in each stage.
var pmodeStagePoints = [...]int{500, 400, 300, 200, 100}

// pmodeStages is the number of stages; pmodeLastStage the whole cover.
const (
	pmodeStages    = len(pmodeStageFracs)
	pmodeLastStage = pmodeStages - 1
)

// pmodeRoundOptions are the game lengths the lobby offers.
var pmodeRoundOptions = [...]int{5, 10, 15, 20}

// pmodePhase is where the room is.
type pmodePhase string

const (
	pmodeLobby  pmodePhase = "lobby"
	pmodeIntro  pmodePhase = "intro"
	pmodeReveal pmodePhase = "reveal"
	pmodeAnswer pmodePhase = "answer"
	pmodeResult pmodePhase = "result"
	pmodeFinal  pmodePhase = "final"
)

var (
	errPmodeUnknownPlayer = errors.New("you are not in this party; join again")
	errPmodeBadCode       = errors.New("that party code is not this handheld's")
	errPmodeFull          = errors.New("the party is full")
	errPmodeSeats         = errors.New("this phone already has two seats in the party")
	errPmodeNotNow        = errors.New("not now")
	errPmodeTaken         = errors.New("someone buzzed first")
	errPmodeLocked        = errors.New("you are out for this round")
	errPmodeBadChoice     = errors.New("no such answer")
	errPmodeSmallDeck     = errors.New("Guess the Game needs at least 4 games with box art")
	errPmodeNoPlayers     = errors.New("nobody has joined yet")
	errPmodeRunning       = errors.New("a game is running")
)

// pmodeCard is one game the deck can ask about.
type pmodeCard struct {
	Title  string
	System string
	Art    string
}

// pmodeRound is one question.
type pmodeRound struct {
	card    pmodeCard
	choices []string
	// cx, cy is the crop's centre as a share of the cover's width and height.
	cx, cy float64
	// pic is the decoded cover (at most 512 px on its longer side), set by
	// the server's worker before the round leaves its intro and dropped
	// when the next round starts. Its pixels are never written after it is
	// set.
	pic    *image.NRGBA
	picErr bool
	// winner is the player's ID; winnerName keeps the name for the result
	// even if that player leaves before it is shown.
	winner     string
	winnerName string
	points     int
	locked     map[string]bool
	wrong      []string
}

// pmodePlayer is one seat. token is the phone's secret; the handheld's own
// seat has none and is driven from the pad.
type pmodePlayer struct {
	id     string
	token  string
	name   string
	color  int
	score  int
	right  int
	delta  int
	local  bool
	joined time.Time
	seen   time.Time
	// addr is the phone's network address, for the seats-per-phone limit.
	addr string
}

// pmodeEvent is one thing the handheld plays a sound for.
type pmodeEvent struct {
	seq    uint64
	kind   string // join, leave, start, round, stage, buzz, right, wrong, timeout, nobody, final, nocovers
	name   string
	points int
}

// pmodeRoom is the whole game. Every exported-style method locks mu.
type pmodeRoom struct {
	mu        sync.Mutex
	code      string
	phase     pmodePhase
	phaseEnd  time.Time
	stage     int
	stageLeft time.Duration
	// introSince is when the current intro began, for pmodePictureWait;
	// introLate is set (and announced once) when its deadline passed
	// before the cover was ready; picMiss counts intros in a row that gave
	// up waiting, so a stuck decoder ends the game instead of trying every
	// spare in turn.
	introSince time.Time
	introLate  bool
	picMiss    int
	players    []*pmodePlayer
	byToken    map[string]*pmodePlayer
	deck       []pmodeCard
	deckReady  bool
	rounds     []*pmodeRound
	spare      []pmodeCard
	roundIdx   int
	target     int
	buzzer     string
	version    uint64
	changed    chan struct{}
	events     []pmodeEvent
	eventSeq   uint64
	nextColor  int
	rng        *mrand.Rand
	// game counts the games started in this room. It is part of every
	// picture URL, so a phone's cache never serves the last game's round 1
	// for this one's.
	game int
}

func newPmodeRoom() *pmodeRoom {
	seed := time.Now().UnixNano()
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		seed = int64(binary.LittleEndian.Uint64(b[:]))
	}
	return &pmodeRoom{
		code:    partyNewCode(),
		phase:   pmodeLobby,
		target:  10,
		byToken: map[string]*pmodePlayer{},
		changed: make(chan struct{}),
		rng:     mrand.New(mrand.NewSource(seed)),
	}
}

func pmodeRandomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand does not fail on the platforms JukaHub runs on; a
		// time-derived value still keeps two seats apart.
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(buf)
}

// pmodeCleanName keeps a name printable and short: control, bidi-format and
// enclosing-mark characters go, runs of spaces (tabs included) become one,
// "$" goes (the handheld's text layer reads $name as a variable), and the
// result is cut to pmodeNameLimit runes.
func pmodeCleanName(name string) string {
	var b strings.Builder
	n, marks := 0, 0
	pendingSpace := false
	for _, r := range name {
		if n >= pmodeNameLimit {
			break
		}
		switch {
		case unicode.IsSpace(r):
			pendingSpace = b.Len() > 0
			continue
		case r == utf8.RuneError, r == '$', unicode.IsControl(r), unicode.Is(unicode.Cf, r), unicode.Is(unicode.Me, r):
			continue
		case unicode.Is(unicode.Mn, r):
			// Stacked combining marks would draw over the next scoreboard
			// line: two on a letter is plenty for any language.
			if marks >= 2 {
				continue
			}
			marks++
		default:
			marks = 0
		}
		if pendingSpace {
			if n+1 >= pmodeNameLimit {
				break
			}
			b.WriteByte(' ')
			n++
			pendingSpace = false
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// pmodeUniqueNameLocked gives a second "Sam" the name "Sam 2".
func (r *pmodeRoom) pmodeUniqueNameLocked(name string) string {
	taken := func(candidate string) bool {
		for _, p := range r.players {
			if strings.EqualFold(p.name, candidate) {
				return true
			}
		}
		return false
	}
	if !taken(name) {
		return name
	}
	for i := 2; i < 100; i++ {
		suffix := " " + strconv.Itoa(i)
		base := []rune(name)
		if len(base)+utf8.RuneCountInString(suffix) > pmodeNameLimit {
			base = base[:pmodeNameLimit-utf8.RuneCountInString(suffix)]
		}
		candidate := strings.TrimSpace(string(base)) + suffix
		if !taken(candidate) {
			return candidate
		}
	}
	return name
}

// ── Change notification ──────────────────────────────────────────────────

// bumpLocked marks a change: long polls waiting on the old channel return.
func (r *pmodeRoom) bumpLocked() {
	r.version++
	close(r.changed)
	r.changed = make(chan struct{})
}

// Changed is the current version and the channel closed at the next change.
func (r *pmodeRoom) Changed() (uint64, <-chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.version, r.changed
}

func (r *pmodeRoom) eventLocked(kind, name string, points int) {
	r.eventSeq++
	r.events = append(r.events, pmodeEvent{seq: r.eventSeq, kind: kind, name: name, points: points})
	if len(r.events) > pmodeEventKeep {
		r.events = append([]pmodeEvent(nil), r.events[len(r.events)-pmodeEventKeep:]...)
	}
}

// EventsAfter returns the events newer than seq and the newest seq.
func (r *pmodeRoom) EventsAfter(seq uint64) ([]pmodeEvent, uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []pmodeEvent
	for _, e := range r.events {
		if e.seq > seq {
			out = append(out, e)
		}
	}
	return out, r.eventSeq
}

// ── Seats ────────────────────────────────────────────────────────────────

func (r *pmodeRoom) Code() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.code
}

// CodeMatches compares a typed or scanned code, ignoring case and spaces.
func (r *pmodeRoom) CodeMatches(code string) bool {
	code = strings.ToUpper(strings.Join(strings.Fields(code), ""))
	r.mu.Lock()
	defer r.mu.Unlock()
	return code != "" && code == r.code
}

// pmodeColorLocked is the first palette colour no seated player wears,
// else the next in turn.
func (r *pmodeRoom) pmodeColorLocked() int {
	var used [pmodeColors]bool
	for _, p := range r.players {
		used[p.color%pmodeColors] = true
	}
	for c, taken := range used {
		if !taken {
			return c
		}
	}
	c := r.nextColor % pmodeColors
	r.nextColor++
	return c
}

func (r *pmodeRoom) addPlayerLocked(name, addr string, local bool, now time.Time) (*pmodePlayer, error) {
	if len(r.players) >= pmodeMaxPlayers {
		return nil, errPmodeFull
	}
	if addr != "" {
		// One phone holds at most two seats (a reload that lost its token,
		// a friend sharing it), so one person cannot fill the room.
		same := 0
		for _, q := range r.players {
			if q.addr == addr {
				same++
			}
		}
		if same >= pmodeSeatsPerAddr {
			return nil, errPmodeSeats
		}
	}
	name = pmodeCleanName(name)
	if name == "" {
		name = "Player " + strconv.Itoa(len(r.players)+1)
	}
	p := &pmodePlayer{
		id:     pmodeRandomHex(6),
		name:   r.pmodeUniqueNameLocked(name),
		color:  r.pmodeColorLocked(),
		local:  local,
		joined: now,
		seen:   now,
		addr:   addr,
	}
	if !local {
		p.token = pmodeRandomHex(16)
		r.byToken[p.token] = p
	}
	r.players = append(r.players, p)
	r.eventLocked("join", p.name, 0)
	r.bumpLocked()
	return p, nil
}

// Join seats a phone (from network address addr) and returns its token.
func (r *pmodeRoom) Join(name, addr string, now time.Time) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.advanceLocked(now)
	p, err := r.addPlayerLocked(name, addr, false, now)
	if err != nil {
		return "", err
	}
	return p.token, nil
}

// SetLocal seats (on) or unseats (off) the handheld's own player.
func (r *pmodeRoom) SetLocal(on bool, name string, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.advanceLocked(now)
	if cur := r.localLocked(); cur != nil {
		if !on {
			r.removeLocked(cur, now)
		}
		return nil
	}
	if !on {
		return nil
	}
	_, err := r.addPlayerLocked(name, "", true, now)
	return err
}

func (r *pmodeRoom) localLocked() *pmodePlayer {
	for _, p := range r.players {
		if p.local {
			return p
		}
	}
	return nil
}

func (r *pmodeRoom) byIDLocked(id string) *pmodePlayer {
	for _, p := range r.players {
		if p.id == id {
			return p
		}
	}
	return nil
}

// Touch notes a phone's poll and returns its seat (nil: unknown token).
func (r *pmodeRoom) touchLocked(token string, now time.Time) *pmodePlayer {
	if token == "" {
		return nil
	}
	p := r.byToken[token]
	if p == nil {
		return nil
	}
	if now.Sub(p.seen) >= pmodeAwayAfter {
		// Back from away: the scoreboard shows the seat lit again.
		p.seen = now
		r.bumpLocked()
		return p
	}
	p.seen = now
	return p
}

// Leave takes a phone's seat away.
func (r *pmodeRoom) Leave(token string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := r.byToken[token]; p != nil {
		r.removeLocked(p, now)
	}
}

func (r *pmodeRoom) removeLocked(p *pmodePlayer, now time.Time) {
	for i, q := range r.players {
		if q == p {
			r.players = append(r.players[:i], r.players[i+1:]...)
			break
		}
	}
	if p.token != "" {
		delete(r.byToken, p.token)
	}
	r.eventLocked("leave", p.name, 0)
	if r.phase == pmodeAnswer && r.buzzer == p.id {
		// The buzzer walked away: the reveal goes on, nobody is charged.
		r.buzzer = ""
		r.phase = pmodeReveal
		r.phaseEnd = now.Add(r.stageLeft)
	}
	if (r.phase == pmodeReveal || r.phase == pmodeAnswer) && r.allOutLocked(now) {
		r.resultLocked(now)
	}
	r.bumpLocked()
}

// away reports whether a phone has stopped polling.
func (p *pmodePlayer) away(now time.Time) bool {
	return !p.local && now.Sub(p.seen) >= pmodeAwayAfter
}

// ── Lobby ────────────────────────────────────────────────────────────────

// SetDeck hands the room the games it may ask about.
func (r *pmodeRoom) SetDeck(cards []pmodeCard) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deck = cards
	r.deckReady = true
	r.bumpLocked()
}

// CycleTarget steps the game length through pmodeRoundOptions.
func (r *pmodeRoom) CycleTarget(dir int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.phase != pmodeLobby && r.phase != pmodeFinal {
		return
	}
	idx := 0
	for i, n := range pmodeRoundOptions {
		if n == r.target {
			idx = i
		}
	}
	idx = (idx + dir + len(pmodeRoundOptions)) % len(pmodeRoundOptions)
	r.target = pmodeRoundOptions[idx]
	r.bumpLocked()
}

// pmodeBuildDeck turns the library into cards: games with a cover on the
// card, not hidden, one card per title (the same game on two consoles would
// make two identical answers).
func pmodeBuildDeck(lib gameLibraryReport) []pmodeCard {
	seen := map[string]bool{}
	var out []pmodeCard
	for _, sys := range lib.Systems {
		// The built-in games' covers print their name: the card would answer
		// itself.
		if sys.Name == builtInLibrarySystem {
			continue
		}
		for _, g := range sys.Games {
			if g.Hidden || !g.HasArt || strings.TrimSpace(g.Art) == "" {
				continue
			}
			title := strings.TrimSpace(g.Label())
			key := strings.ToLower(title)
			if title == "" || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, pmodeCard{Title: title, System: sys.Name, Art: g.Art})
		}
	}
	return out
}

// pmodeChoicesFor is the answer and three decoys, shuffled. Decoys from the
// same console come first: "which SNES game is this" is the fair question.
// It samples the deck rather than walking it (Start runs it once per round
// on the render thread, and a library can hold thousands of covers), and
// walks it in order only when sampling came up short in a small deck.
func pmodeChoicesFor(card pmodeCard, deck []pmodeCard, rng *mrand.Rand) []string {
	const want = pmodeChoiceCount - 1
	seen := map[string]bool{strings.ToLower(card.Title): true}
	var same, other []string
	consider := func(c pmodeCard) {
		key := strings.ToLower(c.Title)
		if seen[key] {
			return
		}
		switch {
		case c.System == card.System && len(same) < want:
			same = append(same, c.Title)
		case c.System != card.System && len(other) < want:
			other = append(other, c.Title)
		default:
			return
		}
		seen[key] = true
	}
	for tries := 0; tries < 64 && len(deck) > 0 && (len(same) < want || len(other) < want); tries++ {
		consider(deck[rng.Intn(len(deck))])
	}
	if len(same)+len(other) < want {
		for _, c := range deck {
			consider(c)
		}
	}
	out := []string{card.Title}
	for _, list := range [][]string{same, other} {
		for _, t := range list {
			if len(out) == pmodeChoiceCount {
				break
			}
			out = append(out, t)
		}
	}
	rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

func (r *pmodeRoom) newRoundLocked(card pmodeCard) *pmodeRound {
	return &pmodeRound{
		card:    card,
		choices: pmodeChoicesFor(card, r.deck, r.rng),
		// Away from the edges, and below the top third where most covers
		// print the title: the first stages should not be a free read.
		cx:     0.25 + r.rng.Float64()*0.5,
		cy:     0.35 + r.rng.Float64()*0.45,
		locked: map[string]bool{},
	}
}

// Start begins a game from the lobby, or a new one from the final screen
// with the same players.
func (r *pmodeRoom) Start(now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.advanceLocked(now)
	if r.phase != pmodeLobby && r.phase != pmodeFinal {
		return errPmodeRunning
	}
	if len(r.deck) < pmodeMinDeck {
		return errPmodeSmallDeck
	}
	present := 0
	for _, p := range r.players {
		if !p.away(now) {
			present++
		}
	}
	if present == 0 {
		return errPmodeNoPlayers
	}
	for _, p := range r.players {
		p.score, p.right, p.delta = 0, 0, 0
	}
	order := r.rng.Perm(len(r.deck))
	n := min(r.target, len(r.deck))
	r.rounds = make([]*pmodeRound, 0, n)
	r.spare = make([]pmodeCard, 0, min(pmodeMaxSpare, len(r.deck)-n))
	for i, idx := range order {
		switch {
		case i < n:
			r.rounds = append(r.rounds, r.newRoundLocked(r.deck[idx]))
		case len(r.spare) < pmodeMaxSpare:
			r.spare = append(r.spare, r.deck[idx])
		}
	}
	r.roundIdx = 0
	r.picMiss = 0
	r.game++
	r.eventLocked("start", "", 0)
	r.enterIntroLocked(now)
	return nil
}

// EndGame goes back to the lobby; everyone keeps their seat.
func (r *pmodeRoom) EndGame() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.phase = pmodeLobby
	r.rounds = nil
	r.spare = nil
	r.buzzer = ""
	for _, p := range r.players {
		p.score, p.right, p.delta = 0, 0, 0
	}
	r.bumpLocked()
}

// ── Flow ─────────────────────────────────────────────────────────────────

func (r *pmodeRoom) current() *pmodeRound {
	if r.roundIdx < 0 || r.roundIdx >= len(r.rounds) {
		return nil
	}
	return r.rounds[r.roundIdx]
}

func pmodeStageLen(stage int) time.Duration {
	if stage >= pmodeLastStage {
		return pmodeLastStageFor
	}
	return pmodeStageFor
}

func (r *pmodeRoom) enterIntroLocked(now time.Time) {
	r.phase = pmodeIntro
	r.phaseEnd = now.Add(pmodeIntroFor)
	r.introSince = now
	r.introLate = false
	r.stage = 0
	r.buzzer = ""
	for _, p := range r.players {
		p.delta = 0
	}
	r.eventLocked("round", "", r.roundIdx+1)
	r.bumpLocked()
}

func (r *pmodeRoom) resultLocked(now time.Time) {
	r.phase = pmodeResult
	r.phaseEnd = now.Add(pmodeResultFor)
	r.buzzer = ""
	if cur := r.current(); cur != nil && cur.winner == "" {
		r.eventLocked("nobody", cur.card.Title, 0)
	}
	r.bumpLocked()
}

// allOutLocked reports whether no present player can still buzz this round.
func (r *pmodeRoom) allOutLocked(now time.Time) bool {
	cur := r.current()
	if cur == nil {
		return true
	}
	for _, p := range r.players {
		if !cur.locked[p.id] && !p.away(now) {
			return false
		}
	}
	return true
}

// replaceRoundLocked swaps the current round, whose cover would not decode,
// for a spare game; with no spare left it drops that round alone, and the
// game ends only when no round is left to play.
func (r *pmodeRoom) replaceRoundLocked(now time.Time) {
	r.introSince = now
	r.introLate = false
	if len(r.spare) > 0 {
		r.rounds[r.roundIdx] = r.newRoundLocked(r.spare[0])
		r.spare = r.spare[1:]
		r.bumpLocked()
		return
	}
	last := len(r.rounds) - 1
	copy(r.rounds[r.roundIdx:], r.rounds[r.roundIdx+1:])
	r.rounds[last] = nil
	r.rounds = r.rounds[:last]
	if r.roundIdx >= len(r.rounds) {
		r.endRoundsLocked()
		return
	}
	r.bumpLocked()
}

// endRoundsLocked ends the game at the rounds already played: the lobby when
// none was, else the final screen about the last one played.
func (r *pmodeRoom) endRoundsLocked() {
	for i := r.roundIdx; i < len(r.rounds); i++ {
		r.rounds[i] = nil
	}
	r.rounds = r.rounds[:r.roundIdx]
	r.buzzer = ""
	if r.roundIdx == 0 {
		r.phase = pmodeLobby
		r.eventLocked("nocovers", "", 0)
	} else {
		r.roundIdx--
		r.phase = pmodeFinal
		r.eventLocked("final", r.leaderNameLocked(), 0)
	}
	r.bumpLocked()
}

// Tick moves the room past deadlines and tidies the lobby.
func (r *pmodeRoom) Tick(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.advanceLocked(now)
	if r.phase != pmodeLobby {
		return
	}
	for {
		var gone *pmodePlayer
		for _, p := range r.players {
			if !p.local && now.Sub(p.seen) >= pmodeLobbyDropAfter {
				gone = p
				break
			}
		}
		if gone == nil {
			return
		}
		r.removeLocked(gone, now)
	}
}

func (r *pmodeRoom) advanceLocked(now time.Time) {
	for step := 0; step < 2*pmodeStages+4; step++ {
		switch r.phase {
		case pmodeIntro:
			if now.Before(r.phaseEnd) {
				return
			}
			cur := r.current()
			if cur == nil {
				r.phase = pmodeLobby
				r.bumpLocked()
				return
			}
			if cur.pic == nil {
				if cur.picErr {
					r.replaceRoundLocked(now)
					continue
				}
				if now.Sub(r.introSince) >= pmodePictureWait {
					// Twice in a row: the decoder is stuck or far too slow,
					// and trying every spare would leave the room waiting
					// for minutes. End the game where it is.
					if r.picMiss++; r.picMiss >= 2 {
						logWarn("partymode", "covers are not decoding; ending the game")
						r.endRoundsLocked()
						return
					}
					r.replaceRoundLocked(now)
					continue
				}
				if !r.introLate {
					// Phones show "Shuffling the next cover…" from here.
					r.introLate = true
					r.bumpLocked()
				}
				// Waiting for the decode; SetPicture moves on.
				return
			}
			r.picMiss = 0
			r.phase = pmodeReveal
			r.stage = 0
			r.phaseEnd = now.Add(pmodeStageLen(0))
			r.eventLocked("stage", "", pmodeStagePoints[0])
			r.bumpLocked()
		case pmodeReveal:
			if now.Before(r.phaseEnd) {
				return
			}
			if r.stage >= pmodeLastStage {
				r.resultLocked(now)
				continue
			}
			r.stage++
			base := r.phaseEnd
			if now.Sub(base) > time.Second {
				base = now
			}
			r.phaseEnd = base.Add(pmodeStageLen(r.stage))
			r.eventLocked("stage", "", pmodeStagePoints[r.stage])
			r.bumpLocked()
		case pmodeAnswer:
			if now.Before(r.phaseEnd) {
				return
			}
			r.wrongLocked(r.buzzer, now, true)
		case pmodeResult:
			if now.Before(r.phaseEnd) {
				return
			}
			if r.roundIdx+1 < len(r.rounds) {
				// The answer's cover stays for the final screen only when
				// this was the last round.
				if cur := r.current(); cur != nil {
					cur.pic = nil
				}
				r.roundIdx++
				r.enterIntroLocked(now)
				continue
			}
			r.phase = pmodeFinal
			r.eventLocked("final", r.leaderNameLocked(), 0)
			r.bumpLocked()
			return
		default:
			return
		}
	}
}

func (r *pmodeRoom) leaderNameLocked() string {
	ranked := r.rankedLocked()
	if len(ranked) == 0 {
		return ""
	}
	return ranked[0].name
}

// rankedLocked is the players best first: score, then right answers, then
// who joined first.
func (r *pmodeRoom) rankedLocked() []*pmodePlayer {
	out := append([]*pmodePlayer(nil), r.players...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].right > out[j].right
	})
	return out
}

// pmodeRanks gives tied players the same place: 1, 2, 2, 4.
func pmodeRanks(ranked []*pmodePlayer) map[string]int {
	out := make(map[string]int, len(ranked))
	for i, p := range ranked {
		if i > 0 && p.score == ranked[i-1].score && p.right == ranked[i-1].right {
			out[p.id] = out[ranked[i-1].id]
			continue
		}
		out[p.id] = i + 1
	}
	return out
}

// ── Plays ────────────────────────────────────────────────────────────────

func (r *pmodeRoom) buzzLocked(p *pmodePlayer, now time.Time) error {
	r.advanceLocked(now)
	switch r.phase {
	case pmodeReveal:
	case pmodeAnswer:
		if r.buzzer == p.id {
			// A second press of one's own buzz: already answering.
			return nil
		}
		return errPmodeTaken
	default:
		return errPmodeNotNow
	}
	cur := r.current()
	if cur == nil {
		return errPmodeNotNow
	}
	if cur.locked[p.id] {
		return errPmodeLocked
	}
	left := r.phaseEnd.Sub(now)
	if left < 500*time.Millisecond {
		left = 500 * time.Millisecond
	}
	r.stageLeft = left
	r.buzzer = p.id
	r.phase = pmodeAnswer
	r.phaseEnd = now.Add(pmodeAnswerFor)
	r.eventLocked("buzz", p.name, pmodeStagePoints[r.stage])
	r.bumpLocked()
	return nil
}

// Buzz is a phone's buzzer.
func (r *pmodeRoom) Buzz(token string, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.touchLocked(token, now)
	if p == nil {
		return errPmodeUnknownPlayer
	}
	return r.buzzLocked(p, now)
}

// BuzzLocal is the handheld seat's buzzer (A).
func (r *pmodeRoom) BuzzLocal(now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.localLocked()
	if p == nil {
		return errPmodeUnknownPlayer
	}
	return r.buzzLocked(p, now)
}

func (r *pmodeRoom) answerLocked(p *pmodePlayer, choice int, now time.Time) (bool, error) {
	r.advanceLocked(now)
	if r.phase != pmodeAnswer || r.buzzer != p.id {
		return false, errPmodeNotNow
	}
	cur := r.current()
	if cur == nil {
		return false, errPmodeNotNow
	}
	if choice < 0 || choice >= len(cur.choices) {
		return false, errPmodeBadChoice
	}
	if cur.choices[choice] != cur.card.Title {
		r.wrongLocked(p.id, now, false)
		return false, nil
	}
	points := pmodeStagePoints[r.stage]
	p.score += points
	p.delta += points
	p.right++
	cur.winner = p.id
	cur.winnerName = p.name
	cur.points = points
	r.eventLocked("right", p.name, points)
	r.resultLocked(now)
	return true, nil
}

// Answer is a phone's pick among the choices.
func (r *pmodeRoom) Answer(token string, choice int, now time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.touchLocked(token, now)
	if p == nil {
		return false, errPmodeUnknownPlayer
	}
	return r.answerLocked(p, choice, now)
}

// AnswerLocal is the handheld seat's pick.
func (r *pmodeRoom) AnswerLocal(choice int, now time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.localLocked()
	if p == nil {
		return false, errPmodeUnknownPlayer
	}
	return r.answerLocked(p, choice, now)
}

// wrongLocked charges a wrong pick (or the clock running out), takes the
// player out of the round and lets the reveal go on.
func (r *pmodeRoom) wrongLocked(id string, now time.Time, timeout bool) {
	cur := r.current()
	if p := r.byIDLocked(id); p != nil && cur != nil {
		pen := pmodeWrongPenalty
		if pen > p.score {
			pen = p.score
		}
		p.score -= pen
		p.delta -= pen
		cur.locked[p.id] = true
		cur.wrong = append(cur.wrong, p.name)
		kind := "wrong"
		if timeout {
			kind = "timeout"
		}
		r.eventLocked(kind, p.name, -pen)
	}
	r.buzzer = ""
	if r.allOutLocked(now) {
		r.resultLocked(now)
		return
	}
	r.phase = pmodeReveal
	r.phaseEnd = now.Add(r.stageLeft)
	r.bumpLocked()
}

// ── Pictures ─────────────────────────────────────────────────────────────

// NeedPicture names a round whose cover the worker should decode: the
// current one first, then the next, so the next intro rarely waits.
func (r *pmodeRoom) NeedPicture() (*pmodeRound, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.phase == pmodeLobby || r.phase == pmodeFinal {
		return nil, ""
	}
	for i := r.roundIdx; i < len(r.rounds) && i <= r.roundIdx+1; i++ {
		if rd := r.rounds[i]; rd.pic == nil && !rd.picErr {
			return rd, rd.card.Art
		}
	}
	return nil, ""
}

// SetPicture stores a decoded cover, or marks the round's cover broken.
func (r *pmodeRoom) SetPicture(rd *pmodeRound, pic *image.NRGBA, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	still := false
	for _, x := range r.rounds {
		if x == rd {
			still = true
			break
		}
	}
	if !still {
		return
	}
	if pic == nil || pic.Rect.Dx() < 8 || pic.Rect.Dy() < 8 {
		rd.picErr = true
		// A later round's broken cover is swapped for a spare now, so its
		// intro does not wait for a decode it could have had already.
		for i := r.roundIdx + 1; i < len(r.rounds); i++ {
			if r.rounds[i] == rd && len(r.spare) > 0 {
				r.rounds[i] = r.newRoundLocked(r.spare[0])
				r.spare = r.spare[1:]
				break
			}
		}
	} else {
		rd.pic = pic
	}
	r.advanceLocked(now)
	r.bumpLocked()
}

// pmodeCropRect is the stage's square: frac of the cover's shorter side,
// centred on (cx, cy) as far as the cover's edges allow. frac >= 1 is the
// whole cover.
func pmodeCropRect(b image.Rectangle, cx, cy, frac float64) image.Rectangle {
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 || frac >= 1 {
		return b
	}
	short := min(w, h)
	side := int(math.Round(frac * float64(short)))
	side = max(min(8, short), min(side, short))
	x0 := int(math.Round(cx*float64(w))) - side/2
	y0 := int(math.Round(cy*float64(h))) - side/2
	x0 = max(0, min(x0, w-side))
	y0 = max(0, min(y0, h-side))
	return image.Rect(b.Min.X+x0, b.Min.Y+y0, b.Min.X+x0+side, b.Min.Y+y0+side)
}

// pmodeImageKey names one stage's picture in URLs and caches.
func pmodeImageKey(round, stage int) string {
	return "r" + strconv.Itoa(round) + "s" + strconv.Itoa(stage)
}

// ImageFor returns the picture a phone may see for round/stage right now:
// only the current round, and only stages already shown — a later stage's
// URL asked for early is refused, so the page cannot peek ahead.
func (r *pmodeRoom) ImageFor(token string, round, stage int, now time.Time) (*image.NRGBA, image.Rectangle, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.advanceLocked(now)
	if r.touchLocked(token, now) == nil {
		return nil, image.Rectangle{}, false
	}
	cur := r.current()
	if cur == nil || cur.pic == nil || round != r.roundIdx || stage < 0 || stage > pmodeLastStage {
		return nil, image.Rectangle{}, false
	}
	switch r.phase {
	case pmodeReveal, pmodeAnswer:
		if stage > r.stage {
			return nil, image.Rectangle{}, false
		}
	case pmodeResult, pmodeFinal:
	default:
		return nil, image.Rectangle{}, false
	}
	return cur.pic, pmodeCropRect(cur.pic.Rect, cur.cx, cur.cy, pmodeStageFracs[stage]), true
}

// ── Views ────────────────────────────────────────────────────────────────

// pmodePlayerView is one scoreboard line.
type pmodePlayerView struct {
	ID     string
	Name   string
	Color  int
	Score  int
	Delta  int
	Right  int
	Rank   int
	Local  bool
	Away   bool
	Locked bool
}

// pmodeView is what the handheld draws, copied out under the lock.
type pmodeView struct {
	Code      string
	Phase     pmodePhase
	Version   uint64
	Round     int // 1-based
	Rounds    int
	Target    int
	Stage     int
	Points    int
	Left      time.Duration
	PhaseFor  time.Duration
	Waiting   bool
	Buzzer    string
	BuzzLocal bool
	// Players in join order; Rank orders the scoreboard.
	Players   []pmodePlayerView
	DeckSize  int
	DeckReady bool
	// The current round's picture and the stage's crop of it.
	Pic      *image.NRGBA
	PicRound int
	Crop     image.Rectangle
	// Choices is set while the handheld's own seat is answering.
	Choices []string
	// Title, System, Winner, WinPoints and Wrong describe the round just
	// played (result) or the last one (final).
	Title     string
	System    string
	Winner    string
	WinPoints int
	Wrong     []string
	HasLocal  bool
	LocalOut  bool
}

func (r *pmodeRoom) phaseForLocked() time.Duration {
	switch r.phase {
	case pmodeIntro:
		return pmodeIntroFor
	case pmodeReveal:
		return pmodeStageLen(r.stage)
	case pmodeAnswer:
		return pmodeAnswerFor
	case pmodeResult:
		return pmodeResultFor
	}
	return 0
}

// View advances the room and copies what the handheld draws.
func (r *pmodeRoom) View(now time.Time) pmodeView {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.advanceLocked(now)
	v := pmodeView{
		Code:      r.code,
		Phase:     r.phase,
		Version:   r.version,
		Rounds:    len(r.rounds),
		Target:    r.target,
		Stage:     r.stage,
		DeckSize:  len(r.deck),
		DeckReady: r.deckReady,
		PhaseFor:  r.phaseForLocked(),
		PicRound:  r.roundIdx,
	}
	if r.phase != pmodeLobby {
		v.Round = r.roundIdx + 1
		if v.Round > v.Rounds {
			v.Round = v.Rounds
		}
	}
	if r.phase == pmodeReveal || r.phase == pmodeAnswer {
		v.Points = pmodeStagePoints[r.stage]
	}
	if v.PhaseFor > 0 {
		v.Left = r.phaseEnd.Sub(now)
		if v.Left < 0 {
			v.Left = 0
			v.Waiting = r.phase == pmodeIntro
		}
	}
	cur := r.current()
	ranks := pmodeRanks(r.rankedLocked())
	for _, p := range r.players {
		pv := pmodePlayerView{
			ID: p.id, Name: p.name, Color: p.color, Score: p.score, Delta: p.delta,
			Right: p.right, Rank: ranks[p.id], Local: p.local, Away: p.away(now),
		}
		if cur != nil && (r.phase == pmodeReveal || r.phase == pmodeAnswer) {
			pv.Locked = cur.locked[p.id]
		}
		if p.local {
			v.HasLocal = true
			v.LocalOut = pv.Locked
		}
		v.Players = append(v.Players, pv)
	}
	if b := r.byIDLocked(r.buzzer); b != nil && r.phase == pmodeAnswer {
		v.Buzzer = b.name
		v.BuzzLocal = b.local
		if b.local && cur != nil {
			v.Choices = append([]string(nil), cur.choices...)
		}
	}
	if cur != nil && cur.pic != nil {
		v.Pic = cur.pic
		frac := pmodeStageFracs[r.stage]
		if r.phase == pmodeResult || r.phase == pmodeFinal {
			frac = 1
		}
		v.Crop = pmodeCropRect(cur.pic.Rect, cur.cx, cur.cy, frac)
	}
	if cur != nil && (r.phase == pmodeResult || r.phase == pmodeFinal) {
		v.Title, v.System = cur.card.Title, cur.card.System
		v.WinPoints = cur.points
		v.Wrong = append([]string(nil), cur.wrong...)
		v.Winner = cur.winnerName
	}
	return v
}

// pmodeStateJSON is what a phone polls.
type pmodeStateJSON struct {
	V       uint64            `json:"v"`
	Phase   string            `json:"phase"`
	Code    string            `json:"code"`
	Round   int               `json:"round"`
	Rounds  int               `json:"rounds"`
	Target  int               `json:"target"`
	Stage   int               `json:"stage"`
	Stages  int               `json:"stages"`
	Points  int               `json:"points"`
	LeftMS  int64             `json:"left_ms"`
	ForMS   int64             `json:"for_ms"`
	Waiting bool              `json:"waiting,omitempty"`
	Buzzer  string            `json:"buzzer,omitempty"`
	Img     string            `json:"img,omitempty"`
	ImgR    int               `json:"img_r"`
	ImgS    int               `json:"img_s"`
	ImgG    int               `json:"img_g"`
	Me      pmodeMeJSON       `json:"me"`
	Players []pmodePlayerJSON `json:"players"`
	Result  *pmodeResultJSON  `json:"result,omitempty"`
}

type pmodeMeJSON struct {
	Name      string   `json:"name"`
	Color     int      `json:"color"`
	Score     int      `json:"score"`
	Delta     int      `json:"delta"`
	Rank      int      `json:"rank"`
	Locked    bool     `json:"locked"`
	Answering bool     `json:"answering"`
	Choices   []string `json:"choices,omitempty"`
}

type pmodePlayerJSON struct {
	Name  string `json:"name"`
	Color int    `json:"color"`
	Score int    `json:"score"`
	Delta int    `json:"delta"`
	Rank  int    `json:"rank"`
	You   bool   `json:"you,omitempty"`
	Away  bool   `json:"away,omitempty"`
	Here  bool   `json:"here,omitempty"`
	Out   bool   `json:"out,omitempty"`
}

type pmodeResultJSON struct {
	Title  string   `json:"title"`
	System string   `json:"system"`
	Winner string   `json:"winner,omitempty"`
	Points int      `json:"points"`
	Wrong  []string `json:"wrong,omitempty"`
}

// State is a phone's view; ok is false for an unknown token.
func (r *pmodeRoom) State(token string, now time.Time) (pmodeStateJSON, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.advanceLocked(now)
	me := r.touchLocked(token, now)
	if me == nil {
		return pmodeStateJSON{}, false
	}
	s := pmodeStateJSON{
		V:      r.version,
		Phase:  string(r.phase),
		Code:   r.code,
		Rounds: len(r.rounds),
		Target: r.target,
		Stage:  r.stage,
		Stages: pmodeStages,
		ImgR:   r.roundIdx,
		ImgS:   r.stage,
		ImgG:   r.game,
	}
	if r.phase != pmodeLobby {
		s.Round = min(r.roundIdx+1, len(r.rounds))
	}
	if pf := r.phaseForLocked(); pf > 0 {
		left := r.phaseEnd.Sub(now)
		if left < 0 {
			left = 0
			s.Waiting = r.phase == pmodeIntro
		}
		s.LeftMS = left.Milliseconds()
		s.ForMS = pf.Milliseconds()
	}
	cur := r.current()
	if r.phase == pmodeReveal || r.phase == pmodeAnswer {
		s.Points = pmodeStagePoints[r.stage]
	}
	if cur != nil && cur.pic != nil {
		switch r.phase {
		case pmodeReveal, pmodeAnswer:
			s.Img = pmodeImageKey(r.roundIdx, r.stage)
		case pmodeResult, pmodeFinal:
			s.ImgS = pmodeLastStage
			s.Img = pmodeImageKey(r.roundIdx, pmodeLastStage)
		}
	}
	ranks := pmodeRanks(r.rankedLocked())
	for _, p := range r.players {
		pj := pmodePlayerJSON{
			Name: p.name, Color: p.color, Score: p.score, Delta: p.delta, Rank: ranks[p.id],
			You: p == me, Away: p.away(now), Here: p.local,
		}
		if cur != nil && (r.phase == pmodeReveal || r.phase == pmodeAnswer) {
			pj.Out = cur.locked[p.id]
		}
		s.Players = append(s.Players, pj)
	}
	s.Me = pmodeMeJSON{Name: me.name, Color: me.color, Score: me.score, Delta: me.delta, Rank: ranks[me.id]}
	if cur != nil && (r.phase == pmodeReveal || r.phase == pmodeAnswer) {
		s.Me.Locked = cur.locked[me.id]
	}
	if b := r.byIDLocked(r.buzzer); b != nil && r.phase == pmodeAnswer {
		s.Buzzer = b.name
		if b == me && cur != nil {
			s.Me.Answering = true
			s.Me.Choices = append([]string(nil), cur.choices...)
		}
	}
	if cur != nil && (r.phase == pmodeResult || r.phase == pmodeFinal) {
		s.Result = &pmodeResultJSON{Title: cur.card.Title, System: cur.card.System, Winner: cur.winnerName,
			Points: cur.points, Wrong: append([]string(nil), cur.wrong...)}
	}
	return s, true
}

// PlayerCount is the number of seats taken.
func (r *pmodeRoom) PlayerCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.players)
}

// PresentPhones is the number of phones still polling: the handheld's own
// seat and phones gone quiet do not keep the screen awake.
func (r *pmodeRoom) PresentPhones(now time.Time) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, p := range r.players {
		if !p.local && !p.away(now) {
			n++
		}
	}
	return n
}
