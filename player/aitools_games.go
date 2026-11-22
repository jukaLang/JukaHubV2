package main

// aitools_games.go — Juka's game tools: the library, "why won't it start?"
// (gamediag.go's checks), RetroArch's options and the performance setting.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

func aiGameTools() []aiTool {
	return []aiTool{
		{
			Name: "list_games",
			Desc: "The game library: with no arguments, the consoles and how many games each has; with query and/or system, the matching games with their file paths.",
			Params: aiObj(map[string]any{
				"query":  aiStrP("Part of a game's title"),
				"system": aiStrP("A console name as list_games shows it, e.g. GBA, SNES, PS"),
				"limit":  aiIntP("Most games to list, 1-60 (default 25)"),
			}),
			Risk: aiRiskRead,
			Run:  aiListGames,
		},
		{
			Name: "diagnose_game",
			Desc: "Check why a game won't start: the game file, the emulator and core, the BIOS files it needs, and the last launch's log. With no arguments it checks the game launched last.",
			Params: aiObj(map[string]any{
				"title": aiStrP("The game's title (or part of it)"),
				"path":  aiStrP("The game file's path, if known"),
			}),
			Risk: aiRiskRead,
			Run:  aiDiagnoseGame,
		},
		{
			Name: "retroarch_get",
			Desc: "Read RetroArch options: one key, or every key containing search. With core (e.g. \"Snes9x\", \"mGBA\"), also that core's override file.",
			Params: aiObj(map[string]any{
				"key":    aiStrP("An exact retroarch.cfg key, e.g. video_smooth"),
				"search": aiStrP("List the keys containing this text, e.g. video_ or audio_latency"),
				"core":   aiStrP("A core name for its override file (config/<core>/<core>.cfg)"),
			}),
			Risk: aiRiskRead,
			Run:  aiRetroArchGet,
		},
		{
			Name: "retroarch_set",
			Desc: "Change one RetroArch option, in retroarch.cfg or only for one core (an override). Folders, paths and accounts are refused. A backup of retroarch.cfg is kept. The user is asked first.",
			Params: aiObj(map[string]any{
				"key":   aiStrP("The retroarch.cfg key"),
				"value": aiStrP("The new value, e.g. true, false, 2"),
				"core":  aiStrP("Only for this core (an override); empty = for every game"),
			}, "key", "value"),
			Risk: aiRiskChange,
			Run:  aiRetroArchSet,
		},
		{
			Name:   "performance_get",
			Desc:   "The performance settings now: power mode and its choices, automatic CPU speed, the CPU clock and temperature.",
			Params: aiObj(nil),
			Risk:   aiRiskRead,
			Run:    aiPerformanceGet,
		},
		{
			Name: "performance_set",
			Desc: "Set the power mode (a value performance_get lists) and/or automatic CPU speed. The user is asked first.",
			Params: aiObj(map[string]any{
				"power_mode": aiStrP("A power mode value from performance_get"),
				"auto_cpu":   aiStrP("on or off"),
			}),
			Risk: aiRiskChange,
			Run:  aiPerformanceSet,
		},
	}
}

// ── The library ──────────────────────────────────────────────────────────

// aiLibCache keeps one scan for a few minutes: an answer may call the
// library tools several times.
var aiLibCache struct {
	mu  sync.Mutex
	at  time.Time
	lib gameLibraryReport
}

func aiLibrary() gameLibraryReport {
	aiLibCache.mu.Lock()
	defer aiLibCache.mu.Unlock()
	if time.Since(aiLibCache.at) < 5*time.Minute && len(aiLibCache.lib.Systems) > 0 {
		return aiLibCache.lib
	}
	// An empty playtime map: the live ledger is the render thread's.
	lib := buildGameLibraryWith(scanROMLibrary(romLibraryRoots()), libraryArtworkRoot(), map[string]int64{})
	aiLibCache.lib, aiLibCache.at = lib, time.Now()
	return lib
}

