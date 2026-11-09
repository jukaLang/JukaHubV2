package main

// cfgdocui.go — the Config inspector: your config as a diff against what this
// version ships.
//
// Three views, switched with L1/R1 (Tab), the tab pills or a tap:
//
//   - Your changes: every setting that differs from the shipped defaults,
//     grouped by section (input profiles, layouts, theme, behaviour) with the
//     old → new value, plus the scenes of jukaconfig.json that differ from the
//     shipped ones. X resets the focused setting (press twice), Y exports the
//     whole document to the card or Downloads.
//   - Upstream: the defaults this version changed since you last looked, what
//     you use for each, and "use the new default"; Y says "got it".
//   - Import: the config documents on the card or in Downloads. A previews one:
//     every setting it would change, each with a switch (secrets and settings
//     from another kind of device are off or refused, with the reason), and Y
//     applies the ones that are on — after a backup of your settings.
//
// Everything is computed off the render thread from a snapshot of the settings
// taken on entry; nothing is read or measured per frame.

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

// cfgdocSceneName is the inspector's scene, injected in memory (xpackroute.go).
const cfgdocSceneName = "ConfigInspector"

// The inspector's views.
const (
	cfgdocTabChanges = iota
	cfgdocTabUpstream
	cfgdocTabImport
	cfgdocTabCount
)

var cfgdocTabLabels = [cfgdocTabCount]string{"Your changes", "Upstream", "Import"}

// cfgdocData is what the inspector shows, computed off the render thread.
var cfgdocData struct {
	mu          sync.Mutex
	seq         int // the newest refresh asked for
	gen         int // changes whenever anything below does
	loading     bool
	report      cfgdocReport
	record      cfgdocDefaultsRecord
	upstream    []cfgdocChange
	docs        []cfgdocDocFile
	plan        *cfgdocImportPlan
	importReady bool
	importErr   error
}

// cfgdocBusy is set while an export or an import is writing files.
var cfgdocBusy atomic.Bool

// cfgdocViewKey is what the card's buttons and text were built for: they are
// rebuilt when it changes, never per frame.
type cfgdocViewKey struct {
	tab, stage, gen int
	id              string
	width           int32
}

// cfgdocUI is the inspector's focus and last frame's geometry.
var cfgdocUI struct {
	visit       context.Context
	tab         int
	stage       int // Import: 0 lists the files, 1 previews one
	gen         int
	rowsTab     int
	rowsStage   int
	list        xpackKitList
	rows        []xpackKitRow
	inPane      bool
	button      int
	buttons     []xpackKitButton
	buttonRects []sdl.Rect
	buttonsKey  cfgdocViewKey
	paneKey     cfgdocViewKey
	paneTitle   string
	paneSub     string
	paneLines   []string
	tabRects    [cfgdocTabCount]sdl.Rect
	upCount     int
	upLabel     string
	statusN     int
	status      string
	confirmKey  string
	confirmAt   time.Time
}

// cfgdocOpenInspector opens the Config inspector (the Tools hub and Settings →
// About call it).
func cfgdocOpenInspector(config *Config) {
	if config == nil {
		return
	}
	xpackEnsurePlatformScenes(config)
	idx := sceneIndexExact(config, cfgdocSceneName)
	if idx < 0 {
		return
	}
	if idx != currentSceneIndex {
		changeSceneTo(config, idx)
	}
}

// cfgdocEnsureVisit starts the comparison when the screen is (re)entered.
func cfgdocEnsureVisit(c *Config) {
	ctx := sceneWorkContext()
	if ctx == cfgdocUI.visit {
		return
	}
	cfgdocUI.visit = ctx
	cfgdocUI.inPane, cfgdocUI.button, cfgdocUI.stage = false, 0, 0
	cfgdocUI.gen = -1
	// Only a real visit compares: a render test drawing the page must not
	// start work that reads the card and records the defaults.
	if currentSceneWorkName() == cfgdocSceneName {
		cfgdocRefreshAsync(c)
	}
}

// cfgdocRefreshAsync compares the settings with the shipped defaults off the
// render thread, from a snapshot taken here.
func cfgdocRefreshAsync(c *Config) {
	vars := cfgdocSnapshot(c)
	inputVars := cfgdocInputVars(c, nil)
	version := versionString(c)
	cfgdocData.mu.Lock()
	cfgdocData.seq++
	seq := cfgdocData.seq
	cfgdocData.loading = true
	cfgdocData.gen++
	cfgdocData.mu.Unlock()
	go func() {
		rep := cfgdocReportFor(vars, inputVars, version)
		rec, up := cfgdocUpstream(rep.Shipped)
		docs := cfgdocFindDocs()
		cfgdocData.mu.Lock()
		defer cfgdocData.mu.Unlock()
		if seq != cfgdocData.seq {
			return // a newer comparison is on its way
		}
		cfgdocData.report, cfgdocData.record, cfgdocData.upstream, cfgdocData.docs = rep, rec, up, docs
		cfgdocData.loading = false
		cfgdocData.gen++
	}()
}

// cfgdocSnapshotData copies what the render thread needs under the lock.
func cfgdocSnapshotData() (int, bool, cfgdocReport, cfgdocDefaultsRecord, []cfgdocChange, []cfgdocDocFile, *cfgdocImportPlan) {
	cfgdocData.mu.Lock()
	defer cfgdocData.mu.Unlock()
	return cfgdocData.gen, cfgdocData.loading, cfgdocData.report, cfgdocData.record, cfgdocData.upstream, cfgdocData.docs, cfgdocData.plan
}

// ── Rows ──────────────────────────────────────────────────────────────────

// cfgdocKindBadge is a change's badge and tone.
func cfgdocKindBadge(kind string) (string, int) {
	switch kind {
	case cfgdocAdded:
		return "Added", xpackKitToneGood
	case cfgdocRemoved:
		return "Removed", xpackKitToneWarn
	}
	return "Changed", xpackKitToneAccent
}

