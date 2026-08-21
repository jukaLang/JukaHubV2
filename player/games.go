package main

import (
	"fmt"
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

	// Game cards — 2x2 grid
	cardW := int32(500)
	cardH := int32(200)
	gap := int32(30)
	startX := (screenWidth - (cardW*2 + gap)) / 2
	startY := int32(120)

	for i, g := range gameList {
		col := i % 2
		row := i / 2
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
			drawText(renderer, smallFont, g.desc, cx+80, cy+56, ColorTextSecondary(), textAlignLeft)
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
		if gameMenuCursor >= 2 {
			gameMenuCursor -= 2
		}
		PlayNavSound()
	case sdl.K_DOWN:
		if gameMenuCursor < len(gameList)-2 {
			gameMenuCursor += 2
		}
		PlayNavSound()
	case sdl.K_LEFT:
		if gameMenuCursor%2 == 1 {
			gameMenuCursor--
		}
		PlayNavSound()
	case sdl.K_RIGHT:
		if gameMenuCursor%2 == 0 && gameMenuCursor+1 < len(gameList) {
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
