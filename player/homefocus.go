package main

// HomeFocusID is a stable identifier for a selectable home-screen card. IDs
// outlive the cards' names: HomeFocusEmulator is the Games card (it opens the
// game library), and keeps its old ID so config mappings stay valid.
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
	// HomeFocusBug is the report shortcut between the bell and trophy cup.
	HomeFocusBug HomeFocusID = "bug_report"
	// HomeFocusPS5 is the PS5 Remote Play tile in the header, left of the phone
	// pairing QR. It is disabled whenever that tile is not drawn, so the D-pad
	// can never land on something invisible.
	HomeFocusPS5 HomeFocusID = "ps5"
	// HomeFocusPS4 is the stable PS4 Remote Play tile, one slot left of the PS5
	// tile. Disabled (and skipped by the D-pad) while it is not drawn.
	HomeFocusPS4 HomeFocusID = "ps4"
	// HomeFocusUpdate is the "new release available" download chip in the header's
	// system corner, immediately left of the trophy room's cup beside the
	// notification bell — the same place every scene header draws it. It is
	// disabled unless a release is actually offered and drawn.
	HomeFocusUpdate HomeFocusID = "update"
	// HomeFocusNowPlaying is the now-playing chip at the right end of the status
	// cluster, between the clock and the system corner (the release offer, the
	// trophy room's cup and the bell). It is a chrome control on other
	// scenes, so Home needs its own entry to reach it; it is enabled only while
	// the chip is drawn.
	HomeFocusNowPlaying HomeFocusID = "nowplaying"
	// HomeFocusWiFi is the signal meter, left of the weather text. It opens the
	// network picker, and it is a chrome control on every other scene, so Home
	// needs its own entry for the D-pad; it is enabled only while the meter is
	// drawn (the cluster drops the network slot first when space is short).
	HomeFocusWiFi HomeFocusID = "wifi"
	// HomeFocusSearch is the search pill under the greeting, above the Continue
	// card. A opens the search sheet (globalsearch.go).
	HomeFocusSearch HomeFocusID = "search"
	// HomeFocusVolume is the speaker in the status cluster, immediately left
	// of the Bluetooth button. A opens the volume popover (volumepopover.go).
	// It is a chrome control on every other scene; enabled only while it is
	// drawn (no mixer, no button).
	HomeFocusVolume HomeFocusID = "volume"
	// HomeFocusBluetooth is the Bluetooth rune between the speaker and the
	// signal meter (statusbar_bluetooth.go). A opens the Bluetooth page. It is
	// a chrome control on every other scene; enabled only while it is drawn,
	// which on Home is wherever the Bluetooth page can do something (Home
	// never trims its cluster; a crowded scene bar does drop the rune).
	HomeFocusBluetooth HomeFocusID = "bluetooth"
	// HomeFocusBrightness is the sun at the left end of the status cluster,
	// immediately left of the speaker. A opens the brightness card
	// (brightnesspopover.go). Enabled only while it is drawn: it appears once
	// the brightness controller has found what it drives.
	HomeFocusBrightness HomeFocusID = "brightness"
	// HomeFocusQR is the phone-pairing code in the header band, right of the
	// Remote Play launchers. A shows it full screen (qrzoom.go), the size a
	// phone camera reads from arm's length. Enabled only while it is drawn.
	HomeFocusQR HomeFocusID = "qr"
	// HomeFocusEditPill is the Edit pill right of the power button on a
	// Carousel or Widgets Home (home_edit.go): A opens Edit Home. Enabled only
	// while it is drawn.
	HomeFocusEditPill HomeFocusID = "editpill"
	// HomeFocusTrophy is the trophy room's cup, left of Report a bug
	// (header_trophy.go): A opens Trophies. It is a chrome control on every
	// other scene; enabled only while it is drawn.
	HomeFocusTrophy HomeFocusID = "trophies"
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
	// The search pill sits under the greeting, above the Continue card: Down
	// drops into Continue, and Right crosses the header band to the Remote Play
	// launchers (and on to the clock when they are hidden).
	HomeFocusSearch: {
		Up: HomeFocusSearch, Down: HomeFocusContinue,
		Left: HomeFocusSearch, Right: HomeFocusPS4,
	},
	HomeFocusContinue: {
		Up: HomeFocusSearch, Down: HomeFocusFavorites,
		Left: HomeFocusContinue, Right: HomeFocusMedia,
	},
	HomeFocusMedia: {
		// Media spans the whole right column, so Down belongs to the first tile
		// under it: Games. Up is the PS5 launcher drawn over it in the header
		// band; the layout re-points that edge at whichever card the tile really
		// sits over (SetTileLink), and with the launcher hidden Move falls
		// through to the clock.
		Up: HomeFocusPS5, Down: HomeFocusEmulator,
		Left: HomeFocusContinue, Right: HomeFocusMedia,
	},
	HomeFocusEmulator: {
		// Games (the HomeFocusEmulator card) is the first tile of the row under
		// Media, beside the Continue hero: Up is Media, Left the hero, and Down
		// the Tools tile its right half sits over in the bottom row.
		Up: HomeFocusMedia, Down: HomeFocusTools,
		Left: HomeFocusContinue, Right: HomeFocusPackages,
	},
	// The Remote Play launchers are header tiles: Up reaches the header clock,
	// Down returns to the card below them (Media, or whichever card the layout
	// links), Left steps toward the search pill on the same band and Right
	// toward the clock.
	HomeFocusPS4: {
		Up: HomeFocusClock, Down: HomeFocusMedia,
		Left: HomeFocusSearch, Right: HomeFocusPS5,
	},
	HomeFocusPS5: {
		Up: HomeFocusClock, Down: HomeFocusMedia,
		Left: HomeFocusPS4, Right: HomeFocusClock,
	},
	HomeFocusPackages: {
		// Left is the Games card beside it, never a jump over it to the hero.
		// Up is the Media card drawn over the whole row; Down is Files, the
		// bottom-row tile under its centre.
		Up: HomeFocusMedia, Down: HomeFocusFiles,
		Left: HomeFocusEmulator, Right: HomeFocusApps,
	},
	HomeFocusApps: {
		// Apps sits under the right end of the Media card and above the
		// Settings column; it used to point Up at Files and Down at Emulator,
		// neither of which is beside it.
		Up: HomeFocusMedia, Down: HomeFocusSettings,
		Left: HomeFocusPackages, Right: HomeFocusApps,
	},
	// The bottom row's left end drops to the power button under it, at the
	// left end of the footer (home_power.go), and Up from the button comes
	// back here; the rest of the row has nothing below it, so Down stays put.
	HomeFocusFavorites: {
		Up: HomeFocusContinue, Down: HomeFocusExit,
		Left: HomeFocusFavorites, Right: HomeFocusBrowser,
	},
	// The browser card sits between Favorites and Chat in the bottom row, so
	// Right walks Favorites -> Browser -> Chat -> Tools -> Files -> Settings.
	HomeFocusBrowser: {
		Up: HomeFocusContinue, Down: HomeFocusBrowser,
		Left: HomeFocusFavorites, Right: HomeFocusChat,
	},
	HomeFocusChat: {
		// Chat's centre sits under the Games card (the Continue hero ends left
		// of it), so Up is Games, the card drawn above most of the tile.
		Up: HomeFocusEmulator, Down: HomeFocusChat,
		Left: HomeFocusBrowser, Right: HomeFocusTools,
	},
	HomeFocusTools: {
		Up: HomeFocusEmulator, Down: HomeFocusTools,
		Left: HomeFocusChat, Right: HomeFocusFiles,
	},
	HomeFocusFiles: {
		// Files is the fifth tile of the bottom row, under Packages.
		Up: HomeFocusPackages, Down: HomeFocusFiles,
		Left: HomeFocusTools, Right: HomeFocusSettings,
	},
	// Settings is the last tile of the bottom row, under Apps. Its edges still
	// pointed at the top-right corner it was drawn in before the six-tile row
	// (Up the clock, Down Apps, Left Files), so the row's right end could not
	// be walked to and Up from Settings jumped over Apps.
	HomeFocusSettings: {
		Up: HomeFocusApps, Down: HomeFocusSettings,
		Left: HomeFocusFiles, Right: HomeFocusSettings,
	},
	// The header chips sit in the status row above the Remote Play launchers,
	// so Down from any of them lands on the launcher under that end of the bar
	// (and falls through to the Media card when the launchers are hidden).
	// The weather chip sits between the signal meter and the clock.
	HomeFocusWeather: {
		Up: HomeFocusWeather, Down: HomeFocusPS4,
		Left: HomeFocusWiFi, Right: HomeFocusClock,
	},
	// The signal meter is the Wi-Fi button: Left is the Bluetooth button
	// beside it (and, when that is not drawn, the next control toward the left
	// edge: the speaker, the sun, then the search pill), and Right reads the
	// weather text. The
	// release offer is drawn at the other end of the bar, in the system corner
	// beside the trophy room's cup and the bell, so the meter does not point at
	// it: the pad used to jump from here to a pill on the far right.
	HomeFocusWiFi: {
		Up: HomeFocusWiFi, Down: HomeFocusPS4,
		Left: HomeFocusBluetooth, Right: HomeFocusWeather,
	},
	// The Bluetooth rune sits between the speaker and the signal meter: Left
	// is the speaker, Right the meter, and Down drops to the launcher under
	// that end of the bar like theirs do.
	HomeFocusBluetooth: {
		Up: HomeFocusBluetooth, Down: HomeFocusPS4,
		Left: HomeFocusVolume, Right: HomeFocusWiFi,
	},
	// The speaker sits between the sun and the Bluetooth rune: Left is the sun
	// (and, when it is not drawn, the search pill beyond it), Right is the
	// rune (or, without one, the meter), and Down drops to the launcher under
	// that end of the bar like the meter's does.
	HomeFocusVolume: {
		Up: HomeFocusVolume, Down: HomeFocusPS4,
		Left: HomeFocusBrightness, Right: HomeFocusBluetooth,
	},
	// The sun leads the status cluster: Left leaves the bar for the search
	// pill, Right is the speaker, Down the launcher under that end of the bar.
	HomeFocusBrightness: {
		Up: HomeFocusBrightness, Down: HomeFocusPS4,
		Left: HomeFocusSearch, Right: HomeFocusVolume,
	},
	// The pairing code is the header band's right end, under the bell: Up is
	// the bell, Left the PS5 launcher beside it, Down the card under it (the
	// layout links whichever card that is, see SetQRLink). Right is the screen
	// edge. The edges *into* it are made in neighbor(), so that a code that is
	// not drawn (no Wi-Fi, no companion server) leaves the old paths exactly as
	// they were: PS5 → clock, bell → PS5.
	HomeFocusQR: {
		Up: HomeFocusBell, Down: HomeFocusMedia,
		Left: HomeFocusPS5, Right: HomeFocusQR,
	},
	// The update download chip is a header button drawn immediately left of the
	// trophy room's cup, right of the now-playing chip. Its neighbours are the
	// controls actually beside it on screen: before this, the graph put it left
	// of the Wi-Fi meter, so pressing Left/Right next to the drawn pill skipped
	// it and the offer could only be reached by walking to the meter and
	// pressing Left — which moved focus rightward, away from the pill.
	//
	// Its neighbours may be disabled (nothing playing, no cup drawn) and so may
	// the chip itself (no release offered); Move keeps walking their own
	// neighbour chains, so the corner still has a complete focus route when
	// the update offer is hidden.
	HomeFocusUpdate: {
		Up: HomeFocusUpdate, Down: HomeFocusPS5,
		Left: HomeFocusNowPlaying, Right: HomeFocusTrophy,
	},
	// The trophy room's cup sits between the release offer and Report a bug:
	// Left crosses the offer when it is drawn and Right reaches the bug form.
	// Down drops to the pairing code under that end of the bar
	// (neighbor) or the launcher, as the bell's does.
	HomeFocusTrophy: {
		Up: HomeFocusTrophy, Down: HomeFocusPS5,
		Left: HomeFocusUpdate, Right: HomeFocusBug,
	},
	HomeFocusBug: {
		Up: HomeFocusBug, Down: HomeFocusPS5,
		Left: HomeFocusTrophy, Right: HomeFocusBell,
	},
	// The now-playing chip sits right of the clock and names the running
	// player. Disabled while nothing plays, so Right from the clock falls
	// through to the release offer (or the trophy room's cup when none is
	// offered).
	HomeFocusNowPlaying: {
		Up: HomeFocusNowPlaying, Down: HomeFocusPS5,
		Left: HomeFocusClock, Right: HomeFocusUpdate,
	},
	HomeFocusClock: {
		Up: HomeFocusClock, Down: HomeFocusPS5,
		Left: HomeFocusWeather, Right: HomeFocusNowPlaying,
	},
	// The bell is the last control in the header, right of Report a bug.
	// Left is the bug form (and, when it is not drawn, the cup or
	// whatever is left of it); Right stays put at the screen edge and Down
	// drops to the launcher under the right end of the bar, or the Media card
	// when it is hidden.
	HomeFocusBell: {
		Up: HomeFocusBell, Down: HomeFocusPS5,
		Left: HomeFocusBug, Right: HomeFocusBell,
	},
	// The power button sits at the footer's left end, under Favorites; it is
	// enabled only while it is drawn (HomeLayout.Render). Right is the Edit
	// pill beside it, which only the Carousel and Widgets layouts draw; while
	// it is disabled Right stays on the power button.
	HomeFocusExit: {
		Up: HomeFocusFavorites, Down: HomeFocusExit,
		Left: HomeFocusExit, Right: HomeFocusEditPill,
	},
	// The Edit pill; the dynamic layouts take Up to the tile drawn above it
	// (home_dynbody.go), so the graph's Up only matters to the dashboard,
	// which never draws it.
	HomeFocusEditPill: {
		Up: HomeFocusBrowser, Down: HomeFocusEditPill,
		Left: HomeFocusExit, Right: HomeFocusEditPill,
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
	// qrUnder is the card under the header pairing code, set the same way, so
	// Down from the code lands on the card actually drawn below it.
	qrUnder HomeFocusID
	// dynamic holds the focus IDs of the Carousel and Widgets tiles that are
	// not in the graph (widgets, apps, the add tile), and bodyOrder every
	// tile in order: the first enabled one is where a lost focus goes
	// (home_dynbody.go). Both are empty on the standard layouts.
	dynamic   map[HomeFocusID]bool
	bodyOrder []HomeFocusID
}

