package main

// displaytune_soft.go — contrast, exposure and colour temperature drawn by
// JukaHub itself where the display driver has no picture nodes
// (displaytune.go: the desktop builds, a firmware without disp2's
// enhancement).
//
// The layer works on the finished frame with blending alone: a few
// full-screen fills on the GPU, no pixel read-back.
//
//   - Contrast k = 1 + 0.08·step. Below 1: multiply by k (MOD), then add
//     (1−k)/2 (ADD). Above 1: subtract (k−1)/(2k) first, then multiply by k
//     with a custom mode (src·dst + dst), in that order so neither step clips
//     a value the other would have brought back into range. The subtraction
//     is invert, ADD, invert, so every blend uses the ADD operation, whose
//     meaning no renderer disputes.
//   - Exposure e = 0.1·step. Brighter multiplies by 1+e (the same custom
//     mode; a plain ADD of grey where the renderer refuses custom modes),
//     darker multiplies by 1+e (MOD).
//   - Colour temperature is a MOD tint: warmer takes away blue and a little
//     green, cooler takes away red and a little green.
//
// It runs last in the frame, after the screenshot and screen-share captures
// and before the software dimmer (main.go, core.go), so a screenshot shows the
// picture untouched, as the panel's own enhancement would leave it. The same
// per-frame call writes the driver's nodes again after a wake, since a suspend
// can reset them. Main thread only, like every renderer call.

