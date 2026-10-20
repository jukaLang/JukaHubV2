package main

import (
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Games Collection — Snake, Tetris, 2048, Pong
// Accessible from Start Menu or the Games scene
// ──────────────────────────────────────────────────────────────────────

// Game selector
type gameID int

const (
	gameNone gameID = iota
	gameSnake
	gameTetris
	game2048
	gamePong
	gameMinesweeper
	gameFlappy
)

var (
	activeGame    gameID = gameNone
	gameOver      bool
	gameScore     int
	gameStartTime time.Time
)

// ──────────────────────────────────────────────────────────────────────
// Game selector menu
// ──────────────────────────────────────────────────────────────────────

type gameEntry struct {
	name string
	icon string
	desc string
	id   gameID
}

var gameList = []gameEntry{
	{"Snake", "🐍", "Classic snake — eat, grow, survive", gameSnake},
	{"Tetris", "[T]", "Stack blocks, clear lines", gameTetris},
	{"2048", "[2]", "Merge tiles, reach 2048", game2048},
	{"Pong", "[P]", "Classic paddle ball", gamePong},
	{"Minesweeper", "[M]", "Flag mines, clear the board", gameMinesweeper},
	{"Flappy", "[F]", "Tap to fly through pipes", gameFlappy},
}

var gameMenuCursor int

func renderGamesMenu(renderer *sdl.Renderer, config *Config) {
	// Background
	renderer.SetDrawColor(ColorBackground.R, ColorBackground.G, ColorBackground.B, 255)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	renderAmbientBackground(renderer)

	titleFont, _ := getCachedFont(config, "large")
	bodyFont, _ := getCachedFont(config, "medium")
	smallFont, _ := getCachedFont(config, "small")

	// Title
	title := "[G] Games"
	tw, _, _ := titleFont.SizeUTF8(title)
	drawText(renderer, titleFont, title, (screenWidth-int32(tw))/2, 40, getAccentColor(config), textAlignLeft)

	// Game cards — 3x2 grid so all six games fit on the 720p screen.
	const gameCols = 3
	cardW := int32(390)
	cardH := int32(190)
	gap := int32(30)
	startX := (screenWidth - (cardW*gameCols + gap*(gameCols-1))) / 2
	startY := int32(112)

	for i, g := range gameList {
		col := i % gameCols
		row := i / gameCols
		cx := startX + int32(col)*(cardW+gap)
		cy := startY + int32(row)*(cardH+gap)
		focused := i == gameMenuCursor

		// Card background
		bgColor := ColorCard
		if focused {
			bgColor = ColorCardFocus
		}
		fillRoundedRect(renderer, cx, cy, cardW, cardH, 12, bgColor)

		if focused {
			strokeRoundedRect(renderer, cx, cy, cardW, cardH, 12, 2, getAccentColor(config))
		}

		// Icon
		if titleFont != nil {
			iconFont, _ := getCachedFont(config, "large")
			drawText(renderer, iconFont, g.icon, cx+20, cy+20, ColorTextPrimary(), textAlignLeft)
		}

		// Name
		if bodyFont != nil {
			nameCol := ColorTextPrimary()
			if focused {
				nameCol = getAccentColor(config)
			}
			drawText(renderer, bodyFont, g.name, cx+80, cy+24, nameCol, textAlignLeft)
		}

		// Description
		if smallFont != nil {
			desc := ellipsize(smallFont, g.desc, cardW-100)
			drawText(renderer, smallFont, desc, cx+80, cy+56, ColorTextSecondary(), textAlignLeft)
		}

		// "Press A to play" hint
		if focused && smallFont != nil {
			hint := "Press A to play"
			hw, _, _ := smallFont.SizeUTF8(hint)
			drawText(renderer, smallFont, hint, cx+cardW-int32(hw)-20, cy+cardH-32, getAccentColor(config), textAlignLeft)
		}
	}

	// Footer
	if smallFont != nil {
		footer := "↑↓←→ navigate • A select • B back"
		fw, _, _ := smallFont.SizeUTF8(footer)
		drawText(renderer, smallFont, footer, (screenWidth-int32(fw))/2, screenHeight-40, ColorTextTertiary(), textAlignLeft)
	}
}

func handleGamesMenuInput(event *sdl.KeyboardEvent) bool {
	if activeGame != gameNone || gameOver {
		return false
	}
	switch event.Keysym.Sym {
	case sdl.K_UP:
		if gameMenuCursor >= 3 {
			gameMenuCursor -= 3
		}
		PlayNavSound()
	case sdl.K_DOWN:
		if gameMenuCursor+3 < len(gameList) {
			gameMenuCursor += 3
		}
		PlayNavSound()
	case sdl.K_LEFT:
		if gameMenuCursor%3 > 0 {
			gameMenuCursor--
		}
		PlayNavSound()
	case sdl.K_RIGHT:
		if gameMenuCursor%3 < 2 && gameMenuCursor+1 < len(gameList) {
			gameMenuCursor++
		}
		PlayNavSound()
	case sdl.K_RETURN, sdl.K_a:
		startGame(gameList[gameMenuCursor].id)
		PlayActivateSound()
	case sdl.K_ESCAPE, sdl.K_b:
		// Go back to previous scene
		if idx := findSceneIndex(appConfig, "Main"); idx >= 0 {
			changeSceneTo(appConfig, idx)
		}
		PlayBackSound()
	default:
		return false
	}
	return true
}

func startGame(id gameID) {
	activeGame = id
	gameOver = false
	gameScore = 0
	gameStartTime = time.Now()
	switch id {
	case gameSnake:
		initSnake()
	case gameTetris:
		initTetris()
	case game2048:
		init2048()
	case gamePong:
		initPong()
	case gameMinesweeper:
		initMinesweeper()
	case gameFlappy:
		initFlappy()
	}
}

func renderActiveGame(renderer *sdl.Renderer, config *Config) {
	if activeGame == gameNone {
		return
	}

	// Background
	renderer.SetDrawColor(ColorBackground.R, ColorBackground.G, ColorBackground.B, 255)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	switch activeGame {
	case gameSnake:
		renderSnake(renderer, config)
	case gameTetris:
		renderTetris(renderer, config)
	case game2048:
		render2048(renderer, config)
	case gamePong:
		renderPong(renderer, config)
	case gameMinesweeper:
		renderMinesweeper(renderer, config)
	case gameFlappy:
		renderFlappy(renderer, config)
	}
}

func handleActiveGameInput(event *sdl.KeyboardEvent) bool {
	if activeGame == gameNone {
		return false
	}
	switch event.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_q:
		activeGame = gameNone
		PlayBackSound()
		return true
	}
	switch activeGame {
	case gameSnake:
		return handleSnakeInput(event)
	case gameTetris:
		return handleTetrisInput(event)
	case game2048:
		return handle2048Input(event)
	case gamePong:
		return handlePongInput(event)
	case gameMinesweeper:
		return handleMinesweeperInput(event)
	case gameFlappy:
		return handleFlappyInput(event)
	}
	return true
}

func tickActiveGame() {
	if activeGame == gameNone || gameOver {
		return
	}
	switch activeGame {
	case gameSnake:
		tickSnake()
	case gameTetris:
		tickTetris()
	case gamePong:
		tickPong()
	case gameMinesweeper:
		tickMinesweeper()
	case gameFlappy:
		tickFlappy()
	}
}

