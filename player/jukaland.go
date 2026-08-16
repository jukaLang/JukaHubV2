package main

import (
	"encoding/json"
	"math"
	"math/rand"
	"os"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const tileSize = 32
const worldW = 50
const worldH = 28
const worldPixelsW = worldW * tileSize
const worldPixelsH = worldH * tileSize

const tileSky = 0
const tileGrass = 1
const tileDirt = 2
const tileStone = 3
const tileWood = 4
const tileLeaves = 5
const tileSand = 6
const tileBrick = 7
const tilePlanks = 8
const tileCobble = 9

const playerSpeed = 280.0
const playerJump = 420.0
const playerGravity = 980.0
const playerMaxFall = 800.0
const carSpeed = 520.0

type JukaLandState struct {
	PlayerX     float64
	PlayerY     float64
	PlayerVX    float64
	PlayerVY    float64
	OnGround    bool
	FacingRight bool
	InCar       bool
	CarX        float64
	CarY        float64

	TileMap [worldH][worldW]uint8

	Inventory map[uint8]int
	Selected  uint8

	CameraX float64
	CameraY float64

	CraftOpen bool
	CraftSel  int

	GameOver bool
	LastTime float64

	Score   int
	Health  int
	Slimes  []Slime
}

type Slime struct {
	X      float64
	Y      float64
	VX     float64
	Dir    float64
	Alive  bool
	HP     int
	MaxHP  int
}

var jukaland JukaLandState
var jukalandInited bool

func initJukaLand() {
	if jukalandInited {
		return
	}
	loadJukaLand()
	if jukaland.GameOver || jukaland.Health <= 0 {
		resetJukaLand()
	}
	jukalandInited = true
}

func resetJukaLand() {
	rand.Seed(time.Now().UnixNano())
	jukaland.PlayerX = float64(worldPixelsW / 2)
	jukaland.PlayerY = float64(worldPixelsH - 200)
	jukaland.PlayerVX = 0
	jukaland.PlayerVY = 0
	jukaland.OnGround = false
	jukaland.FacingRight = true
	jukaland.InCar = false
	jukaland.CarX = 0
	jukaland.CarY = 0
	jukaland.CameraX = jukaland.PlayerX - float64(screenWidth)/2
	jukaland.CameraY = jukaland.PlayerY - float64(screenHeight)/2
	jukaland.Inventory = map[uint8]int{
		tilePlanks: 4,
		tileBrick:  2,
	}
	jukaland.Selected = tilePlanks
	jukaland.CraftOpen = false
	jukaland.CraftSel = 0
	jukaland.GameOver = false
	jukaland.Health = 100
	jukaland.Score = 0
	jukaland.LastTime = float64(sdl.GetTicks64()) / 1000.0
	jukaland.Slimes = nil

	for y := 0; y < worldH; y++ {
		for x := 0; x < worldW; x++ {
			jukaland.TileMap[y][x] = tileSky
		}
	}

	for x := 0; x < worldW; x++ {
		h := 18 + rand.Intn(4)
		for y := worldH - h; y < worldH; y++ {
			if y == worldH-h {
				jukaland.TileMap[y][x] = tileGrass
			} else if y > worldH-h+3 {
				jukaland.TileMap[y][x] = tileStone
			} else {
				jukaland.TileMap[y][x] = tileDirt
			}
		}
	}

	for i := 0; i < 20; i++ {
		tx := 5 + rand.Intn(worldW-10)
		ty := worldH - 22 - rand.Intn(4)
		if ty < 0 {
			ty = 0
		}
		baseY := ty
		for th := 3 + rand.Intn(2); th > 0; th-- {
			if baseY+th < worldH {
				jukaland.TileMap[baseY+th][tx] = tileWood
			}
		}
		for dy := -2; dy <= 2; dy++ {
			for dx := -2; dx <= 2; dx++ {
				ly, lx := baseY-2+dy, tx+dx
				if lx >= 0 && lx < worldW && ly >= 0 && ly < worldH {
					if jukaland.TileMap[ly][lx] == tileSky {
						if (dx*dx + dy*dy) <= 5 {
							jukaland.TileMap[ly][lx] = tileLeaves
						}
					}
				}
			}
		}
	}

	for i := 0; i < 8; i++ {
		tx := 3 + rand.Intn(worldW-6)
		ty := worldH - 22 - rand.Intn(3)
		if ty < 0 {
			ty = 0
		}
		for dy := 0; dy < 3; dy++ {
			for dx := 0; dx < 3; dx++ {
				ly, lx := ty+dy, tx+dx
				if lx >= 0 && lx < worldW && ly >= 0 && ly < worldH {
					if jukaland.TileMap[ly][lx] == tileSky || jukaland.TileMap[ly][lx] == tileGrass {
						jukaland.TileMap[ly][lx] = tileSand
					}
				}
			}
		}
	}

	for i := 0; i < 6; i++ {
		tx := 8 + rand.Intn(worldW-16)
		ty := worldH - 20 - rand.Intn(8)
		if ty < 0 {
			ty = 0
		}
		jukaland.Slimes = append(jukaland.Slimes, Slime{
			X: float64(tx * tileSize),
			Y: float64(ty * tileSize),
			VX: 40 + float64(rand.Intn(60)),
			Dir: 1,
			Alive: true,
			HP: 30,
			MaxHP: 30,
		})
	}

	saveJukaLand()
}

func saveJukaLand() {
	data, err := json.Marshal(jukaland)
	if err != nil {
		return
	}
	os.WriteFile("jukaland.json", data, 0644)
}

func loadJukaLand() {
	data, err := os.ReadFile("jukaland.json")
	if err != nil {
		resetJukaLand()
		return
	}
	if err := json.Unmarshal(data, &jukaland); err != nil {
		resetJukaLand()
	}
}

func isSolid(t uint8) bool {
	return t != tileSky && t != tileLeaves
}

func tileAt(px, py float64) uint8 {
	tx := int(px / tileSize)
	ty := int(py / tileSize)
	if tx < 0 || ty < 0 || ty >= worldH || tx >= worldW {
		return tileStone
	}
	return jukaland.TileMap[ty][tx]
}

func setTile(tx, ty int, t uint8) {
	if tx >= 0 && ty >= 0 && ty < worldH && tx < worldW {
		jukaland.TileMap[ty][tx] = t
	}
}

func worldBounds() (float64, float64, float64, float64) {
	return 0, 0, float64(worldPixelsW), float64(worldPixelsH)
}

func updateJukaLand() {
	now := float64(sdl.GetTicks64()) / 1000.0
	dt := now - jukaland.LastTime
	if dt <= 0 {
		dt = 0.016
	}
	if dt > 0.1 {
		dt = 0.1
	}
	jukaland.LastTime = now

	if jukaland.GameOver {
		return
	}

	if jukaland.CraftOpen {
		return
	}

	moveX := jukalandMoveX
	moveY := jukalandMoveY

	keys := sdl.GetKeyboardState()
	if keys[sdl.SCANCODE_W] != 0 || keys[sdl.SCANCODE_UP] != 0 {
		moveY = -1
	}
	if keys[sdl.SCANCODE_S] != 0 || keys[sdl.SCANCODE_DOWN] != 0 {
		moveY = 1
	}
	if keys[sdl.SCANCODE_A] != 0 || keys[sdl.SCANCODE_LEFT] != 0 {
		moveX = -1
	}
	if keys[sdl.SCANCODE_D] != 0 || keys[sdl.SCANCODE_RIGHT] != 0 {
		moveX = 1
	}

	if moveX != 0 || moveY != 0 {
		len := math.Hypot(moveX, moveY)
		moveX /= len
		moveY /= len
	}

	targetVX := moveX * playerSpeed
	if jukaland.InCar {
		targetVX = moveX * carSpeed
		jukaland.PlayerVY = moveY * carSpeed
	} else {
		if math.Abs(jukaland.PlayerVX-targetVX) > 10 {
			if jukaland.PlayerVX < targetVX {
				jukaland.PlayerVX += 1200 * dt
			} else {
				jukaland.PlayerVX -= 1200 * dt
			}
		} else {
			jukaland.PlayerVX = targetVX
		}
		jukaland.PlayerVY += playerGravity * dt
		if jukaland.PlayerVY > playerMaxFall {
			jukaland.PlayerVY = playerMaxFall
		}
	}

	nx := jukaland.PlayerX + jukaland.PlayerVX*dt
	ny := jukaland.PlayerY + jukaland.PlayerVY*dt

	pw := 24.0
	ph := 28.0
	if jukaland.InCar {
		pw = 40.0
		ph = 24.0
	}

	if jukaland.PlayerVX > 0 {
		jukaland.FacingRight = true
	} else if jukaland.PlayerVX < 0 {
		jukaland.FacingRight = false
	}

	cx := nx - pw/2
	cy := ny - ph
	collideX := false
	for dy := 0; dy < int(ph/tileSize)+1; dy++ {
		for dx := 0; dx < int(pw/tileSize)+1; dx++ {
			checkX := cx + float64(dx*tileSize)
			checkY := cy + float64(dy*tileSize)
			if isSolid(tileAt(checkX, checkY)) || isSolid(tileAt(checkX+tileSize-1, checkY)) {
				collideX = true
			}
		}
	}
	if !collideX {
		jukaland.PlayerX = nx
	} else {
		jukaland.PlayerVX = 0
	}

	cy = ny - ph
	cx = jukaland.PlayerX - pw/2
	collideY := false
	for dy := 0; dy < int(ph/tileSize)+1; dy++ {
		for dx := 0; dx < int(pw/tileSize)+1; dx++ {
			checkX := cx + float64(dx*tileSize)
			checkY := cy + float64(dy*tileSize)
			if isSolid(tileAt(checkX, checkY)) || isSolid(tileAt(checkX, checkY+tileSize-1)) {
				collideY = true
			}
		}
	}
	if !collideY {
		jukaland.PlayerY = ny
		jukaland.OnGround = false
	} else {
		if jukaland.PlayerVY > 0 {
			jukaland.OnGround = true
			snapY := math.Floor((ny-ph)/tileSize) * tileSize
			jukaland.PlayerY = snapY + ph
		}
		jukaland.PlayerVY = 0
	}

	if jukaland.OnGround && moveY < 0 && !jukaland.InCar {
		jukaland.PlayerVY = -playerJump
		jukaland.OnGround = false
	}

	if jukaland.InCar {
		jukaland.CarX = jukaland.PlayerX
		jukaland.CarY = jukaland.PlayerY + 12
	}

	minCX := 0.0
	maxCX := float64(worldPixelsW) - float64(screenWidth)
	minCY := 0.0
	maxCY := float64(worldPixelsH) - float64(screenHeight)
	if maxCX < minCX {
		maxCX = minCX
	}
	if maxCY < minCY {
		maxCY = minCY
	}
	targetCX := jukaland.PlayerX - float64(screenWidth)/2
	targetCY := jukaland.PlayerY - float64(screenHeight)/2
	jukaland.CameraX += (targetCX - jukaland.CameraX) * 0.12
	jukaland.CameraY += (targetCY - jukaland.CameraY) * 0.12
	if jukaland.CameraX < minCX {
		jukaland.CameraX = minCX
	}
	if jukaland.CameraX > maxCX {
		jukaland.CameraX = maxCX
	}
	if jukaland.CameraY < minCY {
		jukaland.CameraY = minCY
	}
	if jukaland.CameraY > maxCY {
		jukaland.CameraY = maxCY
	}

	updateJukaLandEnemies(dt)
	updateJukaLandInput()
}

func updateJukaLandEnemies(dt float64) {
	for i := range jukaland.Slimes {
		s := &jukaland.Slimes[i]
		if !s.Alive {
			continue
		}
		s.VX = 40 + float64(rand.Intn(20))
		s.X += s.VX * s.Dir * dt
		if s.X < 0 || s.X > float64(worldPixelsW) {
			s.Dir *= -1
			s.X = math.Max(0, math.Min(s.X, float64(worldPixelsW)))
		}
		groundY := float64(worldH-1) * tileSize
		colX := s.X
		colY := groundY
		if !isSolid(tileAt(colX, colY)) {
			ty := int(groundY / tileSize)
			for ty > 0 && !isSolid(tileAt(colX, float64(ty)*tileSize)) {
				ty--
			}
			s.Y = float64(ty)*tileSize + tileSize
		} else {
			s.Y = groundY
		}
		s.Y = math.Min(s.Y, float64(worldH*tileSize))

		px := jukaland.PlayerX
		py := jukaland.PlayerY - 14
		dist := math.Hypot(px-s.X, py-s.Y)
		if dist < 30 && jukaland.PlayerVY > 0 && jukaland.PlayerY < s.Y-10 {
			s.HP -= 30
			jukaland.PlayerVY = -playerJump * 0.8
			if s.HP <= 0 {
				s.Alive = false
				jukaland.Score += 100
			}
		} else if dist < 28 && !jukaland.InCar {
			jukaland.Health -= 15
			if jukaland.Health <= 0 {
				jukaland.Health = 0
				jukaland.GameOver = true
				saveJukaLand()
			}
		}
	}
}

func renderJukaLand(renderer *sdl.Renderer, config *Config) {
	renderer.SetDrawColor(20, 24, 40, 255)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	startTX := int(jukaland.CameraX / tileSize)
	startTY := int(jukaland.CameraY / tileSize)
	endTX := startTX + int(screenWidth/tileSize) + 1
	endTY := startTY + int(screenHeight/tileSize) + 1
	if startTX < 0 {
		startTX = 0
	}
	if startTY < 0 {
		startTY = 0
	}
	if endTX > worldW {
		endTX = worldW
	}
	if endTY > worldH {
		endTY = worldH
	}

	for ty := startTY; ty < endTY; ty++ {
		for tx := startTX; tx < endTX; tx++ {
			t := jukaland.TileMap[ty][tx]
			sx := int32(float64(tx*tileSize) - jukaland.CameraX)
			sy := int32(float64(ty*tileSize) - jukaland.CameraY)
			switch t {
			case tileGrass:
				renderer.SetDrawColor(60, 160, 60, 255)
			case tileDirt:
				renderer.SetDrawColor(120, 80, 40, 255)
			case tileStone:
				renderer.SetDrawColor(100, 100, 110, 255)
			case tileWood:
				renderer.SetDrawColor(100, 60, 20, 255)
			case tileLeaves:
				renderer.SetDrawColor(20, 100, 30, 255)
			case tileSand:
				renderer.SetDrawColor(200, 180, 120, 255)
			case tileBrick:
				renderer.SetDrawColor(160, 60, 50, 255)
			case tilePlanks:
				renderer.SetDrawColor(140, 100, 60, 255)
			default:
				continue
			}
			renderer.FillRect(&sdl.Rect{X: sx, Y: sy, W: tileSize, H: tileSize})
			renderer.SetDrawColor(0, 0, 0, 40)
			renderer.DrawRect(&sdl.Rect{X: sx, Y: sy, W: tileSize, H: tileSize})
		}
	}

	for _, s := range jukaland.Slimes {
		if !s.Alive {
			continue
		}
		sx := int32(s.X - jukaland.CameraX - 12)
		sy := int32(s.Y - jukaland.CameraY - 12)
		renderer.SetDrawColor(80, 200, 80, 255)
		renderer.FillRect(&sdl.Rect{X: sx, Y: sy, W: 24, H: 24})
		renderer.SetDrawColor(60, 160, 60, 255)
		renderer.FillRect(&sdl.Rect{X: sx + 4, Y: sy + 4, W: 16, H: 8})
		renderer.SetDrawColor(40, 120, 40, 255)
		renderer.FillRect(&sdl.Rect{X: sx + 2, Y: sy + 16, W: 20, H: 8})
	}

	if jukaland.InCar {
		cx := int32(jukaland.CarX - jukaland.CameraX - 20)
		cy := int32(jukaland.CarY - jukaland.CameraY - 12)
		renderer.SetDrawColor(220, 60, 40, 255)
		renderer.FillRect(&sdl.Rect{X: cx, Y: cy, W: 40, H: 24})
		renderer.SetDrawColor(255, 200, 50, 255)
		renderer.FillRect(&sdl.Rect{X: cx + 4, Y: cy + 4, W: 12, H: 10})
		renderer.FillRect(&sdl.Rect{X: cx + 24, Y: cy + 4, W: 12, H: 10})
		renderer.SetDrawColor(40, 40, 40, 255)
		renderer.FillRect(&sdl.Rect{X: cx + 2, Y: cy + 16, W: 10, H: 8})
		renderer.FillRect(&sdl.Rect{X: cx + 28, Y: cy + 16, W: 10, H: 8})
	}

	px := int32(jukaland.PlayerX - jukaland.CameraX - 12)
	py := int32(jukaland.PlayerY - jukaland.CameraY - 28)
	if jukaland.InCar {
		px = int32(jukaland.CarX - jukaland.CameraX - 10)
		py = int32(jukaland.CarY - jukaland.CameraY - 28)
	}
	renderer.SetDrawColor(80, 140, 220, 255)
	renderer.FillRect(&sdl.Rect{X: px, Y: py, W: 24, H: 28})
	renderer.SetDrawColor(255, 255, 255, 255)
	renderer.FillRect(&sdl.Rect{X: px + 6, Y: py + 6, W: 4, H: 4})
	renderer.FillRect(&sdl.Rect{X: px + 14, Y: py + 6, W: 4, H: 4})
	renderer.SetDrawColor(40, 40, 60, 255)
	renderer.FillRect(&sdl.Rect{X: px + 6, Y: py + 12, W: 4, H: 2})
	renderer.FillRect(&sdl.Rect{X: px + 14, Y: py + 12, W: 4, H: 2})

	font, _ := getCachedFont(config, "small")
	if font != nil {
		renderText(renderer, config, font, fmt.Sprintf("JukaLand  Score: %d", jukaland.Score), sdl.Color{R: 240, G: 245, B: 255, A: 255}, 10, 10)
		renderText(renderer, config, font, fmt.Sprintf("Health: %d%%", jukaland.Health), sdl.Color{R: 80, G: 220, B: 120, A: 255}, 10, 36)
		carText := "No car"
		if jukaland.InCar {
			carText = "In Car"
		}
		renderText(renderer, config, font, carText, sdl.Color{R: 255, G: 220, B: 100, A: 255}, 10, 62)
		if jukaland.CraftOpen {
			panelW := int32(400)
			panelH := int32(260)
			panelX := (screenWidth - panelW) / 2
			panelY := (screenHeight - panelH) / 2
			fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 12, WithAlpha(ColorSurfaceRaised, 235))
			renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 255)
			renderer.FillRect(&sdl.Rect{X: panelX, Y: panelY, W: panelW, H: 3})
			renderText(renderer, config, font, "Crafting", sdl.Color{R: 255, G: 255, B: 255, A: 255}, panelX+16, panelY+14)

			recipes := [][3]string{
				{"4 Wood -> 8 Planks", fmt.Sprintf("%d", jukaland.Inventory[tileWood])},
				{"3 Stone -> 1 Brick", fmt.Sprintf("%d", jukaland.Inventory[tileStone])},
				{"4 Wood + 2 Stone -> Car", fmt.Sprintf("%d/%d", jukaland.Inventory[tileWood], jukaland.Inventory[tileStone])},
			}
			for i, r := range recipes {
				ry := panelY + 50 + int32(i)*40
				color := ColorTextSecondary()
				if i == jukaland.CraftSel {
					color = sdl.Color{R: 100, G: 180, B: 255, A: 255}
					fillRoundedRect(renderer, panelX+10, ry-4, panelW-20, 34, 8, WithAlpha(ColorSurfaceCard, 200))
				}
				renderText(renderer, config, font, r[0], color, panelX+24, ry)
				renderText(renderer, config, font, r[1], ColorTextTertiary(), panelX+300, ry)
			}
			renderText(renderer, config, font, "A: Craft  B: Close", ColorTextTertiary(), panelX+16, panelY+panelH-36)
		}
	}
}