// cfgdocChangeDetail is a change's second line.
func cfgdocChangeDetail(ch cfgdocChange) string {
	switch ch.Kind {
	case cfgdocAdded:
		return "Yours: " + cfgdocShow(ch.New, ch.Secret)
	case cfgdocRemoved:
		return "Shipped: " + cfgdocShow(ch.Old, ch.Secret) + " · not in your config"
	}
	return cfgdocShow(ch.Old, ch.Secret) + "  →  " + cfgdocShow(ch.New, ch.Secret)
}

// cfgdocBuildRows rebuilds the list for the open view.
func cfgdocBuildRows(loading bool, rep cfgdocReport, rec cfgdocDefaultsRecord, up []cfgdocChange, docs []cfgdocDocFile, plan *cfgdocImportPlan) []xpackKitRow {
	var rows []xpackKitRow
	if loading && rep.Current == nil {
		return []xpackKitRow{{Kind: xpackKitItem, Label: "Comparing with the shipped defaults…", Disabled: true}}
	}
	if rep.Err != nil && cfgdocUI.tab != cfgdocTabImport {
		return []xpackKitRow{{Kind: xpackKitItem, Label: "The shipped defaults could not be read", Detail: firstLine(rep.Err.Error()), Disabled: true}}
	}
	switch cfgdocUI.tab {
	case cfgdocTabChanges:
		section := ""
		for _, ch := range rep.Changes {
			if ch.Section != section {
				section = ch.Section
				rows = append(rows, xpackKitRow{Kind: xpackKitHeading, Label: cfgdocSectionTitle(section)})
			}
			badge, tone := cfgdocKindBadge(ch.Kind)
			rows = append(rows, xpackKitRow{Kind: xpackKitItem, ID: "key:" + ch.Key, Label: ch.Key, Detail: cfgdocChangeDetail(ch), Badge: badge, Tone: tone})
		}
		if len(rep.Scenes.Names) > 0 || len(rep.Scenes.Removed) > 0 {
			rows = append(rows, xpackKitRow{Kind: xpackKitHeading, Label: "Scenes in jukaconfig.json"})
			for _, name := range rep.Scenes.Names {
				badge, tone := "Edited", xpackKitToneAccent
				if rep.Scenes.Added[name] {
					badge, tone = "Added", xpackKitToneGood
				}
				rows = append(rows, xpackKitRow{Kind: xpackKitItem, ID: "scene:" + name, Label: sceneDisplayName(name), Detail: name + " differs from the shipped layout", Badge: badge, Tone: tone})
			}
			for _, name := range rep.Scenes.Removed {
				rows = append(rows, xpackKitRow{Kind: xpackKitItem, ID: "scene:" + name, Label: sceneDisplayName(name), Detail: name + " is not in your design file", Badge: "Removed", Tone: xpackKitToneWarn})
			}
		}
		if len(rows) == 0 {
			rows = append(rows, xpackKitRow{Kind: xpackKitItem, ID: "none", Label: "Nothing changed", Detail: "Every setting matches what this version ships"})
		}
	case cfgdocTabUpstream:
		if len(up) == 0 {
			since := rep.Shipped.Version
			if rec.Version != "" {
				since = rec.Version
			}
			rows = append(rows, xpackKitRow{Kind: xpackKitItem, ID: "none", Label: "No default changed", Detail: "Since you last looked (v" + since + ")"})
			break
		}
		rows = append(rows, xpackKitRow{Kind: xpackKitHeading, Label: "Defaults changed since v" + rec.Version})
		for _, ch := range up {
			badge, tone := cfgdocKindBadge(ch.Kind)
			row := xpackKitRow{Kind: xpackKitItem, ID: "key:" + ch.Key, Label: ch.Key, Badge: badge, Tone: tone}
			row.Detail = "was " + cfgdocShow(ch.Old, ch.Secret) + " · now " + cfgdocShow(ch.New, ch.Secret)
			if cur, ok := rep.Current[ch.Key]; ok && !cfgdocEqual(cur, ch.New) {
				row.Value = "you: " + cfgdocShow(cur, ch.Secret)
			}
			rows = append(rows, row)
		}
	case cfgdocTabImport:
		if cfgdocUI.stage == 1 && plan != nil {
			return cfgdocPlanRows(plan)
		}
		for _, d := range docs {
			row := xpackKitRow{Kind: xpackKitItem, ID: "doc:" + d.Path, Label: d.Name}
			if d.Problem != "" {
				row.Detail, row.Tone, row.Disabled = "Can't use it: "+d.Problem, xpackKitToneWarn, true
			} else {
				from := d.Doc.Device.Name
				if from == "" {
					from = d.Doc.Device.Platform
				}
				row.Detail = "From " + from + " · " + d.Doc.GeneratedBy + " · " + d.ModTime.Format("Jan 2, 15:04")
				row.Value = xpackKitCount(len(cfgdocDocValues(d.Doc)), "setting", "settings")
			}
			rows = append(rows, row)
		}
		if len(rows) == 0 {
			rows = append(rows, xpackKitRow{Kind: xpackKitItem, ID: "none", Label: "No config files found",
				Detail: "Export one here, or copy a " + cfgdocFileName + " into " + bkupExportDir()})
		}
	}
	return rows
}

