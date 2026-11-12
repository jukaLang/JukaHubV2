package main

// header_arrange.go — the user's order for the right of the top bar.
//
// The right of every top bar holds twelve controls: the brightness, volume
// and Bluetooth buttons, the Wi-Fi meter, the forecast, the clock, the
// battery, the now-playing chip, the release offer, the trophy room's cup,
// Report a bug and the notification bell. Edit Home's "Top bar" row
// (home_edit_header.go) lets the user put them in any order and hide the
// ones they do not want; the choice is the Custom "headerLayout" text, a
// comma-separated list of slot ids left to right, a leading "-" marking a
// hidden one:
//
//	brightness,volume,-bluetooth,wifi,weather,clock,battery,media,update,trophies,bug,notifications
//
// Missing or blank is the shipped order (the list above with nothing hidden).
// Unknown ids are skipped and a slot the text does not name goes back after
// the slot that comes before it by default, so a control added by a later
// build turns up where it ships. The bell cannot be hidden: it is the one way
// to the notification centre from every screen.
//
// Both headers lay the right of the bar out with headerPlan: the status parts
// headerStatusParts made (with the hidden ones already left out) and the
// corner's buttons, in the user's order, right to left from the margin. The
// part-to-part gap stays the cluster's own and every other gap is the
// corner's, so the shipped order draws exactly as before.

