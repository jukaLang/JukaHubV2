package main

// devsync_ui.go — Device Sync on the Save sync page (ssyncui.go).
//
// The start view gets a "What syncs with a paired device" section: one switch
// per category (devsync.go), "Send my settings", and settings another device
// sent, waiting to be used. A comparison with a paired device also asks it
// about the switched-on categories and lists them under "Sync now": the
// merges, the BIOS files to copy each way, and one row per console of missing
// ROMs (A leaves a console out). "Sync now" then copies the saves first and
// the rest after; copying ROMs asks first, with the total size.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// devsyncPlanState is what a comparison found besides saves.
type devsyncPlanState struct {
	peerID   string
	peerName string
	at       time.Time
	old      bool
	err      string
	cols     bool
	recents  bool
	files    map[string]*devsyncFilePlan
	fileErr  map[string]string
	romSkip  map[string]bool
}

var devsyncPlanNow struct {
	mu sync.Mutex
	st *devsyncPlanState
}

// devsyncWorkLabel retitles the running Save sync job.
func devsyncWorkLabel(label string) {
	ssyncWork.mu.Lock()
	if ssyncWork.busy {
		ssyncWork.label, ssyncWork.detail = label, ""
		ssyncWork.done, ssyncWork.total = 0, 0
	}
	ssyncWork.mu.Unlock()
}

// devsyncCompareExtras asks a paired device about the switched-on categories
// (the comparison's goroutine, after the saves).
func devsyncCompareExtras(ctx context.Context, peerID, peerName string) {
	st := &devsyncPlanState{peerID: peerID, peerName: peerName, at: time.Now(),
		files: map[string]*devsyncFilePlan{}, fileErr: map[string]string{}, romSkip: map[string]bool{}}
	_, old, err := devsyncFetchCaps(ctx, peerID)
	switch {
	case err != nil:
		st.err = peerErrorText(err, peerName)
	case old:
		st.old = true
	default:
		st.cols = devsyncCatOn(dsCatCollections)
		st.recents = devsyncCatOn(dsCatRecents)
		for _, cat := range []string{dsCatBIOS, dsCatROMs} {
			if !devsyncCatOn(cat) || ctx.Err() != nil {
				continue
			}
			devsyncWorkLabel("Comparing " + devsyncCatLabel(cat) + " with " + peerName + "…")
			plan, err := devsyncCompareFiles(ctx, peerID, cat)
			if err != nil {
				st.fileErr[cat] = peerErrorText(err, peerName)
				continue
			}
			p := plan
			st.files[cat] = &p
		}
	}
	devsyncPlanNow.mu.Lock()
	devsyncPlanNow.st = st
	devsyncPlanNow.mu.Unlock()
}

// devsyncCatLabel is a category's name in a sentence.
func devsyncCatLabel(cat string) string {
	switch cat {
	case dsCatBIOS:
		return "BIOS files"
	case dsCatROMs:
		return "games"
	}
	return cat
}

// devsyncPlanFor is the comparison's extras for a device, or nil.
func devsyncPlanFor(peerID string) *devsyncPlanState {
	devsyncPlanNow.mu.Lock()
	defer devsyncPlanNow.mu.Unlock()
	if st := devsyncPlanNow.st; st != nil && st.peerID == peerID {
		return st
	}
	return nil
}

// devsyncFileTotals is what a file plan copies, without the consoles left out.
func devsyncFileTotals(st *devsyncPlanState, cat string) (pull, push int, pullBytes, pushBytes int64) {
	p := st.files[cat]
	if p == nil {
		return 0, 0, 0, 0
	}
	for _, g := range p.Groups {
		if cat == dsCatROMs && st.romSkip[strings.ToLower(g.Name)] {
			continue
		}
		pull += g.Pull
		push += g.Push
		pullBytes += g.PullBytes
		pushBytes += g.PushBytes
	}
	return pull, push, pullBytes, pushBytes
}

