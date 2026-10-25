package main

// ──────────────────────────────────────────────────────────────────────
// UI keyframe states
//
// A tiny animation layer for the shell chrome: overlays and cards animate in
// with a short, keyframed motion (slide + fade) and respond to hover with a
// small scale lift. The model is deliberately simple:
//
//   * uiKeyframe is a list of (normalized time, value) keys; the value at any
//     time is interpolated between neighbouring keys. An overshoot key before
//     1.0 gives a settle-in feel without a dedicated easing function per
//     animation.
//   * uiEnter holds one entrance animation's start tick and duration. It is
//     inert until begin() is called, so anything rendered without an entrance
//     (tests, cold first frame) draws at its final state.
//   * compositeUILayer draws a panel into an offscreen layer and copies the
//     layer back with one opacity and offset, which fades text and shapes
//     together. Backends without render targets simply draw the panel
//     directly: the motion degrades, never the UI.
//
// Everything is gated by reduced motion: the callers ask uiMotionEnabled and
// otherwise use the final keyframe, so the shell snaps into place.
// ──────────────────────────────────────────────────────────────────────

import (
	"math"

	"github.com/veandco/go-sdl2/sdl"
)

// uiClockMS is the animation clock. It is a variable so tests can drive the
// keyframes deterministically instead of sleeping.
var uiClockMS = func() uint64 { return sdl.GetTicks64() }

// Entrance durations. They are short on purpose: on a handheld screen a panel
// that takes longer than ~200ms to arrive reads as lag, not polish.
const (
	uiMenuEnterMS  = 170
	uiPanelEnterMS = 190
	uiFadeMS       = 120 // scrim fade, faster than the panel
)

// uiKey is one keyframe.
type uiKey struct {
	At    float64 // normalized time in [0,1]
	Value float64
}

// uiKeyframe is a time-ordered key list.
type uiKeyframe []uiKey

// at evaluates the track at t. Values outside the track clamp to its ends, and
// each segment eases with smoothstep so key positions alone shape the motion.
func (k uiKeyframe) at(t float64) float64 {
	if len(k) == 0 {
		return 0
	}
	if t <= k[0].At {
		return k[0].Value
	}
	for i := 1; i < len(k); i++ {
		if t <= k[i].At {
			span := k[i].At - k[i-1].At
			if span <= 0 {
				return k[i].Value
			}
			f := (t - k[i-1].At) / span
			f = f * f * (3 - 2*f)
			return k[i-1].Value + (k[i].Value-k[i-1].Value)*f
		}
	}
	return k[len(k)-1].Value
}

// The shared motions. Menus drop down from under the Apps pill; centred panels
// rise a little; the fade lags the slide slightly so the motion reads first.
var (
	uiMenuDrop  = uiKeyframe{{At: 0, Value: -18}, {At: 0.62, Value: 3}, {At: 1, Value: 0}}
	uiDropDown  = uiKeyframe{{At: 0, Value: -10}, {At: 0.62, Value: 2}, {At: 1, Value: 0}}
	uiPanelRise = uiKeyframe{{At: 0, Value: 14}, {At: 0.62, Value: -2}, {At: 1, Value: 0}}
	uiFadeIn    = uiKeyframe{{At: 0, Value: 0}, {At: 0.45, Value: 0.75}, {At: 1, Value: 1}}
)

// uiEnter is one entrance animation's bookkeeping. The zero value is "already
// settled", which is what a panel that was never opened should look like.
type uiEnter struct {
	start  uint64
	dur    uint64
	active bool
}

// begin restarts the entrance at now.
func (e *uiEnter) begin(now uint64, dur uint64) {
	e.start, e.dur, e.active = now, dur, true
}

// settled reports whether the entrance has finished.
func (e *uiEnter) settled() bool { return !e.active }

