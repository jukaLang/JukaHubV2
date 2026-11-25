package main

// ghost.go — Ghost Challenges: race a friend's run in JukaHub's own arcade
// games.
//
// Every run of Flappy, Snake, Tetris, 2048 and Brick Breaker is recorded as
// it is played: the seed its randomness came from, the score every 100 ms and,
// for Flappy, the bird's height every 50 ms — a few kilobytes a minute
// (ghostRun). Your best run of each game is kept. Send it to a friend — to a
// JukaHub paired under Nearby devices, or as a .jghost file in the Ghosts
// folder for any other way (Chat, a card, a cable) — and their JukaHub races
// it: the same seed, so the same pipes in Flappy, the same pieces in Tetris,
// the same first tiles in 2048; the ghost's score counts up beside theirs as
// it did for you, and in Flappy the ghost bird flies the run you flew, at your
// height, through the same pipes. At the end the card says who won, by how
// much, and A sends your run back.
//
// Why these games and not an emulator's: the arcade games are JukaHub's own
// code, so a run is only a seed and a few numbers and the ghost can be drawn
// in the game. A RetroArch input replay (BSV) needs the same ROM and core
// build on both sides and cannot be drawn into the friend's game; it is the
// next step (MODERNIZATION.md's ideas parking lot).
//
// The race is fair as far as the games are deterministic: the seed decides
// everything the game itself decides, and the player decides the rest. Snake
// places its food away from the snake, so two snakes that went different
// ways see different food after the first bite; the score is still the race.
//
// Files: <data>/ghosts/best-<game>.json (your best run of each game) and
// <data>/ghosts/inbox/<id>.json (runs from friends, with your best attempt at
// each), at most ghostInboxMax of them, the oldest going first. A friend's
// run is checked before it is kept: the game must be one of the five, the
// numbers in range, the file under ghostMaxFile (ghostParseRun).
//
// State: ghost (the run being recorded, the one being raced) belongs to the
// arcade games and is guarded by minigameMu like them (games.go); the stored
// runs are read and written off the main thread through ghostStoreMu.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	mathrand "math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	ghostVersion    = 1
	ghostPaceStepMS = 100
	ghostYStepMS    = 50
	// ghostMaxRunMS is the longest run kept: an hour.
	ghostMaxRunMS = 60 * 60 * 1000
	ghostMaxPace  = ghostMaxRunMS / ghostPaceStepMS
	ghostMaxY     = ghostMaxRunMS / ghostYStepMS
	// ghostMaxFile bounds a run read from a friend or a file.
	ghostMaxFile = 900 << 10
	// ghostInboxMax is how many friends' runs are kept.
	ghostInboxMax = 30
	// ghostPeerPath is where a paired JukaHub sends a run (peernet.go).
	ghostPeerPath = "/peer/v1/ghost"
	// ghostFileExt is a run saved as a file.
	ghostFileExt = ".jghost"
	// notifActGhost opens a friend's run from the bell.
	notifActGhost = "ghost.open"
)

// ghostGames are the games that record runs, by their stable names.
var ghostGames = []struct {
	id          gameID
	name, title string
}{
	{gameFlappy, "flappy", "Flappy"},
	{gameSnake, "snake", "Snake"},
	{gameTetris, "tetris", "Tetris"},
	{game2048, "2048", "2048"},
	{gameBrickBreaker, "brickbreaker", "Brick Breaker"},
}

// ghostGameName is a game's stable name, "" for a game that records none.
func ghostGameName(id gameID) string {
	for _, g := range ghostGames {
		if g.id == id {
			return g.name
		}
	}
	return ""
}

// ghostGameID is the game a name stands for, gameNone for none.
func ghostGameID(name string) gameID {
	for _, g := range ghostGames {
		if g.name == name {
			return g.id
		}
	}
	return gameNone
}

// ghostGameTitle is a game's name on screen.
func ghostGameTitle(name string) string {
	for _, g := range ghostGames {
		if g.name == name {
			return g.title
		}
	}
	return name
}

