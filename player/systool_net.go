package main

// systool_net.go — the Speed test screen (config scene "NetSpeed").
//
// The test runs by itself when the screen opens: latency (the median of a few
// round trips on a warm connection, with jitter), download (a stream read for
// up to eight seconds, drawn live as a line) and upload (a bounded POST).
// Offline it does not dial anything: it shows a compact "No internet
// connection" state and starts on its own as soon as the network is back. X
// (or Y) runs it again, X stops a run, and leaving the screen cancels it.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// sysToolNetDownBytes / sysToolNetDownTime bound the download: whichever
	// comes first ends it, so a fast line is not charged 25 MB for nothing and
	// a slow one is not kept waiting.
	sysToolNetDownBytes = 25000000
	sysToolNetDownTime  = 8 * time.Second
	// sysToolNetUpBytes / sysToolNetUpTime bound the upload the same way.
	sysToolNetUpBytes = 4 << 20
	sysToolNetUpTime  = 8 * time.Second
	// sysToolNetPings is how many round trips the latency takes; the first one
	// pays for the TLS handshake and is not counted.
	sysToolNetPings = 5
	// sysToolNetSampleEvery is how often the live line gets a point.
	sysToolNetSampleEvery = 250 * time.Millisecond
	// sysToolNetMinBytes is the least a phase must move to say anything.
	sysToolNetMinBytes = 256 << 10
)

// The endpoints are variables so tests can point them at a local server.
var (
	sysToolNetHTTP    = &http.Client{Timeout: 25 * time.Second}
	sysToolNetPingURL = "https://speed.cloudflare.com/__down?bytes=0"
	sysToolNetDownURL = "https://speed.cloudflare.com/__down?bytes=25000000"
	sysToolNetUpURL   = "https://speed.cloudflare.com/__up"
	sysToolNetServer  = "speed.cloudflare.com"
)

type sysToolNetPhase int

const (
	sysToolNetIdle sysToolNetPhase = iota
	sysToolNetOffline
	sysToolNetPing
	sysToolNetDown
	sysToolNetUp
	sysToolNetDone
	sysToolNetFailed
)

// sysToolNetData is one run's results, copied to the render thread whole.
type sysToolNetData struct {
	running  bool
	phase    sysToolNetPhase
	pingMS   float64
	jitterMS float64
	pingOK   bool
	downMbps float64
	downOK   bool
	upMbps   float64
	upOK     bool
	upFailed bool
	live     float64
	progress float64
	samples  sysToolRing
	err      string
	at       time.Time
}

type sysToolNetState struct {
	mu      sync.Mutex
	gen     uint64
	version uint64
	cancel  context.CancelFunc
	d       sysToolNetData
}

var sysToolNet sysToolNetState

// sysToolNetUpdate changes the published run, unless a newer run replaced it.
func sysToolNetUpdate(gen uint64, fn func(d *sysToolNetData)) bool {
	sysToolNet.mu.Lock()
	defer sysToolNet.mu.Unlock()
	if gen != sysToolNet.gen {
		return false
	}
	fn(&sysToolNet.d)
	sysToolNet.version++
	return true
}

// sysToolNetStart begins a run (on entry, or Run again). A run in progress is
// left alone.
func sysToolNetStart() {
	sysToolNet.mu.Lock()
	if sysToolNet.d.running {
		sysToolNet.mu.Unlock()
		return
	}
	sysToolNet.gen++
	gen := sysToolNet.gen
	sysToolNet.d = sysToolNetData{running: true, phase: sysToolNetPing}
	sysToolNet.version++
	sysToolNet.mu.Unlock()
	started := runSceneTask(func(ctx context.Context) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		sysToolNet.mu.Lock()
		if gen == sysToolNet.gen {
			sysToolNet.cancel = cancel
		}
		sysToolNet.mu.Unlock()
		sysToolNetRun(ctx, gen)
	})
	if !started {
		sysToolNetUpdate(gen, func(d *sysToolNetData) {
			d.running = false
			d.phase = sysToolNetIdle
		})
	}
}

// sysToolNetEnter starts this visit's test as the screen opens. A run from
// the previous visit was cancelled when that visit ended but may still be
// winding down; it is replaced rather than waited for, and its late updates
// carry an older generation, so they are dropped.
func sysToolNetEnter() {
	sysToolNet.mu.Lock()
	sysToolNet.d.running = false
	sysToolNet.cancel = nil
	sysToolNet.mu.Unlock()
	sysToolNetStart()
}

