package main

// musicquiz.go — the music games that run inside the player (Extras › Games
// on the iPod screen): Music Quiz here, Sound Racer in musicracer.go. They
// draw over the whole player in the iPod theme's four shades and take its
// input while they run; B leaves them and stops their music.
//
// Music Quiz: ten rounds; each plays fifteen seconds from the middle of a
// random song of the library (the source the iPod screen browses) and offers
// four titles. A right answer scores 100 plus 10 for every second left. The
// best score is kept.

import (
	"context"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

type mgameMode int

const (
	mgameNone mgameMode = iota
	mgameQuiz
	mgameRacer
)

// mgame is the music game on screen. Main thread only.
var mgame struct {
	mode mgameMode
}

func mgameActive() bool { return mgame.mode != mgameNone }

// mgameQuit leaves the game and stops its music.
func mgameQuit() {
	if mgame.mode == mgameNone {
		return
	}
	mgame.mode = mgameNone
	musicStop()
	PlayBackSound()
}

// mgameKey routes a key to the game on screen.
func mgameKey(e *sdl.KeyboardEvent, config *Config) bool {
	if !mgameActive() {
		return false
	}
	if e == nil || e.Type != sdl.KEYDOWN {
		return true
	}
	switch mgame.mode {
	case mgameQuiz:
		musicQuizKey(e.Keysym.Sym, config)
	case mgameRacer:
		musicRacerKey(e.Keysym.Sym, config)
	}
	return true
}

// mgamePad turns a pad press into the game's key.
func mgamePad(e *sdl.ControllerButtonEvent, config *Config) bool {
	if !mgameActive() {
		return false
	}
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return true
	}
	var key sdl.Keycode
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		key = sdl.K_UP
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		key = sdl.K_DOWN
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		key = sdl.K_LEFT
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		key = sdl.K_RIGHT
	case sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_START:
		key = sdl.K_RETURN
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		key = sdl.K_ESCAPE
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		key = sdl.K_LEFT
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		key = sdl.K_RIGHT
	default:
		return true
	}
	return mgameKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: key}}, config)
}

// mgameClick handles a tap in the game.
func mgameClick(mx, my int32, config *Config) bool {
	switch mgame.mode {
	case mgameQuiz:
		for i, r := range quiz.rects {
			if r.W > 0 && pointInRect(mx, my, r) {
				quiz.cursor = i
				musicQuizKey(sdl.K_RETURN, config)
				return true
			}
		}
		if quiz.done {
			musicQuizKey(sdl.K_RETURN, config)
		}
		return true
	case mgameRacer:
		if mx < screenWidth/2 {
			musicRacerKey(sdl.K_LEFT, config)
		} else {
			musicRacerKey(sdl.K_RIGHT, config)
		}
		return true
	}
	return false
}

// renderMusicGames draws the game on screen and reports whether there was
// one.
func renderMusicGames(renderer *sdl.Renderer, config *Config) bool {
	switch mgame.mode {
	case mgameQuiz:
		brwClearChromeHits()
		renderMusicQuiz(renderer, config)
		return true
	case mgameRacer:
		brwClearChromeHits()
		renderMusicRacer(renderer, config)
		return true
	}
	return false
}

// mgamePool fetches songs for a game from the iPod screen's source: tracks of
// at least 30 seconds with a title.
func mgamePool(config *Config, then func([]musicItem, error)) {
	src := musicSourceFromConfig(config)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		items, err := musicFetch(ctx, src, musicQuery{What: "random"})
		var pool []musicItem
		for _, it := range items {
			if it.Kind == "track" && it.Path != "" && strings.TrimSpace(it.Title) != "" && (it.Duration == 0 || it.Duration >= 30) {
				pool = append(pool, it)
			}
		}
		tbxPost(func() { then(pool, err) })
	}()
}

// mgamePlay plays one item alone, from at seconds.
func mgamePlay(it musicItem, at float64) {
	musicMetaRemember(it)
	if !musicPlayPlaylist([]musicTrack{{Path: it.Path, Name: musicItemTrackName(it), Duration: it.Duration}}, 0) {
		return
	}
	musicPlayTrack(0)
	if at > 1 {
		musicSeek(at)
	}
}

// ──────────────────────────────────────────────────────────────────────
// Music Quiz
// ──────────────────────────────────────────────────────────────────────

const (
	quizRounds  = 10
	quizClip    = 15 * time.Second
	quizReveal  = 1800 * time.Millisecond
	quizBestKey = "musicQuizBest"
)

var quiz struct {
	pool    []musicItem
	used    map[string]bool
	round   int
	choices [4]musicItem
	answer  int
	cursor  int
	picked  int // -1 waiting, -2 out of time, else the choice
	started time.Time
	shownAt time.Time
	score   int
	correct int
	best    int
	loading bool
	err     string
	done    bool
	rects   [4]sdl.Rect
}