// ghostRun is one run: what was played and how it went.
type ghostRun struct {
	V      int    `json:"v"`
	ID     string `json:"id"`
	Game   string `json:"game"`
	Seed   int64  `json:"seed"`
	Score  int    `json:"score"`
	MS     int    `json:"ms"`
	Player string `json:"player,omitempty"`
	At     int64  `json:"at,omitempty"` // unix seconds; 0 before the clock was set
	Step   int    `json:"step"`
	Pace   []int  `json:"pace"`
	YStep  int    `json:"ystep,omitempty"`
	Y      []int  `json:"y,omitempty"`
	// Kept on this side only: your best score against a friend's run, and
	// whether you beat it.
	Tried int  `json:"tried,omitempty"`
	Beat  bool `json:"beat,omitempty"`
	Tries int  `json:"tries,omitempty"`
}

// ghostScoreAt is the run's score ms into it (its final score after its end).
func (g *ghostRun) ghostScoreAt(ms int) int {
	if g == nil || len(g.Pace) == 0 || g.Step <= 0 {
		return 0
	}
	if ms >= g.MS {
		return g.Score
	}
	i := ms / g.Step
	if i >= len(g.Pace) {
		i = len(g.Pace) - 1
	}
	if i < 0 {
		i = 0
	}
	return g.Pace[i]
}

// ghostYAt is the bird's height ms into a Flappy run, and false after the
// bird's last sample (it crashed there).
func (g *ghostRun) ghostYAt(ms float64) (float64, bool) {
	if g == nil || g.YStep <= 0 || len(g.Y) == 0 {
		return 0, false
	}
	f := ms / float64(g.YStep)
	i := int(f)
	if i < 0 || i >= len(g.Y)-1 {
		return 0, false
	}
	t := f - float64(i)
	return float64(g.Y[i])*(1-t) + float64(g.Y[i+1])*t, true
}

// ghostParseRun reads a run from a friend or a file, checked: one of the
// games, numbers in range, series no longer than an hour.
func ghostParseRun(data []byte) (ghostRun, error) {
	var g ghostRun
	if len(data) == 0 || len(data) > ghostMaxFile {
		return g, errors.New("not a ghost run")
	}
	if err := json.Unmarshal(data, &g); err != nil {
		return g, errors.New("not a ghost run")
	}
	switch {
	case g.V != ghostVersion:
		return g, errors.New("made by another version of JukaHub")
	case ghostGameID(g.Game) == gameNone:
		return g, errors.New("a game this JukaHub does not have")
	case g.Step != ghostPaceStepMS || len(g.Pace) == 0 || len(g.Pace) > ghostMaxPace:
		return g, errors.New("a damaged run")
	case g.Score < 0 || g.Score > 100_000_000 || g.MS <= 0 || g.MS > ghostMaxRunMS:
		return g, errors.New("a damaged run")
	case len(g.Y) > ghostMaxY || (len(g.Y) > 0 && g.YStep != ghostYStepMS):
		return g, errors.New("a damaged run")
	}
	for _, v := range g.Pace {
		if v < 0 || v > g.Score {
			return g, errors.New("a damaged run")
		}
	}
	g.ID = ghostCleanID(g.ID)
	if g.ID == "" {
		g.ID = ghostNewID()
	}
	g.Player = ghostCleanName(g.Player)
	if g.Tried < 0 {
		g.Tried = 0
	}
	return g, nil
}

// ghostCleanID keeps an id to 8–32 lower-case hex digits.
func ghostCleanID(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if len(id) < 8 || len(id) > 32 {
		return ""
	}
	for _, r := range id {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return ""
		}
	}
	return id
}

// ghostCleanName keeps a player's name printable and short.
func ghostCleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if r := []rune(s); len(r) > 24 {
		s = string(r[:24])
	}
	return s
}

// ghostNewID is a random id.
func ghostNewID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
}

// ghostPlayerName is the name a run carries: Settings' name, else the
// device's.
func ghostPlayerName() string {
	if n := ghostCleanName(userName(appConfig)); n != "" {
		return n
	}
	return ghostCleanName(peerDeviceName())
}

// ── Recording and racing (minigameMu) ─────────────────────────────────────

