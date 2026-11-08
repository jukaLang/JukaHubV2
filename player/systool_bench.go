package main

// systool_bench.go — the Benchmark screen (config scene "Benchmark").
//
// It measures the storage JukaHub runs from as soon as it opens: sequential
// write and read (the shape of copying a big file) and random 4 KB write and
// read (the shape of many small files). The measuring itself is runBenchmark
// (morefeatures.go), which reports its steps and results here through the
// sysToolBench hooks; this file only keeps them and draws them. X runs it
// again; leaving the screen cancels a run and removes its scratch file.

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// sysToolBenchResult is one finished run.
type sysToolBenchResult struct {
	seq, rnd     benchOutcome
	seqOK, rndOK bool
	at           time.Time
}

// sysToolBenchData is the run the screen shows.
type sysToolBenchData struct {
	running   bool
	step      string
	stepIndex int
	stepFrac  float64
	seq, rnd  benchOutcome
	seqOK     bool
	rndOK     bool
	seqErr    string
	rndErr    string
	at        time.Time
	cancelled bool
	// prev is the last finished run, shown faintly while a new one measures.
	prev sysToolBenchResult
}

type sysToolBenchState struct {
	mu      sync.Mutex
	version uint64
	run     uint64 // the latest run to begin
	d       sysToolBenchData
}

var sysToolBench sysToolBenchState

// sysToolBenchVisit counts the screen's visits, and sysToolBenchServed is the
// visit the latest run began in. A visit that opens while the previous
// visit's cancelled run is still winding down starts its own run as soon as
// that one has stopped (renderSysToolBench).
var sysToolBenchVisit, sysToolBenchServed atomic.Int64

// sysToolBenchSteps are the four measurements, in the order they run.
var sysToolBenchSteps = [...]string{"Sequential write", "Sequential read", "Random write", "Random read"}

func sysToolBenchUpdate(fn func(d *sysToolBenchData)) {
	sysToolBench.mu.Lock()
	fn(&sysToolBench.d)
	sysToolBench.version++
	sysToolBench.mu.Unlock()
}

// sysToolBenchEnter measures as the screen opens (sysToolSceneEnter).
func sysToolBenchEnter(config *Config) {
	sysToolBenchVisit.Add(1)
	runBenchmark(config) // a no-op while another run is still active
}

// sysToolBenchBegin is called by runBenchmark when a run starts. It returns
// the run's number, which the run hands back to sysToolBenchEnd.
func sysToolBenchBegin() uint64 {
	sysToolBenchServed.Store(sysToolBenchVisit.Load())
	var run uint64
	sysToolBenchUpdate(func(d *sysToolBenchData) {
		sysToolBench.run++
		run = sysToolBench.run
		if d.seqOK || d.rndOK {
			d.prev = sysToolBenchResult{seq: d.seq, rnd: d.rnd, seqOK: d.seqOK, rndOK: d.rndOK, at: d.at}
		}
		prev := d.prev
		*d = sysToolBenchData{running: true, step: "Preparing", prev: prev}
	})
	return run
}

// sysToolBenchStep is runBenchmark's progress: which measurement, how far.
func sysToolBenchStep(p benchProgress) {
	idx := 0
	for i, s := range sysToolBenchSteps {
		if s == p.label {
			idx = i
		}
	}
	frac := 1.0
	if p.total > 0 {
		frac = float64(p.done) / float64(p.total)
	}
	if frac > 1 {
		frac = 1
	}
	if frac < 0 {
		frac = 0
	}
	sysToolBenchUpdate(func(d *sysToolBenchData) {
		d.step, d.stepIndex, d.stepFrac = p.label, idx, frac
	})
}

// sysToolBenchPhase is one phase's result (sequential or random).
func sysToolBenchPhase(sequential bool, out benchOutcome, err error) {
	sysToolBenchUpdate(func(d *sysToolBenchData) {
		switch {
		case sequential && err != nil:
			d.seqErr = firstLine(err.Error())
		case sequential:
			d.seq, d.seqOK = out, true
		case err != nil:
			d.rndErr = firstLine(err.Error())
		default:
			d.rnd, d.rndOK = out, true
		}
	})
}

