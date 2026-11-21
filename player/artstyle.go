package main

// artstyle.go — Settings → Games & emulators → Game art (the "Game art" rows
// and art toggles).
//
// The library's list layout shows the focused game's picture beside the list.
// These rows decide what that picture is and how it is drawn:
//
//   - Game art: Off leaves the list text-only (no preview, no background).
//   - Art type: Box art (default), Screenshot or Title screen. A game without
//     the chosen picture shows its box art, as before.
//   - Art style: Thumbnail (default, the preview beside the list) or
//     Background: the focused game's picture fills the page behind
//     the list, full height and dimmed, and the list takes the whole width.
//   - Art width: the preview's share of the page, Standard (34 %, a square
//     the page height may cap — the look before this setting) or 30–60 %.
//     A wider frame than the page is tall gives a landscape screenshot room.
//   - Art corner radius: 0 (square, the look before) to 24 px.
//   - Console backgrounds: a console's own picture behind its game list —
//     JukaHub's data/backgrounds/<System>.png (spaces as "_", or as written;
//     .png or .jpg) or NextUI's Roms/<console folder>/.media/bg.png —
//     drawn cover-fit and dimmed with the theme's base colour.
//
// Where the other pictures live: box art stays where it always was
// (data/game-art/<System>/<stem>.png, or the card's own .media / Imgs / .res
// picture, which counts as box art). Screenshots and title screens go in
// hidden folders of the game-art folder, named like the box art:
//
//	data/game-art/.snap/<System>/<stem>.png    libretro Named_Snaps
//	data/game-art/.title/<System>/<stem>.png   libretro Named_Titles
//
// Dot folders are never a console to the cover cleanup (artcleanup.go), so
// they are never "unused covers" wholesale; the cleanup looks inside them
// with the same rules as the box art (artStyleCleanupFind). Remove cover
// moves them with the box art, Rename file and the GBC sort carry them, Delete
// game takes them (artStyleKindPaths). The downloads are artstyle_fetch.go;
// the Artwork Manager screen is artmanager.go.
//
// Nothing here decodes on the render thread: the focused game's picture goes
// through the library's own one-at-a-time decoder and texture cache
// (libraryArtTexture), and a console background through a small cache of its
// own whose decode runs on a goroutine (artStyleBgTexture).

import (
	"context"
	"image"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// Settings keys (Custom variables, written by the Settings rows).
	artStyleOnKey        = "gameArt"
	artStyleTypeKey      = "gameArtType"
	artStyleStyleKey     = "gameArtStyle"
	artStyleWidthKey     = "gameArtWidth"
	artStyleRadiusKey    = "gameArtRadius"
	artStyleConsoleBgKey = "consoleBackgrounds"

	artTypeBox   = "box"
	artTypeSnap  = "snap"
	artTypeTitle = "title"

	artStyleThumbnail  = "thumbnail"
	artStyleBackground = "background"

	// artStyleSnapDir and artStyleTitleDir are the hidden folders of the
	// game-art folder the screenshots and title screens go in.
	artStyleSnapDir  = ".snap"
	artStyleTitleDir = ".title"

	// artStyleStandardWidth is the preview's share of the page before the
	// Art width row existed (renderGameLibraryGames).
	artStyleStandardWidth = 34
)

// artStyleTypeOptions, artStyleModeOptions, artStyleWidthOptions and
// artStyleRadiusOptions are the rows' stops, in the order Left/Right step.
var (
	artStyleTypeOptions = []setOption{
		{Value: artTypeBox, Label: "Box art"},
		{Value: artTypeSnap, Label: "Screenshot"},
		{Value: artTypeTitle, Label: "Title screen"},
	}
	artStyleModeOptions = []setOption{
		{Value: artStyleThumbnail, Label: "Thumbnail"},
		{Value: artStyleBackground, Label: "Background"},
	}
	artStyleWidthOptions = []setOption{
		{Value: "0", Label: "Standard"},
		{Value: "30", Label: "30%"},
		{Value: "35", Label: "35%"},
		{Value: "40", Label: "40%"},
		{Value: "45", Label: "45%"},
		{Value: "50", Label: "50%"},
		{Value: "55", Label: "55%"},
		{Value: "60", Label: "60%"},
	}
	artStyleRadiusOptions = []setOption{
		{Value: "0", Label: "Square"},
		{Value: "4", Label: "4 px"},
		{Value: "8", Label: "8 px"},
		{Value: "12", Label: "12 px"},
		{Value: "16", Label: "16 px"},
		{Value: "20", Label: "20 px"},
		{Value: "24", Label: "24 px"},
	}
)

