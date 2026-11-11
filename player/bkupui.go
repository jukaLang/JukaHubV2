package main

// bkupui.go — the Backup & restore screen.
//
// One row backs everything up: the settings, the library, themes, plugins,
// input profiles and packs (and the game saves when that switch is on, with
// their size beside it) go into one .jkbackup on the card or in Downloads. The
// backups found there are listed below it, newest first; A on one checks every
// file in it and offers its categories, each with a switch and what it holds,
// and "Restore selected" (pressed twice) writes a safety backup of what it is
// about to replace before it puts anything back. A weekly backup, keeping the
// three newest, is one switch away.
//
// Pad: D-pad moves, A presses or switches, Right goes to the card's buttons,
// Y backs up now, B goes back (out of a restore first). Keys: arrows, Enter,
// Y, Esc/Backspace. Mouse and touch: tap a row, a switch or a button.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// bkupSceneName is the screen's scene, injected in memory (xpackroute.go).
const bkupSceneName = "Backup"

// bkupData is what the screen shows that is read or written off the render
// thread.
var bkupData struct {
	mu        sync.Mutex
	gen       int
	listing   bool
	listed    bool
	list      []bkupListItem
	measuring bool
	sizes     map[string]bkupSize
	busy      bool
	busyLabel string
	done      int
	total     int
	verifying bool
	verified  bool
	verifyErr string
	verifyFor string
	manifest  bkupManifest
	summary   []bkupCatSummary
	exportDir string
	safetyDir string
	result    *bkupRestoreResult
}

// bkupViewKey is what the card's buttons and text were built for.
type bkupViewKey struct {
	stage, gen int
	id         string
	width      int32
}

// bkupUI is the screen's focus, choices and last frame's geometry.
var bkupUI struct {
	visit       context.Context
	stage       int // 0 the main list, 1 restoring one backup
	gen         int
	rowsStage   int
	list        xpackKitList
	rows        []xpackKitRow
	inPane      bool
	button      int
	buttons     []xpackKitButton
	buttonRects []sdl.Rect
	buttonsKey  bkupViewKey
	paneKey     bkupViewKey
	paneTitle   string
	paneSub     string
	paneLines   []string
	saves       bool
	plugins     bool
	choicesSet  bool
	file        bkupListItem
	cats        map[string]bool
	confirmAt   time.Time
}

// bkupOpenScreen opens Backup & restore (the Tools hub and Settings call it).
func bkupOpenScreen(config *Config) {
	if config == nil {
		return
	}
	xpackEnsurePlatformScenes(config)
	idx := sceneIndexExact(config, bkupSceneName)
	if idx < 0 {
		return
	}
	if idx != currentSceneIndex {
		changeSceneTo(config, idx)
	}
}

// bkupEnsureVisit lists the backups and sizes everything when the screen is
// (re)entered.
func bkupEnsureVisit(c *Config) {
	ctx := sceneWorkContext()
	if ctx == bkupUI.visit {
		return
	}
	bkupUI.visit = ctx
	bkupUI.inPane, bkupUI.button = false, 0
	if !bkupUI.choicesSet {
		bkupUI.plugins, bkupUI.choicesSet = true, true
	}
	bkupData.mu.Lock()
	busy := bkupData.busy
	bkupData.mu.Unlock()
	if !busy {
		bkupUI.stage = 0
	}
	if currentSceneWorkName() == bkupSceneName {
		bkupRefreshAsync()
	}
	bkupUI.gen = -1
}

// bkupRefreshAsync re-lists the backups and re-measures the categories.
func bkupRefreshAsync() {
	bkupData.mu.Lock()
	if bkupData.listing {
		bkupData.mu.Unlock()
		return
	}
	bkupData.listing, bkupData.measuring = true, true
	bkupData.gen++
	bkupData.mu.Unlock()
	go func() {
		list := bkupList()
		dir, safety := bkupExportDir(), bkupSafetyDir()
		bkupData.mu.Lock()
		bkupData.list, bkupData.listing, bkupData.listed = list, false, true
		bkupData.exportDir, bkupData.safetyDir = dir, safety
		bkupData.gen++
		bkupData.mu.Unlock()
		sizes := bkupMeasure(bkupOptions{PluginFiles: true})
		bkupData.mu.Lock()
		bkupData.sizes, bkupData.measuring = sizes, false
		bkupData.gen++
		bkupData.mu.Unlock()
	}()
}