func aiListGames(ctx context.Context, a aiArgs) aiStep {
	lib := aiLibrary()
	query := strings.ToLower(a.str("query"))
	system := strings.ToLower(a.str("system"))
	limit := a.num("limit", 25, 1, 60)
	var b strings.Builder
	if query == "" && system == "" {
		fmt.Fprintf(&b, "%d games in %d consoles:\n", lib.Total, len(lib.Systems))
		for _, s := range lib.Systems {
			fmt.Fprintf(&b, "%s: %d\n", s.Name, len(s.Games))
		}
		return aiStep{Result: b.String(), Status: "Looked at the game library"}
	}
	n := 0
	for _, s := range lib.Systems {
		if system != "" && !strings.Contains(strings.ToLower(s.Name), system) {
			continue
		}
		for _, g := range s.Games {
			if query != "" && !strings.Contains(strings.ToLower(g.Label()), query) && !strings.Contains(strings.ToLower(filepath.Base(g.Path)), query) {
				continue
			}
			if n < limit {
				fmt.Fprintf(&b, "%s [%s] %s\n", g.Label(), s.Name, g.Path)
			}
			n++
		}
	}
	if n == 0 {
		return aiStep{Result: "No game matches.", Status: "Searched the game library"}
	}
	if n > limit {
		fmt.Fprintf(&b, "… %d more\n", n-limit)
	}
	return aiStep{Result: b.String(), Status: "Searched the game library"}
}

// aiFindGame picks the game a diagnose call means.
func aiFindGame(title, path string) (libraryGame, bool) {
	if path != "" {
		if p, err := aiPath(path); err == nil {
			path = p
		}
	}
	if path == "" && title == "" {
		if recent := recentGames(); len(recent) > 0 {
			r := recent[0]
			return libraryGame{Path: r.Path, System: r.System, Title: r.Title}, true
		}
		return libraryGame{}, false
	}
	lib := aiLibrary()
	want := strings.ToLower(title)
	var best libraryGame
	bestScore := 0
	for _, s := range lib.Systems {
		// A built-in game has no ROM or emulator to diagnose; its title
		// (Tetris, Pong…) must not win over a ROM of the same name.
		if s.Name == builtInLibrarySystem {
			continue
		}
		for _, g := range s.Games {
			if path != "" && filepath.Clean(g.Path) == filepath.Clean(path) {
				return g, true
			}
			if want == "" {
				continue
			}
			label := strings.ToLower(g.Label())
			score := 0
			switch {
			case label == want:
				score = 3
			case strings.HasPrefix(label, want):
				score = 2
			case strings.Contains(label, want):
				score = 1
			}
			if score > bestScore {
				best, bestScore = g, score
			}
		}
	}
	if bestScore > 0 {
		return best, true
	}
	if path != "" {
		if _, err := os.Stat(path); err == nil {
			for _, r := range recentGames() {
				if filepath.Clean(r.Path) == filepath.Clean(path) {
					return libraryGame{Path: r.Path, System: r.System, Title: r.Title}, true
				}
			}
		}
	}
	return libraryGame{}, false
}

func aiDiagnoseGame(ctx context.Context, a aiArgs) aiStep {
	game, ok := aiFindGame(a.str("title"), a.str("path"))
	if !ok {
		return aiFail("no such game in the library (list_games finds titles)")
	}
	if game.System == androidLibrarySystem {
		return aiStep{Result: game.Label() + " is an Android game: it starts through its game profile; there is nothing to check here.", Status: "Checked " + game.Label()}
	}
	launcher := resolveGameLauncherFor(game)
	gameDiagState.mu.Lock()
	sameGame := gameDiagState.lastGame == game.Path
	gameDiagState.mu.Unlock()
	logPath := ""
	if sameGame {
		if dir := gameDiagLogDir(); dir != "" {
			logPath = filepath.Join(dir, "retroarch.log")
		}
	}
	report, failed := diagnoseLibraryGame(game, launcher, logPath)
	var b strings.Builder
	fmt.Fprintf(&b, "Game: %s [%s]\nFile: %s\n", game.Label(), game.System, game.Path)
	if launcher != "" {
		fmt.Fprintf(&b, "Emulator: %s\n", filepath.Base(launcher))
	}
	if len(report.Issues) == 0 {
		b.WriteString("Checks: nothing missing (game file, core and BIOS were checked).\n")
	} else {
		fmt.Fprintf(&b, "Found %d problem(s):\n", len(report.Issues))
		for _, is := range report.Issues {
			fmt.Fprintf(&b, "- %s", is.Title)
			if is.Detail != "" {
				fmt.Fprintf(&b, ": %s", is.Detail)
			}
			if len(is.Files) > 0 {
				fmt.Fprintf(&b, " Files: %s.", strings.Join(is.Files, ", "))
			}
			if is.Where != "" {
				fmt.Fprintf(&b, " Folder: %s.", is.Where)
			}
			b.WriteString("\n")
		}
	}
	switch {
	case !sameGame:
		b.WriteString("No log: this was not the last game launched.\n")
	case len(report.LogLines) > 0:
		if failed {
			b.WriteString("The last launch's log says loading failed:\n")
		} else {
			b.WriteString("The last launch's log:\n")
		}
		for _, l := range report.LogLines {
			b.WriteString("  " + l + "\n")
		}
	}
	return aiStep{Result: b.String(), Status: "Checked " + game.Label()}
}

