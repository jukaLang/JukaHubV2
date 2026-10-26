package main

import (
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Screensaver: constellation particles + rotating quotes
// ──────────────────────────────────────────────────────────────────────

// ssChatNote is one message that arrived while the device was idle. The
// screensaver shows the newest few, so a chat answer is visible without the
// user having to wake the panel first.
type ssChatNote struct {
	sender string
	text   string
	at     time.Time
}

var ssChatFeed = struct {
	mu    sync.Mutex
	notes []ssChatNote
}{}

const (
	ssChatFeedLimit = 8
	ssChatNoteTTL   = 10 * time.Minute
	ssChatShown     = 3
)

// noteScreensaverChat records an incoming message for the idle screen. It is
// deliberately cheap and callable from the Discord goroutine.
func noteScreensaverChat(sender, text string) {
	sender = strings.TrimSpace(sender)
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	ssChatFeed.mu.Lock()
	defer ssChatFeed.mu.Unlock()
	ssChatFeed.notes = append(ssChatFeed.notes, ssChatNote{sender: sender, text: text, at: time.Now()})
	if len(ssChatFeed.notes) > ssChatFeedLimit {
		ssChatFeed.notes = ssChatFeed.notes[len(ssChatFeed.notes)-ssChatFeedLimit:]
	}
}

// screensaverChatNotes returns the newest notes, oldest first, dropping ones
// that have aged out so yesterday's conversation does not reappear.
func screensaverChatNotes(now time.Time, limit int) []ssChatNote {
	if limit <= 0 {
		return nil
	}
	ssChatFeed.mu.Lock()
	defer ssChatFeed.mu.Unlock()
	var recent []ssChatNote
	for _, note := range ssChatFeed.notes {
		if now.Sub(note.at) <= ssChatNoteTTL {
			recent = append(recent, note)
		}
	}
	if len(recent) > limit {
		recent = recent[len(recent)-limit:]
	}
	return recent
}

// screensaverIdleTimeout is how long the device waits without input before the
// starfield appears. 25 seconds is the shipped default; the settings row and
// Quick Settings both cycle this value at runtime.
var screensaverIdleTimeout = 25 * time.Second

const (
	ssStarCount          = 120
	ssConstellationCount = 8
	ssQuoteInterval      = 12 * time.Second
	ssFadeInDuration     = 1500
)

var screensaverQuotes = []string{
	"The best interface is no interface.",
	"Good design is as little design as possible.",
	"Simplicity is the ultimate sophistication.",
	"The details are not the details. They make the design.",
	"Design is how it works.",
	"Perfection is achieved when there is nothing left to take away.",
	"Your device. Your way.",
	"Code is poetry.",
	"Ship it.",
	"Make it work, make it right, make it fast.",
	"Talk is cheap. Show me the code.",
	"Progress, not perfection.",
}

type ssStar struct {
	x, y    float64
	size    float64
	bright  float64
	twinkle float64
	speed   float64
	r, g, b uint8
}

type ssNode struct {
	x, y    float64
	vx, vy  float64
	size    float64
	bright  float64
	phase   float64
	group   int
	r, g, b uint8
}

type screensaverState struct {
	active          bool
	fadingIn        bool
	fadeAlpha       uint8
	startTick       uint64
	lastQuoteChange time.Time
	quoteIndex      int
	stars           []ssStar
	nodes           []ssNode
	connections     [][2]int
	inited          bool
}

var ss screensaverState

func initScreensaver() {
	ss.stars = make([]ssStar, ssStarCount)
	for i := range ss.stars {
		s := &ss.stars[i]
		s.x = rand.Float64() * float64(screenWidth)
		s.y = rand.Float64() * float64(screenHeight)
		s.size = 1 + rand.Float64()*2
		s.bright = 0.3 + rand.Float64()*0.7
		s.twinkle = rand.Float64() * math.Pi * 2
		s.speed = 0.5 + rand.Float64()*2
		if rand.Float64() < 0.15 {
			s.r = 200 + uint8(rand.Intn(56))
			s.g = 160 + uint8(rand.Intn(40))
			s.b = 120 + uint8(rand.Intn(40))
		} else {
			s.r = 180 + uint8(rand.Intn(76))
			s.g = 200 + uint8(rand.Intn(56))
			s.b = 240 + uint8(rand.Intn(16))
		}
	}

	ss.nodes = make([]ssNode, ssConstellationCount*5)
	for i := range ss.nodes {
		n := &ss.nodes[i]
		n.group = i % ssConstellationCount
		centerX := float64(screenWidth) * (0.15 + 0.7*float64(n.group)/float64(ssConstellationCount))
		centerY := float64(screenHeight) * (0.2 + 0.6*rand.Float64())
		n.x = centerX + (rand.Float64()-0.5)*200
		n.y = centerY + (rand.Float64()-0.5)*140
		n.vx = (rand.Float64() - 0.5) * 8
		n.vy = (rand.Float64() - 0.5) * 6
		n.size = 2 + rand.Float64()*3
		n.bright = 0.5 + rand.Float64()*0.5
		n.phase = rand.Float64() * math.Pi * 2
		n.r = 120 + uint8(rand.Intn(80))
		n.g = 160 + uint8(rand.Intn(60))
		n.b = 240 + uint8(rand.Intn(16))
	}

	ss.connections = ss.connections[:0]
	for i := range ss.nodes {
		n1 := &ss.nodes[i]
		bestDist := math.MaxFloat64
		bestIdx := -1
		for j := range ss.nodes {
			if i == j || ss.nodes[j].group != n1.group {
				continue
			}
			n2 := &ss.nodes[j]
			dx := n1.x - n2.x
			dy := n1.y - n2.y
			d := dx*dx + dy*dy
			if d < bestDist && d < 150*150 {
				bestDist = d
				bestIdx = j
			}
		}
		if bestIdx >= 0 {
			dup := false
			for _, c := range ss.connections {
				if (c[0] == i && c[1] == bestIdx) || (c[0] == bestIdx && c[1] == i) {
					dup = true
					break
				}
			}
			if !dup {
				ss.connections = append(ss.connections, [2]int{i, bestIdx})
			}
		}
	}

	ss.quoteIndex = rand.Intn(len(screensaverQuotes))
	ss.lastQuoteChange = time.Now()
	ss.inited = true
}

// screensaverNotifyInput wakes the screensaver on any input. It runs on the
// event thread, which also draws the screensaver, so the wake is immediate
// instead of waiting for a poll.
func screensaverNotifyInput() {
	noteUserInputTime(time.Now())
	ambientNotifyInput()
	if ss.active {
		deactivateScreensaver()
	}
}

// renderScreensaver draws one frame of the screensaver. Returns true while
// active so the caller skips normal UI rendering.
func renderScreensaver(renderer *sdl.Renderer, config *Config, tick uint64) bool {
	if !ss.inited {
		initScreensaver()
	}

	ss.active = true
	tickNow := tick

	// Fade in.
	if ss.fadingIn {
		elapsed := tickNow - ss.startTick
		if elapsed >= uint64(ssFadeInDuration) {
			ss.fadingIn = false
			ss.fadeAlpha = 255
		} else {
			ss.fadeAlpha = uint8(255 * float64(elapsed) / float64(ssFadeInDuration))
		}
	}

	renderer.SetDrawColor(6, 8, 14, 255)
	renderer.Clear()

	// Stars.
	for i := range ss.stars {
		s := &ss.stars[i]
		twinkle := math.Sin(float64(tickNow)*0.001*s.speed + s.twinkle)
		alpha := uint8(float64(s.bright) * (0.5 + 0.5*twinkle) * float64(ss.fadeAlpha) / 255.0)
		if alpha == 0 {
			continue
		}
		renderer.SetDrawColor(s.r, s.g, s.b, alpha)
		sx, sy := int32(s.x), int32(s.y)
		renderer.DrawPoint(sx, sy)
		if s.size > 1.5 {
			renderer.DrawPoint(sx+1, sy)
			renderer.DrawPoint(sx-1, sy)
			renderer.DrawPoint(sx, sy+1)
			renderer.DrawPoint(sx, sy-1)
		}
	}

	// Constellation connections.
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	for _, c := range ss.connections {
		n1 := &ss.nodes[c[0]]
		n2 := &ss.nodes[c[1]]
		dist := math.Sqrt((n1.x-n2.x)*(n1.x-n2.x) + (n1.y-n2.y)*(n1.y-n2.y))
		if dist > 200 {
			continue
		}
		alpha := uint8(40 * (1 - dist/200) * float64(ss.fadeAlpha) / 255.0)
		if alpha == 0 {
			continue
		}
		renderer.SetDrawColor(100, 140, 200, alpha)
		renderer.DrawLine(int32(n1.x), int32(n1.y), int32(n2.x), int32(n2.y))
	}

	// Constellation nodes.
	for i := range ss.nodes {
		n := &ss.nodes[i]
		n.x += n.vx * 0.016
		n.y += n.vy * 0.016
		if n.x < -20 {
			n.x = float64(screenWidth) + 20
		} else if n.x > float64(screenWidth)+20 {
			n.x = -20
		}
		if n.y < -20 {
			n.y = float64(screenHeight) + 20
		} else if n.y > float64(screenHeight)+20 {
			n.y = -20
		}
		pulse := 0.7 + 0.3*math.Sin(float64(tickNow)*0.002+n.phase)
		alpha := uint8(n.bright * pulse * float64(ss.fadeAlpha) / 255.0)
		if alpha == 0 {
			continue
		}
		drawParticle(renderer, int32(n.x), int32(n.y), int32(n.size),
			n.r, n.g, n.b, alpha)
	}

	// Rotating quote.
	now := time.Now()
	if now.Sub(ss.lastQuoteChange) > ssQuoteInterval {
		ss.quoteIndex = (ss.quoteIndex + 1) % len(screensaverQuotes)
		ss.lastQuoteChange = now
	}
	font, _ := getCachedFont(config, "small")
	if font != nil && ss.fadeAlpha > 60 {
		q := screensaverQuotes[ss.quoteIndex]
		qw, _, _ := font.SizeUTF8(q)
		qx := (screenWidth - int32(qw)) / 2
		qy := screenHeight/2 + 30
		alpha := uint8(float64(ss.fadeAlpha) * 0.8)
		if alpha > 180 {
			alpha = 180
		}
		renderText(renderer, config, font, q,
			sdl.Color{R: 140, G: 160, B: 200, A: alpha}, qx, qy)
	}

	// Clock in top-right, with the battery right under it so the saver is also
	// a glanceable status screen.
	timeFont, _ := getCachedFont(config, "medium")
	if timeFont != nil && ss.fadeAlpha > 80 {
		timeStr := time.Now().Format("15:04")
		tw, _, _ := timeFont.SizeUTF8(timeStr)
		tx := screenWidth - int32(tw) - 40
		ty := int32(40)
		alpha := uint8(float64(ss.fadeAlpha) * 0.9)
		if alpha > 200 {
			alpha = 200
		}
		renderText(renderer, config, timeFont, timeStr,
			sdl.Color{R: 200, G: 210, B: 230, A: alpha}, tx, ty)

		if small, _ := getCachedFont(config, "small"); small != nil {
			line := "Battery unknown"
			if pct := getBatteryPercent(); pct >= 0 {
				line = fmt.Sprintf("%d%%", pct)
				if getBatteryCharging() {
					line += " charging"
				}
			}
			lw, _, _ := small.SizeUTF8(line)
			lineAlpha := uint8(float64(ss.fadeAlpha) * 0.65)
			if lineAlpha > 170 {
				lineAlpha = 170
			}
			renderText(renderer, config, small, line,
				sdl.Color{R: 160, G: 175, B: 198, A: lineAlpha},
				screenWidth-int32(lw)-40, ty+int32(timeFont.Height())+6)
		}
	}

	drawScreensaverChat(renderer, config, time.Now())

	// Hint at bottom.
	if ss.fadeAlpha > 120 {
		hintFont, _ := getCachedFont(config, "small")
		if hintFont != nil {
			hint := "Press any key to wake"
			hw, _, _ := hintFont.SizeUTF8(hint)
			hx := (screenWidth - int32(hw)) / 2
			hy := screenHeight - 50
			pulse := 0.4 + 0.3*math.Sin(float64(tickNow)*0.003)
			a := uint8(float64(ss.fadeAlpha) * pulse * 0.5)
			renderText(renderer, config, hintFont, hint,
				sdl.Color{R: 120, G: 130, B: 150, A: a}, hx, hy)
		}
	}

	return true
}

// drawScreensaverChat paints the newest incoming messages as a quiet card in
// the lower-left corner, above the wake hint.
func drawScreensaverChat(renderer *sdl.Renderer, config *Config, now time.Time) {
	notes := screensaverChatNotes(now, ssChatShown)
	if len(notes) == 0 || ss.fadeAlpha < 60 {
		return
	}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	lineH := int32(font.Height()) + 6
	cardH := int32(38) + lineH*int32(len(notes))
	drawChatCard(renderer, config, notes, ss.fadeAlpha, 40, screenHeight-92-cardH)
}

// drawChatCard paints the newest incoming messages in the lower-left area of an
// idle screen. The caller places it (above the wake hint on the saver, above the
// now-playing card on the dock) and passes the fade its screen uses.
func drawChatCard(renderer *sdl.Renderer, config *Config, notes []ssChatNote, alpha uint8, x, y int32) {
	font, _ := getCachedFont(config, "small")
	if font == nil || len(notes) == 0 || alpha < 60 {
		return
	}
	lineH := int32(font.Height()) + 6
	cardW := int32(620)
	if cardW > screenWidth-96 {
		cardW = screenWidth - 96
	}
	cardH := int32(38) + lineH*int32(len(notes))

	a := float64(alpha) / 255.0
	fillRoundedRect(renderer, x, y, cardW, cardH, 12, sdl.Color{R: 12, G: 16, B: 26, A: uint8(170 * a)})
	strokeRoundedRect(renderer, x, y, cardW, cardH, 12, 1, sdl.Color{R: 255, G: 255, B: 255, A: uint8(30 * a)})
	fillRoundedRect(renderer, x, y, 4, cardH, 2, sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: uint8(220 * a)})

	renderText(renderer, config, font, "NEW CHAT",
		sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: uint8(230 * a)}, x+18, y+10)
	lineY := y + 34
	for _, note := range notes {
		sender := note.sender
		if sender == "" {
			sender = "Chat"
		}
		label := sender + ": "
		sw, _, _ := font.SizeUTF8(label)
		renderText(renderer, config, font, label,
			sdl.Color{R: 206, G: 220, B: 240, A: uint8(215 * a)}, x+18, lineY)
		textW := cardW - 36 - int32(sw)
		if textW > 0 {
			renderText(renderer, config, font, ellipsize(font, note.text, textW),
				sdl.Color{R: 160, G: 175, B: 198, A: uint8(200 * a)}, x+18+int32(sw), lineY)
		}
		lineY += lineH
	}
}