// sysToolNetStop ends the run in progress (X while testing).
func sysToolNetStop() bool {
	sysToolNet.mu.Lock()
	running, cancel := sysToolNet.d.running, sysToolNet.cancel
	sysToolNet.mu.Unlock()
	if !running || cancel == nil {
		return false
	}
	cancel()
	return true
}

// sysToolNetRun is one whole test. Run as a scene task.
func sysToolNetRun(ctx context.Context, gen uint64) {
	defer sysToolNetUpdate(gen, func(d *sysToolNetData) {
		d.running = false
		if ctx.Err() != nil && d.phase != sysToolNetDone && d.phase != sysToolNetFailed {
			d.phase = sysToolNetIdle
			d.err = "Stopped"
		}
	})
	// Offline: no dial errors, no timeouts — wait for the network instead.
	if !IsOnline() {
		sysToolNetUpdate(gen, func(d *sysToolNetData) { d.phase = sysToolNetOffline })
		for !IsOnline() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}

	sysToolNetUpdate(gen, func(d *sysToolNetData) { d.phase = sysToolNetPing; d.progress = 0 })
	ping, jitter, err := sysToolNetMeasurePing(ctx, gen)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		sysToolNetFail(gen, err)
		return
	}
	sysToolNetUpdate(gen, func(d *sysToolNetData) {
		d.pingMS, d.jitterMS, d.pingOK = ping, jitter, true
		d.phase, d.progress, d.live = sysToolNetDown, 0, 0
	})

	down, err := sysToolNetMeasureDown(ctx, gen)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		sysToolNetFail(gen, err)
		return
	}
	sysToolNetUpdate(gen, func(d *sysToolNetData) {
		d.downMbps, d.downOK = down, true
		d.phase, d.progress, d.live = sysToolNetUp, 0, 0
	})

	up, err := sysToolNetMeasureUp(ctx, gen)
	if ctx.Err() != nil {
		return
	}
	sysToolNetUpdate(gen, func(d *sysToolNetData) {
		if err != nil {
			d.upFailed = true
		} else {
			d.upMbps, d.upOK = up, true
		}
		d.phase, d.progress, d.live = sysToolNetDone, 1, 0
		d.at = time.Now()
	})
}

// sysToolNetFail ends the run with a sentence a person can act on.
func sysToolNetFail(gen uint64, err error) {
	sysToolNetUpdate(gen, func(d *sysToolNetData) {
		d.phase = sysToolNetFailed
		d.err = plainNetworkError(err)
		d.at = time.Now()
	})
}

// sysToolNetMeasurePing times a few small requests on one warm connection and
// returns the median round trip and the jitter (mean change between trips),
// in milliseconds.
func sysToolNetMeasurePing(ctx context.Context, gen uint64) (float64, float64, error) {
	var trips []float64
	for i := 0; i < sysToolNetPings; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, sysToolNetPingURL, nil)
		if err != nil {
			return 0, 0, err
		}
		start := time.Now()
		resp, err := sysToolNetHTTP.Do(req)
		if err != nil {
			return 0, 0, err
		}
		rtt := time.Since(start)
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return 0, 0, fmt.Errorf("the test server answered HTTP %d", resp.StatusCode)
		}
		if i > 0 {
			trips = append(trips, float64(rtt)/float64(time.Millisecond))
		}
		done := i + 1
		sysToolNetUpdate(gen, func(d *sysToolNetData) {
			d.progress = float64(done) / float64(sysToolNetPings)
			if len(trips) > 0 {
				d.live = trips[len(trips)-1]
			}
		})
	}
	return sysToolNetMedian(trips), sysToolNetJitter(trips), nil
}

// sysToolNetMedian is the middle value (0 for none).
func sysToolNetMedian(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	s := append([]float64(nil), vals...)
	sort.Float64s(s)
	mid := len(s) / 2
	if len(s)%2 == 0 {
		return (s[mid-1] + s[mid]) / 2
	}
	return s[mid]
}

