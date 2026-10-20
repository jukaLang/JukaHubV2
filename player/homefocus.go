package main

// HomeFocusID is a stable identifier for a selectable home-screen card.
type HomeFocusID string

const (
	HomeFocusContinue  HomeFocusID = "continue"
	HomeFocusMedia     HomeFocusID = "media"
	HomeFocusFiles     HomeFocusID = "files"
	HomeFocusPackages  HomeFocusID = "packages"
	HomeFocusApps      HomeFocusID = "apps"
	HomeFocusFavorites HomeFocusID = "favorites"
	HomeFocusChat      HomeFocusID = "chat"
	HomeFocusTools     HomeFocusID = "tools"
	HomeFocusEmulator  HomeFocusID = "emulator"
	HomeFocusSettings  HomeFocusID = "settings"
	HomeFocusWeather   HomeFocusID = "weather"
	HomeFocusClock     HomeFocusID = "clock"
	HomeFocusExit      HomeFocusID = "exit"
)

// FocusDirection indicates the direction to move focus.
type FocusDirection uint8

const (
	FocusUp FocusDirection = iota
	FocusDown
	FocusLeft
	FocusRight
)

// homeNeighbors holds the focus navigation neighbors for each home focus ID.
type homeNeighbors struct {
	Up    HomeFocusID
	Down  HomeFocusID
	Left  HomeFocusID
	Right HomeFocusID
}

// homeFocusGraph maps each HomeFocusID to its focus neighbors for D-pad/controller navigation.
var homeFocusGraph = map[HomeFocusID]homeNeighbors{
	HomeFocusContinue: {
		Up: HomeFocusContinue, Down: HomeFocusFavorites,
		Left: HomeFocusContinue, Right: HomeFocusMedia,
	},
	HomeFocusMedia: {
		Up: HomeFocusClock, Down: HomeFocusPackages,
		Left: HomeFocusContinue, Right: HomeFocusFiles,
	},
	HomeFocusFiles: {
		Up: HomeFocusClock, Down: HomeFocusApps,
		Left: HomeFocusMedia, Right: HomeFocusSettings,
	},
	HomeFocusPackages: {
		Up: HomeFocusMedia, Down: HomeFocusTools,
		Left: HomeFocusContinue, Right: HomeFocusApps,
	},
	HomeFocusApps: {
		Up: HomeFocusFiles, Down: HomeFocusEmulator,
		Left: HomeFocusPackages, Right: HomeFocusApps,
	},
	HomeFocusFavorites: {
		Up: HomeFocusContinue, Down: HomeFocusExit,
		Left: HomeFocusFavorites, Right: HomeFocusChat,
	},
	HomeFocusChat: {
		Up: HomeFocusContinue, Down: HomeFocusChat,
		Left: HomeFocusFavorites, Right: HomeFocusTools,
	},
	HomeFocusTools: {
		Up: HomeFocusPackages, Down: HomeFocusTools,
		Left: HomeFocusChat, Right: HomeFocusEmulator,
	},
	HomeFocusEmulator: {
		Up: HomeFocusApps, Down: HomeFocusEmulator,
		Left: HomeFocusTools, Right: HomeFocusEmulator,
	},
	HomeFocusSettings: {
		Up: HomeFocusClock, Down: HomeFocusApps,
		Left: HomeFocusFiles, Right: HomeFocusSettings,
	},
	// The Clock is the header time in the top-right corner: the top-right
	// cards reach it Up and it drops back Down to Settings.
	HomeFocusWeather: {
		Up: HomeFocusWeather, Down: HomeFocusSettings,
		Left: HomeFocusMedia, Right: HomeFocusClock,
	},
	HomeFocusClock: {
		Up: HomeFocusClock, Down: HomeFocusSettings,
		Left: HomeFocusWeather, Right: HomeFocusClock,
	},
	// Exit sits on the footer's left end; Favorites drops down to it.
	HomeFocusExit: {
		Up: HomeFocusFavorites, Down: HomeFocusExit,
		Left: HomeFocusExit, Right: HomeFocusExit,
	},
}