// deactivateScreensaver starts the fade-out and returns to normal UI.
func deactivateScreensaver() {
	ss.active = false
	ss.fadingIn = false
}

// lastInputUnixNano tracks when the user last interacted. It is an atomic
// because the idle-monitor goroutines read it while the event loop writes it:
// a plain time.Time there would be a data race, and a torn read on 32-bit ARM.
var lastInputUnixNano atomic.Int64

func init() { lastInputUnixNano.Store(time.Now().UnixNano()) }

// noteUserInputTime records one interaction for the monitors.
func noteUserInputTime(t time.Time) {
	lastInputUnixNano.Store(t.UnixNano())
}

// userInputAge is how long ago the last interaction happened. A missing
// timestamp counts as "just now" so a monitor never enters the idle screen
// before the app has started.
func userInputAge(now time.Time) time.Duration {
	ns := lastInputUnixNano.Load()
	if ns == 0 {
		return 0
	}
	return now.Sub(time.Unix(0, ns))
}

// startScreensaverMonitor prepares the screensaver state. Activation itself
// runs on the event thread in screensaverAutoTick: the entry check reads modal
// flags and the player/download state, which only that thread may touch, and a
// one-second check starts the saver promptly instead of up to five seconds
// late the way the old polling goroutine did.
func startScreensaverMonitor() {
	if !ss.inited {
		initScreensaver()
	}
}

