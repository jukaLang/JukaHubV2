package main

// site_videos_scene.go — the full-screen video site scene. A site tile opens
// this scene the way the YouTube tile opens the Media screen: a page of the
// app, not a popup over Home.

import (
	"image"
	"image/draw"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
)

var siteThumbCache struct {
	mu       sync.Mutex
	textures map[string]*sdl.Texture
	failed   map[string]bool
}

// siteThumbTexture decodes a cached poster once and keeps its texture for the
// life of the scene. The file was downloaded off the render thread.
func siteThumbTexture(renderer *sdl.Renderer, path string) *sdl.Texture {
	if path == "" {
		return nil
	}
	siteThumbCache.mu.Lock()
	defer siteThumbCache.mu.Unlock()
	if siteThumbCache.textures == nil {
		siteThumbCache.textures = map[string]*sdl.Texture{}
		siteThumbCache.failed = map[string]bool{}
	}
	if tex := siteThumbCache.textures[path]; tex != nil {
		return tex
	}
	if siteThumbCache.failed[path] {
		return nil
	}
	pic, err := imgvDecodeSDL(path)
	if err != nil {
		siteThumbCache.failed[path] = true
		return nil
	}
	bounds := pic.Bounds()
	nrgba := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(nrgba, nrgba.Bounds(), pic, bounds.Min, draw.Src)
	tex := imgvTexture(renderer, nrgba)
	if tex == nil {
		siteThumbCache.failed[path] = true
		return nil
	}
	siteThumbCache.textures[path] = tex
	return tex
}

const siteVideosSceneName = "SiteVideos"

func init() {
	registerScene(siteVideosScene{sceneBase{name: siteVideosSceneName}})
}

type siteVideosScene struct{ sceneBase }

// ensureSiteVideosScene adds the scene in memory; shipped configs never carry
// it and the user's config file is not rewritten. Called at startup and after
// a config reload, like the Tools hub.
func ensureSiteVideosScene(config *Config) {
	if config == nil || sceneIndexExact(config, siteVideosSceneName) >= 0 {
		return
	}
	config.Scenes = append(config.Scenes, SceneConfig{
		Name:        siteVideosSceneName,
		Description: "Videos from a site you added",
	})
}

func (siteVideosScene) render(r *sdl.Renderer, c *Config) bool {
	renderSiteVideosScene(r, c)
	return true
}

func (siteVideosScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return siteVideosSceneKey(e, c)
}

func (siteVideosScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return siteVideosSceneController(e, c)
}

func siteVideosSceneKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		siteVideosMove(-max(1, siteVideoUI.cols))
	case sdl.K_DOWN:
		siteVideosMove(max(1, siteVideoUI.cols))
	case sdl.K_LEFT:
		siteVideosMove(-1)
	case sdl.K_RIGHT:
		siteVideosMove(1)
	case sdl.K_RETURN, sdl.K_SPACE, sdl.K_a:
		siteVideosActivate()
	case sdl.K_y:
		// Search belongs to this site, so its domain is already the target.
		siteVideosSearch()
	case sdl.K_x:
		vkOpenOtherVideosPlay()
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		goBackScene(c)
	}
	return true
}

func siteVideosSceneController(e *sdl.ControllerButtonEvent, c *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		siteVideosMove(-max(1, siteVideoUI.cols))
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		siteVideosMove(max(1, siteVideoUI.cols))
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		siteVideosMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		siteVideosMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		siteVideosActivate()
	case sdl.CONTROLLER_BUTTON_Y:
		siteVideosSearch()
	case sdl.CONTROLLER_BUTTON_X:
		vkOpenOtherVideosPlay()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		goBackScene(c)
	}
	return true
}

// siteVideosSearch opens the keyboard with this site already selected.
func siteVideosSearch() {
	otherVideosSite = siteVideoUI.site
	vkOpenVideoSearch()
}

// siteVideosHandleClick is called from the main mouse dispatcher while this
// scene is on screen. The search bar opens the keyboard; a card selects, and a
// second click on the same card plays it, like the game library.
func siteVideosHandleClick(mx, my int32, config *Config) bool {
	if currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) ||
		config.Scenes[currentSceneIndex].Name != siteVideosSceneName {
		return false
	}
	if siteVideoUI.searchRect.W > 0 && pointInRect(mx, my, siteVideoUI.searchRect) {
		siteVideosSearch()
		return true
	}
	for i, rect := range siteVideoUI.cardRects {
		if !pointInRect(mx, my, rect) {
			continue
		}
		idx := siteVideoUI.cardFirst + i
		if idx < 0 || idx >= len(siteVideoUI.entries) {
			return true
		}
		if idx == siteVideoUI.cursor && idx == siteVideoUI.lastClick {
			siteVideoUI.lastClick = -1
			siteVideosActivate()
		} else {
			siteVideoUI.cursor = idx
			siteVideoUI.lastClick = idx
			PlayNavSound()
		}
		return true
	}
	return false
}