import (
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// displayTuneSoft is what the layer draws: the offsets of the controls whose
// node is missing (displayTuneSoftSync), zero for the rest. Main thread.
var displayTuneSoft struct {
	contrast int
	exposure int
	temp     int
	on       bool
	// wake is the wakeCount the driver's nodes were last written for.
	wake uint64
	// probed is the renderer the custom modes were tried on, and custom
	// whether it took them; a renderer that refuses them (SDL's software
	// renderer) loses contrast above default and keeps the rest.
	probed *sdl.Renderer
	custom bool
	// synced is when the offsets were last read from the config: a config
	// swapped underneath (a reload, a restored backup) shows within seconds.
	synced time.Time
}

var (
	// displayTuneMulMode multiplies the frame by 1 + the fill colour:
	// src·dst + dst.
	displayTuneMulMode = sdl.ComposeCustomBlendMode(
		sdl.BLENDFACTOR_DST_COLOR, sdl.BLENDFACTOR_ONE, sdl.BLENDOPERATION_ADD,
		sdl.BLENDFACTOR_ZERO, sdl.BLENDFACTOR_ONE, sdl.BLENDOPERATION_ADD)
	// displayTuneInvertMode turns the frame into its negative with a white
	// fill: 1·(1 − dst) + 0·dst.
	displayTuneInvertMode = sdl.ComposeCustomBlendMode(
		sdl.BLENDFACTOR_ONE_MINUS_DST_COLOR, sdl.BLENDFACTOR_ZERO, sdl.BLENDOPERATION_ADD,
		sdl.BLENDFACTOR_ZERO, sdl.BLENDFACTOR_ONE, sdl.BLENDOPERATION_ADD)
)

// displayTuneSoftSync copies the stored offsets of the controls JukaHub must
// draw itself into displayTuneSoft. Called after every change, the reset and
// the startup apply. Main thread.
func displayTuneSoftSync(c *Config) {
	s := &displayTuneSoft
	s.synced = time.Now()
	s.contrast, s.exposure, s.temp = 0, 0, 0
	for _, ctl := range displayTuneControls {
		if !ctl.soft || displayTuneNodeOK(ctl.node) {
			continue
		}
		v := displayTuneOffset(c, ctl)
		switch ctl.key {
		case "display_contrast":
			s.contrast = v
		case "display_exposure":
			s.exposure = v
		case "display_colortemp":
			s.temp = v
		}
	}
	s.on = s.contrast != 0 || s.exposure != 0 || s.temp != 0
}

// displayTuneSoftRender draws the layer over the finished frame, and after a
// wake writes the driver's nodes again. Main thread, once a frame.
func displayTuneSoftRender(renderer *sdl.Renderer) {
	s := &displayTuneSoft
	displayTuneWakeTick()
	if time.Since(s.synced) >= 2*time.Second {
		displayTuneSoftSync(appConfig)
	}
	if !s.on || renderer == nil || ambientIsScreenOff() {
		return
	}
	// A clip left set by an overlay must not cut the layer short.
	_ = renderer.SetClipRect(nil)
	if s.contrast != 0 {
		displayTuneSoftContrast(renderer, s.contrast)
	}
	if s.exposure != 0 {
		displayTuneSoftExposure(renderer, s.exposure)
	}
	if s.temp != 0 {
		r, g, b := displayTuneSoftTint(s.temp)
		displayTuneSoftFill(renderer, sdl.BLENDMODE_MOD, r, g, b)
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
}

// displayTuneWakeTick writes the driver's nodes again after a wake: a suspend
// can reset them. One atomic load a frame; also called while a game owns the
// screen (main.go), since the game is what the nodes are for. Main thread.
func displayTuneWakeTick() {
	if n := wakeCount(); n != displayTuneSoft.wake {
		displayTuneSoft.wake = n
		displayTuneApplyStartup(appConfig)
	}
}

// displayTuneSoftNoCustom reports that the renderer was tried and refused
// the custom modes, so contrast above default is not drawn.
func displayTuneSoftNoCustom() bool {
	return displayTuneSoft.probed != nil && !displayTuneSoft.custom
}

// displayTuneSoftCustom reports whether renderer takes the two custom modes,
// trying them once per renderer.
func displayTuneSoftCustom(renderer *sdl.Renderer) bool {
	s := &displayTuneSoft
	if s.probed != renderer {
		s.probed = renderer
		// Headers older than SDL 2.0.6 compose BLENDMODE_NONE, which the
		// renderer takes, and the invert fill would paint the screen white.
		s.custom = displayTuneMulMode != sdl.BLENDMODE_NONE && displayTuneInvertMode != sdl.BLENDMODE_NONE &&
			renderer.SetDrawBlendMode(displayTuneMulMode) == nil &&
			renderer.SetDrawBlendMode(displayTuneInvertMode) == nil
		if !s.custom {
			logInfo("display", "this renderer has no custom blending: contrast above default is not drawn")
		}
	}
	return s.custom
}

// displayTuneSoftFill fills the whole frame with one grey or tint in mode.
func displayTuneSoftFill(renderer *sdl.Renderer, mode sdl.BlendMode, r, g, b uint8) {
	if renderer.SetDrawBlendMode(mode) != nil {
		return
	}
	_ = renderer.SetDrawColor(r, g, b, 255)
	_ = renderer.FillRect(nil)
}

// displayTuneSoftGrey is a 0..1 level as a channel value. Pure.
func displayTuneSoftGrey(x float64) uint8 {
	switch {
	case x <= 0:
		return 0
	case x >= 1:
		return 255
	}
	return uint8(x*255 + 0.5)
}

func displayTuneSoftContrast(renderer *sdl.Renderer, step int) {
	k := 1 + 0.08*float64(step)
	if k < 1 {
		m := displayTuneSoftGrey(k)
		displayTuneSoftFill(renderer, sdl.BLENDMODE_MOD, m, m, m)
		a := displayTuneSoftGrey((1 - k) / 2)
		displayTuneSoftFill(renderer, sdl.BLENDMODE_ADD, a, a, a)
		return
	}
	if !displayTuneSoftCustom(renderer) {
		return
	}
	sub := displayTuneSoftGrey((k - 1) / (2 * k))
	displayTuneSoftFill(renderer, displayTuneInvertMode, 255, 255, 255)
	displayTuneSoftFill(renderer, sdl.BLENDMODE_ADD, sub, sub, sub)
	displayTuneSoftFill(renderer, displayTuneInvertMode, 255, 255, 255)
	m := displayTuneSoftGrey(k - 1)
	displayTuneSoftFill(renderer, displayTuneMulMode, m, m, m)
}

func displayTuneSoftExposure(renderer *sdl.Renderer, step int) {
	e := 0.1 * float64(step)
	if e < 0 {
		m := displayTuneSoftGrey(1 + e)
		displayTuneSoftFill(renderer, sdl.BLENDMODE_MOD, m, m, m)
		return
	}
	if displayTuneSoftCustom(renderer) {
		m := displayTuneSoftGrey(e)
		displayTuneSoftFill(renderer, displayTuneMulMode, m, m, m)
		return
	}
	a := displayTuneSoftGrey(e * 0.4)
	displayTuneSoftFill(renderer, sdl.BLENDMODE_ADD, a, a, a)
}

// displayTuneSoftTint is the MOD colour for a colour-temperature offset
// (-20 warmest .. 20 coolest): at the ends (255, 215, 155) and
// (195, 225, 255). Pure.
func displayTuneSoftTint(step int) (r, g, b uint8) {
	if step < 0 {
		w := -step
		if w > 20 {
			w = 20
		}
		return 255, uint8(255 - 2*w), uint8(255 - 5*w)
	}
	if step > 20 {
		step = 20
	}
	return uint8(255 - 3*step), uint8(255 - (3*step)/2), 255
}
