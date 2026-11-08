package main

// ssyncui.go — the Save sync page (Tools → Save sync).
//
// Three views on one page:
//
//   - the start: the paired devices to sync with, "Export encrypted saves"
//     and "Import a saves file", and the result of the last job;
//   - a comparison with one device: how many saves are the same, what comes
//     here, what goes there and what changed on both (both kept), file by file,
//     with "Sync now" on top;
//   - the saves files found on this device, for an import.
//
// Comparing, syncing, exporting and importing run on a goroutine (they hash
// and copy files) and report progress through ssyncWork; a job keeps running
// if the page is left, and says how it ended in a toast. B while a job runs
// offers to stop it.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

type ssyncMode int

const (
	ssyncModeHome ssyncMode = iota
	ssyncModeCompare
	ssyncModeImport
)

// ssyncWork is the job in progress, written by its goroutine.
var ssyncWork struct {
	mu     sync.Mutex
	busy   bool
	label  string
	detail string
	done   int
	total  int
	cancel context.CancelFunc
}

func init() { bootOnShutdown(ssyncWorkCancel) }

// ssyncWorkStart claims the job slot; false when a job is already running.
func ssyncWorkStart(label string) (context.Context, bool) {
	ssyncWork.mu.Lock()
	defer ssyncWork.mu.Unlock()
	if ssyncWork.busy {
		return nil, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	ssyncWork.busy, ssyncWork.label, ssyncWork.detail = true, label, ""
	ssyncWork.done, ssyncWork.total, ssyncWork.cancel = 0, 0, cancel
	return ctx, true
}

// ssyncWorkProgress is a job's progress callback.
func ssyncWorkProgress(done, total int, current string) {
	ssyncWork.mu.Lock()
	ssyncWork.done, ssyncWork.total = done, total
	if current != "" {
		ssyncWork.detail = path.Base(current)
	}
	ssyncWork.mu.Unlock()
}

// ssyncWorkEnd frees the job slot.
func ssyncWorkEnd() {
	ssyncWork.mu.Lock()
	if ssyncWork.cancel != nil {
		ssyncWork.cancel()
	}
	ssyncWork.busy, ssyncWork.cancel = false, nil
	ssyncWork.mu.Unlock()
}

// ssyncWorkCancel stops the running job (B, or the app closing).
func ssyncWorkCancel() {
	ssyncWork.mu.Lock()
	cancel := ssyncWork.cancel
	ssyncWork.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// ssyncWorkView is the job as the page draws it.
type ssyncWorkView struct {
	busy   bool
	label  string
	detail string
	done   int
	total  int
}

func ssyncWorkSnapshot() ssyncWorkView {
	ssyncWork.mu.Lock()
	defer ssyncWork.mu.Unlock()
	return ssyncWorkView{busy: ssyncWork.busy, label: ssyncWork.label, detail: ssyncWork.detail, done: ssyncWork.done, total: ssyncWork.total}
}

// ssyncScreenModel is what the start view draws, rebuilt a few times a second.
type ssyncScreenModel struct {
	builtAt   time.Time
	header    UIPageHeader
	work      ssyncWorkView
	enabled   bool
	devices   []peerRowModel
	exportSub string
	importSub string
}

type ssyncScreenState struct {
	visit tbxVisit
	nav   tbxNav
	mode  ssyncMode
	model ssyncScreenModel
	// cmp is the comparison on screen, with its rows built once.
	cmp        *ssyncComparison
	cmpRows    []peerRowModel
	cmpSummary string
	// bundles are the saves files found for an import.
	bundles     []ssyncBundleInfo
	bundlesRead bool
	// resultLine is the last job's outcome.
	resultLine string
	// openWith is a device to compare with as soon as the page opens.
	openWith string
}

var ssyncScreen ssyncScreenState

type ssyncSceneModule struct{ sceneBase }

func (ssyncSceneModule) render(r *sdl.Renderer, c *Config) bool {
	ssyncRenderScreen(r, c)
	return true
}

func (ssyncSceneModule) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), ssyncScreenAction)
}

func (ssyncSceneModule) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), ssyncScreenAction)
}

func init() { registerScene(ssyncSceneModule{sceneBase{name: ssyncSceneName}}) }

// ssyncOpenScreen opens Save sync (the Tools hub tile).
func ssyncOpenScreen(config *Config) {
	peerOpenScene(config, ssyncSceneName)
}