// ──────────────────────────────────────────────────────────────────────
// SNAKE
// ──────────────────────────────────────────────────────────────────────

const (
	snakeW      = 20
	snakeH      = 15
	snakeCellSz = 28
	snakeSpeed  = 120 // ms per tick
)

type snakeDir int

const (
	snakeUp snakeDir = iota
	snakeDown
	snakeLeft
	snakeRight
)

type snakePoint struct{ x, y int }

var (
	snakeBody    []snakePoint
	snakeCurDir  snakeDir
	snakeFood    snakePoint
	snakeNextDir snakeDir
	snakeTick    uint64
)

func initSnake() {
	midX, midY := snakeW/2, snakeH/2
	snakeBody = []snakePoint{
		{midX, midY}, {midX - 1, midY}, {midX - 2, midY},
	}
	snakeCurDir = snakeRight
	snakeNextDir = snakeRight
	placeSnakeFood()
	snakeTick = 0
}

func placeSnakeFood() {
	for {
		food := snakePoint{rand.Intn(snakeW), rand.Intn(snakeH)}
		occupied := false
		for _, s := range snakeBody {
			if s == food {
				occupied = true
				break
			}
		}
		if !occupied {
			snakeFood = food
			return
		}
	}
}

func handleSnakeInput(event *sdl.KeyboardEvent) bool {
	switch event.Keysym.Sym {
	case sdl.K_UP, sdl.K_w:
		if snakeCurDir != snakeDown {
			snakeNextDir = snakeUp
		}
	case sdl.K_DOWN, sdl.K_s:
		if snakeCurDir != snakeUp {
			snakeNextDir = snakeDown
		}
	case sdl.K_LEFT, sdl.K_a:
		if snakeCurDir != snakeRight {
			snakeNextDir = snakeLeft
		}
	case sdl.K_RIGHT, sdl.K_d:
		if snakeCurDir != snakeLeft {
			snakeNextDir = snakeRight
		}
	case sdl.K_r:
		if gameOver {
			initSnake()
			gameOver = false
			gameScore = 0
		}
	default:
		return false
	}
	return true
}

func tickSnake() {
	now := sdl.GetTicks64()
	if now-snakeTick < uint64(snakeSpeed) {
		return
	}
	snakeTick = now
	snakeCurDir = snakeNextDir

	head := snakeBody[0]
	newHead := head
	switch snakeCurDir {
	case snakeUp:
		newHead.y--
	case snakeDown:
		newHead.y++
	case snakeLeft:
		newHead.x--
	case snakeRight:
		newHead.x++
	}

	// Wall collision
	if newHead.x < 0 || newHead.x >= snakeW || newHead.y < 0 || newHead.y >= snakeH {
		gameOver = true
		return
	}
	// Self collision
	for _, s := range snakeBody {
		if s == newHead {
			gameOver = true
			return
		}
	}

	snakeBody = append([]snakePoint{newHead}, snakeBody...)

	if newHead == snakeFood {
		gameScore += 10
		placeSnakeFood()
	} else {
		snakeBody = snakeBody[:len(snakeBody)-1]
	}
}

func renderSnake(renderer *sdl.Renderer, config *Config) {
	offsetX := (screenWidth - snakeW*snakeCellSz) / 2
	offsetY := (screenHeight - snakeH*snakeCellSz) / 2

	// Grid background
	fillRoundedRect(renderer, offsetX-8, offsetY-8,
		snakeW*snakeCellSz+16, snakeH*snakeCellSz+16, 8, ColorPanel)

	// Food
	foodX := offsetX + int32(snakeFood.x)*snakeCellSz
	foodY := offsetY + int32(snakeFood.y)*snakeCellSz
	fillRoundedRect(renderer, foodX+2, foodY+2, int32(snakeCellSz)-4, int32(snakeCellSz)-4, 6,
		sdl.Color{R: 220, G: 50, B: 50, A: 255})

	// Snake body
	for i, s := range snakeBody {
		sx := offsetX + int32(s.x)*snakeCellSz
		sy := offsetY + int32(s.y)*snakeCellSz
		var col sdl.Color
		if i == 0 {
			col = getAccentColor(config) // head
		} else {
			frac := float64(i) / float64(len(snakeBody))
			col = sdl.Color{
				R: uint8(float64(getAccentColor(config).R) * (1 - frac*0.5)),
				G: uint8(float64(getAccentColor(config).G) * (1 - frac*0.3)),
				B: uint8(float64(getAccentColor(config).B) * (1 - frac*0.3)),
				A: 255,
			}
		}
		fillRoundedRect(renderer, sx+1, sy+1, int32(snakeCellSz)-2, int32(snakeCellSz)-2, 4, col)
	}

	// Score
	font, _ := getCachedFont(config, "medium")
	if font != nil {
		scoreText := fmt.Sprintf("Score: %d", gameScore)
		sw, _, _ := font.SizeUTF8(scoreText)
		drawText(renderer, font, scoreText, (screenWidth-int32(sw))/2, 20, getAccentColor(config), textAlignLeft)
	}

	// Game over
	if gameOver {
		renderGameOver(renderer, config, "[S] Game Over!", gameScore)
	}
}

// ──────────────────────────────────────────────────────────────────────
// [T] TETRIS
// ──────────────────────────────────────────────────────────────────────

const (
	tetrisW     = 10
	tetrisH     = 20
	tetrisCell  = 28
	tetrisSpeed = 400
)

var (
	tetrisBoard [tetrisH][tetrisW]int
	tetrisPiece struct {
		shape [4][4]int
		x, y  int
		color sdl.Color
	}
	tetrisNextPiece [4][4]int
	tetrisNextColor sdl.Color
	tetrisTick      uint64
	tetrisLines     int
	tetrisDAS       int // delayed auto shift
)

var tetrisShapes = []struct {
	shape [4][4]int
	color sdl.Color
}{
	{[4][4]int{{0, 0, 0, 0}, {1, 1, 1, 1}, {0, 0, 0, 0}, {0, 0, 0, 0}},
		sdl.Color{R: 0, G: 240, B: 240, A: 255}}, // I
	{[4][4]int{{1, 0, 0}, {1, 1, 1}, {0, 0, 0}},
		sdl.Color{R: 0, G: 0, B: 240, A: 255}}, // J
	{[4][4]int{{0, 0, 1}, {1, 1, 1}, {0, 0, 0}},
		sdl.Color{R: 240, G: 160, B: 0, A: 255}}, // L
	{[4][4]int{{1, 1}, {1, 1}},
		sdl.Color{R: 240, G: 240, B: 0, A: 255}}, // O
	{[4][4]int{{0, 1, 1}, {1, 1, 0}, {0, 0, 0}},
		sdl.Color{R: 0, G: 240, B: 0, A: 255}}, // S
	{[4][4]int{{0, 1, 0}, {1, 1, 1}, {0, 0, 0}},
		sdl.Color{R: 160, G: 0, B: 240, A: 255}}, // T
	{[4][4]int{{1, 1, 0}, {0, 1, 1}, {0, 0, 0}},
		sdl.Color{R: 240, G: 0, B: 0, A: 255}}, // Z
}

func initTetris() {
	for i := range tetrisBoard {
		for j := range tetrisBoard[i] {
			tetrisBoard[i][j] = 0
		}
	}
	tetrisLines = 0
	tetrisTick = 0
	tetrisDAS = 0
	spawnTetrisPiece()
	tetrisNextPiece, tetrisNextColor = randomTetrisShape()
}