// ── The settings ─────────────────────────────────────────────────────────

// artStyleVars is the Custom map the settings live in (appConfig's when c is
// nil: the library draws without a config at hand).
func artStyleVars(c *Config) map[string]interface{} {
	if c == nil {
		c = appConfig
	}
	if c == nil {
		return nil
	}
	return c.Variables.Custom
}

// artStyleArtOn is Game art: on unless switched off.
func artStyleArtOn(c *Config) bool {
	return customBool(artStyleVars(c), artStyleOnKey, true)
}

// artStyleType is the chosen Art type, box art for anything unknown.
func artStyleType(c *Config) string {
	switch v := strings.ToLower(strings.TrimSpace(customStringValue(artStyleVars(c), artStyleTypeKey))); v {
	case artTypeSnap, artTypeTitle:
		return v
	}
	return artTypeBox
}

// artStyleMode is the chosen Art style, Thumbnail for anything unknown.
func artStyleMode(c *Config) string {
	if strings.EqualFold(strings.TrimSpace(customStringValue(artStyleVars(c), artStyleStyleKey)), artStyleBackground) {
		return artStyleBackground
	}
	return artStyleThumbnail
}

// artStyleWidthPct is the chosen Art width in percent, 0 for Standard.
func artStyleWidthPct(c *Config) int {
	n, err := strconv.Atoi(strings.TrimSpace(customStringValue(artStyleVars(c), artStyleWidthKey)))
	if err != nil || n < 30 || n > 60 {
		return 0
	}
	return n - n%5 // a hand-edited 42 reads as the 40 % stop
}

// artStyleRadius is the chosen Art corner radius in pixels, 0 to 24.
func artStyleRadius(c *Config) int32 {
	n, err := strconv.Atoi(strings.TrimSpace(customStringValue(artStyleVars(c), artStyleRadiusKey)))
	if err != nil || n <= 0 {
		return 0
	}
	n = min(n, 24)
	return int32(n - n%4) // the 4 px stops
}

// artStyleConsoleBgOn is Console backgrounds: on unless switched off.
func artStyleConsoleBgOn(c *Config) bool {
	return customBool(artStyleVars(c), artStyleConsoleBgKey, true)
}

// artStyleOptionIndex is value's place in options, -1 when it is not a stop.
func artStyleOptionIndex(options []setOption, value string) int {
	for i, o := range options {
		if o.Value == value {
			return i
		}
	}
	return -1
}

// artStyleOptionLabel is value's label in options.
func artStyleOptionLabel(options []setOption, value string) string {
	if i := artStyleOptionIndex(options, value); i >= 0 {
		return options[i].Label
	}
	return value
}

// artStyleTypeNoun is what the type's pictures are called in a sentence.
func artStyleTypeNoun(kind string, plural bool) string {
	switch kind {
	case artTypeSnap:
		if plural {
			return "screenshots"
		}
		return "screenshot"
	case artTypeTitle:
		if plural {
			return "title screens"
		}
		return "title screen"
	}
	if plural {
		return "covers"
	}
	return "cover"
}

// artStyleRow is one row of the Game art group by id — gameArt,
// gameArtType, gameArtStyle, gameArtWidth, gameArtRadius,
// consoleBackgrounds, artManager — so setmodel.go can list them one by one
// inside the Games & emulators literal. An unknown id is a read-out saying so
// rather than a crash.
func artStyleRow(id string) setRow {
	for _, row := range artStyleSettingsRows() {
		if row.ID == id {
			return row
		}
	}
	return setRow{ID: id, Label: id, Kind: setKindInfo, Detail: setStatic("Unknown Game art row")}
}