// ssyncHubTile is Save sync's tile on the Tools hub. It needs nothing: the
// export and import work with no network at all.
func ssyncHubTile() toolsHubTile {
	t := toolsHubTile{
		id: "ssync", label: "Save sync", icon: "sync",
		value: "Saves the same on all your JukaHubs — no cloud",
	}
	switch {
	case ssyncWorkSnapshot().busy:
		t.value, t.tone = "Working…", toolsHubToneBusy
	case peerEnabledFlag.Load():
		if n := peerPairedCount(); n == 1 {
			t.value = "1 paired device · or export a file"
		} else if n > 1 {
			t.value = strconv.Itoa(n) + " paired devices · or export a file"
		}
	}
	return t
}

// ssyncOpenWith opens Save sync and compares with one paired device at once
// (the Devices page's "Sync saves").
func ssyncOpenWith(config *Config, peerID string) {
	ssyncScreen.openWith = peerID
	if peerCurrentScene(config) == ssyncSceneName {
		ssyncStartCompare(peerID)
		ssyncScreen.openWith = ""
		return
	}
	peerOpenScene(config, ssyncSceneName)
}

// ssyncScreenEnter runs once per visit.
func ssyncScreenEnter() {
	ssyncScreen.mode = ssyncModeHome
	ssyncScreen.nav.scroll = 0
	ssyncScreen.nav.focus = ""
	ssyncScreen.model.builtAt = time.Time{}
	ssyncScreen.bundlesRead = false
	if id := ssyncScreen.openWith; id != "" {
		ssyncScreen.openWith = ""
		ssyncStartCompare(id)
	}
	// Peers' addresses are refreshed for the device rows' "online" state.
	if peerEnabledFlag.Load() {
		runSceneTask(peerScanAndMerge)
	}
}

// ── Model ────────────────────────────────────────────────────────────────

func ssyncBuildModel(now time.Time) ssyncScreenModel {
	m := ssyncScreenModel{builtAt: now, work: ssyncWorkSnapshot(), enabled: peerEnabledFlag.Load()}
	subtitle := "Your saves, the same on every JukaHub — over Wi-Fi or with one file"
	status, ink := "", ColorTextTertiary()
	switch {
	case m.work.busy:
		status, ink = m.work.label, glassAccent()
	case ssyncScreen.mode == ssyncModeCompare && ssyncScreen.cmp != nil:
		subtitle = "Compared with " + ssyncScreen.cmp.PeerName + " at " + ssyncScreen.cmp.At.Format("15:04")
	case ssyncScreen.mode == ssyncModeImport:
		subtitle = "Choose a saves file to import"
	}
	m.header = tbxHeader("Save sync", subtitle, "sync", status, ink)

	for _, p := range peerPairedList() {
		spec := tbxRowSpec{icon: "devices", title: p.Name, pill: "Compare", tone: tbxToneAccent}
		if peerOnline(p.ID) {
			spec.subtitle = "On this Wi-Fi now"
		} else {
			spec.subtitle = "Not seen on this Wi-Fi lately — it is looked for when you press A"
			spec.tone = tbxToneNeutral
		}
		if last := peerLastSyncOf(p.ID); !last.IsZero() {
			spec.subtitle += " · synced " + peerAgo(now, last)
		}
		m.devices = append(m.devices, peerRowModel{id: "dev:" + p.ID, spec: spec})
	}
	dir := ssyncExportDir()
	m.exportSub = "One .jukasaves file in " + dir + " — carry it on a card or over SFTP"
	switch {
	case !ssyncScreen.bundlesRead:
		m.importSub = "Looks in " + dir + ", the card's top folder and Downloads"
	case len(ssyncScreen.bundles) == 0:
		m.importSub = "No saves file found yet — put one in " + dir
	case len(ssyncScreen.bundles) == 1:
		m.importSub = "1 saves file found"
	default:
		m.importSub = strconv.Itoa(len(ssyncScreen.bundles)) + " saves files found"
	}
	return m
}

// ssyncResultText says what a job did.
func ssyncResultText(res ssyncResult) string {
	var parts []string
	if res.Pulled > 0 {
		parts = append(parts, fmt.Sprintf("%d updated here", res.Pulled))
	}
	if res.Pushed > 0 {
		parts = append(parts, fmt.Sprintf("%d sent", res.Pushed))
	}
	if res.Conflicts > 0 {
		parts = append(parts, fmt.Sprintf("%d changed on both, both kept", res.Conflicts))
	}
	if res.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", res.Failed))
	}
	if len(parts) == 0 {
		return "Already the same — nothing to copy"
	}
	return strings.Join(parts, " · ")
}

