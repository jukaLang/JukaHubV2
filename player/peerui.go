package main

// peerui.go — the Devices page, and what the three peer pages (Devices, Save
// sync, Nearby) share: the footer, the click and wheel routing main.go calls,
// and the pairing sheet.
//
// The pages are built on the tool screens' frame (tbxui.go): Home's backdrop,
// the shared page header, a focus list walked with the D-pad, glass rows and
// one details sheet. They draw their own footer (the same band and badges as
// every scene) so it can follow the focused row and the open sheet.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// ── Shared page plumbing ──────────────────────────────────────────────────

// peerDrawFooter is the shared footer band with the page's own hints: the
// same planner and badges renderFooter uses.
func peerDrawFooter(renderer *sdl.Renderer, config *Config, hints []FooterHint) {
	band := sceneFooterBand()
	drawGlassFooterBand(renderer, band)
	pad := scaleX(16, screenWidth)
	plan := planFooterHints(config, hints, band.W-2*pad)
	if plan.font == nil {
		return
	}
	x := band.X + band.W - pad - plan.width
	textY := band.Y + (band.H-plan.fontHeight)/2
	badgeY := band.Y + (band.H-plan.badgeH)/2
	for i, g := range plan.hints {
		labelX := x
		if plan.badgeW[i] > 0 {
			badge := sdl.Rect{X: x, Y: badgeY, W: plan.badgeW[i], H: plan.badgeH}
			labelX = badge.X + badge.W + plan.badgeGap
			drawGlassHintBadge(renderer, config, plan.font, badge, g.Button, g.Label, labelX, textY)
		} else {
			drawGlassText(renderer, config, plan.font, g.Label, ColorTextMuted(), labelX, textY)
		}
		x = labelX + plan.labelW[i] + plan.groupGap
	}
}

// peerEndFrame is tbxEndFrame with the page's own footer: the status bar, the
// footer (the sheet's keys while one is up) and the sheet over both.
func peerEndFrame(renderer *sdl.Renderer, config *Config, hints []FooterHint) {
	renderer.SetClipRect(nil)
	renderStatusBar(renderer, config)
	if tbxSheetVisible(config) {
		hints = tbxSheetHints()
	}
	peerDrawFooter(renderer, config, hints)
	tbxRenderSheet(renderer, config)
	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetClipRect(nil)
}

// peerScr moves a body rect (y=0 at the top of the content) onto the screen.
func peerScr(r sdl.Rect, top int32) sdl.Rect {
	r.Y += top
	return r
}

// peerScenesClick routes a click or tap on the three pages: the open sheet
// first, then the page's own rows. A miss is left for the bars.
func peerScenesClick(mx, my int32, config *Config) bool {
	name := peerCurrentScene(config)
	if name == "" {
		return false
	}
	if tbxSheetVisible(config) {
		tbxSheetMouse(mx, my)
		return true
	}
	switch name {
	case peerSceneName:
		return peerScreenClick(mx, my, config)
	case ssyncSceneName:
		return ssyncScreenClick(mx, my, config)
	case nearbySceneName:
		return nearbyScreenClick(mx, my, config)
	}
	return false
}

// peerScenesWheel scrolls a page or its open sheet.
func peerScenesWheel(config *Config, y int32) bool {
	name := peerCurrentScene(config)
	if name == "" || y == 0 {
		return false
	}
	if tbxSheetVisible(config) {
		if y > 0 {
			tbxSheetAction(tbxActUp)
		} else {
			tbxSheetAction(tbxActDown)
		}
		return true
	}
	var nav *tbxNav
	switch name {
	case peerSceneName:
		nav = &peerScreen.nav
	case ssyncSceneName:
		nav = &ssyncScreen.nav
	case nearbySceneName:
		nav = &nearbyScreen.nav
	}
	if nav == nil {
		return false
	}
	nav.scrollBy(-y * 48)
	return true
}

// peerFooterHints is the footer of the three pages: the open sheet's keys, or
// the page's own. ok is false for any other scene. It is meant for extras.go's
// footerHints, beside tbxFooterHints, so the controls legend lists them too.
func peerFooterHints(config *Config, name string) ([]FooterHint, bool) {
	switch name {
	case peerSceneName, ssyncSceneName, nearbySceneName:
	default:
		return nil, false
	}
	if tbxSheetVisible(config) {
		return tbxSheetHints(), true
	}
	switch name {
	case peerSceneName:
		return peerScreenHints(), true
	case ssyncSceneName:
		return ssyncScreenHints(), true
	}
	return nearbyScreenHints(), true
}