// NewHomeFocusController creates a new HomeFocusController with all focus IDs
// enabled, except the Edit pill, which the layout enables while it draws it.
func NewHomeFocusController() *HomeFocusController {
	f := &HomeFocusController{
		current:  HomeFocusContinue,
		lastHome: HomeFocusContinue,
		enabled:  make(map[HomeFocusID]bool, len(homeFocusGraph)),
	}
	for id := range homeFocusGraph {
		f.enabled[id] = true
	}
	f.enabled[HomeFocusEditPill] = false
	return f
}

// SetDynamic replaces the tiles a dynamic layout adds: ids are the ones the
// graph does not know (they are enabled here; the previous set is dropped),
// order is every tile in order. nil, nil goes back to the graph alone.
func (f *HomeFocusController) SetDynamic(ids, order []HomeFocusID) {
	if f == nil {
		return
	}
	next := make(map[HomeFocusID]bool, len(ids))
	for _, id := range ids {
		next[id] = true
	}
	for id := range f.dynamic {
		if !next[id] {
			delete(f.enabled, id)
		}
	}
	f.dynamic = next
	for id := range next {
		f.enabled[id] = true
	}
	f.bodyOrder = append(f.bodyOrder[:0], order...)
	if !f.IsEnabled(f.current) {
		f.current = f.firstEnabled()
	}
	if !f.IsEnabled(f.lastHome) {
		f.lastHome = f.current
	}
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

// SetQRLink records the card under the header pairing code, or clears the
// link when the code is not drawn.
func (f *HomeFocusController) SetQRLink(under HomeFocusID) {
	if f == nil {
		return
	}
	f.qrUnder = under
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
	if _, ok := homeFocusGraph[id]; !ok && !f.dynamic[id] {
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
	// The pairing code is entered from the PS5 launcher on its left (Right)
	// and from the bell above it (Down), but only while it is drawn: a hidden
	// code is not a stop on the way, so those presses keep the targets they
	// had before the code was a control (the clock, the PS5 launcher).
	if f.IsEnabled(HomeFocusQR) {
		if direction == FocusRight && id == HomeFocusPS5 {
			return HomeFocusQR
		}
		if direction == FocusDown && (id == HomeFocusBell || id == HomeFocusBug || id == HomeFocusTrophy) {
			return HomeFocusQR
		}
	}
	if direction == FocusDown && id == HomeFocusQR && f.qrUnder != "" {
		return f.qrUnder
	}
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

// firstEnabled returns the first enabled focus ID in the default order: a
// dynamic layout's first tile, then the dashboard's order.
func (f *HomeFocusController) firstEnabled() HomeFocusID {
	for _, id := range f.bodyOrder {
		if f.IsEnabled(id) {
			return id
		}
	}
	order := []HomeFocusID{
		HomeFocusContinue, HomeFocusMedia, HomeFocusEmulator,
		HomeFocusPackages, HomeFocusApps, HomeFocusFavorites, HomeFocusBrowser,
		HomeFocusChat, HomeFocusTools, HomeFocusFiles, HomeFocusSettings,
		HomeFocusSearch, HomeFocusWeather, HomeFocusClock, HomeFocusNowPlaying,
		HomeFocusUpdate, HomeFocusTrophy, HomeFocusBug, HomeFocusBell, HomeFocusPS4, HomeFocusPS5, HomeFocusExit,
		HomeFocusBrightness, HomeFocusVolume, HomeFocusBluetooth, HomeFocusWiFi, HomeFocusQR,
	}
	for _, id := range order {
		if f.IsEnabled(id) {
			return id
		}
	}
	return HomeFocusContinue
}
