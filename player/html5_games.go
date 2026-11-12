package main

// html5_games.go — local HTML5 games in the Games Library. On TrimUI the
// built-in Canvas 2D game view runs them. On desktop, a loopback-only HTTP
// origin lets the system browser run games with relative assets and storage.

import (
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	html5LibrarySystem   = "HTML5"
	html5LibraryLauncher = "html5:browser"
	html5MaxGames        = 300
	html5MaxDepth        = 3
)

//go:embed html5_demos/*/*.html html5_demos/*/*.png html5_demos/LICENSE
var html5Demos embed.FS

var html5DemoNames = map[string]string{
	"snake":    "Neon Snake",
	"breakout": "Brick Bounce",
	"dodger":   "Star Dodger",
}

func html5PathKey(file string) string {
	file = filepath.Clean(file)
	if IsWindows() {
		return strings.ToLower(file)
	}
	return file
}

// html5DataRoot holds bundled demos and is also a place to copy games on a PC.
func html5DataRoot() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "HTML5")
}

// html5LibraryRoots also recognizes Games/HTML5 beside JukaHub and at the
// card's root. The data directory is last so a copied game can take precedence
// over an identically named bundled demo.
func html5LibraryRoots() []string {
	roots := []string{resolvePath(filepath.Join("Games", "HTML5"))}
	if IsTrimuiDevice() {
		roots = append(roots, filepath.Join(filepath.Dir(deviceRomsRoot), "Games", "HTML5"))
	}
	if data := html5DataRoot(); data != "" {
		roots = append(roots, data)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		if root == "" {
			continue
		}
		root = filepath.Clean(root)
		key := html5PathKey(root)
		if !seen[key] {
			seen[key] = true
			out = append(out, root)
		}
	}
	return out
}