// peerAgo is a past moment as people say it.
func peerAgo(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + " min ago"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + " h ago"
	case d < 48*time.Hour:
		return "yesterday"
	}
	return strconv.Itoa(int(d.Hours()/24)) + " days ago"
}

// peerOffCard draws the "Nearby devices is off" explanation the three pages
// share, with the switch row above it.
func peerOffCard(renderer *sdl.Renderer, config *Config, rect sdl.Rect, body string) {
	UICard(renderer, rect, DefaultUICardStyle())
	tbxDrawEmpty(renderer, config, rect, "devices", "Nearby devices is off", body)
}

// peerPairedSummary is Settings → Network → Paired devices' read-out.
func peerPairedSummary(*Config) string {
	switch n := peerPairedCount(); n {
	case 0:
		return "None"
	case 1:
		return "1 device"
	default:
		return strconv.Itoa(n) + " devices"
	}
}

// ── Prompts and notices ───────────────────────────────────────────────────

// peerOpenRenamePrompt asks for this device's name (Settings and the Devices
// page share it).
func peerOpenRenamePrompt(config *Config) {
	tbxOpenPrompt("Device name", "How your other devices see this one", peerDeviceName(), func(text string) {
		if strings.TrimSpace(text) == "" {
			return
		}
		if err := peerSetDeviceName(text); err != nil {
			ShowToast("Could not rename: "+firstLine(err.Error()), ToastKindError)
			return
		}
		nearbyInvalidate()
		ShowToast("This device is now "+peerDeviceName(), ToastKindSuccess)
	})
}

// peerPairNotifyIncoming tells the user a pairing request has its code on
// screen. The Devices page and Home show it by themselves; anywhere else a
// toast says where to go.
func peerPairNotifyIncoming(token int64, name string) {
	if token == 0 {
		return
	}
	if peerCurrentScene(appConfig) == peerSceneName || homeLayoutActive {
		PlayActivateSound()
		return
	}
	ShowToast(name+" wants to pair — go Home to compare the code", ToastKindInfo)
}

// peerPairNotifyDone confirms a pairing.
func peerPairNotifyDone(name string) {
	ShowToast("Paired with "+name+" — sync saves and continue games between you", ToastKindSuccess)
}

// peerPairCancelView cancels the session a sheet or card shows.
func peerPairCancelView(v peerPairView) {
	if v.initiator {
		peerPairAnswerOutgoing(false)
		return
	}
	peerPairAnswerIncoming(false)
}

// peerPairConfirmView confirms the code for the session a sheet or card shows.
func peerPairConfirmView(v peerPairView) {
	if v.initiator {
		peerPairAnswerOutgoing(true)
		return
	}
	peerPairAnswerIncoming(true)
}

// peerPairSheetSpec is the pairing sheet for one stage of a session.
func peerPairSheetSpec(v peerPairView) tbxSheetSpec {
	spec := tbxSheetSpec{
		token: "pair-" + strconv.FormatInt(v.token, 10),
		icon:  "devices",
		title: "Pair with " + v.name,
	}
	cancel := tbxSheetButton{label: "Cancel", run: func() { peerPairCancelView(v) }}
	switch {
	case v.stage == peerPairConnecting || v.stage == peerPairHello:
		spec.subtitle = "Asking " + v.name + "…"
		spec.body = "Keep both devices on the same Wi-Fi. A code appears on both screens in a moment."
		spec.buttons = []tbxSheetButton{cancel}
	case v.localOK:
		spec.subtitle = "Code " + peerFormatCode(v.code)
		spec.body = "Waiting for " + v.name + " — choose Codes match there too."
		spec.buttons = []tbxSheetButton{cancel}
	default:
		spec.subtitle = "Code " + peerFormatCode(v.code)
		spec.body = "## Code\n" + peerFormatCode(v.code) + "\n\n" +
			"Check that " + v.name + " shows exactly the same code, then choose Codes match on both devices. " +
			"If the codes differ, cancel: something else on this network answered."
		if !v.initiator {
			spec.body += "\n\nOnly confirm if you started pairing on " + v.name + "."
		}
		spec.buttons = []tbxSheetButton{
			cancel,
			{label: "Codes match", primary: true, run: func() { peerPairConfirmView(v) }},
		}
		spec.focus = 1
	}
	return spec
}

