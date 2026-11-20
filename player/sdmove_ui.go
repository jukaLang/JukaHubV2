package main

// sdmove_ui.go — the "Move to a new SD card" screen (Settings → About, and
// Tools → Your setup on the handheld).
//
// One page that walks through four stages:
//
//   - Choose: what is on the card now, the cards the move could go to (USB
//     card readers from USB & OTG, a second slot) and, on a card that was
//     moved to, the result of the check after the swap.
//   - Plan: every category with its size and a switch, the new card's room
//     and format, and a checklist whose failed hard checks keep Start off.
//   - Moving: one progress card; Stop (or Back) asks before it stops.
//   - Done: what happened, and on success exactly how to swap the cards.
//
// The disk work runs on goroutines (sdmove.go, sdmove_copy.go); this file
// reads their published state under a lock and never touches a card itself.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const sdMoveSceneName = "SDCardMove"

// sdMoveShared is written by the workers and read by the screen.
var sdMoveShared struct {
	mu        sync.Mutex
	gen       uint64
	extra     []sdMoveDest
	extraBusy bool
	scanning  bool
	scanFiles int
	scan      *sdMoveScan
	scanErr   string
	probing   bool
	probeFor  string
	info      *sdMoveDestInfo
	running   bool
	cancel    context.CancelFunc
	prog      sdMoveProgress
	progAt    time.Time // when the run last reported, for the stall hint
	result    *sdMoveResult
	mounting  string
}

// sdMoveSnap is the shared state copied for one frame.
type sdMoveSnap struct {
	gen       uint64
	extra     []sdMoveDest
	scanning  bool
	scanFiles int
	scan      *sdMoveScan
	scanErr   string
	probing   bool
	probeFor  string
	info      *sdMoveDestInfo
	running   bool
	prog      sdMoveProgress
	progAt    time.Time
	result    *sdMoveResult
	mounting  string
}

func sdMoveSnapshot() sdMoveSnap {
	sdMoveShared.mu.Lock()
	defer sdMoveShared.mu.Unlock()
	return sdMoveSnap{
		gen: sdMoveShared.gen, extra: sdMoveShared.extra, scanning: sdMoveShared.scanning,
		scanFiles: sdMoveShared.scanFiles, scan: sdMoveShared.scan, scanErr: sdMoveShared.scanErr,
		probing: sdMoveShared.probing, probeFor: sdMoveShared.probeFor, info: sdMoveShared.info,
		running: sdMoveShared.running, prog: sdMoveShared.prog, progAt: sdMoveShared.progAt, result: sdMoveShared.result,
		mounting: sdMoveShared.mounting,
	}
}

// sdMoveUpdate changes the shared state under its lock.
func sdMoveUpdate(fn func()) {
	sdMoveShared.mu.Lock()
	fn()
	sdMoveShared.gen++
	sdMoveShared.mu.Unlock()
}

// The screen's stages.
const (
	sdMoveStageChoose = iota
	sdMoveStagePlan
	sdMoveStageMoving
	sdMoveStageDone
)

type sdMoveButton struct {
	id       string
	label    string
	primary  bool
	disabled bool
}

type sdMoveRowModel struct {
	id   string
	spec tbxRowSpec
}

type sdMoveSection struct {
	title string
	icon  string
	rows  []sdMoveRowModel
}

type sdMoveModel struct {
	builtAt  time.Time
	stage    int
	header   UIPageHeader
	icon     string
	headline string
	sub      string
	ink      sdl.Color
	progress float64
	buttons  []sdMoveButton
	sections []sdMoveSection
}

// sdMoveScreen is the screen's own state, main thread only.
var sdMoveScreen struct {
	visit     tbxVisit
	nav       tbxNav
	wrap      tbxWrapCache
	model     sdMoveModel
	refreshAt time.Time
	dest      sdMoveDest
	leftOut   map[string]bool
	acceptBig bool
	plan      sdMovePlan
	planKey   string
	planAt    time.Time
	dirty     bool
}

// sdMoveOpenScreen opens the screen (Settings and the Tools hub call it).
func sdMoveOpenScreen(config *Config) {
	if !sdMoveSupported() {
		ShowToast("Moving to a new SD card works on the handheld", ToastKindInfo)
		return
	}
	tbxOpenScene(config, sdMoveSceneName)
}

type sdMoveScene struct{ sceneBase }

func (sdMoveScene) render(r *sdl.Renderer, c *Config) bool {
	if sdMoveScreen.visit.begin() {
		sdMoveScreen.nav.scroll = 0
		sdMoveScreen.dirty = true
		sdMoveScreen.refreshAt = time.Time{}
		sdMoveEnsureScan(false)
		if _, _, _, running, _ := sdMoveCheckView(); !running {
			// Shows a stored result, or picks up a check that has not run.
			sdMoveCheckStart(false)
		}
	}
	sdMoveRenderScreen(r, c)
	return true
}

func (sdMoveScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), sdMoveSceneAction)
}

func (sdMoveScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), sdMoveSceneAction)
}

func init() { registerScene(sdMoveScene{sceneBase{name: sdMoveSceneName}}) }

// sdMoveSettingsRow is the Settings → About row.
func sdMoveSettingsRow() setRow {
	return setRow{
		ID: "sdMove", Label: "Move to a new SD card", Kind: setKindAction,
		Detail:  func(*Config) string { return sdMoveStatusLine() },
		Visible: func(*Config) bool { return sdMoveSupported() },
		Open:    sdMoveOpenScreen,
	}
}

// sdMoveHubTile is the Tools hub tile (handheld only).
func sdMoveHubTile() toolsHubTile {
	t := toolsHubTile{id: "sdmove", label: "Move to a new SD card", icon: "transfer", value: sdMoveStatusLine()}
	if sdMoveSnapshot().running {
		t.tone = toolsHubToneBusy
	}
	return t
}

// sdMoveStatusLine is the one-liner Settings and the Tools tile show.
func sdMoveStatusLine() string {
	snap := sdMoveSnapshot()
	if snap.running {
		if snap.prog.TotalBytes > 0 {
			return fmt.Sprintf("Moving… %d%%", int(float64(snap.prog.Bytes)*100/float64(snap.prog.TotalBytes)))
		}
		return "Moving…"
	}
	if st, done, total, running, _ := sdMoveCheckView(); st != nil {
		switch {
		case st.Err != "" || st.MoveState != sdMoveStateVerified || st.problems() > 0:
			return "New card check: keep your old card"
		case running && total > 0:
			return fmt.Sprintf("Checking the new card… %d%%", done*100/total)
		case st.Phase == sdMoveCheckDone:
			return "New card checked: all " + pluralizeCount(st.Files, "file", "files") + " are here"
		}
	}
	return "Copy everything to a new card, checked file by file"
}

// ── Background work ──────────────────────────────────────────────────────

