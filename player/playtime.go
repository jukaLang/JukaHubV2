package main

// Playtime & achievements. Tracking is deliberately cheap: one wall-clock
// sample per frame, integer seconds, and a structured snapshot saved through
// the existing user-config debounce. The screen is a registered scene so it
// owns its own rendering without adding config elements.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	playtimeCustomKey     = "playtime_v1"
	playtimeSceneName     = "Playtime"
	playtimeSaveInterval  = 30 * time.Second
	playtimeMaxFrameDelta = 5 * time.Second
)

type playtimeData struct {
	Total    int64             `json:"total"`
	Scenes   map[string]int64  `json:"scenes"`
	Days     map[string]int64  `json:"days"`
	Unlocked map[string]string `json:"unlocked"`
}

type playtimeState struct {
	loaded   bool
	data     playtimeData
	lastTick time.Time
	lastSave time.Time
	carry    float64
}

var playtime playtimeState

func newPlaytimeData() playtimeData {
	return playtimeData{
		Scenes:   map[string]int64{},
		Days:     map[string]int64{},
		Unlocked: map[string]string{},
	}
}

// playtimeInit decodes the persisted snapshot, tolerating the map form that
// comes back from JSON and the typed struct form used in-process.
func playtimeInit(config *Config) {
	playtime = playtimeState{data: newPlaytimeData()}
	if config != nil {
		if raw, ok := config.Variables.Custom[playtimeCustomKey]; ok {
			if b, err := json.Marshal(raw); err == nil {
				var data playtimeData
				if err := json.Unmarshal(b, &data); err == nil {
					if data.Scenes == nil {
						data.Scenes = map[string]int64{}
					}
					if data.Days == nil {
						data.Days = map[string]int64{}
					}
					if data.Unlocked == nil {
						data.Unlocked = map[string]string{}
					}
					playtime.data = data
				}
			}
		}
	}
	playtime.loaded = true
	playtime.lastTick = time.Now()
	playtime.lastSave = time.Now()
}

func playtimePersist(config *Config) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	playtimePruneDays()
	config.Variables.Custom[playtimeCustomKey] = playtime.data
	playtime.lastSave = time.Now()
	saveUserConfigDebounced(userConfigFromVariables(config))
}

// playtimeFlush is called on graceful exit so the last partial interval and
// any freshly unlocked achievement are not lost.
func playtimeFlush(config *Config) {
	if !playtime.loaded || config == nil {
		return
	}
	playtimeTick(config)
	playtimePruneDays()
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[playtimeCustomKey] = playtime.data
	// Synchronous on exit: the debounced writer can be still pending when the
	// process closes, which used to lose the last few seconds of stats.
	saveUserConfig(userConfigFromVariables(config))
}

func playtimePruneDays() {
	cutoff := time.Now().AddDate(0, 0, -90).Format("2006-01-02")
	for day := range playtime.data.Days {
		if day < cutoff {
			delete(playtime.data.Days, day)
		}
	}
}

func playtimeTodayKey() string {
	return time.Now().Format("2006-01-02")
}

func playtimeRecord(seconds int64, scene string) {
	if seconds <= 0 {
		return
	}
	if scene == "" {
		scene = "Home"
	}
	if playtime.data.Scenes == nil {
		playtime.data.Scenes = map[string]int64{}
	}
	if playtime.data.Days == nil {
		playtime.data.Days = map[string]int64{}
	}
	if playtime.data.Unlocked == nil {
		playtime.data.Unlocked = map[string]string{}
	}
	playtime.data.Total += seconds
	playtime.data.Scenes[scene] += seconds
	playtime.data.Days[playtimeTodayKey()] += seconds
	playtimeCheckAchievements()
}

// playtimeActiveScene reports the canonical scene key used for stats. The
// onboarding tour is not an app and is therefore not counted.
func playtimeActiveScene(config *Config) string {
	if config == nil || onboarding.active || screensaverActiveNow() {
		return ""
	}
	if currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return ""
	}
	return config.Scenes[currentSceneIndex].Name
}

// screensaverActiveNow keeps the tracker out of the screensaver package's
// internals: the screensaver's active flag is the app-wide idle state.
func screensaverActiveNow() bool {
	return ss.active
}