func handleJukaLandInput(e *sdl.KeyboardEvent) {
	if e.Type != sdl.KEYDOWN {
		return
	}
	if jukaland.CraftOpen {
		switch e.Keysym.Sym {
		case sdl.K_UP:
			jukaland.CraftSel--
			if jukaland.CraftSel < 0 {
				jukaland.CraftSel = 2
			}
		case sdl.K_DOWN:
			jukaland.CraftSel++
			if jukaland.CraftSel > 2 {
				jukaland.CraftSel = 0
			}
		case sdl.K_a, sdl.K_RETURN, sdl.K_SPACE:
			craftItem()
		case sdl.K_b, sdl.K_ESCAPE:
			jukaland.CraftOpen = false
		}
		return
	}

	switch e.Keysym.Sym {
	case sdl.K_ESCAPE:
		if jukaland.InCar {
			jukaland.InCar = false
			jukaland.PlayerX = jukaland.CarX
			jukaland.PlayerY = jukaland.CarY - 20
		} else {
			jukaland.GameOver = true
			saveJukaLand()
			changeSceneTo(appConfig, findSceneIndex(appConfig, "Misc"))
		}
	case sdl.K_RETURN, sdl.K_SPACE:
		startJukaLandCraft()
	case sdl.K_e:
		if jukaland.InCar {
			jukaland.InCar = false
			jukaland.PlayerX = jukaland.CarX
			jukaland.PlayerY = jukaland.CarY - 20
		} else if !jukaland.InCar && jukaland.CarX != 0 {
			dx := jukaland.PlayerX - jukaland.CarX
			dy := jukaland.PlayerY - jukaland.CarY
			if math.Hypot(dx, dy) < 60 {
				jukaland.InCar = true
			}
		}
	case sdl.K_q:
		placeBlock()
	case sdl.K_a, sdl.K_x:
		breakBlock()
	case sdl.K_1:
		jukaland.Selected = tilePlanks
	case sdl.K_2:
		jukaland.Selected = tileBrick
	case sdl.K_3:
		jukaland.Selected = tileGrass
	}
}