// sysToolBenchEnd is called by runBenchmark when run is over. The end of a
// run that a newer one has already replaced is not news.
func sysToolBenchEnd(run uint64, cancelled bool) {
	sysToolBenchUpdate(func(d *sysToolBenchData) {
		if run != sysToolBench.run {
			return
		}
		d.running = false
		d.cancelled = cancelled
		if !cancelled {
			d.at = time.Now()
		}
	})
}

// sysToolBenchView is the published run with its strings, rebuilt when it
// changes.
var sysToolBenchView struct {
	version  uint64
	built    bool
	d        sysToolBenchData
	values   [4]string // seq write, seq read, rnd write, rnd read
	subs     [4]string
	fracs    [4]float64
	verdicts [2]string
	stepText string
	status   string
	where    string
	runRect  sdl.Rect
}

// sysToolBenchVerdict says what a read speed means for using the device.
func sysToolBenchVerdict(sequential bool, read float64) string {
	if sequential {
		switch {
		case read >= 80:
			return "Fast storage — big games and videos load quickly"
		case read >= 40:
			return "Good for games and video"
		case read >= 15:
			return "Average — large games take a moment to load"
		}
		return "Slow — consider a faster card"
	}
	switch {
	case read >= 8:
		return "Great with many small files"
	case read >= 3:
		return "Fine with many small files"
	}
	return "Slow with many small files (saves, thumbnails)"
}

func sysToolBenchSync() {
	sysToolBench.mu.Lock()
	if sysToolBenchView.built && sysToolBenchView.version == sysToolBench.version {
		sysToolBench.mu.Unlock()
		return
	}
	sysToolBenchView.version = sysToolBench.version
	sysToolBenchView.d = sysToolBench.d
	sysToolBench.mu.Unlock()
	v := &sysToolBenchView
	v.built = true
	d := &v.d
	if v.where == "" {
		if wd, err := os.Getwd(); err == nil {
			v.where = termShortPath(wd, 48)
		}
	}

	type metric struct {
		ok   bool
		val  float64
		prev float64
		have bool
		ref  float64
		err  string
	}
	metrics := [4]metric{
		{d.seqOK, d.seq.write, d.prev.seq.write, d.prev.seqOK, 100, d.seqErr},
		{d.seqOK, d.seq.read, d.prev.seq.read, d.prev.seqOK, 100, d.seqErr},
		{d.rndOK, d.rnd.write, d.prev.rnd.write, d.prev.rndOK, 10, d.rndErr},
		{d.rndOK, d.rnd.read, d.prev.rnd.read, d.prev.rndOK, 10, d.rndErr},
	}
	for i, m := range metrics {
		switch {
		case m.ok:
			v.values[i] = fmt.Sprintf("%.1f", m.val)
			v.fracs[i] = m.val / m.ref
			v.subs[i] = "MB/s"
		case m.err != "":
			v.values[i], v.fracs[i], v.subs[i] = "—", 0, m.err
		case d.running && d.stepIndex == i:
			v.values[i], v.fracs[i], v.subs[i] = "…", d.stepFrac, "Measuring"
		default:
			v.values[i], v.fracs[i], v.subs[i] = "—", 0, "Waiting"
		}
		if !m.ok && m.have && m.err == "" {
			v.subs[i] = fmt.Sprintf("Last time %.1f MB/s", m.prev)
		}
		if v.fracs[i] > 1 {
			v.fracs[i] = 1
		}
	}
	v.verdicts = [2]string{"", ""}
	if d.seqOK {
		v.verdicts[0] = sysToolBenchVerdict(true, d.seq.read)
	}
	if d.rndOK {
		v.verdicts[1] = sysToolBenchVerdict(false, d.rnd.read)
	}
	switch {
	case d.running:
		v.status = "Measuring…"
		v.stepText = fmt.Sprintf("%s · %d%%", d.step, int(d.stepFrac*100))
	case d.cancelled:
		v.status = "Stopped"
		v.stepText = ""
	case !d.at.IsZero():
		v.status = "Measured " + d.at.Format("15:04")
		v.stepText = ""
	default:
		v.status, v.stepText = "", ""
	}
}