func randomTetrisShape() ([4][4]int, sdl.Color) {
	s := tetrisShapes[rand.Intn(len(tetrisShapes))]
	return s.shape, s.color
}

func spawnTetrisPiece() {
	tetrisPiece.shape = tetrisNextPiece
	tetrisPiece.color = tetrisNextColor
	tetrisPiece.x = 3
	tetrisPiece.y = 0
	tetrisNextPiece, tetrisNextColor = randomTetrisShape()
}

func tetrisCanPlace(shape [4][4]int, px, py int) bool {
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			if shape[y][x] != 0 {
				bx := px + x
				by := py + y
				if bx < 0 || bx >= tetrisW || by >= tetrisH {
					return false
				}
				if by >= 0 && tetrisBoard[by][bx] != 0 {
					return false
				}
			}
		}
	}
	return true
}

func tetrisPlace() {
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			if tetrisPiece.shape[y][x] != 0 {
				bx := tetrisPiece.x + x
				by := tetrisPiece.y + y
				if by >= 0 && by < tetrisH && bx >= 0 && bx < tetrisW {
					tetrisBoard[by][bx] = 1
				}
			}
		}
	}
	// Clear full lines
	linesCleared := 0
	for y := tetrisH - 1; y >= 0; y-- {
		full := true
		for x := 0; x < tetrisW; x++ {
			if tetrisBoard[y][x] == 0 {
				full = false
				break
			}
		}
		if full {
			// Move everything above down
			for yy := y; yy > 0; yy-- {
				tetrisBoard[yy] = tetrisBoard[yy-1]
			}
			tetrisBoard[0] = [tetrisW]int{}
			linesCleared++
			y++ // recheck this row
		}
	}
	tetrisLines += linesCleared
	gameScore += linesCleared * linesCleared * 100
	spawnTetrisPiece()
	if !tetrisCanPlace(tetrisPiece.shape, tetrisPiece.x, tetrisPiece.y) {
		gameOver = true
	}
}

func tetrisRotate() [4][4]int {
	var rotated [4][4]int
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			rotated[x][3-y] = tetrisPiece.shape[y][x]
		}
	}
	return rotated
}

func handleTetrisInput(event *sdl.KeyboardEvent) bool {
	switch event.Keysym.Sym {
	case sdl.K_LEFT:
		if tetrisCanPlace(tetrisPiece.shape, tetrisPiece.x-1, tetrisPiece.y) {
			tetrisPiece.x--
		}
	case sdl.K_RIGHT:
		if tetrisCanPlace(tetrisPiece.shape, tetrisPiece.x+1, tetrisPiece.y) {
			tetrisPiece.x++
		}
	case sdl.K_DOWN:
		if tetrisCanPlace(tetrisPiece.shape, tetrisPiece.x, tetrisPiece.y+1) {
			tetrisPiece.y++
			gameScore++
		}
	case sdl.K_UP:
		rotated := tetrisRotate()
		if tetrisCanPlace(rotated, tetrisPiece.x, tetrisPiece.y) {
			tetrisPiece.shape = rotated
		}
	case sdl.K_SPACE:
		// Hard drop
		for tetrisCanPlace(tetrisPiece.shape, tetrisPiece.x, tetrisPiece.y+1) {
			tetrisPiece.y++
			gameScore += 2
		}
		tetrisPlace()
	case sdl.K_r:
		if gameOver {
			initTetris()
			gameOver = false
			gameScore = 0
		}
	default:
		return false
	}
	PlayNavSound()
	return true
}

func tickTetris() {
	now := sdl.GetTicks64()
	if now-tetrisTick < uint64(tetrisSpeed) {
		return
	}
	tetrisTick = now
	if tetrisCanPlace(tetrisPiece.shape, tetrisPiece.x, tetrisPiece.y+1) {
		tetrisPiece.y++
	} else {
		tetrisPlace()
	}
}

func renderTetris(renderer *sdl.Renderer, config *Config) {
	offsetX := (screenWidth - tetrisW*tetrisCell) / 2
	offsetY := (screenHeight - tetrisH*tetrisCell) / 2

	// Board background
	fillRoundedRect(renderer, offsetX-4, offsetY-4,
		tetrisW*tetrisCell+8, tetrisH*tetrisCell+8, 6, ColorPanel)

	// Placed blocks
	for y := 0; y < tetrisH; y++ {
		for x := 0; x < tetrisW; x++ {
			if tetrisBoard[y][x] != 0 {
				cx := offsetX + int32(x)*tetrisCell
				cy := offsetY + int32(y)*tetrisCell
				fillRoundedRect(renderer, cx+1, cy+1, int32(tetrisCell)-2, int32(tetrisCell)-2, 3,
					sdl.Color{R: 100, G: 120, B: 160, A: 255})
			}
		}
	}

	// Current piece
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			if tetrisPiece.shape[y][x] != 0 {
				px := offsetX + int32(tetrisPiece.x+x)*tetrisCell
				py := offsetY + int32(tetrisPiece.y+y)*tetrisCell
				if py >= 0 {
					fillRoundedRect(renderer, px+1, py+1, int32(tetrisCell)-2, int32(tetrisCell)-2, 3,
						tetrisPiece.color)
				}
			}
		}
	}

	// Ghost piece
	ghostY := tetrisPiece.y
	for tetrisCanPlace(tetrisPiece.shape, tetrisPiece.x, ghostY+1) {
		ghostY++
	}
	if ghostY != tetrisPiece.y {
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				if tetrisPiece.shape[y][x] != 0 {
					px := offsetX + int32(tetrisPiece.x+x)*tetrisCell
					py := offsetY + int32(ghostY+y)*tetrisCell
					if py >= 0 {
						renderer.SetDrawColor(tetrisPiece.color.R, tetrisPiece.color.G, tetrisPiece.color.B, 60)
						renderer.FillRect(&sdl.Rect{X: px + 2, Y: py + 2, W: int32(tetrisCell) - 4, H: int32(tetrisCell) - 4})
					}
				}
			}
		}
	}

	// Score & lines
	font, _ := getCachedFont(config, "medium")
	if font != nil {
		drawText(renderer, font, fmt.Sprintf("Score: %d", gameScore),
			offsetX+tetrisW*tetrisCell+16, offsetY, getAccentColor(config), textAlignLeft)
		drawText(renderer, font, fmt.Sprintf("Lines: %d", tetrisLines),
			offsetX+tetrisW*tetrisCell+16, offsetY+32, ColorTextSecondary(), textAlignLeft)
	}

	if gameOver {
		renderGameOver(renderer, config, "[T] Tetris Over!", gameScore)
	}
}

// ──────────────────────────────────────────────────────────────────────
// [2] 2048
// ──────────────────────────────────────────────────────────────────────

const (
	g2048Grid = 4
	g2048Cell = 80
)

var g2048Board [g2048Grid][g2048Grid]int

func init2048() {
	for i := range g2048Board {
		for j := range g2048Board[i] {
			g2048Board[i][j] = 0
		}
	}
	place2048Tile()
	place2048Tile()
	gameOver = false
	gameScore = 0
}

