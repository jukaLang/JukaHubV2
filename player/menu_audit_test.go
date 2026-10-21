package main

// Menu audit — walks every shipped config and checks that what the user can
// see is what the user can use:
//
//   * every scene has a renderer (a registered module or renderable elements),
//     so no menu row leads to a blank screen;
//   * every button trigger is handled somewhere in the non-test sources, so no
//     button is a no-op;
//   * every change_scene target exists, so no button navigates nowhere;
//   * interactive elements stay inside the content panel and do not overlap,
//     so no control is unreachable or clipped;
//   * every scene paints text once rendered offscreen, so a scene that draws
//     only a background is caught.
//
// The configs are the ones shipped with the app (desktop and handheld).

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// auditConfigNames are the configs the app ships.
var auditConfigNames = []string{"jukaconfig.json", "trimui/jukaconfig.device.json"}

// cancelPendingUserConfigSave drops the debounced jukauser.json write a config
// load schedules. The audit loads the shipped configs from the checkout, so
// without this a test run would rewrite the tracked user files a second later.
func cancelPendingUserConfigSave(t *testing.T) {
	t.Helper()
	userConfigSaveMutex.Lock()
	if userConfigSaveTimer != nil {
		userConfigSaveTimer.Stop()
	}
	pendingUserConfig = nil
	pendingUserConfigDir = ""
	userConfigSaveMutex.Unlock()
}

// loadAuditConfigs loads each shipped config, skipping when the test is not run
// from the player directory (the same guard the widget tests use).
func loadAuditConfigs(t *testing.T) map[string]*Config {
	t.Helper()
	out := map[string]*Config{}
	for _, name := range auditConfigNames {
		if _, err := os.Stat(name); err != nil {
			t.Skipf("config %s is not next to the test: %v", name, err)
		}
		config, _, err := LoadXMLConfigWithFallback(name)
		cancelPendingUserConfigSave(t)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		out[name] = config
	}
	return out
}

// interactiveElement reports whether an element is something the user presses.
func interactiveElement(el Element) bool {
	return el.Type == "button" || el.Type == "toggle"
}

// packageSources parses every non-test Go file in the package.
func packageSources(t *testing.T) map[string]*ast.File {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*ast.File{}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		out[name] = file
	}
	return out
}

// stringConstants maps the package's string constants to their values, so a
// trigger written as `case triggerAppRefresh:` resolves like the literal.
func stringConstants(t *testing.T) map[string]string {
	t.Helper()
	consts := map[string]string{}
	for _, file := range packageSources(t) {
		ast.Inspect(file, func(n ast.Node) bool {
			decl, ok := n.(*ast.GenDecl)
			if !ok || decl.Tok != token.CONST {
				return true
			}
			for _, spec := range decl.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok || len(value.Names) != len(value.Values) {
					continue
				}
				for i, expr := range value.Values {
					lit, ok := expr.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					if v, err := strconv.Unquote(lit.Value); err == nil {
						consts[value.Names[i].Name] = v
					}
				}
			}
			return true
		})
	}
	return consts
}

// triggerActionsFromSource returns the case labels of the switch in
// handleTrigger: exactly the trigger actions the app dispatches. Reading the
// call site beats searching for the string, which would also match a trigger
// that only survives in a comment or an unrelated table.
func triggerActionsFromSource(t *testing.T) map[string]bool {
	t.Helper()
	file := packageSources(t)["core.go"]
	if file == nil {
		t.Fatal("core.go is not in the package sources")
	}
	consts := stringConstants(t)
	actions := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "handleTrigger" {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			clause, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, expr := range clause.List {
				switch label := expr.(type) {
				case *ast.BasicLit:
					if label.Kind != token.STRING {
						continue
					}
					if value, err := strconv.Unquote(label.Value); err == nil {
						actions[value] = true
					}
				case *ast.Ident:
					if value, ok := consts[label.Name]; ok {
						actions[value] = true
					}
				}
			}
			return true
		})
	}
	if len(actions) == 0 {
		t.Fatal("no trigger actions found in handleTrigger")
	}
	return actions
}