// cancel snaps the entrance to its final state. Callers use it when motion is
// disabled, so the animation never stays "active" and keeps the render loop in
// its fast cadence for a panel that is already settled.
func (e *uiEnter) cancel() { e.active = false }

// progress returns the normalized time of the entrance (1 when inactive).
func (e *uiEnter) progress(now uint64) float64 {
	if !e.active || e.dur == 0 {
		return 1
	}
	if now < e.start {
		return 0
	}
	t := float64(now-e.start) / float64(e.dur)
	if t >= 1 {
		e.active = false
		return 1
	}
	return t
}

// uiMotionEnabled reports whether entrance/hover motion is allowed. Reduced
// motion and the battery-saver (Low Power) mode both snap every state to its//	final value and the power selector reports it for them (powermode.go).
func uiMotionEnabled(config *Config) bool {
	if config == nil {
		return true
	}
	return !effectiveReducedMotion(config) && !effectiveLowPower(config)
}

// uiEaseOutCubic maps a linear progress onto a decelerating curve.
func uiEaseOutCubic(t float64) float64 {
	t = uiClamp01(t)
	return 1 - math.Pow(1-t, 3)
}

// uiScrimProgress returns how opaque the dimming scrim behind a panel should
// be. It reaches full strength after uiFadeMS even when the panel's own
// entrance is longer, so the backdrop settles first and the panel reads as
// arriving on top of it.
func uiScrimProgress(progress float64, durationMS uint64) float64 {
	if durationMS == 0 || uiFadeMS == 0 {
		return 1
	}
	t := uiClamp01(progress) * float64(durationMS) / float64(uiFadeMS)
	return uiEaseOutCubic(t)
}

// uiClamp01 clamps v into [0,1].
func uiClamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// uiAnimating reports whether any overlay entrance is still playing, so the
// adaptive frame-rate controller can keep the fast cadence while it does.
func uiAnimating() bool {
	return tb.enter.active || nc.enter.active || qs.enter.active
}

// ── Layer compositing ─────────────────────────────────────────────────

var (
	uiLayerTex         *sdl.Texture
	uiLayerFor         *sdl.Renderer // the renderer that owns uiLayerTex
	uiLayerW, uiLayerH int32
	uiLayerUnsupported bool
)

// uiLayerTexture returns (and caches) a full-screen target texture. It returns
// nil when the backend has no render targets; callers then draw unanimated.
func uiLayerTexture(renderer *sdl.Renderer) *sdl.Texture {
	if renderer == nil {
		return nil
	}
	if uiLayerFor != renderer {
		// A new renderer (window recreation, or the next test): the old layer
		// belonged to a renderer that is gone, so drop the reference and probe
		// target support again instead of trusting the previous verdict.
		uiLayerTex, uiLayerFor = nil, renderer
		uiLayerUnsupported = false
	}
	if uiLayerUnsupported {
		return nil
	}
	if uiLayerTex != nil && uiLayerW == screenWidth && uiLayerH == screenHeight {
		return uiLayerTex
	}
	if uiLayerTex != nil {
		uiLayerTex.Destroy()
		uiLayerTex = nil
	}
	tex, err := renderer.CreateTexture(sdl.PIXELFORMAT_ARGB8888, sdl.TEXTUREACCESS_TARGET, screenWidth, screenHeight)
	if err != nil || tex == nil {
		uiLayerUnsupported = true
		return nil
	}
	_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
	uiLayerTex, uiLayerW, uiLayerH = tex, screenWidth, screenHeight
	return tex
}

// beginUILayer makes the layer texture the current render target. A cached
// layer can belong to a renderer that has since been
// destroyed (Go may hand a new renderer the same wrapper address), so a failed
// attach drops the cache and retries once with a fresh texture.
func beginUILayer(renderer *sdl.Renderer) *sdl.Texture {
	for attempt := 0; attempt < 2; attempt++ {
		tex := uiLayerTexture(renderer)
		if tex == nil {
			return nil
		}
		if err := renderer.SetRenderTarget(tex); err == nil {
			_ = renderer.SetClipRect(nil)
			return tex
		}
		uiLayerTex, uiLayerW, uiLayerH = nil, 0, 0
		uiLayerUnsupported = attempt == 1
	}
	return nil
}

