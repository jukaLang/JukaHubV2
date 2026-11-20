package main

// profiles_ui.go — the faces of profiles (profiles_store.go): Settings →
// General → Profiles (a chain of the hub's own list sheets), the "Who is
// playing?" picker shared by the start-up question and the Start menu's
// Switch profile row, and the chip beside the top bar's Menu pill.
//
// Nothing here shows until a second profile exists or a Guest session is on:
// only the Settings row is always there.

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
)

const profilesStartMenuName = "Switch profile"

// profilesColour is a palette entry as a colour.
func profilesColour(i int) sdl.Color {
	if c, ok := parseHexColor(profilesColours[profilesClampColour(i)].hex); ok {
		return c
	}
	return accentColor
}

// profilesInitial is the letter on an avatar.
func profilesInitial(name string) string {
	r, size := utf8.DecodeRuneInString(strings.TrimSpace(name))
	if size == 0 || r == utf8.RuneError {
		return "?"
	}
	return string(unicode.ToUpper(r))
}

// profilesSwitchFromUI switches and says so.
func profilesSwitchFromUI(c *Config, id string) bool {
	if err := profilesSwitch(c, id); err != nil {
		ShowToast(err.Error(), ToastKindWarning)
		return false
	}
	if profilesGuestActive() {
		ShowToast("Guest session: play anything — nothing is kept when it ends", ToastKindSuccess)
	} else {
		ShowToast("Now playing: "+profilesCurrentName(), ToastKindSuccess)
	}
	setHub.dirty = true
	return true
}

// profilesActiveValue is the sheet value of whoever is playing.
func profilesActiveValue() string {
	a := profilesCurrent()
	switch {
	case a == nil:
		return "p:"
	case a.Guest:
		return profilesGuestID
	}
	return "p:" + a.ID
}

// profilesPeople is the picker's rows: the owner, every profile and Guest.
func profilesPeople(doc profilesDoc) []setOption {
	playing := profilesActiveValue()
	note := func(value, text string) string {
		if value == playing {
			return "Playing now · " + text
		}
		return text
	}
	opts := make([]setOption, 0, len(doc.Profiles)+2)
	opts = append(opts, setOption{Value: "p:", Label: profilesOwnerLabel(doc),
		Note: note("p:", "Owner: the saves and settings JukaHub always kept"), Swatch: profilesColours[profilesClampColour(doc.OwnerColour)].hex})
	for _, p := range doc.Profiles {
		opts = append(opts, setOption{Value: "p:" + p.ID, Label: p.Name,
			Note: note("p:"+p.ID, "Own saves, Continue list, trophies and look"), Swatch: profilesColours[profilesClampColour(p.Colour)].hex})
	}
	opts = append(opts, setOption{Value: profilesGuestID, Label: "Guest",
		Note: note(profilesGuestID, "Try any game; nothing is kept when the session ends")})
	return opts
}

// profilesValueID turns a sheet value into a profile id ("" the owner).
func profilesValueID(value string) string {
	if value == profilesGuestID {
		return profilesGuestID
	}
	return strings.TrimPrefix(value, "p:")
}

// ── Settings → General → Profiles ────────────────────────────────────────

// profilesSettingsRow is the General section's row (setmodel.go).
func profilesSettingsRow() setRow {
	return setRow{
		ID: "profiles", Label: "Profiles", Kind: setKindAction,
		Detail: profilesRowDetail,
		Value:  profilesRowValue,
		Open:   profilesOpenPage,
	}
}

func profilesRowDetail(*Config) string {
	if profilesCurrent() == nil && !profilesAny() {
		return "Let others play with their own saves and trophies, or as a Guest"
	}
	if hintsUseKeyboard() {
		return "Own saves, Continue list, trophies and look · F10 → Switch profile"
	}
	return "Own saves, Continue list, trophies and look · Start → Switch profile"
}

func profilesRowValue(*Config) string {
	if a := profilesCurrent(); a != nil {
		return a.Name
	}
	if !profilesAny() {
		return "Just you"
	}
	return profilesOwnerLabel(profilesSnapshot())
}

// profilesOpenPage opens the list: everyone, Guest, Add, and the start-up
// question.
func profilesOpenPage(c *Config) {
	doc := profilesSnapshot()
	opts := profilesPeople(doc)
	if len(doc.Profiles) < profilesMax {
		opts = append(opts, setOption{Value: "add", Label: "Add a profile",
			Note: "Type a name; they start with your look and an empty library"})
	}
	if len(doc.Profiles) > 0 {
		opts = append(opts, setOption{Value: "ask", Label: "Ask who is playing at start: " + setOnOff(doc.AskAtStart),
			Note: "Off: JukaHub starts as whoever played last"})
	}
	setSheetOpen(&setHub.sheet, "Profiles", "Playing now: "+profilesCurrentName(), opts, profilesActiveValue(),
		func(value string) { profilesPageChoose(c, value) })
}