// HomeFocusController manages focus state for the home screen layout.
type HomeFocusController struct {
	current  HomeFocusID
	lastHome HomeFocusID
	enabled  map[HomeFocusID]bool
}

// NewHomeFocusController creates a new HomeFocusController with all focus IDs enabled.
func NewHomeFocusController() *HomeFocusController {
	f := &HomeFocusController{
		current:  HomeFocusContinue,
		lastHome: HomeFocusContinue,
		enabled:  make(map[HomeFocusID]bool, len(homeFocusGraph)),
	}
	for id := range homeFocusGraph {
		f.enabled[id] = true
	}
	return f
}

// Current returns the currently focused HomeFocusID.
func (f *HomeFocusController) Current() HomeFocusID {
	if f == nil || f.current == "" {
		return HomeFocusContinue
	}
	return f.current
}

// Set sets the currently focused HomeFocusID.
func (f *HomeFocusController) Set(id HomeFocusID) bool {
	if f == nil || !f.IsEnabled(id) {
		return false
	}
	if _, ok := homeFocusGraph[id]; !ok {
		return false
	}
	f.current = id
	return true
}

// SetEnabled enables or disables a focus ID.
func (f *HomeFocusController) SetEnabled(id HomeFocusID, enabled bool) {
	if f == nil {
		return
	}
	f.enabled[id] = enabled
	if !enabled && f.current == id {
		f.current = f.firstEnabled()
		f.lastHome = f.current
	}
}

// IsEnabled reports whether a focus ID is enabled.
func (f *HomeFocusController) IsEnabled(id HomeFocusID) bool {
	if f == nil {
		return false
	}
	enabled, exists := f.enabled[id]
	return exists && enabled
}

// Move moves focus in the given direction, respecting the focus graph.
func (f *HomeFocusController) Move(direction FocusDirection) HomeFocusID {
	if f == nil {
		return HomeFocusContinue
	}
	if f.current == "" || !f.IsEnabled(f.current) {
		f.current = f.firstEnabled()
	}
	candidate := f.neighbor(f.current, direction)
	visited := map[HomeFocusID]bool{f.current: true}
	for candidate != "" && !visited[candidate] {
		if f.IsEnabled(candidate) {
			f.current = candidate
			f.lastHome = candidate
			return candidate
		}
		visited[candidate] = true
		candidate = f.neighbor(candidate, direction)
	}
	return f.current
}

// Remember saves the current focus as the "last home" position.
func (f *HomeFocusController) Remember() {
	if f == nil || !f.IsEnabled(f.current) {
		return
	}
	f.lastHome = f.current
}

// Restore returns focus to the last home position.
func (f *HomeFocusController) Restore() HomeFocusID {
	if f == nil {
		return HomeFocusContinue
	}
	if f.IsEnabled(f.lastHome) {
		f.current = f.lastHome
	} else {
		f.current = f.firstEnabled()
		f.lastHome = f.current
	}
	return f.current
}

// neighbor returns the neighbor HomeFocusID in the given direction.
func (f *HomeFocusController) neighbor(id HomeFocusID, direction FocusDirection) HomeFocusID {
	n, ok := homeFocusGraph[id]
	if !ok {
		return ""
	}
	switch direction {
	case FocusUp:
		return n.Up
	case FocusDown:
		return n.Down
	case FocusLeft:
		return n.Left
	case FocusRight:
		return n.Right
	default:
		return id
	}
}

// firstEnabled returns the first enabled focus ID in the default order.
func (f *HomeFocusController) firstEnabled() HomeFocusID {
	order := []HomeFocusID{
		HomeFocusContinue, HomeFocusMedia, HomeFocusFiles,
		HomeFocusPackages, HomeFocusApps, HomeFocusFavorites,
		HomeFocusChat, HomeFocusTools, HomeFocusSettings,
		HomeFocusWeather, HomeFocusClock, HomeFocusExit,
	}
	for _, id := range order {
		if f.IsEnabled(id) {
			return id
		}
	}
	return HomeFocusContinue
}