func place2048Tile() {
	empty := [][2]int{}
	for y := 0; y < g2048Grid; y++ {
		for x := 0; x < g2048Grid; x++ {
			if g2048Board[y][x] == 0 {
				empty = append(empty, [2]int{x, y})
			}
		}
	}
	if len(empty) == 0 {
		return
	}
	pos := empty[rand.Intn(len(empty))]
	g2048Board[pos[1]][pos[0]] = 2
	if rand.Intn(10) == 0 {
		g2048Board[pos[1]][pos[0]] = 4
	}
}

func g2048SlideRow(row [g2048Grid]int) ([g2048Grid]int, int) {
	score := 0
	// Compact
	var compacted [g2048Grid]int
	idx := 0
	for _, v := range row {
		if v != 0 {
			compacted[idx] = v
			idx++
		}
	}
	// Merge
	for i := 0; i < g2048Grid-1; i++ {
		if compacted[i] != 0 && compacted[i] == compacted[i+1] {
			compacted[i] *= 2
			score += compacted[i]
			compacted[i+1] = 0
		}
	}
	// Compact again
	idx = 0
	var result [g2048Grid]int
	for _, v := range compacted {
		if v != 0 {
			result[idx] = v
			idx++
		}
	}
	return result, score
}

func g2048MoveLeft() int {
	score := 0
	for y := 0; y < g2048Grid; y++ {
		row := g2048Board[y]
		newRow, s := g2048SlideRow(row)
		g2048Board[y] = newRow
		score += s
	}
	return score
}

func g2048MoveRight() int {
	score := 0
	for y := 0; y < g2048Grid; y++ {
		row := g2048Board[y]
		// Reverse
		for i, j := 0, g2048Grid-1; i < j; i, j = i+1, j-1 {
			row[i], row[j] = row[j], row[i]
		}
		newRow, s := g2048SlideRow(row)
		// Reverse back
		for i, j := 0, g2048Grid-1; i < j; i, j = i+1, j-1 {
			newRow[i], newRow[j] = newRow[j], newRow[i]
		}
		g2048Board[y] = newRow
		score += s
	}
	return score
}

func g2048MoveUp() int {
	score := 0
	for x := 0; x < g2048Grid; x++ {
		var col [g2048Grid]int
		for y := 0; y < g2048Grid; y++ {
			col[y] = g2048Board[y][x]
		}
		newCol, s := g2048SlideRow(col)
		for y := 0; y < g2048Grid; y++ {
			g2048Board[y][x] = newCol[y]
		}
		score += s
	}
	return score
}

func g2048MoveDown() int {
	score := 0
	for x := 0; x < g2048Grid; x++ {
		var col [g2048Grid]int
		for y := 0; y < g2048Grid; y++ {
			col[y] = g2048Board[y][x]
		}
		// Reverse
		for i, j := 0, g2048Grid-1; i < j; i, j = i+1, j-1 {
			col[i], col[j] = col[j], col[i]
		}
		newCol, s := g2048SlideRow(col)
		// Reverse back
		for i, j := 0, g2048Grid-1; i < j; i, j = i+1, j-1 {
			newCol[i], newCol[j] = newCol[j], newCol[i]
		}
		for y := 0; y < g2048Grid; y++ {
			g2048Board[y][x] = newCol[y]
		}
		score += s
	}
	return score
}

func g2048HasMoves() bool {
	for y := 0; y < g2048Grid; y++ {
		for x := 0; x < g2048Grid; x++ {
			if g2048Board[y][x] == 0 {
				return true
			}
			if x < g2048Grid-1 && g2048Board[y][x] == g2048Board[y][x+1] {
				return true
			}
			if y < g2048Grid-1 && g2048Board[y][x] == g2048Board[y+1][x] {
				return true
			}
		}
	}
	return false
}

func handle2048Input(event *sdl.KeyboardEvent) bool {
	if gameOver {
		if event.Keysym.Sym == sdl.K_r {
			init2048()
		}
		return true
	}
	scoreBefore := gameScore
	switch event.Keysym.Sym {
	case sdl.K_LEFT:
		gameScore += g2048MoveLeft()
	case sdl.K_RIGHT:
		gameScore += g2048MoveRight()
	case sdl.K_UP:
		gameScore += g2048MoveUp()
	case sdl.K_DOWN:
		gameScore += g2048MoveDown()
	case sdl.K_r:
		init2048()
		return true
	default:
		return false
	}
	if gameScore != scoreBefore {
		place2048Tile()
		PlayNavSound()
		if !g2048HasMoves() {
			gameOver = true
		}
	}
	return true
}

var g2048Colors = map[int]sdl.Color{
	0:    {R: 20, G: 26, B: 42, A: 255},
	2:    {R: 238, G: 228, B: 218, A: 255},
	4:    {R: 237, G: 224, B: 200, A: 255},
	8:    {R: 242, G: 177, B: 121, A: 255},
	16:   {R: 245, G: 149, B: 99, A: 255},
	32:   {R: 246, G: 124, B: 95, A: 255},
	64:   {R: 246, G: 94, B: 59, A: 255},
	128:  {R: 237, G: 207, B: 114, A: 255},
	256:  {R: 237, G: 204, B: 97, A: 255},
	512:  {R: 237, G: 200, B: 80, A: 255},
	1024: {R: 237, G: 197, B: 63, A: 255},
	2048: {R: 237, G: 194, B: 46, A: 255},
}

func render2048(renderer *sdl.Renderer, config *Config) {
	totalW := g2048Grid * g2048Cell
	offsetX := (screenWidth - int32(totalW)) / 2
	offsetY := (screenHeight-int32(totalW))/2 + 20

	// Board
	fillRoundedRect(renderer, offsetX-8, offsetY-8, int32(totalW)+16, int32(totalW)+16, 12, ColorPanel)

	font, _ := getCachedFont(config, "large")
	bodyFont, _ := getCachedFont(config, "medium")

	for y := 0; y < g2048Grid; y++ {
		for x := 0; x < g2048Grid; x++ {
			cx := offsetX + int32(x)*int32(g2048Cell)
			cy := offsetY + int32(y)*int32(g2048Cell)
			val := g2048Board[y][x]

			bg := g2048Colors[val]
			if bg == (sdl.Color{}) {
				bg = sdl.Color{R: 60, G: 53, B: 46, A: 255}
			}
			fillRoundedRect(renderer, cx+2, cy+2, int32(g2048Cell)-4, int32(g2048Cell)-4, 6, bg)

			if val != 0 && font != nil {
				text := fmt.Sprintf("%d", val)
				tw, th, _ := font.SizeUTF8(text)
				textCol := sdl.Color{R: 249, G: 246, B: 242, A: 255}
				if val <= 4 {
					textCol = sdl.Color{R: 119, G: 110, B: 101, A: 255}
				}
				drawText(renderer, font, text,
					cx+(int32(g2048Cell)-int32(tw))/2,
					cy+(int32(g2048Cell)-int32(th))/2,
					textCol, textAlignLeft)
			}
		}
	}

	// Score
	if bodyFont != nil {
		drawText(renderer, bodyFont, fmt.Sprintf("Score: %d", gameScore),
			offsetX, offsetY-int32(g2048Cell), getAccentColor(config), textAlignLeft)
	}

	if gameOver {
		renderGameOver(renderer, config, "[2] Game Over!", gameScore)
	}
}

// ──────────────────────────────────────────────────────────────────────
// [P] PONG
// ──────────────────────────────────────────────────────────────────────