// artStyleSettingsRows is the Game art group of Settings → Games & emulators.
func artStyleSettingsRows() []setRow {
	thumbOnly := func(c *Config) bool { return !artStyleArtOn(c) || artStyleMode(c) == artStyleBackground }
	return []setRow{
		{
			ID: "gameArt", Label: "Game art", Kind: setKindToggle,
			Detail: func(c *Config) string {
				if artStyleArtOn(c) {
					return "Games library list: the focused game's picture beside or behind the list"
				}
				return "Off: the library's list is text only"
			},
			Get: artStyleArtOn,
			Set: func(c *Config, on bool) {
				setStoreBool(c, artStyleOnKey, on)
				setToast("Game art: " + setOnOff(on))
			},
		},
		{
			ID: "gameArtType", Label: "Art type", Kind: setKindChoice,
			Detail: func(c *Config) string {
				if !artStyleArtOn(c) {
					return "Game art is off"
				}
				if artStyleType(c) == artTypeBox {
					return "The box cover; NextUI's .media pictures count as box art"
				}
				return "Fill gaps and the Artwork Manager fetch them; a game without one shows its box art"
			},
			Disabled: func(c *Config) bool { return !artStyleArtOn(c) },
			Options:  func(*Config) []setOption { return artStyleTypeOptions },
			Current:  func(c *Config) int { return artStyleOptionIndex(artStyleTypeOptions, artStyleType(c)) },
			Choose: func(c *Config, value string) {
				setStoreString(c, artStyleTypeKey, value)
				artStylePickReset()
				setToast("Art type: " + artStyleOptionLabel(artStyleTypeOptions, value))
			},
		},
		{
			ID: "gameArtStyle", Label: "Art style", Kind: setKindChoice,
			Detail: func(c *Config) string {
				if !artStyleArtOn(c) {
					return "Game art is off"
				}
				if artStyleMode(c) == artStyleBackground {
					return "The focused game's picture fills the page behind the list, dimmed"
				}
				return "The focused game's picture beside the list"
			},
			Disabled: func(c *Config) bool { return !artStyleArtOn(c) },
			Options:  func(*Config) []setOption { return artStyleModeOptions },
			Current:  func(c *Config) int { return artStyleOptionIndex(artStyleModeOptions, artStyleMode(c)) },
			Choose: func(c *Config, value string) {
				setStoreString(c, artStyleStyleKey, value)
				setToast("Art style: " + artStyleOptionLabel(artStyleModeOptions, value))
			},
		},
		{
			ID: "gameArtWidth", Label: "Art width", Kind: setKindChoice, Clamp: true,
			Detail: func(c *Config) string {
				if thumbOnly(c) {
					return "For the Thumbnail style"
				}
				return "The picture's share of the page beside the list"
			},
			Disabled: thumbOnly,
			Options:  func(*Config) []setOption { return artStyleWidthOptions },
			Current: func(c *Config) int {
				return artStyleOptionIndex(artStyleWidthOptions, strconv.Itoa(artStyleWidthPct(c)))
			},
			Choose: func(c *Config, value string) {
				setStoreString(c, artStyleWidthKey, value)
				setToast("Art width: " + artStyleOptionLabel(artStyleWidthOptions, value))
			},
		},
		{
			ID: "gameArtRadius", Label: "Art corner radius", Kind: setKindChoice, Clamp: true,
			Detail: func(c *Config) string {
				if thumbOnly(c) {
					return "For the Thumbnail style"
				}
				return "How round the picture's corners are"
			},
			Disabled: thumbOnly,
			Options:  func(*Config) []setOption { return artStyleRadiusOptions },
			Current: func(c *Config) int {
				return artStyleOptionIndex(artStyleRadiusOptions, strconv.Itoa(int(artStyleRadius(c))))
			},
			Choose: func(c *Config, value string) {
				setStoreString(c, artStyleRadiusKey, value)
				setToast("Art corner radius: " + artStyleOptionLabel(artStyleRadiusOptions, value))
			},
		},
		{
			ID: "consoleBackgrounds", Label: "Console backgrounds", Kind: setKindToggle,
			Detail: func(c *Config) string {
				if artStyleConsoleBgOn(c) {
					return "A console's own picture behind its games: data/backgrounds or NextUI's .media/bg.png"
				}
				return "Off: every console's games sit on the Home backdrop"
			},
			Get: artStyleConsoleBgOn,
			Set: func(c *Config, on bool) {
				setStoreBool(c, artStyleConsoleBgKey, on)
				artStyleBgPathReset()
				setToast("Console backgrounds: " + setOnOff(on))
			},
		},
		artManagerSettingsRow(),
	}
}

