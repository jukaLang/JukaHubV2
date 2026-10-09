package main

import (
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// ConfigWatcher monitors the project's config files for changes and triggers a
// reload. It watches jukaconfig.json and jukaconfig.xml together and resolves
// between them with ResolveConfigPath, so a hot reload picks exactly the same
// file (and therefore the same design) as a restart would.
type ConfigWatcher struct {
	mu       sync.Mutex
	path     string // primary config path (jukaconfig.json)
	paths    []string
	lastMod  map[string]time.Time
	running  bool
	stopCh   chan struct{}
	reloadCh chan struct{}
	onReload func(*Config)
	onSource func(string)
	debounce *time.Timer
}

// NewConfigWatcher creates a watcher for the given config path. Its XML sibling
// is watched too, so exporting jukaconfig.xml from the builder is picked up.
func NewConfigWatcher(path string) *ConfigWatcher {
	xmlPath := XMLSidecarPath(path)
	paths := []string{path}
	if xmlPath != path {
		paths = append(paths, xmlPath)
	}
	return &ConfigWatcher{
		path:     path,
		paths:    paths,
		lastMod:  make(map[string]time.Time),
		stopCh:   make(chan struct{}),
		reloadCh: make(chan struct{}, 1),
	}
}

// SetOnReload sets the callback invoked with the reloaded config.
func (cw *ConfigWatcher) SetOnReload(fn func(*Config)) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	cw.onReload = fn
}

// SetOnSource sets an optional callback invoked with the path the config was
// loaded from (useful for diagnostics and UI hints).
func (cw *ConfigWatcher) SetOnSource(fn func(string)) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	cw.onSource = fn
}

// Start begins the background file watcher. It polls the modification times
// every 2 seconds and debounces changes for 500ms to avoid rapid reloads while
// a file is still being written.
func (cw *ConfigWatcher) Start() {
	cw.mu.Lock()
	if cw.running {
		cw.mu.Unlock()
		return
	}
	cw.running = true
	for _, p := range cw.paths {
		if info, err := os.Stat(p); err == nil {
			cw.lastMod[p] = info.ModTime()
		}
	}
	cw.mu.Unlock()

	go cw.watchLoop()
	log.Printf("[hotreload] watching %s for changes", strings.Join(cw.paths, ", "))
}

// Stop halts the background file watcher.
func (cw *ConfigWatcher) Stop() {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	if !cw.running {
		return
	}
	cw.running = false
	close(cw.stopCh)
}

// watchLoop polls the config files every 2 seconds.
func (cw *ConfigWatcher) watchLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-cw.stopCh:
			return
		case <-ticker.C:
			cw.checkFile()
		case <-cw.reloadCh:
			cw.performReload()
		}
	}
}

// checkFile checks whether any watched config file changed since the last check
// (including a file that has just been created - the editor's first XML export,
// for example, which previously was ignored until a restart).
func (cw *ConfigWatcher) checkFile() {
	changed := false

	cw.mu.Lock()
	for _, p := range cw.paths {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		modTime := info.ModTime()
		lastMod, seen := cw.lastMod[p]
		if seen && !modTime.After(lastMod) {
			continue
		}
		cw.lastMod[p] = modTime
		// A file seen for the first time starts a reload only when another
		// watched file already existed, so Start() recording an absent sibling
		// cannot trigger a spurious reload.
		if seen || modTime.After(activeStart) {
			changed = true
		}
	}
	if !changed {
		cw.mu.Unlock()
		return
	}

	// Debounce: wait 500ms after the last detected change
	if cw.debounce != nil {
		cw.debounce.Stop()
	}
	cw.debounce = time.AfterFunc(500*time.Millisecond, func() {
		select {
		case cw.reloadCh <- struct{}{}:
		default:
		}
	})
	cw.mu.Unlock()
}

// performReload loads the newest config and invokes the callback.
func (cw *ConfigWatcher) performReload() {
	cw.mu.Lock()
	fn := cw.onReload
	srcFn := cw.onSource
	primary := cw.path
	cw.mu.Unlock()

	if fn == nil {
		return
	}

	cfg, source, err := LoadXMLConfigWithFallback(primary)
	if err != nil {
		log.Printf("[hotreload] reload failed: %v", err)
		return
	}

	// Validate
	if err := NewConfigValidator().Validate(cfg); err != nil {
		log.Printf("[hotreload] validation failed: %v", err)
		return
	}

	if srcFn != nil {
		srcFn(source)
	}
	fn(cfg)
	log.Printf("[hotreload] config reloaded successfully from %s", source)
}

// activeStart is when the process started; a config file created after this
// point is a genuine edit rather than an already-present sibling.
var activeStart = time.Now()

// globalConfigWatcher is the shared instance used by main.
var globalConfigWatcher *ConfigWatcher

// StartConfigWatcher begins watching the given config file for changes.
// The reloadFn callback is invoked on the main goroutine (inside the event
// loop) when a change is detected.
func StartConfigWatcher(path string, reloadFn func(*Config)) {
	globalConfigWatcher = NewConfigWatcher(path)
	globalConfigWatcher.SetOnReload(reloadFn)
	globalConfigWatcher.Start()
}

// StopConfigWatcher halts the config file watcher.
func StopConfigWatcher() {
	if globalConfigWatcher != nil {
		globalConfigWatcher.Stop()
	}
}
