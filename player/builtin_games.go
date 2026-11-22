package main

// builtin_games.go — the games that ship inside JukaHub (JukaLand, the Synth,
// Solitaire and the Games screen's arcade minigames: Snake, Tetris, 2048,
// Pong, Minesweeper, Flappy, Brick Breaker) as the Games library lists them, and their
// covers.
//
// No thumbnail server has a box for these, so their covers are drawn by
// branding/mkcovers.py and embedded here. The library shows a cover by file
// path (artTexture, the background decoder, the preview pane), so the
// embedded PNGs are copied once into the data directory and the entries point
// at those copies; the Games screen's cards draw the embedded bytes directly.

import (
	"bytes"
	"embed"
	"os"
	"path/filepath"
	"sync"

	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"
)

const (
	builtInLibrarySystem = "Built-in games"
	jukaLandLibraryPath  = "builtin:jukaland"
	synthLibraryPath     = "builtin:synth"
	solitaireLibraryPath = "builtin:solitaire"
)

//go:embed builtin_covers/*.png
var builtinCovers embed.FS

// builtInLibraryGame is one game JukaHub carries itself: the scene that plays
// it, its library title and detail line, the glyph drawn when no cover can be
// shown, and its embedded cover. An arcade minigame (games.go) is played on
// the Games screen, so its scene is "Games" and arcade says which game to
// start there; a game with a scene of its own has gameNone.
type builtInLibraryGame struct {
	path   string
	scene  string
	arcade gameID
	title  string
	detail string
	glyph  string
	cover  string // file name under builtin_covers/
}

// builtInLibraryGames is the order the "Built-in games" console lists them in.
var builtInLibraryGames = []builtInLibraryGame{
	{path: jukaLandLibraryPath, scene: "JukaLand", title: "JukaLand", detail: "Built-in JukaHub game", glyph: "jukaland", cover: "jukaland.png"},
	{path: synthLibraryPath, scene: synthSceneName, title: "Synth", detail: "Built-in groovebox: make music", glyph: "synth", cover: "synth.png"},
	{path: solitaireLibraryPath, scene: solitaireSceneName, title: "Solitaire", detail: "Built-in card game: Klondike", glyph: "cards", cover: "solitaire.png"},
	{path: "builtin:snake", scene: "Games", arcade: gameSnake, title: "Snake", detail: "Built-in arcade game: eat, grow, survive", glyph: "arcade", cover: "snake.png"},
	{path: "builtin:tetris", scene: "Games", arcade: gameTetris, title: "Tetris", detail: "Built-in arcade game: stack blocks, clear lines", glyph: "arcade", cover: "tetris.png"},
	{path: "builtin:2048", scene: "Games", arcade: game2048, title: "2048", detail: "Built-in puzzle: merge tiles, reach 2048", glyph: "arcade", cover: "2048.png"},
	{path: "builtin:pong", scene: "Games", arcade: gamePong, title: "Pong", detail: "Built-in arcade game: classic paddle ball", glyph: "arcade", cover: "pong.png"},
	{path: "builtin:minesweeper", scene: "Games", arcade: gameMinesweeper, title: "Minesweeper", detail: "Built-in puzzle: flag mines, clear the board", glyph: "arcade", cover: "minesweeper.png"},
	{path: "builtin:flappy", scene: "Games", arcade: gameFlappy, title: "Flappy", detail: "Built-in arcade game: fly through the pipes", glyph: "arcade", cover: "flappy.png"},
	{path: "builtin:brickbreaker", scene: "Games", arcade: gameBrickBreaker, title: "Brick Breaker", detail: "Built-in arcade game: break the wall", glyph: "arcade", cover: "brickbreaker.png"},
}

// builtInLibraryGameFor reports which built-in game a library entry is.
func builtInLibraryGameFor(game libraryGame) (builtInLibraryGame, bool) {
	if game.System != builtInLibrarySystem {
		return builtInLibraryGame{}, false
	}
	for _, b := range builtInLibraryGames {
		if b.path == game.Path {
			return b, true
		}
	}
	return builtInLibraryGame{}, false
}

func isBuiltInLibraryGame(game libraryGame) bool {
	_, ok := builtInLibraryGameFor(game)
	return ok
}

// builtInCoverData is an embedded cover's bytes by file name, nil for none.
func builtInCoverData(cover string) []byte {
	if cover == "" {
		return nil
	}
	data, err := builtinCovers.ReadFile("builtin_covers/" + cover)
	if err != nil {
		return nil
	}
	return data
}

// builtInMenuCover is the embedded cover file of a Games screen card: an
// arcade minigame's by its id, a scene game's by its scene; "" for none. The
// Games screen's own scene has no cover: each of its minigames carries one.
func builtInMenuCover(g gameEntry) string {
	for _, b := range builtInLibraryGames {
		if g.scene != "" && b.arcade == gameNone && b.scene == g.scene {
			return b.cover
		}
		if g.scene == "" && g.id != gameNone && b.arcade == g.id {
			return b.cover
		}
	}
	return ""
}

// builtInCoverRoot is where the covers are copied for the library, "" when
// there is no data directory (the cards then draw the glyph). Not inside
// game-art: the art cleanup (artcleanup.go) would take a folder there that no
// console scanned for as an orphan.
func builtInCoverRoot() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "builtin-covers")
}

var builtInCoverInstall struct {
	mu   sync.Mutex
	done map[string]bool // roots already written this run
}

