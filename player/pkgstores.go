package main

// pkgstores.go — the "More stores" strip at the top of the Packages screen.
//
// Packages installs JukaLang packages, and it is also where people look for
// anything new to put on the handheld. The other places that do that lived
// elsewhere: the APK Store (Android games, apkstore.go) and PortMaster's ports
// (ports.go) only in the Start menu's Games group, next to Windows Apps
// (windowsapps.go), and a tester could not find the APK Store at all. The
// strip puts all three one press away, between the toolbar and the shelves, in
// every view — and in the empty state too, because the APK Store and Windows
// Apps work without a package list or a network.
//
// It is one more focus row above the content: Up at the top of the rows, the
// grid or the list lands on it, Up again reaches the top bar, Down goes back
// to the content, Left and Right walk its pills and A opens one. While the
// model holds no packages at all the strip is the only thing to focus, so it
// has the focus. pkgBodyFocused is false while the strip holds it, so the
// hero, the shelves and the grid draw no ring under it.

import "github.com/veandco/go-sdl2/sdl"

const (
	// pkgStoresH is the strip's band. The pills are a little shorter, so a
	// focused one lifts into the gaps above and below it without touching
	// the toolbar or the first shelf.
	pkgStoresH      = int32(40)
	pkgStorePillH   = int32(36)
	pkgStorePillGap = int32(10)
	pkgStoreGlyph   = int32(20)
	// pkgStorePadL and pkgStorePadR are a pill's insets; pkgStoreInGap is the
	// space between its glyph, title and sub-label.
	pkgStorePadL  = int32(12)
	pkgStorePadR  = int32(16)
	pkgStoreInGap = int32(8)
	// pkgStoreLabelGap is the space after the strip's label.
	pkgStoreLabelGap = int32(14)
	pkgStoresLabel   = "More stores"
	// pkgStoreMaxLinks bounds the pills one frame measures.
	pkgStoreMaxLinks = 4
)

// pkgStoreLink is one pill: what it says and what it opens. screen marks a
// link that changes scene (the APK Store): it gets the activation sound here,
// and Back from it returns to the pill. The panels (PortMaster, Windows Apps)
// play their own sound and close back onto the strip.
type pkgStoreLink struct {
	title  string
	sub    string
	glyph  string
	screen bool
	open   func(*Config)
}

// pkgStoresState is the strip's focus. Main thread only.
type pkgStoresState struct {
	// focus: the pad is on the strip (it was entered from the content, or
	// used while it was the only thing on the screen).
	focus  bool
	cursor int
	// leftFor marks a screen opened from the strip: the visit that comes back
	// from it keeps the pad on the strip instead of starting on the content.
	leftFor bool
	// leftScene is the scene the last scene change left (packageActionsForget
	// records it on every change). leftFor only counts when the arrival comes
	// straight from the screen the strip opened: a user who went on from the
	// APK Store to Home and later opens Packages again starts on the content.
	leftScene string
}

var pkgStores pkgStoresState

// On the Smart Pro the four package destinations form one trigger-navigable
// loop. PortMaster and Windows Apps are panels over Packages; the APK Store is
// a scene, so a trigger leaving it can request a panel for Packages' arrival.
const (
	pkgHubPackages = iota
	pkgHubAPK
	pkgHubPorts
	pkgHubWindows
	pkgHubCount
)

var pkgHubPending int // a panel to open after returning from the APK scene

func pkgHubEnabled() bool { return DetectDevice() == DeviceTrimuiSmartPro }

func pkgHubStep(current int, s padShoulder) int {
	if current < 0 || current >= pkgHubCount {
		return -1
	}
	switch s {
	case padL2:
		return (current + pkgHubCount - 1) % pkgHubCount
	case padR2:
		return (current + 1) % pkgHubCount
	}
	return -1
}

// pkgHubLocation accepts only the package journey. An APK Store opened from
// Start or another scene keeps its normal input, as do panels over Emulators.
func pkgHubLocation(config *Config) int {
	if !pkgHubEnabled() || config == nil || transitionPhase != "none" {
		return -1
	}
	switch tbxCurrentScene(config) {
	case "Packages":
		switch {
		case winAppsPanel.open:
			if !winAppsPanel.returnEmu && !winAppsPanel.help && modalPanelBlockReason() == "Windows apps panel" {
				return pkgHubWindows
			}
			return -1
		case portsPanel.open:
			if !portsPanel.returnEmu && modalPanelBlockReason() == "ports panel" {
				return pkgHubPorts
			}
			return -1
		case pkgUI.sheet.open || tbxSheetVisible(config) || modalPanelBlocksShoulders():
			return -1
		default:
			return pkgHubPackages
		}
	case apkStoreSceneName:
		if pkgStores.leftFor && pkgStores.leftScene == "Packages" && !tbxSheetVisible(config) && !modalPanelBlocksShoulders() {
			return pkgHubAPK
		}
	}
	return -1
}