func profilesPageChoose(c *Config, value string) {
	switch value {
	case "add":
		tbxOpenPrompt("New profile", "Their name", "", func(text string) {
			if strings.TrimSpace(text) == "" {
				return
			}
			p, err := profilesAdd(c, text)
			if err != nil {
				ShowToast(err.Error(), ToastKindWarning)
				return
			}
			ShowToast("Profile added: "+p.Name+" · Start → "+profilesStartMenuName+" to play as them", ToastKindSuccess)
			setHub.dirty = true
		})
	case "ask":
		doc := profilesSnapshot()
		profilesSetAskAtStart(!doc.AskAtStart)
		setHub.dirty = true
		profilesOpenPage(c)
	default:
		profilesOpenPerson(c, profilesValueID(value))
	}
}

// profilesOpenPerson opens one person's sheet: switch, rename, colour, their
// save folders, what they share with the owner, delete.
func profilesOpenPerson(c *Config, id string) {
	doc := profilesSnapshot()
	owner := profilesOwnerLabel(doc)
	name, colour := owner, doc.OwnerColour
	guest := id == profilesGuestID
	switch {
	case guest:
		name = "Guest"
	case id != "":
		p, ok := profilesFind(doc, id)
		if !ok {
			ShowToast("That profile was deleted", ToastKindWarning)
			return
		}
		name, colour = p.Name, p.Colour
	}
	playingNow := profilesValueID(profilesActiveValue()) == id
	var opts []setOption
	if !playingNow {
		label, note := "Switch to "+name, "Their saves, Continue list, trophies and look"
		if guest {
			label, note = "Start a Guest session", "No history, playtime or trophies; saves are wiped when it ends"
		}
		opts = append(opts, setOption{Value: "switch", Label: label, Note: note})
	}
	if !guest {
		opts = append(opts,
			setOption{Value: "rename", Label: "Rename", Note: name},
			setOption{Value: "colour", Label: "Colour", Note: profilesColours[profilesClampColour(colour)].name,
				Swatch: profilesColours[profilesClampColour(colour)].hex})
	}
	if id != "" {
		where := "RetroArch saves and states: their own folder"
		if dir := profilesDirFor(id); dir != "" {
			saves, _ := profilesSaveDirs(dir)
			where = "RetroArch: " + saves
		}
		if guest {
			where = "RetroArch: a scratch folder, wiped when the session ends"
		}
		opts = append(opts, setOption{Value: "saves", Label: "Own saves", Note: where})
		shared := "Standalone emulators (PPSSPP, DraStic…) — asked before the first game"
		if list := profilesSharedEmulators(); len(list) > 0 {
			shared = strings.Join(list, ", ") + " — asked before the first game"
		}
		opts = append(opts, setOption{Value: "shared", Label: "Saves shared with " + owner, Note: shared})
	}
	if guest {
		opts = append(opts, setOption{Value: "about", Label: "Your look, read-only",
			Note: "A Guest sees " + owner + "'s settings; changes made as Guest are not kept"})
	} else if id != "" {
		opts = append(opts, setOption{Value: "delete", Label: "Delete profile",
			Note: "Asks twice; its folder is kept in profiles/.deleted"})
	}
	opts = append(opts, setOption{Value: "back", Label: "Back to Profiles"})
	note := "Owner"
	switch {
	case playingNow:
		note = "Playing now"
	case guest:
		note = "Anyone, for one session"
	case id != "":
		note = "Profile"
	}
	setSheetOpen(&setHub.sheet, name, note, opts, "", func(value string) { profilesPersonChoose(c, id, name, value) })
}