// ── Where the other pictures live ────────────────────────────────────────

// artStyleKindDir is the hidden folder of a picture type, "" for box art.
func artStyleKindDir(kind string) string {
	switch kind {
	case artTypeSnap:
		return artStyleSnapDir
	case artTypeTitle:
		return artStyleTitleDir
	}
	return ""
}

// artStyleKinds are the picture types that have a folder of their own.
var artStyleKinds = [...]string{artTypeSnap, artTypeTitle}

// artStyleKindPath is where rom's picture of kind goes:
// <root>/.snap/<System>/<stem>.png or <root>/.title/<System>/<stem>.png.
// "" for box art (romArtworkPath) and without an art folder.
func artStyleKindPath(rom scannedROM, root, kind string) string {
	dir := artStyleKindDir(kind)
	if dir == "" || strings.TrimSpace(root) == "" {
		return ""
	}
	return romArtworkPath(rom, filepath.Join(root, dir))
}

// artStyleKindPaths are rom's screenshot and title-screen files, present or
// not, for the code that moves or deletes a game's pictures.
func artStyleKindPaths(rom scannedROM, root string) []string {
	var out []string
	for _, kind := range artStyleKinds {
		if p := artStyleKindPath(rom, root, kind); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// artStyleKindPairs are the (old, new) screenshot and title-screen paths of a
// game that moves from one name or console to another.
func artStyleKindPairs(from, to scannedROM, root string) [][2]string {
	var out [][2]string
	for _, kind := range artStyleKinds {
		a, b := artStyleKindPath(from, root, kind), artStyleKindPath(to, root, kind)
		if a != "" && b != "" {
			out = append(out, [2]string{a, b})
		}
	}
	return out
}

// artStyleKindNames lists one console's folder of kind's pictures by
// lower-case file name. A missing folder gives an empty set.
func artStyleKindNames(root, kind, system string) map[string]bool {
	out := map[string]bool{}
	dir := artStyleKindDir(kind)
	if dir == "" || strings.TrimSpace(root) == "" {
		return out
	}
	entries, err := os.ReadDir(filepath.Join(root, dir, artCleanupDirName(system)))
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			out[strings.ToLower(e.Name())] = true
		}
	}
	return out
}

// artStyleCleanupFind is the cover cleanup's look inside the screenshot and
// title-screen folders: the same rules as for the box art (artCleanupFind),
// labelled by type.
func artStyleCleanupFind(root string, scanned map[string]bool, used map[string]map[string]bool, now time.Time) []artCleanupFile {
	var out []artCleanupFile
	for _, kind := range artStyleKinds {
		for _, f := range artCleanupFind(filepath.Join(root, artStyleKindDir(kind)), scanned, used, now) {
			f.System += " " + artStyleTypeNoun(kind, true)
			out = append(out, f)
		}
	}
	return out
}

// ── Which picture a game shows ───────────────────────────────────────────

// artStylePick remembers, per game, the picture the chosen type resolved to,
// so the render thread stats a file once per game and fetch, not per frame.
// paths maps a game's path to {its Art then, the picture}: a game whose box
// art changed since (Choose cover, a rescan) is looked up again.
var artStylePick struct {
	gen   uint64
	kind  string
	epoch int
	paths map[string][2]string
}

// artStylePickEpoch is bumped when the type changes, so the next frame looks
// again. Render thread.
var artStylePickEpoch int

// artStylePickReset forgets the resolved pictures (a new type, a reset).
func artStylePickReset() { artStylePickEpoch++ }