// bkupSnapshot is the shared state as the render thread needs it.
type bkupSnapshot struct {
	gen                 int
	listing, measuring  bool
	list                []bkupListItem
	sizes               map[string]bkupSize
	busy                bool
	busyLabel           string
	done, total         int
	verifying, verified bool
	verifyErr           string
	verifyFor           string
	manifest            bkupManifest
	summary             []bkupCatSummary
	exportDir           string
	safetyDir           string
}

func bkupSnap() bkupSnapshot {
	bkupData.mu.Lock()
	defer bkupData.mu.Unlock()
	return bkupSnapshot{
		gen:       bkupData.gen,
		listing:   bkupData.listing && !bkupData.listed,
		measuring: bkupData.measuring,
		list:      bkupData.list,
		sizes:     bkupData.sizes,
		busy:      bkupData.busy,
		busyLabel: bkupData.busyLabel,
		done:      bkupData.done,
		total:     bkupData.total,
		verifying: bkupData.verifying,
		verified:  bkupData.verified,
		verifyErr: bkupData.verifyErr,
		verifyFor: bkupData.verifyFor,
		manifest:  bkupData.manifest,
		summary:   bkupData.summary,
		exportDir: bkupData.exportDir,
		safetyDir: bkupData.safetyDir,
	}
}

// bkupWhere is the backups folder as the screen names it: the one the last
// listing found, read off the render thread.
func bkupWhere(s bkupSnapshot) string {
	if s.exportDir == "" {
		return "the backups folder"
	}
	return s.exportDir
}

// ── Rows ──────────────────────────────────────────────────────────────────

// bkupBackupCats are the categories "Back up now" writes.
func bkupBackupCats() map[string]bool {
	cats := map[string]bool{}
	for _, cat := range bkupCategoryOrder {
		cats[cat] = cat != bkupCatSaves || bkupUI.saves
	}
	return cats
}

// bkupBuildRows rebuilds the list for the stage.
func bkupBuildRows(c *Config, s bkupSnapshot) []xpackKitRow {
	if bkupUI.stage == 1 {
		return bkupRestoreRows(s)
	}
	var rows []xpackKitRow
	rows = append(rows, xpackKitRow{Kind: xpackKitHeading, Label: "Back up"})
	now := xpackKitRow{Kind: xpackKitItem, ID: "now", Label: "Back up now", Detail: "Everything but your games and media, in one file", Tone: xpackKitToneAccent}
	if s.busy {
		now.Label, now.Disabled = s.busyLabel, true
		if s.total > 0 {
			now.Detail = fmt.Sprintf("%d of %d files", s.done, s.total)
		}
	} else if !s.measuring && s.sizes != nil {
		total := int64(0)
		for cat, size := range s.sizes {
			if cat != bkupCatSaves || bkupUI.saves {
				total += size.Bytes
			}
		}
		now.Value = "about " + humanBytes(total)
	}
	rows = append(rows, now)
	saves := xpackKitRow{Kind: xpackKitItem, ID: "saves", Label: "Include game saves", Switch: true, On: bkupUI.saves, Detail: "Measuring your save folders…"}
	if !s.measuring && s.sizes != nil {
		size := s.sizes[bkupCatSaves]
		saves.Detail = xpackKitCount(size.Files, "file", "files") + " · " + humanBytes(size.Bytes)
		if size.Files == 0 {
			saves.Detail = "No save folders found on this device"
		}
	}
	rows = append(rows,
		saves,
		xpackKitRow{Kind: xpackKitItem, ID: "plugins", Label: "Include plugin scripts", Switch: true, On: bkupUI.plugins,
			Detail: "Plugin manifests always come along; programs built for one system never do"},
		xpackKitRow{Kind: xpackKitItem, ID: "auto", Label: "Weekly automatic backup", Switch: true, On: bkupAutoEnabled(c),
			Detail: "Keeps the three newest, without saves"})
	rows = append(rows, xpackKitRow{Kind: xpackKitHeading, Label: "Restore from"})
	switch {
	case s.listing:
		rows = append(rows, xpackKitRow{Kind: xpackKitItem, Label: "Looking for backups…", Disabled: true})
	case len(s.list) == 0:
		rows = append(rows, xpackKitRow{Kind: xpackKitItem, ID: "none", Label: "No backups found yet",
			Detail: "Backups are saved in " + bkupWhere(s)})
	}
	for _, it := range s.list {
		row := xpackKitRow{Kind: xpackKitItem, ID: "file:" + it.Path, Label: it.ModTime.Format("Jan 2, 2006 · 15:04")}
		if it.Problem != "" {
			row.Detail, row.Tone, row.Disabled = it.Name+" · can't use it: "+it.Problem, xpackKitToneWarn, true
		} else {
			kind := ""
			switch {
			case it.Safety:
				kind = "safety backup · "
			case it.Manifest.Label == "weekly":
				kind = "weekly · "
			}
			row.Label += " · " + it.Manifest.DeviceName
			row.Detail = kind + humanBytes(it.Size) + " · " + it.Name
			row.Value = it.Manifest.AppVersion
		}
		rows = append(rows, row)
	}
	return rows
}