// compositeUILayer draws one panel through an offscreen layer so the whole
// panel — text, shapes and all — fades as a single layer and slides by offsetY.
// With full opacity and no offset it bypasses the layer entirely, which keeps
// the settled state free of compositing cost.
func compositeUILayer(renderer *sdl.Renderer, alpha float64, offsetY int32, draw func()) {
	compositeUILayerIn(renderer, alpha, offsetY, sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight}, draw)
}

// compositeUILayerIn is compositeUILayer restricted to the panel's own area.
// Only that rectangle is blended back, so a 380px-wide panel costs a fraction
// of a full-screen composite. The area is grown a little to keep soft edges
// and shadows from being clipped.
func compositeUILayerIn(renderer *sdl.Renderer, alpha float64, offsetY int32, area sdl.Rect, draw func()) {
	alpha = uiClamp01(alpha)
	if alpha >= 0.995 && offsetY == 0 {
		draw()
		return
	}
	src, dst, ok := uiLayerRects(area, offsetY)
	clear := sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight}
	if ok {
		// Only the panel region needs to be cleared: it is the only part of
		// the layer that is copied back.
		clear = src
	}

	prev := renderer.GetRenderTarget()
	tex := beginUILayer(renderer)
	if tex == nil {
		draw()
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_NONE)
	renderer.SetDrawColor(0, 0, 0, 0)
	renderer.FillRect(&clear)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	draw()
	_ = renderer.SetRenderTarget(prev)
	_ = renderer.SetClipRect(nil)

	_ = tex.SetAlphaMod(uint8(alpha*255 + 0.5))
	_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
	if ok {
		renderer.Copy(tex, &src, &dst)
	} else {
		renderer.Copy(tex, nil, &sdl.Rect{X: 0, Y: offsetY, W: screenWidth, H: screenHeight})
	}
	_ = tex.SetAlphaMod(255)
}

// uiLayerMargin keeps drop shadows and rounded edges inside the copied region.
const uiLayerMargin = 24

// uiLayerRects turns a panel area into the source/destination rectangles of the
// layer copy, clipped to the screen.
func uiLayerRects(area sdl.Rect, offsetY int32) (sdl.Rect, sdl.Rect, bool) {
	if area.W <= 0 || area.H <= 0 {
		return sdl.Rect{}, sdl.Rect{}, false
	}
	src := sdl.Rect{
		X: area.X - uiLayerMargin,
		Y: area.Y - uiLayerMargin,
		W: area.W + uiLayerMargin*2,
		H: area.H + uiLayerMargin*2,
	}
	// Keep the source inside the layer texture.
	if src.X < 0 {
		src.W += src.X
		src.X = 0
	}
	if src.Y < 0 {
		src.H += src.Y
		src.Y = 0
	}
	if src.X+src.W > screenWidth {
		src.W = screenWidth - src.X
	}
	if src.Y+src.H > screenHeight {
		src.H = screenHeight - src.Y
	}
	if src.W <= 0 || src.H <= 0 {
		return sdl.Rect{}, sdl.Rect{}, false
	}
	dst := src
	dst.Y += offsetY
	return src, dst, true
}

// ── Hover weight ──────────────────────────────────────────────────────

// uiHover eases a 0..1 weight toward its target once per frame. It is used for
// the small hover/focus scale lift on cards, so the growth settles instead of
// snapping when the pointer or D-pad moves.
type uiHover struct {
	weight float64
}