// sysToolNetJitter is the mean absolute change between consecutive values.
func sysToolNetJitter(vals []float64) float64 {
	if len(vals) < 2 {
		return 0
	}
	sum := 0.0
	for i := 1; i < len(vals); i++ {
		d := vals[i] - vals[i-1]
		if d < 0 {
			d = -d
		}
		sum += d
	}
	return sum / float64(len(vals)-1)
}

// sysToolNetMeasureDown streams the download sample for up to
// sysToolNetDownTime and returns the average in Mbit/s, publishing the live
// rate as it goes.
func sysToolNetMeasureDown(ctx context.Context, gen uint64) (float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sysToolNetDownURL, nil)
	if err != nil {
		return 0, err
	}
	resp, err := sysToolNetHTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("the test server answered HTTP %d", resp.StatusCode)
	}
	buf := make([]byte, 64<<10)
	start := time.Now()
	lastTick, lastBytes, total := start, int64(0), int64(0)
	for {
		n, rerr := resp.Body.Read(buf)
		total += int64(n)
		now := time.Now()
		if span := now.Sub(lastTick); span >= sysToolNetSampleEvery {
			live := float64(total-lastBytes) * 8 / span.Seconds() / 1e6
			avg := float64(total) * 8 / now.Sub(start).Seconds() / 1e6
			frac := float64(now.Sub(start)) / float64(sysToolNetDownTime)
			if byBytes := float64(total) / float64(sysToolNetDownBytes); byBytes > frac {
				frac = byBytes
			}
			sysToolNetUpdate(gen, func(d *sysToolNetData) {
				d.live, d.downMbps, d.progress = avg, avg, frac
				d.samples.push(live)
			})
			lastTick, lastBytes = now, total
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			if total >= sysToolNetMinBytes {
				break
			}
			return 0, rerr
		}
		if total >= sysToolNetDownBytes || now.Sub(start) >= sysToolNetDownTime {
			break
		}
	}
	elapsed := time.Since(start).Seconds()
	if total < sysToolNetMinBytes || elapsed <= 0 {
		return 0, errors.New("the download ended too early to measure")
	}
	return float64(total) * 8 / elapsed / 1e6, nil
}

// sysToolNetUploadBody is the upload's request body: sysToolNetUpBytes of
// filler, counting what has been sent so the screen can draw it.
type sysToolNetUploadBody struct {
	left  int64
	sent  int64
	start time.Time
	last  time.Time
	gen   uint64
}

func (b *sysToolNetUploadBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > b.left {
		n = b.left
	}
	b.left -= n
	b.sent += n
	now := time.Now()
	if b.start.IsZero() {
		b.start, b.last = now, now
	}
	if now.Sub(b.last) >= sysToolNetSampleEvery {
		b.last = now
		avg := float64(b.sent) * 8 / now.Sub(b.start).Seconds() / 1e6
		frac := float64(b.sent) / float64(sysToolNetUpBytes)
		if byTime := float64(now.Sub(b.start)) / float64(sysToolNetUpTime); byTime > frac {
			frac = byTime
		}
		sysToolNetUpdate(b.gen, func(d *sysToolNetData) {
			d.live, d.progress = avg, frac
		})
	}
	return int(n), nil
}

// sysToolNetMeasureUp posts the upload sample and returns Mbit/s. A slow line
// is cut at sysToolNetUpTime and measured from what was sent by then.
func sysToolNetMeasureUp(ctx context.Context, gen uint64) (float64, error) {
	upCtx, cancel := context.WithTimeout(ctx, sysToolNetUpTime)
	defer cancel()
	body := &sysToolNetUploadBody{left: sysToolNetUpBytes, gen: gen}
	req, err := http.NewRequestWithContext(upCtx, http.MethodPost, sysToolNetUpURL, body)
	if err != nil {
		return 0, err
	}
	req.ContentLength = sysToolNetUpBytes
	req.Header.Set("Content-Type", "application/octet-stream")
	start := time.Now()
	resp, err := sysToolNetHTTP.Do(req)
	elapsed := time.Since(start).Seconds()
	if err != nil {
		if ctx.Err() == nil && body.sent >= sysToolNetMinBytes && elapsed > 0 {
			// Cut by the time limit: what was sent so far is the measure.
			return float64(body.sent) * 8 / elapsed / 1e6, nil
		}
		return 0, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return 0, fmt.Errorf("the test server answered HTTP %d", resp.StatusCode)
	}
	if body.sent < sysToolNetMinBytes || elapsed <= 0 {
		return 0, errors.New("the upload ended too early to measure")
	}
	return float64(body.sent) * 8 / elapsed / 1e6, nil
}