// ssyncBuildCompareRows prepares the comparison's rows once.
func ssyncBuildCompareRows(cmp *ssyncComparison) ([]peerRowModel, string) {
	names := map[string]string{}
	for _, r := range cmp.Remote.Roots {
		names[r.Key] = r.Name
	}
	for _, r := range cmp.Local.Roots {
		names[r.Key] = r.Name
	}
	plan := cmp.Plan
	summary := fmt.Sprintf("%d the same · %d to get · %d to send · %d changed on both",
		len(plan.Same), plan.Pull, plan.Push, plan.Conflict)
	if len(plan.Items) == 0 {
		summary = fmt.Sprintf("All %d saves are already the same on both devices", len(plan.Same))
	}
	rows := make([]peerRowModel, 0, len(plan.Items))
	for i, it := range plan.Items {
		if i >= 300 {
			break
		}
		spec := tbxRowSpec{title: path.Base(it.Path), note: ""}
		folder := setOr(names[it.Root], it.Root)
		switch it.Action {
		case ssyncPull:
			spec.icon, spec.pill, spec.tone = "download", "Get", tbxToneAccent
			spec.subtitle = folder + " · " + it.Reason + " · " + humanBytes(it.Remote.Size)
		case ssyncPush:
			spec.icon, spec.pill, spec.tone = "upload", "Send", tbxToneGood
			spec.subtitle = folder + " · " + it.Reason + " · " + humanBytes(it.Local.Size)
		case ssyncConflict:
			spec.icon, spec.pill, spec.tone = "copy", "Keep both", tbxToneWarn
			spec.subtitle = folder + " · changed on both · the other copy is kept as " + path.Base(it.CopyPath)
		}
		rows = append(rows, peerRowModel{id: "item:" + strconv.Itoa(i), spec: spec})
	}
	return rows, summary
}

// ── Render ───────────────────────────────────────────────────────────────

func ssyncRenderScreen(renderer *sdl.Renderer, config *Config) {
	if ssyncScreen.visit.begin() {
		ssyncScreenEnter()
	}
	now := time.Now()
	if now.Sub(ssyncScreen.model.builtAt) > 250*time.Millisecond {
		ssyncScreen.model = ssyncBuildModel(now)
	}
	m := &ssyncScreen.model
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, m.header)
	body := frame.bodyRect()
	nav := &ssyncScreen.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	top := frame.bodyTop - nav.scroll
	y := int32(0)
	if m.work.busy {
		y = ssyncDrawProgress(renderer, config, fonts, frame, top, m.work)
	} else if ssyncScreen.resultLine != "" && ssyncScreen.mode == ssyncModeHome {
		card := sdl.Rect{X: frame.x, Y: y, W: frame.w, H: fonts.bodyH + 28}
		UICard(renderer, peerScr(card, top), DefaultUICardStyle())
		line := ellipsize(fonts.body, ssyncScreen.resultLine, card.W-32)
		renderText(renderer, config, fonts.body, line, ColorTextSecondary(), card.X+16, top+card.Y+14)
		y += card.H + 14
	}
	switch ssyncScreen.mode {
	case ssyncModeCompare:
		ssyncRenderCompare(renderer, config, fonts, frame, top, y)
	case ssyncModeImport:
		ssyncRenderImport(renderer, config, fonts, frame, top, y)
	default:
		ssyncRenderHome(renderer, config, fonts, frame, top, y, m)
	}
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	peerEndFrame(renderer, config, ssyncScreenHints())
}

// ssyncDrawProgress draws the running job's card and returns the y below it.
func ssyncDrawProgress(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, top int32, work ssyncWorkView) int32 {
	card := sdl.Rect{X: frame.x, Y: 0, W: frame.w, H: fonts.titleH + fonts.smallH + 52}
	UICard(renderer, peerScr(card, top), DefaultUICardStyle())
	textW := card.W - 32
	renderText(renderer, config, fonts.title, ellipsize(fonts.title, work.label, textW), ColorTextPrimary(), card.X+16, top+card.Y+12)
	detail := "Working…"
	if work.total > 0 {
		detail = fmt.Sprintf("%d of %d", work.done, work.total)
		if work.detail != "" {
			detail += " · " + work.detail
		}
	}
	detail += " · B stops"
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, detail, textW), ColorTextMuted(), card.X+16, top+card.Y+16+fonts.titleH)
	bar := sdl.Rect{X: card.X + 16, Y: top + card.Y + card.H - 14, W: card.W - 32, H: 6}
	fillRoundedRect(renderer, bar.X, bar.Y, bar.W, bar.H, 3, glassA(30))
	if work.total > 0 {
		fill := int32(int64(bar.W) * int64(work.done) / int64(work.total))
		if fill > 0 {
			fillRoundedRect(renderer, bar.X, bar.Y, fill, bar.H, 3, glassAccent())
		}
	}
	return card.H + 14
}

