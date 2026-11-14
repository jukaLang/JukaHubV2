package main

// The PS5 and Switch Home rows follow the order in which their apps were last
// opened. Edit Home still owns the saved tile list; this small history only
// changes the order in which the two console themes draw that list.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const homeConsoleRecentMax = homeTilesMax + maxRecentGames
const homeConsoleAutoAppsMax = 8

type homeConsoleUse struct {
	Ref      string    `json:"ref,omitempty"`
	GamePath string    `json:"game_path,omitempty"`
	UsedAt   time.Time `json:"used_at"`
}

var homeConsoleRecents struct {
	sync.Mutex
	loaded bool
	items  []homeConsoleUse
	gen    uint64
}

var homeConsoleRecentsFile = func() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "home-console-recents.json")
}

func homeConsoleRecentLoadLocked() {
	if homeConsoleRecents.loaded {
		return
	}
	homeConsoleRecents.loaded = true
	file := homeConsoleRecentsFile()
	if file == "" {
		return
	}
	var saved []homeConsoleUse
	if _, err := readJSONWithBackup(file, &saved); err != nil {
		if !os.IsNotExist(err) {
			logWarn("home", "could not read console app history: %v", err)
		}
		return
	}
	seen := make(map[string]bool)
	apps, games := 0, 0
	for _, item := range saved {
		key := ""
		if item.GamePath != "" {
			if games == maxRecentGames {
				continue
			}
			item.Ref = ""
			key = "game:" + item.GamePath
		} else {
			if apps == homeTilesMax {
				continue
			}
			item.Ref = homeTileNormalizeRef(item.Ref)
			kind := homeTileKindOf(item.Ref)
			if kind != homeTileCard && kind != homeTileApp {
				continue
			}
			key = "app:" + strings.ToLower(item.Ref)
		}
		if item.UsedAt.IsZero() || seen[key] {
			continue
		}
		seen[key] = true
		homeConsoleRecents.items = append(homeConsoleRecents.items, item)
		if item.GamePath != "" {
			games++
		} else {
			apps++
		}
	}
}

func homeConsoleRecentGen() uint64 {
	homeConsoleRecents.Lock()
	defer homeConsoleRecents.Unlock()
	homeConsoleRecentLoadLocked()
	return homeConsoleRecents.gen
}

// homeConsoleRecordApp records a successful Home or Start-menu app opening.
// Games have their own timestamped recent list (recordGameRecent).
func homeConsoleRecordApp(ref string) {
	ref = homeTileNormalizeRef(ref)
	if kind := homeTileKindOf(ref); kind != homeTileCard && kind != homeTileApp {
		return
	}
	homeConsoleRecord(homeConsoleUse{Ref: ref})
}

// homeConsoleMenuRef gives a Start-menu opening the same tile identity as its
// Home card. Other offered entries use their own app tile identity.
func homeConsoleMenuRef(entry startMenuEntry) string {
	switch entry.name {
	case gamesLibraryEntryName:
		return string(HomeFocusEmulator)
	}
	switch entry.scene {
	case "Tube":
		return string(HomeFocusMedia)
	case "FileExplorer":
		return string(HomeFocusFiles)
	case "Packages":
		return string(HomeFocusPackages)
	case "Apps":
		return string(HomeFocusApps)
	case "Favorites":
		return string(HomeFocusFavorites)
	case browserSceneName:
		return string(HomeFocusBrowser)
	case "Chat":
		return string(HomeFocusChat)
	case toolsHubSceneName:
		return string(HomeFocusTools)
	case "Settings":
		return string(HomeFocusSettings)
	}
	if homeAppTileOffered(entry) {
		return homeAppRefFor(entry)
	}
	return ""
}

func homeConsoleRecordGame(path string) {
	if strings.TrimSpace(path) != "" {
		homeConsoleRecord(homeConsoleUse{GamePath: path})
	}
}

