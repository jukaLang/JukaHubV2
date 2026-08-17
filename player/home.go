package main

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// This file routes the home (Main) scene through the dedicated HomeLayout
// renderer and the explicit home focus graph (homefocus.go), while keeping the
// generic config-driven scene renderer for every other scene.

var (
	homeLayout      *HomeLayout
	homePressedID   HomeFocusID
	homePressedAt   uint64
	homeTileByIndex = map[int]HomeFocusID{} // Main scene element index -> home item

	homeFontsOnce  sync.Once
	homeFontsCache HomeFonts
)

// homePressDuration is the visual "pressed" feedback window before the action
// takes effect (spec: 70-100 ms). The scene transition begins right after, so
// the darkened card is visible as feedback starts.
const homePressDuration uint64 = 90

// refreshHomeLayout syncs the home layout state with the active scene. It is
// called every frame from renderScene and lazily builds the layout the first
// time the home scene is shown.
func refreshHomeLayout(config *Config) {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		homeLayoutActive = false
		return
	}
	scene := config.Scenes[currentSceneIndex]
	homeLayoutActive = scene.Layout == "home"
	if !homeLayoutActive {
		return
	}
	if homeLayout == nil {
		homeLayout = NewHomeLayout(screenWidth, screenHeight)
	} else if homeLayout.Width != screenWidth || homeLayout.Height != screenHeight {
		homeLayout.Resize(screenWidth, screenHeight)
	}

	// Map config tile elements to home items so activation reuses the triggers
	// declared in jukaconfig.json ("Preserve the existing activation trigger").
	if len(homeTileByIndex) == 0 {
		for i, el := range scene.Elements {
			if el.Type != "button" || el.Style != "tile" {
				continue
			}
			title := strings.TrimSpace(el.Text)
			for _, id := range homeLayout.Order {
				if id == HomeFocusContinue {
					continue
				}
				item, ok := homeLayout.Items[id]
				if !ok {
					continue
				}
				if strings.EqualFold(strings.TrimSpace(item.Title), title) ||
					(id == HomeFocusTools && strings.Contains(strings.ToLower(title), "tools")) {
					homeTileByIndex[i] = id
					break
				}
			}
		}
	}

	// Clear the pressed feedback once the short visual window has elapsed.
	if homePressedID != "" && sdl.GetTicks64()-homePressedAt > homePressDuration {
		homePressedID = ""
	}
}

// homeTileRect is a compatibility shim for the legacy tile renderer so the
// render + mouse code stays consistent even though the home scene now renders
// through HomeLayout.
func homeTileRect(elemIndex int) (sdl.Rect, bool) {
	if homeLayout == nil {
		return sdl.Rect{}, false
	}
	id, ok := homeTileByIndex[elemIndex]
	if !ok {
		return sdl.Rect{}, false
	}
	item, ok := homeLayout.Items[id]
	if !ok {
		return sdl.Rect{}, false
	}
	return item.Rect, true
}

// homeRecentRect returns the Continue card rect for the legacy recent panel.
func homeRecentRect() (sdl.Rect, bool) {
	if homeLayout == nil {
		return sdl.Rect{}, false
	}
	item, ok := homeLayout.Items[HomeFocusContinue]
	if !ok {
		return sdl.Rect{}, false
	}
	return item.Rect, true
}

// homeSceneTarget resolves a home item to a destination scene name. It prefers
// the configured tile triggers on the home scene; otherwise it falls back to a
// fixed mapping against the real scene names in jukaconfig.json.
func homeSceneTarget(config *Config, id HomeFocusID) string {
	if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
		for _, el := range config.Scenes[currentSceneIndex].Elements {
			if el.Type != "button" || el.Style != "tile" || el.Trigger == "" {
				continue
			}
			title := strings.TrimSpace(el.Text)
			matches := strings.EqualFold(title, string(id)) ||
				(id == HomeFocusTools && strings.Contains(strings.ToLower(title), "tools"))
			if id == HomeFocusContinue {
				matches = strings.EqualFold(title, "media")
			}
			if matches && strings.HasPrefix(el.Trigger, "change_scene:") {
				return strings.TrimPrefix(el.Trigger, "change_scene:")
			}
		}
	}
	switch id {
	case HomeFocusContinue, HomeFocusMedia:
		return "Tube"
	case HomeFocusFiles:
		return "FileExplorer"
	case HomeFocusPackages:
		return "Packages"
	case HomeFocusApps:
		return "Apps"
	case HomeFocusFavorites:
		return "Favorites"
	case HomeFocusChat:
		return "Chat"
	case HomeFocusTools:
		return "Misc"
	case HomeFocusSettings:
		return "Settings"
	}
	return ""
}