func ssyncRenderHome(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, top, y int32, m *ssyncScreenModel) {
	nav := &ssyncScreen.nav
	x, w := frame.x, frame.w
	rowH := tbxRowHeight(fonts)
	row := 0
	addRow := func(id string, spec tbxRowSpec) {
		rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		tbxDrawRow(renderer, config, fonts, peerScr(rect, top), spec, nav.focused(id))
		nav.add(id, rect, row, false)
		row++
		y += rowH + 8
	}

	tbxDrawSection(renderer, config, "With a paired device", "sync", x, top+y, w)
	y += tbxSectionHeight
	switch {
	case !m.enabled:
		addRow("enable", tbxRowSpec{icon: "wifi", title: "Nearby devices is off", pill: "Turn on", tone: tbxToneAccent,
			subtitle: "Turn it on to sync with your other JukaHubs over Wi-Fi"})
	case len(m.devices) == 0:
		addRow("pair", tbxRowSpec{icon: "devices", title: "Pair a device first", pill: "Open", tone: tbxToneAccent,
			subtitle: "Settings → Network → Paired devices: compare a code on both screens once"})
	default:
		for _, d := range m.devices {
			addRow(d.id, d.spec)
		}
	}

	y += 14
	tbxDrawSection(renderer, config, "Without a network", "upload", x, top+y, w)
	y += tbxSectionHeight
	addRow("export", tbxRowSpec{icon: "upload", title: "Export encrypted saves", subtitle: m.exportSub, pill: "Export", tone: tbxToneNeutral})
	addRow("import", tbxRowSpec{icon: "download", title: "Import a saves file", subtitle: m.importSub, pill: "Import", tone: tbxToneNeutral})

	y += 14
	addRow("manager", tbxRowSpec{icon: "backup", title: "Save Manager",
		subtitle: "Every sync and import snapshots the saves it replaces first — restore one here"})
	nav.extend(y)
}

func ssyncRenderCompare(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, top, y int32) {
	nav := &ssyncScreen.nav
	x, w := frame.x, frame.w
	rowH := tbxRowHeight(fonts)
	row := 0
	cmp := ssyncScreen.cmp
	if cmp == nil {
		nav.extend(y)
		return
	}
	card := sdl.Rect{X: x, Y: y, W: w, H: fonts.bodyH + 28}
	UICard(renderer, peerScr(card, top), DefaultUICardStyle())
	renderText(renderer, config, fonts.body, ellipsize(fonts.body, ssyncScreen.cmpSummary, card.W-32), ColorTextSecondary(), card.X+16, top+card.Y+14)
	y += card.H + 14

	run := tbxRowSpec{icon: "sync", title: "Sync now", pill: "Sync", tone: tbxToneAccent,
		subtitle: "Newest wins; both copies are kept when both changed; a safety snapshot is taken first"}
	disabled := len(cmp.Plan.Items) == 0
	if disabled {
		run.subtitle, run.pill, run.tone, run.dim = "Nothing to copy", "", tbxToneNeutral, true
	}
	rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
	tbxDrawRow(renderer, config, fonts, peerScr(rect, top), run, nav.focused("run"))
	nav.add("run", rect, row, disabled)
	row++
	y += rowH + 8
	if len(cmp.Plan.Skipped) > 0 {
		note := fmt.Sprintf("%d save(s) left out: names or folders the other device cannot hold", len(cmp.Plan.Skipped))
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, note, w), ToastWarnInk(), x, top+y)
		y += fonts.smallH + 10
	}
	for _, r := range ssyncScreen.cmpRows {
		rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		// Rows outside the viewport are laid out but not drawn.
		if top+y+rowH >= frame.bodyTop && top+y <= frame.bodyBot {
			tbxDrawRow(renderer, config, fonts, peerScr(rect, top), r.spec, nav.focused(r.id))
		}
		nav.add(r.id, rect, row, false)
		row++
		y += rowH + 8
	}
	if len(cmp.Plan.Items) > len(ssyncScreen.cmpRows) {
		more := fmt.Sprintf("…and %d more", len(cmp.Plan.Items)-len(ssyncScreen.cmpRows))
		renderText(renderer, config, fonts.small, more, ColorTextTertiary(), x, top+y)
		y += fonts.smallH + 10
	}
	nav.extend(y)
}