// TestEveryButtonTriggerIsHandled fails when a configured button has no
// trigger, or a trigger that handleTrigger does not dispatch, so a button that
// looks pressable can never do anything.
func TestEveryButtonTriggerIsHandled(t *testing.T) {
	handled := triggerActionsFromSource(t)

	configs := loadAuditConfigs(t)
	for name, config := range configs {
		for _, scene := range config.Scenes {
			for _, el := range scene.Elements {
				if el.Type != "button" {
					continue
				}
				trigger := strings.TrimSpace(el.Trigger)
				if trigger == "" {
					// Home tiles are driven by the home focus graph, not by a
					// trigger string.
					if strings.EqualFold(el.Style, "tile") {
						continue
					}
					t.Errorf("%s / %s: button %q has no trigger", name, scene.Name, el.Text)
					continue
				}
				action, arg, _ := strings.Cut(trigger, ":")
				if action == "change_scene" || action == "set_variable" {
					if arg == "" {
						t.Errorf("%s / %s: button %q has trigger %q without a target",
							name, scene.Name, el.Text, trigger)
						continue
					}
					if action == "change_scene" {
						if sceneIndexExact(config, arg) < 0 {
							t.Errorf("%s / %s: button %q navigates to missing scene %q",
								name, scene.Name, el.Text, arg)
						}
					}
					continue
				}
				if !handled[action] {
					t.Errorf("%s / %s: button %q uses unhandled trigger %q",
						name, scene.Name, el.Text, action)
				}
			}
		}
	}
}

// TestEverySceneHasARenderer fails when a config scene would draw nothing:
// it is neither a registered module nor does it carry renderable elements.
func TestEverySceneHasARenderer(t *testing.T) {
	renderable := map[string]bool{
		"label": true, "button": true, "input": true, "searchresults": true,
		"dynamiclist": true, "textlog": true, "piechart": true, "favorites": true,
		"unitconverter": true, "chat": true, "shortslist": true, "canvas": true,
		"textbrowser": true, "packagelist": true, "recent": true, "image": true,
		"video": true, "themegallery": true, "toggle": true,
	}
	configs := loadAuditConfigs(t)
	for name, config := range configs {
		for _, scene := range config.Scenes {
			if _, ok := lookupScene(scene.Name); ok {
				continue
			}
			usable := false
			for _, el := range scene.Elements {
				if renderable[el.Type] {
					usable = true
					break
				}
			}
			if !usable {
				t.Errorf("%s: scene %q has neither a module nor renderable elements", name, scene.Name)
			}
		}
	}
}

// TestInteractiveElementsStayInBounds catches controls that run into the
// footer or off the screen, which makes them unreachable.
func TestInteractiveElementsStayInBounds(t *testing.T) {
	withScreen(t, probeW, probeH)
	configs := loadAuditConfigs(t)
	for name, config := range configs {
		for _, scene := range config.Scenes {
			if strings.EqualFold(scene.Layout, "home") {
				// Home is laid out from the measured card grid, not from the
				// config coordinates; home_render_test.go covers that geometry.
				continue
			}
			footerTop := probeH - bottomBarHeight(scene.Name)
			for _, el := range scene.Elements {
				if !interactiveElement(el) {
					continue
				}
				w, h := resolveElementDimensions(el, 320, 48)
				if el.X < 0 || el.Y < 0 || el.X+w > probeW {
					t.Errorf("%s / %s: %q at %d,%d size %dx%d is off screen",
						name, scene.Name, el.Text, el.X, el.Y, w, h)
				}
				if el.Y+h > footerTop {
					t.Errorf("%s / %s: %q ends at y=%d, inside the footer band from %d",
						name, scene.Name, el.Text, el.Y+h, footerTop)
				}
			}
		}
	}
}