// sysToolNetRating says what a download speed is good for.
func sysToolNetRating(mbps float64) string {
	switch {
	case mbps < 3:
		return "Enough for music and web pages"
	case mbps < 10:
		return "Good for HD video"
	case mbps < 25:
		return "Great for HD streaming"
	case mbps < 100:
		return "Great for 4K streaming and big downloads"
	}
	return "Blazing fast"
}

// sysToolNetMbps formats a rate with sensible precision.
func sysToolNetMbps(v float64) string {
	switch {
	case v >= 100:
		return fmt.Sprintf("%.0f", v)
	case v >= 10:
		return fmt.Sprintf("%.1f", v)
	}
	return fmt.Sprintf("%.2f", v)
}

// sysToolNetView is the published run plus its formatted strings, rebuilt only
// when the run changes.
var sysToolNetView struct {
	version  uint64
	built    bool
	d        sysToolNetData
	caption  string
	big      string
	unit     string
	line     string
	pingVal  string
	pingSub  string
	downVal  string
	downSub  string
	upVal    string
	upSub    string
	status   string
	runRect  sdl.Rect
	wifiRect sdl.Rect
	scratch  [sysToolRingSize]float64
	// wrapped is line broken to the card's width, kept until either changes.
	wrapped     []string
	wrappedLine string
	wrappedW    int32
}

func sysToolNetSync() {
	sysToolNet.mu.Lock()
	if sysToolNetView.built && sysToolNetView.version == sysToolNet.version {
		sysToolNet.mu.Unlock()
		return
	}
	sysToolNetView.version = sysToolNet.version
	sysToolNetView.d = sysToolNet.d
	sysToolNet.mu.Unlock()
	sysToolNetView.built = true
	d := &sysToolNetView.d
	v := &sysToolNetView

	v.pingVal, v.pingSub = "—", "Waiting"
	if d.pingOK {
		v.pingVal = fmt.Sprintf("%.0f", d.pingMS)
		v.pingSub = fmt.Sprintf("Jitter %.0f ms", d.jitterMS)
	} else if d.phase == sysToolNetPing {
		v.pingSub = "Measuring…"
	}
	v.downVal, v.downSub = "—", "Waiting"
	if d.downOK {
		v.downVal = sysToolNetMbps(d.downMbps)
		v.downSub = sysToolNetRating(d.downMbps)
	} else if d.phase == sysToolNetDown {
		v.downVal = sysToolNetMbps(d.live)
		v.downSub = "Measuring…"
	}
	v.upVal, v.upSub = "—", "Waiting"
	switch {
	case d.upOK:
		v.upVal = sysToolNetMbps(d.upMbps)
		v.upSub = "Upload"
	case d.upFailed:
		v.upSub = "The server did not take the upload"
	case d.phase == sysToolNetUp:
		v.upVal = sysToolNetMbps(d.live)
		v.upSub = "Measuring…"
	}

	v.unit = "Mbit/s"
	switch d.phase {
	case sysToolNetPing:
		v.caption, v.unit = "Latency", "ms"
		v.big = "…"
		if d.live > 0 {
			v.big = fmt.Sprintf("%.0f", d.live)
		}
		v.line = "Timing round trips to " + sysToolNetServer
		v.status = "Testing…"
	case sysToolNetDown:
		v.caption = "Download"
		v.big = sysToolNetMbps(d.live)
		v.line = "Downloading a test file from " + sysToolNetServer
		v.status = "Testing…"
	case sysToolNetUp:
		v.caption = "Upload"
		v.big = sysToolNetMbps(d.live)
		v.line = "Sending test data to " + sysToolNetServer
		v.status = "Testing…"
	case sysToolNetDone:
		v.caption = "Download speed"
		v.big = sysToolNetMbps(d.downMbps)
		v.line = sysToolNetRating(d.downMbps)
		v.status = "Tested " + d.at.Format("15:04")
	case sysToolNetFailed:
		v.caption, v.big, v.unit = "Speed test", "—", ""
		v.line = d.err
		v.status = "Failed"
	case sysToolNetOffline:
		v.status = "Offline"
	default:
		v.caption, v.big, v.unit = "Speed test", "—", ""
		v.line = "Press X to run the test"
		if d.err != "" {
			v.line = d.err + " — press X to run it again"
		}
		v.status = ""
	}
}

