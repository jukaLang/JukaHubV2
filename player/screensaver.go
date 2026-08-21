package main

import (
	"math"
	"math/rand"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Screensaver: constellation particles + rotating quotes
// ──────────────────────────────────────────────────────────────────────

var screensaverIdleTimeout = 5 * time.Minute

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
	wakeCh          chan struct{}
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
	ss.wakeCh = make(chan struct{}, 1)
}

// screensaverNotifyInput wakes the screensaver on any input.
func screensaverNotifyInput() {
	lastScreensaverInput = time.Now()
	if ss.active {
		select {
		case ss.wakeCh <- struct{}{}:
		default:
		}
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

	// Clock in top-right.
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
	}

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

// deactivateScreensaver starts the fade-out and returns to normal UI.
func deactivateScreensaver() {
	ss.active = false
	ss.fadingIn = false
}

// lastScreensaverInput tracks when the user last interacted.
var lastScreensaverInput = time.Now()

// startScreensaverMonitor runs a goroutine that watches for idle timeout.
func startScreensaverMonitor() {
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			if ss.active {
				// Check for wake signal.
				select {
				case <-ss.wakeCh:
					deactivateScreensaver()
					lastScreensaverInput = time.Now()
				default:
				}
				continue
			}
			if time.Since(lastScreensaverInput) > screensaverIdleTimeout {
				if !ss.inited {
					initScreensaver()
				}
				ss.active = true
				ss.fadingIn = true
				ss.fadeAlpha = 0
				ss.startTick = sdl.GetTicks64()
			}
		}
	}()
}