var (
	pongBall struct {
		x, y   float64
		vx, vy float64
	}
	pongPaddleY [2]float64 // player 0 (left), player 1 (right/AI)
	pongW       = int32(800)
	pongH       = int32(500)
	pongPaddleH = int32(80)
	pongPaddleW = int32(12)
	pongBallR   = int32(8)
	pongSpeed   = 4.0
)

func initPong() {
	pongBall.x = float64(pongW) / 2
	pongBall.y = float64(pongH) / 2
	pongBall.vx = pongSpeed
	pongBall.vy = pongSpeed * (rand.Float64()*0.6 - 0.3)
	pongPaddleY[0] = float64(pongH)/2 - float64(pongPaddleH)/2
	pongPaddleY[1] = float64(pongH)/2 - float64(pongPaddleH)/2
	gameScore = 0
}

func handlePongInput(event *sdl.KeyboardEvent) bool {
	switch event.Keysym.Sym {
	case sdl.K_UP:
		pongPaddleY[0] -= 30
		if pongPaddleY[0] < 0 {
			pongPaddleY[0] = 0
		}
	case sdl.K_DOWN:
		pongPaddleY[0] += 30
		if pongPaddleY[0] > float64(pongH)-float64(pongPaddleH) {
			pongPaddleY[0] = float64(pongH) - float64(pongPaddleH)
		}
	case sdl.K_r:
		initPong()
	default:
		return false
	}
	return true
}

func tickPong() {
	// Ball movement
	pongBall.x += pongBall.vx
	pongBall.y += pongBall.vy

	// Top/bottom bounce
	if pongBall.y <= 0 || pongBall.y >= float64(pongH) {
		pongBall.vy = -pongBall.vy
		pongBall.y = clampF64(pongBall.y, 0, float64(pongH))
	}

	// Left paddle collision
	if pongBall.x <= float64(pongPaddleW)+4 &&
		pongBall.y >= pongPaddleY[0] && pongBall.y <= pongPaddleY[0]+float64(pongPaddleH) {
		pongBall.vx = -pongBall.vx
		pongBall.x = float64(pongPaddleW) + 5
		// Add spin based on where it hit the paddle
		hitPos := (pongBall.y - pongPaddleY[0]) / float64(pongPaddleH)
		pongBall.vy = pongSpeed * (hitPos - 0.5) * 2
		gameScore++
	}

	// Right paddle collision (AI)
	if pongBall.x >= float64(pongW)-float64(pongPaddleW)-4 &&
		pongBall.y >= pongPaddleY[1] && pongBall.y <= pongPaddleY[1]+float64(pongPaddleH) {
		pongBall.vx = -pongBall.vx
		pongBall.x = float64(pongW) - float64(pongPaddleW) - 5
	}

	// AI paddle
	aiCenter := pongPaddleY[1] + float64(pongPaddleH)/2
	diff := pongBall.y - aiCenter
	if diff > 10 {
		pongPaddleY[1] += 3.5
	} else if diff < -10 {
		pongPaddleY[1] -= 3.5
	}
	pongPaddleY[1] = clampF64(pongPaddleY[1], 0, float64(pongH)-float64(pongPaddleH))

	// Score / reset
	if pongBall.x < -20 {
		// AI scored
		pongBall.x = float64(pongW) / 2
		pongBall.y = float64(pongH) / 2
		pongBall.vx = pongSpeed
		pongBall.vy = pongSpeed * (rand.Float64()*0.6 - 0.3)
	}
	if pongBall.x > float64(pongW)+20 {
		// Player scored
		pongBall.x = float64(pongW) / 2
		pongBall.y = float64(pongH) / 2
		pongBall.vx = -pongSpeed
		pongBall.vy = pongSpeed * (rand.Float64()*0.6 - 0.3)
		gameScore += 5
	}
}