// devsyncHasWork reports whether Sync now has anything besides saves to do
// with this device.
func devsyncHasWork(peerID string) bool {
	st := devsyncPlanFor(peerID)
	if st == nil || st.old || st.err != "" {
		return false
	}
	if st.cols || st.recents {
		return true
	}
	for _, cat := range []string{dsCatBIOS, dsCatROMs} {
		if pull, push, _, _ := devsyncFileTotals(st, cat); pull+push > 0 {
			return true
		}
	}
	return false
}

// devsyncResult is what the extras of a sync did.
type devsyncResult struct {
	parts []string
	errs  []string
}

// devsyncRunExtras carries out the extras after the saves (the sync's
// goroutine) and forgets the comparison.
func devsyncRunExtras(ctx context.Context, peerID, peerName string) devsyncResult {
	var res devsyncResult
	st := devsyncPlanFor(peerID)
	if st == nil || st.old || st.err != "" {
		return res
	}
	defer func() {
		devsyncPlanNow.mu.Lock()
		if devsyncPlanNow.st == st {
			devsyncPlanNow.st = nil
		}
		devsyncPlanNow.mu.Unlock()
	}()
	if st.cols && ctx.Err() == nil {
		devsyncWorkLabel("Merging collections with " + peerName + "…")
		if n, err := devsyncExchangeCollections(ctx, peerID); err != nil {
			res.errs = append(res.errs, "Collections: "+peerErrorText(err, peerName))
		} else if n > 0 {
			res.parts = append(res.parts, pluralizeCount(n, "collection", "collections")+" merged")
		}
	}
	if st.recents && ctx.Err() == nil {
		devsyncWorkLabel("Merging recents and play time with " + peerName + "…")
		if n, err := devsyncExchangePlay(ctx, peerID); err != nil {
			res.errs = append(res.errs, "Recents: "+peerErrorText(err, peerName))
		} else if n > 0 {
			res.parts = append(res.parts, pluralizeCount(n, "recent game", "recent games")+" updated")
		}
	}
	for _, cat := range []string{dsCatBIOS, dsCatROMs} {
		plan := st.files[cat]
		if plan == nil || ctx.Err() != nil {
			continue
		}
		skip := map[string]bool{}
		if cat == dsCatROMs {
			for k, v := range st.romSkip {
				skip[k] = v
			}
		}
		devsyncWorkLabel("Copying " + devsyncCatLabel(cat) + " with " + peerName + "…")
		copied, errs := devsyncRunFiles(ctx, peerID, *plan, skip, ssyncWorkProgress)
		if copied > 0 {
			what := pluralizeCount(copied, "BIOS file", "BIOS files")
			if cat == dsCatROMs {
				what = pluralizeCount(copied, "game", "games")
			}
			res.parts = append(res.parts, what+" copied")
		}
		res.errs = append(res.errs, errs...)
	}
	return res
}

// devsyncResultSuffix is the extras for the sync's result line.
func devsyncResultSuffix(res devsyncResult) string {
	s := ""
	if len(res.parts) > 0 {
		s = " · " + strings.Join(res.parts, " · ")
	}
	if len(res.errs) > 0 {
		s += " — " + res.errs[0]
	}
	return s
}

// devsyncConfirmROMs asks before a sync that copies games, with the total;
// it returns false (and does nothing) when no games are to be copied.
func devsyncConfirmROMs(config *Config, start func()) bool {
	cmp := ssyncScreen.cmp
	if cmp == nil || cmp.PeerID == gdrivePeerID {
		return false
	}
	st := devsyncPlanFor(cmp.PeerID)
	if st == nil || st.old || st.err != "" {
		return false
	}
	pull, push, pullBytes, pushBytes := devsyncFileTotals(st, dsCatROMs)
	if pull+push == 0 {
		return false
	}
	body := "## Games\n" +
		fmt.Sprintf("• %s from %s to this device (%s)\n", pluralizeCount(pull, "game comes", "games come"), cmp.PeerName, humanBytes(pullBytes)) +
		fmt.Sprintf("• %s from this device to %s (%s)\n\n", pluralizeCount(push, "game goes", "games go"), cmp.PeerName, humanBytes(pushBytes)) +
		"Only games one side lacks are copied; nothing is replaced or deleted. Over handheld Wi-Fi a gigabyte takes several minutes. " +
		"A copy that stops goes on from where it was at the next sync."
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Copy " + humanBytes(pullBytes+pushBytes) + " of games?",
		subtitle: "With " + cmp.PeerName,
		icon:     "download",
		body:     body,
		buttons: []tbxSheetButton{
			{label: "Cancel"},
			{label: "Sync", primary: true, run: start},
		},
		focus: 1,
	})
	return true
}