// update steps the weight toward target. stepped=false snaps (reduced motion,
// or the first frame of a new focus).
func (h *uiHover) update(target float64, stepped bool) float64 {
	target = uiClamp01(target)
	if !stepped {
		h.weight = target
		return h.weight
	}
	// Frame-rate independent exponential settle: ~90% of the distance in 120ms
	// at 60fps, without needing the frame delta.
	h.weight += (target - h.weight) * 0.28
	if math.Abs(target-h.weight) < 0.01 {
		h.weight = target
	}
	return h.weight
}

// ── Click feedback ────────────────────────────────────────────────────

// uiPressDepth turns the time since a click into a 0..1 "dip" weight: it
// rises quickly, holds briefly and eases back out, so a press reads as a
// deliberate push rather than a colour that snaps on and off. At or after the
// duration the weight is 0, which is the settled state.
func uiPressDepth(elapsedMS, durationMS float64) float64 {
	if durationMS <= 0 || elapsedMS <= 0 {
		return 0
	}
	t := uiClamp01(elapsedMS / durationMS)
	if t >= 1 {
		return 0
	}
	// sin(pi*t) peaks at the midpoint; the cubic ease on the way in keeps the
	// press from starting with a visible jump.
	return math.Sin(math.Pi * uiEaseOutCubic(t))
}

// uiPressProgress returns the press depth to draw for a click that started
// elapsedMS ago. Reduced motion snaps to the full dip for the whole press
// window: the feedback stays, the per-frame animation does not.
//
// Unlike the entrance animations this deliberately ignores LowPower: a 3px dip
// over a 90-150ms press is nearly free, and on the TrimUI (where LowPower is on
// by default) it is the only motion the user sees on every button press.
func uiPressProgress(config *Config, elapsedMS, durationMS float64) float64 {
	if config != nil && effectiveReducedMotion(config) {
		return 1
	}
	return uiPressDepth(elapsedMS, durationMS)
}

// ── Scene transitions ─────────────────────────────────────────────────

// Scene fade timings. The old loop used 250ms out / 300ms in, which on a
// handheld reads as waiting after every button press. These keep the same
// fade-out/fade-in shape at 160/200ms: short enough to feel immediate on the
// TrimUI panel, long enough for the eye to track the new screen arriving.
const (
	uiSceneOutMS = 160
	uiSceneInMS  = 200
)

// uiSceneFadeMS returns the duration of a scene fade phase ("fade-out" or
// "fade-in"). Unknown phases report 0 so callers can snap. A config with
// transition "instant" reports 0 for both phases, which settles the scrim on
// the same frame instead of waiting out the fade.
func uiSceneFadeMS(config *Config, phase string) float64 {
	if motionInstant(config) {
		return 0
	}
	switch phase {
	case "fade-out":
		return uiSceneOutMS
	case "fade-in":
		return uiSceneInMS
	}
	return 0
}

// uiSceneScrimAlpha returns the scrim opacity for a fade phase at elapsedMS.
// Fade-out eases toward fully covered; fade-in eases from covered back to
// clear. The curve is the same ease-out the loop used before the timings were
// tokenized, so the motion feels identical, only faster.
func uiSceneScrimAlpha(config *Config, phase string, elapsedMS float64) uint8 {
	dur := uiSceneFadeMS(config, phase)
	if dur <= 0 {
		if phase == "fade-in" {
			return 0
		}
		return 255
	}
	t := uiEaseOutCubic(elapsedMS / dur)
	if phase == "fade-in" {
		return uint8((1 - t) * 255)
	}
	return uint8(t * 255)
}

// uiSceneScrimColor is the colour the scenes fade through. It follows the
// active theme's background instead of a hardcoded near-black, so light themes
// no longer flash dark between screens.
func uiSceneScrimColor() sdl.Color {
	return ColorBackground
}

// uiSceneFadeDone reports whether a fade phase has run its course.
func uiSceneFadeDone(config *Config, phase string, elapsedMS float64) bool {
	dur := uiSceneFadeMS(config, phase)
	return dur <= 0 || elapsedMS >= dur
}