func ssyncRenderImport(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, top, y int32) {
	nav := &ssyncScreen.nav
	x, w := frame.x, frame.w
	rowH := tbxRowHeight(fonts)
	if !ssyncScreen.bundlesRead {
		card := sdl.Rect{X: x, Y: y, W: w, H: 132}
		UICard(renderer, peerScr(card, top), DefaultUICardStyle())
		tbxDrawEmpty(renderer, config, peerScr(card, top), "search", "Looking for saves files…", "")
		nav.extend(y + card.H)
		return
	}
	if len(ssyncScreen.bundles) == 0 {
		card := sdl.Rect{X: x, Y: y, W: w, H: 150}
		UICard(renderer, peerScr(card, top), DefaultUICardStyle())
		tbxDrawEmpty(renderer, config, peerScr(card, top), "download", "No saves file found",
			"Copy a .jukasaves file into "+ssyncExportDir()+" on this device (card reader or SFTP), then open Import again.")
		nav.extend(y + card.H)
		return
	}
	for i, b := range ssyncScreen.bundles {
		when := b.Mod
		if !b.Created.IsZero() {
			when = b.Created
		}
		spec := tbxRowSpec{
			icon:     "download",
			title:    b.Name,
			subtitle: when.Local().Format("Jan 2, 15:04") + " · " + humanBytes(b.Size) + " · " + filepath.Base(filepath.Dir(b.Path)),
			pill:     "Open",
			tone:     tbxToneAccent,
		}
		id := "file:" + strconv.Itoa(i)
		rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		tbxDrawRow(renderer, config, fonts, peerScr(rect, top), spec, nav.focused(id))
		nav.add(id, rect, i, false)
		y += rowH + 8
	}
	nav.extend(y)
}

// ── Input ────────────────────────────────────────────────────────────────

// ssyncScreenAction handles one action on the page. B steps back to the start
// view first, and offers to stop a running job.
func ssyncScreenAction(config *Config, act tbxAction) bool {
	nav := &ssyncScreen.nav
	switch act {
	case tbxActConfirm:
		ssyncActivate(config, nav.focus)
		return true
	case tbxActBack:
		if ssyncWorkSnapshot().busy {
			ssyncConfirmStop(config)
			return true
		}
		if ssyncScreen.mode != ssyncModeHome {
			PlayBackSound()
			ssyncScreen.mode = ssyncModeHome
			nav.focus, nav.scroll = "", 0
			return true
		}
		return false
	case tbxActX, tbxActY:
		return true
	}
	return tbxNavAction(nav, act)
}

// ssyncScreenClick handles a click on the page's rows.
func ssyncScreenClick(mx, my int32, config *Config) bool {
	it, ok := ssyncScreen.nav.hit(mx, my)
	if !ok {
		return false
	}
	ssyncScreen.nav.focus = it.id
	ssyncActivate(config, it.id)
	return true
}

// ssyncScreenHints is the footer.
func ssyncScreenHints() []FooterHint {
	id := ssyncScreen.nav.focus
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}}
	switch {
	case ssyncWorkSnapshot().busy:
		return []FooterHint{{Button: "B", Label: "Stop"}}
	case id == "run":
		hints = append(hints, FooterHint{Button: "A", Label: "Sync now"})
	case strings.HasPrefix(id, "dev:"):
		hints = append(hints, FooterHint{Button: "A", Label: "Compare"})
	case id == "export":
		hints = append(hints, FooterHint{Button: "A", Label: "Export"})
	case id == "import", strings.HasPrefix(id, "file:"):
		hints = append(hints, FooterHint{Button: "A", Label: "Open"})
	case id == "enable":
		hints = append(hints, FooterHint{Button: "A", Label: "Turn on"})
	case id == "pair", id == "manager":
		hints = append(hints, FooterHint{Button: "A", Label: "Open"})
	}
	return append(hints, FooterHint{Button: "B", Label: "Back"})
}

// ssyncActivate runs the focused row's action.
func ssyncActivate(config *Config, id string) {
	if ssyncWorkSnapshot().busy {
		ShowToast("Wait for the current job to finish (B stops it)", ToastKindInfo)
		return
	}
	switch {
	case id == "enable":
		PlayToggleSound()
		peerSettingSetEnabled(config, true)
	case id == "pair":
		PlayActivateSound()
		peerDevicesOpen(config)
	case id == "manager":
		openSaveManagerPanel()
	case strings.HasPrefix(id, "dev:"):
		PlayActivateSound()
		ssyncStartCompare(strings.TrimPrefix(id, "dev:"))
	case id == "run":
		ssyncStartSync()
	case strings.HasPrefix(id, "item:"):
		ssyncOpenItemSheet(config, id)
	case id == "export":
		PlayActivateSound()
		ssyncAskExportPassphrase(config, "")
	case id == "import":
		PlayActivateSound()
		ssyncOpenImport()
	case strings.HasPrefix(id, "file:"):
		n, err := strconv.Atoi(strings.TrimPrefix(id, "file:"))
		if err != nil || n < 0 || n >= len(ssyncScreen.bundles) {
			return
		}
		PlayActivateSound()
		ssyncAskImportPassphrase(config, ssyncScreen.bundles[n])
	}
}