// sysToolBenchFooterHints is the Benchmark's footer row.
func sysToolBenchFooterHints() []FooterHint {
	if benchRunActive.Load() {
		return []FooterHint{{Button: "B", Label: "Back"}}
	}
	return []FooterHint{{Button: "B", Label: "Back"}, {Button: "X", Label: "Run again"}}
}

func renderSysToolBench(renderer *sdl.Renderer, config *Config) {
	// This visit has not measured yet because the last visit's cancelled run
	// was still stopping as the screen opened: measure now that it has.
	if sysToolBenchServed.Load() != sysToolBenchVisit.Load() && !benchRunActive.Load() {
		runBenchmark(config)
	}
	sysToolBenchSync()
	v := &sysToolBenchView
	statusColor := ColorTextTertiary()
	if v.d.running {
		statusColor = glassAccent()
	}
	area := sysToolDrawPage(renderer, config, "Benchmark", sysToolPageSpec{
		Title:       "Benchmark",
		Subtitle:    "Read and write speed of the storage JukaHub runs from",
		Icon:        "benchmark",
		Status:      v.status,
		StatusColor: statusColor,
	})
	f, ok := sysToolLoadFonts(config)
	if !ok {
		sysToolFinishPage(renderer, config)
		return
	}
	v.runRect = sdl.Rect{}
	gap := int32(16)
	actionH := int32(f.body.Height()) + 22

	// The progress strip: the four steps, the current one lit.
	stripH := int32(f.body.Height()) + 26
	strip := sdl.Rect{X: area.X, Y: area.Y, W: area.W, H: stripH}
	sysToolCard(renderer, strip, v.d.running)
	stepW := (strip.W - 2*12) / 4
	for i, name := range sysToolBenchSteps {
		cell := sdl.Rect{X: strip.X + 12 + int32(i)*stepW, Y: strip.Y, W: stepW, H: strip.H}
		done := (i < 2 && v.d.seqOK) || (i >= 2 && v.d.rndOK) || (v.d.running && i < v.d.stepIndex)
		current := v.d.running && i == v.d.stepIndex
		ink := ColorTextTertiary()
		switch {
		case current:
			ink = ColorTextPrimary()
		case done:
			ink = ColorTextSecondary()
		}
		icon := int32(18)
		iy := cell.Y + (cell.H-icon)/2
		switch {
		case done:
			DrawHomeIcon(renderer, "check", sdl.Rect{X: cell.X + 8, Y: iy, W: icon, H: icon}, false)
		case current:
			fillCircle(renderer, cell.X+8+icon/2, iy+icon/2, 5, glassAccent())
		default:
			fillCircle(renderer, cell.X+8+icon/2, iy+icon/2, 4, glassA(40))
		}
		label := name
		if current && v.stepText != "" {
			label = v.stepText
		}
		_, lh := measureText(f.body, label)
		drawText(renderer, f.body, ellipsize(f.body, label, cell.W-icon-24), cell.X+8+icon+8, cell.Y+(cell.H-lh)/2, ink, textAlignLeft)
	}
	if v.d.running {
		overall := (float64(v.d.stepIndex) + v.d.stepFrac) / 4
		UIProgressBar(renderer, sdl.Rect{X: strip.X + 16, Y: strip.Y + strip.H - 5, W: strip.W - 32, H: 3}, overall, sdl.Color{}, glassAccent())
	}

	// Two cards: sequential and random, each with write and read.
	cardsY := strip.Y + strip.H + gap
	cardsH := area.Y + area.H - actionH - gap - cardsY
	cardW := (area.W - gap) / 2
	titles := [2]string{"Sequential", "Random 4 KB"}
	hints := [2]string{"Copying one big file", "Many small files at once"}
	for c := 0; c < 2; c++ {
		card := sdl.Rect{X: area.X + int32(c)*(cardW+gap), Y: cardsY, W: cardW, H: cardsH}
		active := v.d.running && v.d.stepIndex/2 == c
		sysToolCard(renderer, card, active)
		pad := int32(20)
		x, y := card.X+pad, card.Y+pad
		drawText(renderer, f.value, titles[c], x, y, ColorTextPrimary(), textAlignLeft)
		y += int32(f.value.Height())
		drawText(renderer, f.label, hints[c], x, y, ColorTextTertiary(), textAlignLeft)
		y += int32(f.label.Height()) + 14
		inner := card.W - 2*pad
		rowH := (card.Y + card.H - pad - int32(f.body.Height()) - 10 - y) / 2
		for r := 0; r < 2; r++ {
			i := c*2 + r
			ry := y + int32(r)*rowH
			name := "Write"
			if r == 1 {
				name = "Read"
			}
			drawText(renderer, f.label, name, x, ry, ColorTextTertiary(), textAlignLeft)
			ry += int32(f.label.Height()) + 2
			ink := ColorTextPrimary()
			if v.values[i] == "—" || v.values[i] == "…" {
				ink = ColorTextTertiary()
			}
			vw, vh := measureText(f.value, v.values[i])
			drawText(renderer, f.value, v.values[i], x, ry, ink, textAlignLeft)
			subX := x + vw + 10
			_, sh := measureText(f.body, v.subs[i])
			drawText(renderer, f.body, ellipsize(f.body, v.subs[i], inner-vw-10), subX, ry+vh-sh-4, ColorTextSecondary(), textAlignLeft)
			ry += vh + 6
			col := glassAccent()
			if v.values[i] != "…" && v.fracs[i] > 0 {
				col = sysToolHeatColor(1 - v.fracs[i])
			}
			UIProgressBar(renderer, sdl.Rect{X: x, Y: ry, W: inner, H: 6}, v.fracs[i], glassA(18), col)
		}
		if v.verdicts[c] != "" {
			drawText(renderer, f.body, ellipsize(f.body, v.verdicts[c], inner), x, card.Y+card.H-pad-int32(f.body.Height()), ColorTextSecondary(), textAlignLeft)
		}
	}

	// Run again, and what a run costs.
	ay := area.Y + area.H - actionH
	if !v.d.running {
		v.runRect = sysToolAction(renderer, f, area.X, ay, "X", "Run again", true)
	}
	note := "Writes about 60 MB of scratch data and removes it afterwards"
	if v.where != "" {
		note = "Measured in " + v.where
	}
	nw, nh := measureText(f.label, note)
	if maxW := area.W - v.runRect.W - 24; nw > maxW {
		note = ellipsize(f.label, note, maxW)
		nw, nh = measureText(f.label, note)
	}
	drawText(renderer, f.label, note, area.X+area.W-nw, ay+(actionH-nh)/2, ColorTextTertiary(), textAlignLeft)

	sysToolFinishPage(renderer, config)
}