func handleJukaLandController(e *sdl.ControllerButtonEvent) {
	if e.Type != sdl.CONTROLLERBUTTONDOWN {
		return
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_BACK:
		jukalandBtnExit = true
	case sdl.CONTROLLER_BUTTON_START:
		jukalandBtnCraft = true
	case sdl.CONTROLLER_BUTTON_A:
		jukalandBtnJump = true
	case sdl.CONTROLLER_BUTTON_B:
		jukalandBtnBreak = true
	case sdl.CONTROLLER_BUTTON_X:
		jukalandBtnPlace = true
	case sdl.CONTROLLER_BUTTON_Y:
		if jukaland.InCar {
			jukaland.InCar = false
			jukaland.PlayerX = jukaland.CarX
			jukaland.PlayerY = jukaland.CarY - 20
		} else if !jukaland.InCar && jukaland.CarX != 0 {
			dx := jukaland.PlayerX - jukaland.CarX
			dy := jukaland.PlayerY - jukaland.CarY
			if math.Hypot(dx, dy) < 60 {
				jukaland.InCar = true
			}
		}
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		jukalandMoveX = -1
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		jukalandMoveX = 1
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		jukalandMoveY = -1
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		jukalandMoveY = 1
	}
}

func updateJukaLandInput() {
	if jukalandBtnExit {
		jukalandBtnExit = false
		if jukaland.InCar {
			jukaland.InCar = false
			jukaland.PlayerX = jukaland.CarX
			jukaland.PlayerY = jukaland.CarY - 20
		} else {
			jukaland.GameOver = true
			saveJukaLand()
			changeSceneTo(appConfig, findSceneIndex(appConfig, "Misc"))
		}
	}
	if jukalandBtnCraft {
		jukalandBtnCraft = false
		startJukaLandCraft()
	}
	if jukalandBtnBreak {
		jukalandBtnBreak = false
		if jukaland.CraftOpen {
			craftItem()
		} else {
			breakBlock()
		}
	}
	if jukalandBtnJump {
		jukalandBtnJump = false
		if jukaland.CraftOpen {
			jukaland.CraftOpen = false
		} else if jukaland.OnGround && !jukaland.InCar {
			jukaland.PlayerVY = -playerJump
			jukaland.OnGround = false
		}
	}
	if jukalandBtnPlace {
		jukalandBtnPlace = false
		if !jukaland.CraftOpen {
			placeBlock()
		}
	}
}