// sdMoveEnsureScan walks the card unless a walk is running (or done, unless
// again).
func sdMoveEnsureScan(again bool) {
	start := false
	sdMoveUpdate(func() {
		if sdMoveShared.scanning || sdMoveShared.running || (sdMoveShared.scan != nil && !again) {
			return
		}
		sdMoveShared.scanning, sdMoveShared.scanFiles, sdMoveShared.scanErr = true, 0, ""
		start = true
	})
	if !start {
		return
	}
	go func() {
		s, err := sdMoveScanSource(context.Background(), sdMoveSourceRoot(), func(n int) {
			sdMoveUpdate(func() { sdMoveShared.scanFiles = n })
		})
		sdMoveUpdate(func() {
			sdMoveShared.scanning = false
			if err != nil {
				sdMoveShared.scanErr = firstLine(err.Error())
				return
			}
			sdMoveShared.scan = s
			sdMoveShared.scanFiles = len(s.Items)
		})
		tbxPost(func() { sdMoveScreen.dirty = true })
	}()
}

// sdMoveRefreshDests asks USB & OTG for a scan and looks for other mounted
// cards, both off the render thread.
func sdMoveRefreshDests() {
	usbOtgRefresh()
	start := false
	sdMoveUpdate(func() {
		if !sdMoveShared.extraBusy {
			sdMoveShared.extraBusy, start = true, true
		}
	})
	if !start {
		return
	}
	go func() {
		extra := sdMoveExtraDests(sdMoveSourceRoot())
		sdMoveUpdate(func() {
			sdMoveShared.extra, sdMoveShared.extraBusy = extra, false
		})
	}()
}

// sdMoveDests is every destination on screen: USB volumes first.
func sdMoveDests(snap sdMoveSnap) []sdMoveDest {
	out := sdMoveUSBDests(usbOtgCurrent())
	mounted := map[string]bool{}
	for _, d := range out {
		if d.Path != "" {
			mounted[d.Path] = true
		}
	}
	// A USB volume the firmware mounted under /media is listed once, as USB.
	for _, d := range snap.extra {
		if !mounted[d.Path] {
			out = append(out, d)
		}
	}
	return out
}

// sdMoveProbe looks at the chosen destination on a goroutine.
func sdMoveProbe(d sdMoveDest) {
	sdMoveUpdate(func() {
		sdMoveShared.probing, sdMoveShared.probeFor, sdMoveShared.info = true, d.ID, nil
	})
	go func() {
		info := sdMoveProbeDest(d.Path, sdMoveSourceRoot())
		info.Label = d.Label
		sdMoveUpdate(func() {
			if sdMoveShared.probeFor == d.ID {
				sdMoveShared.probing, sdMoveShared.info = false, &info
			}
		})
		tbxPost(func() { sdMoveScreen.dirty = true })
	}()
}

// sdMoveChoose picks a destination: mounts a USB volume first when it is not
// mounted yet.
func sdMoveChoose(d sdMoveDest) {
	if d.Path != "" {
		sdMoveScreen.dest = d
		sdMoveScreen.nav.scroll = 0
		sdMoveScreen.nav.focus = ""
		sdMoveScreen.planKey = ""
		sdMoveProbe(d)
		return
	}
	if !d.USB || d.VolName == "" {
		return
	}
	if snap := sdMoveSnapshot(); snap.mounting != "" {
		return
	}
	sdMoveUpdate(func() { sdMoveShared.mounting = d.VolName })
	go func() {
		point, err := usbOtgMountVolume(d.VolName, d.FS)
		sdMoveUpdate(func() { sdMoveShared.mounting = "" })
		tbxPost(func() {
			if err != nil {
				ShowToast("Could not mount the new card: "+firstLine(err.Error()), ToastKindError)
				return
			}
			usbOtgRefresh()
			d.Path = point
			sdMoveChoose(d)
		})
	}()
}

// sdMoveCurrentPlan is the plan for the chosen destination, recomputed when a
// choice or the shared state changes and every few seconds (battery, games).
func sdMoveCurrentPlan(snap sdMoveSnap) sdMovePlan {
	if snap.scan == nil || snap.info == nil {
		return sdMovePlan{}
	}
	var lo []string
	for _, c := range sdMoveCatOrder {
		if sdMoveScreen.leftOut[c] {
			lo = append(lo, c)
		}
	}
	key := fmt.Sprintf("%p|%p|%s|%v", snap.scan, snap.info, strings.Join(lo, ","), sdMoveScreen.acceptBig)
	if key == sdMoveScreen.planKey && time.Since(sdMoveScreen.planAt) < 5*time.Second {
		return sdMoveScreen.plan
	}
	game := gameScreenSuspended() || len(liveScreenChildren()) > 0
	sdMoveScreen.plan = sdMoveEvaluate(snap.scan, *snap.info, sdMoveScreen.leftOut, sdMoveScreen.acceptBig, getBatteryPercent(), getBatteryCharging(), game)
	sdMoveScreen.planKey, sdMoveScreen.planAt = key, time.Now()
	return sdMoveScreen.plan
}

// sdMoveStart starts the move with the plan's choices.
func sdMoveStart(config *Config) {
	snap := sdMoveSnapshot()
	if snap.running || snap.scan == nil || snap.info == nil {
		return
	}
	sdMoveScreen.planKey = ""
	plan := sdMoveCurrentPlan(snap)
	if !plan.CanStart {
		for _, c := range plan.Checks {
			if c.Hard && !c.OK {
				ShowToast(c.Title+": "+c.Sub, ToastKindWarning)
				return
			}
		}
		ShowToast("Nothing to copy", ToastKindInfo)
		return
	}
	scan, dest := snap.scan, sdMoveScreen.dest
	root := snap.info.Root
	opts := sdMoveOptions{LeftOut: map[string]bool{}, AcceptBig: sdMoveScreen.acceptBig}
	for c, on := range sdMoveScreen.leftOut {
		if on {
			opts.LeftOut[c] = true
		}
	}
	version := versionString(config)
	ctx, cancel := context.WithCancel(context.Background())
	sdMoveUpdate(func() {
		sdMoveShared.running, sdMoveShared.cancel, sdMoveShared.result = true, cancel, nil
		sdMoveShared.prog = sdMoveProgress{Phase: "prepare", Began: time.Now()}
		sdMoveShared.progAt = time.Now()
	})
	sdMoveScreen.nav.focus = ""
	sdMoveScreen.nav.scroll = 0
	go func() {
		defer cancel()
		// Looked at once more, and the whole plan checked again with what
		// is there now: the reader may have been swapped or filled since.
		fresh := sdMoveProbeDest(root, scan.Root)
		fresh.Label = dest.Label
		var res sdMoveResult
		switch {
		case fresh.Err != "":
			res.Err = "The move could not start: " + fresh.Err
		case fresh.SameCard || !fresh.Writable:
			res.Err = "The move could not start: the new card cannot be written to."
		default:
			game := gameScreenSuspended() || len(liveScreenChildren()) > 0
			again := sdMoveEvaluate(scan, fresh, opts.LeftOut, opts.AcceptBig, getBatteryPercent(), getBatteryCharging(), game)
			if !again.CanStart {
				res.Err = "The move could not start: nothing to copy."
				for _, c := range again.Checks {
					if c.Hard && !c.OK {
						res.Err = "The move could not start: " + c.Title + " — " + c.Sub + "."
						break
					}
				}
				break
			}
			res = sdMoveRun(ctx, scan, fresh, opts, version, func(p sdMoveProgress) {
				sdMoveUpdate(func() { sdMoveShared.prog, sdMoveShared.progAt = p, time.Now() })
			})
		}
		res.DestDisk, res.USB = dest.Disk, dest.USB
		if res.DestPath == "" {
			res.DestPath = root
		}
		sdMoveUpdate(func() {
			sdMoveShared.running, sdMoveShared.cancel = false, nil
			sdMoveShared.result = &res
			// The new card changed: look at it again before another plan.
			sdMoveShared.info, sdMoveShared.probeFor = nil, ""
		})
		msg, kind := sdMoveResultToast(res)
		ShowToast(msg, kind)
		tbxPost(func() { sdMoveScreen.dirty = true })
	}()
}