// arcadeRng is the arcade games' randomness, seeded for every run so a run can
// be played again with the same pipes, pieces and tiles. Guarded by
// minigameMu, like the games.
var arcadeRng = mathrand.New(mathrand.NewSource(time.Now().UnixNano()))

// ghost is the run being recorded and the run being raced.
var ghost struct {
	rec       ghostRun
	recStart  time.Time
	recording bool
	finished  bool
	// vs is the run raced (nil: none), and vsLabel its name on screen.
	vs      *ghostRun
	vsLabel string
	// last is the last finished run, offered on the game-over card.
	last *ghostRun
	// result is the game-over card's line about the race.
	result string
}

// ghostRunBegin starts a run of id: called by each game's init (start and
// restart). The randomness is seeded with the raced run's seed when it is
// that game's, else afresh.
func ghostRunBegin(id gameID) {
	name := ghostGameName(id)
	seed := time.Now().UnixNano()
	if ghost.vs != nil {
		if ghost.vs.Game == name {
			seed = ghost.vs.Seed
		} else {
			ghost.vs = nil
		}
	}
	arcadeRng.Seed(seed)
	ghost.rec = ghostRun{V: ghostVersion, Game: name, Seed: seed, Step: ghostPaceStepMS}
	if id == gameFlappy {
		ghost.rec.YStep = ghostYStepMS
	}
	ghost.recStart = time.Now()
	ghost.recording = name != ""
	ghost.finished = false
	ghost.result = ""
	ghost.last = nil
}

// ghostRace starts game name against run: the Games screen opens on it with
// the run's seed. Main thread.
func ghostRace(config *Config, run ghostRun, label string) {
	id := ghostGameID(run.Game)
	if id == gameNone || config == nil {
		return
	}
	idx := sceneIndexExact(config, "Games")
	if idx < 0 {
		ShowToast("This setup has no Games screen", ToastKindInfo)
		return
	}
	minigameMu.Lock()
	r := run
	ghost.vs, ghost.vsLabel = &r, label
	minigameMu.Unlock()
	if idx != currentSceneIndex {
		changeSceneTo(config, idx)
	}
	queueGameStart(config, id)
}

// ghostLeft forgets the race when the game is left (the Games menu shows).
func ghostLeft() {
	minigameMu.Lock()
	ghost.vs, ghost.vsLabel = nil, ""
	ghost.recording = false
	minigameMu.Unlock()
}

// ghostElapsedMS is how far into the run the game is.
func ghostElapsedMS() int {
	if ghost.finished {
		return ghost.rec.MS
	}
	return int(time.Since(ghost.recStart) / time.Millisecond)
}

// ghostTick records the run: the tick goroutine, under minigameMu.
func ghostTick() {
	if !ghost.recording || gameOver {
		return
	}
	el := int(time.Since(ghost.recStart) / time.Millisecond)
	if el > ghostMaxRunMS {
		ghost.recording = false
		return
	}
	for len(ghost.rec.Pace)*ghostPaceStepMS <= el {
		ghost.rec.Pace = append(ghost.rec.Pace, gameScore)
	}
	if activeGame == gameFlappy && ghost.rec.YStep > 0 {
		ft := int(flappyClock * 1000)
		for len(ghost.rec.Y)*ghostYStepMS <= ft && len(ghost.rec.Y) < ghostMaxY {
			ghost.rec.Y = append(ghost.rec.Y, int(math.Round(flappyY)))
		}
	}
}

// ghostFrame ends the run when its game is over: the render, under
// minigameMu, once per run.
func ghostFrame() {
	if !ghost.recording || ghost.finished || !gameOver {
		return
	}
	ghost.recording = false
	ghost.finished = true
	run := ghost.rec
	run.Pace = append([]int(nil), ghost.rec.Pace...)
	run.Y = append([]int(nil), ghost.rec.Y...)
	run.Score = gameScore
	run.MS = max(int(time.Since(ghost.recStart)/time.Millisecond), 1)
	run.ID = ghostNewID()
	run.Player = ghostPlayerName()
	if now := time.Now(); now.Year() >= 2024 {
		run.At = now.Unix()
	}
	ghost.rec.MS = run.MS
	if run.Score <= 0 || len(run.Pace) == 0 {
		return
	}
	ghost.last = &run
	if vs := ghost.vs; vs != nil {
		diff := run.Score - vs.Score
		switch {
		case diff > 0:
			ghost.result = fmt.Sprintf("You beat %s by %d!", ghost.vsLabel, diff)
		case diff == 0:
			ghost.result = fmt.Sprintf("A tie with %s: %d each", ghost.vsLabel, run.Score)
		default:
			ghost.result = fmt.Sprintf("%s wins by %d · Start to try again", ghost.vsLabel, -diff)
		}
		if vs.ID != "" {
			go ghostNoteAttempt(vs.ID, run.Score)
		}
	}
	go ghostKeepBest(run)
}

