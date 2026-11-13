package main

// themetabs.go — the theme gallery's four tabs: Colour, Total conversion,
// Consoles and Experience.
//
// Consoles is not a kind of preset — a console look is a total conversion like
// any other, and everything that checks Kind == themeKindConversion (the
// schedule, plugins, the packs, the previews) keeps treating it as one. The tab
// is a filter over the conversions instead (themePresetIsConsole), and Total
// conversion shows the rest.
//
// Experience is not a list of presets at all: it is the Experience packs
// screen (xpackui.go), which draws the same tab strip under its header with
// Experience lit. Choosing the tab on Themes opens that screen, and choosing
// another tab there comes back to Themes with that tab open. Both moves are
// sideways — switchScene without a history entry — so the two screens are one
// place as far as B is concerned: B from either goes wherever Themes was
// entered from, never back and forth between the two.

import (
	"strconv"

	"github.com/veandco/go-sdl2/sdl"
)

// Tab groups (themeTab.Group). Empty is every preset of the tab's Kind that
// no other tab claims.
const (
	themeTabConsole    = "console"
	themeTabExperience = "experience"
)

// themeTabPresets is the list a tab's grid draws, in the canonical order.
// ListThemePresetsFor keeps meaning "every preset of a kind" for its other
// callers (the schedule, voice settings).
func themeTabPresets(tab themeTab) []string {
	switch tab.Group {
	case themeTabExperience:
		return nil
	case themeTabConsole:
		return themeConsolePresets()
	}
	names := ListThemePresetsFor(tab.Kind)
	if tab.Kind != themeKindConversion {
		return names
	}
	out := names[:0]
	for _, name := range names {
		if !themePresetIsConsole(name) {
			out = append(out, name)
		}
	}
	return out
}

// themeConsolePresets is the Consoles tab: themeConsoleTabOrder first, then
// any other console conversion in the catalogue's order.
func themeConsolePresets() []string {
	names := make([]string, 0, len(themeConsoleTabOrder)+4)
	seen := make(map[string]bool, len(themeConsoleTabOrder))
	for _, name := range themeConsoleTabOrder {
		if p, ok := themePresets[name]; ok && p.Kind == themeKindConversion && !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	for _, name := range ListThemePresetsFor(themeKindConversion) {
		if !seen[name] && themePresetIsConsole(name) {
			names = append(names, name)
			seen[name] = true
		}
	}
	return names
}

// themeTabIndex is the index of the tab with a group, or -1.
func themeTabIndex(group string) int {
	for i, tab := range themeGalleryTabs {
		if tab.Group == group {
			return i
		}
	}
	return -1
}

// themeGallerySelectTab opens tab i of the Themes gallery and reports whether
// anything changed. The Experience tab opens the Experience packs screen
// instead and leaves the gallery's own tab where it was.
func themeGallerySelectTab(i int) bool {
	if i < 0 || i >= len(themeGalleryTabs) || i == themeGalleryTab {
		return false
	}
	if themeGalleryTabs[i].Group == themeTabExperience {
		return themeTabsOpenExperience(appConfig)
	}
	themeGalleryTab = i
	themeGalleryIndex = 0
	themeGalleryScroll = 0
	return true
}

// themeTabsSideways switches to a scene without recording the one being left
// (see the file comment) and reports whether the switch started.
func themeTabsSideways(c *Config, idx int) bool {
	if c == nil || idx < 0 || idx >= len(c.Scenes) || idx == currentSceneIndex || transitionPhase != "none" {
		return false
	}
	switchScene(c, idx, false)
	return pendingSceneIndex == idx
}

// themeTabsOpenExperience is the Experience tab on Themes.
func themeTabsOpenExperience(c *Config) bool {
	if c == nil {
		return false
	}
	xpackEnsurePlatformScenes(c)
	return themeTabsSideways(c, sceneIndexExact(c, xpackSceneName))
}

// themeTabsLeaveExperience is tab i chosen on the Experience packs screen:
// back to Themes with that tab open.
func themeTabsLeaveExperience(c *Config, i int) bool {
	if i < 0 || i >= len(themeGalleryTabs) || themeGalleryTabs[i].Group == themeTabExperience {
		return false
	}
	if !themeTabsSideways(c, sceneIndexExact(c, "ThemePresets")) {
		return false
	}
	themeGalleryTab = i
	themeGalleryIndex = 0
	themeGalleryScroll = 0
	return true
}

// themeTabStep is the tab delta steps from i away, wrapping as the gallery's
// Tab and L1/R1 do.
func themeTabStep(i, delta int) int {
	n := len(themeGalleryTabs)
	if n == 0 {
		return -1
	}
	return ((i+delta)%n + n) % n
}

// drawThemeTabStrip draws the tab pills along element's top edge, tab active
// filled with the accent, and hint at the far end when it fits beside them.
func drawThemeTabStrip(renderer *sdl.Renderer, config *Config, element Element, active int, hint string) {
	font := themeGalleryTabFont(config)
	if font == nil || len(themeGalleryTabs) == 0 {
		return
	}
	for i, tab := range themeGalleryTabs {
		rect := themeGalleryTabRect(element, i)
		open := i == active
		fill := glassChipFill()
		ink := ColorTextSecondary()
		if open {
			fill = glassAccent()
			ink = contrastText(fill)
		}
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, rect.H/2, fill)
		if !open {
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, rect.H/2, 1, glassChipEdge())
		}
		tw, th, _ := font.SizeUTF8(tab.Label)
		drawText(renderer, font, tab.Label, rect.X+(rect.W-int32(tw))/2,
			rect.Y+(rect.H-int32(th))/2, ink, textAlignLeft)
	}
	if hint == "" {
		return
	}
	last := themeGalleryTabRect(element, len(themeGalleryTabs)-1)
	hintW, hintH, _ := font.SizeUTF8(hint)
	hintX := element.X + getElementWidth(element, 1160) - int32(hintW) - 4
	if hintX > last.X+last.W+16 {
		drawText(renderer, font, hint, hintX, last.Y+(last.H-int32(hintH))/2,
			ColorTextSecondary(), textAlignLeft)
	}
}

// xpackTabStrip is where the Experience packs screen drew the tabs last
// frame, for the mouse; drawn is false until it has.
var xpackTabStrip struct {
	element Element
	drawn   bool
}

// xpackDrawThemeTabs draws the tab strip at y across the page and returns the
// y below it.
func xpackDrawThemeTabs(renderer *sdl.Renderer, c *Config, x, y, w int32) int32 {
	el := Element{X: x, Y: y, Width: StringOrInt(strconv.Itoa(int(w)))}
	xpackTabStrip.element, xpackTabStrip.drawn = el, true
	drawThemeTabStrip(renderer, c, el, themeTabIndex(themeTabExperience), "")
	return y + themeGalleryTabH
}

// xpackThemeTabStep is Tab / L1 / R1 on the Experience packs screen.
func xpackThemeTabStep(c *Config, delta int) bool {
	next := themeTabStep(themeTabIndex(themeTabExperience), delta)
	if themeTabsLeaveExperience(c, next) {
		PlayNavSound()
		return true
	}
	return false
}

// xpackThemeTabClick is a click on the Experience packs screen's tab strip;
// it reports whether the click hit a pill.
func xpackThemeTabClick(c *Config, mx, my int32) bool {
	if !xpackTabStrip.drawn {
		return false
	}
	i := themeGalleryTabAt(xpackTabStrip.element, mx, my)
	if i < 0 {
		return false
	}
	if themeTabsLeaveExperience(c, i) {
		PlayNavSound()
	}
	return true
}