// cfgdocPlanRows lists an import preview: every setting with its switch.
func cfgdocPlanRows(plan *cfgdocImportPlan) []xpackKitRow {
	var rows []xpackKitRow
	if n := len(plan.Upstream); n > 0 {
		rows = append(rows, xpackKitRow{Kind: xpackKitItem, ID: "upstream", Label: "Made with older defaults",
			Detail: xpackKitCount(n, "default has", "defaults have") + " changed since; see Upstream", Tone: xpackKitToneWarn})
	}
	section := ""
	for i, it := range plan.Items {
		if it.Section != section {
			section = it.Section
			rows = append(rows, xpackKitRow{Kind: xpackKitHeading, Label: cfgdocSectionTitle(section)})
		}
		detail := cfgdocShow(it.Current, it.Secret) + "  →  " + cfgdocShow(it.Value, it.Secret)
		if it.Remove {
			detail = "back to the shipped " + cfgdocShow(it.Value, it.Secret)
		}
		if it.Note != "" {
			detail = it.Note
		}
		rows = append(rows, xpackKitRow{Kind: xpackKitItem, ID: "item:" + strconv.Itoa(i), Label: it.Key, Detail: detail,
			Switch: true, On: it.Accept && !it.Blocked, Disabled: it.Blocked})
	}
	if len(plan.Scenes) > 0 {
		rows = append(rows, xpackKitRow{Kind: xpackKitHeading, Label: "Scenes"})
		for i, sc := range plan.Scenes {
			detail := "Replaces or adds this screen in jukaconfig.json"
			if !plan.SameDevice {
				detail = "Laid out on another kind of device; off unless you turn it on"
			}
			rows = append(rows, xpackKitRow{Kind: xpackKitItem, ID: "scene:" + strconv.Itoa(i), Label: sc.Name, Detail: detail, Switch: true, On: sc.Accept})
		}
	}
	if len(plan.Items) == 0 && len(plan.Scenes) == 0 {
		rows = append(rows, xpackKitRow{Kind: xpackKitItem, ID: "none", Label: "Nothing to import", Detail: "Every setting in the file already matches yours"})
	}
	return rows
}

// cfgdocSyncRows rebuilds the rows when the data, the view or the stage
// changed.
func cfgdocSyncRows() {
	gen, loading, rep, rec, up, docs, plan := cfgdocSnapshotData()
	if gen == cfgdocUI.gen && cfgdocUI.rowsTab == cfgdocUI.tab && cfgdocUI.rowsStage == cfgdocUI.stage {
		return
	}
	keep := ""
	if row, ok := cfgdocUI.list.focused(cfgdocUI.rows); ok && cfgdocUI.rowsTab == cfgdocUI.tab && cfgdocUI.rowsStage == cfgdocUI.stage {
		keep = row.ID
	}
	cfgdocUI.gen, cfgdocUI.rowsTab, cfgdocUI.rowsStage = gen, cfgdocUI.tab, cfgdocUI.stage
	cfgdocUI.rows = cfgdocBuildRows(loading, rep, rec, up, docs, plan)
	if keep != "" {
		for i, r := range cfgdocUI.rows {
			if r.ID == keep && r.Kind == xpackKitItem {
				cfgdocUI.list.cursor = i
			}
		}
	}
	cfgdocUI.list.clamp(cfgdocUI.rows)
	cfgdocUI.buttonsKey = cfgdocViewKey{gen: -1}
	cfgdocUI.paneKey = cfgdocViewKey{gen: -1}
}

// ── Actions ───────────────────────────────────────────────────────────────

// cfgdocFocusedID is the focused row's id.
func cfgdocFocusedID() string {
	if row, ok := cfgdocUI.list.focused(cfgdocUI.rows); ok {
		return row.ID
	}
	return ""
}

// cfgdocConfirm reports whether key was pressed twice within three seconds.
func cfgdocConfirm(key string) bool {
	if cfgdocUI.confirmKey == key && time.Since(cfgdocUI.confirmAt) < 3*time.Second {
		cfgdocUI.confirmKey = ""
		return true
	}
	cfgdocUI.confirmKey, cfgdocUI.confirmAt = key, time.Now()
	return false
}

// cfgdocResetFocused puts the focused setting back to its shipped default.
func cfgdocResetFocused(c *Config) {
	id := cfgdocFocusedID()
	if !strings.HasPrefix(id, "key:") || cfgdocUI.tab != cfgdocTabChanges {
		return
	}
	key := strings.TrimPrefix(id, "key:")
	if ok, why := cfgdocImportable(key); !ok {
		ShowToast(key+" is "+why+"; it cannot be reset here", ToastKindInfo)
		return
	}
	if !cfgdocConfirm("reset:" + key) {
		PlayNavSound()
		ShowToast("Press again to reset "+key+" to the shipped default", ToastKindInfo)
		return
	}
	if err := cfgdocResetKey(c, key, cfgdocShippedDefaults().Values); err != nil {
		ShowToast("Could not reset "+key+": "+err.Error(), ToastKindWarning)
		return
	}
	cfgdocRefresh(c)
	PlayActivateSound()
	ShowToast(key+" is back to the shipped default", ToastKindSuccess)
	cfgdocRefreshAsync(c)
}

// cfgdocUseNewDefault takes the new default of the focused upstream change.
func cfgdocUseNewDefault(c *Config) {
	id := cfgdocFocusedID()
	if !strings.HasPrefix(id, "key:") || cfgdocUI.tab != cfgdocTabUpstream {
		return
	}
	key := strings.TrimPrefix(id, "key:")
	shipped := cfgdocShippedDefaults()
	if err := cfgdocResetKey(c, key, shipped.Values); err != nil {
		ShowToast("Could not change "+key+": "+err.Error(), ToastKindWarning)
		return
	}
	cfgdocRefresh(c)
	PlayActivateSound()
	ShowToast(key+" now uses this version's default", ToastKindSuccess)
	cfgdocRefreshAsync(c)
}

// cfgdocAcknowledge records this version's defaults as seen.
func cfgdocAcknowledge(c *Config) {
	if err := cfgdocAcknowledgeUpstream(); err != nil {
		ShowToast("Could not save that: "+firstLine(err.Error()), ToastKindWarning)
		return
	}
	PlayActivateSound()
	ShowToast("Got it — the list starts over at the next update", ToastKindInfo)
	cfgdocRefreshAsync(c)
}