func playtimeTick(config *Config) {
	if !playtime.loaded {
		playtimeInit(config)
		return
	}
	now := time.Now()
	if playtime.lastTick.IsZero() {
		playtime.lastTick = now
		return
	}
	delta := now.Sub(playtime.lastTick)
	playtime.lastTick = now
	if delta <= 0 || delta > playtimeMaxFrameDelta {
		return
	}
	scene := playtimeActiveScene(config)
	if scene == "" {
		return
	}
	playtime.carry += delta.Seconds()
	whole := int64(playtime.carry)
	if whole <= 0 {
		return
	}
	playtime.carry -= float64(whole)
	playtimeRecord(whole, scene)

	if now.Sub(playtime.lastSave) >= playtimeSaveInterval {
		playtimePersist(config)
	}
}

// playtimeAchievements is the fixed badge set. The checks are based only on
// accumulated data, so they remain true after a restart.
type playtimeAchievement struct {
	ID    string
	Title string
	Desc  string
	Icon  string
	Check func(playtimeData) bool
}

var playtimeAchievements = []playtimeAchievement{
	{"first_steps", "First Steps", "Spend 10 minutes in JukaHub", "01", func(d playtimeData) bool { return d.Total >= 10*60 }},
	{"one_hour", "One More Level", "Spend 1 hour in JukaHub", "02", func(d playtimeData) bool { return d.Total >= 60*60 }},
	{"ten_hours", "Home Screen Hero", "Spend 10 hours in JukaHub", "03", func(d playtimeData) bool { return d.Total >= 10*60*60 }},
	{"gamer", "Gamer", "Play games for 30 minutes", "04", func(d playtimeData) bool { return d.Scenes["Games"] >= 30*60 }},
	{"audiophile", "Audiophile", "Listen to music for 30 minutes", "05", func(d playtimeData) bool { return d.Scenes["MusicPlayer"] >= 30*60 }},
	{"explorer", "Explorer", "Use 8 different screens", "06", func(d playtimeData) bool { return len(d.Scenes) >= 8 }},
}

func playtimeCheckAchievements() {
	for _, a := range playtimeAchievements {
		if _, ok := playtime.data.Unlocked[a.ID]; ok {
			continue
		}
		if !a.Check(playtime.data) {
			continue
		}
		playtime.data.Unlocked[a.ID] = time.Now().Format(time.RFC3339)
		ShowToast("Achievement unlocked: "+a.Title, ToastKindSuccess)
		noteAwayAchievement(a.Title)
	}
}

// sortedPlaytimeScenes returns scene totals in descending order.
func sortedPlaytimeScenes(data playtimeData) []struct {
	Name    string
	Seconds int64
} {
	out := make([]struct {
		Name    string
		Seconds int64
	}, 0, len(data.Scenes))
	for name, seconds := range data.Scenes {
		if seconds <= 0 {
			continue
		}
		out = append(out, struct {
			Name    string
			Seconds int64
		}{Name: name, Seconds: seconds})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Seconds == out[j].Seconds {
			return out[i].Name < out[j].Name
		}
		return out[i].Seconds > out[j].Seconds
	})
	return out
}

func formatPlaytime(seconds int64) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%dm", seconds/60)
	}
	h := seconds / 3600
	m := (seconds % 3600) / 60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %02dm", h, m)
}

// ensurePlaytimeScene adds the stats screen to configs that predate it.
func ensurePlaytimeScene(config *Config) {
	if config == nil || sceneByName(config, playtimeSceneName) != nil {
		return
	}
	config.Scenes = append(config.Scenes, SceneConfig{
		Name:        playtimeSceneName,
		Icon:        "T",
		Description: "Playtime and achievements",
		Background:  "#0B0F17",
	})
}

// ensurePlaytimeEntry adds a button to the Tools (Misc) page. It uses the free
// slot in the second row so existing buttons keep their positions.
func ensurePlaytimeEntry(config *Config) {
	scene := sceneByName(config, "Misc")
	if scene == nil {
		return
	}
	for _, el := range scene.Elements {
		if el.Trigger == "change_scene:"+playtimeSceneName || strings.EqualFold(el.TriggerTarget, playtimeSceneName) {
			return
		}
	}
	scene.Elements = append(scene.Elements, Element{
		Type:          "button",
		Text:          "Playtime & Achievements",
		X:             336,
		Y:             202,
		Width:         "296",
		Height:        "55",
		Trigger:       "change_scene:" + playtimeSceneName,
		TriggerTarget: playtimeSceneName,
		Color:         "$labelColor",
		BgColor:       "$buttonColor",
	})
}