// musicQuizStart opens the quiz.
func musicQuizStart(config *Config) {
	openMusicPlayerScene(config)
	mgame.mode = mgameQuiz
	quiz.round, quiz.score, quiz.correct, quiz.done, quiz.err = 0, 0, 0, false, ""
	quiz.used = map[string]bool{}
	quiz.best, _ = strconv.Atoi(customStringValue(config.Variables.Custom, quizBestKey))
	quiz.loading = true
	mgamePool(config, func(pool []musicItem, err error) {
		quiz.loading = false
		titles := map[string]bool{}
		for _, it := range pool {
			titles[strings.ToLower(it.Title)] = true
		}
		if len(titles) < 4 {
			quiz.err = "Music Quiz needs at least four songs in your library"
			if err != nil {
				quiz.err = firstLine(err.Error())
			}
			return
		}
		quiz.pool = pool
		musicQuizNext(config)
	})
	PlayActivateSound()
}

// musicQuizNext starts the next round (or ends the game).
func musicQuizNext(config *Config) {
	if quiz.round >= quizRounds {
		quiz.done = true
		musicStop()
		if quiz.score > quiz.best {
			quiz.best = quiz.score
			if config.Variables.Custom == nil {
				config.Variables.Custom = map[string]interface{}{}
			}
			config.Variables.Custom[quizBestKey] = strconv.Itoa(quiz.best)
		}
		return
	}
	quiz.round++
	// The answer: a song not asked yet this game.
	var answer musicItem
	for tries := 0; tries < 50; tries++ {
		answer = quiz.pool[rand.Intn(len(quiz.pool))]
		if !quiz.used[answer.Path] {
			break
		}
	}
	quiz.used[answer.Path] = true
	choices := []musicItem{answer}
	seen := map[string]bool{strings.ToLower(answer.Title): true}
	for _, i := range rand.Perm(len(quiz.pool)) {
		it := quiz.pool[i]
		if seen[strings.ToLower(it.Title)] {
			continue
		}
		seen[strings.ToLower(it.Title)] = true
		choices = append(choices, it)
		if len(choices) == 4 {
			break
		}
	}
	rand.Shuffle(len(choices), func(i, j int) { choices[i], choices[j] = choices[j], choices[i] })
	for i := range quiz.choices {
		quiz.choices[i] = choices[i]
		if sameAudioPath(choices[i].Path, answer.Path) {
			quiz.answer = i
		}
	}
	quiz.cursor, quiz.picked = 0, -1
	quiz.started = time.Now()
	at := 30.0
	if answer.Duration > 40 {
		at = answer.Duration * (0.2 + 0.4*rand.Float64())
	}
	mgamePlay(answer, at)
}

// musicQuizAnswer settles the round.
func musicQuizAnswer(choice int) {
	if quiz.picked != -1 {
		return
	}
	quiz.picked = choice
	quiz.shownAt = time.Now()
	if choice == quiz.answer {
		left := quizClip - time.Since(quiz.started)
		if left < 0 {
			left = 0
		}
		quiz.score += 100 + int(left.Seconds())*10
		quiz.correct++
		PlaySuccessSound()
	} else {
		PlayErrorSound()
	}
}

func musicQuizKey(sym sdl.Keycode, config *Config) {
	switch sym {
	case sdl.K_ESCAPE, sdl.K_BACKSPACE:
		mgameQuit()
		return
	}
	if quiz.err != "" || quiz.loading {
		return
	}
	if quiz.done {
		if sym == sdl.K_RETURN || sym == sdl.K_SPACE {
			musicQuizStart(config)
		}
		return
	}
	switch sym {
	case sdl.K_UP:
		if quiz.cursor > 0 {
			quiz.cursor--
			musicPodTick(config)
		}
	case sdl.K_DOWN:
		if quiz.cursor < 3 {
			quiz.cursor++
			musicPodTick(config)
		}
	case sdl.K_1, sdl.K_2, sdl.K_3, sdl.K_4:
		musicQuizAnswer(int(sym - sdl.K_1))
	case sdl.K_RETURN, sdl.K_SPACE:
		if quiz.picked == -1 {
			musicQuizAnswer(quiz.cursor)
		} else {
			musicQuizNext(config)
		}
	}
}

// musicQuizTick ends a round that ran out of time and moves on after the
// answer has been shown.
func musicQuizTick(config *Config) {
	if quiz.done || quiz.loading || quiz.err != "" || quiz.round == 0 {
		return
	}
	if quiz.picked == -1 && time.Since(quiz.started) > quizClip {
		quiz.picked, quiz.shownAt = -2, time.Now()
		PlayErrorSound()
	}
	if quiz.picked != -1 && time.Since(quiz.shownAt) > quizReveal {
		musicQuizNext(config)
	}
}