// sysToolNetFooterHints is the Speed test's footer row.
func sysToolNetFooterHints() []FooterHint {
	sysToolNet.mu.Lock()
	phase, running := sysToolNet.d.phase, sysToolNet.d.running
	sysToolNet.mu.Unlock()
	switch {
	case phase == sysToolNetOffline:
		return []FooterHint{{Button: "A", Label: "Wi-Fi"}, {Button: "B", Label: "Back"}}
	case running:
		return []FooterHint{{Button: "B", Label: "Back"}, {Button: "X", Label: "Stop"}}
	}
	return []FooterHint{{Button: "B", Label: "Back"}, {Button: "X", Label: "Run again"}}
}

func renderSysToolNet(renderer *sdl.Renderer, config *Config) {
	sysToolNetSync()
	v := &sysToolNetView
	statusColor := ColorTextTertiary()
	if v.d.running {
		statusColor = glassAccent()
	}
	area := sysToolDrawPage(renderer, config, "NetSpeed", sysToolPageSpec{
		Title:       "Speed test",
		Subtitle:    "Download, upload and latency to the nearest Cloudflare server",
		Icon:        "speed",
		Status:      v.status,
		StatusColor: statusColor,
	})
	f, ok := sysToolLoadFonts(config)
	if !ok {
		sysToolFinishPage(renderer, config)
		return
	}
	v.runRect, v.wifiRect = sdl.Rect{}, sdl.Rect{}

	if v.d.phase == sysToolNetOffline {
		UIEmptyState(renderer, config, sdl.Rect{X: area.X, Y: area.Y, W: area.W, H: area.H - 70}, "@wifi-off",
			"No internet connection", "Connect to Wi-Fi — the test starts by itself when you are online.")
		label := "Wi-Fi settings"
		w := sysToolActionWidth(f, "A", label)
		v.wifiRect = sysToolAction(renderer, f, area.X+(area.W-w)/2, area.Y+area.H-56, "A", label, true)
		sysToolFinishPage(renderer, config)
		return
	}

	actionH := int32(f.body.Height()) + 22
	cardsH := area.H - actionH - 16
	leftW := area.W * 58 / 100
	gap := int32(16)
	left := sdl.Rect{X: area.X, Y: area.Y, W: leftW, H: cardsH}
	sysToolCard(renderer, left, v.d.running)

	// The headline: what is being measured, its number, what it means.
	pad := int32(22)
	x, y := left.X+pad, left.Y+pad
	drawText(renderer, f.label, v.caption, x, y, ColorTextTertiary(), textAlignLeft)
	y += int32(f.label.Height()) + 4
	bigInk := ColorTextPrimary()
	if v.d.phase == sysToolNetFailed || v.d.phase == sysToolNetIdle {
		bigInk = ColorTextTertiary()
	}
	bw, bh := measureText(f.big, v.big)
	drawText(renderer, f.big, v.big, x, y, bigInk, textAlignLeft)
	if v.unit != "" {
		_, uh := measureText(f.body, v.unit)
		drawText(renderer, f.body, v.unit, x+bw+10, y+bh-uh-10, ColorTextSecondary(), textAlignLeft)
	}
	y += bh + 2
	lineInk := ColorTextSecondary()
	if v.d.phase == sysToolNetFailed {
		lineInk = DangerInk()
	}
	if v.wrapped == nil || v.wrappedLine != v.line || v.wrappedW != left.W-2*pad {
		v.wrapped = wrapTextLines(f.body, v.line, left.W-2*pad, 2)
		v.wrappedLine, v.wrappedW = v.line, left.W-2*pad
	}
	for _, l := range v.wrapped {
		drawText(renderer, f.body, l, x, y, lineInk, textAlignLeft)
		y += int32(f.body.Height()) + 2
	}
	// The live rate as a line, under the text.
	chartTop := y + 14
	chartBottom := left.Y + left.H - pad - 14
	if chartBottom-chartTop > 30 {
		samples := v.d.samples.ordered(&v.scratch)
		top := v.d.samples.peak() * 1.15
		if top < 1 {
			top = 1
		}
		sysToolSparkline(renderer, sdl.Rect{X: x, Y: chartTop, W: left.W - 2*pad, H: chartBottom - chartTop}, samples, sysToolRingSize, top, glassAccent())
	}
	if v.d.running {
		UIProgressBar(renderer, sdl.Rect{X: x, Y: left.Y + left.H - pad - 6, W: left.W - 2*pad, H: 6}, v.d.progress, glassA(18), glassAccent())
	}

	// The three results.
	rightX := left.X + left.W + gap
	rightW := area.X + area.W - rightX
	tileH := (cardsH - 2*12) / 3
	tiles := []sysToolStat{
		{Label: "Ping", Icon: "clock", Value: v.pingVal, Unit: "ms", Sub: v.pingSub, Frac: -1,
			Active: v.d.phase == sysToolNetPing, Dim: !v.d.pingOK},
		{Label: "Download", Icon: "download", Value: v.downVal, Unit: "Mbit/s", Sub: v.downSub, Frac: -1,
			Active: v.d.phase == sysToolNetDown, Dim: !v.d.downOK && v.d.phase != sysToolNetDown},
		{Label: "Upload", Icon: "upload", Value: v.upVal, Unit: "Mbit/s", Sub: v.upSub, Frac: -1,
			Active: v.d.phase == sysToolNetUp, Dim: !v.d.upOK && v.d.phase != sysToolNetUp},
	}
	for i, t := range tiles {
		sysToolDrawStat(renderer, f, sdl.Rect{X: rightX, Y: area.Y + int32(i)*(tileH+12), W: rightW, H: tileH}, t)
	}

	// Run again (or Stop), and where the numbers come from.
	label := "Run again"
	if v.d.running {
		label = "Stop"
	}
	ay := area.Y + area.H - actionH
	v.runRect = sysToolAction(renderer, f, area.X, ay, "X", label, !v.d.running)
	note := "Server: " + sysToolNetServer
	nw, nh := measureText(f.label, note)
	drawText(renderer, f.label, note, area.X+area.W-nw, ay+(actionH-nh)/2, ColorTextTertiary(), textAlignLeft)

	sysToolFinishPage(renderer, config)
}