// homeActivate triggers the action for the currently focused home item.
// Continue resumes the most recent item when one exists, otherwise it opens
// Media. Every other item performs a scene change using its configured target.
func homeActivate(config *Config) {
	if homeLayout == nil {
		return
	}
	PlayActivateSound()
	id := homeLayout.Focus.Current()
	homePressedID = id
	homePressedAt = sdl.GetTicks64()

	if id == HomeFocusContinue {
		if items := getRecentItems(); len(items) > 0 {
			items[0].Play(config)
			return
		}
		id = HomeFocusMedia
	}
	target := homeSceneTarget(config, id)
	if target == "" {
		return
	}
	if idx := findSceneIndex(config, target); idx >= 0 {
		changeSceneTo(config, idx)
	}
}

// handleHomeKey processes keyboard input on the home scene. It returns true
// when the key was consumed so the generic element-based handler is skipped.
func handleHomeKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || homeLayout == nil {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		if homeLayout.Focus.Current() != homeLayout.Move(FocusUp) {
			PlayNavSound()
		}
	case sdl.K_DOWN:
		if homeLayout.Focus.Current() != homeLayout.Move(FocusDown) {
			PlayNavSound()
		}
	case sdl.K_LEFT:
		if homeLayout.Focus.Current() != homeLayout.Move(FocusLeft) {
			PlayNavSound()
		}
	case sdl.K_RIGHT:
		if homeLayout.Focus.Current() != homeLayout.Move(FocusRight) {
			PlayNavSound()
		}
	case sdl.K_RETURN, sdl.K_SPACE:
		homeActivate(config)
	case sdl.K_ESCAPE:
		// Home is the root scene; nothing to go back to.
	default:
		return false
	}
	return true
}

