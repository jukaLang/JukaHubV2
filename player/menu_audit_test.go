package main

// Menu audit — walks every shipped config and checks that what the user can
// see is what the user can use:
//
//   * every scene has a renderer (a registered module or renderable elements),
//     so no menu row leads to a blank screen;
//   * every button trigger is handled somewhere in the non-test sources, so no
//     button is a no-op;
//   * every change_scene target exists, so no button navigates nowhere;
//   * every Apps and Start menu row opens a scene that exists or runs an
//     action the app handles.
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
)

// auditConfigNames are the configs the app ships.
var auditConfigNames = []string{"jukaconfig.json", "trimui/jukaconfig.device.json"}

// cancelPendingUserConfigSave drops the debounced jukauser.json write a config
// load schedules. The audit loads the shipped configs from the checkout, so
// without this a test run would rewrite the tracked user files a second later.
func cancelPendingUserConfigSave(t testing.TB) {
	t.Helper()
	userConfigSaveMutex.Lock()
	if userConfigSaveTimer != nil {
		userConfigSaveTimer.Stop()
	}
	pendingUserConfig = nil
	pendingUserConfigDir = ""
	userConfigSaveMutex.Unlock()
}

// loadAuditConfigs loads each shipped config. The configs are committed next to
// the tests and Go runs a package's tests from its directory, so a missing file
// means the checkout or the working directory is broken — failing here is the
// point: the whole menu audit would otherwise evaporate without a word.
func loadAuditConfigs(t testing.TB) map[string]*Config {
	t.Helper()
	out := map[string]*Config{}
	for _, name := range auditConfigNames {
		if _, err := os.Stat(name); err != nil {
			t.Fatalf("config %s is not next to the test: %v", name, err)
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
	ensureToolsHubScene(config)
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