// screensaverAutoCheckInterval throttles the idle check. One second keeps the
// start close to the configured timeout without doing work every frame.
const screensaverAutoCheckInterval = time.Second

var screensaverLastCheck time.Time

// screensaverAutoTick starts the screensaver once the device has been quiet for
// the configured timeout and nothing is using the panel.
func screensaverAutoTick(config *Config, now time.Time) {
	if config == nil || ss.active {
		return
	}
	// The dock/ambient screen is the idle screen when it is enabled; two idle
	// overlays competing for the panel is worse than either alone.
	if ambientModeEnabled(config) || ambientIsActive() {
		return
	}
	if !screensaverLastCheck.IsZero() && now.Sub(screensaverLastCheck) < screensaverAutoCheckInterval {
		return
	}
	screensaverLastCheck = now
	if idleScreenHoldOff(config) {
		// Playback, a download or an install is use without input. Keep the
		// idle clock moving so the countdown restarts when it finishes rather
		// than dropping straight onto the saver the second it ends.
		noteUserInputTime(now)
		return
	}
	if screensaverIdleTimeout <= 0 || userInputAge(now) < screensaverIdleTimeout {
		return
	}
	activateScreensaver()
}

// idleScreenHoldOffFrom is the pure decision at the heart of the gate: the
// screen stays awake while a modal owns the UI, while audio or video plays, and
// while a download or install is running.
func idleScreenHoldOffFrom(uiBlocked, musicPlaying, musicPaused, downloadActive, patchBusy bool) bool {
	return uiBlocked || musicPlaying || musicPaused || downloadActive || patchBusy
}