// sdMoveResultToast is a finished run in one sentence.
func sdMoveResultToast(res sdMoveResult) (string, ToastKind) {
	switch {
	case res.Canceled:
		return "Move stopped — start it again to continue where it stopped", ToastKindInfo
	case res.Err != "":
		return firstLine(res.Err), ToastKindWarning
	case res.State == sdMoveStateVerified:
		return "The new card is ready: " + pluralizeCount(res.Files, "file", "files") + " copied and checked", ToastKindSuccess
	}
	return "The move finished, but " + pluralizeCount(len(res.Failed), "file", "files") + " did not copy correctly", ToastKindWarning
}

// sdMoveAskStop asks before stopping a running move.
func sdMoveAskStop(config *Config) {
	tbxOpenSheet(config, tbxSheetSpec{
		title: "Stop the move?",
		icon:  "transfer",
		body:  "What is copied so far stays on the new card. Start the move again later and it continues where it stopped — files already copied are not copied twice.\n\nDo not swap the cards until a move has finished and says the new card is ready.",
		buttons: []tbxSheetButton{
			{label: "Keep copying"},
			{label: "Stop", primary: true, run: func() {
				sdMoveUpdate(func() {
					if sdMoveShared.cancel != nil {
						sdMoveShared.cancel()
					}
				})
				ShowToast("Stopping after the current file…", ToastKindInfo)
			}},
		},
		focus: 0,
	})
}

// ── Model ────────────────────────────────────────────────────────────────

func sdMoveStageOf(snap sdMoveSnap) int {
	switch {
	case snap.running:
		return sdMoveStageMoving
	case snap.result != nil:
		return sdMoveStageDone
	case sdMoveScreen.dest.ID != "":
		return sdMoveStagePlan
	}
	return sdMoveStageChoose
}

// sdMoveSpeedLine is "X of Y · Z/s · about N min left".
func sdMoveSpeedLine(p sdMoveProgress) string {
	if p.TotalBytes <= 0 {
		return ""
	}
	line := humanBytes(p.Bytes) + " of " + humanBytes(p.TotalBytes)
	secs := time.Since(p.Began).Seconds()
	if secs >= 3 && p.Bytes > 0 {
		speed := float64(p.Bytes) / secs
		line += " · " + humanBytes(int64(speed)) + "/s"
		if left := float64(p.TotalBytes-p.Bytes) / speed; left > 0 && p.Paused == "" {
			mins := int(left/60 + 0.5)
			switch {
			case mins < 1:
				line += " · less than a minute left"
			case mins < 90:
				line += fmt.Sprintf(" · about %d min left", mins)
			default:
				line += fmt.Sprintf(" · about %d h %d min left", mins/60, mins%60)
			}
		}
	}
	return line
}

func sdMoveBuildModel(snap sdMoveSnap) sdMoveModel {
	m := sdMoveModel{builtAt: time.Now(), stage: sdMoveStageOf(snap), progress: -1, ink: ColorTextPrimary()}
	status, statusColor := "", ColorTextMuted()
	switch m.stage {
	case sdMoveStageMoving:
		status, statusColor = "Moving", glassAccent()
		sdMoveModelMoving(&m, snap)
	case sdMoveStageDone:
		sdMoveModelDone(&m, snap)
		status = "Finished"
		if snap.result.State == sdMoveStateVerified && snap.result.Err == "" && !snap.result.Canceled {
			status, statusColor = "Ready to swap", ToastSuccessInk()
		}
	case sdMoveStagePlan:
		sdMoveModelPlan(&m, snap)
		status = "Plan"
	default:
		sdMoveModelChoose(&m, snap)
	}
	m.header = tbxHeader("Move to a new SD card", "Copy everything to a new card, then check it", "transfer", status, statusColor)
	return m
}

// sdMoveCheckRow is the "check after the swap" row, when this card was moved
// to.
func sdMoveCheckRow() (sdMoveRowModel, bool) {
	st, done, total, running, paused := sdMoveCheckView()
	if st == nil {
		return sdMoveRowModel{}, false
	}
	msg, kind := sdMoveCheckSummary(st)
	spec := tbxRowSpec{icon: "check", title: "New card check", subtitle: msg, pill: "All here", tone: tbxToneGood}
	switch {
	case kind == ToastKindWarning:
		spec.icon, spec.pill, spec.tone = "warning", "Keep old card", tbxToneBad
	case paused && running:
		spec.pill, spec.tone = "Paused", tbxToneNeutral
	case running && total > 0:
		spec.pill, spec.tone = fmt.Sprintf("Checking %d%%", done*100/total), tbxToneAccent
	case st.Phase != sdMoveCheckDone:
		spec.pill, spec.tone = "Waiting", tbxToneNeutral
	case kind == ToastKindInfo:
		spec.pill, spec.tone = "Keep old card", tbxToneWarn
	}
	return sdMoveRowModel{id: "check", spec: spec}, true
}