// TestInteractiveElementsDoNotOverlap fails when two pressable controls cover
// each other; the one drawn first can never be clicked.
func TestInteractiveElementsDoNotOverlap(t *testing.T) {
	withScreen(t, probeW, probeH)
	configs := loadAuditConfigs(t)
	for name, config := range configs {
		for _, scene := range config.Scenes {
			if scene.Name == "Main" && strings.EqualFold(scene.Layout, "home") {
				continue // the home layout owns its own tile geometry
			}
			for i := 0; i < len(scene.Elements); i++ {
				a := scene.Elements[i]
				if !interactiveElement(a) {
					continue
				}
				aw, ah := resolveElementDimensions(a, 320, 48)
				for j := i + 1; j < len(scene.Elements); j++ {
					b := scene.Elements[j]
					if !interactiveElement(b) {
						continue
					}
					bw, bh := resolveElementDimensions(b, 320, 48)
					overlapX := a.X < b.X+bw && b.X < a.X+aw
					overlapY := a.Y < b.Y+bh && b.Y < a.Y+ah
					if overlapX && overlapY {
						t.Errorf("%s / %s: %q overlaps %q", name, scene.Name, a.Text, b.Text)
					}
				}
			}
		}
	}
}

// TestContentStaysInsidePanel fails when anything a scene draws reaches into
// the header or the footer band, where it would be covered by chrome.
func TestContentStaysInsidePanel(t *testing.T) {
	withScreen(t, probeW, probeH)
	for name, config := range loadAuditConfigs(t) {
		for _, scene := range config.Scenes {
			if strings.EqualFold(scene.Layout, "home") {
				continue // the home layout draws its own chrome and cards
			}
			footerTop := probeH - bottomBarHeight(scene.Name) - 16
			for _, el := range scene.Elements {
				w, h := resolveElementDimensions(el, 320, 48)
				if el.Y < HomeTopBarH+8 {
					t.Errorf("%s / %s: %q starts at y=%d, under the header bar",
						name, scene.Name, el.Text, el.Y)
				}
				if el.Y+h > footerTop {
					t.Errorf("%s / %s: %q ends at y=%d, below the content panel (%d)",
						name, scene.Name, el.Text, el.Y+h, footerTop)
				}
				if el.X < 24 || el.X+w > probeW-24 {
					t.Errorf("%s / %s: %q spans x=%d..%d, outside the 24px margins",
						name, scene.Name, el.Text, el.X, el.X+w)
				}
			}
		}
	}
}

// TestSharedScenesMatchAcrossConfigs keeps the handheld and desktop builds
// visually identical: both panels are 1280x720, so a scene that ships in both
// configs must have the same elements in the same places. Home is exempt
// because its tiles only carry activation triggers.
func TestSharedScenesMatchAcrossConfigs(t *testing.T) {
	configs := loadAuditConfigs(t)
	if len(configs) < 2 {
		t.Skip("needs both shipped configs")
	}
	desktop, device := configs["jukaconfig.json"], configs["trimui/jukaconfig.device.json"]
	if desktop == nil || device == nil {
		t.Fatal("both configs must load")
	}
	for _, ds := range desktop.Scenes {
		if strings.EqualFold(ds.Layout, "home") {
			continue
		}
		idx := sceneIndexExact(device, ds.Name)
		if idx < 0 {
			continue // handheld-only scenes are allowed to differ
		}
		got := device.Scenes[idx].Elements
		if len(got) != len(ds.Elements) {
			t.Errorf("%s: %d elements on the handheld, %d on the desktop",
				ds.Name, len(got), len(ds.Elements))
			continue
		}
		for i := range ds.Elements {
			a, b := ds.Elements[i], got[i]
			if a.Type != b.Type || a.Text != b.Text || a.Trigger != b.Trigger {
				t.Errorf("%s[%d]: desktop %s/%q/%q vs handheld %s/%q/%q", ds.Name, i,
					a.Type, a.Text, a.Trigger, b.Type, b.Text, b.Trigger)
			}
			aw, ah := resolveElementDimensions(a, 320, 48)
			bw, bh := resolveElementDimensions(b, 320, 48)
			if a.X != b.X || a.Y != b.Y || aw != bw || ah != bh {
				t.Errorf("%s[%d] %q: desktop at %d,%d %dx%d vs handheld at %d,%d %dx%d",
					ds.Name, i, a.Text, a.X, a.Y, aw, ah, b.X, b.Y, bw, bh)
			}
		}
	}
}