// ── Drawing ──────────────────────────────────────────────────────────────

// devsyncRowAdder draws rows the way the Save sync page does.
type devsyncRowAdder struct {
	renderer *sdl.Renderer
	config   *Config
	fonts    tbxFonts
	frame    tbxFrame
	top      int32
	y        int32
	row      int
}

func (a *devsyncRowAdder) add(id string, spec tbxRowSpec) {
	nav := &ssyncScreen.nav
	rowH := tbxRowHeight(a.fonts)
	rect := sdl.Rect{X: a.frame.x, Y: a.y, W: a.frame.w, H: rowH}
	if a.top+a.y+rowH >= a.frame.bodyTop && a.top+a.y <= a.frame.bodyBot {
		tbxDrawRow(a.renderer, a.config, a.fonts, peerScr(rect, a.top), spec, nav.focused(id))
	}
	nav.add(id, rect, a.row, false)
	a.row++
	a.y += rowH + 8
}

func (a *devsyncRowAdder) section(title string) {
	a.y += 14
	tbxDrawSection(a.renderer, a.config, title, "sync", a.frame.x, a.top+a.y, a.frame.w)
	a.y += tbxSectionHeight
}

// devsyncRenderHome is the start view's section: the switches, Send my
// settings and waiting settings. Nothing without a paired device.
func devsyncRenderHome(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, top, y int32, row int) (int32, int) {
	if !peerEnabledFlag.Load() || peerPairedCount() == 0 {
		return y, row
	}
	a := &devsyncRowAdder{renderer: renderer, config: config, fonts: fonts, frame: frame, top: top, y: y, row: row}
	a.section("What syncs with a paired device")
	if box := devsyncInboxGet(); box != nil {
		a.add("ds:inbox", tbxRowSpec{icon: "download", title: "Settings from " + box.from,
			subtitle: "Waiting for you — look at them and use them here, or dismiss", pill: "Review", tone: tbxToneAccent})
	}
	for _, s := range devsyncCatSpecs {
		on := devsyncCatOn(s.name)
		spec := tbxRowSpec{icon: s.icon, title: s.label, subtitle: s.note, pill: setOnOff(on), tone: tbxToneNeutral}
		if on {
			spec.tone = tbxToneGood
		}
		a.add("ds:cat:"+s.name, spec)
	}
	a.add("ds:settings", tbxRowSpec{icon: "upload", title: "Send my settings",
		subtitle: "Theme, colours, wallpaper, Home layout, sounds and accessibility — to one paired device", pill: "Send", tone: tbxToneNeutral})
	return a.y, a.row
}