import (
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// headerSlot is one control of the right of the top bar.
type headerSlot int

// The slots, in the shipped order. The first eight are status parts
// (headerStatusParts); from hsUpdate on they are the corner's buttons.
const (
	hsBrightness headerSlot = iota
	hsVolume
	hsBluetooth
	hsWiFi
	hsWeather
	hsClock
	hsBattery
	hsMedia
	hsUpdate
	hsTrophy
	hsBug
	hsBell
	hsCount
)

// headerArrangeKey is the Custom key the arrangement is stored under.
const headerArrangeKey = "headerLayout"

// headerSlotInfo is each slot's stored id, its name in Edit Home and its glyph.
var headerSlotInfo = [hsCount]struct{ id, label, icon string }{
	hsBrightness: {"brightness", "Brightness", "brightness"},
	hsVolume:     {"volume", "Volume", "volume"},
	hsBluetooth:  {"bluetooth", "Bluetooth", "bluetooth"},
	hsWiFi:       {"wifi", "Wi-Fi", "wifi"},
	hsWeather:    {"weather", "Weather", "weather"},
	hsClock:      {"clock", "Clock", "clock"},
	hsBattery:    {"battery", "Battery", "battery"},
	hsMedia:      {"media", "Now playing", "music"},
	hsUpdate:     {"update", "Update offer", "download"},
	hsTrophy:     {"trophies", "Trophies", "trophy"},
	hsBug:        {"bug", "Report a bug", "bug"},
	hsBell:       {"notifications", "Notifications", "bell"},
}

// headerArrangement is the order (left to right) and which slots are hidden.
type headerArrangement struct {
	order  [hsCount]headerSlot
	hidden [hsCount]bool // by slot
}

// headerArrangeDefault is the shipped order with nothing hidden.
func headerArrangeDefault() headerArrangement {
	var a headerArrangement
	for i := range a.order {
		a.order[i] = headerSlot(i)
	}
	return a
}

// headerSlotIsCorner reports whether the slot is one of the corner's buttons
// rather than a status part.
func headerSlotIsCorner(s headerSlot) bool { return s >= hsUpdate && s < hsCount }

// headerSlotHideable reports whether the slot may be hidden: all but the bell.
func headerSlotHideable(s headerSlot) bool { return s != hsBell }

// headerSlotByID finds a slot by its stored id (any case).
func headerSlotByID(id string) (headerSlot, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for s := headerSlot(0); s < hsCount; s++ {
		if headerSlotInfo[s].id == id {
			return s, true
		}
	}
	return 0, false
}

// headerOrderIndex is where slot s stands in a, or -1.
func headerOrderIndex(a *headerArrangement, s headerSlot) int {
	for i, o := range a.order {
		if o == s {
			return i
		}
	}
	return -1
}

// parseHeaderArrangement reads the stored text (see the file comment).
func parseHeaderArrangement(raw string) headerArrangement {
	var a headerArrangement
	var seen [hsCount]bool
	n := 0
	for _, f := range strings.Split(raw, ",") {
		f = strings.TrimSpace(f)
		hide := strings.HasPrefix(f, "-")
		s, ok := headerSlotByID(strings.TrimPrefix(f, "-"))
		if !ok || seen[s] {
			continue
		}
		seen[s] = true
		a.order[n] = s
		n++
		a.hidden[s] = hide && headerSlotHideable(s)
	}
	// A slot the text does not name goes back after the slot that precedes
	// it in the shipped order (or first, when none of those is placed yet).
	for s := headerSlot(0); s < hsCount; s++ {
		if seen[s] {
			continue
		}
		pos := 0
		for p := s - 1; p >= 0; p-- {
			if seen[p] {
				for i := 0; i < n; i++ {
					if a.order[i] == p {
						pos = i + 1
						break
					}
				}
				break
			}
		}
		copy(a.order[pos+1:n+1], a.order[pos:n])
		a.order[pos] = s
		n++
		seen[s] = true
	}
	return a
}

// formatHeaderArrangement is the stored text for a.
func formatHeaderArrangement(a headerArrangement) string {
	var b strings.Builder
	for i, s := range a.order {
		if i > 0 {
			b.WriteByte(',')
		}
		if a.hidden[s] {
			b.WriteByte('-')
		}
		b.WriteString(headerSlotInfo[s].id)
	}
	return b.String()
}

// headerArrangeCache keeps the parsed arrangement for the stored text, so the
// two headers do not parse it every frame.
var headerArrangeCache struct {
	valid bool
	raw   string
	have  bool
	a     headerArrangement
}

// headerArrangementRaw reads the stored text without allocating for a string.
func headerArrangementRaw(config *Config) (string, bool) {
	if config == nil || config.Variables.Custom == nil {
		return "", false
	}
	v, ok := config.Variables.Custom[headerArrangeKey]
	if !ok || v == nil {
		return "", false
	}
	if s, isStr := v.(string); isStr {
		return s, true
	}
	return customString(config.Variables.Custom, headerArrangeKey)
}

// headerArrangementCurrent is the arrangement in force: while Edit Home's Top
// bar row is open, the one being edited (home_edit_header.go), so every
// change shows in the bar at once.
func headerArrangementCurrent(config *Config) headerArrangement {
	if homeEditBarActive() {
		return homeEditBar.work
	}
	raw, have := headerArrangementRaw(config)
	c := &headerArrangeCache
	if c.valid && c.raw == raw && c.have == have {
		return c.a
	}
	if have && strings.TrimSpace(raw) != "" {
		c.a = parseHeaderArrangement(raw)
	} else {
		c.a = headerArrangeDefault()
	}
	c.raw, c.have, c.valid = raw, have, true
	return c.a
}

// headerArrangementIsDefault reports whether a is the shipped arrangement.
func headerArrangementIsDefault(a headerArrangement) bool {
	return a == headerArrangeDefault()
}

// headerArrangementSet stores a through the shared override-and-save path
// every Settings row uses. The shipped arrangement is stored as "", so a
// later build's new control still lands where it ships.
func headerArrangementSet(config *Config, a headerArrangement) {
	raw := formatHeaderArrangement(a)
	if headerArrangementIsDefault(a) {
		raw = ""
	}
	c := &headerArrangeCache
	c.a, c.raw, c.have, c.valid = a, raw, true, true
	if config != nil {
		setStoreString(config, headerArrangeKey, raw)
	}
}

// headerSlotHidden reports whether the user hid slot s.
func headerSlotHidden(config *Config, s headerSlot) bool {
	if s < 0 || s >= hsCount {
		return false
	}
	a := headerArrangementCurrent(config)
	return a.hidden[s]
}

// headerSlotOfPart is the slot a status part belongs to.
func headerSlotOfPart(part string) headerSlot {
	switch {
	case isBrightnessStatusPart(part):
		return hsBrightness
	case isVolumeStatusPart(part):
		return hsVolume
	case isBluetoothStatusPart(part):
		return hsBluetooth
	case isWiFiStatusPart(part):
		return hsWiFi
	case isWeatherStatusPart(part):
		return hsWeather
	case isBatteryStatusPart(part):
		return hsBattery
	case isChipStatusPart(part):
		return hsMedia
	}
	return hsClock
}

// headerPartsVisible drops the parts of hidden slots, in place.
func headerPartsVisible(config *Config, parts []string) []string {
	a := headerArrangementCurrent(config)
	out := parts[:0]
	for _, p := range parts {
		if !a.hidden[headerSlotOfPart(p)] {
			out = append(out, p)
		}
	}
	return out
}

// headerPlanItem is one control on the right of a top bar, placed: x is its
// left edge and w its width. part is the status part a cluster control draws;
// the corner's buttons have none.
type headerPlanItem struct {
	slot headerSlot
	part string
	x, w int32
}

// headerPlan lays the right of a top bar out, right to left from right: the
// status parts (each measured by partW) and the corner's buttons (corner
// holds their widths by slot, 0 for a button that is not drawn), in the
// user's order. Two status parts side by side are partGap apart, anything
// else cornerGap.
func headerPlan(config *Config, parts []string, partW func(string) int32, corner *[hsCount]int32, partGap, cornerGap, right int32) []headerPlanItem {
	a := headerArrangementCurrent(config)
	items := make([]headerPlanItem, 0, len(parts)+4)
	// Every part belongs to exactly one slot and every slot is on the order,
	// so each part is placed once.
	for _, s := range a.order {
		if headerSlotIsCorner(s) {
			if corner != nil && corner[s] > 0 {
				items = append(items, headerPlanItem{slot: s, w: corner[s]})
			}
			continue
		}
		for _, p := range parts {
			if headerSlotOfPart(p) == s {
				items = append(items, headerPlanItem{slot: s, part: p, w: partW(p)})
			}
		}
	}
	x := right
	for i := len(items) - 1; i >= 0; i-- {
		items[i].x = x - items[i].w
		x = items[i].x
		if i > 0 {
			if items[i-1].part != "" && items[i].part != "" {
				x -= partGap
			} else {
				x -= cornerGap
			}
		}
	}
	return items
}

// headerPlanFit drops status parts the way a crowded bar always has
// (statusDropIndex) until the plan starts at or right of left. It returns the
// parts kept and their plan.
func headerPlanFit(config *Config, parts []string, partW func(string) int32, corner *[hsCount]int32, partGap, cornerGap, right, left int32) ([]string, []headerPlanItem) {
	for {
		plan := headerPlan(config, parts, partW, corner, partGap, cornerGap, right)
		if len(plan) == 0 || plan[0].x >= left || len(parts) <= 1 {
			return parts, plan
		}
		drop := statusDropIndex(parts)
		parts = append(parts[:drop], parts[drop+1:]...)
	}
}

// headerSlotRects are where the header on screen drew each slot this frame
// (empty when not drawn). Edit Home's Top bar row rings the live control.
var headerSlotRects [hsCount]sdl.Rect

// headerSlotRectsClear forgets the last frame's slots.
func headerSlotRectsClear() { headerSlotRects = [hsCount]sdl.Rect{} }

// headerGrowStatusButtons grows the four status buttons' hit areas
// (growStatusButtonRects) with every other drawn control as a neighbour they
// must stop short of, wherever the user put them.
func headerGrowStatusButtons(barH int32) {
	var others [hsCount]sdl.Rect
	n := 0
	for s := headerSlot(0); s < hsCount; s++ {
		switch s {
		case hsBrightness, hsVolume, hsBluetooth, hsWiFi:
			continue
		}
		if r := headerSlotRects[s]; r.W > 0 {
			others[n] = r
			n++
		}
	}
	growStatusButtonRects(barH, others[:n]...)
}

// homeHeaderFocusIDs are Home's top-bar controls in the focus graph.
var homeHeaderFocusIDs = [...]HomeFocusID{
	HomeFocusBrightness, HomeFocusVolume, HomeFocusBluetooth, HomeFocusWiFi, HomeFocusWeather,
	HomeFocusClock, HomeFocusNowPlaying, HomeFocusUpdate, HomeFocusTrophy, HomeFocusBug, HomeFocusBell,
}

// isHomeHeaderFocusID reports whether id is one of them.
func isHomeHeaderFocusID(id HomeFocusID) bool {
	for _, o := range homeHeaderFocusIDs {
		if o == id {
			return true
		}
	}
	return false
}

// homeHeaderNeighbor is the D-pad's step from a top-bar control on Home,
// taken from where the controls are drawn, since the user can put them in
// any order: Left and Right go to the nearest enabled control on that side
// (Left past the first one is the search pill, Right past the last one stays
// put), Down to whichever of the pairing code and the Remote Play launchers
// is nearest under it. ok is false when the geometry is not known (no Home on
// screen, the control not drawn yet, nothing under it), and the static graph
// answers instead.
func homeHeaderNeighbor(f *HomeFocusController, id HomeFocusID, dir FocusDirection) (HomeFocusID, bool) {
	// Only the live Home's own controller: its rects are this Home's.
	h := homeLayout
	if h == nil || f == nil || h.Focus != f || !isHomeHeaderFocusID(id) {
		return "", false
	}
	cur := h.Items[id].Rect
	if cur.W <= 0 {
		return "", false
	}
	cx := cur.X + cur.W/2
	switch dir {
	case FocusLeft, FocusRight:
		best, bestD := HomeFocusID(""), int32(0)
		for _, o := range homeHeaderFocusIDs {
			if o == id || !f.IsEnabled(o) {
				continue
			}
			r := h.Items[o].Rect
			if r.W <= 0 {
				continue
			}
			d := r.X + r.W/2 - cx
			if dir == FocusLeft {
				d = -d
			}
			if d <= 0 {
				continue
			}
			if best == "" || d < bestD {
				best, bestD = o, d
			}
		}
		if best != "" {
			return best, true
		}
		if dir == FocusLeft {
			return HomeFocusSearch, true
		}
		return id, true
	case FocusDown:
		best, bestD := HomeFocusID(""), int32(0)
		for _, c := range [...]struct {
			id HomeFocusID
			r  sdl.Rect
		}{{HomeFocusQR, companionHeaderRect}, {HomeFocusPS4, ps4TileRect}, {HomeFocusPS5, ps5TileRect}} {
			if c.r.W <= 0 || !f.IsEnabled(c.id) {
				continue
			}
			d := int32(0)
			if cx < c.r.X {
				d = c.r.X - cx
			} else if cx > c.r.X+c.r.W {
				d = cx - (c.r.X + c.r.W)
			}
			if best == "" || d < bestD {
				best, bestD = c.id, d
			}
		}
		if best != "" {
			return best, true
		}
	}
	return "", false
}