// cfgdocExportNow writes jukahub-config.json to the card or Downloads.
func cfgdocExportNow(c *Config) {
	if !cfgdocBusy.CompareAndSwap(false, true) {
		ShowToast("Still writing the last file", ToastKindInfo)
		return
	}
	vars := cfgdocSnapshot(c)
	inputVars := cfgdocInputVars(c, nil)
	version, dir := versionString(c), bkupExportDir()
	PlayActivateSound()
	ShowToast("Writing "+cfgdocFileName+"…", ToastKindInfo)
	go func() {
		defer cfgdocBusy.Store(false)
		rep := cfgdocReportFor(vars, inputVars, version)
		if rep.Err != nil {
			ShowToast("Could not export: "+firstLine(rep.Err.Error()), ToastKindWarning)
			return
		}
		path, err := cfgdocWrite(rep.Doc, dir)
		if err != nil {
			ShowToast("Could not export: "+firstLine(err.Error()), ToastKindWarning)
			return
		}
		ShowToast("Saved "+path, ToastKindSuccess)
	}()
}

// cfgdocPreviewFocused previews the focused document.
func cfgdocPreviewFocused() {
	id := cfgdocFocusedID()
	if !strings.HasPrefix(id, "doc:") {
		return
	}
	path := strings.TrimPrefix(id, "doc:")
	_, _, rep, _, _, docs, _ := cfgdocSnapshotData()
	if rep.Current == nil {
		ShowToast("Still reading your settings…", ToastKindInfo)
		return
	}
	for _, d := range docs {
		if d.Path != path || d.Problem != "" {
			continue
		}
		plan := cfgdocPlanImport(path, d.Doc, rep.Current, rep.Shipped)
		cfgdocData.mu.Lock()
		cfgdocData.plan = plan
		cfgdocData.gen++
		cfgdocData.mu.Unlock()
		cfgdocUI.stage = 1
		cfgdocUI.list.cursor, cfgdocUI.list.top = 0, 0
		cfgdocUI.inPane = false
		PlayActivateSound()
		return
	}
}

// cfgdocToggleFocused flips the focused preview switch.
func cfgdocToggleFocused() {
	id := cfgdocFocusedID()
	cfgdocData.mu.Lock()
	plan := cfgdocData.plan
	changed := false
	switch {
	case plan == nil:
	case strings.HasPrefix(id, "item:"):
		if i, err := strconv.Atoi(strings.TrimPrefix(id, "item:")); err == nil && i >= 0 && i < len(plan.Items) && !plan.Items[i].Blocked {
			plan.Items[i].Accept = !plan.Items[i].Accept
			changed = true
		}
	case strings.HasPrefix(id, "scene:"):
		if i, err := strconv.Atoi(strings.TrimPrefix(id, "scene:")); err == nil && i >= 0 && i < len(plan.Scenes) {
			plan.Scenes[i].Accept = !plan.Scenes[i].Accept
			changed = true
		}
	}
	if changed {
		cfgdocData.gen++
	}
	cfgdocData.mu.Unlock()
	if changed {
		PlayToggleSound()
	}
}

// cfgdocSetAll turns every applicable preview switch on, or all off when they
// already are all on.
func cfgdocSetAll() {
	cfgdocData.mu.Lock()
	defer cfgdocData.mu.Unlock()
	plan := cfgdocData.plan
	if plan == nil {
		return
	}
	allOn := true
	for _, it := range plan.Items {
		if !it.Blocked && !it.Accept {
			allOn = false
		}
	}
	for _, sc := range plan.Scenes {
		if !sc.Accept {
			allOn = false
		}
	}
	for i := range plan.Items {
		if !plan.Items[i].Blocked {
			plan.Items[i].Accept = !allOn
		}
	}
	for i := range plan.Scenes {
		plan.Scenes[i].Accept = !allOn
	}
	cfgdocData.gen++
	PlayToggleSound()
}

// cfgdocAccepted counts the preview's switches that are on.
func cfgdocAccepted(plan *cfgdocImportPlan) (int, int) {
	if plan == nil {
		return 0, 0
	}
	items, scenes := 0, 0
	for _, it := range plan.Items {
		if it.Accept && !it.Blocked {
			items++
		}
	}
	for _, sc := range plan.Scenes {
		if sc.Accept {
			scenes++
		}
	}
	return items, scenes
}

// cfgdocApplyNow backs the settings up, then (cfgdocPoll) applies the import.
func cfgdocApplyNow(c *Config) {
	_, _, _, _, _, _, plan := cfgdocSnapshotData()
	items, scenes := cfgdocAccepted(plan)
	if items+scenes == 0 {
		ShowToast("Turn on at least one setting to import", ToastKindInfo)
		return
	}
	if !cfgdocBusy.CompareAndSwap(false, true) {
		ShowToast("Still writing the last file", ToastKindInfo)
		return
	}
	version := versionString(c)
	PlayActivateSound()
	ShowToast("Saving a backup of your settings first…", ToastKindInfo)
	go func() {
		cats := map[string]bool{bkupCatSettings: true}
		_, _, err := bkupCreate(context.Background(), bkupSafetyDir(), bkupFileName("safety", time.Now()), cats, bkupOptions{}, "pre-import", version, nil, nil)
		if err == nil {
			bkupPrune(bkupSafetyDir(), "JukaHub-safety-", "", bkupSafetyKeep)
		}
		cfgdocData.mu.Lock()
		cfgdocData.importReady, cfgdocData.importErr = true, err
		cfgdocData.mu.Unlock()
	}()
}