// sysToolNetAgain is X / Y / the button: run again, or stop a run.
func sysToolNetAgain() {
	if sysToolNetStop() {
		PlayBackSound()
		return
	}
	PlayActivateSound()
	sysToolNetStart()
}

func sysToolNetPad(e *sdl.ControllerButtonEvent, config *Config) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		goBackScene(config)
	case sdl.CONTROLLER_BUTTON_X, sdl.CONTROLLER_BUTTON_Y:
		sysToolNetAgain()
	case sdl.CONTROLLER_BUTTON_A:
		if sysToolNetView.d.phase == sysToolNetOffline {
			PlayActivateSound()
			wifiScrOpen(config)
		}
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		return focusChromeBar()
	default:
		return false
	}
	return true
}

func sysToolNetKey(e *sdl.KeyboardEvent, config *Config) bool {
	switch e.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		goBackScene(config)
	case sdl.K_x, sdl.K_y, sdl.K_F5, sdl.K_r:
		sysToolNetAgain()
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE, sdl.K_a:
		if sysToolNetView.d.phase == sysToolNetOffline {
			PlayActivateSound()
			wifiScrOpen(config)
		} else {
			sysToolNetAgain()
		}
	case sdl.K_UP:
		return focusChromeBar()
	default:
		return false
	}
	return true
}

func sysToolNetClick(mx, my int32, config *Config) bool {
	switch {
	case sysToolNetView.runRect.W > 0 && pointInRect(mx, my, sysToolNetView.runRect):
		sysToolNetAgain()
		return true
	case sysToolNetView.wifiRect.W > 0 && pointInRect(mx, my, sysToolNetView.wifiRect):
		PlayActivateSound()
		wifiScrOpen(config)
		return true
	}
	return false
}

// sysToolNetScene is the registered module for the "NetSpeed" scene.
type sysToolNetScene struct{ sceneBase }

func (sysToolNetScene) render(r *sdl.Renderer, c *Config) bool { renderSysToolNet(r, c); return true }

func (sysToolNetScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	return sysToolNetKey(e, c)
}

func (sysToolNetScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return sysToolNetPad(e, c)
}

func init() { registerScene(sysToolNetScene{sceneBase{name: "NetSpeed"}}) }
