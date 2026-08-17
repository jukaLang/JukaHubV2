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
	HomeFocusSettings  HomeFocusID = "settings"
)

type FocusDirection uint8

const (
	FocusUp FocusDirection = iota
	FocusDown
	FocusLeft
	FocusRight
)

type homeNeighbors struct {
	Up    HomeFocusID
	Down  HomeFocusID
	Left  HomeFocusID
	Right HomeFocusID
}

var homeFocusGraph = map[HomeFocusID]homeNeighbors{
	HomeFocusContinue: {
		Up: HomeFocusContinue, Down: HomeFocusFavorites,
		Left: HomeFocusContinue, Right: HomeFocusMedia,
	},
	HomeFocusMedia: {
		Up: HomeFocusMedia, Down: HomeFocusPackages,
		Left: HomeFocusContinue, Right: HomeFocusFiles,
	},
	HomeFocusFiles: {
		Up: HomeFocusFiles, Down: HomeFocusApps,
		Left: HomeFocusMedia, Right: HomeFocusFiles,
	},
	HomeFocusPackages: {
		Up: HomeFocusMedia, Down: HomeFocusTools,
		Left: HomeFocusContinue, Right: HomeFocusApps,
	},
	HomeFocusApps: {
		Up: HomeFocusFiles, Down: HomeFocusSettings,
		Left: HomeFocusPackages, Right: HomeFocusApps,
	},
	HomeFocusFavorites: {
		Up: HomeFocusContinue, Down: HomeFocusFavorites,
		Left: HomeFocusFavorites, Right: HomeFocusChat,
	},
	HomeFocusChat: {
		Up: HomeFocusPackages, Down: HomeFocusChat,
		Left: HomeFocusFavorites, Right: HomeFocusTools,
	},
	HomeFocusTools: {
		Up: HomeFocusPackages, Down: HomeFocusTools,
		Left: HomeFocusChat, Right: HomeFocusSettings,
	},
	HomeFocusSettings: {
		Up: HomeFocusApps, Down: HomeFocusSettings,
		Left: HomeFocusTools, Right: HomeFocusSettings,
	},
}

type HomeFocusController struct {
	current  HomeFocusID
	lastHome HomeFocusID
	enabled  map[HomeFocusID]bool
}

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

func (f *HomeFocusController) Current() HomeFocusID {
	if f == nil || f.current == "" {
		return HomeFocusContinue
	}
	return f.current
}

func (f *HomeFocusController) Set(id HomeFocusID) bool {
	if f == nil || !f.IsEnabled(id) {
		return false
	}
	if _, ok := homeFocusGraph[id]; !ok {
		return false
	}
	// Set is used for programmatic placement (mouse clicks, restore). It does
	// NOT update lastHome so returning to Home restores the card the user last
	// navigated to, not whatever was focused programmatically.
	f.current = id
	return true
}

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

func (f *HomeFocusController) IsEnabled(id HomeFocusID) bool {
	if f == nil {
		return false
	}
	enabled, exists := f.enabled[id]
	return exists && enabled
}

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

func (f *HomeFocusController) Remember() {
	if f == nil || !f.IsEnabled(f.current) {
		return
	}
	f.lastHome = f.current
}

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

func (f *HomeFocusController) firstEnabled() HomeFocusID {
	order := []HomeFocusID{
		HomeFocusContinue, HomeFocusMedia, HomeFocusFiles,
		HomeFocusPackages, HomeFocusApps, HomeFocusFavorites,
		HomeFocusChat, HomeFocusTools, HomeFocusSettings,
	}
	for _, id := range order {
		if f.IsEnabled(id) {
			return id
		}
	}
	return HomeFocusContinue
}