// ssyncConfirmStop asks before stopping a running job.
func ssyncConfirmStop(config *Config) {
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Stop?",
		subtitle: ssyncWorkSnapshot().label,
		icon:     "sync",
		body:     "Files already copied stay copied; the rest is left for next time. Nothing is left half-written.",
		buttons: []tbxSheetButton{
			{label: "Keep going"},
			{label: "Stop", primary: true, run: ssyncWorkCancel},
		},
	})
}

// ssyncOpenItemSheet explains one file of the comparison.
func ssyncOpenItemSheet(config *Config, id string) {
	n, err := strconv.Atoi(strings.TrimPrefix(id, "item:"))
	cmp := ssyncScreen.cmp
	if err != nil || cmp == nil || n < 0 || n >= len(cmp.Plan.Items) {
		return
	}
	PlayActivateSound()
	it := cmp.Plan.Items[n]
	describe := func(f ssyncFile, has bool) string {
		if !has {
			return "not there"
		}
		return humanBytes(f.Size) + ", changed " + time.Unix(f.MTime, 0).Local().Format("Jan 2, 15:04")
	}
	body := "## " + it.Root + "\n" + it.Path + "\n\n" +
		"## This device\n" + describe(it.Local, it.HasLocal) + "\n\n" +
		"## " + cmp.PeerName + "\n" + describe(it.Remote, it.HasRemote) + "\n\n"
	switch it.Action {
	case ssyncPull:
		body += "The copy on " + cmp.PeerName + " comes here (" + it.Reason + ")."
	case ssyncPush:
		body += "This device's copy goes to " + cmp.PeerName + " (" + it.Reason + ")."
	case ssyncConflict:
		body += "Both copies changed since the last sync. The newer one keeps this name on both devices and the other is kept beside it as " +
			path.Base(it.CopyPath) + ", so nothing is lost."
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    path.Base(it.Path),
		subtitle: it.Reason,
		icon:     "files",
		body:     body,
		buttons:  []tbxSheetButton{{label: "Close", primary: true}},
	})
}

// ── Jobs ─────────────────────────────────────────────────────────────────

// ssyncStartCompare compares with one paired device in the background.
func ssyncStartCompare(peerID string) {
	rec, ok := peerPairedRecord(peerID)
	if !ok {
		return
	}
	if !peerEnabledFlag.Load() {
		ShowToast("Turn on Nearby devices first", ToastKindWarning)
		return
	}
	ctx, ok := ssyncWorkStart("Comparing saves with " + rec.Name + "…")
	if !ok {
		ShowToast("Wait for the current job to finish", ToastKindInfo)
		return
	}
	ssyncScreen.model.builtAt = time.Time{}
	go func() {
		cmp, err := ssyncCompare(ctx, rec.ID, rec.Name)
		tbxPost(func() {
			ssyncWorkEnd()
			ssyncScreen.model.builtAt = time.Time{}
			if err != nil {
				ShowToast("Could not compare: "+peerErrorText(err, rec.Name), ToastKindWarning)
				return
			}
			ssyncScreen.cmp = &cmp
			ssyncScreen.cmpRows, ssyncScreen.cmpSummary = ssyncBuildCompareRows(&cmp)
			ssyncScreen.mode = ssyncModeCompare
			ssyncScreen.nav.focus, ssyncScreen.nav.scroll = "run", 0
			PlaySuccessSound()
		})
	}()
}