func renderPong(renderer *sdl.Renderer, config *Config) {
	offsetX := (screenWidth - pongW) / 2
	offsetY := (screenHeight - pongH) / 2

	// Court border
	strokeRoundedRect(renderer, offsetX-4, offsetY-4, pongW+8, pongH+8, 8, 2, ColorBorder)

	// Center line
	for y := int32(0); y < pongH; y += 20 {
		renderer.SetDrawColor(ColorBorder.R, ColorBorder.G, ColorBorder.B, 100)
		renderer.FillRect(&sdl.Rect{X: offsetX + pongW/2 - 1, Y: offsetY + y, W: 2, H: 10})
	}

	// Paddles
	paddleColor := getAccentColor(config)
	fillRoundedRect(renderer, offsetX+2, offsetY+int32(pongPaddleY[0]), pongPaddleW, pongPaddleH, 4, paddleColor)
	fillRoundedRect(renderer, offsetX+pongW-pongPaddleW-2, offsetY+int32(pongPaddleY[1]), pongPaddleW, pongPaddleH, 4,
		sdl.Color{R: 220, G: 80, B: 80, A: 255})

	// Ball
	bx := offsetX + int32(pongBall.x)
	by := offsetY + int32(pongBall.y)
	renderer.SetDrawColor(255, 255, 255, 255)
	renderer.FillRect(&sdl.Rect{X: bx - pongBallR, Y: by - pongBallR, W: pongBallR * 2, H: pongBallR * 2})

	// Score
	font, _ := getCachedFont(config, "large")
	if font != nil {
		scoreText := fmt.Sprintf("%d", gameScore)
		sw, _, _ := font.SizeUTF8(scoreText)
		drawText(renderer, font, scoreText, (screenWidth-int32(sw))/2, offsetY-40, getAccentColor(config), textAlignLeft)
	}

	// Instructions
	if !gameOver {
		smallFont, _ := getCachedFont(config, "small")
		if smallFont != nil {
			hint := "↑↓ move paddle  •  Q quit"
			hw, _, _ := smallFont.SizeUTF8(hint)
			drawText(renderer, smallFont, hint, (screenWidth-int32(hw))/2, offsetY+pongH+16, ColorTextTertiary(), textAlignLeft)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────
// Game Over overlay
// ──────────────────────────────────────────────────────────────────────

func renderGameOver(renderer *sdl.Renderer, config *Config, title string, score int) {
	// Dim overlay
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 160)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	// Card
	cardW := int32(400)
	cardH := int32(200)
	cx := (screenWidth - cardW) / 2
	cy := (screenHeight - cardH) / 2
	fillRoundedRect(renderer, cx, cy, cardW, cardH, 16, ColorPanel)
	strokeRoundedRect(renderer, cx, cy, cardW, cardH, 16, 2, getAccentColor(config))

	font, _ := getCachedFont(config, "large")
	bodyFont, _ := getCachedFont(config, "medium")
	smallFont, _ := getCachedFont(config, "small")

	if font != nil {
		tw, _, _ := font.SizeUTF8(title)
		drawText(renderer, font, title, cx+(cardW-int32(tw))/2, cy+30, getAccentColor(config), textAlignLeft)
	}
	if bodyFont != nil {
		scoreText := fmt.Sprintf("Score: %d", score)
		sw, _, _ := bodyFont.SizeUTF8(scoreText)
		drawText(renderer, bodyFont, scoreText, cx+(cardW-int32(sw))/2, cy+80, ColorTextPrimary(), textAlignLeft)
	}
	if smallFont != nil {
		hint := "Press R to restart  •  Q to quit"
		hw, _, _ := smallFont.SizeUTF8(hint)
		drawText(renderer, smallFont, hint, cx+(cardW-int32(hw))/2, cy+130, ColorTextTertiary(), textAlignLeft)
	}
}

// Scene controller mapping, moved here from controller.go so the scene owns
// its own input.

func ctrlGames(e *sdl.ControllerButtonEvent, config *Config) bool {
	if activeGame != gameNone {
		return ctrlActiveGame(e)
	}
	return ctrlGamesMenu(e, config)
}

func ctrlGamesMenu(e *sdl.ControllerButtonEvent, config *Config) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if gameMenuCursor >= 3 {
			gameMenuCursor -= 3
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		if gameMenuCursor+3 < len(gameList) {
			gameMenuCursor += 3
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		if gameMenuCursor%3 > 0 {
			gameMenuCursor--
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		if gameMenuCursor%3 < 2 && gameMenuCursor+1 < len(gameList) {
			gameMenuCursor++
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_A:
		startGame(gameList[gameMenuCursor].id)
		PlayActivateSound()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		goBackScene(config) // plays the back sound itself
	default:
		return false
	}
	return true
}

func ctrlActiveGame(e *sdl.ControllerButtonEvent) bool {
	// Universal game controls: B always leaves the game and returns to the
	// game list. It used to work only on the Game Over screen, so a running
	// game could not be exited with the back button at all.
	if e.Button == sdl.CONTROLLER_BUTTON_B || e.Button == sdl.CONTROLLER_BUTTON_BACK {
		activeGame = gameNone
		gameOver = false
		PlayBackSound()
		return true
	}
	if e.Button == sdl.CONTROLLER_BUTTON_START && gameOver {
		switch activeGame {
		case gameSnake:
			initSnake()
		case gameTetris:
			initTetris()
		case game2048:
			init2048()
		case gamePong:
			initPong()
		case gameMinesweeper:
			initMinesweeper()
		case gameFlappy:
			initFlappy()
		}
		gameOver = false
		gameScore = 0
		return true
	}

	switch activeGame {
	case gameSnake:
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_DPAD_UP:
			if snakeCurDir != snakeDown {
				snakeNextDir = snakeUp
			}
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
			if snakeCurDir != snakeUp {
				snakeNextDir = snakeDown
			}
		case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
			if snakeCurDir != snakeRight {
				snakeNextDir = snakeLeft
			}
		case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
			if snakeCurDir != snakeLeft {
				snakeNextDir = snakeRight
			}
		case sdl.CONTROLLER_BUTTON_A:
			if gameOver {
				initSnake()
				gameOver = false
				gameScore = 0
			}
		}
	case gameTetris:
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
			if tetrisCanPlace(tetrisPiece.shape, tetrisPiece.x-1, tetrisPiece.y) {
				tetrisPiece.x--
			}
		case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
			if tetrisCanPlace(tetrisPiece.shape, tetrisPiece.x+1, tetrisPiece.y) {
				tetrisPiece.x++
			}
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
			if tetrisCanPlace(tetrisPiece.shape, tetrisPiece.x, tetrisPiece.y+1) {
				tetrisPiece.y++
				gameScore++
			}
		case sdl.CONTROLLER_BUTTON_DPAD_UP:
			rotated := tetrisRotate()
			if tetrisCanPlace(rotated, tetrisPiece.x, tetrisPiece.y) {
				tetrisPiece.shape = rotated
			}
		case sdl.CONTROLLER_BUTTON_A:
			if gameOver {
				initTetris()
				gameOver = false
				gameScore = 0
			} else {
				for tetrisCanPlace(tetrisPiece.shape, tetrisPiece.x, tetrisPiece.y+1) {
					tetrisPiece.y++
					gameScore += 2
				}
				tetrisPlace()
			}
		case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
			if tetrisCanPlace(tetrisPiece.shape, tetrisPiece.x, tetrisPiece.y+1) {
				tetrisPiece.y++
				gameScore++
			}
		}
	case game2048:
		if gameOver {
			return true
		}
		scoreBefore := gameScore
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
			gameScore += g2048MoveLeft()
		case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
			gameScore += g2048MoveRight()
		case sdl.CONTROLLER_BUTTON_DPAD_UP:
			gameScore += g2048MoveUp()
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
			gameScore += g2048MoveDown()
		default:
			return true
		}
		if gameScore != scoreBefore {
			place2048Tile()
			PlayNavSound()
			if !g2048HasMoves() {
				gameOver = true
			}
		}
	case gamePong:
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_DPAD_UP:
			pongPaddleY[0] -= 30
			if pongPaddleY[0] < 0 {
				pongPaddleY[0] = 0
			}
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
			pongPaddleY[0] += 30
			if pongPaddleY[0] > float64(pongH)-float64(pongPaddleH) {
				pongPaddleY[0] = float64(pongH) - float64(pongPaddleH)
			}
		case sdl.CONTROLLER_BUTTON_A:
			if gameOver {
				initPong()
			}
		}
	case gameMinesweeper:
		if gameOver {
			if e.Button == sdl.CONTROLLER_BUTTON_A || e.Button == sdl.CONTROLLER_BUTTON_START {
				initMinesweeper()
				gameOver = false
				gameScore = 0
			}
			return true
		}
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_DPAD_UP:
			msCursorY--
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
			msCursorY++
		case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
			msCursorX--
		case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
			msCursorX++
		case sdl.CONTROLLER_BUTTON_A:
			msRevealAtCursor()
		case sdl.CONTROLLER_BUTTON_X, sdl.CONTROLLER_BUTTON_Y:
			msToggleFlagAtCursor()
		default:
			return true
		}
		msClampCursor()
		PlayNavSound()
	case gameFlappy:
		if e.Button == sdl.CONTROLLER_BUTTON_A || e.Button == sdl.CONTROLLER_BUTTON_START {
			flappyFlap()
		}
	}
	return true
}

// ──────────────────────────────────────────────────────────────────────
// MINESWEEPER
// ──────────────────────────────────────────────────────────────────────

const (
	msW      = 12
	msH      = 9
	msMines  = 16
	msCellSz = 32
)

var (
	msMine     [msH][msW]bool
	msRevealed [msH][msW]bool
	msFlagged  [msH][msW]bool
	msCursorX  int
	msCursorY  int
	msWon      bool
)

func initMinesweeper() {
	msMine = [msH][msW]bool{}
	msRevealed = [msH][msW]bool{}
	msFlagged = [msH][msW]bool{}
	msCursorX, msCursorY = msW/2, msH/2
	msWon = false
	gameScore = 0
	// Keep the first move safe: no mine under the centre 3x3 area.
	for placed := 0; placed < msMines; {
		x, y := rand.Intn(msW), rand.Intn(msH)
		if msMine[y][x] || (msAbs(x-msCursorX) <= 1 && msAbs(y-msCursorY) <= 1) {
			continue
		}
		msMine[y][x] = true
		placed++
	}
}

func msAbs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func msClampCursor() {
	if msCursorX < 0 {
		msCursorX = 0
	}
	if msCursorX >= msW {
		msCursorX = msW - 1
	}
	if msCursorY < 0 {
		msCursorY = 0
	}
	if msCursorY >= msH {
		msCursorY = msH - 1
	}
}