// ghostDraw draws the race over the game: the ghost's score beside yours
// and, in Flappy, the ghost bird. The render, under minigameMu.
func ghostDraw(renderer *sdl.Renderer, config *Config) {
	small, _ := getCachedFont(config, "small")
	if small == nil {
		return
	}
	vs := ghost.vs
	if vs != nil && activeGame == ghostGameID(vs.Game) {
		el := ghostElapsedMS()
		if activeGame == gameFlappy {
			if y, ok := vs.ghostYAt(flappyClock * 1000); ok {
				ox := (screenWidth - flappyW) / 2
				oy := (screenHeight - flappyH) / 2
				bx, by := ox+flappyBirdX, oy+int32(math.Round(y))
				fillCircle(renderer, bx, by, flappyBirdR, sdl.Color{R: 235, G: 240, B: 255, A: 120})
				strokeCircle(renderer, bx, by, flappyBirdR, sdl.Color{R: 255, G: 255, B: 255, A: 170})
			}
		}
		line := fmt.Sprintf("%s %d · You %d", ghost.vsLabel, vs.ghostScoreAt(el), gameScore)
		if el >= vs.MS {
			line = fmt.Sprintf("%s finished on %d · You %d", ghost.vsLabel, vs.Score, gameScore)
		}
		ghostChip(renderer, config, small, line, 16)
	}
	if !gameOver || !ghost.finished {
		return
	}
	lines := []string{}
	if ghost.result != "" {
		lines = append(lines, ghost.result)
	}
	if ghost.last != nil {
		lines = append(lines, hintSentence("%s Send this run to a friend", "Y", "Send"))
	}
	y := screenHeight/2 + 112
	for _, l := range lines {
		ghostChip(renderer, config, small, l, y)
		y += int32(small.Height()) + 18
	}
}

// ghostChip is a line of text on a dark pill, centred at height y.
func ghostChip(renderer *sdl.Renderer, config *Config, font *ttf.Font, text string, y int32) {
	w, h := measureText(font, text)
	pad := int32(14)
	x := (screenWidth - w) / 2
	fillRoundedRect(renderer, x-pad, y-6, w+2*pad, h+12, (h+12)/2, sdl.Color{R: 10, G: 14, B: 22, A: 200})
	renderText(renderer, config, font, text, sdl.Color{R: 245, G: 248, B: 255, A: 255}, x, y)
}

// ghostOfferRun is the finished run the game-over card offers, if any.
// Under minigameMu.
func ghostOfferRun() (ghostRun, bool) {
	if !gameOver || ghost.last == nil || ghost.last.Game != ghostGameName(activeGame) {
		return ghostRun{}, false
	}
	return *ghost.last, true
}

// ── Stored runs ───────────────────────────────────────────────────────────

// ghostStoreMu serialises the stored runs' reads and writes.
var ghostStoreMu sync.Mutex

// ghostDir is <data>/ghosts.
func ghostDir() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, "ghosts")
}

// ghostSharedDir is the Ghosts folder people can reach: on the card's top
// level on a handheld, in Documents on a PC.
func ghostSharedDir() string {
	if IsTrimuiDevice() {
		card := strings.TrimSpace(defaultTrimuiSDRoot())
		if card == "" {
			card = "/mnt/SDCARD"
		}
		return filepath.Join(card, "Ghosts")
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		if dirExists(filepath.Join(home, "Documents")) {
			return filepath.Join(home, "Documents", "JukaHub Ghosts")
		}
		return filepath.Join(home, "JukaHub Ghosts")
	}
	return ""
}