// bkupRestoreRows lists the restore of the chosen backup: its check, its
// categories with switches, and the button row.
func bkupRestoreRows(s bkupSnapshot) []xpackKitRow {
	rows := []xpackKitRow{{Kind: xpackKitHeading, Label: "Restore " + bkupUI.file.ModTime.Format("Jan 2 · 15:04")}}
	check := xpackKitRow{Kind: xpackKitItem, ID: "check", Label: "Checking every file…", Detail: bkupUI.file.Name, Disabled: true}
	switch {
	case s.verifyErr != "":
		check.Label, check.Detail, check.Tone = "This backup cannot be restored", s.verifyErr, xpackKitToneBad
	case s.verified:
		check.Label = "Checked: " + xpackKitCount(len(s.manifest.Files), "file", "files") + ", all intact"
		check.Detail, check.Tone = "Made by "+s.manifest.AppVersion+" on "+s.manifest.DeviceName, xpackKitToneGood
	}
	rows = append(rows, check)
	if !s.verified {
		return rows
	}
	// The per-category counts were worked out with the check, off the render
	// thread: placing the save folders reads the emulators' configs.
	for _, sum := range s.summary {
		detail := xpackKitCount(sum.Files, "file", "files") + " · " + humanBytes(sum.Bytes)
		if sum.Skipped > 0 {
			detail += " · " + xpackKitCount(sum.Skipped, "stays out", "stay out") + ": " + sum.Reason
		}
		rows = append(rows, xpackKitRow{Kind: xpackKitItem, ID: "cat:" + sum.Category, Label: bkupCategoryLabel(sum.Category),
			Detail: detail, Switch: true, On: bkupUI.cats[sum.Category], Disabled: sum.Files == 0 || s.busy})
	}
	run := xpackKitRow{Kind: xpackKitItem, ID: "go", Label: "Restore selected", Detail: "A safety backup of what it replaces is saved first", Tone: xpackKitToneAccent}
	if s.busy {
		run.Label, run.Disabled = s.busyLabel, true
	}
	return append(rows, run)
}

// bkupAllCats is every category switched on (for sizing a plan).
func bkupAllCats() map[string]bool {
	cats := map[string]bool{}
	for _, cat := range bkupCategoryOrder {
		cats[cat] = true
	}
	return cats
}

// bkupSyncRows rebuilds the rows when the data or the stage changed.
func bkupSyncRows(c *Config) {
	s := bkupSnap()
	if s.gen == bkupUI.gen && bkupUI.rowsStage == bkupUI.stage {
		return
	}
	keep := ""
	if row, ok := bkupUI.list.focused(bkupUI.rows); ok && bkupUI.rowsStage == bkupUI.stage {
		keep = row.ID
	}
	bkupUI.gen, bkupUI.rowsStage = s.gen, bkupUI.stage
	bkupUI.rows = bkupBuildRows(c, s)
	if keep != "" {
		for i, r := range bkupUI.rows {
			if r.ID == keep && r.Kind == xpackKitItem {
				bkupUI.list.cursor = i
			}
		}
	}
	bkupUI.list.clamp(bkupUI.rows)
	bkupUI.buttonsKey = bkupViewKey{gen: -1}
	bkupUI.paneKey = bkupViewKey{gen: -1}
}

// bkupBump marks the data changed so the rows rebuild.
func bkupBump() {
	bkupData.mu.Lock()
	bkupData.gen++
	bkupData.mu.Unlock()
}

// ── Actions ───────────────────────────────────────────────────────────────

// bkupFocusedID is the focused row's id.
func bkupFocusedID() string {
	if row, ok := bkupUI.list.focused(bkupUI.rows); ok {
		return row.ID
	}
	return ""
}