// cfgdocPoll finishes an import on the main thread once its backup is safe:
// the settings go through the normal setting path, and accepted scenes are
// written into the design file, which the config watcher reloads.
func cfgdocPoll(c *Config) {
	cfgdocData.mu.Lock()
	ready, err, plan := cfgdocData.importReady, cfgdocData.importErr, cfgdocData.plan
	cfgdocData.importReady, cfgdocData.importErr = false, nil
	cfgdocData.mu.Unlock()
	if !ready {
		return
	}
	defer cfgdocBusy.Store(false)
	if err != nil {
		ShowToast("The backup before the import failed, so nothing changed: "+firstLine(err.Error()), ToastKindWarning)
		return
	}
	if plan == nil {
		return
	}
	applied, failed := cfgdocApplyImport(c, plan, cfgdocShippedDefaults())
	if _, scenes := cfgdocAccepted(plan); scenes > 0 {
		sceneList := append([]cfgdocImportScene(nil), plan.Scenes...)
		go func() {
			n, err := cfgdocWriteScenes(cfgdocDesignPath, sceneList)
			if err != nil {
				ShowToast("Scenes not imported: "+firstLine(err.Error()), ToastKindWarning)
				return
			}
			if n > 0 {
				ShowToast(xpackKitCount(n, "scene", "scenes")+" imported into jukaconfig.json", ToastKindSuccess)
			}
		}()
	}
	msg := "Imported " + xpackKitCount(applied, "setting", "settings") + " · your settings were backed up first"
	if len(failed) > 0 {
		msg += fmt.Sprintf(" · %d skipped", len(failed))
		LogScene("cfgdoc").Warn("import items skipped", "items", strings.Join(failed, "; "))
	}
	ShowToast(msg, ToastKindSuccess)
	cfgdocUI.stage = 0
	cfgdocUI.list.cursor, cfgdocUI.list.top = 0, 0
	cfgdocRefreshAsync(c)
}

// cfgdocSwitchTab moves to another view.
func cfgdocSwitchTab(tab int) {
	if tab < 0 || tab >= cfgdocTabCount || tab == cfgdocUI.tab {
		return
	}
	cfgdocUI.tab = tab
	cfgdocUI.inPane, cfgdocUI.button = false, 0
	cfgdocUI.list.cursor, cfgdocUI.list.top = 0, 0
	PlayNavSound()
}

// cfgdocPaneButtons is the card's buttons for the focused row, rebuilt when the
// view, the focus or the data changes.
func cfgdocPaneButtons() {
	id := cfgdocFocusedID()
	gen, _, _, _, up, _, plan := cfgdocSnapshotData()
	key := cfgdocViewKey{tab: cfgdocUI.tab, stage: cfgdocUI.stage, gen: gen, id: id}
	if key == cfgdocUI.buttonsKey {
		return
	}
	cfgdocUI.buttonsKey = key
	items, scenes := cfgdocAccepted(plan)
	var b []xpackKitButton
	switch cfgdocUI.tab {
	case cfgdocTabChanges:
		if strings.HasPrefix(id, "key:") {
			ok, _ := cfgdocImportable(strings.TrimPrefix(id, "key:"))
			b = append(b, xpackKitButton{ID: "reset", Label: "Reset to default", Disabled: !ok})
		}
		b = append(b, xpackKitButton{ID: "export", Label: "Export file", Primary: !strings.HasPrefix(id, "key:")})
	case cfgdocTabUpstream:
		if strings.HasPrefix(id, "key:") {
			b = append(b, xpackKitButton{ID: "usedefault", Label: "Use the new default", Primary: true})
		}
		b = append(b, xpackKitButton{ID: "ack", Label: "Got it", Disabled: len(up) == 0})
	case cfgdocTabImport:
		if cfgdocUI.stage == 1 {
			label := "Apply " + xpackKitCount(items+scenes, "change", "changes")
			b = append(b,
				xpackKitButton{ID: "apply", Label: label, Primary: true, Disabled: items+scenes == 0},
				xpackKitButton{ID: "all", Label: "All on / off"},
				xpackKitButton{ID: "files", Label: "Back to files"})
		} else if strings.HasPrefix(id, "doc:") {
			b = append(b, xpackKitButton{ID: "preview", Label: "Preview", Primary: true})
		}
	}
	cfgdocUI.buttons = b
	if cfgdocUI.button >= len(b) {
		cfgdocUI.button = 0
	}
	if len(b) == 0 {
		cfgdocUI.inPane = false
	}
}

// cfgdocActivateButton presses card button i.
func cfgdocActivateButton(c *Config, i int) {
	if i < 0 || i >= len(cfgdocUI.buttons) || cfgdocUI.buttons[i].Disabled {
		return
	}
	switch cfgdocUI.buttons[i].ID {
	case "reset":
		cfgdocResetFocused(c)
	case "export":
		cfgdocExportNow(c)
	case "usedefault":
		cfgdocUseNewDefault(c)
	case "ack":
		cfgdocAcknowledge(c)
	case "preview":
		cfgdocPreviewFocused()
	case "apply":
		cfgdocApplyNow(c)
	case "all":
		cfgdocSetAll()
	case "files":
		cfgdocUI.stage = 0
		cfgdocUI.inPane = false
		PlayBackSound()
	}
}

// cfgdocPress is A on the list.
func cfgdocPress(c *Config) {
	if cfgdocUI.inPane {
		cfgdocActivateButton(c, cfgdocUI.button)
		return
	}
	switch {
	case cfgdocUI.tab == cfgdocTabImport && cfgdocUI.stage == 1:
		cfgdocToggleFocused()
	case cfgdocUI.tab == cfgdocTabImport:
		cfgdocPreviewFocused()
	default:
		cfgdocEnterPane()
	}
}