// ghostWrite writes a run as JSON.
func ghostWrite(path string, run ghostRun) error {
	data, err := json.Marshal(run)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return AtomicWrite(path, data, 0o644)
}

// ghostRead reads a run file.
func ghostRead(path string) (ghostRun, error) {
	st, err := os.Stat(path)
	if err != nil {
		return ghostRun{}, err
	}
	if !st.Mode().IsRegular() || st.Size() > ghostMaxFile {
		return ghostRun{}, errors.New("not a ghost run")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ghostRun{}, err
	}
	return ghostParseRun(data)
}

// ghostKeepBest keeps run as its game's best when it beats the one kept.
func ghostKeepBest(run ghostRun) {
	dir := ghostDir()
	if dir == "" {
		return
	}
	ghostStoreMu.Lock()
	defer ghostStoreMu.Unlock()
	path := filepath.Join(dir, "best-"+run.Game+".json")
	if old, err := ghostRead(path); err == nil && old.Score >= run.Score {
		return
	}
	if err := ghostWrite(path, run); err != nil {
		logWarn("ghost", "could not keep the best %s run: %v", run.Game, err)
	}
}

// ghostBestRuns are your best run of each game, in the games' order.
func ghostBestRuns() []ghostRun {
	dir := ghostDir()
	if dir == "" {
		return nil
	}
	ghostStoreMu.Lock()
	defer ghostStoreMu.Unlock()
	var out []ghostRun
	for _, g := range ghostGames {
		if run, err := ghostRead(filepath.Join(dir, "best-"+g.name+".json")); err == nil {
			out = append(out, run)
		}
	}
	return out
}

// ghostInboxDir is where friends' runs are kept.
func ghostInboxDir() string {
	if dir := ghostDir(); dir != "" {
		return filepath.Join(dir, "inbox")
	}
	return ""
}

// ghostInboxRuns are friends' runs, newest first.
func ghostInboxRuns() []ghostRun {
	dir := ghostInboxDir()
	if dir == "" {
		return nil
	}
	ghostStoreMu.Lock()
	defer ghostStoreMu.Unlock()
	entries, _ := os.ReadDir(dir)
	type kept struct {
		run ghostRun
		mod time.Time
	}
	var all []kept
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if run, err := ghostRead(filepath.Join(dir, e.Name())); err == nil {
			all = append(all, kept{run, info.ModTime()})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].mod.After(all[j].mod) })
	out := make([]ghostRun, 0, len(all))
	for _, k := range all {
		out = append(out, k.run)
	}
	return out
}