// artStylePickMax bounds the remembered games.
const artStylePickMax = 4096

// artStyleArtPath is the picture the library draws for game: its box art, or
// the chosen screenshot / title screen when there is one. A game whose cover
// was removed (Art "") stays without a picture. Render thread.
func artStyleArtPath(game libraryGame) string {
	kind := artStyleType(nil)
	if kind == artTypeBox || strings.TrimSpace(game.Art) == "" {
		return game.Art
	}
	gen := artworkFetchGeneration()
	if artStylePick.paths == nil || artStylePick.gen != gen || artStylePick.kind != kind ||
		artStylePick.epoch != artStylePickEpoch || len(artStylePick.paths) >= artStylePickMax {
		artStylePick.paths = map[string][2]string{}
		artStylePick.gen, artStylePick.kind, artStylePick.epoch = gen, kind, artStylePickEpoch
	}
	if p, ok := artStylePick.paths[game.Path]; ok && p[0] == game.Art {
		return p[1]
	}
	p := game.Art
	if rom, ok := gameCoverROM(game); ok {
		if alt := artStyleKindPath(rom, libraryArtworkRoot(), kind); alt != "" && artworkCoverPresent(alt) {
			p = alt
		}
	}
	artStylePick.paths[game.Path] = [2]string{game.Art, p}
	return p
}

// ── The list layout's preview ────────────────────────────────────────────

// artStyleHidesPreview reports the list layout drawn without its preview
// column by choice: Game art off, or the Background style (the backdrop draws
// the picture). The cover layouts keep theirs: they are the covers, and the
// backdrop draws nothing behind them.
func artStyleHidesPreview() bool {
	return (!artStyleArtOn(nil) || artStyleMode(nil) == artStyleBackground) &&
		libraryLayoutMode() == libraryLayoutList
}

// artStylePreviewWidth is the preview column's width for the list layout:
// standard is the width the page worked out (34 %, capped square by the
// height, 0 on a narrow canvas). Off and Background give no column.
func artStylePreviewWidth(body sdl.Rect, standard int32) int32 {
	if artStyleHidesPreview() {
		return 0
	}
	pct := artStyleWidthPct(nil)
	if pct == 0 || standard == 0 || libraryLayoutMode() != libraryLayoutList {
		return standard // the cover layouts keep their preview as it was
	}
	// The list keeps at least 280 px beside the 24 px gap.
	w := min(body.W*int32(pct)/100, body.W-24-280)
	if w < 120 {
		return 0
	}
	return w
}

// artStylePreviewHeight is the preview frame's height: square as before,
// unless a chosen width is wider than the page leaves room for.
func artStylePreviewHeight(artW, bodyH int32) int32 {
	if artW > bodyH-70 && bodyH-70 >= 120 {
		return bodyH - 70
	}
	return artW
}

// artStyleDrawFitted draws a preview picture: dst is the square fit the
// preview worked out; a frame that is not square is fitted again, and the
// corners are rounded by Art corner radius. Square and radius 0 is exactly
// the old single copy.
func artStyleDrawFitted(renderer *sdl.Renderer, tex *sdl.Texture, frame, dst sdl.Rect) {
	if renderer == nil || tex == nil {
		return
	}
	if frame.W != frame.H {
		if _, _, tw, th, err := tex.Query(); err == nil && tw > 0 && th > 0 {
			dst = gaFit(tw, th, frame.W-16, frame.H-16)
			dst.X += frame.X + 8
			dst.Y += frame.Y + 8
		}
	}
	artStyleCopyRounded(renderer, tex, dst, artStyleRadius(nil))
}