// bkupBackupNow writes a backup of everything chosen to the card or Downloads.
func bkupBackupNow(c *Config) {
	bkupData.mu.Lock()
	if bkupData.busy {
		bkupData.mu.Unlock()
		ShowToast("A backup or restore is already running", ToastKindInfo)
		return
	}
	bkupData.busy, bkupData.busyLabel, bkupData.done, bkupData.total = true, "Backing up…", 0, 0
	bkupData.gen++
	bkupData.mu.Unlock()
	vars := cfgdocSnapshot(c)
	inputVars := cfgdocInputVars(c, nil)
	version, dir := versionString(c), bkupExportDir()
	cats, opts := bkupBackupCats(), bkupOptions{PluginFiles: bkupUI.plugins, Saves: bkupUI.saves}
	PlayActivateSound()
	go func() {
		rep := cfgdocReportFor(vars, inputVars, version)
		var doc []byte
		if rep.Err == nil {
			doc, _ = cfgdocMarshal(rep.Doc)
		}
		progress := func(done, total int) {
			bkupData.mu.Lock()
			bkupData.done, bkupData.total = done, total
			bkupData.gen++
			bkupData.mu.Unlock()
		}
		path, m, err := bkupCreate(context.Background(), dir, bkupFileName("manual", time.Now()), cats, opts, "manual", version, doc, progress)
		bkupData.mu.Lock()
		bkupData.busy = false
		bkupData.gen++
		bkupData.mu.Unlock()
		if err != nil {
			ShowToast("The backup failed: "+firstLine(err.Error()), ToastKindWarning)
			return
		}
		trophyEvent(tkBackup)
		ShowToast("Saved "+xpackClip(bkupBaseName(path), 60)+" · "+humanBytes(m.TotalBytes)+" in "+dir, ToastKindSuccess)
		bkupRefreshAsync()
	}()
}

// bkupBaseName is the last element of a path, for toasts.
func bkupBaseName(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[i+1:]
	}
	return path
}

// bkupToggle flips one of the main view's switches.
func bkupToggle(c *Config, id string) {
	switch id {
	case "saves":
		bkupUI.saves = !bkupUI.saves
	case "plugins":
		bkupUI.plugins = !bkupUI.plugins
	case "auto":
		if c.Variables.Custom == nil {
			c.Variables.Custom = map[string]interface{}{}
		}
		on := !bkupAutoEnabled(c)
		c.Variables.Custom[bkupAutoKey] = on
		saveUserConfigDebounced(userConfigFromVariables(c))
		if on {
			ShowToast("A backup will be made every week (the three newest are kept)", ToastKindInfo)
			bkupMaybeAutoBackup(c)
		}
	default:
		return
	}
	PlayToggleSound()
	bkupBump()
}

// bkupOpenRestore enters the restore of a backup and checks it off the render
// thread.
func bkupOpenRestore(id string) {
	path := strings.TrimPrefix(id, "file:")
	s := bkupSnap()
	for _, it := range s.list {
		if it.Path != path || it.Problem != "" {
			continue
		}
		bkupUI.file = it
		bkupUI.stage = 1
		bkupUI.inPane = false
		bkupUI.list.cursor, bkupUI.list.top = 0, 0
		bkupUI.cats = map[string]bool{}
		for _, cat := range it.Manifest.Categories {
			bkupUI.cats[cat] = cat != bkupCatSaves
		}
		bkupData.mu.Lock()
		bkupData.verifying, bkupData.verified, bkupData.verifyErr, bkupData.verifyFor = true, false, "", path
		bkupData.summary = nil
		bkupData.gen++
		bkupData.mu.Unlock()
		PlayActivateSound()
		go func() {
			m, err := bkupVerify(path)
			var summary []bkupCatSummary
			if err == nil {
				summary = bkupSummarize(bkupPlanRestore(path, m, bkupAllCats()))
			}
			bkupData.mu.Lock()
			defer bkupData.mu.Unlock()
			if bkupData.verifyFor != path {
				return
			}
			bkupData.verifying = false
			if err != nil {
				bkupData.verifyErr = firstLine(err.Error())
			} else {
				bkupData.verified, bkupData.manifest, bkupData.summary = true, m, summary
			}
			bkupData.gen++
		}()
		return
	}
}