func homeConsoleRecord(item homeConsoleUse) {
	homeConsoleRecents.Lock()
	defer homeConsoleRecents.Unlock()
	homeConsoleRecentLoadLocked()
	next := make([]homeConsoleUse, 0, homeConsoleRecentMax)
	item.UsedAt = time.Now().UTC()
	next = append(next, item)
	apps, games := 0, 0
	if item.GamePath != "" {
		games++
	} else {
		apps++
	}
	for _, older := range homeConsoleRecents.items {
		if (item.GamePath != "" && older.GamePath == item.GamePath) ||
			(item.Ref != "" && strings.EqualFold(older.Ref, item.Ref)) {
			continue
		}
		if older.GamePath != "" {
			if games == maxRecentGames {
				continue
			}
			games++
		} else {
			if apps == homeTilesMax {
				continue
			}
			apps++
		}
		next = append(next, older)
		if len(next) == homeConsoleRecentMax {
			break
		}
	}
	homeConsoleRecents.items = next
	homeConsoleRecents.gen++
	if file := homeConsoleRecentsFile(); file != "" {
		if data, err := json.MarshalIndent(next, "", "  "); err == nil {
			if err := AtomicWrite(file, append(data, '\n'), 0o644); err != nil {
				logWarn("home", "could not save console app history: %v", err)
			}
		}
	}
}

// homeConsoleVisibleTiles shows a few recently opened apps even when they
// were launched from the Start menu instead of being pinned in Edit Home.
// The extra tiles are only in the console rows; Edit Home keeps its saved list.
func homeConsoleVisibleTiles(config *Config, saved []homeTile) []homeTile {
	if homeEdit.open || (homeConsoleLook.carousel != homeCarouselHub && !homeConsoleLook.switchHome) {
		return saved
	}
	homeConsoleRecents.Lock()
	homeConsoleRecentLoadLocked()
	recent := append([]homeConsoleUse(nil), homeConsoleRecents.items...)
	homeConsoleRecents.Unlock()
	seen := make(map[string]bool, len(saved)+homeConsoleAutoAppsMax)
	for _, tile := range saved {
		seen[strings.ToLower(tile.Ref)] = true
	}
	var available map[string]bool
	if config != nil {
		available = make(map[string]bool)
		entries, _ := homeAppEntries(config)
		for _, entry := range entries {
			available[strings.ToLower(homeAppRefFor(entry))] = true
		}
	}
	visible := append([]homeTile(nil), saved...)
	added := 0
	for _, use := range recent {
		ref := use.Ref
		if ref == "" || ref == string(HomeFocusContinue) || seen[strings.ToLower(ref)] {
			continue
		}
		kind := homeTileKindOf(ref)
		if kind != homeTileCard && kind != homeTileApp {
			continue
		}
		if kind == homeTileApp && !available[strings.ToLower(ref)] {
			continue
		}
		visible = append(visible, homeTile{Ref: ref, Size: homeTileSizeOne})
		seen[strings.ToLower(ref)] = true
		added++
		if added == homeConsoleAutoAppsMax {
			break
		}
	}
	return visible
}

// homeConsoleOrderTiles puts recently used apps and games first. The saved
// event order works even on a TrimUI whose clock has reset. Older games from
// before this history existed follow, then untouched tiles in Edit Home order.
func homeConsoleOrderTiles(out []homeDynTile) []homeDynTile {
	if homeEdit.open || (homeConsoleLook.carousel != homeCarouselHub && !homeConsoleLook.switchHome) {
		return out
	}
	homeConsoleRecents.Lock()
	homeConsoleRecentLoadLocked()
	used := make(map[string]int, len(homeConsoleRecents.items))
	for i, item := range homeConsoleRecents.items {
		if item.GamePath != "" {
			used["game:"+item.GamePath] = i
		} else {
			used["app:"+strings.ToLower(item.Ref)] = i
		}
	}
	olderGame, untouched := len(homeConsoleRecents.items), len(homeConsoleRecents.items)+1
	homeConsoleRecents.Unlock()
	rank := func(t homeDynTile) int {
		if t.Kind == homeTileRecent {
			if i := homeSwRecentIndex(t.ID); i >= 0 {
				if g, ok := homeSwitch.recents[i].item.Data.(recentGame); ok {
					if rank, found := used["game:"+g.Path]; found {
						return rank
					}
				}
			}
			return olderGame
		}
		if t.Kind == homeTileCard || t.Kind == homeTileApp {
			if rank, found := used["app:"+strings.ToLower(t.Ref)]; found {
				return rank
			}
		}
		return untouched
	}
	ordered := append([]homeDynTile(nil), out...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Kind == homeTileAdd {
			return false
		}
		if ordered[j].Kind == homeTileAdd {
			return true
		}
		return rank(ordered[i]) < rank(ordered[j])
	})
	return ordered
}