func renderMusicQuiz(renderer *sdl.Renderer, config *Config) {
	musicQuizTick(config)
	col := musicPodColors(config)
	renderer.SetDrawColor(col.bg.R, col.bg.G, col.bg.B, 255)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	small, _ := getCachedFont(config, "small")
	med, _ := getCachedFont(config, "medium")
	big, _ := getCachedFont(config, "big")
	if small == nil || med == nil || big == nil {
		return
	}
	area := sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight}
	podCentered(renderer, config, big, "Music Quiz", col.ink, area, 28)
	y := 28 + int32(big.Height()) + 12
	quiz.rects = [4]sdl.Rect{}
	switch {
	case quiz.err != "":
		podWrapped(renderer, config, med, quiz.err, col.ink, area, screenHeight/3)
		podCentered(renderer, config, small, hintLine(FooterHint{"B", "Back"}), col.dim, area, screenHeight-int32(small.Height())-16)
		return
	case quiz.loading:
		renderSpinner(renderer, screenWidth/2, screenHeight/2, 18, col.ink)
		podCentered(renderer, config, small, "Picking songs…", col.dim, area, screenHeight/2+30)
		return
	case quiz.done:
		lines := []string{
			fmt.Sprintf("Score %d", quiz.score),
			fmt.Sprintf("%d of %d right", quiz.correct, quizRounds),
			fmt.Sprintf("Best %d", quiz.best),
		}
		ly := screenHeight / 3
		for i, l := range lines {
			font := med
			if i == 0 {
				font = big
			}
			podCentered(renderer, config, font, l, col.ink, area, ly)
			ly += int32(font.Height()) + 14
		}
		podCentered(renderer, config, small, hintLine(FooterHint{"A", "Play Again"}, FooterHint{"B", "Quit"}), col.dim,
			area, screenHeight-int32(small.Height())-16)
		return
	}
	podCentered(renderer, config, med, fmt.Sprintf("Round %d of %d   ·   Score %d", quiz.round, quizRounds, quiz.score), col.dim, area, y)
	y += int32(med.Height()) + 16
	// Time left.
	frac := 1 - float64(time.Since(quiz.started))/float64(quizClip)
	if quiz.picked != -1 {
		frac = 0
	}
	if frac < 0 {
		frac = 0
	}
	bar := sdl.Rect{X: screenWidth / 6, Y: y, W: screenWidth * 2 / 3, H: 12}
	podStroke(renderer, bar, col.ink)
	renderer.SetDrawColor(col.ink.R, col.ink.G, col.ink.B, 255)
	renderer.FillRect(&sdl.Rect{X: bar.X + 2, Y: bar.Y + 2, W: int32(float64(bar.W-4) * frac), H: bar.H - 4})
	y += 30
	question := "Which song is playing?"
	switch {
	case quiz.picked == -2:
		question = "Out of time!"
	case quiz.picked == quiz.answer:
		question = "Right!"
	case quiz.picked >= 0:
		question = "Not this time"
	}
	podCentered(renderer, config, med, question, col.ink, area, y)
	y += int32(med.Height()) + 20
	rowH := int32(med.Height()) + 22
	w := screenWidth * 3 / 4
	x := (screenWidth - w) / 2
	for i, c := range quiz.choices {
		r := sdl.Rect{X: x, Y: y + int32(i)*(rowH+10), W: w, H: rowH}
		quiz.rects[i] = r
		ink := col.ink
		revealed := quiz.picked != -1
		switch {
		case revealed && i == quiz.answer:
			renderer.SetDrawColor(col.ink.R, col.ink.G, col.ink.B, 255)
			renderer.FillRect(&r)
			ink = col.bg
		case revealed && i == quiz.picked:
			renderer.SetDrawColor(col.line.R, col.line.G, col.line.B, 255)
			renderer.FillRect(&r)
		case !revealed && i == quiz.cursor:
			renderer.SetDrawColor(col.selBg.R, col.selBg.G, col.selBg.B, 255)
			renderer.FillRect(&r)
			ink = col.selInk
		}
		podStroke(renderer, r, col.ink)
		label := c.Title
		if c.Artist != "" {
			label += " — " + c.Artist
		}
		label = fmt.Sprintf("%d.  %s", i+1, label)
		renderText(renderer, config, med, ellipsize(med, label, r.W-32), ink, r.X+16, r.Y+(r.H-int32(med.Height()))/2)
	}
	hint := hintLine(FooterHint{"Up/Down", "Choose"}, FooterHint{"A", "Answer"}, FooterHint{"B", "Quit"})
	podCentered(renderer, config, small, hint, col.dim, area, screenHeight-int32(small.Height())-16)
}