// installHTML5Demos copies only absent files. A user can change a demo without
// a library scan or update silently replacing their copy.
func installHTML5Demos(dataRoot string) error {
	if dataRoot == "" {
		return nil
	}
	for id, name := range html5DemoNames {
		dest := filepath.Join(dataRoot, "Demos", name)
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return err
		}
		for _, file := range []string{"index.html", "cover.png", "LICENSE"} {
			target := filepath.Join(dest, file)
			if _, err := os.Stat(target); err == nil {
				continue
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			asset := "html5_demos/" + id + "/" + file
			if file == "LICENSE" {
				asset = "html5_demos/LICENSE"
			}
			data, err := html5Demos.ReadFile(asset)
			if err != nil {
				return err
			}
			if err := AtomicWrite(target, data, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

func html5EntryIn(dir string) string {
	for _, name := range []string{"index.html", "index.htm"} {
		candidate := filepath.Join(dir, name)
		if regularFile(candidate) {
			return candidate
		}
	}
	return ""
}

func html5CoverPath(entry string) string {
	dir := filepath.Dir(entry)
	stem := strings.TrimSuffix(filepath.Base(entry), filepath.Ext(entry))
	for _, base := range []string{"cover.png", "cover.jpg", "cover.jpeg", stem + ".png", stem + ".jpg"} {
		candidate := filepath.Join(dir, base)
		if regularFile(candidate) {
			return candidate
		}
	}
	return ""
}

func html5GameTitle(entry string) string {
	if strings.EqualFold(strings.TrimSuffix(filepath.Base(entry), filepath.Ext(entry)), "index") {
		return strings.ReplaceAll(filepath.Base(filepath.Dir(entry)), "_", " ")
	}
	return gameDisplayTitle(entry)
}

// scanHTML5Games accepts a self-contained .html file at a root or a folder
// containing index.html (up to three levels down). Once a folder has an entry
// point, its internal HTML files are assets, not separate games.
func scanHTML5Games(roots []string) []libraryGame {
	games := make([]libraryGame, 0)
	seen := map[string]bool{}
	var add func(string)
	add = func(entry string) {
		if len(games) >= html5MaxGames {
			return
		}
		absolute, err := filepath.Abs(entry)
		if err != nil || !regularFile(absolute) {
			return
		}
		key := html5PathKey(absolute)
		if seen[key] {
			return
		}
		seen[key] = true
		art := html5CoverPath(absolute)
		game := libraryGame{Path: absolute, System: html5LibrarySystem,
			Title: html5GameTitle(absolute), Art: art, HasArt: art != ""}
		if info, err := os.Stat(absolute); err == nil {
			game.AddedAt = info.ModTime()
		}
		games = append(games, game)
	}
	var walk func(string, int)
	walk = func(dir string, depth int) {
		if depth > html5MaxDepth || len(games) >= html5MaxGames {
			return
		}
		if depth > 0 {
			if entry := html5EntryIn(dir); entry != "" {
				add(entry)
				return
			}
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, item := range entries {
			if skipScanEntryName(item.Name()) || len(games) >= html5MaxGames {
				continue
			}
			child := filepath.Join(dir, item.Name())
			if item.IsDir() {
				walk(child, depth+1)
			} else if depth == 0 && (strings.EqualFold(filepath.Ext(item.Name()), ".html") || strings.EqualFold(filepath.Ext(item.Name()), ".htm")) {
				add(child)
			}
		}
	}
	for _, root := range roots {
		walk(root, 0)
	}
	sort.SliceStable(games, func(i, j int) bool {
		return strings.ToLower(games[i].Title) < strings.ToLower(games[j].Title)
	})
	return games
}

func libraryAddHTML5Games(lib *gameLibraryReport, seconds map[string]int64, roots []string, demoRoot string) {
	if lib == nil {
		return
	}
	if demoRoot != "" {
		if err := installHTML5Demos(demoRoot); err != nil {
			logWarn("library", "could not install HTML5 demos: %v", err)
		}
	}
	games := scanHTML5Games(roots)
	if len(games) == 0 {
		return
	}
	libraryDecorateGames(games, seconds)
	withArt := countCoveredGames(games)
	lib.Systems = append(lib.Systems, librarySystem{Name: html5LibrarySystem, Games: games, WithArt: withArt})
	lib.Total += len(games)
	lib.WithArt += withArt
	sortLibrarySystems(lib.Systems)
}

// html5GameServer serves only the selected game's directory. A random route
// token makes another local process unable to guess paths; os.Root prevents a
// symlink in an imported game from reaching outside that directory.
type html5GameServer struct {
	mu     sync.RWMutex
	base   string
	host   string
	srv    *http.Server
	routes map[string]*os.Root
	paths  map[string]string
}

func newHTML5GameServer() (*html5GameServer, error) {
	return newHTML5GameServerAt("127.0.0.1:0")
}

func newHTML5GameServerAt(address string) (*html5GameServer, error) {
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	s := &html5GameServer{
		base: "http://" + ln.Addr().String(), host: ln.Addr().String(),
		routes: map[string]*os.Root{}, paths: map[string]string{},
	}
	s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

func (s *html5GameServer) Close() {
	if s == nil {
		return
	}
	_ = s.srv.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, root := range s.routes {
		_ = root.Close()
		delete(s.routes, token)
	}
}

func (s *html5GameServer) URL(entry string) (string, error) {
	absolute, err := filepath.Abs(entry)
	if err != nil {
		return "", err
	}
	if !regularFile(absolute) || !strings.EqualFold(filepath.Ext(absolute), ".html") && !strings.EqualFold(filepath.Ext(absolute), ".htm") {
		return "", errors.New("HTML5 game entry point is missing")
	}
	dir := filepath.Dir(absolute)
	s.mu.Lock()
	defer s.mu.Unlock()
	token := s.paths[dir]
	if token == "" {
		root, err := os.OpenRoot(dir)
		if err != nil {
			return "", err
		}
		secret := make([]byte, 16)
		if _, err := rand.Read(secret); err != nil {
			_ = root.Close()
			return "", err
		}
		token = hex.EncodeToString(secret)
		s.routes[token] = root
		s.paths[dir] = token
	}
	return s.base + "/g/" + token + "/" + url.PathEscape(filepath.Base(absolute)), nil
}

func (s *html5GameServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Host != s.host || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	tail, ok := strings.CutPrefix(r.URL.Path, "/g/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	token, name, ok := strings.Cut(tail, "/")
	if !ok || token == "" {
		http.NotFound(w, r)
		return
	}
	s.mu.RLock()
	root := s.routes[token]
	s.mu.RUnlock()
	if root == nil {
		http.NotFound(w, r)
		return
	}
	if name == "" {
		name = "index.html"
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == ".." || segment == "." || strings.Contains(segment, "\\") || strings.ContainsRune(segment, 0) {
			http.NotFound(w, r)
			return
		}
	}
	file, err := root.Open(filepath.FromSlash(name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, filepath.Base(name), info.ModTime(), file)
}

var html5ServerState struct {
	sync.Mutex
	servers map[string]*html5GameServer
}

// Each game gets a repeatable port and therefore a separate browser origin.
// A browser's localStorage and IndexedDB are scoped to the origin, not the
// URL path: sharing one port would mix saves from unrelated imported games,
// while a random port on every launch would make them disappear after a reboot.
func html5PreferredPort(entry string, attempt int) int {
	key := html5PathKey(entry)
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s#%d", key, attempt)))
	return 20000 + int(binary.BigEndian.Uint16(sum[:2]))%28000
}

func html5Server(entry string) (*html5GameServer, error) {
	absolute, err := filepath.Abs(entry)
	if err != nil {
		return nil, err
	}
	key := html5PathKey(absolute)
	html5ServerState.Lock()
	defer html5ServerState.Unlock()
	if html5ServerState.servers == nil {
		html5ServerState.servers = map[string]*html5GameServer{}
	}
	if server := html5ServerState.servers[key]; server != nil {
		return server, nil
	}
	var lastErr error
	for attempt := 0; attempt < 16; attempt++ {
		address := fmt.Sprintf("127.0.0.1:%d", html5PreferredPort(key, attempt))
		server, err := newHTML5GameServerAt(address)
		if err == nil {
			if attempt > 0 {
				logWarn("library", "the usual HTML5 game address is in use; browser saves may be under a temporary origin")
			}
			html5ServerState.servers[key] = server
			return server, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// launchHTML5Game uses the built-in game view on TrimUI. On desktop the local
// server stays alive for the JukaHub process so browser tabs can be resumed.
func launchHTML5Game(game libraryGame) error {
	if IsTrimuiDevice() {
		return openHTML5GameInApp(game)
	}
	server, err := html5Server(game.Path)
	if err != nil {
		return fmt.Errorf("cannot start the local HTML5 game server: %w", err)
	}
	address, err := server.URL(game.Path)
	if err != nil {
		return err
	}
	if err := openExternalURL(address); err != nil {
		return fmt.Errorf("cannot open the web browser: %w", err)
	}
	gmetaNoteLaunch(game)
	recordGameLaunch(game)
	return nil
}