// idleScreenHoldOff reports whether something is using the device right now, so
// running it in the background is not the same as being idle.
func idleScreenHoldOff(config *Config) bool {
	_, _, _, playing, paused := ambientNowPlaying()
	return idleScreenHoldOffFrom(
		ambientEntryBlocked(config),
		playing,
		paused,
		installInProgress(),
		patchInstallBusy(),
	)
}

// installInProgress reports whether a package is being fetched or unpacked, so
// the progress bar does not disappear behind an idle screen mid-download.
func installInProgress() bool {
	visible, _, phase, _, _, _, _, _, ending := installBarSnapshot()
	if !visible || ending {
		return false
	}
	switch phase {
	case installPhaseResolving, installPhaseDownload, installPhaseExtract, installPhaseVerify:
		return true
	}
	return false
}

// patchInstallBusy reports whether a saved patch is being applied.
func patchInstallBusy() bool {
	return strings.TrimSpace(CurrentPatchBusyLabel()) != ""
}

// activateScreensaver starts the fade-in. Runs on the event thread.
func activateScreensaver() {
	if !ss.inited {
		initScreensaver()
	}
	ss.active = true
	ss.fadingIn = true
	ss.fadeAlpha = 0
	ss.startTick = sdl.GetTicks64()
}

// applyScreensaverTimeout sets the idle timeout everywhere it is needed: the
// package timer the monitor reads, the config field, the Quick Settings row and
// the saved user config.
func applyScreensaverTimeout(config *Config, seconds int) int {
	if seconds <= 0 {
		seconds = 25
	}
	screensaverIdleTimeout = time.Duration(seconds) * time.Second
	if len(qs.cycles) > 0 {
		qs.cycles[qsCycleScreensaverRow].Value = seconds
	}
	if config != nil {
		config.Variables.ScreensaverTimeout = seconds
		config.Variables.Custom["screensaverTimeout"] = strconv.Itoa(seconds)
		// The Settings row shows $screensaverTimeoutLabel, and substitution
		// reads only the custom block: keep the derived text in step with the
		// value everywhere the timeout is changed (Settings and Quick Settings).
		config.Variables.Custom["screensaverTimeoutLabel"] = screenTimeoutLabel(seconds)
		saveUserConfigDebounced(userConfigFromVariables(config))
	}
	return seconds
}