func msNeighborMines(x, y int) int {
	n := 0
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			if dx == 0 && dy == 0 {
				continue
			}
			nx, ny := x+dx, y+dy
			if nx >= 0 && nx < msW && ny >= 0 && ny < msH && msMine[ny][nx] {
				n++
			}
		}
	}
	return n
}

func msToggleFlagAtCursor() {
	if gameOver || msRevealed[msCursorY][msCursorX] {
		return
	}
	msFlagged[msCursorY][msCursorX] = !msFlagged[msCursorY][msCursorX]
}

func msRevealAtCursor() {
	if gameOver {
		return
	}
	msRevealCell(msCursorX, msCursorY)
	if gameOver {
		return
	}
	revealed := 0
	for y := 0; y < msH; y++ {
		for x := 0; x < msW; x++ {
			if msRevealed[y][x] {
				revealed++
			}
		}
	}
	gameScore = revealed
	if revealed == msW*msH-msMines {
		gameOver = true
		msWon = true
	}
}

func msRevealCell(x, y int) {
	if x < 0 || x >= msW || y < 0 || y >= msH || msRevealed[y][x] || msFlagged[y][x] {
		return
	}
	msRevealed[y][x] = true
	if msMine[y][x] {
		gameOver = true
		msWon = false
		return
	}
	if msNeighborMines(x, y) != 0 {
		return
	}
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			if dx != 0 || dy != 0 {
				msRevealCell(x+dx, y+dy)
			}
		}
	}
}

func handleMinesweeperInput(event *sdl.KeyboardEvent) bool {
	if gameOver {
		switch event.Keysym.Sym {
		case sdl.K_r, sdl.K_RETURN, sdl.K_SPACE:
			initMinesweeper()
			gameOver = false
		}
		return true
	}
	switch event.Keysym.Sym {
	case sdl.K_UP, sdl.K_w:
		msCursorY--
	case sdl.K_DOWN, sdl.K_s:
		msCursorY++
	case sdl.K_LEFT, sdl.K_a:
		msCursorX--
	case sdl.K_RIGHT, sdl.K_d:
		msCursorX++
	case sdl.K_RETURN, sdl.K_SPACE:
		msRevealAtCursor()
	case sdl.K_f:
		msToggleFlagAtCursor()
	case sdl.K_r:
		initMinesweeper()
	default:
		return false
	}
	msClampCursor()
	PlayNavSound()
	return true
}

func tickMinesweeper() {}

var msNumberColors = map[int]sdl.Color{
	1: {R: 90, G: 160, B: 255, A: 255},
	2: {R: 90, G: 200, B: 120, A: 255},
	3: {R: 240, G: 110, B: 110, A: 255},
	4: {R: 180, G: 120, B: 255, A: 255},
	5: {R: 240, G: 160, B: 70, A: 255},
	6: {R: 80, G: 210, B: 210, A: 255},
	7: {R: 230, G: 230, B: 230, A: 255},
	8: {R: 150, G: 160, B: 180, A: 255},
}

func renderMinesweeper(renderer *sdl.Renderer, config *Config) {
	boardW := int32(msW * msCellSz)
	boardH := int32(msH * msCellSz)
	offsetX := (screenWidth - boardW) / 2
	offsetY := (screenHeight-boardH)/2 + 18
	font, _ := getCachedFont(config, "medium")
	small, _ := getCachedFont(config, "small")

	fillRoundedRect(renderer, offsetX-10, offsetY-10, boardW+20, boardH+20, 12, ColorPanel)

	flags := 0
	revealed := 0
	for y := 0; y < msH; y++ {
		for x := 0; x < msW; x++ {
			if msFlagged[y][x] {
				flags++
			}
			if msRevealed[y][x] {
				revealed++
			}
			px := offsetX + int32(x*msCellSz)
			py := offsetY + int32(y*msCellSz)
			cell := sdl.Rect{X: px + 1, Y: py + 1, W: msCellSz - 2, H: msCellSz - 2}
			bg := sdl.Color{R: 38, G: 48, B: 68, A: 255}
			if msRevealed[y][x] {
				bg = sdl.Color{R: 22, G: 28, B: 42, A: 255}
			}
			if (gameOver && msMine[y][x] && !msWon) || (msWon && msMine[y][x]) {
				bg = sdl.Color{R: 120, G: 36, B: 46, A: 255}
			}
			fillRoundedRect(renderer, cell.X, cell.Y, cell.W, cell.H, 5, bg)
			if msRevealed[y][x] && msMine[y][x] {
				fillCircle(renderer, px+msCellSz/2, py+msCellSz/2, 7, sdl.Color{R: 240, G: 90, B: 90, A: 255})
			} else if msRevealed[y][x] {
				n := msNeighborMines(x, y)
				if n > 0 && font != nil {
					label := fmt.Sprintf("%d", n)
					lw, lh, _ := font.SizeUTF8(label)
					col := msNumberColors[n]
					if col == (sdl.Color{}) {
						col = ColorTextPrimary()
					}
					drawText(renderer, font, label, px+(msCellSz-int32(lw))/2, py+(msCellSz-int32(lh))/2, col, textAlignLeft)
				}
			} else if msFlagged[y][x] && small != nil {
				fw, fh, _ := small.SizeUTF8("F")
				drawText(renderer, small, "F", px+(msCellSz-int32(fw))/2, py+(msCellSz-int32(fh))/2,
					sdl.Color{R: 240, G: 190, B: 80, A: 255}, textAlignLeft)
			}
		}
	}

	// Cursor ring.
	cx := offsetX + int32(msCursorX*msCellSz)
	cy := offsetY + int32(msCursorY*msCellSz)
	strokeRoundedRect(renderer, cx-2, cy-2, msCellSz+4, msCellSz+4, 7, 2, getAccentColor(config))

	if font != nil {
		header := fmt.Sprintf("Minesweeper   %d/%d cleared   %d flags   %d mines", revealed, msW*msH-msMines, flags, msMines)
		hw, _, _ := font.SizeUTF8(header)
		drawText(renderer, font, header, (screenWidth-int32(hw))/2, offsetY-58, getAccentColor(config), textAlignLeft)
	}
	if small != nil {
		hint := "Arrows move  •  Enter reveal  •  F flag  •  R restart  •  Q quit"
		hw, _, _ := small.SizeUTF8(hint)
		drawText(renderer, small, hint, (screenWidth-int32(hw))/2, offsetY+boardH+18, ColorTextTertiary(), textAlignLeft)
	}
	if gameOver {
		title := "Boom!"
		if msWon {
			title = "Board cleared!"
		}
		renderGameOver(renderer, config, title, gameScore)
	}
}

// ──────────────────────────────────────────────────────────────────────
// FLAPPY
// ──────────────────────────────────────────────────────────────────────

const (
	flappyW        = 760
	flappyH        = 500
	flappyGround   = 64
	flappyBirdX    = 190
	flappyBirdR    = 16
	flappyGap      = 150
	flappyPipeW    = 64
	flappyGravity  = 1500.0
	flappyFlapVel  = -500.0
	flappyPipeVel  = 190.0
	flappySpawnGap = 1.45
)

type flappyPipe struct {
	X      float64
	GapY   float64
	Passed bool
}

var (
	flappyY         float64
	flappyVy        float64
	flappyPipes     []flappyPipe
	flappyLastMs    uint64
	flappySpawnWait float64
)

