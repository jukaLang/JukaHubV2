package main

// toolspages.go — the small tool pages the Tools hub gained on 2026-11-23,
// and what they share: Connection check (conncheck*.go), What's using space
// (spaceuse*.go), Verify a file (filecheck*.go), Screen test (screentest.go)
// and the QR maker (qrmaker.go).
//
// The first four are Go-owned scenes injected in memory like the hub's own
// pages (toolsHubEnsureScene): no shipped config lists them, they carry no
// Icon so the Start menu does not list them twice, and Home search finds them
// by name (toolsPagesSearchEntries). Three are drawn with the tool screens'
// frame (tbxui.go: page header, focus list, rows and the details sheet); the
// Screen test fills the whole screen. Their clicks, wheel, footer and
// once-per-visit entry arrive through the Tools hub's hooks (toolsScenes* in
// toolshub.go), the way the Network tools and Wi-Fi pages get theirs; keys and
// buttons through the scene registry. The QR maker is a keyboard prompt and
// the full-screen QR card (qrzoom.go), with no scene of its own.

import "strings"

// The pages' scene names.
const (
	connCheckSceneName  = "ConnCheck"
	spaceUseSceneName   = "SpaceUse"
	fileCheckSceneName  = "FileCheck"
	screenTestSceneName = "ScreenTest"
)

// toolsPagesEnsureScenes appends whichever of the pages' scenes are missing.
// toolsHubEnsureScenes calls it: at startup, after every reload, and before
// every open.
func toolsPagesEnsureScenes(config *Config) {
	if config == nil {
		return
	}
	toolsHubEnsureScene(config, connCheckSceneName, "", "Find where the connection fails")
	toolsHubEnsureScene(config, spaceUseSceneName, "", "The folders and files that fill a drive")
	toolsHubEnsureScene(config, fileCheckSceneName, "", "A file's SHA-256, SHA-1, MD5 and CRC32")
	toolsHubEnsureScene(config, screenTestSceneName, "", "Colours and patterns for the display")
}

// toolsPagesIs reports whether name is one of the pages' scenes.
func toolsPagesIs(name string) bool {
	switch name {
	case connCheckSceneName, spaceUseSceneName, fileCheckSceneName, screenTestSceneName:
		return true
	}
	return false
}

// toolsPagesCurrent names the current scene when it is one of the pages, or "".
func toolsPagesCurrent(config *Config) string {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return ""
	}
	if name := config.Scenes[currentSceneIndex].Name; toolsPagesIs(name) {
		return name
	}
	return ""
}

// toolsPagesOpen switches to one of the pages. Panels that could still be up
// are put away first, as tbxOpenScene does: a scene change must never leave a
// modal floating over the next screen. Opening the page already on screen
// enters it again.
func toolsPagesOpen(config *Config, name string) bool {
	if config == nil {
		config = appConfig
	}
	if config == nil {
		return false
	}
	toolsHubEnsureScenes(config)
	idx := sceneIndexExact(config, name)
	if idx < 0 {
		return false
	}
	powerTools.open = false
	communityPanel.open = false
	qs.open = false
	if idx == currentSceneIndex {
		toolsPagesEnter(config, name)
		return true
	}
	changeSceneTo(config, idx)
	return true
}

// toolsPagesLive is the page entered last and not yet left ("" when the screen
// is none of them). Main thread only.
var toolsPagesLive string

// toolsPagesEnter runs on every completed scene transition (toolsScenesEnter,
// from core.go), after the new scene's task scope has started: the page being
// left lets go of what it holds, the page arriving starts its work.
func toolsPagesEnter(config *Config, name string) {
	if toolsPagesLive != "" && toolsPagesLive != name {
		toolsPagesLeave(toolsPagesLive)
	}
	toolsPagesLive = ""
	if !toolsPagesIs(name) {
		return
	}
	toolsPagesLive = name
	switch name {
	case connCheckSceneName:
		connCheckEnter()
	case spaceUseSceneName:
		spaceUseEnter(config)
	case fileCheckSceneName:
		fileCheckEnter(config)
	case screenTestSceneName:
		screenTestEnter()
	}
}

// toolsPagesLeave tells a page its screen was left.
func toolsPagesLeave(name string) {
	switch name {
	case fileCheckSceneName:
		fileCheckLeave()
	case screenTestSceneName:
		screenTestLeave()
	}
}

// toolsPagesNav is the focus list of a page drawn in the tool screens' frame.
func toolsPagesNav(name string) *tbxNav {
	switch name {
	case connCheckSceneName:
		return &connCheckUI.nav
	case spaceUseSceneName:
		return &spaceUI.nav
	case fileCheckSceneName:
		return fileCheckActiveNav()
	}
	return nil
}