// ghostInboxAdd keeps a friend's run (the same id replaces it), and makes
// room by dropping the oldest. It reports whether the run was new.
func ghostInboxAdd(run ghostRun) (bool, error) {
	dir := ghostInboxDir()
	if dir == "" {
		return false, errors.New("no data folder")
	}
	ghostStoreMu.Lock()
	defer ghostStoreMu.Unlock()
	path := filepath.Join(dir, run.ID+".json")
	_, err := os.Stat(path)
	fresh := err != nil
	if err := ghostWrite(path, run); err != nil {
		return false, err
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) > ghostInboxMax {
		sort.Slice(entries, func(i, j int) bool {
			a, _ := entries[i].Info()
			b, _ := entries[j].Info()
			return a != nil && b != nil && a.ModTime().Before(b.ModTime())
		})
		for _, e := range entries[:len(entries)-ghostInboxMax] {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	return fresh, nil
}

// ghostInboxDelete forgets a friend's run.
func ghostInboxDelete(id string) {
	if id = ghostCleanID(id); id == "" {
		return
	}
	ghostStoreMu.Lock()
	defer ghostStoreMu.Unlock()
	_ = os.Remove(filepath.Join(ghostInboxDir(), id+".json"))
}

// ghostInboxFind is the friend's run with id.
func ghostInboxFind(id string) (ghostRun, bool) {
	if id = ghostCleanID(id); id == "" {
		return ghostRun{}, false
	}
	ghostStoreMu.Lock()
	defer ghostStoreMu.Unlock()
	run, err := ghostRead(filepath.Join(ghostInboxDir(), id+".json"))
	return run, err == nil
}

// ghostNoteAttempt records your score against a friend's run.
func ghostNoteAttempt(id string, score int) {
	run, ok := ghostInboxFind(id)
	if !ok {
		return
	}
	run.Tries++
	if score > run.Tried {
		run.Tried = score
	}
	if score > run.Score {
		run.Beat = true
	}
	ghostStoreMu.Lock()
	defer ghostStoreMu.Unlock()
	_ = ghostWrite(filepath.Join(ghostInboxDir(), run.ID+".json"), run)
}

// ghostImportShared takes the .jghost files in the Ghosts folder into the
// inbox (your own exports are skipped by id). It returns how many were new.
func ghostImportShared() int {
	dir := ghostSharedDir()
	if dir == "" {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	own := map[string]bool{}
	for _, b := range ghostBestRuns() {
		own[b.ID] = true
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ghostFileExt) {
			continue
		}
		run, err := ghostRead(filepath.Join(dir, e.Name()))
		if err != nil || own[run.ID] {
			continue
		}
		if _, ok := ghostInboxFind(run.ID); ok {
			continue
		}
		if fresh, err := ghostInboxAdd(ghostAsReceived(run)); err == nil && fresh {
			n++
		}
	}
	return n
}

// ghostAsReceived is a run as this side keeps it: no attempts yet.
func ghostAsReceived(run ghostRun) ghostRun {
	run.Tried, run.Beat, run.Tries = 0, false, 0
	return run
}

// ghostExport writes run into the Ghosts folder and returns the file.
func ghostExport(run ghostRun) (string, error) {
	dir := ghostSharedDir()
	if dir == "" {
		return "", errors.New("no folder to save it in")
	}
	run = ghostAsReceived(run)
	name := fmt.Sprintf("%s-%d-%s%s", run.Game, run.Score, safeDownloadName(setOr(run.Player, "run")), ghostFileExt)
	path := filepath.Join(dir, name)
	if err := ghostWrite(path, run); err != nil {
		return "", err
	}
	return path, nil
}

// ── Nearby devices ────────────────────────────────────────────────────────

// ghostSendToPeer sends run to a paired JukaHub.
func ghostSendToPeer(id string, run ghostRun) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return peerDoJSON(ctx, id, http.MethodPost, ghostPeerPath, ghostAsReceived(run), nil, 0)
}

// ghostHandlePeer takes a run from a paired JukaHub (peernet.go, paired
// only): checked, kept in the inbox, and announced with a Play in the bell.
func ghostHandlePeer(w http.ResponseWriter, r *http.Request, call peerCall) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST a run", http.StatusMethodNotAllowed)
		return
	}
	var raw json.RawMessage
	if !peerDecodeBody(w, r, ghostMaxFile, &raw) {
		return
	}
	run, err := ghostParseRun(raw)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	run = ghostAsReceived(run)
	if run.Player == "" {
		run.Player = ghostCleanName(call.rec.Name)
	}
	if _, err := ghostInboxAdd(run); err != nil {
		http.Error(w, "could not keep it", http.StatusInternalServerError)
		return
	}
	who := setOr(run.Player, "A friend")
	notifPost(notifSpec{
		Message:  fmt.Sprintf("%s challenges you to %s: beat %d", who, ghostGameTitle(run.Game), run.Score),
		Priority: notifInfo, Source: notifSrcChallenges, Toast: true,
		Action: notifAction{Label: "Play", Kind: notifActGhost, Arg: run.ID},
	})
	peerWriteJSONResponse(w, http.StatusOK, map[string]bool{"ok": true})
}

func init() {
	notifActionRegister(notifActGhost, func(c *Config, id string) bool {
		run, ok := ghostInboxFind(id)
		if !ok {
			ShowToast("That challenge is no longer here", ToastKindInfo)
			return false
		}
		ghostRace(c, run, ghostRunLabel(run))
		return true
	})
}

// ghostRunLabel is the name a run races under ("Ana's ghost").
func ghostRunLabel(run ghostRun) string {
	if run.Player == "" {
		return "Ghost"
	}
	return run.Player + "'s ghost"
}
