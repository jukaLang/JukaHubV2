package main

// xpackroute.go — the plumbing the platform screens (Experience packs, Config
// and Backup & restore) share with the rest of the app: their in-memory scenes
// and the click, wheel, footer and header-name hooks main.go and extras.go call
// once for all three.

// xpackEnsurePlatformScenes gives the three screens a scene each, in memory,
// like ensureAlarmScene: no shipped config carries them and the user's config
// file is never rewritten for them. They carry no monogram, so the Start menu
// leaves them to Settings and the Tools hub. Idempotent.
func xpackEnsurePlatformScenes(config *Config) {
	if config == nil {
		return
	}
	for _, s := range []struct{ name, description string }{
		{xpackSceneName, "Experience packs"},
		{cfgdocSceneName, "Config inspector"},
		{bkupSceneName, "Backup & restore"},
	} {
		if sceneIndexExact(config, s.name) >= 0 {
			continue
		}
		config.Scenes = append(config.Scenes, SceneConfig{Name: s.name, Description: s.description})
	}
}

// xpackPlatformScene names the current scene when it is one of the three.
func xpackPlatformScene(config *Config) string {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return ""
	}
	switch name := config.Scenes[currentSceneIndex].Name; name {
	case xpackSceneName, cfgdocSceneName, bkupSceneName:
		return name
	}
	return ""
}

// xpackRouteMouse routes a click or tap on a platform screen (main.go calls it
// after the top bar and the overlays have had theirs).
func xpackRouteMouse(mx, my int32, config *Config) bool {
	switch xpackPlatformScene(config) {
	case xpackSceneName:
		return xpackMouse(mx, my, config)
	case cfgdocSceneName:
		return cfgdocMouse(mx, my, config)
	case bkupSceneName:
		return bkupMouse(mx, my, config)
	}
	return false
}

// xpackRouteWheel scrolls a platform screen's list or open sheet.
func xpackRouteWheel(dy int32, config *Config) bool {
	switch xpackPlatformScene(config) {
	case xpackSceneName:
		return xpackWheel(dy)
	case cfgdocSceneName:
		return cfgdocWheel(dy)
	case bkupSceneName:
		return bkupWheel(dy)
	}
	return false
}

// xpackRouteFooterHints is a platform screen's footer (extras.go footerHints
// asks first), following the focused control and any open sheet.
func xpackRouteFooterHints(scene SceneConfig) ([]FooterHint, bool) {
	switch scene.Name {
	case xpackSceneName:
		return xpackHints(), true
	case cfgdocSceneName:
		return cfgdocHints(), true
	case bkupSceneName:
		return bkupHints(), true
	}
	return nil, false
}

// xpackDisplayName is the header's name for a platform screen, "" for any
// other scene (extras.go sceneDisplayName asks first).
func xpackDisplayName(name string) string {
	switch name {
	case xpackSceneName:
		return "Experience packs"
	case cfgdocSceneName:
		return "Config"
	case bkupSceneName:
		return "Backup & restore"
	}
	return ""
}