// installBuiltInCovers writes each embedded cover under root, replacing a copy
// whose bytes differ (a JukaHub update redraws a cover), and returns the paths
// by library path. A cover that cannot be written is left out, so its card
// falls back to the glyph. Once per root and process: a scan runs often.
func installBuiltInCovers(root string) map[string]string {
	out := map[string]string{}
	if root == "" {
		return out
	}
	builtInCoverInstall.mu.Lock()
	defer builtInCoverInstall.mu.Unlock()
	if builtInCoverInstall.done == nil {
		builtInCoverInstall.done = map[string]bool{}
	}
	already := builtInCoverInstall.done[root]
	for _, b := range builtInLibraryGames {
		target := filepath.Join(root, b.cover)
		if !already {
			data, err := builtinCovers.ReadFile("builtin_covers/" + b.cover)
			if err != nil {
				continue
			}
			if old, rerr := os.ReadFile(target); rerr != nil || !bytes.Equal(old, data) {
				if err := os.MkdirAll(root, 0o755); err != nil {
					logDebug("art", "built-in covers: %v", err)
					continue
				}
				if err := AtomicWrite(target, data, 0o644); err != nil {
					logDebug("art", "built-in cover %s: %v", b.cover, err)
					continue
				}
			}
		}
		if _, err := os.Stat(target); err == nil {
			out[b.path] = target
		}
	}
	// A failed write (the data folder not ready yet) is tried again next scan.
	if len(out) == len(builtInLibraryGames) {
		builtInCoverInstall.done[root] = true
	}
	return out
}

// libraryAddBuiltInGames lists the built-in games whose scene this
// configuration has, so the library shows them even on a card with no ROMs.
// hasScene is answered before the background scan starts (it reads the
// config); coverRoot is where their covers are installed ("" for none).
func libraryAddBuiltInGames(lib *gameLibraryReport, hasScene func(string) bool, coverRoot string) {
	if lib == nil || hasScene == nil {
		return
	}
	covers := installBuiltInCovers(coverRoot)
	var games []libraryGame
	for _, b := range builtInLibraryGames {
		if !hasScene(b.scene) {
			continue
		}
		art := covers[b.path]
		games = append(games, libraryGame{Path: b.path, System: builtInLibrarySystem, Title: b.title, Art: art, HasArt: art != ""})
	}
	if len(games) == 0 {
		return
	}
	lib.Systems = append(lib.Systems, librarySystem{Name: builtInLibrarySystem, Games: games, WithArt: countCoveredGames(games)})
	lib.Total += len(games)
	sortLibrarySystems(lib.Systems)
}

// builtInScenesPresent snapshots which built-in games' scenes config has, for
// a scan that runs off the main goroutine.
func builtInScenesPresent(config *Config) func(string) bool {
	have := map[string]bool{}
	for _, b := range builtInLibraryGames {
		if config != nil && sceneIndexExact(config, b.scene) >= 0 {
			have[b.scene] = true
		}
	}
	return func(scene string) bool { return have[scene] }
}

// ── Covers on the Games screen ────────────────────────────────────────────

var builtInCoverTex struct {
	owner *sdl.Renderer
	tex   map[string]*sdl.Texture // cover file → texture (nil = failed, not retried)
}

// builtInCoverTexture is an embedded cover (builtInMenuCover's file name) as a
// texture of renderer, nil when there is none or it cannot be loaded.
// Textures belong to the renderer they were made on: a new renderer starts
// the cache over.
func builtInCoverTexture(renderer *sdl.Renderer, cover string) *sdl.Texture {
	if renderer == nil || cover == "" {
		return nil
	}
	if builtInCoverTex.owner != renderer {
		// The old renderer's textures went with it; only forget them.
		builtInCoverTex.owner = renderer
		builtInCoverTex.tex = map[string]*sdl.Texture{}
	}
	if tex, ok := builtInCoverTex.tex[cover]; ok {
		return tex
	}
	var tex *sdl.Texture
	if data := builtInCoverData(cover); len(data) > 0 {
		if rw, err := sdl.RWFromMem(data); err == nil {
			tex, _ = img.LoadTextureRW(renderer, rw, false)
			rw.Close()
		}
	}
	builtInCoverTex.tex[cover] = tex
	return tex
}

// builtInCoverArtShare is the top part of a built-in cover that holds the
// picture: mkcovers.py puts the title pill below 246 of its 300 rows. The
// Games screen's cards print the name themselves, so they leave the pill out.
const builtInCoverArtShare = 0.8

// drawCoverFill draws tex over rect, cropped to rect's shape (no bars).
func drawCoverFill(renderer *sdl.Renderer, tex *sdl.Texture, rect sdl.Rect) bool {
	return drawCoverTopFill(renderer, tex, rect, 1)
}

// drawCoverTopFill is drawCoverFill over the top share (0 < share <= 1) of
// tex's height only.
func drawCoverTopFill(renderer *sdl.Renderer, tex *sdl.Texture, rect sdl.Rect, share float64) bool {
	if tex == nil || rect.W <= 0 || rect.H <= 0 {
		return false
	}
	_, _, tw, th, err := tex.Query()
	if err != nil || tw <= 0 || th <= 0 {
		return false
	}
	if share > 0 && share < 1 {
		th = max(1, int32(float64(th)*share))
	}
	src := sdl.Rect{W: tw, H: th}
	// Keep the whole height when the texture is wider than rect, else the
	// whole width; centre the crop.
	if int64(tw)*int64(rect.H) > int64(th)*int64(rect.W) {
		src.W = int32(int64(th) * int64(rect.W) / int64(rect.H))
		src.X = (tw - src.W) / 2
	} else {
		src.H = int32(int64(tw) * int64(rect.H) / int64(rect.W))
		src.Y = (th - src.H) / 2
	}
	return renderer.Copy(tex, &src, &rect) == nil
}
