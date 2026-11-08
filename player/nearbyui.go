package main

// nearbyui.go — the Nearby page (Tools → Nearby): the JukaHubs on this Wi-Fi,
// what they are playing, how many of your games each one has, and "Popular
// nearby" — titles two or more devices have or play — each one a way into the
// Games Library. Everything comes from the LAN while the page is up
// (nearby.go); there is no account and no server.

import (
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// nearbyScreenModel is what the page draws, rebuilt twice a second.
type nearbyScreenModel struct {
	builtAt time.Time
	header  UIPageHeader
	enabled bool
	sharing bool
	lan     string
	view    nearbyView
	devices []peerRowModel
	popular []peerRowModel
}

type nearbyScreenState struct {
	visit tbxVisit
	nav   tbxNav
	model nearbyScreenModel
}

var nearbyScreen nearbyScreenState

type nearbySceneModule struct{ sceneBase }

func (nearbySceneModule) render(r *sdl.Renderer, c *Config) bool {
	nearbyRenderScreen(r, c)
	return true
}

func (nearbySceneModule) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), nearbyScreenAction)
}

func (nearbySceneModule) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), nearbyScreenAction)
}

func init() { registerScene(nearbySceneModule{sceneBase{name: nearbySceneName}}) }

// nearbyOpenScreen opens Nearby (the Tools hub tile).
func nearbyOpenScreen(config *Config) {
	peerOpenScene(config, nearbySceneName)
}

// nearbyHubTile is Nearby's tile on the Tools hub (Together section). It
// needs a network address: everything it shows comes from the LAN.
func nearbyHubTile() toolsHubTile {
	t := toolsHubTile{
		id: "nearby", label: "Nearby", icon: "devices", need: toolsHubNeedLAN,
		value: "Who is playing what on this Wi-Fi",
	}
	switch {
	case !peerEnabledFlag.Load():
		t.value = "Off — turn on Nearby devices to look around"
	case nearbyShareFlag.Load():
		t.value, t.tone = "Sharing what you play", toolsHubToneGood
	}
	return t
}

// nearbyScreenEnter runs once per visit: an empty list and the scan that
// fills it, which stops when the page is left.
func nearbyScreenEnter() {
	nearbyReset()
	nearbyScreen.nav.scroll = 0
	nearbyScreen.nav.focus = ""
	nearbyScreen.model.builtAt = time.Time{}
	runSceneTask(nearbyScanLoop)
}

// nearbyPlayingText is a shared game as a line: "Zelda (Game Boy Advance)".
func nearbyPlayingText(p nearbyPlaying) string {
	if p.System == "" {
		return p.Title
	}
	return p.Title + " (" + p.System + ")"
}

// nearbyBuildModel prepares every string the page draws.
func nearbyBuildModel(now time.Time) nearbyScreenModel {
	m := nearbyScreenModel{
		builtAt: now,
		enabled: peerEnabledFlag.Load(),
		sharing: nearbyShareFlag.Load(),
		lan:     partyLANAddress(),
		view:    nearbySnapshot(),
	}
	status, ink := "Not sharing", ColorTextTertiary()
	switch {
	case !m.enabled:
		status = "Off"
	case m.lan == "":
		status, ink = "Needs Wi-Fi", ToastWarnInk()
	case m.sharing:
		status, ink = "Sharing what you play", ToastSuccessInk()
	}
	m.header = tbxHeader("Nearby", "Who is playing what on this Wi-Fi — no account, nothing leaves your network", "devices", status, ink)
	for _, p := range m.view.peers {
		spec := tbxRowSpec{icon: "gamepad", title: p.Name}
		switch {
		case !p.Share:
			spec.subtitle = "Does not share what it plays"
			spec.dim = true
		case p.Playing != nil:
			spec.subtitle = "Playing " + nearbyPlayingText(*p.Playing)
		case len(p.Recent) > 0:
			spec.subtitle = "Played " + nearbyPlayingText(p.Recent[0]) + " lately"
		default:
			spec.subtitle = "Not playing right now"
		}
		if p.Paired {
			spec.subtitle += " · paired"
		}
		switch {
		case !p.Share:
		case p.Common > 0:
			spec.pill, spec.tone = strconv.Itoa(p.Common)+" of your games", tbxToneAccent
		case p.Common == 0:
			spec.pill, spec.tone = "None of yours", tbxToneNeutral
		default:
			spec.pill, spec.tone = "Reading…", tbxToneNeutral
		}
		m.devices = append(m.devices, peerRowModel{id: "dev:" + p.ID, spec: spec})
	}
	for i, pop := range m.view.popular {
		sub := "On " + strconv.Itoa(pop.Devices) + " devices nearby"
		if pop.Playing > 0 {
			sub += " · " + strconv.Itoa(pop.Playing) + " playing now"
		}
		if pop.System != "" {
			sub = pop.System + " · " + sub
		}
		spec := tbxRowSpec{icon: "sparkles", title: pop.Title, subtitle: sub, pill: "Not in yours", tone: tbxToneNeutral}
		if pop.Mine {
			spec.pill, spec.tone = "In your library", tbxToneGood
		}
		m.popular = append(m.popular, peerRowModel{id: "pop:" + strconv.Itoa(i), spec: spec})
	}
	return m
}