// renderPlaytime draws the stats screen. All numbers are formatted, never raw
// seconds, so the handheld's tiny screen stays readable.
var playtimeRARect sdl.Rect

func renderPlaytime(renderer *sdl.Renderer, config *Config) {
	playtimeRARect = sdl.Rect{}
	renderer.SetDrawColor(9, 12, 20, 255)
	renderer.Clear()
	renderAmbientBackground(renderer)

	font, _ := getCachedFont(config, "small")
	med, _ := getCachedFont(config, "medium")
	big, _ := getCachedFont(config, "big")
	if font == nil {
		return
	}
	if med == nil {
		med = font
	}
	if big == nil {
		big = med
	}

	renderText(renderer, config, med, "Playtime & Achievements", ColorTextPrimary(), 28, 18)
	back := hintLine(FooterHint{"B", "Back"})
	bw, _, _ := font.SizeUTF8(back)
	renderText(renderer, config, font, back, ColorTextTertiary(), screenWidth-int32(bw)-28, 22)
	if retroAchievementsSupported() {
		raHint := hintLine(FooterHint{"Y", "RetroAchievements"})
		raw, _, _ := font.SizeUTF8(raHint)
		raHintW := int32(raw)
		x := screenWidth - int32(bw) - 28 - 24 - raHintW
		playtimeRARect = sdl.Rect{X: x - 8, Y: 16, W: raHintW + 16, H: 30}
		col := ColorTextTertiary()
		if raSettingsReady() {
			col = accentColor
		}
		renderText(renderer, config, font, raHint, col, x, 22)
	}

	// Summary strip.
	cardW := screenWidth - 56
	drawCard(renderer, 28, 64, cardW, 112, 16)
	total := formatPlaytime(playtime.data.Total)
	renderText(renderer, config, font, "TOTAL TIME", ColorTextTertiary(), 52, 82)
	renderText(renderer, config, big, total, ColorTextPrimary(), 52, 104)

	week := playtimeWeekSeconds(playtime.data)
	renderText(renderer, config, font, "THIS WEEK", ColorTextTertiary(), 360, 82)
	renderText(renderer, config, big, formatPlaytime(week), ColorTextPrimary(), 360, 104)

	unlocked := len(playtime.data.Unlocked)
	renderText(renderer, config, font, "BADGES", ColorTextTertiary(), 680, 82)
	renderText(renderer, config, big, fmt.Sprintf("%d / %d", unlocked, len(playtimeAchievements)), ColorTextPrimary(), 680, 104)

	// Top apps + weekly bars.
	leftW := int32(460)
	drawCard(renderer, 28, 194, leftW, 238, 16)
	renderText(renderer, config, font, "MOST USED", ColorTextTertiary(), 52, 214)
	rows := sortedPlaytimeScenes(playtime.data)
	if len(rows) == 0 {
		renderText(renderer, config, font, "Start exploring — your stats appear here.", ColorTextSecondary(), 52, 254)
	} else {
		max := rows[0].Seconds
		for i := 0; i < len(rows) && i < 5; i++ {
			y := int32(248 + i*34)
			name := sceneDisplayName(rows[i].Name)
			renderText(renderer, config, font, ellipsize(font, name, 180), ColorTextSecondary(), 52, y)
			barW := int32(0)
			if max > 0 {
				barW = int32(float64(rows[i].Seconds) / float64(max) * 190)
			}
			fillRoundedRect(renderer, 244, y+5, 190, 8, 4, WithAlpha(ColorBorder, 90))
			if barW > 0 {
				fillRoundedRect(renderer, 244, y+5, barW, 8, 4, accentColor)
			}
			val := formatPlaytime(rows[i].Seconds)
			vw, _, _ := font.SizeUTF8(val)
			renderText(renderer, config, font, val, ColorTextTertiary(), 444-int32(vw), y)
		}
	}

	rightX := int32(504)
	rightW := screenWidth - rightX - 28
	drawCard(renderer, rightX, 194, rightW, 238, 16)
	renderText(renderer, config, font, "LAST 7 DAYS", ColorTextTertiary(), rightX+24, 214)
	drawPlaytimeWeek(renderer, config, sdl.Rect{X: rightX + 24, Y: 244, W: rightW - 48, H: 158})

	// Achievements.
	achY := int32(448)
	achH := screenHeight - achY - 36
	if achH < 120 {
		achH = 120
	}
	drawCard(renderer, 28, achY, cardW, achH, 16)
	renderText(renderer, config, font, "ACHIEVEMENTS", ColorTextTertiary(), 52, achY+18)
	cols := 3
	cellW := (cardW - 48) / int32(cols)
	for i, a := range playtimeAchievements {
		col := i % cols
		row := i / cols
		x := 52 + int32(col)*cellW
		y := achY + 48 + int32(row)*62
		if y+54 > achY+achH {
			break
		}
		_, done := playtime.data.Unlocked[a.ID]
		iconCol := WithAlpha(ColorTextTertiary(), 150)
		titleCol := ColorTextTertiary()
		if done {
			iconCol = accentColor
			titleCol = ColorTextPrimary()
		}
		fillRoundedRect(renderer, x, y, 38, 38, 10, WithAlpha(iconCol, 30))
		iw, ih, _ := font.SizeUTF8(a.Icon)
		renderText(renderer, config, font, a.Icon, iconCol, x+(38-int32(iw))/2, y+(38-int32(ih))/2)
		renderText(renderer, config, font, ellipsize(font, a.Title, cellW-58), titleCol, x+48, y+2)
		renderText(renderer, config, font, ellipsize(font, a.Desc, cellW-58), ColorTextTertiary(), x+48, y+20)
	}
}