func sdMoveModelChoose(m *sdMoveModel, snap sdMoveSnap) {
	m.icon = "transfer"
	m.headline = "Move everything to a new card"
	m.sub = "Games, saves, BIOS files, ports, artwork, emulators, JukaHub and its settings are copied to the new card, and every copy is read back and compared before you swap. Put the new card into a USB card reader on the USB-C port, then choose it below."
	if row, ok := sdMoveCheckRow(); ok {
		m.sections = append(m.sections, sdMoveSection{title: "This card", icon: "check", rows: []sdMoveRowModel{row}})
	}
	src := tbxRowSpec{icon: "disk", title: "SD card"}
	switch {
	case snap.scanErr != "":
		src.subtitle, src.pill, src.tone = snap.scanErr, "Problem", tbxToneBad
	case snap.scan == nil:
		src.subtitle, src.pill, src.tone = "Looking at your card… "+pluralizeCount(snap.scanFiles, "file", "files"), "Reading", tbxToneAccent
	default:
		s := snap.scan
		bits := []string{pluralizeCount(len(s.Items), "file", "files"), humanBytes(s.Bytes)}
		if fsName := usbFSName(s.FS); fsName != "" {
			bits = append(bits, fsName)
		}
		src.subtitle = strings.Join(bits, " · ")
		src.pill, src.tone = "Ready", tbxToneGood
		if s.System {
			src.subtitle += " · starts the handheld itself"
		}
		if len(s.Unreadable) > 0 {
			src.pill, src.tone = pluralizeCount(len(s.Unreadable), "read error", "read errors"), tbxToneWarn
		}
	}
	rows := []sdMoveRowModel{{id: "src", spec: src}}
	m.sections = append(m.sections, sdMoveSection{title: "From", icon: "disk", rows: rows})

	var dests []sdMoveRowModel
	for _, d := range sdMoveDests(snap) {
		spec := tbxRowSpec{icon: "disk", title: d.Title, subtitle: d.Sub, pill: "Choose", tone: tbxToneAccent}
		switch {
		case snap.mounting != "" && snap.mounting == d.VolName:
			spec.pill, spec.tone = "Mounting…", tbxToneAccent
		case d.Path == "":
			spec.pill, spec.tone = "Mount", tbxToneNeutral
		}
		dests = append(dests, sdMoveRowModel{id: "dest:" + d.ID, spec: spec})
	}
	if len(dests) == 0 {
		dests = append(dests, sdMoveRowModel{id: "nodest", spec: tbxRowSpec{icon: "plug", title: "No new card found",
			subtitle: "Plug a USB card reader with the new card into the USB-C port", dim: true}})
	}
	m.sections = append(m.sections, sdMoveSection{title: "To", icon: "transfer", rows: dests})
	m.sections = append(m.sections, sdMoveSection{title: "Help", icon: "info", rows: []sdMoveRowModel{
		{id: "about", spec: tbxRowSpec{icon: "info", title: "How the move works", subtitle: "What is copied, how it is checked, and the swap"}},
	}})
}

func sdMoveModelPlan(m *sdMoveModel, snap sdMoveSnap) {
	d := sdMoveScreen.dest
	m.icon = "transfer"
	m.headline = "From your SD card to " + d.Title
	switch {
	case snap.scanErr != "":
		m.sub = "Your card could not be read: " + snap.scanErr
		m.buttons = []sdMoveButton{{id: "back", label: "Choose another card"}}
		return
	case snap.scan == nil:
		m.sub = "Looking at your card… " + pluralizeCount(snap.scanFiles, "file", "files") + " so far."
		m.buttons = []sdMoveButton{{id: "back", label: "Choose another card"}}
		return
	case snap.info == nil || snap.probeFor != d.ID:
		m.sub = "Looking at the new card…"
		m.buttons = []sdMoveButton{{id: "back", label: "Choose another card"}}
		return
	case snap.info.Err != "":
		m.sub = "The new card could not be used: " + snap.info.Err
		m.ink = ToastWarnInk()
		m.buttons = []sdMoveButton{{id: "back", label: "Choose another card", primary: true}}
		return
	}
	info := snap.info
	plan := sdMoveCurrentPlan(snap)
	dest := humanBytes(info.Free) + " free of " + humanBytes(info.Total)
	if fsName := usbFSName(info.FS); fsName != "" {
		dest = fsName + " · " + dest
	}
	m.sub = fmt.Sprintf("%s to copy (%s). The new card: %s.", humanBytes(plan.Bytes), pluralizeCount(plan.Files, "file", "files"), dest)
	if !plan.CanStart {
		m.sub += " Fix the checks marked below to start."
	}
	m.buttons = append(m.buttons, sdMoveButton{id: "start", label: "Start the move", primary: true, disabled: !plan.CanStart})
	if len(plan.Suggest) > 0 {
		label := "Leave out " + sdMoveCatLabel(plan.Suggest[0])
		if len(plan.Suggest) > 1 {
			label += " and more"
		}
		m.buttons = append(m.buttons, sdMoveButton{id: "suggest", label: label})
	}
	m.buttons = append(m.buttons, sdMoveButton{id: "back", label: "Another card"})

	var cats []sdMoveRowModel
	for _, c := range sdMoveCatOrder {
		t := snap.scan.Cats[c]
		if t.Files == 0 {
			continue
		}
		spec := tbxRowSpec{icon: sdMoveCatIcon(c), title: sdMoveCatLabel(c),
			subtitle: pluralizeCount(t.Files, "file", "files") + " · " + humanBytes(t.Bytes), pill: "Included", tone: tbxToneGood}
		if sdMoveScreen.leftOut[c] {
			spec.pill, spec.tone, spec.dim = "Left out", tbxToneNeutral, true
		}
		cats = append(cats, sdMoveRowModel{id: "cat:" + c, spec: spec})
	}
	m.sections = append(m.sections, sdMoveSection{title: "What is copied", icon: "files", rows: cats})

	var checks []sdMoveRowModel
	for _, c := range plan.Checks {
		spec := tbxRowSpec{icon: "check", title: c.Title, subtitle: c.Sub, pill: "OK", tone: tbxToneGood}
		switch {
		case c.Note:
			spec.icon, spec.pill, spec.tone = "info", "Note", tbxToneNeutral
		case !c.OK && c.Hard:
			spec.icon, spec.pill, spec.tone = "warning", "Fix this", tbxToneBad
		case !c.OK:
			spec.icon, spec.pill, spec.tone = "warning", "Warning", tbxToneWarn
		}
		checks = append(checks, sdMoveRowModel{id: "chk:" + c.ID, spec: spec})
	}
	m.sections = append(m.sections, sdMoveSection{title: "Checks", icon: "shield", rows: checks})
	if n := len(snap.scan.Skipped); n > 0 {
		m.sections = append(m.sections, sdMoveSection{title: "Left alone", icon: "info", rows: []sdMoveRowModel{
			{id: "skipped", spec: tbxRowSpec{icon: "info", title: "Not copied on purpose", subtitle: pluralizeCount(n, "item", "items") + ": logs, swap files, links, the computer's own folders"}},
		}})
	}
}