// handleHomeController processes controller input on the home scene. Shoulder
// buttons intentionally fall through so scene paging keeps working.
func handleHomeController(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN || homeLayout == nil {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if homeLayout.Focus.Current() != homeLayout.Move(FocusUp) {
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		if homeLayout.Focus.Current() != homeLayout.Move(FocusDown) {
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		if homeLayout.Focus.Current() != homeLayout.Move(FocusLeft) {
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		if homeLayout.Focus.Current() != homeLayout.Move(FocusRight) {
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_A:
		homeActivate(config)
	case sdl.CONTROLLER_BUTTON_B:
		// Home is the root scene; nothing to go back to.
	default:
		return false
	}
	return true
}

// handleHomeMouseClick selects and activates a home card under the cursor.
func handleHomeMouseClick(mx, my int32, config *Config) bool {
	if homeLayout == nil {
		return false
	}
	if homeLayout.FocusAt(mx, my) {
		// A clicked card is a deliberate choice, so it becomes the card
		// restored when the user returns to Home.
		homeLayout.Focus.Remember()
		homeActivate(config)
		return true
	}
	return false
}

// homeViewData assembles the per-frame data the HomeLayout renderer needs:
// greeting, header status (network / weather / time / battery), the Continue
// state resolved from the recently-played store, and pressed feedback.
// homeGreeting builds the greeting line, appending the configured username so
// the home screen feels personal without touching the header status cluster.
func homeGreeting(name string) string {
	g := defaultGreeting(time.Now())
	if n := strings.TrimSpace(name); n != "" {
		return g + ", " + n
	}
	return g
}

func homeViewData(config *Config) HomeViewData {
	data := HomeViewData{
		Greeting:     homeGreeting(""),
		Subtitle:     "What do you want to do?",
		Version:      versionString(config),
		StatusParts:  headerStatusParts(config),
		Pressed:      homePressedID,
		ShowBackHint: true,
	}
	if name, ok := config.Variables.Custom["TSPUsername"].(string); ok {
		data.Greeting = homeGreeting(name)
	}

	if items := getRecentItems(); len(items) > 0 {
		it := items[0]
		data.Continue = HomeContinueState{
			HasRecent: true,
			Title:     it.Label(),
			Subtitle:  recentSourceLabel(it),
			Progress:  homeRecentProgress(it),
		}
		// Show the saved watch position on the subtitle so the resume point
		// is readable at a glance, e.g. "Video · 2h ago · 12:34 / 45:00".
		if it.Type == "video" {
			if v, ok := it.Data.(VideoInfo); ok && v.Duration > 0 && v.Position > 0 {
				pos := v.Position
				if pos > v.Duration {
					pos = v.Duration
				}
				data.Continue.Subtitle = fmt.Sprintf("%s · %s / %s", data.Continue.Subtitle, formatTime(pos), formatTime(v.Duration))
			}
		}
	}
	return data
}

// recentSourceLabel describes where a recent item came from for the Continue
// card's supporting text.
func recentSourceLabel(it FavoriteItem) string {
	label := ""
	switch it.Type {
	case "video":
		label = "Video"
	case "iptv":
		label = "Channel"
	case "file":
		label = "File"
	default:
		label = "Item"
	}
	if !it.Timestamp.IsZero() {
		label += " · " + timeAgo(it.Timestamp)
	}
	return label
}

// homeRecentProgress returns the 0..1 playback fraction for the Continue
// card, derived from the persisted watch position on the most recent video.
func homeRecentProgress(it FavoriteItem) float32 {
	if it.Type != "video" {
		return 0
	}
	v, ok := it.Data.(VideoInfo)
	if !ok || v.Duration <= 0 || v.Position <= 0 {
		return 0
	}
	p := float32(v.Position / v.Duration)
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}
	return p
}

func timeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// renderHomeModern draws the home scene through the dedicated layout renderer.
// It owns the background so the layout stays focused on content.
func renderHomeModern(renderer *sdl.Renderer, config *Config) {
	if renderer == nil || config == nil {
		return
	}
	refreshHomeLayout(config)
	if homeLayout == nil {
		return
	}

	// Background: reuse the shared backdrop image/gradient path.
	ensureBackgroundTexture(renderer, config)
	if bgTexture != nil {
		_ = renderer.Copy(bgTexture, nil, &sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
		setDrawColor(renderer, overlayColor)
		_ = renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	} else {
		ensureGradientTexture(renderer, config, ColorBackground)
		if gradientTexture != nil {
			_ = renderer.Copy(gradientTexture, nil, &sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
		} else {
			setDrawColor(renderer, ColorBackground)
			_ = renderer.Clear()
		}
	}

	_ = homeLayout.Render(renderer, loadHomeFonts(), homeViewData(config))
}

// loadHomeFonts opens the bundled Inter font at the home-screen sizes,
// falling back to DejaVu when Inter is unavailable. Fonts are cached so the
// render loop never allocates per frame.
func loadHomeFonts() HomeFonts {
	homeFontsOnce.Do(func() {
		homeFontsCache = HomeFonts{
			Brand:    openHomeFont(26),
			Greeting: openHomeFont(32),
			Subtitle: openHomeFont(19),
			Card:     openHomeFont(23),
			Helper:   openHomeFont(17),
			Small:    openHomeFont(16),
		}
	})
	return homeFontsCache
}

func openHomeFont(size int) *ttf.Font {
	for _, candidate := range []string{"Inter-Regular.ttf", "DejaVuSans.ttf", "DejaVuSans-Bold.ttf"} {
		if f, err := ttf.OpenFont(resolvePath(candidate), size); err == nil {
			// Register in fontCache so the shutdown path closes it.
			fontCache["home_"+candidate+fmt.Sprint(size)] = f
			return f
		}
	}
	return nil
}