// cfgdocEnterPane moves the focus onto the card's buttons.
func cfgdocEnterPane() {
	if len(cfgdocUI.buttons) == 0 {
		return
	}
	cfgdocUI.inPane, cfgdocUI.button = true, 0
	PlayNavSound()
}

// cfgdocBack is B: buttons → list → (preview → files) → off the screen.
func cfgdocBack(c *Config) {
	switch {
	case cfgdocUI.inPane:
		cfgdocUI.inPane = false
		PlayBackSound()
	case cfgdocUI.tab == cfgdocTabImport && cfgdocUI.stage == 1:
		cfgdocUI.stage = 0
		PlayBackSound()
	default:
		goBackScene(c)
	}
}

// cfgdocMove steps the focus in the list or between the card's buttons.
func cfgdocMove(dx, dy int) {
	if cfgdocUI.inPane {
		next, moved := xpackKitMoveButton(cfgdocUI.button, len(cfgdocUI.buttons), dx, dy)
		switch {
		case moved:
			cfgdocUI.button = next
			PlayNavSound()
		case dx < 0 && cfgdocUI.button%2 == 0:
			cfgdocUI.inPane = false
			PlayNavSound()
		}
		return
	}
	switch {
	case dx > 0:
		cfgdocEnterPane()
	case dy < 0:
		if cfgdocUI.list.atFirst(cfgdocUI.rows) {
			focusEngine.ClearFocus()
			focusChromeBar()
			return
		}
		if cfgdocUI.list.move(cfgdocUI.rows, -1) {
			PlayNavSound()
		}
	case dy > 0:
		if cfgdocUI.list.move(cfgdocUI.rows, 1) {
			PlayNavSound()
		}
	}
}

// cfgdocX is X: reset, use the default, or all on/off, by view.
func cfgdocX(c *Config) {
	switch {
	case cfgdocUI.tab == cfgdocTabChanges:
		cfgdocResetFocused(c)
	case cfgdocUI.tab == cfgdocTabUpstream:
		cfgdocUseNewDefault(c)
	case cfgdocUI.stage == 1:
		cfgdocSetAll()
	}
}

// cfgdocY is Y: export, got it, or apply, by view.
func cfgdocY(c *Config) {
	switch {
	case cfgdocUI.tab == cfgdocTabChanges:
		cfgdocExportNow(c)
	case cfgdocUI.tab == cfgdocTabUpstream:
		cfgdocAcknowledge(c)
	case cfgdocUI.stage == 1:
		cfgdocApplyNow(c)
	}
}

// ── Input ─────────────────────────────────────────────────────────────────

func cfgdocKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	cfgdocEnsureVisit(c)
	switch e.Keysym.Sym {
	case sdl.K_UP:
		cfgdocMove(0, -1)
	case sdl.K_DOWN:
		cfgdocMove(0, 1)
	case sdl.K_LEFT:
		cfgdocMove(-1, 0)
	case sdl.K_RIGHT:
		cfgdocMove(1, 0)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		cfgdocPress(c)
	case sdl.K_x, sdl.K_DELETE:
		cfgdocX(c)
	case sdl.K_y, sdl.K_e:
		cfgdocY(c)
	case sdl.K_TAB, sdl.K_PAGEDOWN:
		if e.Keysym.Mod&sdl.KMOD_SHIFT != 0 {
			cfgdocSwitchTab(cfgdocUI.tab - 1)
		} else {
			cfgdocSwitchTab(cfgdocUI.tab + 1)
		}
	case sdl.K_PAGEUP:
		cfgdocSwitchTab(cfgdocUI.tab - 1)
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		cfgdocBack(c)
	default:
		return false
	}
	return true
}

func cfgdocPad(e *sdl.ControllerButtonEvent, c *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_START, sdl.CONTROLLER_BUTTON_GUIDE,
		sdl.CONTROLLER_BUTTON_LEFTSTICK, sdl.CONTROLLER_BUTTON_RIGHTSTICK:
		return false
	}
	cfgdocEnsureVisit(c)
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		cfgdocMove(0, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		cfgdocMove(0, 1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		cfgdocMove(-1, 0)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		cfgdocMove(1, 0)
	case sdl.CONTROLLER_BUTTON_A:
		cfgdocPress(c)
	case sdl.CONTROLLER_BUTTON_X:
		cfgdocX(c)
	case sdl.CONTROLLER_BUTTON_Y:
		cfgdocY(c)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		cfgdocSwitchTab(cfgdocUI.tab - 1)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		cfgdocSwitchTab(cfgdocUI.tab + 1)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		cfgdocBack(c)
	}
	return true
}

// cfgdocMouse handles a click or a tap on the inspector.
func cfgdocMouse(mx, my int32, c *Config) bool {
	cfgdocEnsureVisit(c)
	for i, r := range cfgdocUI.tabRects {
		if r.W > 0 && hitTouchTarget(r, mx, my) {
			cfgdocSwitchTab(i)
			return true
		}
	}
	if i := xpackKitButtonHit(cfgdocUI.buttonRects, mx, my); i >= 0 {
		cfgdocUI.inPane, cfgdocUI.button = true, i
		cfgdocActivateButton(c, i)
		return true
	}
	i, onSwitch := cfgdocUI.list.hit(mx, my)
	if i < 0 || !xpackKitFocusable(cfgdocUI.rows, i) {
		return false
	}
	again := i == cfgdocUI.list.cursor && !cfgdocUI.inPane
	cfgdocUI.list.cursor, cfgdocUI.inPane = i, false
	switch {
	case onSwitch || (again && cfgdocUI.tab == cfgdocTabImport && cfgdocUI.stage == 1):
		cfgdocToggleFocused()
	case again && cfgdocUI.tab == cfgdocTabImport:
		cfgdocPreviewFocused()
	default:
		PlayNavSound()
	}
	return true
}