func sdMoveModelMoving(m *sdMoveModel, snap sdMoveSnap) {
	p := snap.prog
	m.icon = "transfer"
	switch p.Phase {
	case "copy":
		m.headline = fmt.Sprintf("Copying file %d of %d", min(p.Done+1, max(p.Total, 1)), p.Total)
	case "verify":
		m.headline = fmt.Sprintf("Checking copy %d of %d on the new card", min(p.Done+1, max(p.Total, 1)), p.Total)
	case "catchup":
		if p.Total > 0 {
			m.headline = fmt.Sprintf("Copying what changed meanwhile: %d of %d", min(p.Done+1, p.Total), p.Total)
		} else {
			m.headline = "Looking for changes made meanwhile… " + pluralizeCount(p.Done, "file", "files")
		}
	case "finish":
		m.headline = "Writing the move's record…"
	default:
		m.headline = "Getting ready…"
	}
	var lines []string
	if s := sdMoveSpeedLine(p); s != "" {
		lines = append(lines, s)
	}
	if p.Paused != "" {
		lines = append(lines, p.Paused+".")
	} else if working := p.Phase == "copy" || p.Phase == "verify" || (p.Phase == "catchup" && p.Total > 0); working && !snap.progAt.IsZero() {
		// The worker never blocks this screen, but a read stuck on a
		// failing card looks like a frozen bar: say what is going on.
		if idle := time.Since(snap.progAt); idle >= 45*time.Second {
			lines = append(lines, fmt.Sprintf("No progress for %d s — a failing card can take minutes over one file. Stop ends the move as soon as the card answers.", int(idle.Seconds())))
		}
	}
	if p.Current != "" {
		lines = append(lines, p.Current)
	}
	m.sub = strings.Join(lines, "\n")
	if p.TotalBytes > 0 {
		m.progress = float64(p.Bytes) / float64(p.TotalBytes)
	} else if p.Total > 0 {
		m.progress = float64(p.Done) / float64(p.Total)
	}
	m.buttons = []sdMoveButton{{id: "stop", label: "Stop the move"}}
	rows := []sdMoveRowModel{
		{id: "tip", spec: tbxRowSpec{icon: "battery", title: "Keep it plugged in", subtitle: "Leave the charger and the card reader connected until it says the new card is ready"}},
		{id: "leave", spec: tbxRowSpec{icon: "info", title: "You can leave this page", subtitle: "The move goes on; it waits while a game runs and copies its saves at the end"}},
	}
	if p.Problems > 0 {
		rows = append(rows, sdMoveRowModel{id: "probs", spec: tbxRowSpec{icon: "warning", title: "Problems so far",
			subtitle: pluralizeCount(p.Problems, "file", "files") + " could not be copied; the list comes at the end", pill: "Listed later", tone: tbxToneWarn}})
	}
	m.sections = append(m.sections, sdMoveSection{title: "While it copies", icon: "info", rows: rows})
}

func sdMoveModelDone(m *sdMoveModel, snap sdMoveSnap) {
	res := snap.result
	ok := res.State == sdMoveStateVerified && res.Err == "" && !res.Canceled
	switch {
	case ok:
		m.icon, m.headline, m.ink = "check", "The new card is ready", ToastSuccessInk()
		m.sub = fmt.Sprintf("%s (%s) are on the new card, and every copy was read back and matched the old card.", pluralizeCount(res.Files, "file", "files"), humanBytes(res.Bytes))
		if len(res.Unreadable)+len(res.NotCopied) > 0 || len(res.LeftOut) > 0 {
			m.sub += " Some files stayed behind (see below): keep your old card."
		} else {
			m.sub += " Swap the cards as described below."
		}
	case res.Canceled:
		m.icon, m.headline = "pause", "Move stopped"
		m.sub = "What was copied stays on the new card. Continue later: files already copied are not copied again. Do not swap the cards yet."
	case res.Err != "":
		m.icon, m.headline, m.ink = "warning", "The move stopped", ToastWarnInk()
		m.sub = res.Err + " Do not swap the cards yet."
	default:
		m.icon, m.headline, m.ink = "warning", "Some files did not copy correctly", ToastWarnInk()
		m.sub = "The new card did not take every file correctly — it may be faulty. Do not retire your old card. Try the move again, or try another card."
	}
	if ok && res.USB {
		m.buttons = append(m.buttons, sdMoveButton{id: "eject", label: "Eject the new card", primary: true})
	}
	if !ok {
		m.buttons = append(m.buttons, sdMoveButton{id: "again", label: "Continue the move", primary: true})
	}
	m.buttons = append(m.buttons, sdMoveButton{id: "done", label: "Done"})

	if ok {
		steps := []sdMoveRowModel{
			{id: "step:1", spec: tbxRowSpec{icon: "upload", title: "1. Eject the new card", subtitle: "Press Eject above, then take the card out of the reader"}},
			{id: "step:2", spec: tbxRowSpec{icon: "power", title: "2. Power off the handheld", subtitle: "Switch it off completely, not just to sleep — and play nothing first: what a game saves now is only on the old card"}},
			{id: "step:3", spec: tbxRowSpec{icon: "disk", title: "3. Swap the cards", subtitle: "Take the old card out of the slot, put the new one in; keep the old card safe"}},
			{id: "step:4", spec: tbxRowSpec{icon: "check", title: "4. Power on", subtitle: "JukaHub checks the new card by itself and tells you when the old one can be reused"}},
		}
		if !res.USB {
			steps[0].spec.subtitle = "Nothing to eject: power off before taking a card out"
		}
		m.sections = append(m.sections, sdMoveSection{title: "Swap the cards", icon: "disk", rows: steps})
	}
	rows := []sdMoveRowModel{
		{id: "res:verified", spec: tbxRowSpec{icon: "check", title: "Read back and matched", subtitle: "Copies compared with the old card's bytes", pill: fmt.Sprintf("%d", res.Verified), tone: tbxToneGood}},
		{id: "res:copied", spec: tbxRowSpec{icon: "transfer", title: "Copied this time", subtitle: fmt.Sprintf("%d were already on the new card from an earlier run", res.Reused), pill: fmt.Sprintf("%d", res.Copied), tone: tbxToneNeutral}},
	}
	if res.Recopied > 0 {
		rows = append(rows, sdMoveRowModel{id: "res:recopied", spec: tbxRowSpec{icon: "refresh", title: "Fixed after a mismatch", subtitle: "Copied again and matched the second time", pill: fmt.Sprintf("%d", res.Recopied), tone: tbxToneWarn}})
	}
	if n := len(res.Failed); n > 0 {
		rows = append(rows, sdMoveRowModel{id: "res:failed", spec: tbxRowSpec{icon: "warning", title: "Not correctly on the new card", subtitle: "The new card would not take them, or gave back other bytes", pill: fmt.Sprintf("%d", n), tone: tbxToneBad}})
	}
	if n := len(res.Unreadable); n > 0 {
		rows = append(rows, sdMoveRowModel{id: "res:unreadable", spec: tbxRowSpec{icon: "warning", title: "Could not be read from the old card", subtitle: "Only on the old card, if anywhere — it may be failing", pill: fmt.Sprintf("%d", n), tone: tbxToneWarn}})
	}
	if n := len(res.NotCopied); n > 0 {
		rows = append(rows, sdMoveRowModel{id: "res:notcopied", spec: tbxRowSpec{icon: "info", title: "Stayed on the old card", subtitle: "Too big for FAT32, a clashing name, a link, or a file that kept changing", pill: fmt.Sprintf("%d", n), tone: tbxToneWarn}})
	}
	if len(res.LeftOut) > 0 {
		var names []string
		for _, c := range res.LeftOut {
			names = append(names, sdMoveCatLabel(c))
		}
		rows = append(rows, sdMoveRowModel{id: "res:leftout", spec: tbxRowSpec{icon: "info", title: "Left out", subtitle: strings.Join(names, ", ") + " — still on the old card", tone: tbxToneNeutral}})
	}
	m.sections = append(m.sections, sdMoveSection{title: "Result", icon: "chart", rows: rows})
}