// artStyleCopyRounded copies tex onto dst with rounded corners: the corner
// rows are copied one scan line at a time, inset like a rounded rectangle's
// (roundedCornerInset), the middle in one copy. 2·radius+1 copies at most.
func artStyleCopyRounded(renderer *sdl.Renderer, tex *sdl.Texture, dst sdl.Rect, radius int32) {
	if renderer == nil || tex == nil || dst.W <= 0 || dst.H <= 0 {
		return
	}
	radius = min(radius, dst.W/2, dst.H/2)
	if radius <= 0 {
		_ = renderer.Copy(tex, nil, &dst)
		return
	}
	_, _, tw, th, err := tex.Query()
	if err != nil || tw <= 0 || th <= 0 {
		_ = renderer.Copy(tex, nil, &dst)
		return
	}
	srcY := func(y int32) int32 { return int32(int64(y) * int64(th) / int64(dst.H)) }
	srcX := func(x int32) int32 { return int32(int64(x) * int64(tw) / int64(dst.W)) }
	if midH := dst.H - 2*radius; midH > 0 {
		s := sdl.Rect{X: 0, Y: srcY(radius), W: tw, H: max(1, srcY(dst.H-radius)-srcY(radius))}
		d := sdl.Rect{X: dst.X, Y: dst.Y + radius, W: dst.W, H: midH}
		_ = renderer.Copy(tex, &s, &d)
	}
	for dy := int32(0); dy < radius; dy++ {
		inset := roundedCornerInset(radius, dy)
		w := dst.W - 2*inset
		if w <= 0 {
			continue
		}
		for _, y := range [2]int32{dy, dst.H - 1 - dy} {
			s := sdl.Rect{X: srcX(inset), Y: srcY(y), W: max(1, srcX(inset+w)-srcX(inset)), H: max(1, srcY(y+1)-srcY(y))}
			d := sdl.Rect{X: dst.X + inset, Y: dst.Y + y, W: w, H: 1}
			_ = renderer.Copy(tex, &s, &d)
		}
	}
}

// ── Behind the list ──────────────────────────────────────────────────────

// artStyleDrawLibraryBackdrop paints what sits behind a console's page, over
// Home's backdrop and under the header: the console's own background, then —
// in the Background art style and the list layout — the focused game's
// picture. onPage is false on the consoles grid, which keeps Home's look.
func artStyleDrawLibraryBackdrop(renderer *sdl.Renderer, config *Config, system librarySystem, onPage bool, area sdl.Rect) {
	if renderer == nil || !onPage || area.W <= 0 || area.H <= 0 {
		return
	}
	if path := artStyleConsoleBgPath(system); path != "" {
		if tex := artStyleBgTexture(renderer, path); tex != nil && drawCoverFill(renderer, tex, area) {
			artStyleDim(renderer, area, 165)
		}
	}
	if !artStyleArtOn(config) || artStyleMode(config) != artStyleBackground ||
		libraryLayoutMode() != libraryLayoutList || len(system.Games) == 0 {
		return
	}
	cursor := min(max(gameLib.cursor, 0), len(system.Games)-1)
	path := artStyleArtPath(system.Games[cursor])
	if strings.TrimSpace(path) == "" {
		setLibraryArtFocus("")
		return
	}
	// The library's own decoder and cache: one picture at a time, off the
	// render thread, the same texture a thumbnail would have used.
	tex := libraryArtTexture(renderer, path)
	if tex == nil {
		return
	}
	_, _, tw, th, err := tex.Query()
	if err != nil || tw <= 0 || th <= 0 {
		return
	}
	// Full height, against the right edge; a picture wider than the page is
	// cropped to it instead.
	w := int32(int64(tw) * int64(area.H) / int64(th))
	if w >= area.W {
		drawCoverFill(renderer, tex, area)
	} else {
		dst := sdl.Rect{X: area.X + area.W - w, Y: area.Y, W: w, H: area.H}
		_ = renderer.Copy(tex, nil, &dst)
	}
	// Dimmed all over, and more on the left where the titles are.
	artStyleDim(renderer, area, 120)
	const strips = 24
	stripW := max(1, area.W*6/10/strips)
	for i := int32(0); i < strips; i++ {
		alpha := uint8(110 * (strips - i) / strips)
		artStyleDim(renderer, sdl.Rect{X: area.X + i*stripW, Y: area.Y, W: stripW, H: area.H}, alpha)
	}
}