func breakBlock() {
	reach := 50.0
	dirX := 0.0
	if jukaland.FacingRight {
		dirX = 1
	}
	tx := int((jukaland.PlayerX + dirX*reach) / tileSize)
	ty := int((jukaland.PlayerY - 14) / tileSize)
	if tx < 0 || ty < 0 || ty >= worldH || tx >= worldW {
		return
	}
	t := jukaland.TileMap[ty][tx]
	if t != tileSky && t != tileLeaves {
		jukaland.Inventory[t]++
		setTile(tx, ty, tileSky)
		saveJukaLand()
	}
}

func placeBlock() {
	reach := 50.0
	dirX := 0.0
	if jukaland.FacingRight {
		dirX = 1
	} else {
		dirX = -1
	}
	tx := int((jukaland.PlayerX + dirX*reach) / tileSize)
	ty := int((jukaland.PlayerY - 14) / tileSize)
	if tx < 0 || ty < 0 || ty >= worldH || tx >= worldW {
		return
	}
	if jukaland.Inventory[jukaland.Selected] <= 0 {
		return
	}
	if jukaland.TileMap[ty][tx] != tileSky {
		return
	}
	setTile(tx, ty, jukaland.Selected)
	jukaland.Inventory[jukaland.Selected]--
	saveJukaLand()
}

func startJukaLandCraft() {
	jukaland.CraftOpen = true
	jukaland.CraftSel = 0
}

func craftItem() {
	switch jukaland.CraftSel {
	case 0:
		if jukaland.Inventory[tileWood] >= 4 {
			jukaland.Inventory[tileWood] -= 4
			jukaland.Inventory[tilePlanks] += 8
		}
	case 1:
		if jukaland.Inventory[tileStone] >= 3 {
			jukaland.Inventory[tileStone] -= 3
			jukaland.Inventory[tileBrick] += 1
		}
	case 2:
		if jukaland.Inventory[tileWood] >= 4 && jukaland.Inventory[tileStone] >= 2 {
			jukaland.Inventory[tileWood] -= 4
			jukaland.Inventory[tileStone] -= 2
			jukaland.CarX = jukaland.PlayerX
			jukaland.CarY = jukaland.PlayerY
		}
	}
	saveJukaLand()
}
