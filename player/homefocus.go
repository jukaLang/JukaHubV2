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
	HomeFocusBrowser   HomeFocusID = "browser"
	HomeFocusChat      HomeFocusID = "chat"
	HomeFocusTools     HomeFocusID = "tools"
	HomeFocusEmulator  HomeFocusID = "emulator"
	HomeFocusSettings  HomeFocusID = "settings"
	HomeFocusWeather   HomeFocusID = "weather"
	HomeFocusClock     HomeFocusID = "clock"
	HomeFocusExit      HomeFocusID = "exit"
	// HomeFocusBell is the notification bell in the far-right corner of the
	// header. It is a chrome control on every other scene, but Home drives its
	// own focus graph, so without an entry here the D-pad could never reach it.
	// It is enabled only while the bell is actually drawn.
	HomeFocusBell HomeFocusID = "bell"
	// HomeFocusPS5 is the PS5 Remote Play tile in the header, left of the phone
	// pairing QR. It is disabled whenever that tile is not drawn, so the D-pad
	// can never land on something invisible.
	HomeFocusPS5 HomeFocusID = "ps5"
	// HomeFocusPS4 is the stable PS4 Remote Play tile, one slot left of the PS5
	// tile. Disabled (and skipped by the D-pad) while it is not drawn.
	HomeFocusPS4 HomeFocusID = "ps4"
	// HomeFocusUpdate is the "new release available" download chip in the status
	// cluster, left of the weather text. It is disabled unless a release is
	// actually offered and drawn.
	HomeFocusUpdate HomeFocusID = "update"
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
		// Up lands on the PS5 launcher, which sits above the top-right cards in
		// the header band. The layout re-points this edge at whichever card the
		// tile actually sits over (see SetTileLink), so the D-pad and the tile
		// can never disagree. When the tile is hidden the PS5 entry is disabled
		// and Move falls through to the PS5 entry's own Up neighbour, the clock,
		// which is what Files did before the launcher existed.
		Up: HomeFocusPS5, Down: HomeFocusApps,
		Left: HomeFocusMedia, Right: HomeFocusSettings,
	},
	// The Remote Play launchers are header tiles: Up reaches the header clock,
	// Down returns to the card below them, and Left/Right step between the two
	// tiles instead of wandering into a card that is not beside them.
	HomeFocusPS4: {
		Up: HomeFocusClock, Down: HomeFocusFiles,
		Left: HomeFocusPS4, Right: HomeFocusPS5,
	},
	HomeFocusPS5: {
		Up: HomeFocusClock, Down: HomeFocusFiles,
		Left: HomeFocusPS4, Right: HomeFocusClock,
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
		Left: HomeFocusFavorites, Right: HomeFocusBrowser,
	},
	// The browser card sits between Favorites and Chat in the bottom row, so
	// Right walks Favorites -> Browser -> Chat -> Tools -> Emulator.
	HomeFocusBrowser: {
		Up: HomeFocusContinue, Down: HomeFocusExit,
		Left: HomeFocusFavorites, Right: HomeFocusChat,
	},
	HomeFocusChat: {
		Up: HomeFocusContinue, Down: HomeFocusChat,
		Left: HomeFocusBrowser, Right: HomeFocusTools,
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
	// The weather chip sits directly right of the update download chip, so Left
	// steps onto the offer. With no update the chip is disabled and Move falls
	// through to the update entry's own Left neighbour, Media, which is what
	// the weather chip did before the offer existed.
	HomeFocusWeather: {
		Up: HomeFocusWeather, Down: HomeFocusSettings,
		Left: HomeFocusUpdate, Right: HomeFocusClock,
	},
	// The update download chip is a header button: Right returns to the weather
	// text, Down drops to the first card in the settings column.
	HomeFocusUpdate: {
		Up: HomeFocusUpdate, Down: HomeFocusSettings,
		Left: HomeFocusMedia, Right: HomeFocusWeather,
	},
	HomeFocusClock: {
		Up: HomeFocusClock, Down: HomeFocusSettings,
		Left: HomeFocusWeather, Right: HomeFocusBell,
	},
	// The bell is the last control in the header, right of the clock. Right
	// stays put at the screen edge and Down drops into the Settings column
	// under it.
	HomeFocusBell: {
		Up: HomeFocusBell, Down: HomeFocusSettings,
		Left: HomeFocusClock, Right: HomeFocusBell,
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
	// tileUnder is the card the header launcher sits over. The home layout sets
	// it from the measured geometry, so Up from that card reaches the launcher
	// and Down from the launcher returns there. The static graph cannot know
	// this: the header cluster's width moves the tile.
	tileUnder HomeFocusID
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

// SetTileLink records which card the header launcher sits over, or clears the
// link when the tile is not drawn.
func (f *HomeFocusController) SetTileLink(under HomeFocusID) {
	if f == nil {
		return
	}
	f.tileUnder = under
}

// TileLink returns the card the header launcher currently sits over.
func (f *HomeFocusController) TileLink() HomeFocusID {
	if f == nil {
		return ""
	}
	return f.tileUnder
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
	if f.tileUnder != "" {
		// The header launcher trades Up/Down with the card under it, so the
		// D-pad never has to guess which column the tile ended up in.
		if direction == FocusUp && id == f.tileUnder {
			return HomeFocusPS5
		}
		if direction == FocusDown && (id == HomeFocusPS5 || id == HomeFocusPS4) {
			return f.tileUnder
		}
	}
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
		HomeFocusWeather, HomeFocusClock, HomeFocusBell, HomeFocusUpdate, HomeFocusPS4, HomeFocusPS5, HomeFocusExit,
	}
	for _, id := range order {
		if f.IsEnabled(id) {
			return id
		}
	}
	return HomeFocusContinue
}