// ── RetroArch ────────────────────────────────────────────────────────────

var (
	aiRAKeyRe  = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[a-z0-9_]{2,64}$`) })
	aiRACoreRe = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _+.-]{0,47}$`) })
)

// aiRAKeyRefused reports a key Juka may not change: folders, paths, the core
// and config files, accounts and passwords, and how the config is saved.
func aiRAKeyRefused(key string) bool {
	for _, w := range []string{"directory", "_path", "path_", "libretro", "config", "password", "token", "username", "_dir", "netplay_mitm", "cheevos_", "menu_driver", "video_driver", "audio_driver", "input_driver", "core_updater", "bundle_assets", "log_"} {
		if strings.Contains(key, w) {
			return true
		}
	}
	return false
}

// aiRACorePath is a core's override file beside retroarch.cfg.
func aiRACorePath(cfgPath, core string) string {
	return filepath.Join(filepath.Dir(cfgPath), "config", core, core+".cfg")
}

func aiRetroArchGet(ctx context.Context, a aiArgs) aiStep {
	text, path, ok := retroArchConfig()
	if !ok {
		return aiFail("no retroarch.cfg was found on this device")
	}
	key := strings.ToLower(a.str("key"))
	search := strings.ToLower(a.str("search"))
	core := a.str("core")
	var b strings.Builder
	fmt.Fprintf(&b, "retroarch.cfg: %s\n", path)
	switch {
	case key != "":
		if v, found := raConfigGet(text, key); found {
			fmt.Fprintf(&b, "%s = %q\n", key, v)
		} else {
			fmt.Fprintf(&b, "%s is not set (RetroArch uses its default)\n", key)
		}
	case search != "":
		n := 0
		for _, line := range strings.Split(text, "\n") {
			k, v, _, okl := raConfigLine(line)
			if !okl || !strings.Contains(strings.ToLower(k), search) {
				continue
			}
			if n < 60 {
				fmt.Fprintf(&b, "%s = %q\n", k, v)
			}
			n++
		}
		if n > 60 {
			fmt.Fprintf(&b, "… %d more\n", n-60)
		}
		if n == 0 {
			b.WriteString("no key contains that\n")
		}
	}
	if core != "" {
		if !aiRACoreRe().MatchString(core) {
			return aiFail("that is not a core name")
		}
		cp := aiRACorePath(path, core)
		data, err := os.ReadFile(cp)
		switch {
		case err != nil:
			fmt.Fprintf(&b, "%s has no override file (%s)\n", core, cp)
		case key != "":
			if v, found := raConfigGet(string(data), key); found {
				fmt.Fprintf(&b, "%s override: %s = %q\n", core, key, v)
			} else {
				fmt.Fprintf(&b, "%s override does not set %s\n", core, key)
			}
		default:
			fmt.Fprintf(&b, "%s override (%s):\n%s\n", core, cp, strings.TrimSpace(string(data)))
		}
	}
	return aiStep{Result: b.String(), Status: "Read RetroArch's settings"}
}

func aiRetroArchSet(ctx context.Context, a aiArgs) aiStep {
	key := strings.ToLower(a.str("key"))
	value := a.str("value")
	core := a.str("core")
	if !aiRAKeyRe().MatchString(key) || aiRAKeyRefused(key) {
		return aiFail("%q can't be changed by Juka (folders, paths, accounts and drivers stay as they are)", key)
	}
	if len(value) > 120 || strings.ContainsAny(value, "\"\n\r\x00") {
		return aiFail("that value can't go in retroarch.cfg")
	}
	if core != "" && !aiRACoreRe().MatchString(core) {
		return aiFail("that is not a core name")
	}
	text, path, ok := retroArchConfig()
	if !ok {
		return aiFail("no retroarch.cfg was found on this device")
	}
	target := path
	where := "for every game"
	current, found := raConfigGet(text, key)
	if core != "" {
		target = aiRACorePath(path, core)
		where = "for " + core + " games only"
		if data, err := os.ReadFile(target); err == nil {
			if v, f := raConfigGet(string(data), key); f {
				current, found = v, f
			}
		}
	}
	from := "unset"
	if found {
		from = current
	}
	if found && raValueEqual(current, value) {
		return aiStep{Result: key + " is already " + value + " " + where + "."}
	}
	return aiStep{
		Confirm: fmt.Sprintf("Change RetroArch's %s from %s to %s %s?", key, from, value, where),
		Apply: func(ctx context.Context) string {
			if _, err := os.Stat(target); os.IsNotExist(err) && core != "" {
				if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					return "Error: " + err.Error()
				}
				if err := AtomicWrite(target, []byte(key+` = "`+value+`"`+"\n"), 0o644); err != nil {
					return "Error: " + err.Error()
				}
				logInfo("aitools", "RetroArch override %s: %s set at the user's OK", core, key)
				return "Set " + key + " = " + value + " " + where + " (new override file). It applies the next time a game starts."
			}
			changed, err := editRetroArchConfig(target, func(cfg string) (string, bool) { return raConfigSet(cfg, key, value) })
			if err != nil {
				return "Error: " + err.Error()
			}
			if !changed {
				return key + " was already " + value + "."
			}
			logInfo("aitools", "RetroArch %s set at the user's OK", key)
			return "Set " + key + " = " + value + " " + where + ". It applies the next time a game starts."
		},
	}
}