// ssyncStartSync carries out the comparison on screen.
func ssyncStartSync() {
	cmp := ssyncScreen.cmp
	if cmp == nil || len(cmp.Plan.Items) == 0 {
		ShowToast("Nothing to copy — the saves are already the same", ToastKindInfo)
		return
	}
	ctx, ok := ssyncWorkStart("Syncing saves with " + cmp.PeerName + "…")
	if !ok {
		return
	}
	PlayActivateSound()
	job := *cmp
	go func() {
		res := ssyncRunSync(ctx, job, ssyncWorkProgress)
		tbxPost(func() {
			ssyncWorkEnd()
			ssyncScreen.cmp = nil
			ssyncScreen.cmpRows = nil
			ssyncScreen.mode = ssyncModeHome
			ssyncScreen.nav.focus, ssyncScreen.nav.scroll = "", 0
			ssyncScreen.resultLine = "Last sync with " + job.PeerName + ": " + ssyncResultText(res)
			ssyncScreen.model.builtAt = time.Time{}
			peerLastSync.mu.Lock()
			if peerLastSync.m == nil {
				peerLastSync.m = map[string]time.Time{}
			}
			peerLastSync.m[job.PeerID] = time.Now()
			peerLastSync.mu.Unlock()
			kind := ToastKindSuccess
			if res.Failed > 0 {
				kind = ToastKindWarning
			}
			msg := "Saves synced with " + job.PeerName + " · " + ssyncResultText(res)
			if res.Failed > 0 && len(res.Errors) > 0 {
				msg += " — " + res.Errors[0]
			}
			ShowToast(msg, kind)
		})
	}()
}

// ssyncAskExportPassphrase asks for the export passphrase. The text stays in
// memory only: the keyboard's prompt buffer is not saved anywhere.
func ssyncAskExportPassphrase(config *Config, seed string) {
	tbxOpenPrompt("Export passphrase", "At least 8 characters — you type it again on the other device", seed, func(text string) {
		if len([]rune(text)) < ssyncMinPassphrase {
			ShowToast(fmt.Sprintf("Use at least %d characters", ssyncMinPassphrase), ToastKindWarning)
			return
		}
		ssyncConfirmExport(config, text)
	})
}

// ssyncConfirmExport shows the passphrase once more before the export runs.
func ssyncConfirmExport(config *Config, passphrase string) {
	dir := ssyncExportDir()
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Export your saves?",
		subtitle: "One encrypted .jukasaves file",
		icon:     "upload",
		body: "## Passphrase\n" + passphrase + "\n\n" +
			"Write it down: without it the file cannot be opened, and nobody can recover it.\n\n" +
			"## Where it goes\n" + dir + "\n\n" +
			"Copy the file to your other JukaHub — with a card reader, or over SFTP (Tools → Network tools) — " +
			"put it in its JukaHub Saves folder and choose Import a saves file there.",
		buttons: []tbxSheetButton{
			{label: "Change", run: func() { ssyncAskExportPassphrase(config, passphrase) }},
			{label: "Export", primary: true, run: func() { ssyncStartExport(config, passphrase) }},
		},
		focus: 1,
	})
}

// ssyncStartExport writes the saves file in the background.
func ssyncStartExport(config *Config, passphrase string) {
	ctx, ok := ssyncWorkStart("Exporting your saves…")
	if !ok {
		return
	}
	go func() {
		file, n, err := ssyncExport(ctx, saveManagerRoots(), ssyncExportDir(), passphrase, func(done, total int) {
			ssyncWorkProgress(done, total, "")
		})
		tbxPost(func() {
			ssyncWorkEnd()
			ssyncScreen.model.builtAt = time.Time{}
			ssyncScreen.bundlesRead = false
			if err != nil {
				if errors.Is(err, errSaveManagerEmpty) {
					ShowToast("No saves to export yet — play a game first", ToastKindInfo)
					return
				}
				ShowToast("Export failed: "+firstLine(err.Error()), ToastKindError)
				return
			}
			ssyncScreen.resultLine = fmt.Sprintf("Exported %d saves to %s", n, filepath.Base(file))
			PlaySuccessSound()
			tbxOpenSheet(config, tbxSheetSpec{
				title:    "Saves exported",
				subtitle: fmt.Sprintf("%d saves · %s", n, ssyncFileSize(file)),
				icon:     "upload",
				body: "## File\n" + file + "\n\n" +
					"Copy it to the other JukaHub's JukaHub Saves folder (card reader or SFTP), then choose " +
					"Import a saves file there and type the passphrase.",
				buttons: []tbxSheetButton{{label: "Done", primary: true}},
			})
		})
	}()
}

// ssyncFileSize is a file's size for a sheet, or "".
func ssyncFileSize(p string) string {
	info, err := os.Stat(p)
	if err != nil {
		return ""
	}
	return humanBytes(info.Size())
}

// ssyncOpenImport switches to the file list and looks for files off the main
// loop.
func ssyncOpenImport() {
	ssyncScreen.mode = ssyncModeImport
	ssyncScreen.bundlesRead = false
	ssyncScreen.nav.focus, ssyncScreen.nav.scroll = "", 0
	go func() {
		list := ssyncFindBundles()
		tbxPost(func() {
			ssyncScreen.bundles = list
			ssyncScreen.bundlesRead = true
			ssyncScreen.model.builtAt = time.Time{}
		})
	}()
}