// startupInjectScenes applies the in-memory scene injections main() performs at
// startup, which is what makes the extra applets (Browser, Games, Audio, ...)
// reachable on configs that do not ship them.
func startupInjectScenes(config *Config) {
	ensureAlarmScene(config)
	ensureBrowserScene(config)
	ensureSettingsWidgets(config)
	ensureOnboardingEntry(config)
	ensureMusicScene(config)
	ensureGamesScene(config)
	ensurePlaytimeScene(config)
	ensurePlaytimeEntry(config)
}

// TestAppsMenuRowsResolve builds the Apps list the way the app does (with the
// startup injections) and checks that every built-in row can actually open
// something. A pruned config used to list applets whose scene it did not have.
func TestAppsMenuRowsResolve(t *testing.T) {
	for name, config := range loadAuditConfigs(t) {
		startupInjectScenes(config)
		entries := collectAppEntries(config, t.TempDir(), nil)
		if len(entries) == 0 {
			t.Errorf("%s: the Apps list has no rows", name)
		}
		for _, entry := range entries {
			if action := strings.TrimPrefix(entry.Path, builtinActionPrefix); action != entry.Path {
				if !builtinAppActionSupported(action) {
					t.Errorf("%s: Apps row %q uses unhandled action %q", name, entry.Name, action)
				}
				continue
			}
			scene, _ := appEntryTarget(entry)
			if scene == "" {
				continue // an external program from the apps folders
			}
			if sceneIndexExact(config, scene) < 0 {
				t.Errorf("%s: Apps row %q opens missing scene %q", name, entry.Name, scene)
			}
		}
	}
}

// TestStartMenuEveryEntryResolves checks the Apps menu: each row either opens a
// scene that exists or carries an action.
func TestStartMenuEveryEntryResolves(t *testing.T) {
	configs := loadAuditConfigs(t)
	for name, config := range configs {
		entries := getStartMenuApps(config)
		if len(entries) == 0 {
			t.Errorf("%s: the Apps menu is empty", name)
		}
		seen := map[string]bool{}
		for _, entry := range entries {
			if entry.name == "" {
				t.Errorf("%s: an Apps row has no name", name)
				continue
			}
			if seen[entry.name] {
				t.Errorf("%s: Apps row %q appears twice", name, entry.name)
			}
			seen[entry.name] = true
			if entry.scene == "" && entry.action == nil {
				t.Errorf("%s: Apps row %q has no scene and no action", name, entry.name)
			}
			if entry.scene != "" && sceneIndexExact(config, entry.scene) < 0 {
				t.Errorf("%s: Apps row %q points at missing scene %q", name, entry.name, entry.scene)
			}
			if entry.scene != "" && entry.action != nil {
				t.Errorf("%s: Apps row %q has both a scene and an action", name, entry.name)
			}
		}
	}
}

// layoutForHome returns the home layout as refreshHomeLayout would build it for
// the config's Main scene, with the real tile -> card mapping populated. The
// globals it relies on are restored when the test ends.
func layoutForHome(t *testing.T, config *Config) *HomeLayout {
	t.Helper()
	idx := sceneIndexExact(config, "Main")
	if idx < 0 {
		t.Fatal("config has no Main scene")
	}
	prevScene, prevConfig := currentSceneIndex, appConfig
	prevLayout, prevTiles := homeLayout, homeTileByIndex
	currentSceneIndex, appConfig = idx, config
	homeLayout = NewHomeLayout(probeW, probeH)
	homeTileByIndex = map[int]HomeFocusID{}
	refreshHomeLayout(config)
	t.Cleanup(func() {
		currentSceneIndex, appConfig = prevScene, prevConfig
		homeLayout, homeTileByIndex = prevLayout, prevTiles
	})
	return homeLayout
}

// TestEveryHomeTileIsFocusable checks that every tile declared on Main is
// attached to a drawn card. The home layout draws its own cards, so a tile
// that fails to map leaves an invisible press target behind.
func TestEveryHomeTileIsFocusable(t *testing.T) {
	for name, config := range loadAuditConfigs(t) {
		layout := layoutForHome(t, config)
		idx := sceneIndexExact(config, "Main")
		tiles := 0
		for i, el := range config.Scenes[idx].Elements {
			if !strings.EqualFold(el.Style, "tile") {
				continue
			}
			tiles++
			id, ok := homeTileByIndex[i]
			if !ok {
				t.Errorf("%s: tile %q on Main is not a card the home layout draws", name, el.Text)
				continue
			}
			if _, ok := layout.Items[id]; !ok {
				t.Errorf("%s: tile %q maps to %q, which has no layout item", name, el.Text, id)
			}
		}
		if tiles == 0 {
			t.Errorf("%s: the home scene declares no tiles", name)
		}
	}
}