// ── Performance ──────────────────────────────────────────────────────────

func aiPerformanceGet(ctx context.Context, a aiArgs) aiStep {
	var out string
	err := aiOnMain(ctx, func() {
		c := appConfig
		var b strings.Builder
		if row, _, ok := aiFindRow(c, "powerMode"); ok {
			fmt.Fprintf(&b, "Power mode: %s (choices: %s)\n", aiRowValue(c, row), aiRowChoices(c, row))
		}
		if row, _, ok := aiFindRow(c, "autoPerformance"); ok {
			fmt.Fprintf(&b, "Automatic CPU speed: %s — %s\n", aiRowValue(c, row), autoPerfSettingDetail(c))
		}
		// The hub's own clock while it is used (uiboost.go).
		if row, _, ok := aiFindRow(c, uiBoostSetting); ok {
			fmt.Fprintf(&b, "Faster menus: %s — %s\n", aiRowValue(c, row), uiBoostSettingDetail(c))
		}
		// The boost and the heat guard: why a game runs at the clock it does
		// (cpuboost.go, heatguard.go).
		if row, _, ok := aiFindRow(c, cpuBoostKey); ok {
			fmt.Fprintf(&b, "CPU boost in games: %s — %s\n", aiRowValue(c, row), cpuBoostSettingDetail(c))
		}
		if row, _, ok := aiFindRow(c, heatLimitKey); ok {
			fmt.Fprintf(&b, "Heat limit: %s — %s\n", aiRowValue(c, row), heatLimitSettingDetail(c))
		}
		out = b.String()
	})
	if err != nil {
		return aiFail("%v", err)
	}
	if t, ok := cpuTempValueC(); ok {
		out += fmt.Sprintf("Temperature: %.0f °C\n", t)
	}
	if mhz := sysToolCPUFreqMHz(); mhz > 0 {
		out += fmt.Sprintf("CPU now: %d MHz\n", mhz)
	}
	return aiStep{Result: out, Status: "Checked performance settings"}
}

func aiPerformanceSet(ctx context.Context, a aiArgs) aiStep {
	type change struct{ id, value string }
	var changes []change
	if v := a.str("power_mode"); v != "" {
		changes = append(changes, change{"powerMode", v})
	}
	if v := a.str("auto_cpu"); v != "" {
		changes = append(changes, change{"autoPerformance", v})
	}
	if len(changes) == 0 {
		return aiFail("give power_mode and/or auto_cpu")
	}
	var lines []string
	var problem string
	err := aiOnMain(ctx, func() {
		for _, ch := range changes {
			line, perr := aiPlanRow(appConfig, ch.id, ch.value)
			if perr != nil {
				problem = perr.Error()
				return
			}
			if line != "" {
				lines = append(lines, line)
			}
		}
	})
	if err != nil {
		return aiFail("%v", err)
	}
	if problem != "" {
		return aiFail("%s", problem)
	}
	if len(lines) == 0 {
		return aiStep{Result: "Already set that way."}
	}
	return aiStep{
		Confirm: strings.Join(lines, "\n"),
		Apply: func(ctx context.Context) string {
			var done []string
			err := aiOnMain(ctx, func() {
				for _, ch := range changes {
					if msg, err := aiApplyRow(appConfig, ch.id, ch.value); err == nil {
						done = append(done, msg)
					} else {
						done = append(done, "Error: "+err.Error())
					}
				}
			})
			if err != nil {
				return "Error: " + err.Error()
			}
			sort.Strings(done)
			return strings.Join(done, "\n")
		},
	}
}