func initFlappy() {
	flappyY = float64(flappyH-flappyGround) / 2
	flappyVy = 0
	flappyPipes = nil
	flappySpawnWait = 0.9
	flappyLastMs = sdl.GetTicks64()
	gameScore = 0
	gameOver = false
}

func flappyFlap() {
	if gameOver {
		initFlappy()
		return
	}
	flappyVy = flappyFlapVel
}

func handleFlappyInput(event *sdl.KeyboardEvent) bool {
	switch event.Keysym.Sym {
	case sdl.K_SPACE, sdl.K_RETURN, sdl.K_UP, sdl.K_w:
		flappyFlap()
	case sdl.K_r:
		initFlappy()
	default:
		return false
	}
	return true
}

func tickFlappy() {
	if gameOver {
		return
	}
	now := sdl.GetTicks64()
	if flappyLastMs == 0 {
		flappyLastMs = now
		return
	}
	dt := float64(now-flappyLastMs) / 1000
	flappyLastMs = now
	if dt <= 0 {
		return
	}
	if dt > 0.1 {
		dt = 0.1
	}

	flappyVy += flappyGravity * dt
	flappyY += flappyVy * dt
	floor := float64(flappyH - flappyGround)
	if flappyY < float64(flappyBirdR) {
		flappyY = float64(flappyBirdR)
		flappyVy = 0
	}
	if flappyY+float64(flappyBirdR) >= floor {
		gameOver = true
		return
	}

	for i := range flappyPipes {
		flappyPipes[i].X -= flappyPipeVel * dt
		p := &flappyPipes[i]
		if !p.Passed && p.X+flappyPipeW < float64(flappyBirdX) {
			p.Passed = true
			gameScore++
			PlayNavSound()
		}
	}
	if len(flappyPipes) > 0 && flappyPipes[0].X+flappyPipeW < -40 {
		flappyPipes = flappyPipes[1:]
	}

	flappySpawnWait -= dt
	if flappySpawnWait <= 0 {
		gapMin := float64(80)
		gapMax := floor - flappyGap - 80
		gapY := gapMin + rand.Float64()*(gapMax-gapMin)
		flappyPipes = append(flappyPipes, flappyPipe{X: float64(flappyW) + 60, GapY: gapY})
		flappySpawnWait = flappySpawnGap
	}

	// Circle-vs-rectangle check, slightly forgiving on the pipe caps.
	bx, by := float64(flappyBirdX), flappyY
	for _, p := range flappyPipes {
		if bx+float64(flappyBirdR) < p.X || bx-float64(flappyBirdR) > p.X+flappyPipeW {
			continue
		}
		if by-float64(flappyBirdR) < p.GapY || by+float64(flappyBirdR) > p.GapY+flappyGap {
			gameOver = true
			return
		}
	}
}

func renderFlappy(renderer *sdl.Renderer, config *Config) {
	offsetX := (screenWidth - flappyW) / 2
	offsetY := (screenHeight - flappyH) / 2

	// Sky and horizon.
	fillRoundedRect(renderer, offsetX-4, offsetY-4, flappyW+8, flappyH+8, 14, sdl.Color{R: 60, G: 150, B: 215, A: 255})
	fillRoundedRect(renderer, offsetX, offsetY+flappyH-flappyGround, flappyW, flappyGround, 0, sdl.Color{R: 90, G: 180, B: 95, A: 255})
	renderer.SetDrawColor(255, 255, 255, 45)
	for i := int32(0); i < 5; i++ {
		x := offsetX + 60 + i*170
		fillCircle(renderer, x, offsetY+90+(i%2)*34, 34, sdl.Color{R: 255, G: 255, B: 255, A: 40})
	}

	// Pipes.
	for _, p := range flappyPipes {
		x := offsetX + int32(math.Round(p.X))
		gapTop := offsetY + int32(math.Round(p.GapY))
		gapBottom := gapTop + flappyGap
		pipeCol := sdl.Color{R: 70, G: 190, B: 90, A: 255}
		pipeDark := sdl.Color{R: 45, G: 145, B: 70, A: 255}
		fillRoundedRect(renderer, x, offsetY, flappyPipeW, gapTop-offsetY, 7, pipeCol)
		fillRoundedRect(renderer, x-4, gapTop-24, flappyPipeW+8, 24, 5, pipeDark)
		fillRoundedRect(renderer, x, gapBottom, flappyPipeW, offsetY+flappyH-flappyGround-gapBottom, 7, pipeCol)
		fillRoundedRect(renderer, x-4, gapBottom, flappyPipeW+8, 24, 5, pipeDark)
	}

	// Bird with wing and eye.
	bx := offsetX + flappyBirdX
	by := offsetY + int32(math.Round(flappyY))
	body := sdl.Color{R: 255, G: 215, B: 70, A: 255}
	fillCircle(renderer, bx, by, flappyBirdR, body)
	fillCircle(renderer, bx+6, by-5, 5, sdl.Color{R: 255, G: 255, B: 255, A: 255})
	fillCircle(renderer, bx+8, by-5, 2, sdl.Color{R: 25, G: 30, B: 40, A: 255})
	fillRoundedRect(renderer, bx-18, by+2, 14, 8, 4, sdl.Color{R: 245, G: 170, B: 45, A: 255})
	fillRoundedRect(renderer, bx+flappyBirdR-2, by+2, 12, 8, 4, sdl.Color{R: 250, G: 150, B: 40, A: 255})

	font, _ := getCachedFont(config, "large")
	small, _ := getCachedFont(config, "small")
	if font != nil {
		score := fmt.Sprintf("%d", gameScore)
		sw, _, _ := font.SizeUTF8(score)
		drawText(renderer, font, score, (screenWidth-int32(sw))/2, offsetY+18, sdl.Color{R: 255, G: 255, B: 255, A: 240}, textAlignLeft)
	}
	if small != nil {
		hint := "Space / A flap  •  R restart  •  Q quit"
		hw, _, _ := small.SizeUTF8(hint)
		drawText(renderer, small, hint, (screenWidth-int32(hw))/2, offsetY+flappyH+12, ColorTextTertiary(), textAlignLeft)
	}
	if gameOver {
		renderGameOver(renderer, config, "Game Over", gameScore)
	}
}

// ensureGamesScene adds the collection to configs that predate it, so the Start
// menu always has a Games row even after a config reset or an old update.
func ensureGamesScene(config *Config) {
	if config == nil || sceneByName(config, "Games") != nil {
		return
	}
	config.Scenes = append(config.Scenes, SceneConfig{
		Name:        "Games",
		Icon:        "G",
		Description: "Snake, Tetris, 2048, Pong, Minesweeper and Flappy",
		Background:  "#0B0F17",
	})
}

// Scene registration. The menu and a running game share one scene name, so both
// paths are dispatched here.
type gamesScene struct{ sceneBase }

func (gamesScene) render(r *sdl.Renderer, c *Config) bool {
	if activeGame != gameNone {
		renderActiveGame(r, c)
	} else {
		renderGamesMenu(r, c)
	}
	return true
}

func (gamesScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	if activeGame != gameNone {
		handleActiveGameInput(e)
	} else {
		handleGamesMenuInput(e)
	}
	// Both handlers are void-ish; the old chain ignored their result and never
	// fell through to element navigation.
	return true
}

func (gamesScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return ctrlGames(e, c)
}

func init() { registerScene(gamesScene{sceneBase{name: "Games"}}) }