// ssyncAskImportPassphrase asks for a file's passphrase, then previews it.
func ssyncAskImportPassphrase(config *Config, b ssyncBundleInfo) {
	tbxOpenPrompt("Passphrase", "The one chosen when "+b.Name+" was exported", "", func(text string) {
		if text == "" {
			return
		}
		ssyncStartPreview(config, b, text)
	})
}

// ssyncStartPreview reads the file's list with the passphrase and shows what
// importing it would change.
func ssyncStartPreview(config *Config, b ssyncBundleInfo, passphrase string) {
	ctx, ok := ssyncWorkStart("Opening " + b.Name + "…")
	if !ok {
		return
	}
	go func() {
		prev, err := ssyncImportPreviewFile(ctx, b.Path, passphrase, saveManagerRoots())
		tbxPost(func() {
			ssyncWorkEnd()
			ssyncScreen.model.builtAt = time.Time{}
			if err != nil {
				ShowToast(ssyncImportErrorText(err), ToastKindWarning)
				return
			}
			ssyncOpenPreviewSheet(config, prev, passphrase)
		})
	}()
}

// ssyncImportErrorText is a failed import as a sentence.
func ssyncImportErrorText(err error) string {
	switch {
	case errors.Is(err, errSsyncAuth):
		return "Wrong passphrase — or the file was changed or damaged"
	case errors.Is(err, errSsyncTruncated):
		return "The file is cut short — copy it again"
	case errors.Is(err, errSsyncNotBundle):
		return "That is not a JukaHub saves file"
	case errors.Is(err, context.Canceled):
		return "Stopped"
	}
	return "Could not read the file: " + firstLine(err.Error())
}

// ssyncOpenPreviewSheet shows what an import would change.
func ssyncOpenPreviewSheet(config *Config, prev ssyncImportPreview, passphrase string) {
	plan := prev.Plan
	when := prev.Header.Created
	if t, err := time.Parse(time.RFC3339, when); err == nil {
		when = t.Local().Format("Jan 2, 2006 15:04")
	}
	body := "## In this file\n" + strconv.Itoa(len(prev.Bundle.Files)) + " saves from " + prev.From + ", exported " + when + "\n\n" +
		"## What changes here\n" +
		"• " + strconv.Itoa(plan.Pull) + " new or newer saves are copied in\n" +
		"• " + strconv.Itoa(plan.Conflict) + " changed here and there: both are kept\n" +
		"• " + strconv.Itoa(prev.Kept) + " stay as they are: newer here, or already kept\n" +
		"• " + strconv.Itoa(len(plan.Same)) + " are already the same\n\n" +
		"Every save it replaces goes into a Save Manager snapshot first."
	if len(plan.Skipped) > 0 {
		body += "\n\n" + strconv.Itoa(len(plan.Skipped)) + " save(s) are left out: this device has no such save folder."
	}
	buttons := []tbxSheetButton{{label: "Cancel"}}
	if len(plan.Items) > 0 {
		buttons = append(buttons, tbxSheetButton{label: "Import", primary: true, run: func() { ssyncStartImport(prev, passphrase) }})
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Import saves from " + prev.From + "?",
		subtitle: filepath.Base(prev.Path),
		icon:     "download",
		body:     body,
		buttons:  buttons,
		focus:    len(buttons) - 1,
	})
}

// ssyncStartImport merges the file in the background.
func ssyncStartImport(prev ssyncImportPreview, passphrase string) {
	ctx, ok := ssyncWorkStart("Importing saves from " + prev.From + "…")
	if !ok {
		return
	}
	go func() {
		res, err := ssyncImportApply(ctx, prev, passphrase, saveManagerRoots(), ssyncWorkProgress)
		tbxPost(func() {
			ssyncWorkEnd()
			ssyncScreen.mode = ssyncModeHome
			ssyncScreen.nav.focus, ssyncScreen.nav.scroll = "", 0
			ssyncScreen.model.builtAt = time.Time{}
			if err != nil {
				ShowToast(ssyncImportErrorText(err), ToastKindWarning)
				return
			}
			ssyncScreen.resultLine = "Imported from " + prev.From + ": " + ssyncResultText(res)
			kind := ToastKindSuccess
			if res.Failed > 0 {
				kind = ToastKindWarning
			}
			ShowToast("Saves imported · "+ssyncResultText(res), kind)
		})
	}()
}
