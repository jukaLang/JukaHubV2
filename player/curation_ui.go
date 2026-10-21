package main

// curation_ui.go — the Curation Hub overlay.
//
// Three pages share one card: an essentials checklist, a profile exchange and a
// compatibility vote list. The layout follows the Save Manager and Accessibility
// panels so it does not look bolted on, and every page keeps the same A/B
// contract: A acts, B goes back one level, then closes.

import (
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

type curationPage int

const (
	curationPageHome curationPage = iota
	curationPageEssentials
	curationPageProfiles
	curationPageVotes
)

type curationRowAction int

const (
	curationActionOpenEssentials curationRowAction = iota
	curationActionOpenProfiles
	curationActionOpenVotes
	curationActionApplyBundle
	curationActionApplyStep
	curationActionCaptureProfile
	curationActionRefreshProfiles
	curationActionApplyProfile
	curationActionShareProfile
	curationActionDeleteProfile
	curationActionSubmitVotes
	curationActionToggleVote
	curationActionRefreshFeed
	curationActionRefresh
	curationActionBack
	curationActionInfo
)

type curationRow struct {
	label   string
	value   string
	detail  string
	hint    string
	action  curationRowAction
	index   int
	enabled bool
	accent  bool
}

type curationRowHit struct {
	index int
	rect  sdl.Rect
}

type curationPanelState struct {
	open          bool
	page          curationPage
	cursor        int
	scroll        int
	rect          sdl.Rect
	device        curationDevice
	bundle        curationBundle
	essentials    curationEssentials
	profiles      []curationProfile
	profileIssues []string
	votes         curationVotesFile
	voteIndex     map[string]bool
	targets       []curationTarget
	scanning      bool
	lastScanBuilt bool
}

var curationPanel curationPanelState
var curationRowHits []curationRowHit

func openCurationPanel(config *Config) {
	// Overlays never stack: opening the hub replaces every other modal.
	qs.open = false
	toolsPanel.open = false
	emuPanel.open = false
	gpPanel.open = false
	raPanel.open = false
	cpPanel.open = false
	winAppsPanel = windowsAppsPanelState{}
	portsPanel = portsPanelState{}
	castPanel.open = false
	musicSettings.open = false
	a11y.open = false
	smartHomePanel.open = false
	chiakiPanel = chiakiPanelState{}
	communityPanel.open = false
	powerTools.open = false
	if calibrationPanel.open {
		closeStickCalibrationPanel()
	}
	savePanel = saveManagerPanelState{}
	saveRowHits = nil
	tb.startOpen = false

	curationPanel = curationPanelState{
		open:      true,
		page:      curationPageHome,
		cursor:    0,
		scroll:    0,
		rect:      sdl.Rect{},
		device:    currentCurationDevice(),
		voteIndex: map[string]bool{},
	}
	curationRowHits = nil
	refreshCurationPanel(config)
	refreshCurationFeedAsync()
	PlayActivateSound()
}

func closeCurationPanel() {
	if !curationPanel.open {
		return
	}
	curationPanel = curationPanelState{}
	curationRowHits = nil
	PlayBackSound()
}

// refreshCurationPanel re-reads everything except the remote feed, which has its
// own background refresh. The lists are small and the panel opens rarely, so
// there is no cache to invalidate.
func refreshCurationPanel(config *Config) {
	panel := &curationPanel
	panel.device = currentCurationDevice()
	panel.bundle, _ = curationBundleForDevice(panel.device, allCurationBundles())
	panel.essentials = buildCurationEssentials(panel.bundle, liveCurationEnv(config))
	panel.profiles, panel.profileIssues = allCurationProfiles()
	panel.votes = loadCurationVotes()
	panel.voteIndex = curationVoteIndex(panel.votes)
	// The ROM walk is expensive on a big card, so it only starts when the
	// Votes page is actually opened. The home page needs the vote count, not
	// a scan.
	panel.targets, panel.scanning = liveCurationTargets(false)
	clampCurationCursor()
}

// liveCurationTargets collects the vote list. The ROM walk runs in the
// background; while it is missing the ports alone are shown.
func liveCurationTargets(startScan bool) ([]curationTarget, bool) {
	games, _, _, scanning, built := compatibilitySnapshot()
	if startScan && !built && !scanning {
		startCompatibilityScan()
		scanning = true
	}
	ports := scanPortLaunchers(portRoots(portMasterSDRoot))
	return curationTargets(games, ports), scanning
}

// pollCurationScan picks up a finished ROM scan without rescanning every frame.
func pollCurationScan() {
	if !curationPanel.open || curationPanel.page != curationPageVotes {
		return
	}
	games, _, _, scanning, built := compatibilitySnapshot()
	if built && !curationPanel.lastScanBuilt {
		curationPanel.lastScanBuilt = true
		curationPanel.targets, curationPanel.scanning = liveCurationTargets(true)
		return
	}
	curationPanel.scanning = scanning
	if scanning {
		return
	}
	if built {
		curationPanel.targets, curationPanel.scanning = curationTargets(games, scanPortLaunchers(portRoots(portMasterSDRoot))), false
	}
}

func curationDeviceLabel(key string) string {
	switch key {
	case "", curationDeviceAny:
		return "Any device"
	case "smartpro":
		return "Smart Pro"
	case "smartpro-s":
		return "Smart Pro S"
	case "brick":
		return "Brick"
	case "brick-pro":
		return "Brick Pro"
	case "hammer":
		return "Hammer"
	}
	return key
}

func curationProfileValue(profile curationProfile) string {
	return profile.Theme + " · " + strconv.Itoa(int(math.Round(profile.TextScale*100))) + "%"
}

func curationProfileDetail(profile curationProfile) string {
	parts := []string{curationDeviceLabel(profile.Device)}
	if profile.Author != "" {
		parts = append(parts, "by "+profile.Author)
	}
	detail := strings.Join(parts, " · ")
	if profile.Notes != "" {
		detail += " — " + profile.Notes
	}
	return detail
}

func curationFeedDetail() string {
	if url := curationFeedURL(); url != "" {
		return url
	}
	return "No feed configured — built-in bundles and profiles only."
}

func clampCurationCursor() {
	rows := curationRows(appConfig)
	if len(rows) == 0 {
		curationPanel.cursor = 0
		curationPanel.scroll = 0
		return
	}
	if curationPanel.cursor < 0 {
		curationPanel.cursor = 0
	}
	if curationPanel.cursor >= len(rows) {
		curationPanel.cursor = len(rows) - 1
	}
}

func curationRows(config *Config) []curationRow {
	pollCurationScan()
	panel := &curationPanel
	switch panel.page {
	case curationPageEssentials:
		return curationEssentialsRows(panel)
	case curationPageProfiles:
		return curationProfileRows(panel)
	case curationPageVotes:
		return curationVoteRows(panel)
	}
	return curationHomeRows(panel)
}

func curationHomeRows(panel *curationPanelState) []curationRow {
	ready, total := panel.essentials.Ready, len(panel.essentials.Steps)
	essentialsValue := fmt.Sprintf("%d/%d ready", ready, total)
	if total == 0 {
		essentialsValue = "Unavailable"
	}
	voted := len(panel.votes.Votes)
	profilesValue := fmt.Sprintf("%d profile(s)", len(panel.profiles))
	return []curationRow{
		{
			label:   panel.essentials.Bundle.Name,
			value:   essentialsValue,
			detail:  panel.essentials.Bundle.Summary,
			hint:    "A: open",
			action:  curationActionOpenEssentials,
			enabled: total > 0,
		},
		{
			label:   "UI & theme profiles",
			value:   profilesValue,
			detail:  "Themes and text sizes tuned for this panel — apply or share one.",
			hint:    "A: open",
			action:  curationActionOpenProfiles,
			enabled: true,
		},
		{
			label:   "Compatibility votes",
			value:   fmt.Sprintf("%d voted", voted),
			detail:  "Mark games and PortMaster titles that need testing.",
			hint:    "A: open",
			action:  curationActionOpenVotes,
			enabled: true,
		},
		{
			label:   "Community feed",
			value:   curationFeedLabel(),
			detail:  curationFeedDetail(),
			hint:    "A: refresh",
			action:  curationActionRefreshFeed,
			enabled: true,
		},
		{
			label:   "Refresh",
			detail:  "Rescan profiles, save folders, ports and the ROM library.",
			hint:    "A: refresh",
			action:  curationActionRefresh,
			enabled: true,
		},
		{
			label:  "Close",
			value:  "Back to the app",
			hint:   "B",
			action: curationActionBack,
		},
	}
}

func curationEssentialsRows(panel *curationPanelState) []curationRow {
	steps := panel.essentials.Steps
	actionable := false
	for _, step := range steps {
		if step.CanApply {
			actionable = true
			break
		}
	}
	rows := []curationRow{{
		label:   "Apply essentials",
		value:   fmt.Sprintf("%d/%d ready", panel.essentials.Ready, len(steps)),
		detail:  "Fills gaps only — a theme or setting you chose is never overwritten.",
		hint:    "A: apply",
		action:  curationActionApplyBundle,
		enabled: actionable,
	}}
	for i, step := range steps {
		rows = append(rows, curationRow{
			label:   curationStepLabel(step.Step.Kind),
			value:   step.Value,
			detail:  step.Line,
			hint:    "A: apply",
			action:  curationActionApplyStep,
			index:   i,
			enabled: step.CanApply,
			accent:  !step.Done,
		})
	}
	rows = append(rows, curationRow{label: "Back", value: "Curation Hub", hint: "B", action: curationActionBack})
	return rows
}

func curationProfileRows(panel *curationPanelState) []curationRow {
	theme := "hub"
	if appConfig != nil {
		if v, ok := customString(appConfig.Variables.Custom, "theme_preset"); ok && strings.TrimSpace(v) != "" {
			theme = strings.TrimSpace(v)
		}
	}
	scale := a11y.fontScale
	if scale <= 0 {
		scale = 1
	}
	current := fmt.Sprintf("%s · %d%%", theme, int(math.Round(scale*100)))
	rows := []curationRow{
		{
			label:   "Capture this setup",
			value:   current,
			detail:  "Save the current theme and text size as a profile you can share.",
			hint:    "A: save",
			action:  curationActionCaptureProfile,
			enabled: true,
		},
		{
			label:   "Import folder",
			value:   fmt.Sprintf("%d file issue(s)", len(panel.profileIssues)),
			detail:  "Drop profile JSON into data/curation/import, then refresh.",
			hint:    "A: rescan",
			action:  curationActionRefreshProfiles,
			enabled: true,
		},
	}
	for i, profile := range panel.profiles {
		hint := "A: apply   Y: share"
		if profile.Source == "local" {
			hint = "A: apply   X: delete   Y: share"
		}
		rows = append(rows, curationRow{
			label:   profile.Name,
			value:   curationProfileValue(profile),
			detail:  curationProfileDetail(profile),
			hint:    hint,
			action:  curationActionApplyProfile,
			index:   i,
			enabled: true,
		})
	}
	rows = append(rows, curationRow{label: "Back", value: "Curation Hub", hint: "B", action: curationActionBack})
	return rows
}

func curationVoteRows(panel *curationPanelState) []curationRow {
	voted := len(panel.votes.Votes)
	detail := "Marks what needs testing. No collector configured — a JSON bundle is written to data/curation/export."
	if curationVotesURL() != "" {
		detail = "Marks what needs testing and sends the bundle to the configured collector."
	}
	rows := []curationRow{{
		label:   "Submit votes",
		value:   fmt.Sprintf("%d", voted),
		detail:  detail,
		hint:    "A: submit",
		action:  curationActionSubmitVotes,
		enabled: voted > 0,
	}}
	if panel.scanning {
		rows = append(rows, curationRow{
			label:  "Scanning the ROM library…",
			detail: "Ports are already listed; games appear when the scan finishes.",
			action: curationActionInfo,
		})
	}
	if len(panel.targets) == 0 && !panel.scanning {
		rows = append(rows, curationRow{
			label:  "Nothing to vote on yet",
			detail: "Add ROMs or install a port, then refresh.",
			action: curationActionRefresh,
		})
	}
	for i, target := range panel.targets {
		value := ""
		hint := "A: vote"
		if panel.voteIndex[curationVoteKey(target)] {
			value = "▲ voted"
			hint = "A: remove vote"
		}
		rows = append(rows, curationRow{
			label:   target.Title,
			value:   value,
			detail:  target.System + " · " + target.Detail,
			hint:    hint,
			action:  curationActionToggleVote,
			index:   i,
			enabled: true,
			accent:  value != "",
		})
	}
	rows = append(rows, curationRow{label: "Back", value: "Curation Hub", hint: "B", action: curationActionBack})
	return rows
}

func curationMove(delta int) {
	rows := curationRows(appConfig)
	if len(rows) == 0 {
		return
	}
	curationPanel.cursor += delta
	if curationPanel.cursor < 0 {
		curationPanel.cursor = len(rows) - 1
	}
	if curationPanel.cursor >= len(rows) {
		curationPanel.cursor = 0
	}
	PlayNavSound()
}

func curationPageMove(dir int) {
	rows := curationRows(appConfig)
	if len(rows) == 0 {
		return
	}
	curationPanel.cursor += dir * 5
	if curationPanel.cursor < 0 {
		curationPanel.cursor = 0
	}
	if curationPanel.cursor >= len(rows) {
		curationPanel.cursor = len(rows) - 1
	}
	PlayNavSound()
}

func curationBack() {
	if curationPanel.page == curationPageHome {
		closeCurationPanel()
		return
	}
	curationPanel.page = curationPageHome
	curationPanel.cursor = 0
	curationPanel.scroll = 0
	PlayBackSound()
}

func curationActivate(config *Config) {
	rows := curationRows(config)
	if curationPanel.cursor < 0 || curationPanel.cursor >= len(rows) {
		return
	}
	row := rows[curationPanel.cursor]
	if !row.enabled {
		ShowToast("Nothing to do for this row yet", ToastKindInfo)
		return
	}
	switch row.action {
	case curationActionOpenEssentials:
		curationPanel.page = curationPageEssentials
		curationPanel.cursor = 0
		curationPanel.scroll = 0
		PlayActivateSound()
	case curationActionOpenProfiles:
		curationPanel.page = curationPageProfiles
		curationPanel.cursor = 0
		curationPanel.scroll = 0
		PlayActivateSound()
	case curationActionOpenVotes:
		curationPanel.page = curationPageVotes
		curationPanel.cursor = 0
		curationPanel.scroll = 0
		// Opening the page is the consent to walk the ROM library.
		curationPanel.targets, curationPanel.scanning = liveCurationTargets(true)
		PlayActivateSound()
	case curationActionApplyBundle:
		result := applyCurationBundle(config, curationPanel.bundle)
		after, _ := curationBundleForDevice(curationPanel.device, allCurationBundles())
		curationPanel.bundle = after
		curationPanel.essentials = buildCurationEssentials(after, liveCurationEnv(config))
		if len(result.Failed) > 0 {
			ShowToast("Essentials: "+strings.Join(result.Failed, " · "), ToastKindWarning)
			return
		}
		PlaySuccessSound()
		ShowToast("Essentials: "+result.summary(), ToastKindSuccess)
	case curationActionApplyStep:
		if row.index < 0 || row.index >= len(curationPanel.essentials.Steps) {
			return
		}
		step := curationPanel.essentials.Steps[row.index]
		message, err := applyCurationStep(config, step.Step)
		curationPanel.essentials = buildCurationEssentials(curationPanel.bundle, liveCurationEnv(config))
		if err != nil {
			ShowToast(firstLine(err.Error()), ToastKindError)
			return
		}
		PlaySuccessSound()
		ShowToast(curationStepLabel(step.Step.Kind)+": "+message, ToastKindSuccess)
	case curationActionCaptureProfile:
		profile, err := captureCurationProfile(config, curationPanel.device)
		if err != nil {
			ShowToast("Could not save the profile: "+firstLine(err.Error()), ToastKindError)
			return
		}
		curationPanel.profiles, curationPanel.profileIssues = allCurationProfiles()
		PlaySuccessSound()
		ShowToast("Saved "+profile.Name+" — share it with Y", ToastKindSuccess)
	case curationActionRefreshProfiles:
		curationPanel.profiles, curationPanel.profileIssues = allCurationProfiles()
		if len(curationPanel.profileIssues) > 0 {
			ShowToast("Profiles: "+strings.Join(curationPanel.profileIssues, " · "), ToastKindWarning)
			return
		}
		ShowToast(fmt.Sprintf("%d profile(s) available", len(curationPanel.profiles)), ToastKindSuccess)
	case curationActionApplyProfile:
		if row.index < 0 || row.index >= len(curationPanel.profiles) {
			return
		}
		profile := curationPanel.profiles[row.index]
		if err := applyCurationProfile(config, profile); err != nil {
			ShowToast("Could not apply the profile: "+firstLine(err.Error()), ToastKindError)
			return
		}
		PlaySuccessSound()
		message := "Applied " + profile.Name
		if !curationProfileMatchesDevice(profile, curationPanel.device) {
			message += " (tuned for " + curationDeviceLabel(profile.Device) + ")"
		}
		ShowToast(message, ToastKindSuccess)
	case curationActionShareProfile:
		curationShareProfile(row.index)
	case curationActionDeleteProfile:
		curationDeleteProfile(row.index)
	case curationActionRefresh:
		refreshCurationPanel(config)
		ShowToast("Curation Hub refreshed", ToastKindSuccess)
	case curationActionRefreshFeed:
		if curationFeedURL() == "" {
			ShowToast("No curation feed configured — built-in content stays available", ToastKindInfo)
			return
		}
		refreshCurationFeedAsync()
		ShowToast("Refreshing the community feed…", ToastKindInfo)
	case curationActionSubmitVotes:
		curationSubmitVotesUI()
	case curationActionToggleVote:
		if row.index < 0 || row.index >= len(curationPanel.targets) {
			return
		}
		target := curationPanel.targets[row.index]
		added, err := toggleCurationVote(&curationPanel.votes, target)
		if err != nil {
			ShowToast("Could not save the vote: "+firstLine(err.Error()), ToastKindError)
			return
		}
		curationPanel.voteIndex = curationVoteIndex(curationPanel.votes)
		if added {
			PlayActivateSound()
			ShowToast("Voted for "+target.Title, ToastKindSuccess)
		} else {
			PlayBackSound()
			ShowToast("Vote removed", ToastKindInfo)
		}
	case curationActionBack:
		curationBack()
	}
}

// curationSubmitVotesUI is shared by the Submit row and the Y button so both
// paths report the same outcome.
func curationSubmitVotesUI() {
	path, sent, err := submitCurationVotes(curationPanel.votes)
	if err != nil {
		if path != "" {
			ShowToast(firstLine(err.Error())+" — saved "+filepath.Base(path), ToastKindWarning)
		} else {
			ShowToast(firstLine(err.Error()), ToastKindWarning)
		}
		return
	}
	PlaySuccessSound()
	if sent {
		ShowToast("Votes submitted — thank you", ToastKindSuccess)
		return
	}
	ShowToast("Votes saved to "+filepath.Base(path)+" — share that file with the catalog maintainers", ToastKindInfo)
}

func curationShareProfile(index int) {
	if index < 0 || index >= len(curationPanel.profiles) {
		return
	}
	profile := curationPanel.profiles[index]
	path, err := exportCurationProfile(profile)
	if err != nil {
		ShowToast("Could not export the profile: "+firstLine(err.Error()), ToastKindError)
		return
	}
	PlaySuccessSound()
	ShowToast("Exported "+filepath.Base(path)+" — copy it from data/curation/export", ToastKindSuccess)
}

func curationDeleteProfile(index int) {
	if index < 0 || index >= len(curationPanel.profiles) {
		return
	}
	profile := curationPanel.profiles[index]
	if profile.Source != "local" {
		ShowToast("Only profiles saved here can be deleted", ToastKindInfo)
		return
	}
	if err := deleteCurationProfile(profile); err != nil {
		ShowToast("Could not delete the profile: "+firstLine(err.Error()), ToastKindError)
		return
	}
	curationPanel.profiles, curationPanel.profileIssues = allCurationProfiles()
	if curationPanel.cursor >= len(curationRows(appConfig)) {
		curationPanel.cursor = len(curationRows(appConfig)) - 1
	}
	ShowToast("Profile deleted", ToastKindSuccess)
}

func curationSecondary(config *Config) {
	rows := curationRows(config)
	if curationPanel.cursor < 0 || curationPanel.cursor >= len(rows) {
		return
	}
	if curationPanel.page == curationPageProfiles && rows[curationPanel.cursor].action == curationActionApplyProfile {
		curationDeleteProfile(rows[curationPanel.cursor].index)
	}
}

func curationTertiary(config *Config) {
	rows := curationRows(config)
	if curationPanel.cursor < 0 || curationPanel.cursor >= len(rows) {
		return
	}
	switch {
	case curationPanel.page == curationPageProfiles && rows[curationPanel.cursor].action == curationActionApplyProfile:
		curationShareProfile(rows[curationPanel.cursor].index)
	case curationPanel.page == curationPageVotes:
		curationSubmitVotesUI()
	}
}

func ctrlCuration(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || !curationPanel.open {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		curationMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		curationMove(1)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		curationPageMove(-1)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		curationPageMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		curationActivate(config)
	case sdl.CONTROLLER_BUTTON_X:
		curationSecondary(config)
	case sdl.CONTROLLER_BUTTON_Y:
		curationTertiary(config)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		curationBack()
	}
	return true
}

func handleCurationKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !curationPanel.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		curationMove(-1)
	case sdl.K_DOWN:
		curationMove(1)
	case sdl.K_PAGEUP:
		curationPageMove(-1)
	case sdl.K_PAGEDOWN:
		curationPageMove(1)
	case sdl.K_HOME:
		curationPanel.cursor = 0
	case sdl.K_END:
		curationPanel.cursor = len(curationRows(config)) - 1
		if curationPanel.cursor < 0 {
			curationPanel.cursor = 0
		}
	case sdl.K_RETURN, sdl.K_SPACE, sdl.K_a:
		curationActivate(config)
	case sdl.K_x, sdl.K_DELETE:
		curationSecondary(config)
	case sdl.K_y:
		curationTertiary(config)
	case sdl.K_r:
		refreshCurationPanel(config)
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		curationBack()
	}
	return true
}