// artStyleDim lays the theme's base colour over rect at alpha.
func artStyleDim(renderer *sdl.Renderer, rect sdl.Rect, alpha uint8) {
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	setDrawColor(renderer, WithAlpha(ColorBackground, alpha))
	_ = renderer.FillRect(&rect)
}

// ── Console backgrounds ──────────────────────────────────────────────────

// artStyleBgPaths remembers each console's background for a while: a stat
// or two per console every half minute, never per frame. Render thread.
var artStyleBgPaths struct {
	at    map[string]time.Time
	paths map[string]string
}

const artStyleBgPathTTL = 30 * time.Second

// artStyleBgPathReset forgets the remembered backgrounds (the switch moved)
// and lets their textures go. Render thread.
func artStyleBgPathReset() {
	artStyleBgPaths.at, artStyleBgPaths.paths = nil, nil
	artStyleBgFree()
}

// artStyleBgFree destroys the uploaded console backgrounds and drops a decoded
// one waiting for upload — a few megabytes — when the library closes
// (closeGameLibraryPanel) or the switch moves. A decode still running lands
// on the next visit. Render thread.
func artStyleBgFree() {
	for _, tex := range artStyleBg.tex {
		if tex != nil {
			tex.Destroy()
		}
	}
	artStyleBg.tex, artStyleBg.order = map[string]*sdl.Texture{}, nil
	artStyleBg.mu.Lock()
	artStyleBg.done, artStyleBg.failed = nil, nil
	artStyleBg.mu.Unlock()
}

// artStyleConsoleBgPath is system's background picture, "" for none or with
// Console backgrounds off. Render thread.
func artStyleConsoleBgPath(system librarySystem) string {
	if !artStyleConsoleBgOn(nil) || system.Name == "" || system.Name == continueSystemName {
		return ""
	}
	now := time.Now()
	if at, ok := artStyleBgPaths.at[system.Name]; ok && now.Sub(at) >= 0 && now.Sub(at) < artStyleBgPathTTL {
		return artStyleBgPaths.paths[system.Name]
	}
	if artStyleBgPaths.at == nil || len(artStyleBgPaths.at) > 256 {
		artStyleBgPaths.at, artStyleBgPaths.paths = map[string]time.Time{}, map[string]string{}
	}
	path := artStyleFindConsoleBg(system)
	artStyleBgPaths.at[system.Name], artStyleBgPaths.paths[system.Name] = now, path
	return path
}

// artStyleFindConsoleBg looks for system's background: JukaHub's
// data/backgrounds/<System>.png first (named like the game-art folder, or as
// the console is written; .png or .jpg), then NextUI's .media/bg.png in the
// console folder the games are in.
func artStyleFindConsoleBg(system librarySystem) string {
	if dir, err := P().DataDir(); err == nil && strings.TrimSpace(dir) != "" {
		for _, name := range []string{artCleanupDirName(system.Name), system.Name} {
			for _, ext := range []string{".png", ".jpg", ".jpeg"} {
				if p := filepath.Join(dir, "backgrounds", name+ext); regularFile(p) {
					return p
				}
			}
		}
	}
	roots := romLibraryRoots()
	seen := map[string]bool{}
	for i, game := range system.Games {
		if i >= 8 {
			break // the console folder is the same for nearly every game
		}
		root := artCleanupRootOf(roots, game.Path)
		if root == "" {
			continue
		}
		rel, err := filepath.Rel(root, game.Path)
		if err != nil || !filepath.IsLocal(rel) {
			continue
		}
		first := strings.Split(filepath.ToSlash(rel), "/")[0]
		console := filepath.Join(root, first)
		if first == "" || seen[console] || console == filepath.Clean(game.Path) {
			continue
		}
		seen[console] = true
		if p := filepath.Join(console, ".media", "bg.png"); regularFile(p) {
			return p
		}
	}
	return ""
}

// artStyleBgMaxEdge bounds a decoded background: sharp on the 1280×720 and
// 1024×768 screens, a few megabytes of texture.
const artStyleBgMaxEdge = 1280