// bkupRestoreNow restores the chosen categories (pressed twice): the files go
// back off the render thread, then bkupPoll merges and reloads.
func bkupRestoreNow(c *Config) {
	s := bkupSnap()
	if !s.verified || s.busy {
		return
	}
	chosen := 0
	for _, on := range bkupUI.cats {
		if on {
			chosen++
		}
	}
	if chosen == 0 {
		ShowToast("Switch on at least one category to restore", ToastKindInfo)
		return
	}
	if time.Since(bkupUI.confirmAt) > 3*time.Second {
		bkupUI.confirmAt = time.Now()
		PlayNavSound()
		ShowToast("Press again to restore over your current setup (a safety backup is made first)", ToastKindInfo)
		return
	}
	bkupUI.confirmAt = time.Time{}
	cats := map[string]bool{}
	for cat, on := range bkupUI.cats {
		cats[cat] = on
	}
	path, manifest := bkupUI.file.Path, s.manifest
	vars := cfgdocSnapshot(c)
	inputVars := cfgdocInputVars(c, nil)
	version := versionString(c)
	bkupData.mu.Lock()
	bkupData.busy, bkupData.busyLabel = true, "Restoring…"
	bkupData.gen++
	bkupData.mu.Unlock()
	PlayActivateSound()
	go func() {
		plan := bkupPlanRestore(path, manifest, cats)
		rep := cfgdocReportFor(vars, inputVars, version)
		var doc []byte
		if rep.Err == nil {
			doc, _ = cfgdocMarshal(rep.Doc)
		}
		res := bkupRestoreFiles(context.Background(), plan, cats, version, doc)
		bkupData.mu.Lock()
		bkupData.result = &res
		bkupData.gen++
		bkupData.mu.Unlock()
	}()
}

// bkupPoll finishes a restore on the main thread once its files are written.
func bkupPoll(c *Config) {
	bkupData.mu.Lock()
	res := bkupData.result
	bkupData.result = nil
	bkupData.mu.Unlock()
	if res == nil {
		return
	}
	defer func() {
		bkupData.mu.Lock()
		bkupData.busy = false
		bkupData.gen++
		bkupData.mu.Unlock()
		bkupRefreshAsync()
	}()
	if res.Err != nil {
		ShowToast("Restore stopped: "+firstLine(res.Err.Error()), ToastKindWarning)
		return
	}
	if err := bkupCommit(c, *res); err != nil {
		ShowToast("Restore stopped: "+firstLine(err.Error()), ToastKindWarning)
		return
	}
	n := res.Written
	if res.HasUser {
		n++
	}
	msg := "Restored " + xpackKitCount(n, "file", "files") + " · safety backup saved"
	if len(res.Skipped) > 0 {
		msg += fmt.Sprintf(" · %d left out", len(res.Skipped))
		LogScene("bkup").Info("restore left files out", "files", strings.Join(res.Skipped, "; "))
	}
	trophyEvent(tkRestore)
	ShowToast(msg, ToastKindSuccess)
	bkupUI.stage = 0
	bkupUI.list.cursor, bkupUI.list.top = 0, 0
}

// bkupPaneButtons is the card's buttons for the focused row.
func bkupPaneButtons() {
	id := bkupFocusedID()
	s := bkupSnap()
	key := bkupViewKey{stage: bkupUI.stage, gen: s.gen, id: id}
	if key == bkupUI.buttonsKey {
		return
	}
	bkupUI.buttonsKey = key
	var b []xpackKitButton
	if bkupUI.stage == 1 {
		b = append(b,
			xpackKitButton{ID: "go", Label: "Restore selected", Primary: true, Disabled: !s.verified || s.busy},
			xpackKitButton{ID: "back", Label: "Choose another"})
	} else {
		b = append(b, xpackKitButton{ID: "now", Label: "Back up now", Primary: true, Disabled: s.busy})
		if strings.HasPrefix(id, "file:") {
			b = append(b, xpackKitButton{ID: "restore", Label: "Restore from it…", Disabled: s.busy})
		}
	}
	bkupUI.buttons = b
	if bkupUI.button >= len(b) {
		bkupUI.button = 0
	}
}

// bkupActivate presses row id (A, Enter or a tap).
func bkupActivate(c *Config, id string) {
	switch {
	case id == "now":
		bkupBackupNow(c)
	case id == "saves", id == "plugins", id == "auto":
		bkupToggle(c, id)
	case strings.HasPrefix(id, "file:"):
		bkupOpenRestore(id)
	case strings.HasPrefix(id, "cat:"):
		if s := bkupSnap(); s.busy {
			return
		}
		cat := strings.TrimPrefix(id, "cat:")
		bkupUI.cats[cat] = !bkupUI.cats[cat]
		PlayToggleSound()
		bkupBump()
	case id == "go":
		bkupRestoreNow(c)
	}
}