func handleCurationMouse(mx, my int32, config *Config) bool {
	if !curationPanel.open {
		return false
	}
	for _, hit := range curationRowHits {
		if pointInRect(mx, my, hit.rect) {
			curationPanel.cursor = hit.index
			curationActivate(config)
			return true
		}
	}
	if curationPanel.rect.W > 0 && pointInRect(mx, my, curationPanel.rect) {
		return true
	}
	closeCurationPanel()
	return true
}

func curationPageSubtitle(panel *curationPanelState) string {
	switch panel.page {
	case curationPageEssentials:
		return "Fills gaps only · " + panel.essentials.Bundle.Name
	case curationPageProfiles:
		return "Theme + text size · A applies, Y exports"
	case curationPageVotes:
		if panel.scanning {
			return "Scanning the ROM library · ports already listed"
		}
		return "Mark what needs testing · A toggles a vote"
	}
	return "Curated for " + panel.device.Label + " · " + curationFeedLabel()
}

// renderCurationPanel draws the modal card. It mirrors the Save Manager layout
// so the hub feels like part of the same system.
func renderCurationPanel(renderer *sdl.Renderer, config *Config) {
	if !curationPanel.open || renderer == nil {
		return
	}
	rows := curationRows(config)
	if len(rows) == 0 {
		rows = []curationRow{{label: "Nothing here yet", detail: "Press B to close.", enabled: false}}
	}
	clampCurationCursor()

	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}
	smallH := int32(font.Height())
	medH := int32(medium.Height())

	const padX = int32(24)
	rowH := 8 + medH + 4 + smallH + 8
	headerH := 18 + medH + 4 + smallH + 14
	footerH := 20 + smallH

	panelW := int32(880)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	maxRows := (screenHeight - 40 - headerH - footerH) / rowH
	if maxRows < 3 {
		maxRows = 3
	}
	visible := int32(len(rows))
	if visible > maxRows {
		visible = maxRows
	}
	panelH := headerH + visible*rowH + footerH
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	curationPanel.rect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	if curationPanel.cursor < curationPanel.scroll {
		curationPanel.scroll = curationPanel.cursor
	}
	if curationPanel.cursor >= curationPanel.scroll+int(visible) {
		curationPanel.scroll = curationPanel.cursor - int(visible) + 1
	}
	if curationPanel.scroll > len(rows)-int(visible) {
		curationPanel.scroll = len(rows) - int(visible)
	}
	if curationPanel.scroll < 0 {
		curationPanel.scroll = 0
	}

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 180)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, ColorPanelRaised)
	strokeRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, 1, ColorBorderDefault)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 110)
	renderer.FillRect(&sdl.Rect{X: panelX + 18, Y: panelY + 1, W: panelW - 36, H: 2})

	title := curationPanelTtl
	switch curationPanel.page {
	case curationPageEssentials:
		title = "Curation Hub · Essentials"
	case curationPageProfiles:
		title = "Curation Hub · Profiles"
	case curationPageVotes:
		title = "Curation Hub · Votes"
	}
	renderText(renderer, config, medium, title, ColorTextPrimary(), panelX+padX, panelY+18)
	sub := curationPageSubtitle(&curationPanel)
	renderText(renderer, config, font, ellipsize(font, sub, panelW-2*padX), ColorTextTertiary(), panelX+padX, panelY+18+medH+4)

	focusColor := installHighlight()
	rowW := panelW - 2*padX
	curationRowHits = make([]curationRowHit, 0, visible)
	for i := int32(0); i < visible; i++ {
		idx := curationPanel.scroll + int(i)
		row := rows[idx]
		y := panelY + headerH + i*rowH
		r := sdl.Rect{X: panelX + padX, Y: y, W: rowW, H: rowH - 8}
		curationRowHits = append(curationRowHits, curationRowHit{index: idx, rect: r})

		focused := idx == curationPanel.cursor
		rowBg := WithAlpha(ColorSurfaceRow, 190)
		if focused {
			rowBg = WithAlpha(focusColor, 46)
		}
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, rowBg)
		if focused {
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, 1, focusColor)
		}

		labelColor := ColorTextPrimary()
		valueColor := ColorTextSecondary()
		detailColor := ColorTextTertiary()
		if !row.enabled && row.action != curationActionInfo {
			labelColor = ColorTextTertiary()
			valueColor = ColorTextTertiary()
		}
		if row.accent && row.enabled {
			valueColor = focusColor
		}
		renderText(renderer, config, medium, ellipsize(medium, row.label, r.W-260), labelColor, r.X+16, r.Y+8)
		if row.value != "" {
			vw, _, _ := font.SizeUTF8(row.value)
			renderText(renderer, config, font, ellipsize(font, row.value, 240), valueColor, r.X+r.W-16-int32(vw), r.Y+8+(medH-smallH)/2)
		}
		if row.detail != "" {
			renderText(renderer, config, font, ellipsize(font, row.detail, r.W-32), detailColor, r.X+16, r.Y+8+medH+4)
		}
	}

	hint := "A: open   B: close"
	switch curationPanel.page {
	case curationPageEssentials:
		hint = "A: apply   B: back"
	case curationPageProfiles:
		hint = "A: apply   X: delete saved   Y: export   B: back"
	case curationPageVotes:
		hint = "A: vote   Y: submit   B: back"
	}
	if len(rows) > int(visible) {
		hint = fmt.Sprintf("L/R: page %d-%d of %d   %s",
			curationPanel.scroll+1, curationPanel.scroll+int(visible), len(rows), hint)
	}
	renderText(renderer, config, font, hint, ColorTextSecondary(), panelX+padX, panelY+panelH-footerH+8)

	if len(rows) > int(visible) {
		thumbFrac := float64(visible) / float64(len(rows))
		scrollFrac := float64(curationPanel.scroll) / float64(len(rows)-int(visible))
		drawScrollbar(renderer, panelX+panelW-8, panelY+headerH, 3, panelH-headerH-footerH, thumbFrac, scrollFrac)
	}
}