// devsyncRenderCompare lists a comparison's extras under Sync now.
func devsyncRenderCompare(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, top, y int32, row int) (int32, int) {
	cmp := ssyncScreen.cmp
	if cmp == nil || cmp.PeerID == gdrivePeerID {
		return y, row
	}
	st := devsyncPlanFor(cmp.PeerID)
	if st == nil {
		return y, row
	}
	a := &devsyncRowAdder{renderer: renderer, config: config, fonts: fonts, frame: frame, top: top, y: y, row: row}
	switch {
	case st.old:
		a.section("Device Sync")
		a.add("ds:x:old", tbxRowSpec{icon: "devices", title: cmp.PeerName + " has an older JukaHub",
			subtitle: "Only saves sync with it until it is updated", dim: true})
		return a.y, a.row
	case st.err != "":
		a.section("Device Sync")
		a.add("ds:x:err", tbxRowSpec{icon: "devices", title: "Only saves this time", subtitle: st.err, dim: true})
		return a.y, a.row
	}
	if !st.cols && !st.recents && len(st.files) == 0 && len(st.fileErr) == 0 {
		return y, row
	}
	a.section("Also with " + cmp.PeerName)
	if st.cols {
		a.add("ds:x:cols", tbxRowSpec{icon: "files", title: "Collections", subtitle: "Merged both ways by name when you sync", pill: "Merge", tone: tbxToneAccent})
	}
	if st.recents {
		a.add("ds:x:recents", tbxRowSpec{icon: "sync", title: "Recents & play time", subtitle: "Merged both ways when you sync", pill: "Merge", tone: tbxToneAccent})
	}
	if e := st.fileErr[dsCatBIOS]; e != "" {
		a.add("ds:x:bios", tbxRowSpec{icon: "files", title: "BIOS", subtitle: "Could not compare: " + e, dim: true})
	} else if st.files[dsCatBIOS] != nil {
		pull, push, pullBytes, pushBytes := devsyncFileTotals(st, dsCatBIOS)
		spec := tbxRowSpec{icon: "files", title: "BIOS", pill: "Copy", tone: tbxToneAccent,
			subtitle: fmt.Sprintf("%d to get (%s) · %d to send (%s) · nothing is replaced", pull, humanBytes(pullBytes), push, humanBytes(pushBytes))}
		if pull+push == 0 {
			spec.subtitle, spec.pill, spec.tone, spec.dim = "Nothing missing on either side", "", tbxToneNeutral, true
		}
		a.add("ds:x:bios", spec)
	}
	if e := st.fileErr[dsCatROMs]; e != "" {
		a.add("ds:x:roms", tbxRowSpec{icon: "download", title: "ROMs", subtitle: "Could not compare: " + e, dim: true})
	} else if p := st.files[dsCatROMs]; p != nil {
		pull, push, pullBytes, pushBytes := devsyncFileTotals(st, dsCatROMs)
		spec := tbxRowSpec{icon: "download", title: "ROMs · " + humanBytes(pullBytes+pushBytes) + " in all",
			subtitle: fmt.Sprintf("%d games to get (%s) · %d to send (%s) · A on a console leaves it out", pull, humanBytes(pullBytes), push, humanBytes(pushBytes))}
		if len(p.Groups) == 0 {
			spec.title, spec.subtitle, spec.dim = "ROMs", "No game is missing on either side", true
		}
		a.add("ds:x:roms", spec)
		for _, g := range p.Groups {
			on := !st.romSkip[strings.ToLower(g.Name)]
			gs := tbxRowSpec{icon: "download", title: g.Name, pill: setOnOff(on), tone: tbxToneGood,
				subtitle: fmt.Sprintf("%d to get (%s) · %d to send (%s)", g.Pull, humanBytes(g.PullBytes), g.Push, humanBytes(g.PushBytes))}
			if !on {
				gs.tone, gs.dim = tbxToneNeutral, true
			}
			a.add("ds:rom:"+strings.ToLower(g.Name), gs)
		}
	}
	return a.y, a.row
}

// ── Input ────────────────────────────────────────────────────────────────

// devsyncActivate runs a Device Sync row of the Save sync page; false for
// rows that are not its own.
func devsyncActivate(config *Config, id string) bool {
	if !strings.HasPrefix(id, "ds:") {
		return false
	}
	switch {
	case strings.HasPrefix(id, "ds:cat:"):
		name := strings.TrimPrefix(id, "ds:cat:")
		PlayToggleSound()
		devsyncSetCat(config, name, !devsyncCatOn(name))
		ssyncScreen.model.builtAt = time.Time{}
	case strings.HasPrefix(id, "ds:rom:"):
		cmp := ssyncScreen.cmp
		if cmp == nil {
			return true
		}
		if st := devsyncPlanFor(cmp.PeerID); st != nil {
			key := strings.TrimPrefix(id, "ds:rom:")
			PlayToggleSound()
			devsyncPlanNow.mu.Lock()
			st.romSkip[key] = !st.romSkip[key]
			devsyncPlanNow.mu.Unlock()
		}
	case id == "ds:settings":
		PlayActivateSound()
		devsyncPickSettingsTarget(config)
	case id == "ds:inbox":
		PlayActivateSound()
		devsyncReviewInbox(config)
	}
	return true
}