// ── Drawing ──────────────────────────────────────────────────────────────

func sdMoveRenderScreen(renderer *sdl.Renderer, config *Config) {
	now := time.Now()
	snap := sdMoveSnapshot()
	switch stage := sdMoveStageOf(snap); {
	case stage == sdMoveStageChoose && now.Sub(sdMoveScreen.refreshAt) >= 2*time.Second:
		sdMoveScreen.refreshAt = now
		sdMoveRefreshDests()
	case stage == sdMoveStagePlan && !snap.probing && snap.probeFor != sdMoveScreen.dest.ID && sdMoveScreen.dest.Path != "":
		// The look at the new card was dropped (a run ended): look again.
		sdMoveProbe(sdMoveScreen.dest)
	}
	if sdMoveScreen.dirty || now.Sub(sdMoveScreen.model.builtAt) >= 250*time.Millisecond {
		sdMoveScreen.model = sdMoveBuildModel(snap)
		sdMoveScreen.dirty = false
	}
	m := &sdMoveScreen.model
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, m.header)
	body := frame.bodyRect()
	nav := &sdMoveScreen.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	top := body.Y - nav.scroll
	x, w := frame.x, frame.w
	y := int32(0)
	row := 0

	// The hero card: the stage's headline, its explanation and, while
	// moving, the progress bar.
	badge := int32(56)
	textX := x + 22 + badge + 18
	textW := x + w - 22 - textX
	lines := sdMoveScreen.wrap.get(fonts.small, m.sub, textW)
	if len(lines) > 6 {
		lines = lines[:6]
	}
	block := fonts.titleH + 6 + int32(len(lines))*(fonts.smallH+2)
	if m.progress >= 0 {
		block += 18
	}
	heroH := max(int32(120), block+36)
	hero := sdl.Rect{X: x, Y: top + y, W: w, H: heroH}
	UICard(renderer, hero, DefaultUICardStyle())
	box := drawGlassBadge(renderer, sdl.Rect{X: hero.X + 22, Y: hero.Y + (heroH-badge)/2, W: badge, H: badge})
	DrawHomeIcon(renderer, m.icon, box, false)
	ty := hero.Y + (heroH-block)/2
	renderText(renderer, config, fonts.title, ellipsize(fonts.title, m.headline, textW), m.ink, textX, ty)
	ty += fonts.titleH + 6
	if m.progress >= 0 {
		UIProgressBar(renderer, sdl.Rect{X: textX, Y: ty + 2, W: textW, H: 8}, m.progress, glassA(20), glassAccent())
		ty += 18
	}
	for _, line := range lines {
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, line, textW), ColorTextMuted(), textX, ty)
		ty += fonts.smallH + 2
	}
	y += heroH + 16

	if len(m.buttons) > 0 {
		bx := x
		for _, b := range m.buttons {
			bw := tbxButtonWidth(fonts.body, b.label, 150)
			if bx+bw > x+w {
				bw = x + w - bx
			}
			rect := sdl.Rect{X: bx, Y: y, W: bw, H: 48}
			screen := rect
			screen.Y += top
			tbxDrawButton(renderer, config, fonts.body, screen, b.label, b.primary, nav.focused(b.id), b.disabled)
			nav.add(b.id, rect, row, false)
			bx += bw + 12
		}
		row++
		y += 48 + 22
	}

	rowH := tbxRowHeight(fonts)
	for _, sec := range m.sections {
		tbxDrawSection(renderer, config, sec.title, sec.icon, x, top+y, w)
		y += tbxSectionHeight
		for _, r := range sec.rows {
			rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
			screen := rect
			screen.Y += top
			tbxDrawRow(renderer, config, fonts, screen, r.spec, nav.focused(r.id))
			nav.add(r.id, rect, row, false)
			row++
			y += rowH + 8
		}
		y += 14
	}

	nav.extend(y)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

// ── Input ────────────────────────────────────────────────────────────────

func sdMoveSceneAction(config *Config, act tbxAction) bool {
	nav := &sdMoveScreen.nav
	switch act {
	case tbxActConfirm:
		sdMoveActivate(config, nav.focus)
		return true
	case tbxActX, tbxActY:
		return true
	case tbxActBack:
		switch sdMoveStageOf(sdMoveSnapshot()) {
		case sdMoveStageMoving:
			PlayActivateSound()
			sdMoveAskStop(config)
			return true
		case sdMoveStagePlan:
			PlayBackSound()
			sdMoveScreen.dest = sdMoveDest{}
			sdMoveScreen.nav.focus, sdMoveScreen.nav.scroll = "", 0
			sdMoveScreen.dirty = true
			return true
		}
		return false
	}
	return tbxNavAction(nav, act)
}

func sdMoveSceneClick(mx, my int32, config *Config) bool {
	it, ok := sdMoveScreen.nav.hit(mx, my)
	if !ok {
		return false
	}
	sdMoveScreen.nav.focus = it.id
	sdMoveActivate(config, it.id)
	return true
}

func sdMoveSceneHints() []FooterHint {
	back := "Back"
	switch sdMoveStageOf(sdMoveSnapshot()) {
	case sdMoveStageMoving:
		back = "Stop"
	case sdMoveStagePlan:
		back = "Another card"
	}
	label := "Details"
	f := sdMoveScreen.nav.focus
	switch {
	case strings.HasPrefix(f, "cat:"):
		label = "Include / leave out"
	case strings.HasPrefix(f, "dest:"):
		label = "Choose"
	case f == "start", f == "stop", f == "eject", f == "done", f == "again", f == "back", f == "suggest":
		label = "Select"
	}
	return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: label}, {Button: "B", Label: back}}
}