// toolsPagesClick routes a click or tap: the open sheet takes it first, then
// the page's own rows.
func toolsPagesClick(mx, my int32, config *Config) bool {
	name := toolsPagesCurrent(config)
	if name == "" {
		return false
	}
	if name == screenTestSceneName {
		return screenTestClick(mx, my)
	}
	if tbxSheetVisible(config) {
		tbxSheetMouse(mx, my)
		return true
	}
	switch name {
	case connCheckSceneName:
		return connCheckClick(mx, my, config)
	case spaceUseSceneName:
		return spaceUseClick(mx, my, config)
	case fileCheckSceneName:
		return fileCheckClick(mx, my, config)
	}
	return false
}

// toolsPagesWheel scrolls the open sheet or the page's list. On the Screen
// test the wheel steps through the patterns.
func toolsPagesWheel(config *Config, y int32) bool {
	name := toolsPagesCurrent(config)
	if name == "" || y == 0 {
		return false
	}
	if name == screenTestSceneName {
		if y > 0 {
			screenTestStep(-1)
		} else {
			screenTestStep(1)
		}
		return true
	}
	if tbxSheetVisible(config) {
		if y > 0 {
			tbxSheetAction(tbxActUp)
		} else {
			tbxSheetAction(tbxActDown)
		}
		return true
	}
	if nav := toolsPagesNav(name); nav != nil {
		nav.scrollBy(-y * 48)
		return true
	}
	return false
}

// toolsPagesFooterHints is a page's footer: the open sheet's buttons, or what
// the page's keys do. ok is false for any other scene. The Screen test draws
// no footer; its row is what the Controls card (F1) lists.
func toolsPagesFooterHints(name string) ([]FooterHint, bool) {
	if !toolsPagesIs(name) {
		return nil, false
	}
	if name == screenTestSceneName {
		return screenTestHints(), true
	}
	if tbxSheetVisible(appConfig) {
		return tbxSheetHints(), true
	}
	switch name {
	case connCheckSceneName:
		return connCheckHints(), true
	case spaceUseSceneName:
		return spaceUseHints(), true
	case fileCheckSceneName:
		return fileCheckHints(), true
	}
	return nil, false
}

// toolsPagesDisplayName is the name a page shows under (the header caption,
// sceneDisplayName), or "" for any other scene.
func toolsPagesDisplayName(name string) string {
	switch name {
	case connCheckSceneName:
		return "Connection check"
	case spaceUseSceneName:
		return "What's using space"
	case fileCheckSceneName:
		return "Verify a file"
	case screenTestSceneName:
		return "Screen test"
	}
	return ""
}

// toolsPagesSearchEntries are the pages in Home search's catalog, with the
// words people look for them by.
func toolsPagesSearchEntries() []homeSearchEntry {
	return []homeSearchEntry{
		{title: "Connection check", where: "Tools", icon: "internet", open: connCheckOpen,
			keys: "connection check internet not working no internet wifi problem diagnose troubleshoot router dns offline why cannot connect"},
		{title: "What's using space", where: "Tools", icon: "chart", open: func(c *Config) { spaceUseOpen(c, "") },
			keys: "what is using space storage full card full sd card disk usage biggest largest folders files free up space clean up du"},
		{title: "Verify a file", where: "Tools", icon: "check", open: func(c *Config) { fileCheckOpen(c, "") },
			keys: "verify file checksum hash sha256 sha-256 sha1 sha-1 md5 crc crc32 integrity corrupt download rom dat compare"},
		{title: "Screen test", where: "Tools", icon: "monitor", open: screenTestOpen,
			keys: "screen test display dead pixel dead pixels stuck pixel backlight bleed colours colors banding lcd ips uniformity ghosting"},
		{title: "QR maker", where: "Tools", icon: "qr", open: qrMakerOpen,
			keys: "qr code qr maker make a qr generate qr text link url share to phone"},
	}
}

// toolsShowInFiles opens Files at path: a folder is browsed, a file is shown
// in its folder with the cursor on it (usbOtgOpenInFiles, chatShowInFiles).
func toolsShowInFiles(config *Config, path string, dir bool) {
	if strings.TrimSpace(path) == "" {
		return
	}
	if dir {
		PlayActivateSound()
		usbOtgOpenInFiles(config, path)
		return
	}
	chatShowInFiles(config, path)
}

// toolsPagesTone is the pill tone of a row whose work is running, failed,
// warned or passed (in that order of precedence), or neutral.
func toolsPagesTone(ok, warn, bad, busy bool) tbxTone {
	switch {
	case busy:
		return tbxToneAccent
	case bad:
		return tbxToneBad
	case warn:
		return tbxToneWarn
	case ok:
		return tbxToneGood
	}
	return tbxToneNeutral
}