// devsyncPickSettingsTarget asks which paired device gets the settings.
func devsyncPickSettingsTarget(config *Config) {
	list := peerPairedList()
	switch {
	case len(list) == 0:
		ShowToast("Pair a device first", ToastKindInfo)
		return
	case len(list) == 1:
		devsyncConfirmSend(config, list[0])
		return
	}
	var buttons []tbxSheetButton
	for i, p := range list {
		if i >= 3 {
			break
		}
		rec := p
		buttons = append(buttons, tbxSheetButton{label: rec.Name, run: func() { devsyncConfirmSend(config, rec) }})
	}
	buttons = append(buttons, tbxSheetButton{label: "Cancel"})
	tbxOpenSheet(config, tbxSheetSpec{
		title:   "Send my settings to…",
		icon:    "upload",
		body:    "Choose the paired device that should look and feel like this one.",
		buttons: buttons,
	})
}

// devsyncConfirmSend asks once more, then sends.
func devsyncConfirmSend(config *Config, rec peerRecord) {
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Send my settings to " + rec.Name + "?",
		subtitle: "One way: " + rec.Name + "'s user chooses to use them there",
		icon:     "upload",
		body: "## What goes\nTheme and colours, wallpaper, Home layout and tabs, language and typeface, text size, " +
			"interface sounds and music, and the Accessibility settings.\n\n" +
			"## What never goes\nWi-Fi, accounts and passwords, the name Home greets, power and brightness, " +
			"the button-layout fix and anything else that belongs to one device.",
		buttons: []tbxSheetButton{
			{label: "Cancel"},
			{label: "Send", primary: true, run: func() { devsyncStartSend(config, rec) }},
		},
		focus: 1,
	})
}

// devsyncStartSend sends the settings in the background.
func devsyncStartSend(config *Config, rec peerRecord) {
	if !peerEnabledFlag.Load() {
		ShowToast("Turn on Nearby devices first", ToastKindWarning)
		return
	}
	doc := devsyncCaptureSettings(config)
	ctx, ok := ssyncWorkStart("Sending settings to " + rec.Name + "…")
	if !ok {
		ShowToast("Wait for the current job to finish", ToastKindInfo)
		return
	}
	go func() {
		err := devsyncSendSettings(ctx, rec.ID, doc)
		tbxPost(func() {
			ssyncWorkEnd()
			ssyncScreen.model.builtAt = time.Time{}
			if err != nil {
				var se *peerStatusError
				if errors.As(err, &se) && se.code == http.StatusNotFound {
					ShowToast(rec.Name+" has an older JukaHub — update it to receive settings", ToastKindWarning)
					return
				}
				ShowToast("Could not send: "+peerErrorText(err, rec.Name), ToastKindWarning)
				return
			}
			ssyncScreen.resultLine = "Settings sent to " + rec.Name + " — they are used there once its user says so"
			ShowToast("Settings sent to "+rec.Name, ToastKindSuccess)
		})
	}()
}

// devsyncReviewInbox shows the waiting settings with Use them / Dismiss.
func devsyncReviewInbox(config *Config) {
	box := devsyncInboxGet()
	if box == nil {
		return
	}
	n := len(box.doc.Keys) + len(box.doc.Unset)
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Use " + box.from + "'s settings?",
		subtitle: pluralizeCount(n, "setting", "settings") + " plus colours and wallpaper",
		icon:     "download",
		body: "Theme and colours, wallpaper, Home layout and tabs, language, text size, interface sounds and " +
			"Accessibility become the same as on " + box.from + ".\n\n" +
			"Wi-Fi, accounts, passwords and this device's own hardware settings stay as they are. " +
			"A wallpaper or theme file that is not on this card is left as it was.",
		buttons: []tbxSheetButton{
			{label: "Dismiss", run: func() { devsyncInboxState.box = nil; ssyncScreen.model.builtAt = time.Time{} }},
			{label: "Use them", primary: true, run: func() {
				changed := devsyncApplySettings(config, box.doc)
				devsyncInboxState.box = nil
				ssyncScreen.model.builtAt = time.Time{}
				ShowToast("Using "+box.from+"'s settings · "+pluralizeCount(changed, "setting", "settings")+" changed", ToastKindSuccess)
			}},
		},
		focus: 1,
	})
}