// pkgHubShoulder switches the visible destination on one L2/R2 press.
// It runs before the generic modal-panel blocker in shoulders.go.
func pkgHubShoulder(s padShoulder, config *Config) bool {
	if s != padL2 && s != padR2 {
		return false
	}
	current := pkgHubLocation(config)
	next := pkgHubStep(current, s)
	if next < 0 {
		return false
	}
	if current == pkgHubPorts {
		closePortsPanel()
	} else if current == pkgHubWindows {
		closeWindowsAppsPanel()
	}
	if current == pkgHubAPK {
		idx := sceneIndexExact(config, "Packages")
		if idx < 0 {
			return false
		}
		pkgStores.focus = true
		if next > pkgHubPackages {
			pkgStores.cursor = next - 1
		}
		pkgStores.leftFor = true
		pkgHubPending = next
		switchScene(config, idx, false)
		if transitionPhase != "fade-out" {
			pkgHubPending = pkgHubPackages
			return false
		}
		return true
	}
	if next == pkgHubPackages {
		pkgStores.focus = true
		PlayNavSound()
		return true
	}
	pkgStores.focus = true
	pkgStores.cursor = next - 1
	pkgStoresOpen(config, next-1)
	return true
}

// pkgHubOnPackagesEnter fulfils an APK-scene trigger switch after the scene
// has actually changed, so the destination panel appears with Packages.
func pkgHubOnPackagesEnter() {
	next := pkgHubPending
	pkgHubPending = pkgHubPackages
	switch next {
	case pkgHubPorts:
		openPortsPanel(false)
	case pkgHubWindows:
		openWindowsAppsPanel(false)
	}
}

// pkgStoreLinkCache is the pills, built on first use (the platform does not
// change while JukaHub runs).
var pkgStoreLinkCache []pkgStoreLink

// pkgStoreLinks is the strip's pills.
func pkgStoreLinks() []pkgStoreLink {
	if pkgStoreLinkCache == nil {
		pkgStoreLinkCache = pkgBuildStoreLinks(IsWindows())
	}
	return pkgStoreLinkCache
}

// pkgBuildStoreLinks is the pills for a platform. The APK Store and PortMaster
// are the handheld's, the same rule the Start menu's Games group follows;
// Windows Apps is everywhere (Wine on the handheld, the programs themselves on
// Windows).
func pkgBuildStoreLinks(windows bool) []pkgStoreLink {
	links := make([]pkgStoreLink, 0, 3)
	if !windows {
		links = append(links,
			pkgStoreLink{title: apkStoreEntryName, sub: "Native Android APKs", glyph: apkStoreGlyph, screen: true,
				open: apkStoreOpen},
			pkgStoreLink{title: portMasterAppName, sub: "PC and indie ports", glyph: "packages",
				open: func(*Config) { openPortsPanel(false) }},
		)
	}
	sub := "Windows programs through Wine"
	if windows {
		sub = "Programs on this PC"
	}
	return append(links, pkgStoreLink{title: windowsAppsEntryName, sub: sub, glyph: "apps",
		open: func(*Config) { openWindowsAppsPanel(false) }})
}

// pkgStoresOnly reports a model with no packages in it (no list yet, nothing
// installed, no release card): the strip is then all the screen can offer.
func pkgStoresOnly() bool {
	m := pkgUI.model
	return m == nil || (len(m.Items) == 0 && m.App == nil)
}

// pkgStoresFocused reports whether the strip holds the store's focus: it was
// entered, or there is nothing else to focus. The details sheet, when open,
// owns the focus instead.
func pkgStoresFocused() bool {
	if pkgUI.sheet.open || len(pkgStoreLinks()) == 0 {
		return false
	}
	return pkgStores.focus || pkgStoresOnly()
}

// pkgStoresHasPad reports whether a pill draws the focus ring this frame: the
// strip holds the store's focus and the pad is on the store, not the top bar.
func pkgStoresHasPad() bool {
	return chromeTier(focusEngine.ElementIndex()) == 0 && pkgStoresFocused()
}

