package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

func TestHTML5InAppDemosRun(t *testing.T) {
	for id, title := range html5DemoNames {
		t.Run(id, func(t *testing.T) {
			entry := filepath.Join("html5_demos", id, "index.html")
			view, err := newHTML5GameView(libraryGame{Path: entry, System: html5LibrarySystem, Title: title})
			if err != nil {
				t.Fatal(err)
			}
			if len(view.commands) == 0 {
				t.Fatal("the opening frame drew nothing")
			}
			if id != "snake" {
				if hud, ok := gojaHUD(view); !ok || !strings.Contains(hud, "Lives") {
					t.Fatalf("missing in-game score display: %q", hud)
				}
			}
			view.event("keydown", "Enter", 0, 0, 0)
			view.event("keydown", "ArrowLeft", 0, 0, 0)
			view.event("keyup", "ArrowLeft", 0, 0, 0)
			for i := 0; i < 20; i++ {
				view.frame(time.Now().Add(time.Duration(i+1) * 20 * time.Millisecond))
			}
			if view.errorText != "" || len(view.commands) == 0 || len(view.commands) > 1000 {
				t.Fatalf("game stopped or failed to redraw its canvas: %s (%d commands)", view.errorText, len(view.commands))
			}
		})
	}
}

func TestHTML5InAppLocalScriptAndPad(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "index.html")
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry, []byte(`<canvas id="game" width="120" height="80"></canvas><script src="assets/main.js"></script>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "main.js"), []byte(`const c=document.getElementById('game').getContext('2d');c.fillStyle='#123456';c.fillRect(0,0,120,80);`), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := newHTML5GameView(libraryGame{Path: entry, Title: "Local", System: html5LibrarySystem})
	if err != nil || len(v.commands) != 1 || v.commands[0].Color != "#123456" {
		t.Fatalf("local script did not draw its canvas: %v, %+v", err, v)
	}
	for button, want := range map[uint8]int{
		sdl.CONTROLLER_BUTTON_A:          0,
		sdl.CONTROLLER_BUTTON_START:      9,
		sdl.CONTROLLER_BUTTON_DPAD_UP:    12,
		sdl.CONTROLLER_BUTTON_DPAD_DOWN:  13,
		sdl.CONTROLLER_BUTTON_DPAD_LEFT:  14,
		sdl.CONTROLLER_BUTTON_DPAD_RIGHT: 15,
	} {
		if got := html5WebGamepadButton(button); got != want {
			t.Errorf("button %d maps to %d, want %d", button, got, want)
		}
	}
	if err := os.WriteFile(entry, []byte(`<canvas width="120" height="80"></canvas><script src="../outside.js"></script>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadHTML5Package(entry); err == nil {
		t.Fatal("script traversal was accepted")
	}
}