func sdMoveActivate(config *Config, id string) {
	if id == "" {
		return
	}
	sdMoveScreen.dirty = true
	snap := sdMoveSnapshot()
	for _, b := range sdMoveScreen.model.buttons {
		if b.id == id && b.disabled {
			PlayBackSound()
			if id == "start" {
				sdMoveStart(config) // says which check is in the way
			}
			return
		}
	}
	PlayActivateSound()
	switch {
	case id == "start":
		sdMoveStart(config)
	case id == "stop":
		sdMoveAskStop(config)
	case id == "back":
		sdMoveScreen.dest = sdMoveDest{}
		sdMoveScreen.nav.focus, sdMoveScreen.nav.scroll = "", 0
	case id == "suggest":
		plan := sdMoveCurrentPlan(snap)
		if sdMoveScreen.leftOut == nil {
			sdMoveScreen.leftOut = map[string]bool{}
		}
		var names []string
		for _, c := range plan.Suggest {
			sdMoveScreen.leftOut[c] = true
			names = append(names, sdMoveCatLabel(c))
		}
		sdMoveScreen.planKey = ""
		if len(names) > 0 {
			ShowToast("Left out: "+strings.Join(names, ", ")+" — they stay on the old card", ToastKindInfo)
		}
	case strings.HasPrefix(id, "cat:"):
		c := strings.TrimPrefix(id, "cat:")
		if sdMoveScreen.leftOut == nil {
			sdMoveScreen.leftOut = map[string]bool{}
		}
		sdMoveScreen.leftOut[c] = !sdMoveScreen.leftOut[c]
		sdMoveScreen.planKey = ""
		if sdMoveScreen.leftOut[c] {
			msg := sdMoveCatLabel(c) + " left out — they stay on the old card"
			if c == sdMoveCatSaves || c == sdMoveCatJukaHub || c == sdMoveCatBIOS {
				msg = sdMoveCatLabel(c) + " left out — the new card will not have them. Keep your old card!"
			}
			ShowToast(msg, ToastKindInfo)
		}
	case strings.HasPrefix(id, "chk:"):
		sdMoveOpenCheck(config, strings.TrimPrefix(id, "chk:"), snap)
	case strings.HasPrefix(id, "dest:"):
		did := strings.TrimPrefix(id, "dest:")
		for _, d := range sdMoveDests(snap) {
			if d.ID == did {
				sdMoveChoose(d)
				return
			}
		}
	case id == "nodest":
		usbOtgRefresh()
		ShowToast("Plug a USB card reader with the new card into the USB-C port. If nothing shows, see Tools → USB & OTG.", ToastKindInfo)
	case id == "src":
		sdMoveOpenSource(config, snap)
	case id == "skipped":
		if snap.scan != nil {
			sdMoveOpenList(config, "Not copied on purpose", "info", "These are left alone: the new card makes its own, or they cannot be copied safely.", snap.scan.Skipped)
		}
	case id == "check":
		sdMoveOpenCheckResult(config)
	case id == "about", strings.HasPrefix(id, "step:"), id == "tip", id == "leave":
		sdMoveOpenAbout(config)
	case id == "probs":
		ShowToast("The list of files that could not be copied comes when the move ends", ToastKindInfo)
	case id == "eject":
		sdMoveEject(snap)
	case id == "again":
		d := sdMoveScreen.dest
		sdMoveUpdate(func() { sdMoveShared.result = nil })
		if d.ID != "" && d.Path != "" {
			sdMoveProbe(d)
		}
	case id == "done":
		sdMoveUpdate(func() {
			sdMoveShared.result = nil
			sdMoveShared.scan = nil // the card changed during the move: walk it again next time
		})
		sdMoveScreen.dest = sdMoveDest{}
		sdMoveScreen.leftOut, sdMoveScreen.acceptBig = nil, false
		sdMoveScreen.nav.focus, sdMoveScreen.nav.scroll = "", 0
		sdMoveEnsureScan(false)
	case strings.HasPrefix(id, "res:") && snap.result != nil:
		res := snap.result
		switch id {
		case "res:failed":
			sdMoveOpenList(config, "Not correctly on the new card", "warning", "These are not (correctly) on the new card. Keep your old card. Continue the move to try them again, or use another card if this keeps happening.", res.Failed)
		case "res:unreadable":
			sdMoveOpenList(config, "Could not be read", "warning", "The old card would not give these back, even on a second try. They are not on the new card. If they matter, try reading the old card in a computer.", res.Unreadable)
		case "res:notcopied":
			sdMoveOpenList(config, "Stayed on the old card", "info", "These were not copied; they are still on the old card.", res.NotCopied)
		default:
			sdMoveOpenAbout(config)
		}
	}
}

// sdMoveEject ejects a USB destination after a finished move.
func sdMoveEject(snap sdMoveSnap) {
	if snap.result == nil || !snap.result.USB || snap.result.DestDisk == "" {
		return
	}
	disk := snap.result.DestDisk
	ShowToast("Ejecting the new card…", ToastKindInfo)
	go func() {
		lazy, err := usbOtgEjectDisk(disk)
		tbxPost(func() {
			usbOtgRefresh()
			switch {
			case err != nil:
				ShowToast("Could not eject: "+firstLine(err.Error()), ToastKindError)
			case lazy:
				ShowToast("Ejected, but a program still had a file open — wait a few seconds before you take the card out", ToastKindWarning)
			default:
				ShowToast("The new card is safe to take out. Now power off and swap the cards.", ToastKindSuccess)
			}
		})
	}()
}

// ── Sheets ───────────────────────────────────────────────────────────────

func sdMoveOpenCheck(config *Config, id string, snap sdMoveSnap) {
	plan := sdMoveCurrentPlan(snap)
	for _, c := range plan.Checks {
		if c.ID != id {
			continue
		}
		buttons := []tbxSheetButton{{label: "Close", primary: true}}
		if id == "big" {
			if sdMoveScreen.acceptBig {
				buttons = []tbxSheetButton{{label: "Copy them after all", run: func() {
					sdMoveScreen.acceptBig, sdMoveScreen.planKey, sdMoveScreen.dirty = false, "", true
				}}, {label: "Close", primary: true}}
			} else {
				buttons = []tbxSheetButton{{label: "Close"}, {label: "Copy without them", primary: true, run: func() {
					sdMoveScreen.acceptBig, sdMoveScreen.planKey, sdMoveScreen.dirty = true, "", true
					ShowToast("The big files stay on the old card", ToastKindInfo)
				}}}
			}
		}
		tbxOpenSheet(config, tbxSheetSpec{title: c.Title, subtitle: c.Sub, icon: "check", body: c.Body, buttons: buttons, focus: len(buttons) - 1})
		return
	}
}