func profilesPersonChoose(c *Config, id, name, value string) {
	switch value {
	case "switch":
		profilesSwitchFromUI(c, id)
	case "rename":
		tbxOpenPrompt("Rename "+name, "Name", name, func(text string) {
			if strings.TrimSpace(text) == "" {
				return
			}
			if err := profilesRename(c, id, text); err != nil {
				ShowToast(err.Error(), ToastKindWarning)
				return
			}
			ShowToast("Renamed: "+profilesCleanName(text), ToastKindSuccess)
			setHub.dirty = true
		})
	case "colour":
		opts := make([]setOption, 0, len(profilesColours))
		current := ""
		doc := profilesSnapshot()
		now := doc.OwnerColour
		if p, ok := profilesFind(doc, id); ok {
			now = p.Colour
		}
		for i, col := range profilesColours {
			v := string(rune('0' + i))
			if i == now {
				current = v
			}
			opts = append(opts, setOption{Value: v, Label: col.name, Swatch: col.hex})
		}
		setSheetOpen(&setHub.sheet, "Colour", name, opts, current, func(v string) {
			if len(v) == 1 && v[0] >= '0' && v[0] <= '9' {
				profilesSetColour(id, int(v[0]-'0'))
				setHub.dirty = true
			}
			profilesOpenPerson(c, id)
		})
	case "saves":
		if dir := profilesDirFor(id); dir != "" && id != profilesGuestID {
			saves, states := profilesSaveDirs(dir)
			ShowToast("Saves: "+saves+" · States: "+states, ToastKindInfo)
		} else {
			ShowToast("A Guest's saves are wiped when the session ends", ToastKindInfo)
		}
	case "shared":
		ShowToast("These emulators keep one save folder for everyone. JukaHub stops the first start of each and says so; start again to play.", ToastKindInfo)
	case "about":
		profilesOpenPerson(c, id)
	case "delete":
		profilesConfirmDelete(c, id, name)
	case "back":
		profilesOpenPage(c)
	}
}

// profilesConfirmDelete asks twice before a profile goes.
func profilesConfirmDelete(c *Config, id, name string) {
	if a := profilesCurrent(); a != nil && a.ID == id {
		ShowToast("Switch to someone else first", ToastKindWarning)
		return
	}
	setConfirm(c, "Delete "+name+"?",
		name+"'s Continue list, playtime, trophies, favourites and settings leave the list, and their RetroArch saves with them.",
		"trash", "Delete", func(c *Config) {
			setConfirm(c, "Really delete "+name+"?",
				"Their folder is moved to profiles/.deleted in JukaHub's data folder, where Files can still reach it. Nothing else is touched.",
				"trash", "Delete "+name, func(*Config) {
					dest, err := profilesDelete(id)
					switch {
					case err != nil:
						ShowToast("Not deleted: "+err.Error(), ToastKindError)
					case dest != "":
						ShowToast(name+" deleted · their folder is in "+dest, ToastKindSuccess)
					default:
						ShowToast(name+" deleted", ToastKindSuccess)
					}
				})
		})
}

// ── The picker ───────────────────────────────────────────────────────────

// profilesPicker is "Who is playing?", over any screen: from the Start menu,
// the chip, and at start when the question is on. It is drawn by
// renderTopBarOverlays and takes the keys and the pad while it is up.
var profilesPicker struct {
	sheet setSheetState
}

// profilesPickerOpen opens the picker; boot is the start-up question.
func profilesPickerOpen(c *Config, boot bool) {
	if !boot && profilesGameRunning() {
		ShowToast("Quit the game first, then switch", ToastKindWarning)
		return
	}
	note := "Everyone has their own saves, Continue list and trophies"
	if boot {
		note = "Pick yourself; Guest keeps nothing"
	}
	setSheetOpen(&profilesPicker.sheet, "Who is playing?", note, profilesPeople(profilesSnapshot()), profilesActiveValue(),
		func(value string) {
			id := profilesValueID(value)
			if id == profilesValueID(profilesActiveValue()) {
				ShowToast("Playing: "+profilesCurrentName(), ToastKindInfo)
				return
			}
			profilesSwitchFromUI(c, id)
		})
}

// profilesPickerKey is the picker's keyboard twin (main.go's key chain).
func profilesPickerKey(e *sdl.KeyboardEvent, _ *Config) bool {
	if !profilesPicker.sheet.open || e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	setSheetKey(&profilesPicker.sheet, e.Keysym.Sym)
	return true
}

// profilesPickerController takes the pad while the picker is up
// (handleControllerEvent).
func profilesPickerController(e *sdl.ControllerButtonEvent, _ *Config) bool {
	if !profilesPicker.sheet.open || e == nil {
		return false
	}
	setSheetPad(&profilesPicker.sheet, e.Button)
	return true
}

// profilesPickerMouse takes a click while the picker is up: a row chooses, a
// click outside the card closes it, nothing reaches the screen behind.
func profilesPickerMouse(mx, my int32) bool {
	if !profilesPicker.sheet.open {
		return false
	}
	setSheetMouse(&profilesPicker.sheet, mx, my)
	return true
}