// nearbyRenderScreen draws the page.
func nearbyRenderScreen(renderer *sdl.Renderer, config *Config) {
	if nearbyScreen.visit.begin() {
		nearbyScreenEnter()
	}
	now := time.Now()
	if now.Sub(nearbyScreen.model.builtAt) > 500*time.Millisecond {
		nearbyScreen.model = nearbyBuildModel(now)
	}
	m := &nearbyScreen.model
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, m.header)
	body := frame.bodyRect()
	nav := &nearbyScreen.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	nearbyRenderBody(renderer, config, fonts, frame, m)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	peerEndFrame(renderer, config, nearbyScreenHints())
}

func nearbyRenderBody(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, m *nearbyScreenModel) {
	nav := &nearbyScreen.nav
	top := frame.bodyTop - nav.scroll
	x, w := frame.x, frame.w
	rowH := tbxRowHeight(fonts)
	y := int32(0)
	row := 0
	addRow := func(id string, spec tbxRowSpec) {
		rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		tbxDrawRow(renderer, config, fonts, peerScr(rect, top), spec, nav.focused(id))
		nav.add(id, rect, row, false)
		row++
		y += rowH + 8
	}
	emptyCard := func(icon, title, text string) {
		card := sdl.Rect{X: x, Y: y, W: w, H: 132}
		UICard(renderer, peerScr(card, top), DefaultUICardStyle())
		tbxDrawEmpty(renderer, config, peerScr(card, top), icon, title, text)
		y += card.H + 8
	}

	if !m.enabled {
		addRow("enable", tbxRowSpec{icon: "wifi", title: "Nearby devices", pill: "Off", tone: tbxToneNeutral,
			subtitle: "Turn it on to see the JukaHubs on this Wi-Fi"})
		card := sdl.Rect{X: x, Y: y, W: w, H: 170}
		peerOffCard(renderer, config, peerScr(card, top),
			"Nearby lists the JukaHubs on your Wi-Fi, what they are playing and which of your games they have — without an account. Your game list is never sent: only a fingerprint others can check titles against.")
		nav.extend(y + card.H + 8)
		return
	}

	share := tbxRowSpec{icon: "share", title: "Share what I'm playing on this Wi-Fi", pill: "Off", tone: tbxToneNeutral,
		subtitle: "Off — you still see the others; they do not see you"}
	if m.sharing {
		share.pill, share.tone = "On", tbxToneGood
		share.subtitle = "Others see your current game and can check which of their games you have — not your list"
	}
	addRow("share", share)

	y += 14
	tbxDrawSection(renderer, config, "On this Wi-Fi", "devices", x, top+y, w)
	y += tbxSectionHeight
	if len(m.devices) == 0 {
		switch {
		case m.lan == "":
			emptyCard("wifi-off", "Not on Wi-Fi", "JukaHubs on your network appear here once you are connected.")
		case m.view.scans < 2:
			emptyCard("radar", "Looking around…", "Devices with Nearby devices on show up here in a few seconds.")
		default:
			emptyCard("radar", "No other JukaHub here", "Devices show up when Nearby devices is on there too.")
		}
	}
	for _, r := range m.devices {
		addRow(r.id, r.spec)
	}

	y += 14
	tbxDrawSection(renderer, config, "Popular nearby", "sparkles", x, top+y, w)
	y += tbxSectionHeight
	if len(m.popular) == 0 {
		emptyCard("sparkles", "Nothing popular yet",
			"Titles show up here when two or more devices nearby have them or play them, and they share what they play.")
	}
	for _, r := range m.popular {
		addRow(r.id, r.spec)
	}
	nav.extend(y)
}