// pkgStoresOnEnter runs on every arrival (pkgOnSceneEnter): a visit starts on
// the content, except the one coming straight back from the screen the strip
// opened. The mark is spent either way, so it can never carry over to a later,
// unrelated visit.
func pkgStoresOnEnter() {
	back := pkgStores.leftFor && pkgStores.leftScene == apkStoreSceneName
	pkgStores.leftFor = false
	if !back {
		pkgStores.focus = false
	}
}

// pkgStoresNoteDeparture records the scene a scene change is leaving
// (packageActionsForget calls it from switchScene, while currentSceneIndex is
// still that scene).
func pkgStoresNoteDeparture() {
	pkgStores.leftScene = ""
	if c := appConfig; c != nil && currentSceneIndex >= 0 && currentSceneIndex < len(c.Scenes) {
		pkgStores.leftScene = c.Scenes[currentSceneIndex].Name
	}
}

// pkgStoresEnterFromContent is Up from the top of a view: the strip takes the
// focus (the top bar is the next Up).
func pkgStoresEnterFromContent() {
	links := pkgStoreLinks()
	if len(links) == 0 {
		focusChromeBar()
		return
	}
	pkgStores.focus = true
	pkgStores.cursor = clampIndex(pkgStores.cursor, len(links))
	PlayNavSound()
}

// pkgStoresDo is an action while the strip holds the focus. It reports false
// for the actions that belong to the view under it — Tab's filter chips, and
// the jump to the last row — which pkgDo then hands on.
func pkgStoresDo(config *Config, act pkgAct) bool {
	links := pkgStoreLinks()
	if len(links) == 0 {
		pkgStores.focus = false
		return false
	}
	// With nothing else on the screen the strip has the focus without having
	// been entered. Once it is used it keeps it, so a package list that
	// arrives meanwhile does not pull the ring out from under the pad.
	pkgStores.focus = true
	cur := clampIndex(pkgStores.cursor, len(links))
	pkgStores.cursor = cur
	switch act {
	case pkgActLeft, pkgActRight:
		next := cur + 1
		if act == pkgActLeft {
			next = cur - 1
		}
		if next >= 0 && next < len(links) {
			pkgStores.cursor = next
			PlayNavSound()
		}
	case pkgActUp:
		// The strip keeps its flag: Down from the top bar lands on it again.
		focusChromeBar()
	case pkgActDown, pkgActPageDown:
		if !pkgStoresOnly() {
			pkgStores.focus = false
			pkgUI.reveal = true
			PlayNavSound()
		}
	case pkgActLast:
		// L2 / End jump to the content's last row. A strip entered from the
		// grid's or the list's filter chips would hand the jump back to the
		// chips, which ignore it, so the chips let go of the pad first.
		if pkgStoresOnly() {
			return true
		}
		pkgStores.focus = false
		if pkgUI.view != pkgViewRows {
			pkgUI.chipFocus = false
		}
		return false
	case pkgActConfirm:
		pkgStoresOpen(config, cur)
	case pkgActNextChip, pkgActPrevChip:
		return false
	}
	return true
}

// pkgStoresOpen opens pill i. Nothing opens while Packages is fading out: input
// is not gated during a scene fade, and a panel opened then would float over
// the screen being arrived at (packageActionsForget drops the sheet and the
// press for the same reason).
func pkgStoresOpen(config *Config, i int) {
	links := pkgStoreLinks()
	if i < 0 || i >= len(links) || links[i].open == nil || transitionPhase == "fade-out" {
		return
	}
	if config == nil {
		config = appConfig
	}
	l := links[i]
	if l.screen {
		// No scene change can start while the arrival fade still runs
		// (switchScene refuses it), so the press is not answered with a sound.
		if transitionPhase != "none" {
			return
		}
		PlayActivateSound()
	}
	l.open(config)
	if l.screen {
		// Marked only when the scene change really started: a switch refused
		// (or a screen already up) must not send a later visit to the strip.
		pkgStores.leftFor = transitionPhase == "fade-out"
	}
}

// pkgStoresClick is a press on pill i: the pad's focus moves there and the
// pill opens at once (the strip does not scroll, so there is no drag to wait
// for, unlike a card).
func pkgStoresClick(config *Config, i int) {
	pkgStores.focus = true
	pkgStores.cursor = i
	pkgStoresOpen(config, i)
}

