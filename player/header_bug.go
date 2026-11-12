package main

// The bug chip sits between Notifications and Trophies in both headers. Its
// badge counts reports saved on the device that have not reached the service.

import "github.com/veandco/go-sdl2/sdl"

const headerBugLabel = "Report a bug"

var headerBugRect sdl.Rect

func headerBugSize(config *Config) (int32, int32) {
	return notifBellSize(config)
}

func headerBugInScreen(config *Config) bool {
	return config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
		config.Scenes[currentSceneIndex].Name == bugrepSceneName
}

func drawHeaderBug(renderer *sdl.Renderer, config *Config, rightX, barH int32, focused bool) sdl.Rect {
	w, h := headerBugSize(config)
	if renderer == nil || w <= 0 || h <= 0 {
		return sdl.Rect{}
	}
	rect := sdl.Rect{X: rightX - w, Y: (barH - h) / 2, W: w, H: h}
	ink := sdl.Color{R: 244, G: 147, B: 107, A: 255}
	button := UIIconButton{
		Rect: rect,
		Style: UIIconButtonStyle{
			Surface:     ColorIconSurface,
			OpenSurface: ColorIconSurface,
			Ring:        accentColor,
			RingWidth:   glassFocusRingWidth,
			IconInset:   notifBellIconInset,
			Glyph: func(r *sdl.Renderer, box sdl.Rect, _ sdl.Color) {
				drawHomeGlyphTinted(r, "bug", box, ink)
			},
		},
		State:      UIIconButtonState{Focused: focused, Open: headerBugInScreen(config)},
		BadgeFont:  notifBadgeFont(config),
		BadgeColor: ink,
		BadgeInk:   ColorIconDark,
		Badge:      notifBadgeLabel(bugrepOutboxCount()),
	}
	UIIconButtonDraw(renderer, button)
	headerBugRect = rect
	return rect
}

func headerBugHit(x, y int32) bool {
	return headerBugRect.W > 0 && headerBugRect.H > 0 && hitTouchTarget(headerBugRect, x, y)
}

func headerBugClear() { headerBugRect = sdl.Rect{} }

func headerBugActivate(config *Config) {
	if config != nil {
		bugrepOpenScreen(config)
	}
}
