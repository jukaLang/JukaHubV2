package main

// brickbreaker.go — Brick Breaker, the iPod's own arcade game, as one of the
// Games screen's minigames (games.go dispatches to it): a paddle, a ball and
// a wall of bricks. Each level adds a row and speeds the ball up; the top
// rows take two hits from level two on. A held D-pad (or arrow key, or the
// left stick) moves the paddle; A, Space or Up launch the ball.
//
// The held state is read on the render thread (SDL wants its input calls
// there) and the 60 Hz tick moves the paddle and the ball, under minigameMu
// like every arcade game.

import (
	"fmt"
	"math"
	"math/rand"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	bbW      = 640
	bbH      = 480
	bbCols   = 10
	bbMaxRow = 8
	bbBrickH = 20
	bbGap    = 4
	bbTop    = 56
	bbPadY   = 446
	bbPadH   = 12
	bbBallR  = 6
)

var bb struct {
	bricks  [bbMaxRow][bbCols]int // hits left, 0 = broken
	rows    int
	padX    float64 // the paddle's left edge
	padW    float64
	ballX   float64
	ballY   float64
	vx, vy  float64
	stuck   bool // resting on the paddle until launched
	lives   int
	level   int
	move    int // -1, 0, +1: the held direction, set by the render thread
	launch  bool
	flash   int // ticks of the "level clear" banner
	ballSpd float64
}

func initBrickBreaker() {
	bb.lives, bb.level = 3, 1
	gameScore = 0
	bbNewLevel()
}

func bbNewLevel() {
	bb.rows = 4 + bb.level
	if bb.rows > bbMaxRow {
		bb.rows = bbMaxRow
	}
	for r := 0; r < bbMaxRow; r++ {
		for c := 0; c < bbCols; c++ {
			hits := 0
			if r < bb.rows {
				hits = 1
				if bb.level >= 2 && r < 2 {
					hits = 2
				}
			}
			bb.bricks[r][c] = hits
		}
	}
	bb.padW = 100 - float64(bb.level-1)*6
	if bb.padW < 64 {
		bb.padW = 64
	}
	bb.ballSpd = 5 + float64(bb.level-1)*0.6
	bbResetBall()
}

func bbResetBall() {
	bb.padX = (bbW - bb.padW) / 2
	bb.stuck = true
	bb.ballX = bb.padX + bb.padW/2
	bb.ballY = bbPadY - bbBallR - 1
	bb.vx, bb.vy = 0, 0
}

func bbBrickW() float64 { return (bbW - float64(bbCols+1)*bbGap) / bbCols }

func bbBrickRect(r, c int) (x, y, w, h float64) {
	w = bbBrickW()
	return bbGap + float64(c)*(w+bbGap), bbTop + float64(r)*(bbBrickH+bbGap), w, bbBrickH
}

// handleBrickBreakerInput takes the keyboard (the held arrows are polled).
func handleBrickBreakerInput(event *sdl.KeyboardEvent) bool {
	switch event.Keysym.Sym {
	case sdl.K_SPACE, sdl.K_RETURN, sdl.K_UP, sdl.K_a:
		bb.launch = true
	case sdl.K_r:
		initBrickBreaker()
		gameOver = false
	default:
		return false
	}
	return true
}

// bbPoll reads the held direction (render thread, minigameMu held).
func bbPoll(config *Config) {
	move := 0
	keys := sdl.GetKeyboardState()
	if len(keys) > int(sdl.SCANCODE_RIGHT) {
		if keys[sdl.SCANCODE_LEFT] != 0 {
			move--
		}
		if keys[sdl.SCANCODE_RIGHT] != 0 {
			move++
		}
	}
	if pad := gameController; pad != nil {
		held := jlandPadHeld(config, pad)
		if held[sdl.CONTROLLER_BUTTON_DPAD_LEFT] {
			move--
		}
		if held[sdl.CONTROLLER_BUTTON_DPAD_RIGHT] {
			move++
		}
		if ax := pad.Axis(sdl.GameControllerAxis(sdl.CONTROLLER_AXIS_LEFTX)); ax < -16000 {
			move = -1
		} else if ax > 16000 {
			move = 1
		}
	}
	if move < -1 {
		move = -1
	} else if move > 1 {
		move = 1
	}
	bb.move = move
}

func tickBrickBreaker() {
	if bb.flash > 0 {
		bb.flash--
		return
	}
	bb.padX += float64(bb.move) * 8
	if bb.padX < 0 {
		bb.padX = 0
	}
	if bb.padX > bbW-bb.padW {
		bb.padX = bbW - bb.padW
	}
	if bb.stuck {
		bb.ballX = bb.padX + bb.padW/2
		bb.ballY = bbPadY - bbBallR - 1
		if bb.launch {
			bb.stuck = false
			a := -math.Pi/2 + (rand.Float64()-0.5)*0.6
			bb.vx, bb.vy = math.Cos(a)*bb.ballSpd, math.Sin(a)*bb.ballSpd
		}
		bb.launch = false
		return
	}
	bb.launch = false
	// Two half steps a tick, so a fast ball cannot pass through a brick.
	for step := 0; step < 2; step++ {
		bb.ballX += bb.vx / 2
		bb.ballY += bb.vy / 2
		if bb.ballX < bbBallR {
			bb.ballX, bb.vx = bbBallR, math.Abs(bb.vx)
		}
		if bb.ballX > bbW-bbBallR {
			bb.ballX, bb.vx = bbW-bbBallR, -math.Abs(bb.vx)
		}
		if bb.ballY < bbBallR {
			bb.ballY, bb.vy = bbBallR, math.Abs(bb.vy)
		}
		// The paddle: where it is hit sets the angle.
		if bb.vy > 0 && bb.ballY+bbBallR >= bbPadY && bb.ballY+bbBallR <= bbPadY+bbPadH+bb.vy &&
			bb.ballX >= bb.padX-bbBallR && bb.ballX <= bb.padX+bb.padW+bbBallR {
			hit := (bb.ballX-bb.padX)/bb.padW*2 - 1
			if hit < -1 {
				hit = -1
			} else if hit > 1 {
				hit = 1
			}
			a := -math.Pi/2 + hit*1.05
			bb.vx, bb.vy = math.Cos(a)*bb.ballSpd, math.Sin(a)*bb.ballSpd
			bb.ballY = bbPadY - bbBallR - 1
		}
		if bbHitBrick() {
			break
		}
	}
	if bb.ballY > bbH+bbBallR {
		bb.lives--
		if bb.lives <= 0 {
			gameOver = true
			return
		}
		bbResetBall()
		return
	}
	left := 0
	for r := 0; r < bb.rows; r++ {
		for c := 0; c < bbCols; c++ {
			if bb.bricks[r][c] > 0 {
				left++
			}
		}
	}
	if left == 0 {
		gameScore += 100 * bb.level
		bb.level++
		bb.flash = 90
		bbNewLevel()
	}
}

