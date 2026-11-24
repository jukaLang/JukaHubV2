package main

// storeshots.go — pictures and plain words for what the stores offer.
//
// Packages. A registry entry may name screenshots ("screenshots": up to three
// HTTPS links) and the handhelds someone checked it on ("checked_on"), apart
// from the "device" list its author made it for. The details sheet fetches
// the screenshots when it opens — the same rules as the store's icons
// (pkgart.go): HTTPS only, a megabyte at most, only bytes that sniff as a
// picture, into the same bounded folder — and shows them above the text; the
// Compatibility section says who checked it where.
//
// Themes. A community theme may carry a "screenshot" (an HTTPS picture), the
// handhelds it was made for ("devices") and a longer "about". X in the theme
// library opens a theme's page: its screenshot when there is one, otherwise a
// preview of Home drawn from the theme's own colours (so every theme shows
// what it looks like before it is installed), the long description, and
// whether it was made for this handheld.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
)

// pkgRawList is the first key holding a string or a list of strings, as a
// list of at most max non-empty strings.
func pkgRawList(fields map[string]json.RawMessage, max int, keys ...string) []string {
	for _, k := range keys {
		raw, ok := fields[k]
		if !ok {
			continue
		}
		var list []string
		if json.Unmarshal(raw, &list) != nil {
			var one string
			if json.Unmarshal(raw, &one) != nil {
				continue
			}
			list = []string{one}
		}
		out := make([]string, 0, len(list))
		for _, v := range list {
			if v = strings.TrimSpace(v); v != "" && len(out) < max {
				out = append(out, v)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

// pkgCheckedOnLine says who checked a package where: "Checked on Smart Pro
// and Brick (and on this handheld)." "" when nobody said.
func pkgCheckedOnLine(devs []string) string {
	labels := make([]string, 0, len(devs))
	for _, d := range devs {
		if l := pkgDeviceLabel(d); l != "" {
			labels = append(labels, l)
		}
	}
	if len(labels) == 0 {
		return ""
	}
	line := "Checked on " + strings.Join(labels, ", ") + "."
	if pkgDeviceFit(devs, DetectDevice()) == 1 {
		line += " ✓ That includes this handheld."
	}
	return line
}

// storeShots is what the pictures' fetches found: a link → its cached file
// ("" while it is fetched or when it failed).
var storeShots struct {
	mu    sync.Mutex
	asked map[string]bool
	ready map[string]string
}

// storeShotPath is a link's cached file when it is there.
func storeShotPath(u string) string {
	storeShots.mu.Lock()
	defer storeShots.mu.Unlock()
	return storeShots.ready[u]
}

// storeShotWant starts fetching links not asked for yet this run. Main
// thread; the fetch is a goroutine's.
func storeShotWant(links []string) {
	var todo []string
	storeShots.mu.Lock()
	if storeShots.asked == nil {
		storeShots.asked, storeShots.ready = map[string]bool{}, map[string]string{}
	}
	for _, u := range links {
		if !strings.HasPrefix(strings.ToLower(u), "https://") || storeShots.asked[u] {
			continue
		}
		storeShots.asked[u] = true
		todo = append(todo, u)
	}
	storeShots.mu.Unlock()
	if len(todo) == 0 {
		return
	}
	go func() {
		pkgArtMu.Lock()
		defer pkgArtMu.Unlock()
		for _, u := range todo {
			path := pkgArtPath(u)
			if !regularFile(path) {
				if !IsOnline() || pkgArtCount() >= pkgArtMaxFiles {
					continue
				}
				if err := pkgFetchArt(context.Background(), u, path); err != nil {
					logDebug("store", "screenshot not fetched: %v", err)
					continue
				}
			}
			storeShots.mu.Lock()
			storeShots.ready[u] = path
			storeShots.mu.Unlock()
		}
	}()
}

// storeDrawPicture draws the picture at path fitted inside r (centred,
// aspect kept) and returns the rectangle it used; zero when it is not
// loaded.
func storeDrawPicture(renderer *sdl.Renderer, path string, r sdl.Rect) sdl.Rect {
	tex := appIconTexture(renderer, path)
	if tex == nil || r.W <= 0 || r.H <= 0 {
		return sdl.Rect{}
	}
	_, _, tw, th, err := tex.Query()
	if err != nil || tw <= 0 || th <= 0 {
		return sdl.Rect{}
	}
	w, h := r.W, r.W*th/tw
	if h > r.H {
		h, w = r.H, r.H*tw/th
	}
	dst := sdl.Rect{X: r.X + (r.W-w)/2, Y: r.Y + (r.H-h)/2, W: w, H: h}
	_ = renderer.Copy(tex, nil, &dst)
	strokeRoundedRect(renderer, dst.X, dst.Y, dst.W, dst.H, 8, 1, glassA(40))
	return dst
}

// pkgDrawShots draws a package's cached screenshots side by side in r and
// returns the height it took (0 with none yet).
func pkgDrawShots(renderer *sdl.Renderer, it *pkgItem, r sdl.Rect) int32 {
	if it == nil || len(it.Extra.Screenshots) == 0 || r.H < 60 {
		return 0
	}
	storeShotWant(it.Extra.Screenshots)
	var paths []string
	for _, u := range it.Extra.Screenshots {
		if p := storeShotPath(u); p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return 0
	}
	const gap = int32(10)
	n := int32(len(paths))
	cell := sdl.Rect{X: r.X, Y: r.Y, W: (r.W - gap*(n-1)) / n, H: r.H}
	used := int32(0)
	for _, p := range paths {
		if d := storeDrawPicture(renderer, p, cell); d.H > used {
			used = d.H
		}
		cell.X += cell.W + gap
	}
	if used == 0 {
		return 0
	}
	return used + 14
}

// ── Themes ───────────────────────────────────────────────────────────────

// themeCommunityFit is whether a community theme was made for this
// handheld: 1 yes (or for any), -1 no, 0 not said.
func themeCommunityFit(entry communityTheme) int {
	return pkgDeviceFit(entry.Devices, DetectDevice())
}

// themeCommunityFitLine says it in words.
func themeCommunityFitLine(entry communityTheme) string {
	labels := make([]string, 0, len(entry.Devices))
	for _, d := range entry.Devices {
		if l := pkgDeviceLabel(d); l != "" {
			labels = append(labels, l)
		}
	}
	switch themeCommunityFit(entry) {
	case 1:
		if len(labels) > 0 {
			return "Made for " + strings.Join(labels, ", ") + " · fits this handheld"
		}
		return "Made for every handheld"
	case -1:
		return "Made for " + strings.Join(labels, ", ") + " · not for this handheld, it may look off"
	}
	return "Any screen: the author did not name a handheld"
}

// themeCommunityPreview draws what Home looks like in a theme: its
// screenshot when there is one and it has arrived, otherwise a small Home
// painted from the theme's own colours.
func themeCommunityPreview(renderer *sdl.Renderer, entry communityTheme, r sdl.Rect) {
	if shot := strings.TrimSpace(entry.Screenshot); shot != "" {
		storeShotWant([]string{shot})
		if p := storeShotPath(shot); p != "" {
			if d := storeDrawPicture(renderer, p, r); d.W > 0 {
				return
			}
		}
	}
	col := func(hex string, fallback sdl.Color) sdl.Color {
		if c, ok := parseHexColor(hex); ok {
			return sdl.Color{R: c.R, G: c.G, B: c.B, A: 255}
		}
		return fallback
	}
	bg := col(entry.Theme.Background, sdl.Color{R: 18, G: 20, B: 28, A: 255})
	surface := col(entry.Theme.Surface, sdl.Color{R: 34, G: 38, B: 52, A: 255})
	focus := col(entry.Theme.BorderFocus, glassAccent())
	ink := col(entry.Theme.TextPrimary, sdl.Color{R: 235, G: 237, B: 245, A: 255})
	dim := WithAlpha(ink, 120)
	// The 16:9 screen inside r.
	w, h := r.W, r.W*9/16
	if h > r.H {
		h, w = r.H, r.H*16/9
	}
	s := sdl.Rect{X: r.X + (r.W-w)/2, Y: r.Y + (r.H-h)/2, W: w, H: h}
	fillRoundedRect(renderer, s.X, s.Y, s.W, s.H, 10, bg)
	// The top bar: a title and the status chips.
	unit := max32(s.H/24, 2)
	fillRoundedRect(renderer, s.X+unit*2, s.Y+unit*2, s.W/5, unit*2, unit, dim)
	for i := int32(0); i < 3; i++ {
		fillRoundedRect(renderer, s.X+s.W-unit*2-(i+1)*unit*4, s.Y+unit*2, unit*3, unit*2, unit, WithAlpha(ink, 70))
	}
	// A hero card and a row of tiles, the second one focused.
	hero := sdl.Rect{X: s.X + unit*2, Y: s.Y + unit*6, W: s.W * 2 / 5, H: s.H / 2}
	fillRoundedRect(renderer, hero.X, hero.Y, hero.W, hero.H, unit*2, surface)
	fillRoundedRect(renderer, hero.X+unit*2, hero.Y+hero.H-unit*6, hero.W/2, unit*2, unit, ink)
	fillRoundedRect(renderer, hero.X+unit*2, hero.Y+hero.H-unit*3, hero.W/3, unit, unit/2, dim)
	tileW := (s.W - hero.W - unit*8) / 2
	for i := int32(0); i < 4; i++ {
		t := sdl.Rect{X: hero.X + hero.W + unit*2 + (i%2)*(tileW+unit*2), Y: hero.Y + (i/2)*(hero.H/2+unit), W: tileW, H: hero.H/2 - unit}
		fillRoundedRect(renderer, t.X, t.Y, t.W, t.H, unit*2, surface)
		fillRoundedRect(renderer, t.X+unit*2, t.Y+t.H-unit*3, t.W/2, unit, unit/2, dim)
		if i == 1 {
			strokeRoundedRect(renderer, t.X-2, t.Y-2, t.W+4, t.H+4, unit*2+2, 2, focus)
		}
	}
	// The footer's hints.
	for i := int32(0); i < 3; i++ {
		fillRoundedRect(renderer, s.X+unit*2+i*s.W/6, s.Y+s.H-unit*3, s.W/8, unit, unit/2, WithAlpha(ink, 60))
	}
	strokeRoundedRect(renderer, s.X, s.Y, s.W, s.H, 10, 1, glassA(40))
}