func playtimeWeekSeconds(data playtimeData) int64 {
	var total int64
	for i := 0; i < 7; i++ {
		total += data.Days[time.Now().AddDate(0, 0, -i).Format("2006-01-02")]
	}
	return total
}

func drawPlaytimeWeek(renderer *sdl.Renderer, config *Config, rect sdl.Rect) {
	values := make([]int64, 7)
	var max int64
	for i := 0; i < 7; i++ {
		values[i] = playtime.data.Days[time.Now().AddDate(0, 0, -6+i).Format("2006-01-02")]
		if values[i] > max {
			max = values[i]
		}
	}
	barW := int32(22)
	gap := int32(16)
	baseY := rect.Y + rect.H - 18
	for i, v := range values {
		x := rect.X + int32(i)*(barW+gap)
		h := int32(12)
		if max > 0 && v > 0 {
			h = int32(float64(v) / float64(max) * float64(rect.H-52))
			if h < 12 {
				h = 12
			}
		}
		col := WithAlpha(ColorBorder, 100)
		if v > 0 {
			col = WithAlpha(accentColor, 210)
		}
		fillRoundedRect(renderer, x, baseY-h, barW, h, 5, col)
		label := time.Now().AddDate(0, 0, -6+i).Format("Mon")[:1]
		// Initials avoid a 7×3-letter row on the handheld. Use the shared
		// renderer with the caller's font for the label.
		f, _ := getCachedFont(config, "small")
		if f != nil {
			lw, _, _ := f.SizeUTF8(label)
			renderText(renderer, config, f, label, ColorTextTertiary(), x+(barW-int32(lw))/2, baseY+4)
		}
	}
}

// Scene registration: the Playtime screen has no config elements, so it owns
// rendering and B/Escape navigation.
type playtimeScene struct{ sceneBase }

func (playtimeScene) render(r *sdl.Renderer, c *Config) bool {
	renderPlaytime(r, c)
	return true
}

func (playtimeScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_b:
		goBackScene(c)
		PlayBackSound()
	case sdl.K_y:
		openRetroAchievementsPanel()
	default:
		return false
	}
	return true
}

func (playtimeScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		goBackScene(c)
		PlayBackSound()
	case sdl.CONTROLLER_BUTTON_Y:
		openRetroAchievementsPanel()
	default:
		return false
	}
	return true
}

// handlePlaytimeMouse opens RetroAchievements from the touch target in the
// header. The scene has no config elements, so main.go forwards the click here.
func handlePlaytimeMouse(mx, my int32) bool {
	if playtimeRARect.W > 0 && pointInRect(mx, my, playtimeRARect) {
		openRetroAchievementsPanel()
		return true
	}
	return false
}

func init() { registerScene(playtimeScene{sceneBase{name: playtimeSceneName}}) }