// bbHitBrick bounces the ball off the brick it overlaps, if any.
func bbHitBrick() bool {
	for r := 0; r < bb.rows; r++ {
		for c := 0; c < bbCols; c++ {
			if bb.bricks[r][c] <= 0 {
				continue
			}
			x, y, w, h := bbBrickRect(r, c)
			if bb.ballX+bbBallR < x || bb.ballX-bbBallR > x+w || bb.ballY+bbBallR < y || bb.ballY-bbBallR > y+h {
				continue
			}
			bb.bricks[r][c]--
			if bb.bricks[r][c] == 0 {
				gameScore += 10 * (bb.rows - r)
			}
			// Bounce on the side with the smaller overlap.
			overX := math.Min(bb.ballX+bbBallR-x, x+w-(bb.ballX-bbBallR))
			overY := math.Min(bb.ballY+bbBallR-y, y+h-(bb.ballY-bbBallR))
			if overX < overY {
				bb.vx = -bb.vx
			} else {
				bb.vy = -bb.vy
			}
			return true
		}
	}
	return false
}

// bbRowColor is a row's colour: the rainbow of the cover.
var bbRowColors = []sdl.Color{
	{R: 255, G: 90, B: 110, A: 255}, {R: 255, G: 150, B: 70, A: 255}, {R: 255, G: 214, B: 70, A: 255},
	{R: 90, G: 220, B: 120, A: 255}, {R: 70, G: 180, B: 255, A: 255}, {R: 170, G: 110, B: 255, A: 255},
	{R: 255, G: 110, B: 200, A: 255}, {R: 120, G: 230, B: 230, A: 255},
}

func renderBrickBreaker(renderer *sdl.Renderer, config *Config) {
	bbPoll(config)
	ox := (screenWidth - bbW) / 2
	oy := (screenHeight - bbH) / 2
	strokeRoundedRect(renderer, ox-4, oy-4, bbW+8, bbH+8, 8, 2, ColorBorder)
	for r := 0; r < bb.rows; r++ {
		for c := 0; c < bbCols; c++ {
			hits := bb.bricks[r][c]
			if hits <= 0 {
				continue
			}
			x, y, w, h := bbBrickRect(r, c)
			col := bbRowColors[r%len(bbRowColors)]
			if hits > 1 {
				col = WithAlpha(col, 255)
				strokeRoundedRect(renderer, ox+int32(x)-2, oy+int32(y)-2, int32(w)+4, int32(h)+4, 5, 2, ColorTextPrimary())
			}
			fillRoundedRect(renderer, ox+int32(x), oy+int32(y), int32(w), int32(h), 4, col)
		}
	}
	accent := getAccentColor(config)
	fillRoundedRect(renderer, ox+int32(bb.padX), oy+bbPadY, int32(bb.padW), bbPadH, 6, accent)
	fillCircle(renderer, ox+int32(bb.ballX), oy+int32(bb.ballY), bbBallR, ColorTextPrimary())

	font, _ := getCachedFont(config, "medium")
	small, _ := getCachedFont(config, "small")
	if font != nil {
		status := fmt.Sprintf("Score %d   ·   Level %d   ·   Lives %d", gameScore, bb.level, bb.lives)
		sw, _ := measureText(font, status)
		drawText(renderer, font, status, (screenWidth-sw)/2, oy-int32(font.Height())-10, accent, textAlignLeft)
		if bb.flash > 0 {
			msg := fmt.Sprintf("Level %d", bb.level)
			mw, _ := measureText(font, msg)
			drawText(renderer, font, msg, (screenWidth-mw)/2, oy+bbH/2+40, ColorTextPrimary(), textAlignLeft)
		}
	}
	if small != nil && !gameOver {
		hint := hintLine(FooterHint{"Left/Right", "Move"}, FooterHint{"A", "Launch"}, FooterHint{"B", "Quit"})
		if !bb.stuck {
			hint = hintLine(FooterHint{"Left/Right", "Move"}, FooterHint{"B", "Quit"})
		}
		hw, _ := measureText(small, hint)
		drawText(renderer, small, hint, (screenWidth-hw)/2, oy+bbH+14, ColorTextTertiary(), textAlignLeft)
	}
	if gameOver {
		renderGameOver(renderer, config, "Brick Breaker", gameScore)
	}
}