// cfgdocWheel scrolls the list.
func cfgdocWheel(dy int32) bool {
	cfgdocUI.list.wheel(cfgdocUI.rows, dy)
	return true
}

// cfgdocHints is the inspector's footer.
func cfgdocHints() []FooterHint {
	if cfgdocUI.inPane {
		return []FooterHint{{Button: "D-Pad", Label: "Choose"}, {Button: "A", Label: "Select"}, {Button: "B", Label: "List"}}
	}
	// "/ Tab": the keyboard switches the view with Tab (cfgdocKey).
	tabs := FooterHint{Button: "L1/R1 / Tab", Label: "View"}
	switch {
	case cfgdocUI.tab == cfgdocTabChanges:
		return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "X", Label: "Reset"}, {Button: "Y", Label: "Export"}, tabs, {Button: "B", Label: "Back"}}
	case cfgdocUI.tab == cfgdocTabUpstream:
		return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "X", Label: "Use default"}, {Button: "Y", Label: "Got it"}, tabs, {Button: "B", Label: "Back"}}
	case cfgdocUI.stage == 1:
		return []FooterHint{{Button: "A", Label: "Switch"}, {Button: "X", Label: "All"}, {Button: "Y", Label: "Apply"}, {Button: "B", Label: "Files"}}
	}
	return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Preview"}, tabs, {Button: "B", Label: "Back"}}
}

// ── Drawing ───────────────────────────────────────────────────────────────

// cfgdocRender draws the inspector.
func cfgdocRender(renderer *sdl.Renderer, c *Config) {
	cfgdocEnsureVisit(c)
	cfgdocPoll(c)
	cfgdocSyncRows()
	cfgdocPaneButtons()
	f, ok := xpackKitLoadFonts(c)
	if !ok {
		drawAppBackdrop(renderer, c, screenWidth, screenHeight)
		xpackKitFinish(renderer, c)
		return
	}
	_, loading, rep, _, up, _, _ := cfgdocSnapshotData()
	header := UIPanelHeader("Config", "What you changed from what this version ships, as one file to diff, export and merge", "code")
	switch {
	case loading && rep.Current == nil:
		header.Status = "Comparing…"
	case rep.Err == nil:
		if n := len(rep.Changes); n != cfgdocUI.statusN || cfgdocUI.status == "" {
			cfgdocUI.statusN, cfgdocUI.status = n, xpackKitCount(n, "change", "changes")
		}
		header.Status = cfgdocUI.status
	}
	contentY, bottom, margin := xpackKitFrame(renderer, c, cfgdocSceneName, header)
	if n := len(up); n != cfgdocUI.upCount || cfgdocUI.upLabel == "" {
		cfgdocUI.upCount, cfgdocUI.upLabel = n, cfgdocTabLabels[cfgdocTabUpstream]
		if n > 0 {
			cfgdocUI.upLabel += " · " + strconv.Itoa(n)
		}
	}
	tabsBottom := cfgdocDrawTabs(renderer, f, margin, contentY)
	listW := (screenWidth - 2*margin) * 58 / 100
	area := sdl.Rect{X: margin, Y: tabsBottom + 12, W: listW, H: bottom - tabsBottom - 12}
	ring := !cfgdocUI.inPane && !xpackKitChrome()
	xpackKitDrawList(renderer, c, f, &cfgdocUI.list, cfgdocUI.rows, area, ring)
	paneX := margin + listW + 18
	cfgdocDrawPane(renderer, c, f, sdl.Rect{X: paneX, Y: contentY, W: screenWidth - margin - paneX, H: bottom - contentY})
	xpackKitFinish(renderer, c)
}

// cfgdocDrawTabs draws the view pills and returns the y below them.
func cfgdocDrawTabs(renderer *sdl.Renderer, f xpackKitFonts, x, y int32) int32 {
	h := int32(f.small.Height()) + 14
	for i := 0; i < cfgdocTabCount; i++ {
		label := cfgdocTabLabels[i]
		if i == cfgdocTabUpstream {
			label = cfgdocUI.upLabel
		}
		tw, th := measureText(f.small, label)
		r := sdl.Rect{X: x, Y: y, W: tw + 28, H: h}
		cfgdocUI.tabRects[i] = r
		fill, ink := glassA(14), ColorTextSecondary()
		if i == cfgdocUI.tab {
			fill, ink = glassAccent(), contrastText(glassAccent())
		} else if pointInRect(mouseX, mouseY, r) {
			fill = glassA(24)
		}
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, fill)
		if i != cfgdocUI.tab {
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, 1, glassA(40))
		}
		drawText(renderer, f.small, label, r.X+14, r.Y+(r.H-th)/2, ink, textAlignLeft)
		x += r.W + 10
	}
	return y + h
}