// artStyleBg is the background texture cache: a few uploaded textures on the
// render thread (tex, order, owner) and the one decode a goroutine runs
// (the fields under mu).
var artStyleBg struct {
	mu      sync.Mutex
	pending string
	done    map[string]*image.NRGBA
	failed  map[string]bool

	owner *sdl.Renderer
	tex   map[string]*sdl.Texture
	order []string
}

// artStyleBgKeep is how many background textures stay uploaded.
const artStyleBgKeep = 2

// artStyleBgTexture is path's texture, nil until the goroutine has decoded it
// (or for a picture that cannot be read). Render thread.
func artStyleBgTexture(renderer *sdl.Renderer, path string) *sdl.Texture {
	if renderer == nil || path == "" {
		return nil
	}
	if artStyleBg.owner != renderer {
		// Textures belong to their renderer; a new one starts empty (the old
		// ones went with it, so they are dropped, not destroyed).
		artStyleBg.owner, artStyleBg.tex, artStyleBg.order = renderer, map[string]*sdl.Texture{}, nil
	}
	if tex, ok := artStyleBg.tex[path]; ok {
		return tex
	}
	artStyleBg.mu.Lock()
	pic, ready := artStyleBg.done[path]
	if ready {
		delete(artStyleBg.done, path)
	}
	failed := artStyleBg.failed[path]
	busy := artStyleBg.pending != ""
	if !ready && !failed && !busy {
		artStyleBg.pending = path
	}
	artStyleBg.mu.Unlock()
	switch {
	case ready:
		tex := imgvTexture(renderer, pic)
		artStyleBgRemember(path, tex)
		return tex
	case failed:
		artStyleBgRemember(path, nil)
		return nil
	case busy:
		return nil
	}
	go func() {
		pic, err := artStyleDecodeBg(path)
		artStyleBg.mu.Lock()
		defer artStyleBg.mu.Unlock()
		artStyleBg.pending = ""
		if err != nil || pic == nil {
			if artStyleBg.failed == nil || len(artStyleBg.failed) > 64 {
				artStyleBg.failed = map[string]bool{}
			}
			artStyleBg.failed[path] = true
			return
		}
		artStyleBg.done = map[string]*image.NRGBA{path: pic}
	}()
	return nil
}

// artStyleBgRemember keeps tex (nil: a picture that cannot be read) and
// destroys the oldest past artStyleBgKeep. Render thread.
func artStyleBgRemember(path string, tex *sdl.Texture) {
	artStyleBg.tex[path] = tex
	artStyleBg.order = append(artStyleBg.order, path)
	for len(artStyleBg.order) > artStyleBgKeep {
		oldest := artStyleBg.order[0]
		artStyleBg.order = artStyleBg.order[1:]
		if old := artStyleBg.tex[oldest]; old != nil {
			old.Destroy()
		}
		delete(artStyleBg.tex, oldest)
	}
}

// artStyleDecodeBg decodes a background off the render thread under the
// shared decode limit, shrunk to artStyleBgMaxEdge (decodeLibraryArt's rules
// with a larger edge: a cover is drawn small, a background full screen).
func artStyleDecodeBg(path string) (*image.NRGBA, error) {
	ctx := context.Background()
	if !imageDecodeSem.Acquire(ctx, 1) {
		return nil, ctx.Err()
	}
	defer imageDecodeSem.Release(1)
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var picture image.Image
	if config, _, cfgErr := image.DecodeConfig(file); cfgErr == nil {
		if !libraryArtDimensionsAllowed(config.Width, config.Height) {
			return nil, errLibraryArtDimensions
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		if picture, _, err = image.Decode(file); err != nil {
			return nil, err
		}
	} else if picture, err = imgvDecodeSDL(path); err != nil {
		return nil, cfgErr
	}
	bounds := picture.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if !libraryArtDimensionsAllowed(w, h) {
		return nil, errLibraryArtDimensions
	}
	if edge := max(w, h); edge > artStyleBgMaxEdge {
		scale := float64(artStyleBgMaxEdge) / float64(edge)
		w = max(1, int(math.Round(float64(w)*scale)))
		h = max(1, int(math.Round(float64(h)*scale)))
	}
	return imgvScale(picture, w, h), nil
}