func TestHTML5DemosAndScan(t *testing.T) {
	root := t.TempDir()
	if err := installHTML5Demos(root); err != nil {
		t.Fatal(err)
	}
	modified := filepath.Join(root, "Demos", "Neon Snake", "index.html")
	if err := os.WriteFile(modified, []byte("custom copy"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installHTML5Demos(root); err != nil { // an update keeps existing files
		t.Fatal(err)
	}
	if kept, err := os.ReadFile(modified); err != nil || string(kept) != "custom copy" {
		t.Fatalf("modified demo was replaced: %q, %v", kept, err)
	}
	for _, name := range html5DemoNames {
		for _, asset := range []string{"index.html", "cover.png", "LICENSE"} {
			if !regularFile(filepath.Join(root, "Demos", name, asset)) {
				t.Fatalf("%s is missing %s", name, asset)
			}
		}
	}
	custom := filepath.Join(root, "My_Game")
	if err := os.MkdirAll(filepath.Join(custom, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(custom, "index.html"), []byte("<title>My Game</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(custom, "assets", "help.html"), []byte("help"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Solo.html"), []byte("solo"), 0o644); err != nil {
		t.Fatal(err)
	}
	games := scanHTML5Games([]string{root, root})
	if len(games) != 5 {
		t.Fatalf("found %d games, want three demos and two added games: %+v", len(games), games)
	}
	titles := map[string]bool{}
	for _, game := range games {
		if game.System != html5LibrarySystem {
			t.Errorf("%s has system %q", game.Title, game.System)
		}
		titles[game.Title] = true
	}
	for _, title := range []string{"Neon Snake", "Brick Bounce", "Star Dodger", "My Game", "Solo"} {
		if !titles[title] {
			t.Errorf("%s not scanned", title)
		}
	}
	if got := resolveGameLauncher(html5LibrarySystem); got != html5LibraryLauncher {
		t.Errorf("HTML5 launcher = %q", got)
	}
}

func TestHTML5GameServerOnlyServesGameFolder(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "game")
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		filepath.Join(dir, "index.html"):        "<h1>Playable</h1>",
		filepath.Join(dir, "assets", "game.js"): "let score = 1;",
		filepath.Join(parent, "private.txt"):    "private",
	} {
		if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, err := newHTML5GameServer()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	gameURL, err := s.URL(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	for suffix, want := range map[string]string{
		"":               "<h1>Playable</h1>",
		"assets/game.js": "let score = 1;",
	} {
		address := gameURL
		if suffix != "" {
			address = strings.TrimSuffix(gameURL, "index.html") + suffix
		}
		resp, err := http.Get(address)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || string(body) != want {
			t.Errorf("%s: status %d body %q", suffix, resp.StatusCode, body)
		}
	}
	u, err := url.Parse(gameURL)
	if err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSuffix(u.Path, "index.html")
	for _, bad := range []string{base + "../private.txt", base + "assets/", "/g/unknown/index.html"} {
		req := httptest.NewRequest(http.MethodGet, "http://"+s.host+bad, nil)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s returned %d", bad, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, gameURL, nil)
	req.Host = "example.com"
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("different Host returned %d", rec.Code)
	}
	link := filepath.Join(dir, "outside.txt")
	if err := os.Symlink(filepath.Join(parent, "private.txt"), link); err == nil {
		resp, err := http.Get(strings.TrimSuffix(gameURL, "index.html") + "outside.txt")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("outside symlink returned %d", resp.StatusCode)
		}
	}
}

func TestHTML5GameOptionsOnlyShowLibrarySettings(t *testing.T) {
	priorGame, priorChoice := gameOptsUI.game, gameOptsUI.choice
	t.Cleanup(func() { gameOptsUI.game, gameOptsUI.choice = priorGame, priorChoice })
	gameOptsUI.game.System = html5LibrarySystem
	gameOptsUI.choice = gameOptionsChoiceNone
	if got := gameOptionsRows(); got != 5 {
		t.Fatalf("HTML5 options show %d rows, want 5", got)
	}
	for i, want := range []string{"Display title", "Visibility", "Library sort", "Library view", "Show hidden"} {
		if got := gameOptionsRowName(i); got != want {
			t.Errorf("HTML5 option %d is %q, want %q", i, got, want)
		}
	}
	gameOptsUI.game.System = "NES"
	if got := gameOptionsRows(); got != gameOptionsRowCount {
		t.Errorf("ROM options show %d rows, want %d", got, gameOptionsRowCount)
	}
}

func TestHTML5GamesUseStableSeparateOrigins(t *testing.T) {
	root := t.TempDir()
	entries := []string{filepath.Join(root, "first", "index.html"), filepath.Join(root, "second", "index.html")}
	for _, entry := range entries {
		if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(entry, []byte("<html>game</html>"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var opened []*html5GameServer
	t.Cleanup(func() {
		html5ServerState.Lock()
		for _, entry := range entries {
			delete(html5ServerState.servers, html5PathKey(entry))
		}
		html5ServerState.Unlock()
		for _, server := range opened {
			server.Close()
		}
	})
	first, err := html5Server(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	opened = append(opened, first)
	again, err := html5Server(entries[0])
	if err != nil || again != first {
		t.Fatalf("the same game did not reuse its origin: %v", err)
	}
	second, err := html5Server(entries[1])
	if err != nil {
		t.Fatal(err)
	}
	opened = append(opened, second)
	if first.host == second.host {
		t.Fatal("two games shared a browser origin")
	}
	if IsWindows() {
		if got, want := html5PreferredPort(entries[0], 0), html5PreferredPort(strings.ToUpper(entries[0]), 0); got != want {
			t.Errorf("preferred port changed with path case: %d, %d", got, want)
		}
	}
}