// ── Devices page ──────────────────────────────────────────────────────────

// peerRowModel is one prepared row.
type peerRowModel struct {
	id   string
	spec tbxRowSpec
}

// peerScreenModel is what the Devices page draws, rebuilt a few times a
// second.
type peerScreenModel struct {
	builtAt  time.Time
	header   UIPageHeader
	enabled  bool
	lan      string
	selfName string
	selfSub  string
	clone    string
	paired   []peerRowModel
	nearby   []peerRowModel
	scans    int
}

type peerScreenState struct {
	visit tbxVisit
	nav   tbxNav
	model peerScreenModel
	// sheetSig names the pairing sheet that is up, so it opens once per stage.
	sheetSig string
}

var peerScreen peerScreenState

// peerScreenScans counts the page's scans (0 means the first is running).
var peerScreenScans atomic.Int32

// peerLastSync caches when each paired device last synced, read off the main
// loop by the page's scan task.
var peerLastSync struct {
	mu sync.Mutex
	m  map[string]time.Time
}

type peerDevicesScene struct{ sceneBase }

func (peerDevicesScene) render(r *sdl.Renderer, c *Config) bool {
	peerRenderScreen(r, c)
	return true
}

func (peerDevicesScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), peerScreenAction)
}

func (peerDevicesScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), peerScreenAction)
}

func init() { registerScene(peerDevicesScene{sceneBase{name: peerSceneName}}) }

// peerScreenEnter runs once per visit: a fresh list and the scan that fills
// it, tied to this visit so leaving the page stops it.
func peerScreenEnter() {
	peerScreenScans.Store(0)
	peerScreen.nav.scroll = 0
	peerScreen.nav.focus = ""
	peerScreen.model.builtAt = time.Time{}
	peerScreen.sheetSig = ""
	runSceneTask(peerScreenScanLoop)
}