// pkgStoresFooterHints is the footer while the strip holds the focus.
func pkgStoresFooterHints() []FooterHint {
	hints := []FooterHint{
		{Button: "D-Pad", Label: "Choose"},
		{Button: "A", Label: "Open"},
		{Button: "B", Label: "Back"},
	}
	if pkgHubEnabled() {
		hints = append(hints, FooterHint{Button: "L2/R2", Label: "Stores"})
	}
	return append(hints, FooterHint{Button: "X", Label: "Refresh"}, FooterHint{Button: "Y", Label: "View"})
}

// pkgDrawStores paints the strip in r: its label and the pills, left to
// right, and records each pill as a click target. A canvas too narrow for
// everything (the Brick, a large text size) drops the label first, then the
// sub-labels — the label returns when the bare pills leave room for it — and
// only then shortens the titles, the pills sharing what is left.
func pkgDrawStores(renderer *sdl.Renderer, config *Config, ft *pkgType, r sdl.Rect) {
	links := pkgStoreLinks()
	if renderer == nil || ft == nil || ft.body == nil || ft.meta == nil || len(links) == 0 || r.W <= 0 {
		return
	}
	if len(links) > pkgStoreMaxLinks {
		links = links[:pkgStoreMaxLinks]
	}
	n := int32(len(links))
	fixed := pkgStorePadL + pkgStoreGlyph + pkgStoreInGap + pkgStorePadR
	var titleW, subW [pkgStoreMaxLinks]int32
	for i, l := range links {
		titleW[i], _ = measureText(ft.body, l.title)
		if l.sub != "" {
			subW[i], _ = measureText(ft.meta, l.sub)
		}
	}
	labelW, labelH := measureText(ft.meta, pkgStoresLabel)
	need := func(label, subs bool) int32 {
		w := (n - 1) * pkgStorePillGap
		if label {
			w += labelW + pkgStoreLabelGap
		}
		for i := range links {
			w += fixed + titleW[i]
			if subs && subW[i] > 0 {
				w += pkgStoreInGap + subW[i]
			}
		}
		return w
	}
	// A single pill (Windows Apps on Windows) is not a row of stores, so it
	// goes without the label.
	showLabel, showSubs := n > 1, true
	if need(showLabel, true) > r.W {
		showLabel = false
	}
	if need(false, true) > r.W {
		showSubs = false
		// Without the sub-labels the label may fit again.
		showLabel = n > 1 && need(true, false) <= r.W
	}
	// limit > 0 caps every title: what is left once the pills' own chrome
	// and the gaps between them are paid for, shared equally.
	limit := int32(0)
	if need(false, false) > r.W {
		limit = (r.W-(n-1)*pkgStorePillGap)/n - fixed
		if limit < 24 {
			limit = 24
		}
	}

	midY := r.Y + r.H/2
	x := r.X
	if showLabel {
		renderText(renderer, config, ft.meta, pkgStoresLabel, ColorTextTertiary(), x, midY-labelH/2)
		x += labelW + pkgStoreLabelGap
	}
	ring := pkgStoresHasPad()
	cursor := clampIndex(pkgStores.cursor, len(links))
	for i, l := range links {
		title, tw := l.title, titleW[i]
		if limit > 0 && tw > limit {
			title = ellipsize(ft.body, title, limit)
			tw, _ = measureText(ft.body, title)
		}
		w := fixed + tw
		if showSubs && subW[i] > 0 {
			w += pkgStoreInGap + subW[i]
		}
		pill := sdl.Rect{X: x, Y: midY - pkgStorePillH/2, W: w, H: pkgStorePillH}
		focused := ring && i == cursor
		if focused {
			drawGlassFocusGlow(renderer, pill)
		}
		drawGlassSurface(renderer, pill, pkgStorePillH/2, glassState{Focused: focused, Hovered: pointInRect(mouseX, mouseY, pill)})
		gx := pill.X + pkgStorePadL
		DrawHomeIcon(renderer, l.glyph, sdl.Rect{X: gx, Y: midY - pkgStoreGlyph/2, W: pkgStoreGlyph, H: pkgStoreGlyph}, false)
		tx := gx + pkgStoreGlyph + pkgStoreInGap
		_, th := measureText(ft.body, title)
		renderText(renderer, config, ft.body, title, ColorTextPrimary(), tx, midY-th/2)
		if showSubs && subW[i] > 0 {
			_, sh := measureText(ft.meta, l.sub)
			renderText(renderer, config, ft.meta, l.sub, ColorTextTertiary(), tx+tw+pkgStoreInGap, midY-sh/2+1)
		}
		pkgAddHit(pkgHit{Kind: pkgHitStore, Rect: pill, Index: i})
		x += w + pkgStorePillGap
	}
}