// bkupActivateButton presses card button i.
func bkupActivateButton(c *Config, i int) {
	if i < 0 || i >= len(bkupUI.buttons) || bkupUI.buttons[i].Disabled {
		return
	}
	switch bkupUI.buttons[i].ID {
	case "now":
		bkupBackupNow(c)
	case "restore":
		bkupOpenRestore(bkupFocusedID())
	case "go":
		bkupRestoreNow(c)
	case "back":
		bkupBack(c)
	}
}

// bkupPress is A on the list or the card.
func bkupPress(c *Config) {
	if bkupUI.inPane {
		bkupActivateButton(c, bkupUI.button)
		return
	}
	if row, ok := bkupUI.list.focused(bkupUI.rows); ok && !row.Disabled {
		bkupActivate(c, row.ID)
	}
}

// bkupBack is B: buttons → list → the main list → off the screen.
func bkupBack(c *Config) {
	switch {
	case bkupUI.inPane:
		bkupUI.inPane = false
		PlayBackSound()
	case bkupUI.stage == 1:
		if s := bkupSnap(); s.busy {
			ShowToast("The restore is still running", ToastKindInfo)
			return
		}
		bkupUI.stage = 0
		bkupUI.list.cursor, bkupUI.list.top = 0, 0
		bkupData.mu.Lock()
		bkupData.verifyFor = ""
		bkupData.gen++
		bkupData.mu.Unlock()
		PlayBackSound()
	default:
		goBackScene(c)
	}
}

// bkupMove steps the focus in the list or between the card's buttons.
func bkupMove(dx, dy int) {
	if bkupUI.inPane {
		next, moved := xpackKitMoveButton(bkupUI.button, len(bkupUI.buttons), dx, dy)
		switch {
		case moved:
			bkupUI.button = next
			PlayNavSound()
		case dx < 0 && bkupUI.button%2 == 0:
			bkupUI.inPane = false
			PlayNavSound()
		}
		return
	}
	switch {
	case dx > 0:
		if len(bkupUI.buttons) > 0 {
			bkupUI.inPane, bkupUI.button = true, 0
			PlayNavSound()
		}
	case dy < 0:
		if bkupUI.list.atFirst(bkupUI.rows) {
			focusEngine.ClearFocus()
			focusChromeBar()
			return
		}
		if bkupUI.list.move(bkupUI.rows, -1) {
			PlayNavSound()
		}
	case dy > 0:
		if bkupUI.list.move(bkupUI.rows, 1) {
			PlayNavSound()
		}
	}
}

// ── Input ─────────────────────────────────────────────────────────────────

func bkupKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	bkupEnsureVisit(c)
	switch e.Keysym.Sym {
	case sdl.K_UP:
		bkupMove(0, -1)
	case sdl.K_DOWN:
		bkupMove(0, 1)
	case sdl.K_LEFT:
		bkupMove(-1, 0)
	case sdl.K_RIGHT:
		bkupMove(1, 0)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		bkupPress(c)
	case sdl.K_y:
		if bkupUI.stage == 0 {
			bkupBackupNow(c)
		}
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		bkupBack(c)
	default:
		return false
	}
	return true
}