// peerScreenScanLoop scans every few seconds while the page is up.
func peerScreenScanLoop(ctx context.Context) {
	times := map[string]time.Time{}
	for _, p := range peerPairedList() {
		times[p.ID] = ssyncLastSync(p.ID)
	}
	peerLastSync.mu.Lock()
	peerLastSync.m = times
	peerLastSync.mu.Unlock()
	for {
		if ctx.Err() != nil {
			return
		}
		if peerEnabledFlag.Load() && partyLANAddress() != "" {
			devs, _ := peerDiscoverOnce(ctx, peerScanWait)
			if ctx.Err() != nil {
				return
			}
			peerMergeSeen(devs, time.Now())
		}
		peerScreenScans.Add(1)
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

// peerLastSyncOf is when a paired device last synced, as the page last read.
func peerLastSyncOf(id string) time.Time {
	peerLastSync.mu.Lock()
	defer peerLastSync.mu.Unlock()
	return peerLastSync.m[id]
}

// peerBuildModel prepares every string the page draws.
func peerBuildModel(now time.Time) peerScreenModel {
	m := peerScreenModel{
		builtAt:  now,
		enabled:  peerEnabledFlag.Load(),
		lan:      partyLANAddress(),
		selfName: peerDeviceName(),
		scans:    int(peerScreenScans.Load()),
	}
	status, ink := "Visible on this Wi-Fi", ToastSuccessInk()
	switch {
	case !m.enabled:
		status, ink = "Off", ColorTextTertiary()
	case m.lan == "":
		status, ink = "Needs Wi-Fi", ToastWarnInk()
	case !peerRunning() && peerServerError() != "":
		status, ink = "Could not start", ToastErrorInk()
	case !peerRunning():
		status, ink = "Starting…", ColorTextTertiary()
	}
	m.header = tbxHeader("Devices", "Pair your JukaHubs to sync saves and continue where you left off", "devices", status, ink)
	if !m.enabled {
		return m
	}
	sub := peerModelName()
	if peerIdentityExists() {
		sub += " · ID " + peerShortID(peerSelfID())
	}
	if port := peerListenPortNow.Load(); m.lan != "" && port > 0 {
		sub += " · " + m.lan + ":" + strconv.Itoa(int(port))
	}
	m.selfSub = sub
	m.clone = peerCloneAddr()

	paired := map[string]bool{}
	for _, p := range peerPairedList() {
		paired[p.ID] = true
		model := setOr(p.Model, "JukaHub")
		spec := tbxRowSpec{icon: "devices", title: p.Name}
		switch {
		case peerIsUnpairedThere(p.ID):
			spec.subtitle = "It forgot this device — forget it here too, then pair again"
			spec.pill, spec.tone = "Pair again", tbxToneWarn
		case peerOnline(p.ID):
			spec.subtitle = model + " · on this Wi-Fi now"
			spec.pill, spec.tone = "Online", tbxToneGood
		case !p.LastSeen.IsZero():
			spec.subtitle = model + " · last seen " + peerAgo(now, p.LastSeen)
			spec.pill, spec.tone = "Away", tbxToneNeutral
		default:
			spec.subtitle = model + " · not seen yet"
			spec.pill, spec.tone = "Away", tbxToneNeutral
		}
		if last := peerLastSyncOf(p.ID); !last.IsZero() {
			spec.note = "synced " + peerAgo(now, last)
		}
		m.paired = append(m.paired, peerRowModel{id: "pair:" + p.ID, spec: spec})
	}
	for _, d := range peerSeenList() {
		if paired[d.ID] {
			continue
		}
		m.nearby = append(m.nearby, peerRowModel{
			id: "near:" + d.ID,
			spec: tbxRowSpec{
				icon:     "devices",
				title:    d.Name,
				subtitle: setOr(d.Model, "JukaHub") + " · " + d.IP,
				pill:     "Pair",
				tone:     tbxToneAccent,
			},
		})
	}
	return m
}

// peerRenderScreen draws the Devices page.
func peerRenderScreen(renderer *sdl.Renderer, config *Config) {
	if peerScreen.visit.begin() {
		peerScreenEnter()
	}
	now := time.Now()
	if now.Sub(peerScreen.model.builtAt) > 300*time.Millisecond {
		peerScreen.model = peerBuildModel(now)
	}
	peerSyncPairSheet(config)
	m := &peerScreen.model
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, m.header)
	body := frame.bodyRect()
	nav := &peerScreen.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	peerRenderBody(renderer, config, fonts, frame, m)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	peerEndFrame(renderer, config, peerScreenHints())
}

func peerRenderBody(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, m *peerScreenModel) {
	nav := &peerScreen.nav
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

	toggle := tbxRowSpec{icon: "wifi", title: "Nearby devices", pill: "Off", tone: tbxToneNeutral,
		subtitle: "Off — this device is invisible on the network and listens for nothing"}
	if m.enabled {
		toggle.pill, toggle.tone = "On", tbxToneGood
		toggle.subtitle = "Find your other JukaHubs on this Wi-Fi · nothing is shared until you pair"
	}
	addRow("toggle", toggle)
	if !m.enabled {
		card := sdl.Rect{X: x, Y: y, W: w, H: 170}
		peerOffCard(renderer, config, peerScr(card, top),
			"Turn it on to pair this device with your other JukaHubs: sync saves, continue a game where you left off and see what is playing nearby. No account, no cloud — only your Wi-Fi.")
		nav.extend(y + card.H + 8)
		return
	}

	addRow("self", tbxRowSpec{icon: "user", title: m.selfName, subtitle: m.selfSub, pill: "Rename", tone: tbxToneAccent})
	if m.clone != "" {
		addRow("clone", tbxRowSpec{
			icon:     "warning",
			title:    "Another device uses this device's identity",
			subtitle: "It answers at " + m.clone + " — a copied JukaHub folder carries its key along",
			pill:     "Fix",
			tone:     tbxToneWarn,
		})
	}

	y += 14
	tbxDrawSection(renderer, config, "Paired", "users", x, top+y, w)
	y += tbxSectionHeight
	if len(m.paired) == 0 {
		emptyCard("users", "No paired devices yet", "Pair one from the list below. Both devices need Nearby devices on and the same Wi-Fi.")
	}
	for _, r := range m.paired {
		addRow(r.id, r.spec)
	}

	y += 14
	tbxDrawSection(renderer, config, "On this Wi-Fi", "radar", x, top+y, w)
	y += tbxSectionHeight
	if len(m.nearby) == 0 {
		switch {
		case m.lan == "":
			emptyCard("wifi-off", "Not on Wi-Fi", "JukaHubs on your network appear here once you are connected.")
		case m.scans < 2:
			emptyCard("radar", "Looking for JukaHubs…", "Open Settings → Network → Nearby devices on the other device too.")
		default:
			emptyCard("radar", "No other JukaHub found", "Turn on Nearby devices on the other device and join the same Wi-Fi.")
		}
	}
	for _, r := range m.nearby {
		addRow(r.id, r.spec)
	}
	if IsWindows() {
		addRow("firewall", tbxRowSpec{
			icon:     "shield",
			title:    "Not showing up on your other devices?",
			subtitle: "Windows Defender Firewall may be blocking JukaHub on this network",
			pill:     "How to fix",
			tone:     tbxToneNeutral,
		})
	}
	nav.extend(y)
}

// peerSyncPairSheet opens the pairing sheet when a session reaches a new
// stage, and treats putting the sheet away (B, a tap outside) as a cancel.
func peerSyncPairSheet(config *Config) {
	in, out := peerPairViews()
	var v peerPairView
	sig := ""
	switch {
	case out.live:
		v = out
		sig = fmt.Sprintf("out:%d:%d:%t", out.token, out.stage, out.localOK)
	case in.live && in.stage == peerPairCompare:
		v = in
		sig = fmt.Sprintf("in:%d:%d:%t", in.token, in.stage, in.localOK)
	}
	if sig == peerScreen.sheetSig {
		if sig != "" && !tbxSheetVisible(config) {
			peerScreen.sheetSig = ""
			peerPairCancelView(v)
		}
		return
	}
	peerScreen.sheetSig = sig
	if sig == "" {
		if tbxSheetVisible(config) && strings.HasPrefix(tbxSheet.spec.token, "pair-") {
			tbxCloseSheet()
		}
		return
	}
	tbxOpenSheet(config, peerPairSheetSpec(v))
}

// peerScreenAction handles one action on the page (the sheet has had its
// turn). Up at the top is left unhandled so the top bar can take it.
func peerScreenAction(config *Config, act tbxAction) bool {
	nav := &peerScreen.nav
	switch act {
	case tbxActConfirm:
		peerScreenActivate(config, nav.focus)
		return true
	case tbxActX, tbxActY:
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(nav, act)
}

// peerScreenClick handles a click on the page's rows.
func peerScreenClick(mx, my int32, config *Config) bool {
	it, ok := peerScreen.nav.hit(mx, my)
	if !ok {
		return false
	}
	peerScreen.nav.focus = it.id
	peerScreenActivate(config, it.id)
	return true
}

// peerScreenHints is the footer: the focused row's own action first.
func peerScreenHints() []FooterHint {
	id := peerScreen.nav.focus
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}}
	switch {
	case id == "toggle":
		label := "Turn on"
		if peerEnabledFlag.Load() {
			label = "Turn off"
		}
		hints = append(hints, FooterHint{Button: "A", Label: label})
	case id == "self":
		hints = append(hints, FooterHint{Button: "A", Label: "Rename"})
	case strings.HasPrefix(id, "pair:"):
		hints = append(hints, FooterHint{Button: "A", Label: "Options"})
	case strings.HasPrefix(id, "near:"):
		hints = append(hints, FooterHint{Button: "A", Label: "Pair"})
	case id == "clone", id == "firewall":
		hints = append(hints, FooterHint{Button: "A", Label: "Open"})
	}
	return append(hints, FooterHint{Button: "B", Label: "Back"})
}