// sysToolBenchAgain is X / Y / the button.
func sysToolBenchAgain(config *Config) {
	if benchRunActive.Load() {
		return
	}
	PlayActivateSound()
	runBenchmark(config)
}

func sysToolBenchPad(e *sdl.ControllerButtonEvent, config *Config) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		goBackScene(config)
	case sdl.CONTROLLER_BUTTON_X, sdl.CONTROLLER_BUTTON_Y:
		sysToolBenchAgain(config)
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		return focusChromeBar()
	default:
		return false
	}
	return true
}

func sysToolBenchKey(e *sdl.KeyboardEvent, config *Config) bool {
	switch e.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		goBackScene(config)
	case sdl.K_x, sdl.K_y, sdl.K_F5, sdl.K_r, sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		sysToolBenchAgain(config)
	case sdl.K_UP:
		return focusChromeBar()
	default:
		return false
	}
	return true
}

func sysToolBenchClick(mx, my int32, config *Config) bool {
	if sysToolBenchView.runRect.W > 0 && pointInRect(mx, my, sysToolBenchView.runRect) {
		sysToolBenchAgain(config)
		return true
	}
	return false
}

// sysToolBenchScene is the registered module for the "Benchmark" scene.
type sysToolBenchScene struct{ sceneBase }

func (sysToolBenchScene) render(r *sdl.Renderer, c *Config) bool { renderSysToolBench(r, c); return true }

func (sysToolBenchScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	return sysToolBenchKey(e, c)
}

func (sysToolBenchScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return sysToolBenchPad(e, c)
}

func init() { registerScene(sysToolBenchScene{sceneBase{name: "Benchmark"}}) }