func sdMoveOpenSource(config *Config, snap sdMoveSnap) {
	s := snap.scan
	if s == nil {
		return
	}
	var b strings.Builder
	b.WriteString("## On the card\n")
	for _, c := range sdMoveCatOrder {
		if t := s.Cats[c]; t.Files > 0 {
			b.WriteString("• " + sdMoveCatLabel(c) + ": " + pluralizeCount(t.Files, "file", "files") + ", " + humanBytes(t.Bytes) + "\n")
		}
	}
	b.WriteString("\n## The card\n• " + s.Root + "\n")
	if s.Total > 0 {
		b.WriteString("• " + humanBytes(s.Total-s.Free) + " used of " + humanBytes(s.Total) + "\n")
	}
	if fsName := usbFSName(s.FS); fsName != "" {
		b.WriteString("• Format: " + fsName + "\n")
	}
	if s.System {
		b.WriteString("\nThis card starts the handheld by itself (it has a boot area or holds the running system). Only its data partition can be copied, into the " + sdMoveOSDataLabel + " partition of a new card already written with JukaHub OS.\n")
	}
	if n := len(s.Unreadable); n > 0 {
		b.WriteString("\n## Could not be read\n")
		for i, u := range s.Unreadable {
			if i == 30 {
				fmt.Fprintf(&b, "• … and %d more\n", n-30)
				break
			}
			b.WriteString("• " + u.Path + "\n")
		}
	}
	tbxOpenSheet(config, tbxSheetSpec{title: "Your SD card", subtitle: pluralizeCount(len(s.Items), "file", "files"), icon: "disk", body: b.String(),
		buttons: []tbxSheetButton{{label: "Look again", run: func() { sdMoveEnsureScan(true) }}, {label: "Close", primary: true}}, focus: 1})
}

func sdMoveOpenList(config *Config, title, icon, intro string, list []sdMoveProblem) {
	var b strings.Builder
	b.WriteString(intro + "\n\n")
	for i, p := range list {
		if i == 200 {
			fmt.Fprintf(&b, "• … and %d more (all of them are in .jukahub-move/manifest.json on the new card)\n", len(list)-200)
			break
		}
		line := "• " + p.Path
		if p.Why != "" {
			line += " — " + p.Why
		}
		b.WriteString(line + "\n")
	}
	tbxOpenSheet(config, tbxSheetSpec{title: title, subtitle: pluralizeCount(len(list), "item", "items"), icon: icon, body: b.String(), tall: len(list) > 8,
		buttons: []tbxSheetButton{{label: "Close", primary: true}}})
}

func sdMoveOpenCheckResult(config *Config) {
	st, done, total, running, paused := sdMoveCheckView()
	if st == nil {
		return
	}
	msg, _ := sdMoveCheckSummary(st)
	var b strings.Builder
	b.WriteString(msg + "\n\n")
	if st.MoveState == sdMoveStateVerified && st.Err == "" {
		b.WriteString("## How far\n")
		switch {
		case st.Phase == sdMoveCheckDone:
			b.WriteString("• Every file's presence, size and contents were checked.\n")
		case running && total > 0:
			fmt.Fprintf(&b, "• Every file is there with its size; contents checked for %d of %d.\n", done, total)
		default:
			b.WriteString("• Every file is there with its size; the contents check continues in the background.\n")
		}
		if paused {
			b.WriteString("• Paused — it waits while a game runs, or until you resume it.\n")
		}
		if st.ChangedN > 0 {
			b.WriteString("• " + pluralizeCount(st.ChangedN, "file was", "files were") + " changed since the move (played or edited on this card) — that is fine.\n")
		}
		if st.OwnN > 0 {
			b.WriteString("• " + pluralizeCount(st.OwnN, "of JukaHub's own files was", "of JukaHub's own files were") + " updated by JukaHub since — that is fine.\n")
		}
		if len(st.LeftOut) > 0 {
			var names []string
			for _, c := range st.LeftOut {
				names = append(names, sdMoveCatLabel(c))
			}
			b.WriteString("• Left out of the move: " + strings.Join(names, ", ") + ".\n")
		}
		if st.NotOnCard > 0 {
			b.WriteString("• " + pluralizeCount(st.NotOnCard, "file was", "files were") + " not copied (unreadable, too big or clashing) and only exist on the old card.\n")
		}
	}
	list := func(head string, paths []string, n int) {
		if n == 0 {
			return
		}
		b.WriteString("\n## " + head + "\n")
		for _, p := range paths {
			b.WriteString("• " + p + "\n")
		}
		if n > len(paths) {
			fmt.Fprintf(&b, "• … and %d more\n", n-len(paths))
		}
	}
	list("Missing", st.Missing, st.MissingN)
	list("Different from the old card", st.Damaged, st.DamagedN)
	var buttons []tbxSheetButton
	if running {
		if sdMoveCheckPaused.Load() {
			buttons = append(buttons, tbxSheetButton{label: "Resume", run: func() { sdMoveCheckPaused.Store(false) }})
		} else {
			buttons = append(buttons, tbxSheetButton{label: "Pause", run: func() { sdMoveCheckPaused.Store(true) }})
		}
	} else {
		buttons = append(buttons, tbxSheetButton{label: "Check again", run: func() {
			sdMoveCheckPaused.Store(false)
			sdMoveCheckStart(true)
			ShowToast("Checking the new card again in the background", ToastKindInfo)
		}})
	}
	buttons = append(buttons, tbxSheetButton{label: "Close", primary: true})
	tbxOpenSheet(config, tbxSheetSpec{title: "New card check", icon: "check", body: b.String(), buttons: buttons, focus: len(buttons) - 1})
}

func sdMoveOpenAbout(config *Config) {
	body := "## What is copied\n" +
		"Everything on the card JukaHub runs from: games, saves and states, BIOS files, ports, artwork, emulators and apps, JukaHub with all its settings, and your other files. Left alone on purpose: logs, swap files, unfinished copies, links and the folders a computer keeps for itself.\n\n" +
		"## Where it goes\n" +
		"The handheld has one card slot, so the new card goes into a USB card reader on the USB-C port (it shows under \"To\"; a reader that is not mounted yet is mounted when you choose it). A second card slot works too, where a handheld has one.\n\n" +
		"## How it is checked\n" +
		"Each file is copied under a temporary name, written to the card for real, then given its real name, so a cut-off copy never looks finished. Afterwards every copy is read back from the new card — not from memory — and compared with the old card's bytes (SHA-256). A copy that does not match is made once more; a card that gives back wrong bytes twice is reported, which is also how a fake-capacity card shows up.\n\n" +
		"## If something goes wrong\n" +
		"A file the old card cannot read is listed and the move goes on — a failing card is a good reason to move. Stopping, a pulled reader or a flat battery lose nothing: start again and the move continues where it stopped.\n\n" +
		"## The swap\n" +
		"When the move says the new card is ready: eject it, power off, swap the cards, power on. JukaHub then checks the new card against the move's record by itself — first that every file is there, then, in the background, every file's contents — and tells you when the old card can be reused. Until then, keep the old card."
	tbxOpenSheet(config, tbxSheetSpec{title: "How the move works", icon: "info", body: body, tall: true, buttons: []tbxSheetButton{{label: "Close", primary: true}}})
}