// cfgdocDrawPane draws the card for the focused row on the right. Its text is
// built and wrapped when the view, the focus, the data or the width changes.
func cfgdocDrawPane(renderer *sdl.Renderer, c *Config, f xpackKitFonts, area sdl.Rect) {
	cfgdocUI.buttonRects = cfgdocUI.buttonRects[:0]
	if area.W <= 80 || area.H <= 80 {
		return
	}
	UICard(renderer, area, DefaultUICardStyle())
	const pad = int32(20)
	x, w := area.X+pad, area.W-2*pad
	y := area.Y + 16
	bh := max(int32(f.body.Height())+20, 44)
	rows := int32((len(cfgdocUI.buttons) + 1) / 2)
	buttonsY := area.Y + area.H - pad - rows*(bh+10) + 10
	limit := buttonsY - 10
	id := cfgdocFocusedID()
	gen, _, _, _, _, _, _ := cfgdocSnapshotData()
	if key := (cfgdocViewKey{tab: cfgdocUI.tab, stage: cfgdocUI.stage, gen: gen, id: id, width: w}); key != cfgdocUI.paneKey {
		cfgdocUI.paneKey = key
		title, sub, body := cfgdocPaneText(id)
		cfgdocUI.paneTitle, cfgdocUI.paneSub = title, sub
		cfgdocUI.paneLines = cfgdocUI.paneLines[:0]
		for _, para := range body {
			if para == "" {
				cfgdocUI.paneLines = append(cfgdocUI.paneLines, "")
				continue
			}
			cfgdocUI.paneLines = append(cfgdocUI.paneLines, wrapTextLines(f.body, para, w, 4)...)
		}
	}
	y = xpackKitText(renderer, f.big, cfgdocUI.paneTitle, x, y, w, ColorTextPrimary()) + 2
	y = xpackKitText(renderer, f.small, cfgdocUI.paneSub, x, y, w, ColorTextTertiary()) + 12
	lineH := int32(f.body.Height()) + 2
	for _, l := range cfgdocUI.paneLines {
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
	focus := -1
	if cfgdocUI.inPane && !xpackKitChrome() {
		focus = cfgdocUI.button
	}
	xpackKitDrawButtons(renderer, c, f, cfgdocUI.buttons, &cfgdocUI.buttonRects, x, buttonsY, w, area.Y+area.H-pad+10, focus)
}

// cfgdocPaneText is the card's title, subtitle and paragraphs for the focused
// row ("" between paragraphs is a gap).
func cfgdocPaneText(id string) (string, string, []string) {
	_, _, rep, rec, up, docs, plan := cfgdocSnapshotData()
	title, sub := "", ""
	var body []string
	switch {
	case cfgdocUI.tab == cfgdocTabImport && cfgdocUI.stage == 1 && plan != nil:
		items, scenes := cfgdocAccepted(plan)
		from := plan.Doc.Device.Name
		if from == "" {
			from = plan.Doc.Device.Platform
		}
		title = "Import preview"
		sub = "From " + from + " · " + plan.Doc.GeneratedBy
		body = append(body, xpackKitCount(len(plan.Items), "setting differs", "settings differ")+" · "+strconv.Itoa(items)+" on")
		if len(plan.Scenes) > 0 {
			body = append(body, xpackKitCount(len(plan.Scenes), "scene", "scenes")+" · "+strconv.Itoa(scenes)+" on")
		}
		if !plan.SameDevice {
			body = append(body, "Made on another kind of device: device settings start switched off.")
		}
		if i, err := strconv.Atoi(strings.TrimPrefix(id, "item:")); strings.HasPrefix(id, "item:") && err == nil && i >= 0 && i < len(plan.Items) {
			it := plan.Items[i]
			body = append(body, "", it.Key+" ("+cfgdocSectionTitle(it.Section)+")",
				"Now: "+cfgdocShow(it.Current, it.Secret), "From the file: "+cfgdocShow(it.Value, it.Secret))
			if it.Note != "" {
				body = append(body, it.Note)
			}
		}
		body = append(body, "", "A backup of your settings is saved before anything changes.")
	case cfgdocUI.tab == cfgdocTabImport:
		title, sub = "Import a config file", "Merge settings from a "+cfgdocFileName
		body = append(body, "Files named like "+cfgdocFileName+" on the card, in its JukaHub Backups folder or in Downloads are listed here.")
		for _, d := range docs {
			if "doc:"+d.Path != id {
				continue
			}
			body = append(body, "", d.Path)
			if d.Problem == "" {
				body = append(body, "Made "+d.Doc.GeneratedAt, "Defaults stamp "+d.Doc.Defaults.Stamp+" (v"+d.Doc.Defaults.Version+")")
			}
		}
	case cfgdocUI.tab == cfgdocTabUpstream:
		title = "What changed upstream"
		sub = "Defaults of v" + rep.Shipped.Version + " against the ones you last saw"
		if rec.Version != "" {
			sub = "v" + rec.Version + " → v" + rep.Shipped.Version
		}
		body = append(body, "When an update ships a different default, it is listed here with what you use, so a new default never changes your setup without you seeing it.")
		for _, ch := range up {
			if "key:"+ch.Key != id {
				continue
			}
			body = append(body, "", ch.Key, "Was: "+cfgdocShow(ch.Old, ch.Secret), "Now: "+cfgdocShow(ch.New, ch.Secret))
			if cur, ok := rep.Current[ch.Key]; ok {
				body = append(body, "Yours: "+cfgdocShow(cur, ch.Secret))
			}
		}
	default:
		title = cfgdocFileName
		sub = "Defaults: " + rep.Shipped.Source + " · stamp " + rep.Shipped.Stamp
		for _, ch := range rep.Changes {
			if "key:"+ch.Key != id {
				continue
			}
			title, sub = ch.Key, cfgdocSectionTitle(ch.Section)
			body = append(body, "Shipped: "+cfgdocShow(ch.Old, ch.Secret), "Yours: "+cfgdocShow(ch.New, ch.Secret))
			if ch.Secret {
				body = append(body, "A secret: the exported file says "+cfgdocRedacted+" instead of the value.")
			}
		}
		if len(body) == 0 {
			body = append(body, "Every key in the list differs from what this version ships. Export writes them, sorted into input profiles, layouts, theme and behaviour, to one file you can keep, diff and import anywhere.",
				"", "Secrets — API keys, tokens, passwords — are never written into it.")
		}
	}
	return title, sub, body
}

// ── Scene registration ────────────────────────────────────────────────────

type cfgdocScene struct{ sceneBase }

func (cfgdocScene) render(r *sdl.Renderer, c *Config) bool {
	cfgdocRender(r, c)
	return true
}

func (cfgdocScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool { return cfgdocKey(e, c) }

func (cfgdocScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return cfgdocPad(e, c)
}

func init() { registerScene(cfgdocScene{sceneBase{name: cfgdocSceneName}}) }