func bkupPad(e *sdl.ControllerButtonEvent, c *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_START, sdl.CONTROLLER_BUTTON_GUIDE,
		sdl.CONTROLLER_BUTTON_LEFTSTICK, sdl.CONTROLLER_BUTTON_RIGHTSTICK:
		return false
	}
	bkupEnsureVisit(c)
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		bkupMove(0, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		bkupMove(0, 1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		bkupMove(-1, 0)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		bkupMove(1, 0)
	case sdl.CONTROLLER_BUTTON_A:
		bkupPress(c)
	case sdl.CONTROLLER_BUTTON_Y:
		if bkupUI.stage == 0 {
			bkupBackupNow(c)
		}
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		bkupBack(c)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		if !bkupUI.inPane && bkupUI.list.move(bkupUI.rows, -5) {
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		if !bkupUI.inPane && bkupUI.list.move(bkupUI.rows, 5) {
			PlayNavSound()
		}
	}
	return true
}

// bkupMouse handles a click or a tap.
func bkupMouse(mx, my int32, c *Config) bool {
	bkupEnsureVisit(c)
	if i := xpackKitButtonHit(bkupUI.buttonRects, mx, my); i >= 0 {
		bkupUI.inPane, bkupUI.button = true, i
		bkupActivateButton(c, i)
		return true
	}
	i, onSwitch := bkupUI.list.hit(mx, my)
	if i < 0 || !xpackKitFocusable(bkupUI.rows, i) {
		return false
	}
	again := i == bkupUI.list.cursor && !bkupUI.inPane
	bkupUI.list.cursor, bkupUI.inPane = i, false
	row := bkupUI.rows[i]
	switch {
	case row.Disabled:
	case onSwitch || again || row.Switch:
		bkupActivate(c, row.ID)
	default:
		PlayNavSound()
	}
	return true
}

// bkupWheel scrolls the list.
func bkupWheel(dy int32) bool {
	bkupUI.list.wheel(bkupUI.rows, dy)
	return true
}

// bkupHints is the screen's footer.
func bkupHints() []FooterHint {
	if bkupUI.inPane {
		return []FooterHint{{Button: "D-Pad", Label: "Choose"}, {Button: "A", Label: "Select"}, {Button: "B", Label: "List"}}
	}
	if bkupUI.stage == 1 {
		return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Switch"}, {Button: "B", Label: "Backups"}}
	}
	return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Select"}, {Button: "Y", Label: "Back up now"}, {Button: "B", Label: "Back"}}
}

// ── Drawing ───────────────────────────────────────────────────────────────

// bkupRender draws the screen.
func bkupRender(renderer *sdl.Renderer, c *Config) {
	bkupEnsureVisit(c)
	bkupPoll(c)
	bkupSyncRows(c)
	bkupPaneButtons()
	f, ok := xpackKitLoadFonts(c)
	if !ok {
		drawAppBackdrop(renderer, c, screenWidth, screenHeight)
		xpackKitFinish(renderer, c)
		return
	}
	header := UIPanelHeader("Backup & restore", "Your whole setup in one file — no cloud, no account", "backup")
	contentY, bottom, margin := xpackKitFrame(renderer, c, bkupSceneName, header)
	listW := (screenWidth - 2*margin) * 58 / 100
	area := sdl.Rect{X: margin, Y: contentY, W: listW, H: bottom - contentY}
	ring := !bkupUI.inPane && !xpackKitChrome()
	xpackKitDrawList(renderer, c, f, &bkupUI.list, bkupUI.rows, area, ring)
	paneX := margin + listW + 18
	bkupDrawPane(renderer, c, f, sdl.Rect{X: paneX, Y: contentY, W: screenWidth - margin - paneX, H: bottom - contentY})
	xpackKitFinish(renderer, c)
}

// bkupDrawPane draws the card for the focused row. Its text is rebuilt when the
// focus, the stage, the data or the width changes.
func bkupDrawPane(renderer *sdl.Renderer, c *Config, f xpackKitFonts, area sdl.Rect) {
	bkupUI.buttonRects = bkupUI.buttonRects[:0]
	if area.W <= 80 || area.H <= 80 {
		return
	}
	UICard(renderer, area, DefaultUICardStyle())
	const pad = int32(20)
	x, w := area.X+pad, area.W-2*pad
	y := area.Y + 16
	bh := max(int32(f.body.Height())+20, 44)
	rows := int32((len(bkupUI.buttons) + 1) / 2)
	buttonsY := area.Y + area.H - pad - rows*(bh+10) + 10
	limit := buttonsY - 10
	id := bkupFocusedID()
	s := bkupSnap()
	if key := (bkupViewKey{stage: bkupUI.stage, gen: s.gen, id: id, width: w}); key != bkupUI.paneKey {
		bkupUI.paneKey = key
		title, sub, body := bkupPaneText(id, s)
		bkupUI.paneTitle, bkupUI.paneSub = title, sub
		bkupUI.paneLines = bkupUI.paneLines[:0]
		for _, para := range body {
			if para == "" {
				bkupUI.paneLines = append(bkupUI.paneLines, "")
				continue
			}
			bkupUI.paneLines = append(bkupUI.paneLines, wrapTextLines(f.body, para, w, 4)...)
		}
	}
	y = xpackKitText(renderer, f.big, bkupUI.paneTitle, x, y, w, ColorTextPrimary()) + 2
	y = xpackKitText(renderer, f.small, bkupUI.paneSub, x, y, w, ColorTextTertiary()) + 12
	lineH := int32(f.body.Height()) + 2
	for _, l := range bkupUI.paneLines {
		if l == "" {
			y += lineH / 2
			continue
		}
		if y+lineH > limit {
			break
		}
		drawText(renderer, f.body, l, x, y, ColorTextSecondary(), textAlignLeft)
		y += lineH
	}
	if s.busy && s.total > 0 && y+12 < limit {
		UIProgressBar(renderer, sdl.Rect{X: x, Y: y + 4, W: w, H: 8}, float64(s.done)/float64(s.total), glassA(20), glassAccent())
	}
	focus := -1
	if bkupUI.inPane && !xpackKitChrome() {
		focus = bkupUI.button
	}
	xpackKitDrawButtons(renderer, c, f, bkupUI.buttons, &bkupUI.buttonRects, x, buttonsY, w, area.Y+area.H-pad+10, focus)
}

// bkupPaneText is the card's title, subtitle and paragraphs.
func bkupPaneText(id string, s bkupSnapshot) (string, string, []string) {
	switch {
	case bkupUI.stage == 1:
		m := bkupUI.file.Manifest
		if s.verified {
			m = s.manifest
		}
		body := []string{
			"Made " + bkupUI.file.ModTime.Format("Monday, Jan 2 2006 at 15:04") + " by " + m.AppVersion + " on " + m.DeviceName + ".",
			"",
		}
		if m.Platform != P().Name() {
			body = append(body, "It comes from "+bkupPlatformLabel(m.Platform)+": folders are translated to this device's, and files that only work there (the design file, installed packages, launcher overrides) stay out.", "")
		}
		safety := s.safetyDir
		if safety == "" {
			safety = "the backups folder"
		}
		body = append(body, "Before anything is replaced, what the chosen categories hold now is saved as a safety backup in "+safety+".",
			"", "Game saves are only restored when you switch them on.")
		return "Restore", bkupUI.file.Name, body
	case strings.HasPrefix(id, "file:"):
		path := strings.TrimPrefix(id, "file:")
		for _, it := range s.list {
			if it.Path != path {
				continue
			}
			body := []string{path, ""}
			if it.Problem != "" {
				body = append(body, "This file cannot be restored: "+it.Problem)
				return "Backup", it.Name, body
			}
			m := it.Manifest
			body = append(body, "Made "+it.ModTime.Format("Jan 2 2006, 15:04")+" by "+m.AppVersion+" on "+m.DeviceName+".")
			var cats []string
			for _, cat := range m.Categories {
				cats = append(cats, bkupCategoryLabel(cat))
			}
			body = append(body, "Holds: "+strings.Join(cats, ", ")+" · "+xpackKitCount(len(m.Files), "file", "files")+" · "+humanBytes(m.TotalBytes)+".")
			if m.Platform != P().Name() {
				body = append(body, "", "Made on "+bkupPlatformLabel(m.Platform)+"; it can still be restored here.")
			}
			return "Backup", it.Name, body
		}
	case id == "saves":
		return "Game saves", "Off unless you switch it on", []string{
			"Saves and states from the emulators' save folders this device has (RetroArch, PPSSPP, DraStic, Flycast).",
			"", "They can be large, so they only come along when you ask; a restore only puts them back when you switch them on there too.",
		}
	case id == "plugins":
		return "Plugin scripts", "The plugins folder", []string{
			"With this on, a plugin's whole folder is saved: its manifest, scripts and data files. Programs built for one system are always left out, so a backup made on Windows restores cleanly on the handheld.",
		}
	case id == "auto":
		return "Weekly backup", "Saved in " + bkupWhere(s), []string{
			"Once a week, the first time JukaHub starts after the last one is seven days old, it saves a backup of everything except the game saves and keeps the three newest weekly files.",
		}
	}
	body := []string{
		"One file holds your setup: every setting and the config document, the library (favourites, recents, playtime and trophies, alarms, notes, saved chat notes, the words the keyboard learned, bookmarks), themes, plugins, input profiles and experience packs. No games, no videos, no music.",
		"", "Copy it to any device and restore it there — Windows and the handheld read each other's backups.",
	}
	if s.sizes != nil {
		body = append(body, "")
		for _, cat := range bkupCategoryOrder {
			size := s.sizes[cat]
			if size.Files == 0 {
				continue
			}
			body = append(body, bkupCategoryLabel(cat)+": "+xpackKitCount(size.Files, "file", "files")+" · "+humanBytes(size.Bytes))
		}
	}
	return "Back up", "Saved in " + bkupWhere(s), body
}

// ── Scene registration ────────────────────────────────────────────────────

type bkupScene struct{ sceneBase }

func (bkupScene) render(r *sdl.Renderer, c *Config) bool {
	bkupRender(r, c)
	return true
}

func (bkupScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool { return bkupKey(e, c) }

func (bkupScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return bkupPad(e, c)
}

func init() { registerScene(bkupScene{sceneBase{name: bkupSceneName}}) }