// TestHomeCardsResolve checks that every card the home screen draws can be
// activated: its action either opens a scene the config actually has, or is
// handled by a dedicated overlay/card handler before the scene lookup.
func TestHomeCardsResolve(t *testing.T) {
	// Cards whose activation is an overlay or a direct action, not a scene.
	overlay := map[HomeFocusID]bool{
		HomeFocusContinue: true, HomeFocusClock: true, HomeFocusWeather: true,
		HomeFocusBell: true, HomeFocusUpdate: true, HomeFocusPS5: true,
		HomeFocusPS4: true, HomeFocusEmulator: true, HomeFocusExit: true,
	}
	for name, config := range loadAuditConfigs(t) {
		layout := layoutForHome(t, config)
		for _, id := range layout.Order {
			if _, ok := layout.Items[id]; !ok {
				t.Errorf("%s: home card %q has no item", name, id)
				continue
			}
			if _, ok := homeFocusGraph[id]; !ok {
				t.Errorf("%s: home card %q has no focus neighbours", name, id)
			}
			if overlay[id] {
				continue
			}
			target := homeSceneTarget(config, id)
			if target == "" {
				t.Errorf("%s: home card %q has no target", name, id)
				continue
			}
			if sceneIndexExact(config, target) < 0 {
				t.Errorf("%s: home card %q opens missing scene %q", name, id, target)
			}
		}
	}
}

// countSceneTextPixels renders a scene and counts bright (text-like) pixels and
// distinct colours, which distinguishes a drawn menu from an empty background.
func countSceneTextPixels(t *testing.T, configName string, config *Config, idx int) (bright, colours int) {
	t.Helper()
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)

	prevScene, prevConfig := currentSceneIndex, appConfig
	currentSceneIndex, appConfig = idx, config
	defer func() { currentSceneIndex, appConfig = prevScene, prevConfig }()

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderScene(renderer, config, config.Scenes[idx])
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("ReadPixels unavailable: %v", err)
	}
	// JUKAHUB_SCENE_DUMP_DIR=<dir> writes every scene to <dir>/<config>_<scene>.png
	// so a layout review can look at the real pixels instead of the config.
	if dir := os.Getenv("JUKAHUB_SCENE_DUMP_DIR"); dir != "" {
		file := strings.NewReplacer("/", "_", "\\", "_").Replace(configName) +
			"_" + config.Scenes[idx].Name + ".png"
		_ = writeFramePNG(filepath.Join(dir, file), f.buf)
	}
	seen := map[uint32]bool{}
	for i := 0; i+3 < len(f.buf); i += 4 {
		r, g, b := f.buf[i+2], f.buf[i+1], f.buf[i]
		seen[uint32(r)<<16|uint32(g)<<8|uint32(b)] = true
		if int(r)+int(g)+int(b) > 420 {
			bright++
		}
	}
	return bright, len(seen)
}

// TestEverySceneRendersSomething renders every scene of every config offscreen
// and requires visible text plus a non-trivial picture. A scene that draws only
// its background is a dead menu row.
func TestEverySceneRendersSomething(t *testing.T) {
	configs := loadAuditConfigs(t)
	withScreen(t, probeW, probeH)
	ClearTextCache()
	t.Cleanup(ClearTextCache)

	for name, config := range configs {
		for i := range config.Scenes {
			scene := config.Scenes[i]
			bright, colours := countSceneTextPixels(t, name, config, i)
			if bright < 60 {
				t.Errorf("%s / %s: %d text pixels — the scene looks empty", name, scene.Name, bright)
			}
			if colours < 8 {
				t.Errorf("%s / %s: only %d distinct colours — nothing was drawn", name, scene.Name, colours)
			}
		}
	}
}