func renderSiteVideosScene(renderer *sdl.Renderer, config *Config) {
	site := siteVideoUI.site
	if renderer == nil || site.Host == "" {
		return
	}
	title := site.Name
	if title == "" {
		title = site.Host
	}
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	header := tbxHeader(title, "Videos from "+site.Host, "youtube", "", ColorTextTertiary())
	frame := tbxBeginFrame(renderer, config, header)
	body := frame.bodyRect()

	// Search bar, like the Media tab's: one line that opens the site search
	// on Y or takes a pasted link on X.
	barH := fonts.bodyH + 16
	fillRoundedRect(renderer, body.X, body.Y, body.W, barH, barH/2, WithAlpha(ColorSurfaceRow, 190))
	strokeRoundedRect(renderer, body.X, body.Y, body.W, barH, barH/2, 1, ColorBorderDefault)
	siteVideoUI.searchRect = sdl.Rect{X: body.X, Y: body.Y, W: body.W, H: barH}
	siteVideoUI.cardRects = siteVideoUI.cardRects[:0]
	siteVideoUI.cardFirst = 0
	prompt := "Search videos or paste a link"
	if siteVideoUI.query != "" && siteVideoUI.query != "trending" {
		prompt = "Search: " + siteVideoUI.query
	}
	renderText(renderer, config, fonts.body, ellipsize(fonts.body, prompt, body.W-24),
		ColorTextTertiary(), body.X+12, body.Y+8)

	// Section chips with counts, from the same entries the grid draws.
	chipsY := body.Y + barH + 12
	chipsH := fonts.smallH + 14
	counts := map[string]int{}
	for _, v := range siteVideoUI.entries {
		counts[v.Section]++
	}
	chipTexts := []string{
		"Recommended " + itoa(counts["Recommended"]),
		"Downloads " + itoa(counts["Downloaded"]),
		"Favorites " + itoa(counts["Favorites"]),
		"History " + itoa(counts["History"]),
	}
	cx := body.X
	for _, text := range chipTexts {
		cw, ch, _ := fonts.small.SizeUTF8(text)
		chipW, chipH := int32(cw)+24, int32(ch)+14
		if cx+chipW > body.X+body.W {
			break
		}
		fillRoundedRect(renderer, cx, chipsY, chipW, chipH, chipH/2, WithAlpha(ColorSurfaceRow, 170))
		renderText(renderer, config, fonts.small, text, ColorTextSecondary(), cx+12, chipsY+7)
		cx += chipW + 10
	}

	if siteVideoUI.loading {
		renderText(renderer, config, fonts.body, "Loading videos…", ColorTextSecondary(),
			body.X+8, chipsY+chipsH+16)
	}
	if siteVideoUI.err != "" {
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, siteVideoUI.err, body.W-16),
			ColorTextTertiary(), body.X+8, chipsY+chipsH+16)
	}

	// Card grid: a dark art tile, the title and a short meta line.
	const cardW, cardH, gap = int32(220), int32(150), int32(14)
	gridY := chipsY + chipsH + 14
	cols := int((body.W + gap) / (cardW + gap))
	if cols < 1 {
		cols = 1
	}
	siteVideoUI.cols = cols
	rowsVisible := int((body.Y + body.H - gridY - 34) / (cardH + gap))
	if rowsVisible < 1 {
		rowsVisible = 1
	}
	firstRow := 0
	if siteVideoUI.cursor >= 0 {
		firstRow = siteVideoUI.cursor / cols
		if firstRow+rowsVisible <= siteVideoUI.cursor/cols {
			firstRow = siteVideoUI.cursor/cols - rowsVisible + 1
		}
	}
	if firstRow < 0 {
		firstRow = 0
	}
	siteVideoUI.cardFirst = firstRow * cols
	for i, v := range siteVideoUI.entries {
		row, col := i/cols, i%cols
		if row < firstRow || row >= firstRow+rowsVisible {
			continue
		}
		x := body.X + int32(col)*(cardW+gap)
		y := gridY + int32(row-firstRow)*(cardH+gap)
		rect := sdl.Rect{X: x, Y: y, W: cardW, H: cardH}
		siteVideoUI.cardRects = append(siteVideoUI.cardRects, sdl.Rect{X: rect.X, Y: rect.Y, W: rect.W, H: rect.H + 34})
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, WithAlpha(ColorSurfaceRow, 205))
		selected := i == siteVideoUI.cursor
		if selected {
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, 2, installHighlight())
		} else {
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, 1, ColorBorderDefault)
		}
		art := sdl.Rect{X: x + 8, Y: y + 8, W: cardW - 16, H: cardH - 56}
		fillRoundedRect(renderer, art.X, art.Y, art.W, art.H, 8, WithAlpha(ColorPanelRaised, 230))
		if tex := siteThumbTexture(renderer, v.Thumb); tex != nil {
			_, _, tw, th, err := tex.Query()
			if err != nil || tw <= 0 || th <= 0 {
				continue
			}
			w, h := imgvFitInside(int(tw), int(th), int(art.W), int(art.H))
			dst := sdl.Rect{
				X: art.X + (art.W-int32(w))/2, Y: art.Y + (art.H-int32(h))/2,
				W: int32(w), H: int32(h),
			}
			_ = renderer.Copy(tex, nil, &dst)
		}
		renderText(renderer, config, fonts.body, ellipsize(fonts.body, v.Title, cardW-16), ColorTextPrimary(),
			x+8, y+cardH-42)
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, v.Label, cardW-16), ColorTextTertiary(),
			x+8, y+cardH-22)
	}
	if len(siteVideoUI.entries) == 0 && !siteVideoUI.loading {
		renderText(renderer, config, fonts.body, "No videos from this site are saved, favourited or watched yet.",
			ColorTextSecondary(), body.X+8, gridY+8)
	}

	footer := hintLine(
		FooterHint{"Arrows", "Pick"}, FooterHint{"A", "Play"},
		FooterHint{"Y", "Search"}, FooterHint{"X", "Paste link"}, FooterHint{"B", "Back"})
	fw, _, _ := fonts.small.SizeUTF8(footer)
	renderText(renderer, config, fonts.small, footer, ColorTextTertiary(),
		body.X+(body.W-int32(fw))/2, body.Y+body.H-24)
	tbxEndFrame(renderer, config)
}