// nearbyScreenAction handles one action on the page.
func nearbyScreenAction(config *Config, act tbxAction) bool {
	nav := &nearbyScreen.nav
	switch act {
	case tbxActConfirm:
		nearbyActivate(config, nav.focus)
		return true
	case tbxActX, tbxActY:
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(nav, act)
}

// nearbyScreenClick handles a click on the page's rows.
func nearbyScreenClick(mx, my int32, config *Config) bool {
	it, ok := nearbyScreen.nav.hit(mx, my)
	if !ok {
		return false
	}
	nearbyScreen.nav.focus = it.id
	nearbyActivate(config, it.id)
	return true
}

// nearbyScreenHints is the footer.
func nearbyScreenHints() []FooterHint {
	id := nearbyScreen.nav.focus
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}}
	switch {
	case id == "enable":
		hints = append(hints, FooterHint{Button: "A", Label: "Turn on"})
	case id == "share":
		label := "Share"
		if nearbyShareFlag.Load() {
			label = "Stop sharing"
		}
		hints = append(hints, FooterHint{Button: "A", Label: label})
	case strings.HasPrefix(id, "dev:"):
		hints = append(hints, FooterHint{Button: "A", Label: "Details"})
	case strings.HasPrefix(id, "pop:"):
		hints = append(hints, FooterHint{Button: "A", Label: "Open"})
	}
	return append(hints, FooterHint{Button: "B", Label: "Back"})
}

// nearbyActivate runs the focused row's action.
func nearbyActivate(config *Config, id string) {
	m := &nearbyScreen.model
	switch {
	case id == "enable":
		PlayToggleSound()
		peerSettingSetEnabled(config, true)
		m.builtAt = time.Time{}
	case id == "share":
		PlayToggleSound()
		nearbySettingSetShare(config, !nearbyShareFlag.Load())
		m.builtAt = time.Time{}
	case strings.HasPrefix(id, "dev:"):
		devID := strings.TrimPrefix(id, "dev:")
		for _, p := range m.view.peers {
			if p.ID == devID {
				PlayActivateSound()
				nearbyOpenPeerSheet(config, p)
				return
			}
		}
	case strings.HasPrefix(id, "pop:"):
		n, err := strconv.Atoi(strings.TrimPrefix(id, "pop:"))
		if err != nil || n < 0 || n >= len(m.view.popular) {
			return
		}
		pop := m.view.popular[n]
		if pop.Mine {
			PlayActivateSound()
			nearbyOpenInLibrary(pop.Title)
			return
		}
		PlayActivateSound()
		tbxOpenSheet(config, tbxSheetSpec{
			title:    pop.Title,
			subtitle: "Not in your library",
			icon:     "sparkles",
			body: "On " + strconv.Itoa(pop.Devices) + " devices nearby. Ask a friend which version they play — " +
				"Nearby never copies games between devices.",
			buttons: []tbxSheetButton{{label: "Close", primary: true}},
		})
	}
}

// nearbyOpenInLibrary opens the Games Library on a title.
func nearbyOpenInLibrary(title string) {
	openGameLibraryPanel()
	setGameLibraryQuery(title)
}

// nearbyOpenPeerSheet shows what one device shares.
func nearbyOpenPeerSheet(config *Config, p nearbyPeer) {
	var body strings.Builder
	if !p.Share {
		body.WriteString("This device does not share what it plays.")
	} else {
		if p.Playing != nil {
			body.WriteString("## Playing now\n" + nearbyPlayingText(*p.Playing) + "\n\n")
		}
		if len(p.Recent) > 0 {
			body.WriteString("## Played lately\n")
			for _, r := range p.Recent {
				body.WriteString("• " + nearbyPlayingText(r) + "\n")
			}
			body.WriteString("\n")
		}
		switch {
		case p.Common > 0:
			body.WriteString("Also on " + p.Name + ": " + strconv.Itoa(p.Common) + " of your games")
		case p.Common == 0:
			body.WriteString("None of your games are on " + p.Name)
		default:
			body.WriteString("Still reading what " + p.Name + " has")
		}
		if p.Games > 0 {
			body.WriteString(" (it has " + strconv.Itoa(p.Games) + ").")
		}
		body.WriteString("\n\nCounts come from a fingerprint of its library, not the list itself, so one in about a hundred may be a near miss.")
	}
	buttons := []tbxSheetButton{{label: "Close"}}
	if p.Playing != nil {
		title := p.Playing.Title
		buttons = append(buttons, tbxSheetButton{label: "Find in my library", primary: true, run: func() { nearbyOpenInLibrary(title) }})
	}
	sub := setOr(p.Model, "JukaHub")
	if p.Paired {
		sub += " · paired"
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    p.Name,
		subtitle: sub,
		icon:     "devices",
		body:     body.String(),
		buttons:  buttons,
		focus:    len(buttons) - 1,
	})
}
