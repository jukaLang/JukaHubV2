package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// --- Favorites types ---

type FavoriteItem struct {
	Type      string      `json:"type"`
	Data      interface{} `json:"data"`
	Timestamp time.Time   `json:"timestamp"`
}

func (f FavoriteItem) MarshalJSON() ([]byte, error) {
	type Alias FavoriteItem
	var dataBytes []byte
	var err error
	switch f.Type {
	case "video":
		if v, ok := f.Data.(VideoInfo); ok {
			dataBytes, err = json.Marshal(v)
		} else {
			dataBytes, err = json.Marshal(f.Data)
		}
	case "file", "iptv":
		if fe, ok := f.Data.(FileEntry); ok {
			dataBytes, err = json.Marshal(fe)
		} else {
			dataBytes, err = json.Marshal(f.Data)
		}
	default:
		dataBytes, err = json.Marshal(f.Data)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(&struct {
		Type      string
		Data      json.RawMessage
		Timestamp time.Time
	}{
		Type:      f.Type,
		Data:      dataBytes,
		Timestamp: f.Timestamp,
	})
}

func (f *FavoriteItem) UnmarshalJSON(data []byte) error {
	type Alias FavoriteItem
	aux := &struct {
		Data json.RawMessage
		*Alias
	}{
		Alias: (*Alias)(f),
	}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	switch f.Type {
	case "video":
		var v VideoInfo
		if err := json.Unmarshal(aux.Data, &v); err == nil {
			f.Data = v
			return nil
		}
	case "file", "iptv":
		var fe FileEntry
		if err := json.Unmarshal(aux.Data, &fe); err == nil {
			f.Data = fe
			return nil
		}
	}
	var raw interface{}
	if err := json.Unmarshal(aux.Data, &raw); err != nil {
		return err
	}
	f.Data = raw
	return nil
}

type FavoritesStore struct {
	Videos []FavoriteItem `json:"videos"`
	Recent []FavoriteItem `json:"recent"`
	Files  []FavoriteItem `json:"files"`
	IPTV   []FavoriteItem `json:"iptv"`
}

func (f *FavoriteItem) Label() string {
	switch f.Type {
	case "video":
		if v, ok := f.Data.(VideoInfo); ok {
			if v.Title != "" {
				return v.Title
			}
			return v.GetURL()
		}
	case "file":
		if fe, ok := f.Data.(FileEntry); ok {
			return fe.Name
		}
	case "iptv":
		if fe, ok := f.Data.(FileEntry); ok {
			return fe.Name
		}
	}
	return fmt.Sprintf("%v", f.Data)
}

func (f *FavoriteItem) Play(config *Config) {
	switch f.Type {
	case "video":
		if v, ok := f.Data.(VideoInfo); ok {
			playVideoURL(config, v.GetURL())
		}
	case "iptv":
		if fe, ok := f.Data.(FileEntry); ok {
			playStream(config, fe.Path)
		}
	case "file":
		if fe, ok := f.Data.(FileEntry); ok {
			openInExplorer(fe.Path)
		}
	}
}

// --- Favorites global state ---

var (
	favoritesStore      FavoritesStore
	favoritesMutex      sync.Mutex
	favoritesCurrentTab int
	favoritesFocusIndex int
)

const favoritesJSON = "favorites.json"

func loadFavorites() {
	loadFavoritesFrom(favoritesJSON)
}

func loadFavoritesFrom(path string) {
	favoritesMutex.Lock()
	defer favoritesMutex.Unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if err := json.Unmarshal(data, &favoritesStore); err != nil {
		log.Printf("loadFavorites: parse error: %v", err)
	}
	if favoritesStore.Videos == nil {
		favoritesStore.Videos = []FavoriteItem{}
	}
	if favoritesStore.Recent == nil {
		favoritesStore.Recent = []FavoriteItem{}
	}
	if favoritesStore.Files == nil {
		favoritesStore.Files = []FavoriteItem{}
	}
	if favoritesStore.IPTV == nil {
		favoritesStore.IPTV = []FavoriteItem{}
	}
	if favoritesFocusIndex < 0 {
		favoritesFocusIndex = 0
	}
}

func saveFavorites() {
	saveFavoritesTo(favoritesJSON)
}

func saveFavoritesTo(path string) {
	favoritesMutex.Lock()
	defer favoritesMutex.Unlock()
	data, err := json.MarshalIndent(favoritesStore, "", "  ")
	if err != nil {
		log.Printf("saveFavorites: marshal error: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		log.Printf("saveFavorites: write error: %v", err)
	}
}

func addFavoriteVideo(v VideoInfo) {
	favoritesMutex.Lock()
	defer favoritesMutex.Unlock()
	favoritesStore.Videos = removeDuplicateFavorite(favoritesStore.Videos, v.GetURL(), "video", v)
	favoritesStore.Videos = append([]FavoriteItem{{Type: "video", Data: v, Timestamp: time.Now()}}, favoritesStore.Videos...)
}

func addRecentFile(path string) {
	favoritesMutex.Lock()
	defer favoritesMutex.Unlock()
	fe := FileEntry{Name: filepath.Base(path), Path: path, IsDir: false}
	favoritesStore.Recent = removeDuplicateFavorite(favoritesStore.Recent, path, "file", fe)
	favoritesStore.Recent = append([]FavoriteItem{{Type: "file", Data: fe, Timestamp: time.Now()}}, favoritesStore.Recent...)
}

func addRecentIPTV(ch FileEntry) {
	favoritesMutex.Lock()
	defer favoritesMutex.Unlock()
	favoritesStore.IPTV = removeDuplicateFavorite(favoritesStore.IPTV, ch.Path, "iptv", ch)
	favoritesStore.IPTV = append([]FavoriteItem{{Type: "iptv", Data: ch, Timestamp: time.Now()}}, favoritesStore.IPTV...)
}

func removeDuplicateFavorite(list []FavoriteItem, key string, itemType string, data interface{}) []FavoriteItem {
	filtered := make([]FavoriteItem, 0, len(list))
	for _, item := range list {
		if item.Type == itemType {
			switch item.Type {
			case "video":
				if v, ok := item.Data.(VideoInfo); ok && v.GetURL() == key {
					continue
				}
			case "file", "iptv":
				if fe, ok := item.Data.(FileEntry); ok && fe.Path == key {
					continue
				}
			}
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func removeFavoriteAt(tabIndex int, idx int) {
	favoritesMutex.Lock()
	defer favoritesMutex.Unlock()
	switch tabIndex {
	case 0:
		if idx >= 0 && idx < len(favoritesStore.Videos) {
			favoritesStore.Videos = append(favoritesStore.Videos[:idx], favoritesStore.Videos[idx+1:]...)
		}
	case 1:
		if idx >= 0 && idx < len(favoritesStore.Recent) {
			favoritesStore.Recent = append(favoritesStore.Recent[:idx], favoritesStore.Recent[idx+1:]...)
		}
	case 2:
		if idx >= 0 && idx < len(favoritesStore.Files) {
			favoritesStore.Files = append(favoritesStore.Files[:idx], favoritesStore.Files[idx+1:]...)
		}
	case 3:
		if idx >= 0 && idx < len(favoritesStore.IPTV) {
			favoritesStore.IPTV = append(favoritesStore.IPTV[:idx], favoritesStore.IPTV[idx+1:]...)
		}
	}
	if favoritesFocusIndex < 0 {
		favoritesFocusIndex = 0
	}
}

func getCurrentFavorites() []FavoriteItem {
	favoritesMutex.Lock()
	defer favoritesMutex.Unlock()
	switch favoritesCurrentTab {
	case 0:
		return favoritesStore.Videos
	case 1:
		return favoritesStore.Recent
	case 2:
		return favoritesStore.Files
	case 3:
		return favoritesStore.IPTV
	default:
		return favoritesStore.Videos
	}
}

// --- Favorites rendering ---

var favTabRects [4]sdl.Rect
var favBackRect sdl.Rect
func renderFavorites(renderer *sdl.Renderer, config *Config, element Element) {
	tabLabels := []string{"Videos", "Recent", "Files", "IPTV"}
	tabColors := []sdl.Color{
		{R: 255, G: 59, B: 59, A: 255},
		{R: 59, G: 155, B: 255, A: 255},
		{R: 46, G: 204, B: 113, A: 255},
		{R: 155, G: 89, B: 182, A: 255},
	}
	tabWidth := int32(180)
	tabHeight := int32(40)
	tabY := element.Y + 8
	tabStartX := element.X + 10
	tabGap := int32(12)

	favTabRects = [4]sdl.Rect{}
	titleFont, _ := getCachedFont(config, "medium")
	font, _ := getCachedFont(config, "small")
	if font == nil {
		font = titleFont
	}

	for i, label := range tabLabels {
		tx := tabStartX + int32(i)*(tabWidth+tabGap)
		active := i == favoritesCurrentTab
		// inactive tab background
		fillRoundedRect(renderer, tx, tabY, tabWidth, tabHeight, 10, sdl.Color{R: 24, G: 28, B: 40, A: 255})
		if active {
			// active tab: solid color with subtle inner highlight
			fillRoundedRect(renderer, tx, tabY, tabWidth, tabHeight, 10, tabColors[i])
			fillRoundedRect(renderer, tx+2, tabY+2, tabWidth-4, tabHeight/2, 8, sdl.Color{R: 255, G: 255, B: 255, A: 30})
			renderer.SetDrawColor(255, 255, 255, 160)
			renderer.FillRect(&sdl.Rect{X: tx + 8, Y: tabY + tabHeight - 2, W: tabWidth - 16, H: 2})
		} else {
			renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 80)
			renderer.FillRect(&sdl.Rect{X: tx + 8, Y: tabY + tabHeight - 2, W: tabWidth - 16, H: 1})
		}
		if font != nil {
			lw, _, _ := font.SizeUTF8(label)
			lx := tx + (tabWidth-int32(lw))/2
			ly := tabY + (tabHeight-int32(14))/2
			tc := sdl.Color{R: 255, G: 255, B: 255, A: 255}
			if !active {
				tc = sdl.Color{R: 160, G: 170, B: 190, A: 255}
			}
			renderText(renderer, config, font, label, tc, lx, ly)
		}
		favTabRects[i] = sdl.Rect{X: tx, Y: tabY, W: tabWidth, H: tabHeight}
	}

	items := getCurrentFavorites()
	listX := element.X + 10
	listY := tabY + tabHeight + 14
	listW := getElementWidth(element, 1160)
	listH := getElementHeight(element, 500)
	listW -= 20
	if listW < 200 {
		listW = 200
	}

	drawPanel(renderer, listX, listY, listW, listH, sdl.Color{R: 16, G: 19, B: 26, A: 220}, accentColor)

	if len(items) == 0 {
		if font != nil {
			renderText(renderer, config, font, "No favorites yet. Browse and add items!", sdl.Color{R: 160, G: 170, B: 190, A: 255}, listX+20, listY+20)
		}
	} else {
		lineH := int32(40)
		maxVisible := int((listH - 20) / lineH)
		if maxVisible < 1 {
			maxVisible = 1
		}
		if favoritesFocusIndex >= len(items) {
			favoritesFocusIndex = len(items) - 1
		}
		if favoritesFocusIndex < 0 {
			favoritesFocusIndex = 0
		}
		start := 0
		if favoritesFocusIndex >= maxVisible {
			start = favoritesFocusIndex - maxVisible + 1
		}
		end := start + maxVisible
		if end > len(items) {
			end = len(items)
		}
		listFont, _ := getCachedFont(config, element.Font)
		if listFont == nil {
			listFont = font
		}
		prefixMap := map[string]string{
			"video": "\u25b6 ",
			"iptv":  "\u25b6 ",
			"file":  "\U0001f4c1 ",
		}
		colorMap := map[string]sdl.Color{
			"video": {R: 120, G: 200, B: 255, A: 255},
			"iptv":  {R: 155, G: 89, B: 182, A: 255},
			"file":  {R: 255, G: 230, B: 120, A: 255},
		}
		for i := start; i < end; i++ {
			item := items[i]
			iy := listY + 10 + int32(i-start)*lineH
			rowX := listX + 8
			rowY := iy + 2
			rowW := listW - 16
			rowH := lineH - 4
			fillRoundedRect(renderer, rowX+1, rowY+1, rowW, rowH, 8, sdl.Color{R: 0, G: 0, B: 0, A: 40})
			fillRoundedRect(renderer, rowX, rowY, rowW, rowH, 8, sdl.Color{R: 26, G: 30, B: 40, A: 255})
			if i == favoritesFocusIndex {
				fillRoundedRect(renderer, rowX, rowY, rowW, rowH, 8, sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 50})
				renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 255)
				renderer.FillRect(&sdl.Rect{X: rowX, Y: rowY, W: 4, H: rowH})
				// subtle inner highlight
				fillRoundedRect(renderer, rowX+1, rowY+1, rowW-2, rowH/2, 7, sdl.Color{R: 255, G: 255, B: 255, A: 10})
			}

			prefix := prefixMap[item.Type]
			if prefix == "" {
				prefix = "\u25b6 "
			}
			tc := colorMap[item.Type]
			if tc.R == 0 && tc.G == 0 && tc.B == 0 {
				tc = sdl.Color{R: 200, G: 210, B: 230, A: 255}
			}
			labelText := prefix + item.Label()
			if len(labelText) > 60 {
				labelText = labelText[:57] + "..."
			}
			if listFont != nil {
				renderText(renderer, config, listFont, labelText, tc, rowX+12, rowY+8)
			}
		}
	}

	backText := "Back"
	bw, bh := int32(120), int32(40)
	bx := element.X + getElementWidth(element, 1160) - bw - 20
	by := element.Y + getElementHeight(element, 500) + 26
	if by < element.Y+getElementHeight(element, 500) {
		by = element.Y + getElementHeight(element, 500) + 10
	}
	fillRoundedRect(renderer, bx+3, by+3, bw, bh, 10, sdl.Color{R: 0, G: 0, B: 0, A: 40})
	fillRoundedRect(renderer, bx, by, bw, bh, 10, sdl.Color{R: 44, G: 49, B: 62, A: 255})
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 120)
	renderer.FillRect(&sdl.Rect{X: bx + 8, Y: by + bh - 2, W: bw - 16, H: 2})
	if font != nil {
		blw, _, _ := font.SizeUTF8(backText)
		renderText(renderer, config, font, backText, sdl.Color{R: 235, G: 238, B: 245, A: 255}, bx+(bw-int32(blw))/2, by+10)
	}
	favBackRect = sdl.Rect{X: bx, Y: by, W: bw, H: bh}
}

func handleFavoritesInput(e *sdl.KeyboardEvent, config *Config) {
	items := getCurrentFavorites()
	switch e.Keysym.Sym {
	case sdl.K_LEFT:
		if favoritesCurrentTab > 0 {
			favoritesCurrentTab--
			favoritesFocusIndex = 0
		}
	case sdl.K_RIGHT:
		if favoritesCurrentTab < 3 {
			favoritesCurrentTab++
			favoritesFocusIndex = 0
		}
	case sdl.K_UP:
		if favoritesFocusIndex > 0 {
			favoritesFocusIndex--
		}
	case sdl.K_DOWN:
		if favoritesFocusIndex < len(items)-1 {
			favoritesFocusIndex++
		}
	case sdl.K_RETURN, sdl.K_SPACE:
		if favoritesFocusIndex >= 0 && favoritesFocusIndex < len(items) {
			items := getCurrentFavorites()
			if favoritesFocusIndex < len(items) {
				items[favoritesFocusIndex].Play(config)
			}
		}
	case sdl.K_BACKSPACE, sdl.K_ESCAPE:
		for _, scene := range config.Scenes {
			if scene.Name == "Main" {
				changeSceneTo(config, findSceneIndex(config, "Main"))
				break
			}
		}
	case sdl.K_d:
		if favoritesFocusIndex >= 0 && favoritesFocusIndex < len(items) {
			removeFavoriteAt(favoritesCurrentTab, favoritesFocusIndex)
			saveFavorites()
			if favoritesFocusIndex >= len(getCurrentFavorites()) && favoritesFocusIndex > 0 {
				favoritesFocusIndex--
			}
			showToast("Removed from favorites", sdl.Color{R: 230, G: 80, B: 80, A: 255})
		}
	}
}

func findSceneIndex(config *Config, name string) int {
	for i, scene := range config.Scenes {
		if scene.Name == name {
			return i
		}
	}
	return 0
}

func handleFavoritesMouseClick(mx, my int32, config *Config) {
	for i, rect := range favTabRects {
		if rect.W > 0 && mx >= rect.X && mx <= rect.X+rect.W && my >= rect.Y && my <= rect.Y+rect.H {
			favoritesCurrentTab = i
			favoritesFocusIndex = 0
			return
		}
	}
	if mx >= favBackRect.X && mx <= favBackRect.X+favBackRect.W && my >= favBackRect.Y && my <= favBackRect.Y+favBackRect.H {
		for _, scene := range config.Scenes {
			if scene.Name == "Main" {
				changeSceneTo(config, findSceneIndex(config, "Main"))
				break
			}
		}
		return
	}
	items := getCurrentFavorites()
	lineH := int32(42)
	maxVisible := 12
	if maxVisible < 1 {
		maxVisible = 1
	}
	if favoritesFocusIndex >= len(items) {
		favoritesFocusIndex = len(items) - 1
	}
	if favoritesFocusIndex < 0 {
		favoritesFocusIndex = 0
	}
	start := 0
	if favoritesFocusIndex >= maxVisible {
		start = favoritesFocusIndex - maxVisible + 1
	}
	end := start + maxVisible
	if end > len(items) {
		end = len(items)
	}
	elem := config.Scenes[currentSceneIndex].Elements[0]
	if len(config.Scenes[currentSceneIndex].Elements) > 0 {
		for _, e := range config.Scenes[currentSceneIndex].Elements {
			if e.Type == "favorites" {
				elem = e
				break
			}
		}
	}
	listY := elem.Y + 10 + 44 + 16
	for i := start; i < end; i++ {
		iy := listY + 10 + int32(i-start)*lineH
		rowX := elem.X + 18
		rowY := iy + 2
		rowW := getElementWidth(elem, 1160) - 36
		rowH := lineH - 4
		if rowW < 200 {
			rowW = 200
		}
		if mx >= rowX && mx <= rowX+rowW && my >= rowY && my <= rowY+rowH {
			favoritesFocusIndex = i
			if i < len(items) {
				items[i].Play(config)
			}
			return
		}
	}
}

func openInExplorer(path string) {
	if runtime.GOOS == "windows" {
		dir := filepath.Dir(path)
		if _, err := os.Stat(dir); err != nil {
			dir = "."
		}
		exec.Command("explorer.exe", dir).Start()
	} else {
		exec.Command("xdg-open", filepath.Dir(path)).Start()
	}
}

func sortFavoritesByTimestamp(items []FavoriteItem) []FavoriteItem {
	sorted := make([]FavoriteItem, len(items))
	copy(sorted, items)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Timestamp.After(sorted[j].Timestamp)
	})
	return sorted
}