// renderProfilesPicker draws the picker and its hints (renderTopBarOverlays).
func renderProfilesPicker(renderer *sdl.Renderer, config *Config) {
	s := &profilesPicker.sheet
	if renderer == nil || !s.open {
		return
	}
	setSheetDraw(renderer, config, s)
	small := FontForRole(config, TypeLabel)
	if small == nil || s.card.W == 0 {
		return
	}
	smallH := int32(small.Height())
	hy := s.card.Y + s.card.H + 10
	if hy+smallH+6 > screenHeight {
		return
	}
	hx := s.card.X
	for _, h := range [2][2]string{{"A", "Choose"}, {"B", "Close"}} {
		bw, _ := measureText(small, hintKeyFor(h[0], h[1]))
		badge := sdl.Rect{X: hx, Y: hy, W: bw + 20, H: smallH + 6}
		hx = drawGlassHintBadge(renderer, config, small, badge, h[0], h[1], badge.X+badge.W+8, hy+3) + 24
	}
}

// ── The Start menu row and the top bar's chip ────────────────────────────

// profilesStartMenuInsert puts Switch profile first in the Device group once
// there is someone to switch to (startmenu.go).
func profilesStartMenuInsert(device []startMenuEntry) []startMenuEntry {
	if profilesCurrent() == nil && !profilesAny() {
		return device
	}
	row := startMenuEntry{name: profilesStartMenuName, icon: "P", glyph: "user", badge: profilesCurrentName(),
		action: func(c *Config) { profilesPickerOpen(c, false) }}
	return append([]startMenuEntry{row}, device...)
}

// profilesChipRect is the chip as drawn this frame; empty when none is.
var profilesChipRect sdl.Rect

// profilesDrawHeaderChip draws who is playing at x on the top bar — an initial
// in the person's colour and their name, or GUEST — only once there is someone
// besides the owner or a Guest session is on. It returns the room it took
// with its gap, 0 when nothing was drawn (renderStatusBar).
func profilesDrawHeaderChip(renderer *sdl.Renderer, config *Config, x, barH int32) int32 {
	profilesChipRect = sdl.Rect{}
	a := profilesCurrent()
	if a == nil && !profilesAny() {
		return 0
	}
	font := headerCaptionFont(config)
	if font == nil {
		return 0
	}
	name, col := "", accentColor
	switch {
	case a == nil:
		doc := profilesSnapshot()
		name, col = profilesOwnerLabel(doc), profilesColour(doc.OwnerColour)
	case a.Guest:
		name, col = "GUEST", sdl.Color{R: 235, G: 170, B: 60, A: 255}
	default:
		name, col = a.Name, profilesColour(a.Colour)
	}
	name = ellipsize(font, name, 140)
	sh := headerPillHeight()
	dot := sh - 8
	if dot < 12 {
		dot = 12
	}
	tw, _ := measureText(font, name)
	w := 4 + dot + 6 + tw + 10
	y := (barH - sh) / 2
	fillRoundedRect(renderer, x, y, w, sh, sh/2, WithAlpha(col, 45))
	strokeRoundedRect(renderer, x, y, w, sh, sh/2, 1, WithAlpha(col, 210))
	cx, cy := x+4+dot/2, y+sh/2
	fillCircle(renderer, cx, cy, dot/2, col)
	initial := profilesInitial(name)
	if a != nil && a.Guest {
		initial = "G"
	}
	iw, ih := measureText(font, initial)
	renderTextRaw(renderer, config, font, initial, sdl.Color{R: 255, G: 255, B: 255, A: 255}, cx-iw/2, cy-ih/2)
	textY := y + (sh-int32(font.Height()))/2
	if a != nil && a.Guest {
		renderText(renderer, config, font, name, col, x+4+dot+6, textY)
	} else {
		renderTextRaw(renderer, config, font, name, ColorTextPrimary(), x+4+dot+6, textY)
	}
	profilesChipRect = sdl.Rect{X: x, Y: y, W: w, H: sh}
	return SpaceSM + w
}

// profilesHeaderChipClick opens the picker from a tap on the chip
// (handleChromeBarClick).
func profilesHeaderChipClick(mx, my int32, config *Config) bool {
	if profilesChipRect.W == 0 || !hitTouchTarget(profilesChipRect, mx, my) {
		return false
	}
	PlayActivateSound()
	profilesPickerOpen(config, false)
	return true
}