// peerScreenActivate runs the focused row's action.
func peerScreenActivate(config *Config, id string) {
	switch {
	case id == "toggle":
		PlayToggleSound()
		peerSettingSetEnabled(config, !peerSettingEnabled(config))
		peerScreen.model.builtAt = time.Time{}
	case id == "self":
		PlayActivateSound()
		peerOpenRenamePrompt(config)
	case id == "clone":
		PlayActivateSound()
		peerOpenCloneSheet(config)
	case id == "firewall":
		PlayActivateSound()
		peerOpenFirewallSheet(config)
	case strings.HasPrefix(id, "pair:"):
		peerOpenPairedSheet(config, strings.TrimPrefix(id, "pair:"))
	case strings.HasPrefix(id, "near:"):
		dev, ok := peerSeenDeviceByID(strings.TrimPrefix(id, "near:"))
		if !ok {
			ShowToast("That device stopped answering — try again in a moment", ToastKindWarning)
			return
		}
		PlayActivateSound()
		peerPairStart(dev)
	}
}

// peerOpenPairedSheet shows one paired device and what can be done with it.
func peerOpenPairedSheet(config *Config, id string) {
	rec, ok := peerPairedRecord(id)
	if !ok {
		return
	}
	PlayActivateSound()
	now := time.Now()
	status := "Not on this Wi-Fi right now"
	if peerOnline(id) {
		status = "On this Wi-Fi now"
	} else if !rec.LastSeen.IsZero() {
		status = "Last seen " + peerAgo(now, rec.LastSeen)
	}
	body := "## Device\n" + setOr(rec.Model, "JukaHub") + " · ID " + peerShortID(rec.ID) + "\n" +
		"Paired " + rec.PairedAt.Local().Format("Jan 2, 2006") + "\n\n" +
		"## What you can do\n" +
		"• Sync saves: make your saves the same on both devices\n" +
		"• Continue: what you play here is offered on its Home, and the other way round\n" +
		"• Send to " + rec.Name + ": from the Start menu, at any time"
	if last := peerLastSyncOf(id); !last.IsZero() {
		body += "\n\nLast save sync: " + peerAgo(now, last)
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    rec.Name,
		subtitle: status,
		icon:     "devices",
		body:     body,
		buttons: []tbxSheetButton{
			{label: "Forget", run: func() { peerConfirmForget(config, rec) }},
			{label: "Sync saves", primary: true, run: func() { ssyncOpenWith(config, rec.ID) }},
		},
		focus: 1,
	})
}

// peerConfirmForget asks before unpairing.
func peerConfirmForget(config *Config, rec peerRecord) {
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Forget " + rec.Name + "?",
		subtitle: "Unpair",
		icon:     "devices",
		body: "This device stops trusting " + rec.Name + ": no more save sync or handoff between the two until you pair again. " +
			rec.Name + " is told to forget this device too when it can be reached.",
		buttons: []tbxSheetButton{
			{label: "Keep"},
			{label: "Forget", primary: true, run: func() {
				id := rec.ID
				go peerUnpairRemote(id)
				peerRemovePaired(id)
				peerScreen.model.builtAt = time.Time{}
				ShowToast(rec.Name+" forgotten", ToastKindInfo)
			}},
		},
	})
}

// peerOpenCloneSheet explains a copied identity and offers a new one.
func peerOpenCloneSheet(config *Config) {
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Two devices, one identity",
		subtitle: "Another device answers as this one",
		icon:     "warning",
		body: "A device at " + peerCloneAddr() + " answers with this device's own key. That happens when a JukaHub " +
			"folder is copied from one handheld to another: the key in data/peer/identity.json comes along.\n\n" +
			"Give this device a new identity. Its paired devices are forgotten; pair them again afterwards. " +
			"Do the same on the other device if it was the copy.",
		buttons: []tbxSheetButton{
			{label: "Cancel"},
			{label: "New identity", primary: true, run: func() {
				if err := peerResetIdentity(); err != nil {
					ShowToast("Could not make a new identity: "+firstLine(err.Error()), ToastKindError)
					return
				}
				peerScreen.model.builtAt = time.Time{}
				ShowToast("This device has a new identity — pair your devices again", ToastKindSuccess)
			}},
		},
	})
}

// peerOpenFirewallSheet is the Windows firewall hint.
func peerOpenFirewallSheet(config *Config) {
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Let your devices find this PC",
		subtitle: "Windows Defender Firewall",
		icon:     "shield",
		body: "Windows may ask whether JukaHub can use the network the first time Nearby devices is on. " +
			"If that was answered with Cancel, the other devices cannot see this PC.\n\n" +
			"• Open Windows Security → Firewall & network protection → Allow an app through firewall\n" +
			"• Choose Change settings, find JukaHub and tick Private\n" +
			"• Check that this Wi-Fi is a Private network (Settings → Network & internet)\n\n" +
			"JukaHub only uses TCP port 8322 and UDP port 8321, and only while Nearby devices is on.",
		buttons: []tbxSheetButton{{label: "Done", primary: true}},
	})
}
